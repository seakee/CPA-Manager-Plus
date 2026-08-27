# 配置与数据目录

CPAMP 的核心数据都在本地。部署时先搞清楚三件事：SQLite 放在哪里，`data.key` 怎么保存，管理员密钥从哪里来。

## 关键文件

| 文件               | 说明                                                  |
| ------------------ | ----------------------------------------------------- |
| `usage.sqlite`     | SQLite 数据库，保存请求事件、配置、价格、别名等数据。 |
| `usage.sqlite-wal` | WAL 模式下可能存在；存在时必须一起备份。              |
| `usage.sqlite-shm` | WAL 模式下可能存在；存在时必须一起备份。              |
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

## SQLite 数据库位置

默认使用 `USAGE_DATA_DIR` 或 `USAGE_DB_PATH`。需要把完整 SQLite 连接参数传给 driver 时，可以改用单一的 `USAGE_DB_URL`；它与 `USAGE_DB_PATH` 互斥。配置文件中的等价字段是 `dbUrl` 和 `dbPath`，同样不能同时设置。

数据库位置优先级：

```text
环境变量 USAGE_DB_URL / USAGE_DB_PATH
> 环境变量 USAGE_DATA_DIR
> config.json dbUrl / dbPath
> 默认 data/usage.sqlite
```

`USAGE_DB_URL` 只接受绝对、本地、持久化的 `file:` URI，并要求显式设置 `_txlock=immediate`、foreign keys、journal mode、synchronous 和正数 busy timeout。完整格式、示例、journal mode 切换和网络文件系统限制见 [Manager Server 指南](./manager-server.md#高级-sqlite-database-url)。

使用 URL 且未显式设置 `CPA_MANAGER_DATA_KEY_PATH` 时，`data.key` 默认放在 URL 数据库的同一目录。无论使用哪种数据库位置配置，都应把数据库、`data.key` 和当前存在的 SQLite sidecar 文件作为一组备份。

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
- 通过安装器或环境变量管理的 CPA 连接，来自 `CPA_UPSTREAM_URL` 和 `CPA_MANAGEMENT_KEY` / `CPA_MANAGEMENT_KEY_FILE`。这种连接不写入 SQLite；如果使用一键安装脚本，密钥通常在安装目录的 `secrets/cpa-management-key`。

CPAMP 轻量面板由 CPA 托管，浏览器持有 CPA Management Key，符合 CPA 端口访问方式。

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
