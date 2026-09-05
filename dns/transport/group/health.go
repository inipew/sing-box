package group

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"

	mDNS "github.com/miekg/dns"
)

const (
	defaultHealthCheckInterval = 10 * time.Minute
	defaultHealthCheckTimeout  = 5 * time.Second
	maxHealthCheckConcurrency  = 4

	// healthCheckQuery is the probe domain. Using "." (root) with TypeNS is a
	// lightweight query that almost all resolvers answer quickly.
	healthCheckQuery = "."
)

// HealthChecker runs periodic active probes to all member transports and
// records RTT results into the shared RTTEstimator.
type HealthChecker struct {
	members  []adapter.DNSTransport
	interval time.Duration
	timeout  time.Duration
	rtt      RTTEstimator
	logger   log.ContextLogger
	ctx      context.Context
	cancel   context.CancelFunc
	waiter   sync.WaitGroup
}

func NewHealthChecker(
	parentCtx context.Context,
	members []adapter.DNSTransport,
	opts *option.DNSGroupHealthCheckOptions,
	rtt RTTEstimator,
	logger log.ContextLogger,
) *HealthChecker {
	interval := time.Duration(opts.Interval)
	if interval == 0 {
		interval = defaultHealthCheckInterval
	}
	timeout := time.Duration(opts.Timeout)
	if timeout == 0 {
		timeout = defaultHealthCheckTimeout
	}
	ctx, cancel := context.WithCancel(parentCtx)
	return &HealthChecker{
		members:  members,
		interval: interval,
		timeout:  timeout,
		rtt:      rtt,
		logger:   logger,
		ctx:      ctx,
		cancel:   cancel,
	}
}

// Start launches the background health-check goroutine.
func (h *HealthChecker) Start() {
	h.waiter.Add(1)
	go func() {
		defer h.waiter.Done()
		h.loop()
	}()
}

// Close stops the background health-check goroutine.
func (h *HealthChecker) Close() {
	h.cancel()
	h.waiter.Wait()
}

func (h *HealthChecker) loop() {
	// Small initial delay so we don't hit the network at startup before
	// connections are warmed up.
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-h.ctx.Done():
			return
		case <-timer.C:
			h.probeAll()
			timer.Reset(h.interval)
		}
	}
}

func (h *HealthChecker) probeAll() {
	workerCount := min(len(h.members), maxHealthCheckConcurrency)
	if workerCount == 0 {
		return
	}
	jobs := make(chan adapter.DNSTransport)
	var waiter sync.WaitGroup
	for range workerCount {
		waiter.Add(1)
		go func() {
			defer waiter.Done()
			for transport := range jobs {
				h.probe(transport)
			}
		}()
	}
	for _, transport := range h.members {
		select {
		case jobs <- transport:
		case <-h.ctx.Done():
			close(jobs)
			waiter.Wait()
			return
		}
	}
	close(jobs)
	waiter.Wait()
}

// probe sends a lightweight NS query for "." to the given transport and
// records the RTT. NXDOMAIN / SERVFAIL still gives a valid RTT measurement;
// only network-level errors are counted as failures.
func (h *HealthChecker) probe(transport adapter.DNSTransport) {
	msg := &mDNS.Msg{
		MsgHdr: mDNS.MsgHdr{
			RecursionDesired: true,
		},
		Question: []mDNS.Question{{
			Name:   healthCheckQuery,
			Qtype:  mDNS.TypeNS,
			Qclass: mDNS.ClassINET,
		}},
	}

	probeCtx, cancel := context.WithTimeout(h.ctx, h.timeout)
	defer cancel()

	tag := transport.Tag()
	start := time.Now()
	_, err := transport.Exchange(probeCtx, msg)
	elapsed := time.Since(start)

	if err != nil {
		if !errors.Is(err, context.Canceled) {
			h.rtt.RecordFailure(tag)
			h.logger.Debug("dns health check [", tag, "] failed: ", err)
		}
		return
	}
	h.rtt.Record(tag, elapsed)
	h.logger.Debug("dns health check [", tag, "] ok: ", elapsed.Milliseconds(), "ms")
}
