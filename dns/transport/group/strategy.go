package group

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"sync/atomic"
)

// appendRemaining appends all elements of full that are not already in result.
func appendRemaining(result []string, full []string) []string {
	for _, t := range full {
		found := false
		for _, r := range result {
			if t == r {
				found = true
				break
			}
		}
		if !found {
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

func (s strategyWP2) Select(tags []string, rtt RTTEstimator) []string {
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
	result := make([]string, 0, len(sorted))
	result = append(result, primary, secondary)
	return appendRemaining(result, sorted)
}

// strategyFirst always puts the lowest-RTT server first.
type strategyFirst struct{}

func (s strategyFirst) Name() string { return "first" }

func (s strategyFirst) Select(tags []string, rtt RTTEstimator) []string {
	if len(tags) == 0 {
		return nil
	}
	return rtt.Sorted(tags)
}

// strategyRandom chooses a random primary; remaining servers follow in
// their original (unsorted) order.
type strategyRandom struct{}

func (s strategyRandom) Name() string { return "random" }

func (s strategyRandom) Select(tags []string, rtt RTTEstimator) []string {
	if len(tags) == 0 {
		return nil
	}
	idx := rand.IntN(len(tags))
	sorted := rtt.Sorted(tags)
	result := make([]string, 0, len(sorted))
	result = append(result, tags[idx])
	// Remaining servers follow in RTT-sorted order so sequential dispatcher
	// always falls back to the fastest available server.
	return appendRemaining(result, sorted)
}

// strategyRoundRobin cycles through all servers as primary; remaining servers
// follow in their declaration order.
type strategyRoundRobin struct {
	counter atomic.Uint64
}

func (s *strategyRoundRobin) Name() string { return "round_robin" }

func (s *strategyRoundRobin) Select(tags []string, rtt RTTEstimator) []string {
	if len(tags) == 0 {
		return nil
	}
	// Use uint64 modulo before converting to int to prevent negative-index
	// panic on counter overflow (uint64 wraps → int64(-1) → -1 % n = -1).
	idx := int(s.counter.Add(1) % uint64(len(tags)))
	sorted := rtt.Sorted(tags)
	result := make([]string, 0, len(sorted))
	result = append(result, tags[idx])
	// Remaining servers follow in RTT-sorted order.
	return appendRemaining(result, sorted)
}

// strategyWeighted picks a primary server with probability inversely proportional to its EWMA RTT.
type strategyWeighted struct{}

func (s strategyWeighted) Name() string { return "weighted" }

func (s strategyWeighted) Select(tags []string, rtt RTTEstimator) []string {
	if len(tags) == 0 {
		return nil
	}
	if len(tags) == 1 {
		return []string{tags[0]}
	}

	snapshots := rtt.AllSnapshots()
	weights := make([]float64, len(tags))
	totalWeight := 0.0

	for i, tag := range tags {
		snap, ok := snapshots[tag]
		var w float64
		if !ok || snap.EWMA <= 0 {
			// Unseen server: give it a generous initial weight (equiv. 10ms).
			// It will be explored by epsilon_greedy or naturally through fallback.
			w = 1.0 / 10.0
		} else {
			// Score = EWMA + 2*Jitter (same formula as Sorted).
			score := snap.EWMA + (2.0 * snap.Jitter)
			if score < 1.0 {
				score = 1.0
			}
			w = 1.0 / score

			// Penalize servers with consecutive failures: divide weight by 10 per failure,
			// capped at 3 failures so the server still occasionally gets a probe.
			if snap.Failures > 0 {
				penalty := snap.Failures
				if penalty > 3 {
					penalty = 3
				}
				w /= float64(penalty) * 10.0
			}
		}

		weights[i] = w
		totalWeight += w
	}

	r := rand.Float64() * totalWeight
	sorted := rtt.Sorted(tags)
	var primary string
	sum := 0.0
	for i, w := range weights {
		sum += w
		if r <= sum {
			primary = tags[i]
			break
		}
	}
	// Floating-point accumulation may not reach totalWeight exactly.
	// Fall back to the last element in sorted order (best available fallback).
	if primary == "" {
		primary = sorted[len(sorted)-1]
	}

	result := make([]string, 0, len(sorted))
	result = append(result, primary)
	return appendRemaining(result, sorted)
}

// strategyEpsilonGreedy picks the absolute best server most of the time (1 - epsilon),
// but occasionally picks a completely random server (epsilon) to explore.
type strategyEpsilonGreedy struct {
	epsilon float64
}

func (s strategyEpsilonGreedy) Name() string { return "epsilon_greedy" }

func (s strategyEpsilonGreedy) Select(tags []string, rtt RTTEstimator) []string {
	if len(tags) == 0 {
		return nil
	}
	sorted := rtt.Sorted(tags)
	if len(tags) == 1 {
		return sorted
	}

	var primary string
	if rand.Float64() < s.epsilon {
		// Explore
		idx := rand.IntN(len(tags))
		primary = tags[idx]
	} else {
		// Exploit
		primary = sorted[0]
	}

	result := make([]string, 0, len(sorted))
	result = append(result, primary)
	return appendRemaining(result, sorted)
}

// NewStrategy parses a strategy name and returns the corresponding selector.
// Supported values: "wp2" (default), "first", "random", "round_robin", "weighted", "epsilon_greedy".
func NewStrategy(name string) (Strategy, error) {
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
	case "weighted":
		return strategyWeighted{}, nil
	case "epsilon_greedy":
		return strategyEpsilonGreedy{epsilon: 0.1}, nil
	default:
		return nil, fmt.Errorf("unknown dns group strategy: %q (valid: wp2, first, random, round_robin, weighted, epsilon_greedy)", name)
	}
}
