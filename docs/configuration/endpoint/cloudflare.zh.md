---
icon: material/cloud-outline
---

# Cloudflare Endpoint

Cloudflare 端点实现了 Cloudflare WARP MASQUE (Connect-IP) 协议。

### 结构

```json
{
  "type": "cloudflare",
  "tag": "cloudflare-out",

  "server": "engage.cloudflareclient.com",
  "server_port": 443,
  "private_key": "",
  "public_key": "",
  "certificate": "",
  "id": "10f85726-f693-4188-abe1-c1a3d0eab3de",
  "token": "a2bd13c1-71c5-47b7-9d5f-9c209bbdbeba",
  "remote_ip": "172.16.0.2",
  "remote_ipv6": "2606:4700:110:824e:c877:7af5:44e2:b14a",
  "mtu": 1280,
  "udp_timeout": "5m",

  ... // 拨号字段
}
```

### 字段

#### server

Cloudflare WARP 端点地址。

如果为空，将使用 `engage.cloudflareclient.com`。

#### server_port

Cloudflare WARP 端点端口。

如果为空，将使用 `443`。

#### private_key

==必填==

设备的 base64 编码 ECDSA 私钥。

#### public_key

==必填==

Cloudflare 端点的 base64 编码 ECDSA 公钥。

#### certificate

==必填==

设备的 base64 编码证书。

#### id

设备唯一标识符 (UUID)。

#### token

用于 API 访问的身份验证令牌 (license)。

#### remote_ip

Cloudflare 分配的 IPv4 地址。

#### remote_ipv6

Cloudflare 分配的 IPv6 地址。

#### mtu

最大传输单元 (MTU)。

如果为空，将使用 `1280`。

#### udp_timeout

QUIC 保活周期。

### 拨号字段

详情请参阅 [拨号字段](/configuration/shared/dial/)。
