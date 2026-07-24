package group

import (
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
)

func nopLogger() log.ContextLogger {
	return log.NewNOPFactory().NewLogger("test")
}

type ExportedRTT struct{ inner RTTEstimator }

func ExportNewRTTEstimator(sampleSize int) *ExportedRTT {
	return &ExportedRTT{inner: NewRTTEstimator(sampleSize)}
}

func (e *ExportedRTT) Record(tag string, d time.Duration) { e.inner.Record(tag, d) }
func (e *ExportedRTT) RecordFailure(tag string)           { e.inner.RecordFailure(tag) }
func (e *ExportedRTT) Sorted(tags []string) []string      { return e.inner.Sorted(tags) }
func (e *ExportedRTT) Snapshot(tag string) *RTTSnapshot {
	snaps := e.inner.AllSnapshots()
	s, ok := snaps[tag]
	if !ok {
		return nil
	}
	return &s
}

type ExportedStrategy struct{ inner Strategy }

func ExportNewStrategy(name string) *ExportedStrategy {
	s, err := NewStrategy(name)
	if err != nil {
		return nil
	}
	return &ExportedStrategy{inner: s}
}

func (s *ExportedStrategy) Select(tags []string, rtt *ExportedRTT) []string {
	return s.inner.Select(tags, rtt.inner)
}

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

	tr := &GroupTransport{
		tag:           tag,
		logger:        logger,
		memberTags:    memberTags,
		strategyName:  strategyName,
		modeStr:       modeStr,
		fallbackDelay: fallbackDelay,
		maxRetries:    maxRetries,
		rtt:           NewRTTEstimator(0),
		members:       members,
		strategy:      strategy,
		dispatcher:    dispatcher,
		started:       true,
	}

	t.Cleanup(func() { _ = tr.Close() })
	return tr
}
