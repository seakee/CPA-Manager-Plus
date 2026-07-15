# Windows one-click launcher

Windows native packages include `Start-CPA.cmd` and `Start-CPA.ps1`. The launcher starts CLIProxyAPI and CPA Manager Plus together, verifies that the expected executables own their ports, runs an authenticated GPT-5.5 completion, and waits for the resulting usage event to be inserted by Manager Plus.

## First-time setup

Save the CPA API key and the Manager Plus admin or external management key with Windows DPAPI:

```powershell
.\Start-CPA.ps1 -Configure
```

The encrypted values and launcher logs are stored under `%LOCALAPPDATA%\CPA Launcher`. Plaintext credentials are not written to the package directory or logs. Environment variables override the encrypted values:

- `CPA_API_KEY`
- `CPA_MANAGER_PLUS_ADMIN_KEY`

## Start both services

Double-click `Start-CPA.cmd`, or run:

```powershell
.\Start-CPA.ps1
```

Useful options:

```powershell
.\Start-CPA.ps1 -NoBrowser -NoPause
.\Start-CPA.ps1 -CPAPath C:\apps\cpa\cliproxyapi.exe
.\Start-CPA.ps1 -ManagerPath C:\apps\manager\cpa-manager-plus.exe
.\Start-CPA.ps1 -- --listen 127.0.0.1:18317
```

CLIProxyAPI may be named either `cliproxyapi.exe` or `cli-proxy-api.exe`. Explicit paths and the `CPA_CORE_BIN`, `CPA_MANAGER_PLUS_BIN`, and `CPA_CONFIG_PATH` environment variables take precedence over automatic discovery.

## Start Menu and desktop shortcuts

Install a current-user Start Menu shortcut that can be found with `Win+S`:

```powershell
.\Start-CPA.ps1 -InstallShortcuts
```

Also install a desktop shortcut:

```powershell
.\Start-CPA.ps1 -InstallShortcuts -DesktopShortcut
```

Remove both shortcut locations:

```powershell
.\Start-CPA.ps1 -UninstallShortcuts
```

Administrator privileges are not required.

## Readiness and safety

The launcher does not treat a process name, an open port, or an HTTP 200 response as sufficient readiness. It:

1. Acquires a cross-process launcher lock.
2. Resolves the expected executable paths without following directory reparse points.
3. Rejects ports owned by a different executable.
4. Uses the existing `cpa-manager-plusctl.ps1` PID metadata and log handling.
5. Waits for the CPA model endpoint and Manager Plus health endpoint.
6. Records Manager Plus event and insertion counters.
7. Sends an authenticated `gpt-5.5` chat completion containing `hi`.
8. Requires both the event count and collector insertion count to increase.
9. Fails if the collector reports an error or SQLite ingestion does not complete before the timeout.

The readiness check validates the running request and collection pipeline. It intentionally does not trust the model name echoed in a response as proof of the upstream model because response model fields may be rewritten by routing configuration.
