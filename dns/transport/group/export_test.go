// export_test.go exposes internal constructors to the group_test package.
// This file is only compiled during testing.
package group

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
)

// nopLogger returns the singleton nop context logger.
func nopLogger() log.ContextLogger {
	return log.NewNOPFactory().NewLogger("test")
}

type ExportedRTT = RTTEstimator

func ExportNewRTTEstimator(sampleSize int) RTTEstimator {
	return newRTTEstimator(sampleSize)
}

type ExportedStrategy = Strategy

func ExportNewStrategy(name string) Strategy {
	s, err := NewStrategy(name)
	if err != nil {
		return nil
	}
	return s
}

// ---- Group transport test constructors ----

func ExportNewGroupWithMembers(
	t *testing.T,
	tag, strategyName, modeStr string,
	maxRetries int,
	members []adapter.DNSTransport,
) *GroupTransport {
	t.Helper()
	return buildGroupTransport(t, tag, strategyName, modeStr, maxRetries, defaultFallbackDelay, members)
}

func ExportNewGroupWithMembersOrdered(
	t *testing.T,
	tag, modeStr string,
	maxRetries int,
	members []adapter.DNSTransport,
) *GroupTransport {
	t.Helper()
	return buildGroupTransport(t, tag, "round_robin", modeStr, maxRetries, defaultFallbackDelay, members)
}

func ExportNewGroupWithMembersAndDelay(
	t *testing.T,
	tag, modeStr string,
	fallbackDelay time.Duration,
	members []adapter.DNSTransport,
) *GroupTransport {
	t.Helper()
	return buildGroupTransport(t, tag, "round_robin", modeStr, 0, fallbackDelay, members)
}

func buildGroupTransport(
	t testing.TB,
	tag, strategyName, modeStr string,
	maxRetries int,
	fallbackDelay time.Duration,
	members []adapter.DNSTransport,
) *GroupTransport {
	if t != nil {
		t.Helper()
	}

	strategy, err := NewStrategy(strategyName)
	if err != nil {
		if t != nil {
			t.Fatalf("invalid strategy %q: %v", strategyName, err)
		}
		panic(err)
	}

	if fallbackDelay == 0 {
		fallbackDelay = defaultFallbackDelay
	}

	memberTags := make([]string, len(members))
	for i, m := range members {
		memberTags[i] = m.Tag()
	}

	logger := nopLogger()
	retryRCodes := map[int]bool{2: true, 5: true}

	var dispatcher Dispatcher
	workerWaiter := new(sync.WaitGroup)
	switch modeStr {
	case "concurrent":
		dispatcher = &ConcurrentDispatcher{Tag: tag, Logger: logger, MaxRetries: maxRetries, MaxInflight: len(members), RetryRCodes: retryRCodes, workers: workerWaiter}
	case "fallback":
		dispatcher = &FallbackDispatcher{Tag: tag, Logger: logger, FallbackDelay: fallbackDelay, MaxRetries: maxRetries, MaxInflight: 2, RetryRCodes: retryRCodes, workers: workerWaiter}
	default:
		dispatcher = &SequentialDispatcher{Tag: tag, Logger: logger, MaxRetries: maxRetries, RetryRCodes: retryRCodes}
	}

	tr := &GroupTransport{
		tag:           tag,
		logger:        logger,
		memberTags:    memberTags,
		strategyName:  strategyName,
		modeStr:       modeStr,
		fallbackDelay: fallbackDelay,
		maxRetries:    maxRetries,
		rtt:           newRTTEstimator(0),
	}
	runtimeCtx, cancelRuntime := context.WithCancel(context.Background())
	slots := make(chan struct{}, defaultMaxConcurrent)
	byTag := make(map[string]adapter.DNSTransport, len(members))
	for _, member := range members {
		byTag[member.Tag()] = &admissionTransport{
			DNSTransport: member,
			runtimeCtx:   runtimeCtx,
			slots:        slots,
		}
	}
	tr.runtime.Store(&groupRuntime{
		ctx:        runtimeCtx,
		cancel:     cancelRuntime,
		members:    members,
		byTag:      byTag,
		strategy:   strategy,
		dispatcher: dispatcher,
		workers:    workerWaiter,
		slots:      slots,
	})

	if t != nil {
		t.Cleanup(func() { _ = tr.Close() })
	}
	return tr
}

func ExportNewGroupWithMaxConcurrent(
	t testing.TB,
	tag, strategyName, modeStr string,
	maxConcurrent int,
	members []adapter.DNSTransport,
) *GroupTransport {
	if t != nil {
		t.Helper()
	}
	tr := buildGroupTransport(t, tag, strategyName, modeStr, 0, defaultFallbackDelay, members)
	if maxConcurrent > 0 {
		runtime := tr.runtime.Load()
		if runtime != nil {
			slots := make(chan struct{}, maxConcurrent)
			runtime.slots = slots
			for _, m := range members {
				runtime.byTag[m.Tag()] = &admissionTransport{
					DNSTransport: m,
					runtimeCtx:   runtime.ctx,
					slots:        slots,
				}
			}
		}
	}
	return tr
}
