---
title: CPAMP Lightweight Panel
description: Let CPA host CPA Manager Plus directly on port 8317 as a lightweight enhanced alternative to the official Management Center, without Manager Server.
---

# CPAMP Lightweight Panel

The CPAMP Lightweight Panel is hosted directly by CPA and replaces the official Management Center UI with CPA Manager Plus. It requires no Manager Server, SQLite database, additional container, or new port, making it suitable when you want a different management UI without a heavier deployment.

## Shortest Installation Path

1. Set `panel-github-repository` in CPA `config.yaml`.
2. Confirm that the management panel is not disabled.
3. Restart or reload CPA.
4. Open `http://<cpa-host>:8317/management.html`.
5. Log in with the CPA Management Key.

The rest of this page provides the complete configuration and security notes. Users already familiar with CPA configuration only need the steps above.

```text
Browser
  -> CPA :8317/management.html
      -> CPAMP single-file panel
      -> CPA Management API
```

## When To Use It

- CPA / CLIProxyAPI is already running.
- You prefer a different interface or clearer information hierarchy than the official panel.
- You want to keep a single CPA process and management port.
- Your main tasks are providers, auth files, OAuth, API keys, quota, logs, plugins, and configuration.
- You do not currently need persistent request history, cost analytics, or server-side automation.

## Requirements

- Use a current CPA release; the CPAMP README currently recommends CPA `v7.1.39+`.
- Configure `remote-management.secret-key`; otherwise the Management API and panel are disabled.
- Keep `remote-management.disable-control-panel` set to `false`.
- CPA must be able to reach GitHub Releases, directly or through its configured proxy, to download `management.html`.

## Configure CPA

Edit CPA `config.yaml`:

```yaml
remote-management:
  # Safe default. Change to true only for access from outside localhost.
  allow-remote: false

  # CPA Management Key. Never commit a real key to Git.
  secret-key: 'replace-with-your-management-key'

  # CPA must be allowed to serve the management panel.
  disable-control-panel: false

  # false lets CPA periodically check for panel updates.
  disable-auto-update-panel: false

  # Download management.html from CPAMP Releases.
  panel-github-repository: 'https://github.com/seakee/CPA-Manager-Plus'
```

`allow-remote: true` exposes the remote Management API. Use it only on a trusted network, through a VPN, or behind a protected reverse proxy. Do not expose the management port and key to an untrusted network.

## Start And Log In

1. Save the configuration and restart or reload CPA.
2. Open:

```text
http://<cpa-host>:8317/management.html
```

3. Log in with the CPA Management Key.
4. Confirm that Dashboard, Configuration, Providers, Auth Files, OAuth, Quota, and Logs load correctly.

On first access, CPA checks the latest Release in the configured GitHub repository for an asset named `management.html` and caches it in the CPA working directory.

## Updates And Cache

With `disable-auto-update-panel: false`, CPA periodically checks for a newer panel. If an upgrade still shows the old interface:

1. Confirm that `panel-github-repository` still points to CPA Manager Plus.
2. Stop CPA.
3. Remove the cached panel from the CPA working directory:

```bash
rm static/management.html
```

4. Restart CPA and open `/management.html` again.

With `disable-auto-update-panel: true`, CPA downloads the panel only when the cached file is missing and does not perform periodic updates.

## Lightweight Panel Capabilities

| Capability                                                   | CPAMP Lightweight Panel                             |
| ------------------------------------------------------------ | --------------------------------------------------- |
| CPA config, providers, auth files, OAuth, quota, and logs    | Supported                                           |
| API keys, model aliases, priorities, and plugin management   | Supported when exposed by CPA APIs and plugin paths |
| Browser-local account checks                                 | Supported where the page provides a local workflow  |
| SQLite request history and request monitoring                | Not supported                                       |
| Usage and cost analytics, model prices, and API key aliases  | Not supported                                       |
| Server inspection, quota cooldowns, and account action queue | Not supported                                       |
| Full Mode backup, migration, and maintenance tools           | Not supported                                       |

The lightweight panel does not connect to or read a separately running Manager Server. Starting Manager Server does not add full features to `:8317/management.html`.

## Upgrade To CPAMP Full Mode

When you need request history, cost analytics, or server-side automation:

1. Start Manager Server with [Docker Deployment](./docker.md) or [Native Packages](./native.md).
2. Open:

```text
http://<cpamp-host>:18317/management.html
```

3. Complete setup or log in with the CPAMP Admin Key.
4. Configure the CPA address, CPA Management Key, and request collection.

The lightweight panel may remain on the CPA port, but full capabilities are available only from the Manager Server panel entry.

## Troubleshooting

- Panel does not open: confirm that `secret-key` is non-empty and `disable-control-panel` is `false`.
- Official panel still appears: check `panel-github-repository` spelling and clear the cache.
- Remote access is rejected: check `allow-remote`, firewall, and reverse proxy settings.
- Monitoring is missing: this is expected in Lightweight Mode; use the Manager Server panel.
