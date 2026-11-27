# Fresh production consumer record

`consumer.json` is the sole build-bound Fresh qualification authority. It is
needed because the original immutable AQ manifests describe evidence and
fixture profiles, not independently accepted Fresh production consumers. The
record references those original hashes/runner HEADs; it does not replace their
reports, create a second AQ ledger, or grant whole-profile/Beta qualification.

**All four consumers are accepted only for the exact scope and implementation pinned below.**
The accepted scope is bounded to the four Phase4-03 consumer paths.
Original r4 Stage capability remains `limited`; dedicated CI resource and
Legacy transition chains provide the separate live-process proof.
The single build-bound `consumer.json` controls authorization. No fixture, CI
job, request, environment variable or Runtime observation can change it in a
running Manager.

The current [CPA v8-only scoped acceptance](evidence/e93d58e966c2a592696ffb54c69a86aaca776685744e7574ab1cfba6718feb1a.md)
binds the actual source and destination executions to official CPA `v8.0.55`.
Both real-process producers passed from immutable implementation
`6c06426ef23fb2a5402df2efc70f8385ce01b986`
(tree `065f1655dfe74b206c49cea6a45d462b82ceda47`). The build read a clean
`git archive` of that commit rather than host scratch directories. Race-enabled
Linux/amd64 test tools and the Runtime Supervisor were newly compiled under an
unprivileged identity in `golang:1.26.11-bookworm`, then executed in a throwaway
`debian:bookworm-slim` (`linux/amd64`) container with `--network none`.

Each private source profile carried 133 Client Keys. Both source and destination
were actual pinned CPA `v8.0.55` processes; no v7 executable was mounted or run.
Readback, effective client authentication, destination identity, Supervisor
restart and preserved source state were verified. `gatewayPending` remains true.
The private `legacy_managed` class now denotes a constrained legacy-layout v0
safe read on CPA v8, not a supported pre-v8 runtime or public Fresh admission.

The current consumer implementation SHA-256 is
`3db7719c1bcf59b3a6439d0074a75a98bf0d96434b8c850878f50bd61387d29c`.
The independently audited 11-file Runtime execution-chain pin is
`598260a2127acff39b9e202f508325363aabc26a4392cb1952dd25ca05f41431` (updated from
`340686a6ad37b2a6843c943f6beb2a1339edc5abcf441f1ae2483ddaa63941d6` after
independent read-only Carbon audit of `1272db0214b33ae86bbef1908f66c9fe8c055d21`
with GO and 0 findings).

| Current chain | Report SHA-256 | Executed source |
| --- | --- | --- |
| network resources | `3ff33182fb26b6510e3eeb75b1e0a90b2be4d202e704c7158917315cd1c05151` | `6c06426ef23fb2a5402df2efc70f8385ce01b986` |
| legacy-layout v8 resources | `d2d3259e29698c97a7cf89957de81cce66e1c83002493a5061d6364ebf264ec7` | `6c06426ef23fb2a5402df2efc70f8385ce01b986` |
| legacy-managed v8 transition | `a8d00920c5d4710500b6126d711bfd882b11f41e73c01df36e7935967595af38` | `6c06426ef23fb2a5402df2efc70f8385ce01b986` |

Thirteen raw producer outputs were byte-verified; all three manifests were
already identical and ten report/trace/observation files are new. All 115
existing `v2` evidence files remain unchanged. The previous
[acceptance](evidence/a8230400f8defdb46d7adb38b6be0872f7b7d337d6d69b63af56f341ad5e30a1.md)
and its [final audit](evidence/17363ecbbe981be8798f5376aa6a74a3f629f2eb2394366e4d38fb443b31500b.md)
(along with earlier records) are superseded historical records, not current
v8-only support evidence. Historical v7 evidence is neither relabeled nor
executed as a current positive.

The three original fixed-CPA8 AQ reports retain their selected traces, original
runner `8935bce5ea7ce994185ee97acb8a4fdbd9cdd6cc` and tree
`b636fae617e9fb19eea938bfe3b6e1fb54af3ac5`. The six AQ tools were byte-compared
with that revision and are unchanged. Their original limited, unknown, failed
and aborted boundaries are not promoted. The separate [independent byte/provenance
audit](evidence/757f21faca53c7f04b11bb881b8740c8029e9ca55d22f3a93856b1367fdb4c43.md)
completed with GO and no findings (audited by read-only `QualByteAuditArgon`,
actual runtime model `oracle-cpa/gemini-4-neon-xhigh`).

This private renewal does not certify public Gateway activation, five-mode UI,
SSE/WebSocket, live provider/OAuth accounts, Native installation, arm64, other
operating systems, arbitrary External topology or future CPA major versions.


To accept a consumer, independent review must first identify the exact original
case subset and explicitly accept its applicability to that consumer's actual
implementation. Acceptance is valid only for exactly the scope this build can
enforce: `applicability` must equal the enforced target/source-transport/
destination-transport/config/mode tuple, and `limitations` must exactly equal
the closed enforced set for that consumer. Free-text, missing, extra, duplicate
or unenforced limitations make the record unqualified. A review that needs a
restriction outside this closed scope must leave the consumer pending. Review
must separately accept legacy staging transition.

### Runtime-enforced applicability

| Dimension | Enforced values |
| --- | --- |
| `target` | `exact_cpa_v8_artifact`, `bounded_v8_management_surface`, `manager_owned_exact_v8_destination` |
| `sourceTransport` | `none`, `bounded_v8_direct_safe_read`, `pinned_legacy_cpa_v8_0_55_direct_safe_read` |
| `destinationTransport` | `external_connection`, `manager_runtime_private_v1` |
| `config` | `cpa_v8`, `cpa_v8_0_55_source_to_cpa_v8_destination` |
| `mode` | `fresh_new_managed`, `fresh_connect_external`, `fresh_adopt_network_existing`, `fresh_adopt_legacy_managed` |

The enforced limitation vocabulary is `management_generation_v8`,
`exact_cpa_v8_artifact_identity`, `read_only_bounded_safe_read`,
`direct_transport_no_proxy_no_redirect`, `client_api_keys_only`,
`legacy_source_profile_cpa_v8_0_55`, `source_preserved` and `gateway_pending`,
with a fixed set per consumer. These keys map to facts the code actually checks;
they are not review prose. `client_api_keys_only` is a `legacy_managed` limitation
only: a network adoption migrates credentials, gateway configuration and the
provider sections its evidence chain authorizes.

The following evidence restrictions cannot be checked by the running Manager and
therefore cannot be represented as accepted applicability. A review that relies
on one of them must keep the consumer pending:

- target host/topology class (loopback/private/remote, IPv6, reverse proxy or
  mediation) beyond the bounded safe-read operation itself;
- TLS/CA/mTLS specifics beyond normal certificate verification;
- CPA process platform/OS/arch beyond the exact artifact bytes;
- CPA configuration semantics beyond the checked config schema;
- deployment/operational modes the Manager does not observe;
- time-bound review validity.

Only then may the reviewed record name `status: accepted`, its original profile/
case/report/manifest/runner-HEAD bindings, independent review SHA-256, explicit
accepted limitations and invalidation conditions. `implementationSha256` binds
the compiled Fresh strategy/loader/adoption source bytes and the v8 safe-read/
destination verification source
digest; changing them requires renewed acceptance. It is independent of the
original evidence execution HEADs and avoids a commit containing its own SHA.

Referenced original manifests, reports, selected sanitized traces and the
independent review must be included unchanged under
`evidence/<sha256>.json` or `evidence/<sha256>.md`. The loader verifies the
actual bytes, report-to-manifest binding, observed archive/binary hashes,
resulting Runtime ArtifactID, exact runner HEAD, profile/case/run/trace bindings
and completed passing evidence. It does not rerun AQ or infer acceptance from
those observations. Evidence unavailable in the build, malformed canonical
record bytes or any binding mismatch grants no qualification to any consumer.
Git attributes preserve these bytes on Windows.

The four consumers are independent: New Managed, External, network adoption and
legacy adoption. Accepted source qualification still grants no resource replay,
destination ownership, Gateway activation or product completion. Both adoption
consumers reference original `CPA8-STAGE-r1` limited background evidence
separately from the real-process resource proof. `adoptionEvidence` binds the
independent class-specific `CPA8-ADOPT-RESOURCE-r1` manifest and its distinct
`readCaseRef`, `replayCaseRef` and `verifyCaseRef`. Only those separately
accepted completed/supported cases authorize the bounded `Migrate` decisions and
exact destination ArtifactID. The three traces each carry a per-kind count, and
a kind is authorized only when all three name it, so one kind's evidence can
never authorize another. External safe-read or the
original r4 `limited` observations cannot substitute for any resource proof.
Legacy transition acceptance remains separate from network adoption acceptance.

Legacy transition authority is separate from source/profile evidence and is a
complete chain, not a standalone observation. An accepted
`adopt_existing`/`legacy_managed` consumer must carry a `legacyTransition`
binding with the accepted `CPA8-STAGE-r1` profile, the checked-in transition
case `fresh-legacy-managed-stage-v8-r1`, the manifest/report/implementation-head
digests and the observation digest. The loader verifies:

```
consumer binding
  -> transition manifest (case, profile, exact pinned v8 legacy-layout source, target)
  -> transition report (manifest, runner head, completed PASS result, evidence ref)
  -> transition observation (source profile, exact target, implementation digest, facts)
```

The checked-in manifest is content-addressed under
`evidence/4aa6eaeda49ddfa624ea562f5b9425544f8abf5bf0343e34fc3b2b3d6235d4df.json`.
The per-run report and sanitized observation are generated by the exact-CPA CI
job as `fresh-legacy-transition-report.json` and
`fresh-legacy-transition.json`; acceptance must pin those exact bytes. This case
is deliberately **not** inserted into, or required from, the retained original
r4 `CPA8-STAGE-r1` report case refs. `network_existing` must not carry the field,
and only a fully validated chain sets `StagingV8TransitionQualified`; ordinary
profile evidence never does.

The accepted legacy-managed source profile is exact, not a version range: CPA
`v8.0.55` (`X-CPA-VERSION: 8.0.55`), archive SHA-256
`4cd593251db85e50c820f42e6ea5cecdd05f902201d3dbdb025671d4f306a123` and Linux
amd64 executable SHA-256
`e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7`.
The adapter checks the authenticated endpoint's version header and constrained
legacy-shaped v0 response; it does not remotely attest executable bytes. The
binary hashes identify the CI executable, not arbitrary operator-selected
servers. Missing, pre-v8 and nonmatching headers fail closed before parsing any
body. The pinned v8 source is accepted only with this version and bounded body
shape; it does not gain the network source class's wider replay authority.

Client-key adoption resources use a separate, class-specific evidence chain
instead of the retained r4 report's case refs:

```
consumer.adoptionEvidence
  -> resource manifest (CPA8-ADOPT-RESOURCE-r1, exact v8 candidate, source class)
  -> resource report (manifest, runner HEAD, artifact observation)
  -> three distinct completed/supported cases and sanitized traces:
       source-read | destination-replay | destination-verify
  -> implementation head and compiled-consumer digest
```

The retained `CPA8-STAGE-r1` report and its `AQ04-S*` case refs remain
background profile evidence. They are **not** used to prove client-key replay,
and the new resource cases are not inserted into the immutable r4 report. The
checked-in resource manifests are
`evidence/41725ced527fa1221e32d4f0aabd9b3a7709dd454cc2761165e8e375bd9db6a2.json`
(network) and
`evidence/9305f4fa655d918079be6227113877cf28c22f234bead1daef0981dee67967ce.json`
(legacy). CI emits per-class
`fresh-adoption-resource-{network,legacy}-{manifest,report,read,replay,verify,authority}.json`
from the real process run. Source read, destination replay, destination
verification and legacy staging transition are four separate proofs: a valid
one never grants another, and a resource chain for one source class cannot
authorize the other. Any missing/mismatched manifest, report, head, caseRef,
trace, source profile or target fails the whole record closed.

Accepted consumers also persist the bounded, non-secret
`adoptionResources` inventory discovered from the source. Every entry has
exactly one decision. A kind may be `Migrate` only when the resource chain's
three traces all name it, so `client-api-keys` and — for a `network_existing`
source — `cpa-auth-files`, `cpa-config-routing` and `provider-state-api-keys`
migrate, while the remaining sections stay explicit manual or unsupported
actions. The section item is a whole `api-keys.*` group or one of the copyable
`oauth.*` children, which are provider configuration rather than the source's
auth directory; both are gated under `provider-state-api-keys`. A `legacy_managed`
source uses a pinned CPA v8 legacy-layout safe-read profile whose accepted
evidence authorizes `client-api-keys` only; other kinds retain explicit manual
actions. Unknown upstream sections collapse to the fixed `cpa-config-unknown`
reference and never become durable upstream-controlled identities. The
inventory and its decisions are written through the existing Fresh CAS before
any destination mutation and are immutable afterwards.

**Honest status of the widened kinds (2026-10-29 waiver carried forward under `eedd3243`, `1eba18df`, `5e2c9dcc`, and `025f5175` on 2026-11-06).**
Historical acceptance `65eb659a` covered client API keys only. The network
consumer's migration of credentials, gateway configuration and provider sections
was introduced under a **maintainer waiver dated 2026-10-29** and carries
forward under the pre-Beta maintainer acceptance record
`eedd32432a0165e10d8993323500bd885a8a0d86272318e4e933874ccb6fdee5`, its
clean-source provenance re-emission addendum
`1eba18df2a5bb486e8f184ce4ba618f5d5a52097d7beb20c1a9b9d44df35e8d9`, the
superseded re-verification record
`5e2c9dcc9bf2ad453cd9c4d5312eac9a2f1e8f79d408c806e55c7b18ac9758e5`, and the
previous independent re-verification record
`025f517544f71ba83eac62b104c3ae381b454b5d07c29b7b46c86962fb562e3d`, followed by
the historical 2026-11-15 renewal
`1d9c68bc18ad51f8abf7955e9c25e9cc3262779b1b2ccd9e29eb53081c5f867c`, the
superseded CPA v8-only renewals `a8230400f8defdb46d7adb38b6be0872f7b7d337d6d69b63af56f341ad5e30a1`
and `41ab4005c260ef24064cd8fa6a7a9cee88b9a2644e77bd4c2e1f10290ba8f265`, and the current
CPA v8-only acceptance `e93d58e966c2a592696ffb54c69a86aaca776685744e7574ab1cfba6718feb1a`,
backed by actual CPA `v8.0.55` source-read, destination-replay and verification
evidence. The executor's per-item gate is what keeps the four-kind breadth of
`network_existing` honest: an item is copied only when the record's decision for
its own reference is `migrate`. The
record and the gate are computed from one pinned resolver,
`internal/service/cpa/adoption_ref.go` (its bytes and the `AdoptionUniverseDigest`
of every item-to-reference pair are part of the consumer digest):
`AdoptionItemRef(item)` classifies one item, and `AdoptionRootRefs(root)` is the
derived set every item under a discovered root carries. Discovery records a
decision for every reference in each reported root's set, so a root is no longer
one resource — `oauth` contributes `cpa-auth-files`, `provider-state-api-keys` and
`cpa-config-routing` (its refresh worker count and `providers` configuration),
shared host roots (`server`, `plugins`, `observability`) contribute both their
platform-managed host reference and `cpa-config-routing`, and the reader's
unknown catch-all (where `requests`/`multimedia`/`upstream`/`models`/`credentials`
arrive) contributes both `cpa-config-routing` and `cpa-config-unknown`.
No set contains a reference that no item under that root
produces, so an item is never recorded as manual work under one reference while
being copied under another, and the record never asks the operator to redo work
the executor never does. A source key that no copyable item carries is named in
the universe on purpose, and `TestEverySourceKeyIsCoveredByItsRoot` checks every
direct child key of every named section against that, so a key of the accepted CPA
v8 document cannot silently leave the adoption.

## Current acceptance-repair inputs

The bounded destination execution chain is pinned by
`runtime_destination_implementation.sha256`. The regression test recomputes
that digest over the Manager Runtime destination client/model and the Supervisor
destination/start/restart/recovery/private-protocol source files. The pin is part
of `consumerImplementationSHA256`, so changing that execution chain invalidates
any later accepted consumer record.

Controlled real-process CI for `legacy_managed` launches official pinned CPA
`v8.0.55` with a legacy-layout configuration. The source stays live and
byte-preserved while Fresh reads its authenticated v0 compatibility endpoint.
The existing Supervisor `StageDestination` child operation creates a separate
Manager-owned destination using the same exact v8 candidate; its ownership,
staging, readback and restart are independently verified. A class label or a
configuration echo cannot substitute for those two real processes and proofs.

Acceptance is based on the independently reviewed exact evidence bytes,
not on CI success alone. Any implementation drift invalidates the record.
