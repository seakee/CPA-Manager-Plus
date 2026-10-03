import { useId, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { SegmentedTabs } from '@/components/ui/SegmentedTabs';
import { Select } from '@/components/ui/Select';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { CoolingPolicySelect } from '@/components/providers/CoolingPolicySelect';
import { IconCode, IconCopy, IconRefreshCw } from '@/components/ui/icons';
import type { AccountRow } from '@/features/accounts/model/accountRows';
import { getProviderLabel } from '@/features/accounts/model/accountsPagePresentation';
import type { UseAuthFileConfigurationEditorResult } from '@/features/authFiles/hooks/useAuthFileConfigurationEditor';
import {
  AUTH_FILE_WEIGHT_MAX,
  XAI_OFFICIAL_API_BASE_URL,
  getAuthFileConfigurationCapabilities,
  type AuthFileConfigurationErrorKey,
  type XaiRoutingMode,
} from '@/features/authFiles/model/authFileConfiguration';
import styles from '@/features/accounts/AccountsPage.module.scss';

type AccountConfigurationTabProps = {
  row: AccountRow;
  disableControls: boolean;
  editor: UseAuthFileConfigurationEditorResult;
  onCopyText: (text: string) => void | Promise<void>;
};

export function AccountConfigurationTab({
  row,
  disableControls,
  editor,
  onCopyText,
}: AccountConfigurationTabProps) {
  const { t } = useTranslation();
  const reloadButtonId = useId();
  const { state, draft, errors, rawDataText, sharedSourceReadOnly, sourceMemberCount } = editor;
  const capabilities = getAuthFileConfigurationCapabilities(state?.providerKey || row.provider);
  const providerLabel = getProviderLabel(state?.providerKey || row.provider, t);
  const disabled = disableControls || sharedSourceReadOnly || state?.saving === true;
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
        <section className={styles.drawerSection}>
          <div className={styles.credentialDefaultState}>
            <strong>{t('accounts.detail_runtime_config_unavailable')}</strong>
            <p>{t('accounts.config_runtime_only_desc')}</p>
          </div>
        </section>
      </div>
    );
  }

  if (!state || state.loading) {
    return (
      <div className={styles.configurationLoading} role="status" aria-live="polite">
        <span>{t('accounts.config_loading')}</span>
      </div>
    );
  }

  if (state.error || !draft || !state.record) {
    return (
      <div className={styles.drawerDetailStack}>
        <section className={styles.drawerSection}>
          <div className={styles.configurationLoadError} role="alert">
            <strong>{t('accounts.config_load_failed')}</strong>
            <p>{state.error || t('accounts.config_error_invalid_source')}</p>
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
          </div>
        </section>
      </div>
    );
  }

  return (
    <div
      className={styles.configurationStack}
      data-provider={state.providerKey}
      role="region"
      aria-label={t('accounts.detail_tab_config')}
    >
      {/* Save / discard live in the drawer footer save pill (shared with the Models tab). */}
      {sharedSourceReadOnly ? (
        <div className={styles.configurationReadOnlyNotice} role="note">
          {t('accounts.config_shared_source_read_only', { count: sourceMemberCount })}
        </div>
      ) : null}

      <section className={styles.configurationSection}>
        <h3 className={styles.configurationSectionTitle}>{t('accounts.config_section_routing')}</h3>
        <div className={styles.configurationFieldGrid}>
          <Input
            label={t('auth_files.prefix_label')}
            value={draft.prefix}
            placeholder={t('accounts.config_prefix_placeholder')}
            hint={t('accounts.config_prefix_hint')}
            disabled={disabled}
            onChange={(event) => editor.updateField('prefix', event.target.value)}
          />
          <div className={styles.configurationFieldFull}>
            <Input
              label={t('auth_files.proxy_url_label')}
              value={draft.proxyUrl}
              placeholder={t('auth_files.proxy_url_placeholder')}
              hint={t('accounts.config_proxy_hint')}
              disabled={disabled}
              onChange={(event) => editor.updateField('proxyUrl', event.target.value)}
            />
          </div>
        </div>
      </section>

      <section className={styles.configurationSection}>
        <h3 className={styles.configurationSectionTitle}>
          {t('accounts.config_section_scheduling')}
        </h3>
        <div className={styles.configurationFieldGrid}>
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
          <div className={styles.configurationFieldFull}>
            <Input
              label={t('auth_files.note_label')}
              value={draft.note}
              placeholder={t('auth_files.note_placeholder')}
              disabled={disabled}
              onChange={(event) => editor.updateField('note', event.target.value)}
            />
          </div>
        </div>
      </section>

      {(capabilities.websockets || capabilities.xaiRouting || capabilities.claudeCloak) && (
        <section className={styles.configurationSection}>
          <h3 className={styles.configurationSectionTitle}>
            {t('accounts.config_section_provider', { provider: providerLabel })}
          </h3>

          {capabilities.xaiRouting ? (
            <div className={styles.configurationProviderGroup}>
              <div className={styles.configurationFieldLabel}>
                {t('accounts.config_xai_route_mode')}
              </div>
              <SegmentedTabs<XaiRoutingMode>
                items={xaiRouteItems}
                activeTab={draft.xaiRoutingMode}
                ariaLabel={t('accounts.config_xai_route_mode')}
                idBase="account-xai-route"
                onChange={(mode) => editor.updateField('xaiRoutingMode', mode)}
                disabled={disabled}
                fullWidth
                equalWidth
              />
              <p className={styles.configurationFieldHint}>
                {draft.xaiRoutingMode === 'official-api'
                  ? t('accounts.config_xai_route_official_hint')
                  : t('accounts.config_xai_route_grok_hint')}
              </p>
              <Input
                label={t('accounts.config_xai_base_url')}
                value={draft.baseUrl}
                placeholder={XAI_OFFICIAL_API_BASE_URL}
                error={fieldError('baseUrl')}
                hint={t('accounts.config_xai_base_url_hint')}
                disabled={disabled}
                onChange={(event) => editor.updateField('baseUrl', event.target.value)}
              />
            </div>
          ) : null}

          {capabilities.websockets ? (
            <div className={styles.configurationToggleRow}>
              <div>
                <strong>{t('auth_files.websockets_label')}</strong>
                <p>{t('auth_files.websockets_hint')}</p>
              </div>
              <ToggleSwitch
                checked={draft.websockets}
                onChange={(value) => editor.updateField('websockets', value)}
                disabled={disabled}
                ariaLabel={t('auth_files.websockets_label')}
              />
            </div>
          ) : null}

          {capabilities.claudeCloak ? (
            <div className={styles.configurationProviderGroup}>
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
              <div className={styles.configurationToggleRow}>
                <div>
                  <strong>{t('ai_providers.claude_cloak_strict_label')}</strong>
                  <p>{t('ai_providers.claude_cloak_strict_hint')}</p>
                </div>
                <ToggleSwitch
                  checked={draft.cloakStrictMode}
                  onChange={(value) => editor.updateField('cloakStrictMode', value)}
                  disabled={disabled}
                  ariaLabel={t('ai_providers.claude_cloak_strict_label')}
                />
              </div>
              <div className="form-group">
                <label>{t('ai_providers.claude_cloak_sensitive_words_label')}</label>
                <textarea
                  className="input"
                  rows={4}
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
              <div className={styles.configurationToggleRow}>
                <div>
                  <strong>{t('accounts.config_cloak_cache_user_id')}</strong>
                  <p>{t('accounts.config_cloak_cache_user_id_hint')}</p>
                </div>
                <ToggleSwitch
                  checked={draft.cloakCacheUserId}
                  onChange={(value) => editor.updateField('cloakCacheUserId', value)}
                  disabled={disabled}
                  ariaLabel={t('accounts.config_cloak_cache_user_id')}
                />
              </div>
              <div className={styles.configurationToggleRow}>
                <div>
                  <strong>{t('accounts.config_tool_prefix_disabled')}</strong>
                  <p>{t('accounts.config_tool_prefix_disabled_hint')}</p>
                </div>
                <ToggleSwitch
                  checked={draft.toolPrefixDisabled}
                  onChange={(value) => editor.updateField('toolPrefixDisabled', value)}
                  disabled={disabled}
                  ariaLabel={t('accounts.config_tool_prefix_disabled')}
                />
              </div>
            </div>
          ) : null}
        </section>
      )}

      <section className={styles.configurationSection}>
        <h3 className={styles.configurationSectionTitle}>{t('accounts.config_section_models')}</h3>
        <div className="form-group">
          <label>{t('auth_files.excluded_models_label')}</label>
          <textarea
            className="input"
            rows={5}
            value={draft.excludedModelsText}
            placeholder={t('auth_files.excluded_models_placeholder')}
            aria-label={t('auth_files.excluded_models_label')}
            disabled={disabled}
            onChange={(event) => editor.updateField('excludedModelsText', event.target.value)}
          />
          <div className="hint">
            {t(
              sharedSourceReadOnly
                ? 'accounts.config_excluded_models_shared_hint'
                : 'accounts.config_excluded_models_hint'
            )}
          </div>
        </div>
      </section>

      <section className={styles.configurationSection}>
        <h3 className={styles.configurationSectionTitle}>
          {t('accounts.config_section_advanced')}
        </h3>
        <CoolingPolicySelect
          value={draft.disableCooling}
          onChange={(value) => editor.updateField('disableCooling', value)}
          translationPrefix="auth_files"
          disabled={disabled}
          id="account-cooling-policy"
        />
        <div className={styles.configurationFieldGrid}>
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
        </div>
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
      </section>

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
          <dl className={styles.configurationTechnicalMeta}>
            <div>
              <dt>{t('auth_files.paste_file_name_label')}</dt>
              <dd>{state.fileName}</dd>
            </div>
            <div>
              <dt>{t('accounts.col_provider')}</dt>
              <dd>{state.providerKey || row.provider}</dd>
            </div>
            <div>
              <dt>{t('accounts.detail_auth_index')}</dt>
              <dd>{row.authIndex || '-'}</dd>
            </div>
          </dl>
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
