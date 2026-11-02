# CPA-Manager 到 CPA Manager Plus 迁移指南

本文面向从旧 `seakee/cpa-manager` / CPA-Manager 项目迁移到 `seakee/cpa-manager-plus` 的用户。目标是保留历史请求统计、模型价格、API Key 别名和已保存的 CPA 连接配置。

## 关键变化

- 镜像名从 `seakee/cpa-manager` 变为 `seakee/cpa-manager-plus`。
- 原生包和二进制从 `cpa-manager` 变为 `cpa-manager-plus`。
- 后端目录从旧项目的 `usage-service` 变为 `apps/manager-server`，但 HTTP 兼容端点仍保留 `/usage-service/*`。
- 完整 Docker 方案的登录凭证从 CPA Management Key 变为 Manager Server 管理员密钥 `cpamp_...`。
- CPA Management Key 会使用 `/data/data.key` 加密后保存到 SQLite。
- 旧 `settings.setup` 会迁移到 `settings.manager_config_v1`，并继续保留为兼容数据。

## 迁移前检查

1. 确认 CPA 本体版本：推荐 `v7.1.0+`，HTTP 用量队列至少需要 `v6.10.8+`。
2. 确认旧 Manager Server 数据位置：
   - Docker volume 常见为 `cpa-manager-data`。
   - 宿主机目录挂载通常映射到容器 `/data`。
   - 原生包默认在程序目录下的 `data/usage.sqlite`。
3. 停止旧容器或旧进程，避免 SQLite WAL 文件仍在写入。
4. 备份整个旧数据目录，而不是只备份单个数据库文件。至少保留：
   - `usage.sqlite`
   - `usage.sqlite-wal`
   - `usage.sqlite-shm`
5. 决定管理员密钥策略。推荐迁移时显式设置 `CPA_MANAGER_ADMIN_KEY` 或 `CPA_MANAGER_ADMIN_KEY_FILE`，避免依赖首次启动日志。

## Docker Volume 迁移

旧 compose 常见结构：

```yaml
services:
  cpa-manager:
    image: seakee/cpa-manager:latest
    volumes:
      - cpa-manager-data:/data

volumes:
  cpa-manager-data:
```

迁移时可以让 Plus 直接挂载旧 volume：

```yaml
services:
  cpa-manager-plus:
    image: seakee/cpa-manager-plus:latest
    restart: unless-stopped
    ports:
      - "18317:18317"
    environment:
      HTTP_ADDR: "0.0.0.0:18317"
      USAGE_DB_PATH: "/data/usage.sqlite"
      CPA_MANAGER_DATA_KEY_PATH: "/data/data.key"
      CPA_MANAGER_ADMIN_KEY: "replace-with-a-long-random-admin-key"
      USAGE_COLLECTOR_MODE: "auto"
    volumes:
      - cpa-manager-data:/data

volumes:
  cpa-manager-data:
    external: true
```

注意：Plus 示例 compose 默认创建 `cpa-manager-plus-data`。如果你直接使用默认新 volume，面板会像新安装一样没有旧数据。

## 宿主机目录迁移

如果旧容器使用宿主机目录，例如：

```bash
docker run -d \
  --name cpa-manager \
  -p 18317:18317 \
  -v /srv/cpa-manager-data:/data \
  seakee/cpa-manager:latest
```

迁移为：

```bash
docker stop cpa-manager
cp -a /srv/cpa-manager-data /srv/cpa-manager-data.backup

docker run -d \
  --name cpa-manager-plus \
  --restart unless-stopped \
  -p 18317:18317 \
  -v /srv/cpa-manager-data:/data \
  -e CPA_MANAGER_ADMIN_KEY='replace-with-a-long-random-admin-key' \
  seakee/cpa-manager-plus:latest
```

启动后打开 `http://<host>:18317/management.html`，使用管理员密钥登录。

## 原生包迁移

1. 停止旧 `cpa-manager` 进程。
2. 备份旧程序目录，尤其是 `data/usage.sqlite*`。
3. 解压 `cpa-manager-plus_<version>_<os>_<arch>`。
4. 将旧 `data` 目录复制到新包目录，或设置 `USAGE_DATA_DIR` / `USAGE_DB_PATH` 指向旧数据目录。
5. 首次启动时建议设置管理员密钥：

```bash
CPA_MANAGER_ADMIN_KEY='replace-with-a-long-random-admin-key' ./cpa-manager-plus
```

如果未设置，服务会生成 `cpamp_...` 并只在首次启动日志输出一次。

## 首次启动后验证

1. 查看启动日志：
   - 如果没有设置 `CPA_MANAGER_ADMIN_KEY`，保存 `CPA Manager Plus admin key generated: cpamp_...`。
   - 确认没有 `decrypt secret`、`open sqlite`、`bootstrap manager server` 错误。
2. 打开面板并进入「配置面板 -> CPA Manager Plus 配置」。
3. 检查已绑定 CPA 地址、请求监控开关、采集模式、轮询间隔。
4. 打开仪表盘或监控页，确认历史数据可见。
5. 请求 `/status`，确认 collector 状态、`lastConsumedAt`、`lastInsertedAt` 和 `lastError`。
6. 备份迁移后的 `/data`，此时必须包含新生成的 `data.key`。

## 数据密钥备份规则

Plus 会把 CPA Management Key 加密存储到 SQLite。默认数据密钥位于 `/data/data.key`。

- 只泄露 `usage.sqlite` 时，攻击者不能直接读出 CPA Management Key。
- 同时泄露 `usage.sqlite` 和 `data.key` 时，CPA Management Key 可被解密。
- 丢失 `data.key` 时，已加密的 CPA Management Key 无法恢复，只能重新保存 CPA 连接配置。

因此，灾备时要同时备份 SQLite 文件和 `data.key`；共享排查材料时不要上传 `data.key`。

## 管理员密钥丢失处理

管理员密钥重置指引已单独维护。请先停止 Manager Server、备份 `/data`，再按 [重置 Manager Server 管理员密钥](reset-admin-key.zh-CN.md) 处理。

## Managed CPA 导入

`cpa-manager-plus upgrade cpa-import` 会为已停止的 1.x 实例中受管 CPA 的状态创建、绑定并连接 v2 目标。**本次构建不会复制 provider 配置或 OAuth 凭证。** 该步骤以不带任何迁移决策的方式运行 adoption 执行器——Upgrade 界面没有资格证明记录——因此每个配置项与凭证项都会以 `upgrade_cpa_import_requires_qualification` 的原因记录为手动项，不会向目标 CPA 写入任何内容。自动复制推迟到受信任的 re-pin 步骤（U1-1b）；在它落地之前请手动完成这些项。操作会停在 `cpa_import` 且不记录 `cpa_config` 域，因为该域的含义是目标已持有复制后的状态，只有真正完成复制才能声明。

规划迁移时以下规则很重要：

- 它只在对账之后运行：记录的操作必须处于 `reconcile`（或处于自身未记录 `cpa_config` 域的 `cpa_import`，即重试位置），否则会在读取来源之前以 `upgrade_cpa_stage_conflict` 拒绝。创建目标阶段发生的拒绝不会改动已记录的位置。
- 先停止 1.x Manager 和受管 CPA，再停止 Manager Server：该命令持有 v2 数据根进程锁，锁被占用时会以 `upgrade_store_locked`（退出码 1）拒绝。它还会以只读描述符持有 1.x 来源的 ownership marker——与 copy-in 相同的排他栅栏，因此只读挂载的来源也能证明独占——并在整个运行期间保持；若 marker 被其他进程持有，或来源根本没有 marker，会在读取 CPA 目录之前以 `upgrade_cpa_source_locked` 拒绝。
- **该 marker 的残余限制。** marker 只能证明 1.x Manager 已停止，不能证明受管 CPA 容器已停止：CPA 本身不持有这个 marker，命令也无法观察运行中的容器。导入前请先停止 `cli-proxy-api` 服务（`docker compose stop cli-proxy-api`）。
- 只有当 `--legacy` 目录就是 `copy-in` 步骤记录的那个目录时才会读取它；换成另一棵树会在任何读取之前以 `upgrade_cpa_source_root_mismatch` 拒绝。
- 目标自己的路径永远不会被复制：目标的管理密钥、监听地址、端口、插件根目录、凭证目录和客户端密钥列表都保留目标自身的值。
- 来源版本取 `--cpa-version` 指定的确切版本，或 stack 声明的 `cli-proxy-api` 镜像 tag。1.x 安装在 `compose.yaml` 里写的是 `image: ${CPA_IMAGE}`，因此读取器只从旁边的 `.env` 解析这一个变量；`.env` 里的其他变量不会被解析、保留或报告。移动 tag、digest（包括 `repo:v7.3.4@sha256:...`）或无法解析的引用都不构成版本，导入会以 `upgrade_cpa_version_required` 拒绝，而不是猜测。这两个输入都是声明：命令不会运行或检查来源镜像，因此写错的 `--cpa-version` 不会被与运行中的 CPA 比对发现。
- 目标请求一旦记录就不可变。本步骤自行创建目标时，会在创建之前记录生成的密钥（用 `/data/data.key` 加密）、Supervisor 实际发布的完整请求（期望的 Runtime identity、generation 与 active artifact、来源 identity 与来源类型、有序客户端密钥列表、插件选择）以及 operation ID。重试会在改动任何东西之前把当前输入与这条记录比较：来源、插件选择、客户端密钥集合或密钥发生变化时，会以 `upgrade_cpa_destination_request_changed` 拒绝，已记录的尝试保持不变。目标 URL 是例外，因为 Supervisor 的请求里没有它：只有当某次运行证明它就是创建出的目标之后才会记录，所以从未被证明过的 URL 不会绑定这条尝试——重试时改为指向 Supervisor 实际创建的目标会被接受；而证明成功之后再换 URL，仍会以同一错误码拒绝。Supervisor 报告 stage/start 终态失败后，下一次运行才按它的 supersede 约定用新的 operation ID 重放完全相同的请求；Supervisor 重启会发布新的 generation，同样会被拒绝；而已经处于 ready 的目标会直接验证并续跑，不会重新提交。已记录的尝试不会重新生成密钥。
- 目标在任何其他访问之前完成绑定：每个创建分支最后都会用本次运行创建的密钥做一次客户端密钥列表的认证读取；若 CPA 不能同时以该密钥和该密钥集合应答，会以 `upgrade_cpa_import_target_foreign` 拒绝，且不会收到任何配置或凭证。如果 URL 上根本没有任何应答，那是另一种结果：拒绝码为 `upgrade_cpa_import_target_unreachable`，并明确说明没有任何应答——目标可能仍在启动，或该 URL 指向已停止的 1.x CPA。请先启动该 CPA 再重跑：同一个 URL 或修正后的 URL 都仍然会被接受，两种情况都没有写入任何内容。既没有记录也没有提供密钥的 ready 目标需要 `--destination-management-key-file`，否则以 `upgrade_cpa_destination_key_required` 拒绝。
- 当前工作完成后，目标 URL 和同一个管理密钥会写入 Manager 自身的 CPA 连接（`manager_config_v1` 及其 legacy setup 镜像），重启后控制台仍能连上目标；其他 Manager 设置保持不变。若 Manager 已持有指向另一个 CPA 的连接，本步骤会以 `upgrade_cpa_connection_conflict` 拒绝而不是改指；请在停止 Manager Server 后显式重绑该连接（`store-cpa-connection --repair-conflict`）再重跑，已记录的目标密钥会被复用。目标尝试行会保留，因为本次构建不记录 `cpa_config`，重跑仍需重放它。
- 每个 provider 配置项与凭证项都是明确的手动项，而不是被静默丢弃：每项都带有 `upgrade_cpa_import_requires_qualification` 和执行器自己的操作说明。规则本身就归为手动的项保留各自的具体原因，例如符号链接（`credential_not_regular`）、超过 1 MiB 的文件（`credential_too_large`）、无法读取的条目（`credential_unreadable`）；请自行复制这些文件。
- 声明 `config-version: 8` 的来源只接受 v8 版本的声明；其客户端密钥从 `access.api-keys` 读取，映射表未覆盖的 `api-keys.<family>` 会被报告而不是丢弃。

本步骤不会推广安装派生权威：它创建、绑定目标并保存目标连接，同时把配置与凭证复制记录为推迟的手动工作；但不会创建 Fresh 安装记录、策略检查点或资格。现有消费者明确拒绝升级派生（`legacy_managed`）来源，因此目标只作为连接来管理，而不是 New Managed 安装。该缺口仍然开放，自动复制要等待受信任的 re-pin 步骤。

**已知限制：`codex-header-defaults`。** CPA v8 仍会加载该配置段（`codex-header-defaults` 加载为 `oauth.providers.codex.header-defaults`），但 v2 目标没有可写入的 `upstream.codex.header-defaults` 叶子，因此推迟的复制不包含它。操作行会把它记为手动项 `section:codex-header-defaults`；如仍需这些请求头默认值，请在目标 CPA 配置中重新设置。同类的 `claude-header-defaults.*` 在目标有对应路径，属于推迟复制集合。

## 回滚

回滚前先停止 Plus。旧 CPA-Manager 可继续读取主要用量表和旧 `settings.setup`，但无法识别 Plus 新增的管理员凭证、bootstrap 状态和加密数据密钥。建议只在完成备份后回滚，并优先回滚到迁移前备份。

## 常见问题

- 迁移后没有旧数据：通常是挂载了新的 `cpa-manager-plus-data` 空 volume，而不是旧 `cpa-manager-data`。
- 登录一直 401：Manager Server 接口需要管理员密钥；CPA Management Key 只用于登录 CPA 控制面板。
- 监控为空：确认 CPA 用量发布已开启，`USAGE_COLLECTOR_MODE` 与网络路径匹配，并且同一个 CPA 实例只有一个 Manager Server 消费用量队列。
- 解密失败：确认迁移后没有丢失或替换 `/data/data.key`。
