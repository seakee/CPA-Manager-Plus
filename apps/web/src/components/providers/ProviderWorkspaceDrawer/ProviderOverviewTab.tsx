import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import {
  IconChartLine,
  IconCheck,
  IconCopy,
  IconEye,
  IconEyeOff,
  IconKey,
  IconModelCluster,
  IconSatellite,
  IconX,
} from '@/components/ui/icons';
import { useNotificationStore } from '@/stores';
import { coolingPolicyFromOverride } from '@/types';
import { copyToClipboard } from '@/utils/clipboard';
import { normalizeCredentialWeight } from '@/utils/credentialWeight';
import { maskApiKey } from '@/utils/format';
import { ProviderStatusBar } from '../ProviderStatusBar';
import { PROVIDER_KIND_LABELS } from '../ProviderTable/kindMeta';
import type { ProviderRow } from '../ProviderTable/rowData';
import {
  getProviderTotalStats,
  stripDisableAllModelsRule,
  type ProviderRecentUsageMap,
} from '../utils';
import type { ProviderDrawerTab } from '../types';
import type { ProviderDrawerFormState } from './useProviderDrawerEditor';
import styles from './ProviderWorkspaceDrawer.module.scss';

export interface ProviderOverviewTabProps {
  row: ProviderRow;
  form: ProviderDrawerFormState;
  usageByProvider: ProviderRecentUsageMap;
  actionsDisabled: boolean;
  toggleDisabled?: boolean;
  isDirty?: boolean;
  saving?: boolean;
  onSwitchTab: (tab: ProviderDrawerTab) => void;
  onToggleEnabled?: (row: ProviderRow, enabled: boolean) => void;
}

const MAX_MODEL_PREVIEW_PILLS = 10;
const stripTrailingColon = (text: string): string => text.replace(/[:：]\s*$/, '');


export function ProviderOverviewTab({
  row,
  form,
  usageByProvider,
  actionsDisabled,
  toggleDisabled,
  isDirty = false,
  saving = false,
  onSwitchTab,
  onToggleEnabled,
}: ProviderOverviewTabProps) {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const [revealedState, setRevealedState] = useState<{
    rowKey: string;
    openAIKeys: Set<number>;
    singleKey: boolean;
  }>({
    rowKey: row.key,
    openAIKeys: new Set(),
    singleKey: false,
  });
  const revealedOpenAIKeys = useMemo(
    () => (revealedState.rowKey === row.key ? revealedState.openAIKeys : new Set<number>()),
    [revealedState, row.key]
  );
  const revealedSingleKey = revealedState.rowKey === row.key && revealedState.singleKey;
  const formatWeightLabel = (weight: number | undefined) => {
    const normalized = normalizeCredentialWeight(weight);
    return normalized === undefined
      ? `1 (${t('ai_providers.weight_default_label')})`
      : String(normalized);
  };
  const handleCopy = async (value: string, label: string) => {
    const trimmed = value.trim();
    if (!trimmed) return;
    const copied = await copyToClipboard(trimmed);
    if (copied) {
      showNotification(t('common.copied_to_clipboard', { label }), 'success');
    }
  };

  const toggleOpenAIKeyReveal = (index: number) => {
    setRevealedState((prev) => {
      const baseSet = prev.rowKey === row.key ? prev.openAIKeys : new Set<number>();
      const next = new Set(baseSet);
      if (next.has(index)) next.delete(index);
      else next.add(index);
      return { rowKey: row.key, openAIKeys: next, singleKey: false };
    });
  };

  const coolingLabel = useMemo(() => {
    const policy = coolingPolicyFromOverride(row.disableCooling);
    if (policy === 'enabled') return t('ai_providers.cooling_policy_enabled');
    if (policy === 'disabled') return t('ai_providers.cooling_policy_disabled');
    return t('ai_providers.cooling_policy_inherit');
  }, [row.disableCooling, t]);

  const rawHeaders = row.raw.headers ?? {};
  const headerEntries = Object.entries(rawHeaders);
  const explicitModels = row.raw.models ?? [];
  const excludedModels =
    row.kind === 'openai' ? [] : stripDisableAllModelsRule(row.raw.excludedModels);
  const aliasText = form.name.trim() || row.alias || '';

  return (
    <div className={styles.drawerTabPanel}>
      {!row.enabled && (
        <div className={styles.drawerDisabledNotice} role="status">
          <div className={styles.drawerDisabledText}>
            <strong>
              {t('ai_providers.drawer_disabled_notice_title', { defaultValue: '当前提供商已停用' })}
            </strong>
            <p>
              {t('ai_providers.drawer_disabled_notice_desc', {
                defaultValue: '该提供商当前不会参与路由分发；修改模型或配置时将保持停用状态。',
              })}
            </p>
          </div>
          {onToggleEnabled && (
            <Button
              variant="secondary"
              size="xs"
              disabled={actionsDisabled || toggleDisabled || isDirty || saving}
              onClick={() => onToggleEnabled(row, true)}
            >
              {t('ai_providers.drawer_quick_enable', { defaultValue: '立即启用' })}
            </Button>
          )}
        </div>
      )}

      {/* 1. 路由与调度摘要卡 */}
      <section className={styles.sectionCard}>
        <div className={styles.sectionHeader}>
          <div className={styles.sectionHeading}>
            <IconSatellite size={15} />
            <h4>{t('ai_providers.overview_routing_title', { defaultValue: '路由与调度摘要' })}</h4>
          </div>
          <Button variant="ghost" size="xs" onClick={() => onSwitchTab('config')}>
            {t('ai_providers.overview_edit_config_link', { defaultValue: '编辑配置 →' })}
          </Button>
        </div>

        <div className={styles.fieldGrid}>
          <div className={styles.fieldCell}>
            <span className={styles.fieldLabel}>
              {t('ai_providers.name_label', { defaultValue: '名称 / 别名' })}
            </span>
            <div className={styles.fieldValueRow}>
              <span className={styles.fieldValue}>{aliasText || '—'}</span>
            </div>
          </div>

          <div className={styles.fieldCell}>
            <span className={styles.fieldLabel}>
              {t('ai_providers.provider_kind_label', { defaultValue: '协议类型' })}
            </span>
            <div className={styles.fieldValueRow}>
              <span className={styles.fieldValue}>{PROVIDER_KIND_LABELS[row.kind]}</span>
            </div>
          </div>

          <div className={`${styles.fieldCell} ${styles.fieldCellWide}`}>
            <span className={styles.fieldLabel}>{t('ai_providers.base_url_label', { defaultValue: 'Base URL' })}</span>
            <div className={styles.fieldValueRow}>
              <span className={`${styles.fieldValue} ${styles.fieldValueMono}`}>
                {row.baseUrl || '—'}
              </span>
              {row.baseUrl && (
                <button
                  type="button"
                  className={styles.inlineCopyBtn}
                  onClick={() => void handleCopy(row.baseUrl, 'Base URL')}
                  aria-label={t('common.copy', { defaultValue: '复制' })}
                >
                  <IconCopy size={13} />
                </button>
              )}
            </div>
          </div>

          <div className={styles.fieldCell}>
            <span className={styles.fieldLabel}>{stripTrailingColon(t('ai_providers.prefix_label'))}</span>
            <div className={styles.fieldValueRow}>
              <span className={`${styles.fieldValue} ${styles.fieldValueMono}`}>
                {row.prefix ? `${row.prefix}/<model>` : '—'}
              </span>
              {row.prefix && (
                <button
                  type="button"
                  className={styles.inlineCopyBtn}
                  onClick={() => void handleCopy(row.prefix, stripTrailingColon(t('ai_providers.prefix_label')))}
                  aria-label={t('common.copy', { defaultValue: '复制' })}
                >
                  <IconCopy size={13} />
                </button>
              )}
            </div>
          </div>

          <div className={styles.fieldCell}>
            <span className={styles.fieldLabel}>
              {t('ai_providers.proxy_url_label', { defaultValue: '代理地址' })}
            </span>
            <div className={styles.fieldValueRow}>
              <span className={`${styles.fieldValue} ${styles.fieldValueMono}`}>
                {row.kind === 'openai'
                  ? row.raw.apiKeyEntries?.some((entry) => Boolean(entry.proxyUrl?.trim()))
                    ? t('ai_providers.overview_per_key_proxy_count', {
                        defaultValue: '{{count}} 个密钥已配置代理',
                        count: row.raw.apiKeyEntries.filter((entry) => Boolean(entry.proxyUrl?.trim())).length,
                      })
                    : '—'
                  : row.raw.proxyUrl?.trim() || '—'}
              </span>
            </div>
          </div>

          <div className={styles.fieldCell}>
            <span className={styles.fieldLabel}>
              {t('ai_providers.auth_index_label', { defaultValue: 'Auth Index' })}
            </span>
            <div className={styles.fieldValueRow}>
              <span className={`${styles.fieldValue} ${styles.fieldValueMono}`}>
                {row.raw.authIndex?.trim() || '—'}
              </span>
              {row.raw.authIndex?.trim() && (
                <button
                  type="button"
                  className={styles.inlineCopyBtn}
                  onClick={() => void handleCopy(row.raw.authIndex ?? '', 'Auth Index')}
                  aria-label={t('common.copy', { defaultValue: '复制' })}
                >
                  <IconCopy size={13} />
                </button>
              )}
            </div>
          </div>

          <div className={styles.fieldCell}>
            <span className={styles.fieldLabel}>
              {t('ai_providers.scheduling_priority_weight', { defaultValue: '优先级 / 权重' })}
            </span>
            <div className={styles.fieldValueRow}>
              <span className={`${styles.fieldValue} ${styles.fieldValueMono}`}>
                {row.kind === 'openai'
                  ? t('ai_providers.priority_only_value', {
                      defaultValue: '优先级 {{priority}}',
                      priority: row.priority ?? 0,
                    })
                  : t('ai_providers.priority_weight_value', {
                      defaultValue: '优先级 {{priority}} · 权重 {{weight}}',
                      priority: row.priority ?? 0,
                      weight: formatWeightLabel(row.raw.weight),
                    })}
              </span>
            </div>
          </div>

          <div className={styles.fieldCell}>
            <span className={styles.fieldLabel}>{stripTrailingColon(t('ai_providers.cooling_policy_label'))}</span>
            <div className={styles.fieldValueRow}>
              <span className={styles.fieldValue}>{coolingLabel}</span>
            </div>
          </div>

          {row.kind !== 'openai' && (row.kind === 'codex' || row.kind === 'xai') && (
            <div className={styles.fieldCell}>
              <span className={styles.fieldLabel}>{stripTrailingColon(t('ai_providers.codex_websockets_label'))}</span>
              <div className={styles.fieldValueRow}>
                <span className={styles.fieldValue}>
                  {row.raw.websockets
                    ? t('common.enabled', { defaultValue: '已开启' })
                    : t('common.disabled', { defaultValue: '未开启' })}
                </span>
              </div>
            </div>
          )}

          {row.kind === 'claude' && (
            <div className={`${styles.fieldCell} ${styles.fieldCellWide}`}>
              <span className={styles.fieldLabel}>
                {t('ai_providers.overview_claude_capabilities', { defaultValue: 'Claude 协议特性' })}
              </span>
              <div className={styles.badgeList}>
                <span className={styles.infoPill}>
                  {stripTrailingColon(t('ai_providers.claude_request_fingerprint_label'))}:{' '}
                  {row.raw.fingerprintProfile === 'claude-code-cli'
                    ? t('ai_providers.claude_request_fingerprint_claude_code_cli')
                    : t('ai_providers.claude_request_fingerprint_default')}
                </span>
                <span className={styles.infoPill}>
                  {stripTrailingColon(t('ai_providers.rebuild_mid_system_message_label'))}:{' '}
                  {row.raw.rebuildMidSystemMessage
                    ? t('common.enabled', { defaultValue: '已开启' })
                    : t('common.disabled', { defaultValue: '未开启' })}
                </span>
                <span className={styles.infoPill}>
                  {stripTrailingColon(t('ai_providers.claude_cloak_title'))}:{' '}
                  {row.raw.cloak
                    ? `${row.raw.cloak.mode || 'auto'} · ${stripTrailingColon(t('ai_providers.claude_cloak_strict_label'))}: ${
                        row.raw.cloak.strictMode ? t('common.yes', { defaultValue: '是' }) : t('common.no', { defaultValue: '否' })
                      } · ${stripTrailingColon(t('ai_providers.claude_cloak_sensitive_words_count'))}: ${
                        row.raw.cloak.sensitiveWords?.length ?? 0
                      }`
                    : t('common.disabled', { defaultValue: '未开启' })}
                </span>
              </div>
            </div>
          )}

          <div className={`${styles.fieldCell} ${styles.fieldCellWide}`}>
            <span className={styles.fieldLabel}>
              {t('ai_providers.custom_headers_summary', {
                defaultValue: '自定义请求头 ({{count}})',
                count: headerEntries.length,
              })}
            </span>
            {headerEntries.length > 0 ? (
              <div className={styles.badgeList}>
                {headerEntries.slice(0, 6).map(([headerKey, headerVal]) => (
                  <span key={headerKey} className={`${styles.infoPill} ${styles.fieldValueMono}`}>
                    {headerKey}: {maskApiKey(headerVal) || '****'}
                  </span>
                ))}
                {headerEntries.length > 6 && (
                  <span className={styles.infoPill}>+{headerEntries.length - 6}</span>
                )}
              </div>
            ) : (
              <span className={styles.fieldValue}>—</span>
            )}
          </div>
        </div>
      </section>

      {/* 2. 近期请求时序卡 */}
      <section className={styles.sectionCard}>
        <div className={styles.sectionHeader}>
          <div className={styles.sectionHeading}>
            <IconChartLine size={15} />
            <h4>{t('ai_providers.overview_recent_status_title', { defaultValue: '近期请求时序' })}</h4>
          </div>
          <div className={styles.usageCounters}>
            <span className={styles.statSuccess}>
              <IconCheck size={13} />
              {row.stats.success}
            </span>
            <span className={styles.statFailure}>
              <IconX size={13} />
              {row.stats.failure}
            </span>
          </div>
        </div>

        {row.usageStatsCoverage === 'unavailable' ? (
          <p className={styles.coverageNotice}>
            {t('ai_providers.overview_usage_unavailable', {
              defaultValue: '当前提供商未配置可用于索引统计的密钥，暂无近期请求时序数据。',
            })}
          </p>
        ) : (
          <>
            <ProviderStatusBar statusData={row.statusData} />
            {row.usageStatsCoverage === 'partial' && (
              <p className={styles.coverageNotice}>
                {t('ai_providers.overview_usage_partial', {
                  defaultValue: '部分密钥未配置明文凭据，仅展示可追踪密钥的聚合统计。',
                })}
              </p>
            )}
          </>
        )}
      </section>

      {/* 3. 鉴权密钥明细卡 */}
      <section className={styles.sectionCard}>
        <div className={styles.sectionHeader}>
          <div className={styles.sectionHeading}>
            <IconKey size={15} />
            <h4>{t('ai_providers.overview_keys_title', { defaultValue: '鉴权密钥明细' })}</h4>
          </div>
          <Button variant="ghost" size="xs" onClick={() => onSwitchTab('connectivity')}>
            {t('ai_providers.overview_go_connectivity_link', { defaultValue: '去测活 →' })}
          </Button>
        </div>

        {row.kind === 'openai' ? (
          <div className={styles.keyList}>
            {(row.raw.apiKeyEntries ?? []).length === 0 ? (
              <div className={styles.emptyHint}>
                {t('ai_providers.overview_keyless_notice', { defaultValue: '无密钥通道配置' })}
              </div>
            ) : (
              (row.raw.apiKeyEntries ?? []).map((entry, idx) => {
                const rawKey = String(entry.apiKey ?? '').trim();
                const isRevealed = revealedOpenAIKeys.has(idx);
                const keyHeaderCount = entry.headers ? Object.keys(entry.headers).length : 0;
                const keyStats = rawKey
                  ? getProviderTotalStats(usageByProvider, row.raw.name, rawKey, row.raw.baseUrl)
                  : null;

                return (
                  <div key={`${idx}-${rawKey || 'keyless'}`} className={styles.keyDetailCard}>
                    <div className={styles.keyDetailLeft}>
                      <span className={styles.keyIndexBadge}>#{idx + 1}</span>
                      {rawKey ? (
                        <>
                          <code className={styles.keyValueCode}>
                            {isRevealed ? rawKey : maskApiKey(rawKey)}
                          </code>
                          <button
                            type="button"
                            className={styles.inlineCopyBtn}
                            onClick={() => toggleOpenAIKeyReveal(idx)}
                            aria-label={
                              isRevealed
                                ? t('common.hide', { defaultValue: '隐藏' })
                                : t('common.reveal', { defaultValue: '显示明文' })
                            }
                          >
                            {isRevealed ? <IconEyeOff size={13} /> : <IconEye size={13} />}
                          </button>
                          <button
                            type="button"
                            className={styles.inlineCopyBtn}
                            onClick={() => void handleCopy(rawKey, `API Key #${idx + 1}`)}
                            aria-label={t('common.copy', { defaultValue: '复制' })}
                          >
                            <IconCopy size={13} />
                          </button>
                        </>
                      ) : (
                        <span className={styles.infoPill}>
                          {t('ai_providers.overview_keyless_entry', { defaultValue: '无密钥通道' })}
                        </span>
                      )}
                    </div>
                    <div className={styles.keyDetailRight}>
                      <span className={styles.infoPill}>
                        {t('ai_providers.credential_weight_badge', {
                          defaultValue: '权重 {{weight}}',
                          weight: formatWeightLabel(entry.weight),
                        })}
                      </span>
                      {entry.proxyUrl?.trim() && (
                        <span className={styles.infoPill} title={entry.proxyUrl}>
                          {t('ai_providers.proxy_configured', { defaultValue: '已配代理' })}
                        </span>
                      )}
                      {keyHeaderCount > 0 && (
                        <span className={styles.infoPill}>
                          {t('ai_providers.header_count_badge', {
                            defaultValue: '请求头 {{count}}',
                            count: keyHeaderCount,
                          })}
                        </span>
                      )}
                      {keyStats && (
                        <span className={styles.usageCounters}>
                          <span className={styles.statSuccess}>{keyStats.success}</span>
                          <span>/</span>
                          <span className={styles.statFailure}>{keyStats.failure}</span>
                        </span>
                      )}
                    </div>
                  </div>
                );
              })
            )}
          </div>
        ) : (
          <div className={styles.keyDetailCard}>
            <div className={styles.keyDetailLeft}>
              {row.raw.apiKey?.trim() ? (
                <>
                  <code className={styles.keyValueCode}>
                    {revealedSingleKey ? row.raw.apiKey.trim() : maskApiKey(row.raw.apiKey.trim())}
                  </code>
                  <button
                    type="button"
                    className={styles.inlineCopyBtn}
                    onClick={() =>
                      setRevealedState((prev) => ({
                        rowKey: row.key,
                        openAIKeys: new Set(),
                        singleKey: prev.rowKey === row.key ? !prev.singleKey : true,
                      }))
                    }
                    aria-label={
                      revealedSingleKey
                        ? t('common.hide', { defaultValue: '隐藏' })
                        : t('common.reveal', { defaultValue: '显示明文' })
                    }
                  >
                    {revealedSingleKey ? <IconEyeOff size={13} /> : <IconEye size={13} />}
                  </button>
                  <button
                    type="button"
                    className={styles.inlineCopyBtn}
                    onClick={() => void handleCopy(row.raw.apiKey ?? '', 'API Key')}
                    aria-label={t('common.copy', { defaultValue: '复制' })}
                  >
                    <IconCopy size={13} />
                  </button>
                </>
              ) : (
                <span className={styles.infoPill}>
                  {t('ai_providers.overview_keyless_notice', {
                    defaultValue: '当前未配置静态密钥（无密钥端点或凭据托管）',
                  })}
                </span>
              )}
            </div>
            <div className={styles.keyDetailRight}>
              <span className={styles.infoPill}>
                {t('ai_providers.credential_weight_badge', {
                  defaultValue: '权重 {{weight}}',
                  weight: formatWeightLabel(row.raw.weight),
                })}
              </span>
              {row.raw.authIndex?.trim() && (
                <span className={`${styles.infoPill} ${styles.fieldValueMono}`}>
                  #{row.raw.authIndex.trim()}
                </span>
              )}
            </div>
          </div>
        )}
      </section>

      {/* 4. 模型覆盖摘要卡 */}
      <section className={styles.sectionCard}>
        <div className={styles.sectionHeader}>
          <div className={styles.sectionHeading}>
            <IconModelCluster size={15} />
            <h4>{t('ai_providers.overview_models_title', { defaultValue: '模型覆盖摘要' })}</h4>
          </div>
          <Button variant="ghost" size="xs" onClick={() => onSwitchTab('models')}>
            {t('ai_providers.overview_manage_models_link', { defaultValue: '管理模型 →' })}
          </Button>
        </div>

        <div className={styles.badgeList}>
          <span className={styles.infoPill}>
            {explicitModels.length > 0
              ? t('ai_providers.overview_explicit_models_count', {
                  defaultValue: '显式映射 {{count}} 个',
                  count: explicitModels.length,
                })
              : t('ai_providers.overview_wildcard_models_notice', {
                  defaultValue: '未配置显式模型（默认通配上游模型）',
                })}
          </span>
          {row.kind !== 'openai' && (
            <span className={styles.infoPill}>
              {t('ai_providers.overview_excluded_models_count', {
                defaultValue: '排除规则 {{count}} 条',
                count: excludedModels.length,
              })}
            </span>
          )}
        </div>

        {explicitModels.length > 0 && (
          <div className={styles.badgeList}>
            {explicitModels.slice(0, MAX_MODEL_PREVIEW_PILLS).map((model, idx) => (
              <span key={`${model.name}-${idx}`} className={styles.modelPill}>
                <span className={styles.modelPillName}>{model.name}</span>
                {model.alias && model.alias !== model.name && (
                  <span className={styles.modelPillAlias}>→ {model.alias}</span>
                )}
              </span>
            ))}
            {explicitModels.length > MAX_MODEL_PREVIEW_PILLS && (
              <span className={styles.infoPill}>
                +{explicitModels.length - MAX_MODEL_PREVIEW_PILLS}
              </span>
            )}
          </div>
        )}

        {excludedModels.length > 0 && (
          <div className={styles.badgeList}>
            {excludedModels.slice(0, MAX_MODEL_PREVIEW_PILLS).map((pattern) => (
              <span key={pattern} className={`${styles.modelPill} ${styles.excludedModelPill}`}>
                <span className={styles.modelPillName}>! {pattern}</span>
              </span>
            ))}
            {excludedModels.length > MAX_MODEL_PREVIEW_PILLS && (
              <span className={styles.infoPill}>
                +{excludedModels.length - MAX_MODEL_PREVIEW_PILLS}
              </span>
            )}
          </div>
        )}
      </section>
    </div>
  );
}
