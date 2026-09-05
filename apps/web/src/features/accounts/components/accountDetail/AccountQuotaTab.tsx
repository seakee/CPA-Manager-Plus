import { useId } from 'react';
import { useTranslation } from 'react-i18next';
import type { JSX } from 'react';
import { Button } from '@/components/ui/Button';
import {
  IconBinary,
  IconChartLine,
  IconCheck,
  IconDollarSign,
  IconRefreshCw,
} from '@/components/ui/icons';
import type { AccountDetailViewModel } from '@/features/accounts/model/accountDetailViewModel';
import {
  formatPercent,
  formatQuotaResetTimestamp,
} from '@/features/accounts/model/accountsPagePresentation';
import {
  isIntervalAccountQuotaWindow,
  isModelScopedAccountQuotaWindow,
} from '@/features/accounts/model/accountQuotaDisplayWindows';
import { formatCompactNumber, formatUsd } from '@/utils/usage';
import { QuotaWindowCard } from '../QuotaWindowCard';
import styles from '@/features/accounts/AccountsPage.module.scss';

type MetricTone = 'blue' | 'green' | 'teal' | 'amber';

interface MetricCellProps {
  icon: JSX.Element;
  tone: MetricTone;
  label: string;
  value: string;
  valueTitle?: string;
}

const metricIconClass = (tone: MetricTone): string => {
  switch (tone) {
    case 'blue':
      return `${styles.metricIcon} ${styles.metricIconBlue}`;
    case 'green':
      return `${styles.metricIcon} ${styles.metricIconGreen}`;
    case 'teal':
      return `${styles.metricIcon} ${styles.metricIconTeal}`;
    case 'amber':
      return `${styles.metricIcon} ${styles.metricIconAmber}`;
    default:
      return styles.metricIcon;
  }
};

const metricCardClass = (tone: MetricTone): string => {
  switch (tone) {
    case 'blue':
      return styles.quotaSummaryMetricBlue;
    case 'green':
      return styles.quotaSummaryMetricGreen;
    case 'teal':
      return styles.quotaSummaryMetricTeal;
    case 'amber':
      return styles.quotaSummaryMetricAmber;
    default:
      return '';
  }
};

const MetricCell = ({ icon, tone, label, value, valueTitle }: MetricCellProps): JSX.Element => {
  const tooltipId = useId();
  const hasValueTooltip = valueTitle !== undefined && valueTitle !== value;

  return (
    <div className={`${styles.quotaSummaryMetric} ${metricCardClass(tone)}`}>
      <div className={styles.quotaSummaryMetricHeader} data-account-quota-metric-header="true">
        <span className={metricIconClass(tone)} aria-hidden="true">
          {icon}
        </span>
        <span className={styles.quotaSummaryMetricLabel}>{label}</span>
      </div>
      <span className={styles.quotaSummaryValueWrap} data-account-quota-metric-value="true">
        <strong
          className={styles.quotaSummaryValue}
          tabIndex={hasValueTooltip ? 0 : undefined}
          aria-describedby={hasValueTooltip ? tooltipId : undefined}
        >
          {value}
        </strong>
        {hasValueTooltip ? (
          <span id={tooltipId} className={styles.quotaSummaryValueTooltip} role="tooltip">
            <span className={styles.quotaSummaryValueTooltipLabel}>{label}</span>
            <span className={styles.quotaSummaryValueTooltipValue}>{valueTitle}</span>
          </span>
        ) : null}
      </span>
    </div>
  );
};

interface AccountQuotaTabProps {
  detailView: AccountDetailViewModel;
  windowUsageError: string;
  historyAvailable: boolean;
  historyRefreshing: boolean;
  onRefreshHistory: () => void;
  onResetQuota: () => void;
  resetQuotaDisabled: boolean;
}

export function AccountQuotaTab({
  detailView,
  windowUsageError,
  historyAvailable,
  historyRefreshing,
  onRefreshHistory,
  onResetQuota,
  resetQuotaDisabled,
}: AccountQuotaTabProps) {
  const { t, i18n } = useTranslation();
  const history = detailView.history;
  const allWindows = detailView.quota.windows;
  const standardWindows = allWindows.filter(
    (window) => isIntervalAccountQuotaWindow(window) && !isModelScopedAccountQuotaWindow(window)
  );
  const modelWindows = allWindows.filter(
    (window) => isIntervalAccountQuotaWindow(window) && isModelScopedAccountQuotaWindow(window)
  );
  const otherQuotaItems = allWindows.filter((window) => !isIntervalAccountQuotaWindow(window));

  const formatNumber = (value: number) => new Intl.NumberFormat(i18n.language).format(value);
  const formatTime = (value: number | null) =>
    value
      ? new Intl.DateTimeFormat(i18n.language, {
          year: 'numeric',
          month: '2-digit',
          day: '2-digit',
          hour: '2-digit',
          minute: '2-digit',
        }).format(value)
      : '-';
  const plugin = detailView.quota.plugin;
  const formatPluginCost = (value: number) => {
    if (plugin?.currency && plugin.minorUnit !== null) {
      return `${new Intl.NumberFormat(i18n.language, {
        minimumFractionDigits: plugin.minorUnit,
        maximumFractionDigits: plugin.minorUnit,
      }).format(value / 10 ** plugin.minorUnit)} ${plugin.currency}`;
    }
    return `${formatNumber(value)} ${t('accounts.detail_plugin_minor_units', {
      defaultValue: 'minor units',
    })}`;
  };
  const showPluginState =
    plugin !== null && plugin.windows.length === 0 && plugin.availability === 'unavailable';
  const pluginStateLabel = plugin?.stale
    ? t('accounts.detail_quota_snapshot_stale')
    : t('accounts.detail_plugin_quota_unavailable', {
        defaultValue: 'Plugin quota data is unavailable',
      });
  const maxDailyCost = Math.max(0, ...(plugin?.daily.map((day) => day.costMinorUnits) ?? []));
  const spend = plugin?.spend;
  const pluginMetrics = [
    [spend?.meteredMinorUnits, 'accounts.detail_plugin_metered_spend', 'Metered spend', true],
    [spend?.todayMinorUnits, 'accounts.detail_plugin_today', 'Today', true],
    [spend?.periodMinorUnits, 'accounts.detail_plugin_period_spend', 'Period spend', true],
    [spend?.latestTokens, 'accounts.detail_plugin_latest_tokens', 'Latest tokens', false],
    [spend?.periodTokens, 'accounts.detail_plugin_period_tokens', 'Period tokens', false],
  ] as const;

  const hasResetRecords =
    detailView.quota.resetCreditsAvailableCount !== null ||
    detailView.quota.resetCreditExpiries.length > 0;
  const shouldShowResetRecords = detailView.identity.provider === 'codex' && hasResetRecords;

  return (
    <div className={styles.quotaTab} data-account-quota-tab="true">
      <div className={styles.quotaTabHeader}>
        <div className={styles.quotaPageHeading}>
          <h2 className={styles.quotaPageTitle}>{t('accounts.detail_tab_quota')}</h2>
          <p>{t('accounts.detail_quota_window_usage_desc')}</p>
        </div>
        <div className={styles.quotaTabActions}>
          <Button
            variant="secondary"
            size="sm"
            onClick={onRefreshHistory}
            disabled={!historyAvailable || historyRefreshing}
            loading={historyRefreshing}
            title={!historyAvailable ? t('accounts.history_unavailable') : undefined}
          >
            {!historyRefreshing ? <IconRefreshCw size={15} /> : null}
            {t('accounts.refresh_history')}
          </Button>
        </div>
      </div>

      <section className={styles.quotaSummaryPanel} data-account-quota-usage-summary="true">
        <div className={styles.quotaSummaryHeading}>
          <h3>{t('accounts.detail_total_usage', { defaultValue: '凭证总体用量' })}</h3>
          <div className={styles.quotaSummaryMeta}>
            <span>{t('accounts.detail_usage_time_range', { defaultValue: '统计时间范围' })}</span>
            <strong>
              {history
                ? `${formatTime(history.firstSeenMs)} — ${formatTime(history.lastSeenMs)}`
                : t('accounts.detail_usage_time_empty', { defaultValue: '暂无使用时间范围' })}
            </strong>
          </div>
        </div>
        <div className={styles.quotaSummaryMetrics} data-account-quota-metrics="true">
          <MetricCell
            icon={<IconChartLine size={20} />}
            tone="blue"
            label={t('accounts.detail_total_requests')}
            value={history ? formatCompactNumber(history.totalRequests) : '-'}
            valueTitle={history ? formatNumber(history.totalRequests) : undefined}
          />
          <MetricCell
            icon={<IconBinary size={20} />}
            tone="teal"
            label={t('accounts.detail_total_tokens')}
            value={history ? formatCompactNumber(history.totalTokens) : '-'}
            valueTitle={history ? formatNumber(history.totalTokens) : undefined}
          />
          <MetricCell
            icon={<IconDollarSign size={20} />}
            tone="amber"
            label={t('accounts.detail_total_cost')}
            value={history ? formatUsd(history.totalCost) : '-'}
          />
          <MetricCell
            icon={<IconCheck size={20} />}
            tone="green"
            label={t('accounts.detail_success_rate')}
            value={formatPercent(history?.successRate, 2)}
          />
          {pluginMetrics.map(([value, labelKey, defaultLabel, money]) =>
            value === null || value === undefined ? null : (
              <MetricCell
                key={labelKey}
                icon={money ? <IconDollarSign size={20} /> : <IconBinary size={20} />}
                tone={money ? 'amber' : 'teal'}
                label={t(labelKey, { defaultValue: defaultLabel })}
                value={money ? formatPluginCost(value) : formatCompactNumber(value)}
                valueTitle={money ? undefined : formatNumber(value)}
              />
            )
          )}
        </div>
      </section>

      {plugin &&
      (showPluginState ||
        plugin.daily.length > 0 ||
        plugin.topModel ||
        plugin.provenance.length > 0) ? (
        <section className={styles.quotaSection} data-account-plugin-quota="true">
          <div className={styles.quotaSectionHeading}>
            <h3>{t('accounts.detail_plugin_quota_title', { defaultValue: 'Plugin quota' })}</h3>
            <span>
              {t('accounts.detail_plugin_quota_desc', {
                defaultValue: 'Provider-reported spend and token usage',
              })}
            </span>
          </div>
          {showPluginState ? (
            <p className={styles.quotaEmpty} data-account-plugin-quota-state="true">
              {pluginStateLabel}
            </p>
          ) : null}
          {plugin.daily.length > 0 ? (
            <div className={styles.pluginQuotaDaily}>
              <h4>
                {t('accounts.detail_plugin_daily_usage', { defaultValue: 'Daily plugin usage' })}
              </h4>
              <ul className={styles.pluginQuotaHistogram} data-account-quota-daily-chart="true">
                {plugin.daily.map((day) => {
                  const height =
                    maxDailyCost > 0 ? Math.max(4, (day.costMinorUnits / maxDailyCost) * 100) : 4;
                  return (
                    <li
                      key={day.date}
                      className={styles.pluginQuotaDailyItem}
                      data-account-quota-daily-bar="true"
                    >
                      <span className={styles.pluginQuotaBarTrack} aria-hidden="true">
                        <span className={styles.pluginQuotaBar} style={{ height: `${height}%` }} />
                      </span>
                      <strong>{day.date}</strong>
                      <span>{formatPluginCost(day.costMinorUnits)}</span>
                      {day.tokens !== null ? (
                        <span>
                          {formatNumber(day.tokens)} {t('accounts.detail_usage_tokens')}
                        </span>
                      ) : null}
                    </li>
                  );
                })}
              </ul>
            </div>
          ) : null}

          {plugin.topModel || plugin.provenance.length > 0 ? (
            <div className={styles.quotaSummaryMeta}>
              {plugin.topModel ? (
                <span>
                  {t('accounts.detail_plugin_top_model', { defaultValue: 'Top model' })}:{' '}
                  <strong>{plugin.topModel}</strong>
                </span>
              ) : null}
              {plugin.provenance.length > 0 ? (
                <span data-account-plugin-quota-provenance="true">
                  {t('accounts.detail_plugin_provenance', { defaultValue: 'Sources' })}:{' '}
                  <strong>{plugin.provenance.join(', ')}</strong>
                </span>
              ) : null}
            </div>
          ) : null}
        </section>
      ) : null}

      {windowUsageError ? <div className={styles.errorBox}>{windowUsageError}</div> : null}

      {standardWindows.length > 0 || allWindows.length === 0 ? (
        <section className={styles.quotaSection} data-quota-window-group="standard">
          <div className={styles.quotaSectionHeading}>
            <h3>{t('accounts.detail_quota_standard_title', { defaultValue: '标准额度' })}</h3>
            <span>
              {t('accounts.detail_quota_standard_desc', {
                defaultValue: '按时间窗口统计并滚动更新',
              })}
            </span>
          </div>
          {standardWindows.length > 0 ? (
            <div className={styles.quotaCardList}>
              {standardWindows.map((window) => (
                <QuotaWindowCard
                  key={window.key}
                  window={window}
                  mode="standard"
                  locale={i18n.language}
                />
              ))}
            </div>
          ) : (
            <p className={styles.quotaEmpty}>{t('accounts.detail_no_quota_windows')}</p>
          )}
        </section>
      ) : null}

      {modelWindows.length > 0 ? (
        <section className={styles.quotaSection} data-quota-window-group="model">
          <div className={styles.quotaSectionHeading}>
            <h3>{t('accounts.detail_quota_model_title', { defaultValue: '模型额度' })}</h3>
            <span>
              {t('accounts.detail_quota_model_desc', {
                defaultValue: '按模型及窗口统计的配额信息',
              })}
            </span>
          </div>
          <div className={styles.quotaCardList}>
            {modelWindows.map((window) => (
              <QuotaWindowCard
                key={window.key}
                window={window}
                mode="model"
                locale={i18n.language}
              />
            ))}
          </div>
        </section>
      ) : null}

      {otherQuotaItems.length > 0 ? (
        <section className={styles.quotaSection} data-quota-window-group="other">
          <div className={styles.quotaSectionHeading}>
            <h3>{t('accounts.detail_quota_other_items', { defaultValue: '其他额度项' })}</h3>
            <span>
              {t('accounts.detail_quota_other_items_desc', {
                defaultValue: '金额、产品或缺少完整窗口边界的额度不生成区间统计。',
              })}
            </span>
          </div>
          <div className={styles.quotaCardList}>
            {otherQuotaItems.map((window) => (
              <QuotaWindowCard
                key={window.key}
                window={window}
                mode="other"
                locale={i18n.language}
              />
            ))}
          </div>
        </section>
      ) : null}

      {shouldShowResetRecords ? (
        <section
          className={styles.quotaSection}
          data-account-quota-evidence="true"
          data-account-quota-reset-records="true"
        >
          <div className={styles.quotaResetCard} data-quota-evidence-panel="reset">
            <div className={styles.quotaResetHeader}>
              <div className={styles.quotaResetHeaderMain}>
                <span
                  className={`${styles.quotaPanelIcon} ${styles.quotaResetIcon}`}
                  aria-hidden="true"
                >
                  <IconRefreshCw size={16} />
                </span>
                <div className={styles.quotaResetTitle}>
                  <h3>{t('accounts.detail_quota_reset_records', { defaultValue: '重置记录' })}</h3>
                  <span>{t('codex_quota.reset_credits_card_subtitle')}</span>
                </div>
              </div>
              <div className={styles.quotaResetHeaderActions}>
                {detailView.quota.resetCreditsAvailableCount !== null ? (
                  <div className={styles.quotaResetCount} data-quota-reset-count="true">
                    <span>{t('codex_quota.reset_credits_available_label')}</span>
                    <strong>{detailView.quota.resetCreditsAvailableCount}</strong>
                    <span className={styles.quotaResetCountUnit}>
                      {t('codex_quota.reset_credits_unit')}
                    </span>
                  </div>
                ) : null}
                <Button
                  variant="secondary"
                  size="sm"
                  className={styles.quotaResetAction}
                  data-quota-reset-action="true"
                  onClick={onResetQuota}
                  disabled={resetQuotaDisabled}
                >
                  <IconRefreshCw size={14} />
                  {t('codex_quota.reset_action_button')}
                </Button>
              </div>
            </div>
            {detailView.quota.resetCreditsAvailableCount === 0 ? (
              <div className={styles.quotaResetAvailabilityNote} role="status">
                {t('codex_quota.reset_credits_unavailable_label')}
              </div>
            ) : null}
            {detailView.quota.resetCreditExpiries.length > 0 ? (
              <div className={styles.quotaResetExpirySection}>
                <span className={styles.quotaResetExpiryLabel}>
                  {t('codex_quota.reset_credits_expected_expiry_label')}
                </span>
                <div className={styles.quotaResetExpiryList}>
                  {detailView.quota.resetCreditExpiries.map((item, index) => (
                    <div
                      key={`${item.id}:${item.expiresAtMs}`}
                      className={styles.quotaResetExpiryItem}
                    >
                      <span>{t('codex_quota.reset_credit_expiry_item', { index: index + 1 })}</span>
                      <strong data-quota-reset-credit-expiry={item.id}>
                        {formatQuotaResetTimestamp(item.expiresAtMs, i18n.language)}
                      </strong>
                    </div>
                  ))}
                </div>
              </div>
            ) : null}
            {detailView.quota.cooldown ? (
              <div className={styles.quotaResetCooldown}>
                <span>{t('accounts.detail_cooldown')}</span>
                <strong data-quota-cooldown-recover-at="true">
                  {formatQuotaResetTimestamp(detailView.quota.cooldown.recoverAtMs, i18n.language)}
                </strong>
              </div>
            ) : null}
          </div>
        </section>
      ) : null}
    </div>
  );
}
