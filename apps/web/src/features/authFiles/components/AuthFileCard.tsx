import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import {
  IconDownload,
  IconInfo,
  IconModelCluster,
  IconRefreshCw,
  IconSettings,
  IconTrash2,
} from '@/components/ui/icons';
import { ProviderStatusBar } from '@/components/providers/ProviderStatusBar';
import type { AuthFileItem, CodexQuotaState } from '@/types';
import { resolveAuthProvider } from '@/utils/quota';
import { formatCompactNumber, formatUsd } from '@/utils/usage';
import {
  normalizeRecentRequestAuthIndex,
  normalizeRecentRequestBuckets,
  normalizeUsageTotal,
  statusBarDataFromRecentRequests,
} from '@/utils/recentRequests';
import { formatFileSize, formatUnixTimestamp } from '@/utils/format';
import {
  QUOTA_PROVIDER_TYPES,
  formatModified,
  getAuthFileStatusMessage,
  getTypeColor,
  getTypeLabel,
  isRuntimeOnlyAuthFile,
  normalizeProviderKey,
  parsePriorityValue,
  type QuotaProviderType,
  type ResolvedTheme,
} from '@/features/authFiles/constants';
import type { AuthFileStatusBarData } from '@/features/authFiles/hooks/useAuthFilesStatusBarCache';
import type { AntigravitySubscriptionState } from '@/features/authFiles/hooks/useAntigravitySubscriptions';
import type { AuthFileCodexStatusBadge } from '@/features/authFiles/model/authFilesPageModel';
import type { AuthFileUsageSummary } from '@/features/authFiles/model/authFileUsageSummary';
import type { QuotaCooldownInfo } from '@/services/api/usageService';
import { AuthFileQuotaSection } from '@/features/authFiles/components/AuthFileQuotaSection';
import styles from '@/features/authFiles/AuthFilesPage.module.scss';

const HEALTHY_STATUS_MESSAGES = new Set(['ok', 'healthy', 'ready', 'success', 'available']);

export type AuthFileCardProps = {
  file: AuthFileItem;
  compact: boolean;
  selected: boolean;
  resolvedTheme: ResolvedTheme;
  disableControls: boolean;
  deleting: string | null;
  statusUpdating: Record<string, boolean>;
  statusBarCache: Map<string, AuthFileStatusBarData>;
  codexStatusBadges?: AuthFileCodexStatusBadge[];
  codexNeedsReauth?: boolean;
  codexDisplayQuota?: CodexQuotaState;
  usageSummary?: AuthFileUsageSummary;
  antigravitySubscription?: AntigravitySubscriptionState;
  onRefreshAntigravitySubscription?: (file: AuthFileItem) => void;
  quotaCooldown?: QuotaCooldownInfo;
  onShowModels: (file: AuthFileItem) => void;
  onReauth?: (file: AuthFileItem) => void;
  onDownload: (name: string) => void;
  onOpenPrefixProxyEditor: (file: AuthFileItem) => void;
  onDelete: (name: string) => void;
  onToggleStatus: (file: AuthFileItem, enabled: boolean) => void;
  onToggleSelect: (name: string) => void;
};

const resolveQuotaType = (file: AuthFileItem): QuotaProviderType | null => {
  const provider = resolveAuthProvider(file);
  if (!QUOTA_PROVIDER_TYPES.has(provider as QuotaProviderType)) return null;
  return provider as QuotaProviderType;
};

const getProjectIdValue = (file: AuthFileItem): string => {
  const raw =
    file.project_id ?? file.projectId ?? file.gemini_virtual_project ?? file.geminiVirtualProject;
  return typeof raw === 'string' ? raw.trim() : '';
};

export function AuthFileCard(props: AuthFileCardProps) {
  const { t } = useTranslation();
  const {
    file,
    compact,
    selected,
    resolvedTheme,
    disableControls,
    deleting,
    statusUpdating,
    statusBarCache,
    codexStatusBadges = [],
    codexNeedsReauth = false,
    codexDisplayQuota,
    usageSummary,
    antigravitySubscription,
    onRefreshAntigravitySubscription,
    quotaCooldown,
    onShowModels,
    onReauth,
    onDownload,
    onOpenPrefixProxyEditor,
    onDelete,
    onToggleStatus,
    onToggleSelect,
  } = props;

  const recentBuckets = normalizeRecentRequestBuckets(file.recent_requests ?? file.recentRequests);
  const fileStats = {
    success: normalizeUsageTotal(file.success),
    failure: normalizeUsageTotal(file.failed),
  };
  const isRuntimeOnly = isRuntimeOnlyAuthFile(file);
  const resolvedProvider = resolveAuthProvider(file);
  const providerKey = normalizeProviderKey(String(file.type ?? file.provider ?? 'unknown'));
  const isAntigravity = resolvedProvider === 'antigravity';
  const isAistudio = providerKey === 'aistudio';
  const showModelsButton = !isRuntimeOnly || isAistudio;
  const typeColor = getTypeColor(providerKey, resolvedTheme);
  const typeLabel = getTypeLabel(t, providerKey);

  const quotaType = resolveQuotaType(file);
  const showQuotaLayout = Boolean(quotaType) && !isRuntimeOnly && !compact;
  const showUsageSummary = Boolean(usageSummary) && !compact;

  const providerCardClass =
    quotaType === 'antigravity'
      ? styles.antigravityCard
      : quotaType === 'claude'
        ? styles.claudeCard
        : quotaType === 'codex'
          ? styles.codexCard
          : quotaType === 'kimi'
            ? styles.kimiCard
            : '';

  const rawAuthIndex = file['auth_index'] ?? file.authIndex;
  const authIndexKey = normalizeRecentRequestAuthIndex(rawAuthIndex);
  const statusData =
    (authIndexKey && statusBarCache.get(authIndexKey)) ||
    statusBarDataFromRecentRequests(recentBuckets);
  const rawStatusMessage = getAuthFileStatusMessage(file);
  const hasStatusWarning =
    Boolean(rawStatusMessage) && !HEALTHY_STATUS_MESSAGES.has(rawStatusMessage.toLowerCase());

  const priorityValue = parsePriorityValue(file.priority ?? file['priority']);
  const projectIdValue = getProjectIdValue(file);
  const noteValue = typeof file.note === 'string' ? file.note.trim() : '';
  const subscription =
    isAntigravity && !isRuntimeOnly ? antigravitySubscription : undefined;
  const subscriptionData = subscription?.status === 'success' ? subscription.data : undefined;
  const isSubscriptionLoading = subscription?.status === 'loading';
  const subscriptionPlanLabel =
    subscriptionData?.plan === 'free'
      ? t('antigravity_subscription.plan_free')
      : subscriptionData?.plan === 'pro'
        ? t('antigravity_subscription.plan_pro')
        : subscriptionData?.plan === 'ultra'
          ? t('antigravity_subscription.plan_ultra')
          : subscriptionData?.plan === 'ultra-lite'
            ? t('antigravity_subscription.plan_ultra_lite')
            : subscriptionData
              ? subscriptionData.tierName ||
                subscriptionData.tierId ||
                t('antigravity_subscription.plan_unknown')
              : '';
  const subscriptionBadgeLabel =
    isSubscriptionLoading
      ? t('antigravity_subscription.loading_short')
      : subscription?.status === 'error'
      ? t('antigravity_subscription.error_badge')
      : subscriptionData
        ? t('antigravity_subscription.plan_badge', {
            plan: subscriptionPlanLabel,
          })
        : '';
  const subscriptionTitle =
    subscription?.status === 'error'
      ? subscription.error || t('common.unknown_error')
      : subscriptionData?.tierName && subscriptionData.tierId
        ? `${subscriptionData.tierName} (${subscriptionData.tierId})`
        : subscriptionData?.tierName || subscriptionData?.tierId || subscriptionBadgeLabel;
  const subscriptionBadgeClass =
    isSubscriptionLoading
      ? styles.subscriptionBadgeLoading
      : subscription?.status === 'error'
      ? styles.subscriptionBadgeError
      : subscriptionData?.plan === 'free'
        ? styles.subscriptionBadgeFree
        : subscriptionData?.plan === 'unknown'
          ? styles.subscriptionBadgeUnknown
          : styles.subscriptionBadgePaid;
  const subscriptionErrorMessage =
    subscription?.status === 'error'
      ? subscription.error || t('common.unknown_error')
      : '';
  const showSubscriptionRefreshButton =
    isAntigravity &&
    !isRuntimeOnly &&
    !subscriptionBadgeLabel &&
    Boolean(onRefreshAntigravitySubscription);
  const stateLabel = isRuntimeOnly
    ? t('auth_files.type_virtual') || '虚拟认证文件'
    : file.disabled
      ? t('auth_files.health_status_disabled')
      : hasStatusWarning
        ? t('auth_files.health_status_warning')
        : rawStatusMessage
          ? t('auth_files.health_status_healthy')
          : t('auth_files.status_toggle_label');
  const stateBadgeClass = isRuntimeOnly
    ? styles.stateBadgeVirtual
    : file.disabled
      ? styles.stateBadgeDisabled
      : hasStatusWarning
        ? styles.stateBadgeWarning
        : styles.stateBadgeActive;
  const codexStatusBadgeClassByTone = {
    danger: styles.codexStatusBadgeDanger,
    warning: styles.codexStatusBadgeWarning,
    info: styles.codexStatusBadgeInfo,
  } satisfies Record<AuthFileCodexStatusBadge['tone'], string>;
  const formatUsageTokens = (value: number) => formatCompactNumber(Math.round(value));
  const formatEstimatedTokens = (value: number | null | undefined) =>
    value && value > 0
      ? `~${formatCompactNumber(Math.round(value))}`
      : t('auth_files.usage_estimate_unavailable');
  const formatEstimatedUsd = (value: number | null | undefined) =>
    value !== null && value !== undefined && value >= 0
      ? `~${formatUsd(value)}`
      : t('auth_files.usage_estimate_unavailable');

  return (
    <div
      className={`${styles.fileCard} ${compact ? styles.fileCardCompact : ''} ${providerCardClass} ${selected ? styles.fileCardSelected : ''} ${file.disabled ? styles.fileCardDisabled : ''}`}
    >
      <div className={styles.fileCardLayout}>
        <div className={styles.fileCardMain}>
          <div className={styles.cardHeader}>
            {!isRuntimeOnly && (
              <SelectionCheckbox
                checked={selected}
                onChange={() => onToggleSelect(file.name)}
                className={styles.cardSelection}
                aria-label={
                  selected ? t('auth_files.batch_deselect') : t('auth_files.batch_select_all')
                }
                title={selected ? t('auth_files.batch_deselect') : t('auth_files.batch_select_all')}
              />
            )}
            <div className={styles.cardHeaderContent}>
              <div className={styles.cardBadgeRow}>
                <span
                  className={styles.typeBadge}
                  style={{
                    backgroundColor: typeColor.bg,
                    color: typeColor.text,
                    ...(typeColor.border ? { border: typeColor.border } : {}),
                  }}
                >
                  {typeLabel}
                </span>
                <span className={`${styles.stateBadge} ${stateBadgeClass}`}>{stateLabel}</span>
                {subscriptionBadgeLabel && (
                  <span
                    className={`${styles.subscriptionBadge} ${subscriptionBadgeClass}`}
                    title={subscriptionTitle}
                  >
                    {isSubscriptionLoading && (
                      <LoadingSpinner size={10} className={styles.subscriptionBadgeSpinner} />
                    )}
                    {subscriptionBadgeLabel}
                  </span>
                )}
                {showSubscriptionRefreshButton && (
                  <button
                    type="button"
                    className={styles.subscriptionRefreshButton}
                    title={t('antigravity_subscription.refresh_button')}
                    onClick={() => onRefreshAntigravitySubscription?.(file)}
                    disabled={disableControls}
                  >
                    {t('antigravity_subscription.refresh_short')}
                  </button>
                )}
                {codexStatusBadges.map((badge) => {
                  const label = t(badge.labelKey, {
                    defaultValue: badge.defaultLabel,
                    ...badge.labelParams,
                  });
                  const title = badge.titleKey
                    ? t(badge.titleKey, {
                        defaultValue: badge.defaultTitle ?? badge.defaultLabel,
                        ...badge.labelParams,
                      })
                    : (badge.defaultTitle ?? label);

                  return (
                    <span
                      key={badge.kind}
                      className={`${styles.codexStatusBadge} ${codexStatusBadgeClassByTone[badge.tone]}`}
                      title={title}
                    >
                      {label}
                    </span>
                  );
                })}
                {quotaCooldown && (
                  <span
                    className={`${styles.codexStatusBadge} ${styles.codexStatusBadgeInfo} ${styles.quotaCooldownBadge}`}
                    title={t('auth_files.quota_cooldown_badge_title', {
                      recoverAt: formatUnixTimestamp(quotaCooldown.recoverAtMs),
                      owner: quotaCooldown.owner || 'cpamp_usage_429',
                      defaultValue:
                        'This auth file is in a CPAMP-managed quota cooldown and will be recovered automatically. It is not the native CPA disabled state. Owner: {{owner}}. Expected recovery: {{recoverAt}}.',
                    })}
                  >
                    {t('auth_files.quota_cooldown_badge', {
                      recoverAt: formatUnixTimestamp(quotaCooldown.recoverAtMs),
                      defaultValue: 'Cooldown until {{recoverAt}}',
                    })}
                  </span>
                )}
              </div>
              <span className={styles.fileName} title={file.name}>
                {file.name}
              </span>
              {!compact && noteValue && (
                <div className={styles.noteText} title={noteValue}>
                  <span className={styles.noteLabel}>{t('auth_files.note_display')}</span>
                  <span className={styles.noteValue}>{noteValue}</span>
                </div>
              )}
            </div>
          </div>

          <div className={`${styles.cardMeta} ${compact ? styles.cardMetaCompact : ''}`}>
            <div className={styles.metaItem}>
              <span className={styles.metaLabel}>{t('auth_files.file_size')}</span>
              <span className={styles.metaValue}>
                {file.size ? formatFileSize(file.size) : '-'}
              </span>
            </div>
            <div className={styles.metaItem}>
              <span className={styles.metaLabel}>{t('auth_files.file_modified')}</span>
              <span className={styles.metaValue}>{formatModified(file)}</span>
            </div>
            {priorityValue !== undefined && (
              <div className={`${styles.metaItem} ${styles.priorityBadge}`}>
                <span className={styles.metaLabel}>{t('auth_files.priority_display')}</span>
                <span className={`${styles.metaValue} ${styles.priorityValue}`}>
                  {priorityValue}
                </span>
              </div>
            )}
            {projectIdValue && (
              <div className={styles.metaItem} title={projectIdValue}>
                <span className={styles.metaLabel}>{t('auth_files.project_id_display')}</span>
                <span className={styles.metaValue}>{projectIdValue}</span>
              </div>
            )}
          </div>

          {rawStatusMessage && hasStatusWarning && (
            <div className={styles.healthStatusMessage} title={rawStatusMessage}>
              <IconInfo className={styles.messageIcon} size={14} />
              <span>{rawStatusMessage}</span>
            </div>
          )}

          {subscriptionErrorMessage && (
            <div className={styles.subscriptionError} title={subscriptionErrorMessage}>
              <IconInfo className={styles.messageIcon} size={14} />
              <span>
                {t('antigravity_subscription.load_failed', {
                  message: subscriptionErrorMessage,
                })}
              </span>
            </div>
          )}

          <div className={`${styles.cardInsights} ${compact ? styles.cardInsightsCompact : ''}`}>
            <div className={`${styles.cardStats} ${compact ? styles.cardStatsCompact : ''}`}>
              <div className={`${styles.statPill} ${styles.statSuccess}`}>
                <span className={styles.statLabel}>{t('stats.success')}</span>
                <span className={styles.statValue}>{fileStats.success}</span>
              </div>
              <div className={`${styles.statPill} ${styles.statFailure}`}>
                <span className={styles.statLabel}>{t('stats.failure')}</span>
                <span className={styles.statValue}>{fileStats.failure}</span>
              </div>
            </div>

            {showUsageSummary && usageSummary && (
              <div className={styles.usageSummaryPanel} title={t('auth_files.usage_summary_title')}>
                <div className={styles.usageSummaryItem}>
                  <span className={styles.usageSummaryLabel}>
                    {t('auth_files.usage_column_recorded')}
                  </span>
                  <span className={styles.usageSummaryMetric}>
                    {t('auth_files.usage_metric_tokens')}
                  </span>
                  <span className={styles.usageSummaryValue}>
                    {formatUsageTokens(usageSummary.totalTokens)}
                  </span>
                </div>
                <div className={styles.usageSummaryItem}>
                  <span className={styles.usageSummaryLabel}>
                    {t('auth_files.usage_column_weekly_projection')}
                  </span>
                  <span className={styles.usageSummaryMetric}>
                    {t('auth_files.usage_metric_tokens')}
                  </span>
                  <span className={styles.usageSummaryValue}>
                    {formatEstimatedTokens(usageSummary.codexWeeklyLimitTokens)}
                  </span>
                </div>
                <div className={styles.usageSummaryItem}>
                  <span className={styles.usageSummaryLabel}>
                    {t('auth_files.usage_column_five_hour_projection')}
                  </span>
                  <span className={styles.usageSummaryMetric}>
                    {t('auth_files.usage_metric_tokens')}
                  </span>
                  <span className={styles.usageSummaryValue}>
                    {formatEstimatedTokens(usageSummary.codexFiveHourLimitTokens)}
                  </span>
                </div>
                <div className={styles.usageSummaryItem}>
                  <span className={styles.usageSummaryLabel}>
                    {t('auth_files.usage_column_recorded')}
                  </span>
                  <span className={styles.usageSummaryMetric}>
                    {t('auth_files.usage_metric_cost')}
                  </span>
                  <span className={styles.usageSummaryValue}>
                    {formatUsd(usageSummary.estimatedCost)}
                  </span>
                </div>
                <div className={styles.usageSummaryItem}>
                  <span className={styles.usageSummaryLabel}>
                    {t('auth_files.usage_column_weekly_projection')}
                  </span>
                  <span className={styles.usageSummaryMetric}>
                    {t('auth_files.usage_metric_value')}
                  </span>
                  <span className={styles.usageSummaryValue}>
                    {formatEstimatedUsd(usageSummary.codexWeeklyLimitCost)}
                  </span>
                </div>
                <div className={styles.usageSummaryItem}>
                  <span className={styles.usageSummaryLabel}>
                    {t('auth_files.usage_column_five_hour_projection')}
                  </span>
                  <span className={styles.usageSummaryMetric}>
                    {t('auth_files.usage_metric_value')}
                  </span>
                  <span className={styles.usageSummaryValue}>
                    {formatEstimatedUsd(usageSummary.codexFiveHourLimitCost)}
                  </span>
                </div>
              </div>
            )}

            <div className={`${styles.statusPanel} ${compact ? styles.statusPanelCompact : ''}`}>
              <div className={styles.statusPanelLabel}>
                <span>{t('auth_files.health_status_label')}</span>
              </div>
              <ProviderStatusBar statusData={statusData} styles={styles} />
            </div>

            {showQuotaLayout && quotaType && (
              <AuthFileQuotaSection
                file={file}
                quotaType={quotaType}
                disableControls={disableControls}
                quotaOverride={quotaType === 'codex' ? (codexDisplayQuota ?? null) : undefined}
              />
            )}
          </div>

          <div className={styles.cardActions}>
            <div className={styles.cardActionsMain}>
              {(showModelsButton || !isRuntimeOnly) && (
                <div className={styles.cardUtilityActions}>
                  {showModelsButton && (
                    <Button
                      variant="secondary"
                      size="sm"
                      onClick={() => onShowModels(file)}
                      className={`${styles.primaryActionButton} ${styles.modelsActionButton}`}
                      title={t('auth_files.models_button', { defaultValue: '模型' })}
                      aria-label={t('auth_files.models_button', { defaultValue: '模型' })}
                      disabled={disableControls}
                    >
                      <span className={styles.modelsActionIconWrap}>
                        <IconModelCluster className={styles.actionIcon} size={16} />
                      </span>
                    </Button>
                  )}
                  {!isRuntimeOnly && (
                    <>
                      <Button
                        variant="secondary"
                        size="sm"
                        onClick={() => onDownload(file.name)}
                        className={styles.iconButton}
                        title={t('auth_files.download_button')}
                        disabled={disableControls}
                      >
                        <IconDownload className={styles.actionIcon} size={16} />
                      </Button>
                      {codexNeedsReauth && onReauth ? (
                        <Button
                          variant="secondary"
                          size="sm"
                          onClick={() => onReauth(file)}
                          className={styles.iconButton}
                          title={t('codex_reauth.button')}
                          aria-label={t('codex_reauth.button')}
                          disabled={disableControls}
                        >
                          <IconRefreshCw className={styles.actionIcon} size={16} />
                        </Button>
                      ) : null}
                      <Button
                        variant="secondary"
                        size="sm"
                        onClick={() => onOpenPrefixProxyEditor(file)}
                        className={styles.iconButton}
                        title={t('auth_files.prefix_proxy_button')}
                        disabled={disableControls}
                      >
                        <IconSettings className={styles.actionIcon} size={16} />
                      </Button>
                      <Button
                        variant="danger"
                        size="sm"
                        onClick={() => onDelete(file.name)}
                        className={styles.iconButton}
                        title={t('auth_files.delete_button')}
                        disabled={disableControls || deleting === file.name}
                      >
                        {deleting === file.name ? (
                          <LoadingSpinner size={14} />
                        ) : (
                          <IconTrash2 className={styles.actionIcon} size={16} />
                        )}
                      </Button>
                    </>
                  )}
                </div>
              )}
            </div>
            {!isRuntimeOnly && (
              <div className={styles.statusToggle}>
                <span className={styles.statusToggleLabel}>
                  {t('auth_files.status_toggle_label')}
                </span>
                <ToggleSwitch
                  ariaLabel={t('auth_files.status_toggle_label')}
                  checked={!file.disabled}
                  disabled={disableControls || statusUpdating[file.name] === true}
                  onChange={(value) => onToggleStatus(file, value)}
                />
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
