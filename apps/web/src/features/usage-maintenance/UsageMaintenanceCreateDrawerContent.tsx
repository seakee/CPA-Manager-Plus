import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import {
  DrawerDefinitionList,
  DrawerDlRow,
  DrawerNotice,
  DrawerSectionCard,
  DrawerTabPanel,
} from '@/components/common/WorkspaceDrawer';
import {
  IconCalendar,
  IconChartLine,
  IconInfo,
  IconRefreshCw,
  IconSlidersHorizontal,
  IconTriangleAlert,
} from '@/components/ui/icons';
import { SegmentedTabs, type SegmentedTabItem } from '@/components/ui/SegmentedTabs';
import type { UsageArchivePreview, UsageMaintenanceStatus } from '@/services/api/usageService';
import { formatDateTime, formatFileSize } from '@/utils/format';
import {
  isRecentCutoffWarning,
  retentionPresetDays,
  toLocalDateTimeValue,
  type MaintenanceIntent,
  type RawEventRangeState,
  type RetentionPresetDays,
  type RetentionSelection,
} from './usageMaintenanceModel';
import styles from './UsageMaintenancePage.module.scss';

type Props = {
  maintenance: UsageMaintenanceStatus;
  intent: MaintenanceIntent;
  retentionSelection: RetentionSelection;
  customCutoff: string;
  referenceNowMS: number;
  resolvedCutoffMS: number | null;
  recommendedDays: RetentionPresetDays | null;
  rawRange: RawEventRangeState;
  preview: UsageArchivePreview | null;
  previewLoading: boolean;
  previewError: string | null;
  working: boolean;
  hasActiveMutex: boolean;
  archiveReadinessBlocked: boolean;
  confirmStep: boolean;
  onChangeIntent: (intent: MaintenanceIntent) => void;
  onSelectRetention: (selection: RetentionSelection) => void;
  onChangeCustomCutoff: (value: string) => void;
  onRetryPreview: () => void;
  onViewHistory: () => void;
};

export function UsageMaintenanceCreateDrawerContent({
  maintenance,
  intent,
  retentionSelection,
  customCutoff,
  referenceNowMS,
  resolvedCutoffMS,
  recommendedDays,
  rawRange,
  preview,
  previewLoading,
  previewError,
  working,
  hasActiveMutex,
  archiveReadinessBlocked,
  confirmStep,
  onChangeIntent,
  onSelectRetention,
  onChangeCustomCutoff,
  onRetryPreview,
  onViewHistory,
}: Props) {
  const { t, i18n } = useTranslation();
  const formatTime = (value?: number | null) =>
    value ? formatDateTime(new Date(value), i18n.language) : '—';

  const hasArchived = (maintenance.raw_archived_event_count ?? 0) > 0;
  const canRecommend =
    !hasArchived && recommendedDays !== null && recommendedDays !== retentionSelection;
  const recentCutoffWarning = isRecentCutoffWarning(intent, resolvedCutoffMS, referenceNowMS);

  const retentionTabs: SegmentedTabItem<string>[] = [
    ...retentionPresetDays.map((days) => ({
      id: String(days),
      label: t(`usage_maintenance.retention_${days}_label`),
      testId: `usage-maintenance-preset-${days}`,
    })),
    {
      id: 'custom',
      label: t('usage_maintenance.retention_custom_label'),
      testId: 'usage-maintenance-preset-custom',
    },
  ];

  if (confirmStep && preview) {
    const cutoffTimestamp = preview.cutoff_timestamp_ms ?? resolvedCutoffMS;
    return (
      <DrawerTabPanel testId="usage-maintenance-create-confirm">
        <DrawerNotice tone="info">
          {t('usage_maintenance.archive_prepare_confirm_message', {
            defaultValue:
              'The archive will be prepared and verified. Online details remain available until you separately confirm cleanup.',
          })}
        </DrawerNotice>

        <DrawerSectionCard
          icon={<IconChartLine size={15} />}
          title={t('usage_maintenance.preview', { defaultValue: 'Impact preview' })}
        >
          <DrawerDefinitionList>
            <DrawerDlRow
              label={t('usage_maintenance.preview_events', { defaultValue: 'New events to archive' })}
              mono
            >
              {preview.event_count.toLocaleString(i18n.language)}
            </DrawerDlRow>
            <DrawerDlRow label={t('usage_maintenance.preview_source_bytes')} mono>
              {formatFileSize(preview.estimated_bytes)}
            </DrawerDlRow>
            <DrawerDlRow
              label={t('usage_maintenance.cutoff', { defaultValue: 'Archive events before' })}
              mono
            >
              {formatTime(cutoffTimestamp)}
            </DrawerDlRow>
            <DrawerDlRow
              label={t('usage_maintenance.preview_range', { defaultValue: 'Timestamp range' })}
              mono
            >
              {formatTime(preview.min_timestamp_ms)} – {formatTime(preview.max_timestamp_ms)}
            </DrawerDlRow>
          </DrawerDefinitionList>
        </DrawerSectionCard>

        <p className={styles.flatRowMeta}>
          {t('usage_maintenance.workspace_recalculate', {
            defaultValue:
              'The server recalculates eligible data when the operation starts. This estimate does not include existing archives or promise a disk-space saving.',
          })}
        </p>
      </DrawerTabPanel>
    );
  }

  return (
    <DrawerTabPanel testId="usage-maintenance-create-drawer">
      <DrawerSectionCard
        icon={<IconSlidersHorizontal size={15} />}
        headingLevel="h3"
        title={t('usage_maintenance.workspace_purpose', {
          defaultValue: 'What would you like to do?',
        })}
      >
        <div
          className={styles.intentGrid}
          role="group"
          aria-label={t('usage_maintenance.workspace_purpose', {
            defaultValue: 'What would you like to do?',
          })}
        >
          {(['archive', 'cleanup'] as const).map((value) => (
            <button
              key={value}
              type="button"
              aria-pressed={intent === value}
              className={`${styles.intentCard} ${intent === value ? styles.intentCardActive : ''}`}
              disabled={working}
              onClick={() => onChangeIntent(value)}
              data-testid={`usage-maintenance-intent-${value}`}
            >
              <strong>{t(`usage_maintenance.intent_${value}`)}</strong>
              <span>{t(`usage_maintenance.intent_${value}_hint`)}</span>
            </button>
          ))}
        </div>
      </DrawerSectionCard>

      <DrawerSectionCard
        icon={<IconCalendar size={15} />}
        headingLevel="h3"
        title={t('usage_maintenance.workspace_range', { defaultValue: 'Choose the data range' })}
        metaActions={
          canRecommend ? (
            <Button
              variant="ghost"
              size="xs"
              disabled={working}
              onClick={() => onSelectRetention(recommendedDays)}
            >
              {t('usage_maintenance.use_recommended_retention', {
                days: recommendedDays,
                defaultValue: `Older than ${recommendedDays} days`,
              })}
            </Button>
          ) : null
        }
      >
        <SegmentedTabs<string>
          variant="joined"
          size="sm"
          responsiveFullWidth={false}
          activeTab={String(retentionSelection)}
          onChange={(value) => {
            const next: RetentionSelection =
              value === 'custom'
                ? 'custom'
                : (Number(value) as (typeof retentionPresetDays)[number]);
            onSelectRetention(next);
          }}
          items={retentionTabs}
          ariaLabel={t('usage_maintenance.retention_group_label')}
          idBase="usage-maintenance-retention-tabs"
        />

        {retentionSelection === 'custom' ? (
          <div className={`form-group ${styles.customDateField}`}>
            <label htmlFor="usage-maintenance-custom-cutoff" className={styles.customDateLabel}>
              {t('usage_maintenance.cutoff', { defaultValue: 'Archive events before' })}
            </label>
            <input
              id="usage-maintenance-custom-cutoff"
              type="datetime-local"
              max={toLocalDateTimeValue(referenceNowMS)}
              className={`input ${styles.customDateInput}`}
              value={customCutoff}
              disabled={working}
              onChange={(event) => onChangeCustomCutoff(event.target.value)}
            />
          </div>
        ) : null}

        <DrawerDefinitionList>
          <DrawerDlRow
            label={t('usage_maintenance.cutoff', { defaultValue: 'Archive events before' })}
            mono
          >
            {formatTime(resolvedCutoffMS)}
          </DrawerDlRow>
          {rawRange.kind === 'available' ? (
            <DrawerDlRow label={t('usage_maintenance.online_range_title')} mono>
              {formatTime(rawRange.minTimestampMS)} – {formatTime(rawRange.maxTimestampMS)}
            </DrawerDlRow>
          ) : null}
        </DrawerDefinitionList>
        <p className={styles.flatRowMeta}>
          {t('usage_maintenance.workspace_once', {
            defaultValue:
              'This range applies to this operation only. It does not create an automatic retention rule.',
          })}
        </p>
      </DrawerSectionCard>

      <DrawerSectionCard
        icon={<IconChartLine size={15} />}
        headingLevel="h3"
        title={t('usage_maintenance.preview', { defaultValue: 'Impact preview' })}
        dataAttributes={{ 'aria-live': 'polite', 'aria-busy': previewLoading }}
        metaActions={
          <Button
            variant="ghost"
            size="xs"
            loading={previewLoading}
            disabled={working}
            onClick={onRetryPreview}
            data-testid="usage-maintenance-preview-btn"
          >
            <IconRefreshCw size={13} aria-hidden="true" />
            <span>{t('usage_maintenance.preview_retry', { defaultValue: 'Recalculate' })}</span>
          </Button>
        }
      >
        {previewLoading ? (
          <div
            className={styles.skeletonGrid}
            aria-label={t('usage_maintenance.preview_loading', { defaultValue: 'Calculating…' })}
          >
            <div className={styles.skeletonCard} />
            <div className={styles.skeletonCard} />
            <div className={`${styles.skeletonCard} ${styles.skeletonCardWide}`} />
          </div>
        ) : null}

        {!previewLoading && previewError ? (
          <div className={`${styles.alertBanner} ${styles.alertDanger}`} role="alert">
            <span>{previewError}</span>
            {resolvedCutoffMS ? (
              <Button size="xs" variant="secondary" disabled={working} onClick={onRetryPreview}>
                {t('usage_maintenance.preview_retry', { defaultValue: 'Retry' })}
              </Button>
            ) : null}
          </div>
        ) : null}

        {!previewLoading && !previewError && preview && preview.event_count > 0 ? (
          <>
            <div className={styles.drawerPreviewGrid} data-testid="usage-maintenance-preview-result">
              <div className={styles.inlineMetric}>
                <span className={styles.inlineMetricLabel}>
                  {t('usage_maintenance.preview_events', { defaultValue: 'New events to archive' })}
                </span>
                <strong className={styles.inlineMetricValue}>
                  {preview.event_count.toLocaleString(i18n.language)}
                </strong>
              </div>
              <div className={styles.inlineMetric}>
                <span className={styles.inlineMetricLabel}>
                  {t('usage_maintenance.preview_source_bytes')}
                </span>
                <strong className={styles.inlineMetricValue}>
                  {formatFileSize(preview.estimated_bytes)}
                </strong>
              </div>
              <div className={`${styles.inlineMetric} ${styles.inlineMetricFull}`}>
                <span className={styles.inlineMetricLabel}>
                  {t('usage_maintenance.preview_range', { defaultValue: 'Timestamp range' })}
                </span>
                <strong className={styles.inlineMetricValue}>
                  {formatTime(preview.min_timestamp_ms)} – {formatTime(preview.max_timestamp_ms)}
                </strong>
              </div>
            </div>
            <div className={`${styles.alertBanner} ${styles.alertInfo}`}>
              <IconInfo size={15} aria-hidden="true" />
              <span>
                {t('usage_maintenance.preview_space_expectation', {
                  defaultValue:
                    'Expected reclaimable space: ~{{size}}. Note: SQLite physical file size does not shrink immediately and requires offline compaction.',
                  size: formatFileSize(preview.estimated_bytes),
                })}
              </span>
            </div>
          </>
        ) : null}

        {!previewLoading && !previewError && preview?.event_count === 0 ? (
          <div className={styles.emptyPreviewBox} data-testid="usage-maintenance-preview-empty">
            <div className={styles.emptyPreviewHeader}>
              <IconInfo size={16} aria-hidden="true" />
              <strong>{t('usage_maintenance.preview_empty_title')}</strong>
            </div>
            <p className={styles.flatRowMeta}>
              {rawRange.kind === 'empty'
                ? t('usage_maintenance.preview_empty_no_data')
                : hasArchived
                  ? t('usage_maintenance.preview_empty_archived', {
                      defaultValue: t('usage_maintenance.preview_empty_generic'),
                    })
                  : rawRange.kind === 'available' && recommendedDays
                    ? t('usage_maintenance.preview_empty_recommendation', {
                        oldest: formatTime(rawRange.minTimestampMS),
                        days: recommendedDays,
                      })
                    : t('usage_maintenance.preview_empty_generic')}
            </p>
            <div className={styles.rowActions}>
              <Button size="xs" variant="secondary" onClick={onViewHistory}>
                {t('usage_maintenance.workspace_existing', {
                  defaultValue: 'View existing archives',
                })}
              </Button>
              {canRecommend ? (
                <Button
                  size="xs"
                  variant="ghost"
                  disabled={working}
                  onClick={() => onSelectRetention(recommendedDays)}
                >
                  {t('usage_maintenance.use_recommended_retention', {
                    days: recommendedDays,
                    defaultValue: `Older than ${recommendedDays} days`,
                  })}
                </Button>
              ) : null}
            </div>
          </div>
        ) : null}
      </DrawerSectionCard>

      {recentCutoffWarning ? (
        <DrawerNotice
          tone="danger"
          role="alert"
          icon={<IconTriangleAlert size={15} />}
          title={t('usage_maintenance.cleanup_recent_warning_title', {
            defaultValue: 'Selected range includes usage data from the last 14 days',
          })}
        >
          {t('usage_maintenance.cleanup_recent_warning_desc', {
            defaultValue:
              'Recent request-level details will no longer be queryable online after cleanup (archives and aggregates remain intact).',
          })}
        </DrawerNotice>
      ) : null}

      {hasActiveMutex ? (
        <DrawerNotice tone="warning">{t('usage_maintenance.create_blocked_active')}</DrawerNotice>
      ) : archiveReadinessBlocked ? (
        <DrawerNotice tone="warning">
          {t('usage_maintenance.archive_readiness_pending')}
        </DrawerNotice>
      ) : null}
      <p className={styles.flatRowMeta}>
        {intent === 'cleanup'
          ? t('usage_maintenance.workspace_cleanup_hint')
          : t('usage_maintenance.archive_prepare_no_delete')}
      </p>

      <details className={styles.detailsCollapse}>
        <summary>
          {t('usage_maintenance.workspace_impact', {
            defaultValue: 'What changes after cleanup?',
          })}
        </summary>
        <div className={styles.detailsCollapseBody}>
          <p>{t('usage_maintenance.workspace_impact_hint')}</p>
          <p>{t('usage_maintenance.preview_excludes_existing')}</p>
          <p>{t('usage_maintenance.preview_source_bytes_hint')}</p>
        </div>
      </details>
    </DrawerTabPanel>
  );
}
