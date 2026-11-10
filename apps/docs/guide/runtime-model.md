# CPAMP 与 CPA 如何协作

## v2 Beta：Embedded 与 External

v2 Beta 的完整模式不等同于下方 1.x 的单 Manager 部署。内置官方 CPA 固定为 **`v8.0.55`**；External 目标最低要求 **`v8.0.55`**，仍需通过实际资格检查。1.x 直升基线是精确的 **`v1.14.10`**，见 [升级与回滚](../operations/update.md)。

| 模式 | 谁运行 CPA | CPAMP 边界 |
| --- | --- | --- |
| Embedded / New Managed、合格 Adopt | Docker 的 `cpamp-runtime` 或 Native Runtime Supervisor 托管官方 CPA | Ingress + Manager + Runtime；Docker 仅公开 Ingress `18317`，三镜像同版 Digest 配对；Native 用 launcher 管理三进程及 CPA 子进程 |
| External | 操作者自己的 CPA 主机/容器/服务 | CPAMP 验证并保存连接、管理受支持配置与 Client Key、读取状态和观测；不启停、不重启、不升级外部 CPA 进程，不自动接管其流量入口 |
| Lightweight Panel | CPA 直接托管 `management.html` | 无 Manager、SQLite 或 Runtime；用 CPA Management Key 登录，不能提供 Manager 请求历史 |

**Embedded** 的浏览器与模型流量使用配置好的公开 origin；Ingress 决定公开路由，不能把 `8317`/`9081` 控制或内部入口另外公开。新安装必须由 Host `initialize` 提供短期 Handoff，浏览器设置 Admin Key、完成策略与资格检查后才进入正常产品。向导提供的首个 Client Key 用于模型请求，Admin Key 只用于管理。进程存活、安装完成、Gateway 就绪是不同状态。

**External** 安装先验证连接与 Management Key，配置界面的支持范围不等于获得宿主机写入/生命周期权限。规则写入无独占窗口时保持只读；可写标量按实际能力处理。使用量统计需要管理员显式确认，未确认不会自动改写外部 CPA。外部 CPA 仍由操作者负责升级、TLS/网络、配置/OAuth 凭证备份及可用性；客户端继续使用外部 CPA 的实际入口，不因 Manager 安装完成而自动改道。

外部 Management Key 轮换后，用面板重新绑定并证明当前连接；已被 CPA 拒绝的凭证不应反复重试。Manager 的 `data.key` 与加密数据库必须成套保留。Adopt 的资格拒绝或未完成迁移不是 External 自动降级许可；按向导报告处理，不以更换环境变量或手改记录代替证明。

v2 Beta 的浏览器入口是公开 origin 下的控制台 **`/console`**（例如 `http://<host>:18317/console`）。v2 不提供 CPAMP 的 `/management.html`，访问该路径会返回 404；Embedded 的 `8317` 与 `9081` 只在容器内，不要另外公开。

安装：[v2 Docker](../deployment/docker.md) / [linux/amd64 Native](../deployment/native.md)；维护：[备份](../operations/backup.md) / [许可证与发布资产](../reference/releases.md)。

## 1.x Stable 与轻量面板参考

本节描述 1.x Stable：完整模式由 Manager Server 在 `:18317/management.html` 托管面板。v2 Beta 请使用上文的 `/console` 入口，不要把本节的 `:18317/management.html` 当成 v2 入口。

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
