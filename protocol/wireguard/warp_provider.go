//go:build with_wireguard && with_warp

package wireguard

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"math/rand"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/cloudflare"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/ntp"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/filemanager"
)

type ProfileProvider interface {
	Load(ctx context.Context) (*WARPConfig, error)
}

// StaticProfileProvider provides WARPConfig directly from user-supplied options.
type StaticProfileProvider struct {
	options option.WireGuardWARPEndpointOptions
}

func NewStaticProfileProvider(options option.WireGuardWARPEndpointOptions) *StaticProfileProvider {
	return &StaticProfileProvider{options: options}
}

func (p *StaticProfileProvider) Load(ctx context.Context) (*WARPConfig, error) {
	options := p.options

	privateKey, err := ParseKey(options.PrivateKey)
	if err != nil {
		return nil, E.Cause(err, "parse private key")
	}

	var peerPublicKey Key
	if options.PeerPublicKey != "" {
		peerPublicKey, err = ParseKey(options.PeerPublicKey)
		if err != nil {
			return nil, E.Cause(err, "parse peer public key")
		}
	} else {
		peerPublicKey, _ = ParseKey(DefaultWarpPublicKey)
	}

	var preSharedKey *Key
	if options.PreSharedKey != "" {
		psk, pskErr := ParseKey(options.PreSharedKey)
		if pskErr != nil {
			return nil, E.Cause(pskErr, "parse pre-shared key")
		}
		preSharedKey = &psk
	}

	reserved, err := ParseReserved(options.Reserved)
	if err != nil {
		return nil, err
	}

	if len(options.Address) == 0 {
		return nil, E.New("missing local address in static WARP config")
	}

	return &WARPConfig{
		PrivateKey:                  privateKey,
		PeerPublicKey:               peerPublicKey,
		PreSharedKey:                preSharedKey,
		Address:                     options.Address,
		Reserved:                    reserved,
		Server:                      options.Server,
		ServerPort:                  options.ServerPort,
		MTU:                         options.MTU,
		Workers:                     options.Workers,
		UDPTimeout:                  time.Duration(options.UDPTimeout),
		PersistentKeepaliveInterval: options.PersistentKeepaliveInterval,
		System:                      options.System,
		Name:                        options.Name,
		ListenPort:                  options.ListenPort,
		DialerOptions:               options.DialerOptions,
		BootstrapResolver:           options.BootstrapResolver,
	}, nil
}

// CloudflareProfileProvider retrieves or registers a profile with Cloudflare.
type CloudflareProfileProvider struct {
	tag            string
	options        option.WireGuardWARPEndpointOptions
	logger         log.ContextLogger
	outboundDialer N.Dialer
}

func NewCloudflareProfileProvider(tag string, options option.WireGuardWARPEndpointOptions, logger log.ContextLogger, outboundDialer N.Dialer) *CloudflareProfileProvider {
	return &CloudflareProfileProvider{
		tag:            tag,
		options:        options,
		logger:         logger,
		outboundDialer: outboundDialer,
	}
}

func (p *CloudflareProfileProvider) Load(ctx context.Context) (*WARPConfig, error) {
	options := p.options
	provision := options.Provision
	if provision == nil {
		provision = &option.WARPProvisionOptions{}
	}

	cacheFile := service.FromContext[adapter.CacheFile](ctx)
	storagePath := provision.CachePath
	if storagePath == "" {
		storagePath = filemanager.BasePath(ctx, "warp.json")
	}

	var storedProfile *cloudflare.StoredProfile
	if !provision.Recreate {
		if cacheFile != nil {
			if dataStr := cacheFile.LoadWarp(p.tag); dataStr != "" {
				var cached cloudflare.StoredProfile
				if err := json.Unmarshal([]byte(dataStr), &cached); err == nil && cached.Credentials.PrivateKey != "" && len(cached.Tunnel.Address) > 0 {
					p.logger.Info("loaded cached Cloudflare WARP profile from cache database")
					storedProfile = &cached
				}
			}
		}
		if storedProfile == nil {
			if loaded, err := cloudflare.LoadProfile(storagePath); err == nil {
				p.logger.Info("loaded cached Cloudflare WARP profile from ", storagePath)
				storedProfile = loaded
			}
		}
	}

	if storedProfile == nil {
		p.logger.Info("generating new Cloudflare WARP account profile...")
		privateKey, err := GeneratePrivateKey()
		if err != nil {
			return nil, E.Cause(err, "generate private key")
		}

		client := cloudflare.NewClient(p.makeHTTPClient(ctx))
		newProfile, err := client.Register(ctx, privateKey.String(), privateKey.PublicKey().String(), provision.License)
		if err != nil {
			return nil, E.Cause(err, "register Cloudflare WARP account")
		}

		storedProfile = newProfile
		p.saveProfile(ctx, cacheFile, storagePath, storedProfile)
	}

	// Translate StoredProfile to trusted WARPConfig
	privateKey, err := ParseKey(storedProfile.Credentials.PrivateKey)
	if err != nil {
		return nil, E.Cause(err, "parse stored private key")
	}

	var peerPublicKey Key
	if options.PeerPublicKey != "" {
		peerPublicKey, err = ParseKey(options.PeerPublicKey)
		if err != nil {
			return nil, E.Cause(err, "parse peer public key")
		}
	} else if storedProfile.Tunnel.PeerPublicKey != "" {
		peerPublicKey, err = ParseKey(storedProfile.Tunnel.PeerPublicKey)
		if err != nil {
			peerPublicKey, _ = ParseKey(DefaultWarpPublicKey)
		}
	} else {
		peerPublicKey, _ = ParseKey(DefaultWarpPublicKey)
	}

	var preSharedKey *Key
	if options.PreSharedKey != "" {
		psk, pskErr := ParseKey(options.PreSharedKey)
		if pskErr != nil {
			return nil, E.Cause(pskErr, "parse pre-shared key")
		}
		preSharedKey = &psk
	}

	var addresses []netip.Prefix
	for _, addrStr := range storedProfile.Tunnel.Address {
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
			addresses = append(addresses, prefix)
		}
	}
	if len(addresses) == 0 {
		return nil, E.New("no valid address in stored WARP profile")
	}

	reserved, err := ParseReserved(options.Reserved)
	if err != nil {
		return nil, err
	}

	serverHost := options.Server
	serverPort := options.ServerPort
	if serverHost == "" && storedProfile.Tunnel.PeerEndpoint != "" {
		serverHost = storedProfile.Tunnel.PeerEndpoint
		if len(storedProfile.Tunnel.PeerPorts) > 0 {
			serverPort = uint16(storedProfile.Tunnel.PeerPorts[rand.Intn(len(storedProfile.Tunnel.PeerPorts))])
		}
	}

	return &WARPConfig{
		PrivateKey:                  privateKey,
		PeerPublicKey:               peerPublicKey,
		PreSharedKey:                preSharedKey,
		Address:                     addresses,
		Reserved:                    reserved,
		Server:                      serverHost,
		ServerPort:                  serverPort,
		MTU:                         options.MTU,
		Workers:                     options.Workers,
		UDPTimeout:                  time.Duration(options.UDPTimeout),
		PersistentKeepaliveInterval: options.PersistentKeepaliveInterval,
		System:                      options.System,
		Name:                        options.Name,
		ListenPort:                  options.ListenPort,
		DialerOptions:               options.DialerOptions,
		BootstrapResolver:           options.BootstrapResolver,
	}, nil
}

func (p *CloudflareProfileProvider) saveProfile(ctx context.Context, cacheFile adapter.CacheFile, storagePath string, profile *cloudflare.StoredProfile) {
	if cacheFile != nil {
		if data, err := json.Marshal(profile); err == nil {
			if saveErr := cacheFile.SaveWarp(p.tag, string(data)); saveErr != nil {
				p.logger.Warn("failed to save WARP profile to cache database: ", saveErr)
			}
		}
	}
	if err := cloudflare.AtomicSaveProfile(storagePath, profile); err != nil {
		p.logger.Warn("failed to save WARP profile to file: ", err)
	} else {
		p.logger.Info("Cloudflare WARP profile saved to ", storagePath)
	}
}

func (p *CloudflareProfileProvider) makeHTTPClient(ctx context.Context) *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			ForceAttemptHTTP2: true,
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				return p.outboundDialer.DialContext(ctx, network, M.ParseSocksaddr(address))
			},
			TLSClientConfig: &tls.Config{
				RootCAs: adapter.RootPoolFromContext(ctx),
				Time:    ntp.TimeFuncFromContext(ctx),
			},
		},
	}
}
