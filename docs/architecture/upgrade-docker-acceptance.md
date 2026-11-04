# Real-Docker Upgrade acceptance (`bin/ci/upgrade-docker.mjs`)

Delivery slice: U1-8, the D harness. It is the only acceptance that exercises the
Upgrade chain against a **real 1.x release image**, a real seeded data volume and
the real v2 binary, instead of a hand-written SQLite fixture.

## Run it

```bash
npm run test:upgrade-docker                    # build the fixture image, then run
node bin/ci/upgrade-docker.mjs --reuse-fixture # reuse the recorded image
node bin/ci/upgrade-docker.mjs --keep          # keep the scratch tree
node --test bin/ci/upgrade-docker.test.mjs     # the harness's own logic, no Docker
```

* `SKIP_UPGRADE_DOCKER=1` is the **only** way to skip it, and it says so in the
  log and in CI (`::warning`). There is no path filter: every pull request and
  every push to the gated branches creates the job. A missing Docker daemon is a
  failure, not a skip.
* Every object it creates is named `cpampd<runid>-…` and every volume
  `cpampd<runid>_…`. It refuses to start when that prefix is already taken, it
  tracks each container, volume, network, compose project, host process and
  `git worktree` checkout **before** creating it, and it releases them on the
  normal path, on every error path and on SIGINT/SIGTERM (a handled signal runs
  the same release and exits 130/143). A release that fails, a listing it cannot
  read, a registry entry for any kind that survives the release, or any surviving
  object under its own prefix makes the process exit non-zero: a partial cleanup
  can never read as PASS. Docker listings only name containers, volumes and
  networks, so the registry is checked for every kind whether or not the listings
  answered. Every image it builds, tags or removes lives in its own `cpampd-`
  namespace: the fixture is `cpampd-fixture/legacy-v1:<version>` and the only
  other reference is this run's `cpampd<runid>/upgrade:vX.Y.Z` alias. The three
  argv shapes that write an image (`build -t`, `tag`, `image rm`) are checked
  before Docker runs, so it never creates, retags or removes a `cpamp-*` image;
  the pre-existing `cpamp-d-fixture-v1:*` and `cpampd-fixture/upgrade:*` tags are
  **inspect-only** inputs. It also never addresses `buildx_buildkit_*`,
  `cpa-manager-plus-cpa-manager-plus-1` or `cpa-usage-keeper`.
* The handled-signal boundary is **the running JavaScript turn**. The first
  SIGINT/SIGTERM closes the registry fence synchronously: every later `track`
  and every host-process spawn is refused, so a step that was awaiting when the
  signal arrived cannot start the next step or register a new resource after
  cleanup has taken its release set. Finalisation releases the tracked set first
  (that release is often what lets an interrupted wait return), then waits for
  the acceptance continuation to settle before it decides the host is clean. A
  failure during an abort is logged and does not change the signal's exit code.
  A second signal — the same kind included — joins the same finalisation
  instead of releasing again or falling through to Node's default termination,
  so a repeated Ctrl-C or a supervisor's repeated SIGTERM cannot interrupt the
  release or skip the evidence.
  `SIGKILL`, a crash or a host power loss cannot run any handler: an object
  created by such a run may survive and must be cleaned from the printed
  `cpampd<runid>` prefix by an operator. This guarantee covers handled signals,
  not unhandled termination.
* Every source-reading command (`preflight`, `run`, `reconcile`) declares the
  pinned accepted release with `--source-version`. The value is not a second pin
  in the harness: the probe preflight reads the fixture it inspects, the
  product's report names the accepted release, and every later invocation —
  including the `next` command `upgrade run` prints — carries that exact value.
  A command that would run without a declaration fails the harness before the
  product sees it.
* The rollback alias is run-owned: `cpampd<runid>/upgrade:vX.Y.Z`, tagged from
  the fixture bytes, recorded in the resource registry before it is created, and
  removed only at cleanup (the tag, never the image the fixture repository
  holds). The shared `cpampd-fixture/upgrade` tags are read-only inputs; older,
  unlabeled images already occupy `:1.14.10` and `:1.14.4`, so the harness never
  retags, removes or reuses them. Cleanup inspects those shared references and
  fails when a pre-existing image ID changed or any `cpampd<runid>/upgrade` tag
  survived.
* Evidence lands in `bin/ci/evidence/upgrade-d/<runid>.json` and is bound to the
  run that wrote it: `implementation` records the HEAD commit, the harness file
  digest, the built binary digest and whether the tree was clean; `verdict` and
  `exitCode` record the outcome; `hashRule` states the canonical rule
  (`sha256` of the document without its own `sha256`, `JSON.stringify(…, null, 2)`);
  `resultsDigest` covers what the run observed (chain reports, faults, rollback,
  admission), while `stableDigest` covers only the stable facts two runs of the
  same build are expected to agree on.
  `node bin/ci/upgrade-docker.mjs --verify-evidence <file> [--expect-head <sha>]`
  re-checks that hash, the verdict, the head binding, the built binary digest and
  a clean tree without Docker: a document from a dirty tree or one that never
  built a binary fails verification instead of passing as a binding to the
  commit. CI runs
  it on the file the run published to `GITHUB_OUTPUT` and uploads exactly that
  file, so an artifact can never be a committed historical record; the harness
  also refuses to reuse a fixture image that does not carry the provenance
  labels its build writes.
* The `sha256` is an integrity check, not an authenticity proof: a document can
  be edited and rehashed. What an edited document cannot do is name a commit the
  harness did not run at, which is why the head binding and the CI verification
  step exist.

## Harness logic tests

`node --test bin/ci/upgrade-docker.test.mjs` exercises the parts that decide what
the real-Docker run may touch, without Docker: the resource registry (ownership
refusals, release order including the run-owned image alias, a failed release
staying owned and failing the run, the exit-code verdict), the declared source
version (injected for preflight/run/reconcile, never invented, the printed next
command carries it), the rollback alias (prefix-derived `vX.Y.Z` reference,
shared names refused, release removing only this run's tag after an ownership
inspect, pre-existing images compared by ID), the signal guard (one release, exit 130/143, no second
release), port allocation (two ports of a run are never equal, busy and reserved
ports are skipped, exhaustion refuses), redaction (a registered secret never
survives a message and the leak assertion never reprints it) and the evidence
binding (untouched document verifies, an unrehashed edit fails, a rehashed edit
still fails the head binding, a document without a verdict/head is refused).
Three interleaving tests drive the real `main` → `runAcceptance` → `abortRun` →
`finalize` → `cleanupResources` flow with stand-ins only at the Docker, product
and process boundaries: a SIGTERM during an awaited wizard step must let that
step finish while refusing the next admission step and every later `track` or
spawn; a release pass that missed a tracked resource while all three Docker
listings answer must still fail cleanup through the registry; and a release that
throws must stay owned, fail cleanup and still exit with the signal code. These
are unit-level contracts over controlled stand-ins; they are not a substitute
for the real-Docker run below.

## What it proves

| Step | Assertion |
| --- | --- |
| fixture | `git worktree` of the 1.x tag, `docker build -t cpampd-fixture/legacy-v1:<version>` with the provenance labels (`io.cpamp.upgrade.fixture.{contract,tag,commit}`), image id and commit recorded; `--reuse-fixture` accepts only an image in that namespace carrying those labels and the tag's commit, and refuses an unlabeled one rather than rebuilding over it; the pre-existing `cpamp-d-fixture-v1:*` tags are inspect-only and never a build, tag or remove target; the rollback alias is this run's `cpampd<runid>/upgrade:vX.Y.Z`, resolves to the same image ID, carries the same labels, and is the only image reference cleanup removes |
| seed | three UTC days of usage events, two models, two client keys, model prices and key aliases, one archive run that is verified and whose raw rows are deleted — all through the 1.x HTTP API |
| `upgrade preflight` | exit 0, `ready`, no blocked check, the source version the contract accepts |
| `upgrade run` | exit 0, stage `copy_in`, `restored`, both source fingerprints and the backup manifest digest recorded |
| `upgrade reconcile` | exit 0, stage `reconcile`, all four comparison domains (`retained`, `pricing`, `coverage`, `history`) present on the table each names, applicable, every source day compared (`comparedDays === sourceDays`), zero mismatches, zero marked days; on the main chain the seeded day counts (2/3/1/3) and the recorded `domainsDone` are pinned |
| `upgrade status` | the persisted row reads back at stage `reconcile` with the same `domainsDone` the reconcile report compared |
| wizard routes | on a real Fresh installation, `GET …/installation/upgrade/{preflight,status}` answer 200 to the 1.x administrator key only, project `ready` and `nextAction`, and contain no credential, path, full digest or data-key material; a write is 405 |
| fault: source touched after copy-in | a second `run` reports the recorded operation and writes nothing; `rollback --prepare` refuses with `upgrade_source_changed` |
| fault: copy-in interrupted before the row | the provenance marker is caught and the process killed; `status` shows no row; the resume adopts the materialized store (`restored:false`) and records `copy_in`; over HTTP the same window reads `recorded` with `evidence: copy_in_marker`, stage and `nextAction` `copy_in` (`sourceRoot: available`), never the `preflight` step before any work started. This case is a **successful resume**: the target changes, so `targetRootUnchanged` is `false` and the code is `resumed_copy_in` |
| fault: cleared derived rollup with retained support | reconcile refuses with `upgrade_reconcile_mismatch` naming the days; after `--acknowledge` it marks them as `derived_rollup_lost` |
| fault: cleared derived rollup without support | `upgrade_reconcile_target_incomplete`, unwaivable even with `--acknowledge` |
| fault: dropped table on the target | `upgrade_reconcile_target_incomplete` naming the missing table |
| fault: natively busy source | `upgrade_source_busy` from `flock` held on `usage.sqlite.manager.lock`; the **write path** (`upgrade run` against a real v2 store) refuses, the store stays byte-identical and the recorded stage does not move — a preflight on a directory the command never receives is not the proof |
| rollback | plan → the plan's named volume is created as a Docker local volume **bound to the 1.x directory itself**, and a probe write through it is observed on that host directory → real compose apply of the printed document on the plan's port → the restarted 1.x accepts a new row (the actual HTTP status is recorded, not a constant) → probe of the manager's own listener refused with `upgrade_rollback_probe_self` and the recorded row's `stage`/`updated_at_ms` unchanged → `--verify --legacy` points at the **same directory**, and its observed fingerprint differs from the plan's pre-rollback fingerprint and equals a product fingerprint of that live directory taken just before and just after the verdict (a still-serving 1.x may touch its WAL index) → after the stack is released, the persisted `rolled_back` root is restarted and admits only the Upgrade surface, and the written row is counted in that same directory from the host |
| admission, unfinished | with the recorded row at `copy_in` and at `reconcile` the manager answers `/health` 503 and serves both wizard reads on the fixed Bootstrap carrier: 200 to the 1.x administrator key only (status shows the recorded stage and `nextAction`), 401 to no credential or a foreign one, and 405 with `Allow: GET` only after the key is proved; the canonical continuation stays 404 because a copied root carries no Fresh record, and the console (`/management.html`), the key/pricing API (`/v0/management/model-prices`), the gateway/model route (`/v0/management/models`) and `/usage-service/info` all answer 404 |
| admission, mountless | with the source mount absent, the same two roots still answer `status` 200 from the Manager store — `evidence: operation_row`, the recorded stage and a typed `sourceRoot: upgrade_legacy_root_missing` — to the key the mount accepted, while the v2 Admin key stays 401 and the preflight keeps its typed 503 with the same code |
| admission, refused mount | with the source mounted but its `usage.sqlite` a symbolic link — a file the shared database file policy refuses on both the mount lookup and the credential read — the same two roots keep answering `status` 200 from the Manager store with the recorded stage and a typed `sourceRoot: upgrade_source_symlinked` to the key the mount accepted, the v2 Admin key stays 401, and the preflight still inspects the mount the operator supplied: 200, `ready: false`, `source_ownership` blocked with `upgrade_source_symlinked`. Before the fix this state answered 503 on the entry page and both reads, so the operator could not reach the page that names the condition |
| origin rule, both reads | on both surfaces a cross-site `Origin` is 403 `origin_rejected` and `Sec-Fetch-Site: cross-site` is 403 (the surface's code on the fixed carrier, the transport's fetch-metadata refusal on the Fresh admin surface), this very origin is served 200, and an anonymous cross-site caller is still 401 first, because the credential's shape is decided from the request alone before the origin verdict |
| admission, complete | the terminal `complete` row admits the full writable surface (`/health` 200) and both reads answer 404 to every caller, the source key included |
| admission, `rolled_back` | after the verdict the manager is restarted on that root: `/health` is 503, the wizard `status` answers 200 to the source key with `stage` and `nextAction` `rolled_back`, the v2 Admin key stays 401, and the console, key/pricing, gateway/model and `/usage-service/info` routes all answer 404 |

A **refusal** case asserts that the refused command wrote nothing: the target
root is byte-identical before and after (`targetRootUnchanged: true`). A
**successful** case is the opposite: the interrupted copy-in resumes and does
write its target (`targetRootUnchanged: false`, recorded code `resumed_copy_in`),
which is what a resume is supposed to do. The two are separate claims and are
recorded separately.

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
4. **Wizard surface on a refused mount** (`internal/service/upgrade/preflight.go:397`,
   `MountDatabase`). The mount lookup counted a mount's `usage.sqlite` with
   `os.Stat`, which follows a symbolic link, so the mount looked like a source;
   the credential read then refused the linked file under the shared database
   file policy, and the entry page and both reads answered 503 — the operator
   could not reach the page that names the condition. The mount question now asks
   the credential read's own inspection, so a mount the policy refuses holds no
   database for the surface: the credential resolves to the Manager store, the
   surface stays reachable, and `status` and `preflight` both report
   `upgrade_source_symlinked` for that mount. Regression:
   `TestStartupUpgradeSurfaceAcceptsOneCredentialEverywhere`.

## Gaps the harness records but cannot close inside this slice

* **Same-volume rollback needs a bindable local volume.** The proof that the
  restarted 1.x writes where `--verify` reads uses a Docker local volume created
  with `--opt o=bind --opt device=<1.x directory>`. The harness proves the binding
  with a probe write observed on the host before it prepares the plan; if a host's
  Docker cannot bind that directory (or the tag cannot be resolved), it fails
  there instead of recording a verdict against a different directory.

* **Source release pin.** The contract pins `v1.14.10`
  (`internal/domain/bootstrap/contract.go:12`,
  `a67ed8834a914ab24c36d2490fa6bb6d4166724c`), so the harness runs the entire
  chain — preflight, copy-in, reconcile, import, External connect, verify,
  wizard, admission, fault injection and rollback — directly against the
  `v1.14.10` fixture it builds and seeds. The evidence says so
  (`mode.sourceFixture: "same"`, `mode.acceptedRelease: "v1.14.10"`); there is
  no second fixture and no recorded version gap. The accepted release is still
  read from the product's own preflight report rather than duplicated here, and
  the gap branch that records a typed `upgrade_source_version_unsupported`
  refusal stays as the guard for the next re-pin. The historical `v1.14.4`
  candidate survives only for the Phase 4-00 / Phase 4A fixtures
  (`bootstrap.ExactV1144Candidate()`), which still read the old pin.
* **Completion transition.** No step in this milestone writes the terminal
  `complete` row, so the completed-admission case writes that stage onto the row
  the real copy-in recorded. The evidence admission demands — the
  `upgraded-legacy` kind, both source fingerprints and the backup manifest digest
  is the copy-in's own; only the stage token is the harness's. The evidence
  says so (`admission.completionInput`).
* **Mount lookup bound.** The wizard reads decide the credential's shape and the
  browser's origin from the request alone, so the anonymous and cross-site cases
  of the origin-rule row above never reach the operator's mount, and a caller
  that does present a credential shape pays one lookup — under
  `MountLookupTimeout` (2s) — shared by the authentication and the `sourceRoot`
  marker, with at most one lookup per handler in flight. A lookup that outlives
  its bound answers `upgrade_mount_timeout` — a retry, not a database the
  operator has to repair — and a poll that arrives while a hung lookup is running
  joins that lookup instead of starting another. The harness cannot reach the
  deadline or the one-in-flight state: the mount it supplies is a local
  directory, and a filesystem whose `stat` blocks needs a hung network or FUSE
  mount this harness may not create. Both are proved by
  `internal/http/controller/upgradewizard/handler_test.go`
  `TestMountLookupRunsOncePerRequestInsideItsBound`,
  `TestMountDeadlineIsATimeoutNotAnUnreadableSource` and
  `TestMountIsInspectedOnlyAfterTheCredentialAndOriginGates`, which substitute
  the lookup for one handler; the harness proves the gate order it can observe
  (anonymous 401 and cross-site 403 on both reads) and the codes a local mount
  does reach — absent (`upgrade_legacy_root_missing`) and refused by the shared
  database file policy (`upgrade_source_symlinked`).
