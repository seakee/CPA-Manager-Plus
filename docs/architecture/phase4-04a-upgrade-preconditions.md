# Phase4-04A Upgrade source and backup preconditions

Product baseline: `v2@fca97ed253be8c87659278ae06e5ec7a35dd0154`.
Governing Plan: [cpamp-plan PR #24](https://github.com/seakee/cpamp-plan/pull/24),
merged; task and local prompt read from Plan main
`8c52e55657c40cccc8f524680055a21ea88fd07a`.

## Existing assets and bounded changes

Paths below are relative to `apps/manager-server/`.

| Asset | Reuse and actual gap |
| --- | --- |
| `internal/service/legacypreflight/service.go`: `inspect`, `AdmitStartup` | Reuse read-before-write classification. Startup rejects legacy sources on the schema alone; the recorded Upgrade operation is the only additional admission input (U1-2 below). |
| `internal/service/legacyprofile/profile.go`: `Classify` | Reuse exact known capability shapes. Shape is never historical release provenance. |
| `internal/repository/sqlite/legacy_inspection.go`: `InspectLegacySource` | Reuse immutable cold read or private WAL recovery copy. Add an administrator-protected entry using the same reader before schema/history inspection. |
| `internal/repository/setting`: `LoadAdminCredential`; `internal/security`: `VerifyAdminKey` | Reuse the existing salt/HMAC credential and verification. No writable Store, Session substitution, CPA credential fallback, administrator initialization or reset. |
| `internal/processlock`: `AcquireExisting`, `Validate` | Hold one canonical source fence through authentication, inventory, expensive validation and final rechecks. No new marker; cooperating Manager writers are excluded. |
| `internal/service/maintenancebundle`: `Inspect`, `Verify`, `ValidateMatchingEvidence`, `RevalidateSource` | Validate the existing bundle and exact current source revision. Preserve DB/WAL/SHM/journal/key/config/archive presence and full-file digests; reuse SQLite/key/config/archive validation and private production-open checks. No second snapshot format or publication journal. |
| `internal/service/maintenancebundle`: `Create`, `RecoverPublication`, `RestoreNew` | Keep existing host maintenance owners and recovery semantics. The new check does not create, recover, restore or modify any bundle. |
| `internal/domain/bootstrap`: `UpgradeAdmission`, `ExactV1144Candidate` | Return existing contract-compatible shape/candidate/unverified/blocked evidence. No new public status or persisted operation. |
| `internal/service/legacyartifact` | Keep artifact-only verification, unqualified producer/validator hard stops and diagnostic-only cleanup unchanged. No `ExecuteHop`/`ValidateHop` call. |
| Phase4-01 discovery, Phase4-02 transport, Phase4-03 Fresh | No public surface change. No Upgrade authority on fixed Fresh carrier; no Source/Backup inventory in unauthenticated discovery. |

## Host check

An authorized host operator first obtains a bundle through the existing
maintenance procedure. Creation requires the existing Manager marker, held
canonical lock and actual writer isolation; the command's `--source-stopped`
assertion alone is not proof of isolation. Do not create a snapshot from an
uncontrolled live source. This slice adds only the check:

```sh
cpa-manager-plus legacy-preflight --upgrade-check \
  --db /private/source/usage.sqlite \
  --data-key /private/source/data.key \
  --config /private/source/config.json \
  --archives /private/source/usage-archives \
  --source-stopped --backup /private/backups/bundle \
  --expected-source-revision EXPECTED_SOURCE_REVISION --timeout 2m \
  < /private/operator/existing-admin-key
```

The key comes only from stdin (bounded to 4096 bytes, EOF required), never an
argument, output, persisted secret, HTTP request to CPA/Gateway or Fresh carrier.
The host must be authorized to read the selected source paths. Failed key
verification emits no schema/history/file inventory; missing/expired browser
Sessions never trigger Fresh. Lost/unverifiable keys require the existing
stopped-host recovery procedure; this command performs no recovery.

The expected revision is the existing bundle's deterministic content revision,
not a claimed release/version or a source-owner credential. The check compares
it to the entire source file set, verifies the bundle inventory, rejects recovered
or different-source snapshots, runs matching evidence validation, and rechecks
both the bundle and source under the same held lock. Optional absent archive
roots remain explicit; persisted segment/run references still require their
actual files. Config and `data.key` are required. Environment-only keys must be
handled by a separately authorized owner; this check never materializes a key.

`upgradePreconditions.backup` is returned only after all content checks and
final rechecks succeed. It reports integrity and matching evidence, including
SQLite quick-check/foreign keys, encrypted connection/key matching where such
fields exist, config JSON and DB-referenced archive file hashes/size/contracts.
The maintained validator may open/migrate a discarded private scratch copy to
check current-reader restorability; it never opens the source or backup for
migration and publishes no migrated data. A source with no encrypted settings
cannot establish key correspondence merely because its key is well-formed.

This is a read-only observation, not a transferable authorization/checkpoint.
There is no new OperationID or operation owner: the canonical source lock owns
this check's lifetime. A later write owner must establish its own operation,
authority and source revision again; JSON replay cannot retain this fence.

## Honest admission result and stop conditions

**Every current production invocation returns BLOCKED/nonzero**, even when
backup content checks succeed. The exact candidate remains only
`v1.14.4@166262365d506157b99968105c890f480cc7c07c`. There is no production allowlist.
No caller-supplied version, same schema, immutable writer archive or fabricated
evidence reference can promote a particular source database to that identity.
The implementation deliberately has no input to inject such a promotion.

| Obligation / reason | Current result |
| --- | --- |
| `exact-source-provenance-unverified` | No qualified production producer binds the particular database to exact v1.14.4. SchemaC and untrusted v1.14.7/version labels remain unverified. |
| `external-writer-isolation-unverified` | Manager fence excludes cooperating writers only; absence of arbitrary external writers is not established. Matching hashes and operator stop assertions do not establish quiescence. |
| `qualified-forward-hop-required` | SchemaA/B sources are blocked before backup validation; no automatic older-source hop or downgrade. |
| `retained-history-coverage-unverified` | Any observed raw-deleted coverage remains blocked. Existing bundle matching verifies file/row contracts, not full retained projection/pricing/coverage semantics. No reconstruction or current-pricing fallback. |
| Missing/invalid Admin, lock, profile, expected revision, config/key, bundle/WAL/archive or corrupt/foreign backup | Explicit bounded failure reason; no weakened check or partial positive backup result. |
| Interrupted, incomplete/unpublished or changed source/bundle | Blocked; no source mutation and no implicit retry/recovery. Existing `recover-publication` reconciles the original maintenance operation separately, then the complete authenticated check must run again. |

`UpgradeAuthorized=false`, `TrustedHopReceipt=false`, admission
`EvidenceStatus=SourceUnverified`, `Readiness=BridgeBlocked` throughout.
Successful content checks do not clear the conservative preflight Unknown or
Bridge obligations or turn `AdmitStartup` into an Upgrade writer.

## Evidence and verification

The new service tests use the existing independently sourced v1 DDL fixtures,
a stationary real SQLite WAL captured to a cold test source, and a private copy
of the retained BAK-02 real v1 archive bundle. They check successful content
validation without admission, key failures, unknown/older/conflicting shapes,
lock conflict/absence, cancellation, mismatched revisions, missing key/config,
corrupt/unpublished/foreign bundles, WAL omission, archive omission even after
recomputed integrity metadata, and raw-deleted uncertainty. All branches compare
the complete source bytes and metadata before/after. Command tests cover the
stdin credential path, bounded input, no-secret output, no-inventory denial and
no Fresh/Upgrade fallback.

Existing bundle tests remain the fault/recovery authority: real subprocess crash
and restart, uncertain publication, interrupted journals, cancellation and disk
failure, source mutation, wrong keys, SQLite corruption and archive matching.
No new crash harness, recovery journal, or full Phase4A-07 49-case run.

Verification results are recorded in the product PR and delivery report.
Local test/build artifacts remain under task-owned `bin/tmp/phase4-04a/`.
These bounded fixtures and local tests do not qualify a production source,
Docker/native deployment, real v1-to-v2 migration or recovery.

### Phase4-04B1: published archive content

Matching evidence now calls the maintained usage archive file reader through
`usage.ValidatePublishedArchiveFiles`, with persisted repository rows and no
Store, worker, or lifecycle transition. It checks bounded manifest JSON,
gzip/JSONL restore envelopes, canonical event hashes, ordering, counts, byte
totals, time ranges and segment/run digests. Existing path and private-file
checks apply. Cancellation is observed during segment checksum and decoding.
Required/present publication metadata selects decoding; legal unpublished,
failed/resumable and cancelled snapshots retain their existing lifecycle rules.
Cancelled snapshots without segment rows check the retained manifest and run
metadata only; they do not establish complete payload evidence.

Focused tests use private byte-identical copies of fixed BAK-02 and optionally
the pinned materialized RET-07 bundle (`CPAMP_TEST_RET07_BUNDLE`). Git does not
preserve archive 0600 modes; only disposable copies receive private modes.
Corruption cases recompute outer bundle, manifest and segment digests before
asserting decoded-content rejection and unchanged source/backup bytes.

Payload validity does not qualify historical completeness, exact database
writer provenance, noncooperating writer exclusion/continuity or retained
pricing/coverage parity. All three production NO-GOs and
`UpgradeAuthorized=false`, `TrustedHopReceipt=false` remain unchanged.

## 04A / 04B boundary and scope check

04A supplies an authenticated source/backup observation and explicit stop
reasons. It does not make a source admissible when provenance, isolation or
retained history is unproved. Those unresolved qualifications must be addressed
by an independently accepted source/profile consumer before any production
Upgrade, without treating this PR as an acceptance decision.

04B requires separate Plan scope and independent acceptance for the real
write/hop/import owner, operation/checkpoints, migration parity, conditional
recovery/rollback and route cutover. None is implemented or activated here.
CPA lifecycle takeover, Gateway activation and Phase4-05/06 UI remain stopped.

Over-design / over-defense / scope verdict: **PASS for this bounded change,
pending independent review**. One existing command/report and one maintained
inspection path; existing locks, credential, bundle, candidate and admission
contracts. No generic migration/auth/backup engine, extra journal/proof ledger,
new public status, speculative all-version support or additional UI.

## U1-1a: Upgrade operation row and read-only preflight

Added after the 04A slice. `UpgradeAuthorized` and `TrustedHopReceipt` stay
hard `false`; the new surface grants no migration authority.

- **Operation state.** `upgrade_operations` (single row, `id = 1`) stores the
  stage, the completed domains, the source kind/version and before/after
  fingerprints, the backup manifest reference, reconciliation output and manual
  items. `internal/repository/upgradeoperation.Advance` appends a domain and
  moves the stage in one transaction; a backwards move is rejected and leaves
  the row untouched. `complete` never moves again, `failed` parks the operation
  without dropping completed domains. The table is created by the v2 migration
  in the Manager database, never in the legacy source root.
- **Preflight** (`internal/service/upgrade.Preflight`) is read-only. It holds
  the existing source fence (`processlock.AcquireExisting`), opens
  `usage.sqlite` with `mode=ro` plus `query_only` and runs `quick_check`, and
  writes nothing — no WAL, no migration, no journal.
- **Checks, in order:** `source_ownership` (`upgrade_source_busy`,
  `upgrade_source_unreadable`), `source_version`
  (`upgrade_source_version_unsupported`), `fingerprint`, `disk`
  (`upgrade_disk_insufficient`), `cpa_classification`, `parity`. Parity gaps
  (missing/mismatched projection, past watermark, per-day deleted coverage
  disagreement) never block; they are reported for a later explicit decision.
- **Version source.** 1.x never persisted its release, so the release is a
  floor derived from schema and settings markers and compared with
  `bootstrap.PlannedDirectRelease`. A marker introduced after the pinned release
  is a mismatch. Re-pinning to v1.14.10 is U1-1b, not this slice.
- **CLI.** `cpa-manager-plus upgrade preflight --legacy <dir> [--json]` prints
  the report and exits 0 (ready), 1 (blocked) or 2 (usage/environment).
  `cpa-manager-plus upgrade status [--json] [--db-path PATH]` reads the
  operation row read-only, so both work on non-writable volumes.
- **Deferred:** v1.14.10 re-pin and its qualification evidence (U1-1b),
  reconciliation execution and CPA import (U1-3) and route cutover/rollback
  (U1-3).

## U1-2: copy-in and the two-stage admission gate

`internal/service/upgrade.CopyIn` is the only Upgrade step that writes, and it
writes only inside the v2 data root. `UpgradeAuthorized` and `TrustedHopReceipt`
stay hard `false`; the recorded operation grants startup surface, never
migration authority.

- **Pipeline, fixed order:** fingerprint the 1.x source (`source_fingerprint_before`)
  → `maintenancebundle.Create` into the backup destination → `Verify` → validate
  the manifest digest → `RestoreNew` into `.upgrade-copy-in/restored` inside the
  v2 data root → `os.Rename` each restored member into the v2 layout
  (`database`→`usage.sqlite`, its WAL/SHM/journal, `data-key`→`data.key`,
  `usage-archives`→`usage-archives/`) → publish `.upgrade-copy-in/copy-in.json`
  → open the copied store, run the incremental v2 migration and commit `copy_in`
  → remove the work directory only after that commit. Everything is on one
  filesystem, so no member is ever copied.
- **Crash window.** The migration rewrites the copied database in place, so a
  process death between the migration commit and the operation row leaves a
  store whose schema says `native-v2` and whose row does not exist. The marker
  (`internal/service/upgrade/copyinmarker`) closes that window: it is written
  before the migration starts, names the target database, both source
  fingerprints and the backup manifest digest, and is removed only once the row
  commits. Both the admission gate and the copy-in resume read it, so such a
  store is `in_progress` — never a finished install — and a resume adopts the
  migrated database instead of refusing to replace it.
- **Source immutability.** The 1.x volume is opened read-only under the held
  `processlock` fence; nothing is created, renamed or unlinked beside it. The
  fingerprint covers the database, its WAL/SHM/journal, `data.key`, every
  archive file and every other entry of the source root (the ownership marker is
  the one deliberate exception: it belongs to the holder, not the source). It is
  recomputed after the copy; a difference rolls back the members this run moved
  plus the work directory and aborts with `upgrade_source_changed`.
- **Resumability.** A verified bundle is reused instead of recreated. A target
  whose members already match the manifest is not restored again. A published
  staging tree that still describes the backup is adopted rather than restored
  twice. A committed `copy_in` (or later stage) is verified against the v2
  layout before it is reported: the data key and the archive tree must still
  match the backup, the archive tree must hold nothing the backup does not
  describe, and the database must be the migrated store — otherwise the run
  fails with `upgrade_target_modified` instead of a false success. A target that
  holds *different* bytes under a member name is never replaced.
- **Recorded fields** in `upgrade_operations`: `stage=copy_in`,
  `domains_done+=[source_copy]`, `source_kind=upgraded-legacy`,
  `source_version` (release floor read from the backup copy),
  `source_fingerprint_before`, `source_fingerprint_after` and
  `backup_manifest_sha256` (digest of the published `manifest.json`). The stage
  only moves forward; a retry after a later stage keeps that later stage.
- **CLI.** `cpa-manager-plus upgrade run --legacy <dir> [--target <dir>]
  [--bundle <dir>] [--json]` runs the preflight gate first (a blocked source
  writes nothing), then the copy-in, and prints the same `Result` that the
  record holds. `upgrade preflight` and `upgrade status` remain read-only.

### Admission surface table

`legacypreflight.AdmitStartup` reads the injectable
`UpgradeAdmission` (`DatabaseUpgradeAdmission` in production) before it
classifies a source. The reader resolves the `upgrade_operations` row first and,
when the row is missing, the `.upgrade-copy-in/copy-in.json` marker beside the
database. A reader error fails closed; a `nil` reader keeps the historical rule.
`Surface()` then selects what the process serves:

| Recorded state | Admission | Startup surface |
| --- | --- | --- |
| no table, no row and no marker for this database (or `nil` reader) | `none` | unchanged: only `fresh` and `native-v2` are admitted; a 1.x source stays blocked |
| copy-in marker, no row (the crash window: the migration ran, the row did not) | `in_progress` | Upgrade surface only |
| `preflight`, `copy_in`, `reconcile`, `cpa_import`, `external_connect`, `verify`, `failed` | `in_progress` | Upgrade surface only: fixed Bootstrap discovery status reads, the read-only shell and the Upgrade authentication leaf. Console, Key workspace, gateway configuration, lifecycle and model traffic answer 404; health stays 503 and no writable subsystem starts |
| `rolled_back` | `in_progress` + reason `upgrade_rolled_back` | Upgrade surface only, same as any unfinished operation. The reason is the typed string the console shows; it is never a completion |
| `complete` with `source_kind=upgraded-legacy`, both source fingerprints and the backup manifest digest | `complete` | normal v2 surfaces, as for a native store |
| `complete` missing any of those observations | — | not admitted (fail closed) |
| reader error, unreadable marker or unsupported stage | — | not admitted (fail closed) |

A record may relabel a source only when a copy-in could have produced it: the
copy-in always runs the incremental migration before it records anything, so the
classified kind of a copied-in store is `native-v2`. `legacy-manager` is
therefore never relabelled — a 1.x schema with a row (complete or not) stays
blocked and is never admitted to the full surface.


## U1-6: rollback to 1.x and the `upgrade run` boundary

Added after the copy-in slice. A rollback is modelled, never executed by the
Manager: the process runs inside a container and must not drive the Docker
socket, so it *prepares* an exact invocation and *records* a proven verdict.

- **`rolled_back` is a terminal stage.** `model.UpgradeStageRolledBack` is
  valid and rank 8, but `CanAdvanceTo` refuses every successor and refuses to
  leave it, including a relabel to `failed`. It is also the one move a
  `complete` row may still make: a rollback abandons the forward order rather
  than extending it, so the completion barrier does not apply to it.
- **`PrepareRollback`** (`internal/service/upgrade/rollback.go`) requires a
  recorded operation in `copy_in|reconcile|cpa_import|external_connect|verify|complete`
  (`upgrade_rollback_not_applicable` otherwise), binds the directory under
  decision to the source root the copy-in recorded
  (`upgrade_rollback_volume_unbound`, because a plan may not claim "the original
  volume" it cannot check), rejects roots that contain each other exactly as
  copy-in does (`upgrade_rollback_roots_overlap`), recomputes the 1.x source
  fingerprint and refuses a difference (`upgrade_source_changed`), refuses a row
  without both fingerprints or the backup manifest digest
  (`upgrade_rollback_unproven`), and pins the 1.x image to
  `seakee/cpa-manager-plus:v<source_version>`. A recorded version that is not an
  exact release, or an explicit reference that names no version — `:latest`, a
  bare repository, an empty tag, a partial version or a `@sha256:` digest — is
  `upgrade_rollback_image_unpinned`; an explicit release outside the recorded
  source's version family is `upgrade_rollback_image_family` unless the operator
  passes the family override. Refusals write nothing at all.
- **The plan** (`<v2 data root>/.upgrade-rollback/plan.json`, canonical-JSON
  SHA-256 in `digest`) names the pinned image, the volume derived from the
  compose project and the mountpoint the 1.x container uses, the recorded source
  root, the exact `docker compose -p cpamp -f - up -d cpa-manager-plus`
  invocation, the 1.x compose file to feed on stdin, the preconditions (including
  the volume mountpoint check) and the health proof: `GET /health` on 18317 with
  the documented `{"ok":true,"service":"cpa-manager-plus"}` identity, plus a
  read-only SQLite open. `/health` is the only unauthenticated 1.x endpoint —
  `/status` requires the admin key and answers 401 without it, so it can never
  prove a volume. It is evidence for the operator, not an authorization, and it
  is written only under the v2 data root.
- **`RecordRollback`** takes the operator's observation (roots, backup, image,
  health URL, time) and derives the proof itself in one call: it probes the
  health endpoint and requires the documented identity (a bare 2xx or another
  service is `upgrade_rollback_identity_foreign`, a probe that does not address
  `/health` is `upgrade_rollback_probe_endpoint`); it opens the live 1.x
  database immutable read-only with a `quick_check`; it reads the recorded
  backup snapshot read-only, re-verifies its manifest digest, and requires the
  live volume to still retain at least the rows the backup holds — raw usage
  events counted together with the append-only archive projection — and every
  archived segment and the data key the backup describes
  (`upgrade_rollback_identity_foreign` on any difference,
  `upgrade_rollback_unproven` when a store cannot be opened). It writes the
  stage and the evidence (image, health endpoint, observed fingerprint,
  observation time) in one transaction through
  `upgradeoperation.RecordRollback`, which never rewrites `source_kind`,
  `source_root`, both fingerprints, `backup_manifest_sha256` or the completed
  domains; a second verdict is `upgrade_rollback_not_applicable`. An observation
  older than the freshness window is `upgrade_rollback_evidence_stale`. The
  observed fingerprint is informational: the restarted 1.x manager keeps writing
  rows, so byte equality with the recorded before-fingerprint is neither
  required nor recorded as one.
- **CLI.** `upgrade rollback --prepare --legacy <dir> [--legacy-image <ref>]
  [--project <name>] [--allow-image-family-mismatch] [--json]` prints the plan
  and exits 0, or 1 with the typed refusal written to stderr. `upgrade rollback
  --verify --legacy <dir> [--backup <dir>] [--status-url <url>]
  [--legacy-image <ref>] [--json]` passes the observation to the service, which
  probes the default `http://127.0.0.1:18317/health` unless a URL is given, then
  records; unproven evidence exits 1, usage errors 2.
- **`upgrade run` stops at the copy-in.** It performs preflight plus copy-in,
  never reconciliation, and records the 1.x data directory it read so a later
  rollback can bind its volume. It prints the exact
  `upgrade reconcile --legacy … --target …` command (with `--bundle` only when
  the operator passed one). A recorded stage beyond `copy_in` — including a
  terminal `rolled_back` — is reported as-is with no write and no reconciliation
  hint, and a blocked preflight still writes nothing.
- **The 1.x volume is never written** by any path above: both the health probe
  and the database probe are read-only, the database is opened immutable so not
  even a WAL index appears beside it, and the source manifest is unchanged by
  prepare, verify and every refusal.
