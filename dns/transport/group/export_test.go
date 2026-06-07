// export_test.go exposes internal constructors to the group_test package.
// This file is only compiled during testing.
package group

import (
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
)

// nopLogger returns the singleton nop context logger.
func nopLogger() log.ContextLogger {
	return log.NewNOPFactory().NewLogger("test")
}

// ---- RTT estimator exports ----

// RTTSnapshot is the public view of rttEntry exposed to tests.
type RTTSnapshot struct {
	EWMA     float64
	Failures int
}

// ExportedRTT wraps rttEstimator for test access.
type ExportedRTT struct{ inner *rttEstimator }

func ExportNewRTTEstimator(sampleSize int) *ExportedRTT {
	return &ExportedRTT{inner: newRTTEstimator(sampleSize)}
}

func (e *ExportedRTT) Record(tag string, d time.Duration)  { e.inner.Record(tag, d) }
func (e *ExportedRTT) RecordFailure(tag string)            { e.inner.RecordFailure(tag) }
func (e *ExportedRTT) Sorted(tags []string) []string       { return e.inner.Sorted(tags) }
func (e *ExportedRTT) Snapshot(tag string) *RTTSnapshot {
	s := e.inner.Snapshot(tag)
	if s == nil {
		return nil
	}
	return &RTTSnapshot{EWMA: s.ewma, Failures: s.failures}
}

// ---- Strategy exports ----

// ExportedStrategy wraps a strategy for test access.
type ExportedStrategy struct{ inner strategySelector }

// ExportNewStrategy parses a strategy name; returns nil on error.
func ExportNewStrategy(name string) *ExportedStrategy {
	s, err := newStrategy(name)
	if err != nil {
		return nil
	}
	return &ExportedStrategy{inner: s}
}

func (s *ExportedStrategy) Select(tags []string, rtt *ExportedRTT) []string {
	return s.inner.Select(tags, rtt.inner)
}

// ---- Group transport test constructors ----

// ExportNewGroupWithMembers builds a started GroupTransport with wp2 strategy.
func ExportNewGroupWithMembers(
	t *testing.T,
	tag, strategyName, modeStr string,
	maxRetries int,
	members []adapter.DNSTransport,
) *GroupTransport {
	t.Helper()
	return buildGroupTransport(t, tag, strategyName, modeStr, maxRetries, defaultFallbackDelay, members)
}

// ExportNewGroupWithMembersOrdered builds a started GroupTransport using
// round_robin strategy so that member order is deterministic in tests.
func ExportNewGroupWithMembersOrdered(
	t *testing.T,
	tag, modeStr string,
	maxRetries int,
	members []adapter.DNSTransport,
) *GroupTransport {
	t.Helper()
	return buildGroupTransport(t, tag, "round_robin", modeStr, maxRetries, defaultFallbackDelay, members)
}

// ExportNewGroupWithMembersAndDelay builds a started GroupTransport with a
// custom fallback delay (used in fallback-mode tests).
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

	var mode dispatchMode
	switch modeStr {
	case "concurrent":
		mode = dispatchConcurrent
	case "fallback":
		mode = dispatchFallback
	default:
		mode = dispatchSequential
	}

	strategy, err := newStrategy(strategyName)
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
		logger:        nopLogger(),
		memberTags:    memberTags,
		strategyName:  strategyName,
		mode:          mode,
		fallbackDelay: fallbackDelay,
		maxRetries:    maxRetries,
		rtt:           newRTTEstimator(0),
		members:       members,
		strategy:      strategy,
		started:       true,
	}

	t.Cleanup(func() { _ = tr.Close() })
	return tr
}
