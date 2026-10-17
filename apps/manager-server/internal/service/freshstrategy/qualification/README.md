# Fresh production consumer record

`consumer.json` is the sole build-bound Fresh qualification authority. It is
needed because the original immutable AQ manifests describe evidence and
fixture profiles, not independently accepted Fresh production consumers. The
record references those original hashes/runner HEADs; it does not replace their
reports, create a second AQ ledger, or grant whole-profile/Beta qualification.

**All four consumers are pending. No production qualification is granted.**
The empty case sets, acceptance hashes, limitations and implementation hash
are deliberate missing acceptance facts, not placeholders interpreted as PASS.
The current record preserves the candidate and evidence identities from the
Phase4-03 audit. No fixture, CI job, request, environment variable or Runtime
observation can change it in a running Manager.

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
they are not review prose.

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
consumers additionally reference the existing immutable `CPA8-STAGE-r1` manifest.
Their optional `adoptionEvidence` names three distinct accepted cases in that
profile: `readCaseRef`, `replayCaseRef` and `verifyCaseRef`. Independent review must
accept those exact cases for the bounded `client-api-keys` resource and this
implementation. Only that explicit mapping yields its `Migrate` decision and
exact destination ArtifactID. An External safe-read profile cannot supply it.
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
`evidence/316f7452e29145039eb856dee65cfc15c2f0bdc86e839be4cc4b43d8a1794694.json`.
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
adapter fails closed before interpreting any body when the declared version is
missing or different. CPA v8.0.13 still serves `/v0/management/config` with a
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
`evidence/563ef299810ce576636df6bd5ebf39a0554d54e5b8bde31b8606405c4d2ac3be.json`
(network) and
`evidence/7388022d21d2d6502449c311bc5379e4e954db0030912e73a2607d809fefb5f2.json`
(legacy). CI emits per-class
`fresh-adoption-resource-{network,legacy}-{manifest,report,read,replay,verify,authority}.json`
from the real process run. Source read, destination replay, destination
verification and legacy staging transition are four separate proofs: a valid
one never grants another, and a resource chain for one source class cannot
authorize the other. Any missing/mismatched manifest, report, head, caseRef,
trace, source profile or target fails the whole record closed.

Accepted consumers also persist the bounded, non-secret
`adoptionResources` inventory discovered from the source. Every entry has
exactly one decision: only `client-api-keys` may be `Migrate`; auth files,
provider state and configuration sections remain explicit manual or unsupported
actions. Unknown upstream sections collapse to the fixed `cpa-config-unknown`
reference and never become durable upstream-controlled identities. The
inventory and its decisions are written through the existing Fresh CAS before
any destination mutation and are immutable afterwards.

All current entries still omit `applicability`, `adoptionEvidence` and accepted
limitations, and remain pending. Missing destination capability retains
`adoption_destination_staging_required`; accepted source-only consumers with an
available destination return `adoption_resource_qualification_required` before
source discovery. Any target/transport/config/mode mismatch has no consumer entry
and therefore fails before a source probe, destination staging or Runtime
mutation. Test-only synthetic acceptance is constructed in memory and is never
part of this directory or the production binary. Controlled real-CPA CI
observations are review inputs, not authority files and not automatic production
acceptance.


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

All consumers remain pending until the exact-head observations from these
repairs are independently reviewed. CI success alone never writes acceptance.
