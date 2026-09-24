---
icon: material/server-network
---

# DNS 服务器组

`group` 是按策略选择并重试成员 DNS 服务器的虚拟服务器。响应只有通过协议校验和 DNS 规则响应校验后才能胜出。

### 结构

```json
{
  "type": "group",
  "tag": "reliable-dns",
  "servers": ["cloudflare", "quad9", "google"],
  "detour": "proxy",
  "policy": "reliable",
  "advanced": {
    "selection": "adaptive",
    "execution": "hedge",
    "max_attempts": 3,
    "max_inflight": 2,
    "hedge_delay": "300ms",
    "retry_rcodes": ["SERVFAIL", "REFUSED"],
    "health": {
      "window_size": 20,
      "failure_threshold": 3,
      "cooldown": "30s",
      "max_cooldown": "5m",
      "active_probe": { "enabled": false, "interval": "10m", "timeout": "5s", "name": ".", "type": "NS" }
    }
  }
}
```

### 字段

#### servers

必填。成员标签必须唯一。服务器组不能包含自身、其他服务器组或 `fakeip` 服务器。

#### detour

可选。为所有需要网络连接的成员应用指定出站。若网络成员不能使用该拨号器克隆，启动将失败。明确无网络的传输（例如 `hosts`）不受影响。

#### policy

| 值 | 行为 |
|----|------|
| `reliable` | 默认。自适应选择；主请求 300ms 后仍未完成时追加一个受限并发请求。 |
| `low_latency` | 并发查询健康候选，返回第一个可接受响应。 |
| `privacy` | 每次仅查询一个候选，只在失败后尝试下一个。 |

#### advanced

用于覆盖预设的可选配置：

- `selection`：`adaptive`、`ordered`、`random` 或 `round_robin`。
- `execution`：`failover`、`hedge` 或 `parallel`。
- `max_attempts`：最多尝试的成员数，不能超过成员总数。
- `max_inflight`：最大并发尝试数；`failover` 时必须为 `1`。
- `hedge_delay`：`hedge` 模式的正延迟。
- `retry_rcodes`：替换可重试的 DNS RCODE 集合；默认为 `SERVFAIL` 和 `REFUSED`。
- `health.window_size`：滚动结果窗口，默认 `20`。
- `health.failure_threshold`：打开熔断器前的连续失败次数，默认 `3`。
- `health.cooldown`：初始熔断时间，默认 `30s`。
- `health.max_cooldown`：指数退避上限，默认 `5m`。
- `health.active_probe`：可选后台探测。默认关闭；默认每 `10m` 查询一次 `NS .`，超时 `5s`。

`NOERROR` 和 `NXDOMAIN` 是协议有效的终止响应。默认重试 `SERVFAIL`、`REFUSED`、畸形响应、传输错误及被 DNS 规则拒绝的响应。因其他成员胜出而取消的请求不计为失败。

### 运行状态

启用 Clash API 后，`GET /dns/groups` 返回全部服务器组快照，`GET /dns/groups/{tag}` 返回指定组。快照包含健康状态和计数器，但不会暴露查询名称。

### 迁移

旧字段 `strategy`、`mode`、`fallback_delay`、`max_retries` 和 `health_check` 已移除，并会产生配置错误。请改用 `policy` 和 `advanced`。
