package group

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"

	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/batch"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/x/list"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
)

func RegisterFallback(registry *outbound.Registry) {
	outbound.Register[option.FallbackOutboundOptions](registry, C.TypeFallback, NewFallback)
}

var _ adapter.OutboundGroup = (*Fallback)(nil)

type Fallback struct {
	outbound.Adapter
	ctx                          context.Context
	outbound                     adapter.OutboundManager
	connection                   adapter.ConnectionManager
	logger                       log.ContextLogger
	tags                         []string
	link                         string
	interval                     time.Duration
	idleTimeout                  time.Duration
	timeout                      time.Duration
	fallbackDelay                time.Duration
	group                        *FallbackGroup
	interruptExternalConnections bool
}

func NewFallback(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.FallbackOutboundOptions) (adapter.Outbound, error) {
	outbound := &Fallback{
		Adapter:                      outbound.NewAdapter(C.TypeFallback, tag, []string{N.NetworkTCP, N.NetworkUDP}, options.Outbounds),
		ctx:                          ctx,
		outbound:                     service.FromContext[adapter.OutboundManager](ctx),
		connection:                   service.FromContext[adapter.ConnectionManager](ctx),
		logger:                       logger,
		tags:                         options.Outbounds,
		link:                         options.URL,
		interval:                     time.Duration(options.Interval),
		idleTimeout:                  time.Duration(options.IdleTimeout),
		timeout:                      time.Duration(options.Timeout),
		fallbackDelay:                time.Duration(options.FallbackDelay),
		interruptExternalConnections: options.InterruptExistConnections,
	}
	if len(outbound.tags) == 0 {
		return nil, E.New("missing tags")
	}
	return outbound, nil
}

func (s *Fallback) Start() error {
	outbounds := make([]adapter.Outbound, 0, len(s.tags))
	for i, tag := range s.tags {
		detour, loaded := s.outbound.Outbound(tag)
		if !loaded {
			return E.New("outbound ", i, " not found: ", tag)
		}
		outbounds = append(outbounds, detour)
	}
	group, err := NewFallbackGroup(s.ctx, s.outbound, s.logger, outbounds, s.link, s.interval, s.idleTimeout, s.timeout, s.fallbackDelay, s.interruptExternalConnections)
	if err != nil {
		return err
	}
	s.group = group
	return nil
}

func (s *Fallback) PostStart() error {
	s.group.PostStart()
	return nil
}

func (s *Fallback) Close() error {
	return common.Close(
		common.PtrOrNil(s.group),
	)
}

func (s *Fallback) Now() string {
	if s.group.selectedOutboundTCP != nil {
		return s.group.selectedOutboundTCP.Tag()
	} else if s.group.selectedOutboundUDP != nil {
		return s.group.selectedOutboundUDP.Tag()
	}
	// Fallback to first available outbound so prematch flow works before URL tests complete
	if len(s.tags) > 0 {
		return s.tags[0]
	}
	return ""
}

func (s *Fallback) All() []string {
	return s.tags
}

func (s *Fallback) URLTest(ctx context.Context) (map[string]uint16, error) {
	return s.group.URLTest(ctx)
}

func (s *Fallback) CheckOutbounds() {
	s.group.CheckOutbounds(true)
}

func (s *Fallback) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	s.group.Touch()
	candidates := s.group.getOrderedOutbounds(network)
	if len(candidates) == 0 {
		return nil, E.New("missing supported outbound")
	}

	if len(candidates) == 1 {
		outbound := candidates[0]
		conn, err := outbound.DialContext(ctx, network, destination)
		if err == nil {
			return s.group.interruptGroup.NewConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
		}
		s.logger.ErrorContext(ctx, err)
		s.group.history.DeleteURLTestHistory(RealTag(s.outbound, outbound))
		return nil, err
	}

	type dialResult struct {
		conn     net.Conn
		err      error
		outbound adapter.Outbound
	}

	resultChan := make(chan dialResult, len(candidates))
	dialCtx, cancelAll := context.WithCancel(ctx)
	defer cancelAll()

	var wg sync.WaitGroup
	var activeCount atomic.Int32
	activeCount.Store(int32(len(candidates)))

	for i, outbound := range candidates {
		wg.Add(1)
		go func(idx int, target adapter.Outbound) {
			defer wg.Done()

			if idx > 0 {
				delay := time.Duration(idx) * s.fallbackDelay
				timer := time.NewTimer(delay)
				select {
				case <-dialCtx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}

			select {
			case <-dialCtx.Done():
				return
			default:
			}

			conn, err := target.DialContext(dialCtx, network, destination)
			select {
			case <-dialCtx.Done():
				if conn != nil {
					conn.Close()
				}
				return
			case resultChan <- dialResult{conn: conn, err: err, outbound: target}:
			}
		}(i, outbound)
	}

	var firstErr error
	var failedCount int

	for failedCount < len(candidates) {
		select {
		case <-ctx.Done():
			cancelAll()
			go func() {
				wg.Wait()
				close(resultChan)
				for r := range resultChan {
					if r.conn != nil {
						r.conn.Close()
					}
				}
			}()
			return nil, ctx.Err()
		case res := <-resultChan:
			if res.err != nil {
				failedCount++
				if firstErr == nil {
					firstErr = res.err
				}
				s.logger.ErrorContext(ctx, E.Cause(res.err, "dial fallback attempt for ", res.outbound.Tag()))
				s.group.history.DeleteURLTestHistory(RealTag(s.outbound, res.outbound))
			} else {
				cancelAll()
				go func() {
					wg.Wait()
					close(resultChan)
					for r := range resultChan {
						if r.conn != nil {
							r.conn.Close()
						}
					}
				}()
				return s.group.interruptGroup.NewConn(res.conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
			}
		}
	}

	if firstErr != nil {
		return nil, firstErr
	}
	return nil, E.New("all dial attempts failed")
}

func (s *Fallback) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	s.group.Touch()
	candidates := s.group.getOrderedOutbounds(N.NetworkUDP)
	if len(candidates) == 0 {
		return nil, E.New("missing supported outbound")
	}

	var firstErr error
	for _, outbound := range candidates {
		conn, err := outbound.ListenPacket(ctx, destination)
		if err == nil {
			return s.group.interruptGroup.NewPacketConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
		}
		s.logger.ErrorContext(ctx, E.Cause(err, "listen packet fallback attempt for ", outbound.Tag()))
		s.group.history.DeleteURLTestHistory(RealTag(s.outbound, outbound))
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, E.New("all listen packet attempts failed")
}

func (s *Fallback) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx)
	s.connection.NewConnection(ctx, s, conn, metadata, onClose)
}

func (s *Fallback) NewPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx)
	s.connection.NewPacketConnection(ctx, s, conn, metadata, onClose)
}

type FallbackGroup struct {
	ctx                          context.Context
	outbound                     adapter.OutboundManager
	pause                        pause.Manager
	pauseCallback                *list.Element[pause.Callback]
	logger                       log.Logger
	outbounds                    []adapter.Outbound
	link                         string
	interval                     time.Duration
	idleTimeout                  time.Duration
	timeout                      time.Duration
	fallbackDelay                time.Duration
	history                      *urltest.HistoryStorage
	checking                     atomic.Bool
	selectedOutboundTCP          adapter.Outbound
	selectedOutboundUDP          adapter.Outbound
	interruptGroup               *interrupt.Group
	interruptExternalConnections bool
	access                       sync.Mutex
	ticker                       *time.Ticker
	close                        chan struct{}
	started                      bool
	lastActive                   common.TypedValue[time.Time]
}

func NewFallbackGroup(ctx context.Context, outboundManager adapter.OutboundManager, logger log.Logger, outbounds []adapter.Outbound, link string, interval time.Duration, idleTimeout time.Duration, timeout time.Duration, fallbackDelay time.Duration, interruptExternalConnections bool) (*FallbackGroup, error) {
	if interval == 0 {
		interval = C.DefaultURLTestInterval
	}
	if idleTimeout == 0 {
		idleTimeout = C.DefaultURLTestIdleTimeout
	}
	if interval > idleTimeout {
		return nil, E.New("interval must be less or equal than idle_timeout")
	}
	history := service.PtrFromContext[urltest.HistoryStorage](ctx)
	if history == nil {
		return nil, E.New("missing URL test history storage")
	}
	return &FallbackGroup{
		ctx:                          ctx,
		outbound:                     outboundManager,
		logger:                       logger,
		outbounds:                    outbounds,
		link:                         link,
		interval:                     interval,
		idleTimeout:                  idleTimeout,
		timeout:                      timeout,
		fallbackDelay:                fallbackDelay,
		history:                      history,
		close:                        make(chan struct{}),
		pause:                        service.FromContext[pause.Manager](ctx),
		interruptGroup:               interrupt.NewGroup(),
		interruptExternalConnections: interruptExternalConnections,
	}, nil
}

func (g *FallbackGroup) PostStart() {
	g.access.Lock()
	defer g.access.Unlock()
	g.started = true
	g.lastActive.Store(time.Now())
	go g.CheckOutbounds(false)
}

func (g *FallbackGroup) Touch() {
	if !g.started {
		return
	}
	g.access.Lock()
	defer g.access.Unlock()
	if g.ticker != nil {
		g.lastActive.Store(time.Now())
		return
	}
	ticker := time.NewTicker(g.interval)
	g.ticker = ticker
	g.pauseCallback = pause.RegisterTicker(g.pause, ticker, g.interval, nil)
	go g.loopCheck(ticker, g.close)
}

func (g *FallbackGroup) Close() error {
	g.access.Lock()
	defer g.access.Unlock()
	if g.ticker == nil {
		return nil
	}
	g.ticker.Stop()
	g.ticker = nil
	g.pause.UnregisterCallback(g.pauseCallback)
	g.pauseCallback = nil
	close(g.close)
	return nil
}

// Select chooses the first healthy proxy in the order of outbounds.
func (g *FallbackGroup) Select(network string) (adapter.Outbound, bool) {
	for _, detour := range g.outbounds {
		if !common.Contains(detour.Network(), network) {
			continue
		}
		history := g.history.LoadURLTestHistory(RealTag(g.outbound, detour))
		if history == nil {
			continue
		}
		return detour, true
	}
	for _, detour := range g.outbounds {
		if !common.Contains(detour.Network(), network) {
			continue
		}
		return detour, false
	}
	return nil, false
}

func (g *FallbackGroup) getOrderedOutbounds(network string) []adapter.Outbound {
	var healthy []adapter.Outbound
	var unhealthy []adapter.Outbound
	for _, detour := range g.outbounds {
		if !common.Contains(detour.Network(), network) {
			continue
		}
		history := g.history.LoadURLTestHistory(RealTag(g.outbound, detour))
		if history != nil {
			healthy = append(healthy, detour)
		} else {
			unhealthy = append(unhealthy, detour)
		}
	}
	return append(healthy, unhealthy...)
}

func (g *FallbackGroup) loopCheck(ticker *time.Ticker, closeChan <-chan struct{}) {
	if time.Since(g.lastActive.Load()) > g.interval {
		g.lastActive.Store(time.Now())
		g.CheckOutbounds(false)
	}
	for {
		select {
		case <-closeChan:
			return
		case <-ticker.C:
		}
		if time.Since(g.lastActive.Load()) > g.idleTimeout {
			g.access.Lock()
			if g.ticker == ticker {
				g.ticker.Stop()
				g.ticker = nil
				g.pause.UnregisterCallback(g.pauseCallback)
				g.pauseCallback = nil
			}
			g.access.Unlock()
			return
		}
		g.CheckOutbounds(false)
	}
}

func (g *FallbackGroup) CheckOutbounds(force bool) {
	_, _ = g.urlTest(g.ctx, force)
}

func (g *FallbackGroup) URLTest(ctx context.Context) (map[string]uint16, error) {
	return g.urlTest(ctx, false)
}

func (g *FallbackGroup) urlTest(ctx context.Context, force bool) (map[string]uint16, error) {
	result := make(map[string]uint16)
	if g.checking.Swap(true) {
		return result, nil
	}
	defer g.checking.Store(false)
	b, _ := batch.New(ctx, batch.WithConcurrencyNum[any](10))
	checked := make(map[string]bool)
	var resultAccess sync.Mutex
	for _, detour := range g.outbounds {
		tag := detour.Tag()
		realTag := RealTag(g.outbound, detour)
		if checked[realTag] {
			continue
		}
		history := g.history.LoadURLTestHistory(realTag)
		if !force && history != nil && time.Since(history.Time) < g.interval {
			continue
		}
		checked[realTag] = true
		p, loaded := g.outbound.Outbound(realTag)
		if !loaded {
			continue
		}
		b.Go(realTag, func() (any, error) {
			timeout := g.timeout
			if timeout == 0 {
				timeout = C.TCPTimeout
			}
			testCtx, cancel := context.WithTimeout(g.ctx, timeout)
			defer cancel()
			t, err := urltest.URLTest(testCtx, g.link, p)
			if err != nil {
				g.logger.Debug("outbound ", tag, " unavailable: ", err)
				g.history.DeleteURLTestHistory(realTag)
			} else {
				g.logger.Debug("outbound ", tag, " available: ", t, "ms")
				g.history.StoreURLTestHistory(realTag, &adapter.URLTestHistory{
					Time:  time.Now(),
					Delay: t,
				})
				resultAccess.Lock()
				result[tag] = t
				resultAccess.Unlock()
			}
			return nil, nil
		})
	}
	b.Wait()
	g.performUpdateCheck()
	return result, nil
}

func (g *FallbackGroup) performUpdateCheck() {
	var updated bool
	if outbound, exists := g.Select(N.NetworkTCP); outbound != nil && (g.selectedOutboundTCP == nil || (exists && outbound != g.selectedOutboundTCP)) {
		if g.selectedOutboundTCP != nil {
			updated = true
		}
		g.selectedOutboundTCP = outbound
	}
	if outbound, exists := g.Select(N.NetworkUDP); outbound != nil && (g.selectedOutboundUDP == nil || (exists && outbound != g.selectedOutboundUDP)) {
		if g.selectedOutboundUDP != nil {
			updated = true
		}
		g.selectedOutboundUDP = outbound
	}
	if updated {
		g.interruptGroup.Interrupt(g.interruptExternalConnections)
	}
}
