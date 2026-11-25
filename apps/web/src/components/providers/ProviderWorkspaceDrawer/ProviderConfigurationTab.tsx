import { useState, type Dispatch, type SetStateAction } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { HeaderInputList } from '@/components/ui/HeaderInputList';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import {
  IconEye,
  IconEyeOff,
  IconKey,
  IconPlus,
  IconSatellite,
  IconScrollText,
  IconShield,
  IconSlidersHorizontal,
  IconTrash2,
} from '@/components/ui/icons';
import { parseSensitiveWordsText, type ProviderDrawerFormState } from './useProviderDrawerEditor';
import type { CloakConfig } from '@/types';
import {
  MAX_CREDENTIAL_WEIGHT,
  getCredentialWeightError,
  toCredentialWeightInputValue,
} from '@/utils/credentialWeight';
import { preservePersistedOpenAIKeyEntry, updateOpenAIApiKey } from '@/utils/openAIKeyEntries';
import { CoolingPolicySelect } from '../CoolingPolicySelect';
import { CredentialWeightInput } from '../CredentialWeightInput';
import type { ProviderKind } from '../ProviderTable/rowData';
import { buildApiKeyEntry, resolveClaudeFingerprintSelection } from '../utils';
import styles from './ProviderWorkspaceDrawer.module.scss';

export interface ProviderConfigurationTabProps {
  mode: 'view-edit' | 'create';
  activeKind: ProviderKind;
  form: ProviderDrawerFormState;
  setForm: Dispatch<SetStateAction<ProviderDrawerFormState>>;
  saving: boolean;
  actionsDisabled: boolean;
  v8AliasSupported?: boolean;
  onOpenAIKeyCountChange?: (action: 'add' | 'remove', index?: number) => void;
}

const cleanLabelText = (text: string): string =>
  text.replace(/\s*[（(](?:可选|必填|optional|required)[)）]/gi, '').replace(/[:：]\s*$/, '');

export function ProviderConfigurationTab({
  activeKind,
  form,
  setForm,
  saving,
  actionsDisabled,
  v8AliasSupported,
  onOpenAIKeyCountChange,
}: ProviderConfigurationTabProps) {
  const { t } = useTranslation();
  const [singleKeyRevealed, setSingleKeyRevealed] = useState(false);
  const [revealedOpenAIKeys, setRevealedOpenAIKeys] = useState<Set<number>>(() => new Set());

  const controlsDisabled = actionsDisabled || saving;
  const isOpenAI = activeKind === 'openai';
  const aliasInputDisabled = controlsDisabled || (!isOpenAI && v8AliasSupported === false);
  const baseUrlRequired =
    activeKind === 'openai' ||
    activeKind === 'claude' ||
    activeKind === 'codex' ||
    activeKind === 'xai' ||
    activeKind === 'vertex';

  const rawBaseUrlLabel =
    activeKind === 'openai'
      ? t('ai_providers.openai_add_modal_url_label')
      : activeKind === 'claude'
        ? t('ai_providers.claude_add_modal_url_label')
        : activeKind === 'xai'
          ? t('ai_providers.xai_add_modal_url_label')
          : activeKind === 'codex'
            ? t('ai_providers.codex_add_modal_url_label')
            : activeKind === 'vertex'
              ? t('ai_providers.vertex_add_modal_url_label')
              : t('ai_providers.gemini_base_url_label');
  const baseUrlLabel = cleanLabelText(rawBaseUrlLabel);
  const baseUrlPlaceholder =
    activeKind === 'openai'
      ? t('ai_providers.openai_add_modal_url_placeholder')
      : activeKind === 'claude'
        ? t('ai_providers.claude_add_modal_url_placeholder')
        : activeKind === 'xai'
          ? t('ai_providers.xai_add_modal_url_placeholder')
          : activeKind === 'codex'
            ? t('ai_providers.codex_add_modal_url_placeholder')
            : activeKind === 'vertex'
              ? t('ai_providers.vertex_add_modal_url_placeholder')
              : t('ai_providers.gemini_base_url_placeholder');

  const effectiveCloakEnabled = Boolean(form.cloak);
  const cloakMode = form.cloak?.mode || 'auto';
  const cloakStrict = Boolean(form.cloak?.strictMode);
  const cloakCacheUserId = Boolean(form.cloak?.cacheUserId);
  const cloakSensitiveWordsText = form.cloakSensitiveWordsInput;

  const updateCloak = (patch: Partial<CloakConfig>) => {
    setForm((prev) => ({
      ...prev,
      cloak: {
        ...(prev.cloak ?? {}),
        mode: prev.cloak?.mode || 'auto',
        strictMode: prev.cloak?.strictMode ?? false,
        cacheUserId: prev.cloak?.cacheUserId ?? false,
        sensitiveWords: prev.cloak?.sensitiveWords,
        ...patch,
      },
    }));
  };

  const toggleOpenAIKeyReveal = (index: number) => {
    setRevealedOpenAIKeys((prev) => {
      const next = new Set(prev);
      if (next.has(index)) next.delete(index);
      else next.add(index);
      return next;
    });
  };

  return (
    <div className={styles.drawerTabPanel}>
      {/* Card 1: 基础与路由 */}
      <section className={styles.sectionCard}>
        <div className={styles.sectionHeader}>
          <div className={styles.sectionHeading}>
            <IconSatellite size={15} />
            <h4>{t('ai_providers.config_section_basic_routing', { defaultValue: '基础与路由' })}</h4>
          </div>
        </div>

        <div className={styles.formGridTwo}>
          <Input
            label={
              isOpenAI
                ? `${t('ai_providers.name_label', { defaultValue: '名称 / 别名' })} *`
                : t('ai_providers.name_label', { defaultValue: '名称 / 别名' })
            }
            placeholder={
              isOpenAI
                ? t('ai_providers.openai_name_placeholder', {
                    defaultValue: '例如 openrouter / 主力网关',
                  })
                : t('ai_providers.alias_placeholder_optional', {
                    defaultValue: '例如 Claude 主力池（留空自动生成）',
                  })
            }
            value={form.name}
            onChange={(e) => setForm((prev) => ({ ...prev, name: e.target.value }))}
            disabled={aliasInputDisabled}
            hint={
              !isOpenAI && v8AliasSupported === false
                ? t('ai_providers.v8_alias_unsupported_hint', {
                    defaultValue: '当前后端版本暂未启用 v8 别名配置接口，已锁定别名输入。',
                  })
                : undefined
            }
          />

          <Input
            label={cleanLabelText(t('ai_providers.prefix_label'))}
            placeholder={t('ai_providers.prefix_placeholder')}
            hint={t('ai_providers.prefix_hint')}
            value={form.prefix}
            onChange={(e) => setForm((prev) => ({ ...prev, prefix: e.target.value }))}
            disabled={controlsDisabled}
          />

          <div className={styles.formFullRow}>
            <Input
              label={baseUrlRequired ? `${baseUrlLabel} *` : baseUrlLabel}
              placeholder={baseUrlPlaceholder}
              value={form.baseUrl}
              onChange={(e) => setForm((prev) => ({ ...prev, baseUrl: e.target.value }))}
              disabled={controlsDisabled}
            />
          </div>

          {!isOpenAI && (
            <div className={styles.formFullRow}>
              <Input
                label={cleanLabelText(t('ai_providers.proxy_url_label', { defaultValue: '代理地址' }))}
                placeholder={t('ai_providers.proxy_url_placeholder', {
                  defaultValue: 'socks5://... 或 http://...',
                })}
                value={form.proxyUrl}
                onChange={(e) => setForm((prev) => ({ ...prev, proxyUrl: e.target.value }))}
                disabled={controlsDisabled}
              />
            </div>
          )}
        </div>
      </section>
      {/* Card 2: 鉴权密钥 */}
      <section className={styles.sectionCard}>
        <div className={styles.sectionHeader}>
          <div className={styles.sectionHeading}>
            <IconKey size={15} />
            <h4>{t('ai_providers.config_section_auth_keys', { defaultValue: '鉴权密钥' })}</h4>
          </div>
          {isOpenAI && (
            <Button
              variant="secondary"
              size="xs"
              disabled={controlsDisabled}
              onClick={() => {
                const prevCount = form.apiKeyEntries.length;
                setForm((prev) => ({
                  ...prev,
                  apiKeyEntries: [...prev.apiKeyEntries, buildApiKeyEntry()],
                }));
                onOpenAIKeyCountChange?.('add', prevCount);
              }}
            >
              <IconPlus size={13} />
              {t('ai_providers.openai_keys_add_btn')}
            </Button>
          )}
        </div>

        {isOpenAI ? (
          <div className={styles.keyList}>
            {form.apiKeyEntries.map((entry, idx) => {
              const weightErr = getCredentialWeightError(entry.weight);
              return (
                <div key={idx} className={styles.keyEntryEditorCard}>
                  <div className={styles.keyEntryEditorHeader}>
                    <span className={styles.keyIndexBadge}>#{idx + 1}</span>
                    <div className={styles.sectionMetaActions}>
                      <Button
                        variant="ghost"
                        size="xs"
                        disabled={controlsDisabled || form.apiKeyEntries.length <= 1}
                        onClick={() => {
                          const prevCount = form.apiKeyEntries.length;
                          setForm((prev) => ({
                            ...prev,
                            apiKeyEntries: prev.apiKeyEntries.filter((_, i) => i !== idx),
                          }));
                          onOpenAIKeyCountChange?.('remove', idx);
                          void prevCount;
                        }}
                        aria-label={t('common.delete')}
                      >
                        <IconTrash2 size={13} />
                      </Button>
                    </div>
                  </div>

                  <div className={styles.keyEntryEditorGrid}>
                    <Input
                      label={t('common.api_key', { defaultValue: 'API Key' })}
                      type={revealedOpenAIKeys.has(idx) ? 'text' : 'password'}
                      autoComplete="off"
                      className={styles.monoInput}
                      placeholder={t('ai_providers.openai_key_placeholder')}
                      value={entry.apiKey}
                      onChange={(e) =>
                        setForm((prev) => ({
                          ...prev,
                          apiKeyEntries: prev.apiKeyEntries.map((item, i) =>
                            i === idx ? updateOpenAIApiKey(item, e.target.value) : item
                          ),
                        }))
                      }
                      disabled={controlsDisabled}
                      rightElement={
                        entry.apiKey ? (
                          <button
                            type="button"
                            className={styles.inputEyeBtn}
                            onClick={() => toggleOpenAIKeyReveal(idx)}
                            disabled={controlsDisabled}
                            aria-label={
                              revealedOpenAIKeys.has(idx)
                                ? t('common.hide', { defaultValue: '隐藏' })
                                : t('common.reveal', { defaultValue: '显示明文' })
                            }
                          >
                            {revealedOpenAIKeys.has(idx) ? (
                              <IconEyeOff size={14} />
                            ) : (
                              <IconEye size={14} />
                            )}
                          </button>
                        ) : undefined
                      }
                    />
                    <Input
                      label={t('ai_providers.weight_label')}
                      type="text"
                      inputMode="text"
                      value={entry.weight ?? ''}
                      onChange={(e) =>
                        setForm((prev) => ({
                          ...prev,
                          apiKeyEntries: prev.apiKeyEntries.map((item, i) =>
                            i === idx
                              ? preservePersistedOpenAIKeyEntry(item, {
                                  ...item,
                                  weight: toCredentialWeightInputValue(e.target.value),
                                })
                              : item
                          ),
                        }))
                      }
                      disabled={controlsDisabled}
                      error={
                        weightErr
                          ? t(
                              weightErr === 'maximum'
                                ? 'ai_providers.weight_error_maximum'
                                : 'ai_providers.weight_error_integer',
                              { max: MAX_CREDENTIAL_WEIGHT.toLocaleString() }
                            )
                          : undefined
                      }
                    />
                    <Input
                      label={t('ai_providers.proxy_url_label', { defaultValue: '代理' })}
                      placeholder={t('ai_providers.openai_proxy_placeholder')}
                      value={entry.proxyUrl ?? ''}
                      onChange={(e) =>
                        setForm((prev) => ({
                          ...prev,
                          apiKeyEntries: prev.apiKeyEntries.map((item, i) =>
                            i === idx
                              ? preservePersistedOpenAIKeyEntry(item, {
                                  ...item,
                                  proxyUrl: e.target.value,
                                })
                              : item
                          ),
                        }))
                      }
                      disabled={controlsDisabled}
                    />
                  </div>

                </div>
              );
            })}
          </div>
        ) : (
          <div className={styles.formGridTwo}>
            <div className={styles.formFullRow}>
              <Input
                label={cleanLabelText(t('ai_providers.api_key_label', { defaultValue: 'API Key' }))}
                type={singleKeyRevealed ? 'text' : 'password'}
                autoComplete="off"
                className={styles.monoInput}
                placeholder={
                  activeKind === 'vertex'
                    ? t('ai_providers.vertex_add_modal_key_placeholder')
                    : t('ai_providers.api_key_placeholder', {
                        defaultValue: '输入 API Key（无密钥端点或 auth-index 模式可留空）',
                      })
                }
                value={form.apiKey}
                onChange={(e) => setForm((prev) => ({ ...prev, apiKey: e.target.value }))}
                disabled={controlsDisabled}
                rightElement={
                  form.apiKey ? (
                    <button
                      type="button"
                      className={styles.inputEyeBtn}
                      onClick={() => setSingleKeyRevealed((prev) => !prev)}
                      disabled={controlsDisabled}
                      aria-label={
                        singleKeyRevealed
                          ? t('common.hide', { defaultValue: '隐藏' })
                          : t('common.reveal', { defaultValue: '显示明文' })
                      }
                    >
                      {singleKeyRevealed ? <IconEyeOff size={14} /> : <IconEye size={14} />}
                    </button>
                  ) : undefined
                }
              />
            </div>

            {form.authIndex.trim() && (
              <div className={styles.formFullRow}>
                <span className={styles.authIndexChip}>
                  Auth Index: <code className={styles.fieldValueMono}>{form.authIndex.trim()}</code>
                </span>
              </div>
            )}
          </div>
        )}
      </section>

      {/* Card 3: 调度与冷却策略 */}
      <section className={styles.sectionCard}>
        <div className={styles.sectionHeader}>
          <div className={styles.sectionHeading}>
            <IconSlidersHorizontal size={15} />
            <h4>
              {t('ai_providers.config_section_scheduling_cooling', {
                defaultValue: '调度与冷却策略',
              })}
            </h4>
          </div>
        </div>

        <div className={styles.formGridTwo}>
          <Input
            label={cleanLabelText(t('ai_providers.priority_label'))}
            hint={t('ai_providers.priority_hint')}
            type="number"
            step={1}
            placeholder={t('ai_providers.priority_placeholder')}
            value={form.priority ?? ''}
            onChange={(e) => {
              const raw = e.target.value;
              const parsed = raw.trim() === '' ? undefined : Number(raw);
              setForm((prev) => ({
                ...prev,
                priority: parsed !== undefined && Number.isFinite(parsed) ? parsed : undefined,
              }));
            }}
            disabled={controlsDisabled}
          />

          {!isOpenAI && (
            <CredentialWeightInput
              value={form.weight}
              onChange={(weight) => setForm((prev) => ({ ...prev, weight }))}
              disabled={controlsDisabled}
            />
          )}

          {isOpenAI && (
            <Input
              label={t('ai_providers.openai_default_test_model_label', {
                defaultValue: '默认测试模型',
              })}
              placeholder={t('ai_providers.openai_test_model_placeholder')}
              value={form.testModel}
              onChange={(e) => setForm((prev) => ({ ...prev, testModel: e.target.value }))}
              disabled={controlsDisabled}
            />
          )}

          <div className={styles.formFullRow}>
            <CoolingPolicySelect
              value={form.disableCooling}
              onChange={(disableCooling) => setForm((prev) => ({ ...prev, disableCooling }))}
              disabled={controlsDisabled}
              legacyProviderSupported={activeKind !== 'vertex'}
              tooltipHint
            />
          </div>
        </div>
      </section>

      {/* Card 4: 协议高级特性 (codex / xai / claude) */}
      {(activeKind === 'codex' || activeKind === 'xai' || activeKind === 'claude') && (
        <section className={styles.sectionCard}>
          <div className={styles.sectionHeader}>
            <div className={styles.sectionHeading}>
              <IconShield size={15} />
              <h4>
                {t('ai_providers.config_section_protocol_advanced', {
                  defaultValue: '协议高级特性',
                })}
              </h4>
            </div>
          </div>

          <div className={styles.settingList}>
            {(activeKind === 'codex' || activeKind === 'xai') && (
              <div className={styles.settingRow}>
                <div className={styles.toggleRowMeta}>
                  <strong>{cleanLabelText(t('ai_providers.codex_websockets_label'))}</strong>
                  <span>{t('ai_providers.codex_websockets_hint')}</span>
                </div>
                <ToggleSwitch
                  size="sm"
                  checked={form.websockets}
                  onChange={(websockets) => setForm((prev) => ({ ...prev, websockets }))}
                  disabled={controlsDisabled}
                  ariaLabel={cleanLabelText(t('ai_providers.codex_websockets_label'))}
                />
              </div>
            )}

            {activeKind === 'claude' && (
              <>
                <div className={styles.settingRow}>
                  <div className={styles.toggleRowMeta}>
                    <strong>{cleanLabelText(t('ai_providers.claude_request_fingerprint_label'))}</strong>
                    <span>
                      {t('ai_providers.claude_request_fingerprint_hint', {
                        defaultValue: '模拟特定官方客户端请求头特征。',
                      })}
                    </span>
                  </div>
                  <div className={styles.settingControlBox}>
                    <Select
                      value={form.fingerprintProfile ?? ''}
                      onChange={(nextValue) =>
                        setForm((prev) => ({
                          ...prev,
                          fingerprintProfile: resolveClaudeFingerprintSelection(
                            prev.fingerprintProfile,
                            nextValue
                          ),
                        }))
                      }
                      options={[
                        {
                          value: '',
                          label: t('ai_providers.claude_request_fingerprint_default'),
                        },
                        {
                          value: 'claude-code-cli',
                          label: t('ai_providers.claude_request_fingerprint_claude_code_cli'),
                        },
                      ]}
                      disabled={controlsDisabled}
                      ariaLabel={cleanLabelText(t('ai_providers.claude_request_fingerprint_label'))}
                    />
                  </div>
                </div>

                <div className={styles.settingRow}>
                  <div className={styles.toggleRowMeta}>
                    <strong>{cleanLabelText(t('ai_providers.rebuild_mid_system_message_label'))}</strong>
                    <span>{t('ai_providers.rebuild_mid_system_message_hint')}</span>
                  </div>
                  <ToggleSwitch
                    size="sm"
                    checked={form.rebuildMidSystemMessage}
                    onChange={(rebuildMidSystemMessage) =>
                      setForm((prev) => ({ ...prev, rebuildMidSystemMessage }))
                    }
                    disabled={controlsDisabled}
                    ariaLabel={cleanLabelText(t('ai_providers.rebuild_mid_system_message_label'))}
                  />
                </div>

                <div className={styles.settingRow}>
                  <div className={styles.toggleRowMeta}>
                    <strong>{cleanLabelText(t('ai_providers.claude_cloak_title'))}</strong>
                    <span>{t('ai_providers.claude_cloak_hint')}</span>
                  </div>
                  <ToggleSwitch
                    size="sm"
                    checked={effectiveCloakEnabled}
                    onChange={(enabled) =>
                      setForm((prev) => ({
                        ...prev,
                        cloak: enabled
                          ? prev.cloak ?? { mode: 'auto', strictMode: false, cacheUserId: false }
                          : undefined,
                        cloakSensitiveWordsInput: enabled
                          ? prev.cloakSensitiveWordsInput ||
                            (prev.cloak?.sensitiveWords ?? []).join('\n')
                          : '',
                      }))
                    }
                    disabled={controlsDisabled}
                    ariaLabel={cleanLabelText(t('ai_providers.claude_cloak_title'))}
                  />
                </div>

                {effectiveCloakEnabled && (
                  <div className={styles.cloakGroup}>
                    <div className={styles.settingRow}>
                      <div className={styles.toggleRowMeta}>
                        <strong>{cleanLabelText(t('ai_providers.claude_cloak_mode_label'))}</strong>
                      </div>
                      <div className={styles.settingControlBox}>
                        <Select
                          value={cloakMode}
                          onChange={(nextMode) => updateCloak({ mode: nextMode })}
                          options={[
                            { value: 'auto', label: t('ai_providers.claude_cloak_mode_auto') },
                            { value: 'always', label: t('ai_providers.claude_cloak_mode_always') },
                            { value: 'never', label: t('ai_providers.claude_cloak_mode_never') },
                          ]}
                          disabled={controlsDisabled}
                          ariaLabel={cleanLabelText(t('ai_providers.claude_cloak_mode_label'))}
                        />
                      </div>
                    </div>

                    <div className={styles.settingRow}>
                      <div className={styles.toggleRowMeta}>
                        <strong>{cleanLabelText(t('ai_providers.claude_cloak_strict_label'))}</strong>
                        <span>{t('ai_providers.claude_cloak_strict_hint')}</span>
                      </div>
                      <ToggleSwitch
                        size="sm"
                        checked={cloakStrict}
                        onChange={(strictMode) => updateCloak({ strictMode })}
                        disabled={controlsDisabled}
                        ariaLabel={cleanLabelText(t('ai_providers.claude_cloak_strict_label'))}
                      />
                    </div>

                    <div className={styles.settingRow}>
                      <div className={styles.toggleRowMeta}>
                        <strong>
                          {t('ai_providers.claude_cloak_cache_user_id', {
                            defaultValue: '缓存伪装用户 ID',
                          })}
                        </strong>
                        <span>
                          {t('ai_providers.claude_cloak_cache_user_id_hint', {
                            defaultValue: '在同一凭证上复用生成的伪装 metadata.user_id。',
                          })}
                        </span>
                      </div>
                      <ToggleSwitch
                        size="sm"
                        checked={cloakCacheUserId}
                        onChange={(cacheUserId) => updateCloak({ cacheUserId })}
                        disabled={controlsDisabled}
                        ariaLabel={t('ai_providers.claude_cloak_cache_user_id', {
                          defaultValue: '缓存伪装用户 ID',
                        })}
                      />
                    </div>

                    <div className="form-group">
                      <label>{cleanLabelText(t('ai_providers.claude_cloak_sensitive_words_label'))}</label>
                      <textarea
                        className="input"
                        rows={3}
                        placeholder={t('ai_providers.claude_cloak_sensitive_words_placeholder')}
                        value={cloakSensitiveWordsText}
                        onChange={(e) => {
                          const rawText = e.target.value;
                          const words = parseSensitiveWordsText(rawText);
                          setForm((prev) => ({
                            ...prev,
                            cloakSensitiveWordsInput: rawText,
                            cloak: {
                              ...(prev.cloak ?? {}),
                              mode: prev.cloak?.mode || 'auto',
                              strictMode: prev.cloak?.strictMode ?? false,
                              cacheUserId: prev.cloak?.cacheUserId ?? false,
                              sensitiveWords: words.length ? words : undefined,
                            },
                          }));
                        }}
                        disabled={controlsDisabled}
                      />
                    </div>
                  </div>
                )}
              </>
            )}
          </div>
        </section>
      )}

      {/* Card 5: 自定义请求头 */}
      <section className={styles.sectionCard}>
        <div className={styles.sectionHeader}>
          <div className={styles.sectionHeading}>
            <IconScrollText size={15} />
            <h4>{cleanLabelText(t('common.custom_headers_label'))}</h4>
          </div>
          <Button
            variant="secondary"
            size="xs"
            disabled={controlsDisabled}
            onClick={() =>
              setForm((prev) => ({
                ...prev,
                headers: [...prev.headers, { key: '', value: '' }],
              }))
            }
          >
            <IconPlus size={13} />
            {t('common.custom_headers_add')}
          </Button>
        </div>
        <HeaderInputList
          entries={form.headers}
          onChange={(headers) => setForm((prev) => ({ ...prev, headers }))}
          addLabel={t('common.custom_headers_add')}
          keyPlaceholder={t('common.custom_headers_key_placeholder')}
          valuePlaceholder={t('common.custom_headers_value_placeholder')}
          disabled={controlsDisabled}
          allowEmpty
          hideAddButton
          emptyText={t('ai_providers.custom_headers_empty_hint', {
            defaultValue: '未配置额外请求头；点击右上角添加需透传至上游的 HTTP Header。',
          })}
        />
      </section>
    </div>
  );
}
