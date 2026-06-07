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

// rttEstimator tracks exponentially-weighted moving-average RTT for each
// member transport tag. It is safe for concurrent use.
type rttEstimator struct {
	mu         sync.RWMutex
	entries    map[string]*rttEntry
	sampleSize int
	alpha      float64
}

type rttEntry struct {
	ewma          float64   // milliseconds, EWMA; 0 means no sample yet
	samples       int       // total samples recorded
	failures      int       // consecutive failure count (reset on success)
	totalFailures int       // lifetime failure count
	lastQueryTime time.Time // wall time of last recorded sample or failure
}

func newRTTEstimator(sampleSize int) *rttEstimator {
	if sampleSize <= 0 {
		sampleSize = defaultSampleSize
	}
	return &rttEstimator{
		entries:    make(map[string]*rttEntry),
		sampleSize: sampleSize,
		alpha:      2.0 / float64(sampleSize+1),
	}
}

// Record updates the EWMA RTT for the given transport tag after a successful
// exchange. rtt is the total round-trip duration observed by the caller.
func (e *rttEstimator) Record(tag string, rtt time.Duration) {
	ms := float64(rtt.Milliseconds())
	if ms < 0 {
		ms = 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	entry := e.getOrCreate(tag)
	if entry.samples == 0 || entry.ewma == 0 {
		entry.ewma = ms
	} else {
		entry.ewma = e.alpha*ms + (1-e.alpha)*entry.ewma
	}
	entry.samples++
	entry.failures = 0 // reset consecutive failures on success
	entry.lastQueryTime = time.Now()
}

// RecordFailure increments the consecutive failure counter for the given tag.
func (e *rttEstimator) RecordFailure(tag string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	entry := e.getOrCreate(tag)
	entry.failures++
	entry.totalFailures++
	entry.lastQueryTime = time.Now()
}

// getOrCreate returns the entry for the given tag, creating it if needed.
// Must be called with e.mu held.
func (e *rttEstimator) getOrCreate(tag string) *rttEntry {
	entry, ok := e.entries[tag]
	if !ok {
		entry = &rttEntry{}
		e.entries[tag] = entry
	}
	return entry
}

// Sorted returns a copy of tags sorted by ascending EWMA RTT.
// Tags with consecutive failures are moved toward the end.
// Tags with no samples yet are placed after measured tags but before failed ones.
func (e *rttEstimator) Sorted(tags []string) []string {
	e.mu.RLock()
	// snapshot to avoid holding lock during sort
	type snapshot struct {
		tag      string
		ewma     float64
		failures int
		hasSample bool
	}
	snaps := make([]snapshot, len(tags))
	for i, tag := range tags {
		entry, ok := e.entries[tag]
		if !ok {
			snaps[i] = snapshot{tag: tag, ewma: math.MaxFloat64, hasSample: false}
		} else {
			snaps[i] = snapshot{
				tag:       tag,
				ewma:      entry.ewma,
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
		return a.ewma < b.ewma
	})

	sorted := make([]string, len(snaps))
	for i, s := range snaps {
		sorted[i] = s.tag
	}
	return sorted
}

// Snapshot returns the current rttEntry for a tag (nil if unseen).
func (e *rttEstimator) Snapshot(tag string) *rttEntry {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if entry, ok := e.entries[tag]; ok {
		cp := *entry
		return &cp
	}
	return nil
}

// AllSnapshots returns a map of tag → copy of rttEntry for all known tags.
func (e *rttEstimator) AllSnapshots() map[string]rttEntry {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make(map[string]rttEntry, len(e.entries))
	for tag, entry := range e.entries {
		out[tag] = *entry
	}
	return out
}
