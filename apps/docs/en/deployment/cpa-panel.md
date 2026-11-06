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
- Your main tasks are providers, credentials and account health, OAuth, API keys, logs, plugins, and configuration.
- You do not currently need persistent request history, cost analytics, or server-side automation.

## Requirements

- The 1.x Stable panel retains its existing CPA compatibility range. v2 Beta panel acceptance targets pinned official CPA `v8.0.55`, and External Full Mode also requires at least `v8.0.55`; older README recommendations do not prove Beta qualification.
- Configure `remote-management.secret-key`; otherwise the Management API and panel are disabled.
- Keep `remote-management.disable-control-panel` set to `false`.
- CPA must be able to reach GitHub Releases, directly or through its configured proxy, to download `management.html`.
- CPA v8 works: the v2 Beta lightweight panel runs on the `/v0/management` API that CPA v8 still serves; see "v2 Beta And The Lightweight Panel" below.

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

CPA v8 also reads its new configuration layout: `config-version: 8` with a top-level `management:` section holding the same fields as `remote-management` above. Both spellings have been verified on CPA v8.

## Start And Log In

1. Save the configuration and restart or reload CPA.
2. Open:

```text
http://<cpa-host>:8317/management.html
```

3. Log in with the CPA Management Key.
4. Confirm that Dashboard, Configuration, Providers, Accounts, OAuth, and Logs load correctly.

CPA looks for an asset named `management.html` in `releases/latest` of the configured GitHub repository (the newest non-prerelease Release) and caches it as `static/management.html`, in the `static/` folder next to CPA's `config.yaml` (`MANAGEMENT_STATIC_PATH` or `WRITABLE_PATH`, when set, take precedence).

## Updates And Cache

With `disable-auto-update-panel: false`, CPA periodically checks for a newer panel. If an upgrade still shows the old interface:

1. Confirm that `panel-github-repository` still points to CPA Manager Plus.
2. Stop CPA.
3. Remove the cached panel from the `static/` folder next to CPA's `config.yaml`:

```bash
rm static/management.html
```

4. Restart CPA and open `/management.html` again.

With `disable-auto-update-panel: true`, CPA downloads the panel only when the cached file is missing and does not perform periodic updates.

## v2 Beta And The Lightweight Panel

This is the CPAMP v2 Beta publication contract for the lightweight panel. Until the first v2 Beta prerelease is published, the Releases page has no matching file.

- **`releases/latest` stays on 1.x.** The v2 Beta is published as a GitHub prerelease, and GitHub's `releases/latest` excludes prereleases. CPA reads only `releases/latest`, so a CPA configured with `panel-github-repository` as on this page keeps downloading and updating the 1.x lightweight panel during the Beta and is never switched to v2 automatically.
- **The prerelease carries `management.html`.** Each v2 Beta prerelease also ships the single-file `management.html` (the `npm run build:panel` output, with no reference to any `assets/` resource). CPA does not download it automatically.
- **The Management API stays `/v0/management`.** The v2 Beta lightweight panel reads and writes CPA configuration through `/v0/management`, which CPA v8 still serves, and signs in with the CPA v8 Management Key. Moving to `/v8/management` is post-Stable work and is outside the first Beta.
- **Pre-release acceptance.** `bin/ci/panel-lite-cpa-v8.mjs` verifies on the pinned CPA v8 that the `static/management.html` CPA obtains is byte-identical to the build, `/management.html` answers 200, the Management Key authenticates `/v0/management/config` and a changed configuration field is written to `config.yaml`, and `/management.html` answers 404 when `disable-control-panel: true`.

### Try The v2 Beta Panel On CPA

Automatic updates through `panel-github-repository` use `releases/latest`, not a Beta Prerelease selector. Do not put a prerelease Tag URL in that setting to change channels:

- **`api.github.com` URL**: if it does not end with `/releases/latest`, CPA appends that suffix. Thus `.../releases/tags/<tag>` becomes `.../releases/tags/<tag>/releases/latest`, rather than fetching the chosen prerelease.
- **Browser URL**: `github.com/<owner>/<repo>/releases/tag/<tag>` uses only the first two path segments and resolves to the repository's `repos/<owner>/<repo>/releases/latest`. It still obtains the 1.x panel, not the page's Tag.
- **Fallback after a failed download**: if fetching fails and no local cached file exists, CPA attempts to install the upstream fallback panel from `cpamc.router-for.me`. That is not the selected CPAMP Beta file; merely opening `/management.html` does not prove the intended Beta was installed.

Install the v2 Beta panel manually, retaining the cache location and automatic-update settings below:

1. Download `management.html` from an actually published v2 Beta prerelease and compare it with that release's `checksums.txt`. Stable URL: `https://github.com/seakee/CPA-Manager-Plus/releases/latest/download/management.html`. Beta template: `https://github.com/seakee/CPA-Manager-Plus/releases/download/v2.0.0-beta.X/management.html`; replace `X` with an actually published number.
2. Set `disable-auto-update-panel: true` in CPA `config.yaml`. Otherwise CPA overwrites the manually placed file with the 1.x panel from `releases/latest`.
3. Stop CPA, copy the file to `management.html` in the `static/` folder next to CPA's `config.yaml`, and start CPA.
4. Open `http://<cpa-host>:8317/management.html` and log in with the CPA Management Key.

To return to the 1.x panel, delete `static/management.html`, set `disable-auto-update-panel` back to `false`, and restart CPA.

External Full Mode is not this backend-free lightweight panel: it needs Manager while the operator independently runs, upgrades, and backs up external CPA. CPAMP does not take over its host lifecycle; see [Runtime Model](../guide/runtime-model.md). v2 Embedded instead requires the matching three-image Compose/pairing record; copying only `management.html` does not upgrade Full Web. See [Releases And Licensing](../reference/releases.md) for current project MIT, official CPA MIT, and image third-party notice inspection paths.


## Lightweight Panel Capabilities

| Capability                                                   | CPAMP Lightweight Panel                             |
| ------------------------------------------------------------ | --------------------------------------------------- |
| CPA config, providers, Accounts, OAuth, and logs             | Supported                                           |
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
- The old 1.x interface returns after installing the v2 Beta panel: confirm `disable-auto-update-panel: true`; otherwise CPA overwrites the manually placed file with the 1.x panel from `releases/latest`.
- Monitoring is missing: this is expected in Lightweight Mode; use the Manager Server panel.
