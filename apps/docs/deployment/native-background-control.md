# 原生包后台运行控制

原生发布包内置控制脚本，可以让 CPA Manager Plus 在后台运行，不需要一直占用终端。

请在解压后的原生包目录中使用脚本。

## 命令

macOS 和 Linux：

```bash
./cpa-manager-plusctl start
./cpa-manager-plusctl status
./cpa-manager-plusctl logs
./cpa-manager-plusctl logs -f
./cpa-manager-plusctl restart
./cpa-manager-plusctl stop
```

Windows PowerShell：

```powershell
.\cpa-manager-plusctl.ps1 start
.\cpa-manager-plusctl.ps1 status
.\cpa-manager-plusctl.ps1 logs
.\cpa-manager-plusctl.ps1 logs -f
.\cpa-manager-plusctl.ps1 restart
.\cpa-manager-plusctl.ps1 stop
```

`logs [lines]` 只接受正整数行数。`logs -f` 和 `logs --follow` 会持续跟随当前日志输出。

## 运行文件

默认情况下，脚本会在解压后的包目录中写入运行状态：

| 平台 | PID 记录 | 日志 |
|---|---|---|
| macOS/Linux | `run/cpa-manager-plus.pid` | `logs/cpa-manager-plus.log` |
| Windows | `run\cpa-manager-plus.pid` | `logs\cpa-manager-plus.log`、`logs\cpa-manager-plus.err.log` |

PID 文件记录的是进程元数据，不只是裸 PID。`status`、`stop` 和 `restart` 不会信任无法校验或已经过期的 PID 记录。

## 环境变量覆盖

脚本支持这些环境变量：

| 变量 | 用途 |
|---|---|
| `CPA_MANAGER_PLUS_BIN` | 覆盖二进制路径。 |
| `CPA_MANAGER_PLUS_RUN_DIR` | 覆盖默认运行目录。 |
| `CPA_MANAGER_PLUS_LOG_DIR` | 覆盖默认日志目录。 |
| `CPA_MANAGER_PLUS_PID_FILE` | 覆盖 PID 记录路径。 |
| `CPA_MANAGER_PLUS_LOG_FILE` | 覆盖 stdout 日志路径。 |
| `CPA_MANAGER_PLUS_ERR_LOG_FILE` | 覆盖 Windows stderr 日志路径。 |

示例：

```bash
CPA_MANAGER_PLUS_RUN_DIR=/var/lib/cpa-manager-plus/run \
CPA_MANAGER_PLUS_LOG_DIR=/var/log/cpa-manager-plus \
./cpa-manager-plusctl start
```

PowerShell：

```powershell
$env:CPA_MANAGER_PLUS_RUN_DIR = 'C:\cpamp\run'
$env:CPA_MANAGER_PLUS_LOG_DIR = 'C:\cpamp\logs'
.\cpa-manager-plusctl.ps1 start
```

## 安全说明

Manager Server 首次启动时可能会把生成的管理员密钥打印到 stdout。后台运行模式会把 stdout 和 stderr 写入日志文件，因此日志目录应当视为敏感数据目录。

默认 `run/` 和 `logs/` 目录是私有的。macOS/Linux 上，默认目录权限会设置为 `0700`，运行文件权限为 `0600`。Windows 上，默认运行目录和文件会设置为仅当前用户可访问的受保护 ACL。

如果使用自定义 PID 或日志文件路径，父目录必须是私有、由当前用户控制的目录。脚本会拒绝 symlink/reparse-point 运行文件，也会拒绝已经存在且可被宽泛本地身份写入的自定义父目录，例如 Unix 的 group/world-writable 目录，或 Windows 上 Everyone、Authenticated Users、Users 可写的目录。

## Windows 参数说明

脚本支持简单的 `start [args...]` 参数转发。包含空格或引号的复杂 Windows 参数会经过 `Start-Process -ArgumentList`，具体解析行为可能受 shell 影响。

Windows 上更建议通过环境变量配置运行参数。

## 排障

`status` 提示 stale PID file：
记录中的进程已经不存在。执行一次 `stop` 可以清理过期 PID 记录。

`status` 或 `stop` 提示 PID record 无法校验：
PID 文件指向了一个仍在运行、但和记录元数据不匹配的进程。脚本会拒绝停止它。请手动检查 PID 文件和对应进程后，再决定是否删除 PID 文件。

`start` 失败：
查看脚本打印的最近日志。Windows 上需要同时查看 stdout 和 stderr 日志。

`logs` 提示日志文件不存在：
服务可能还没有写入日志，或者 `CPA_MANAGER_PLUS_LOG_FILE` 配置路径不正确。
