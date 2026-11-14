package group

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/stretchr/testify/require"

	mDNS "github.com/miekg/dns"
)

type sharedConcurrencyTracker struct {
	active    atomic.Int64
	maxActive atomic.Int64
}

type concurrencyTestTransport struct {
	tag       string
	delay     time.Duration
	blockCh   chan struct{}
	err       error
	tracker   *sharedConcurrencyTracker
	calls     atomic.Int64
	active    atomic.Int64
	maxActive atomic.Int64
}

func (c *concurrencyTestTransport) Tag() string                                        { return c.tag }
func (c *concurrencyTestTransport) Type() string                                       { return "test" }
func (c *concurrencyTestTransport) Dependencies() []string                             { return nil }
func (c *concurrencyTestTransport) Reset()                                             {}
func (c *concurrencyTestTransport) Start(_ adapter.StartStage, _ *adapter.Scope) error { return nil }
func (c *concurrencyTestTransport) Close() error                                       { return nil }

func (c *concurrencyTestTransport) ExchangeAsync(ctx context.Context, msg *mDNS.Msg, callback func(response *mDNS.Msg, err error)) {
	callback(c.Exchange(ctx, msg))
}

func (c *concurrencyTestTransport) Exchange(ctx context.Context, msg *mDNS.Msg) (*mDNS.Msg, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.calls.Add(1)
	curr := c.active.Add(1)
	defer c.active.Add(-1)

	for {
		max := c.maxActive.Load()
		if curr <= max || c.maxActive.CompareAndSwap(max, curr) {
			break
		}
	}

	if c.tracker != nil {
		gcurr := c.tracker.active.Add(1)
		defer c.tracker.active.Add(-1)
		for {
			gmax := c.tracker.maxActive.Load()
			if gcurr <= gmax || c.tracker.maxActive.CompareAndSwap(gmax, gcurr) {
				break
			}
		}
	}

	if c.blockCh != nil {
		select {
		case <-c.blockCh:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	} else if c.delay > 0 {
		select {
		case <-time.After(c.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if c.err != nil {
		return nil, c.err
	}

	resp := new(mDNS.Msg)
	resp.SetReply(msg)
	resp.Rcode = mDNS.RcodeSuccess
	resp.Answer = append(resp.Answer, &mDNS.A{
		Hdr: mDNS.RR_Header{Name: "example.com.", Rrtype: mDNS.TypeA, Class: mDNS.ClassINET, Ttl: 60},
		A:   []byte{1, 2, 3, 4},
	})
	return resp, nil
}

// -------------------------------------------------------------
// P1-A: Atomic Half-Open Recovery Tests
// -------------------------------------------------------------

func TestHalfOpenAtomicReservation(t *testing.T) {
	now := time.Unix(100, 0)
	estimator := newDefaultRTTEstimator(4, 3, 30*time.Second, 5*time.Minute, func() time.Time { return now })
	reserver, ok := any(estimator).(halfOpenReserver)
	require.True(t, ok, "defaultRTTEstimator must implement halfOpenReserver")

	// 1. Closed circuit: cannot acquire
	release, acquired := reserver.TryAcquireHalfOpen("server1")
	require.False(t, acquired)
	require.Nil(t, release)

	// Cause 3 failures to trip the circuit to OPEN
	for range 3 {
		estimator.RecordFailure("server1")
	}
	require.Equal(t, "open", estimator.Snapshot("server1").State)

	// 2. Open circuit before cooldown: cannot acquire
	release, acquired = reserver.TryAcquireHalfOpen("server1")
	require.False(t, acquired)
	require.Nil(t, release)

	// Advance time past cooldown (30s) -> state becomes half_open
	now = now.Add(31 * time.Second)
	require.Equal(t, "half_open", estimator.Snapshot("server1").State)

	// 3. Concurrent acquisition attempts: exactly ONE must succeed
	const goroutines = 20
	var wg sync.WaitGroup
	startBarrier := make(chan struct{})
	var successfulAcquisitions atomic.Int32
	var winningRelease func()
	var mu sync.Mutex

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startBarrier
			rel, acq := reserver.TryAcquireHalfOpen("server1")
			if acq {
				successfulAcquisitions.Add(1)
				mu.Lock()
				winningRelease = rel
				mu.Unlock()
			}
		}()
	}

	close(startBarrier)
	wg.Wait()

	require.Equal(t, int32(1), successfulAcquisitions.Load(), "only one goroutine should acquire the half-open reservation")
	require.NotNil(t, winningRelease)

	// While reserved, further attempts must fail
	rel2, acq2 := reserver.TryAcquireHalfOpen("server1")
	require.False(t, acq2)
	require.Nil(t, rel2)

	// 4. Release reservation: next attempt should succeed
	winningRelease()
	rel3, acq3 := reserver.TryAcquireHalfOpen("server1")
	require.True(t, acq3)
	require.NotNil(t, rel3)
	rel3()
}

func TestHalfOpenProbeSuccessClosesCircuit(t *testing.T) {
	now := time.Unix(100, 0)
	estimator := newDefaultRTTEstimator(4, 1, time.Minute, 5*time.Minute, func() time.Time { return now })
	reserver := halfOpenReserver(estimator)

	estimator.RecordFailure("m")
	now = now.Add(time.Minute + time.Second)
	require.Equal(t, "half_open", estimator.Snapshot("m").State)

	release, acquired := reserver.TryAcquireHalfOpen("m")
	require.True(t, acquired)
	defer release()

	// Probe succeeds
	estimator.Record("m", 25*time.Millisecond)

	snap := estimator.Snapshot("m")
	require.Equal(t, "closed", snap.State)
	require.Equal(t, 0, snap.Failures)
}

func TestHalfOpenProbeFailureReopensCircuitWithBackoff(t *testing.T) {
	now := time.Unix(100, 0)
	estimator := newDefaultRTTEstimator(4, 1, 30*time.Second, 5*time.Minute, func() time.Time { return now })
	reserver := halfOpenReserver(estimator)

	estimator.RecordFailure("m")
	now = now.Add(31 * time.Second)
	require.Equal(t, "half_open", estimator.Snapshot("m").State)

	release, acquired := reserver.TryAcquireHalfOpen("m")
	require.True(t, acquired)

	// Probe fails -> circuit re-opens with doubled cooldown (60s)
	estimator.RecordFailure("m")
	release()

	snap := estimator.Snapshot("m")
	require.Equal(t, "open", snap.State)

	// Even though release was called, circuit is open again (in cooldown)
	_, acquiredAgain := reserver.TryAcquireHalfOpen("m")
	require.False(t, acquiredAgain)

	// Advance time past the new 60s cooldown -> half-open again
	now = now.Add(61 * time.Second)
	require.Equal(t, "half_open", estimator.Snapshot("m").State)
	rel2, acq2 := reserver.TryAcquireHalfOpen("m")
	require.True(t, acq2)
	rel2()
}

func TestConcurrentQueriesOnlyOneHalfOpenProbe(t *testing.T) {
	now := time.Unix(100, 0)
	healthy := &concurrencyTestTransport{tag: "healthy"}
	recovering := &concurrencyTestTransport{tag: "recovering", delay: 30 * time.Millisecond}

	groupTransport := buildGroupTransport(t, "group", "wp2", "hedge", 2, 100*time.Millisecond,
		[]adapter.DNSTransport{healthy, recovering})

	estimator := newDefaultRTTEstimator(4, 1, time.Minute, 5*time.Minute, func() time.Time { return now })
	estimator.Record("healthy", 10*time.Millisecond)
	estimator.RecordFailure("recovering")
	now = now.Add(time.Minute + time.Second)
	groupTransport.rtt = estimator

	require.Equal(t, "half_open", estimator.Snapshot("recovering").State)

	const queries = 25
	var wg sync.WaitGroup
	startBarrier := make(chan struct{})

	for range queries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startBarrier
			query := new(mDNS.Msg)
			query.SetQuestion("example.com.", mDNS.TypeA)
			resp, err := groupTransport.Exchange(context.Background(), query)
			require.NoError(t, err)
			require.NotNil(t, resp)
		}()
	}

	close(startBarrier)
	wg.Wait()

	// Exactly ONE recovery probe should have been dispatched to 'recovering'.
	// All other queries must have used 'healthy'.
	require.Equal(t, int64(1), recovering.calls.Load(), "recovering member should receive exactly 1 probe query")
	require.Equal(t, int64(1), recovering.maxActive.Load(), "max concurrent probes on recovering member must be 1")
	require.GreaterOrEqual(t, healthy.calls.Load(), int64(queries-1), "healthy member should serve the remaining queries")
	require.Equal(t, "closed", estimator.Snapshot("recovering").State)
}

func TestHalfOpenProbeCancellationReleasesReservation(t *testing.T) {
	now := time.Unix(100, 0)
	recoveringBlock := make(chan struct{})
	recovering := &concurrencyTestTransport{tag: "recovering", blockCh: recoveringBlock}
	healthy := &concurrencyTestTransport{tag: "healthy"}

	groupTransport := buildGroupTransport(t, "group", "first", "sequential", 2, 0,
		[]adapter.DNSTransport{recovering, healthy})

	estimator := newDefaultRTTEstimator(4, 1, time.Minute, 5*time.Minute, func() time.Time { return now })
	estimator.RecordFailure("recovering")
	now = now.Add(time.Minute + time.Second)
	groupTransport.rtt = estimator

	require.Equal(t, "half_open", estimator.Snapshot("recovering").State)

	// Query 1 starts and acquires the probe, but gets canceled while waiting
	ctx1, cancel1 := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		query := new(mDNS.Msg)
		query.SetQuestion("example.com.", mDNS.TypeA)
		_, err := groupTransport.Exchange(ctx1, query)
		require.ErrorIs(t, err, context.Canceled)
	}()

	// Wait for Query 1 to call recovering
	for recovering.calls.Load() == 0 {
		time.Sleep(2 * time.Millisecond)
	}

	// Cancel Query 1
	cancel1()
	wg.Wait()

	// Reservation must be released. Next query must be able to probe recovering
	close(recoveringBlock)
	query := new(mDNS.Msg)
	query.SetQuestion("example.com.", mDNS.TypeA)
	resp, err := groupTransport.Exchange(context.Background(), query)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, int64(2), recovering.calls.Load(), "recovering should be probed again by the second query")
}

// -------------------------------------------------------------
// P1-B: Global Concurrency Cap Tests
// -------------------------------------------------------------

func TestGlobalConcurrencyCapNeverExceeded(t *testing.T) {
	const maxConcurrent = 3
	tracker := &sharedConcurrencyTracker{}
	t1 := &concurrencyTestTransport{tag: "t1", delay: 20 * time.Millisecond, tracker: tracker}
	t2 := &concurrencyTestTransport{tag: "t2", delay: 20 * time.Millisecond, tracker: tracker}

	groupTransport := ExportNewGroupWithMaxConcurrent(t, "test", "wp2", "hedge", maxConcurrent,
		[]adapter.DNSTransport{t1, t2})

	const numQueries = 30
	var wg sync.WaitGroup
	startBarrier := make(chan struct{})

	for range numQueries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startBarrier
			query := new(mDNS.Msg)
			query.SetQuestion("example.com.", mDNS.TypeA)
			resp, err := groupTransport.Exchange(context.Background(), query)
			require.NoError(t, err)
			require.NotNil(t, resp)
		}()
	}

	close(startBarrier)
	wg.Wait()

	// Simultaneous active exchanges across all members at any time must not exceed maxConcurrent
	totalMaxActive := tracker.maxActive.Load()
	require.LessOrEqual(t, totalMaxActive, int64(maxConcurrent),
		"total simultaneous exchanges (%d) must not exceed maxConcurrent (%d)", totalMaxActive, maxConcurrent)

	// Verify all slots were released
	runtime := groupTransport.runtime.Load()
	require.NotNil(t, runtime)
	require.Empty(t, runtime.slots, "semaphore slots must be completely empty after all queries finish")
}

func TestGlobalConcurrencyAcrossModes(t *testing.T) {
	modes := []string{"failover", "hedge", "parallel"}

	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			const maxConcurrent = 2
			tracker := &sharedConcurrencyTracker{}
			t1 := &concurrencyTestTransport{tag: "m1", delay: 15 * time.Millisecond, tracker: tracker}
			t2 := &concurrencyTestTransport{tag: "m2", delay: 15 * time.Millisecond, tracker: tracker}

			modeStr := mode
			if mode == "failover" {
				modeStr = "sequential"
			} else if mode == "parallel" {
				modeStr = "concurrent"
			} else if mode == "hedge" {
				modeStr = "fallback"
			}

			groupTransport := ExportNewGroupWithMaxConcurrent(t, "test-"+mode, "round_robin", modeStr, maxConcurrent,
				[]adapter.DNSTransport{t1, t2})

			const numQueries = 15
			var wg sync.WaitGroup
			startBarrier := make(chan struct{})

			for range numQueries {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-startBarrier
					query := new(mDNS.Msg)
					query.SetQuestion("example.com.", mDNS.TypeA)
					resp, err := groupTransport.Exchange(context.Background(), query)
					require.NoError(t, err)
					require.NotNil(t, resp)
				}()
			}

			close(startBarrier)
			wg.Wait()

			totalMaxActive := tracker.maxActive.Load()
			require.LessOrEqual(t, totalMaxActive, int64(maxConcurrent),
				"mode %s: simultaneous exchanges (%d) must not exceed maxConcurrent (%d)", mode, totalMaxActive, maxConcurrent)
		})
	}
}

func TestGlobalConcurrencyCancellationWhileWaiting(t *testing.T) {
	const maxConcurrent = 1
	blockCh := make(chan struct{})
	t1 := &concurrencyTestTransport{tag: "t1", blockCh: blockCh}

	groupTransport := ExportNewGroupWithMaxConcurrent(t, "test", "first", "sequential", maxConcurrent,
		[]adapter.DNSTransport{t1})

	// Query 1 takes the single slot and blocks
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		query := new(mDNS.Msg)
		query.SetQuestion("example.com.", mDNS.TypeA)
		resp, err := groupTransport.Exchange(context.Background(), query)
		require.NoError(t, err)
		require.NotNil(t, resp)
	}()

	for t1.calls.Load() == 0 {
		time.Sleep(2 * time.Millisecond)
	}

	// Query 2 has a short timeout and will be stuck waiting for the slot
	ctx2, cancel2 := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel2()
	query2 := new(mDNS.Msg)
	query2.SetQuestion("example.com.", mDNS.TypeA)
	_, err2 := groupTransport.Exchange(ctx2, query2)
	require.ErrorIs(t, err2, context.DeadlineExceeded)

	// Unblock Query 1
	close(blockCh)
	wg.Wait()

	// Query 3 should be able to run immediately (slot was not leaked by Query 2)
	t1.blockCh = nil
	query3 := new(mDNS.Msg)
	query3.SetQuestion("example.com.", mDNS.TypeA)
	resp3, err3 := groupTransport.Exchange(context.Background(), query3)
	require.NoError(t, err3)
	require.NotNil(t, resp3)
}

func TestGlobalConcurrencyShutdownCancelsWaiters(t *testing.T) {
	const maxConcurrent = 1
	blockCh := make(chan struct{})
	t1 := &concurrencyTestTransport{tag: "t1", blockCh: blockCh}

	groupTransport := ExportNewGroupWithMaxConcurrent(t, "test", "first", "sequential", maxConcurrent,
		[]adapter.DNSTransport{t1})

	// Query 1 holds slot
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		query := new(mDNS.Msg)
		query.SetQuestion("example.com.", mDNS.TypeA)
		_, _ = groupTransport.Exchange(context.Background(), query)
	}()

	for t1.calls.Load() == 0 {
		time.Sleep(2 * time.Millisecond)
	}

	// Query 2 is waiting for slot
	query2Started := make(chan struct{})
	var err2 error
	wg.Add(1)
	go func() {
		defer wg.Done()
		close(query2Started)
		query := new(mDNS.Msg)
		query.SetQuestion("example.com.", mDNS.TypeA)
		_, err2 = groupTransport.Exchange(context.Background(), query)
	}()

	<-query2Started
	time.Sleep(10 * time.Millisecond)

	// Close the group while Query 1 is active and Query 2 is waiting
	closeErr := groupTransport.Close()
	require.NoError(t, closeErr)

	// Query 2 must wake up and return context canceled
	close(blockCh)
	wg.Wait()
	require.ErrorIs(t, err2, context.Canceled)
}

func TestGlobalConcurrencySlotReleasedOnError(t *testing.T) {
	const maxConcurrent = 1
	failTransport := &concurrencyTestTransport{tag: "fail", err: errors.New("upstream failed")}

	groupTransport := ExportNewGroupWithMaxConcurrent(t, "test", "first", "sequential", maxConcurrent,
		[]adapter.DNSTransport{failTransport})

	query := new(mDNS.Msg)
	query.SetQuestion("example.com.", mDNS.TypeA)
	_, err := groupTransport.Exchange(context.Background(), query)
	require.Error(t, err)

	// Slot must be free, so next query can run
	failTransport.err = nil
	resp, err2 := groupTransport.Exchange(context.Background(), query)
	require.NoError(t, err2)
	require.NotNil(t, resp)
}

// -------------------------------------------------------------
// Performance Benchmarks
// -------------------------------------------------------------

func BenchmarkSingleQueryOverhead(b *testing.B) {
	t1 := &concurrencyTestTransport{tag: "t1"}
	groupTransport := ExportNewGroupWithMaxConcurrent(b, "bench", "first", "sequential", 64,
		[]adapter.DNSTransport{t1})

	query := new(mDNS.Msg)
	query.SetQuestion("example.com.", mDNS.TypeA)
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = groupTransport.Exchange(ctx, query)
	}
}

func BenchmarkConcurrentThroughput(b *testing.B) {
	t1 := &concurrencyTestTransport{tag: "t1"}
	t2 := &concurrencyTestTransport{tag: "t2"}
	groupTransport := ExportNewGroupWithMaxConcurrent(b, "bench", "wp2", "hedge", 64,
		[]adapter.DNSTransport{t1, t2})

	query := new(mDNS.Msg)
	query.SetQuestion("example.com.", mDNS.TypeA)
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = groupTransport.Exchange(ctx, query)
		}
	})
}
