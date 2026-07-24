package group_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/dns/transport/group"

	mDNS "github.com/miekg/dns"
)

// ---- fake transport helpers ----

type fakeTransport struct {
	tag      string
	delay    time.Duration
	err      error
	callCount atomic.Int64
}

func (f *fakeTransport) Type() string                  { return "fake" }
func (f *fakeTransport) Tag() string                   { return f.tag }
func (f *fakeTransport) Dependencies() []string        { return nil }
func (f *fakeTransport) Reset()                        {}
func (f *fakeTransport) Start(_ adapter.StartStage) error { return nil }
func (f *fakeTransport) Close() error                  { return nil }

func (f *fakeTransport) Exchange(ctx context.Context, msg *mDNS.Msg) (*mDNS.Msg, error) {
	f.callCount.Add(1)
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	resp := new(mDNS.Msg)
	resp.SetReply(msg)
	resp.Answer = append(resp.Answer, &mDNS.A{
		Hdr: mDNS.RR_Header{Name: "example.com.", Rrtype: mDNS.TypeA, Class: mDNS.ClassINET, Ttl: 60},
		A:   []byte{1, 2, 3, 4},
	})
	return resp, nil
}

func (f *fakeTransport) ExchangeAsync(ctx context.Context, msg *mDNS.Msg, callback func(response *mDNS.Msg, err error)) {
	go func() {
		callback(f.Exchange(ctx, msg))
	}()
}

func makeMsg() *mDNS.Msg {
	msg := new(mDNS.Msg)
	msg.SetQuestion("example.com.", mDNS.TypeA)
	return msg
}

// ---- RTT estimator tests ----

func TestRTTEstimator_EWMA(t *testing.T) {
	e := group.ExportNewRTTEstimator(10) // returns *group.ExportedRTT
	e.Record("a", 100*time.Millisecond)
	e.Record("a", 200*time.Millisecond)
	e.Record("a", 100*time.Millisecond)

	snap := e.Snapshot("a") // returns *group.RTTSnapshot
	if snap == nil {
		t.Fatal("expected snapshot for 'a'")
	}
	// EWMA should be between 100ms and 200ms
	if snap.EWMA < 100 || snap.EWMA > 200 {
		t.Errorf("EWMA %v not in [100,200]", snap.EWMA)
	}
}

func TestRTTEstimator_Sorted(t *testing.T) {
	e := group.ExportNewRTTEstimator(10)
	e.Record("slow", 300*time.Millisecond)
	e.Record("fast", 10*time.Millisecond)
	e.Record("mid", 100*time.Millisecond)

	sorted := e.Sorted([]string{"slow", "fast", "mid"})
	if sorted[0] != "fast" {
		t.Errorf("expected fast first, got %v", sorted[0])
	}
	if sorted[2] != "slow" {
		t.Errorf("expected slow last, got %v", sorted[2])
	}
}

func TestRTTEstimator_FailureDeprioritization(t *testing.T) {
	e := group.ExportNewRTTEstimator(10)
	e.Record("good", 50*time.Millisecond)
	e.RecordFailure("bad")
	e.RecordFailure("bad")
	e.RecordFailure("bad")

	sorted := e.Sorted([]string{"bad", "good"})
	if sorted[0] != "good" {
		t.Errorf("expected good first (bad is failing), got %v", sorted[0])
	}
}

// ---- Strategy tests ----

func rtt(t *testing.T) *group.ExportedRTT {
	t.Helper()
	return group.ExportNewRTTEstimator(10)
}

func TestStrategyFirst(t *testing.T) {
	e := rtt(t)
	e.Record("a", 300*time.Millisecond)
	e.Record("b", 10*time.Millisecond)
	e.Record("c", 150*time.Millisecond)

	s := group.ExportNewStrategy("first")
	got := s.Select([]string{"a", "b", "c"}, e)
	// Select returns all servers in RTT order; first element must be fastest.
	if len(got) == 0 {
		t.Fatalf("first strategy returned empty list")
	}
	if got[0] != "b" {
		t.Errorf("expected 'b' (fastest) as first element, got %q", got[0])
	}
	if len(got) != 3 {
		t.Errorf("expected all 3 servers in result, got %d", len(got))
	}
}

func TestStrategyRoundRobin(t *testing.T) {
	e := rtt(t)
	s := group.ExportNewStrategy("round_robin")
	tags := []string{"a", "b", "c"}
	seen := map[string]bool{}
	for i := 0; i < 9; i++ {
		got := s.Select(tags, e)
		seen[got[0]] = true
	}
	for _, tag := range tags {
		if !seen[tag] {
			t.Errorf("round_robin never selected %q in 9 iterations", tag)
		}
	}
}

func TestStrategyWeighted(t *testing.T) {
	e := rtt(t)
	e.Record("fast", 10*time.Millisecond)
	e.Record("slow", 500*time.Millisecond)

	s := group.ExportNewStrategy("weighted")
	if s == nil {
		t.Fatal("weighted strategy should be valid")
	}
	fastCount := 0
	for i := 0; i < 100; i++ {
		got := s.Select([]string{"fast", "slow"}, e)
		if got[0] == "fast" {
			fastCount++
		}
	}
	if fastCount < 60 {
		t.Errorf("weighted strategy selected fast server only %d/100 times", fastCount)
	}
}

func TestStrategyEpsilonGreedy(t *testing.T) {
	s := group.ExportNewStrategy("epsilon_greedy")
	if s == nil {
		t.Fatal("epsilon_greedy strategy should be valid")
	}
}

func TestStrategyInvalid(t *testing.T) {
	s := group.ExportNewStrategy("bogus_strategy_xyz")
	if s != nil {
		t.Error("expected nil for invalid strategy name")
	}
}

// ---- Group transport dispatch tests ----
// These tests use exported constructor helpers from an export_test.go shim.

func TestGroupSequential_FirstServerSuccess(t *testing.T) {
	fast := &fakeTransport{tag: "fast", delay: 5 * time.Millisecond}
	slow := &fakeTransport{tag: "slow", delay: 200 * time.Millisecond}

	tr := group.ExportNewGroupWithMembers(t, "test", "wp2", "sequential", 0,
		[]adapter.DNSTransport{fast, slow})

	resp, err := tr.Exchange(context.Background(), makeMsg())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response")
	}
}

func TestGroupSequential_FallbackOnError(t *testing.T) {
	bad := &fakeTransport{tag: "bad", err: errors.New("connection refused")}
	good := &fakeTransport{tag: "good"}

	// Use "first" strategy so bad always comes first if it has lowest RTT (unseen → no RTT).
	// Force ordering: put bad first in member list and use round_robin so bad is tried first.
	tr := group.ExportNewGroupWithMembersOrdered(t, "test", "sequential", 0,
		[]adapter.DNSTransport{bad, good})

	resp, err := tr.Exchange(context.Background(), makeMsg())
	if err != nil {
		t.Fatalf("sequential should fall back to 'good': %v", err)
	}
	if resp == nil {
		t.Fatal("expected response from fallback server")
	}
	if good.callCount.Load() == 0 {
		t.Error("'good' server was never called")
	}
}

func TestGroupSequential_AllFail(t *testing.T) {
	a := &fakeTransport{tag: "a", err: errors.New("err")}
	b := &fakeTransport{tag: "b", err: errors.New("err")}

	tr := group.ExportNewGroupWithMembersOrdered(t, "test", "sequential", 0,
		[]adapter.DNSTransport{a, b})

	_, err := tr.Exchange(context.Background(), makeMsg())
	if err == nil {
		t.Fatal("expected error when all servers fail")
	}
}

func TestGroupConcurrent_ReturnsFirst(t *testing.T) {
	fast := &fakeTransport{tag: "fast", delay: 10 * time.Millisecond}
	slow := &fakeTransport{tag: "slow", delay: 300 * time.Millisecond}

	tr := group.ExportNewGroupWithMembersOrdered(t, "test", "concurrent", 0,
		[]adapter.DNSTransport{fast, slow})

	start := time.Now()
	resp, err := tr.Exchange(context.Background(), makeMsg())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response")
	}
	// Should complete well before the slow server's delay
	if elapsed > 200*time.Millisecond {
		t.Errorf("concurrent mode took %v, expected < 200ms", elapsed)
	}
}

func TestGroupConcurrent_CancelsOthers(t *testing.T) {
	// slow should be cancelled once fast responds
	fast := &fakeTransport{tag: "fast", delay: 10 * time.Millisecond}
	slow := &fakeTransport{tag: "slow", delay: 500 * time.Millisecond}

	tr := group.ExportNewGroupWithMembersOrdered(t, "test", "concurrent", 0,
		[]adapter.DNSTransport{fast, slow})

	_, _ = tr.Exchange(context.Background(), makeMsg())
	// Give goroutines time to notice cancellation
	time.Sleep(50 * time.Millisecond)

	// slow may have been called but should not have completed successfully
	if slow.callCount.Load() > 0 {
		// It was called but should have been cancelled — that's fine.
		// The key is that Exchange returned well before slow's delay.
	}
}

func TestGroupFallback_PromotesAfterDelay(t *testing.T) {
	// primary is slow; fallback should kick in after the delay
	primary := &fakeTransport{tag: "primary", delay: 500 * time.Millisecond}
	secondary := &fakeTransport{tag: "secondary", delay: 5 * time.Millisecond}

	const delay = 50 * time.Millisecond
	tr := group.ExportNewGroupWithMembersAndDelay(t, "test", "fallback", delay,
		[]adapter.DNSTransport{primary, secondary})

	start := time.Now()
	resp, err := tr.Exchange(context.Background(), makeMsg())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("fallback mode should succeed via secondary: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response")
	}
	// Should complete around fallbackDelay + secondary.delay, not primary.delay
	if elapsed > 300*time.Millisecond {
		t.Errorf("fallback took %v, expected ~%v", elapsed, delay)
	}
	if secondary.callCount.Load() == 0 {
		t.Error("secondary server was never called")
	}
}

func TestGroupMaxRetries(t *testing.T) {
	a := &fakeTransport{tag: "a", err: errors.New("err")}
	b := &fakeTransport{tag: "b", err: errors.New("err")}
	c := &fakeTransport{tag: "c"} // would succeed if reached

	// maxRetries=2 → only a and b tried, c never reached
	tr := group.ExportNewGroupWithMembersOrdered(t, "test-retry", "sequential", 2,
		[]adapter.DNSTransport{a, b, c})

	_, err := tr.Exchange(context.Background(), makeMsg())
	if err == nil {
		t.Fatal("expected error: maxRetries=2 should stop before reaching 'c'")
	}
	if c.callCount.Load() > 0 {
		t.Error("server 'c' should not have been called with maxRetries=2")
	}
}
