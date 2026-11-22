import type { TFunction } from 'i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { SegmentedTabs } from '@/components/ui/SegmentedTabs';
import { Select, type SelectOption } from '@/components/ui/Select';
import { IconRefreshCw, IconSearch, IconSlidersHorizontal, IconTimer } from '@/components/ui/icons';
import type { MonitoringTimeRange } from '@/features/monitoring/hooks/useMonitoringData';
import styles from '../MonitoringCenterPage.module.scss';

type MonitoringFiltersPanelProps = {
  timeRange: MonitoringTimeRange;
  autoRefreshMs: string;
  selectedAccount: string;
  selectedProvider: string;
  selectedModel: string;
  selectedChannel: string;
  selectedApiKeyHash: string;
  selectedStatus: string;
  searchInput: string;
  accountOptions: ReadonlyArray<SelectOption>;
  providerOptions: ReadonlyArray<SelectOption>;
  modelOptions: ReadonlyArray<SelectOption>;
  channelOptions: ReadonlyArray<SelectOption>;
  apiKeyOptions: ReadonlyArray<SelectOption>;
  statusOptions: ReadonlyArray<SelectOption>;
  combinedError: string | null;
  usageStatisticsEnabled: boolean;
  overallLoading: boolean;
  t: TFunction;
  onTimeRangeChange: (value: MonitoringTimeRange) => void;
  onAutoRefreshChange: (value: string) => void;
  onRefreshAll: () => void | Promise<void>;
  onAccountFilterChange: (value: string) => void;
  onProviderChange: (value: string) => void;
  onModelChange: (value: string) => void;
  onChannelChange: (value: string) => void;
  onApiKeyChange: (value: string) => void;
  onStatusChange: (value: string) => void;
  onSearchChange: (value: string) => void;
  onClearFilters: () => void;
};

const TIME_RANGE_OPTIONS: Array<{ value: MonitoringTimeRange; labelKey: string }> = [
  { value: 'today', labelKey: 'monitoring.range_today' },
  { value: 'yesterday', labelKey: 'monitoring.range_yesterday' },
  { value: '7d', labelKey: 'monitoring.range_7d' },
  { value: '14d', labelKey: 'monitoring.range_14d' },
  { value: '30d', labelKey: 'monitoring.range_30d' },
  { value: 'all', labelKey: 'monitoring.range_all' },
  { value: 'custom', labelKey: 'monitoring.range_custom' },
];

const AUTO_REFRESH_OPTIONS = [
  { value: '0', labelKey: 'monitoring.auto_refresh_off' },
  { value: '5000', labelKey: 'monitoring.auto_refresh_5s' },
  { value: '10000', labelKey: 'monitoring.auto_refresh_10s' },
  { value: '30000', labelKey: 'monitoring.auto_refresh_30s' },
  { value: '60000', labelKey: 'monitoring.auto_refresh_60s' },
  { value: '300000', labelKey: 'monitoring.auto_refresh_5m' },
];

const shortLabel = (t: TFunction, shortKey: string, fallbackKey: string) => {
  const fallback = t(fallbackKey);
  const label = t(shortKey, { defaultValue: fallback });
  return label === shortKey ? fallback : label;
};

export function MonitoringFiltersPanel({
  timeRange,
  autoRefreshMs,
  selectedAccount,
  selectedProvider,
  selectedModel,
  selectedChannel,
  selectedApiKeyHash,
  selectedStatus,
  searchInput,
  accountOptions,
  providerOptions,
  modelOptions,
  channelOptions,
  apiKeyOptions,
  statusOptions,
  combinedError,
  usageStatisticsEnabled,
  overallLoading,
  t,
  onTimeRangeChange,
  onAutoRefreshChange,
  onRefreshAll,
  onAccountFilterChange,
  onProviderChange,
  onModelChange,
  onChannelChange,
  onApiKeyChange,
  onStatusChange,
  onSearchChange,
  onClearFilters,
}: MonitoringFiltersPanelProps) {
  const autoRefreshLabel = shortLabel(
    t,
    'monitoring.auto_refresh_short',
    'monitoring.auto_refresh'
  );
  const clearFiltersLabel = shortLabel(
    t,
    'monitoring.clear_filters_short',
    'monitoring.clear_filters'
  );

  return (
    <div className={styles.filterToolbar}>
      <div className={styles.controlBar}>
        <SegmentedTabs<MonitoringTimeRange>
          variant="joined"
          roleMode="group"
          allowReselect
          responsiveFullWidth={false}
          ariaLabel={t('monitoring.time_range_label', { defaultValue: '时间范围' })}
          activeTab={timeRange}
          onChange={onTimeRangeChange}
          items={TIME_RANGE_OPTIONS.map((option) => ({
            id: option.value,
            label: t(option.labelKey),
          }))}
        />

        <div className={styles.filterSearchInputWrap}>
          <Input
            value={searchInput}
            onChange={(event) => onSearchChange(event.target.value)}
            placeholder={t('monitoring.search_placeholder')}
            className={styles.filterSearchInput}
            rightElement={<IconSearch size={16} />}
            aria-label={t('monitoring.search_placeholder')}
          />
        </div>

        <div className={styles.refreshControls}>
          <div className={styles.autoRefreshField}>
            <span className={styles.autoRefreshLabel} title={t('monitoring.auto_refresh')}>
              <IconTimer size={15} />
              {autoRefreshLabel}
            </span>
            <Select
              className={styles.autoRefreshSelect}
              triggerClassName={styles.autoRefreshSelectTrigger}
              value={autoRefreshMs}
              options={AUTO_REFRESH_OPTIONS.map((option) => ({
                value: option.value,
                label: t(option.labelKey),
              }))}
              onChange={onAutoRefreshChange}
              ariaLabel={t('monitoring.auto_refresh')}
              fullWidth={false}
            />
          </div>

          <Button
            variant="secondary"
            size="sm"
            onClick={() => void onRefreshAll()}
            disabled={overallLoading}
          >
            <IconRefreshCw
              size={15}
              className={overallLoading ? styles.refreshIconSpinning : styles.refreshIcon}
            />
            <span className={styles.refreshButtonLabel}>{t('usage_stats.refresh')}</span>
          </Button>

          <Button
            variant="secondary"
            size="sm"
            onClick={onClearFilters}
            title={t('monitoring.clear_filters')}
            aria-label={t('monitoring.clear_filters')}
          >
            <IconSlidersHorizontal size={15} />
            <span>{clearFiltersLabel}</span>
          </Button>
        </div>
      </div>
      <div className={styles.filterBar}>
        <div className={styles.filterGrid}>
          <div className={styles.filterAccountStack}>
            <Select
              value={selectedAccount}
              options={accountOptions}
              onChange={onAccountFilterChange}
              ariaLabel={t('monitoring.filter_account')}
              triggerClassName={styles.filterSelectTrigger}
            />
          </div>
          <Select
            value={selectedProvider}
            options={providerOptions}
            onChange={onProviderChange}
            ariaLabel={t('monitoring.filter_provider')}
            triggerClassName={styles.filterSelectTrigger}
          />
          <Select
            value={selectedModel}
            options={modelOptions}
            onChange={onModelChange}
            ariaLabel={t('monitoring.filter_model')}
            triggerClassName={styles.filterSelectTrigger}
          />
          <Select
            value={selectedChannel}
            options={channelOptions}
            onChange={onChannelChange}
            ariaLabel={t('monitoring.filter_channel')}
            triggerClassName={styles.filterSelectTrigger}
          />
          <Select
            value={selectedApiKeyHash}
            options={apiKeyOptions}
            onChange={onApiKeyChange}
            ariaLabel={t('monitoring.filter_api_key')}
            triggerClassName={styles.filterSelectTrigger}
          />
          <Select
            value={selectedStatus}
            options={statusOptions}
            onChange={onStatusChange}
            ariaLabel={t('monitoring.filter_status')}
            triggerClassName={styles.filterSelectTrigger}
          />
        </div>
      </div>

      {combinedError ? <div className={styles.errorBox}>{combinedError}</div> : null}
      {!usageStatisticsEnabled ? (
        <div className={styles.callout}>
          <strong>{t('monitoring.usage_disabled_title')}</strong>
          <span>{t('monitoring.usage_disabled_body')}</span>
        </div>
      ) : null}
    </div>
  );
}
