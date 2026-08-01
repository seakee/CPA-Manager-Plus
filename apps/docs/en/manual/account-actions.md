---
title: Account Action Queue And Safe Auto-Disable
description: Review, disable, re-enable, resolve, or delete problem accounts from sanitized credential evidence without treating every 401/403 as invalid credentials.
---

# Account Action Queue And Safe Auto-Disable

Account Action Queue centralizes auth-related candidates detected from Monitoring. It is not a request-details page. It lists accounts that may need human action.

If you need the failed request itself, start with [Monitoring](./monitoring.md).

Open the [Account Action Queue Demo](https://seakee.github.io/CPA-Manager-Plus/#/demo/monitoring/account-actions) to inspect fictional candidates and actions.

## When Candidates Appear

When Monitoring captures revoked tokens, invalid OAuth, auth-file failures, or similar signals, CPAMP can add the account to the candidate queue. Realtime request responses and Codex Inspection use the same credential-failure policy so the same error does not produce conflicting actions.

The queue is controlled by `USAGE_ACCOUNT_ACTIONS_ENABLED` or the Configuration switch. Automatic disable also requires `USAGE_ACCOUNT_ACTIONS_AUTO_DISABLE`, and depends on the queue being enabled.

## Automation Boundaries

CPAMP never auto-disables an account from the HTTP status alone. `401` and `403` are inputs, but the error code, response body, and safe response headers must provide a sufficiently explicit credential signal:

- Explicit invalid, expired, or revoked credentials, such as `invalid_token`, `Invalid or expired credentials`, or `no auth context`: suggest reauthorization and allow auto-disable.
- An xAI response that explicitly rejects the current credential for the chat endpoint with exact credential or permission guidance; `X-Should-Retry: false` strengthens the signal: suggest review and allow auto-disable.
- Region restrictions, model permissions, or ambiguous `permission-denied`: review only, without auto-disable.
- An explicitly deactivated account or workspace: suggest deleting the stale auth file and allow auto-disable while it awaits handling.

Do not treat every `401/403` as an auto-disable rule. Codex and xAI are both classified by response semantics, not status code alone.

## Fields

- **Account**: account identity resolved from request events, auth files, or `auth_index`.
- **Auth file**: related file name.
- **Suggested action**: reauthorize, review, or delete a stale auth file.
- **Reason code**: stable machine classification for invalid credentials, permission review, deactivation, and related cases.
- **Auto-disable state**: whether the candidate is eligible for auto-disable and whether CPAMP already disabled it.
- **Reason**: CPAMP's summary of why the candidate exists.
- **First seen / last seen**: whether the issue is persistent.
- **Hits**: how often the signal appeared.
- **Status**: pending, ignored, resolved, or failed.
- **Evidence**: sanitized failure summary and related request context.

## Handling Candidates

1. Open evidence first and confirm the issue is not a one-off upstream error.
2. If the account was already reauthorized or handled manually, mark it resolved.
3. If the candidate is a false positive or not worth action, ignore it.
4. If an auth file was disabled but is now safe to use, enable it.
5. Delete an auth file only after confirming the account is invalid and no longer used.

Before deleting, confirm that the same auth file is not used by another model or project. Delete affects history and future tracking.

## Related Pages

- Need failure details: [Monitoring](./monitoring.md).
- Need reauth: [OAuth Login](./oauth.md).
- Need account enable/disable: [Auth Files](./auth-files.md).
- Need Codex/xAI quota or state: [Account Inspection](./codex-inspection.md).

## Usage Advice

Use this queue for recurring auth-class problems. It is not meant for one-off network errors, upstream 5xx, or missing model names. If unsure, ignore or observe first; do not delete immediately.

Recovery follows an ownership rule: quota cooldown restores only credentials it disabled; inspection restores only inspection-owned disables; auth-failure disables are not restored by either worker and require reauthorization or manual handling.
