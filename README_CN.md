# CPA Manager Plus

[English](README.md)

CPA Manager Plus 是面向 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 的单文件管理面板，并提供 Manager Server 用于持久化请求监控。README 只作为项目入口；部署、运维和排障细节请看 [Wiki](https://github.com/seakee/CPA-Manager-Plus/wiki)。

- 推荐 CPA 版本：`v7.1.39+`
- HTTP 用量队列最低 CPA 版本：`v6.10.8+`
- 前端：React 19、Vite、单文件 `management.html`
- 后端：Go 1.24 Manager Server，使用 `modernc.org/sqlite`，无需 CGO
- 镜像：`seakee/cpa-manager-plus` 和 `ghcr.io/seakee/cpa-manager-plus`

## 面板预览

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

## 核心能力

- 将 CPA 用量队列沉淀为 SQLite 请求台账，支持实时监控、历史查询、导入导出和长期分析。
- 按模型、提供商、账号/认证文件、API Key 别名、项目、渠道和时间窗口拆解费用、Token、缓存、延迟、失败率与吞吐。
- Codex 账号运营：浏览器本地巡检和 Manager Server 定时巡检，识别 quota window、401 重登、停用工作区、失效账号，并给出启用、禁用、删除或重登建议。
- 账号池保护：遇到 Codex `usage_limit_reached` 时，可临时禁用认证文件并按 reset 时间恢复；只恢复由 CPAMP 临时禁用的账号，不会误启用手动禁用项。revoked/invalid OAuth token 等认证异常会进入候选队列，可人工处理或经身份校验后自动禁用。
- 模型价格从 LiteLLM 和 OpenRouter 同步，支持改名/带提供商前缀模型的候选匹配，费用估算会回流到首页、监控和用量分析。
- CPA 日常运维覆盖提供商、认证文件、OAuth、额度、API Key、日志、插件管理/商店和系统信息，支持 JSON 粘贴导入与批量认证文件操作。
- Manager Server 模式提供管理员密钥登录、CPA Management Key 加密存储、请求监控和服务端自动化；CPA 面板模式保留轻量入口，适合继续由 CPA 托管面板。
- 提供 Docker 镜像、Linux/macOS/Windows 的 `amd64`/`arm64` 原生包，以及可独立使用的单文件 `management.html`。

## 选择模式

| 模式 | 入口地址 | 登录凭证 | 适用场景 |
|---|---|---|---|
| Manager Server 模式 | `http://<host>:18317/management.html` | Manager Server 管理员密钥 | 新部署、请求监控、历史统计 |
| CPA 面板模式 | `http://<cpa-host>:8317/management.html` | CPA Management Key | 继续使用 CPA 托管面板，不需要 Manager Server 统计 |
| 前端开发模式 | Vite dev server 或 `apps/web/dist/index.html` | CPA 地址和密钥 | 本地 UI 开发 |

Manager Server 模式是完整 CPA Manager Plus 体验。CPA 面板模式保持为纯 CPA 面板：不配置 Manager Server，也不读取 Manager Server SQLite 数据。

## 快速开始

运行 Manager Server：

```bash
docker run -d \
  --name cpa-manager-plus \
  --restart unless-stopped \
  -p 18317:18317 \
  -v cpa-manager-plus-data:/data \
  seakee/cpa-manager-plus:latest
```

打开：

```text
http://<host>:18317/management.html
```

首次启动时，通过 `docker logs cpa-manager-plus` 获取生成的管理员密钥，然后在 setup 中填写：

- Manager Server 管理员密钥
- CPA 地址
- CPA Management Key
- 请求监控设置

完整 setup、Compose、Linux 宿主机网络、升级、备份和原生包部署请看 Wiki。

## 文档

| 主题 | 文档 |
|---|---|
| 从这里开始 | [Wiki 首页](https://github.com/seakee/CPA-Manager-Plus/wiki) |
| Docker 部署 | [Docker 部署 CPA Manager Plus](https://github.com/seakee/CPA-Manager-Plus/wiki/Docker-部署-CPA-Manager-Plus) |
| 原生运行包 | [二进制部署 CPA Manager Plus](https://github.com/seakee/CPA-Manager-Plus/wiki/二进制部署-CPA-Manager-Plus) |
| Manager Server 配置、接口、数据和安全 | [Manager Server 使用指南](https://github.com/seakee/CPA-Manager-Plus/wiki/Manager-Server-使用指南) |
| 反向代理 | [同域名反代](https://github.com/seakee/CPA-Manager-Plus/wiki/Reverse-Proxy-CPA-and-CPA-Manager-Plus-with-the-Same-Domain-Chinese) |
| 从旧 CPA-Manager 迁移 | [从 CPA-Manager 迁移](https://github.com/seakee/CPA-Manager-Plus/wiki/从-CPA-Manager-迁移) |
| 重置管理员密钥 | [重置管理员密钥](https://github.com/seakee/CPA-Manager-Plus/wiki/重置管理员密钥) |
| 常见问题 | [CPA Manager Plus 常见问题与解决方案](https://github.com/seakee/CPA-Manager-Plus/wiki/CPA-Manager-Plus-常见问题与解决方案) |
| 发布流程 | [docs/release.md](docs/release.md) |
| 版本说明 | [docs/release-notes](docs/release-notes) |

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

## 许可证

MIT
