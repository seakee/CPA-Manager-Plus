# 版本说明

## v2 Beta 预发布通道与资产

v2 Beta 是 **Prerelease**，不是 Stable；`releases/latest`、1.x 自动面板更新与默认安装脚本继续面向 **1.x Stable**。仅在维护者实际发布后，才可从 [全部 Releases](https://github.com/seakee/CPA-Manager-Plus/releases) 选择对应 Beta 版本下载；本页不是已经发布或已获生产升级授权的声明。不要将 Beta 指向浮动标签，也不要用 Beta 单文件面板替换 Embedded Full Web。

同次 v2 发布的运行组合为：

- 三个镜像：`seakee/cpa-manager-plus:<tag>@sha256:<digest>`、`seakee/cpamp-ingress:<tag>@sha256:<digest>`、`seakee/cpamp-runtime:<tag>@sha256:<digest>`；官方 CPA 固定 `v8.0.55`。
- Prerelease 附件 `docker-compose.v2-embedded.yml` 与 `embedded-release-pairing.json`，同版校验文件及发布说明；不能混合版本或只更换一个角色。
- linux/amd64 Native：`cpa-manager-plus-native-v2_<ver>_linux_amd64.tar.gz`。不提供 v2 的 macOS、Windows、linux/arm64 原生包，见 [原生部署](../deployment/native.md)。
- 独立 CPA 轻量面板 `management.html`；Stable 与 Beta 的双轨取得方式见 [轻量面板](../deployment/cpa-panel.md)。

v2 更新元数据契约使用 `schema_version: 2`、`migration_required: true`、`minimum_direct_upgrade_version: v1.14.10`、`minimum_cpa_version: v8.0.55`。更新索引外层 `versions.json` 仍为 `schema_version: 1`，`channels.beta` 使用 `schema_version: 2`。这些字段不表示 1.x 可自动原地升级：先读 [离线 copy-in、对账与回滚](../operations/update.md)，备份 `data.key`、SQLite WAL/SHM 和 `usage-archives/`。

## 当前许可证与可核验文件

**当前仓库根 `LICENSE` 是 MIT**，镜像复制该文件；官方 CPA 的 MIT LICENSE 独立分发，第三方依赖保留各自许可证。不要把未来许可提案视为已生效，也不要仅根据 CPAMP 的 MIT 推断所有打包依赖均采用 MIT。实际分发内容与对应许可证文本是核验依据。

### Embedded 镜像

| 文件 | 含义 |
| --- | --- |
| `/usr/local/share/licenses/cpamp/LICENSE` | 三角色镜像中的 CPAMP 项目许可证 |
| `/usr/local/share/licenses/cli-proxy-api/LICENSE` | Runtime 镜像中随内置 CPA 分发的官方许可证，不在另外两角色中重复提供 |
| `/usr/local/share/cpamp/release/third-party-inventory.txt` | JSON 内容的聚合第三方收集清单（不要因 `.txt` 后缀当作纯文本列表） |
| `/usr/local/share/licenses/<component>/inventory.json` | 角色对应的组件清单和原始 LICENSE/NOTICE 文件摘要 |
| `/usr/local/share/cpamp/provenance.json` | 版本、源码、角色/平台、二进制与第三方清单的字节绑定记录 |

Manager 的 component 为 `manager`、`web`、`base-image`；Ingress 为 `ingress`、`base-image`；Runtime 为 `runtime-supervisor`、`base-image`。Go/Web 收集原始许可文本；Alpine 的边界是已安装 APK 许可元数据及可取得的包内声明，Debian 基础镜像使用已安装包的 copyright 文件。`VERIFIED_LICENSE_COLLECTION` 不代表法律审批，也不把“仅元数据”自动变为完整原文覆盖。

在已安装的同版发布 Compose 中查验（输出是公开构建资料，不包含运行密钥）：

```bash
docker compose -f docker-compose.v2-embedded.yml exec -T cpamp-manager \
  cat /usr/local/share/licenses/cpamp/LICENSE
docker compose -f docker-compose.v2-embedded.yml exec -T cpamp-runtime \
  cat /usr/local/share/licenses/cli-proxy-api/LICENSE
docker compose -f docker-compose.v2-embedded.yml exec -T cpamp-manager \
  cat /usr/local/share/cpamp/release/third-party-inventory.txt
docker compose -f docker-compose.v2-embedded.yml exec -T cpamp-manager \
  cat /usr/local/share/licenses/manager/inventory.json
docker compose -f docker-compose.v2-embedded.yml exec -T cpamp-manager \
  cat /usr/local/share/cpamp/provenance.json
docker compose -f docker-compose.v2-embedded.yml exec -T cpamp-ingress \
  cat /usr/local/share/cpamp/provenance.json
docker compose -f docker-compose.v2-embedded.yml exec -T cpamp-runtime \
  cat /usr/local/share/cpamp/provenance.json
```

将版本、`sourceCommit`、平台/角色与配对记录核对，并按 inventory 列出的路径/摘要查验具体声明。Provenance 是**未签名的构建来源声明**，不是受保护发布者证明、签名或法律结论；校验和只证明下载内容匹配相应记录，仍须信任取得记录的发布来源。

### linux/amd64 Native

从解压后的包根目录读取：

```bash
cat LICENSE
cat share/licenses/cli-proxy-api/LICENSE
cat share/cpamp/runtime/cpa-artifact.json
```

当前 Native 包实际携带 CPAMP 根 LICENSE 与上述 CPA LICENSE。镜像里的组件 inventory/Provenance 不等于 Native 也有同一份证据；当前 Native 包不提供 `THIRD_PARTY_NOTICES/` 或独立 Native Provenance，不能用容器路径宣称完成 Native 依赖声明核验。

## 1.x Stable：版本入口


查看新版本时，重点关注三件事：新增了什么、是否有不兼容变化、升级前是否需要额外备份或配置调整。

## 查看最新版本

- [最新 GitHub Release](https://github.com/seakee/CPA-Manager-Plus/releases/latest)
- [全部历史版本](https://github.com/seakee/CPA-Manager-Plus/releases)

每个 Release 会列出主要功能、修复、下载文件和必要的升级说明。

## 升级前

1. 阅读目标版本的 Upgrade Notes。
2. 确认当前使用的是轻量面板、Docker 完整模式还是原生包。
3. 完整模式先备份 SQLite、`data.key` 和安装目录中的 secret。
4. 再按[更新 CPAMP](../operations/update.md)执行对应步骤。

轻量面板通常由 CPA 自动检查新的 `management.html`。如果升级后仍显示旧界面，查看[轻量面板更新与缓存](../deployment/cpa-panel.md#更新与缓存)。

## 遇到问题

- 更新后无法登录：[重置管理员密钥](../operations/reset-admin-key.md)
- 更新后监控为空：[请求监控没有数据](../troubleshooting/request-monitoring.md)
- 不确定数据文件位置：[配置与数据目录](../operations/configuration.md)
