# Phase4-04A Upgrade source and backup preconditions

Product baseline: `v2@fca97ed253be8c87659278ae06e5ec7a35dd0154`.
Governing Plan: [cpamp-plan PR #24](https://github.com/seakee/cpamp-plan/pull/24),
merged; task and local prompt read from Plan main
`8c52e55657c40cccc8f524680055a21ea88fd07a`.

## Existing assets and bounded changes

Paths below are relative to `apps/manager-server/`.

| Asset | Reuse and actual gap |
| --- | --- |
| `internal/service/legacypreflight/service.go`: `inspect`, `AdmitStartup` | Reuse read-before-write classification. Startup remains unchanged and rejects legacy sources. |
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
- **Deferred:** v1.14.10 re-pin and its qualification evidence (U1-1b), the
  copy-in entry, reconciliation execution and CPA import (U1-2), and route
  cutover/rollback (U1-3).
