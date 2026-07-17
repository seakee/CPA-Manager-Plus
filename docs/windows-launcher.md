# Windows Launcher

Windows native packages include `cpa-launcher.cmd` and `cpa-launcher.ps1`. The
launcher starts CLIProxyAPI and CPA Manager Plus as one installation without
assuming a fixed drive or installation directory.

## Start both services

Run from a terminal:

```powershell
.\cpa-launcher.cmd start -NoPause
```

The launcher searches for `cliproxyapi.exe` and `cli-proxy-api.exe` beside the
Manager package and in its immediate parent directory. Use an explicit path
when more than one installation exists:

```powershell
.\cpa-launcher.cmd start -CpaRoot "D:\Apps\CLIProxyAPI" -NoPause
```

The following environment variables are also supported:

| Variable | Purpose |
| --- | --- |
| `CPA_ROOT` | CLIProxyAPI installation directory |
| `CPA_CONFIG` | CLIProxyAPI `config.yaml` path |
| `CPA_LAUNCHER_API_KEY` | API key used only in memory for the completion readiness check |
| `CPA_LAUNCHER_RUN_DIR` | Launcher PID-state directory |
| `CPA_LAUNCHER_LOG_DIR` | CLIProxyAPI stdout/stderr log directory |

By default the launcher sends a real, authenticated `hi` completion using
`gpt-5.5` before it reports the installation as ready. It reads the first
`api-keys` entry from `config.yaml` when `CPA_LAUNCHER_API_KEY` is not set. The
key is never printed or written to launcher logs. Use
`-SkipCompletionCheck` only when the configured upstream cannot serve the
verification model.

Readiness is transactional across both services: the launcher records the
Manager collector and SQLite counters, sends the completion, and waits for
both the durable event count and collector insertion count to increase. A
collector error, an unrelated port owner, or a request that never reaches
SQLite fails startup instead of opening the browser.

This verifies authentication, request execution, upstream response handling,
and usage persistence. It does not treat the response `model` field as proof
of a native upstream model because CLIProxyAPI installations may configure
model aliases or response rewriting.

`-NoBrowser` suppresses opening the panel. `-NoPause` makes the launcher safe
for terminals and automation. Additional arguments after the launcher options
are forwarded to the Manager controller.

## Start Menu and desktop shortcuts

Install a current-user Start Menu shortcut named `CPA`:

```powershell
.\cpa-launcher.cmd install-shortcuts -NoPause
```

Add `-DesktopShortcut` to install a desktop shortcut as well. Remove both
shortcuts with:

```powershell
.\cpa-launcher.cmd uninstall-shortcuts -NoPause
```

The shortcut uses the launcher's actual installation path, so paths containing
spaces or non-ASCII characters are supported. No administrator privileges are
required.

## Process safety

The launcher does not treat a process name or an open port as proof that the
correct service is running. It records the PID, process start time, and binary
path, then verifies that the expected PID owns the configured listening port.
Stale records are removed, while conflicting records and unrelated port owners
cause a clear failure instead of killing or reusing another process.

Runtime state and logs are stored in private `run` and `logs` directories. The
launcher refuses reparse-point runtime paths to reduce link-swap attacks.

## Other commands

```powershell
.\cpa-launcher.cmd status -NoPause
.\cpa-launcher.cmd stop -NoPause
```

The `stop` command validates process identity before stopping CLIProxyAPI and
delegates Manager shutdown to `cpa-manager-plusctl.ps1`.
