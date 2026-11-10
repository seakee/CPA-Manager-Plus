# UI Design System

This specification defines the token contracts, layout conventions, and automated CI governance for the CPAMP v2 web console (`apps/web`).

`apps/web/src/styles/themes.scss` is the single source of truth for theme token values across `:root`, `[data-theme='white']`, `[data-theme='dark']`, mobile `@media` overrides, and `[data-visual-effects='reduced']`. Full Web carrier aliases in `apps/web/src/fullweb/carrier.css` resolve directly to tokens defined in `themes.scss` without introducing a second palette.

## 1. Primary Actions and Accent Tokens

- **Solid primary fills**: Filled primary buttons, primary action pills, and checked switch tracks use `--primary-solid`, `--primary-solid-hover`, and `--primary-solid-active` paired with `--primary-contrast` text. This ramp is shared across light and dark themes so button text maintains `>= 4.5:1` contrast in every state.
- **Solid danger fills**: Filled destructive buttons use `--danger-solid`, `--danger-solid-hover`, and `--danger-solid-active` with `--primary-contrast` text, shared across themes for the same reason. `--color-danger` / `--error-color` stay for status text, badges, and charts.
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
- **Elevation and overlay scrims**:
  - Non-floating drop shadows (`--app-shadow`, `--app-shadow-soft`, `--glass-shadow`, `--shadow`, `--shadow-lg`) are `none`.
  - The only elevation cue on cards and panels is `--app-specular-highlight` (`box-shadow: var(--app-specular-highlight)`).
  - Floating layers (menus, popovers, tooltips, dialogs, drawers) may keep a drop shadow so they separate from the page. New or edited floating layers must be marked with `/* floating layer: elevation allowed */` and use the `--floating-shadow-*` scale defined per theme in `themes.scss` (do not duplicate `[data-theme='dark']` shadow overrides in component stylesheets); any remaining literal floating-layer shadow is legacy debt to migrate when touched:
    - `--floating-shadow-sm`: compact status tooltips that show a short, non-scrolling summary next to their trigger (account status bar hover tooltip, AI provider status bar tooltip, monitoring health status bar tooltip).
    - `--floating-shadow-md`: tooltips that carry explanations or details, whether inline or portaled, including scrollable or interactive ones (`InfoTooltip`, `AccountRequestTimeTooltip`, monitoring quota tooltip, `FailureDetailsTooltip`, AI provider key test status tooltip). Pick by content, not by how the layer is rendered.
    - `--floating-shadow-lg`: floating action and context menus (`DropdownMenu`, `ProviderToolbar` model and sort dropdowns, `ModelMappingDiagram` context menu, header and login language/theme/visual-effects menus, the collapsed-sidebar navigation flyout, including the subtle accent ring), and floating action bars anchored to the viewport edge (credentials batch action bar, secondary-page bottom action bar, mobile filter sheet). Bars use this tier because their shadow falls off-screen at the anchor edge and the tier's accent ring is what separates the bar from the page.
    - `--floating-shadow-xl`: form select popovers (`Select`, including its input-focus accent ring).
    - `--floating-shadow-dialog`: centered modal dialogs (`.modal`).
    - `--floating-shadow-drawer`: side drawer panels (`Drawer` panel edge shadow, Codex inspection config drawer).
  - The dark theme deepens the scale instead of reusing the light values: every tier keeps its light offsets and adds a heavier drop layer plus a hairline ring (`0 0 0 1px rgb(255 255 255 / 0.06)` for `sm`, `0.08` for `md`, the accent ring for `lg` and above), because against a near-black page the drop layer alone barely reads and the ring carries the separation.
  - Overlay backdrops use the `--overlay-scrim*` tokens from `themes.scss`:
    - `--overlay-scrim`: centered modal dialog backdrop (`.modal-overlay`).
    - `--overlay-scrim-backdrop`: mobile navigation sidebar backdrop (`.sidebar-backdrop`).
    - `--overlay-scrim-drawer`: slide-over drawer backdrop (`Drawer` overlay, Codex inspection config drawer overlay).

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
- **Data-visualisation radius tokens (chart elements only)**: `apps/web/src/styles/themes.scss` defines `--radius-data-sm` (`2px`) and `--radius-data` (`3px`) in `:root` exclusively for chart and data-visualisation elements (bars, progress/rank bars, heat cells, AI provider status blocks, config diff stat cells, legend swatches, and sparkline segments) so their compact geometry is preserved. Ordinary UI elements (cards, badges, buttons, and rows) must use the concentric role scale (`--app-radius-*`) instead.
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
  - Replace a literal only with a token whose meaning and value match in both themes. When no such token exists and a swap would change the appearance, keep the literal with a one-line comment giving the reason; it stays counted in the R5 baseline. Accepted cases today: CodeMirror selection and search-match highlights (`ConfigPage.module.scss`), the GitHub brand tile (`SystemPage.module.scss`), and the sidebar navigation white specular gradient (`layout.scss`). New cases need reviewer sign-off in the pull request.
- **Avoid `!important`**: Maintain flat selector specificity in CSS/SCSS modules instead of overriding rules with `!important`.
  - To override a shared component (for example the `Select` trigger), qualify the selector with its parent or repeat the class (`.parent .trigger, .trigger.trigger`) so it outranks the shared rule, rather than adding `!important`.
  - When removing an existing `!important`, confirm in the browser that the computed value is unchanged in every state the declaration targeted (default, hover, active, focus-visible, expanded, disabled) in both themes.
- **Page headers**: Top-level pages reached directly from the sidebar must not render a duplicate in-page `<h1>` or breadcrumb header (the shell top bar breadcrumb serves as the page title). Drill-down subpages keep a lightweight back navigation bar.
- **Contrast**: Body copy, badge labels, and button text must maintain at least `4.5:1` contrast against their background fill in both light and dark themes.
  - White text on a coloured fill uses the solid ramps (`--primary-solid*` / `--danger-solid*` with `--primary-contrast`), never the theme-adaptive `--primary-color`.
  - Status text tinted with a badge text token can fall below `4.5:1` on light surfaces. Mix it with the primary text colour, for example `color-mix(in srgb, var(--data-badge-success-text) 62%, var(--text-primary))`; danger text typically needs about `75%`. Measure the result in both themes.

## 7. Undefined Variables

There is no known undefined-variable debt. The R1 baseline is empty, so any `var(--name)` without a matching definition fails CI. Replace it with an existing token from `themes.scss` rather than adding an alias.

## 8. Automated Governance (`check:design-tokens`)

`bin/ci/check-design-tokens.mjs` scans `apps/web/src/**` (`.css`, `.scss`, `.ts`, `.tsx`, excluding tests and `.d.ts` files). It strips `/* ... */` block comments in all scanned files, strips `//` line comments only in `.scss`, `.ts`, and `.tsx` files (not in `.css`), and masks quoted strings, `url(...)` spans, and `#{...}` interpolation delimiters in `.css`/`.scss`. Interpolation bodies stay visible everywhere, including inside quoted strings and `url(...)`, because Sass evaluates them there:

| Rule | ID | Behaviour |
| --- | --- | --- |
| R1 | `undefined-var` | Strict per-file, per-variable ratchet. Every `var(--name)` reference must match a custom property definition: `--name:` in CSS/SCSS, or an entry in the runtime property registry (`bin/ci/design-token-runtime-properties.json`) for names that TS/TSX sets at runtime. TS/TSX source is not parsed for definitions. Any new undefined variable or count increase fails. |
| R2 | `radius` | Per-file ratchet on `border-radius`, `border-*-*-radius`, and `--*radius*` custom-property declarations whose shorthand parts are outside the allowed radius scale or expressions (`var()`, `calc()`, `min()`, `max()`, `clamp()`, `$`, `#{`). Dedicated chart radius tokens (`--radius-data-sm`, `--radius-data`) declared in `apps/web/src/styles/themes.scss` are recorded in its R2 baseline entry and referenced via `var(...)` on chart/data-viz elements only. |
| R3 | `font-weight` | Per-file ratchet on `font-weight` declarations and on numeric weight tokens (`1`–`1000`) or `bolder`/`lighter` that precede the font-size in `font` shorthand declarations, outside `400`, `500`, `600`, `700`, `normal`, `bold`, `inherit`, `initial`, `unset`, `revert`, `revert-layer`, `var(...)`, or `$...`. Words after the font-size or after the first comma are family names and are not checked. |
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

### Runtime Custom Properties

Custom properties that TS/TSX sets at runtime (inline `style={{ '--name': value }}` or `element.style.setProperty('--name', value)`) are not discovered from source. List each one in `bin/ci/design-token-runtime-properties.json`:

```json
{
  "version": 1,
  "properties": {
    "--content-center-x": "components/layout/MainLayout.tsx: content centre via setProperty"
  }
}
```

- Each key must be a valid `--name`, and each value a non-empty note naming where the property is set.
- R1 treats registered names as defined. A stylesheet that reads a runtime property which is not registered fails R1, and the remediation message points to the registry.
- A registered name that no longer appears as a quoted literal (`'--name'`, `"--name"` or `` `--name` ``) in any scanned TS/TSX file is a hard failure, so stale entries cannot keep masking references. Remove the entry when the code stops setting the property.
- The registry is read from the same directory as the baseline (override with `--runtime-properties <path>`). A missing default registry means no runtime definitions; an explicitly passed path that cannot be read, or an invalid registry, fails the run.

### Known Limitations

- **Intentional scope boundaries**:
  - **No Sass evaluation**: SCSS maps, `@each`/`@for`-generated declarations, mixins/functions that synthesize property names, and interpolated property names (such as `border-#{$side}-radius: 3px`) are not evaluated.
  - **Global custom-property existence (R1)**: R1 checks that a `--name` is defined somewhere in the scanned tree (or registered as a runtime property) rather than modelling per-theme or per-selector CSS cascade scope (for example, a variable declared only under `[data-theme='dark']` counts as defined globally).
  - **Runtime property registry (R1)**: the stale check only confirms that each registered name appears as a quoted literal in some scanned TS/TSX file; it does not verify that the literal is actually passed to an inline style or `.setProperty()`.
  - **Stylesheet-only rules (R2, R3, R5, R6)**: R2, R3, R5, and R6 inspect `.css`/`.scss` stylesheets only and do not inspect `.ts`/`.tsx` files.
  - **Colour function and keyword scope (R5)**: `color-mix()` itself is not counted as a colour literal (only colour literals inside its arguments count), and CSS named colours (`red`, `white`, etc.) are not counted.
  - **CSS identifier escapes**: Plain CSS backslash escapes in identifiers (for example `--\66 oo`) are not decoded.
  - **Per-file ratchet granularity (R2–R6)**: R2–R6 lock violation counts per file rather than per line or selector.
- **Remaining lexical parser boundaries**:
  - A SCSS nested property with a base value is recognised only when whitespace follows the colon (`border: 1px solid #fff { radius: 8px; }`). Write nested properties in this form or as separate declarations.
  - An ID selector that looks like a 3-, 4-, 6-, or 8-digit hex colour and appears in a selector-bearing at-rule prelude (`@extend #abc`, `@at-root #abc`, `@scope (#abc)`, `@supports selector(#abc)`) is counted as a colour literal.
  - R3 `font` shorthand inspection treats the first dimension, percentage, unitless zero, size keyword, or `size/line-height` token as the font-size and checks only the tokens before it; a font-size supplied through `var()`/`calc()`/`$`/`#{}` is not recognised, so a later family named `bolder`/`lighter` would still be counted. It does not parse the full CSS `font` shorthand grammar.
  - Nested quotes of the same kind inside an interpolated string (`"a #{"b"} c"`) end the outer string early; use the other quote style inside the interpolation.
- **Resolved in baseline schema v2**:
  - TS/TSX no longer defines custom properties by inference. Names set at runtime through inline styles or `.setProperty()` count as defined only when listed in `bin/ci/design-token-runtime-properties.json`, which removes the lexical TSX scanning behind R1-01, R2-01, R3-01, R4-01 and R5-01.
  - Quoted strings and `url(...)` contents in `.css`/`.scss` are masked across R1, R4, R5, and R6, while `#{...}` interpolation bodies, including those inside strings and `url(...)`, remain inspected; string literals inside an interpolation body are masked again (R1-02, R2-02, R3-02).
  - Numeric weight tokens (`1`–`1000`) and `bolder`/`lighter` before the font-size in `font` shorthand declarations are checked by R3; family names after the size, including after a unitless `0` size, are not (R1-03, R2-03, R3-03).
  - Legacy `$radius-*` definitions in `variables.scss` freeze both variable name and raw value in `scssRadiusDefinitions` (R1-04).
  - Baseline loading validates `version: 2`, all six `rules` keys, and the `scssRadiusDefinitions` map, failing closed on missing or invalid files and requiring `--init` for first-time creation (R1-05).
  - Modern CSS colour functions (`hwb()`, `lab()`, `lch()`, `oklab()`, `oklch()`, `color()`) are counted by R5 with CSS-identifier boundary checks (R1-06).
  - Single-line `//` comments are stripped only in `.scss` and `.ts`/`.tsx` files, not in `.css` (R1-07).
  - CI workflow integrity tests lock the `Frontend` job step ordering (`npm ci` -> `Lint` -> `Check design tokens` -> `Test`) and `npm run check:design-tokens` command (R1-08).
  - Unused `valueOffset` declaration metadata was removed (R1-09).

## 9. Style Pull Request Checklist

For pull requests that change stylesheets or the look of UI components:

- **Scope**: do not change behaviour, routes, i18n text, `data-testid`, or class names referenced from TSX or tests; do not add dependencies.
- **Visible changes**: list every visible change (radius, colour, shadow, height, spacing, font weight) under User Impact, in light and dark themes. Reviewers treat a visible change that is not listed, or that breaks the rules above, as a regression.
- **Contrast**: measure every changed text colour against its background in both themes and report the ratios.
- **Screenshots**: attach light and dark screenshots of the changed surfaces.
- **Baseline after a rebase**: when `bin/ci/design-token-baseline.json` conflicts, start from the base branch's file, run `npm run check:design-tokens -- --update-baseline`, and confirm that no per-file entry is higher than on the base. Use `--allow-increase` only for an exception documented here (for example the chart radius tokens in `themes.scss`) and state it in the pull request.
