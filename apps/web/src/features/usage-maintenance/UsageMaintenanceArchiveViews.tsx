import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { Button } from '@/components/ui/Button';
import type {
  UsageArchiveList,
  UsageArchiveRunSummary,
  UsageArchiveStatus,
  UsageMaintenanceStatus,
} from '@/services/api/usageService';
import { formatDateTime, formatFileSize } from '@/utils/format';
import {
  getArchiveRunAction,
  getArchiveRunPresentationStage,
  resolveProgressPercent,
  type ArchiveHistoryFilter,
  type ArchiveRunAction,
  type UsageMaintenanceView,
} from './usageMaintenanceModel';
import styles from './UsageMaintenanceArchiveViews.module.scss';

const archiveStatuses = new Set([
  'previewed',
  'archiving',
  'archived',
  'verifying',
  'verified',
  'deleting',
  'completed',
  'failed',
  'cancelled',
]);
const archiveModes = new Set(['manual', 'retention']);
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

const knownValueLabel = (
  t: TFunction,
  prefix: string,
  value: string,
  knownValues: ReadonlySet<string>
) =>
  knownValues.has(value)
    ? t(`usage_maintenance.${prefix}_${value}`, { defaultValue: value })
    : value;

type Navigate = (view: UsageMaintenanceView) => void;

type PageHeaderProps = {
  title: string;
  subtitle: string;
  children?: React.ReactNode;
};

function PageHeader({ title, subtitle, children }: PageHeaderProps) {
  return (
    <header className={styles.pageHeader}>
      <div>
        <h1>{title}</h1>
        <p>{subtitle}</p>
      </div>
      {children ? <div className={styles.headerActions}>{children}</div> : null}
    </header>
  );
}

const coveragePercent = (coverage?: {
  watermark_event_id: number;
  target_event_id: number;
  complete: boolean;
}) => {
  if (!coverage) return null;
  if (coverage.complete) return 100;
  return resolveProgressPercent(coverage.watermark_event_id, coverage.target_event_id);
};

const runProgress = (run: UsageArchiveRunSummary) => {
  if (run.status === 'deleting' || run.resume_status === 'deleting' || run.status === 'completed') {
    return resolveProgressPercent(run.deleted_event_count, run.event_count);
  }
  return resolveProgressPercent(run.archived_event_count, run.event_count);
};

const presentationTone = (run: UsageArchiveRunSummary) => {
  const stage = getArchiveRunPresentationStage(run);
  if (stage === 'attention') return styles.toneDanger;
  if (stage === 'completed' || stage === 'delete_ready') return styles.toneSuccess;
  if (stage === 'verifying') return styles.toneWarning;
  return styles.toneInfo;
};

type OverviewProps = {
  maintenance: UsageMaintenanceStatus;
  archives: UsageArchiveRunSummary[];
  working: boolean;
  onRefresh: () => void;
  onNavigate: Navigate;
  onOpenRun: (run: UsageArchiveRunSummary) => void;
};

export function UsageMaintenanceOverviewView({
  maintenance,
  archives,
  working,
  onRefresh,
  onNavigate,
  onOpenRun,
}: OverviewProps) {
  const { t, i18n } = useTranslation();
  const formatTime = (value?: number) =>
    value ? formatDateTime(new Date(value), i18n.language) : '-';
  const migrationProgress = coveragePercent(maintenance.migration_coverage);
  const aggregateProgress = coveragePercent(maintenance.hourly_aggregate_coverage);
  const recentRuns = archives.slice(0, 3);
  const archiveStatusLabel = (value: string) =>
    knownValueLabel(t, 'run_status', value, archiveStatuses);
  const archiveModeLabel = (value: string) => knownValueLabel(t, 'run_mode', value, archiveModes);

  const readinessRows = [
    {
      label: t('usage_maintenance.migration', { defaultValue: 'Accounting migration' }),
      progress: migrationProgress,
      ready: maintenance.readiness.migration_ready,
      status: knownValueLabel(
        t,
        'migration_status',
        maintenance.migration.status,
        migrationStatuses
      ),
    },
    {
      label: t('usage_maintenance.hourly_aggregate', { defaultValue: 'Hourly aggregate' }),
      progress: aggregateProgress,
      ready: maintenance.readiness.hourly_aggregate_ready,
      status: knownValueLabel(
        t,
        'aggregate_status',
        maintenance.hourly_aggregate.status,
        aggregateStatuses
      ),
    },
  ];

  return (
    <div className={styles.view}>
      <PageHeader
        title={t('usage_maintenance.overview_title', {
          defaultValue: 'Usage maintenance overview',
        })}
        subtitle={t('usage_maintenance.overview_subtitle', {
          defaultValue: 'Archive, readiness, and SQLite storage health at a glance.',
        })}
      >
        <Button variant="secondary" size="sm" onClick={onRefresh} disabled={working}>
          {t('common.refresh')}
        </Button>
      </PageHeader>

      <section className={styles.metricGrid} aria-label={t('usage_maintenance.status_title')}>
        <article className={styles.metricCard}>
          <span className={`${styles.metricIcon} ${styles.purple}`} aria-hidden="true">
            ⌁
          </span>
          <div>
            <span>{t('usage_maintenance.raw_events')}</span>
            <strong>{maintenance.raw_event_count.toLocaleString(i18n.language)}</strong>
            <small>
              {maintenance.raw_min_timestamp_ms && maintenance.raw_max_timestamp_ms
                ? `${formatTime(maintenance.raw_min_timestamp_ms)} – ${formatTime(maintenance.raw_max_timestamp_ms)}`
                : t('usage_maintenance.raw_range_unavailable')}
            </small>
          </div>
        </article>
        <article className={styles.metricCard}>
          <span className={`${styles.metricIcon} ${styles.cyan}`} aria-hidden="true">
            ▤
          </span>
          <div>
            <span>
              {t('usage_maintenance.archived_online', { defaultValue: 'Archived but online' })}
            </span>
            <strong>
              {(maintenance.raw_archived_event_count ?? 0).toLocaleString(i18n.language)}
            </strong>
            <small>
              {t('usage_maintenance.archived_online_hint', {
                defaultValue: 'Protected by an archive and still queryable',
              })}
            </small>
          </div>
        </article>
        <article className={styles.metricCard}>
          <span className={`${styles.metricIcon} ${styles.red}`} aria-hidden="true">
            ⌫
          </span>
          <div>
            <span>{t('usage_maintenance.deleted_events')}</span>
            <strong>{maintenance.raw_deleted_event_count.toLocaleString(i18n.language)}</strong>
            <small>
              {t('usage_maintenance.deleted_events_hint', {
                defaultValue: 'Archive references and identity ledger remain',
              })}
            </small>
          </div>
        </article>
        <article className={styles.metricCard}>
          <span className={`${styles.metricIcon} ${styles.blue}`} aria-hidden="true">
            ▥
          </span>
          <div>
            <span>
              {t('usage_maintenance.sqlite_total', { defaultValue: 'SQLite total size' })}
            </span>
            <strong>{formatFileSize(maintenance.storage.total_bytes)}</strong>
            <small>
              {t('usage_maintenance.sqlite_reclaimable_hint', {
                defaultValue: '{{size}} reclaimable after offline compaction',
                size: formatFileSize(maintenance.storage.reclaimable_bytes),
              })}
            </small>
          </div>
        </article>
      </section>

      <div className={styles.overviewGrid}>
        <div className={styles.primaryColumn}>
          <section className={styles.card}>
            <div className={styles.sectionHeader}>
              <h2>
                {t('usage_maintenance.readiness_title', {
                  defaultValue: 'Data availability / Readiness',
                })}
              </h2>
              <span
                className={`${styles.pill} ${maintenance.readiness.migration_ready && maintenance.readiness.hourly_aggregate_ready ? styles.toneSuccess : styles.toneWarning}`}
              >
                {maintenance.readiness.migration_ready &&
                maintenance.readiness.hourly_aggregate_ready
                  ? t('usage_maintenance.readiness_healthy', { defaultValue: 'Healthy' })
                  : t('usage_maintenance.readiness_catching_up', { defaultValue: 'Catching up' })}
              </span>
            </div>
            <div className={styles.readinessGrid}>
              <div className={styles.coverageList}>
                {readinessRows.map((row) => (
                  <div className={styles.coverageRow} key={row.label}>
                    <span>{row.label}</span>
                    <div
                      className={styles.progressTrack}
                      role={row.progress === null ? undefined : 'progressbar'}
                      aria-label={row.label}
                      aria-valuemin={row.progress === null ? undefined : 0}
                      aria-valuemax={row.progress === null ? undefined : 100}
                      aria-valuenow={row.progress === null ? undefined : Math.round(row.progress)}
                    >
                      {row.progress === null ? (
                        <i className={styles.indeterminate} />
                      ) : (
                        <i style={{ width: `${row.progress}%` }} />
                      )}
                    </div>
                    <strong>
                      {row.progress === null ? row.status : `${row.progress.toFixed(0)}%`}
                    </strong>
                  </div>
                ))}
              </div>
              <div className={styles.capabilityList}>
                <div>
                  <span aria-hidden="true">◇</span>
                  <p>
                    <strong>
                      {t('usage_maintenance.core_summary_available', {
                        defaultValue: 'Core summaries available',
                      })}
                    </strong>
                    <small>
                      {t('usage_maintenance.core_summary_hint', {
                        defaultValue: 'Usage statistics and aggregate queries',
                      })}
                    </small>
                  </p>
                  <span
                    className={`${styles.pill} ${maintenance.readiness.migration_ready ? styles.toneSuccess : styles.toneWarning}`}
                  >
                    {maintenance.readiness.migration_ready
                      ? t('usage_maintenance.available')
                      : t('common.loading')}
                  </span>
                </div>
                <div>
                  <span aria-hidden="true">◇</span>
                  <p>
                    <strong>
                      {t('usage_maintenance.raw_details_available', {
                        defaultValue: 'Raw event details available',
                      })}
                    </strong>
                    <small>
                      {t('usage_maintenance.raw_details_hint', {
                        defaultValue: 'Detailed monitoring and search depend on online raw data',
                      })}
                    </small>
                  </p>
                  <span
                    className={`${styles.pill} ${maintenance.raw_event_count > 0 ? styles.toneSuccess : styles.toneNeutral}`}
                  >
                    {maintenance.raw_event_count > 0
                      ? t('usage_maintenance.available')
                      : t('usage_maintenance.empty')}
                  </span>
                </div>
                <div>
                  <span aria-hidden="true">◇</span>
                  <p>
                    <strong>
                      {t('usage_maintenance.archive_delete_capability', {
                        defaultValue: 'Archive deletion',
                      })}
                    </strong>
                    <small>
                      {t('usage_maintenance.archive_delete_capability_hint', {
                        defaultValue: 'The server rechecks exact coverage for every batch',
                      })}
                    </small>
                  </p>
                  <span
                    className={`${styles.pill} ${maintenance.readiness.archive_delete_enabled ? styles.toneInfo : styles.toneNeutral}`}
                  >
                    {maintenance.readiness.archive_delete_enabled
                      ? t('common.enabled', { defaultValue: 'Enabled' })
                      : t('common.disabled', { defaultValue: 'Disabled' })}
                  </span>
                </div>
              </div>
            </div>
          </section>

          <section className={styles.card}>
            <div className={styles.sectionHeader}>
              <h2>{t('usage_maintenance.storage_title', { defaultValue: 'Storage footprint' })}</h2>
            </div>
            <div className={styles.storageGrid}>
              <div>
                <span>Database</span>
                <strong>{formatFileSize(maintenance.storage.database_bytes)}</strong>
              </div>
              <div>
                <span>WAL</span>
                <strong>{formatFileSize(maintenance.storage.wal_bytes)}</strong>
              </div>
              <div>
                <span>SHM</span>
                <strong>{formatFileSize(maintenance.storage.shm_bytes)}</strong>
              </div>
              <div>
                <span>{t('usage_maintenance.reclaimable')}</span>
                <strong>{formatFileSize(maintenance.storage.reclaimable_bytes)}</strong>
              </div>
            </div>
            <p className={styles.warningNote}>
              {t('usage_maintenance.storage_offline_note', {
                defaultValue:
                  'Logical deletion does not shrink SQLite files. Stop every Manager Server before running compact-usage.',
              })}
            </p>
          </section>

          <section className={styles.card}>
            <div className={styles.sectionHeader}>
              <h2>
                {t('usage_maintenance.recent_runs_title', { defaultValue: 'Recent archive tasks' })}
              </h2>
              <button
                type="button"
                className={styles.linkButton}
                onClick={() => onNavigate('history')}
              >
                {t('usage_maintenance.view_all', { defaultValue: 'View all' })} →
              </button>
            </div>
            <div className={styles.tableWrap}>
              <table>
                <thead>
                  <tr>
                    <th>{t('usage_maintenance.technical_run_id')}</th>
                    <th>{t('usage_maintenance.technical_mode')}</th>
                    <th>Cutoff</th>
                    <th>{t('usage_maintenance.preview_events')}</th>
                    <th>{t('usage_maintenance.technical_status')}</th>
                    <th>{t('common.action')}</th>
                  </tr>
                </thead>
                <tbody>
                  {recentRuns.map((run) => (
                    <tr key={run.id}>
                      <td className={styles.mono}>{run.id}</td>
                      <td>{archiveModeLabel(run.mode)}</td>
                      <td>{formatTime(run.cutoff_timestamp_ms)}</td>
                      <td>{run.event_count.toLocaleString(i18n.language)}</td>
                      <td>
                        <span className={`${styles.pill} ${presentationTone(run)}`}>
                          {archiveStatusLabel(run.status)}
                        </span>
                      </td>
                      <td>
                        <button
                          type="button"
                          className={styles.linkButton}
                          onClick={() => onOpenRun(run)}
                        >
                          {t('usage_maintenance.details', { defaultValue: 'Details' })}
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {recentRuns.length === 0 ? (
              <p className={styles.empty}>{t('usage_maintenance.no_runs')}</p>
            ) : null}
          </section>
        </div>

        <aside className={styles.secondaryColumn}>
          <section className={styles.card}>
            <div className={styles.sectionHeader}>
              <h2>
                {t('usage_maintenance.current_status_title', {
                  defaultValue: 'Current maintenance status',
                })}
              </h2>
              <span
                className={`${styles.pill} ${maintenance.active_lock ? styles.toneInfo : styles.toneNeutral}`}
              >
                {maintenance.active_lock
                  ? t('usage_maintenance.lock_busy', { defaultValue: 'Lock held' })
                  : t('usage_maintenance.lock_idle', { defaultValue: 'Idle' })}
              </span>
            </div>
            <dl className={styles.keyValues}>
              <div>
                <dt>{t('usage_maintenance.active_task', { defaultValue: 'Active task' })}</dt>
                <dd>{maintenance.active_run?.id ?? '-'}</dd>
              </div>
              <div>
                <dt>
                  {t('usage_maintenance.maintenance_lock', { defaultValue: 'Maintenance lock' })}
                </dt>
                <dd>{maintenance.active_lock?.operation ?? '-'}</dd>
              </div>
            </dl>
            {maintenance.active_run ? (
              <Button
                fullWidth
                variant="secondary"
                onClick={() => onOpenRun(maintenance.active_run!)}
              >
                {t('usage_maintenance.open_active_run', { defaultValue: 'Open active task' })}
              </Button>
            ) : null}
          </section>
          <section className={styles.card}>
            <h2>
              {t('usage_maintenance.operations_title', { defaultValue: 'Maintenance operations' })}
            </h2>
            <div className={styles.operationList}>
              <Button fullWidth onClick={() => onNavigate('create')}>
                {t('usage_maintenance.create', { defaultValue: 'Create archive task' })}
              </Button>
              <Button fullWidth variant="secondary" onClick={() => onNavigate('transfer')}>
                {t('usage_maintenance.transfer_title', { defaultValue: 'Import / export usage' })}
              </Button>
              <Button fullWidth variant="secondary" onClick={() => onNavigate('advanced')}>
                {t('usage_maintenance.advanced_title', { defaultValue: 'Advanced maintenance' })}
              </Button>
              <Button fullWidth variant="ghost" onClick={() => onNavigate('diagnostics')}>
                {t('usage_maintenance.diagnostics_title', { defaultValue: 'Diagnostics' })}
              </Button>
            </div>
          </section>
          <section className={styles.card}>
            <h2>
              {t('usage_maintenance.data_semantics_title', { defaultValue: 'Data semantics' })}
            </h2>
            <ul className={styles.legendList}>
              <li>
                <i className={styles.purple} />
                {t('usage_maintenance.raw_semantics', {
                  defaultValue: 'Online raw data is the authoritative event detail in SQLite.',
                })}
              </li>
              <li>
                <i className={styles.cyan} />
                {t('usage_maintenance.archived_semantics', {
                  defaultValue:
                    'Archived but online data remains queryable until separately deleted.',
                })}
              </li>
              <li>
                <i className={styles.red} />
                {t('usage_maintenance.deleted_semantics', {
                  defaultValue: 'Deleted raw detail is absent; archived references remain.',
                })}
              </li>
            </ul>
          </section>
        </aside>
      </div>
    </div>
  );
}

type HistoryProps = {
  archiveList: UsageArchiveList;
  filter: ArchiveHistoryFilter;
  loading: boolean;
  working: boolean;
  canGoBack: boolean;
  onFilter: (filter: ArchiveHistoryFilter) => void;
  onNextPage: () => void;
  onPreviousPage: () => void;
  onRefresh: () => void;
  onNavigate: Navigate;
  onOpenRun: (run: UsageArchiveRunSummary) => void;
  onAction: (run: UsageArchiveRunSummary, action: ArchiveRunAction) => void;
  actionDisabled: (run: UsageArchiveRunSummary, action: ArchiveRunAction) => boolean;
  actionTitle: (run: UsageArchiveRunSummary, action: ArchiveRunAction) => string | undefined;
  actionLabel: (run: UsageArchiveRunSummary, action: ArchiveRunAction) => string;
};

export function UsageArchiveHistoryView({
  archiveList,
  filter,
  loading,
  working,
  canGoBack,
  onFilter,
  onNextPage,
  onPreviousPage,
  onRefresh,
  onNavigate,
  onOpenRun,
  onAction,
  actionDisabled,
  actionTitle,
  actionLabel,
}: HistoryProps) {
  const { t, i18n } = useTranslation();
  const formatTime = (value?: number) =>
    value ? formatDateTime(new Date(value), i18n.language) : '-';
  const filters: ArchiveHistoryFilter[] = ['all', 'archiving', 'archived', 'verified', 'failed'];
  const counts = archiveList.status_counts ?? {};
  const allCount = Object.values(counts).reduce((total, count) => total + count, 0);
  const archiveStatusLabel = (value: string) =>
    knownValueLabel(t, 'run_status', value, archiveStatuses);
  const archiveModeLabel = (value: string) => knownValueLabel(t, 'run_mode', value, archiveModes);

  return (
    <div className={styles.view}>
      <PageHeader
        title={t('usage_maintenance.history_title', { defaultValue: 'Archive task history' })}
        subtitle={t('usage_maintenance.history_subtitle', {
          defaultValue:
            'Review manual and retention tasks, then continue the exact server-supported stage.',
        })}
      >
        <Button size="sm" onClick={() => onNavigate('create')}>
          {t('usage_maintenance.create', { defaultValue: 'Create archive' })}
        </Button>
        <Button size="sm" variant="secondary" onClick={onRefresh} disabled={loading || working}>
          {t('common.refresh')}
        </Button>
        <Button size="sm" variant="ghost" onClick={() => onNavigate('overview')}>
          {t('common.back', { defaultValue: 'Back' })}
        </Button>
      </PageHeader>
      <section className={styles.card} aria-busy={loading}>
        <div
          className={styles.filterBar}
          role="tablist"
          aria-label={t('usage_maintenance.history_filters', {
            defaultValue: 'Archive status filters',
          })}
        >
          {filters.map((item) => (
            <button
              key={item}
              type="button"
              role="tab"
              aria-selected={filter === item}
              className={filter === item ? styles.filterActive : ''}
              onClick={() => onFilter(item)}
            >
              {t(`usage_maintenance.history_filter_${item}`, { defaultValue: item })}
              <strong>
                {item === 'all'
                  ? allCount || archiveList.total || archiveList.runs.length
                  : (counts[item] ?? 0)}
              </strong>
            </button>
          ))}
        </div>
        <div className={styles.tableWrap}>
          <table>
            <thead>
              <tr>
                <th>Run ID</th>
                <th>{t('usage_maintenance.technical_mode')}</th>
                <th>Cutoff</th>
                <th>{t('usage_maintenance.preview_events')}</th>
                <th>
                  {t('usage_maintenance.progress_title', {
                    defaultValue: 'Archive / delete progress',
                  })}
                </th>
                <th>{t('usage_maintenance.technical_status')}</th>
                <th>{t('usage_maintenance.technical_resume_status')}</th>
                <th>{t('usage_maintenance.updated_at')}</th>
                <th>{t('common.action')}</th>
              </tr>
            </thead>
            <tbody>
              {archiveList.runs.map((run) => {
                const action = getArchiveRunAction(run.status);
                const progress = runProgress(run);
                return (
                  <tr key={run.id}>
                    <td className={styles.mono}>
                      <button
                        type="button"
                        className={styles.linkButton}
                        onClick={() => onOpenRun(run)}
                      >
                        {run.id}
                      </button>
                    </td>
                    <td>{archiveModeLabel(run.mode)}</td>
                    <td>{formatTime(run.cutoff_timestamp_ms)}</td>
                    <td>{run.event_count.toLocaleString(i18n.language)}</td>
                    <td>
                      <span>
                        {run.archived_event_count.toLocaleString(i18n.language)} /{' '}
                        {run.deleted_event_count.toLocaleString(i18n.language)}
                      </span>
                      {progress === null ? null : (
                        <div
                          className={styles.miniProgress}
                          aria-label={t('usage_maintenance.progress_title', {
                            defaultValue: 'Task progress',
                          })}
                          role="progressbar"
                          aria-valuemin={0}
                          aria-valuemax={100}
                          aria-valuenow={Math.round(progress)}
                        >
                          <i style={{ width: `${progress}%` }} />
                        </div>
                      )}
                    </td>
                    <td>
                      <span className={`${styles.pill} ${presentationTone(run)}`}>
                        {archiveStatusLabel(run.status)}
                      </span>
                    </td>
                    <td>{run.resume_status ? archiveStatusLabel(run.resume_status) : '-'}</td>
                    <td>{formatTime(run.updated_at_ms)}</td>
                    <td>
                      <div className={styles.rowActions}>
                        <button
                          type="button"
                          className={styles.linkButton}
                          onClick={() => onOpenRun(run)}
                        >
                          {t('usage_maintenance.details', { defaultValue: 'Details' })}
                        </button>
                        {action ? (
                          <button
                            type="button"
                            className={`${styles.linkButton} ${action === 'delete' || run.status === 'deleting' || run.resume_status === 'deleting' ? styles.dangerLink : ''}`}
                            disabled={working || actionDisabled(run, action)}
                            title={actionTitle(run, action)}
                            onClick={() => onAction(run, action)}
                          >
                            {actionLabel(run, action)}
                          </button>
                        ) : null}
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        {!loading && archiveList.runs.length === 0 ? (
          <p className={styles.empty}>{t('usage_maintenance.no_runs')}</p>
        ) : null}
        <p className={styles.infoNote}>
          {t('usage_maintenance.history_note', {
            defaultValue:
              'Stable archived or verified manual tasks do not block new tasks. Failed tasks expose only a resumable stage; internal error text is not public.',
          })}
        </p>
        <div className={styles.pagination}>
          <Button
            size="xs"
            variant="secondary"
            disabled={!canGoBack || loading}
            onClick={onPreviousPage}
          >
            ‹ {t('common.previous', { defaultValue: 'Previous' })}
          </Button>
          <Button
            size="xs"
            variant="secondary"
            disabled={!archiveList.next_cursor || loading}
            onClick={onNextPage}
          >
            {t('common.next', { defaultValue: 'Next' })} ›
          </Button>
        </div>
      </section>
    </div>
  );
}

type RunViewProps = {
  archive: UsageArchiveStatus;
  active: boolean;
  maintenance: UsageMaintenanceStatus;
  working: boolean;
  onBack: () => void;
  onRefresh: () => void;
  onStopWaiting: () => void;
  onAction: (run: UsageArchiveRunSummary, action: ArchiveRunAction) => void;
  actionDisabled: (run: UsageArchiveRunSummary, action: ArchiveRunAction) => boolean;
  actionTitle: (run: UsageArchiveRunSummary, action: ArchiveRunAction) => string | undefined;
  actionLabel: (run: UsageArchiveRunSummary, action: ArchiveRunAction) => string;
};

export function UsageArchiveRunView({
  archive,
  active,
  maintenance,
  working,
  onBack,
  onRefresh,
  onStopWaiting,
  onAction,
  actionDisabled,
  actionTitle,
  actionLabel,
}: RunViewProps) {
  const { t, i18n } = useTranslation();
  const run = archive.run;
  const action = getArchiveRunAction(run.status);
  const presentation = getArchiveRunPresentationStage(run);
  const progress = runProgress(run);
  const formatTime = (value?: number) =>
    value ? formatDateTime(new Date(value), i18n.language) : '-';
  const archiveStatusLabel = (value: string) =>
    knownValueLabel(t, 'run_status', value, archiveStatuses);
  const archiveModeLabel = (value: string) => knownValueLabel(t, 'run_mode', value, archiveModes);
  const stepOrder =
    presentation === 'archiving'
      ? 1
      : presentation === 'verifying'
        ? 2
        : presentation === 'delete_ready' || presentation === 'deleting'
          ? 3
          : presentation === 'completed'
            ? 4
            : run.resume_status === 'deleting'
              ? 3
              : run.resume_status === 'verifying'
                ? 2
                : 1;

  return (
    <div className={styles.view}>
      <PageHeader
        title={
          active
            ? t('usage_maintenance.active_run_title', { defaultValue: 'Archive task in progress' })
            : t('usage_maintenance.run_detail_title', { defaultValue: 'Archive task details' })
        }
        subtitle={
          active
            ? t('usage_maintenance.active_run_subtitle', {
                defaultValue:
                  'The server continues in the background when this page closes or the browser stops waiting.',
              })
            : t('usage_maintenance.run_detail_subtitle', {
                defaultValue: 'Task state, progress, and sanitized segment summaries.',
              })
        }
      >
        <Button size="sm" variant="secondary" onClick={onRefresh}>
          {t('common.refresh')}
        </Button>
        <Button size="sm" variant="ghost" onClick={onBack}>
          {t('common.back', { defaultValue: 'Back to history' })}
        </Button>
      </PageHeader>
      <div className={styles.detailGrid}>
        <div className={styles.primaryColumn}>
          <section className={styles.card}>
            <div className={styles.runHeader}>
              <div>
                <span>{t('usage_maintenance.technical_run_id')}</span>
                <strong className={styles.mono}>{run.id}</strong>
                <small>
                  {archiveModeLabel(run.mode)} · {formatTime(run.created_at_ms)}
                </small>
              </div>
              <span className={`${styles.pill} ${presentationTone(run)}`}>
                {archiveStatusLabel(run.status)}
              </span>
            </div>
            <div
              className={styles.stepper}
              aria-label={t('usage_maintenance.run_steps_label', {
                defaultValue: 'Archive workflow progress',
              })}
            >
              {(['archive', 'verify', 'delete'] as const).map((step, index) => {
                const number = index + 1;
                const done = stepOrder > number;
                const current = stepOrder === number;
                return (
                  <div
                    key={step}
                    className={done ? styles.stepDone : current ? styles.stepCurrent : ''}
                  >
                    <span>{done ? '✓' : number}</span>
                    {t(`usage_maintenance.guided_step_${step}`)}
                  </div>
                );
              })}
            </div>
            {progress === null ? null : (
              <>
                <div className={styles.progressLabel}>
                  <span>
                    {t('usage_maintenance.progress_title', { defaultValue: 'Task progress' })}
                  </span>
                  <strong>{progress.toFixed(1)}%</strong>
                </div>
                <div
                  className={styles.progressTrack}
                  role="progressbar"
                  aria-valuemin={0}
                  aria-valuemax={100}
                  aria-valuenow={Math.round(progress)}
                >
                  <i style={{ width: `${progress}%` }} />
                </div>
              </>
            )}
            <div className={styles.storageGrid}>
              <div>
                <span>{t('usage_maintenance.preview_events')}</span>
                <strong>{run.event_count.toLocaleString(i18n.language)}</strong>
              </div>
              <div>
                <span>{t('usage_maintenance.archived_count', { defaultValue: 'Archived' })}</span>
                <strong>{run.archived_event_count.toLocaleString(i18n.language)}</strong>
              </div>
              <div>
                <span>
                  {t('usage_maintenance.uncompressed_size', { defaultValue: 'Uncompressed' })}
                </span>
                <strong>{formatFileSize(run.archived_uncompressed_bytes)}</strong>
              </div>
              <div>
                <span>
                  {t('usage_maintenance.compressed_size', { defaultValue: 'Compressed' })}
                </span>
                <strong>{formatFileSize(run.archived_compressed_bytes)}</strong>
              </div>
            </div>
          </section>
          <section className={styles.card}>
            <div className={styles.sectionHeader}>
              <h2>{t('usage_maintenance.segment_summary', { defaultValue: 'Segment summary' })}</h2>
              <span>{archive.segments.length} segments</span>
            </div>
            <div className={styles.tableWrap}>
              <table>
                <thead>
                  <tr>
                    <th>#</th>
                    <th>{t('usage_maintenance.technical_status')}</th>
                    <th>Event ID</th>
                    <th>{t('usage_maintenance.preview_range')}</th>
                    <th>{t('usage_maintenance.preview_events')}</th>
                    <th>
                      {t('usage_maintenance.uncompressed_size', { defaultValue: 'Uncompressed' })}
                    </th>
                    <th>
                      {t('usage_maintenance.compressed_size', { defaultValue: 'Compressed' })}
                    </th>
                    <th>{t('usage_maintenance.verified_at', { defaultValue: 'Verified at' })}</th>
                  </tr>
                </thead>
                <tbody>
                  {archive.segments.map((segment) => (
                    <tr key={`${segment.run_id}-${segment.sequence}`}>
                      <td>{segment.sequence}</td>
                      <td>
                        <span
                          className={`${styles.pill} ${segment.status === 'verified' ? styles.toneSuccess : styles.toneInfo}`}
                        >
                          {segment.status}
                        </span>
                      </td>
                      <td>
                        {segment.first_event_id.toLocaleString(i18n.language)} –{' '}
                        {segment.last_event_id.toLocaleString(i18n.language)}
                      </td>
                      <td>
                        {formatTime(segment.min_timestamp_ms)} –{' '}
                        {formatTime(segment.max_timestamp_ms)}
                      </td>
                      <td>{segment.event_count.toLocaleString(i18n.language)}</td>
                      <td>{formatFileSize(segment.uncompressed_bytes)}</td>
                      <td>{formatFileSize(segment.compressed_bytes)}</td>
                      <td>{formatTime(segment.verified_at_ms)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {archive.segments.length === 0 ? (
              <p className={styles.empty}>
                {t('usage_maintenance.no_segments', {
                  defaultValue: 'No segment summaries are available for this task yet.',
                })}
              </p>
            ) : null}
            <p className={styles.infoNote}>
              {t('usage_maintenance.segment_public_note', {
                defaultValue:
                  'Public segment fields omit archive paths, filenames, checksums, and internal errors.',
              })}
            </p>
          </section>
        </div>
        <aside className={styles.secondaryColumn}>
          <section className={styles.card}>
            <h2>{t('usage_maintenance.run_summary', { defaultValue: 'Task summary' })}</h2>
            <dl className={styles.keyValues}>
              <div>
                <dt>Cutoff</dt>
                <dd>{formatTime(run.cutoff_timestamp_ms)}</dd>
              </div>
              <div>
                <dt>Target event ID</dt>
                <dd>{run.target_event_id.toLocaleString(i18n.language)}</dd>
              </div>
              <div>
                <dt>{t('usage_maintenance.deleted_events')}</dt>
                <dd>
                  {run.deleted_event_count.toLocaleString(i18n.language)} /{' '}
                  {run.event_count.toLocaleString(i18n.language)}
                </dd>
              </div>
              <div>
                <dt>{t('usage_maintenance.verified_at', { defaultValue: 'Verified at' })}</dt>
                <dd>{formatTime(run.verified_at_ms)}</dd>
              </div>
              <div>
                <dt>has_error</dt>
                <dd>{String(run.has_error)}</dd>
              </div>
            </dl>
          </section>
          {active ? (
            <section className={styles.card}>
              <div className={styles.sectionHeader}>
                <h2>{t('usage_maintenance.lock_title', { defaultValue: 'Maintenance lock' })}</h2>
                <span
                  className={`${styles.pill} ${maintenance.active_lock ? styles.toneInfo : styles.toneNeutral}`}
                >
                  {maintenance.active_lock
                    ? t('usage_maintenance.lock_busy', { defaultValue: 'Held' })
                    : t('usage_maintenance.lock_idle', { defaultValue: 'Idle' })}
                </span>
              </div>
              <dl className={styles.keyValues}>
                <div>
                  <dt>Run ID</dt>
                  <dd>{maintenance.active_lock?.run_id ?? '-'}</dd>
                </div>
                <div>
                  <dt>{t('usage_maintenance.technical_status')}</dt>
                  <dd>{maintenance.active_lock?.operation ?? archiveStatusLabel(run.status)}</dd>
                </div>
              </dl>
              <Button fullWidth variant="secondary" onClick={onStopWaiting} disabled={!working}>
                {t('usage_maintenance.archive_prepare_stop', { defaultValue: 'Stop waiting' })}
              </Button>
              <p className={styles.warningNote}>
                {t('usage_maintenance.stop_waiting_note', {
                  defaultValue: 'Stopping the browser wait does not cancel the server task.',
                })}
              </p>
            </section>
          ) : null}
          {action ? (
            <section
              className={`${styles.card} ${action === 'delete' || run.resume_status === 'deleting' ? styles.dangerCard : ''}`}
            >
              <h2>
                {action === 'delete' || run.resume_status === 'deleting'
                  ? t('usage_maintenance.raw_events')
                  : t('usage_maintenance.next_action', { defaultValue: 'Next action' })}
              </h2>
              <p>
                {action === 'delete'
                  ? t('usage_maintenance.delete_detail_note', {
                      defaultValue:
                        'The archive is verified. Raw deletion is independent and requires confirmation.',
                    })
                  : t('usage_maintenance.resume_detail_note', {
                      defaultValue: 'Continue only the resumable stage reported by the server.',
                    })}
              </p>
              <Button
                fullWidth
                variant={
                  action === 'delete' || run.resume_status === 'deleting' ? 'danger' : 'primary'
                }
                disabled={working || actionDisabled(run, action)}
                title={actionTitle(run, action)}
                onClick={() => onAction(run, action)}
              >
                {actionLabel(run, action)}
              </Button>
            </section>
          ) : null}
        </aside>
      </div>
    </div>
  );
}
