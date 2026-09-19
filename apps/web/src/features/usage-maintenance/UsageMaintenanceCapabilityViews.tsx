import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { IconCheck, IconInfo, IconCopy } from '@/components/ui/icons';
import type { UsageMaintenanceStatus } from '@/services/api/usageService';
import { formatDateTime, formatFileSize } from '@/utils/format';
import { resolveProgressPercent } from './usageMaintenanceModel';
import styles from './UsageMaintenanceCapabilityViews.module.scss';

export const COMPACT_USAGE_COMMAND =
  'cpa-manager-plus compact-usage --db-path /path/to/usage.sqlite';

type SharedProps = { maintenance: UsageMaintenanceStatus };

function StatusPill({ ready, children }: { ready: boolean; children: React.ReactNode }) {
  return (
    <span className={`${styles.pill} ${ready ? styles.success : styles.warning}`}>{children}</span>
  );
}

export function UsageMaintenanceAdvancedView({
  maintenance,
  stale = false,
  onCopyCommand,
}: SharedProps & {
  stale?: boolean;
  onCopyCommand: () => void;
}) {
  const { t, i18n } = useTranslation();
  const storage = maintenance.storage;
  const size = (bytes: number) => (stale ? '—' : formatFileSize(bytes));
  return (
    <div className={styles.view}>
      {stale ? (
        <p className={styles.warningNote}>
          {t('usage_maintenance.workspace_cleanup_refresh_failed')}
        </p>
      ) : null}
      <section className={styles.section}>
        <div className={styles.storageTotal}>
          <span>{t('usage_maintenance.sqlite_total')}</span>
          <strong>{size(storage.total_bytes)}</strong>
          <small>{t('usage_maintenance.sqlite_total_hint')}</small>
        </div>
        <dl className={styles.storageGrid}>
          <div>
            <dt>{t('usage_maintenance.database')}</dt>
            <dd>{size(storage.database_bytes)}</dd>
          </div>
          <div>
            <dt>WAL</dt>
            <dd>{size(storage.wal_bytes)}</dd>
          </div>
          <div>
            <dt>SHM</dt>
            <dd>{size(storage.shm_bytes)}</dd>
          </div>
        </dl>
        <div className={styles.reclaimable}>
          <span>{t('usage_maintenance.reclaimable')}</span>
          <strong>{size(storage.reclaimable_bytes)}</strong>
        </div>
        <p className={styles.muted}>{t('usage_maintenance.advanced_sqlite_note')}</p>
      </section>
      <section className={styles.section}>
        <h2>{t('usage_maintenance.advanced_compact_title')}</h2>
        <ol className={styles.instructions}>
          <li>{t('usage_maintenance.advanced_stop_all')}</li>
          <li>
            {t('usage_maintenance.advanced_backup_set')}
            <div className={styles.backupFiles}>
              {[
                'usage.sqlite',
                'usage.sqlite-wal',
                'usage.sqlite-shm',
                'data.key',
                'usage-archives/',
              ].map((item) => (
                <code key={item}>{item}</code>
              ))}
            </div>
          </li>
          <li>{t('usage_maintenance.advanced_command_label')}</li>
        </ol>
        <pre className={styles.codeBox}>
          <code>{COMPACT_USAGE_COMMAND}</code>
        </pre>
        <Button variant="secondary" size="sm" onClick={onCopyCommand}>
          <IconCopy size={15} />
          {t('usage_maintenance.advanced_copy_command')}
        </Button>
        <p className={styles.muted}>{t('usage_maintenance.advanced_command_note')}</p>
      </section>
      <section className={styles.section}>
        <h2>{t('usage_maintenance.advanced_retention_title')}</h2>
        <p>{t('usage_maintenance.advanced_retention_note')}</p>
      </section>
      <details className={styles.technical}>
        <summary>{t('usage_maintenance.advanced_capabilities_title')}</summary>
        <dl className={styles.keyValues}>
          <div>
            <dt>{t('usage_maintenance.advanced_service')}</dt>
            <dd>{t('usage_maintenance.advanced_available')}</dd>
          </div>
          <div>
            <dt>{t('usage_maintenance.advanced_delete_config')}</dt>
            <dd>
              {t(
                maintenance.readiness.archive_delete_enabled ? 'common.enabled' : 'common.disabled'
              )}
            </dd>
          </div>
          <div>
            <dt>{t('usage_maintenance.advanced_offline_compact')}</dt>
            <dd>{t('usage_maintenance.advanced_stop_required')}</dd>
          </div>
          <div>
            <dt>{t('usage_maintenance.advanced_maintenance_api')}</dt>
            <dd>{t('usage_maintenance.advanced_manager_authorization')}</dd>
          </div>
          <div>
            <dt>{t('usage_maintenance.advanced_data_exposure')}</dt>
            <dd>{t('usage_maintenance.advanced_sanitized_only')}</dd>
          </div>
        </dl>
        <p className={styles.muted}>{t('usage_maintenance.advanced_auth_note')}</p>
        <h3>{t('usage_maintenance.advanced_unavailable_title')}</h3>
        <ul>
          {['advanced_archive_browse', 'advanced_online_vacuum', 'advanced_failure_detail'].map(
            (key) => (
              <li key={key}>{t(`usage_maintenance.${key}`)}</li>
            )
          )}
        </ul>
        <h3>{t('usage_maintenance.workspace_technical')}</h3>
        <dl className={styles.keyValues}>
          {(['page_size', 'page_count', 'freelist_count'] as const).map((key) => (
            <div key={key}>
              <dt>{key}</dt>
              <dd>{stale ? '—' : storage[key].toLocaleString(i18n.language)}</dd>
            </div>
          ))}
        </dl>
      </details>
    </div>
  );
}

export function UsageMaintenanceDiagnosticsView({
  maintenance,
  onOpenActive,
}: SharedProps & { onOpenActive: () => void }) {
  const { t, i18n } = useTranslation();
  const formatTime = (time: number) =>
    time > 0 ? formatDateTime(new Date(time), i18n.language) : '—';
  const migrationComplete =
    maintenance.migration_coverage?.complete ?? maintenance.readiness.migration_ready;
  const aggregateComplete =
    maintenance.hourly_aggregate_coverage?.complete ?? maintenance.readiness.hourly_aggregate_ready;
  const coverageReady = migrationComplete && aggregateComplete;
  const coverages = [
    {
      label: t('usage_maintenance.migration'),
      coverage: maintenance.migration_coverage,
      ready: maintenance.readiness.migration_ready,
      status: t(`usage_maintenance.migration_status_${maintenance.migration.status}`, {
        defaultValue: maintenance.migration.status,
      }),
      updated: maintenance.migration.updated_at_ms,
    },
    {
      label: t('usage_maintenance.hourly_aggregate'),
      coverage: maintenance.hourly_aggregate_coverage,
      ready: maintenance.readiness.hourly_aggregate_ready,
      status: t(`usage_maintenance.aggregate_status_${maintenance.hourly_aggregate.status}`, {
        defaultValue: maintenance.hourly_aggregate.status,
      }),
      updated: maintenance.hourly_aggregate.updated_at_ms,
    },
  ];
  return (
    <div className={styles.view}>
      <div
        className={`${styles.banner} ${coverageReady ? styles.success : styles.warning}`}
        role="status"
      >
        {coverageReady ? <IconCheck size={18} /> : <IconInfo size={18} />}
        <p>
          {t(
            coverageReady
              ? 'usage_maintenance.diagnostics_ready_banner'
              : 'usage_maintenance.diagnostics_pending_banner'
          )}
        </p>
      </div>
      {!maintenance.readiness.archive_delete_enabled ? (
        <p className={styles.warningNote}>{t('usage_maintenance.delete_disabled')}</p>
      ) : null}
      <section className={styles.section}>
        <h2>{t('usage_maintenance.diagnostics_coverage_title')}</h2>
        {coverages.map(({ label, coverage, ready, status, updated }) => {
          const percent = coverage
            ? coverage.complete
              ? 100
              : resolveProgressPercent(coverage.watermark_event_id, coverage.target_event_id)
            : null;
          return (
            <div className={styles.coverage} key={label}>
              <div className={styles.line}>
                <strong>{label}</strong>
                <StatusPill ready={ready}>
                  {t(ready ? 'usage_maintenance.ready' : 'usage_maintenance.pending')}
                </StatusPill>
              </div>
              <div className={styles.line}>
                <span>{status}</span>
                {percent !== null ? <span>{Math.round(percent)}%</span> : null}
              </div>
              {percent !== null ? (
                <div
                  className={styles.progressTrack}
                  role="progressbar"
                  aria-label={label}
                  aria-valuemin={0}
                  aria-valuemax={100}
                  aria-valuenow={Math.round(percent)}
                >
                  <i style={{ width: `${percent}%` }} />
                </div>
              ) : null}
              <small>
                {t('usage_maintenance.updated_at')} · {formatTime(updated)}
              </small>
            </div>
          );
        })}
        <p className={styles.muted}>{t('usage_maintenance.diagnostics_coverage_note')}</p>
      </section>
      <section className={styles.section}>
        <h2>{t('usage_maintenance.diagnostics_lock_title')}</h2>
        <dl className={styles.keyValues}>
          <div>
            <dt>{t('usage_maintenance.active_task')}</dt>
            <dd>
              {maintenance.active_run
                ? t(`usage_maintenance.run_status_${maintenance.active_run.status}`, {
                    defaultValue: maintenance.active_run.status,
                  })
                : t('usage_maintenance.none')}
            </dd>
          </div>
          <div>
            <dt>{t('usage_maintenance.maintenance_lock')}</dt>
            <dd>{maintenance.active_lock?.operation ?? t('usage_maintenance.lock_idle')}</dd>
          </div>
          {maintenance.active_lock ? (
            <div>
              <dt>{t('usage_maintenance.diagnostics_lock_updated')}</dt>
              <dd>{formatTime(maintenance.active_lock.updated_at_ms)}</dd>
            </div>
          ) : null}
        </dl>
        {maintenance.active_run ? (
          <Button variant="secondary" size="sm" onClick={onOpenActive}>
            {t('usage_maintenance.open_active_run')}
          </Button>
        ) : null}
      </section>
      <section className={styles.section}>
        <h2>{t('usage_maintenance.diagnostics_readiness_title')}</h2>
        <dl className={styles.keyValues}>
          <div>
            <dt>{t('usage_maintenance.archive_delete_capability')}</dt>
            <dd>
              {t(
                maintenance.readiness.archive_delete_enabled ? 'common.enabled' : 'common.disabled'
              )}
            </dd>
          </div>
          <div>
            <dt>{t('usage_maintenance.advanced_offline_compact')}</dt>
            <dd>{t('usage_maintenance.advanced_stop_required')}</dd>
          </div>
        </dl>
        <p className={styles.muted}>{t('usage_maintenance.delete_batch_recheck_note')}</p>
      </section>
      <details className={styles.technical}>
        <summary>{t('usage_maintenance.workspace_technical')}</summary>
        <dl className={styles.keyValues}>
          <div>
            <dt>{t('usage_maintenance.active_task')}</dt>
            <dd>{maintenance.active_run?.id ?? '—'}</dd>
          </div>
          {maintenance.active_lock ? (
            <div>
              <dt>{t('usage_maintenance.lock_title')}</dt>
              <dd>{maintenance.active_lock.run_id}</dd>
            </div>
          ) : null}
          <div>
            <dt>{t('usage_maintenance.raw_events')}</dt>
            <dd>{maintenance.raw_event_count.toLocaleString(i18n.language)}</dd>
          </div>
          <div>
            <dt>{t('usage_maintenance.archived_online')}</dt>
            <dd>{maintenance.raw_archived_event_count?.toLocaleString(i18n.language) ?? '—'}</dd>
          </div>
          {Object.entries(maintenance.migration).map(([key, value]) => (
            <div key={key}>
              <dt>migration.{key}</dt>
              <dd>{String(value)}</dd>
            </div>
          ))}
          {Object.entries(maintenance.hourly_aggregate).map(([key, value]) => (
            <div key={key}>
              <dt>hourly_aggregate.{key}</dt>
              <dd>{String(value)}</dd>
            </div>
          ))}
          {maintenance.migration_coverage
            ? Object.entries(maintenance.migration_coverage).map(([key, value]) => (
                <div key={key}>
                  <dt>migration_coverage.{key}</dt>
                  <dd>{String(value)}</dd>
                </div>
              ))
            : null}
          {maintenance.hourly_aggregate_coverage
            ? Object.entries(maintenance.hourly_aggregate_coverage).map(([key, value]) => (
                <div key={key}>
                  <dt>hourly_aggregate_coverage.{key}</dt>
                  <dd>{String(value)}</dd>
                </div>
              ))
            : null}
        </dl>
      </details>
      <details className={styles.technical}>
        <summary>{t('usage_maintenance.diagnostics_capabilities_title')}</summary>
        <dl className={styles.keyValues}>
          {['401', '404', '503', '409'].map((code) => (
            <div key={code}>
              <dt>{code}</dt>
              <dd>{t(`usage_maintenance.diagnostics_${code}`)}</dd>
            </div>
          ))}
        </dl>
        <h3>{t('usage_maintenance.diagnostics_empty_title')}</h3>
        <p>
          {t(
            maintenance.raw_event_count === 0
              ? 'usage_maintenance.diagnostics_no_raw'
              : 'usage_maintenance.diagnostics_raw_present'
          )}
        </p>
        <p>{t('usage_maintenance.diagnostics_zero_preview')}</p>
      </details>
    </div>
  );
}
