---
title: Auth Files And Account State
description: Manage CPA auth files, Sub2API multi-account imports, priority, quota health, automation state, and safe bulk actions.
---

# Auth Files And Account State

Auth Files manages account credentials and account state. It answers: which accounts exist, whether they are enabled, which `auth_index` they use, and whether recent quota or auth issues were observed.

Use [OAuth Login](./oauth.md) to add new OAuth accounts. This page is for maintenance after credentials exist.

Open the [Auth Files Demo](https://seakee.github.io/CPA-Manager-Plus/#/demo/auth-files) to inspect fictional Codex, Claude, and xAI quota, inspection, and automation state.

## What To Check First

- **File name and provider type**: confirm whether the account belongs to Codex, Claude, Vertex, Antigravity, Kimi, or another source.
- **`auth_index`**: the stable account index. Usage, quota, inspection, and account actions all depend on it.
- **Enabled state**: manually disabled accounts are not restored automatically.
- **Note, priority, and project ID**: use them to separate account purpose and routing preference.
- **Quota and health hints**: cooldown, reauth required, manual review, auto-disable, quota windows, or recently observed response headers.

In multi-account setups, stable `auth_index` values are mandatory. Without them, history, quota, inspection, and actions are hard to connect to the right account.

## Common Actions

- Refresh auth files and account state.
- Paste JSON or upload auth files.
- Download, edit, disable, restore, or delete auth files.
- Use search, sort, page size, and display mode to find accounts.
- Filter by Codex status, plan type, problem-only view, or healthy-only view. Account-action candidates and quota cooldowns participate in health classification.
- Batch edit priority, notes, project ID, or enabled state.
- View supported models to decide whether an account should handle a target model.
- Open prefix proxy settings and copy client-facing proxy URLs.

If you are not sure whether an account is still needed, disable it first instead of deleting it. Disable keeps history connected. Delete makes later inspection, quota, and action tracking harder.

## Add Or Update Auth Files

1. For OAuth accounts, complete [OAuth Login](./oauth.md) first.
2. If you already have JSON, paste or upload it.
3. After saving, return to the list and confirm file name, provider type, and `auth_index`.
4. Send one low-cost request.
5. Open [Monitoring](./monitoring.md) and confirm the request used the expected account.

When pasting JSON, choose the format that matches the source. JSON formats differ by source, and a successful save does not guarantee the account can serve requests.

### Sub2API Multi-Account Import

When you upload an official Sub2API account export, CPAMP detects and converts its OpenAI OAuth accounts in the browser, then uploads one independent CPA Codex auth file per account. Pasted Sub2API JSON uses the same conversion flow. Multi-account imports generate stable, unique file names instead of saving a top-level JSON array as one auth file.

Conversion requires supported OpenAI OAuth accounts with an `access_token`. Empty exports, malformed fields, and partial upload failures produce explicit results; the original export is not silently saved as an ordinary CPA auth file.

## Handling Problem Accounts

- **Needs reauth**: open [OAuth Login](./oauth.md), then confirm state here.
- **Quota exhausted or cooling down**: open [Quota](./quota.md) to check reset time and cooldown source.
- **Codex/xAI state is abnormal**: open [Account Inspection](./codex-inspection.md) for provider-specific evidence and suggested actions.
- **Requests fail while the account looks fine**: open [Monitoring](./monitoring.md) and read the failure summary and actual model.
- **Account Action Queue has a candidate**: open [Account Action Queue](./account-actions.md) and decide whether to ignore, resolve, enable, or delete.

Bulk delete in problem-only view uses a safe subset. Cooldowns, reauth candidates, and review-only candidates are excluded. Only explicit delete candidates, or auth files with their own deletable problem state, are included.

## Security Notes

Auth files contain sensitive credentials. Do not share full JSON, OAuth tokens, API keys, or management keys. For troubleshooting, share sanitized monitoring summaries, account-state screenshots, and log timestamps instead.
