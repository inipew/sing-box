---
icon: material/new-box
---

!!! question "Since sing-box 1.12.0"

# DNS over HTTPS (DoH)

### Structure

```json
{
  "dns": {
    "servers": [
      {
        "type": "https",
        "tag": "",

        "server": "",
        "server_port": 443,

        "upstreams": [
          {
            "server": "",
            "server_port": 443
          }
        ],

        "path": "",
        "headers": {},

        "tls": {},
        
        // Dial Fields
      }
    ]
  }
}
```

!!! info "Difference from legacy HTTPS server"

    * The old server uses default outbound by default unless detour is specified; the new one uses dialer just like outbound, which is equivalent to using an empty direct outbound by default.
    * The old server uses `address_resolver` and `address_strategy` to resolve the domain name in the server; the new one uses `domain_resolver` and `domain_strategy` in [Dial Fields](/configuration/shared/dial/) instead.

### Fields

#### server

Required unless `upstreams` is set.

The address of the DNS server. Provide either this field or at least one entry under `upstreams`.

For TLS handshakes, either supply `server` or set `tls.server_name` when only `upstreams` are provided.

If domain name is used, `domain_resolver` must also be set to resolve IP address.

#### server_port

The port of the DNS server.

`443` will be used by default.

#### upstreams

Additional upstream DNS endpoints.

Each entry accepts the same `server` and `server_port` fields as the primary address. When multiple upstreams are configured, they will be rotated and retried in order until a connection succeeds. The primary `server` field can be omitted if at least one upstream is provided.

#### upstream_strategy

Optional. Controls how multiple upstream addresses are balanced.

- `round_robin` (default): rotate upstreams for each request.
- `random`: pick a random order for each request.
- `fastest`: prefer the lowest-latency upstreams based on recent queries.
- `fastest_random_two_thirds`: shuffle requests among the fastest two thirds, then fall back to the rest.
- `parallel`: dial all upstreams in parallel and use the first successful connection.

#### path

The path of the DNS server.

`/dns-query` will be used by default.

#### headers

Additional headers to be sent to the DNS server.

#### tls

TLS configuration, see [TLS](/configuration/shared/tls/#outbound).

### Dial Fields

See [Dial Fields](/configuration/shared/dial/) for details.
