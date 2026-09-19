import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Select } from '@/components/ui/Select';
import { IconCheck, IconClock, IconInfo } from '@/components/ui/icons';
import type {
  UsageArchiveList,
  UsageArchiveRunSummary,
  UsageArchiveStatus,
  UsageMaintenanceStatus,
} from '@/services/api/usageService';
import { formatDateTime, formatFileSize } from '@/utils/format';
import {
  getArchiveRunAction,
  isArchiveRunCancellable,
  resolveProgressPercent,
  type ArchiveHistoryFilter,
  type ArchiveHistorySource,
  type ArchiveRunAction,
  type UsageMaintenanceView,
} from './usageMaintenanceModel';
import type { MaintenanceIntent } from './usageMaintenanceNavigation';
import styles from './UsageMaintenanceArchiveViews.module.scss';

const archiveStatuses = [
  'previewed',
  'archiving',
  'archived',
  'verifying',
  'verified',
  'deleting',
  'completed',
  'failed',
  'cancelled',
] as const;
type Navigate = (view: UsageMaintenanceView) => void;
type Actions = {
  working: boolean;
  onAction: (run: UsageArchiveRunSummary, action: ArchiveRunAction) => void;
  actionDisabled: (run: UsageArchiveRunSummary, action: ArchiveRunAction) => boolean;
  actionTitle: (run: UsageArchiveRunSummary, action: ArchiveRunAction) => string | undefined;
  actionLabel: (run: UsageArchiveRunSummary, action: ArchiveRunAction) => string;
};

export function UsageArchiveRunActions({
  run,
  working,
  onAction,
  actionDisabled,
  actionTitle,
  actionLabel,
  compact = false,
  intent = 'archive',
}: Actions & { run: UsageArchiveRunSummary; compact?: boolean; intent?: MaintenanceIntent }) {
  const { t } = useTranslation();
  const action = getArchiveRunAction(run.status);
  return (
    <>
      {!compact && isArchiveRunCancellable(run) ? (
        <Button
          size="sm"
          variant="ghost"
          disabled={working || actionDisabled(run, 'cancel')}
          title={actionTitle(run, 'cancel')}
          onClick={() => onAction(run, 'cancel')}
        >
          {actionLabel(run, 'cancel')}
        </Button>
      ) : null}
      {action ? (
        <Button
          size="sm"
          variant={
            compact || (run.status === 'verified' && intent === 'archive') ? 'ghost' : 'primary'
          }
          disabled={working || actionDisabled(run, action)}
          title={actionTitle(run, action)}
          onClick={() => onAction(run, action)}
        >
          {run.status === 'verified'
            ? t(
                intent === 'cleanup' && !compact
                  ? 'usage_maintenance.continue_cleanup'
                  : 'usage_maintenance.review_cleanup'
              )
            : actionLabel(run, action)}
        </Button>
      ) : null}
    </>
  );
}

export function UsageMaintenanceOverviewView({
  maintenance,
  archives,
  stale = false,
  onNavigate,
  onOpenRun,
}: {
  maintenance: UsageMaintenanceStatus;
  archives: UsageArchiveRunSummary[];
  stale?: boolean;
  onNavigate: Navigate;
  onOpenRun: (run: UsageArchiveRunSummary) => void;
}) {
  const { t, i18n } = useTranslation();
  const pending = [maintenance.active_run, ...archives].filter(
    (run, index, all): run is UsageArchiveRunSummary =>
      Boolean(
        run &&
        run.status !== 'verified' &&
        getArchiveRunAction(run.status) &&
        all.findIndex((item) => item?.id === run.id) === index
      )
  );
  const active = pending[0];
  return (
    <div className={styles.view}>
      <dl className={styles.summary}>
        <div>
          <dt>
            {t('usage_maintenance.raw_events')}
            <button
              type="button"
              className={styles.infoButton}
              onClick={() => onNavigate('overview')}
              aria-label={t('usage_maintenance.online_range_title')}
              title={t('usage_maintenance.online_range_title')}
            >
              <IconInfo size={14} />
            </button>
          </dt>
          <dd>{stale ? '—' : maintenance.raw_event_count.toLocaleString(i18n.language)}</dd>
          <small>
            {t('usage_maintenance.workspace_archived_subset', {
              count: stale
                ? '—'
                : (maintenance.raw_archived_event_count?.toLocaleString(i18n.language) ?? '—'),
            })}
          </small>
        </div>
        <div>
          <dt>{t('usage_maintenance.deleted_events')}</dt>
          <dd>{stale ? '—' : maintenance.raw_deleted_event_count.toLocaleString(i18n.language)}</dd>
          <small>{t('usage_maintenance.workspace_deleted_hint')}</small>
        </div>
        <div>
          <dt>{t('usage_maintenance.sqlite_total')}</dt>
          <dd>{stale ? '—' : formatFileSize(maintenance.storage.total_bytes)}</dd>
          <small>{t('usage_maintenance.sqlite_total_hint')}</small>
        </div>
        <div>
          <dt>{t('usage_maintenance.reclaimable')}</dt>
          <dd>{stale ? '—' : formatFileSize(maintenance.storage.reclaimable_bytes)}</dd>
          <button
            type="button"
            className={styles.textButton}
            onClick={() => onNavigate('advanced')}
          >
            {t('usage_maintenance.offline_reclaim')}
          </button>
        </div>
      </dl>
      {active ? (
        <section
          className={styles.continuation}
          aria-label={t('usage_maintenance.workspace_continue')}
        >
          <IconClock size={18} />
          <div>
            <strong>{t('usage_maintenance.pending_records', { count: pending.length })}</strong>
            <p>
              {t(`usage_maintenance.run_status_${active.status}`, { defaultValue: active.status })}
              {' · '}
              {formatDateTime(new Date(active.created_at_ms), i18n.language)}
            </p>
          </div>
          <Button variant="secondary" size="sm" onClick={() => onOpenRun(active)}>
            {t('usage_maintenance.workspace_continue_record')}
          </Button>
        </section>
      ) : maintenance.active_lock ? (
        <section className={styles.continuation}>
          <IconClock size={18} />
          <p>{t('usage_maintenance.create_blocked_active')}</p>
          <Button variant="ghost" size="sm" onClick={() => onNavigate('diagnostics')}>
            {t('usage_maintenance.diagnostics_title')}
          </Button>
        </section>
      ) : null}
    </div>
  );
}

type HistoryProps = Actions & {
  archiveList: UsageArchiveList;
  filter: ArchiveHistoryFilter;
  source: ArchiveHistorySource;
  loading: boolean;
  canGoBack: boolean;
  onFilter: (filter: ArchiveHistoryFilter) => void;
  onSource: (source: ArchiveHistorySource) => void;
  onNextPage: () => void;
  onPreviousPage: () => void;
  onOpenRun: (run: UsageArchiveRunSummary) => void;
};

export function UsageArchiveHistoryView({
  archiveList,
  filter,
  source,
  loading,
  canGoBack,
  onFilter,
  onSource,
  onNextPage,
  onPreviousPage,
  onOpenRun,
  ...actions
}: HistoryProps) {
  const { t, i18n } = useTranslation();
  const formatTime = (value?: number) =>
    value ? formatDateTime(new Date(value), i18n.language) : '—';
  const counts = archiveList.status_counts;
  return (
    <section className={styles.history} aria-busy={loading}>
      <div className={styles.sectionHeader}>
        <h2>{t('usage_maintenance.archive_records')}</h2>
        <div className={styles.filters}>
          <Select
            value={filter}
            ariaLabel={t('usage_maintenance.history_filters')}
            onChange={(value) => onFilter(value as ArchiveHistoryFilter)}
            options={(['all', ...archiveStatuses] as const).map((value) => ({
              value,
              label:
                (value === 'all'
                  ? t('usage_maintenance.history_filter_all')
                  : t(`usage_maintenance.run_status_${value}`)) +
                (counts
                  ? ` (${value === 'all' ? Object.values(counts).reduce((sum, count) => sum + count, 0) : (counts[value] ?? 0)})`
                  : ''),
            }))}
          />
          <Select
            value={source}
            ariaLabel={t('usage_maintenance.source_filter')}
            onChange={(value) => onSource(value as ArchiveHistorySource)}
            options={(['all', 'manual', 'retention'] as const).map((value) => ({
              value,
              label: t(
                value === 'all'
                  ? 'usage_maintenance.source_all'
                  : `usage_maintenance.run_mode_${value}`
              ),
            }))}
          />
        </div>
      </div>
      <table className={styles.recordTable}>
        <thead>
          <tr>
            <th>{t('usage_maintenance.created_at')}</th>
            <th>{t('usage_maintenance.technical_mode')}</th>
            <th>{t('usage_maintenance.cutoff')}</th>
            <th>{t('usage_maintenance.workspace_record_count')}</th>
            <th>{t('usage_maintenance.technical_status')}</th>
            <th>
              <span className={styles.srOnly}>{t('usage_maintenance.record_actions')}</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {archiveList.runs.map((run) => (
            <tr key={run.id} data-run-id={run.id}>
              <td data-label={t('usage_maintenance.created_at')}>
                <button className={styles.recordTitle} type="button" onClick={() => onOpenRun(run)}>
                  {formatTime(run.created_at_ms)}
                </button>
              </td>
              <td data-label={t('usage_maintenance.technical_mode')}>
                {t(`usage_maintenance.run_mode_${run.mode}`, { defaultValue: run.mode })}
              </td>
              <td data-label={t('usage_maintenance.cutoff')}>
                {formatTime(run.cutoff_timestamp_ms)}
              </td>
              <td data-label={t('usage_maintenance.workspace_record_count')}>
                <strong className={styles.numeric}>
                  {run.event_count.toLocaleString(i18n.language)}
                </strong>
                <small>
                  {t('usage_maintenance.archived_count')}{' '}
                  {run.archived_event_count.toLocaleString(i18n.language)}
                </small>
                {run.deleted_event_count > 0 ? (
                  <small>
                    {t('usage_maintenance.deleted_events')}{' '}
                    {run.deleted_event_count.toLocaleString(i18n.language)}
                  </small>
                ) : null}
              </td>
              <td data-label={t('usage_maintenance.technical_status')}>
                <span className={styles.pill} data-status={run.status}>
                  {t(`usage_maintenance.run_status_${run.status}`, { defaultValue: run.status })}
                </span>
                {run.status === 'verified' || run.status === 'completed' ? (
                  <small>
                    {t(
                      run.status === 'verified'
                        ? 'usage_maintenance.online_retained'
                        : 'usage_maintenance.archive_retained'
                    )}
                  </small>
                ) : null}
              </td>
              <td className={styles.recordActions}>
                <Button size="sm" variant="ghost" onClick={() => onOpenRun(run)}>
                  {t('usage_maintenance.details')}
                </Button>
                <UsageArchiveRunActions run={run} compact {...actions} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {archiveList.runs.length === 0 ? (
        <p className={styles.empty}>
          {loading ? t('common.loading') : t('usage_maintenance.no_runs')}
        </p>
      ) : null}
      <div className={styles.pagination}>
        <span>{t('usage_maintenance.records_on_page', { count: archiveList.runs.length })}</span>
        <Button
          size="sm"
          variant="secondary"
          disabled={!canGoBack || loading}
          onClick={onPreviousPage}
        >
          {t('common.previous')}
        </Button>
        <Button
          size="sm"
          variant="secondary"
          disabled={!archiveList.next_cursor || loading}
          onClick={onNextPage}
        >
          {t('common.next')}
        </Button>
      </div>
    </section>
  );
}

export function UsageArchiveRunView({
  archive,
  active,
  maintenance,
  intent = 'archive',
  ...actions
}: Actions & {
  archive: UsageArchiveStatus;
  active: boolean;
  maintenance: UsageMaintenanceStatus;
  intent?: MaintenanceIntent;
}) {
  const { t, i18n } = useTranslation();
  const run = archive.run;
  const formatTime = (value?: number) =>
    value ? formatDateTime(new Date(value), i18n.language) : '—';
  const statusLabel = (value: string) =>
    t(`usage_maintenance.run_status_${value}`, { defaultValue: value });
  const deleting =
    run.status === 'deleting' || run.resume_status === 'deleting' || run.status === 'completed';
  const progress = deleting
    ? resolveProgressPercent(run.deleted_event_count, run.event_count)
    : run.status === 'archiving' || run.resume_status === 'archiving'
      ? resolveProgressPercent(run.archived_event_count, run.event_count)
      : null;
  const action = getArchiveRunAction(run.status);
  const disabledReason = action ? actions.actionTitle(run, action) : undefined;
  const steps =
    deleting || intent === 'cleanup' ? ['archive', 'verify', 'delete'] : ['archive', 'verify'];
  const stepOrder = deleting
    ? run.status === 'completed'
      ? 4
      : 3
    : run.status === 'verified'
      ? 3
      : run.status === 'verifying' || run.status === 'archived' || run.resume_status === 'verifying'
        ? 2
        : 1;
  return (
    <div className={styles.view}>
      <section className={styles.runResult} data-status={run.status} aria-live="polite">
        <span className={styles.pill} data-status={run.status}>
          {statusLabel(run.status)}
        </span>
        <h2>
          {run.status === 'verified'
            ? t(
                intent === 'cleanup'
                  ? 'usage_maintenance.workspace_ready_cleanup'
                  : 'usage_maintenance.workspace_archive_done'
              )
            : run.status === 'completed'
              ? t('usage_maintenance.cleanup_complete_title')
              : run.status === 'failed'
                ? t('usage_maintenance.archive_prepare_attention')
                : t('usage_maintenance.workspace_current')}
        </h2>
        <p>{formatTime(run.created_at_ms)}</p>
      </section>
      <ol className={styles.stepper} aria-label={t('usage_maintenance.run_steps_label')}>
        {steps.map((step, index) => (
          <li
            key={step}
            data-complete={stepOrder > index + 1}
            aria-current={
              stepOrder === index + 1 && run.status !== 'failed' && run.status !== 'cancelled'
                ? 'step'
                : undefined
            }
          >
            <span>{stepOrder > index + 1 ? <IconCheck size={14} /> : index + 1}</span>
            {t(`usage_maintenance.guided_step_${step}`)}
          </li>
        ))}
      </ol>
      {progress !== null ? (
        <div className={styles.progress}>
          <div>
            {t(
              deleting
                ? 'usage_maintenance.workspace_delete_progress'
                : 'usage_maintenance.workspace_archive_progress'
            )}
            <strong>{progress.toFixed(1)}%</strong>
          </div>
          <div
            className={styles.progressTrack}
            role="progressbar"
            aria-label={t(
              deleting
                ? 'usage_maintenance.workspace_delete_progress'
                : 'usage_maintenance.workspace_archive_progress'
            )}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={Math.round(progress)}
          >
            <i style={{ width: `${progress}%` }} />
          </div>
        </div>
      ) : null}
      <dl className={styles.runCounts}>
        <div>
          <dt>{t('usage_maintenance.workspace_record_count')}</dt>
          <dd>{run.event_count.toLocaleString(i18n.language)}</dd>
        </div>
        <div>
          <dt>{t('usage_maintenance.archived_count')}</dt>
          <dd>{run.archived_event_count.toLocaleString(i18n.language)}</dd>
        </div>
        <div>
          <dt>{t('usage_maintenance.deleted_events')}</dt>
          <dd>{run.deleted_event_count.toLocaleString(i18n.language)}</dd>
        </div>
      </dl>
      <p className={styles.scope}>
        <strong>{t('usage_maintenance.cutoff')}</strong>
        <br />
        {formatTime(run.cutoff_timestamp_ms)}
      </p>
      {run.status === 'verified' ? (
        <p className={styles.hint}>{t('usage_maintenance.workspace_existing_scope')}</p>
      ) : null}
      {run.status === 'completed' ? (
        <p className={styles.hint}>{t('usage_maintenance.delete_storage_note')}</p>
      ) : null}
      {run.status === 'failed' ? (
        <p className={styles.warning}>{t('usage_maintenance.resume_detail_note')}</p>
      ) : null}
      {disabledReason ? <p className={styles.warning}>{disabledReason}</p> : null}
      {active || actions.working ? (
        <p className={styles.hint}>{t('usage_maintenance.stop_waiting_note')}</p>
      ) : null}
      <details className={styles.technical}>
        <summary>{t('usage_maintenance.workspace_technical')}</summary>
        <dl className={styles.keyValues}>
          <div>
            <dt>{t('usage_maintenance.technical_run_id')}</dt>
            <dd className={styles.mono}>{run.id}</dd>
          </div>
          <div>
            <dt>{t('usage_maintenance.technical_mode')}</dt>
            <dd>{t(`usage_maintenance.run_mode_${run.mode}`, { defaultValue: run.mode })}</dd>
          </div>
          <div>
            <dt>{t('usage_maintenance.technical_resume_status')}</dt>
            <dd>{run.resume_status ? statusLabel(run.resume_status) : '—'}</dd>
          </div>
          <div>
            <dt>{t('usage_maintenance.preview_target_event')}</dt>
            <dd>{run.target_event_id.toLocaleString(i18n.language)}</dd>
          </div>
          <div>
            <dt>{t('usage_maintenance.uncompressed_size')}</dt>
            <dd>{formatFileSize(run.archived_uncompressed_bytes)}</dd>
          </div>
          <div>
            <dt>{t('usage_maintenance.compressed_size')}</dt>
            <dd>{formatFileSize(run.archived_compressed_bytes)}</dd>
          </div>
          <div>
            <dt>{t('usage_maintenance.verified_at')}</dt>
            <dd>{formatTime(run.verified_at_ms)}</dd>
          </div>
          <div>
            <dt>{t('usage_maintenance.updated_at')}</dt>
            <dd>{formatTime(run.updated_at_ms)}</dd>
          </div>
          {maintenance.active_lock ? (
            <div>
              <dt>{t('usage_maintenance.lock_title')}</dt>
              <dd>
                {maintenance.active_lock.operation} · {maintenance.active_lock.run_id}
              </dd>
            </div>
          ) : null}
        </dl>
        <h3>{t('usage_maintenance.segment_summary')}</h3>
        {archive.segments.map((segment) => (
          <details className={styles.segment} key={segment.sequence}>
            <summary>
              #{segment.sequence} · {statusLabel(segment.status)} ·{' '}
              {segment.event_count.toLocaleString(i18n.language)}
            </summary>
            <dl className={styles.keyValues}>
              <div>
                <dt>{t('usage_maintenance.technical_event_ids')}</dt>
                <dd>
                  {segment.first_event_id.toLocaleString(i18n.language)} –{' '}
                  {segment.last_event_id.toLocaleString(i18n.language)}
                </dd>
              </div>
              <div>
                <dt>{t('usage_maintenance.preview_range')}</dt>
                <dd>
                  {formatTime(segment.min_timestamp_ms)} – {formatTime(segment.max_timestamp_ms)}
                </dd>
              </div>
              <div>
                <dt>{t('usage_maintenance.uncompressed_size')}</dt>
                <dd>{formatFileSize(segment.uncompressed_bytes)}</dd>
              </div>
              <div>
                <dt>{t('usage_maintenance.compressed_size')}</dt>
                <dd>{formatFileSize(segment.compressed_bytes)}</dd>
              </div>
              <div>
                <dt>{t('usage_maintenance.verified_at')}</dt>
                <dd>{formatTime(segment.verified_at_ms)}</dd>
              </div>
            </dl>
          </details>
        ))}
        {archive.segments.length === 0 ? (
          <p className={styles.hint}>{t('usage_maintenance.no_segments')}</p>
        ) : null}
      </details>
    </div>
  );
}
