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

`cpa-manager-plus upgrade cpa-import` 会通过 adoption 执行器，把已停止的 1.x 实例中受管 CPA 的配置与凭证文件复制到 v2 目标。规划迁移时有三条规则：

- 只有当 `--legacy` 目录就是 `copy-in` 步骤记录的那个目录时才会读取它；换成另一棵树会在任何读取之前以 `upgrade_cpa_source_root_mismatch` 拒绝。
- 目标自己的路径永远不会被复制：目标的管理密钥、监听地址、端口、插件根目录、凭证目录和客户端密钥列表都保留目标自身的值。本步骤自行创建目标时会先生成管理密钥并用 `/data/data.key` 加密记录，然后才创建目标，因此目标是已运行状态时重试仍可继续；导入成功后该记录会被删除。
- 目标已在运行且没有记录过密钥时，需要 `--destination-management-key-file`。本步骤会拒绝，而不会凭空再生成一个密钥（`upgrade_cpa_destination_key_required`）。

**已知限制：`codex-header-defaults`。** CPA v8 仍会加载该配置段（`codex-header-defaults` 加载为 `oauth.providers.codex.header-defaults`），但 v2 目标没有可写入的 `upstream.codex.header-defaults` 叶子，因此导入不会复制它。操作行会把它记为手动项 `section:codex-header-defaults`；如仍需这些请求头默认值，请在目标 CPA 配置中重新设置。同类的 `claude-header-defaults.*` 在目标有对应路径，会被正常复制。

## 回滚

回滚前先停止 Plus。旧 CPA-Manager 可继续读取主要用量表和旧 `settings.setup`，但无法识别 Plus 新增的管理员凭证、bootstrap 状态和加密数据密钥。建议只在完成备份后回滚，并优先回滚到迁移前备份。

## 常见问题

- 迁移后没有旧数据：通常是挂载了新的 `cpa-manager-plus-data` 空 volume，而不是旧 `cpa-manager-data`。
- 登录一直 401：Manager Server 接口需要管理员密钥；CPA Management Key 只用于登录 CPA 控制面板。
- 监控为空：确认 CPA 用量发布已开启，`USAGE_COLLECTOR_MODE` 与网络路径匹配，并且同一个 CPA 实例只有一个 Manager Server 消费用量队列。
- 解密失败：确认迁移后没有丢失或替换 `/data/data.key`。
