import { useId, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { SegmentedTabs } from '@/components/ui/SegmentedTabs';
import { Select } from '@/components/ui/Select';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { CoolingPolicySelect } from '@/components/providers/CoolingPolicySelect';
import {
  DrawerDefinitionList,
  DrawerDlRow,
  DrawerEmptyBox,
  DrawerFormFullRow,
  DrawerFormGrid,
  DrawerIndentGroup,
  DrawerNotice,
  DrawerSectionCard,
  DrawerSettingList,
  DrawerSettingRow,
} from '@/components/common/WorkspaceDrawer';
import {
  IconCode,
  IconCopy,
  IconModelCluster,
  IconRefreshCw,
  IconSatellite,
  IconScrollText,
  IconShield,
  IconSlidersHorizontal,
} from '@/components/ui/icons';
import type { AccountRow } from '@/features/accounts/model/accountRows';
import { getAccountModelExclusionPriority } from '@/features/accounts/model/accountModelRules';
import { getProviderLabel } from '@/features/accounts/model/accountsPagePresentation';
import type { UseAuthFileConfigurationEditorResult } from '@/features/authFiles/hooks/useAuthFileConfigurationEditor';
import {
  AUTH_FILE_WEIGHT_MAX,
  XAI_OFFICIAL_API_BASE_URL,
  getAuthFileConfigurationCapabilities,
  type AuthFileConfigurationErrorKey,
  type XaiRoutingMode,
} from '@/features/authFiles/model/authFileConfiguration';
import { useAuthStore } from '@/stores/useAuthStore';
import styles from '@/features/accounts/AccountsPage.module.scss';

type AccountConfigurationTabProps = {
  row: AccountRow;
  disableControls: boolean;
  editor: UseAuthFileConfigurationEditorResult;
  onCopyText: (text: string) => void | Promise<void>;
  hideInlineActions?: boolean;
};

export function AccountConfigurationTab({
  row,
  disableControls,
  editor,
  onCopyText,
  hideInlineActions = false,
}: AccountConfigurationTabProps) {
  const { t } = useTranslation();
  const exclusionPriority = getAccountModelExclusionPriority(
    useAuthStore((authState) => authState.serverVersion)
  );
  const reloadButtonId = useId();
  const {
    state,
    draft,
    errors,
    dirty,
    canSave,
    rawDataText,
    sharedSourceReadOnly,
    sourceMemberCount,
  } = editor;
  const capabilities = getAuthFileConfigurationCapabilities(state?.providerKey || row.provider, {
    file: state?.authFile ? { ...row.raw, ...state.authFile } : row.raw,
    record: state?.record,
  });
  const providerLabel = getProviderLabel(state?.providerKey || row.provider, t);
  const disabled =
    disableControls ||
    sharedSourceReadOnly ||
    Boolean(state?.error) ||
    state?.saving === true ||
    state?.loading === true;
  // Unknown CPA precedence only blocks the credential exclusion list; every other
  // configuration field stays editable.
  const excludedModelsDisabled = disabled || exclusionPriority === 'unknown';
  const reloadAndRestoreFocus = () => {
    void editor.reload().then(() => {
      if (typeof window === 'undefined') return;
      window.requestAnimationFrame(() => {
        document.getElementById(reloadButtonId)?.focus();
      });
    });
  };
  const fieldError = (key: keyof typeof errors, params?: Record<string, number>) => {
    const errorKey = errors[key] as AuthFileConfigurationErrorKey | undefined;
    return errorKey ? t(errorKey, params) : undefined;
  };

  const xaiRouteItems = useMemo(
    () => [
      { id: 'grok-build' as const, label: t('accounts.config_xai_route_grok') },
      { id: 'official-api' as const, label: t('accounts.config_xai_route_official') },
    ],
    [t]
  );
  const cloakModeOptions = useMemo(
    () => [
      { value: '', label: t('accounts.config_cloak_inherit') },
      { value: 'auto', label: t('ai_providers.claude_cloak_mode_auto') },
      { value: 'always', label: t('ai_providers.claude_cloak_mode_always') },
      { value: 'never', label: t('ai_providers.claude_cloak_mode_never') },
    ],
    [t]
  );

  if (row.runtimeOnly) {
    return (
      <div className={styles.drawerDetailStack}>
        <DrawerSectionCard
          title={t('accounts.detail_runtime_config_unavailable')}
          headingLevel="h3"
        >
          <DrawerEmptyBox>{t('accounts.config_runtime_only_desc')}</DrawerEmptyBox>
        </DrawerSectionCard>
      </div>
    );
  }

  if (!state || (state.loading && !state.record)) {
    return (
      <div className={styles.configurationLoading} role="status" aria-live="polite">
        <span>{t('accounts.config_loading')}</span>
      </div>
    );
  }

  if (!draft || !state.record) {
    return (
      <div className={styles.drawerDetailStack}>
        <DrawerNotice
          tone="danger"
          role="alert"
          title={t('accounts.config_load_failed')}
          action={
            <Button
              variant="secondary"
              size="sm"
              id={reloadButtonId}
              data-account-config-reload="retry"
              onClick={reloadAndRestoreFocus}
            >
              <IconRefreshCw size={14} />
              {t('common.retry')}
            </Button>
          }
        >
          {state.error || t('accounts.config_error_invalid_source')}
        </DrawerNotice>
      </div>
    );
  }

  return (
    <div
      className={styles.configurationStack}
      data-provider={state.providerKey}
      role="region"
      aria-label={t('accounts.detail_tab_config')}
      aria-busy={state.loading}
    >
      {state.error ? (
        <DrawerNotice
          tone="danger"
          role="alert"
          action={
            <Button
              variant="secondary"
              size="sm"
              id={reloadButtonId}
              data-account-config-reload="retry"
              onClick={reloadAndRestoreFocus}
            >
              <IconRefreshCw size={14} />
              {t('common.retry')}
            </Button>
          }
        >
          {state.error}
        </DrawerNotice>
      ) : null}
      {!hideInlineActions ? (
        <div className={styles.configurationToolbar}>
          {dirty ? (
            <span className={styles.configurationDirtyBadge} role="status">
              {t('accounts.config_unsaved')}
            </span>
          ) : null}
          <div className={styles.configurationToolbarActions}>
            <Button
              variant="secondary"
              className={styles.configurationToolbarButton}
              onClick={editor.reset}
              disabled={!dirty || state.saving}
            >
              {t('common.reset')}
            </Button>
            <Button
              className={styles.configurationToolbarButton}
              onClick={() => void editor.save()}
              loading={state.saving}
              disabled={!canSave}
            >
              {t('common.save')}
            </Button>
          </div>
        </div>
      ) : null}

      {sharedSourceReadOnly ? (
        <DrawerNotice tone="warning" role="note">
          {t('accounts.config_shared_source_read_only', { count: sourceMemberCount })}
        </DrawerNotice>
      ) : null}

      <DrawerSectionCard
        variant="default"
        icon={<IconSatellite size={15} />}
        title={t('accounts.config_section_routing')}
        headingLevel="h3"
      >
        <DrawerFormGrid>
          <Input
            label={t('auth_files.prefix_label')}
            value={draft.prefix}
            placeholder={t('accounts.config_prefix_placeholder')}
            hint={t('accounts.config_prefix_hint')}
            disabled={disabled}
            onChange={(event) => editor.updateField('prefix', event.target.value)}
          />
          <Input
            label={t('auth_files.proxy_url_label')}
            value={draft.proxyUrl}
            placeholder={t('auth_files.proxy_url_placeholder')}
            hint={t('accounts.config_proxy_hint')}
            disabled={disabled}
            onChange={(event) => editor.updateField('proxyUrl', event.target.value)}
          />
        </DrawerFormGrid>
      </DrawerSectionCard>

      <DrawerSectionCard
        variant="default"
        icon={<IconSlidersHorizontal size={15} />}
        title={t('accounts.config_section_scheduling')}
        headingLevel="h3"
      >
        <DrawerFormGrid>
          <Input
            label={t('auth_files.priority_label')}
            type="number"
            step="1"
            value={draft.priority}
            error={fieldError('priority')}
            hint={t('auth_files.priority_hint')}
            disabled={disabled}
            onChange={(event) => editor.updateField('priority', event.target.value)}
          />
          <Input
            label={t('accounts.config_weight_label')}
            type="number"
            min="0"
            step="1"
            max={AUTH_FILE_WEIGHT_MAX}
            value={draft.weight}
            error={fieldError('weight', { max: AUTH_FILE_WEIGHT_MAX })}
            hint={t('accounts.config_weight_hint')}
            disabled={disabled}
            onChange={(event) => editor.updateField('weight', event.target.value)}
          />
          <DrawerFormFullRow>
            <Input
              label={t('auth_files.note_label')}
              value={draft.note}
              placeholder={t('auth_files.note_placeholder')}
              disabled={disabled}
              onChange={(event) => editor.updateField('note', event.target.value)}
            />
          </DrawerFormFullRow>
        </DrawerFormGrid>
      </DrawerSectionCard>

      {(capabilities.websockets ||
        capabilities.supportsSiwc ||
        draft.useSiwc ||
        capabilities.xaiRouting ||
        capabilities.claudeCloak) && (
        <DrawerSectionCard
          variant="default"
          icon={<IconShield size={15} />}
          title={t('accounts.config_section_provider', { provider: providerLabel })}
          headingLevel="h3"
        >
          {capabilities.xaiRouting ? (
            <>
              <div className="form-group">
                <label>{t('accounts.config_xai_route_mode')}</label>
                <SegmentedTabs<XaiRoutingMode>
                  variant="joined"
                  size="sm"
                  roleMode="group"
                  responsiveFullWidth={false}
                  items={xaiRouteItems}
                  activeTab={draft.xaiRoutingMode}
                  ariaLabel={t('accounts.config_xai_route_mode')}
                  idBase="account-xai-route"
                  onChange={(mode) => editor.updateField('xaiRoutingMode', mode)}
                  disabled={disabled}
                />
                <div className="hint">
                  {draft.xaiRoutingMode === 'official-api'
                    ? t('accounts.config_xai_route_official_hint')
                    : t('accounts.config_xai_route_grok_hint')}
                </div>
              </div>
              <Input
                label={t('accounts.config_xai_base_url')}
                value={draft.baseUrl}
                placeholder={XAI_OFFICIAL_API_BASE_URL}
                error={fieldError('baseUrl')}
                hint={t('accounts.config_xai_base_url_hint')}
                disabled={disabled}
                onChange={(event) => editor.updateField('baseUrl', event.target.value)}
              />
            </>
          ) : null}

          {capabilities.websockets || capabilities.supportsSiwc || draft.useSiwc ? (
            <DrawerSettingList>
              {capabilities.websockets ? (
                <DrawerSettingRow
                  title={t('auth_files.websockets_label')}
                  description={t('auth_files.websockets_hint')}
                  control={
                    <ToggleSwitch
                      size="sm"
                      checked={draft.websockets}
                      onChange={(value) => editor.updateField('websockets', value)}
                      disabled={disabled}
                      ariaLabel={t('auth_files.websockets_label')}
                    />
                  }
                />
              ) : null}

              {capabilities.supportsSiwc || draft.useSiwc ? (
                <DrawerSettingRow
                  title={t('auth_files.use_siwc_label')}
                  description={
                    !capabilities.canEnableSiwc && !draft.useSiwc
                      ? t('auth_files.use_siwc_requires_bind')
                      : t('auth_files.use_siwc_hint')
                  }
                  control={
                    <ToggleSwitch
                      size="sm"
                      checked={draft.useSiwc}
                      onChange={(value) => editor.updateField('useSiwc', value)}
                      disabled={disabled || (!capabilities.canEnableSiwc && !draft.useSiwc)}
                      ariaLabel={t('auth_files.use_siwc_label')}
                    />
                  }
                />
              ) : null}
            </DrawerSettingList>
          ) : null}

          {capabilities.claudeCloak ? (
            <>
              <div className="form-group">
                <label>{t('ai_providers.claude_cloak_mode_label')}</label>
                <Select
                  value={draft.cloakMode}
                  options={cloakModeOptions}
                  onChange={(value) => editor.updateField('cloakMode', value)}
                  disabled={disabled}
                  ariaLabel={t('ai_providers.claude_cloak_mode_label')}
                />
                {fieldError('cloakMode') ? (
                  <div className="error-box">{fieldError('cloakMode')}</div>
                ) : (
                  <div className="hint">{t('ai_providers.claude_cloak_mode_hint')}</div>
                )}
              </div>
              <DrawerIndentGroup>
                <DrawerSettingList>
                  <DrawerSettingRow
                    title={t('ai_providers.claude_cloak_strict_label')}
                    description={t('ai_providers.claude_cloak_strict_hint')}
                    control={
                      <ToggleSwitch
                        size="sm"
                        checked={draft.cloakStrictMode}
                        onChange={(value) => editor.updateField('cloakStrictMode', value)}
                        disabled={disabled}
                        ariaLabel={t('ai_providers.claude_cloak_strict_label')}
                      />
                    }
                  />
                  <DrawerSettingRow
                    title={t('accounts.config_cloak_cache_user_id')}
                    description={t('accounts.config_cloak_cache_user_id_hint')}
                    control={
                      <ToggleSwitch
                        size="sm"
                        checked={draft.cloakCacheUserId}
                        onChange={(value) => editor.updateField('cloakCacheUserId', value)}
                        disabled={disabled}
                        ariaLabel={t('accounts.config_cloak_cache_user_id')}
                      />
                    }
                  />
                  <DrawerSettingRow
                    title={t('accounts.config_tool_prefix_disabled')}
                    description={t('accounts.config_tool_prefix_disabled_hint')}
                    control={
                      <ToggleSwitch
                        size="sm"
                        checked={draft.toolPrefixDisabled}
                        onChange={(value) => editor.updateField('toolPrefixDisabled', value)}
                        disabled={disabled}
                        ariaLabel={t('accounts.config_tool_prefix_disabled')}
                      />
                    }
                  />
                </DrawerSettingList>
                <div className="form-group">
                  <label>{t('ai_providers.claude_cloak_sensitive_words_label')}</label>
                  <textarea
                    className="input"
                    rows={3}
                    value={draft.cloakSensitiveWordsText}
                    placeholder={t('ai_providers.claude_cloak_sensitive_words_placeholder')}
                    aria-label={t('ai_providers.claude_cloak_sensitive_words_label')}
                    disabled={disabled}
                    onChange={(event) =>
                      editor.updateField('cloakSensitiveWordsText', event.target.value)
                    }
                  />
                  <div className="hint">{t('ai_providers.claude_cloak_sensitive_words_hint')}</div>
                </div>
              </DrawerIndentGroup>
            </>
          ) : null}
        </DrawerSectionCard>
      )}

      <DrawerSectionCard
        variant="default"
        icon={<IconModelCluster size={15} />}
        title={t('accounts.config_section_models')}
        headingLevel="h3"
      >
        <div className="form-group">
          <label>{t('auth_files.excluded_models_label')}</label>
          <textarea
            className="input"
            rows={5}
            value={draft.excludedModelsText}
            placeholder={t('auth_files.excluded_models_placeholder')}
            aria-label={t('auth_files.excluded_models_label')}
            disabled={excludedModelsDisabled}
            onChange={(event) => editor.updateField('excludedModelsText', event.target.value)}
          />
          <div className="hint">
            {t(
              sharedSourceReadOnly
                ? 'accounts.config_excluded_models_shared_hint'
                : 'accounts.config_excluded_models_hint'
            )}
          </div>
          {!sharedSourceReadOnly && exclusionPriority !== 'merge' ? (
            <div className="hint" role="note">
              {t(
                exclusionPriority === 'override'
                  ? 'accounts.config_excluded_models_override_hint'
                  : 'accounts.config_excluded_models_precedence_unknown_hint'
              )}
            </div>
          ) : null}
        </div>
      </DrawerSectionCard>

      <DrawerSectionCard
        variant="default"
        icon={<IconScrollText size={15} />}
        title={t('accounts.config_section_advanced')}
        headingLevel="h3"
      >
        <DrawerFormGrid>
          <CoolingPolicySelect
            value={draft.disableCooling}
            onChange={(value) => editor.updateField('disableCooling', value)}
            translationPrefix="auth_files"
            disabled={disabled}
            id="account-cooling-policy"
          />
          <Input
            label={t('accounts.config_request_retry_label')}
            type="number"
            min="0"
            step="1"
            value={draft.requestRetry}
            error={fieldError('requestRetry')}
            hint={t('accounts.config_request_retry_hint')}
            disabled={disabled}
            onChange={(event) => editor.updateField('requestRetry', event.target.value)}
          />
        </DrawerFormGrid>
        <div className="form-group">
          <label>{t('auth_files.headers_label')}</label>
          <textarea
            className={`input ${errors.headersText ? styles.configurationTextareaInvalid : ''}`}
            rows={6}
            value={draft.headersText}
            placeholder={t('auth_files.headers_placeholder')}
            aria-label={t('auth_files.headers_label')}
            aria-invalid={Boolean(errors.headersText)}
            disabled={disabled}
            onChange={(event) => editor.updateField('headersText', event.target.value)}
          />
          {fieldError('headersText') ? (
            <div className="error-box">{fieldError('headersText')}</div>
          ) : (
            <div className="hint">{t('auth_files.headers_hint')}</div>
          )}
        </div>
      </DrawerSectionCard>

      <details className={styles.configurationRawDataDetails}>
        <summary>
          <span className={styles.configurationRawDataSummaryLabel}>
            <IconCode size={15} aria-hidden="true" />
            {t('accounts.config_view_raw_data')}
          </span>
          <span className={styles.configurationRawDataBadge}>
            {t('accounts.config_raw_data_redacted')}
          </span>
        </summary>
        <div className={styles.configurationRawDataBody}>
          <DrawerDefinitionList>
            <DrawerDlRow label={t('auth_files.paste_file_name_label')} mono>
              {state.fileName}
            </DrawerDlRow>
            <DrawerDlRow label={t('accounts.col_provider')} mono>
              {state.providerKey || row.provider}
            </DrawerDlRow>
            <DrawerDlRow label={t('accounts.detail_auth_index')} mono muted={!row.authIndex}>
              {row.authIndex || '-'}
            </DrawerDlRow>
          </DrawerDefinitionList>
          <div className={styles.configurationRawDataHeader}>
            <p>{t('accounts.config_raw_data_hint')}</p>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => void onCopyText(rawDataText)}
              disabled={!rawDataText}
            >
              <IconCopy size={14} />
              {t('common.copy')}
            </Button>
          </div>
          <pre
            className={styles.configurationRawData}
            aria-label={t('accounts.config_view_raw_data')}
            tabIndex={0}
          >
            {rawDataText}
          </pre>
        </div>
      </details>
    </div>
  );
}
