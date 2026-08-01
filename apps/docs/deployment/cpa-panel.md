---
title: CPAMP 轻量面板
description: 不部署 Manager Server，直接让 CPA 在 8317 端口托管 CPA Manager Plus，作为官方 Management Center 的轻量增强替代 WebUI。
---

# CPAMP 轻量面板

CPAMP 轻量面板由 CPA 直接托管，用 CPA Manager Plus 的界面替换官方 Management Center。它不需要 Manager Server、SQLite、额外容器或新的端口，适合希望保持轻量部署但更换管理 UI 的用户。

## 最短安装流程

1. 在 CPA `config.yaml` 中设置 `panel-github-repository`。
2. 确认管理面板没有被禁用。
3. 重启或重新加载 CPA。
4. 打开 `http://<cpa-host>:8317/management.html`。
5. 使用 CPA Management Key 登录。

下面给出完整配置和安全说明；已经熟悉 CPA 配置的用户只需要完成以上步骤。

```text
浏览器
  -> CPA :8317/management.html
      -> CPAMP 单文件面板
      -> CPA Management API
```

## 适合什么场景

- 已经运行 CPA / CLIProxyAPI。
- 不喜欢官方面板的界面或信息组织。
- 希望继续只维护一个 CPA 进程和一个管理端口。
- 主要管理 Provider、认证文件、OAuth、API Key、Quota、日志、插件和配置。
- 暂时不需要持久化请求历史、成本分析或服务端自动化。

## 前置条件

- 推荐使用当前版本 CPA；CPAMP README 当前推荐 CPA `v7.1.39+`。
- CPA 必须配置 `remote-management.secret-key`，否则 Management API 和面板无法使用。
- `remote-management.disable-control-panel` 必须为 `false`。
- CPA 需要能够访问 GitHub Release，或通过它已经配置的代理下载 `management.html`。

## 配置 CPA

编辑 CPA 的 `config.yaml`：

```yaml
remote-management:
  # 安全默认值。只有需要从非 localhost 访问时才改为 true。
  allow-remote: false

  # CPA Management Key。不要把真实密钥提交到 Git。
  secret-key: 'replace-with-your-management-key'

  # 必须允许 CPA 托管管理面板。
  disable-control-panel: false

  # false 表示允许 CPA 定期检查并更新面板。
  disable-auto-update-panel: false

  # 让 CPA 从 CPAMP Release 下载 management.html。
  panel-github-repository: 'https://github.com/seakee/CPA-Manager-Plus'
```

`allow-remote: true` 会开放远程 Management API。只在可信网络、VPN 或受保护的反向代理后使用，不要把管理端口和密钥暴露到不可信网络。

## 启动和登录

1. 保存配置并重启或重载 CPA。
2. 打开：

```text
http://<cpa-host>:8317/management.html
```

3. 使用 CPA Management Key 登录。
4. 确认 Dashboard、配置中心、Provider、认证文件、OAuth、Quota 和日志能够正常读取。

CPA 首次访问时会从指定 GitHub 仓库的最新 Release 查找名为 `management.html` 的资源，并缓存到 CPA 工作目录。

## 更新与缓存

当 `disable-auto-update-panel: false` 时，CPA 会定期检查最新面板。升级后仍显示旧界面时：

1. 确认 `panel-github-repository` 仍指向 CPA Manager Plus。
2. 停止 CPA。
3. 删除 CPA 工作目录中的面板缓存：

```bash
rm static/management.html
```

4. 重新启动 CPA 并访问 `/management.html`。

如果启用了 `disable-auto-update-panel: true`，CPA 只会在缓存文件不存在时下载面板，不会定期自动更新。

## 轻量面板支持什么

| 能力                                              | CPAMP 轻量面板                  |
| ------------------------------------------------- | ------------------------------- |
| CPA 配置、Provider、认证文件、OAuth、Quota 和日志 | 支持                            |
| API Key、模型别名、优先级和插件管理               | 支持，取决于 CPA API 与插件路径 |
| 浏览器本地账号检查                                | 支持页面提供的本地能力          |
| SQLite 请求历史与请求监控                         | 不支持                          |
| 用量与成本分析、模型价格、API Key 别名            | 不支持                          |
| 服务端账号巡检、配额冷却和账号处理队列            | 不支持                          |
| 完整模式的备份、迁移和维护工具                    | 不支持                          |

轻量面板不会连接或读取独立运行的 Manager Server。启动 Manager Server 不会让 `:8317/management.html` 自动获得完整能力。

## 升级到 CPAMP 完整模式

需要请求历史、成本分析或服务端自动化时：

1. 按 [Docker 部署](./docker.md) 或 [原生包部署](./native.md) 启动 Manager Server。
2. 打开：

```text
http://<cpamp-host>:18317/management.html
```

3. 使用 CPAMP Admin Key 完成 setup 或登录。
4. 配置 CPA 地址、CPA Management Key 和请求采集。

CPA 端口上的轻量面板可以继续保留，但完整功能只从 Manager Server 面板入口使用。

## 常见问题

- 打不开面板：确认 `secret-key` 非空且 `disable-control-panel` 为 `false`。
- 仍显示官方面板：检查 `panel-github-repository` 拼写并清理缓存。
- 远程访问返回拒绝：检查 `allow-remote`、防火墙和反向代理。
- 没有请求监控：这是轻量模式的预期行为；使用 Manager Server 面板。
