package group

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	dnscore "github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	C "github.com/sagernet/sing-box/constant"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"

	mDNS "github.com/miekg/dns"
)

// dispatchMode controls how queries are sent to selected member servers.
type dispatchMode uint8

const (
	// dispatchSequential tries servers one by one until one succeeds.
	dispatchSequential dispatchMode = iota
	// dispatchConcurrent races all selected servers; first success wins.
	dispatchConcurrent
	// dispatchFallback sends to the primary first; after fallbackDelay it
	// concurrently promotes remaining servers (happy-eyeballs style).
	dispatchFallback
)

const (
	defaultFallbackDelay = 300 * time.Millisecond
)

var (
	_ adapter.DNSTransport          = (*GroupTransport)(nil)
	_ adapter.DNSTransportWithStats = (*GroupTransport)(nil)
)

// GroupTransport is a virtual DNS transport that dispatches queries across
// a pool of member transports using a pluggable strategy and dispatch mode.
type GroupTransport struct {
	ctx           context.Context
	tag           string
	logger        log.ContextLogger
	memberTags    []string
	strategyName  string
	mode          dispatchMode
	fallbackDelay time.Duration
	maxRetries    int
	hcOptions     *option.DNSGroupHealthCheckOptions

	access   sync.RWMutex
	members  []adapter.DNSTransport // resolved at Start
	strategy strategySelector
	rtt      *rttEstimator
	hc       *healthChecker
	started  bool
}

// RegisterTransport registers the group transport type with the DNS transport registry.
func RegisterTransport(registry *dnscore.TransportRegistry) {
	dnscore.RegisterTransport[option.GroupDNSServerOptions](registry, C.DNSTypeGroup, NewGroupTransport)
}

// NewGroupTransport creates a new GroupTransport from the given options.
func NewGroupTransport(ctx context.Context, logger log.ContextLogger, tag string, options option.GroupDNSServerOptions) (adapter.DNSTransport, error) {
	if len(options.Servers) == 0 {
		return nil, E.New("dns group[", tag, "]: no member servers specified")
	}

	// Parse and validate mode.
	var mode dispatchMode
	switch strings.ToLower(options.Mode) {
	case "", "sequential":
		mode = dispatchSequential
	case "concurrent":
		mode = dispatchConcurrent
	case "fallback":
		mode = dispatchFallback
	default:
		return nil, fmt.Errorf("dns group[%s]: unknown mode %q (valid: sequential, concurrent, fallback)", tag, options.Mode)
	}

	// Validate strategy early; actual instance is created in Start().
	strategyName := options.Strategy
	if strategyName == "" {
		strategyName = "wp2"
	}
	if _, err := newStrategy(strategyName); err != nil {
		return nil, E.Cause(err, "dns group[", tag, "]")
	}

	// Fallback delay.
	fd := time.Duration(options.FallbackDelay)
	if fd == 0 {
		fd = defaultFallbackDelay
	}

	// RTT sample size from health_check options.
	sampleSize := 0
	if options.HealthCheck != nil {
		sampleSize = options.HealthCheck.SampleSize
	}

	return &GroupTransport{
		ctx:           ctx,
		tag:           tag,
		logger:        logger,
		memberTags:    options.Servers,
		strategyName:  strategyName,
		mode:          mode,
		fallbackDelay: fd,
		maxRetries:    options.MaxRetries,
		hcOptions:     options.HealthCheck,
		rtt:           newRTTEstimator(sampleSize),
	}, nil
}

// ---- adapter.DNSTransport interface ----

func (t *GroupTransport) Type() string { return C.DNSTypeGroup }
func (t *GroupTransport) Tag() string  { return t.tag }

// Dependencies returns member tags so the transport manager starts them first.
func (t *GroupTransport) Dependencies() []string {
	return t.memberTags
}

// Reset resets the underlying connections of all member transports.
func (t *GroupTransport) Reset() {
	t.access.RLock()
	members := t.members
	t.access.RUnlock()
	for _, m := range members {
		m.Reset()
	}
}

// Start resolves member transport references and launches the health checker.
// It is called by the transport manager after all dependencies are started.
func (t *GroupTransport) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}

	// Resolve member transport references from the transport manager in context.
	tm := service.FromContext[adapter.DNSTransportManager](t.ctx)
	if tm == nil {
		return E.New("dns group[", t.tag, "]: DNSTransportManager not found in context")
	}

	members := make([]adapter.DNSTransport, 0, len(t.memberTags))
	for _, memberTag := range t.memberTags {
		transport, loaded := tm.Transport(memberTag)
		if !loaded {
			return E.New("dns group[", t.tag, "]: member transport not found: ", memberTag)
		}
		members = append(members, transport)
	}

	strategy, err := newStrategy(t.strategyName)
	if err != nil {
		return E.Cause(err, "dns group[", t.tag, "]")
	}

	t.access.Lock()
	t.members = members
	t.strategy = strategy
	t.started = true
	t.access.Unlock()

	// Start health checker if configured.
	if t.hcOptions != nil {
		hc := newHealthChecker(t.ctx, members, t.hcOptions, t.rtt, t.logger)
		t.hc = hc
		hc.Start()
	}

	modeStr := [...]string{"sequential", "concurrent", "fallback"}[t.mode]
	t.logger.Info("dns group [", t.tag, "] started with ", len(members),
		" members, strategy=", t.strategyName, ", mode=", modeStr)
	return nil
}

// Close stops the health checker.
func (t *GroupTransport) Close() error {
	t.access.Lock()
	t.started = false
	hc := t.hc
	t.hc = nil
	t.access.Unlock()
	if hc != nil {
		hc.Close()
	}
	return nil
}

// ---- Core dispatch ----

// Exchange dispatches the DNS message according to the configured mode,
// recording per-server RTT for future strategy decisions.
func (t *GroupTransport) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	t.access.RLock()
	members := t.members
	strategy := t.strategy
	started := t.started
	t.access.RUnlock()

	if !started {
		return nil, E.New("dns group[", t.tag, "]: not started")
	}
	if len(members) == 0 {
		return nil, E.New("dns group[", t.tag, "]: no member transports")
	}

	// Build tag→transport index and ordered tag list for the strategy.
	tags := make([]string, len(members))
	tagToTransport := make(map[string]adapter.DNSTransport, len(members))
	for i, m := range members {
		tags[i] = m.Tag()
		tagToTransport[m.Tag()] = m
	}

	selected := strategy.Select(tags, t.rtt)
	if len(selected) == 0 {
		return nil, E.New("dns group[", t.tag, "]: strategy returned no servers")
	}

	switch t.mode {
	case dispatchConcurrent:
		return t.exchangeConcurrent(ctx, message, selected, tagToTransport)
	case dispatchFallback:
		return t.exchangeFallback(ctx, message, selected, tagToTransport)
	default:
		return t.exchangeSequential(ctx, message, selected, tagToTransport)
	}
}

// exchangeSequential tries each selected server in order until one succeeds.
func (t *GroupTransport) exchangeSequential(
	ctx context.Context,
	message *mDNS.Msg,
	selected []string,
	byTag map[string]adapter.DNSTransport,
) (*mDNS.Msg, error) {
	limit := len(selected)
	if t.maxRetries > 0 && t.maxRetries < limit {
		limit = t.maxRetries
	}
	var lastErr error
	for i := 0; i < limit; i++ {
		tag := selected[i]
		transport, ok := byTag[tag]
		if !ok {
			continue
		}
		start := time.Now()
		resp, err := transport.Exchange(ctx, message)
		if err == nil {
			t.rtt.Record(tag, time.Since(start))
			return resp, nil
		}
		if !errors.Is(err, context.Canceled) {
			t.rtt.RecordFailure(tag)
		}
		t.logger.DebugContext(ctx, "dns group[", t.tag, "] sequential: server ", tag, " failed: ", err)
		lastErr = err
	}
	return nil, E.Cause(lastErr, "dns group[", t.tag, "]: all servers failed")
}

// exchangeConcurrent races all selected servers and returns the first
// successful response, cancelling the remaining goroutines.
func (t *GroupTransport) exchangeConcurrent(
	ctx context.Context,
	message *mDNS.Msg,
	selected []string,
	byTag map[string]adapter.DNSTransport,
) (*mDNS.Msg, error) {
	type result struct {
		tag  string
		resp *mDNS.Msg
		rtt  time.Duration
		err  error
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	launched := 0
	ch := make(chan result, len(selected))
	for _, tag := range selected {
		transport, ok := byTag[tag]
		if !ok {
			continue
		}
		launched++
		go func(tag string, transport adapter.DNSTransport) {
			start := time.Now()
			resp, err := transport.Exchange(ctx, message)
			ch <- result{tag: tag, resp: resp, rtt: time.Since(start), err: err}
		}(tag, transport)
	}

	if launched == 0 {
		return nil, E.New("dns group[", t.tag, "]: no valid transports to race")
	}

	received := 0
	var lastErr error
	for received < launched {
		select {
		case r := <-ch:
			received++
			if r.err == nil {
				t.rtt.Record(r.tag, r.rtt)
				cancel() // signal remaining goroutines to stop
				return r.resp, nil
			}
			if !errors.Is(r.err, context.Canceled) {
				t.rtt.RecordFailure(r.tag)
			}
			lastErr = r.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, E.Cause(lastErr, "dns group[", t.tag, "]: all concurrent servers failed")
}

// exchangeFallback sends to the primary server first; if it does not respond
// within fallbackDelay, it concurrently promotes the remaining servers
// (happy-eyeballs style) and returns the first success.
func (t *GroupTransport) exchangeFallback(
	ctx context.Context,
	message *mDNS.Msg,
	selected []string,
	byTag map[string]adapter.DNSTransport,
) (*mDNS.Msg, error) {
	if len(selected) == 1 {
		return t.exchangeSequential(ctx, message, selected, byTag)
	}

	type result struct {
		tag  string
		resp *mDNS.Msg
		rtt  time.Duration
		err  error
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Channel sized for all servers.
	ch := make(chan result, len(selected))

	launch := func(tag string) bool {
		transport, ok := byTag[tag]
		if !ok {
			return false
		}
		go func(tag string, tr adapter.DNSTransport) {
			start := time.Now()
			resp, err := tr.Exchange(ctx, message)
			ch <- result{tag: tag, resp: resp, rtt: time.Since(start), err: err}
		}(tag, transport)
		return true
	}

	// Launch primary immediately.
	primary := selected[0]
	launched := 0
	if launch(primary) {
		launched = 1
	}

	fallbackTimer := time.NewTimer(t.fallbackDelay)
	defer fallbackTimer.Stop()
	fallbackLaunched := false

	launchFallbacks := func() {
		if fallbackLaunched {
			return
		}
		fallbackLaunched = true
		for _, tag := range selected[1:] {
			if launch(tag) {
				t.logger.DebugContext(ctx, "dns group[", t.tag, "] fallback: promoting server ", tag)
				launched++
			}
		}
	}

	received := 0
	var lastErr error

	for {
		// Only exit when all launched goroutines have reported.
		if received >= launched && launched > 0 {
			break
		}
		select {
		case <-fallbackTimer.C:
			launchFallbacks()
		case r := <-ch:
			received++
			if r.err == nil {
				t.rtt.Record(r.tag, r.rtt)
				cancel()
				return r.resp, nil
			}
			if !errors.Is(r.err, context.Canceled) {
				t.rtt.RecordFailure(r.tag)
			}
			t.logger.DebugContext(ctx, "dns group[", t.tag, "] fallback: server ", r.tag, " failed: ", r.err)
			lastErr = r.err
			// If primary failed immediately, promote fallbacks right away.
			if r.tag == primary && !fallbackLaunched {
				launchFallbacks()
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return nil, E.Cause(lastErr, "dns group[", t.tag, "]: all fallback servers failed")
}

// ---- adapter.DNSTransportWithStats interface ----

// Stats returns a snapshot of RTT and health metrics for each member transport.
func (t *GroupTransport) Stats() []adapter.DNSTransportMemberStats {
	t.access.RLock()
	members := t.members
	t.access.RUnlock()

	snapshots := t.rtt.AllSnapshots()
	stats := make([]adapter.DNSTransportMemberStats, len(members))
	for i, m := range members {
		entry, ok := snapshots[m.Tag()]
		if ok {
			stats[i] = adapter.DNSTransportMemberStats{
				Tag:           m.Tag(),
				AverageRTTMs:  entry.ewma,
				Failures:      entry.totalFailures,
				LastQueryTime: entry.lastQueryTime,
			}
		} else {
			stats[i] = adapter.DNSTransportMemberStats{Tag: m.Tag()}
		}
	}
	return stats
}
