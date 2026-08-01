---
title: Codex, Claude, And xAI Quota
description: Inspect quota, reset, provider evidence, and controlled CPAMP cooldown state for Codex, Claude, xAI/Grok, and other CPA accounts.
---

# Codex, Claude, And xAI Quota

Quota answers "can this account continue serving requests now?" It combines auth files, inspection results, failure summaries, response headers, and cooldown records to decide whether an account should pause or recover.

It is about account state, not cost. Use [Usage Analytics](./usage-analytics.md) for cost breakdowns.

Open the [Quota Demo](https://seakee.github.io/CPA-Manager-Plus/#/demo/quota) to inspect fictional Codex, Claude, and xAI windows.

## Before Opening It

Quota data depends on auth files and provider responses. Before using this page, confirm:

1. The account exists in [Auth Files](./auth-files.md) and has a stable `auth_index`.
2. Requests are flowing through CPA and recent requests exist.
3. For Codex accounts, run [Codex Inspection](./codex-inspection.md) when needed.

## Data Sources

Quota clues may come from:

- Account information from CPA providers or auth files.
- Codex inspection results.
- Failure summaries such as `usage_limit_reached`.
- Recent response headers that recorded quota or reset times.
- CPAMP quota cooldown records.

Different providers return different data. Unknown means CPAMP did not get enough information. It does not mean the account is unlimited.

## Provider Capability Overview

| Provider        | Possible evidence                                                                    | Boundary                                                                                            |
| --------------- | ------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------- |
| Codex           | Five-hour/weekly windows, reset, observed headers, workspace, and inspection state   | Fields depend on plan and API responses.                                                            |
| Claude          | Base quota, weekly quota, and model-scoped limits                                    | Scoped limits can be duplicated, missing, or inactive; CPAMP groups them by identity and freshness. |
| xAI/Grok OAuth  | CLI billing weekly/monthly data, official API identity, and request-event exhaustion | Paid API identity does not provide queryable cost or remaining percentages.                         |
| Other providers | CPA quota, auth-file metadata, or recent response headers                            | No common active quota API is assumed.                                                              |

### Paid xAI OAuth

Free Grok Build OAuth credentials can return weekly and monthly data through the CLI billing endpoints. OAuth credentials intended for the official `api.x.ai` API may receive `403 Access denied` from those endpoints, and there is currently no public paid-quota endpoint that CPAMP can query for this credential type.

When both CLI billing requests return a generic `403 Access denied` without a more specific subscription, entitlement, or quota signal, CPAMP uses the read-only `GET https://api.x.ai/v1/me` endpoint to check the official API identity. A successful response is shown as an “Official API” health state. CPAMP does not synthesize quota, cost, or remaining percentages and does not invoke a model. This state proves only that the OAuth identity is reachable; it does not verify chat routing or model access.

To route paid xAI OAuth requests through the official API in CPA, the auth JSON normally needs `using_api: true` together with `base_url: https://api.x.ai/v1`. Otherwise OAuth may continue using the Grok CLI chat proxy by default. Check the xAI console for actual cost and remaining quota.

### xAI Request-Monitoring Evidence

When an xAI request returns HTTP `402` or `429` with `subscription:free-usage-exhausted`, Monitoring structures the model, `actual/limit`, remaining amount, and overage from the error body. If the response describes a rolling 24-hour window without an explicit reset, recovery is estimated as 24 hours after the event and is clearly labeled as estimated. That estimate is an upper bound for cooldown scheduling, not a precise reconstruction of the rolling window. Explicit body fields such as `billing_period_end` or `reset_at` take precedence. Transport `Retry-After` only describes request retry backoff and does not override free-usage cooldown recovery.

`X-Ratelimit-Limit-*` and `X-Ratelimit-Remaining-*` on successful responses describe the current API request or token rate-limit window. They are throughput diagnostics, not the included Grok free-plan allowance, and CPAMP does not use them to calculate free-usage remaining.

Monitoring also exposes safe diagnostic signals such as `X-Request-Id`, `traceparent`, `X-Should-Retry`, `X-Data-Retention`, and `X-Zero-Retention`. `Set-Cookie`, API keys, and other token-like headers are not published. The same sanitized exhaustion evidence is attached to CPAMP-created cooldown records for troubleshooting in Auth Files.

## Page Actions

- Search by file name, account, note, or index.
- Sort by plan, quota state, or name.
- Refresh auth files and quota to reload accounts and queryable quota.
- Follow cooldown, reauth, or health hints into Auth Files, OAuth, or inspection pages.
- When comparing accounts, rely on `auth_index` and notes, not only file names.

## Quota Cooldown

When quota cooldown is enabled and a supported account reaches a strict quota signal, CPAMP can temporarily disable the related auth file and recover it after the reset time. Supported signals currently include Codex `usage_limit_reached` with an explicit reset and xAI `subscription:free-usage-exhausted`, which uses the documented rolling 24-hour recovery window.

Cooldown records include a reason code and window kind. Current window kinds are `five_hour`, `weekly`, `monthly`, `rolling_24h`, and `unknown`. For example, if a Codex five-hour limit is exhausted while the weekly limit remains available, only the five-hour window controls the cooldown. After recovery, the credential can re-enter CPA scheduling without waiting for the weekly window.

Notes:

- Enable it with `USAGE_QUOTA_COOLDOWN_ENABLED` or the Configuration switch.
- Auto-restore depends on CPAMP continuing to run.
- While the CPAMP cooldown is active, the credential is disabled in CPA and is not selected for new requests.
- Quota cooldown restores only credentials disabled by that cooldown record. Manual, inspection-owned, or auth-failure disables are not overridden.
- Unstable `auth_index` values can prevent accurate account binding.

Quota cooldown is for clear quota exhaustion. It is not a good tool for expired login, upstream bans, or configuration errors. Use [Account Action Queue](./account-actions.md) or [OAuth Login](./oauth.md) for those.

## Troubleshooting

If an account looks usable but requests fail:

1. Read the failure summary in Monitoring.
2. Check whether Account Inspection reports Codex/xAI quota, workspace, billing, or auth issues.
3. Check Auth Files for manual disabled state or cooldown.
4. Check whether the account action queue has pending candidates.
5. If the page has no quota data, confirm whether that provider supports active quota lookup or only passive header observation.
