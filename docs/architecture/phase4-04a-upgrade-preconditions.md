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
| `internal/processlock`: `AcquireExisting`, `AcquireExistingReadOnly`, `Validate` | Hold one canonical source fence through authentication, inventory, expensive validation and final rechecks. `AcquireExistingReadOnly` takes the same marker through a read-only descriptor, so a stopped source preserved on a read-only mount still proves exclusivity; it is used by the read-only preflight, `upgrade run` and `upgrade reconcile`, and grants nothing beyond the shared exclusivity fence. No new marker; cooperating Manager writers are excluded. |
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
  stage, completed domains, source observations, backup manifest reference,
  reconciliation object, manual items, `revision`, `last_forward_stage` and
  `source_root`.
  `Put` uses the revision returned by `Get` as a compare-and-set token inside
  its write transaction; a stale token returns `ErrConflict` without changing
  evidence or progress. A missing or migrated row starts at revision zero, and
  every committed writer increments it, even within one millisecond: `Put`,
  `Advance`, the External step's refusal and clear paths, the reconciliation
  record, the source-root backfill and the rollback verdict all carry the
  revision they read and store one more.
  Timestamps are repository-owned observations, never concurrency tokens.
  `Advance` appends a domain and moves the stage in one transaction; `Put` also
  preserves completed domains. Duplicate domains and non-object reconciliation
  JSON (including `null`) are rejected before defaults are normalized. Stored
  corruption is reported rather than silently repaired: defaults are applied
  only to construction and `Put` input, so a stored row must hold an actual
  JSON string array for `domains_done` / `manual_items` and an actual JSON
  object for `reconciliation`; empty text, JSON `null`, a non-array or an array
  with a non-string element makes `Get` (and any write that would read the row)
  return `ErrInvalidRow`.
  Entering `failed` preserves the highest forward stage in `last_forward_stage`;
  a resume below it is refused. A migrated failed row with no recorded high-water
  mark stays parked and cannot automatically resume from an invented position.
  `complete` is frozen. The first transition into `complete`, including the
  write that creates the row there, is accepted once; afterwards a `Put` or
  `Advance` that would change a domain, an observation or the stage is refused
  with `ErrStageRegression` and leaves every column, the revision and the
  timestamps untouched. An exactly identical repeat is a success that writes
  nothing, and entering `failed` from `complete` stays refused.
  `rolled_back` is terminal and is recorded only together with the evidence it
  rests on (the restarted image, its source and form, the health endpoint, the
  observed fingerprint and when it was observed): `RecordRollback` writes the
  stage and the observation in one transaction, a bare verdict is refused with
  `ErrRollbackEvidenceRequired`, and a stored `rolled_back` row without a whole
  observation is reported as `ErrRollbackEvidenceMissing` instead of being read
  as a malformed row. A failed row is not a rollback position, so it cannot
  resume at the verdict. The table is created only in the v2 Manager database,
  never in the legacy source root.
- **Never in place.** The existing source fence is acquired through a read-only
  descriptor (`processlock.AcquireExistingReadOnly`) before fingerprinting,
  archive walking, CPA inspection or snapshot copying, and held through cleanup.
  A live writer is `upgrade_source_busy`; a source whose database is absent or
  empty is `upgrade_source_missing`; a shared-policy refusal of the file itself
  (a hard link, or a link reached through the database path) is
  `upgrade_source_symlinked` or `upgrade_source_lock_unavailable`, the same codes
  the credential read and the mount lookup answer with; a database that cannot
  be read through the private snapshot is `upgrade_source_unreadable`, and one
  that changed while it was copied is `upgrade_source_changed`. No source lock
  file is created. Every v1.14.x release sets `pragma journal_mode = WAL`, and a
  plain `mode=ro` open can create sidecars beside it. Preflight therefore copies
  the database and sidecars into private scratch (`OpenSnapshot`), compares source
  digests before and after against the copied bytes, opens only that copy with
  `mode=ro` plus `query_only`, and removes scratch. The credential read of the
  Upgrade surface uses the same private copy rather than opening the source in
  place, so a preserved 1.x source keeps its bytes and gains no sidecar; the
  source fence is taken read-only, so a source preserved on a read-only volume
  still proves exclusivity. `upgrade status` uses the same private-snapshot rule
  for the Manager database, so it works when the Manager volume is not writable.
- **Cooperative cancellation.** Fingerprinting, archive walking and snapshot
  copies check context between bounded read/write batches. Cancellation retains
  its context error and releases the fence, files and scratch. A filesystem call
  already blocked in the kernel cannot be forcibly cancelled by these checks.
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
  An applicable parity query or scan failure blocks readiness with
  `upgrade_source_unreadable` and the fixed detail `parity inspection failed`;
  it is not a skipped check and never exposes SQL details. A successfully measured
  parity gap remains non-blocking, so later reconciliation can handle it.
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
  from a private snapshot, so both work on non-writable volumes.
  `cpa-manager-plus upgrade migrate [--json] [--db-path PATH]` is the one command
  that adds upgrade columns an older store lacks; it needs the v2 Manager
  stopped and holds the same process lock for its proof snapshot and its single
  writable open. That snapshot is
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
- **V2 data-root fence.** `CopyIn` takes the v2 data root's own ownership
  lock (`processlock.Acquire` on `<target>/usage.sqlite`) in the service,
  before its first write to that root, and holds it for the whole mutating
  path: target creation, backup creation/reuse, staging restore, the member
  renames, the marker publications, the migration, the row commit and the
  work-directory/marker cleanup. The
  CLI does not take it, so every caller is covered by the same fence, and it
  is a different lock in a different directory from the 1.x source fence.
  A held root is a typed refusal (`upgrade_copy_in_v2_running`) before any
  target or backup byte is written; the CLI reports exit 1 for it, like a
  blocked preflight, while usage and environment failures stay exit 2. The
  fence excludes cooperating Manager writers; it does not claim anything
  about a process that ignores the marker. `upgrade preflight` and
  `upgrade status` remain read-only and take no exclusive target lock.
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
  the source as bytes under the held `processlock` fence. `upgrade run` and
  `upgrade reconcile` take that fence through a read-only descriptor
  (`processlock.AcquireExistingReadOnly`, as preflight does): the same exclusive
  marker and the same excluded writers, but the source is never opened for
  writing and a stopped source preserved on a read-only mount still proves
  exclusivity. The fingerprint covers
  the database, its WAL/SHM/journal, `data.key`, every archive file and every
  other entry of the source root (the ownership marker is the one deliberate
  exception: it belongs to the holder, not the source). It is recomputed after
  the copy; a difference rolls back the members this run moved plus the work
  directory and aborts with `upgrade_source_changed`. The read-only descriptor
  is proven on the test host with a `0400` source marker; an actual Linux
  read-only (EROFS) bind mount was not exercised, so that mount qualification
  remains unverified.
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
  `updated_at_ms` monotonic; no caller proposes a timestamp. The run's `Result`
  is read back from the persisted row after the commit, so `updatedAtMs` reports
  the durable `updated_at_ms`: a first run no longer prints `0`, and a rerun that
  rewrites the row reports the timestamp it just wrote.
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
  `upgrade preflight` and `upgrade status` remain read-only. Before the
  preflight, `run` reports a recorded stage past `copy_in` without changing
  anything. That guard reads the copy-in marker first and steps aside while a
  copy-in is in flight (or its marker is unreadable): the marker and the member
  digests are what a resume compares, never an open of the target, because a
  read-only open of the materializing database would create its `-shm` and
  strand the resume. The run stops at the copy-in and prints
  `cpa-manager-plus upgrade status --db-path <v2 database>` plus a note that
  reconciliation is a separate authorised step this build does not ship; it
  prints no command that this build cannot dispatch. Every subcommand shares the
  exit contract: 0 ready, 1 a blocked preflight or a typed operational refusal
  (the command writes the reason to stderr exactly once and `main` does not log
  it again), and 2 for usage, environment and retryable failures.

### Admission surface table

`legacypreflight.AdmitStartup` reads the injectable
`UpgradeAdmission` (`DatabaseUpgradeAdmission` in production) before it
classifies a source. The reader consults the `.upgrade-copy-in/copy-in.json`
marker beside the database first, because the marker is the only evidence that
can be read without opening the target:

- A valid `materializing` marker is decided from the marker alone. That marker is
  published before the first member is moved and before the migration that
  creates the operation table, so no row can describe such a target; opening it
  read-only joins the copied WAL and rewrites `usage-sqlite-shm`, the member the
  resumed move compares with the backup. Admission would otherwise make an
  interrupted live-WAL copy-in unresumable.
- Any other marker state reads the `upgrade_operations` row next, so a committed
  row outlives its marker: the marker turns `migrating` before the migration and
  is removed only after the row commits, and a death between those events leaves
  both, where the row is the stronger evidence.
- Without a row, a marker still answers `in_progress`.

A reader error fails closed, including an unreadable marker beside a readable
row; a `nil` reader keeps the historical rule. `Surface()` then selects what the
process serves:

| Recorded state | Admission | Startup surface |
| --- | --- | --- |
| no table, no row and no marker for this database (or `nil` reader) | `none` | unchanged: only `fresh` and `native-v2` are admitted; a 1.x source stays blocked |
| `materializing` marker, no row (the members are being moved, the migration has not run) | `in_progress` | Upgrade surface only, and the target is not opened: the resume keeps the `-shm` it restored |
| `migrating` marker, no row (the members moved, the row did not) | `in_progress` | Upgrade surface only |
| the same marker under another absolute path, or a root relocated to another mount | `in_progress` | Upgrade surface only: the marker belongs to the data root it sits in, so a relocated volume is never read as "no record" |
| a marker naming a different database file | — | not admitted (fail closed): it is evidence for another run |
| `preflight`, `copy_in`, `reconcile`, `cpa_import`, `external_connect`, `verify`, `failed` | `in_progress` | Upgrade surface only: fixed Bootstrap discovery status reads, the read-only shell, the Upgrade authentication leaf and the wizard's two read-only routes. Console, Key workspace, gateway configuration, lifecycle and model traffic answer 404; health stays 503 and no writable subsystem starts |
| `rolled_back` | `in_progress` + reason `upgrade_rolled_back` | Upgrade surface only, same as any unfinished operation. The reason is the typed string the console-side `upgrade status` prints as `reason: upgrade_rolled_back` and puts in its `reason` JSON field, derived by the same `legacypreflight.ReasonForStage` the admission log uses; it is never a completion. Unauthenticated discovery cannot carry it: the public projection exposes only mode, authentication and recovery, and its reason vocabulary has no rollback value |
| `complete` with `source_kind=upgraded-legacy`, both source fingerprints and the backup manifest digest | `complete` | normal v2 surfaces, as for a native store |
| `complete` missing any of those observations | — | not admitted (fail closed) |
| reader error, unreadable marker or unsupported stage | — | not admitted (fail closed) |

A record may relabel a source only when a copy-in could have produced it: the
copy-in always runs the incremental migration before it records anything, so the
classified kind of a copied-in store is `native-v2`. `legacy-manager` is
therefore never relabelled — a 1.x schema with a row (complete or not) stays
blocked and is never admitted to the full surface.


## U1-7: the Upgrade wizard's two read routes

`internal/http/controller/upgradewizard` serves `installation/upgrade/preflight`
and `installation/upgrade/status`. The fixed Bootstrap API carries them before
route cutover and again while an admitted Upgrade operation is unfinished, the
canonical continuation carries them before completion, and a `ProductComplete`
installation answers 404: the reads end with the operation. They never appear
on both surfaces at once, and no write route exists.

- **Fixed surface while the operation is unfinished.**
  `cmd/cpa-manager-plus/startup_discovery.go` mounts the two reads beside the
  Upgrade authentication leaf on the fixed Bootstrap API. They share that
  leaf's gate — not ready, discovery still active, Upgrade auth constructed —
  and close on `BeginWritableStartup`, on readiness and on completion. This is
  the only carrier a copied-in v2 root has: the copy-in replaces `usage.sqlite`
  and leaves no Fresh record, so the canonical continuation would answer 404
  while the operation is in progress. No other fixed-carrier resource opens to a
  source credential.

- **Credential.** The Upgrade surface has its own identity — the source
  administrator key the bootstrap Upgrade entry verified — presented as
  `Authorization: Bearer <key>` with exactly one header value. The whole surface
  resolves one credential file by one rule
  (`upgrade.CredentialDatabaseFrom`): the configured 1.x database when the mount
  holds one the shared file policy accepts, otherwise the Manager's own store,
  because a copied-in v2 root carries the same administrator credential and is
  the only file left without a mount. The rule has one implementation, and the
  handler has no credential input of its own: it applies that rule to the mount
  it inspected for the request, so nothing a caller supplies can point the read
  at a file the policy refused. The handler verifies the resolved file through
  `upgrade.VerifyAdminSource` when it is a 1.x source, which reads it through
  the same private snapshot the preflight takes (`OpenSnapshot`) and runs
  `setting.LoadAdminCredential` + `security.VerifyAdminKey` on the copy. No
  source is ever opened in place: `mode=ro` still creates `-wal`/`-shm` beside a
  WAL database, and an immutable handle on a source that was cold when it was
  inspected silently misses a WAL that appears while the read is being set up.
  The source files are digested before the copy, again after it, and the copy is
  digested as it is written; any difference means the source was not stopped and
  the read is refused (`upgrade_source_unreadable`) instead of answering from a
  stale or mixed state. The Manager's own store is this process's live database,
  not a volume to preserve, so `VerifyManagerCredential` reads it in place
  read-only and immutable only when no live WAL can hold rows. The one exception
  is a target an unfinished copy-in still marks `materializing`: its restored
  members, including the copied WAL index, are exactly what the resumed move
  compares with its backup, and a read-only open of that store would join the
  copied WAL and rewrite `-shm`. The marker is therefore read before any open,
  the same order admission and the status projection use, and the key is proved
  against a private snapshot outside both the target's data root and the
  configured 1.x source root. Both roots are named by every caller that can reach
  that read: the entry leaf carries the root it resolved at startup, and the
  wizard carries the root its per-request mount lookup resolved — a lookup that
  outlives its bound still names the root it published before the hung call, but
  the snapshot then fails closed with `503 upgrade_mount_timeout` instead of
  resolving that root again: canonicalizing it for the exclusion comparison is
  the filesystem call the bound exists to stop. A lookup that never resolved the
  root refuses with the typed unknown-root condition instead of staging a copy in
  a tree nobody named. A target whose key cannot be proved this way
  answers the typed condition that stopped the read — the mount's deadline, the
  unknown-root condition, or the unreadable-source condition of a refused copy;
  authentication is never skipped, and a rejected key still answers `401`.
  The credential read returns the credential row and nothing else:
  no `quick_check`, no schema vocabulary, no parity. The first read of one
  source state is bounded by `CredentialVerifyTimeout` — a 2s base plus 1s per
  64 MiB of every byte the read carries, the database plus a present WAL — and
  capped by `CredentialVerifyMaxBudget` (20s): the read copies the whole source,
  and a source that has not checkpointed keeps most of its data in the WAL, so a
  budget derived from the database file alone answered 503 on a healthy
  WAL-heavy source. The sum is computed without overflow and saturates into the
  cap. A proved credential
  is then remembered in memory for the source state it was proved against: the
  digest of the presented key bound to the database and WAL device, inode, size
  and modification time, with an absent WAL as its own state. Every request stats
  the source; an unchanged identity reuses the verdict, and a write, checkpoint,
  a replaced file, a new WAL, an eviction or a restart verifies again. No
  credential and no key is stored and nothing is persisted; a rejected key or a
  failed read is never cached as a success; at most 64 success entries are kept;
  and concurrent first requests for one state share a single verification. The
  work itself is bounded separately from those entries: at most four
  verifications may run at once, and a request for a key already in flight joins
  it without taking another slot. A caller that gives up while waiting for a slot
  starts nothing and leaves no flight; a distinct wrong key can therefore not
  grow the active work or the cache without bound. The shared read has its own
  lifetime — it may finish for another waiter and for the cache — while every
  caller, the one that started it included, waits under its own context and a
  cancelled request returns its deadline rather than a success that landed after
  it was abandoned. That check covers a remembered verdict too: a caller
  cancelled while it waited for the cache itself is answered with its context
  error, not with the entry that was already there when it got in. Every
  authenticated request runs this, including every status poll, so a whole-file
  scan there would answer 503 on a large source and the result page would never
  arrive. The integrity check stays in `Preflight`, which is what qualifies a
  source. `VerifyAdminSource` takes no fence. The Manager store's live credential
  is not cached: it is this process's live database, whose WAL and file change
  under ordinary traffic, and its credential is one indexed row in an already
  open store. Only a target read as a preserved source because it is still
  `materializing` participates in the state-keyed cache, and only for the exact
  byte state it was proved against.
  A read that outlives the bound — or a caller whose own deadline had passed —
  is `503 upgrade_credential_timeout`: a slow but healthy volume is a retry, not
  a repair.
  The installation session, the Fresh Admin key, a cookie and an absent header
  are all `401`; the two surfaces no longer check a session before this handler
  runs, so no missing decision can read as allow.
  The rule is applied at two different times. The entry leaf resolves once, when
  it is constructed (`upgrade.CredentialDatabase` over the startup `paths`); the
  two read routes resolve per request. A key accepted at the entry page is
  therefore accepted by the reads while the mount is unchanged, and a key kept in
  the other file is refused by both — but a mount that appears, disappears or is
  repointed after startup is seen by the reads first, and the entry leaf keeps its
  startup resolution until it is rebuilt.
- **Entry statuses.** The authentication leaf
  (`/.well-known/cpamp-bootstrap/upgrade/authenticate`) answers one of exactly
  two statuses for a proved credential, and both carry `authenticated: true`,
  `migration_available: false`, `expires_in_seconds: 60` and an opaque instance
  revision that is never a session token: `unsupported_upgrade` when the public
  discovery observation could classify the source, and `observation_unavailable`
  when it could not describe it — a live WAL source is recovery-owned, so the
  public observation refuses it. `observation_unavailable` says only that the
  identity is proved and the public summary is unavailable: it claims no stage,
  no readiness and no migration permission, and the two read routes prove the
  key again on their own and name the source's conditions. It is not a credential
  verdict, so a proved key is never downgraded to `401` because the observation
  refused the source, and it is not a success claim, so the page does not present
  it as upgrade-ready. A wrong key cannot open the leaf: it is
  `401 admin_unauthorized` whenever the credential can be asked and refused, and
  `503 source_unavailable` when the observation faulted for its own reason and
  the key could not be proved — rather than blaming a credential the leaf could
  not check. Every response for a proved credential is `200`, including the
  unobservable one; `503 source_unavailable` is reserved for a source this build
  cannot prove a key against, and `409 source_changed` for a presented revision
  that no longer matches.
- **The entry's own read preserves the source.** The authenticated observation
  behind the entry leaf (`legacypreflight.WithAuthenticatedInspection`) reads a
  cold source through a private copy as well: the read-only snapshot branch of
  the SQLite inspection copies the rollback-mode database into scratch space
  outside every source directory, re-validates the copy's own journal header and
  opens only that copy. The source therefore keeps its entries, bytes, sizes and
  modification times and gains no `-wal`/`-shm`; before this, a writer that
  switched a cold source to WAL between the header check and the first credential
  query left the sidecars of a read-only open in the preserved tree.
- **One file policy on both paths.** The mount lookup applies the same database
  file rules as the preflight and the credential read
  (`processlock.InspectDatabaseFile`, via `upgrade.MountDatabase`): the path must
  be the database itself, never a symbolic link, and a regular file with content
  reachable by exactly one link. A mount that fails the policy therefore holds no
  database — the credential resolves to the Manager store and every leaf stays
  usable, instead of the read being pointed at a file it would refuse and
  answering 503 on the page that should name the condition. The refusal carries
  the file policy's code (`upgrade_source_symlinked` for a link,
  `upgrade_source_missing` for an absent or empty file,
  `upgrade_source_lock_unavailable` for a hard-linked or otherwise uninspectable
  one) and the status projection reports it as its `sourceRoot` marker.
  The lookup belongs to the request that presented a credential shape from the
  request's own origin: no credential and a cross-site request never reach it,
  and an admitted request asks it at most once — authentication and the marker
  share that answer. It runs under `MountLookupTimeout` (2s) and at most one
  lookup per handler is in flight, because the filesystem calls behind it cannot
  be cancelled: a poll that arrives while a hung lookup is running joins it
  instead of starting another, and a mount that never answers is reported as
  `upgrade_mount_timeout` — a retry, not a database the operator has to repair —
  instead of holding the poll open. The lookup is a handler field rather than a
  package variable, so a test substitutes it for one handler and the package
  holds no mutable state.
  `503 upgrade_source_symlinked` on a leaf therefore means the resolved file
  itself is a link — in practice the Manager store — never the mount the
  operator mounted through one. When the read runs, the resolved file is never
  opened in place: the database and its sidecars are copied into private scratch
  space and only the copy is opened (`OpenSnapshot`). A swap between the
  inspection and the read is refused (`ErrSourceDatabaseChanged`, reported as
  `upgrade_source_unreadable`) by the digests taken before the copy, after it and
  over the copy as it is written, instead of comparing a credential from a file
  nothing assessed. A verdict served from the remembered state is guarded by the
  same identity: the source is stat'ed on every request, and any change to the
  database or its WAL reads again.
- **Order.** The credential is answered before the path and before the verb, on
  both the entry leaf and these two routes: an unauthenticated caller receives
  `401` whatever its method and whatever the path, so it cannot tell a real
  route from an unknown one or learn which method a route takes. Only a proved
  credential sees `405` (`Allow: GET`) or `404`. The mount lookup itself sits
  behind the two gates that need no filesystem at all — the credential's shape
  and the browser's origin — so a caller that presents no credential, and a
  cross-site caller whatever it presents, makes the reads touch no file.
- **Failure order.** `401` is reserved for a credential verdict. A missing or
  unusable mount answers `503 upgrade_legacy_root_missing` /
  `upgrade_legacy_root_unusable`; a configured mount whose database the file
  policy refuses answers that refusal
  (`503 upgrade_source_missing`, `upgrade_source_symlinked`, or
  `upgrade_source_lock_unavailable` for a hard-linked or non-regular database),
  and only when there is no Manager store to fall back to — with one, the
  credential resolves there and the refusal is reported as the `sourceRoot`
  marker instead; a mount lookup that outlives its bound answers
  `503 upgrade_mount_timeout` on every route that still needs the mount — the
  preflight, and any request whose authentication would have to resolve the
  source root to keep a private snapshot out of it; a status read whose
  credential falls back to the live Manager store needs no snapshot and reports
  the deadline as the `sourceRoot` marker instead; a credential read that exceeds
  its bound answers `503 upgrade_credential_timeout`; a resolved database that is
  itself a link answers `503 upgrade_source_symlinked`; a source that exists but
  cannot be read to verify the key answers `503 upgrade_source_unreadable`.
  None of these is
  a wrong-password message, and none of them is the ownership marker's verdict:
  that marker is the preflight's, and this surface never inspects it — the
  `upgrade_source_lock_unavailable` above is the file policy refusing the file,
  not a statement about who holds the source.
- **Ownership without a writer fence.** `upgrade.Preflight` takes the source's
  ownership fence through a read-only descriptor
  (`processlock.AcquireExistingReadOnly`): the same validated target and the same
  marker identity check as the writer path, with no write access, so a source
  preserved on a read-only mount still proves exclusivity. A live writer is
  `upgrade_source_busy`; a database that cannot be read through the private
  snapshot is `upgrade_source_unreadable`, and one that changed while it was
  copied is `upgrade_source_changed`. The fence is exclusive, so two concurrent
  preflights do not both proceed: the second is `upgrade_source_busy`. The
  wizard's own reads never take it — the credential read and the mount lookup
  apply the shared database file policy instead (`upgrade_source_symlinked` for
  a link, `upgrade_source_missing` for an absent or empty file, and
  `upgrade_source_lock_unavailable` for a file the policy refuses) — which is why
  a read still answers while an operator runs the preflight, and why the
  before/after fingerprint gate in `upgrade run` remains the write path's guard.
- **Projection.** The status route selects named columns (stage, domains,
  source kind/version, reconciliation days, manual items, error code/detail,
  updated time) and maps the stage onto `nextAction`. An unknown stage is shown
  verbatim; fingerprints, digests and paths have no field at all. The recorded
  stage is readable without the mount; a separate `sourceRoot` marker reports
  `available` or the code this request's mount lookup reached, and those are the
  only reachable values: `upgrade_legacy_root_missing` /
  `upgrade_legacy_root_unusable` for the configured directory,
  `upgrade_source_missing` / `upgrade_source_symlinked` /
  `upgrade_source_lock_unavailable` from the shared database file policy, and
  `upgrade_mount_timeout` when the lookup outlived its bound.
  The marker mirrors the file policy, not the preflight: a mount whose ownership
  marker is absent or busy, or whose database fails its integrity check, still
  reads `available` here and is refused by the preflight, which is the step that
  qualifies the source. The marker is only visible to a caller holding the
  resolved credential: when the mount is refused the credential is the Manager
  store, so the mount's own key gets `401` and cannot read the marker that
  explains the refusal. `preflight` still inspects that mount, not the credential
  file the surface fell back to.
  The projection applies the admission's marker order, not an equivalent one: a
  `materializing` marker answers `copy_in` with `copy_in_marker` evidence and
  `nextAction=copy_in` without opening the target, so a status poll cannot
  rewrite the `-shm` a resumed live-WAL move restored; any other marker state
  reads the row first, because a committed row outlives its marker. Both readers
  use one provenance rule (`copyinmarker.SameTarget`): the marker names the
  database inside its own data root, so a relocated root keeps its in-progress
  state for the admission and the projection alike, while a marker naming a
  different database file is a refusal for the admission and no record for the
  projection.
  Reconciliation travels through the same fixed allow-list (`mismatchDays`,
  `mismatchDaysTotal`, `mismatchDaysTruncated`), never the producer's payload.
  The list is bounded at 366 days — a transport bound, not a judgement about the
  history — so a longer history is reported with its total and the truncation
  flag: a cut list is a visible window, never silently the whole history, and
  the wizard refuses a reply whose total is smaller than the list it stands for.
  The page reads the recorded operation before anything else: a recorded stage
  resumes where it stands, terminal or not, and the read-only preflight is the
  first step only when the store answers that nothing is recorded.
- **Scrubbing.** Operator-visible text is redacted by content, not by path
  shape: `Bearer …` (including a value wrapped onto the next line after a lone
  `Bearer`), `Authorization: Basic …`, `sk-…`, this product's own
  `cpamp_`/`cmp_admin_` key prefixes in any case (a key broken mid-token across a
  line boundary has its continuation removed where it sits, so the fragment
  without a prefix does not survive), `x-api-key`/`X_API_KEY` values, a
  `password=`/`password:` assignment in a query string, DSN, variable or
  command, credentials embedded in a URL authority and local paths; the same
  scrubber covers `manual_items`, and a multi-line detail is scrubbed line by
  line instead of being dropped.
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
(`processlock.AcquireExistingReadOnly`, as `upgrade run` and preflight do) and
the v2 data-root process lock on the Manager store (`processlock.Acquire`, as
`adminreset` and `derivedmaintenance` do). The source is read-only for this
step, so its fence never opens the source volume for writing: a stopped source
preserved on a read-only mount still proves exclusivity, and the refusal for a
marker another process holds is unchanged. The service refuses before it reads
anything unless both locks belong to the paths it is about to use, so a
programmatic caller cannot skip them. A fence another process holds is a typed
refusal: `upgrade_source_busy` for the source and `upgrade_target_locked` for
the store, both exit 1. A live Manager and a second reconciliation therefore
cannot write the same store or share the same scratch directory.

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

**Coverage and parity applicability.** An earlier draft of this section recorded
that a pinned v1.14.4 source never holds
`usage_archive_deleted_coverage_daily` because the table "arrived in v1.14.6",
and that the `coverage` domain was therefore never applicable outside fixtures.
That premise is wrong for the schema this step requires: `requiredLegacyTables`
in `service/upgrade/schema.go` lists both
`usage_archive_deleted_coverage_daily` and `usage_archive_event_refs`, and the
coverage table is part of the v1.14.0 schema itself — the 1.14.x DDL creates it
in every release from v1.14.0 to v1.14.10. A source preflight accepts therefore
always carries the tables the `coverage` domain and the parity detection read.
The parity question is `not-applicable` only when one of
`usage_archive_event_refs`, `usage_archive_deleted_coverage_daily` or
`usage_events` is absent, which preflight already refuses as a structural lower
bound; it is not a property of the pinned release. The CLI-level regression
`TestReconcileReportsTheCopiedStoreAndRecordsTheStage` runs `upgrade run` and
`upgrade reconcile` against that pinned release and asserts the `coverage`
domain is among the recorded domains, so this applicability is real for the
documented CLI path, not something only a service fixture can reach. This slice
still does not change the pinned bootstrap contract.

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
  (`upgrade_rollback_unproven`), and pins the 1.x image to an exact reference.
  Two forms are accepted: an exact release tag (`vX.Y.Z`, with or without a
  repository prefix, derived as `seakee/cpa-manager-plus:v<source_version>` when
  none is given) or an immutable `@sha256:` digest. Both forms follow the Docker
  reference grammar for the repository part: an optional registry host with an
  optional port, then one or more lowercase repository components. A reference
  whose repository part carries whitespace, a newline or a shell metacharacter is
  `upgrade_rollback_image_unpinned` instead of text that reaches the compose file
  or the printed `docker pull`, so `$(…)` and a newline cannot add a compose line
  or a shell word. A digest is `[registry[:port]/]<repository>` followed by
  `@sha256:` and 64 lowercase hex characters, so
  `registry.example.com:5000/team/cpamp@sha256:…` and
  `localhost:5000/cpamp@sha256:…` and `[::1]:5000/cpamp@sha256:…` (a bracketed
  IPv6 registry) are accepted, while `Team/cpamp@…` is refused: a first
  component without a dot or a colon is a repository path, and a path component
  is lowercase. The registry host itself is case-insensitive, as in Docker. The
  compose image line is a quoted YAML scalar, so a bracketed registry stays one
  value. Everything else — `:latest`,
  `:main`, `:beta`, a bare repository, an empty tag, a partial version, a
  malformed digest — is `upgrade_rollback_image_unpinned`, and the family
  override never changes that: it only relaxes the version-family comparison of
  an exact tag. An exact tag outside the recorded source's family is
  `upgrade_rollback_image_family` unless the operator passes the override; a
  digest is immutable, so no family comparison applies. The plan records
  `imageSource` (`operator` or `derived`) and `imageForm` (`release-tag` or
  `digest`), and the printed verify command passes `--legacy-image` back only
  when the operator named it, so a derived reference is never recorded as the
  image the operator started. The verdict persists the same two fields beside
  the reference (`rollback_image_source`, `rollback_image_form`), so a reader of
  the row can still tell an operator tag from a derived one and a release tag
  from a digest. The plan is written while holding the v2 data-root process
  lock, so a Manager that still serves the store refuses the plan with
  `upgrade_rollback_v2_running` even when the CLI runs in another network
  namespace and cannot reach its listener. Refusals write nothing at all.
- **An unfinished copy-in is resumed, never read.** Before `--prepare` or
  `--verify` reads the operation row, it reads the copy-in marker. A marker
  means the v2 data root may still hold members an interrupted copy-in is
  moving, and any SQLite open in place creates or rewrites the WAL index the
  resume compares by file identity: the refusal would strand the very recovery
  the operator needs. Both commands therefore refuse such a root with
  `upgrade_rollback_not_applicable`, name `upgrade run` as the way to resume,
  and write no plan and no row; once the resume records `copy_in`, the rollback
  commands work as documented below.
- **A row without a source root is bound once, not sent back to the copy-in.**
  When the row names no 1.x directory (a row written before the column existed),
  the plan compares the directory under decision with the before-fingerprint the
  copy-in recorded. Equality is the proof that this is the volume it read, and
  the row is backfilled once through `upgradeoperation.RecordSourceRoot` (a
  writable open, which is also what adds the column). A directory that does not
  reproduce that fingerprint is `upgrade_rollback_volume_unbound`, and the
  refusal names what works — point `--legacy` at the mounted volume whose
  contents reproduce the fingerprint, or run `--prepare` while the volume is
  still stopped and unchanged. Re-running the copy-in is never suggested: it
  refuses once it is recorded.
- **The plan's port is not the v2 port.** A rollback publishes the restarted
  1.x manager on `18318` (`--port` overrides it), never on the `18317` this
  Manager publishes by default, because both managers answer `GET /health` with
  the same identity. A plan whose port is the Manager's own is
  `upgrade_rollback_port_collision`; a plan whose port is already held is
  `upgrade_rollback_port_busy` (the port is bound on every local address and
  both wildcards). `--manager-addr` overrides the v2 listen address; without it
  the CLI uses the configured `HTTP_ADDR`, so a Manager moved to another port is
  compared and dialled on the port it actually holds. A wildcard bind
  (`0.0.0.0:PORT`, `[::]:PORT`) is the normal configuration and is reached on
  loopback. With that address the plan refuses while the v2 stack still answers
  there (`upgrade_rollback_v2_running`), and a probe of that listener is
  `upgrade_rollback_probe_self`. A refusal writes nothing.
- **The port and v2-running checks only see the CLI's own network namespace.**
  `portFree` binds the port on every address of the process that runs the
  command, and `requireV2Stopped` dials from the same namespace, so under
  `docker compose run --rm` they describe the run container, not the host.
  That is why the plan states the host-side checks (`docker ps
  --filter publish=<port>`, `lsof -nP -iTCP:<port> -sTCP:LISTEN`, and the
  compose invocation itself) for the operator to reproduce where the stack
  really runs; the checks are not claimed to be host observations.
- **The volume is the one the install used, and the identity half is the
  operator's.** `--volume` names either a Docker volume (including the
  unprefixed `cpa-manager-plus-data` a `docker run -v` install uses) or the
  absolute path of the original bind mount; without it the plan derives
  `<project>_cpa-manager-plus-data` as before. `volumeKind` says which form the
  plan generated: a named volume stays `external: true` (compose refuses a
  missing one instead of creating an empty replacement), a bind mount is written
  as `<host directory>:/data` with no top-level `volumes:` key. A bind source
  that is not the directory the operation recorded is
  `upgrade_rollback_volume_unbound`. The plan proves the *directory* (data key,
  backup manifest, archived bytes) and that a v1 answers `/health`. It cannot
  prove that the answering process mounts that directory: the Manager runs in a
  container and must not reach the Docker socket. `identityProof` is therefore
  `operator-verified`, `identityStatement` says so in words, and the CLI prints
  both; the preconditions list the host-side checks (mount source, volume
  mountpoint, port) the operator reproduces. When the CLI itself runs under
  `docker compose run --rm`, it sees its own network namespace and paths, so
  those checks are stated as host-side checks rather than observations.
- **The plan** (`<v2 data root>/.upgrade-rollback/plan.json`, canonical-JSON
  SHA-256 in `digest`) names the pinned image, the volume and its kind
  (`volume`/`volumeKind`), the mountpoint the 1.x container uses, the recorded
  source root, the published `port` and the v2 Manager `managerPort` beside it,
  the
  recorded `backupDirectory`, the exact `docker compose -p cpamp -f - up -d
  cpa-manager-plus` invocation, the 1.x compose file to feed on stdin (publishing
  and binding the plan's own port), the preconditions and the health proof:
  `GET /health` on the plan's port with the documented
  `{"ok":true,"service":"cpa-manager-plus"}` identity, plus a read-only SQLite
  open. The preconditions state that the v2 stack must be stopped first, print
  the port check the operator reproduces on the host (`docker ps --filter
  publish=<port>` and `lsof -nP -iTCP:<port> -sTCP:LISTEN` must print nothing),
  and quote every path. `/health` is the only unauthenticated 1.x endpoint —
  `/status` requires the admin key and answers 401 without it, so it can never
  prove a volume. It is evidence for the operator, not an authorization, and it
  is written only under the v2 data root.
- **`RecordRollback`** takes the operator's observation (roots, backup, image,
  health URL, time) and derives the proof itself in one call: it probes the
  health endpoint and requires the documented identity (a bare 2xx or another
  service is `upgrade_rollback_identity_foreign`, a probe that does not address
  `/health` is `upgrade_rollback_probe_endpoint`); it refuses a probe that
  addresses this Manager's own listener before the request is sent
  (`upgrade_rollback_probe_self`): the 1.x and the v2 Manager state the same
  service identity, so an answer from the port this Manager holds describes the
  v2 stack, never a restored volume. The probe target's port is compared with the
  Manager's own port (`--manager-addr`, or the configured `HTTP_ADDR`), and the
  host must not resolve to this machine — a loopback, wildcard, interface or
  hostname address is refused, including a container-local one. It also refuses
  a verdict while the v2 stack still answers on the address the operator named
  (`upgrade_rollback_v2_running`): the verdict says that stack was abandoned. It
  then reads the live 1.x database through a private snapshot of the database
  and its WAL sidecars and runs `quick_check` on that copy — never an in-place
  open and never `immutable=1`, which cannot see rows a live v1 has committed to
  its WAL — retrying the bounded snapshot budget when the copy races v1 writes.
  No exclusive 1.x source lock is taken: v1 is expected to be serving. It reads
  the recorded backup snapshot read-only, re-verifies its manifest digest,
  and requires the live volume to still retain at least the rows the backup
  holds — raw usage events counted together with the append-only archive
  projection — and every archived segment and the data key the backup describes
  (`upgrade_rollback_identity_foreign` on any difference,
  `upgrade_rollback_unproven` when a store cannot be opened). It writes the
  stage and the evidence (image, its source and form, health endpoint, observed
  fingerprint, observation time) in one transaction through
  `upgradeoperation.RecordRollback`, which never rewrites `source_kind`,
  `source_root`, both fingerprints, `backup_manifest_sha256` or the completed
  domains; a second verdict is `upgrade_rollback_not_applicable`. Stage
  eligibility is decided again inside that transaction, against the row the
  transaction holds: `preflight` and `failed` are refused as
  `upgrade_rollback_not_applicable` even if the service precheck saw a later
  stage, because a copy-in retry can park a row at `failed` after that read and
  a preflight row never copied a root. The recorded
  observation time is the service's own clock reading after its proofs, not the
  caller's label; a supplied time outside the freshness window is still
  refused (`upgrade_rollback_evidence_stale`), and a zero `observedAtMs` leaves
  the moment to the service, so a programmatic caller that proves the install
  without labelling a time is not refused. The observed fingerprint is
  informational: the restarted 1.x manager keeps writing rows, so byte equality
  with the recorded before-fingerprint is neither required nor recorded as one.
- **CLI.** `upgrade rollback --prepare --legacy <dir> [--legacy-image <ref>]
  [--project <name>] [--volume <name|host-directory>] [--port N]
  [--manager-addr HOST:PORT] [--backup <dir>]
  [--allow-image-family-mismatch] [--json]` prints the plan and exits 0, or 1
  with the typed refusal written to stderr exactly once; an untyped environment
  or usage failure exits 2. `--manager-addr` defaults to the configured
  `HTTP_ADDR`, so the plan's manager port and the probe comparison describe the
  listener the Manager really holds; the printed verify command uses
  single-quoted shell words (Go quoting would leave `$` and backticks live to
  the shell), names
  `--target`, `--backup`, `--legacy-image` and `--status-url` at the plan's own
  port, and carries `--manager-addr`. `upgrade rollback --verify --legacy <dir>
  [--backup <dir>] [--status-url <url>] [--manager-addr HOST:PORT]
  [--legacy-image <ref>] [--json]` passes the observation to the service, which
  probes the default `http://127.0.0.1:18318/health` unless a URL is given, then
  records; unproven evidence exits 1, usage and environment errors 2.
- **The verdict cannot exist without its evidence.** `model.Validate` requires
  `Rollback` whenever the stage is `rolled_back`, and the repository enforces the
  same invariant at both stage-writing paths: `Put` refuses such a row, and
  `Advance` refuses the terminal stage outright
  (`upgradeoperation.ErrRollbackEvidenceRequired`) because it carries no
  observation. The verdict is only ever written by
  `upgradeoperation.RecordRollback`, stage and evidence in one transaction.
- **A store this build has not opened says so, and names a command that works.**
  `upgrade status` and `upgrade rollback --prepare` read the operation row through
  a read-only open, which cannot migrate, and a Manager admitted only for the
  Upgrade surface never opens the store writable. A store whose
  `upgrade_operations` table predates the current columns is therefore reported as
  `upgrade_store_schema_outdated` (naming the missing columns) instead of
  surfacing raw SQL, and the command exits 1. The remedy the refusal prints is
  `cpa-manager-plus upgrade migrate [--db-path PATH]`: it takes the v2
  data-root process lock first, proves from a private snapshot that the store
  holds the row the copy-in records (`source_kind` `upgraded-legacy`,
  `domains_done` contains `source_copy`, both fingerprints and the backup
  manifest), then opens the store writable once, adds the columns, and is
  idempotent. Any other store — a 1.x store, or a native store with no recorded
  copy-in — is refused as `upgrade_migrate_not_copied_in` without a writable
  open, because that open would migrate it into a store this build admits as
  native without the Upgrade evidence; its bytes are unchanged. Because the lock
  comes first, a store a running Manager still owns always reports
  `upgrade_store_locked` — never a `upgrade_source_changed` from a snapshot that
  raced that writer, a code that describes the 1.x source rather than the lock.
- **A verdict without its evidence is the same outdated store.** A row that
  names `rolled_back` and none — or only part — of its evidence is reported as
  `upgrade_store_schema_outdated` too, with the same code and the same exit 1
  from both `upgrade status` and `upgrade rollback --prepare`: no write path of
  this build records the stage without its observation, so such a row comes from
  a build before the evidence columns, and a writable open only adds columns. The
  copy therefore promises no repair; it says the operation cannot be read as
  recorded and names the only real path: copy the 1.x source into a new v2 data
  root with `upgrade run` and decide against that record. A test reopens the
  store writable and re-reads it to keep that claim true.
- **`upgrade run` stops at the copy-in.** It performs preflight plus copy-in,
  never reconciliation, and records the 1.x data directory it read so a later
  rollback can bind its volume. It prints exactly
  `cpa-manager-plus upgrade status --db-path <v2 database>` and says that
  reconciliation is a separate authorised step this build does not ship; it
  names no subcommand this build cannot dispatch. A recorded stage beyond
  `copy_in` — including a terminal `rolled_back` — is reported as-is with no
  write and no next-step hint. The guard that reads that row never opens the
  data root in place: it skips the read only for a materializing marker (that
  target belongs to the resume, which compares members by file digest), and
  reads every other state — no marker, a migrating marker, or a marker that
  outlived its committed row — through a private snapshot. A terminal row is
  therefore reported without a writable open even when a stale marker survives
  it, and that marker is left in place: only the run that finishes the copy-in
  removes it. A blocked preflight still writes nothing.
- **The 1.x volume is never written** by any path above: both the health probe
  and the database probe are read-only, the live database is read through a
  private snapshot copied into validated scratch space outside both the 1.x
  source and the v2 data root (the OS temporary directory by default), so not
  even a WAL index appears beside it, and the source manifest is unchanged by
  prepare, verify and every refusal. A scratch directory inside either root —
  including a default `TMPDIR` that points there — is refused with
  `upgrade_snapshot_failed` before a directory is created, because the snapshot
  copies the whole database. The same check covers the private snapshot
  `upgrade migrate` uses to prove the store was copied in. The copy costs
  scratch space and time proportional to the database size, and it is retried
  the shared bounded number of times when it races v1 writes.
- **Known limits of the rollback evidence.** These are stated here so no reader
  infers a proof the code does not make:
  - A probe that reaches this Manager through a reverse proxy or tunnel in front
    of the 1.x port is not detected. The self-probe refusal compares the probe
    address and port with this Manager's listener; a proxy that forwards the
    health path to v2 defeats that comparison. Point `--status-url` at the 1.x
    container port directly, with no proxy between.
  - An evidence-less `rolled_back` row written by an earlier build cannot be
    repaired in place; it reads as an outdated store and the only real path is a
    fresh copy-in into a new v2 data root.
  - A bind-mode plan records the path the CLI itself sees. `--legacy` and
    `--volume <host-directory>` are resolved in whatever filesystem namespace
    the CLI runs in; under `docker compose run --rm` that is the run container,
    not the host. The plan writes the recorded path as the compose bind source
    (`<dir>:/data`), so if the CLI container sees the 1.x data at a different
    absolute path than the host mount, the plan asks the host to bind a
    directory that may be empty, v1 can start and answer `/health`, and verify
    reads the CLI container's own copy instead of the host volume. Run
    `--prepare` and `--verify` with the 1.x data mounted at the same absolute
    path the host compose will mount, or use the named-volume form. The `stat`
    precondition checks the recorded directory on the host and remains the
    operator's proof that this path is the host's 1.x volume; it cannot repair a
    path mismatch the CLI cannot see.
  - The port and v2-running checks describe the CLI's own network namespace, so
    under `docker compose run --rm` the operator reproduces them on the host.
