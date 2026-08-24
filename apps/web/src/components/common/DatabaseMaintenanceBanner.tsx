import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useDatabaseMaintenance } from './useDatabaseMaintenance';
import styles from './DatabaseMaintenanceBanner.module.scss';

const CLEANUP_COMMAND = 'cleanup-derived';

const formatCount = (value: number | undefined, locale: string) =>
  Number.isFinite(value) && Number(value) >= 0 ? Number(value).toLocaleString(locale) : '0';

const normalizeCount = (value: number | undefined) =>
  Number.isFinite(value) && Number(value) >= 0 ? Number(value) : 0;

export function DatabaseMaintenanceBanner() {
  const { t, i18n } = useTranslation();
  const { status } = useDatabaseMaintenance();
  const [detailsOpen, setDetailsOpen] = useState(false);

  if (!status?.databaseMaintenance?.required) return null;

  const maintenance = status.databaseMaintenance;
  const deferredIndexCount = normalizeCount(maintenance.deferredIndexes);
  const offlineJobCount = normalizeCount(maintenance.offlineJobs);
  const deferredIndexes = t('database_maintenance.query_index_count', {
    count: deferredIndexCount,
    formattedCount: formatCount(deferredIndexCount, i18n.language),
  });
  const offlineJobs = t('database_maintenance.offline_job_count', {
    count: offlineJobCount,
    formattedCount: formatCount(offlineJobCount, i18n.language),
  });
  const summary =
    deferredIndexCount > 0 && offlineJobCount > 0
      ? t('database_maintenance.summary_both', { indexes: deferredIndexes, jobs: offlineJobs })
      : deferredIndexCount > 0
        ? t('database_maintenance.summary_indexes', { indexes: deferredIndexes })
        : t('database_maintenance.summary_jobs', { jobs: offlineJobs });

  return (
    <aside className={styles.banner} role="status" aria-live="polite">
      <span className={styles.icon} aria-hidden="true">
        !
      </span>
      <div className={styles.content}>
        <div className={styles.headingRow}>
          <div>
            <h2 className={styles.title}>{t('database_maintenance.title')}</h2>
            <p className={styles.summary}>{summary}</p>
          </div>
          <span className={styles.badge}>{t('database_maintenance.badge')}</span>
        </div>
        <p className={styles.body}>{t('database_maintenance.body')}</p>
        <p className={styles.safeNote}>{t('database_maintenance.data_safe')}</p>

        <details
          className={styles.details}
          open={detailsOpen}
          onToggle={(event) => setDetailsOpen(event.currentTarget.open)}
        >
          <summary>{t('database_maintenance.steps_title')}</summary>
          <p className={styles.stepsIntro}>{t('database_maintenance.steps_intro')}</p>
          <div className={styles.commandGrid}>
            <section className={styles.commandBlock}>
              <h3>{t('database_maintenance.docker_title')}</h3>
              <code>
                <span>docker compose stop cpa-manager-plus</span>
                <span>docker compose run --rm --no-deps \</span>
                <span>
                  &nbsp;&nbsp;cpa-manager-plus cleanup-derived --db-path /data/usage.sqlite
                </span>
                <span>docker compose start cpa-manager-plus</span>
              </code>
            </section>
            <section className={styles.commandBlock}>
              <h3>{t('database_maintenance.native_title')}</h3>
              <code>
                <span>cpa-manager-plus {CLEANUP_COMMAND}</span>
                <span>cpa-manager-plus {CLEANUP_COMMAND} --db-path /path/to/usage.sqlite</span>
              </code>
            </section>
          </div>
          <p className={styles.stepsNote}>{t('database_maintenance.steps_note')}</p>
        </details>
      </div>
    </aside>
  );
}
