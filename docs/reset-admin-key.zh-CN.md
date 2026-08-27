# 重置 Manager Server 管理员密钥

本文用于重置 CPA Manager Plus 完整 Docker / 原生 Manager Server 托管面板使用的管理员密钥。它不会重置 CPA Management Key，也不能恢复丢失的 `data.key`。

重置命令会直接修改本地 SQLite 数据库。执行前请先停止 Manager Server，避免服务运行中继续写入 SQLite。

## 命令作用

`cpa-manager-plus reset-admin-key` 会替换 Manager Server SQLite 中的 `settings.admin_credential_v1`，写入新的盐和 HMAC 摘要。

- 不指定密钥时，会复用首次启动初始化方案，生成一个长随机 `cpamp_...` 管理员密钥。
- 指定密钥时，只保存摘要，命令不会把指定密钥回显到输出。
- 命令不会启动 HTTP 服务、采集器或后台任务。
- 命令不需要 CPA Management Key，也不需要 `data.key`。

也可以使用别名 `reset-admin-password`，方便把管理员密钥理解为登录密码的用户。

## 执行前检查

1. 停止 Manager Server。
2. 备份完整数据目录；如果存在 `usage.sqlite-wal`、`usage.sqlite-shm`，也要一起备份。
3. 确认命令指向真实的 Manager Server 数据库：
   - Docker 默认：`/data/usage.sqlite`
   - 原生包默认：二进制旁边的 `data/usage.sqlite`
   - 自定义部署：`USAGE_DB_PATH` 的值，或 `USAGE_DB_URL` 的本地 path

## Docker Compose

在 compose 文件所在目录执行：

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

重启后在 Manager Server 登录页使用这个新密钥登录。

## Docker Named Volume

如果你手动运行容器并使用默认命名卷：

```bash
docker stop cpa-manager-plus
docker run --rm \
  -v cpa-manager-plus-data:/data \
  seakee/cpa-manager-plus:latest \
  reset-admin-key
docker start cpa-manager-plus
```

如果使用 GitHub Container Registry 镜像，请把 `seakee/cpa-manager-plus:latest` 替换为 `ghcr.io/seakee/cpa-manager-plus:latest`。URL 部署手工 `docker run` 时还必须传入原来的 `USAGE_DB_URL`；更推荐会继承 service environment 的 Compose 命令。

## Docker 宿主机目录挂载

如果容器把宿主机目录映射到 `/data`，重置容器也要挂载同一个目录：

```bash
docker stop cpa-manager-plus
cp -a /srv/cpa-manager-plus-data /srv/cpa-manager-plus-data.backup
docker run --rm \
  -v /srv/cpa-manager-plus-data:/data \
  seakee/cpa-manager-plus:latest \
  reset-admin-key
docker start cpa-manager-plus
```

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

`--admin-key` 也可用于受控环境，但可能被 shell history 或进程审计记录：

```bash
cpa-manager-plus reset-admin-key --admin-key 'replace-with-a-long-random-admin-key'
```

## 原生包

先停止原生进程，再进入解压后的程序目录执行：

macOS / Linux：

```bash
./cpa-manager-plus reset-admin-key
```

Windows PowerShell：

```powershell
.\cpa-manager-plus.exe reset-admin-key
```

如果部署使用 `USAGE_DB_URL`，请在同一配置环境中省略 `--db-path`，以保留 URL 参数。显式传入 `--db-path` 会选择默认 SQLite 设置。

仅使用 path 且 SQLite 数据库不在默认数据目录时，显式指定路径：

macOS / Linux：

```bash
./cpa-manager-plus reset-admin-key --db-path /path/to/usage.sqlite
```

Windows PowerShell：

```powershell
.\cpa-manager-plus.exe reset-admin-key --db-path C:\path\to\usage.sqlite
```

然后重新启动原生进程，并使用新的管理员密钥登录。

## 排障

- **`SQLite database not found`**：当前命令没有运行在 Manager Server 的真实配置环境中。URL 部署应传入原来的 `USAGE_DB_URL` 并省略 `--db-path`；path 部署可传入 `--db-path`。同时确认已挂载正确的 Docker volume / 宿主机目录。
- **`is empty` / `does not look like a CPA Manager Plus Manager Server database`**：路径指向了错误文件或新建的空文件。请从 Manager Server 数据目录中找到真实的 `usage.sqlite`。
- **`database is locked`**：Manager Server 或其他进程仍在使用 SQLite。停止相关进程后重试。
- **重置后仍无法登录**：确认面板访问的是同一个 Manager Server。Docker 场景请检查 volume 名称或宿主机挂载路径。
- **生成的新密钥没有保存**：在 Manager Server 停止状态下重新执行命令，会生成另一个随机密钥。

## 安全提示

- 把生成的 `cpamp_...` 当作密钥保存。
- 如果旧管理员密钥可能泄露，请同步轮换部署系统中保存的旧密钥。
- 该命令只修改 Manager Server 登录凭证，不会修改 CPA 凭证或已加密保存的 CPA Manager Plus 配置。
