# Usage Analytics 原型对齐实施记录

> 内部历史记录：本文记录 Usage Analytics 开发过程中的原型差异和阶段任务，不是用户使用文档，也不是文档站内容来源。用户文档请以 [`cpamp-hq/docs`](https://github.com/cpamp-hq/docs)（[cpamp-docs.seakee.dev](https://cpamp-docs.seakee.dev/)）为准。

## 范围说明

本轮以设计图中的 Usage Analytics 功能目标为准，样式、配色、卡片质感和现有设计系统以当前已实现页面为标准，不额外重做视觉主题。差异分析重点放在功能、数据流、交互入口、测试和可验证性。

## 初始功能差异清单

### 未实现功能

- 顶部页面标题、说明、更新时间、收藏视图选择、刷新和导出入口未完整呈现。
- 总览页缺少分析入口卡片，不能从总览快速进入趋势、模型、API Key、凭证和热力矩阵。
- 常用视图、收藏视图、最近访问视图缺少本地状态管理和操作入口。
- 趋势页仅有基础折线图，缺少 Token 结构拆分、健康指标卡、异常 Key 列表和下钻预览组合。
- 模型页仅有排行和成本占比，缺少实体趋势、平均调用成本、洞察提示和模型明细联动增强。
- API Key 页缺少异常明细表、按指标切换的 API Key 趋势和异常组合跳转入口。
- 凭证页缺少活跃凭证切换、Provider 用量占比、Provider 健康、配额状态和洞察联动。
- 热力页仅支持星期/小时热力，缺少 API Key x Model、Auth File x Model、Provider x Model 的矩阵热力图。
- 导出功能仍是占位提示，没有实际生成可下载 JSON。
- i18n 未覆盖原型新增的按钮、列名、视图、洞察、矩阵和配额文案。
- 测试 mock 只覆盖旧 hook 返回结构，无法验证新增视图状态、矩阵、异常和配额数据。

### 实现不正确或阻塞项

- 后端 Usage Analytics 事件页结构存在冲突残留，事件 ID 与 request_id 字段未稳定映射。
- Monitoring summary 合并冲突导致 latency summary 与 total_count 优化无法同时保留。
- 监控中心 page model import 存在冲突残留，阻塞前端编译。
- 下钻预览成本曾固定为 0，无法体现模型价格估算。
- API Key 展示和导出需继续保证只显示掩码，不能泄漏原始 hash。
- 真实 Provider x Model、配额状态目前后端没有专用接口，只能基于已有聚合数据推导或生成估算 mock。

## 阶段性提示词与任务

### 阶段 1: 阻塞修复与现状分析

提示词：

```text
分析 Usage Analytics 当前实现与原型图功能差异，只关注功能和交互；样式配色沿用当前页面标准。先修复编译阻塞和冲突残留，再整理可执行开发阶段。
```

任务成果：

- 修复后端 analytics event page 的 `id` / `request_id` 扫描结构。
- 保留 monitoring summary 的 latency summary 和 total_count 优化。
- 清理 Usage Analytics 相关前端冲突残留。
- 确认原有页面已有基础 tab、筛选、趋势图、排行表和下钻预览能力。

问题记录：

- 工作区存在多处既有修改，不能回退非本轮改动。
- 原型图目标中部分功能没有独立后端 API，需要由现有聚合数据推导。

### 阶段 2: 数据模型扩展

提示词：

```text
在不新增后端接口的前提下，扩展 Usage Analytics 前端模型，生成 Provider 行、实体趋势、实体/模型矩阵、Key 异常、凭证配额估算和洞察数据。
```

任务成果：

- 新增 `UsageProviderRow`、`UsageEntityTrendSeries`、`UsageMatrix`、`UsageKeyAnomalyRow`、`UsageCredentialQuotaRow`、`UsageInsight` 等模型。
- Analytics 请求 include 增加 `channel_share`。
- 新增矩阵、趋势、异常、配额和洞察构建函数。
- 下钻预览成本改为按模型 cost/token 估算。

问题记录：

- Provider x Model 只能从 API Key / Credential 的 models 聚合推导。
- 凭证配额没有真实上游额度字段，本轮按近期成本生成估算状态，并在 UI 和文档中标注。

### 阶段 3: 页面功能实现

提示词：

```text
按照原型功能目标补齐 Usage Analytics 页面。保留当前视觉标准，补顶部操作、总览入口、收藏/最近视图、趋势结构、实体趋势、异常明细、Provider 健康、配额表和矩阵热力。
```

任务成果：

- 增加页面标题、说明、更新时间、收藏视图选择、刷新和 JSON 导出。
- 总览页增加分析入口、收藏视图、最近视图、洞察和请求预览。
- 趋势页增加 Token 结构、健康指标、异常时间点和异常 Key 明细。
- 模型页增加平均调用成本、实体趋势和洞察。
- API Key 页增加异常明细表、实体趋势和请求明细跳转。
- 凭证页增加活跃过滤、Provider 占比、Provider 健康和配额状态。
- 热力页增加实体/模型矩阵和热门组合列表。

问题记录：

- 由于单文件离线构建要求，未引入新图表库或动态 chunk。
- API Key 导出已对表格行、筛选条件、异常行和下钻预览做脱敏，避免 JSON 中出现原始 hash。

### 阶段 4: i18n、样式与测试

提示词：

```text
补齐所有新增 UI 文案的 en/zh-CN/zh-TW/ru 翻译，扩展测试 mock，覆盖矩阵、异常、下钻成本估算和页面主要入口。
```

任务成果：

- 四个 locale 已补齐新增 Usage Analytics key。
- `usageAnalyticsWiring.test.ts` 增加新增 key 的跨语言存在性检查。
- `UsageAnalyticsPage.test.tsx` mock 扩展到新 hook 字段。
- `UsageAnalyticsPage.test.tsx` 增加 JSON 导出脱敏回归测试，覆盖筛选条件中的 API Key hash。
- `usageAnalyticsModel.test.ts` 增加矩阵、Key 异常和下钻成本估算测试。

问题记录：

- React test renderer 对 ECharts DOM 细节不做像素级验证，页面功能验证以组件文本、回调和模型测试为主。
- 完整 UI 截图验收需在 dev server 或 preview server 中用浏览器测试补充。

### 阶段 5: 验证与收敛

提示词：

```text
运行 Usage Analytics focused tests、前端 type-check、lint、test、build 以及后端测试；遇到阻塞自动修复，直到任务完成。
```

验证结果：

- `npm --workspace apps/web run test -- src/features/usage-analytics` 已通过，3 个测试文件、20 个用例通过。
- `npm run type-check` 已通过。
- `npm run lint` 已通过。
- `npm run test` 已通过，52 个测试文件、392 个用例通过。
- `npm run build` 已通过，Vite singlefile 输出保持内联。
- `cd apps/manager-server && GOCACHE=... go test ./internal/service/monitoring` 已通过。
- `cd apps/manager-server && GOCACHE=... go test -run '^$' ./...` 已通过，确认后端全包编译无剩余错误。
- 使用 Playwright + Vite dev server + mock Manager API 验收 `/usage-analytics` 已通过：总览、趋势、模型、API Key、凭证、热力图 tab 均可渲染；ECharts canvas 已挂载；可见文本中未出现原始 API Key hash。
- 已在统一 ECharts 注册入口加入 `LegacyGridContainLabel`，消除 ECharts 6 对既有 `grid.containLabel` 布局的兼容提示并保留现有布局行为。

当前问题：

- `npm run manager-server:test` 在当前沙箱中会因 Go build cache 写入 `~/Library/Caches/go-build` 被拒绝；改用仓库内 `GOCACHE` 后，完整运行仍会因沙箱禁止 `httptest` 监听本地端口而失败。真实编译错误已修复，非监听端口的 focused 验证已通过。
- Vite dev server 与后端 `httptest` 一样需要本地端口监听；当前环境默认沙箱会拒绝监听，浏览器验收已通过受控本地服务完成。
