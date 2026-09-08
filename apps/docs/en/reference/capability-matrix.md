---
title: CPA Manager Plus Capability Matrix
description: Compare management, monitoring, cost, quota, inspection, automation, and plugin support across the CPAMP Lightweight Panel and Full Mode, then choose Docker or a native package for Full Mode.
---

# CPA Manager Plus Capability Matrix

Use this page to confirm which features each usage option provides and what evidence each provider can expose. Missing fields remain unknown; CPAMP does not convert missing data into a healthy state, unlimited quota, or success.

## By Usage Option

| Capability                                                    | CPAMP Lightweight Panel     | CPAMP Full Mode                                      |
| ------------------------------------------------------------- | --------------------------- | ---------------------------------------------------- |
| CPA config, providers, Accounts, OAuth, and logs              | ✅                          | ✅                                                   |
| Plugin management and plugin pages                            | Depends on CPA/plugin paths | ✅                                                   |
| Browser-local account inspection (`codex` / `xai` only)       | ✅                          | ✅                                                   |
| SQLite request history                                        | ❌                          | ✅                                                   |
| Request monitoring and failure diagnosis                      | ❌                          | ✅                                                   |
| Usage and cost analytics                                      | ❌                          | ✅                                                   |
| Model prices and API key aliases                              | ❌                          | ✅                                                   |
| Server account inspection and history                         | ❌                          | ✅ (`codex`/`xai`, plus read-only `claude` OAuth usage) |
| Quota cooldowns and account action queue                      | ❌                          | ✅                                                   |
| Login credential                                              | CPA Management Key          | CPAMP Admin Key                                      |
| Backups, migration state, and pprof                           | ❌                          | ✅                                                   |

The CPAMP Lightweight Panel is an enhanced WebUI hosted directly by CPA and requires no additional service, just like the official panel. It does not connect to or read Manager Server. Use the Manager Server `:18317/management.html` entry for the server-backed capabilities in this table.

> **The Live Demo is not a runtime mode.** It only previews the interface with fictional data. It cannot connect to, manage, or monitor a real CPA instance and does not mean the capabilities in this matrix are actually running.

## How To Install Full Mode

Docker and native packages provide the same Full Mode capabilities; only the installation method differs.

| Installation                    | Best for                              | Panel entry                                 |
| ------------------------------- | ------------------------------------- | ------------------------------------------- |
| Docker deployment (recommended) | Most new users and server deployments | `http://<cpamp-host>:18317/management.html` |
| Native package deployment       | Hosts where Docker is not used        | `http://<cpamp-host>:18317/management.html` |

- Docker users should follow [Docker Deployment](../deployment/docker.md).
- Linux, macOS, and Windows users should follow [Native Packages](../deployment/native.md).

## By Account And Provider

| Provider / account type              | Configuration                                                                                                      | Quota and health evidence                                             | Active inspection                                                                 |
| ------------------------------------ | ------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------- | --------------------------------------------------------------------------------- |
| Codex OAuth/Auth File                | Provider, auth file, OAuth, and model aliases                                                                      | Five-hour/weekly windows, reset, workspace, and credential state     | Local and server; eligible results can use controlled automatic actions          |
| Claude                               | Provider and OAuth/Auth File                                                                                       | Base quota, weekly quota, and model-scoped limits when returned      | Manager Server only, read-only OAuth usage inspection; no automatic actions      |
| xAI/Grok OAuth                       | Provider/Auth File/OAuth                                                                                           | CLI billing, paid OAuth identity fallback, and request-event evidence | Local and server; eligible results can use controlled automatic actions          |
| xAI API Key                          | `xai-api-key`, priority, models, and key testing                                                                   | Request results and provider responses                                | Provider key test                                                                 |
| Qwen / Qoder / iFlow                 | Existing CPA-exposed OAuth/auth-file workflows; Qoder device-code OAuth remains conditional on CPA support         | Auth-file state and recent request evidence                           | Remote inspection and active quota refresh unavailable: no verified safe provider contract |
| Gemini / Vertex / Antigravity / Kimi | Provider, auth file, or OAuth depending on CPA                                                                     | Provider-specific quota or recent request evidence                    | Depends on CPA and provider APIs                                                  |
| OpenAI-compatible                    | Base URL, API key, headers, model mapping, and key testing                                                         | Request status, latency, redacted failures, and cost                  | No assumed common quota API                                                       |

## Data And Automation Boundaries

- Browser-local inspection supports only `codex` and `xai`.
- Manager Server also provides read-only `claude` OAuth usage inspection. It sends no model or inference request and never automatically disables, enables, deletes, reauthenticates, or otherwise mutates credentials.
- Automatic inspection actions apply only to eligible `codex` and `xai` results, never `claude`.
- CPAMP only uses evidence returned by the provider, exposed by CPA, or safely extracted from request events.
- An HTTP `401/403` alone is not enough to auto-disable a credential; explicit credential semantics are required.
- Quota cooldowns, inspection, and account actions follow “the owner that disabled is the owner that may restore.”
- Manual disables are never overridden by unrelated automation.
- Cost is estimated from collected requests and model prices; provider billing remains authoritative.
- Requests missed after CPA queue expiry or while Manager Server is offline cannot be recovered.

## Related Guides

- [Account Inspection](../manual/codex-inspection.md)
- [Accounts And Credential Health](../manual/accounts.md)
- [Account Action Queue](../manual/account-actions.md)
- [Manager Server Guide](../operations/manager-server.md)
