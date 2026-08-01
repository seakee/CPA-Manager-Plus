# CPAMP 与 CPA 如何协作

CPA 负责接收和转发真实模型请求，CPAMP 负责管理 CPA，并在完整模式下保存请求历史、分析成本和处理账号状态。

普通使用者只需要记住三件事：

1. Codex、Claude Code、OpenCode 等客户端始终连接 CPA，不连接 CPAMP。
2. CPAMP 轻量面板从 CPA 的 `:8317/management.html` 打开，使用 CPA Management Key。
3. CPAMP 完整模式从 `:18317/management.html` 打开，使用 CPAMP 管理员密钥。

## 请求应该发到哪里

| 操作                   | 地址                            | 使用的密钥         |
| ---------------------- | ------------------------------- | ------------------ |
| 客户端请求模型         | CPA 的 `/v1/...` 等模型接口     | CPA 普通 API 密钥  |
| 使用 CPAMP 轻量面板    | CPA `:8317/management.html`     | CPA Management Key |
| 使用 CPAMP 完整模式    | CPAMP `:18317/management.html`  | CPAMP 管理员密钥   |
| CPAMP 完整模式连接 CPA | setup 或配置中心填写的 CPA 地址 | CPA Management Key |

不要把这三类密钥混用。登录失败时，先确认当前打开的是 `8317` 还是 `18317`。

## 两种模式的区别

- **轻量面板**：只替换 CPA 官方管理界面，不增加服务或数据库。
- **完整模式**：增加 Manager Server，用于请求历史、成本分析、服务端巡检、备份和自动化。

不知道应该使用哪一种时，查看[如何选择面板](./choosing-a-panel.md)。

::: details 请求和数据的完整路径

```text
Codex / Claude Code / 其他客户端
  -> CPA
      -> 模型提供商
      -> 请求日志与用量队列

CPAMP 完整模式
  -> 从 CPA 读取管理信息和用量事件
  -> 保存请求历史、价格、巡检和自动化状态
```

因此：

- 客户端请求失败时，先检查 CPA、Provider、账号和客户端配置。
- CPAMP 页面没有数据时，先确认请求经过 CPA，再检查请求监控采集。
- CPAMP 不会独立转发模型请求。

:::

## 什么时候需要修改 CPA 配置

以下能力仍由 CPA 配置控制：

- Provider、模型路由、认证文件和 OAuth。
- 客户端 API 密钥、配额、日志和插件。
- 远程管理、用量发布和队列保留时间。

日常操作优先使用 CPAMP 页面。只有页面没有对应字段、部署前准备 CPA 或高级排障时，才需要直接编辑 CPA `config.yaml`。

## 下一步

- 安装：[快速开始](./getting-started.md)
- 添加模型服务：[AI 提供商](../manual/ai-providers.md)
- 配置客户端：[客户端接入](../gateway/clients.md)
- 监控没有数据：[请求监控为空](../troubleshooting/request-monitoring.md)
