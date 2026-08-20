# Manager Server Guide

Manager Server is the backend for the full CPAMP experience. It hosts `management.html`, stores local SQLite data, consumes the CPA usage queue through the collector, and protects management capabilities with the CPAMP Admin Key.

Most users do not need to read this page from top to bottom. Start with the document that matches your task:

| Goal                                    | Recommended document                                               |
| --------------------------------------- | ------------------------------------------------------------------ |
| Install Full Mode for the first time    | [Quick Start](../guide/getting-started.md)                         |
| Change the CPA connection or Monitoring | [Configuration](../manual/configuration.md)                        |
| Monitoring has no data                  | [Monitoring Has No Data](../troubleshooting/request-monitoring.md) |
| Upgrade or back up                      | [Upgrade CPAMP](./update.md), [Backup And Restore](./backup.md)    |

This page is mainly for advanced deployments that need environment variables, custom collection networking, runtime endpoints, or data-directory control.

When you open this entry point, you are using Manager Server mode:

```text
http://<host>:18317/management.html
```

When CPA itself serves this entry point, you are using the CPAMP Lightweight Panel:

```text
http://<cpa-host>:8317/management.html
```

The CPAMP Lightweight Panel does not connect to or read Manager Server SQLite and does not provide full historical monitoring, model prices, API key aliases, import/export, or server inspection history.

## What Manager Server Does

Manager Server:

- Serves the embedded management panel.
- Runs first setup or reads an environment-managed CPA connection.
- Authenticates users with the `cpamp_...` admin key.
- Encrypts setup/panel-saved CPA Management Keys with `data.key`.
- Proxies CPA Management API calls after setup.
- Consumes CPA usage events.
- Persists usage events in SQLite.
- Provides Dashboard, Request Monitoring, Usage Analytics, Model Pricing, API Key Alias, Usage Import/Export, and Server Codex Inspection APIs.

::: details Advanced: architecture and data flow

## Architecture

```text
Browser
  -> Manager Server :18317
      -> /management.html
      -> /usage-service/info
      -> /usage-service/config
      -> /v0/management/usage              from SQLite
      -> /v0/management/model-prices       from SQLite
      -> /v0/management/api-key-aliases    from SQLite
      -> /v0/management/dashboard/*        from SQLite
      -> /v0/management/monitoring/*       from SQLite
      -> /v0/management/codex-inspection/* from SQLite / background workers
      -> other /v0/management/*            proxied to CPA
      -> collector -> CPA usage queue
      -> /data/usage.sqlite
```

CPA still runs separately. CPAMP does not bundle CPA.

:::

## First Setup And Login

On first startup, CPAMP needs an admin key. You can provide one:

```bash
CPA_MANAGER_ADMIN_KEY='replace-with-a-long-random-admin-key'
```

If not configured, Manager Server generates:

```text
cpamp_...
```

and prints it once in the startup logs.

First setup asks for:

```text
Admin Key
CPA URL
CPA Management Key
Request Monitoring
Collection Mode
Poll Interval
```

After setup:

- Browser login uses the CPAMP admin key.
- Setup/panel-saved CPA Management Keys are stored server-side and encrypted.
- In installer env/secret mode, Manager Server reads the CPA URL and CPA Management Key from the deployment environment.
- Manager Server uses the resolved CPA Management Key when calling CPA.
- New browsers no longer need the CPA Management Key.

## CPA Prerequisites

Request monitoring requires CPA usage publishing and the CPA usage queue.

Minimum:

```text
CPA v6.10.8+ for HTTP usage queue
```

Recommended:

```text
CPA v7.1.39+
```

CPA Management API must be enabled:

```yaml
remote-management:
  secret-key: 'your CPA Management Key'
  allow-remote: true
```

Usage publishing can be enabled by CPAMP during setup/config save, or directly in CPA:

```yaml
usage-statistics-enabled: true
```

Queue retention is controlled by CPA:

```yaml
redis-usage-queue-retention-seconds: 60
```

Default retention is 60 seconds and the maximum is 3600 seconds. Keep Manager Server running continuously.

## Collection Mode

Default:

```text
auto
```

Behavior:

```text
auto -> RESP Pub/Sub -> HTTP usage queue -> RESP pop fallback
```

| Mode        | Use when                                                           |
| ----------- | ------------------------------------------------------------------ |
| `auto`      | Recommended default.                                               |
| `subscribe` | Force RESP Pub/Sub for low-latency direct CPA API access.          |
| `http`      | Force HTTP usage queue, useful behind normal HTTP reverse proxies. |
| `resp`      | Force legacy RESP pop; must directly reach the CPA API port.       |

RESP transports cannot pass through a normal HTTP reverse proxy. If you see `unsupported RESP prefix 'H'`, the RESP client is probably connecting to an HTTP endpoint.

## Configuration Boundary

Managed by Manager Server:

- Bound CPA URL.
- Encrypted CPA Management Key.
- Request monitoring switch.
- Collection mode, poll interval, batch size, and query limit.
- SQLite usage data.
- Model pricing data.
- API Key aliases.
- Server inspection history.

Still managed by CPA:

- `usage-statistics-enabled`
- `redis-usage-queue-retention-seconds`
- `remote-management`
- proxy and routing config
- logging config
- auth files
- provider config
- CPA `config.yaml`

Saving CPAMP configuration does not rewrite the full CPA `config.yaml`.

## Environment Variables

| Variable                                | Default                                                     | Description                                                                                                                                                                                                                        |
| --------------------------------------- | ----------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `CPA_MANAGER_CONFIG`                    | empty                                                       | Optional config file path. Native packages default to `config.json` next to the binary.                                                                                                                                            |
| `HTTP_ADDR`                             | `0.0.0.0:18317`                                             | Manager Server listen address.                                                                                                                                                                                                     |
| `CPA_MANAGER_PPROF_ADDR`                | empty                                                       | Optional Go pprof listen address; only `localhost`, `127.0.0.1`, or `::1` is accepted.                                                                                                                                             |
| `USAGE_DATA_DIR`                        | Docker: `/data`; native: `./data`                           | Base data directory.                                                                                                                                                                                                               |
| `USAGE_DB_PATH`                         | Docker: `/data/usage.sqlite`; native: `./data/usage.sqlite` | SQLite database path.                                                                                                                                                                                                              |
| `CPA_MANAGER_ADMIN_KEY`                 | empty                                                       | Optional admin key.                                                                                                                                                                                                                |
| `CPA_MANAGER_ADMIN_KEY_FILE`            | `/run/secrets/cpa_admin_key`                                | Optional admin key file.                                                                                                                                                                                                           |
| `CPA_MANAGER_DATA_KEY`                  | empty                                                       | Optional data encryption key.                                                                                                                                                                                                      |
| `CPA_MANAGER_DATA_KEY_FILE`             | `/run/secrets/cpa_data_key`                                 | Optional data encryption key file.                                                                                                                                                                                                 |
| `CPA_MANAGER_DATA_KEY_PATH`             | Docker: `/data/data.key`; native: `./data/data.key`         | Generated data key path.                                                                                                                                                                                                           |
| `CPA_UPSTREAM_URL`                      | empty                                                       | Optional environment-managed CPA URL.                                                                                                                                                                                              |
| `CPA_MANAGEMENT_KEY`                    | empty                                                       | Optional environment-managed CPA Management Key.                                                                                                                                                                                   |
| `CPA_MANAGEMENT_KEY_FILE`               | `/run/secrets/cpa_management_key`                           | Optional CPA Management Key file.                                                                                                                                                                                                  |
| `USAGE_COLLECTOR_MODE`                  | `auto`                                                      | `auto`, `subscribe`, `http`, or `resp`.                                                                                                                                                                                            |
| `USAGE_RESP_QUEUE`                      | `usage`                                                     | RESP key argument; normally leave unchanged.                                                                                                                                                                                       |
| `USAGE_RESP_POP_SIDE`                   | `right`                                                     | `right` uses `RPOP`; `left` uses `LPOP`.                                                                                                                                                                                           |
| `USAGE_BATCH_SIZE`                      | `100`                                                       | Max records per batch.                                                                                                                                                                                                             |
| `USAGE_POLL_INTERVAL_MS`                | `500`                                                       | Idle poll interval.                                                                                                                                                                                                                |
| `USAGE_QUERY_LIMIT`                     | `50000`                                                     | Max recent usage events.                                                                                                                                                                                                           |
| `USAGE_DASHBOARD_HOURLY_ROLLUP_ENABLED` | `true`                                                      | Enable the hourly rollup worker plus the Dashboard and strictly unfiltered Usage Analytics query paths. Temporarily set it to `false` when diagnosing SQLite write contention or rollup failures; queries fall back to raw events. |
| `USAGE_ARCHIVE_RETENTION_ENABLED`       | `false`                                                     | Enable the startup-and-daily archive, verification, and bounded-delete worker. It is disabled by default, requires hourly rollup to remain enabled, and requires a Manager Server restart after changes.                           |
| `USAGE_ARCHIVE_RETENTION_DAYS`          | `30`                                                        | Age in days used by automatic retention; effective only when retention and hourly rollup are both enabled.                                                                                                                         |
| `USAGE_CORS_ORIGINS`                    | `*`                                                         | CORS origins for compatibility endpoints.                                                                                                                                                                                          |
| `USAGE_RESP_TLS_SKIP_VERIFY`            | `false`                                                     | Skip TLS verification for RESP connection.                                                                                                                                                                                         |
| `USAGE_QUOTA_COOLDOWN_ENABLED`          | `false`                                                     | Enable the provider quota cooldown worker for strict Codex usage-limit and xAI free-usage-exhausted signals.                                                                                                                       |
| `USAGE_ACCOUNT_ACTIONS_ENABLED`         | `false`                                                     | Enable the account action queue for auth issues that need review.                                                                                                                                                                  |
| `USAGE_ACCOUNT_ACTIONS_AUTO_DISABLE`    | `false`                                                     | Enable automatic disabling for auth issues. This only takes effect when the account action queue is enabled.                                                                                                                       |
| `PANEL_PATH`                            | empty                                                       | Optional custom `management.html`.                                                                                                                                                                                                 |

Startup precedence:

```text
environment variables > config.json > defaults
```

Temporarily enable the loopback-only pprof server when diagnosing CPU, heap, or goroutine behavior:

```bash
CPA_MANAGER_PPROF_ADDR=127.0.0.1:6060 ./cpa-manager-plus
go tool pprof http://127.0.0.1:6060/debug/pprof/heap
```

The equivalent config-file field is `pprofAddr`. The service is disabled by default and should not be exposed through Docker port mappings or a reverse proxy.

Hourly rollup is enabled by default. The worker catches up historical events in bounded batches. Dashboard and strictly unfiltered Usage Analytics long-window core metrics reuse complete hourly data; searches and dimension, status, latency, or cache filters continue to read raw events. If the checkpoint is pending, the requested timezone cannot be represented losslessly by UTC hourly buckets, or a rollup read fails, the affected query falls back to raw events. Runtime failures are recorded through rate-limited logs. To stop background rollup temporarily, set:

```bash
USAGE_DASHBOARD_HOURLY_ROLLUP_ENABLED=false
```

Restart Manager Server after changing it. Dashboard and Usage Analytics will always use raw events while disabled. Except for the one-time startup format upgrade described below, disabling this runtime switch does not delete current-format rollup data. The switch is not exposed in the UI.

Historical usage archives are stored under `usage-archives/` in the resolved data directory. Each segment is a sequence-numbered gzip JSONL file (`segment-*.jsonl.gz`). Usage import reads decompressed JSONL and does not automatically decode a `.gz` file; follow the archive-recovery procedure in [Backup And Restore](./backup.md). If only `USAGE_DB_PATH` or `dbPath` overrides the database location and no data directory is explicitly configured, the archive directory is placed beside that SQLite file. When both a data directory and database path are explicit, the data directory wins, so include that separate archive location in backups. On POSIX systems Manager Server creates directories as `0700` and files as `0600`. Windows inherits the parent directory ACL, so protect the resolved archive parent so only the service account and authorized administrators can access it. SQLite stores archive runs, segments, the maintenance lock, and the event identity ledger. Manual maintenance endpoints accept the CPAMP Admin Key only:

- `POST /v0/management/usage/archives/preview` with `{"cutoff_timestamp_ms": ...}` previews the eligible event count and estimated size.
- `POST /v0/management/usage/archives` with the same `{"cutoff_timestamp_ms": ...}` body creates a `previewed` run.
- `GET /v0/management/usage/archives?limit=20` returns recent sanitized run summaries.
- `GET /v0/management/usage/archives/{id}` returns sanitized progress and segment metadata.
- `POST /v0/management/usage/archives/{id}/resume` continues archiving or a failed stage.
- `POST /v0/management/usage/archives/{id}/verify` re-reads the manifest, segment checksums, and event digest.
- `POST /v0/management/usage/archives/{id}/delete` deletes raw rows in bounded batches only after the archive is verified and every required derived read path covers the run target.
- `HEAD /v0/management/usage/maintenance` is an Admin-Key-only capability probe; a supported Manager Server returns `204 No Content`.
- `GET /v0/management/usage/maintenance` returns raw/deleted counts, the active run and lock, migration and aggregate readiness, plus SQLite page/freelist and file-size statistics.

Creating a run never deletes raw rows immediately. A manual `resume` returns a coverage conflict while the cache-accounting migration is incomplete; after that migration completes, it finishes any pending response-metadata backfill before writing the first segment. Stable manual `archived` and `verified` runs do not block a later manual archive, so archive/verify can be used without enabling deletion; automatic-retention runs remain active until their delete stage completes. Deletion is allowed only after the archive files, manifest, and identity ledger have been verified and the cache-accounting migration, permanent hourly aggregate, pricing and monitoring rollups, monitoring search index, and account-history/dashboard checkpoints are ready through the run target. `GET /v0/management/usage/maintenance` summarizes the primary readiness signals, while the delete operation repeats the complete gate inside each bounded transaction. The same run can be resumed after a process restart or interruption. Deletion removes only `usage_events` rows; archive files and the identity ledger remain, so re-importing archived events stays idempotent. This workflow never runs SQLite `VACUUM` online. Automatic retention is disabled by default and does not start while `USAGE_DASHBOARD_HOURLY_ROLLUP_ENABLED=false`; enable it only after checking archive-disk capacity and rehearsing recovery.

Monitoring analytics responses include a `coverage` object when either the current query range or its summary-comparison range intersects raw history deleted after a verified archive. Current-range and comparison-range raw/deleted counts are reported independently and are time-range counts that are not narrowed by provider, model, account, search, or other analytics filters. The object also reports `core_aggregate_used` and machine-readable `fidelity_limitations`. Permanent hourly aggregates and event projections can continue to serve supported summary, model, and timeline totals accurately, but raw-only event details, latency percentiles, distributions, failure diagnostics, credential timelines, or unsupported searches can be incomplete. The Monitoring and Usage Analytics pages display this limitation instead of treating missing raw rows or zero-valued raw-only fields as complete history.

The Usage Maintenance page is available only when the panel is hosted by Manager Server and the Manager Service is available. A regular CPA-hosted panel does not show this entry. The page can run preview/create/resume/verify/delete and report reclaimable space, but physical compaction remains an offline CLI operation.

### Reclaim SQLite Space While Stopped

Logical deletion normally does not shrink the SQLite file immediately. Before compacting, back up the complete data set as described in [Backup And Restore](./backup.md), stop every Manager Server connected to the database, and reserve temporary free space conservatively equal to at least the current database-file size. Static `previewed`, `archived`, `verified`, and `failed` runs do not block compaction; recorded maintenance locks and active `archiving`, `verifying`, or `deleting` stages do. Pending derived-data migrations are allowed and their checkpoint state is preserved exactly. Never delete WAL, SHM, or a maintenance lock manually.

Native package:

```bash
cpa-manager-plus compact-usage --db-path ./data/usage.sqlite
```

Docker Compose, after stopping the service:

```bash
docker compose stop cpa-manager-plus
docker compose run --rm --no-deps cpa-manager-plus \
  compact-usage --db-path /data/usage.sqlite
docker compose up -d cpa-manager-plus
```

Windows PowerShell, after stopping the service:

```powershell
.\cpa-manager-plus.exe compact-usage --db-path .\data\usage.sqlite
```

The command acquires the same process-level database lock used by Manager Server, opens one SQLite connection with exclusive access, runs `quick_check`, foreign-key validation, `wal_checkpoint(TRUNCATE)`, `VACUUM`, `integrity_check`, and a second foreign-key validation, then compares logical usage summaries before and after compaction. It prints before/after database, WAL, SHM, page, and freelist statistics. It does not create an archive, delete raw rows, rewrite archive files, advance derived-data migrations, or touch `data.key`. A stale maintenance lock still blocks compaction. If it belongs to a resumable active or failed run, start Manager Server and resume that run; if it remains for an inactive or terminal run, preserve the backup and logs and stop for diagnosis. Never delete the lock manually. Preserve the database and complete backup set after a failure.

When upgrading to the lossless model encoding, Manager Server clears the old `usage_dashboard_hourly_rollups` rows and resets only the `dashboard_hourly` checkpoint. When hourly rollup is enabled, the worker then rebuilds it in bounded background batches; while disabled, it remains empty until the worker is enabled again. This format migration itself does not modify or delete `usage_events` and does not reset the account-history rollup. Long-window queries temporarily fall back to raw events until catch-up completes. The new encoding distinguishes an empty model, the literal `-` model, and models with surrounding whitespace, so a legitimate `-` model no longer disables the entire rollup path.

When upgrading an existing database, Manager Server performs schema and metadata changes plus any required derived-rollup reset during startup, but does not scan historical `usage_events`. Cache-accounting corrections that require a historical event scan begin in the background after the HTTP listener is bound, processing 1,000 rows per batch. Candidate scanning, event correction, and derived-row clearing each use bounded transactions with committed progress, so a restart resumes the active phase without repeating completed batches. Readers use raw events while corrected history is being applied or stale rollups are being cleared.

While the migration is running:

- Newly collected events are written in the new format and are outside the legacy migration target range.
- Account-history and dashboard-hourly rollup catch-up is paused to avoid building summaries from partially migrated data.
- Logs report migration start, progress, retryable failures, and completion.
- `GET /status` exposes `status`, `lastEventId`, `targetEventId`, `processedRows`, `changedRows`, and `appliedRows` under `dataMigration`; low-level migration error text is not returned.

After completion, the response-metadata backfill and both rollup workers continue automatically. Do not start a second Manager Server against the same SQLite database or CPA queue to accelerate the migration.

Historical rollup rebuilding and stale-row cleanup run only after the HTTP listener is available. Startup index preparation is deliberately bounded: Manager Server creates a missing index only when its target table is empty and the index name is not retained by a parked table. Indexes for non-empty tables and retained names are logged as deferred so collector startup is not delayed by a large index build. During a rebuild, queries use the current complete revision or fall back to raw `usage_events`; an interrupted batch resumes from its committed checkpoint after restart. These tasks must not modify or delete `usage_events`.

An upgraded database can retain an old request-monitoring FTS generation after its paired projection rows have been removed in bounded online batches. It can also have deferred indexes for populated tables or an obsolete quota-cooldown identity index that must be replaced offline. In the exceptional case where one legacy quota observation group exceeds the safe online batch limit, the migration enters a failed state and logs `offline cleanup required`; the original snapshot fallback remains available. When the log reports deferred index preparation, `cleanup requires offline finalization`, or `offline cleanup required`, stop every Manager Server process using the database, run the same-version binary once, and then restart the service:

```bash
cpa-manager-plus cleanup-derived --db-path /data/usage.sqlite
```

For a native installation using the default database path, `cpa-manager-plus cleanup-derived` is sufficient. Manager Server holds an operating-system lock at `<absolute-database-path>.manager.lock` for its entire lifetime, and the command refuses to run while that lock is held. The lock file itself is persistent and does not need to be deleted; stop the Manager Server process and retry instead. Symbolic-link aliases resolve to the same lock, while databases with multiple hard links are rejected because SQLite WAL/SHM sidecars cannot safely share those aliases. Back up the SQLite database plus `data.key` before offline maintenance. The command prepares deferred indexes, replaces obsolete derived indexes, completes oversized legacy quota observation groups, removes obsolete derived FTS/projection generations, and leaves `usage_events` intact.

See the [July 10, 2026 Performance Optimization Report](./performance-optimization-2026-07-10.md) for the causes, delivery stages, and complete 100k benchmark evidence.

When `USAGE_QUOTA_COOLDOWN_ENABLED`, `USAGE_ACCOUNT_ACTIONS_ENABLED`, or `USAGE_ACCOUNT_ACTIONS_AUTO_DISABLE` is set through the environment, the matching panel switch is shown as environment-sourced and locked. Remove the environment variable and restart Manager Server if you want the setting to be editable from the panel.

::: details Advanced: runtime endpoints

## Runtime Endpoints

| Endpoint                                                         | Purpose                                                                                                      |
| ---------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------ |
| `GET /health`                                                    | Health check.                                                                                                |
| `GET /status`                                                    | Collector, SQLite, event count, and background data-migration progress.                                      |
| `GET /usage-service/info`                                        | Manager Server mode detection.                                                                               |
| `GET /usage-service/config`                                      | Read CPAMP Manager Server config.                                                                            |
| `PUT /usage-service/config`                                      | Save CPAMP config and restart collector if needed.                                                           |
| `GET /usage-service/account-processing-policy`                   | Read quota cooldown, account action queue, and auto-disable policy.                                          |
| `PATCH /usage-service/account-processing-policy`                 | Update account processing policy. Fields locked by environment variables cannot be modified through the API. |
| `GET /usage-service/quota-cooldowns`                             | Read active quota cooldowns so Credential Management can show recovery hints.                                |
| `POST /setup`                                                    | First setup.                                                                                                 |
| `GET /v0/management/usage`                                       | Compatible usage data.                                                                                       |
| `GET /v0/management/usage/export`                                | Export JSONL usage events.                                                                                   |
| `POST /v0/management/usage/import`                               | Import JSONL or compatible legacy snapshots.                                                                 |
| `POST /v0/management/usage/archives/preview`                     | Preview a historical archive range (Admin Key only).                                                         |
| `POST /v0/management/usage/archives`                             | Create a historical archive run (Admin Key only).                                                            |
| `GET /v0/management/usage/archives?limit=20`                     | Read recent sanitized archive-run summaries (Admin Key only).                                                |
| `GET /v0/management/usage/archives/{id}`                         | Read sanitized archive-run status and segment metadata (Admin Key only).                                     |
| `POST /v0/management/usage/archives/{id}/resume`                 | Resume an archive run (Admin Key only).                                                                      |
| `POST /v0/management/usage/archives/{id}/verify`                 | Verify the archive manifest and segments (Admin Key only).                                                   |
| `POST /v0/management/usage/archives/{id}/delete`                 | Delete verified raw data in bounded batches (Admin Key only).                                                |
| `HEAD /v0/management/usage/maintenance`                          | Probe Usage Maintenance capability; success is `204 No Content` (Admin Key only).                            |
| `GET /v0/management/usage/maintenance`                           | Read maintenance readiness and reclaimable SQLite space (Admin Key only).                                    |
| `GET /v0/management/model-prices/usage-summary`                  | Return the lightweight model-call summary used by the Model Prices page.                                     |
| `GET /v0/management/model-prices`                                | Model pricing.                                                                                               |
| `PUT /v0/management/model-prices`                                | Replace saved model pricing.                                                                                 |
| `POST /v0/management/model-prices/sync`                          | Price sync.                                                                                                  |
| `GET /v0/management/api-key-aliases`                             | API Key aliases.                                                                                             |
| `GET /v0/management/account-action-candidates`                   | Auth issue action queue.                                                                                     |
| `POST /v0/management/account-action-candidates/{id}/ignore`      | Ignore an account action candidate.                                                                          |
| `POST /v0/management/account-action-candidates/{id}/resolve`     | Mark an account action candidate as resolved.                                                                |
| `POST /v0/management/account-action-candidates/{id}/enable`      | Re-enable the auth file linked to a candidate.                                                               |
| `DELETE /v0/management/account-action-candidates/{id}/auth-file` | Delete the auth file linked to a candidate.                                                                  |
| `GET /v0/management/dashboard/*`                                 | Dashboard data.                                                                                              |
| `GET /v0/management/monitoring/*`                                | Monitoring data.                                                                                             |
| `GET /v0/management/codex-inspection/*`                          | Server Codex inspection.                                                                                     |
| `GET /models`, `GET /v1/models`                                  | Proxy model-list requests to CPA after setup.                                                                |
| `/v0/management/*`                                               | Proxied to CPA unless handled by CPAMP.                                                                      |

After setup, Manager Server management endpoints require:

```text
Authorization: Bearer <CPAMP_ADMIN_KEY>
```

:::

## Data And Security

Back up:

```text
usage.sqlite
usage.sqlite-wal
usage.sqlite-shm
data.key
usage-archives/
```

Security notes:

- The admin key is not stored in plaintext; only a salted HMAC credential is stored.
- CPA Management Keys saved through setup or the panel are encrypted before being stored in SQLite.
- If `usage.sqlite` leaks without `data.key`, the saved CPA Management Key is not directly readable.
- If both `usage.sqlite` and `data.key` leak, the saved CPA Management Key can be decrypted.
- If `data.key` is lost, the saved CPA Management Key cannot be recovered.
- If the CPA connection is env/secret-managed, also back up the secret files in the install directory.
- Request metadata may contain model names, endpoints, account labels, project snapshots, token usage, latency, and failure summaries.
- Raw failure bodies stay local in SQLite. Normal APIs and JSONL exports expose sanitized summaries instead of raw diagnostic bodies.
- Archive JSONL files may contain event-level `fail_body` and `raw_json`; protect them with the same access controls as the live SQLite database.

## Import And Export

Manager Server exports JSONL / NDJSON usage events.

It can import:

- JSONL / NDJSON exported by Manager Server.
- Legacy usage snapshots only when request-level details exist.

Large files use resumable chunk sessions, with total size bounded by `USAGE_IMPORT_DISK_QUOTA_BYTES`. Each JSONL record or individual object in a legacy snapshot `details` array must be no larger than 10 MiB; chunked upload does not relax this per-record limit.

Aggregate-only legacy files cannot reconstruct request-level monitoring. Test imports against a backup or staging database when accuracy matters.
