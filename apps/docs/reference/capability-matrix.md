---
title: CPA Manager Plus 能力矩阵
description: 对比 CPAMP 轻量面板和完整模式支持的管理、监控、成本、配额、巡检与插件能力，并选择 Docker 或原生包安装完整模式。
---

# CPA Manager Plus 能力矩阵

本页用于确认不同使用方式支持哪些功能，以及不同 Provider 能提供哪些状态证据。未知或缺失字段会按未知处理，不会被当作健康、无限额度或成功。

## 按使用方式

| 能力                                         | CPAMP 轻量面板        | CPAMP 完整模式                                        |
| -------------------------------------------- | --------------------- | ----------------------------------------------------- |
| CPA 配置、Provider、凭证管理、OAuth、日志    | ✅                    | ✅                                                    |
| 插件管理和插件页面                           | 取决于 CPA 与插件路径 | ✅                                                    |
| 浏览器本地账号巡检（仅 `codex` / `xai`）     | ✅                    | ✅                                                    |
| SQLite 请求历史                              | ❌                    | ✅                                                    |
| 请求监控与失败诊断                           | ❌                    | ✅                                                    |
| 用量与成本分析                               | ❌                    | ✅                                                    |
| 模型价格和 API Key 别名                      | ❌                    | ✅                                                    |
| 服务端账号巡检和历史                         | ❌                    | ✅（`codex`/`xai`，以及只读 `claude` OAuth 用量检查） |
| 配额冷却与账号处理队列                       | ❌                    | ✅                                                    |
| 登录凭证                                     | CPA Management Key    | CPAMP Admin Key                                       |
| 备份、迁移状态、pprof                        | ❌                    | ✅                                                    |

CPAMP 轻量面板是 CPA 直接托管的增强 WebUI，与官方面板一样不需要额外服务。它不会连接或读取 Manager Server；需要表中的服务端能力时，必须改用 Manager Server 的 `:18317/management.html` 入口。

> **在线演示不是运行模式。** 它只使用虚构数据预览界面，不能连接、管理或监控真实 CPA，也不代表矩阵中的功能已经实际运行。

## 完整模式如何安装

Docker 和原生包提供相同的完整模式能力，只是安装方式不同。

| 安装方式            | 适合谁                   | 面板入口                                    |
| ------------------- | ------------------------ | ------------------------------------------- |
| Docker 部署（推荐） | 大多数新用户和服务器部署 | `http://<cpamp-host>:18317/management.html` |
| 原生包部署          | 不使用 Docker 的主机     | `http://<cpamp-host>:18317/management.html` |

- Docker 用户查看 [Docker 部署](../deployment/docker.md)。
- Linux、macOS 或 Windows 用户查看 [原生包部署](../deployment/native.md)。

## 按账号与 Provider

| Provider / 账号类型                  | 配置管理                                                                     | 配额与健康证据                                           | 主动巡检                                                              |
| ------------------------------------ | ---------------------------------------------------------------------------- | -------------------------------------------------------- | --------------------------------------------------------------------- |
| Codex OAuth/Auth File                | Provider、Auth File、OAuth、模型别名                                         | 5 小时/周窗口、reset、workspace、凭证状态                | 本地与服务端；符合条件的结果可执行受控自动化动作                      |
| Claude                               | Provider、OAuth/Auth File                                                    | 基础额度、周额度、模型级 scoped limits（取决于返回字段） | 仅 Manager Server，只读 OAuth 用量检查；不执行自动动作                |
| xAI/Grok OAuth                       | Provider/Auth File/OAuth                                                     | CLI billing、付费 OAuth identity fallback、请求事件证据  | 本地与服务端；符合条件的结果可执行受控自动化动作                      |
| xAI API Key                          | `xai-api-key` 配置、优先级、模型和密钥测试                                   | 请求结果与 Provider 返回信息                             | Provider key test                                                     |
| Qwen / Qoder / iFlow                 | 保持现有 CPA 暴露的 OAuth/Auth File 工作流；Qoder 设备码 OAuth 仍取决于 CPA 支持 | 认证文件状态和最近请求证据                               | 远程巡检与主动配额刷新不可用：没有经过验证的安全 Provider 合约        |
| Gemini / Vertex / Antigravity / Kimi | Provider、Auth File 或 OAuth（按 CPA 能力）                                  | Provider 特定 quota 或最近请求证据                       | 取决于 CPA 与 Provider 接口                                           |
| OpenAI-compatible                    | Base URL、API Key、Header、模型映射和密钥测试                                | 请求状态、延迟、失败摘要和成本                           | 不假设存在统一 quota API                                              |

## 数据与自动化边界

- 浏览器本地巡检仅支持 `codex` 和 `xai`。
- Manager Server 还提供只读 `claude` OAuth 用量检查；它不会发送模型或推理请求，也绝不会自动禁用、启用、删除、重新认证或以其他方式修改凭证。
- 自动巡检动作仅适用于符合条件的 `codex` 和 `xai` 结果，绝不适用于 `claude`。
- CPAMP 只使用 Provider 实际返回、CPA 提供或请求事件中可安全提取的证据。
- `401/403` 本身不足以自动禁用账号，必须有明确凭证语义。
- 配额冷却、巡检和账号处理队列遵循“谁禁用、谁恢复”。
- 手动禁用不会被其他自动化越权恢复。
- 成本是根据已采集请求和模型价格估算的，Provider 账单仍是最终依据。
- CPA queue 过期或 Manager Server 停机期间未采集的请求无法补回。

## 相关文档

- [账号巡检](../manual/codex-inspection.md)
- [凭证管理与账号状态](../manual/accounts.md)
- [账号处理队列](../manual/account-actions.md)
- [Manager Server 指南](../operations/manager-server.md)
