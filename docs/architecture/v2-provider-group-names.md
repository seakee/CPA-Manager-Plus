# Native provider group names (v2)

This implements the name-editing slice of [#492](https://github.com/seakee/CPA-Manager-Plus/issues/492), following the maintainer's decision to use CPA v8 `api-keys.<provider>[].name`. There is no CPAMP alias table or browser-side name store. This does not backport a feature to frozen v1.

## User behavior

Full Web v2 adds **Provider group names** above AI Providers. Operators can enter an email or another display label, edit an existing name, cancel a draft, and refresh the persisted names. Clearing optional metadata removes the `name` key; CPA remains responsible for rejecting a missing name where its own provider contract requires one. Names have a 120-code-point limit, must be trimmed, and cannot contain control characters.

A group can contain several credentials: the name belongs to the native **group**, not to an individual key. The provider and displayed group number locate a group only within the loaded configuration revision. Names and array positions are never stable identities, routing inputs, monitoring aggregation keys, deduplication keys, or historical linkage keys.

## Control-plane contract

Authenticated post-installation Administrator resources:

- `GET providers/names`: returns only `generation`, opaque `etag`, `writable`, and `groups` containing provider/index/name. Provider and client secrets, addresses, headers and raw configuration never leave Manager through this resource.
- `PUT providers/names`: accepts exactly `{generation, etag, provider, index, name}`. Unknown, duplicate, null and malformed fields are refused. Setup continuation credentials cannot write.

Manager resolves the same committed installation authority used by gateway configuration. The private H1 scope remains read-only; managed installations and supported External CPA v8 connections can save. External version and rejected-management-credential gates remain in effect.

CPA's configuration GET must provide an ETag. Manager reads a fresh secret-bearing document, checks the user's generation/revision, changes one `name` only, and replaces that provider array with `If-Match`. CPA's array contract has no index-addressable leaf writes. Unknown group/key fields, large JSON integers, other credentials, WebSocket settings and disabled-state fields are preserved. CPA strips its read-only auth-index projection when accepting the update. A concurrent CPA writer causes a 412/conflict, never an unconditional replacement. Missing ETag support fails closed.

No request is automatically retried. A failed or lost write response keeps the browser draft and requires an explicit refresh before another edit. Success is followed by a persisted-value readback; it does not claim the runtime has reloaded. The browser ignores responses from previous client/credential scopes and clears scoped state when switching accounts.

## Compatibility boundary for review

This is a first native-name editor, not the complete v2 Provider-management rewrite. The existing credential editor/table still uses the legacy compatibility projection; this PR does not change its routing, statistics, health checks or secret-bearing request contract. In particular, this slice does **not** yet reuse native group names in Request Monitoring or individual legacy credential rows. The old compatibility editor can reconstruct provider groups when saving credentials; preservation across those separate legacy mutations must be resolved as part of the native v2 Provider editor. For that reason this change is submitted as a **draft** for the v2 integration boundary to be reviewed, not as a v1 production upgrade.

Name persistence across ordinary upgrades follows CPA's own configuration-volume lifecycle. Replacing binaries or frontend assets does not remove native configuration metadata; replacing the config volume/document or rebuilding groups through a legacy writer can. Back up the CPA configuration through the operator's existing secret-safe process. There is no second CPAMP database of labels to migrate.

## Verification

- CPA transport tests: authenticated scoped reads/writes, required ETag, `If-Match`, 412, uncertain 5xx, no automatic replay, closed provider paths.
- Service tests: secret-free projection; name-only changes preserve other groups, credentials, WebSocket/disabled flags, unknown fields and large integers; stale baseline/generation, invalid names and frozen private scope refused.
- Canonical API tests: Administrator authentication, strict request shape, no secret projection and one guarded upstream write.
- UI tests: save/readback, draft retention and no replay after failure, authenticated-scope isolation and malformed projection refusal.
- Manual UI preview uses fictional `example.com` accounts and an in-memory client. It proves editor interaction only; no production configuration or paid model request is used for this PR.

## 中文说明

v2 的「提供商分组名称」直接编辑 CPA 原生 `name`，可填邮箱或备注，不增加私有备注数据库。保存前校验安装代次与配置版本，并通过 `If-Match` 防止覆盖其他修改；保存后回读确认。名称属于整个凭证组，仅用于展示。

本 PR 以草稿提交：旧凭证编辑器、请求监控尚未迁移到完整的 v2 原生分组模型，跨旧编辑器保存时的名称保留仍需随该迁移解决。普通前后端升级只要保留 CPA 配置卷，名称即可继续保留。
