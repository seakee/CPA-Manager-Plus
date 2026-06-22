# CPA Manager Plus

[中文文档](README_CN.md)

CPA Manager Plus is a single-file management panel for [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) plus a Manager Server for persistent request monitoring. Use this README as the project entry point; detailed deployment and operation guides live in the [Wiki](https://github.com/seakee/CPA-Manager-Plus/wiki).

- Recommended CPA version: `v7.1.39+`
- Minimum CPA version for the HTTP usage queue: `v6.10.8+`
- Frontend: React 19, Vite, single-file `management.html`
- Backend: Go 1.24 Manager Server, SQLite through `modernc.org/sqlite`, no CGO
- Images: `seakee/cpa-manager-plus` and `ghcr.io/seakee/cpa-manager-plus`

## Panel Preview

<table>
  <tr>
    <td align="center">
      <strong>Dashboard</strong><br>
      <img src="img/home.png" alt="CPA Manager Plus dashboard" width="420">
    </td>
    <td align="center">
      <strong>Request Monitoring</strong><br>
      <img src="img/monitoring.png" alt="Request monitoring center" width="420">
    </td>
  </tr>
  <tr>
    <td align="center">
      <strong>Usage Analytics</strong><br>
      <img src="img/usage-analytics.png" alt="Usage analytics view" width="420">
    </td>
    <td align="center">
      <strong>Codex Account Inspection</strong><br>
      <img src="img/codex-inspection.png" alt="Codex account inspection view" width="420">
    </td>
  </tr>
</table>

## Core Features

- Turns the CPA usage queue into a SQLite request ledger for live monitoring, historical search, import/export, and long-running analytics.
- Breaks down cost, tokens, cache usage, latency, failures, and throughput by model, provider, account/auth file, API key alias, project, channel, and time window.
- Codex account operations: browser-local checks and Manager Server scheduled inspections for quota windows, 401 reauth needs, deactivated workspaces, stale accounts, and safe enable/disable/delete suggestions.
- Account-pool safeguards: Codex `usage_limit_reached` can temporarily disable an auth file and recover it at reset time, while manual disables are left untouched. Revoked or invalid OAuth tokens are collected as auth issue candidates for review or verified auto-disable.
- Model pricing sync from LiteLLM and OpenRouter, with candidate matching for renamed/provider-prefixed models and cost estimates reused by the dashboard, monitoring, and analytics pages.
- CPA operations UI for providers, auth files, OAuth, quota, API keys, logs, plugins, plugin store, and system information, including paste/import and batch auth-file workflows.
- Manager Server mode adds admin-key login, encrypted CPA Management Key storage, request monitoring, and server automation; CPA panel mode stays lightweight for existing CPA-hosted panels.
- Docker images, native packages for Linux/macOS/Windows on `amd64` and `arm64`, and a standalone single-file `management.html`.

## Choose A Mode

| Mode | Entry URL | Login credential | Best for |
|---|---|---|---|
| Manager Server mode | `http://<host>:18317/management.html` | Manager Server admin key | New deployments, request monitoring, historical analytics |
| CPA panel mode | `http://<cpa-host>:8317/management.html` | CPA Management Key | Existing CPA panel hosting without Manager Server analytics |
| Frontend development | Vite dev server or `apps/web/dist/index.html` | CPA URL and key | Local UI development |

Manager Server mode is the full CPA Manager Plus experience. CPA panel mode stays a pure CPA panel: it does not configure Manager Server and does not read Manager Server SQLite data.

## Quick Start

Run Manager Server:

```bash
docker run -d \
  --name cpa-manager-plus \
  --restart unless-stopped \
  -p 18317:18317 \
  -v cpa-manager-plus-data:/data \
  seakee/cpa-manager-plus:latest
```

Open:

```text
http://<host>:18317/management.html
```

On first startup, read the generated admin key from `docker logs cpa-manager-plus`, then complete setup with:

- Manager Server admin key
- CPA URL
- CPA Management Key
- Request monitoring settings

Detailed setup, Compose, Linux host networking, upgrades, backup rules, and native packages are in the Wiki.

## Documentation

| Topic | Guide |
|---|---|
| Start here | [Wiki Home](https://github.com/seakee/CPA-Manager-Plus/wiki) |
| Docker deployment | [Docker Deployment](https://github.com/seakee/CPA-Manager-Plus/wiki/Docker-Deployment) |
| Native packages | [Native Binary Deployment](https://github.com/seakee/CPA-Manager-Plus/wiki/Native-Binary-Deployment) |
| Manager Server config, endpoints, data, and security | [Manager Server Guide](https://github.com/seakee/CPA-Manager-Plus/wiki/Manager-Server-Guide) |
| Reverse proxy | [Reverse Proxy Same Domain](https://github.com/seakee/CPA-Manager-Plus/wiki/Reverse-Proxy-CPA-and-CPA-Manager-Plus-with-the-Same-Domain) |
| Migrate from old CPA-Manager | [Migration from CPA-Manager](https://github.com/seakee/CPA-Manager-Plus/wiki/Migration-from-CPA-Manager) |
| Reset lost admin key | [Reset Admin Key](https://github.com/seakee/CPA-Manager-Plus/wiki/Reset-Admin-Key) |
| Troubleshooting | [FAQ and Troubleshooting](https://github.com/seakee/CPA-Manager-Plus/wiki/FAQ-and-Troubleshooting) |
| Release process | [docs/release.md](docs/release.md) |
| Release notes | [docs/release-notes](docs/release-notes) |

## Development

```bash
npm install
npm run dev
npm run type-check
npm run lint
npm run test
npm run build
```

Manager Server:

```bash
cd apps/manager-server
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/cpa-manager-plus
```

Build the Docker stack locally:

```bash
docker compose -f docker-compose.manager.yml up --build
```

## Release

- `npm run build` creates a single-file `apps/web/dist/index.html`.
- `bin/release/package-native.sh` embeds the built panel into native packages.
- Tag pushes such as `vX.Y.Z` trigger `.github/workflows/release.yml`.
- Release assets include `management.html`, native packages, and Docker images for `linux/amd64` and `linux/arm64`.

## Acknowledgements

- Thanks to the upstream projects [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) and [Cli-Proxy-API-Management-Center](https://github.com/router-for-me/Cli-Proxy-API-Management-Center) for the foundation and inspiration.
- Thanks to the [Linux.do](https://linux.do/) community for project promotion and feedback.

## License

MIT
