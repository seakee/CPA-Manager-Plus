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

升级到使用无损 model 编码的版本时，Manager Server 会清空旧的 `usage_dashboard_hourly_rollups` 并重置 `dashboard_hourly` checkpoint。小时汇总启用时，后台 worker 会随后分批重建；禁用时则保持为空，直到重新启用。该格式迁移本身不会修改或删除 `usage_events`，也不会重置 account-history rollup；重建完成前相关长窗口查询会临时回退 raw events。新的编码会区分空 model、字面量 `-` 和包含前后空格的 model，避免合法的 `-` model 使整个查询回退。

升级旧数据库时，Manager Server 在启动阶段执行 schema/metadata 变更和必要的派生 rollup 重置，但不扫描历史 `usage_events`。需要扫描历史事件的 cache accounting 修正会在 HTTP 服务开始监听后，以每批 1000 条的方式在后台执行。每批数据更新和 checkpoint 在同一个事务中提交，进程重启后会从最后成功的 event ID 继续，不会重新处理已经提交的批次。

迁移期间：

- 新采集的事件会按新格式直接写入，不进入旧数据迁移目标范围。
- account history 和 dashboard hourly rollup 暂停追平，避免基于半迁移数据生成错误汇总。
- 日志输出迁移进度、完成状态或可重试错误。
- `GET /status` 的 `dataMigration` 字段可查看 `status`、`lastEventId`、`targetEventId` 和 `processedRows`；该接口不返回底层迁移错误文本。

迁移完成后，response metadata backfill 和两个 rollup worker 会自动继续。不要为了缩短迁移时间同时启动第二个 Manager Server 连接同一 SQLite 或消费同一 CPA 队列。

完整的优化原因、实现阶段和 100k benchmark 数据见 [2026-07-10 性能优化报告](./performance-optimization-2026-07-10.md)。

如果 `USAGE_QUOTA_COOLDOWN_ENABLED`、`USAGE_ACCOUNT_ACTIONS_ENABLED` 或 `USAGE_ACCOUNT_ACTIONS_AUTO_DISABLE` 由环境变量设置，面板中的对应开关会显示为环境变量来源并被锁定。要改成面板可编辑，需要移除环境变量并重启 Manager Server。

::: details 高级：运行时接口

## 运行时接口

| Endpoint                                                         | 用途                                                     |
| ---------------------------------------------------------------- | -------------------------------------------------------- |
| `GET /health`                                                    | 健康检查。                                               |
| `GET /status`                                                    | 采集器、SQLite、事件计数和后台数据迁移进度。             |
| `GET /usage-service/info`                                        | Manager Server 模式探测。                                |
| `GET /usage-service/config`                                      | 读取 CPAMP Manager Server 配置。                         |
| `PUT /usage-service/config`                                      | 保存 CPAMP 配置，必要时重启采集器。                      |
| `GET /usage-service/account-processing-policy`                   | 读取配额冷却、账号处理队列和自动禁用策略。               |
| `PATCH /usage-service/account-processing-policy`                 | 更新账号处理策略；被环境变量锁定的字段不能通过接口修改。 |
| `GET /usage-service/quota-cooldowns`                             | 读取当前活跃的配额冷却，用于认证文件页面展示恢复提示。   |
| `POST /setup`                                                    | 首次 setup。                                             |
| `GET /v0/management/usage`                                       | 兼容 usage data。                                        |
| `GET /v0/management/usage/export`                                | 导出 JSONL usage events。                                |
| `POST /v0/management/usage/import`                               | 导入 JSONL 或兼容旧快照。                                |
| `GET /v0/management/model-prices/usage-summary`                  | 返回模型价格页使用的轻量模型调用汇总。                   |
| `GET /v0/management/model-prices`                                | 模型价格。                                               |
| `PUT /v0/management/model-prices`                                | 替换保存的模型价格。                                     |
| `POST /v0/management/model-prices/sync`                          | 价格同步。                                               |
| `GET /v0/management/api-key-aliases`                             | API 密钥别名。                                           |
| `GET /v0/management/account-action-candidates`                   | 认证问题处理队列。                                       |
| `POST /v0/management/account-action-candidates/{id}/ignore`      | 忽略账号处理候选项。                                     |
| `POST /v0/management/account-action-candidates/{id}/resolve`     | 标记账号处理候选项已处理。                               |
| `POST /v0/management/account-action-candidates/{id}/enable`      | 重新启用候选项关联的认证文件。                           |
| `DELETE /v0/management/account-action-candidates/{id}/auth-file` | 删除候选项关联的认证文件。                               |
| `GET /v0/management/dashboard/*`                                 | 仪表盘数据。                                             |
| `GET /v0/management/monitoring/*`                                | 请求监控数据。                                           |
| `GET /v0/management/codex-inspection/*`                          | 服务端 Codex 巡检。                                      |
| `GET /models`, `GET /v1/models`                                  | setup 后代理 model-list 请求到 CPA。                     |
| `/v0/management/*`                                               | CPAMP 未处理的路径代理到 CPA。                           |

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

## 导入和导出

Manager Server 可以导出 JSONL / NDJSON usage events。

可以导入：

- Manager Server 导出的 JSONL / NDJSON。
- 带 request-level details 的旧 usage snapshot。

只有聚合数据的旧文件不能重建请求级 monitoring。对准确性有要求时，先在备份或 staging 数据库上测试导入。
