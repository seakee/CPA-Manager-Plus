# Providers And Compatibility Reference

To add, edit, or test a model service, go directly to [AI Providers](../manual/ai-providers.md). This page is a reference for how provider families and client interfaces relate.

## Common Providers

| Provider type     | Common use                                  | Related pages                               |
| ----------------- | ------------------------------------------- | ------------------------------------------- |
| Codex             | Codex CLI, accounts, and quota              | AI Providers, Accounts, Inspection          |
| Claude            | Claude Code and compatible calls            | AI Providers, Accounts, Monitoring          |
| OpenAI-compatible | Relays, self-hosted, or compatible services | AI Providers, Model Prices, Usage Analytics |
| Gemini / Vertex   | Google models and project credentials       | AI Providers, OAuth, Accounts               |
| xAI / Grok        | API key or OAuth accounts                   | AI Providers, Accounts, Inspection          |
| Muse / Meta       | Device Flow account or `meta-api-key` inference provider | AI Providers, OAuth, Accounts     |

When adding a provider, confirm four things first: base URL, authentication method, model names used by clients, and the account or auth file binding.

Muse / Meta requires an extra credential distinction: OAuth Device Flow produces a DCA for account authentication and quota reads, while model inference uses a separate `meta-api-key`. Full support requires CPA `v7.3.4+`.

## When Requests Fail

1. Run an available model or key test from AI Providers.
2. Check whether the auth file is disabled or out of quota.
3. Send a low-cost real request.
4. Read the status code and sanitized failure summary in Monitoring.
5. Use Logs only after those checks.

::: details Advanced: compatibility APIs and reverse proxy

Common client interfaces include:

- `/v1/...` for OpenAI-compatible clients.
- `/v1beta/...` for Gemini-compatible clients.
- `/backend-api/codex/...` for Codex CLI.
- Provider callback paths for OAuth login.

Model requests must go to CPA, not CPAMP. For same-domain routing, see [Reverse Proxy](../deployment/reverse-proxy.md).

Model prices affect CPAMP local cost estimates only. They do not change CPA routing or provider billing.

:::

### xAI weekly periods with zero usage

Some unified-billing accounts omit `creditUsagePercent` when usage is zero or rounds to zero.
The panel and server inspection accept an implicit 0% used only when settings confirm SuperGrok / SuperGrok Heavy
and a complete, successful gRPC billing response from the same credential matches the active REST weekly period.
The existing remaining-quota display then shows 100%. This does not prove that no tokens were consumed
and cannot be converted into a remaining token count.

REST period metadata alone, Free / unconfirmed plans, and expired or incomplete responses remain unknown (`-`).
The optional reads use the existing CPA management proxy, add at most six seconds, and never turn an enrichment failure
into an account health failure or an automatic account action.
