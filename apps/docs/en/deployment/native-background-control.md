# Native Background Control

Native release packages include small control scripts for running CPA Manager Plus in the background without keeping a terminal open.

Use the script from the extracted native package directory.

## Commands

macOS and Linux:

```bash
./cpa-manager-plusctl start
./cpa-manager-plusctl status
./cpa-manager-plusctl logs
./cpa-manager-plusctl logs -f
./cpa-manager-plusctl restart
./cpa-manager-plusctl stop
```

Windows PowerShell:

```powershell
.\cpa-manager-plusctl.ps1 start
.\cpa-manager-plusctl.ps1 status
.\cpa-manager-plusctl.ps1 logs
.\cpa-manager-plusctl.ps1 logs -f
.\cpa-manager-plusctl.ps1 restart
.\cpa-manager-plusctl.ps1 stop
```

`logs [lines]` accepts only a positive integer line count. `logs -f` and `logs --follow` follow the current log stream.

## Runtime Files

By default, the scripts write runtime state inside the extracted package directory:

| Platform | PID record | Logs |
|---|---|---|
| macOS/Linux | `run/cpa-manager-plus.pid` | `logs/cpa-manager-plus.log` |
| Windows | `run\cpa-manager-plus.pid` | `logs\cpa-manager-plus.log`, `logs\cpa-manager-plus.err.log` |

The PID record stores process metadata, not just a raw PID. `status`, `stop`, and `restart` refuse to trust an unverifiable or stale PID record.

## Environment Overrides

The scripts support these environment variables:

| Variable | Purpose |
|---|---|
| `CPA_MANAGER_PLUS_BIN` | Override the binary path. |
| `CPA_MANAGER_PLUS_RUN_DIR` | Override the default runtime directory. |
| `CPA_MANAGER_PLUS_LOG_DIR` | Override the default log directory. |
| `CPA_MANAGER_PLUS_PID_FILE` | Override the PID record path. |
| `CPA_MANAGER_PLUS_LOG_FILE` | Override the stdout log path. |
| `CPA_MANAGER_PLUS_ERR_LOG_FILE` | Override the stderr log path on Windows. |

Example:

```bash
CPA_MANAGER_PLUS_RUN_DIR=/var/lib/cpa-manager-plus/run \
CPA_MANAGER_PLUS_LOG_DIR=/var/log/cpa-manager-plus \
./cpa-manager-plusctl start
```

PowerShell:

```powershell
$env:CPA_MANAGER_PLUS_RUN_DIR = 'C:\cpamp\run'
$env:CPA_MANAGER_PLUS_LOG_DIR = 'C:\cpamp\logs'
.\cpa-manager-plusctl.ps1 start
```

## Security Notes

The Manager Server can print the generated first-start admin key to stdout. In background mode, stdout and stderr are written to log files, so treat the log directory as sensitive.

The default `run/` and `logs/` directories are private. On macOS/Linux, default directories are set to `0700` and runtime files to `0600`. On Windows, default runtime directories and files receive a protected ACL for the current user.

When using custom PID or log file paths, keep the parent directories private and user-controlled. The scripts reject symlinked or reparse-point runtime files and reject existing custom parent directories that are writable by broad local identities such as group/world on Unix or Everyone, Authenticated Users, or Users on Windows.

## Windows Argument Notes

Simple `start [args...]` forwarding is supported. Complex Windows arguments containing spaces or quotes can be shell-dependent because PowerShell forwards them through `Start-Process -ArgumentList`.

Prefer environment variables for runtime configuration on Windows.

## Troubleshooting

`status` reports a stale PID file:
The recorded process is gone. Run `stop` once to remove the stale PID record.

`status` or `stop` reports an unverifiable PID record:
The PID file points at a running process that does not match the recorded process metadata. The script refuses to stop it. Inspect the PID file and process manually before deleting the PID file.

`start` fails:
Check the recent log output printed by the script. On Windows, check both stdout and stderr logs.

`logs` says the log file does not exist:
The server has not written logs yet, or the configured `CPA_MANAGER_PLUS_LOG_FILE` path is wrong.
