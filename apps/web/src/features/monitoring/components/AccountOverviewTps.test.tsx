import { renderToStaticMarkup } from 'react-dom/server';
import type { TFunction } from 'i18next';
import { describe, expect, it, vi } from 'vitest';
import type { MonitoringAccountRow } from '../model/types';
import { buildAccountOverviewColumns } from '../model/monitoringCenterPageModel';
import { AccountOverviewPanel } from './AccountOverviewPanel';
import { AccountModelUsageTable } from './AccountOverviewCard';

const t = ((key: string) => key) as TFunction;
const statusData = { blocks: [], blockDetails: [], successRate: 1, totalSuccess: 2, totalFailure: 0 };
const makeRow = (value: number | null): MonitoringAccountRow => ({
  id: 'account', account: 'test@example.com', displayAccount: 'test@example.com',
  accountMasked: 't***@example.com', authLabels: [], authIndices: [], channels: [],
  totalCalls: 2, successCalls: 2, failureCalls: 0, successRate: 1,
  inputTokens: 0, outputTokens: 200, cachedTokens: 0, cacheReadTokens: 0,
  cacheCreationTokens: 0, totalTokens: 200, totalCost: 0, averageLatencyMs: 5500,
  averageOutputTps: value, lastSeenAt: 1, recentPattern: [],
  models: [{
    model: 'test-model', totalCalls: 2, successCalls: 2, failureCalls: 0,
    successRate: 1, inputTokens: 0, outputTokens: 200, cachedTokens: 0,
    cacheReadTokens: 0, cacheCreationTokens: 0, totalTokens: 200, totalCost: 0,
    averageOutputTps: value, lastSeenAt: 1,
  }],
});

const renderPanel = (mode: 'table' | 'card', row: MonitoringAccountRow) => renderToStaticMarkup(
  <AccountOverviewPanel
    mode={mode} accountDisplayMode="full" searchInput=""
    columns={buildAccountOverviewColumns(t)} rows={[row]}
    pagination={{ currentPage: 1, totalPages: 1, pageItems: [row], startItem: 1, endItem: 1 }}
    accountSort={{ key: 'lastSeenAt', direction: 'desc' }} accountSortOptions={[]}
    expandedAccounts={{ [row.id]: true }} focusedAccountId={null}
    accountAuthStateByRowId={new Map()} accountStatusDataByRowId={new Map()}
    emptyAccountStatusData={statusData} accountQuotaStatesByRowId={{}}
    accountPageSize={12} accountPageSizeOptions={[12]} accountOverviewScopeText="scope"
    hasPrices={false} overallLoading={false} locale="en-US" emptyState="empty" t={t}
    onSearchChange={vi.fn()} onRefreshAll={vi.fn()} onAccountSortKeyChange={vi.fn()}
    onModeChange={vi.fn()} onAccountDisplayModeChange={vi.fn()} onAccountSort={vi.fn()}
    onLoadAccountQuota={vi.fn()} onToggleExpanded={vi.fn()} onFocusAccount={vi.fn()}
    onPageChange={vi.fn()} onPageSizeChange={vi.fn()}
  />
);

describe('account average output TPS components', () => {
  it.each(['table', 'card'] as const)('shows account and expanded model TPS in %s mode', (mode) => {
    const html = renderPanel(mode, makeRow(55));
    expect(html.match(/55\.00/g)).toHaveLength(2);
    expect(html).toContain('monitoring.average_output_tps_hint');
    expect(html).toContain('test-model');
  });

  it.each(['table', 'card'] as const)('shows em dashes for no valid samples in %s mode', (mode) => {
    const html = renderPanel(mode, makeRow(null));
    expect(html.match(/—/g)).toHaveLength(2);
    expect(html).not.toContain('55.00');
    expect(renderPanel(mode, { ...makeRow(null), models: [] })).toContain('monitoring.account_overview_no_models');
  });

  it('does not add TPS to the shared API-key model table by default', () => {
    const html = renderToStaticMarkup(
      <AccountModelUsageTable row={makeRow(55)} hasPrices={false} locale="en-US" t={t} />
    );
    expect(html).not.toContain('monitoring.average_output_tps');
    expect(html).not.toContain('55.00');
  });

  it('adds a display-only column without sorting', () => {
    const column = buildAccountOverviewColumns(t).find((item) => item.key === 'average-output-tps');
    expect(column).toBeDefined();
    expect(column?.sortKey).toBeUndefined();
  });
});
