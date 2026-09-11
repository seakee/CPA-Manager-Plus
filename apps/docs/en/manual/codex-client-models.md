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

The list shows each model's origin, context window, default reasoning level, and visibility. There are four origins: Base comes straight from the upstream catalog, Overridden means local edits were applied to fields inside the entry, Custom means the whole entry was added locally, and Removed means the entry was written to null locally. Expanding a removed entry and editing any field rebuilds it.

The top of the page also shows the current catalog source, the revision, and the override file path.

## List And Filters

- The search box filters by slug or display name.
- The filter buttons group entries by origin and switch between All, Overridden, Custom, Removed, and Base.
- The top right can refresh the catalog, add an entry, or clear every override.
- Entries that carry an override show a remove button on the right that affects only that entry.

## Common Fields

Common fields are grouped by purpose, with the most frequently used ones visible directly:

- Basic: display name, description, visibility, priority.
- Context and reasoning: context window, max context window, default reasoning level, default reasoning summary, default verbosity, verbosity control.
- Capabilities: available in API, parallel tool calls, reasoning summaries, search tool, prefer WebSockets, use Responses Lite.
- Tools and modalities: input modalities, apply patch tool, shell type, search tool type, multi-agent version, multi-agent reasoning effort.
- Prompts: base instructions and model messages.

The remaining fields live under Advanced: remaining fields below the expanded area and are collapsed by default. The raw entry JSON lives under JSON patch, also collapsed by default; it replaces the whole patch, so apply your edits back to the form or reset them, and saving stays blocked while the text is unapplied.

## Field-Level Sources

Every field carries a source marker in its top right corner showing whether the current value is a local override, comes from the catalog, or is inherited from another model.

- The marker expands to point a single field at a different inheritance source.
- An overridden field can be restored to its default on its own, without discarding the rest of the entry's edits.
- When only one field needs a special value, set that field's source and leave the remaining fields on the entry's inheritance source.

When other overrides use the entry as their inheritance source, the editor header shows how many references it has.

## Prompts

Base instructions and the instruction template inside model messages usually hold the same content, so Override base instructions and the message template together is on by default and writes to both places on save. Turn the switch off to display and edit the two fields separately.

When the two fields come from different models, the merged input reports the mismatch; choosing either source makes them agree again.

Prompts are collapsed by default and show up to four preview lines before expanding, so they do not take up excessive height while collapsed.

## Inheritance File Format

Overrides live in a local override file whose path is shown at the top of the page. Each entry is keyed by slug, with inheritance written in the `$inherit` field:

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
      "context_window": "gpt-5.3-codex-spark"
    },
    "slug": "my-sol-lite",
    "display_name": "My Sol Lite"
  }
}
```

- A string `$inherit` makes the whole entry inherit from that model.
- An object `$inherit` is a dotted-path to source-slug map, where the empty string is equivalent to inheriting the whole entry.
- Local fields are applied last and can replace inherited values; writing `null` removes the field.
- Slug, display name, and description always come from the local entry and never participate in inheritance.

Inheritance can chain to another inheriting entry, and it can point at entries in the official catalog.

## When Overrides Do Not Apply

The top of the page lists overrides that could not be applied, with the slug, the offending field path, and the reason.

- When a single entry fails, the remaining entries still apply, and the failing entry falls back to its upstream catalog content or drops out of the catalog.
- When the override file cannot be parsed at all, the page explains why and keeps using the upstream catalog.
- After editing the override file, reload the configuration or refresh the page to read it again.

## Configuration Boundaries

- An override affects only the catalog CPA serves to Codex clients; it does not change CPA's request routing to upstream providers.
- Overridden entries do not disappear when the upstream catalog updates, but inherited fields follow their source entry.
- The catalog reapplies local overrides after the upstream catalog refreshes, so there is no need to redo them by hand after each update.
