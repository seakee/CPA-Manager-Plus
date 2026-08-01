---
title: Codex、Claude 与 xAI 配额管理
description: 查看 Codex、Claude、xAI/Grok 等账号的配额、reset、Provider 证据和 CPAMP 安全冷却状态。
---

# Codex、Claude 与 xAI 配额管理

配额管理页面回答的是“这个账号现在还能不能继续跑请求”。它不只看剩余额度，也会结合认证文件、巡检结果、请求失败摘要和冷却记录判断账号是否应该暂停或恢复。

它面向账号状态，不负责展示成本；成本拆解请看 [用量分析](./usage-analytics.md)。

打开[配额演示](https://seakee.github.io/CPA-Manager-Plus/#/demo/quota)可以查看虚构的 Codex、Claude 和 xAI 窗口数据。

## 进入页面前

配额页依赖认证文件和提供商返回的数据。使用前建议先确认：

1. [认证文件](./auth-files.md) 中账号存在且 `auth_index` 稳定。
2. 请求确实经过 CPA，并且最近有相关请求。
3. 如果是 Codex 账号，必要时先跑一次 [Codex 账号巡检](./codex-inspection.md)。

## 数据来源

页面里的配额线索可能来自：

- CPA 提供商或认证文件中的账号信息。
- Codex 账号巡检结果。
- 请求失败摘要中的 `usage_limit_reached` 等信号。
- 最近响应 Header 中记录的额度和恢复时间。
- CPAMP 的配额冷却记录。

不同提供商能返回的信息不一样。未知状态只代表 CPAMP 没拿到足够信息，不代表账号一定无限可用。

## Provider 能力概览

| Provider       | 可能显示的证据                                          | 边界                                                           |
| -------------- | ------------------------------------------------------- | -------------------------------------------------------------- |
| Codex          | 5 小时/周窗口、reset、Header 观察、workspace 和巡检状态 | 字段取决于账号计划和接口返回。                                 |
| Claude         | 基础额度、周额度、模型级 scoped limits                  | scoped limits 可能重复、缺失或停用，CPAMP 按身份和新鲜度归并。 |
| xAI/Grok OAuth | CLI billing 周/月数据、官方 API 身份、请求事件耗尽信号  | 付费 API 身份不等于可查询费用或剩余百分比。                    |
| 其他 Provider  | CPA quota、认证文件元数据或最近响应 Header              | 不假设存在统一主动额度接口。                                   |

### xAI 付费 OAuth

xAI 的免费 Grok Build OAuth 可以通过 CLI billing 接口返回周额度和月度账单数据。面向官方 `api.x.ai` 的 OAuth 凭证可能无法访问这些接口，并返回 `403 Access denied`，同时也没有可供 CPAMP 查询的公开付费额度接口。

当两个 CLI billing 请求都只返回通用的 `403 Access denied`，且没有更明确的订阅、权限或额度信号时，CPAMP 会使用只读的 `GET https://api.x.ai/v1/me` 检查官方 API 身份。成功时页面显示“官方 API”健康状态，但不会伪造额度、费用或剩余百分比，也不会发送模型请求。该状态只证明 OAuth 身份可访问，不代表聊天路由或模型权限已经验证。

付费 xAI OAuth 通过 CPA 调用官方 API 时，认证 JSON 通常需要设置 `using_api: true`，并使用 `base_url: https://api.x.ai/v1`。否则 OAuth 默认可能继续路由到 Grok CLI chat proxy。真实费用和剩余额度仍需在 xAI 控制台查看。

### xAI 请求监控证据

当 xAI 请求以 HTTP `402` 或 `429` 返回 `subscription:free-usage-exhausted` 时，请求监控会把错误正文中的模型、`actual/limit`、剩余量和超额量整理为结构化证据。对于“滚动 24 小时”但没有明确 reset 的响应，恢复时间按事件时间加 24 小时估算，并明确标记为“预计恢复”；这是冷却调度用的上界估算，不是精确重建滚动窗口。如果错误正文中有明确的 `billing_period_end` / `reset_at` 等字段，则优先使用该时间。传输层 `Retry-After` 只表示请求重试退避，不会覆盖 free-usage 冷却恢复时间。

成功响应中的 `X-Ratelimit-Limit-*` 和 `X-Ratelimit-Remaining-*` 只表示当前 API 请求或 Token 限流窗口，用于诊断吞吐限制，不等同于 Grok 免费计划的包含额度。CPAMP 不会用这些 Header 推算免费额度余额。

请求监控还会显示可安全保留的 `X-Request-Id`、`traceparent`、`X-Should-Retry`、`X-Data-Retention` 和 `X-Zero-Retention` 信号。`Set-Cookie`、API Key 和其他可能包含 Token 的 Header 不会进入公开监控数据。相同的脱敏耗尽证据会附在 CPAMP 创建的冷却记录中，供认证文件页面排障。

## 页面操作

- 使用搜索框按文件名、账号、备注或索引快速定位账号。
- 使用排序查看计划、额度状态或名称顺序。
- 点击刷新认证文件和额度，重新读取账号列表和可查询额度。
- 如果账号卡片提示冷却、失效或需要重新授权，按提示跳转到认证文件、OAuth 或巡检页面。
- 对同一批账号做判断时，优先按 `auth_index` 和备注区分，不要只看文件名。

## 配额冷却

开启配额冷却后，受支持账号出现精确额度耗尽信号时，CPAMP 可以临时禁用对应认证文件，并在恢复时间后重新启用。目前支持带明确重置时间的 Codex `usage_limit_reached`，以及按官方滚动 24 小时窗口恢复的 xAI `subscription:free-usage-exhausted`。

冷却记录会标记原因代码和窗口类型，当前可区分 `five_hour`、`weekly`、`monthly`、`rolling_24h` 和 `unknown`。例如 Codex 5 小时限额已满但周限额未满时，只按 5 小时窗口冷却；恢复后重新参与 CPA 的新请求调度，不需要等待周窗口。

注意：

- 需要 `USAGE_QUOTA_COOLDOWN_ENABLED` 或配置中心开关启用。
- 自动恢复依赖 CPAMP 持续运行。
- CPAMP 创建冷却后，对应凭证会在 CPA 中处于禁用状态，不再参与新请求调度。
- 只有该冷却记录自己禁用的账号会被自动恢复；手动禁用、巡检禁用或认证故障禁用不会被配额冷却覆盖。
- 如果 `auth_index` 不稳定，冷却记录可能无法准确绑定账号。

配额冷却适合处理明确的额度耗尽，不适合用来处理登录失效、上游封禁或配置错误。后几类问题应进入 [账号处理队列](./account-actions.md) 或 [OAuth 登录](./oauth.md) 处理。

## 排障

账号看起来可用但请求失败时：

1. 查看请求监控中的失败摘要。
2. 查看账号巡检是否提示 Codex/xAI 配额、工作区、billing 或认证问题。
3. 查看认证文件页面是否处于手动禁用或冷却状态。
4. 查看账号处理队列是否有待处理候选项。
5. 如果页面没有配额数据，确认该提供商是否支持主动查询，或是否只能通过请求 Header 被动观察。
