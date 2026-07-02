/**
 * Generic quota card component.
 */

import { useTranslation } from 'react-i18next';
import type { ReactElement, ReactNode } from 'react';
import type { TFunction } from 'i18next';
import { Button } from '@/components/ui/Button';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import {
  DEFAULT_QUOTA_ACCOUNT_DISPLAY_MODE,
  type QuotaAccountDisplayMode,
} from '@/features/quota/quotaPageUiState';
import type { AuthFileItem, ResolvedTheme, ThemeColors } from '@/types';
import { TYPE_COLORS } from '@/utils/quota';
import { resolveQuotaAccountDisplayText } from './quotaDisplay';
import styles from '@/features/quota/QuotaPage.module.scss';

type QuotaStatus = 'idle' | 'loading' | 'success' | 'error';

export interface QuotaStatusState {
  status: QuotaStatus;
  error?: string;
  errorStatus?: number;
}

export interface QuotaProgressBarProps {
  percent: number | null;
  highThreshold: number;
  mediumThreshold: number;
}

export function QuotaProgressBar({
  percent,
  highThreshold,
  mediumThreshold,
}: QuotaProgressBarProps) {
  const clamp = (value: number, min: number, max: number) => Math.min(max, Math.max(min, value));
  const normalized = percent === null ? null : clamp(percent, 0, 100);
  const fillClass =
    normalized === null
      ? styles.quotaBarFillMedium
      : normalized >= highThreshold
        ? styles.quotaBarFillHigh
        : normalized >= mediumThreshold
          ? styles.quotaBarFillMedium
          : styles.quotaBarFillLow;
  const widthPercent = Math.round(normalized ?? 0);

  return (
    <div className={styles.quotaBar}>
      <div
        className={`${styles.quotaBarFill} ${fillClass}`}
        style={{ width: `${widthPercent}%` }}
      />
    </div>
  );
}

export interface QuotaRenderHelpers {
  styles: typeof styles;
  QuotaProgressBar: (props: QuotaProgressBarProps) => ReactElement;
}

interface QuotaCardProps<TState extends QuotaStatusState> {
  item: AuthFileItem;
  quota?: TState;
  resolvedTheme: ResolvedTheme;
  i18nPrefix: string;
  cardIdleMessageKey?: string;
  cardClassName: string;
  defaultType: string;
  accountDisplayMode?: QuotaAccountDisplayMode;
  canRefresh?: boolean;
  onRefresh?: () => void;
  selected?: boolean;
  onToggleSelect?: (name: string, selected: boolean) => void;
  canReset?: boolean;
  resetLabel?: string;
  onReset?: () => void;
  canReauth?: boolean;
  onReauth?: () => void;
  deleteAuthFileAction?: ReactNode;
  renderQuotaItems: (quota: TState, t: TFunction, helpers: QuotaRenderHelpers) => ReactNode;
}

export function QuotaCard<TState extends QuotaStatusState>({
  item,
  quota,
  resolvedTheme,
  i18nPrefix,
  cardIdleMessageKey,
  cardClassName,
  defaultType,
  accountDisplayMode = DEFAULT_QUOTA_ACCOUNT_DISPLAY_MODE,
  canRefresh = false,
  onRefresh,
  selected = false,
  onToggleSelect,
  canReset = false,
  resetLabel,
  onReset,
  canReauth = false,
  onReauth,
  deleteAuthFileAction,
  renderQuotaItems,
}: QuotaCardProps<TState>) {
  const { t } = useTranslation();

  const displayType = item.type || item.provider || defaultType;
  const typeColorKey = String(displayType).trim().toLowerCase().replace(/_/g, '-');
  const normalizedTypeColorKey =
    typeColorKey === 'x-ai' || typeColorKey === 'grok' ? 'xai' : typeColorKey;
  const typeColorSet = TYPE_COLORS[normalizedTypeColorKey] || TYPE_COLORS.unknown;
  const typeColor: ThemeColors =
    resolvedTheme === 'dark' && typeColorSet.dark ? typeColorSet.dark : typeColorSet.light;

  const quotaStatus = quota?.status ?? 'idle';
  const quotaErrorMessage = resolveQuotaErrorMessage(
    t,
    quota?.errorStatus,
    quota?.error || t('common.unknown_error')
  );
  const idleMessageKey = onRefresh
    ? `${i18nPrefix}.idle`
    : (cardIdleMessageKey ?? `${i18nPrefix}.idle`);
  const accountDisplay = resolveQuotaAccountDisplayText(item, accountDisplayMode);

  const getTypeLabel = (type: string): string => {
    const key = `auth_files.filter_${type}`;
    const translated = t(key);
    if (translated !== key) return translated;
    if (type.toLowerCase() === 'iflow') return 'iFlow';
    return type.charAt(0).toUpperCase() + type.slice(1);
  };

  const renderActions = (options?: { includeReauth?: boolean }) =>
    onRefresh || (onReset && resetLabel) || (options?.includeReauth && onReauth) || deleteAuthFileAction ? (
      <div className={styles.quotaActions}>
        {options?.includeReauth && onReauth ? (
          <Button
            type="button"
            variant="secondary"
            size="sm"
            className={styles.quotaActionButton}
            onClick={onReauth}
            disabled={!canReauth}
          >
            {t('codex_reauth.button')}
          </Button>
        ) : null}
        {onReset && resetLabel ? (
          <Button
            type="button"
            variant="secondary"
            size="sm"
            className={styles.quotaActionButton}
            onClick={onReset}
            disabled={!canReset}
          >
            {resetLabel}
          </Button>
        ) : null}
        {onRefresh ? (
          <Button
            type="button"
            variant="secondary"
            size="sm"
            className={styles.quotaActionButton}
            onClick={onRefresh}
            disabled={!canRefresh}
          >
            {t(`${i18nPrefix}.refresh_button`)}
          </Button>
        ) : null}
        {deleteAuthFileAction}
      </div>
    ) : null;

  return (
    <div className={`${styles.fileCard} ${cardClassName}`}>
      <div className={styles.cardHeader}>
        {onToggleSelect ? (
          <SelectionCheckbox
            checked={selected}
            onChange={(value) => onToggleSelect(item.name, value)}
            className={styles.quotaCardSelection}
            ariaLabel={selected ? t('auth_files.batch_deselect') : t('auth_files.batch_select_all')}
            title={selected ? t('auth_files.batch_deselect') : t('auth_files.batch_select_all')}
          />
        ) : null}
        <span
          className={styles.typeBadge}
          style={{
            backgroundColor: typeColor.bg,
            color: typeColor.text,
            ...(typeColor.border ? { border: typeColor.border } : {}),
          }}
        >
          {getTypeLabel(displayType)}
        </span>
        <span className={styles.fileName} title={accountDisplay.title}>
          {accountDisplay.primary}
        </span>
      </div>

      <div className={styles.quotaSection}>
        {quotaStatus === 'loading' ? (
          <div className={styles.quotaMessage}>{t(`${i18nPrefix}.loading`)}</div>
        ) : quotaStatus === 'idle' ? (
          onRefresh ? (
            <button
              type="button"
              className={`${styles.quotaMessage} ${styles.quotaMessageAction}`}
              onClick={onRefresh}
              disabled={!canRefresh}
            >
              {t(idleMessageKey)}
            </button>
          ) : (
            <div className={styles.quotaMessage}>{t(idleMessageKey)}</div>
          )
        ) : quotaStatus === 'error' ? (
          <>
            <div className={styles.quotaError}>
              {t(`${i18nPrefix}.load_failed`, {
                message: quotaErrorMessage,
              })}
            </div>
            {renderActions({ includeReauth: canReauth })}
          </>
        ) : quota ? (
          <>
            {renderQuotaItems(quota, t, { styles, QuotaProgressBar })}
            {renderActions()}
          </>
        ) : (
          <div className={styles.quotaMessage}>{t(idleMessageKey)}</div>
        )}
      </div>
    </div>
  );
}

const resolveQuotaErrorMessage = (
  t: TFunction,
  status: number | undefined,
  fallback: string
): string => {
  if (status === 404) return t('common.quota_update_required');
  if (status === 403) return t('common.quota_check_credential');
  return fallback;
};
