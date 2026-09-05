//go:build with_wireguard && with_warp

package wireguard

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"

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

const (
	warpStateCreated uint32 = iota
	warpStateStarting
	warpStateReady
	warpStateClosed
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
	underlying *Endpoint
	state      atomic.Uint32
	startMutex sync.Mutex
}

func NewWARPEndpoint(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.WireGuardWARPEndpointOptions) (adapter.Endpoint, error) {
	outboundDialer, err := dialer.NewWithOptions(dialer.Options{
		Context:          ctx,
		Options:          options.DialerOptions,
		RemoteIsDomain:   true,
		ResolverOnDetour: true,
	})
	if err != nil {
		return nil, err
	}

	var provider ProfileProvider
	if options.PrivateKey != "" && len(options.Address) > 0 {
		provider = NewStaticProfileProvider(options)
	} else {
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
	ep.state.Store(warpStateCreated)
	return ep, nil
}

func (w *WARPEndpoint) Start(stage adapter.StartStage) error {
	switch stage {
	case adapter.StartStateInitialize:
		return nil

	case adapter.StartStateStart:
		w.startMutex.Lock()
		defer w.startMutex.Unlock()

		if w.state.Load() == warpStateClosed {
			return E.New("endpoint already closed")
		}

		w.state.Store(warpStateStarting)
		warpConfig, err := w.provider.Load(w.ctx)
		if err != nil {
			w.state.Store(warpStateCreated)
			return E.Cause(err, "load WARP profile")
		}

		wgOpts := warpConfig.WireGuardEndpointOptions()
		underlying, err := NewEndpoint(w.ctx, w.router, w.logger, w.Tag(), wgOpts)
		if err != nil {
			w.state.Store(warpStateCreated)
			return E.Cause(err, "initialize WireGuard endpoint for WARP")
		}

		ep, ok := underlying.(*Endpoint)
		if !ok {
			w.state.Store(warpStateCreated)
			return E.New("unexpected WireGuard endpoint type")
		}

		if err = ep.Start(adapter.StartStateStart); err != nil {
			w.state.Store(warpStateCreated)
			return err
		}
		w.underlying = ep

	case adapter.StartStatePostStart:
		w.startMutex.Lock()
		defer w.startMutex.Unlock()

		if w.underlying != nil {
			if err := w.underlying.Start(adapter.StartStatePostStart); err != nil {
				return err
			}
		}
		w.state.Store(warpStateReady)
	}
	return nil
}

func (w *WARPEndpoint) Close() error {
	w.state.Store(warpStateClosed)
	w.startMutex.Lock()
	defer w.startMutex.Unlock()

	if w.underlying != nil {
		return w.underlying.Close()
	}
	return nil
}

func (w *WARPEndpoint) InterfaceUpdated(ctx context.Context) {
	if w.state.Load() != warpStateReady || w.underlying == nil {
		return
	}
	w.underlying.InterfaceUpdated(ctx)
}

func (w *WARPEndpoint) PreMatchFlow(network string, destination netip.Addr) adapter.PreMatchAction {
	if w.state.Load() != warpStateReady || w.underlying == nil {
		return adapter.PreMatchFlow
	}
	return w.underlying.PreMatchFlow(network, destination)
}

func (w *WARPEndpoint) PortAddresses() (netip.Addr, netip.Addr) {
	if w.state.Load() != warpStateReady || w.underlying == nil {
		return netip.Addr{}, netip.Addr{}
	}
	return w.underlying.PortAddresses()
}

func (w *WARPEndpoint) PortMTU() uint32 {
	if w.state.Load() != warpStateReady || w.underlying == nil {
		return DefaultWarpMTU
	}
	return w.underlying.PortMTU()
}

func (w *WARPEndpoint) AttachReturn(returnPath tun.Return) error {
	if w.state.Load() != warpStateReady || w.underlying == nil {
		return E.New("WARP endpoint is not ready")
	}
	return w.underlying.AttachReturn(returnPath)
}

func (w *WARPEndpoint) DetachReturn(returnPath tun.Return) error {
	if w.state.Load() != warpStateReady || w.underlying == nil {
		return E.New("WARP endpoint is not ready")
	}
	return w.underlying.DetachReturn(returnPath)
}

func (w *WARPEndpoint) JudgeFlow(network uint8, source netip.AddrPort, destination netip.AddrPort, firstPacket []byte) tun.FlowVerdict {
	if w.state.Load() != warpStateReady || w.underlying == nil {
		return tun.FlowVerdict{Action: tun.ActionReject}
	}
	return w.underlying.JudgeFlow(network, source, destination, firstPacket)
}

func (w *WARPEndpoint) NewDNSPacket(payload []byte, source M.Socksaddr, destination M.Socksaddr, writer N.PacketWriter) {
	if w.state.Load() != warpStateReady || w.underlying == nil {
		return
	}
	w.underlying.NewDNSPacket(payload, source, destination, writer)
}

func (w *WARPEndpoint) WritePackets(packets [][]byte) error {
	if w.state.Load() != warpStateReady || w.underlying == nil {
		return E.New("WARP endpoint is not ready")
	}
	return w.underlying.WritePackets(packets)
}

func (w *WARPEndpoint) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	if w.state.Load() != warpStateReady || w.underlying == nil {
		_ = conn.Close()
		return
	}
	w.underlying.NewConnectionEx(ctx, conn, source, destination, onClose)
}

func (w *WARPEndpoint) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	if w.state.Load() != warpStateReady || w.underlying == nil {
		_ = conn.Close()
		return
	}
	w.underlying.NewPacketConnectionEx(ctx, conn, source, destination, onClose)
}

func (w *WARPEndpoint) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if w.state.Load() != warpStateReady || w.underlying == nil {
		return nil, E.New("WARP endpoint is not ready")
	}
	return w.underlying.DialContext(ctx, network, destination)
}

func (w *WARPEndpoint) ListenPacketWithDestination(ctx context.Context, destination M.Socksaddr) (net.PacketConn, netip.Addr, error) {
	if w.state.Load() != warpStateReady || w.underlying == nil {
		return nil, netip.Addr{}, E.New("WARP endpoint is not ready")
	}
	return w.underlying.ListenPacketWithDestination(ctx, destination)
}

func (w *WARPEndpoint) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	if w.state.Load() != warpStateReady || w.underlying == nil {
		return nil, E.New("WARP endpoint is not ready")
	}
	return w.underlying.ListenPacket(ctx, destination)
}

func (w *WARPEndpoint) PreferredDomain(metadata *adapter.InboundContext, domain string) bool {
	if w.state.Load() != warpStateReady || w.underlying == nil {
		return false
	}
	return w.underlying.PreferredDomain(metadata, domain)
}

func (w *WARPEndpoint) PreferredAddress(metadata *adapter.InboundContext, address netip.Addr) bool {
	if w.state.Load() != warpStateReady || w.underlying == nil {
		return false
	}
	return w.underlying.PreferredAddress(metadata, address)
}
