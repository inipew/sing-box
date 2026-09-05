//go:build with_wireguard && with_warp

package wireguard

import (
	"context"
	"net"
	"net/netip"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/common/dialer"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-tun"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

var (
	_ adapter.Endpoint                    = (*WARPEndpoint)(nil)
	_ adapter.OutboundWithPreferredRoutes = (*WARPEndpoint)(nil)
	_ adapter.InterfaceUpdateListener     = (*WARPEndpoint)(nil)
	_ dialer.PacketDialerWithDestination  = (*WARPEndpoint)(nil)
)

func RegisterWARPEndpoint(registry *endpoint.Registry) {
	endpoint.Register[option.WireGuardWARPEndpointOptions](registry, C.TypeWarp, NewWARPEndpoint)
}

type WARPEndpoint struct {
	endpoint.Adapter
	ctx        context.Context
	router     adapter.Router
	logger     log.ContextLogger
	options    option.WireGuardWARPEndpointOptions
	provider   ProfileProvider
	access     sync.RWMutex
	underlying *Endpoint
	started    bool
	ready      bool
	closed     bool
}

func NewWARPEndpoint(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.WireGuardWARPEndpointOptions) (adapter.Endpoint, error) {
	hasStaticProfile := options.PrivateKey != "" || len(options.Address) > 0
	var provider ProfileProvider
	if hasStaticProfile {
		if options.PrivateKey == "" {
			return nil, E.New("missing private_key in static WARP configuration")
		}
		if len(options.Address) == 0 {
			return nil, E.New("missing address in static WARP configuration")
		}
		provider = NewStaticProfileProvider(options)
	} else {
		outboundDialer, err := dialer.NewWithOptions(dialer.Options{
			Context:          ctx,
			Options:          options.DialerOptions,
			RemoteIsDomain:   true,
			ResolverOnDetour: true,
		})
		if err != nil {
			return nil, err
		}
		provider = NewCloudflareProfileProvider(tag, options, logger, outboundDialer)
	}

	var dependencies []string
	if options.Detour != "" {
		dependencies = append(dependencies, options.Detour)
	}

	ep := &WARPEndpoint{
		Adapter:  endpoint.NewAdapter(C.TypeWarp, tag, []string{N.NetworkTCP, N.NetworkUDP, N.NetworkICMP}, dependencies),
		ctx:      ctx,
		router:   router,
		logger:   logger,
		options:  options,
		provider: provider,
	}
	return ep, nil
}

func (w *WARPEndpoint) Start(stage adapter.StartStage) error {
	switch stage {
	case adapter.StartStateInitialize:
		return nil
	case adapter.StartStateStart:
		w.access.Lock()
		defer w.access.Unlock()
		if w.closed {
			return E.New("endpoint already closed")
		}
		if w.started {
			return nil
		}
		warpConfig, err := w.provider.Load(w.ctx)
		if err != nil {
			return E.Cause(err, "load WARP profile")
		}

		wgOpts := warpConfig.WireGuardEndpointOptions()
		underlying, err := NewEndpoint(w.ctx, w.router, w.logger, w.Tag(), wgOpts)
		if err != nil {
			return E.Cause(err, "initialize WireGuard endpoint for WARP")
		}

		ep, ok := underlying.(*Endpoint)
		if !ok {
			_ = underlying.Close()
			return E.New("unexpected WireGuard endpoint type")
		}

		if err = ep.Start(adapter.StartStateStart); err != nil {
			_ = ep.Close()
			return err
		}
		w.underlying = ep
		w.started = true
	case adapter.StartStatePostStart:
		w.access.Lock()
		defer w.access.Unlock()
		if w.closed {
			return E.New("endpoint already closed")
		}
		if !w.started || w.underlying == nil {
			return E.New("WARP endpoint has not completed start stage")
		}
		if w.ready {
			return nil
		}
		if err := w.underlying.Start(adapter.StartStatePostStart); err != nil {
			_ = w.underlying.Close()
			w.underlying = nil
			w.started = false
			return err
		}
		w.ready = true
	}
	return nil
}

func (w *WARPEndpoint) Close() error {
	w.access.Lock()
	defer w.access.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	w.started = false
	w.ready = false
	underlying := w.underlying
	w.underlying = nil
	if underlying == nil {
		return nil
	}
	return underlying.Close()
}

func (w *WARPEndpoint) acquire() (*Endpoint, func(), bool) {
	w.access.RLock()
	if !w.ready || w.underlying == nil {
		w.access.RUnlock()
		return nil, nil, false
	}
	return w.underlying, w.access.RUnlock, true
}

func (w *WARPEndpoint) InterfaceUpdated(ctx context.Context) {
	underlying, release, loaded := w.acquire()
	if !loaded {
		return
	}
	defer release()
	underlying.InterfaceUpdated(ctx)
}

func (w *WARPEndpoint) PreMatchFlow(network string, destination netip.Addr) adapter.PreMatchAction {
	underlying, release, loaded := w.acquire()
	if !loaded {
		return adapter.PreMatchFlow
	}
	defer release()
	return underlying.PreMatchFlow(network, destination)
}

func (w *WARPEndpoint) PortAddresses() (netip.Addr, netip.Addr) {
	underlying, release, loaded := w.acquire()
	if !loaded {
		return netip.Addr{}, netip.Addr{}
	}
	defer release()
	return underlying.PortAddresses()
}

func (w *WARPEndpoint) PortMTU() uint32 {
	underlying, release, loaded := w.acquire()
	if !loaded {
		return DefaultWarpMTU
	}
	defer release()
	return underlying.PortMTU()
}

func (w *WARPEndpoint) AttachReturn(returnPath tun.Return) error {
	underlying, release, loaded := w.acquire()
	if !loaded {
		return E.New("WARP endpoint is not ready")
	}
	defer release()
	return underlying.AttachReturn(returnPath)
}

func (w *WARPEndpoint) DetachReturn(returnPath tun.Return) error {
	underlying, release, loaded := w.acquire()
	if !loaded {
		return E.New("WARP endpoint is not ready")
	}
	defer release()
	return underlying.DetachReturn(returnPath)
}

func (w *WARPEndpoint) JudgeFlow(network uint8, source netip.AddrPort, destination netip.AddrPort, firstPacket []byte) tun.FlowVerdict {
	underlying, release, loaded := w.acquire()
	if !loaded {
		return tun.FlowVerdict{Action: tun.ActionReject}
	}
	defer release()
	return underlying.JudgeFlow(network, source, destination, firstPacket)
}

func (w *WARPEndpoint) NewDNSPacket(payload []byte, source M.Socksaddr, destination M.Socksaddr, writer N.PacketWriter) {
	underlying, release, loaded := w.acquire()
	if !loaded {
		return
	}
	defer release()
	underlying.NewDNSPacket(payload, source, destination, writer)
}

func (w *WARPEndpoint) WritePackets(packets [][]byte) error {
	underlying, release, loaded := w.acquire()
	if !loaded {
		return E.New("WARP endpoint is not ready")
	}
	defer release()
	return underlying.WritePackets(packets)
}

func (w *WARPEndpoint) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	underlying, release, loaded := w.acquire()
	if !loaded {
		_ = conn.Close()
		return
	}
	defer release()
	underlying.NewConnectionEx(ctx, conn, source, destination, onClose)
}

func (w *WARPEndpoint) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	underlying, release, loaded := w.acquire()
	if !loaded {
		_ = conn.Close()
		return
	}
	defer release()
	underlying.NewPacketConnectionEx(ctx, conn, source, destination, onClose)
}

func (w *WARPEndpoint) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	underlying, release, loaded := w.acquire()
	if !loaded {
		return nil, E.New("WARP endpoint is not ready")
	}
	defer release()
	return underlying.DialContext(ctx, network, destination)
}

func (w *WARPEndpoint) ListenPacketWithDestination(ctx context.Context, destination M.Socksaddr) (net.PacketConn, netip.Addr, error) {
	underlying, release, loaded := w.acquire()
	if !loaded {
		return nil, netip.Addr{}, E.New("WARP endpoint is not ready")
	}
	defer release()
	return underlying.ListenPacketWithDestination(ctx, destination)
}

func (w *WARPEndpoint) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	underlying, release, loaded := w.acquire()
	if !loaded {
		return nil, E.New("WARP endpoint is not ready")
	}
	defer release()
	return underlying.ListenPacket(ctx, destination)
}

func (w *WARPEndpoint) PreferredDomain(metadata *adapter.InboundContext, domain string) bool {
	underlying, release, loaded := w.acquire()
	if !loaded {
		return false
	}
	defer release()
	return underlying.PreferredDomain(metadata, domain)
}

func (w *WARPEndpoint) PreferredAddress(metadata *adapter.InboundContext, address netip.Addr) bool {
	underlying, release, loaded := w.acquire()
	if !loaded {
		return false
	}
	defer release()
	return underlying.PreferredAddress(metadata, address)
}
