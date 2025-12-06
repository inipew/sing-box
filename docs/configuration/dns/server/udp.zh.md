---
icon: material/new-box
---

!!! question "自 sing-box 1.12.0 起"

# UDP

### 结构

```json
{
  "dns": {
    "servers": [
      {
        "type": "udp",
        "tag": "",

        "server": "",
        "server_port": 53,

        "upstreams": [
          {
            "server": "",
            "server_port": 53
          }
        ],

        // 拨号字段
      }
    ]
  }
}
```

!!! info "与旧版 UDP 服务器的区别"

    * 旧服务器默认使用默认出站，除非指定了绕行；新服务器像出站一样使用拨号器，相当于默认使用空的直连出站。
    * 旧服务器使用 `address_resolver` 和 `address_strategy` 来解析服务器中的域名；新服务器改用 [拨号字段](/zh/configuration/shared/dial/) 中的 `domain_resolver` 和 `domain_strategy`。

### 字段

#### server

`upstreams` 未配置时必填。

DNS 服务器的地址。填写此字段或在 `upstreams` 中提供至少一个上游地址。

如果使用域名，还必须设置 `domain_resolver` 来解析 IP 地址。

#### server_port

DNS 服务器的端口。

默认使用 `53`。

#### upstreams

额外的上游 DNS 端点。

每个条目使用与主地址相同的 `server` 与 `server_port` 字段。配置多个上游时，会按顺序轮换并在失败后继续尝试。只要提供至少一个上游，主 `server` 字段可以留空。

#### upstream_strategy

可选，控制多上游的负载均衡方式。

- `round_robin`（默认）：每次请求轮换上游。
- `random`：每次请求随机选择顺序。
- `fastest`：根据近期查询延迟优先选择最快上游。
- `fastest_random_two_thirds`：在最快的三分之二中随机分配，请求失败再退回其余上游。
- `parallel`：并行拨号所有上游，优先使用最先成功的连接。

### 拨号字段

参阅 [拨号字段](/zh/configuration/shared/dial/) 了解详情。