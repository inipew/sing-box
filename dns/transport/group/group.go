package group

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/dialer"
	C "github.com/sagernet/sing-box/constant"
	dnscore "github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"

	mDNS "github.com/miekg/dns"
)

const (
	defaultFallbackDelay = 300 * time.Millisecond
)

var (
	_ adapter.DNSTransport                   = (*GroupTransport)(nil)
	_ adapter.DNSTransportWithStats          = (*GroupTransport)(nil)
	_ adapter.DNSTransportWithDialerOverride = (*GroupTransport)(nil)
	_ adapter.IdleConnectionKeeper           = (*GroupTransport)(nil)
	_ adapter.Referrer                       = (*GroupTransport)(nil)
)

// GroupTransport is a virtual DNS transport that dispatches queries across
// a pool of member transports using a pluggable strategy and dispatch mode.
type GroupTransport struct {
	ctx           context.Context
	tag           string
	logger        log.ContextLogger
	memberTags    []string
	strategyName  string
	modeStr       string
	fallbackDelay time.Duration
	maxRetries    int
	detour        string // optional: outbound tag to force for all member transports
	customDialer  N.Dialer
	hcOptions     *option.DNSGroupHealthCheckOptions

	access        sync.RWMutex
	members       []adapter.DNSTransport // resolved at Start
	clonedMembers []adapter.DNSTransport // clones created by detour override, must be closed by group
	strategy      Strategy
	dispatcher    Dispatcher
	rtt           RTTEstimator
	hc            *HealthChecker
	started       bool
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

	modeStr := strings.ToLower(options.Mode)
	switch modeStr {
	case "", "sequential":
		modeStr = "sequential"
	case "concurrent":
	case "fallback":
	default:
		return nil, fmt.Errorf("dns group[%s]: unknown mode %q (valid: sequential, concurrent, fallback)", tag, options.Mode)
	}

	// Validate strategy early; actual instance is created in Start().
	strategyName := options.Strategy
	if strategyName == "" {
		strategyName = "wp2"
	}
	if _, err := NewStrategy(strategyName); err != nil {
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
		modeStr:       modeStr,
		fallbackDelay: fd,
		maxRetries:    options.MaxRetries,
		detour:        options.Detour,
		hcOptions:     options.HealthCheck,
		rtt:           newRTTEstimator(sampleSize),
	}, nil
}

func (t *GroupTransport) Type() string { return C.DNSTypeGroup }
func (t *GroupTransport) Tag() string  { return t.tag }

func (t *GroupTransport) Dependencies() []string {
	return t.memberTags
}

func (t *GroupTransport) References() []string {
	if t.detour != "" {
		return []string{t.detour}
	}
	return nil
}

func (t *GroupTransport) Reset() {
	t.access.RLock()
	members := t.members
	t.access.RUnlock()
	for _, m := range members {
		m.Reset()
	}
}

func (t *GroupTransport) SetKeepIdleConnections(keep bool) {
	t.access.RLock()
	members := t.members
	t.access.RUnlock()
	for _, m := range members {
		if keeper, ok := m.(adapter.IdleConnectionKeeper); ok {
			keeper.SetKeepIdleConnections(keep)
		}
	}
}

func (t *GroupTransport) CloseIdleConnections() {
	t.access.RLock()
	members := t.members
	t.access.RUnlock()
	for _, m := range members {
		if keeper, ok := m.(adapter.IdleConnectionKeeper); ok {
			keeper.CloseIdleConnections()
		}
	}
}

func (t *GroupTransport) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}

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

	strategy, err := NewStrategy(t.strategyName)
	if err != nil {
		return E.Cause(err, "dns group[", t.tag, "]")
	}

	var dispatcher Dispatcher
	switch t.modeStr {
	case "concurrent":
		dispatcher = &ConcurrentDispatcher{Tag: t.tag, Logger: t.logger, MaxRetries: t.maxRetries}
	case "fallback":
		dispatcher = &FallbackDispatcher{Tag: t.tag, Logger: t.logger, FallbackDelay: t.fallbackDelay, MaxRetries: t.maxRetries}
	default:
		dispatcher = &SequentialDispatcher{Tag: t.tag, Logger: t.logger, MaxRetries: t.maxRetries}
	}

	var overrideDialer N.Dialer
	if t.customDialer != nil {
		overrideDialer = t.customDialer
	} else if t.detour != "" {
		outboundManager := service.FromContext[adapter.OutboundManager](t.ctx)
		if outboundManager == nil {
			return E.New("OutboundManager not found in context; cannot apply detour")
		}
		detourDialer := dialer.NewDetour(outboundManager, t.detour, false)
		if err := dialer.InitializeDetour(detourDialer); err != nil {
			return E.Cause(err, "initialize detour ", t.detour)
		}
		overrideDialer = detourDialer
	}

	var clonedMembers []adapter.DNSTransport
	if overrideDialer != nil {
		effective, cloned, err := t.wrapMembersWithDialer(members, overrideDialer)
		if err != nil {
			return E.Cause(err, "dns group[", t.tag, "] override dialer")
		}
		members = effective
		clonedMembers = cloned
	}

	t.access.Lock()
	t.members = members
	t.clonedMembers = clonedMembers
	t.strategy = strategy
	t.dispatcher = dispatcher
	t.started = true
	t.access.Unlock()

	if t.hcOptions != nil {
		hc := NewHealthChecker(t.ctx, members, t.hcOptions, t.rtt, t.logger)
		t.hc = hc
		hc.Start()
	}

	detourInfo := ""
	if t.detour != "" {
		detourInfo = ", detour=" + t.detour
	}
	t.logger.Info("dns group [", t.tag, "] started with ", len(members),
		" members, strategy=", t.strategyName, ", mode=", t.modeStr, detourInfo)
	return nil
}

func (t *GroupTransport) wrapMembersWithDialer(members []adapter.DNSTransport, overrideDialer N.Dialer) (effective []adapter.DNSTransport, clonedMembers []adapter.DNSTransport, err error) {
	effective = make([]adapter.DNSTransport, len(members))
	for i, m := range members {
		if overridable, ok := m.(adapter.DNSTransportWithDialerOverride); ok {
			existingDetour := dialer.DetourTag(overridable.RawDialer())
			newDetour := dialer.DetourTag(overrideDialer)
			// Warn only if both sides are named detours that differ.
			// If newDetour is empty the overrideDialer is a custom (non-detour) dialer;
			// in that case we still override silently (desired behaviour).
			if existingDetour != "" && newDetour != "" && existingDetour != newDetour {
				t.logger.Warn("dns group[", t.tag, "] overrides detour '", existingDetour,
					"' with '", newDetour, "' for member ", m.Tag())
			} else if existingDetour != "" && newDetour == "" {
				t.logger.Debug("dns group[", t.tag, "] applying custom dialer to member ", m.Tag(),
					" (replaces detour '", existingDetour, "')")
			}

			cloned := overridable.WithDialer(overrideDialer)
			if err := cloned.Start(adapter.StartStateStart); err != nil {
				// Prevent leak: close previously started clones
				for _, c := range clonedMembers {
					c.Close()
				}
				return nil, nil, E.Cause(err, "start cloned transport ", m.Tag())
			}
			effective[i] = cloned
			clonedMembers = append(clonedMembers, cloned)
			t.logger.Debug("dns group[", t.tag, "] dialer override applied to member: ", m.Tag())
		} else {
			effective[i] = m
			t.logger.Debug("dns group[", t.tag, "] member ", m.Tag(), " does not support dialer override, using as-is")
		}
	}
	return effective, clonedMembers, nil
}

func (t *GroupTransport) Close() error {
	t.access.Lock()
	t.started = false
	hc := t.hc
	t.hc = nil
	clonedMembers := t.clonedMembers
	t.clonedMembers = nil
	t.access.Unlock()

	if hc != nil {
		hc.Close()
	}
	for _, m := range clonedMembers {
		m.Close()
	}
	return nil
}

func (t *GroupTransport) RawDialer() N.Dialer {
	if t.customDialer != nil {
		return t.customDialer
	}
	if t.detour == "" {
		return nil
	}
	return dialer.NewDetour(nil, t.detour, false)
}

func (t *GroupTransport) WithDialer(d N.Dialer) adapter.DNSTransport {
	clone := *t
	clone.customDialer = d
	clone.detour = "" // overriding the string detour
	clone.access = sync.RWMutex{}
	clone.members = nil
	clone.clonedMembers = nil
	clone.hc = nil
	clone.started = false
	sampleSize := 0
	if clone.hcOptions != nil {
		sampleSize = clone.hcOptions.SampleSize
	}
	clone.rtt = newRTTEstimator(sampleSize)
	return &clone
}

func (t *GroupTransport) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	t.access.RLock()
	members := t.members
	strategy := t.strategy
	dispatcher := t.dispatcher
	started := t.started
	rtt := t.rtt
	t.access.RUnlock()

	if !started {
		return nil, E.New("dns group[", t.tag, "]: not started")
	}
	if len(members) == 0 {
		return nil, E.New("dns group[", t.tag, "]: no member transports")
	}

	tags := make([]string, len(members))
	tagToTransport := make(map[string]adapter.DNSTransport, len(members))
	for i, m := range members {
		tags[i] = m.Tag()
		tagToTransport[m.Tag()] = m
	}

	selected := strategy.Select(tags, rtt)
	if len(selected) == 0 {
		return nil, E.New("dns group[", t.tag, "]: strategy returned no servers")
	}

	return dispatcher.Dispatch(ctx, message, selected, tagToTransport, rtt)
}

func (t *GroupTransport) ExchangeAsync(ctx context.Context, message *mDNS.Msg, callback func(response *mDNS.Msg, err error)) {
	go func() {
		callback(t.Exchange(ctx, message))
	}()
}

func (t *GroupTransport) Stats() []adapter.DNSTransportMemberStats {
	t.access.RLock()
	members := t.members
	rtt := t.rtt
	t.access.RUnlock()

	snapshots := rtt.AllSnapshots()
	stats := make([]adapter.DNSTransportMemberStats, len(members))
	for i, m := range members {
		entry, ok := snapshots[m.Tag()]
		if ok {
			stats[i] = adapter.DNSTransportMemberStats{
				Tag:           m.Tag(),
				AverageRTTMs:  entry.EWMA,
				JitterMs:      entry.Jitter,
				Failures:      entry.TotalFailures,
				LastQueryTime: entry.LastQueryTime,
			}
		} else {
			stats[i] = adapter.DNSTransportMemberStats{Tag: m.Tag()}
		}
	}
	return stats
}
