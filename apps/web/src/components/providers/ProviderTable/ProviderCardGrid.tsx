import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ChangeEvent,
  type KeyboardEvent,
  type MouseEvent,
} from 'react';
import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import {
  IconBinary,
  IconCheck,
  IconCopy,
  IconDollarSign,
  IconModelCluster,
  IconSatellite,
  IconSend,
  IconSettings,
  IconShield,
  IconTrash2,
} from '@/components/ui/icons';
import { copyToClipboard } from '@/utils/clipboard';
import { ProviderStatusBar } from '../ProviderStatusBar';
import {
  formatCompactHistoryUsd,
  formatCompactTimestamp,
  formatCompactTokens,
} from './formatters';
import { PROVIDER_KIND_LABELS } from './kindMeta';
import {
  ProviderIdentityBlock,
  ProviderModelsTrigger,
  type ProviderTableProps,
} from './ProviderTable';
import type { ProviderRow } from './rowData';
import styles from './ProviderCardGrid.module.scss';

export type ProviderCardGridProps = ProviderTableProps;

const stopPropagation = (event: MouseEvent) => {
  event.stopPropagation();
};

const priorityToDraft = (priority?: number): string =>
  typeof priority === 'number' && Number.isFinite(priority) ? String(Math.trunc(priority)) : '';

interface CardPriorityPillProps {
  row: ProviderRow;
  disabled: boolean;
  onChangePriority?: (row: ProviderRow, nextPriority?: number) => Promise<void> | void;
}

function CardPriorityPill({ row, disabled, onChangePriority }: CardPriorityPillProps) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState(() => priorityToDraft(row.priority));
  const [isEditing, setIsEditing] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const skipNextBlurCommitRef = useRef(false);

  const currentPriority = Number.isFinite(row.priority) ? Math.trunc(row.priority ?? 0) : 0;
  const displayPriority = priorityToDraft(row.priority) || '0';
  const priorityLabel = t('ai_providers.priority_short', { defaultValue: '优先级' });
  const editAriaLabel = t('ai_providers.priority_edit', { defaultValue: '编辑优先级' });
  useEffect(() => {
    if (!isEditing) return;
    inputRef.current?.focus();
    inputRef.current?.select();
  }, [isEditing]);

  const commitDraft = () => {
    if (skipNextBlurCommitRef.current) {
      skipNextBlurCommitRef.current = false;
      return;
    }
    const trimmed = draft.trim();
    if (!trimmed) {
      setDraft(priorityToDraft(row.priority));
      setIsEditing(false);
      return;
    }
    const parsed = Number(trimmed);
    if (!Number.isFinite(parsed)) {
      setDraft(priorityToDraft(row.priority));
      setIsEditing(false);
      return;
    }
    const nextPriority = Math.trunc(parsed);
    setDraft(String(nextPriority));
    setIsEditing(false);
    if (nextPriority !== currentPriority || row.priority === undefined) {
      void Promise.resolve(onChangePriority?.(row, nextPriority));
    }
  };

  const startEditing = (event: MouseEvent<HTMLButtonElement>) => {
    event.stopPropagation();
    if (disabled || !onChangePriority) return;
    skipNextBlurCommitRef.current = false;
    setDraft(priorityToDraft(row.priority));
    setIsEditing(true);
  };

  const handleChange = (event: ChangeEvent<HTMLInputElement>) => {
    setDraft(event.target.value);
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    event.stopPropagation();
    if (event.key === 'Enter') {
      event.preventDefault();
      commitDraft();
      skipNextBlurCommitRef.current = true;
      event.currentTarget.blur();
    } else if (event.key === 'Escape') {
      event.preventDefault();
      skipNextBlurCommitRef.current = true;
      setDraft(priorityToDraft(row.priority));
      setIsEditing(false);
      event.currentTarget.blur();
    }
  };

  if (isEditing) {
    return (
      <input
        ref={inputRef}
        type="number"
        step={1}
        inputMode="numeric"
        className={styles.priorityPillInput}
        value={draft}
        disabled={disabled}
        aria-label={editAriaLabel}
        title={editAriaLabel}
        onClick={stopPropagation}
        onChange={handleChange}
        onBlur={commitDraft}
        onKeyDown={handleKeyDown}
      />
    );
  }

  return (
    <button
      type="button"
      className={[
        styles.priorityPill,
        currentPriority < 0 ? styles.priorityPillNegative : '',
      ]
        .filter(Boolean)
        .join(' ')}
      disabled={disabled || !onChangePriority}
      aria-label={`${priorityLabel} ${displayPriority}`}
      title={onChangePriority ? `${priorityLabel} ${displayPriority} (${editAriaLabel})` : `${priorityLabel} ${displayPriority}`}
      onClick={startEditing}
    >
      <span>{priorityLabel}</span>
      <strong>{displayPriority}</strong>
    </button>
  );
}

interface CardStatusInfo {
  label: string;
  title?: string;
  tone: 'good' | 'warn' | 'bad' | 'muted';
}

function resolveCardStatus(
  row: ProviderRow,
  t: TFunction,
  locale?: string
): CardStatusInfo {
  if (!row.enabled) {
    return {
      label: t('ai_providers.status_disabled', { defaultValue: '已停用' }),
      tone: 'muted',
    };
  }
  if (row.kind === 'openai' && row.keyCount === 0) {
    return {
      label: t('ai_providers.keys_count_empty', { defaultValue: '无密钥' }),
      tone: 'warn',
    };
  }
  if (row.latestFailure?.isMostRecent) {
    const code = row.latestFailure.statusCode
      ? String(row.latestFailure.statusCode)
      : row.latestFailure.errorCode;
    const failureTime = formatCompactTimestamp(row.latestFailure.timestampMs, locale).full;
    const summaryParts = [
      code ? `HTTP ${code}` : null,
      row.latestFailure.summary || null,
      failureTime,
    ].filter(Boolean);
    return {
      label: code
        ? t('ai_providers.card_status_error_code', {
            code,
            defaultValue: `异常 ${code}`,
          })
        : t('ai_providers.latest_failure_fallback', { defaultValue: '异常' }),
      title: summaryParts.join(' · ') || undefined,
      tone: 'bad',
    };
  }
  return {
    label: t('ai_providers.card_status_available', { defaultValue: '可用' }),
    tone: 'good',
  };
}

export function ProviderCardGrid({
  rows,
  loading = false,
  actionsDisabled = false,
  toggleDisabled,
  resolvedTheme,
  emptyState = null,
  selectionMode = false,
  selectedKeys,
  onToggleSelectRow,
  selectedDrawerKey = null,
  onInspect,
  onOpenModels,
  onHealthCheckRow,
  onEdit,
  onDelete,
  onToggleEnabled,
  onChangePriority,
  routingStrategy,
  globalDisableCooling,
}: ProviderCardGridProps) {
  const { t, i18n } = useTranslation();
  const [copiedKey, setCopiedKey] = useState<string | null>(null);
  const copyTimerRef = useRef<number | undefined>(undefined);

  useEffect(() => {
    return () => {
      if (copyTimerRef.current !== undefined) {
        window.clearTimeout(copyTimerRef.current);
      }
    };
  }, []);

  const handleCopyText = useCallback(async (event: MouseEvent, text: string, key: string) => {
    event.stopPropagation();
    const ok = await copyToClipboard(text);
    if (!ok) return;
    setCopiedKey(key);
    if (copyTimerRef.current !== undefined) {
      window.clearTimeout(copyTimerRef.current);
    }
    copyTimerRef.current = window.setTimeout(() => {
      setCopiedKey((current) => (current === key ? null : current));
    }, 1500);
  }, []);

  const handleOpenModels = onOpenModels ?? onEdit;
  const resolvedToggleDisabled = toggleDisabled ?? actionsDisabled;

  if (loading && rows.length === 0) {
    return <div className="hint">{t('common.loading')}</div>;
  }

  if (rows.length === 0) {
    return <>{emptyState}</>;
  }

  const handleCardKeyDown = (event: KeyboardEvent<HTMLElement>, row: ProviderRow) => {
    if (event.target !== event.currentTarget) return;
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      if (selectionMode) {
        onToggleSelectRow?.(row.key);
      } else {
        onInspect?.(row);
      }
    }
  };

  return (
    <div className={styles.grid} role="list" aria-label={t('ai_providers.table_aria_label')}>
      {rows.map((row) => {
        const kindLabel = PROVIDER_KIND_LABELS[row.kind];
        const isBulkSelected = Boolean(selectedKeys?.has(row.key));
        const isDrawerSelected = selectedDrawerKey === row.key;
        const statusInfo = resolveCardStatus(row, t, i18n?.language);
        const statusBadgeToneClass =
          statusInfo.tone === 'good'
            ? styles.badgeGood
            : statusInfo.tone === 'warn'
              ? styles.badgeWarn
              : statusInfo.tone === 'bad'
                ? styles.badgeBad
                : styles.badgeMuted;

        const cardClassName = [
          styles.card,
          !row.enabled ? styles.cardDisabled : '',
          isDrawerSelected ? styles.cardSelected : '',
          isBulkSelected ? styles.cardBulkSelected : '',
          selectionMode ? styles.cardSelectionMode : '',
        ]
          .filter(Boolean)
          .join(' ');

        const effectiveWeight =
          typeof row.weight === 'number' && Number.isFinite(row.weight)
            ? Math.trunc(row.weight)
            : 1;
        const showWeightBadge =
          routingStrategy === 'weighted-round-robin' && effectiveWeight !== 1;
        const coolingBadge =
          row.disableCooling === true
            ? {
                text: t('ai_providers.cooling_disabled_badge', { defaultValue: '不冷却' }),
                title: t('ai_providers.cooling_disabled_hint', {
                  defaultValue: '该渠道已禁用失败冷却（disable-cooling）',
                }),
                tone: 'warn' as const,
              }
            : row.disableCooling === false && globalDisableCooling === true
              ? {
                  text: t('ai_providers.cooling_enabled_override_badge', {
                    defaultValue: '允许冷却',
                  }),
                  title: t('ai_providers.cooling_enabled_override_hint', {
                    defaultValue: '全局已禁用冷却，但该渠道显式开启冷却',
                  }),
                  tone: 'neutral' as const,
                }
              : null;

        const cloakBadgeText = row.cloakMode
          ? row.cloakMode === 'never'
            ? t('ai_providers.endpoint_cloak_off_badge', { defaultValue: '不伪装' })
            : t('ai_providers.endpoint_cloak_badge', { defaultValue: '伪装' })
          : '';
        const prefixCopyKey = `${row.key}:prefix`;
        const isPrefixCopied = copiedKey === prefixCopyKey;
        const urlCopyKey = `${row.key}:url`;
        const isUrlCopied = copiedKey === urlCopyKey;
        const endpointText =
          row.displayEndpoint ||
          row.baseUrl ||
          t('ai_providers.official_default_endpoint', { defaultValue: '官方默认端点' });

        // Historical usage KPI values
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
        const rateToneClass =
          displayRate === null
            ? styles.metricValueMuted
            : displayRate >= 90
              ? styles.metricValueGood
              : displayRate >= 50
                ? styles.metricValueWarn
                : styles.metricValueBad;
        const { compact: lastSeenCompact, full: lastSeenFull } = formatCompactTimestamp(
          row.lastSeenMs,
          i18n?.language
        );

        // Recent status (CPA 200-min window) + latest failure
        const latestFailure = row.latestFailure;
        const latestFailureBadgeText = latestFailure
          ? latestFailure.statusCode
            ? String(latestFailure.statusCode)
            : latestFailure.errorCode ||
              t('ai_providers.latest_failure_fallback', { defaultValue: '异常' })
          : null;
        const showRecentErrorBadge = Boolean(latestFailure?.isMostRecent && latestFailureBadgeText);
        const failureSummaryText = latestFailure?.summary || latestFailure?.errorCode || '';
        const failureStatusPrefix =
          latestFailure?.statusCode &&
          !failureSummaryText.includes(String(latestFailure.statusCode))
            ? String(latestFailure.statusCode)
            : null;
        const failureBannerSummary =
          latestFailure?.isMostRecent && failureSummaryText
            ? [failureStatusPrefix, failureSummaryText].filter(Boolean).join(' · ')
            : null;
        const cardStatusData = row.recentStatusData ?? row.statusData;
        const recentWindowSuccess = cardStatusData.totalSuccess;
        const recentWindowFailure = cardStatusData.totalFailure;
        const recentWindowTotal = recentWindowSuccess + recentWindowFailure;
        const recentWindowRate =
          recentWindowTotal > 0 ? Math.round((recentWindowSuccess / recentWindowTotal) * 100) : null;
        const recentTrailToneClass = showRecentErrorBadge
          ? styles.recentTrailBad
          : recentWindowRate === null
            ? styles.recentTrailMuted
            : recentWindowRate >= 90
              ? styles.recentTrailGood
              : recentWindowRate >= 50
                ? styles.recentTrailWarn
                : styles.recentTrailBad;

        // Models section header summary
        const aliasCount = row.modelEntries.filter((entry) => Boolean(entry.alias)).length;
        const modelSummaryItems: Array<{ key: string; text: string; warn?: boolean }> = [];
        if (row.modelCount > 3 || (row.modelCount > 0 && aliasCount === 0 && row.excludedModelCount === 0)) {
          modelSummaryItems.push({
            key: 'total',
            text: t('ai_providers.models_total_summary', {
              count: row.modelCount,
              defaultValue: `共 ${row.modelCount} 个模型`,
            }),
          });
        }
        if (aliasCount > 0) {
          modelSummaryItems.push({
            key: 'aliases',
            text: t('ai_providers.models_alias_count', {
              count: aliasCount,
              defaultValue: `${aliasCount} 条别名`,
            }),
          });
        }
        if (row.excludedModelCount > 0) {
          modelSummaryItems.push({
            key: 'excluded',
            text: t('ai_providers.excluded_models_pill', {
              count: row.excludedModelCount,
              defaultValue: `排除 ${row.excludedModelCount}`,
            }),
            warn: true,
          });
        }

        return (
          <article
            key={row.key}
            role="listitem"
            tabIndex={0}
            aria-label={`${kindLabel} ${row.label}`}
            className={cardClassName}
            onClick={
              selectionMode
                ? () => onToggleSelectRow?.(row.key)
                : () => onInspect?.(row)
            }
            onKeyDown={(event) => handleCardKeyDown(event, row)}
          >
            {/* 1. Header: Logo + Identity | Semantic Status Badge + Toggle */}
            <div className={styles.cardHeader}>
              <div className={styles.cardHeaderIdentity}>
                {selectionMode ? (
                  <div className={styles.cardCheckbox} onClick={stopPropagation}>
                    <SelectionCheckbox
                      checked={isBulkSelected}
                      onChange={() => onToggleSelectRow?.(row.key)}
                      disabled={actionsDisabled}
                      ariaLabel={t('ai_providers.select_provider', {
                        name: row.label,
                        defaultValue: `选择 ${row.label}`,
                      })}
                    />
                  </div>
                ) : null}
                <ProviderIdentityBlock
                  row={row}
                  resolvedTheme={resolvedTheme}
                  copiedKey={copiedKey}
                  onCopyText={handleCopyText}
                />
              </div>

              <div className={styles.cardHeaderRight} onClick={stopPropagation}>
                <span
                  className={`${styles.statusBadge} ${statusBadgeToneClass}`}
                  title={statusInfo.title}
                >
                  {statusInfo.label}
                </span>
                <ToggleSwitch
                  ariaLabel={t('ai_providers.config_toggle_label')}
                  checked={row.enabled}
                  disabled={resolvedToggleDisabled}
                  onChange={(value) => onToggleEnabled?.(row, value)}
                />
              </div>
            </div>

            {/* 2. Meta Badges Strip (Scheduling + Routing) & Single-line Endpoint Bar */}
            <div className={styles.cardMetaSection}>
              <div className={styles.cardMetaBadges}>
                <CardPriorityPill
                  row={row}
                  disabled={actionsDisabled}
                  onChangePriority={onChangePriority}
                />

                {showWeightBadge ? (
                  <span
                    className={`${styles.metaPill} ${styles.metaPillNeutral}`}
                    title={t('ai_providers.weight_label', { defaultValue: '权重' })}
                  >
                    {t('ai_providers.weight_badge', {
                      weight: effectiveWeight,
                      defaultValue: `权重 ${effectiveWeight}`,
                    })}
                  </span>
                ) : null}

                {coolingBadge ? (
                  <span
                    className={`${styles.metaPill} ${
                      coolingBadge.tone === 'warn' ? styles.metaPillWarn : styles.metaPillNeutral
                    }`}
                    title={coolingBadge.title}
                  >
                    {coolingBadge.text}
                  </span>
                ) : null}

                {row.isOfficialEndpoint ? (
                  <span
                    className={`${styles.metaPill} ${styles.metaPillInfo}`}
                    title={t('ai_providers.official_default_endpoint', {
                      defaultValue: '官方默认端点',
                    })}
                  >
                    {t('ai_providers.official_endpoint_badge', { defaultValue: '官方' })}
                  </span>
                ) : null}

                {row.prefix ? (
                  <button
                    type="button"
                    className={styles.prefixPill}
                    title={t('ai_providers.endpoint_prefix_hint', {
                      prefix: row.prefix,
                      defaultValue: `路由前缀：调用 ${row.prefix}/<模型名> 时命中此渠道（点击复制）`,
                    })}
                    aria-label={`${t('ai_providers.endpoint_prefix_label', { defaultValue: '前缀' })} ${row.prefix}/`}
                    onClick={(event) => void handleCopyText(event, row.prefix, prefixCopyKey)}
                  >
                    <span>
                      {t('ai_providers.endpoint_prefix_label', { defaultValue: '前缀' })}
                    </span>
                    <code>{`${row.prefix}/`}</code>
                    {isPrefixCopied ? <IconCheck size={11} /> : null}
                  </button>
                ) : null}

                {row.hasProxy ? (
                  <span
                    className={`${styles.metaPill} ${styles.metaPillNeutral}`}
                    title={t('ai_providers.endpoint_proxy_hint', {
                      defaultValue: '已为该渠道配置上游代理地址',
                    })}
                  >
                    {t('ai_providers.endpoint_proxy_badge', { defaultValue: '代理' })}
                  </span>
                ) : null}

                {cloakBadgeText ? (
                  <span
                    className={`${styles.metaPill} ${styles.metaPillNeutral}`}
                    title={t('ai_providers.endpoint_cloak_hint', {
                      mode: row.cloakMode,
                      defaultValue: `请求伪装 (${row.cloakMode})：按指定策略模拟官方客户端请求特征`,
                    })}
                  >
                    {cloakBadgeText}
                  </span>
                ) : null}

                {row.websockets ? (
                  <span
                    className={`${styles.metaPill} ${styles.metaPillNeutral}`}
                    title={t('ai_providers.endpoint_ws_hint', {
                      defaultValue: '已开启 WebSocket 传输',
                    })}
                  >
                    WS
                  </span>
                ) : null}
              </div>

              {row.baseUrl ? (
                <button
                  type="button"
                  className={styles.cardEndpointBar}
                  title={`${row.baseUrl} (${t('common.copy')})`}
                  aria-label={`${t('common.copy')} ${row.baseUrl}`}
                  onClick={(event) => void handleCopyText(event, row.baseUrl, urlCopyKey)}
                >
                  <IconSatellite size={12} className={styles.cardEndpointIcon} />
                  <span className={styles.cardEndpointText}>{endpointText}</span>
                  <span className={styles.cardEndpointCopyHint}>
                    {isUrlCopied ? (
                      <>
                        <IconCheck size={11} />
                        <span>{t('common.copied', { defaultValue: '已复制' })}</span>
                      </>
                    ) : (
                      <IconCopy size={11} />
                    )}
                  </span>
                </button>
              ) : (
                <div className={`${styles.cardEndpointBar} ${styles.cardEndpointBarStatic}`}>
                  <IconSatellite size={12} className={styles.cardEndpointIcon} />
                  <span className={styles.cardEndpointText}>{endpointText}</span>
                </div>
              )}
            </div>

            {/* 3. Historical Usage 4-Tile KPI Grid */}
            <div className={styles.cardHistorySection}>
              <div className={styles.cardSectionHeader}>
                <span className={styles.cardSectionTitle}>
                  {t('ai_providers.card_history_title', { defaultValue: '历史用量' })}
                </span>
                {lastSeenCompact ? (
                  <span
                    className={styles.cardSectionMeta}
                    title={lastSeenFull ?? undefined}
                  >
                    {lastSeenCompact}
                  </span>
                ) : null}
              </div>

              <div className={styles.cardHistoryGrid}>
                <div className={`${styles.historyMetric} ${styles.historyMetricRequests}`}>
                  <span className={styles.historyMetricIcon} aria-hidden="true">
                    <IconSend size={12} />
                  </span>
                  <span className={styles.historyMetricLabel}>
                    {t('ai_providers.card_metric_requests', { defaultValue: '请求' })}
                  </span>
                  <strong className={totalCalls > 0 ? undefined : styles.metricValueMuted}>
                    {totalCalls > 0 ? formatCompactTokens(totalCalls) : '-'}
                  </strong>
                </div>

                <div className={`${styles.historyMetric} ${styles.historyMetricTokens}`}>
                  <span className={styles.historyMetricIcon} aria-hidden="true">
                    <IconBinary size={12} />
                  </span>
                  <span className={styles.historyMetricLabel}>
                    {t('ai_providers.card_metric_tokens', { defaultValue: 'Token' })}
                  </span>
                  <strong
                    className={
                      hasHistoryMetrics && history ? undefined : styles.metricValueMuted
                    }
                  >
                    {hasHistoryMetrics && history
                      ? formatCompactTokens(history.totalTokens)
                      : '-'}
                  </strong>
                </div>

                <div className={`${styles.historyMetric} ${styles.historyMetricCost}`}>
                  <span className={styles.historyMetricIcon} aria-hidden="true">
                    <IconDollarSign size={12} />
                  </span>
                  <span className={styles.historyMetricLabel}>
                    {t('ai_providers.card_metric_cost', { defaultValue: '费用' })}
                  </span>
                  <strong
                    className={
                      hasHistoryMetrics && history && history.totalCost !== null
                        ? undefined
                        : styles.metricValueMuted
                    }
                  >
                    {hasHistoryMetrics && history && history.totalCost !== null
                      ? formatCompactHistoryUsd(history.totalCost)
                      : '-'}
                  </strong>
                </div>

                <div className={`${styles.historyMetric} ${styles.historyMetricSuccess}`}>
                  <span className={styles.historyMetricIcon} aria-hidden="true">
                    <IconCheck size={12} />
                  </span>
                  <span className={styles.historyMetricLabel}>
                    {t('ai_providers.card_metric_success_rate', { defaultValue: '成功率' })}
                  </span>
                  <strong className={rateToneClass}>
                    {displayRate !== null ? `${displayRate}%` : '-'}
                  </strong>
                </div>
              </div>
            </div>

            {/* 4. Recent Status (CPA 200-min Window) */}
            <div className={styles.cardRecentSection}>
              <div className={styles.cardSectionHeader}>
                <span className={styles.cardSectionTitleStrong}>
                  {t('ai_providers.card_recent_status_title', { defaultValue: '最近状态' })}
                </span>
                <div className={styles.cardRecentCounts}>
                  <span
                    className={styles.recentCountSuccess}
                    title={`${t('ai_providers.card_recent_success_label', { defaultValue: '近期成功' })}: ${recentWindowSuccess}`}
                  >
                    <span className={styles.metricDot} aria-hidden="true" />
                    <strong>{recentWindowSuccess}</strong>
                  </span>
                  <span
                    className={`${styles.recentCountFailure} ${
                      recentWindowFailure > 0 ? styles.recentCountFailureActive : ''
                    }`}
                    title={`${t('ai_providers.card_recent_failure_label', { defaultValue: '近期失败' })}: ${recentWindowFailure}`}
                  >
                    <span className={styles.metricDot} aria-hidden="true" />
                    <strong>{recentWindowFailure}</strong>
                  </span>
                </div>
              </div>

              <div className={styles.cardRecentBarRow}>
                <div className={styles.cardRecentBarWrap}>
                  <ProviderStatusBar statusData={cardStatusData} hideRate />
                </div>
                <span
                  className={`${styles.recentTrailPill} ${recentTrailToneClass}`}
                  title={showRecentErrorBadge ? statusInfo.title : undefined}
                >
                  {showRecentErrorBadge
                    ? latestFailureBadgeText
                    : recentWindowRate !== null
                      ? `${recentWindowRate}%`
                      : '--'}
                </span>
              </div>

              {failureBannerSummary ? (
                <div
                  className={styles.cardFailureBanner}
                  title={statusInfo.title ?? failureBannerSummary}
                >
                  <span className={styles.cardFailureBannerLabel}>
                    {t('ai_providers.card_latest_failure_label', { defaultValue: '最近失败' })}
                  </span>
                  <span className={styles.cardFailureBannerText}>{failureBannerSummary}</span>
                </div>
              ) : null}
            </div>

            {/* 5. Full-Width Models Section */}
            <div className={styles.cardModelsSection}>
              <div className={styles.cardSectionHeader}>
                <span className={styles.cardSectionTitleStrong}>
                  {t('ai_providers.card_models_title', { defaultValue: '模型映射' })}
                </span>
                {modelSummaryItems.length > 0 ? (
                  <div className={styles.cardModelsSummary}>
                    {modelSummaryItems.map((item, idx) => (
                      <span
                        key={item.key}
                        className={item.warn ? styles.cardModelsSummaryWarn : undefined}
                      >
                        {idx > 0 ? (
                          <span className={styles.metaSep} aria-hidden="true">·</span>
                        ) : null}
                        <span>{item.text}</span>
                      </span>
                    ))}
                  </div>
                ) : null}
              </div>

              <div className={styles.cardModelsTriggerWrap}>
                <ProviderModelsTrigger
                  row={row}
                  disabled={actionsDisabled}
                  onOpenModels={handleOpenModels}
                  maxVisible={3}
                  hideSummaryLine
                />
              </div>
            </div>

            {/* 6. Footer: Left Danger | Right High-Frequency Actions + Detail CTA */}
            <div
              className={styles.cardFooter}
              onClick={stopPropagation}
              onKeyDown={(event) => event.stopPropagation()}
            >
              <div className={styles.cardFooterLeft}>
                <Button
                  type="button"
                  variant="ghost"
                  size="xs"
                  iconOnly
                  className={styles.cardDeleteButton}
                  onClick={() => onDelete(row)}
                  disabled={actionsDisabled}
                  aria-label={t('common.delete')}
                  title={t('common.delete')}
                >
                  <IconTrash2 size={14} />
                </Button>
              </div>

              <div className={styles.cardFooterRight}>
                {onHealthCheckRow ? (
                  <Button
                    type="button"
                    variant="ghost"
                    size="xs"
                    iconOnly
                    className={styles.cardActionButton}
                    onClick={() => onHealthCheckRow(row)}
                    disabled={actionsDisabled}
                    aria-label={t('ai_providers.row_health_check', { defaultValue: '测活' })}
                    title={t('ai_providers.row_health_check', { defaultValue: '测活' })}
                  >
                    <IconShield size={14} />
                  </Button>
                ) : null}
                <Button
                  type="button"
                  variant="ghost"
                  size="xs"
                  iconOnly
                  className={styles.cardActionButton}
                  onClick={() => handleOpenModels(row)}
                  disabled={actionsDisabled}
                  aria-label={t('ai_providers.models_action', { defaultValue: '模型配置' })}
                  title={t('ai_providers.models_action', { defaultValue: '模型配置' })}
                >
                  <IconModelCluster size={14} />
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="xs"
                  iconOnly
                  className={styles.cardActionButton}
                  onClick={() => onEdit(row)}
                  disabled={actionsDisabled}
                  aria-label={t('common.edit')}
                  title={t('common.edit')}
                >
                  <IconSettings size={14} />
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="xs"
                  className={styles.cardDetailButton}
                  onClick={() => onInspect?.(row)}
                  disabled={!onInspect}
                  aria-label={t('ai_providers.detail_button')}
                >
                  {t('ai_providers.detail_button')}
                </Button>
              </div>
            </div>
          </article>
        );
      })}
    </div>
  );
}
