### 结构

```json
{
  "type": "warp",
  "tag": "warp-out",

  "server": "engage.cloudflareclient.com",
  "server_port": 2408,
  "private_key": "",
  "public_key": "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=",
  "pre_shared_key": "",
  "address": [
    "172.16.0.2/32",
    "2606:4700:110:8f83:3cd3:bc68:e3b7:58bb/128"
  ],
  "reserved": [0, 0, 0],
  "mtu": 1280,
  "udp_timeout": "5m",
  "persistent_keepalive": 0,
  "workers": 4,
  "license": ""
}
```

### 字段

#### server

WARP 服务器地址。如果为空，将使用 Cloudflare WARP 默认地址 `engage.cloudflareclient.com`。

#### server_port

WARP 服务器端口。如果为空，将使用 Cloudflare WARP 默认端口 `2408`。

#### private_key

Base64 编码的 WireGuard 私钥。如果为空，将自动注册 Cloudflare 账号。

#### public_key

Base64 编码的 Peer 公钥。如果为空，将使用 Cloudflare WARP 默认公钥。

#### pre_shared_key

Base64 编码的 WireGuard 预共享密钥。可选。

#### address

分配给接口的 IP (v4 或 v6) 地址前缀列表。

#### reserved

WireGuard 保留字段字节。

#### mtu

最大传输单元（MTU）。如果为空，将使用 `1280`。

#### udp_timeout

UDP 连接超时时间。

#### persistent_keepalive

WireGuard 持续保活间隔（以秒为单位）。

#### workers

WireGuard 工作线程数。

#### inner_domain_resolver

内部域名解析器选项。详情请参见 [域名解析器选项](/configuration/shared/domain_resolve/)。

#### license

Cloudflare WARP 账户许可证密钥（例如，用于升级到 WARP+）。

---

### 工具

#### warp-export

`warp-export` 工具允许您导出存储在缓存数据库 (`cache.db`) 或 `warp.json` 中的 Cloudflare WARP 凭据/配置。

```shell
sing-box tools warp-export [cache-path] [flags]
```

##### 参数

- `[cache-path]`: 缓存数据库文件路径。默认为 `cache.db`。

##### 标志

- `-o`, `--outbound <tag>`: 指定要导出的 WARP 出站的 Tag（如果数据库中存储了多个账户，则此项为必填）。
- `-f`, `--format <format>`: 指定输出格式。
  - `wg`: WireGuard `.conf` 格式（默认）。
  - `json`: 原始 JSON 配置格式。
  - `sing`: 带预填凭据的 `sing-box` `warp` 出站配置。
  - `endpoint`: `sing-box` `wireguard` 出站端点配置。
- `--cache-id <id>`: 如果在 `experimental.cache_file.cache_id` 中定义了命名空间缓存 ID，则可通过此标志指定。
- `-l`, `--list`: 列出存储在缓存数据库中的所有 WARP 标签。
