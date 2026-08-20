import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import type { UsageMaintenanceStatus } from '@/services/api/usageService';
import { formatDateTime, formatFileSize } from '@/utils/format';
import { resolveProgressPercent } from './usageMaintenanceModel';
import styles from './UsageMaintenanceCapabilityViews.module.scss';

export const COMPACT_USAGE_COMMAND =
  'cpa-manager-plus compact-usage --db-path /path/to/usage.sqlite';

type SharedProps = {
  maintenance: UsageMaintenanceStatus;
  working: boolean;
  onBack: () => void;
  onRefresh: () => void;
};

type AdvancedProps = SharedProps & {
  onCopyCommand: () => void;
};

type DiagnosticsProps = SharedProps & {
  onOpenActive: () => void;
};

const formatCoveragePercent = (coverage?: UsageMaintenanceStatus['migration_coverage']) => {
  if (!coverage) return null;
  if (coverage.complete) return 100;
  return resolveProgressPercent(coverage.watermark_event_id, coverage.target_event_id);
};

const statusText = (
  value: string,
  t: (key: string, options?: Record<string, unknown>) => string,
  prefix: 'migration_status' | 'aggregate_status'
) => t(`usage_maintenance.${prefix}_${value}`, { defaultValue: value });

const coverageStatus = (
  coverage: UsageMaintenanceStatus['migration_coverage'],
  fallbackStatus: string,
  t: (key: string, options?: Record<string, unknown>) => string,
  prefix: 'migration_status' | 'aggregate_status'
) => {
  const percent = formatCoveragePercent(coverage);
  if (percent !== null) return `${Math.round(percent)}%`;
  return statusText(coverage?.status ?? fallbackStatus, t, prefix);
};

function PageHeader({
  title,
  subtitle,
  onBack,
  onRefresh,
  working,
  children,
}: Pick<SharedProps, 'working' | 'onBack' | 'onRefresh'> & {
  title: string;
  subtitle: string;
  children?: React.ReactNode;
}) {
  const { t } = useTranslation();
  return (
    <header className={styles.pageHeader}>
      <div>
        <h1>{title}</h1>
        <p>{subtitle}</p>
      </div>
      <div className={styles.headerActions}>
        {children}
        <Button variant="secondary" size="sm" onClick={onRefresh} disabled={working}>
          ↻ {t('common.refresh', { defaultValue: 'Refresh' })}
        </Button>
        <Button variant="secondary" size="sm" onClick={onBack}>
          ← {t('common.back', { defaultValue: 'Back' })}
        </Button>
      </div>
    </header>
  );
}

function StatusPill({ tone, children }: { tone: string; children: React.ReactNode }) {
  return (
    <span className={`${styles.pill} ${styles[tone as keyof typeof styles]}`}>{children}</span>
  );
}

function Metric({
  label,
  value,
  hint,
  tone = 'blue',
}: {
  label: string;
  value: string;
  hint: string;
  tone?: string;
}) {
  return (
    <article className={styles.metric}>
      <span className={`${styles.metricIcon} ${styles[tone]}`} aria-hidden="true">
        {tone === 'purple' ? '⌁' : tone === 'cyan' ? '▤' : tone === 'orange' ? '⌛' : '◔'}
      </span>
      <div>
        <span>{label}</span>
        <strong>{value}</strong>
        <small>{hint}</small>
      </div>
    </article>
  );
}

export function UsageMaintenanceAdvancedView({
  maintenance,
  working,
  onBack,
  onRefresh,
  onCopyCommand,
}: AdvancedProps) {
  const { t } = useTranslation();
  const storage = maintenance.storage;
  const capabilityRows = [
    {
      label: t('usage_maintenance.advanced_route', { defaultValue: 'Maintenance route' }),
      value: '204',
      tone: 'success',
    },
    {
      label: t('usage_maintenance.advanced_service', { defaultValue: 'Manager service' }),
      value: t('usage_maintenance.advanced_available', { defaultValue: 'Available' }),
      tone: 'success',
    },
    {
      label: t('usage_maintenance.advanced_delete_config', {
        defaultValue: 'Archive delete config',
      }),
      value: maintenance.readiness.archive_delete_enabled
        ? t('common.enabled', { defaultValue: 'Enabled' })
        : t('common.disabled', { defaultValue: 'Disabled' }),
      tone: maintenance.readiness.archive_delete_enabled ? 'info' : 'neutral',
    },
    {
      label: t('usage_maintenance.advanced_offline_compact', { defaultValue: 'Offline compact' }),
      value: t('usage_maintenance.advanced_stop_required', {
        defaultValue: 'Stop server required',
      }),
      tone: 'warning',
    },
  ];

  return (
    <div className={styles.view}>
      <PageHeader
        title={t('usage_maintenance.advanced_page_title', {
          defaultValue: 'Advanced maintenance / offline compact',
        })}
        subtitle={t('usage_maintenance.advanced_page_subtitle', {
          defaultValue: 'Offline physical compaction, complete backups, and capability boundaries.',
        })}
        working={working}
        onBack={onBack}
        onRefresh={onRefresh}
      >
        <Button variant="secondary" size="sm" onClick={onCopyCommand}>
          ▱ {t('usage_maintenance.advanced_copy_command', { defaultValue: 'Copy command' })}
        </Button>
      </PageHeader>

      <div className={styles.twoColumn}>
        <div className={styles.stack}>
          <section className={styles.card}>
            <div className={styles.sectionHeader}>
              <h2>
                ▥{' '}
                {t('usage_maintenance.advanced_sqlite_title', {
                  defaultValue: 'SQLite physical space',
                })}
              </h2>
              <StatusPill tone="warning">
                {t('usage_maintenance.advanced_reclaimable', {
                  defaultValue: '{{size}} reclaimable',
                  size: formatFileSize(storage.reclaimable_bytes),
                })}
              </StatusPill>
            </div>
            <div className={styles.statGrid}>
              <Metric
                label={t('usage_maintenance.database', { defaultValue: 'Database' })}
                value={formatFileSize(storage.database_bytes)}
                hint=""
              />
              <Metric label="WAL" value={formatFileSize(storage.wal_bytes)} hint="" tone="cyan" />
              <Metric label="SHM" value={formatFileSize(storage.shm_bytes)} hint="" tone="orange" />
              <Metric
                label={t('usage_maintenance.total', { defaultValue: 'Total' })}
                value={formatFileSize(storage.total_bytes)}
                hint=""
              />
            </div>
            <p className={styles.warningNote}>
              ⚠{' '}
              {t('usage_maintenance.advanced_sqlite_note', {
                defaultValue:
                  'Logical deletion does not immediately shrink SQLite files. compact-usage requires every Manager Server to be stopped; online VACUUM is not exposed by this panel.',
              })}
            </p>
          </section>

          <section className={styles.card}>
            <h2>
              ▱{' '}
              {t('usage_maintenance.advanced_compact_title', {
                defaultValue: 'Offline compact-usage',
              })}
            </h2>
            <h3>
              {t('usage_maintenance.advanced_backup_required', {
                defaultValue: 'Back up before running',
              })}
            </h3>
            <div className={styles.chipRow}>
              {[
                'usage.sqlite',
                'usage.sqlite-wal',
                'usage.sqlite-shm',
                'data.key',
                'usage-archives/',
              ].map((item) => (
                <span className={styles.chip} key={item}>
                  {item}
                </span>
              ))}
            </div>
            <h3>{t('usage_maintenance.advanced_command_label', { defaultValue: 'Command' })}</h3>
            <pre className={styles.codeBox}>
              <code>{`# 1. ${t('usage_maintenance.advanced_stop_all', { defaultValue: 'Stop every Manager Server' })}\n# 2. ${t('usage_maintenance.advanced_backup_set', { defaultValue: 'Back up the complete set above' })}\n./${COMPACT_USAGE_COMMAND}`}</code>
            </pre>
            <p className={styles.subtleNote}>
              💡{' '}
              {t('usage_maintenance.advanced_command_note', {
                defaultValue:
                  'The command is copied for an operator to run offline; the browser never executes it.',
              })}
            </p>
          </section>

          <section className={styles.card}>
            <h2>
              ◷{' '}
              {t('usage_maintenance.advanced_retention_title', {
                defaultValue: 'Automatic retention',
              })}
            </h2>
            <p className={styles.bodyText}>
              {t('usage_maintenance.advanced_retention_note', {
                defaultValue:
                  'Automatic retention is controlled by the Manager Server environment or config. The public maintenance API does not expose its complete configuration, next-run time, or a write control, so this page does not invent an editable switch.',
              })}
            </p>
            <div className={styles.unavailableBox}>
              <StatusPill tone="neutral">
                {t('usage_maintenance.advanced_not_exposed', {
                  defaultValue: 'Not exposed by API',
                })}
              </StatusPill>
              <span>
                {t('usage_maintenance.advanced_retention_boundary', {
                  defaultValue: 'Read-only capability boundary',
                })}
              </span>
            </div>
          </section>
        </div>

        <div className={styles.stack}>
          <section className={styles.card}>
            <h2>
              🔧{' '}
              {t('usage_maintenance.advanced_capabilities_title', {
                defaultValue: 'Maintenance capabilities',
              })}
            </h2>
            <div className={styles.capabilityList}>
              {capabilityRows.map((row) => (
                <div className={styles.line} key={row.label}>
                  <span>{row.label}</span>
                  <StatusPill tone={row.tone}>{row.value}</StatusPill>
                </div>
              ))}
            </div>
          </section>

          <section className={styles.card}>
            <h2>
              ◇{' '}
              {t('usage_maintenance.advanced_auth_title', {
                defaultValue: 'Authorization boundary',
              })}
            </h2>
            <dl className={styles.keyValues}>
              <div>
                <dt>
                  {t('usage_maintenance.advanced_maintenance_api', {
                    defaultValue: 'Maintenance API',
                  })}
                </dt>
                <dd>
                  {t('usage_maintenance.advanced_manager_authorization', {
                    defaultValue: 'Manager authorization',
                  })}
                </dd>
              </div>
              <div>
                <dt>
                  {t('usage_maintenance.advanced_data_exposure', { defaultValue: 'Data exposure' })}
                </dt>
                <dd>
                  {t('usage_maintenance.advanced_sanitized_only', {
                    defaultValue: 'Sanitized summaries only',
                  })}
                </dd>
              </div>
            </dl>
            <p className={styles.infoNote}>
              ⓘ{' '}
              {t('usage_maintenance.advanced_auth_note', {
                defaultValue:
                  'The panel does not expose database paths, archive filenames, checksums, raw payloads, or internal failure bodies.',
              })}
            </p>
          </section>

          <section className={styles.card}>
            <h2>
              ▣{' '}
              {t('usage_maintenance.advanced_unavailable_title', {
                defaultValue: 'Capabilities not exposed here',
              })}
            </h2>
            <div className={styles.capabilityList}>
              {[
                t('usage_maintenance.advanced_archive_browse', {
                  defaultValue: 'Archive file browsing / download',
                }),
                t('usage_maintenance.advanced_cancel_task', {
                  defaultValue: 'Archive task cancellation',
                }),
                t('usage_maintenance.advanced_online_vacuum', { defaultValue: 'Online VACUUM' }),
                t('usage_maintenance.advanced_failure_detail', {
                  defaultValue: 'Detailed internal failure text',
                }),
              ].map((label) => (
                <div className={styles.line} key={label}>
                  <span>{label}</span>
                  <StatusPill tone="neutral">
                    {t('usage_maintenance.advanced_not_exposed', { defaultValue: 'Not exposed' })}
                  </StatusPill>
                </div>
              ))}
            </div>
          </section>
        </div>
      </div>
    </div>
  );
}

export function UsageMaintenanceDiagnosticsView({
  maintenance,
  working,
  onBack,
  onRefresh,
  onOpenActive,
}: DiagnosticsProps) {
  const { t, i18n } = useTranslation();
  const migrationPercent = formatCoveragePercent(maintenance.migration_coverage);
  const aggregatePercent = formatCoveragePercent(maintenance.hourly_aggregate_coverage);
  const migrationComplete =
    maintenance.migration_coverage?.complete ?? maintenance.readiness.migration_ready;
  const aggregateComplete =
    maintenance.hourly_aggregate_coverage?.complete ?? maintenance.readiness.hourly_aggregate_ready;
  const coverageReady = migrationComplete && aggregateComplete;
  const coverageRow = (label: string, percent: number | null, status: string, tone: string) => (
    <div className={styles.coverageRow} key={label}>
      <span>{label}</span>
      <div
        className={styles.progressTrack}
        role={percent === null ? undefined : 'progressbar'}
        aria-label={label}
        aria-valuemin={percent === null ? undefined : 0}
        aria-valuemax={percent === null ? undefined : 100}
        aria-valuenow={percent === null ? undefined : Math.round(percent)}
      >
        {percent === null ? (
          <i className={styles.indeterminate} />
        ) : (
          <i className={tone} style={{ width: `${percent}%` }} />
        )}
      </div>
      <strong>{percent === null ? status : `${Math.round(percent)}%`}</strong>
    </div>
  );

  return (
    <div className={styles.view}>
      <PageHeader
        title={t('usage_maintenance.diagnostics_page_title', {
          defaultValue: 'Usage maintenance / diagnostics',
        })}
        subtitle={t('usage_maintenance.diagnostics_page_subtitle', {
          defaultValue:
            'Coverage, readiness, locks, storage, and capability state from the latest server snapshot.',
        })}
        working={working}
        onBack={onBack}
        onRefresh={onRefresh}
      />

      <div
        className={`${styles.banner} ${coverageReady ? styles.bannerSuccess : styles.bannerWarning}`}
        role="status"
      >
        {coverageReady ? '✓ ' : '⚠ '}
        {coverageReady
          ? t('usage_maintenance.diagnostics_ready_banner', {
              defaultValue:
                'Derived coverage matches the latest maintenance snapshot. Destructive actions still recheck exact archive coverage.',
            })
          : t('usage_maintenance.diagnostics_pending_banner', {
              defaultValue:
                'Derived coverage is still catching up. Raw deletion remains gated until the required coverage is complete; refresh after the background work advances.',
            })}
      </div>

      <div className={styles.metricGrid}>
        <Metric
          label={t('usage_maintenance.raw_events', { defaultValue: 'Online raw data' })}
          value={maintenance.raw_event_count.toLocaleString(i18n.language)}
          hint={t('usage_maintenance.diagnostics_raw_hint', {
            defaultValue: 'Still available for detailed queries',
          })}
          tone="purple"
        />
        <Metric
          label={t('usage_maintenance.archived_online', { defaultValue: 'Archived but online' })}
          value={(maintenance.raw_archived_event_count ?? 0).toLocaleString(i18n.language)}
          hint={t('usage_maintenance.archived_online_hint', {
            defaultValue: 'Archive reference remains queryable',
          })}
          tone="cyan"
        />
        <Metric
          label={t('usage_maintenance.migration', { defaultValue: 'Migration' })}
          value={statusText(maintenance.migration.status, t, 'migration_status')}
          hint={t('usage_maintenance.diagnostics_migration_hint', {
            defaultValue: 'Accounting projection readiness',
          })}
          tone="orange"
        />
        <Metric
          label={t('usage_maintenance.hourly_aggregate', { defaultValue: 'Hourly aggregate' })}
          value={coverageStatus(
            maintenance.hourly_aggregate_coverage,
            maintenance.hourly_aggregate.status,
            t,
            'aggregate_status'
          )}
          hint={t('usage_maintenance.diagnostics_aggregate_hint', {
            defaultValue: 'Coverage against the latest event watermark',
          })}
          tone="blue"
        />
      </div>

      <div className={styles.twoColumn}>
        <div className={styles.stack}>
          <section className={styles.card}>
            <h2>
              {t('usage_maintenance.diagnostics_coverage_title', { defaultValue: 'Data coverage' })}{' '}
              ⓘ
            </h2>
            <div className={styles.coverageList}>
              {coverageRow(
                t('usage_maintenance.migration', { defaultValue: 'Accounting migration' }),
                migrationPercent,
                statusText(
                  maintenance.migration_coverage?.status ?? maintenance.migration.status,
                  t,
                  'migration_status'
                ),
                styles.orangeBar
              )}
              {coverageRow(
                t('usage_maintenance.hourly_aggregate', { defaultValue: 'Hourly aggregate' }),
                aggregatePercent,
                statusText(
                  maintenance.hourly_aggregate_coverage?.status ??
                    maintenance.hourly_aggregate.status,
                  t,
                  'aggregate_status'
                ),
                styles.orangeBar
              )}
            </div>
            <p className={styles.infoNote}>
              ⓘ{' '}
              {t('usage_maintenance.diagnostics_coverage_note', {
                defaultValue:
                  'When derived coverage is incomplete, readers should fall back to raw where possible. Missing detail is not zero usage.',
              })}
            </p>
          </section>

          <section className={styles.card}>
            <h2>
              {t('usage_maintenance.diagnostics_storage_title', {
                defaultValue: 'Storage snapshot',
              })}
            </h2>
            <div className={styles.statGrid}>
              <Metric
                label={t('usage_maintenance.database', { defaultValue: 'Database' })}
                value={formatFileSize(maintenance.storage.database_bytes)}
                hint=""
              />
              <Metric
                label="WAL"
                value={formatFileSize(maintenance.storage.wal_bytes)}
                hint=""
                tone="cyan"
              />
              <Metric
                label="SHM"
                value={formatFileSize(maintenance.storage.shm_bytes)}
                hint=""
                tone="orange"
              />
              <Metric
                label={t('usage_maintenance.reclaimable', { defaultValue: 'Reclaimable' })}
                value={formatFileSize(maintenance.storage.reclaimable_bytes)}
                hint={t('usage_maintenance.diagnostics_offline_hint', {
                  defaultValue: 'Offline compact only',
                })}
                tone="blue"
              />
            </div>
          </section>

          <section className={styles.card}>
            <h2>
              {t('usage_maintenance.diagnostics_empty_title', {
                defaultValue: 'Empty-state semantics',
              })}
            </h2>
            <div className={styles.emptyBox}>
              ⓘ{' '}
              {maintenance.raw_event_count === 0
                ? t('usage_maintenance.diagnostics_no_raw', {
                    defaultValue: 'No raw data: there are no events available for archiving yet.',
                  })
                : t('usage_maintenance.diagnostics_raw_present', {
                    defaultValue:
                      'Raw data is present. Coverage and readiness determine which derived actions are currently safe.',
                  })}
            </div>
            <p className={styles.subtleNote}>
              ⓘ{' '}
              {t('usage_maintenance.diagnostics_zero_preview', {
                defaultValue:
                  'A zero archive preview is not an error; it can mean the cutoff has no new eligible events or the range is already protected.',
              })}
            </p>
          </section>
        </div>

        <div className={styles.stack}>
          <section className={styles.card}>
            <h2>
              {t('usage_maintenance.diagnostics_capabilities_title', {
                defaultValue: 'State and capability handling',
              })}
            </h2>
            <div className={styles.capabilityList}>
              <div className={styles.line}>
                <span>401</span>
                <span>
                  {t('usage_maintenance.diagnostics_401', {
                    defaultValue: 'Management authorization is invalid or insufficient.',
                  })}
                </span>
              </div>
              <div className={styles.line}>
                <span>
                  404 /{' '}
                  {t('usage_maintenance.diagnostics_legacy', { defaultValue: 'legacy' })}
                </span>
                <span>
                  {t('usage_maintenance.diagnostics_404', {
                    defaultValue: 'The maintenance API is not supported by this server.',
                  })}
                </span>
              </div>
              <div className={styles.line}>
                <span>503</span>
                <span>
                  {t('usage_maintenance.diagnostics_503', {
                    defaultValue: 'The archive service or directory is not ready.',
                  })}
                </span>
              </div>
              <div className={styles.line}>
                <span>409</span>
                <span>
                  {t('usage_maintenance.diagnostics_409', {
                    defaultValue: 'State, lock, or coverage changed; refresh and re-check.',
                  })}
                </span>
              </div>
            </div>
          </section>

          <section className={styles.card}>
            <h2>
              {t('usage_maintenance.diagnostics_lock_title', {
                defaultValue: 'Active maintenance lock',
              })}
            </h2>
            <dl className={styles.keyValues}>
              <div>
                <dt>{t('usage_maintenance.active_task', { defaultValue: 'Active task' })}</dt>
                <dd>
                  {maintenance.active_run?.id ??
                    t('usage_maintenance.none', { defaultValue: 'None' })}
                </dd>
              </div>
              <div>
                <dt>
                  {t('usage_maintenance.maintenance_lock', { defaultValue: 'Maintenance lock' })}
                </dt>
                <dd>
                  {maintenance.active_lock?.operation ??
                    t('usage_maintenance.lock_idle', { defaultValue: 'Idle' })}
                </dd>
              </div>
              {maintenance.active_lock ? (
                <div>
                  <dt>
                    {t('usage_maintenance.diagnostics_lock_updated', {
                      defaultValue: 'Lock updated',
                    })}
                  </dt>
                  <dd>
                    {formatDateTime(new Date(maintenance.active_lock.updated_at_ms), i18n.language)}
                  </dd>
                </div>
              ) : null}
            </dl>
            {maintenance.active_run ? (
              <Button fullWidth variant="secondary" onClick={onOpenActive}>
                {t('usage_maintenance.open_active_run', { defaultValue: 'Open active task' })}
              </Button>
            ) : null}
          </section>

          <section className={styles.card}>
            <h2>
              {t('usage_maintenance.diagnostics_readiness_title', {
                defaultValue: 'Readiness gates',
              })}
            </h2>
            <div className={styles.capabilityList}>
              <div className={styles.line}>
                <span>{t('usage_maintenance.migration', { defaultValue: 'Migration' })}</span>
                <StatusPill tone={maintenance.readiness.migration_ready ? 'success' : 'warning'}>
                  {maintenance.readiness.migration_ready
                    ? t('usage_maintenance.ready', { defaultValue: 'Ready' })
                    : t('usage_maintenance.pending', { defaultValue: 'Pending' })}
                </StatusPill>
              </div>
              <div className={styles.line}>
                <span>
                  {t('usage_maintenance.hourly_aggregate', { defaultValue: 'Hourly aggregate' })}
                </span>
                <StatusPill
                  tone={maintenance.readiness.hourly_aggregate_ready ? 'success' : 'warning'}
                >
                  {maintenance.readiness.hourly_aggregate_ready
                    ? t('usage_maintenance.ready', { defaultValue: 'Ready' })
                    : t('usage_maintenance.pending', { defaultValue: 'Pending' })}
                </StatusPill>
              </div>
              <div className={styles.line}>
                <span>
                  {t('usage_maintenance.archive_delete_capability', {
                    defaultValue: 'Archive deletion',
                  })}
                </span>
                <StatusPill
                  tone={
                    maintenance.readiness.archive_delete_enabled && coverageReady
                      ? 'info'
                      : 'neutral'
                  }
                >
                  {maintenance.readiness.archive_delete_enabled && coverageReady
                    ? t('common.enabled', { defaultValue: 'Enabled' })
                    : t('common.disabled', { defaultValue: 'Disabled' })}
                </StatusPill>
              </div>
              <div className={styles.line}>
                <span>
                  {t('usage_maintenance.advanced_offline_compact', {
                    defaultValue: 'Offline compact',
                  })}
                </span>
                <StatusPill tone="warning">
                  {t('usage_maintenance.advanced_stop_required', {
                    defaultValue: 'Stop server required',
                  })}
                </StatusPill>
              </div>
            </div>
          </section>
        </div>
      </div>
    </div>
  );
}
