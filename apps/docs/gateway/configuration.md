# 部署前准备 CPA

只有手动部署完整模式，或请求监控一直没有数据时，才需要直接检查本页配置。使用一键安装器完成“CPA + CPAMP”安装的用户通常可以跳过。

## 必须准备的两项配置

### 1. 允许 CPAMP 管理 CPA

```yaml
remote-management:
  secret-key: 'replace-with-a-long-random-management-key'
  allow-remote: true
```

- `secret-key` 就是 CPA Management Key。
- CPAMP 和 CPA 不在同一个进程时，需要确认 `allow-remote` 允许它们通信。
- 不要把 CPA 管理端口和密钥直接暴露到不可信网络。

### 2. 为完整模式发布请求数据

```yaml
usage-statistics-enabled: true
redis-usage-queue-retention-seconds: 60
```

轻量面板不需要请求采集。完整模式依赖这项配置显示请求历史和用量分析。先保持 60 秒默认值，只有采集经常中断时再提高。

## 如何确认准备完成

1. CPA 正常启动，没有配置解析错误。
2. CPAMP 可以使用 CPA 地址和 CPA Management Key 完成连接。
3. 发送一条真实请求后，完整模式的请求监控出现记录。

如果连接正常但没有请求数据，查看[请求监控为空](../troubleshooting/request-monitoring.md)。

## 日常配置在哪里修改

| 要做的事                      | 推荐入口                               |
| ----------------------------- | -------------------------------------- |
| 添加 Provider、模型或 API Key | [AI 提供商](../manual/ai-providers.md) |
| 登录、导入或禁用账号          | [认证文件](../manual/auth-files.md)    |
| OAuth 登录                    | [OAuth 登录](../manual/oauth.md)       |
| 调整 CPA 或 CPAMP 运行设置    | [配置中心](../manual/configuration.md) |

::: details 高级：认证目录、存储和配置归属

认证文件应保存在持久化目录中。尽量保持文件名或 `auth_index` 稳定，避免历史请求和账号状态失去关联。日常禁用账号时优先使用面板，不要直接删除文件。

CPA 可以使用本地文件或它支持的外部存储。无论选择哪一种，都要提前确认备份、权限和回滚方式。

CPA 保存 Provider、认证文件、OAuth、客户端 API Key、日志、插件和路由规则。CPAMP 完整模式保存 CPA 连接、请求历史、价格、别名、巡检和自动化状态。

:::
