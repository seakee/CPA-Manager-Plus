# 更新检查与一次性通知

Manager Server 负责检查 CPAMP 更新。面板读取服务端缓存；常规检查共享 60 秒全局冷却（normal checks share 60-second cooldown）；显式切换通道会强制执行一次立即检查（explicit channel changes force one immediate discovery）。后台首次延迟 5–59 秒，此后约每 6 小时检查一次；失败后退避 15–30 分钟。设置环境变量 `CPAMP_UPDATE_CHECK_ENABLED=false` 关闭后台检查，手动检查和切换通道仍可用。

Channel preference 为 auto、stable、rc、beta。auto 跟随服务端当前发行阶段，显式选择跨升级保存。未知构建不比较版本。当前版本高于通道目标时不推荐降级。跨大版本或要求迁移的更新展示指引，不提供直接替换镜像建议。V1 不自动安装，不覆盖并行维护线通知。

更新状态保存到现有 SQLite settings，不修改 usage 历史。正常运行下，同一精确 tag 的元数据只成功下载一次；下载成功到持久化之间崩溃可能重复。备份恢复、数据目录复制和重建会影响去重口径。

## 一次性通知

每个 Manager 实例对每个精确 tag 最多领取一次主动通知，所有管理员、标签页共享。后台发现不会领取通知；可见且已登录的页面领取后展示可关闭提示。软件更新页会记录该版本已被查看，不再叠加提示。关闭、查看、刷新、重启和手动检查均不重复弹出，版本卡片继续显示更新入口。新的 tag 可以再次通知。

领取记录先持久化，再返回浏览器；浏览器在领取后崩溃或响应丢失可能错过提示。此处选择不重复提醒，无法保证跨服务端和浏览器的严格恰好一次显示。用户仍可通过版本卡片查看。

## 面板入口

Dashboard 版本卡片保留当前版本和「有更新」入口，当前版本仍链接到对应 Release。点击更新标记或通知正文进入「系统信息 → 软件更新」（`#/system/updates`）；系统信息页也提供常驻入口。

软件更新页集中展示目标版本、摘要、最近成功检查时间和手动检查按钮。部署步骤与通道设置默认收起，迁移要求和最低 CPA 版本直接展示。Docker 镜像可复制精确版本，原生部署链接到对应版本的安装包。检查失败或缓存过期时会明确标记已有信息，不显示「已是最新」。

### External 面板兼容

- **Manager-hosted panel**：更新检查由 Manager Server 完成，完整支持 Channel、release-info、通知和软件更新页。
- **已确认的 CPA-hosted/external panel**：为保持旧版兼容，仅由浏览器读取 public update-index.json 中的 Stable pointer，用于 Dashboard Stable 更新提示。
- external fallback 不下载 release-info.json，不参与 Release Reach 计数，不提供 beta/rc Channel，也不进入完整软件更新页。请求失败时不保留旧 candidate。
- 并发检查采用 latest-request-wins，较早请求晚返回时不会覆盖较新的 Stable 结果。

## 发布元数据

新版本的中文 Release Notes 中必须包含唯一 JSON 注释，供生成器使用，不增加另一套完整发行说明：

```html
<!-- cpamp-update
{
  "summary": {"zh": "本次更新摘要", "en": "Release summary"},
  "update": {
    "breaking": false,
    "migration_required": false,
    "minimum_direct_upgrade_version": null,
    "upgrade_guide_url": "https://github.com/seakee/CPA-Manager-Plus/releases/tag/v2.0.0"
  },
  "compatibility": {"minimum_cpa_version": null}
}
-->
```

替换示例 tag，显式审查迁移和最低版本字段。新预发布标签使用 beta.N 或 rc.N。构建生成的 release-info.json 与所有资产一起校验并发布为不可变资产。恢复发布从精确资产恢复，不改写历史发行版。

## Channel Index

固定地址：`https://raw.githubusercontent.com/seakee/CPA-Manager-Plus/update-channel/update-index.json`。

正常发布与 recovery 在发布成功后调用同一流程：校验不可变 Release、资产集合和摘要、源提交以及精确版本镜像；推进 Docker latest/preview/major.minor 和 GitHub Latest；最后原子提交 Index、候选缓存、撤回列表与安装器使用的 stable-version.txt。

preview 跟随 beta Channel，RC 升级使用精确版本。Index 公布的 DockerHub 镜像必须实际可用，因此同步阶段需要 DockerHub 与 GHCR 都完整发布。某个注册表不可用时保留旧 Index，修复后使用独立恢复流程，不重新构建 Release。

首次启用可从包含元数据的 Beta、RC 或 Stable Release 开始。还没有 Stable 候选时，Index 的 stable 为 null，现有 latest 保持原值；Native 安装器可通过 CPAMP_VERSION 显式指定版本。历史无元数据版本不回填、不纳入候选。Index 尚未上线时，客户端显示检查失败。

在 main 上运行 Recover update channels 可独立修复 Index，输入 withdraw_release 撤回精确版本，restore_release 恢复候选。流程不发送 Telegram。所有发布流程共享 release-publish 并发组，非强制 Git ref 更新还会拒绝冲突；冲突后重跑并重新计算。撤回最后一个经过验证的 Stable 后，允许 stable 通道暂无可推荐版本（stable channel can temporarily have no candidate after withdrawal，客户端表达为 no_candidate），此时不再发布 stable-version.txt。

撤回不会删除精确版本或自动降级运行中的实例；没有替代目标的旧 minor 标签可能继续存在，不应作为该维护线仍受支持的承诺。
