## Summary

<!-- What changed and why. Keep it short, 2-4 lines. -->

> Community and maintenance PRs must target `dev`. Repository automation
> retargets other pull requests to `dev`; this can change the diff and make
> existing review comments outdated. `main` accepts only a promotion PR whose
> source is this repository's `dev` branch.

## Scope

<!-- Check all areas touched by this PR. -->

- [ ] Frontend panel
- [ ] Manager Server
- [ ] CPA panel mode
- [ ] Full Docker mode
- [ ] Native packages / release
- [ ] Docs / Wiki
- [ ] CI / build / tooling

## Changes

<!-- List concrete changes, not implementation trivia. -->

-
-

## User Impact

<!-- What users will notice. Use "No user-facing behavior change" if applicable.
For UI changes, list every visible change (radius, colour, shadow, height, spacing, font weight) in light and dark themes; see docs/development/ui-design-system.md. -->

## Compatibility / Runtime Notes

<!-- Mention mode-specific behavior, config changes, queue behavior, or N/A. -->

- CPA panel mode:
- Manager Server mode:
- Full Docker / native packages:

## Data / Security Notes

<!-- Required for SQLite, admin key, CPA Management Key, auth files, logs, raw usage data, or plugins. Use N/A if not relevant. -->

## Risk / Rollback

Risk level: Low / Medium / High

Rollback notes:

## Verification

<!-- Check what was actually done. Keep skipped items unchecked and explain when needed. -->

- [ ] Type check
- [ ] Lint
- [ ] Design token check (`npm run check:design-tokens`)
- [ ] Tests
- [ ] Build
- [ ] Manual UI check
- [ ] Docs/link check
- [ ] Not applicable, docs-only

Commands / evidence:

```text

```

## Screenshots / Recordings

<!-- Required for visible UI changes. Use N/A for backend/docs-only changes. -->

## Docs

User documentation lives in [cpamp-hq/docs](https://github.com/cpamp-hq/docs) (1.x on `main`, v2 on `next`); this repository no longer builds a docs site.

- [ ] README / README_CN updated for user-visible capabilities
- [ ] Linked the `cpamp-hq/docs` PR (1.x → `main`, v2 → `next`), or state why it is not needed
- [ ] Release notes needed
- [ ] Not needed — explanation included below

Docs decision / `cpamp-hq/docs` PR:

## Related

<!-- Closes #123 / Fixes #123 / Refs #123 / N/A -->
