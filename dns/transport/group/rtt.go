package group

import (
	"math"
	"sort"
	"sync"
	"time"
)

const (
	defaultSampleSize = 10
	// Alpha for EWMA with defaultSampleSize: 2/(10+1) ≈ 0.18
)

// defaultRTTEstimator implements RTTEstimator and tracks exponentially-weighted moving-average RTT.
// It is safe for concurrent use.
type defaultRTTEstimator struct {
	mu         sync.RWMutex
	entries    map[string]*rttEntry
	sampleSize int
	alpha      float64
}

type rttEntry struct {
	ewma          float64   // milliseconds, EWMA; 0 means no sample yet
	jitter        float64   // milliseconds, EWMA of absolute deviation
	samples       int       // total samples recorded
	failures      int       // consecutive failure count (reset on success)
	totalFailures int       // lifetime failure count
	lastQueryTime time.Time // wall time of last recorded sample or failure
}

func newRTTEstimator(sampleSize int) RTTEstimator {
	if sampleSize <= 0 {
		sampleSize = defaultSampleSize
	}
	return &defaultRTTEstimator{
		entries:    make(map[string]*rttEntry),
		sampleSize: sampleSize,
		alpha:      2.0 / float64(sampleSize+1),
	}
}

func (e *defaultRTTEstimator) Record(tag string, rtt time.Duration) {
	ms := float64(rtt.Milliseconds())
	if ms < 0 {
		ms = 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	entry := e.getOrCreate(tag)
	if entry.samples == 0 || entry.ewma == 0 {
		entry.ewma = ms
		entry.jitter = 0 // No jitter estimate yet on first sample
	} else {
		diff := ms - entry.ewma
		entry.jitter = e.alpha*math.Abs(diff) + (1-e.alpha)*entry.jitter
		entry.ewma = e.alpha*ms + (1-e.alpha)*entry.ewma
	}
	entry.samples++
	entry.failures = 0 // reset consecutive failures on success
	entry.lastQueryTime = time.Now()
}

func (e *defaultRTTEstimator) RecordFailure(tag string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	entry := e.getOrCreate(tag)
	entry.failures++
	entry.totalFailures++
	entry.lastQueryTime = time.Now()
}

// getOrCreate returns the entry for the given tag, creating it if needed.
// Must be called with e.mu held.
func (e *defaultRTTEstimator) getOrCreate(tag string) *rttEntry {
	entry, ok := e.entries[tag]
	if !ok {
		entry = &rttEntry{}
		e.entries[tag] = entry
	}
	return entry
}

func (e *defaultRTTEstimator) Sorted(tags []string) []string {
	e.mu.RLock()
	type snapshot struct {
		tag       string
		score     float64
		failures  int
		hasSample bool
	}
	snaps := make([]snapshot, len(tags))
	for i, tag := range tags {
		entry, ok := e.entries[tag]
		if !ok {
			// Unseen server: score = 0 so it sorts first (explore phase).
			snaps[i] = snapshot{tag: tag, score: 0, hasSample: false}
		} else {
			snaps[i] = snapshot{
				tag:       tag,
				score:     entry.ewma + (2.0 * entry.jitter),
				failures:  entry.failures,
				hasSample: entry.samples > 0,
			}
		}
	}
	e.mu.RUnlock()

	sort.SliceStable(snaps, func(i, j int) bool {
		a, b := snaps[i], snaps[j]
		// Servers with too many consecutive failures sink to the bottom.
		aFailed := a.failures >= 3
		bFailed := b.failures >= 3
		if aFailed != bFailed {
			return !aFailed
		}
		// Prefer servers that have NOT been sampled (explore phase).
		if a.hasSample != b.hasSample {
			return !a.hasSample
		}
		return a.score < b.score
	})

	sorted := make([]string, len(snaps))
	for i, s := range snaps {
		sorted[i] = s.tag
	}
	return sorted
}

func (e *defaultRTTEstimator) Snapshot(tag string) *RTTSnapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if entry, ok := e.entries[tag]; ok {
		return &RTTSnapshot{
			EWMA:          entry.ewma,
			Jitter:        entry.jitter,
			Failures:      entry.failures,
			TotalFailures: entry.totalFailures,
			LastQueryTime: entry.lastQueryTime,
		}
	}
	return nil
}

func (e *defaultRTTEstimator) AllSnapshots() map[string]RTTSnapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make(map[string]RTTSnapshot, len(e.entries))
	for tag, entry := range e.entries {
		out[tag] = RTTSnapshot{
			EWMA:          entry.ewma,
			Jitter:        entry.jitter,
			Failures:      entry.failures,
			TotalFailures: entry.totalFailures,
			LastQueryTime: entry.lastQueryTime,
		}
	}
	return out
}
