# Auth Files

Auth Files manages account credentials and account state. It answers: which accounts exist, whether they are enabled, which `auth_index` they use, and whether recent quota or auth issues were observed.

Use [OAuth Login](./oauth.md) to add new OAuth accounts. This page is for maintenance after credentials exist.

## What To Check First

- **File name and provider type**: confirm whether the account belongs to Codex, Claude, Vertex, Antigravity, Kimi, or another source.
- **`auth_index`**: the stable account index. Usage, quota, inspection, and account actions all depend on it.
- **Enabled state**: manually disabled accounts are not restored automatically.
- **Note, priority, and project ID**: use them to separate account purpose and routing preference.
- **Quota and health hints**: cooldown, reauth needed, quota windows, or recently observed response headers.

In multi-account setups, stable `auth_index` values are mandatory. Without them, history, quota, inspection, and actions are hard to connect to the right account.

## Common Actions

- Refresh auth files and account state.
- Paste JSON or upload auth files.
- Download, edit, disable, restore, or delete auth files.
- Use search, sort, page size, and display mode to find accounts.
- Filter by Codex status, plan type, or problem-only view.
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

When you directly upload an official sub2api account export, CPAMP detects and converts its OpenAI OAuth accounts in the browser, then uploads one independent CPA Codex auth file per account. Pasted sub2api JSON uses the same conversion flow. Multi-account imports generate per-account file names instead of saving a top-level JSON array as one auth file.

## Handling Problem Accounts

- **Needs reauth**: open [OAuth Login](./oauth.md), then confirm state here.
- **Quota exhausted or cooling down**: open [Quota](./quota.md) to check reset time and cooldown source.
- **Codex state is abnormal**: open [Codex Inspection](./codex-inspection.md) for suggested actions.
- **Requests fail while the account looks fine**: open [Monitoring](./monitoring.md) and read the failure summary and actual model.
- **Account Action Queue has a candidate**: open [Account Action Queue](./account-actions.md) and decide whether to ignore, resolve, enable, or delete.

## Security Notes

Auth files contain sensitive credentials. Do not share full JSON, OAuth tokens, API keys, or management keys. For troubleshooting, share sanitized monitoring summaries, account-state screenshots, and log timestamps instead.
