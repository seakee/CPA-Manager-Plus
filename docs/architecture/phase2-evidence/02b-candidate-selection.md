# CPAMP v2 Phase2-02B — Candidate Visibility / Selection / Retry-Fallback Evidence

## 1. 概述与元数据

- **所属阶段**: CPAMP v2 Phase 2 Runtime Contract And Capability Matrix
- **任务代号**: Phase2-02B (C2-B 并行切片)
- **基线 Commit**: `v2@aca237cf7a99ce5d098dedcaec5cf8e147f05e4b`
- **被测 CPA 产物基线**:
  - 当前捆绑 (Embedded): CPA `v7.3.3` (`7bbfeaf8a7acf2cd5a834dcb0842539fe6aabc2b`)
  - 候选产物 (Candidate Release): CPA `v7.3.8` (`c93978c4ea2e908255a2a06c37599fda3651554a`)
  - 外部运行时 (External): `external-unnegotiated` (未协商，保持 unknown)
- **机器可读证据文件**: [`tests/fixtures/phase2-evidence/extensions/phase2-02b-candidate-selection.json`](../../../tests/fixtures/phase2-evidence/extensions/phase2-02b-candidate-selection.json)
- **自动化测试文件**: [`tests/phase2CandidateSelectionEvidence.test.mjs`](../../../tests/phase2CandidateSelectionEvidence.test.mjs)
- **核心定位**: 证明 CPA 真实的候选生成 (candidate generation) → 过滤 (filtering) → 优先级可见性 (priority visibility) → 调度选择 (scheduler selection) → 锁定认证 (selected/pinned auth) → 重试与回退 (retry/fallback) 能力边界。
- **边界声明**: **本切片纯属能力验证与证据沉淀，不实现 Hard Routing，亦不做最终 Hard Routing Go/No-Go 决定（留待 Phase2-04）。**

---

## 2. 核心命题与实证解答

### 2.1 Scheduler 前有哪些 Credential 会被过滤？

在 CPA 中，插件调度器（`PluginScheduler`）并不是直接从全局凭证库中挑选凭证，而是由 Conductor 预先执行过滤后将 `available` 切片作为 `Candidates` 传递给 Scheduler。

在 `sdk/cliproxy/auth/conductor_selection.go`（`pickNextWithProvider` 与 `pickNextMixedLegacy`）中，在调用 `availableAuthsForSelector` 之前与之中，严格执行以下前置过滤链：

1. **Disabled 过滤**:
   - `candidate == nil || candidate.Disabled`: 显式禁用的凭证直接在第一轮遍历中跳过。
   - `candidate.Status == StatusDisabled`: 在后续状态校验中视为不可用。
2. **Provider Mismatch 过滤**:
   - `providerKey := executorKeyFromAuth(candidate)`: 凭证所属的 Provider 必须属于当前请求指定的 Provider 集合（如单 Provider 路由或 Mixed 路由允许的 providers）。不匹配的直接剔除。
   - `_, ok := m.executors[providerKey]`: 若该 Provider 没有注册有效的 `ProviderExecutor`，直接剔除。
3. **Model Mismatch 过滤**:
   - `modelKey != "" && !m.authSupportsRouteModel(registryRef, candidate, model)`: 通过全局模型注册中心 `registryRef.ClientSupportsModel` 检查该凭证是否声明支持目标模型或其规范化映射别名。不支持目标模型的凭证直接剔除。
4. **Unauthorized / Policy Mismatch 过滤**:
   - `!eligibility.allows(candidate)`:
     - `requiredKind`: 若请求上下文指定了凭证类型（如 oauth / api_key），类型不符被剔除；
     - `credentialPolicy`: 若上下文携带凭证策略（如 `credentialPolicyAllows`），策略拒绝者被剔除；
     - `disallowFreeAuth`: 若请求元数据标记禁止免费凭证，Codex free 凭证被剔除。
5. **Cooldown / Unavailable 过滤**:
   - 在 `availableAuthsForRouteModel` / `availableAuthsForRouteModelAcrossPriorities` 中遍历剩余候选：
     - 调用 `isAuthBlockedForModel(candidate, checkModel, now)` 检查 `auth.Unavailable`、`auth.Quota.Exceeded`（含配额耗尽冷却）、以及 `auth.ModelStates` 中的模型级冷却。
     - 凡处于冷却期（`nextRetryAfter.After(now)`）或处于不可用状态的凭证均被过滤出可用集。
     - 若全部候选均处于冷却，则返回 `modelCooldownError`；若全部因 401 失败，则返回 `TerminalAuthError`。
6. **Round-Attempted / Tried 过滤**:
   - `_, used := tried[candidate.ID]`: 在同一请求执行轮次（round）中，前次尝试失败的凭证会被记录进 `tried` 集合并在后续候选生成中剔除，防止在同一轮中重复选择同一个失败凭证。
7. **Priority 过滤**:
   - 剩余可用凭证按 `authPriority(candidate)` 进行分桶（Priority Bucketing）。
   - 在未开启跨优先级调度时，仅最高 Priority 桶的候选被保留并交付 Scheduler。

**结论**: Scheduler 前严格过滤 Provider、Model、Disabled、Cooldown、Unauthorized、Tried 以及（默认模式下的）低 Priority。任何 Scheduler 无法看到已被上述规则排除的凭证。

---

### 2.2 v7.3.3 默认 Scheduler 实际看到什么 Candidate Set？

在 CPA `v7.3.3` (`7bbfeaf8a7acf2cd5a834dcb0842539fe6aabc2b`) 中：

- `conductor_selection.go:availableAuthsForSelector`:
  ```go
  if _, sessionAffinity := selector.(*SessionAffinitySelector); !sessionAffinity {
      priorityAuths, err = m.availableAuthsForRouteModel(auths, provider, routeModel, now)
      if err != nil {
          return nil, nil, err
      }
      priorityAuths = cloneAuthSlice(priorityAuths)
      return priorityAuths, priorityAuths, nil
  }
  ```
- 此时 CPA 尚未引入 `SchedulerAcrossPriorities`（该能力于 commit `b715526` 引入，晚于 v7.3.3）。
- 除非宿主 selector 本身为 `SessionAffinitySelector`，否则 `priorityAuths` 始终来自 `availableAuthsForRouteModel(..., allPriorities = false)`。
- `availableAuthsFromPriorityBuckets(availableByPriority, false)` 仅提取 `maxPriority` 对应的桶。

**证明结论**:
在 v7.3.3 中，默认传递给 `PluginScheduler` 的 `Candidates` 列表**仅包含最高可用 Priority tier 且通过了 Provider/Model/Disabled/Cooldown 过滤的凭证**。即使低优先级桶中有大量完全健康、启用的凭证，插件调度器也完全看不见它们。

---

### 2.3 v7.3.8 的 Candidate Visibility 差异

在 CPA `v7.3.8` (`c93978c4ea2e908255a2a06c37599fda3651554a`) 中：

- 引入了 `Capabilities.SchedulerAcrossPriorities` (Go) / `scheduler_across_priorities` (RPC)。
- Conductor 通过 `m.pluginSchedulerWantsAcrossPrioritiesLocked()` 动态探测插件是否声明了跨优先级可见性意图：

  ```go
  allAuths, errAcross := m.availableAuthsForRouteModelAcrossPriorities(auths, provider, routeModel, now)
  if errAcross != nil {
      return nil, nil, errAcross
  }
  allAuths = cloneAuthSlice(allAuths)

  if schedulerAcross {
      priorityAuths = allAuths
  } else {
      priorityAuths = highestPriorityAuths(allAuths)
  }
  ```

**差异对比矩阵**:

| 维度                  | v7.3.8 默认 (`SchedulerAcrossPriorities=false`)   | v7.3.8 Opt-In (`SchedulerAcrossPriorities=true`)   |
| --------------------- | ------------------------------------------------- | -------------------------------------------------- |
| 可见 Priority 范围    | 仅最高可用 Priority tier (`highestPriorityAuths`) | 跨越所有 Priority tier 的全部可用候选 (`allAuths`) |
| Disabled 凭证         | 严格排除（前置过滤）                              | 严格排除（前置过滤）                               |
| Cooldown 凭证         | 严格排除（前置过滤）                              | 严格排除（前置过滤）                               |
| Provider/Model 不匹配 | 严格排除（前置过滤）                              | 严格排除（前置过滤）                               |
| 敏感属性安全清洗      | 剔除 token/apikey/cookie/secret 等敏感 key        | 剔除 token/apikey/cookie/secret 等敏感 key         |
| 调度选择回退基础      | 若插件未处理，回退到 highestPriorityAuths         | 若插件未处理，回退到 highestPriorityAuths          |

**关键发现**:
`SchedulerAcrossPriorities=true` **仅仅扩大了 Scheduler 可以看到的健康候选集合的优先级维度**，它**绝不绕过** Provider、Model、Disabled 或 Cooldown 过滤。如果低优先级凭证处于 Cooldown 或 Model 不匹配状态，它依然不会出现在 candidates 中。

---

### 2.4 Scheduler 返回合法 Auth.ID 的行为

当 Scheduler 返回一个存在于传入 `Candidates` 列表中的合法 `Auth.ID` 时：

1. `internal/pluginhost/scheduler.go:normalizeSchedulerResponse`:
   - 验证 `resp.AuthID` 不为空；
   - 调用 `schedulerCandidateExists(req.Candidates, resp.AuthID)` 确认其存在于 `Candidates` 中；
   - 校验通过，返回 `(resp, true, "")`。
2. `sdk/cliproxy/auth/conductor_selection.go:pickViaPluginScheduler`:
   - `selected := pickSchedulerAuthByID(candidates, resp.AuthID)` 查找到对应的 `*Auth` 实例；
   - 返回 `(selected, true, nil)`。
3. `conductor_selection.go` 主流程：
   - 提取该凭证的 `providerKey := executorKeyFromAuth(selected)`；
   - 确认匹配的 `executor, ok := m.Executor(providerKey)`；
   - 执行 `authCopy := selected.Clone()`；
   - 确认/指派 `current.EnsureIndex()` 确保稳定 index；
   - 返回选中的凭证、执行器与 providerKey。
4. `conductor_execution.go`:
   - 调用 `publishSelectedAuthMetadata(opts.Metadata, auth)`；
   - 记录 `tried[auth.ID] = struct{}{}`；
   - 推进至上游执行。

---

### 2.5 Scheduler 返回不在 Supplied Candidate Set 中的 Auth.ID 时的行为

如果 Plugin Scheduler 返回了一个凭证 ID，但该 ID **并未包含在宿主传给它的 `Candidates` 列表中**（例如该凭证在 CPA 中不存在、被禁用、在冷却中、跨 Provider 不匹配、或者属于未放开的低优先级桶）：

CPA 的行为在两个层次上进行了确定性防护：

1. **PluginHost 校验层** (`internal/pluginhost/scheduler.go`):

   ```go
   if hasAuthID {
       if !schedulerCandidateExists(req.Candidates, resp.AuthID) {
           return pluginapi.SchedulerPickResponse{}, false, "unknown auth id"
       }
       return resp, true, ""
   }
   ```

   - 当 `resp.AuthID` 不在 `req.Candidates` 中时，`normalizeSchedulerResponse` 判定响应无效，记录警告日志：
     `pluginhost: scheduler returned invalid response: unknown auth id`
   - 返回 `(pluginapi.SchedulerPickResponse{}, false, nil)`，即 `handled = false`！

2. **Conductor 校验层** (`sdk/cliproxy/auth/conductor_selection.go`):

   ```go
   if selected := pickSchedulerAuthByID(candidates, resp.AuthID); selected != nil {
       return selected, true, nil
   }
   strategy, okStrategy := builtinSchedulerStrategy(resp.DelegateBuiltin)
   if !okStrategy {
       return nil, false, nil
   }
   return m.pickViaBuiltinScheduler(...)
   ```

   - 即使绕过 host 检查，`pickSchedulerAuthByID` 依然遍历 `candidates` 查找。找不到则返回 `nil`。
   - 若此时 `resp.DelegateBuiltin` 包含合法内置策略（如 `round_robin` 或 `fill_first`），则委托给内置策略；
   - 若未声明合法委托策略，则返回 `handled = false`。

3. **Conductor 最终回退**:
   - 当 `!handled` 时，代码走向回退逻辑：
     `selected, errPick = selector.Pick(selectorCtx, provider, selectionArgForSelector(selector, model), opts, selectorAuths)`
   - 宿主静默忽略非法 Auth.ID，**安全回退到宿主配置的内置 Selector（如 RoundRobinSelector）继续在合法候选集中选择**！

**明确回答**:

- **Reject？**: 是，非法 Auth.ID 会被直接丢弃并打印警告日志。
- **Delegate？**: 若响应中同时带有合法 `DelegateBuiltin`，则转由内置调度策略接管。
- **Fallback？**: 是，若无有效委托，则无缝回退（Fallback）至宿主默认 Selector。
- **Exact Error？**: **不会直接抛出异常中断请求**，宿主保证请求服务的高可用回退。只有当宿主候选集也彻底为空或全部冷却时，才在后续返回 `auth_not_found` 或 `model_cooldown`。

---

### 2.6 `pinned_auth_id` 的真实语义

在 CPA 源码中深度检索 `pinned_auth_id`（`cliproxyexecutor.PinnedAuthMetadataKey`），其实际处理逻辑如下：

1. **在哪里消费？**:
   - `conductor_selection.go:pickNextWithProvider` 与 `pickNextMixedLegacy`:
     ```go
     pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
     ...
     for _, candidate := range m.auths {
         ...
         if pinnedAuthID != "" && candidate.ID != pinnedAuthID {
             continue
         }
         ...
     }
     ```
   - `scheduler.go`:
     ```go
     predicate := scheduledAuthPredicate(eligibility, tried, pinnedAuthID, ...)
     ```
   - `conductor_selection.go:closestCooldownWaitWithAttempted` 与 `retryAllowed`:
     用于在重试轮次中计算冷却时间与是否允许重试。
   - `conductor_home.go`: Home 调度模式下的约束参数。
2. **是否绕过 Normal Candidate Generation？**:
   - **绝不绕过！** 源码表明，`pinnedAuthID` 只是作为循环中的一条过滤条件（`if candidate.ID != pinnedAuthID { continue }`）。
   - 目标凭证依然必须通过：
     - `candidate.Disabled` 检查；
     - `eligibility.allows(candidate)` 检查；
     - Provider 匹配检查；
     - Model 支持检查（`authSupportsRouteModel`）；
     - Cooldown 检查（`isAuthBlockedForModel`）；
     - Tried 未尝试检查。
3. **目标 Disabled / Cooldown / Missing / Provider Mismatch 时表现**:
   - **Target Missing**: 候选集为空，返回 `&Error{Code: "auth_not_found", Message: "no auth available"}`。
   - **Target Disabled**: 被 `candidate.Disabled` 剔除，候选集为空，返回 `auth_not_found`。
   - **Target Cooldown**: 候选集在冷却过滤中被判定全部冷却，返回 `newModelCooldownErrorWithCause`（`Code: "model_cooldown"`）。
   - **Provider Mismatch**: 被 `providerSet` 剔除，候选集为空，返回 `auth_not_found`。
   - **结论**: 表现为严格的 **Fail-Closed（故障闭锁）**，绝不会静默降级或改选其它凭证。
4. **Retry 时是否保持 Pin？**:
   - **保持 Pin**。`opts.Metadata` 中的 `pinned_auth_id` 在多次尝试与重试轮次间保持不变。
   - 在第一级重试（同轮次 `executeMixedOnce` 内）：因为该凭证已被置入 `tried[auth.ID]`，下一个循环中由于 `candidate.ID != pinnedAuthID` 且 `tried` 已命中该凭证，导致可用集立即变空，第一轮重试直接终止；
   - 在第二级重试（跨轮次冷却重试）：若错误属于可重试冷却，且 `retryAllowed` 判定允许重试，等待冷却结束后进入下一个 attempt 轮次，此时 `tried` 重新初始化，系统会**再次且仅尝试该 pinned 凭证**。

---

### 2.7 `selected_auth_id` 的产生、观察与重试可变性

1. **何时产生？**:
   - 在 Conductor 成功选出凭证（无论是通过 Plugin Scheduler 还是内置 Selector）后、发起上游请求前：
     ```go
     auth, executor, provider, errPick := m.pickNextMixed(...)
     ...
     publishSelectedAuthMetadata(opts.Metadata, auth)
     ```
   - 此时 `opts.Metadata[cliproxyexecutor.SelectedAuthMetadataKey] = auth.ID`，同时写入 `selected_auth_index`。
   - 若设置了 `selected_auth_callback`，则同步触发回调。
2. **哪些 Hook 能观察到？**:
   - **Before-Auth Request Interceptor**: **无法观察**。因为认证前拦截器在模型解析后、凭证选择前执行（Stage 3: `model_resolved`）。
   - **After-Auth Request Interceptor**: **可以观察**。执行器调用 `applyRequestAfterAuthInterceptor` 时携带更新后的 `opts.Metadata`。
   - **SelectedAuthCallback**: **同步观察**。凭证确定瞬间通过元数据回调函数即时触发。
   - **Terminal Request Lifecycle Hook**: **可以观察**。最终完成回调上下文可关联到请求元数据。
3. **Retry 时会不会变化？**:
   - **会变化**。如果前一次尝试失败，进入下一个重试迭代并选出了新凭证，`publishSelectedAuthMetadata` 会以新的 `auth` 再次被调用，覆盖 `opts.Metadata[SelectedAuthMetadataKey]`。因此它反映的是**当前尝试（attempt-specific）所绑定的实际凭证**。

---

### 2.8 上游失败后的 Retry、Credential Fallback 与 Provider Fallback

1. **重新执行 Candidate Filtering 与 Scheduler？**:
   - **是，完全重新执行！**
   - 查看 `conductor_execution.go:executeMixedOnce` 的重试循环：
     ```go
     for {
         ...
         auth, executor, provider, errPick := m.pickNextMixed(ctx, providers, routeModel, pickOpts, tried)
         ...
         tried[auth.ID] = struct{}{}
         ...
         resp, errExec := executor.Execute(...)
         if errExec != nil {
             ...
             lastErr = authErr
             continue // 循环重试！
         }
         return resp, nil
     }
     ```
   - 每次失败后，当前凭证被加入 `tried` 集合。
   - 下一次循环调用 `pickNextMixed` 时，再次执行候选者筛选，剔除 `tried` 中的凭证，重新计算 `availableAuthsForSelector`，并**重新调用 `pickViaPluginScheduler`**！
   - 调度器在每次重试中都会被调用，并看到被剔除了已失败凭证后的最新健康候选集。
2. **Credential Fallback**:
   - 在同一 Provider 下，若凭证 A 失败，Scheduler 在下一次选择中选择凭证 B，实现平滑故障转移。
3. **Provider Fallback**:
   - 在 `mixed` Provider 路由模式下，候选集包含所有合法 Provider 的凭证。
   - 若 Provider X 的凭证 A 失败，Scheduler 可在第 2 次尝试中选择 Provider Y 的凭证 C，实现跨 Provider 自动回退。

---

### 2.9 Stream 与 Non-Stream 的重试表现一致性

Stream 与 Non-Stream 在失败重试能力上存在**根本性分水岭**：

1. **Non-Stream (普通请求)**:
   - 完整响应在内存中缓冲，直至上游返回完毕。
   - 整个请求生命周期内发生的任何网络、状态码或协议错误均发生在向下游客户端交付前。
   - **完全支持**：多凭证重试、跨 Provider 回退、等待冷却后多轮重试。
2. **Stream (流式请求)**:
   - **阶段一：Bootstrap 阶段 (首个数据包到达前)**:
     - 源码：`conductor_stream.go:readStreamBootstrap(ctx, streamResult.Chunks)`。
     - 在首个 Payload Chunk 发送给下游客户端之前，若上游返回 503 Overload、429、520 或网络异常，CPA 会捕获该 `bootstrapErr`。
     - 此时下游客户端尚未接收任何 chunk，因此 Conductor **可以且会**记录失败凭证，并循环调用 `executeStreamMixedOnce` / `executeStreamWithModelPool` 换用下一个凭证（参见单元测试 `TestExecuteStream_BootstrapOverload_SkipsConsecutiveOverloadedCredentials`）。
   - **阶段二：In-Flight 阶段 (首个数据包已发送后)**:
     - 源码：`conductor_stream.go:wrapStreamResult` 中的异步 goroutine。
     - 首包一旦发出，HTTP 响应头与前序 SSE/Chunk 已流入下游客户端。
     - 若此时连接断开或上游吐出错误 Chunk，`emit` 函数只能将错误包装为 `StreamChunk{Err: err}` 发送给客户端并关闭 Channel。
     - **不可重试，亦无法 Fallback**。因为下游已经消费了部分数据，HTTP 协议无法重置流。

**总结结论**: Stream 与 Non-stream 在 Bootstrap 阶段语义一致；在首包之后的 In-flight 阶段表现出**不可避免的协议级分歧（Non-Retryable）**。

---

### 2.10 Plugin 的异常与边界行为

在 `internal/pluginhost/scheduler.go` 中：

1. **Unhandled (`resp.Handled = false`)**:
   - 插件主动放弃调度决策。
   - 宿主 `PickAuth` 返回 `handled = false`，Conductor 安全回退至内置 Selector（如 RoundRobin）。
2. **Delegate Builtin (`resp.DelegateBuiltin = "round_robin" | "fill_first"`)**:
   - 插件指示宿主采用指定的内置算法。
   - 宿主 Conductor 识别内置策略并调用 `m.pickViaBuiltinScheduler(...)` 执行相应分流。若策略名称非法，则回退到默认 selector。
3. **Error (插件显式返回 error)**:
   - `resp, errPick := scheduler.Pick(ctx, req)` 返回 `errPick != nil`。
   - 宿主记录 Warn 日志：`pluginhost: scheduler rejected auth pick`。
   - `PickAuth` 返回 `(resp, true, errPick)`。
   - Conductor 尊重插件的拒绝/报错意图，**中断本次尝试**，并将错误上报。
4. **Panic / Fused (插件 Panic 崩溃)**:
   - 源码中包含 `defer func() { if recovered := recover(); recovered != nil { h.fusePlugin(record.id, "Scheduler.Pick", recovered) ... } }()`。
   - 发生 Panic 时，宿主自动将该插件置于 **熔断状态 (Fused)**。
   - 本次请求吞掉 Panic，返回 `handled = false, err = nil`，无缝回退至内置 Selector。
   - 后续所有请求在 `schedulerRecord()` 阶段因检测到 `h.isPluginFused(record.id)` 直接跳过该插件，不再发生 RPC 或函数调用。
5. **Reload / Disabled (插件热重载或禁用)**:
   - 若插件被禁用或注销，`h.schedulerRecord()` 返回 `nil`。
   - `HasScheduler()` 返回 `false`。
   - Conductor 立即走无插件快速通道，纯由内置调度引擎接管。

---

### 2.11 External 未协商能力的分类

对于通过网络连接的独立 External CPA 实例：

- CPAMP 目前仅能通过管理 API 获取状态，尚无能力协商协议层证明其具体的 CPA 版本、是否加载了 Bridge Plugin、以及是否启用了 `SchedulerAcrossPriorities` 开关。
- 依据 Phase 2 证据基线规范，**不得将 Embedded 源码的推论跨环境推广至 External**。
### 2.12 v7.3.8 Release Black-box Observation <a id="v738-release-black-box-observation"></a>

基于官方正式发布的 CPA `v7.3.8` 二进制产物及 C-ABI 动态插件，在隔离的临时环境中执行了真实黑盒运行观测，完整覆盖了 Case A、Case B、Case C 与 Case D 四项核心验证。

#### 1. 被测工件与运行环境基线

- **官方发布版本**: `CLIProxyAPI Version: 7.3.8, Commit: c93978c4ea2e908255a2a06c37599fda3651554a`
- **官方发行包校验和**:
  - `CLIProxyAPI_7.3.8_linux_amd64.tar.gz`: `3fe5228c458624175d5e4e81d9dd003d82de688ca498fa368a790ea120bda0e3`
  - `CLIProxyAPI_7.3.8_linux_arm64.tar.gz`: `8d09ce286d857b2d0e6d77c39e08a755246120df58c02a115d58c391fc73e3f1`
  - `CLIProxyAPI_7.3.8_darwin_amd64.tar.gz`: `38099b7e0ad4792bfc449ccc0f5e9fd9feb9823e245c24691a3bca01d5a2b865`
- **执行环境**: 本地 Darwin x86_64 宿主直接拉起官方原生发布二进制 `cli-proxy-api`。
- **插件实现**: 遵循 CPA Plugin C-ABI（`ABI_VERSION = 1`），导出 `cliproxy_plugin_init`、`cliproxyPluginCall`、`cliproxyPluginFree`、`cliproxyPluginShutdown`，编译为共享库 `test-scheduler.dylib` 并通过配置文件 `plugins.configs.test-scheduler` 挂载。

#### 2. 凭据清单 (Synthetic Inventory)

测试环境中预置 4 份合成凭据文件于 `auth-dir`：

| 文件名 | Provider (`type`) | Priority | Disabled | 预期作用 |
| :--- | :--- | :--- | :--- | :--- |
| `auth-high.json` | `claude` | `10` | `false` | 最高优先级活跃凭据 |
| `auth-low.json` | `claude` | `5` | `false` | 低优先级健康凭据 |
| `auth-disabled.json` | `claude` | `10` | `true` | 高优先级但被显式禁用凭据 |
| `auth-codex.json` | `codex` | `10` | `false` | 高优先级但 Provider 不匹配凭据 |

#### 3. 观测过程与实测记录

##### Case A: 默认状态最高优先级可见性 (Highest Tier Default Visibility)
- **配置**: 插件能力声明 `capabilities: {"scheduler": true}`（即 `scheduler_across_priorities: false` 默认值）。
- **激励**: 向 `POST /v1/messages` 发送模型为 `claude-sonnet-4-5-20250929` 的请求。
- **实测观测数据**:
  ```json
  {
    "Model": "claude-sonnet-4-5-20250929",
    "Candidates": [
      {
        "ID": "auth-high.json",
        "Provider": "claude",
        "Priority": 10,
        "Status": "active"
      }
    ]
  }
  ```
- **结论**: 在未开启跨优先级时，虽然 `auth-low.json` 状态完全健康，但宿主在预处理阶段通过最高优先级分桶将其截断，插件**只能看见最高优先级凭据**。

##### Case B: 跨优先级候选可见性 (Across-Priorities Candidate Visibility)
- **配置**: 插件能力声明 `capabilities: {"scheduler": true, "scheduler_across_priorities": true}`。
- **激励**: 相同请求。
- **实测观测数据**:
  ```json
  {
    "Model": "claude-sonnet-4-5-20250929",
    "Candidates": [
      {
        "ID": "auth-high.json",
        "Provider": "claude",
        "Priority": 10,
        "Status": "active"
      },
      {
        "ID": "auth-low.json",
        "Provider": "claude",
        "Priority": 5,
        "Status": "active"
      }
    ]
  }
  ```
- **结论**: 显式声明 `scheduler_across_priorities: true` 后，多优先级候选（Priority 10 与 Priority 5）同时被完整传递给调度插件。

##### Case C: 调度前置资格过滤栅栏 (Pre-Scheduler Eligibility Fencing)
- **观测比对**:
  - 在 Case A 与 Case B 任意一轮的 `Candidates` 列表中，`auth-disabled.json`（`disabled: true`）均**彻底缺席**；
  - `auth-codex.json`（Provider 不匹配）同样**彻底缺席**。
- **结论**:
  本次 release black-box Case C 直接观测了：
  - Disabled filtering
  - Provider mismatch filtering

  Cooldown filtering 由 v7.3.8 upstream source + Go unit test 证明；
  Model mismatch 与 unauthorized / policy filtering 由 upstream source evidence 证明。
  证明无论是否开启 `scheduler_across_priorities`，Conductor 层的资格前置过滤均先于调度插件严格生效，插件调度器绝不可能触碰不可用凭据。

##### Case D: 选择有效性与非法候选优雅降级 (Selection Validity & Fallback)
- **Part 1 (合法跨优先级选择)**:
  - 在 Case B 中，插件决策选拔低优先级凭据 `auth-low.json`（返回 `Handled: true, AuthID: "auth-low.json"`）。
  - 宿主 debug log 明确显示实际执行凭证为 auth-low.json，例如：`Use OAuth provider=claude auth_file=auth-low.json for model ...`，证明合法低优先级 candidate 被 Scheduler 选择后确实进入执行路径。
- **Part 2 (非法/未知候选优雅降级)**:
  - 插件决策强行返回候选集中不存在的凭据 `invalid-ghost-auth.json`（`Handled: true, AuthID: "invalid-ghost-auth.json"`）。
  - 宿主运行日志实测捕获：
    ```text
    [warn] [scheduler.go:27] pluginhost: scheduler returned invalid response: unknown auth id plugin_id=test-scheduler
    [debug] [conductor_execution.go:1827] Use OAuth provider=claude auth_file=auth-high.json for model claude-sonnet-4-5-20250929
    ```
  - 结论：宿主 `normalizeSchedulerResponse` 识别未知凭据，重置 `Handled = false`，Conductor 立即降级回退至内置原生 selector，自动选用最高优先级活跃凭据 `auth-high.json` 继续服务。整个过程中宿主**未发生任何 panic、崩溃或请求挂起**。

---

## 3. 为什么“Scheduler 可见更多”不等于“可安全 Hard Route”？

在技术评审中，必须对以下陷阱保持极度警惕，**绝不能把 `SchedulerAcrossPriorities=true` 简单等价于“CPAMP 可以安全 Hard Route 到任意凭证”**：

```text
┌───────────────────────────────────────────────────────────────────────────────┐
│                           CPA Host Candidate Gate                             │
│                                                                               │
│   All Registered Auths ──> [ Provider Filter ] ──> [ Model Support Filter ]   │
│                                                            │                  │
│   [ Priority Buckets ] <── [ Cooldown Filter ] <── [ Disabled Filter ]        │
│           │                                                                   │
│           ▼ (SchedulerAcrossPriorities toggles tier flattening only)          │
│   Visible Candidates to Plugin Scheduler                                     │
└──────────────────────────────────────┬────────────────────────────────────────┘
                                       │
                                       ▼
                       Plugin Scheduler Choice: Target Auth
                                       │
                                       ▼
                     [ Normalize Check: Target in Candidates? ]
                                       │
                         ┌─────────────┴─────────────┐
                         │ YES                       │ NO
                         ▼                           ▼
                 Execute Target Auth         SILENT FALLBACK
                                         (to Host Default Selector!)
```

### 必须通过的 8 项鲁棒性检验：

1. **Target Missing (目标凭证不存在)**:
   - 若 Hard Route 指定了一个不存在的凭证，该凭证不在 candidates 中；
   - Scheduler 返回它会被 `normalizeSchedulerResponse` 判定为 `unknown auth id`，宿主**静默回退**到 RoundRobin 选择其他凭证；
   - 这与 Hard Route 期望的“严格定向（Fail-Closed）”直接冲突。
2. **Target Disabled (目标凭证被禁用)**:
   - 目标凭证在第一步就被过滤掉，根本不会出现在 candidates 中。
   - Scheduler 强行返回该凭证同样被静默丢弃并回退，导致流量走到了非预期凭证。
3. **Target Cooldown (目标凭证在冷却中)**:
   - 目标凭证若处于 429 或配额耗尽冷却，同样被前置过滤。
   - 强行指定只会导致回退至其他可用凭证，违背路由隔离。
4. **Provider / Model Mismatch (模型不支持)**:
   - 目标凭证未绑定该模型，前置过滤将其剔除。强行指定同样导致回退。
5. **Retry 漂移**:
   - 上游单次超时或报错后，若重试机制被触发，重试循环会排除已失败凭证，重新调用调度器选择下一个凭证；
   - 这意味着如果不做强制锁定，一次瞬时失败就会使请求漂移出 Hard Route 范围。
6. **Fallback 泄露**:
   - CPA 设计初衷是高可用代理，倾向于“只要还有活着的凭证就尽力提供服务（Fail-Open 回退）”；
   - 而 Hard Routing 的安全要求通常是“宁可报错不可打乱租户/凭证隔离（Fail-Closed）”。
7. **Reload / Fused 降级**:
   - 一旦插件因意外 Panic 被 Fused，调度完全回退到原生 RoundRobin，Hard Route 彻底失效。
8. **Pinning 真实语义限制**:
   - CPA 原生唯一真正的强锁定是 `pinned_auth_id`；
   - 但 `pinned_auth_id` 目前只对已知凭证生效，且目标异常时直接 fail-closed，它并不是由插件调度器在选凭证时自由指定的，而是作为前置 metadata 传入 Conductor 的。

**架构结论**:
单纯依赖“在 Plugin Scheduler 中挑选一个 ID”**根本不足以**安全交付生产级 Hard Routing。必须由 Conductor 层具备可协商的 Pre-filter 策略控制或受信的 `pinned_auth_id` 上下文注入，才能建立真正的安全路由。

---

## 4. 结论与证据矩阵汇总

| 验证项                    | CPA v7.3.3 (Embedded)                                                 | CPA v7.3.8 (Candidate Release)                                | External (Unnegotiated) | 证据来源                           | 架构定论                        |
| ------------------------- | --------------------------------------------------------------------- | ------------------------------------------------------------- | ----------------------- | ---------------------------------- | ------------------------------- |
| **候选可见性 (Default)**  | 仅最高可用 Priority tier (`partial`)                                  | 仅最高可用 Priority tier (`partial`)                          | 未知 (`unknown`)        | `conductor_selection.go` / v7.3.8 Release Black-box Case A | 默认均不具备全局跨优先级视野    |
| **跨优先级调度 (Opt-In)** | 不支持 (`unsupported`)                                                | 支持跨优先级但受前置过滤限制 (`supported`)                    | 未知 (`unknown`)        | `scheduler_test.go` / v7.3.8 Release Black-box Case B    | 仅 v7.3.8 具备该开关            |
| **前置过滤 (Pre-Filter)** | 支持 Provider/Model/Disabled/Cooldown/Unauthorized 过滤 (`supported`) | 支持完整前置安全过滤 (`supported`)                            | 未知 (`unknown`)        | `conductor_selection.go` / v7.3.8 Release Black-box Case C (Disabled + Provider Mismatch) | 过滤发生在调度器之前            |
| **合法候选选择**          | 正常接管与执行 (`supported`)                                          | 正常接管与执行 (`supported`)                                  | 未知 (`unknown`)        | `internal/pluginhost/scheduler.go` / v7.3.8 Release Black-box Case D1   | 合法 ID 均被正常处理            |
| **非法候选回退**          | 静默忽略并回退内置 Selector (`supported`)                             | 规范化校验，忽略并回退 (`supported`)                          | 未知 (`unknown`)        | `internal/pluginhost/scheduler.go` / `scheduler_test.go` / Case D2 Fallback | 不会崩溃，保证高可用回退        |
| **Plugin 委托与未处理**   | 支持委托和 unhandled 回退 (`supported`)                               | 支持委托和 unhandled 回退 (`supported`)                       | 未知 (`unknown`)        | `internal/pluginhost/scheduler.go` / `scheduler_test.go`      | 健壮性保障                      |
| **Plugin Panic 熔断**     | 支持熔断回退 (`supported`)                                            | 支持熔断回退 (`supported`)                                    | 未知 (`unknown`)        | `scheduler_test.go`                | 单插件异常不影响整体可用性      |
| **pinned_auth_id 语义**   | 严格过滤，不绕过健康检查，重试保持，Fail-Closed (`supported`)         | 严格过滤，不绕过健康检查，重试保持，Fail-Closed (`supported`) | 未知 (`unknown`)        | `conductor_execution.go` / `conductor_selection.go`           | 是 CPA 原生最严密的安全锁定语义 |
| **selected_auth_id 观察** | 认证后元数据可见，重试可变 (`supported`)                              | 认证后元数据可见，重试可变 (`supported`)                      | 未知 (`unknown`)        | `conductor_execution.go#publishSelectedAuthMetadata`          | 不对认证前拦截器暴露            |
| **重试与故障转移**        | 重试重新执行候选筛选与 Scheduler 调度 (`supported`)                   | 重试重新执行候选筛选与 Scheduler 调度 (`supported`)           | 未知 (`unknown`)        | `conductor_execution.go`           | 支持凭证与 Provider 级重试回退  |
| **Stream 重试分歧**       | Bootstrap 阶段可重试，In-flight 阶段不可重试 (`partial`)              | Bootstrap 阶段可重试，In-flight 阶段不可重试 (`partial`)      | 未知 (`unknown`)        | `conductor_stream.go`              | 流式首包发出后不可回退          |
| **Non-Stream 重试**       | 完整支持 (`supported`)                                                | 完整支持 (`supported`)                                        | 未知 (`unknown`)        | `conductor_execution.go`           | 普通请求可安全多级重试          |

---

## 5. 后续规划接口

1. **Phase2-04 汇聚**:
   - 本文沉淀的 12 项能力验证与 25 条 Capability Records 将由 Phase2-04 统一整合进入完整 Capability Matrix。
   - 本文证明的“调度器回退陷阱”与“Fail-Open 默认行为”将成为 Hard Routing Go/No-Go 决策的关键输入。
2. **产品基线保持**:
   - CPAMP 当前运行时镜像与 `Dockerfile.runtime` 继续锁定官方 CPA `v7.3.3`。
   - 本文对 `v7.3.8` 的验证成果仅作为黑盒证据扩展，未在此处进行隐式产品升级。
