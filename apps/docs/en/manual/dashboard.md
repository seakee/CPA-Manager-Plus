---
title: CPA Management And Observability Dashboard
description: Use the CPA Manager Plus dashboard to inspect CPA/Manager Server connectivity, requests, success rate, cost, tokens, collector state, providers, and health alerts.
---

# CPA Management And Observability Dashboard

The dashboard is the first page to check after opening CPAMP. It does not replace request details. It answers the first operational questions: is the runtime connected, are requests arriving, did failures increase, did cost jump, and are there account or runtime warnings.

If you already know which account or request is involved, go directly to [Monitoring](./monitoring.md), [Auth Files](./auth-files.md), or [Codex Inspection](./codex-inspection.md).

Open the [Dashboard Demo](https://seakee.github.io/CPA-Manager-Plus/#/demo) to inspect fictional request, cost, failure, and provider state generated for the current date.

## What To Check First

- **Connection and version**: confirm that the panel is connected to CPA or Manager Server and that the runtime mode matches your deployment.
- **Quick stats**: API keys, auth files, AI providers, and model counts. These tell you whether configuration is being read.
- **Requests and cost**: today's requests, success rate, average latency, tokens, and estimated cost.
- **Collector status**: check whether request monitoring is running, whether the queue can be read, and whether recent collection succeeded.
- **Health alerts**: log errors, connection issues, unavailable monitoring, or missing configuration show up here first.

Dashboard data comes from Manager Server's local SQLite database and the CPA usage queue. Cost is estimated from request events and model prices; it is not a provider invoice.

## Common Workflow

1. Start with connection state and collector state.
2. If request count is zero, confirm that clients are actually sending traffic through CPA, then open [Request Monitoring Troubleshooting](../troubleshooting/request-monitoring.md).
3. If success rate dropped, open [Monitoring](./monitoring.md) and filter by status, model, account, or API key.
4. If cost increased, open [Usage Analytics](./usage-analytics.md) and break it down by model, account, and caller.
5. If an alert points to logs or runtime state, open [Logs](./logs.md) and [System](./system.md) before changing configuration.

## If Dashboard Is Empty

If Dashboard stays empty, do not start by changing filters. Check:

1. Client requests actually pass through CPA.
2. CPA usage publishing is enabled.
3. The CPAMP collector is running.
4. CPA URL and CPA Management Key are correct.
5. Usage queue retention is long enough for events to be collected.

For the full sequence, see [Request Monitoring Troubleshooting](../troubleshooting/request-monitoring.md).

## Usage Tips

- If success rate drops, open [Monitoring](./monitoring.md) and filter by status code, model, and account.
- If cost spikes, open [Usage Analytics](./usage-analytics.md) and compare model and account breakdowns.
- If one account looks unhealthy, open [Auth Files](./auth-files.md), [Quota](./quota.md), or [Codex Inspection](./codex-inspection.md).
- If login or authorization looks wrong, open [OAuth Login](./oauth.md), then return to Auth Files.
- If system state looks wrong, open [Logs](./logs.md) and [System](./system.md) and collect version and log context.
