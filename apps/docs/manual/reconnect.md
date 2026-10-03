---
title: 自助重连
description: 当 Claude、Codex、Antigravity、xAI 或 Muse 登录失效时，通过 Webhook 向登录所有者发送一次性链接，由其自行重新登录，无需管理员代为操作。
---

# 自助重连

CPA 中的部分 OAuth 登录只能通过重新登录恢复，例如 refresh token 被吊销后。自助重连会通知该登录的所有者并发送一次性链接，由其自行登录，CPAMP 再把结果交给 CPA，管理员无需代为登录。

此功能需要完整模式（Manager Server），默认关闭。

## 工作方式

1. 每个检查间隔，Manager Server 读取 CPA 认证文件列表，查找状态信息包含 `unauthorized`、`invalid_grant` 或 `invalid grant` 且未禁用的 Claude、Codex、Antigravity、xAI 或 Muse 登录。
2. 登录需持续异常 10 分钟才会通知，短暂失败后自行恢复的情况会被忽略。
3. 按登录邮箱匹配所有者。CPAMP 向你的 Webhook 发送一条消息，附带指向 `<公开地址>/management.html#/reconnect/<token>` 的一次性链接。
4. 所有者无需登录 CPAMP 即可打开链接：
   - **Claude、Codex、Antigravity**：点击“连接”，在提供商网站登录，然后粘贴跳转后的 `localhost` 页面地址。该页面无法打开，这是正常的。
   - **xAI、Muse**：在提供商网站批准请求，页面会自动检测授权。
5. 成功后，原先失效的凭证会被删除。如果登录的账号与要求的不一致，所有者自己的链接保持有效，并提示其使用正确的账号重试。另一个账号的登录仍然有用，因此会被保留：
   - 如果该账号正在等待重连，其请求会以 **已重连** 关闭，原先失效的凭证会被删除；若之前已通知过其所有者，会发送无需处理的通知。
   - 如果该账号尚无登录，会加入共享池，其未处理的邀请随之关闭，并向其所有者发送欢迎消息。
   - 链接所有者看到的错误信息末尾会说明发生了哪种情况。
6. 如果所有者操作前登录已自行恢复，CPAMP 会发送一条无需处理的通知。

## 设置

打开 **配置中心 → Manager Server**，找到 **自助重连**。修改通过页面浮动栏的 **保存** 按钮与其他 Manager Server 设置一起保存。

- **面板公开地址**：用户访问此面板的地址，启用前必填。首次打开时会自动填入当前访问面板的地址。
- **通知 Webhook 地址**：必须使用 `https`，加密保存，API 不会返回。留空则保留已保存的值。
- **发送者名称**：显示在消息中。
- **检查间隔**：1–60 分钟。
- **链接有效期**：1–72 小时。
- **提醒**：未重连的所有者每 1–6 小时收到一次提醒，仅在所选时区的指定时段内发送。每次提醒附带新链接，旧链接随即失效。

## Webhook 负载

每条消息是一个 JSON `POST`：

```json
{
  "sendTo": "owner@example.com",
  "body": "<p>HTML message</p>",
  "type": "reconnect",
  "provider": "claude",
  "email": "owner@example.com",
  "link": "https://cpamp.example.com/management.html#/reconnect/…",
  "expires_at": "2026-01-01T12:00:00Z",
  "reason": "unauthorized",
  "followup": false
}
```

`sendTo` 和 `body` 足以对接 Microsoft Teams Power Automate 流程或 Slack 工作流，其余字段可用于自行路由或排版。`type` 取值为 `reconnect`、`reconnect_followup`（提醒）、`reconnect_resolved`（无需处理通知）、`reconnect_test`、`reconnect_invite` 或 `reconnect_welcome`（通过他人链接加入的登录）。

## 手动发送链接

在 **发送链接** 中选择登录类型并填写邮箱：

- **登录正常**：发送测试链接，未使用时会自动关闭。
- **没有此类型的登录**：发送邀请。
- **登录异常**：发送重连请求；若已通知过则不发送。

## 请求列表

列表显示最近 30 天的请求及状态，默认每页 20 条：

- **等待中**：所有者尚未重连。
- **已重连**：所有者通过链接完成重连。
- **已自行恢复**：登录未使用链接即恢复。
- **未使用**：管理员发送的链接过期未使用。

## 安全说明

- 链接随机生成、仅可使用一次，且只保存哈希。
- 公开链接接口按 IP 限流，不暴露内部错误。
- 页面只能重连签发时指定的登录类型和邮箱。
