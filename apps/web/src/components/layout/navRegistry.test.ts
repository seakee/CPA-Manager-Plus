import type { TFunction } from 'i18next';
import { describe, expect, it } from 'vitest';
import type { PluginResourceEntry } from '@/features/plugins/pluginResources';
import {
  buildCanonicalNavModel,
  buildPluginMenuPinId,
  CANONICAL_NAV_IDS,
  findNavSectionByItemId,
  PINNED_SECTION_ID,
  projectNavSections,
  type NavSidebarIconSet,
} from './navRegistry';

const t = ((key: string, options?: { defaultValue?: string }) =>
  options?.defaultValue ?? key) as unknown as TFunction;

const icons: NavSidebarIconSet = {
  dashboard: 'dashboard',
  resources: 'resources',
  clients: 'clients',
  observe: 'observe',
  aiProviders: 'aiProviders',
  authFiles: 'authFiles',
  oauth: 'oauth',
  usageAnalytics: 'usageAnalytics',
  monitoring: 'monitoring',
  plugins: 'plugins',
  config: 'config',
  logs: 'logs',
  system: 'system',
  pinned: 'pinned',
};

const makePluginResource = (
  pluginID: string,
  menuIndex: number,
  path: string,
  label = `Plugin ${pluginID} #${menuIndex}`
): PluginResourceEntry => ({
  pluginID,
  pluginTitle: pluginID,
  pluginLogo: '',
  menuIndex,
  menu: { path, menu: label, description: label },
  label,
  description: label,
  route: `/plugin-pages/${encodeURIComponent(pluginID)}/${menuIndex}`,
});

describe('navRegistry', () => {
  it('builds stable plugin pin ids from normalized same-origin resource paths and rejects invalid ones', () => {
    expect(buildPluginMenuPinId('alpha', '/v0/resource/plugins/alpha/index.html')).toBe(
      'plugin:alpha:/v0/resource/plugins/alpha/index.html'
    );
    expect(buildPluginMenuPinId('alpha', '/v0/resource/plugins/alpha/ui/')).toBe(
      'plugin:alpha:/v0/resource/plugins/alpha/ui'
    );
    expect(buildPluginMenuPinId('alpha', 'https://evil.example/page')).toBeNull();
    expect(buildPluginMenuPinId('alpha', 'data:text/html,hi')).toBeNull();
    expect(buildPluginMenuPinId('alpha', '/v0/resource/plugins/../escape')).toBeNull();
    expect(buildPluginMenuPinId('alpha', '/v0/resource/plugins/')).toBeNull();
  });

  it('marks duplicate plugin menu paths non-pinnable while keeping unique plugin menus pinnable', () => {
    const model = buildCanonicalNavModel({
      t,
      icons,
      featureAvailability: { requestMonitoringAvailable: true, modelPricesAvailable: true },
      hasHostRoutes: true,
      pluginControlMenuVisible: true,
      pluginResources: [
        makePluginResource('dup-plugin', 0, '/v0/resource/plugins/dup-plugin/same.html', 'First'),
        makePluginResource('dup-plugin', 1, '/v0/resource/plugins/dup-plugin/same.html/', 'Second'),
        makePluginResource('good-plugin', 0, '/v0/resource/plugins/good-plugin/view.html', 'Good'),
      ],
      apiBase: '',
      renderPluginIcon: () => 'plugin-icon',
    });

    const pluginsSection = model.canonicalSections.find((s) => s.id === 'plugins')!;
    const dupItems = pluginsSection.items.filter((item) => item.label === 'First' || item.label === 'Second');
    const goodItem = pluginsSection.items.find((item) => item.label === 'Good')!;

    expect(dupItems).toHaveLength(2);
    expect(dupItems.every((item) => item.pinnable === false)).toBe(true);
    expect(goodItem.pinnable).toBe(true);
    expect(goodItem.id).toBe('plugin:good-plugin:/v0/resource/plugins/good-plugin/view.html');
  });

  it('projects pinned items in exact order, hides empty source groups, and restores original order on unpin', () => {
    const model = buildCanonicalNavModel({
      t,
      icons,
      featureAvailability: { requestMonitoringAvailable: true, modelPricesAvailable: true },
      hasHostRoutes: true,
      pluginControlMenuVisible: false,
      pluginResources: [],
      apiBase: '',
      renderPluginIcon: () => 'plugin-icon',
    });

    // Pin the single-item clients group entry plus a system entry
    const projected = projectNavSections({
      canonicalSections: model.canonicalSections,
      pinnedIds: [CANONICAL_NAV_IDS.logs, CANONICAL_NAV_IDS.apiKeys, 'unknown.id'],
      enablePinnedNav: true,
      pinnedSectionLabel: 'Pinned',
      pinnedSectionIcon: icons.pinned,
    });

    expect(projected.hasVisiblePinnedItems).toBe(true);
    expect(projected.displaySections.map((s) => s.id)).toEqual([
      PINNED_SECTION_ID,
      'resources',
      'observe',
      'system',
    ]);
    // Pinned section follows pin order, not canonical order
    expect(projected.displaySections[0].items.map((item) => item.id)).toEqual([
      CANONICAL_NAV_IDS.logs,
      CANONICAL_NAV_IDS.apiKeys,
    ]);
    // No duplication in remaining sections
    const systemSection = projected.displaySections.find((s) => s.id === 'system')!;
    expect(systemSection.items.map((item) => item.id)).not.toContain(CANONICAL_NAV_IDS.logs);
    // Canonical order stays unaffected
    expect(model.canonicalNavOrder).toContain('/api-keys');
    expect(model.canonicalNavOrder.indexOf('/api-keys')).toBeLessThan(
      model.canonicalNavOrder.indexOf('/logs')
    );

    // Canonical vs display section lookup diverge intentionally
    expect(findNavSectionByItemId(model.canonicalSections, CANONICAL_NAV_IDS.apiKeys)?.id).toBe(
      'clients'
    );
    expect(findNavSectionByItemId(projected.displaySections, CANONICAL_NAV_IDS.apiKeys)?.id).toBe(
      PINNED_SECTION_ID
    );

    // Unpinning restores clients group in its original slot
    const afterUnpin = projectNavSections({
      canonicalSections: model.canonicalSections,
      pinnedIds: [CANONICAL_NAV_IDS.logs],
      enablePinnedNav: true,
      pinnedSectionLabel: 'Pinned',
      pinnedSectionIcon: icons.pinned,
    });
    expect(afterUnpin.displaySections.map((s) => s.id)).toEqual([
      PINNED_SECTION_ID,
      'resources',
      'clients',
      'observe',
      'system',
    ]);
  });

  it('never leaks capability-hidden or disabled plugin items and restores them when capability returns', () => {
    const pluginPinId = 'plugin:demo:/v0/resource/plugins/demo/app.html';
    const pinnedIds = [CANONICAL_NAV_IDS.usageAnalytics, pluginPinId];

    const restricted = buildCanonicalNavModel({
      t,
      icons,
      featureAvailability: { requestMonitoringAvailable: false, modelPricesAvailable: false },
      hasHostRoutes: true,
      pluginControlMenuVisible: false,
      pluginResources: [],
      apiBase: '',
      renderPluginIcon: () => 'plugin-icon',
    });

    const restrictedProjection = projectNavSections({
      canonicalSections: restricted.canonicalSections,
      pinnedIds,
      enablePinnedNav: true,
      pinnedSectionLabel: 'Pinned',
      pinnedSectionIcon: icons.pinned,
    });
    expect(restrictedProjection.hasVisiblePinnedItems).toBe(false);
    expect(restrictedProjection.displaySections.some((s) => s.id === PINNED_SECTION_ID)).toBe(false);

    const restored = buildCanonicalNavModel({
      t,
      icons,
      featureAvailability: { requestMonitoringAvailable: true, modelPricesAvailable: false },
      hasHostRoutes: true,
      pluginControlMenuVisible: true,
      pluginResources: [makePluginResource('demo', 0, '/v0/resource/plugins/demo/app.html', 'Demo App')],
      apiBase: '',
      renderPluginIcon: () => 'plugin-icon',
    });

    const restoredProjection = projectNavSections({
      canonicalSections: restored.canonicalSections,
      pinnedIds,
      enablePinnedNav: true,
      pinnedSectionLabel: 'Pinned',
      pinnedSectionIcon: icons.pinned,
    });
    expect(restoredProjection.hasVisiblePinnedItems).toBe(true);
    expect(restoredProjection.displaySections[0].items.map((i) => i.id)).toEqual([
      CANONICAL_NAV_IDS.usageAnalytics,
      pluginPinId,
    ]);
  });

  it('returns canonical sections untouched when enablePinnedNav is false (Panel Lite)', () => {
    const model = buildCanonicalNavModel({
      t,
      icons,
      featureAvailability: { requestMonitoringAvailable: true, modelPricesAvailable: true },
      hasHostRoutes: false,
      pluginControlMenuVisible: false,
      pluginResources: [],
      apiBase: '',
      renderPluginIcon: () => 'plugin-icon',
    });

    const projected = projectNavSections({
      canonicalSections: model.canonicalSections,
      pinnedIds: [CANONICAL_NAV_IDS.accounts, CANONICAL_NAV_IDS.logs],
      enablePinnedNav: false,
      pinnedSectionLabel: 'Pinned',
      pinnedSectionIcon: icons.pinned,
    });

    expect(projected.hasVisiblePinnedItems).toBe(false);
    expect(projected.pinnedItemIds.size).toBe(0);
    expect(projected.displaySections).toEqual(model.canonicalSections);
    expect(model.dashboardItem.pinnable).toBe(false);
  });
});
