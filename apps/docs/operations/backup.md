# 备份与恢复

CPAMP 的请求历史、配置和加密凭证都在本机。备份时最容易犯的错，是只复制 `usage.sqlite`，漏掉 WAL/SHM、`data.key` 或安装目录里的 secret 文件。

## 必备备份文件

至少把这些文件作为一组备份：

- `usage.sqlite`
- `usage.sqlite-wal`
- `usage.sqlite-shm`
- `data.key`

如果部署目录还有自定义配置文件，也应一起备份。使用一键安装脚本时，至少额外备份安装目录中的 `secrets/`；完整安装和 env/secret 管理模式会把 CPA Management Key 放在 `secrets/cpa-management-key`。

## 为什么必须备份 data.key

通过 setup 或面板保存的 CPA 连接，会把 CPA Management Key 使用 `data.key` 加密后保存到 SQLite。

- 只有 `usage.sqlite` 泄露时，攻击者不能直接读出 CPA Management Key。
- `usage.sqlite` 和 `data.key` 同时泄露时，CPA Management Key 可被解密。
- 丢失 `data.key` 时，已经保存的 CPA Management Key 无法恢复，只能重新保存 CPA 连接配置。

如果 CPA 连接由环境变量或 secret 文件管理，CPA Management Key 不写入 SQLite。请把对应的 secret 文件和数据目录作为一组备份。

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
4. 如果使用 env/secret 管理 CPA 连接，同时恢复安装目录里的 `secrets/`。
5. 启动 CPAMP。
6. 登录后检查配置、监控数据和采集器状态。

如果恢复后出现解密失败，优先检查 `data.key` 是否和 SQLite 匹配。

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
