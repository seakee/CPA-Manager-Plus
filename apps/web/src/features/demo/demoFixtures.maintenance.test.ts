import { afterEach, describe, expect, it, vi } from 'vitest';
import { getDemoUsageServiceStatus } from './demoFixtures';

const stubLocation = (search = '', hash = '#/demo') => {
  vi.stubGlobal('window', {
    location: { search, hash },
  });
};

describe('database maintenance demo fixture', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('keeps the normal demo clean by default', () => {
    stubLocation('', '#/demo');

    expect(getDemoUsageServiceStatus().databaseMaintenance).toEqual({
      required: false,
      performanceDegraded: false,
      deferredIndexes: 0,
      offlineJobs: 0,
      reasons: [],
    });
  });

  it('exposes the degraded scenario from a query before the hash route', () => {
    stubLocation('?maintenance=degraded', '#/demo/monitoring');

    expect(getDemoUsageServiceStatus().databaseMaintenance).toEqual({
      required: true,
      performanceDegraded: true,
      deferredIndexes: 10,
      offlineJobs: 1,
      reasons: ['deferred_indexes', 'offline_derived_cleanup', 'legacy_index_replacement'],
      command: 'cleanup-derived',
    });
  });

  it('accepts the hash query form for direct scenario links', () => {
    stubLocation('', '#/demo/system?maintenance=degraded');

    expect(getDemoUsageServiceStatus().databaseMaintenance).toMatchObject({
      required: true,
      performanceDegraded: true,
      deferredIndexes: 10,
      offlineJobs: 1,
    });
  });

  it('falls back to clean for unknown or explicit clean scenarios', () => {
    stubLocation('?maintenance=unknown', '#/demo');
    expect(getDemoUsageServiceStatus().databaseMaintenance?.required).toBe(false);

    stubLocation('?maintenance=clean', '#/demo');
    expect(getDemoUsageServiceStatus().databaseMaintenance?.required).toBe(false);
  });
});
