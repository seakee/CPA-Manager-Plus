---
title: AI Provider Management
description: Manage Gemini, Codex, Claude, Vertex, xAI, and OpenAI-compatible CPA providers with priorities, models, proxies, headers, and key testing.
---

# AI Provider Management

The AI Providers page controls how CPA routes client requests to upstream model services. These are CPA provider settings, not the CPAMP admin login or CPA Management Key.

Open the [AI Providers Demo](https://seakee.github.io/CPA-Manager-Plus/#/demo/ai-providers) to inspect fictional configuration without contacting a real provider.

## Most Common Workflow

1. Select Add and choose a provider type.
2. Enter the base URL and credential, or choose the related OAuth / Auth File flow.
3. Enter model names or aliases that clients should use.
4. Save and run an available model or key test.
5. Send a low-cost real request and confirm it in [Monitoring](./monitoring.md).

## Supported Configuration Types

- Gemini / AI Studio
- Codex API Key
- Claude API Key
- Vertex
- xAI API Key
- OpenAI-compatible
- Other compatible configuration exposed by the current CPA version

Fields vary by provider, but they generally cover the base URL, credential, models, headers, proxy, priority, and enabled state.

## Add Or Edit A Provider

1. Select the provider type.
2. Enter the base URL and API key, or use the matching OAuth/Auth File workflow.
3. Use a clear name; when binding an auth file, keep the account identifier `auth_index` stable.
4. Configure model lists, aliases, exclusions, and provider-specific options.
5. Save, then run a low-cost key test or real request.
6. Confirm the provider, model, and account in [Monitoring](./monitoring.md).

## xAI API Keys

The panel manages CPA `xai-api-key` entries, including:

- API key, base URL, proxy, and custom headers.
- Models, aliases, prefixes, and excluded models.
- Provider priority and enabled state.
- Provider test actions for credential and model access.

An xAI API key is different from an xAI/Grok OAuth auth file. See [OAuth Login](./oauth.md), [Quota](./quota.md), and [Account Inspection](./codex-inspection.md) for OAuth, billing evidence, and account health.

## Priority And Concurrent Saves

Provider priority can be edited directly in the table. It affects routing order supported by CPA; it is not a health or cost priority.

CPAMP uses targeted update APIs where available and refreshes configuration caches after saves. Reload before editing if another browser or process may have changed the same configuration.

## Model And Key Testing

- Model discovery uses the provider base URL, API key, headers, and proxy.
- OpenAI-compatible, Codex, Claude, and xAI entries can expose key or model testing depending on the current panel and CPA capability.
- A successful test proves only that test request; it does not guarantee every model, region, account state, or future quota window.
- Inspect failures only in the authenticated local panel. Never paste API keys or raw credentials into an issue.

## Verify After Saving

1. Send a low-cost real request.
2. Confirm provider, model, account, and status in Monitoring.
3. If cost is missing, check [Model Prices](./model-prices.md).
4. For credential or quota failures, check [Auth Files](./auth-files.md), [Quota](./quota.md), and the [Account Action Queue](./account-actions.md).
5. If no event appears, troubleshoot collection before repeatedly changing the provider.

## Configuration Boundaries

- Provider configuration controls CPA upstream requests, not CPAMP authentication.
- Client aliases, provider model names, and price-table names may differ and require separate mapping.
- Do not mix CPA Management Keys, CPAMP Admin Keys, and ordinary model API keys.
- Provider support depends on the current CPA version; older versions may ignore or reject newer fields.
