import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import {
  IconChartLine,
  IconCheck,
  IconChevronRight,
  IconChevronUp,
  IconClock,
  IconDatabaseZap,
  IconExternalLink,
  IconFileText,
  IconRefreshCw,
  IconShield,
  IconTimer,
  IconTriangleAlert,
  IconTrendingUp,
} from '@/components/ui/icons';
import type { AccountDetailViewModel } from '@/features/accounts/model/accountDetailViewModel';
import type { AccountRow } from '@/features/accounts/model/accountRows';
import type { MonitoringAnalyticsEventRow } from '@/services/api';
import {
  formatDurationMs,
  formatPercent,
  formatTimestamp,
  getEventFailureReason,
  getEventStatusText,
  translateDetailEnum,
} from '@/features/accounts/model/accountsPagePresentation';
import { formatCompactNumber } from '@/utils/usage';
import { CopyableText } from '../CopyableText';
import { AccountDetailFieldList } from './AccountDetailFieldList';
import styles from '@/features/accounts/AccountsPage.module.scss';

interface AccountDiagnosticsTabProps {
  row: AccountRow;
  detailView: AccountDetailViewModel;
  inspectionLoading: boolean;
  candidatesLoading: boolean;
  candidatesError: string;
  events: MonitoringAnalyticsEventRow[];
  eventsTotalCount: number;
  eventsHasMore: boolean;
  eventsLoading: boolean;
  eventsRefreshing: boolean;
  eventsAppending: boolean;
  eventsError: string;
  eventsUnavailable: boolean;
  nextBeforeMs: number | null;
  nextBeforeId: number | null;
  onRefreshEvents: () => void;
  onLoadMoreEvents: (beforeMs: number | null, beforeId: number | null) => void;
}

export function AccountDiagnosticsTab({
  row,
  detailView,
  inspectionLoading,
  candidatesLoading,
  candidatesError,
  events,
  eventsTotalCount,
  eventsHasMore,
  eventsLoading,
  eventsRefreshing,
  eventsAppending,
  eventsError,
  eventsUnavailable,
  nextBeforeMs,
  nextBeforeId,
  onRefreshEvents,
  onLoadMoreEvents,
}: AccountDiagnosticsTabProps) {
  const { t, i18n } = useTranslation();
  const conclusion = detailView.strategy.conclusion;
  const activity = detailView.strategy.activity;
  const monitoringParams = new URLSearchParams({ auth_file: row.fileName });
  if (row.authIndex) monitoringParams.set('auth_index', row.authIndex);

  const evidenceStatusClass = {
    current: styles.diagnosticEvidenceStatusCurrent,
    outdated: styles.diagnosticEvidenceStatusOutdated,
    conflict: styles.diagnosticEvidenceStatusConflict,
  }[conclusion.evidenceStatus];
  const hasInspectionEvidence = detailView.strategy.inspectionFields.length > 0;
  const hasCodexEvidence = detailView.strategy.codexBadges.length > 0;
  const hasCandidateEvidence =
    detailView.strategy.actionCandidates.length > 0 || Boolean(candidatesError);
  const hasDiagnosticEvidence = hasInspectionEvidence || hasCodexEvidence || hasCandidateEvidence;
  const activityTotalCalls = activity.totalCalls ?? eventsTotalCount;
  const recentFailureMeta = activity.recentFailure
    ? [
        formatTimestamp(activity.recentFailure.timestampMs, i18n.language, true),
        activity.recentFailure.model,
        activity.recentFailure.statusCode ? `HTTP ${activity.recentFailure.statusCode}` : '',
      ].filter(Boolean)
    : [];

  return (
    <div className={styles.diagnosticDetailStack} data-diagnostic-layout="prototype">
      <section
        className={`${styles.diagnosticCard} ${styles.diagnosticConclusionCard}`}
        data-diagnostic-card="conclusion"
        data-diagnostic-evidence-status={conclusion.evidenceStatus}
        aria-busy={inspectionLoading}
      >
        <div className={styles.diagnosticCardHeader}>
          <h3 className={styles.diagnosticSectionTitle}>
            <IconShield size={24} />
            <span>{t('accounts.detail_diagnostic_conclusion')}</span>
          </h3>
          <span className={`${styles.diagnosticEvidenceBadge} ${evidenceStatusClass}`}>
            {t(conclusion.evidenceStatusLabelKey)}
          </span>
        </div>
        <div className={styles.diagnosticConclusionMain}>
          <span
            className={styles.diagnosticConclusionMark}
            data-diagnostic-priority={conclusion.priority ?? 'normal'}
            aria-hidden="true"
          >
            {conclusion.priority ? <IconTriangleAlert size={15} /> : <IconCheck size={15} />}
          </span>
          <div className={styles.diagnosticConclusionCopy}>
            <strong>{t(conclusion.actionLabelKey)}</strong>
            <p>{t(conclusion.reasonKey)}</p>
          </div>
        </div>
        <div className={styles.diagnosticConclusionMeta}>
          <span>
            <IconFileText size={18} />
            <strong>{t('accounts.detail_diagnostic_source')}</strong>
            <span>{t(conclusion.sourceLabelKey)}</span>
          </span>
          {conclusion.observedAtMs !== null ? (
            <span>
              <IconClock size={18} />
              <strong>{t('accounts.detail_observed_at')}</strong>
              <span>{formatTimestamp(conclusion.observedAtMs, i18n.language)}</span>
            </span>
          ) : null}
          {conclusion.evidenceStatus !== 'current' && conclusion.latestActivityAtMs !== null ? (
            <span>
              <IconExternalLink size={18} />
              <strong>{t('accounts.detail_diagnostic_latest_activity')}</strong>
              <span>{formatTimestamp(conclusion.latestActivityAtMs, i18n.language)}</span>
            </span>
          ) : null}
        </div>
      </section>

      <section
        className={`${styles.diagnosticCard} ${styles.diagnosticActivityCard}`}
        data-diagnostic-card="activity"
        aria-busy={eventsLoading}
      >
        <div className={styles.diagnosticCardHeader}>
          <h3 className={styles.diagnosticSectionTitle}>
            <IconTrendingUp size={24} />
            <span>{t('accounts.detail_activity_title')}</span>
          </h3>
          <Button
            variant="secondary"
            size="sm"
            className={styles.diagnosticRefreshButton}
            onClick={onRefreshEvents}
            disabled={eventsUnavailable || eventsLoading || eventsRefreshing || eventsAppending}
            loading={eventsRefreshing}
          >
            {!eventsRefreshing ? <IconRefreshCw size={14} /> : null}
            {t('common.refresh')}
          </Button>
        </div>
        {eventsUnavailable ? (
          <p className={styles.diagnosticEmptyState}>{t('accounts.detail_events_unavailable')}</p>
        ) : eventsError ? (
          <div className={styles.errorBox}>{eventsError}</div>
        ) : (
          <div className={styles.diagnosticActivityBody}>
            <div className={styles.diagnosticKpiGrid}>
              <article
                className={styles.diagnosticKpiCard}
                data-diagnostic-activity-metric="requests"
              >
                <div>
                  <span>{t('accounts.detail_activity_requests')}</span>
                  <strong title={String(activityTotalCalls)}>
                    {formatCompactNumber(activityTotalCalls)}
                  </strong>
                </div>
                <span className={`${styles.diagnosticKpiIcon} ${styles.diagnosticKpiIconBlue}`}>
                  <IconDatabaseZap size={25} />
                </span>
              </article>
              <article
                className={styles.diagnosticKpiCard}
                data-diagnostic-activity-metric="failure-rate"
              >
                <div>
                  <span>{t('accounts.detail_activity_failure_rate')}</span>
                  <strong>{formatPercent(activity.failureRate, 1)}</strong>
                </div>
                <span className={`${styles.diagnosticKpiIcon} ${styles.diagnosticKpiIconPurple}`}>
                  <IconChartLine size={25} />
                </span>
              </article>
              <article
                className={styles.diagnosticKpiCard}
                data-diagnostic-activity-metric="p95-latency"
              >
                <div>
                  <span>{t('accounts.detail_activity_p95_latency')}</span>
                  <strong>{formatDurationMs(activity.p95LatencyMs)}</strong>
                </div>
                <span className={`${styles.diagnosticKpiIcon} ${styles.diagnosticKpiIconGreen}`}>
                  <IconTimer size={25} />
                </span>
              </article>
            </div>

            {activity.recentFailure ? (
              <div className={styles.diagnosticRecentFailure}>
                <span>{t('accounts.detail_activity_latest_failure')}</span>
                <strong>
                  {activity.recentFailure.reason || t('accounts.detail_event_failed_reason_empty')}
                </strong>
                {recentFailureMeta.length > 0 ? (
                  <small>{recentFailureMeta.join(' · ')}</small>
                ) : null}
              </div>
            ) : null}

            {events.length === 0 ? (
              <div className={styles.diagnosticActivityFooter}>
                <span>{t('accounts.detail_events_empty')}</span>
                <a href={`#/monitoring?${monitoringParams.toString()}`}>
                  <span>{t('accounts.detail_event_footer_open_monitoring')}</span>
                  <IconChevronRight size={17} />
                </a>
              </div>
            ) : (
              <>
                <div className={styles.diagnosticRequestList}>
                  {events.map((event) => {
                    const requestLabel = event.request_id || event.event_hash.slice(0, 10) || '-';
                    const modelLabel = event.resolved_model || event.model || '-';
                    const failureReason = getEventFailureReason(event);
                    return (
                      <article
                        key={event.event_hash}
                        className={styles.diagnosticRequestRow}
                        data-diagnostic-request={requestLabel}
                      >
                        <div className={styles.diagnosticRequestTop}>
                          <span
                            className={`${styles.eventStatus} ${
                              event.failed ? styles.eventStatusFailed : styles.eventStatusSuccess
                            }`}
                            title={failureReason || undefined}
                          >
                            {getEventStatusText(event, t)}
                          </span>
                          <div className={styles.diagnosticRequestId}>
                            <CopyableText
                              value={requestLabel}
                              copyValue={event.request_id || event.event_hash}
                              className={styles.diagnosticCopyButton}
                            />
                          </div>
                          <time className={styles.diagnosticRequestTime}>
                            {formatTimestamp(event.timestamp_ms, i18n.language, true)}
                          </time>
                          <span className={styles.diagnosticRequestModel} title={modelLabel}>
                            {modelLabel}
                          </span>
                        </div>
                        <div className={styles.diagnosticRequestMetrics}>
                          <span>
                            {t('accounts.value_input_tokens')}:{' '}
                            <b>{formatCompactNumber(event.input_tokens)}</b>
                          </span>
                          <span>
                            {t('accounts.value_output_tokens')}:{' '}
                            <b>{formatCompactNumber(event.output_tokens)}</b>
                          </span>
                          <span>
                            {t('accounts.detail_event_col_latency')}:{' '}
                            <b>{formatDurationMs(event.latency_ms)}</b>
                          </span>
                          <span>
                            TTFT: <b>{formatDurationMs(event.ttft_ms)}</b>
                          </span>
                        </div>
                        {event.failed ? (
                          <p className={styles.diagnosticRequestFailure}>
                            {failureReason || t('accounts.detail_event_failed_reason_empty')}
                          </p>
                        ) : null}
                      </article>
                    );
                  })}
                </div>
                {eventsHasMore ? (
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => onLoadMoreEvents(nextBeforeMs, nextBeforeId)}
                    disabled={eventsAppending}
                    loading={eventsAppending}
                  >
                    {t('accounts.detail_event_load_more')}
                  </Button>
                ) : null}
                <div className={styles.diagnosticActivityFooter}>
                  <span>
                    {t('accounts.detail_event_footer_count', {
                      shown: events.length,
                      total: eventsTotalCount || events.length,
                    })}
                  </span>
                  <a href={`#/monitoring?${monitoringParams.toString()}`}>
                    <span>{t('accounts.detail_event_footer_open_monitoring')}</span>
                    <IconChevronRight size={17} />
                  </a>
                </div>
              </>
            )}
          </div>
        )}
      </section>

      {hasDiagnosticEvidence ? (
        <details
          className={`${styles.diagnosticCard} ${styles.diagnosticEvidenceCard}`}
          data-diagnostic-card="evidence"
          aria-busy={inspectionLoading || candidatesLoading}
        >
          <summary>
            <span className={styles.diagnosticEvidenceSummaryTitle}>
              <IconFileText size={24} />
              <span>{t('accounts.detail_diagnostic_evidence')}</span>
            </span>
            {candidatesError ? (
              <small>{t('accounts.detail_diagnostic_evidence_error')}</small>
            ) : null}
            <IconChevronUp size={22} className={styles.diagnosticEvidenceChevron} />
          </summary>
          <div className={styles.diagnosticEvidenceBody}>
            {hasInspectionEvidence ? (
              <section
                className={`${styles.diagnosticEvidenceGroup} ${styles.diagnosticInspectionEvidenceGroup}`}
              >
                <AccountDetailFieldList fields={detailView.strategy.inspectionFields} />
              </section>
            ) : null}

            {hasCodexEvidence ? (
              <section className={styles.diagnosticEvidenceGroup}>
                <h4>{t('accounts.detail_diagnostic_codex_evidence')}</h4>
                <div className={styles.detailBadgeList}>
                  {[...detailView.strategy.codexBadges]
                    .sort((left, right) => {
                      const order = { danger: 0, warning: 1, info: 2 } as const;
                      return order[left.tone] - order[right.tone];
                    })
                    .map((badge) => (
                      <span
                        key={badge.kind}
                        className={`${styles.badge} ${
                          badge.tone === 'danger'
                            ? styles.badgeBad
                            : badge.tone === 'warning'
                              ? styles.badgeWarn
                              : styles.badgeInfo
                        }`}
                        title={
                          badge.titleKey
                            ? t(badge.titleKey, {
                                defaultValue: badge.defaultTitle,
                                ...badge.labelParams,
                              })
                            : undefined
                        }
                      >
                        {t(badge.labelKey, {
                          defaultValue: badge.defaultLabel,
                          ...badge.labelParams,
                        })}
                      </span>
                    ))}
                </div>
              </section>
            ) : null}

            {hasCandidateEvidence ? (
              <section className={styles.diagnosticEvidenceGroup}>
                <div className={styles.diagnosticEvidenceGroupHeader}>
                  <h4>{t('accounts.detail_diagnostic_candidate_evidence')}</h4>
                </div>
                {candidatesError ? (
                  <div className={styles.errorBox}>{candidatesError}</div>
                ) : detailView.strategy.actionCandidates.length > 0 ? (
                  <div className={styles.detailCandidateList}>
                    {detailView.strategy.actionCandidates.map((candidate) => {
                      const candidateReason = candidate.reasonCode
                        ? t(`account_actions.reason_${candidate.reasonCode}`, {
                            defaultValue: candidate.reason || '-',
                          })
                        : candidate.reason || '-';
                      return (
                        <div key={candidate.id} className={styles.detailCandidateItem}>
                          <div>
                            <div className={styles.detailCandidateHeader}>
                              <strong>
                                {t(`accounts.action_type_${candidate.actionType}`, {
                                  defaultValue: candidate.actionType,
                                })}
                              </strong>
                              <span className={styles.detailCandidateStatus}>
                                {translateDetailEnum(
                                  t,
                                  'accounts.action_status_',
                                  candidate.status
                                )}
                              </span>
                            </div>
                            <span>{candidateReason}</span>
                          </div>
                          <small>
                            {t('accounts.detail_action_candidate_meta', {
                              hits: candidate.hitCount,
                              seen: formatTimestamp(candidate.lastSeenAtMs, i18n.language),
                            })}
                          </small>
                        </div>
                      );
                    })}
                  </div>
                ) : null}
              </section>
            ) : null}
          </div>
        </details>
      ) : null}
    </div>
  );
}
