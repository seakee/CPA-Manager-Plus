# CPA Manager Plus

[中文文档](README_CN.md)

A single-file Web UI for **CLI Proxy API (CPA)** plus a **Manager Server** for persistent usage analytics and panel hosting.

Since v6.10.0, CPA no longer includes built-in usage statistics. This project now supports usage analytics through a long-running Manager Server that consumes the CPA usage queue, persists request events to SQLite, and exposes panel-compatible usage APIs.

CPA Manager Plus is the recommended successor to CPA-Manager. It combines the CPA management panel with a Docker-ready Manager Server, admin-key protected full-panel mode, encrypted CPA Management Key storage, server-backed analytics, model pricing, API key aliases, dashboard cards, and Codex account inspection.

- **CPA Main project**: https://github.com/router-for-me/CLIProxyAPI
- **Recommended CPA version**: >= v7.1.39
- **Minimum CPA version for HTTP usage queue**: >= v6.10.8

## Panel Preview

![Dashboard overview showing Manager Server status, usage metrics, request health, token breakdowns, model costs, and collector state](img/dashboard-overview.png)
![Request monitoring account overview showing account statistics, quota progress, token structure, model usage, and per-account costs](img/request-monitoring-account-overview.png)
![Request monitoring realtime events showing recent calls, model, reasoning effort, request status, TPS, latency, usage, and cost](img/request-monitoring-realtime-events.png)
![Server Codex account inspection showing scheduled runs, result pagination, account status, cleanup recommendations, and logs](img/server-codex-inspection.png)

## What This Provides

- A single-file React management panel for CPA Management API (`/v0/management`)
- A Dockerized Manager Server for SQLite-backed usage persistence and built-in panel hosting
- Native `amd64` and `arm64` packages for Windows, macOS, and Linux with the panel embedded
- Two deployment modes:
  - **Full Docker mode**: open the built-in panel from Manager Server; first startup logs an admin key, first setup uses that admin key to save the CPA connection, and later logins use the admin key to manage the whole panel
  - **CPA panel mode**: keep using CPA's `/management.html` as a pure CPA panel; it does not configure or call a separate Manager Server
- Full Docker mode adds runtime monitoring, account/model/channel breakdowns, model pricing, estimated token cost, imports/exports, API key aliases, server Codex inspection, and Manager Server system utilities
- Both modes keep normal CPA management and local Codex account inspection available

## Choose a Deployment Mode

| Mode | Entry URL | What the user configures | Best for |
|---|---|---|---|
| Full Docker mode | `http://<host>:18317/management.html` | First startup log provides the admin key; first setup: admin key + CPA URL + CPA Management Key; later login: admin key | New deployments, one entry point, least browser/CORS complexity |
| CPA panel mode | `http://<cpa-host>:8317/management.html` | Log in to CPA with the CPA Management Key | Existing CPA automatic panel loading without Manager Server analytics |
| Frontend only | Vite dev server or `apps/web/dist/index.html` | CPA URL | Development |

Full Docker mode does not bundle CPA itself. CPA still runs as the upstream service; the Docker image provides the Manager Server plus an embedded copy of this management panel.

### Feature Boundary by Mode

| Capability | Full Docker mode | CPA panel mode |
|---|---:|---:|
| CPA config, provider/account/key management, auth files, logs, quota views, and CPA Management API features | Yes | Yes |
| Local Codex account inspection in the browser | Yes | Yes |
| Manager Server setup, admin-key login, encrypted CPA Management Key storage | Yes | No |
| Request monitoring, dashboard usage statistics, model prices, API key aliases, usage import/export | Yes | No |
| Server Codex inspection, scheduled runs, persisted inspection history | Yes | No |
| Manager Server `/status`, `/usage-service/config`, `/v0/management/usage`, model-price, alias, and import/export APIs | Admin key only | Not used |

One Manager Server binds to exactly one CPA during setup. The regular configuration page can change collector settings and update the CPA Management Key for that bound CPA, but it cannot switch to another CPA URL. To bind a different CPA, intentionally reset or migrate the Manager Server data/configuration.

## CPA Prerequisites

Request statistics require the CPA usage queue:

- CPA Management must be enabled because the usage queue uses the same availability and CPA Management Key as `/v0/management`.
- Request monitoring requires CPA usage publishing: set `usage-statistics-enabled: true`, or submit `{ "value": true }` to `PUT /usage-statistics-enabled`. CPA Manager Plus enables this automatically when request monitoring is enabled during setup or configuration save.
- Disabling CPAM request monitoring only stops the Manager Server collector. It does not automatically disable CPA usage publishing or clear the CPA usage queue. If CPA usage publishing remains enabled, re-enabling request monitoring within the queue retention window may collect events retained while the collector was stopped.
- CPA `v7.1.39+` is recommended for current panel capabilities and the full Redis usage metadata set used by newer monitoring views: request-side `reasoning_effort`, `service_tier`, `executor_type`, `tokens.cache_read_tokens`, `tokens.cache_creation_tokens`, `fail.status_code`, and `fail.body`. CPA `v6.10.8+` already exposes the HTTP usage queue endpoint `/v0/management/usage-queue`, which can pass through regular HTTP reverse proxies. Older compatible CPA versions omit these optional fields; CPA Manager Plus still imports and collects those events, with missing string fields shown as empty/unknown and missing numeric fields treated as `0`.
- CPA `v7.1.39+` RESP Pub/Sub emits usage control messages such as `{"support_refresh":true}` and `{"refresh":true}`. Current CPA Manager Plus filters those control messages instead of storing them as empty request rows; refresh messages also clear the auth snapshot cache so account metadata is re-read after CPA auth/config changes.
- `reasoning_effort` is the request-side reasoning configuration. It is not actual reasoning token usage; actual reasoning consumption is still reported by `tokens.reasoning_tokens`.
- Manager Server `auto` mode tries RESP Pub/Sub (`subscribe`) first, then the HTTP usage queue, then RESP pop mode for older CPA versions. RESP transports listen on the CPA API port, usually `8317`, and cannot pass through a regular HTTP reverse proxy.
- CPA keeps queue items in memory for `redis-usage-queue-retention-seconds`, default `60` seconds and maximum `3600` seconds. Keep Manager Server running continuously.
- Manager Server `pollIntervalMs` must be less than or equal to the CPA queue retention window converted to milliseconds. Saves are rejected when the collector would poll too slowly and risk expired queue items.
- Exactly one Manager Server should consume the same CPA usage queue.

## Architecture

### Full Docker Mode

```text
Browser
  -> Manager Server :18317
      -> built-in management.html
      -> /v0/management/usage and /v0/management/model-prices from SQLite
      -> other /v0/management/* proxied to CPA
      -> HTTP/RESP/PubSub consumer -> CPA API port
      -> SQLite /data/usage.sqlite
```

On first startup, if `CPA_MANAGER_ADMIN_KEY` / `CPA_MANAGER_ADMIN_KEY_FILE` is not provided, Manager Server generates a `cmp_admin_...` admin key and prints it to the startup log only once. The login page calls `GET /usage-service/info` and detects that it is hosted by Manager Server. If the response is not configured yet, it shows the setup wizard: you first enter the admin key, then the CPA URL, CPA Management Key, and choose whether to enable request monitoring. When monitoring is enabled, you also set the collector polling interval; Manager Server validates the CPA Management API, enables CPA usage publishing, checks that the poll interval does not exceed the CPA queue retention window, stores CPA Manager Plus configuration in SQLite, starts the collector with the configured mode (`auto` by default: RESP Pub/Sub, then HTTP queue, then RESP pop fallback), and serves the panel from the same origin. When monitoring is disabled, the CPA connection is still saved for Management API proxying, but CPA usage publishing and the collector stay off.

After Manager Server is configured, a new browser opening the same URL uses the normal login form. Full Docker mode uses the admin key as the login credential; the CPA Management Key is stored server-side and is only used by Manager Server when it talks to CPA upstream.

### CPA Panel Mode

```text
Browser
  -> CPA /management.html
      -> normal CPA Management API calls stay on CPA
      -> local Codex inspection runs in the browser
```

Use this when CPA still auto-downloads and serves the panel. This mode is served by CPA and is fully isolated from Manager Server. It does not show the full Docker setup wizard, does not ask for the Manager Server admin key, does not save a Manager Server URL, and does not expose features backed by Manager Server SQLite or CPA usage statistics. The monitoring center, dashboard usage statistics, model pricing, API key aliases, usage import/export, collector status, and server Codex inspection are hidden or unavailable. Local Codex account inspection remains available because it runs in the browser against the CPA-accessible auth files.

### Manager Server Backend

The Go backend lives under the `github.com/seakee/cpa-manager-plus/apps/manager-server` module. It still exposes the compatible `/usage-service/*` management endpoints. Its request path follows a layered shape:

```text
model -> repository -> service -> controller -> router
```

- `internal/model` defines persisted and API-facing data structures.
- `internal/repository` owns SQLite access and schema migration while keeping the existing tables compatible.
- `internal/service` contains setup, manager config, usage, model price, API key alias, proxy, panel, and collector lifecycle rules.
- `internal/http/controller`, `internal/http/middleware`, and `internal/http/router` keep HTTP decoding, CORS/auth/recovery, Gin routing, and response writing at the edge.
- `internal/httpapi` remains a compatibility wrapper for the current `cmd/cpa-manager-plus` entrypoint.
- `internal/worker` coordinates collector startup/restart/stop without changing the existing HTTP, RESP Pub/Sub, RESP pop, and auto queue consumers.

## Quick Start: Full Docker Mode

### Container Images

Public multi-arch images are published to both registries:

- Docker Hub: `seakee/cpa-manager-plus`
- GitHub Container Registry: `ghcr.io/seakee/cpa-manager-plus`

```bash
docker run -d \
  --name cpa-manager-plus \
  --restart unless-stopped \
  -p 18317:18317 \
  -v cpa-manager-plus-data:/data \
  seakee/cpa-manager-plus:latest
```

Open:

```text
http://<host>:18317/management.html
```

On first setup, enter:

- Admin key: `CPA Manager Plus admin key generated: cmp_admin_...` from the first startup log
- CPA URL:
  - Docker Desktop host CPA: `http://host.docker.internal:8317` (default suggestion unless the panel was built with `VITE_DEFAULT_CPA_BASE_URL`)
  - Same compose network: `http://cli-proxy-api:8317`
  - Remote CPA: `https://your-cpa.example.com`
- CPA Management Key

You can read the generated admin key with `docker logs cpa-manager-plus`. After setup, the same entry URL uses the saved CPA connection from Manager Server SQLite. New browsers only need the admin key on the login page.

The published image supports `linux/amd64` and `linux/arm64`. Docker examples use Docker Hub by default. To pull from GitHub Container Registry instead, replace `seakee/cpa-manager-plus:latest` with `ghcr.io/seakee/cpa-manager-plus:latest`.

### Native Packages

GitHub Releases also provide native packages with the panel embedded:

- `cpa-manager-plus_<version>_linux_amd64.tar.gz`
- `cpa-manager-plus_<version>_linux_arm64.tar.gz`
- `cpa-manager-plus_<version>_darwin_amd64.tar.gz`
- `cpa-manager-plus_<version>_darwin_arm64.tar.gz`
- `cpa-manager-plus_<version>_windows_amd64.zip`
- `cpa-manager-plus_<version>_windows_arm64.zip`

macOS/Linux:

```bash
tar -xzf cpa-manager-plus_vX.Y.Z_linux_amd64.tar.gz
cd cpa-manager-plus_vX.Y.Z_linux_amd64
./cpa-manager-plus
```

The tar archives preserve execute permissions, so no extra `chmod +x` is normally required after extraction. If macOS blocks the unsigned binary, run `xattr -dr com.apple.quarantine .` in the extracted directory and start it again.

Windows PowerShell:

```powershell
Expand-Archive .\cpa-manager-plus_vX.Y.Z_windows_amd64.zip -DestinationPath .
cd .\cpa-manager-plus_vX.Y.Z_windows_amd64
.\cpa-manager-plus.exe
```

You can double-click `cpa-manager-plus.exe` on Windows, but PowerShell is recommended because it keeps logs and startup errors visible.

Then open:

```text
http://<host>:18317/management.html
```

Native packages do not include CPA itself. Run CPA separately, then enter the admin key, CPA URL, and CPA Management Key during first setup. After setup, the login page only needs the admin key. Set `USAGE_DATA_DIR` or `USAGE_DB_PATH` only when you want to override the default data location.

On first start, if `USAGE_DATA_DIR` and `USAGE_DB_PATH` are not set, the native package creates `config.json` next to the binary and writes SQLite data to `data/usage.sqlite` in the same directory. The extracted package directory therefore contains both the program and its user data.

### Docker Compose

```yaml
services:
  cpa-manager-plus:
    image: seakee/cpa-manager-plus:latest
    restart: unless-stopped
    ports:
      - "18317:18317"
    volumes:
      - cpa-manager-plus-data:/data

volumes:
  cpa-manager-plus-data:
```

Start:

```bash
docker compose up -d
```

To use GitHub Container Registry, replace the compose image with `ghcr.io/seakee/cpa-manager-plus:latest`.

### Linux Host CPA

If CPA runs directly on a Linux host and Manager Server runs in Docker, add a host gateway:

```bash
docker run -d \
  --name cpa-manager-plus \
  --restart unless-stopped \
  --add-host=host.docker.internal:host-gateway \
  -p 18317:18317 \
  -v cpa-manager-plus-data:/data \
  seakee/cpa-manager-plus:latest
```

Then enter `http://host.docker.internal:8317` as the CPA URL during first setup.

## Quick Start: CPA Panel Mode

1. Start CPA as usual and open:

   ```text
   http://<cpa-host>:8317/management.html
   ```

   Log in to CPA with the CPA Management Key. This entry is served by CPA and does not use the full Docker setup wizard.

2. To make CPA use this project as its default panel, open:

   ```text
   Configuration -> Remote Access and Control Panel
   ```

   Set **Panel Repository** (`remote-management.panel-repo`) to:

   ```text
   https://github.com/seakee/CPA-Manager-Plus
   ```

   Keep **Disable Control Panel** off. If **Disable Panel Auto Updates** is on, CPA only downloads the panel when the cached `static/management.html` is missing.

3. Save the CPA configuration and reload:

   ```text
   http://<cpa-host>:8317/management.html
   ```

4. Use the CPA panel normally.

This mode is intentionally limited to CPA-backed functionality. It does not configure Manager Server, does not send the current CPA URL or CPA Management Key to Manager Server, and does not read Manager Server SQLite data. Use Full Docker mode when you need request monitoring, historical usage statistics, model pricing, API key aliases, usage import/export, or server Codex inspection.

## Build Locally

```bash
docker compose -f docker-compose.manager.yml up --build
```

This builds the React panel and embeds it into the Go Manager Server binary.

## Manager Server Configuration

The CPA URL and CPA Management Key are bound during first setup, or from environment variables for unattended startup. After that, **Configuration -> CPA Manager Plus Configuration** manages request monitoring enablement, collection mode, polling interval, and CPA Management Key rotation for the already bound CPA; it does not change the CPA URL. CPA Manager Plus configuration is persisted in SQLite.

The variables below are Manager Server runtime settings. Frontend build-time settings are separate: `VITE_DEFAULT_CPA_BASE_URL` sets the default CPA URL shown by the Manager Server-hosted first setup wizard. When it is not set, the Docker-hosted panel suggests `http://host.docker.internal:8317`.

| Variable | Default | Description |
|---|---:|---|
| `CPA_MANAGER_CONFIG` | empty | Optional config file path. When empty, native packages use `config.json` next to the binary |
| `HTTP_ADDR` | `0.0.0.0:18317` | Manager Server HTTP listen address |
| `USAGE_DB_PATH` | Docker: `/data/usage.sqlite`; native: `./data/usage.sqlite` | SQLite database path |
| `USAGE_DATA_DIR` | Docker: `/data`; native: `./data` | Base data directory when `USAGE_DB_PATH` is not overridden |
| `CPA_MANAGER_ADMIN_KEY` | empty | Optional admin key; when empty, first startup generates and logs one |
| `CPA_MANAGER_ADMIN_KEY_FILE` | `/run/secrets/cpa_admin_key` | Optional admin key file |
| `CPA_MANAGER_DATA_KEY` | empty | Optional data encryption key; when empty, read or generate it through `CPA_MANAGER_DATA_KEY_PATH` |
| `CPA_MANAGER_DATA_KEY_FILE` | `/run/secrets/cpa_data_key` | Optional data encryption key file |
| `CPA_MANAGER_DATA_KEY_PATH` | Docker: `/data/data.key`; native: `./data/data.key` | Auto-generated data encryption key file path |
| `CPA_UPSTREAM_URL` | empty | Optional CPA base URL for unattended startup |
| `CPA_MANAGEMENT_KEY` | empty | Optional CPA Management Key for unattended startup |
| `CPA_MANAGEMENT_KEY_FILE` | `/run/secrets/cpa_management_key` | Optional file containing the CPA Management Key |
| `USAGE_COLLECTOR_MODE` | `auto` | Collection mode: `auto` tries RESP Pub/Sub, then HTTP usage queue, then RESP pop fallback; `subscribe` forces RESP Pub/Sub; `http` forces HTTP; `resp` forces RESP pop |
| `USAGE_RESP_QUEUE` | `usage` | RESP key argument; CPA currently ignores it, leave the default unless upstream changes |
| `USAGE_RESP_POP_SIDE` | `right` | `right` uses `RPOP`; `left` uses `LPOP` |
| `USAGE_BATCH_SIZE` | `100` | Maximum queue records per pop |
| `USAGE_POLL_INTERVAL_MS` | `500` | Idle polling interval |
| `USAGE_QUERY_LIMIT` | `50000` | Maximum recent events returned through compatible `/usage` |
| `USAGE_CORS_ORIGINS` | `*` | Allowed browser origins for CPA panel mode |
| `USAGE_RESP_TLS_SKIP_VERIFY` | `false` | Skip TLS verification for RESP connection |
| `USAGE_QUOTA_COOLDOWN_ENABLED` | `false` | Opt-in Codex usage-limit cooldown worker; see [Codex usage-limit cooldown](#codex-usage-limit-cooldown) |
| `PANEL_PATH` | empty | Serve a custom `management.html` instead of the embedded one |

Startup configuration precedence is: environment variables > `config.json` > program defaults. Relative paths in the config file are resolved from the config file directory. The generated default config is:

```json
{
  "httpAddr": "0.0.0.0:18317",
  "dataDir": "./data"
}
```

If `CPA_MANAGER_ADMIN_KEY` is set, the service initializes the admin credential from that value and does not log a generated admin key. If `CPA_UPSTREAM_URL` and `CPA_MANAGEMENT_KEY` are set, collection starts automatically on boot and the connection is shown as environment-managed in the panel. Otherwise, use the full Docker setup flow; the result is saved to SQLite `settings.manager_config_v1`. The legacy `settings.setup` value is still written for compatibility and rollback.

### Codex usage-limit cooldown

The quota cooldown worker is disabled by default. To enable it, set:

```bash
USAGE_QUOTA_COOLDOWN_ENABLED=true
```

or add the equivalent config-file field:

```json
{
  "quotaCooldownEnabled": true
}
```

When enabled, Manager Server watches only newly inserted request-monitoring events and temporarily disables a CPA auth file only for the strict Codex short-window usage-limit case:

- provider is `codex`;
- HTTP status is `429`;
- structured error type is `usage_limit_reached`;
- the event includes an explicit `resets_at` or `resets_in_seconds` value;
- the event includes an auth-file snapshot.

Temporary disables are persisted in `quota_cooldowns` with CPAMP ownership metadata such as `owner=cpamp_usage_429`, auth file/index, account snapshot, event hash, `recover_at_ms`, and the pre-disable state. Recovery is driven from SQLite, so due cooldowns can be recovered after Manager Server restarts as long as the collector runtime CPA URL and Management Key are available from the current environment, saved Manager Server configuration, or setup record.

Safety boundaries:

- only CPAMP-owned cooldowns with `pre_disabled_state=false` are auto-recovered;
- auth files that were already disabled before CPAMP acted are not re-enabled automatically;
- recovery re-reads current CPA auth files and verifies the auth file and auth index before enabling;
- the cooldown state is a CPAMP-derived temporary state, not a third CPA auth-file state;
- the worker does not expose raw request `fail_body` / `raw_json` through management APIs.

### CPA vs CPA Manager Plus Configuration Boundary

- **CPA configuration**: `usage-statistics-enabled`, `redis-usage-queue-retention-seconds`, proxy, logging, routing, auth files, and related fields still belong to CPA and are managed by `/config` / `/config.yaml`.
- **CPA Manager Plus configuration**: the setup-bound CPA URL, encrypted CPA Management Key, request monitoring enablement, Manager Server collection mode, `pollIntervalMs`, `batchSize`, and `queryLimit` are persisted in Manager Server SQLite in Full Docker mode. The key can be rotated for the same CPA; changing the CPA URL requires resetting setup.
- The configuration panel shows CPA and CPA Manager Plus settings separately. Saving CPAM settings does not write to CPA `config.yaml`; enabling request monitoring calls CPA Management API to enable usage publishing, while disabling request monitoring only stops the CPAM collector.

### Migration Guide

When upgrading from the old CPA-Manager project, read [Migration from CPA-Manager](docs/migration-from-cpa-manager.md) first. The core rules are:

1. Stop the old backend service before backup, then back up the old `/data` directory or Docker volume, including at least `usage.sqlite`, `usage.sqlite-wal`, and `usage.sqlite-shm`.
2. Start CPA Manager Plus with the same old `/data` volume, or copy the old data into the new `/data`. The old project often used `cpa-manager-data`; the Plus examples use `cpa-manager-plus-data`. Do not accidentally start with an empty new volume.
3. On first Plus startup, the service adds `settings.admin_credential_v1`, `settings.bootstrap_state_v1`, and `/data/data.key`. From this point forward, backups must include both SQLite files and `data.key`.
4. Full Docker mode now logs in with the Manager Server admin key, not the CPA Management Key. Prefer setting `CPA_MANAGER_ADMIN_KEY` or `CPA_MANAGER_ADMIN_KEY_FILE` during migration; otherwise save the generated `cmp_admin_...` value from the first startup log.
5. If an older version already saved CPA URL and CPA Management Key through `/setup`, the service migrates from `settings.setup` to `settings.manager_config_v1` and rewrites the old plaintext CPA Management Key as encrypted storage during startup migration.
6. If you use `CPA_UPSTREAM_URL` / `CPA_MANAGEMENT_KEY`, the connection remains environment-managed. To switch to panel persistence, remove those environment variables, restart, and save from the panel.
7. Older external-Manager CPA panel integrations are no longer supported. Open the Manager Server-hosted panel to view historical usage data, model prices, aliases, imports/exports, and server inspection history. CPA panel mode remains a pure CPA panel.

## Data and Security Notes

- SQLite data is stored under `/data`; mount it to persistent storage.
- The admin key is not stored in plaintext. SQLite `settings.admin_credential_v1` stores only the salt and HMAC-SHA256 digest. An automatically generated admin key is printed to the first startup log once; use `CPA_MANAGER_ADMIN_KEY_FILE` with Docker Secret or an external secret manager for managed deployments.
- CPA Management Key is encrypted with the data key before it is stored in SQLite `settings`, so collection and CPA Management API proxying can resume after restart.
- The data key is provided by `CPA_MANAGER_DATA_KEY` / `CPA_MANAGER_DATA_KEY_FILE`, or generated at `CPA_MANAGER_DATA_KEY_PATH`; Docker defaults to `/data/data.key` with `0600` permissions.
- Data key security assessment: AES-GCM prevents a leaked SQLite file alone from directly exposing the CPA Management Key. If an attacker gets both `/data/usage.sqlite` and `/data/data.key`, the CPA Management Key can still be decrypted. If the data key is lost, encrypted CPA Management Key values cannot be recovered and the CPA connection must be initialized or saved again.
- New versions prefer SQLite `settings.manager_config_v1`; legacy `settings.setup` is kept as compatibility data.
- Protect the `/data` volume. It contains usage metadata, admin credential digest, the data key file, and the encrypted CPA Management Key.
- Manager Server redacts key-like fields before storing raw JSON payload snapshots, but request metadata may still expose requested/resolved models, endpoints, account labels, project snapshots, and token usage.
- RESP pop queue consumption is destructive. RESP Pub/Sub is streaming. Do not run multiple Manager Server consumers against the same CPA instance.
- If Manager Server is down longer than CPA's queue retention window, that period's usage cannot be recovered without CPA-side persistence.
- If only the CPAM collector is stopped while CPA usage publishing remains enabled, restarting the collector within the retention window may consume queue items produced while collection was disabled.

## Runtime Endpoints

| Endpoint | Purpose |
|---|---|
| `GET /health` | Basic health check |
| `GET /status` | Collector, SQLite, event count, and error status |
| `GET /usage-service/info` | Allows the frontend to detect full Docker mode and read `configured` for setup vs login flow |
| `GET /usage-service/config` | Reads persistent CPA Manager Plus configuration and CPA usage publishing status |
| `PUT /usage-service/config` | Saves CPA Manager Plus configuration and restarts the collector when needed |
| `POST /setup` | Protected by the admin key; saves CPA URL + CPA Management Key and starts collection |
| `GET /v0/management/usage` | Compatible usage payload for the panel |
| `GET /v0/management/usage/export` | Export usage events as JSONL |
| `POST /v0/management/usage/import` | Import JSONL usage events or legacy JSON snapshots |
| `GET /v0/management/model-prices` | Read SQLite-backed model pricing |
| `PUT /v0/management/model-prices` | Replace saved model pricing |
| `POST /v0/management/model-prices/sync` | Sync model prices from LiteLLM, OpenRouter, and other pricing metadata sources, including source metadata |
| `GET /models`, `GET /v1/models` | Proxy model-list requests to CPA after setup |
| `/v0/management/*` | Proxied to CPA except usage endpoints |

After full Docker setup, `/status`, usage, model-pricing, and `/v0/management/*` proxy endpoints require the admin key as a Bearer token. CPA Management Key is not accepted for Manager Server-only endpoints; it is stored server-side and used only by Manager Server when it talks to the bound CPA upstream.

Usage import accepts two file families: JSONL/NDJSON event files exported by Manager Server, and legacy JSON snapshots produced by older CPA `/usage/export`. Legacy JSON can be converted only when `usage.apis.*.models.*.details[]` request details are present. Files that contain only aggregate totals are rejected because request-level monitoring data cannot be reconstructed. Legacy import is a migration/recovery path, not a perfect continuation of newly collected Manager Server data: old files may miss metadata such as `api_key_hash`, channel, request ID, method/path, latency, cache tokens, or failure reason, so account matching, API Key level analysis, and detail accuracy may be lower. Importing legacy files affects totals, trend charts, and account/key breakdowns; use a test or backup database first when accuracy matters.

Failure bodies from CPA usage events are treated as sensitive diagnostics. Manager Server keeps the raw `fail_body` only in the local SQLite database for internal troubleshooting, while normal APIs, compatible usage payloads, and JSONL exports expose only `fail_summary`, which is redacted and truncated. JSONL exports intentionally omit `raw_json` and raw `fail_body`; imports remain compatible with older exports and snapshots.

## Feature Overview

- **Dashboard**: connection state, backend version, quick health summary
- **Configuration**: visual/source editing for CPA configuration, including Codex `identity-confuse`, and separate CPA Manager Plus configuration
- **AI Providers**: Gemini, Codex, Claude, Vertex, OpenAI-compatible providers, and Ampcode
- **Auth Files**: upload, download, delete, status, OAuth exclusions, model aliases
- **Quota**: quota views for supported providers
- **Request Monitoring**: persisted usage KPIs, model/channel/account/API-key breakdowns, requested vs resolved model tracking, project snapshots, model pricing, estimated token cost, failure analysis, realtime tables with a readable source label and one prioritized supplemental detail
- **Codex Account Inspection**: batch probing and cleanup suggestions for Codex auth pools
- **Logs**: incremental file log reading and filtering
- **System Info**: model list, version checks, and local state tools

## Development

Frontend:

```bash
npm install
npm run dev
npm run type-check
npm run lint
npm run build
```

Manager Server:

```bash
cd apps/manager-server
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/cpa-manager-plus
```

## Build and Release

- Vite builds a single-file `apps/web/dist/index.html`.
- Tagging `vX.Y.Z` or a prerelease tag such as `vX.Y.Z-beta` triggers `.github/workflows/release.yml`.
- The release workflow uploads `apps/web/dist/management.html`, native packages, and `checksums.txt` to GitHub Releases.
- Native packages are published for `linux`, `darwin`, and `windows` on both `amd64` and `arm64`, with the management panel embedded.
- The same workflow builds `Dockerfile.manager-server` and pushes public images to Docker Hub and GitHub Container Registry.
- The Docker image is published for `linux/amd64` and `linux/arm64`.
- The GitHub Container Registry image is `ghcr.io/seakee/cpa-manager-plus`; it uses the workflow `GITHUB_TOKEN` with `packages: write`.
- The workflow syncs `README.md` to the Docker Hub overview when Docker Hub publishing is enabled.
- Optional GitHub secrets for Docker Hub publishing:
  - `DOCKERHUB_USERNAME`
  - `DOCKERHUB_TOKEN`

## Troubleshooting

- **Cannot connect in full Docker mode**: verify the CPA URL from inside the Manager Server container. For host CPA on Linux, use `--add-host=host.docker.internal:host-gateway`.
- **Full Docker mode opens the login form instead of setup**: Manager Server is already configured. Enter the admin key; the CPA URL comes from the server-side configuration.
- **Wrong default CPA URL in first setup**: rebuild the panel with `VITE_DEFAULT_CPA_BASE_URL=<your-cpa-url>` or enter the correct CPA URL manually.
- **Monitoring is empty**: enable CPA usage publishing, verify Manager Server `/status`, and confirm only one consumer is running.
- **Realtime monitoring shows empty `-` request rows after upgrading CPA**: upgrade CPA Manager Plus. CPA `v7.1.39+` sends Pub/Sub control messages; older CPAM builds may store them as empty usage events.
- **`unsupported RESP prefix 'H'`**: upgrade CPA to `v6.10.8+` or keep `USAGE_COLLECTOR_MODE=http` for reverse-proxied HTTP queue access. RESP Pub/Sub/RESP pop modes require the CPA URL to be a container/host direct address for port `8317`, not a regular HTTP reverse-proxy domain.
- **CPA panel still shows the old panel**: verify that CPA **Panel Repository** is `https://github.com/seakee/CPA-Manager-Plus`. If the new panel still does not load, clear CPA's cached panel file and reload or restart CPA:
  ```bash
  rm static/management.html
  ```
- **401 from Manager Server**: Manager Server endpoints use the admin key. CPA Management Key only logs in to CPA and is not accepted for Manager Server-only APIs.
- **Docker panel shows stale data**: check `/status` for `lastConsumedAt`, `lastInsertedAt`, and `lastError`.
- **CPA panel mode is missing monitoring/pricing/imports**: this is expected. These are Full Docker / Manager Server-hosted panel features.
- **Data disappears after container rebuild**: mount `/data` to a Docker volume or host directory.
- **Old data is missing after migrating from CPA-Manager**: verify that the Plus container is mounting the old `/data` volume, not a newly created empty `cpa-manager-plus-data` volume.
- **Admin key is lost**: setting `CPA_MANAGER_ADMIN_KEY` does not overwrite an existing `settings.admin_credential_v1`. Stop Manager Server, back up `/data`, and follow [Reset the Manager Server Admin Key](docs/reset-admin-key.md).
- **Detailed FAQ**: see [FAQ and Troubleshooting](https://github.com/seakee/CPA-Manager-Plus/wiki/CPA-Manager-Plus-FAQ-and-Troubleshooting) or the [Chinese FAQ](https://github.com/seakee/CPA-Manager-Plus/wiki/CPA%E2%80%90Manager-%E5%B8%B8%E8%A7%81%E9%97%AE%E9%A2%98%E4%B8%8E%E8%A7%A3%E5%86%B3%E6%96%B9%E6%A1%88).

## References

- CLIProxyAPI: https://github.com/router-for-me/CLIProxyAPI
- Redis usage queue documentation: https://help.router-for.me/management/redis-usage-queue.html
- Migration from CPA-Manager: [docs/migration-from-cpa-manager.md](docs/migration-from-cpa-manager.md)
- Reset the Manager Server admin key: [docs/reset-admin-key.md](docs/reset-admin-key.md)
- Release checklist: [docs/release-checklist.md](docs/release-checklist.md)

## Acknowledgements

- Thanks to the upstream projects [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) and [Cli-Proxy-API-Management-Center](https://github.com/router-for-me/Cli-Proxy-API-Management-Center) for the foundation and inspiration.
- Thanks to the [Linux.do](https://linux.do/) community for project promotion and feedback.

## License

MIT
