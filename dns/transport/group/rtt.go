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
	mu               sync.RWMutex
	entries          map[string]*rttEntry
	sampleSize       int
	alpha            float64
	failureThreshold int
	cooldown         time.Duration
	maxCooldown      time.Duration
	now              func() time.Time
}

type rttEntry struct {
	ewma            float64   // milliseconds, EWMA; 0 means no sample yet
	jitter          float64   // milliseconds, EWMA of absolute deviation
	samples         int       // total samples recorded
	failures        int       // consecutive failure count (reset on success)
	totalFailures   int       // lifetime failure count
	lastQueryTime   time.Time // wall time of last recorded sample or failure
	window          []bool
	totalAttempts   uint64
	circuitUntil    time.Time
	currentCooldown time.Duration
	lastSuccess     time.Time
	lastFailure     time.Time
	selected        uint64
	won             uint64
	inflight        int
	probeAttempts   uint64
	probeFailures   uint64
}

func newRTTEstimator(sampleSize int) RTTEstimator {
	return newDefaultRTTEstimator(sampleSize, 3, 30*time.Second, 5*time.Minute, time.Now)
}

func newDefaultRTTEstimator(sampleSize int, failureThreshold int, cooldown time.Duration, maxCooldown time.Duration, now func() time.Time) *defaultRTTEstimator {
	if sampleSize <= 0 {
		sampleSize = defaultSampleSize
	}
	return &defaultRTTEstimator{
		entries:          make(map[string]*rttEntry),
		sampleSize:       sampleSize,
		alpha:            2.0 / float64(sampleSize+1),
		failureThreshold: failureThreshold,
		cooldown:         cooldown,
		maxCooldown:      maxCooldown,
		now:              now,
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
	e.recordLatency(entry, ms)
	entry.won++
	entry.totalAttempts++
	entry.window = appendWindow(entry.window, true, e.sampleSize)
	entry.lastSuccess = entry.lastQueryTime
}

func (e *defaultRTTEstimator) recordLatency(entry *rttEntry, ms float64) {
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
	entry.circuitUntil = time.Time{}
	entry.currentCooldown = 0
	entry.lastQueryTime = e.now()
}

func (e *defaultRTTEstimator) RecordFailure(tag string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	entry := e.getOrCreate(tag)
	entry.failures++
	entry.totalFailures++
	entry.totalAttempts++
	entry.window = appendWindow(entry.window, false, e.sampleSize)
	entry.lastQueryTime = e.now()
	entry.lastFailure = entry.lastQueryTime
	if entry.failures >= e.failureThreshold {
		if entry.currentCooldown == 0 {
			entry.currentCooldown = e.cooldown
		} else {
			entry.currentCooldown = min(entry.currentCooldown*2, e.maxCooldown)
		}
		entry.circuitUntil = entry.lastQueryTime.Add(entry.currentCooldown)
	}
}

func (e *defaultRTTEstimator) Begin(tag string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	entry := e.getOrCreate(tag)
	entry.selected++
	entry.inflight++
}

func (e *defaultRTTEstimator) End(tag string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	entry := e.getOrCreate(tag)
	if entry.inflight > 0 {
		entry.inflight--
	}
}

func (e *defaultRTTEstimator) RecordProbe(tag string, rtt time.Duration, probeErr error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	entry := e.getOrCreate(tag)
	entry.probeAttempts++
	if probeErr == nil {
		ms := max(float64(rtt.Milliseconds()), 0)
		e.recordLatency(entry, ms)
		return
	}
	entry.probeFailures++
	entry.failures++
	entry.lastQueryTime = e.now()
	entry.lastFailure = entry.lastQueryTime
	if entry.failures >= e.failureThreshold {
		if entry.currentCooldown == 0 {
			entry.currentCooldown = e.cooldown
		} else {
			entry.currentCooldown = min(entry.currentCooldown*2, e.maxCooldown)
		}
		entry.circuitUntil = entry.lastQueryTime.Add(entry.currentCooldown)
	}
}

func appendWindow(window []bool, success bool, size int) []bool {
	window = append(window, success)
	if len(window) > size {
		window = window[len(window)-size:]
	}
	return window
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
			if !entry.circuitUntil.IsZero() && e.now().Before(entry.circuitUntil) {
				snaps[i].failures = max(snaps[i].failures, e.failureThreshold)
			}
		}
	}
	e.mu.RUnlock()

	sort.SliceStable(snaps, func(i, j int) bool {
		a, b := snaps[i], snaps[j]
		// Servers with too many consecutive failures sink to the bottom.
		aFailed := a.failures >= e.failureThreshold
		bFailed := b.failures >= e.failureThreshold
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
		return e.snapshot(entry)
	}
	return nil
}

func (e *defaultRTTEstimator) AllSnapshots() map[string]RTTSnapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make(map[string]RTTSnapshot, len(e.entries))
	for tag, entry := range e.entries {
		out[tag] = *e.snapshot(entry)
	}
	return out
}

func (e *defaultRTTEstimator) snapshot(entry *rttEntry) *RTTSnapshot {
	successes := 0
	for _, success := range entry.window {
		if success {
			successes++
		}
	}
	successRate := 0.0
	if len(entry.window) > 0 {
		successRate = float64(successes) / float64(len(entry.window))
	}
	state := "closed"
	if !entry.circuitUntil.IsZero() {
		if e.now().Before(entry.circuitUntil) {
			state = "open"
		} else {
			state = "half_open"
		}
	}
	return &RTTSnapshot{
		EWMA: entry.ewma, Jitter: entry.jitter, Failures: entry.failures,
		TotalFailures: entry.totalFailures, LastQueryTime: entry.lastQueryTime,
		SuccessRate: successRate, TotalAttempts: entry.totalAttempts,
		State: state, CircuitUntil: entry.circuitUntil,
		LastSuccess: entry.lastSuccess, LastFailure: entry.lastFailure,
		Selected: entry.selected, Won: entry.won, Inflight: entry.inflight,
		ProbeAttempts: entry.probeAttempts, ProbeFailures: entry.probeFailures,
	}
}
