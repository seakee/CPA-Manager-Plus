# Configuration

Configuration changes connections, request monitoring, and CPA system settings. Use [AI Providers](./ai-providers.md) to add model services, and use [OAuth Login](./oauth.md) or [Auth Files](./auth-files.md) to add accounts.

| What you want to do                     | Area to use                              |
| --------------------------------------- | ---------------------------------------- |
| Change common CPA settings              | Visual configuration                     |
| View or edit the complete `config.yaml` | Source configuration                     |
| Connect CPA or enable Monitoring        | Manager Server configuration (Full Mode) |

## Page Areas

The configuration page normally has three editing modes:

- **Visual configuration**: the safest choice for routine changes. Fields are grouped by feature.
- **Source configuration**: direct `config.yaml` editing. Useful, but easier to break with bad YAML.
- **Manager Server configuration**: available when Manager Server hosts the panel. Use it to bind CPA, control request monitoring, and save CPAMP runtime settings.

If a tab is unavailable, first check whether you are using the CPAMP Lightweight Panel or Full Mode. Manager Server-only features do not appear in Lightweight Mode; Full Mode can be installed with Docker or a native package. The Live Demo only shows fictional data and cannot configure a real CPA instance or validate its runtime state.

## Manager Server Configuration

Most users only need to confirm three fields:

- **CPA URL**: the address Manager Server uses to reach CPA. In Docker this is often a service name on the Docker network, not the browser URL.
- **CPA Management Key**: used by CPAMP to call CPA management APIs. It is not a client model API key.
- **Request monitoring**: enables CPA usage publishing and starts the CPAMP collector.

After saving, return to Dashboard to confirm that CPA is connected, then send a real request and check Monitoring.

::: details Advanced collection settings

- **Collector mode**: auto, RESP, or HTTP. RESP must connect directly to the CPA API port and cannot go through a normal HTTP reverse proxy.
- **Poll interval, batch size, and query limit**: control collection latency and read volume. Do not set the poll interval longer than CPA queue retention.
- **Configuration source**: environment, SQLite, or unsaved. Environment values win over panel saves.

Keep automatic mode and default values unless your network has a known restriction or Monitoring remains empty.

:::

## Visual And Source Configuration

Use the visual editor for normal operations. Use source mode when you need to:

- Change several sections at once.
- Paste an existing `config.yaml`.
- Edit a field that is not exposed visually.
- Compare the runtime configuration with your deployment file.

Before saving source configuration, check indentation, arrays, and quoted strings. If a saved change does not appear to take effect, check whether CPA supports hot reload or needs a restart.

## Verify After Saving

1. Return to Dashboard and check connection and collector state.
2. Open [AI Providers](./ai-providers.md) and confirm provider entries are still present.
3. Send a low-cost request.
4. Open [Monitoring](./monitoring.md) and confirm the event appears.
5. If the change affects usage data, refresh [Usage Analytics](./usage-analytics.md).

## Common Mistakes

- CPAMP Admin Key, CPA Management Key, client API keys, and provider API keys are different secrets.
- Stopping the CPAMP collector does not clear CPA's usage queue.
- Model prices only affect CPAMP cost estimation. They do not change provider billing.
- Environment-sourced settings must be changed in the deployment environment and then restarted. A panel save will not override them.
