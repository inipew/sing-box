package group

import (
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	"github.com/stretchr/testify/require"
)

func TestCompilePolicyReliableDefaults(t *testing.T) {
	policy, err := compilePolicy(3, option.GroupDNSServerOptions{Policy: "reliable"})
	require.NoError(t, err)
	require.Equal(t, selectionAdaptive, policy.selection)
	require.Equal(t, executionHedge, policy.execution)
	require.Equal(t, 3, policy.maxAttempts)
	require.Equal(t, 2, policy.maxInflight)
	require.Equal(t, 300*time.Millisecond, policy.hedgeDelay)
	require.Equal(t, 20, policy.windowSize)
	require.Equal(t, 3, policy.failureThreshold)
}

func TestCompilePolicyPresets(t *testing.T) {
	privacy, err := compilePolicy(4, option.GroupDNSServerOptions{Policy: "privacy"})
	require.NoError(t, err)
	require.Equal(t, executionFailover, privacy.execution)
	require.Equal(t, 1, privacy.maxInflight)

	fast, err := compilePolicy(4, option.GroupDNSServerOptions{Policy: "low_latency"})
	require.NoError(t, err)
	require.Equal(t, executionParallel, fast.execution)
	require.Equal(t, 4, fast.maxInflight)
}

func TestCompilePolicyRejectsInvalidOverride(t *testing.T) {
	_, err := compilePolicy(2, option.GroupDNSServerOptions{
		Policy: "privacy",
		Advanced: &option.DNSGroupAdvancedOptions{
			Selection: "unknown",
		},
	})
	require.EqualError(t, err, "unknown DNS group selection: unknown")
}

func TestCompilePolicyRejectsInvalidActiveProbeType(t *testing.T) {
	_, err := compilePolicy(2, option.GroupDNSServerOptions{
		Policy: "reliable",
		Advanced: &option.DNSGroupAdvancedOptions{Health: option.DNSGroupHealthOptions{
			ActiveProbe: &option.DNSGroupActiveProbeOptions{Enabled: true, Type: "NOT_A_DNS_TYPE"},
		}},
	})
	require.EqualError(t, err, "unknown DNS group active probe type: NOT_A_DNS_TYPE")
}

func TestRTTEstimatorOpensAndRecoversCircuit(t *testing.T) {
	now := time.Unix(100, 0)
	estimator := newDefaultRTTEstimator(4, 3, 30*time.Second, 5*time.Minute, func() time.Time { return now })
	estimator.Record("healthy", 20*time.Millisecond)
	for range 3 {
		estimator.RecordFailure("failing")
	}

	open := estimator.Snapshot("failing")
	require.Equal(t, "open", open.State)
	require.Equal(t, 0.0, open.SuccessRate)
	require.Equal(t, "healthy", estimator.Sorted([]string{"failing", "healthy"})[0])

	now = now.Add(31 * time.Second)
	halfOpen := estimator.Snapshot("failing")
	require.Equal(t, "half_open", halfOpen.State)
	estimator.Record("failing", 10*time.Millisecond)
	require.Equal(t, "closed", estimator.Snapshot("failing").State)
}

func TestRTTEstimatorTracksUserAndProbeMetricsSeparately(t *testing.T) {
	estimator := newDefaultRTTEstimator(4, 3, 30*time.Second, 5*time.Minute, time.Now)
	estimator.Begin("a")
	require.Equal(t, 1, estimator.Snapshot("a").Inflight)
	estimator.End("a")
	estimator.Record("a", 10*time.Millisecond)
	estimator.RecordProbe("a", 12*time.Millisecond, nil)

	snapshot := estimator.Snapshot("a")
	require.Equal(t, uint64(1), snapshot.Selected)
	require.Equal(t, uint64(1), snapshot.Won)
	require.Equal(t, uint64(1), snapshot.TotalAttempts)
	require.Equal(t, uint64(1), snapshot.ProbeAttempts)
	require.Zero(t, snapshot.Inflight)
}

func TestAvailableTagsSkipsOpenCircuitUnlessAllAreOpen(t *testing.T) {
	now := time.Unix(100, 0)
	estimator := newDefaultRTTEstimator(4, 1, time.Minute, 5*time.Minute, func() time.Time { return now })
	estimator.RecordFailure("open")
	require.Equal(t, []string{"healthy"}, availableTags([]string{"open", "healthy"}, estimator))
	estimator.RecordFailure("healthy")
	require.Equal(t, []string{"open", "healthy"}, availableTags([]string{"open", "healthy"}, estimator))
}
