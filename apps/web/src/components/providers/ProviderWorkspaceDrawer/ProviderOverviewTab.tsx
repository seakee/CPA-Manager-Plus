import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  MetricSummaryGrid,
  type MetricSummaryCardProps,
} from '@/components/common/MetricSummaryCard';
import {
  DrawerDefinitionList,
  DrawerEmptyBox,
  DrawerNotice,
  DrawerScopePill,
  DrawerSectionCard,
  DrawerTabPanel,
} from '@/components/common/WorkspaceDrawer';
import { DrawerRecentStatusCard } from '@/components/common/WorkspaceDrawer/DrawerRecentStatusCard';
import { Button } from '@/components/ui/Button';
import {
  IconChartLine,
  IconCopy,
  IconEye,
  IconEyeOff,
  IconKey,
  IconModelCluster,
  IconSlidersHorizontal,
} from '@/components/ui/icons';
import { useNotificationStore } from '@/stores';
import { coolingPolicyFromOverride } from '@/types';
import { copyToClipboard } from '@/utils/clipboard';
import { normalizeCredentialWeight } from '@/utils/credentialWeight';
import { maskApiKey } from '@/utils/format';
import {
  formatCompactHistoryUsd,
  formatCompactTimestamp,
  formatCompactTokens,
} from '../ProviderTable/formatters';
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
  routingStrategy?: string;
  globalDisableCooling?: boolean;
  onSwitchTab: (tab: ProviderDrawerTab) => void;
  onToggleEnabled?: (row: ProviderRow, enabled: boolean) => void;
}
const MAX_MODEL_PREVIEW_PILLS = 10;
const stripLabelNoise = (text: string): string =>
  text.replace(/\s*[（(](?:可选|必填|optional|required)[)）]/gi, '').replace(/[:：]\s*$/, '');

export function ProviderOverviewTab({
  row,
  usageByProvider,
  actionsDisabled,
  toggleDisabled,
  isDirty = false,
  saving = false,
  routingStrategy,
  globalDisableCooling,
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
    const base = t('ai_providers.cooling_policy_inherit');
    if (globalDisableCooling === true) {
      return `${base} (${t('ai_providers.cooling_disabled_badge', { defaultValue: '不冷却' })})`;
    }
    if (globalDisableCooling === false) {
      return `${base} (${t('ai_providers.cooling_policy_enabled')})`;
    }
    return base;
  }, [globalDisableCooling, row.disableCooling, t]);
  const rawHeaders = row.raw.headers ?? {};
  const headerEntries = Object.entries(rawHeaders);
  const explicitModels = row.raw.models ?? [];
  const excludedModels =
    row.kind === 'openai' ? [] : stripDisableAllModelsRule(row.raw.excludedModels);
  const totalCalls = row.stats.success + row.stats.failure;
  const history = row.history;
  const hasHistoryMetrics = Boolean(history && history.totalRequests > 0);
  const rawRate =
    totalCalls > 0
      ? hasHistoryMetrics && history && Number.isFinite(history.successRate)
        ? history.successRate
        : (row.stats.success / totalCalls) * 100
      : null;
  const displayRate = rawRate !== null ? Math.round(rawRate * 10) / 10 : null;
  const { compact: lastSeenCompact, full: lastSeenFull } = formatCompactTimestamp(row.lastSeenMs);
  const latestFailure = row.latestFailure;
  const failureSummaryText = latestFailure?.summary || latestFailure?.errorCode || '';
  const failureStatusPrefix =
    latestFailure?.statusCode && !failureSummaryText.includes(String(latestFailure.statusCode))
      ? `HTTP ${latestFailure.statusCode}`
      : null;
  const failureBannerText =
    latestFailure?.isMostRecent && (failureStatusPrefix || failureSummaryText)
      ? [failureStatusPrefix, failureSummaryText].filter(Boolean).join(' · ')
      : null;

  const hasActivity = hasHistoryMetrics || totalCalls > 0;

  const historyCards = useMemo((): MetricSummaryCardProps[] => {
    if (!hasActivity) return [];
    const rateTone =
      displayRate === null
        ? undefined
        : displayRate >= 95
          ? 'good'
          : displayRate >= 85
            ? 'warn'
            : 'bad';

    if (hasHistoryMetrics && history) {
      const cards: MetricSummaryCardProps[] = [
        {
          label: t('accounts.value_requests', { defaultValue: '请求数' }),
          value: formatCompactTokens(totalCalls),
          valueTitle: totalCalls.toLocaleString(),
          meta: '',
          icon: 'calls',
          accent: 'blue',
        },
        {
          label: t('accounts.detail_success_rate', { defaultValue: '成功率' }),
          value: displayRate !== null ? `${displayRate}%` : '-',
          meta: '',
          icon: 'success',
          accent: 'green',
          tone: rateTone,
        },
        {
          label: t('usage_analytics.trend_metric_totalTokens', { defaultValue: '总 Token' }),
          value: formatCompactTokens(history.totalTokens),
          valueTitle: history.totalTokens.toLocaleString(),
          meta: '',
          icon: 'tokens',
          accent: 'teal',
          variant: 'secondary',
        },
      ];
      if (history.totalCost !== null) {
        cards.push({
          label: t('accounts.history_cost', { defaultValue: '估算费用' }),
          value: formatCompactHistoryUsd(history.totalCost),
          meta: '',
          icon: 'cost',
          accent: 'amber',
        });
      }
      if (lastSeenCompact) {
        cards.push({
          label: t('accounts.detail_overview_activity_last_active', { defaultValue: '最后活动' }),
          value: lastSeenCompact,
          valueTitle: lastSeenFull ?? undefined,
          meta: '',
          icon: 'trend',
          accent: 'blue',
        });
      }
      return cards;
    }

    return [
      {
        label: t('accounts.value_requests', { defaultValue: '请求数' }),
        value: formatCompactTokens(totalCalls),
        valueTitle: totalCalls.toLocaleString(),
        meta: '',
        icon: 'calls',
        accent: 'blue',
      },
      {
        label: t('accounts.detail_success_rate', { defaultValue: '成功率' }),
        value: displayRate !== null ? `${displayRate}%` : '-',
        meta: '',
        icon: 'success',
        accent: 'green',
        tone: rateTone,
      },
      {
        label: t('accounts.detail_overview_activity_success', { defaultValue: '成功请求' }),
        value: row.stats.success.toLocaleString(),
        meta: '',
        icon: 'success',
        accent: 'green',
      },
      {
        label: t('accounts.detail_overview_activity_failure', { defaultValue: '失败请求' }),
        value: row.stats.failure.toLocaleString(),
        meta: '',
        icon: 'failure',
        accent: 'red',
        tone: row.stats.failure > 0 ? 'bad' : undefined,
      },
    ];
  }, [
    displayRate,
    hasActivity,
    hasHistoryMetrics,
    history,
    lastSeenCompact,
    lastSeenFull,
    row.stats.failure,
    row.stats.success,
    t,
    totalCalls,
  ]);
  const openAIProxyCount =
    row.kind === 'openai'
      ? (row.raw.apiKeyEntries ?? []).filter((entry) => Boolean(entry.proxyUrl?.trim())).length
      : 0;
  const singleProxyUrl = row.kind !== 'openai' ? row.raw.proxyUrl?.trim() ?? '' : '';
  const singleRawKey = row.kind !== 'openai' ? row.raw.apiKey?.trim() ?? '' : '';
  const authIndexValue = row.raw.authIndex?.trim() ?? '';

  const prioritySummary =
    row.kind !== 'openai' && routingStrategy === 'weighted-round-robin'
      ? t('ai_providers.priority_weight_value', {
          defaultValue: '优先级 {{priority}} · 权重 {{weight}}',
          priority: row.priority ?? 0,
          weight: formatWeightLabel(row.raw.weight),
        })
      : row.kind !== 'openai' && normalizeCredentialWeight(row.raw.weight) !== undefined
        ? t('ai_providers.overview_priority_weight_inactive', {
            defaultValue: '优先级 {{priority}}（权重 {{weight}} · 仅加权轮询生效）',
            priority: row.priority ?? 0,
            weight: formatWeightLabel(row.raw.weight),
          })
        : t('ai_providers.priority_only_value', {
            defaultValue: '优先级 {{priority}}',
            priority: row.priority ?? 0,
          });

  const claudeProtocolSummary = useMemo(() => {
    if (row.kind !== 'claude') return '';
    const segments: string[] = [];
    const fpLabel =
      row.raw.fingerprintProfile === 'claude-code-cli'
        ? t('ai_providers.claude_request_fingerprint_claude_code_cli')
        : t('ai_providers.claude_request_fingerprint_default');
    segments.push(`${stripLabelNoise(t('ai_providers.claude_request_fingerprint_label'))}: ${fpLabel}`);
    if (row.raw.rebuildMidSystemMessage) {
      segments.push(
        `${stripLabelNoise(t('ai_providers.rebuild_mid_system_message_label'))}: ${t('common.enabled', { defaultValue: '已开启' })}`
      );
    }
    const cloakText = row.raw.cloak
      ? [
          row.raw.cloak.mode || 'auto',
          row.raw.cloak.strictMode
            ? stripLabelNoise(t('ai_providers.claude_cloak_strict_label'))
            : null,
          row.raw.cloak.sensitiveWords?.length
            ? `${stripLabelNoise(t('ai_providers.claude_cloak_sensitive_words_count'))} ${row.raw.cloak.sensitiveWords.length}`
            : null,
        ]
          .filter(Boolean)
          .join(' · ')
      : t('common.disabled', { defaultValue: '未开启' });
    segments.push(`${t('ai_providers.overview_cloak_label', { defaultValue: '请求伪装' })}: ${cloakText}`);
    return segments.join('  ·  ');
  }, [row, t]);

  return (
    <DrawerTabPanel>
      {!row.enabled && (
        <DrawerNotice
          tone="danger"
          role="status"
          title={t('ai_providers.drawer_disabled_notice_title', {
            defaultValue: '当前提供商已停用',
          })}
          action={
            onToggleEnabled ? (
              <Button
                variant="secondary"
                size="xs"
                disabled={actionsDisabled || toggleDisabled || isDirty || saving}
                onClick={() => onToggleEnabled(row, true)}
              >
                {t('ai_providers.drawer_quick_enable', { defaultValue: '立即启用' })}
              </Button>
            ) : null
          }
        >
          {t('ai_providers.drawer_disabled_notice_desc', {
            defaultValue: '该提供商当前不会参与路由分发；修改模型或配置时将保持停用状态。',
          })}
        </DrawerNotice>
      )}

      <DrawerRecentStatusCard
        title={t('accounts.detail_overview_recent_status_title', { defaultValue: '最近状态' })}
        scopeLabel={t('accounts.detail_overview_recent_status_scope', {
          defaultValue: '最近 200 分钟 · 20 个 10 分钟区间',
        })}
        statusData={row.recentStatusData ?? row.statusData}
        successLabel={t('accounts.detail_overview_recent_status_success', {
          defaultValue: '成功请求',
        })}
        failureLabel={t('accounts.detail_overview_recent_status_failure', {
          defaultValue: '失败请求',
        })}
        timelineLabel={t('accounts.detail_overview_recent_status_timeline', {
          defaultValue: '请求状态时间线',
        })}
        timelineHint={t('accounts.detail_overview_recent_status_timeline_hint', {
          defaultValue: '每段 10 分钟',
        })}
        unavailableMessage={
          row.usageStatsCoverage === 'unavailable'
            ? t('ai_providers.overview_usage_unavailable', {
                defaultValue: '当前提供商未配置可用于索引统计的密钥，暂无近期请求时序数据。',
              })
            : undefined
        }
        emptyMessage={t('accounts.detail_overview_recent_status_empty', {
          defaultValue: '最近约 200 分钟没有请求记录。',
        })}
        failureBannerTitle={t('accounts.detail_overview_recent_status_message', {
          defaultValue: '当前状态说明',
        })}
        failureBannerMessage={failureBannerText || undefined}
        coverageNotice={
          row.usageStatsCoverage === 'partial'
            ? t('ai_providers.overview_usage_partial', {
                defaultValue: '部分密钥未配置明文凭据，仅展示可追踪密钥的聚合统计。',
              })
            : undefined
        }
      />

      <DrawerSectionCard
        variant="default"
        icon={<IconChartLine size={15} />}
        title={t('ai_providers.card_history_title', { defaultValue: '历史用量' })}
        metaActions={
          <DrawerScopePill fallback={!hasHistoryMetrics && hasActivity}>
            {hasHistoryMetrics
              ? t('ai_providers.overview_history_scope_monitoring', {
                  defaultValue: '请求监控 · 历史累计',
                })
              : t('accounts.detail_overview_activity_scope_recent', {
                  defaultValue: '近期快照 · 回退数据',
                })}
          </DrawerScopePill>
        }
        dataAttributes={{ 'data-overview-section': 'activity' }}
      >
        {hasActivity ? (
          <MetricSummaryGrid
            cards={historyCards}
            density="compact"
            columns={Math.min(historyCards.length, 5)}
          />
        ) : (
          <DrawerEmptyBox>
            {t('ai_providers.overview_history_empty', {
              defaultValue: '暂无历史用量记录。',
            })}
          </DrawerEmptyBox>
        )}
      </DrawerSectionCard>

      <DrawerSectionCard
        icon={<IconSlidersHorizontal size={15} />}
        title={t('ai_providers.overview_routing_credentials_title', {
          defaultValue: '生效路由、调度与凭据',
        })}
      >
        <DrawerDefinitionList>
          <div className={styles.overviewDlRow}>
            <dt className={styles.overviewDt}>
              {t('ai_providers.base_url_label', { defaultValue: 'Base URL' })}
            </dt>
            <dd className={styles.overviewDd}>
              {row.baseUrl ? (
                <>
                  <span className={`${styles.fieldValue} ${styles.fieldValueMono}`}>
                    {row.baseUrl}
                  </span>
                  {row.isOfficialEndpoint && (
                    <span className={styles.officialBadge}>
                      {t('ai_providers.official_badge', { defaultValue: '官方' })}
                    </span>
                  )}
                  <button
                    type="button"
                    className={styles.inlineCopyBtn}
                    onClick={() => void handleCopy(row.baseUrl, 'Base URL')}
                    aria-label={t('common.copy', { defaultValue: '复制' })}
                  >
                    <IconCopy size={13} />
                  </button>
                </>
              ) : (
                <span className={styles.overviewMutedValue}>
                  {t('ai_providers.official_default_endpoint', { defaultValue: '官方默认端点' })}
                </span>
              )}
            </dd>
          </div>

          <div className={styles.overviewDlRow}>
            <dt className={styles.overviewDt}>
              {t('ai_providers.endpoint_prefix_label', { defaultValue: '前缀' })}
            </dt>
            <dd className={styles.overviewDd}>
              {row.prefix ? (
                <>
                  <span className={`${styles.fieldValue} ${styles.fieldValueMono}`}>
                    {`${row.prefix}/<model>`}
                  </span>
                  <button
                    type="button"
                    className={styles.inlineCopyBtn}
                    onClick={() =>
                      void handleCopy(
                        row.prefix,
                        t('ai_providers.endpoint_prefix_label', { defaultValue: '前缀' })
                      )
                    }
                    aria-label={t('common.copy', { defaultValue: '复制' })}
                  >
                    <IconCopy size={13} />
                  </button>
                </>
              ) : (
                <span className={styles.overviewMutedValue}>
                  {t('ai_providers.overview_prefix_unrestricted', {
                    defaultValue: '未限制（匹配无前缀请求）',
                  })}
                </span>
              )}
            </dd>
          </div>

          <div className={styles.overviewDlRow}>
            <dt className={styles.overviewDt}>
              {t('ai_providers.overview_scheduling_label', { defaultValue: '调度与冷却' })}
            </dt>
            <dd className={styles.overviewDd}>
              <span className={styles.fieldValueMono}>{prioritySummary}</span>
              <span className={styles.metaSeparator} aria-hidden="true">
                ·
              </span>
              <span>{coolingLabel}</span>
            </dd>
          </div>

          {row.kind !== 'openai' && (
            <div className={styles.overviewDlRow}>
              <dt className={styles.overviewDt}>
                {t('ai_providers.config_section_auth_keys', { defaultValue: '鉴权密钥' })}
              </dt>
              <dd className={styles.overviewDd}>
                {singleRawKey ? (
                  <>
                    <code className={styles.keyValueCode}>
                      {revealedSingleKey ? singleRawKey : maskApiKey(singleRawKey)}
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
                      onClick={() => void handleCopy(singleRawKey, 'API Key')}
                      aria-label={t('common.copy', { defaultValue: '复制' })}
                    >
                      <IconCopy size={13} />
                    </button>
                  </>
                ) : (
                  <span className={styles.overviewMutedValue}>
                    {t('ai_providers.overview_keyless_notice', {
                      defaultValue: '无静态密钥（无密钥端点或凭据托管）',
                    })}
                  </span>
                )}
                {authIndexValue ? (
                  <span className={styles.authIndexChip} title={`Auth Index: ${authIndexValue}`}>
                    <span>#{authIndexValue}</span>
                    <button
                      type="button"
                      className={styles.inlineCopyBtn}
                      onClick={() => void handleCopy(authIndexValue, 'Auth Index')}
                      aria-label={t('common.copy', { defaultValue: '复制' })}
                    >
                      <IconCopy size={12} />
                    </button>
                  </span>
                ) : null}
              </dd>
            </div>
          )}

          {row.kind === 'openai' && authIndexValue ? (
            <div className={styles.overviewDlRow}>
              <dt className={styles.overviewDt}>
                {t('ai_providers.auth_index_label', { defaultValue: 'Auth Index' })}
              </dt>
              <dd className={styles.overviewDd}>
                <span className={styles.authIndexChip}>
                  <span>#{authIndexValue}</span>
                  <button
                    type="button"
                    className={styles.inlineCopyBtn}
                    onClick={() => void handleCopy(authIndexValue, 'Auth Index')}
                    aria-label={t('common.copy', { defaultValue: '复制' })}
                  >
                    <IconCopy size={12} />
                  </button>
                </span>
              </dd>
            </div>
          ) : null}

          {(row.kind === 'openai' ? openAIProxyCount > 0 : Boolean(singleProxyUrl)) && (
            <div className={styles.overviewDlRow}>
              <dt className={styles.overviewDt}>
                {t('ai_providers.proxy_url_label', { defaultValue: '代理地址' })}
              </dt>
              <dd className={styles.overviewDd}>
                <span className={`${styles.fieldValue} ${styles.fieldValueMono}`}>
                  {row.kind === 'openai'
                    ? t('ai_providers.overview_per_key_proxy_count', {
                        defaultValue: '{{count}} 个密钥已配置代理',
                        count: openAIProxyCount,
                      })
                    : singleProxyUrl}
                </span>
              </dd>
            </div>
          )}

          {row.kind !== 'openai' && (row.kind === 'codex' || row.kind === 'xai') && row.raw.websockets && (
            <div className={styles.overviewDlRow}>
              <dt className={styles.overviewDt}>
                {stripLabelNoise(t('ai_providers.codex_websockets_label'))}
              </dt>
              <dd className={styles.overviewDd}>
                <span>{t('common.enabled', { defaultValue: '已开启' })}</span>
              </dd>
            </div>
          )}

          {row.kind === 'claude' && (
            <div className={styles.overviewDlRow}>
              <dt className={styles.overviewDt}>
                {t('ai_providers.overview_claude_capabilities', { defaultValue: '协议特性' })}
              </dt>
              <dd className={styles.overviewDd}>
                <span>{claudeProtocolSummary}</span>
              </dd>
            </div>
          )}

          {headerEntries.length > 0 && (
            <div className={styles.overviewDlRow}>
              <dt className={styles.overviewDt}>
                {stripLabelNoise(t('common.custom_headers_label'))}
              </dt>
              <dd className={styles.overviewDd}>
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
              </dd>
            </div>
          )}
        </DrawerDefinitionList>
      </DrawerSectionCard>

      {row.kind === 'openai' && (
        <DrawerSectionCard
          icon={<IconKey size={15} />}
          title={t('ai_providers.overview_keys_title', { defaultValue: '鉴权密钥明细' })}
          metaActions={
            <Button variant="ghost" size="xs" onClick={() => onSwitchTab('connectivity')}>
              {t('ai_providers.overview_go_connectivity_link', { defaultValue: '去测活 →' })}
            </Button>
          }
        >

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
        </DrawerSectionCard>
      )}

      <DrawerSectionCard
        icon={<IconModelCluster size={15} />}
        title={t('ai_providers.overview_models_title', { defaultValue: '模型覆盖摘要' })}
        metaActions={
          <Button variant="ghost" size="xs" onClick={() => onSwitchTab('models')}>
            {t('ai_providers.overview_manage_models_link', { defaultValue: '管理模型 →' })}
          </Button>
        }
      >

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
          {row.kind !== 'openai' && excludedModels.length > 0 && (
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
            {explicitModels.slice(0, MAX_MODEL_PREVIEW_PILLS).map((model, idx) => {
              const hasAlias = Boolean(model.alias && model.alias !== model.name);
              return (
                <span key={`${model.name}-${idx}`} className={styles.modelPill}>
                  {hasAlias ? (
                    <>
                      <span className={styles.modelPillPrimary}>{model.alias}</span>
                      <span className={styles.modelPillTarget}>→ {model.name}</span>
                    </>
                  ) : (
                    <span className={styles.modelPillName}>{model.name}</span>
                  )}
                </span>
              );
            })}
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
      </DrawerSectionCard>
    </DrawerTabPanel>
  );
}
