# Phase2-02A: Caller Identity and Scope Evidence

## 1. Meta & Provenance

- **Task**: CPAMP v2 Phase2-02A — Caller Identity / Scope Evidence
- **Status**: Complete / Evidence-only
- **CPAMP Baseline**: `v2@aca237cf7a99ce5d098dedcaec5cf8e147f05e4b`
- **Target CPA Releases & Verified Digest Provenance**:
  - Current Embedded: CPA `v7.3.3` (`7bbfeaf8a7acf2cd5a834dcb0842539fe6aabc2b`)
    - Linux amd64: `7af8c99cd08eee3ccc81d1596e8a31785674d3de6bd7ec61416d59493dd8fc01`
    - Linux arm64: `5f320e3fae52af00f07b78201311e9d096b36e759441d948de48a10f49e71883`
  - Candidate Fixture: CPA `v7.3.8` (`c93978c4ea2e908255a2a06c37599fda3651554a`)
    - Linux amd64: `3fe5228c458624175d5e4e81d9dd003d82de688ca498fa368a790ea120bda0e3`
    - Linux arm64: `8d09ce286d857b2d0e6d77c39e08a755246120df58c02a115d58c391fc73e3f1`
  - External: `external-unnegotiated` (version, plugin availability, and auth unnegotiated)
- **Plugin ABI Schema**: `SchemaVersion = 6`
- **Fixture Extension**: [`tests/fixtures/phase2-evidence/extensions/phase2-02a-caller-identity.json`](../../../tests/fixtures/phase2-evidence/extensions/phase2-02a-caller-identity.json)
- **Focused Test**: [`tests/phase2CallerIdentityEvidence.test.mjs`](../../../tests/phase2CallerIdentityEvidence.test.mjs)

---

## 2. Executive Summary

This evidence slice proves the exact semantics, visibility, lifecycle stability, and security boundaries of downstream caller identities (`APIKey`, `Principal`, and `caller_scope`) in CPA v7.3.3 and candidate v7.3.8.

### Core Proven Findings

1. **Built-in `config_access` conflates Principal with Raw Secret**:
   Under built-in API key authentication, CPA's access provider returns `candidate.value` (the raw secret string) directly as `Result.Principal`, and records it on the Gin context as `userApiKey`.
2. **`caller_scope` is a Deterministic, Domain-Separated One-Way Hash**:
   CPA derives `caller_scope = hex(sha256("cli-proxy-api:caller-scope:v1\x00" + strings.TrimSpace(userApiKey)))`.
   - **Properties**: Deterministic, domain-separated, whitespace-normalized, non-plaintext one-way hash.
   - **Stability**: Multiple requests from the same caller (identical key) produce identical 64-character hex strings across the entire runtime lifecycle.
   - **Partitioning / Scope**: `caller_scope` is deterministically derived from the caller principal and is used as an input to session-affinity partitioning. The two tested distinct principals produced distinct caller_scope values. No uniqueness guarantee beyond SHA-256's normal collision-resistance properties is claimed.
   - **Absence**: Missing if no access provider establishes an authenticated principal, if authentication fails/rejects, or prior to `accessAuthMiddleware` execution (`request_received`).
3. **Raw Secret Exposure Boundary**:
   - **HTTP Header Cloning Pipeline**: `sdk/api/handlers/handlers_context.go#headersFromContext` clones inbound HTTP headers from the Gin context, and `sdk/api/handlers/model_execution.go#modelExecutionHeaders` forwards them into `opts.Headers`.
   - **RequestInterceptor (before-auth & after-auth)**: **Exposed**. Receives `opts.Headers` / `req.Headers` containing raw inbound `Authorization` / `X-Api-Key` headers.
   - **Scheduler (`SchedulerPickRequest`)**: **Exposed via Headers**. Raw inbound credentials remain exposed through `Options.Headers`. `Options.Metadata` carries `caller_scope` plus other execution metadata, but does not receive the built-in raw `userApiKey` through the normal `requestExecutionMetadata` path.
   - **Terminal `RequestCompletion`**: **Redacted**. RequestCompletion has no request `Headers` field. `Metadata` preserves `caller_scope` plus other execution metadata; the normal metadata construction path does not copy the built-in raw `userApiKey` into this map.
   - **`UsagePlugin` (`pluginapi.UsageRecord`)**: **Exposed**. `APIKeyFromContext` in `internal/runtime/executor/helps/usage_helpers.go` retrieves `ginCtx["userApiKey"]`, placing the raw API key into `UsageRecord.APIKey`.
4. **Targeted Identity Semantics Parity Between v7.3.3 and v7.3.8**:
   The identity middleware, access providers, `CallerScope` derivation algorithm, `APIKeyFromContext` extraction, and metadata construction logic are identical in behavior between CPA v7.3.3 and v7.3.8. (Unrelated changes in v7.3.8 `usage_helpers.go` cover model substitution warnings and stream response model buffer fields, leaving caller identity propagation unchanged).
5. **External Boundary**:
   An unnegotiated External runtime must remain `unknown` until runtime/version/plugin capability negotiation provides direct evidence, and cannot inherit embedded observations.

---

## 3. CPA Authentication & Principal Generation

### 3.1 Built-in `config_access` Provider

Located at `internal/access/config_access/provider.go`:

```go
func (p *provider) Authenticate(_ context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
    ...
    candidates := []struct {
        value  string
        source string
    }{
        {apiKey, "authorization"},
        {authHeaderGoogle, "x-goog-api-key"},
        {authHeaderAnthropic, "x-api-key"},
        {queryKey, "query-key"},
        {queryAuthToken, "query-auth-token"},
    }

    for _, candidate := range candidates {
        if _, ok := p.keys[candidate.value]; ok {
            return &sdkaccess.Result{
                Provider:  p.Identifier(),
                Principal: candidate.value, // <--- RAW CLIENT SECRET
                Metadata: map[string]string{
                    "source": candidate.source,
                },
            }, nil
        }
    }
    return nil, sdkaccess.NewInvalidCredentialError()
}
```

- **Principal**: Exactly equal to the raw API key candidate string.
- **Provider**: Default `"config-inline"`.
- **Metadata**: Fixed map `{"source": "authorization" | "x-goog-api-key" | "x-api-key" | "query-key" | "query-auth-token"}`.

### 3.2 Gin Context Population

Located at `internal/api/server_middleware.go` (`accessAuthMiddleware`):

```go
result, err := manager.Authenticate(c.Request.Context(), c.Request)
if err == nil {
    if result != nil {
        c.Set("userApiKey", result.Principal)
        c.Set("accessProvider", result.Provider)
        if len(result.Metadata) > 0 {
            c.Set("accessMetadata", result.Metadata)
        }
    }
    c.Next()
    return
}
```

The Gin context key `"userApiKey"` holds `result.Principal` (the raw secret for built-in auth).

### 3.3 `FrontendAuthProvider` Plugin Interface

Defined in `sdk/pluginapi/types.go` and implemented in `internal/pluginhost/adapters_auth.go`:

- Plugin receives: `FrontendAuthRequest{ Method, Path, Headers, Query, Body }`.
- Plugin returns: `FrontendAuthResponse{ Authenticated: bool, Principal: string, Metadata: map[string]string }`.
- Adapter converts to `sdkaccess.Result{ Provider: "plugin:<plugin-id>:<provider-id>", Principal: resp.Principal, Metadata: resp.Metadata }`.
- **Semantic Divergence**: Unlike built-in `config_access` where `Principal` is always the raw secret, a `FrontendAuthProvider` may emit an abstract user identifier (e.g., `"user-12345"`). However, CPA treats whatever is in `result.Principal` as the downstream caller identity for `userApiKey` and subsequent `CallerScope` derivation.

---

## 4. `caller_scope` Derivation, Stability, and Partitioning

### 4.1 Derivation Algorithm

Defined in `sdk/cliproxy/session/identity.go`:

```go
func CallerScope(value string) string {
    value = strings.TrimSpace(value)
    if value == "" {
        return ""
    }
    sum := sha256.Sum256([]byte("cli-proxy-api:caller-scope:v1\x00" + value))
    return hex.EncodeToString(sum[:])
}
```

And extracted into execution metadata in `sdk/api/handlers/handlers.go`:

```go
func requestCallerScope(ginCtx *gin.Context) string {
    if ginCtx == nil {
        return ""
    }
    value, exists := ginCtx.Get("userApiKey")
    if !exists || value == nil {
        return ""
    }
    return coresession.CallerScope(fmt.Sprint(value))
}
```

### 4.2 Stability & Cryptographic Properties

- **Determinism**: For a given normalized principal string $P$, `CallerScope(P)` is deterministic and stable across repeated evaluations and process restarts.
- **Normalization**: Leading and trailing Unicode whitespace recognized by Go `strings.TrimSpace` is removed before hashing.
- **Case Sensitivity**: The hash is case-sensitive (e.g. `CallerScope("sk-abc") != CallerScope("SK-ABC")`).
- **Domain Separation**: The fixed prefix `"cli-proxy-api:caller-scope:v1\x00"` provides a caller-scope-specific input domain, separating caller_scope hashes from other SHA-256 applications in CPA. It does not alter SHA-256's underlying collision resistance.
- **One-way / Non-plaintext**: 64-character lowercase hex string. Does not reveal plaintext key contents or length directly.
- **Security Limitation (Low-Entropy Principals)**: `caller_scope` is non-plaintext, but it should not be treated as anonymization. CPA does not enforce entropy, length, or randomness requirements for either built-in `config_access` APIKeys or plugin-defined `FrontendAuthProvider` principals. If the underlying principal is low-entropy or guessable, an observer who knows the fixed domain prefix `"cli-proxy-api:caller-scope:v1\x00"` can test candidate values offline by hashing them and comparing the resulting `caller_scope`.

### 4.3 Isolation & Limitations

- **Caller Partitioning**: Proven in `sdk/cliproxy/auth/selector_lcp_test.go#TestSessionAffinitySelectorLCPCallerScopeIsolation`: CPA's session affinity engine specifically groups and isolates session trees by `caller_scope`. The upstream isolation test demonstrates that distinct caller scopes do not reuse the same LCP binding for the tested shared-prompt scenario. This is a session-affinity isolation property, not an authorization boundary.
- **Absence Conditions**:
  - **No Authenticated Principal**: `caller_scope` is empty when no authenticated Principal/userApiKey is established, for example when the access manager has no active providers or the execution path does not carry an authenticated Gin request context. An empty built-in `APIKeys` configuration disables the `config_access` credential source, but does not imply that plugin `FrontendAuthProvider`s are absent.
  - Auth rejected: request terminated in middleware $\rightarrow$ execution metadata never initialized.
  - Pre-auth stage: at `request_received`, before middleware finishes, `caller_scope` does not exist.
  - Non-HTTP contexts: unit test calls bypassing Gin context where `ctx.Value("gin")` is nil.
- **Key Rotation**: If the caller rotates their API key, `caller_scope` changes immediately. It cannot track an ongoing account across credential changes.

---

## 5. Lifecycle Visibility & Secret Exposure Matrix

The table below maps the 11 canonical CPA lifecycle stages against caller identity and secret exposure:

| Ordinal | Stage ID | Placement | Raw Secret in Headers? | Raw Secret in Metadata? | `caller_scope` Available? | Primary Source / Test Evidence |
|:---:|:---|:---|:---:|:---:|:---:|:---|
| 0 | `request_received` | before-client-auth | Yes (inbound HTTP) | No | No (pre-auth) | `server_middleware.go`; `FrontendAuthRequest` |
| 1 | `client_api_key_resolved` | client-auth | Yes (inbound HTTP) | No | No (derived next) | `config_access/provider.go`; `server_middleware.go` |
| 2 | `model_resolved` | before-credential-auth | Yes (`opts.Headers`) | No (redacted) | **Yes** (`meta["caller_scope"]`) | `model_execution.go#modelExecutionHeaders`; `handlers_metadata_test.go` |
| 3 | `credential_candidates_generated` | before-credential-auth | Yes (`opts.Headers`) | No (redacted) | **Yes** (`opts.Metadata`) | `sdk/cliproxy/auth/conductor_selection.go` |
| 4 | `disabled_cooldown_priority_filtering` | before-credential-auth | Yes (`opts.Headers`) | No (redacted) | **Yes** (`opts.Metadata`) | `sdk/cliproxy/auth/conductor_selection.go` |
| 5 | `credential_selected` | after-credential-auth | Yes (`SchedulerPickRequest.Options.Headers`) | No (redacted) | **Yes** (`SchedulerPickRequest.Options.Metadata`) | `sdk/cliproxy/auth/conductor_selection.go`; `PickAuth` |
| 6 | `provider_endpoint_resolved` | spans route/endpoint | Yes (`opts.Headers`) | No (redacted) | **Yes** (`opts.Metadata`) | `sdk/api/handlers/model_execution.go#modelExecutionHeaders` |
| 7 | `upstream_request_stream` | after-credential-auth | Yes (`req.Headers`) | No (redacted) | **Yes** (`RequestAfterAuthInterceptRequest.Metadata ← execOpts.Metadata`) | `conductor_execution.go#applyRequestAfterAuthInterceptor` |
| 8 | `retry_fallback` | repeated after auth | Yes (`opts.Headers` / per-attempt `execOpts.Headers`) | No (redacted) | **Yes** (`opts.Metadata` / per-attempt `execOpts.Metadata`) | `conductor_execution.go` |
| 9 | `response_cancel_reject_failure` | terminal | **No** (struct has no headers) | **No** (redacted) | **Yes** (`completion.Metadata`) | `handlers_interceptors.go`; `RequestCompletion` |
| 10 | `usage_accounting_settlement` | attempt-coupled | N/A | **Yes** (`record.APIKey` has raw secret) | **No** (not copied to usage) | `usage_helpers.go#APIKeyFromContext`; `UsageRecord` |

---

## 6. Official Release Binary Black-Box Observations

### 6.1 Release Binary Verification & Ephemeral Observer Harness

Official Linux arm64 binaries from GitHub releases were verified against frozen CPAMP plan digests before execution:

| Artifact | CPA Release | Commit | Architecture | Archive SHA-256 Digest | Status |
|:---|:---:|:---:|:---:|:---|:---:|
| `current-bundled-v7-3-3` | `v7.3.3` | `7bbfeaf8` | `linux/arm64` | `5f320e3fae52af00f07b78201311e9d096b36e759441d948de48a10f49e71883` | Verified |
| `candidate-release-v7-3-8` | `v7.3.8` | `c93978c4` | `linux/arm64` | `8d09ce286d857b2d0e6d77c39e08a755246120df58c02a115d58c391fc73e3f1` | Verified |

- **Ephemeral Observer Harness**: A minimal C-ABI dynamic shared library (`observer.so`, Plugin ABI v1) implementing `RequestInterceptor` (`request.intercept_before`, `request.intercept_after`) and `RequestLifecyclePlugin` (`request.complete`) was compiled in an isolated container and loaded via standard CPA plugin configuration (`plugins.configs.observer`).
- **Topology**: Isolated Linux containers (`debian:bookworm-slim` arm64) executing on dedicated ephemeral ports (`8081` for v7.3.3, `8088` for v7.3.8).
- **Execution Mode**: Local model mode (`-local-model`) with synthetic configuration keys: `["sk-phase2-caller-alpha", "sk-phase2-caller-beta"]`.
- **Termination Mechanism**: The observer intercepts inbound requests at `request.intercept_before`, records full incoming headers and metadata payloads into an external event log, and terminates with `Terminate: true, StatusCode: 200` to avoid external model invocation while cleanly triggering terminal lifecycle tracking (`request.complete`).

---

<a id="v7-3-3-release-binary-black-box-observation"></a>
### 6.2 CPA v7.3.3 Black-Box Observation (`phase2-02a-v7-3-3-release-black-box`)

Official CPA v7.3.3 release binary was executed on port `8081`. The runtime events emitted by the release binary into `observer.so` confirmed the following observations:

#### Case A: Caller Alpha Initial Request
- Client sends `POST /v1/chat/completions` with header `Authorization: Bearer sk-phase2-caller-alpha`.
- `request.intercept_before` observed runtime payload:
  - `Headers["Authorization"]`: `["Bearer sk-phase2-caller-alpha"]` (raw credential present in headers).
  - `Metadata["caller_scope"]`: `b3b1a4b63a0b68349348164be3edb6fcb9a3ee41d4d026c1ef21afb93cafa151`.
  - `Metadata`: Contains no raw secret or plain key text.

#### Case B: Caller Alpha Repeat Request (Runtime Stability)
- Client sends a second request with identical header `Authorization: Bearer sk-phase2-caller-alpha`.
- `request.intercept_before` observed runtime payload:
  - `Metadata["caller_scope"]`: `b3b1a4b63a0b68349348164be3edb6fcb9a3ee41d4d026c1ef21afb93cafa151`.
  - Runtime proof: $\text{caller\_scope}_{A1} == \text{caller\_scope}_{A2}$. Identity is stable across repeated requests.

#### Case C: Caller Beta Request (Distinct Principal Observation)
- Client sends `POST /v1/chat/completions` with header `Authorization: Bearer sk-phase2-caller-beta`.
- `request.intercept_before` observed runtime payload:
  - `Headers["Authorization"]`: `["Bearer sk-phase2-caller-beta"]`.
  - `Metadata["caller_scope"]`: `0eb2fb39ffeac09dfadd792351df6e98ca2007e20e2015068111247fa9e2411c`.
  - Runtime proof: $\text{caller\_scope}_{\alpha} \neq \text{caller\_scope}_{\beta}$ for the two observed principals, confirming caller-dependent separation in this test.

#### Case D: Raw Header Visibility Across Alternate Header Forms
- Client sends requests using alternate API key headers:
  - `x-api-key: sk-phase2-caller-alpha` $\rightarrow$ `Headers["X-Api-Key"]`: `["sk-phase2-caller-alpha"]`, `Metadata["caller_scope"]`: `b3b1a4b6...`.
  - `x-goog-api-key: sk-phase2-caller-alpha` $\rightarrow$ `Headers["X-Goog-Api-Key"]`: `["sk-phase2-caller-alpha"]`, `Metadata["caller_scope"]`: `b3b1a4b6...`.
  - Runtime proof: Raw credential strings are propagated into `RequestInterceptor` headers across all supported access header forms without redaction, while `Metadata["caller_scope"]` consistently resolves to the derived hash.

#### Case E: Terminal RequestCompletion Redaction
- Terminal lifecycle hook `request.complete` observed runtime payload across all requests:
  - `Metadata["caller_scope"]`: Preserves the exact `caller_scope` derived during execution (`b3b1a4b6...` for Alpha, `0eb2fb39...` for Beta).
  - Header Redaction: `RequestCompletion` exposes no request Headers field. For the tested request, the configured built-in client credential was not observed in `RequestCompletion.Metadata`; `caller_scope` remained present.

---

<a id="v7-3-8-release-binary-black-box-observation"></a>
### 6.3 CPA v7.3.8 Black-Box Observation (`phase2-02a-v7-3-8-release-black-box`)

The identical test battery was executed against candidate release binary CPA v7.3.8 on port `8088`. The observed runtime events confirmed exact semantic parity with v7.3.3:

1. **Case A (Alpha Initial)**: `RequestInterceptor` received `Headers["Authorization"] = ["Bearer sk-phase2-caller-alpha"]` and `Metadata["caller_scope"] = b3b1a4b63a0b68349348164be3edb6fcb9a3ee41d4d026c1ef21afb93cafa151`.
2. **Case B (Alpha Repeat)**: Emitted identical `caller_scope` `b3b1a4b6...`, confirming runtime stability in candidate v7.3.8.
3. **Case C (Beta Caller Scope)**: Emitted `caller_scope` `0eb2fb39ffeac09dfadd792351df6e98ca2007e20e2015068111247fa9e2411c`. For the two tested distinct principals, $\text{caller\_scope}_{\alpha} \neq \text{caller\_scope}_{\beta}$, confirming caller-dependent scope separation in this observation.
4. **Case D (Alternate Headers)**: Raw credentials propagated unredacted into `Headers["X-Api-Key"]` and `Headers["X-Goog-Api-Key"]`.
5. **Case E (Terminal Redaction)**: `RequestCompletion` preserved `caller_scope` in `Metadata` and exposed no request Headers field. The tested built-in client credential was not observed in `RequestCompletion.Metadata`.

---

### 6.4 Source-Supported Boundaries (Non-Black-Box Findings)

Certain downstream lifecycle stages cannot be safely triggered in an isolated black-box harness without mocking upstream provider networks. These boundaries are explicitly backed by upstream source analysis rather than claimed as black-box observations:

1. **Scheduler Header Exposure**:
   - `sdk/api/handlers/handlers_context.go#headersFromContext` clones inbound HTTP request headers.
   - `sdk/api/handlers/model_execution.go#modelExecutionHeaders` propagates them into `opts.Headers`.
   - `sdk/cliproxy/auth/conductor_selection.go#schedulerOptions` copies `opts.Headers` directly into `SchedulerPickRequest.Options.Headers`.
   - *Source conclusion*: Raw client authentication headers are source-proven to be visible to scheduler plugins through `SchedulerPickRequest.Options.Headers`.
   - *Black-box boundary note*: This scheduler boundary was not directly exercised by the release observer black-box harness.
2. **UsageRecord.APIKey Exposure**:
   - `internal/runtime/executor/helps/usage_helpers.go#APIKeyFromContext` reads `ginCtx["userApiKey"]`.
   - `NewUsageReporter` stores that value in `reporter.apiKey`.
   - `UsageReporter.buildRecordForModel` copies it into `usage.Record.APIKey`.
   - `internal/pluginhost/adapters_usage_translation.go#usageAdapter.HandleUsage` translates `usage.Record.APIKey` into `pluginapi.UsageRecord.APIKey`.
   - *Source conclusion*: Under built-in `config_access`, Principal equals the raw client credential. Therefore the raw downstream credential is propagated through `UsageReporter` into plugin-facing `UsageRecord.APIKey`.
   - *Black-box boundary note*: UsageRecord.APIKey exposure is source-supported only; it was not directly exercised by the release black-box observer.

---

## 7. Field Classification & Security Boundaries

CPAMP strictly classifies CPA identity and metadata fields to avoid dangerous identity promotions:

### 7.1 Raw Secrets (Must NOT be promoted to CPAMP Identity)
- `candidate.value` in `config_access`
- Inbound HTTP header values (`Authorization`, `X-Api-Key`, `X-Goog-Api-Key`)
- Inbound URL query values (`key`, `auth_token`)
- `pluginapi.UsageRecord.APIKey` / `usage.Record.APIKey` under built-in access
- Upstream provider credentials (`auth.Attributes["api_key"]`)

### 7.2 Principals (Variable Semantics)
- `sdkaccess.Result.Principal`
- Gin context `"userApiKey"`
- `pluginapi.FrontendAuthResponse.Principal`
*Boundary*: Under built-in access, Principal is identical to Raw Secret. Under plugin access, Principal is an abstract string. CPAMP must not treat CPA's Principal as an authenticated user entity without inspecting the provider.

### 7.3 Hashed / Scoped Identity (Usable for Caller/Session Scoping)
- `coreexecutor.CallerScopeMetadataKey` (`"caller_scope"`)
- `coresession.CallerScope(val)`
*Boundary*: Valid for caller/session scoping, session-affinity partitioning, and caller-scoped affinity/cache namespacing. It is not a per-request correlation key because repeated requests from the same principal intentionally share the same caller_scope. It is also unsuitable as a persistent account entity across key rotations.

### 7.4 Display Metadata (Unsuitable for Authorization or Routing)
- `sdkaccess.Result.Metadata["source"]`
- Inbound `User-Agent`, client IP (`requestClientIP`, `X-Forwarded-For`)
- Model alias, requested model, request path
- Upstream credential metadata (`email`, `credential_filename`, `base_url`, `provider_display_name`)

---

## 8. Version Parity: CPA v7.3.3 vs. v7.3.8

A targeted comparison between v7.3.3 (`7bbfeaf8a7acf2cd5a834dcb0842539fe6aabc2b`) and v7.3.8 (`c93978c4ea2e908255a2a06c37599fda3651554a`) confirms:

1. `internal/api/server_middleware.go` (`accessAuthMiddleware`): **Identical**.
2. `internal/access/config_access/provider.go`: **Identical**.
3. `sdk/cliproxy/session/identity.go` (`CallerScope`): **Identical**.
4. `sdk/api/handlers/handlers.go` (`requestCallerScope`): **Identical**.
5. `sdk/api/handlers/handlers_context.go` (`headersFromContext`): **Identical**.
6. `sdk/api/handlers/model_execution.go` (`modelExecutionHeaders`): **Identical**.
7. `internal/runtime/executor/helps/usage_helpers.go`: `APIKeyFromContext` identity extraction semantics are identical, and caller identity propagation remains unchanged across both releases. (Unrelated changes in v7.3.8 add model substitution warnings and streaming buffer response model fields).
8. `internal/pluginhost/rpc_client.go`: Identity metadata sanitization and caller identity-related behavior remain unchanged between v7.3.3 and v7.3.8. (v7.3.8 introduces `SchedulerAcrossPriorities` capability forwarding without altering identity attributes).

**Conclusion**: Identity semantics, caller scoping algorithms, and security boundaries in candidate v7.3.8 are identical to the bundled v7.3.3 baseline.

---

## 9. External Unnegotiated Runtime Boundary

For `external-unnegotiated`:
- CPA version is `null`, `versionKnown` is `false`, and plugin availability is `unknown`.
- Plugin contract is `null`.
- Per the Phase2-01 contract, capability records for External must remain `unknown` until runtime/version/plugin capability negotiation provides direct evidence.
- External runtimes cannot inherit Embedded observations without live version and capability negotiation.
