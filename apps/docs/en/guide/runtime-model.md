# How CPAMP Works With CPA

## v2 Beta: Embedded And External

Full Mode in v2 Beta is not the single-Manager 1.x deployment below. Embedded official CPA is pinned to **`v8.0.55`**; External requires at least **`v8.0.55`** and actual qualification. The exact 1.x direct upgrade baseline is **`v1.14.10`**; see [Upgrade And Rollback](../operations/update.md).

| Mode | Who runs CPA | CPAMP boundary |
| --- | --- | --- |
| Embedded / New Managed or qualified Adopt | Docker `cpamp-runtime` or Native Runtime Supervisor manages official CPA | Ingress + Manager + Runtime; Docker publishes only Ingress `18317`, with one Digest-paired image group; Native launcher manages three processes and the CPA child |
| External | The operator's own CPA host/container/service | CPAMP proves/saves the connection, manages supported configuration and Client Keys, and reads status/observations; it does not start, stop, restart, or upgrade external CPA or take over its traffic entry automatically |
| Lightweight Panel | CPA directly hosts `management.html` | No Manager, SQLite, or Runtime; signs in with CPA Management Key and cannot provide Manager request history |

**Embedded** browser/model traffic uses the configured public origin. Ingress owns public routing; do not separately publish internal/control `8317`/`9081`. New installation requires a host `initialize` Handoff, browser Admin Key setup, and strategy/qualification completion before normal product access. The first Client Key is for model requests; Admin Key is for administration. Process liveness, installation completion, and Gateway readiness are different states.

**External** first proves the connection and Management Key. Supported configuration does not imply host filesystem/lifecycle authority. Rule writes stay read-only without an exclusive write window; writable scalar fields follow actual capabilities. Usage statistics require explicit administrator confirmation rather than automatic changes to external CPA. Its operator remains responsible for CPA upgrades, TLS/networking, configuration/OAuth backups, and availability. Clients retain the external CPA's real entry; Manager completion does not reroute them automatically.

After external Management Key rotation, rebind and prove the current connection in the panel; do not repeatedly retry credentials CPA already rejected. Retain Manager `data.key` with its encrypted database. A refused or incomplete Adopt migration does not authorize automatic External fallback: follow the wizard report, not environment changes or manually edited records.

The v2 Beta browser entry is the console at **`/console`** on the public origin (for example `http://<host>:18317/console`). v2 does not serve CPAMP's `/management.html`; that path returns 404. Embedded `8317` and `9081` are container-internal only and must not be published separately.

Install: [v2 Docker](../deployment/docker.md) / [linux/amd64 Native](../deployment/native.md). Operate: [Backup](../operations/backup.md) / [Licensing And Release Assets](../reference/releases.md).

## 1.x Stable And Lightweight Panel Reference

This section describes 1.x Stable, where Full Mode is the Manager Server panel at `:18317/management.html`. For v2 Beta use the `/console` entry above; do not treat this section's `:18317/management.html` as the v2 entry.

CPA receives and forwards real model requests. CPAMP manages CPA and, in Full Mode, stores request history, analyzes cost, and helps maintain account state.

Most users only need to remember three things:

1. Codex, Claude Code, OpenCode, and other clients always connect to CPA, not CPAMP.
2. The CPAMP Lightweight Panel opens from CPA `:8317/management.html` and uses the CPA Management Key.
3. CPAMP Full Mode opens from `:18317/management.html` and uses the CPAMP Admin Key.

## Where Each Request Goes

| Action                      | Address                                       | Key                |
| --------------------------- | --------------------------------------------- | ------------------ |
| A client requests a model   | CPA model endpoints such as `/v1/...`         | CPA client API key |
| Use CPAMP Lightweight Panel | CPA `:8317/management.html`                   | CPA Management Key |
| Use CPAMP Full Mode         | CPAMP `:18317/management.html`                | CPAMP Admin Key    |
| Connect Full Mode to CPA    | CPA URL entered during setup or configuration | CPA Management Key |

Do not mix these keys. If login fails, first confirm whether the current page uses port `8317` or `18317`.

## The Two Modes

- **Lightweight Panel**: replaces the official CPA management UI without adding a service or database.
- **Full Mode**: adds Manager Server for request history, cost analytics, server-side inspection, backups, and automation.

If you are unsure which one to use, read [Choosing A Panel](./choosing-a-panel.md).

::: details Complete request and data flow

```text
Codex / Claude Code / other clients
  -> CPA
      -> model providers
      -> request logs and usage queue

CPAMP Full Mode
  -> reads management information and usage events from CPA
  -> stores request history, prices, inspection, and automation state
```

This means:

- For a failed client request, check CPA, the provider, the account, and client configuration first.
- When CPAMP has no data, confirm that requests pass through CPA, then inspect monitoring collection.
- CPAMP does not forward model requests by itself.

:::

## When To Change CPA Configuration

CPA configuration still controls:

- Providers, model routing, auth files, and OAuth.
- Client API keys, quota, logs, and plugins.
- Remote management, usage publishing, and queue retention.

Prefer the CPAMP interface for daily work. Edit CPA `config.yaml` directly only when the UI does not expose a field, when preparing CPA before deployment, or during advanced troubleshooting.

## Next Steps

- Install: [Quick Start](./getting-started.md)
- Add model services: [AI Providers](../manual/ai-providers.md)
- Configure clients: [Client Configuration](../gateway/clients.md)
- Monitoring has no data: [Monitoring Has No Data](../troubleshooting/request-monitoring.md)
