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
| `internal/processlock`: `AcquireExisting`, `AcquireExistingReadOnly`, `Validate` | Hold one canonical source fence through authentication, inventory, expensive validation and final rechecks. `AcquireExistingReadOnly` takes the same marker through a read-only descriptor, so a stopped source preserved on a read-only mount still proves exclusivity; it is used by the U1-1a read-only preflight and grants nothing here. No new marker; cooperating Manager writers are excluded. |
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
  row untouched. `Put` is monotonic too: it may only advance the stage and
  append domains, and `updated_at_ms` never moves backwards — an older
  caller-supplied value (or a clock that went backwards) is replaced, so a retry
  cannot make recorded work look uncompleted or the row look older than the
  write it replaced.
  `complete` never moves again, `failed` parks the operation without dropping
  completed domains. The table is created by the v2 migration in the Manager
  database, never in the legacy source root.
- **Never in place.** Every v1.14.x release sets `pragma journal_mode = WAL`, and
  a plain `mode=ro` open of a WAL database creates `usage.sqlite-wal` and
  `usage.sqlite-shm` next to it. Preflight therefore copies `usage.sqlite` and
  its sidecars into a private scratch directory (`OpenSnapshot`), digests the
  source files before the copy, again after it, and the copy as it is written —
  any difference blocks with `upgrade_source_changed` — then opens only the copy
  with `mode=ro` plus `query_only` and removes the scratch directory. The source
  fence is taken through a read-only descriptor
  (`processlock.AcquireExistingReadOnly`), so a stopped source preserved on a
  read-only mount still proves exclusivity. `upgrade status` uses the same
  snapshot for the Manager database, so it works when the Manager volume is not
  writable.
- **Checks, in order:** `source_ownership` (`upgrade_source_busy`,
  `upgrade_source_unreadable`, `upgrade_source_changed`,
  `upgrade_snapshot_failed`), `source_version`
  (`upgrade_source_version_undeclared`, `upgrade_source_version_unsupported`,
  `upgrade_source_already_v2`), `fingerprint`, `disk`
  (`upgrade_disk_insufficient`, `upgrade_disk_probe_failed`),
  `cpa_classification`, `parity`. Parity gaps (missing/mismatched/stale
  projection, past watermark, per-day deleted coverage disagreement) never
  block; they are reported for a later explicit decision. A failed free-space
  probe blocks: an unmeasured target volume is never assumed to be big enough,
  and the probe measures the volume that will receive the copy, not the source.
- **Version source.** No 1.x release persisted its release string, and the
  tables do not discriminate: v1.14.0 through v1.14.10 create the same schema.
  The only per-release values v1 write are derived-rollup format revisions in
  `settings`, and each value belongs to a range of releases, never to one:
  `usage_cache_accounting_semantics_revision` is `2` in v1.14.4 and v1.14.5 and
  `3` from v1.14.6 on. A settings value is therefore a floor at release
  granularity, never an identity, so preflight never derives a release from it.
  The operator declares the release (`--source-version`); preflight accepts only
  exact equality with `bootstrap.PlannedDirectRelease` and blocks anything else
  with both values.
- **Newer markers.** A declaration is a claim, so it is not the last word on the
  newer side either. A source that carries a marker the pinned release genuinely
  lacks has already moved past the pin, and blocks with
  `upgrade_source_version_unsupported` even when the declaration matches. The
  detail reads `source is newer than the pinned release <pin>` and names every
  carried marker with the release that introduced it, e.g.
  `usage_cache_accounting_semantics_revision=3 (v1.14.6)`; it no longer repeats
  the matching `expected`/`declared` pair next to the refusal. For a v1.14.4 pin
  those markers are the revision v1.14.6 raised to `3` (v1.14.4 and v1.14.5
  write `2`) and the `idx_usage_events_ts_latency_ttft_cover` index v1.14.7
  added. The revision is compared as a floor, not for equality: v1 only ever
  raises it, so any value at or above `3` is evidence of v1.14.6 or later and a
  value of `4` is refused rather than silently accepted. The floor is a literal
  that names its release, not
  `datamigration.CurrentUsageCacheAccountingSemanticsRevision`: that constant
  tracks this build, and a future v2 bump would stop matching a 1.14.6–1.14.10
  source and let it through. The list is keyed by the release that introduced
  each marker, so a later re-pin drops the markers its own schema accepts
  instead of refusing its own source. No base table is a later marker: every
  1.14.x release creates `usage_archive_deleted_coverage_daily`, the pinned one
  included, so reading it as a v1.14.6 marker would refuse the pin's own source —
  the defect the real-Docker harness found.
- **What each marker can prove.** The two markers are not equally strong. The
  settings revision is written by the v1.14.6 migration itself, so a 1.14.6+
  source that ran it carries the value: present is reliable positive evidence,
  and absent is compatible with a pinned source (v1.14.4/1.14.5 write `2`, and an
  install stopped before the migration wrote anything has no row at all). The
  index is weaker. v1.14.7 creates it through the deferred/offline derived-index
  lifecycle (`internal/repository/sqlite/derived_cleanup.go` at tag v1.14.7):
  post-listen maintenance creates an index only when its target table is empty,
  and on a non-empty `usage_events` it records a deferral and leaves the index to
  the offline `cleanup-derived` command. The v1.14.7 release notes — "Upgrade
  Notes" in `docs/release-notes/v1.14.7-en.md` at tag v1.14.7 — say the same:
  skipping that maintenance only postpones the index's performance benefit. A
  present index is therefore reliable positive evidence of v1.14.7 or later; an
  absent one is no evidence at all, because most upgraded installs never built
  it. The ladder keeps it for the positive direction only. The preflight fixture
  therefore carries the pin's own `usage_cache_accounting_semantics_revision=2`
  row, plus a case with the row absent, so neither a key-exists rule nor a plain
  `!= 3` comparison can pass the suite.
- **Limit of the gate.** The version check refuses on evidence; it does not
  prove identity. Sources that are not the pinned release are accepted, and two
  are enough to show it: v1.14.5, whose schema, settings values and structure
  are identical to v1.14.4 (it writes the same `2`), so nothing distinguishes it
  and accepting it is unavoidable; and a v1.14.7+ install that skipped the
  offline derived-index maintenance and was stopped before its migration stamped
  the revision `3`, so it carries neither evidence. That is the cost of a rule
  restricted to observations a source actually makes. Proving the exact release
  needs provenance — a writer archive or candidate commit — which this check
  does not claim and the 04A admission above keeps blocked.
- **Structural lower bound.** A declaration is a claim, so it is never the last
  word. The source must also carry the v1.14 structure: `settings`,
  `usage_events`, `usage_event_identity_ledger`, `usage_archive_event_refs`,
  `usage_monitoring_event_projection_v1`, `usage_monitoring_rollup_state` and
  `usage_archive_deleted_coverage_daily`. Missing any of them blocks with
  `upgrade_source_version_unsupported`; the check detail and
  `report.missingTables` name every missing table. This is a lower bound, not a
  discriminator — all v1.14 releases pass it, some v1.13 shapes do not — so a
  correct declaration cannot make a structurally older source ready. A source
  that already carries a v2-only table (`gateway_*`, `fresh_*`,
  `upgrade_operations`) blocks with `upgrade_source_already_v2`. Re-pinning to
  v1.14.10 is U1-1b, not this slice.
- **Parity trust.** The `projection_v1` watermark is trusted only when
  `schema_version = 1`, `structure_revision` equals
  `usageidentity.MonitoringProjectionStructureRevision()` and the status is set
  and not `clearing`. A same-timestamp projection row in any other state is
  reported as stale rather than healthy, because the state that produced it
  cannot vouch for it. Parity is *not applicable* — status `not-applicable`,
  `parityGaps.applicable=false` and a reason naming the table — when
  `usage_archive_event_refs` or `usage_archive_deleted_coverage_daily` is
  absent. Such a source has no retained history to compare; reporting zero gaps
  would read as a clean result.
- **Scratch placement.** `--scratch-dir` may not point inside the legacy source
  root or inside the target root (the Manager data directory): the snapshot
  copies the whole database, so either place writes data into a tree this
  operation must not touch or into the volume being upgraded. Both are refused
  before a directory is created, with `upgrade_snapshot_failed` and nothing
  written. An unset `--scratch-dir` is not an exemption: it means the OS
  temporary directory, and that default is checked against both roots the same
  way. A host whose `TMPDIR` points into the source tree or the Manager volume
  is refused before anything is created; otherwise the default would put the
  copy there, and a snapshot abandoned by a `SIGKILL` (no deferred cleanup runs)
  would stay on that volume for good. The scratch volume and the target volume
  each need room for the source bytes; the disk detail states both
  (`requiredBytes`, `scratchRequiredBytes`). An unset target is never measured as
  the working directory: the probe falls back to the scratch volume, and the CLI
  requires an explicit `--target-dir` when the Manager database path cannot be
  derived. The second whole-tree fingerprint is evidence too: if it cannot be
  taken after the copy, the report blocks with `upgrade_source_unreadable`
  instead of trusting the pre-copy value.
- **CLI.** `cpa-manager-plus upgrade preflight --legacy <dir> --source-version
  <release> [--target-dir DIR] [--scratch-dir DIR] [--json]` prints the report
  and exits 0 (ready), 1 (blocked) or 2 (usage/environment/retryable).
  `--target-dir` is required when the Manager database path cannot be derived;
  the command never substitutes the working directory. `cpa-manager-plus upgrade
  status [--json] [--db-path PATH] [--scratch-dir DIR]` reads the operation row
  from a private snapshot, so both work on non-writable volumes. That snapshot is
  a full copy of the Manager database and its sidecars: the read costs time and
  scratch space proportional to the database size, not to the single row it
  returns, so `status` is not a cheap poll and should not be looped tightly on a
  large database. While a Manager writes — an upgrade in progress is the normal
  case — the copy can race a commit and `status` may briefly report the
  retryable `upgrade_source_changed`; it retries a bounded number of times
  (8 attempts, exponential backoff capped at 200 ms) until a write gap appears,
  and only an exhausted budget is reported, with the same code and the retryable
  marker instead of a generic usage error. Preflight never retries: a legacy
  source that is still being written remains a stop condition.
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
  the manifest digest → check the target for members this run did not place →
  publish `.upgrade-copy-in/copy-in.json` in its `materializing` phase →
  `RestoreNew` into `.upgrade-copy-in/restored` inside the v2 data root →
  `os.Rename` each restored member into the v2 layout
  (`database`→`usage.sqlite`, its WAL/SHM/journal, `data-key`→`data.key`,
  `usage-archives`→`usage-archives/`) → republish the marker in its `migrating`
  phase, naming the database file it moved → open the copied store, run the
  incremental v2 migration and commit `copy_in` → remove the work directory,
  marker included, only after that commit. Everything is on one filesystem, so
  no member is ever copied.
- **Crash windows.** Two interruptions matter. Inside the move, some members are
  in the v2 layout and the rest are in the staging tree; the marker's
  `materializing` phase says so, and the resume continues the move from its own
  staging tree. Between the migration commit and the operation row, every member
  is in place and the database has already been rewritten; the marker's
  `migrating` phase says so, and the resume adopts the target instead of
  restoring over it. Both markers are removed only once the row commits.
- **Marker trust.** The marker always names its phase, the target database, the
  source kind, the source fingerprint the copy started from and the backup
  manifest digest. A `migrating` marker additionally names the device and inode
  (`copyinmarker.FileIdentity`, volume serial and file index on Windows) of the
  database this copy-in moved: a rename and the migration's in-place rewrite both
  keep that identity, and a database swapped in beside the marker does not. A
  marker that names a different database file or a different backup does not
  vouch for this target, and a `migrating` marker whose identity no longer
  matches — or whose data key, archive tree or archive contents no longer match
  the backup — fails closed with `upgrade_target_conflict`. Nothing is deleted,
  restored or moved before that decision: clearing the work directory would take
  the marker with it, and a migrated store with no row and no marker is admitted
  as a finished install. A marker that names the same database file under another
  absolute path is accepted, because a relocated mount changes the path, not the
  store.
- **Source immutability.** The 1.x volume is never opened in place. Preflight
  copies the database and its WAL sidecars into private scratch space, proves the
  source did not change while the copy was taken and reads only that copy, so a
  `-shm` is never created or rewritten beside a stopped source; the copy-in reads
  the source as bytes under the held `processlock` fence. The fingerprint covers
  the database, its WAL/SHM/journal, `data.key`, every archive file and every
  other entry of the source root (the ownership marker is the one deliberate
  exception: it belongs to the holder, not the source). It is recomputed after
  the copy; a difference rolls back the members this run moved plus the work
  directory and aborts with `upgrade_source_changed`.
- **Resumability.** A verified bundle is reused instead of recreated. A target
  whose members already match the manifest is not restored again. A published
  staging tree that still describes the backup is adopted rather than restored
  twice. The resume never opens the target database before the migration that
  owns it: a read-only open of a WAL database rewrites or creates its `-shm`,
  which would make the member the resume has to move disagree with the backup and
  leave a live-WAL source unresumable forever. A committed `copy_in` (or later
  stage) is verified against the v2 layout before it is reported: the data key
  and the archive tree must still match the backup, the archive tree must hold
  nothing the backup does not describe, and the database must be the migrated
  store — otherwise the run fails with `upgrade_target_modified` instead of a
  false success. A target that holds *different* bytes under a member name is
  never replaced. A rerun that finds its own marker after the row committed
  reports the recorded outcome and removes the marker.
- **Recorded fields** in `upgrade_operations`: `stage=copy_in`,
  `domains_done+=[source_copy]`, `source_kind=upgraded-legacy`,
  `source_version` (the operator's declaration; 1.x never recorded it, so an
  omitted declaration is recorded as `unknown` instead of guessed),
  `source_fingerprint_before`, `source_fingerprint_after` and
  `backup_manifest_sha256` (digest of the published `manifest.json`). The stage
  only moves forward; a retry after a later stage keeps that later stage. Every
  write goes through `upgradeoperation.Repository`, whose `nextUpdatedAtMS` keeps
  `updated_at_ms` monotonic; no caller proposes a timestamp.
- **CLI.** `cpa-manager-plus upgrade run --legacy <dir> --source-version
  <release> [--target <dir>] [--bundle <dir>] [--scratch-dir DIR] [--json]` runs
  the preflight gate first (a blocked source writes nothing), then the copy-in,
  and prints the same `Result` that the record holds. An explicit `--target`
  states the layout. Without it the Manager configuration is the default only
  when every member the copy-in writes is the member the service will read: the
  database must be the canonical `usage.sqlite`, `dataKeyPath` and
  `usageArchiveDir` must resolve to `data.key` and `usage-archives/` inside it,
  and no inline environment key may override the copied key. Anything else is
  refused with `upgrade_target_layout_mismatch`, because a copied data key the
  service never loads leaves the 1.x encrypted fields undecryptable.
  `upgrade preflight` and `upgrade status` remain read-only.

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
| copy-in marker, no row (the crash windows: the members moved, the row did not) | `in_progress` | Upgrade surface only |
| the same marker under another absolute path, or a root relocated to another mount | `in_progress` | Upgrade surface only: the marker belongs to the data root it sits in, so a relocated volume is never read as "no record" |
| a marker naming a different database file | — | not admitted (fail closed): it is evidence for another run |
| `preflight`, `copy_in`, `reconcile`, `cpa_import`, `external_connect`, `verify`, `failed` | `in_progress` | Upgrade surface only: fixed Bootstrap discovery status reads, the read-only shell and the Upgrade authentication leaf. Console, Key workspace, gateway configuration, lifecycle and model traffic answer 404; health stays 503 and no writable subsystem starts |
| `complete` with `source_kind=upgraded-legacy`, both source fingerprints and the backup manifest digest | `complete` | normal v2 surfaces, as for a native store |
| `complete` missing any of those observations | — | not admitted (fail closed) |
| reader error, unreadable marker or unsupported stage | — | not admitted (fail closed) |

A record may relabel a source only when a copy-in could have produced it: the
copy-in always runs the incremental migration before it records anything, so the
classified kind of a copied-in store is `native-v2`. `legacy-manager` is
therefore never relabelled — a 1.x schema with a row (complete or not) stays
blocked and is never admitted to the full surface.


## U1-3: four-domain reconciliation and permanent incomplete marking

`upgrade reconcile` compares the copied v2 store against the stopped 1.x source
it was copied from and permanently marks the UTC days that can never be
completed again. It is the only Upgrade step that reads both stores, and it
never re-runs the copy-in.

### The source is read through a copy

The source fingerprint recorded by the copy-in is re-checked first, before and
after the comparison; a source that moved fails with `upgrade_source_changed`
and nothing is compared. The database is read through the same private snapshot
preflight and status use (`upgrade.OpenSnapshot`): the database and its
`-wal`/`-shm` sidecars are copied into `<ScratchDir>/.upgrade-reconcile` and the
*snapshot* digests the source before and after taking the copy, so a source that
was written while it was being copied is refused instead of compared. Opening
the original, even read-only, would create a WAL index beside it and change the
fingerprint the copy-in recorded. The scratch area is
`<ScratchDir>/.upgrade-reconcile` (the v2 data root by default): only that
tool-owned subdirectory is created and removed, so the directory the caller
named is never the path the step deletes. The named root must not be, sit inside,
or contain the source root, and the copy is removed on every return path — a
clean comparison, a mismatch, or a failure before the comparison finished — so
nothing but the report survives the step.

The report carries no credential and no raw failure body, but it is not
row-free: a mismatch names the dimension that moved, which for a retained bucket
is the hour, model and `api_key_hash` and for a rollup is the row identity
(account, label, file, key hash, auth index, source hash, …), because the
operator has to be able to find the row. Those values already live in the same
v2 store the report is recorded in, and the key is a hash rather than a key.

### The four compared domains

| Domain | Query | Match rule |
| --- | --- | --- |
| `retained` | `usage_events` grouped by UTC hour, `model` and `coalesce(api_key_hash, '')`: `count(*)`, `sum(total_tokens)`, `sum(failed)` | every hour bucket and every per-key, per-model total equal |
| `pricing` | `usage_pricing_hourly_rollups_v1` per stored row (hour bucket and the whole identity, `failed` included): calls, every token class and cost | every row and every column equal; cost is recomputed per side |
| `coverage` | `usage_archive_deleted_coverage_daily`: per-day `deleted_event_count`, `min_timestamp_ms`, `max_timestamp_ms` | all three equal per day |
| `history` | `usage_monitoring_account_daily_rollups_v1` per stored row: the table's whole primary key (hour bucket, account and auth snapshots, provider, auth index and account id, source and source hash, file, key hash, executor, model, billing/pricing model, service tier, context threshold, `failed`) | calls, every token class, `total_tokens`, `last_seen_ms` and the latency aggregates equal per identity |

Each query groups by exactly the expression it selects, so one reported key is
one row: a NULL and an empty `api_key_hash` are the same "no key" and are summed
into one bucket instead of returning two groups that overwrite each other in the
cell map.

`pricing` and `history` are read only for the `structure_revision` the store's
own read path selects — the pricing revision the model price table defines, the
account-history revision the rollup contract names. Rows an older revision left
behind are not what the reader shows, so they are not compared as if they were;
a row moved to an inactive revision therefore shows up as the row the active
revision lost.

The pricing table stores no cost, so cost is recomputed on both sides with
`pricing.CostForModelCandidatesWithServiceTier`; a price change between the two
stores is therefore visible instead of invisible, and it is compared exactly: the
same inputs produce the same integer micro sum, so every difference is a
difference and no relative tolerance may hide one. Comparing at the granularity
each read path exposes is what makes a permutation inside a day visible instead
of cancelling out in a day sum: a bucket that moved an hour, two `failed` rows
trading their calls, a rewritten API key, provider or model, a history row whose
`failed` flag, auth index, account id, source hash or pricing model changed, or a
row that reallocated input tokens to output tokens inside an unchanged total, is
reported in the column that moved (`bucket_ms`, `calls`, `api_key_hash`,
`failed`, `auth_index`, `input_tokens`, `min_timestamp_ms`, …), on the row it
moved on. The read path exposes those columns, so a change a reader would show
must not reconcile clean.

Each side is read inside one read-only transaction. A day present on only one
side is reported as a `presence` mismatch, never as a skip, and `comparedDays`
counts only the days both sides hold. A domain both sides hold is `applicable`.
A domain the 1.x source holds rows of but the store cannot show blocks with the
typed `upgrade_reconcile_target_incomplete` naming the domain and the table,
because that is lost data rather than a measured disagreement. The one
exception is a derived rollup, and the boundary is what the table is derived
from:

- `pricing` and `history` are rollups of the retained event log. A rollup table
  that is still there but holds none of the source's days is a rebuild away from
  being correct, so it does not block: but the exception is only as strong as
  its premise, which is the retained support the rebuild reads. Each lost day is
  classified on its own: a day the store still holds at least one retained
  event for is listed in the domain's `recoverableDays`, joins the days that
  need an acknowledgement, and is marked `derived_rollup_lost` once the operator
  accepts it. A lost day the store holds no retained event for has nothing to
  rebuild it from: it is listed in `unrecoverableDays`, the run fails closed
  with `upgrade_reconcile_target_incomplete` naming that day, and no
  acknowledgement can waive it. That is the reviewer's probe: a source pricing
  day with no retained events of its own, and a cleared store-side rollup, is
  refused instead of being treated as recoverable.
- A missing derived table, and any day of `retained` or `coverage` the store
  cannot show, stays unwaivable: the retained events are the log itself and the
  deleted coverage table records what an archive run removed, so neither can be
  recomputed from anything else. The boundary is per day, not per domain: one
  lost day blocks the step even when every other day of the same domain
  compares, because a day the store cannot show is lost data and not a
  disagreement.

A run that established no operation domain at all — the source holds none of the
compared tables — compared nothing, so it is not recorded: it fails with the
typed `upgrade_reconcile_nothing_compared` instead of reporting a recorded stage
that names no domain. Nothing is marked and the row keeps the stage `copy_in`.

### Verdict, marks and blocking

Every measured mismatch day and every day the parity detection flags (raw-deleted
rows whose projection is missing, stale or past the watermark, plus a deleted
coverage table that disagrees with the archive refs) must be acknowledged:

- `--acknowledge <YYYY-MM-DD>`, repeatable; anything that is not a calendar day
  is a usage error, so a typo cannot acknowledge another day.
- `--acknowledge-parity-gaps` accepts the days whose *only* problem is the
  parity detection. It is not a bulk acknowledgement: a day that also carries a
  measured mismatch, or a cleared derived rollup the store lost, still blocks
  until it is named with `--acknowledge`, because the switch says nothing about
  the data difference on that day. The days it accepted are still written into
  the evidence.

An unacknowledged day blocks with the typed `upgrade_reconcile_mismatch`, marks
nothing, records nothing and leaves the stage at `copy_in`; the report still
names every day that needs the decision. An acknowledged day is inserted into
`usage_coverage_incomplete_days` with `reconcile_mismatch` when a measure
disagreed, `derived_rollup_lost` when only a cleared derived rollup explains it,
or `projection_lost_before_v1_14_6` when only projection loss does — a measured
disagreement always keeps the stronger reason.

Each derived day is acknowledged individually. `--acknowledge <YYYY-MM-DD>` names
one calendar day per flag and has to be repeated for every day the report lists,
because there is no bulk switch for a cleared derived rollup:
`--acknowledge-parity-gaps` covers only a day whose sole problem is what the
parity detection reports. The
report lists the days under the domain's `recoverableDays` and repeats them in
`unacknowledgedDays`, so the operator acknowledges exactly the days the run
measured, and a day listed in `unrecoverableDays` cannot be acknowledged at all.

### Locks

The step reads a stopped 1.x source and writes the v2 store, so the command
holds two fences for the whole run: the 1.x ownership lock on the source
(`processlock.AcquireExisting`, as `upgrade run` does) and the v2 data-root
process lock on the Manager store (`processlock.Acquire`, as `adminreset` and
`derivedmaintenance` do). The service refuses before it reads anything unless
both locks belong to the paths it is about to use, so a programmatic caller
cannot skip them. A fence another process holds is a typed refusal:
`upgrade_source_busy` for the source and `upgrade_target_locked` for the store,
both exit 1. A live Manager and a second reconciliation therefore cannot write
the same store or share the same scratch directory.

### Recording

The marks, the evidence JSON in `upgrade_operations.reconciliation` and the
`Advance` over the operation domains the comparison actually established —
`retained_projection` for the retained events and the account rollups derived
from them, `pricing`, `coverage` — to stage `reconcile` are written in one
transaction, always with `Advance`, never `Put`: `Put` rebuilds the row and would
clear the provenance `complete` requires. Both writers own `updated_at_ms`
through the operation repository's `nextUpdatedAtMS`, and validate the row they
are about to write in the same transaction, so a row whose clock ran ahead
during an earlier copy-in is recorded instead of rejected until the wall clock
catches up. A reader therefore never sees marks
without the evidence that explains them, and an interruption leaves the store
exactly as it was; because every write is idempotent, running the step again
writes the whole result. A domain that was not applicable is never recorded, so
`domains_done` only ever lists domains this step compared. A store the operation
already carried past `reconcile` is refused before anything is compared, and the
mark insert is `on conflict (day) do nothing`, so a day keeps its first reason
and timestamp and a second run marks nothing new.

### After a rebuild

Rebuilding a cleared derived rollup later does not clear its mark, and no code
path in this repository removes one: `derived_rollup_lost` records what the
migrated store lost, not what the running server can recompute later, and the
marking table is evidence rather than derived data. The mark is therefore a
lower bound that stays for the life of the store — it disappears only when the
store itself is rewritten, and such a store starts from its own reconciliation
and writes its own marks. An operator who rebuilds the lost days must leave the
mark in place and keep treating those ranges as partial: `RawCoverage`,
`AnalyticsCoverage` and the Key workspace report a marked day as incomplete no
matter what the rebuilt rollup now holds, and a manual `delete from
usage_coverage_incomplete_days` would silently restore the overstatement the
mark exists to prevent.

### The marking table and its consumer

```sql
create table if not exists usage_coverage_incomplete_days (
  day text primary key,
  -- 'projection_lost_before_v1_14_6' | 'reconcile_mismatch' | 'derived_rollup_lost'
  reason text not null,
  source text not null,          -- 'upgrade_reconcile'
  recorded_at_ms integer not null
)
```

The rows are evidence, not derived data: no revision change or cleanup job may
drop or rebuild them. Three read paths report them instead of presenting a
marked day as complete, and none of them changes the numbers:

- `usagearchive.Repository.RawCoverage` sets `Incomplete` and lists
  `IncompleteDays[{day, reason}]` beside the counts it already returned.
- `monitoring.AnalyticsCoverage` carries the same `Incomplete`/`IncompleteDays`
  pair for the requested range, a `ComparisonIncomplete`/
  `ComparisonIncompleteDays` pair for the comparison window, and
  `Incomplete`/`IncompleteDays` inside every `AuxiliaryRange` — a marked day
  cannot be presented as complete in a drilldown or a previous window — and
  `coverageWarningRequired` turns true when any range is marked, so the analytics
  coverage warning is raised for it.
- The Key workspace (`identityusage`) reads `IncompleteDaysIn`, and a key's
  numbers read `partial` with reason `upgrade_incomplete` when a marked day falls
  inside that key's provable window.

### CLI

`cpa-manager-plus upgrade reconcile --legacy <dir> --source-version <release>
[--target <dir>] [--bundle <dir>] [--scratch-dir DIR] [--json]
[--acknowledge <day>]… [--acknowledge-parity-gaps]` runs the preflight gate
first, then the reconciliation. The declaration is required for the same reason
`upgrade run` requires it: a 1.x database does not record its release.

| Exit | Meaning |
| --- | --- |
| 0 | reconciled: the marks, the evidence and the stage were recorded |
| 1 | a refusal the operator has to look at: a blocked preflight (`upgrade_source_busy` among others), a fence another process holds (`upgrade_source_busy`, `upgrade_target_locked`), no recorded copy-in (`upgrade_reconcile_not_copied_in`), a stage that can no longer enter the step (`upgrade_reconcile_stage_closed`), a record without the copied source fingerprint (`upgrade_reconcile_record_incomplete`), an unacknowledged day (`upgrade_reconcile_mismatch`), a day the store cannot show (`upgrade_reconcile_target_incomplete`), or a run that compared nothing (`upgrade_reconcile_nothing_compared`) |
| 2 | usage, environment, or a retryable condition: `upgrade_source_changed` is retryable and keeps exit 2 |

Every refusal prints the report with its typed `errorCode` (the report is printed
either way), so the exit code only says the migration did not advance and the
code says why. The `--bundle` default is the recorded copy-in's backup
destination, and the recorded copy-in is re-verified against it before anything
is compared.

**Known limit (U1-1b).** The accepted source contract is pinned to v1.14.4, and
`usage_archive_deleted_coverage_daily` arrived in v1.14.6. A source the CLI
accepts therefore never holds the coverage table, so the `coverage` domain is
reported `not applicable` on every real pinned source and is never established;
its comparison is exercised by fixtures that carry the table, and it becomes
live when the pin moves. The same applies to the parity detection, which is
`not-applicable` for a source without `usage_archive_event_refs`. This is a
property of the pin, not of reconciliation, and this slice does not change the
pinned bootstrap contract.
