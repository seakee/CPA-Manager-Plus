# 客户端接入

客户端只需要知道 CPA 的地址和普通 API 密钥。不要把 Codex、Claude Code、OpenCode 或 OpenAI SDK 指向 CPAMP；CPAMP 负责观测和管理，不承接普通模型请求。

## 通用规则

| 项目     | 建议                                                                          |
| -------- | ----------------------------------------------------------------------------- |
| Base URL | CPA 对外地址，例如 `https://gateway.example.com` 或 `http://localhost:8317`。 |
| API 密钥 | 使用 CPA 的普通 API 密钥，不要使用 CPAMP 管理员密钥或 CPA Management Key。    |
| 模型     | 使用 CPA 提供商暴露的模型名或别名。                                           |
| 监控     | 请求通过 CPA 后，CPAMP 才能采集用量队列并展示在请求监控 / 用量分析中。        |

同域名反向代理时，`/v1/*`、`/v1beta/*`、`/backend-api/codex/*` 这些客户端请求应转发到 CPA。普通模型请求转给 CPAMP 只会增加 401、404 或 428 的排障成本。

## Codex

Codex 接入前先准备好：

- Base URL 指向 CPA 的 Codex 兼容入口。
- API 密钥使用 CPA 普通 API 密钥。
- Codex 提供商或认证文件已在 CPA 中配置。
- 如果要做账号巡检，认证文件中需要稳定的 `auth_index` 和可识别账号信息。

Codex 请求失败时，先看 [请求监控](../manual/monitoring.md) 的失败摘要；如果像账号或配额问题，再看 [Codex 账号巡检](../manual/codex-inspection.md)。

## Claude Code

Claude Code 接入前检查：

- CPA 中已有 Claude Code 提供商或兼容提供商。
- OAuth 或认证文件已完成。
- 客户端使用 CPA 对外地址和普通 API 密钥。

OAuth 成功只能说明认证流程完成，不代表账号一定能服务请求。请求失败时，先在凭证管理中检查账号状态，再看请求监控的失败摘要。

## OpenCode 与通用 OpenAI 客户端

OpenCode、OpenAI SDK 和多数中转客户端使用 OpenAI 兼容的 `/v1/...`：

```text
Base URL: https://gateway.example.com/v1
API 密钥: CPA 普通 API 密钥
模型:     CPA 暴露的模型名或别名
```

经过反向代理时，`/v1/models` 返回 401 通常是 CPA API 密钥问题；`/v0/management/config` 返回 401 才是 CPAMP 管理登录问题。

## Factory Droid / 其他工具

其他工具只要能填写 OpenAI 兼容或 Gemini 兼容端点，就按 CPA 暴露的接口配置。关键是让请求经过 CPA；只有这样，CPAMP 才能看到请求事件、成本和账号健康信息。
