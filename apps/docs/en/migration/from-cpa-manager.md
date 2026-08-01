# Migrate From CPA-Manager

Use this guide when migrating from the old `seakee/cpa-manager` / CPA-Manager project to `seakee/cpa-manager-plus`. The goal is to keep historical request statistics, model prices, API Key aliases, and saved CPA connection configuration.

If you never used the old `seakee/cpa-manager`, skip this page and use [Quick Start](../guide/getting-started.md).

## Key Changes

- The image name changed from `seakee/cpa-manager` to `seakee/cpa-manager-plus`.
- Native packages and binaries changed from `cpa-manager` to `cpa-manager-plus`.
- Full Docker login changed from CPA Management Key to the Manager Server admin key `cpamp_...`.
- The CPA Management Key is encrypted with `/data/data.key` before being saved to SQLite.
- Existing data receives the required compatibility migration during the first startup.

## Before Migration

1. Check the CPA version: `v7.1.0+` is recommended, and HTTP usage queue needs at least `v6.10.8+`.
2. Locate the old Manager Server data:
   - Docker volume is commonly `cpa-manager-data`.
   - Host directory mounts usually map to container `/data`.
   - Native packages default to `data/usage.sqlite` under the program directory.
3. Stop the old container or process so SQLite WAL files stop changing.
4. Back up the whole old data directory. Keep at least:
   - `usage.sqlite`
   - `usage.sqlite-wal`
   - `usage.sqlite-shm`
5. Decide the admin key strategy. During migration, explicitly setting `CPA_MANAGER_ADMIN_KEY` or `CPA_MANAGER_ADMIN_KEY_FILE` is recommended.

## Docker Volume Migration

A typical old Compose service looks like:

```yaml
services:
  cpa-manager:
    image: seakee/cpa-manager:latest
    volumes:
      - cpa-manager-data:/data

volumes:
  cpa-manager-data:
```

During migration, Plus can mount the old volume directly:

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
      CPA_MANAGER_DATA_KEY_PATH: '/data/data.key'
      CPA_MANAGER_ADMIN_KEY: 'replace-with-a-long-random-admin-key'
      USAGE_COLLECTOR_MODE: 'auto'
    volumes:
      - cpa-manager-data:/data

volumes:
  cpa-manager-data:
    external: true
```

Note: the Plus example Compose file creates `cpa-manager-plus-data` by default. If you use that new empty volume, the panel will look like a fresh install and will not show old data.

## Host Directory Migration

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

After startup, open `http://<host>:18317/management.html` and log in with the admin key.

## Native Package Migration

1. Stop the old `cpa-manager` process.
2. Back up the old program directory, especially `data/usage.sqlite*`.
3. Extract `cpa-manager-plus_<version>_<os>_<arch>`.
4. Copy the old `data` directory into the new package directory, or set `USAGE_DATA_DIR` / `USAGE_DB_PATH` to the old data directory.
5. Set an admin key for the first startup:

```bash
CPA_MANAGER_ADMIN_KEY='replace-with-a-long-random-admin-key' ./cpa-manager-plus
```

## Verify After First Startup

1. Check startup logs and confirm there are no `decrypt secret`, `open sqlite`, or `bootstrap manager server` errors.
2. Open the panel and go to the configuration page.
3. Check the CPA URL, request monitoring toggle, collection mode, and polling interval.
4. Open the dashboard or monitoring page and confirm historical data is visible.
5. Request `/status` and confirm collector status, `lastConsumedAt`, `lastInsertedAt`, and `lastError`.
6. Back up the migrated `/data`; it must now include the newly generated `data.key`.

## Rollback

Stop Plus before rollback. The old CPA-Manager can still read the main usage tables and old `settings.setup`, but it cannot understand the new admin credential, bootstrap state, or encrypted data key. Prefer rolling back to the pre-migration backup.

## FAQ

- Old data is missing after migration: usually the deployment mounted a new empty `cpa-manager-plus-data` volume instead of the old `cpa-manager-data`.
- Login always returns 401: Manager Server APIs need the admin key; CPA Management Key is only for logging in to the CPA control panel.
- Monitoring is empty: confirm CPA usage publishing is enabled, the collection mode matches the network path, and only one Manager Server consumes the usage queue for the same CPA instance.
- Decryption fails: confirm `/data/data.key` was not lost or replaced after migration.
