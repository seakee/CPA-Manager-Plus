import type { ReactNode } from 'react';
import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { AuthFileItem, CodexQuotaState } from '@/types';
import type { MonitoringAnalyticsCredentialStatRow } from '@/services/api/usageService';
import { QuotaPage } from './QuotaPage';

const { mocks } = vi.hoisted(() => ({
  mocks: {
    list: vi.fn(),
    fetchConfigYaml: vi.fn(),
    getHeaderSnapshots: vi.fn(),
    loadUsage: vi.fn(),
    t: (key: string, options?: Record<string, unknown>) =>
      key === 'codex_quota.aggregate_coverage'
        ? `${options?.estimated}/${options?.total}`
        : key,
    codexQuota: {} as Record<string, CodexQuotaState>,
    usageRows: {
      retained: [] as MonitoringAnalyticsCredentialStatRow[],
      fiveHour: [] as MonitoringAnalyticsCredentialStatRow[],
      weekly: [] as MonitoringAnalyticsCredentialStatRow[],
    },
  },
}));

vi.mock('react-i18next', () => ({
  initReactI18next: { type: '3rdParty', init: () => {} },
  useTranslation: () => ({
    t: mocks.t,
  }),
}));

vi.mock('@/hooks/useHeaderRefresh', () => ({
  useHeaderRefresh: () => {},
}));

vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => ({
    managerServiceBase: 'http://manager.local:18317',
    requestMonitoringAvailable: true,
  }),
}));

vi.mock('@/stores', () => ({
  useAuthStore: (selector: (state: unknown) => unknown) =>
    selector({ connectionStatus: 'connected', managementKey: 'test-key' }),
  useQuotaStore: (selector: (state: unknown) => unknown) =>
    selector({ codexQuota: mocks.codexQuota }),
}));

vi.mock('@/services/api', () => ({
  authFilesApi: { list: mocks.list },
  configFileApi: { fetchConfigYaml: mocks.fetchConfigYaml },
}));

vi.mock('@/services/api/usageService', () => ({
  monitoringAnalyticsApi: { getHeaderSnapshots: mocks.getHeaderSnapshots },
}));

vi.mock('@/features/authFiles/hooks/useAuthFileUsageAnalytics', () => ({
  useAuthFileUsageAnalytics: () => ({
    rows: mocks.usageRows,
    loading: false,
    load: mocks.loadUsage,
  }),
}));

vi.mock('@/components/quota', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/components/quota')>();
  return {
    ...actual,
    QuotaSection: ({
      config,
      summary,
    }: {
      config: { type: string };
      summary?: ReactNode;
    }) => <section data-quota-section={config.type}>{summary}</section>,
  };
});

vi.mock('@/features/oauth/CodexReauthDialog', () => ({
  CodexReauthDialog: () => null,
}));

const credentialRow = (
  file: AuthFileItem,
  totalTokens: number,
  cost: number
): MonitoringAnalyticsCredentialStatRow => ({
  id: `${file.name}-${totalTokens}`,
  auth_file_snapshot: file.name,
  auth_index: file.authIndex == null ? undefined : String(file.authIndex),
  calls: 1,
  success_calls: 1,
  failure_calls: 0,
  success_rate: 1,
  input_tokens: 0,
  output_tokens: 0,
  cached_tokens: 0,
  cache_read_tokens: 0,
  cache_creation_tokens: 0,
  total_tokens: totalTokens,
  cost,
  average_latency_ms: null,
  last_seen_ms: 0,
});

const readText = (node: ReactTestInstance): string =>
  node.children
    .map((child) => (typeof child === 'string' ? child : readText(child)))
    .join('');

describe('QuotaPage Codex aggregate summary', () => {
  beforeEach(() => {
    (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT = true;
    mocks.list.mockReset();
    mocks.fetchConfigYaml.mockReset();
    mocks.getHeaderSnapshots.mockReset();
    mocks.loadUsage.mockReset();
    mocks.codexQuota = {};
    mocks.usageRows.retained = [];
    mocks.usageRows.fiveHour = [];
    mocks.usageRows.weekly = [];
  });

  it('renders fleet-wide 5-hour and weekly remaining totals below Codex quota', async () => {
    const file: AuthFileItem = { name: 'codex-main.json', type: 'codex', authIndex: '0' };
    mocks.list.mockResolvedValue({ files: [file] });
    mocks.fetchConfigYaml.mockResolvedValue(undefined);
    mocks.getHeaderSnapshots.mockResolvedValue({ items: [] });
    mocks.usageRows.fiveHour = [credentialRow(file, 5_000, 0.25)];
    mocks.usageRows.weekly = [credentialRow(file, 28_000, 2.8)];
    mocks.codexQuota = {
      'codex-main.json::0': {
        status: 'success',
        authFileKey: 'codex-main.json::0',
        authFileName: file.name,
        authIndex: '0',
        windows: [
          {
            id: 'five-hour',
            label: '5-hour limit',
            usedPercent: 25,
            resetLabel: 'soon',
            limitWindowSeconds: 18_000,
          },
          {
            id: 'weekly',
            label: 'Weekly limit',
            usedPercent: 40,
            resetLabel: 'later',
            limitWindowSeconds: 604_800,
          },
        ],
      },
    };

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<QuotaPage />);
      await Promise.resolve();
      await Promise.resolve();
    });

    const codexSection = renderer.root.findByProps({ 'data-quota-section': 'codex' });
    const fiveHour = codexSection.findByProps({
      'data-codex-aggregate-window': 'five-hour',
    });
    const weekly = codexSection.findByProps({ 'data-codex-aggregate-window': 'weekly' });

    expect(readText(fiveHour)).toContain('~$0.75');
    expect(readText(fiveHour)).toContain('~15.0K');
    expect(readText(weekly)).toContain('~$4.20');
    expect(readText(weekly)).toContain('~42.0K');
    expect(readText(weekly)).toContain('1/1');
    expect(mocks.loadUsage).toHaveBeenCalledTimes(1);
  });
});
