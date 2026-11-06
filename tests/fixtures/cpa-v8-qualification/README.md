# CPA v8 qualification evidence

## AQ-05 r5 inert extension guard fixtures

The current manifest has **94 declarations**: **79 selected leaves in 24 guard
groups**, plus 15 unexecuted function declarations. Five mandatory profiles stay
selected and the two optional plugin profiles stay off. Schema 1 and profile
composition r1 remain unchanged; r1-r4 factories and original reports remain the
coverage sources for those revisions. The r5 base is AQ-04 merge
`aacf002b9b80bad3fbebb84f5478ca89cd0ff4ef`.

`cpa-v8-extension-fixtures.mjs` exports a fixed evaluator and a foundation report
builder. It has no executable CLI, network client, CPA run or plugin loader.
The protected `GET /v0/management/cpamp-fixture/read` and public
`GET /v0/resource/plugins/cpamp-fixture/status` are inert fixture data only. The
EXT-GUARD manifest surface remains `transport=fixture`, method/path null and
`probeEligible=false`; actual EXT-ENABLE surfaces remain empty.

The evaluator distinguishes exact core ownership, declared plugin ownership,
unknown routes and conflicts. Duplicate keys, including same-plugin duplicates,
are rejected independently of declaration order. Fixture trust is explicit; a
reported menu, header, list or digest cannot grant it. Paths reject encoded,
ambiguous and noncanonical forms; query and method overrides cannot widen a
route. A one-use dispatch ticket checks current identity and monotonic epoch at
send and delivery. Revocation/ABA blocks new sends and late delivery while
retaining the count of effects already sent.

Protected headers contain only an adapter-owned synthetic CPA key; public
headers contain no credentials. Inbound headers are rebuilt, with a 32 KiB
UTF-8 byte limit. Responses accept only fixed inert JSON or plain text, nosniff,
32 KiB headers and one MiB bodies. Redirects, cookies, active content, ambiguous
MIME and unknown response headers reject the whole response. No actual browser
origin/DOM/storage/network isolation is established. Native CPA plugins remain
fully trusted code that can access runtime state and key material.

`emitExtensionEvidence(ownedParent, implementationCommit)` creates a fresh owned
child directory and never overwrites an existing report. It writes the original
canonical manifest, per-leaf safe traces and a `foundation-fixture` report. The
candidate remains declared: provenance `not-verified`, both observed hashes
null. Guard assertions may support guard behavior only; response harness leaves
remain unknown. Directory drift or failed private cleanup prevents a completed
run. The supplied execution commit must be verified independently against the
actual implementation and test source; it is not an attestation service.

Run the six qualification/classifier test files and `npm run test:repo`. These
checks execute every fixed guard leaf and transport/process tripwires; they do
not start CPA or qualify optional plugins. The existing runner's default
manifest validation accepts r5, but it gains no r5 upstream case set.

### Independent exit index — pending

```yaml
decision: pending
qualification: not accepted
profileComposition: r1
toolAcceptance: pending
observedUpstream: see-original-scope-reports
acceptedPre4A: pending
acceptedProductionConsumer: pending
publishedSupported: not established
independentReviewer: pending
```

This is a manual index prepared by the implementer. A reviewer must expand each
requested profile to **every mandatory leaf and expectation in its original
manifest**, then accept or reject applicable evidence and limitations. The rows
below locate original material; they do not assert complete or independently
accepted coverage. CI success, merged PRs and this emitter cannot decide Go.

| Scope             | Implementation / original material                                                                               | Current acceptance state                                                                                                                                            |
| ----------------- | ---------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| r1 foundation     | #912, `32ba418c2a5f09aaaba0f887a8d429da91df2fbe`; `createSeedManifest()`                                         | Tool/independent acceptance reference missing                                                                                                                       |
| r2 normal         | #915, implementation `27f4bf80dc7cbbca90c07cc2d3853be7c5ee9902`; local `normal-04/output/run-NDrZLb` (92 leaves) | Report records base `5bb3a5b88e234a2e8315af694388e72bc0bfe804` as runnerCommit; all three source hashes match implementation. Independent reconciliation pending    |
| r2 natural expiry | local `expiry-01/output/run-TNSLLJ` (one leaf)                                                                   | Same recorded base; runner/validator hashes differ from final AQ-02 implementation. Affected evidence review or rerun remains pending; original report is preserved |
| r3 External       | #917, `22eaaf12db07571a6664437a51282bcc54f73456`; local `run-02/output/run-i0iIO6` (50 leaves)                   | Exact committed run; independent acceptance and requested real-target coverage pending                                                                              |
| r4 Usage/Stage    | #920, `7d8c98cb93a4ca9d405f2f91504348d439655f59`; local `run-06/output/run-ZNLRZB` (40 leaves)                   | Exact committed run; limitations and independent acceptance pending                                                                                                 |
| r5 guard          | This scope's emitted foundation report, exact implementation/test head and all 79 leaf traces                    | Independent tool/guard acceptance pending; no upstream execution                                                                                                    |

Original report / manifest SHA256 bindings (local artifact locators above; traces
are resolved through each original report's evidenceRefs and recorded hashes):

```text
r2 normal report   d4e46e961db257770b0d705b5f1d3fa200edf930f2363186a75e04330b07a702
r2 expiry report   7e0255abad94f3ca83a9619cb69866fbb01ba96de3fc0ddc19c3ede349d14202
r2 manifest        758fafa41bbbf6ce58961c0a3ac3beedab2af29b18631bef3ffb4064dd3955f2
r3 report          2d8872beae7ed07c75d2f911daff11a7e9d8f5e8a001b2159536b9b2bdfb2f2f
r3 manifest        75f79bc124a5a712ac74f162b0ab8d434438863b295a79de60e92a8d7c5523ea
r4 report          6add816401dad04a7e29285eece43e30abcef6625313a8e492ae24b4ef079a7c
r4 manifest        3c21eb7db0d45638b9be81891277329b0c04ea941588dc90213d6d864f34896f
```

All original r2-r4 reports above observe archive
`50ecffb47fdd81c8c5a9825a73a7a905ab66342337e274f39c4276b92d3533f3`
and binary `b682e9e42586263f476888361514f68f89ff4ebf89a5dadba394b850b797ce41`.
Those observations belong to their original runs and are never copied into r5.

For each required leaf, copy and complete this row without changing the source
report or marking an empty field accepted:

```yaml
profileRef: pending
operationRef: pending
originalManifestRevision: pending
originalManifestSha256: pending
runId: pending
caseRef: pending
expectationRef: pending
fixtureRef: pending
configProfileId: pending
modeProfileId: pending
artifactProvenanceAndObservedHashes: pending
implementationCommit: pending
testCommitAndSourceHashes: pending
safeEvidencePathAndSha256: pending
sliceIndependentAcceptanceRef: pending
statusAndAcceptedLimitation: pending
invalidatedBy: manifest-or-head-or-artifact-or-config-or-mode-or-profile-change
remainingConsumerOwner: pending
```

| Exit check        | Evidence and remaining closure condition                                                                                                                                                            | Decision |
| ----------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- |
| X01 Plan/heads    | Gate commit/blob in manifest; exact implementation/test heads above; AQ-02 metadata/source reconciliation and independent baseline references required                                              | pending  |
| X02 Profiles      | 16 operations, seven profiles, five mandatory/two optional off; requested scope must remain explicit                                                                                                | pending  |
| X03 Completeness  | Expand original catalogues by mandatory leaf/expectation; neither seed counts nor selected-pass totals prove coverage                                                                               | pending  |
| X04 Tool/artifact | Independently accept validator/runner/source binding and original observed archive/binary provenance                                                                                                | pending  |
| X05 Management    | Original readback/persisted/effective controls; real provider OAuth exchange and refresh remain missing                                                                                             | pending  |
| X06 External      | Original target/transport/semantic/freshness facts; real target/proxy applicability remains unaccepted                                                                                              | pending  |
| X07 Usage         | Limited receipts, loss, disabling, retention and competition; maximum natural window, observed overflow and exact enqueue witness remain missing                                                    | pending  |
| X08 Stage         | Synthetic snapshots, startup writes, commits and conditional restore; no real legacy upgrade or product cutover acceptance                                                                          | pending  |
| X09 Extension     | Accept all applicable r5 guard/harness facts independently; optional actual enable remains off and needs separate provenance/route/content/native-trust evidence                                    | pending  |
| X10 Integrity     | Check original hashes, limits, cleanup and actual CI per implementation head; partial/failed/unknown runs cannot fill requirements                                                                  | pending  |
| X11 ADR           | ADR-0001 authority and ADR-0004 Full Web Public-only/Lite separation still apply; fixture-only code adds no durable authority change. Independent reconciliation required                           | pending  |
| X12 Exit/owners   | Independent reviewer records accepted/rejected/pending scope and limitations. Accepted pre-4A permits shaping 4A-01 only; product consumers, UI, optional enable and release retain their own gates | pending  |

No new ADR is needed for this inert fixture implementation. ADR-0004's general
CPA Management boundary covers v8 as well as its v0 examples; a custom declared
v0 fixture is no permission for Full Web to connect directly. Real transport,
browser content/native trust and migration/recovery consumers remain with their
respective Foundation, Lite/Plugin and lifecycle owners. #870 and the design/UI
stop remain in force. **Pre-4A exit remains pending/blocked.**

## AQ-04 r4 Usage and Stage fixtures

The historical r4 manifest has **55 declarations**. `usage-stage-r4` selects **40 leaves
in 34 groups** (18 Usage, 16 Stage). Thirteen other operations plus the maximum
natural retention and observed subscriber overflow leaves remain pending. The
schema, 16 operations, seven profiles and r1 profile composition are unchanged.
`createSeedManifest()`, `createManagementManifest()` and `createExternalManifest()`
preserve their original catalogues; no earlier results are copied into r4.
The implementation base is AQ-03 merge `4343c7aa597fa133aa112d45dcec5ca07e8dd33a`.

All r4 cases use `standard-r1`: request five seconds, header 32 KiB, decoded body
one MiB, startup 30 seconds, case 90 seconds, slice 15 minutes. Queue reads consume
records. There is no consuming retry, transport fallback, peek, ACK or cursor.
Each case uses at most 32 pops, 16 records per pop and 512 ordinary records;
pressure is bounded by 1,024 records and 32 MiB. The selected slow-reader fixture
produces 64 records and does not claim an observed 256-slot overflow.

Usage cases compare synthetic model forwarding, private record identities,
consumer receipts, HTTP competition, RESP fanout/pop, separate statistics and
queue disabling, and process restart. The loss relay fully captures a bounded
upstream receipt before dropping the consumer response. An early disconnect
keeps its cutpoint unknown. Local duplicate/coverage projections always preserve
unknown gaps and unproven replay. Raw usage IDs, keys, headers, source fields and
tokens stay private; report records contain only the existing typed assertions.

Retention experiments wait actual five and 60 seconds, with paired receipt and
fresh-producer controls. An empty result alone is not proof of expiry, loss or
zero usage; enqueue timestamps remain unobserved. An omitted retention document
leaf returns 404, and explicit 0/-1/3601 values are read back unchanged. Those
GETs do not prove the effective default or runtime clamp. The one-hour natural
window and an actual subscriber overflow remain unselected required evidence.

Stage uses three synthetic source shapes: legacy with a prepared hash, mixed
legacy/v8, and v8 with synthetic credential metadata. A separate preparation
process obtains the hash; the frozen source is never launched. Complete sibling
source/snapshot/candidate trees include regular files, directories, modes and
absence, bounded to 64 members, one MiB per file and 16 MiB total. Links, aliases,
special files and unstable identities are rejected. Candidate runtime paths are
rebound before launch. Source integrity is checked at every cutpoint and after
owned child shutdown. Startup writes, semantic reads, persistent mutation,
lost-reply reconciliation and later writes are distinguished.

Restoration requires a confirmed stopped child, complete snapshot, unchanged
source, known committed state and an exact one-use checkpoint. A later write,
binding change or tamper denies restoration. The complete snapshot is restored
to a new candidate, including absence of new auth/state, then runtime paths and
real AUTH/CFG/KEY/CRED/MODEL controls are checked again. This is harness evidence;
it establishes no production rollback, crash atomicity or real legacy upgrade.

The read-only candidate control first proves write-open is denied. macOS Docker
shared directories can report mode 0400 while permitting writes, so use a fresh
Linux-owned backing directory for the `/work` bind mount when needed. The CPA
container still has exactly four recorded bind mounts, no network, a read-only
root, non-root UID, no capabilities and bounded resources. Create/own that empty
directory before starting CPA, retain its mount identity, and copy only the final
sanitized output after private fixture cleanup. Never accept missing permission
enforcement as a passing read-only control.

Run `tests/cpaV8UsageStage.test.mjs` together with the runner, evidence, External
and classifier tests, then `npm run test:repo`. Build from committed source and
verify both new helper hashes in the image identity. All function observations
remain limited; harness/guard leaves stay unknown. Formal independent acceptance,
real provider gaps and Phase4A remain pending even when every selected case passes.

## AQ-03 r3 External safety fixtures

The historical r3 manifest contains **64 declarations**, selecting **50 leaves in 28
groups** through `--case-set external-safety-r3`. The remaining 14 entries keep
other operations explicitly pending. Historical r1 and r2 manifests are retained
by their original factories; their complete original leaves and reports remain
the source for earlier coverage, not these pending declarations.

The implementation base is `bda686bcdf93a1dcb1e71a3d8d6047323a0224e8`, the merge
of AQ-02 #915. The frozen planning input is the same planning commit and Gate
blob listed below, with scope revision r3. Schema 1, 16 operations, seven profiles
and profile composition r1 remain unchanged.

Only `GET /v8/management/config` is an External probe. Fixed fixture targets map
to private loopback listeners. Logical `10.0.0.7` is mapped to loopback for a
policy test; it does not prove physical private-network connectivity. All 3xx
are rejected, including same-origin redirects. Each read uses its own direct
agent, fixed resolution and peer/CA/SAN checks. The caller supplies no arbitrary
URL, key, handler or proxy through the CLI. Reads have no automatic retries.

DNS, TLS, headers, body and decoding share an active five-second maximum deadline.
Encoded and decoded bodies are each capped at one MiB; headers at 32 KiB. Only
identity and gzip JSON are accepted, with fatal UTF-8 and required-field checks.
Negative fixtures use shorter deadlines to observe actual cancellation. TLS
fixtures use owned OpenSSL-generated CA, valid and actually expired certificates;
IPv6 requires a real `::1` listener. Missing tools or IPv6 fail setup rather than
turning into skipped success. The tool image records OpenSSL and all helper hashes.

The real AUTH/CFG leaves read the verified official CPA. A TLS front forwards the
presented synthetic key to that same private CPA and is explicitly mediated
evidence; it does not prove native upstream TLS. Transport/deny/stale observations
remain harness or guard evidence with function capability `unknown`. Late response
tests change the binding while an actual request is outstanding and reject its
publication, including A-to-B-to-A. Profile projection rejects missing, duplicate,
unaccepted or inappropriate evidence.

Run the four focused test files (including `tests/cpaV8ExternalSafety.test.mjs`),
then `npm run test:repo`. The isolated procedure below also applies to r3; use
`external-safety-r3` and its frozen `createExternalManifest()` manifest. Build from committed
source and include `bin/ci/cpa-v8-external-fixtures.mjs` in the source/hash audit.
Every runtime input remains local to the owned network-none container.

Real user targets, explicit proxies, production clients/browser adapters and
formal independent qualification remain pending. AQ-02 real refresh and OAuth
exchange gaps are not cleared by r3. No profile, Phase4A or plugin activation is
accepted by this runner or a passing fixture report.

## AQ-02 r2 management runner

The historical r2 manifest contains **101 cases**: 95 management leaves in 36
groups and six retained downstream declarations. The normal case set selects 92
leaves. One natural-expiry leaf has its own case set. Real provider refresh and
successful OAuth exchange remain required and unselected in this offline scope.
The validator retains the r1 catalogue through `createSeedManifest()` and all r1
regressions. `createManagementManifest()` returns the frozen r2 catalogue.

| Input                           | Identity                                                                  |
| ------------------------------- | ------------------------------------------------------------------------- |
| AQ-01 merged prerequisite head  | `32ba418c2a5f09aaaba0f887a8d429da91df2fbe`                                |
| AQ-02 implementation base on v2 | `5bb3a5b88e234a2e8315af694388e72bc0bfe804`                                |
| Planning repository commit      | `b654d625902c0e810f9bfc581aa17c35f4b0cff0`                                |
| Gate blob                       | `d5367e63d4feda5854d0639746c18d6a78e0b274`                                |
| Profile composition             | r1: 16 operations, seven profiles, five required selections               |
| Tool base                       | Node 24.13.0 bookworm-slim, Linux amd64                                   |
| Tool base manifest digest       | `sha256:46feb5752989c05b8606e6323fbbc3db667d14ade1c24f5d0d44d9ca9909d607` |

The candidate archive/version/commit/digest are unchanged from r1 below. The
Python helper pins the archive size to 22,952,865 bytes, audits exactly five
regular members, verifies ELF64/little-endian/x86-64 and records the binary hash.
It uses Python 3.11+ stdlib and never downloads or executes an archive member.
An existing staging destination, links, unsafe ownership/modes, changed input,
CRC/truncation, oversized metadata and input limits cause rejection and cleanup.

### Local tool checks

```sh
node bin/ci/run-cpa-v8-qualification.mjs --manifest tests/fixtures/cpa-v8-qualification/manifest.json
npx vitest run tests/cpaV8QualificationRunner.test.mjs tests/cpaV8EvidenceContract.test.mjs tests/prCheckClassifier.test.mjs
npm run test:repo
```

The first command only checks the declared plan; it performs no network request,
spawn, CPA execution or write. The normal PR checks use synthetic archives,
owned fake children, local HTTP fixtures and mock time for tool arithmetic.
They never download CPA, use real accounts or run the 37-minute expiry case.
Missing Python is a test failure, not a skipped success.

### Build and isolated execution

Build from a clean **committed** implementation for independent acceptance:

```sh
git ls-files --error-unmatch bin/ci/run-cpa-v8-qualification.mjs bin/ci/prepare-cpa-v8-artifact.py bin/ci/validate-cpa-v8-evidence.mjs
git diff --exit-code HEAD -- bin/ci/run-cpa-v8-qualification.mjs bin/ci/prepare-cpa-v8-artifact.py bin/ci/validate-cpa-v8-evidence.mjs
docker build --platform linux/amd64 \
  --build-arg RUNNER_COMMIT="$(git rev-parse HEAD)" \
  -f tests/fixtures/cpa-v8-qualification/Dockerfile.runner \
  -t cpamp-aq02-tool:reviewed .
docker image inspect cpamp-aq02-tool:reviewed --format '{{.Id}}'
```

The image records the actual Node, Python and libc versions and hashes of the
runner, validator and archive helper. Freeze the resulting image digest before
execution. Rebuilds after package updates are distinct tool identities. A build
argument alone does not prove a commit contains the copied source; independent
acceptance must check both the clean source and recorded file hashes.

Use a new owned host directory with mode 0700 for each run. Place the explicitly
obtained official archive in an immutable owned regular file with mode 0600.
Copy the canonical manifest into that directory, create an empty `inspect.json`
file with mode 0600, and create an empty `output/` directory with mode 0700.
Set the example variables to absolute paths and the frozen local image digest:

```sh
docker create --name "$AQ02_CONTAINER" --platform linux/amd64 \
  --user "$(id -u)" --init --read-only --network none \
  --cap-drop ALL --security-opt no-new-privileges \
  --pids-limit 128 --memory 1g --cpus 2 \
  --mount "type=bind,src=$AQ02_ARCHIVE,dst=/input/CLIProxyAPI_8.0.55_linux_amd64.tar.gz,readonly" \
  --mount "type=bind,src=$AQ02_RUN_DIR/manifest.json,dst=/run/aq02-manifest.json,readonly" \
  --mount "type=bind,src=$AQ02_RUN_DIR/inspect.json,dst=/run/aq02-isolation.json,readonly" \
  --mount "type=bind,src=$AQ02_RUN_DIR/output,dst=/work" \
  "$AQ02_IMAGE_DIGEST" --run --manifest /run/aq02-manifest.json \
  --archive /input/CLIProxyAPI_8.0.55_linux_amd64.tar.gz \
  --output-parent /work --case-set management-r2
docker inspect "$AQ02_CONTAINER" --format '{{json .}}' > "$AQ02_RUN_DIR/inspect.json"
docker start --attach "$AQ02_CONTAINER"
```

The host's read-only inspect document is cross-checked against live Linux
properties: only loopback interfaces, a read-only root, no capabilities and
no-new-privileges. It must declare the frozen image, non-root user, bounded CPU,
memory and PIDs, and exactly the three read-only inputs plus private output
mount. There is no Docker socket, host networking, published port, production
mount or fallback to host execution. Host inspect authenticity still belongs to
the trusted operator and independent acceptance; this is not hostile same-user
process isolation.

For natural expiry, create another new container/output directory and replace
the case set with `oauth-expiry-r2`. Only M32 receives the 37-minute case and
40-minute slice budget; ordinary cases retain 90 seconds / 15 minutes. Requests
remain bounded to five seconds, 32 KiB headers and 1 MiB bodies. Encoded bodies
are rejected; redirects are not followed. The long run polls the same live
Codex state every 15 seconds, preserves pending/error/expiry observations and
checks no credential was saved. It never cancels or restarts the flow to create
an expiry result. Mock time tests do not produce upstream evidence.

### Observation and safety boundaries

- Every case owns a fresh config, secrets, auth directory, CPA process group and
  local OpenAI-compatible stub. The child environment is constructed from an
  empty allowlist. `--local-model`, disabled panel/plugins/discovery and the
  network namespace prevent unrelated services or inherited proxy/store state.
- Management/client/upstream keys are independent. Successful data-plane
  controls must reach the stub; missing/wrong keys must not reach it. A provider
  error is a separate observation. Stub success does not qualify a real provider.
- Startup waits for a TCP listener before its semantic config probe. Only a
  transient transport failure may retry that read once; authentication failures
  and semantic mismatches stop immediately. The complete startup sequence,
  including the no-secret data-plane control, shares a 30-second deadline.
- Config writes require persisted readback and actual client-key behavior.
  Map PATCH preserves unrelated root fields; list PATCH replaces the family.
  JSON root PUT preserves omitted matching TURN secrets; YAML replacement drops
  them. YAML GET is a normalized view and must leave the stored file unchanged.
- Empty/absent/null client-key families are observed explicitly. They must not
  be described as deny-all. Credential upload/status/fields have file and runtime
  postconditions; synthetic credentials do not prove provider usability.
- M20 applies Linux `RLIMIT_FSIZE` only to the recorded CPA child to induce a
  write failure. CPA can restore chmod write permissions, so chmod alone is not
  a valid failure fixture. A failed write may truncate private configuration;
  the replacement client key must not silently become effective.
- M25's offline refresh-error leaf uses an existing name with a mismatched
  auth index. Its positive control proves the credential exists. Real provider
  refresh and M33's complete OAuth exchange/save/replay sequence remain pending.
- Callback acceptance, a temporary callback file and an error callback are
  separate from token exchange and committed credentials. Repeated callback and
  cancellation races record the observed limits without claiming exactly-once.
- Mutation response loss is deliberate client-side discard followed by readback;
  the mutation is sent once. No CAS, operation lookup or automatic replay is
  inferred. Ordinary effective-state polling stays within its case deadline.
- Child logs are discarded with a 1 MiB cap. Graceful shutdown is bounded and
  escalates only for the recorded process group. Incomplete shutdown preserves
  private files and marks cleanup failed. Cancellation retains completed cases
  and any already-observed long-run state; it cannot fill pending results green.

Each run creates a new `run-*` directory with its manifest, sanitized traces,
report and tool/isolation identity. Raw config, auth files, response bodies,
OAuth state/code and child output stay private and are removed only after owned
child and fixture shutdown is proved. Never publish private leftovers after a
cleanup failure. Validate the saved report with the matching manifest and
evidence directory before acceptance.

Exit 0 means only that all selected mandatory assertions passed, setup/cleanup
completed and the evidence validator accepted the report. Failures, aborts,
timeouts or invalid inputs return 1; invalid CLI usage returns 2. Required
unselected cases remain pending. Neither an exit code nor structural counters
sign profile qualification, independent acceptance, Phase4A or production use.

Development runs must retain their original manifest/image/source hashes and
failures. Reports made before the implementation is committed are development
observations; a baseline SHA plus modified source is not final exact-head
acceptance. Commit-bound qualification and independent acceptance remain later
gates. AQ-03/04/05, real provider deltas, product consumers and #870 stay under
their existing owners.

## AQ-01 r1 historical foundation

This is the **r1 evidence-format foundation**. No CPA artifact was downloaded,
started or qualified by this slice. The 16 seed cases declare intent, not complete
surface, field, provider, failure or recovery coverage. Seven profiles have five
mandatory selections and two disabled optional selections.

## Frozen inputs

| Input                                            | Identity                                                          |
| ------------------------------------------------ | ----------------------------------------------------------------- |
| Planning repository commit                       | 85d4f9942049fb3fdbf8358a5d8a9d7624f110bd                          |
| CPA v8 Gate blob                                 | 779860b2126a95b1878af6645497e95419ce1775                          |
| Explicitly selected local v2 implementation base | f5d12c6ca4af1f36f087e8acd28e565c5a0ae655                          |
| Source-observed CPA candidate                    | v8.0.55 at e6e5708e677adb2463d45accdd24bc9fd028ddd8               |
| Candidate artifact                               | Linux amd64 plugin variant, CLIProxyAPI_8.0.55_linux_amd64.tar.gz |
| Declared archive SHA256                          | 4cd593251db85e50c820f42e6ea5cecdd05f902201d3dbdb025671d4f306a123  |

The draft was shaped against v2 at 58076c6e26c372f179695d6754e3dc306190191f.
The selected local base predates PR #888; those five identity-consumer files do
not intersect AQ-01. This fixture records the actual base and does not accept a
production identity consumer or replace its later acceptance.

Routes were read from CPA's internal/api/server_management_v8.go,
internal/api/handlers/management/config_v8.go and internal/api/server_routes.go
at the candidate commit. Digest and routes are declared/source-observed inputs,
not locally observed binary evidence.

## Use

From the repository root:

```sh
node bin/ci/validate-cpa-v8-evidence.mjs --manifest tests/fixtures/cpa-v8-qualification/manifest.json
node bin/ci/validate-cpa-v8-evidence.mjs --manifest /owned/manifest.json --report /owned/report.json --evidence-root /owned/evidence
npx vitest run tests/cpaV8EvidenceContract.test.mjs tests/prCheckClassifier.test.mjs
npm run test:repo
```

Only --manifest, --report and --evidence-root are accepted; report and
evidence-root must appear together. There is no environment credential fallback,
URL/executable input, network, subprocess, download, CPA execution or file write.
The module exposes validateEvidence and createSeedManifest; both are read-only.
The latter returns a fresh copy of the trusted r1 declarations.

| Exit | Meaning                                                          |
| ---- | ---------------------------------------------------------------- |
| 0    | manifest_valid or report_valid: format/references are consistent |
| 1    | invalid data, binding, state, privacy or file boundary           |
| 2    | invalid CLI usage                                                |

The CLI emits one JSON line and no raw stderr. Success does not authenticate
observations or sign acceptance. Fabricated runtime-shaped reports can be
structurally valid; independent acceptance must inspect actual bytes, execution,
postconditions and original traces.

Output counts declared, selected, completed, pending, requiredPending,
setupFailures and cleanupFailures. pending counts uncompleted seed cases;
requiredPending counts required seed cases without completed supported results.
These are structural counters, not qualification. Zero cannot clear a Gate;
cleanup failure remains a blocker despite retained completed observations.
No input-supplied ID, path, secret or arbitrary string is echoed.

## Format and budgets

Manifest, report and traces use UTF-8 without BOM and exactly
JSON.stringify(value, null, 2) plus one LF. Object key order follows the decoded
object. Unknown fields/versions/revisions/identities, duplicate members, unsafe
or noncanonical numbers and noncanonical bytes fail closed. No input is rewritten.

| Object                                         | Limit                |
| ---------------------------------------------- | -------------------- |
| Manifest / report                              | 1 MiB / 4 MiB        |
| Cases / results / evidence entries             | 256 each             |
| Profiles / operations / fixtures               | Exact r1: 7 / 16 / 4 |
| Surfaces per operation / expectations per case | 32 / 32              |
| Profile refs / surface refs per case           | 7 / 32               |
| JSON depth / decoded nodes                     | 16 / 65,536          |
| Generic array elements or object members       | 256 per container    |
| Trace / all traces                             | 1 MiB / 16 MiB       |
| Trace records                                  | 32                   |
| Safe ID / evidence relative path length        | 96 / 256 characters  |

Reads consume at most limit+1 bytes to detect overflow. Inputs must be regular
files. Evidence root must be an owned immutable directory at its real absolute
path, without untrusted concurrent writers. Root/subdirectories cannot be
group/world writable. Files must be owned, not group/world writable, regular,
single-link files. Symlinks, aliases, traversal, URL/encoded paths and observed
path/inode drift fail closed. File identity is checked around the bounded read;
directory identity and real paths are checked around evidence verification.
This does not isolate hostile same-user concurrent processes. Stabilize inputs
before validation; arbitrary shared mutable trees are outside the contract.

Traces allow only schemaVersion, runId, caseRef and records. Each record binds
expectationRef, outcome, safe observationCode, HTTP status and safe integer
elapsed/byte counters. Every executed assertion needs a matching typed record. A
not_run assertion has observationCode NONE and no execution trace. Hash alone,
empty trace or foreign run/case/expectation references cannot support an assertion.
Raw bodies/config/headers, credentials, OAuth state/code and free-form errors are
not shared evidence.

## Proof boundaries and next owners

- Exact r1 operation/profile composition, candidate and fixture inputs are fixed.
  Changes require reviewed scope/revision deltas; optional profiles remain disabled.
- Each composite operation separates method/path/effect/auth. Core Management is
  v8 only. Client-key POST /v1/chat/completions is data-plane auth. Queue GET consumes;
  OAuth auth-url GET starts a flow; neither is probe eligible. Callback uses
  oauth-state authority. Undeclared extensions have no fabricated routes.
- Fixture config/mode IDs declare future isolated categories. Equality does not
  prove effective config/network mode. STAGE later references AUTH/CFG/KEY/CRED/MODEL
  postconditions; plugin consumers similarly consume AUTH/CFG/OAUTH evidence.
- not_run and aborted stay unknown. Supported functions require upstream-real /
  upstream-function, an upstream run, matching observed archive/binary identity,
  passing mandatory assertions and typed traces. Guard/harness cannot support a
  function. Foundation has no verified artifact and its functions remain unknown.
- Optional assertion outcomes remain visible without overruling passing mandatory
  assertions. A completed result cannot carry a not-run or abort reason such as
  NOT_IMPLEMENTED or TIMEOUT; those retain their own execution state and uncertainty.
- AQ-02 owns the real isolated runner and complete management cases; AQ-03 External
  safety; AQ-04 usage/staging failures; AQ-05 extension guards and independent
  pre-4A Go/No-Go. Consumers have separate exits. AQ-01 does not advance Phase4A,
  resume #870 or enable plugins.

## Verification

Tests contain three positive format families: declared seed, not-run foundation
and partial runtime-shaped reports. Runtime-shaped data and hashes are synthetic,
generated in private temporary directories, and are not actual CPA evidence.

V-AQ01-01 through 24 cover canonical/size/schema/ref boundaries, mixed identity,
coverage, surface safety, false support, run state, owned evidence, trace binding,
privacy and tool inputs. Subprocess tests trap network/process/write APIs, observe
limit+1 reads and inject root identity drift. Only the owning harness cleans its
temporary files.

Validator-only changes trigger Frontend/repo checks. Manifest, fixture README and
contract tests already trigger that job via tests/. Changes to the classifier or
its regression test preserve the existing all-checks rule. No workflow or new
dependency is added. Local success is not a GitHub job result: actual PR checks
and independent acceptance remain pending until the exact PR head is verified.

Order: usage; bounded decode/depth/canonical; field/type; ID/ref/coverage;
surface/artifact; state/assertion; evidence/privacy. Fixed diagnostic codes:

```text
USAGE INPUT_INVALID INPUT_LIMIT NON_CANONICAL SCHEMA_VERSION FIELD_REJECTED
ID_OR_REF_INVALID COVERAGE_INVALID SURFACE_POLICY_INVALID ARTIFACT_BINDING_INVALID
RESULT_STATE_INVALID EVIDENCE_INVALID PRIVACY_REJECTED SIDE_EFFECT_REJECTED
```
