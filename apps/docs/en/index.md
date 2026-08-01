---
layout: home
title: CPA / CLIProxyAPI Management Panel And Observability Docs
description: CPA Manager Plus documentation for CPA / CLIProxyAPI configuration, request monitoring, cost analytics, quota, Codex/xAI account health, plugins, deployment, and operations.

hero:
  name: CPA Manager Plus
  text: CPA Management And Observability Docs
  tagline: Operate CPA / CLIProxyAPI, persist requests, analyze cost, and manage Codex, Claude, and xAI quota and account health.
  actions:
    - theme: brand
      text: Get Started
      link: /en/guide/getting-started
    - theme: alt
      text: Choose A Panel
      link: /en/guide/choosing-a-panel
    - theme: alt
      text: Live Demo
      link: https://seakee.github.io/CPA-Manager-Plus/

features:
  - title: Install With Confidence
    details: Choose Lightweight Panel or Full Mode, then follow the recommended login and verification steps.
  - title: Manage Models And Accounts
    details: Add providers, OAuth, and auth files, then check quota and account state.
  - title: Understand Requests And Cost
    details: Find failed requests and analyze tokens, cost, latency, and callers.
---

<script setup>
import homePreview from '../images/home.png';
</script>

<figure class="cpamp-home-preview">
  <img :src="homePreview" alt="CPA Manager Plus dashboard screenshot" />
  <figcaption>CPA / CLIProxyAPI gateway management, request monitoring, cost analytics, and account health in one self-hosted panel.</figcaption>
</figure>

## Read By Task

<div class="cpamp-doc-grid">
  <section class="cpamp-doc-card">
    <h3>Get Started</h3>
    <p>Choose the right mode, then complete installation, login, and the first verification.</p>
    <ul>
      <li><a href="./guide/choosing-a-panel.html">Choose Lightweight Panel Or Full Mode</a></li>
      <li><a href="./guide/getting-started.html">Quick Start</a></li>
      <li><a href="./deployment/cpa-panel.html">Install Lightweight Panel</a></li>
      <li><a href="./deployment/installer.html">Install Full Mode</a></li>
    </ul>
  </section>
  <section class="cpamp-doc-card">
    <h3>Manage Models And Accounts</h3>
    <p>Handle providers, OAuth, auth files, and client setup for daily use.</p>
    <ul>
      <li><a href="./manual/ai-providers.html">AI Providers</a></li>
      <li><a href="./manual/auth-files.html">Auth Files</a></li>
      <li><a href="./manual/oauth.html">OAuth Login</a></li>
      <li><a href="./gateway/clients.html">Client Configuration</a></li>
    </ul>
  </section>
  <section class="cpamp-doc-card">
    <h3>Understand Requests And Cost</h3>
    <p>Use Dashboard to spot a problem, then investigate failures, cost, and account health.</p>
    <ul>
      <li><a href="./manual/dashboard.html">Dashboard</a></li>
      <li><a href="./manual/monitoring.html">Monitoring</a></li>
      <li><a href="./manual/usage-analytics.html">Usage Analytics</a></li>
      <li><a href="./manual/quota.html">Quota</a></li>
    </ul>
  </section>
  <section class="cpamp-doc-card">
    <h3>Maintain And Troubleshoot</h3>
    <p>Upgrade and back up safely, then troubleshoot monitoring, login, or network problems by symptom.</p>
    <ul>
      <li><a href="./operations/update.html">Upgrade CPAMP</a></li>
      <li><a href="./operations/backup.html">Backup And Restore</a></li>
      <li><a href="./troubleshooting/request-monitoring.html">Monitoring Has No Data</a></li>
      <li><a href="./reference/faq.html">FAQ</a></li>
    </ul>
  </section>
</div>

## Choose How To Use CPAMP

<div class="cpamp-mode-grid">
  <section class="cpamp-mode-card">
    <h3>CPAMP Lightweight Panel</h3>
    <p>Keep your existing CPA and replace only the management UI, with no additional service, database, or port.</p>
    <a href="./deployment/cpa-panel.html">Install Lightweight Panel</a>
  </section>
  <section class="cpamp-mode-card">
    <h3>CPAMP Full Mode</h3>
    <p>Use request history, cost analytics, account inspection, and automation through Docker or a native package.</p>
    <a href="./deployment/docker.html">Docker Deployment (Recommended)</a>
    <a href="./deployment/native.html">Native Package Deployment</a>
  </section>
</div>

## Preview The Interface

The Live Demo only previews the interface with fictional data. It is not a deployment or runtime mode and cannot connect to, manage, or monitor a real CPA instance.

[Open Live Demo](https://seakee.github.io/CPA-Manager-Plus/)
