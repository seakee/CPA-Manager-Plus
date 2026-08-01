# Providers And Compatibility Reference

To add, edit, or test a model service, go directly to [AI Providers](../manual/ai-providers.md). This page is a reference for how provider families and client interfaces relate.

## Common Providers

| Provider type     | Common use                                  | Related pages                               |
| ----------------- | ------------------------------------------- | ------------------------------------------- |
| Codex             | Codex CLI, accounts, and quota              | AI Providers, Auth Files, Inspection        |
| Claude            | Claude Code and compatible calls            | AI Providers, Auth Files, Monitoring        |
| OpenAI-compatible | Relays, self-hosted, or compatible services | AI Providers, Model Prices, Usage Analytics |
| Gemini / Vertex   | Google models and project credentials       | AI Providers, OAuth, Auth Files             |
| xAI / Grok        | API key or OAuth accounts                   | AI Providers, Quota, Inspection             |

When adding a provider, confirm four things first: base URL, authentication method, model names used by clients, and the account or auth file binding.

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
