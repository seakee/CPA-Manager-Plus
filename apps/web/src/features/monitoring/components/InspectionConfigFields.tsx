import type { TFunction } from 'i18next';
import { Input } from '@/components/ui/Input';
import type { CodexInspectionAutoActionMode } from '@/features/monitoring/codexInspection';
import { CodexInspectionAutoActionEditor } from '@/features/monitoring/components/CodexInspectionAutoActionEditor';
import type {
  InspectionConfigFieldErrors,
  SharedInspectionConfigDraft,
  SharedInspectionConfigField,
} from '@/features/monitoring/model/codexInspectionPresentation';
import styles from '../CodexInspectionPage.module.scss';

type InspectionConfigFieldsProps = {
  draft: SharedInspectionConfigDraft;
  errors: InspectionConfigFieldErrors;
  t: TFunction;
  onFieldChange: (field: SharedInspectionConfigField, value: string) => void;
  onAutoActionModeChange: (mode: CodexInspectionAutoActionMode) => void;
  onAutoRecoverEnabledChange: (enabled: boolean) => void;
};

// 本地与服务端共享的 9 个配置字段。分组:基础规则 → 自动处置 → 高级(默认折叠)。
// 字段 id 与 field 名一致,供概览卡点击后在 Drawer 内定位聚焦。
export function InspectionConfigFields({
  draft,
  errors,
  t,
  onFieldChange,
  onAutoActionModeChange,
  onAutoRecoverEnabledChange,
}: InspectionConfigFieldsProps) {
  return (
    <>
      <section className={styles.configSection}>
        <header className={styles.configSectionHeader}>
          <span>{t('monitoring.codex_inspection_settings_group_strategy')}</span>
        </header>
        <div className={styles.serverConfigGrid}>
          <div className={styles.serverField}>
            <Input
              id="usedPercentThreshold"
              label={t('monitoring.codex_inspection_settings_used_percent_threshold_label')}
              hint={t('monitoring.codex_inspection_settings_threshold_hint')}
              error={errors.usedPercentThreshold}
              type="number"
              min={0}
              max={100}
              step={0.1}
              value={draft.usedPercentThreshold}
              onChange={(event) => onFieldChange('usedPercentThreshold', event.target.value)}
            />
          </div>
          <div className={styles.serverField}>
            <Input
              id="sampleSize"
              label={t('monitoring.codex_inspection_settings_sample_size_label')}
              hint={t('monitoring.codex_inspection_settings_sample_size_hint')}
              error={errors.sampleSize}
              type="number"
              min={0}
              step={1}
              value={draft.sampleSize}
              onChange={(event) => onFieldChange('sampleSize', event.target.value)}
            />
          </div>
        </div>
      </section>

      <section className={styles.configSection}>
        <header className={styles.configSectionHeader}>
          <span>{t('monitoring.codex_inspection_settings_group_auto')}</span>
        </header>
        <div className={styles.autoActionField} id="autoActionMode">
          <CodexInspectionAutoActionEditor
            value={draft.autoActionMode}
            autoRecoverEnabled={draft.autoRecoverEnabled}
            t={t}
            onChange={onAutoActionModeChange}
            onAutoRecoverChange={onAutoRecoverEnabledChange}
          />
        </div>
      </section>

      <details className={styles.advancedSection}>
        <summary>
          <span>{t('monitoring.server_codex_inspection_advanced_title')}</span>
          <span className={styles.advancedSummaryHint}>
            {t('monitoring.server_codex_inspection_advanced_hint')}
          </span>
        </summary>
        <div className={styles.advancedBody}>
          <div className={styles.serverField}>
            <Input
              id="targetType"
              label={t('monitoring.codex_inspection_settings_target_type_label')}
              error={errors.targetType}
              value={draft.targetType}
              onChange={(event) => onFieldChange('targetType', event.target.value)}
            />
          </div>
          <div className={styles.serverField}>
            <Input
              id="workers"
              label={t('monitoring.codex_inspection_settings_workers_label')}
              error={errors.workers}
              type="number"
              min={1}
              step={1}
              value={draft.workers}
              onChange={(event) => onFieldChange('workers', event.target.value)}
            />
          </div>
          <div className={styles.serverField}>
            <Input
              id="deleteWorkers"
              label={t('monitoring.codex_inspection_settings_delete_workers_label')}
              error={errors.deleteWorkers}
              type="number"
              min={1}
              step={1}
              value={draft.deleteWorkers}
              onChange={(event) => onFieldChange('deleteWorkers', event.target.value)}
            />
          </div>
          <div className={styles.serverField}>
            <Input
              id="timeout"
              label={t('monitoring.codex_inspection_settings_timeout_label')}
              error={errors.timeout}
              type="number"
              min={1}
              step={100}
              value={draft.timeout}
              onChange={(event) => onFieldChange('timeout', event.target.value)}
            />
          </div>
          <div className={styles.serverField}>
            <Input
              id="retries"
              label={t('monitoring.codex_inspection_settings_retries_label')}
              error={errors.retries}
              type="number"
              min={0}
              step={1}
              value={draft.retries}
              onChange={(event) => onFieldChange('retries', event.target.value)}
            />
          </div>
          <div className={`${styles.serverField} ${styles.serverFieldWide}`}>
            <Input
              id="userAgent"
              label={t('monitoring.codex_inspection_settings_user_agent_label')}
              value={draft.userAgent}
              onChange={(event) => onFieldChange('userAgent', event.target.value)}
            />
          </div>
        </div>
      </details>
    </>
  );
}
