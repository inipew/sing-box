### 结构

```json
{
  "type": "fallback",
  "tag": "fallback-auto",
  
  "outbounds": [
    "proxy-a",
    "proxy-b",
    "proxy-c"
  ],
  "url": "",
  "interval": "",
  "idle_timeout": "",
  "timeout": "",
  "fallback_delay": "",
  "interrupt_exist_connections": false
}
```

### 字段

#### outbounds

==必填==

要测试的出站标签列表。

#### url

测试的 URL。如果为空，将使用 `https://www.gstatic.com/generate_204`。

#### interval

测试间隔。如果为空，将使用 `3m`。

#### idle_timeout

空闲超时。如果为空，将使用 `30m`。

#### timeout

测试超时。如果为空，将使用 `15s`。

#### fallback_delay

并发拨号延迟。如果非零（例如 `300ms`），当主出站连接缓慢时，将在该延迟后并发拨号备用出站（Happy Eyeballs 机制）。如果为 `0` 或为空，则改用顺序拨号重试。

#### interrupt_exist_connections

当选择的出站发生更改时，中断现有连接。

只有入站连接会受到此设置的影响，内部连接始终会被中断。
