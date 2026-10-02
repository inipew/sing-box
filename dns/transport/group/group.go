package group

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
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
	_ adapter.DNSTransportWithResponseCheck  = (*GroupTransport)(nil)
	_ adapter.DNSTransportWithEnvironment    = (*GroupTransport)(nil)
	_ adapter.DNSGroupSnapshotProvider       = (*GroupTransport)(nil)
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
	policy        compiledPolicy
	hcOptions     *option.DNSGroupActiveProbeOptions

	lifecycleAccess sync.Mutex
	runtime         atomic.Pointer[groupRuntime]
	rtt             RTTEstimator
}

type groupRuntime struct {
	ctx        context.Context
	cancel     context.CancelFunc
	members    []adapter.DNSTransport
	byTag      map[string]adapter.DNSTransport
	strategy   Strategy
	dispatcher Dispatcher
	health     *HealthChecker
	owned      *adapter.Scope
	access     sync.Mutex
	closing    bool
	waiter     sync.WaitGroup
	workers    *sync.WaitGroup
}

func (r *groupRuntime) acquire() bool {
	r.access.Lock()
	defer r.access.Unlock()
	if r.closing {
		return false
	}
	r.waiter.Add(1)
	return true
}

func (r *groupRuntime) release() {
	r.waiter.Done()
}

func (r *groupRuntime) beginClose() {
	r.access.Lock()
	r.closing = true
	r.access.Unlock()
	r.cancel()
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
	seenMembers := make(map[string]struct{}, len(options.Servers))
	for _, memberTag := range options.Servers {
		if memberTag == tag {
			return nil, E.New("dns group[", tag, "]: group cannot contain itself")
		}
		if _, loaded := seenMembers[memberTag]; loaded {
			return nil, E.New("dns group[", tag, "]: duplicate member tag: ", memberTag)
		}
		seenMembers[memberTag] = struct{}{}
	}

	policy, err := compilePolicy(len(options.Servers), options)
	if err != nil {
		return nil, E.Cause(err, "dns group[", tag, "]")
	}
	strategyName := string(policy.selection)
	modeStr := string(policy.execution)

	return &GroupTransport{
		ctx:           ctx,
		tag:           tag,
		logger:        logger,
		memberTags:    options.Servers,
		strategyName:  strategyName,
		modeStr:       modeStr,
		fallbackDelay: policy.hedgeDelay,
		maxRetries:    policy.maxAttempts,
		detour:        options.Detour,
		policy:        policy,
		hcOptions:     policy.activeProbe,
		rtt:           newDefaultRTTEstimator(policy.windowSize, policy.failureThreshold, policy.cooldown, policy.maxCooldown, time.Now),
	}, nil
}

func (t *GroupTransport) Type() string { return C.DNSTypeGroup }
func (t *GroupTransport) Tag() string  { return t.tag }

func (t *GroupTransport) Dependencies() []string {
	return append([]string(nil), t.memberTags...)
}

func (t *GroupTransport) Environment() []string {
	runtime := t.runtime.Load()
	if runtime == nil || !runtime.acquire() {
		return nil
	}
	defer runtime.release()
	var environment []string
	for _, member := range runtime.members {
		withEnvironment, ok := member.(adapter.DNSTransportWithEnvironment)
		if !ok {
			continue
		}
		prefix := strconv.Itoa(len(member.Tag())) + ":" + member.Tag()
		for _, entry := range withEnvironment.Environment() {
			environment = append(environment, prefix+entry)
		}
	}
	return environment
}

func (t *GroupTransport) References() []string {
	if t.detour != "" {
		return []string{t.detour}
	}
	return nil
}

func (t *GroupTransport) Reset() {
	runtime := t.runtime.Load()
	if runtime == nil || !runtime.acquire() {
		return
	}
	defer runtime.release()
	for _, m := range runtime.members {
		m.Reset()
	}
}

func (t *GroupTransport) SetKeepIdleConnections(keep bool) {
	runtime := t.runtime.Load()
	if runtime == nil || !runtime.acquire() {
		return
	}
	defer runtime.release()
	for _, m := range runtime.members {
		if keeper, ok := m.(adapter.IdleConnectionKeeper); ok {
			keeper.SetKeepIdleConnections(keep)
		}
	}
}

func (t *GroupTransport) CloseIdleConnections() {
	runtime := t.runtime.Load()
	if runtime == nil || !runtime.acquire() {
		return
	}
	defer runtime.release()
	for _, m := range runtime.members {
		if keeper, ok := m.(adapter.IdleConnectionKeeper); ok {
			keeper.CloseIdleConnections()
		}
	}
}

func (t *GroupTransport) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	t.lifecycleAccess.Lock()
	defer t.lifecycleAccess.Unlock()
	if t.runtime.Load() != nil {
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
		if transport.Type() == C.DNSTypeGroup {
			return E.New("dns group[", t.tag, "]: nested group member is not supported: ", memberTag)
		}
		if transport.Type() == C.DNSTypeFakeIP {
			return E.New("dns group[", t.tag, "]: fakeip member is not supported: ", memberTag)
		}
		members = append(members, transport)
	}

	strategy, err := newSelectionStrategy(t.policy.selection)
	if err != nil {
		return E.Cause(err, "dns group[", t.tag, "]")
	}

	var dispatcher Dispatcher
	workerWaiter := new(sync.WaitGroup)
	switch t.modeStr {
	case string(executionParallel):
		dispatcher = &ConcurrentDispatcher{Tag: t.tag, Logger: t.logger, MaxRetries: t.maxRetries, MaxInflight: t.policy.maxInflight, RetryRCodes: t.policy.retryRCodes, workers: workerWaiter}
	case string(executionHedge):
		dispatcher = &FallbackDispatcher{Tag: t.tag, Logger: t.logger, FallbackDelay: t.fallbackDelay, MaxRetries: t.maxRetries, MaxInflight: t.policy.maxInflight, RetryRCodes: t.policy.retryRCodes, workers: workerWaiter}
	default:
		dispatcher = &SequentialDispatcher{Tag: t.tag, Logger: t.logger, MaxRetries: t.maxRetries, RetryRCodes: t.policy.retryRCodes}
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

	var clonedMembers *adapter.Scope
	if overrideDialer != nil {
		effective, cloned, err := t.wrapMembersWithDialer(members, overrideDialer)
		if err != nil {
			return E.Cause(err, "dns group[", t.tag, "] override dialer")
		}
		members = effective
		clonedMembers = cloned
	}

	runtimeCtx, cancelRuntime := context.WithCancel(t.ctx)
	runtime := &groupRuntime{
		ctx:        runtimeCtx,
		cancel:     cancelRuntime,
		members:    members,
		byTag:      make(map[string]adapter.DNSTransport, len(members)),
		strategy:   strategy,
		dispatcher: dispatcher,
		owned:      clonedMembers,
		workers:    workerWaiter,
	}
	for _, member := range members {
		if _, exists := runtime.byTag[member.Tag()]; exists {
			cancelRuntime()
			return closeOwnedTransports(E.New("dns group[", t.tag, "]: duplicate member tag: ", member.Tag()), clonedMembers)
		}
		runtime.byTag[member.Tag()] = member
	}
	if t.hcOptions != nil && t.hcOptions.Enabled {
		runtime.health = NewHealthChecker(runtimeCtx, members, t.hcOptions, t.rtt, t.logger)
	}
	t.runtime.Store(runtime)
	scope.Add(t.Close)
	if runtime.health != nil {
		runtime.health.Start()
	}

	detourInfo := ""
	if t.detour != "" {
		detourInfo = ", detour=" + t.detour
	}
	t.logger.Info("dns group[", t.tag, "] started with ", len(members),
		" members, strategy=", t.strategyName, ", mode=", t.modeStr, detourInfo)
	return nil
}

func (t *GroupTransport) wrapMembersWithDialer(members []adapter.DNSTransport, overrideDialer N.Dialer) (effective []adapter.DNSTransport, clonedMembers *adapter.Scope, err error) {
	effective = make([]adapter.DNSTransport, len(members))
	clonedMembers = adapter.NewScope(t.ctx, t.logger)
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
			if err := clonedMembers.Start("cloned DNS transport "+m.Tag(), cloned, adapter.StartStateStart); err != nil {
				return nil, nil, closeOwnedTransports(E.Cause(err, "start cloned transport ", m.Tag()), clonedMembers)
			}
			effective[i] = cloned
			t.logger.Debug("dns group[", t.tag, "] dialer override applied to member: ", m.Tag())
		} else {
			if _, networkless := m.(adapter.DNSTransportNetworkless); !networkless {
				return nil, nil, closeOwnedTransports(E.New("member ", m.Tag(), " does not support group detour override"), clonedMembers)
			}
			effective[i] = m
			t.logger.Debug("dns group[", t.tag, "] networkless member ", m.Tag(), " is unaffected by detour")
		}
	}
	return effective, clonedMembers, nil
}

func (t *GroupTransport) Close() error {
	t.lifecycleAccess.Lock()
	defer t.lifecycleAccess.Unlock()
	runtime := t.runtime.Swap(nil)
	if runtime == nil {
		return nil
	}
	runtime.beginClose()
	if runtime.health != nil {
		runtime.health.Close()
	}
	runtime.waiter.Wait()
	runtime.workers.Wait()
	return closeOwnedTransports(nil, runtime.owned)
}

func closeOwnedTransports(err error, scope *adapter.Scope) error {
	if scope != nil {
		err = E.Errors(err, scope.Close())
	}
	return err
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
	return &GroupTransport{
		ctx:           t.ctx,
		tag:           t.tag,
		logger:        t.logger,
		memberTags:    append([]string(nil), t.memberTags...),
		strategyName:  t.strategyName,
		modeStr:       t.modeStr,
		fallbackDelay: t.fallbackDelay,
		maxRetries:    t.maxRetries,
		customDialer:  d,
		policy:        t.policy,
		hcOptions:     t.hcOptions,
		rtt:           newDefaultRTTEstimator(t.policy.windowSize, t.policy.failureThreshold, t.policy.cooldown, t.policy.maxCooldown, time.Now),
	}
}

func (t *GroupTransport) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	return t.ExchangeWithResponseCheck(ctx, message, nil)
}

func (t *GroupTransport) ExchangeWithResponseCheck(ctx context.Context, message *mDNS.Msg, checker adapter.DNSResponseChecker) (*mDNS.Msg, error) {
	runtime := t.runtime.Load()
	if runtime == nil || !runtime.acquire() {
		return nil, E.New("dns group[", t.tag, "]: not started")
	}
	defer runtime.release()
	if len(runtime.members) == 0 {
		return nil, E.New("dns group[", t.tag, "]: no member transports")
	}
	tags := make([]string, len(runtime.members))
	for i, m := range runtime.members {
		tags[i] = m.Tag()
	}
	tags = availableTags(tags, t.rtt)
	selected := runtime.strategy.Select(tags, t.rtt)
	if len(selected) == 0 {
		return nil, E.New("dns group[", t.tag, "]: strategy returned no servers")
	}
	selected = prioritizeHalfOpen(selected, t.rtt.AllSnapshots())

	queryCtx, cancelQuery := context.WithCancel(ctx)
	stopRuntimeCancel := context.AfterFunc(runtime.ctx, cancelQuery)
	defer func() {
		stopRuntimeCancel()
		cancelQuery()
	}()
	return runtime.dispatcher.Dispatch(queryCtx, message, selected, runtime.byTag, t.rtt, checker)
}

func prioritizeHalfOpen(selected []string, snapshots map[string]RTTSnapshot) []string {
	for index, tag := range selected {
		snapshot, ok := snapshots[tag]
		if !ok || snapshot.State != "half_open" || snapshot.Inflight != 0 {
			continue
		}
		if index == 0 {
			return selected
		}
		result := append([]string(nil), selected...)
		copy(result[1:index+1], selected[:index])
		result[0] = tag
		return result
	}
	return selected
}

func availableTags(tags []string, estimator RTTEstimator) []string {
	snapshots := estimator.AllSnapshots()
	available := make([]string, 0, len(tags))
	for _, tag := range tags {
		if snapshot, loaded := snapshots[tag]; !loaded || snapshot.State != "open" {
			available = append(available, tag)
		}
	}
	if len(available) == 0 {
		return tags
	}
	return available
}

func (t *GroupTransport) ExchangeAsync(ctx context.Context, message *mDNS.Msg, callback func(response *mDNS.Msg, err error)) {
	go func() {
		callback(t.Exchange(ctx, message))
	}()
}

func (t *GroupTransport) GroupSnapshot() adapter.DNSGroupSnapshot {
	result := adapter.DNSGroupSnapshot{
		Tag:          t.tag,
		Policy:       t.policy.name,
		Selection:    string(t.policy.selection),
		Execution:    string(t.policy.execution),
		MaxAttempts:  t.policy.maxAttempts,
		MaxInflight:  t.policy.maxInflight,
		HedgeDelayMs: t.policy.hedgeDelay.Milliseconds(),
	}
	runtime := t.runtime.Load()
	if runtime == nil {
		return result
	}
	snapshots := t.rtt.AllSnapshots()
	members := runtime.members
	result.Members = make([]adapter.DNSGroupMemberSnapshot, len(members))
	for i, m := range members {
		entry, ok := snapshots[m.Tag()]
		if ok {
			result.Members[i] = adapter.DNSGroupMemberSnapshot{
				Tag:                 m.Tag(),
				State:               entry.State,
				AverageRTTMs:        entry.EWMA,
				JitterMs:            entry.Jitter,
				ConsecutiveFailures: entry.Failures,
				TotalFailures:       uint64(entry.TotalFailures),
				TotalAttempts:       entry.TotalAttempts,
				SuccessRate:         entry.SuccessRate,
				LastAttempt:         entry.LastQueryTime,
				LastSuccess:         entry.LastSuccess,
				LastFailure:         entry.LastFailure,
				CircuitUntil:        entry.CircuitUntil,
				Selected:            entry.Selected,
				Won:                 entry.Won,
				Inflight:            entry.Inflight,
				ProbeAttempts:       entry.ProbeAttempts,
				ProbeFailures:       entry.ProbeFailures,
			}
		} else {
			result.Members[i] = adapter.DNSGroupMemberSnapshot{Tag: m.Tag(), State: "closed"}
		}
	}
	return result
}
