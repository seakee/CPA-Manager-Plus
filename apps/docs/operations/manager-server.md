# Manager Server 指南

Manager Server 是完整 CPAMP 体验的后端。它托管 `management.html`，保存本地 SQLite，用采集器消费 CPA 用量队列，并用 CPAMP 管理员密钥保护管理能力。

普通用户通常不需要从头阅读本页。根据目标直接进入对应文档：

| 目标                | 推荐文档                                                 |
| ------------------- | -------------------------------------------------------- |
| 第一次安装完整模式  | [快速开始](../guide/getting-started.md)                  |
| 修改 CPA 连接或监控 | [配置中心](../manual/configuration.md)                   |
| 请求监控没有数据    | [请求监控为空](../troubleshooting/request-monitoring.md) |
| 更新或备份          | [更新 CPAMP](./update.md)、[备份与恢复](./backup.md)     |

本页主要面向需要环境变量、自定义采集网络、运行时接口或数据目录的高级部署。

当你打开下面入口时，使用的是 Manager Server 模式：

```text
http://<host>:18317/management.html
```

当 CPA 自己托管下面入口时，属于 CPAMP 轻量面板：

```text
http://<cpa-host>:8317/management.html
```

CPAMP 轻量面板不会连接或读取 Manager Server SQLite，也没有完整的历史请求监控、模型价格、API 密钥别名、导入导出和服务端巡检历史。

## Manager Server 负责什么

- 托管内置管理面板。
- 执行首次 setup，或读取环境管理的 CPA 连接。
- 使用 `cpamp_...` 管理员密钥做登录认证。
- 使用 `data.key` 加密保存通过 setup / 面板写入的 CPA Management Key。
- setup 后代理 CPA Management API。
- 消费 CPA 用量事件。
- 将用量事件持久化到 SQLite。
- 提供仪表盘、请求监控、用量分析、模型价格、API 密钥别名、用量导入导出和服务端 Codex 账号巡检 API。

::: details 高级：架构和数据流

## 架构

```text
Browser
  -> Manager Server :18317
      -> /management.html
      -> /usage-service/info
      -> /usage-service/config
      -> /v0/management/usage              从 SQLite 读取
      -> /v0/management/model-prices       从 SQLite 读取
      -> /v0/management/api-key-aliases    从 SQLite 读取
      -> /v0/management/dashboard/*        从 SQLite 读取
      -> /v0/management/monitoring/*       从 SQLite 读取
      -> /v0/management/codex-inspection/* 从 SQLite / 后台任务读取
      -> 其他 /v0/management/*             代理到 CPA
      -> 采集器 -> CPA 用量队列
      -> /data/usage.sqlite
```

CPA 仍然需要单独运行，CPAMP 不包含 CPA 本体。

:::

## 首次 setup 与登录

首次启动时，CPAMP 需要管理员密钥。可以显式提供：

```bash
CPA_MANAGER_ADMIN_KEY='replace-with-a-long-random-admin-key'
```

如果不提供，Manager Server 会生成：

```text
cpamp_...
```

并只在启动日志中输出一次。

首次 setup 需要填写：

```text
管理员密钥
CPA URL
CPA Management Key
请求监控
采集模式
轮询间隔
```

setup 后：

- 浏览器登录使用 CPAMP 管理员密钥。
- setup / 面板保存的 CPA Management Key 会在服务端加密保存。
- 安装器 env/secret 模式下，Manager Server 从部署环境读取 CPA URL 和 CPA Management Key。
- Manager Server 使用解析后的 CPA Management Key 访问 CPA。
- 新浏览器不再需要 CPA Management Key。

## CPA 前置条件

请求监控依赖 CPA 用量发布和 CPA 用量队列。

最低要求：

```text
CPA v6.10.8+ 支持 HTTP 用量队列
```

推荐：

```text
CPA v7.1.39+
```

CPA Management API 必须启用：

```yaml
remote-management:
  secret-key: 'your CPA Management Key'
  allow-remote: true
```

用量发布可以由 CPAMP 在 setup / config save 时启用，也可以直接在 CPA 中设置：

```yaml
usage-statistics-enabled: true
```

队列保留时间由 CPA 控制：

```yaml
redis-usage-queue-retention-seconds: 60
```

默认 60 秒，最大 3600 秒。Manager Server 需要持续运行。

## 采集模式

默认：

```text
auto
```

行为：

```text
auto -> RESP Pub/Sub -> HTTP 用量队列 -> RESP pop fallback
```

| 模式        | 适用场景                                                 |
| ----------- | -------------------------------------------------------- |
| `auto`      | 推荐默认值。                                             |
| `subscribe` | 强制 RESP Pub/Sub，适合能直连 CPA API 端口的低延迟采集。 |
| `http`      | 强制 HTTP 用量队列，适合普通 HTTP 反向代理。             |
| `resp`      | 强制旧 RESP pop，必须直连 CPA API 端口。                 |

RESP 传输不能穿过普通 HTTP 反向代理。如果看到 `unsupported RESP prefix 'H'`，通常是 RESP 客户端连到了 HTTP 地址。

## 配置边界

Manager Server 管理：

- 绑定的 CPA URL。
- 加密后的 CPA Management Key。
- 请求监控开关。
- 采集模式、轮询间隔、batch size、query limit。
- SQLite 用量数据。
- 模型价格。
- API 密钥别名。
- 服务端巡检历史。

仍由 CPA 管理：

- `usage-statistics-enabled`
- `redis-usage-queue-retention-seconds`
- `remote-management`
- proxy / routing 配置
- logging 配置
- 认证文件
- 提供商配置
- CPA `config.yaml`

保存 CPAMP 配置不会重写完整 CPA `config.yaml`。

## 常用环境变量

| 变量                                    | 默认值                                                      | 说明                                                                                                                                                       |
| --------------------------------------- | ----------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `CPA_MANAGER_CONFIG`                    | 空                                                          | 可选配置文件路径；原生包默认使用二进制旁边的 `config.json`。                                                                                               |
| `HTTP_ADDR`                             | `0.0.0.0:18317`                                             | Manager Server 监听地址。                                                                                                                                  |
| `CPA_MANAGER_PPROF_ADDR`                | 空                                                          | 可选 Go pprof 监听地址；仅接受 `localhost`、`127.0.0.1` 或 `::1`。                                                                                         |
| `USAGE_DATA_DIR`                        | Docker: `/data`; native: `./data`                           | 数据目录。                                                                                                                                                 |
| `USAGE_DB_PATH`                         | Docker: `/data/usage.sqlite`; native: `./data/usage.sqlite` | SQLite 路径。                                                                                                                                              |
| `CPA_MANAGER_ADMIN_KEY`                 | 空                                                          | 可选管理员密钥。                                                                                                                                           |
| `CPA_MANAGER_ADMIN_KEY_FILE`            | `/run/secrets/cpa_admin_key`                                | 可选管理员密钥文件。                                                                                                                                       |
| `CPA_MANAGER_DATA_KEY`                  | 空                                                          | 可选数据加密 key。                                                                                                                                         |
| `CPA_MANAGER_DATA_KEY_FILE`             | `/run/secrets/cpa_data_key`                                 | 可选数据加密 key 文件。                                                                                                                                    |
| `CPA_MANAGER_DATA_KEY_PATH`             | Docker: `/data/data.key`; native: `./data/data.key`         | 自动生成的数据 key 路径。                                                                                                                                  |
| `CPA_UPSTREAM_URL`                      | 空                                                          | 可选环境变量管理的 CPA URL。                                                                                                                               |
| `CPA_MANAGEMENT_KEY`                    | 空                                                          | 可选环境变量管理的 CPA Management Key。                                                                                                                    |
| `CPA_MANAGEMENT_KEY_FILE`               | `/run/secrets/cpa_management_key`                           | 可选 CPA Management Key 文件。                                                                                                                             |
| `USAGE_COLLECTOR_MODE`                  | `auto`                                                      | `auto`、`subscribe`、`http` 或 `resp`。                                                                                                                    |
| `USAGE_RESP_QUEUE`                      | `usage`                                                     | RESP key 参数，通常保持默认。                                                                                                                              |
| `USAGE_RESP_POP_SIDE`                   | `right`                                                     | `right` 使用 `RPOP`；`left` 使用 `LPOP`。                                                                                                                  |
| `USAGE_BATCH_SIZE`                      | `100`                                                       | 单批最大记录数。                                                                                                                                           |
| `USAGE_POLL_INTERVAL_MS`                | `500`                                                       | 空闲轮询间隔。                                                                                                                                             |
| `USAGE_QUERY_LIMIT`                     | `50000`                                                     | 最近 usage events 返回上限。                                                                                                                               |
| `USAGE_DASHBOARD_HOURLY_ROLLUP_ENABLED` | `true`                                                      | 启用小时汇总 worker，以及 Dashboard 和严格无筛选 Usage Analytics 的 rollup 查询；排查 SQLite 写竞争或汇总异常时可临时设为 `false`，查询会回退 raw events。 |
| `USAGE_ARCHIVE_RETENTION_ENABLED`       | `false`                                                     | 启用启动时及每 24 小时一次的历史归档、校验和有界删除 worker；默认关闭，要求小时汇总保持启用，修改后需重启 Manager Server。                                 |
| `USAGE_ARCHIVE_RETENTION_DAYS`          | `30`                                                        | 自动 retention 的保留天数；仅在 retention 与小时汇总同时启用时生效。                                                                                       |
| `USAGE_CORS_ORIGINS`                    | `*`                                                         | 兼容接口 CORS origin。                                                                                                                                     |
| `USAGE_RESP_TLS_SKIP_VERIFY`            | `false`                                                     | RESP 跳过 TLS 校验。                                                                                                                                       |
| `USAGE_QUOTA_COOLDOWN_ENABLED`          | `false`                                                     | 启用多供应商额度冷却 worker，严格处理 Codex usage-limit 和 xAI free-usage-exhausted 信号。                                                                 |
| `USAGE_ACCOUNT_ACTIONS_ENABLED`         | `false`                                                     | 启用账号处理队列，用于记录需要人工处理的认证问题。                                                                                                         |
| `USAGE_ACCOUNT_ACTIONS_AUTO_DISABLE`    | `false`                                                     | 启用认证问题自动禁用；只有账号处理队列启用时才会生效。                                                                                                     |
| `PANEL_PATH`                            | 空                                                          | 使用自定义 `management.html`。                                                                                                                             |

启动优先级：

```text
environment variables > config.json > defaults
```

需要诊断 CPU、堆或 goroutine 时，可临时启用仅本机可访问的 pprof 服务：

```bash
CPA_MANAGER_PPROF_ADDR=127.0.0.1:6060 ./cpa-manager-plus
go tool pprof http://127.0.0.1:6060/debug/pprof/heap
```

配置文件中的等价字段是 `pprofAddr`。该服务默认关闭，不应通过 Docker 端口映射或反向代理暴露。

小时汇总默认启用。服务会分批追平历史事件，Dashboard 和严格无筛选的 Usage Analytics 长窗口核心统计会复用完整小时数据；带搜索或维度、状态、延迟、缓存条件的分析仍读取 raw events。如果 checkpoint 尚未追平、目标时区无法由 UTC 小时桶无损表达或 rollup 读取失败，相关查询会自动回退 raw events，并以限频日志记录运行异常。需要临时停止后台汇总时，可设置：

```bash
USAGE_DASHBOARD_HOURLY_ROLLUP_ENABLED=false
```

关闭后需重启 Manager Server，Dashboard 和 Usage Analytics 将始终读取 raw events。除下述启动时的一次性格式升级外，关闭该运行时开关本身不会删除当前格式的 rollup 数据。该开关不接入 UI。

历史用量归档位于解析后数据目录中的 `usage-archives/`。每个 segment 都是按顺序编号的 gzip JSONL 文件（`segment-*.jsonl.gz`）；用量导入只读取解压后的 JSONL，不会根据 `.gz` 后缀自动解压，恢复时应按 [备份与恢复](./backup.md) 的归档恢复步骤处理。如果只通过 `USAGE_DB_PATH` 或 `dbPath` 覆盖数据库位置、没有显式配置数据目录，归档目录会放在该 SQLite 文件旁；如果数据目录和数据库路径都显式配置，则归档目录跟随数据目录，因此备份时必须包含这个可能独立的位置。POSIX 系统上，Manager Server 将目录创建为 `0700`、文件创建为 `0600`；Windows 会继承父目录 ACL，因此应确保归档父目录仅服务账号和授权管理员可访问。SQLite 中保存 archive run、segment、维护锁和 event identity ledger。手动维护接口只接受 CPAMP Admin Key：

- `POST /v0/management/usage/archives/preview`：提交 `{"cutoff_timestamp_ms": ...}`，只读预览可归档事件数量和估算大小。
- `POST /v0/management/usage/archives`：提交相同的 `{"cutoff_timestamp_ms": ...}` 请求体并创建 `previewed` run。
- `GET /v0/management/usage/archives?limit=20`：读取最近的脱敏 run 摘要。
- `GET /v0/management/usage/archives/{id}`：读取脱敏进度和 segment 元数据。
- `POST /v0/management/usage/archives/{id}/resume`：继续 archive 或失败恢复。
- `POST /v0/management/usage/archives/{id}/verify`：重新读取 manifest、segment checksum 和 event digest。
- `POST /v0/management/usage/archives/{id}/delete`：仅在归档已验证且所有必需派生读路径覆盖 run target 后，按批次删除 raw rows。
- `HEAD /v0/management/usage/maintenance`：仅 Admin Key 可用的 capability probe；支持该功能的 Manager Server 返回 `204 No Content`。
- `GET /v0/management/usage/maintenance`：读取 raw/已删除数量、活动 run/锁、迁移与 aggregate readiness，以及 SQLite page/freelist 和文件大小统计。

创建 run 不会立即删除 raw。手动执行 `resume` 时，如果 cache-accounting migration 尚未完成会返回 coverage conflict；迁移完成后会先补齐仍待处理的 response metadata，再写入第一个 segment。稳定的手动 `archived` 和 `verified` run 不会阻止后续手动归档，因此无需启用删除也可以持续使用 archive/verify；自动 retention run 则会保持活动状态，直到 delete 阶段完成。只有 archive 文件、manifest 和 identity ledger 已验证，并且 cache-accounting migration、永久小时 aggregate、pricing/monitoring rollup、监控搜索索引以及 account-history/dashboard checkpoint 都已追平 run target 后，才允许删除。`GET /v0/management/usage/maintenance` 汇总主要 readiness 信号，而 delete 会在每个有界事务内重新执行完整门禁。服务重启或进程中断后可用同一个 run 继续。删除只清理 `usage_events` 行，归档文件和 identity ledger 会保留，因此重复导入已归档事件仍会被幂等跳过。该流程不会在线执行 SQLite `VACUUM`。自动 retention 默认关闭；当 `USAGE_DASHBOARD_HOURLY_ROLLUP_ENABLED=false` 时不会启动，启用前应确认归档目录有足够空间并演练恢复。

当当前查询范围或 summary comparison 范围命中已完成验证归档并删除的 raw 历史时，Monitoring analytics 响应会返回 `coverage` 对象。当前范围与对比范围的 raw/deleted 数量分别报告；这些数量只按时间范围统计，不会被提供方、模型、账号、搜索或其他 analytics 筛选条件缩小。对象同时包含 `core_aggregate_used` 和机器可读的 `fidelity_limitations`。永久小时 aggregate 与 event projection 仍可准确提供受支持的 summary、model 和 timeline 核心统计，但仅依赖 raw 的事件明细、延迟百分位、分布、失败诊断、凭证时间线或不受支持的搜索可能不完整。Monitoring 与 Usage Analytics 页面会明确展示该限制，不会把缺失的 raw rows 或仅 raw 指标中的零值误认为完整历史。

只有面板由 Manager Server 托管且 Manager Service 可用时，才会显示“用量维护”页面；普通 CPA 托管面板不会显示该入口。页面可以执行 preview/create/resume/verify/delete 并显示可回收空间，但物理压缩始终是离线 CLI 操作。

### 停服回收 SQLite 空间

逻辑删除后，SQLite 文件通常不会立即缩小。执行压缩前，先按 [备份与恢复](./backup.md) 备份完整数据组，停止所有连接同一数据库的 Manager Server，并保守预留至少相当于当前数据库文件大小的临时空间。静态的 `previewed`、`archived`、`verified` 和 `failed` run 不会阻断压缩；维护锁以及处于 `archiving`、`verifying`、`deleting` 的活动阶段会阻断。未完成的派生数据迁移可以保留，其 checkpoint 会被原样保存。不要手工删除 WAL、SHM 或维护锁。

原生包：

```bash
cpa-manager-plus compact-usage --db-path ./data/usage.sqlite
```

Docker Compose（服务已停止）：

```bash
docker compose stop cpa-manager-plus
docker compose run --rm --no-deps cpa-manager-plus \
  compact-usage --db-path /data/usage.sqlite
docker compose up -d cpa-manager-plus
```

Windows PowerShell 在停止服务后运行：

```powershell
.\cpa-manager-plus.exe compact-usage --db-path .\data\usage.sqlite
```

命令会取得 Manager Server 使用的同一进程级数据库锁，通过单一 SQLite 连接获取独占访问，依次执行 `quick_check`、外键检查、`wal_checkpoint(TRUNCATE)`、`VACUUM`、`integrity_check` 和第二次外键检查，再比较压缩前后的逻辑用量摘要。输出包含压缩前后的数据库、WAL、SHM、page 和 freelist 统计。它不会创建归档、删除 raw rows、重写归档文件、推进派生数据迁移或触碰 `data.key`。残留维护锁仍会阻断压缩：若锁属于可恢复的活动或 `failed` run，应启动 Manager Server 并继续该 run；若锁仍关联非活动或终态 run，应保留备份和日志并停止压缩以进一步诊断。不得手工删除维护锁。失败后保留数据库现场和完整备份。

升级到使用无损 model 编码的版本时，Manager Server 会清空旧的 `usage_dashboard_hourly_rollups` 并重置 `dashboard_hourly` checkpoint。小时汇总启用时，后台 worker 会随后分批重建；禁用时则保持为空，直到重新启用。该格式迁移本身不会修改或删除 `usage_events`，也不会重置 account-history rollup；重建完成前相关长窗口查询会临时回退 raw events。新的编码会区分空 model、字面量 `-` 和包含前后空格的 model，避免合法的 `-` model 使整个查询回退。

升级旧数据库时，Manager Server 在启动阶段执行 schema/metadata 变更和必要的派生 rollup 重置，但不扫描历史 `usage_events`。需要扫描历史事件的 cache accounting 修正会在 HTTP 服务开始监听后，以每批 1000 条的方式在后台执行。候选扫描、事件修正和过期派生行清理都使用有界事务并提交各阶段进度，进程重启后会从当前阶段继续，不会重新处理已经提交的批次；修正历史数据或清理旧 rollup 期间，读路径会回退到 raw events。

迁移期间：

- 新采集的事件会按新格式直接写入，不进入旧数据迁移目标范围。
- account history 和 dashboard hourly rollup 暂停追平，避免基于半迁移数据生成错误汇总。
- 日志输出迁移进度、完成状态或可重试错误。
- `GET /status` 的 `dataMigration` 字段可查看 `status`、`lastEventId`、`targetEventId`、`processedRows`、`changedRows` 和 `appliedRows`；该接口不返回底层迁移错误文本。

迁移完成后，response metadata backfill 和两个 rollup worker 会自动继续。不要为了缩短迁移时间同时启动第二个 Manager Server 连接同一 SQLite 或消费同一 CPA 队列。

历史 rollup 重建和过期行清理只会在 HTTP 监听可用后执行。启动阶段的索引准备严格受限：仅当目标表为空且索引名未被 parked 表保留时，Manager Server 才会创建缺失索引。非空表索引和被旧表占用的索引名会记录为 deferred，避免大表建索引延迟采集器启动。重建期间，查询使用当前完整 revision，尚未完成时回退到原始 `usage_events`；批次中断后，进程重启会从已提交 checkpoint 继续。这些任务不得修改或删除 `usage_events`。

数据库升级后，旧的请求监控 FTS generation 可能在配对投影行完成分批在线清理后继续保留；非空表索引也可能被延后，旧 quota cooldown identity 索引则可能需要离线替换。极端情况下，单个旧 quota observation group 超过在线安全批次限制时，迁移会进入 failed 状态并记录 `offline cleanup required`，原始 snapshot fallback 仍继续可用。日志出现 deferred index preparation、`cleanup requires offline finalization` 或 `offline cleanup required` 时，先停止所有使用该数据库的 Manager Server 进程，再使用同版本二进制执行一次命令，完成后重启服务：

```bash
cpa-manager-plus cleanup-derived --db-path /data/usage.sqlite
```

原生安装若使用默认数据库路径，可直接运行 `cpa-manager-plus cleanup-derived`。Manager Server 在整个生命周期内都会持有 `<数据库绝对路径>.manager.lock` 操作系统锁；锁仍被持有时，该命令会直接拒绝执行。锁文件会持久保留，无需手工删除，应停止 Manager Server 进程后重试。符号链接别名会解析到同一个锁；具有多个硬链接的数据库会被拒绝，因为 SQLite WAL/SHM 侧车文件无法安全共享这些别名。离线维护前应备份 SQLite 和 `data.key`。该命令会创建延后的索引、替换过期派生索引、完成超大旧 quota observation group 的迁移并删除已过期的 FTS/投影 generation，不会修改 `usage_events`。

完整的优化原因、实现阶段和 100k benchmark 数据见 [2026-07-10 性能优化报告](./performance-optimization-2026-07-10.md)。

如果 `USAGE_QUOTA_COOLDOWN_ENABLED`、`USAGE_ACCOUNT_ACTIONS_ENABLED` 或 `USAGE_ACCOUNT_ACTIONS_AUTO_DISABLE` 由环境变量设置，面板中的对应开关会显示为环境变量来源并被锁定。要改成面板可编辑，需要移除环境变量并重启 Manager Server。

::: details 高级：运行时接口

## 运行时接口

| Endpoint                                                         | 用途                                                          |
| ---------------------------------------------------------------- | ------------------------------------------------------------- |
| `GET /health`                                                    | 健康检查。                                                    |
| `GET /status`                                                    | 采集器、SQLite、事件计数和后台数据迁移进度。                  |
| `GET /usage-service/info`                                        | Manager Server 模式探测。                                     |
| `GET /usage-service/config`                                      | 读取 CPAMP Manager Server 配置。                              |
| `PUT /usage-service/config`                                      | 保存 CPAMP 配置，必要时重启采集器。                           |
| `GET /usage-service/account-processing-policy`                   | 读取配额冷却、账号处理队列和自动禁用策略。                    |
| `PATCH /usage-service/account-processing-policy`                 | 更新账号处理策略；被环境变量锁定的字段不能通过接口修改。      |
| `GET /usage-service/quota-cooldowns`                             | 读取当前活跃的配额冷却，用于凭证管理展示恢复提示。            |
| `POST /setup`                                                    | 首次 setup。                                                  |
| `GET /v0/management/usage`                                       | 兼容 usage data。                                             |
| `GET /v0/management/usage/export`                                | 导出 JSONL usage events。                                     |
| `POST /v0/management/usage/import`                               | 导入 JSONL 或兼容旧快照。                                     |
| `POST /v0/management/usage/archives/preview`                     | 预览历史归档范围（仅 Admin Key）。                            |
| `POST /v0/management/usage/archives`                             | 创建历史归档 run（仅 Admin Key）。                            |
| `GET /v0/management/usage/archives?limit=20`                     | 读取最近归档 run 的脱敏摘要（仅 Admin Key）。                 |
| `GET /v0/management/usage/archives/{id}`                         | 查询脱敏归档 run 状态和 segment 元数据（仅 Admin Key）。      |
| `POST /v0/management/usage/archives/{id}/resume`                 | 恢复归档 run（仅 Admin Key）。                                |
| `POST /v0/management/usage/archives/{id}/verify`                 | 校验归档 manifest/segment（仅 Admin Key）。                   |
| `POST /v0/management/usage/archives/{id}/delete`                 | 有界删除已验证 raw 数据（仅 Admin Key）。                     |
| `HEAD /v0/management/usage/maintenance`                          | 探测用量维护能力；成功返回 `204 No Content`（仅 Admin Key）。 |
| `GET /v0/management/usage/maintenance`                           | 读取维护 readiness 和 SQLite 可回收空间（仅 Admin Key）。     |
| `GET /v0/management/model-prices/usage-summary`                  | 返回模型价格页使用的轻量模型调用汇总。                        |
| `GET /v0/management/model-prices`                                | 模型价格。                                                    |
| `PUT /v0/management/model-prices`                                | 替换保存的模型价格。                                          |
| `POST /v0/management/model-prices/sync`                          | 价格同步。                                                    |
| `GET /v0/management/api-key-aliases`                             | API 密钥别名。                                                |
| `GET /v0/management/account-action-candidates`                   | 认证问题处理队列。                                            |
| `POST /v0/management/account-action-candidates/{id}/ignore`      | 忽略账号处理候选项。                                          |
| `POST /v0/management/account-action-candidates/{id}/resolve`     | 标记账号处理候选项已处理。                                    |
| `POST /v0/management/account-action-candidates/{id}/enable`      | 重新启用候选项关联的认证文件。                                |
| `DELETE /v0/management/account-action-candidates/{id}/auth-file` | 删除候选项关联的认证文件。                                    |
| `GET /v0/management/dashboard/*`                                 | 仪表盘数据。                                                  |
| `GET /v0/management/monitoring/*`                                | 请求监控数据。                                                |
| `GET /v0/management/codex-inspection/*`                          | 服务端 Codex 巡检。                                           |
| `GET /models`, `GET /v1/models`                                  | setup 后代理 model-list 请求到 CPA。                          |
| `/v0/management/*`                                               | CPAMP 未处理的路径代理到 CPA。                                |

setup 后，Manager Server 管理接口需要：

```text
Authorization: Bearer <CPAMP_ADMIN_KEY>
```

:::

## 数据和安全

必须备份：

```text
usage.sqlite
usage.sqlite-wal
usage.sqlite-shm
data.key
usage-archives/
```

安全边界：

- 管理员密钥不会明文保存；SQLite 中只保存 salt 和 HMAC 摘要。
- 通过 setup / 面板保存到 SQLite 的 CPA Management Key 会先加密。
- 只有 `usage.sqlite` 泄露时，保存的 CPA Management Key 不能直接读取。
- `usage.sqlite` 和 `data.key` 同时泄露时，保存的 CPA Management Key 可以被解密。
- `data.key` 丢失后，保存到 SQLite 的 CPA Management Key 无法恢复。
- 如果 CPA 连接由 env/secret 管理，同时备份安装目录里的 secret 文件。
- 请求元数据可能包含模型名、端点、账号标签、项目快照、Token 用量、延迟和失败摘要。
- 原始失败 body 只保存在本地 SQLite；普通 API 和 JSONL 导出只暴露脱敏摘要。
- 归档 JSONL 可能包含事件级 `fail_body` 和 `raw_json`，应使用与在线 SQLite 相同的访问控制保护。

## 导入和导出

Manager Server 可以导出 JSONL / NDJSON usage events。

可以导入：

- Manager Server 导出的 JSONL / NDJSON。
- 带 request-level details 的旧 usage snapshot。

大文件通过可恢复分块会话上传，总文件大小受 `USAGE_IMPORT_DISK_QUOTA_BYTES` 限制；单条 JSONL 记录或旧快照 `details` 数组中的单个对象不得超过 10 MiB。该单记录限制不会因分块上传而放宽。

只有聚合数据的旧文件不能重建请求级 monitoring。对准确性有要求时，先在备份或 staging 数据库上测试导入。
