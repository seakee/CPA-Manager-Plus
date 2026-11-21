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

  it('shows a revoked badge instead of available badge and omits duplicate sha256 subtitle when maskedPreview equals label', () => {
    const renderer = renderPanel(
      createRow({
        apiKeyLabel: 'cpamp_sk_…old1',
        apiKeyMasked: 'cpamp_sk_…old1',
        apiKeyDisplayName: 'cpamp_sk_…old1 · Revoked',
        apiKeyIsRevoked: true,
        apiKeyCopyValue: undefined,
      })
    );
    const text = JSON.stringify(renderer.toJSON());
    expect(text).toContain('cpamp_sk_…old1');
    expect(text).not.toContain('sha256:hash-a');
    expect(text).not.toContain('monitoring.api_key_available');
  });

  it('renders rotated badge instead of available badge for rotated historical hashes and suppresses available badge for missing keys', () => {
    const rotatedRenderer = renderPanel(
      createRow({
        apiKeyLabel: 'Production',
        apiKeyMasked: 'cpamp_sk_…old2',
        apiKeyDisplayName: 'Production (cpamp_sk_…old2) · Rotated',
        apiKeyIsRotated: true,
        apiKeyLifecycle: 'rotated',
        apiKeyCopyValue: undefined,
      })
    );
    const rotatedJson = JSON.stringify(rotatedRenderer.toJSON());
    expect(rotatedJson).toContain('Rotated');
    expect(rotatedJson).not.toContain('monitoring.api_key_available');

    const missingRenderer = renderPanel(
      createRow({
        apiKeyLabel: 'Staged Key',
        apiKeyMasked: 'cpamp_sk_…stgd',
        apiKeyDisplayName: 'Staged Key (cpamp_sk_…stgd)',
        apiKeyLifecycle: 'missing',
        apiKeyCopyValue: undefined,
      })
    );
    const missingJson = JSON.stringify(missingRenderer.toJSON());
    expect(missingJson).not.toContain('monitoring.api_key_available');
  });
});
