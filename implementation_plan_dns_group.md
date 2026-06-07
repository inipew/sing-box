# DNS Group Transport — Concurrent Query / Load-Balance / Strategy

## Background

sing-box's DNS subsystem currently routes every query to **exactly one transport** as selected by matching rules or the default server. There is no built-in mechanism to:

- Send the same query to **multiple servers simultaneously** and take the first successful answer (race/concurrent mode).
- **Load-balance** across a pool of servers using latency-aware strategies (like dnscrypt-proxy's `lb_strategy`).
- **Fallback** with a delay (happy-eyeballs style) when the primary server is slow.
- **Retry** on failure across a server pool automatically.

This plan introduces a new DNS transport type: `dns_group` (type tag `"group"`). It acts as a **virtual transport** that wraps multiple real transports and manages query dispatch using pluggable strategies — directly analogous to dnscrypt-proxy's pool management.

---

## Architecture Overview

```
DNS Query
   │
   ▼
Router.exchangeWithRules()
   │  (selects a DNSTransport by tag)
   ▼
GroupTransport.Exchange()   ← NEW
   │
   ├─ RTT Estimator (background goroutine)
   │
   ├─ Server Selector (strategy)
   │     ├── first         – always fastest tracked server
   │     ├── random        – uniform random
   │     ├── round_robin   – cyclic
   │     ├── p2            – random from top-2 by RTT
   │     ├── ph            – random from top-half by RTT
   │     ├── pN            – random from top-N by RTT
   │     └── wp2           – weighted power-of-two (default)
   │
   └─ Dispatch Mode
         ├── sequential    – try servers one by one until success
         ├── concurrent    – race all / first-wins (cancel others)
         └── fallback      – primary first, promote others after delay
```

---

## Open Questions

> [!IMPORTANT]
> **Q1 — Type tag name**: Use `"group"` (parallel with outbound group types like `selector`/`urltest`) or a more descriptive `"dns_group"` or `"pool"`?

> [!IMPORTANT]
> **Q2 — RTT health check**: Should the group transport run *active* health-check probes (like outbound URLTest) using a configurable `health_check_url` and `interval`, or rely solely on *passive* RTT measurement from real queries?

> [!IMPORTANT]
> **Q3 — Cache interaction**: When `mode: concurrent`, multiple in-flight queries could each try to write the same cache key. Should the group transport coalesce responses at its level, or rely on the existing `cacheLock` in `dns/client.go` (which already deduplicates per transport-tag, so each member transport is independent)?

> [!NOTE]
> **Q4 — Scope**: Should `"group"` be allowed as `dns.final` / default transport? Current codebase forbids fakeip as default — group should be allowed as default.

---

## Proposed Changes

### 1. Constants

#### [MODIFY] [dns.go](file:///c:/Users/dhima/Downloads/sbx/constant/dns.go)

Add new constant:
```go
DNSTypeGroup = "group"
```

---

### 2. Option Structs

#### [MODIFY] [dns.go](file:///c:/Users/dhima/Downloads/sbx/option/dns.go)

Add `GroupDNSServerOptions`:

```go
// GroupDNSServerOptions configures a virtual DNS transport that dispatches
// queries across a pool of member transports using a pluggable strategy.
type GroupDNSServerOptions struct {
    // Servers lists the tags of member DNS transports.
    Servers []string `json:"servers"`

    // Strategy controls which server(s) are selected for each query.
    // Values: "first", "random", "round_robin", "p2", "ph", "p<N>", "wp2" (default).
    Strategy string `json:"strategy,omitempty"`

    // Mode controls how queries are dispatched.
    // Values: "sequential" (default), "concurrent", "fallback".
    Mode string `json:"mode,omitempty"`

    // FallbackDelay is the delay before promoting fallback servers in "fallback" mode.
    // Default: 300ms (happy-eyeballs style).
    FallbackDelay badoption.Duration `json:"fallback_delay,omitempty"`

    // MaxRetries is the maximum number of servers to try in "sequential" mode before
    // returning an error. 0 means try all servers.
    MaxRetries int `json:"max_retries,omitempty"`

    // HealthCheck configures active latency probing. When omitted, only passive
    // RTT measurement is used.
    HealthCheck *DNSGroupHealthCheckOptions `json:"health_check,omitempty"`
}

type DNSGroupHealthCheckOptions struct {
    // Interval between health-check probes. Default: 10m.
    Interval badoption.Duration `json:"interval,omitempty"`
    // Timeout for each probe. Default: 5s.
    Timeout badoption.Duration `json:"timeout,omitempty"`
    // SampleSize is the number of recent RTT samples to keep per server. Default: 10.
    SampleSize int `json:"sample_size,omitempty"`
}
```

---

### 3. RTT Estimator (New File)

#### [NEW] [dns/transport/group/rtt.go](file:///c:/Users/dhima/Downloads/sbx/dns/transport/group/rtt.go)

Implements a thread-safe, EWMA-based round-trip-time tracker per member server.

```go
// rttEstimator tracks exponentially-weighted moving average RTT for each server.
// It is used by strategy selectors to rank servers by performance.
type rttEstimator struct {
    mu      sync.RWMutex
    entries map[string]*rttEntry // keyed by transport tag
}

type rttEntry struct {
    ewma     float64       // milliseconds, EWMA
    samples  int           // total samples recorded
    failures int           // consecutive failure count
    lastSeen time.Time
}

// Record updates RTT for a transport after a successful exchange.
func (e *rttEstimator) Record(tag string, rtt time.Duration)

// RecordFailure increments the failure counter for a transport.
func (e *rttEstimator) RecordFailure(tag string)

// Sorted returns all transport tags sorted by ascending EWMA RTT,
// with transports that have too many consecutive failures deprioritized.
func (e *rttEstimator) Sorted(tags []string) []string
```

**EWMA formula**: `new_ewma = α * sample + (1-α) * old_ewma`, where `α = 2/(N+1)` and N = sample size (default 10).

---

### 4. Strategy Selector (New File)

#### [NEW] [dns/transport/group/strategy.go](file:///c:/Users/dhima/Downloads/sbx/dns/transport/group/strategy.go)

```go
type strategySelector interface {
    // Select returns an ordered list of transport tags to try for this query.
    // The list may be shorter than the full pool (e.g. "first" returns 1 tag).
    Select(tags []string, rtt *rttEstimator) []string
}

// Implementations:
//   strategyFirst      — returns [sortedTags[0]]
//   strategyRandom     — returns [randomPick(tags)]
//   strategyRoundRobin — cyclic, returns [tags[counter % len]]
//   strategyP2         — returns 1 random from top-2 sorted by RTT
//   strategyPH         — returns 1 random from top-half sorted by RTT
//   strategyPN         — returns 1 random from top-N sorted by RTT
//   strategyWP2        — weighted power-of-two: pick 2 random, return the better one

func newStrategy(name string) (strategySelector, error)
```

---

### 5. Group Transport (New File)

#### [NEW] [dns/transport/group/group.go](file:///c:/Users/dhima/Downloads/sbx/dns/transport/group/group.go)

Implements `adapter.DNSTransport`.

```go
type GroupTransport struct {
    ctx      context.Context
    tag      string
    logger   log.ContextLogger
    members  []adapter.DNSTransport   // resolved at Start()
    memberTags []string               // tags declared in config
    strategy strategySelector
    mode     dispatchMode             // sequential | concurrent | fallback
    fallbackDelay time.Duration
    maxRetries    int
    rtt      *rttEstimator
    healthCheck *healthChecker        // optional, nil if disabled
    access   sync.RWMutex
    started  bool
}

// Exchange dispatches the DNS message according to the configured mode:
//
//   sequential: iterate selector output, try each server, stop on first success.
//   concurrent: send to all selected servers simultaneously, return first success,
//               cancel the rest.
//   fallback:   send to primary, after fallbackDelay concurrently promote secondary
//               servers (happy-eyeballs), return first success.
func (t *GroupTransport) Exchange(ctx context.Context, message *dns.Msg) (*dns.Msg, error)

// Dependencies returns all member transport tags so the transport manager
// can start them before this group.
func (t *GroupTransport) Dependencies() []string { return t.memberTags }
```

**Concurrent mode** uses `context.WithCancel` + goroutines + a `chan result` buffered to N. The first non-error response cancels all remaining goroutines. RTT is recorded from actual exchange timing.

**Fallback mode** mirrors the outbound `fallback` group in `protocol/group/fallback.go` but adapted for DNS.

---

### 6. Health Checker (New File)

#### [NEW] [dns/transport/group/health.go](file:///c:/Users/dhima/Downloads/sbx/dns/transport/group/health.go)

Runs background probes to actively measure RTT (only instantiated when `health_check` is configured):

```go
type healthChecker struct {
    transports []adapter.DNSTransport
    interval   time.Duration
    timeout    time.Duration
    rtt        *rttEstimator
    ctx        context.Context
    cancel     context.CancelFunc
}

func (h *healthChecker) Start()
func (h *healthChecker) Close()
func (h *healthChecker) probe(transport adapter.DNSTransport)
```

Probe query: `A? health.example.` (a simple, unlikely-to-be-cached query) or user-configurable.

---

### 7. Registration

#### [MODIFY] [dns/transport/group/group.go](file:///c:/Users/dhima/Downloads/sbx/dns/transport/group/group.go)

Add registration function (same pattern as other transports):

```go
func RegisterTransport(registry *dns.TransportRegistry) {
    dns.RegisterTransport[option.GroupDNSServerOptions](
        registry,
        C.DNSTypeGroup,
        NewGroupTransport,
    )
}
```

#### [MODIFY] [include/registry.go](file:///c:/Users/dhima/Downloads/sbx/include/registry.go)

```go
import "github.com/sagernet/sing-box/dns/transport/group"
// ...
group.RegisterTransport(registry)
```

#### [MODIFY] [constant/dns.go](file:///c:/Users/dhima/Downloads/sbx/constant/dns.go)

```go
DNSTypeGroup = "group"
```

---

### 8. Transport Manager — Dependency Resolution

The transport manager's `startTransports()` already resolves dependencies in topological order. Since `GroupTransport.Dependencies()` returns member tags, group transports will automatically be started **after** all member transports. No changes needed to `transport_manager.go`.

However, the manager's `Remove()` method checks `dependByTag`. The group transport must register its member tags when `Create()` is called:

#### [MODIFY] [dns/transport_manager.go](file:///c:/Users/dhima/Downloads/sbx/dns/transport_manager.go)

No structural change needed — the existing `dependByTag` tracking in `Create()` already handles this correctly via `transport.Dependencies()`.

---

### 9. Router — Alias/Group Resolution

The router already looks up transports by tag. When a rule routes to a group tag, the group transport's `Exchange()` is called transparently. **No router changes are needed** — the group is just another `adapter.DNSTransport` from the router's perspective.

---

### 10. Adapter Interface — Optional Metrics

#### [MODIFY] [adapter/dns.go](file:///c:/Users/dhima/Downloads/sbx/adapter/dns.go)

Add an optional interface for transports that expose RTT metrics (for future API/dashboard use):

```go
// DNSTransportWithStats is optionally implemented by transports that
// track per-server latency and health metrics.
type DNSTransportWithStats interface {
    DNSTransport
    // Stats returns a snapshot of RTT and health metrics for each member.
    Stats() []DNSTransportMemberStats
}

type DNSTransportMemberStats struct {
    Tag            string
    AverageRTTMs   float64
    Failures       int
    LastQueryTime  time.Time
}
```

---

## Example Configuration

```json
{
  "dns": {
    "servers": [
      { "tag": "cloudflare", "type": "https", "server": "1.1.1.1", "path": "/dns-query" },
      { "tag": "google",     "type": "https", "server": "8.8.8.8", "path": "/dns-query" },
      { "tag": "quad9",      "type": "tls",   "server": "9.9.9.9" },
      {
        "tag":      "best-doh",
        "type":     "group",
        "servers":  ["cloudflare", "google", "quad9"],
        "strategy": "wp2",
        "mode":     "fallback",
        "fallback_delay": "300ms",
        "health_check": {
          "interval": "10m",
          "timeout":  "5s"
        }
      }
    ],
    "rules": [
      { "type": "default", "action": "route", "server": "best-doh" }
    ],
    "final": "best-doh"
  }
}
```

### Strategy Reference

| Strategy | Description | Best For |
|---|---|---|
| `wp2` *(default)* | Weighted power-of-two: pick 2 random servers, prefer the faster one | Best all-around balance |
| `first` | Always use the lowest-RTT server | Minimum latency, single point of failure |
| `p2` | Random pick from top-2 fastest | Good latency + mild redundancy |
| `ph` | Random pick from top-half | More diversity, still latency-aware |
| `p<N>` | Random pick from top-N fastest | Configurable balance |
| `random` | Uniform random from all members | Even distribution ignoring RTT |
| `round_robin` | Cyclic across all members | Strict load distribution |

### Mode Reference

| Mode | Description |
|---|---|
| `sequential` *(default)* | Try servers in strategy order; stop on first success |
| `concurrent` | Race all selected servers; first success wins, others cancelled |
| `fallback` | Primary first; after `fallback_delay`, promote remaining servers concurrently |

---

## File Summary

| File | Action | Purpose |
|---|---|---|
| `constant/dns.go` | MODIFY | Add `DNSTypeGroup = "group"` |
| `option/dns.go` | MODIFY | Add `GroupDNSServerOptions`, `DNSGroupHealthCheckOptions` |
| `adapter/dns.go` | MODIFY | Add `DNSTransportWithStats` optional interface |
| `dns/transport/group/rtt.go` | NEW | EWMA RTT estimator |
| `dns/transport/group/strategy.go` | NEW | Strategy selector implementations |
| `dns/transport/group/group.go` | NEW | Group transport (sequential/concurrent/fallback dispatch) |
| `dns/transport/group/health.go` | NEW | Active health-check background prober |
| `include/registry.go` | MODIFY | Register group transport |

---

## Verification Plan

### Automated Tests

- `dns/transport/group/group_test.go` — unit tests for each strategy and mode:
  - `TestGroupSequential_FirstServerSuccess`
  - `TestGroupSequential_FallsBackOnError`
  - `TestGroupConcurrent_ReturnsFirst`
  - `TestGroupConcurrent_CancelsOthers`
  - `TestGroupFallback_PromotesAfterDelay`
  - `TestRTTEstimator_EWMA`
  - `TestStrategy_WP2Selection`
  - `TestStrategy_RoundRobin`

```bash
go test ./dns/transport/group/... -v -race
go test ./dns/... -v -race
go build ./...
```

### Manual Verification

1. Configure a group with 3 DoH servers in `concurrent` mode, verify only one response is returned and timing < slowest server.
2. Set one member offline, verify `sequential` mode automatically retries the next server.
3. Use `health_check` + `first` strategy, verify traffic shifts to the lowest-RTT server over time.
4. Set `mode: fallback` with 500ms delay, verify fallback kicks in for a server with artificial 1s latency.
