# 提供商与兼容接口参考

如果你的目标是新增、编辑或测试模型服务，直接使用 [AI 提供商](../manual/ai-providers.md)。本页用于确认不同 Provider 和客户端接口的大致对应关系。

## 常见 Provider

| Provider 类型     | 常见用途                 | 相关页面                      |
| ----------------- | ------------------------ | ----------------------------- |
| Codex             | Codex CLI、账号和配额    | AI 提供商、认证文件、账号巡检 |
| Claude            | Claude Code 和兼容请求   | AI 提供商、认证文件、请求监控 |
| OpenAI-compatible | 中转、自建或兼容模型服务 | AI 提供商、模型价格、用量分析 |
| Gemini / Vertex   | Google 模型与项目凭证    | AI 提供商、OAuth、认证文件    |
| xAI / Grok        | API Key 或 OAuth 账号    | AI 提供商、配额、账号巡检     |

添加 Provider 时优先确认四件事：Base URL、认证方式、客户端使用的模型名、是否绑定对应账号或认证文件。

## 出现请求失败时

1. 在 AI 提供商页面执行可用的模型或密钥测试。
2. 检查认证文件是否被禁用或达到配额。
3. 发送一条低成本真实请求。
4. 在请求监控中查看状态码和脱敏失败摘要。
5. 最后再查看日志。

::: details 高级：兼容接口和反向代理

常见客户端接口包括：

- `/v1/...`：OpenAI-compatible 客户端。
- `/v1beta/...`：Gemini-compatible 客户端。
- `/backend-api/codex/...`：Codex CLI。
- Provider 回调路径：OAuth 登录。

模型请求应转发到 CPA，不应转发到 CPAMP。需要同域名分流时查看[反向代理](../deployment/reverse-proxy.md)。

模型价格只影响 CPAMP 的本地成本估算，不会改变 CPA 路由或 Provider 账单。

:::
