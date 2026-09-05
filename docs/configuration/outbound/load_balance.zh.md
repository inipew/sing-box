### 结构

```json
{
  "type": "load-balance",
  "tag": "lb-auto",
  
  "outbounds": [
    "proxy-a",
    "proxy-b",
    "proxy-c"
  ],
  "strategy": "round-robin",
  "url": "",
  "interval": "",
  "idle_timeout": "",
  "timeout": "",
  "interrupt_exist_connections": false
}
```

### 字段

#### outbounds

==必填==

用于负载均衡的出站标签列表。

#### strategy

要使用的负载均衡策略。

- `round-robin`（默认）：在健康的出站节点之间以延迟权重的轮询顺序动态分配请求。更快的节点按比例分配更多的请求。
- `consistent-hashing`：使用哈希环将发往相同目标地址的请求导向同一个代理节点。哈希环使用延迟比例缩放的虚拟节点（Virtual Nodes），因此更快的代理可以处理更大部分的哈希一致性流量。
- `sticky-sessions`：将具有相同源地址和目标地址的请求导向相同的代理节点，持续 10 分钟。如果粘性节点变得过载或性能下降，它将结合动态最小连接数（Least-Connections）进行自动重新平衡。

#### url

测试的 URL。如果为空，将使用 `https://www.gstatic.com/generate_204`。

#### interval

测试间隔。如果为空，将使用 `3m`。

#### idle_timeout

空闲超时。如果为空，将使用 `30m`。

#### timeout

测试超时。如果为空，将使用 `15s`。

#### interrupt_exist_connections

当选择的出站发生更改时，中断现有连接。

只有入站连接会受到此设置的影响，内部连接始终会被中断。
