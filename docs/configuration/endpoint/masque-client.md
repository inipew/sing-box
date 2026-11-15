# MASQUE Client

!!! question "Since sing-box 1.15.0"

`masque-client` endpoint is an IP proxying over HTTP ([RFC 9484](https://datatracker.ietf.org/doc/html/rfc9484), CONNECT-IP) client.

## Structure

```json
{
  "type": "masque-client",
  "tag": "masque-client",

  "server": "127.0.0.1",
  "server_port": 443,
  "username": "",
  "password": "",
  "path": "",
  "headers": {},
  "version": 0,
  "disable_version_fallback": false,
  "tls": {},
  "advertise_routes": [],
  "protocol": "",
  "address": [],
  "http2_connect_mode": "",
  "capsule_include_context_id": true,
  "system": false,
  "name": "",
  "mtu": 1280,
  "on_demand": false,

  ... // HTTP2 Fields / QUIC Fields
  ... // UDP NAT Fields
  ... // Dial Fields
}
```

!!! note ""

    You can ignore the JSON Array [] tag when the content is only one item

## Fields

### server

==Required==

The server address.

### server_port

==Required==

The server port.

### username

Basic authorization username.

### password

Basic authorization password.

### path

URI template path of the IP proxying resource, may contain the `target` and `ipproto` variables.

`/.well-known/masque/ip/{target}/{ipproto}/` is used by default.

### headers

Extra headers of HTTP request.

### version

HTTP version.

Available values: `1`, `2`, `3`.

`3` is used by default.

When `2`, [QUIC Fields](#quic-fields) are replaced by [HTTP2 Fields](#http2-fields).

### disable_version_fallback

Disable automatic fallback to lower HTTP version.

### tls

TLS configuration, see [TLS](/configuration/shared/tls/#outbound).

Required for HTTP/3.

### advertise_routes

List of IP prefixes to advertise to the server.

The server will route traffic for these prefixes into this endpoint, where it is handled as inbound traffic.

### protocol

MASQUE upgrade token or CONNECT protocol identifier.

`connect-ip` is used by default.

Can be set to custom protocol tokens (e.g. `cf-connect-ip` for Cloudflare WARP).

### address

List of static IP prefixes assigned to this endpoint.

If not specified, dynamic address assignment via the `ADDRESS_REQUEST` capsule ([RFC 9484](https://datatracker.ietf.org/doc/html/rfc9484)) will be used. When static addresses are configured, dynamic address request is skipped.

### http2_connect_mode

CONNECT method mode when using HTTP/2.

Available values: `extended`, `plain`.

`extended` is used by default (Extended CONNECT per RFC 8441 / RFC 9484, using the `:protocol` pseudo-header).

When `plain`, standard HTTP CONNECT is used without `:protocol` and `Capsule-Protocol` request headers, for compatibility with non-standard proxies (e.g. Cloudflare WARP).

### capsule_include_context_id

Whether to include a Context ID (RFC 9297) prefix in DATAGRAM capsule payloads over the HTTP stream.

`true` is used by default.

When set to `false`, raw IP packets are framed directly in DATAGRAM capsules without the Context ID prefix, for compatibility with non-standard endpoints (e.g. Cloudflare WARP over HTTP/2).

### system

Use system interface.

Requires privilege and cannot conflict with existing system interfaces.

If disabled, sing-box uses the internal network stack.

### name

Custom interface name for system interface.

An automatically generated `masque` interface name is used by default.

### mtu

Tunnel MTU.

`1280` will be used by default.

### on_demand

Allow the endpoint to be disconnected when necessary.

## HTTP2 Fields

When `version` is `2`.

See [HTTP2 Fields](/configuration/shared/http2/) for details.

## QUIC Fields

When `version` is `3` (default).

See [QUIC Fields](/configuration/shared/quic/) for details.

## UDP NAT Fields

See [UDP NAT Fields](/configuration/shared/udp-nat/) for details.

## Dial Fields

See [Dial Fields](/configuration/shared/dial/) for details.
