package group

import (
	"context"
	"time"

	"github.com/sagernet/sing-box/adapter"

	mDNS "github.com/miekg/dns"
)

// RTTSnapshot is a point-in-time copy of the RTT statistics for a single transport.
type RTTSnapshot struct {
	EWMA          float64
	Jitter        float64
	Failures      int
	TotalFailures int
	LastQueryTime time.Time
}

// RTTEstimator tracks exponentially-weighted moving-average RTT for member transports.
type RTTEstimator interface {
	// Record updates the EWMA RTT for the given transport tag after a successful exchange.
	Record(tag string, rtt time.Duration)

	// RecordFailure increments the consecutive failure counter for the given tag.
	RecordFailure(tag string)

	// Sorted returns a copy of tags sorted by ascending EWMA RTT.
	Sorted(tags []string) []string

	// Snapshot returns the current RTTSnapshot for a tag, or nil if unseen.
	Snapshot(tag string) *RTTSnapshot

	// AllSnapshots returns a map of tag -> RTTSnapshot for all known tags.
	AllSnapshots() map[string]RTTSnapshot
}

// Strategy picks an ordered candidate list from the full server pool for each query.
type Strategy interface {
	// Select returns a fully-ordered slice of transport tags.
	Select(tags []string, rtt RTTEstimator) []string

	// Name returns the human-readable strategy name.
	Name() string
}

// Dispatcher executes DNS queries using a specific distribution method (e.g. Sequential, Concurrent).
type Dispatcher interface {
	// Dispatch sends the DNS message to the selected transports according to the mode.
	Dispatch(ctx context.Context, message *mDNS.Msg, selected []string, byTag map[string]adapter.DNSTransport, rtt RTTEstimator) (*mDNS.Msg, error)
}
