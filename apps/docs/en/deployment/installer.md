# One-Click Installer

Use the installer for a first deployment, or when CPA is already running and you only want to bring up CPAMP. It does not overwrite existing config files by default. Before it writes files or starts services, it shows a summary and asks for confirmation.

When the installer receives a CPA URL and CPA Management Key, they are one-time import inputs. The installer runs `store-cpa-connection`, encrypts the connection into CPAMP's SQLite configuration with `data.key`, and removes the temporary key file only after health, admin-auth, and proxied CPA Management API checks succeed. The final runtime configuration no longer contains `CPA_UPSTREAM_URL`, `CPA_MANAGEMENT_KEY_FILE`, or a long-lived CPA-key secret mount.

Most users only need four steps: run the script, choose the install scope, choose Docker or a native package, and confirm the summary. After installation, use the address and key printed by the installer.

## Run It

Download the script, then run it:

```bash
curl -fsSLO https://raw.githubusercontent.com/seakee/CPA-Manager-Plus/main/bin/install-cpamp.sh
bash install-cpamp.sh
```

If you want to inspect it first:

```bash
less install-cpamp.sh
bash install-cpamp.sh
```

The wizard walks through:

1. Detecting OS, architecture, WSL, ports, and required commands.
2. Choosing the operation language.
3. Choosing the install scope: CPA + CPAMP, or CPAMP only.
4. Choosing the deployment method: Docker, or CPAMP native package.
5. Generating minimal config files and local secret files (the CPA Management Key file is temporary and used only during import).
6. Showing a summary so you can confirm, modify, or abort.
7. Running the install only after confirmation.

## Supported Combinations

| Install scope |    Docker |    Native package |
| ------------- | --------: | ----------------: |
| CPA + CPAMP   | Supported | Not supported yet |
| CPAMP only    | Supported |         Supported |

Use Docker for a full CPA + CPAMP install. The CPAMP native package contains Manager Server only; CPA must already be deployed separately.

## Full Docker Install

Choose this when CPA is not installed yet. The installer starts both CPA and CPAMP and prepares persistent storage and login keys.

::: details Generated files and connection behavior

When you choose CPA + CPAMP, the script generates:

```text
compose.yaml
.env
secrets/cpamp-admin-key
secrets/cpa-management-key       # temporary import input
secrets/cpa-demo-client-key
cliproxyapi/config.yaml
cliproxyapi/auths/
cliproxyapi/logs/
```

Generated keys use these formats by default:

```text
CPAMP Admin Key: cpamp_ + 32 alphanumeric characters
CPA Management Key: cpa_ + 32 alphanumeric characters
Demo client API key: sk- + 64 alphanumeric characters
```

When rerun, the installer reuses existing non-empty single-line secret files as-is, so manually managed keys do not have to match the default generated format.

The CPA minimal config enables remote management and usage publishing:

```yaml
api-keys:
  - 'sk-...'

remote-management:
  secret-key: 'cpa_...'
  allow-remote: true

usage-statistics-enabled: true
redis-usage-queue-retention-seconds: 60
```

The generated Compose file uses the paths expected by the CPA image:

```text
./cliproxyapi/config.yaml -> /CLIProxyAPI/config.yaml
./cliproxyapi/auths       -> /root/.cli-proxy-api
./cliproxyapi/logs        -> /CLIProxyAPI/logs
```

CPA hashes a plaintext `remote-management.secret-key` back into `cliproxyapi/config.yaml` on startup, so that file must remain writable.

Before CPAMP starts, the installer performs one offline import and uses the Docker internal URL:

```text
http://cli-proxy-api:8317
```

The import encrypts the CPA URL and CPA Management Key into `/data/usage.sqlite`, protected by `/data/data.key`. After the install passes health, admin-auth, and proxied CPA Management API checks, `secrets/cpa-management-key` is deleted; the final `compose.yaml` no longer passes the CPA Key to Manager Server. Open the panel and log in with the CPAMP Admin Key; first setup is not required.

After deployment, open:

```text
http://<host>:18317/management.html
```

The script saves the CPAMP Admin Key and prints its file path and view command. Interactive installs can choose whether to reveal the full key in the terminal; do not share terminal screenshots containing it. The demo client API key is only for a quick post-install connectivity check; create named production clients in the panel.

:::

## CPAMP-Only Install

If CPA is already running, choose CPAMP only. The interactive wizard first asks whether you want to enter the CPA URL and CPA Management Key now.

If you choose to enter them now and skip first setup, the installer imports the connection into SQLite once:

```text
.env                    # non-sensitive runtime settings only
data/usage.sqlite       # encrypted Manager configuration
data/data.key
```

After startup, log in with the CPAMP Admin Key; first setup is not required. After import, CPAMP reads the CPA URL and CPA Management Key from encrypted SQLite; the panel never returns the saved CPA Key to the browser. To change the connection, submit a new CPA Key in the panel. The offline import command accepts only a first import or an identical idempotent retry and refuses to overwrite a conflicting connection.

If you choose to enter it later, the installer does not write the CPA Management Key into environment-managed config. Open the panel and complete setup with:

```text
CPA URL
CPA Management Key
Request monitoring preference
```

Manual deployments may still use `CPA_UPSTREAM_URL`, `CPA_MANAGEMENT_KEY`, or `CPA_MANAGEMENT_KEY_FILE`, but that is not the installer's final state; the installer migrates those inputs into encrypted SQLite. With `CPAMP_SKIP_EXECUTE=1`, it does not run the import or delete the temporary file; it prints the import and cleanup commands for manual review.

For CPAMP-only Docker installs where CPA runs on the same host, the installer defaults to:

```text
http://host.docker.internal:8317
```

On Linux it also writes `host.docker.internal:host-gateway`, so the container can reach the host CPA process. If CPA runs on another machine, use that address instead.

## Native Package Mode

For CPAMP-only installs, you can choose the native package. The script downloads the matching GitHub Release asset for your OS and architecture, then creates:

```text
runtime/<package>/
data/
secrets/cpamp-admin-key
secrets/cpa-management-key       # deleted after import
run.sh
cpa-manager-plus.service  # Linux
cpa-manager-plus.log
cpa-manager-plus.pid
```

The native package imports the CPA connection offline before starting in the background. The installer verifies health, admin authentication, and a CPA Management API request proxied through CPAMP, then removes the temporary `secrets/cpa-management-key` only after all checks pass. On Linux it also creates `cpa-manager-plus.service`; copy it into your systemd service directory and enable it according to your host policy. On macOS, or with another process manager, keep using `run.sh` as the integration point.

::: details Automation, reruns, and repair

## Advanced Usage

Preview the plan without writing files or starting services:

```bash
CPAMP_DRY_RUN=1 bash install-cpamp.sh
```

Generate config but skip startup:

```bash
CPAMP_SKIP_EXECUTE=1 bash install-cpamp.sh
```

This mode keeps the temporary CPA Key and prints manual commands in the order import -> start -> health check -> admin authentication -> proxied CPA Management API check -> temporary-key removal. For a Docker upgrade that still uses legacy env/secret wiring, the script leaves the runtime config untouched and prints one full upgrade command without `CPAMP_SKIP_EXECUTE`; the normal upgrade flow then owns backup, rollback, and final cleanup.

Non-interactive full Docker install:

```bash
CPAMP_NON_INTERACTIVE=1 \
CPAMP_CONFIRM=1 \
CPAMP_LANG=en-US \
CPAMP_INSTALL_MODE=stack \
CPAMP_DEPLOY_METHOD=docker \
CPAMP_INSTALL_DIR="$HOME/cpa-manager-plus" \
bash install-cpamp.sh
```

Common variables:

| Variable                    | Description                                                                                                          |
| --------------------------- | -------------------------------------------------------------------------------------------------------------------- |
| `CPAMP_LANG`                | `zh-CN` or `en-US`.                                                                                                  |
| `CPAMP_INSTALL_MODE`        | `stack` or `cpamp`.                                                                                                  |
| `CPAMP_DEPLOY_METHOD`       | `docker` or `native`.                                                                                                |
| `CPAMP_INSTALL_DIR`         | Install directory. Defaults to `~/cpa-manager-plus`.                                                                 |
| `CPAMP_PORT`                | Public CPAMP port. Defaults to `18317`.                                                                              |
| `CPAMP_CPA_PORT`            | Public CPA port for full Docker install. Defaults to `8317`.                                                         |
| `CPAMP_IMAGE`               | CPAMP Docker image.                                                                                                  |
| `CPAMP_CPA_IMAGE`           | CPA Docker image.                                                                                                    |
| `CPAMP_VERSION`             | Native package version. Defaults to `latest`.                                                                        |
| `CPAMP_CPA_CONNECTION_MODE` | `setup` or `env`; `env` means one-time import input from the environment/prompt.                                     |
| `CPAMP_CPA_URL`             | CPA URL for `env` mode.                                                                                              |
| `CPAMP_CPA_MANAGEMENT_KEY`  | One-time CPA Management Key input for `env` mode; not retained in the final Compose config.                          |
| `CPAMP_OPERATION`           | `install`, `upgrade`, `repair`, or `regenerate`. Existing non-interactive deployments require an explicit operation. |
| `CPAMP_PROJECT_NAME`        | Docker Compose project name. Defaults to `cpamp`; use another name for an isolated deployment on the same host.      |

## Rerun And Overwrite

The following `CPAMP_OPERATION` modes apply to Docker deployments. Native packages continue to use their existing version and overwrite options.

Before writing files, the installer checks both the install directory and Docker data volume. When it detects an existing deployment, interactive mode offers:

1. **Upgrade existing deployment**: pull and recreate containers; if a legacy env/secret CPA connection is found, import it into SQLite and remove the old runtime fields.
2. **Repair admin login**: stop CPAMP, synchronize the SQLite admin credential with `secrets/cpamp-admin-key`, restart, and verify login. CPA and application data are not deleted.
3. **Regenerate deployment config**: back up generated config before replacing it while preserving secrets and the data volume.
4. **Exit**.

If the install directory was deleted but `cpamp_cpa-manager-plus-data` still exists, the installer no longer silently generates a new key and reports success. It requires either recovery of the old data or a fresh install with a different Compose project name.

Non-interactive upgrade:

```bash
CPAMP_OPERATION=upgrade \
CPAMP_NON_INTERACTIVE=1 \
CPAMP_CONFIRM=1 \
bash install-cpamp.sh
```

Non-interactive admin-login repair:

```bash
CPAMP_OPERATION=repair \
CPAMP_NON_INTERACTIVE=1 \
CPAMP_CONFIRM=1 \
bash install-cpamp.sh
```

If the install directory is gone and only the old Docker volume remains, non-interactive repair must also set the original `CPAMP_INSTALL_MODE=stack` or `CPAMP_INSTALL_MODE=cpamp` so the installer does not generate the wrong service combination.

To regenerate deployment config:

```bash
CPAMP_OPERATION=regenerate bash install-cpamp.sh
```

`CPAMP_OVERWRITE=1` remains compatible with the old workflow and maps to config regeneration. The installer backs up the previous `.env`, `compose.yaml`, CPA config, `run.sh`, and service file under `backups/installer-*`. You should still separately back up `secrets/`, `data/`, and `cliproxyapi/`. If `data.key` is lost, stored CPA Management Keys cannot be recovered.

When upgrading a legacy env/secret Docker deployment, the installer first stops Manager Server and creates a protected offline snapshot inside the data volume. The snapshot records the original presence and contents of `usage.sqlite`, `-wal`, `-shm`, `-journal`, and `data.key`. Import and any SQLite schema migration start only after the complete snapshot exists. The snapshot is deleted only after health, admin authentication, and CPA proxy validation all pass. On failure, the installer stops the replacement process, restores the data and old config, and then starts the previous service. A snapshot whose restore fails is retained and its path is reported for manual recovery.

The installer also keeps timestamped `compose.yaml.cpa-key-migration.bak.*` and `.env.cpa-key-migration.bak.*` files. Legacy CPA inputs are resolved only from sources actually referenced by the `cpa-manager-plus` Compose service, with the process environment overriding `.env`; unreferenced legacy `.env` declarations and stale secret files do not trigger migration. The installer may restrict and remove its managed `secrets/cpa-management-key`. An external `CPA_MANAGEMENT_KEY_FILE` is read only: its contents, permissions, and file remain unchanged.

Native-package upgrades likewise back up SQLite sidecars and `data.key` before switching runtime entry files. The new binary sanitizes the old `config.json` with Go's JSON parser, deleting only `cpaUpstreamUrl` and `managementKeyFile` while preserving unknown fields. The common exit handler also rolls back unhandled shell failures.

:::

## Startup And Login Verification

After Docker installation, upgrade, or repair, and after native-package installation, the script waits for CPAMP health and then uses the current admin key against a protected Manager Server endpoint. For a CPA connection imported in this run, it also requests the CPA management configuration through CPAMP's server-side proxy to confirm that the URL and key actually work. It reports the install as completed only after all checks pass, then removes the temporary CPA Key file used for import.

If import, health, admin-key verification, or the proxied CPA connection check fails, the temporary CPA Key file remains available for a retry; legacy upgrades also restore the previous config. If the container is healthy but the admin key is rejected, interactive mode offers to stop CPAMP and repair the database credential automatically. Non-interactive mode exits with a failure and instructs the operator to use `CPAMP_OPERATION=repair`. This prevents the installer from presenting a newly generated key that does not match an existing database or deleting an unverified CPA Key.
