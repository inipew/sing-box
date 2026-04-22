package cloudflare

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	connectip "github.com/Diniboy1123/connect-ip-go"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/common/dialer"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	tun "github.com/sagernet/sing-tun"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/service"
	"github.com/yosida95/uritemplate/v3"
)

var (
	_ adapter.OutboundWithPreferredRoutes = (*Endpoint)(nil)
)

func RegisterEndpoint(registry *endpoint.Registry) {
	endpoint.Register[option.CloudflareEndpointOptions](registry, C.TypeCloudflare, NewEndpoint)
}

type Endpoint struct {
	endpoint.Adapter
	ctx            context.Context
	router         adapter.Router
	dnsRouter      adapter.DNSRouter
	logger         logger.ContextLogger
	options        option.CloudflareEndpointOptions
	localAddresses []netip.Prefix

	dialer N.Dialer

	// Runtime
	connMu       sync.Mutex
	ipConn       *connectip.Conn
	quicConn     any
	h3Transport  *http3.Transport
	udpConn      *net.UDPConn
	cancelPumps  context.CancelFunc
	pumpWG       sync.WaitGroup
	packetBuffer *NetBuffer
}

func NewEndpoint(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.CloudflareEndpointOptions) (adapter.Endpoint, error) {
	ep := &Endpoint{
		Adapter:   endpoint.NewAdapterWithDialerOptions(C.TypeCloudflare, tag, []string{N.NetworkTCP, N.NetworkUDP, N.NetworkICMP}, options.DialerOptions),
		ctx:       ctx,
		router:    router,
		dnsRouter: service.FromContext[adapter.DNSRouter](ctx),
		logger:    logger,
		options:   options,
	}

	if options.RemoteIP.IsValid() {
		ep.localAddresses = append(ep.localAddresses, netip.PrefixFrom(options.RemoteIP, 32))
	}
	if options.RemoteIPv6.IsValid() {
		ep.localAddresses = append(ep.localAddresses, netip.PrefixFrom(options.RemoteIPv6, 128))
	}

	mtu := options.MTU
	if mtu == 0 {
		mtu = 1280
	}
	ep.packetBuffer = NewNetBuffer(int(mtu))

	outboundDialer, err := dialer.NewWithOptions(dialer.Options{
		Context:        ctx,
		Options:        options.DialerOptions,
		RemoteIsDomain: true,
	})
	if err != nil {
		return nil, err
	}
	ep.dialer = outboundDialer

	return ep, nil
}

func (e *Endpoint) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	return e.reconnect()
}

func (e *Endpoint) Close() error {
	e.connMu.Lock()
	defer e.connMu.Unlock()
	e.stopPumps()
	if e.ipConn != nil {
		e.ipConn.Close()
	}
	if e.h3Transport != nil {
		e.h3Transport.Close()
	}
	if e.quicConn != nil {
		e.quicConn.(interface{ CloseWithError(quic.ApplicationErrorCode, string) error }).CloseWithError(0, "")
	}
	if e.udpConn != nil {
		e.udpConn.Close()
	}
	return nil
}

func (e *Endpoint) reconnect() error {
	e.connMu.Lock()
	defer e.connMu.Unlock()

	e.stopPumps()

	privKey, err := decodePrivateKey(e.options.PrivateKey)
	if err != nil {
		return E.Cause(err, "decode private key")
	}
	peerPubKey, err := decodePublicKey(e.options.PublicKey)
	if err != nil {
		return E.Cause(err, "decode public key")
	}
	cert, err := decodeCertificate(e.options.Certificate)
	if err != nil {
		return E.Cause(err, "decode certificate")
	}

	server := e.options.Server
	if server == "" {
		server = "engage.cloudflareclient.com"
	}
	port := e.options.ServerPort
	if port == 0 {
		port = 443
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{
			{
				Certificate: cert,
				PrivateKey:  privKey,
			},
		},
		ServerName:         "consumer-masque.cloudflareclient.com",
		NextProtos:         []string{http3.NextProtoH3},
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return nil
			}
			cert, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return err
			}
			if !cert.PublicKey.(*ecdsa.PublicKey).Equal(peerPubKey) {
				return x509.CertificateInvalidError{Cert: cert, Reason: 10, Detail: "remote endpoint public key mismatch"}
			}
			return nil
		},
	}

	dest := M.ParseSocksaddrHostPort(server, port)
	
	// For now, always use H3
	udpConn, err := e.dialer.ListenPacket(e.ctx, dest)
	if err != nil {
		return err
	}
	e.udpConn = udpConn.(*net.UDPConn)

	quicConfig := &quic.Config{
		EnableDatagrams: true,
	}
	if e.options.UDPTimeout != 0 {
		quicConfig.KeepAlivePeriod = time.Duration(e.options.UDPTimeout)
	}
	if e.options.InitialPacketSize > 0 {
		quicConfig.InitialPacketSize = e.options.InitialPacketSize
		quicConfig.DisablePathMTUDiscovery = true
	}

	remoteAddr, err := net.ResolveUDPAddr("udp", dest.String())
	if err != nil {
		return err
	}

	conn, err := quic.Dial(e.ctx, e.udpConn, remoteAddr, tlsConfig, quicConfig)
	if err != nil {
		return err
	}
	e.quicConn = conn

	tr := &http3.Transport{
		EnableDatagrams:    true,
		AdditionalSettings: map[uint64]uint64{0x276: 1},
		DisableCompression: true,
	}
	e.h3Transport = tr

	hconn := tr.NewClientConn(conn)
	template := uritemplate.MustNew("https://cloudflareaccess.com")
	headers := http.Header{
		"User-Agent": []string{""},
	}
	if e.options.ID != "" {
		headers.Set("cf-access-id", e.options.ID)
	}
	if e.options.Token != "" {
		headers.Set("cf-access-token", e.options.Token)
	}

	ipConn, rsp, err := connectip.Dial(e.ctx, hconn, template, "cf-connect-ip", headers, true)
	if err != nil {
		return err
	}
	if rsp.StatusCode != 200 {
		return E.New("connect-ip handshake failed: ", rsp.Status)
	}
	e.ipConn = ipConn

	e.startPumps()
	return nil
}

func (e *Endpoint) startPumps() {
	pumpCtx, cancel := context.WithCancel(e.ctx)
	e.cancelPumps = cancel
	e.pumpWG.Add(1)
	go e.readPump(pumpCtx)
}

func (e *Endpoint) stopPumps() {
	if e.cancelPumps != nil {
		e.cancelPumps()
		e.pumpWG.Wait()
		e.cancelPumps = nil
	}
}

func (e *Endpoint) readPump(ctx context.Context) {
	defer e.pumpWG.Done()
	localBuf := make([]byte, 2048)
	for {
		n, err := e.ipConn.ReadPacket(localBuf, true)
		if err != nil {
			if ctx.Err() == nil {
				e.logger.Error("read packet error: ", err)
			}
			return
		}
		buffer := buf.NewSize(n)
		buffer.Write(localBuf[:n])
		e.handleInboundPacket(buffer)
	}
}

func (e *Endpoint) handleInboundPacket(buffer *buf.Buffer) {
	packet := buffer.Bytes()
	var source, destination netip.Addr
	if packet[0]>>4 == 4 {
		source = netip.AddrFrom4([4]byte(packet[12:16]))
		destination = netip.AddrFrom4([4]byte(packet[16:20]))
	} else {
		source = netip.AddrFrom16([16]byte(packet[8:24]))
		destination = netip.AddrFrom16([16]byte(packet[24:40]))
	}

	var metadata adapter.InboundContext
	metadata.Inbound = e.Tag()
	metadata.InboundType = e.Type()
	metadata.Source = M.SocksaddrFrom(source, 0)
	metadata.Destination = M.SocksaddrFrom(destination, 0)

	// Simple routing for now
	clonedBuffer := buf.NewSize(buffer.Len())
	clonedBuffer.Write(buffer.Bytes())
	e.router.RoutePacketConnectionEx(e.ctx, &packetConnWrapper{packet: clonedBuffer, ep: e}, metadata, nil)
	buffer.Release()
}

func (e *Endpoint) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return nil, E.New("DialContext not implemented for Cloudflare endpoint")
}

func (e *Endpoint) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, E.New("ListenPacket not implemented for Cloudflare endpoint")
}

func (e *Endpoint) PrepareConnection(network string, source M.Socksaddr, destination M.Socksaddr, routeContext tun.DirectRouteContext, timeout time.Duration) (tun.DirectRouteDestination, error) {
	var ipVersion uint8
	if !destination.IsIPv6() {
		ipVersion = 4
	} else {
		ipVersion = 6
	}
	return e.router.PreMatch(adapter.InboundContext{
		Inbound:     e.Tag(),
		InboundType: e.Type(),
		IPVersion:   ipVersion,
		Network:     network,
		Source:      source,
		Destination: destination,
	}, routeContext, timeout, false)
}

func (e *Endpoint) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	// Logic to encapsulate TCP into MASQUE packets would go here
}

func (e *Endpoint) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	// Logic to encapsulate UDP into MASQUE packets would go here
}

func (e *Endpoint) PreferredDomain(domain string) bool {
	return false
}

func (e *Endpoint) PreferredAddress(address netip.Addr) bool {
	for _, localPrefix := range e.localAddresses {
		if localPrefix.Contains(address) {
			return true
		}
	}
	return false
}

// Helpers

func decodePrivateKey(s string) (*ecdsa.PrivateKey, error) {
	var b []byte
	var err error
	block, _ := pem.Decode([]byte(s))
	if block != nil {
		b = block.Bytes
	} else {
		b, err = base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, err
		}
	}
	return x509.ParseECPrivateKey(b)
}

func decodePublicKey(s string) (*ecdsa.PublicKey, error) {
	var b []byte
	var err error
	block, _ := pem.Decode([]byte(s))
	if block != nil {
		b = block.Bytes
	} else {
		b, err = base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, err
		}
	}
	pub, err := x509.ParsePKIXPublicKey(b)
	if err != nil {
		return nil, err
	}
	ecPub, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, E.New("not an ecdsa public key")
	}
	return ecPub, nil
}

func decodeCertificate(s string) ([][]byte, error) {
	var b []byte
	var err error
	block, _ := pem.Decode([]byte(s))
	if block != nil {
		b = block.Bytes
	} else {
		b, err = base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, err
		}
	}
	return [][]byte{b}, nil
}

type NetBuffer struct {
	capacity int
	buf      sync.Pool
}

func NewNetBuffer(capacity int) *NetBuffer {
	return &NetBuffer{
		capacity: capacity,
		buf: sync.Pool{
			New: func() interface{} {
				b := make([]byte, capacity)
				return &b
			},
		},
	}
}

func (n *NetBuffer) Get() []byte {
	return *(n.buf.Get().(*[]byte))
}

func (n *NetBuffer) Put(buf []byte) {
	if cap(buf) != n.capacity {
		return
	}
	n.buf.Put(&buf)
}

type packetConnWrapper struct {
	packet *buf.Buffer
	ep     *Endpoint
}

func (w *packetConnWrapper) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	buffer.Write(w.packet.Bytes())
	return M.Socksaddr{}, nil
}

func (w *packetConnWrapper) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	w.ep.connMu.Lock()
	defer w.ep.connMu.Unlock()
	if w.ep.ipConn == nil {
		return E.New("not connected")
	}
	_, err := w.ep.ipConn.WritePacket(buffer.Bytes())
	return err
}

func (w *packetConnWrapper) Close() error {
	w.packet.Release()
	return nil
}

func (w *packetConnWrapper) LocalAddr() net.Addr {
	return nil
}

func (w *packetConnWrapper) SetDeadline(t time.Time) error {
	return nil
}

func (w *packetConnWrapper) SetReadDeadline(t time.Time) error {
	return nil
}

func (w *packetConnWrapper) SetWriteDeadline(t time.Time) error {
	return nil
}
