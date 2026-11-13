import { useTranslation } from 'react-i18next';
import type { UsageServiceStatus } from '@/services/api/usageService';
import {
  evaluateCollectorHealth,
  resolveCollectorDeadLetters,
  type CollectorHealthEvaluation,
} from '../model/dashboardHealthEvaluator';
import styles from './CollectorStatusCard.module.scss';

interface CollectorStatusCardProps {
  enabled: boolean;
  serviceBase: string;
  managementKey?: string;
  refreshSignal?: number;
  status: UsageServiceStatus | null;
  loading: boolean;
  error: string;
  healthEvaluation?: CollectorHealthEvaluation;
}

type StatusTone = 'ok' | 'warn' | 'error' | 'muted';

const formatCount = (value: number | undefined) =>
  Number.isFinite(value) ? Number(value).toLocaleString() : '-';

const formatTimestamp = (value: number | undefined, locale: string) => {
  if (!value || !Number.isFinite(value)) return '-';
  return new Date(value).toLocaleTimeString(locale, {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
};

export function CollectorStatusCard({
  enabled,
  status,
  loading,
  error,
  healthEvaluation,
}: CollectorStatusCardProps) {
  const { t, i18n } = useTranslation();
  const collector = status?.collector;
  const evaluation =
    healthEvaluation ??
    evaluateCollectorHealth({
      isPanelLite: false,
      panelHostMode: 'manager_embedded',
      managerServiceAvailable: enabled,
      featureChecking: false,
      featureReason: enabled ? '' : 'monitoring_disabled',
      usageEnabled: enabled,
      currentScope: '__standalone__',
      evidenceScope: '__standalone__',
      status,
      loading,
      error,
    });
  const deadLetters = resolveCollectorDeadLetters(status);

  let queueStatus = '-';
  let statusTone: StatusTone = 'muted';

  if (evaluation.state === 'attention_required') {
    if (
      evaluation.reasons.includes('service_unavailable') ||
      evaluation.reasons.includes('status_fetch_error')
    ) {
      queueStatus = t('dashboard.collector_unavailable');
      statusTone = 'error';
    } else if (evaluation.reasons.includes('collector_phase_error')) {
      queueStatus = t('dashboard.health_status_problem');
      statusTone = 'error';
    } else if (evaluation.reasons.includes('collector_stopped')) {
      queueStatus = t('dashboard.collector_state_stopped');
      statusTone = 'warn';
    } else {
      queueStatus = t('dashboard.health_status_warning');
      statusTone = 'warn';
    }
  } else if (!enabled) {
    queueStatus = t('dashboard.health_status_disabled');
    statusTone = 'muted';
  } else if (evaluation.state === 'confirmed_healthy') {
    queueStatus = t('dashboard.health_status_normal');
    statusTone = 'ok';
  } else if (evaluation.unconfirmedReason === 'collector_starting') {
    queueStatus = t('dashboard.collector_state_starting');
    statusTone = 'muted';
  } else if (loading && !status) {
    queueStatus = '...';
    statusTone = 'muted';
  } else {
    queueStatus = t('dashboard.collector_state_unconfirmed_short');
    statusTone = 'muted';
  }

  const diagnosticParts: string[] = [];
  const collectorLastError = collector?.lastError?.trim() ?? '';
  const fetchError = error?.trim() ?? '';
  const checkpointError = status?.database?.checkpoint?.error?.trim() ?? '';
  const collectorPhase = collector?.collector?.trim().toLowerCase() ?? '';

  if (collectorLastError) {
    diagnosticParts.push(collectorLastError);
  } else if (fetchError) {
    diagnosticParts.push(fetchError);
  }
  if (checkpointError) {
    diagnosticParts.push(
      t('dashboard.collector_checkpoint_error', { error: checkpointError })
    );
  }
  if (deadLetters > 0) {
    diagnosticParts.push(
      t('dashboard.collector_dead_letters_warning', { count: deadLetters })
    );
  }
  if (collectorPhase === 'stopped') {
    diagnosticParts.push(t('dashboard.collector_state_stopped'));
  } else if (collectorPhase === 'error' && !collectorLastError && !fetchError) {
    diagnosticParts.push(t('dashboard.health_status_problem'));
  } else if (
    evaluation.reasons.includes('service_unavailable') &&
    diagnosticParts.length === 0
  ) {
    diagnosticParts.push(t('dashboard.collector_unavailable'));
  }

  const diagnosticText = diagnosticParts.join(' · ');
  const toneClassMap: Record<StatusTone, string> = {
    ok: styles.statusOk,
    warn: styles.statusWarn,
    error: styles.statusError,
    muted: styles.statusMuted,
  };

  const rows = [
    { label: t('dashboard.collector_mode'), value: collector?.mode || collector?.collector || '-' },
    { label: t('dashboard.health_queue_status'), value: queueStatus, isStatus: true },
    { label: t('dashboard.collector_events'), value: formatCount(status?.events) },
    {
      label: t('dashboard.collector_dead_letters'),
      value: status ? formatCount(deadLetters) : '-',
    },
    {
      label: t('dashboard.collector_last_consumed'),
      value: formatTimestamp(collector?.lastConsumedAt, i18n.language),
    },
    {
      label: t('dashboard.collector_last_inserted'),
      value: formatTimestamp(collector?.lastInsertedAt, i18n.language),
    },
    { label: t('dashboard.collector_total_inserted'), value: formatCount(collector?.totalInserted) },
    { label: t('dashboard.collector_total_skipped'), value: formatCount(collector?.totalSkipped) },
  ];

  return (
    <section className={styles.dataCard}>
      <div className={styles.cardHeader}>
        <h3>{t('dashboard.collector_status_title')}</h3>
      </div>
      <div className={styles.statusList}>
        {rows.map((row, i) => (
          <div key={i} className={styles.statusItem}>
            <span className={styles.label}>{row.label}</span>
            <span
              className={`${styles.value} ${
                row.isStatus ? `${styles.statusText} ${toneClassMap[statusTone]}` : ''
              }`}
            >
              {loading && !status && !row.isStatus ? '...' : row.value}
              {row.isStatus && <span className={styles.statusDot} />}
            </span>
          </div>
        ))}
      </div>
      {diagnosticText ? (
        <div
          className={`${styles.errorLine} ${
            statusTone === 'error' ? styles.errorLineDanger : ''
          }`}
        >
          <span>{t('dashboard.collector_last_error')}</span>
          <strong>{diagnosticText}</strong>
        </div>
      ) : null}
    </section>
  );
}
