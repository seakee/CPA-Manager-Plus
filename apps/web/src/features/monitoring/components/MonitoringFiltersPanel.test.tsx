import { renderToStaticMarkup } from 'react-dom/server';
import type { TFunction } from 'i18next';
import { describe, expect, it } from 'vitest';
import { MonitoringFiltersPanel } from './MonitoringFiltersPanel';

const t = ((key: string) => key) as TFunction;

describe('MonitoringFiltersPanel time ranges', () => {
  it('places yesterday after today in the Request Monitoring presets', () => {
    const markup = renderToStaticMarkup(
      <MonitoringFiltersPanel
        timeRange="today"
        autoRefreshMs="30000"
        selectedAccount="all"
        selectedProvider="all"
        selectedModel="all"
        selectedChannel="all"
        selectedApiKeyHash="all"
        selectedStatus="all"
        searchInput=""
        accountOptions={[]}
        providerOptions={[]}
        modelOptions={[]}
        channelOptions={[]}
        apiKeyOptions={[]}
        statusOptions={[]}
        combinedError={null}
        usageStatisticsEnabled
        overallLoading={false}
        t={t}
        onTimeRangeChange={() => {}}
        onAutoRefreshChange={() => {}}
        onRefreshAll={() => {}}
        onAccountFilterChange={() => {}}
        onProviderChange={() => {}}
        onModelChange={() => {}}
        onChannelChange={() => {}}
        onApiKeyChange={() => {}}
        onStatusChange={() => {}}
        onSearchChange={() => {}}
        onClearFilters={() => {}}
      />
    );

    const rangeLabels = [
      'monitoring.range_24h',
      'monitoring.range_today',
      'monitoring.range_yesterday',
      'monitoring.range_7d',
      'monitoring.range_14d',
      'monitoring.range_30d',
      'monitoring.range_all',
      'monitoring.range_custom',
    ];
    const labelPositions = rangeLabels.map((label) => markup.indexOf(`>${label}<`));

    expect(labelPositions.every((position) => position >= 0)).toBe(true);
    expect(labelPositions).toEqual([...labelPositions].sort((left, right) => left - right));
  });
});
