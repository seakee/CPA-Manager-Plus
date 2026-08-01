# How CPAMP Works With CPA

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
