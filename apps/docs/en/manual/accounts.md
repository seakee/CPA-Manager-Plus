---
title: Accounts And Credential Health
description: Manage CPA credentials, account health, quota windows, inspection results, cooldowns, and safe bulk actions in the unified Accounts workspace.
---

# Accounts And Credential Health

`/accounts` is CPAMP's unified credential-management entry. It combines the former auth-file, account-quota, and inspection state into one workspace that answers: which accounts exist, whether they are enabled, whether they can serve requests now, and which accounts need reauthentication or manual review.

Use [OAuth Login](./oauth.md) to add OAuth accounts. Import, editing, status decisions, and troubleshooting after the credential is saved belong in Accounts.

Open the unified [Accounts Demo](https://seakee.github.io/CPA-Manager-Plus/#/demo/accounts) to inspect fictional credentials, quota, inspection, and automation state.

## What To Check First

- **Credential and provider**: confirm whether the account belongs to Codex, Claude, Vertex, Antigravity, Kimi, xAI, Qoder, or another source.
- **`auth_index`**: the stable account index used to connect usage, quota, inspection, and account actions.
- **Enabled state**: automated recovery does not override manually disabled accounts.
- **Note, priority, and project ID**: use them to separate account purpose and routing preference.
- **Account health**: combines credential state, the latest inspection, Provider queries, response Headers, cooldowns, and pending actions.
- **Quota windows**: shows confirmed usage, reset, source, and observation time. Unknown does not mean unlimited.

Keep `auth_index` stable in multi-account deployments. File-name-only identity is not sufficient for reliably joining history and operational state to one account.

## Common Actions

- Refresh the credential list or explicitly refresh Provider quota for one credential.
- Paste JSON, upload, download, edit, disable, restore, or delete credentials.
- Filter by search, provider, state, plan, quota window, or operational state.
- Batch change priority or enabled state, and toggle WebSockets for selected Codex credentials.
- Inspect overview, configuration, supported models, quota history, and diagnostic evidence.
- Run local or Manager Server Codex/xAI inspection from the health workspace.

If you are unsure whether an account is still needed, disable it before deleting it. Disable preserves history joins; deletion makes later inspection, quota, and action tracking harder.

## Add Or Update Credentials

1. Complete [OAuth Login](./oauth.md) for OAuth accounts.
2. Paste or upload JSON when credentials already exist.
3. Confirm the provider, account, file name, and `auth_index` after saving.
4. Send one low-cost request.
5. Open [Monitoring](./monitoring.md) and confirm the request used the expected account.

Official Sub2API multi-account exports are converted in the browser into independent CPA Codex credentials. Empty exports, malformed fields, and partial upload failures produce explicit results; a top-level array is not silently saved as one ordinary credential file.

## Quota And Health Evidence

Account state may come from:

- Provider quota queries explicitly started by the user.
- Local or Manager Server Codex/xAI inspection results.
- Safe response Headers from recent successful requests.
- Failure summaries such as `usage_limit_reached`, HTTP `401`, `402`, or `429`.
- Manager Server quota cooldowns and account-action candidates.

Provider quota refresh remains explicit. Opening Accounts, reading history, or passively loading Header evidence does not poll upstream quota endpoints.

CPAMP reconciles evidence by credential identity and observation time. Newer healthy evidence can supersede older reauth, quota-limit, cooldown, and action-candidate state; a newer `401` or explicit quota exhaustion remains authoritative. After reauthentication, inspection and quota evidence from the replaced credential cannot reattach to the new credential.

| Provider        | Possible evidence                                                              | Boundary                                                                                            |
| --------------- | ------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------- |
| Codex           | Five-hour/weekly windows, reset, Headers, workspace, and inspection state      | Fields depend on plan and API responses.                                                            |
| Claude          | Base quota, weekly quota, and model-scoped limits                              | Scoped limits can be duplicated, missing, or inactive; CPAMP groups them by identity and freshness. |
| xAI/Grok OAuth  | CLI billing weekly/monthly data, official API identity, and request exhaustion | Official API identity does not provide queryable cost or remaining percentages.                     |
| Qoder OAuth     | Auth-file state, recent requests, and response Headers                         | No generic active quota lookup is assumed at this stage.                                            |
| Other providers | CPA credential metadata or recent response Headers                             | No common active quota API is assumed.                                                              |

## Quota Cooldown And Account Actions

When quota cooldown is enabled and a supported account reaches a strict exhaustion signal, CPAMP can temporarily disable the related credential and restore it after recovery. Auto-restore only affects credentials disabled by that cooldown record; it does not override manual, inspection-owned, or authentication-failure disables.

Quota cooldown is for clear quota exhaustion, not expired login, upstream bans, or configuration errors. Use [Account Action Queue](./account-actions.md) or [OAuth Login](./oauth.md) for those cases.

## Troubleshooting Order

1. Read status codes and sanitized failure summaries in [Monitoring](./monitoring.md).
2. Check whether the credential is manually disabled, needs reauth, or is cooling down.
3. Run [Account Inspection](./codex-inspection.md) and review provider, workspace, billing, and authentication evidence.
4. Check [Account Action Queue](./account-actions.md) for pending candidates.
5. If quota is unavailable, confirm whether the provider supports active lookup or only passive Header observation.

Use [Usage Analytics](./usage-analytics.md) for cost breakdowns; cost is not part of credential management.

## Security Notes

Credential files contain sensitive data. Do not share full JSON, OAuth tokens, API keys, Management Keys, `Set-Cookie`, or unredacted Authorization Headers. For troubleshooting, share sanitized request summaries, account-state screenshots, and log timestamps.
