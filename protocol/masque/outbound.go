//go:build with_masque

package masque

import (
	"context"
	"crypto/ecdsa"
	stdtls "crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/quic-go"
	"github.com/sagernet/quic-go/http3"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing-box/common/tls"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/route/rule"
	"github.com/sagernet/sing-box/transport/masque"
	"github.com/sagernet/sing-box/transport/wireguard"
	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

const (
	reconnectDelayMin = 1 * time.Second
	reconnectDelayMax = 30 * time.Second
)

var (
	_ adapter.OutboundWithPreferredRoutes = (*Outbound)(nil)
	_ adapter.InterfaceUpdateListener     = (*Outbound)(nil)
)

func RegisterOutbound(registry *outbound.Registry) {
	outbound.Register[option.MasqueOutboundOptions](registry, C.TypeMasque, NewOutbound)
}

type Outbound struct {
	outbound.Adapter
	ctx                  context.Context
	router               adapter.Router
	dnsRouter            adapter.DNSRouter
	logger               log.ContextLogger
	localAddresses       []netip.Prefix
	started              atomic.Bool
	tunDevice            wireguard.Device
	quicConfig           *quic.Config
	tlsConfig            *stdtls.Config
	dialer               N.Dialer
	uri                  string
	server               M.Socksaddr
	accessToken          string
	runCtx               context.Context
	runCancel            context.CancelFunc
	runMutex             sync.Mutex
	running              atomic.Bool
	// connMu guards quicConn, ipConn, h3Transport, and reconnectDelay.
	connMu               sync.Mutex
	quicConn             *quic.Conn
	ipConn               masque.IPConn
	h3Transport          *http3.Transport
	reconnectDelay       time.Duration
	innerDNSQueryOptions adapter.DNSQueryOptions
}

func NewOutbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.MasqueOutboundOptions) (adapter.Outbound, error) {
	s := &Outbound{
		Adapter:        outbound.NewAdapterWithDialerOptions(C.TypeMasque, tag, []string{N.NetworkTCP, N.NetworkUDP, N.NetworkICMP}, options.DialerOptions),
		ctx:            ctx,
		router:         router,
		dnsRouter:      service.FromContext[adapter.DNSRouter](ctx),
		logger:         logger,
		localAddresses: options.Address,
	}

	outboundDialer, err := dialer.NewWithOptions(dialer.Options{
		Context:        ctx,
		Options:        options.DialerOptions,
		RemoteIsDomain: options.ServerOptions.ServerIsDomain(),
	})
	if err != nil {
		return nil, err
	}
	s.dialer = outboundDialer
	s.server = options.ServerOptions.Build()

	runCtx, runCancel := context.WithCancel(ctx)
	s.runCtx = runCtx
	s.runCancel = runCancel

	privKeyB64, err := base64.StdEncoding.DecodeString(options.PrivateKey)
	if err != nil {
		return nil, E.Cause(err, "decode private key")
	}
	privKey, err := x509.ParseECPrivateKey(privKeyB64)
	if err != nil {
		return nil, E.Cause(err, "parse private key")
	}

	endpointPubKeyB64, err := base64.StdEncoding.DecodeString(options.PublicKey)
	if err != nil {
		return nil, E.Cause(err, "decode public key")
	}
	pubKey, err := x509.ParsePKIXPublicKey(endpointPubKeyB64)
	if err != nil {
		return nil, E.Cause(err, "parse public key")
	}
	ecPubKey, ok := pubKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, E.New("failed to assert public key as ECDSA")
	}

	serverName := options.Server
	if options.TLS != nil && options.TLS.ServerName != "" {
		serverName = options.TLS.ServerName
	}
	if serverName == "" {
		serverName = "consumer-masque.cloudflareclient.com"
	}

	s.uri = options.URI
	if s.uri == "" {
		s.uri = "https://" + serverName + "/"
	}
	s.accessToken = options.AccessToken

	var stdTLSConfig *stdtls.Config
	tlsClient, err := tls.NewClient(ctx, logger, serverName, common.PtrValueOrDefault(options.TLS))
	if err != nil {
		return nil, E.Cause(err, "create TLS client")
	}
	stdTLSConfig, err = tlsClient.STDConfig()
	if err != nil {
		return nil, E.Cause(err, "get standard TLS config")
	}

	masqueTLSConfig, err := masque.PrepareTLSConfig(privKey, ecPubKey, serverName, false)
	if err != nil {
		return nil, E.Cause(err, "prepare MASQUE TLS config")
	}

	stdTLSConfig.Certificates = append(stdTLSConfig.Certificates, masqueTLSConfig.Certificates...)
	if len(stdTLSConfig.NextProtos) == 0 {
		stdTLSConfig.NextProtos = masqueTLSConfig.NextProtos
	}
	if options.TLS == nil || !options.TLS.Insecure {
		stdTLSConfig.VerifyConnection = masqueTLSConfig.VerifyConnection
		stdTLSConfig.InsecureSkipVerify = true
	}

	s.quicConfig = &quic.Config{
		EnableDatagrams:                true,
		KeepAlivePeriod:                options.QUICOptions.KeepAlivePeriod.Build(),
		MaxIdleTimeout:                 options.QUICOptions.IdleTimeout.Build(),
		InitialStreamReceiveWindow:     options.QUICOptions.StreamReceiveWindow.Value(),
		InitialConnectionReceiveWindow: options.QUICOptions.ConnectionReceiveWindow.Value(),
		MaxIncomingStreams:             int64(options.QUICOptions.MaxConcurrentStreams),
		InitialPacketSize:              uint16(options.QUICOptions.InitialPacketSize),
		DisablePathMTUDiscovery:        options.QUICOptions.DisablePathMTUDiscovery,
	}
	if s.quicConfig.KeepAlivePeriod == 0 {
		s.quicConfig.KeepAlivePeriod = 30 * time.Second
	}

	mtu := options.MTU
	if mtu == 0 {
		mtu = 1280
	}
	if len(options.Address) == 0 {
		return nil, E.New("missing local address")
	}

	gso := options.System
	if options.GSO != nil {
		gso = *options.GSO
	}

	tunOptions := wireguard.DeviceOptions{
		Name:        "masque-" + tag,
		MTU:         mtu,
		Address:     options.Address,
		Handler:     s,
		Context:     ctx,
		Logger:      logger,
		UDPTimeout:  5 * time.Minute,
		ICMPTimeout: 30 * time.Second,
		System:      options.System,
		GSO:         gso,
	}
	s.tunDevice, err = wireguard.NewDevice(tunOptions)
	if err != nil {
		return nil, E.Cause(err, "create device")
	}

	s.tlsConfig = stdTLSConfig

	if options.InnerDomainResolver != nil {
		innerDNSOpts, err := adapter.DNSQueryOptionsFrom(ctx, options.InnerDomainResolver)
		if err != nil {
			return nil, E.Cause(err, "inner domain resolver")
		}
		s.innerDNSQueryOptions = innerDNSOpts
	}

	return s, nil
}

func (s *Outbound) Start(stage adapter.StartStage) error {
	switch stage {
	case adapter.StartStateStart:
		if err := s.tunDevice.Start(); err != nil {
			return err
		}
		s.startLoops()
	case adapter.StartStatePostStart:
		s.started.Store(true)
	}
	return nil
}

// startLoops launches the read (MASQUE→TUN) and write (TUN→MASQUE) goroutines.
// They run for the entire outbound lifetime and survive reconnects gracefully.
func (s *Outbound) startLoops() {
	// Read loop: MASQUE → TUN
	// Waits briefly when the connection is nil (reconnecting), then retries.
	go func() {
		for s.runCtx.Err() == nil {
			s.connMu.Lock()
			conn := s.ipConn
			s.connMu.Unlock()

			if conn == nil {
				// Connection not yet established or reconnecting — wait and retry.
				select {
				case <-s.runCtx.Done():
					return
				case <-time.After(100 * time.Millisecond):
				}
				continue
			}

			buf, err := conn.ReadPacket()
			if err != nil {
				if s.runCtx.Err() != nil {
					return
				}
				s.logger.WarnContext(s.runCtx, "read from MASQUE failed: ", err)
				// Mark connection as dead so run() can reconnect.
				if s.running.CompareAndSwap(true, false) {
					s.closeResourcesAndBackoff()
				}
				continue
			}

			if len(buf) > 0 {
				if _, werr := s.tunDevice.Write([][]byte{buf}, 0); werr != nil {
					if s.runCtx.Err() == nil {
						s.logger.WarnContext(s.runCtx, "write to TUN failed: ", werr)
					}
				}
			}
		}
	}()

	// Write loop: TUN → MASQUE
	// Reads packets in batches; drops them while reconnecting (conn == nil).
	go func() {
		const batchSize = 16
		bufs := make([][]byte, batchSize)
		for i := range bufs {
			bufs[i] = make([]byte, 2048)
		}
		sizes := make([]int, batchSize)

		for s.runCtx.Err() == nil {
			n, err := s.tunDevice.Read(bufs, sizes, 0)
			if err != nil {
				if s.runCtx.Err() == nil {
					s.logger.WarnContext(s.runCtx, "read from TUN failed: ", err)
				}
				return
			}

			s.connMu.Lock()
			conn := s.ipConn
			s.connMu.Unlock()

			if conn == nil {
				// Drop packets gracefully during reconnect.
				continue
			}

			for i := 0; i < n; i++ {
				if sizes[i] > 0 {
					if _, werr := conn.WritePacket(bufs[i][:sizes[i]]); werr != nil {
						if s.runCtx.Err() == nil {
							s.logger.WarnContext(s.runCtx, "write to MASQUE failed: ", werr)
						}
						break
					}
				}
			}
		}
	}()
}

// closeResources closes active QUIC/MASQUE connection resources under connMu.
// It is safe to call concurrently and multiple times.
func (s *Outbound) closeResources() {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	s.closeResourcesLocked()
}

func (s *Outbound) closeResourcesLocked() {
	if s.ipConn != nil {
		s.ipConn.Close()
		s.ipConn = nil
	}
	if s.h3Transport != nil {
		s.h3Transport.Close()
		s.h3Transport = nil
	}
	if s.quicConn != nil {
		(*s.quicConn).CloseWithError(0, "")
		s.quicConn = nil
	}
}

// closeResourcesAndBackoff closes the active connection and increases the
// reconnect backoff delay. Called from the read loop when a connection dies.
func (s *Outbound) closeResourcesAndBackoff() {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	s.closeResourcesLocked()
	if s.reconnectDelay == 0 {
		s.reconnectDelay = reconnectDelayMin
	} else {
		s.reconnectDelay *= 2
		if s.reconnectDelay > reconnectDelayMax {
			s.reconnectDelay = reconnectDelayMax
		}
	}
}

func (s *Outbound) Close() error {
	s.started.Store(false)
	s.runCancel()
	s.running.Store(false)
	s.closeResources()
	if s.tunDevice != nil {
		return s.tunDevice.Close()
	}
	return nil
}

// InterfaceUpdated is called by sing-box when the network interface changes
// (e.g., WiFi → mobile data). It forces a reconnect on the next connection attempt.
func (s *Outbound) InterfaceUpdated() {
	s.logger.Debug("network interface updated, resetting MASQUE connection")
	s.running.Store(false)
	s.connMu.Lock()
	s.closeResourcesLocked()
	s.reconnectDelay = 0 // Reset backoff on explicit interface change.
	s.connMu.Unlock()
}

// run ensures a live MASQUE connection exists, reconnecting with exponential
// backoff if a previous connection has died. Safe to call concurrently.
func (s *Outbound) run(ctx context.Context) error {
	if s.running.Load() {
		return nil
	}
	s.runMutex.Lock()
	defer s.runMutex.Unlock()
	if s.running.Load() {
		return nil
	}

	if s.runCtx.Err() != nil {
		return s.runCtx.Err()
	}

	// Apply reconnect backoff if we're recovering from a previous failure.
	s.connMu.Lock()
	delay := s.reconnectDelay
	s.connMu.Unlock()
	if delay > 0 {
		s.logger.InfoContext(ctx, "waiting ", delay, " before MASQUE reconnect")
		select {
		case <-s.runCtx.Done():
			return s.runCtx.Err()
		case <-time.After(delay):
		}
	}

	// Resolve the server address without going through the OS resolver again.
	serverAddr, err := s.resolveServer(ctx)
	if err != nil {
		s.closeResourcesAndBackoff()
		return E.Cause(err, "resolve server address")
	}

	s.logger.DebugContext(ctx, "dialing packet connection to ", s.server)
	packetConn, err := s.dialer.ListenPacket(ctx, s.server)
	if err != nil {
		s.closeResourcesAndBackoff()
		return err
	}

	s.logger.DebugContext(ctx, "dialing QUIC connection to ", serverAddr)
	quicConn, err := quic.Dial(ctx, packetConn, serverAddr, s.tlsConfig, s.quicConfig)
	if err != nil {
		packetConn.Close()
		s.closeResourcesAndBackoff()
		return err
	}

	s.logger.DebugContext(ctx, "sending MASQUE Connect-IP request to ", s.uri)
	h3Transport, ipConn, err := masque.DialConnectIP(ctx, quicConn, s.uri, s.accessToken)
	if err != nil {
		s.logger.ErrorContext(ctx, "failed to negotiate MASQUE: ", err)
		(*quicConn).CloseWithError(0, "")
		s.closeResourcesAndBackoff()
		return err
	}

	s.logger.InfoContext(ctx, "MASQUE connection established")

	// Store new connection and reset backoff atomically under connMu.
	s.connMu.Lock()
	s.quicConn = quicConn
	s.ipConn = ipConn
	s.h3Transport = h3Transport
	s.reconnectDelay = 0
	s.connMu.Unlock()

	s.running.Store(true)
	return nil
}

// resolveServer converts s.server to a *net.UDPAddr without a redundant OS
// DNS lookup. If the server is already an IP it is used directly; if it is a
// domain name the sing-box DNS router is consulted first, then the system
// resolver as a fallback.
func (s *Outbound) resolveServer(ctx context.Context) (*net.UDPAddr, error) {
	if s.server.IsIP() {
		return net.UDPAddrFromAddrPort(s.server.AddrPort()), nil
	}
	addrs, err := s.lookupDestination(ctx, s.server.Fqdn)
	if err != nil {
		return nil, err
	}
	return net.UDPAddrFromAddrPort(netip.AddrPortFrom(addrs[0], s.server.Port)), nil
}

func (s *Outbound) PrepareConnection(network string, source M.Socksaddr, destination M.Socksaddr, routeContext tun.DirectRouteContext, timeout time.Duration) (tun.DirectRouteDestination, error) {
	if !s.started.Load() {
		return nil, E.New("Masque is not ready yet")
	}
	var ipVersion uint8
	if !destination.IsIPv6() {
		ipVersion = 4
	} else {
		ipVersion = 6
	}
	routeDestination, err := s.router.PreMatch(adapter.InboundContext{
		Inbound:     s.Tag(),
		InboundType: s.Type(),
		IPVersion:   ipVersion,
		Network:     network,
		Source:      source,
		Destination: destination,
	}, routeContext, timeout, false)
	if err != nil {
		switch {
		case rule.IsBypassed(err):
			err = nil
		case rule.IsRejected(err):
			s.logger.Trace("reject ", network, " connection from ", source.AddrString(), " to ", destination.AddrString())
		}
	}
	return routeDestination, err
}

func (s *Outbound) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	var metadata adapter.InboundContext
	metadata.Inbound = s.Tag()
	metadata.InboundType = s.Type()
	metadata.Source = source
	for _, localPrefix := range s.localAddresses {
		if localPrefix.Contains(destination.Addr) {
			metadata.OriginDestination = destination
			if destination.Addr.Is4() {
				destination.Addr = netip.AddrFrom4([4]uint8{127, 0, 0, 1})
			} else {
				destination.Addr = netip.IPv6Loopback()
			}
			break
		}
	}
	metadata.Destination = destination
	s.logger.InfoContext(ctx, "inbound connection from ", source)
	s.logger.InfoContext(ctx, "inbound connection to ", metadata.Destination)
	s.router.RouteConnectionEx(ctx, conn, metadata, onClose)
}

func (s *Outbound) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	var metadata adapter.InboundContext
	metadata.Inbound = s.Tag()
	metadata.InboundType = s.Type()
	metadata.Source = source
	metadata.Destination = destination
	for _, localPrefix := range s.localAddresses {
		if localPrefix.Contains(destination.Addr) {
			metadata.OriginDestination = destination
			if destination.Addr.Is4() {
				metadata.Destination.Addr = netip.AddrFrom4([4]uint8{127, 0, 0, 1})
			} else {
				metadata.Destination.Addr = netip.IPv6Loopback()
			}
			conn = bufio.NewNATPacketConn(bufio.NewNetPacketConn(conn), metadata.OriginDestination, metadata.Destination)
		}
	}
	s.logger.InfoContext(ctx, "inbound packet connection from ", source)
	s.logger.InfoContext(ctx, "inbound packet connection to ", destination)
	s.router.RoutePacketConnectionEx(ctx, conn, metadata, onClose)
}

func (s *Outbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	switch network {
	case N.NetworkTCP:
		s.logger.InfoContext(ctx, "outbound connection to ", destination)
	case N.NetworkUDP:
		s.logger.InfoContext(ctx, "outbound packet connection to ", destination)
	}
	if err := s.run(ctx); err != nil {
		return nil, err
	}
	if destination.IsDomain() {
		destinationAddresses, err := s.lookupDestination(ctx, destination.Fqdn)
		if err != nil {
			return nil, err
		}
		return N.DialSerial(ctx, s.tunDevice, network, destination, destinationAddresses)
	} else if !destination.Addr.IsValid() {
		return nil, E.New("invalid destination: ", destination)
	}
	return s.tunDevice.DialContext(ctx, network, destination.Unwrap())
}

func (s *Outbound) ListenPacketWithDestination(ctx context.Context, destination M.Socksaddr) (net.PacketConn, netip.Addr, error) {
	s.logger.InfoContext(ctx, "outbound packet connection to ", destination)
	if err := s.run(ctx); err != nil {
		return nil, netip.Addr{}, err
	}
	if destination.IsDomain() {
		destinationAddresses, err := s.lookupDestination(ctx, destination.Fqdn)
		if err != nil {
			return nil, netip.Addr{}, err
		}
		return N.ListenSerial(ctx, s.tunDevice, destination, destinationAddresses)
	}
	packetConn, err := s.tunDevice.ListenPacket(ctx, destination.Unwrap())
	if err != nil {
		return nil, netip.Addr{}, err
	}
	if destination.IsIP() {
		return packetConn, destination.Addr, nil
	}
	return packetConn, netip.Addr{}, nil
}

func (s *Outbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	packetConn, destinationAddress, err := s.ListenPacketWithDestination(ctx, destination)
	if err != nil {
		return nil, err
	}
	if destinationAddress.IsValid() && destination != M.SocksaddrFrom(destinationAddress, destination.Port) {
		return bufio.NewNATPacketConn(bufio.NewPacketConn(packetConn), M.SocksaddrFrom(destinationAddress, destination.Port), destination), nil
	}
	return packetConn, nil
}

func (s *Outbound) PreferredDomain(domain string) bool {
	return false
}

func (s *Outbound) PreferredAddress(address netip.Addr) bool {
	return false
}

// lookupDestination resolves a domain using the sing-box DNS router first,
// then falls back to the system resolver. All addresses are Unmap()-ed to
// avoid IPv4-in-IPv6 representations being passed to the TUN stack.
func (s *Outbound) lookupDestination(ctx context.Context, domain string) ([]netip.Addr, error) {
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
