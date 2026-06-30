import {
  ReactNode,
  SVGProps,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from 'react';
import { NavLink, useLocation, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { PageTransition } from '@/components/common/PageTransition';
import { MainRoutes } from '@/router/MainRoutes';
import {
  IconSidebarAuthFiles,
  IconSidebarConfig,
  IconSidebarDashboard,
  IconSidebarInspection,
  IconSidebarLogs,
  IconSidebarMonitor,
  IconSidebarOauth,
  IconSidebarPlugins,
  IconSidebarProviders,
  IconSidebarQuota,
  IconSidebarSystem,
  IconSidebarUsage,
} from '@/components/ui/icons';
import { INLINE_LOGO_JPEG } from '@/assets/logoInline';
import {
  useAuthStore,
  useConfigStore,
  useLanguageStore,
  useNotificationStore,
  useThemeStore,
  useVisualEffectsStore,
} from '@/stores';
import { pluginsApi } from '@/services/api';
import {
  collectPluginResourceEntries,
  isPluginManagementNavVisible,
  isPluginResourceNavVisible,
  PLUGIN_RESOURCES_REFRESH_EVENT,
  resolvePluginAssetURL,
  type PluginResourceEntry,
} from '@/features/plugins/pluginResources';
import { triggerHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { isFileLogsAvailable } from '@/features/logs/logFeatureAvailability';
import { getDemoLogoutPath, prefixRouteBase, stripRouteBase } from '@/features/demo/demoMode';
import { LANGUAGE_LABEL_KEYS, LANGUAGE_ORDER, STORAGE_KEY_SIDEBAR } from '@/utils/constants';
import { isSupportedLanguage } from '@/utils/language';
import type { Theme, VisualEffectsMode } from '@/types';

const SIDEBAR_ICON_SIZE = 20;

const sidebarIcons: Record<string, ReactNode> = {
  dashboard: <IconSidebarDashboard size={SIDEBAR_ICON_SIZE} />,
  aiProviders: <IconSidebarProviders size={SIDEBAR_ICON_SIZE} />,
  authFiles: <IconSidebarAuthFiles size={SIDEBAR_ICON_SIZE} />,
  oauth: <IconSidebarOauth size={SIDEBAR_ICON_SIZE} />,
  quota: <IconSidebarQuota size={SIDEBAR_ICON_SIZE} />,
  usageAnalytics: <IconSidebarUsage size={SIDEBAR_ICON_SIZE} />,
  codexInspection: <IconSidebarInspection size={SIDEBAR_ICON_SIZE} />,
  monitoring: <IconSidebarMonitor size={SIDEBAR_ICON_SIZE} />,
  plugins: <IconSidebarPlugins size={SIDEBAR_ICON_SIZE} />,
  config: <IconSidebarConfig size={SIDEBAR_ICON_SIZE} />,
  logs: <IconSidebarLogs size={SIDEBAR_ICON_SIZE} />,
  system: <IconSidebarSystem size={SIDEBAR_ICON_SIZE} />,
};

// Header action icons - smaller size for header buttons
const headerIconProps: SVGProps<SVGSVGElement> = {
  width: 16,
  height: 16,
  viewBox: '0 0 24 24',
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 2,
  strokeLinecap: 'round',
  strokeLinejoin: 'round',
  'aria-hidden': 'true',
  focusable: 'false',
};

const headerIcons = {
  refresh: (
    <svg {...headerIconProps}>
      <path d="M21 12a9 9 0 1 1-9-9c2.52 0 4.93 1 6.74 2.74L21 8" />
      <path d="M21 3v5h-5" />
    </svg>
  ),
  menu: (
    <svg {...headerIconProps}>
      <path d="M4 7h16" />
      <path d="M4 12h16" />
      <path d="M4 17h16" />
    </svg>
  ),
  close: (
    <svg {...headerIconProps}>
      <path d="M18 6 6 18" />
      <path d="m6 6 12 12" />
    </svg>
  ),
  sidebarCollapse: (
    <svg {...headerIconProps}>
      <rect x="3" y="4" width="18" height="16" rx="2" />
      <path d="M9 4v16" />
      <path d="m16 9-3 3 3 3" />
    </svg>
  ),
  sidebarExpand: (
    <svg {...headerIconProps}>
      <rect x="3" y="4" width="18" height="16" rx="2" />
      <path d="M9 4v16" />
      <path d="m13 9 3 3-3 3" />
    </svg>
  ),
  language: (
    <svg {...headerIconProps}>
      <circle cx="12" cy="12" r="10" />
      <path d="M2 12h20" />
      <path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z" />
    </svg>
  ),
  sun: (
    <svg {...headerIconProps}>
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2" />
      <path d="M12 20v2" />
      <path d="m4.93 4.93 1.41 1.41" />
      <path d="m17.66 17.66 1.41 1.41" />
      <path d="M2 12h2" />
      <path d="M20 12h2" />
      <path d="m6.34 17.66-1.41 1.41" />
      <path d="m19.07 4.93-1.41 1.41" />
    </svg>
  ),
  moon: (
    <svg {...headerIconProps}>
      <path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9z" />
    </svg>
  ),
  autoTheme: (
    <svg {...headerIconProps}>
      <rect x="4" y="5" width="16" height="11" rx="2" />
      <path d="M8 21h8" />
      <path d="M12 16v5" />
      <path d="M9 11a3 3 0 0 1 5.2-2" />
      <path d="M14.5 7v2h-2" />
      <path d="M15 11a3 3 0 0 1-5.2 2" />
      <path d="M9.5 15v-2h2" />
    </svg>
  ),
  visualEffectsFull: (
    <svg {...headerIconProps}>
      <path d="m12 3 1.85 5.15L19 10l-5.15 1.85L12 17l-1.85-5.15L5 10l5.15-1.85L12 3z" />
      <path d="M5 3v4" />
      <path d="M3 5h4" />
      <path d="M19 17v4" />
      <path d="M17 19h4" />
    </svg>
  ),
  visualEffectsReduced: (
    <svg {...headerIconProps}>
      <path d="M4 14a8 8 0 0 1 16 0" />
      <path d="M12 14l4-5" />
      <path d="M8 14h8" />
      <path d="M5 19h14" />
    </svg>
  ),
  logout: (
    <svg {...headerIconProps}>
      <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
      <path d="m16 17 5-5-5-5" />
      <path d="M21 12H9" />
    </svg>
  ),
};

const THEME_OPTIONS: Array<{
  key: Theme;
  labelKey: string;
  icon: ReactNode;
}> = [
  { key: 'auto', labelKey: 'theme.auto', icon: headerIcons.autoTheme },
  { key: 'white', labelKey: 'theme.white', icon: headerIcons.sun },
  { key: 'dark', labelKey: 'theme.dark', icon: headerIcons.moon },
];

const VISUAL_EFFECTS_OPTIONS: Array<{
  key: VisualEffectsMode;
  labelKey: string;
  icon: ReactNode;
}> = [
  { key: 'full', labelKey: 'visual_effects.full', icon: headerIcons.visualEffectsFull },
  {
    key: 'reduced',
    labelKey: 'visual_effects.reduced',
    icon: headerIcons.visualEffectsReduced,
  },
];

function PluginSidebarIcon({ src }: { src: string }) {
  const [failed, setFailed] = useState(false);
  const showImage = Boolean(src) && !failed;

  return showImage ? (
    <img src={src} alt="" onError={() => setFailed(true)} />
  ) : (
    <IconSidebarPlugins size={SIDEBAR_ICON_SIZE} />
  );
}

type NavItem = {
  path: string;
  label: string;
  shortLabel?: string;
  icon: ReactNode;
  exact?: boolean;
};

interface MainLayoutProps {
  routeBase?: string;
  demoMode?: boolean;
}

export function MainLayout({ routeBase = '', demoMode = false }: MainLayoutProps = {}) {
  const { t } = useTranslation();
  const { showNotification } = useNotificationStore();
  const location = useLocation();
  const navigate = useNavigate();
  const routePathname = stripRouteBase(location.pathname, routeBase);

  const logout = useAuthStore((state) => state.logout);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const apiBase = useAuthStore((state) => state.apiBase);
  const supportsPlugin = useAuthStore((state) => state.supportsPlugin);

  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const clearCache = useConfigStore((state) => state.clearCache);
  const featureAvailability = usePanelFeatureAvailability();

  const theme = useThemeStore((state) => state.theme);
  const setTheme = useThemeStore((state) => state.setTheme);
  const visualEffectsMode = useVisualEffectsStore((state) => state.mode);
  const setVisualEffectsMode = useVisualEffectsStore((state) => state.setMode);
  const language = useLanguageStore((state) => state.language);
  const setLanguage = useLanguageStore((state) => state.setLanguage);

  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(() => {
    try {
      return localStorage.getItem(STORAGE_KEY_SIDEBAR) === 'true';
    } catch {
      return false;
    }
  });
  const [languageMenuOpen, setLanguageMenuOpen] = useState(false);
  const [themeMenuOpen, setThemeMenuOpen] = useState(false);
  const [visualEffectsMenuOpen, setVisualEffectsMenuOpen] = useState(false);
  const [pluginResources, setPluginResources] = useState<PluginResourceEntry[]>([]);
  const contentRef = useRef<HTMLDivElement | null>(null);
  const languageMenuRef = useRef<HTMLDivElement | null>(null);
  const themeMenuRef = useRef<HTMLDivElement | null>(null);
  const visualEffectsMenuRef = useRef<HTMLDivElement | null>(null);
  const headerRef = useRef<HTMLElement | null>(null);

  const fullBrandName = 'CPA Manager Plus';
  const abbrBrandName = t('title.abbr');
  const isLogsPage = routePathname.startsWith('/logs');
  const isPluginResourcePage = routePathname.startsWith('/plugin-pages');
  const showSidebarLabels = !sidebarCollapsed || sidebarOpen;
  const pluginControlMenuVisible = isPluginManagementNavVisible({ supportsPlugin });
  const configPluginsEnabled = config?.pluginsEnabled;

  // 将顶部悬浮控制区高度写入 CSS 变量，供移动端粘性元素和浮层避让。
  useLayoutEffect(() => {
    const updateHeaderHeight = () => {
      const height = headerRef.current?.offsetHeight;
      if (height) {
        document.documentElement.style.setProperty('--header-height', `${height}px`);
      }
    };

    updateHeaderHeight();

    const resizeObserver =
      typeof ResizeObserver !== 'undefined' && headerRef.current
        ? new ResizeObserver(updateHeaderHeight)
        : null;
    if (resizeObserver && headerRef.current) {
      resizeObserver.observe(headerRef.current);
    }

    window.addEventListener('resize', updateHeaderHeight);

    return () => {
      if (resizeObserver) {
        resizeObserver.disconnect();
      }
      window.removeEventListener('resize', updateHeaderHeight);
    };
  }, []);

  // 将主内容区的中心点写入 CSS 变量，供底部浮层（配置面板操作栏、提供商导航）对齐到内容区
  useLayoutEffect(() => {
    const updateContentCenter = () => {
      const el = contentRef.current;
      if (!el) return;
      const rect = el.getBoundingClientRect();
      const centerX = rect.left + rect.width / 2;
      document.documentElement.style.setProperty('--content-center-x', `${centerX}px`);
    };

    updateContentCenter();

    const resizeObserver =
      typeof ResizeObserver !== 'undefined' && contentRef.current
        ? new ResizeObserver(updateContentCenter)
        : null;

    if (resizeObserver && contentRef.current) {
      resizeObserver.observe(contentRef.current);
    }

    window.addEventListener('resize', updateContentCenter);

    return () => {
      if (resizeObserver) {
        resizeObserver.disconnect();
      }
      window.removeEventListener('resize', updateContentCenter);
      document.documentElement.style.removeProperty('--content-center-x');
    };
  }, []);

  useEffect(() => {
    if (!languageMenuOpen) {
      return;
    }

    const handlePointerDown = (event: MouseEvent) => {
      if (!languageMenuRef.current?.contains(event.target as Node)) {
        setLanguageMenuOpen(false);
      }
    };

    const handleEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setLanguageMenuOpen(false);
      }
    };

    document.addEventListener('mousedown', handlePointerDown);
    document.addEventListener('keydown', handleEscape);

    return () => {
      document.removeEventListener('mousedown', handlePointerDown);
      document.removeEventListener('keydown', handleEscape);
    };
  }, [languageMenuOpen]);

  useEffect(() => {
    if (!themeMenuOpen) {
      return;
    }

    const handlePointerDown = (event: MouseEvent) => {
      if (!themeMenuRef.current?.contains(event.target as Node)) {
        setThemeMenuOpen(false);
      }
    };

    const handleEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setThemeMenuOpen(false);
      }
    };

    document.addEventListener('mousedown', handlePointerDown);
    document.addEventListener('keydown', handleEscape);

    return () => {
      document.removeEventListener('mousedown', handlePointerDown);
      document.removeEventListener('keydown', handleEscape);
    };
  }, [themeMenuOpen]);

  useEffect(() => {
    if (!visualEffectsMenuOpen) {
      return;
    }

    const handlePointerDown = (event: MouseEvent) => {
      if (!visualEffectsMenuRef.current?.contains(event.target as Node)) {
        setVisualEffectsMenuOpen(false);
      }
    };

    const handleEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setVisualEffectsMenuOpen(false);
      }
    };

    document.addEventListener('mousedown', handlePointerDown);
    document.addEventListener('keydown', handleEscape);

    return () => {
      document.removeEventListener('mousedown', handlePointerDown);
      document.removeEventListener('keydown', handleEscape);
    };
  }, [visualEffectsMenuOpen]);

  const toggleLanguageMenu = useCallback(() => {
    setLanguageMenuOpen((prev) => !prev);
    setThemeMenuOpen(false);
    setVisualEffectsMenuOpen(false);
  }, []);

  const toggleThemeMenu = useCallback(() => {
    setThemeMenuOpen((prev) => !prev);
    setLanguageMenuOpen(false);
    setVisualEffectsMenuOpen(false);
  }, []);

  const toggleVisualEffectsMenu = useCallback(() => {
    setVisualEffectsMenuOpen((prev) => !prev);
    setLanguageMenuOpen(false);
    setThemeMenuOpen(false);
  }, []);

  const handleThemeSelect = useCallback(
    (nextTheme: Theme) => {
      setTheme(nextTheme);
      setThemeMenuOpen(false);
    },
    [setTheme]
  );

  const handleVisualEffectsSelect = useCallback(
    (nextMode: VisualEffectsMode) => {
      setVisualEffectsMode(nextMode);
      setVisualEffectsMenuOpen(false);
    },
    [setVisualEffectsMode]
  );

  const handleLanguageSelect = useCallback(
    (nextLanguage: string) => {
      if (!isSupportedLanguage(nextLanguage)) {
        return;
      }
      setLanguage(nextLanguage);
      setLanguageMenuOpen(false);
    },
    [setLanguage]
  );

  useEffect(() => {
    fetchConfig().catch(() => {
      // ignore initial failure; login flow会提示
    });
  }, [fetchConfig]);

  const loadPluginResources = useCallback(async () => {
    if (connectionStatus !== 'connected' || !supportsPlugin) {
      setPluginResources([]);
      return;
    }

    try {
      const plugins = await pluginsApi.list();
      setPluginResources(
        isPluginResourceNavVisible({
          supportsPlugin,
          pluginsEnabled: plugins.pluginsEnabled,
        })
          ? collectPluginResourceEntries(plugins.plugins)
          : []
      );
    } catch {
      setPluginResources([]);
    }
  }, [connectionStatus, supportsPlugin]);

  useEffect(() => {
    const timer = window.setTimeout(() => {
      void loadPluginResources();
    }, 0);

    window.addEventListener(PLUGIN_RESOURCES_REFRESH_EVENT, loadPluginResources);

    return () => {
      window.clearTimeout(timer);
      window.removeEventListener(PLUGIN_RESOURCES_REFRESH_EVENT, loadPluginResources);
    };
  }, [apiBase, configPluginsEnabled, loadPluginResources]);

  const fileLogsAvailable = isFileLogsAvailable(config);
  const navShortLabel = (key: string, fallback: string) => {
    const shortKey = `${key}_short`;
    const label = t(shortKey, { defaultValue: fallback });
    return label === shortKey ? fallback : label;
  };
  const dashboardNavItem: NavItem = {
    path: '/', label: t('nav.dashboard'),
    shortLabel: navShortLabel('nav.dashboard', t('nav.dashboard')),
    icon: sidebarIcons.dashboard,
  };
  const usageAnalyticsNavItem = featureAvailability.requestMonitoringAvailable
    ? {
        path: '/usage-analytics',
        label: t('nav.usage_analytics'),
        shortLabel: navShortLabel('nav.usage_analytics', t('nav.usage_analytics')),
        icon: sidebarIcons.usageAnalytics,
      }
    : null;
  const monitoringNavItem = featureAvailability.requestMonitoringAvailable
    ? {
        path: '/monitoring',
        label: t('nav.monitoring_center'),
        shortLabel: navShortLabel('nav.monitoring_center', t('nav.monitoring_center')),
        icon: sidebarIcons.monitoring,
      }
    : null;
  const operationNavItems: NavItem[] = [
    ...(fileLogsAvailable
      ? [
          {
            path: '/logs',
            label: t('nav.logs'),
            shortLabel: navShortLabel('nav.logs', t('nav.logs')),
            icon: sidebarIcons.logs,
          },
        ]
      : []),
  ];
  const pluginControlNavItems: NavItem[] = pluginControlMenuVisible
    ? [
        {
          path: '/plugins',
          label: t('nav.plugins'),
          shortLabel: navShortLabel('nav.plugins', t('nav.plugins')),
          icon: sidebarIcons.plugins,
        },
      ]
    : [];
  const pluginResourceNavItems: NavItem[] = pluginControlMenuVisible
    ? pluginResources.map((resource) => ({
        path: resource.route,
        label: resource.label,
        shortLabel: resource.label,
        icon: <PluginSidebarIcon src={resolvePluginAssetURL(resource.pluginLogo, apiBase)} />,
      }))
    : [];
  const navSections: NavItem[][] = [
    [
      dashboardNavItem,
      ...(usageAnalyticsNavItem ? [usageAnalyticsNavItem] : []),
      ...(monitoringNavItem ? [monitoringNavItem] : []),
    ],
    [
      {
        path: '/config',
        label: t('nav.config_management'),
        shortLabel: navShortLabel('nav.config_management', t('nav.config_management')),
        icon: sidebarIcons.config,
      },
      {
        path: '/ai-providers',
        label: t('nav.ai_providers'),
        shortLabel: navShortLabel('nav.ai_providers', t('nav.ai_providers')),
        icon: sidebarIcons.aiProviders,
      },
      ...pluginControlNavItems,
    ],
    [
      {
        path: '/auth-files',
        label: t('nav.auth_files'),
        shortLabel: navShortLabel('nav.auth_files', t('nav.auth_files')),
        icon: sidebarIcons.authFiles,
      },
      {
        path: '/oauth',
        label: t('nav.oauth', { defaultValue: 'OAuth' }),
        shortLabel: navShortLabel('nav.oauth', t('nav.oauth', { defaultValue: 'OAuth' })),
        icon: sidebarIcons.oauth,
      },
      {
        path: '/quota',
        label: t('nav.quota_management'),
        shortLabel: navShortLabel('nav.quota_management', t('nav.quota_management')),
        icon: sidebarIcons.quota,
      },
      {
        path: '/codex-inspection',
        label: t('nav.codex_inspection'),
        shortLabel: navShortLabel('nav.codex_inspection', t('nav.codex_inspection')),
        icon: sidebarIcons.codexInspection,
      },
    ],
    operationNavItems,
    pluginResourceNavItems,
    [
      {
        path: '/system',
        label: t('nav.system_info'),
        shortLabel: navShortLabel('nav.system_info', t('nav.system_info')),
        icon: sidebarIcons.system,
      },
    ],
  ].filter((section) => section.length > 0);
  const navItems = navSections.flat();
  const navOrder = navItems.map((item) => item.path);
  const getRouteOrder = (pathname: string) => {
    const trimmedPath =
      pathname.length > 1 && pathname.endsWith('/') ? pathname.slice(0, -1) : pathname;
    const normalizedPath = trimmedPath === '/dashboard' ? '/' : trimmedPath;

    const aiProvidersIndex = navOrder.indexOf('/ai-providers');
    if (aiProvidersIndex !== -1) {
      if (normalizedPath === '/ai-providers') return aiProvidersIndex;
      if (normalizedPath.startsWith('/ai-providers/')) {
        if (normalizedPath.startsWith('/ai-providers/gemini')) return aiProvidersIndex + 0.1;
        if (normalizedPath.startsWith('/ai-providers/codex')) return aiProvidersIndex + 0.2;
        if (normalizedPath.startsWith('/ai-providers/claude')) return aiProvidersIndex + 0.3;
        if (normalizedPath.startsWith('/ai-providers/vertex')) return aiProvidersIndex + 0.4;
        if (normalizedPath.startsWith('/ai-providers/openai')) return aiProvidersIndex + 0.6;
        return aiProvidersIndex + 0.05;
      }
    }

    const authFilesIndex = navOrder.indexOf('/auth-files');
    if (authFilesIndex !== -1) {
      if (normalizedPath === '/auth-files') return authFilesIndex;
      if (normalizedPath.startsWith('/auth-files/')) {
        if (normalizedPath.startsWith('/auth-files/oauth-excluded')) return authFilesIndex + 0.1;
        if (normalizedPath.startsWith('/auth-files/oauth-model-alias')) return authFilesIndex + 0.2;
        return authFilesIndex + 0.05;
      }
    }

    const exactIndex = navOrder.indexOf(normalizedPath);
    if (exactIndex !== -1) return exactIndex;
    const nestedIndex = navOrder.findIndex(
      (path) => path !== '/' && normalizedPath.startsWith(`${path}/`)
    );
    return nestedIndex === -1 ? null : nestedIndex;
  };

  const getTransitionVariant = useCallback((fromPathname: string, toPathname: string) => {
    const normalize = (pathname: string) => {
      const trimmed =
        pathname.length > 1 && pathname.endsWith('/') ? pathname.slice(0, -1) : pathname;
      return trimmed === '/dashboard' ? '/' : trimmed;
    };

    const from = normalize(fromPathname);
    const to = normalize(toPathname);
    const isAuthFiles = (pathname: string) =>
      pathname === '/auth-files' || pathname.startsWith('/auth-files/');
    const isAiProviders = (pathname: string) =>
      pathname === '/ai-providers' || pathname.startsWith('/ai-providers/');
    if (isAuthFiles(from) && isAuthFiles(to)) return 'ios';
    if (isAiProviders(from) && isAiProviders(to)) return 'ios';
    return 'none';
  }, []);

  const handleRefreshAll = async () => {
    clearCache();
    const results = await Promise.allSettled([
      fetchConfig(undefined, true),
      loadPluginResources(),
      triggerHeaderRefresh(),
    ]);
    const rejected = results.find((result) => result.status === 'rejected');
    if (rejected && rejected.status === 'rejected') {
      const reason = rejected.reason;
      const message =
        typeof reason === 'string' ? reason : reason instanceof Error ? reason.message : '';
      showNotification(
        `${t('notification.refresh_failed')}${message ? `: ${message}` : ''}`,
        'error'
      );
      return;
    }
    showNotification(t('notification.data_refreshed'), 'success');
  };
  const handleLogout = () => {
    if (demoMode) {
      navigate(getDemoLogoutPath(routeBase), { replace: true });
      return;
    }
    logout();
  };
  const mobileSidebarToggleLabel = sidebarOpen
    ? t('sidebar.toggle_collapse', { defaultValue: 'Close navigation' })
    : t('sidebar.toggle_expand', { defaultValue: 'Open navigation' });
  const normalizedLocationPath =
    routePathname.length > 1 && routePathname.endsWith('/')
      ? routePathname.slice(0, -1)
      : routePathname;
  const currentPath = normalizedLocationPath === '/dashboard' ? '/' : normalizedLocationPath;
  const matchesNavPath = (item: NavItem, pathname: string) =>
    item.path === '/' || item.exact
      ? pathname === item.path
      : pathname === item.path || pathname.startsWith(`${item.path}/`);
  const activeNavItem =
    [...navItems]
      .sort((a, b) => b.path.length - a.path.length)
      .find((item) => matchesNavPath(item, currentPath)) ?? navItems[0];
  const currentRouteLabel = activeNavItem?.label ?? fullBrandName;

  return (
    <div
      className={[
        'app-shell',
        sidebarCollapsed ? 'sidebar-is-collapsed' : '',
        isPluginResourcePage ? 'plugin-resource-shell' : '',
      ]
        .filter(Boolean)
        .join(' ')}
    >
      <header className="main-header" ref={headerRef}>
        <div className="navbar">
          <div className="navbar-left">
            <button
              type="button"
              className="hamburger-container"
              onClick={() => {
                if (window.matchMedia('(max-width: 768px)').matches) {
                  setSidebarOpen((prev) => !prev);
                  return;
                }
                setSidebarCollapsed((prev) => {
                  const next = !prev;
                  try {
                    localStorage.setItem(STORAGE_KEY_SIDEBAR, String(next));
                  } catch {
                    /* ignore storage failures */
                  }
                  return next;
                });
              }}
              title={mobileSidebarToggleLabel}
              aria-label={mobileSidebarToggleLabel}
            >
              {sidebarOpen
                ? headerIcons.close
                : sidebarCollapsed
                  ? headerIcons.sidebarExpand
                  : headerIcons.sidebarCollapse}
            </button>

            <nav
              className="app-breadcrumb"
              aria-label={t('common.navigation', { defaultValue: 'Navigation' })}
            >
              <span className="breadcrumb-item">{currentRouteLabel}</span>
            </nav>
          </div>

          <div className="navbar-right">
            <Button
              variant="ghost"
              size="sm"
              onClick={handleRefreshAll}
              title={t('header.refresh_all')}
              aria-label={t('header.refresh_all')}
            >
              {headerIcons.refresh}
            </Button>

            <div
              className={`language-menu ${languageMenuOpen ? 'open' : ''}`}
              ref={languageMenuRef}
            >
              <Button
                variant="ghost"
                size="sm"
                onClick={toggleLanguageMenu}
                title={t('language.switch')}
                aria-label={t('language.switch')}
                aria-haspopup="menu"
                aria-expanded={languageMenuOpen}
              >
                {headerIcons.language}
              </Button>
              {languageMenuOpen && (
                <div
                  className="notification entering language-menu-popover"
                  role="menu"
                  aria-label={t('language.switch')}
                >
                  {LANGUAGE_ORDER.map((lang) => (
                    <button
                      key={lang}
                      type="button"
                      className={`language-menu-option ${language === lang ? 'active' : ''}`}
                      onClick={() => handleLanguageSelect(lang)}
                      role="menuitemradio"
                      aria-checked={language === lang}
                    >
                      <span>{t(LANGUAGE_LABEL_KEYS[lang])}</span>
                    </button>
                  ))}
                </div>
              )}
            </div>

            <div className={`theme-menu ${themeMenuOpen ? 'open' : ''}`} ref={themeMenuRef}>
              <Button
                variant="ghost"
                size="sm"
                onClick={toggleThemeMenu}
                title={t('theme.switch')}
                aria-label={t('theme.switch')}
                aria-haspopup="menu"
                aria-expanded={themeMenuOpen}
              >
                {theme === 'auto'
                  ? headerIcons.autoTheme
                  : theme === 'dark'
                    ? headerIcons.moon
                    : headerIcons.sun}
              </Button>
              {themeMenuOpen && (
                <div
                  className="notification entering theme-menu-popover"
                  role="menu"
                  aria-label={t('theme.switch')}
                >
                  {THEME_OPTIONS.map((option) => (
                    <button
                      key={option.key}
                      type="button"
                      className={`theme-option ${theme === option.key ? 'active' : ''}`}
                      onClick={() => handleThemeSelect(option.key)}
                      role="menuitemradio"
                      aria-checked={theme === option.key}
                      title={t(option.labelKey)}
                      aria-label={t(option.labelKey)}
                    >
                      <span className="theme-option-icon">{option.icon}</span>
                    </button>
                  ))}
                </div>
              )}
            </div>

            <div
              className={`visual-effects-menu ${visualEffectsMenuOpen ? 'open' : ''}`}
              ref={visualEffectsMenuRef}
            >
              <Button
                variant="ghost"
                size="sm"
                onClick={toggleVisualEffectsMenu}
                title={t('visual_effects.switch')}
                aria-label={t('visual_effects.switch')}
                aria-haspopup="menu"
                aria-expanded={visualEffectsMenuOpen}
              >
                {visualEffectsMode === 'full'
                  ? headerIcons.visualEffectsFull
                  : headerIcons.visualEffectsReduced}
              </Button>
              {visualEffectsMenuOpen && (
                <div
                  className="notification entering visual-effects-menu-popover"
                  role="menu"
                  aria-label={t('visual_effects.switch')}
                >
                  {VISUAL_EFFECTS_OPTIONS.map((option) => (
                    <button
                      key={option.key}
                      type="button"
                      className={`visual-effects-option ${
                        visualEffectsMode === option.key ? 'active' : ''
                      }`}
                      onClick={() => handleVisualEffectsSelect(option.key)}
                      role="menuitemradio"
                      aria-checked={visualEffectsMode === option.key}
                      title={t(option.labelKey)}
                      aria-label={t(option.labelKey)}
                    >
                      <span className="visual-effects-option-icon">{option.icon}</span>
                      <span className="visual-effects-option-label">{t(option.labelKey)}</span>
                    </button>
                  ))}
                </div>
              )}
            </div>

            <Button
              variant="ghost"
              size="sm"
              onClick={handleLogout}
              title={t('header.logout')}
              aria-label={t('header.logout')}
            >
              {headerIcons.logout}
            </Button>
          </div>
        </div>
      </header>

      <div className="main-body">
        <button
          type="button"
          className={`sidebar-backdrop ${sidebarOpen ? 'visible' : ''}`}
          onClick={() => setSidebarOpen(false)}
          aria-label={t('common.close')}
          aria-hidden={!sidebarOpen}
          tabIndex={sidebarOpen ? 0 : -1}
        />

        <aside
          className={`sidebar ${sidebarOpen ? 'open' : ''} ${sidebarCollapsed ? 'collapsed' : ''}`}
        >
          <div className="sidebar-brand" title={fullBrandName}>
            <div className="sidebar-brand-main">
              <img src={INLINE_LOGO_JPEG} alt="CPAMC logo" className="sidebar-brand-logo" />
              {showSidebarLabels && <span className="sidebar-brand-title">{abbrBrandName}</span>}
            </div>
            {!showSidebarLabels && (
              <span className="sidebar-brand-short">{abbrBrandName.charAt(0) || 'C'}</span>
            )}
          </div>

          <div className="nav-section">
            {navSections.map((section, sectionIndex) => (
              <div className="nav-menu-section" key={`nav-section-${sectionIndex}`}>
                {sectionIndex > 0 && <div className="nav-menu-divider" aria-hidden="true" />}
                {section.map((item) => (
                  <NavLink
                    key={item.path}
                    to={prefixRouteBase(item.path, routeBase)}
                    end={item.path === '/' || item.exact}
                    className={({ isActive }) =>
                      `nav-item ${isActive || matchesNavPath(item, currentPath) ? 'active' : ''}`
                    }
                    onClick={() => setSidebarOpen(false)}
                    title={item.label}
                  >
                    <span className="nav-icon">{item.icon}</span>
                    {showSidebarLabels && (
                      <span className="nav-label">{item.shortLabel ?? item.label}</span>
                    )}
                  </NavLink>
                ))}
              </div>
            ))}
          </div>
        </aside>

        <div
          className={[
            'content',
            isLogsPage ? 'content-logs' : '',
            isPluginResourcePage ? 'content-plugin-resource' : '',
          ]
            .filter(Boolean)
            .join(' ')}
          ref={contentRef}
        >
          <main
            className={[
              'main-content',
              isLogsPage ? 'main-content-logs' : '',
              isPluginResourcePage ? 'main-content-plugin-resource' : '',
            ]
              .filter(Boolean)
              .join(' ')}
          >
            <PageTransition
              key={routeBase || 'main'}
              render={(location) => <MainRoutes location={location} routeBase={routeBase} />}
              getRouteOrder={(pathname) => getRouteOrder(stripRouteBase(pathname, routeBase))}
              getTransitionVariant={(fromPathname, toPathname) =>
                getTransitionVariant(
                  stripRouteBase(fromPathname, routeBase),
                  stripRouteBase(toPathname, routeBase)
                )
              }
              scrollContainerRef={contentRef}
            />
          </main>
        </div>
      </div>
    </div>
  );
}
