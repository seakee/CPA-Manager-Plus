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

When upgrading to the lossless model encoding, Manager Server clears the old `usage_dashboard_hourly_rollups` rows and resets only the `dashboard_hourly` checkpoint. When hourly rollup is enabled, the worker then rebuilds it in bounded background batches; while disabled, it remains empty until the worker is enabled again. This format migration itself does not modify or delete `usage_events` and does not reset the account-history rollup. Long-window queries temporarily fall back to raw events until catch-up completes. The new encoding distinguishes an empty model, the literal `-` model, and models with surrounding whitespace, so a legitimate `-` model no longer disables the entire rollup path.

When upgrading an existing database, Manager Server performs schema and metadata changes plus any required derived-rollup reset during startup, but does not scan historical `usage_events`. Cache-accounting corrections that require a historical event scan begin in the background after the HTTP listener is bound, processing 1,000 rows per batch. Each data update and its checkpoint commit in the same transaction, so a restart resumes at the last successfully committed event ID instead of repeating completed batches.

While the migration is running:

- Newly collected events are written in the new format and are outside the legacy migration target range.
- Account-history and dashboard-hourly rollup catch-up is paused to avoid building summaries from partially migrated data.
- Logs report migration start, progress, retryable failures, and completion.
- `GET /status` exposes `status`, `lastEventId`, `targetEventId`, and `processedRows` under `dataMigration`; low-level migration error text is not returned.

After completion, the response-metadata backfill and both rollup workers continue automatically. Do not start a second Manager Server against the same SQLite database or CPA queue to accelerate the migration.

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
| `GET /usage-service/quota-cooldowns`                             | Read active quota cooldowns so the auth files page can show recovery hints.                                  |
| `POST /setup`                                                    | First setup.                                                                                                 |
| `GET /v0/management/usage`                                       | Compatible usage data.                                                                                       |
| `GET /v0/management/usage/export`                                | Export JSONL usage events.                                                                                   |
| `POST /v0/management/usage/import`                               | Import JSONL or compatible legacy snapshots.                                                                 |
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

## Import And Export

Manager Server exports JSONL / NDJSON usage events.

It can import:

- JSONL / NDJSON exported by Manager Server.
- Legacy usage snapshots only when request-level details exist.

Aggregate-only legacy files cannot reconstruct request-level monitoring. Test imports against a backup or staging database when accuracy matters.
