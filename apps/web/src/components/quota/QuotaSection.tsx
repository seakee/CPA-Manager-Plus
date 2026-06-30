/**
 * Generic quota section component.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { triggerHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { authFilesApi } from '@/services/api';
import { useNotificationStore, useQuotaStore, useThemeStore } from '@/stores';
import type { AuthFileItem, ResolvedTheme } from '@/types';
import { getStatusFromError } from '@/utils/quota';
import {
  getHighConfidenceUsageHeaderSnapshotForAuthFile,
  type UsageHeaderSnapshotLookup,
} from '@/utils/usageHeaderSnapshots';
import { QuotaCard } from './QuotaCard';
import type { QuotaStatusState } from './QuotaCard';
import { useQuotaLoader } from './useQuotaLoader';
import {
  getQuotaStoreKey,
  resolveQuotaDisplayState,
  type QuotaConfig,
  type QuotaSortMode,
} from './quotaConfigs';
import { resolveQuotaAccountDisplayText } from './quotaDisplay';
import {
  DEFAULT_QUOTA_ACCOUNT_DISPLAY_MODE,
  type QuotaAccountDisplayMode,
  type QuotaSectionViewMode,
} from '@/features/quota/quotaPageUiState';
import { useGridColumns } from './useGridColumns';
import { IconEye, IconEyeOff, IconRefreshCw, IconTrash2 } from '@/components/ui/icons';
import styles from '@/features/quota/QuotaPage.module.scss';

type QuotaUpdater<T> = T | ((prev: T) => T);

type QuotaSetter<T> = (updater: QuotaUpdater<T>) => void;

const MAX_ITEMS_PER_PAGE = 25;
const MAX_SHOW_ALL_THRESHOLD = 30;
const PAGE_ROWS = 5;

const stringifySearchValue = (value: unknown): string[] => {
  if (value === undefined || value === null) return [];
  if (Array.isArray(value)) return value.flatMap(stringifySearchValue);
  if (typeof value === 'string') return value.trim() ? [value] : [];
  if (typeof value === 'number' || typeof value === 'boolean') return [String(value)];
  return [];
};

const compareFileName = (left: AuthFileItem, right: AuthFileItem) =>
  left.name.localeCompare(right.name, undefined, { numeric: true, sensitivity: 'base' });

interface QuotaPaginationState<T> {
  pageSize: number;
  totalPages: number;
  currentPage: number;
  pageItems: T[];
  setPageSize: (size: number) => void;
  goToPrev: () => void;
  goToNext: () => void;
  loading: boolean;
  loadingScope: 'page' | 'all' | null;
  setLoading: (loading: boolean, scope?: 'page' | 'all' | null) => void;
}

const useQuotaPagination = <T,>(items: T[], defaultPageSize = 6): QuotaPaginationState<T> => {
  const [page, setPage] = useState(1);
  const [pageSize, setPageSizeState] = useState(defaultPageSize);
  const [loading, setLoadingState] = useState(false);
  const [loadingScope, setLoadingScope] = useState<'page' | 'all' | null>(null);

  const totalPages = useMemo(
    () => Math.max(1, Math.ceil(items.length / pageSize)),
    [items.length, pageSize]
  );

  const currentPage = useMemo(() => Math.min(page, totalPages), [page, totalPages]);

  const pageItems = useMemo(() => {
    const start = (currentPage - 1) * pageSize;
    return items.slice(start, start + pageSize);
  }, [items, currentPage, pageSize]);

  const setPageSize = useCallback((size: number) => {
    setPageSizeState((prev) => {
      if (prev === size) return prev;
      setPage(1);
      return size;
    });
  }, []);

  const goToPrev = useCallback(() => {
    setPage((prev) => Math.max(1, prev - 1));
  }, []);

  const goToNext = useCallback(() => {
    setPage((prev) => Math.min(totalPages, prev + 1));
  }, [totalPages]);

  const setLoading = useCallback((isLoading: boolean, scope?: 'page' | 'all' | null) => {
    setLoadingState(isLoading);
    setLoadingScope(isLoading ? (scope ?? null) : null);
  }, []);

  return {
    pageSize,
    totalPages,
    currentPage,
    pageItems,
    setPageSize,
    goToPrev,
    goToNext,
    loading,
    loadingScope,
    setLoading,
  };
};

interface QuotaSectionProps<TState extends QuotaStatusState, TData> {
  config: QuotaConfig<TState, TData>;
  files: AuthFileItem[];
  loading: boolean;
  disabled: boolean;
  searchQuery?: string;
  sortMode?: QuotaSortMode;
  viewMode?: QuotaSectionViewMode;
  onViewModeChange?: (viewMode: QuotaSectionViewMode) => void;
  onReauthAccount?: (item: AuthFileItem) => void;
  accountDisplayMode?: QuotaAccountDisplayMode;
  onAccountDisplayModeChange?: (mode: QuotaAccountDisplayMode) => void;
  headerSnapshotLookup?: UsageHeaderSnapshotLookup;
}

export function QuotaSection<TState extends QuotaStatusState, TData>({
  config,
  files,
  loading,
  disabled,
  searchQuery = '',
  sortMode = 'default',
  viewMode,
  onViewModeChange,
  onReauthAccount,
  accountDisplayMode,
  onAccountDisplayModeChange,
  headerSnapshotLookup,
}: QuotaSectionProps<TState, TData>) {
  const { t } = useTranslation();
  const resolvedTheme: ResolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);
  const setQuota = useQuotaStore((state) => state[config.storeSetter]) as QuotaSetter<
    Record<string, TState>
  >;

  /* Removed useRef */
  const [columns, gridRef] = useGridColumns(380); // Min card width 380px matches SCSS
  const [internalViewMode, setInternalViewMode] = useState<QuotaSectionViewMode>('paged');
  const [internalAccountDisplayMode, setInternalAccountDisplayMode] =
    useState<QuotaAccountDisplayMode>(DEFAULT_QUOTA_ACCOUNT_DISPLAY_MODE);
  const [showTooManyWarning, setShowTooManyWarning] = useState(false);
  const [deletingFileNames, setDeletingFileNames] = useState<Set<string>>(() => new Set());
  const [deletedFileNames, setDeletedFileNames] = useState<Set<string>>(() => new Set());
  const [selectedFileNames, setSelectedFileNames] = useState<Set<string>>(() => new Set());
  const resolvedViewMode = viewMode ?? internalViewMode;
  const resolvedAccountDisplayMode = accountDisplayMode ?? internalAccountDisplayMode;
  const setViewMode = useCallback(
    (nextViewMode: QuotaSectionViewMode) => {
      if (onViewModeChange) {
        onViewModeChange(nextViewMode);
      } else {
        setInternalViewMode(nextViewMode);
      }
    },
    [onViewModeChange]
  );
  const setAccountDisplayMode = useCallback(
    (nextMode: QuotaAccountDisplayMode) => {
      if (onAccountDisplayModeChange) {
        onAccountDisplayModeChange(nextMode);
      } else {
        setInternalAccountDisplayMode(nextMode);
      }
    },
    [onAccountDisplayModeChange]
  );
  const getAccountDisplayName = useCallback(
    (file: AuthFileItem) =>
      resolveQuotaAccountDisplayText(file, resolvedAccountDisplayMode).primary,
    [resolvedAccountDisplayMode]
  );

  const filteredFiles = useMemo(
    () => files.filter((file) => config.filterFn(file) && !deletedFileNames.has(file.name)),
    [files, config, deletedFileNames]
  );
  const selectedCount = selectedFileNames.size;
  const normalizedSearchQuery = searchQuery.trim().toLowerCase();

  const { quota, loadQuota } = useQuotaLoader(config);

  const getScopedQuota = useCallback(
    (file: AuthFileItem): TState | undefined => {
      const storeKey = getQuotaStoreKey(config, file);
      const activeQuota = quota[storeKey];
      const scopedQuota = config.scopeState ? config.scopeState(file, activeQuota) : activeQuota;
      if (scopedQuota || storeKey === file.name) return scopedQuota;
      const legacyQuota = quota[file.name];
      return config.scopeState ? config.scopeState(file, legacyQuota) : legacyQuota;
    },
    [config, quota]
  );

  const getDisplayQuota = useCallback(
    (file: AuthFileItem): TState | undefined => {
      const activeQuota = getScopedQuota(file);
      const observedQuota = config.buildObservedState?.(
        file,
        getHighConfidenceUsageHeaderSnapshotForAuthFile(headerSnapshotLookup, file),
        t
      );
      return resolveQuotaDisplayState(activeQuota, observedQuota);
    },
    [config, getScopedQuota, headerSnapshotLookup, t]
  );

  const displayFiles = useMemo(() => {
    const matchesSearch = (file: AuthFileItem): boolean => {
      if (!normalizedSearchQuery) return true;
      const fileQuota = getDisplayQuota(file);
      const searchValues = [
        file.name,
        file.type,
        file.provider,
        file.authIndex,
        file['auth_index'],
        file.status,
        file.statusMessage,
        fileQuota?.status,
        fileQuota?.error,
        fileQuota?.errorStatus,
        ...(config.getSearchText?.(file, fileQuota, t) ?? []),
      ];

      return stringifySearchValue(searchValues).some((value) =>
        value.toLowerCase().includes(normalizedSearchQuery)
      );
    };

    const nextFiles = filteredFiles.filter(matchesSearch);
    const sortedFiles = [...nextFiles];

    if (sortMode === 'name-asc') {
      sortedFiles.sort(compareFileName);
      return sortedFiles;
    }

    if (sortMode === 'plan-asc' || sortMode === 'plan-desc') {
      sortedFiles.sort((left, right) => {
        const leftRank = config.getPlanSortRank?.(left, getDisplayQuota(left));
        const rightRank = config.getPlanSortRank?.(right, getDisplayQuota(right));
        const leftKnown = leftRank !== null && leftRank !== undefined;
        const rightKnown = rightRank !== null && rightRank !== undefined;

        if (leftKnown || rightKnown) {
          if (!leftKnown) return 1;
          if (!rightKnown) return -1;
          const rankDiff = sortMode === 'plan-desc' ? rightRank - leftRank : leftRank - rightRank;
          if (rankDiff !== 0) return rankDiff;
        }

        return compareFileName(left, right);
      });
    }

    return sortedFiles;
  }, [config, filteredFiles, getDisplayQuota, normalizedSearchQuery, sortMode, t]);

  const showAllAllowed = displayFiles.length <= MAX_SHOW_ALL_THRESHOLD;
  const effectiveViewMode: QuotaSectionViewMode =
    resolvedViewMode === 'all' && !showAllAllowed ? 'paged' : resolvedViewMode;

  const {
    pageSize,
    totalPages,
    currentPage,
    pageItems,
    setPageSize,
    goToPrev,
    goToNext,
    loading: sectionLoading,
    setLoading,
  } = useQuotaPagination(displayFiles);

  useEffect(() => {
    if (showAllAllowed) return;
    if (resolvedViewMode !== 'all') return;

    let cancelled = false;
    queueMicrotask(() => {
      if (cancelled) return;
      setViewMode('paged');
      setShowTooManyWarning(true);
    });

    return () => {
      cancelled = true;
    };
  }, [resolvedViewMode, setViewMode, showAllAllowed]);

  // Update page size based on view mode and columns
  useEffect(() => {
    if (effectiveViewMode === 'all') {
      setPageSize(Math.max(1, displayFiles.length));
    } else {
      // Paged mode: fixed rows * columns, capped to avoid oversized pages.
      setPageSize(Math.min(columns * PAGE_ROWS, MAX_ITEMS_PER_PAGE));
    }
  }, [effectiveViewMode, columns, displayFiles.length, setPageSize]);

  useEffect(() => {
    setDeletedFileNames((prev) => {
      if (prev.size === 0) return prev;
      const fileNames = new Set(files.map((file) => file.name));
      const next = new Set([...prev].filter((name) => fileNames.has(name)));
      return next.size === prev.size ? prev : next;
    });
  }, [files]);

  useEffect(() => {
    setSelectedFileNames((prev) => {
      if (prev.size === 0) return prev;
      const displayNames = new Set(displayFiles.map((file) => file.name));
      const next = new Set([...prev].filter((name) => displayNames.has(name)));
      return next.size === prev.size ? prev : next;
    });
  }, [displayFiles]);

  const pendingQuotaRefreshRef = useRef(false);
  const prevFilesLoadingRef = useRef(loading);

  const handleRefresh = useCallback(() => {
    pendingQuotaRefreshRef.current = true;
    void triggerHeaderRefresh();
  }, []);

  useEffect(() => {
    const wasLoading = prevFilesLoadingRef.current;
    prevFilesLoadingRef.current = loading;

    if (!pendingQuotaRefreshRef.current) return;
    if (loading) return;
    if (!wasLoading) return;

    pendingQuotaRefreshRef.current = false;
    const scope = effectiveViewMode === 'all' ? 'all' : 'page';
    const targets = effectiveViewMode === 'all' ? displayFiles : pageItems;
    if (targets.length === 0) return;
    loadQuota(targets, scope, setLoading);
  }, [loading, effectiveViewMode, displayFiles, pageItems, loadQuota, setLoading]);

  useEffect(() => {
    if (loading) return;
    if (filteredFiles.length === 0) {
      setQuota({});
      return;
    }
    setQuota((prev) => {
      const nextState: Record<string, TState> = {};
      filteredFiles.forEach((file) => {
        const storeKey = getQuotaStoreKey(config, file);
        const cached = prev[storeKey];
        const scoped = config.scopeState && cached ? config.scopeState(file, cached) : cached;
        if (scoped) {
          nextState[storeKey] = cached;
          return;
        }
        if (storeKey === file.name) {
          return;
        }
        const legacyCached = prev[file.name];
        const legacyScoped =
          config.scopeState && legacyCached ? config.scopeState(file, legacyCached) : legacyCached;
        if (legacyScoped) {
          nextState[file.name] = legacyCached;
        }
      });
      return nextState;
    });
  }, [config, filteredFiles, loading, setQuota]);

  const refreshQuotaForFile = useCallback(
    async (file: AuthFileItem) => {
      if (disabled || file.disabled) return;
      if (getScopedQuota(file)?.status === 'loading') return;
      const displayName = getAccountDisplayName(file);
      const storeKey = getQuotaStoreKey(config, file);

      setQuota((prev) => ({
        ...prev,
        [storeKey]: config.buildLoadingState(file),
      }));

      try {
        const data = await config.fetchQuota(file, t);
        setQuota((prev) => ({
          ...prev,
          [storeKey]: config.buildSuccessState(data, file),
        }));
        showNotification(t('auth_files.quota_refresh_success', { name: displayName }), 'success');
      } catch (err: unknown) {
        const message = err instanceof Error ? err.message : t('common.unknown_error');
        const status = getStatusFromError(err);
        setQuota((prev) => ({
          ...prev,
          [storeKey]: config.buildErrorState(message, status, file),
        }));
        showNotification(
          t('auth_files.quota_refresh_failed', { name: displayName, message }),
          'error'
        );
      }
    },
    [config, disabled, getAccountDisplayName, getScopedQuota, setQuota, showNotification, t]
  );

  const resetQuotaForFile = useCallback(
    (file: AuthFileItem) => {
      if (!config.resetQuota || disabled || file.disabled) return;
      const fileQuota = getScopedQuota(file);
      const canReset =
        config.canResetQuota?.(file, fileQuota) ??
        Boolean(fileQuota && fileQuota.status === 'success');
      if (!canReset) return;
      const resetCount =
        (fileQuota as { rateLimitResetCreditsAvailableCount?: number | null } | undefined)
          ?.rateLimitResetCreditsAvailableCount ?? 0;
      const displayName = getAccountDisplayName(file);
      const storeKey = getQuotaStoreKey(config, file);

      showConfirmation({
        title: t(`${config.i18nPrefix}.reset_confirm_title`),
        message: t(`${config.i18nPrefix}.reset_confirm_message`, {
          name: displayName,
          count: resetCount,
        }),
        confirmText: t(`${config.i18nPrefix}.reset_button`, { count: resetCount }),
        cancelText: t('common.cancel'),
        variant: 'primary',
        onConfirm: async () => {
          setQuota((prev) => ({
            ...prev,
            [storeKey]: config.buildLoadingState(file),
          }));

          try {
            const data = await config.resetQuota?.(file, t);
            if (data === undefined) {
              throw new Error(t('common.unknown_error'));
            }
            setQuota((prev) => ({
              ...prev,
              [storeKey]: config.buildSuccessState(data, file),
            }));
            showNotification(
              t(`${config.i18nPrefix}.reset_success`, { name: displayName }),
              'success'
            );
          } catch (err: unknown) {
            const message = err instanceof Error ? err.message : t('common.unknown_error');
            const status = getStatusFromError(err);
            setQuota((prev) => ({
              ...prev,
              [storeKey]: config.buildErrorState(message, status, file),
            }));
            showNotification(
              t(`${config.i18nPrefix}.reset_failed`, { name: displayName, message }),
              'error'
            );
          }
        },
      });
    },
    [
      config,
      disabled,
      getAccountDisplayName,
      getScopedQuota,
      setQuota,
      showConfirmation,
      showNotification,
      t,
    ]
  );

  const applyDeletedAuthFiles = useCallback(
    (names: string[]) => {
      if (names.length === 0) return;
      const deletedNames = new Set(names);
      const deletedStoreKeys = new Set(
        files
          .filter((file) => deletedNames.has(file.name))
          .map((file) => getQuotaStoreKey(config, file))
      );

      setDeletedFileNames((prev) => {
        const next = new Set(prev);
        names.forEach((name) => next.add(name));
        return next;
      });
      setSelectedFileNames((prev) => {
        if (prev.size === 0) return prev;
        const next = new Set(prev);
        names.forEach((name) => next.delete(name));
        return next;
      });
      setQuota((prev) => {
        const next = { ...prev };
        names.forEach((name) => {
          delete next[name];
        });
        deletedStoreKeys.forEach((key) => {
          delete next[key];
        });
        return next;
      });
    },
    [config, files, setQuota]
  );

  const deleteAuthFile = useCallback(
    (file: AuthFileItem) => {
      if (disabled || deletingFileNames.has(file.name)) return;
      const displayName = getAccountDisplayName(file);

      showConfirmation({
        title: t('auth_files.delete_title', { defaultValue: 'Delete File' }),
        message: `${t('auth_files.delete_confirm')} "${displayName}" ?`,
        confirmText: t('common.confirm'),
        cancelText: t('common.cancel'),
        variant: 'danger',
        onConfirm: async () => {
          setDeletingFileNames((prev) => new Set(prev).add(file.name));
          try {
            const result = await authFilesApi.deleteFile(file.name);
            applyDeletedAuthFiles(result.files.length > 0 ? result.files : [file.name]);
            showNotification(t('auth_files.delete_success'), 'success');
          } catch (err: unknown) {
            const message = err instanceof Error ? err.message : t('common.unknown_error');
            showNotification(`${t('notification.delete_failed')}: ${message}`, 'error');
          } finally {
            setDeletingFileNames((prev) => {
              const next = new Set(prev);
              next.delete(file.name);
              return next;
            });
          }
        },
      });
    },
    [
      applyDeletedAuthFiles,
      deletingFileNames,
      disabled,
      getAccountDisplayName,
      showConfirmation,
      showNotification,
      t,
    ]
  );

  const toggleSelectedFile = useCallback((name: string, selected: boolean) => {
    setSelectedFileNames((prev) => {
      const next = new Set(prev);
      if (selected) {
        next.add(name);
      } else {
        next.delete(name);
      }
      return next;
    });
  }, []);

  const deleteSelectedAuthFiles = useCallback(() => {
    if (disabled || selectedFileNames.size === 0) return;
    const names = [...selectedFileNames].filter((name) => !deletingFileNames.has(name));
    if (names.length === 0) return;

    showConfirmation({
      title: t('auth_files.batch_delete_title'),
      message: t('auth_files.batch_delete_confirm', { count: names.length }),
      confirmText: t('common.confirm'),
      cancelText: t('common.cancel'),
      variant: 'danger',
      onConfirm: async () => {
        setDeletingFileNames((prev) => {
          const next = new Set(prev);
          names.forEach((name) => next.add(name));
          return next;
        });
        try {
          const result = await authFilesApi.deleteFiles(names);
          const failedNames = new Set(result.failed.map((item) => item.name));
          const deletedNames =
            result.files.length > 0
              ? result.files
              : result.failed.length === 0
                ? names
                : names.filter((name) => !failedNames.has(name));
          applyDeletedAuthFiles(deletedNames);
          if (result.failed.length > 0) {
            showNotification(
              t('auth_files.delete_filtered_result_partial', {
                success: deletedNames.length,
                failed: result.failed.length,
              }),
              'warning'
            );
          } else {
            showNotification(
              t('auth_files.delete_filtered_result_success', { count: deletedNames.length }),
              'success'
            );
          }
        } catch (err: unknown) {
          const message = err instanceof Error ? err.message : t('common.unknown_error');
          showNotification(`${t('notification.delete_failed')}: ${message}`, 'error');
        } finally {
          setDeletingFileNames((prev) => {
            const next = new Set(prev);
            names.forEach((name) => next.delete(name));
            return next;
          });
        }
      },
    });
  }, [
    applyDeletedAuthFiles,
    deletingFileNames,
    disabled,
    selectedFileNames,
    showConfirmation,
    showNotification,
    t,
  ]);

  const clearSelectedFiles = useCallback(() => {
    setSelectedFileNames(new Set());
  }, []);

  const titleNode = (
    <div className={styles.titleWrapper}>
      <span>{t(`${config.i18nPrefix}.title`)}</span>
      {filteredFiles.length > 0 && (
        <span className={styles.countBadge}>
          {normalizedSearchQuery ? displayFiles.length : filteredFiles.length}
        </span>
      )}
    </div>
  );

  const isRefreshing = sectionLoading || loading;
  const nextAccountDisplayMode: QuotaAccountDisplayMode =
    resolvedAccountDisplayMode === 'masked' ? 'full' : 'masked';
  const AccountDisplayIcon = resolvedAccountDisplayMode === 'masked' ? IconEyeOff : IconEye;
  const accountDisplayHint = t(
    resolvedAccountDisplayMode === 'masked'
      ? 'quota_management.show_full_credentials_hint'
      : 'quota_management.show_masked_credentials_hint'
  );

  return (
    <Card
      title={titleNode}
      extra={
        <div className={styles.headerActions}>
          <Button
            type="button"
            variant="secondary"
            size="sm"
            className={[
              styles.accountDisplayModeButton,
              resolvedAccountDisplayMode === 'full' ? styles.accountDisplayModeButtonActive : '',
            ]
              .filter(Boolean)
              .join(' ')}
            onClick={() => setAccountDisplayMode(nextAccountDisplayMode)}
            title={accountDisplayHint}
            aria-label={accountDisplayHint}
          >
            <AccountDisplayIcon size={15} aria-hidden="true" />
            {t(
              resolvedAccountDisplayMode === 'masked'
                ? 'quota_management.account_display_masked'
                : 'quota_management.account_display_full'
            )}
          </Button>
          <div className={styles.viewModeToggle}>
            <Button
              variant="secondary"
              size="sm"
              className={`${styles.viewModeButton} ${
                effectiveViewMode === 'paged' ? styles.viewModeButtonActive : ''
              }`}
              onClick={() => setViewMode('paged')}
            >
              {t('auth_files.view_mode_paged')}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              className={`${styles.viewModeButton} ${
                effectiveViewMode === 'all' ? styles.viewModeButtonActive : ''
              }`}
              onClick={() => {
                if (displayFiles.length > MAX_SHOW_ALL_THRESHOLD) {
                  setShowTooManyWarning(true);
                } else {
                  setViewMode('all');
                }
              }}
            >
              {t('auth_files.view_mode_all')}
            </Button>
          </div>
          <Button
            variant="secondary"
            size="sm"
            className={styles.refreshAllButton}
            onClick={handleRefresh}
            disabled={disabled || isRefreshing}
            loading={isRefreshing}
            title={t('quota_management.refresh_all_credentials')}
            aria-label={t('quota_management.refresh_all_credentials')}
          >
            {!isRefreshing && <IconRefreshCw size={16} />}
            {t('quota_management.refresh_all_credentials')}
          </Button>
          {selectedCount > 0 ? (
            <div className={styles.quotaBatchActions}>
              <span className={styles.quotaBatchSelected}>
                {t('auth_files.batch_selected', { count: selectedCount })}
              </span>
              <Button variant="danger" size="sm" onClick={deleteSelectedAuthFiles} disabled={disabled}>
                <IconTrash2 size={16} />
                {t('auth_files.delete_button')}
              </Button>
              <Button variant="secondary" size="sm" onClick={clearSelectedFiles}>
                {t('auth_files.batch_deselect')}
              </Button>
            </div>
          ) : null}
        </div>
      }
    >
      {filteredFiles.length === 0 ? (
        <EmptyState
          title={t(`${config.i18nPrefix}.empty_title`)}
          description={t(`${config.i18nPrefix}.empty_desc`)}
        />
      ) : displayFiles.length === 0 ? (
        <EmptyState
          title={t('quota_management.search_empty_title')}
          description={t('quota_management.search_empty_desc')}
        />
      ) : (
        <>
          <div ref={gridRef} className={config.gridClassName}>
            {pageItems.map((item) => {
              const itemQuota = getScopedQuota(item);
              const displayQuota = getDisplayQuota(item);
              const isDeletingFile = deletingFileNames.has(item.name);
              const resetCount =
                (itemQuota as { rateLimitResetCreditsAvailableCount?: number | null } | undefined)
                  ?.rateLimitResetCreditsAvailableCount ?? 0;
              const canReset =
                Boolean(config.resetQuota) &&
                !disabled &&
                !item.disabled &&
                !isDeletingFile &&
                (config.canResetQuota?.(item, itemQuota) ??
                  Boolean(itemQuota && itemQuota.status === 'success'));
              const canReauth =
                config.type === 'codex' &&
                itemQuota?.status === 'error' &&
                itemQuota.errorStatus === 401 &&
                !disabled &&
                !item.disabled &&
                !isDeletingFile &&
                Boolean(onReauthAccount);
              const canDeleteAuthFile = !disabled && !isDeletingFile;
              const deleteAuthFileAction = (
                <Button
                  type="button"
                  variant="danger"
                  size="sm"
                  className={styles.quotaDeleteAuthFileButton}
                  onClick={() => deleteAuthFile(item)}
                  disabled={!canDeleteAuthFile}
                  title={t('auth_files.delete_button')}
                  aria-label={t('auth_files.delete_button')}
                >
                  {isDeletingFile ? <LoadingSpinner size={14} /> : <IconTrash2 size={16} />}
                </Button>
              );

              return (
                <QuotaCard
                  key={getQuotaStoreKey(config, item)}
                  item={item}
                  quota={displayQuota}
                  resolvedTheme={resolvedTheme}
                  i18nPrefix={config.i18nPrefix}
                  cardIdleMessageKey={config.cardIdleMessageKey}
                  cardClassName={config.cardClassName}
                  defaultType={config.type}
                  accountDisplayMode={resolvedAccountDisplayMode}
                  canRefresh={!disabled && !item.disabled && !isDeletingFile}
                  onRefresh={() => void refreshQuotaForFile(item)}
                  selected={selectedFileNames.has(item.name)}
                  onToggleSelect={toggleSelectedFile}
                  canReset={canReset}
                  resetLabel={
                    canReset
                      ? t(`${config.i18nPrefix}.reset_action_button`, { count: resetCount })
                      : undefined
                  }
                  onReset={canReset ? () => resetQuotaForFile(item) : undefined}
                  canReauth={canReauth}
                  onReauth={canReauth ? () => onReauthAccount?.(item) : undefined}
                  deleteAuthFileAction={deleteAuthFileAction}
                  renderQuotaItems={config.renderQuotaItems}
                />
              );
            })}
          </div>
          {displayFiles.length > pageSize && effectiveViewMode === 'paged' && (
            <div className={styles.pagination}>
              <Button variant="secondary" size="sm" onClick={goToPrev} disabled={currentPage <= 1}>
                {t('auth_files.pagination_prev')}
              </Button>
              <div className={styles.pageInfo}>
                {t('auth_files.pagination_info', {
                  current: currentPage,
                  total: totalPages,
                  count: displayFiles.length,
                })}
              </div>
              <Button
                variant="secondary"
                size="sm"
                onClick={goToNext}
                disabled={currentPage >= totalPages}
              >
                {t('auth_files.pagination_next')}
              </Button>
            </div>
          )}
        </>
      )}
      {showTooManyWarning && (
        <div className={styles.warningOverlay} onClick={() => setShowTooManyWarning(false)}>
          <div className={styles.warningModal} onClick={(e) => e.stopPropagation()}>
            <p>{t('auth_files.too_many_files_warning')}</p>
            <Button variant="primary" size="sm" onClick={() => setShowTooManyWarning(false)}>
              {t('common.confirm')}
            </Button>
          </div>
        </div>
      )}
    </Card>
  );
}
