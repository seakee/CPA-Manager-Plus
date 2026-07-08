# Native Packages

Use native packages when Docker is not part of your environment. They fit deployments already managed by systemd, launchd, Windows services, or another process supervisor.

Native mode is still Manager Server mode: the binary serves `/management.html`, stores SQLite data locally, and uses the CPAMP Admin Key for login. It is not the old "external Usage Service for the CPA-hosted panel" workflow.

If you only want to install the CPAMP native package, you can use [One-Click Installer](./installer.md). The script does not install CPA natively; use Docker for a full new deployment.

## Requirements

Before running it, prepare:

- CPA / CLI Proxy API running separately.
- CPA Management API enabled.
- A CPA Management Key.
- A persistent and backed-up CPAMP data directory.
- Exactly one CPAMP Manager Server consuming one CPA usage queue.

Recommended CPA version:

```text
v7.1.39+
```

Minimum for HTTP usage queue:

```text
v6.10.8+
```

## Download

Download the package for your platform from [GitHub Releases](https://github.com/seakee/CPA-Manager-Plus/releases/latest).

Common package names:

```text
cpa-manager-plus_<version>_linux_amd64.tar.gz
cpa-manager-plus_<version>_linux_arm64.tar.gz
cpa-manager-plus_<version>_darwin_amd64.tar.gz
cpa-manager-plus_<version>_darwin_arm64.tar.gz
cpa-manager-plus_<version>_windows_amd64.zip
cpa-manager-plus_<version>_windows_arm64.zip
```

Check Linux architecture:

```bash
uname -m
```

Mapping:

```text
x86_64  -> linux_amd64
aarch64 -> linux_arm64
arm64   -> linux_arm64
```

## Run Manually

macOS / Linux:

```bash
tar -xzf cpa-manager-plus_vX.Y.Z_linux_amd64.tar.gz
cd cpa-manager-plus_vX.Y.Z_linux_amd64
./cpa-manager-plus
```

Windows PowerShell:

```powershell
Expand-Archive .\cpa-manager-plus_vX.Y.Z_windows_amd64.zip -DestinationPath .
cd .\cpa-manager-plus_vX.Y.Z_windows_amd64
.\cpa-manager-plus.exe
```

Open:

```text
http://<host>:18317/management.html
```

If no admin key is configured, the process prints a generated `cpamp_...` key once. Save it immediately.

You can also set it explicitly.

macOS / Linux:

```bash
CPA_MANAGER_ADMIN_KEY='replace-with-a-long-random-admin-key' ./cpa-manager-plus
```

Windows PowerShell:

```powershell
$env:CPA_MANAGER_ADMIN_KEY = 'replace-with-a-long-random-admin-key'
.\cpa-manager-plus.exe
```

## Data Location

By default, native packages create:

```text
config.json
data/usage.sqlite
data/data.key
```

next to the binary.

Override with:

```bash
USAGE_DATA_DIR=/var/lib/cpa-manager-plus ./cpa-manager-plus
```

or:

```bash
USAGE_DB_PATH=/var/lib/cpa-manager-plus/usage.sqlite ./cpa-manager-plus
```

Back up:

```text
data/usage.sqlite
data/usage.sqlite-wal
data/usage.sqlite-shm
data/data.key
```

`data.key` decrypts the saved CPA Management Key. If it is lost, save the CPA connection again.

## Linux systemd Example

Install to a fixed directory:

```bash
sudo mkdir -p /opt/cpa-manager-plus /var/lib/cpa-manager-plus
sudo cp -a cpa-manager-plus_vX.Y.Z_linux_amd64/* /opt/cpa-manager-plus/
sudo useradd --system --no-create-home --shell /usr/sbin/nologin cpa-manager-plus
sudo chown -R cpa-manager-plus:cpa-manager-plus /opt/cpa-manager-plus /var/lib/cpa-manager-plus
```

Create `/etc/systemd/system/cpa-manager-plus.service`:

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
# Recommended: provide a stable secret through an environment file or secret manager.
# Environment=CPA_MANAGER_ADMIN_KEY=replace-with-a-long-random-admin-key

[Install]
WantedBy=multi-user.target
```

Start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now cpa-manager-plus
sudo systemctl status cpa-manager-plus
```

Logs:

```bash
journalctl -u cpa-manager-plus -f
```

## First Setup

Open:

```text
http://<host>:18317/management.html
```

Enter:

```text
Admin Key:          cpamp_... from logs, or your configured admin key
CPA URL:            http://127.0.0.1:8317, http://<cpa-host>:8317, or your CPA URL
CPA Management Key: CPA remote-management.secret-key
```

After setup:

- Browser login uses the CPAMP Admin Key.
- CPA Management Key is stored server-side and encrypted.
- New browsers no longer need the CPA Management Key.

## Running In The Background

Native packages include background control scripts for `start`, `status`, `logs`, `restart`, and `stop`. The scripts write PID records and log files, and protect the default runtime directories with private permissions. See [Native Background Control](./native-background-control.md).

For production, you can also run the process through systemd, launchd, Windows Service Manager, or another process manager. Whichever method you use, make sure the data directory is persistent and backed up.

## Upgrade

1. Stop the native process.
2. Back up the data directory, including `data.key`.
3. Extract the new package.
4. Copy over `config.json` and `data/`, or keep using `USAGE_DATA_DIR` / `USAGE_DB_PATH`.
5. Start the new binary.

systemd example:

```bash
sudo systemctl stop cpa-manager-plus
sudo cp -a /var/lib/cpa-manager-plus /var/lib/cpa-manager-plus.backup.$(date +%Y%m%d%H%M%S)
sudo cp -a cpa-manager-plus_vX.Y.Z_linux_amd64/* /opt/cpa-manager-plus/
sudo systemctl start cpa-manager-plus
```

Upgrades do not require manual SQLite migration. Compatible migrations run automatically at startup.

## Verification

```bash
curl http://127.0.0.1:18317/health
curl http://127.0.0.1:18317/usage-service/info
curl -H "Authorization: Bearer <CPAMP_ADMIN_KEY>" \
  http://127.0.0.1:18317/status
```

Check `configured`, `collector.lastError`, `lastConsumedAt`, `lastInsertedAt`, and `eventCount`.

If the monitoring page is empty, continue with [Request Monitoring Troubleshooting](../troubleshooting/request-monitoring.md).
