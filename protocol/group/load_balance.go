package group

import (
	"context"
	"hash/fnv"
	"math/rand"
	"net"
	"sort"
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

func RegisterLoadBalance(registry *outbound.Registry) {
	outbound.Register[option.LoadBalanceOutboundOptions](registry, C.TypeLoadBalance, NewLoadBalance)
}

var _ adapter.OutboundGroup = (*LoadBalance)(nil)

type LoadBalance struct {
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
	group                        *LoadBalanceGroup
	interruptExternalConnections bool
}

func NewLoadBalance(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.LoadBalanceOutboundOptions) (adapter.Outbound, error) {
	outbound := &LoadBalance{
		Adapter:                      outbound.NewAdapter(C.TypeLoadBalance, tag, []string{N.NetworkTCP, N.NetworkUDP}, options.Outbounds),
		ctx:                          ctx,
		outbound:                     service.FromContext[adapter.OutboundManager](ctx),
		connection:                   service.FromContext[adapter.ConnectionManager](ctx),
		logger:                       logger,
		tags:                         options.Outbounds,
		link:                         options.URL,
		interval:                     time.Duration(options.Interval),
		idleTimeout:                  time.Duration(options.IdleTimeout),
		timeout:                      time.Duration(options.Timeout),
		interruptExternalConnections: options.InterruptExistConnections,
	}
	if len(outbound.tags) == 0 {
		return nil, E.New("missing tags")
	}

	group, err := NewLoadBalanceGroup(ctx, outbound.outbound, logger, nil, outbound.link, outbound.interval, outbound.idleTimeout, outbound.timeout, options.Strategy, outbound.interruptExternalConnections)
	if err != nil {
		return nil, err
	}
	outbound.group = group
	return outbound, nil
}

func (s *LoadBalance) Start() error {
	outbounds := make([]adapter.Outbound, 0, len(s.tags))
	for i, tag := range s.tags {
		detour, loaded := s.outbound.Outbound(tag)
		if !loaded {
			return E.New("outbound ", i, " not found: ", tag)
		}
		outbounds = append(outbounds, detour)
	}
	s.group.outbounds = outbounds
	return nil
}

func (s *LoadBalance) PostStart() error {
	s.group.PostStart()
	return nil
}

func (s *LoadBalance) Close() error {
	return common.Close(
		common.PtrOrNil(s.group),
	)
}

func (s *LoadBalance) Now() string {
	// Return the best available outbound by latency for prematch flow.
	// This ensures preMatchFlow in route.go can successfully resolve the outbound.
	if healthy := s.group.getHealthyOutbounds(N.NetworkTCP); len(healthy) > 0 {
		// Return the best (lowest latency) outbound
		var bestTag string
		var bestDelay uint16
		for _, detour := range healthy {
			h := s.group.history.LoadURLTestHistory(RealTag(s.outbound, detour))
			if h != nil && (bestDelay == 0 || h.Delay < bestDelay) {
				bestDelay = h.Delay
				bestTag = detour.Tag()
			}
		}
		if bestTag != "" {
			return bestTag
		}
		return healthy[0].Tag()
	}
	// Fallback to first tag so prematch flow works before URL tests complete
	if len(s.tags) > 0 {
		return s.tags[0]
	}
	return ""
}

func (s *LoadBalance) All() []string {
	return s.tags
}

func (s *LoadBalance) URLTest(ctx context.Context) (map[string]uint16, error) {
	return s.group.URLTest(ctx)
}

func (s *LoadBalance) CheckOutbounds() {
	s.group.CheckOutbounds(true)
}

func (s *LoadBalance) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	s.group.Touch()
	outbound := s.group.SelectTarget(ctx, network, destination)
	if outbound == nil {
		return nil, E.New("missing supported outbound")
	}

	tag := outbound.Tag()
	g := s.group
	g.connMutex.Lock()
	cnt, ok := g.activeConns[tag]
	if !ok {
		var val int32
		cnt = &val
		g.activeConns[tag] = cnt
	}
	g.connMutex.Unlock()

	atomic.AddInt32(cnt, 1)
	decrement := func() {
		atomic.AddInt32(cnt, -1)
	}

	conn, err := outbound.DialContext(ctx, network, destination)
	if err == nil {
		wrappedConn := &LoadBalanceConn{
			Conn:    conn,
			onClose: decrement,
		}
		return s.group.interruptGroup.NewConn(wrappedConn, interrupt.IsExternalConnectionFromContext(ctx)), nil
	}
	decrement()
	s.logger.ErrorContext(ctx, err)
	s.group.history.DeleteURLTestHistory(outbound.Tag())
	return nil, err
}

func (s *LoadBalance) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	s.group.Touch()
	outbound := s.group.SelectTarget(ctx, N.NetworkUDP, destination)
	if outbound == nil {
		return nil, E.New("missing supported outbound")
	}

	tag := outbound.Tag()
	g := s.group
	g.connMutex.Lock()
	cnt, ok := g.activeConns[tag]
	if !ok {
		var val int32
		cnt = &val
		g.activeConns[tag] = cnt
	}
	g.connMutex.Unlock()

	atomic.AddInt32(cnt, 1)
	decrement := func() {
		atomic.AddInt32(cnt, -1)
	}

	conn, err := outbound.ListenPacket(ctx, destination)
	if err == nil {
		wrappedConn := &LoadBalancePacketConn{
			PacketConn: conn,
			onClose:    decrement,
		}
		return s.group.interruptGroup.NewPacketConn(wrappedConn, interrupt.IsExternalConnectionFromContext(ctx)), nil
	}
	decrement()
	s.logger.ErrorContext(ctx, err)
	s.group.history.DeleteURLTestHistory(outbound.Tag())
	return nil, err
}

func (s *LoadBalance) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx)
	s.connection.NewConnection(ctx, s, conn, metadata, onClose)
}

func (s *LoadBalance) NewPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx)
	s.connection.NewPacketConnection(ctx, s, conn, metadata, onClose)
}

type stickySession struct {
	proxy      adapter.Outbound
	assignedAt time.Time
}

type LoadBalanceGroup struct {
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
	strategy                     string
	history                      *urltest.HistoryStorage
	checking                     atomic.Bool
	activeConns                  map[string]*int32
	connMutex                    sync.Mutex
	stickyCache                  map[string]stickySession
	stickyMutex                  sync.RWMutex
	interruptGroup               *interrupt.Group
	interruptExternalConnections bool
	access                       sync.Mutex
	ticker                       *time.Ticker
	close                        chan struct{}
	started                      bool
	lastActive                   common.TypedValue[time.Time]
}

func NewLoadBalanceGroup(ctx context.Context, outboundManager adapter.OutboundManager, logger log.Logger, outbounds []adapter.Outbound, link string, interval time.Duration, idleTimeout time.Duration, timeout time.Duration, strategy string, interruptExternalConnections bool) (*LoadBalanceGroup, error) {
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
	return &LoadBalanceGroup{
		ctx:                          ctx,
		outbound:                     outboundManager,
		logger:                       logger,
		outbounds:                    outbounds,
		link:                         link,
		interval:                     interval,
		idleTimeout:                  idleTimeout,
		timeout:                      timeout,
		strategy:                     strategy,
		history:                      history,
		activeConns:                  make(map[string]*int32),
		stickyCache:                  make(map[string]stickySession),
		close:                        make(chan struct{}),
		pause:                        service.FromContext[pause.Manager](ctx),
		interruptGroup:               interrupt.NewGroup(),
		interruptExternalConnections: interruptExternalConnections,
	}, nil
}

func (g *LoadBalanceGroup) PostStart() {
	g.access.Lock()
	defer g.access.Unlock()
	g.started = true
	g.lastActive.Store(time.Now())
	go g.CheckOutbounds(false)
}

func (g *LoadBalanceGroup) Touch() {
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

func (g *LoadBalanceGroup) Close() error {
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

func (g *LoadBalanceGroup) getHealthyOutbounds(network string) []adapter.Outbound {
	var healthy []adapter.Outbound
	for _, detour := range g.outbounds {
		if !common.Contains(detour.Network(), network) {
			continue
		}
		history := g.history.LoadURLTestHistory(RealTag(g.outbound, detour))
		if history != nil {
			healthy = append(healthy, detour)
		}
	}
	return healthy
}

func (g *LoadBalanceGroup) fallbackOutbound(network string) adapter.Outbound {
	for _, detour := range g.outbounds {
		if common.Contains(detour.Network(), network) {
			return detour
		}
	}
	return nil
}

func (g *LoadBalanceGroup) SelectTarget(ctx context.Context, network string, destination M.Socksaddr) adapter.Outbound {
	healthy := g.getHealthyOutbounds(network)
	if len(healthy) == 0 {
		return g.fallbackOutbound(network)
	}

	strategy := g.strategy
	if strategy == "" {
		strategy = "round-robin"
	}

	switch strategy {
	case "consistent-hashing":
		return g.selectConsistentHashing(healthy, destination.String())
	case "sticky-sessions":
		inboundCtx := adapter.ContextFrom(ctx)
		var sourceAddr string
		if inboundCtx != nil {
			sourceAddr = inboundCtx.Source.String()
		} else {
			return g.selectConsistentHashing(healthy, destination.String())
		}
		return g.selectStickySessions(healthy, sourceAddr, destination.String())
	case "round-robin":
		fallthrough
	default:
		return g.selectRoundRobin(healthy)
	}
}

func (g *LoadBalanceGroup) selectRoundRobin(healthy []adapter.Outbound) adapter.Outbound {
	var totalWeight uint32
	weights := make([]uint32, len(healthy))
	for i, detour := range healthy {
		history := g.history.LoadURLTestHistory(RealTag(g.outbound, detour))
		weight := uint32(1)
		if history != nil && history.Delay > 0 {
			w := 2000 / uint32(history.Delay)
			if w > 40 {
				w = 40
			}
			if w < 1 {
				w = 1
			}
			weight = w
		} else if history != nil {
			weight = 40
		}
		weights[i] = weight
		totalWeight += weight
	}

	r := uint32(rand.Int31n(int32(totalWeight)))
	var current uint32
	for i, w := range weights {
		current += w
		if r < current {
			return healthy[i]
		}
	}
	return healthy[0]
}

func (g *LoadBalanceGroup) selectConsistentHashing(healthy []adapter.Outbound, target string) adapter.Outbound {
	type ringNode struct {
		hash  uint32
		proxy adapter.Outbound
	}
	var ring []ringNode

	for _, detour := range healthy {
		history := g.history.LoadURLTestHistory(RealTag(g.outbound, detour))
		weight := 5
		if history != nil && history.Delay > 0 {
			w := 2000 / int(history.Delay)
			if w > 100 {
				w = 100
			}
			if w < 2 {
				w = 2
			}
			weight = w
		} else if history != nil {
			weight = 100
		}

		tag := detour.Tag()
		for v := 0; v < weight; v++ {
			vnodeStr := tag + "#vnode" + string(rune(v))
			h := fnvHash(vnodeStr)
			ring = append(ring, ringNode{hash: h, proxy: detour})
		}
	}

	sort.Slice(ring, func(i, j int) bool {
		return ring[i].hash < ring[j].hash
	})

	targetHash := fnvHash(target)
	idx := sort.Search(len(ring), func(i int) bool {
		return ring[i].hash >= targetHash
	})
	if idx == len(ring) {
		idx = 0
	}
	return ring[idx].proxy
}

func (g *LoadBalanceGroup) selectStickySessions(healthy []adapter.Outbound, source string, target string) adapter.Outbound {
	sessionKey := source + "=>" + target

	g.stickyMutex.Lock()
	now := time.Now()
	for k, sess := range g.stickyCache {
		if now.Sub(sess.assignedAt) > 10*time.Minute {
			delete(g.stickyCache, k)
		}
	}

	sess, found := g.stickyCache[sessionKey]
	g.stickyMutex.Unlock()

	if found {
		isHealthy := false
		for _, detour := range healthy {
			if detour.Tag() == sess.proxy.Tag() {
				isHealthy = true
				break
			}
		}
		if isHealthy {
			tag := sess.proxy.Tag()
			g.connMutex.Lock()
			cntPtr, ok := g.activeConns[tag]
			g.connMutex.Unlock()
			if ok && atomic.LoadInt32(cntPtr) < 50 {
				return sess.proxy
			}
		}
	}

	var bestProxy adapter.Outbound
	var minConns int32 = -1

	for _, detour := range healthy {
		tag := detour.Tag()
		g.connMutex.Lock()
		cntPtr, ok := g.activeConns[tag]
		if !ok {
			var val int32
			cntPtr = &val
			g.activeConns[tag] = cntPtr
		}
		g.connMutex.Unlock()

		conns := atomic.LoadInt32(cntPtr)
		if minConns == -1 || conns < minConns {
			minConns = conns
			bestProxy = detour
		}
	}

	if bestProxy != nil {
		g.stickyMutex.Lock()
		g.stickyCache[sessionKey] = stickySession{
			proxy:      bestProxy,
			assignedAt: time.Now(),
		}
		g.stickyMutex.Unlock()
		return bestProxy
	}

	return healthy[0]
}

func fnvHash(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}

func (g *LoadBalanceGroup) loopCheck(ticker *time.Ticker, closeChan <-chan struct{}) {
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

func (g *LoadBalanceGroup) CheckOutbounds(force bool) {
	_, _ = g.urlTest(g.ctx, force)
}

func (g *LoadBalanceGroup) URLTest(ctx context.Context) (map[string]uint16, error) {
	return g.urlTest(ctx, false)
}

func (g *LoadBalanceGroup) urlTest(ctx context.Context, force bool) (map[string]uint16, error) {
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
	return result, nil
}

type LoadBalanceConn struct {
	net.Conn
	onClose func()
	once    sync.Once
}

func (c *LoadBalanceConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.onClose)
	return err
}

type LoadBalancePacketConn struct {
	net.PacketConn
	onClose func()
	once    sync.Once
}

func (c *LoadBalancePacketConn) Close() error {
	err := c.PacketConn.Close()
	c.once.Do(c.onClose)
	return err
}
