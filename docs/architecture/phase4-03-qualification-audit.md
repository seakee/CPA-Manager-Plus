# Phase4-03 production prerequisite audit — 2026-10-17

Product: #995, audited implementation `20d00aaa232c32758484230c5968290e2dde6129`.
Plan: [seakee/cpamp-plan#23](https://github.com/seakee/cpamp-plan/pull/23),
merged plan tree `5558a10f68cce29a1f77873b2120b92bf518a2f4`,
`active/task-v2-phase4-03-production-prerequisite-closure.zh-CN.md`.

**Qualification Gate: BLOCKED. Adopt destination Gate: BLOCKED.**
This is an evidence audit, not a qualification authority or an acceptance record.
The plan's independent acceptance approves implementation scope; it does not
accept the product's CPA artifact/profile or any resource replay capability.

## Web-head reconciliation — 2026-10-17

The original #995 worktree was clean and fast-forwarded from
`d2493f31f2a67bcf6fad978e4c1be3d04ff23c9f` to the live branch head
`983055163b32ae9db45ec01dd157e149035de265`. The Web commit tightens credential
replay and connection-secret recovery; it adds no qualification acceptance or
destination primitive. All five workflows passed on that exact head. The
maintainer separately confirmed there is no additional independent acceptance
record or actual adoption read/replay/verify evidence for this batch.

The four original reports in the binding table below were revalidated with
`validateEvidence`, including their referenced traces. Their report and manifest
hashes still match the table. Both r3/r4 tool-identity files also match all six
recorded source hashes at execution HEAD `7d8c98cb...`. The r5 catalogue and
historical independent-review hashes above/below are unchanged. These checks
establish retained integrity only; no candidate was rerun or newly accepted.

The next acceptance work is bounded by the actual consumer, rather than the
whole AQ/Beta matrix:

| Consumer | Minimum independent decision still needed | Exact retained facts that cannot substitute for it |
| --- | --- | --- |
| New Managed | Accept the exact archive/binary/Runtime ArtifactID for this Fresh consumer, with applicable platform/config/mode limitations, evidence hashes, implementation heads and invalidation conditions | r2 normal/expiry and r3/r4 bind the bytes, but the historical review does not grant Fresh production-consumer acceptance |
| Connect External | Accept the subset used by the single authenticated `GET /v8/management/config`, including the adapter's direct transport, URL/redirect/TLS/DNS/timeout/body/auth boundaries and explicit target/config/mode applicability | In the retained r3 control, only `AQ03-E01-real-auth` and `AQ03-E01-real-config` are `supported`; `AQ03-E02-real-auth`, `AQ03-E02-real-config` and missing/wrong-auth leaves are `limited`; root/prefix, private/remote, IPv6 and mediation leaves are `unknown` |
| Adopt `network_existing` | Accept its source-read subset separately, and name at least the bounded resource set with exact read/replay/readback/effective-state evidence before authorizing `Migrate` | External config observation drops values and grants no replay/write authority. No inspected record accepts a resource for this consumer's destination replay/verify |
| Adopt `legacy_managed` | The same resource decisions plus independent acceptance of the actual legacy-to-new-destination staging-v8 transition, preserving the source | r4 `AQ04-S02`–`S10` and `S11-reconcile` remain `limited`; `S01-snapshot`, `S11-lost-reply`, `S12-precommit`, `S13-restore`, `S14-fence`, `S15-tamper` and `S16-crash` remain `unknown` |

This table is a review handoff, not a new profile definition or qualification
ledger. Applicability must be explicit; accepting an External read does not
accept network adoption, and network adoption does not accept a legacy
transition. The current `ReadV8Config` observation is not a resource snapshot.
Supervisor artifact staging owns executable bytes, not a separately configured
adoption destination, resource replay or semantic verification. Neither can be
relabelled as the missing primitive.

At that head, the evidence-only batch added neither a production consumer
record/loader nor speculative destination orchestration. The existing
typed blockers and rejection of Adopt durable checkpoint shapes remain intact.
Both gates remain **BLOCKED**, successful destination failure/restart/lost-reply
acceptance remains unperformed, and #995 remains **Draft / NO-GO**.

## Build-bound loader implementation follow-up

The subsequent implementation adds a strict loader and wires production Fresh
construction through `freshstrategy.NewProduction`. Its sole compiled authority
is [`qualification/consumer.json`](../../apps/manager-server/internal/service/freshstrategy/qualification/consumer.json).
No existing immutable manifest can carry accepted Fresh consumer facts without
widening its fixture/evidence trust semantics, so this is the plan's minimal
consumer-record exception. The original AQ hashes/HEADs are references, not a
second AQ ledger. All four consumer entries remain **pending**; no independently
accepted facts or resource set have been invented.

The loader rejects malformed/noncanonical records, unknown/missing/duplicate
fields, duplicate consumers, invalid artifact digests, incomplete legacy profile
bindings and unsupported consumer/profile combinations. Accepted entries would
additionally require the actual byte-pinned original manifest, report, selected
sanitized traces and independent review inside the binary, plus explicit case
sets, accepted limitations and invalidation conditions. It checks exact observed
archive/binary hashes and resulting ArtifactID, manifest/report/profile/case/run
bindings and the original evidence runner HEAD. It does not replace the existing
AQ validator or decide whether a review's applicability/limitations are adequate.

An acceptance also pins the compiled Fresh loader/strategy and v8 safe-read
implementation digest. This is distinct from original evidence execution HEADs;
it invalidates qualification when consumer code changes without trying to store
a commit's own SHA inside that same commit. The canonical consumer record and
byte-pinned implementation/evidence files have explicit Windows Git attributes.
The implementation digest remains empty while no consumer is accepted.

Production construction discards injectable qualification and selects only the
exact strategy/source-class entry. Accepted External safe-read cannot promote
network adoption, and neither can promote a legacy transition. Synthetic
accepted evidence exists only in test memory. Regression tests cover bindings,
withdrawal, cross-consumer rejection, production injection rejection, the real
HTTP/reopen boundary and the existing exact Runtime artifact fence.

At the loader-only baseline, **Qualification Gate: BLOCKED by independent
production acceptance**, with the loader mechanism implemented. **Adopt
destination Gate: BLOCKED** at that point lacked both an accepted migratable
native resource set and a destination primitive. The implementation follow-up
below supplies the bounded primitive; the missing acceptance still blocks
production before any source request or destination mutation.

Local loader verification passed: Manager `go test -count=1 ./...` and full
`go vet ./...`; affected Bootstrap/Fresh/store/CPA/routing/HTTP/command race
suites; Supervisor and Ingress full race/vet; Web type-check and 55 workflow/
classifier tests. Docker's daemon is unavailable locally. Docker/Windows
execution must be verified in existing CI on the pushed implementation head;
earlier heads' green checks do not qualify this implementation or its evidence.

## Evidence and independent acceptance are separate

The Adopt implementation follow-up (baseline `38d1a7ec`) adds actual bounded
destination execution for **client API keys only**, using the existing Fresh
store/OperationID and Runtime/Supervisor journal. It does not accept any consumer.
The exact artifact and original immutable AQ bindings below are unchanged. The
network entry additionally references the same original r4 staging manifest/report
already used by legacy adoption; no copied manifest or second qualification
ledger was created. A resource-specific read/replay/verify case mapping in the
same consumer record is required before `Migrate`; External safe-read acceptance
cannot supply this mapping.

Reviewable checks now include source preservation; protected snapshot/CAS/reopen;
destination replay/readback and actual client-auth checks; conflicting source,
destination, artifact, Runtime identity/generation; partial journal persistence;
and lost successful stage/Start responses. The existing PR workflow downloads
the exact original Linux amd64 archive, checks both SHA-256 values, builds the
current Supervisor, and runs real-process tests for both source classes with race.
It uploads only sanitized JSONL and source/tested HEAD/artifact metadata as
`fresh-adoption-v8-<run>-<attempt>`. These observations require independent review;
they are not inserted into the production consumer record or represented as a
new AQ acceptance report. A local native build of exact CPA source exercises
target config/start/auth behavior but is not the qualified Linux artifact.

At that baseline the controlled `legacy_managed` case used the exact v8 binary
with legacy config spelling. It did **not** prove transition from a real
historical legacy runtime; the follow-up below replaces that shortcut with a
live v7.0.12 source and an explicit legacy read. Independent acceptance of the
resulting transition evidence remains an explicit legacy blocker. New Managed
and External production-consumer acceptance also remain absent. Both Gate
decisions at the top therefore remain **BLOCKED**, even when implementation/CI
tests pass.

| Consumer fact | Existing evidence | Independently accepted scope | Unaccepted or missing prerequisite |
| --- | --- | --- | --- |
| New Managed exact v8 artifact | r2 normal/expiry and r3/r4 reports observe the same Linux amd64 plugin archive and executable | Historical independent review accepts exact locally verified bytes and their recorded official metadata binding | No accepted New Managed production-consumer qualification or profile/limitations/invalidation binding in the implemented build-bound loader |
| Connect External required v8 safe-read profile | r3 control run, `CPA8-EXTERNAL-SAFETY-r1`; real auth/config leaves exist | Historical review accepts bounded real config/auth and harness/denial engineering facts | No independently accepted production safe-read subset for the actual supported target/transport/config/mode combinations; deployment/proxy applicability remains unaccepted |
| Adopt `network_existing` | Same safe-read facts; bounded client-key destination implementation and controlled real-process CI checks | No independent acceptance of native-resource read/replay/verify for this Fresh consumer | Required profile acceptance and independently accepted client-key read/replay/readback/effective-state evidence are missing |
| Adopt `legacy_managed` | r4 `CPA8-STAGE-r1`; synthetic snapshots, startup writes and conditional restore observations | Historical review accepts only those bounded synthetic engineering facts | Required profile **and** separately accepted staging-v8 transition are missing; real legacy CPA source-to-destination transition/replay/verify is not established |

The historical independent review is
`bin/tmp/pre4a-exit/20261009-entry-review-3B89KD/independent-review.zh-CN.md`
in the main checkout, SHA-256
`ae378022a8d32455c9a3b36f2b5ddb8e1eb18e9d1ee9072171429cdf11706da5`.
It identifies its independent reviewer, exact baseline and original reports;
its X04/X06/X08 conclusions distinguish accepted engineering observations from
unaccepted profiles, actual External applicability and actual migration.
It explicitly records CPA qualification and public Beta as No-Go. Later limited
engineering-entry permission does not turn those facts into qualified profiles.

The live Phase4A compatibility ledger was also read at plan tree `5558a10f...`:
`active/issue-v2-phase4a-01-compatibility-ledger.zh-CN.md`. Its independent source
assessment concerns capability integration and explicitly retains CPA/Beta
No-Go. The archived #944 acceptance concerns the legacy schema classifier and
Bridge observations; it does not accept the full Phase4A-03 exit or a CPA v8
transition. The fixed v1.14.4 writer archive pins in `legacyartifact` identify a
**Manager writer**, not a CPA executable or a Fresh adoption profile.

## Exact original bindings

Current checked-in r5 catalogue:
`tests/fixtures/cpa-v8-qualification/manifest.json`, SHA-256
`47154799753813e47b0a159685bb3ede25b250de40dafe7991a18bb96e0f9980`.
Its candidate is declared, and its schema contains operations, profiles, fixtures
and cases, not production acceptance. Its adjacent README explicitly says
`qualification: not accepted` and `acceptedProductionConsumer: pending`.

All four accessible original reports below observe archive SHA-256
`50ecffb47fdd81c8c5a9825a73a7a905ab66342337e274f39c4276b92d3533f3`
and executable SHA-256
`b682e9e42586263f476888361514f68f89ff4ebf89a5dadba394b850b797ce41`.
Candidate: `CPA8-8.0.13-linux-amd64-plugin`, source
`d7914afdedca7af95ee974a42453dc49fc1388ce`.
Runtime's content-derived ID for those executable bytes would be
`sha256:b682e9e42586263f476888361514f68f89ff4ebf89a5dadba394b850b797ce41`;
this derivation grants no qualification. No actual destination Runtime was
observed or accepted in this audit, and the candidate archive was not rerun.

Original directories are relative to the main checkout's
`bin/tmp/pre4a-exit/20261008-1c3l3os7/`. These local artifacts are not included
in this product commit and must remain accessible for any future acceptance.

| Evidence | Original directory | Exact execution HEAD | Report SHA-256 | Manifest SHA-256 |
| --- | --- | --- | --- | --- |
| r2 normal **rerun** | `aq02-rerun/normal/output/run-VeocP0` | `27f4bf80dc7cbbca90c07cc2d3853be7c5ee9902` | `5481b5ca7ce53175bc5199f32cd6a68f5a1f29c302c9cbf3465a1a62ccfe0d92` | `758fafa41bbbf6ce58961c0a3ac3beedab2af29b18631bef3ffb4064dd3955f2` |
| r2 expiry **rerun** | `aq02-rerun/expiry/output/run-haHpzr` | `27f4bf80dc7cbbca90c07cc2d3853be7c5ee9902` | `c0cef73919b9072e76072a837603044e6381d17d9bf70fb07fdd24a09ddb0ac3` | `758fafa41bbbf6ce58961c0a3ac3beedab2af29b18631bef3ffb4064dd3955f2` |
| r3 recovered **control run** | `originals/r3-control` | `7d8c98cb93a4ca9d405f2f91504348d439655f59` | `ce1e806cf8f5a676c97c7337bef3618b2cdf1213b9b33827a90d2e3a0141e779` | `75f79bc124a5a712ac74f162b0ab8d434438863b295a79de60e92a8d7c5523ea` |
| r4 original recovered run | `originals/r4-usage-stage` | `7d8c98cb93a4ca9d405f2f91504348d439655f59` | `6add816401dad04a7e29285eece43e30abcef6625313a8e492ae24b4ef079a7c` | `3c21eb7db0d45638b9be81891277329b0c04ea941588dc90213d6d864f34896f` |

This audit recalculated these hashes, validated every report and referenced
trace with the existing `validateEvidence`, and compared recorded tool source
hashes with `git show <execution HEAD>:<source path>`. All bindings match.
This verifies retained evidence integrity, not its acceptance or applicability.
The older r2 normal/expiry and original r3 report locators in the fixture README
were not found in the inspected local evidence directories. The reruns/control
run have their own identities and must never be presented as those older runs.

r3 has two supported leaves, including `AQ03-E01-real-auth` and
`AQ03-E01-real-config`; the `AQ03-E02-*` stronger coverage is limited and the
root/prefix/private/remote/IPv6/mediation applicability leaves remain unknown.
r4 stage `AQ04-S02`–`S10` and `S11-reconcile` are limited; snapshot, lost-reply,
precommit, restore, fence, tamper and crash leaves remain unknown. These facts
cannot qualify a resource as `Migrate`. Only the minimum consumer-specific
subset needs later reconciliation; this audit does not demand a whole AQ/Beta
rerun as a substitute for identifying that subset.

## Manifest reuse decision

Reuse original r2/r3/r4 immutable manifest/report hashes and implementation
heads as evidence references, preserving original identities and limitations.
The r5 catalogue can locate profile declarations but cannot carry accepted
production facts without changing its fixture-only trust semantics.

Runtime artifact observation/selection/staging manifests bind executable bytes
and, when present, source archive digests. They contain no independently accepted
management, External or transition profile and cannot supply `CPAQualification`.
Phase4A evidence manifests and the immutable SRC-11 sealed artifact bind Manager
history/bundle evidence; they do not qualify CPA resources or a CPA executable.
Their acceptance is scoped to their own case, even when a later planning record
accepts that case. Immutability alone does not widen that scope.

**No inspected existing manifest is an accepted Fresh production qualification
authority.** The initial audit added no consumer record or loader. The later
loader implementation above uses the approved minimal consumer-record exception
because no existing authoritative manifest can carry these facts. Pending
entries reference the original hashes/heads without granting qualification;
accepted cases/limitations/review bindings remain absent. This record is the
single effective source of truth, never a duplicate accepted manifest.
This document is never loaded as authority, and tests inject only synthetic
qualification to exercise existing contracts.

## Safely deliverable change and retained blockers

`legacy_managed` now requires both the required-v8 profile and staging-v8
transition. A transition flag alone previously passed the domain gate; a negative
test reproduced that gap before the fix. `network_existing` retains its separate
required-profile gate. No Phase4-04 behavior is added.

Production application wiring retains unqualified server-owned defaults and
these exact blockers before source access or destination effects:

- New Managed: `exact_v8_artifact_qualification_required`.
- Connect External: `required_v8_profile_qualification_required`.
- Both Adopt source classes: `adoption_source_qualification_required`.
- Qualified source without a destination primitive: `adoption_destination_staging_required`.
- Qualified source without independently accepted resource read/replay/verify: `adoption_resource_qualification_required`.

New regression coverage exercises real Fresh store reopen, the authenticated
strategy handler, rejected HTTP qualification fields, unchanged durable records,
zero source requests for both classes and three strategies, repeated/reconstructed
blocked adoption, and withdrawal/mismatch of qualification for a staged managed
target despite its ready state and v8 version. Existing Fresh admission, claim,
Admin, routing, exact Runtime identity/generation/artifact and retry tests remain.
The existing Windows preflight job now includes the Bootstrap/Fresh strategy
packages and the new HTTP/reopen test in its startup filter; no new job or
qualification workflow is introduced.

Destination discovery, resource decisions, replay and semantic verification are
now implemented for client API keys, with failure/restart/timeout/lost-reply
regressions and explicit real-process CI checks. These checks establish no
production acceptance by themselves. Final independent #995 acceptance is not
possible until both gates close for both source classes, including the missing
real legacy-to-staging-v8 transition evidence.

Scope check: no second qualification authority, Runtime authority, routing or
state ledger, workflow/migration framework, signatures/remote trust, history
transfer, Upgrade/rollback or product UI is introduced. Source lifecycle,
configuration, credentials, history and queues are untouched. Product behavior
changes are limited to the qualification and checkpoint/replay boundaries here.

## State-machine review follow-up

The two P0 production prerequisites above remain blocked. The subsequent review
identified two independent P1 defects, both reproduced by failing regressions:
incomplete durable strategy shapes could pass `FreshOperation.Validate`, and
the HTTP committed-replay shortcut returned success without adapter validation.

Strategy validation now recognizes only the empty initial checkpoint and the
complete shapes emitted by New Managed and Connect External. Managed records
require exact artifact identity and a positive Runtime generation; External
records require their URL fingerprint and existing generation sentinel. Unknown,
partial and currently unimplemented Adopt checkpoints fail closed. The existing
repository validator applies this on both load and update, so corrupt records
cannot reach `/complete` or be admitted after reopen. This validates structure,
not production qualification or proof of actual CPA state.

Committed HTTP retries now pass through the same strategy adapter. Managed
requests must match the observed Runtime identity/generation/artifact at every
durable stage. External URL conflicts are rejected before any source probe;
matching retries retain the bounded safe-read check. Successful committed replay
does not rewrite checkpoints, connection ciphertext or revisions, including after
product completion and real close/reopen. No second persistence authority or
general state-machine framework is added. Both new HTTP regressions and the
Fresh repository tests run in the existing Windows preflight job.

## Local verification

- Manager `go test ./...`, affected Bootstrap/Fresh/CPA/controller/processlock
  race suites (including `cmd/cpa-manager-plus`), and `go vet ./...`: PASS.
- Supervisor and Ingress `go test -race ./...` and `go vet ./...`: PASS.
- Web: 3953 tests; repository: 734 tests; workflow/classifier final check: 55
  tests; type-check, lint, build and demo isolation: PASS.
- Embedded Full Web packaged-dist test, Runtime architecture boundary and both
  Compose configurations: PASS.
- Docker daemon is unavailable locally; Docker build/E2E and Windows execution
  must be checked in the existing CI on the final pushed head. Cross-compilation
  and the previous head's green CI cannot substitute for that execution.

These checks verify the safe subset and retained blockers. They do not accept
CPA production qualification or successful destination replay/verification.

### Windows CI follow-up

At pushed head `3149fb24781cd32e73336a7a37690814729e68cf`, the added
Fresh strategy qualification tests passed on Windows. The expanded Bootstrap
suite exposed a pre-existing negative-fixture construction defect:
`TestStrictFixtureRejectsUnknownDuplicateAndMissing` removed a member by matching
an LF-terminated line. With CRLF checkout bytes the replacement was a no-op,
so it incorrectly tested the original valid fixture as a missing-field case.
The job failed and the subsequent Fresh HTTP/reopen step was not executed.

The follow-up test change removes the JSON member independently of line endings,
asserts that removal actually changed the bytes, and exercises both LF and CRLF
for missing/unknown/duplicate rejection. Both variants and the full Bootstrap
package pass locally, including race/vet. The parser, schema, production gates
and original fixture are unchanged; no test is skipped or weakened. Windows
execution of the corrected suite and the HTTP/reopen test still requires CI on
the follow-up head.

At follow-up head `6fd49ff85fbe3eba10f05b8e593bcf24565dd380`, Windows
[job 117303368962](https://github.com/seakee/CPA-Manager-Plus/actions/runs/39000801694/job/117303368962)
passed both LF/CRLF controls and the full preflight ownership/qualification step.
The newly executed HTTP/reopen test then failed at the real store reopen with
`SQL logic error: out of memory (1)`. Fresh's checkpoint reader built
`file://C:/...`, treating the Windows drive as a URI host. A platform-independent
regression reproduced that incorrect local path before the fix. The checkpoint
reader now uses the same leading-slash handling as the existing SQLite URI
builders, retaining `mode=ro`, `query_only(1)` and its busy timeout. No fallback,
test skip, writable inspection or qualification change is introduced. Windows
execution on the corrected implementation remains required before CI acceptance.
The corrected implementation passes local Manager `go test -count=1 ./...` and
`go vet ./...`, plus Fresh service/command-entry `go test -race -count=1`.

At `1545565b116f83d92f574e9cd75f0921eed2cfd6`,
[Windows job 117309262171](https://github.com/seakee/CPA-Manager-Plus/actions/runs/39002747923/job/117309262171)
then passed the complete suite, including LF/CRLF controls and the real
HTTP/reopen test. All 22 checks on that head passed. The subsequent P1
state-machine follow-up above requires verification on its own implementation
head; the earlier green CI does not qualify that new change or either P0 gate.


## Independent acceptance repair follow-up

The latest repair batch addresses the three findings from the subsequent
independent #995 review without promoting any production consumer:

- Adoption converts the bounded v8 source-section inventory into explicit
  existing-domain decisions. Client API keys are the sole `Migrate` resource;
  CPA auth files, CPA configuration and provider runtime state remain explicit
  `ManualAction` entries. A staged/committed checkpoint missing the required
  auth/config decisions fails validation.
- The real-process legacy case pins and starts an actual v7.0.12 Linux amd64
  historical source, keeps it live and byte-preserved, and starts the exact
  v8.0.13 candidate only against an isolated staging copy before destination
  replay/verification. The previous legacy-shaped-v8 shortcut is no longer
  treated as transition evidence.
- `runtime_destination_implementation.sha256` pins the bounded
  Manager-to-Supervisor destination execution source chain. A test recomputes
  the digest from actual source files, and the pin is included in
  `consumerImplementationSHA256`, so a destination/protocol/lifecycle semantic
  change invalidates any later accepted consumer record.

All four consumers deliberately remain `pending` in this repair batch. The new
exact-head CI artifact must be independently reviewed before any
`status: accepted`, case refs, acceptance hash or limitations are written.
Previous-head green CI is not reused to self-promote the changed implementation.

Scope check: no generic migration engine, second Runtime authority, dynamic
attestation/trust store, source lifecycle takeover, historical usage/request/log
migration, v0 fallback, Phase4-04 behavior or product UI is introduced.

## Legacy transition, inventory and semantic-binding follow-up

This batch closes four implementation gaps without accepting any consumer:

- `legacy_managed` adoption now reads a real pre-v8 source through a dedicated,
  explicit `GET /v0/management/config` adapter with the same direct transport,
  fixed DNS, no proxy, no redirect/retry, bounded header/body and five-second
  budget as the v8 safe read. It is not a v0 fallback: `network_existing` uses
  only the v8 probe, legacy uses only this adapter, and neither retries against
  the other generation. Only client API keys and fixed bounded resource
  categories escape the adapter.
- Both source classes normalize into one internal snapshot and persist the
  bounded `adoptionResources` inventory together with one decision per resource
  in the existing Fresh checkpoint/CAS before any destination mutation.
  `client-api-keys` is the sole `Migrate`; auth files, provider state,
  configuration sections and the fixed `cpa-config-unknown` sentinel remain
  explicit manual or unsupported actions. The inventory is immutable after the
  staged checkpoint and survives Manager/SQLite reopen.
- The production loader no longer derives legacy transition qualification from
  the source class. An accepted legacy consumer must carry a separate
  `legacyTransition` binding to a byte-pinned transition observation that binds
  the exact target ArtifactID/archive/binary, source class, consumer
  implementation digest and source-preserved/staging-v8/destination-verified
  facts. `network_existing` must not carry it; pending consumers must not either.
  All four compiled consumers remain `pending`.
- The consumer implementation digest now also covers the Bootstrap
  qualification/adoption/checkpoint semantic sources and the legacy safe-read
  adapter, with LF attributes for Windows byte equality. The exact-CPA CI job
  emits a sanitized `fresh-legacy-transition.json` from an actual v7.0.12
  process plus the Manager-owned exact v8.0.13 destination, and merges only
  exact-head/tree/platform metadata. CI PASS still writes no acceptance.

The controlled legacy case now points Fresh at the real v7 process URL and
asserts the source stays live, byte-preserved, on v0 only, that the destination
uses the source client keys with a Manager-generated management key and the
exact accepted ArtifactID, and that the committed checkpoint follows the v8
readback. Independent acceptance of this exact-head artifact is still required
before any production consumer may become `accepted`.

## Legacy source-profile and transition-authority repair

The independent review of the previous batch kept Gate A blocked on two P0s; this
repair closes them without accepting any consumer:

- The explicit legacy adapter now requires the exact accepted source profile.
  It reads `X-CPA-VERSION` from the single `GET /v0/management/config` response
  and succeeds only for the pinned CPA v7.0.12 profile (`7.0.12` in the official
  release header). Missing, malformed, other v7 releases, v8 and unknown values
  fail closed with a stable typed error before any body interpretation; no
  destination mutation occurs. The pinned profile also fixes the official Linux
  amd64 archive (`8afc9ed4...`) and executable (`1eb108b8...`) digests, and the
  real-process test verifies the downloaded executable against that constant.
  Body shape remains only an extra sanity check: exact CPA v8.0.13 still serves
  `/v0/management/config` with a legacy-shaped body, so the real test now proves
  that endpoint exists and is rejected as an unsupported source profile.
- Legacy transition authority is a complete chain instead of a standalone
  observation JSON. The new checked-in, content-addressed manifest
  `qualification/evidence/316f7452....json` defines the
  `fresh-legacy-managed-stage-v8-r1` case under `CPA8-STAGE-r1`, the exact v8
  target and the accepted v7.0.12 source profile. The loader requires a
  consumer binding with manifest/report/implementation-head/observation digests
  and verifies: manifest case/profile/source/target, report manifest/revision/
  runner head/artifact observation/completed PASS result/evidence ref, then the
  observation's source class, v7 profile, exact target, consumer implementation
  digest and the source-preserved/legacy-read/staging-v8/destination-verified/
  Gateway-pending facts. Any mismatch grants nothing. The new case is not
  inserted into or required from the retained original r4 report case refs.
- The exact-CPA CI job now emits the sanitized transition manifest copy, report
  and observation as separate content-addressed artifacts, checks that the
  report binds the emitted manifest/observation/head, and records their SHA-256
  values in the environment metadata. The Go test only writes them after every
  transition, preservation, readback and restart-fence assertion passed. CI
  still writes no acceptance; `consumer.json` remains pending for all four
  consumers.
- `legacy_transition.go` is part of `consumerImplementationSHA256` and has LF
  attributes, so a change to the transition authority contract invalidates any
  later acceptance.

Scope check: no version negotiation/range, no v0/v8 fallback, no new workflow,
no second qualification ledger, no migration engine, no Runtime/Supervisor
authority change and no Phase4-04 behavior is introduced.

## Accepted-applicability enforcement (P1)

The loader previously accepted a consumer on `status: accepted`, a pinned
review hash and any non-empty `limitations` strings. Accepted evidence can be
narrower than the implementation (target class, transport, config or mode), so
free text could not prevent an accepted qualification from being used outside
the reviewed scope. This repair makes the accepted scope machine-checkable and
keeps every consumer that needs a non-checkable restriction pending.

Phase 1 classification of applicability constraints:

1. **Already enforced by the build:** v8 management generation and config
   shape; exact CPA v8 archive/binary/ArtifactID for New Managed and Adopt;
   bounded direct safe read (no proxy/redirect/retry, 5 s, 32 KiB headers,
   1 MiB body, identity/gzip, JSON content type); exact legacy v7.0.12
   `X-CPA-VERSION` plus pinned archive/binary; client API keys as the only
   adopted resource; preserved source; Manager-owned exact-v8 destination;
   Runtime identity/generation/artifact fencing; Gateway pending; consumer
   isolation by strategy/source class; persisted Fresh operation revalidation.
2. **Made enforceable with the existing consumer record:** a closed
   `applicability` tuple (target, source transport, destination transport,
   config, mode) and a closed limitation vocabulary whose keys map to the
   checks above. An accepted consumer must declare exactly the enforced tuple
   and exactly the enforced limitation set; missing, extra, duplicate,
   free-text or unenforceable limitations fail the whole record.
3. **Not enforceable by this Manager, therefore a pending blocker:** target
   host/topology classes beyond the bounded read (loopback/private/remote,
   IPv6, reverse proxy, mediation); TLS/CA/mTLS specifics beyond normal
   verification; CPA platform/OS/arch beyond the artifact bytes; CPA config
   semantics beyond the checked schema; deployment/operational modes the
   Manager does not observe; time-bound review validity. These cannot be
   represented as accepted applicability, so a review relying on them must
   keep the consumer pending rather than record an unenforced limitation.

The loader now rejects `accepted` consumers without the exact enforced
applicability/limitations and rejects `pending` consumers that claim either.
Mode/target mismatches are tested to fail before any source read, destination
staging or Runtime mutation, including for a persisted staged operation after a
restart. All four production consumers remain `pending`: the retained evidence
does not independently accept the exact enforceable scope, legacy transition
evidence is not independently accepted, and the network-adoption resource cases
are not accepted.

Scope check: no policy engine, version range, environment probing, new ledger,
runtime authority, resource type or Phase4-04 behavior is introduced. The
change is a closed, code-derived applicability check in the existing loader.

## Adoption resource evidence contract (G5/G7 repair)

The previous `adoptionEvidence` required three case refs to exist in the
retained r4 `CPA8-STAGE-r1` report. The real CI read/replay/verify evidence is
not an r4 case, so it could not be expressed without falsely editing r4. The
contract now binds an independent, class-specific chain:

- checked-in immutable resource manifest selecting `CPA8-ADOPT-RESOURCE-r1` and
  exactly three cases with distinct proof scopes: `source-read`,
  `destination-replay`, `destination-verify`;
- per-run sanitized report binding the manifest, exact runner HEAD/tree, target
  artifact observation and three completed/passing/supported results;
- one sanitized trace per case with `runId`/`caseRef`/`sourceClass` and the
  compiled-consumer digest; replay and verify facts are separate fields, so a
  read cannot authorize replay and replay cannot authorize verification;
- class-specific source binding: network requires `sourceProfile: cpa_v8`; legacy
  requires the pinned v7.0.12 archive/binary and its own read trace.

The real CPA + Supervisor integration test emits the network and legacy resource
chains after all source/replay/readback/lost-reply/reopen/restart-fence
assertions. The legacy staging-v8 transition remains a separate checked-in
manifest/report/observation chain. A network resource chain cannot satisfy a
legacy consumer (and vice versa), and a resource chain cannot satisfy the
transition: these are four independent proofs, and the loader grants only the
fact whose exact chain validated. The r4 `AQ04-S*` cases remain background
evidence, not resource replay authority.

Checked-in manifests:

- network `qualification/evidence/563ef299810ce576636df6bd5ebf39a0554d54e5b8bde31b8606405c4d2ac3be.json`
- legacy `qualification/evidence/7388022d21d2d6502449c311bc5379e4e954db0030912e73a2607d809fefb5f2.json`

The loader rejects missing/mismatched manifest, report, head, caseRef, trace,
proof scope, source class/profile, target artifact, result capability and
consumer implementation digest. `consumer.json` remains `pending` for all four
consumers; this repair only makes the real evidence expressible and checkable.

Scope check: no generic AQ framework, second ledger, new Runtime authority,
remote attestation, full AQ rerun, Phase4-04 or UI is introduced.
