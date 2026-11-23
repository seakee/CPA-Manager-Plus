import type { ReactNode } from 'react';
import type { TFunction } from 'i18next';
import {
  isSupportedPluginResourcePath,
  resolvePluginAssetURL,
  type PluginResourceEntry,
} from '@/features/plugins/pluginResources';
import { isValidNavPinId } from './navPreferences';

export const PINNED_SECTION_ID = 'pinned' as const;

export const CANONICAL_NAV_IDS = {
  dashboard: 'dashboard',
  accounts: 'resources.accounts',
  aiProviders: 'resources.ai_providers',
  oauth: 'resources.oauth',
  apiKeys: 'clients.api_keys',
  monitoring: 'observe.monitoring',
  usageAnalytics: 'observe.usage_analytics',
  modelPrices: 'observe.model_prices',
  systemConfig: 'system.config',
  runtimeStatus: 'system.runtime',
  usageMaintenance: 'system.usage_maintenance',
  gatewayManagement: 'system.gateway',
  updates: 'system.updates',
  logs: 'system.logs',
  pluginsManagement: 'plugins.management',
} as const;

export interface NavItem {
  id: string;
  path: string;
  label: string;
  shortLabel?: string;
  icon: ReactNode;
  exact?: boolean;
  pinnable?: boolean;
}

export interface NavSection {
  id: string;
  label: string;
  icon: ReactNode;
  items: NavItem[];
}

export interface NavSidebarIconSet {
  dashboard: ReactNode;
  resources: ReactNode;
  clients: ReactNode;
  observe: ReactNode;
  aiProviders: ReactNode;
  authFiles: ReactNode;
  oauth: ReactNode;
  usageAnalytics: ReactNode;
  monitoring: ReactNode;
  plugins: ReactNode;
  config: ReactNode;
  logs: ReactNode;
  system: ReactNode;
  pinned: ReactNode;
}

export interface NavFeatureAvailability {
  requestMonitoringAvailable: boolean;
  modelPricesAvailable: boolean;
  managerServiceAvailable?: boolean;
  reason?: string;
}

export interface CanonicalNavModel {
  dashboardItem: NavItem;
  canonicalSections: NavSection[];
  canonicalItems: NavItem[];
  canonicalNavOrder: string[];
}

export interface NavProjectionResult {
  displaySections: NavSection[];
  pinnedItemIds: ReadonlySet<string>;
  hasVisiblePinnedItems: boolean;
}

export function buildPluginMenuPinId(pluginID: string, rawPath: string): string | null {
  const trimmedPluginId = pluginID.trim();
  const trimmedPath = rawPath.trim();
  if (!trimmedPluginId || !isSupportedPluginResourcePath(trimmedPath)) {
    return null;
  }
  const normalizedPath =
    trimmedPath.length > 1 && trimmedPath.endsWith('/')
      ? trimmedPath.replace(/\/+$/, '')
      : trimmedPath;
  if (!isSupportedPluginResourcePath(normalizedPath)) {
    return null;
  }
  const candidate = `plugin:${encodeURIComponent(trimmedPluginId)}:${normalizedPath}`;
  return isValidNavPinId(candidate) ? candidate : null;
}

export function buildCanonicalNavModel({
  t,
  icons,
  featureAvailability,
  hasHostRoutes,
  pluginControlMenuVisible,
  pluginResources,
  apiBase,
  renderPluginIcon,
}: {
  t: TFunction;
  icons: NavSidebarIconSet;
  featureAvailability: NavFeatureAvailability;
  hasHostRoutes: boolean;
  pluginControlMenuVisible: boolean;
  pluginResources: readonly PluginResourceEntry[];
  apiBase: string;
  renderPluginIcon: (src: string) => ReactNode;
}): CanonicalNavModel {
  const navShortLabel = (key: string, fallback: string) => {
    const shortKey = `${key}_short`;
    const label = t(shortKey, { defaultValue: fallback });
    return label === shortKey ? fallback : label;
  };

  const dashboardItem: NavItem = {
    id: CANONICAL_NAV_IDS.dashboard,
    path: '/',
    label: t('nav.dashboard'),
    shortLabel: navShortLabel('nav.dashboard', t('nav.dashboard')),
    icon: icons.dashboard,
    pinnable: false,
  };

  const usageAnalyticsNavItem: NavItem | null = featureAvailability.requestMonitoringAvailable
    ? {
        id: CANONICAL_NAV_IDS.usageAnalytics,
        path: '/usage-analytics',
        label: t('nav.usage_analytics'),
        shortLabel: navShortLabel('nav.usage_analytics', t('nav.usage_analytics')),
        icon: icons.usageAnalytics,
        pinnable: true,
      }
    : null;

  // Request monitoring is a switch: while only the collector is off the entry
  // stays in the sidebar and the page explains how to start it. Hiding it here
  // is what made a disabled collector look like a missing feature.
  const monitoringNavItem: NavItem | null =
    featureAvailability.requestMonitoringAvailable ||
    featureAvailability.reason === 'monitoring_disabled'
      ? {
          id: CANONICAL_NAV_IDS.monitoring,
          path: '/monitoring',
          label: t('nav.monitoring_center'),
          shortLabel: navShortLabel('nav.monitoring_center', t('nav.monitoring_center')),
          icon: icons.monitoring,
          pinnable: true,
        }
      : null;

  const pluginControlNavItems: NavItem[] = pluginControlMenuVisible
    ? [
        {
          id: CANONICAL_NAV_IDS.pluginsManagement,
          path: '/plugins',
          label: t('nav.plugins'),
          shortLabel: navShortLabel('nav.plugins', t('nav.plugins')),
          icon: icons.plugins,
          pinnable: true,
        },
      ]
    : [];

  const pluginCandidateIds = pluginControlMenuVisible
    ? pluginResources.map((resource) => buildPluginMenuPinId(resource.pluginID, resource.menu.path))
    : [];
  const pluginCandidateCounts = new Map<string, number>();
  for (const id of pluginCandidateIds) {
    if (!id) continue;
    pluginCandidateCounts.set(id, (pluginCandidateCounts.get(id) ?? 0) + 1);
  }

  const pluginResourceNavItems: NavItem[] = pluginControlMenuVisible
    ? pluginResources.map((resource, index) => {
        const candidateId = pluginCandidateIds[index];
        const hasUniquePinId =
          candidateId !== null && (pluginCandidateCounts.get(candidateId) ?? 0) === 1;
        return {
          id: hasUniquePinId
            ? candidateId
            : `plugin-unpinnable:${encodeURIComponent(resource.pluginID)}:${resource.menuIndex}`,
          path: resource.route,
          label: resource.label,
          shortLabel: resource.label,
          icon: renderPluginIcon(resolvePluginAssetURL(resource.pluginLogo, apiBase)),
          pinnable: hasUniquePinId,
        };
      })
    : [];

  const canonicalSections: NavSection[] = [
    {
      id: 'resources',
      label: t('nav.group_resources'),
      icon: icons.resources,
      items: [
        {
          id: CANONICAL_NAV_IDS.accounts,
          path: '/accounts',
          label: t('nav.accounts', { defaultValue: t('accounts.title') }),
          icon: icons.authFiles,
          pinnable: true,
        },
        {
          id: CANONICAL_NAV_IDS.aiProviders,
          path: '/ai-providers',
          label: t('nav.ai_providers'),
          shortLabel: navShortLabel('nav.ai_providers', t('nav.ai_providers')),
          icon: icons.aiProviders,
          pinnable: true,
        },
        {
          id: CANONICAL_NAV_IDS.oauth,
          path: '/oauth',
          label: t('nav.oauth', { defaultValue: 'OAuth' }),
          shortLabel: navShortLabel('nav.oauth', t('nav.oauth', { defaultValue: 'OAuth' })),
          icon: icons.oauth,
          pinnable: true,
        },
      ],
    },
    {
      id: 'clients',
      label: t('nav.group_clients'),
      icon: icons.clients,
      items: hasHostRoutes
        ? [
            {
              id: CANONICAL_NAV_IDS.apiKeys,
              path: '/api-keys',
              label: t('nav.api_keys'),
              icon: icons.clients,
              pinnable: true,
            },
          ]
        : [],
    },
    {
      id: 'observe',
      label: t('nav.group_observe'),
      icon: icons.observe,
      items: [
        ...(monitoringNavItem ? [monitoringNavItem] : []),
        ...(usageAnalyticsNavItem ? [usageAnalyticsNavItem] : []),
        ...(featureAvailability.modelPricesAvailable
          ? [
              {
                id: CANONICAL_NAV_IDS.modelPrices,
                path: '/model-prices',
                label: t('nav.model_prices'),
                icon: icons.usageAnalytics,
                pinnable: true,
              },
            ]
          : []),
      ],
    },
    {
      id: 'system',
      label: t('nav.group_system'),
      icon: icons.system,
      items: [
        {
          id: CANONICAL_NAV_IDS.systemConfig,
          path: '/system',
          label: hasHostRoutes ? t('nav.system_config') : t('nav.system_info'),
          shortLabel: hasHostRoutes
            ? navShortLabel('nav.system_config', t('nav.system_config'))
            : navShortLabel('nav.system_info', t('nav.system_info')),
          icon: icons.system,
          exact: true,
          pinnable: true,
        },
        {
          id: CANONICAL_NAV_IDS.runtimeStatus,
          path: '/system/runtime',
          label: t('nav.runtime_status', { defaultValue: 'Runtime status' }),
          shortLabel: navShortLabel(
            'nav.runtime_status',
            t('nav.runtime_status', { defaultValue: 'Runtime' })
          ),
          icon: icons.system,
          pinnable: true,
        },
        ...(hasHostRoutes || featureAvailability.managerServiceAvailable
          ? [
              {
                id: CANONICAL_NAV_IDS.usageMaintenance,
                path: '/usage-maintenance',
                label: t('nav.usage_maintenance', { defaultValue: 'Usage Maintenance' }),
                shortLabel: navShortLabel(
                  'nav.usage_maintenance',
                  t('nav.usage_maintenance', { defaultValue: 'Maintenance' })
                ),
                icon: icons.system,
                pinnable: true,
              },
            ]
          : []),
        {
          id: CANONICAL_NAV_IDS.gatewayManagement,
          path: '/config',
          label: hasHostRoutes ? t('nav.gateway_management') : t('nav.config_management'),
          shortLabel: hasHostRoutes
            ? navShortLabel('nav.gateway_management', t('nav.gateway_management'))
            : navShortLabel('nav.config_management', t('nav.config_management')),
          icon: icons.config,
          pinnable: true,
        },
        {
          id: CANONICAL_NAV_IDS.updates,
          path: '/system/updates',
          label: t('manager_updates.title'),
          icon: icons.system,
          pinnable: true,
        },
        {
          id: CANONICAL_NAV_IDS.logs,
          path: '/logs',
          label: t('nav.logs'),
          icon: icons.logs,
          pinnable: true,
        },
      ],
    },
    {
      id: 'plugins',
      label: t('nav.group_plugins'),
      icon: icons.plugins,
      items: [...pluginControlNavItems, ...pluginResourceNavItems],
    },
  ].filter((section) => section.items.length > 0);

  const canonicalItems = [dashboardItem, ...canonicalSections.flatMap((section) => section.items)];
  const canonicalNavOrder = canonicalItems.map((item) => item.path);

  return {
    dashboardItem,
    canonicalSections,
    canonicalItems,
    canonicalNavOrder,
  };
}

export function projectNavSections({
  canonicalSections,
  pinnedIds,
  enablePinnedNav,
  pinnedSectionLabel,
  pinnedSectionIcon,
}: {
  canonicalSections: readonly NavSection[];
  pinnedIds: readonly string[];
  enablePinnedNav: boolean;
  pinnedSectionLabel: string;
  pinnedSectionIcon: ReactNode;
}): NavProjectionResult {
  if (!enablePinnedNav) {
    return {
      displaySections: [...canonicalSections],
      pinnedItemIds: new Set<string>(),
      hasVisiblePinnedItems: false,
    };
  }

  const pinnableById = new Map<string, NavItem>();
  for (const section of canonicalSections) {
    for (const item of section.items) {
      if (item.pinnable !== false) {
        pinnableById.set(item.id, item);
      }
    }
  }

  const pinnedItems: NavItem[] = [];
  const pinnedItemIds = new Set<string>();
  for (const id of pinnedIds) {
    if (pinnedItemIds.has(id)) continue;
    const item = pinnableById.get(id);
    if (!item) continue;
    pinnedItemIds.add(id);
    pinnedItems.push(item);
  }

  const remainingSections: NavSection[] = [];
  for (const section of canonicalSections) {
    const remainingItems = section.items.filter((item) => !pinnedItemIds.has(item.id));
    if (remainingItems.length > 0) {
      remainingSections.push({
        ...section,
        items: remainingItems,
      });
    }
  }

  if (pinnedItems.length === 0) {
    return {
      displaySections: remainingSections,
      pinnedItemIds,
      hasVisiblePinnedItems: false,
    };
  }

  return {
    displaySections: [
      {
        id: PINNED_SECTION_ID,
        label: pinnedSectionLabel,
        icon: pinnedSectionIcon,
        items: pinnedItems,
      },
      ...remainingSections,
    ],
    pinnedItemIds,
    hasVisiblePinnedItems: true,
  };
}

export function findNavSectionByItemId(
  sections: readonly NavSection[],
  itemId: string | undefined
): NavSection | undefined {
  if (!itemId) return undefined;
  return sections.find((section) => section.items.some((item) => item.id === itemId));
}
