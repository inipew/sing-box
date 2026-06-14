---
icon: material/server-network
---

# Group

`group` 虚拟 DNS 服务器通过可插拔的负载均衡策略和分发模式，在一组 DNS 服务器成员之间分发查询。其灵感来源于 dnscrypt-proxy 的 `lb_strategy` 系统。

### 结构

```json
{
  "dns": {
    "servers": [
      {
        "type": "group",
        "tag": "",
        "servers": [],
        "strategy": "wp2",
        "mode": "sequential",
        "fallback_delay": "300ms",
        "max_retries": 0,
        "health_check": {
          "interval": "10m",
          "timeout": "5s",
          "sample_size": 10
        }
      }
    ]
  }
}
```

### 字段

#### servers

必填

此组包含的 DNS 服务器 tag 列表。

限制：
- 组内不能包含另一个组。
- 组内不能包含 `fakeip` 类型的服务器。

#### detour

强制此组内所有支持网络连接的成员 transport 通过指定的出站 (outbound) tag 进行路由。成员 transport **无需**在自身配置中声明 `detour`。

此功能允许同一组 DNS 服务器定义被多个使用不同代理的组复用：

```json
// 服务器仅定义一次，无需设置 detour
{ "type": "https", "tag": "cf_doh",     "server": "1.1.1.1", "path": "/dns-query" },
{ "type": "https", "tag": "google_doh", "server": "8.8.8.8", "path": "/dns-query" },

// 通过不同代理复用相同服务器
{ "type": "group", "tag": "dns_via_id", "servers": ["cf_doh", "google_doh"], "detour": "proxy-id" },
{ "type": "group", "tag": "dns_via_sg", "servers": ["cf_doh", "google_doh"], "detour": "proxy-sg" }
```

> **注意**：`local`、`hosts`、`fakeip`、`dhcp` 类型的 transport 不使用网络 dialer，将直接使用而不受 `detour` 设置影响。

#### strategy

用于为每个查询选择服务器的负载均衡策略。

| 值 | 描述 |
|-------|-------------|
| `wp2` | (默认) 加权 2 次方 (Weighted Power-of-Two)。随机选择两台服务器，并使用 EWMA RTT 较低的一台。 |
| `first` | 始终选择 EWMA RTT 最低的一台服务器。 |
| `random` | 忽略 RTT，均匀随机地选择一台服务器。 |
| `round_robin` | 轮询，按顺序循环使用所有服务器。 |
| `weighted` | 根据 EWMA RTT 成反比分配概率。更快的服务器会按比例接收更多流量。 |
| `epsilon_greedy` | 10% 的概率随机探索服务器，90% 的概率使用当前最快的服务器。 |

#### mode

控制如何将查询发送到所选服务器的分发模式。

| 值 | 描述 |
|-------|-------------|
| `sequential` | (默认) 顺序模式。按照 `strategy` 决定的顺序逐个尝试服务器，直到有一个成功。 |
| `concurrent` | 并发模式。同时竞争所有选定的服务器；最先成功的响应获胜。 |
| `fallback` | 回退模式。首先发送到主服务器。在 `fallback_delay` 之后，它会并发地提升剩余的服务器（Happy Eyeballs 风格）。 |

#### fallback_delay

在 `fallback` 模式下提升备用服务器之前的延迟。

默认值: `300ms`。

#### max_retries

在所有模式下尝试或竞争的最大服务器数量。
- 在 `sequential`（顺序）模式下，它是按顺序尝试的最大服务器数。
- 在 `concurrent`（并发）模式下，它是同时竞争的最大服务器数。
- 在 `fallback`（回退）模式下，它是尝试的最大服务器数（1 个主服务器 + 剩余的作为回退）。

`0` 表示将尝试所有可用的服务器。

#### health_check

可选。配置主动延迟探测。
如果省略，则仅使用来自真实查询的被动 RTT 测量。

##### interval

健康检查探测之间的间隔。

默认值: `10m`。

##### timeout

每次健康检查探测的超时时间。

默认值: `5s`。

##### sample_size

用于计算指数加权移动平均值 (EWMA) 的最近 RTT 样本数。

默认值: `10`。
