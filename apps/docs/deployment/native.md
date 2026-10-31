# 原生包部署

不想使用 Docker 时，可以直接运行 CPAMP 原生包。它适合已有进程管理、systemd、launchd 或 Windows 服务托管习惯的环境。

原生包模式仍然是 Manager Server 模式：二进制会托管 `/management.html`，本地保存 SQLite 数据，并使用 CPAMP 管理员密钥登录。它不是旧 CPA-Manager 的“给 CPA 端口面板外接 Usage Service”工作流。

如果只想安装 CPAMP 原生包，可以使用 [一键安装脚本](./installer.md)。脚本不会原生安装 CPA；完整新部署仍建议用 Docker。

## 最短安装流程

1. 确认 CPA 已经运行，并准备 CPA 地址和 CPA Management Key。
2. 从 GitHub Release 下载与你系统和架构匹配的原生包。
3. 解压后运行 `cpa-manager-plus`，或使用包内的后台控制脚本。
4. 打开 `http://<host>:18317/management.html`。
5. 使用日志中生成的 CPAMP 管理员密钥登录并连接 CPA。

需要长期运行时，再选择 systemd、launchd、Windows 服务或其他进程管理方式。

## Windows 桌面浮窗

Windows amd64/arm64 原生包同时包含 `cpa-manager-plus-monitor.exe`。它是独立的只读桌面客户端，不是第二个 Manager Server、采集器、代理或数据库读取器。Manager 需要另行保持运行；关闭浮窗不会停止 Manager 或 CPA。

```powershell
.\cpa-manager-plus-monitor.exe --url http://127.0.0.1:18317 --lang zh
```

![CPAMP Windows 桌面浮窗，展开](../../images/windows-floating-monitor.png)

![CPAMP Windows 桌面浮窗，收起](../../images/windows-floating-monitor-compact.png)

`--url` 是 **Manager** 地址，不是 CPA 网关，也不是 `management.html` 页面地址。反向代理前缀会保留，例如 `https://HOST/cpamp`；只接受本机 HTTP 或任意 HTTPS 地址。仅在远程 Manager 只提供明文 HTTP 时，勾选 **允许远程明文 HTTP** 才能连接；此时管理员密钥在传输中不加密。带账号密码、查询串、片段或含义不明路径的 URL 会被拒绝。`--lang zh` / `--lang en` 选择中文或英文。在原生密码输入框中填写 **CPAMP 管理员密钥**；没有密钥命令行参数、环境变量或 URL 参数。

### 显示与操作

- 拖动 CPAMP 标题区域移动窗口。置顶切换真实 Windows 置顶属性。收起/展开在小型浮条与完整账单视图之间切换。窗口按显示器 DPI 缩放，显示器变化后会把保存位置限制在最近工作区内。
- 展开：最近一条已记录请求的模型、完成/失败状态与时间；输入、输出、缓存命中、合计 Token；本次预估费用；本机当日请求数/Token/预估费用；输出速度、流式首字延迟、总耗时；两条更早请求。
- 窗口由程序自绘（圆角深色底、分段线、页脚条、蓝色强调），不是系统主题对话框。
- 收起：模型、今日 Token、输出速度、首字延迟、总耗时以及连接/更新时间。完整消耗和历史需展开查看。收起状态不是实时 Token 流进度。
- 隐藏：保留通知区域图标，同时暂停并取消网络请求；单击或右键图标恢复并立即刷新。关闭：退出浮窗并等待工作线程结束。修改连接时暂停轮询；取消保留原连接。「打开面板」用浏览器打开校验后的 Manager 地址加 `/management.html`，**不带密钥**。
- 原生按钮、静态文本和输入框支持键盘与辅助功能。自动化窗口类名 `CPAMPFloatingMonitorWindow`。按钮 ID：置顶 101、收起 102、隐藏 103、关闭 104、连接设置 105、打开面板 106。表单 ID：地址 201、密码 202、记住 203、连接 204、取消 205。指标 ID：状态 301、模型 302、请求 303、速度 304、首字 305、耗时 306、Token 307、本次费用 308、今日 309、历史 310/311、更新时间 312。使用标准 Win32 消息；生产代码中没有截图或测试后门。

### 统计口径与估算

请求在 UI 线程外串行轮询（每次完成后间隔三秒），隐藏、重连、授权失效或关闭时取消。单次 HTTP 超时 12 秒；一次完整轮询 20 秒截止；每个响应上限 2 MiB。拒绝重定向（含同源重定向）。网络、状态码或 JSON 错误不会回显响应正文或密钥。

今日统计来自 `/v0/management/dashboard/summary`，使用本机午夜与当前时刻。跨午夜或夏令时变化时今日值先重置为未知再刷新；最近一条请求不会因午夜消失。历史记录单独查询最近 30 个自然日，`events_page.limit=3`，关闭无关聚合。保留策略或归档删除可能让历史不完整；服务端报告不完整时界面会标注。

输出速度遵循 CPAMP 监控口径：**输出 Token ÷ 完整请求耗时**，包含首字等待。它是整段请求的平均输出速率，不是纯生成阶段速率；有意不同于设计稿中减去 TTFT 的算法。首字延迟只在确认流式且有正数测量时显示；非流式、零、非法或缺失值显示未知。耗时和 Token 的真实零值保留为 0；缺失/非法显示 `—`。缓存命中已包含在标准化输入中，不会重复加到合计。

本次费用复用 Manager 的 `internal/service/pricing` 与 model-prices 接口，包含模型解析、上下文档位、缓存桶和 service tier。缺少价格或费用时显示未知，不会虚构免费；显式零价保持为零。今日费用直接取 dashboard 估算，不由最近三条请求重算。临时错误保留同一连接的最后成功数据并显示断线/更新时间；重连会清空旧连接，授权失效会清空所有统计并删除已保存密钥。

### 本地设置与清理

`%LOCALAPPDATA%\CPA-Manager-Plus\monitor\preferences.json` 只保存 Manager 地址、位置、收起和置顶。记住密钥默认关闭；勾选后另存 `key.dpapi`，内容为当前用户 DPAPI 密文并绑定标准化 Manager 地址。目录和文件使用受保护的当前用户/SYSTEM DACL，而不是依赖 Unix 权限位。更换地址不会复用其他 Manager 的密钥。取消勾选并连接即可删除已保存密文。解密失败或文件损坏时会要求重新输入，不会转发无效凭据。浮窗不持久化账号身份、请求正文、原始事件元数据或用量数据库。

关闭浮窗后，只删除 `%LOCALAPPDATA%\CPA-Manager-Plus\monitor` 即可重置设置和密钥；不要删除 Manager 的数据/配置目录。Go 字符串不是安全内存设施：已输入的密钥在认证期间必然存在于进程内存中。

### 构建与专项测试

在仓库根目录（Git Bash/Linux/macOS）构建浮窗不需要前端构建或 CGO：

```bash
cd apps/manager-server
go test ./internal/desktopmonitor ./internal/service/pricing
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags '-s -w -H windowsgui' -o cpa-manager-plus-monitor.exe ./cmd/cpa-manager-plus-monitor
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -trimpath -ldflags '-s -w -H windowsgui' -o cpa-manager-plus-monitor-arm64.exe ./cmd/cpa-manager-plus-monitor
```

可移植的 HTTP/数据模型测试可在其他系统运行。原生控件/生命周期、窗口设置、Windows DACL 和 DPAPI 测试在 Windows 上使用临时目录与受控 HTTP 夹具运行，不接触真实 Manager 数据。专项 CI 工作流会测试这些包并交叉构建两种架构。`bin/release/package-native.sh` 只向 Windows 压缩包加入该 GUI，其他平台不变。可用 `npx vitest run tests/nativeMonitorPackage.test.mjs` 校验打包约定。

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

查看当前二进制版本不会启动 Manager Server，也不需要读取配置或数据库：

```bash
./cpa-manager-plus --version
```

Windows PowerShell：

```powershell
.\cpa-manager-plus.exe --version
```

也可以使用简写参数 `-v`。

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
