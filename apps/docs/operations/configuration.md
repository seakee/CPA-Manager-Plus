# 配置与数据目录

CPAMP 的核心数据都在本地。部署时先搞清楚三件事：SQLite 放在哪里，`data.key` 怎么保存，管理员密钥从哪里来。

## 关键文件

| 文件               | 说明                                                  |
| ------------------ | ----------------------------------------------------- |
| `usage.sqlite`     | SQLite 数据库，保存请求事件、配置、价格、别名等数据。 |
| `usage.sqlite-wal` | SQLite WAL 文件，存在时必须一起备份。                 |
| `usage.sqlite-shm` | SQLite SHM 文件，存在时必须一起备份。                 |
| `data.key`         | 数据密钥，用于加密写入 SQLite 的敏感配置。            |

Docker 默认路径：

```text
/data/usage.sqlite
/data/data.key
```

原生包默认路径：

```text
./data/usage.sqlite
./data/data.key
```

## 管理员密钥

完整 Docker / 原生 Manager Server 模式使用 `cpamp_...` 管理员密钥登录。

可通过以下方式配置：

| 变量                         | 说明                   |
| ---------------------------- | ---------------------- |
| `CPA_MANAGER_ADMIN_KEY`      | 直接传入管理员密钥。   |
| `CPA_MANAGER_ADMIN_KEY_FILE` | 从文件读取管理员密钥。 |

如果未配置，首次启动会生成随机管理员密钥并输出到日志。该值不会再次显示。

## CPA Management Key

CPA Management Key 用于访问 CPA 管理接口。

它的保存位置取决于配置来源：

- 通过 setup 或面板保存的 CPA 连接，会使用 `data.key` 加密后写入 SQLite。
- 一键安装器接收的 `CPA_UPSTREAM_URL` 和 `CPA_MANAGEMENT_KEY` / `CPA_MANAGEMENT_KEY_FILE` 只作为一次性导入输入，成功后会使用 `data.key` 加密写入 SQLite，并删除临时 `secrets/cpa-management-key`。手动部署仍可使用这些环境变量作为运行时兼容方式，但不属于安装器的最终配置。

配置 API 只返回 `managementKeyConfigured` 状态，不返回可逆解密后的 CPA Management Key。CPAMP 服务端在代理请求时读取并解密该密钥；浏览器和第三方 iframe 不需要接触它。

连接记录的 authority 规则是：完整的 `manager_config_v1` 权威，并会覆盖过期的旧 `setup` 镜像；manager partial 而 setup 完整且兼容时，setup 可以补全 manager。没有完整 authority 且 partial 记录互相冲突时，服务端会拒绝猜测并要求使用 `store-cpa-connection --repair-conflict` 显式修复。

CPAMP 轻量面板由 CPA 托管，仍遵循 CPA 端口访问方式；它与 Manager Server 的服务端密钥存储是两条独立链路。

## 采集配置

推荐使用：

```text
USAGE_COLLECTOR_MODE=auto
```

自动模式会依次尝试 RESP Pub/Sub、HTTP queue 和 RESP pop。

约束：

- RESP 连接必须直连 CPA API 端口，通常是 `8317`。
- HTTP queue 可以经过 HTTP proxy。
- `pollIntervalMs` 不应超过 CPA 用量队列保留时间。
- CPA retention 默认 60s，最大 3600s。
- 同一个 CPA queue 只应由一个 Manager Server 消费。

## 用量生命周期配置

可恢复导入和历史归档的服务端限制由 Manager Server 环境变量控制：

- `USAGE_QUERY_LIMIT` 只限制兼容的 Usage 查询，不限制 Usage Maintenance 的完整 raw JSONL export。
- `USAGE_IMPORT_CHUNK_BYTES` 是服务端上传分块大小；面板使用 session 返回的 `chunk_size_bytes` 动态展示。
- `USAGE_IMPORT_DISK_QUOTA_BYTES`、`USAGE_IMPORT_MAX_SESSIONS` 和 `USAGE_IMPORT_SESSION_TTL_MINUTES` 分别控制活动临时文件总配额、活动会话数和会话 TTL。
- `USAGE_ARCHIVE_RETENTION_ENABLED` 与 `USAGE_ARCHIVE_RETENTION_DAYS` 控制自动 retention；retention 仍受小时汇总和归档覆盖门禁约束。启用后，一旦归档校验和删除覆盖门禁满足，就会自动删除超过保留期的历史原始事件。在发生第一次 raw deletion 之后，已配置价格的模型集合和上下文分层阈值结构将进入 fail-closed 冻结，不再允许结构性变动；但纯单价调整（包括服务层价格）仍可继续。

完整 export 只覆盖导出开始时仍存在的 raw usage events，不会把已删除的 archive segment 自动合并回 JSONL，也不替代包含 SQLite、WAL/SHM、`data.key` 和 `usage-archives/` 的备份。
