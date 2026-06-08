---
name: release
description: CPAMP-specific release workflow. Use when the user asks to run or plan the CPA Manager Plus release flow, create versioned release notes, open or merge the release PR, or push the release tag.
---

# CPAMP Release

This is a repo-local Codex skill for CPA Manager Plus only. Do not generalize
it to other projects or install it globally. Follow `docs/release.md`,
`.github/workflows/release.yml`, and the shared approval semantics in
`/Users/seakee/.codex/prompt-policy.md`.

GitHub remote operations must use the GitHub MCP tools (`mcp__github__*`) when
available. Do not use `gh` for PRs, branch reads/writes, release checks, or
other GitHub API work while MCP can cover the operation.

## Hard Rules

1. Explicit confirmation is required before any state-changing operation.
2. Before confirmation, run read-only checks only: inspect auth, refs, status,
   tags, commits, and render previews. Do not switch branches, pull, write
   files, commit, push, merge, or tag.
3. `--dry-run` is non-mutating: preview notes and the execution plan only.
4. Release notes must be committed to `main` through a release PR before the
   tag is pushed.
5. `docs/release-notes/` is reserved for versioned note files:
   `docs/release-notes/<tag>-<lang>.md`.
6. No AI markers in commits, PRs, release notes, or tags.
7. Do not mutate `.gitignore` or tracked collaboration files unless the user
   explicitly approves that exact scope.
8. If the current GitHub MCP toolset cannot perform a required remote mutation
   such as creating a tag/ref or deleting a remote branch, stop and ask for an
   explicit fallback decision. Do not silently use `gh` or `git push`.

## Read First

- `docs/release.md`
- `.github/workflows/release.yml`
- Current branch/status and current tags

## Pre-Confirmation Workflow

1. Read-only preflight:

```bash
current_branch="$(git branch --show-current)"
git status --porcelain
git rev-parse --verify main
git rev-parse --verify origin/main
git rev-list --left-right --count main...origin/main
last_tag="$(git tag --list 'v*' --sort=-v:refname | head -1)"
```

Also use GitHub MCP for remote source-of-truth checks:

- `mcp__github__list_branches` for `main` and the planned release branch.
- `mcp__github__list_tags` or `mcp__github__get_tag` for the previous and
  planned tags.
- `mcp__github__get_file_contents` when remote file contents are needed.

Abort or ask for direction if the tree is dirty, MCP access is missing, refs
are missing, local `main` differs from remote `main`, or the planned remote
branch/tag already exists.

2. Resolve version:
   - Validate an explicit version as `v<major>.<minor>.<patch>` with optional
     prerelease suffix.
   - If omitted, infer from `${last_tag}..HEAD --no-merges`: breaking changes
     -> major, `feat` -> minor, otherwise patch.
   - Confirm the tag does not already exist.

3. Collect release data from `${last_tag}..HEAD`:
   - commit count
   - shortstat
   - non-merge commit subjects grouped by conventional type
   - external contributors, excluding repo owner

4. Draft release note content in memory, using `docs/release.md` as the
   template. Render previews for requested languages. Do not write files yet.

5. Present the plan:
   - version/tag
   - note files to be written
   - branch `release/<version>`
   - commit message
   - PR base `main`
   - merge method
   - tag push step
   - whether `main` needs update after confirmation

Stop here for `--dry-run`.

## Execute After Confirmation

Only after explicit confirmation, use GitHub MCP for the remote release PR flow:

1. `mcp__github__create_branch` to create `release/<version>` from `main`.
2. `mcp__github__push_files` to commit the confirmed release note files to that
   branch in one commit.
3. `mcp__github__create_pull_request` to open the release PR against `main`.
4. `mcp__github__pull_request_read` with `get_status` and/or `get_check_runs`
   to inspect PR checks when requested.
5. `mcp__github__merge_pull_request` to merge with the selected method.

Do not use `gh pr create` or local `git push` for operations covered by MCP.
If tag creation or remote branch deletion is required and no MCP tool for that
operation is available, stop after the MCP-supported steps and report the exact
remaining action instead of using a fallback automatically.

Report the PR URL, release tag URL, and Actions run link.
