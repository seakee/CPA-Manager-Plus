# Real-Docker Upgrade acceptance (`bin/ci/upgrade-docker.mjs`)

Delivery slice: U1-8, the D harness. It is the only acceptance that exercises the
Upgrade chain against a **real 1.x release image**, a real seeded data volume and
the real v2 binary, instead of a hand-written SQLite fixture.

## Run it

```bash
npm run test:upgrade-docker                    # build the fixture image, then run
node bin/ci/upgrade-docker.mjs --reuse-fixture # reuse the recorded image
node bin/ci/upgrade-docker.mjs --keep          # keep the scratch tree
```

* `SKIP_UPGRADE_DOCKER=1` is the **only** way to skip it, and it says so in the
  log and in CI (`::warning`). A missing Docker daemon is a failure, not a skip.
* Every object it creates is named `cpampd<runid>-…` and every volume
  `cpampd<runid>_…`. It refuses to start when that prefix is already taken, and
  it fails when a container or volume outside the prefix changed, so it can run
  on a host that also runs other stacks.
* Evidence lands in `bin/ci/evidence/upgrade-d/<runid>.json`: the fixture image
  ids and source commits, the seeded dataset shape, every command with its exit
  code and output digests, the chain reports, each fault's exit code and typed
  code, the rollback plan and verdict, the wizard contracts, the findings, the
  cleanup result and a `sha256` of the document itself. `stableDigest` covers the
  run's stable facts only, so two runs of the same build agree.

## What it proves

| Step | Assertion |
| --- | --- |
| fixture | `git worktree` of the 1.x tag, `docker build`, image id and commit recorded; `--reuse-fixture` skips the build and still passes |
| seed | three UTC days of usage events, two models, two client keys, model prices and key aliases, one archive run that is verified and whose raw rows are deleted — all through the 1.x HTTP API |
| `upgrade preflight` | exit 0, `ready`, no blocked check, the source version the contract accepts |
| `upgrade run` | exit 0, stage `copy_in`, `restored`, both source fingerprints and the backup manifest digest recorded |
| `upgrade reconcile` | exit 0, stage `reconcile`, every applicable domain compared with no mismatch and no unacknowledged day |
| `upgrade status` | the persisted row reads back at stage `reconcile` read-only |
| wizard routes | on a real Fresh installation, `GET …/installation/upgrade/{preflight,status}` answer 200 to the 1.x administrator key only, project `ready` and `nextAction`, and contain no credential, path, full digest or data-key material; a write is 405 |
| fault: source touched after copy-in | a second `run` reports the recorded operation and writes nothing; `rollback --prepare` refuses with `upgrade_source_changed` |
| fault: copy-in interrupted before the row | the provenance marker is caught and the process killed; `status` shows no row; the resume adopts the materialized store (`restored:false`) and records `copy_in` |
| fault: cleared derived rollup with retained support | reconcile refuses with `upgrade_reconcile_mismatch` naming the days; after `--acknowledge` it marks them as `derived_rollup_lost` |
| fault: cleared derived rollup without support | `upgrade_reconcile_target_incomplete`, unwaivable even with `--acknowledge` |
| fault: dropped table on the target | `upgrade_reconcile_target_incomplete` naming the missing table |
| fault: natively busy source | `upgrade_source_busy` from `flock` held on `usage.sqlite.manager.lock` |
| rollback | plan → real compose apply on the plan's volume and port → the restarted 1.x accepts a new row → probe of the manager's own listener refused with `upgrade_rollback_probe_self` → `--verify` records `rolled_back` with the image, endpoint and observed fingerprint |
| admission, unfinished | with the recorded row at `copy_in` and at `reconcile` the manager answers `/health` 503 and serves both wizard reads on the fixed Bootstrap carrier: 200 to the 1.x administrator key only (status shows the recorded stage and `nextAction`), 401 to no credential or a foreign one, and 405 with `Allow: GET` only after the key is proved; the canonical continuation stays 404 because a copied root carries no Fresh record |
| admission, complete | the terminal `complete` row admits the full writable surface (`/health` 200) and both reads answer 404 to every caller, the source key included |

Every fault case also asserts that the target root is byte-identical across the
refused command (`targetRootUnchanged`).

## Defects this harness found and closed

1. **Source version ladder** (`internal/service/upgrade/schema.go:366`, was
   `:370`). `usage_archive_deleted_coverage_daily` was listed as a v1.14.6 marker
   although every 1.14.x release creates it (v1.14.0 included). Any real 1.x
   source therefore reported markers newer than the pinned release, and the
   pinned release itself was refused with `upgrade_source_version_unsupported`:
   no real install could pass preflight. The table now belongs to the v1.14.0
   step, and a marker the pinned release's own schema carries can never be
   reported as newer. Regression:
   `TestSourceVersionAcceptsThePinnedReleaseSchema`.
2. **Interrupted copy-in** (`internal/service/upgrade/copyin.go:657`, was
   `targetMigrated`). A run killed after materialize and before the operation row
   committed left its provenance marker, but the resume compared the
   migration-rewritten database sidecars with the backup, deleted the marker and
   then refused to replace the members it had already moved — the exact window
   the marker exists for. The marker now proves materialization and only the
   members the migration never touches are compared. Regression:
   `TestCopyInResumesAfterAnInterruptionInsideTheMigration`.
3. **Wizard reads after the copy-in** (`cmd/cpa-manager-plus/startup_discovery.go`,
   was `:59`). An unfinished operation admits only the fixed Bootstrap surface,
   and the copy-in replaces `usage.sqlite`, so the copied root carries no Fresh
   record for the canonical continuation: both wizard read routes answered 404
   while the process had a `copy_in` or `reconcile` stage recorded, and the
   operator could never see the stage the chain had just written. The two reads
   are now mounted beside the Upgrade entry leaf on the fixed surface, under the
   same gate and the same source credential, and retire on completion.
   Regression: `TestStartupServesTheUpgradeWizardFromTheCopiedOperation`.

## Gaps the harness records but cannot close inside this slice

* **Source release pin.** The contract accepts `v1.14.4`
  (`internal/domain/bootstrap/contract.go:12`), so the planned `v1.14.10`
  fixture is refused with `upgrade_source_version_unsupported`. The harness reads
  the accepted release from the product's own report and proves the chain
  against it, while building and recording the `v1.14.10` fixture for the U1-1b
  re-pin (same image, same seed script: `--v1-tag v1.14.10`). After the re-pin
  the two fixtures collapse into one and the harness needs no change.
* **Completion transition.** No step in this milestone writes the terminal
  `complete` row, so the completed-admission case writes that stage onto the row
  the real copy-in recorded. The evidence admission demands — the
  `upgraded-legacy` kind, both source fingerprints and the backup manifest digest
  — is the copy-in's own; only the stage token is the harness's. The evidence
  says so (`admission.completionInput`).
