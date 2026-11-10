import { describe, expect, it } from 'vitest';
import type { ManagerConfig } from '@/services/api/usageService';
import { buildRequestMonitoringConfig } from './requestMonitoringConfig';

const storedConfig: ManagerConfig = {
  cpaConnection: {
    cpaBaseUrl: 'http://cpa.internal:8317',
    managementKeyConfigured: true,
  },
  collector: {
    enabled: true,
    collectorMode: 'resp',
    queue: 'usage-events',
    popSide: 'right',
    batchSize: 250,
    pollIntervalMs: 1500,
    queryLimit: 20000,
    tlsSkipVerify: true,
  },
  codexInspection: {
    enabled: true,
    schedule: { mode: 'time_points', timePoints: ['03:00'], intervalMinutes: 90, timeZone: 'UTC' },
    workers: 4,
    usedPercentThreshold: 82,
  },
  externalUsageService: {
    enabled: true,
    serviceBase: 'https://usage.example.com',
  },
  updatedAtMs: 1_700_000_000_000,
};

describe('buildRequestMonitoringConfig', () => {
  it('changes only collector.enabled and returns every other stored field', () => {
    const next = buildRequestMonitoringConfig(storedConfig, false);

    expect(next).toEqual({
      ...storedConfig,
      collector: { ...storedConfig.collector, enabled: false },
    });
    expect(next.cpaConnection).toEqual(storedConfig.cpaConnection);
    expect(next.collector.collectorMode).toBe('resp');
    expect(next.collector.queue).toBe('usage-events');
    expect(next.collector.popSide).toBe('right');
    expect(next.collector.batchSize).toBe(250);
    expect(next.collector.pollIntervalMs).toBe(1500);
    expect(next.collector.queryLimit).toBe(20000);
    expect(next.collector.tlsSkipVerify).toBe(true);
    expect(next.codexInspection).toEqual(storedConfig.codexInspection);
    expect(next.externalUsageService).toEqual({
      enabled: true,
      serviceBase: 'https://usage.example.com',
    });
  });

  it('keeps the same shape when monitoring is switched back on', () => {
    const disabled = buildRequestMonitoringConfig(storedConfig, false);
    const enabled = buildRequestMonitoringConfig(disabled, true);

    expect(enabled).toEqual(storedConfig);
    // The stored object is never mutated, so a failed write cannot leave a
    // half-switched config behind.
    expect(storedConfig.collector.enabled).toBe(true);
  });
});
