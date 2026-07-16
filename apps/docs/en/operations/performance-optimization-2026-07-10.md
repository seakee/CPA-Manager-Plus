# Performance Optimization Report — July 10, 2026

This report documents the Manager Server, Dashboard, Request Monitoring, Usage Analytics, and Model Prices performance work started on July 10 and completed on July 15, 2026. It covers the causes, implementation strategy, benchmark methodology, and measured results. The report was updated on July 16 with the later stages and fresh measurements from current `main`.

## Executive Summary

The work was delivered in ten stages:

1. PR #319 bounded memory, request, and SQLite connection resources.
2. PR #320 moved Dashboard core metrics to incremental hourly rollups.
3. PR #323 scoped Usage Analytics requests to the active tab.
4. Usage Analytics Hourly Rollup Phase 2B reused hourly rollups for strictly unfiltered long-window core metrics.
5. Usage Analytics Compact Summary skipped Summary subqueries not required by the active tab.
6. Credential Timeline SQL preaggregation and on-demand loading read timelines only for the selected credential.
7. Usage Analytics Hourly Rollup Projected Read selected only the rollup dimensions required by the caller.
8. Latency Percentile Compact Read used integer projections for latency and TTFT samples.
9. Dashboard Refresh Bounded Concurrency executed independent Dashboard reads concurrently.
10. Complete Monitoring requests reused duplicate dimension statistics and executed independent SQLite reads with bounded concurrency.

Using the effective work required to open Usage Analytics Overview on a 100,000-event dataset:

| Metric | Legacy path | Current path | Combined change |
|---|---:|---:|---:|
| Request time | about 7.00s | about 1.12s | about 84% lower |
| Allocation per operation | about 215MB | about 5.23MB | about 97.6% lower |

The legacy path requested every tab and the complete filter dataset. The current path executes only the main Overview query and selector request. Three fresh current-main runs measured about 1.119–1.126s and 5.23MB/op. This comparison represents the user-visible Overview workload rather than two identical SQL workloads.

## Benchmark Interpretation

- `ns/op` or duration is the execution time for one benchmark operation.
- `B/op` is total allocation during one operation; it is not the final process RSS.
- `allocs/op` is the number of allocations during one operation.
- pprof `inuse_space` shows heap objects still alive after GC and is more useful for identifying retained event slices or leaks.
- Core benchmarks use 100,000 synthetic usage events across 12 models, multiple accounts and API keys, successes and failures, tokens, and latency samples.

## Stage 1: Memory Pressure Controls

### Cause

The growth from a small initial RSS to hundreds of megabytes was not traced to one classic leak. It came from several linear or unbounded resource paths being exercised together:

- Usage import and export built complete datasets in memory.
- Monitoring retained a continuously growing event array.
- Multiple presentation snapshots could indirectly retain event-derived data.
- SQLite had no maximum open connection limit, allowing per-connection caches to multiply.
- Model Prices downloaded the full usage payload only to count model calls.
- Duplicate or stale Analytics requests continued running.
- Latency percentile calculation retained large sample windows in the Go heap.

### Changes

| Area | Before | After |
|---|---|---|
| Usage export | Build the complete response | Stream rows from a fixed database snapshot |
| Usage import | Parse the complete payload before persistence | Commit batches of 256 events |
| SQLite pool | Unlimited maximum open connections | 4 open, 2 idle, 5-minute idle lifetime |
| Monitoring events | Could continue growing | Retain at most 2,000 events |
| Monitoring page size | Wide results accumulated | 500 events per page |
| Presentation snapshots | Derived state could retain rows | At most 4 snapshots without event rows |
| Auto refresh | Could continue while hidden | 30-second default and pause while hidden |
| Analytics requests | Duplicate and stale work could continue | Abort, throttle, and in-flight deduplication |
| Model Prices | Download up to 50,000 complete events | Return model-level usage summaries |
| Summary P95 | Retain a full Go sample window | Calculate with a SQLite window query |

### Why Memory Improved

The import working set changed from `O(total events)` to `O(256)`. Frontend events, presentation snapshots, and SQLite connections now have explicit limits. Complete JSON payloads, full event arrays, and an expanding set of connection caches no longer amplify RSS after large operations.

On the current 100,000-event benchmark dataset with 12 models, the Model Prices result shape changes from up to 50,000 complete event rows to about 12 model summary rows—roughly 4,167 times fewer rows. This stage did not preserve one unified end-to-end before/after benchmark, so the report does not claim an unsupported fixed RSS reduction.

## Stage 2: Dashboard Hourly Rollup

### Cause

Each Dashboard refresh repeatedly scanned the same `usage_events` window for:

```text
aggregate
+ model stats
+ top models
+ hourly timeline
```

The cost was approximately `query count × raw event count`, so CPU, SQLite I/O, and page latency grew with retained history.

### Changes

A UTC hourly rollup stores stable metrics by:

```text
hour + model + billing model + service tier
```

It contains calls, success/failure, token categories, latency sum/count, and zero-token calls. Reads use:

```text
raw leading edge
+ complete hourly rollups
+ raw trailing edge
```

Cost is calculated from current model prices at read time. Pending checkpoints, disabled rollups, or read errors safely fall back to raw events.

### 100k Results

| Path | Duration | Change |
|---|---:|---:|
| Raw events | about 774ms | baseline |
| Hourly rollup | about 2.66ms | about 291 times faster |
| Latency reduction |  | about 99.7% |

Twenty consecutive rollup runs remained near 2.66ms/op and 556KB/op. The ending heap profile showed about 2.9MB in use with no retained 100,000-event slice.

## Stage 3: Active-Tab Usage Analytics Requests

### Cause

Before this change, opening Overview, Trends, Models, API Keys, Credentials, or Heatmap requested nearly the complete Analytics dataset and full filter options. SQL for hidden tabs still executed.

### Changes

- Each tab sends the minimum include matrix required by its UI.
- Filter selectors are loaded independently from the main Analytics request.
- The selector path only reads distinct model, API key, provider, and auth file values.
- Switching tabs does not reload stable selectors.
- Selector failure does not block the main content.
- Compatibility flags preserve behavior with older Manager Server versions.

### 100k Results

| Request | Duration | Allocation |
|---|---:|---:|
| Legacy full | about 7.00s | about 215MB |
| Overview initial | about 3.63s | about 34MB |
| Specialized tabs | about 2.34–3.10s | not recorded separately |
| Filter selectors | about 402ms | about 25KB |

Overview time decreased by about 48% and allocation by about 84%. Specialized tab time decreased by approximately 56%–67%.

## Stage 4: Usage Analytics Hourly Rollup Phase 2B

### Cause

After active-tab query shaping, Overview and related pages still scanned raw events for current-period and previous-period aggregates, model stats, and timelines. These core queries continued to scale with total history.

### Changes

A shared Dashboard and Monitoring hourly reader now owns:

- Checkpoint and latest-event completeness checks.
- Complete UTC hour reads.
- Raw leading and trailing edge compensation.
- Aggregate, model-stat, and timeline merging.
- Rate-limited fallback diagnostics.

Only strictly unfiltered requests use rollups:

- No search query or API key search.
- No model, provider, account, auth file, API key, project, source, or header filters.
- Both successful and failed events are included.
- No latency or cache-status filter.

Rollup-backed metrics:

- Current-period and previous-period aggregates.
- Model stats and cost calculated with current prices.
- Losslessly representable hour/day timelines.
- Average latency.

Metrics intentionally left on raw events:

- P95 latency and P95 TTFT.
- Rolling 30-minute RPM/TPM.
- Task buckets, active days, and zero-token models.
- API key, credential, channel, account, and heatmap dimensions.
- Every searched or filtered request.

### Timezone Correctness

Raw Analytics and the rollup reader use one shared timezone bucket rule. Every complete UTC hour is checked to ensure its first and last millisecond map to the same target bucket:

- UTC, whole-hour offsets, and normal DST boundaries can use rollups.
- Half-hour or 45-minute zones fall back to raw timelines when a UTC hour cannot be represented losslessly.
- Timeline fallback does not prevent Summary and Model Stats from using safe rollup data.

Tests cover UTC, Asia/Shanghai, Asia/Kolkata, America/New_York DST spring/fall, partial hours, price changes, pending checkpoints, disabled rollups, and empty-model semantics.

### 100k Overview — Three Runs

| Path | Average duration | B/op | allocs/op |
|---|---:|---:|---:|
| Raw | about 3.21s | 34.43MB | about 2.69 million |
| Rollup | about 2.48s | 20.24MB | about 0.97 million |
| Change | about 23% lower | about 41% lower | about 64% lower |

### 100k Rollup-Owned Core Path — Three Runs

This scope includes only the aggregate, model-stat, and timeline work owned by Phase 2B:

| Path | Average duration | B/op | allocs/op |
|---|---:|---:|---:|
| Raw | about 777ms | 23.70MB | about 1.86 million |
| Rollup | about 39ms | 9.51MB | about 142 thousand |
| Change | about 20 times faster | about 60% lower | about 92% lower |

The rollup-owned path is about 20 times faster, while the complete Overview request is about 23% faster because P95, TTFT, task, active-day, API key, and channel queries remain on raw events and now dominate the remaining time.

## Stage 5: Usage Analytics Compact Summary

### Cause

Usage Analytics already scoped the main dataset by tab, but the original Summary contract still ran rolling 30m, task buckets, active days, zero-token models, and percentile subqueries. Most tabs only display calls, tokens, cost, and average latency, so they still paid for hidden metrics.

### Changes

- Added a backward-compatible `compact` Summary profile; the full contract remains the default.
- All Usage Analytics tabs use the compact profile.
- Only Overview requests P95 latency and P95 TTFT; other tabs skip percentile queries.
- Request Monitoring and existing consumers continue using the full Summary contract.

### Fresh 100k Results On Current main, Three Runs

| Path | Average duration | B/op | allocs/op |
|---|---:|---:|---:|
| Full Summary | about 1.66s | about 3.77MB | about 310k |
| Compact Summary with percentiles | about 775ms | about 31KB | about 506 |
| Compact Summary without percentiles | about 21.5ms | about 22.8KB | 399 |

With percentiles retained, duration fell about 53% and allocation about 99%. Tabs that do not need percentiles reduced Summary duration by about 98.7%.

## Stage 6: Credential Timeline SQL Preaggregation And On-Demand Loading

### Cause

The Credentials tab previously read credential events and constructed timelines for every credential in Go even when the user viewed only one credential. Hidden timelines added scans, transfer, and allocation as event and credential counts increased.

### Changes

- Moved hourly Credential Timeline aggregation into SQLite while preserving partial raw edges.
- Shared UTC-hour representability checks; fractional-offset zones and non-lossless ranges safely fall back to raw events.
- Added exact `credential_ids` filtering for auth file, auth index, source hash, and source-only identities.
- The frontend loads credential rankings first and requests a timeline only for the selected credential.
- Cancellation, stale-response protection, loading state, and error feedback remain intact.

### Fresh 100k Results On Current main, Three Runs

| Request | Average duration | B/op | allocs/op |
|---|---:|---:|---:|
| Credential ranking and Summary | about 524ms | about 847KB | about 11.9k |
| One selected credential timeline | about 50.0ms | about 340KB | about 3,275 |
| Two-stage total | about 565ms | about 1.19MB | about 15.2k |

Opening the tab executes only the first row. The second request runs only when a selected credential needs a timeline, avoiding timeline construction for credentials the user does not inspect.

## Stage 7: Hourly Rollup Projected Read

### Cause

Phase 2B replaced many raw scans with hourly rollups, but the reader still loaded complete `hour + model + billing model + service tier` rows and built a full snapshot. Model-only and UTC-day callers still allocated unrelated dimensions and intermediate objects.

### Changes

- Added model-only and UTC daily projections.
- Selected compact snapshots based on the requested Summary, Model Stats, and Timeline combination.
- Preserved partial raw edges, current-price calculation, checkpoint checks, and timezone fallback.
- No schema or API changes were required.

### Fresh 100k Core Results On Current main, Three Runs

| Path | Average duration | B/op | allocs/op |
|---|---:|---:|---:|
| Raw | about 830ms | 23.78MB | about 1.865m |
| Projected rollup | about 24.0ms | 611KB | 7,900 |
| Versus raw | about 34.6 times faster | about 97.4% lower | about 99.6% lower |

Compared with the Phase 2B rollup result of about 39ms and 9.51MB/op, projected reads reduced duration by another 38% and allocation by about 94%.

## Stage 8: Latency Percentile Compact Read

### Cause

Nullable latency and TTFT samples read through `database/sql` created per-row integer-to-string-to-float conversions and temporary objects. Percentile paths in Trends, Models, and Overview retained unnecessary allocations.

### Changes And Results

- SQLite projects only valid integer latency and TTFT samples.
- Go reads compact integers directly while preserving nearest-rank semantics.
- Coverage includes DST, fractional-offset zones, filters, NULL samples, and 10k/100k datasets.
- Trends and Models allocation fell from about 7.20MB/op to about 4.80MB/op, a reduction of about 33%, with unchanged response and filtering semantics.

Fresh current-main runs kept Trends near 532–544ms and 4.81MB/op, and Models near 543–560ms and 4.80MB/op.

## Stage 9: Dashboard Refresh Bounded Concurrency

### Cause

Hourly rollups made Dashboard core metrics cheap, but rolling 30m, health timeline, recent failures, channel stats, and failure sources still ran serially. These recent/raw queries became the refresh critical path as data volume grew.

### Changes And Results

- Independent Dashboard queries run concurrently within the existing four-connection SQLite pool.
- Context cancellation and first-error propagation are preserved.
- No resident recent-event cache or response-contract change was added.
- Merge-time 10k, 100k, and 1m benchmarks reduced full-refresh latency by about 37%, 44%, and 50%, respectively.

Fresh single-operation runs on current `main`:

| Event count | Full refresh duration | B/op | allocs/op |
|---|---:|---:|---:|
| 10k | about 39.7–43.2ms | about 1.63MB | about 20.5k |
| 100k | about 403–405ms | about 1.64MB | about 21.8k |

Allocation remains nearly flat as event count grows; time is still dominated by queries that must scan recent/raw ranges.

## Stage 10: Complete Monitoring Request Deduplication And Bounded Concurrency

### Cause

Request Monitoring loads Summary, Timeline, hourly distribution, model/channel/account/API-key statistics, failure sources, task buckets, filter options, and event pagination in one analytics request. These independent reads previously ran sequentially. For an unfiltered request, `filter_options` also recalculated account, API-key, channel, and model statistics already requested by the main response.

In the 100k, 30-day UTC Monitoring include profile:

- The complete request took about 5.44s.
- Removing `filter_options` reduced it to about 3.38s.
- `filter_options` alone took about 2.09s.

### Changes

- Reuse loaded model, channel, account, and API-key source statistics when the main filter and filter-options base filter are equivalent.
- Reuse the built main-response rows inside filter options so both values and tie ordering are identical.
- Prefetch timeline percentiles, hourly distribution, high-dimensional statistics, filter options, recent failures, and the events page through a cancellable query group.
- Limit background prefetch to two concurrent reads. Together with the foreground summary/task path, one request uses at most three SQLite connections, leaving one connection in the four-connection pool available for other work.
- Cancel sibling work after the first query error and assemble the response on one goroutine only after all reads complete.
- Add no schema, rollup table, resident cache, or API field. Requests with dimension or status filters that the filter-options base scope clears continue to calculate those options independently, preserving the original option semantics. Search-only requests remain reusable because search is retained in that base scope.

### 100k Monitoring Include-Profile Results

| Path | Duration | B/op | allocs/op |
|---|---:|---:|---:|
| Before | about 5.44s | 16.26MB | about 984 thousand |
| After | about 1.69–1.81s | 15.14MB | about 958 thousand |
| Change | about 67%–69% lower | about 7% lower | about 3% lower |

Twenty consecutive runs remained near 1.69s/op and 15.14MB/op. The change adds no cross-request cache or retained event array.

A separate post-change benchmark using the curl scope (`from_ms=1`, `Asia/Shanghai`, and the same include payload) completed at about 1.815s/op, 26.02MB/op, and 1.093 million allocs/op. The wider time range increases allocation, but not elapsed time materially on this dataset.

### 58,686-Event Real-Data Validation

On a disposable SQLite backup of `bin/tmp/db/data`, after completing the cache-accounting migration and dashboard-rollup catch-up:

- The complete request baseline after the first Monitoring query-shaping pass was about 5.80s.
- This stage completed the service call in 2.21–2.38s.
- That is another reduction of approximately 59%–62% from the previous stage.
- Serializing the 7.58MB JSON response took only about 16ms, so SQLite reads still dominate the remaining time.
- The original `bin/tmp/db/data` was not modified.

## Memory And Stability Validation

- Ten-run and 200-run 100k rollup benchmarks remained stable.
- The 200-run benchmark stayed near 38–40ms/op.
- The final heap profile showed about 5MB in use.
- No CPAMP hourly reader or complete event slice appeared in the in-use top.
- No retained heap growth was observed as the request count increased.

`B/op` measures cumulative request allocation and does not mean the memory remains resident. The pprof in-use result is the more relevant leak signal.

## Why The Combined System Is Faster

The main cost previously resembled:

```text
all tab datasets × multiple queries × all raw events
```

It now resembles:

```text
active-tab data
×
complete hourly rollup rows
+
small raw edges
+
specialized metrics that cannot be rolled up safely
```

The architectural changes are:

- `O(all events)` retained memory became `O(fixed batch/fixed limit)`.
- All-tab queries became active-tab queries.
- Repeated raw scans became hourly-rollup reads.
- Complete event responses became aggregate responses.
- Unlimited connections and caches became explicit resource budgets.
- Stale work became cancel, throttle, and deduplication.

## Verification

The final implementation passed:

- The complete Manager Server test suite.
- The complete Go race suite.
- `go vet ./...`.
- 86 Vitest files and 719 tests.
- The VitePress documentation build.
- Timezone, DST, fallback, and price-change tests.
- Multiple code-review passes with no blocking findings remaining.

## Runtime And Rollback

Hourly rollup is enabled by default. To disable it temporarily:

```bash
USAGE_DASHBOARD_HOURLY_ROLLUP_ENABLED=false
```

Restart Manager Server after changing the variable. Dashboard and Usage Analytics will use raw events while disabled. Except for the one-time startup format upgrade documented in the Manager Server guide, disabling this runtime switch does not delete current-format rollup data. The switch is intentionally not exposed in the UI.

See the [Manager Server Guide](./manager-server.md) for the full runtime reference.

## Recommended Next Step

Compact Summary, Credential Timeline SQL preaggregation and on-demand loading, projected hourly rollup reads, compact latency-percentile reads, bounded Dashboard/Monitoring concurrency, and high-dimensional request shaping are now implemented. The remaining time depends primarily on queries that must preserve raw-event semantics:

- P95 latency and P95 TTFT.
- Task buckets.
- Active days.
- Zero-token models.
- Overview API key and channel dimensions.

Re-profile on a larger real dataset before adding another optimization layer. If filter-option distinct reads or one high-dimensional aggregate consistently dominates, prefer a focused query-shaping change or a dedicated dimension rollup. Current evidence still does not justify a bounded recent-event cache.
