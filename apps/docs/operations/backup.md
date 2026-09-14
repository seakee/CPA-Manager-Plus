# 备份与恢复

CPAMP 的请求历史、配置和加密凭证都在本机。备份时最容易犯的错，是只复制 `usage.sqlite`，漏掉 WAL/SHM、`data.key` 或安装目录里的 secret 文件。

## 必备备份文件

至少把这些文件作为一组备份：

- `usage.sqlite`
- `usage.sqlite-wal`
- `usage.sqlite-shm`
- `data.key`
- `usage-archives/`（使用过历史归档时）

如果部署目录还有自定义配置文件，也应一起备份。使用一键安装脚本时，至少额外备份安装目录中的 `secrets/` 和 `data/`；成功导入后通常不会再有 `secrets/cpa-management-key`，但升级失败或 `CPAMP_SKIP_EXECUTE=1` 时该临时文件可能仍需保留以便重试。手动 env/secret 部署仍应备份对应 secret 文件。

## 为什么必须备份 data.key

通过 setup 或面板保存的 CPA 连接，会把 CPA Management Key 使用 `data.key` 加密后保存到 SQLite。

- 只有 `usage.sqlite` 泄露时，攻击者不能直接读出 CPA Management Key。
- `usage.sqlite` 和 `data.key` 同时泄露时，CPA Management Key 可被解密。
- 丢失 `data.key` 时，已经保存的 CPA Management Key 无法恢复，只能重新保存 CPA 连接配置。

如果 CPA 连接由手动环境变量或 secret 文件管理，CPA Management Key 可能不写入 SQLite；请把对应的 secret 文件和数据目录作为一组备份。一键安装器的 env 输入会在成功后迁移到 SQLite，不应只备份一次性输入文件。

归档文件可能包含事件级 `fail_body` 和 `raw_json`，应按敏感数据保护。备份或恢复时必须让 SQLite、WAL/SHM、`data.key` 和 `usage-archives/` 来自同一个一致时间点。使用自定义 `dataDir` 或 `dbPath` 时，应按 [Manager Server 指南](./manager-server.md) 确认归档实际位置；它可能与 SQLite 不在同一目录。不要单独删除 WAL，也不要只恢复归档目录中的部分 run。

Usage Maintenance 的“导出用量”是当前 raw `usage_events` 的完整 JSONL snapshot：它不受 `USAGE_QUERY_LIMIT` 影响，按稳定的 snapshot boundary 分批流式导出，导出开始后新写入的事件不会混入。它不是完整 CPAMP 备份，也不会自动把已经删除的 raw events 从 `usage-archives/` segment 合并回 JSONL；需要迁移或灾备时仍应备份上面的完整数据组。

## Docker 备份示例

如果使用 named volume，可以先停止容器，再用临时容器导出：

```bash
docker stop cpa-manager-plus
docker run --rm \
  -v cpa-manager-plus-data:/data:ro \
  -v "$PWD":/backup \
  alpine \
  tar czf /backup/cpa-manager-plus-data.tgz -C /data .
docker start cpa-manager-plus
```

如果使用宿主机目录挂载：

```bash
docker stop cpa-manager-plus
cp -a /srv/cpa-manager-plus-data /srv/cpa-manager-plus-data.backup
docker start cpa-manager-plus
```

## 原生包备份

停止进程后复制数据目录：

```bash
cp -a ./data ./data.backup
```

Windows PowerShell：

```powershell
Copy-Item -Recurse .\data .\data.backup
```

## 恢复

1. 停止 CPAMP。
2. 恢复完整数据目录。
3. 确认 `usage.sqlite` 和 `data.key` 来自同一次备份。
4. 使用过历史归档时，同时恢复对应的 `usage-archives/`。
5. 如果使用 env/secret 管理 CPA 连接，同时恢复安装目录里的 `secrets/`。
6. 启动 CPAMP。
7. 登录后检查配置、监控数据、用量维护状态和采集器状态。

如果恢复后出现解密失败，优先检查 `data.key` 是否和 SQLite 匹配。

## 从已验证归档恢复 raw 请求历史

完整恢复应优先使用同一时间点的 SQLite、WAL/SHM、`data.key` 和 `usage-archives/` 备份。下面的 segment 导入流程用于原数据库不可用、需要在隔离环境恢复归档请求历史的情况；它不是对运行中生产数据库的表级合并方案。

归档 segment 是 `.jsonl.gz`，而用量导入只读取解压后的 JSONL。仅修改后缀无效，面板文件选择器和导入接口都不会透明解压 gzip。恢复步骤如下：

1. 只选择状态为 `verified` 或 `completed` 的 run，保留该 run 的 manifest 和原始 segment，不要直接修改归档文件。
2. 使用空数据目录启动隔离的恢复实例，确保它创建的是空 `usage.sqlite`。不要把 segment 导入仍保留原 identity ledger 的源数据库；源数据库会把这些事件按设计幂等跳过，这只能验证防重复，不能恢复 raw rows。
3. 把 segment 复制到权限受限的临时目录，并按文件名序号逐个解压。例如：

```bash
mkdir -p ./archive-restore
chmod 700 ./archive-restore
gzip -dc -- "./usage-archives/<run-id>/<segment-name>.jsonl.gz" \
  > "./archive-restore/<segment-name>.jsonl"
chmod 600 "./archive-restore/<segment-name>.jsonl"
```

Windows 上使用可信的 gzip 解压工具生成同样的 `.jsonl` 文件。解压后的文件仍可能包含 `fail_body` 和 `raw_json`，必须继续按敏感数据保护。

4. 登录隔离恢复实例，在请求监控中按 segment 序号逐个导入解压后的 `.jsonl`。不要直接选择 `.jsonl.gz`。
5. 对空恢复实例，每个 segment 的 `added` 应等于 manifest 中对应的 `event_count`，`skipped` 应为 `0`；所有 segment 的总数应等于 manifest 的总事件数。随后检查请求监控、Usage Analytics 和抽样事件字段。
6. 验证完成前保留原始归档和完整备份。不要把恢复实例的 SQLite 文件覆盖到仍在使用的生产数据目录，也不要手工合并表；生产环境完整回退应恢复一致时间点的整套备份。

如果同一个解压 segment 导入源数据库后全部显示 `skipped`，说明 identity ledger 正在正常阻止已归档事件复活；这是幂等性检查，不是恢复失败。

浏览器恢复已有 uploaded prefix 的可恢复导入会话时，会分块计算所选文件的 SHA-256 prefix，并由服务端比对持久化 digest。重新选择的文件必须与原文件内容一致，不能只依赖文件名、大小或 `lastModified`；不匹配时会停止续传并要求新建会话。旧的、已有上传 prefix 但没有 digest 的会话不会被当作安全续传目标。

归档 run 的“放弃任务”只允许用于尚未发布任何 segment 且从未开始 raw delete 的 `previewed` 以及未发布 segment 的 `failed` 状态；一旦发布了 segment（包括已发布的 `archived`、`verified`、`failed`），或者进入 `deleting`、部分删除或 `completed`，均不能取消。取消不会删除 raw usage、已发布归档 segment 或 identity ledger；已经开始 raw delete 的 run 必须继续恢复或完成。

注意：执行第一次 raw deletion 之前的完整备份（包含 `usage.sqlite`、`usage.sqlite-wal`、`usage.sqlite-shm`、`data.key` 以及 `usage-archives/`），是未来若某次升级需要依赖完整原始历史重建派生数据时的恢复边界。首次删除原始事件后，系统会冻结已配置价格的模型集合和上下文分层阈值，未来若需要依赖完整 raw 重建派生历史，可能需要恢复该备份或使用对应版本提供的专用迁移路径。

## 逻辑删除后的物理空间回收

用量维护页面中的删除只移除已归档且已验证的 raw rows，不会让 SQLite 文件立即缩小。完成上述停服备份后，可运行：

```bash
cpa-manager-plus compact-usage --db-path ./data/usage.sqlite
```

Docker named volume 使用同一镜像执行离线命令：

```bash
docker compose stop cpa-manager-plus
docker compose run --rm --no-deps cpa-manager-plus \
  compact-usage --db-path /data/usage.sqlite
docker compose up -d cpa-manager-plus
```

执行命令前必须停止所有连接该数据库的 Manager Server。进程级数据库锁会拒绝仍在运行的 Manager Server，SQLite 独占访问会拒绝冲突事务。任何维护锁或处于 `archiving`/`verifying`/`deleting` 的活动阶段也会阻断压缩；静态的 `previewed`、`archived`、`verified` 和 `failed` run 可以压缩。尚未完成的派生数据迁移 checkpoint 会被原样保留，服务重启后继续执行；`compact-usage` 不会推进、重置或重写这些 checkpoint。若维护锁属于可恢复的活动或 `failed` run，应先启动 Manager Server 并继续该 run；若锁仍关联非活动或终态 run，应保留备份和日志并停止压缩以进一步诊断，不得手工删除锁或 WAL。保留完整备份，并保守预留至少相当于当前数据库文件大小的临时可用空间。压缩后启动服务并检查 `/health`、`/status`、Dashboard、Usage Analytics 和用量维护页面；按上节方法解压一个归档样本并重新导入源数据库时应继续幂等跳过，而导入空的隔离恢复实例时应成功新增。

## 不保留请求历史，只迁移 Manager 配置

如果旧 `usage.sqlite` 很大且请求历史不需要保留，可以让新实例使用空数据目录，然后通过现有 Manager 配置 API 导出和导入非敏感的 CPA 连接地址、采集器、Codex 巡检与 External Usage Service 配置。该方式不会复制 `usage_events`、rollup、巡检运行历史、模型价格、API 密钥别名或账号处理策略，也不会导出 CPA Management Key。

在旧实例仍可访问时导出：

```bash
export OLD_CPAMP_URL='http://old-host:18317'
export OLD_CPAMP_ADMIN_KEY='cpamp_...'

curl -fsS \
  -H "Authorization: Bearer ${OLD_CPAMP_ADMIN_KEY}" \
  "${OLD_CPAMP_URL}/usage-service/config" \
  | jq '{config: .config}' \
  > manager-config.json
chmod 600 manager-config.json
```

新版本的 `manager-config.json` 不包含 CPA Management Key；仍应按配置文件管理，避免把其他敏感配置提交到版本库或发送到 Issue。来自旧版本的导出文件可能包含明文密钥，必须立即按 secret 处理并在迁移后删除。

停止旧实例并准备新实例的空数据目录。先在 Manager Server 未运行时用离线命令重新提供 CPA Management Key：

```bash
cpa-manager-plus store-cpa-connection \
  --cpa-base-url 'http://cpa:8317' \
  --management-key-file '/secure/cpa-management-key' \
  --db-path './data/usage.sqlite' \
  --data-key-path './data/data.key'
```

该命令要求先停止 Manager Server；它会把密钥加密写入 SQLite，命令输出不会回显密钥。

连接记录的 authority 规则是：完整的 `manager_config_v1` 权威；它与过期或冲突的旧 `setup` 同时存在时，启动和导入会保留 manager 连接并 canonicalize setup，不需要修复。manager 只有 partial 数据而完整的旧 setup 与其已有字段兼容时，setup 会补全 manager。只有在没有完整 authority 且 partial 记录彼此冲突，或解析器无法判断持久化状态时，上面的命令才会拒绝写入并在错误信息中给出修复方式。确认要以显式提供的连接为准时，追加 `--repair-conflict` 修复：

```bash
cpa-manager-plus store-cpa-connection \
  --repair-conflict \
  --cpa-base-url 'http://cpa:8317' \
  --management-key-file '/secure/cpa-management-key' \
  --db-path './data/usage.sqlite' \
  --data-key-path './data/data.key'
```

`--repair-conflict` 只用于修复解析器无法信任的历史状态：互相冲突的 `manager_config_v1`/`setup` 记录，或与请求冲突且没有权威方的 partial 记录。它把你显式提供的连接在单个事务里同时写入 `manager_config_v1` 与旧 `setup` 镜像（密钥加密存储），并保留采集器设置与其他数据；对完整且一致的已存连接仍然要求输入完全匹配，不会静默改绑。修复完成后再正常启动，连接存储迁移会照常完成。然后再启动新实例并导入其余配置：

```bash
export NEW_CPAMP_URL='http://new-host:18317'
export NEW_CPAMP_ADMIN_KEY='cpamp_...'

curl -fsS \
  -X PUT \
  -H "Authorization: Bearer ${NEW_CPAMP_ADMIN_KEY}" \
  -H 'Content-Type: application/json' \
  --data-binary @manager-config.json \
  "${NEW_CPAMP_URL}/usage-service/config"
```

导入时会校验 CPA Management API；成功后检查采集器状态和相关开关。确认恢复完成后安全删除导出文件和临时密钥文件。

如果旧实例仍由环境变量或 secret 文件管理，API 返回的 `source` 为 `env`，连接字段不能通过 API 导入覆盖；应先用上面的离线命令把 CPA 连接写入新 SQLite，或在新实例 setup 中重新填写。管理员登录凭证也不属于 Manager 配置导出：新实例使用新生成或显式设置的 `CPA_MANAGER_ADMIN_KEY`。
