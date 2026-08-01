import { useEffect, useRef, useState } from 'react';
import type { TFunction } from 'i18next';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { IconChevronDown, IconCopy, IconInfo } from '@/components/ui/icons';
import type { CodexInspectionAutoActionMode } from '@/features/monitoring/codexInspection';
import { CodexInspectionAutoActionEditor } from '@/features/monitoring/components/CodexInspectionAutoActionEditor';
import {
  getInspectionUserAgentVisibility,
  type InspectionConfigFieldErrors,
  type SharedInspectionConfigDraft,
  type SharedInspectionConfigField,
} from '@/features/monitoring/model/codexInspectionPresentation';
import {
  DEFAULT_XAI_INSPECTION_MODEL,
  DEFAULT_XAI_INSPECTION_PROMPT,
} from '@/utils/quota/constants';
import { copyToClipboard } from '@/utils/clipboard';
import styles from '../CodexInspectionPage.module.scss';

type InspectionConfigFieldsProps = {
  draft: SharedInspectionConfigDraft;
  errors: InspectionConfigFieldErrors;
  t: TFunction;
  onFieldChange: (field: SharedInspectionConfigField, value: string) => void;
  onXaiInferenceEnabledChange: (enabled: boolean) => void;
  onAutoActionModeChange: (mode: CodexInspectionAutoActionMode) => void;
  onAutoRecoverEnabledChange: (enabled: boolean) => void;
};

// 本地与服务端共享的巡检配置字段。分组:基础规则 → 自动处置 → 高级(默认折叠)。
// 字段 id 与 field 名一致,供概览卡点击后在 Drawer 内定位聚焦。
export function InspectionConfigFields({
  draft,
  errors,
  t,
  onFieldChange,
  onXaiInferenceEnabledChange,
  onAutoActionModeChange,
  onAutoRecoverEnabledChange,
}: InspectionConfigFieldsProps) {
  const [promptCopied, setPromptCopied] = useState(false);
  const advancedSectionRef = useRef<HTMLDetailsElement>(null);
  const includesXai = draft.targetTypes.includes('xai');
  const userAgentVisibility = getInspectionUserAgentVisibility(
    draft.targetTypes,
    draft.xaiInferenceEnabled
  );
  const hasAdvancedErrors = Boolean(
    errors.workers ||
    errors.deleteWorkers ||
    errors.timeout ||
    errors.retries ||
    errors.xaiInferenceModel ||
    errors.xaiInferencePrompt
  );

  useEffect(() => {
    if (hasAdvancedErrors) {
      advancedSectionRef.current?.setAttribute('open', '');
    }
  }, [hasAdvancedErrors]);

  const copyPrompt = async () => {
    const copied = await copyToClipboard(draft.xaiInferencePrompt);
    if (!copied) return;
    setPromptCopied(true);
    window.setTimeout(() => setPromptCopied(false), 1500);
  };

  return (
    <>
      <section
        className={styles.configSection}
        aria-label={t('monitoring.codex_inspection_settings_group_scope')}
      >
        <div className={styles.serverConfigGrid}>
          <div className={`${styles.serverField} ${styles.serverFieldWide}`}>
            <div className="form-group">
              <label className={styles.serverFieldLabel} htmlFor="targetTypes">
                {t('monitoring.codex_inspection_settings_target_type_label')}
              </label>
              <Select
                id="targetTypes"
                value={draft.targetTypes}
                options={[
                  { value: 'codex', label: t('monitoring.codex_inspection_target_codex') },
                  { value: 'xai', label: t('monitoring.codex_inspection_target_xai') },
                  {
                    value: 'codex+xai',
                    label: t('monitoring.codex_inspection_target_codex_xai'),
                  },
                ]}
                onChange={(value) => onFieldChange('targetTypes', value)}
                ariaLabel={t('monitoring.codex_inspection_settings_target_type_label')}
                triggerClassName={styles.configSelectTrigger}
                dropdownClassName={styles.configSelectDropdown}
              />
              <div className="hint">
                {t('monitoring.codex_inspection_settings_target_type_hint')}
              </div>
              {errors.targetTypes ? <div className="error-box">{errors.targetTypes}</div> : null}
            </div>
          </div>
          {includesXai ? (
            <>
              <div
                id="xaiInferenceEnabled"
                className={`${styles.xaiInferenceToggle} ${styles.serverFieldWide}`}
              >
                <div>
                  <strong>
                    {t('monitoring.codex_inspection_settings_xai_inference_enabled_label')}
                  </strong>
                  <span>
                    {t('monitoring.codex_inspection_settings_xai_inference_enabled_hint')}
                  </span>
                </div>
                <ToggleSwitch
                  checked={draft.xaiInferenceEnabled}
                  onChange={onXaiInferenceEnabledChange}
                  ariaLabel={t('monitoring.codex_inspection_settings_xai_inference_enabled_label')}
                />
              </div>
              {draft.xaiInferenceEnabled ? (
                <div className={`${styles.xaiInferenceNotice} ${styles.serverFieldWide}`}>
                  <IconInfo size={17} aria-hidden="true" />
                  <span>{t('monitoring.codex_inspection_settings_xai_inference_notice')}</span>
                </div>
              ) : null}
            </>
          ) : null}
        </div>
      </section>

      <section className={`${styles.configSection} ${styles.configSectionStrategy}`}>
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

      <details ref={advancedSectionRef} className={styles.advancedSection}>
        <summary>
          <span className={styles.advancedSummaryCopy}>
            <span>{t('monitoring.server_codex_inspection_advanced_title')}</span>
            <span className={styles.advancedSummaryHint}>
              {t('monitoring.server_codex_inspection_advanced_hint')}
            </span>
          </span>
          <IconChevronDown className={styles.advancedSummaryChevron} size={15} aria-hidden="true" />
        </summary>
        <div className={styles.advancedBody}>
          <section className={styles.advancedGroup}>
            <h3 className={styles.advancedGroupTitle}>
              <span>{t('monitoring.codex_inspection_settings_group_concurrency')}</span>
            </h3>
            <div className={`${styles.advancedGroupGrid} ${styles.advancedExecutionGrid}`}>
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
            </div>
          </section>

          {userAgentVisibility.codex ? (
            <section className={styles.advancedGroup}>
              <h3 className={styles.advancedGroupTitle}>
                <span>{t('monitoring.codex_inspection_target_codex')}</span>
              </h3>
              <div className={styles.advancedGroupGrid}>
                <div className={`${styles.serverField} ${styles.serverFieldWide}`}>
                  <Input
                    id="userAgent"
                    label={t('monitoring.codex_inspection_settings_provider_user_agent_label')}
                    value={draft.userAgent}
                    onChange={(event) => onFieldChange('userAgent', event.target.value)}
                  />
                </div>
              </div>
            </section>
          ) : null}

          {userAgentVisibility.xaiInference ? (
            <section className={styles.advancedGroup}>
              <h3 className={styles.advancedGroupTitle}>
                <span>{t('monitoring.codex_inspection_probe_source_xai_inference')}</span>
              </h3>
              <div className={styles.advancedGroupGrid}>
                <div className={`${styles.serverField} ${styles.serverFieldWide}`}>
                  <Input
                    id="xaiInferenceUserAgent"
                    label={t('monitoring.codex_inspection_settings_provider_user_agent_label')}
                    value={draft.xaiInferenceUserAgent}
                    onChange={(event) => onFieldChange('xaiInferenceUserAgent', event.target.value)}
                  />
                </div>
                <div className={`${styles.serverField} ${styles.serverFieldWide}`}>
                  <Input
                    id="xaiInferenceModel"
                    list="xaiInspectionRecommendedModels"
                    label={t('monitoring.codex_inspection_settings_provider_model_label')}
                    error={errors.xaiInferenceModel}
                    hint={t('monitoring.codex_inspection_settings_xai_model_hint')}
                    value={draft.xaiInferenceModel}
                    onChange={(event) => onFieldChange('xaiInferenceModel', event.target.value)}
                  />
                  <datalist id="xaiInspectionRecommendedModels">
                    <option value={DEFAULT_XAI_INSPECTION_MODEL} />
                  </datalist>
                </div>
                <div className={`${styles.serverField} ${styles.serverFieldWide}`}>
                  <label className={styles.serverFieldLabel} htmlFor="xaiInferencePrompt">
                    {t('monitoring.codex_inspection_settings_provider_prompt_label')}
                  </label>
                  <textarea
                    id="xaiInferencePrompt"
                    className="input"
                    rows={4}
                    value={draft.xaiInferencePrompt}
                    onChange={(event) => onFieldChange('xaiInferencePrompt', event.target.value)}
                  />
                  <div className={styles.promptFieldFooter}>
                    <span className={styles.promptFieldHint}>
                      {t('monitoring.codex_inspection_settings_xai_prompt_hint')}
                    </span>
                    <div className={styles.promptFieldFooterSide}>
                      <span className={styles.promptCharacterCount}>
                        {draft.xaiInferencePrompt.length}{' '}
                        {t('monitoring.codex_inspection_settings_prompt_characters')}
                      </span>
                      <div className={styles.promptFieldActions}>
                        <button
                          type="button"
                          onClick={() =>
                            onFieldChange('xaiInferencePrompt', DEFAULT_XAI_INSPECTION_PROMPT)
                          }
                        >
                          {t('monitoring.codex_inspection_settings_restore_default_prompt')}
                        </button>
                        <button type="button" onClick={() => void copyPrompt()}>
                          <IconCopy size={13} aria-hidden="true" />
                          {promptCopied
                            ? t('common.copied')
                            : t('monitoring.codex_inspection_settings_copy_prompt')}
                        </button>
                      </div>
                    </div>
                  </div>
                  {errors.xaiInferencePrompt ? (
                    <div className="error-box">{errors.xaiInferencePrompt}</div>
                  ) : null}
                </div>
              </div>
            </section>
          ) : null}
        </div>
      </details>
    </>
  );
}
