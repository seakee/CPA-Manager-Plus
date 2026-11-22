# UI Page Patterns & Mandatory UX Standards

This document is the mandatory companion to [`ui-design-system.md`](./ui-design-system.md) (tokens, colours, radius/height scales, and automated CI governance). While `ui-design-system.md` defines the visual tokens, **this document defines the mandatory, reusable UI/UX contract for every CPA Manager Plus v2 screen (`Full Web` and `Panel Lite`).**

All new screens, feature additions, and UI refactors **must reuse the shared primitives and patterns defined here** rather than inventing per-feature layouts, headers, footers, switchers, or card styles.

---

## 1. Brand Posture & Golden References

CPA Manager Plus is a **high-density gateway operations console** with crisp light/dark surface hierarchy — not a minimalist consumer app that hides controls. Its visual and interaction identity is anchored by four verified reference implementations:

- **`/accounts` (`features/accounts/AccountsPage.tsx`)** — Reference for **Resource Management Workspaces** (`P1`).
- **`/monitoring` (`features/monitoring/MonitoringCenterPage.tsx`)** — Reference for **Observability & Analytics Workspaces** (`P2`).
- **`/config` & `/system` (`fullweb/FreshGatewayWorkspace.tsx`, `fullweb/FreshSystemWorkspace.tsx`)** — Reference for **Grouped Config Workspaces** (`P3`).
- **`/logs` (`features/logs/LogsPage.tsx`, `LogsSettingsPanel.tsx`)** — Reference for **Live Stream, Diagnostic File Table & Settings Workspaces** (`P5` / `P1` / `P3`).

### Seven Non-Negotiable UX Principles

1. **Crisp Physical Boundaries**: Primary navigation segmented controls, inputs, buttons, and cards keep explicit `1px` borders and surface hierarchy (`--app-bg` canvas -> `--bg-primary` card -> `--surface-subtle` muted slot). Never flatten primary segmented tabs into borderless shapes that blend into white cards.
2. **Permanent Functional Anchors & Zero Layout Shift**:
   - `<PageControlBar>` is **always the first top block** of any headered page. Never stack status banners or standalone toggle cards above `<PageControlBar>`, and never allow Row 1 height (`40px` tabs + single horizontal `actions` row) to jump when switching tabs.
   - Configuration save footers (`<SectionActionFooter>`), list filter toolbars, KPI summary bands, and full `<PaginationFooter>` controls remain **permanently visible** even when clean or at `0` rows. Express non-actionable state via `disabled={!dirty}` and `<EmptyState>`, never by unmounting anchors.
3. **Clear Global vs. Local Control Boundaries & Single Authority**:
   - Controls governing the **entire screen** (primary page tabs, global search/filters, time windows, page-level actions) belong in the top `<PageControlBar>`.
   - Controls governing **only one data surface** (e.g. `/monitoring`'s `Accounts | API Key | Realtime` table tabs, `/accounts`'s `Select / Full / Refresh Quota / Table|Card` controls) stay attached to that data card header (`padding: 12px 16px`, `border-bottom: 1px solid var(--border-subtle)`).
   - **Global runtime switches belong in configuration pages (`/config` or `/system`)**, not duplicated as heavy cards inside business feature tabs (e.g. `plugins.enabled` lives in `/config` `FreshGatewayWorkspace`; `/plugins` only shows a conditional warning banner with a `[Open gateway config]` shortcut when disabled).
4. **Restrained Row 1 & Proportional Row 2 Widescreen Rhythm**:
   - Keep `<PageControlBar>` Row 1 clean: `leading` holds only primary `SegmentedTabs` or a single `•` status anchor; `actions` holds single-row `32px` controls ordered `[framed toggle/meta] -> [secondary...] -> [at most ONE primary rightmost]`. Never cram 4+ metric/path pills into Row 1 or duplicate counts already shown in Row 2 filter tabs.
   - Push secondary operational metadata (e.g. counts, sources, truncated monospace directory path with full `title` tooltip) to the right end (`margin-left: auto`) of Row 2 (`toolbar`). Hide empty `0 / 0` noise counters when a table has `0` records.
   - Filter toolbars use balanced CSS Grid tracks or bounded search widths (`max-width: 320px–420px`). Never stretch a standalone search input across 1000px+ with `flex: 1; max-width: none`.
5. **Dual-Channel Refresh Ergonomics**:
   - Every reloadable screen wires `useHeaderRefresh` so the global shell top-right `↻` works everywhere.
   - Reloadable screens also keep a consistent in-page `<Button variant="secondary" size="sm">` with `<IconRefreshCw size={15} />` inside `<PageControlBar>` Row 1 `actions` (never buried inside a lower card header on one tab while living in Row 1 on sibling tabs).
6. **Clean Card Hierarchy (No Card-Inside-Card)**:
   - Avoid wrapping every setting row inside its own grey bordered sub-card inside a white card. Use a single flat `1px` white card with a standard `.panelHeader` (`padding: 14px 20px; border-bottom: 1px solid var(--border-subtle)`) and flat `1px solid var(--border-subtle)` row dividers.
7. **Inline Inspection Before Download**:
   - For downloadable text diagnostic artifacts (e.g. request error logs), provide an inline `<Modal>` preview directly from both row action (`[View]`) and clickable monospace filename alongside `[Download]`, with stale-response race protection, safe large-file truncation (`512 KB`), zero-refetch download from the fetched `Blob`, copy-to-clipboard, and word-wrap toggle.

---

## 2. Mandatory Reusable Primitives

Every new screen or UI refactor **must** compose from these **6 shared layout/page primitives** and **2 core interactive primitives**. Do not copy-paste private CSS clones or cross-import primitives across feature directories (`features/a` -> `features/b`).

### Shared Page Primitives (`apps/web/src/components/common/`)

| Primitive | Path | Mandatory Role & Contract |
| --- | --- | --- |
| **`<PageControlBar>`** | `components/common/PageControlBar.tsx` | Unified flat (`--bg-primary`, `1px solid var(--border-color)`, `--app-radius-lg` `20px`) top control card with Row 1 (`leading` + single-row `actions`) and optional attached Row 2 (`toolbar`, separated by `1px solid var(--border-subtle)`). Never wrap `actions` in an unstyled block `<div>` that forces line wrapping. |
| **`<MetricSummaryCard>`** | `components/common/MetricSummaryCard.tsx` | Unified KPI summary card (semantic icon tile, `12px` label, `24px` `tabular-nums` value with exact-value tooltip, `11px` meta note, subtle sparkline accent). |
| **`<PaginationFooter>`** | `components/common/PaginationFooter.tsx` | Sole table/grid/list pagination footer (`variant="flush"` for edge-to-edge tables, `variant="inset"` inside padded card surfaces; supports `extraActions`, `32px` page-size `<Select>`, prev/next, and `showPageJump`). Max one per card; never stack a second load-more bar below it. |
| **`<SectionActionFooter>`** | `components/common/SectionActionFooter.tsx` | Permanent inline card bottom bar (`1px solid var(--border-subtle)` top border, left status copy, right buttons) for per-group config saves (`disabled={!dirty}`) and lifecycle actions. |
| **`<FloatingActionDock>`** | `components/common/FloatingActionDock.tsx` | Portaled viewport-bottom dock (`isCurrentLayer`-guarded, `ResizeObserver`-measured `--page-floating-dock-height`) reserved strictly for multi-row batch selection or whole-document unsaved drafts. |
| **`<AppFooter>`** | `components/common/AppFooter.tsx` + `appFooterMeta.ts` | Global watermark-style page footer (`CPAMP v2.0.0 Designed by Seakee`) rendered once by shells and standalone entry pages (`MainLayout`, `LoginPage`, `FreshBootstrapPage`, `UpgradeAuthenticationEntry`; omitted only on full-bleed plugin resource iframes). **Business pages must never render a private footer.** Must stay free of `/features/` imports to satisfy `bin/build/check-fullweb-build.mjs`. |

### Core Interactive Primitives (`apps/web/src/components/ui/`)

| Primitive | Path | Mandatory Role & Contract |
| --- | --- | --- |
| **`<SegmentedTabs>`** | `components/ui/SegmentedTabs.tsx` | Sole segmented switcher across the app — strictly two tiers: default `size="md"` (`40px` bordered track) for Row 1 primary page navigation; `variant="joined"` (`32px` `--control-height-sm`, `8px` `--app-radius-sm`, `responsiveFullWidth={false}`) for Row 2 filters, time ranges, mode toggles, and data-card tabs. Use `roleMode="group"` (`aria-pressed`) for non-panel mode/layout toggles and `allowReselect` when re-clicking reopens a modal. |
| **`<EmptyState>`** | `components/ui/EmptyState.tsx` | Dual-mode empty state: **`variant="canvas"`** is mandatory for primary workspace cards (tables, log viewports, plugin/store grids, model catalogs, monitoring tables); **`variant="inline"`** is reserved for compact nested sub-sections. |

---

## 3. Five Standard Page Archetypes (`P1`–`P5`)

Every sidebar route must map to one of these five archetypes. The `50px` shell `.main-header` breadcrumb is the sole page title; never add a duplicate in-page `<h1>`/`<h2>` masthead.

```text
Standard Console Composition (gap: var(--app-gap) = 20px)
┌──────────────────────────────────────────────────────────────────────┐
│ Layer 1 · <PageControlBar> (20px radius, 1px border, flat white)     │
│   Row 1: [leading: SegmentedTabs(40px) or • Status] | [actions 32px] │
│   ─────────────────── 1px solid var(--border-subtle) ─────────────── │
│   Row 2: [toolbar: bounded search + joined tabs / toggles | meta]    │
├──────────────────────────────────────────────────────────────────────┤
│ Layer 2 · Optional <MetricSummaryCard> KPI Band or Warning Banner    │
├──────────────────────────────────────────────────────────────────────┤
│ Layer 3 · Primary Workspace Card (20px radius, 1px border)           │
│   ├─ Local DataPanelHeader (12px 16px, border-bottom subtle)         │
│   ├─ Main Surface (Table / Card Grid / Log Stream / <EmptyState>)    │
│   └─ Footer (<PaginationFooter> or <SectionActionFooter>)            │
├──────────────────────────────────────────────────────────────────────┤
│ Shell Footer · <AppFooter /> (embedded watermark, rendered by shell) │
└──────────────────────────────────────────────────────────────────────┘
```

| Archetype | Reference | Representative Routes | Header (`PageControlBar`) | Body & Footer Contract |
| --- | --- | --- | --- | --- |
| **P1 · Resource Workspace** | **`/accounts`** | `/accounts`, `/ai-providers`, `/api-keys`, `/plugins`, `/model-prices`, `/logs` (Error Logs tab) | Row 1: `SegmentedTabs(md, 40px)` or `•` status capsule + `[secondary...] -> [single primary rightmost]`. Row 2: bounded search + `SegmentedTabs(joined)` / dimension selects + right-aligned muted meta. | Optional `<MetricSummaryCard>` strip -> Primary card (`1px` flat) with data table or <=4-col card grid (or `<EmptyState variant="canvas">`) -> `<PaginationFooter>` (+ `<FloatingActionDock>` on batch select). |
| **P2 · Observability & Analytics** | **`/monitoring`** | `/monitoring`, `/usage-analytics` | Row 1: live `•` status + borderless timestamp + `secondary` tools only. Row 2: Grid time range (`SegmentedTabs joined` + `allowReselect`) + bounded search + auto-refresh/refresh + equal-width selects. | `<MetricSummaryCard>` KPI strip + charts -> Data card with attached local view tabs (`SegmentedTabs joined`) -> single `<PaginationFooter>` (incremental load-more inside `extraActions`). |
| **P3 · Grouped Config Workspace** | **`/config`**, **`/system`** | Full Web `/config`, Full Web `/system`, Panel Lite `/config`, `/logs` (Settings tab) | Row 1: group `SegmentedTabs(md, 40px)` + capability pill + mode switch (`SegmentedTabs joined`) + `[↻ Reload]`. Row 2: bounded search + draft summary. | Flat `1px` white `<Card>` sections with `.panelHeader` and `1px solid var(--border-subtle)` horizontal row dividers (left title + monospace key + hint; right toggle or `120px` numeric input + `Button size="sm"`). Editable groups end with permanent `<SectionActionFooter>` (`disabled={!dirty}`). Whole-document YAML/Panel Lite drafts use `<FloatingActionDock>`. |
| **P4 · Overview / Direct Card Stack** | **`/`**, **`/oauth`** | `/`, `/system/runtime`, `/oauth`, `/system/updates`, Panel Lite `/system` | Screens with live status/refresh use single-row `<PageControlBar>`; pure card stacks omit filler headers and align directly with `20px` gap. | Adaptive multi-column `<Card>` grid; bottom lifecycle actions (Restart/Stop) use `<SectionActionFooter>`. |
| **P5 · Live Stream / Viewport Terminal** | **`/logs`** (Logs tab) | `/logs` | Row 1: `SegmentedTabs(md, 40px)` + framed `32px` auto-refresh toggle + `size="sm"` actions. Row 2: bounded search + collapsible structured filter trigger (collapsed by default, persisted) + inline framed `32px` toggles + right-aligned line counts; expanded filter chips inside Row 2 with compact right-aligned `[Clear]`. | Root uses `flex: 1; min-height: 0; overflow: hidden;` (never hardcoded `calc(100vh - ...)` so `<AppFooter compact />` never clips). Pure stream card fills remaining viewport height; compact `20px / 6px` badges; `<EmptyState variant="canvas">` when disabled/empty. |

---

## 4. Mandatory Component Micro-Standards

| Control / Pattern | Mandatory Specification |
| --- | --- |
| **Primary Row 1 Tabs** | `<SegmentedTabs>` (`variant="default"`, `size="md"`, `40px` outer height, `1px` border). Never shrink primary Row 1 tabs to `size="sm"`. |
| **Inline / Toolbar / Card-Header Switches** | `<SegmentedTabs variant="joined" responsiveFullWidth={false} />` — `32px` (`--control-height-sm`) height and `8px` (`--app-radius-sm`) radius. Never hand-roll `.filterChip` pill strips for tab/status filtering. |
| **Header & Toolbar Buttons** | Always `<Button size="sm">` (or `<Link className="btn btn-secondary btn-sm">`): `32px` height, `8px` (`--app-radius-sm`) radius. Never override buttons to `9999px` pill radius. |
| **Action Ordering & Weight** | Strict left-to-right order in `PageControlBar` `actions`: `[framed toggle / static meta] -> [secondary buttons...] -> [at most ONE primary button, ALWAYS rightmost]`. Read-only observability pages keep all toolbar buttons `secondary`. |
| **Refresh Buttons** | `<Button variant="secondary" size="sm">` with `<IconRefreshCw size={15} />`, always placed in Row 1 `actions` (never rightmost when a `primary` CTA exists) and wired to `useHeaderRefresh`. |
| **Inline Toolbar Toggles** | Toolbar switches (`Auto refresh`, `Hide /v0/management`, `Show raw logs`, `Word wrap`) share one `.toggleFrame` pattern: `height: var(--control-height-sm)` (`32px`), `padding: 0 10px`, `border: 1px solid var(--border-color)`, `border-radius: var(--app-radius-sm)`, `background: var(--bg-primary)`. |
| **Search Inputs** | `<Input>` at `32px` height / `8px` radius with right-aligned `<IconSearch size={15|16} />` (`rightElement`, switching to a clear `×` button when non-empty). Desktop width bounded to `max-width: 320px–420px` or a balanced CSS Grid track. |
| **Static Indicators vs. Interactive Controls** | Static counts, timestamps, and readonly badges use borderless subtle surfaces (`border: 1px solid transparent; background: var(--surface-subtle)`). Only interactive buttons, inputs, and toggles carry a visible `1px solid var(--border-color)` outline. |
| **Semantic Status Capsules** | Status pills (`Connected`, `Running`, `Enabled`, `Synced catalog · N models`) include a `7px` circular dot (`border-radius: 50%` / `9999px`) paired with `--data-badge-*-bg` / `--data-badge-*-text` tokens (no harsh coloured outline). |
| **Data Card Headers & Tables** | Card headers use `padding: 12px 16px` and `border-bottom: 1px solid var(--border-subtle)`. Table column alignment must match between `<thead> th` and `<tbody> td` (especially right-aligned action columns); numeric/size/timestamp cells use `font-variant-numeric: tabular-nums`; IDs/filenames/paths use `var(--font-mono)`. |
| **Catalog Card Grids** | Cap desktop density at 4 columns (`repeat(4, minmax(0, 1fr))`, responsive `3 -> 2 -> 1`). Clamp multi-line descriptions (`-webkit-line-clamp: 3` + `title` tooltip), cap visible tags (`<= 4` + `+N`), decode plain HTML entities safely via React text nodes, and paginate large lists with `<PaginationFooter variant="inset" />`. |
| **Log / Stream Micro-Badges** | High-density stream row badges (level, status code, method, latency, request ID) use compact `20px` height and `6px` (`--app-radius-xs`) radius with borderless semantic tints — never `9999px` pills. |
| **Empty States** | Primary workspace empty/no-match states use `<EmptyState variant="canvas" />` centered inside the workspace card with clear primary/secondary recovery actions (`[Refresh]`, `[Clear filters]`, `[Install plugin]`, `[Enable file logging]`). |
| **Inline Diagnostic Preview Modals** | `<Modal width={880}>` with monospace filename title, meta bar (size / modified / line count + framed word-wrap toggle), monotonic request counter to ignore stale responses, `512 KB` truncation guard with warning banner, monospace scrollable `<pre>`, and footer `[Copy]` + `[Download]` (reusing fetched `Blob`) + `[Close]`. |
| **Form Save Footers & Settings Rows** | `<SectionActionFooter>` stays visible with `disabled={!dirty}` when clean. Per-row numeric settings keep descriptions on the left and a compact `[120px monospace Input (32px)] + [Save (sm, disabled={!dirty})]` on the right with `aria-invalid` on validation errors. |
| **Global Page Footer (`<AppFooter>`)** | Embedded watermark aesthetic: `11.5px / 400`, `letter-spacing: 0.025em`, single low-contrast colour `color-mix(in srgb, var(--text-tertiary) 72%, transparent)`, no logo/badge/border/separator dots, zero link underlines (including `:hover`), cursor pointer + subtle hover lift to `var(--text-secondary)`. All external links use `target="_blank" rel="noopener noreferrer"`; dev builds display the base semver (`v2.0.0`, full build string in `title`) and link to `/releases` to prevent 404s. |

---

## 5. Forbidden Anti-Patterns (Do / Don't)

- **Don't** place banners, warning boxes, or standalone switch cards above `<PageControlBar>` — `<PageControlBar>` is always Layer 1.
- **Don't** wrap `PageControlBar` `actions` in a block `<div>` or unstyled wrapper that splits toggles and buttons onto two rows.
- **Don't** cram 3–4 status/stat/path pills into Row 1 `leading` alongside primary `SegmentedTabs` or duplicate counts already shown in Row 2 filter tabs.
- **Don't** hide `<SectionActionFooter>`, `0`-row filter toolbars, or single-page `<PaginationFooter>` controls to chase minimal chrome.
- **Don't** flatten primary Row 1 `<SegmentedTabs>` below `40px` or hand-roll pill `.filterChip` bars where `<SegmentedTabs variant="joined">` belongs.
- **Don't** set `flex: 1; max-width: none` on a standalone search input in a sparse row (`<= 420px` or balanced Grid tracks).
- **Don't** place a `primary` CTA to the left of `secondary` buttons (e.g. `[+ Install] [Refresh]`), or style read-only export/refresh buttons as `primary`.
- **Don't** nest bordered grey sub-cards for each setting row inside a white settings card — use flat `1px solid var(--border-subtle)` dividers.
- **Don't** hard-code `height: calc(100vh - ...)` inside viewport-locked pages (`P5`) — use `flex: 1; min-height: 0;` so `<AppFooter compact />` fits without clipping.
- **Don't** give static metric counters or timestamps the same `1px` border box as clickable controls, or render a standalone `0 / 0` counter when a list is empty.
- **Don't** style `<AppFooter>` with loud logos, white boxes, underlines, or high-contrast colours; never render a second footer inside a feature page.
- **Don't** import components across feature boundaries (`features/accounts` -> `features/monitoring`) or import `/features/*` from bootstrap-shared primitives (`AppFooter`).

---

## 6. Mandatory Pre-PR / Code Review Checklist

Before submitting or approving any PR that adds or edits a console page or shared UI primitive:

- [ ] Page maps cleanly to `P1`–`P5`; no duplicate `<h1>`/`<h2>` below the shell breadcrumb; `<PageControlBar>` is Layer 1 with stable `40px` Row 1 across sibling tabs.
- [ ] Reuses the 6 mandatory shared primitives (`PageControlBar`, `MetricSummaryCard`, `PaginationFooter`, `SectionActionFooter`, `FloatingActionDock`, `AppFooter`) and `SegmentedTabs` / `EmptyState` / `Button size="sm"` — zero feature-private clones and zero cross-feature imports.
- [ ] Row 1 `leading` is uncluttered; `actions` stay single-row ordered `[toggle/meta] -> [secondary...] -> [single primary rightmost]`; refresh lives in Row 1 `actions` and wires `useHeaderRefresh`.
- [ ] Row 2 search uses right-aligned icon + bounded width (`<= 420px` or balanced Grid); inline switches share the `32px` `.toggleFrame`; secondary metadata sits right-aligned (`margin-left: auto`).
- [ ] Tables align `th` and `td` consistently, use `tabular-nums` / `var(--font-mono)`, paginate via a single `<PaginationFooter>`, and offer inline preview for downloadable text diagnostics where applicable.
- [ ] Settings cards use flat row dividers (no card-in-card), permanent `<SectionActionFooter>` with `disabled={!dirty}`, and accessible `aria-invalid` validation.
- [ ] Primary empty states use `<EmptyState variant="canvas" />`; no private footer duplicates `<AppFooter />`; viewport-locked screens use `flex: 1; min-height: 0;`.
- [ ] `npm run check:design-tokens`, `npm run type-check`, `npm run lint`, targeted Vitest suites, and `npm --workspace apps/web run build` (including `check-fullweb-build.mjs`) pass cleanly in both light and dark themes.
