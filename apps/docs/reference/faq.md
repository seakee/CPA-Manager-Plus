# 常见问题

CPA Manager Plus 的完整监控和分析能力来自 Manager Server 托管面板。旧 CPA-Manager 通过 External Usage Service 接入用量服务的工作流不适用于 Plus。

CPAMP 统计和监控能力应从 Manager Server 托管面板进入：

```text
http://<host>:18317/management.html
```

## 应该选择哪种使用方式？

| 目标                                         | 推荐模式                                     |
| -------------------------------------------- | -------------------------------------------- |
| 新部署，需要全部功能                         | CPAMP 完整模式（Docker，推荐）               |
| 请求监控、历史统计、模型价格、别名、导入导出 | CPAMP 完整模式                               |
| 不增加额外服务，用更清晰的界面替代官方 UI    | [CPAMP 轻量面板](../deployment/cpa-panel.md) |
| 不使用 Docker，但需要全部功能                | CPAMP 完整模式（原生包）                     |

CPAMP 轻量面板由 CPA 直接托管，是官方 Management Center 的增强替代 UI。它不配置 Manager Server，也不读取 Manager Server SQLite 数据。需要完整 CPAMP 能力时，使用 Docker 或原生包启动 Manager Server，并打开 `:18317/management.html`。

## 打开面板后应该访问哪个地址？

CPAMP 完整模式（Docker 或原生包）：

```text
http://<host>:18317/management.html
```

CPAMP 轻量面板由 CPA 托管，通常从 CPA 端口访问：

```text
http://<cpa-host>:8317/management.html
```

如果你看到登录页而不是 setup，说明 Manager Server 已经配置过。请使用 CPAMP 管理员密钥登录。

## 管理员密钥和 CPA Management Key 有什么区别？

| 位置                                | 使用的密钥                                |
| ----------------------------------- | ----------------------------------------- |
| CPAMP Full Docker / 原生登录        | CPAMP 管理员密钥，通常以 `cpamp_...` 开头 |
| CPAMP 首次 setup 连接 CPA           | CPA Management Key                        |
| CPAMP 轻量面板登录                  | CPA Management Key                        |
| 普通模型 API 请求                   | CPA API 密钥                              |
| `GET /v1/models`                    | CPA API 密钥                              |
| setup 后的 CPAMP Manager Server API | CPAMP 管理员密钥                          |

不要混用这些密钥。通过 setup 或面板保存的 CPA 连接会把 CPA Management Key 用 `data.key` 加密后写入 SQLite；安装器 env/secret 管理的连接从安装目录读取密钥，不写入 SQLite。CPAMP 轻量面板由浏览器持有 CPA Management Key。

## Full Docker 打开的是登录页，不是 setup

说明 Manager Server 已经配置过。

请使用 CPAMP 管理员密钥：

```text
cpamp_...
```

不要在 CPAMP 登录表单中使用 CPA Management Key。如果管理员密钥丢失，阅读 [重置管理员密钥](../operations/reset-admin-key.md)。

## 忘记管理员密钥怎么办？

先停止 Manager Server 并备份数据目录，然后按 [重置管理员密钥](../operations/reset-admin-key.md) 执行。

## 只改了 CPA Panel Repository，为什么监控为空？

修改 CPA panel repository 只会改变 CPA 托管的前端页面。

请求监控和历史统计需要 Manager Server：

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

setup 填写：

```text
CPAMP 管理员密钥
CPA URL
CPA Management Key
```

不要再寻找旧的 External Usage Service 配置项。

## setup 默认 CPA 地址不符合环境

setup 表单中的默认 CPA 地址可能来自前端构建配置。

修复方式：

```text
1. 手动填写正确的 CPA 地址。
2. 使用 VITE_DEFAULT_CPA_BASE_URL=<your-cpa-url> 重新构建面板。
```

Docker Desktop 访问宿主机 CPA 常用：

```text
http://host.docker.internal:8317
```

同一个 Compose 网络常用：

```text
http://cli-proxy-api:8317
```

Linux 宿主机 CPA + Docker CPAMP 需要加 `--add-host=host.docker.internal:host-gateway`。详情见 [Docker 部署](../deployment/docker.md)。

## 请求监控为空怎么办？

先按 [请求监控排障](../troubleshooting/request-monitoring.md) 检查 `/status`、采集器、用量队列和保留时间。

常见原因：

```text
1. CPA 用量发布没启用
2. Manager Server 没配置完成
3. CPA URL 从 Manager Server 所在网络看不可达
4. CPA Management Key 错误
5. CPA 版本过旧
6. 采集模式和网络路径不匹配
7. 多个 Manager Server 消费同一个 CPA 用量队列
8. Manager Server 停机超过 CPA 队列保留时间
9. 轮询间隔长于队列保留窗口
```

检查 CPA 用量发布：

```yaml
usage-statistics-enabled: true
```

或通过 CPA Management API 启用：

```bash
curl -X PUT \
  -H "Authorization: Bearer <CPA_MANAGEMENT_KEY>" \
  -H "Content-Type: application/json" \
  -d '{"value":true}' \
  http://<cpa-address>:8317/v0/management/usage-statistics-enabled
```

检查 Manager Server 状态：

```bash
curl -H "Authorization: Bearer <CPAMP_ADMIN_KEY>" \
  http://<cpamp-host>:18317/status
```

重点字段：

```text
collector.lastError
lastConsumedAt
lastInsertedAt
eventCount
```

如果 `lastConsumedAt` 为空，说明 Manager Server 没消费到事件。检查 CPA URL、Management API、CPA 版本、用量发布和采集器错误。

如果 `lastConsumedAt` 变化但 `lastInsertedAt` 不变，说明 SQLite 写入失败。检查磁盘空间、权限和 `/data` 挂载。

如果两者都变化但页面为空，检查浏览器筛选条件、时间范围、硬刷新页面，并验证 `/v0/management/usage`。

## `unsupported RESP prefix 'H'` 是什么？

通常表示 RESP 采集器连到了 HTTP 端点。

RESP 模式必须直连 CPA API 端口，不能穿过普通 HTTP 反代。推荐修复：

```text
USAGE_COLLECTOR_MODE=auto
```

使用 CPA `v6.10.8+` 的 HTTP 用量队列，或使用当前推荐的 CPA `v7.1.39+`。

如果必须使用 RESP，CPA URL 应该是直连地址：

```text
http://cli-proxy-api:8317
```

不要使用公网 HTTPS 反代域名。

## HTTP 反向代理能代理 RESP 吗？

不能。RESP Pub/Sub 和 RESP pop 需要直接连接 CPA API 端口。HTTP queue 可以经过 HTTP proxy。

同域名部署时，阅读 [反向代理](../deployment/reverse-proxy.md)。核心规则：

```text
/management.html        -> CPAMP
/usage-service/*        -> CPAMP
/v0/management/*        -> CPAMP
/v1/*                   -> CPA
/backend-api/codex/*    -> CPA
OAuth callbacks         -> CPA
Fallback routes          -> CPA
```

CPAMP 管理路径使用 CPAMP 管理员密钥；`/v1/*` 使用普通 API 密钥。

## 容器无法连接宿主机 CPA

Docker 容器里的 `127.0.0.1` 指容器自身。

如果 CPA 跑在 Linux 宿主机，CPAMP 跑在 Docker：

```bash
docker run -d \
  --name cpa-manager-plus \
  --restart unless-stopped \
  --add-host=host.docker.internal:host-gateway \
  -p 18317:18317 \
  -v cpa-manager-plus-data:/data \
  seakee/cpa-manager-plus:latest
```

然后使用：

```text
http://host.docker.internal:8317
```

在容器内测试：

```bash
docker exec -it cpa-manager-plus sh
wget -qO- http://host.docker.internal:8317/healthz
```

## Docker 重建后数据没了

通常是 `/data` 没挂载，或 Plus 启动到了新的空 volume。

正确：

```bash
-v cpa-manager-plus-data:/data
```

错误：

```bash
docker run seakee/cpa-manager-plus:latest
```

从旧 CPA-Manager 迁移时尤其要注意 volume 名：

```text
旧项目常见 volume: cpa-manager-data
Plus 示例 volume:   cpa-manager-plus-data
```

如果期望看到旧数据，应挂载旧 volume 或复制旧数据。

## 为什么备份需要 data.key？

备份完整数据目录：

```text
usage.sqlite
usage.sqlite-wal
usage.sqlite-shm
data.key
```

通过 setup 或面板保存的 CPA 连接会把 CPA Management Key 用 `data.key` 加密后写入 SQLite。丢失 `data.key` 后，已加密的 CPA Management Key 无法恢复，只能重新保存 CPA 连接。

如果使用安装器 env/secret 管理连接，CPA Management Key 通常在安装目录的 `secrets/cpa-management-key`，不写入 SQLite。备份时要把安装目录里的 `secrets/` 和数据目录一起保存。

## Manager Server 返回 401

setup 后，Manager Server 接口需要：

```text
Authorization: Bearer <CPAMP_ADMIN_KEY>
```

示例：

```bash
curl -H "Authorization: Bearer <CPAMP_ADMIN_KEY>" \
  http://<cpamp-host>:18317/status
```

```bash
curl -H "Authorization: Bearer <CPAMP_ADMIN_KEY>" \
  http://<cpamp-host>:18317/v0/management/config
```

CPA Management Key 不能用于 CPAMP Manager Server-only API。

## `/models` 返回 412

说明 Manager Server 尚未完成 setup。

打开：

```text
http://<cpamp-host>:18317/management.html
```

先完成 setup。

## 停机期间的用量为什么无法恢复？

CPA 用量队列是内存队列，保留时间有限。

默认：

```text
60 seconds
```

最大：

```text
3600 seconds
```

如果 Manager Server 停机超过保留窗口，通常无法从 CPA 恢复那段时间的数据。请保持 Manager Server 持续运行。

## CPAMP 轻量面板缺少监控或模型价格

这是预期行为。

CPAMP 轻量面板不使用 Manager Server 分析能力，也不能挂接独立 Manager Server。需要监控、仪表盘、模型价格、API 密钥别名、用量导入导出和服务端巡检时，请打开：

```text
http://<cpamp-host>:18317/management.html
```

## CPA 面板仍显示旧面板

确认 CPA 配置指向本项目：

```yaml
remote-management:
  panel-github-repository: 'https://github.com/seakee/CPA-Manager-Plus'
```

如果新面板仍未加载，清理 CPA 缓存的面板文件，然后重新载入或重启 CPA：

```bash
rm static/management.html
```

如果开启了 `Disable Panel Auto Updates`，CPA 只有在缓存文件不存在时才会重新下载面板。

## CPAMP 会上传数据吗？

不会回传遥测。CPAMP 不包含分析 SDK，也不需要云账号。默认只连接你配置的 CPA 网关；模型价格同步、OAuth、提供商检查等可选功能只会在你明确配置或触发时访问对应外部服务。

## 在线演示站会连接真实后端吗？

不会。在线演示使用前端 mock 数据，不需要 CPA、Manager Server、CPA Management Key、Token 或 SQLite。

## release 里的 management.html 是什么？

`management.html` 是 release 包中的单文件管理面板，可由 Manager Server 托管，也可作为 CPAMP 轻量面板由 CPA 加载。在线文档仍通过 GitHub Pages 访问，不随安装包分发。
