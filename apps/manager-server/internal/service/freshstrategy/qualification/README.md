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

The current [scoped access-prefix / multi-Key acceptance record](evidence/1d9c68bc18ad51f8abf7955e9c25e9cc3262779b1b2ccd9e29eb53081c5f867c.md)
separates immutable upstream firmware AQ evidence from renewed product execution
evidence. The three original AQ reports, their selected traces and their
`implementationHead` remain at `8935bce5ea7ce994185ee97acb8a4fdbd9cdd6cc`
(tree `b636fae617e9fb19eea938bfe3b6e1fb54af3ac5`); the six AQ tools are byte-identical
to that revision, and the official CPA `v8.0.55` candidate is unchanged. Their
original limited, unknown, failed and aborted boundaries are not promoted.

The real Go destination and adoption producers were rebuilt and executed from
clean implementation `77625e1804289a69beffdcb2b641a1ade9ac746c`
(tree `c039a5e462c8e5cd304e4f64740634bd10fcedde`). Both producers passed with actual
CPA `v8.0.55` / `v7.0.12` processes and the newly compiled Runtime Supervisor in
task-owned network isolation. Each private adoption profile carried 133 Client
Keys. The generated traces verify readback, client authentication, destination
identity and preserved source state; `gatewayPending` remains true.

The current consumer implementation SHA-256 is
`c7a8dc1def60316a37e90f0a4246d466449f3c58d351805f52308cd5ff438187`, and the
11-file Runtime execution-chain pin is
`340686a6ad37b2a6843c943f6beb2a1339edc5abcf441f1ae2483ddaa63941d6`.

| Renewed chain | Report SHA-256 | Original runner |
| --- | --- | --- |
| network resources | `755d0a52edfc8fc1e30561b97433454504b9fd37f2766cffea62d91765a6b054` | `77625e1804289a69beffdcb2b641a1ade9ac746c` |
| legacy resources | `87bfcc272383120bfc62c0ebe98bce238fcbba077c92678ca8385934d9ba31ef` | `77625e1804289a69beffdcb2b641a1ade9ac746c` |
| legacy transition | `9dfd548c2e6504767e32a12b02a9b9b0ba787bd9cb67e2b4e4efc5eb6279d2c1` | `77625e1804289a69beffdcb2b641a1ade9ac746c` |

The acceptance record lists every renewed report, trace, observation and manifest
digest. Existing qualification evidence and the previous
[independent pre-Beta verification](evidence/025f517544f71ba83eac62b104c3ae381b454b5d07c29b7b46c86962fb562e3d.md)
remain historical audit records, including the earlier Argon and Sol reviews and
Native NH2 evidence. This renewal does not claim a new Native, public Gateway,
five-mode UI, SSE/WebSocket, online provider/OAuth, arm64 or cross-OS run. External
acceptance still does not guarantee connectivity for every topology.

The [final independent artifact/loader/source audit](evidence/f71b3d471ed1a1cd49901a4cfff5596162d6823e3434a0bac63efb2b80bd5773.md)
completed with GO and no findings. Its final model was verified from session
provenance as `gemini-4-argon-high`, without fallback. It independently checked
the 23 implementation inputs, 11-file Runtime pin, canonical consumer bytes,
all 10 new JSON outputs and all 74 retained historical evidence files. It is a
read-only artifact audit, not execution of the Manager suite or product acceptance.


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
| `sourceTransport` | `none`, `bounded_v8_direct_safe_read`, `pinned_legacy_cpa_v7_0_12_direct_safe_read` |
| `destinationTransport` | `external_connection`, `manager_runtime_private_v1` |
| `config` | `cpa_v8`, `cpa_v7_0_12_source_to_cpa_v8_destination` |
| `mode` | `fresh_new_managed`, `fresh_connect_external`, `fresh_adopt_network_existing`, `fresh_adopt_legacy_managed` |

The enforced limitation vocabulary is `management_generation_v8`,
`exact_cpa_v8_artifact_identity`, `read_only_bounded_safe_read`,
`direct_transport_no_proxy_no_redirect`, `client_api_keys_only`,
`legacy_source_profile_cpa_v7_0_12`, `source_preserved` and `gateway_pending`,
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
  -> transition manifest (case, profile, exact v7 source profile, target)
  -> transition report (manifest, runner head, completed PASS result, evidence ref)
  -> transition observation (source profile, exact target, implementation digest, facts)
```

The checked-in manifest is content-addressed under
`evidence/80fc3fea2d865edf50e3f2a87ae5109fb15e7c97bc3863a8589c39b50e974a5e.json`.
The per-run report and sanitized observation are generated by the exact-CPA CI
job as `fresh-legacy-transition-report.json` and
`fresh-legacy-transition.json`; acceptance must pin those exact bytes. This case
is deliberately **not** inserted into, or required from, the retained original
r4 `CPA8-STAGE-r1` report case refs. `network_existing` must not carry the field,
and only a fully validated chain sets `StagingV8TransitionQualified`; ordinary
profile evidence never does.

The accepted legacy source profile is exact, not a version range: CPA `v7.0.12`
(the official release reports `X-CPA-VERSION: 7.0.12`), archive SHA-256
`8afc9ed4c89a50a5ffce5d122a9bb93442c5b8ff3d8c6f07b7a274c719109b74` and Linux
amd64 executable SHA-256
`1eb108b85ad554b92bab9f5a85af433d7c2e1b951bb192b639d73605d4593d1a`. The runtime
adapter checks the authenticated endpoint's declared `X-CPA-VERSION`
and the constrained v0 response shape; it does **not** attest the remote source
binary SHA. The reference binary hashes above identify the CI test executable,
not arbitrary operator-selected production sources. The runtime adapter fails
closed before interpreting any body when the declared version is missing or
different. CPA v8.0.55 still serves `/v0/management/config` with a
legacy-shaped body; it is rejected by version metadata, never treated as legacy.

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
`evidence/08d8c0163c77f235365b4d5f9149a6d3855062c642071eb99e5d12bfa69b49fe.json`
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
source is a pre-v8 process with no v8 management
surface, so its evidence authorizes `client-api-keys` only and its other kinds
keep the manual action they had before. Unknown upstream sections collapse to the fixed `cpa-config-unknown`
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
the scoped 2026-11-15 renewal
`1d9c68bc18ad51f8abf7955e9c25e9cc3262779b1b2ccd9e29eb53081c5f867c`, backed by
real-process CPA `v8.0.55` source-read, destination-replay and destination-verify
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
`cpa-config-unknown` (the source keys nothing copies: its refresh worker count and
its `providers` runtime block), and the reader's unknown catch-all (where
`requests`/`multimedia`/`upstream` arrive) both `cpa-config-routing` and
`cpa-config-unknown`. No set contains a reference that no item under that root
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

Controlled real-process CI for `legacy_managed` uses a pinned real pre-v8 CPA
source that remains live and byte-preserved. Fresh points at that v7 source URL
and reads it only through the explicit legacy v0 adapter; the exact v8 candidate
then runs as the Manager-owned destination created by the existing Supervisor
`StageDestination` child operation. That destination is the staging-v8
transition itself. Merely labelling a v8 process as `legacy_managed`, or letting
a test pre-build a v8 source, is not transition evidence.

Acceptance is based on the independently reviewed exact evidence bytes,
not on CI success alone. Any implementation drift invalidates the record.
