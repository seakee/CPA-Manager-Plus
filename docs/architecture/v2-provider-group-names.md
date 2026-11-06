# Native provider group names (v2)

This implements the name-editing slice of [#492](https://github.com/seakee/CPA-Manager-Plus/issues/492), following the maintainer's decision to use CPA v8 `api-keys.<provider>[].name`. There is no CPAMP alias table or browser-side name store. This does not backport a feature to frozen v1.

## User behavior

Full Web v2 adds **Provider group names** in the AI Providers workspace. Operators can enter an email or another display label, edit an existing name, cancel a draft, and refresh the persisted names. Clearing optional metadata removes the `name` key; CPA remains responsible for rejecting a missing name where its own provider contract requires one. Names have a 120-code-point limit, must be trimmed, and cannot contain control characters.

Supported families are **codex, claude, gemini, interactions, xai and vertex**. `openai-compatibility.name` is an identifier with identity and possible routing semantics; it is excluded from both GET projection and PUT validation, and the UI rejects unsupported projections. `meta` is not qualified for this editor and is not shown; an unsupported-only configuration explicitly reports that no supported groups exist.

A group can contain several credentials: the name belongs to the native **group**, not to an individual key. The provider and displayed group number locate a group only within the loaded configuration revision. Names and array positions are never stable identities, routing inputs, monitoring aggregation keys, deduplication keys, or historical linkage keys.

## Control-plane contract

Authenticated post-installation Administrator resources:

- `GET providers/names`: returns only `generation`, opaque `etag`, `writable`, and `groups` containing provider/index/name. Provider and client secrets, addresses, headers and raw configuration never leave Manager through this resource.
- `PUT providers/names`: accepts exactly `{generation, etag, provider, index, name}`. Unknown, duplicate, null and malformed fields are refused. Setup continuation credentials cannot write.

Manager resolves the same committed installation authority used by gateway configuration. The private H1 scope remains read-only; managed installations and supported External CPA v8 connections can save. External version and rejected-management-credential gates remain in effect.

This feature requires **config GET ETag plus atomic If-Match writes**, not merely a CPA v8 version number. The upstream source contract starts at **v8.0.31**: v8.0.13/v8.0.30 lack it, while v8.0.31/v8.0.40/v8.0.47 have it, as identified in the maintainer's review. This revision tests that contract with mocks; it does not claim a live run of those releases. A custom build must actually expose both capabilities. The four-language read-error text states this requirement. CPA's configuration GET must provide an ETag. Manager reads a fresh secret-bearing document, checks the user's generation/revision, changes one `name` only, and replaces that provider array with `If-Match`. CPA's array contract has no index-addressable leaf writes. Unknown group/key fields, large JSON integers, other credentials, WebSocket settings and disabled-state fields are preserved. CPA strips its read-only auth-index projection when accepting the update. A concurrent CPA writer causes a 412/conflict, never an unconditional replacement. Missing ETag support fails closed.

No request is automatically retried. A failed or lost write response keeps the browser draft and requires an explicit refresh before another edit. Success is followed by a persisted-value readback from the **same installation generation**. A changed generation, even with the same provider/index/name, retains the draft and requires explicit refresh without replay. The returned ETag may change after our own save and is therefore not compared with the pre-write ETag. A successful readback does not claim the runtime has reloaded. The browser ignores responses from previous client/credential scopes and clears scoped state when switching accounts. Cancel is an explicit non-submit button. CPA 401/403 responses on the actual GET or PUT map to the existing typed management-rejection errors; External records the refusal against that request’s connection and protected credential envelope. Later reads and writes stop locally until rebind/re-verification, without returning upstream error bodies.

## Compatibility boundary for review

This is a first native-name editor, not the complete v2 Provider-management rewrite or a complete resolution of #492. It remains independent of U1 and Beta Gate. Request Monitoring and individual credential rows do not yet consume these names.

**Legacy Provider writes are paused in Full Web v2.** The old table/editor is not mounted on `/ai-providers` or any child route (including bookmarked edit/model pages). The replacement explains that old edit/add/delete, enable/disable and batch health-check writes are paused, and directs credential management to CPA's native management interface until a native Provider editor is available.

The v2 server bridge also refuses all mutating methods on the legacy `codex-api-key`, `claude-api-key`, `gemini-api-key`, `interactions-api-key`, `xai-api-key`, `vertex-api-key`, `openai-compatibility` and `meta-api-key` sections (and child paths), with HTTP 409 / `legacy_provider_write_paused`. This protects against stale tabs and direct legacy calls for Managed, Adopt and External installations. Legacy GET/HEAD and unrelated resources remain available. The guard is installed only by the Fresh v2 server; panel/v1 server behavior is unchanged.

This chooses the maintainer's bounded stop-write option rather than rebuilding the full Provider subsystem. A saved native name cannot subsequently be erased or its keys regrouped by these legacy Provider operations through this Manager. Out-of-band configuration replacement, other Managers and CPA's own writers remain operator-controlled changes; no cross-process ownership guarantee is claimed.

Name persistence across ordinary upgrades follows CPA's own configuration-volume lifecycle. Replacing binaries or frontend assets does not remove native configuration metadata; replacing the config volume/document or rebuilding groups through a legacy writer can. Back up the CPA configuration through the operator's existing secret-safe process. There is no second CPAMP database of labels to migrate.

## Verification

- CPA transport tests: actual GET/PUT 401/403 classification with no upstream-body leak; unsupported identity-family refusal; authenticated scoped reads/writes, required ETag, `If-Match`, 412, uncertain 5xx, no automatic replay, closed provider paths.
- Service tests: secret-free projection; name-only changes preserve other groups, credentials, WebSocket/disabled flags, unknown fields and large integers; stale baseline/generation, invalid names and frozen private scope refused.
- Canonical API tests: same-envelope External rejection fencing and rebind isolation; save followed by blocked legacy edit/enable/batch/delete across every family with multi-group/multi-key fixtures; Administrator authentication, strict request shape, no secret projection and one guarded upstream write.
- UI tests: jsdom native Cancel/Save form events, same-name cross-generation mismatch and delayed old readback, retired legacy list and child routes; save/readback, draft retention and no replay after failure, authenticated-scope isolation and malformed projection refusal.
- Manual UI preview uses fictional `example.com` accounts and an in-memory client. It proves editor interaction only; no production configuration or paid model request is used for this PR.

## 中文说明

v2 的「提供商分组名称」直接编辑 CPA 原生 `name`，可填邮箱或备注，不增加私有备注数据库。保存前校验安装代次与配置版本，并通过 `If-Match` 防止覆盖其他修改；保存后回读确认。名称属于整个凭证组；仅支持 codex、claude、gemini、interactions、xai、vertex。OpenAI 兼容名称具有身份语义，已排除；meta 暂不支持并明确提示。需要实际具备配置 ETag 与原子 If-Match 能力（v8.0.31 起的合同）。

本 PR 保持草稿，不承担完整 #492、U1 或 Beta Gate。为避免旧写操作丢名或拆组，v2 旧提供商列表及其编辑子页面已由名称编辑器和停写提示替代；服务端同时拒绝相关旧写接口。凭证变更暂由 CPA 原生管理界面处理。请求监控尚未复用这些名称。取消不提交；回读校验安装代次；401/403 接入既有拒绝记录，后续读写本地停止，换绑不继承旧凭据的拒绝。普通前后端升级只要保留 CPA 配置卷，名称即可继续保留。
