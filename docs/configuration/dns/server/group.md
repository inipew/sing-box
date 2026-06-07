---
icon: material/server-network
---

# Group

`group` is a virtual DNS server that dispatches queries across a pool of member DNS servers using a pluggable load-balancing strategy and dispatch mode. It is inspired by dnscrypt-proxy's `lb_strategy` system.

### Structure

```json
{
  "dns": {
    "servers": [
      {
        "type": "group",
        "tag": "",
        "servers": [],
        "strategy": "wp2",
        "mode": "sequential",
        "fallback_delay": "300ms",
        "max_retries": 0,
        "health_check": {
          "interval": "10m",
          "timeout": "5s",
          "sample_size": 10
        }
      }
    ]
  }
}
```

### Fields

#### servers

Required.

List of DNS server tags to include in this group.

Restrictions:
- A group cannot contain another group.
- A group cannot contain a `fakeip` server.

#### detour

Forces all network-capable member transports in this group to route their connections through the specified outbound tag. Member transports do **not** need to declare their own `detour`.

This allows the same bare server definitions to be reused across multiple groups that use different proxies:

```json
// Define servers ONCE without detour
{ "type": "https", "tag": "cf_doh",     "server": "1.1.1.1", "path": "/dns-query" },
{ "type": "https", "tag": "google_doh", "server": "8.8.8.8", "path": "/dns-query" },

// Reuse same servers via different proxies
{ "type": "group", "tag": "dns_via_id", "servers": ["cf_doh", "google_doh"], "detour": "proxy-id" },
{ "type": "group", "tag": "dns_via_sg", "servers": ["cf_doh", "google_doh"], "detour": "proxy-sg" }
```

> **Note**: `local`, `hosts`, `fakeip`, and `dhcp` transport types do not use a network dialer and will be used as-is, unaffected by the `detour` setting.

#### strategy

The load-balancing strategy used to select servers for each query.

| Value | Description |
|-------|-------------|
| `wp2` | (Default) Weighted Power-of-Two. Picks two random servers and uses the one with the lower EWMA RTT. |
| `first` | Always selects the single lowest-RTT server. |
| `random` | Selects one server uniformly at random, ignoring RTT. |
| `round_robin` | Cycles through all servers in order. |
| `p2` | Picks one server at random from the top 2 sorted by EWMA RTT. |
| `ph` | Picks one server at random from the top half sorted by EWMA RTT. |
| `p<N>` | Picks one server at random from the top `N` sorted by EWMA RTT (e.g. `p3`). |

#### mode

The dispatch mode that controls how queries are sent to selected servers.

| Value | Description |
|-------|-------------|
| `sequential` | (Default) Tries servers one by one in the order determined by the `strategy` until one succeeds. |
| `concurrent` | Races all selected servers simultaneously; the first successful response wins. |
| `fallback` | Sends to the primary server first. After `fallback_delay`, it concurrently promotes the remaining servers (happy-eyeballs style). |

#### fallback_delay

The delay before promoting fallback servers in `fallback` mode.

Default: `300ms`.

#### max_retries

The maximum number of servers to try in `sequential` mode before returning an error. 
`0` means it will try all available servers.

#### health_check

Optional. Configures active latency probing.
When omitted, only passive RTT measurement from real queries is used.

##### interval

The interval between health-check probes.

Default: `10m`.

##### timeout

The timeout for each health-check probe.

Default: `5s`.

##### sample_size

The number of recent RTT samples used for calculating the Exponentially Weighted Moving Average (EWMA).

Default: `10`.
