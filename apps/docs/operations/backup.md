# 备份与恢复

CPAMP 的请求历史、配置和加密凭证都在本机。备份时最容易犯的错，是只复制 `usage.sqlite`，漏掉 WAL/SHM、`data.key` 或安装目录里的 secret 文件。

## 必备备份文件

至少把这些文件作为一组备份：

- `usage.sqlite`
- `usage.sqlite-wal`
- `usage.sqlite-shm`
- `data.key`
- `usage-archives/`（使用过历史归档时）

如果部署目录还有自定义配置文件，也应一起备份。使用一键安装脚本时，至少额外备份安装目录中的 `secrets/`；完整安装和 env/secret 管理模式会把 CPA Management Key 放在 `secrets/cpa-management-key`。

## 为什么必须备份 data.key

通过 setup 或面板保存的 CPA 连接，会把 CPA Management Key 使用 `data.key` 加密后保存到 SQLite。

- 只有 `usage.sqlite` 泄露时，攻击者不能直接读出 CPA Management Key。
- `usage.sqlite` 和 `data.key` 同时泄露时，CPA Management Key 可被解密。
- 丢失 `data.key` 时，已经保存的 CPA Management Key 无法恢复，只能重新保存 CPA 连接配置。

如果 CPA 连接由环境变量或 secret 文件管理，CPA Management Key 不写入 SQLite。请把对应的 secret 文件和数据目录作为一组备份。

归档文件可能包含事件级 `fail_body` 和 `raw_json`，应按敏感数据保护。备份或恢复时必须让 SQLite、WAL/SHM、`data.key` 和 `usage-archives/` 来自同一个一致时间点。使用自定义 `dataDir` 或 `dbPath` 时，应按 [Manager Server 指南](./manager-server.md) 确认归档实际位置；它可能与 SQLite 不在同一目录。不要单独删除 WAL，也不要只恢复归档目录中的部分 run。

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

如果旧 `usage.sqlite` 很大且请求历史不需要保留，可以让新实例使用空数据目录，然后通过现有 Manager 配置 API 导出和导入 CPA 连接、采集器、Codex 巡检与 External Usage Service 配置。该方式不会复制 `usage_events`、rollup、巡检运行历史、模型价格、API 密钥别名或账号处理策略。

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

`manager-config.json` 可能包含明文 CPA Management Key，应按 secret 管理，不要提交到版本库或发送到 Issue。

然后停止旧实例，使用空目录启动新实例。记录新实例首次启动生成的管理员密钥，再导入：

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

导入时会校验 CPA Management API；成功后检查采集器状态和相关开关。确认恢复完成后安全删除导出文件。

如果连接配置由环境变量或 secret 文件管理，API 返回的 `source` 为 `env`，连接字段不能通过导入覆盖；应改为迁移部署环境中的 `CPA_UPSTREAM_URL`、`CPA_MANAGEMENT_KEY` 或对应 secret 文件。管理员登录凭证也不属于 Manager 配置导出：新实例使用新生成或显式设置的 `CPA_MANAGER_ADMIN_KEY`。
