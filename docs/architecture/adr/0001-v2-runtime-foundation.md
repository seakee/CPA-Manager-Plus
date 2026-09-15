# ADR-0001: CPAMP 2.0 Runtime Foundation

- Status: Accepted
- Date: 2026-09-14
- Scope: Phase 1 Runtime Foundation

## Context

CPAMP 2.0 separates product control, privileged runtime execution, and model traffic into distinct ownership domains. The goal is to let the gateway keep serving model traffic when the control plane is unavailable, while keeping privileged process and update operations out of the Manager process.

This ADR freezes the minimum architecture needed to begin implementation. It intentionally does not freeze later Gateway Governance, Public API v2, migration UI, or provider-routing policy.

## Decision

### 1. Product and runtime roles

CPAMP uses three runtime roles:

- **Manager** — Control Plane. Owns desired product/runtime configuration, user/admin state, usage/analytics, update recommendation policy, and encrypted product secrets.
- **Runtime Supervisor** — Execution Plane for Embedded mode. Owns privileged lifecycle execution and its private operation journal. It does not own product configuration.
- **CPA** — Data Plane. Owns gateway request handling, provider/credential runtime behavior, and model traffic.

Model traffic MUST follow:

```text
AI Client -> CPA -> Provider
```

Model traffic MUST NOT pass through Manager or Runtime Supervisor.

Manager MAY communicate directly with the CPA Management API for management-domain operations. Runtime Supervisor MUST NOT become a proxy between Manager and the CPA Management API.

### 2. Runtime modes

CPAMP exposes two durable runtime modes:

- `embedded` — CPAMP manages CPA lifecycle through Runtime Supervisor. This is the default/recommended mode.
- `external` — CPAMP connects to a separately managed CPA. This is Advanced Compatibility Mode.

Embedded and External MUST use one application-level `RuntimeClient` contract. Capability negotiation determines which lifecycle/update actions are available. Business code MUST NOT spread mode-specific `if embedded / if external` branches when a capability can express the difference.

Adopting an existing CPA into CPAMP management is a migration path from External to Embedded, not a third runtime mode.

### 3. State ownership

Ownership is fixed as follows:

| State / Action | Authority |
|---|---|
| Runtime mode | Manager |
| Desired CPA connection | Manager |
| CPA Management Key | Manager encrypted storage |
| Desired/recommended version | Manager update policy |
| Product migration checkpoint | Manager |
| Runtime capabilities | Runtime handshake; Manager may cache/project |
| CPA PID / running version / process health | Supervisor / CPA observed state |
| Lifecycle/update operation execution | Supervisor |
| Operation journal / rollback execution state | Supervisor private journal |
| Usage / analytics | Manager |

Invariant:

> Manager owns desired state; Supervisor owns privileged execution journal; CPA/Supervisor expose observed state; Manager reconciles desired and observed state.

Runtime Supervisor MUST NOT read, open, migrate, or depend on Manager SQLite databases or `data.key`.

### 4. Runtime Protocol v1

Phase 1 uses a versioned **HTTP/JSON** Runtime Protocol.

Transport placement:

- Docker Embedded: private Docker network only; no host publication by default.
- Native Linux/macOS/Windows: loopback by default.

The protocol MUST be authenticated per installation and MUST support explicit timeouts. The base observation surface contains handshake, protocol version, runtime identity/generation, capabilities, status, and running CPA version. Lifecycle mutations are added as operation-specific typed endpoints rather than a generic action API.

#### Runtime generation

Under the mutation-capable Runtime Protocol contract, `RuntimeGeneration` is the **Supervisor execution authority epoch** for one Runtime Supervisor process incarnation. Runtime Supervisor owns the current generation. Manager only observes it through handshake/status, may cache it, and echoes it as a mutation precondition. Manager MUST NOT create, increment, or persist a generation as authority.

A mutation-capable Supervisor MUST establish a new execution authority epoch and freshly sample a cryptographically random, non-zero, opaque `uint64` generation for each process incarnation before serving handshake/status or accepting mutations. Phase 1 treats accidental numeric collision as negligible and does not introduce a durable generation registry or counter solely to prove uniqueness; an implementation MUST NOT intentionally reuse a known generation. Generation is compared for equality only; it has no ordering, monotonic-counter, business-version, desired-state-revision, database-generation, CPA PID, CPA-version, or CPA-restart-count semantics.

Once mutation capability is enabled, CPA stop, start, restart, crash recovery, or binary replacement does not by itself change generation while the same Supervisor process remains the execution authority. After Supervisor restart, Manager MUST observe handshake/status again before submitting another mutation instead of intentionally reusing its cached pre-restart generation. Under Phase 1's random-epoch model, the freshly sampled value makes that cached value stale except for the accepted negligible collision probability.

#### Mutation operation envelope and fencing

Every mutation request MUST combine common mutation metadata with a typed operation request. The common metadata is:

- `operationId`: created by Manager, opaque to Supervisor, non-empty, stable across retries of the same logical mutation, and no more than 128 UTF-8 bytes. The protocol does not require a UUID format.
- `expectedRuntimeIdentity`: the Runtime identity most recently observed by Manager.
- `expectedRuntimeGeneration`: the Runtime generation most recently observed by Manager.

Mutation requests MUST use operation-specific typed payloads. An open-ended `action` plus arbitrary `params` map is not part of Runtime Protocol v1.

The first lifecycle mutation is typed Start at `POST /v1/runtime/operations/start`. Its request contains only the common mutation envelope because Start has no caller-controlled payload in this phase. In particular, Runtime Protocol callers MUST NOT supply an executable path, argv, shell command, environment, or working directory. The CPA executable used by Start is Supervisor-local execution configuration. A successful Start operation means the OS process was spawned and ownership was published; it MUST NOT be interpreted as listener readiness, CPA Management readiness, running-version verification, or overall Runtime readiness.

Supervisor's durable idempotency namespace is `(RuntimeIdentity, operationId)`. If a private journal is permanently scoped to one immutable Runtime identity, `operationId` alone may be its physical key, but the protocol semantics are the same. `RuntimeGeneration` records the execution authority epoch in which an operation was created; it MUST NOT partition or reset the durable idempotency namespace.

For idempotency comparison, the logical request consists of the operation type and its typed payload. `expectedRuntimeIdentity` selects the Runtime namespace, while `expectedRuntimeGeneration` is a freshness precondition for the current submission; changing only that expected generation after re-observation does not make the logical request different.

Across all retained generations for the same Runtime identity:

- A previously unseen operation ID with valid current Runtime identity/generation is durably recorded before any side effect.
- Replaying an existing operation ID with the same logical typed request returns its existing operation/result state, including the generation in which it was created, and MUST NOT execute the side effect twice.
- Reusing an existing operation ID for a different operation type or typed payload fails with `operation_id_conflict`.

For the lifetime of a Runtime identity, an operation ID is not reusable. Retention or compaction MUST NOT make a recorded operation ID appear previously unseen. Detailed result data may be discarded only if a durable tombstone preserves enough request-identity and outcome metadata to maintain replay behavior, idempotency, and conflict detection.

Mutation submission order is fixed:

1. Authenticate.
2. Decode and perform basic request validation.
3. Compare `expectedRuntimeIdentity` with the current identity.
4. Compare `expectedRuntimeGeneration` with the current generation.
5. Resolve operation ID against the durable cross-generation idempotency namespace.
6. Evaluate operation-specific preconditions for a new operation.
7. Durably record intent for a new operation.
8. Perform the side effect.

An identity mismatch fails with `runtime_identity_mismatch`; a generation mismatch fails with `stale_runtime_generation`. Both checks happen before idempotency lookup, durable intent, and side effects. Consequently, replaying a request that still carries a previous generation is rejected as stale. After Manager observes the current generation and retries the same logical request with the same operation ID, the durable lookup finds the earlier-generation record: an identical request returns the existing state, while a different request fails with `operation_id_conflict`. A generation change alone MUST NOT create or execute the operation again. A future, separate operation-observation API is responsible for querying operation status without resubmitting a mutation.

Runtime Protocol v1 keeps the existing JSON error envelope with a stable code and a human-readable message. The code is the protocol contract; clients MUST NOT branch on or otherwise depend on message wording. Mutation application errors and their HTTP status are:

| HTTP status | Error code | Meaning |
|---|---|---|
| `400` | `invalid_request` | Malformed or invalid mutation request. |
| `400` | `unsupported_operation` | The typed operation is not supported by this Runtime. |
| `409` | `runtime_identity_mismatch` | The request targets a different Runtime identity. |
| `409` | `stale_runtime_generation` | The request targets a different Supervisor authority epoch. |
| `409` | `operation_id_conflict` | The operation ID already names a different logical request for this Runtime identity. |
| `409` | `operation_state_conflict` | The typed operation is incompatible with the current operation/runtime state. |
| `503` | `operation_persistence_unavailable` | Durable operation state cannot be committed, so no side effect is performed. |
| `500` | `internal_error` | An unexpected internal failure occurred. |

HTTP authentication failure remains the existing `401` protocol behavior and does not introduce a parallel domain authentication error system. Future typed operations may define additional stable application codes without changing this common envelope.

A mutation result contains at least `operationId`, `operationType`, `runtimeIdentity`, `runtimeGeneration`, `state`, and an optional structured reason/error with a stable code and a human-readable message. Message text is not a stable API field. The initial state vocabulary is `accepted`, `running`, `succeeded`, and `failed`. This contract does not introduce progress percentages, streaming, queues, or a workflow engine.

Unix Domain Sockets and Windows Named Pipes are deferred. They may later be introduced as transport adapters without changing protocol semantics.

### 5. Supervisor operation journal

Runtime Supervisor uses its own small local SQLite database for durable operation state.

The journal is local runtime state, not product data. It MUST NOT be placed on shared/NFS storage and MUST NOT contain Manager product configuration, analytics, or long-lived product secrets.

Privileged mutations obey:

> durable intent before side effect

If an operation cannot be durably recorded, Supervisor MUST NOT perform binary replacement, process switching, rollback mutation, or other privileged filesystem side effects.

The initial journal needs only operation-oriented fields such as operation ID, type, Runtime identity, creation generation, enough typed-request data to detect conflicting operation ID reuse, expected/current target version, state, timestamps, rollback reference, and structured error code. Records created under an earlier generation MUST NOT be deleted or excluded from idempotency lookup merely because Supervisor starts with a new generation; recovery and observation of those records are later tasks.

### 6. Secret ownership

CPA Management Key remains a Manager-owned product secret and is stored using Manager encrypted storage.

For Embedded provisioning or mutation, Manager sends only the minimum execution material required by the current authenticated operation. Supervisor may hold that material in memory and apply it to CPA runtime configuration, but it MUST NOT become the long-term authority for the secret.

Secrets MUST NOT be written to the Supervisor operation journal, progress events, or logs.

### 7. Readiness and crash-loop fencing

Runtime readiness is based on deterministic runtime conditions, not a real provider/model request.

A CPA runtime becomes ready only after the required local checks succeed, including:

1. CPA process is alive.
2. Expected listener is ready.
3. CPA management/status probe succeeds.
4. Running version matches the expected operation state when version is part of the operation precondition.

Provider availability, quota state, credential cooling, or upstream network failure MUST NOT make the CPA process itself "not ready". These belong to gateway/provider health.

Supervisor performs bounded recovery. Repeated exits before reaching readiness MUST eventually enter a structured crash-loop/manual-intervention state rather than restarting forever. Phase 1 begins with a simple bounded retry budget; complex adaptive recovery is out of scope.

### 8. Platform topology

The logical architecture is identical across platforms. Platform differences are deployment adapters only.

#### Docker Embedded — Phase 1 priority

```text
cpamp-manager container
  -> Manager

cpamp-runtime container
  -> Runtime Supervisor (PID 1)
      -> CPA child process
```

Manager data and Runtime data use separate storage ownership. Runtime Supervisor does not require Docker socket access to manage CPA.

Docker Phase 1 has two deployment/container failure domains:

1. `cpamp-manager` container.
2. `cpamp-runtime` container.

Within `cpamp-runtime`, Runtime Supervisor and CPA remain separate processes with distinct roles, ownership, state, and authority, but they share the Runtime container failure domain. Phase 1 does not guarantee that the CPA child survives Runtime Supervisor PID 1 exit or Runtime container crash, stop, or kill.

Phase 1 does not introduce a third CPA container, Docker socket orchestration, or another sidecar/controller to manufacture an additional deployment failure domain.

#### Native Linux

```text
systemd: CPAMP Manager
systemd: CPAMP Runtime Supervisor
         -> CPA child process
```

#### Native macOS

```text
launchd: CPAMP Manager
launchd: CPAMP Runtime Supervisor
         -> CPA child process
```

#### Native Windows

```text
Windows Service: CPAMP Manager
Windows Service: CPAMP Runtime Supervisor
                 -> CPA child process
```

Full Docker Embedded is the Phase 1 delivery target. Full Native Embedded follows after Runtime Foundation is stable and is not a Phase 1 start blocker.

Future Native topology may provide different OS/service failure and survival semantics. Docker Phase 1 does not depend on those semantics.

### 9. Failure-domain requirements

The architecture distinguishes application behavior invariants from deployment survival guarantees.

Application behavior invariants:

- Manager crash, restart, or temporary unavailability MUST NOT cause the control path to intentionally terminate a healthy CPA gateway. While the Runtime container remains healthy, model traffic continues directly through `AI Client -> CPA -> Provider` without traversing Manager.
- CPA child crash, exit, or readiness failure MUST NOT make Manager Console/API unavailable. Manager MUST be able to eventually observe and report the CPA runtime condition; the concrete lifecycle states are defined by later lifecycle work.
- Runtime Supervisor MUST NOT intentionally terminate a healthy CPA merely because Manager disconnects, a status request fails, ordinary reconciliation fails, or the control path has a transient failure.
- Manager health MUST be independently observable from CPA runtime health.

Deployment survival guarantee:

- Manager container failure MUST NOT stop an otherwise healthy Runtime container. Docker Phase 1 treats Manager and Runtime as separate deployment/container failure domains; this does not claim independence from a shared host or container-engine failure.
- Docker Phase 1 does not guarantee CPA child survival after Runtime Supervisor PID 1 exits or the Runtime container crashes, stops, or is killed. Supervisor and CPA share that deployment failure domain.

Logical ownership, security/authority, state ownership, process role, and deployment failure domain are separate architectural dimensions. Sharing the Runtime container failure domain does not merge ownership: Runtime Supervisor remains the Execution Plane, and CPA remains the Data Plane.

### 10. Update ownership

- Manager decides update recommendation/channel/policy.
- Supervisor executes exact-version CPA runtime operations, staging, switch, readiness verification, and rollback.
- Manager and CPA updates are independent recovery domains; `Update All` is orchestration, not one atomic rollback transaction.
- Supervisor self-replacement is not part of the first Runtime Foundation implementation. Supervisor is updated by the outer CPAMP package/container/install mechanism.

## Phase 1 implementation boundary

Phase 1 MUST establish:

- `RuntimeClient` application port.
- Embedded and External adapters.
- Runtime Supervisor executable boundary.
- Runtime Protocol v1 handshake/status/capability slice.
- Supervisor private durable operation journal.
- Full Docker Embedded lifecycle foundation.
- Tests enforcing failure-domain and storage-ownership invariants.

Phase 1 does NOT require:

- Full Public API v2.
- Migration Wizard or new-install UI.
- Full Native Embedded lifecycle.
- Unix socket / named-pipe transport.
- Supervisor self-update.
- Key-to-Credential hard routing.
- Gateway Governance / policy enforcement.
- Provider/model requests as readiness checks.
- Full runtime updater in the first skeleton PR.

## Consequences

This design introduces a third process role in Embedded mode, but keeps it intentionally thin. The benefit is an explicit execution boundary without making Supervisor another product backend or putting model traffic through CPAMP.

HTTP/JSON is chosen for Phase 1 implementation speed, portability, testability, and observability. The protocol semantics are independent from transport so a stronger local IPC transport can be added later if required.

A separate local SQLite journal adds a small persistence component, but avoids unsafe ad-hoc JSON persistence and prevents Supervisor from depending on Manager databases.

## Non-negotiable invariants

1. Client model traffic never traverses Manager or Supervisor.
2. Supervisor never opens Manager databases.
3. Supervisor is not product configuration or long-lived secret authority.
4. Manager may access CPA Management API directly; Supervisor is lifecycle execution, not a management proxy.
5. Embedded and External share the same application-level RuntimeClient contract.
6. Privileged runtime side effects require durable operation intent first.
7. Docker Phase 1 keeps the Manager and Runtime containers as separate deployment failure domains; Supervisor and CPA keep distinct roles, ownership, state, and authority within the shared Runtime container failure domain.
8. Before mutation capability is enabled, each Runtime Supervisor process incarnation must establish a new authority epoch by freshly sampling an opaque random generation; Manager only observes and echoes it.
9. Mutations fence both Runtime identity and generation before idempotency resolution, durable intent, or side effects.
10. For one Runtime identity, durable operation ID idempotency spans Supervisor generations: replaying the same logical request never executes its side effect twice, and conflicting reuse fails closed.