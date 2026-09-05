### Structure

```json
{
  "type": "fallback",
  "tag": "fallback-auto",
  
  "outbounds": [
    "proxy-a",
    "proxy-b",
    "proxy-c"
  ],
  "url": "",
  "interval": "",
  "idle_timeout": "",
  "timeout": "",
  "fallback_delay": "",
  "interrupt_exist_connections": false
}
```

### Fields

#### outbounds

==Required==

List of outbound tags to test.

#### url

The URL to test. `https://www.gstatic.com/generate_204` will be used if empty.

#### interval

The test interval. `3m` will be used if empty.

#### idle_timeout

The idle timeout. `30m` will be used if empty.

#### timeout

The health check timeout. `15s` will be used if empty.

#### fallback_delay

The parallel dial delay. If non-zero (e.g., `300ms`), backup outbounds will be dialed in parallel after this delay if the primary outbound is slow to connect (Happy Eyeballs). If `0` or empty, sequential dial retry will be used instead.

#### interrupt_exist_connections

Interrupt existing connections when the selected outbound has changed.

Only inbound connections are affected by this setting, internal connections will always be interrupted.
