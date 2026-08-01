import type { RefObject } from 'react';
import type { TFunction } from 'i18next';
import {
  IconChevronDown,
  IconChevronUp,
  IconCopy,
  IconRefreshCw,
  IconTrash2,
} from '@/components/ui/icons';
import { Panel } from '@/features/monitoring/components/CodexInspectionPanels';
import {
  formatTimestamp,
  type InspectionLogLevelFilter,
  type InspectionLogViewEntry,
} from '@/features/monitoring/model/codexInspectionPresentation';
import styles from '../CodexInspectionPage.module.scss';

type CodexInspectionLogsPanelProps = {
  logs: InspectionLogViewEntry[];
  logsCollapsed: boolean;
  levelFilter: InspectionLogLevelFilter;
  logListRef: RefObject<HTMLDivElement | null>;
  locale: string;
  t: TFunction;
  onLevelFilterChange: (filter: InspectionLogLevelFilter) => void;
  onCopyLogs?: () => void;
  onJumpToLatest?: () => void;
  onClearLogs?: () => void;
  onToggleCollapsed: () => void;
};

const levelClassMap: Record<InspectionLogViewEntry['level'], string> = {
  info: styles.logInfo,
  success: styles.logSuccess,
  warning: styles.logWarning,
  error: styles.logError,
};

export function CodexInspectionLogsPanel({
  logs,
  logsCollapsed,
  levelFilter,
  logListRef,
  locale,
  t,
  onLevelFilterChange,
  onCopyLogs,
  onJumpToLatest,
  onClearLogs,
  onToggleCollapsed,
}: CodexInspectionLogsPanelProps) {
  const counts: Record<InspectionLogLevelFilter, number> = {
    all: logs.length,
    info: 0,
    success: 0,
    warning: 0,
    error: 0,
  };
  logs.forEach((entry) => {
    counts[entry.level] += 1;
  });
  const filterOptions: ReadonlyArray<{ value: InspectionLogLevelFilter; label: string }> = [
    { value: 'all', label: t('monitoring.codex_inspection_filter_all') },
    { value: 'info', label: t('monitoring.codex_inspection_log_level_info') },
    { value: 'success', label: t('monitoring.codex_inspection_log_level_success') },
    { value: 'warning', label: t('monitoring.codex_inspection_log_level_warning') },
    { value: 'error', label: t('monitoring.codex_inspection_log_level_error') },
  ];
  const filteredLogs =
    levelFilter === 'all' ? logs : logs.filter((entry) => entry.level === levelFilter);

  return (
    <Panel
      title={t('monitoring.codex_inspection_logs_title')}
      extra={
        <div className={styles.logToolbar}>
          {logs.length > 0 ? (
            <div
              className={styles.logFilterGroup}
              role="tablist"
              aria-label={t('monitoring.codex_inspection_logs_title')}
            >
              <div className={styles.segmentedControl}>
                {filterOptions.map((option) => {
                  const active = levelFilter === option.value;
                  return (
                    <button
                      key={option.value}
                      type="button"
                      role="tab"
                      aria-selected={active}
                      className={`${styles.segmentButton} ${active ? styles.segmentButtonActive : ''}`}
                      onClick={() => onLevelFilterChange(option.value)}
                    >
                      {option.label}
                      <span className={styles.segmentCount}>{counts[option.value]}</span>
                    </button>
                  );
                })}
              </div>
            </div>
          ) : (
            <span />
          )}
          <div className={styles.logToolbarRight}>
            {onJumpToLatest ? (
              <button
                type="button"
                className={styles.iconButton}
                onClick={onJumpToLatest}
                disabled={logs.length === 0}
                aria-label={t('monitoring.codex_inspection_logs_jump_latest')}
                title={t('monitoring.codex_inspection_logs_jump_latest')}
              >
                <IconRefreshCw size={14} />
              </button>
            ) : null}
            {onCopyLogs ? (
              <button
                type="button"
                className={styles.iconButton}
                onClick={onCopyLogs}
                disabled={logs.length === 0}
                aria-label={t('monitoring.codex_inspection_logs_copy')}
                title={t('monitoring.codex_inspection_logs_copy')}
              >
                <IconCopy size={14} />
              </button>
            ) : null}
            {onClearLogs ? (
              <button
                type="button"
                className={styles.iconButton}
                onClick={onClearLogs}
                disabled={logs.length === 0}
                aria-label={t('monitoring.codex_inspection_logs_clear')}
                title={t('monitoring.codex_inspection_logs_clear')}
              >
                <IconTrash2 size={14} />
              </button>
            ) : null}
            <button
              type="button"
              className={styles.foldButton}
              onClick={onToggleCollapsed}
              disabled={logs.length === 0}
            >
              {logsCollapsed ? <IconChevronDown size={14} /> : <IconChevronUp size={14} />}
              <span>
                {logsCollapsed
                  ? t('monitoring.codex_inspection_expand_logs')
                  : t('monitoring.codex_inspection_fold_logs')}
              </span>
            </button>
          </div>
        </div>
      }
    >
      {!logsCollapsed ? (
        <div ref={logListRef} className={styles.logList}>
          {filteredLogs.length > 0 ? (
            filteredLogs.map((entry) => (
              <div key={entry.id} className={`${styles.logRow} ${levelClassMap[entry.level]}`}>
                <span className={styles.logTime}>{formatTimestamp(entry.timestamp, locale)}</span>
                <div className={styles.logMessage}>
                  <span>{entry.message}</span>
                  {entry.detail ? (
                    <details className={styles.logDetailDisclosure}>
                      <summary>{t('monitoring.codex_inspection_log_detail')}</summary>
                      <pre>{entry.detail}</pre>
                    </details>
                  ) : null}
                </div>
              </div>
            ))
          ) : (
            <div className={styles.emptyBlockSmall}>
              {t(
                logs.length === 0
                  ? 'monitoring.codex_inspection_logs_empty'
                  : 'monitoring.codex_inspection_logs_filter_empty'
              )}
            </div>
          )}
        </div>
      ) : (
        <div className={styles.logCollapsedBar}>
          <span>{t('monitoring.codex_inspection_logs_collapsed', { count: logs.length })}</span>
        </div>
      )}
    </Panel>
  );
}
