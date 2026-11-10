# Docker Deployment

## v2: Single-Image Installation

::: warning Choose the correct channel
This section covers v2 single-image deployment. The existing 1.x single-service and separate CPA examples below remain **1.x Stable** instructions; `bin/install-cpamp.sh` also still installs 1.x by default. Do not mount an existing 1.x volume directly into v2: read [Upgrade And Rollback](../operations/update.md) first.
:::

### Build Targets: `full` vs `slim`

The root `Dockerfile` provides two targets sharing byte-identical `cpamp-launcher`, `cpamp-runtime-supervisor`, `cpa-manager-plus`, and `cpamp-ingress` binaries:

| Target | Bundled components | Current S1 support |
| --- | --- | --- |
| `full` (current Compose default) | All CPAMP components + pinned official CPA `v8.0.55` | Completes **Fresh New / New Managed** offline as well as **External** |
| `slim` | CPAMP components only (no `cli-proxy-api` binary) | Supports **External**, or prepares the pinned CPA in the setup wizard (**Fresh New** and **Adopt** on the review step, **Upgrade Managed** at the `cpa-import` step; via official GitHub, a CDN prefix, or upload, with SHA-256 digest verification) |

### Run With `docker run` Or Docker Compose

Inside the single container, `cpamp-launcher` supervises `Runtime Supervisor` (`127.0.0.1:9081`), `Manager` (`127.0.0.1:18319`), `Ingress` (public `0.0.0.0:18317`, control `127.0.0.1:9082`), and managed `CPA` (`127.0.0.1:8317`), exposing only port `18317` and a single `/data` volume:

**Option 1: `docker run`**

```bash
docker build --target full \
  --build-arg VERSION=dev \
  --build-arg SOURCE_COMMIT="$(git rev-parse HEAD)" \
  -t cpa-manager-plus:full-local .
docker run -d \
  --name cpa-manager-plus \
  -p 18317:18317 \
  -v cpamp-data:/data \
  --stop-timeout 150 \
  -e CPAMP_PUBLIC_ORIGIN='https://cpamp.example.com' \
  cpa-manager-plus:full-local
```

**Option 2: `docker compose`**

```bash
export CPAMP_PUBLIC_ORIGIN='https://cpamp.example.com'
export VERSION=dev
export SOURCE_COMMIT="$(git rev-parse HEAD)"
docker compose config
docker compose up -d --build
```

Preserve the single-container security boundary: no Docker socket mount, privileged mode, host networking, or published internal loopback ports; do not use `--pid=host`, `--pid=container:...`, `--init`, or Compose `pid:` / `init: true`, and do not mount `/proc` with `hidepid` (`cpamp-launcher` must run as PID 1 in a private container PID namespace with an unfiltered `/proc` procfs view, and container UID `10001` is reserved for the managed CPA process). Back up the `/data` volume as a complete unit; see [Backup And Restore](../operations/backup.md). Configure graceful drain duration via `CPAMP_DRAIN_TIMEOUT` (default `90s`) and ensure `--stop-timeout` / `stop_grace_period >= CPAMP_DRAIN_TIMEOUT + 60s` (default `150s`).

### Initialize And Claim The Installation

Configure the real public origin reachable from the deployment environment. **Prefer an HTTPS origin behind a configured TLS reverse proxy**, for example `https://cpamp.example.com`; replace it with your actual domain and configure the proxy/certificate first. Plain HTTP is only for trusted local/isolated test networks, never for sending Handoff, Admin Key, or Client Key over untrusted networks. Set `CPAMP_PUBLIC_PORT` as well if the host port differs. The origin is not CPA's internal URL.

```bash
docker compose exec -T -u 10002:10002 cpa-manager-plus cpa-manager-plus initialize
# Or when started via docker run:
# docker exec -u 10002:10002 cpa-manager-plus cpa-manager-plus initialize
```

`initialize` is a one-shot host-authorized operation. It prints `/.well-known/cpamp-bootstrap/ui#handoff=...`. Append the **entire output, including the fragment**, to your public origin and open it in a browser. This short-lived claim credential must not enter screenshots or service logs. To reissue/resume, stop the writable Manager first, run `docker compose run --rm --no-deps -u 10002:10002 --entrypoint cpa-manager-plus cpa-manager-plus initialize`, then `up -d` again; do not delete the database to reset installation.

In the browser, set and save the CPAMP Admin Key, then choose **New Managed** (new managed CPA; on `slim`, the wizard prepares the pinned CPA engine before Managed setup completes), **Adopt** (qualify and migrate an existing CPA; on `slim`, the wizard also prepares the pinned CPA engine first), or **External** (connect to an independently operated CPA, supported on both `full` and `slim`). Adopt/External are not unconditional success paths: resolve the wizard's source, version, credential, or evidence refusals and stop if admission fails; do not edit installation records manually. External requires at least CPA `v8.0.55`; see [Runtime Model](../guide/runtime-model.md).

For New Managed, save the one-time first Client Key and verify model requests through the public entry before sending production traffic. Admin Key, CPA Management Key, and Client Key are different credentials. A successful process `/health` is not installation completion or Gateway readiness: require the wizard's completion, routing verification, and client connectivity.
## 1.x Stable: Manual Deployment Reference


Docker is the simplest path for a new deployment. The CPAMP image contains Manager Server and the embedded `management.html` panel. CPA / CLI Proxy API is still a separate service, and can run in the same Compose stack.

If you want the script to check the environment and generate Compose files for you, start with [One-Click Installer](./installer.md). The rest of this page is for manual Compose maintenance or merging CPAMP into an existing deployment.

For new deployments, use the Manager Server-hosted panel:

```text
http://<host>:18317/management.html
```

Do not carry over the old CPA-Manager "CPA panel + External Usage Service URL" workflow. In Plus, the full feature set comes from Manager Server. The CPAMP Lightweight Panel is an independent UI choice hosted by CPA and does not connect to or read Manager Server SQLite monitoring data.

## Choose Your Scenario

| Your environment                         | Recommended action                                |
| ---------------------------------------- | ------------------------------------------------- |
| Neither CPA nor CPAMP is installed       | Use the [One-Click Installer](./installer.md)     |
| CPA already runs and you need Full Mode  | Jump to [Deploy CPAMP Only](#deploy-cpamp-only)   |
| You maintain your own Compose file       | Use the CPA + CPAMP example on this page          |
| You only want to replace the official UI | Use the [CPAMP Lightweight Panel](./cpa-panel.md) |

If you do not need custom networking, images, or Compose integration, use the installer and skip the advanced sections on this page.

## Requirements

Before deployment, confirm:

- A running CPA / CLI Proxy API instance, or a plan to start CPA in the same Compose stack.
- CPA Management API enabled.
- A CPA Management Key.
- Persistent `/data` storage mounted and backed up.
- Exactly one CPAMP Manager Server consuming one CPA usage queue.

Recommended CPA version:

```text
v7.1.39+
```

Minimum for HTTP usage queue:

```text
v6.10.8+
```

CPA must allow Manager Server to access the Management API:

```yaml
remote-management:
  secret-key: 'your CPA Management Key'
  allow-remote: true
```

Request monitoring depends on CPA usage publishing:

```yaml
usage-statistics-enabled: true
```

CPAMP can also enable this during first setup or config save.

The environment-variable examples on this page are for manual Docker deployments and legacy compatibility. The one-click installer does not leave the CPA URL or CPA Management Key in the final Compose environment; it runs `store-cpa-connection` once and encrypts the connection into SQLite with `/data/data.key`. The configuration API also returns only `managementKeyConfigured`, never the saved CPA Key.

## Deploy CPA And CPAMP Together

If CPA is not running yet, start CPA and CPAMP with this Compose file:

```yaml
services:
  cli-proxy-api:
    image: eceasy/cli-proxy-api:latest
    container_name: cli-proxy-api
    restart: unless-stopped
    ports:
      - '8317:8317'
    volumes:
      - cpa-data:/app/data

  cpa-manager-plus:
    image: seakee/cpa-manager-plus:latest
    container_name: cpa-manager-plus
    restart: unless-stopped
    ports:
      - '18317:18317'
    environment:
      HTTP_ADDR: '0.0.0.0:18317'
      USAGE_DB_PATH: '/data/usage.sqlite'
      CPA_MANAGER_DATA_KEY_PATH: '/data/data.key'
      # Recommended for managed deployments:
      # CPA_MANAGER_ADMIN_KEY: "replace-with-a-long-random-admin-key"
      USAGE_COLLECTOR_MODE: 'auto'
      USAGE_BATCH_SIZE: '100'
      USAGE_POLL_INTERVAL_MS: '500'
      USAGE_QUERY_LIMIT: '50000'
    volumes:
      - cpa-manager-plus-data:/data
    depends_on:
      - cli-proxy-api
    healthcheck:
      test: ['CMD', 'wget', '-qO-', 'http://127.0.0.1:18317/health']
      interval: 10s
      timeout: 3s
      retries: 3

volumes:
  cpa-data:
  cpa-manager-plus-data:
```

```bash
docker compose up -d
```

Open:

```text
http://<host>:18317/management.html
```

On first setup, enter:

```text
Admin Key:          cpamp_... from startup logs or the secret file
CPA URL:            http://cli-proxy-api:8317
CPA Management Key: CPA remote-management.secret-key
```

If `CPA_MANAGER_ADMIN_KEY` is not set, CPAMP generates an admin key and prints it once in the startup log:

```bash
docker compose logs cpa-manager-plus
```

After setup, new browsers log in with the CPAMP Admin Key. The CPA Management Key is encrypted and stored server-side.

## Deploy CPAMP Only

If CPA is already running, start only CPAMP:

```bash
docker run -d \
  --name cpa-manager-plus \
  --restart unless-stopped \
  -p 18317:18317 \
  -v cpa-manager-plus-data:/data \
  seakee/cpa-manager-plus:latest
```

You can also use the GHCR image:

```text
ghcr.io/seakee/cpa-manager-plus:latest
```

## CPA URL Examples

| Scenario                                       | CPA URL to enter during CPAMP setup                                                    |
| ---------------------------------------------- | -------------------------------------------------------------------------------------- |
| CPA and CPAMP in the same Compose network      | `http://cli-proxy-api:8317`                                                            |
| CPA runs on Docker Desktop host                | `http://host.docker.internal:8317`                                                     |
| CPA runs on Linux host, CPAMP in Docker        | `http://host.docker.internal:8317` plus `--add-host=host.docker.internal:host-gateway` |
| CPA is remote and only suitable for HTTP queue | `https://your-cpa.example.com`                                                         |

Linux host CPA example:

```bash
docker run -d \
  --name cpa-manager-plus \
  --restart unless-stopped \
  --add-host=host.docker.internal:host-gateway \
  -p 18317:18317 \
  -v cpa-manager-plus-data:/data \
  seakee/cpa-manager-plus:latest
```

Then use:

```text
http://host.docker.internal:8317
```

Do not use `127.0.0.1` from inside a container to reach CPA on the host. Inside the container, `127.0.0.1` means the container itself.

::: details Advanced: common environment variables

## Common Environment Variables

| Variable                     | Default                           | Description                                      |
| ---------------------------- | --------------------------------- | ------------------------------------------------ |
| `HTTP_ADDR`                  | `0.0.0.0:18317`                   | Manager Server listen address.                   |
| `USAGE_DATA_DIR`             | `/data`                           | Data directory.                                  |
| `USAGE_DB_PATH`              | `/data/usage.sqlite`              | SQLite database path.                            |
| `CPA_MANAGER_DATA_KEY_PATH`  | `/data/data.key`                  | Data key path.                                   |
| `CPA_MANAGER_ADMIN_KEY`      | empty                             | Explicit Manager Server admin key.               |
| `CPA_MANAGER_ADMIN_KEY_FILE` | `/run/secrets/cpa_admin_key`      | Read the admin key from a file.                  |
| `CPA_MANAGER_DATA_KEY`       | empty                             | Explicit data encryption key.                    |
| `CPA_MANAGER_DATA_KEY_FILE`  | `/run/secrets/cpa_data_key`       | Read the data encryption key from a file.        |
| `CPA_UPSTREAM_URL`           | empty                             | Optional environment-managed CPA URL.            |
| `CPA_MANAGEMENT_KEY`         | empty                             | Optional environment-managed CPA Management Key. |
| `CPA_MANAGEMENT_KEY_FILE`    | `/run/secrets/cpa_management_key` | Read the CPA Management Key from a file.         |
| `USAGE_COLLECTOR_MODE`       | `auto`                            | `auto`, `subscribe`, `http`, or `resp`.          |
| `USAGE_BATCH_SIZE`           | `100`                             | Max collected records per batch.                 |
| `USAGE_POLL_INTERVAL_MS`     | `500`                             | Idle poll interval.                              |
| `USAGE_QUERY_LIMIT`          | `50000`                           | Max recent usage events returned.                |

For the full runtime reference, see [Manager Server Guide](../operations/manager-server.md).

:::

## Data Persistence And Backup

Always mount `/data`. Docker defaults:

```text
/data/usage.sqlite
/data/usage.sqlite-wal
/data/usage.sqlite-shm
/data/data.key
```

Backups must include both SQLite files and `data.key`:

```bash
docker run --rm \
  -v cpa-manager-plus-data:/data \
  -v "$PWD":/backup \
  alpine \
  tar czf /backup/cpa-manager-plus-data-backup.tar.gz -C /data .
```

Why `data.key` matters:

- `usage.sqlite` stores usage data and encrypted CPAMP configuration.
- `data.key` decrypts CPA Management Keys saved to SQLite through setup or the panel.
- If `data.key` is lost, CPA Management Keys saved to SQLite cannot be recovered; save the CPA connection again.
- If this is a manual env/secret deployment, or the installer has not completed/skipped the CPA connection import, also back up `secrets/` in the install directory.

## Read-Only Root Filesystem

Standard CPAMP Docker/Compose deployments use a writable root filesystem and require no additional temporary-volume configuration.

If you harden the Manager Server container with `readOnlyRootFilesystem: true`, SQLite still needs a writable temporary directory. For Kubernetes, mount a writable ephemeral volume at `/tmp`:

```yaml
volumeMounts:
  - name: tmp
    mountPath: /tmp

volumes:
  - name: tmp
    emptyDir: {}
```

If you set an `emptyDir.sizeLimit`, size it for your database size and workload. On Unix-like environments you can alternatively point `SQLITE_TMPDIR` at a writable ephemeral path.

The database file and its WAL/SHM sidecars must also be writable by the Manager Server process. CPAMP does not automatically change file ownership or permissions, and does not relocate SQLite temporary files.

::: details Advanced: collection protocols and network requirements

## Collection Paths

When `USAGE_COLLECTOR_MODE=auto`, Manager Server tries these paths in order:

1. RESP Pub/Sub.
2. HTTP usage queue.
3. RESP pop fallback.

RESP Pub/Sub and RESP pop must connect directly to the CPA API port, usually `8317`. Normal HTTP reverse proxies do not work for RESP. HTTP usage queue can go through an HTTP proxy.

If you see `unsupported RESP prefix 'H'`, the RESP collector is probably connected to an HTTP address. Prefer `auto` or `http`, and confirm CPA is at least `v6.10.8+`.

:::

## Upgrade

Back up `/data` first.

Compose:

```bash
docker compose pull
docker compose up -d
```

`docker run`:

```bash
docker pull seakee/cpa-manager-plus:latest
docker stop cpa-manager-plus
docker rm cpa-manager-plus
docker run -d \
  --name cpa-manager-plus \
  --restart unless-stopped \
  -p 18317:18317 \
  -v cpa-manager-plus-data:/data \
  seakee/cpa-manager-plus:latest
```

### Database Maintenance Is Pending After Upgrade

To keep HTTP startup bounded on large historical databases, Manager Server does not run unbounded `CREATE INDEX` or derived cleanup before listening. If the global warning, System Info, or `/status.databaseMaintenance.required` reports pending maintenance, collection and HTTP remain available, but historical request queries can become noticeably slower or time out.

Run these commands from the Compose directory:

```bash
docker compose stop cpa-manager-plus

docker compose run --rm --no-deps \
  cpa-manager-plus \
  cleanup-derived --db-path /data/usage.sqlite

docker compose start cpa-manager-plus
```

Replace `cpa-manager-plus` if your Compose service uses another name. Do not run cleanup while Manager Server is still active, and do not trigger it from the web UI; the command requires the exclusive SQLite process lock. After restart, Manager Server derives the state from database metadata again, so the warning disappears automatically when maintenance is clean.

`cleanup-derived` handles derived cleanup, deferred indexes, and legacy index replacement only. It never deletes, rebuilds, or rewrites authoritative `usage_events`. Back up `/data` and `data.key` first.

## Verification

Basic health checks:

```bash
curl http://127.0.0.1:18317/health
curl http://127.0.0.1:18317/usage-service/info
```

After setup, check collector status:

```bash
curl -H "Authorization: Bearer <CPAMP_ADMIN_KEY>" \
  http://127.0.0.1:18317/status
```

Important fields:

```text
configured
collector.lastError
lastConsumedAt
lastInsertedAt
eventCount
databaseMaintenance.required
databaseMaintenance.deferredIndexes
databaseMaintenance.offlineJobs
```

`databaseMaintenance.required=true` means the service is running but query performance may be degraded. Follow the offline steps above. After maintenance and restart, it should be `false`, with both counts at `0`.

If the monitoring page is empty, continue with [Request Monitoring Troubleshooting](../troubleshooting/request-monitoring.md).

## What Changed From CPA-Manager

Old CPA-Manager Docker docs used `seakee/cpa-manager` and described an external Usage Service for a CPA-hosted panel. In CPA Manager Plus:

- Image is `seakee/cpa-manager-plus`.
- Container is usually named `cpa-manager-plus`.
- Full Docker / Manager Server mode login uses the CPAMP Admin Key, not the CPA Management Key.
- Setup/panel-saved CPA Management Keys are encrypted with `/data/data.key`; installer env/secret input is one-time import data and is removed from the final runtime configuration after success.
- The CPAMP Lightweight Panel does not configure or attach external Manager Server analytics.
