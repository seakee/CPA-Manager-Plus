---
title: Account Inspection (Codex / xAI)
description: Inspect Codex and xAI accounts locally or on a Manager Server schedule, including quota, credentials, workspace, billing evidence, and controlled actions.
---

# Account Inspection (Codex / xAI)

Account inspection explains why an account cannot reliably serve requests. The route and parts of the UI still use the `Codex Inspection` name, but inspection targets now include Codex and xAI.

Open the [Account Inspection Demo](https://seakee.github.io/CPA-Manager-Plus/#/demo/codex-inspection) to inspect fictional results without contacting a provider.

For a single failed request, start with [Monitoring](./monitoring.md). Use inspection after the problem is narrowed to an account, credential, or quota signal.

## Local And Server Inspection

- **Local inspection** runs in the current browser session and is useful for a few accounts or temporary diagnostics.
- **Server inspection** runs in Manager Server and supports schedules, history, logs, and shared action policy.

Before server inspection, confirm the CPA URL, CPA Management Key, auth files, and stable `auth_index` values.

## Codex Evidence

- Plan, five-hour/weekly quota windows, reset, and remaining quota.
- OAuth token and credential state.
- Workspace availability or deactivation.
- Explicit evidence such as `usage_limit_reached`.
- Recommendations to keep, reauthorize, disable, enable, or delete.

Missing fields remain unknown; they are not converted into healthy or unhealthy states.

## xAI Evidence

xAI inspection prefers read-only evidence that does not send a model inference request:

- Grok Build / CLI OAuth may expose weekly quota, monthly billing, and account state.
- Free-usage exhaustion events can enter a controlled rolling 24-hour cooldown.
- When paid `api.x.ai` OAuth cannot access CLI billing, a read-only identity endpoint can verify official API identity.
- Identity success proves only access to the identity endpoint; it does not prove a specific model, chat route, cost, or remaining quota.
- Ambiguous `403`, regional restrictions, and model permissions are not automatically treated as invalid credentials.

## Results And Actions

- **Keep**: no sufficient evidence requires action.
- **Reauthorize**: OAuth or credential state is explicitly invalid.
- **Review**: evidence is incomplete or may involve region, permission, or model scope.
- **Disable**: the account should not receive new requests and policy permits the action.
- **Enable**: the same inspection automation disabled the credential and now has explicit recovery evidence.
- **Delete**: only after the account is clearly invalid, the file is not shared, and the user confirms.

Read the provider, reason code, redacted evidence, and recent request behavior together with the action label.

## Scheduling And Automation Boundaries

Server inspection can run at fixed intervals or a daily time. Start with record-only or conservative actions before enabling automatic disables.

All restores follow “the owner that disabled is the owner that may restore”:

- Inspection only restores credentials disabled by inspection.
- Quota cooldown only restores credentials disabled by that cooldown record.
- Manual and account-action disables are not overridden by inspection.

## Related Pages

- Quota windows and cooldowns: [Quota](./quota.md)
- OAuth reauthorization: [OAuth Login](./oauth.md)
- Auth file state: [Auth Files](./auth-files.md)
- Repeated credential failures: [Account Action Queue](./account-actions.md)
- Individual request evidence: [Monitoring](./monitoring.md)
