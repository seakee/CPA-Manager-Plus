---
title: AI 提供商管理
description: 在 CPA Manager Plus 中管理 Gemini、Codex、Claude、Vertex、xAI 和 OpenAI-compatible Provider，配置优先级、模型、代理、Header 与密钥测试。
---

# AI 提供商管理

AI 提供商页面决定 CPA 如何把客户端请求路由到上游模型服务。这里配置的是 CPA Provider，不是 CPAMP 管理员登录，也不是 CPA Management Key。

打开 [AI Providers Demo](https://seakee.github.io/CPA-Manager-Plus/#/demo/ai-providers) 可以查看虚构配置，不会连接真实 Provider。

## 最常用流程

1. 点击新增并选择 Provider 类型。
2. 填写 Base URL 和凭证，或选择对应的 OAuth / 认证文件。
3. 填写要让客户端使用的模型名或别名。
4. 保存后执行页面提供的模型或密钥测试。
5. 发送一条低成本真实请求，并到[请求监控](./monitoring.md)确认结果。

## 支持的配置类型

- Gemini / AI Studio
- Codex API Key
- Claude API Key
- Vertex
- xAI API Key
- OpenAI-compatible
- CPA 当前版本暴露的其他兼容配置

不同类型的字段不完全相同，但都围绕 Base URL、凭证、模型、Header、代理、优先级和启用状态。

## 新增或编辑 Provider

1. 选择 Provider 类型。
2. 填写 Base URL 和 API Key，或选择对应 OAuth/Auth File 工作流。
3. 设置清晰的名称；需要关联认证文件时保持账号标识 `auth_index` 稳定。
4. 配置模型列表、别名、排除规则或 Provider 特定选项。
5. 保存后执行低成本密钥测试或真实请求。
6. 到[请求监控](./monitoring.md)确认请求命中了预期 Provider、模型和账号。

## xAI API Key

当前面板支持管理 CPA `xai-api-key` 配置，包括：

- API Key、Base URL、代理和自定义 Header。
- 模型列表、别名、前缀和排除模型。
- Provider 优先级和启用状态。
- 通过 Provider 测试入口验证密钥和模型访问。

xAI API Key 与 xAI/Grok OAuth 认证文件不是同一种凭证。OAuth 登录、billing 证据和账号巡检请分别查看 [OAuth 登录](./oauth.md)、[配额管理](./quota.md) 和[账号巡检](./codex-inspection.md)。

## 优先级与并发保存

Provider 表格支持直接调整优先级。数值只影响 CPA 当前支持的路由排序，不代表健康状态或成本优先级。

保存时 CPAMP 会尽量复用精确的单项更新接口并刷新配置缓存。若配置在其他浏览器或进程中同时修改，保存前重新加载页面，避免用旧快照覆盖新配置。

## 模型与密钥测试

- 模型获取会使用当前 Provider 的 Base URL、API Key、Header 和代理配置。
- OpenAI-compatible、Codex、Claude 和 xAI 等配置可以根据页面能力执行密钥或模型测试。
- 测试成功只证明当前测试请求成功，不代表所有模型、地区、账号状态和长期配额都可用。
- 测试失败时完整错误只应在已认证的本地面板中查看，不要把 API Key 或原始凭证贴到 Issue。

## 保存后验证

1. 发一条低成本真实请求。
2. 在请求监控确认 Provider、模型、账号和状态码。
3. 如果成本为空，检查[模型价格](./model-prices.md)。
4. 如果像认证或额度问题，检查[认证文件](./auth-files.md)、[配额管理](./quota.md)和[账号处理队列](./account-actions.md)。
5. 如果页面没有请求事件，先排查采集链路，而不是反复修改 Provider。

## 配置边界

- Provider 配置决定 CPA 如何请求上游，不决定 CPAMP 的登录方式。
- 模型别名、Provider 模型名和价格表名称可能不同，需要分别维护。
- 不要把 CPA Management Key、CPAMP Admin Key 和普通模型 API Key 混用。
- Provider 能力取决于当前 CPA 版本；旧版本可能忽略或拒绝新字段。
