# Release Process

This document defines the release note conventions used by the repo-local
`/release` workflow and `.github/workflows/release.yml`.

The `/release` workflow is CPA Manager Plus specific. It is suitable as a
repo-local Claude command or repo-local Codex skill, but it should not be
installed as a global cross-project skill.

`docs/release-notes/` is reserved for versioned release note files only. Do not
place process documentation or README files inside that directory.

Formal release notes are technical release records. Community-facing release
copy is authored separately so it can prioritize user value and readability
without weakening the technical notes.

## Release Note Files

```text
docs/release-notes/<tag>-<lang>.md
```

- `<tag>` keeps the `v` prefix, for example `v1.0.2` or `v1.1.0-beta.1`.
- `<lang>` is `zh` for the authored Chinese source or `en` for the English
  mirror translation.
- `zh-CN` is supported by CI as a compatibility fallback, but normal authored
  files should use `zh` and `en`.

Examples:

```text
docs/release-notes/v1.0.2-zh.md
docs/release-notes/v1.0.2-en.md
```

## Community Release Post

Each new release must include a reviewed Telegram post:

```text
docs/release-posts/<tag>-telegram.html
```

Example:

```text
docs/release-posts/v1.0.2-telegram.html
```

The repo-local `/release` workflow drafts this file before confirmation and
shows the exact message in the release plan. Commit it in the same release PR
as the Chinese and English release notes. Do not generate or rewrite the post
inside GitHub Actions.

Write the post for users and community members rather than code reviewers:

- use `## <M> 月 <D> 日 v<version>` as the Markdown heading, with `更新内容`,
  `注意事项`, `发布截图`, and `致谢` sections as applicable;
- list every release-relevant, user-visible semantic change in `更新内容`; do
  not impose a fixed item count, but merge implementation commits that result
  in the same user behavior;
- keep each bullet to one concise sentence with the product subject and its
  user-visible result; retain necessary product terms but omit CRUD lists,
  commit/file counts, internal paths, tests, demo fixtures, and pure CI noise;
- include `注意事项` only for upgrade, data, compatibility, configuration, or
  meaningful behavior changes that users need to act on or understand;
- include `发布截图` only when a specific screen or workflow should be shown;
- keep `发布截图` in the Markdown release summary only; Telegram HTML must omit
  it because the workflow does not attach media;
- include acknowledgements only for external contributors and preserve their
  GitHub profile links;
- keep claims factual and grounded in the formal release notes;
- keep the complete HTML body within 3,500 characters.

Telegram posts use a conservative HTML subset supported by the Bot API:

```text
<b> <i> <code> <a href="https://example.com">...</a>
```

Escape other HTML characters. Do not include inline keyboard JSON, bot tokens,
chat IDs, thread IDs, or any other secret in the post file. The release
workflow adds one `View Release` button at send time.

After both release jobs succeed, `.github/workflows/release.yml` reads the
tag-matched post and sends it through Telegram Bot API `sendMessage`. Configure
these repository secrets:

```text
TELEGRAM_BOT_TOKEN
TELEGRAM_CHAT_ID
TELEGRAM_MESSAGE_THREAD_ID  # optional, for a forum topic
```

Missing configuration or a missing post file skips the notification with an
Actions warning. Telegram delivery failure must not roll back or invalidate an
otherwise successful GitHub Release.

## CI Lookup

On tag pushes, the `Generate release notes` step checks the current tag in this
priority order:

```text
docs/release-notes/<tag>-zh.md
docs/release-notes/<tag>-zh-CN.md
docs/release-notes/<tag>-en.md
```

- If a file is found, it becomes the GitHub Release body.
- If no file is found, CI falls back to `git log --pretty="- %h %s"` for the
  range from the previous tag to the current tag.

The filename must match the pushed tag exactly. Otherwise, CI will use the git
log fallback.

## Writing Template

Chinese is the authored source. Other languages should preserve the same
structure, links, and statistics. Language switch links must be tag-pinned
GitHub blob URLs under `docs/release-notes/` because GitHub Releases render the
curated note body outside that directory.

```markdown
# CPA Manager Plus <version>

> <n> commits · <files> files changed · +<added> / -<deleted>

> [English ->](https://github.com/seakee/CPA-Manager-Plus/blob/<version>/docs/release-notes/<version>-en.md)

## Overview

<One short paragraph describing the release theme and context.>

## Highlights

### Features

- <User-facing capability description> (`<scope>`)

### Fixes

- <What was fixed and the affected scope>

<Keep only non-empty groups as needed: Performance / Refactor / Docs / Chore / CI / Build. Drop merge commits and noise.>

## Upgrade Notes

<Breaking changes, migration steps, or risk notes. Use "None" if not applicable.>

## Acknowledgements

<List external contributors only. Omit the section when there are none.>

- @<contributor> - <one sentence summarizing the contribution>

---

**Full Changelog**: https://github.com/seakee/CPA-Manager-Plus/compare/<previous tag>...<version>
```

## Commit Type Groups

| Type     | Group       |
| -------- | ----------- |
| feat     | Features    |
| fix      | Fixes       |
| perf     | Performance |
| refactor | Refactor    |
| docs     | Docs        |
| chore    | Chore       |
| ci       | CI          |
| build    | Build       |
