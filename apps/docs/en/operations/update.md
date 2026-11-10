# Upgrade CPA Manager Plus

## v2 Beta: Upgrade From 1.x, Not An In-Place Image Change

::: warning Exact baseline and stop conditions
The minimum direct upgrade version is **`v1.14.10`**; the current CLI accepts only that exact declaration, not every later 1.x release automatically. For `<= v1.14.9`, first upgrade using the original deployment method to `v1.14.10`, verify it, then stop it. Merely changing `--source-version` is not an upgrade. Target CPA requires at least **`v8.0.55`** (Embedded pins that version). Version differences, unknown/newer schemas, a busy source, evidence mismatches, or history differences require operator action; never edit SQLite markers to proceed.
:::

1. Download one v2 Prerelease's `docker-compose.v2-embedded.yml`, `embedded-release-pairing.json`, and checksum file. Retain all three `<tag>@sha256:<digest>` image references as a group; see [Beta Installation](../deployment/docker.md). `releases/latest` and the installer remain 1.x Stable.
2. Save the exact 1.x image/binary, Compose, environment, and volume mapping. Stop the old Manager; also stop the old CPA when migrating a managed CPA. Back up `usage.sqlite`, WAL/SHM, `data.key`, `usage-archives/` when present, configuration, and secrets; see [Backup](./backup.md). Prevent all other source writers.
3. Give v2 **separate new data volumes**. Keep the original 1.x volume unchanged and read-only. Source roots must not overlap target/backup roots; scratch snapshots must be outside source and target. Follow the preflight disk-space report.

### Docker: Offline Preflight, Copy-In, And Four-Domain Reconciliation

Run from the v2 release Compose directory. `LEGACY_MOUNT` is the **actual old volume name** (check `docker inspect`), not a new empty volume. For a bind-mounted source, set both `LEGACY_MOUNT` and `LEGACY_ROOT` to the same actual absolute host path so the container and rollback plan agree. Replace the example volume name.

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

Require exit `0` and `ready: true`, then execute these separately:

```bash
upgrade_cli run --legacy "$LEGACY_ROOT" \
  --source-version v1.14.10 --target /data --json
upgrade_cli reconcile --legacy "$LEGACY_ROOT" \
  --source-version v1.14.10 --target /data --json
```

Continue only after each step succeeds. `run` performs backup/copy-in, not the complete upgrade; its default backup is the target's `upgrade-backup/`. `reconcile` compares **`retained`, `pricing`, `coverage`, and `history`**; inapplicable domains must state why. Review permanently incomplete UTC days individually before repeating the same `reconcile` command with `--acknowledge YYYY-MM-DD` for each day. `--acknowledge-parity-gaps` accepts only reported parity days already lost by old versions, not other measured mismatches. Do not add acknowledgements blindly or interpret them as restoring lost history.

### Finish CPA Connection And Final Verification

**External path**: the CPA operator independently upgrades/configures CPA to at least `v8.0.55` and makes it reachable from the CLI container. Put the real Management Key in a private host file `upgrade-secrets/cpa-management-key`, not a command argument. Replace the example URL:

```bash
upgrade_cli external-connect --target /data \
  --cpa-base-url 'http://your-cpa-host:8317' \
  --management-key-file /upgrade-secrets/cpa-management-key --json
upgrade_cli verify --legacy "$LEGACY_ROOT" --target /data --json
upgrade_cli status --db-path /data/usage.sqlite --json
```

Normal startup requires proven connection, identity, model surface, reconciliation, and the persisted stage `complete`. If the version header is absent, `external-connect --cpa-version` only declares the actual CPA version; it does not turn v7 into v8. After rotating the key, use `--reprove` at the same URL to prove it again rather than repeatedly presenting rejected credentials.

**Managed path**: import the old CPA configuration/credentials and prove the Runtime destination; do not use `external-connect` against an internal address instead. Start only target Runtime and Ingress, keeping Manager stopped. Add read-only mounts for the old CPA directory and its real auth directory to the one-shot Manager container. The command template is (paths/source version must come from the old deployment, and plugin choice must be explicit):

```text
docker compose -f docker-compose.v2-embedded.yml up -d --no-deps cpamp-runtime cpamp-ingress
docker compose -f docker-compose.v2-embedded.yml run --rm --no-deps \
  -v <old-Manager-mount-source>:<original-LEGACY_ROOT>:ro \
  -v <old-CPA-absolute-directory>:/legacy-cpa:ro \
  -v <old-CPA-auth-absolute-directory>:/legacy-cpa-auth:ro \
  cpamp-manager upgrade cpa-import --legacy <original-LEGACY_ROOT> --target /data \
  --cpa-dir /legacy-cpa --cpa-auth-dir /legacy-cpa-auth \
  --cpa-version <actual-source-CPA-version> --plugins false --json
```

Replace every angle-bracket parameter before running. `--plugins false` explicitly disables plugins in this example; choose according to your intent. Stop on qualification/destination-proof refusals, retain the report, and do not manually copy credentials into Runtime. After success, **keep Runtime Supervisor and the destination CPA running and Ready; keep only the long-running Manager Server stopped**. The one-shot Manager CLI's `upgrade verify` must read live Runtime identity and active-artifact evidence, so do not stop Runtime first. Ingress, already started in the example, may remain running, but isolate public traffic at the host firewall or reverse proxy. Ingress liveness neither replaces live Runtime proof nor establishes product readiness.

In that state, run the `upgrade_cli verify` and `upgrade_cli status` commands above separately. Only after the persisted stage is confirmed as `complete`, run `docker compose -f docker-compose.v2-embedded.yml up -d` to start the full stack and restore public traffic. Retain original 1.x data and upgrade backups until full acceptance. Verify public origin, console browser login (the control base path chosen at installation, default `/console`), Client Key, history/coverage, and real model requests; `/health` cannot replace these checks.

### Exact Rollback To Original 1.x Data

Rollback never mounts v2 SQLite into the old image. Stop the **whole v2 stack**, retain v2 data for diagnosis, and prepare under the same Compose project and identical read-only source mount:

```bash
docker compose -f docker-compose.v2-embedded.yml stop
upgrade_cli rollback --prepare --legacy "$LEGACY_ROOT" --target /data \
  --volume "$LEGACY_MOUNT" --legacy-image seakee/cpa-manager-plus:v1.14.10
```

`--prepare` prints/saves a plan; it never controls Docker. Check every emitted precondition on the **Docker host**: the old volume/path is the copy-in source, v2 is stopped, and the rollback port is free. Default 1.x rollback port is **18318**, distinct from v2 public port 18317; checks inside a container cannot prove host port availability. Apply the printed Compose and invocation on the host, or restore the original 1.x Compose against the **original unchanged 1.x volume**, using the plan's exact image and port, and restore the original CPA configuration. Never use a floating tag or the v2 data copy.

After 1.x starts, **complete the Docker-host container-identity and mount inspection gate before running `rollback --verify`**. The CLI has no Docker socket and cannot prove that the container answering health checks uses the original volume. Locate the unique container on the plan's actual rollback port, then set `ROLLBACK_CONTAINER_ID` to its real full ID:

```bash
docker ps --no-trunc --filter publish=18318 --format '{{.ID}} {{.Image}} {{.Names}}'
export ROLLBACK_CONTAINER_ID='actual-full-container-id'
docker inspect --format '{{.Id}} {{.Config.Image}} {{.Image}}' "$ROLLBACK_CONTAINER_ID"
docker inspect --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Type}} {{.Name}} {{.Source}} -> {{.Destination}}{{println}}{{end}}{{end}}' "$ROLLBACK_CONTAINER_ID"
```

Confirm the container ID belongs to the old Manager just restarted. `.Config.Image` must equal the plan's exact image reference; `.Image` is its actual image ID. For a named volume, `/data`'s `.Name` must equal the original `LEGACY_MOUNT`; run `docker volume inspect "$LEGACY_MOUNT"` and compare its returned `Mountpoint` with that container's `/data` `.Source`. For a bind mount, `.Source` must equal the original absolute host path. Where container-side paths differ, manually establish that this is the same source mounted read-only during copy-in. Stop on any mismatch or unproven correspondence; retain the inspection record with the plan.

Only after that gate, run `--verify` with the plan's backup and port. Inside the one-shot CLI container above, the Docker host is not `127.0.0.1`; for the default port:

```bash
upgrade_cli rollback --verify --legacy "$LEGACY_ROOT" --target /data \
  --legacy-image seakee/cpa-manager-plus:v1.14.10 \
  --status-url http://host.docker.internal:18318/health
upgrade_cli status --db-path /data/usage.sqlite --json
```

For custom backup/port settings, retain the emitted `--backup` and use the actual port. `rolled_back` records only the CLI's successful health-identity, source-directory, backup-integrity, and retained-data checks; **it contains neither automatic Docker container/mount proof nor a manual-confirmation field**. Consider rollback complete only together with the operator's saved container/mount inspection record above. An ordinary HTTP request exiting `0` is insufficient too. If refused, do not recreate the source volume or delete upgrade records to proceed.

### Native And Common Diagnostics

linux/amd64 Native uses the matching package's `bin/cpa-manager-plus upgrade ...`. Stop the complete launcher/systemd stack, replace `upgrade_cli` above with that binary, use the original absolute 1.x data directory for `--legacy`, and the actual new `data/` for `--target` / `--target-dir`. Use a private host credential file for External. Native Manager defaults to internal `127.0.0.1:18319`; pass `--manager-addr 127.0.0.1:18319` to rollback (or the actual overridden address), and confirm the public Ingress is stopped too. Restore the old 1.x binary/service and original data directory, start on the plan's separate port, then run host-side `rollback --verify --legacy ... --target ... --status-url http://127.0.0.1:18318/health`; do not use container-only `host.docker.internal`.

| Report/exit code | Action |
| --- | --- |
| `0` | Current step succeeded; inspect its stage, not every step is complete |
| `1` | Blocked/refused/acknowledgement required: read the report and stop automation |
| `2` | Usage, environment, or retryable error: resolve the cause before retry |
| `upgrade_source_version_unsupported` / `upgrade_source_version_undeclared` | Check the actual release and exact `v1.14.10` declaration |
| `upgrade_source_busy` / `upgrade_store_locked` | Stop the actual source/target Manager; do not remove lock files to proceed |
| `upgrade_source_changed` | Source is changing: stop all writers and reassess source/backup |
| `upgrade_rollback_image_unpinned` / `upgrade_rollback_volume_unbound` | Provide the exact old image and original volume mapping, not a new empty volume |

## 1.x Stable: Update Within The Original Deployment

The in-place pull/binary-replacement instructions below are for 1.x updates, not the v2 copy-in procedure.


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

## What Happens After Startup

- Manager Server applies compatible SQLite schema and metadata migrations automatically; do not run SQL manually.
- Large historical corrections may continue in the background after the HTTP server starts listening.
- Account-history or dashboard-hourly rollups may pause during migration. Related pages temporarily fall back to raw events and can be slower until catch-up completes.
- Do not start a second Manager Server against the same SQLite database or CPA queue to accelerate migration or rollup rebuilds.
- Use authenticated `GET /status` to inspect migration, collector, event, and `databaseMaintenance` state. The global warning, System Info, and Request Monitoring explain when offline maintenance is required.

### If Database Maintenance Is Degraded

Manager Server binds HTTP before running work whose cost grows with historical data. Missing indexes on populated tables and some legacy-derived cleanup are therefore not executed without a bound during startup. This does not mean data is missing, but historical request queries can become noticeably slower or time out until the query indexes are prepared.

Docker Compose:

```bash
docker compose stop cpa-manager-plus

docker compose run --rm --no-deps \
  cpa-manager-plus \
  cleanup-derived --db-path /data/usage.sqlite

docker compose start cpa-manager-plus
```

Native installation:

```bash
cpa-manager-plus cleanup-derived
# Non-default path
cpa-manager-plus cleanup-derived --db-path /path/to/usage.sqlite
```

Stop Manager Server first because the offline command requires the exclusive SQLite process lock. The web UI does not run cleanup automatically, spawn a cleanup subprocess, or create large-table indexes online. The global warning reads bounded metadata through `/status?scope=database-maintenance`, so maintenance polling does not scan or count `usage_events`. After the command completes and Manager Server restarts, `/status.databaseMaintenance` recovers from the real metadata automatically. `cleanup-derived` never deletes, rebuilds, or rewrites authoritative `usage_events`.

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
- `/status.databaseMaintenance.required` is `false`. If it is `true`, inspect the deferred-index and offline-job counts and follow the offline steps above.
- Reverse-proxied `/management.html`, `/usage-service/*`, and management API paths still route to the correct service.

## Rollback Principles

- Docker: restore the previous image tag and recreate the container while mounting the pre-upgrade data backup.
- Native: stop the new version and restore the old binary, configuration, and pre-upgrade data backup.
- Single-file panel: restore the previous `management.html`.
- Do not assume an older binary can read a database migrated by a newer version. For schema or data-semantics changes, restore the pre-upgrade SQLite, WAL/SHM, and `data.key` together.
- If the cause is unclear, preserve both versions' logs and database copies. Do not repeatedly start different versions against the same live database.
