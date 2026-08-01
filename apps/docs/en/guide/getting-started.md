# Quick Start

Choose a path based on what you need. Both use the CPAMP interface, but installation and available capabilities differ.

| Your situation                                                      | Recommended path                                             |
| ------------------------------------------------------------------- | ------------------------------------------------------------ |
| CPA already runs and you only want a clearer management UI          | [CPAMP Lightweight Panel](#path-1-install-lightweight-panel) |
| You need request history, cost analytics, inspection, or automation | [CPAMP Full Mode](#path-2-install-full-mode)                 |
| You are not sure                                                    | Read [Choosing A Panel](./choosing-a-panel.md)               |

## Path 1: Install Lightweight Panel

Use this when CPA already runs and you only want to replace the official Management Center. It needs no additional service, database, or port.

1. Set CPA `panel-github-repository` to `seakee/CPA-Manager-Plus`.
2. Restart or reload CPA.
3. Open:

```text
http://<cpa-host>:8317/management.html
```

Log in with the CPA Management Key. See [Install Lightweight Panel](../deployment/cpa-panel.md) for the complete configuration and update instructions.

## Path 2: Install Full Mode

Full Mode runs Manager Server for request history, cost analytics, server-side inspection, and automation. The installer is the recommended path for most users:

```bash
curl -fsSLO https://raw.githubusercontent.com/seakee/CPA-Manager-Plus/main/bin/install-cpamp.sh
bash install-cpamp.sh
```

In the installer:

1. Install scope: choose “CPA + CPAMP” if CPA is not installed, or “CPAMP only” if CPA already runs.
2. Deployment method: prefer Docker, or choose a native package when Docker is not used.
3. Review the summary and confirm deployment.

After installation, open:

```text
http://<host>:18317/management.html
```

Log in with the CPAMP Admin Key saved or printed by the installer. See [Install Full Mode](../deployment/installer.md) for details.

## First CPA Connection

If the installer did not save a CPA connection, enter these values when Full Mode opens for the first time:

1. CPAMP Admin Key.
2. CPA URL, for example `http://cli-proxy-api:8317`.
3. CPA Management Key.
4. Keep request monitoring in the default automatic mode.

## Confirm That It Works

### Lightweight Panel

- You can log in with the CPA Management Key.
- CPA management features such as providers, auth files, OAuth, quota, logs, and plugins load normally.
- The page remains on CPA `:8317/management.html`.

### Full Mode

- You can log in to `:18317/management.html` with the CPAMP Admin Key.
- Dashboard shows that CPA is connected.
- Monitoring shows an event after a real request passes through CPA.
- Usage Analytics shows the corresponding tokens and estimated cost.

If Full Mode opens but has no request data, see [Monitoring Has No Data](../troubleshooting/request-monitoring.md).

## What To Do Next

- Add model services: [AI Providers](../manual/ai-providers.md)
- Log in or import accounts: [OAuth Login](../manual/oauth.md) and [Auth Files](../manual/auth-files.md)
- Configure clients: [Client Configuration](../gateway/clients.md)
- Upgrade and back up: [Upgrade CPAMP](../operations/update.md) and [Backup And Restore](../operations/backup.md)
- Maintain deployment manually: [Docker Deployment](../deployment/docker.md) or [Native Packages](../deployment/native.md)
