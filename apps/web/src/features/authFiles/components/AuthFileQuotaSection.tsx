import { useCallback, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import {
  ANTIGRAVITY_CONFIG,
  CLAUDE_CONFIG,
  CODEX_CONFIG,
  getQuotaStoreKey,
  KIMI_CONFIG,
  XAI_CONFIG
} from '@/components/quota';
import {
  captureQuotaCacheGeneration,
  commitIfQuotaCacheCurrent,
  useNotificationStore,
  useQuotaStore,
} from '@/stores';
import type { AuthFileItem } from '@/types';
import { getStatusFromError } from '@/utils/quota';
import {
  isRuntimeOnlyAuthFile,
  resolveQuotaErrorMessage,
  type QuotaProviderType
} from '@/features/authFiles/constants';
import { QuotaProgressBar } from '@/features/authFiles/components/QuotaProgressBar';
import styles from '@/features/authFiles/AuthFilesPage.module.scss';

type QuotaState = { status?: string; error?: string; errorStatus?: number } | undefined;
type InlineQuotaConfig = {
  i18nPrefix: string;
  getStoreKey?: (file: AuthFileItem) => string;
  fetchQuota: (file: AuthFileItem, t: TFunction) => Promise<unknown>;
  buildLoadingState: (file?: AuthFileItem) => unknown;
  buildSuccessState: (data: unknown, file?: AuthFileItem) => unknown;
  buildErrorState: (message: string, status?: number, file?: AuthFileItem) => unknown;
  buildFailureState?: (
    message: string,
    status: number | undefined,
    file: AuthFileItem | undefined,
    activeState: unknown | undefined,
    failedAtMs: number
  ) => unknown;
  renderQuotaItems: (quota: unknown, t: TFunction, helpers: unknown) => unknown;
  scopeState?: (file: AuthFileItem, state: QuotaState) => QuotaState;
};

const getQuotaConfig = (type: QuotaProviderType) => {
  if (type === 'antigravity') return ANTIGRAVITY_CONFIG;
  if (type === 'claude') return CLAUDE_CONFIG;
  if (type === 'codex') return CODEX_CONFIG;
  if (type === 'kimi') return KIMI_CONFIG;
  return XAI_CONFIG;
};

export type AuthFileQuotaSectionProps = {
  file: AuthFileItem;
  quotaType: QuotaProviderType;
  disableControls: boolean;
  quotaOverride?: QuotaState | null;
};

export function AuthFileQuotaSection(props: AuthFileQuotaSectionProps) {
  const { file, quotaType, disableControls, quotaOverride } = props;
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const config = getQuotaConfig(quotaType) as unknown as InlineQuotaConfig;
  const storeKey = getQuotaStoreKey(config, file);

  const storedQuota = useQuotaStore((state) => {
    if (quotaType === 'antigravity') return state.antigravityQuota[storeKey] as QuotaState;
    if (quotaType === 'claude') return state.claudeQuota[storeKey] as QuotaState;
    if (quotaType === 'codex') {
      return (state.codexQuota[storeKey] ?? state.codexQuota[file.name]) as QuotaState;
    }
    if (quotaType === 'kimi') return state.kimiQuota[storeKey] as QuotaState;
    return state.xaiQuota[storeKey] as QuotaState;
  });
  const quota = config.scopeState ? config.scopeState(file, storedQuota) : storedQuota;

  const updateQuotaState = useQuotaStore((state) => {
    if (quotaType === 'antigravity') return state.setAntigravityQuota as unknown as (updater: unknown) => void;
    if (quotaType === 'claude') return state.setClaudeQuota as unknown as (updater: unknown) => void;
    if (quotaType === 'codex') return state.setCodexQuota as unknown as (updater: unknown) => void;
    if (quotaType === 'kimi') return state.setKimiQuota as unknown as (updater: unknown) => void;
    return state.setXaiQuota as unknown as (updater: unknown) => void;
  });

  const refreshQuotaForFile = useCallback(async () => {
    if (disableControls) return;
    if (isRuntimeOnlyAuthFile(file)) return;
    if (file.disabled) return;
    if (quota?.status === 'loading') return;
    const previousQuota = quota;
    const cacheGeneration = captureQuotaCacheGeneration();

    updateQuotaState((prev: Record<string, unknown>) => ({
      ...prev,
      [storeKey]: config.buildLoadingState(file)
    }));

    try {
      const data = await config.fetchQuota(file, t);
      commitIfQuotaCacheCurrent(cacheGeneration, () => {
        updateQuotaState((prev: Record<string, unknown>) => ({
          ...prev,
          [storeKey]: config.buildSuccessState(data, file)
        }));
        showNotification(t('auth_files.quota_refresh_success', { name: file.name }), 'success');
      });
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : t('common.unknown_error');
      const status = getStatusFromError(err);
      commitIfQuotaCacheCurrent(cacheGeneration, () => {
        updateQuotaState((prev: Record<string, unknown>) => ({
          ...prev,
          [storeKey]: config.buildFailureState
            ? config.buildFailureState(message, status, file, previousQuota, Date.now())
            : config.buildErrorState(message, status, file)
        }));
        showNotification(t('auth_files.quota_refresh_failed', { name: file.name, message }), 'error');
      });
    }
  }, [config, disableControls, file, quota, showNotification, storeKey, t, updateQuotaState]);

  const displayQuota = quotaOverride === undefined ? quota : (quotaOverride ?? undefined);
  const quotaStatus = displayQuota?.status ?? 'idle';
  const canRefreshQuota = !disableControls && !file.disabled;
  const quotaErrorMessage = resolveQuotaErrorMessage(
    t,
    displayQuota?.errorStatus,
    displayQuota?.error || t('common.unknown_error')
  );

  return (
    <div className={styles.quotaSection}>
      {quotaStatus === 'loading' ? (
        <div className={styles.quotaMessage}>{t(`${config.i18nPrefix}.loading`)}</div>
      ) : quotaStatus === 'idle' ? (
        <button
          type="button"
          className={`${styles.quotaMessage} ${styles.quotaMessageAction}`}
          onClick={() => void refreshQuotaForFile()}
          disabled={!canRefreshQuota}
        >
          {t(`${config.i18nPrefix}.idle`)}
        </button>
      ) : quotaStatus === 'error' ? (
        <div className={styles.quotaError}>
          {t(`${config.i18nPrefix}.load_failed`, {
            message: quotaErrorMessage
          })}
        </div>
      ) : displayQuota ? (
        (config.renderQuotaItems(displayQuota, t, { styles, QuotaProgressBar }) as ReactNode)
      ) : (
        <div className={styles.quotaMessage}>{t(`${config.i18nPrefix}.idle`)}</div>
      )}
    </div>
  );
}
