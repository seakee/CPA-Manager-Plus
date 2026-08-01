---
title: CPA Usage And Cost Analytics
description: Analyze requests, tokens, cache, cost, latency, and failures by model, account, API key, provider, project, and time range.
---

# CPA Usage And Cost Analytics

Usage Analytics answers "where did the money go?" and "which requests caused the abnormal pattern?" It uses request-monitoring events and [Model Prices](./model-prices.md). It does not change provider billing.

Open the [Usage Analytics Demo](https://seakee.github.io/CPA-Manager-Plus/#/demo/usage-analytics) to browse fictional Overview, trends, models, API keys, credentials, and heatmap data.

## Pick The Range First

Start with time range and granularity:

- Today or the last hour is best for a recent incident.
- 7 days or 30 days is better for trend review.
- Custom range is useful for billing periods or incident windows.
- Finer granularity helps find spikes. Coarser granularity helps read trends.

Filters include model, API key, provider, status, auth file, latency, and cache state. Once filters are applied, trend charts, rankings, and preview rows all use the same request set.

## Main Views

- **Overview**: request count, tokens, cost, failure rate, and latency.
- **Model ranking**: the most expensive, most active, or least healthy models.
- **API Key ranking**: callers responsible for cost or failures.
- **Credential ranking**: account-level usage, useful with quota and inspection.
- **Trend charts**: request volume, cost, tokens, and failure rate over time.
- **Anomaly points**: sudden changes in cost, tokens, or failures.
- **Heatmap**: peak and quiet hours for scheduling decisions.
- **Request preview**: jump back to Monitoring for individual requests.

## Cost Spike Workflow

1. Check whether only cost increased, or whether request count and failure rate also increased.
2. Open model ranking to find expensive model concentration.
3. Open API Key ranking to find unusual callers.
4. Open credential ranking to find account or project concentration.
5. Jump to request details and confirm model names, tokens, and caller.

If the model name is an alias or internal name, add the matching entry in [Model Prices](./model-prices.md), or cost will be underestimated or empty.

## Long Histories And Query Behavior

- Each tab requests only the data it needs; stable filter selectors load separately from main analytics.
- Strictly unfiltered long-range statistics can use incremental hourly rollups, while search, provider, account, latency, cache, and other complex filters continue to read raw events.
- Credential timelines load only after a credential is selected.
- When a rollup checkpoint is pending, a timezone cannot be represented losslessly, or a rollup read fails, the query falls back to raw events to preserve correctness.
- Historical cost may be recalculated with current model prices.

## Accuracy Boundary

- Provider bills are the source of truth.
- CPAMP estimates cost from request events and model prices.
- If model names are rewritten by clients, providers, or route aliases, maintain the corresponding name in Model Prices.
- Missing token fields can make cost incomplete.
- Requests lost while Manager Server was stopped or queue data expired cannot be reconstructed.
- Performance depends on data volume, filter complexity, disk, and SQLite state. See the [Performance Report](../operations/performance-optimization-2026-07-10.md) for diagnostic details.
