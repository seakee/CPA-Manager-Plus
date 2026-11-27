import type { ReactNode } from 'react';
import { ProviderStatusBar } from '@/components/providers/ProviderStatusBar';
import { IconActivity } from '@/components/ui/icons';
import type { StatusBarData } from '@/utils/recentRequests';
import { DrawerEmptyBox, DrawerScopePill, DrawerSectionCard } from './WorkspaceDrawer';
import styles from './WorkspaceDrawer.module.scss';

export interface DrawerRecentStatusCardProps {
  title: ReactNode;
  scopeLabel: ReactNode;
  statusData: StatusBarData;
  statusBarStyles?: Record<string, string>;
  successLabel: ReactNode;
  failureLabel: ReactNode;
  timelineLabel: ReactNode;
  timelineHint: ReactNode;
  formatCount?: (value: number) => string;
  headingLevel?: 'h3' | 'h4';
  unavailableMessage?: ReactNode;
  emptyMessage?: ReactNode;
  failureBannerTitle?: ReactNode;
  failureBannerMessage?: ReactNode;
  coverageNotice?: ReactNode;
  timelineDataAttributes?: Record<string, string | boolean | undefined>;
  emptyDataAttributes?: Record<string, string | boolean | undefined>;
  messageDataAttributes?: Record<string, string | boolean | undefined>;
  dataAttributes?: Record<string, string | boolean | undefined>;
  className?: string;
}

export function DrawerRecentStatusCard({
  title,
  scopeLabel,
  statusData,
  statusBarStyles,
  successLabel,
  failureLabel,
  timelineLabel,
  timelineHint,
  formatCount = (value) => value.toLocaleString(),
  headingLevel = 'h4',
  unavailableMessage,
  emptyMessage,
  failureBannerTitle,
  failureBannerMessage,
  coverageNotice,
  timelineDataAttributes,
  emptyDataAttributes,
  messageDataAttributes,
  dataAttributes,
  className,
}: DrawerRecentStatusCardProps) {
  const totalRecent = statusData.totalSuccess + statusData.totalFailure;
  const hasSuccess = statusData.totalSuccess > 0;
  const hasFailure = statusData.totalFailure > 0;

  return (
    <DrawerSectionCard
      variant="default"
      icon={<IconActivity size={15} />}
      title={title}
      headingLevel={headingLevel}
      metaActions={<DrawerScopePill>{scopeLabel}</DrawerScopePill>}
      dataAttributes={{
        'data-overview-section': 'recent-status',
        ...dataAttributes,
      }}
      className={className}
    >
      {unavailableMessage ? (
        <DrawerEmptyBox>{unavailableMessage}</DrawerEmptyBox>
      ) : (
        <>
          <div className={styles.recentStatusLayout}>
            <div className={styles.recentStatusStats}>
              <div
                className={`${styles.recentStatusMetric} ${
                  hasSuccess ? styles.recentStatusMetricSuccess : styles.recentStatusMetricMuted
                }`}
              >
                <span>{successLabel}</span>
                <strong>{formatCount(statusData.totalSuccess)}</strong>
              </div>
              <div
                className={`${styles.recentStatusMetric} ${
                  hasFailure ? styles.recentStatusMetricFailure : styles.recentStatusMetricMuted
                }`}
              >
                <span>{failureLabel}</span>
                <strong>{formatCount(statusData.totalFailure)}</strong>
              </div>
            </div>

            <div className={styles.recentStatusTimeline} {...timelineDataAttributes}>
              <div className={styles.recentStatusTimelineHeader}>
                <span>{timelineLabel}</span>
                <span>{timelineHint}</span>
              </div>
              <div className={styles.recentStatusBarHost}>
                <ProviderStatusBar statusData={statusData} styles={statusBarStyles} />
              </div>
            </div>
          </div>

          {totalRecent === 0 && emptyMessage ? (
            <DrawerEmptyBox dataAttributes={emptyDataAttributes}>{emptyMessage}</DrawerEmptyBox>
          ) : null}

          {failureBannerMessage ? (
            <div className={styles.recentStatusMessage} {...messageDataAttributes}>
              {failureBannerTitle ? <span>{failureBannerTitle}</span> : null}
              <p>{failureBannerMessage}</p>
            </div>
          ) : null}

          {coverageNotice ? <p className={styles.coverageNotice}>{coverageNotice}</p> : null}
        </>
      )}
    </DrawerSectionCard>
  );
}
