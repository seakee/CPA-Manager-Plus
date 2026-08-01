---
title: Model Prices And Cost Estimation
description: Configure model prices, service tiers, long-context multipliers, and cache read/write/creation billing for local CPAMP cost estimates.
---

# Model Prices And Cost Estimation

Model Prices maintains CPAMP's local cost-estimation rules. It affects Dashboard, Monitoring, and Usage Analytics; it does not change provider billing or CPA routing.

Open the [Model Prices Demo](https://seakee.github.io/CPA-Manager-Plus/#/demo/model-prices) to inspect fictional prices and model usage.

## Price Sources

- Public metadata synchronized from models.dev first, with LiteLLM and OpenRouter used as fallbacks when the preferred source is unavailable or lacks a model.
- Local prices added or overridden by the user.
- Entries for aliases, internal names, or provider-specific variants.

Synchronization only occurs when the user triggers it and may use the current Manager Server proxy configuration.

Automatic matching runs strictly in models.dev, LiteLLM, then OpenRouter order. CPAMP uses the canonical model metadata in the models.dev catalog to prefer the first-party official entry. A source is saved automatically only when it has one clear, strong identity match; fuzzy similarities are never auto-confirmed. An ambiguous source falls through to the next source. If none of the three sources yields a unique match, the confirmation list keeps candidates from each source separately, even when they share the same original model ID.

The current sync maps models.dev `cost.input`, `cost.output`, `cost.cache_read`, and `cost.cache_write`, converts valid `cost.tiers` context tiers into CPAMP billing rules, and maps `experimental.modes.fast.cost` to short-context Fast/Priority prices. The complete model object remains available in raw metadata; reasoning prices, unknown experimental modes, unknown tier types, and rules that cannot be validated safely do not activate automatic billing.

### Sync failures and last-known-good prices

- When models.dev is temporarily unavailable, CPAMP continues with LiteLLM and OpenRouter.
- A transient models.dev failure cannot automatically replace a stored models.dev price with a lower-priority source; fallback sources may still fill models that have no local price.
- When models.dev responds successfully but has no official entry or remains ambiguous, fallback sources are tried in order; only a unique strong identity match may replace the model.
- If every source fails, synchronization stops before any database write and existing prices remain unchanged.
- A synchronized price remains the last-known-good value until a later successful sync or a manual edit; `syncedAtMs` indicates its freshness.

## Supported Billing Semantics

A price rule may include:

- Input and output tokens.
- Reasoning tokens.
- Cache read, cache write, and cache creation.
- Fixed per-request cost.
- `service_tier` differences.
- models.dev context-price tiers.
- Long-context thresholds and multipliers.
- Model alias and billing-model mapping.

Models such as GPT-5.6 may vary by context length, service tier, and cache type. CPAMP can only apply a rule when both the request event and price entry contain the required fields.

### Context-tier semantics

- A tier matches only when normalized input tokens are **strictly greater than** `tier.size`; an exact-threshold request stays in the lower band.
- When multiple tiers match, CPAMP selects the highest matching threshold.
- The selected tier's rates apply to the entire request, not only to tokens above the threshold.
- Input, output, or cache rates omitted by a tier inherit the base price; an explicit zero from models.dev remains zero.
- CPAMP currently activates only safely validated tiers with `tier.type = context` and a positive threshold. Other rules remain in raw metadata for inspection.

### Fast/Priority semantics

- `experimental.modes.fast.cost` matches both `fast` usage telemetry and API `priority` telemetry.
- Short-context requests prefer explicit Fast/Priority prices. Missing fields inherit base rates, while explicit zeros remain zero.
- A matched context tier or the legacy GPT long-context rule uses its standard context price without stacking Fast/Priority pricing.
- Non-models.dev entries, older data, and models without an explicit mode price retain the existing multiplier as a compatibility fallback.

Model Prices displays synchronized context tiers and service-tier prices as read-only rules. The current manual editor manages base prices only; saving a manual price explicitly clears existing synchronized advanced rules, with a warning shown before saving.

## Matching Model Names

The client model, CPA alias, provider model, and price-table name may differ. When cost is missing:

1. Inspect the event model and billing model in [Monitoring](./monitoring.md).
2. Search for matching entries in Model Prices.
3. Add a local alias or override when required.
4. Refresh [Usage Analytics](./usage-analytics.md).

## Usage Summary

The page uses a compact model-usage summary to show which prices are active. It does not download full request history just to count model calls.

## Accuracy Boundaries

- Provider billing remains authoritative.
- Missing token, service tier, long-context, or cache fields reduce estimate accuracy.
- Subscriptions, grants, non-context tiers, unsupported dynamic-mode prices, and multiple currencies may not fit a single price entry.
- Historical cost may be displayed using current prices after an update; the price table is not an immutable billing snapshot.
