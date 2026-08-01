import { useState, type ReactNode } from 'react';
import type { TFunction } from 'i18next';
import { Card } from '@/components/ui/Card';
import {
  IconChartLine,
  IconChevronDown,
  IconChevronRight,
  IconChevronUp,
  IconCrosshair,
  IconInbox,
  IconInfo,
  IconRefreshCw,
  IconTimer,
  IconTrendingUp,
} from '@/components/ui/icons';
import { sortAccountOverviewCardMetrics } from '@/features/monitoring/accountOverviewCardMetrics';
import {
  resolveAccountDisplayText,
  type AccountDisplayMode,
  type MonitoringAccountAuthState,
} from '@/features/monitoring/accountOverviewState';
import type {
  MonitoringAccountModelSpendRow,
  MonitoringAccountRow,
} from '@/features/monitoring/hooks/useMonitoringData';
import { formatCompactNumber, formatUsd } from '@/utils/usage';
import type { StatusBarData } from '@/utils/recentRequests';
import { MonitoringHealthStatusBar } from './MonitoringHealthStatusBar';
import {
  buildAccountSecondaryText,
  buildAccountSummaryMetrics,
  buildCacheTokenPresentation,
  formatPercent,
  getAccountStatusDotClassName,
  getAccountStatusLabel,
  getAccountStatusTone,
  getSuccessRateClassName,
  type AccountQuotaEntry,
  type AccountQuotaState,
  type AccountQuotaWindow,
  type AccountSummaryMetric,
} from './accountOverviewPresentation';
import styles from '../MonitoringCenterPage.module.scss';

export function AccountStatusBadge({
  authState,
  t,
}: {
  authState: MonitoringAccountAuthState;
  t: TFunction;
}) {
  const tone = getAccountStatusTone(authState);
  const label = getAccountStatusLabel(authState, t);

  return (
    <span
      className={[styles.accountStatusBadge, styles[`accountStatusBadge${tone}`]]
        .filter(Boolean)
        .join(' ')}
      title={label}
    >
      <span
        className={[styles.accountStatusDot, getAccountStatusDotClassName(tone)]
          .filter(Boolean)
          .join(' ')}
        aria-hidden="true"
      />
      {label}
    </span>
  );
}

const shortLabel = (t: TFunction, shortKey: string, fallbackKey: string) => {
  const fallback = t(fallbackKey);
  const label = t(shortKey, { defaultValue: fallback });
  return label === shortKey ? fallback : label;
};

export function AccountSummaryPrimary({
  row,
  expanded,
  onToggle,
  accountDisplayMode,
  statusTone = 'enabled',
  showSecondary = true,
}: {
  row: MonitoringAccountRow;
  expanded: boolean;
  onToggle: () => void;
  accountDisplayMode: AccountDisplayMode;
  statusTone?: string;
  showSecondary?: boolean;
}) {
  const accountDisplay = resolveAccountDisplayText(row, accountDisplayMode);
  const secondaryText = buildAccountSecondaryText(row);
  const accountSecondaryText = accountDisplay.secondary || secondaryText;

  return (
    <button
      type="button"
      className={[
        styles.accountButton,
        expanded ? styles.expandedAccountButton : '',
        statusTone === 'disabled' || statusTone === 'unavailable' ? styles.accountButtonMuted : '',
      ]
        .filter(Boolean)
        .join(' ')}
      onClick={onToggle}
      aria-expanded={expanded}
      title={accountDisplay.title}
    >
      <span className={styles.accountExpandGlyph} aria-hidden="true">
        {expanded ? <IconChevronUp size={15} /> : <IconChevronDown size={15} />}
      </span>
      <span className={styles.accountIdentityLine}>
        <span
          className={[styles.accountStatusDot, getAccountStatusDotClassName(statusTone)]
            .filter(Boolean)
            .join(' ')}
          aria-hidden="true"
        />
        <span className={styles.accountButtonLabel}>{accountDisplay.primary}</span>
      </span>
      {showSecondary && accountSecondaryText ? <small>{accountSecondaryText}</small> : null}
    </button>
  );
}

function AccountQuotaPanel({
  quotaState,
  locale,
  t,
  onRefreshQuota,
}: {
  quotaState?: AccountQuotaState;
  locale: string;
  t: TFunction;
  onRefreshQuota: () => void;
}) {
  const quotaEntries = quotaState?.entries ?? [];
  const quotaLoading = quotaState?.status === 'loading';
  const lastQuotaSyncMs =
    quotaState?.lastRefreshedAt && Number.isFinite(quotaState.lastRefreshedAt)
      ? quotaState.lastRefreshedAt
      : undefined;
  const singleQuotaEntry = quotaEntries.length === 1 ? quotaEntries[0] : null;

  const buildQuotaInfoRows = (entry: AccountQuotaEntry) => {
    const fromUsageHeaders = entry.observedFromUsageHeaders === true;
    const timestampMs = fromUsageHeaders
      ? entry.observedAtMs
      : (entry.fetchedAtMs ?? lastQuotaSyncMs);
    const formattedTime =
      timestampMs && Number.isFinite(timestampMs)
        ? new Date(timestampMs).toLocaleString(locale)
        : '--';

    return [
      {
        key: 'source',
        label: t('codex_quota.tooltip_source_label'),
        value: fromUsageHeaders
          ? t('codex_quota.tooltip_source_header')
          : t('codex_quota.tooltip_source_api'),
      },
      {
        key: 'fetched-at',
        label: t('codex_quota.tooltip_fetched_at_label'),
        value: formattedTime,
      },
    ];
  };

  const renderQuotaInfo = (entry: AccountQuotaEntry, windowLabel: string) => {
    const rows = buildQuotaInfoRows(entry);

    return (
      <span
        className={styles.quotaInfoTrigger}
        tabIndex={0}
        aria-label={t('codex_quota.tooltip_label', { label: windowLabel })}
      >
        <IconInfo
          size={14}
          className={styles.quotaInfoIcon}
          aria-hidden="true"
          focusable={false}
        />
        <span className={styles.quotaInfoTooltip} role="tooltip">
          {rows.map((row) => (
            <span key={row.key} className={styles.quotaInfoTooltipRow}>
              <span className={styles.quotaInfoTooltipLabel}>{row.label}</span>
              <span className={styles.quotaInfoTooltipValue}>{row.value}</span>
            </span>
          ))}
        </span>
      </span>
    );
  };

  const renderQuotaWindows = (windows: AccountQuotaWindow[], entry: AccountQuotaEntry) => (
    <div className={styles.quotaWindowList}>
      {windows.map((window) => {
        const percentLabel =
          window.remainingPercent === null ? '--' : `${Math.round(window.remainingPercent)}%`;
        const barStyle =
          window.remainingPercent === null
            ? undefined
            : { width: `${Math.max(0, Math.min(100, window.remainingPercent))}%` };

        return (
          <div key={window.id} className={styles.quotaWindowRow}>
            <div className={styles.quotaWindowHeader}>
              <span className={styles.quotaWindowLabel}>
                <span>{window.label}</span>
                {renderQuotaInfo(entry, window.label)}
              </span>
              <strong>{percentLabel}</strong>
            </div>
            <div className={styles.quotaProgressTrack}>
              <span className={styles.quotaProgressBar} style={barStyle} />
            </div>
            <div className={styles.quotaWindowMeta}>
              <small>{`${t('monitoring.account_quota_reset_at')}: ${window.resetLabel}`}</small>
              {window.usageLabel ? <small>{window.usageLabel}</small> : null}
            </div>
          </div>
        );
      })}
    </div>
  );

  const renderRefreshButton = () => (
    <button
      type="button"
      className={styles.quotaRefreshButton}
      onClick={onRefreshQuota}
      disabled={quotaLoading}
    >
      <IconRefreshCw
        size={14}
        className={quotaLoading ? styles.refreshIconSpinning : styles.refreshIcon}
      />
      <span>{t('monitoring.account_quota_refresh_button')}</span>
    </button>
  );

  const renderStateMessage = (message: ReactNode, hint?: ReactNode, retry = false) => (
    <div className={styles.quotaStateMessage}>
      <span>{message}</span>
      {hint ? <small>{hint}</small> : null}
      {retry ? (
        <button
          type="button"
          className={styles.quotaRetryButton}
          onClick={onRefreshQuota}
          disabled={quotaLoading}
        >
          <IconRefreshCw
            size={14}
            className={quotaLoading ? styles.refreshIconSpinning : styles.refreshIcon}
          />
          <span>{t('monitoring.account_quota_retry_button')}</span>
        </button>
      ) : null}
    </div>
  );

  return (
    <section className={styles.quotaSection}>
      <div className={styles.quotaSectionHeader}>
        <div className={styles.quotaSectionTitleGroup}>
          <strong>{t('monitoring.account_quota_title')}</strong>
        </div>
        {renderRefreshButton()}
      </div>

      {quotaLoading && quotaEntries.length === 0
        ? renderStateMessage(t('monitoring.account_quota_loading'))
        : null}

      {!quotaLoading && quotaState?.status === 'error' && quotaEntries.length === 0
        ? renderStateMessage(
            t('monitoring.account_quota_load_failed', {
              message: quotaState.error || t('common.unknown_error'),
            }),
            undefined,
            true
          )
        : null}

      {!quotaLoading && quotaState?.status === 'success' && quotaEntries.length === 0
        ? renderStateMessage(
            t('monitoring.account_quota_empty'),
            t('monitoring.account_quota_idle')
          )
        : null}

      {!quotaState && quotaEntries.length === 0
        ? renderStateMessage(
            t('monitoring.account_quota_empty'),
            t('monitoring.account_quota_idle')
          )
        : null}

      {singleQuotaEntry ? (
        singleQuotaEntry.error ? (
          renderStateMessage(
            t('monitoring.account_quota_load_failed', { message: singleQuotaEntry.error }),
            undefined,
            true
          )
        ) : singleQuotaEntry.windows.length > 0 ? (
          renderQuotaWindows(singleQuotaEntry.windows, singleQuotaEntry)
        ) : (
          renderStateMessage(
            singleQuotaEntry.emptyMessage ?? t('monitoring.account_quota_empty'),
            t('monitoring.account_quota_idle')
          )
        )
      ) : quotaEntries.length > 0 ? (
        <div className={styles.quotaEntryGrid}>
          {quotaEntries.map((entry) => {
            const entryMetaText = `${entry.providerLabel} · ${entry.fileName}`;
            return (
              <div key={entry.key} className={styles.quotaEntryCard}>
                <div className={styles.quotaEntryHeader}>
                  <div className={styles.quotaEntryMain}>
                    <strong>{entry.authLabel}</strong>
                    <small>{entryMetaText}</small>
                  </div>
                </div>

                {entry.error
                  ? renderStateMessage(
                      t('monitoring.account_quota_load_failed', { message: entry.error }),
                      undefined,
                      true
                    )
                  : entry.windows.length > 0
                    ? renderQuotaWindows(entry.windows, entry)
                    : renderStateMessage(
                        entry.emptyMessage ?? t('monitoring.account_quota_empty'),
                        t('monitoring.account_quota_idle')
                      )}
              </div>
            );
          })}
        </div>
      ) : null}
    </section>
  );
}

export function AccountTokenMetricGrid({
  metrics,
  t,
  variant = 'card',
}: {
  metrics: AccountSummaryMetric[];
  t: TFunction;
  variant?: 'card' | 'table';
}) {
  const getTokenMetricIcon = (key: string) => {
    if (key === 'input-tokens') return <IconInbox size={13} />;
    if (key === 'output-tokens') return <IconTrendingUp size={13} />;
    if (key === 'cached-tokens' || key === 'cache-creation-tokens' || key === 'cache-read-tokens') {
      return <IconTimer size={13} />;
    }
    return <IconChartLine size={13} />;
  };
  const getTokenMetricToneClassName = (key: string) => {
    if (key === 'input-tokens') return styles.accountMetricIconInput;
    if (key === 'output-tokens') return styles.accountMetricIconOutput;
    if (key === 'cached-tokens' || key === 'cache-creation-tokens' || key === 'cache-read-tokens') {
      return styles.accountMetricIconCached;
    }
    return styles.accountMetricIconTotal;
  };

  if (variant === 'table') {
    const tokenStructureMetrics = metrics.filter((metric) =>
      ['input-tokens', 'output-tokens', 'cached-tokens'].includes(metric.key)
    );
    const getTokenStructureRowToneClassName = (key: string) => {
      if (key === 'input-tokens') return styles.tokenStructureRowInput;
      if (key === 'output-tokens') return styles.tokenStructureRowOutput;
      if (
        key === 'cached-tokens' ||
        key === 'cache-creation-tokens' ||
        key === 'cache-read-tokens'
      ) {
        return styles.tokenStructureRowCached;
      }
      return '';
    };

    return (
      <section className={styles.accountTokenStructurePanel}>
        <div className={styles.accountSectionHeader}>
          <strong>{t('monitoring.account_overview_token_structure')}</strong>
        </div>
        <div className={styles.tokenStructureRowList}>
          {tokenStructureMetrics.map((metric) => (
            <div
              key={metric.key}
              className={[styles.tokenStructureRow, getTokenStructureRowToneClassName(metric.key)]
                .filter(Boolean)
                .join(' ')}
            >
              <span className={styles.tokenStructureRowLeft}>
                <span className={styles.tokenStructureRowIcon} aria-hidden="true">
                  {getTokenMetricIcon(metric.key)}
                </span>
                <span
                  className={styles.tokenStructureRowLabel}
                  title={metric.fullLabel ?? metric.label}
                >
                  {metric.label}
                </span>
              </span>
              <strong
                className={[styles.tokenStructureRowValue, metric.valueClassName]
                  .filter(Boolean)
                  .join(' ')}
              >
                {metric.value}
              </strong>
            </div>
          ))}
        </div>
      </section>
    );
  }

  return (
    <section className={styles.accountTokenPanel}>
      <div className={styles.accountSectionHeader}>
        <strong>{t('monitoring.account_overview_tokens_title')}</strong>
      </div>
      <div className={styles.accountOverviewMetricGrid}>
        {metrics.map((metric) => (
          <div key={metric.key} className={styles.accountOverviewMetricCard}>
            <span
              className={styles.accountOverviewMetricLabel}
              title={metric.fullLabel ?? metric.label}
            >
              <span
                className={[styles.accountMetricIcon, getTokenMetricToneClassName(metric.key)]
                  .filter(Boolean)
                  .join(' ')}
                aria-hidden="true"
              >
                {getTokenMetricIcon(metric.key)}
              </span>
              {metric.label}
            </span>
            <strong
              className={[styles.accountOverviewMetricValue, metric.valueClassName]
                .filter(Boolean)
                .join(' ')}
            >
              {metric.value}
            </strong>
          </div>
        ))}
      </div>
    </section>
  );
}

function AccountHealthStatusPanel({
  row,
  hasPrices,
  locale,
  t,
  statusData,
  scopeText,
}: {
  row: MonitoringAccountRow;
  hasPrices: boolean;
  locale: string;
  t: TFunction;
  statusData: StatusBarData;
  scopeText: string;
}) {
  const healthMetrics = [
    {
      key: 'total-calls',
      label: shortLabel(t, 'monitoring.total_calls_short', 'monitoring.total_calls'),
      fullLabel: t('monitoring.total_calls'),
      value: formatCompactNumber(row.totalCalls),
    },
    {
      key: 'success-calls',
      label: t('stats.success'),
      fullLabel: t('monitoring.success_calls'),
      value: formatCompactNumber(row.successCalls),
      className: styles.goodText,
    },
    {
      key: 'failure-calls',
      label: t('stats.failure'),
      fullLabel: t('monitoring.failure_calls'),
      value: formatCompactNumber(row.failureCalls),
      className: row.failureCalls > 0 ? styles.badText : undefined,
    },
    {
      key: 'estimated-cost',
      label: shortLabel(t, 'monitoring.estimated_cost_short', 'monitoring.estimated_cost'),
      fullLabel: t('monitoring.estimated_cost'),
      value: hasPrices ? formatUsd(row.totalCost) : '--',
      className: styles.primaryText,
    },
    {
      key: 'success-rate',
      label: shortLabel(
        t,
        'monitoring.column_success_rate_short',
        'monitoring.column_success_rate'
      ),
      fullLabel: t('monitoring.column_success_rate'),
      value: formatPercent(row.successRate),
      className: getSuccessRateClassName(row.successRate),
    },
  ];

  return (
    <section className={styles.accountOverviewStatusSection}>
      <div className={styles.accountSectionHeader}>
        <strong>{t('monitoring.account_overview_health_label')}</strong>
        <span
          className={styles.accountSectionInfo}
          title={t('monitoring.account_overview_health_hint')}
        >
          <IconInfo size={14} />
        </span>
      </div>
      <div className={styles.healthMetricGrid}>
        {healthMetrics.map((metric) => (
          <div key={metric.key} className={styles.healthMetricItem}>
            <span title={metric.fullLabel}>{metric.label}</span>
            <strong className={metric.className}>{metric.value}</strong>
          </div>
        ))}
      </div>
      <MonitoringHealthStatusBar statusData={statusData} locale={locale} t={t} showRate={false} />
      <div className={styles.accountScopeText}>{scopeText}</div>
    </section>
  );
}

function AccountModelUsageList({
  row,
  hasPrices,
  locale,
  t,
  limit = 2,
}: {
  row: { id: string; models: MonitoringAccountModelSpendRow[] };
  hasPrices: boolean;
  locale: string;
  t: TFunction;
  limit?: number;
}) {
  const [showAll, setShowAll] = useState(false);
  const [expandedModels, setExpandedModels] = useState<Record<string, boolean>>({});
  const hasExtraModels = row.models.length > limit;
  const visibleModels = showAll ? row.models : row.models.slice(0, limit);
  const toggleModel = (key: string) =>
    setExpandedModels((previous) => ({ ...previous, [key]: !previous[key] }));

  return (
    <section className={styles.accountModelListPanel}>
      <div className={styles.accountSectionHeader}>
        <strong>
          {t('monitoring.account_overview_models_top', {
            count: Math.min(limit, row.models.length || limit),
          })}
        </strong>
        {hasExtraModels ? (
          <button
            type="button"
            className={styles.accountModelViewAllButton}
            onClick={() => setShowAll((previous) => !previous)}
          >
            {showAll
              ? t('monitoring.account_overview_collapse_models')
              : t('monitoring.account_overview_view_all')}
          </button>
        ) : null}
      </div>

      {visibleModels.length > 0 ? (
        <div className={styles.accountModelList}>
          {visibleModels.map((model) => {
            const modelKey = `${row.id}-${model.model}`;
            const isModelExpanded = Boolean(expandedModels[modelKey]);
            const cacheMetric = buildCacheTokenPresentation(model, t);
            return (
              <div key={modelKey} className={styles.accountModelItem}>
                <button
                  type="button"
                  className={styles.accountModelRow}
                  onClick={() => toggleModel(modelKey)}
                  aria-expanded={isModelExpanded}
                >
                  <span className={styles.accountModelName} title={model.model}>
                    {model.model}
                  </span>
                  <span className={styles.accountModelMetaLine}>
                    <span className={styles.accountModelStat}>
                      <small>{t('monitoring.account_overview_model_calls_short')}</small>
                      <strong>{formatCompactNumber(model.totalCalls)}</strong>
                    </span>
                    <span className={styles.accountModelStat}>
                      <small>{t('monitoring.account_overview_model_success_rate_short')}</small>
                      <strong className={getSuccessRateClassName(model.successRate)}>
                        {formatPercent(model.successRate)}
                      </strong>
                    </span>
                    <span className={styles.accountModelStat}>
                      <small>{t('monitoring.account_overview_model_total_tokens_short')}</small>
                      <strong>{formatCompactNumber(model.totalTokens)}</strong>
                    </span>
                    <span className={styles.accountModelStat}>
                      <small>{t('monitoring.account_overview_model_total_cost_short')}</small>
                      <strong>{hasPrices ? formatUsd(model.totalCost) : '--'}</strong>
                    </span>
                    <span className={styles.accountModelChevron} aria-hidden="true">
                      {isModelExpanded ? (
                        <IconChevronDown size={14} />
                      ) : (
                        <IconChevronRight size={14} />
                      )}
                    </span>
                  </span>
                </button>
                {isModelExpanded ? (
                  <div className={styles.accountModelExpanded}>
                    <div className={styles.accountModelExpandedItem}>
                      <small>
                        {shortLabel(t, 'monitoring.input_tokens_short', 'monitoring.input_tokens')}
                      </small>
                      <strong>{formatCompactNumber(model.inputTokens)}</strong>
                    </div>
                    <div className={styles.accountModelExpandedItem}>
                      <small>
                        {shortLabel(
                          t,
                          'monitoring.output_tokens_short',
                          'monitoring.output_tokens'
                        )}
                      </small>
                      <strong>{formatCompactNumber(model.outputTokens)}</strong>
                    </div>
                    <div className={styles.accountModelExpandedItem}>
                      <small title={cacheMetric.fullLabel}>{cacheMetric.label}</small>
                      <strong>{cacheMetric.value}</strong>
                    </div>
                    <div className={styles.accountModelExpandedItem}>
                      <small>
                        {shortLabel(
                          t,
                          'monitoring.latest_request_time_short',
                          'monitoring.latest_request_time'
                        )}
                      </small>
                      <strong>{new Date(model.lastSeenAt).toLocaleString(locale)}</strong>
                    </div>
                  </div>
                ) : null}
              </div>
            );
          })}
        </div>
      ) : (
        <div className={styles.emptyBlockSmall}>{t('monitoring.account_overview_no_models')}</div>
      )}
    </section>
  );
}

export function AccountModelUsageTable({
  row,
  hasPrices,
  locale,
  t,
  limit = 2,
}: {
  row: { id: string; models: MonitoringAccountModelSpendRow[] };
  hasPrices: boolean;
  locale: string;
  t: TFunction;
  limit?: number;
}) {
  const [showAll, setShowAll] = useState(false);
  const hasExtraModels = row.models.length > limit;
  const visibleModels = showAll ? row.models : row.models.slice(0, limit);
  const modelCountForTitle = Math.min(limit, row.models.length || limit);
  const latestRequestLabel = shortLabel(
    t,
    'monitoring.latest_request_time_short',
    'monitoring.latest_request_time'
  );

  return (
    <section className={styles.accountModelTablePanel}>
      <div className={styles.accountSectionHeader}>
        <strong>
          {t('monitoring.account_overview_models_top', {
            count: modelCountForTitle,
          })}
        </strong>
        <button
          type="button"
          className={styles.accountModelViewAllButton}
          onClick={() => setShowAll((previous) => !previous)}
          disabled={!hasExtraModels}
        >
          {showAll
            ? t('monitoring.account_overview_collapse_models')
            : t('monitoring.account_overview_view_all')}
        </button>
      </div>
      {visibleModels.length > 0 ? (
        <table className={styles.accountModelTable}>
          <thead>
            <tr>
              <th>{t('usage_stats.model_price_model')}</th>
              <th>{t('monitoring.account_overview_model_calls_short')}</th>
              <th>{t('monitoring.account_overview_model_success_rate_short')}</th>
              <th>{t('monitoring.account_overview_model_input_tokens_short')}</th>
              <th>{t('monitoring.account_overview_model_output_tokens_short')}</th>
              <th>{t('monitoring.account_overview_model_cached_tokens_short')}</th>
              <th>{t('monitoring.account_overview_model_total_tokens_short')}</th>
              <th>{t('monitoring.account_overview_model_total_cost_short')}</th>
              <th>{latestRequestLabel}</th>
            </tr>
          </thead>
          <tbody>
            {visibleModels.map((model) => {
              const cacheMetric = buildCacheTokenPresentation(model, t);
              return (
                <tr key={`${row.id}-${model.model}`}>
                  <td>
                    <span className={styles.accountModelName} title={model.model}>
                      {model.model}
                    </span>
                  </td>
                  <td>{formatCompactNumber(model.totalCalls)}</td>
                  <td className={getSuccessRateClassName(model.successRate)}>
                    {formatPercent(model.successRate)}
                  </td>
                  <td>{formatCompactNumber(model.inputTokens)}</td>
                  <td>{formatCompactNumber(model.outputTokens)}</td>
                  <td>
                    <span title={cacheMetric.fullLabel}>
                      {cacheMetric.label ===
                      shortLabel(t, 'monitoring.cached_tokens_short', 'monitoring.cached_tokens')
                        ? cacheMetric.value
                        : `${cacheMetric.label} ${cacheMetric.value}`}
                    </span>
                  </td>
                  <td>{formatCompactNumber(model.totalTokens)}</td>
                  <td>{hasPrices ? formatUsd(model.totalCost) : '--'}</td>
                  <td>{new Date(model.lastSeenAt).toLocaleString(locale)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      ) : (
        <div className={styles.emptyBlockSmall}>{t('monitoring.account_overview_no_models')}</div>
      )}
    </section>
  );
}

export function AccountExpandedDetails({
  row,
  hasPrices,
  locale,
  t,
  summaryMetrics,
  quotaState,
  onRefreshQuota,
  variant,
}: {
  row: MonitoringAccountRow;
  hasPrices: boolean;
  locale: string;
  t: TFunction;
  summaryMetrics: AccountSummaryMetric[];
  quotaState?: AccountQuotaState;
  onRefreshQuota: () => void;
  variant: 'card' | 'table';
}) {
  const tokenMetrics = sortAccountOverviewCardMetrics(summaryMetrics);

  if (variant === 'table') {
    return (
      <div className={styles.expandedAccountDetails}>
        <AccountQuotaPanel
          quotaState={quotaState}
          locale={locale}
          t={t}
          onRefreshQuota={onRefreshQuota}
        />
        <div className={styles.accountStructureModelPanel}>
          <AccountTokenMetricGrid metrics={tokenMetrics} t={t} variant="table" />
          <AccountModelUsageTable row={row} hasPrices={hasPrices} locale={locale} t={t} />
        </div>
      </div>
    );
  }

  return (
    <div className={styles.accountOverviewCardBody}>
      <AccountQuotaPanel
        quotaState={quotaState}
        locale={locale}
        t={t}
        onRefreshQuota={onRefreshQuota}
      />
      <AccountModelUsageList row={row} hasPrices={hasPrices} locale={locale} t={t} />
    </div>
  );
}

export function AccountOverviewCard({
  row,
  authState,
  hasPrices,
  locale,
  t,
  accountDisplayMode,
  isExpanded,
  isFocused,
  statusData,
  scopeText,
  quotaState,
  onToggle,
  onFocus,
  onRefreshQuota,
}: {
  row: MonitoringAccountRow;
  authState: MonitoringAccountAuthState;
  hasPrices: boolean;
  locale: string;
  t: TFunction;
  accountDisplayMode: AccountDisplayMode;
  isExpanded: boolean;
  isFocused: boolean;
  statusData: StatusBarData;
  scopeText: string;
  quotaState?: AccountQuotaState;
  onToggle: () => void;
  onFocus: () => void;
  onRefreshQuota: () => void;
}) {
  const summaryMetrics = buildAccountSummaryMetrics(row, hasPrices, locale, t);
  const cardMetrics = sortAccountOverviewCardMetrics(summaryMetrics);
  const statusTone = getAccountStatusTone(authState);
  const accountDisplay = resolveAccountDisplayText(row, accountDisplayMode);
  const secondaryText = accountDisplay.secondary || buildAccountSecondaryText(row);
  const latestRequestText = new Date(row.lastSeenAt).toLocaleString(locale);
  const latestRequestLabel = shortLabel(
    t,
    'monitoring.latest_request_time_short',
    'monitoring.latest_request_time'
  );

  return (
    <Card
      className={[
        styles.accountOverviewCard,
        isExpanded ? styles.accountOverviewCardExpanded : '',
        isFocused ? styles.accountOverviewCardFocused : '',
        authState.enabledState === 'disabled' ? styles.accountOverviewCardDisabled : '',
      ]
        .filter(Boolean)
        .join(' ')}
    >
      <div className={styles.accountOverviewCardHeader}>
        <div className={styles.accountTitleRow}>
          <AccountSummaryPrimary
            row={row}
            expanded={isExpanded}
            onToggle={onToggle}
            accountDisplayMode={accountDisplayMode}
            statusTone={statusTone}
            showSecondary={false}
          />
        </div>
        <div className={styles.accountMetaRow}>
          {secondaryText ? (
            <span className={styles.accountOverviewCardTimestamp} title={secondaryText}>
              {secondaryText}
            </span>
          ) : null}
          {secondaryText ? <span className={styles.accountMetaSeparator}>·</span> : null}
          <span
            className={styles.accountOverviewCardTimestamp}
            title={t('monitoring.latest_request_time')}
          >
            {`${latestRequestLabel}: ${latestRequestText}`}
          </span>
          <button
            type="button"
            className={`${styles.inlineActionButton} ${styles.accountFocusButton}`}
            onClick={onFocus}
          >
            <IconCrosshair size={12} aria-hidden="true" />
            <span>
              {isFocused ? t('monitoring.restore_account_scope') : t('monitoring.focus_account')}
            </span>
          </button>
        </div>
      </div>

      <AccountHealthStatusPanel
        row={row}
        hasPrices={hasPrices}
        locale={locale}
        t={t}
        statusData={statusData}
        scopeText={scopeText}
      />

      <AccountTokenMetricGrid metrics={cardMetrics} t={t} />

      {isExpanded ? (
        <AccountExpandedDetails
          row={row}
          hasPrices={hasPrices}
          locale={locale}
          t={t}
          summaryMetrics={summaryMetrics}
          quotaState={quotaState}
          onRefreshQuota={onRefreshQuota}
          variant="card"
        />
      ) : null}
    </Card>
  );
}
