# Phase2-03A — Request / Attempt / Terminal Correlation Evidence

本文档记录 **CPAMP v2 Phase2-03A — Request / Attempt / Terminal Correlation Evidence** 的证据分析、源码追踪、基数保证与边界证明。本项工作为纯证据（Evidence-only），不引入新的 Request Event 协议、不新增 Manager journal、不设计配额结算（Quota Settlement）、不引入生产 Bridge Plugin 或运行时用户功能。

对应机器可读证据扩展：[`tests/fixtures/phase2-evidence/extensions/phase2-03a-request-correlation.json`](file:///Users/seakee/WorkSpace/Worktree/CPA-Manager-Plus/test/v2-phase2-03a-request-correlation-evidence/tests/fixtures/phase2-evidence/extensions/phase2-03a-request-correlation.json)  
对应测试验证套件：[`tests/phase2RequestCorrelationEvidence.test.mjs`](file:///Users/seakee/WorkSpace/Worktree/CPA-Manager-Plus/test/v2-phase2-03a-request-correlation-evidence/tests/phase2RequestCorrelationEvidence.test.mjs)

---

## 1. 核心结论摘要 (Capability Summary)

| Capability ID                                 | 目标能力                            | v7.3.3 (Current Bundled) | v7.3.8 (Candidate Fixture) | External (Unnegotiated) | 核心证据与边界                                                                                                                                                         |
| --------------------------------------------- | ----------------------------------- | ------------------------ | -------------------------- | ----------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `phase2-03a-*-request-correlation-continuity` | `request_correlation_continuity`    | **`supported`**          | **`supported`**            | **`unknown`**           | 模型执行入口生成独立 UUID RequestID，继承上下文 TraceID；before-auth、after-auth、response/stream interceptor 及 RequestCompletion 端到端传递同一 RequestID。          |
| `phase2-03a-*-attempt-identity-correlation`   | `attempt_identity_correlation`      | **`requires_upstream`**  | **`requires_upstream`**    | **`unknown`**           | 重试保持相同 RequestID 且当次将 Auth.ID 写入 metadata；但 upstream CPA 不分配或传递 stable attempt ID/ordinal，且 terminal callback 仅触发一次，无法重构重试序列历史。 |
| `phase2-03a-*-terminal-callback-cardinality`  | `terminal_callback_cardinality`     | **`supported`**          | **`supported`**            | **`unknown`**           | `requestLifecycleTracker.complete` 受 `sync.Once` 保护；涵盖成功、失败、拦截器拒绝与客户端取消/断开，基数严格为 Exactly-Once。                                         |
| `phase2-03a-*-lifecycle-resilience-boundary`  | `lifecycle_coverage_and_resilience` | **`partial`**            | **`partial`**              | **`unknown`**           | 未进入 handler 的请求（401/404/bad JSON）无 tracker；插件熔断 (fused)、reload 或不可用时，内存异步分发直接丢弃终态通知，无可靠性重试。                                 |

---

## 2. RequestID 与 TraceID 架构分析

### 2.1 创建位置与生命周期

- **RequestID 创建位置**：
  在 `sdk/api/handlers/handlers_interceptors.go` 的 `BaseAPIHandler.newRequestLifecycleTracker` 中：

  ```go
  func (h *BaseAPIHandler) newRequestLifecycleTracker(ctx context.Context, sourceFormat, model, requestedModel string, stream bool, metadata map[string]any, skipPluginID string) *requestLifecycleTracker {
      requestID := uuid.NewString()
      traceID := logging.GetRequestID(ctx)
      return &requestLifecycleTracker{
          ...
          completion: pluginapi.RequestCompletion{
              RequestID:      requestID,
              TraceID:        traceID,
              ...
          },
      }
  }
  ```

  每次调用均生成新的独立 UUID 字符串，代表**一次单体模型请求的执行生命周期**。

- **TraceID 来源与关系**：
  - `TraceID` 取自 `logging.GetRequestID(ctx)`，由最外层的 Gin 中间件注入（或由客户端入站请求头 `X-Request-Id` / `X-Trace-Id` 传递），标识**入站 HTTP 连接链路**。
  - `RequestID` 则标识**核心模型调用生命周期**。
  - 在 `pluginapi.RequestInterceptRequest` 和 `pluginapi.RequestCompletion` 中，两者并存，形成 `TraceID (HTTP) -> RequestID (Model Execution)` 的关联。

### 2.2 阶段贯穿与连续性 (Hook Continuity)

在 `handlers_execution.go` 与 `handlers_stream.go` 中，同一个 `requestLifecycleTracker` 实例派发 RequestID 至所有生命周期钩子：

1. **Before-Auth RequestID**：
   通过 `applyRequestInterceptorsBeforeAuth` 传入 `lifecycle.requestID()`，插件收到 `RequestInterceptRequest.RequestID`。
2. **After-Auth RequestID**：
   通过 `h.requestAfterAuthInterceptor(..., lifecycle.requestID(), ...)` 绑定闭包。在凭据选择后由 `conductor` 调用，传入完全相同的 RequestID。
3. **Response Interceptor RequestID**：
   非流式响应成功后调用 `applyResponseInterceptors`，传入 `lifecycle.requestID()`。
4. **Stream Hook RequestID**：
   流式响应时，由 `interceptStreamChunk` 逐 chunk 触发，每个 chunk（包括 HeaderInit）均携带完全相同的 `lifecycle.requestID()`。
5. **Terminal RequestCompletion RequestID**：
   最终 `complete` 触发时，`completion.RequestID` 保持同一 UUID。

**结论**：在 v7.3.3 与 v7.3.8 中，RequestID 在所有阶段**完全一致且强延续**。

---

## 3. Attempt 标识符与 Retry / Fallback 关联强度分析

### 3.1 核心问题与源码事实

- **重试是否保持同一个 RequestID？**
  **是**。在 `sdk/cliproxy/auth/conductor_execution.go` 中：

  ```go
  for attempt := 0; ; attempt++ {
      ...
      resp, errExec := m.executeMixedOnce(ctx, normalized, req, roundOpts, maxRetryCredentials, attempt, defaultRequestRetry)
      ...
  }
  ```

  外部传入的 `opts.RequestAfterAuthInterceptor` 闭包封装了固定的 `lifecycle.requestID()`。因此重试期间所有的 after-auth interceptor 观察到的 RequestID 始终不变。

- **是否存在独立的 stable attempt ID 或 attempt ordinal？**
  **否**。
  1. `conductor_execution.go` 内部的 `attempt` 仅为函数局部的循环计数器。
  2. 传递给 after-auth interceptor 的 `RequestAfterAuthInterceptRequest`（定义于 `sdk/cliproxy/executor/types.go`）仅包含：
     - `SourceFormat`, `ToFormat`, `Model`, `RequestedModel`, `Stream`, `Headers`, `Body`, `Metadata`。
       **没有任何 `AttemptID`、`AttemptUUID` 或 `AttemptIndex` 字段**。
  3. 传递给 plugin 的 `pluginapi.RequestInterceptRequest` 同样不存在 attempt 字段。

- **Selected Auth.ID 与 Provider 能否与 attempt 可靠关联？**
  - **在尝试执行中 (In-flight attempt)**：每次选取凭据后，`publishSelectedAuthMetadata(opts.Metadata, auth)` 会将 `auth.ID` 写入 `opts.Metadata["selected_auth_id"]`，并在 after-auth 钩子中可见。
  - **在终端回调中 (Terminal RequestCompletion)**：
    由于重试循环直接就地覆盖 `opts.Metadata` 中的值，终态回调 `RequestCompletion.Metadata` 仅反映**最后一次重试（成功或最终失败）所写入的凭据**。
    如果第 1 次使用 Auth A 失败，重试切换至 Auth B 成功，终端回调仅包含 Auth B，Auth A 的尝试历史完全丢失。
  - **缺少 Attempt-level Terminal Observation**：CPA 没有为每个 attempt 设立独立的生命周期完成事件。

### 3.2 判定结果：`requires_upstream`

因为无法从 upstream 提供的事件流中独立重构出具备稳定唯一标识的 credential attempt 链路，不能人为伪造支持，该能力判定为标准的 **`requires_upstream`**。

---

## 4. 终态回调基数与覆盖场景 (Terminal Cardinality)

### 4.1 Exactly-Once 保证机制

在 `sdk/api/handlers/handlers_interceptors.go` 中，`requestLifecycleTracker.complete` 严格通过 `sync.Once` 保护：

```go
func (t *requestLifecycleTracker) complete(outcome pluginapi.RequestCompletionOutcome, statusCode int, err error) {
    if t == nil {
        return
    }
    t.once.Do(func() {
        completion := t.completion
        completion.Outcome = outcome
        completion.StatusCode = statusCode
        completion.CompletedAt = time.Now()
        ...
        host.CompleteRequest(t.ctx, completion)
    })
}
```

### 4.2 场景覆盖验证

| 场景                   | 触发代码路径                            | 终态 Outcome                 | HTTP StatusCode             | Callback Cardinality |
| ---------------------- | --------------------------------------- | ---------------------------- | --------------------------- | -------------------- |
| **Non-stream Success** | `handlers_execution.go:107`             | `RequestCompletionSucceeded` | `200`                       | **1 (Exactly-once)** |
| **Stream Success**     | `handlers_stream.go:153` / `608`        | `RequestCompletionSucceeded` | `200`                       | **1 (Exactly-once)** |
| **Pre-auth Reject**    | `handlers_execution.go:96`              | `RequestCompletionRejected`  | Interceptor 设定值 (如 429) | **1 (Exactly-once)** |
| **After-auth Reject**  | `conductor_execution.go:348`            | `RequestCompletionRejected`  | Interceptor 设定值 (如 403) | **1 (Exactly-once)** |
| **Upstream Failure**   | `handlers_execution.go:102` (重试耗尽)  | `RequestCompletionFailed`    | 错误对应值 (如 502/503)     | **1 (Exactly-once)** |
| **Downstream Cancel**  | `completeError` 识别 `ctx.Err() != nil` | `RequestCompletionCanceled`  | `0` (CPA 约定清零)          | **1 (Exactly-once)** |
| **Stream Disconnect**  | 流式读取期间客户端断开                  | `RequestCompletionCanceled`  | `0`                         | **1 (Exactly-once)** |

---

## 5. 生命周期盲区与插件容错边界 (Resilience Boundaries)

### 5.1 不会创建 Tracker 的请求路径 (Blind Spots)

以下请求根本不会进入 `executeWithAuthManagerFormats`，因此**完全不会创建 lifecycle tracker，产生 0 次 RequestCompletion**：

1. **客户端访问认证未通过**：
   在 `internal/api/server_middleware.go` 的 `accessAuthMiddleware` 中，当客户端提供的 `Authorization` header 或 API key 校验失败时，直接调用 `c.AbortWithStatusJSON(401, ...)` 返回。
2. **无效路由与方法**：
   非模型接口、404 未知路径。
3. **请求解析前置失败**：
   入站 JSON 格式畸形（Malformed JSON），在 Handler 提取 payload 阶段失败并直接返回 400 Bad Request。

### 5.2 插件熔断、重载与不可用风险 (Observation Loss)

在 `internal/pluginhost/adapters_interceptors.go` 中，终态通知分发逻辑为：

```go
func (h *Host) CompleteRequestExcept(ctx context.Context, completion pluginapi.RequestCompletion, skipPluginID string) {
    if h == nil {
        return
    }
    ...
    for _, record := range h.activeRecords() {
        plugin := record.plugin.Capabilities.RequestLifecyclePlugin
        if h.isPluginFused(record.id) || plugin == nil || record.id == skipPluginID || !h.recordCurrent(record) {
            continue
        }
        ...
        go func(record capabilityRecord, plugin pluginapi.RequestLifecyclePlugin, completion pluginapi.RequestCompletion) {
            defer func() {
                if recovered := recover(); recovered != nil {
                    h.fusePlugin(record.id, "RequestLifecyclePlugin.HandleRequestComplete", recovered)
                }
            }()
            ...
        }(record, plugin, next)
    }
}
```

- **丢失观察的边界**：
  1. **熔断状态 (Fused)**：若插件曾发生 panic 被 host 熔断，后续所有 terminal 通知将被直接丢弃。
  2. **重载状态 (Reload)**：若插件处于重载过渡期（`!h.recordCurrent(record)`），旧实例不再接收通知，新实例就绪前存在通知真空期。
  3. **非持久化内存 Goroutine**：若 CPA 宿主进程崩溃，未消费的内存队列将完全丢失。

---

## 6. 版本差异：v7.3.3 vs v7.3.8 vs External

1. **v7.3.3 与 v7.3.8 的对比**：
   - 经源码对比，两版本在 `handlers_interceptors.go`、`handlers_stream.go`、`pluginhost/adapters_interceptors.go` 及相关单元测试中**完全一致**。
   - 两者均使用 Plugin ABI `SchemaVersion = 6`。
   - 两者在 RequestID 连续性、Attempt 缺失、Terminal Cardinality 和容错边界上的行为完全相同。
2. **External 外部实例**：
   - 外部运行时的插件状态、生命周期回调机制未经协商，无法仅凭 HTTP 管理接口推断，所有对应 capability 统一收敛为 **`unknown`**。
