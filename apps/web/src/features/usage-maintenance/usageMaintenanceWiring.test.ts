import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import layoutSource from '@/components/layout/MainLayout.tsx?raw';
import routesSource from '@/router/MainRoutes.tsx?raw';
import en from '@/i18n/locales/en.json';
import ru from '@/i18n/locales/ru.json';
import zhCN from '@/i18n/locales/zh-CN.json';
import zhTW from '@/i18n/locales/zh-TW.json';
import {
  createDemoUsageArchive,
  getDemoUsageArchives,
  getDemoUsageMaintenance,
  resetDemoUsageArchiveState,
  resumeDemoUsageArchive,
} from '@/features/demo/demoFixtures';

beforeEach(() => resetDemoUsageArchiveState());
afterEach(() => resetDemoUsageArchiveState());

const collectKeys = (value: unknown, result = new Set<string>()): Set<string> => {
  if (Array.isArray(value)) {
    value.forEach((item) => collectKeys(item, result));
    return result;
  }
  if (!value || typeof value !== 'object') return result;
  for (const [key, child] of Object.entries(value)) {
    result.add(key);
    collectKeys(child, result);
  }
  return result;
};

describe('usage maintenance app wiring', () => {
  it('registers the route behind Manager Embedded and Manager Service availability checks', () => {
    expect(routesSource).toContain(
      "import { UsageMaintenancePage } from '@/pages/UsageMaintenancePage';"
    );
    const gateStart = routesSource.indexOf('function UsageMaintenanceGate');
    const gateEnd = routesSource.indexOf('function LogsGate');
    const gateSource = routesSource.slice(gateStart, gateEnd);
    const routeStart = routesSource.indexOf("path: '/usage-maintenance'");
    const routeEnd = routesSource.indexOf("path: '/monitoring/account-actions'");
    const routeSource = routesSource.slice(routeStart, routeEnd);

    expect(gateStart).toBeGreaterThanOrEqual(0);
    expect(gateSource).toContain("availability.panelHostMode !== 'manager_embedded'");
    expect(gateSource).toContain('!availability.managerServiceAvailable');
    expect(routeStart).toBeGreaterThanOrEqual(0);
    expect(routeSource).toContain('<UsageMaintenanceGate>');
    expect(routeSource).toContain('<UsageMaintenancePage />');
  });

  it('only exposes the sidebar entry for an available Manager Embedded service', () => {
    const itemStart = layoutSource.indexOf('const usageMaintenanceNavItem');
    const itemEnd = layoutSource.indexOf('const operationNavItems');
    const itemSource = layoutSource.slice(itemStart, itemEnd);
    const monitoringIndex = layoutSource.indexOf(
      '...(monitoringNavItem ? [monitoringNavItem] : [])'
    );
    const maintenanceIndex = layoutSource.indexOf(
      '...(usageMaintenanceNavItem ? [usageMaintenanceNavItem] : [])'
    );

    expect(itemStart).toBeGreaterThanOrEqual(0);
    expect(itemSource).toContain("featureAvailability.panelHostMode === 'manager_embedded'");
    expect(itemSource).toContain('featureAvailability.managerServiceAvailable');
    expect(itemSource).toContain("path: '/usage-maintenance'");
    expect(maintenanceIndex).toBeGreaterThan(monitoringIndex);
  });

  it('keeps all supported locales aligned', () => {
    const expectedKeys = Object.keys(en.usage_maintenance).sort();
    for (const locale of [ru, zhCN, zhTW]) {
      expect(Object.keys(locale.usage_maintenance).sort()).toEqual(expectedKeys);
      expect(locale.nav.usage_maintenance).toBeTruthy();
      expect(locale.nav.usage_maintenance_short).toBeTruthy();
    }
  });

  it('keeps demo archive responses on the sanitized public contract', () => {
    const created = createDemoUsageArchive(Date.now() - 30 * 24 * 60 * 60 * 1000);
    const archived = resumeDemoUsageArchive(created.run.id);
    const archiveKeys = collectKeys({ archived, list: getDemoUsageArchives() });
    const maintenanceKeys = collectKeys(getDemoUsageMaintenance());

    for (const forbidden of [
      'schema_version',
      'format',
      'archive_digest',
      'manifest_file',
      'manifest_sha256',
      'file_name',
      'content_sha256',
      'event_hash_digest',
      'last_error',
    ]) {
      expect(archiveKeys.has(forbidden)).toBe(false);
    }
    for (const forbidden of [
      'format',
      'archive_digest',
      'manifest_file',
      'manifest_sha256',
      'file_name',
      'content_sha256',
      'event_hash_digest',
      'last_error',
    ]) {
      expect(maintenanceKeys.has(forbidden)).toBe(false);
    }
  });

  it('resets mutable demo archive state between demo sessions', () => {
    const created = createDemoUsageArchive(Date.now() - 30 * 24 * 60 * 60 * 1000);
    resumeDemoUsageArchive(created.run.id);
    expect(getDemoUsageArchives().runs).toHaveLength(3);

    resetDemoUsageArchiveState();

    expect(getDemoUsageArchives().runs.map((run) => run.id)).toEqual([
      'demo-archive-2',
      'demo-archive-1',
    ]);
    expect(getDemoUsageMaintenance().raw_event_count).toBe(184_260);
  });
});
