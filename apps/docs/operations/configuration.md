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
