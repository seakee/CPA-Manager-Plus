import { useTranslation } from 'react-i18next';
import styles from '../MonitoringCenterPage.module.scss';

interface MonitoringDatabaseMaintenanceHintProps {
  performanceDegraded: boolean;
  longRange: boolean;
}

export function MonitoringDatabaseMaintenanceHint({
  performanceDegraded,
  longRange,
}: MonitoringDatabaseMaintenanceHintProps) {
  const { t } = useTranslation();
  if (!performanceDegraded) return null;

  return (
    <div className={styles.databaseMaintenanceHint} role="status" aria-live="polite">
      <strong>{t('monitoring.database_maintenance_hint_title')}</strong>
      <span>{t('monitoring.database_maintenance_hint_body')}</span>
      {longRange ? <span>{t('monitoring.database_maintenance_long_range_hint')}</span> : null}
    </div>
  );
}
