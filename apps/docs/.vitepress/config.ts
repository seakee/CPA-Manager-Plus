import { defineConfig, type DefaultTheme } from 'vitepress';

const zhNav: DefaultTheme.NavItem[] = [
  { text: '首页', link: '/' },
  { text: '选择面板', link: '/guide/choosing-a-panel' },
  { text: '轻量面板', link: '/deployment/cpa-panel' },
  { text: '快速开始', link: '/guide/getting-started' },
  { text: '在线演示', link: 'https://seakee.github.io/CPA-Manager-Plus/' },
];

const enNav: DefaultTheme.NavItem[] = [
  { text: 'Home', link: '/en/' },
  { text: 'Choose A Panel', link: '/en/guide/choosing-a-panel' },
  { text: 'Lightweight Panel', link: '/en/deployment/cpa-panel' },
  { text: 'Get Started', link: '/en/guide/getting-started' },
  { text: 'Live Demo', link: 'https://seakee.github.io/CPA-Manager-Plus/' },
];

const zhSidebar: DefaultTheme.Sidebar = [
  {
    text: '开始使用',
    items: [
      { text: '文档首页', link: '/' },
      { text: '选择适合的模式', link: '/guide/choosing-a-panel' },
      { text: '快速开始', link: '/guide/getting-started' },
      { text: '安装轻量面板', link: '/deployment/cpa-panel' },
      { text: '安装完整模式', link: '/deployment/installer' },
    ],
  },
  {
    text: '日常使用',
    items: [
      { text: '仪表盘', link: '/manual/dashboard' },
      { text: 'AI 提供商', link: '/manual/ai-providers' },
      { text: '认证文件', link: '/manual/auth-files' },
      { text: 'OAuth 登录', link: '/manual/oauth' },
      { text: '请求监控', link: '/manual/monitoring' },
      { text: '用量分析', link: '/manual/usage-analytics' },
      { text: '配额管理', link: '/manual/quota' },
      { text: '账号巡检（Codex / xAI）', link: '/manual/codex-inspection' },
      { text: '账号处理队列', link: '/manual/account-actions' },
      { text: '插件管理', link: '/manual/plugins' },
      { text: '配置中心', link: '/manual/configuration' },
    ],
  },
  {
    text: '维护与排障',
    items: [
      { text: '更新 CPAMP', link: '/operations/update' },
      { text: '备份与恢复', link: '/operations/backup' },
      { text: '重置管理员密钥', link: '/operations/reset-admin-key' },
      { text: '请求监控为空', link: '/troubleshooting/request-monitoring' },
      { text: '日志查看', link: '/manual/logs' },
      { text: '系统信息', link: '/manual/system' },
    ],
  },
  {
    text: '高级配置',
    collapsed: true,
    items: [
      { text: 'CPAMP 与 CPA 如何协作', link: '/guide/runtime-model' },
      { text: 'Docker 手动部署', link: '/deployment/docker' },
      { text: '原生包部署', link: '/deployment/native' },
      { text: '原生包后台控制', link: '/deployment/native-background-control' },
      { text: '反向代理', link: '/deployment/reverse-proxy' },
      { text: 'Manager Server', link: '/operations/manager-server' },
      { text: '准备 CPA 网关', link: '/gateway/configuration' },
      { text: '客户端接入', link: '/gateway/clients' },
      { text: '配置与数据目录', link: '/operations/configuration' },
      { text: '模型价格', link: '/manual/model-prices' },
    ],
  },
  {
    text: '参考',
    collapsed: true,
    items: [
      { text: '常见问题', link: '/reference/faq' },
      { text: '能力矩阵', link: '/reference/capability-matrix' },
      { text: '提供商与兼容接口', link: '/gateway/providers' },
      { text: '版本说明', link: '/reference/releases' },
      { text: '从 CPA-Manager 迁移', link: '/migration/from-cpa-manager' },
    ],
  },
];

const enSidebar: DefaultTheme.Sidebar = [
  {
    text: 'Get Started',
    items: [
      { text: 'Docs Home', link: '/en/' },
      { text: 'Choose A Mode', link: '/en/guide/choosing-a-panel' },
      { text: 'Quick Start', link: '/en/guide/getting-started' },
      { text: 'Install Lightweight Panel', link: '/en/deployment/cpa-panel' },
      { text: 'Install Full Mode', link: '/en/deployment/installer' },
    ],
  },
  {
    text: 'Daily Use',
    items: [
      { text: 'Dashboard', link: '/en/manual/dashboard' },
      { text: 'AI Providers', link: '/en/manual/ai-providers' },
      { text: 'Auth Files', link: '/en/manual/auth-files' },
      { text: 'OAuth Login', link: '/en/manual/oauth' },
      { text: 'Monitoring', link: '/en/manual/monitoring' },
      { text: 'Usage Analytics', link: '/en/manual/usage-analytics' },
      { text: 'Quota', link: '/en/manual/quota' },
      { text: 'Account Inspection (Codex / xAI)', link: '/en/manual/codex-inspection' },
      { text: 'Account Action Queue', link: '/en/manual/account-actions' },
      { text: 'Plugin Management', link: '/en/manual/plugins' },
      { text: 'Configuration', link: '/en/manual/configuration' },
    ],
  },
  {
    text: 'Maintenance And Troubleshooting',
    items: [
      { text: 'Upgrade CPAMP', link: '/en/operations/update' },
      { text: 'Backup And Restore', link: '/en/operations/backup' },
      { text: 'Reset Admin Key', link: '/en/operations/reset-admin-key' },
      {
        text: 'Monitoring Has No Data',
        link: '/en/troubleshooting/request-monitoring',
      },
      { text: 'Logs', link: '/en/manual/logs' },
      { text: 'System', link: '/en/manual/system' },
    ],
  },
  {
    text: 'Advanced Configuration',
    collapsed: true,
    items: [
      { text: 'How CPAMP Works With CPA', link: '/en/guide/runtime-model' },
      { text: 'Manual Docker Deployment', link: '/en/deployment/docker' },
      { text: 'Native Packages', link: '/en/deployment/native' },
      { text: 'Native Background Control', link: '/en/deployment/native-background-control' },
      { text: 'Reverse Proxy', link: '/en/deployment/reverse-proxy' },
      { text: 'Manager Server', link: '/en/operations/manager-server' },
      { text: 'Prepare The CPA Gateway', link: '/en/gateway/configuration' },
      { text: 'Client Configuration', link: '/en/gateway/clients' },
      {
        text: 'Configuration And Data Directory',
        link: '/en/operations/configuration',
      },
      { text: 'Model Prices', link: '/en/manual/model-prices' },
    ],
  },
  {
    text: 'Reference',
    collapsed: true,
    items: [
      { text: 'FAQ', link: '/en/reference/faq' },
      { text: 'Capability Matrix', link: '/en/reference/capability-matrix' },
      { text: 'Providers And Compatibility APIs', link: '/en/gateway/providers' },
      { text: 'Releases', link: '/en/reference/releases' },
      { text: 'Migrate From CPA-Manager', link: '/en/migration/from-cpa-manager' },
    ],
  },
];

const zhSearchTranslations = {
  button: {
    buttonText: '搜索',
    buttonAriaLabel: '搜索文档',
  },
  modal: {
    noResultsText: '没有找到相关结果',
    resetButtonTitle: '清除查询条件',
    displayDetails: '显示详细列表',
    footer: {
      selectText: '选择',
      navigateText: '切换',
      closeText: '关闭',
    },
  },
};

const enSearchTranslations = {
  button: {
    buttonText: 'Search',
    buttonAriaLabel: 'Search docs',
  },
  modal: {
    noResultsText: 'No results found',
    resetButtonTitle: 'Clear search query',
    displayDetails: 'Display detailed list',
    footer: {
      selectText: 'to select',
      navigateText: 'to navigate',
      closeText: 'to close',
    },
  },
};

const editLinkPattern = 'https://github.com/seakee/CPA-Manager-Plus/edit/main/apps/docs/:path';

const commonThemeConfig: DefaultTheme.Config = {
  search: {
    provider: 'local',
    options: {
      locales: {
        root: {
          translations: zhSearchTranslations,
        },
        en: {
          translations: enSearchTranslations,
        },
      },
    },
  },
  socialLinks: [{ icon: 'github', link: 'https://github.com/seakee/CPA-Manager-Plus' }],
  footer: {
    message: 'Released under the MIT License.',
    copyright: 'Copyright 2026 Seakee.',
  },
};

export default defineConfig({
  title: 'CPA Manager Plus Docs',
  description:
    'CPA and CLIProxyAPI management panel documentation for request monitoring, usage analytics, cost, quota, account health, plugins, deployment, and operations.',
  base: '/CPA-Manager-Plus/docs/',
  lastUpdated: true,
  sitemap: {
    hostname: 'https://seakee.github.io/CPA-Manager-Plus/docs/',
  },
  transformPageData(pageData) {
    const isEnglish = pageData.relativePath.startsWith('en/');
    const siteTitle = 'CPA Manager Plus';
    const title = pageData.title ? `${pageData.title} | ${siteTitle}` : siteTitle;
    const description =
      pageData.description ||
      (isEnglish
        ? 'CPA / CLIProxyAPI management panel and observability documentation for requests, cost, quota, failures, and account health.'
        : 'CPA / CLIProxyAPI 管理面板与可观测性文档，覆盖请求监控、成本、配额、失败诊断和账号健康。');
    const pagePath = pageData.relativePath.replace(/index\.md$/, '').replace(/\.md$/, '.html');
    const canonical = `https://seakee.github.io/CPA-Manager-Plus/docs/${pagePath}`;

    pageData.frontmatter.head ??= [];
    pageData.frontmatter.head.push(
      ['link', { rel: 'canonical', href: canonical }],
      ['meta', { property: 'og:type', content: 'article' }],
      ['meta', { property: 'og:title', content: title }],
      ['meta', { property: 'og:description', content: description }],
      ['meta', { property: 'og:url', content: canonical }],
      ['meta', { name: 'twitter:card', content: 'summary_large_image' }],
      ['meta', { name: 'twitter:title', content: title }],
      ['meta', { name: 'twitter:description', content: description }]
    );
  },
  themeConfig: commonThemeConfig,
  locales: {
    root: {
      label: '简体中文',
      lang: 'zh-CN',
      title: 'CPA Manager Plus',
      description:
        'CPA / CLIProxyAPI 管理面板使用文档：网关配置、请求监控、成本分析、配额、账号健康、插件、部署与运维。',
      themeConfig: {
        nav: zhNav,
        sidebar: zhSidebar,
        editLink: {
          pattern: editLinkPattern,
          text: '编辑此页',
        },
        lastUpdated: {
          text: '最后更新',
        },
        outline: {
          label: '本页目录',
        },
        docFooter: {
          prev: '上一页',
          next: '下一页',
        },
        sidebarMenuLabel: '菜单',
        returnToTopLabel: '返回顶部',
        langMenuLabel: '切换语言',
        darkModeSwitchLabel: '外观',
        lightModeSwitchTitle: '切换到浅色模式',
        darkModeSwitchTitle: '切换到深色模式',
      },
    },
    en: {
      label: 'English',
      lang: 'en-US',
      link: '/en/',
      title: 'CPA Manager Plus',
      description:
        'CPA / CLIProxyAPI management panel docs for gateway configuration, request monitoring, cost analytics, quota, account health, plugins, deployment, and operations.',
      themeConfig: {
        nav: enNav,
        sidebar: enSidebar,
        editLink: {
          pattern: editLinkPattern,
          text: 'Edit this page',
        },
        lastUpdated: {
          text: 'Last updated',
        },
        outline: {
          label: 'On this page',
        },
        docFooter: {
          prev: 'Previous page',
          next: 'Next page',
        },
        sidebarMenuLabel: 'Menu',
        returnToTopLabel: 'Return to top',
        langMenuLabel: 'Change language',
        darkModeSwitchLabel: 'Appearance',
        lightModeSwitchTitle: 'Switch to light mode',
        darkModeSwitchTitle: 'Switch to dark mode',
      },
    },
  },
});
