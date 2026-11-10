# 原生包部署

## v2 Beta：仅 linux/amd64

首个 v2 Beta 原生发行资产是 **`cpa-manager-plus-native-v2_<ver>_linux_amd64.tar.gz`**，从对应的 GitHub Prerelease 手动下载并按同版 `checksums.txt` 核验。不要用 1.x 的 `cpa-manager-plus_<ver>_<os>_<arch>` 命名推算 v2 下载地址。v2 Beta **不发布** `darwin_amd64`、`darwin_arm64`、`linux_arm64`、`windows_amd64`、`windows_arm64` 原生包；这些平台继续使用 1.x Stable，或在满足 `linux/amd64` 平台要求的 Docker 主机上运行 [Embedded Beta](./docker.md)。External 是 CPA 的运维模式，不会使未支持平台获得 v2 原生包。

### 包内容与运行边界

与下方 1.x 单二进制包不同，v2 包内有：

- `bin/cpa-manager-plus`、`bin/cpa-manager-plus-ingress`、`bin/cpa-manager-plus-runtime-supervisor`、`bin/cli-proxy-api`（固定官方 CPA `v8.0.55`）。
- `cpa-manager-plus-launcher`（根目录与 `bin/` 中）、`cpa-manager-plusctl`、`cpa-manager-plus.service`。
- `share/cpamp/runtime/cpa-artifact.json`、`share/cpamp/runtime/config.seed.yaml`、根 `LICENSE` 与 `share/licenses/cli-proxy-api/LICENSE`。
- `data/`、`run/`、`logs/`；默认 Manager 数据位于 `data/usage.sqlite`、`data/data.key`，Runtime 数据位于 `data/runtime/`。备份还要包含 SQLite WAL/SHM、`usage-archives/`（存在时）与 Runtime 状态，见 [备份](../operations/backup.md)。

Launcher 管理 Runtime Supervisor、Manager、Ingress，并由 Runtime 托管 CPA；不能仅启动 Manager 二进制替代完整栈。Launcher 以 root 运行，只有 CPA 子进程降权到专用身份；`cpa-manager-plusctl install` 创建 `cpamp-cpa` 账号及隔离目录。不要套用下方 1.x 的单进程 systemd 示例。

### 安装与首次认领

将包解压到固定目录（以下以 `/opt/cpa-manager-plus` 为例，目录里应直接包含 `bin/` 与控制脚本，而非再嵌套一层归档目录）。在该目录执行：

```bash
sudo ./cpa-manager-plusctl install --home /opt/cpa-manager-plus
sudo env USAGE_DATA_DIR=/opt/cpa-manager-plus/data \
  ./bin/cpa-manager-plus initialize
```

记录命令输出的短期 `/.well-known/cpamp-bootstrap/ui#handoff=...`；不要把它当成日志或管理员密钥。生成的 systemd 服务读取 **`<home>/cpa-manager-plus.env`**，此例为 `/opt/cpa-manager-plus/cpa-manager-plus.env`。创建/编辑时保留该文件已有配置，并限制为 root 所有、`0600`：

```bash
sudo touch /opt/cpa-manager-plus/cpa-manager-plus.env
sudo chown root:root /opt/cpa-manager-plus/cpa-manager-plus.env
sudo chmod 0600 /opt/cpa-manager-plus/cpa-manager-plus.env
sudoedit /opt/cpa-manager-plus/cpa-manager-plus.env
```

在文件中设置真实公开 origin，例如已配置 TLS 反向代理的域名：

```dotenv
CPAMP_PUBLIC_ORIGIN=https://cpamp.example.com
```

优先使用 HTTPS，确保反向代理与证书已正确配置，并将示例替换成实际可达域名。明文 HTTP 仅用于可信本地/隔离测试网络，不用于经非可信网络传递 Handoff、Admin Key 或 Client Key。修改环境文件后通过重启服务生效（首次也可用 restart 启动）：

```bash
sudo systemctl restart cpa-manager-plus.service
sudo systemctl status cpa-manager-plus.service
```

在浏览器将公开 origin 与完整 Handoff 路径拼接，设置 Admin Key、选择 New Managed / Adopt / External 并完成资格检查；New Managed 保存首次 Client Key 并验证公开入口。安装完成后，后续登录打开安装时设置的控制台地址（向导默认 `/console`，例如 `https://cpamp.example.com/console`；改了路径就换成新路径，前端 API 随之变化）；v2 不提供 `/management.html`，访问返回 404。重新签发 `initialize`、重置管理员密钥、备份或迁移前先停止整个服务；不要并行运行 systemd 和 `cpa-manager-plusctl start`。升级已有 1.x 必须使用 [v1.14.10 离线迁移和回滚手册](../operations/update.md)，不得指向原 1.x 目录直接启动 v2。

当前 Native 包的许可证查验以实际包内 `LICENSE` 与 `share/licenses/cli-proxy-api/LICENSE` 为准；镜像的第三方清单与 Provenance 路径不适用于 Native，见 [发布与许可证](../reference/releases.md)。

## 1.x Stable：多平台单二进制包


不想使用 Docker 时，可以直接运行 CPAMP 原生包。它适合已有进程管理、systemd、launchd 或 Windows 服务托管习惯的环境。

原生包模式仍然是 Manager Server 模式：二进制会托管 `/management.html`，本地保存 SQLite 数据，并使用 CPAMP 管理员密钥登录。本节的 `:18317/management.html` 入口属于 1.x Stable；v2 Beta 使用安装时设置的控制台地址（默认 `/console`）。它不是旧 CPA-Manager 的“给 CPA 端口面板外接 Usage Service”工作流。

如果只想安装 CPAMP 原生包，可以使用 [一键安装脚本](./installer.md)。脚本不会原生安装 CPA；完整新部署仍建议用 Docker。

## 最短安装流程

1. 确认 CPA 已经运行，并准备 CPA 地址和 CPA Management Key。
2. 从 GitHub Release 下载与你系统和架构匹配的原生包。
3. 解压后运行 `cpa-manager-plus`，或使用包内的后台控制脚本。
4. 打开 1.x Stable 入口 `http://<host>:18317/management.html`。
5. 使用日志中生成的 CPAMP 管理员密钥登录并连接 CPA。

需要长期运行时，再选择 systemd、launchd、Windows 服务或其他进程管理方式。

## 前置要求

运行前先准备：

- CPA / CLI Proxy API 单独运行。
- CPA Management API 已启用。
- CPA Management Key。
- CPAMP 数据目录持久化并纳入备份。
- 同一个 CPA 用量队列只由一个 CPAMP Manager Server 消费。

推荐 CPA 版本：

```text
v7.1.39+
```

HTTP 用量队列最低要求：

```text
v6.10.8+
```

## 下载

从 [GitHub Releases](https://github.com/seakee/CPA-Manager-Plus/releases/latest) 下载对应平台包。

常见包名：

```text
cpa-manager-plus_<version>_linux_amd64.tar.gz
cpa-manager-plus_<version>_linux_arm64.tar.gz
cpa-manager-plus_<version>_darwin_amd64.tar.gz
cpa-manager-plus_<version>_darwin_arm64.tar.gz
cpa-manager-plus_<version>_windows_amd64.zip
cpa-manager-plus_<version>_windows_arm64.zip
```

Linux 查看架构：

```bash
uname -m
```

映射：

```text
x86_64  -> linux_amd64
aarch64 -> linux_arm64
arm64   -> linux_arm64
```

## 手动运行

macOS / Linux：

```bash
tar -xzf cpa-manager-plus_vX.Y.Z_linux_amd64.tar.gz
cd cpa-manager-plus_vX.Y.Z_linux_amd64
./cpa-manager-plus
```

Windows PowerShell：

```powershell
Expand-Archive .\cpa-manager-plus_vX.Y.Z_windows_amd64.zip -DestinationPath .
cd .\cpa-manager-plus_vX.Y.Z_windows_amd64
.\cpa-manager-plus.exe
```

打开（1.x Stable）：

```text
http://<host>:18317/management.html
```

如果没有配置管理员密钥，进程会在日志中输出一次生成的 `cpamp_...`。请立即保存。

也可以显式设置：

macOS / Linux：

```bash
CPA_MANAGER_ADMIN_KEY='replace-with-a-long-random-admin-key' ./cpa-manager-plus
```

Windows PowerShell：

```powershell
$env:CPA_MANAGER_ADMIN_KEY = 'replace-with-a-long-random-admin-key'
.\cpa-manager-plus.exe
```

## 数据位置

默认情况下，原生包会在二进制旁边创建：

```text
config.json
data/usage.sqlite
data/data.key
```

可通过环境变量覆盖：

```bash
USAGE_DATA_DIR=/var/lib/cpa-manager-plus ./cpa-manager-plus
```

或：

```bash
USAGE_DB_PATH=/var/lib/cpa-manager-plus/usage.sqlite ./cpa-manager-plus
```

需要备份：

```text
data/usage.sqlite
data/usage.sqlite-wal
data/usage.sqlite-shm
data/data.key
```

`data.key` 用来解密已保存的 CPA Management Key。丢失后只能重新保存 CPA 连接。

::: details 高级：Linux systemd 示例

## Linux systemd 示例

安装到固定目录：

```bash
sudo mkdir -p /opt/cpa-manager-plus /var/lib/cpa-manager-plus
sudo cp -a cpa-manager-plus_vX.Y.Z_linux_amd64/* /opt/cpa-manager-plus/
sudo useradd --system --no-create-home --shell /usr/sbin/nologin cpa-manager-plus
sudo chown -R cpa-manager-plus:cpa-manager-plus /opt/cpa-manager-plus /var/lib/cpa-manager-plus
```

创建 `/etc/systemd/system/cpa-manager-plus.service`：

```ini
[Unit]
Description=CPA Manager Plus Manager Server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=cpa-manager-plus
Group=cpa-manager-plus
WorkingDirectory=/opt/cpa-manager-plus
ExecStart=/opt/cpa-manager-plus/cpa-manager-plus
Restart=on-failure
RestartSec=3

Environment=HTTP_ADDR=0.0.0.0:18317
Environment=USAGE_DATA_DIR=/var/lib/cpa-manager-plus
# 推荐用环境文件或 secret manager 提供稳定密钥。
# Environment=CPA_MANAGER_ADMIN_KEY=replace-with-a-long-random-admin-key

[Install]
WantedBy=multi-user.target
```

启动：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now cpa-manager-plus
sudo systemctl status cpa-manager-plus
```

查看日志：

```bash
journalctl -u cpa-manager-plus -f
```

:::

## 首次 setup

打开（1.x Stable）：

```text
http://<host>:18317/management.html
```

填写：

```text
管理员密钥:         日志中的 cpamp_... 或你配置的管理员密钥
CPA URL:            http://127.0.0.1:8317、http://<cpa-host>:8317 或你的 CPA 地址
CPA Management Key: CPA remote-management.secret-key
```

setup 后：

- 浏览器登录使用 CPAMP 管理员密钥。
- CPA Management Key 会在服务端加密保存。
- 新浏览器不再需要 CPA Management Key。

## 后台运行

原生包内置后台控制脚本，可以直接执行 `start`、`status`、`logs`、`restart` 和 `stop`。脚本会写入 PID 记录和日志文件，并对默认运行目录使用私有权限。详见 [原生包后台控制](./native-background-control.md)。

生产环境也可以使用 systemd、launchd、Windows 服务管理器或进程管理工具托管进程。无论使用哪种方式，都要保证数据目录持久化并纳入备份。

## 升级

1. 停止原生进程。
2. 备份数据目录，包括 `data.key`。
3. 解压新包。
4. 复制 `config.json` 和 `data/`，或继续使用 `USAGE_DATA_DIR` / `USAGE_DB_PATH`。
5. 启动新二进制。

systemd 示例：

```bash
sudo systemctl stop cpa-manager-plus
sudo cp -a /var/lib/cpa-manager-plus /var/lib/cpa-manager-plus.backup.$(date +%Y%m%d%H%M%S)
sudo cp -a cpa-manager-plus_vX.Y.Z_linux_amd64/* /opt/cpa-manager-plus/
sudo systemctl start cpa-manager-plus
```

升级不会要求手动迁移 SQLite。程序启动时会自动执行兼容迁移。

## 验证

```bash
curl http://127.0.0.1:18317/health
curl http://127.0.0.1:18317/usage-service/info
curl -H "Authorization: Bearer <CPAMP_ADMIN_KEY>" \
  http://127.0.0.1:18317/status
```

检查 `configured`、`collector.lastError`、`lastConsumedAt`、`lastInsertedAt` 和 `eventCount`。

如果监控页面为空，继续按 [请求监控排障](../troubleshooting/request-monitoring.md) 检查。
