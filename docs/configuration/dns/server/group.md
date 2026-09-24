---
icon: material/server-network
---

# Group

`group` is a virtual DNS server that selects and retries member DNS servers under one policy. A response only wins after protocol validation and DNS-rule response validation.

### Structure

```json
{
  "type": "group",
  "tag": "reliable-dns",
  "servers": ["cloudflare", "quad9", "google"],
  "detour": "proxy",
  "policy": "reliable",
  "advanced": {
    "selection": "adaptive",
    "execution": "hedge",
    "max_attempts": 3,
    "max_inflight": 2,
    "hedge_delay": "300ms",
    "retry_rcodes": ["SERVFAIL", "REFUSED"],
    "health": {
      "window_size": 20,
      "failure_threshold": 3,
      "cooldown": "30s",
      "max_cooldown": "5m",
      "active_probe": { "enabled": false, "interval": "10m", "timeout": "5s", "name": ".", "type": "NS" }
    }
  }
}
```

### Fields

#### servers

Required. Unique DNS server tags. A group cannot contain itself, another group, or a `fakeip` server.

#### detour

Optional outbound tag applied to every network-capable member. Startup fails if a network member cannot be cloned with the requested dialer. Explicitly networkless transports, such as `hosts`, are left unchanged.

#### policy

| Value | Behavior |
|-------|----------|
| `reliable` | Default. Adaptive selection with a second bounded attempt after 300 ms. |
| `low_latency` | Races healthy candidates and returns the first acceptable response. |
| `privacy` | Sends to one candidate at a time and only tries another after failure. |

#### advanced

Optional overrides for the selected preset:

- `selection`: `adaptive`, `ordered`, `random`, or `round_robin`.
- `execution`: `failover`, `hedge`, or `parallel`.
- `max_attempts`: maximum attempted members; cannot exceed the member count.
- `max_inflight`: maximum simultaneous attempts; must be `1` for `failover`.
- `hedge_delay`: required positive delay for `hedge` execution.
- `retry_rcodes`: replaces the retryable DNS RCODE set; defaults to `SERVFAIL` and `REFUSED`.
- `health.window_size`: rolling outcome window, default `20`.
- `health.failure_threshold`: consecutive failures before opening the circuit, default `3`.
- `health.cooldown`: initial open-circuit duration, default `30s`.
- `health.max_cooldown`: maximum exponential cooldown, default `5m`.
- `health.active_probe`: optional background probe. Disabled by default; its default query is `NS .`, every `10m`, with a `5s` timeout.

`NOERROR` and `NXDOMAIN` are protocol-valid terminal responses. `SERVFAIL`, `REFUSED`, malformed responses, transport failures, and responses rejected by DNS rules are retried by default. Cancellations caused by another member winning are not failures.

### Runtime status

When Clash API is enabled, `GET /dns/groups` lists group snapshots and `GET /dns/groups/{tag}` returns one group. Snapshots contain health and traffic counters but never query names.

### Migration

The former `strategy`, `mode`, `fallback_delay`, `max_retries`, and `health_check` fields are removed and produce a configuration error. Use `policy` and `advanced`.
