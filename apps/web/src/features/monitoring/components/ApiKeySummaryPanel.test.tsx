import { act } from 'react';
import { create, type ReactTestRenderer } from 'react-test-renderer';
import type { TFunction } from 'i18next';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { MonitoringApiKeyRow } from '@/features/monitoring/hooks/useMonitoringData';
import { ApiKeySummaryPanel } from './ApiKeySummaryPanel';

const { copyToClipboard } = vi.hoisted(() => ({
  copyToClipboard: vi.fn(async () => true),
}));

vi.mock('@/utils/clipboard', () => ({ copyToClipboard }));

const t = ((key: string) => (key === 'common.copy' ? 'Copy' : key)) as unknown as TFunction;

const createRow = (overrides: Partial<MonitoringApiKeyRow> = {}): MonitoringApiKeyRow => ({
  id: 'hash-a',
  apiKeyHash: 'hash-a',
  apiKeyLabel: 'Team A',
  apiKeyMasked: 'sk********aa',
  apiKeyCopyValue: 'sk-original-aa',
  isUnknown: false,
  authLabels: [],
  sourceLabels: [],
  channels: [],
  totalCalls: 2,
  successCalls: 2,
  failureCalls: 0,
  successRate: 1,
  inputTokens: 10,
  outputTokens: 5,
  cachedTokens: 0,
  cacheReadTokens: 0,
  cacheCreationTokens: 0,
  totalTokens: 15,
  totalCost: 0,
  averageLatencyMs: null,
  lastSeenAt: 1_780_000_000_000,
  models: [],
  ...overrides,
});

const renderPanel = (row: MonitoringApiKeyRow) => {
  let renderer!: ReactTestRenderer;
  act(() => {
    renderer = create(
      <ApiKeySummaryPanel
        rows={[row]}
        columns={[{ key: 'api-key', label: 'API Key' }]}
        pagination={{
          currentPage: 1,
          totalPages: 1,
          pageItems: [row],
          startItem: 1,
          endItem: 1,
        }}
        expandedApiKeys={{}}
        hasPrices={false}
        locale="en-US"
        pageSize={10}
        pageSizeOptions={[10]}
        emptyState="Empty"
        t={t}
        onToggleApiKey={vi.fn()}
        onPageChange={vi.fn()}
        onPageSizeChange={vi.fn()}
      />
    );
  });
  return renderer;
};

beforeEach(() => {
  copyToClipboard.mockReset();
  copyToClipboard.mockResolvedValue(true);
});

describe('ApiKeySummaryPanel', () => {
  it('copies the original configured API key instead of its masked label', async () => {
    const renderer = renderPanel(createRow());
    const copyButton = renderer.root
      .findAllByType('button')
      .find((node) => node.props['aria-label'] === 'Copy');
    if (!copyButton) throw new Error('API key copy button not found');

    act(() => {
      copyButton.props.onClick();
    });
    await act(async () => Promise.resolve());

    expect(copyToClipboard).toHaveBeenCalledWith('sk-original-aa');
    expect(copyToClipboard).not.toHaveBeenCalledWith('sk********aa');
  });

  it('does not offer a misleading copy button when the original key is unavailable', () => {
    const renderer = renderPanel(createRow({ apiKeyCopyValue: undefined }));

    expect(
      renderer.root.findAllByType('button').some((node) => node.props['aria-label'] === 'Copy')
    ).toBe(false);
  });
});
