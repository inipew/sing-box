package dns

import (
	"context"
	"crypto/tls"
	"math"
	"math/rand"
	"net"
	"sort"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

type TLSConn interface {
	net.Conn
	ConnectionState() tls.ConnectionState
}

type TLSDialer interface {
	DialTLSContext(ctx context.Context, destination M.Socksaddr) (TLSConn, error)
}

func BuildUpstreamSelector(options option.RemoteDNSServerOptions, defaultPort uint16) (*UpstreamSelector, error) {
	addresses := options.ServerAddressOptions()
	if len(addresses) == 0 {
		return nil, E.New("missing server address")
	}
	serverAddrs := make([]M.Socksaddr, 0, len(addresses))
	for _, address := range addresses {
		serverAddr := address.Build()
		if serverAddr.Port == 0 {
			serverAddr.Port = defaultPort
		}
		if !serverAddr.IsValid() {
			return nil, E.New("invalid server address: ", serverAddr)
		}
		serverAddrs = append(serverAddrs, serverAddr)
	}
	strategy := options.UpstreamStrategy
	if strategy == "" {
		strategy = option.UpstreamStrategyRoundRobin
	}
	return NewUpstreamSelector(serverAddrs, strategy), nil
}

func DialContextWithUpstreams(ctx context.Context, dialer N.Dialer, network string, selector *UpstreamSelector) (net.Conn, M.Socksaddr, error) {
	return dialWithUpstreams(ctx, selector, func(dialCtx context.Context, serverAddr M.Socksaddr) (net.Conn, error) {
		return dialer.DialContext(dialCtx, network, serverAddr)
	})
}

func DialTLSWithUpstreams(ctx context.Context, dialer TLSDialer, selector *UpstreamSelector) (TLSConn, M.Socksaddr, error) {
	return dialWithUpstreams(ctx, selector, func(dialCtx context.Context, serverAddr M.Socksaddr) (TLSConn, error) {
		return dialer.DialTLSContext(dialCtx, serverAddr)
	})
}

func dialWithUpstreams[T any](ctx context.Context, selector *UpstreamSelector, dial func(context.Context, M.Socksaddr) (T, error)) (T, M.Socksaddr, error) {
	if selector.strategy == option.UpstreamStrategyParallel {
		return dialParallel(ctx, selector, dial)
	}
	return dialSequential(ctx, selector, dial)
}

type UpstreamSelector struct {
	addresses []M.Socksaddr
	strategy  option.UpstreamStrategy
	index     atomic.Uint32
	metrics   []upstreamMetric
}

type upstreamMetric struct {
	addr    M.Socksaddr
	latency atomic.Int64
}

func NewUpstreamSelector(addresses []M.Socksaddr, strategy option.UpstreamStrategy) *UpstreamSelector {
	selector := &UpstreamSelector{
		addresses: addresses,
		strategy:  strategy,
		metrics:   make([]upstreamMetric, len(addresses)),
	}
	for i, addr := range addresses {
		selector.metrics[i].addr = addr
	}
	if len(addresses) > 1 {
		selector.index.Store(uint32(len(addresses) - 1))
	}
	return selector
}

func (s *UpstreamSelector) Iterate() []M.Socksaddr {
	switch s.strategy {
	case option.UpstreamStrategyRandom:
		result := append([]M.Socksaddr{}, s.addresses...)
		rand.Shuffle(len(result), func(i, j int) {
			result[i], result[j] = result[j], result[i]
		})
		return result
	case option.UpstreamStrategyFastest:
		return s.sortedByLatency()
	case option.UpstreamStrategyFastestRandomTwo:
		sorted := s.sortedByLatency()
		if len(sorted) <= 1 {
			return sorted
		}
		limit := (len(sorted)*2 + 2) / 3
		fast := append([]M.Socksaddr{}, sorted[:limit]...)
		rand.Shuffle(len(fast), func(i, j int) {
			fast[i], fast[j] = fast[j], fast[i]
		})
		return append(fast, sorted[limit:]...)
	case option.UpstreamStrategyParallel:
		return append([]M.Socksaddr{}, s.addresses...)
	default:
		if len(s.addresses) == 1 {
			return s.addresses
		}
		start := int(s.index.Add(1))
		result := make([]M.Socksaddr, len(s.addresses))
		for i := range s.addresses {
			result[i] = s.addresses[(start+i)%len(s.addresses)]
		}
		return result
	}
}

func (s *UpstreamSelector) Record(serverAddr M.Socksaddr, latency time.Duration) {
	if latency <= 0 {
		return
	}
	latencyNS := latency.Nanoseconds()
	for i := range s.metrics {
		if s.metrics[i].addr == serverAddr {
			for {
				current := s.metrics[i].latency.Load()
				if current <= 0 {
					if s.metrics[i].latency.CompareAndSwap(current, latencyNS) {
						return
					}
					continue
				}
				updated := (current*3 + latencyNS) / 4
				if s.metrics[i].latency.CompareAndSwap(current, updated) {
					return
				}
			}
		}
	}
}

func dialSequential[T any](ctx context.Context, selector *UpstreamSelector, dial func(context.Context, M.Socksaddr) (T, error)) (T, M.Socksaddr, error) {
	var zero T
	var lastErr error
	for _, serverAddr := range selector.Iterate() {
		start := time.Now()
		conn, err := dial(ctx, serverAddr)
		if err != nil {
			lastErr = err
			continue
		}
		selector.Record(serverAddr, time.Since(start))
		return conn, serverAddr, nil
	}
	return zero, M.Socksaddr{}, lastErr
}

func dialParallel[T any](ctx context.Context, selector *UpstreamSelector, dial func(context.Context, M.Socksaddr) (T, error)) (T, M.Socksaddr, error) {
	var zero T
	type dialResult struct {
		value      T
		serverAddr M.Socksaddr
		err        error
		latency    time.Duration
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	resultChan := make(chan dialResult, len(selector.addresses))
	for _, serverAddr := range selector.addresses {
		go func(serverAddr M.Socksaddr) {
			start := time.Now()
			conn, err := dial(ctx, serverAddr)
			if err != nil {
				resultChan <- dialResult{err: err}
				return
			}
			resultChan <- dialResult{value: conn, serverAddr: serverAddr, latency: time.Since(start)}
		}(serverAddr)
	}
	var lastErr error
	for i := 0; i < len(selector.addresses); i++ {
		result := <-resultChan
		if result.err != nil {
			lastErr = result.err
			continue
		}
		selector.Record(result.serverAddr, result.latency)
		cancel()
		for j := i + 1; j < len(selector.addresses); j++ {
			extra := <-resultChan
			if extra.err == nil {
				selector.Record(extra.serverAddr, extra.latency)
				if closer, ok := any(extra.value).(interface{ Close() error }); ok {
					closer.Close()
				}
			}
		}
		return result.value, result.serverAddr, nil
	}
	return zero, M.Socksaddr{}, lastErr
}

func (s *UpstreamSelector) sortedByLatency() []M.Socksaddr {
	result := append([]M.Socksaddr{}, s.addresses...)
	if len(result) <= 1 {
		return result
	}
	sort.SliceStable(result, func(i, j int) bool {
		latencyI := s.latencyOf(result[i])
		latencyJ := s.latencyOf(result[j])
		if latencyI == latencyJ {
			return i < j
		}
		return latencyI < latencyJ
	})
	return result
}

func (s *UpstreamSelector) latencyOf(address M.Socksaddr) int64 {
	for i := range s.metrics {
		if s.metrics[i].addr == address {
			latency := s.metrics[i].latency.Load()
			if latency == 0 {
				return math.MaxInt64
			}
			return latency
		}
	}
	return math.MaxInt64
}
