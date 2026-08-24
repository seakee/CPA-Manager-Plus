# Configuration And Data Directory

CPAMP stores its core data locally. During deployment, identify three things first: where SQLite lives, how `data.key` is stored, and where the admin key comes from.

## Key Files

| File               | Description                                                                           |
| ------------------ | ------------------------------------------------------------------------------------- |
| `usage.sqlite`     | SQLite database for request events, configuration, prices, aliases, and related data. |
| `usage.sqlite-wal` | May exist in WAL mode. Back it up when present.                                       |
| `usage.sqlite-shm` | May exist in WAL mode. Back it up when present.                                       |
| `data.key`         | Data key used to encrypt sensitive configuration written to SQLite.                   |

Docker defaults:

```text
/data/usage.sqlite
/data/data.key
```

Native package defaults:

```text
./data/usage.sqlite
./data/data.key
```

## SQLite Database Location

Use `USAGE_DATA_DIR` or `USAGE_DB_PATH` for normal deployments. When complete SQLite driver parameters are required, use the single `USAGE_DB_URL` setting instead; it is mutually exclusive with `USAGE_DB_PATH`. The equivalent `config.json` fields are `dbUrl` and `dbPath`, which are also mutually exclusive.

Database-location precedence:

```text
environment USAGE_DB_URL / USAGE_DB_PATH
> environment USAGE_DATA_DIR
> config.json dbUrl / dbPath
> default data/usage.sqlite
```

`USAGE_DB_URL` accepts only an absolute, local, persistent `file:` URI and requires explicit `_txlock=immediate`, foreign-key, journal-mode, synchronous, and positive busy-timeout settings. See [Manager Server Guide](./manager-server.md#advanced-sqlite-database-urls) for the complete syntax, examples, journal-mode transition rules, and network-filesystem limitations.

When a URL is used and `CPA_MANAGER_DATA_KEY_PATH` is not set explicitly, `data.key` defaults to the URL database directory. With every database-location method, back up the database, `data.key`, and every SQLite sidecar file that currently exists as one set.

## Admin Key

Full Docker and native Manager Server modes use a `cpamp_...` admin key for login.

Configure it with:

| Variable                     | Description                     |
| ---------------------------- | ------------------------------- |
| `CPA_MANAGER_ADMIN_KEY`      | Pass the admin key directly.    |
| `CPA_MANAGER_ADMIN_KEY_FILE` | Read the admin key from a file. |

If it is not configured, the first startup generates a random admin key and prints it to the logs. It will not be shown again.

## CPA Management Key

CPAMP uses the CPA Management Key to access the CPA management API.

Where it is stored depends on the configuration source:

- CPA connections saved through setup or the panel are encrypted with `data.key` and written to SQLite.
- CPA connections managed by the installer or environment variables come from `CPA_UPSTREAM_URL` and `CPA_MANAGEMENT_KEY` / `CPA_MANAGEMENT_KEY_FILE`. That connection is not written to SQLite; with the one-click installer, the key is usually in `secrets/cpa-management-key` under the install directory.

The CPAMP Lightweight Panel is hosted by CPA, and the browser holds the CPA Management Key, matching CPA-port access semantics.

## Collection Configuration

Recommended setting:

```text
USAGE_COLLECTOR_MODE=auto
```

Auto mode tries RESP Pub/Sub, HTTP queue, and RESP pop in order.

Constraints:

- RESP connections must connect directly to the CPA API port, usually `8317`.
- HTTP queue can go through an HTTP proxy.
- `pollIntervalMs` should not exceed the CPA usage queue retention.
- CPA retention defaults to 60s and is capped at 3600s.
- Only one Manager Server should consume the same CPA queue.
