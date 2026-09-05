### Structure

```json
{
  "type": "load-balance",
  "tag": "lb-auto",
  
  "outbounds": [
    "proxy-a",
    "proxy-b",
    "proxy-c"
  ],
  "strategy": "round-robin",
  "url": "",
  "interval": "",
  "idle_timeout": "",
  "timeout": "",
  "interrupt_exist_connections": false
}
```

### Fields

#### outbounds

==Required==

List of outbound tags to balance.

#### strategy

The load balancing strategy to use.

- `round-robin` (default): Distributes requests dynamically among the healthy outbounds in a latency-weighted rotating order. Faster nodes are assigned more requests proportionally.
- `consistent-hashing`: Directs requests with the same destination address to the same proxy node using a hash ring. The ring is scaled with latency-proportional Virtual Nodes so faster proxies handle a larger proportion of consistent hashed traffic.
- `sticky-sessions`: Directs requests with the same source address and destination address to the same proxy node, persistent for 10 minutes. Includes a dynamic least-connections re-balancing mechanism if the sticky node becomes overloaded or degrades.

#### url

The URL to test. `https://www.gstatic.com/generate_204` will be used if empty.

#### interval

The test interval. `3m` will be used if empty.

#### idle_timeout

The idle timeout. `30m` will be used if empty.

#### timeout

The health check timeout. `15s` will be used if empty.

#### interrupt_exist_connections

Interrupt existing connections when the selected outbound has changed.

Only inbound connections are affected by this setting, internal connections will always be interrupted.
