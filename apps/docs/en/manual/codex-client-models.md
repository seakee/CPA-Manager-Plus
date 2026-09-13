---
title: Codex Client Model Catalog
description: Inspect the model catalog CPA serves to Codex clients, and apply local per-model overrides, field-level inheritance, and prompt adjustments in CPA Manager Plus.
---

# Codex Client Model Catalog

The Model Catalog page shows the model entries CPA currently serves to Codex clients, and lets you override those entries locally. It edits the catalog CPA hands out, not the AI provider configuration and not the Codex client's own settings.

Open the [Model Catalog Demo](https://seakee.github.io/CPA-Manager-Plus/#/demo/codex-client-models) to inspect a fictional catalog and its overrides without contacting a real CPA.

The page entry point requires the matching capability flag from the current CPA version. Older CPA versions do not show it, and visiting the route directly returns to [Configuration](./configuration.md).

## Most Common Workflow

1. Select a model name in the list; the entry expands in place and leaves the right side free for editing.
2. To base an entry on another model, pick a source under Inherit from first. Every field without its own override comes from that model.
3. Edit what you need in the common fields.
4. Choose Save override. The change is written to the local override and takes effect for Codex clients.

The list shows each model's origin, served state, context window, default reasoning level, and visibility. There are five origins: Base means the catalog has an entry for this model, Auto-assembled means it does not and the server built the entry from the default template, Overridden means local edits were applied to fields inside the entry, Removed means the entry was written to null locally, and Unserved means no provider currently serves the model the override names. The origin only tells where the entry comes from; the baseline for every field value is always the entry the server assembled.

The top of the page also shows the current catalog source, the revision, and the override file path.

## Creating A Dedicated Entry

What the server hands out is built by assembling an entry for every model it can currently serve: the catalog entry when there is one, and otherwise the default template plus model metadata. Those assembled entries are the default configuration, and both the catalog shown at the top of the page and the default value of every field come from them.

Expanding an entry that has no local override yet shows Create a dedicated entry for `<slug>`:

- The slug is the identifier clients request the model by, so it cannot be changed.
- Saving adds a local override entry that declares nothing but its slug.
- Every field starts from the configuration clients receive today, and only what you change becomes an override.
- Every value clients receive right now shows up as a default, so none of them has to be overridden just to keep the current behaviour.

A model keeps working without its own entry: it is still served as assembled.

## List And Filters

- The search box filters by slug or display name.
- The filter buttons group entries by origin and switch between All, Auto-assembled, Overridden, Unserved, Removed, and Base.
- The Served column marks whether the model currently shows up in the list clients see; hovering it names the providers serving the model. A model reading Not Served usually means the entry was written to null locally, or the credentials supplying it are temporarily unavailable.
- The top right can refresh the catalog, add an entry, or clear every override. Add entry writes an override for a slug: when the server can serve that model, its fields come with the assembled defaults; when nothing serves it yet, there are no defaults to compare against and every value you set is stored as a local override.
- Entries that carry an override show a remove button on the right that affects only that entry.

## Common Fields

Common fields are grouped by purpose, with the most frequently used ones visible directly:

- Basic: display name, description, visibility, priority.
- Context and reasoning: context window, max context window, max output tokens, auto compact token limit, supported reasoning levels, default reasoning level, default reasoning summary, default verbosity, verbosity control.
- Capabilities: available in API, parallel tool calls, reasoning summaries, search tool, prefer WebSockets, use Responses Lite.
- Tools and modalities: input modalities, apply patch tool, shell type, search tool type, multi-agent version, multi-agent reasoning effort.
- Prompts: base instructions and model messages.

The remaining fields live under Advanced: remaining fields below the expanded area and are collapsed by default. The raw entry JSON lives under JSON patch, also collapsed by default; it replaces the whole patch, so apply your edits back to the form or reset them, and saving stays blocked while the text is unapplied.

## Field-Level Sources

Every field carries a source marker in its top right corner showing whether the current value is the default, a local override, or inherited from another model.

- The marker expands to point a single field at a different inheritance source.
- Any field can be restored to its default on its own, without discarding the rest of the entry's edits; a restored field stays on its default and is not taken over again by the entry's inheritance source.
- When only one field needs a special value, set that field's source and leave the remaining fields on the entry's inheritance source.

A default is the entry the server assembles: a catalog template merged with the model metadata (context length, reasoning levels, provider capabilities), which is what clients receive when nothing overrides it. The override layer is applied on top of that result, so every field can be overridden, including the ones normalization used to win.

The model's identity, where it sits in the catalog, and its context and reasoning configuration (the windows, the token budgets, and the reasoning levels) always come from the entry itself: no source supplies them, and directives that name them are rejected. Every other field follows the entry's inheritance source; to keep one of them on its default instead, the editor writes "do not inherit" on that path.

When other overrides use the entry as their inheritance source, the editor header shows how many references it has.

## Prompts

Base instructions and the instruction template inside model messages usually hold the same content, so Override base instructions and the message template together is on by default and writes to both places on save. Turn the switch off to display and edit the two fields separately.

When the two fields come from different models, the merged input reports the mismatch; choosing either source makes them agree again.

Prompts are collapsed by default and show up to four preview lines before expanding, so they do not take up excessive height while collapsed.

## Inheritance File Format

The catalog the page lists is the default entry the server assembles for every model it can serve; the override layer is applied on top of those entries and lives in a local override file whose path is shown at the top of the page. Each entry is keyed by slug, with inheritance written in the `$inherit` field:

```json
{
  "my-fast-sol": {
    "$inherit": "gpt-5.6-sol",
    "slug": "my-fast-sol",
    "display_name": "My Fast Sol"
  },
  "my-sol-lite": {
    "$inherit": {
      "": "gpt-5.6-sol",
      "base_instructions": "gpt-5.6-terra",
      "model_messages.instructions_template": null
    },
    "slug": "my-sol-lite",
    "display_name": "My Sol Lite"
  },
  "gpt-5.6-terra": {
    "slug": "gpt-5.6-terra"
  }
}
```

- An entry may declare nothing but its `slug`: the local entry exists and every value still matches the assembled result. This is what the editor writes for a new entry.
- A string `$inherit` makes the whole entry inherit from that model.
- An object `$inherit` is a dotted-path to source-slug map, where the empty string is equivalent to inheriting the whole entry.
- A path set to `null` does not inherit: the value comes from the entry itself and an ancestor directive no longer covers it. This is what the editor's restore-to-default action writes.
- Local fields are applied last and can replace inherited values; a local `null` at a field path removes the field.
- Slug, display name, description, visibility, priority, and the context and reasoning configuration (context_window, max_context_window, max_tokens, auto_compact_token_limit, supported_reasoning_levels, default_reasoning_level, default_reasoning_summary, default_verbosity, support_verbosity) only come from the entry itself and never participate in inheritance.

A source resolves to the **served entry** of that model, so its own overrides count as well. When no provider serves an official model, its catalog template stands in, which keeps `$inherit: "gpt-5.5"` writable on a machine with no Codex provider.

Inheritance can chain to another override entry, and it can point at entries in the official catalog.

An override that cannot be applied does not remove the model: the page reports it at the top and the model keeps the entry the server assembled.

## When Overrides Do Not Apply

The top of the page lists overrides that could not be applied, with the slug, the offending field path, and the reason.

- When a single entry fails, the remaining entries still apply, and the failing entry falls back to the entry the server assembled.
- When the override file cannot be parsed at all, the page explains why and keeps using the upstream catalog.
- Saving from the page takes effect immediately; the service watches the override file, so editing it directly is picked up automatically.

## Configuration Boundaries

- An override affects only the catalog CPA serves to Codex clients; it does not change CPA's request routing to upstream providers.
- Overridden entries do not disappear when the upstream catalog updates, but inherited fields follow their source entry.
- The catalog reapplies local overrides after the upstream catalog refreshes, so there is no need to redo them by hand after each update.
