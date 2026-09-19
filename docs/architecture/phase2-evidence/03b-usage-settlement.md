# Phase2-03B — Usage / Reservation / Settlement Evidence

## Meta

- **Task**: CPAMP v2 Phase2-03B — Usage / Reservation / Settlement Evidence
- **Status**: Complete / Proven
- **Base**: `v2@aca237cf7a99ce5d098dedcaec5cf8e147f05e4b`
- **Namespace**: `phase2-03b-*`
- **Machine-readable Fixture**: [`../../../tests/fixtures/phase2-evidence/extensions/phase2-03b-usage-settlement.json`](../../../tests/fixtures/phase2-evidence/extensions/phase2-03b-usage-settlement.json)
- **Automated Test**: [`../../../tests/phase2UsageSettlementEvidence.test.mjs`](../../../tests/phase2UsageSettlementEvidence.test.mjs)

---

## 1. 核心目标与概念分离

本报告通过对 CPA 官方版本 `v7.3.3`（当前 Bundled 产物）和 `v7.3.8`（候选 Release Fixture）的只读代码比对与源码调用链分析，精确划定 CPA `UsagePlugin` 与 usage pipeline 的能力边界：

- **观察（Observation / Telemetry）**：
  - 非流式（non-stream）成功请求的使用量与延迟观测；
  - 流式（stream）成功请求通过 SSE chunks 的动态观测与流终态聚合；
  - 上游 HTTP 失败（4xx/5xx）及超时的失败状态上报（`Failed: true`, `Failure: {StatusCode, Body}`）；
  - 客户端取消（cancel / disconnect）路径在 `context.WithoutCancel` 保障下的脱敏上报；
  - 上游无 token 返回时的保底零 token 计数上报（`EnsurePublished` 保证请求计数不丢失）；
  - 路由经由 Plugin Executor 时的使用量捕获与派发；
  - 插件 Panic 时的熔断隔离（`fusePlugin` 保护宿主主请求不崩溃）。
- **关联（Correlation）**：
  - 仅包含长会话与凭证维度的粗粒度标识（`SessionID`, `ParentSessionID`, `AuthID`, `AuthIndex`）；
  - **严重缺乏请求级与尝试级关联键**：`UsageRecord` 中不存在 `RequestID`、`TraceID`、`AttemptID` 或 `IdempotencyKey`。
- **预留与准入（Reservation / Admission）**：
  - **完全不存在（Unsupported）**：无任何前置预留 hook、资源预扣减接口或配额准入门禁。
- **权威结算（Authoritative Settlement）**：
  - **完全不存在（Unsupported）**：内部依赖内存切片队列（`[]queueItem`）异步单向分发，进程崩溃或重启直接丢弃，无 ACK、无持久化、无二阶段提交、无冲正回滚（rollback）、无幂等记账流水。

> [!IMPORTANT]
> **核心架构定论**：
> `UsagePlugin 收到 completed usage` $\neq$ `reservation` $\neq$ `authoritative exactly-once settlement`。
> CPAMP 严禁将 CPA 的事后使用量观察回调等同于权威结算流水，亦不在此复活 Usage Accounting V2。

---

## 2. 必须回答的核心问题详尽解答（精确版本溯源）

### 1. `UsageRecord` 实际字段清单（v7.3.3 vs v7.3.8）
在 `sdk/pluginapi/types.go` 的 `UsageRecord` 结构体中，两版经逐字段检验具有完全一致的字段定义：

| 字段 | 类型 | 说明 | `router-for-me/CLIProxyAPI@v7.3.3` | `router-for-me/CLIProxyAPI@v7.3.8` |
|---|---|---|:---:|:---:|
| `Provider` | `string` | 上游 Provider 标识 | 存在 | 存在 |
| `BaseURL` | `string` | 凭证或请求配置的 Base URL | 存在 | 存在 |
| `ExecutorType` | `string` | 具体的 Executor 实现类型（如 claude, codex, plugin-executor 等） | 存在 | 存在 |
| `Model` | `string` | 实际请求的模型标识 | 存在 | 存在 |
| `Alias` | `string` | 客户端请求的模型别名（未映射时回退为 Model） | 存在 | 存在 |
| `APIKey` | `string` | 客户端访问 APIKey 标识（当有客户端认证上下文时） | 存在 | 存在 |
| `SessionID` | `string` | 客户端提取或推断的会话 ID | 存在 | 存在 |
| `ParentSessionID` | `string` | 层级会话中的父会话 ID | 存在 | 存在 |
| `AuthID` | `string` | 选中的凭证标识（`auth.ID`） | 存在 | 存在 |
| `AuthIndex` | `string` | 凭证索引编号（`auth.Index` / `auth.EnsureIndex()`） | 存在 | 存在 |
| `AuthType` | `string` | 凭证类型（`auth.AuthKind()`） | 存在 | 存在 |
| `Source` | `string` | 凭证来源类型（`auth.AuthSourceKind()`） | 存在 | 存在 |
| `ReasoningEffort` | `string` | 思考强度（low, medium, high 等） | 存在 | 存在 |
| `ServiceTier` | `string` | 客户端请求或响应的服务层级 | 存在 | 存在 |
| `Generate` | `bool` | 是否请求生成（默认 true） | 存在 | 存在 |
| `RequestedAt` | `time.Time` | 请求接收时间戳 | 存在 | 存在 |
| `Latency` | `time.Duration` | 整个请求耗时 | 存在 | 存在 |
| `TTFT` | `time.Duration` | 流式首包延迟（Time To First Token） | 存在 | 存在 |
| `Failed` | `bool` | 是否失败 | 存在 | 存在 |
| `Failure` | `UsageFailure` | 包含 `StatusCode int` 和 `Body string` | 存在 | 存在 |
| `Detail` | `UsageDetail` | 包含 `InputTokens`, `OutputTokens`, `ReasoningTokens`, `CachedTokens`, `CacheReadTokens`, `CacheCreationTokens`, `TotalTokens int64` | 存在 | 存在 |
| `ResponseHeaders` | `http.Header` | 上游响应头镜像快照 | 存在 | 存在 |

### 2. 是否包含 RequestID / TraceID / AttemptID / IdempotencyKey？
- **结论**：**在 v7.3.3 与 v7.3.8 中均不存在（Absent）**。
- 源码证据：
  - `router-for-me/CLIProxyAPI@v7.3.3:sdk/pluginapi/types.go (UsageRecord)`
  - `router-for-me/CLIProxyAPI@v7.3.8:sdk/pluginapi/types.go (UsageRecord)`
- 分析：结构体中没有任何字段标识一次逻辑 HTTP 请求的唯一标识符（`RequestID`）、分布式追踪链路 ID（`TraceID`）、重试次序编号（`AttemptID`）或幂等键（`IdempotencyKey`）。现存的 `SessionID` 是面向客户端多轮长对话的粗粒度标识，不可用于唯一定位单次 HTTP 请求。

### 3. Usage 的发布层级（Publish Granularity）与版本差异
- **v7.3.3 实现机制**：
  - 源码证据：`router-for-me/CLIProxyAPI@v7.3.3:internal/runtime/executor/helps/usage_helpers.go` 中的 `NewExecutorUsageReporter`、`UsageReporter.publishWithOutcome` 和 `UsageReporter.publishRecord`。
  - 机制：在每个 Executor 每次调用（`Execute` 或 `ExecuteStream`）入口处创建独立的 `UsageReporter` 实例；请求结束时直接调用 `publishWithOutcome`，通过内部 `r.once.Do` 调用 `r.publishRecord` 投递到全局 `usage.PublishRecord`。
  - **重要版本定论**：`publishAttemptRecord` 在 **v7.3.3 中并不存在**。v7.3.3 通过 Executor 作用域的独立 Reporter 实例隐式实现了 attempt 级别的单独发布。
- **v7.3.8 演进机制**：
  - 源码证据：`router-for-me/CLIProxyAPI@v7.3.8:internal/runtime/executor/helps/usage_helpers.go` 中的 `UsageReporter.publishAttemptRecord`。
  - 机制：v7.3.8 显式重构提取了 `publishAttemptRecord` helper 方法，在其内部调用 `publishRecord` 并触发 `warnModelSubstitution` 警告，从架构上显式确立了“单次上游 attempt 级别发布”的契约。
- **共性结论**：两版中**均以 upstream-attempt 为单位直接发布**，不存在将多次 attempt 聚合为单一逻辑请求记录的中央聚合器。

### 4. Retry / Fallback 多 Attempt 时的行为与事实调用链
- **事实链推导**：
  ```text
  logical request
    → conductor attempt A
        → executor A creates UsageReporter A
        → failed attempt publishes failure usage via reporter A
    → conductor attempt B
        → executor B creates UsageReporter B
        → successful attempt publishes usage via reporter B
  ```
- **结论**：
  1. **每个失败的 attempt 均可能发出 usage**：当 attempt 发生错误返回时，Executor 的 `defer reporter.TrackFailure(ctx, &err)` 会触发 `PublishFailure`，向队列发送一条 `Failed: true` 的 `Record`（Token 计数通常为 0）。
  2. **最终成功会再次发出 usage**：Conductor（`sdk/cliproxy/auth/conductor_execution.go:Manager.executeMixedOnce`）重试或 fallback 到后继可用凭证并执行成功后，新 Executor 实例的 Reporter 会发出一条 `Failed: false` 的 `Record`（包含真实的 Token Detail）。
  3. **单个逻辑请求会触发多个 callbacks**：若一次逻辑请求经历 2 次重试（如 Attempt 1 失败，Attempt 2 失败，Attempt 3 成功），UsagePlugin 会相继收到 3 个独立的 `HandleUsage` 回调（2 条失败记录 + 1 条成功记录）。由于缺乏 RequestID/AttemptID，插件无法从原生数据中辨别它们属于同一次客户端调用。
  4. **`once.Do` 的局部作用域**：单个 `UsageReporter` 内部的 `r.once.Do` 仅限制**该 reporter 实例**的发布次数；重试过程生成了多个 reporter 实例，因此不会阻止跨 attempt 的多次 usage 发布。

### 5. Stream 场景行为（Success / Cancel / Disconnect）
- **结论**：两版在 `StreamUsageBuffer` 的流式处理契约上保持一致：
  - **正常成功结束**：流中的 SSE chunk 被 `StreamUsageBuffer` 收集（如 `ObserveClaudeStream` / `ObserveOpenAIStream`），在 stream 结束的 defer 中通过 `streamUsage.Publish(ctx, reporter)` 统一发布完整的 token usage。
  - **客户端取消 / 断开连接**：
    - 若在首个 token 帧到达前断开，`streamUsage.PublishFailure` 会发布包含 cancel 错误的失败记录；
    - 若在流式传输中断开但已观测到部分 token 帧，`StreamUsageBuffer` 会将已捕获的部分 tokens 发送出去；
    - 在两版的 `internal/pluginhost/adapters_usage_translation.go:usageAdapter.HandleUsage` 中，宿主均显式使用 `ctx = context.WithoutCancel(ctx)` 脱敏上下文。
    - **语义收窄说明**：`context.WithoutCancel` 仅避免 UsagePlugin callback 继承客户端取消状态，不构成持久化或可靠送达保证（仍受进程崩溃、队列丢失、熔断、RPC 失败等制约）。

### 6. Failure / Zero-token / Unknown Usage 路径
- **结论**：
  - **Upstream Failure**：发布包含 HTTP 状态码与错误文本的 `Failed: true` 记录，Token Detail 均为 0。
  - **Zero-token / Unknown Usage**：若上游成功响应但不返回 token 消耗（例如部分非标准模型），两版中的 `reporter.EnsurePublished(ctx)` 均兜底发送一条 `Detail: {TotalTokens: 0}, Failed: false` 的记录，确保请求计数（Invocation Count）不发生漂移。
  - **Model Substitution**：在 v7.3.8 中引入了模型替换警告（`warnModelSubstitution`），当上游实际返回的 `ResponseModel` 与请求的 Model 不一致时输出观察警告（v7.3.3 中无此警告逻辑）。

### 7. Plugin Executor Usage 路径
- **结论**：**两版中均受完整支持（Supported）**。
- 源码证据：
  - `router-for-me/CLIProxyAPI@v7.3.3:sdk/api/handlers/handlers_execution.go (ExecuteWithAuthManager)` 与 `handlers_stream.go`；
  - `router-for-me/CLIProxyAPI@v7.3.8:sdk/api/handlers/handlers_execution.go` 与 `handlers_stream.go`。
- 当请求被路由到 Plugin Executor 时，Handler 主动创建 `UsageReporter`，并在执行成功后调用 `parsePluginExecutorResponseUsage` 解析响应 payload 中的 token，随后调用 `reporter.Publish` 或在流式 defer 中调用 `EnsurePublished`。

### 8. 重复与回放（Duplicate / Replay）与 Exactly-Once 契约
- **结论**：**完全属于 Best-Effort Observation Callback，绝对无 Exactly-Once 契约（Unsupported）**。
  - 源码证据：`sdk/cliproxy/usage/manager.go (Manager.Publish, Manager.run)`。
  - 两版均采用内存切片队列：`queue []queueItem`，进程异常退出或重启直接丢失所有未派发事件；
  - 分发过程为单向投递，`safeInvoke` 捕获 panic 但不向调用链报告失败，无 ACK，无重试队列；
  - 没有任何事务去重键（deduplication key）或事务提交机制。

### 9. UsagePlugin 异常处理与容错弹性（Unavailable / Error / Panic / Fuse / Reload）
- **结论**：
  - **Unavailable**：若未加载任何 UsagePlugin，记录在分发层直接静默忽略；
  - **Callback Error**：`HandleUsage` 签名无 error 返回值；RPC 模式下（`internal/pluginhost/rpc_client.go:rpcPluginAdapter.HandleUsage`）若网络调用失败，仅记录 debug 日志并静默忽略，不影响主请求；
  - **Panic & Fuse**：若插件 `HandleUsage` 发生 panic，`adapters_usage_translation.go:usageAdapter.HandleUsage` 会捕获 panic 并调用 `host.fusePlugin`，将该插件状态置为熔断（fused）。熔断后该插件被移出活跃列表，后续 usage 回调直接跳过，宿主进程与主请求绝不会因为插件 panic 而崩溃；
  - **Reload**：热重载时动态刷新插件指针并重新注册，支持无缝热替换。

### 10. Usage Callback 与 RequestCompletion 的顺序关系（两层异步解耦）
- **结论**：**双通道异步解耦，无通用顺序保证（No Universal Ordering Guarantee）**。
  - **Usage 派发路径**：
    ```text
    UsageReporter
    → usage.Manager.Publish
    → in-memory queue ([]queueItem)
    → background worker goroutine
    → UsagePlugin.HandleUsage
    ```
  - **Request completion 派发路径**：
    ```text
    requestLifecycleTracker.complete
    → Host.CompleteRequest
    → Host.CompleteRequestExcept
    → goroutine (go func() { plugin.HandleRequestComplete(ctx, completion) }())
    → RequestLifecyclePlugin.HandleRequestComplete
    ```
  - 源码证据：`internal/pluginhost/adapters_interceptors.go#CompleteRequestExcept` 明确通过单独的 `go func()` 异步调度 `HandleRequestComplete`。
  - 两条路径之间不存在 ACK、barrier、transaction 或持久化顺序协议。因此 UsagePlugin callback 与 RequestLifecyclePlugin callback 不存在可依赖的全局先后顺序。

### 11. 是否存在 Pre-request Reserve / Quota Admission / Commit / Rollback 原语？
- **结论**：**绝对不存在（Unsupported）**。
  - 源码证据：
    - `router-for-me/CLIProxyAPI@v7.3.3:sdk/pluginapi/types.go (UsagePlugin)` 及 `sdk/cliproxy/usage/`
    - `router-for-me/CLIProxyAPI@v7.3.8:sdk/pluginapi/types.go (UsagePlugin, QuotaMetric)` 及 `sdk/cliproxy/usage/`
  - CPA 核心代码与插件接口中没有任何：
    - `Pre-request Reservation`（请求前预冻结 token/费用）；
    - `Quota Admission Hook`（基于额度的准入门禁）；
    - `Authoritative Settlement`（二阶段提交权威结算）；
    - `Rollback`（失败冲正与回滚机制）；
    - `Idempotent Journal`（幂等记账流水）。
  - v7.3.8 虽然在 `QuotaFetchResponse` 中增加了用于管理 UI 展示的 `Summary []QuotaMetric`，但这属于静态只读查询，在运行时模型请求热路径上没有任何额度预留或准入门禁。

### 12. External 未协商时如何分类？
- **结论**：**分类为 `unknown` 或 `partial`，不得推断为 supported**。
  - 在 External Runtime 未连接且未显式协商版本与插件契约时，`pluginContract` 必须为 `null`；
  - 无法预先保证外部 CPA 启用了 UsagePlugin 或配置了相同的数据流；
  - 依据 Phase2-01 合同规则，External 观测边界严禁跨版本推断或自动晋升。

---

## 3. 证据矩阵（Capability Records 总结）

在 [`../../../tests/fixtures/phase2-evidence/extensions/phase2-03b-usage-settlement.json`](../../../tests/fixtures/phase2-evidence/extensions/phase2-03b-usage-settlement.json) 中，我们冻结了 15 个 Evidence Anchors（清除伪 unit 测试，完全基于真实源码链）和 29 个 Capability Records：

| 领域 | 能力标识 (`capability`) | Bundled v7.3.3 | Candidate v7.3.8 | External (Unnegotiated) | 关键限制与版本边界说明 |
|---|---|:---:|:---:|:---:|---|
| **基本观测** | `non_stream_usage_observation` | `supported` | `supported` | `unknown` | 异步观测已完成请求的 Token 与延迟，无 RequestID 关联 |
| **流式观测** | `stream_usage_observation` | `supported` | `supported` | `unknown` | 依赖上游 SSE 输出 token 帧，流中断上报已观测 tokens |
| **失败上报** | `upstream_failure_reporting` | `supported` | `supported` | `unknown` | 上报 Failed=true 与 HTTP 错误码，Token 为 0 |
| **取消上报** | `cancellation_reporting` | `supported` | `supported` | `unknown` | `context.WithoutCancel` 避免继承客户端取消，但不保证可靠送达 |
| **重试回调** | `retry_fallback_attempt_callbacks` | `partial` | `partial` | `unknown` | 每次 attempt 独立回调，单个逻辑请求对应多个 callbacks；v7.3.3 无 `publishAttemptRecord` helper |
| **保底计数** | `zero_unknown_usage_handling` | `supported` | `supported` | `unknown` | `EnsurePublished` 兜底生成零 token 记录保障请求计数 |
| **插件路由** | `plugin_executor_usage_dispatch` | `supported` | `supported` | `unknown` | 经 Plugin Executor 转发的请求完整接入 usage pipeline |
| **容错弹性** | `usage_plugin_fault_resilience` | `supported` | `supported` | `unknown` | Panic 自动熔断（fuse），内存队列崩溃丢数据，主请求不中断 |
| **粗粒度标识**| `request_correlation_keys` | `partial` | `partial` | `unknown` | 仅提供 SessionID 与 AuthID，缺少 RequestID/AttemptID |
| **精确请求关联**| `exact_request_correlation` | `requires_upstream` | `requires_upstream` | `unknown` | 需要 CPA 上游在 `UsageRecord` 中新增 RequestID 与 AttemptID |
| **权威交付** | `exactly_once_delivery` | `unsupported` | `unsupported` | `unknown` | 仅内存队列最佳努力投递，无持久化日志与去重确认机制 |
| **配额预留** | `pre_request_reservation` | `unsupported` | `unsupported` | `partial` | CPA 无任何请求前额度预留或准入门禁原语 |
| **最终结算** | `authoritative_settlement` | `unsupported` | `unsupported` | `partial` | CPA 无二阶段提交、冲正回滚或幂等记账结算流水 |

---

## 4. 验证与回归保证

所有结论已通过自动化工具完成全量回归：

1. **Phase 2 Evidence 校验**：
   ```bash
   node bin/ci/validate-phase2-evidence.mjs \
     --evidence tests/fixtures/phase2-evidence/extensions/phase2-03b-usage-settlement.json \
     --json
   # 输出：15 anchors, 29 records 全部合规通过，无 Schema 违背与悬空引用
   ```
2. **自动化端到端测试套件**：
   ```bash
   npx vitest run tests/phase2UsageSettlementEvidence.test.mjs
   # 14 项深度测试断言全部通过（含版本隔离、无伪 unit anchor、无本地硬编码路径、仓库相对路径校验等）
   ```
3. **全库测试套件回归**：
   ```bash
   npm run test:repo
   # 13 个测试套件，244 项测试全部 PASS
   ```
4. **代码格式与 Diff 检查**：
   ```bash
   git diff --check
   # 零警告，零空白符问题，文件树中严格不存在任何本机硬编码路径
   ```
