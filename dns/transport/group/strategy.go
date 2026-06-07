package group

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync/atomic"
)

// strategySelector picks an ordered candidate list from the full server pool
// for each query. The first element is the strategy's primary choice; the
// remaining elements are the fallback order for sequential/fallback modes.
// concurrent mode races all returned elements simultaneously.
type strategySelector interface {
	// Select returns a fully-ordered slice of transport tags.
	// The first element is the strategy's preferred primary.
	// Remaining elements follow in a sensible fallback order.
	// The returned slice always contains every tag exactly once.
	Select(tags []string, rtt *rttEstimator) []string

	// Name returns the human-readable strategy name.
	Name() string
}

// appendRemaining appends all elements of full that are not already in result.
func appendRemaining(result []string, full []string) []string {
	inResult := make(map[string]bool, len(result))
	for _, t := range result {
		inResult[t] = true
	}
	for _, t := range full {
		if !inResult[t] {
			result = append(result, t)
		}
	}
	return result
}

// strategyWP2 (Weighted Power-of-Two, default).
// Picks the better of two random candidates as primary; remaining servers
// follow in RTT-sorted order.
type strategyWP2 struct{}

func (s strategyWP2) Name() string { return "wp2" }

func (s strategyWP2) Select(tags []string, rtt *rttEstimator) []string {
	if len(tags) == 0 {
		return nil
	}
	if len(tags) == 1 {
		return []string{tags[0]}
	}
	sorted := rtt.Sorted(tags)

	// Pick 2 distinct random indices from the sorted list.
	i := rand.IntN(len(sorted))
	j := rand.IntN(len(sorted) - 1)
	if j >= i {
		j++
	}
	// Prefer the one with lower RTT index (better latency).
	var primary, secondary string
	if i < j {
		primary, secondary = sorted[i], sorted[j]
	} else {
		primary, secondary = sorted[j], sorted[i]
	}
	result := []string{primary, secondary}
	return appendRemaining(result, sorted)
}

// strategyFirst always puts the lowest-RTT server first.
type strategyFirst struct{}

func (s strategyFirst) Name() string { return "first" }

func (s strategyFirst) Select(tags []string, rtt *rttEstimator) []string {
	if len(tags) == 0 {
		return nil
	}
	return rtt.Sorted(tags)
}

// strategyRandom chooses a random primary; remaining servers follow in
// their original (unsorted) order.
type strategyRandom struct{}

func (s strategyRandom) Name() string { return "random" }

func (s strategyRandom) Select(tags []string, rtt *rttEstimator) []string {
	if len(tags) == 0 {
		return nil
	}
	idx := rand.IntN(len(tags))
	result := []string{tags[idx]}
	return appendRemaining(result, tags)
}

// strategyRoundRobin cycles through all servers as primary; remaining servers
// follow in their declaration order.
type strategyRoundRobin struct {
	counter atomic.Uint64
}

func (s *strategyRoundRobin) Name() string { return "round_robin" }

func (s *strategyRoundRobin) Select(tags []string, rtt *rttEstimator) []string {
	if len(tags) == 0 {
		return nil
	}
	idx := int(s.counter.Add(1)-1) % len(tags)
	result := []string{tags[idx]}
	return appendRemaining(result, tags)
}

// strategyPN picks a random primary from the top-N servers by EWMA RTT.
// Remaining servers follow in RTT-sorted order.
// n==0 means "top-half" (ph mode).
type strategyPN struct {
	n    int // 0 means "half"
	name string
}

func (s *strategyPN) Name() string { return s.name }

func (s *strategyPN) Select(tags []string, rtt *rttEstimator) []string {
	if len(tags) == 0 {
		return nil
	}
	sorted := rtt.Sorted(tags)
	n := s.n
	if n <= 0 {
		// ph: top-half (rounded up)
		n = (len(sorted) + 1) / 2
	}
	if n > len(sorted) {
		n = len(sorted)
	}
	// Pick one random server from the top-N as primary.
	idx := rand.IntN(n)
	result := []string{sorted[idx]}
	return appendRemaining(result, sorted)
}

// newStrategy parses a strategy name and returns the corresponding selector.
// Supported values: "wp2" (default), "first", "random", "round_robin",
// "p2", "ph", "p<N>" (e.g. "p3").
func newStrategy(name string) (strategySelector, error) {
	if name == "" {
		name = "wp2"
	}
	switch strings.ToLower(name) {
	case "wp2":
		return strategyWP2{}, nil
	case "first":
		return strategyFirst{}, nil
	case "random":
		return strategyRandom{}, nil
	case "round_robin", "rr":
		return &strategyRoundRobin{}, nil
	case "ph":
		return &strategyPN{n: 0, name: "ph"}, nil
	case "p2":
		return &strategyPN{n: 2, name: "p2"}, nil
	default:
		// Generic p<N> form
		if strings.HasPrefix(name, "p") {
			n, err := strconv.Atoi(name[1:])
			if err == nil && n >= 1 {
				return &strategyPN{n: n, name: name}, nil
			}
		}
		return nil, fmt.Errorf("unknown dns group strategy: %q (valid: wp2, first, random, round_robin, p2, ph, p<N>)", name)
	}
}
