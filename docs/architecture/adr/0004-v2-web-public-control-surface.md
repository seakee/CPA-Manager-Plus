# ADR-0004: CPAMP 2.0 Web and Public Control Surface

- Status: Accepted
- Date: 2026-09-15
- Scope: Phase 1 public ingress + Phase 2–4 Web/Foundation contracts
- Related: ADR-0001 Runtime Foundation, Issue #171

## Context

CPAMP 2.0 has two requirements that must be solved together:

1. Embedded mode presents one product and one default public endpoint (`:18317`) while keeping Manager, Runtime Supervisor, and CPA/Gateway ownership isolated.
2. The 2.0 Full Web must become a real multi-resource frontend project instead of remaining constrained by the 1.x single-file `management.html` delivery model.

A third historical requirement, originally tracked as “custom frontend base path”, becomes architecturally important under the single-public-ingress topology: users may choose a non-default Control Plane route prefix to reduce commodity scanning/probing noise and to support sub-path deployment without making Ingress understand every future Console route.

This ADR freezes the public route, browser application, transport, and Web delivery boundaries. It does not define visual design, page IA details, Public API v2 resource schemas, or the final setup wizard UX.

## Decision

### 1. One public endpoint, separate internal failure domains

Embedded production exposes one default public endpoint:

```text
http://<host>:18317
```

Internally:

```text
Client / Browser
      |
      v
CPAMP Ingress :18317 public
   |                     |
   | Control             | Gateway/model traffic
   v                     v
Manager :18317        CPA/Gateway :8317
internal              internal
                         ^
                         |
              Runtime Supervisor :9081
                    internal
```

Ingress is a stateless L7 transport multiplexer. It does not own product configuration, Runtime lifecycle, provider routing, credential selection, policy, accounting, or product secrets.

This ADR amends ADR-0001's model-traffic diagram from:

```text
AI Client -> CPA -> Provider
```

to the public-deployment form:

```text
AI Client -> CPAMP Ingress -> CPA/Gateway -> Provider
```

The invariant from ADR-0001 remains unchanged: model traffic MUST NOT pass through Manager or Runtime Supervisor.

### 2. Configurable canonical Control Base Path

The long-term Control Plane public surface MUST live under one canonical configurable path prefix:

```text
controlBasePath
```

Example:

```text
controlBasePath = /7f3a9c

:18317
├─ /7f3a9c/*        -> Manager / CPAMP Control Plane
└─ everything else -> CPA / Gateway Data Plane
```

The long-term Ingress classifier is therefore conceptually:

```text
matches effective controlBasePath -> Manager
otherwise                         -> Gateway
```

A very small number of fixed transport/liveness paths may exist when they do not disclose product-sensitive state, but Full Web route growth MUST NOT require an ever-growing Ingress allowlist.

Phase4-02 adds exactly two fixed immutable brand-resource exceptions: exact
`/favicon.ico` and `/apple-touch-icon.png`, GET/HEAD only, are Manager-owned
independently of Control activation. Missing/disabled assets return a local
404, never SPA HTML or a Control-prefix redirect. Other methods, encoded
variants, subpaths and lookalikes receive no exception and retain normal
classification. Control-prefix collision validation reserves these two paths.
The single Phase4 route-owner fixture records this successor contract; this
does not create a generic public static namespace.

`controlBasePath` is an attack-surface and scan-noise reduction measure. It is NOT authentication or authorization and MUST NOT weaken normal security controls.

### 3. Authority and configuration ownership

- Manager owns desired `controlBasePath` as product/runtime configuration authority.
- Ingress consumes only the effective routing value.
- Ingress MUST NOT read Manager SQLite or become product desired-state authority.
- Browser Router basename, Public CPAMP API base, static-asset base, Manager SPA fallback, and Ingress route ownership MUST derive from the same canonical value/contract.
- Changing the prefix requires a validated configuration operation with collision checking and eventual apply/health/commit-or-rollback semantics. The exact reload/distribution mechanism is deferred to the appropriate Foundation/Setup implementation slice.

The canonical contract MUST reject ambiguous forms, including root `/`, dot-segment traversal, encoded path bypasses, duplicate-slash ambiguity, and trailing-slash multi-meaning. It MUST detect collisions with fixed transport paths and Gateway protocol ownership.

Phase4-02 limits canonical prefix characters to ASCII letters, digits and
`-._~$&+,/:;=@`, which remain unchanged in both Go and browser URL path
serialization. Prefixes requiring escaping (including Unicode literals) are
rejected during authority validation, not normalized into a second mount.

### 4. Two Browser Applications, not one mode-switched application

The `apps/web` source workspace may remain shared, but CPAMP 2.0 has two browser applications:

#### Full Web

- CPAMP product Console.
- Multi-resource SPA.
- Runs under `controlBasePath`.
- Consumes Public CPAMP API v2 only.
- Uses route-level code splitting and hashed assets.
- Does not hold or call with a CPA Management Key.

#### Panel Lite

- CPA-hosted management panel compatibility application.
- Single-file `management.html`.
- Consumes CPA Management API only.
- Does not require CPAMP Manager/Runtime to provide approved CPA-local management tasks.
- Does not consume CPAMP `controlBasePath`; its host path remains a CPA Panel contract.

Full Web and Panel Lite MAY share design tokens, UI primitives, i18n infrastructure, and transport-agnostic pure code. They MUST NOT share an implicit browser transport, auth/session authority, base URL authority, secret authority, or mutation contract.

The implementation MUST avoid spreading runtime `if (panelMode)` branches through business pages. Build entry, feature registry, and browser transport boundaries are explicit.

### 5. Migration/Setup is a Full Web Bootstrap Shell

Migration/Setup is independent of the main Console layout but is NOT a third browser application or a third build artifact.

It belongs to the Full Web build as a Bootstrap Shell and must be able to load before large Console features. Heavy unrelated chunks such as Usage Analytics, Monitoring, charts, and editors should not be required for bootstrap/migration.

The setup/migration contract owns the user-facing lifecycle for generating, selecting, or changing `controlBasePath`. The product should keep initialization UI-first and should not require ordinary users to retrieve hidden initialization values from logs/console output.

### 6. Legacy fixed-path discovery must not defeat the prefix

On a standalone/CPA-hosted deployment:

```text
<CPA endpoint>/management.html -> Panel Lite
```

This remains valid.

For CPAMP Full Web, `/management.html` is not the long-term primary entry. After initialization, fixed public aliases such as `/`, `/management.html`, or other predictable paths MUST NOT by default redirect to and reveal the configured `controlBasePath`.

If an upgrade path needs a legacy redirect, it must be explicit, disable-able, and lifecycle-bounded rather than a permanent discovery endpoint.

A temporary bootstrap discovery surface may exist before initialization; its lifecycle ends when normal Control Plane routing is configured.

Phase4-03 bounds that lifecycle to the Manager-owned namespace
`/.well-known/cpamp-bootstrap`: exact root remains GET/OPTIONS non-secret
discovery, `/ui` and `/assets/` load the same Full Web build's technical
Bootstrap graph, and `/api/v2/` carries only Fresh claim/session operations.
Ingress captures the whole namespace including unknown and ambiguous aliases;
there is no Gateway fallback, canonical-prefix redirect or public prefix
disclosure. The fixed API accepts only a short-lived Fresh Bootstrap session,
never Admin Key or normal Console authentication. A verified initial canonical
route commit retires the entire namespace to generic 404 immediately, before
Gateway readiness or Fresh strategy completion if necessary.

The product journey remains one installation wizard: choose the CPA strategy,
configure the administrator key, management entry and CPA connection, review,
then execute to completion. Configuration is a browser-memory draft until the
user confirms execution. Ordinary Console login is not an intermediate setup
step. New installations normally commit the CPA strategy before activating the
management entry.

Route cutover retires the fixed carrier, not the original installation's
short-lived authority. The same Fresh session may continue at the independently
verified canonical entry, limited to its operation, strategy, Runtime observation
and final completion. It retains the original expiry and claim generation; it
cannot initialize another administrator, change routing, access Console/CPA
resources or become an Admin session. After completion it may only read that
operation's receipt until expiry, so a lost completion response can be reconciled.
The browser then enters the existing administrator login and Console. Ordinary
updates of a completed installation preserve that state and must not repeat the
installation wizard. A major v1-to-v2 migration instead requires existing-Admin
authentication and an explicit migration decision; it never consumes a Fresh
claim or creates a replacement administrator key.

#### Existing-Manager authentication leaf (2026-10-23)

Before writable startup, the exact `POST
/.well-known/cpamp-bootstrap/upgrade/authenticate` leaf accepts the **existing
Manager administrator key** in `Authorization: Bearer <key>`. It is outside
`/api/v2/`; Fresh handlers still reject Admin credentials. This leaf creates no
session, claim, backup, migration, route or completion receipt. CPA keys, cookies
and browser logged-in flags are not credentials for it.

The leaf uses the current bounded, fenced, no-migrate source inspection and the
persisted credential verifier. Only a safely observed, initialized legacy or
unowned native-v2 source projecting `unsupported_upgrade` can authenticate;
Fresh-owned sources remain unavailable. Its success DTO is
`{contract_version:"cpamp.upgrade-auth.v1", authenticated:true,
source_revision:<opaque observation id>, status:"unsupported_upgrade",
migration_available:false, expires_in_seconds:60}`. It discloses no inventory,
paths, CPA endpoint or canonical entry. The identifier detects source/credential and
process changes; it grants no authority. Credential revision is compared internally;
source identity uses the existing file identity/size/mtime and schema witness,
not a complete content hash or backup-consistency proof. Revalidation supplies the original key
and `X-CPAMP-Upgrade-Source`; a changed observation returns 409. Refresh/restart
must reverify the credential, not reuse a local authenticated flag. The browser
keeps its observation in memory for at most 60 seconds and revalidates after
30 seconds, on foreground return and on explicit refresh. Late responses are
discarded; failure revokes the local observation and returns to the existing
login page. Remembered credentials use the existing auth storage format, never
an additional account/session authority. Old entries without an explicit
same-origin Manager base/runtime provenance require manual login.

Errors use `{contract_version:"cpamp.upgrade-auth.v1", error:{code:<code>}}`:
401 `admin_unauthorized`, 403 `origin_rejected`, 409 `source_changed`,
503 `source_unavailable`, 405 `method_not_allowed` (`Allow: POST`). Browser
requests must be same-origin; there is no CORS/cookie authentication. The
origin is checked against the existing trusted `CPAMP_PUBLIC_ORIGIN` deployment
setting when supplied (including TLS termination), otherwise direct TLS/Host.
Forwarded headers cannot establish this trust. Query
strings, unknown paths and aliases are generic 404. Responses are `no-store`.
Writable startup, readiness, Fresh takeover or canonical commit/completion
retires this leaf to the captured generic 404 behavior. It never delegates to
Gateway, normal login/config loading, `store.Open`, bootstrap initialization or
collector startup. This narrow auth contract does not resolve the future
migration executor or widen the Fresh-only fixed API.
The fixed asset carrier admits only the Upgrade entry's static manifest closure
in addition to its initial graph; dynamic Console imports remain unavailable.

Administrator initialization is independent of Bootstrap classification. A
matching durable Fresh installation, generation and OperationID checkpoint can
retain `Fresh` in progress after administrator initialization. Administrator
presence or native-v2 schema alone never supplies that evidence; unknown or
pre-existing native-v2 remains fail closed. Unauthenticated discovery during
this continuation returns the existing unavailable envelope; only the current
Fresh session or a host-issued same-operation resume can continue.

### 7. Full Web delivery is multi-resource

The production Full Web artifact is structurally:

```text
index.html
assets/app-<hash>.js
assets/app-<hash>.css
assets/<route>-<hash>.js
...
```

The production Full Web build MUST NOT be constrained by the Panel Lite single-file requirement. Specifically, 1.x-style global single-file packaging, huge asset inlining, disabled CSS splitting, and globally disabled code splitting are not the target Full Web contract.

Manager may embed/package the complete Full Web `dist` inside its binary/image to preserve a simple CPAMP release artifact. Browser delivery remains multi-resource.

For v2.0, prefer atomic same-version Manager/API/Web delivery plus controlled recovery from lazy-chunk/version mismatch. Do not introduce multi-version static-asset retention until evidence shows it is required.

### 8. Browser transport isolation

The architecture has three distinct contracts:

1. Public CPAMP API v2 — Full Web and CPAMP management clients.
2. Runtime Protocol v1 — Manager/Supervisor internal runtime control.
3. CPA Management API — Panel Lite, CPA ecosystem, and controlled Runtime adapters.

Browser guards MUST fail closed:

- Full Web must not request CPA `/v0/management`, direct CPA management ports, or transmit CPA Management Key material.
- Panel Lite must not depend on CPAMP-only Analytics/Automation/Storage browser APIs.

Import/dependency guards should mirror network guards so a shared source tree cannot silently recreate the 1.x coupling.

### 9. Frontend state boundary

Server state and client-global state are separate concerns.

- Server state owns query/cache, stale/refresh, de-duplication, pagination, cancellation, and mutation invalidation.
- Global client state is limited to session/auth context, deployment capabilities, theme/preferences, and small cross-route UI state.
- URL state owns shareable filters/selections/tabs.
- Form draft and local UI state remain feature/component-owned.

No specific server-state library is frozen by this ADR. A dedicated library such as TanStack Query may be evaluated later against bundle size, migration cost, and current hook complexity.

### 10. Ingress implementation language is evidence-driven

Go is the default implementation baseline for the Phase 1 Ingress because Manager and Supervisor already use Go and therefore share build, cross-platform, CI, security-maintenance, and operational tooling.

Rust/Pingora is a valid future performance candidate, but introducing a second toolchain/supply chain requires evidence from CPAMP-relevant workloads. The decision must focus on proxy-added p50/p99 latency, CPU/RSS, long-lived SSE/WebSocket concurrency, streaming copy cost, cancellation propagation, and failure-domain behavior—not hello-world RPS or microsecond-only differences.

A Rust/Pingora replacement/spike is justified only if it demonstrates material resource or tail-latency benefit (for example, roughly 30% class CPU/RSS improvement or a clearly meaningful long-connection p99 improvement). It is not a Phase 1 Runtime 12 blocker.

## Consequences

### Positive

- Full Web can evolve as a normal modern SPA without inheriting `management.html` packaging constraints.
- Panel Lite remains deployable by CPA as a single-file panel.
- Ingress remains thin and does not need awareness of every future Console route.
- A single configurable prefix simultaneously solves SPA deep-link ownership, sub-path deployment, and scan-noise reduction.
- Manager/Gateway failure isolation remains intact.
- Full Web cannot silently become a CPA Management client again.

### Costs

- Setup/migration and runtime configuration must coordinate one canonical `controlBasePath` value across Manager, Ingress, and Browser bootstrap.
- Prefix changes require careful collision validation and safe apply/rollback behavior.
- Two browser applications require build/import/network boundary tests.
- Multi-resource Full Web introduces cache/chunk version behavior that did not exist in the single-file model.

## Rejected Alternatives

### Keep one giant `management.html` for Full Web

Rejected. Source modularity alone does not remove the 1.x delivery/runtime constraint; it prevents normal code splitting and keeps Full Web coupled to CPA Panel packaging.

### Use fixed `/console/*` plus separate `/api/*` and `/setup/*`

Rejected as the long-term contract. It still forces Ingress to own multiple predictable Control Plane namespaces and loses the configurable-prefix property.

### Redirect `/management.html` permanently to the configured Full Web prefix

Rejected. It would disclose the configured prefix to the exact commodity scanners the prefix is intended to avoid.

### Make Manager proxy all model traffic

Rejected. Manager latency/failure would enter the Data Plane hot path and violate Runtime failure-domain goals.

### Make Supervisor or CPA proxy the other product surfaces

Rejected. It mixes privileged execution/Gateway responsibilities with Control Plane ownership.

### Choose Rust solely for maximum benchmark performance

Rejected. The Ingress is intentionally thin and CPAMP targets personal/limited-team self-hosting. A second language/toolchain is justified only by measured CPAMP-relevant benefit.

## Verification Gates

A conforming implementation must verify at least:

- only public host port `18317` in default Embedded deployment;
- `Client -> Ingress -> Gateway -> Provider` model path with no Manager/Supervisor hop;
- Manager-down does not break Gateway routing and Gateway-down does not break Control routing;
- Full Web multi-resource build and Panel Lite single-file build both succeed independently;
- non-root `controlBasePath` supports SPA deep-link refresh, static assets, and Public API requests;
- route normalization/collision cases fail safely;
- Full Web browser/import guards reject CPA Management transport/key/port usage;
- Panel Lite browser/import guards reject CPAMP-only transport dependencies;
- bootstrap/migration is part of Full Web rather than a third build;
- upgrade chunk mismatch has a bounded recovery behavior;
- Ingress language remains Go unless a separately reviewed benchmark justifies change.
