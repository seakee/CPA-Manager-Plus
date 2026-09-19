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
   - **Isolation**: Different callers produce completely disjoint cryptographic hashes, utilized by CPA's session affinity to partition session trees.
   - **Absence**: Missing if authentication is disabled, if authentication fails/rejects, or prior to `accessAuthMiddleware` execution (`request_received`).
3. **Raw Secret Exposure Boundary**:
   - **HTTP Header Cloning Pipeline**: `sdk/api/handlers/handlers_context.go#headersFromContext` clones inbound HTTP headers from the Gin context, and `sdk/api/handlers/model_execution.go#modelExecutionHeaders` forwards them into `opts.Headers`.
   - **RequestInterceptor (before-auth & after-auth)**: **Exposed**. Receives `opts.Headers` / `req.Headers` containing raw inbound `Authorization` / `X-Api-Key` headers.
   - **Scheduler (`SchedulerPickRequest`)**: **Exposed via Headers**. Receives `req.Options.Headers` containing raw headers; however, `req.Options.Metadata` contains only sanitized `caller_scope`.
   - **Terminal `RequestCompletion`**: **Redacted**. Struct lacks any `Headers` field; `Metadata` contains only `caller_scope`.
   - **`UsagePlugin` (`pluginapi.UsageRecord`)**: **Exposed**. `APIKeyFromContext` in `internal/runtime/executor/helps/usage_helpers.go` retrieves `ginCtx["userApiKey"]`, placing the raw API key into `UsageRecord.APIKey`.
4. **Targeted Identity Semantics Parity Between v7.3.3 and v7.3.8**:
   The identity middleware, access providers, `CallerScope` derivation algorithm, `APIKeyFromContext` extraction, and metadata sanitization logic are identical in behavior between CPA v7.3.3 and v7.3.8. (Unrelated changes in v7.3.8 `usage_helpers.go` cover model substitution warnings and stream response model buffer fields, leaving caller identity propagation unchanged).
5. **External Boundary**:
   An unnegotiated external runtime must remain `unknown` / `partial` and cannot inherit embedded observations.

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

- **Determinism**: For any given principal string $P$, `CallerScope(P)` is idempotent and invariant over time and across process restarts.
- **Normalization**: Leading and trailing ASCII whitespace is removed via `strings.TrimSpace(value)`.
- **Case Sensitivity**: The hash is case-sensitive (e.g. `CallerScope("sk-abc") != CallerScope("SK-ABC")`).
- **Domain Separation**: Salted with `"cli-proxy-api:caller-scope:v1\x00"`, preventing hash collisions with other SHA-256 usages in CPA.
- **One-way / Non-plaintext**: 64-character lowercase hex string. Does not reveal plaintext key contents or length directly.

### 4.3 Isolation & Limitations

- **Caller Partitioning**: Proven in `sdk/cliproxy/auth/selector_lcp_test.go#TestSessionAffinitySelectorLCPCallerScopeIsolation`: CPA's session affinity engine specifically groups and isolates session trees by `caller_scope`. Different downstream keys cannot cross-access or interfere with each other's session affinity state.
- **Absence Conditions**:
  - Auth disabled (`manager == nil` or `APIKeys` empty): `userApiKey` unset $\rightarrow$ `caller_scope` is `""`.
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
| 3 | `credential_candidates_generated` | before-credential-auth | Yes (`opts.Headers`) | No (redacted) | **Yes** (`opts.Metadata`) | `conductor_selection.go` |
| 4 | `disabled_cooldown_priority_filtering` | before-credential-auth | Yes (`opts.Headers`) | No (redacted) | **Yes** (`opts.Metadata`) | `conductor_selection.go` |
| 5 | `credential_selected` | after-credential-auth | Yes (`req.Options.Headers`) | No (redacted) | **Yes** (`req.Options.Metadata`) | `conductor_selection.go`; `PickAuth` |
| 6 | `provider_endpoint_resolved` | spans route/endpoint | Yes (`opts.Headers`) | No (redacted) | **Yes** (`opts.Metadata`) | `model_execution.go#modelExecutionHeaders` |
| 7 | `upstream_request_stream` | after-credential-auth | Yes (`req.Headers`) | No (redacted) | **Yes** (`req.Metadata`) | `conductor_execution.go`; `InterceptRequestAfterAuth` |
| 8 | `retry_fallback` | repeated after auth | Yes (`opts.Headers`) | No (redacted) | **Yes** (`opts.Metadata`) | `conductor_execution.go` |
| 9 | `response_cancel_reject_failure` | terminal | **No** (struct has no headers) | **No** (redacted) | **Yes** (`completion.Metadata`) | `handlers_interceptors.go`; `RequestCompletion` |
| 10 | `usage_accounting_settlement` | attempt-coupled | N/A | **Yes** (`record.APIKey` has raw secret) | **No** (not copied to usage) | `usage_helpers.go#APIKeyFromContext`; `UsageRecord` |

---

## 6. Official Release Binary Black-Box Observations

### 6.1 Release Binary Verification & Environment

Official Linux arm64 binaries from GitHub releases were verified against frozen CPAMP plan digests before execution:

| Artifact | CPA Release | Commit | Architecture | Archive SHA-256 Digest | Status |
|:---|:---:|:---:|:---:|:---|:---:|
| `current-bundled-v7-3-3` | `v7.3.3` | `7bbfeaf8` | `linux/arm64` | `5f320e3fae52af00f07b78201311e9d096b36e759441d948de48a10f49e71883` | Verified |
| `candidate-release-v7-3-8` | `v7.3.8` | `c93978c4` | `linux/arm64` | `8d09ce286d857b2d0e6d77c39e08a755246120df58c02a115d58c391fc73e3f1` | Verified |

- **Topology**: Isolated Linux container (`seakee/cpamp-runtime:runtime15-arm64`) running on dedicated ephemeral ports (`18333` for v7.3.3, `18338` for v7.3.8).
- **Execution Mode**: Local model mode (`-local-model`), disabling remote catalog downloads.
- **Configured Client Keys**: `["sk-phase2-caller-alpha", "sk-phase2-caller-beta"]`.

---

<a id="v7-3-3-release-binary-black-box-observation"></a>
### 6.2 CPA v7.3.3 Black-Box Observation (`phase2-02a-v7-3-3-release-black-box`)

#### Scenario 1: Same Caller Stability
- Client sends repeated requests with `Authorization: Bearer sk-phase2-caller-alpha`.
- Server returns HTTP 200 (`{"data":[],"object":"list"}`).
- Derived identity:
  $$\text{caller\_scope} = \text{sha256}(\text{"cli-proxy-api:caller-scope:v1\x00sk-phase2-caller-alpha"}) = \texttt{b3b1a4b63a0b68349348164be3edb6fcb9a3ee41d4d026c1ef21afb93cafa151}$$
- Repeated requests produce the identical 64-character hex hash, ensuring session affinity stability across requests.

#### Scenario 2: Two Caller Isolation
- Client A sends with `Authorization: Bearer sk-phase2-caller-alpha` $\rightarrow$ `caller_scope = b3b1a4b6...`.
- Client B sends with `Authorization: Bearer sk-phase2-caller-beta` $\rightarrow$ `caller_scope = 0eb2fb39ffeac09dfadd792351df6e98ca2007e20e2015068111247fa9e2411c`.
- Both callers authenticate successfully (HTTP 200), and their session states partition into completely separate hash spaces.
- Unauthorized request `sk-invalid` returns HTTP 401 (`{"error":"Invalid API key"}`).
- Unauthenticated request returns HTTP 401 (`{"error":"Missing API key"}`).
- Alternate header forms (`x-api-key`, `x-goog-api-key`) authenticate identically to Bearer tokens.

#### Scenario 3: Secret Exposure Boundary
- **Gin Access Log**: Outputs `[8b049524] [info ] [gin_logger.go:103] 200 | 3ms | 172.17.0.1 | GET "/v1/models"`. Key string does not leak into access logs.
- **Header Pipeline**: Inbound request headers (`Authorization: Bearer ...`) are preserved via `handlers_context.go#headersFromContext` into `modelExecutionHeaders`, making raw secrets visible to request interceptors and scheduler options.
- **Terminal Completion**: Responses omit request headers; `RequestCompletion.Metadata` retains only `caller_scope`.

---

<a id="v7-3-8-release-binary-black-box-observation"></a>
### 6.3 CPA v7.3.8 Black-Box Observation (`phase2-02a-v7-3-8-release-black-box`)

The identical test battery was executed against official CPA v7.3.8 on port `18338`:

#### Scenario 4: v7.3.3 vs. v7.3.8 Parity
- **Unauthenticated**: Returns identical HTTP 401 `{"error":"Missing API key"}`.
- **Invalid Credential**: Returns identical HTTP 401 `{"error":"Invalid API key"}`.
- **Valid Caller Alpha**: Returns identical HTTP 200 via `Authorization`, `x-api-key`, and `x-goog-api-key`.
- **Valid Caller Beta**: Returns identical HTTP 200 and partitions to identical hash `0eb2fb39ffeac09dfadd792351df6e98ca2007e20e2015068111247fa9e2411c`.
- **Conclusion**: Caller identity resolution, isolation, and header exposure boundaries in v7.3.8 are identical to v7.3.3.

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
*Boundary*: Under built-in access, Principal is identical to Raw Secret. Under plugin access, Principal is an arbitrary string. CPAMP must not treat CPA's Principal as an authenticated user entity without inspecting the provider.

### 7.3 Hashed / Scoped Identity (Usable for Request/Session Scoping)
- `coreexecutor.CallerScopeMetadataKey` (`"caller_scope"`)
- `coresession.CallerScope(val)`
*Boundary*: Valid for request correlation, session affinity partitioning, and cache scoping. Unsuitable as a persistent account entity across key rotations.

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
5. `sdk/api/handlers/handlers_context.go` (`headersFromContext`) & `model_execution.go` (`modelExecutionHeaders`): **Identical**.
6. `internal/runtime/executor/helps/usage_helpers.go`: Identity extraction function `APIKeyFromContext` (extracting `ginCtx["userApiKey"]` into `usage.Record.APIKey`) and caller identity propagation are **Identical**. (Note: non-identity changes in v7.3.8 added upstream model substitution warnings and streaming buffer response model fields, which do not alter caller identity semantics).
7. `internal/pluginhost/rpc_client.go`: Identity metadata sanitization and caller propagation logic are **Identical** (v7.3.8 only adds `SchedulerAcrossPriorities` capability forwarding).

**Conclusion**: Identity semantics and security boundaries in candidate v7.3.8 are identical to the bundled v7.3.3 baseline.

---

## 9. External Unnegotiated Runtime Boundary

For `external-unnegotiated`:
- CPA version is `null`, `versionKnown` is `false`, and plugin availability is `unknown`.
- Plugin contract is `null`.
- Per the Phase2-01 contract, capability records for External cannot exceed `unknown` or `partial`.
- External runtimes cannot inherit Embedded observations without live version and capability negotiation.
