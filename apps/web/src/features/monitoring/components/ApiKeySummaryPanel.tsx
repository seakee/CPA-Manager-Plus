import { Fragment, type ReactNode } from 'react';
import type { TFunction } from 'i18next';
import { IconChevronDown, IconChevronUp, IconCopy, IconInfo, IconKey } from '@/components/ui/icons';
import type { MonitoringApiKeyRow } from '@/features/monitoring/hooks/useMonitoringData';
import { useNotificationStore } from '@/stores';
import { copyToClipboard } from '@/utils/clipboard';
import { formatCompactNumber, formatUsd } from '@/utils/usage';
import { AccountModelUsageTable, AccountTokenMetricGrid } from './AccountOverviewCard';
import { MonitoringPanel } from './MonitoringPanel';
import { PaginationControls } from './MonitoringShared';
import type { AccountSummaryMetric } from './accountOverviewPresentation';
import styles from '../MonitoringCenterPage.module.scss';

type ApiKeyOverviewColumn = {
  key: string;
  label: string;
  fullLabel?: string;
};

type ApiKeyPaginationState = {
  currentPage: number;
  totalPages: number;
  pageItems: MonitoringApiKeyRow[];
  startItem: number;
  endItem: number;
};

type ApiKeySummaryPanelProps = {
  embedded?: boolean;
  rows: MonitoringApiKeyRow[];
  columns: ApiKeyOverviewColumn[];
  pagination: ApiKeyPaginationState;
  expandedApiKeys: Record<string, boolean>;
  hasPrices: boolean;
  locale: string;
  pageSize: number;
  pageSizeOptions: readonly number[];
  emptyState: ReactNode;
  t: TFunction;
  onToggleApiKey: (apiKeyId: string) => void;
  onPageChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
};

export type ApiKeySummaryPanelActionsProps = {
  rowCount: number;
  t: TFunction;
};

const joinShort = (values: string[], limit = 2) => {
  if (values.length <= limit) {
    return values.join(', ');
  }
  return `${values.slice(0, limit).join(', ')} +${values.length - limit}`;
};

const buildApiKeySecondaryText = (row: MonitoringApiKeyRow) => {
  if (row.isUnknown) {
    if (row.authLabels.length > 0) {
      return joinShort(row.authLabels, 2);
    }
    if (row.sourceLabels.length > 0) {
      return joinShort(row.sourceLabels, 2);
    }
    if (row.channels.length > 0) {
      return joinShort(row.channels, 2);
    }
  }
  if (row.apiKeyLabel && row.apiKeyMasked && row.apiKeyLabel !== row.apiKeyMasked) {
    return row.apiKeyMasked;
  }
  if (row.apiKeyHash) {
    return `sha256:${row.apiKeyHash.slice(0, 12)}`;
  }
  return '';
};

const shortLabel = (t: TFunction, shortKey: string, fallbackKey: string) => {
  const fallback = t(fallbackKey);
  const label = t(shortKey, { defaultValue: fallback });
  return label === shortKey ? fallback : label;
};

const buildApiKeySummaryMetrics = (
  row: MonitoringApiKeyRow,
  hasPrices: boolean,
  locale: string,
  t: TFunction
): AccountSummaryMetric[] => [
  {
    key: 'total-calls',
    label: shortLabel(t, 'monitoring.total_calls_short', 'monitoring.total_calls'),
    fullLabel: t('monitoring.total_calls'),
    value: formatCompactNumber(row.totalCalls),
  },
  {
    key: 'success-calls',
    label: shortLabel(t, 'monitoring.success_calls_short', 'monitoring.success_calls'),
    fullLabel: t('monitoring.success_calls'),
    value: formatCompactNumber(row.successCalls),
    valueClassName: styles.goodText,
  },
  {
    key: 'failure-calls',
    label: shortLabel(t, 'monitoring.failure_calls_short', 'monitoring.failure_calls'),
    fullLabel: t('monitoring.failure_calls'),
    value: formatCompactNumber(row.failureCalls),
    valueClassName: row.failureCalls > 0 ? styles.badText : undefined,
  },
  {
    key: 'total-tokens',
    label: shortLabel(t, 'monitoring.total_tokens_short', 'monitoring.total_tokens'),
    fullLabel: t('monitoring.total_tokens'),
    value: formatCompactNumber(row.totalTokens),
  },
  {
    key: 'input-tokens',
    label: shortLabel(t, 'monitoring.input_tokens_short', 'monitoring.input_tokens'),
    fullLabel: t('monitoring.input_tokens'),
    value: formatCompactNumber(row.inputTokens),
  },
  {
    key: 'output-tokens',
    label: shortLabel(t, 'monitoring.output_tokens_short', 'monitoring.output_tokens'),
    fullLabel: t('monitoring.output_tokens'),
    value: formatCompactNumber(row.outputTokens),
  },
  {
    key: 'cached-tokens',
    label: shortLabel(t, 'monitoring.cached_tokens_short', 'monitoring.cached_tokens'),
    fullLabel: t('monitoring.cached_tokens'),
    value: formatCompactNumber(row.cachedTokens),
  },
  {
    key: 'cache-creation-tokens',
    label: shortLabel(
      t,
      'monitoring.cache_creation_tokens_short',
      'monitoring.cache_creation_tokens'
    ),
    fullLabel: t('monitoring.cache_creation_tokens'),
    value: formatCompactNumber(row.cacheCreationTokens),
  },
  {
    key: 'cache-read-tokens',
    label: shortLabel(t, 'monitoring.cache_read_tokens_short', 'monitoring.cache_read_tokens'),
    fullLabel: t('monitoring.cache_read_tokens'),
    value: formatCompactNumber(row.cacheReadTokens),
  },
  {
    key: 'estimated-cost',
    label: shortLabel(t, 'monitoring.estimated_cost_short', 'monitoring.estimated_cost'),
    fullLabel: t('monitoring.estimated_cost'),
    value: hasPrices ? formatUsd(row.totalCost) : '--',
  },
  {
    key: 'latest-request-time',
    label: shortLabel(t, 'monitoring.latest_request_time_short', 'monitoring.latest_request_time'),
    fullLabel: t('monitoring.latest_request_time'),
    value: new Date(row.lastSeenAt).toLocaleString(locale),
  },
];

function ApiKeySummaryPrimary({
  row,
  expanded,
  onToggle,
  onCopyText,
  t,
}: {
  row: MonitoringApiKeyRow;
  expanded: boolean;
  onToggle: () => void;
  onCopyText: (text: string) => void;
  t: TFunction;
}) {
  const secondaryText = buildApiKeySecondaryText(row);
  const keyLabel = row.isUnknown
    ? t('monitoring.api_key_unknown_label')
    : row.apiKeyLabel || row.apiKeyMasked || t('monitoring.api_key_unknown_label');
  const copyValue = row.apiKeyCopyValue;

  return (
    <div className={styles.apiKeyPrimaryCell}>
      <button
        type="button"
        className={[styles.accountButton, expanded ? styles.expandedAccountButton : '']
          .filter(Boolean)
          .join(' ')}
        onClick={onToggle}
        aria-expanded={expanded}
        title={keyLabel}
      >
        <span className={styles.accountExpandGlyph} aria-hidden="true">
          {expanded ? <IconChevronUp size={15} /> : <IconChevronDown size={15} />}
        </span>
        <span className={styles.accountIdentityLine}>
          <span className={styles.apiKeyIcon} aria-hidden="true">
            <IconKey size={13} />
          </span>
          <span className={styles.accountButtonLabel}>{keyLabel}</span>
        </span>
        {secondaryText ? <small>{secondaryText}</small> : null}
      </button>
      <span className={styles.apiKeyInlineMeta}>
        {copyValue ? (
          <button
            type="button"
            className={styles.apiKeyCopyButton}
            onClick={() => onCopyText(copyValue)}
            title={t('common.copy')}
            aria-label={t('common.copy')}
          >
            <IconCopy size={13} />
          </button>
        ) : null}
        {!row.isUnknown ? (
          <span className={styles.apiKeyAvailableChip}>{t('monitoring.api_key_available')}</span>
        ) : null}
      </span>
    </div>
  );
}

function ApiKeyExpandedDetails({
  row,
  hasPrices,
  locale,
  t,
}: {
  row: MonitoringApiKeyRow;
  hasPrices: boolean;
  locale: string;
  t: TFunction;
}) {
  const summaryMetrics = buildApiKeySummaryMetrics(row, hasPrices, locale, t);

  return (
    <div className={styles.apiKeyExpandedDetails}>
      <div className={styles.accountStructureModelPanel}>
        <AccountTokenMetricGrid metrics={summaryMetrics} t={t} variant="table" />
        <AccountModelUsageTable row={row} hasPrices={hasPrices} locale={locale} t={t} />
      </div>
    </div>
  );
}

export function ApiKeySummaryPanelActions({ rowCount, t }: ApiKeySummaryPanelActionsProps) {
  return (
    <div className={`${styles.inlineMetrics} ${styles.apiKeySummaryActions}`}>
      <span className={styles.apiKeyCountPill}>
        {t('monitoring.api_key_summary_keys_count', { count: rowCount })}
      </span>
    </div>
  );
}

export function ApiKeySummaryPanel({
  embedded = false,
  rows,
  columns,
  pagination,
  expandedApiKeys,
  hasPrices,
  locale,
  pageSize,
  pageSizeOptions,
  emptyState,
  t,
  onToggleApiKey,
  onPageChange,
  onPageSizeChange,
}: ApiKeySummaryPanelProps) {
  const showNotification = useNotificationStore((state) => state.showNotification);
  const handleCopyText = async (text: string) => {
    const copied = await copyToClipboard(text);
    showNotification(
      t(copied ? 'notification.link_copied' : 'notification.copy_failed'),
      copied ? 'success' : 'error'
    );
  };
  const actions = <ApiKeySummaryPanelActions rowCount={rows.length} t={t} />;
  const content = (
    <>
      <div className={`${styles.tableWrapper} ${styles.apiKeySummaryTableWrapper}`}>
        <table className={`${styles.table} ${styles.apiKeySummaryTable}`}>
          <colgroup>
            {columns.map((column) => (
              <col key={column.key} />
            ))}
          </colgroup>
          <thead>
            <tr>
              {columns.map((column) => (
                <th key={column.key} title={column.fullLabel ?? column.label}>
                  {column.label}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {pagination.pageItems.map((row) => {
              const isExpanded = Boolean(expandedApiKeys[row.id]);
              const keyMetrics = buildApiKeySummaryMetrics(row, hasPrices, locale, t);
              const keyMetricByKey = new Map(keyMetrics.map((metric) => [metric.key, metric]));
              const rowClassName = [
                styles.apiKeySummaryRow,
                isExpanded ? styles.apiKeySummaryRowExpanded : '',
              ]
                .filter(Boolean)
                .join(' ');

              return (
                <Fragment key={row.id}>
                  <tr className={rowClassName}>
                    <td>
                      <ApiKeySummaryPrimary
                        row={row}
                        expanded={isExpanded}
                        onToggle={() => onToggleApiKey(row.id)}
                        onCopyText={(text) => void handleCopyText(text)}
                        t={t}
                      />
                    </td>
                    <td>{keyMetricByKey.get('total-calls')?.value ?? '--'}</td>
                    <td className={keyMetricByKey.get('success-calls')?.valueClassName}>
                      {keyMetricByKey.get('success-calls')?.value ?? '--'}
                    </td>
                    <td className={keyMetricByKey.get('failure-calls')?.valueClassName}>
                      {keyMetricByKey.get('failure-calls')?.value ?? '--'}
                    </td>
                    <td>{keyMetricByKey.get('total-tokens')?.value ?? '--'}</td>
                    <td>{keyMetricByKey.get('estimated-cost')?.value ?? '--'}</td>
                    <td>{keyMetricByKey.get('latest-request-time')?.value ?? '--'}</td>
                  </tr>
                  {isExpanded ? (
                    <tr className={styles.apiKeyDetailRow}>
                      <td colSpan={columns.length}>
                        <ApiKeyExpandedDetails
                          row={row}
                          hasPrices={hasPrices}
                          locale={locale}
                          t={t}
                        />
                      </td>
                    </tr>
                  ) : null}
                </Fragment>
              );
            })}
            {rows.length === 0 ? (
              <tr>
                <td colSpan={columns.length}>{emptyState}</td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>
      <PaginationControls
        count={rows.length}
        currentPage={pagination.currentPage}
        totalPages={pagination.totalPages}
        startItem={pagination.startItem}
        endItem={pagination.endItem}
        pageSize={pageSize}
        pageSizeOptions={pageSizeOptions}
        onPageChange={onPageChange}
        onPageSizeChange={onPageSizeChange}
        t={t}
      />
    </>
  );

  if (embedded) {
    return content;
  }

  return (
    <MonitoringPanel
      title={
        <span className={styles.panelTitleWithHint}>
          {t('monitoring.api_key_summary_title')}
          <span title={t('monitoring.api_key_summary_description')}>
            <IconInfo
              size={14}
              className={styles.panelTitleHintIcon}
              aria-label={t('monitoring.api_key_summary_description')}
            />
          </span>
        </span>
      }
      subtitle={t('monitoring.api_key_summary_desc')}
      className={styles.apiKeyPanel}
      extra={actions}
    >
      {content}
    </MonitoringPanel>
  );
}
