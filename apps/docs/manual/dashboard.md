---
title: CPA 管理与可观测性仪表盘
description: 使用 CPA Manager Plus 仪表盘查看 CPA/Manager Server 连接、请求量、成功率、成本、Token、采集器、Provider 和健康提醒。
---

# CPA 管理与可观测性仪表盘

仪表盘适合每天打开面板后先看一眼。它不替代请求明细，而是先回答几个问题：系统连上了吗、今天有没有请求、失败率是否异常、成本有没有突然升高、有没有账号或运行时告警。

如果你已经知道要处理哪个账号或哪条请求，可以直接进入 [请求监控](./monitoring.md)、[认证文件](./auth-files.md) 或 [Codex 账号巡检](./codex-inspection.md)。

打开[仪表盘演示](https://seakee.github.io/CPA-Manager-Plus/#/demo)可以查看随当前日期生成的虚构请求、成本、失败和 Provider 状态。

## 先看哪些数字

- **连接与版本**：确认面板已经连接到 CPA 或 Manager Server，版本信息和运行模式符合当前部署。
- **快捷统计**：API Key、认证文件、AI 提供商和模型数量，用来判断配置是否被正确读取。
- **请求与成本**：今日请求数、成功率、平均延迟、Token 和预估成本。
- **采集器状态**：确认请求监控采集器是否运行、队列是否可读、最近采集是否正常。
- **健康提醒**：错误日志、连接异常、监控不可用或配置缺失会优先出现在这里。

仪表盘的数据来自 Manager Server 本地 SQLite 和 CPA 用量队列。这里的成本是 CPAMP 根据请求事件和模型价格算出的估算值，不等同于提供商最终账单。

## 常用操作

1. 打开仪表盘后先看连接状态和采集器状态。
2. 如果请求数为 0，先确认客户端请求是否经过 CPA，再进入 [请求监控排障](../troubleshooting/request-monitoring.md)。
3. 如果成功率下降，进入 [请求监控](./monitoring.md)，按状态码、模型、账号或 API Key 过滤。
4. 如果成本上升，进入 [用量分析](./usage-analytics.md)，按模型、账号和调用方拆分。
5. 如果健康提醒指向日志或系统状态，进入 [日志查看](./logs.md) 和 [系统信息](./system.md) 保留证据。

## 页面为空时先查什么

仪表盘长时间为空时，不要先改筛选条件。先确认：

1. 客户端请求确实经过 CPA。
2. CPA 用量发布已开启。
3. CPAMP 采集器正在运行。
4. CPA URL 和 CPA Management Key 正确。
5. 用量队列保留时间没有短到事件来不及采集。

更完整的检查顺序见 [请求监控排障](../troubleshooting/request-monitoring.md)。

## 使用建议

- 成功率突然下降：进入 [请求监控](./monitoring.md)，按状态码、模型和账号过滤。
- 成本突然升高：进入 [用量分析](./usage-analytics.md)，看模型和账号拆解。
- 某个账号异常：进入 [认证文件](./auth-files.md)、[配额管理](./quota.md) 或 [Codex 账号巡检](./codex-inspection.md)。
- 登录或授权异常：进入 [OAuth 登录](./oauth.md) 重新授权，再回到认证文件确认状态。
- 系统状态异常：进入 [日志查看](./logs.md) 和 [系统信息](./system.md)，保留版本和日志线索。
