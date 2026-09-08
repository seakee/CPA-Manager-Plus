---
title: 账号巡检
description: 在浏览器本地或通过 Manager Server 巡检 `codex` 和 `xai`；Manager Server 还提供只读的 `claude` OAuth 用量检查，不发送模型或推理请求，也不修改凭证。
---

# 账号巡检

账号巡检用于判断账号为什么不能稳定服务请求。当前页面路由和部分 UI 仍保留 `Codex Inspection` 名称。浏览器本地巡检目标仅为 `codex` 和 `xai`；Manager Server 还提供只读 `claude` OAuth 用量检查。

打开[账号巡检演示](https://seakee.github.io/CPA-Manager-Plus/#/demo/codex-inspection)可以查看虚构结果，不会向 Provider 发送请求。

如果只是排查某一条请求，先看[请求监控](./monitoring.md)；确认问题集中在账号、凭证或额度后再进入巡检。

## 本地与服务端巡检

- **本机巡检**：由当前浏览器会话执行，仅支持 `codex` 和 `xai`。
- **服务端巡检**：由 Manager Server 执行，`codex` 和 `xai` 支持定时任务、历史、日志和统一动作策略。
- **Claude OAuth 用量检查**：仅在 Manager Server 中提供，只读执行；它不会发送模型或推理请求，也绝不会自动禁用、启用、删除、重新认证或以其他方式修改凭证。

服务端巡检前确认 CPA URL、CPA Management Key、Auth File 和稳定 `auth_index` 均可用。

## 不可用的巡检目标

`qwen`、`qoder` 和 `iflow` 暂无远程巡检或主动配额刷新，因为 CPAMP 没有经过验证的安全 Provider 合约。它们现有的 CPA 暴露 OAuth/Auth File 工作流不受影响；Qoder 设备码 OAuth 在 CPA 支持时仍可使用。它们不是浏览器本地或 Manager Server 可选择、可执行的巡检目标。

## Codex 检查内容

- 账号计划、5 小时/周额度窗口、reset 和剩余额度。
- OAuth Token 与认证状态。
- Workspace 是否停用或不可用。
- `usage_limit_reached` 等明确额度证据。
- 是否建议保留、重新授权、禁用、启用或删除。

缺失字段保持未知，不会被当作健康或异常。

## xAI 检查内容

xAI 巡检优先使用不发送模型推理请求的只读证据：

- Grok Build / CLI OAuth 可查询时读取周额度、月度账单和账号状态。
- 免费额度耗尽事件可进入受控的滚动 24 小时冷却。
- 付费 `api.x.ai` OAuth 无法访问 CLI billing 时，可使用只读身份接口确认官方 API 身份。
- 身份检查成功只代表凭证可访问身份接口，不代表具体模型、聊天路由、费用或剩余额度已经验证。
- 不明确的 `403`、地区限制或模型权限不会被统一解释为凭证失效。

## 结果和动作

- **保留**：没有足够证据要求处理。
- **重新授权**：OAuth 或认证状态明确失效。
- **人工复核**：证据不足或可能涉及权限、地区和模型范围。
- **禁用**：账号当前不适合参与新请求，并且策略允许。
- **启用**：由同一巡检自动化禁用且已明确恢复。
- **删除**：只有账号明确失效、文件不再共享且用户确认时执行。

不要只看动作名称，要同时查看 Provider、原因代码、脱敏证据和最近请求表现。

自动巡检动作仅适用于符合条件的 `codex` 和 `xai` 巡检结果。`claude` OAuth 用量检查始终只读，绝不执行自动动作。

## 定时巡检与自动化边界

服务端可按间隔或每天指定时间运行。建议先使用仅记录或保守动作模式，观察结果后再启用自动禁用。

所有自动恢复遵循“谁禁用、谁恢复”：

- 巡检只恢复由巡检自己禁用的凭证。
- 配额冷却只恢复对应冷却记录禁用的凭证。
- 手动禁用和账号处理队列禁用不会被巡检越权恢复。

## 与其他页面的关系

- 配额窗口和冷却：[凭证管理](./accounts.md)
- OAuth 重新授权：[OAuth 登录](./oauth.md)
- 凭证状态：[凭证管理](./accounts.md)
- 反复认证失败：[账号处理队列](./account-actions.md)
- 单条失败证据：[请求监控](./monitoring.md)
