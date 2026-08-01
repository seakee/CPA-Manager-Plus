---
title: 模型价格与成本估算
description: 配置 CPA Manager Plus 模型价格、service tier、长上下文倍率及 cache read/write/creation 计费，用于请求监控和用量分析的本地成本估算。
---

# 模型价格与成本估算

模型价格页面维护 CPAMP 的本地成本估算规则。它影响 Dashboard、请求监控和用量分析中的成本，不会修改 Provider 账单或 CPA 路由。

打开[模型价格演示](https://seakee.github.io/CPA-Manager-Plus/#/demo/model-prices)可以查看虚构价格和模型调用统计。

## 价格来源

- 首选从 models.dev 主动同步的公开元数据；当该来源不可用或缺少模型时，再使用 LiteLLM 和 OpenRouter 回退。
- 用户手动添加或覆盖的本地价格。
- 为模型别名、内部名称或 Provider 特定变体维护的条目。

同步只在用户主动触发时发生，可能使用当前 Manager Server 代理设置。

自动匹配会严格按 models.dev、LiteLLM、OpenRouter 的顺序进行。CPAMP 使用 models.dev catalog 的规范模型元数据优先识别第一方官方条目；每个来源都只有唯一、明确的模型身份匹配才会自动保存，模糊相似项不会自动确认。某个来源存在歧义时会继续尝试下一来源；三个来源都无法唯一确认时，待确认列表会分别保留各来源的候选，即使它们的原始模型 ID 相同也不会互相覆盖。

当前同步会映射 models.dev 的 `cost.input`、`cost.output`、`cost.cache_read` 和 `cost.cache_write`，将有效的 `cost.tiers` 上下文阶梯转换为 CPAMP 计费规则，并将 `experimental.modes.fast.cost` 映射为 Fast/Priority 短上下文价格。完整模型对象仍保存在原始元数据中；reasoning、未知实验模式、未知阶梯类型或无法安全验证的规则不会激活自动计费。

### 同步失败与最后有效价格

- models.dev 暂时不可用时，CPAMP 会继续尝试 LiteLLM 和 OpenRouter。
- 已保存的 models.dev 价格不会因为本次网络失败而被低优先级来源自动覆盖；回退来源仍可补充本地没有价格的模型。
- models.dev 成功响应但缺少官方条目或匹配存在歧义时，会按顺序尝试回退来源；只有唯一的强身份匹配才会替换该模型。
- 如果所有来源都失败，同步在写入数据库前终止，现有价格保持不变。
- 同步价格会一直作为最后有效数据使用，直到后续成功同步或用户手动修改；`syncedAtMs` 可用于判断数据新鲜度。

## 当前支持的计费语义

价格结构可能包括：

- 输入与输出 Token。
- Reasoning Token。
- Cache read、cache write 和 cache creation。
- 请求级固定费用。
- `service_tier` 差异。
- models.dev 上下文价格阶梯。
- 长上下文阈值和倍率。
- 模型别名与 billing model 映射。

例如 GPT-5.6 及类似模型可能根据上下文长度、service tier 和缓存类型采用不同价格。只有请求事件带有对应字段且价格规则存在时，CPAMP 才能正确计算。

### 上下文阶梯语义

- 阶梯条件是标准化输入 Token **严格大于** `tier.size`；恰好等于阈值时仍使用较低一档。
- 如果多个阶梯都满足条件，选择阈值最高的一档。
- 选中阶梯的费率应用于整个请求，而不是只计算超过阈值的 Token。
- 阶梯中缺失的输入、输出或缓存费率继承基础价格；models.dev 明确提供的零价格会保留为零。
- 目前只自动启用 `tier.type = context`、正数阈值且价格结构可安全验证的阶梯。其他规则仍保留在原始元数据中供排查。

### Fast/Priority 语义

- `experimental.modes.fast.cost` 同时匹配使用数据中的 `fast` 和 API `priority`。
- 短上下文优先使用显式 Fast/Priority 价格；缺失字段继承基础价格，显式零值保持为零。
- 命中上下文阶梯或旧版 GPT 长上下文规则时，使用对应的标准上下文价格，不再叠加 Fast/Priority。
- 非 models.dev、旧数据或没有显式模式价格的模型继续使用现有倍率作为兼容回退。

模型价格页只读展示已同步的上下文阶梯和服务层级价格。当前手动编辑器只维护基础价格；保存手动价格会明确清除该模型已有的同步高级规则，界面会在保存前提示。

## 模型名称匹配

客户端请求名、CPA 路由别名、Provider 实际模型名和价格表名称可能不同。排查成本为空时：

1. 在[请求监控](./monitoring.md)查看事件中的模型和 billing model。
2. 在模型价格页搜索同名条目。
3. 必要时增加本地别名或价格覆盖。
4. 回到[用量分析](./usage-analytics.md)刷新。

## 使用统计

模型价格页使用轻量的模型调用汇总判断哪些价格正在被使用，不会为了展示调用次数下载完整请求历史。

## 准确性边界

- Provider 账单是最终依据。
- 缺失 Token、service tier、长上下文或缓存字段会降低估算精度。
- 包月、赠送额度、非上下文阶梯、未支持的动态模式价格和多币种不一定能由单一价格条目完整表达。
- 更新价格后，历史成本可能按当前价格重新展示；价格表不是不可变账单快照。
