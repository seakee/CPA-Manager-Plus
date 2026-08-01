---
title: CPA 插件管理与插件商店
description: 使用 CPA Manager Plus 安装、选择版本、启用、配置和排查 CPA 插件，包括 GitHub Release、prerelease、手动 tag 和插件页面。
---

# CPA 插件管理与插件商店

插件页面用于安装、升级、启用、配置和排查 CPA 插件。CPAMP 提供管理入口和插件页面容器，插件实际运行能力仍由 CPA runtime 和插件自身决定。

打开[插件管理演示](https://seakee.github.io/CPA-Manager-Plus/#/demo/plugins)可以查看虚构插件与商店数据。

## 页面入口

- **已安装插件**：版本、作者、状态、配置、OAuth、菜单和资源页面。
- **插件商店**：从 `plugins.store-sources` 获取可安装插件。

如果 `plugins.enabled` 关闭，单个插件即使标记为启用也不会运行。

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

## 插件页面和反向代理

插件页面资源通过 `/v0/resource/plugins/*` 访问。自定义反向代理必须把该路径交给 CPAMP，否则页面可能空白。

常见问题：

- 插件未启用或没有注册菜单。
- 插件资源文件缺失。
- 反向代理路径错误。
- 插件版本与 CPA runtime 不兼容。
- 插件配置缺少必填项。

## 安全边界

- 插件是可执行扩展，应像部署服务端代码一样审查来源、权限和更新。
- CPAMP 展示插件元数据并代理管理操作，不保证第三方插件安全或兼容。
- 不要把插件目录、配置或密钥放入公开日志和 Issue。
- 生产升级后观察[仪表盘](./dashboard.md)、[请求监控](./monitoring.md)和[日志](./logs.md)。
