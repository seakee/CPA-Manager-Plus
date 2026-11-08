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
- Legacy SCSS variables in `apps/web/src/styles/variables.scss` (`$radius-sm`, `$radius-md`, `$radius-lg`, `$radius-full`) remain temporarily and are migrated per component role as pages are touched. Do not define new `$radius-*` variables in `variables.scss`.

## 4. Control Heights

Interactive controls use three standard height tokens:

- `--control-height-md` (`36px`): forms, settings panels, dialogs, and primary page actions.
- `--control-height-sm` (`32px`): dense workspaces, filter bars, and toolbars.
- `--control-height-xs` (`28px`): inline table row actions and compact utility buttons.

All interactive controls (search inputs, selects, segmented tabs, and buttons) within a single toolbar row must share the same height token.

## 5. Typography and Formatting

- **Font weights**: Only `400`, `500`, `600`, and `700` (plus `normal`, `bold`, `inherit`, `initial`, `unset`, `revert`, `revert-layer`, `var(...)`, or `$...`) are permitted. Do not use `650`, `750`, `800`, or `900`.
- **Numeric alignment**: Metrics, counts, latencies, percentages, timestamps, and table columns displaying numbers must set `font-variant-numeric: tabular-nums`.
- **Badges and pills**: Status badges, tags, and pills must include `white-space: nowrap`.
- **Monospace**: Key hashes, model IDs, paths, CLI snippets, and request identifiers use `var(--font-mono)` (or `$font-mono`, which aliases `var(--font-mono)`).

## 6. Authoring Rules

- **No raw colour literals outside `themes.scss`**: Hex (`#rgb`, `#rgba`, `#rrggbb`, `#rrggbbaa`) and functional (`rgb()`, `rgba()`, `hsl()`, `hsla()`) colour literals belong in `apps/web/src/styles/themes.scss`. Component and feature stylesheets must reference CSS custom properties.
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

`bin/ci/check-design-tokens.mjs` scans `apps/web/src/**` (`.css`, `.scss`, `.ts`, `.tsx`, excluding tests and `.d.ts` files) after stripping `/* */` and `//` comments:

| Rule | ID | Behaviour |
| --- | --- | --- |
| R1 | `undefined-var` | Strict per-file, per-variable ratchet. Every `var(--name)` reference must match a custom property definition (`--name:` in CSS/SCSS, `'--name':` / `['--name']` / ``[`--name`]`` inline style keys or `setProperty('--name', ...)` in TS/TSX). Any new undefined variable or count increase fails. |
| R2 | `radius` | Per-file ratchet on `border-radius`, `border-*-*-radius`, and `--*radius*` custom-property declarations whose shorthand parts are outside the allowed radius scale or expressions (`var()`, `calc()`, `min()`, `max()`, `clamp()`, `$`, `#{`). |
| R3 | `font-weight` | Per-file ratchet on `font-weight` declarations outside `400`, `500`, `600`, `700`, `normal`, `bold`, `inherit`, `initial`, `unset`, `revert`, `revert-layer`, `var(...)`, or `$...`. |
| R4 | `scss-radius-var` | Per-file ratchet on `$radius-*` uses, plus a hard failure if `variables.scss` defines any `$radius-*` name not listed in `scssRadiusDefinitions`. |
| R5 | `color-literal` | Per-file ratchet on hex and `rgb()`/`rgba()`/`hsl()`/`hsla()` literals in `.css`/`.scss` files outside `apps/web/src/styles/themes.scss`. |
| R6 | `important` | Per-file ratchet on `!important` in `.css`/`.scss` files. |

### Running and Updating the Baseline

```bash
# Run the check (also executed in CI Frontend job)
npm run check:design-tokens

# Tighten the baseline after reducing violations
npm run check:design-tokens -- --update-baseline
```

- Baseline counts in `bin/ci/design-token-baseline.json` may only move downward. When a file's count drops below its recorded baseline, the checker passes and prints a notice that the baseline can be tightened.
- `--update-baseline` rewrites `bin/ci/design-token-baseline.json` to current counts and exits `1` if any count would increase.
- Intentional exceptions that increase a count require explicit reviewer sign-off in the pull request and `--update-baseline --allow-increase`.
- **Known limitations** (out of scope for the static ratchet check):
  - Interpolated property names (for example `border-#{$side}-radius: 3px`) cannot be resolved without evaluating SCSS.
  - A nested property with a base value is only recognised when a space follows the colon (`border: 1px solid #fff { radius: 8px; }`). Write it in this form, or as separate declarations.
  - An ID selector that looks like a hex colour and appears in a selector-bearing at-rule (`@extend #abc`, `@at-root #abc`, `@scope (#abc)`, `@supports selector(#abc)`) is counted as a colour literal.
