---
title: Choosing A CPA / CLIProxyAPI Management Panel
description: Compare the official Management Center, CPAMP Lightweight Panel, and CPAMP Full Mode for CPA administration, monitoring, cost analytics, and account operations.
---

# Choosing A CPA / CLIProxyAPI Management Panel

CPA / CLIProxyAPI users do not have to choose only between the official UI and a full additional service. CPAMP provides both a lightweight panel hosted directly by CPA and a full mode backed by Manager Server.

## Quick Decision

| Goal                                                                   | Recommended choice         |
| ---------------------------------------------------------------------- | -------------------------- |
| Use the upstream UI maintained by the CPA project                      | Official Management Center |
| Replace the official UI with a clearer WebUI and no additional service | CPAMP Lightweight Panel    |
| Persist request history, diagnose failures, and analyze cost           | CPAMP Full Mode            |
| Run server-side inspection, quota cooldowns, and account automation    | CPAMP Full Mode            |
| Start with low commitment and decide about SQLite and collection later | CPAMP Lightweight Panel    |

## Official Management Center

The official panel is the baseline WebUI maintained by the CPA project. It talks directly to the CPA Management API and fits users who prefer the upstream default interface.

- No additional service is required.
- Open it from CPA at `:8317/management.html`.
- Log in with the CPA Management Key.
- Manage configuration, providers, auth files, OAuth, API keys, quota, and logs.

Official project: [`router-for-me/Cli-Proxy-API-Management-Center`](https://github.com/router-for-me/Cli-Proxy-API-Management-Center).

## CPAMP Lightweight Panel

The CPAMP Lightweight Panel uses the same CPA port and Management API while replacing the official interface with CPAMP navigation, forms, cards, and information hierarchy.

- It does not require Manager Server, SQLite, or another container.
- Point CPA `panel-github-repository` at CPA Manager Plus.
- Keep the native CPA configuration, auth file, OAuth, quota, log, and plugin workflows.
- Use it when you want a clearer interface while preserving a single-process, single-port deployment.

See [Install The CPAMP Lightweight Panel](../deployment/cpa-panel.md).

## CPAMP Full Mode

Full Mode adds Manager Server and local SQLite for server-backed capabilities that the lightweight panel does not provide:

- Persistent request events from the CPA usage queue.
- Request, cost, token, latency, and failure analytics by account, model, provider, API key, project, and time range.
- Saved model prices, API key aliases, inspection history, and automation state.
- Server-side Codex/xAI inspection, quota cooldowns, and the account action queue.
- Backups, background migrations, performance diagnostics, and an independent CPAMP Admin Key.

## Entry Points And Capability Boundary

| Usage option                     | Panel entry                                 | Best for                                     |
| -------------------------------- | ------------------------------------------- | -------------------------------------------- |
| Official Management Center       | `http://<cpa-host>:8317/management.html`    | Keeping the upstream default UI              |
| CPAMP Lightweight Panel          | `http://<cpa-host>:8317/management.html`    | Replacing only the UI with no extra service  |
| CPAMP Full Mode (v2 Beta)        | `http://<cpamp-host>:18317/console` (default) | Monitoring, cost, inspection, and automation |
| CPAMP Full Mode (1.x Stable)     | `http://<cpamp-host>:18317/management.html` | Monitoring, cost, inspection, and automation |

The Full Mode entry depends on the version: v2 Beta uses the Control Base Path chosen at installation (the wizard defaults to `/console`, for example `http://<cpamp-host>:18317/console`; a custom path moves both the console and its frontend API) and does not serve `/management.html`, which returns 404; 1.x Stable still uses `:18317/management.html`. The CPA-hosted `:8317` panel does not connect to or read Manager Server. Even when Manager Server runs separately, open its own Full Mode entry to use request history, cost analytics, model prices, and server-side inspection.

## Full Mode Installation Options

Full Mode has one product capability set with two installation choices:

- **Docker deployment (recommended)**: for most new users and server deployments.
- **Native package deployment**: for Linux, macOS, or Windows hosts without Docker.

Both use the CPAMP Admin Key. The entry depends on the version: v2 Beta uses the control base path chosen at installation (default `/console`), while 1.x Stable uses `:18317/management.html`.

## Preview Before Choosing

The [Live Demo](https://seakee.github.io/CPA-Manager-Plus/) lets you preview the interface and navigation with fictional data. It is not a panel, deployment option, or runtime mode. It cannot connect to, manage, or monitor a real CPA instance and cannot validate whether real features are working.

## Recommended Upgrade Path

1. Already run CPA and only want a different UI: install the CPAMP Lightweight Panel.
2. Need historical monitoring or cost analytics: deploy Manager Server.
3. Switch to the Full Mode entry (the control base path chosen at installation, default `/console`, for v2 Beta; `:18317/management.html` for 1.x Stable) and configure the CPA connection and collector.
4. The lightweight panel may remain on the CPA port, but the two entry points have independent capabilities.

## Next Steps

- Install the [CPAMP Lightweight Panel](../deployment/cpa-panel.md).
- Review the [Capability Matrix](../reference/capability-matrix.md).
- Use the recommended [Docker Deployment](../deployment/docker.md).
- Use [Native Packages](../deployment/native.md) without Docker.
