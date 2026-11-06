---
title: CPA Plugin Management And Store
description: Install, select versions, enable, configure, and troubleshoot CPA plugins with GitHub Releases, prereleases, manual tags, sandboxed plugin pages, and the theme message protocol.
---

# CPA Plugin Management And Store

The Plugins page installs, upgrades, enables, configures, and troubleshoots CPA plugins. CPAMP provides the management UI and plugin-page container; actual runtime capability depends on CPA and the plugin.

Open the [Plugin Management Demo](https://seakee.github.io/CPA-Manager-Plus/#/demo/plugins) to inspect fictional plugin and store data.

## Page Areas

- **Installed Plugins** shows versions, authors, state, configuration, OAuth, menus, and page resources.
- **Plugin Store** loads installable plugins from `plugins.store-sources`.

When `plugins.enabled` is off, an individually enabled plugin still cannot run.

The plugin entry appears only when the Manager proves that the current connection supports plugins (`X-CPA-SUPPORT-PLUGIN`). The Manager does not infer this from a route existing: it reads the saved CPA connection's own configuration document and requires the plugin section in it. A default managed installation satisfies that, so no extra configuration is required. If the entry disappears, the saved connection could not prove plugin support — typically an older CPA without the plugin namespace, or a connection the Manager cannot read.

The entry stays visible while `plugins.enabled` is off, because that page holds the switch that turns plugins back on. Only the plugin's own menus disappear.

## Plugin Runtime Switch

In a default managed installation the Runtime Supervisor writes `plugins.enabled` and the plugin directory into the target CPA configuration, and the first-run wizard selection decides the initial value. After installation the managed installation can change the switch from the "Plugin runtime" control at the top of the Plugins page:

- Each flip is one baseline-checked save through the same journal, CAS, and read-back path as the gateway configuration page (Manager API: `plugins/runtime`). External CPA connections and the private H1 profile are refused server-side, so those profiles never show the control.
- The page shows the persisted value and the running value separately. CPA reloads configuration asynchronously, so a successful save only means the value is persisted. The running readout reports that it is unknown until CPA reloads; this release does not observe CPA's reload, so it does not update that readout afterwards either.
- If the result of the last write is unknown, the page offers "Read back result": it reads CPA's current document and records what it proves, and never sends the write again.
- The plugin directory (`plugins.dir`) is published by the Runtime Supervisor and cannot be changed here.

## Install Versions

The store supports three version modes:

- **Latest** uses the default latest version from the store or repository.
- **GitHub Release** loads releases and lets you select a tag, with an option to show prereleases.
- **Manual tag** pins an explicit tag when the release API is unavailable or a fixed version is required.

GitHub release discovery may fail because of repository access, network/proxy configuration, or API rate limits. Latest and manual tag remain fallback paths.

Before installing or upgrading:

1. Trust the repository and download source.
2. Confirm support for the current CPA version, OS, and architecture.
3. Persist the plugin directory.
4. Use prereleases only when the environment accepts compatibility risk.

## Installed Plugin Actions

- Enable or disable a plugin.
- Edit string, number, boolean, array, or object fields.
- Change priority.
- Start plugin-provided OAuth.
- Open plugin page resources.
- Remove plugin configuration or files.

If the UI says a restart is required, restart CPA or Manager Server for the deployment mode; a browser refresh is not enough.

## Sandbox Boundary For Plugin Pages

Plugin pages open inside the Console as an iframe and always run in a **sandboxed opaque origin**. This is not a setting; the Manager enforces it on every response:

- Every `/v0/resource/plugins/*` response carries `Content-Security-Policy: sandbox allow-scripts allow-forms`, without `allow-same-origin`.
- The iframe itself uses `sandbox="allow-scripts allow-forms"`, which grants no popups, top-level navigation, downloads, or same-origin access.
- Responses are reduced to an allowlist of representation headers (`Content-Type`, `Content-Length`, `Content-Encoding`, `Content-Language`, `Content-Disposition`, `ETag`, `Last-Modified`, `Cache-Control`, `Expires`, `Vary`, `Accept-Ranges`, `Content-Range`, and a validated `Location`). Everything else the plugin sends is dropped, including `Set-Cookie`, `Clear-Site-Data`, `NEL`, `Report-To`, `Reporting-Endpoints`, `Strict-Transport-Security`, `Alt-Svc`, and `Access-Control-Allow-Credentials`. Redirects may only stay inside the plugin's own prefix.

The consequences:

- A plugin page **cannot** read `localStorage`, `sessionStorage`, cookies, or the admin document's DOM, and cannot call the management API.
- The sandbox gives the page an **opaque origin**, which is what removes admin authority. It does **not** confine the plugin's network access: the plugin CSP carries no fetch directives, so a page can load from and send data to any origin it likes, and it can read other plugins' resources because those are served with `Access-Control-Allow-Origin: *`. Treat a plugin page as untrusted third-party code that happens to run inside the Console frame, not as a page with a narrowed network.
- Plugins that depend on same-origin access (reading Console storage, relative API paths under the Console, same-origin cookies) are **not supported**. The UI shows a "cannot display this plugin page" explanation instead of falling back to a same-origin load.

The plugin resource namespace is read-only in CPA: a `pluginapi.ResourceRoute` is the plugin's own page ("the management UI menu label for this GET resource"), and CPA's plugin host serves it only for GET. A plugin that needs to accept input registers a Management route instead — the official Codex Invite plugin serves its page from `/v0/resource/plugins/codex-invite/invite` over GET and posts the send action to `/v0/management/codex-invite/invite`.

What a write gets depends on which layer answers it, and both layers refuse:

- **Full Docker (Ingress in front):** the Ingress routes the plugin resource namespace itself and only a bare, unescaped `GET` or `HEAD` for a canonical resource is forwarded to the Manager. Any other method is denied at the transport edge with `404`, before it reaches the Manager or the Gateway.
- **Manager only (no Ingress):** the Manager's plugin proxy is a generic reverse proxy and forwards whatever method the caller used, so the write reaches CPA, and CPA refuses it (`404`, because the resource route is GET-only).

The Manager does not add a method veto of its own, because it does not own that namespace. Nothing about the isolation depends on the method either way: the sandbox CSP, the response-header allowlist, redirect confinement and the Fetch Metadata policy apply to a write exactly as to a read.

The Manager also enforces Fetch Metadata on every plugin resource request: a request without `Sec-Fetch-Site` is rejected with 403, and so are same-origin subresource requests whose `Sec-Fetch-Dest` is not a document destination (for example `script`, `worker`, `style`, or `font`). That check, not the CSP, is what stops an admin document from loading plugin code; the admin document's CSP does not exclude plugin URLs by path.

Older browsers without Fetch Metadata still support plugin management, but cannot open plugin pages. That degradation is expected.

## Theme Message Protocol

The host cannot read or write the plugin document, so theme synchronization is a strictly optional message protocol. The Console posts to the iframe window after the frame loads, when the host theme changes, and after the page navigates again:

```json
{
  "type": "cpamp:plugin-theme",
  "version": 1,
  "theme": "light",
  "themeAttribute": "white",
  "hostAttribute": "data-cpamp-plugin-host",
  "variables": { "--primary-color": "#409eff" }
}
```

- Because the sandboxed document's origin is `null`, `targetOrigin` can only be `*`. The payload contains no credentials, only theme variables already visible in the admin UI.
- A plugin follows the theme only if it listens for `message` in its own document and applies the variables itself; otherwise it keeps its own styling.
- The Console only sends. It never listens for plugin messages and exposes no callback or API to the plugin.
- `variables` are CSS custom properties (`--app-*`, `--bg-*`, `--text-*`, `--border-*`, `--primary-*`, and others) that can be written directly to `document.documentElement.style`.

Minimal implementation:

```js
window.addEventListener('message', (event) => {
  const data = event.data;
  if (!data || data.type !== 'cpamp:plugin-theme' || data.version !== 1) return;
  const root = document.documentElement;
  root.setAttribute(data.hostAttribute, '');
  root.setAttribute('data-theme', data.themeAttribute);
  for (const [name, value] of Object.entries(data.variables)) {
    root.style.setProperty(name, value);
  }
});
```

## Plugin Pages And Reverse Proxies

Plugin pages use `/v0/resource/plugins/*`, in the site's root namespace rather than below `/api/v2/cpamp`. A custom reverse proxy must route that path to CPAMP or the page may be blank.

Common causes:

- The plugin is disabled or declares no menu.
- Resource files are missing.
- Reverse-proxy paths are wrong.
- The plugin version is incompatible with CPA runtime.
- Required configuration is missing.
- The plugin page depends on same-origin access: only resources below `/v0/resource/plugins/<plugin id>/` are supported.

## CPA Version Upgrade Regression Checklist

CPAMP supports CPA v8 and later only. When the pinned CPA version is upgraded, re-verify:

1. **Plugin v0 endpoints** — `/v0/management/plugins`, `/v0/management/plugins/<id>/enabled`, `/v0/management/plugins/<id>/config`, and `/v0/management/plugin-store` still exist with the same semantics. Plugin enable/disable and configuration exist only in `v0`; `/v8/management` does not replace them.
2. **Management path ownership** — `cpaBuiltinManagementPathHeads` must match CPA's current built-in routes. If the "not built-in means plugin" test drifts, plugin endpoints are classified wrongly in either direction.
3. **`pluginabi` version** — `pluginabi.SchemaVersion` (7 in v8.0.44) and the store plugins' compatibility. A mismatch installs successfully but never loads.
4. **Resource prefix** — `/v0/resource/plugins/<id>/` has not changed from v8.0.13 through v8.0.55; re-verify that plugin pages open and that theme synchronization still works.

## Security Boundaries

- Plugins are executable extensions. Review sources, permissions, and updates like server-side code.
- CPAMP displays metadata and proxies management operations; it does not guarantee third-party plugin security or compatibility.
- An unauthenticated request never receives the saved CPA management key; plugin management proxying happens only inside an authenticated admin session.
- Do not publish plugin directories, configuration, or credentials in logs and issues.
- After production upgrades, observe [Dashboard](./dashboard.md), [Monitoring](./monitoring.md), and [Logs](./logs.md).
