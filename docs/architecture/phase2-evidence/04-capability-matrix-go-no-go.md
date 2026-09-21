# Phase2-04 — Capability Matrix Integration / Go/No-Go

本文档收敛 CPAMP v2 Phase2-01、02A、02B、03A、03B 已独立验收的证据，发布按精确
artifact、Plugin 配置和 deployment mode 隔离的产品决策。它是 Phase2 的串行退出产物，
不是产品功能实现，也不修改 CPA bundle、Manager、Runtime、Web、数据库或上游 CPA。

- 集成基线：`v2@d3ddec55346085edae6cc36527106b650f5f438d`
- 机器可读决策：[`phase2-exit-decisions.json`](../../../tests/fixtures/phase2-evidence/phase2-exit-decisions.json)
- 聚合守卫：[`phase2CapabilityMatrixIntegration.test.mjs`](../../../tests/phase2CapabilityMatrixIntegration.test.mjs)
- 冻结合同：[`contract.json`](../../../tests/fixtures/phase2-evidence/contract.json)

## 1. 决策规则

Capability status 与产品决策是两套不同词汇：

| 层级 | 词汇                | 含义                                                     |
| ---- | ------------------- | -------------------------------------------------------- |
| 证据 | `supported`         | 精确 artifact/config/mode 下存在直接证据。               |
| 证据 | `partial`           | 只覆盖声明的阶段、配置或失败边界。                       |
| 证据 | `unsupported`       | 当前合同明确缺少所需原语。                               |
| 证据 | `unknown`           | 没有完成 artifact/version/config negotiation，不能推断。 |
| 证据 | `requires_upstream` | 必须由 CPA 或 Plugin contract 增加能力。                 |
| 产品 | `go`                | 后续任务可在精确前提下直接消费。                         |
| 产品 | `limited`           | 只允许在列明边界内消费，并保留显式降级。                 |
| 产品 | `no_go`             | 当前版本不得启用或承诺该产品能力。                       |
| 产品 | `deferred`          | 等待 upstream 或独立证据，当前不实现。                   |

集成遵循单调性：`partial`、`unknown`、`unsupported`、`requires_upstream` 不能提升为
`supported`；candidate 不能提升为 current bundle；Embedded 不能推导 External。每项决策只
引用已接受的 capability record ID，详细原始证据继续由各 extension 持有。

## 2. Artifact 支持边界

| Artifact                   | CPA / Mode               | Plugin ABI | 产品角色                          | Bundle 结论            |
| -------------------------- | ------------------------ | ---------- | --------------------------------- | ---------------------- |
| `current-bundled-v7-3-3`   | v7.3.3 / Embedded        | schema 6   | 当前捆绑运行时                    | 保持 v7.3.3。          |
| `candidate-release-v7-3-8` | v7.3.8 / release fixture | schema 6   | 候选证据                          | Phase2 不升级 bundle。 |
| `external-unnegotiated`    | unknown / External       | unknown    | Advanced Compatibility 未协商状态 | 所有能力 fail-closed。 |

## 3. 最终 Capability Matrix 与 Go/No-Go

| Decision ID                                       | Artifact / Config                         | Capability          | 产品决策   | 最终边界                                                                                                                                                               |
| ------------------------------------------------- | ----------------------------------------- | ------------------- | ---------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `current-caller-identity-source`                  | v7.3.3 Embedded                           | `partial`           | `limited`  | `caller_scope` 可作 caller/session provenance，但随 key rotation 改变，不是 Canonical APIKeyID；raw credential 边界必须脱敏。                                          |
| `candidate-caller-identity-source`                | v7.3.8 candidate                          | `partial`           | `limited`  | 与 current 相同的身份/生命周期限制；不能由 candidate 推导 bundle 或 Canonical ID。                                                                                     |
| `current-hard-routing-default`                    | v7.3.3 scheduler                          | `partial`           | `limited`  | 仅对 CPA 仍认可的 eligible Auth.ID 使用 `pinned_auth_id`；最高 priority tier 可见，目标不可用时 fail-closed。                                                          |
| `candidate-hard-routing-default`                  | v7.3.8 scheduler，across-priorities=false | `partial`           | `limited`  | 默认仍只见最高 priority tier；机器决策只引用 default-compatible records，不继承 opt-in pre-filter record；invalid scheduler ID 的 builtin fallback 不算 hard routing。 |
| `candidate-hard-routing-across-priorities-opt-in` | v7.3.8 scheduler，across-priorities=true  | `partial`           | `limited`  | opt-in 后可跨 priority 看见 eligible candidates，但 pre-filter、invalid fallback、stream retry 和 candidate 身份仍限制产品承诺。                                       |
| `current-request-correlation-continuity`          | v7.3.3 lifecycle/interceptor              | `supported`         | `go`       | 已创建 tracker 的单次模型请求可贯穿同一 RequestID；不代表 attempt identity。                                                                                           |
| `candidate-request-correlation-continuity`        | v7.3.8 lifecycle/interceptor              | `supported`         | `go`       | 保持相同 RequestID continuity；仍不授权 bundle upgrade。                                                                                                               |
| `current-attempt-correlation`                     | v7.3.3 lifecycle + usage                  | `requires_upstream` | `deferred` | 重试没有 stable AttemptID/ordinal，UsageRecord 也没有精确 request/attempt/idempotency key。                                                                            |
| `candidate-attempt-correlation`                   | v7.3.8 lifecycle + usage                  | `requires_upstream` | `deferred` | `publishAttemptRecord` 不等于稳定 attempt identity，不能重构 attempt ledger。                                                                                          |
| `current-terminal-observation`                    | v7.3.3 lifecycle                          | `partial`           | `limited`  | tracker 内 terminal invocation 为 exactly-once，但 pre-tracker 请求、plugin fuse/reload/unavailable 会产生盲区或丢失。                                                 |
| `candidate-terminal-observation`                  | v7.3.8 lifecycle                          | `partial`           | `limited`  | cardinality 与 delivery/resilience 必须分开；candidate 仍是内存 best-effort delivery。                                                                                 |
| `current-usage-observation`                       | v7.3.3 usage plugin                       | `partial`           | `limited`  | completed usage 可用于 observed/notify；stream cancel、retry、additional-model、缺失/迟到/重复必须进入 freshness/coverage/unknown。                                    |
| `candidate-usage-observation`                     | v7.3.8 usage plugin                       | `partial`           | `limited`  | candidate 增强不产生 authoritative ledger，也不解决 exact request/attempt correlation。                                                                                |
| `current-precise-token-cost-quota`                | v7.3.3 usage plugin                       | `unsupported`       | `no_go`    | 无 exactly-once delivery、并发 reservation、rollback 或幂等 settlement；precise token/cost hard cap 必须禁用。                                                         |
| `candidate-precise-token-cost-quota`              | v7.3.8 usage plugin                       | `unsupported`       | `no_go`    | candidate 同样缺少 reservation/settlement；generic interceptor terminate 不能提升为 precise hard quota。                                                               |
| `external-capability-negotiation`                 | External unknown                          | `unknown`           | `deferred` | artifact/version、Plugin ABI/config、capability generation 未协商；不能继承 Embedded/candidate 结论。                                                                  |

## 4. Identity 与 Credential Selection

### 4.1 Caller identity

`caller_scope` 的有效定位是 caller/session scoping 和 source reconciliation provenance。它由已认证
principal 派生，随下游 client API key 轮换，不满足不可复用、跨轮换稳定的 Canonical ID 语义。
Phase3 G1 必须创建独立 `APIKeyID` / `CredentialID`，将 Runtime identity、CPA `Auth.ID` 与
`caller_scope` 作为 versioned source binding，而不是主键。

Secret boundary 仍是 `partial`：正常 RequestCompletion metadata 不复制内置 raw client key，
但 RequestInterceptor headers、scheduler/usage 的部分路径仍可接触原始值。任何后续 adapter 都必须
显式 redaction，不能因为 hash/caller scope 存在就宣称 secret 已从完整生命周期移除。

### 4.2 Hard Routing

当前可接受的最窄语义是：只对 CPA 已纳入 eligible candidate set 的 `Auth.ID` 进行 pinning，
目标 missing、disabled、cooldown 或其他 validation failure 时 fail-closed。Plugin scheduler 的
pre-filter evidence 必须继续按精确配置隔离，不能从 opt-in 外推到 default。

- v7.3.3 default：scheduler 只看最高可用 priority tier，结论为 `limited`。
- v7.3.8 default：仍只看最高 tier，结论为 `limited`；机器决策引用 default-config visibility、
  valid/invalid scheduler、pinned fencing、selected observation 与 stream retry records，不引用
  `scheduler_across_priorities=true` 的 pre-filter record。
- v7.3.8 opt-in：`SchedulerAcrossPriorities=true` 后可跨 priority 看见 eligible candidates，
  但因 pre-filter、invalid-ID fallback、stream retry 和 candidate/bundle 边界，整体仍为 `limited`。
- scheduler 返回不在候选集的 ID 时，Host 会丢弃响应并回退 builtin selector。这个行为不能被 UI、
  Policy 或审计记录描述为“目标路由成功”。显式目标必须走 fail-closed 的 pinned contract。

## 5. Request、Attempt 与 Terminal

RequestID、AttemptID、terminal invocation 与 delivery resilience 是四个独立事实：

1. RequestID continuity 在 current/candidate 的已跟踪请求中为 `supported/go`。
2. AttemptID/ordinal 不存在，重试只覆盖共享 metadata；per-attempt correlation 为
   `requires_upstream/deferred`。
3. tracker 内 terminal completion 通过 `sync.Once` 保证 invocation cardinality，但它只描述调用
   基数，不描述持久投递。
4. pre-tracker rejection、plugin fuse/reload/unavailable 会产生零通知或丢失，因此 terminal
   observation 是 `partial/limited`。

缺失 terminal callback 必须解释为 coverage unknown，不能解释为请求没有发生；也不能按 callback
顺序或最终 `selected_auth_id` 逆推出完整重试历史。

## 6. Usage、Reservation 与 Settlement

### 6.1 Observed / Notify

current/candidate 都能观察 completed non-stream/stream usage，但这是异步、best-effort 证据：

- stream cancellation 可能只有 partial/zero token；
- 一次逻辑请求可以包含多个 retry/fallback attempt；
- Codex additional-model/image-tool 可让 callback 数超过 attempt 数；
- UsageRecord 缺少精确 RequestID、AttemptID 和 IdempotencyKey；
- plugin queue 没有持久 journal、ACK、replay/dedup。

因此 Phase5 R1 可以推进 `observed/notify`，但必须携带 freshness、coverage、unknown、missing price
和 late usage 语义。历史权威仍是 CPAMP `usage_events`；后续 projection 可重建，但不能改写原始事件。

### 6.2 Precise hard quota

v7.3.3 与 v7.3.8 对 precise token/cost hard quota 都是 `unsupported/no_go`。当前不存在：

- 并发安全的 pre-request reservation；
- abandoned reservation 的过期/rollback；
- 缺失、迟到、重复、partial usage 的幂等 settlement；
- authoritative ledger 与 persistent exactly-once delivery。

`RequestInterceptResponse.Terminate` 只能证明 generic pre-request termination 入口存在，不能证明某个
token/cost metric 的并发 reservation/settlement。soft、request-count 或其他 metric 必须各自按精确
版本/配置重新验收，不能由本矩阵自动提升。

## 7. External negotiation

External 是 Advanced Compatibility Mode。启用任何受能力约束的功能前，至少必须协商并冻结：

1. `artifact_id`；
2. `cpa_version`；
3. `deployment_mode`；
4. `plugin_abi_schema_version`；
5. `enabled_plugin_config`；
6. `capability_generation`。

任一字段 unknown、未协商或 generation stale 时都必须 fail-closed。External 仍可保持基础 management
compatibility，但 Identity、Routing、Correlation、Usage/Quota 等能力保持禁用，直到独立 compatibility
验收完成。

## 8. Upstream dependency list

| Dependency                                 | 阻断范围                                                          | 最小合同                                                                                             | Owners                                                |
| ------------------------------------------ | ----------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- | ----------------------------------------------------- |
| `upstream-attempt-and-usage-correlation`   | exact request/attempt correlation、per-attempt policy、hard quota | UsageRecord RequestID、stable AttemptID/ordinal、idempotency identity                                | CPA Runtime、Plugin contract、Phase3 G2、Phase5 R2–R4 |
| `upstream-durable-observation-delivery`    | authoritative terminal/usage delivery                             | durable journal/queue、ACK、replay/dedup、reload/fuse 语义                                           | CPA Runtime、Plugin contract、Phase5 R2–R4            |
| `upstream-reservation-and-settlement`      | precise token/cost hard quota                                     | concurrency-safe reservation、idempotent settlement、rollback/expiry、missing/partial usage handling | CPA Runtime、Plugin contract、Phase5 R2–R4            |
| `external-capability-negotiation-contract` | External capability enablement                                    | artifact/version、ABI、config、generation、mode                                                      | Runtime Bridge、Release matrix                        |

这些 dependency 是后续能力 Gate，不是要求 Phase2-04 继续扩张实现。明确 `unsupported` 或
`requires_upstream` 是合法的 Phase2 退出结果。

## 9. Downstream handoff

| Owner          | 可以消费                                                        | 禁止假设                                                     | 交接动作                                                                     |
| -------------- | --------------------------------------------------------------- | ------------------------------------------------------------ | ---------------------------------------------------------------------------- |
| Phase3 G1      | Runtime identity、CPA Auth.ID、caller_scope provenance          | caller_scope 是 Canonical APIKeyID                           | 创建不可复用 Canonical APIKeyID/CredentialID、generation 和 source binding。 |
| Phase3 G2      | RequestID、selected credential observation、现有 usage evidence | attempt history 可重构；可改写 `usage_events`                | 建立 shadow mapping 与最小 Policy/Event 关联，保持 `usage_events` 不可变。   |
| Phase5 R1      | completed usage observation                                     | usage 是 authoritative balance                               | 交付带 freshness/coverage/unknown 的 observed/notify。                       |
| Phase5 R2–R4   | eligible pinning、后续 metric-specific capability               | generic terminate 等于 hard quota；current 可跨所有 priority | 每个 soft/hard/ACL/group 功能独立按 metric/version/config 验收。             |
| Runtime Bridge | 产品真实需要的窄 adapter                                        | 需要第二套 Runtime Protocol；External 可 fail-open           | 只适配已证明能力，并对 unknown/stale negotiation fail-closed。               |
| Release matrix | 精确 artifact/config/mode 决策                                  | candidate 自动升级 bundle；External 继承 Embedded            | 单独完成 bundle/compatibility 验收后再声明支持。                             |

## 10. Phase2 退出结论

Phase2-04 的集成结果为 **`go_with_explicit_limits`**，进入独立验收：

- Caller identity source、Hard Routing、terminal/usage observation 均有明确 `limited` 边界；
- RequestID continuity 在精确 artifact/config 下为 `go`；
- Attempt correlation 与 External negotiation 为 `deferred`；
- precise token/cost hard quota 为 `no_go`；
- 不升级 CPA bundle，不改变产品行为，不开始 Phase3。

只有 Required checks 与独立验收均通过后，Phase2 才能标记 complete，并开放 Phase3 G1。任何真实
evidence 矛盾或 frozen contract defect 都必须停止并单独处理，不能在本集成层改写历史证据。

## 11. Verification

```bash
npm run evidence:phase2 -- --evidence tests/fixtures/phase2-evidence/extensions/phase2-02a-caller-identity.json
npm run evidence:phase2 -- --evidence tests/fixtures/phase2-evidence/extensions/phase2-02b-candidate-selection.json
npm run evidence:phase2 -- --evidence tests/fixtures/phase2-evidence/extensions/phase2-03a-request-correlation.json
npm run evidence:phase2 -- --evidence tests/fixtures/phase2-evidence/extensions/phase2-03b-usage-settlement.json
npx vitest run tests/phase2EvidenceContract.test.mjs tests/phase2CallerIdentityEvidence.test.mjs tests/phase2CandidateSelectionEvidence.test.mjs tests/phase2RequestCorrelationEvidence.test.mjs tests/phase2UsageSettlementEvidence.test.mjs tests/phase2CapabilityMatrixIntegration.test.mjs
npm run test:repo
git diff --check
```
