# 请求监控没有数据

先确认你打开的是 CPAMP 完整模式：

```text
http://<cpamp-host>:18317/management.html
```

CPA `:8317/management.html` 上的轻量面板不保存请求历史，因此看不到请求监控属于正常现象。

## 按顺序检查

1. **发送一条真实请求**：确认 Codex、Claude Code 或其他客户端确实通过 CPA 请求了模型。
2. **检查 CPA 连接**：仪表盘应显示 CPA 已连接，没有认证错误。
3. **开启请求监控**：在配置中心的 Manager Server 配置中确认请求监控已启用。
4. **等待新的请求**：请求监控只能显示启用并采集之后的新事件，已经过期的数据不能补回。
5. **确认只有一个完整模式实例**：不要让多个 Manager Server 同时读取同一个 CPA 请求队列。

## 根据现象处理

| 现象                       | 优先检查                                         |
| -------------------------- | ------------------------------------------------ |
| 仪表盘显示 CPA 未连接      | CPA 地址、CPA Management Key、网络和远程管理配置 |
| CPA 已连接，但一直没有请求 | 客户端 Base URL、CPA 用量发布、请求监控开关      |
| 偶尔有数据、偶尔缺失       | Manager Server 重启、队列保留时间、重复采集实例  |
| 使用反向代理后没有数据     | 先改为 Manager Server 直连 CPA `:8317`           |
| 更新后暂时没有新数据       | 发送新请求并检查当前连接                         |

## 最常见的修复方法

1. 确认客户端 Base URL 指向 CPA，而不是 CPAMP。
2. 在 CPA 配置中启用 `usage-statistics-enabled`。
3. 在 CPAMP 配置中心启用请求监控，并保持自动采集模式。
4. 让 Manager Server 直接访问 CPA API 端口。
5. 重启后发送一条新请求，再刷新请求监控。

仍然没有数据时，保存同一时间段的 CPA 日志、Manager Server 日志和系统信息页版本信息。不要分享真实 API Key、Management Key 或认证文件。

::: details 高级诊断：状态字段和采集协议

打开经过认证的 Manager Server `/status`，重点查看：

| 字段             | 含义                                |
| ---------------- | ----------------------------------- |
| `lastConsumedAt` | 最近一次从 CPA 读取到请求事件的时间 |
| `lastInsertedAt` | 最近一次写入本地请求历史的时间      |
| `lastError`      | 最近的网络、认证或数据处理错误      |
| `totalInserted`  | 已保存的请求事件总数                |

如果 `lastConsumedAt` 为空，优先检查 CPA 用量发布、地址和采集网络。如果有读取时间但没有写入时间，检查 `lastError` 和数据目录权限。

自动采集模式会选择可用路径。RESP 模式必须直连 CPA API 端口，普通 HTTP 反向代理无法代理 RESP；HTTP 队列可以经过 HTTP 代理。

CPA 队列保留时间默认较短。Manager Server 停止时间超过保留时间后，旧事件无法补回。轮询间隔不能大于队列保留时间。

:::
