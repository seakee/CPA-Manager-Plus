# 快速开始

先根据需求选择一条路线。两种模式都使用 CPAMP 界面，但安装方式和可用能力不同。

| 你的情况                                 | 推荐路线                                  |
| ---------------------------------------- | ----------------------------------------- |
| 已有 CPA，只想换一个更清晰的管理界面     | [CPAMP 轻量面板](#路线一安装轻量面板)     |
| 需要请求历史、成本分析、账号巡检或自动化 | [CPAMP 完整模式](#路线二安装完整模式)     |
| 还不确定                                 | 先看[如何选择面板](./choosing-a-panel.md) |

## 路线一：安装轻量面板

适合已经运行 CPA、只想替换官方 Management Center 的用户。不需要额外服务、数据库或端口。

1. 在 CPA 配置中将 `panel-github-repository` 指向 `seakee/CPA-Manager-Plus`。
2. 重启或重新加载 CPA。
3. 打开：

```text
http://<cpa-host>:8317/management.html
```

使用 CPA Management Key 登录。完整配置示例和更新方法见 [安装轻量面板](../deployment/cpa-panel.md)。

## 路线二：安装完整模式

完整模式会运行 Manager Server，提供请求历史、成本分析、服务端巡检和自动化。大多数用户建议使用安装向导：

```bash
curl -fsSLO https://raw.githubusercontent.com/seakee/CPA-Manager-Plus/main/bin/install-cpamp.sh
bash install-cpamp.sh
```

在向导中选择：

1. 安装范围：没有 CPA 时选择“CPA + CPAMP”；已有 CPA 时选择“仅 CPAMP”。
2. 部署方式：优先选择 Docker；不使用 Docker 时选择原生包。
3. 确认安装摘要后开始部署。

安装完成后打开：

```text
http://<host>:18317/management.html
```

使用安装器保存或输出的 CPAMP 管理员密钥登录。详细说明见 [安装完整模式](../deployment/installer.md)。

## 首次连接 CPA

如果安装器没有预先保存 CPA 连接，首次打开完整模式时按页面提示填写：

1. CPAMP 管理员密钥。
2. CPA 地址，例如 `http://cli-proxy-api:8317`。
3. CPA Management Key。
4. 请求监控保持默认自动模式。

## 如何确认安装成功

### 轻量面板

- 可以用 CPA Management Key 登录。
- Provider、认证文件、OAuth、Quota、日志和插件等 CPA 管理功能可以正常读取。
- 页面地址仍然是 CPA 的 `:8317/management.html`。

### 完整模式

- 可以用 CPAMP 管理员密钥登录 `:18317/management.html`。
- 仪表盘显示 CPA 已连接。
- 发送一条真实请求后，请求监控出现记录。
- 用量分析可以看到对应的 Token 和成本估算。

如果完整模式能打开但没有请求数据，查看 [请求监控为空](../troubleshooting/request-monitoring.md)。

## 接下来做什么

- 添加模型服务：[AI 提供商](../manual/ai-providers.md)
- 登录或导入账号：[OAuth 登录](../manual/oauth.md)和[认证文件](../manual/auth-files.md)
- 配置客户端：[客户端接入](../gateway/clients.md)
- 更新和备份：[更新 CPAMP](../operations/update.md)和[备份与恢复](../operations/backup.md)
- 手动维护部署：[Docker 部署](../deployment/docker.md)或[原生包部署](../deployment/native.md)
