import { defineConfig, type DefaultTheme } from 'vitepress';

const zhNav: DefaultTheme.NavItem[] = [
  { text: '首页', link: '/' },
  { text: '快速开始', link: '/guide/getting-started' },
  { text: '在线演示', link: 'https://seakee.github.io/CPA-Manager-Plus/' },
];

const enNav: DefaultTheme.NavItem[] = [
  { text: 'Home', link: '/en/' },
  { text: 'Get Started', link: '/en/guide/getting-started' },
  { text: 'Live Demo', link: 'https://seakee.github.io/CPA-Manager-Plus/' },
];

const zhSidebar: DefaultTheme.Sidebar = [
  {
    text: '开始',
    items: [
      { text: '文档首页', link: '/' },
      { text: '快速开始', link: '/guide/getting-started' },
      { text: '运行模型', link: '/guide/runtime-model' },
    ],
  },
  {
    text: '网关运行时',
    items: [
      { text: '网关配置', link: '/gateway/configuration' },
      { text: '提供商与兼容接口', link: '/gateway/providers' },
      { text: '客户端接入', link: '/gateway/clients' },
    ],
  },
  {
    text: '面板手册',
    items: [
      { text: '仪表盘', link: '/manual/dashboard' },
      { text: '配置中心', link: '/manual/configuration' },
      { text: 'AI 提供商', link: '/manual/ai-providers' },
      { text: '认证文件', link: '/manual/auth-files' },
      { text: 'OAuth 登录', link: '/manual/oauth' },
      { text: '配额管理', link: '/manual/quota' },
      { text: '请求监控', link: '/manual/monitoring' },
      { text: '账号处理队列', link: '/manual/account-actions' },
      { text: '用量分析', link: '/manual/usage-analytics' },
      { text: '模型价格', link: '/manual/model-prices' },
      { text: 'Codex 账号巡检', link: '/manual/codex-inspection' },
      { text: '插件管理', link: '/manual/plugins' },
      { text: '日志查看', link: '/manual/logs' },
      { text: '系统信息', link: '/manual/system' },
    ],
  },
  {
    text: '部署',
    items: [
      { text: '一键安装脚本', link: '/deployment/installer' },
      { text: 'Docker 部署', link: '/deployment/docker' },
      { text: '原生包部署', link: '/deployment/native' },
      { text: '原生包后台控制', link: '/deployment/native-background-control' },
      { text: '反向代理', link: '/deployment/reverse-proxy' },
    ],
  },
  {
    text: '运维',
    items: [
      { text: 'Manager Server 指南', link: '/operations/manager-server' },
      { text: '配置与数据目录', link: '/operations/configuration' },
      { text: '备份与恢复', link: '/operations/backup' },
      { text: '重置管理员密钥', link: '/operations/reset-admin-key' },
    ],
  },
  {
    text: '迁移',
    items: [
      { text: '从 CPA-Manager 迁移', link: '/migration/from-cpa-manager' },
    ],
  },
  {
    text: '排障',
    items: [
      { text: '请求监控排障', link: '/troubleshooting/request-monitoring' },
    ],
  },
  {
    text: '参考',
    items: [
      { text: '常见问题', link: '/reference/faq' },
      { text: '版本说明', link: '/reference/releases' },
    ],
  },
];

const enSidebar: DefaultTheme.Sidebar = [
  {
    text: 'Start',
    items: [
      { text: 'Docs Home', link: '/en/' },
      { text: 'Get Started', link: '/en/guide/getting-started' },
      { text: 'Runtime Model', link: '/en/guide/runtime-model' },
    ],
  },
  {
    text: 'Gateway Runtime',
    items: [
      { text: 'Gateway Configuration', link: '/en/gateway/configuration' },
      { text: 'Providers And Compatibility APIs', link: '/en/gateway/providers' },
      { text: 'Client Configuration', link: '/en/gateway/clients' },
    ],
  },
  {
    text: 'Panel Manual',
    items: [
      { text: 'Dashboard', link: '/en/manual/dashboard' },
      { text: 'Configuration', link: '/en/manual/configuration' },
      { text: 'AI Providers', link: '/en/manual/ai-providers' },
      { text: 'Auth Files', link: '/en/manual/auth-files' },
      { text: 'OAuth Login', link: '/en/manual/oauth' },
      { text: 'Quota', link: '/en/manual/quota' },
      { text: 'Monitoring', link: '/en/manual/monitoring' },
      { text: 'Account Action Queue', link: '/en/manual/account-actions' },
      { text: 'Usage Analytics', link: '/en/manual/usage-analytics' },
      { text: 'Model Prices', link: '/en/manual/model-prices' },
      { text: 'Codex Inspection', link: '/en/manual/codex-inspection' },
      { text: 'Plugin Management', link: '/en/manual/plugins' },
      { text: 'Logs', link: '/en/manual/logs' },
      { text: 'System', link: '/en/manual/system' },
    ],
  },
  {
    text: 'Deployment',
    items: [
      { text: 'One-Click Installer', link: '/en/deployment/installer' },
      { text: 'Docker Deployment', link: '/en/deployment/docker' },
      { text: 'Native Packages', link: '/en/deployment/native' },
      { text: 'Native Background Control', link: '/en/deployment/native-background-control' },
      { text: 'Reverse Proxy', link: '/en/deployment/reverse-proxy' },
    ],
  },
  {
    text: 'Operations',
    items: [
      { text: 'Manager Server Guide', link: '/en/operations/manager-server' },
      { text: 'Configuration And Data Directory', link: '/en/operations/configuration' },
      { text: 'Backup And Restore', link: '/en/operations/backup' },
      { text: 'Reset Admin Key', link: '/en/operations/reset-admin-key' },
    ],
  },
  {
    text: 'Migration',
    items: [
      { text: 'Migrate From CPA-Manager', link: '/en/migration/from-cpa-manager' },
    ],
  },
  {
    text: 'Troubleshooting',
    items: [
      { text: 'Request Monitoring Troubleshooting', link: '/en/troubleshooting/request-monitoring' },
    ],
  },
  {
    text: 'Reference',
    items: [
      { text: 'FAQ', link: '/en/reference/faq' },
      { text: 'Releases', link: '/en/reference/releases' },
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

const editLinkPattern =
  'https://github.com/seakee/CPA-Manager-Plus/edit/main/apps/docs/:path';

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
  socialLinks: [
    { icon: 'github', link: 'https://github.com/seakee/CPA-Manager-Plus' },
  ],
  footer: {
    message: 'Released under the MIT License.',
    copyright: 'Copyright 2026 Seakee.',
  },
};

export default defineConfig({
  title: 'CPA Manager Plus',
  description: 'CPA Manager Plus documentation',
  base: '/CPA-Manager-Plus/docs/',
  lastUpdated: true,
  themeConfig: commonThemeConfig,
  locales: {
    root: {
      label: '简体中文',
      lang: 'zh-CN',
      title: 'CPA Manager Plus',
      description: 'CPA Manager Plus 使用文档',
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
      description: 'CPA Manager Plus documentation',
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
