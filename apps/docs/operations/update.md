# 更新 CPA Manager Plus

本页说明如何在不丢失 SQLite 数据、`data.key` 和本机 secret 的前提下更新 CPAMP。请选择与你当前部署方式对应的章节，不要用首次安装命令覆盖已有配置。

## 更新前检查

1. 阅读目标版本的 [版本说明](../reference/releases.md) 和 GitHub Release Upgrade Notes。
2. 记录当前镜像标签、原生包版本、启动命令、环境变量和数据目录。
3. 停止会修改相同 SQLite 的额外实例。同一个 CPA 用量队列只能由一个 Manager Server 消费。
4. 备份完整数据和配置：
   - `usage.sqlite`、`usage.sqlite-wal`、`usage.sqlite-shm`。
   - `data.key`。
   - 安装器目录中的 `secrets/`、`.env`、`compose.yaml` 或 `config.json`。
   - 自定义反向代理、systemd、launchd 或 Windows 服务配置。

详细备份方法见 [备份与恢复](./backup.md)。`data.key` 丢失后，SQLite 中加密保存的 CPA Management Key 无法恢复。

## 更新后会发生什么

- Manager Server 启动时自动执行兼容的 SQLite schema 和 metadata 迁移，不需要手工运行 SQL。
- 大型历史数据修正可能在 HTTP 服务开始监听后继续后台执行。
- migration 期间 account-history 或 dashboard-hourly rollup 可能暂停追平；相关页面会临时回退 raw events，性能可能暂时降低。
- 不要为了加速 migration 或 rollup 重建而启动第二个 Manager Server 连接同一 SQLite 或消费同一 CPA 队列。
- 可通过带管理员密钥的 `GET /status` 查看迁移、采集器和事件状态。

## 一键安装器生成的 Docker 部署

安装器生成的 Docker 部署不需要重新运行安装脚本。进入原安装目录更新镜像：

```bash
cd "$HOME/cpa-manager-plus"
docker compose pull
docker compose up -d
docker compose ps
```

如果安装时通过 `CPAMP_INSTALL_DIR` 选择了其他目录，请替换上面的路径。

完整 CPA + CPAMP 安装会同时拉取 Compose 中配置的 CPA 和 CPAMP 镜像。只想更新 CPAMP 时可以指定服务：

```bash
docker compose pull cpa-manager-plus
docker compose up -d cpa-manager-plus
```

不要为升级设置 `CPAMP_OVERWRITE=1` 重跑安装器。该选项用于重新生成配置，可能覆盖你维护的 `.env`、`compose.yaml`、CPA 配置或 `run.sh`。

## 手动 Docker Compose

### 使用 `latest`

```bash
docker compose pull cpa-manager-plus
docker compose up -d cpa-manager-plus
docker compose logs --tail=100 cpa-manager-plus
```

### 使用固定版本

先把 Compose 中的镜像从旧版本改为目标版本，例如：

```yaml
services:
  cpa-manager-plus:
    image: seakee/cpa-manager-plus:vX.Y.Z
```

然后执行：

```bash
docker compose pull cpa-manager-plus
docker compose up -d cpa-manager-plus
```

也可以使用对应的 GHCR 镜像：

```text
ghcr.io/seakee/cpa-manager-plus:vX.Y.Z
```

确认新容器继续挂载原来的 `/data` volume 或宿主机目录。不要为了更新创建新的空 volume。

## 手动 `docker run`

先检查当前容器的端口、volume、环境变量、network 和 `--add-host` 参数：

```bash
docker inspect cpa-manager-plus
```

拉取目标镜像并重建容器。下面是最小示例，实际命令必须保留原部署的全部参数：

```bash
docker pull seakee/cpa-manager-plus:vX.Y.Z
docker stop cpa-manager-plus
docker rm cpa-manager-plus
docker run -d \
  --name cpa-manager-plus \
  --restart unless-stopped \
  -p 18317:18317 \
  -v cpa-manager-plus-data:/data \
  seakee/cpa-manager-plus:vX.Y.Z
```

如果 CPA 跑在 Linux 宿主机，继续保留：

```text
--add-host=host.docker.internal:host-gateway
```

删除旧容器不会删除命名 volume，但删除 volume 会丢失数据。

## 一键安装器生成的原生部署

安装器通常生成如下结构：

```text
runtime/cpa-manager-plus_<version>_<os>_<arch>/
data/
secrets/
run.sh
cpa-manager-plus.service
```

更新时不要直接覆盖正在运行的旧目录：

1. 停止当前进程或 systemd 服务。
2. 备份 `data/`、`secrets/`、旧 runtime 目录、`run.sh` 和 service 文件。
3. 从 GitHub Releases 下载并解压目标包到 `runtime/` 下的新版本目录。
4. 把旧版本目录的 `config.json` 复制到新版本目录。安装器生成的相对路径会继续指向共享的 `data/` 和 `secrets/`。
5. 把 `run.sh` 的工作目录和二进制路径改为新版本目录。
6. 如果使用安装器生成的 systemd 文件，同步更新 `WorkingDirectory` 和 `ExecStart`，然后执行 `systemctl daemon-reload`。
7. 启动并完成验证后再决定是否清理旧 runtime；保留旧包可以缩短程序回滚时间。

不要只复制 `usage.sqlite` 而遗漏 WAL/SHM；备份应在进程停止后进行，或使用 [备份与恢复](./backup.md) 中的 SQLite 安全方法。

## 手动原生包

### macOS / Linux 前台或控制脚本

1. 执行 `./cpa-manager-plusctl stop`，或停止你自己的进程管理器。
2. 备份数据目录和 `data.key`。
3. 解压新包到新的版本目录。
4. 继续使用原来的外部 `USAGE_DATA_DIR` / `USAGE_DB_PATH`，或在停机状态下把 `config.json` 和 `data/` 复制到新目录。
5. 使用新目录里的控制脚本启动：

```bash
./cpa-manager-plusctl start
./cpa-manager-plusctl status
./cpa-manager-plusctl logs
```

不要把新包直接解压到仍在运行的目录，以免二进制、控制脚本和静态面板来自不同版本。

### Linux systemd 固定目录

如果服务始终从 `/opt/cpa-manager-plus/cpa-manager-plus` 启动：

```bash
sudo systemctl stop cpa-manager-plus
sudo cp -a /var/lib/cpa-manager-plus "/var/lib/cpa-manager-plus.backup.$(date +%Y%m%d%H%M%S)"
sudo cp -a cpa-manager-plus_vX.Y.Z_linux_amd64/. /opt/cpa-manager-plus/
sudo systemctl start cpa-manager-plus
sudo systemctl status cpa-manager-plus
```

保持 `/var/lib/cpa-manager-plus` 独立于程序目录，可以避免更新包覆盖运行数据。

### Windows

1. 使用控制脚本或服务管理器停止 CPAMP：

```powershell
.\cpa-manager-plusctl.ps1 stop
```

2. 备份 `data`、`config.json` 和服务配置。
3. 将新 ZIP 解压到新的版本目录。
4. 继续使用原数据目录，或在停止状态下复制配置和数据。
5. 使用新目录的脚本或 Windows 服务启动并检查日志：

```powershell
.\cpa-manager-plusctl.ps1 start
.\cpa-manager-plusctl.ps1 status
.\cpa-manager-plusctl.ps1 logs
```

如果 Windows 服务的可执行文件路径包含版本目录，需要同步修改服务配置。

## CPA 托管面板兼容模式

CPA 托管面板只更新浏览器前端，不会更新 Manager Server 二进制、SQLite schema 或后台采集能力。

确认 CPA 指向本项目：

```text
remote-management.panel-repo = https://github.com/seakee/CPA-Manager-Plus
```

CPA 通常会自动更新缓存面板。如果仍显示旧版本，删除 CPA 工作目录中的缓存文件并重新加载或重启 CPA：

```bash
rm static/management.html
```

如果开启了 `Disable Panel Auto Updates`，只有缓存文件不存在时 CPA 才会重新下载。删除前确认操作的是 CPA 的 panel cache，不是 Manager Server 的持久化数据。

## 自定义 `management.html` 或 `PANEL_PATH`

如果从 Release 手工部署单文件面板：

1. 下载目标版本的 `management.html`。
2. 校验 Release 中提供的 checksum。
3. 先保存旧文件，再用新文件原子替换静态站点或 `PANEL_PATH` 指向的文件。
4. 刷新反向代理缓存和浏览器缓存。

只替换 `management.html` 不会更新 Manager Server API。前端和 Manager Server 跨多个版本混用可能出现字段或功能不兼容，建议使用同一 CPAMP Release 的面板和 Manager Server。

## 更新后验证

基础检查：

```bash
curl -f http://127.0.0.1:18317/health
curl -f http://127.0.0.1:18317/usage-service/info
curl -f -H "Authorization: Bearer <CPAMP_ADMIN_KEY>" \
  http://127.0.0.1:18317/status
```

确认：

- 面板和服务显示目标版本。
- `configured` 为预期值。
- `collector.lastError` 为空或可解释。
- `lastConsumedAt`、`lastInsertedAt` 和 `eventCount` 正常更新。
- Dashboard、请求监控和 Usage Analytics 能读取数据。
- `/status` 中的后台 migration 最终完成，rollup checkpoint 继续推进。
- 反向代理部署的 `/management.html`、`/usage-service/*` 和管理 API 路径仍指向正确服务。

## 回滚原则

- Docker：将镜像标签改回旧版本并重建容器，继续挂载更新前的数据备份。
- 原生包：停止新版本，恢复旧二进制、配置和更新前的数据备份后再启动。
- 单文件面板：恢复旧 `management.html`。
- 不要假设旧程序一定能读取新版本迁移后的数据库。涉及 schema 或数据语义变更时，应同时恢复更新前的 SQLite、WAL/SHM 和 `data.key`。
- 如果失败原因不明确，保留新旧日志和数据库副本，不要反复启动多个版本写入同一数据库。
