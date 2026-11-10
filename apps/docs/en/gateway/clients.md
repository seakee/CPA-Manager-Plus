# Client Configuration

Clients need the CPA address and a normal CPA API Key. Do not point Codex, Claude Code, OpenCode, or OpenAI SDKs at CPAMP; CPAMP observes and manages, but it does not serve normal model traffic.

## General Rules

| Item       | Recommendation                                                                            |
| ---------- | ----------------------------------------------------------------------------------------- |
| Base URL   | The public CPA address, such as `https://gateway.example.com` or `http://localhost:8317`. |
| API Key    | Use a normal CPA API Key. Do not use the CPAMP Admin Key or CPA Management Key.           |
| Model      | Use the model name or alias exposed by CPA providers.                                     |
| Monitoring | CPAMP can collect and display events only when requests pass through CPA.                 |

With a same-domain reverse proxy, client requests to `/v1/*`, `/v1beta/*`, and `/backend-api/codex/*` should go to CPA. Sending normal model requests to CPAMP only creates extra 401, 404, or 428 troubleshooting.

## Codex

Before connecting Codex, prepare:

- Base URL pointing to CPA's Codex-compatible entry.
- A normal CPA API Key.
- A Codex provider or auth file configured in CPA.
- Stable `auth_index` and recognizable account metadata if you want account inspection.

When Codex requests fail, start with the failure summary in [Monitoring](../manual/monitoring.md). If it looks like an account or quota issue, continue with [Codex Inspection](../manual/codex-inspection.md).

## Claude Code

Before connecting Claude Code:

- CPA has a Claude Code provider or compatible provider.
- OAuth or auth file setup is complete.
- The client uses the CPA public address and a normal API Key.

OAuth success only means authentication completed. If requests still fail, check account state in Accounts first, then read the Monitoring failure summary.

## OpenCode And General OpenAI Clients

OpenCode, OpenAI SDKs, and most relay clients use the OpenAI-compatible `/v1/...` API:

```text
Base URL: https://gateway.example.com/v1
API Key:  normal CPA API Key
Model:    model name or alias exposed by CPA
```

Behind a reverse proxy, a 401 from `/v1/models` is usually a CPA API Key problem. A 401 from `/v0/management/config` is a CPAMP management login problem.

## Factory Droid And Other Tools

Any tool that accepts an OpenAI-compatible or Gemini-compatible endpoint can use the API exposed by CPA. The important part is that traffic passes through CPA; only then can CPAMP observe request events, cost, and account health.
