# 更新 CPA Manager Plus

## v2 Beta：从 1.x 升级，不是原地换镜像

::: warning 精确基线与停止条件
最低直接升级版本为 **`v1.14.10`**，当前 CLI 只接受这个精确声明，不代表所有更高 1.x 都自动合格。`<= v1.14.9` 必须先按原部署方式升级到 `v1.14.10`，验证正常后再停机；不要仅把 `--source-version` 改为该值。目标 CPA 最低 **`v8.0.55`**（Embedded 固定为该版本）。版本不符、未知/更新的 schema、源忙碌、证据失配或历史差异都必须按报告处理，不能修改 SQLite 标记来继续。
:::

1. 下载同一次 v2 Prerelease 的 `docker-compose.v2-embedded.yml`、`embedded-release-pairing.json` 与校验文件，三镜像必须保留 `<tag>@sha256:<digest>` 配对，见 [Beta 安装](../deployment/docker.md)。`releases/latest` 与安装脚本仍属于 1.x Stable。
2. 保存精确的 1.x 镜像/二进制、Compose、环境和数据卷映射。停止旧 Manager；若迁移托管 CPA，同时停止旧 CPA。备份 `usage.sqlite`、WAL/SHM、`data.key`、`usage-archives/`（若存在）、配置与 secrets，见 [备份](./backup.md)。阻止其他进程写入源目录。
3. 为 v2 使用**独立新数据卷**，保留原 1.x 卷不变且只读。复制、目标与备份目录不可包含彼此的源目录；临时快照目录也不能位于源或目标内。磁盘需求以预检报告为准。

### Docker：离线预检、copy-in、四域对账

下面在 v2 发布 Compose 所在目录运行。`LEGACY_MOUNT` 是**实际旧卷名**（先用 `docker inspect` 核对），不是新的空卷名。若旧部署使用 bind mount，把 `LEGACY_MOUNT` 和 `LEGACY_ROOT` 都设置为同一个实际宿主机绝对路径，使容器与回滚计划中的路径一致。示例卷名必须替换。

```bash
export LEGACY_MOUNT='actual-1x-manager-volume'
export LEGACY_ROOT='/legacy'
umask 077
mkdir -p upgrade-secrets
docker compose -f docker-compose.v2-embedded.yml stop
upgrade_cli() {
  docker compose -f docker-compose.v2-embedded.yml run --rm --no-deps \
    --add-host host.docker.internal:host-gateway \
    -v "$LEGACY_MOUNT:$LEGACY_ROOT:ro" \
    -v "$PWD/upgrade-secrets":/upgrade-secrets:ro \
    cpamp-manager upgrade "$@"
}
upgrade_cli preflight --legacy "$LEGACY_ROOT" \
  --source-version v1.14.10 --target-dir /data --json
```

先确认预检退出 `0` 且 `ready: true`，再分别执行：

```bash
upgrade_cli run --legacy "$LEGACY_ROOT" \
  --source-version v1.14.10 --target /data --json
upgrade_cli reconcile --legacy "$LEGACY_ROOT" \
  --source-version v1.14.10 --target /data --json
```

每步成功后才执行下一步。`run` 只备份及 copy-in，不会完成整个升级；默认备份位于目标的 `upgrade-backup/`。`reconcile` 比较 **`retained`、`pricing`、`coverage`、`history`** 四域，不适用项必须有原因。被报告为永久不完整的 UTC 日需要逐日审阅后，用同一 `reconcile` 命令增加重复的 `--acknowledge YYYY-MM-DD`；`--acknowledge-parity-gaps` 仅确认报告中旧版已丢失的 parity 日，不会接受其他实测失配。不要盲目加确认参数，也不要把确认解读为恢复了丢失历史。

### 完成 CPA 连接与最终核验

**External 路径**：由 CPA 操作者独立升级/配置 CPA 至至少 `v8.0.55`，使其可从 CLI 容器访问。把真实 Management Key 写入宿主机私有文件 `upgrade-secrets/cpa-management-key`（不要放入命令行）。以下 URL 为示例，必须替换：

```bash
upgrade_cli external-connect --target /data \
  --cpa-base-url 'http://your-cpa-host:8317' \
  --management-key-file /upgrade-secrets/cpa-management-key --json
upgrade_cli verify --legacy "$LEGACY_ROOT" --target /data --json
upgrade_cli status --db-path /data/usage.sqlite --json
```

只有真实连接、身份、模型面及对账证据通过且持久化阶段为 `complete` 才可启动正常产品。缺版本 header 时，`external-connect --cpa-version` 仅用于如实声明实际 CPA 版本，不会把 v7 变成 v8；密钥轮换后使用同一 URL 的 `--reprove` 重新证明，不要反复提交已拒绝凭证。

**Managed 路径**：还需导入旧 CPA 的配置/凭证并证明 Runtime 目标，不是用 `external-connect` 连接内部地址。先只启动目标 Runtime 和 Ingress，保持 Manager 停止；把旧 CPA 目录及其真实 auth 目录额外只读挂载到一次性 Manager 容器。命令格式如下（路径/源 CPA 版本来自实际旧部署，插件选择必须显式给出）：

```text
docker compose -f docker-compose.v2-embedded.yml up -d --no-deps cpamp-runtime cpamp-ingress
docker compose -f docker-compose.v2-embedded.yml run --rm --no-deps \
  -v <旧Manager挂载源>:<原LEGACY_ROOT>:ro \
  -v <旧CPA绝对目录>:/legacy-cpa:ro \
  -v <旧CPA凭证绝对目录>:/legacy-cpa-auth:ro \
  cpamp-manager upgrade cpa-import --legacy <原LEGACY_ROOT> --target /data \
  --cpa-dir /legacy-cpa --cpa-auth-dir /legacy-cpa-auth \
  --cpa-version <实际源CPA版本> --plugins false --json
```

这段是参数模板，替换所有尖括号后运行；`--plugins false` 是显式关闭插件的示例，按实际意图选择。`cpa-import` 的资格或目标证明拒绝时停止，保留报告，不手工复制凭证到 Runtime。成功后**保持 Runtime Supervisor 和目标 CPA 运行且为 Ready，仅保持长驻 Manager Server 停止**；一次性 Manager CLI 的 `upgrade verify` 需要实时读取 Runtime 的身份和 active artifact，不能先停止 Runtime。示例中已启动的 Ingress 可以保留，但应在宿主机防火墙或反向代理隔离公开流量；Ingress 存活不能替代 Runtime 的实时证明，也不能当成产品就绪。

在此状态分别运行上面的 `upgrade_cli verify`、`upgrade_cli status`；只有持久化阶段确认为 `complete`，才执行 `docker compose -f docker-compose.v2-embedded.yml up -d` 启动完整栈并恢复公开流量。保留原 1.x 数据与升级备份直到完整验收。公开 origin、控制台浏览器登录（安装时设置的控制台地址，默认 `/console`）、Client Key、历史/coverage 和实际模型请求都需核对，`/health` 不能代替它们。

### 精确回滚到原 1.x 数据

回滚不是把 v2 SQLite 挂回旧镜像。停止**整个 v2 栈**，保留 v2 目录供诊断，仍在同一 Compose 项目与相同只读源挂载下生成计划：

```bash
docker compose -f docker-compose.v2-embedded.yml stop
upgrade_cli rollback --prepare --legacy "$LEGACY_ROOT" --target /data \
  --volume "$LEGACY_MOUNT" --legacy-image seakee/cpa-manager-plus:v1.14.10
```

`--prepare` 只输出/保存计划，不控制 Docker。逐条在 **Docker 宿主机**检查输出的 preconditions：旧卷/绝对目录确实是 copy-in 的源，v2 已停，回滚端口空闲。默认旧 Manager 回滚端口是 **18318**，不同于 v2 公开端口 18317；容器内端口检查不能证明宿主机空闲。把输出的 Compose 内容和 invocation 应用于宿主机，或恢复原 1.x Compose 指向**原未修改的 1.x 卷**，使用计划的精确镜像与端口，恢复原 CPA 配置。不要使用浮动标签或 v2 数据副本。

旧 1.x 启动后，**必须先在 Docker 宿主机完成容器身份与挂载人工门禁，才能运行 `rollback --verify`**。CLI 没有 Docker socket，不能证明回答健康检查的容器确实使用了原数据卷。先以计划的实际回滚端口定位唯一容器，再把真实完整容器 ID 设为 `ROLLBACK_CONTAINER_ID`：

```bash
docker ps --no-trunc --filter publish=18318 --format '{{.ID}} {{.Image}} {{.Names}}'
export ROLLBACK_CONTAINER_ID='actual-full-container-id'
docker inspect --format '{{.Id}} {{.Config.Image}} {{.Image}}' "$ROLLBACK_CONTAINER_ID"
docker inspect --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Type}} {{.Name}} {{.Source}} -> {{.Destination}}{{println}}{{end}}{{end}}' "$ROLLBACK_CONTAINER_ID"
```

确认容器 ID 就是本次重启的旧 Manager；`.Config.Image` 必须是计划中的精确镜像引用，`.Image` 是对应的实际镜像 ID。对于 named volume，核对 `/data` 的 `.Name` 等于原 `LEGACY_MOUNT`，并执行 `docker volume inspect "$LEGACY_MOUNT"`，确认输出的 `Mountpoint` 与该容器 `/data` 的 `.Source` 相同；对于 bind mount，`.Source` 必须等于原始宿主机绝对路径。另在容器间路径不同的环境中人工核对它确实是 copy-in 只读挂载的同一源。任何不一致或无法确认就停止，不执行核验。将此检查记录与计划一起保留。

门禁通过后，按计划输出的 `--backup` 与端口执行 `--verify`。在上述一次性 CLI 容器中，宿主机不是 `127.0.0.1`；默认端口示例为：

```bash
upgrade_cli rollback --verify --legacy "$LEGACY_ROOT" --target /data \
  --legacy-image seakee/cpa-manager-plus:v1.14.10 \
  --status-url http://host.docker.internal:18318/health
upgrade_cli status --db-path /data/usage.sqlite --json
```

如果使用自定义备份或端口，保留计划打印的 `--backup` 并改为实际端口。`rolled_back` 仅记录 CLI 的健康身份、源目录、备份完整性与保留数据检查通过，**不包含 Docker 容器/实际挂载的自动证明或人工确认字段**。只有结合上述操作者保存的容器与挂载检查记录，才能把回滚视为完成；普通 HTTP 请求退出 `0` 也不足以证明回滚。若计划拒绝，不重建源卷或删除升级记录来强行继续。

### Native 与常见诊断

linux/amd64 Native 使用同版包的 `bin/cpa-manager-plus upgrade ...`；停止整个 launcher/systemd 栈后，把上面 `upgrade_cli` 换为该二进制，`--legacy` 改为原 1.x 绝对数据目录，`--target` / `--target-dir` 改为实际新 `data/`。External 凭证路径改为宿主机私有文件。默认 Native Manager 内部地址是 `127.0.0.1:18319`，运行回滚计划时用 `--manager-addr 127.0.0.1:18319`（有覆盖则用真实地址），并确认公开 Ingress 也已停止。恢复原 1.x 二进制/服务及原数据目录，按计划的独立端口启动，再从宿主机执行 `rollback --verify --legacy ... --target ... --status-url http://127.0.0.1:18318/health`；不要使用容器专用的 `host.docker.internal`。

| 报告/退出码 | 操作 |
| --- | --- |
| `0` | 当前步骤成功；仍须核对阶段，不等于所有步骤完成 |
| `1` | 阻塞/拒绝/待确认：读报告，停止自动推进 |
| `2` | 用法、环境或可重试错误：排除原因后再重试 |
| `upgrade_source_version_unsupported` / `upgrade_source_version_undeclared` | 核对实际源版本和精确 `v1.14.10` 声明 |
| `upgrade_source_busy` / `upgrade_store_locked` | 停止真实源/目标 Manager，不能移除锁文件规避 |
| `upgrade_source_changed` | 源仍在变化；停止所有写入，重新评估源和备份 |
| `upgrade_rollback_image_unpinned` / `upgrade_rollback_volume_unbound` | 提供精确旧镜像和原卷映射，不能改用新空卷 |
| `source_ownership_unavailable`（`/.well-known/cpamp-bootstrap`） | 1.x 数据卷被直接挂到了 `/data`（锁/密钥属主为 `0:0`，而 v2 Manager 以 `10002:10002` 运行）：停止容器并改挂新 `/data` + 只读 `/legacy:ro`；若仅需只读查看升级入口，只修改 `usage.sqlite*` 与 `data.key` 属主为 `10002:10002`，切勿递归 `chown -R /data`（`/data/runtime` 必须保持 `0:0`） |

### 误将 1.x 数据卷挂到 `/data` 时

1.x 容器以 `root`（`0:0`）写入 `/data/usage.sqlite.manager.lock` 与 `/data/data.key`（`0600`），而 v2 单镜像中的 Manager 以非特权用户 `10002:10002` 运行、Supervisor 要求 `/data/runtime` 保持 `0:0`。若误把 1.x 卷直接挂到 `/data`，v2 会停留在只读引导页（`/.well-known/cpamp-bootstrap/ui`）并报告 `source_ownership_unavailable`，且未修改 1.x 数据库与密钥文件：

1. **推荐做法（标准升级）**：停止当前容器，保留原 1.x 卷只读挂到 `/legacy:ro`，为 `/data` 创建全新空卷后按上文执行 `upgrade preflight` 与 `upgrade run`。
2. **仅打开只读升级入口验证原 1.x 管理员密钥**：Docker 命名卷在宿主机上不存在 `/data` 路径，所以不要直接在宿主机执行 `chown /data/...`，改用“停止 1.x 容器 → 一次性容器挂载该卷修改属主 → 启动 v2 容器 → 回到引导页重新检查”：

   ```bash
   docker stop <1x_container>
   docker run --rm -u 0:0 --entrypoint sh -v <1x_volume>:/data seakee/cpa-manager-plus:<v2_tag> \
     -c 'chown -h 10002:10002 /data/usage.sqlite* /data/data.key'
   docker start <v2_container>
   ```

   只修改 `usage.sqlite*` 与 `data.key` 的属主（属主变更只改文件元数据，不改写数据库内容）；切勿执行 `chown -R 10002:10002 /data`，`/data/runtime` 必须保持 `0:0`。完成后回到引导页点击“重新检查来源状态”。

## 1.x Stable：原部署方式内更新

以下原地拉取/替换二进制只用于 1.x 内更新，不是上述 v2 copy-in 流程。


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
- 可通过带管理员密钥的 `GET /status` 查看迁移、采集器、事件和 `databaseMaintenance` 状态。全局 Warning、系统信息页和请求监控页会在需要离线维护时显示明确提示。

### 如果数据库维护状态为 degraded

为保证大型数据库升级时 Manager Server 能先监听 HTTP，非空大表的缺失索引和部分旧派生数据清理不会在启动阶段无界执行。这不代表数据丢失，但缺失查询索引可能让历史请求查询明显变慢或超时。

Docker Compose：

```bash
docker compose stop cpa-manager-plus

docker compose run --rm --no-deps \
  cpa-manager-plus \
  cleanup-derived --db-path /data/usage.sqlite

docker compose start cpa-manager-plus
```

原生安装：

```bash
cpa-manager-plus cleanup-derived
# 非默认路径
cpa-manager-plus cleanup-derived --db-path /path/to/usage.sqlite
```

必须先停止 Manager Server，因为离线命令需要独占 SQLite 进程锁。Web UI 不会自动执行、启动 cleanup 子进程或在线创建大表索引。全局提示通过 `/status?scope=database-maintenance` 读取有界 metadata，不会为轮询维护状态扫描或统计 `usage_events`。命令完成并重启后，`/status.databaseMaintenance` 会从真实 metadata 自动恢复为 clean。`cleanup-derived` 不会删除、重建或改写 authoritative `usage_events`。

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

## CPAMP 轻量面板

CPAMP 轻量面板由 CPA 下载和托管。更新它只会更新浏览器前端，不会安装或更新 Manager Server、SQLite schema 或后台采集能力。

确认 CPA 指向本项目：

```yaml
remote-management:
  panel-github-repository: 'https://github.com/seakee/CPA-Manager-Plus'
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
- `/status.databaseMaintenance.required` 为 `false`；若为 `true`，确认 deferred index 和 offline job 数量，并按上面的离线步骤处理。
- 反向代理部署的 `/management.html`、`/usage-service/*` 和管理 API 路径仍指向正确服务。

## 回滚原则

- Docker：将镜像标签改回旧版本并重建容器，继续挂载更新前的数据备份。
- 原生包：停止新版本，恢复旧二进制、配置和更新前的数据备份后再启动。
- 单文件面板：恢复旧 `management.html`。
- 不要假设旧程序一定能读取新版本迁移后的数据库。涉及 schema 或数据语义变更时，应同时恢复更新前的 SQLite、WAL/SHM 和 `data.key`。
- 如果失败原因不明确，保留新旧日志和数据库副本，不要反复启动多个版本写入同一数据库。
