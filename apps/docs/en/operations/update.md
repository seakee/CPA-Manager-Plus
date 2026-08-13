# Upgrade CPA Manager Plus

This guide explains how to upgrade CPAMP without losing SQLite data, `data.key`, or locally managed secrets. Follow the section for the way CPAMP is currently deployed; do not overwrite an existing deployment with first-install commands.

## Before You Upgrade

1. Read the target [release notes](../reference/releases.md) and the GitHub Release Upgrade Notes.
2. Record the current image tag or native version, launch command, environment variables, and data directory.
3. Stop extra instances that could write to the same SQLite database. Only one Manager Server may consume a CPA usage queue.
4. Back up:
   - `usage.sqlite`, `usage.sqlite-wal`, and `usage.sqlite-shm`.
   - `data.key`.
   - Installer-managed `secrets/`, `.env`, `compose.yaml`, or `config.json`.
   - Custom reverse-proxy, systemd, launchd, or Windows service configuration.

See [Backup And Restore](./backup.md) for safe procedures. A CPA Management Key encrypted in SQLite cannot be recovered if `data.key` is lost.

## In-Panel Backup And Update

Starting with a compatible native release, a fixed-directory installation run by the bundled control script can expose “Backup & update” on the Dashboard. The capability is disabled by default. Upgrade manually to the first compatible release, then run once from the native package directory:

```bash
./cpa-manager-plusctl enable-updates
./cpa-manager-plusctl restart
```

Windows PowerShell:

```powershell
.\cpa-manager-plusctl.ps1 enable-updates
.\cpa-manager-plusctl.ps1 restart
```

Manager Server then accepts only stable Releases from the official GitHub repository. A release must contain the native archive, `checksums.txt`, `update-manifest.json`, and valid GitHub asset SHA-256 metadata. The operation has two phases:

1. “Prepare update” downloads, extracts, and verifies the target package while the service remains online. It does not modify active program files.
2. “Restart and update” launches an independent updater, stops the service cleanly, creates an offline backup of managed program files, `config.json`, the data directory, and `secrets/`, replaces managed files, then verifies `/health` and the target runtime version.

The backup manifest records every file's size and SHA-256. Startup or validation failure automatically restores the old program and data. If the host is interrupted during switching, the next start through the official control script recovers the incomplete transaction first.

The support boundary is intentionally strict:

- Supported: a fixed-directory native package, launched with the bundled control script, after explicit `enable-updates` enrollment.
- Unsupported: Docker, `docker run`, the installer's versioned runtime/systemd layout, custom systemd/launchd/Windows Services, custom supervisors, and the CPA-hosted lightweight panel on `:8317`.
- Managed updates require the control script's default launch and do not support extra `start [args...]` arguments. Put ports, data paths, and similar settings in `config.json` or environment variables. `enable-updates` refuses enrollment while an argument-based process is running.
- The data directory must be a dedicated child of the installation directory, with SQLite and `data.key` beneath it. The backup directory must not contain the data directory or be contained by it.

On Windows, `.update`, rollback snapshots, and their sensitive files receive protected NTFS ACLs granting full control only to the current user, SYSTEM, and local Administrators. Preparation or backup fails closed if a private ACL cannot be established; program replacement does not continue.

After a successful update, the transaction's downloaded archive and extracted staging directory are removed while transaction metadata remains. Preparing a later update retains only the two newest safely removable old transactions. Backup snapshots are not deleted automatically; manage `backups/` according to your retention and off-host backup policy.

Unsupported installations continue to receive normal Release notifications, but the managed update action remains hidden. Follow the manual section for that deployment type.

## What Happens After Startup

- Manager Server applies compatible SQLite schema and metadata migrations automatically; do not run SQL manually.
- Large historical corrections may continue in the background after the HTTP server starts listening.
- Account-history or dashboard-hourly rollups may pause during migration. Related pages temporarily fall back to raw events and can be slower until catch-up completes.
- Do not start a second Manager Server against the same SQLite database or CPA queue to accelerate migration or rollup rebuilds.
- Use authenticated `GET /status` to inspect migration, collector, and event state.

## Docker Deployment Created By The Installer

Do not rerun the installer. Enter the original installation directory and update the images:

```bash
cd "$HOME/cpa-manager-plus"
docker compose pull
docker compose up -d
docker compose ps
```

Replace the path if `CPAMP_INSTALL_DIR` selected another directory.

A full CPA + CPAMP installation pulls both images declared in Compose. To update only CPAMP:

```bash
docker compose pull cpa-manager-plus
docker compose up -d cpa-manager-plus
```

Do not rerun the installer with `CPAMP_OVERWRITE=1` as an upgrade shortcut. That option regenerates configuration and may overwrite maintained `.env`, `compose.yaml`, CPA configuration, or `run.sh` files.

## Manually Maintained Docker Compose

### Tracking `latest`

```bash
docker compose pull cpa-manager-plus
docker compose up -d cpa-manager-plus
docker compose logs --tail=100 cpa-manager-plus
```

### Pinned Version

Change the Compose image to the target version:

```yaml
services:
  cpa-manager-plus:
    image: seakee/cpa-manager-plus:vX.Y.Z
```

Then run:

```bash
docker compose pull cpa-manager-plus
docker compose up -d cpa-manager-plus
```

The corresponding GHCR image is also available:

```text
ghcr.io/seakee/cpa-manager-plus:vX.Y.Z
```

Confirm that the new container mounts the existing `/data` volume or host directory. Do not create a new empty volume for an upgrade.

## Manual `docker run`

Inspect the current ports, volumes, environment, network, and `--add-host` settings first:

```bash
docker inspect cpa-manager-plus
```

Pull the target image and recreate the container. This is a minimal example; retain every option used by the current deployment:

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

If CPA runs on a Linux host, retain:

```text
--add-host=host.docker.internal:host-gateway
```

Removing the old container does not remove a named volume, but removing the volume destroys the data.

## Native Deployment Created By The Installer

The installer normally creates:

```text
runtime/cpa-manager-plus_<version>_<os>_<arch>/
data/
secrets/
run.sh
cpa-manager-plus.service
```

Do not overwrite the running version directory:

1. Stop the current process or systemd service.
2. Back up `data/`, `secrets/`, the old runtime, `run.sh`, and the service file.
3. Download and extract the target release under `runtime/` as a new version directory.
4. Copy `config.json` from the old version directory into the new one. Installer-generated relative paths continue to reference the shared `data/` and `secrets/` directories.
5. Change the working directory and binary path in `run.sh` to the new version directory.
6. If the generated systemd unit is installed, update `WorkingDirectory` and `ExecStart`, then run `systemctl daemon-reload`.
7. Start and verify the new version before deciding whether to remove the old runtime.

Do not copy only `usage.sqlite` and omit WAL/SHM. Back up while the process is stopped or use a safe SQLite method from [Backup And Restore](./backup.md).

## Manual Native Packages

### macOS Or Linux, Foreground Or Control Script

1. Run `./cpa-manager-plusctl stop`, or stop your process supervisor.
2. Back up the data directory and `data.key`.
3. Extract the new package into a new version directory.
4. Continue using the external `USAGE_DATA_DIR` / `USAGE_DB_PATH`, or copy `config.json` and `data/` while the process is stopped.
5. Start with the control script from the new directory:

```bash
./cpa-manager-plusctl start
./cpa-manager-plusctl status
./cpa-manager-plusctl logs
```

Do not extract over a running directory; that can mix binary, control script, and embedded panel versions.

### Linux systemd With A Fixed Program Directory

If the service always launches `/opt/cpa-manager-plus/cpa-manager-plus`:

```bash
sudo systemctl stop cpa-manager-plus
sudo cp -a /var/lib/cpa-manager-plus "/var/lib/cpa-manager-plus.backup.$(date +%Y%m%d%H%M%S)"
sudo cp -a cpa-manager-plus_vX.Y.Z_linux_amd64/. /opt/cpa-manager-plus/
sudo systemctl start cpa-manager-plus
sudo systemctl status cpa-manager-plus
```

Keeping `/var/lib/cpa-manager-plus` outside the program directory prevents release files from overwriting runtime data.

### Windows

1. Stop CPAMP through its control script or service manager:

```powershell
.\cpa-manager-plusctl.ps1 stop
```

2. Back up `data`, `config.json`, and service configuration.
3. Extract the new ZIP into a new version directory.
4. Continue using the existing data directory, or copy configuration and data while stopped.
5. Start from the new directory and inspect logs:

```powershell
.\cpa-manager-plusctl.ps1 start
.\cpa-manager-plusctl.ps1 status
.\cpa-manager-plusctl.ps1 logs
```

Update the Windows service configuration if its executable path contains the version directory.

## CPAMP Lightweight Panel

The CPAMP Lightweight Panel is downloaded and hosted by CPA. Updating it changes only the browser frontend; it does not install or update Manager Server, the SQLite schema, or the collector.

Confirm that CPA points to this repository:

```yaml
remote-management:
  panel-github-repository: 'https://github.com/seakee/CPA-Manager-Plus'
```

CPA normally refreshes its cached panel automatically. If it still serves an old panel, remove the cached file from the CPA working directory and reload or restart CPA:

```bash
rm static/management.html
```

When `Disable Panel Auto Updates` is enabled, CPA downloads a panel only when the cached file is absent. Confirm that the file is CPA's panel cache, not Manager Server persistent data, before removing it.

## Custom `management.html` Or `PANEL_PATH`

For a manually deployed single-file panel:

1. Download `management.html` from the target release.
2. Verify it against the release checksum.
3. Keep the old file and atomically replace the static file or the file referenced by `PANEL_PATH`.
4. Clear reverse-proxy and browser caches.

Replacing only `management.html` does not update Manager Server APIs. Mixing a frontend and Manager Server across several releases can cause field or feature incompatibilities; use the panel and Manager Server from the same CPAMP release.

## Verify The Upgrade

Run:

```bash
curl -f http://127.0.0.1:18317/health
curl -f http://127.0.0.1:18317/usage-service/info
curl -f -H "Authorization: Bearer <CPAMP_ADMIN_KEY>" \
  http://127.0.0.1:18317/status
```

Confirm:

- The panel and server report the target version.
- `configured` has the expected value.
- `collector.lastError` is empty or understood.
- `lastConsumedAt`, `lastInsertedAt`, and `eventCount` update normally.
- Dashboard, Request Monitoring, and Usage Analytics load data.
- Background migration completes and rollup checkpoints continue advancing in `/status`.
- Reverse-proxied `/management.html`, `/usage-service/*`, and management API paths still route to the correct service.

## Rollback Principles

- Docker: restore the previous image tag and recreate the container while mounting the pre-upgrade data backup.
- Native: stop the new version and restore the old binary, configuration, and pre-upgrade data backup.
- Single-file panel: restore the previous `management.html`.
- Do not assume an older binary can read a database migrated by a newer version. For schema or data-semantics changes, restore the pre-upgrade SQLite, WAL/SHM, and `data.key` together.
- If the cause is unclear, preserve both versions' logs and database copies. Do not repeatedly start different versions against the same live database.
