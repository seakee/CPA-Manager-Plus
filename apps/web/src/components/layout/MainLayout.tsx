import {
  ReactNode,
  SVGProps,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from 'react';
import { createPortal } from 'react-dom';
import { useLocation, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { configurationText } from '@/i18n/configurationWorkspace';
import { Button } from '@/components/ui/Button';
import { AppFooter } from '@/components/common/AppFooter';
import { PageTransition } from '@/components/common/PageTransition';
import { DatabaseMaintenanceBanner } from '@/components/common/DatabaseMaintenanceBanner';
import { DatabaseMaintenanceProvider } from '@/components/common/DatabaseMaintenanceContext';
import { MainRoutes } from '@/router/MainRoutes';
import {
  IconChevronRight,
  IconGithub,
  IconPin,
  IconPinOff,
  IconSidebarAuthFiles,
  IconSidebarClients,
  IconSidebarConfig,
  IconSidebarDashboard,
  IconSidebarLogs,
  IconSidebarMonitor,
  IconSidebarOauth,
  IconSidebarObserve,
  IconSidebarPinned,
  IconSidebarPlugins,
  IconSidebarProviders,
  IconSidebarResources,
  IconSidebarSystem,
  IconSidebarUsage,
} from '@/components/ui/icons';
import {
  CPAMP_SYMBOL_COLOR_PNG_URL,
  CPAMP_WORDMARK_COLOR_PNG_URL,
  CPAMP_WORDMARK_ON_DARK_PNG_URL,
} from '@/assets/brand';
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
  type PluginResourceEntry,
} from '@/features/plugins/pluginResources';
import { triggerHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { prefixRouteBase, stripRouteBase } from '@/router/routeBase';
import { LANGUAGE_LABEL_KEYS, LANGUAGE_ORDER, STORAGE_KEY_SIDEBAR } from '@/utils/constants';
import { isSupportedLanguage } from '@/utils/language';
import type { Theme, VisualEffectsMode } from '@/types';
import {
  getNavSectionsStorageKey,
  getPinnedNavStorageKey,
  parsePinnedNavPreference,
  pinNavItem,
  readOpenNavSections,
  readPinnedNavIds,
  unpinNavItem,
  writeOpenNavSections,
  writePinnedNavIds,
  type NavPreferenceMode,
} from './navPreferences';
import {
  buildCanonicalNavModel,
  findNavSectionByItemId,
  PINNED_SECTION_ID,
  projectNavSections,
  type NavItem,
  type NavSidebarIconSet,
} from './navRegistry';

const SIDEBAR_ICON_SIZE = 20;
const PIN_BUTTON_ICON_SIZE = 13;
const GITHUB_REPOSITORY_URL = 'https://github.com/seakee/CPA-Manager-Plus';
const NAV_SECTIONS_STORAGE_KEY = `${STORAGE_KEY_SIDEBAR}-sections`;

const sidebarIcons: NavSidebarIconSet = {
  dashboard: <IconSidebarDashboard size={SIDEBAR_ICON_SIZE} />,
  resources: <IconSidebarResources size={SIDEBAR_ICON_SIZE} />,
  clients: <IconSidebarClients size={SIDEBAR_ICON_SIZE} />,
  observe: <IconSidebarObserve size={SIDEBAR_ICON_SIZE} />,
  aiProviders: <IconSidebarProviders size={SIDEBAR_ICON_SIZE} />,
  authFiles: <IconSidebarAuthFiles size={SIDEBAR_ICON_SIZE} />,
  oauth: <IconSidebarOauth size={SIDEBAR_ICON_SIZE} />,
  usageAnalytics: <IconSidebarUsage size={SIDEBAR_ICON_SIZE} />,
  monitoring: <IconSidebarMonitor size={SIDEBAR_ICON_SIZE} />,
  plugins: <IconSidebarPlugins size={SIDEBAR_ICON_SIZE} />,
  config: <IconSidebarConfig size={SIDEBAR_ICON_SIZE} />,
  logs: <IconSidebarLogs size={SIDEBAR_ICON_SIZE} />,
  system: <IconSidebarSystem size={SIDEBAR_ICON_SIZE} />,
  pinned: <IconSidebarPinned size={SIDEBAR_ICON_SIZE} />,
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

type PendingPinFocus = {
  mode: 'pin' | 'unpin';
  inFlyout: boolean;
  keyboardTrigger: boolean;
  itemId: string;
  sourceSectionId: string;
  canonicalSectionId?: string;
  siblingItemIds: string[];
};

interface MainLayoutProps {
  routeBase?: string;
  navigationBase?: string;
  enablePinnedNav?: boolean;
  onLogout?: () => void;
  logoutDisabled?: boolean;
  hasUnsavedChanges?: boolean;
  navigationDisabled?: boolean;
  contentFooter?: ReactNode;
  /** Optional intent signal when the user hovers or focuses an inactive nav link. */
  onNavItemIntent?: (path: string) => void;
  /**
   * Optional host-owned route seam. Full Web uses this only for resources
   * whose authority is not the historical CPA panel (for example the
   * Manager-owned Fresh New Key/Config pages). Returning undefined keeps the
   * existing CPAMP route tree unchanged; returning null deliberately renders
   * an unavailable-state page without invoking a legacy request.
   */
  renderRoute?: (pathname: string) => ReactNode | undefined;
}

function MainLayoutContent({
  routeBase = '',
  navigationBase = '',
  enablePinnedNav = false,
  renderRoute,
  onLogout,
  logoutDisabled = false,
  hasUnsavedChanges = false,
  navigationDisabled = false,
  contentFooter,
  onNavItemIntent,
}: MainLayoutProps = {}) {
  const { t } = useTranslation();
  const { showNotification, showConfirmation } = useNotificationStore();
  const logoutBlockedRef = useRef(logoutDisabled);
  useEffect(() => { logoutBlockedRef.current = logoutDisabled; }, [logoutDisabled]);
  const location = useLocation();
  const navigate = useNavigate();
  const routePathname = stripRouteBase(location.pathname, routeBase);

  const logout = useAuthStore((state) => state.logout);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const apiBase = useAuthStore((state) => state.apiBase);
  const supportsPlugin = useAuthStore((state) => state.supportsPlugin);

  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
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
  const preferenceMode: NavPreferenceMode = enablePinnedNav ? 'fullweb' : 'panel';
  const pinnedStorageKey = getPinnedNavStorageKey(routeBase, preferenceMode);
  const sectionsStorageKey = getNavSectionsStorageKey(
    routeBase,
    preferenceMode,
    NAV_SECTIONS_STORAGE_KEY
  );
  const [openNavSections, setOpenNavSections] = useState<Record<string, boolean>>(() =>
    readOpenNavSections(
      sectionsStorageKey,
      enablePinnedNav ? NAV_SECTIONS_STORAGE_KEY : undefined
    )
  );
  const [pinnedNavIds, setPinnedNavIds] = useState<string[]>(() =>
    enablePinnedNav ? readPinnedNavIds(pinnedStorageKey) : []
  );
  const [loadedSectionsKey, setLoadedSectionsKey] = useState(sectionsStorageKey);
  const [loadedPinnedKey, setLoadedPinnedKey] = useState(enablePinnedNav ? pinnedStorageKey : '');
  const [pinEditMode, setPinEditMode] = useState(false);
  const [prevSidebarOpen, setPrevSidebarOpen] = useState(sidebarOpen);
  const [navFlyout, setNavFlyout] = useState<{ id: string; top: number; left: number } | null>(
    null
  );
  const navFlyoutRef = useRef<HTMLDivElement | null>(null);
  const [lastNavRoute, setLastNavRoute] = useState<string | null>(null);
  const [lastAutoExpandedKey, setLastAutoExpandedKey] = useState<string | null>(null);
  const navToggleRefs = useRef<Record<string, HTMLButtonElement | null>>({});
  const navPinButtonRefs = useRef<Record<string, HTMLButtonElement | null>>({});
  const dashboardLinkRef = useRef<HTMLAnchorElement | null>(null);
  const pendingPinFocusRef = useRef<PendingPinFocus | null>(null);
  const sidebarRef = useRef<HTMLElement | null>(null);
  const [pluginResources, setPluginResources] = useState<PluginResourceEntry[]>([]);
  const contentRef = useRef<HTMLDivElement | null>(null);
  const languageMenuRef = useRef<HTMLDivElement | null>(null);
  const themeMenuRef = useRef<HTMLDivElement | null>(null);
  const visualEffectsMenuRef = useRef<HTMLDivElement | null>(null);
  const headerRef = useRef<HTMLElement | null>(null);

  const fullBrandName = 'CPA Manager Plus';
  const isLogsPage = routePathname.startsWith('/logs');
  const logsTabParam = isLogsPage ? new URLSearchParams(location.search).get('tab') : null;
  const isLogsViewportLocked =
    isLogsPage && logsTabParam !== 'errors' && logsTabParam !== 'settings';
  const isPluginResourcePage = routePathname.startsWith('/plugin-pages');
  const showSidebarLabels = !sidebarCollapsed || sidebarOpen;
  const configPluginsEnabled = config?.pluginsEnabled;
  const pluginControlMenuVisible = isPluginManagementNavVisible({ supportsPlugin });

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

  const pluginLoadSeqRef = useRef(0);
  const pluginConnectionKey = `${connectionStatus}:${apiBase}:${String(supportsPlugin)}`;
  const pluginConnectionKeyRef = useRef(pluginConnectionKey);
  useEffect(() => {
    pluginConnectionKeyRef.current = pluginConnectionKey;
    pluginLoadSeqRef.current += 1;
  }, [pluginConnectionKey]);

  const loadPluginResources = useCallback(async () => {
    const seq = ++pluginLoadSeqRef.current;
    const expectedConnectionKey = pluginConnectionKey;
    if (connectionStatus !== 'connected' || !supportsPlugin) {
      setPluginResources([]);
      return;
    }

    try {
      const plugins = await pluginsApi.list();
      if (
        seq !== pluginLoadSeqRef.current ||
        pluginConnectionKeyRef.current !== expectedConnectionKey
      ) {
        return;
      }
      setPluginResources(
        isPluginResourceNavVisible({
          supportsPlugin,
          pluginsEnabled: plugins.pluginsEnabled,
        })
          ? collectPluginResourceEntries(plugins.plugins)
          : []
      );
    } catch {
      if (
        seq !== pluginLoadSeqRef.current ||
        pluginConnectionKeyRef.current !== expectedConnectionKey
      ) {
        return;
      }
      setPluginResources([]);
    }
  }, [connectionStatus, pluginConnectionKey, supportsPlugin]);

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

  const {
    dashboardItem: dashboardNavItem,
    canonicalSections,
    canonicalItems: navItems,
    canonicalNavOrder: navOrder,
  } = buildCanonicalNavModel({
    t,
    icons: sidebarIcons,
    featureAvailability,
    hasHostRoutes: Boolean(renderRoute),
    pluginControlMenuVisible,
    pluginResources,
    apiBase,
    renderPluginIcon: (src) => <PluginSidebarIcon src={src} />,
  });
  const { displaySections, pinnedItemIds } = projectNavSections({
    canonicalSections,
    pinnedIds: pinnedNavIds,
    enablePinnedNav,
    pinnedSectionLabel: t('nav.group_pinned', { defaultValue: 'Pinned' }),
    pinnedSectionIcon: sidebarIcons.pinned,
  });
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
    const isAccounts = (pathname: string) => pathname === '/accounts';
    const isAiProviders = (pathname: string) =>
      pathname === '/ai-providers' || pathname.startsWith('/ai-providers/');
    if (isAccounts(from) && isAccounts(to)) return 'ios';
    if (isAiProviders(from) && isAiProviders(to)) return 'ios';
    return 'none';
  }, []);

  const handleRefreshAll = async () => {
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
    if (logoutDisabled) return;
    const finishLogout = () => {
      if (logoutBlockedRef.current) return;
      if (onLogout) onLogout();
      else logout();
    };
    if (hasUnsavedChanges) {
      showConfirmation({
        title: configurationText('leaveTitle'),
        message: configurationText('leaveHelp'),
        confirmText: configurationText('leaveConfirm'),
        cancelText: configurationText('keep'),
        variant: 'danger',
        onConfirm: finishLogout,
      });
      return;
    }
    finishLogout();
  };
  const mobileSidebarToggleLabel = sidebarOpen
    ? t('sidebar.toggle_collapse', { defaultValue: 'Close navigation' })
    : t('sidebar.toggle_expand', { defaultValue: 'Open navigation' });
  const normalizedLocationPath =
    routePathname.length > 1 && routePathname.endsWith('/')
      ? routePathname.slice(0, -1)
      : routePathname;
  const currentPath = normalizedLocationPath === '/dashboard' ? '/' : normalizedLocationPath;
  const matchesNavPath = (item: NavItem, pathname: string) => {
    if (renderRoute && item.path === '/config' && pathname === '/gateway') {
      return true;
    }
    return item.path === '/' || item.exact
      ? pathname === item.path
      : pathname === item.path || pathname.startsWith(`${item.path}/`);
  };
  const activeNavItem =
    [...navItems]
      .sort((a, b) => b.path.length - a.path.length)
      .find((item) => matchesNavPath(item, currentPath)) ?? null;
  const standaloneRouteLabel =
    currentPath === '/accounts' ? t('nav.accounts', { defaultValue: t('accounts.title') }) : null;
  const currentRouteLabel = activeNavItem?.label ?? standaloneRouteLabel ?? fullBrandName;
  const canonicalSection = findNavSectionByItemId(canonicalSections, activeNavItem?.id);
  const displaySection = findNavSectionByItemId(displaySections, activeNavItem?.id);
  const displaySectionId = displaySection?.id;
  const flyoutSection = displaySections.find((section) => section.id === navFlyout?.id);
  const nestedRouteLabel = currentPath.startsWith('/ai-providers/')
    ? currentPath.endsWith('/models')
      ? t('nav.provider_models')
      : currentPath.includes('/new')
        ? t('common.add')
        : t('common.edit')
    : currentPath === '/monitoring/account-actions'
      ? t('nav.account_actions')
      : null;

  // Reload scoped preferences if the deployment scope changes.
  if (loadedSectionsKey !== sectionsStorageKey) {
    setLoadedSectionsKey(sectionsStorageKey);
    setOpenNavSections(
      readOpenNavSections(
        sectionsStorageKey,
        enablePinnedNav ? NAV_SECTIONS_STORAGE_KEY : undefined
      )
    );
  }
  const expectedPinnedKey = enablePinnedNav ? pinnedStorageKey : '';
  if (loadedPinnedKey !== expectedPinnedKey) {
    setLoadedPinnedKey(expectedPinnedKey);
    setPinnedNavIds(enablePinnedNav ? readPinnedNavIds(pinnedStorageKey) : []);
  }

  // Manual collapse remains respected until navigation changes the route or the
  // active item's section resolves for the first time.
  const navRouteChanged = lastNavRoute !== currentPath;
  const autoExpandKey = `${currentPath}:${displaySectionId ?? ''}`;
  if (navRouteChanged) {
    setLastNavRoute(currentPath);
    setNavFlyout(null);
  }
  if (lastAutoExpandedKey !== autoExpandKey) {
    setLastAutoExpandedKey(autoExpandKey);
    if (displaySectionId) {
      setOpenNavSections((current) =>
        current[displaySectionId] ? current : { ...current, [displaySectionId]: true }
      );
    }
  }

  // Reset mobile/touch pin edit mode when closing the drawer or disabling pinned nav.
  if (prevSidebarOpen !== sidebarOpen) {
    setPrevSidebarOpen(sidebarOpen);
    if (!sidebarOpen && pinEditMode) {
      setPinEditMode(false);
    }
  }
  if ((!enablePinnedNav || !showSidebarLabels) && pinEditMode) {
    setPinEditMode(false);
  }

  // Close the collapsed flyout cleanly if its section disappears after pinning/unpinning.
  if (navFlyout && !flyoutSection) {
    setNavFlyout(null);
  }
  useEffect(() => {
    writeOpenNavSections(sectionsStorageKey, openNavSections);
  }, [sectionsStorageKey, openNavSections]);

  useEffect(() => {
    if (!enablePinnedNav) return;
    const handleStorage = (event: StorageEvent) => {
      if (event.key !== pinnedStorageKey) return;
      setPinnedNavIds(parsePinnedNavPreference(event.newValue));
    };
    window.addEventListener('storage', handleStorage);
    return () => {
      window.removeEventListener('storage', handleStorage);
    };
  }, [enablePinnedNav, pinnedStorageKey]);

  useLayoutEffect(() => {
    const pending = pendingPinFocusRef.current;
    if (!pending) return;
    pendingPinFocusRef.current = null;

    const focusElement = (element: HTMLElement | null | undefined): boolean => {
      if (!element || typeof element.focus !== 'function') return false;
      element.focus();
      return true;
    };

    const focusFirstPinButton = (ids: string[], prefix: string): boolean => {
      for (const candidateId of ids) {
        if (focusElement(navPinButtonRefs.current[`${prefix}${candidateId}`])) {
          return true;
        }
      }
      return false;
    };

    if (pending.inFlyout) {
      const stillOpen = Boolean(navFlyout && displaySections.some((s) => s.id === navFlyout.id));
      if (stillOpen) {
        if (!pending.keyboardTrigger) return;
        if (focusFirstPinButton(pending.siblingItemIds, 'flyout:')) return;
        if (focusElement(navFlyoutRef.current?.querySelector?.<HTMLElement>('a, button'))) return;
        return;
      }
      if (
        pending.mode === 'pin' &&
        focusElement(navToggleRefs.current[PINNED_SECTION_ID])
      ) {
        return;
      }
      if (
        pending.canonicalSectionId &&
        focusElement(navToggleRefs.current[pending.canonicalSectionId])
      ) {
        return;
      }
      if (focusElement(navToggleRefs.current[pending.sourceSectionId])) return;
      const fallbackToggle = displaySections
        .map((section) => navToggleRefs.current[section.id])
        .find(Boolean);
      if (focusElement(fallbackToggle)) return;
      focusElement(dashboardLinkRef.current);
      return;
    }

    if (!pending.keyboardTrigger) return;

    if (pending.mode === 'pin') {
      if (focusElement(navPinButtonRefs.current[`sidebar:${pending.itemId}`])) return;
      if (focusElement(navToggleRefs.current[PINNED_SECTION_ID])) return;
    } else {
      if (focusFirstPinButton(pending.siblingItemIds, 'sidebar:')) return;
      if (focusElement(navPinButtonRefs.current[`sidebar:${pending.itemId}`])) return;
      if (
        pending.canonicalSectionId &&
        focusElement(navToggleRefs.current[pending.canonicalSectionId])
      ) {
        return;
      }
    }
    if (focusElement(navToggleRefs.current[pending.sourceSectionId])) return;
    focusElement(dashboardLinkRef.current);
  }, [displaySections, navFlyout, openNavSections, pinnedNavIds]);

  useEffect(() => {
    if (!navFlyout) return;
    navFlyoutRef.current?.querySelector<HTMLAnchorElement>('a')?.focus();
    const closeOnOutsideClick = (event: MouseEvent) => {
      if (
        !navFlyoutRef.current?.contains(event.target as Node) &&
        !navToggleRefs.current[navFlyout.id]?.contains(event.target as Node)
      ) {
        setNavFlyout(null);
      }
    };
    const closeOnResize = () => setNavFlyout(null);
    document.addEventListener('mousedown', closeOnOutsideClick);
    window.addEventListener('resize', closeOnResize);
    return () => {
      document.removeEventListener('mousedown', closeOnOutsideClick);
      window.removeEventListener('resize', closeOnResize);
    };
  }, [navFlyout]);

  useEffect(() => {
    if (!sidebarOpen) return;
    const mobileViewport = window.matchMedia('(max-width: 768px)');
    const handleViewportChange = (event: MediaQueryListEvent) => {
      if (!event.matches) setSidebarOpen(false);
    };
    mobileViewport.addEventListener('change', handleViewportChange);
    const previousFocus = document.activeElement as HTMLElement | null;
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    const focusable = () =>
      Array.from(
        sidebarRef.current?.querySelectorAll<HTMLElement>('a[href], button:not([disabled])') ?? []
      );
    focusable()[0]?.focus();
    const handleKeyboard = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setSidebarOpen(false);
      if (event.key !== 'Tab') return;
      const items = focusable();
      const first = items[0];
      const last = items[items.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last?.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first?.focus();
      }
    };
    document.addEventListener('keydown', handleKeyboard);
    return () => {
      document.body.style.overflow = previousOverflow;
      document.removeEventListener('keydown', handleKeyboard);
      mobileViewport.removeEventListener('change', handleViewportChange);
      if (previousFocus?.isConnected) previousFocus.focus({ preventScroll: true });
    };
  }, [sidebarOpen]);

  const handleTogglePin = (
    item: NavItem,
    sourceSectionId: string,
    inFlyout: boolean,
    event?: { detail?: number }
  ) => {
    if (!enablePinnedNav || item.pinnable === false) return;
    const currentlyPinned = pinnedItemIds.has(item.id);
    const sourceSection = displaySections.find((section) => section.id === sourceSectionId);
    const sourceIds = sourceSection ? sourceSection.items.map((entry) => entry.id) : [];
    const currentIndex = sourceIds.indexOf(item.id);
    const siblingItemIds = [
      ...sourceIds.slice(currentIndex + 1),
      ...sourceIds.slice(0, Math.max(0, currentIndex)).reverse(),
    ];
    const itemCanonicalSectionId = findNavSectionByItemId(canonicalSections, item.id)?.id;

    pendingPinFocusRef.current = {
      mode: currentlyPinned ? 'unpin' : 'pin',
      inFlyout,
      keyboardTrigger: !event || event.detail === 0,
      itemId: item.id,
      sourceSectionId,
      canonicalSectionId: itemCanonicalSectionId,
      siblingItemIds,
    };
    const nextPinnedIds = currentlyPinned
      ? unpinNavItem(pinnedNavIds, item.id)
      : pinNavItem(pinnedNavIds, item.id);
    writePinnedNavIds(pinnedStorageKey, nextPinnedIds);
    setPinnedNavIds(nextPinnedIds);

    if (!currentlyPinned) {
      // Always reveal Pinned immediately without collapsing the source section.
      setOpenNavSections((current) =>
        current[PINNED_SECTION_ID] ? current : { ...current, [PINNED_SECTION_ID]: true }
      );
      return;
    }

    setOpenNavSections((current) => {
      let changed = false;
      const next = { ...current };
      if (nextPinnedIds.length === 0 && PINNED_SECTION_ID in next) {
        delete next[PINNED_SECTION_ID];
        changed = true;
      }
      if (activeNavItem?.id === item.id && itemCanonicalSectionId && !next[itemCanonicalSectionId]) {
        next[itemCanonicalSectionId] = true;
        changed = true;
      }
      return changed ? next : current;
    });
  };

  const renderNavLink = (item: NavItem, inFlyout = false, sectionId?: string) => {
    const target = prefixRouteBase(item.path, navigationBase || routeBase);
    const active = activeNavItem?.id === item.id;
    const canPin = Boolean(enablePinnedNav && sectionId && item.pinnable !== false);
    const isPinned = canPin && pinnedItemIds.has(item.id);
    const link = (
      <a
        ref={!sectionId ? dashboardLinkRef : undefined}
        key={!canPin ? item.id : undefined}
        href={target}
        aria-current={active ? 'page' : undefined}
        aria-disabled={navigationDisabled || undefined}
        className={`nav-item ${active ? 'active' : ''}`}
        onMouseEnter={() => {
          if (!navigationDisabled && !active) {
            onNavItemIntent?.(item.path);
          }
        }}
        onFocus={() => {
          if (!navigationDisabled && !active) {
            onNavItemIntent?.(item.path);
          }
        }}
        onClick={(event) => {
          if (navigationDisabled) {
            event.preventDefault();
            return;
          }
          if (
            event.defaultPrevented ||
            event.button !== 0 ||
            event.metaKey ||
            event.ctrlKey ||
            event.shiftKey ||
            event.altKey
          )
            return;
          event.preventDefault();
          setSidebarOpen(false);
          setPinEditMode(false);
          setNavFlyout(null);
          navigate(navigationBase ? item.path : routeBase ? target : item.path);
        }}
        title={item.label}
      >
        <span className="nav-icon">{item.icon}</span>
        {(showSidebarLabels || inFlyout) && <span className="nav-label">{item.label}</span>}
      </a>
    );

    if (!canPin || !sectionId) {
      return link;
    }

    const pinRefKey = `${inFlyout ? 'flyout:' : 'sidebar:'}${item.id}`;
    const pinLabel = isPinned
      ? t('nav.unpin_item', { label: item.label, defaultValue: `Unpin ${item.label}` })
      : t('nav.pin_item', { label: item.label, defaultValue: `Pin ${item.label}` });
    return (
      <div
        key={item.id}
        className={`nav-item-row ${active ? 'active' : ''} ${isPinned ? 'is-pinned' : ''}`}
      >
        {link}
        <button
          ref={(element) => {
            if (element) {
              navPinButtonRefs.current[pinRefKey] = element;
            } else {
              delete navPinButtonRefs.current[pinRefKey];
            }
          }}
          type="button"
          className={`nav-pin-button ${isPinned ? 'is-pinned' : ''}`}
          aria-label={pinLabel}
          aria-pressed={isPinned}
          title={pinLabel}
          onClick={(event) => handleTogglePin(item, sectionId, inFlyout, event)}
        >
          {isPinned ? (
            <IconPinOff size={PIN_BUTTON_ICON_SIZE} />
          ) : (
            <IconPin size={PIN_BUTTON_ICON_SIZE} fill="none" />
          )}
        </button>
      </div>
    );
  };

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
                setNavFlyout(null);
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
              {canonicalSection && (
                <>
                  <span className="breadcrumb-item">{canonicalSection.label}</span>
                  <span className="breadcrumb-separator" aria-hidden="true">
                    /
                  </span>
                </>
              )}
              {nestedRouteLabel ? (
                <>
                  <a
                    className="breadcrumb-item"
                    href={prefixRouteBase(activeNavItem?.path ?? '/', navigationBase || routeBase)}
                    onClick={(event) => {
                      event.preventDefault();
                      if (!navigationDisabled && activeNavItem) {
                        navigate(
                          navigationBase
                            ? activeNavItem.path
                            : prefixRouteBase(activeNavItem.path, routeBase)
                        );
                      }
                    }}
                  >
                    {currentRouteLabel}
                  </a>
                  <span className="breadcrumb-separator" aria-hidden="true">
                    /
                  </span>
                  <span className="breadcrumb-item" aria-current="page">
                    {nestedRouteLabel}
                  </span>
                </>
              ) : (
                <span className="breadcrumb-item" aria-current="page">
                  {currentRouteLabel}
                </span>
              )}
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

            <a
              className="btn btn-ghost btn-sm"
              href={GITHUB_REPOSITORY_URL}
              target="_blank"
              rel="noreferrer"
              title={t('header.open_github')}
              aria-label={t('header.open_github')}
            >
              <span>
                <IconGithub size={16} />
              </span>
            </a>

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
              disabled={logoutDisabled}
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
          ref={sidebarRef}
          aria-label={t('common.navigation')}
          className={[
            'sidebar',
            sidebarOpen ? 'open' : '',
            sidebarCollapsed ? 'collapsed' : '',
            pinEditMode ? 'pin-edit-mode' : '',
          ]
            .filter(Boolean)
            .join(' ')}
        >
          <div className="sidebar-brand" title={fullBrandName}>
            <div className="sidebar-brand-main">
              <img
                src={CPAMP_SYMBOL_COLOR_PNG_URL}
                alt={showSidebarLabels ? '' : 'CPA Manager Plus'}
                className="sidebar-brand-symbol"
              />
              {showSidebarLabels && (
                <>
                  <img
                    src={CPAMP_WORDMARK_COLOR_PNG_URL}
                    alt="CPA Manager Plus"
                    className="sidebar-brand-wordmark sidebar-brand-wordmark-light"
                  />
                  <img
                    src={CPAMP_WORDMARK_ON_DARK_PNG_URL}
                    alt="CPA Manager Plus"
                    className="sidebar-brand-wordmark sidebar-brand-wordmark-dark"
                  />
                </>
              )}
            </div>
            {enablePinnedNav && showSidebarLabels && (
              <button
                type="button"
                className={`sidebar-pin-edit-toggle ${pinEditMode ? 'active' : ''}`}
                aria-pressed={pinEditMode}
                aria-label={t('nav.manage_pinned_aria', {
                  defaultValue: 'Manage pinned navigation',
                })}
                onClick={() => setPinEditMode((current) => !current)}
              >
                <IconPin size={PIN_BUTTON_ICON_SIZE} fill={pinEditMode ? 'currentColor' : 'none'} />
                <span>
                  {pinEditMode
                    ? t('nav.manage_pinned_done', { defaultValue: 'Done' })
                    : t('nav.manage_pinned', { defaultValue: 'Pin' })}
                </span>
              </button>
            )}
          </div>

          <nav className="nav-section" onScroll={() => setNavFlyout(null)}>
            {renderNavLink(dashboardNavItem)}
            {displaySections.map((section) => {
              const isSectionOpen = !!openNavSections[section.id];
              const isSectionActiveCollapsed =
                displaySectionId === section.id && (!showSidebarLabels || !isSectionOpen);
              return (
                <div className="nav-menu-section" key={section.id}>
                  <button
                    ref={(element) => {
                      if (element) {
                        navToggleRefs.current[section.id] = element;
                      } else {
                        delete navToggleRefs.current[section.id];
                      }
                    }}
                    type="button"
                    className={[
                      'nav-section-toggle',
                      'nav-item',
                      !showSidebarLabels && displaySectionId === section.id ? 'active' : '',
                      showSidebarLabels && isSectionActiveCollapsed ? 'section-has-active' : '',
                    ]
                      .filter(Boolean)
                      .join(' ')}
                    aria-label={section.label}
                    aria-expanded={
                      showSidebarLabels ? isSectionOpen : navFlyout?.id === section.id
                    }
                    aria-controls={`nav-${section.id}`}
                    onClick={(event) => {
                      if (!showSidebarLabels) {
                        const rect = event.currentTarget.getBoundingClientRect();
                        setNavFlyout((current) =>
                          current?.id === section.id
                            ? null
                            : {
                                id: section.id,
                                top: Math.max(
                                  8,
                                  Math.min(
                                    rect.top,
                                    window.innerHeight - (section.items.length * 42 + 64)
                                  )
                                ),
                                left: rect.right + 12,
                              }
                        );
                        return;
                      }
                      setOpenNavSections((current) => ({
                        ...current,
                        [section.id]: !current[section.id],
                      }));
                    }}
                    onKeyDown={(event) => {
                      if (event.key === 'ArrowDown' && !showSidebarLabels) {
                        event.preventDefault();
                        event.currentTarget.click();
                      }
                    }}
                    title={section.label}
                  >
                    <span className="nav-icon">{section.icon}</span>
                    {showSidebarLabels && <span className="nav-label">{section.label}</span>}
                    {showSidebarLabels && (
                      <span
                        className={`nav-section-chevron ${isSectionOpen ? 'is-open' : ''}`}
                        aria-hidden="true"
                      >
                        <IconChevronRight size={14} />
                      </span>
                    )}
                  </button>
                  {showSidebarLabels && isSectionOpen && (
                    <div className="nav-section-children" id={`nav-${section.id}`}>
                      {section.items.map((item) => renderNavLink(item, false, section.id))}
                    </div>
                  )}
                </div>
              );
            })}
          </nav>
        </aside>
        {navFlyout &&
          flyoutSection &&
          !showSidebarLabels &&
          createPortal(
            <div
              ref={navFlyoutRef}
              id={`nav-${flyoutSection.id}`}
              className="nav-flyout"
              aria-label={flyoutSection.label}
              style={{ top: navFlyout.top, left: navFlyout.left }}
              onBlur={(event) => {
                if (pendingPinFocusRef.current) return;
                const next = event.relatedTarget as Node | null;
                const isGroupToggle = Object.values(navToggleRefs.current).some((toggle) =>
                  toggle?.contains(next)
                );
                if (!event.currentTarget.contains(next) && !isGroupToggle) setNavFlyout(null);
              }}
              onKeyDown={(event) => {
                if (event.key === 'Escape') {
                  event.preventDefault();
                  setNavFlyout(null);
                  navToggleRefs.current[flyoutSection.id]?.focus();
                  return;
                }
                const activeElement = document.activeElement as HTMLElement | null;
                if (event.key === 'ArrowRight' || event.key === 'ArrowLeft') {
                  const row = activeElement?.closest?.('.nav-item-row');
                  if (!row) return;
                  const target =
                    event.key === 'ArrowRight'
                      ? row.querySelector<HTMLElement>('.nav-pin-button')
                      : row.querySelector<HTMLElement>('a');
                  if (target && target !== activeElement) {
                    event.preventDefault();
                    target.focus();
                  }
                  return;
                }
                if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
                event.preventDefault();
                const links = Array.from(
                  event.currentTarget.querySelectorAll<HTMLAnchorElement>('a')
                );
                if (links.length === 0) return;
                const activeRow = activeElement?.closest?.('.nav-item-row');
                const onPinButton = Boolean(activeElement?.classList?.contains('nav-pin-button'));
                const currentLink = activeRow
                  ? activeRow.querySelector<HTMLAnchorElement>('a')
                  : (activeElement as HTMLAnchorElement | null);
                const current = currentLink ? links.indexOf(currentLink) : -1;
                const next =
                  event.key === 'Home'
                    ? 0
                    : event.key === 'End'
                      ? links.length - 1
                      : (current + (event.key === 'ArrowUp' ? -1 : 1) + links.length) %
                        links.length;
                const nextLink = links[next];
                if (!nextLink) return;
                if (onPinButton) {
                  const nextPin = nextLink
                    .closest?.('.nav-item-row')
                    ?.querySelector<HTMLElement>('.nav-pin-button');
                  (nextPin ?? nextLink).focus();
                  return;
                }
                nextLink.focus();
              }}
            >
              <div className="nav-flyout-title">{flyoutSection.label}</div>
              {flyoutSection.items.map((item) => renderNavLink(item, true, flyoutSection.id))}
            </div>,
            document.body
          )}

        <div
          className={[
            'content',
            isLogsViewportLocked ? 'content-logs' : '',
            isPluginResourcePage ? 'content-plugin-resource' : '',
          ]
            .filter(Boolean)
            .join(' ')}
          ref={contentRef}
        >
          <main
            className={[
              'main-content',
              isLogsViewportLocked ? 'main-content-logs' : '',
              isPluginResourcePage ? 'main-content-plugin-resource' : '',
            ]
              .filter(Boolean)
              .join(' ')}
          >
            <DatabaseMaintenanceBanner />
            {!isPluginResourcePage ? (
              <div id="main-content-banners" className="main-content-banners" />
            ) : null}
            <PageTransition
              key={routeBase || 'main'}
              render={(location) => {
                const routePath = stripRouteBase(location.pathname, routeBase);
                const override = renderRoute?.(routePath);
                if (override !== undefined) return override;
                return (
                  <MainRoutes
                    location={
                      routeBase && navigationBase
                        ? { ...location, pathname: stripRouteBase(location.pathname, routeBase) }
                        : location
                    }
                  />
                );
              }}
              getRouteOrder={(pathname) => getRouteOrder(stripRouteBase(pathname, routeBase))}
              getTransitionVariant={(fromPathname, toPathname) =>
                getTransitionVariant(
                  stripRouteBase(fromPathname, routeBase),
                  stripRouteBase(toPathname, routeBase)
                )
              }
              scrollContainerRef={contentRef}
            />
            {contentFooter}
            {!isPluginResourcePage ? <AppFooter compact={isLogsViewportLocked} /> : null}
          </main>
        </div>
      </div>
    </div>
  );
}

export function MainLayout(props: MainLayoutProps = {}) {
  return (
    <DatabaseMaintenanceProvider>
      <MainLayoutContent {...props} />
    </DatabaseMaintenanceProvider>
  );
}
