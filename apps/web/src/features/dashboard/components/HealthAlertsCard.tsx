import { useMemo } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import type { DashboardChannelHealth, DashboardRecentFailure } from '@/services/api/usageService';
import type { CredentialInfo } from '@/types/sourceInfo';
import type { MonitoringAuthMeta, MonitoringChannelMeta } from '@/features/monitoring/model/types';
import type { ApiKeyDisplayInfo } from '@/features/monitoring/model/apiKeys';
import {
  buildMonitoringSourceDisplay,
  isRedundantMonitoringLabel,
  type MonitoringSourceDisplay,
} from '@/features/monitoring/model/sourceDisplay';
import { buildSourceInfoMap } from '@/utils/sourceResolver';
import { maskSensitiveText, truncateText } from '@/utils/format';
import { formatDurationMs } from '@/utils/usage';
import { getPlanPresentation } from '@/utils/plans';
import styles from './HealthAlertsCard.module.scss';

interface HealthAlertsCardProps {
  loading: boolean;
  recentFailures: DashboardRecentFailure[];
  channelHealth: DashboardChannelHealth[];
  authMetaMap: Map<string, MonitoringAuthMeta>;
  authFileMap: Map<string, CredentialInfo>;
  sourceInfoMap: ReturnType<typeof buildSourceInfoMap>;
  channelByAuthIndex: Map<string, MonitoringChannelMeta>;
  apiKeyAliasMap: Map<string, string>;
  apiKeyDisplayMap?: ReadonlyMap<string, ApiKeyDisplayInfo>;
}

export function HealthAlertsCard({
  loading,
  recentFailures,
  channelHealth,
  authMetaMap,
  authFileMap,
  sourceInfoMap,
  channelByAuthIndex,
  apiKeyAliasMap,
  apiKeyDisplayMap,
}: HealthAlertsCardProps) {
  const { t, i18n } = useTranslation();

  const formatPercent = useMemo(
    () =>
      new Intl.NumberFormat(i18n.language, {
        style: 'percent',
        maximumFractionDigits: 1,
      }).format,
    [i18n.language]
  );

  const sourceDisplayContext = {
    authMetaMap,
    authFileMap,
    sourceInfoMap,
    channelByAuthIndex,
    apiKeyAliasMap,
    apiKeyDisplayMap,
    language: i18n.language,
  };

  const channelRows = channelHealth.slice(0, 5).map((channel, index) => ({
    channel,
    key: `${channel.auth_index}-${index}`,
    display: buildMonitoringSourceDisplay(
      {
        source: channel.source,
        authIndex: channel.auth_index,
        accountSnapshot: channel.account_snapshot,
        authLabelSnapshot: channel.auth_label_snapshot,
        authProviderSnapshot: channel.auth_provider_snapshot,
        authLabel: channel.auth_label,
        account: channel.account,
        channel: channel.channel,
      },
      sourceDisplayContext
    ),
  }));
  const channelNameCounts = channelRows.reduce((map, row) => {
    map.set(row.display.primary, (map.get(row.display.primary) ?? 0) + 1);
    return map;
  }, new Map<string, number>());

  const buildRecentFailureDisplay = (failure: DashboardRecentFailure) =>
    buildMonitoringSourceDisplay(
      {
        source: failure.source,
        sourceHash: failure.source_hash,
        apiKeyHash: failure.api_key_hash,
        authIndex: failure.auth_index,
        accountSnapshot: failure.account_snapshot,
        authLabelSnapshot: failure.auth_label_snapshot,
        authProviderSnapshot: failure.auth_provider_snapshot,
        channel: failure.channel,
        authLabel: failure.auth_label,
        account: failure.account,
        apiKeyAlias: failure.api_key_alias,
        apiKeyMasked: failure.api_key_masked,
        apiKeyOrigin: failure.api_key_origin,
      },
      sourceDisplayContext
    );

  const buildFailureTooltip = (
    failure: DashboardRecentFailure,
    provider: string,
    apiKeyLabel?: string
  ) => {
    const statusCode = failure.fail_status_code;
    const summary = maskSensitiveText(failure.fail_summary || '');
    const quotaPlanLabel = getPlanPresentation({
      provider,
      planType: failure.header_quota_plan_type,
      t,
    })?.fullLabel;
    const diagnostics = [
      apiKeyLabel ? `API Key: ${apiKeyLabel}` : '',
      failure.header_error_code || failure.header_error_kind
        ? `${t('monitoring.header_error')}: ${[failure.header_error_kind, failure.header_error_code]
            .filter(Boolean)
            .join(' / ')}`
        : '',
      failure.header_trace_id ? `${t('monitoring.header_trace')}: ${failure.header_trace_id}` : '',
      failure.header_quota_plan_type || typeof failure.header_quota_used_percent === 'number'
        ? `${t('monitoring.header_quota')}: ${[
            quotaPlanLabel,
            typeof failure.header_quota_used_percent === 'number'
              ? `${failure.header_quota_used_percent.toFixed(0)}%`
              : '',
          ]
            .filter(Boolean)
            .join(' · ')}`
        : '',
    ].filter(Boolean);
    if (!statusCode && !summary && diagnostics.length === 0) return null;
    const statusText = statusCode ? `${t('monitoring.fail_status_code_short')} ${statusCode}` : '';
    const compactSummary = summary ? truncateText(summary, 160) : '';
    return {
      statusText,
      summary: compactSummary,
      diagnostics,
      title: [statusText, compactSummary, ...diagnostics].filter(Boolean).join(' · '),
    };
  };

  const renderFailureName = (
    display: MonitoringSourceDisplay,
    tooltip: ReturnType<typeof buildFailureTooltip>
  ) => (
    <span
      className={tooltip ? styles.failureSourceWithTooltip : undefined}
      tabIndex={tooltip ? 0 : undefined}
      title={tooltip ? undefined : display.title}
    >
      <span className={styles.failureSourceText}>
        {display.apiKeyLabel && !isRedundantMonitoringLabel(display.apiKeyLabel, display.primary)
          ? `${display.primary} · ${display.apiKeyLabel}`
          : display.primary}
      </span>
      {tooltip ? (
        <span role="tooltip" className={styles.failureTooltip}>
          {tooltip.statusText ? (
            <span className={styles.failureTooltipStatus}>{tooltip.statusText}</span>
          ) : null}
          {tooltip.summary ? (
            <span className={styles.failureTooltipBody}>{tooltip.summary}</span>
          ) : null}
          {tooltip.diagnostics.map((item) => (
            <span key={item} className={styles.failureTooltipBody}>
              {item}
            </span>
          ))}
        </span>
      ) : null}
    </span>
  );

  return (
    <>
      {/* 1. 渠道健康状态 */}
      <section className={styles.dataCard}>
        <div className={styles.cardHeader}>
          <h3>{t('dashboard.channel_health_status')}</h3>
          <Link to="/monitoring?tab=accounts" className={styles.moreBtn}>
            {t('dashboard.view_all_channels')}
          </Link>
        </div>
        <div className={styles.list}>
          {channelRows.map(({ channel, key, display }) => {
            const isDuplicateName = (channelNameCounts.get(display.primary) ?? 0) > 1;
            const label =
              channel.auth_index === '-'
                ? t('dashboard.health_unlinked_channel')
                : isDuplicateName && display.meta
                  ? `${display.primary} · ${display.meta}`
                  : display.primary;
            const rateText = formatPercent(channel.success_rate);
            const summaryTitle = t('dashboard.health_channel_failure_summary', {
              failures: channel.failures,
              calls: channel.calls,
              rate: rateText,
            });

            return (
              <div key={key} className={styles.listItem} title={summaryTitle}>
                <span className={`${styles.statusDot} ${styles[channel.tone] || ''}`} />
                <span
                  className={styles.label}
                  title={channel.auth_index === '-' ? summaryTitle : `${display.title} · ${summaryTitle}`}
                >
                  {label}
                </span>
                {channel.failures > 0 ? (
                  <span className={styles.channelFailures}>
                    {`${channel.failures}/${channel.calls}`}
                  </span>
                ) : null}
                <span className={styles.value}>{rateText}</span>
              </div>
            );
          })}
          {channelHealth.length === 0 ? (
            <div className={styles.empty}>
              {loading ? '...' : t('dashboard.no_channel_health_data')}
            </div>
          ) : null}
        </div>
      </section>

      {/* 2. 最近失败请求 */}
      <section className={styles.dataCard}>
        <div className={styles.cardHeader}>
          <h3>{t('dashboard.recent_failed_requests')}</h3>
          <Link to="/monitoring?tab=realtime" className={styles.moreBtn}>
            {t('dashboard.view_all_failures')}
          </Link>
        </div>
        <div className={styles.list}>
          {recentFailures.slice(0, 5).map((failure) => {
            const display = buildRecentFailureDisplay(failure);
            const tooltip = buildFailureTooltip(failure, display.provider, display.apiKeyLabel);
            return (
              <div
                key={`${failure.timestamp_ms}-${failure.source_hash}-${failure.model}`}
                className={styles.failureItem}
              >
                <div className={styles.failureMeta}>
                  <span className={styles.failureMetaLeft}>
                    <span className={styles.time}>
                      {new Date(failure.timestamp_ms).toLocaleTimeString(i18n.language)}
                    </span>
                    {typeof failure.fail_status_code === 'number' && failure.fail_status_code > 0 ? (
                      <span className={styles.statusCodeBadge}>{failure.fail_status_code}</span>
                    ) : failure.header_error_code || failure.endpoint ? (
                      <span className={styles.endpointBadge} title={failure.endpoint || undefined}>
                        {failure.header_error_code || failure.endpoint}
                      </span>
                    ) : null}
                  </span>
                  <span className={styles.model} title={failure.model}>
                    {failure.model}
                  </span>
                </div>
                <div className={styles.failureDetail}>
                  {renderFailureName(display, tooltip)}
                  <span className={styles.failureDuration}>
                    {formatDurationMs(failure.duration_ms, { locale: i18n.language })}
                  </span>
                </div>
              </div>
            );
          })}
          {recentFailures.length === 0 ? (
            <div className={styles.empty}>
              {loading ? '...' : t('dashboard.no_recent_failures')}
            </div>
          ) : null}
        </div>
      </section>
    </>
  );
}
