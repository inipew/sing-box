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

Required. A list of tags of member DNS servers.

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
