# UI Design System

This specification defines the token contracts, layout conventions, and automated CI governance for the CPAMP v2 web console (`apps/web`).

`apps/web/src/styles/themes.scss` is the single source of truth for theme token values across `:root`, `[data-theme='white']`, `[data-theme='dark']`, mobile `@media` overrides, and `[data-visual-effects='reduced']`. Full Web carrier aliases in `apps/web/src/fullweb/carrier.css` resolve directly to tokens defined in `themes.scss` without introducing a second palette.

## 1. Primary Actions and Accent Tokens

- **Solid primary fills**: Filled primary buttons, primary action pills, and checked switch tracks use `--primary-solid`, `--primary-solid-hover`, and `--primary-solid-active` paired with `--primary-contrast` text. This ramp is shared across light and dark themes so button text maintains `>= 4.5:1` contrast in every state.
- **Links, focus, and accents**: Text links, active indicators, focus rings, and non-filled accent highlights use `--primary-color` (with `--primary-hover`, `--primary-active`, `--primary-soft`, `--primary-subtle`, `--primary-ring`, and `--focus-*`), which adapts per theme.
- Do not pair `--primary-contrast` text with `--primary-color` fills on dark surfaces; use `--primary-solid*` for filled controls.

## 2. Surfaces, Text, Borders, and Elevation

- **Canvas and surfaces**:
  - Canvas background: `--app-bg` (`--bg-secondary`, `--bg-canvas`), with dark mode using a neutral dark canvas.
  - Primary card/panel surface: `--app-surface` (`--bg-primary`, `--bg-surface`, `--floating-surface`).
  - Opaque/elevated surface: `--app-surface-strong` (`--bg-elevated`, `--bg-code`).
  - Muted/nested surface: `--app-surface-muted` (`--bg-tertiary`) and `--surface-subtle` (`--surface-subtle-hover`, `--surface-subtle-strong`, `--bg-subtle`).
  - Form inputs: `--app-input-bg`, `--app-input-bg-focus`, `--app-input-border`, `--app-input-border-focus`.
- **Text hierarchy**:
  - Primary copy and headings: `--app-text-primary` / `--text-primary`.
  - Secondary body and labels: `--app-text-regular` / `--text-secondary`.
  - Muted metadata and captions: `--app-text-muted` / `--text-tertiary` / `--text-muted`.
  - Disabled/placeholder copy: `--text-quaternary`.
- **Borders**:
  - Default container/row divider: `--app-border` / `--border-color` / `--border-subtle`.
  - Emphasized control/card border: `--app-border-strong` / `--border-primary` / `--border-strong`.
  - Dashed/hover borders: `--border-subtle-dashed`, `--border-hover`.
- **Semantic status palettes**:
  - Status badges and feedback use `--data-badge-{success,warning,danger,info,neutral}-{text,bg,border}` (and their `--success-badge-*`, `--warning-*`, `--failure-badge-*`, `--info-*` aliases) or `--data-{blue,emerald,green,amber,red,violet,cyan,teal,slate}-*`.
- **Elevation**:
  - Drop shadows (`--app-shadow`, `--app-shadow-soft`, `--glass-shadow`, `--floating-shadow`, `--shadow`, `--shadow-lg`) are `none`.
  - The only elevation cue on cards and panels is `--app-specular-highlight` (`box-shadow: var(--app-specular-highlight)`).

## 3. Concentric Radius Scale

Border radii follow a concentric scale from outer containers inward:

| Role | Token | Scale |
| --- | --- | --- |
| Outer page/section container | `--app-radius-lg` | `20px` (outer containers may use `16px` at the mobile breakpoint) |
| Nested card, modal, or group | `--app-radius-md` | `12px` |
| Interactive controls and inputs | `--app-radius-sm` | `8px` |
| Micro tags, inline code, compact chips | `--app-radius-xs` | `6px` |
| Pills and status capsules | `--app-radius-pill` | `9999px` |

- Literal radius values in stylesheets are restricted to `0`, `6px`, `8px`, `12px`, `16px`, `20px`, `9999px`, `50%`, `inherit`, `initial`, `unset`, `revert`, `revert-layer`, or expressions using `var()`, `calc()`, `min()`, `max()`, or `clamp()`.
- Legacy SCSS variables in `apps/web/src/styles/variables.scss` (`$radius-sm`, `$radius-md`, `$radius-lg`, `$radius-full`) remain temporarily and are migrated per component role as pages are touched. Their names and raw values (`{ "$radius-full": "9999px", "$radius-lg": "12px", "$radius-md": "8px", "$radius-sm": "4px" }`) are frozen in `scssRadiusDefinitions`; defining a new `$radius-*` variable or changing a recorded variable's value fails even with `--allow-increase`, while removing a definition is permitted and tightens the baseline.

## 4. Control Heights

Interactive controls use three standard height tokens:

- `--control-height-md` (`36px`): forms, settings panels, dialogs, and primary page actions.
- `--control-height-sm` (`32px`): dense workspaces, filter bars, and toolbars.
- `--control-height-xs` (`28px`): inline table row actions and compact utility buttons.

All interactive controls (search inputs, selects, segmented tabs, and buttons) within a single toolbar row must share the same height token.

## 5. Typography and Formatting

- **Font weights**: Only `400`, `500`, `600`, and `700` (plus `normal`, `bold`, `inherit`, `initial`, `unset`, `revert`, `revert-layer`, `var(...)`, or `$...`) are permitted in `font-weight` and `font` shorthand declarations. Do not use `650`, `750`, `800`, `900`, `bolder`, or `lighter`.
- **Numeric alignment**: Metrics, counts, latencies, percentages, timestamps, and table columns displaying numbers must set `font-variant-numeric: tabular-nums`.
- **Badges and pills**: Status badges, tags, and pills must include `white-space: nowrap`.
- **Monospace**: Key hashes, model IDs, paths, CLI snippets, and request identifiers use `var(--font-mono)` (or `$font-mono`, which aliases `var(--font-mono)`).

## 6. Authoring Rules

- **No raw colour literals outside `themes.scss`**: Hex (`#rgb`, `#rgba`, `#rrggbb`, `#rrggbbaa`) and functional (`rgb()`, `rgba()`, `hsl()`, `hsla()`, `hwb()`, `lab()`, `lch()`, `oklab()`, `oklch()`, `color()`) colour literals belong in `apps/web/src/styles/themes.scss`. Component and feature stylesheets must reference CSS custom properties. `color-mix()` itself is not counted as a colour literal (only raw literals inside its arguments are counted), and CSS named colours (`red`, `white`, etc.) remain out of scope for lexical scanning.
- **Avoid `!important`**: Maintain flat selector specificity in CSS/SCSS modules instead of overriding rules with `!important`.
- **Page headers**: Top-level pages reached directly from the sidebar must not render a duplicate in-page `<h1>` or breadcrumb header (the shell top bar breadcrumb serves as the page title). Drill-down subpages keep a lightweight back navigation bar.
- **Contrast**: Body copy, badge labels, and button text must maintain at least `4.5:1` contrast against their background fill in both light and dark themes.

## 7. Known Undefined-Variable Debt

The following 15 CSS custom property names (67 reference sites across 13 feature stylesheets) are currently referenced without a matching definition and are locked under the R1 per-file/per-variable ratchet until their owning pages are restyled in subsequent UI batches:

| Variable | Sites | Files | Fallback / Current Resolution | Target Replacement |
| --- | --- | --- | --- | --- |
| `--font-family-mono` | 12 | `features/oauth/CodexReauthDialog.module.scss` (1), `features/monitoring/CodexInspectionPage.module.scss` (8), `features/monitoring/styles/_monitoring-summary-table.scss` (2), `features/monitoring/components/FailureDetailsTooltip.module.scss` (1) | Renders its `var()` fallback (monospace font-family stack) | `var(--font-mono)` |
| `--color-text-muted` | 1 | `features/monitoring/MonitoringCenterPage.module.scss` (1) | Renders its `var()` fallback (`features/monitoring/MonitoringCenterPage.module.scss:2318`) | `var(--text-muted)` |
| `--matrix-intensity` | 1 | `features/usage-analytics/UsageAnalyticsPage.module.scss` (1) | Renders its `var()` fallback (`0%` inside `color-mix(...)`) | Remove unused rule or bind inline style |
| `--gray-100` | 2 | `features/accounts/components/AccountHealthBadge.module.scss` (2) | Renders its `var()` fallback (`features/accounts/components/AccountHealthBadge.module.scss:76,86`) | `var(--surface-subtle)` / neutral badge token |
| `--gray-400` | 2 | `features/accounts/components/AccountHealthBadge.module.scss` (2) | Renders its `var()` fallback (`features/accounts/components/AccountHealthBadge.module.scss:82,92`) | `var(--data-badge-neutral-text)` |
| `--gray-200` | 7 | `features/accounts/components/AccountHealthBadge.module.scss` (2), `features/accounts/components/QuotaWindowCard.module.scss` (5) | Mixed: `AccountHealthBadge.module.scss:77,87` renders its `var()` fallback; `QuotaWindowCard.module.scss` uses `var(--color-border, var(--gray-200))` with no fallback on `--gray-200` (initial value if `--color-border` unset) | `var(--border-color)` / `var(--data-badge-neutral-border)` |
| `--gray-500` | 10 | `features/accounts/components/AccountHealthBadge.module.scss` (1), `features/accounts/components/QuotaWindowCard.module.scss` (9) | Mixed: `AccountHealthBadge.module.scss:88` renders its `var()` fallback; `QuotaWindowCard.module.scss` has 2 bare `var(--gray-500)` sites (no fallback, resolve to `inherit`) and 7 nested `var(--color-text-tertiary, var(--gray-500))` sites with no fallback on `--gray-500` | `var(--text-tertiary)` |
| `--gray-600` | 6 | `features/accounts/components/AccountHealthBadge.module.scss` (1), `features/accounts/components/QuotaWindowCard.module.scss` (5) | Mixed: `AccountHealthBadge.module.scss:78` renders its `var()` fallback; `QuotaWindowCard.module.scss` uses `var(--color-text-secondary, var(--gray-600))` with no fallback on `--gray-600` | `var(--text-secondary)` |
| `--gray-50` | 2 | `features/accounts/components/QuotaWindowCard.module.scss` (2) | No fallback on `--gray-50` inside `var(--color-surface-muted, var(--gray-50))` (initial value if `--color-surface-muted` unset) | `var(--app-surface-muted)` |
| `--gray-900` | 7 | `features/accounts/components/QuotaWindowCard.module.scss` (7) | No fallback on `--gray-900` inside `var(--color-text, var(--gray-900))` (initial value if `--color-text` unset) | `var(--text-primary)` |
| `--surface-primary` | 2 | `features/oauth/CodexReauthDialog.module.scss` (2) | No fallback (`background` resolves to `transparent`) | `var(--app-surface)` |
| `--accounts-accent-soft` | 1 | `features/accounts/AccountsPage.module.scss` (1) | No fallback (`background` resolves to `transparent`) | `var(--app-accent-soft)` |
| `--shadow-sm` | 12 | `features/plugins/PluginStorePage.module.scss` (2), `features/plugins/PluginRuntimeSwitch.module.scss` (1), `features/plugins/PluginsPage.module.scss` (2), `features/accounts/AccountsPage.module.scss` (3), `features/monitoring/CodexInspectionPage.module.scss` (4) | No fallback (`box-shadow` resolves to `none`) | `var(--app-specular-highlight)` or `none` |
| `--app-shadow-md` | 1 | `features/dashboard/components/HealthAlertsCard.module.scss` (1) | No fallback (`box-shadow` resolves to `none`) | `var(--app-specular-highlight)` or `none` |
| `--matrix-columns` | 1 | `features/usage-analytics/UsageAnalyticsPage.module.scss` (1) | No fallback (`grid-template-columns` resolves to `none`) | Remove unused rule or bind inline style |

### Summary by Resolution Behavior

- **Renders `var()` fallback currently**: `--font-family-mono` (12), `--color-text-muted` (1), `--matrix-intensity` (1), `--gray-100` (2), `--gray-400` (2), plus the `AccountHealthBadge.module.scss` sites of `--gray-200` (2), `--gray-500` (1), and `--gray-600` (1).
- **No fallback (resolves to CSS property initial value)**: `--surface-primary` (2), `--accounts-accent-soft` (1), `--shadow-sm` (12), `--app-shadow-md` (1), `--matrix-columns` (1), and the `QuotaWindowCard.module.scss` sites of `--gray-50` (2), `--gray-200` (5), `--gray-500` (9), `--gray-600` (5), and `--gray-900` (7) when their outer `--color-*` custom property is not set.

## 8. Automated Governance (`check:design-tokens`)

`bin/ci/check-design-tokens.mjs` scans `apps/web/src/**` (`.css`, `.scss`, `.ts`, `.tsx`, excluding tests and `.d.ts` files). It strips `/* ... */` block comments in all scanned files, strips `//` line comments only in `.scss`, `.ts`, and `.tsx` files (not in `.css`), and masks quoted strings, `url(...)` spans, and `#{...}` interpolation delimiters (while still scanning interpolation bodies) in `.css`/`.scss`:

| Rule | ID | Behaviour |
| --- | --- | --- |
| R1 | `undefined-var` | Strict per-file, per-variable ratchet. Every `var(--name)` reference must match a custom property definition (`--name:` in CSS/SCSS, object keys inside a JSX `style={...}` attribute expression including multi-line `style={{ ... } as X}`, or the first argument of `.style.setProperty('--name', ...)` / `.setProperty('--name', ...)` in TS/TSX). Any new undefined variable or count increase fails. |
| R2 | `radius` | Per-file ratchet on `border-radius`, `border-*-*-radius`, and `--*radius*` custom-property declarations whose shorthand parts are outside the allowed radius scale or expressions (`var()`, `calc()`, `min()`, `max()`, `clamp()`, `$`, `#{`). |
| R3 | `font-weight` | Per-file ratchet on `font-weight` declarations and standalone numeric weight tokens (`1`–`1000`) or `bolder`/`lighter` in `font` shorthand declarations outside `400`, `500`, `600`, `700`, `normal`, `bold`, `inherit`, `initial`, `unset`, `revert`, `revert-layer`, `var(...)`, or `$...`. |
| R4 | `scss-radius-var` | Per-file ratchet on `$radius-*` uses, plus a hard failure (even with `--allow-increase`) if `variables.scss` defines a new `$radius-*` name or changes the raw value of any recorded entry in `scssRadiusDefinitions`. Removing a definition is allowed and tightens the baseline. |
| R5 | `color-literal` | Per-file ratchet on hex and `rgb()`/`rgba()`/`hsl()`/`hsla()`/`hwb()`/`lab()`/`lch()`/`oklab()`/`oklch()`/`color()` literals in `.css`/`.scss` files outside `apps/web/src/styles/themes.scss`. `color-mix()` itself is not counted as a literal (only literals inside it count), and named colours (`red`, `white`) are out of scope. |
| R6 | `important` | Per-file ratchet on `!important` in `.css`/`.scss` files. |

### Running and Updating the Baseline

```bash
# Run the check (also executed in CI Frontend job)
npm run check:design-tokens

# Tighten the baseline after reducing violations
npm run check:design-tokens -- --update-baseline

# Generate a new baseline file for the first time (refuses to overwrite an existing file)
npm run check:design-tokens -- --init
```

- **Baseline schema (`version: 2`)**: `bin/ci/design-token-baseline.json` must declare `"version": 2`, a `"rules"` object containing all six rule keys (`undefined-var`, `radius`, `font-weight`, `scss-radius-var`, `color-literal`, `important`), and a sorted `"scssRadiusDefinitions"` `{ "$radius-name": "rawValue" }` map. Baseline loading fails closed if the file is missing, unreadable, or fails schema validation (both in normal mode and with `--update-baseline`). First-time creation requires `--init`, which refuses to overwrite an existing file.
- Baseline counts in `bin/ci/design-token-baseline.json` may only move downward. When a file's count drops below its recorded baseline (or a legacy `$radius-*` definition is removed), the checker passes and prints a notice that the baseline can be tightened.
- `--update-baseline` rewrites `bin/ci/design-token-baseline.json` to current counts and exits `1` if any count would increase or if any R4 hard failure occurs.
- Intentional exceptions that increase a count require explicit reviewer sign-off in the pull request and `--update-baseline --allow-increase` (new or mutated `$radius-*` definitions in `variables.scss` still fail).

### Known Limitations

- **Intentional scope boundaries**:
  - **No Sass evaluation**: SCSS maps, `@each`/`@for`-generated declarations, mixins/functions that synthesize property names, and interpolated property names (such as `border-#{$side}-radius: 3px`) are not evaluated.
  - **Global custom-property existence (R1)**: R1 checks that a `--name` is defined somewhere in the scanned tree rather than modelling per-theme or per-selector CSS cascade scope (for example, a variable declared only under `[data-theme='dark']` counts as defined globally), and it does not verify that a `.setProperty()` receiver is `CSSStyleDeclaration` or follow non-inline style objects bound to `style={helperObj}`.
  - **Stylesheet-only rules (R2, R3, R5, R6)**: R2, R3, R5, and R6 inspect `.css`/`.scss` stylesheets only and do not inspect `.ts`/`.tsx` files.
  - **Colour function and keyword scope (R5)**: `color-mix()` itself is not counted as a colour literal (only colour literals inside its arguments count), and CSS named colours (`red`, `white`, etc.) are not counted.
  - **CSS identifier escapes**: Plain CSS backslash escapes in identifiers (for example `--\66 oo`) are not decoded.
  - **Per-file ratchet granularity (R2–R6)**: R2–R6 lock violation counts per file rather than per line or selector.
- **Remaining lexical parser boundaries**:
  - A SCSS nested property with a base value is recognised only when whitespace follows the colon (`border: 1px solid #fff { radius: 8px; }`). Write nested properties in this form or as separate declarations.
  - An ID selector that looks like a 3-, 4-, 6-, or 8-digit hex colour and appears in a selector-bearing at-rule prelude (`@extend #abc`, `@at-root #abc`, `@scope (#abc)`, `@supports selector(#abc)`) is counted as a colour literal.
  - R3 `font` shorthand inspection checks standalone numeric weight tokens (`1`–`1000`) and `bolder`/`lighter` outside `var()`/`$` expressions rather than parsing the full CSS `font` shorthand grammar.
- **Resolved in baseline schema v2**:
  - TS/TSX custom-property definitions are restricted to object keys inside JSX `style={...}` attributes and `.setProperty('--name', ...)` calls (R1-01).
  - Quoted strings and `url(...)` contents in `.css`/`.scss` are masked across R1, R4, R5, and R6, with `#{...}` delimiters masked while inspecting their bodies (R1-02).
  - Standalone numeric weight tokens (`1`–`1000`) and `bolder`/`lighter` in `font` shorthand declarations are checked by R3 (R1-03).
  - Legacy `$radius-*` definitions in `variables.scss` freeze both variable name and raw value in `scssRadiusDefinitions` (R1-04).
  - Baseline loading validates `version: 2`, all six `rules` keys, and the `scssRadiusDefinitions` map, failing closed on missing or invalid files and requiring `--init` for first-time creation (R1-05).
  - Modern CSS colour functions (`hwb()`, `lab()`, `lch()`, `oklab()`, `oklch()`, `color()`) are counted by R5 with CSS-identifier boundary checks (R1-06).
  - Single-line `//` comments are stripped only in `.scss` and `.ts`/`.tsx` files, not in `.css` (R1-07).
  - CI workflow integrity tests lock the `Frontend` job step ordering (`npm ci` -> `Lint` -> `Check design tokens` -> `Test`) and `npm run check:design-tokens` command (R1-08).
  - Unused `valueOffset` declaration metadata was removed (R1-09).
