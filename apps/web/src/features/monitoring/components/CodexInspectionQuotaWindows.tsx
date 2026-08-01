import type { TFunction } from 'i18next';
import { formatInspectionQuotaResetLabel } from '@/features/monitoring/model/codexInspectionPresentation';
import styles from '../CodexInspectionPage.module.scss';

export type CodexInspectionQuotaWindowView = {
  id: string;
  labelKey: string;
  labelParams?: Record<string, string | number>;
  usedPercent?: number | null;
  resetLabel?: string;
};

type CodexInspectionQuotaWindowsProps = {
  windows?: readonly CodexInspectionQuotaWindowView[] | null;
  fallbackUsedPercent?: number | null;
  t: TFunction;
};

const clampPercent = (value: number) => Math.min(100, Math.max(0, value));

const normalizePercent = (value?: number | null) =>
  typeof value === 'number' && Number.isFinite(value) ? clampPercent(value) : null;

const formatRemainingPercent = (value: number | null) => {
  if (value === null) return '--';
  return `${Number.isInteger(value) ? value.toFixed(0) : value.toFixed(1)}%`;
};

const getQuotaFillClass = (remainingPercent: number | null) => {
  if (remainingPercent === null) return styles.quotaWindowBarFillMedium;
  if (remainingPercent >= 70) return styles.quotaWindowBarFillHigh;
  if (remainingPercent >= 30) return styles.quotaWindowBarFillMedium;
  return styles.quotaWindowBarFillLow;
};

const formatQuotaLabel = (window: CodexInspectionQuotaWindowView, t: TFunction) =>
  t(window.labelKey, window.labelParams ?? {});

export function CodexInspectionQuotaWindows({
  windows,
  fallbackUsedPercent,
  t,
}: CodexInspectionQuotaWindowsProps) {
  const normalizedFallbackUsedPercent = normalizePercent(fallbackUsedPercent);
  const knownRows = (windows ?? []).flatMap((window) => {
    const usedPercent = normalizePercent(window.usedPercent);
    return usedPercent === null
      ? []
      : [
          {
            id: window.id,
            label: formatQuotaLabel(window, t),
            resetLabel: window.resetLabel,
            usedPercent,
          },
        ];
  });
  const rows =
    knownRows.length > 0
      ? knownRows
      : normalizedFallbackUsedPercent === null
        ? []
        : [
            {
              id: 'overall',
              label: t('monitoring.codex_inspection_used_percent'),
              resetLabel: '',
              usedPercent: normalizedFallbackUsedPercent,
            },
          ];

  if (rows.length === 0) {
    return (
      <div className={styles.quotaWindowEmpty}>
        <span className={styles.quotaWindowUnavailable}>
          {t('monitoring.codex_inspection_quota_unavailable')}
        </span>
        <span className={styles.quotaWindowPlaceholderBar} aria-hidden="true" />
      </div>
    );
  }

  return (
    <div className={styles.quotaWindowList}>
      {rows.map((row) => {
        const usedPercent = normalizePercent(row.usedPercent);
        const remainingPercent = usedPercent === null ? null : clampPercent(100 - usedPercent);
        const resetTime = formatInspectionQuotaResetLabel(row.resetLabel);
        const resetLabel = resetTime
          ? t('monitoring.codex_inspection_quota_reset', { time: resetTime })
          : '';

        return (
          <div key={row.id} className={styles.quotaWindowRow}>
            <div className={styles.quotaWindowHeader}>
              <span className={styles.quotaWindowLabel}>{row.label}</span>
              <span className={styles.quotaWindowValue}>
                {t('monitoring.codex_inspection_quota_remaining', {
                  percent: formatRemainingPercent(remainingPercent),
                })}
              </span>
            </div>
            <div className={styles.quotaWindowBar} aria-hidden="true">
              <span
                className={`${styles.quotaWindowBarFill} ${getQuotaFillClass(remainingPercent)}`}
                style={{ width: `${remainingPercent ?? 0}%` }}
              />
            </div>
            {resetLabel ? <span className={styles.quotaWindowReset}>{resetLabel}</span> : null}
          </div>
        );
      })}
    </div>
  );
}
