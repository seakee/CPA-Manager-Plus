---
title: CPA Request Monitoring And Failure Diagnosis
description: Persist and filter CPA requests by account, caller API key, provider, model, trace, latency, cache, and status while inspecting sanitized failure evidence.
---

# CPA Request Monitoring And Failure Diagnosis

Monitoring is the main entry point for request failures. Dashboard tells you something is wrong. Monitoring tells you which request type, account, model, or caller is involved.

Account handling is documented separately in [Account Action Queue](./account-actions.md). This page focuses on request events.

Open the [Monitoring Demo](https://seakee.github.io/CPA-Manager-Plus/#/demo/monitoring) to browse fictional account overview, API key summary, and realtime request data.

## Availability

Monitoring is available only in the Manager Server-hosted panel and requires CPA usage publishing. The CPAMP Lightweight Panel does not connect to Manager Server or read its SQLite data; open `http://<cpamp-host>:18317/management.html` when monitoring is required.

If the page says request monitoring is disabled, enable it in [Configuration](./configuration.md) under Manager Server configuration.

## Views

- Account Overview: start here to see whether failures concentrate on one account.
- API Key Summary: see whether one caller key causes unusual traffic or cost.
- Realtime Requests: inspect one request's model, status code, latency, tokens, and failure summary.
- Summary cards: total requests, failures, success rate, latency, and tokens.
- Filters: narrow by time, provider, model, project, account, API key, status, failure type, latency, and cache state.

Failure summaries are sanitized. Raw failure bodies stay in local SQLite and are not exposed through normal APIs or JSONL exports.

## Time Ranges

The **24 hours** preset starts 24 elapsed hours before the current local hour and ends at the latest refresh, including current-hour requests. At 10:32, the window is yesterday 10:00 (included) through today 10:32 (excluded); each normal refresh advances the end while the start stays fixed within that hour. Its actual duration is at least 24 hours and less than 25 hours. The hourly timer advances the historical start even when auto-refresh is off, and catches up when you return to the tab; it does not replace or change normal polling. The selected preset is remembered in this browser.

Account/API key/request views, summaries, filter selectors, and hourly charts share this window. The local fallback hourly timeline has 25 chronological timestamp-based buckets: 24 past hours plus the current partial hour, with repeated daylight-saving hours kept separate. Manager Server-provided charts retain the server's existing daylight-saving bucketing behavior. Today, yesterday, and the existing longer presets keep their current calendar-day behavior. Displayed bounds stay attached to the data snapshot while a refresh is pending or fails.

## Common Investigation Flow

1. Select the time range first. Use a short range for active incidents and a larger range for trend review.
2. Check summary cards to see whether failures concentrate on a status code or model.
3. Open Account Overview to see whether only a few accounts are unhealthy.
4. Open API Key Summary to find abnormal callers.
5. Open a realtime request row and inspect model, provider, account, latency, tokens, and failure summary.
6. If the failure points to auth or quota, continue with [Accounts](./accounts.md) or [Codex Inspection](./codex-inspection.md).

## Filters

- **Status**: split successful, failed, and unusual status codes.
- **Provider and model**: find upstream or model-alias mistakes.
- **Account and auth file**: inspect one account over time with `auth_index`.
- **API Key**: find the caller behind traffic or cost spikes.
- **Project, request type, and Trace ID**: reconstruct one business request.
- **Latency and cache state**: inspect slow requests, cache misses, or streaming behavior.

Filters should lead to the next action. Cost issues go to [Usage Analytics](./usage-analytics.md). Auth issues go to [Account Action Queue](./account-actions.md).

Caller API keys remain masked. Copy appears only when the key still exists in the current configuration and can be read safely; historical or deleted keys are not restored.

## Empty Monitoring

If Monitoring has no data, check this order:

1. Client requests actually go through CPA.
2. CPA usage publishing is enabled.
3. The CPAMP collector is running.
4. CPA usage queue retention is long enough.
5. More than one Manager Server is not consuming the same CPA queue.
6. RESP mode connects directly to CPA `:8317`, not through an HTTP reverse proxy.

See [Request Monitoring Troubleshooting](../troubleshooting/request-monitoring.md).

## Data Boundaries

- Monitoring only shows events CPAMP collected. Expired queue data cannot be recovered.
- Multiple Manager Servers consuming one CPA queue can cause missing data.
- Cost is estimated from model prices, not provider billing.
- Sanitized summaries are useful for troubleshooting, but do not share tokens, management keys, or auth files.
