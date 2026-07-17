# One-Click Installer

Use the installer for a first deployment, or when CPA is already running and you only want to bring up CPAMP. It does not overwrite existing config files by default. Before it writes files or starts services, it shows a summary and asks for confirmation.

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
5. Generating minimal config files and local secret files.
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
secrets/cpa-management-key
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

CPAMP reads the CPA Management Key from a Docker secret and connects to CPA through the Docker internal URL:

```text
http://cli-proxy-api:8317
```

This connection is managed by `compose.yaml` and `secrets/cpa-management-key` in the install directory. Open the panel and log in with the CPAMP Admin Key; first setup is not required.

After deployment, open:

```text
http://<host>:18317/management.html
```

The script saves the CPAMP Admin Key and prints its file path and view command. Interactive installs can choose whether to reveal the full key in the terminal; do not share terminal screenshots containing it. The demo client API key is only for a quick post-install connectivity check; create named production clients in the panel.

:::

## CPAMP-Only Install

If CPA is already running, choose CPAMP only. The interactive wizard first asks whether you want to enter the CPA URL and CPA Management Key now.

If you choose to enter them now and skip first setup, the installer stores the connection in:

```text
.env
secrets/cpa-management-key
```

After startup, log in with the CPAMP Admin Key; first setup is not required. This is environment-managed configuration: CPA URL and CPA Management Key come from the install directory, and the panel cannot directly replace that connection. To change it, update the install directory config and secret, then restart CPAMP.

If you choose to enter it later, the installer does not write the CPA Management Key into environment-managed config. Open the panel and complete setup with:

```text
CPA URL
CPA Management Key
Request monitoring preference
```

If you want the connection to be managed by files, choose the option that stores the CPA connection in local secret files. In that mode, CPA URL and CPA Management Key come from config files, and the panel cannot directly replace that connection.

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
run.sh
cpa-manager-plus.service  # Linux
cpa-manager-plus.log
cpa-manager-plus.pid
```

The native package is started in the background. On Linux the installer also creates `cpa-manager-plus.service`; copy it into your systemd service directory and enable it according to your host policy. On macOS, or with another process manager, keep using `run.sh` as the integration point.

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
| `CPAMP_CPA_CONNECTION_MODE` | `setup` or `env`.                                                                                                    |
| `CPAMP_CPA_URL`             | CPA URL for `env` mode.                                                                                              |
| `CPAMP_CPA_MANAGEMENT_KEY`  | CPA Management Key for `env` mode.                                                                                   |
| `CPAMP_OPERATION`           | `install`, `upgrade`, `repair`, or `regenerate`. Existing non-interactive deployments require an explicit operation. |
| `CPAMP_PROJECT_NAME`        | Docker Compose project name. Defaults to `cpamp`; use another name for an isolated deployment on the same host.      |

## Rerun And Overwrite

The following `CPAMP_OPERATION` modes apply to Docker deployments. Native packages continue to use their existing version and overwrite options.

Before writing files, the installer checks both the install directory and Docker data volume. When it detects an existing deployment, interactive mode offers:

1. **Upgrade existing deployment**: pull and recreate containers without changing config or secrets.
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

:::

## Startup And Login Verification

After Docker installation, upgrade, or repair, the script waits for CPAMP health and then uses the current admin key against a protected Manager Server endpoint. It reports the install as completed only after both checks pass.

If the container is healthy but the key is rejected, interactive mode offers to stop CPAMP and repair the database credential automatically. Non-interactive mode exits with a failure and instructs the operator to use `CPAMP_OPERATION=repair`. This prevents the installer from presenting a newly generated key that does not match an existing database.
