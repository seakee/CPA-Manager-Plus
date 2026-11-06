# Backup And Restore

CPAMP keeps request history, configuration, and encrypted credentials on the host. The common mistake is backing up only `usage.sqlite` and missing WAL/SHM files, `data.key`, or secret files in the install directory.

## Required Backup Files

Back up these files as a set:

- `usage.sqlite`
- `usage.sqlite-wal`
- `usage.sqlite-shm`
- `data.key`
- `usage-archives/` when present, including raw request archives and manifests; SQLite alone is insufficient
- `usage.sqlite-journal` when present

If your deployment directory contains custom configuration files, back them up too. With the one-click installer, also back up `secrets/` and `data/` under the install directory; after a successful import, `secrets/cpa-management-key` is normally gone, but it may remain after a failed upgrade or with `CPAMP_SKIP_EXECUTE=1` for retry. Manual env/secret deployments should back up their matching secret files.

## Why data.key Is Required

CPA connections saved through setup or the panel encrypt the CPA Management Key with `data.key` before saving it to SQLite.

- If only `usage.sqlite` leaks, an attacker cannot directly read the CPA Management Key.
- If both `usage.sqlite` and `data.key` leak, the CPA Management Key can be decrypted.
- If `data.key` is lost, the saved CPA Management Key cannot be recovered. You must save the CPA connection configuration again.

If a CPA connection is managed by manual environment variables or secret files, the CPA Management Key may not be written to SQLite. Back up the related secret files together with the data directory. The installer's env input is migrated into SQLite after success, so do not rely on the one-time input file alone.

## v2 Beta: Embedded Stack Backup And Restore

v2 Beta's offline upgrade source is **`v1.14.10`** and target CPA requires at least **`v8.0.55`**. A backup does not authorize migration: follow [Upgrade Preflight And Reconciliation](./update.md). The complete Manager set includes `usage.sqlite`, WAL/SHM when present, `data.key`, `usage-archives/` when present, and custom configuration/secrets. Resetting Admin cannot repair a missing `data.key`.

Embedded also requires the entire Runtime volume: actual paths are `/runtime/gateway/config.yaml`, `/runtime/gateway/auth/` (singular `auth`), `/runtime/gateway/plugins/` when present, and `/runtime/supervisor/` (including `operations.sqlite`, WAL/SHM, and artifacts/active state). CPA configuration alone is insufficient. Retain the transport secret volume at `/run/cpamp/runtime-secret`, matching Compose/pairing record, public origin, and environment configuration. Encrypt backups and restrict access.

Run in the release Compose directory with the same project. The host backup directory is an example; use a new empty directory each time:

```bash
umask 077
mkdir backup
docker compose -f docker-compose.v2-embedded.yml stop
docker compose -f docker-compose.v2-embedded.yml run --rm --no-deps \
  -v "$PWD/backup":/backup --entrypoint tar cpamp-manager \
  -czpf /backup/manager.tgz -C /data .
docker compose -f docker-compose.v2-embedded.yml run --rm --no-deps \
  -v "$PWD/backup":/backup --entrypoint tar cpamp-runtime \
  -czpf /backup/runtime.tgz -C /runtime .
docker compose -f docker-compose.v2-embedded.yml run --rm --no-deps \
  -v "$PWD/backup":/backup --entrypoint tar cpamp-runtime \
  -czpf /backup/runtime-secret.tgz -C /run/cpamp/runtime-secret .
```

Only after all three archive commands succeed, run `docker compose -f docker-compose.v2-embedded.yml up -d`; on failure, leave services stopped and resolve the error. The commands reuse Digest-pinned service images rather than a floating utility image. No other Manager/CPA process may write these volumes while offline.

For restore, stop services, use the backup's Compose/pairing record and project configuration, and prepare **separate empty volumes/directories**. Never overlay an archive onto old WAL, archives, or Runtime state:

```bash
docker compose -f docker-compose.v2-embedded.yml run --rm --no-deps \
  -v "$PWD/backup":/backup:ro --entrypoint tar cpamp-manager \
  -xzpf /backup/manager.tgz -C /data
docker compose -f docker-compose.v2-embedded.yml run --rm --no-deps \
  -v "$PWD/backup":/backup:ro --entrypoint tar cpamp-runtime \
  -xzpf /backup/runtime.tgz -C /runtime
docker compose -f docker-compose.v2-embedded.yml run --rm --no-deps \
  -v "$PWD/backup":/backup:ro --entrypoint tar cpamp-runtime \
  -xzpf /backup/runtime-secret.tgz -C /run/cpamp/runtime-secret
```

Run `up -d` only after all succeed. Preserve archived owners, permissions, and isolation; do not recursively change all data to the CPA identity. Verify Admin login, CPA connection, Client Key, history/coverage, and Gateway; `/health` alone is insufficient. A 1.x rollback uses the original pre-migration 1.x volume, never these v2 archives.

### Scope Of Offline Manager Snapshots

With Manager stopped, `manager-data-snapshot` snapshots the database, WAL/SHM, rollback journal, and `data.key`, with an integrity manifest. It **does not include `usage-archives/`, custom configuration, Runtime, or external CPA**, so it does not replace a full backup. From the v2 native package root (the snapshot directory must not already exist):

```bash
sudo ./bin/cpa-manager-plus manager-data-snapshot create \
  --db-path "$PWD/data/usage.sqlite" --data-key-path "$PWD/data/data.key" \
  --snapshot-dir "$PWD/backup/manager-snapshot"
```

Only when restoring:

```bash
sudo ./bin/cpa-manager-plus manager-data-snapshot restore \
  --db-path "$PWD/data/usage.sqlite" --data-key-path "$PWD/data/data.key" \
  --snapshot-dir "$PWD/backup/manager-snapshot"
```

A held lock refuses the command: stop the actual Manager. Docker can use the same Compose's `run --rm --no-deps cpamp-manager manager-data-snapshot ...`, naming `/data/usage.sqlite`, `/data/data.key`, and a mounted persistent snapshot directory. A full Native backup/restore also includes the same backup's `data/runtime/` and secrets in `run/`, preserving ownership/permissions.

For External, the operator separately backs up/restores CPA configuration, OAuth credentials, and process state. Manager snapshots do not back up the remote host. Configuration-only export is not a complete v2 upgrade path.

## 1.x Stable: Backup Examples


## Docker Backup Example

If you use a named volume, stop the container first, then export through a temporary container:

```bash
docker stop cpa-manager-plus
docker run --rm \
  -v cpa-manager-plus-data:/data:ro \
  -v "$PWD":/backup \
  alpine \
  tar czf /backup/cpa-manager-plus-data.tgz -C /data .
docker start cpa-manager-plus
```

If you use a host directory mount:

```bash
docker stop cpa-manager-plus
cp -a /srv/cpa-manager-plus-data /srv/cpa-manager-plus-data.backup
docker start cpa-manager-plus
```

## Native Package Backup

Stop the process, then copy the data directory:

```bash
cp -a ./data ./data.backup
```

Windows PowerShell:

```powershell
Copy-Item -Recurse .\data .\data.backup
```

## Restore

1. Stop CPAMP.
2. Restore the full data directory.
3. Confirm that `usage.sqlite` and `data.key` come from the same backup.
4. If the CPA connection is env/secret-managed, also restore `secrets/` from the install directory.
5. Start CPAMP.
6. Log in and check configuration, monitoring data, and collector status.

If restore produces decryption errors, first check whether `data.key` matches the SQLite database.

## Move Manager Configuration Without Request History

If the old `usage.sqlite` is large and request history is no longer needed, start the replacement instance with an empty data directory and use the existing Manager configuration API to move the non-sensitive CPA URL, collector, Codex inspection, and External Usage Service settings. This does not copy `usage_events`, rollups, inspection run history, model prices, API Key aliases, or account-processing policy, and it does not export the CPA Management Key.

Export while the old instance is still reachable:

```bash
export OLD_CPAMP_URL='http://old-host:18317'
export OLD_CPAMP_ADMIN_KEY='cpamp_...'

curl -fsS \
  -H "Authorization: Bearer ${OLD_CPAMP_ADMIN_KEY}" \
  "${OLD_CPAMP_URL}/usage-service/config" \
  | jq '{config: .config}' \
  > manager-config.json
chmod 600 manager-config.json
```

The new `manager-config.json` does not contain the CPA Management Key; still treat the configuration file as sensitive and do not commit or attach it to an issue. An export from an older version may contain the plaintext key, so handle it as a secret and delete it after migration.

Stop the old instance and prepare an empty data directory for the replacement. While Manager Server is not running, provide the CPA Management Key with the offline command:

```bash
cpa-manager-plus store-cpa-connection \
  --cpa-base-url 'http://cpa:8317' \
  --management-key-file '/secure/cpa-management-key' \
  --db-path './data/usage.sqlite' \
  --data-key-path './data/data.key'
```

Stop Manager Server before running this command. It encrypts the key into SQLite and never echoes it.

Connection records follow these authority rules: a complete `manager_config_v1` is authoritative; if it coexists with stale or conflicting legacy `setup` data, startup and import keep the manager connection and canonicalize setup without repair. If manager data is partial and legacy setup is complete and compatible with its existing fields, setup completes manager. The command above refuses the write and explains the repair path only when no complete authority exists and partial records conflict, or when the persisted state cannot be resolved. After confirming this explicit connection is correct, append `--repair-conflict` to repair explicitly:

```bash
cpa-manager-plus store-cpa-connection \
  --repair-conflict \
  --cpa-base-url 'http://cpa:8317' \
  --management-key-file '/secure/cpa-management-key' \
  --db-path './data/usage.sqlite' \
  --data-key-path './data/data.key'
```

`--repair-conflict` exists only for persisted state the resolver cannot trust: `manager_config_v1`/`setup` rows that conflict with each other, or authority-less partial rows that conflict with the request. It writes the connection you explicitly provide into both `manager_config_v1` and the legacy `setup` mirror in a single transaction (the key stays encrypted at rest) while preserving collector settings and other data. A complete and consistent stored connection still requires exactly matching input; repair never rebinds silently. After repairing, start normally and the connection-storage migration completes as usual. Start the new instance, then import the remaining configuration:

```bash
export NEW_CPAMP_URL='http://new-host:18317'
export NEW_CPAMP_ADMIN_KEY='cpamp_...'

curl -fsS \
  -X PUT \
  -H "Authorization: Bearer ${NEW_CPAMP_ADMIN_KEY}" \
  -H 'Content-Type: application/json' \
  --data-binary @manager-config.json \
  "${NEW_CPAMP_URL}/usage-service/config"
```

The import validates the CPA Management API. After it succeeds, verify collector status and the related settings, then securely delete the exported file and temporary key file.

If the old connection is managed through environment variables or secret files, the API reports `source` as `env` and an API import cannot override the connection fields. Use the offline command above to write the CPA connection into the new SQLite database, or enter it again during setup. Administrator credentials are also outside the Manager configuration export; the new instance uses its newly generated or explicitly configured `CPA_MANAGER_ADMIN_KEY`.
