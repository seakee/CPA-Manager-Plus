import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import {
  IconCalendar,
  IconCheck,
  IconClock,
  IconCopy,
  IconShieldCheck,
  IconTriangleAlert,
} from '@/components/ui/icons';
import type {
  UsageArchiveRunSummary,
  UsageArchiveStatusSummary,
  UsageMaintenanceStatus,
} from '@/services/api/usageService';
import { formatDateTime, formatFileSize } from '@/utils/format';
import {
  computeArchiveSteps,
  computeRunMilestones,
  isArchiveRunActiveStatus,
  resolveArchiveProgressPresentation,
  type MaintenanceIntent,
} from './usageMaintenanceModel';
import styles from './UsageMaintenancePage.module.scss';

type RunDrawerProps = {
  archive: UsageArchiveStatusSummary;
  maintenance: UsageMaintenanceStatus | null;
  intent?: MaintenanceIntent;
  working?: boolean;
  actionError?: string | null;
  statusToneClass: (status: string) => string;
  renderActions: (run: UsageArchiveRunSummary) => ReactNode;
  onCopyText: (text: string, message: string) => void;
};

export function UsageMaintenanceRunDrawerContent({
  archive,
  maintenance,
  intent = 'archive',
  working = false,
  actionError,
  statusToneClass,
  renderActions,
  onCopyText,
}: RunDrawerProps) {
  const { t, i18n } = useTranslation();
  const { run, segments } = archive;
  const formatTime = (value?: number) =>
    value ? formatDateTime(new Date(value), i18n.language) : '—';
  const statusLabel = (value: string) =>
    t(`usage_maintenance.run_status_${value}`, { defaultValue: value });

  const steps = computeArchiveSteps(run, intent, working);
  const milestones = computeRunMilestones(run);
  const progress = resolveArchiveProgressPresentation(run);
  const active = isArchiveRunActiveStatus(run.status) || working;
  const recentSegments = segments.slice(-20).reverse();

  return (
    <div className={styles.drawerBody} data-testid="usage-maintenance-detail-drawer">
      <div className={styles.mobileRunHeader}>
        <span className={`${styles.statusPill} ${statusToneClass(run.status)}`}>
          <span className={styles.statusDot} aria-hidden="true" />
          {statusLabel(run.status)}
        </span>
        {renderActions(run)}
      </div>

      <ol
        className={styles.stepper}
        aria-label={t('usage_maintenance.run_steps_label', { defaultValue: 'Archive stages' })}
      >
        {steps.map(({ step, complete, current, failed, running }, index) => (
          <li
            key={step}
            className={styles.stepperItem}
            data-complete={complete}
            data-current={current}
            data-failed={failed}
            data-running={running}
            aria-current={current && !failed && run.status !== 'cancelled' ? 'step' : undefined}
            aria-busy={running ? 'true' : undefined}
          >
            <span className={styles.stepperBadge}>
              {complete ? (
                <IconCheck size={13} />
              ) : failed ? (
                <IconTriangleAlert size={13} />
              ) : (
                index + 1
              )}
            </span>
            <span className={styles.stepperLabel}>
              {t(`usage_maintenance.guided_step_${step}`)}
            </span>
          </li>
        ))}
      </ol>

      {run.status === 'failed' || run.has_error || actionError ? (
        <div className={`${styles.alertBanner} ${styles.alertDanger}`} role="alert">
          <div className={styles.alertStack}>
            <strong className={styles.alertHeading}>
              <IconTriangleAlert size={15} aria-hidden="true" />
              <span>
                {t('usage_maintenance.run_failed_alert_title', {
                  defaultValue: 'Task Stopped Due to Error',
                })}
              </span>
            </strong>
            <span>
              {actionError ||
                t('usage_maintenance.run_failed_alert_desc', {
                  defaultValue:
                    'This archive run encountered an error. You can retry or inspect technical details.',
                })}
            </span>
          </div>
        </div>
      ) : null}

      <dl className={styles.runCountsGrid}>
        <div className={styles.inlineMetric}>
          <dt className={styles.inlineMetricLabel}>
            {t('usage_maintenance.workspace_record_count')}
          </dt>
          <dd className={styles.inlineMetricValue}>
            {run.event_count.toLocaleString(i18n.language)}
          </dd>
        </div>
        <div className={styles.inlineMetric}>
          <dt className={styles.inlineMetricLabel}>{t('usage_maintenance.archived_count')}</dt>
          <dd className={styles.inlineMetricValue}>
            {run.archived_event_count.toLocaleString(i18n.language)}
          </dd>
        </div>
        <div className={styles.inlineMetric}>
          <dt className={styles.inlineMetricLabel}>{t('usage_maintenance.deleted_events')}</dt>
          <dd className={styles.inlineMetricValue}>
            {run.deleted_event_count.toLocaleString(i18n.language)}
          </dd>
        </div>
      </dl>

      <div className={styles.cutoffSummaryBar}>
        <span className={styles.flatRowMeta}>{t('usage_maintenance.cutoff')}</span>
        <strong className={styles.mono}>{formatTime(run.cutoff_timestamp_ms)}</strong>
      </div>

      {run.status === 'verified' ? (
        <p className={styles.flatRowMeta}>{t('usage_maintenance.workspace_existing_scope')}</p>
      ) : null}
      {run.status === 'completed' ? (
        <p className={styles.flatRowMeta}>{t('usage_maintenance.delete_storage_note')}</p>
      ) : null}
      {run.status === 'failed' ? (
        <p className={styles.flatRowMeta}>{t('usage_maintenance.resume_detail_note')}</p>
      ) : null}
      {active ? (
        <p className={styles.flatRowMeta}>{t('usage_maintenance.stop_waiting_note')}</p>
      ) : null}

      <details
        className={styles.detailsCollapse}
        open={active || run.status === 'failed' || run.has_error || Boolean(progress)}
      >
        <summary>{t('usage_maintenance.execution_details')}</summary>
        <div className={styles.detailsCollapseBody}>
          <div className={styles.timeline}>
            {milestones.map((item) => (
              <div key={item.key} className={styles.timelineItem} data-status="complete">
                <div className={styles.timelineTrack}>
                  <span className={styles.timelineNode}>
                    <IconCheck size={11} />
                  </span>
                  <div className={styles.timelineLine} />
                </div>
                <div className={styles.timelineContent}>
                  <time className={styles.mono}>{formatTime(item.timestampMS)}</time>
                  <span>{t(`usage_maintenance.milestone_${item.key}`)}</span>
                </div>
              </div>
            ))}
            {progress ? (
              <div
                className={styles.timelineItem}
                data-status={run.status === 'failed' || run.has_error ? 'failed' : 'active'}
              >
                <div className={styles.timelineTrack}>
                  <span className={styles.timelineNode}>
                    {run.status === 'failed' || run.has_error ? (
                      <IconTriangleAlert size={11} />
                    ) : (
                      <IconClock size={11} />
                    )}
                  </span>
                  <div className={styles.timelineLine} />
                </div>
                <div className={styles.timelineContent}>
                  <div className={styles.mobileRunHeader}>
                    <strong className={styles.flatRowLabel}>
                      {run.status === 'failed' || run.has_error
                        ? `${t('usage_maintenance.progress_interrupted_at')}: `
                        : ''}
                      {t(progress.labelKey)}
                    </strong>
                    {progress.percent !== null ? (
                      <span className={styles.mono}>{progress.percent.toFixed(1)}%</span>
                    ) : null}
                  </div>
                  {progress.total > 0 ? (
                    <span className={styles.flatRowMeta}>
                      {progress.current.toLocaleString(i18n.language)} /{' '}
                      {progress.total.toLocaleString(i18n.language)}{' '}
                      {t(`usage_maintenance.progress_unit_${progress.unit ?? 'events'}`)}
                    </span>
                  ) : null}
                  <div
                    className={styles.progressTrack}
                    role="progressbar"
                    aria-label={t(progress.labelKey)}
                    aria-valuemin={0}
                    aria-valuemax={100}
                    aria-valuenow={
                      progress.percent === null ? undefined : Math.round(progress.percent)
                    }
                  >
                    <div
                      className={`${styles.progressFill} ${
                        run.status === 'failed' || run.has_error
                          ? styles.progressFillDanger
                          : progress.percent === null
                            ? styles.progressFillIndeterminate
                            : ''
                      }`}
                      style={progress.percent === null ? undefined : { width: `${progress.percent}%` }}
                    />
                  </div>
                  {progress.updatedAtMS ? (
                    <span className={styles.flatRowMeta}>
                      {t('usage_maintenance.progress_recent')}: {formatTime(progress.updatedAtMS)}
                    </span>
                  ) : null}
                </div>
              </div>
            ) : null}
          </div>

          {recentSegments.length > 0 ? (
            <div className={styles.drawerSection}>
              <div className={styles.drawerSectionHeader}>
                <h4 className={styles.drawerSectionTitle}>
                  {t('usage_maintenance.segment_summary')}
                </h4>
                <span className={styles.flatRowMeta}>
                  {t('usage_maintenance.segment_total', { count: segments.length })}
                </span>
              </div>
              <div className={styles.segmentMiniList}>
                {recentSegments.map((segment) => (
                  <div key={segment.sequence} className={styles.segmentMiniRow}>
                    <span className={styles.mono}>#{segment.sequence}</span>
                    <span className={styles.flatRowMeta}>
                      {segment.event_count.toLocaleString(i18n.language)}{' '}
                      {t('usage_maintenance.progress_unit_events')} ·{' '}
                      {formatFileSize(segment.compressed_bytes)}
                    </span>
                    <span className={`${styles.statusPill} ${statusToneClass(segment.status)}`}>
                      {statusLabel(segment.status)}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          ) : null}
        </div>
      </details>

      <details className={styles.detailsCollapse}>
        <summary>{t('usage_maintenance.workspace_technical')}</summary>
        <div className={styles.detailsCollapseBody}>
          <dl className={styles.drawerKvGrid}>
            <div className={`${styles.inlineMetric} ${styles.inlineMetricFull}`}>
              <dt className={styles.inlineMetricLabel}>
                {t('usage_maintenance.technical_run_id')}
              </dt>
              <dd className={styles.copyValueRow}>
                <code className={styles.mono}>{run.id}</code>
                <button
                  type="button"
                  className={styles.iconActionBtn}
                  title={`${t('common.copy')} ID`}
                  aria-label={`${t('common.copy')} ${run.id}`}
                  onClick={() =>
                    onCopyText(
                      run.id,
                      t('usage_maintenance.run_id_copied', {
                        defaultValue: 'Task ID copied to clipboard',
                      })
                    )
                  }
                >
                  <IconCopy size={13} />
                </button>
              </dd>
            </div>
            <div className={styles.inlineMetric}>
              <dt className={styles.inlineMetricLabel}>{t('usage_maintenance.technical_mode')}</dt>
              <dd className={styles.inlineMetricValue}>
                {t(`usage_maintenance.run_mode_${run.mode}`, { defaultValue: run.mode })}
              </dd>
            </div>
            <div className={styles.inlineMetric}>
              <dt className={styles.inlineMetricLabel}>
                {t('usage_maintenance.technical_resume_status')}
              </dt>
              <dd className={styles.inlineMetricValue}>
                {run.resume_status ? statusLabel(run.resume_status) : '—'}
              </dd>
            </div>
            <div className={styles.inlineMetric}>
              <dt className={styles.inlineMetricLabel}>
                {t('usage_maintenance.preview_target_event')}
              </dt>
              <dd className={styles.inlineMetricValue}>
                {run.target_event_id.toLocaleString(i18n.language)}
              </dd>
            </div>
            <div className={styles.inlineMetric}>
              <dt className={styles.inlineMetricLabel}>
                {t('usage_maintenance.uncompressed_size')}
              </dt>
              <dd className={styles.inlineMetricValue}>
                {formatFileSize(run.archived_uncompressed_bytes)}
              </dd>
            </div>
            <div className={styles.inlineMetric}>
              <dt className={styles.inlineMetricLabel}>
                {t('usage_maintenance.compressed_size')}
              </dt>
              <dd className={styles.inlineMetricValue}>
                {formatFileSize(run.archived_compressed_bytes)}
              </dd>
            </div>
            <div className={styles.inlineMetric}>
              <dt className={styles.inlineMetricLabel}>{t('usage_maintenance.verified_at')}</dt>
              <dd className={styles.inlineMetricValue}>{formatTime(run.verified_at_ms)}</dd>
            </div>
            <div className={styles.inlineMetric}>
              <dt className={styles.inlineMetricLabel}>{t('usage_maintenance.updated_at')}</dt>
              <dd className={styles.inlineMetricValue}>{formatTime(run.updated_at_ms)}</dd>
            </div>
            {maintenance?.active_lock ? (
              <div className={`${styles.inlineMetric} ${styles.inlineMetricFull}`}>
                <dt className={styles.inlineMetricLabel}>{t('usage_maintenance.lock_title')}</dt>
                <dd className={styles.inlineMetricValue}>
                  {t(`usage_maintenance.operation_${maintenance.active_lock.operation}`, {
                    defaultValue: maintenance.active_lock.operation,
                  })}{' '}
                  · {maintenance.active_lock.run_id}
                </dd>
              </div>
            ) : null}
          </dl>

          {recentSegments.length === 0 ? (
            <p className={styles.flatRowMeta}>{t('usage_maintenance.no_segments')}</p>
          ) : (
            recentSegments.map((segment) => (
              <details key={segment.sequence} className={styles.nestedSegmentDetails}>
                <summary>
                  #{segment.sequence} · {statusLabel(segment.status)} ·{' '}
                  {segment.event_count.toLocaleString(i18n.language)}
                </summary>
                <dl className={styles.drawerKvGrid}>
                  <div className={styles.inlineMetric}>
                    <dt className={styles.inlineMetricLabel}>
                      {t('usage_maintenance.technical_event_ids')}
                    </dt>
                    <dd className={styles.inlineMetricValue}>
                      {segment.first_event_id.toLocaleString(i18n.language)} –{' '}
                      {segment.last_event_id.toLocaleString(i18n.language)}
                    </dd>
                  </div>
                  <div className={styles.inlineMetric}>
                    <dt className={styles.inlineMetricLabel}>
                      {t('usage_maintenance.preview_range')}
                    </dt>
                    <dd className={styles.inlineMetricValue}>
                      {formatTime(segment.min_timestamp_ms)} – {formatTime(segment.max_timestamp_ms)}
                    </dd>
                  </div>
                  <div className={styles.inlineMetric}>
                    <dt className={styles.inlineMetricLabel}>
                      {t('usage_maintenance.uncompressed_size')}
                    </dt>
                    <dd className={styles.inlineMetricValue}>
                      {formatFileSize(segment.uncompressed_bytes)}
                    </dd>
                  </div>
                  <div className={styles.inlineMetric}>
                    <dt className={styles.inlineMetricLabel}>
                      {t('usage_maintenance.compressed_size')}
                    </dt>
                    <dd className={styles.inlineMetricValue}>
                      {formatFileSize(segment.compressed_bytes)}
                    </dd>
                  </div>
                  <div className={styles.inlineMetric}>
                    <dt className={styles.inlineMetricLabel}>
                      {t('usage_maintenance.verified_at')}
                    </dt>
                    <dd className={styles.inlineMetricValue}>{formatTime(segment.verified_at_ms)}</dd>
                  </div>
                </dl>
              </details>
            ))
          )}
        </div>
      </details>
    </div>
  );
}

type DeleteConfirmationProps = {
  run: UsageArchiveRunSummary;
  deletionEnabled: boolean;
  acknowledged: boolean;
  onToggleAcknowledged: (value: boolean) => void;
};

export function UsageMaintenanceDeleteConfirmationView({
  run,
  deletionEnabled,
  acknowledged,
  onToggleAcknowledged,
}: DeleteConfirmationProps) {
  const { t, i18n } = useTranslation();
  const [copiedId, setCopiedId] = useState(false);
  const remainingEventCount = Math.max(0, run.event_count - run.deleted_event_count);
  const verified =
    run.status === 'verified' ||
    run.status === 'deleting' ||
    run.status === 'completed' ||
    run.resume_status === 'deleting';

  const handleCopyId = () => {
    if (!navigator.clipboard?.writeText) return;
    void navigator.clipboard.writeText(run.id).then(() => {
      setCopiedId(true);
      window.setTimeout(() => setCopiedId(false), 2000);
    });
  };

  return (
    <div className={styles.drawerBody} data-testid="usage-maintenance-delete-confirmation">
      <p className={styles.flatRowMeta}>{t('usage_maintenance.delete_confirm_lead')}</p>

      {run.status === 'failed' ? (
        <div className={`${styles.alertBanner} ${styles.alertWarn}`} role="alert">
          <IconTriangleAlert size={15} aria-hidden="true" />
          <span>{t('usage_maintenance.delete_confirm_retry_note')}</span>
        </div>
      ) : null}

      <div className={styles.deleteHeroCard}>
        <div className={styles.deleteHeroPrimary}>
          <div className={styles.inlineMetric}>
            <span className={styles.inlineMetricLabel}>
              {t('usage_maintenance.delete_remaining_count')}
            </span>
            <div className={styles.deleteCountRow}>
              <strong className={styles.deleteCountNumber}>
                {remainingEventCount.toLocaleString(i18n.language)}
              </strong>
              <span className={styles.flatRowMeta}>{t('usage_maintenance.events_suffix')}</span>
            </div>
          </div>
          <div className={styles.inlineMetric}>
            <span className={styles.inlineMetricLabel}>
              <IconCalendar size={12} aria-hidden="true" />{' '}
              {t('usage_maintenance.delete_strict_cutoff')}
            </span>
            <strong className={styles.mono}>
              {formatDateTime(new Date(run.cutoff_timestamp_ms), i18n.language)}
            </strong>
          </div>
        </div>

        <div className={styles.deleteHeroMeta}>
          <div className={styles.copyValueRow}>
            <span className={styles.flatRowMeta}>{t('usage_maintenance.technical_run_id')}:</span>
            <code className={styles.mono}>{run.id}</code>
            <button
              type="button"
              className={styles.iconActionBtn}
              onClick={handleCopyId}
              title={copiedId ? t('common.copied') : `${t('common.copy')} ID`}
            >
              {copiedId ? <IconCheck size={12} /> : <IconCopy size={12} />}
            </button>
          </div>
          <div className={styles.rowActions}>
            <span
              className={`${styles.statusPill} ${verified ? styles.statusSuccess : styles.statusWarning}`}
            >
              <IconShieldCheck size={12} aria-hidden="true" />
              {verified
                ? t('usage_maintenance.run_status_verified')
                : t('usage_maintenance.run_status_verifying')}
            </span>
            <span className={styles.flatRowMeta}>
              {t('usage_maintenance.delete_archived_count')}:{' '}
              <strong>{run.event_count.toLocaleString(i18n.language)}</strong>
            </span>
            <span
              className={`${styles.statusPill} ${deletionEnabled ? styles.statusSuccess : styles.statusNeutral}`}
            >
              {t('usage_maintenance.delete_capability_state')}:{' '}
              {deletionEnabled ? t('common.enabled') : t('common.disabled')}
            </span>
          </div>
        </div>
      </div>

      <div className={styles.comparisonGrid}>
        <div className={`${styles.comparisonCard} ${styles.comparisonSuccess}`}>
          <div className={styles.comparisonHeader}>
            <IconShieldCheck size={15} aria-hidden="true" />
            <strong>{t('usage_maintenance.delete_guarantees_title')}</strong>
          </div>
          <ul className={styles.comparisonList}>
            <li>{t('usage_maintenance.delete_raw_limitations_note')}</li>
            <li>{t('usage_maintenance.delete_batch_recheck_note')}</li>
          </ul>
        </div>
        <div className={`${styles.comparisonCard} ${styles.comparisonWarning}`}>
          <div className={styles.comparisonHeader}>
            <IconTriangleAlert size={15} aria-hidden="true" />
            <strong>{t('usage_maintenance.delete_limitations_title')}</strong>
          </div>
          <ul className={styles.comparisonList}>
            <li>{t('usage_maintenance.delete_permanent_constraints_note')}</li>
            <li>{t('usage_maintenance.delete_storage_note')}</li>
          </ul>
        </div>
      </div>

      <SelectionCheckbox
        checked={acknowledged}
        onChange={onToggleAcknowledged}
        className={`${styles.ackCheckboxCard} ${acknowledged ? styles.ackCheckboxCardChecked : ''}`}
        label={
          <span className={styles.ackCheckboxText}>
            <strong>{t('usage_maintenance.delete_confirm_acknowledgement_strong')}</strong>{' '}
            {t('usage_maintenance.delete_confirm_acknowledgement_text')}
          </span>
        }
      />
    </div>
  );
}
