import { renderToStaticMarkup } from 'react-dom/server';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import i18next from 'i18next';
import type { UsageServiceStatus } from '@/services/api/usageService';
import en from '@/i18n/locales/en.json';
import ru from '@/i18n/locales/ru.json';
import zhCN from '@/i18n/locales/zh-CN.json';
import zhTW from '@/i18n/locales/zh-TW.json';
import { DatabaseMaintenanceBanner } from './DatabaseMaintenanceBanner';

const mocks = vi.hoisted(() => ({ status: null as UsageServiceStatus | null }));

vi.mock('./useDatabaseMaintenance', () => ({
  useDatabaseMaintenance: () => ({
    status: mocks.status,
    loading: false,
    error: '',
    refresh: vi.fn(),
  }),
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, values?: Record<string, unknown>) =>
      values ? `${key}:${Object.values(values).join('|')}` : key,
    i18n: { language: 'en-US' },
  }),
}));

const degradedStatus = (): UsageServiceStatus => ({
  databaseMaintenance: {
    required: true,
    performanceDegraded: true,
    deferredIndexes: 10,
    offlineJobs: 1,
    reasons: ['deferred_indexes', 'offline_derived_cleanup'],
    command: 'cleanup-derived',
  },
});

beforeEach(() => {
  mocks.status = null;
});

describe('DatabaseMaintenanceBanner', () => {
  it('renders a warning, formatted counts, offline steps, and the data-safety message', () => {
    mocks.status = degradedStatus();

    const markup = renderToStaticMarkup(<DatabaseMaintenanceBanner />);

    expect(markup).toContain('database_maintenance.title');
    expect(markup).toContain('database_maintenance.query_index_count');
    expect(markup).toContain('database_maintenance.offline_job_count');
    expect(markup).toContain('database_maintenance.summary_both');
    expect(markup).toContain('database_maintenance.data_safe');
    expect(markup).toContain('docker compose stop cpa-manager-plus');
    expect(markup).toContain('cleanup-derived --db-path /data/usage.sqlite');
    expect(markup).toContain('cpa-manager-plus cleanup-derived');
    expect(markup).not.toContain('idx_usage_');
  });

  it('does not render when maintenance is clean or unavailable', () => {
    expect(renderToStaticMarkup(<DatabaseMaintenanceBanner />)).toBe('');

    mocks.status = { service: 'cpa-manager-plus' };
    expect(renderToStaticMarkup(<DatabaseMaintenanceBanner />)).toBe('');

    mocks.status = {
      databaseMaintenance: {
        required: false,
        performanceDegraded: false,
        deferredIndexes: 0,
        offlineJobs: 0,
        reasons: [],
      },
    };
    expect(renderToStaticMarkup(<DatabaseMaintenanceBanner />)).toBe('');
  });

  it('keeps the maintenance copy complete in every supported locale', () => {
    for (const locale of [en, ru, zhCN, zhTW]) {
      expect(locale.database_maintenance.title).toBeTypeOf('string');
      expect(locale.database_maintenance.body).toBeTypeOf('string');
      expect(locale.database_maintenance.data_safe).toBeTypeOf('string');
      expect(locale.database_maintenance.steps_title).toBeTypeOf('string');
      expect(locale.monitoring.database_maintenance_hint_body).toBeTypeOf('string');
      expect(locale.system_info.database_maintenance_hint).toBeTypeOf('string');
    }
  });

  it('formats English and Chinese maintenance counts without singular/plural ambiguity', async () => {
    const english = i18next.createInstance();
    await english.init({ lng: 'en', resources: { en: { translation: en } } });
    expect(
      english.t('database_maintenance.query_index_count', { count: 1, formattedCount: '1' })
    ).toBe('1 query index');
    expect(
      english.t('database_maintenance.offline_job_count', { count: 1, formattedCount: '1' })
    ).toBe('1 offline cleanup job');
    expect(
      english.t('database_maintenance.offline_job_count', { count: 2, formattedCount: '2' })
    ).toBe('2 offline cleanup jobs');

    const chinese = i18next.createInstance();
    await chinese.init({ lng: 'zh-CN', resources: { 'zh-CN': { translation: zhCN } } });
    expect(
      chinese.t('database_maintenance.query_index_count', { count: 10, formattedCount: '10' })
    ).toBe('10 个查询索引');
    expect(
      chinese.t('database_maintenance.offline_job_count', { count: 1, formattedCount: '1' })
    ).toBe('1 个离线清理任务');
  });
});
