import type { TFunction } from 'i18next';
import type { MonitoringAnalyticsCoverage } from '@/services/api/usageService';
import styles from './UsageCoverageWarning.module.scss';

interface UsageCoverageWarningProps {
  coverage?: MonitoringAnalyticsCoverage;
  t: TFunction;
}

export function UsageCoverageWarning({ coverage, t }: UsageCoverageWarningProps) {
  const currentDeleted = coverage?.raw_deleted_event_count ?? 0;
  const comparisonDeleted = coverage?.comparison_raw_deleted_event_count ?? 0;
  const auxiliaryRanges = (coverage?.auxiliary_ranges ?? []).filter(
    (range) => range.raw_deleted_event_count > 0
  );
  const auxiliaryDeleted = auxiliaryRanges.reduce(
    (total, range) => total + range.raw_deleted_event_count,
    0
  );
  if (!coverage || currentDeleted + comparisonDeleted + auxiliaryDeleted <= 0) return null;

  const hasLimitations = coverage.fidelity_limitations.length > 0;
  return (
    <section
      className={styles.notice}
      role="status"
      aria-live="polite"
      data-coverage-mode={coverage.mode}
    >
      <strong>{t('monitoring.coverage_warning_title')}</strong>
      {currentDeleted > 0 ? (
        <span>{t('monitoring.coverage_warning_current', { deleted: currentDeleted })}</span>
      ) : null}
      {comparisonDeleted > 0 ? (
        <span>{t('monitoring.coverage_warning_comparison', { deleted: comparisonDeleted })}</span>
      ) : null}
      {auxiliaryRanges.map((range) => {
        const translationKey =
          range.scope === 'rolling_30m'
            ? 'monitoring.coverage_warning_rolling'
            : range.scope === 'drilldown_preview'
              ? 'monitoring.coverage_warning_drilldown'
              : 'monitoring.coverage_warning_auxiliary';
        return (
          <span key={`${range.scope}:${range.from_ms}:${range.to_ms}`}>
            {t(translationKey, { deleted: range.raw_deleted_event_count })}
          </span>
        );
      })}
      <span>
        {t(
          hasLimitations
            ? 'monitoring.coverage_warning_limited'
            : 'monitoring.coverage_warning_derived'
        )}
      </span>
    </section>
  );
}
