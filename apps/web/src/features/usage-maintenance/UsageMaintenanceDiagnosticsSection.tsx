import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { IconCheck, IconCopy, IconInfo } from '@/components/ui/icons';
import { SegmentedTabs, type SegmentedTabItem } from '@/components/ui/SegmentedTabs';
import { SectionActionFooter } from '@/components/common/SectionActionFooter';
import type { UsageMaintenanceStatus, UsageServiceStatus } from '@/services/api/usageService';
import { formatDateTime, formatFileSize } from '@/utils/format';
import { computeCoveragePercent } from './usageMaintenanceModel';
import styles from './UsageMaintenancePage.module.scss';

type CompactDeploymentType = 'docker-compose' | 'docker-run' | 'binary';

const COMPACT_BINARY_COMMAND = 'cpa-manager-plus compact-usage --db-path /path/to/usage.sqlite';
const COMPACT_DOCKER_COMPOSE_COMMAND =
  'docker compose stop cpa-manager-plus && \\\n  docker compose run --rm --no-deps cpa-manager-plus compact-usage --db-path /data/usage.sqlite && \\\n  docker compose start cpa-manager-plus';
const COMPACT_DOCKER_RUN_COMMAND =
  'docker stop cpa-manager-plus && \\\n  docker run --rm -v cpa-manager-plus-data:/data seakee/cpa-manager-plus:latest compact-usage --db-path /data/usage.sqlite && \\\n  docker start cpa-manager-plus';
const BACKUP_FILES = [
  'usage.sqlite',
  'usage.sqlite-wal',
  'usage.sqlite-shm',
  'data.key',
  'usage-archives/',
] as const;

type Props = {
  maintenance: UsageMaintenanceStatus | null;
  serviceStatus: UsageServiceStatus | null;
  onOpenRun: (runId: string) => void;
  onCopyText: (text: string, message: string) => void;
};

export function UsageMaintenanceDiagnosticsSection({
  maintenance,
  serviceStatus,
  onOpenRun,
  onCopyText,
}: Props) {
  const { t, i18n } = useTranslation();
  const [deploymentType, setDeploymentType] = useState<CompactDeploymentType>('docker-compose');

  const formatTime = (value?: number) =>
    value && value > 0 ? formatDateTime(new Date(value), i18n.language) : '—';

  const storageTotalBytes =
    maintenance?.storage?.total_bytes ?? serviceStatus?.database?.totalBytes ?? 0;
  const storageDbBytes =
    maintenance?.storage?.database_bytes ?? serviceStatus?.database?.databaseBytes ?? 0;
  const storageWalBytes =
    maintenance?.storage?.wal_bytes ?? serviceStatus?.database?.walBytes ?? 0;
  const storageShmBytes =
    maintenance?.storage?.shm_bytes ?? serviceStatus?.database?.shmBytes ?? 0;
  const reclaimableBytes = maintenance?.storage?.reclaimable_bytes ?? 0;
  const reclaimPercent =
    storageTotalBytes > 0 && reclaimableBytes > 0
      ? Math.min(100, Math.round((reclaimableBytes / storageTotalBytes) * 100))
      : 0;

  const migrationComplete =
    maintenance?.migration_coverage?.complete ?? maintenance?.readiness.migration_ready ?? false;
  const aggregateComplete =
    maintenance?.hourly_aggregate_coverage?.complete ??
    maintenance?.readiness.hourly_aggregate_ready ??
    false;
  const coverageReady = migrationComplete && aggregateComplete;

  const migrationPercent = computeCoveragePercent(maintenance?.migration_coverage);
  const aggregatePercent = computeCoveragePercent(maintenance?.hourly_aggregate_coverage);

  const compactTabItems: SegmentedTabItem<CompactDeploymentType>[] = [
    { id: 'docker-compose', label: t('usage_maintenance.advanced_tab_docker_compose') },
    { id: 'docker-run', label: t('usage_maintenance.advanced_tab_docker_run') },
    { id: 'binary', label: t('usage_maintenance.advanced_tab_binary') },
  ];

  const currentCompactCommand =
    deploymentType === 'docker-compose'
      ? COMPACT_DOCKER_COMPOSE_COMMAND
      : deploymentType === 'docker-run'
        ? COMPACT_DOCKER_RUN_COMMAND
        : COMPACT_BINARY_COMMAND;

  return (
    <div className={styles.diagnosticsStack} data-testid="usage-maintenance-diagnostics-tab">
      {maintenance ? (
        <div
          className={`${styles.alertBanner} ${coverageReady ? styles.alertSuccess : styles.alertWarn}`}
          role="status"
        >
          <span className={styles.alertHeading}>
            {coverageReady ? (
              <IconCheck size={16} aria-hidden="true" />
            ) : (
              <IconInfo size={16} aria-hidden="true" />
            )}
            <span>
              {t(
                coverageReady
                  ? 'usage_maintenance.diagnostics_ready_banner'
                  : 'usage_maintenance.diagnostics_pending_banner'
              )}
            </span>
          </span>
        </div>
      ) : null}

      {maintenance && !maintenance.readiness.archive_delete_enabled ? (
        <div className={`${styles.alertBanner} ${styles.alertWarn}`}>
          {t('usage_maintenance.delete_disabled')}
        </div>
      ) : null}

      <section className={styles.panel}>
        <div className={styles.panelHeader}>
          <div className={styles.panelHeaderCopy}>
            <h2 className={styles.panelTitle}>
              {t('usage_maintenance.diagnostics_coverage_title')}
            </h2>
            <p className={styles.panelDescription}>
              {t('usage_maintenance.diagnostics_page_subtitle')}
            </p>
          </div>
        </div>

        <div className={styles.flatRows}>
          <div className={styles.coverageRow}>
            <div className={styles.mobileRunHeader}>
              <div className={styles.flatRowCopy}>
                <span className={styles.flatRowLabel}>{t('usage_maintenance.migration')}</span>
                <span className={styles.flatRowMeta}>
                  {t('usage_maintenance.diagnostics_migration_hint')}
                </span>
              </div>
              <span
                className={`${styles.statusPill} ${
                  maintenance?.readiness.migration_ready
                    ? styles.statusSuccess
                    : styles.statusWarning
                }`}
              >
                <span className={styles.statusDot} aria-hidden="true" />
                {maintenance?.readiness.migration_ready
                  ? t('usage_maintenance.migration_status_completed')
                  : t('usage_maintenance.migration_status_pending')}
              </span>
            </div>
            {migrationPercent !== null ? (
              <div className={styles.progressBlock}>
                <div className={styles.mobileRunHeader}>
                  <span className={styles.flatRowMeta}>
                    {maintenance?.migration_coverage
                      ? `${maintenance.migration_coverage.watermark_event_id.toLocaleString(
                          i18n.language
                        )} / ${maintenance.migration_coverage.target_event_id.toLocaleString(
                          i18n.language
                        )}`
                      : ''}
                  </span>
                  <span className={styles.mono}>{Math.round(migrationPercent)}%</span>
                </div>
                <div
                  className={styles.progressTrack}
                  role="progressbar"
                  aria-label={t('usage_maintenance.migration')}
                  aria-valuemin={0}
                  aria-valuemax={100}
                  aria-valuenow={Math.round(migrationPercent)}
                >
                  <div
                    className={`${styles.progressFill} ${
                      maintenance?.readiness.migration_ready ? styles.progressFillSuccess : ''
                    }`}
                    style={{ width: `${migrationPercent}%` }}
                  />
                </div>
              </div>
            ) : null}
            <span className={styles.flatRowMeta}>
              {t('usage_maintenance.updated_at')}:{' '}
              {formatTime(maintenance?.migration?.updated_at_ms)}
            </span>
          </div>

          <div className={styles.coverageRow}>
            <div className={styles.mobileRunHeader}>
              <div className={styles.flatRowCopy}>
                <span className={styles.flatRowLabel}>
                  {t('usage_maintenance.hourly_aggregate')}
                </span>
                <span className={styles.flatRowMeta}>
                  {t('usage_maintenance.diagnostics_aggregate_hint')}
                </span>
              </div>
              <span
                className={`${styles.statusPill} ${
                  maintenance?.readiness.hourly_aggregate_ready
                    ? styles.statusSuccess
                    : styles.statusWarning
                }`}
              >
                <span className={styles.statusDot} aria-hidden="true" />
                {maintenance?.readiness.hourly_aggregate_ready
                  ? t('usage_maintenance.aggregate_status_ready')
                  : t('usage_maintenance.aggregate_status_pending')}
              </span>
            </div>
            {aggregatePercent !== null ? (
              <div className={styles.progressBlock}>
                <div className={styles.mobileRunHeader}>
                  <span className={styles.flatRowMeta}>
                    {maintenance?.hourly_aggregate_coverage
                      ? `${maintenance.hourly_aggregate_coverage.watermark_event_id.toLocaleString(
                          i18n.language
                        )} / ${maintenance.hourly_aggregate_coverage.target_event_id.toLocaleString(
                          i18n.language
                        )}`
                      : ''}
                  </span>
                  <span className={styles.mono}>{Math.round(aggregatePercent)}%</span>
                </div>
                <div
                  className={styles.progressTrack}
                  role="progressbar"
                  aria-label={t('usage_maintenance.hourly_aggregate')}
                  aria-valuemin={0}
                  aria-valuemax={100}
                  aria-valuenow={Math.round(aggregatePercent)}
                >
                  <div
                    className={`${styles.progressFill} ${
                      maintenance?.readiness.hourly_aggregate_ready
                        ? styles.progressFillSuccess
                        : ''
                    }`}
                    style={{ width: `${aggregatePercent}%` }}
                  />
                </div>
              </div>
            ) : null}
            <span className={styles.flatRowMeta}>
              {t('usage_maintenance.updated_at')}:{' '}
              {formatTime(maintenance?.hourly_aggregate?.updated_at_ms)}
            </span>
          </div>

          <div className={styles.flatRow}>
            <div className={styles.flatRowCopy}>
              <span className={styles.flatRowLabel}>
                {t('usage_maintenance.archive_delete_capability')}
              </span>
              <span className={styles.flatRowMeta}>
                {t('usage_maintenance.archive_delete_capability_hint')}
              </span>
            </div>
            <span
              className={`${styles.statusPill} ${
                maintenance?.readiness.archive_delete_enabled
                  ? styles.statusSuccess
                  : styles.statusNeutral
              }`}
            >
              <span className={styles.statusDot} aria-hidden="true" />
              {maintenance?.readiness.archive_delete_enabled
                ? t('common.enabled')
                : t('common.disabled')}
            </span>
          </div>

          {maintenance ? (
            <div className={styles.flatRow}>
              <div className={styles.flatRowCopy}>
                <span className={styles.flatRowLabel}>
                  {t('usage_maintenance.online_range_title')}
                </span>
                <span className={styles.flatRowMeta}>
                  {t('usage_maintenance.online_range_subtitle')}
                </span>
              </div>
              <span className={styles.mono}>
                {maintenance.raw_min_timestamp_ms && maintenance.raw_max_timestamp_ms
                  ? `${formatTime(maintenance.raw_min_timestamp_ms)} – ${formatTime(
                      maintenance.raw_max_timestamp_ms
                    )}`
                  : t('usage_maintenance.preview_empty_no_data')}
              </span>
            </div>
          ) : null}
        </div>
        <div className={styles.panelFooter}>
          <p className={styles.flatRowMeta}>{t('usage_maintenance.diagnostics_coverage_note')}</p>
        </div>
      </section>

      <section className={styles.panel} data-testid="usage-maintenance-lock-card">
        <div className={styles.panelHeader}>
          <div className={styles.panelHeaderCopy}>
            <h2 className={styles.panelTitle}>{t('usage_maintenance.diagnostics_lock_title')}</h2>
          </div>
          {maintenance?.active_run ? (
            <div className={styles.panelHeaderActions}>
              <Button
                size="xs"
                variant="secondary"
                onClick={() => onOpenRun(maintenance.active_run!.id)}
                data-testid="usage-maintenance-open-active-run-btn"
              >
                {t('usage_maintenance.open_active_run')}
              </Button>
            </div>
          ) : null}
        </div>
        <div className={styles.flatRows}>
          <div className={styles.flatRow}>
            <span className={styles.flatRowLabel}>{t('usage_maintenance.active_task')}</span>
            <span className={styles.flatRowMeta}>
              {maintenance?.active_run
                ? `${t(`usage_maintenance.run_status_${maintenance.active_run.status}`, {
                    defaultValue: maintenance.active_run.status,
                  })} · #${maintenance.active_run.id.slice(0, 8)}`
                : t('usage_maintenance.none')}
            </span>
          </div>
          <div className={styles.flatRow}>
            <span className={styles.flatRowLabel}>{t('usage_maintenance.maintenance_lock')}</span>
            <span className={styles.flatRowMeta}>
              {maintenance?.active_lock
                ? t(`usage_maintenance.operation_${maintenance.active_lock.operation}`, {
                    defaultValue: maintenance.active_lock.operation,
                  })
                : t('usage_maintenance.lock_idle')}
            </span>
          </div>
          {maintenance?.active_lock ? (
            <div className={styles.flatRow}>
              <span className={styles.flatRowLabel}>
                {t('usage_maintenance.diagnostics_lock_updated')}
              </span>
              <span className={styles.mono}>{formatTime(maintenance.active_lock.updated_at_ms)}</span>
            </div>
          ) : null}
        </div>
      </section>

      <section className={styles.panel} data-testid="usage-maintenance-compact-card">
        <div className={styles.panelHeader}>
          <div className={styles.panelHeaderCopy}>
            <h2 className={styles.panelTitle}>{t('usage_maintenance.advanced_compact_title')}</h2>
            <p className={styles.panelDescription}>
              {t('usage_maintenance.advanced_compact_subtitle')}
            </p>
          </div>
          <div className={styles.panelHeaderActions}>
            {reclaimPercent > 0 ? (
              <span className={`${styles.statusPill} ${styles.statusInfo}`}>
                {t('usage_maintenance.advanced_reclaim_potential', {
                  percent: reclaimPercent,
                  defaultValue: `~${reclaimPercent}% reclaimable`,
                })}
              </span>
            ) : null}
            <span className={`${styles.statusPill} ${styles.statusWarning}`}>
              <span className={styles.statusDot} aria-hidden="true" />
              {t('usage_maintenance.compact_offline_badge')}
            </span>
          </div>
        </div>

        <div className={styles.panelBody}>
          {storageTotalBytes > 0 ? (
            <div className={styles.storageBar} aria-hidden="true">
              <div
                className={styles.barDb}
                style={{ width: `${Math.max(2, (storageDbBytes / storageTotalBytes) * 100)}%` }}
              />
              {storageWalBytes > 0 ? (
                <div
                  className={styles.barWal}
                  style={{ width: `${Math.max(1, (storageWalBytes / storageTotalBytes) * 100)}%` }}
                />
              ) : null}
              {storageShmBytes > 0 ? (
                <div
                  className={styles.barShm}
                  style={{ width: `${Math.max(1, (storageShmBytes / storageTotalBytes) * 100)}%` }}
                />
              ) : null}
            </div>
          ) : null}

          <div className={styles.legendRow}>
            <span className={styles.legendItem}>
              <span className={`${styles.legendSwatch} ${styles.swatchDb}`} aria-hidden="true" />
              <span>{t('usage_maintenance.database')}:</span>
              <strong>{formatFileSize(storageDbBytes)}</strong>
            </span>
            <span className={styles.legendItem}>
              <span className={`${styles.legendSwatch} ${styles.swatchWal}`} aria-hidden="true" />
              <span>WAL:</span>
              <strong>{formatFileSize(storageWalBytes)}</strong>
            </span>
            <span className={styles.legendItem}>
              <span className={`${styles.legendSwatch} ${styles.swatchShm}`} aria-hidden="true" />
              <span>SHM:</span>
              <strong>{formatFileSize(storageShmBytes)}</strong>
            </span>
            <span className={styles.legendItem}>
              <span className={`${styles.legendSwatch} ${styles.swatchFree}`} aria-hidden="true" />
              <span>{t('usage_maintenance.compact_reclaimable')}:</span>
              <strong>{formatFileSize(reclaimableBytes)}</strong>
            </span>
          </div>

          {reclaimableBytes > 50 * 1024 * 1024 && storageTotalBytes > 0 ? (
            <div className={`${styles.alertBanner} ${styles.alertInfo}`}>
              {t('usage_maintenance.reclaim_estimated_benefit', {
                total: formatFileSize(storageTotalBytes),
                compacted: formatFileSize(Math.max(0, storageTotalBytes - reclaimableBytes)),
              })}
            </div>
          ) : null}

          <div className={`${styles.alertBanner}`}>
            <IconInfo size={14} aria-hidden="true" />
            <span>{t('usage_maintenance.advanced_sqlite_note')}</span>
          </div>

          <div className={styles.controlRow}>
            <SegmentedTabs<CompactDeploymentType>
              variant="joined"
              responsiveFullWidth={false}
              activeTab={deploymentType}
              onChange={setDeploymentType}
              items={compactTabItems}
              ariaLabel={t('usage_maintenance.advanced_compact_title')}
              idBase="usage-maintenance-compact-tabs"
            />
          </div>


          <div className={styles.flatRowCopy}>
            <span className={styles.flatRowLabel}>
              {t('usage_maintenance.advanced_step_backup_title')}
            </span>
            <span className={styles.flatRowMeta}>
              {t('usage_maintenance.advanced_step_compact_title')}
            </span>
          </div>
          <pre className={styles.commandBox}>{currentCompactCommand}</pre>

          <p className={styles.flatRowMeta}>
            {deploymentType === 'docker-compose'
              ? t('usage_maintenance.advanced_note_docker_compose')
              : deploymentType === 'docker-run'
                ? t('usage_maintenance.advanced_note_docker_run')
                : t('usage_maintenance.advanced_note_binary')}
          </p>

          <p className={styles.flatRowMeta}>{t('usage_maintenance.advanced_command_note')}</p>
        </div>

        <div className={styles.panelFooter}>
          <SectionActionFooter status={BACKUP_FILES.join(' · ')}>
            <Button
              variant="ghost"
              size="sm"
              onClick={() =>
                onCopyText(
                  BACKUP_FILES.join(' '),
                  t('usage_maintenance.advanced_copied_file_list')
                )
              }
            >
              {t('usage_maintenance.advanced_backup_set')}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              onClick={() =>
                onCopyText(
                  currentCompactCommand,
                  t('usage_maintenance.advanced_copy_success', {
                    defaultValue: t('common.copied'),
                  })
                )
              }
            >
              <IconCopy size={14} aria-hidden="true" />
              <span>{t('usage_maintenance.advanced_copy_command')}</span>
            </Button>
          </SectionActionFooter>
        </div>
      </section>

      <section className={styles.panel}>
        <div className={styles.panelHeader}>
          <div className={styles.panelHeaderCopy}>
            <h2 className={styles.panelTitle}>
              {t('usage_maintenance.advanced_retention_title')}
            </h2>
          </div>
        </div>
        <div className={styles.panelBody}>
          <p className={styles.flatRowMeta}>{t('usage_maintenance.advanced_retention_note')}</p>
        </div>
      </section>

      <section className={styles.panel}>
        <div className={styles.panelBody}>
          <details className={styles.detailsCollapse}>
            <summary>{t('usage_maintenance.advanced_capabilities_title')}</summary>
            <div className={styles.detailsCollapseBody}>
              <dl className={styles.drawerKvGrid}>
                <div className={styles.inlineMetric}>
                  <dt className={styles.inlineMetricLabel}>
                    {t('usage_maintenance.advanced_service')}
                  </dt>
                  <dd className={styles.inlineMetricValue}>
                    {t('usage_maintenance.advanced_available')}
                  </dd>
                </div>
                <div className={styles.inlineMetric}>
                  <dt className={styles.inlineMetricLabel}>
                    {t('usage_maintenance.advanced_delete_config')}
                  </dt>
                  <dd className={styles.inlineMetricValue}>
                    {t(
                      maintenance?.readiness.archive_delete_enabled
                        ? 'common.enabled'
                        : 'common.disabled'
                    )}
                  </dd>
                </div>
                <div className={styles.inlineMetric}>
                  <dt className={styles.inlineMetricLabel}>
                    {t('usage_maintenance.advanced_offline_compact')}
                  </dt>
                  <dd className={styles.inlineMetricValue}>
                    {t('usage_maintenance.advanced_stop_required')}
                  </dd>
                </div>
                <div className={styles.inlineMetric}>
                  <dt className={styles.inlineMetricLabel}>
                    {t('usage_maintenance.advanced_maintenance_api')}
                  </dt>
                  <dd className={styles.inlineMetricValue}>
                    {t('usage_maintenance.advanced_manager_authorization')}
                  </dd>
                </div>
                <div className={styles.inlineMetric}>
                  <dt className={styles.inlineMetricLabel}>
                    {t('usage_maintenance.advanced_data_exposure')}
                  </dt>
                  <dd className={styles.inlineMetricValue}>
                    {t('usage_maintenance.advanced_sanitized_only')}
                  </dd>
                </div>
              </dl>
              <p className={styles.flatRowMeta}>{t('usage_maintenance.advanced_auth_note')}</p>
              <strong>{t('usage_maintenance.advanced_unavailable_title')}</strong>
              <ul>
                <li>{t('usage_maintenance.advanced_archive_browse')}</li>
                <li>{t('usage_maintenance.advanced_online_vacuum')}</li>
                <li>{t('usage_maintenance.advanced_failure_detail')}</li>
              </ul>
              {maintenance?.storage ? (
                <>
                  <strong>{t('usage_maintenance.workspace_technical')}</strong>
                  <dl className={styles.drawerKvGrid}>
                    {(['page_size', 'page_count', 'freelist_count'] as const).map((key) => (
                      <div key={key} className={styles.inlineMetric}>
                        <dt className={styles.inlineMetricLabel}>
                          {t(`usage_maintenance.storage_${key}`, { defaultValue: key })}
                        </dt>
                        <dd className={styles.inlineMetricValue}>
                          {maintenance.storage![key].toLocaleString(i18n.language)}
                        </dd>
                      </div>
                    ))}
                  </dl>
                </>
              ) : null}
            </div>
          </details>

          <details className={styles.detailsCollapse}>
            <summary>{t('usage_maintenance.diagnostics_capabilities_title')}</summary>
            <div className={styles.detailsCollapseBody}>
              <dl className={styles.drawerKvGrid}>
                {(['401', '404', '503', '409'] as const).map((code) => (
                  <div key={code} className={styles.inlineMetric}>
                    <dt className={styles.inlineMetricLabel}>{code}</dt>
                    <dd className={styles.flatRowMeta}>
                      {t(`usage_maintenance.diagnostics_${code}`)}
                    </dd>
                  </div>
                ))}
              </dl>
              <strong>{t('usage_maintenance.diagnostics_empty_title')}</strong>
              <p className={styles.flatRowMeta}>
                {t(
                  maintenance?.raw_event_count === 0
                    ? 'usage_maintenance.diagnostics_no_raw'
                    : 'usage_maintenance.diagnostics_raw_present'
                )}
              </p>
              <p className={styles.flatRowMeta}>{t('usage_maintenance.diagnostics_zero_preview')}</p>
            </div>
          </details>

          {maintenance ? (
            <details className={styles.detailsCollapse}>
              <summary>{t('usage_maintenance.workspace_technical')}</summary>
              <div className={styles.detailsCollapseBody}>
                <dl className={styles.drawerKvGrid}>
                  <div className={styles.inlineMetric}>
                    <dt className={styles.inlineMetricLabel}>
                      {t('usage_maintenance.active_task')}
                    </dt>
                    <dd className={styles.inlineMetricValue}>
                      {maintenance.active_run?.id ?? '—'}
                    </dd>
                  </div>
                  {maintenance.active_lock ? (
                    <div className={styles.inlineMetric}>
                      <dt className={styles.inlineMetricLabel}>
                        {t('usage_maintenance.lock_title')}
                      </dt>
                      <dd className={styles.inlineMetricValue}>
                        {maintenance.active_lock.run_id}
                      </dd>
                    </div>
                  ) : null}
                  <div className={styles.inlineMetric}>
                    <dt className={styles.inlineMetricLabel}>
                      {t('usage_maintenance.raw_events')}
                    </dt>
                    <dd className={styles.inlineMetricValue}>
                      {maintenance.raw_event_count.toLocaleString(i18n.language)}
                    </dd>
                  </div>
                  <div className={styles.inlineMetric}>
                    <dt className={styles.inlineMetricLabel}>
                      {t('usage_maintenance.archived_online')}
                    </dt>
                    <dd className={styles.inlineMetricValue}>
                      {maintenance.raw_archived_event_count?.toLocaleString(i18n.language) ?? '—'}
                    </dd>
                  </div>
                  {maintenance.migration
                    ? Object.entries(maintenance.migration).map(([key, value]) => (
                        <div key={`migration-${key}`} className={styles.inlineMetric}>
                          <dt className={styles.inlineMetricLabel}>migration.{key}</dt>
                          <dd className={styles.inlineMetricValue}>{String(value)}</dd>
                        </div>
                      ))
                    : null}
                  {maintenance.hourly_aggregate
                    ? Object.entries(maintenance.hourly_aggregate).map(([key, value]) => (
                        <div key={`agg-${key}`} className={styles.inlineMetric}>
                          <dt className={styles.inlineMetricLabel}>hourly_aggregate.{key}</dt>
                          <dd className={styles.inlineMetricValue}>{String(value)}</dd>
                        </div>
                      ))
                    : null}
                  {maintenance.migration_coverage
                    ? Object.entries(maintenance.migration_coverage).map(([key, value]) => (
                        <div key={`mcov-${key}`} className={styles.inlineMetric}>
                          <dt className={styles.inlineMetricLabel}>migration_coverage.{key}</dt>
                          <dd className={styles.inlineMetricValue}>{String(value)}</dd>
                        </div>
                      ))
                    : null}
                  {maintenance.hourly_aggregate_coverage
                    ? Object.entries(maintenance.hourly_aggregate_coverage).map(([key, value]) => (
                        <div key={`acov-${key}`} className={styles.inlineMetric}>
                          <dt className={styles.inlineMetricLabel}>
                            hourly_aggregate_coverage.{key}
                          </dt>
                          <dd className={styles.inlineMetricValue}>{String(value)}</dd>
                        </div>
                      ))
                    : null}
                </dl>
              </div>
            </details>
          ) : null}
        </div>
      </section>
    </div>
  );
}
