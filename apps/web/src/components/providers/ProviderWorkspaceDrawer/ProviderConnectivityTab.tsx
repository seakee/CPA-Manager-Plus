import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { SegmentedTabs, type SegmentedTabItem } from '@/components/ui/SegmentedTabs';
import {
  IconActivity,
  IconCheck,
  IconChevronDown,
  IconSatellite,
  IconSend,
  IconX,
} from '@/components/ui/icons';
import { maskApiKey } from '@/utils/format';
import { buildHeaderObject } from '@/utils/headers';
import { hasOpenAIKeyEntryConfiguration } from '@/utils/openAIKeyEntries';
import type { ProviderKind, ProviderRow } from '../ProviderTable/rowData';
import type {
  ProviderConnectivityAuthDiagnostic,
  ProviderConnectivityProbeMode,
  ProviderDrawerConnectivityResult,
  ProviderDrawerFormState,
  ProviderOpenAIKeyTestEntry,
} from './useProviderDrawerEditor';
import styles from './ProviderWorkspaceDrawer.module.scss';

export interface ProviderConnectivityModelOption {
  value: string;
  alias?: string;
  label: string;
}

export interface ProviderConnectivityTabProps {
  activeKind: ProviderKind;
  row?: ProviderRow | null;
  form: ProviderDrawerFormState;
  isDirty?: boolean;
  actionsDisabled: boolean;
  saving: boolean;
  isTesting: boolean;
  supportsInferenceProbe?: boolean;
  connectivityProbeMode?: ProviderConnectivityProbeMode;
  onConnectivityProbeModeChange?: (mode: ProviderConnectivityProbeMode) => void;
  connectivityTestModel: string;
  onConnectivityTestModelChange: (value: string) => void;
  modelSelectOptions: ProviderConnectivityModelOption[];
  singleTestResult: ProviderDrawerConnectivityResult;
  openAIAllTestResult: ProviderDrawerConnectivityResult;
  openAIKeyStatuses: ProviderOpenAIKeyTestEntry[];
  onRunSingleTest: () => Promise<void>;
  onTestSingleOpenAIKey: (index: number) => Promise<boolean>;
  onTestAllOpenAIKeys: () => Promise<void>;
}

const renderStatusPill = (
  status: ProviderDrawerConnectivityResult['status'],
  t: (k: string, opts?: Record<string, unknown>) => string
) => {
  if (status === 'success') {
    return (
      <span className={`${styles.statusBadge} ${styles.statusBadgeSuccess}`}>
        <IconCheck size={12} />
        {t('ai_providers.connectivity_status_success', { defaultValue: '连通正常' })}
      </span>
    );
  }
  if (status === 'error') {
    return (
      <span className={`${styles.statusBadge} ${styles.statusBadgeDisabled}`}>
        <IconX size={12} />
        {t('ai_providers.connectivity_status_error', { defaultValue: '测试失败' })}
      </span>
    );
  }
  return (
    <span className={styles.dirtyBadge}>
      {t('ai_providers.connectivity_status_loading', { defaultValue: '测试中...' })}
    </span>
  );
};

const resolvePreflightEndpoint = (
  kind: ProviderKind,
  rawBaseUrl: string,
  probeMode: ProviderConnectivityProbeMode
): { method: 'POST' | 'GET'; url: string } => {
  const trimmed = rawBaseUrl.trim().replace(/\/+$/, '');
  if (probeMode === 'models') {
    if (kind === 'claude') {
      const base = trimmed || 'https://api.anthropic.com';
      const url = /\/v1$/i.test(base) ? `${base}/models` : `${base}/v1/models`;
      return { method: 'GET', url };
    }
    if (kind === 'openai' || kind === 'codex' || kind === 'xai') {
      const defaultBase = kind === 'xai' ? 'https://api.x.ai/v1' : 'https://api.openai.com/v1';
      const base = trimmed || defaultBase;
      const url = /\/models$/i.test(base)
        ? base
        : /\/v\d+(?:beta\d*)?$/i.test(base)
          ? `${base}/models`
          : `${base}/v1/models`;
      return { method: 'GET', url };
    }
    if (kind === 'vertex') {
      const base = trimmed || 'https://aiplatform.googleapis.com';
      return { method: 'GET', url: `${base}/models` };
    }
    const base = trimmed || 'https://generativelanguage.googleapis.com';
    const url = /\/v1beta$/i.test(base) ? `${base}/models` : `${base}/v1beta/models`;
    return { method: 'GET', url };
  }

  if (kind === 'claude') {
    const base = trimmed || 'https://api.anthropic.com';
    const url = /\/v1$/i.test(base) ? `${base}/messages` : `${base}/v1/messages`;
    return { method: 'POST', url };
  }
  if (kind === 'openai') {
    const base = trimmed || 'https://api.openai.com/v1';
    const url = /\/chat\/completions$/i.test(base) ? base : `${base}/chat/completions`;
    return { method: 'POST', url };
  }
  const defaultBase = kind === 'xai' ? 'https://api.x.ai/v1' : 'https://api.openai.com/v1';
  const base = trimmed || defaultBase;
  const url = /\/responses$/i.test(base)
    ? base
    : /\/v1$/i.test(base)
      ? `${base}/responses`
      : `${base}/v1/responses`;
  return { method: 'POST', url };
};

const renderAuthDiagnosticBadges = (
  diag: ProviderConnectivityAuthDiagnostic,
  t: (k: string, opts?: Record<string, unknown>) => string
) => {
  const headerLabel =
    diag.headerType === 'x-api-key'
      ? 'x-api-key'
      : diag.headerType === 'authorization-bearer'
        ? 'Authorization: Bearer'
        : t('common.none', { defaultValue: '未携带' });
  const sourceLabel =
    diag.credentialSource === 'api-key'
      ? 'API Key'
      : diag.credentialSource === 'auth-index'
        ? 'auth-index'
        : diag.credentialSource === 'custom-header'
          ? t('common.custom_headers_label', { defaultValue: '自定义请求头' })
          : t('common.none', { defaultValue: '无' });
  const endpointTypeLabel =
    diag.endpointType === 'anthropic'
      ? t('ai_providers.connectivity_diag_endpoint_anthropic', { defaultValue: '官方 Anthropic' })
      : t('ai_providers.connectivity_diag_endpoint_custom', { defaultValue: '第三方兼容网关' });

  return (
    <div className={styles.authDiagRow}>
      <span className={styles.authDiagChip}>
        {t('ai_providers.connectivity_diag_header', {
          header: headerLabel,
          defaultValue: '认证头: {{header}}',
        })}
      </span>
      <span className={styles.authDiagChip}>
        {t('ai_providers.connectivity_diag_source', {
          source: sourceLabel,
          defaultValue: '凭据源: {{source}}',
        })}
      </span>
      <span className={styles.authDiagChip}>
        {t('ai_providers.connectivity_diag_endpoint_type', {
          type: endpointTypeLabel,
          defaultValue: '网关识别: {{type}}',
        })}
      </span>
    </div>
  );
};

export function ProviderConnectivityTab({
  activeKind,
  row,
  form,
  isDirty = false,
  actionsDisabled,
  saving,
  isTesting,
  supportsInferenceProbe = activeKind === 'claude' ||
    activeKind === 'codex' ||
    activeKind === 'xai' ||
    activeKind === 'openai',
  connectivityProbeMode = supportsInferenceProbe ? 'inference' : 'models',
  onConnectivityProbeModeChange,
  connectivityTestModel,
  onConnectivityTestModelChange,
  modelSelectOptions,
  singleTestResult,
  openAIAllTestResult,
  openAIKeyStatuses,
  onRunSingleTest,
  onTestSingleOpenAIKey,
  onTestAllOpenAIKeys,
}: ProviderConnectivityTabProps) {
  const { t } = useTranslation();
  const controlsDisabled = actionsDisabled || saving;
  const isOpenAI = activeKind === 'openai';
  const effectiveProbeMode: ProviderConnectivityProbeMode = supportsInferenceProbe
    ? connectivityProbeMode
    : 'models';
  const showModelSelector = effectiveProbeMode === 'inference';

  const [comboboxOpen, setComboboxOpen] = useState(false);
  const [filterByTyping, setFilterByTyping] = useState(false);
  const [highlightedIdx, setHighlightedIdx] = useState(-1);
  const [singleBodyExpanded, setSingleBodyExpanded] = useState(false);
  const [expandedKeyBodies, setExpandedKeyBodies] = useState<Set<number>>(() => new Set());
  const [keyFilter, setKeyFilter] = useState<'all' | 'failed'>('all');
  const comboboxRef = useRef<HTMLDivElement | null>(null);

  const configuredOptions = useMemo(
    () => modelSelectOptions.filter((opt) => opt.value && opt.value !== '__custom__'),
    [modelSelectOptions]
  );

  const visibleOptions = useMemo(() => {
    if (!filterByTyping || !connectivityTestModel.trim()) {
      return configuredOptions;
    }
    const q = connectivityTestModel.trim().toLowerCase();
    return configuredOptions.filter(
      (opt) =>
        opt.value.toLowerCase().includes(q) ||
        Boolean(opt.alias && opt.alias.toLowerCase().includes(q))
    );
  }, [configuredOptions, connectivityTestModel, filterByTyping]);

  useEffect(() => {
    if (!comboboxOpen || typeof document === 'undefined') return;
    const handleOutside = (event: MouseEvent) => {
      if (comboboxRef.current && !comboboxRef.current.contains(event.target as Node)) {
        setComboboxOpen(false);
        setFilterByTyping(false);
      }
    };
    document.addEventListener('mousedown', handleOutside);
    return () => document.removeEventListener('mousedown', handleOutside);
  }, [comboboxOpen]);

  const preflight = useMemo(
    () => resolvePreflightEndpoint(activeKind, form.baseUrl, effectiveProbeMode),
    [activeKind, effectiveProbeMode, form.baseUrl]
  );

  const activeHeaderCount = useMemo(
    () => Object.keys(buildHeaderObject(form.headers)).length,
    [form.headers]
  );

  const configuredOpenAIKeyCount = useMemo(
    () => form.apiKeyEntries.filter((entry) => hasOpenAIKeyEntryConfiguration(entry)).length,
    [form.apiKeyEntries]
  );

  const failedKeyCount = useMemo(
    () => openAIKeyStatuses.filter((entry) => entry?.status === 'error').length,
    [openAIKeyStatuses]
  );

  const transportSummary = useMemo(() => {
    if (isOpenAI) {
      const proxyCount = form.apiKeyEntries.filter((entry) => Boolean(entry.proxyUrl?.trim())).length;
      const parts = [
        t('ai_providers.connectivity_openai_keys_summary', {
          count: configuredOpenAIKeyCount,
          defaultValue: '{{count}} 个有效密钥通道',
        }),
      ];
      if (proxyCount > 0) {
        parts.push(
          t('ai_providers.overview_per_key_proxy_count', {
            count: proxyCount,
            defaultValue: '{{count}} 个密钥已配置代理',
          })
        );
      } else {
        parts.push(t('ai_providers.connectivity_direct_upstream', { defaultValue: '直连上游' }));
      }
      if (activeHeaderCount > 0) {
        parts.push(
          t('ai_providers.header_count_badge', {
            count: activeHeaderCount,
            defaultValue: '请求头 {{count}}',
          })
        );
      }
      return parts.join(' · ');
    }

    const rawKey = form.apiKey.trim();
    const authSummary = rawKey
      ? maskApiKey(rawKey)
      : form.authIndex.trim()
        ? `#${form.authIndex.trim()}`
        : t('ai_providers.overview_keyless_entry', { defaultValue: '无静态密钥' });
    const proxySummary = form.proxyUrl.trim()
      ? `${t('ai_providers.proxy_configured', { defaultValue: '已配代理' })} (${form.proxyUrl.trim()})`
      : t('ai_providers.connectivity_direct_upstream', { defaultValue: '直连上游' });
    const parts = [authSummary, proxySummary];
    if (activeKind === 'claude' && form.cloak) {
      parts.push(
        `${t('ai_providers.overview_cloak_label', { defaultValue: '请求伪装' })}: ${form.cloak.mode || 'auto'}`
      );
    }
    if (activeHeaderCount > 0) {
      parts.push(
        t('ai_providers.header_count_badge', {
          count: activeHeaderCount,
          defaultValue: '请求头 {{count}}',
        })
      );
    }
    return parts.join(' · ');
  }, [activeHeaderCount, activeKind, configuredOpenAIKeyCount, form, isOpenAI, t]);

  const recentFailureText = useMemo(() => {
    const latestFailure = row?.latestFailure;
    if (!latestFailure) return null;
    const summary = latestFailure.summary || latestFailure.errorCode || '';
    const statusPrefix =
      latestFailure.statusCode && !summary.includes(String(latestFailure.statusCode))
        ? `HTTP ${latestFailure.statusCode}`
        : null;
    const text = [statusPrefix, summary].filter(Boolean).join(' · ');
    return text || null;
  }, [row?.latestFailure]);

  const handleSelectComboboxModel = (modelValue: string) => {
    onConnectivityTestModelChange(modelValue);
    setComboboxOpen(false);
    setFilterByTyping(false);
    setHighlightedIdx(-1);
  };

  const handleComboboxKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (controlsDisabled || isTesting) return;
    if (event.key === 'ArrowDown') {
      if (configuredOptions.length === 0) return;
      event.preventDefault();
      if (!comboboxOpen) {
        setComboboxOpen(true);
        setHighlightedIdx(0);
        return;
      }
      setHighlightedIdx((prev) => (prev < visibleOptions.length - 1 ? prev + 1 : 0));
    } else if (event.key === 'ArrowUp') {
      if (!comboboxOpen || visibleOptions.length === 0) return;
      event.preventDefault();
      setHighlightedIdx((prev) => (prev > 0 ? prev - 1 : visibleOptions.length - 1));
    } else if (event.key === 'Enter') {
      if (comboboxOpen && highlightedIdx >= 0 && highlightedIdx < visibleOptions.length) {
        event.preventDefault();
        handleSelectComboboxModel(visibleOptions[highlightedIdx].value);
      } else if (!comboboxOpen) {
        event.preventDefault();
        if (isOpenAI) void onTestAllOpenAIKeys();
        else void onRunSingleTest();
      } else {
        setComboboxOpen(false);
      }
    } else if (event.key === 'Escape' && comboboxOpen) {
      event.preventDefault();
      event.stopPropagation();
      setComboboxOpen(false);
      setFilterByTyping(false);
    }
  };

  const toggleKeyBody = (idx: number) => {
    setExpandedKeyBodies((prev) => {
      const next = new Set(prev);
      if (next.has(idx)) next.delete(idx);
      else next.add(idx);
      return next;
    });
  };

  const selectedAlias = useMemo(() => {
    const match = configuredOptions.find((opt) => opt.value === connectivityTestModel.trim());
    return match?.alias;
  }, [configuredOptions, connectivityTestModel]);

  return (
    <div className={styles.drawerTabPanel}>
      {/* Card 1: 实时草稿连通性诊断（双模式切换 + 可伸缩模型 Combobox + OpenAI 扁平逐密钥诊断） */}
      <section className={styles.sectionCard}>
        <div className={styles.sectionHeader}>
          <div className={styles.sectionHeading}>
            <IconActivity size={15} />
            <h4>
              {t('ai_providers.connectivity_panel_title', { defaultValue: '实时草稿连通性诊断' })}
            </h4>
            {isDirty ? (
              <span className={styles.dirtyBadge}>
                {t('ai_providers.connectivity_using_draft_badge', {
                  defaultValue: '使用未保存草稿',
                })}
              </span>
            ) : (
              <span className={styles.officialBadge}>
                {t('ai_providers.connectivity_using_saved_badge', {
                  defaultValue: '已同步保存配置',
                })}
              </span>
            )}
          </div>

          {supportsInferenceProbe && onConnectivityProbeModeChange && (
            <SegmentedTabs<ProviderConnectivityProbeMode>
              idBase="provider-connectivity-probe-mode"
              variant="joined"
              size="sm"
              roleMode="group"
              responsiveFullWidth={false}
              disabled={controlsDisabled || isTesting}
              ariaLabel={t('ai_providers.connectivity_probe_mode_aria', {
                defaultValue: '探测方式',
              })}
              activeTab={effectiveProbeMode}
              onChange={onConnectivityProbeModeChange}
              items={
                [
                  {
                    id: 'inference',
                    label: t('ai_providers.connectivity_mode_inference', {
                      defaultValue: '对话探测',
                    }),
                  },
                  {
                    id: 'models',
                    label: t('ai_providers.connectivity_mode_models', {
                      defaultValue: '/models 探活',
                    }),
                  },
                ] satisfies SegmentedTabItem<ProviderConnectivityProbeMode>[]
              }
            />
          )}
        </div>

        <div className={styles.preflightEndpointBar}>
          <span className={styles.httpMethodBadge}>{preflight.method}</span>
          <span className={styles.preflightUrl}>{preflight.url}</span>
        </div>

        {showModelSelector ? (
          <div className={styles.probeActionRow}>
            <div className={styles.modelCombobox} ref={comboboxRef}>
              <div className={styles.modelComboboxInputWrap}>
                <input
                  type="text"
                  className={`input ${styles.monoInput} ${styles.modelComboboxInput}`}
                  aria-label={t('ai_providers.test_model_label', { defaultValue: '测试模型' })}
                  placeholder={t('ai_providers.custom_test_model_placeholder', {
                    defaultValue: '选择已配模型或输入自定义模型 ID',
                  })}
                  value={connectivityTestModel}
                  disabled={controlsDisabled || isTesting}
                  autoComplete="off"
                  onFocus={() => {
                    if (configuredOptions.length > 0) {
                      setFilterByTyping(false);
                      setComboboxOpen(true);
                    }
                  }}
                  onChange={(e) => {
                    onConnectivityTestModelChange(e.target.value);
                    setFilterByTyping(true);
                    if (configuredOptions.length > 0) {
                      setComboboxOpen(true);
                    }
                    setHighlightedIdx(-1);
                  }}
                  onKeyDown={handleComboboxKeyDown}
                />
                <div className={styles.modelComboboxSuffix}>
                  {selectedAlias && !comboboxOpen && (
                    <span className={styles.comboboxAliasTag}>{selectedAlias}</span>
                  )}
                  {!selectedAlias && configuredOptions.length > 0 && !comboboxOpen && (
                    <span className={styles.comboboxCountHint}>
                      {t('ai_providers.connectivity_configured_models_badge', {
                        count: configuredOptions.length,
                        defaultValue: '{{count}} 个已配',
                      })}
                    </span>
                  )}
                  {connectivityTestModel && comboboxOpen && (
                    <button
                      type="button"
                      className={styles.inputEyeBtn}
                      disabled={controlsDisabled || isTesting}
                      aria-label={t('common.clear', { defaultValue: '清除' })}
                      onClick={() => {
                        onConnectivityTestModelChange('');
                        setFilterByTyping(false);
                        setComboboxOpen(configuredOptions.length > 0);
                      }}
                    >
                      <IconX size={13} />
                    </button>
                  )}
                  {configuredOptions.length > 0 && (
                    <button
                      type="button"
                      className={`${styles.inputEyeBtn} ${
                        comboboxOpen ? styles.comboboxChevronOpen : ''
                      }`}
                      disabled={controlsDisabled || isTesting}
                      aria-label={t('ai_providers.connectivity_toggle_model_list', {
                        defaultValue: '展开已配置模型列表',
                      })}
                      onClick={() => {
                        setFilterByTyping(false);
                        setComboboxOpen((prev) => !prev);
                      }}
                    >
                      <IconChevronDown size={14} />
                    </button>
                  )}
                </div>
              </div>

              {comboboxOpen && configuredOptions.length > 0 && (
                <div className={styles.modelComboboxDropdown} role="listbox">
                  <div className={styles.modelComboboxHeader}>
                    <span>
                      {t('ai_providers.connectivity_dropdown_header', {
                        count: configuredOptions.length,
                        defaultValue: '已配置模型 ({{count}})',
                      })}
                    </span>
                    <span>
                      {t('ai_providers.connectivity_dropdown_custom_hint', {
                        defaultValue: '支持直接输入任意模型 ID',
                      })}
                    </span>
                  </div>
                  {visibleOptions.length === 0 ? (
                    <div className={styles.modelComboboxEmpty}>
                      {t('ai_providers.connectivity_dropdown_use_custom', {
                        model: connectivityTestModel.trim(),
                        defaultValue: '无匹配已配项，将直接使用「{{model}}」发送测试',
                      })}
                    </div>
                  ) : (
                    visibleOptions.map((opt, idx) => {
                      const isSelected = connectivityTestModel.trim() === opt.value;
                      const isHighlighted = idx === highlightedIdx;
                      return (
                        <button
                          key={opt.value}
                          type="button"
                          role="option"
                          aria-selected={isSelected}
                          className={`${styles.modelComboboxOption} ${
                            isSelected ? styles.modelComboboxOptionSelected : ''
                          } ${isHighlighted ? styles.modelComboboxOptionHighlighted : ''}`}
                          onMouseEnter={() => setHighlightedIdx(idx)}
                          onClick={() => handleSelectComboboxModel(opt.value)}
                        >
                          <span className={styles.modelComboboxOptionName}>{opt.value}</span>
                          <span className={styles.modelComboboxOptionRight}>
                            {opt.alias && (
                              <span className={styles.comboboxAliasTag}>{opt.alias}</span>
                            )}
                            {isSelected && <IconCheck size={13} />}
                          </span>
                        </button>
                      );
                    })
                  )}
                </div>
              )}
            </div>

            <Button
              variant="primary"
              size="sm"
              className={styles.probeSendBtn}
              loading={isTesting}
              disabled={controlsDisabled || isTesting || (isOpenAI && configuredOpenAIKeyCount === 0)}
              onClick={() => (isOpenAI ? void onTestAllOpenAIKeys() : void onRunSingleTest())}
            >
              <IconSend size={13} />
              {isOpenAI
                ? t('ai_providers.openai_test_all_action', { defaultValue: '一键测试全部密钥' })
                : t('ai_providers.send_test_request_btn', { defaultValue: '立即发送测试请求' })}
            </Button>
          </div>
        ) : (
          <div className={styles.modelsProbeBar}>
            <span className={styles.sectionHint}>
              {t('ai_providers.connectivity_desc_models_probe', {
                defaultValue: '使用当前草稿中的 Base URL、请求头与密钥向下游拉取模型列表以验证连通性。',
              })}
            </span>
            <Button
              variant="primary"
              size="sm"
              className={styles.probeSendBtn}
              loading={isTesting}
              disabled={controlsDisabled || isTesting || (isOpenAI && configuredOpenAIKeyCount === 0)}
              onClick={() => (isOpenAI ? void onTestAllOpenAIKeys() : void onRunSingleTest())}
            >
              <IconSend size={13} />
              {isOpenAI
                ? t('ai_providers.openai_test_all_action', { defaultValue: '一键测试全部密钥' })
                : t('ai_providers.send_test_request_btn', { defaultValue: '立即发送测试请求' })}
            </Button>
          </div>
        )}

        {/* 单密钥诊断结果卡 */}
        {!isOpenAI && singleTestResult.status !== 'idle' && (
          <div
            className={`${styles.connectivityResultCard} ${
              singleTestResult.status === 'success'
                ? styles.connectivityResultSuccess
                : singleTestResult.status === 'error'
                  ? styles.connectivityResultError
                  : ''
            }`}
            role="status"
          >
            <div className={styles.connectivityResultHeader}>
              {renderStatusPill(singleTestResult.status, t)}
              <div className={styles.connectivityMetrics}>
                {singleTestResult.statusCode !== undefined && (
                  <span>{`HTTP ${singleTestResult.statusCode}`}</span>
                )}
                {singleTestResult.elapsedMs !== undefined && (
                  <span>{`${singleTestResult.elapsedMs} ms`}</span>
                )}
                {singleTestResult.responseBody && (
                  <button
                    type="button"
                    className={styles.bodyToggleBtn}
                    onClick={() => setSingleBodyExpanded((prev) => !prev)}
                  >
                    {singleBodyExpanded
                      ? t('ai_providers.connectivity_hide_body', { defaultValue: '收起响应体' })
                      : t('ai_providers.connectivity_show_body', { defaultValue: '查看响应体' })}
                  </button>
                )}
              </div>
            </div>

            {singleTestResult.message && (
              <div className={styles.connectivityMessage}>{singleTestResult.message}</div>
            )}

            {singleTestResult.authDiagnostic &&
              renderAuthDiagnosticBadges(singleTestResult.authDiagnostic, t)}

            {singleBodyExpanded && singleTestResult.responseBody && (
              <pre className={styles.responseBodyPre}>{singleTestResult.responseBody}</pre>
            )}
          </div>
        )}

        {/* OpenAI 多密钥汇总条 + 扁平细线逐密钥列表 */}
        {isOpenAI && (
          <div className={styles.openAIDiagnosticsBlock}>
            <div className={styles.openAIDiagnosticsHeader}>
              <div className={styles.openAIDiagnosticsSummary}>
                <strong className={styles.openAIKeySectionTitle}>
                  {t('ai_providers.per_key_test_title', { defaultValue: '逐密钥测活状态' })}
                </strong>
                {openAIAllTestResult.status !== 'idle' && (
                  <>
                    {renderStatusPill(openAIAllTestResult.status, t)}
                    <span className={styles.sectionHint}>{openAIAllTestResult.message}</span>
                    {openAIAllTestResult.elapsedMs !== undefined && (
                      <span className={styles.connectivityMetrics}>
                        {`${openAIAllTestResult.elapsedMs} ms`}
                      </span>
                    )}
                  </>
                )}
              </div>

              {failedKeyCount > 0 && form.apiKeyEntries.length > 1 && (
                <SegmentedTabs<'all' | 'failed'>
                  idBase="provider-connectivity-key-filter"
                  variant="joined"
                  size="sm"
                  roleMode="group"
                  responsiveFullWidth={false}
                  ariaLabel={t('ai_providers.per_key_test_title', {
                    defaultValue: '逐密钥测活状态',
                  })}
                  activeTab={keyFilter}
                  onChange={setKeyFilter}
                  items={
                    [
                      {
                        id: 'all',
                        label: t('ai_providers.connectivity_filter_all', {
                          count: form.apiKeyEntries.length,
                          defaultValue: '全部 ({{count}})',
                        }),
                      },
                      {
                        id: 'failed',
                        label: t('ai_providers.connectivity_filter_failed_only', {
                          count: failedKeyCount,
                          defaultValue: '仅失败 ({{count}})',
                        }),
                      },
                    ] satisfies SegmentedTabItem<'all' | 'failed'>[]
                  }
                />
              )}
            </div>

            <div className={styles.openAIKeyFlatList}>
              {form.apiKeyEntries.map((entry, idx) => {
                const keyStatus = openAIKeyStatuses[idx];
                if (keyFilter === 'failed' && keyStatus?.status !== 'error') {
                  return null;
                }
                const configured = hasOpenAIKeyEntryConfiguration(entry);
                const rawKey = entry.apiKey.trim();
                const bodyExpanded = expandedKeyBodies.has(idx);

                return (
                  <div key={idx} className={styles.openAIKeyFlatRow}>
                    <div className={styles.openAIKeyTestHeader}>
                      <div className={styles.openAIKeyTestIdentity}>
                        <span className={styles.keyIndexBadge}>{`#${idx + 1}`}</span>
                        <code className={styles.keyValueCode}>
                          {rawKey
                            ? maskApiKey(rawKey)
                            : t('ai_providers.overview_keyless_entry', {
                                defaultValue: '无密钥通道',
                              })}
                        </code>
                        {entry.proxyUrl?.trim() && (
                          <span className={styles.officialBadge} title={entry.proxyUrl.trim()}>
                            {t('ai_providers.proxy_configured', { defaultValue: '已配代理' })}
                          </span>
                        )}
                      </div>

                      <div className={styles.sectionMetaActions}>
                        {keyStatus?.status === 'success' && (
                          <span className={`${styles.statusBadge} ${styles.statusBadgeSuccess}`}>
                            <IconCheck size={11} />
                            {keyStatus.statusCode ? `HTTP ${keyStatus.statusCode}` : 'OK'}
                          </span>
                        )}
                        {keyStatus?.status === 'error' && (
                          <span className={`${styles.statusBadge} ${styles.statusBadgeDisabled}`}>
                            <IconX size={11} />
                            {keyStatus.statusCode
                              ? `HTTP ${keyStatus.statusCode}`
                              : t('ai_providers.connectivity_status_error', {
                                  defaultValue: '失败',
                                })}
                          </span>
                        )}
                        {keyStatus?.status === 'loading' && (
                          <span className={styles.dirtyBadge}>
                            {t('ai_providers.connectivity_status_loading', {
                              defaultValue: '测试中...',
                            })}
                          </span>
                        )}
                        {keyStatus?.elapsedMs !== undefined && (
                          <span className={styles.connectivityMetrics}>
                            {keyStatus.elapsedMs} ms
                          </span>
                        )}
                        <Button
                          variant="secondary"
                          size="xs"
                          disabled={controlsDisabled || isTesting || !configured}
                          onClick={() => void onTestSingleOpenAIKey(idx)}
                        >
                          {t('ai_providers.test_single_key_btn', { defaultValue: '测试' })}
                        </Button>
                      </div>
                    </div>

                    {keyStatus?.status === 'error' && keyStatus.message && (
                      <div className={styles.openAIKeyErrorLine}>
                        <span className={styles.openAIKeyErrorText}>{keyStatus.message}</span>
                        {keyStatus.responseBody && (
                          <button
                            type="button"
                            className={styles.bodyToggleBtn}
                            onClick={() => toggleKeyBody(idx)}
                          >
                            {bodyExpanded
                              ? t('ai_providers.connectivity_hide_body', {
                                  defaultValue: '收起响应体',
                                })
                              : t('ai_providers.connectivity_show_body', {
                                  defaultValue: '查看响应体',
                                })}
                          </button>
                        )}
                      </div>
                    )}

                    {keyStatus?.status === 'success' &&
                      effectiveProbeMode === 'models' &&
                      keyStatus.message && (
                        <div className={styles.sectionHint}>{keyStatus.message}</div>
                      )}

                    {bodyExpanded && keyStatus?.responseBody && (
                      <pre className={styles.responseBodyPre}>{keyStatus.responseBody}</pre>
                    )}
                  </div>
                );
              })}
            </div>
          </div>
        )}
      </section>

      {/* Card 2: 凭据传输摘要与近期线上网关参考 */}
      <section className={styles.sectionCard}>
        <div className={styles.sectionHeader}>
          <div className={styles.sectionHeading}>
            <IconSatellite size={15} />
            <h4>
              {t('ai_providers.connectivity_preflight_title', {
                defaultValue: '传输摘要与近期网关参考',
              })}
            </h4>
          </div>
        </div>

        <dl className={styles.overviewDl}>
          <div className={styles.overviewDlRow}>
            <dt className={styles.overviewDt}>
              {t('ai_providers.connectivity_transport_label', { defaultValue: '凭据与传输' })}
            </dt>
            <dd className={styles.overviewDd}>
              <span className={styles.fieldValueMono}>{transportSummary}</span>
            </dd>
          </div>
          {row && row.usageStatsCoverage !== 'unavailable' && (
            <div className={styles.overviewDlRow}>
              <dt className={styles.overviewDt}>
                {t('ai_providers.connectivity_recent_gateway_label', {
                  defaultValue: '近期网关状态',
                })}
              </dt>
              <dd className={styles.overviewDd}>
                <span className={styles.usageCounters}>
                  <span className={styles.statSuccess}>
                    <IconCheck size={13} />
                    {row.statusData.totalSuccess}
                  </span>
                  <span className={styles.statFailure}>
                    <IconX size={13} />
                    {row.statusData.totalFailure}
                  </span>
                </span>
                {recentFailureText && (
                  <span className={styles.gatewayFailureInline}>{recentFailureText}</span>
                )}
              </dd>
            </div>
          )}
        </dl>
      </section>
    </div>
  );
}
