### 结构

```json
{
  "type": "warp",
  "tag": "warp-ep",

  "system": false,
  "name": "",
  "server": "engage.cloudflareclient.com",
  "server_port": 2408,
  "private_key": "",
  "peer_public_key": "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=",
  "pre_shared_key": "",
  "address": [
    "172.16.0.2/32",
    "2606:4700:110:8f83:3cd3:bc68:e3b7:58bb/128"
  ],
  "reserved": [0, 0, 0],
  "mtu": 1280,
  "udp_timeout": "5m",
  "persistent_keepalive_interval": 0,
  "workers": 4,
  "provision": {
    "license": "",
    "cache_path": "",
    "recreate": false
  }
}
```

### 字段

#### system

可选。启用 WireGuard 系统接口。

#### name

可选。WireGuard 接口名称。

#### server

WARP 服务器地址。留空时使用 Cloudflare WARP 默认地址 `engage.cloudflareclient.com`。

#### server_port

WARP 服务器端口。留空时使用 Cloudflare WARP 默认端口 `2408`。

#### private_key

Base64 编码的 WireGuard 私钥。留空时自动向 Cloudflare 注册新账号。

#### peer_public_key

Base64 编码的对端公钥。留空时使用 Cloudflare WARP 默认公钥 `bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=`。

#### pre_shared_key

Base64 编码的 WireGuard 预共享密钥。可选。

#### address

分配给该接口的 IP (v4 或 v6) 前缀列表。

#### reserved

WireGuard 保留字节（必须为 3 个字节）。

#### mtu

最大传输单元。留空时使用 `1280`。

#### udp_timeout

UDP 连接超时时间。

#### persistent_keepalive_interval

WireGuard 保活间隔（秒）。默认 `25` 秒。

#### workers

WireGuard 工作线程数。

#### bootstrap_resolver

解析 WARP 服务器端点的域名解析选项。

#### provision

可选的 Cloudflare 账号自动注册设置：

- `license`: Cloudflare WARP 许可密钥 (如 WARP+)。
- `cache_path`: 自定义配置文件存储路径（默认 `warp.json`）。
- `recreate`: 若为 `true`，忽略缓存并重新注册。
