# Docker Deployment

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
- If the installer manages the connection through env/secrets, also back up `secrets/` in the install directory.

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
```

If the monitoring page is empty, continue with [Request Monitoring Troubleshooting](../troubleshooting/request-monitoring.md).

## What Changed From CPA-Manager

Old CPA-Manager Docker docs used `seakee/cpa-manager` and described an external Usage Service for a CPA-hosted panel. In CPA Manager Plus:

- Image is `seakee/cpa-manager-plus`.
- Container is usually named `cpa-manager-plus`.
- Full Docker / Manager Server mode login uses the CPAMP Admin Key, not the CPA Management Key.
- Setup/panel-saved CPA Management Keys are encrypted with `/data/data.key`; installer env/secret mode reads the key from the install directory.
- The CPAMP Lightweight Panel does not configure or attach external Manager Server analytics.
