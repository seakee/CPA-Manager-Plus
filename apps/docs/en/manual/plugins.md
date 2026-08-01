---
title: CPA Plugin Management And Store
description: Install, select versions, enable, configure, and troubleshoot CPA plugins with GitHub Releases, prereleases, manual tags, and plugin pages.
---

# CPA Plugin Management And Store

The Plugins page installs, upgrades, enables, configures, and troubleshoots CPA plugins. CPAMP provides the management UI and plugin-page container; actual runtime capability depends on CPA and the plugin.

Open the [Plugin Management Demo](https://seakee.github.io/CPA-Manager-Plus/#/demo/plugins) to inspect fictional plugin and store data.

## Page Areas

- **Installed Plugins** shows versions, authors, state, configuration, OAuth, menus, and page resources.
- **Plugin Store** loads installable plugins from `plugins.store-sources`.

When `plugins.enabled` is off, an individually enabled plugin still cannot run.

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

## Plugin Pages And Reverse Proxies

Plugin pages use `/v0/resource/plugins/*`. A custom reverse proxy must route that path to CPAMP or the page may be blank.

Common causes:

- The plugin is disabled or declares no menu.
- Resource files are missing.
- Reverse-proxy paths are wrong.
- The plugin version is incompatible with CPA runtime.
- Required configuration is missing.

## Security Boundaries

- Plugins are executable extensions. Review sources, permissions, and updates like server-side code.
- CPAMP displays metadata and proxies management operations; it does not guarantee third-party plugin security or compatibility.
- Do not publish plugin directories, configuration, or credentials in logs and issues.
- After production upgrades, observe [Dashboard](./dashboard.md), [Monitoring](./monitoring.md), and [Logs](./logs.md).
