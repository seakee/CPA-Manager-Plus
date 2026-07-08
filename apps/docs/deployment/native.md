# 原生包部署

不想使用 Docker 时，可以直接运行 CPAMP 原生包。它适合已有进程管理、systemd、launchd 或 Windows 服务托管习惯的环境。

原生包模式仍然是 Manager Server 模式：二进制会托管 `/management.html`，本地保存 SQLite 数据，并使用 CPAMP 管理员密钥登录。它不是旧 CPA-Manager 的“给 CPA 托管面板外接 Usage Service”工作流。

如果只想安装 CPAMP 原生包，可以使用 [一键安装脚本](./installer.md)。脚本不会原生安装 CPA；完整新部署仍建议用 Docker。

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

打开：

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

## 首次 setup

打开：

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
