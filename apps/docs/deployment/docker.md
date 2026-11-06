# Docker 部署

## v2 Beta：Embedded 三镜像安装

::: warning 选择正确的发布通道
本节只适用于 v2 Beta。下方原有的单容器、`latest` 和独立 CPA 示例仅适用于 **1.x Stable**；`bin/install-cpamp.sh` 默认也仍安装 1.x。不要用它们替换 v2 三镜像组，也不要把已有 1.x 数据卷直接挂到 v2：先读 [升级与回滚](../operations/update.md)。
:::

### 下载同一次预发布的资产

从 [GitHub Releases](https://github.com/seakee/CPA-Manager-Plus/releases) 选择实际已发布的 v2 Beta **Prerelease**，下载同一版本的 `docker-compose.v2-embedded.yml`、`embedded-release-pairing.json` 和 `checksums.txt`。发布前不存在对应下载；下面的版本号是示例，必须替换为实际目标版本。

```bash
export TAG=v2.0.0-beta.1
curl -fLO "https://github.com/seakee/CPA-Manager-Plus/releases/download/$TAG/docker-compose.v2-embedded.yml"
curl -fLO "https://github.com/seakee/CPA-Manager-Plus/releases/download/$TAG/embedded-release-pairing.json"
curl -fLO "https://github.com/seakee/CPA-Manager-Plus/releases/download/$TAG/checksums.txt"
```

按 `checksums.txt` 核对下载的两份文件，并保留它们作为升级/回滚记录。配对记录绑定版本、源码提交、三镜像 Manifest Digest、Full Web 清单和 API 契约；不要混用不同版本的资产。Compose 中镜像为 `seakee/cpa-manager-plus:<tag>@sha256:<digest>`、`seakee/cpamp-ingress:<tag>@sha256:<digest>`、`seakee/cpamp-runtime:<tag>@sha256:<digest>`；不能移除 Digest、改用浮动标签或添加本地 `build:`。首个 Beta 的配对平台为 `linux/amd64`，不要把 Runtime 单独升级。

### 拓扑、数据与公开入口

| 服务 | 职责 | 持久化与端口边界 |
| --- | --- | --- |
| `cpamp-ingress` | Full Web、Manager API 与模型流量的统一入口 | 唯一公开端口 `${CPAMP_PUBLIC_PORT:-18317}:18317`；零卷挂载 |
| `cpamp-manager` | 管理、SQLite、Full Web/API 配对 | `cpamp-manager-data` → `/data`；只读挂载 Runtime transport secret；不发布宿主机端口 |
| `cpamp-runtime` | Runtime Supervisor 托管固定的官方 CPA `v8.0.55` | `cpamp-runtime-data` → `/runtime`；可写 transport secret；`8317`/`9081` 仅内部使用 |

`cpamp-runtime-transport-secret` 挂到 Manager 与 Runtime 的 `/run/cpamp/runtime-secret`，不要暴露给 Ingress。保留发布 Compose 的隔离：不挂 Docker socket，不使用 privileged/host network，不公开 Runtime 控制端口。Compose 卷的实际名称含项目名前缀；持续在同一目录/同一项目下操作，备份整个 Manager 与 Runtime 数据集，见 [备份与恢复](../operations/backup.md)。

### 首次初始化与认领

先为外部入口配置可从部署环境访问的真实 origin。**优先使用已配置 TLS 反向代理的 HTTPS origin**，例如 `https://cpamp.example.com`；替换为实际域名并确认代理/证书已配置。明文 HTTP 只用于可信本地/隔离测试网络，不能通过非可信网络传递 Handoff、Admin Key 或 Client Key。非默认宿主机端口还需设置 `CPAMP_PUBLIC_PORT`。origin 不是 CPA 内网地址。

```bash
export CPAMP_PUBLIC_ORIGIN='https://cpamp.example.com'
docker compose -f docker-compose.v2-embedded.yml config
docker compose -f docker-compose.v2-embedded.yml pull
docker compose -f docker-compose.v2-embedded.yml up -d
docker compose -f docker-compose.v2-embedded.yml exec -T cpamp-manager cpa-manager-plus initialize
```

`initialize` 是宿主机授权的一次性命令，输出 `/.well-known/cpamp-bootstrap/ui#handoff=...`。在公开 origin 后拼上**完整输出（含 fragment）**，在浏览器打开；它是短期敏感认领凭据，不要发截图或保存到服务日志。首次只读监听器可以保留运行；需要重新签发/恢复时，先停止可写 Manager，再用 `docker compose -f docker-compose.v2-embedded.yml run --rm --no-deps cpamp-manager initialize`，然后重新 `up -d`，不要删除数据库来重置安装。

浏览器里设置并保存 CPAMP Admin Key，选择 **New Managed**（新建托管 CPA）、**Adopt**（对已有 CPA 做资格检查和迁移）或 **External**（连接自行运维的 CPA）。Adopt/External 不是无条件成功路径：按向导检查报告处理不支持的来源、版本、凭证或不可证明的状态；遇到拒绝就停止，不手工修改安装记录。External 至少要求 CPA `v8.0.55`，详见 [运行模型](../guide/runtime-model.md)。

New Managed 完成后保存向导给出的一次性首个 Client Key，用它通过公开入口验证模型请求，再开始生产流量。Admin Key、CPA Management Key 和 Client Key 不可混用。进程 `/health` 正常不等于安装完成或 Gateway 已就绪；以向导的安装完成、路由验证和客户端连通性为准。

## 1.x Stable：手动部署参考


Docker 是新部署最省心的方式。CPAMP 镜像包含 Manager Server 和内置 `management.html` 面板；CPA / CLI Proxy API 仍是单独服务，可以和 CPAMP 放在同一个 Compose 文件里。

想让脚本检查环境并生成 Compose 文件，可以先看 [一键安装脚本](./installer.md)。下面的内容适合手动维护 Compose 或把 CPAMP 合入已有部署。

新部署建议使用 Manager Server 托管面板：

```text
http://<host>:18317/management.html
```

不要沿用旧 CPA-Manager 的“CPA 面板 + External Usage Service URL”思路。Plus 的完整能力来自 Manager Server；CPAMP 轻量面板是由 CPA 托管的独立 UI 选择，不连接或读取 Manager Server 的 SQLite 监控数据。

## 先选场景

| 你的环境                  | 建议做法                              |
| ------------------------- | ------------------------------------- |
| CPA 和 CPAMP 都没有安装   | 使用 [一键安装脚本](./installer.md)   |
| 已有 CPA，只需要完整模式  | 直接看[仅部署 CPAMP](#仅部署-cpamp)   |
| 需要自己维护 Compose 文件 | 使用本页的 CPA + CPAMP 示例           |
| 只想替换 CPA 官方管理界面 | 改用 [CPAMP 轻量面板](./cpa-panel.md) |

如果没有定制网络、镜像或 Compose 的需求，优先使用安装脚本，不必阅读本页全部高级配置。

## 前置要求

部署前先确认：

- 已运行的 CPA / CLI Proxy API，或准备在同一个 Compose 中启动 CPA。
- CPA Management API 已启用。
- CPA Management Key。
- 挂载并备份持久化 `/data`。
- 同一个 CPA 用量队列只由一个 CPAMP Manager Server 消费。

推荐 CPA 版本：

```text
v7.1.39+
```

HTTP 用量队列最低要求：

```text
v6.10.8+
```

CPA 需要允许 Manager Server 访问 Management API：

```yaml
remote-management:
  secret-key: '你的 CPA Management Key'
  allow-remote: true
```

请求监控依赖 CPA 用量发布：

```yaml
usage-statistics-enabled: true
```

也可以由 CPAMP 在首次 setup 或保存配置时启用。

本页的环境变量示例属于手动 Docker 部署和旧版本兼容路径。一键安装脚本不会把 CPA URL 或 CPA Management Key 留在最终 Compose 环境中，而是先执行一次 `store-cpa-connection`，使用 `/data/data.key` 加密写入 SQLite。配置 API 也只返回 `managementKeyConfigured`，不会把已保存的 CPA Key 回传浏览器。

## CPA + CPAMP 一起部署

如果还没有运行 CPA，用下面的 Compose 文件同时启动 CPA 和 CPAMP：

```yaml
services:
  cli-proxy-api:
    image: eceasy/cli-proxy-api:latest
    container_name: cli-proxy-api
    restart: unless-stopped
    ports:
      - '8317:8317'
    volumes:
      - cpa-data:/app/data

  cpa-manager-plus:
    image: seakee/cpa-manager-plus:latest
    container_name: cpa-manager-plus
    restart: unless-stopped
    ports:
      - '18317:18317'
    environment:
      HTTP_ADDR: '0.0.0.0:18317'
      USAGE_DB_PATH: '/data/usage.sqlite'
      CPA_MANAGER_DATA_KEY_PATH: '/data/data.key'
      # 托管部署建议显式设置：
      # CPA_MANAGER_ADMIN_KEY: "replace-with-a-long-random-admin-key"
      USAGE_COLLECTOR_MODE: 'auto'
      USAGE_BATCH_SIZE: '100'
      USAGE_POLL_INTERVAL_MS: '500'
      USAGE_QUERY_LIMIT: '50000'
    volumes:
      - cpa-manager-plus-data:/data
    depends_on:
      - cli-proxy-api
    healthcheck:
      test: ['CMD', 'wget', '-qO-', 'http://127.0.0.1:18317/health']
      interval: 10s
      timeout: 3s
      retries: 3

volumes:
  cpa-data:
  cpa-manager-plus-data:
```

```bash
docker compose up -d
```

打开：

```text
http://<host>:18317/management.html
```

首次 setup 填写：

```text
管理员密钥:         启动日志或 secret 文件中的 cpamp_...
CPA URL:            http://cli-proxy-api:8317
CPA Management Key: CPA remote-management.secret-key
```

如果没有设置 `CPA_MANAGER_ADMIN_KEY`，CPAMP 会生成管理员密钥，并只在启动日志输出一次：

```bash
docker compose logs cpa-manager-plus
```

setup 完成后，新浏览器登录只需要 CPAMP 管理员密钥。CPA Management Key 会在服务端加密保存。

## 仅部署 CPAMP

如果 CPA 已经在运行，只启动 CPAMP：

```bash
docker run -d \
  --name cpa-manager-plus \
  --restart unless-stopped \
  -p 18317:18317 \
  -v cpa-manager-plus-data:/data \
  seakee/cpa-manager-plus:latest
```

也可以使用 GHCR 镜像：

```text
ghcr.io/seakee/cpa-manager-plus:latest
```

## CPA URL 怎么填

| 场景                                     | CPAMP setup 中填写的 CPA URL                                                            |
| ---------------------------------------- | --------------------------------------------------------------------------------------- |
| CPA 和 CPAMP 在同一个 Compose network    | `http://cli-proxy-api:8317`                                                             |
| CPA 跑在 Docker Desktop 宿主机           | `http://host.docker.internal:8317`                                                      |
| CPA 跑在 Linux 宿主机，CPAMP 跑在 Docker | `http://host.docker.internal:8317`，并加 `--add-host=host.docker.internal:host-gateway` |
| CPA 是远程服务且只适合 HTTP queue        | `https://your-cpa.example.com`                                                          |

Linux 宿主机 CPA 示例：

```bash
docker run -d \
  --name cpa-manager-plus \
  --restart unless-stopped \
  --add-host=host.docker.internal:host-gateway \
  -p 18317:18317 \
  -v cpa-manager-plus-data:/data \
  seakee/cpa-manager-plus:latest
```

然后填写：

```text
http://host.docker.internal:8317
```

不要在容器里用 `127.0.0.1` 访问宿主机 CPA。容器里的 `127.0.0.1` 是容器自身。

::: details 高级：常用环境变量

## 常用环境变量

| 变量                         | 默认值                            | 说明                                    |
| ---------------------------- | --------------------------------- | --------------------------------------- |
| `HTTP_ADDR`                  | `0.0.0.0:18317`                   | Manager Server 监听地址。               |
| `USAGE_DATA_DIR`             | `/data`                           | 数据目录。                              |
| `USAGE_DB_PATH`              | `/data/usage.sqlite`              | SQLite 数据库路径。                     |
| `CPA_MANAGER_DATA_KEY_PATH`  | `/data/data.key`                  | 数据密钥路径。                          |
| `CPA_MANAGER_ADMIN_KEY`      | 空                                | 显式设置 Manager Server 管理员密钥。    |
| `CPA_MANAGER_ADMIN_KEY_FILE` | `/run/secrets/cpa_admin_key`      | 从文件读取管理员密钥。                  |
| `CPA_MANAGER_DATA_KEY`       | 空                                | 显式设置数据加密 key。                  |
| `CPA_MANAGER_DATA_KEY_FILE`  | `/run/secrets/cpa_data_key`       | 从文件读取数据加密 key。                |
| `CPA_UPSTREAM_URL`           | 空                                | 可选环境变量管理的 CPA URL。            |
| `CPA_MANAGEMENT_KEY`         | 空                                | 可选环境变量管理的 CPA Management Key。 |
| `CPA_MANAGEMENT_KEY_FILE`    | `/run/secrets/cpa_management_key` | 从文件读取 CPA Management Key。         |
| `USAGE_COLLECTOR_MODE`       | `auto`                            | `auto`、`subscribe`、`http` 或 `resp`。 |
| `USAGE_BATCH_SIZE`           | `100`                             | 单批最大采集记录数。                    |
| `USAGE_POLL_INTERVAL_MS`     | `500`                             | 空闲轮询间隔。                          |
| `USAGE_QUERY_LIMIT`          | `50000`                           | 最近用量事件返回上限。                  |

更多运行时配置见 [Manager Server 指南](../operations/manager-server.md)。

:::

## 数据持久化和备份

必须挂载 `/data`。Docker 默认数据：

```text
/data/usage.sqlite
/data/usage.sqlite-wal
/data/usage.sqlite-shm
/data/data.key
```

备份必须包含 SQLite 文件和 `data.key`：

```bash
docker run --rm \
  -v cpa-manager-plus-data:/data \
  -v "$PWD":/backup \
  alpine \
  tar czf /backup/cpa-manager-plus-data-backup.tar.gz -C /data .
```

`data.key` 很重要：

- `usage.sqlite` 保存用量数据和加密后的 CPAMP 配置。
- `data.key` 用来解密通过 setup / 面板保存到 SQLite 的 CPA Management Key。
- 如果 `data.key` 丢失，保存到 SQLite 的 CPA Management Key 无法恢复，只能重新保存 CPA 连接。
- 如果是手动 env/secret 部署，或安装器尚未完成/跳过 CPA 连接导入，同时备份安装目录里的 `secrets/`。

## 只读根文件系统

标准 CPAMP Docker/Compose 部署的根文件系统默认可写，不需要额外的临时卷配置。

如果给 Manager Server 容器启用 `readOnlyRootFilesystem: true` 等加固配置，SQLite 仍需要一个可写的临时目录。Kubernetes 可以在 `/tmp` 挂载可写临时卷：

```yaml
volumeMounts:
  - name: tmp
    mountPath: /tmp

volumes:
  - name: tmp
    emptyDir: {}
```

如果自行设置 `emptyDir.sizeLimit`，应根据数据库规模和实际工作负载评估容量。Unix 类环境也可以通过 `SQLITE_TMPDIR` 指向其他可写临时路径。

同时要确保数据库文件及 WAL/SHM 伴生文件对 Manager Server 运行用户可写。CPAMP 不会自动修改文件属主或权限，也不会迁移 SQLite 临时文件。

::: details 高级：采集协议和网络要求

## 采集路径

`USAGE_COLLECTOR_MODE=auto` 时，Manager Server 会按顺序尝试：

1. RESP Pub/Sub。
2. HTTP 用量队列。
3. RESP pop fallback。

RESP Pub/Sub 和 RESP pop 需要直接连接 CPA API 端口，通常是 `8317`。普通 HTTP 反向代理不适用于 RESP。HTTP 用量队列可以经过 HTTP proxy。

如果看到 `unsupported RESP prefix 'H'`，通常表示 RESP 采集器连到了 HTTP 地址。优先改用 `auto` 或 `http`，并确认 CPA 版本至少为 `v6.10.8+`。

:::

## 升级

升级前先备份 `/data`。

Compose：

```bash
docker compose pull
docker compose up -d
```

`docker run`：

```bash
docker pull seakee/cpa-manager-plus:latest
docker stop cpa-manager-plus
docker rm cpa-manager-plus
docker run -d \
  --name cpa-manager-plus \
  --restart unless-stopped \
  -p 18317:18317 \
  -v cpa-manager-plus-data:/data \
  seakee/cpa-manager-plus:latest
```

### 升级后显示数据库维护未完成

为避免大型历史数据库在升级时因无界 `CREATE INDEX` 或派生清理而阻塞 HTTP 监听，Manager Server 只会在启动阶段处理有界 schema/metadata 工作。若全局 Warning、系统信息页或 `/status.databaseMaintenance.required` 表示仍需维护，服务可以继续采集和响应，但历史请求查询可能明显变慢或超时。

在 Compose 文件所在目录执行：

```bash
docker compose stop cpa-manager-plus

docker compose run --rm --no-deps \
  cpa-manager-plus \
  cleanup-derived --db-path /data/usage.sqlite

docker compose start cpa-manager-plus
```

如果 Compose 服务名不是 `cpa-manager-plus`，请替换为实际服务名。不要在 Manager Server 仍运行时执行，也不要从 Web UI 在线触发清理；该命令需要独占 SQLite 进程锁。完成后重新启动，维护状态会从数据库 metadata 自动恢复为 clean，UI Warning 也会自动消失。

`cleanup-derived` 只处理派生清理、延后索引和旧索引替换，不会删除、重建或改写 authoritative `usage_events`。执行前仍应备份 `/data` 和 `data.key`。

## 验证

基础健康检查：

```bash
curl http://127.0.0.1:18317/health
curl http://127.0.0.1:18317/usage-service/info
```

setup 后检查采集器状态：

```bash
curl -H "Authorization: Bearer <CPAMP_ADMIN_KEY>" \
  http://127.0.0.1:18317/status
```

重点检查：

```text
configured
collector.lastError
lastConsumedAt
lastInsertedAt
eventCount
databaseMaintenance.required
databaseMaintenance.deferredIndexes
databaseMaintenance.offlineJobs
```

`databaseMaintenance.required=true` 表示服务可运行但查询性能可能降级；按上面的离线步骤处理。维护完成并重启后，应为 `false`，两个计数应为 `0`。

如果监控页面为空，继续按 [请求监控排障](../troubleshooting/request-monitoring.md) 检查。

## 相比旧 CPA-Manager 的变化

旧 CPA-Manager Docker 文档使用 `seakee/cpa-manager`，并描述了 CPA 面板外接 Usage Service。CPA Manager Plus 中：

- 镜像变为 `seakee/cpa-manager-plus`。
- 容器通常命名为 `cpa-manager-plus`。
- Full Docker / Manager Server 模式登录使用 CPAMP 管理员密钥，不使用 CPA Management Key。
- setup / 面板保存的 CPA Management Key 使用 `/data/data.key` 加密保存；安装器提供的 env/secret 输入只用于一次性导入，成功后从最终运行配置移除。
- CPAMP 轻量面板不会配置或挂接外部 Manager Server 统计。
