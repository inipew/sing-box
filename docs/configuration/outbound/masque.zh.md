# MASQUE

MASQUE 是基于 HTTP/3 Extended CONNECT 的 IP 隧道出站。

### Structure

```json
{
  "type": "masque",
  "tag": "masque-out",

  "server": "192.168.1.1",
  "server_port": 443,
  "system": false,
  "gso": false,
  "address": [
    "172.16.0.2/32"
  ],
  "private_key": "",
  "public_key": "",
  "uri": "https://cloudflareaccess.com",
  "sni": "consumer-masque.cloudflareclient.com",
  "skip_cert_verify": false,
  "mtu": 1280,
  "inner_domain_resolver": {}
}
```

### Fields

#### server

必填。服务器地址。

#### server_port

必填。服务器端口。

#### system

可选。启用平台/系统 TUN 栈。需要管理员/root 权限。

#### gso

可选。启用通用分段卸载（GSO）。仅在 `system: true` 时生效。

#### address

必填。本地虚拟网络接口的 IPv4 或 IPv6 地址前缀。

#### private_key

必填。Base64 编码的 ECDSA 私钥，用于 MASQUE 身份验证。

#### public_key

必填。Base64 编码的远程 MASQUE 端点 ECDSA 公钥。

#### uri

可选。HTTP/3 Extended CONNECT 的 URI（默认 `https://cloudflareaccess.com`）。

#### sni

可选。TLS 服务器名称指示（默认 `consumer-masque.cloudflareclient.com`）。

#### skip_cert_verify

可选。禁用 TLS 证书验证（不推荐）。

#### mtu

可选。虚拟网络接口的 MTU（默认 `1280`）。

#### inner_domain_resolver

可选。此选项与 [domain_resolver](/zh/configuration/shared/dial/#domain_resolver) 格式相同。

---

!!! note ""
    [拨号字段](../shared/dial.md)
