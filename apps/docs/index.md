---
layout: home
title: CPA / CLIProxyAPI 管理面板与可观测性文档
description: CPA Manager Plus 使用文档，覆盖 CPA / CLIProxyAPI 配置、请求监控、成本分析、配额、Codex/xAI 账号健康、插件、部署与运维。

hero:
  name: CPA Manager Plus
  text: CPA 管理面板与可观测性文档
  tagline: 管理 CPA / CLIProxyAPI，持久化请求，分析成本，并处理 Codex、Claude 与 xAI 的配额和账号健康。
  actions:
    - theme: brand
      text: 快速开始
      link: /guide/getting-started
    - theme: alt
      text: 选择面板
      link: /guide/choosing-a-panel
    - theme: alt
      text: 在线演示
      link: https://seakee.github.io/CPA-Manager-Plus/

features:
  - title: 快速完成安装
    details: 先选择轻量面板或完整模式，再按推荐步骤完成登录和验证。
  - title: 管理模型与账号
    details: 添加 Provider、OAuth 和认证文件，检查配额与账号状态。
  - title: 查看请求与成本
    details: 定位失败请求，分析 Token、成本、延迟和调用方。
---

<script setup>
import homePreview from './images/home-zh.png';
</script>

<figure class="cpamp-home-preview">
  <img :src="homePreview" alt="CPA Manager Plus 仪表盘截图" />
  <figcaption>一个自托管 CPA / CLIProxyAPI 面板中完成网关管理、请求监控、成本分析和账号健康运维。</figcaption>
</figure>

## 按任务阅读

<div class="cpamp-doc-grid">
  <section class="cpamp-doc-card">
    <h3>开始使用</h3>
    <p>先确定适合自己的模式，再完成安装、登录和第一次验证。</p>
    <ul>
      <li><a href="./guide/choosing-a-panel.html">选择轻量面板或完整模式</a></li>
      <li><a href="./guide/getting-started.html">快速开始</a></li>
      <li><a href="./deployment/cpa-panel.html">安装轻量面板</a></li>
      <li><a href="./deployment/installer.html">安装完整模式</a></li>
    </ul>
  </section>
  <section class="cpamp-doc-card">
    <h3>管理模型与账号</h3>
    <p>完成 Provider、OAuth、认证文件和客户端接入等日常配置。</p>
    <ul>
      <li><a href="./manual/ai-providers.html">AI 提供商</a></li>
      <li><a href="./manual/auth-files.html">认证文件</a></li>
      <li><a href="./manual/oauth.html">OAuth 登录</a></li>
      <li><a href="./gateway/clients.html">客户端接入</a></li>
    </ul>
  </section>
  <section class="cpamp-doc-card">
    <h3>查看请求与成本</h3>
    <p>从仪表盘发现异常，再定位失败、成本和账号健康问题。</p>
    <ul>
      <li><a href="./manual/dashboard.html">仪表盘</a></li>
      <li><a href="./manual/monitoring.html">请求监控</a></li>
      <li><a href="./manual/usage-analytics.html">用量分析</a></li>
      <li><a href="./manual/quota.html">配额管理</a></li>
    </ul>
  </section>
  <section class="cpamp-doc-card">
    <h3>维护与排障</h3>
    <p>安全更新和备份，在监控、登录或网络异常时按症状处理。</p>
    <ul>
      <li><a href="./operations/update.html">更新 CPAMP</a></li>
      <li><a href="./operations/backup.html">备份与恢复</a></li>
      <li><a href="./troubleshooting/request-monitoring.html">请求监控为空</a></li>
      <li><a href="./reference/faq.html">常见问题</a></li>
    </ul>
  </section>
</div>

## 选择使用方式

<div class="cpamp-mode-grid">
  <section class="cpamp-mode-card">
    <h3>CPAMP 轻量面板</h3>
    <p>已有 CPA，只替换管理界面，不增加服务、数据库或端口。</p>
    <a href="./deployment/cpa-panel.html">安装轻量面板</a>
  </section>
  <section class="cpamp-mode-card">
    <h3>CPAMP 完整模式</h3>
    <p>需要请求历史、成本分析、账号巡检和自动化；可用 Docker 或原生包安装。</p>
    <a href="./deployment/docker.html">Docker 部署（推荐）</a>
    <a href="./deployment/native.html">原生包部署</a>
  </section>
</div>

## 先体验界面

在线演示只用于浏览使用虚构数据的界面，不是部署或运行模式，也不能连接、管理或监控真实 CPA。

[打开在线演示](https://seakee.github.io/CPA-Manager-Plus/)
