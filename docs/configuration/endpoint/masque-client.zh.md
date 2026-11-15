# MASQUE 客户端

!!! question "自 sing-box 1.15.0 起"

`masque-client` endpoint 是一个基于 HTTP 的 IP 代理（[RFC 9484](https://datatracker.ietf.org/doc/html/rfc9484)，CONNECT-IP）客户端。

## 结构

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

  ... // HTTP2 字段 / QUIC 字段
  ... // UDP NAT 字段
  ... // 拨号字段
}
```

!!! note ""

    当内容只有一项时，可以忽略 JSON 数组 [] 标签

## 字段

### server

==必填==

服务器地址。

### server_port

==必填==

服务器端口。

### username

Basic 认证用户名。

### password

Basic 认证密码。

### path

IP 代理资源的 URI 模板路径，可以包含 `target` 和 `ipproto` 变量。

默认使用 `/.well-known/masque/ip/{target}/{ipproto}/`。

### headers

HTTP 请求的额外标头。

### version

HTTP 版本。

可用值：`1`、`2`、`3`。

默认使用 `3`。

当为 `2` 时，[QUIC 字段](#quic-字段) 替换为 [HTTP2 字段](#http2-字段)。

### disable_version_fallback

禁用自动回退到更低的 HTTP 版本。

### tls

TLS 配置，参阅 [TLS](/zh/configuration/shared/tls/#出站)。

HTTP/3 需要 TLS。

### advertise_routes

向服务器声明的 IP 前缀列表。

服务器会把发往这些前缀的流量路由到此 endpoint，并在此作为入站流量处理。

### protocol

MASQUE upgrade 令牌或 CONNECT 协议标识符。

默认使用 `connect-ip`。

可设置为自定义协议令牌（例如 Cloudflare WARP 所用的 `cf-connect-ip`）。

### address

分配给此 endpoint 的静态 IP 前缀列表。

如果未指定，将通过 `ADDRESS_REQUEST` 胶囊（[RFC 9484](https://datatracker.ietf.org/doc/html/rfc9484)）进行动态地址分配。配置静态地址时，将跳过动态地址请求。

### http2_connect_mode

使用 HTTP/2 时的 CONNECT 方法模式。

可用值：`extended`、`plain`。

默认使用 `extended`（符合 RFC 8441 / RFC 9484 的扩展 CONNECT，使用 `:protocol` 伪标头）。

当为 `plain` 时，使用标准 HTTP CONNECT，不发送 `:protocol` 和 `Capsule-Protocol` 请求标头，用于兼容非标准代理（如 Cloudflare WARP）。

### capsule_include_context_id

是否在 HTTP 流上的 DATAGRAM 胶囊负载中包含 Context ID（RFC 9297）前缀。

默认使用 `true`。

当设为 `false` 时，DATAGRAM 胶囊中直接封装原始 IP 数据包而不添加 Context ID 前缀，用于兼容非标准端点（如 HTTP/2 上的 Cloudflare WARP）。

### system

使用系统接口。

需要特权且不能与已有系统接口冲突。

如果禁用，sing-box 将使用内部网络栈。

### name

系统接口的自定义接口名称。

默认使用自动生成的 `masque` 接口名称。

### mtu

隧道 MTU。

默认使用 `1280`。

### on_demand

允许该 endpoint 在需要时断开连接。

## HTTP2 字段

当 `version` 为 `2` 时。

参阅 [HTTP2 字段](/zh/configuration/shared/http2/)。

## QUIC 字段

当 `version` 为 `3`（默认）时。

参阅 [QUIC 字段](/zh/configuration/shared/quic/)。

## UDP NAT 字段

参阅 [UDP NAT 字段](/zh/configuration/shared/udp-nat/)。

## 拨号字段

参阅 [拨号字段](/zh/configuration/shared/dial/)。
