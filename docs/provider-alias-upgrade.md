# Provider alias 补丁升级

官方发布新版本后，在本仓库的 `codex/provider-key-aliases` 分支执行：

```bash
./bin/release/reapply-provider-alias.sh origin/dev
```

脚本会获取官方代码，把 provider alias 提交重放到最新 `origin/dev`，然后运行前端和 Go 测试并构建前后端产物。

确认构建结果后部署：

```bash
./bin/release/reapply-provider-alias.sh origin/dev --deploy
```

部署只替换 `app/current` 和 `panel/current`，不会修改：

- `/data/cpa/CPA-Manager/data`
- `/data/cpa/CLIProxyAPI/config.yaml`

如果 `git rebase` 或测试失败，脚本会停止。发生代码冲突时，解决冲突后执行 `git add` 和 `git rebase --continue`，完成后再运行构建或部署命令；不要在未结束的 rebase 中重新启动脚本。

脚本默认使用 `origin/dev`。也可以传入 tag 或其他提交：

```bash
./bin/release/reapply-provider-alias.sh v1.15.0
```

部署前会检查健康接口和 provider alias 接口；校验失败时会尝试恢复部署前的 `app/current` 与 `panel/current`。
