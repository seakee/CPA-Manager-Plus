import type { CSSProperties } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import type { UsageArchivePreview, UsageMaintenanceStatus } from '@/services/api/usageService';
import { formatDateTime, formatFileSize } from '@/utils/format';
import {
  retentionPresetDays,
  toLocalDateTimeValue,
  type RawEventRangeState,
  type RetentionPresetDays,
  type RetentionSelection,
} from './usageMaintenanceModel';
import styles from './UsageMaintenanceCreateView.module.scss';

export type GuidedArchiveStage =
  | 'idle'
  | 'creating'
  | 'archiving'
  | 'verifying'
  | 'complete'
  | 'attention';

type Props = {
  maintenance: UsageMaintenanceStatus;
  preview: UsageArchivePreview | null;
  previewLoading: boolean;
  previewError: string | null;
  retentionSelection: RetentionSelection;
  customCutoff: string;
  referenceNowMS: number;
  resolvedCutoffTimestamp?: number;
  rawEventRange: RawEventRangeState;
  recommendedRetentionDays: RetentionPresetDays | null;
  guidedArchiveStage: GuidedArchiveStage;
  guidedArchiveRunId: string | null;
  working: boolean;
  createBlockedByMaintenance: boolean;
  archiveReadinessPending: boolean;
  archiveReadinessHint: string;
  onBack: () => void;
  onRefresh: () => void;
  onSelectRetention: (selection: RetentionSelection) => void;
  onUpdateCustomCutoff: (value: string) => void;
  onRetryPreview: () => void;
  onCreate: () => void;
  onStopWaiting: () => void;
};

const migrationStatuses = new Set([
  'discovering',
  'pending',
  'running',
  'applying',
  'clearing',
  'completed',
  'failed',
]);
const aggregateStatuses = new Set([
  'pending',
  'backfilling',
  'catching_up',
  'clearing',
  'ready',
  'failed',
]);

export function UsageMaintenanceCreateView({
  maintenance,
  preview,
  previewLoading,
  previewError,
  retentionSelection,
  customCutoff,
  referenceNowMS,
  resolvedCutoffTimestamp,
  rawEventRange,
  recommendedRetentionDays,
  guidedArchiveStage,
  guidedArchiveRunId,
  working,
  createBlockedByMaintenance,
  archiveReadinessPending,
  archiveReadinessHint,
  onBack,
  onRefresh,
  onSelectRetention,
  onUpdateCustomCutoff,
  onRetryPreview,
  onCreate,
  onStopWaiting,
}: Props) {
  const { t, i18n } = useTranslation();
  const formatTime = (value?: number) =>
    value ? formatDateTime(new Date(value), i18n.language) : '-';
  const hasArchivedRawEvents = (maintenance.raw_archived_event_count ?? 0) > 0;
  const recommendedPresetAvailable =
    !hasArchivedRawEvents &&
    recommendedRetentionDays !== null &&
    recommendedRetentionDays !== retentionSelection;
  const canCreate =
    Boolean(preview && preview.event_count > 0) &&
    !previewLoading &&
    !previewError &&
    !working &&
    !createBlockedByMaintenance &&
    !archiveReadinessPending;
  const createDisabledReason = createBlockedByMaintenance
    ? t('usage_maintenance.create_blocked_active', {
        defaultValue:
          'Finish or recover the active maintenance task before starting another archive.',
      })
    : archiveReadinessPending
      ? archiveReadinessHint
      : previewLoading
        ? t('usage_maintenance.preview_loading', { defaultValue: 'Calculating…' })
        : previewError || undefined;

  const cutoffPosition = (() => {
    if (rawEventRange.kind !== 'available' || !resolvedCutoffTimestamp) return null;
    const span = rawEventRange.maxTimestampMS - rawEventRange.minTimestampMS;
    if (span <= 0) return null;
    return Math.min(
      100,
      Math.max(0, ((resolvedCutoffTimestamp - rawEventRange.minTimestampMS) / span) * 100)
    );
  })();
  const timelineStyle =
    cutoffPosition === null
      ? undefined
      : ({ '--cutoff-position': `${cutoffPosition}%` } as CSSProperties);

  const knownStatus = (prefix: string, value: string, known: ReadonlySet<string>) =>
    known.has(value) ? t(`usage_maintenance.${prefix}_${value}`, { defaultValue: value }) : value;
  const guidedStageLabel = () =>
    t(`usage_maintenance.archive_prepare_${guidedArchiveStage}`, {
      defaultValue:
        guidedArchiveStage === 'creating'
          ? 'Creating archive task'
          : guidedArchiveStage === 'archiving'
            ? 'Writing archive'
            : guidedArchiveStage === 'verifying'
              ? 'Verifying archive'
              : guidedArchiveStage === 'complete'
                ? 'Archive verified'
                : guidedArchiveStage === 'attention'
                  ? 'Needs attention'
                  : '',
    });

  return (
    <div className={styles.view}>
      <header className={styles.pageHeader}>
        <div>
          <h1>
            {t('usage_maintenance.create_page_title', { defaultValue: 'Create archive task' })}
          </h1>
          <p>
            {t('usage_maintenance.create_page_subtitle', {
              defaultValue:
                'Choose a retention policy, preview the exact impact, then archive and verify without deleting raw data.',
            })}
          </p>
        </div>
        <Button variant="secondary" size="sm" onClick={onBack} disabled={working}>
          ← {t('common.back')}
        </Button>
      </header>

      <div className={styles.layout}>
        <div className={styles.sideColumn}>
          <section className={styles.card}>
            <h2>{t('usage_maintenance.retention_policy', { defaultValue: 'Retention policy' })}</h2>
            <p className={styles.sectionHint}>
              {t('usage_maintenance.retention_description', {
                defaultValue:
                  'Events older than the selected period are included in the archive preview. Recent events remain in SQLite.',
              })}
            </p>
            <div
              className={styles.retentionOptions}
              role="group"
              aria-label={t('usage_maintenance.retention_group_label', {
                defaultValue: 'Raw data retention period',
              })}
            >
              {retentionPresetDays.map((days) => (
                <button
                  key={days}
                  type="button"
                  className={retentionSelection === days ? styles.retentionActive : ''}
                  aria-pressed={retentionSelection === days}
                  disabled={working}
                  onClick={() => onSelectRetention(days)}
                >
                  {t(`usage_maintenance.retention_${days}_label`, {
                    defaultValue: `Keep ${days} days`,
                  })}
                </button>
              ))}
              <button
                type="button"
                className={retentionSelection === 'custom' ? styles.retentionActive : ''}
                aria-pressed={retentionSelection === 'custom'}
                disabled={working}
                onClick={() => onSelectRetention('custom')}
              >
                {t('usage_maintenance.retention_custom_label', { defaultValue: 'Custom date' })}
              </button>
            </div>
            {retentionSelection === 'custom' ? (
              <div className={styles.dateField}>
                <Input
                  type="datetime-local"
                  label={t('usage_maintenance.cutoff', { defaultValue: 'Archive events before' })}
                  hint={t('usage_maintenance.custom_cutoff_hint', {
                    defaultValue: 'The cutoff must be in the past.',
                  })}
                  max={toLocalDateTimeValue(referenceNowMS)}
                  value={customCutoff}
                  disabled={working}
                  error={!resolvedCutoffTimestamp ? (previewError ?? undefined) : undefined}
                  onChange={(event) => onUpdateCustomCutoff(event.target.value)}
                />
              </div>
            ) : null}
            <div className={styles.cutoffFact}>
              <span>
                {t('usage_maintenance.resolved_cutoff_label', {
                  defaultValue: 'Resolved archive cutoff',
                })}
              </span>
              <strong>{formatTime(resolvedCutoffTimestamp)}</strong>
              <small>
                {t('usage_maintenance.resolved_cutoff_hint', {
                  defaultValue: 'Older events are archived; newer events stay online.',
                })}
              </small>
            </div>
          </section>

          <section className={styles.card}>
            <h2>
              {t('usage_maintenance.online_range_title', { defaultValue: 'Current online range' })}
            </h2>
            <dl className={styles.keyValues}>
              <div>
                <dt>{t('usage_maintenance.oldest_event', { defaultValue: 'Oldest event' })}</dt>
                <dd>
                  {rawEventRange.kind === 'available'
                    ? formatTime(rawEventRange.minTimestampMS)
                    : '-'}
                </dd>
              </div>
              <div>
                <dt>{t('usage_maintenance.latest_event', { defaultValue: 'Latest event' })}</dt>
                <dd>
                  {rawEventRange.kind === 'available'
                    ? formatTime(rawEventRange.maxTimestampMS)
                    : '-'}
                </dd>
              </div>
              <div>
                <dt>
                  {t('usage_maintenance.online_event_count', { defaultValue: 'Online events' })}
                </dt>
                <dd>{maintenance.raw_event_count.toLocaleString(i18n.language)}</dd>
              </div>
            </dl>
            <p className={styles.infoNote}>
              {rawEventRange.kind === 'unavailable'
                ? t('usage_maintenance.raw_range_unavailable', {
                    defaultValue: 'Time range unavailable on this server version',
                  })
                : t('usage_maintenance.online_range_note', {
                    defaultValue:
                      'This range comes from the current local SQLite summary. The server recalculates the target when the archive task is created.',
                  })}
            </p>
          </section>
        </div>

        <div className={styles.mainColumn}>
          <section className={styles.card} aria-live="polite" aria-busy={previewLoading}>
            <div className={styles.sectionHeader}>
              <div>
                <h2>{t('usage_maintenance.preview', { defaultValue: 'Impact preview' })}</h2>
                <p className={styles.sectionHint}>
                  {t('usage_maintenance.preview_excludes_existing', {
                    defaultValue: 'Existing archive references are excluded from this estimate.',
                  })}
                </p>
              </div>
              {previewLoading ? (
                <span className={styles.loadingLabel}>
                  <span className="loading-spinner" aria-hidden="true" />
                  {t('usage_maintenance.preview_loading', { defaultValue: 'Calculating…' })}
                </span>
              ) : null}
            </div>

            {rawEventRange.kind === 'available' ? (
              <div className={styles.timelineBlock} style={timelineStyle}>
                <div className={styles.timeline}>
                  {cutoffPosition === null ? null : <i aria-hidden="true" />}
                </div>
                <div className={styles.timelineLabels}>
                  <span>
                    {t('usage_maintenance.oldest_event', { defaultValue: 'Oldest event' })}
                    <strong>{formatTime(rawEventRange.minTimestampMS)}</strong>
                  </span>
                  <span>
                    {t('usage_maintenance.cutoff_short', { defaultValue: 'Cutoff' })}
                    <strong>{formatTime(resolvedCutoffTimestamp)}</strong>
                  </span>
                  <span>
                    {t('usage_maintenance.latest_event', { defaultValue: 'Latest event' })}
                    <strong>{formatTime(rawEventRange.maxTimestampMS)}</strong>
                  </span>
                </div>
              </div>
            ) : null}

            {guidedArchiveStage !== 'idle' ? (
              <div className={styles.guidedProgress}>
                <div className={styles.guidedHeader}>
                  <strong>{guidedStageLabel()}</strong>
                  {guidedArchiveRunId ? (
                    <span>
                      {t('usage_maintenance.archive_task_label', {
                        defaultValue: 'Task {{runId}}',
                        runId: guidedArchiveRunId,
                      })}
                    </span>
                  ) : null}
                </div>
                <div className={styles.guidedSteps}>
                  {(['archive', 'verify', 'delete'] as const).map((step) => {
                    const state =
                      guidedArchiveStage === 'complete'
                        ? step === 'delete'
                          ? 'current'
                          : 'complete'
                        : guidedArchiveStage === 'attention'
                          ? 'pending'
                          : step === 'archive'
                            ? guidedArchiveStage === 'creating' ||
                              guidedArchiveStage === 'archiving'
                              ? 'current'
                              : 'complete'
                            : step === 'verify' && guidedArchiveStage === 'verifying'
                              ? 'current'
                              : 'pending';
                    return (
                      <span key={step} className={styles[`guided_${state}`]}>
                        <i>{state === 'complete' ? '✓' : ''}</i>
                        {t(`usage_maintenance.guided_step_${step}`, {
                          defaultValue:
                            step === 'archive'
                              ? 'Archive'
                              : step === 'verify'
                                ? 'Verify'
                                : 'Optional delete',
                        })}
                      </span>
                    );
                  })}
                </div>
                {guidedArchiveStage !== 'complete' ? (
                  <Button
                    size="xs"
                    variant="ghost"
                    onClick={onStopWaiting}
                    disabled={guidedArchiveStage === 'attention'}
                  >
                    {t('usage_maintenance.archive_prepare_stop', { defaultValue: 'Stop waiting' })}
                  </Button>
                ) : (
                  <p>
                    {t('usage_maintenance.archive_prepare_no_delete', {
                      defaultValue:
                        'Raw events are unchanged. Delete them only from the separate action.',
                    })}
                  </p>
                )}
              </div>
            ) : null}

            {previewError && resolvedCutoffTimestamp ? (
              <div className={styles.errorNote}>
                <p>{previewError}</p>
                <Button size="sm" variant="secondary" onClick={onRetryPreview} disabled={working}>
                  {t('usage_maintenance.preview_retry', { defaultValue: 'Retry calculation' })}
                </Button>
              </div>
            ) : null}

            {!previewLoading && !previewError && preview?.event_count === 0 ? (
              <div className={styles.emptyNote}>
                <strong>
                  {t('usage_maintenance.preview_empty_title', {
                    defaultValue: 'No events match this retention policy',
                  })}
                </strong>
                <p>
                  {rawEventRange.kind === 'empty'
                    ? t('usage_maintenance.preview_empty_no_data', {
                        defaultValue:
                          'There is no raw usage data to archive yet. Import or collect events first.',
                      })
                    : hasArchivedRawEvents
                      ? t('usage_maintenance.preview_empty_archived', {
                          defaultValue:
                            'Some older raw events are already protected by an archive. Review archive history before changing the retention period.',
                        })
                      : recommendedPresetAvailable
                        ? t('usage_maintenance.preview_empty_recommendation', {
                            defaultValue:
                              'The oldest raw event is {{oldest}}. Keep {{days}} days to include older events while preserving recent data.',
                            oldest:
                              rawEventRange.kind === 'available'
                                ? formatTime(rawEventRange.minTimestampMS)
                                : '-',
                            days: recommendedRetentionDays,
                          })
                        : rawEventRange.kind === 'available'
                          ? t('usage_maintenance.preview_empty_recent', {
                              defaultValue:
                                'All raw events are newer than the standard retention presets. Use a custom date only if you intentionally want to archive recent data.',
                            })
                          : t('usage_maintenance.preview_empty_generic', {
                              defaultValue:
                                'Try a shorter retention period or choose a custom cutoff after the oldest event.',
                            })}
                </p>
                {recommendedPresetAvailable ? (
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => onSelectRetention(recommendedRetentionDays)}
                    disabled={working}
                  >
                    {t('usage_maintenance.use_recommended_retention', {
                      defaultValue: 'Keep {{days}} days',
                      days: recommendedRetentionDays,
                    })}
                  </Button>
                ) : null}
              </div>
            ) : null}

            {!previewError && preview && preview.event_count > 0 ? (
              <div className={styles.previewGrid}>
                <div>
                  <span>
                    {t('usage_maintenance.preview_events', { defaultValue: 'Eligible events' })}
                  </span>
                  <strong>{preview.event_count.toLocaleString(i18n.language)}</strong>
                  <small>
                    {t('usage_maintenance.preview_events_hint', {
                      defaultValue: 'Not present in existing archive references',
                    })}
                  </small>
                </div>
                <div>
                  <span>
                    {t('usage_maintenance.preview_source_bytes', {
                      defaultValue: 'Estimated source size',
                    })}
                  </span>
                  <strong>{formatFileSize(preview.estimated_bytes)}</strong>
                  <small>
                    {t('usage_maintenance.preview_source_bytes_hint', {
                      defaultValue: 'Source-row estimate, not compressed archive size',
                    })}
                  </small>
                </div>
                <div>
                  <span>
                    {t('usage_maintenance.preview_target_event', {
                      defaultValue: 'Target event ID',
                    })}
                  </span>
                  <strong>{preview.target_event_id.toLocaleString(i18n.language)}</strong>
                  <small>
                    {t('usage_maintenance.preview_target_event_hint', {
                      defaultValue: 'Recalculated when the task is created',
                    })}
                  </small>
                </div>
                <div>
                  <span>
                    {t('usage_maintenance.preview_range', { defaultValue: 'Timestamp range' })}
                  </span>
                  <strong className={styles.rangeValue}>
                    {formatTime(preview.min_timestamp_ms)} → {formatTime(preview.max_timestamp_ms)}
                  </strong>
                  <small>
                    {t('usage_maintenance.preview_range_hint', {
                      defaultValue: 'Based on the current cutoff preview',
                    })}
                  </small>
                </div>
              </div>
            ) : null}

            <div className={styles.previewNotes}>
              <p>
                <strong>
                  {t('usage_maintenance.online_retention_note_title', {
                    defaultValue: 'Online retention',
                  })}
                </strong>
                {t('usage_maintenance.online_retention_note', {
                  defaultValue:
                    'Events from the cutoff onward remain in SQLite for detailed queries and analysis.',
                })}
              </p>
              <p>
                <strong>
                  {t('usage_maintenance.new_writes_note_title', {
                    defaultValue: 'New writes',
                  })}
                </strong>
                {t('usage_maintenance.new_writes_note', {
                  defaultValue:
                    'Collection continues during archiving. Events newer than the fixed task target are unaffected.',
                })}
              </p>
            </div>
          </section>

          <section className={styles.card}>
            <div className={styles.sectionHeader}>
              <h2>
                {t('usage_maintenance.execution_readiness', {
                  defaultValue: 'Execution readiness',
                })}
              </h2>
              <Button size="xs" variant="ghost" onClick={onRefresh} disabled={working}>
                ↻ {t('common.refresh')}
              </Button>
            </div>
            <dl className={styles.readinessList}>
              <div>
                <dt>
                  <strong>
                    {t('usage_maintenance.migration', { defaultValue: 'Accounting migration' })}
                  </strong>
                  <small>
                    {t('usage_maintenance.migration_readiness_hint', {
                      defaultValue: 'Archive metadata and accounting coverage',
                    })}
                  </small>
                </dt>
                <dd
                  className={maintenance.readiness.migration_ready ? styles.ready : styles.pending}
                >
                  ●{' '}
                  {knownStatus('migration_status', maintenance.migration.status, migrationStatuses)}
                </dd>
              </div>
              <div>
                <dt>
                  <strong>
                    {t('usage_maintenance.hourly_aggregate', { defaultValue: 'Hourly aggregate' })}
                  </strong>
                  <small>
                    {t('usage_maintenance.aggregate_readiness_hint', {
                      defaultValue: 'Long-term hourly usage summaries',
                    })}
                  </small>
                </dt>
                <dd
                  className={
                    maintenance.readiness.hourly_aggregate_ready ? styles.ready : styles.pending
                  }
                >
                  ●{' '}
                  {knownStatus(
                    'aggregate_status',
                    maintenance.hourly_aggregate.status,
                    aggregateStatuses
                  )}
                </dd>
              </div>
              <div>
                <dt>
                  <strong>
                    {t('usage_maintenance.maintenance_lock', { defaultValue: 'Maintenance lock' })}
                  </strong>
                  <small>
                    {t('usage_maintenance.lock_readiness_hint', {
                      defaultValue: 'Only one archive or deletion operation can run at a time',
                    })}
                  </small>
                </dt>
                <dd className={maintenance.active_lock ? styles.pending : styles.ready}>
                  ●{' '}
                  {maintenance.active_lock
                    ? t('usage_maintenance.lock_busy', { defaultValue: 'Lock held' })
                    : t('usage_maintenance.lock_idle', { defaultValue: 'Idle' })}
                </dd>
              </div>
            </dl>
            {createBlockedByMaintenance || archiveReadinessPending ? (
              <p className={styles.warningNote}>{createDisabledReason}</p>
            ) : null}
          </section>
        </div>
      </div>

      <footer className={styles.footerBar}>
        <Button variant="ghost" onClick={onBack} disabled={working}>
          {t('common.cancel')}
        </Button>
        <Button onClick={onCreate} disabled={!canCreate} title={createDisabledReason}>
          {t('usage_maintenance.create', { defaultValue: 'Archive and verify' })}
        </Button>
      </footer>
    </div>
  );
}
