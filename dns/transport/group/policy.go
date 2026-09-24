package group

import (
	"fmt"
	"strings"
	"time"

	"github.com/sagernet/sing-box/option"

	mDNS "github.com/miekg/dns"
)

type selectionMode string

const (
	selectionAdaptive   selectionMode = "adaptive"
	selectionOrdered    selectionMode = "ordered"
	selectionRandom     selectionMode = "random"
	selectionRoundRobin selectionMode = "round_robin"
)

type executionMode string

const (
	executionFailover executionMode = "failover"
	executionHedge    executionMode = "hedge"
	executionParallel executionMode = "parallel"
)

type compiledPolicy struct {
	name             string
	selection        selectionMode
	execution        executionMode
	maxAttempts      int
	maxInflight      int
	hedgeDelay       time.Duration
	retryRCodes      map[int]bool
	windowSize       int
	failureThreshold int
	cooldown         time.Duration
	maxCooldown      time.Duration
	activeProbe      *option.DNSGroupActiveProbeOptions
}

func compilePolicy(memberCount int, options option.GroupDNSServerOptions) (compiledPolicy, error) {
	if memberCount <= 0 {
		return compiledPolicy{}, fmt.Errorf("DNS group requires at least one member")
	}
	name := options.Policy
	if name == "" {
		name = "reliable"
	}
	policy := compiledPolicy{
		name:             name,
		selection:        selectionAdaptive,
		maxAttempts:      memberCount,
		windowSize:       20,
		failureThreshold: 3,
		cooldown:         30 * time.Second,
		maxCooldown:      5 * time.Minute,
		retryRCodes:      map[int]bool{2: true, 5: true},
	}
	switch name {
	case "reliable":
		policy.execution = executionHedge
		policy.maxInflight = min(2, memberCount)
		policy.hedgeDelay = 300 * time.Millisecond
	case "low_latency":
		policy.execution = executionParallel
		policy.maxInflight = memberCount
	case "privacy":
		policy.execution = executionFailover
		policy.maxInflight = 1
	default:
		return compiledPolicy{}, fmt.Errorf("unknown DNS group policy: %s", name)
	}
	advanced := options.Advanced
	if advanced == nil {
		return policy, nil
	}
	if advanced.Selection != "" {
		policy.selection = selectionMode(strings.ToLower(advanced.Selection))
		switch policy.selection {
		case selectionAdaptive, selectionOrdered, selectionRandom, selectionRoundRobin:
		default:
			return compiledPolicy{}, fmt.Errorf("unknown DNS group selection: %s", advanced.Selection)
		}
	}
	if advanced.Execution != "" {
		policy.execution = executionMode(strings.ToLower(advanced.Execution))
		switch policy.execution {
		case executionFailover, executionHedge, executionParallel:
		default:
			return compiledPolicy{}, fmt.Errorf("unknown DNS group execution: %s", advanced.Execution)
		}
	}
	if advanced.MaxAttempts > 0 {
		policy.maxAttempts = advanced.MaxAttempts
	}
	if policy.maxAttempts > memberCount {
		return compiledPolicy{}, fmt.Errorf("DNS group max_attempts %d exceeds member count %d", policy.maxAttempts, memberCount)
	}
	if advanced.MaxInflight > 0 {
		policy.maxInflight = advanced.MaxInflight
	}
	if policy.execution == executionFailover {
		if advanced.MaxInflight > 1 {
			return compiledPolicy{}, fmt.Errorf("DNS group max_inflight must be 1 for failover execution")
		}
		policy.maxInflight = 1
	}
	if policy.maxInflight > policy.maxAttempts {
		return compiledPolicy{}, fmt.Errorf("DNS group max_inflight %d exceeds max_attempts %d", policy.maxInflight, policy.maxAttempts)
	}
	if advanced.HedgeDelay != 0 {
		policy.hedgeDelay = time.Duration(advanced.HedgeDelay)
	}
	if advanced.RetryRCodes != nil {
		policy.retryRCodes = make(map[int]bool, len(advanced.RetryRCodes))
		for _, rcodeName := range advanced.RetryRCodes {
			rcode, loaded := mDNS.StringToRcode[strings.ToUpper(rcodeName)]
			if !loaded {
				return compiledPolicy{}, fmt.Errorf("unknown DNS group retry RCODE: %s", rcodeName)
			}
			policy.retryRCodes[rcode] = true
		}
	}
	if policy.execution == executionHedge && policy.hedgeDelay <= 0 {
		return compiledPolicy{}, fmt.Errorf("DNS group hedge execution requires a positive hedge_delay")
	}
	health := advanced.Health
	if health.WindowSize > 0 {
		policy.windowSize = health.WindowSize
	}
	if health.WindowSize < 0 || health.FailureThreshold < 0 || time.Duration(health.Cooldown) < 0 || time.Duration(health.MaxCooldown) < 0 {
		return compiledPolicy{}, fmt.Errorf("DNS group health values must not be negative")
	}
	if health.FailureThreshold > 0 {
		policy.failureThreshold = health.FailureThreshold
	}
	if health.Cooldown != 0 {
		policy.cooldown = time.Duration(health.Cooldown)
	}
	if health.MaxCooldown != 0 {
		policy.maxCooldown = time.Duration(health.MaxCooldown)
	}
	if policy.maxCooldown < policy.cooldown {
		return compiledPolicy{}, fmt.Errorf("DNS group max_cooldown must not be shorter than cooldown")
	}
	policy.activeProbe = health.ActiveProbe
	if policy.activeProbe != nil {
		if time.Duration(policy.activeProbe.Interval) < 0 || time.Duration(policy.activeProbe.Timeout) < 0 {
			return compiledPolicy{}, fmt.Errorf("DNS group active probe durations must not be negative")
		}
		if policy.activeProbe.Type != "" {
			if _, loaded := mDNS.StringToType[strings.ToUpper(policy.activeProbe.Type)]; !loaded {
				return compiledPolicy{}, fmt.Errorf("unknown DNS group active probe type: %s", policy.activeProbe.Type)
			}
		}
	}
	return policy, nil
}
