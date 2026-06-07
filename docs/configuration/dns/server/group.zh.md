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

必填。成员 DNS 服务器的标签列表。

#### strategy

用于为每个查询选择服务器的负载均衡策略。

| 值 | 描述 |
|-------|-------------|
| `wp2` | (默认) 加权 2 次方 (Weighted Power-of-Two)。随机选择两台服务器，并使用 EWMA RTT 较低的一台。 |
| `first` | 始终选择 EWMA RTT 最低的一台服务器。 |
| `random` | 忽略 RTT，均匀随机地选择一台服务器。 |
| `round_robin` | 轮询，按顺序循环使用所有服务器。 |
| `p2` | 从 EWMA RTT 排序的前 2 名中随机选择一台服务器。 |
| `ph` | 从 EWMA RTT 排序的前一半中随机选择一台服务器。 |
| `p<N>` | 从 EWMA RTT 排序的前 `N` 名中随机选择一台服务器 (例如 `p3`)。 |

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

在 `sequential` 模式下返回错误之前尝试的最大服务器数量。
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
