package group

import (
	"context"
	"errors"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"

	mDNS "github.com/miekg/dns"
)

// SequentialDispatcher tries each selected server in order until one succeeds.
type SequentialDispatcher struct {
	Tag        string
	Logger     log.ContextLogger
	MaxRetries int
}

func (d *SequentialDispatcher) Dispatch(ctx context.Context, message *mDNS.Msg, selected []string, byTag map[string]adapter.DNSTransport, rtt RTTEstimator) (*mDNS.Msg, error) {
	limit := len(selected)
	if d.MaxRetries > 0 && d.MaxRetries < limit {
		limit = d.MaxRetries
	}
	var lastErr error
	for i := 0; i < limit; i++ {
		tag := selected[i]
		transport, ok := byTag[tag]
		if !ok {
			continue
		}
		start := time.Now()
		resp, err := transport.Exchange(ctx, message.Copy())
		if err == nil {
			rtt.Record(tag, time.Since(start))
			return resp, nil
		}
		if !errors.Is(err, context.Canceled) {
			rtt.RecordFailure(tag)
		}
		d.Logger.DebugContext(ctx, "dns group[", d.Tag, "] sequential: server ", tag, " failed: ", err)
		lastErr = err
	}
	return nil, E.Cause(lastErr, "dns group[", d.Tag, "]: all servers failed")
}

// ConcurrentDispatcher races all selected servers and returns the first
// successful response, cancelling the remaining goroutines.
type ConcurrentDispatcher struct {
	Tag        string
	Logger     log.ContextLogger
	MaxRetries int
}

func (d *ConcurrentDispatcher) Dispatch(ctx context.Context, message *mDNS.Msg, selected []string, byTag map[string]adapter.DNSTransport, rtt RTTEstimator) (*mDNS.Msg, error) {
	limit := len(selected)
	if d.MaxRetries > 0 && d.MaxRetries < limit {
		limit = d.MaxRetries
	}
	selected = selected[:limit]

	type result struct {
		tag  string
		resp *mDNS.Msg
		rtt  time.Duration
		err  error
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	launched := 0
	ch := make(chan result, len(selected))
	for _, tag := range selected {
		transport, ok := byTag[tag]
		if !ok {
			continue
		}
		launched++
		go func(tag string, transport adapter.DNSTransport) {
			start := time.Now()
			resp, err := transport.Exchange(ctx, message.Copy())
			ch <- result{tag: tag, resp: resp, rtt: time.Since(start), err: err}
		}(tag, transport)
	}

	if launched == 0 {
		return nil, E.New("dns group[", d.Tag, "]: no valid transports to race")
	}

	received := 0
	var lastErr error
	for received < launched {
		select {
		case r := <-ch:
			received++
			if r.err == nil {
				rtt.Record(r.tag, r.rtt)
				cancel() // signal remaining goroutines to stop
				return r.resp, nil
			}
			if !errors.Is(r.err, context.Canceled) {
				rtt.RecordFailure(r.tag)
			}
			lastErr = r.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, E.Cause(lastErr, "dns group[", d.Tag, "]: all concurrent servers failed")
}

// FallbackDispatcher sends to the primary server first; if it does not respond
// within FallbackDelay, it concurrently promotes the remaining servers
// (happy-eyeballs style) and returns the first success.
type FallbackDispatcher struct {
	Tag           string
	Logger        log.ContextLogger
	FallbackDelay time.Duration
	MaxRetries    int
}

func (d *FallbackDispatcher) Dispatch(ctx context.Context, message *mDNS.Msg, selected []string, byTag map[string]adapter.DNSTransport, rtt RTTEstimator) (*mDNS.Msg, error) {
	limit := len(selected)
	if d.MaxRetries > 0 && d.MaxRetries < limit {
		limit = d.MaxRetries
	}
	selected = selected[:limit]

	if len(selected) == 1 {
		// Only one candidate after limiting: attempt directly without fallback machinery.
		transport, ok := byTag[selected[0]]
		if !ok {
			return nil, E.New("dns group[", d.Tag, "]: single fallback transport not found: ", selected[0])
		}
		start := time.Now()
		resp, err := transport.Exchange(ctx, message.Copy())
		if err == nil {
			rtt.Record(selected[0], time.Since(start))
			return resp, nil
		}
		if !errors.Is(err, context.Canceled) {
			rtt.RecordFailure(selected[0])
		}
		return nil, E.Cause(err, "dns group[", d.Tag, "]: server ", selected[0], " failed")
	}

	type result struct {
		tag  string
		resp *mDNS.Msg
		rtt  time.Duration
		err  error
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Channel sized for all servers.
	ch := make(chan result, len(selected))

	launch := func(tag string) bool {
		transport, ok := byTag[tag]
		if !ok {
			return false
		}
		go func(tag string, tr adapter.DNSTransport) {
			start := time.Now()
			resp, err := tr.Exchange(ctx, message.Copy())
			ch <- result{tag: tag, resp: resp, rtt: time.Since(start), err: err}
		}(tag, transport)
		return true
	}

	// Launch primary immediately.
	primary := selected[0]
	launched := 0
	if launch(primary) {
		launched = 1
	}

	fallbackTimer := time.NewTimer(d.FallbackDelay)
	defer fallbackTimer.Stop()
	fallbackLaunched := false

	launchFallbacks := func() {
		if fallbackLaunched {
			return
		}
		fallbackLaunched = true
		for _, tag := range selected[1:] {
			if launch(tag) {
				d.Logger.DebugContext(ctx, "dns group[", d.Tag, "] fallback: promoting server ", tag)
				launched++
			}
		}
	}

	received := 0
	var lastErr error

	for {
		// Only exit when all launched goroutines have reported.
		if received >= launched && launched > 0 {
			break
		}
		select {
		case <-fallbackTimer.C:
			launchFallbacks()
		case r := <-ch:
			received++
			if r.err == nil {
				rtt.Record(r.tag, r.rtt)
				cancel()
				return r.resp, nil
			}
			if !errors.Is(r.err, context.Canceled) {
				rtt.RecordFailure(r.tag)
			}
			d.Logger.DebugContext(ctx, "dns group[", d.Tag, "] fallback: server ", r.tag, " failed: ", r.err)
			lastErr = r.err
			// If primary failed immediately, promote fallbacks right away.
			if r.tag == primary && !fallbackLaunched {
				launchFallbacks()
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return nil, E.Cause(lastErr, "dns group[", d.Tag, "]: all fallback servers failed")
}
