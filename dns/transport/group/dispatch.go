package group

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"

	mDNS "github.com/miekg/dns"
)

// SequentialDispatcher tries each selected server in order until one succeeds.
type SequentialDispatcher struct {
	Tag         string
	Logger      log.ContextLogger
	MaxRetries  int
	RetryRCodes map[int]bool
}

func (d *SequentialDispatcher) Dispatch(ctx context.Context, message *mDNS.Msg, selected []string, byTag map[string]adapter.DNSTransport, rtt RTTEstimator, checker adapter.DNSResponseChecker) (*mDNS.Msg, error) {
	limit := len(selected)
	if d.MaxRetries > 0 && d.MaxRetries < limit {
		limit = d.MaxRetries
	}
	var lastErr error
	attemptErrors := make(map[string]error, limit)
	for i := 0; i < limit; i++ {
		tag := selected[i]
		transport, ok := byTag[tag]
		if !ok {
			continue
		}
		rtt.Begin(tag)
		start := time.Now()
		resp, err := transport.Exchange(ctx, message.Copy())
		rtt.End(tag)
		if err == nil && acceptableResponse(message, resp, checker, d.RetryRCodes) {
			rtt.Record(tag, time.Since(start))
			return resp, nil
		}
		if err == nil {
			err = E.New("response rejected")
		}
		if !errors.Is(err, context.Canceled) {
			rtt.RecordFailure(tag)
		}
		d.Logger.DebugContext(ctx, "dns group[", d.Tag, "] sequential: server ", tag, " failed: ", err)
		lastErr = err
		attemptErrors[tag] = err
	}
	return nil, aggregateDispatchError(d.Tag, "all servers failed", selected[:limit], attemptErrors, lastErr)
}

// ConcurrentDispatcher races all selected servers and returns the first
// successful response, cancelling the remaining goroutines.
type ConcurrentDispatcher struct {
	Tag         string
	Logger      log.ContextLogger
	MaxRetries  int
	MaxInflight int
	RetryRCodes map[int]bool
}

func (d *ConcurrentDispatcher) Dispatch(ctx context.Context, message *mDNS.Msg, selected []string, byTag map[string]adapter.DNSTransport, rtt RTTEstimator, checker adapter.DNSResponseChecker) (*mDNS.Msg, error) {
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
	received := 0
	ch := make(chan result, len(selected))
	launch := func(tag string) bool {
		transport, ok := byTag[tag]
		if !ok {
			return false
		}
		launched++
		go func(tag string, transport adapter.DNSTransport) {
			rtt.Begin(tag)
			defer rtt.End(tag)
			start := time.Now()
			resp, err := transport.Exchange(ctx, message.Copy())
			ch <- result{tag: tag, resp: resp, rtt: time.Since(start), err: err}
		}(tag, transport)
		return true
	}
	maxInflight := d.MaxInflight
	if maxInflight <= 0 || maxInflight > len(selected) {
		maxInflight = len(selected)
	}
	nextCandidate := 0
	for nextCandidate < len(selected) && launched-received < maxInflight {
		launch(selected[nextCandidate])
		nextCandidate++
	}

	if launched == 0 {
		return nil, E.New("dns group[", d.Tag, "]: no valid transports to race")
	}

	var lastErr error
	attemptErrors := make(map[string]error, len(selected))
	for received < launched {
		select {
		case r := <-ch:
			received++
			if r.err == nil && acceptableResponse(message, r.resp, checker, d.RetryRCodes) {
				rtt.Record(r.tag, r.rtt)
				cancel() // signal remaining goroutines to stop
				return r.resp, nil
			}
			if r.err == nil {
				r.err = E.New("response rejected")
			}
			if !errors.Is(r.err, context.Canceled) {
				rtt.RecordFailure(r.tag)
			}
			lastErr = r.err
			attemptErrors[r.tag] = r.err
			for nextCandidate < len(selected) && launched-received < maxInflight {
				launch(selected[nextCandidate])
				nextCandidate++
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, aggregateDispatchError(d.Tag, "all concurrent servers failed", selected, attemptErrors, lastErr)
}

// FallbackDispatcher sends to the primary server first; if it does not respond
// within FallbackDelay, it concurrently promotes the remaining servers
// (happy-eyeballs style) and returns the first success.
type FallbackDispatcher struct {
	Tag           string
	Logger        log.ContextLogger
	FallbackDelay time.Duration
	MaxRetries    int
	MaxInflight   int
	RetryRCodes   map[int]bool
}

func (d *FallbackDispatcher) Dispatch(ctx context.Context, message *mDNS.Msg, selected []string, byTag map[string]adapter.DNSTransport, rtt RTTEstimator, checker adapter.DNSResponseChecker) (*mDNS.Msg, error) {
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
		rtt.Begin(selected[0])
		start := time.Now()
		resp, err := transport.Exchange(ctx, message.Copy())
		rtt.End(selected[0])
		if err == nil && acceptableResponse(message, resp, checker, d.RetryRCodes) {
			rtt.Record(selected[0], time.Since(start))
			return resp, nil
		}
		if err == nil {
			err = E.New("response rejected")
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
			rtt.Begin(tag)
			defer rtt.End(tag)
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
	received := 0

	nextCandidate := 1
	launchFallbacks := func() {
		if fallbackLaunched {
			return
		}
		fallbackLaunched = true
		maxInflight := d.MaxInflight
		if maxInflight <= 0 {
			maxInflight = len(selected)
		}
		for nextCandidate < len(selected) && launched-received < maxInflight {
			tag := selected[nextCandidate]
			nextCandidate++
			if launch(tag) {
				d.Logger.DebugContext(ctx, "dns group[", d.Tag, "] fallback: promoting server ", tag)
				launched++
			}
		}
	}

	var lastErr error
	attemptErrors := make(map[string]error, len(selected))

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
			if r.err == nil && acceptableResponse(message, r.resp, checker, d.RetryRCodes) {
				rtt.Record(r.tag, r.rtt)
				cancel()
				return r.resp, nil
			}
			if r.err == nil {
				r.err = E.New("response rejected")
			}
			if !errors.Is(r.err, context.Canceled) {
				rtt.RecordFailure(r.tag)
			}
			d.Logger.DebugContext(ctx, "dns group[", d.Tag, "] fallback: server ", r.tag, " failed: ", r.err)
			lastErr = r.err
			attemptErrors[r.tag] = r.err
			// If primary failed immediately, promote fallbacks right away.
			if r.tag == primary && !fallbackLaunched {
				launchFallbacks()
			} else if fallbackLaunched && nextCandidate < len(selected) {
				fallbackLaunched = false
				launchFallbacks()
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return nil, aggregateDispatchError(d.Tag, "all fallback servers failed", selected, attemptErrors, lastErr)
}

func aggregateDispatchError(groupTag string, summary string, selected []string, attemptErrors map[string]error, fallback error) error {
	parts := make([]string, 0, len(attemptErrors))
	for _, tag := range selected {
		if attemptErr, loaded := attemptErrors[tag]; loaded {
			parts = append(parts, fmt.Sprintf("%s: %v", tag, attemptErr))
		}
	}
	if len(parts) == 0 {
		return E.Cause(fallback, "dns group[", groupTag, "]: ", summary)
	}
	return &groupDispatchError{message: fmt.Sprintf("dns group[%s]: %s (%s)", groupTag, summary, strings.Join(parts, "; ")), cause: fallback}
}

type groupDispatchError struct {
	message string
	cause   error
}

func (e *groupDispatchError) Error() string { return e.message }
func (e *groupDispatchError) Unwrap() error { return e.cause }

func acceptableResponse(request *mDNS.Msg, response *mDNS.Msg, checker adapter.DNSResponseChecker, retryRCodes map[int]bool) bool {
	if response == nil || len(response.Question) != len(request.Question) {
		return false
	}
	for index := range request.Question {
		if response.Question[index] != request.Question[index] {
			return false
		}
	}
	if retryRCodes[response.Rcode] {
		return false
	}
	if response.Rcode == mDNS.RcodeSuccess || response.Rcode == mDNS.RcodeNameError {
		return checker == nil || checker(response)
	}
	return true
}
