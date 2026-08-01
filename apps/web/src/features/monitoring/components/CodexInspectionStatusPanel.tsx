import { Link } from 'react-router-dom';
import type { TFunction } from 'i18next';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { IconExternalLink } from '@/components/ui/icons';
import { type CodexInspectionProgressSnapshot } from '@/features/monitoring/codexInspection';
import { CodexInspectionConfigOverview } from '@/features/monitoring/components/CodexInspectionConfigOverview';
import {
  SummaryCard as MonitoringSummaryCard,
  type SummaryCardProps as MonitoringSummaryCardProps,
} from '@/features/monitoring/components/MonitoringShared';
import {
  type ConfigOverviewItem,
  type RunStatus,
  type StatusTone,
  type SummaryCard,
} from '@/features/monitoring/model/codexInspectionPresentation';
import styles from '../CodexInspectionPage.module.scss';

type CodexInspectionStatusPanelProps = {
  statusTone: StatusTone;
  statusLabel: string;
  lastFinishedLabel: string | null;
  pendingActionCount: number;
  summaryCards: SummaryCard[];
  progress: CodexInspectionProgressSnapshot;
  progressLabel: string;
  showProgressBar: boolean;
  runStatus: RunStatus;
  runButtonLabel: string;
  executing: boolean;
  isInspectionInFlight: boolean;
  runDisabled: boolean;
  configOverviewItems: ConfigOverviewItem[];
  configOverviewTitle: string;
  configOverviewEditLabel: string;
  t: TFunction;
  onEditConfig: (field?: string) => void;
  onRunInspection: () => void;
  onPauseInspection: () => void;
  onStopInspection: () => void;
};

export function CodexInspectionStatusPanel({
  statusTone,
  statusLabel,
  lastFinishedLabel,
  pendingActionCount,
  summaryCards,
  progress,
  progressLabel,
  showProgressBar,
  runStatus,
  runButtonLabel,
  executing,
  isInspectionInFlight,
  runDisabled,
  configOverviewItems,
  configOverviewTitle,
  configOverviewEditLabel,
  t,
  onEditConfig,
  onRunInspection,
  onPauseInspection,
  onStopInspection,
}: CodexInspectionStatusPanelProps) {
  return (
    <>
      <Card className={`${styles.panel} ${styles.statusPanel}`}>
        <div className={styles.statusBar}>
          <div className={styles.statusInfo}>
            <span className={`${styles.statusBadge} ${styles[`tone-${statusTone}`]}`}>
              <span className={styles.statusDot} aria-hidden="true" />
              {statusLabel}
            </span>
            <div className={styles.statusMeta}>
              {lastFinishedLabel ? <span>{lastFinishedLabel}</span> : null}
              {pendingActionCount > 0 ? (
                <span
                  className={styles.statusMetaWarn}
                >{`${t('monitoring.codex_inspection_pending_total')} ${pendingActionCount}`}</span>
              ) : null}
            </div>
          </div>

          <div className={styles.statusActions}>
            <Link to="/auth-files" className={styles.quickLink}>
              <IconExternalLink size={14} />
              <span>{t('monitoring.codex_inspection_back')}</span>
            </Link>
            <Button
              variant="primary"
              size="sm"
              onClick={onRunInspection}
              loading={runStatus === 'running'}
              disabled={runDisabled}
            >
              {runButtonLabel}
            </Button>
            {isInspectionInFlight ? (
              <>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={onPauseInspection}
                  disabled={runStatus !== 'running' || executing}
                >
                  {t('monitoring.codex_inspection_pause')}
                </Button>
                <Button variant="danger" size="sm" onClick={onStopInspection} disabled={executing}>
                  {t('monitoring.codex_inspection_stop')}
                </Button>
              </>
            ) : null}
          </div>
        </div>

        {showProgressBar ? (
          <div className={styles.progressSection}>
            <div className={styles.progressHeader}>
              <strong>{t('monitoring.codex_inspection_progress_title')}</strong>
              <span>{`${progress.percent}%`}</span>
            </div>
            <div className={styles.progressTrack}>
              <span
                className={styles.progressBar}
                style={{ width: `${Math.max(0, Math.min(100, progress.percent))}%` }}
              />
            </div>
            <div className={styles.progressMeta}>
              <span>{progressLabel}</span>
              {runStatus === 'paused' ? (
                <strong>{t('monitoring.codex_inspection_paused')}</strong>
              ) : null}
            </div>
          </div>
        ) : null}

        <CodexInspectionConfigOverview
          title={configOverviewTitle}
          editLabel={configOverviewEditLabel}
          copyLabel={t('monitoring.codex_inspection_settings_copy_prompt')}
          copiedLabel={t('common.copied')}
          items={configOverviewItems}
          onEdit={onEditConfig}
          compact
          embedded
        />

        <section className={styles.summaryGrid}>
          {summaryCards.map((card) => {
            const tone: MonitoringSummaryCardProps['tone'] =
              card.tone === 'good' || card.tone === 'warn' || card.tone === 'bad'
                ? card.tone
                : undefined;
            return (
              <MonitoringSummaryCard
                key={card.key}
                label={card.label}
                value={card.value}
                meta={card.meta}
                icon={card.icon}
                accent={card.accent}
                tone={tone}
              />
            );
          })}
        </section>
      </Card>
    </>
  );
}
