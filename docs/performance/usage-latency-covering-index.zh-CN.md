# 用量分析延迟覆盖索引验证报告

2026-10-13。结论：采用 `(timestamp_ms, latency_ms, ttft_ms)` 覆盖索引，复用已有延迟索引与离线维护机制。生产代码仅增加一条索引登记；旧时间索引保留。320 万事件的 30 天扫描中位数由 3774.837 ms 降至 425.175 ms，120 天精确 P95 汇总由 49313.379 ms 降至 24914.147 ms。后者仍有约 25 秒的排序成本，不能宣称已消除长窗口卡顿。

## A. 基线与实验边界

- 仓库：`seakee/CPA-Manager-Plus`；分支：`perf/usage-latency-covering-index`。
- 基线：`330fa77bf409e07b0e4f67c7afbb36790aaefdb4`，v1.14.6 发布 PR #954 合入 `dev`。交付前实时核对 GitHub；保留所选基线。
- macOS 13.6.3 / Darwin 22G436；Intel i7-9700K @ 3.60 GHz，8 CPUs，32 GiB RAM；Go 1.26.1 darwin/amd64；SQLite 3.51.2，modernc.org/sqlite v1.46.1。CI 使用 Go 1.24.x。
- 全部为任务独占合成库，120 天均匀分布，时间戳与插入顺序打散，12 模型、3 Provider、100 账号、10 API Key，包含失败、NULL、0 延迟。raw_json 使用 850 字节合成 padding；未接触生产数据库。原始 `timestamp` 文本固定，目标分析使用 `timestamp_ms`。
- 初始空闲约 218 GiB，完成大库生成后约 209 GiB；没有为达到目标文件大小复制生产数据。
- 单连接 128 MiB cache 仅用于生成数据；测量前恢复 SQLite 默认 `cache_size=-2000` 和 CPAMP 4 连接池。各性能实验串行运行，避免相互争用；预热结果包含操作系统文件缓存影响，不是冷启动基准。

| 事件数 | 原库 bytes | 新索引 bytes | 占原库 | 首次 CREATE INDEX ms |
| --- | --- | --- | --- | --- |
| 100000 | 165855232 | 2048000 | 1.23% | 143.9 |
| 500000 | 828948480 | 10334208 | 1.25% | 3593.4 |
| 1000000 | 1657176064 | 20709376 | 1.25% | 10241.7 |
| 3200000 | 5301207040 | 66265088 | 1.25% | 28984.4 |

320 万事件原库 5301207040 bytes（4.94 GiB），建成后 5367472128 bytes；已实测超过 4 GiB。该库从中断的 134 万事件恢复，日志的 seed_ms 只含剩余生成时间，不能用作完整生成耗时。

## B. 根因与适用范围

`LatencyPercentilesWithFilter` 读取原始 `timestamp_ms / latency_ms / ttft_ms`，过滤正值并按时间排序，随后 Go 按桶做 nearest-rank P95。旧单列时间索引只能做范围定位和时间排序，必须回表取得两个延迟值；打散的时间戳导致大量随机页访问。新索引提供同样时间前缀及完整延迟样本，真实 EXPLAIN 为 `USING COVERING INDEX idx_usage_events_ts_latency_ttft_cover`。

`LatencySummaryWithFilter` 两次读取原始样本，通过 `UNION ALL`、`row_number()` 和 `count()` 精确选择第 `ceil(n*0.95)` 项。覆盖索引减少回表，但时间范围内的样本仍需按 kind/value 排序，计划继续包含 `USE TEMP B-TREE FOR ORDER BY`。索引的第二、第三列不把整个时间范围变成按延迟排序的连续序列。

默认 API `include_failed=true`。用户选择成功时增加 `failed=0`；failed、Provider、模型、账号等不在该索引中，必须回表。只有时间／延迟阈值筛选可完全覆盖。自然规划器在复杂筛选下会选择旧时间索引或新索引，受数据、统计信息和 SQLite 版本影响；不会强制生产查询使用它。

小时汇总通过开关、支持的筛选条件、时间桶可表达性和 coverage/readiness 检查后用于可加和统计；不可用时既有路径回退到原始数据。即使启用小时汇总，时间线/异常点仍调用原始延迟样本查询；非 compact summary 或显式 summary_percentiles 仍调用精确原始汇总。平均延迟和计数不能恢复精确 P95，本 PR 不改这条语义边界。

五个 Tab 的主请求：总览包含 summary_percentiles、timeline、comparison 和统计列表；趋势包含 timeline、comparison；模型分析包含 timeline；API Key 与凭证主请求使用 compact summary 和各自统计，不直接请求精确延迟分位数。测试采用真实 controller、认证、JSON decode、service、JSON encode；不包含浏览器、HTTP socket 或网络传输。

旧索引与新索引有时间前缀重叠，写入时二者均维护。保留旧索引以服务其他查询和回滚；新索引增加存储、WAL 和写入 CPU，不能称为零成本。

## C. 实测性能

全部查询先预热，再各 5 次，新旧强制索引交替执行；自然 repository 另行预热并执行 5 次。以下单位 ms，均为中位数。完整最快、最慢、nearest-rank P95 耗时、返回行数和 EXPLAIN 在 [结果 JSON](usage-latency-covering-index.results.json)；5 次样本的 P95 耗时等于最大值，不能作为生产流量的可靠尾延迟估计。

### 原 SQL 扫描与 repository

`timeline` 的旧/新列含 SQLite 执行、driver 迭代、Go Scan 和收集样本；不含分桶排序。`summary` 含 SQL 窗口排序和 Scan。自然列为真实 repository 全流程。

| 事件数 | 查询 | 原 SQL 返回行 | 旧索引 | 覆盖索引 | 自然 repository | 扫描耗时变化 |
| --- | --- | --- | --- | --- | --- | --- |
| 100000 | timeline/7d | 5784 | 18.875 | 3.289 | 4.030 | -82.6% |
| 100000 | timeline/30d | 24787 | 77.641 | 13.686 | 16.016 | -82.4% |
| 100000 | timeline/60d | 49584 | 246.507 | 29.596 | 34.321 | -88.0% |
| 100000 | timeline/90d | 74382 | 228.755 | 41.046 | 48.092 | -82.1% |
| 100000 | timeline/120d | 99171 | 294.564 | 54.127 | 64.331 | -81.6% |
| 100000 | summary/7d | 2 | 62.720 | 31.819 | 31.382 | -49.3% |
| 100000 | summary/30d | 2 | 262.454 | 140.639 | 138.696 | -46.4% |
| 100000 | summary/90d | 2 | 804.119 | 466.392 | 467.519 | -42.0% |
| 100000 | summary/120d | 2 | 1069.348 | 623.619 | 626.770 | -41.7% |
| 500000 | timeline/7d | 28918 | 109.229 | 15.838 | 19.060 | -85.5% |
| 500000 | timeline/30d | 123957 | 473.217 | 68.112 | 82.191 | -85.6% |
| 500000 | timeline/60d | 247922 | 887.392 | 135.901 | 164.308 | -84.7% |
| 500000 | timeline/90d | 371893 | 1307.918 | 202.244 | 245.064 | -84.5% |
| 500000 | timeline/120d | 495857 | 1789.257 | 270.851 | 328.237 | -84.9% |
| 500000 | summary/7d | 2 | 358.657 | 173.223 | 170.220 | -51.7% |
| 500000 | summary/30d | 2 | 1584.251 | 826.785 | 790.976 | -47.8% |
| 500000 | summary/90d | 2 | 4556.520 | 2450.625 | 2439.857 | -46.2% |
| 500000 | summary/120d | 2 | 6159.269 | 3326.948 | 3355.955 | -46.0% |
| 1000000 | timeline/7d | 57842 | 268.752 | 31.706 | 38.623 | -88.2% |
| 1000000 | timeline/30d | 247923 | 1145.513 | 135.375 | 164.553 | -88.2% |
| 1000000 | timeline/60d | 495854 | 2294.504 | 268.598 | 329.426 | -88.3% |
| 1000000 | timeline/90d | 743780 | 3452.571 | 407.806 | 500.037 | -88.2% |
| 1000000 | timeline/120d | 991714 | 4743.351 | 542.050 | 662.867 | -88.6% |
| 1000000 | summary/7d | 2 | 809.738 | 359.372 | 356.090 | -55.6% |
| 1000000 | summary/30d | 2 | 3577.514 | 1640.676 | 1619.803 | -54.1% |
| 1000000 | summary/90d | 2 | 10769.093 | 5085.708 | 5074.946 | -52.8% |
| 1000000 | summary/120d | 2 | 14880.104 | 6965.070 | 6981.578 | -53.2% |
| 3200000 | timeline/7d | 185105 | 877.004 | 99.246 | 126.319 | -88.7% |
| 3200000 | timeline/30d | 793362 | 3774.837 | 425.175 | 532.101 | -88.7% |
| 3200000 | timeline/60d | 1586729 | 7520.464 | 855.107 | 1069.787 | -88.6% |
| 3200000 | timeline/90d | 2380107 | 11507.030 | 1306.759 | 1634.609 | -88.6% |
| 3200000 | timeline/120d | 3173487 | 14888.903 | 1693.495 | 2124.288 | -88.6% |
| 3200000 | summary/7d | 2 | 2708.327 | 1202.328 | 1195.270 | -55.6% |
| 3200000 | summary/30d | 2 | 11771.940 | 5388.657 | 5457.736 | -54.2% |
| 3200000 | summary/90d | 2 | 37073.580 | 17253.041 | 18487.429 | -53.5% |
| 3200000 | summary/120d | 2 | 49313.379 | 24914.147 | 22488.544 | -49.5% |

### SQLite 内部工作与 Go 分桶排序

engine_checksum 将真实时间线 SQL 包在 COUNT/SUM 中，强制 SQLite 计算三个字段，仅向 Go 返回两个整数。它包含额外 SUM，因此只是减少 Go Scan 后的独立代理测量，不能冒充原 SQL 精确 VM-only 时间。Go 分桶排序使用已经扫描的相同样本，独立计时并严格比较 production repository 结果；不通过相减推算扫描或分组时间。

| 事件数 | 天数 | SQLite checksum 旧 | SQLite checksum 新 | 独立 Go 分桶排序 |
| --- | --- | --- | --- | --- |
| 100000 | 30 | 60.334 | 5.245 | 2.522 |
| 100000 | 120 | 241.322 | 20.825 | 10.062 |
| 500000 | 30 | 354.525 | 26.095 | 14.360 |
| 500000 | 120 | 1437.279 | 104.278 | 57.369 |
| 1000000 | 30 | 1066.554 | 53.100 | 30.269 |
| 1000000 | 120 | 4386.346 | 211.777 | 121.197 |
| 3200000 | 30 | 3259.744 | 167.264 | 103.222 |
| 3200000 | 120 | 12693.360 | 664.429 | 411.208 |

### 320 万事件的 30 天筛选

| 筛选 | 查询 | 返回行 | 强制旧 | 强制新 | 自然 | 自然计划 |
| --- | --- | --- | --- | --- | --- | --- |
| success | timeline | 753680 | 3782.654 | 3741.412 | 3896.162 | 旧时间索引回表 |
| success | summary | 2 | 11409.464 | 11004.772 | 11051.615 | 新索引回表 |
| provider | timeline | 264453 | 3757.899 | 3756.160 | 3712.980 | 旧时间索引回表 |
| provider | summary | 2 | 8671.311 | 8061.217 | 8014.379 | 新索引回表 |
| model | timeline | 66104 | 3620.795 | 3533.006 | 3521.571 | 旧时间索引回表 |
| model | summary | 2 | 7365.763 | 7104.082 | 7098.845 | 新索引回表 |
| account | timeline | 7952 | 3977.367 | 3955.428 | 3897.984 | 旧时间索引回表 |
| account | summary | 2 | 8012.193 | 7257.890 | 7109.230 | 新索引回表 |
| threshold | timeline | 358430 | 3357.945 | 239.900 | 289.068 | 覆盖 |
| threshold | summary | 2 | 9052.705 | 2538.929 | 2526.962 | 覆盖 |
| combined | timeline | 2266 | 3491.566 | 3041.692 | 3506.408 | 旧时间索引回表 |
| combined | summary | 2 | 7455.346 | 6134.732 | 6488.748 | 新索引回表 |

各规模都执行相同六种筛选，完整记录见 JSON。账号筛选 auth-000，组合筛选成功 + claude + gpt-01 + auth-001 + min_latency_ms=1000，均非空。基准强制比较原始 SQL 的全部返回值；自然时间线与独立 Go 计算精确一致，普通回归对全部筛选的新旧自然 P95/TTFT 精确比较。后续加固基准使自然 summary 也直接比较强制 SQL 返回值，1000 行 smoke 验证此断言；没有因此重跑数小时大库实验。

### 完整 API

仅 10 万事件分析窗口执行完整 API A/B；readonly 查询确认全表 153212 条，其中原早期写入实验追加的 53212 条在窗口结束后，不参与分析窗口。小时汇总 false/true 均测试，所有统计值精确一致。只删除 generated_at_ms，channel_share 因已有 Go map 遍历顺序不稳定，以唯一 auth_index 对齐后逐字段比较；未放宽 P95 或任何数值断言。后续 readonly marker 与 processlock 守卫用新生成的 1000 行 fixture 完整 smoke。

| Tab | 天数 | 小时汇总 | 旧索引 | 新索引 | 耗时变化 |
| --- | --- | --- | --- | --- | --- |
| overview | 30 | false | 1096.862 | 1003.087 | -8.5% |
| overview | 90 | false | 2945.391 | 2562.932 | -13.0% |
| overview | 120 | false | 3644.990 | 3257.947 | -10.6% |
| trends | 30 | false | 764.044 | 758.433 | -0.7% |
| trends | 90 | false | 2145.329 | 2013.228 | -6.2% |
| trends | 120 | false | 2633.042 | 2652.295 | +0.7% |
| models | 30 | false | 725.543 | 662.262 | -8.7% |
| models | 90 | false | 2031.490 | 1939.025 | -4.6% |
| models | 120 | false | 2620.387 | 2541.540 | -3.0% |
| apiKeys | 30 | false | 443.540 | 434.184 | -2.1% |
| apiKeys | 90 | false | 1288.699 | 1228.565 | -4.7% |
| apiKeys | 120 | false | 1687.562 | 1634.343 | -3.2% |
| credentials | 30 | false | 540.240 | 517.079 | -4.3% |
| credentials | 90 | false | 1399.802 | 1384.833 | -1.1% |
| credentials | 120 | false | 1961.689 | 1835.091 | -6.5% |
| overview | 30 | true | 543.425 | 434.874 | -20.0% |
| overview | 90 | true | 1639.111 | 1237.327 | -24.5% |
| overview | 120 | true | 2042.833 | 1708.885 | -16.3% |
| trends | 30 | true | 403.368 | 372.216 | -7.7% |
| trends | 90 | true | 1182.996 | 1101.408 | -6.9% |
| trends | 120 | true | 1487.462 | 1434.660 | -3.5% |
| models | 30 | true | 395.390 | 352.588 | -10.8% |
| models | 90 | true | 1129.303 | 1065.878 | -5.6% |
| models | 120 | true | 1473.205 | 1436.040 | -2.5% |
| apiKeys | 30 | true | 386.256 | 288.895 | -25.2% |
| apiKeys | 90 | true | 879.725 | 882.762 | +0.3% |
| apiKeys | 120 | true | 1191.064 | 1183.496 | -0.6% |
| credentials | 30 | true | 399.115 | 304.881 | -23.6% |
| credentials | 90 | true | 937.229 | 919.662 | -1.9% |
| credentials | 120 | true | 1235.260 | 1273.056 | +3.1% |

API 总览改善明显小于目标 SQL 扫描；其他聚合、pricing、coverage 和结果组装仍占成本。120 天趋势 raw 中位数 +0.7%，120 天凭证 rollup +3.1%，不能宣称所有请求都提速；部分未请求 P95 的 Tab 改善也不能归因于延迟覆盖，可能来自规划器选择或缓存波动。本次没有 320 万事件 API 数据，不能用 SQL 收益推算端到端收益。

### 真实写入路径

100k 初始库，两个相同 `VACUUM INTO` 独立副本分别保留旧索引和额外候选索引；只在初始化切换索引，避免反复重建造成缓存/WAL 偏差。真实 InsertBatch；预热后交替 5 次。持续采集每组 20 × 1000 条，并发组同时循环读取 120 天时间线。每次重复插入检查 skipped，最终历史计数与时间／延迟 checksum 一致。

| 写入组 | 旧 ms | 新 ms | 变化 | 旧 events/s | 新 events/s |
| --- | --- | --- | --- | --- | --- |
| write/1 | 0.571 | 0.608 | +6.5% | 1751 | 1645 |
| write/100 | 5.922 | 6.106 | +3.1% | 16886 | 16377 |
| write/1000 | 50.707 | 55.321 | +9.1% | 19721 | 18076 |
| sustained/concurrent=false | 2822.631 | 2867.834 | +1.6% | 7086 | 6974 |
| sustained/concurrent=true | 2680.061 | 2463.600 | -8.1% | 7463 | 8118 |

| 写入组 | 旧 CPU ms | 旧 allocations bytes | 旧 WAL bytes | 新 CPU ms | 新 allocations bytes | 新 WAL bytes |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | 0.552 | 10280 | 70072 | 0.612 | 10280 | 74192 |
| 100 | 5.630 | 775848 | 341992 | 7.338 | 777608 | 358472 |
| 1000 | 53.385 | 7791280 | 1668632 | 58.440 | 7788576 | 1709832 |
| sustained | 2430.325 | 177420664 | 8944552 | 2485.878 | 177458120 | 8981632 |
| concurrent | 5511.131 | 214424456 | 214351272 | 5036.846 | 353234312 | 215224712 |

CPU 为进程 user+system 时间；并发组包含读工作，且 CPU/allocations 测量覆盖读协程退出等待，write_ms 只计写入完成，因此不能把两者直接相除。WAL 在每组开始 truncate，结束记录逻辑文件大小，可能已发生自动 checkpoint；不是永久文件增长或所有瞬时高水位。写入事件追加到窗口结束后，没有把随机历史时间插入的成本混入本组。未执行 320 万库的写入 A/B，因此写入成本结论限于本组合成数据。

### CPU、内存与磁盘

以下为 `/usr/bin/time -lp go test` 整个进程树统计，包含生成、预热、A/B、编译启动等；max RSS 不等于每个索引 variant 的工作集，不用于计算单次查询 CPU 收益。单次 API allocation/响应 bytes 和逐轮写入 CPU/alloc/WAL 记录在 JSON。

| 实验 | real s | user s | system s | max RSS bytes |
| --- | --- | --- | --- | --- |
| query-100k.log | 143.78 | 119.08 | 46.13 | 273514496 |
| query-500k.log | 423.92 | 302.11 | 110.53 | 272990208 |
| query-1000k.log | 1051.27 | 697.79 | 323.64 | 274001920 |
| query-3200k.log | 3746.42 | 2290.96 | 1247.86 | 435888128 |
| engine-100k.log | 5.85 | 4.8 | 2.04 | 188956672 |
| engine-500k.log | 28.7 | 15.35 | 10.68 | 149221376 |
| writes-cpu-100k.log | 73.23 | 64.21 | 31.48 | 273682432 |
| api-100k-final.log | 495.22 | 548.59 | 129.51 | 195837952 |
| offline-3200k-final.log | 54.57 | 15.28 | 12.56 | 150769664 |

### 索引与维护

空、10 行、100k 行库验证 Open 不建索引，监听后的空表准备可创建，非空表只登记离线需求；重复创建、重启幂等。真实 writer `BEGIN IMMEDIATE` 阻止离线 DDL，失败保留 pending，解锁后重试成功。300k 行 DDL 用 5ms deadline 真正中断，断言 deadline 已到期、无 partial index、历史不变，离线重试恢复。

320 万 marked fixture 的 startup maintenance 2.949 ms，仅登记 1 个索引；离线 CleanupDerivedOffline 构建 15228.592 ms，索引 66265088 bytes，结束 WAL 66665752 bytes。500ms lsof 采样按 device/inode 去重，计入已 unlink 的 SQLite 排序文件，观测额外打开文件逻辑大小 127759776 bytes（121.8 MiB）。SQLITE_TMPDIR 在启动进程前设置，路径规范化，修正了初版 0 bytes 的漏计；该初版数值弃用。

此次是同一 fixture DROP 后重建，库内仍有约 63.2 MiB 可复用 free pages，且 checksum/先前查询已预热；15.2s 不能替代首次构建 29.0s，观测额外空间也不含这些已占用 pages。新库首次 file 增量另见 A 表。500ms 采样可能漏过短时峰值，测的是逻辑文件长度，不是 APFS 物理占用上界；运维应额外留足 WAL、排序及 checkpoint 空间。

INSERT/UPDATE/DELETE abort triggers 在维护全程保护 raw usage，前后 checksum 相同，旧时间索引 rootpage 不变；失败不清除 pending，完成后要求 maintenance required/performanceDegraded=false。离线 command 的 processlock 必须先取得，真实在线服务持有同一锁时不能并行 cleanup。既有 cleanup/compact/归档 coverage 回归一并验证，未调整数据保留或历史覆盖规则。

## D. 修复与兼容性

- `internal/repository/sqlite/derived_cleanup.go`：在 derivedIndexStatements 登记候选 CREATE INDEX IF NOT EXISTS；仅此一行生产改动。
- SQLite lifecycle 测试：空／已有数据、重启、离线构建、锁失败、真实 DDL 中断、原始历史和状态恢复。
- usageevent 回归／opt-in 实验：自然 planner、NULL/0/失败／筛选精确结果、真实 SQL A/B、Go 排序和写入；CPU helper 按平台拆分。
- monitoring opt-in 实验：真实认证 handler 与五 Tab 响应精确比较。
- 原 TopModels query-plan 回归在 timestamp-only 场景继续严格要求 `idx_usage_events_timestamp`；两索引共存场景只要求 timestamp 范围索引搜索并继续禁止全表扫描，不锁定 SQLite 必须选择哪一个 timestamp-leading 索引。
- 无 API/UI、生产 SQL、分位数定义、归档语义、新配置或后台系统变化。没有生产 INDEXED BY；试验索引选择只存在于测试。

升级后的非空 usage_events 继续可服务，性能提升需停 Manager Server 后运行现有 `cpa-manager-plus cleanup-derived --db-path <数据库>`，完成后重启。新空库由既有 post-listen maintenance 创建。在线 periodic maintenance 不建大库索引。

回滚应用代码兼容旧 schema，新索引可留存但仍增加写入开销；如需回收空间，停服务、备份后按既有离线运维流程处理。删除索引不自动缩小 SQLite 主文件。本 PR 不提供自动删除或 VACUUM。

## E. 三轮审查与验证

三轮均由同一接管会话分别按不同主题重新检查，不宣称不同审查员。

1. 数据库与数据安全：检查 Open/Migrate、post-listen 启动、离线 command 锁、pending 清除顺序、失败恢复、raw triggers/checksum、旧索引 rootpage。发现临时磁盘观测路径／libc 环境缓存问题，最小修正并重测 320 万 fixture；补强 deadline 和完成状态断言。无生产数据写入。
2. 性能与统计语义：核对真实 SQL、nearest-rank 算法、默认 include_failed、小时汇总仍调用原始延迟路径、自然 EXPLAIN、全部尺度／筛选和真实 handler。修正既有 channel_share 无序输出的比较方式，逐值比较仍严格；独立写入副本消除反复 DDL 缓存偏差，补 per-variant CPU。增加自然 summary 对强制 SQL 返回值的直接断言，用小库复查。
3. 回归与范围：检查全部任务文件、跨平台 CPU build tags、CI 分类、无生产强制索引、报告数据边界和临时产物。发现旧 TopModels 测试硬编码旧索引名称的失败，保留旧场景并增加候选共存严格断言，复查通过。生产 diff 仍只有一行；剩余问题不扩大到预聚合重构。

本地验证全部通过：聚焦测试及最后的维护状态断言复查；全量 `go test ./...`、`go test -race ./...`、`go vet ./...`；Go build；现有 LatencyPercentiles/InsertBatch benchmarks；最新 opt-in 小库 smoke；prototype overlay 的 sqlite/cleanup-derived/usagecompact 和大库离线实验。SQLite 普通全量包 28.925s，race 包 594.829s，无竞态。API 完整实验及四尺度查询均 PASS。三轮审查修正后的复查未发现剩余阻断问题。仓库 CI 按范围运行 Manager Server、Windows SQLite 和 Docker，PR Template/Required checks；实时结果以最终 PR checks 为准。

现有 benchmark（3x）中，时间线 10k 为 10710444 ns/op、1150445 B/op，100k 为 119524906 ns/op、10761557 B/op；InsertBatch 全新 size1 为 360413 ns/op、size100 为 3678646 ns/op、size256 为 8469759 ns/op。全部 11 组（含重复和 mixed）记录在 JSON，作为已有路径回归数据。

剩余风险：120 天精确 P95 仍约 25s；复杂筛选需回表；写入约 9% 的 batch1000 成本与额外磁盘开销；只有合成、预热、单机且 5 次采样，不能代表真实生产尾延迟或硬件；未测冷缓存/真实生产布局/大库完整 API/大库写入。已确认收益与安全足以提交本次最小修复，无分位数预聚合或其他架构变更。

## 复现

仅合成 fixture 可用于 opt-in tests。普通库无 marker 会在 readonly probe 阶段拒绝；测试使用 processlock 防并发，仍需由操作者确认路径与磁盘余量。fixture 保留供后续实验，写入副本由 t.TempDir 清理。不要对生产库伪造 marker。

```sh
cd apps/manager-server
# 依次运行；不要并行压测。每次生成独立 fixture，日志输出其路径。
CPAMP_LATENCY_PERF=1 CPAMP_LATENCY_PERF_EVENTS=100000 go test ./internal/repository/usageevent -run '^TestLatencyCoveringIndexPerformanceExperiment$' -count=1 -timeout=120m -v
# 将 EVENTS 分别改成 500000 / 1000000 / 3200000。
# 仅 windows / engine / writes 时可设置 CPAMP_LATENCY_PERF_MODE。
# 已存在的 marked fixture 可设置 CPAMP_LATENCY_PERF_RESUME，EVENTS 须相同。

CPAMP_LATENCY_PERF_DB=<marked-fixture> go test ./internal/http/controller/monitoring -run '^TestLatencyCoveringIndexAPIExperiment$' -count=1 -timeout=120m -v
# 合并前目标场景：仅接受实际包含 320 万 usage_events 的完整 fixture，测试 30 天 Overview 与生产默认 rollup=true。
CPAMP_LATENCY_PERF_DB=<marked-3200000-fixture> CPAMP_LATENCY_PERF_API_TARGET_ONLY=1 go test ./internal/http/controller/monitoring -run '^TestLatencyCoveringIndexAPIExperiment$' -count=1 -timeout=120m -v

# SQLITE_TMPDIR 必须是预先创建的任务独占临时目录。大库实验要求 lsof。
SQLITE_TMPDIR=<task-owned-temp-dir> CPAMP_LATENCY_PERF_MAINT_DB=<marked-3200000-fixture> go test ./internal/repository/sqlite -run '^TestLatencyCoverIndexLargeOfflineExperiment$' -count=1 -timeout=20m -v

go test ./...
go test -race ./...
go vet ./...
go build -o <task-owned-output> ./cmd/cpa-manager-plus
go test ./internal/repository/usageevent -run '^$' -bench 'Benchmark(LatencyPercentilesWithFilter|InsertBatch)$' -benchtime=3x -benchmem -count=1
```

已有 LatencyPercentiles benchmark 直接 Open 后插入，没有调用 post-listen maintenance；它验证既有路径基线，不能当作新索引 A/B 证据。A/B 收益来自上述 opt-in 实验。

## F. Git 交付

性能报告、结果 JSON、测试和一行索引登记作为同一紧耦合提交交付到 `dev` PR。Commit SHA、PR URL、实时 CI 与合并条件记录在 PR 和最终交付消息，避免将自身 SHA 写回提交内容造成循环。提交前检查完整 staged diff；数据库、可执行文件、overlay、scratch 脚本及日志不进 Git。统一推送，PR 不自动合并，不发布版本。
