---
title: CPA 插件管理与插件商店
description: 使用 CPA Manager Plus 安装、选择版本、启用、配置和排查 CPA 插件，包括 GitHub Release、prerelease、手动 tag、沙箱化插件页面与主题同步协议。
---

# CPA 插件管理与插件商店

插件页面用于安装、升级、启用、配置和排查 CPA 插件。CPAMP 提供管理入口和插件页面容器，插件实际运行能力仍由 CPA runtime 和插件自身决定。

打开[插件管理演示](https://seakee.github.io/CPA-Manager-Plus/#/demo/plugins)可以查看虚构插件与商店数据。

## 页面入口

- **已安装插件**：版本、作者、状态、配置、OAuth、菜单和资源页面。
- **插件商店**：从 `plugins.store-sources` 获取可安装插件。

如果 `plugins.enabled` 关闭，单个插件即使标记为启用也不会运行。

插件入口只在 Manager 证明当前连接支持插件时出现（`X-CPA-SUPPORT-PLUGIN`）。Manager 不会因为某条路由存在就宣称该能力：它会读取所保存 CPA 连接自身的配置文档，并要求其中存在插件段。默认托管安装满足这一条件，因此无需额外配置。若入口消失，说明保存的连接无法证明插件支持——通常是旧版 CPA 没有插件命名空间，或 Manager 无法读取该连接。

`plugins.enabled` 关闭时入口仍然保留，因为该页面正持有把它重新打开的开关。只有插件自身的菜单会消失。

## 插件运行开关

默认托管安装由 Runtime Supervisor 把 `plugins.enabled` 和插件目录写入目标 CPA 的配置，首次安装向导中的选择决定初始值。安装完成后，托管安装可以在插件页顶部的“插件运行开关”中修改它：

- 一次开关操作是一次带基线的保存，走与网关配置页相同的日志、CAS 与读回路径（Manager API：`plugins/runtime`）；外部 CPA 连接与私有 H1 profile 会被服务端拒绝，因此这些 profile 不显示开关。
- 页面分别显示已保存值与运行值。CPA 异步重载配置，保存成功只表示配置已持久化。运行值显示为“未知，直到 CPA 重载”；本版本不观测 CPA 的重载，因此重载后也不会更新该读数。
- 如果上次写入结果未知，页面显示“读回结果”：它只读取 CPA 当前文档并记录结论，不会重发写入。
- 插件目录（`plugins.dir`）由 Runtime Supervisor 发布，页面不能修改它。

## 安装版本

插件商店支持三种版本模式：

- **Latest**：使用商店或仓库提供的默认最新版本。
- **GitHub Release**：读取 Release 列表并选择具体 tag；可以显式显示 prerelease。
- **Manual tag**：手动填写版本 tag，适合固定版本或 Release API 不可用时。

GitHub Release 列表可能受到仓库权限、网络、代理或 API rate limit 影响。读取失败时仍可使用 latest 或 manual tag。

安装或升级前确认：

1. 插件仓库和下载来源可信。
2. 版本支持当前 CPA、操作系统和架构。
3. 插件目录已持久化。
4. prerelease 只用于明确接受兼容风险的环境。

## 已安装插件操作

- 启用或停用插件。
- 编辑字符串、数字、布尔、数组或对象配置。
- 修改优先级。
- 发起插件提供的 OAuth 登录。
- 打开插件声明的页面资源。
- 删除插件配置或文件。

保存后如果提示需要重启，应按部署方式重启 CPA 或 Manager Server，不要只刷新浏览器。

## 插件页面的沙箱边界

插件页面在 Console 中以 iframe 打开，并始终运行在**不透明来源**（sandboxed opaque origin）中。这不是可选配置，而是 Manager 强制的响应契约：

- 每个 `/v0/resource/plugins/*` 响应都带 `Content-Security-Policy: sandbox allow-scripts allow-forms`，不含 `allow-same-origin`。
- iframe 自身使用 `sandbox="allow-scripts allow-forms"`，不授予弹窗、顶层导航、下载或同源访问。
- 响应只保留一组表示型响应头白名单（`Content-Type`、`Content-Length`、`Content-Encoding`、`Content-Language`、`Content-Disposition`、`ETag`、`Last-Modified`、`Cache-Control`、`Expires`、`Vary`、`Accept-Ranges`、`Content-Range` 以及经过校验的 `Location`）。插件发送的其余响应头一律丢弃，包括 `Set-Cookie`、`Clear-Site-Data`、`NEL`、`Report-To`、`Reporting-Endpoints`、`Strict-Transport-Security`、`Alt-Svc` 与 `Access-Control-Allow-Credentials`。跳转只能指向同一插件自身的前缀。

由此得到的限制：

- 插件页面**不能**读取 `localStorage`、`sessionStorage`、Cookie 或管理界面的 DOM，也不能调用管理 API。
- 沙箱给页面的是**不透明来源**，这才是失去管理权限的原因。它**并不**限制插件的网络访问：插件 CSP 不含任何 fetch 指令，因此页面可以从任意来源加载资源、向任意来源发送数据；插件资源以 `Access-Control-Allow-Origin: *` 提供，页面因此也能读取其他插件的资源。请把插件页面当作恰好在 Console 框架内运行的不可信第三方代码，而不是网络受限的页面。
- 依赖同源访问的插件（例如直接读取 Console 存储、依赖 Console 下的相对路径 API 或使用同源 Cookie 的页面）**不受支持**。界面会显示“无法显示该插件页面”的说明，而不会降级为同源加载。

插件资源命名空间在 CPA 侧是只读的：`pluginapi.ResourceRoute` 就是插件自己的页面（“the management UI menu label for this GET resource”），CPA 的插件宿主只对 GET 提供服务。需要接收输入的插件应注册 Management 路由——官方 Codex Invite 插件用 GET 从 `/v0/resource/plugins/codex-invite/invite` 提供页面，把发送动作 POST 到 `/v0/management/codex-invite/invite`。

写入会被哪一层拦下取决于部署形态，两层都会拒绝：

- **Full Docker（前置 Ingress）：** Ingress 自己接管插件资源命名空间，只有针对规范资源的裸 `GET` 或 `HEAD` 才会被转发给 Manager；其他方法在传输边缘即以 `404` 拒绝，既不会到达 Manager，也不会到达 Gateway。
- **仅 Manager（无 Ingress）：** Manager 的插件代理是通用反向代理，会按调用方使用的方法原样转发，因此写入会到达 CPA，由 CPA 拒绝（`404`，因为资源路由只支持 GET）。

Manager 不会自行追加方法限制，因为它并不拥有该命名空间。无论哪种情况，隔离本身都与请求方法无关：沙箱 CSP、响应头白名单、跳转限制与 Fetch Metadata 策略对写入与读取一视同仁。

Manager 还会对每个插件资源请求校验 Fetch Metadata：缺少 `Sec-Fetch-Site` 的请求一律返回 403，来自管理界面的 `script`、`worker`、`style`、`font` 等非文档型子资源请求也会被拒绝。拦下管理界面加载插件代码的是这项校验，而不是 CSP；管理站点的 CSP 并不按路径排除插件 URL。

不支持 Fetch Metadata 的老旧浏览器仍可使用插件管理，但插件页面无法打开。这是预期的降级。

## 主题同步协议

插件页面无法被宿主读取或写入 DOM，主题因此改为完全可选的消息协议。Console 在 iframe 加载完成、自身主题变化以及页面重新导航后，向 iframe 的 window 发送：

```json
{
  "type": "cpamp:plugin-theme",
  "version": 1,
  "theme": "light",
  "themeAttribute": "white",
  "hostAttribute": "data-cpamp-plugin-host",
  "variables": { "--primary-color": "#409eff" }
}
```

- 因为沙箱文档的来源是 `null`，`targetOrigin` 只能是 `*`。消息中不含任何凭证，只有管理界面已可见的主题变量。
- Plugin 只有在自己的文档中监听 `message` 并主动应用变量时才会跟随主题；不监听则保持自身样式。
- Console 只发送，不监听插件消息，也不向插件暴露任何回调或 API。
- `variables` 使用 CSS 自定义属性（包括 `--app-*`、`--bg-*`、`--text-*`、`--border-*`、`--primary-*` 等），可直接写入 `document.documentElement.style`。

最小实现：

```js
window.addEventListener('message', (event) => {
  const data = event.data;
  if (!data || data.type !== 'cpamp:plugin-theme' || data.version !== 1) return;
  const root = document.documentElement;
  root.setAttribute(data.hostAttribute, '');
  root.setAttribute('data-theme', data.themeAttribute);
  for (const [name, value] of Object.entries(data.variables)) {
    root.style.setProperty(name, value);
  }
});
```

## 插件页面和反向代理

插件页面资源通过 `/v0/resource/plugins/*` 访问，位置在站点根命名空间，而不是 `/api/v2/cpamp` 之下。自定义反向代理必须把该路径交给 CPAMP，否则页面可能空白。

常见问题：

- 插件未启用或没有注册菜单。
- 插件资源文件缺失。
- 反向代理路径错误。
- 插件版本与 CPA runtime 不兼容。
- 插件配置缺少必填项。
- 插件页面依赖同源访问：只支持 `/v0/resource/plugins/<插件 id>/` 前缀下的资源。

## CPA 版本升级回归清单

CPAMP 只支持 CPA v8 及以后版本。升级到新的固定 CPA 版本时，必须回归：

1. **插件 v0 接口**：`/v0/management/plugins`、`/v0/management/plugins/<id>/enabled`、`/v0/management/plugins/<id>/config`、`/v0/management/plugin-store` 仍存在且语义不变。插件启停与配置只在 `v0` 提供，`/v8/management` 不替代它们。
2. **管理路径归属**：`cpaBuiltinManagementPathHeads` 必须与 CPA 当前内置路由一致。"非内置即插件"的判断一旦错位，插件接口会被误判为内置或反之。
3. **`pluginabi` 版本**：`pluginabi.SchemaVersion`（v8.0.44 为 7）与插件商店中插件的兼容性；版本不一致会导致安装成功但无法加载。
4. **资源前缀**：`/v0/resource/plugins/<id>/` 前缀自 v8.0.13 至 v8.0.55 未变，升级后重新验证一次插件页面打开与主题同步。

## 安全边界

- 插件是可执行扩展，应像部署服务端代码一样审查来源、权限和更新。
- CPAMP 展示插件元数据并代理管理操作，不保证第三方插件安全或兼容。
- 未认证请求永远不会获得保存的 CPA 管理密钥；插件管理代发只在已认证管理员会话下发生。
- 不要把插件目录、配置或密钥放入公开日志和 Issue。
- 生产升级后观察[仪表盘](./dashboard.md)、[请求监控](./monitoring.md)和[日志](./logs.md)。
