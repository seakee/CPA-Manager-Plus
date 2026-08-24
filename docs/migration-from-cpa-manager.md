# Migration from CPA-Manager to CPA Manager Plus

This guide is for users migrating from the old `seakee/cpa-manager` / CPA-Manager project to `seakee/cpa-manager-plus`. The goal is to preserve historical usage events, model prices, API key aliases, and saved CPA connection settings.

## What Changed

- Docker image: `seakee/cpa-manager` -> `seakee/cpa-manager-plus`.
- Native binary/package: `cpa-manager` -> `cpa-manager-plus`.
- Backend source layout: old `usage-service` -> `apps/manager-server`. Compatible HTTP endpoints under `/usage-service/*` are still kept.
- Full Docker login now uses the Manager Server admin key `cpamp_...`, not the CPA Management Key.
- CPA Management Key is encrypted with `/data/data.key` before it is stored in SQLite.
- Legacy `settings.setup` is migrated to `settings.manager_config_v1` and kept as compatibility data.

## Before You Start

1. Check CPA version: `v7.1.0+` is recommended; HTTP usage queue support requires at least `v6.10.8+`.
2. Identify the old Manager Server data location:
   - Docker volume is commonly `cpa-manager-data`.
   - Host directory mounts usually map to container `/data`.
   - Native packages default to `data/usage.sqlite` next to the binary.
3. Stop the old container or process so the SQLite files stop changing.
4. Back up the full old data directory, not only a single database file. Keep at least:
   - `usage.sqlite`
   - Any current `usage.sqlite-wal` / `usage.sqlite-shm` files when WAL mode is active
5. Decide the admin-key strategy. During migration, prefer setting `CPA_MANAGER_ADMIN_KEY` or `CPA_MANAGER_ADMIN_KEY_FILE` explicitly instead of relying on the first startup log.

## Docker Volume Migration

Old compose files often looked like:

```yaml
services:
  cpa-manager:
    image: seakee/cpa-manager:latest
    volumes:
      - cpa-manager-data:/data

volumes:
  cpa-manager-data:
```

Mount the old volume into Plus:

```yaml
services:
  cpa-manager-plus:
    image: seakee/cpa-manager-plus:latest
    restart: unless-stopped
    ports:
      - '18317:18317'
    environment:
      HTTP_ADDR: '0.0.0.0:18317'
      USAGE_DB_PATH: '/data/usage.sqlite'
      # Advanced alternative: remove the previous line before setting a complete USAGE_DB_URL.
      # USAGE_DB_URL: "file:///data/usage.sqlite?_txlock=immediate&_pragma=journal_mode(DELETE)&_pragma=synchronous(EXTRA)&_pragma=busy_timeout(15000)&_pragma=foreign_keys(1)"
      CPA_MANAGER_DATA_KEY_PATH: '/data/data.key'
      CPA_MANAGER_ADMIN_KEY: 'replace-with-a-long-random-admin-key'
      USAGE_COLLECTOR_MODE: 'auto'
    volumes:
      - cpa-manager-data:/data

volumes:
  cpa-manager-data:
    external: true
```

The sample Plus compose creates `cpa-manager-plus-data` by default. If you use that new volume directly, the panel will look like a fresh installation and old data will not appear.

## Host Directory Migration

If the old container used a host directory:

```bash
docker run -d \
  --name cpa-manager \
  -p 18317:18317 \
  -v /srv/cpa-manager-data:/data \
  seakee/cpa-manager:latest
```

Migrate with:

```bash
docker stop cpa-manager
cp -a /srv/cpa-manager-data /srv/cpa-manager-data.backup

docker run -d \
  --name cpa-manager-plus \
  --restart unless-stopped \
  -p 18317:18317 \
  -v /srv/cpa-manager-data:/data \
  -e CPA_MANAGER_ADMIN_KEY='replace-with-a-long-random-admin-key' \
  seakee/cpa-manager-plus:latest
```

Then open `http://<host>:18317/management.html` and log in with the admin key.

## Native Package Migration

1. Stop the old `cpa-manager` process.
2. Back up the old program directory, especially `data/usage.sqlite*`.
3. Extract `cpa-manager-plus_<version>_<os>_<arch>`.
4. Copy the old `data` directory into the new package directory, or set `USAGE_DATA_DIR`, `USAGE_DB_PATH`, or `USAGE_DB_URL` to the old database. The URL and path cannot both be set.
5. Prefer setting the admin key on first startup:

```bash
CPA_MANAGER_ADMIN_KEY='replace-with-a-long-random-admin-key' ./cpa-manager-plus
```

If it is not set, the service generates a `cpamp_...` value and prints it only once in the first startup log.

## Validate the First Startup

1. Check startup logs:
   - If `CPA_MANAGER_ADMIN_KEY` was not set, save `CPA Manager Plus admin key generated: cpamp_...`.
   - Ensure there are no `decrypt secret`, `open sqlite`, or `bootstrap manager server` errors.
2. Open **Configuration -> CPA Manager Plus Configuration**.
3. Verify the bound CPA URL, request monitoring, collection mode, and polling interval.
4. Open the dashboard or monitoring page and confirm historical data is visible.
5. Request `/status` and check collector state, `lastConsumedAt`, `lastInsertedAt`, and `lastError`.
6. Back up the migrated `/data`; from now on it must include the new `data.key`.

## Data Key Backup Rules

Plus encrypts the CPA Management Key before storing it in SQLite. The default data key is `/data/data.key`.

- If only `usage.sqlite` leaks, the CPA Management Key is not directly readable.
- If both `usage.sqlite` and `data.key` leak, the CPA Management Key can be decrypted.
- If `data.key` is lost, encrypted CPA Management Key values cannot be recovered; save the CPA connection again.

Backups must include both SQLite files and `data.key`. Do not upload `data.key` in public troubleshooting material.

## Lost Admin Key Recovery

Admin-key reset instructions are maintained separately. Stop Manager Server, back up `/data`, and follow [Reset the Manager Server Admin Key](reset-admin-key.md).

## Rollback

Stop Plus before rollback. The old CPA-Manager can still read the main usage tables and legacy `settings.setup`, but it does not understand Plus admin credentials, bootstrap state, or the data key. Prefer rolling back to the backup taken before migration.

## FAQ

- Old data is missing: the Plus container is probably mounting a new empty `cpa-manager-plus-data` volume instead of the old `cpa-manager-data`.
- Login returns 401: Manager Server endpoints require the Manager Server admin key. The CPA Management Key only logs in to CPA panel mode.
- Monitoring is empty: enable CPA usage publishing, match `USAGE_COLLECTOR_MODE` to your network path, and make sure only one Manager Server consumes usage from the CPA instance.
- Decryption fails: confirm `/data/data.key` was not lost or replaced.
