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
- **Preflight** (`internal/service/upgrade.Preflight`) is read-only. It opens
  `usage.sqlite` with `mode=ro` plus `query_only` and runs `quick_check`, and
  writes nothing — no WAL, no migration, no journal. It does not take the
  source's ownership fence (see U1-7): a reader must stay readable on a source
  mounted read-only, and the copy-in re-verifies the fingerprint under its own
  exclusive lock before it writes.
- **Checks, in order:** `source_ownership` (`upgrade_source_missing` when the
  mount holds no database at all, `upgrade_source_busy` when a live writer holds
  the marker, `upgrade_source_lock_unavailable` when the marker cannot be
  inspected at all, `upgrade_source_symlinked` when the database path is a
  symbolic link, `upgrade_source_unreadable` when the database or the
  integrity check fails), `source_version`
  (`upgrade_source_version_unsupported`), `fingerprint`, `disk`
  (`upgrade_disk_insufficient`), `cpa_classification`, `parity`. Parity gaps
  (missing/mismatched projection, past watermark, per-day deleted coverage
  disagreement) never block; they are reported for a later explicit decision.
  The database file policy is shared with the credential read (see U1-7): the
  file that is inspected has to be the file that is read, so a linked
  `usage.sqlite` is refused by both paths with `upgrade_source_symlinked`, and
  the fingerprint step adds its own refusal because it hashes plain files.
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


## U1-7: the Upgrade wizard's two read routes

`internal/http/controller/upgradewizard` serves `installation/upgrade/preflight`
and `installation/upgrade/status`. The fixed Bootstrap API carries them before
route cutover, the canonical continuation carries them before completion, and a
`ProductComplete` installation answers 404: the reads end with the operation.
They never appear on both surfaces at once, and no write route exists.

- **Credential.** The Upgrade surface has its own identity — the source
  administrator key the bootstrap Upgrade entry verified — presented as
  `Authorization: Bearer <key>` with exactly one header value. The handler
  verifies it through `upgrade.Service.VerifyAdmin`, which opens the configured
  1.x source read-only and runs `setting.LoadAdminCredential` +
  `security.VerifyAdminKey`. That open reads the credential row and nothing else:
  no `quick_check`, no schema vocabulary, no parity, bounded to
  `CredentialVerifyTimeout` (2s). Every authenticated request runs it, including
  every status poll, so a whole-file scan there would answer 503 on a large
  source and the result page would never arrive. The integrity check stays in
  `Preflight`, which is what qualifies a source. `VerifyAdmin` takes no fence.
  A read that outlives the bound — or a caller whose own deadline had passed —
  is `503 upgrade_credential_timeout`: a slow but healthy volume is a retry, not
  a repair.
  The installation session, the Fresh Admin key, a cookie and an absent header
  are all `401`; the two surfaces no longer check a session before this handler
  runs, so no missing decision can read as allow.
- **One file policy on both paths.** The credential read applies the same
  database file rules as the preflight (`processlock.InspectDatabaseFile`): the
  path must be the database itself, never a symbolic link, and a regular file
  reachable by exactly one link. A linked `usage.sqlite` is therefore
  `503 upgrade_source_symlinked` here and `upgrade_source_symlinked` in the
  report — one condition, one code. SQLite opens lazily on the first query, so
  the open also pins a descriptor and compares it with the inspected file
  (`processlock.OpenedDatabase`, the reader's form of `openedMarker`); after the
  credential row is read the path is compared once more, so a swap between the
  inspection and the read is refused (`ErrSourceDatabaseChanged`, reported as
  `upgrade_source_unreadable`) instead of comparing a credential from a file
  nothing assessed.
- **Order.** The credential is answered before the path and before the verb, on
  both the entry leaf and these two routes: an unauthenticated caller receives
  `401` whatever its method and whatever the path, so it cannot tell a real
  route from an unknown one or learn which method a route takes. Only a proved
  credential sees `405` (`Allow: GET`) or `404`.
- **Failure order.** `401` is reserved for a credential verdict. A missing or
  unusable mount answers `503 upgrade_legacy_root_missing` /
  `upgrade_legacy_root_unusable`; a mount with no database answers
  `503 upgrade_source_missing`; a credential read that exceeds its bound answers
  `503 upgrade_credential_timeout`; a linked database answers
  `503 upgrade_source_symlinked`; a source that exists but cannot be read to
  verify the key answers `503 upgrade_source_unreadable`. None of these is a
  wrong-password message, and none of them is the ownership-marker code — that
  code is the preflight's, and this surface never inspects the marker.
- **Ownership without the fence.** `upgrade.Preflight` observes the source with
  `processlock.ProbeExisting`: a shared, non-blocking lock on the existing
  marker. A live writer is `upgrade_source_busy`; a symbolic database link is
  the shared file policy's `upgrade_source_symlinked` (the same code the
  credential read returns); a marker that cannot be inspected at all (missing,
  unreadable, not a regular file, or a hard-linked database) is the distinct
  `upgrade_source_lock_unavailable`. A read-only source
  tree therefore yields a real report, two concurrent preflights both reach a
  verdict, and the before/after fingerprint gate in `upgrade run` — which holds
  the exclusive lock — remains the write path's guard.
- **Projection.** The status route selects named columns (stage, domains,
  source kind/version, reconciliation days, manual items, error code/detail,
  updated time) and maps the stage onto `nextAction`. An unknown stage is shown
  verbatim; fingerprints, digests and paths have no field at all.
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
and nothing is compared. The database and its `-wal`/`-shm`/`-journal` sidecars
are then copied into `<TargetRoot>/.upgrade-reconcile` and the *copy* is opened
read-write with `query_only`, so SQLite joins a WAL there. Opening the original,
even read-only, would create a WAL index beside it and change the fingerprint
the copy-in recorded. The scratch area is `<ScratchDir>/.upgrade-reconcile`
(the v2 data root by default): only that tool-owned subdirectory is created and
removed, so the directory the caller named is never the path the step deletes.
The named root must not be, sit inside, or contain the source root, and the copy
itself is removed on every return path — a clean comparison, a mismatch, or a
failure before the comparison finished — so nothing but the report (counts and
days, never rows) survives the step.

### The four compared domains

| Domain | Query | Match rule |
| --- | --- | --- |
| `retained` | `usage_events` grouped by UTC hour, `model` and `coalesce(api_key_hash, '')`: `count(*)`, `sum(total_tokens)`, `sum(failed)` | every hour bucket and every per-key, per-model total equal |
| `pricing` | `usage_pricing_hourly_rollups_v1` per stored row (hour bucket and the whole identity, `failed` included): calls, every token class and cost | every row and every column equal; cost is recomputed per side |
| `coverage` | `usage_archive_deleted_coverage_daily`: per-day `deleted_event_count`, `min_timestamp_ms`, `max_timestamp_ms` | all three equal per day |
| `history` | `usage_monitoring_account_daily_rollups_v1` per day, `account_snapshot`, `auth_label_snapshot`, `auth_file_snapshot`, `provider`, `coalesce(api_key_hash, '')`, `executor_type` and `model` | calls and tokens equal per identity |

Each query groups by exactly the expression it selects, so one reported key is
one row: a NULL and an empty `api_key_hash` are the same "no key" and are summed
into one bucket instead of returning two groups that overwrite each other in the
cell map.

The pricing table stores no cost, so cost is recomputed on both sides with
`pricing.CostForModelCandidatesWithServiceTier`; a price change between the two
stores is therefore visible instead of invisible, and it is compared exactly: the
same inputs produce the same integer micro sum, so every difference is a
difference and no relative tolerance may hide one. Comparing at the granularity
each read path exposes is what makes a permutation inside a day visible instead
of cancelling out in a day sum: a bucket that moved an hour, two `failed` rows
trading their calls, a rewritten API key, provider or model, or rewritten
coverage edges are reported in the column that moved (`bucket_ms`, `calls`,
`api_key_hash`, `min_timestamp_ms`, …), on the row they moved on.

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
  recomputed from anything else.

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
- `--acknowledge-parity-gaps` accepts whatever the parity detection reports. The
  days it accepted are still written into the evidence.

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
`--acknowledge-parity-gaps` covers only what the parity detection reports. The
report lists the days under the domain's `recoverableDays` and repeats them in
`unacknowledgedDays`, so the operator acknowledges exactly the days the run
measured, and a day listed in `unrecoverableDays` cannot be acknowledged at all.

The marks, the evidence JSON in `upgrade_operations.reconciliation` and the
`Advance` over the operation domains the comparison actually established —
`retained_projection` for the retained events and the account rollups derived
from them, `pricing`, `coverage` — to stage `reconcile` are written in one
transaction, always with `Advance`, never `Put`: `Put` rebuilds the row and would
clear the provenance `complete` requires. A reader therefore never sees marks
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

`cpa-manager-plus upgrade reconcile --legacy <dir> [--target <dir>]
[--bundle <dir>] [--json] [--acknowledge <day>]… [--acknowledge-parity-gaps]`
runs the preflight gate first, then the reconciliation. Exit 0 reconciled; exit 1
is a refusal the operator has to look at — a blocked preflight, an
unacknowledged mismatch (`upgrade_reconcile_mismatch`), a store that cannot show
what the source holds (`upgrade_reconcile_target_incomplete`), or a run that
compared nothing (`upgrade_reconcile_nothing_compared`); exit 2 is a usage or
environment error. Every refusal prints the report with its typed `errorCode`
first (the report is printed either way), so the exit code only says the
migration did not advance and the code says why. The `--bundle` default is the
recorded copy-in's backup destination, and the recorded copy-in is re-verified
against it before anything is compared.
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
  Manager publishes, because both managers answer `GET /health` with the same
  identity. A plan whose port is the Manager's own is
  `upgrade_rollback_port_collision`; a plan whose port is already held on the
  host is `upgrade_rollback_port_busy` (the port is bound on every local address
  and both wildcards, so a listener on any interface is found); with
  `--manager-addr` the plan also refuses while the v2 stack still answers there
  (`upgrade_rollback_v2_running`). A refusal writes nothing.
- **The plan** (`<v2 data root>/.upgrade-rollback/plan.json`, canonical-JSON
  SHA-256 in `digest`) names the pinned image, the volume derived from the
  compose project and the mountpoint the 1.x container uses, the recorded source
  root, the published `port` and the v2 Manager `managerPort` beside it, the
  recorded `backupDirectory`, the exact `docker compose -p cpamp -f - up - d
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
  Manager's own port (`--manager-addr`, or the documented default), and the host
  must not resolve to this machine — a loopback, wildcard, interface or
  hostname address is refused, including a container-local one. It also refuses
  a verdict while the v2 stack still answers on the address the operator named
  (`upgrade_rollback_v2_running`): the verdict says that stack was abandoned. It
  then opens the live 1.x database immutable read-only with a `quick_check`; it
  reads the recorded backup snapshot read-only, re-verifies its manifest digest,
  and requires the live volume to still retain at least the rows the backup
  holds — raw usage events counted together with the append-only archive
  projection — and every archived segment and the data key the backup describes
  (`upgrade_rollback_identity_foreign` on any difference,
  `upgrade_rollback_unproven` when a store cannot be opened). It writes the
  stage and the evidence (image, health endpoint, observed fingerprint,
  observation time) in one transaction through
  `upgradeoperation.RecordRollback`, which never rewrites `source_kind`,
  `source_root`, both fingerprints, `backup_manifest_sha256` or the completed
  domains; a second verdict is `upgrade_rollback_not_applicable`. The recorded
  observation time is the service's own clock reading after its proofs, not the
  caller's label; a caller-supplied time outside the freshness window is still
  refused (`upgrade_rollback_evidence_stale`). The observed fingerprint is
  informational: the restarted 1.x manager keeps writing rows, so byte equality
  with the recorded before-fingerprint is neither required nor recorded as one.
- **CLI.** `upgrade rollback --prepare --legacy <dir> [--legacy-image <ref>]
  [--project <name>] [--port N] [--manager-addr HOST:PORT] [--backup <dir>]
  [--allow-image-family-mismatch] [--json]` prints the plan and exits 0, or 1
  with the typed refusal written to stderr; the printed verify command is
  quoted, names `--target`, `--backup`, `--legacy-image` and `--status-url` at the
  plan's own port, and carries `--manager-addr` when the plan was derived against
  one. `upgrade rollback --verify --legacy <dir> [--backup <dir>]
  [--status-url <url>] [--manager-addr HOST:PORT] [--legacy-image <ref>]
  [--json]` passes the observation to the service, which probes the default
  `http://127.0.0.1:18318/health` unless a URL is given, then records; unproven
  evidence exits 1, usage errors 2.
- **The verdict cannot exist without its evidence.** `model.Validate` requires
  `Rollback` whenever the stage is `rolled_back`, and the repository enforces the
  same invariant at both stage-writing paths: `Put` refuses such a row, and
  `Advance` refuses the terminal stage outright
  (`upgradeoperation.ErrRollbackEvidenceRequired`) because it carries no
  observation. The verdict is only ever written by
  `upgradeoperation.RecordRollback`, stage and evidence in one transaction.
- **A store this build has not opened says so.** `upgrade status` and `upgrade
  rollback --prepare` read the operation row through a read-only open, which
  cannot migrate. A store whose `upgrade_operations` table predates the current
  columns is reported as `upgrade_store_schema_outdated` (naming the missing
  columns and the writable open that adds them) instead of surfacing raw SQL, and
  the command exits 1.
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
