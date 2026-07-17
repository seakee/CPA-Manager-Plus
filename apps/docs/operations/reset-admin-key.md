# 重置管理员密钥

忘记 CPAMP 管理员密钥时，用这页重置完整 Docker / 原生 Manager Server 模式的登录密钥。它不会重置 CPA Management Key，也不能恢复丢失的 `data.key`。

重置命令会直接修改本地 SQLite 数据库。执行前请先停止 Manager Server，避免服务运行中继续写入 SQLite。

## 命令作用

`cpa-manager-plus reset-admin-key` 会替换 Manager Server SQLite 中的 `settings.admin_credential_v1`，写入新的盐和 HMAC 摘要。

- 不指定密钥时，会生成一个 `cpamp_...` 管理员密钥。
- 指定密钥时，只保存摘要，命令不会把指定密钥回显到输出。
- 命令不会启动 HTTP 服务、采集器或后台任务。
- 命令不需要 CPA Management Key，也不需要 `data.key`。

也可以使用别名 `reset-admin-password`。

## 执行前检查

1. 停止 Manager Server。
2. 备份完整数据目录，包含 `usage.sqlite`、`usage.sqlite-wal`、`usage.sqlite-shm` 和 `data.key`。
3. 确认命令指向真实的 Manager Server 数据库：
   - Docker 默认：`/data/usage.sqlite`
   - 原生包默认：二进制旁边的 `data/usage.sqlite`
   - 自定义部署：`USAGE_DB_PATH` 的值

## Docker Compose

```bash
docker compose -f docker-compose.manager.yml stop cpa-manager-plus
docker compose -f docker-compose.manager.yml run --rm cpa-manager-plus reset-admin-key
docker compose -f docker-compose.manager.yml up -d cpa-manager-plus
```

命令会输出一次新生成的密钥：

```text
CPA Manager Plus admin key reset.
New admin key: cpamp_...
Save this value now. It will not be shown again.
```

### 一键安装脚本创建的 Docker 部署

如果 Docker 部署由 `install-cpamp.sh` 创建，优先让安装器完成停止、重置、重启和登录验证：

```bash
curl -fsSLO https://raw.githubusercontent.com/seakee/CPA-Manager-Plus/main/bin/install-cpamp.sh
CPAMP_OPERATION=repair \
CPAMP_INSTALL_DIR="$HOME/cpa-manager-plus" \
bash install-cpamp.sh
```

修复流程会把 SQLite 中的管理员凭证同步为安装目录中的 `secrets/cpamp-admin-key`，因此文件中的密钥和实际登录密钥会保持一致。它不会删除 Docker 数据卷、CPA Management Key 或请求历史。

非交互环境需要明确确认：

```bash
curl -fsSLO https://raw.githubusercontent.com/seakee/CPA-Manager-Plus/main/bin/install-cpamp.sh
CPAMP_OPERATION=repair \
CPAMP_INSTALL_DIR="$HOME/cpa-manager-plus" \
CPAMP_NON_INTERACTIVE=1 \
CPAMP_CONFIRM=1 \
bash install-cpamp.sh
```

## Docker Named Volume

```bash
docker stop cpa-manager-plus
docker run --rm \
  -v cpa-manager-plus-data:/data \
  seakee/cpa-manager-plus:latest \
  reset-admin-key
docker start cpa-manager-plus
```

如果使用 GitHub Container Registry 镜像，把 `seakee/cpa-manager-plus:latest` 替换为 `ghcr.io/seakee/cpa-manager-plus:latest`。

## 指定管理员密钥

推荐使用 `--admin-key-file`，避免密钥进入 shell history：

```bash
printf '%s\n' 'replace-with-a-long-random-admin-key' > /srv/new-cpamp-admin-key.txt
docker stop cpa-manager-plus
docker run --rm \
  -v cpa-manager-plus-data:/data \
  -v /srv/new-cpamp-admin-key.txt:/run/secrets/new_admin_key:ro \
  seakee/cpa-manager-plus:latest \
  reset-admin-key --admin-key-file /run/secrets/new_admin_key
docker start cpa-manager-plus
```

## 原生包

macOS / Linux：

```bash
./cpa-manager-plus reset-admin-key
```

Windows PowerShell：

```powershell
.\cpa-manager-plus.exe reset-admin-key
```

如果 SQLite 数据库不在默认数据目录，显式指定路径：

```bash
./cpa-manager-plus reset-admin-key --db-path /path/to/usage.sqlite
```

## 排障

- `SQLite database not found`：当前命令没有运行在真实配置环境中。请传入 `--db-path`，或挂载正确的 Docker volume / 宿主机目录。
- `is empty` / `does not look like a CPA Manager Plus Manager Server database`：路径指向了错误文件或新建的空文件。
- `database is locked`：Manager Server 或其他进程仍在使用 SQLite。停止相关进程后重试。
- 重置后仍无法登录：确认面板访问的是同一个 Manager Server。
- 生成的新密钥没有保存：在 Manager Server 停止状态下重新执行命令，会生成另一个随机密钥。
