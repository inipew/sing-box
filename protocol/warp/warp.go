package warp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/dialer"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	wg "github.com/sagernet/sing-box/transport/wireguard"
	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/ntp"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/filemanager"
	"golang.org/x/crypto/curve25519"
)

func RegisterOutbound(registry *outbound.Registry) {
	outbound.Register[option.WarpOutboundOptions](registry, C.TypeWarp, NewWarp)
}

var (
	_ adapter.Outbound                   = (*Warp)(nil)
	_ dialer.PacketDialerWithDestination = (*Warp)(nil)
)

type Warp struct {
	outbound.Adapter
	ctx                  context.Context
	dnsRouter            adapter.DNSRouter
	logger               log.ContextLogger
	options              option.WarpOutboundOptions
	outboundDialer       N.Dialer
	endpoint             *wg.Endpoint
	started              atomic.Bool
	innerDNSQueryOptions adapter.DNSQueryOptions
}

type WarpProfile struct {
	PrivateKey    string   `json:"private_key"`
	Address       []string `json:"address"`
	DeviceID      string   `json:"device_id"`
	Token         string   `json:"token"`
	License       string   `json:"license,omitempty"`
	PeerPublicKey string   `json:"peer_public_key,omitempty"`
	PeerEndpoint  string   `json:"peer_endpoint,omitempty"`
	PeerPorts     []int    `json:"peer_ports,omitempty"`
}

func NewWarp(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.WarpOutboundOptions) (adapter.Outbound, error) {
	s := &Warp{
		Adapter:   outbound.NewAdapterWithDialerOptions(C.TypeWarp, tag, []string{N.NetworkTCP, N.NetworkUDP}, options.DialerOptions),
		ctx:       ctx,
		dnsRouter: service.FromContext[adapter.DNSRouter](ctx),
		logger:    logger,
		options:   options,
	}

	outboundDialer, err := dialer.NewWithOptions(dialer.Options{
		Context:          ctx,
		Options:          options.DialerOptions,
		RemoteIsDomain:   true,
		ResolverOnDetour: true,
	})
	if err != nil {
		return nil, err
	}
	s.outboundDialer = outboundDialer

	if options.InnerDomainResolver != nil {
		innerDNSOpts, err := adapter.DNSQueryOptionsFrom(ctx, options.InnerDomainResolver)
		if err != nil {
			return nil, E.Cause(err, "inner domain resolver")
		}
		s.innerDNSQueryOptions = innerDNSOpts
	}

	return s, nil
}

func (s *Warp) Start(stage adapter.StartStage) error {
	switch stage {
	case adapter.StartStateInitialize:
		return nil
	case adapter.StartStateStart:
		if err := s.initialize(); err != nil {
			return err
		}
		return s.endpoint.Start(false)
	case adapter.StartStatePostStart:
		err := s.endpoint.Start(true)
		if err != nil {
			return err
		}
		s.started.Store(true)
	}
	return nil
}

// initialize resolves the WARP profile (from cache, file, or Cloudflare API)
// and constructs the underlying WireGuard endpoint. It is called during
// StartStateStart, after the DNS Transport Manager has been fully started,
// so that DNS lookups required by the HTTP client (e.g. api.cloudflareclient.com)
// can be served by GroupTransport without hitting a "not started" error.
func (s *Warp) initialize() error {
	options := s.options

	var profile *WarpProfile
	if options.PrivateKey != "" {
		// Manual profile — skip registration entirely.
		var addresses []string
		for _, prefix := range options.Address {
			addresses = append(addresses, prefix.String())
		}
		profile = &WarpProfile{
			PrivateKey: options.PrivateKey,
			Address:    addresses,
		}
	} else {
		var err error
		profile, err = s.getOrCreateProfile(options.License)
		if err != nil {
			return E.Cause(err, "get or create WARP profile")
		}
	}

	var localAddresses []netip.Prefix
	for _, addrStr := range profile.Address {
		prefix, parseErr := netip.ParsePrefix(addrStr)
		if parseErr != nil {
			ip, ipErr := netip.ParseAddr(addrStr)
			if ipErr == nil {
				if ip.Is4() {
					prefix = netip.PrefixFrom(ip, 32)
				} else {
					prefix = netip.PrefixFrom(ip, 128)
				}
			}
		}
		if prefix.IsValid() {
			localAddresses = append(localAddresses, prefix)
		}
	}
	if len(localAddresses) == 0 {
		return E.New("no valid local address found for WARP")
	}

	var mtu uint32 = 1280
	if options.MTU != 0 {
		mtu = options.MTU
	}

	var udpTimeout time.Duration
	if options.UDPTimeout != 0 {
		udpTimeout = time.Duration(options.UDPTimeout)
	} else {
		udpTimeout = C.UDPTimeout
	}

	var peerEndpoint M.Socksaddr
	if options.Server != "" {
		peerPort := options.ServerPort
		if peerPort == 0 {
			peerPort = 2408
		}
		peerEndpoint = M.ParseSocksaddrHostPort(options.Server, peerPort)
	} else if profile != nil && profile.PeerEndpoint != "" {
		peerEndpoint = M.ParseSocksaddr(profile.PeerEndpoint)
		if numPorts := len(profile.PeerPorts); numPorts > 0 {
			b := make([]byte, 1)
			_, _ = rand.Read(b)
			peerEndpoint.Port = uint16(profile.PeerPorts[int(b[0])%numPorts])
		} else if peerEndpoint.Port == 0 {
			peerEndpoint.Port = 2408
		}
	} else {
		peerEndpoint = M.ParseSocksaddrHostPort("engage.cloudflareclient.com", 2408)
	}

	var peerPublicKey string
	if options.PublicKey != "" {
		peerPublicKey = options.PublicKey
	} else if profile != nil && profile.PeerPublicKey != "" {
		peerPublicKey = profile.PeerPublicKey
	} else {
		peerPublicKey = "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo="
	}

	networkManager := service.FromContext[adapter.NetworkManager](s.ctx)
	var egressPoolOptions tun.UDPEgressPoolOptions
	if networkManager != nil {
		egressPoolOptions = tun.UDPEgressPoolOptions{
			Logger:           s.logger,
			InterfaceFinder:  networkManager.InterfaceFinder(),
			InterfaceMonitor: networkManager.InterfaceMonitor(),
			IsExempt: func() bool {
				return networkManager.AutoRedirectOutputMark() != 0
			},
		}
	} else {
		egressPoolOptions = tun.UDPEgressPoolOptions{
			Logger: s.logger,
		}
	}

	endpoint, err := wg.NewEndpoint(wg.EndpointOptions{
		Context:           s.ctx,
		Logger:            s.logger,
		EgressPoolOptions: egressPoolOptions,
		Dialer:            s.outboundDialer,
		CreateDialer: func(interfaceName string) N.Dialer {
			return common.Must1(dialer.NewDefault(s.ctx, option.DialerOptions{
				BindInterface: interfaceName,
			}))
		},
		Address:    localAddresses,
		PrivateKey: profile.PrivateKey,
		MTU:        mtu,
		UDPTimeout: udpTimeout,
		ResolvePeer: func(domain string) (netip.Addr, error) {
			if s.dnsRouter != nil {
				addrs, lookupErr := s.dnsRouter.Lookup(s.ctx, domain, s.outboundDialer.(dialer.ResolveDialer).QueryOptions())
				if lookupErr == nil && len(addrs) > 0 {
					return addrs[0], nil
				}
			}
			addrs, lookupErr := net.DefaultResolver.LookupIP(s.ctx, "ip", domain)
			if lookupErr != nil {
				return netip.Addr{}, lookupErr
			}
			if len(addrs) == 0 {
				return netip.Addr{}, E.New("no address found for ", domain)
			}
			// .Unmap() converts IPv4-in-IPv6 (::ffff:x.x.x.x) to proper IPv4.
			addr, ok := netip.AddrFromSlice(addrs[0])
			if !ok {
				return netip.Addr{}, E.New("invalid address resolved for ", domain)
			}
			return addr.Unmap(), nil
		},
		Peers: []wg.PeerOptions{
			{
				Endpoint:                    peerEndpoint,
				PublicKey:                   peerPublicKey,
				PreSharedKey:                options.PreSharedKey,
				AllowedIPs:                  []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")},
				Reserved:                    options.Reserved,
				PersistentKeepaliveInterval: options.PersistentKeepalive,
			},
		},
		Workers: options.Workers,
	})
	if err != nil {
		return E.Cause(err, "create WireGuard endpoint for WARP")
	}

	s.endpoint = endpoint
	return nil
}

func (s *Warp) Close() error {
	s.started.Store(false)
	if s.endpoint != nil {
		return s.endpoint.Close()
	}
	return nil
}

func (s *Warp) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	switch network {
	case N.NetworkTCP:
		s.logger.InfoContext(ctx, "outbound connection to ", destination)
	case N.NetworkUDP:
		s.logger.InfoContext(ctx, "outbound packet connection to ", destination)
	}
	if !s.started.Load() {
		return nil, E.New("WARP is not ready yet")
	}
	if destination.IsDomain() {
		destinationAddresses, err := s.lookupDestination(ctx, destination.Fqdn)
		if err != nil {
			return nil, err
		}
		return N.DialSerial(ctx, s.endpoint, network, destination, destinationAddresses)
	} else if !destination.Addr.IsValid() {
		return nil, E.New("invalid destination: ", destination)
	}
	return s.endpoint.DialContext(ctx, network, destination)
}

func (s *Warp) ListenPacketWithDestination(ctx context.Context, destination M.Socksaddr) (net.PacketConn, netip.Addr, error) {
	s.logger.InfoContext(ctx, "outbound packet connection to ", destination)
	if !s.started.Load() {
		return nil, netip.Addr{}, E.New("WARP is not ready yet")
	}
	if destination.IsDomain() {
		destinationAddresses, err := s.lookupDestination(ctx, destination.Fqdn)
		if err != nil {
			return nil, netip.Addr{}, err
		}
		return N.ListenSerial(ctx, s.endpoint, destination, destinationAddresses)
	}
	packetConn, err := s.endpoint.ListenPacket(ctx, destination)
	if err != nil {
		return nil, netip.Addr{}, err
	}
	if destination.IsIP() {
		return packetConn, destination.Addr, nil
	}
	return packetConn, netip.Addr{}, nil
}

func (s *Warp) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	packetConn, destinationAddress, err := s.ListenPacketWithDestination(ctx, destination)
	if err != nil {
		return nil, err
	}
	if destinationAddress.IsValid() && destination != M.SocksaddrFrom(destinationAddress, destination.Port) {
		return bufio.NewNATPacketConn(bufio.NewPacketConn(packetConn), M.SocksaddrFrom(destinationAddress, destination.Port), destination), nil
	}
	return packetConn, nil
}


// lookupDestination resolves a domain using the configured DNS router first,
// falling back to the system resolver. All addresses are .Unmap()-ed to
// ensure IPv4-mapped IPv6 addresses (::ffff:x.x.x.x) are converted to proper
// IPv4 addresses before being passed to the WireGuard stack.
func (s *Warp) lookupDestination(ctx context.Context, domain string) ([]netip.Addr, error) {
	var destinationAddresses []netip.Addr
	if s.dnsRouter != nil {
		destinationAddresses, _ = s.dnsRouter.Lookup(ctx, domain, s.innerDNSQueryOptions)
	}
	if len(destinationAddresses) == 0 {
		addrs, err := net.DefaultResolver.LookupIP(ctx, "ip", domain)
		if err == nil {
			for _, addr := range addrs {
				if ip, ok := netip.AddrFromSlice(addr); ok {
					destinationAddresses = append(destinationAddresses, ip.Unmap())
				}
			}
		}
	}
	if len(destinationAddresses) == 0 {
		return nil, E.New("failed to resolve domain: ", domain)
	}
	return destinationAddresses, nil
}

func (s *Warp) getOrCreateProfile(license string) (*WarpProfile, error) {
	cacheFile := service.FromContext[adapter.CacheFile](s.ctx)

	var profile *WarpProfile

	if cacheFile != nil {
		if dataStr := cacheFile.LoadWarp(s.Tag()); dataStr != "" {
			var cachedProfile WarpProfile
			if jsonErr := json.Unmarshal([]byte(dataStr), &cachedProfile); jsonErr == nil &&
				cachedProfile.PrivateKey != "" && len(cachedProfile.Address) > 0 {
				s.logger.Info("loaded cached Cloudflare WARP profile from cache database")
				profile = &cachedProfile
			} else if jsonErr != nil {
				s.logger.Warn("cached WARP profile is malformed, will re-register: ", jsonErr)
			}
		}
	} else {
		profilePath := filemanager.BasePath(s.ctx, "warp.json")
		data, err := os.ReadFile(profilePath)
		if err == nil {
			var cachedProfile WarpProfile
			if jsonErr := json.Unmarshal(data, &cachedProfile); jsonErr == nil &&
				cachedProfile.PrivateKey != "" && len(cachedProfile.Address) > 0 {
				s.logger.Info("loaded cached Cloudflare WARP profile from warp.json")
				profile = &cachedProfile
			} else if jsonErr != nil {
				s.logger.Warn("warp.json is malformed, will re-register: ", jsonErr)
			}
		}
	}

	if profile != nil {
		if license != "" && profile.License != license && profile.DeviceID != "" && profile.Token != "" {
			if err := s.updateLicense(profile.DeviceID, profile.Token, license); err == nil {
				profile.License = license
				s.saveProfile(cacheFile, profile)
			} else {
				s.logger.Warn("failed to update Cloudflare WARP license: ", err)
			}
		}
		return profile, nil
	}

	return s.registerNewProfile(cacheFile, license)
}

func (s *Warp) registerNewProfile(cacheFile adapter.CacheFile, license string) (*WarpProfile, error) {
	s.logger.Info("generating new Cloudflare WARP profile...")

	var privateKeyBytes [32]byte
	if _, err := rand.Read(privateKeyBytes[:]); err != nil {
		return nil, err
	}
	// Clamp private key per RFC 7748.
	privateKeyBytes[0] &= 248
	privateKeyBytes[31] &= 127
	privateKeyBytes[31] |= 64

	var publicKeyBytes [32]byte
	curve25519.ScalarBaseMult(&publicKeyBytes, &privateKeyBytes)

	privateKeyBase64 := base64.StdEncoding.EncodeToString(privateKeyBytes[:])
	publicKeyBase64 := base64.StdEncoding.EncodeToString(publicKeyBytes[:])

	payload := map[string]any{
		"key":          publicKeyBase64,
		"install_id":   s.generateRandomHex(22),
		"fcm_token":    "",
		"referrer":     "",
		"warp_enabled": true,
		"tos":          time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		"type":         "Android",
		"locale":       "en_US",
	}

	reqBytes, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, "https://api.cloudflareclient.com/v0a1922/reg", bytes.NewReader(reqBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "okhttp/3.12.1")

	s.logger.Info("registering account on Cloudflare WARP API...")
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, E.Cause(err, "send register request to Cloudflare")
	}
	defer common.Close(resp.Body)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, E.New("failed to register warp, status: ", resp.Status, " body: ", string(bodyBytes))
	}

	var regResp struct {
		ID     string `json:"id"`
		Token  string `json:"token"`
		Config struct {
			Interface struct {
				Addresses struct {
					V4 string `json:"v4"`
					V6 string `json:"v6"`
				} `json:"addresses"`
			} `json:"interface"`
			Peers []struct {
				PublicKey string `json:"public_key"`
				Endpoint  struct {
					Host  string `json:"host"`
					Ports []int  `json:"ports"`
				} `json:"endpoint"`
			} `json:"peers"`
		} `json:"config"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&regResp); err != nil {
		return nil, E.Cause(err, "decode register response")
	}

	var addresses []string
	if regResp.Config.Interface.Addresses.V4 != "" {
		addresses = append(addresses, regResp.Config.Interface.Addresses.V4)
	}
	if regResp.Config.Interface.Addresses.V6 != "" {
		addresses = append(addresses, regResp.Config.Interface.Addresses.V6)
	}
	if len(addresses) == 0 {
		return nil, E.New("no address assigned by Cloudflare WARP")
	}

	var peerPublicKey string
	var peerEndpoint string
	var peerPorts []int
	if len(regResp.Config.Peers) > 0 {
		peerPublicKey = regResp.Config.Peers[0].PublicKey
		peerEndpoint = regResp.Config.Peers[0].Endpoint.Host
		peerPorts = regResp.Config.Peers[0].Endpoint.Ports
	}

	newProfile := &WarpProfile{
		PrivateKey:    privateKeyBase64,
		Address:       addresses,
		DeviceID:      regResp.ID,
		Token:         regResp.Token,
		PeerPublicKey: peerPublicKey,
		PeerEndpoint:  peerEndpoint,
		PeerPorts:     peerPorts,
	}

	if license != "" {
		if err := s.updateLicense(regResp.ID, regResp.Token, license); err == nil {
			newProfile.License = license
		} else {
			s.logger.Warn("failed to apply license during registration: ", err)
		}
	}

	s.saveProfile(cacheFile, newProfile)
	return newProfile, nil
}

func (s *Warp) saveProfile(cacheFile adapter.CacheFile, profile *WarpProfile) {
	profileBytes, _ := json.MarshalIndent(profile, "", "  ")
	if cacheFile != nil {
		if err := cacheFile.SaveWarp(s.Tag(), string(profileBytes)); err != nil {
			s.logger.Warn("failed to save WARP profile to cache database: ", err)
		} else {
			s.logger.Info("Cloudflare WARP profile saved to cache database")
		}
	} else {
		profilePath := filemanager.BasePath(s.ctx, "warp.json")
		if err := os.WriteFile(profilePath, profileBytes, 0o600); err != nil {
			s.logger.Warn("failed to save WARP profile to warp.json: ", err)
		} else {
			s.logger.Info("Cloudflare WARP profile saved to warp.json")
		}
	}
}

func (s *Warp) updateLicense(deviceID, token, license string) error {
	s.logger.Info("updating Cloudflare WARP account license...")
	reqBytes, _ := json.Marshal(map[string]any{"license": license})
	req, err := http.NewRequestWithContext(
		s.ctx,
		http.MethodPut,
		"https://api.cloudflareclient.com/v0a1922/reg/"+deviceID+"/account",
		bytes.NewReader(reqBytes),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "okhttp/3.12.1")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return E.Cause(err, "send update license request to Cloudflare")
	}
	defer common.Close(resp.Body)

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return E.New("failed to update license, status: ", resp.Status, " body: ", string(bodyBytes))
	}
	s.logger.Info("Cloudflare WARP license updated successfully")
	return nil
}

func (s *Warp) generateRandomHex(length int) string {
	b := make([]byte, length)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Warp) httpClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			ForceAttemptHTTP2: true,
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				return s.outboundDialer.DialContext(ctx, network, M.ParseSocksaddr(address))
			},
			TLSClientConfig: &tls.Config{
				RootCAs: adapter.RootPoolFromContext(s.ctx),
				Time:    ntp.TimeFuncFromContext(s.ctx),
			},
		},
	}
}
