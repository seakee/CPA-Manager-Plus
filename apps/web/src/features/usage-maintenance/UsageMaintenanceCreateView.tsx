import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import type { UsageArchivePreview, UsageMaintenanceStatus } from '@/services/api/usageService';
import { formatDateTime, formatFileSize } from '@/utils/format';
import {
  retentionPresetDays,
  toLocalDateTimeValue,
  type RetentionPresetDays,
  type RetentionSelection,
} from './usageMaintenanceModel';
import type { MaintenanceIntent } from './usageMaintenanceNavigation';
import styles from './UsageMaintenanceCreateView.module.scss';

export type GuidedArchiveStage =
  | 'idle'
  | 'creating'
  | 'archiving'
  | 'verifying'
  | 'complete'
  | 'attention';

type Props = {
  maintenance: UsageMaintenanceStatus;
  preview: UsageArchivePreview | null;
  previewLoading: boolean;
  previewError: string | null;
  retentionSelection: RetentionSelection;
  customCutoff: string;
  referenceNowMS: number;
  resolvedCutoffTimestamp?: number;
  recommendedRetentionDays: RetentionPresetDays | null;
  intent: MaintenanceIntent;
  working: boolean;
  createBlockedByMaintenance: boolean;
  archiveReadinessPending: boolean;
  archiveReadinessHint: string;
  onIntent: (intent: MaintenanceIntent) => void;
  onHistory: () => void;
  onSelectRetention: (selection: RetentionSelection) => void;
  onUpdateCustomCutoff: (value: string) => void;
  onRetryPreview: () => void;
};

export function UsageMaintenanceCreateView({
  maintenance,
  preview,
  previewLoading,
  previewError,
  retentionSelection,
  customCutoff,
  referenceNowMS,
  resolvedCutoffTimestamp,
  recommendedRetentionDays,
  intent,
  working,
  createBlockedByMaintenance,
  archiveReadinessPending,
  archiveReadinessHint,
  onIntent,
  onHistory,
  onSelectRetention,
  onUpdateCustomCutoff,
  onRetryPreview,
}: Props) {
  const { t, i18n } = useTranslation();
  const formatTime = (value?: number) =>
    value ? formatDateTime(new Date(value), i18n.language) : '—';
  const hasArchived = (maintenance.raw_archived_event_count ?? 0) > 0;
  const canRecommend =
    !hasArchived &&
    recommendedRetentionDays !== null &&
    recommendedRetentionDays !== retentionSelection;
  const disabledReason = createBlockedByMaintenance
    ? t('usage_maintenance.create_blocked_active', {
        defaultValue:
          'Finish or recover the active maintenance task before starting another archive.',
      })
    : archiveReadinessPending
      ? archiveReadinessHint
      : previewError || undefined;

  return (
    <div className={styles.view}>
      <div className={styles.layout}>
        <section className={styles.card}>
          <h2>
            {t('usage_maintenance.workspace_purpose', {
              defaultValue: 'What would you like to do?',
            })}
          </h2>
          <div
            className={styles.intentOptions}
            role="group"
            aria-label={t('usage_maintenance.workspace_purpose')}
          >
            {(['archive', 'cleanup'] as const).map((value) => (
              <button
                key={value}
                type="button"
                aria-pressed={intent === value}
                className={intent === value ? styles.selected : ''}
                disabled={working}
                onClick={() => onIntent(value)}
              >
                <strong>{t(`usage_maintenance.intent_${value}`)}</strong>
                <span>{t(`usage_maintenance.intent_${value}_hint`)}</span>
              </button>
            ))}
          </div>
          <h2>
            {t('usage_maintenance.workspace_range', { defaultValue: 'Choose the data range' })}
          </h2>
          <div
            className={styles.retentionOptions}
            role="group"
            aria-label={t('usage_maintenance.retention_group_label', {
              defaultValue: 'Data range for this operation',
            })}
          >
            {retentionPresetDays.map((days) => (
              <button
                key={days}
                type="button"
                aria-pressed={retentionSelection === days}
                className={retentionSelection === days ? styles.selected : ''}
                disabled={working}
                onClick={() => onSelectRetention(days)}
              >
                {t(`usage_maintenance.retention_${days}_label`, {
                  defaultValue: `Older than ${days} days`,
                })}
              </button>
            ))}
            <button
              type="button"
              aria-pressed={retentionSelection === 'custom'}
              className={retentionSelection === 'custom' ? styles.selected : ''}
              disabled={working}
              onClick={() => onSelectRetention('custom')}
            >
              {t('usage_maintenance.retention_custom_label', { defaultValue: 'Custom date' })}
            </button>
          </div>
          {retentionSelection === 'custom' ? (
            <Input
              type="datetime-local"
              label={t('usage_maintenance.cutoff', { defaultValue: 'Archive events before' })}
              max={toLocalDateTimeValue(referenceNowMS)}
              value={customCutoff}
              disabled={working}
              error={!resolvedCutoffTimestamp ? (previewError ?? undefined) : undefined}
              onChange={(event) => onUpdateCustomCutoff(event.target.value)}
            />
          ) : null}
          <p className={styles.cutoff}>
            {t('usage_maintenance.cutoff', { defaultValue: 'Archive events before' })}
            <strong>{formatTime(resolvedCutoffTimestamp)}</strong>
          </p>
          <p className={styles.hint}>
            {t('usage_maintenance.workspace_once', {
              defaultValue:
                'This range applies to this operation only. It does not create an automatic retention rule.',
            })}
          </p>
        </section>

        <section className={styles.card} aria-live="polite" aria-busy={previewLoading}>
          <h2>{t('usage_maintenance.preview', { defaultValue: 'Impact preview' })}</h2>
          {previewLoading ? (
            <p className={styles.hint}>
              {t('usage_maintenance.preview_loading', { defaultValue: 'Calculating…' })}
            </p>
          ) : null}
          {previewError ? (
            <div className={styles.warning} role="alert">
              <p>{previewError}</p>
              {resolvedCutoffTimestamp ? (
                <Button size="sm" variant="secondary" onClick={onRetryPreview} disabled={working}>
                  {t('usage_maintenance.preview_retry', { defaultValue: 'Retry calculation' })}
                </Button>
              ) : null}
            </div>
          ) : null}
          {!previewError && preview && preview.event_count > 0 ? (
            <dl className={styles.previewGrid}>
              <div>
                <dt>
                  {t('usage_maintenance.preview_events', { defaultValue: 'New events to archive' })}
                </dt>
                <dd>{preview.event_count.toLocaleString(i18n.language)}</dd>
              </div>
              <div>
                <dt>
                  {t('usage_maintenance.preview_source_bytes', {
                    defaultValue: 'Estimated source size',
                  })}
                </dt>
                <dd>{formatFileSize(preview.estimated_bytes)}</dd>
              </div>
              <div className={styles.range}>
                <dt>{t('usage_maintenance.preview_range', { defaultValue: 'Timestamp range' })}</dt>
                <dd>
                  {formatTime(preview.min_timestamp_ms)} – {formatTime(preview.max_timestamp_ms)}
                </dd>
              </div>
            </dl>
          ) : null}
          {!previewLoading && !previewError && preview?.event_count === 0 ? (
            <div className={styles.empty}>
              <strong>
                {t('usage_maintenance.preview_empty_title', {
                  defaultValue: 'No new events to archive in this range',
                })}
              </strong>
              <p>
                {maintenance.raw_event_count === 0
                  ? t('usage_maintenance.preview_empty_no_data')
                  : hasArchived
                    ? t('usage_maintenance.preview_empty_archived')
                    : canRecommend
                      ? t('usage_maintenance.preview_empty_recommendation', {
                          days: recommendedRetentionDays,
                          oldest: formatTime(maintenance.raw_min_timestamp_ms),
                        })
                      : t('usage_maintenance.preview_empty_recent')}
              </p>
              <Button size="sm" variant="secondary" onClick={onHistory}>
                {t('usage_maintenance.workspace_existing', {
                  defaultValue: 'View existing archives',
                })}
              </Button>
              {canRecommend ? (
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => onSelectRetention(recommendedRetentionDays)}
                  disabled={working}
                >
                  {t('usage_maintenance.use_recommended_retention', {
                    defaultValue: 'Older than {{days}} days',
                    days: recommendedRetentionDays,
                  })}
                </Button>
              ) : null}
            </div>
          ) : null}
          {disabledReason && !previewError ? (
            <p className={styles.warning}>{disabledReason}</p>
          ) : null}
          <p className={styles.hint}>
            {t('usage_maintenance.preview_excludes_existing', {
              defaultValue:
                'This preview counts only data not already archived. Existing archives are handled separately, one record at a time.',
            })}
          </p>
          {hasArchived ? (
            <Button variant="ghost" size="sm" onClick={onHistory}>
              {t('usage_maintenance.workspace_existing', {
                defaultValue: 'View existing archives',
              })}
            </Button>
          ) : null}
          <p className={styles.hint}>
            {t('usage_maintenance.preview_source_bytes_hint', {
              defaultValue:
                'Source-row estimate, not archive size or disk space that will be released.',
            })}
          </p>
          <p className={styles.hint}>
            {intent === 'cleanup'
              ? t('usage_maintenance.workspace_cleanup_hint', {
                  defaultValue:
                    'After verification, review this archive and confirm cleanup separately. Archived data remains online until you confirm.',
                })
              : t('usage_maintenance.archive_prepare_no_delete', {
                  defaultValue:
                    'Archiving keeps online details available. You can clean them up later from this record.',
                })}
          </p>
          <details className={styles.details}>
            <summary>
              {t('usage_maintenance.workspace_impact', {
                defaultValue: 'What changes after cleanup?',
              })}
            </summary>
            <p>
              {t('usage_maintenance.workspace_impact_hint', {
                defaultValue:
                  'Cleanup removes online details for this archive. The server checks statistics coverage first; detailed queries and future recalculation are limited afterward. Back up the database, key and archives before cleanup. The SQLite file shrinks only after offline compaction.',
              })}
            </p>
          </details>
        </section>
      </div>
    </div>
  );
}
