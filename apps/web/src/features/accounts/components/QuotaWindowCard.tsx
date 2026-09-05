/**
 * QuotaWindowCard
 * 统一的“配额窗口卡片”——在凭证详情额度 Tab 中承载标准、模型和其他额度项。
 *
 * compact 变体仍供列表型调用方使用；drawer 变体按详情抽屉的三列额度方案渲染。
 */
import { useTranslation } from 'react-i18next';
import type { JSX } from 'react';
import {
  IconCalendar,
  IconCircleHelp,
  IconClock,
  IconDatabaseZap,
  IconDollarSign,
  IconModelCluster,
  IconShield,
  IconShieldCheck,
  IconSidebarQuota,
  IconTimer,
  IconTrendingUp,
  IconTriangleAlert,
  IconBinary,
  IconChartLine,
  IconCheck,
} from '@/components/ui/icons';
import {
  isIntervalAccountQuotaWindow,
  isModelScopedAccountQuotaWindow,
  type AccountQuotaWindowKind,
} from '@/features/accounts/model/accountQuotaDisplayWindows';
import type { AccountQuotaBoundaryAccuracy } from '@/features/accounts/model/accountQuotaWindowDefinitions';
import type {
  AccountDetailQuotaWindow,
  AccountDetailWindowUsageSummary,
} from '@/features/accounts/model/accountDetailViewModel';
import { formatQuotaResetDisplay } from '@/features/accounts/model/accountsPagePresentation';
import { formatCompactNumber, formatUsd } from '@/utils/usage';
import { QuotaProgressBar } from './QuotaProgressBar';
import styles from './QuotaWindowCard.module.scss';

export type QuotaWindowCardMode = 'standard' | 'model' | 'other';

interface QuotaWindowCardProps {
  window: AccountDetailQuotaWindow;
  /** 详情抽屉中的额度分组；未传入时根据窗口类型兼容推断。 */
  mode?: QuotaWindowCardMode;
  /** 渲染风格：抽屉用 drawer（更紧凑），列表用 compact（单行）。 */
  variant?: 'drawer' | 'compact';
  locale?: string;
}

type MetricTone = 'blue' | 'green' | 'teal' | 'amber';

const QUOTA_PROGRESS_HIGH_THRESHOLD = 70;
const QUOTA_PROGRESS_MEDIUM_THRESHOLD = 30;

const formatPercent = (value: number | null | undefined, digits = 0): string => {
  if (typeof value !== 'number' || !Number.isFinite(value)) return '-';
  return `${value.toFixed(digits)}%`;
};

const formatOptionalCompactNumber = (value: number | null | undefined): string =>
  typeof value !== 'number' || !Number.isFinite(value) ? '-' : formatCompactNumber(value);

const formatMoney = (value: number | null | undefined): string => {
  if (typeof value !== 'number' || !Number.isFinite(value)) return '-';
  return formatUsd(value);
};

const formatRange = (
  fromMs: number | null | undefined,
  toMs: number | null | undefined,
  locale: string
): string => {
  if (!fromMs || !toMs || fromMs >= toMs) return '-';
  const formatter = new Intl.DateTimeFormat(locale, {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  });
  return `${formatter.format(fromMs)} — ${formatter.format(toMs)}`;
};

const isReliableBoundary = (accuracy: AccountQuotaBoundaryAccuracy | null | undefined) =>
  accuracy === 'exact' || accuracy === 'derived';

const formatCurrentWindowRange = (
  window: AccountDetailQuotaWindow,
  usage: AccountDetailWindowUsageSummary | null | undefined,
  locale: string,
  unconfirmedLabel: string
): string => {
  const hasLifecycleEvidence =
    window.availability !== undefined ||
    window.currentCycle !== undefined ||
    window.previousCycle !== undefined;
  if (
    (window.windowMode === 'fixed' || window.windowMode === 'calendar') &&
    hasLifecycleEvidence &&
    (!window.currentCycle ||
      window.currentCycle.state === 'provisional' ||
      !isReliableBoundary(window.currentCycle.boundaryAccuracy))
  ) {
    return unconfirmedLabel;
  }

  const cycleStartMs = window.cycleStartMs;
  const cycleEndMs = window.cycleEndMs;
  if (
    (window.windowMode === 'fixed' || window.windowMode === 'calendar') &&
    typeof cycleStartMs === 'number' &&
    Number.isFinite(cycleStartMs) &&
    typeof cycleEndMs === 'number' &&
    Number.isFinite(cycleEndMs) &&
    cycleStartMs < cycleEndMs
  ) {
    return formatRange(cycleStartMs, cycleEndMs, locale);
  }
  return formatRange(usage?.fromMs, usage?.toMs, locale);
};

const formatPreviousWindowRange = (
  window: AccountDetailQuotaWindow,
  usage: AccountDetailWindowUsageSummary | null | undefined,
  locale: string
): string => {
  const scheduledStartMs = window.previousCycle?.scheduledStartMs;
  const scheduledEndMs = window.previousCycle?.scheduledEndMs;
  if (
    (window.windowMode === 'fixed' || window.windowMode === 'calendar') &&
    typeof scheduledStartMs === 'number' &&
    Number.isFinite(scheduledStartMs) &&
    typeof scheduledEndMs === 'number' &&
    Number.isFinite(scheduledEndMs) &&
    scheduledStartMs < scheduledEndMs
  ) {
    return formatRange(scheduledStartMs, scheduledEndMs, locale);
  }
  return formatRange(usage?.fromMs, usage?.toMs, locale);
};

const formatObservedAt = (value: number, locale: string): string =>
  new Intl.DateTimeFormat(locale, {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(value);

const inferCardMode = (window: AccountDetailQuotaWindow): QuotaWindowCardMode => {
  if (!isIntervalAccountQuotaWindow(window)) return 'other';
  if (isModelScopedAccountQuotaWindow(window)) return 'model';
  return 'standard';
};

const windowIconForKind = (
  kind: AccountQuotaWindowKind | undefined,
  mode: QuotaWindowCardMode
): JSX.Element => {
  if (mode === 'model') return <IconModelCluster size={18} />;

  switch (kind) {
    case 'five_hour':
      return <IconTimer size={18} />;
    case 'daily':
      return <IconClock size={18} />;
    case 'weekly':
    case 'monthly':
    case 'billing':
      return <IconCalendar size={18} />;
    case 'payg':
      return <IconDollarSign size={18} />;
    case 'product':
      return <IconModelCluster size={18} />;
    case 'summary':
      return <IconSidebarQuota size={18} />;
    case 'unknown':
    default:
      return <IconCircleHelp size={18} />;
  }
};

const metricIconClass = (tone: MetricTone): string => {
  switch (tone) {
    case 'blue':
      return `${styles.rowIcon} ${styles.rowIconBlue}`;
    case 'green':
      return `${styles.rowIcon} ${styles.rowIconGreen}`;
    case 'teal':
      return `${styles.rowIcon} ${styles.rowIconTeal}`;
    case 'amber':
      return `${styles.rowIcon} ${styles.rowIconAmber}`;
    default:
      return styles.rowIcon;
  }
};

interface MetricItemProps {
  icon: JSX.Element;
  tone: MetricTone;
  label: string;
  value: string;
}

const MetricItem = ({ icon, tone, label, value }: MetricItemProps): JSX.Element => (
  <div className={styles.compareItem}>
    <span className={metricIconClass(tone)} aria-hidden="true">
      {icon}
    </span>
    <span className={styles.metricLabel}>{label}</span>
    <strong className={styles.metricValue}>{value}</strong>
  </div>
);

interface UsageLabels {
  requests: string;
  tokens: string;
  cost: string;
  successRate: string;
}

const UsageMetricList = ({
  usage,
  labels,
}: {
  usage: AccountDetailWindowUsageSummary;
  labels: UsageLabels;
}): JSX.Element => (
  <div className={styles.compareList}>
    <MetricItem
      icon={<IconChartLine size={16} />}
      tone="blue"
      label={labels.requests}
      value={formatOptionalCompactNumber(usage.totalRequests)}
    />
    <MetricItem
      icon={<IconBinary size={16} />}
      tone="teal"
      label={labels.tokens}
      value={formatOptionalCompactNumber(usage.totalTokens)}
    />
    <MetricItem
      icon={<IconDollarSign size={16} />}
      tone="amber"
      label={labels.cost}
      value={formatMoney(usage.totalCost)}
    />
    <MetricItem
      icon={<IconCheck size={16} />}
      tone="green"
      label={labels.successRate}
      value={formatPercent(usage.successRate, 2)}
    />
  </div>
);

const EmptyUsage = ({ message }: { message: string }): JSX.Element => (
  <div className={styles.emptyState}>{message}</div>
);

const QuotaProgress = ({
  percent,
  className,
}: {
  percent: number | null | undefined;
  className: string;
}): JSX.Element => (
  <div className={className} data-quota-progress="shared">
    <QuotaProgressBar
      percent={percent ?? null}
      highThreshold={QUOTA_PROGRESS_HIGH_THRESHOLD}
      mediumThreshold={QUOTA_PROGRESS_MEDIUM_THRESHOLD}
    />
  </div>
);

const UsageColumn = ({
  title,
  subtitle,
  period,
  usage,
  labels,
  emptyMessage,
}: {
  title: string;
  subtitle: string;
  period: 'current' | 'previous';
  usage: AccountDetailWindowUsageSummary | null | undefined;
  labels: UsageLabels;
  emptyMessage: string;
}): JSX.Element => (
  <div className={styles.compareColumn} data-quota-usage-period={period}>
    <div className={styles.compareColumnTitle}>{title}</div>
    <div className={styles.compareColumnSubtitle}>{subtitle || '\u00a0'}</div>
    {usage?.matched ? (
      <UsageMetricList usage={usage} labels={labels} />
    ) : (
      <EmptyUsage message={emptyMessage} />
    )}
  </div>
);

const ForecastColumn = ({
  forecast,
  title,
  subtitle,
  labels,
  unavailableMessage,
  emptyMessage,
}: {
  forecast: NonNullable<AccountDetailQuotaWindow['forecast']> | null | undefined;
  title: string;
  subtitle: string;
  labels: {
    requests: string;
    tokens: string;
    cost: string;
  };
  unavailableMessage: string;
  emptyMessage: string;
}): JSX.Element => (
  <div
    className={`${styles.compareColumn} ${styles.compareColumnPrediction}`}
    {...(forecast ? { 'data-quota-usage-forecast': 'true' } : {})}
  >
    <div className={styles.compareColumnTitle}>
      <IconTrendingUp size={18} />
      {title}
    </div>
    <div className={styles.compareColumnSubtitle}>{subtitle || '\u00a0'}</div>
    {forecast ? (
      <div className={styles.compareList}>
        <MetricItem
          icon={<IconChartLine size={16} />}
          tone="blue"
          label={labels.requests}
          value={formatOptionalCompactNumber(forecast.requests)}
        />
        <MetricItem
          icon={<IconBinary size={16} />}
          tone="teal"
          label={labels.tokens}
          value={formatOptionalCompactNumber(forecast.tokens)}
        />
        <MetricItem
          icon={<IconDollarSign size={16} />}
          tone="amber"
          label={labels.cost}
          value={formatMoney(forecast.cost)}
        />
        <div
          className={styles.forecastNotice}
          data-quota-forecast-success-rate="unavailable"
          role="note"
        >
          {unavailableMessage}
        </div>
      </div>
    ) : (
      <EmptyUsage message={emptyMessage} />
    )}
  </div>
);

export const QuotaWindowCard = ({
  window: q,
  mode,
  variant = 'drawer',
  locale,
}: QuotaWindowCardProps): JSX.Element => {
  const { t, i18n } = useTranslation();
  const resolvedLocale = locale ?? i18n.language;
  const resolvedMode = mode ?? inferCardMode(q);
  const usage = q.currentUsage ?? q.usage;
  const previousUsage = q.previousUsage;
  const resetTimestamp =
    typeof q.resetAtMs === 'number' && Number.isFinite(q.resetAtMs) ? q.resetAtMs : null;
  const resetDisplay = formatQuotaResetDisplay(resetTimestamp, q.resetLabel, resolvedLocale);
  const boundaryDisplay =
    q.windowMode === 'rolling' && resetTimestamp !== null
      ? t('accounts.detail_rolling_estimated_recovery', {
          defaultValue: '预计恢复：{{time}}',
          time: resetDisplay,
        })
      : resetDisplay;
  const compactTitle = [q.groupLabel, q.label, q.amountLabel, q.description]
    .filter(Boolean)
    .join(' · ');
  const usageLabels: UsageLabels = {
    requests: t('accounts.detail_usage_requests', {
      defaultValue: '请求数',
    }),
    tokens: t('accounts.detail_usage_tokens', { defaultValue: 'Token' }),
    cost: t('accounts.detail_usage_cost', { defaultValue: '预计花费' }),
    successRate: t('accounts.detail_success_rate'),
  };
  const forecastSubtitle =
    q.forecast?.basis === 'previous'
      ? t('accounts.detail_forecast_basis_previous', {
          defaultValue: '基于上个窗口实际值',
        })
      : q.forecast?.basis === 'quota'
        ? t('accounts.detail_forecast_basis_quota', {
            defaultValue: '基于 Provider 已用额度比例',
          })
        : '';
  const forecastEmptyMessage = t('accounts.detail_forecast_unavailable', {
    defaultValue: '暂无可用预测依据，暂不预测',
  });
  const currentWindowBoundaryUnconfirmed = t(
    'accounts.detail_current_window_boundary_unconfirmed',
    { defaultValue: '周期边界尚未确认' }
  );
  const hasUsageScopeWarning = (item: AccountDetailWindowUsageSummary | null | undefined) =>
    item?.scopeMatchStatus === 'partial' || item?.scopeMatchStatus === 'unmatched';
  const hasScopeWarning =
    q.modelScope?.complete === false ||
    hasUsageScopeWarning(usage) ||
    hasUsageScopeWarning(previousUsage);
  const unmatchedScopeRequests = [usage, previousUsage].reduce(
    (total, item) => (hasUsageScopeWarning(item) ? total + (item?.unmatchedRequests ?? 0) : total),
    0
  );
  const modelHasUsableUsage =
    resolvedMode === 'model' &&
    q.modelScope?.complete !== false &&
    Boolean(usage?.matched || previousUsage?.matched || q.forecast);
  const modelWindowStatsUnavailable = resolvedMode === 'model' && !modelHasUsableUsage;
  const lifecycleUnavailable = q.availability === 'pending_absent' || q.availability === 'inactive';
  const reopened = q.availability === 'active' && (q.activationGeneration ?? 0) > 1;
  const previousEndReason = q.previousCycle?.endReason ?? '';
  const hasEarlyReset = previousEndReason === 'early_reset';
  const hasProviderReset = previousEndReason === 'provider_reset';
  const hasLifecycleNotice = lifecycleUnavailable || reopened || hasEarlyReset || hasProviderReset;
  const hasSourceWarnings =
    (Boolean(q.stale) && !lifecycleUnavailable) ||
    hasLifecycleNotice ||
    (hasScopeWarning && !modelWindowStatsUnavailable);

  if (variant === 'compact') {
    return (
      <div className={styles.compactCard} title={compactTitle || q.label}>
        <span className={styles.compactLabel}>{q.label}</span>
        <QuotaProgress className={styles.compactBar} percent={q.remainingPercent} />
        <span className={styles.compactValue}>{formatPercent(q.remainingPercent)}</span>
        <span className={styles.compactReset}>{q.amountLabel ?? boundaryDisplay}</span>
      </div>
    );
  }

  const sourceMetaWarnings = hasSourceWarnings ? (
    <div className={styles.sourceMetaWarnings} data-quota-source-warnings="true">
      {q.stale && !lifecycleUnavailable ? (
        <span className={styles.sourceMetaWarn}>
          <IconTriangleAlert size={12} />
          {t('accounts.detail_quota_snapshot_stale')}
        </span>
      ) : null}
      {q.availability === 'pending_absent' ? (
        <span className={styles.sourceMetaWarn} data-quota-lifecycle-notice="pending_absent">
          <IconTriangleAlert size={12} />
          {t('accounts.detail_quota_window_pending_absent')}
        </span>
      ) : null}
      {q.availability === 'inactive' ? (
        <span className={styles.sourceMetaWarn} data-quota-lifecycle-notice="inactive">
          <IconTriangleAlert size={12} />
          {t('accounts.detail_quota_window_inactive')}
        </span>
      ) : null}
      {reopened ? (
        <span className={styles.sourceMetaWarn} data-quota-lifecycle-notice="reopened">
          <IconCircleHelp size={12} />
          {t('accounts.detail_quota_window_reopened', {
            generation: q.activationGeneration,
          })}
        </span>
      ) : null}
      {hasEarlyReset ? (
        <span className={styles.sourceMetaWarn} data-quota-lifecycle-notice="early_reset">
          <IconTriangleAlert size={12} />
          {t('accounts.detail_quota_window_early_reset')}
        </span>
      ) : null}
      {hasProviderReset ? (
        <span className={styles.sourceMetaWarn} data-quota-lifecycle-notice="provider_reset">
          <IconTriangleAlert size={12} />
          {t('accounts.detail_quota_window_provider_reset')}
        </span>
      ) : null}
      {!modelWindowStatsUnavailable && q.modelScope?.complete === false ? (
        <span className={styles.sourceMetaWarn}>
          <IconCircleHelp size={12} />
          {t('accounts.detail_scope_unknown')}
        </span>
      ) : null}
      {!modelWindowStatsUnavailable &&
      (hasUsageScopeWarning(usage) || hasUsageScopeWarning(previousUsage)) ? (
        <span className={styles.sourceMetaWarn}>
          <IconCircleHelp size={12} />
          {t('accounts.detail_scope_incomplete', {
            defaultValue: '模型范围用量可能不完整',
            count: unmatchedScopeRequests,
          })}
        </span>
      ) : null}
    </div>
  ) : null;

  const sourceMeta = (
    <div className={styles.sourceMeta}>
      <div className={styles.sourceMetaMain}>
        <span className={styles.sourceMetaItem}>
          <IconDatabaseZap size={14} />
          {t(`accounts.quota_observation_source_${q.observationSource ?? 'api_query'}`, {
            defaultValue: q.observationSource ?? 'api_query',
          })}
        </span>
        <span className={styles.sourceMetaItem}>
          <IconClock size={13} />
          {q.observedAtMs !== null && q.observedAtMs !== undefined
            ? formatObservedAt(q.observedAtMs, resolvedLocale)
            : '-'}
        </span>
        <span className={styles.sourceMetaSyncLabel}>
          {t('accounts.detail_quota_latest_observation_time', {
            defaultValue: 'Most recent observation',
          })}
        </span>
        <span className={styles.sourceMetaItem}>
          {isReliableBoundary(q.boundaryAccuracy) ? (
            <IconShieldCheck size={13} />
          ) : (
            <IconShield size={13} />
          )}
          {t(`accounts.quota_boundary_${q.boundaryAccuracy ?? 'unknown'}`, {
            defaultValue: q.boundaryAccuracy ?? 'unknown',
          })}
        </span>
        {q.relationshipKind === 'concurrent_subwindow' && q.containerProviderWindowId ? (
          <span className={styles.sourceMetaItem} data-quota-window-relationship="subwindow">
            <IconTimer size={13} />
            {t('accounts.detail_quota_window_subwindow', {
              container: q.containerProviderWindowId,
            })}
          </span>
        ) : null}
      </div>
      {sourceMetaWarnings}
    </div>
  );

  const modelWarning = modelWindowStatsUnavailable ? (
    <div className={styles.modelWarning} data-quota-model-warning="true" role="alert">
      <span className={styles.warningIcon} aria-hidden="true">
        <IconTriangleAlert size={13} />
      </span>
      <div>
        <strong>
          {q.modelScope?.complete === false
            ? t('accounts.detail_scope_unknown')
            : t('accounts.detail_model_window_stats_unavailable')}
        </strong>
        {q.modelScope?.complete === false ? null : (
          <p>{t('accounts.detail_model_window_stats_unavailable_desc')}</p>
        )}
      </div>
    </div>
  ) : null;

  const header = (
    <div className={styles.header}>
      <div className={styles.headerMain}>
        <span
          className={styles.windowIcon}
          data-quota-window-icon={resolvedMode === 'model' ? 'model' : (q.kind ?? 'unknown')}
        >
          {windowIconForKind(q.kind, resolvedMode)}
        </span>
        <div className={styles.headerLabel}>
          {q.groupLabel ? <span className={styles.groupLabel}>{q.groupLabel}</span> : null}
          <div className={styles.titleRow}>
            <strong className={styles.titleLabel} title={q.label}>
              {q.label}
            </strong>
            {resetDisplay !== '-' ? (
              <span className={styles.resetBadge}>
                {t('accounts.detail_quota_reset_at', {
                  defaultValue: '重置于 {{time}}',
                  time: resetDisplay,
                })}
              </span>
            ) : null}
          </div>
          {q.description ? (
            <span className={styles.subtitle} title={q.description}>
              {q.description}
            </span>
          ) : (resolvedMode === 'other' || q.windowMode === 'rolling') &&
            boundaryDisplay !== '-' ? (
            <span className={styles.subtitle}>{boundaryDisplay}</span>
          ) : null}
        </div>
      </div>
      <div className={styles.headerAside}>
        <div className={styles.remaining}>
          <span>{t('accounts.detail_quota_remaining_label', { defaultValue: '剩余' })}</span>
          <strong>{formatPercent(q.remainingPercent)}</strong>
        </div>
      </div>
    </div>
  );

  const progress = <QuotaProgress className={styles.bar} percent={q.remainingPercent} />;

  if (resolvedMode === 'other' || !isIntervalAccountQuotaWindow(q)) {
    return (
      <div
        className={`${styles.card} ${styles.otherCard}`}
        data-quota-window-mode={q.windowMode ?? 'unknown'}
        data-quota-window-availability={q.availability ?? 'unknown'}
        data-quota-card-mode="other"
      >
        {header}
        {progress}
        <div className={styles.meta}>
          {q.amountLabel ? <span className={styles.amountLabel}>{q.amountLabel}</span> : null}
          <span>
            {t('accounts.detail_used')}: {formatPercent(q.usedPercent)}
          </span>
          {q.windowMode === 'unknown' ? (
            <span className={styles.metaEmpty}>
              {t('accounts.detail_window_boundary_incomplete')}
            </span>
          ) : null}
        </div>
        {sourceMeta}
      </div>
    );
  }

  if (resolvedMode === 'model') {
    return (
      <div
        className={`${styles.card} ${styles.modelCard}`}
        data-quota-window-mode={q.windowMode ?? 'unknown'}
        data-quota-window-availability={q.availability ?? 'unknown'}
        data-quota-card-mode="model"
      >
        {header}
        {progress}
        {modelWarning}
        {modelHasUsableUsage ? (
          <div className={styles.compareColumns} data-quota-model-comparison="true">
            <UsageColumn
              title={
                q.previousPeriod === 'previous_equal_range'
                  ? t('accounts.detail_previous_equal_range', { defaultValue: '前一等长区间' })
                  : t('accounts.detail_previous_usage', { defaultValue: '上个窗口用量' })
              }
              subtitle={formatPreviousWindowRange(q, previousUsage, resolvedLocale)}
              period="previous"
              usage={previousUsage}
              labels={usageLabels}
              emptyMessage={t('accounts.detail_window_stats_empty', {
                defaultValue: '窗口统计暂未采集',
              })}
            />
            <UsageColumn
              title={t('accounts.detail_current_used', { defaultValue: '当前窗口已用' })}
              subtitle={formatCurrentWindowRange(
                q,
                usage,
                resolvedLocale,
                currentWindowBoundaryUnconfirmed
              )}
              period="current"
              usage={usage}
              labels={usageLabels}
              emptyMessage={t('accounts.detail_window_stats_empty', {
                defaultValue: '窗口统计暂未采集',
              })}
            />
            <ForecastColumn
              forecast={q.forecast}
              title={t('accounts.detail_current_forecast', { defaultValue: '当前窗口预测' })}
              subtitle={forecastSubtitle}
              labels={{
                requests: t('accounts.detail_forecast_requests', { defaultValue: '预计请求' }),
                tokens: t('accounts.detail_forecast_tokens', { defaultValue: '预计 Token' }),
                cost: t('accounts.detail_forecast_cost', { defaultValue: '预计花费' }),
              }}
              unavailableMessage={t('accounts.detail_forecast_success_rate_unavailable', {
                defaultValue: '暂不预测成功率',
              })}
              emptyMessage={forecastEmptyMessage}
            />
          </div>
        ) : null}
        {sourceMeta}
      </div>
    );
  }

  return (
    <div
      className={`${styles.card} ${styles.standardCard}`}
      data-quota-window-mode={q.windowMode ?? 'unknown'}
      data-quota-window-availability={q.availability ?? 'unknown'}
      data-quota-card-mode="standard"
    >
      {header}
      {progress}
      <div className={styles.compareColumns} data-quota-standard-comparison="true">
        <UsageColumn
          title={
            q.previousPeriod === 'previous_equal_range'
              ? t('accounts.detail_previous_equal_range', { defaultValue: '前一等长区间' })
              : t('accounts.detail_previous_usage', { defaultValue: '上个窗口用量' })
          }
          subtitle={formatPreviousWindowRange(q, previousUsage, resolvedLocale)}
          period="previous"
          usage={previousUsage}
          labels={usageLabels}
          emptyMessage={t('accounts.detail_window_stats_empty', {
            defaultValue: '窗口统计暂未采集',
          })}
        />
        <UsageColumn
          title={t('accounts.detail_current_used', { defaultValue: '当前窗口已用' })}
          subtitle={formatCurrentWindowRange(
            q,
            usage,
            resolvedLocale,
            currentWindowBoundaryUnconfirmed
          )}
          period="current"
          usage={usage}
          labels={usageLabels}
          emptyMessage={t('accounts.detail_window_stats_empty', {
            defaultValue: '窗口统计暂未采集',
          })}
        />
        <ForecastColumn
          forecast={q.forecast}
          title={t('accounts.detail_current_forecast', { defaultValue: '当前窗口预测' })}
          subtitle={forecastSubtitle}
          labels={{
            requests: t('accounts.detail_forecast_requests', { defaultValue: '预计请求' }),
            tokens: t('accounts.detail_forecast_tokens', { defaultValue: '预计 Token' }),
            cost: t('accounts.detail_forecast_cost', { defaultValue: '预计花费' }),
          }}
          unavailableMessage={t('accounts.detail_forecast_success_rate_unavailable', {
            defaultValue: '暂不预测成功率',
          })}
          emptyMessage={forecastEmptyMessage}
        />
      </div>
      {sourceMeta}
      {q.windowMode === 'unknown' ? (
        <div className={styles.emptyState}>{t('accounts.detail_window_boundary_incomplete')}</div>
      ) : null}
    </div>
  );
};
