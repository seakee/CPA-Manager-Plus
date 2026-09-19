# Phase 2 artifact and request-lifecycle evidence contract

This document defines the evidence contract shared by Phase2-02 and Phase2-03. It is an evidence fixture, not a product capability declaration. The machine-readable source of truth is [`tests/fixtures/phase2-evidence/contract.json`](../../tests/fixtures/phase2-evidence/contract.json), validated against its adjacent JSON Schema.

## Frozen artifact baseline

| Evidence class            | CPAMP / CPA identity                                                                                            | Deployment meaning                                                                            | Linux release assets                                                                                                                                             |
| ------------------------- | --------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Current bundled           | CPAMP `v2@2850980d3d08fa30878744e1d67fc04607504c01`; CPA `v7.3.3` at `7bbfeaf8a7acf2cd5a834dcb0842539fe6aabc2b` | Current Embedded product behavior                                                             | amd64 `7af8c99cd08eee3ccc81d1596e8a31785674d3de6bd7ec61416d59493dd8fc01`; arm64/aarch64 asset `5f320e3fae52af00f07b78201311e9d096b36e759441d948de48a10f49e71883` |
| Candidate release fixture | CPA `v7.3.8` at `c93978c4ea2e908255a2a06c37599fda3651554a`                                                      | Exact official release used for Phase 2 capability evidence; it is not a CPAMP bundle upgrade | amd64 `3fe5228c458624175d5e4e81d9dd003d82de688ca498fa368a790ea120bda0e3`; arm64/aarch64 asset `8d09ce286d857b2d0e6d77c39e08a755246120df58c02a115d58c391fc73e3f1` |
| External                  | Version, artifact, plugin availability, and plugin configuration are unnegotiated                               | Separately managed CPA                                                                        | Unknown                                                                                                                                                          |

The official asset names, sizes, tag commits, and SHA-256 values are in the fixture. `Dockerfile.runtime` remains pinned to v7.3.3. A newer CPA release does not move this contract from v7.3.8.

`SchedulerAcrossPriorities` originates at `b715526add0c452acc62062bf4fcef53897be604`. Git ancestry gives the frozen provenance:

| CPA release | Tag commit                                 | Contains `b715526a` |
| ----------- | ------------------------------------------ | ------------------- |
| v7.3.3      | `7bbfeaf8a7acf2cd5a834dcb0842539fe6aabc2b` | No                  |
| v7.3.7      | `b773607e3e7756dc6020a291825e4eb08899595a` | Yes                 |
| v7.3.8      | `c93978c4ea2e908255a2a06c37599fda3651554a` | Yes                 |

The v7.3.8 Go capability is `Capabilities.SchedulerAcrossPriorities`; its RPC field is `scheduler_across_priorities`. Its zero value is `false`. With `false`, the scheduler receives the filtered highest available priority tier. With `true`, it receives filtered candidates across priority tiers. Provider/model eligibility, disabled state, and cooldown filtering still happen first. Candidate visibility does not establish safe hard pinning.

Both v7.3.3 and v7.3.8 declare Plugin ABI `SchemaVersion uint32 = 6` in `sdk/pluginabi/types.go`. This schema version is pinned independently from CPA artifact identity and plugin configuration. The unchanged schema version does not prove that `SchedulerAcrossPriorities` exists: v7.3.3 uses schema 6 without that capability, while v7.3.8 uses schema 6 and contains it. Capability conclusions must therefore retain all three dimensions: exact CPA artifact/version, Plugin ABI schema, and the relevant plugin capability/config.

## Canonical request lifecycle

The shared stage vocabulary remains:

```text
request_received
  -> client_api_key_resolved
  -> model_resolved
  -> credential_candidates_generated
  -> disabled_cooldown_priority_filtering
  -> credential_selected
  -> provider_endpoint_resolved
  -> upstream_request_stream
  -> retry_fallback
  -> response_cancel_reject_failure
  -> usage_accounting_settlement
```

CPA source adds constraints that the linear vocabulary alone cannot express:

1. Access authentication runs in Gin middleware before the handler creates its request lifecycle tracker or calls the before-auth request interceptor.
2. Model normalization and provider-route resolution happen before credential candidates are built. The concrete endpoint remains executor-specific after credential selection.
3. Candidate construction and disabled/cooldown/provider/model/priority filtering form one host boundary. A scheduler plugin never receives the unfiltered credential inventory.
4. Retry and fallback re-enter candidate filtering, selection, and upstream execution for each attempt.
5. Usage publication occurs inside executor attempts. CPA does not establish a universal ordering in which usage settlement follows the terminal lifecycle callback, and the callback is completed usage observation rather than reservation/settlement.

| Stage                                  | Auth placement                                  | Plugin observation/control                                                                | Primary source evidence                                                | Contract limit                                                          |
| -------------------------------------- | ----------------------------------------------- | ----------------------------------------------------------------------------------------- | ---------------------------------------------------------------------- | ----------------------------------------------------------------------- |
| `request_received`                     | Before client auth                              | `FrontendAuthProvider` can observe/reject; no raw `RequestInterceptor` hook               | `internal/api/server_middleware.go`; `handlers_execution.go`           | Request interception starts later                                       |
| `client_api_key_resolved`              | Client auth                                     | Access provider participates; request interceptor does not directly receive the principal | `accessAuthMiddleware`; `config_access.provider.Authenticate`          | Stable caller correlation is Phase2-02                                  |
| `model_resolved`                       | Before credential auth                          | Request/model metadata is visible; interceptor may rewrite or terminate                   | `executeWithAuthManagerFormats`; `applyRequestInterceptorsBeforeAuth`  | This alone is not an arbitrary routing contract                         |
| `credential_candidates_generated`      | Before credential auth                          | Unfiltered inventory is not exposed                                                       | `conductor_selection.go`                                               | Plugin cannot restore excluded credentials                              |
| `disabled_cooldown_priority_filtering` | Before credential auth                          | Scheduler sees the post-filter result                                                     | `availableAuthsForRouteModelWithPriorityMode`; `isAuthBlockedForModel` | Exclusion reasons are not part of the scheduler request                 |
| `credential_selected`                  | After credential auth                           | Scheduler may choose only a supplied candidate                                            | `pickViaPluginScheduler`; `pluginhost.Host.PickAuth`                   | Retry fencing and missing-target behavior remain Phase2-02              |
| `provider_endpoint_resolved`           | Spans route-before-auth and endpoint-after-auth | Route/executor hooks can observe or control their declared slice                          | `providersForExecution`; auth execution loop                           | The label spans two real boundaries                                     |
| `upstream_request_stream`              | After credential auth                           | Declared interceptor/executor hooks can observe, mutate, or terminate                     | `Manager.Execute`; `executeStreamWithModelPool`                        | Availability depends on the exact plugin contract and route             |
| `retry_fallback`                       | Repeated after credential auth                  | Each scheduler invocation sees that attempt's filtered candidates                         | `conductor_execution.go`; `conductor_stream.go`                        | Credential/provider may change between attempts                         |
| `response_cancel_reject_failure`       | Terminal                                        | Asynchronous, observable, immutable, terminal-only                                        | `requestLifecycleTracker.complete`; handler interceptor tests          | Cannot change an already completed result                               |
| `usage_accounting_settlement`          | Attempt-coupled                                 | Completed usage is observable and immutable                                               | `UsageReporter.Publish`; `usage.Manager.Publish`                       | No reservation, exactly-once settlement, or terminal ordering is proven |

Every stage in the fixture includes host/plugin `observable`, `mutable`, `blockable`, and `terminalOnly` flags; available identity/metadata; exact artifact scope; and source/test anchors. Source anchors bind each conclusion to a CPA commit, so a v7.3.8 observation cannot be inherited by v7.3.3 or an unknown External runtime.

## Capability evidence vocabulary

Every C2/C3 capability record must use exactly one status:

- `supported`
- `partial`
- `unsupported`
- `unknown`
- `requires_upstream`

A record must include `capability`, `status`, `artifactId`, plugin schema/config when applicable, `deploymentMode`, one or more evidence kinds (`source`, `unit`, `black-box`, `integration`), evidence references, and limitations. The validator rejects other status words and rejects promotion of an unversioned External record above `unknown` or `partial`.

Current bundled, candidate release, and External records are distinct artifact IDs. Candidate evidence never changes current product capability. External evidence stays unknown until its version, plugin availability, plugin config, and relevant capability are actually observed.

## Offline fixture and local acceptance

The required check is deterministic and performs no network access:

```bash
npm run evidence:phase2
npx vitest run tests/phase2EvidenceContract.test.mjs
```

The same harness can verify a local CPA checkout without fetching refs. It reads the exact commits recorded in the fixture, checks every source symbol, and verifies the scheduler commit ancestry:

```bash
npm run evidence:phase2 -- \
  --cpa-source /Users/seakee/WorkSpace/Golang/src/github.com/seakee/CPA
```

Official release archives may be supplied locally. The harness never downloads or commits them:

```bash
npm run evidence:phase2 -- \
  --artifact candidate-linux-amd64=/local/path/CLIProxyAPI_7.3.8_linux_amd64.tar.gz
```

C2/C3 add their own evidence without editing the frozen baseline. Each append-only extension contains new anchors and records:

```json
{
  "anchors": [
    {
      "id": "phase2-02-candidate-selection-black-box",
      "artifactId": "candidate-release-v7-3-8",
      "evidenceKind": "black-box",
      "evidenceReference": "tests/fixtures/phase2-02/candidate-selection.test.mjs#selects-target",
      "limitations": ["Fixture-only observation; no Hard Routing decision is implied."]
    }
  ],
  "records": [
    {
      "id": "phase2-02-candidate-selection-evidence",
      "capability": "candidate_selection_evidence",
      "status": "partial",
      "artifactId": "candidate-release-v7-3-8",
      "pluginContract": {
        "schemaVersion": 6,
        "config": { "scheduler": true, "scheduler_across_priorities": true }
      },
      "deploymentMode": "release-artifact-fixture",
      "evidenceKind": ["source", "black-box"],
      "evidenceRefs": ["plugin-abi-schema-v6", "phase2-02-candidate-selection-black-box"],
      "limitations": ["Hard Routing remains a Phase2-02 decision."]
    }
  ]
}
```

Validate it with:

```bash
npm run evidence:phase2 -- --evidence tests/fixtures/phase2-02/c2-evidence.json
```

Extension anchor and record IDs must be new. An extension record may reference baseline anchors and anchors declared in the same extension. Every extension artifact ID must resolve to the frozen Phase2-01 artifacts. The extension schema has no artifact, vocabulary, lifecycle, or provenance fields, so it cannot replace those baseline sections. This lets Phase2-02 and Phase2-03 own separate evidence files and validate independently without modifying `contract.json`.

A failed optional source, artifact, or extension check exits only this evidence command; no production package imports the harness.

## Deferred decisions

This contract does not decide Hard Routing, credential pinning/fencing, Quota enforcement, or final capability Go/No-Go. Those decisions remain respectively in Phase2-02, Phase2-03, and Phase2-04. It also does not install a Bridge Plugin or give a plugin scheduler production ownership.
