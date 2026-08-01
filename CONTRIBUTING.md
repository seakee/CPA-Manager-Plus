# Contributing to CPA Manager Plus

Thanks for contributing. `main` is the stable release branch and remains the
repository default branch. All feature, fix, documentation, and maintenance
work is integrated through `dev` before it is promoted to `main`.

## Branch and Pull Request Flow

1. Fork the repository and add the upstream remote.
2. Fetch `upstream/dev` and create your working branch from it.
3. Open your pull request against `seakee/CPA-Manager-Plus:dev`.
4. Address review feedback and keep the branch current with `upstream/dev`.
5. A maintainer promotes the tested repository `dev` branch to `main`.

Do not open a feature or fix pull request directly to `main`. `main` accepts
only a pull request from this repository's `dev` branch.

```bash
git remote add upstream https://github.com/seakee/CPA-Manager-Plus.git
git fetch upstream
git switch -c fix/short-description upstream/dev

# Before requesting review, update your branch as appropriate for your team.
git fetch upstream
git rebase upstream/dev
```

## Before Opening a Pull Request

- Read the pull request template and complete every applicable section.
- Keep each pull request focused; do not combine unrelated features and fixes.
- Include tests for changed behavior and run the checks relevant to your scope.
- Add screenshots or recordings for visible UI changes.
- Preserve both CPA Panel and Full Docker semantics when your change affects
  authentication, setup, proxying, collection, or monitoring.
- Never commit secrets, admin keys, CPA Management Keys, SQLite data, generated
  runtime files, or local configuration.

## Local Verification

Use the narrowest checks that cover your change, then run broader checks when
the change crosses frontend, Manager Server, packaging, or runtime boundaries.

| Area | Command |
| --- | --- |
| Frontend type and lint | `npm run type-check` and `npm run lint` |
| Frontend and repository tests | `npm run test` |
| Frontend bundle | `npm run build` |
| Manager Server | `npm run manager-server:test` |
| Concurrent backend behavior | `cd apps/manager-server && go test -race ./...` |

CI runs the applicable checks on pull requests to `dev`. Passing CI does not
replace mode-specific manual verification where the pull request template
requires it.

## Maintainer Promotion

After `dev` is reviewed and tested, open a pull request from
`seakee/CPA-Manager-Plus:dev` to `main`. The promotion must pass the same CI,
the source-branch gate, required review, and branch-protection rules before it
is merged. Create release tags only from the verified `main` commit.
