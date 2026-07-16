# 2026-07-10 性能优化报告

本文记录自 2026-07-10 开始、并在 2026-07-15 完成的 Manager Server、Dashboard、请求监控、Usage Analytics 和 Model Prices 性能优化，包括问题原因、实现方式、测试口径和实测结果。报告于 2026-07-16 补充后续阶段及当前 `main` 的复测数据。

## 执行摘要

本轮优化分为十个阶段：

1. PR #319：限制无界内存、请求和 SQLite 连接资源。
2. PR #320：Dashboard 核心统计改用增量小时汇总。
3. PR #323：Usage Analytics 按当前 Tab 请求最小数据集。
4. Usage Analytics Hourly Rollup Phase 2B：严格无筛选的长窗口核心统计复用小时汇总。
5. Usage Analytics Compact Summary：跳过当前 Tab 不需要的 Summary 子查询。
6. Credential Timeline SQL 预聚合与按需加载：只为选中凭据读取时间线。
7. Usage Analytics Hourly Rollup Projected Read：按调用方需要裁剪小时汇总字段。
8. Latency Percentile Compact Read：使用整数投影读取延迟和 TTFT 样本。
9. Dashboard Refresh Bounded Concurrency：并行执行独立 Dashboard 读取。
10. Monitoring 完整请求复用重复维度统计，并以有界并发执行独立 SQLite 读取。

在 100,000 条测试事件下，以打开 Usage Analytics Overview 的有效工作量为口径：

| 指标 | 优化前 legacy 路径 | 当前路径 | 综合变化 |
|---|---:|---:|---:|
| 请求耗时 | 约 7.00s | 约 1.12s | 降低约 84% |
| 单次内存分配 | 约 215MB | 约 5.23MB | 降低约 97.6% |

legacy 路径会请求全部 Tab 和完整筛选数据；当前路径只执行 Overview 实际需要的主查询和 selector 请求。当前 `main` 三次复测为约 1.119～1.126s、5.23MB/op。因此该结果代表用户打开 Overview 时的有效工作量变化，而不是两组完全相同 SQL 的对比。

## 测试口径说明

- `ns/op` 或耗时：一次 benchmark 操作的执行时间。
- `B/op`：一次操作期间累计分配的字节数，不等于进程最终 RSS。
- `allocs/op`：一次操作产生的分配次数。
- pprof `inuse_space`：GC 后仍然存活的堆对象，更适合判断是否存在 retained heap 或事件切片泄漏。
- 所有核心 benchmark 使用 100,000 条合成 usage events，覆盖 12 个模型、多个账号/API Key、成功/失败、Token 和延迟数据。

## 阶段一：内存压力治理

### 问题原因

程序从较低初始 RSS 增长到数百 MB，未定位到单一经典内存泄漏点。主要问题是多个线性或无界资源路径叠加：

- Usage 导入、导出一次性构建完整数据集。
- Monitoring 长时间保留持续增长的事件数组。
- 多份展示快照可能间接保留事件数据。
- SQLite 最大连接数不受限，每个连接可能持有独立缓存。
- Model Prices 下载完整 usage payload，只为统计模型调用次数。
- 重复或已过期的 Analytics 请求继续执行。
- 延迟百分位计算在 Go 堆中保留较大的样本窗口。

### 优化内容

| 优化项 | 优化前 | 优化后 |
|---|---|---|
| Usage 导出 | 完整结果构建后返回 | 从固定数据库快照逐行写入 |
| Usage 导入 | 完整 payload 解析后写入 | 每 256 条提交一批 |
| SQLite 连接 | 最大连接数不受限 | 最多 4 个 open、2 个 idle，5 分钟 idle lifetime |
| Monitoring 事件 | 可持续增长 | 最多保留 2,000 条 |
| Monitoring 分页 | 宽结果持续累积 | 每页 500 条 |
| 展示快照 | 多份派生状态可能保留事件 | 最多 4 份，快照不保存事件行 |
| 自动刷新 | 页面不可见时仍可能继续 | 默认 30 秒，页面隐藏时暂停 |
| Analytics 请求 | 重复或旧请求可能继续 | Abort、节流和 in-flight 去重 |
| Model Prices | 最多下载 50,000 条完整事件 | 返回按模型聚合的轻量统计 |
| P95 Summary | Go 堆保留完整样本 | SQLite window query 计算 |

### 为什么会降低内存

导入工作集由 `O(总事件数)` 变为 `O(256)`；前端事件、展示快照和 SQLite 连接都有明确上限。大请求结束后，不再有完整 JSON、全部事件数组或多连接缓存持续放大 RSS。

在当前 100,000 条测试数据包含 12 个模型的情况下，Model Prices 的结果规模从最多 50,000 条完整事件降为约 12 条模型统计行，返回行数约缩小 4,167 倍。该阶段没有保存统一的端到端前后耗时 benchmark，因此不对整体 RSS 给出不准确的固定下降数字。

## 阶段二：Dashboard Hourly Rollup

### 问题原因

Dashboard 每次刷新会针对同一个时间窗口重复扫描 `usage_events`：

```text
aggregate
+ model stats
+ top models
+ hourly timeline
```

查询成本近似为 `查询数量 × 原始事件数量`，数据增长后 CPU、SQLite I/O 和页面延迟同步增长。

### 优化内容

新增 UTC 小时汇总，按以下维度保存稳定统计：

```text
hour + model + billing model + service tier
```

汇总字段包括调用数、成功/失败、各类 Token、延迟 sum/count 和 zero-token calls。读取策略为：

```text
raw leading edge
+ complete hourly rollups
+ raw trailing edge
```

价格不会写入 rollup，而是在读取时使用当前 Model Prices 重新计算。checkpoint 未追平、rollup 关闭或读取异常时，查询自动回退 raw events。

### 100k 测试结果

| 路径 | 耗时 | 变化 |
|---|---:|---:|
| Raw events | 约 774ms | 基线 |
| Hourly rollup | 约 2.66ms | 约快 291 倍 |
| 延迟降幅 |  | 约 99.7% |

连续 20 次 rollup benchmark 稳定在约 2.66ms/op、556KB/op。heap profile 结束时 in-use 约 2.9MB，未发现 100,000 条事件切片被长期保留。

## 阶段三：Usage Analytics 按 Tab 裁剪

### 问题原因

优化前，无论用户打开 Overview、Trends、Models、API Keys、Credentials 还是 Heatmap，前端都会请求几乎完整的 Analytics 数据集和筛选选项。隐藏 Tab 的 SQL 仍会执行。

### 优化内容

- 每个 Tab 只发送实际需要的 include 矩阵。
- Filter selectors 从主 Analytics 请求中拆分。
- Selector 只读取 model、API key、provider 和 auth file 的 distinct 值。
- Tab 切换不重复加载稳定 selectors。
- Selector 失败不阻塞主内容。
- 同时发送兼容标志，旧 Manager Server 仍可返回完整 filter options。

### 100k 测试结果

| 请求类型 | 耗时 | 单次分配 |
|---|---:|---:|
| Legacy full | 约 7.00s | 约 215MB |
| Overview initial | 约 3.63s | 约 34MB |
| 专项 Tab | 约 2.34～3.10s | 未单独记录 |
| Filter selectors | 约 402ms | 约 25KB |

Overview 耗时降低约 48%，分配降低约 84%。专项 Tab 耗时降低约 56%～67%。

## 阶段四：Usage Analytics Hourly Rollup Phase 2B

### 问题原因

按 Tab 裁剪后，Overview 等页面仍需要对 raw events 执行当前周期、上一周期、model stats 和 timeline 扫描。这些核心查询仍随历史事件总量增长。

### 优化内容

新增 Dashboard 和 Monitoring 共用的 hourly reader，统一处理：

- checkpoint 和 latest event 完整性检查。
- 完整 UTC 小时读取。
- 首尾 raw edge 补偿。
- Aggregate、model stats 和 timeline 合并。
- 限频 fallback 诊断。

只有严格无筛选请求使用 rollup：

- 无 search query 或 API key search。
- 无 model、provider、account、auth file、API key、project、source 或 header filters。
- 包含成功与失败事件。
- 无 latency/cache status 条件。

以下统计使用 rollup：

- 当前和上一周期 aggregate。
- Model stats 和按当前价格计算的 cost。
- 可无损表达的 hour/day timeline。
- Average latency。

以下统计继续读取 raw events：

- P95 latency 和 P95 TTFT。
- Rolling 30m RPM/TPM。
- Task buckets、active days 和 zero-token models。
- API Key、Credential、Channel、Account 和 Heatmap 等高维统计。
- 所有搜索或筛选请求。

### 时区正确性

Raw analytics 与 rollup reader 共用同一个时区 bucket 规则。每个 UTC 小时会检查区间首尾是否映射到同一目标 bucket：

- UTC、整小时时区和通常的 DST 边界可使用 rollup。
- 半小时或 45 分钟时区无法无损表达时，timeline 自动回退 raw。
- Timeline 回退不会阻止 Summary 和 Model Stats 使用安全的 rollup 数据。

测试覆盖 UTC、Asia/Shanghai、Asia/Kolkata、America/New_York DST spring/fall、partial hour、price change、checkpoint pending、disabled 和空模型语义。

### 100k Overview 三次测试

| 路径 | 平均耗时 | B/op | allocs/op |
|---|---:|---:|---:|
| Raw | 约 3.21s | 34.43MB | 约 269 万 |
| Rollup | 约 2.48s | 20.24MB | 约 97 万 |
| 变化 | 降低约 23% | 降低约 41% | 降低约 64% |

### 100k 核心路径三次测试

该口径只包含 Phase 2B 实际负责的 aggregate、model stats 和 timeline：

| 路径 | 平均耗时 | B/op | allocs/op |
|---|---:|---:|---:|
| Raw | 约 777ms | 23.70MB | 约 186 万 |
| Rollup | 约 39ms | 9.51MB | 约 14.2 万 |
| 变化 | 约快 20 倍 | 降低约 60% | 降低约 92% |

核心路径约快 20 倍，但 Overview 整体只快约 23%，是因为 P95、TTFT、task、active days、API Key 和 Channel 等保留的 raw 查询已经成为主要耗时来源。

## 阶段五：Usage Analytics Compact Summary

### 问题原因

Usage Analytics 各 Tab 虽然已经按需裁剪主数据集，但原有 Summary 合同仍会执行 rolling 30m、task buckets、active days、zero-token models 和 percentile 等完整子查询。多数 Tab 只显示调用数、Token、成本和平均延迟，仍为不可见指标支付查询成本。

### 优化内容

- 新增向后兼容的 `compact` Summary profile；未显式请求时继续保持完整合同。
- 所有 Usage Analytics Tab 使用 compact profile。
- 只有 Overview 请求 P95 latency 和 P95 TTFT；其他 Tab 跳过 percentile 查询。
- Request Monitoring 等既有消费者继续使用完整 Summary，不改变响应语义。

### 100k 当前 main 三次复测

| 路径 | 平均耗时 | B/op | allocs/op |
|---|---:|---:|---:|
| Full Summary | 约 1.66s | 约 3.77MB | 约 31.0 万 |
| Compact Summary + percentiles | 约 775ms | 约 31KB | 约 506 |
| Compact Summary，无 percentiles | 约 21.5ms | 约 22.8KB | 399 |

保留 percentile 时耗时降低约 53%，分配降低约 99%；不需要 percentile 的 Tab，Summary 耗时降低约 98.7%。

## 阶段六：Credential Timeline SQL 预聚合与按需加载

### 问题原因

Credentials Tab 原本会读取范围内的凭据事件并在 Go 中逐条构建所有凭据的时间线，即使用户最终只查看其中一个凭据。事件数量和凭据数量增长后，隐藏时间线成为额外的扫描、传输和分配成本。

### 优化内容

- 把 Credential Timeline 的小时聚合下推到 SQLite，并保留首尾 partial raw edge。
- 共用 UTC 小时可表达性检查；半小时、45 分钟时区和无法无损表达的范围安全回退 raw。
- 新增精确 `credential_ids` 筛选，兼容 auth file、auth index、source hash 和 source-only identity。
- 前端先加载凭据排行，只有用户选中凭据后才发起对应 Timeline 请求。
- 保留取消、stale response 防护、加载状态和错误反馈。

### 100k 当前 main 三次复测

| 请求 | 平均耗时 | B/op | allocs/op |
|---|---:|---:|---:|
| Credentials 排行与 Summary | 约 524ms | 约 847KB | 约 1.19 万 |
| 单个选中凭据 Timeline | 约 50.0ms | 约 340KB | 约 3,275 |
| 两阶段合计 | 约 565ms | 约 1.19MB | 约 1.52 万 |

首次进入 Credentials 时只执行第一行；第二行仅在存在选中凭据且需要时间线时执行，避免为未查看的凭据构建 Timeline。

## 阶段七：Hourly Rollup Projected Read

### 问题原因

Phase 2B 已经用小时汇总替代大量 raw scan，但 reader 仍会加载完整的 `hour + model + billing model + service tier` 行并构建完整 snapshot。只需要 model 聚合或 UTC day timeline 的调用方仍承担无关维度和中间对象的分配。

### 优化内容

- 增加 model-only 和 UTC daily projection。
- 按 Summary、Model Stats 和 Timeline 的实际组合选择紧凑 snapshot。
- 保留 partial raw edge、价格重算、checkpoint 检查和时区 fallback。
- 不修改 schema 和 API。

### 100k 核心路径当前 main 三次复测

| 路径 | 平均耗时 | B/op | allocs/op |
|---|---:|---:|---:|
| Raw | 约 830ms | 23.78MB | 约 186.5 万 |
| Projected rollup | 约 24.0ms | 611KB | 7,900 |
| 相对 Raw | 约快 34.6 倍 | 降低约 97.4% | 降低约 99.6% |

与 Phase 2B 当时的 rollup 结果（约 39ms、9.51MB/op）相比，投影读取又把耗时降低约 38%，分配降低约 94%。

## 阶段八：Latency Percentile Compact Read

### 问题原因

Latency 和 TTFT percentile 查询经 `database/sql` 读取可空数值时，会产生 integer → string → float 的逐行转换和临时对象。Trends、Models 和 Overview 的 percentile 路径因此保留了不必要的分配。

### 优化内容与结果

- SQLite 只投影有效的整数 latency/TTFT 样本。
- Go 端直接读取紧凑整数并保持 nearest-rank 语义。
- 覆盖 DST、非整小时时区、筛选、NULL 样本和 10k/100k 数据。
- Trends 和 Models 的分配从约 7.20MB/op 降至约 4.80MB/op，降低约 33%，响应和筛选语义不变。

当前 `main` 三次复测中，Trends 约 532～544ms、4.81MB/op，Models 约 543～560ms、4.80MB/op，与优化记录的紧凑分配水平一致。

## 阶段九：Dashboard Refresh 有界并发

### 问题原因

小时汇总已大幅降低 Dashboard 核心统计成本，但 rolling 30m、health timeline、recent failures、channel stats 和 failure sources 等独立读取仍串行执行。数据量增大后，这些 recent/raw 查询成为刷新关键路径。

### 优化内容与结果

- 在现有四连接 SQLite 连接池内并行执行独立 Dashboard 查询。
- 保留 context cancellation 和 first-error propagation。
- 不增加常驻 recent-event cache，不改变响应合同。
- 合入时的 10k、100k、1m benchmark 分别降低完整刷新延迟约 37%、44% 和 50%。

当前 `main` 三次单次复测结果：

| 数据量 | 完整刷新耗时 | B/op | allocs/op |
|---|---:|---:|---:|
| 10k | 约 39.7～43.2ms | 约 1.63MB | 约 2.05 万 |
| 100k | 约 403～405ms | 约 1.64MB | 约 2.18 万 |

分配量随事件规模基本稳定，耗时仍主要来自必须扫描 recent/raw 范围的查询。

## 阶段十：Monitoring 完整请求去重与有界并发

### 问题原因

Request Monitoring 会在一次 analytics 请求中同时加载 Summary、Timeline、小时分布、Model/Channel/Account/API Key 统计、失败来源、task buckets、filter options 和事件分页。这些独立读取原本串行执行，且无筛选请求的 `filter_options` 会再次计算主响应已经请求的 account、API key、channel 和 model 统计。

在 100k、30 天 UTC 的 Monitoring include profile 中：

- 完整请求约 5.44s。
- 移除 `filter_options` 后约 3.38s。
- 单独 `filter_options` 约 2.09s。

### 优化内容

- 主 filter 与 filter-options 基准 filter 等价时，复用已加载的 model、channel、account 和 API key 原始统计。
- Filter options 直接复用主响应构建后的行，保证数值与 tie ordering 完全一致。
- Timeline percentile、小时分布、高维统计、filter options、recent failures 和 events page 使用可取消的查询组预取。
- 后台预取最多并发 2 个读取；加上前台 Summary/task 路径，单请求最多占用 3 个 SQLite 连接，为四连接池中的其他工作保留 1 个连接。
- 第一个查询错误会取消同组任务；响应仅在全部查询完成后单线程组装。
- 不新增 schema、rollup 表、常驻 cache 或 API 字段。对于 filter-options 基准范围会清除的维度或状态筛选，选项继续独立计算并保持原有语义；仅搜索请求仍可复用，因为基准范围会保留搜索条件。

### 100k Monitoring include-profile 测试

| 路径 | 耗时 | B/op | allocs/op |
|---|---:|---:|---:|
| 优化前 | 约 5.44s | 16.26MB | 约 98.4 万 |
| 优化后 | 约 1.69～1.81s | 15.14MB | 约 95.8 万 |
| 变化 | 降低约 67%～69% | 降低约 7% | 降低约 3% |

20 次连续测试保持在约 1.69s/op、15.14MB/op，未新增跨请求持有的缓存或事件数组。

另一次优化后 benchmark 使用 curl 范围（`from_ms=1`、`Asia/Shanghai` 和相同 include payload），结果约为 1.815s/op、26.02MB/op、109.3 万 allocs/op。更宽的时间范围会增加分配量，但在该数据集上没有显著增加耗时。

### 58,686 条真实数据验证

在 `bin/tmp/db/data` 的 disposable SQLite backup 上，完成 cache-accounting migration 和 dashboard rollup 追平后：

- Monitoring 第一轮查询整形后的完整请求基线约 5.80s。
- 本阶段 service 耗时稳定在 2.21～2.38s。
- 相对上一阶段再降低约 59%～62%。
- 7.58MB JSON 响应的序列化仅约 16ms，剩余耗时仍主要来自 SQLite 查询。
- 原始 `bin/tmp/db/data` 未被修改。

## 内存与稳定性验证

- 10 次和 200 次连续 100k rollup benchmark 保持稳定。
- 200 次测试约 38～40ms/op。
- 最终 heap profile in-use 约 5MB。
- in-use top 中没有 CPAMP hourly reader 或完整事件切片。
- 未观察到随请求次数持续增长的 retained heap。

`B/op` 表示请求期间累计分配，不代表这些内存会持续保留。pprof in-use 结果更接近是否存在泄漏的判断依据。

## 整体性能为什么提升

优化前的主要成本近似为：

```text
全部 Tab 数据 × 多组查询 × 全部原始事件
```

优化后变为：

```text
当前 Tab 所需数据
×
完整小时汇总行
+
首尾少量 raw events
+
无法汇总的专项指标
```

复杂度变化可以概括为：

- `O(全部事件)` 内存保留变为 `O(固定批次/固定上限)`。
- 全 Tab 查询变为当前 Tab 查询。
- 重复 raw scan 变为小时 rollup。
- 完整事件响应变为聚合响应。
- 无界连接和缓存变为明确资源预算。
- stale 请求继续执行变为 cancel、throttle 和 dedupe。

## 验证结果

最终通过：

- Manager Server 全量测试。
- Go race 全量测试。
- `go vet ./...`。
- 86 个 Vitest 文件、719 个测试。
- VitePress 文档构建。
- 多时区、DST、fallback 和价格变更测试。
- 多轮代码审查，最终无阻断发现。

## 运行与回滚

小时汇总默认开启。临时关闭时设置：

```bash
USAGE_DASHBOARD_HOURLY_ROLLUP_ENABLED=false
```

修改后重启 Manager Server。Dashboard 和 Usage Analytics 会回退 raw events。除 Manager Server 指南说明的启动时一次性格式升级外，关闭该运行时开关本身不会删除当前格式的 rollup 数据。该开关不接入 UI。

更多配置见 [Manager Server 指南](./manager-server.md)。

## 后续方向

当前 Compact Summary、credential timeline SQL 预聚合与按需加载、hourly rollup 投影读取、latency percentile 紧凑读取、Dashboard/Monitoring 有界并发和高维请求裁剪均已完成。剩余耗时主要依赖必须保留 raw 语义的查询：

- P95 latency 和 P95 TTFT。
- Task buckets。
- Active days。
- Zero-token models。
- Overview 的 API Key 和 Channel 高维聚合。

继续优化前应先在更大真实数据上重新 profile。若 filter option distinct 或某个高维聚合稳定占据主导，再选择单一查询整形或专用维度 rollup；现有证据仍不支持引入 bounded recent event cache。
