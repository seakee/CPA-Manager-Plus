<div align="center">

# CPA Manager Plus

[![Release](https://img.shields.io/github/v/release/seakee/CPA-Manager-Plus?style=flat-square)](https://github.com/seakee/CPA-Manager-Plus/releases/latest)
[![License](https://img.shields.io/github/license/seakee/CPA-Manager-Plus?style=flat-square&color=blue)](https://github.com/seakee/CPA-Manager-Plus/blob/main/LICENSE)
[![Docker Pulls](https://img.shields.io/docker/pulls/seakee/cpa-manager-plus?style=flat-square)](https://hub.docker.com/r/seakee/cpa-manager-plus)
[![Stars](https://img.shields.io/github/stars/seakee/CPA-Manager-Plus?style=flat-square&label=stars)](https://github.com/seakee/CPA-Manager-Plus/stargazers)

自托管的 AI Gateway 监控面板，追踪请求、成本、失败、额度和账号健康状态。

配合 [CPA / CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 及 OpenAI-compatible 网关使用，支持 Codex、Claude Code 等工具的流量观测。

[English](README.md) ｜ [在线演示](https://seakee.github.io/CPA-Manager-Plus/) ｜ [在线文档](https://seakee.github.io/CPA-Manager-Plus/docs/)

</div>

## 亮点

- 实时仪表盘展示今日请求量、成功率、平均延迟和预估成本，支持按模型、账号、项目和时间范围筛选。
- 按模型的成本排行和按账号的费用拆解，Token 粒度细分到输入、输出、推理和缓存。
- Codex 账号定时巡检，检查 quota 剩余、凭证有效性和工作区状态。触达限额的账号自动暂停，到 reset 时间后恢复。
- 一个 Docker 容器搞定，数据全在本地，没有遥测 SDK，也不需要注册账号。外部请求只限于你配置的 Gateway，以及你主动配置或触发的模型价格同步、OAuth、provider 等集成。

## 截图

<table>
  <tr>
    <td align="center">
      <strong>首页仪表盘</strong><br>
      <img src="img/home-zh.png" alt="CPA Manager Plus 首页仪表盘" width="420">
    </td>
    <td align="center">
      <strong>请求监控</strong><br>
      <img src="img/monitoring-zh.png" alt="请求监控中心" width="420">
    </td>
  </tr>
  <tr>
    <td align="center">
      <strong>用量分析</strong><br>
      <img src="img/usage-analytics-zh.png" alt="用量分析页面" width="420">
    </td>
    <td align="center">
      <strong>Codex 账号巡检</strong><br>
      <img src="img/codex-inspection-zh.png" alt="Codex 账号巡检页面" width="420">
    </td>
  </tr>
</table>

## 什么时候需要它

**"为什么我的 Codex 请求全部失败了？"** — 打开监控页面，可以看到失败率、状态码和受影响的账号或模型。失败原因以脱敏摘要呈现，原始错误体不会离开本机。

**"这周的 AI 流量花了多少钱？"** — 用量分析页面按模型、提供商、账号、项目拆解费用。可以看到哪个模型最贵，Token 在输入、输出、推理和缓存之间怎么分布。

**"我的 Codex 账号还能用吗？"** — 巡检页面列出每个账号的 quota 剩余、计划等级、reset 时间和凭证状态。如果账号被停用或撞了限额，CPAMP 会告诉你发生了什么、下一步怎么做。

## 产品能力

### 请求监控

经过 Gateway 的每条请求都会被记录并可搜索。监控页面提供三个视图：账号概览、调用方 API Key 汇总、以及展示单条请求的模型/状态/延迟/Token 用量的实时流。支持将请求历史导出为 JSONL，也可以从备份导入历史数据。

### 成本与用量分析

独立的分析页面按模型排列成本、展示 Token 构成、按账号拆解费用。筛选维度覆盖提供商、项目、渠道和任意日期范围。模型价格从 LiteLLM 和 OpenRouter 同步，提供商调价后成本估算会跟着更新。

### 账号健康与 Quota

CPAMP 对 Codex 账号做定时巡检：检查 quota window、reset credit 及其到期时间、凭证有效性（OAuth token、工作区状态），并判断账号应该暂停还是恢复。当账号触达 `usage_limit_reached` 时，对应认证文件会被临时禁用，到 reset 时间后自动恢复。手动禁用的账号不会被自动覆盖。

### Gateway 日常运维

面板同时覆盖 CPA 日常操作：管理提供商、认证文件、OAuth 登录、API Key、额度、日志、插件和系统配置。认证文件支持 JSON 粘贴或批量导入。

### 自托管与隐私

CPAMP 没有分析 SDK、没有云账号依赖，也不需要注册账号。默认只连接你配置的 CPA Gateway；模型价格同步、OAuth、provider 检查等可选功能只会在你明确配置或触发时访问对应外部服务。支持 Docker 容器或原生二进制（Linux、macOS、Windows，amd64/arm64），数据全部存储在本地文件中。

## 快速开始

CPA Manager Plus 配合 [CPA / CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 使用，CPA 是一个把请求路由到 OpenAI-compatible 提供商的 AI Gateway。

### 安装脚本

想按向导部署，可以直接运行：

```bash
curl -fsSLO https://raw.githubusercontent.com/seakee/CPA-Manager-Plus/main/bin/install-cpamp.sh
bash install-cpamp.sh
```

脚本会检查环境、选择语言、选择完整安装或仅安装 CPAMP、生成最小配置，并在最终确认后执行部署。更多选项见 [一键安装脚本](https://seakee.github.io/CPA-Manager-Plus/docs/deployment/installer.html)。

### CPA + CPAMP 一起部署

如果还没有在运行 CPA，用这个 Compose 文件同时启动两个服务：

```yaml
services:
  cli-proxy-api:
    image: eceasy/cli-proxy-api:latest
    restart: unless-stopped
    ports:
      - "8317:8317"
    volumes:
      - cpa-data:/app/data

  cpa-manager-plus:
    image: seakee/cpa-manager-plus:latest
    restart: unless-stopped
    ports:
      - "18317:18317"
    volumes:
      - cpa-manager-plus-data:/data

volumes:
  cpa-data:
  cpa-manager-plus-data:
```

```bash
docker compose up -d
```

打开 `http://<host>:18317/management.html`，通过 `docker compose logs cpa-manager-plus` 拿到管理员密钥，然后填写：

1. 管理员密钥。
2. CPA 地址：`http://cli-proxy-api:8317`。
3. CPA Management Key。
4. 请求监控偏好设置。

### 仅部署 CPAMP

如果 CPA 已经在运行，单独启动 CPAMP：

```bash
docker run -d \
  --name cpa-manager-plus \
  --restart unless-stopped \
  -p 18317:18317 \
  -v cpa-manager-plus-data:/data \
  seakee/cpa-manager-plus:latest
```

推荐 CPA 版本：`v7.1.39+`，HTTP 用量队列需要 `v6.10.8+`。

CPAMP 也支持作为 CPA 托管面板（`:8317`）或独立前端开发使用。Compose 变体、宿主机网络、升级、备份、反向代理和排障等部署细节请看 [文档站](https://seakee.github.io/CPA-Manager-Plus/docs/)。

## 文档

| 主题 | 文档 |
|---|---|
| 演示站 | [在线演示](https://seakee.github.io/CPA-Manager-Plus/) |
| 文档站 | [CPAMP Docs](https://seakee.github.io/CPA-Manager-Plus/docs/) |
| 从这里开始 | [快速开始](https://seakee.github.io/CPA-Manager-Plus/docs/guide/getting-started.html) |
| 安装脚本 | [一键安装脚本](https://seakee.github.io/CPA-Manager-Plus/docs/deployment/installer.html) |
| 运行模型 | [CPA gateway runtime 与 CPAMP](https://seakee.github.io/CPA-Manager-Plus/docs/guide/runtime-model.html) |
| Gateway 配置 | [Gateway 配置](https://seakee.github.io/CPA-Manager-Plus/docs/gateway/configuration.html)、[提供商与兼容接口](https://seakee.github.io/CPA-Manager-Plus/docs/gateway/providers.html)、[客户端接入](https://seakee.github.io/CPA-Manager-Plus/docs/gateway/clients.html) |
| 面板手册 | [仪表盘](https://seakee.github.io/CPA-Manager-Plus/docs/manual/dashboard.html)、[配置中心](https://seakee.github.io/CPA-Manager-Plus/docs/manual/configuration.html)、[AI 提供商](https://seakee.github.io/CPA-Manager-Plus/docs/manual/ai-providers.html)、[请求监控](https://seakee.github.io/CPA-Manager-Plus/docs/manual/monitoring.html)、[插件管理](https://seakee.github.io/CPA-Manager-Plus/docs/manual/plugins.html) |
| Docker 部署 | [Docker 部署](https://seakee.github.io/CPA-Manager-Plus/docs/deployment/docker.html) |
| 原生运行包 | [原生包部署](https://seakee.github.io/CPA-Manager-Plus/docs/deployment/native.html) |
| 原生包后台控制 | [原生包后台控制](https://seakee.github.io/CPA-Manager-Plus/docs/deployment/native-background-control.html) |
| Manager Server 配置、接口、数据和安全 | [Manager Server 指南](https://seakee.github.io/CPA-Manager-Plus/docs/operations/manager-server.html) |
| 反向代理 | [反向代理](https://seakee.github.io/CPA-Manager-Plus/docs/deployment/reverse-proxy.html) |
| 从旧 CPA-Manager 迁移 | [从 CPA-Manager 迁移](https://seakee.github.io/CPA-Manager-Plus/docs/migration/from-cpa-manager.html) |
| 重置管理员密钥 | [重置管理员密钥](https://seakee.github.io/CPA-Manager-Plus/docs/operations/reset-admin-key.html) |
| 常见问题 | [常见问题](https://seakee.github.io/CPA-Manager-Plus/docs/reference/faq.html) 和 [请求监控排障](https://seakee.github.io/CPA-Manager-Plus/docs/troubleshooting/request-monitoring.html) |
| 发布流程 | [docs/release.md](docs/release.md) |
| 版本说明 | [docs/release-notes](docs/release-notes) |
| 旧 Wiki | [仅作为过渡归档](https://github.com/seakee/CPA-Manager-Plus/wiki) |

## 数据与隐私

- CPAMP 不会回传遥测，也没有分析 SDK 或账号注册流程。外部连接只限于你配置的 CPA Gateway，以及你明确配置或触发的模型价格同步、OAuth、provider API 等集成。
- 所有数据（请求历史、凭证、配置）都存储在本机的本地文件中。
- Gateway 密钥在入库前加密，导出数据不包含原始错误体。
- CPAMP 用于监控你有权管理的流量：成本追踪、故障诊断和运维健康检查。

## 开发

```bash
npm install
npm run dev
npm run type-check
npm run lint
npm run test
npm run build
```

Manager Server：

```bash
cd apps/manager-server
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/cpa-manager-plus
```

本地构建 Docker stack：

```bash
docker compose -f docker-compose.manager.yml up --build
```

## 发布

- `npm run build` 生成单文件 `apps/web/dist/index.html`。
- `bin/release/package-native.sh` 将构建后的面板内置到原生包。
- 推送 `vX.Y.Z` 这类 tag 会触发 `.github/workflows/release.yml`。
- 发布产物包含 `management.html`、原生运行包，以及 `linux/amd64` 和 `linux/arm64` Docker 镜像。

## 致谢

- 感谢上游项目 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 和 [Cli-Proxy-API-Management-Center](https://github.com/router-for-me/Cli-Proxy-API-Management-Center) 提供基础与参考。
- 感谢 [Linux.do](https://linux.do/) 社区对项目推广与反馈的支持。

## 社区与反馈

- Telegram 交流群: https://t.me/cpa_mp

## Star History

<a href="https://www.star-history.com/?repos=seakee%2FCPA-Manager-Plus&type=date&logscale=&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=seakee/CPA-Manager-Plus&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=seakee/CPA-Manager-Plus&type=date&legend=top-left" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=seakee/CPA-Manager-Plus&type=date&legend=top-left" />
 </picture>
</a>

## 许可证

[MIT](https://github.com/seakee/CPA-Manager-Plus/blob/main/LICENSE) — Copyright 2026 Seakee。
