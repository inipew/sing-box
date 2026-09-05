// export_test.go exposes internal constructors to the group_test package.
// This file is only compiled during testing.
package group

import (
	"context"
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
	t *testing.T,
	tag, strategyName, modeStr string,
	maxRetries int,
	fallbackDelay time.Duration,
	members []adapter.DNSTransport,
) *GroupTransport {
	t.Helper()

	strategy, err := NewStrategy(strategyName)
	if err != nil {
		t.Fatalf("invalid strategy %q: %v", strategyName, err)
	}

	if fallbackDelay == 0 {
		fallbackDelay = defaultFallbackDelay
	}

	memberTags := make([]string, len(members))
	for i, m := range members {
		memberTags[i] = m.Tag()
	}

	logger := nopLogger()

	var dispatcher Dispatcher
	switch modeStr {
	case "concurrent":
		dispatcher = &ConcurrentDispatcher{Tag: tag, Logger: logger, MaxRetries: maxRetries}
	case "fallback":
		dispatcher = &FallbackDispatcher{Tag: tag, Logger: logger, FallbackDelay: fallbackDelay, MaxRetries: maxRetries}
	default:
		dispatcher = &SequentialDispatcher{Tag: tag, Logger: logger, MaxRetries: maxRetries}
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
	byTag := make(map[string]adapter.DNSTransport, len(members))
	for _, member := range members {
		byTag[member.Tag()] = member
	}
	tr.runtime.Store(&groupRuntime{
		ctx:        runtimeCtx,
		cancel:     cancelRuntime,
		members:    members,
		byTag:      byTag,
		strategy:   strategy,
		dispatcher: dispatcher,
	})

	t.Cleanup(func() { _ = tr.Close() })
	return tr
}
