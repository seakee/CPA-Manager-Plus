import { useDeferredValue, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import type { PointerEvent as ReactPointerEvent } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { PageControlBar } from '@/components/common/PageControlBar';
import { PaginationFooter } from '@/components/common/PaginationFooter';
import { SegmentedTabs, type SegmentedTabItem } from '@/components/ui/SegmentedTabs';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import {
  IconChevronDown,
  IconChevronUp,
  IconCode,
  IconCopy,
  IconDownload,
  IconEye,
  IconEyeOff,
  IconRefreshCw,
  IconSearch,
  IconSlidersHorizontal,
  IconTimer,
  IconTrash2,
  IconX,
} from '@/components/ui/icons';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useLocalStorage } from '@/hooks/useLocalStorage';
import { useAuthStore, useConfigStore, useNotificationStore } from '@/stores';
import { configApi } from '@/services/api/config';
import { logsApi } from '@/services/api/logs';
import type { LogsQuery, LogsResponse } from '@/services/api/logs';
import type { PublicApiClient } from '@/fullweb/publicApi';
import { configurationText } from '@/i18n/configurationWorkspace';
import { copyToClipboard } from '@/utils/clipboard';
import { downloadBlob } from '@/utils/download';
import { MANAGEMENT_API_PREFIX } from '@/utils/constants';
import { formatUnixTimestamp } from '@/utils/format';
import {
  HTTP_METHODS,
  STATUS_GROUPS,
  resolveStatusGroup,
  type LogState,
} from './hooks/logTypes';
import { parseLogLine } from './hooks/logParsing';
import { useLogFilters } from './hooks/useLogFilters';
import { isNearBottom, useLogScroller } from './hooks/useLogScroller';
import { isFileLogsAvailable } from './logFeatureAvailability';
import { LogsSettingsPanel, type LogsSettingField } from './LogsSettingsPanel';
import {
  extractWriteRefusalCode,
  mapAuthorityReasonKey,
  useLogsConfigAuthority,
} from './useLogsConfigAuthority';
import styles from './LogsPage.module.scss';

interface ErrorLogItem {
  name: string;
  size?: number;
  modified?: number;
}

interface ErrorLogPreviewState {
  item: ErrorLogItem;
  loading: boolean;
  error: string;
  content: string;
  blob: Blob | null;
  truncated: boolean;
  totalBytes: number;
}

// 初始只渲染最近 100 行，滚动到顶部再逐步加载更多（避免一次性渲染过多导致卡顿）
const INITIAL_DISPLAY_LINES = 100;
const ERROR_LOG_PAGE_SIZE_OPTIONS = [20, 50, 100] as const;
const ERROR_LOG_DEFAULT_PAGE_SIZE = 20;
const ERROR_LOG_PREVIEW_MAX_BYTES = 512 * 1024;
const MAX_BUFFER_LINES = 10000;
const LONG_PRESS_MS = 650;
const LONG_PRESS_MOVE_THRESHOLD = 10;

const formatByteSize = (bytes: number): string => {
  if (bytes >= 1024 * 1024) {
    return `${(bytes / (1024 * 1024)).toFixed(2)} MB`;
  }
  return `${(bytes / 1024).toFixed(1)} KB`;
};

const getErrorMessage = (err: unknown): string => {
  if (err instanceof Error) return err.message;
  if (typeof err === 'string') return err;
  if (typeof err !== 'object' || err === null) return '';
  if (!('message' in err)) return '';

  const message = (err as { message?: unknown }).message;
  return typeof message === 'string' ? message : '';
};

type TabType = 'logs' | 'errors' | 'settings';
type LogPosition = Pick<LogsQuery, 'after' | 'cursor'>;

const resolveTab = (raw: string | null): TabType => {
  if (raw === 'errors' || raw === 'settings') return raw;
  return 'logs';
};

export interface LogsPageProps {
  authorityClient?: PublicApiClient;
}
const buildLogsQuery = (incremental: boolean, position: LogPosition): LogsQuery => {
  if (!incremental) return { limit: MAX_BUFFER_LINES };

  const params: LogsQuery = { limit: MAX_BUFFER_LINES };
  if (position.cursor) {
    params.cursor = position.cursor;
  }
  if (position.after !== undefined && position.after > 0) {
    params.after = position.after;
  }
  return params;
};

export function LogsPage({ authorityClient }: LogsPageProps = {}) {
  const connectionScope = useAuthStore((state) =>
    JSON.stringify([state.apiBase, state.managementKey])
  );
  return <LogsPageContent key={connectionScope} authorityClient={authorityClient} />;
}

function LogsPageContent({ authorityClient }: LogsPageProps) {
  const { t } = useTranslation();
  const [searchParams, setSearchParams] = useSearchParams();
  const { showNotification, showConfirmation } = useNotificationStore();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey);
  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const updateConfigValue = useConfigStore((state) => state.updateConfigValue);
  const clearCache = useConfigStore((state) => state.clearCache);
  const fileLogsAvailable = isFileLogsAvailable(config);
  const authority = useLogsConfigAuthority({
    connectionStatus,
    apiBase,
    managementKey,
    authorityClient,
  });

  const [activeTab, setActiveTab] = useState<TabType>(() => resolveTab(searchParams.get('tab')));
  const requestLogEnabled = config?.requestLog ?? false;
  const [logState, setLogState] = useState<LogState>({ buffer: [], visibleFrom: 0 });
  const [loading, setLoading] = useState(true);
  const [logsLoaded, setLogsLoaded] = useState(false);
  const [error, setError] = useState('');
  const [autoRefresh, setAutoRefresh] = useLocalStorage('logsPage.autoRefresh', false);
  const [searchQuery, setSearchQuery] = useState('');
  const deferredSearchQuery = useDeferredValue(searchQuery);
  const [hideManagementLogs, setHideManagementLogs] = useLocalStorage(
    'logsPage.hideManagementLogs',
    true
  );
  const [showRawLogs, setShowRawLogs] = useLocalStorage('logsPage.showRawLogs', false);
  const [structuredFiltersExpanded, setStructuredFiltersExpanded] = useLocalStorage(
    'logsPage.structuredFiltersExpanded',
    false
  );
  const [errorLogs, setErrorLogs] = useState<ErrorLogItem[]>([]);
  const [errorSearchQuery, setErrorSearchQuery] = useState('');
  const [errorPage, setErrorPage] = useState(1);
  const [errorPageSize, setErrorPageSize] = useState<number>(ERROR_LOG_DEFAULT_PAGE_SIZE);
  const [loadingErrors, setLoadingErrors] = useState(false);
  const [errorLogsLoaded, setErrorLogsLoaded] = useState(false);
  const [requestLogId, setRequestLogId] = useState<string | null>(null);
  const [errorLogPreview, setErrorLogPreview] = useState<ErrorLogPreviewState | null>(null);
  const [errorLogPreviewWrap, setErrorLogPreviewWrap] = useState(true);
  const [errorLogsError, setErrorLogsError] = useState('');
  const [requestLogDownloading, setRequestLogDownloading] = useState(false);
  const [savingField, setSavingField] = useState<LogsSettingField | null>(null);
  const [settingsSaveError, setSettingsSaveError] = useState('');
  const [refreshingSettings, setRefreshingSettings] = useState(false);
  const settingWriteInFlightRef = useRef(false);

  const logScrollerRef = useRef<ReturnType<typeof useLogScroller> | null>(null);
  const longPressRef = useRef<{
    timer: number | null;
    startX: number;
    startY: number;
    fired: boolean;
  } | null>(null);
  const logRequestInFlightRef = useRef(false);
  const pendingFullReloadRef = useRef(false);
  const logGenerationRef = useRef(0);
  const errorLogRequestRef = useRef(0);
  const errorLogPreviewRequestRef = useRef(0);
  const scopeActiveRef = useRef(false);
  useLayoutEffect(() => {
    scopeActiveRef.current = true;
    return () => {
      scopeActiveRef.current = false;
    };
  }, []);
  useEffect(
    () => () => {
      logGenerationRef.current += 1;
      logRequestInFlightRef.current = false;
      errorLogRequestRef.current += 1;
      errorLogPreviewRequestRef.current += 1;
      pendingFullReloadRef.current = false;
    },
    []
  );

  // 新版 CPA 优先使用 cursor，旧版接口继续使用 after 时间戳。
  const logPositionRef = useRef<LogPosition>({});

  const resetLogPosition = () => {
    logPositionRef.current = {};
  };

  const updateLogPosition = (data: LogsResponse, incremental: boolean) => {
    const currentPosition = logPositionRef.current;
    const nextPosition: LogPosition = {};
    if (data.nextCursor) {
      nextPosition.cursor = data.nextCursor;
    }
    if (data.latestAfter !== undefined) {
      nextPosition.after = data.latestAfter;
    } else if (incremental && currentPosition.after !== undefined) {
      nextPosition.after = currentPosition.after;
    }
    logPositionRef.current = nextPosition;
  };

  const disableControls = connectionStatus !== 'connected';
  const disableLogFileControls = disableControls || !fileLogsAvailable;
  const canLoadFileLogs = activeTab === 'logs' && fileLogsAvailable;

  const handleTabChange = (tab: TabType) => {
    setSettingsSaveError('');
    setActiveTab(tab);
    const nextParams = new URLSearchParams(searchParams);
    if (tab === 'errors' || tab === 'settings') {
      nextParams.set('tab', tab);
    } else {
      nextParams.delete('tab');
    }
    setSearchParams(nextParams, { replace: true });
  };

  const formatSettingsWriteError = (err: unknown): string => {
    const refusalCode = extractWriteRefusalCode(err);
    if (refusalCode) {
      return configurationText(mapAuthorityReasonKey(refusalCode));
    }
    const detail = getErrorMessage(err);
    return detail
      ? `${t('logs.settings_save_failed')}: ${detail}`
      : t('logs.settings_save_failed');
  };

  const handleRefreshSettings = async () => {
    if (settingWriteInFlightRef.current) return;
    setRefreshingSettings(true);
    setSettingsSaveError('');
    try {
      await Promise.all([
        fetchConfig ? fetchConfig(undefined, true) : Promise.resolve(null),
        authority.refresh(),
      ]);
    } catch (err: unknown) {
      if (!scopeActiveRef.current) return;
      setSettingsSaveError(formatSettingsWriteError(err));
    } finally {
      if (scopeActiveRef.current) setRefreshingSettings(false);
    }
  };

  const handleToggleSettingField = async (
    field: 'logging-to-file' | 'debug' | 'request-log',
    next: boolean
  ) => {
    if (!authority.canEditLogs || settingWriteInFlightRef.current) return;
    settingWriteInFlightRef.current = true;
    setSavingField(field);
    setSettingsSaveError('');
    try {
      if (field === 'logging-to-file') {
        await configApi.updateLoggingToFile(next);
      } else if (field === 'debug') {
        await configApi.updateDebug(next);
      } else {
        await configApi.updateRequestLog(next);
      }
      if (!scopeActiveRef.current) return;
      updateConfigValue?.(field, next);
      clearCache?.(field);
      showNotification(t('logs.settings_saved'), 'success');
    } catch (err: unknown) {
      if (!scopeActiveRef.current) return;
      const message = formatSettingsWriteError(err);
      setSettingsSaveError(message);
      showNotification(message, 'error');
    } finally {
      settingWriteInFlightRef.current = false;
      if (scopeActiveRef.current) setSavingField(null);
    }
  };

  const handleSaveNumberSettingField = async (
    field: 'logs-max-total-size-mb' | 'error-logs-max-files',
    next: number
  ): Promise<boolean> => {
    if (!authority.canEditLogs || settingWriteInFlightRef.current) return false;
    settingWriteInFlightRef.current = true;
    setSavingField(field);
    setSettingsSaveError('');
    try {
      if (field === 'logs-max-total-size-mb') {
        await configApi.updateLogsMaxTotalSizeMb(next);
      } else {
        await configApi.updateErrorLogsMaxFiles(next);
      }
      if (!scopeActiveRef.current) return false;
      updateConfigValue?.(field, next);
      clearCache?.(field);
      showNotification(t('logs.settings_saved'), 'success');
      return true;
    } catch (err: unknown) {
      if (!scopeActiveRef.current) return false;
      const message = formatSettingsWriteError(err);
      setSettingsSaveError(message);
      showNotification(message, 'error');
      return false;
    } finally {
      settingWriteInFlightRef.current = false;
      if (scopeActiveRef.current) setSavingField(null);
    }
  };

  const loadLogs = async (incremental = false) => {
    if (!canLoadFileLogs) {
      setLoading(false);
      return;
    }

    if (connectionStatus !== 'connected') {
      setLoading(false);
      return;
    }

    if (logRequestInFlightRef.current) {
      if (!incremental) {
        pendingFullReloadRef.current = true;
      }
      return;
    }

    logRequestInFlightRef.current = true;
    const generation = logGenerationRef.current;

    if (!incremental) {
      setLoading(true);
    }
    setError('');

    try {
      const scrollerInstance = logScrollerRef.current;
      const stickToBottom =
        !logsLoaded || isNearBottom(scrollerInstance?.logViewerRef.current ?? null);
      if (stickToBottom) {
        scrollerInstance?.requestScrollToBottom();
      }

      const params = buildLogsQuery(incremental, logPositionRef.current);
      const data = await logsApi.fetchLogs(params);
      if (generation !== logGenerationRef.current) return;
      updateLogPosition(data, incremental);
      setLogsLoaded(true);

      const newLines = Array.isArray(data.lines) ? data.lines : [];

      if (incremental && data.cursorReset) {
        const buffer = newLines.slice(-MAX_BUFFER_LINES);
        const visibleFrom = Math.max(buffer.length - INITIAL_DISPLAY_LINES, 0);
        setLogState((previous) => ({
          buffer,
          visibleFrom: stickToBottom
            ? visibleFrom
            : Math.max(buffer.length - (previous.buffer.length - previous.visibleFrom), 0),
        }));
      } else if (incremental && newLines.length > 0) {
        // 增量更新：追加新日志并限制缓冲区大小（避免内存与渲染膨胀）
        setLogState((prev) => {
          const prevRenderedCount = prev.buffer.length - prev.visibleFrom;
          const combined = [...prev.buffer, ...newLines];
          const dropCount = Math.max(combined.length - MAX_BUFFER_LINES, 0);
          const buffer = dropCount > 0 ? combined.slice(dropCount) : combined;
          let visibleFrom = Math.max(prev.visibleFrom - dropCount, 0);

          // 若用户停留在底部（跟随最新日志），则保持“渲染窗口”大小不变，避免无限增长
          if (stickToBottom) {
            visibleFrom = Math.max(buffer.length - prevRenderedCount, 0);
          }

          return { buffer, visibleFrom };
        });
      } else if (!incremental) {
        // 全量加载：默认只渲染最后 100 行，向上滚动再展开更多
        const buffer = newLines.slice(-MAX_BUFFER_LINES);
        const visibleFrom = Math.max(buffer.length - INITIAL_DISPLAY_LINES, 0);
        setLogState((previous) => ({
          buffer,
          visibleFrom: stickToBottom ? visibleFrom : Math.min(previous.visibleFrom, buffer.length),
        }));
      }
    } catch (err: unknown) {
      if (generation !== logGenerationRef.current) return;
      console.error('Failed to load logs:', err);
      setError(getErrorMessage(err) || t('logs.load_error'));
    } finally {
      if (generation === logGenerationRef.current) {
        if (!incremental) setLoading(false);
        logRequestInFlightRef.current = false;
        if (pendingFullReloadRef.current) {
          pendingFullReloadRef.current = false;
          void loadLogs(false);
        }
      }
    }
  };

  const clearLogs = async () => {
    showConfirmation({
      title: t('logs.clear_confirm_title', { defaultValue: 'Clear Logs' }),
      message: t('logs.clear_confirm'),
      variant: 'danger',
      confirmText: t('common.confirm'),
      onConfirm: async () => {
        if (!scopeActiveRef.current) return;
        try {
          await logsApi.clearLogs();
          if (!scopeActiveRef.current) return;
          logGenerationRef.current += 1;
          logRequestInFlightRef.current = false;
          pendingFullReloadRef.current = false;
          setLoading(false);
          setLogsLoaded(true);
          setLogState({ buffer: [], visibleFrom: 0 });
          resetLogPosition();
          showNotification(t('logs.clear_success'), 'success');
        } catch (err: unknown) {
          if (!scopeActiveRef.current) return;
          const message = getErrorMessage(err);
          showNotification(
            `${t('notification.delete_failed')}${message ? `: ${message}` : ''}`,
            'error'
          );
        }
      },
    });
  };

  const downloadLogs = () => {
    if (!fileLogsAvailable || logState.buffer.length === 0) return;
    const text = logState.buffer.join('\n');
    downloadBlob({ filename: 'logs.txt', blob: new Blob([text], { type: 'text/plain' }) });
    showNotification(t('logs.download_success'), 'success');
  };

  const loadErrorLogs = async () => {
    const requestId = ++errorLogRequestRef.current;
    if (connectionStatus !== 'connected') {
      setLoadingErrors(false);
      return;
    }

    setLoadingErrors(true);
    setErrorLogsError('');
    try {
      const res = await logsApi.fetchErrorLogs();
      if (requestId !== errorLogRequestRef.current) return;
      // API 返回 { files: [...] }
      setErrorLogs(Array.isArray(res.files) ? res.files : []);
      setErrorLogsLoaded(true);
    } catch (err: unknown) {
      console.error('Failed to load error logs:', err);
      if (requestId !== errorLogRequestRef.current) return;
      const message = getErrorMessage(err);
      setErrorLogsError(
        message ? `${t('logs.error_logs_load_error')}: ${message}` : t('logs.error_logs_load_error')
      );
    } finally {
      if (requestId === errorLogRequestRef.current) setLoadingErrors(false);
    }
  };

  useHeaderRefresh(() => {
    if (activeTab === 'settings') {
      return handleRefreshSettings();
    }
    if (activeTab === 'errors') {
      return loadErrorLogs();
    }
    return loadLogs(false);
  }, connectionStatus === 'connected');

  const downloadErrorLog = async (name: string) => {
    try {
      const response = await logsApi.downloadErrorLog(name);
      downloadBlob({ filename: name, blob: new Blob([response.data], { type: 'text/plain' }) });
      showNotification(t('logs.error_log_download_success'), 'success');
    } catch (err: unknown) {
      const message = getErrorMessage(err);
      showNotification(
        `${t('notification.download_failed')}${message ? `: ${message}` : ''}`,
        'error'
      );
    }
  };

  const openErrorLogPreview = async (item: ErrorLogItem) => {
    const requestId = ++errorLogPreviewRequestRef.current;
    setErrorLogPreview({
      item,
      loading: true,
      error: '',
      content: '',
      blob: null,
      truncated: false,
      totalBytes: item.size ?? 0,
    });
    try {
      const response = await logsApi.downloadErrorLog(item.name);
      if (requestId !== errorLogPreviewRequestRef.current) return;
      const rawData = response.data;
      const blob =
        rawData instanceof Blob
          ? rawData
          : new Blob([typeof rawData === 'string' ? rawData : String(rawData ?? '')], {
              type: 'text/plain',
            });
      const totalBytes = blob.size;
      const truncated = totalBytes > ERROR_LOG_PREVIEW_MAX_BYTES;
      const previewSlice = truncated ? blob.slice(0, ERROR_LOG_PREVIEW_MAX_BYTES) : blob;
      const content =
        typeof previewSlice.text === 'function'
          ? await previewSlice.text()
          : typeof rawData === 'string'
            ? rawData.slice(0, ERROR_LOG_PREVIEW_MAX_BYTES)
            : '';
      if (requestId !== errorLogPreviewRequestRef.current) return;
      setErrorLogPreview({
        item,
        loading: false,
        error: '',
        content,
        blob,
        truncated,
        totalBytes,
      });
    } catch (err: unknown) {
      if (requestId !== errorLogPreviewRequestRef.current) return;
      const message = getErrorMessage(err);
      setErrorLogPreview({
        item,
        loading: false,
        error: message
          ? `${t('logs.error_logs_preview_error')}: ${message}`
          : t('logs.error_logs_preview_error'),
        content: '',
        blob: null,
        truncated: false,
        totalBytes: item.size ?? 0,
      });
    }
  };

  const closeErrorLogPreview = () => {
    errorLogPreviewRequestRef.current += 1;
    setErrorLogPreview(null);
  };

  const downloadPreviewErrorLog = () => {
    if (!errorLogPreview) return;
    if (errorLogPreview.blob) {
      downloadBlob({
        filename: errorLogPreview.item.name,
        blob: errorLogPreview.blob,
      });
      showNotification(t('logs.error_log_download_success'), 'success');
      return;
    }
    void downloadErrorLog(errorLogPreview.item.name);
  };

  const copyPreviewErrorLog = async () => {
    if (!errorLogPreview?.content) return;
    const ok = await copyToClipboard(errorLogPreview.content);
    if (ok) {
      showNotification(t('logs.copy_success', { defaultValue: 'Copied to clipboard' }), 'success');
    } else {
      showNotification(t('logs.copy_failed', { defaultValue: 'Copy failed' }), 'error');
    }
  };

  useEffect(() => {
    const tab = resolveTab(searchParams.get('tab'));
    setActiveTab((current) => (current === tab ? current : tab));
  }, [searchParams]);

  const prevFileLogsAvailableRef = useRef(fileLogsAvailable);

  useEffect(() => {
    if (config === null) return;
    const wasAvailable = prevFileLogsAvailableRef.current;
    prevFileLogsAvailableRef.current = fileLogsAvailable;
    if (wasAvailable && !fileLogsAvailable) {
      logGenerationRef.current += 1;
      logRequestInFlightRef.current = false;
      pendingFullReloadRef.current = false;
      resetLogPosition();
      setLogState({ buffer: [], visibleFrom: 0 });
      setLogsLoaded(false);
      setLoading(false);
      setError('');
      setAutoRefresh(false);
    } else if (!fileLogsAvailable && autoRefresh) {
      setAutoRefresh(false);
    }
  }, [autoRefresh, config, fileLogsAvailable, setAutoRefresh]);

  useEffect(() => {
    if (connectionStatus !== 'connected') {
      setLoading(false);
      return;
    }

    if (!canLoadFileLogs) {
      setLoading(false);
      return;
    }

    resetLogPosition();
    loadLogs(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connectionStatus, canLoadFileLogs]);

  useEffect(() => {
    if (activeTab !== 'errors') return;
    if (connectionStatus !== 'connected') return;
    void loadErrorLogs();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeTab, connectionStatus, requestLogEnabled]);

  useEffect(() => {
    if (!autoRefresh || connectionStatus !== 'connected' || !canLoadFileLogs) {
      return;
    }
    const id = window.setInterval(() => {
      loadLogs(true);
    }, 8000);
    return () => window.clearInterval(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [autoRefresh, connectionStatus, canLoadFileLogs]);

  const visibleLines = useMemo(
    () => logState.buffer.slice(logState.visibleFrom),
    [logState.buffer, logState.visibleFrom]
  );

  const trimmedSearchQuery = deferredSearchQuery.trim();
  const isSearching = trimmedSearchQuery.length > 0;
  const baseLines = isSearching ? logState.buffer : visibleLines;

  const parsedSearchLines = useMemo(() => {
    let working = baseLines;

    if (hideManagementLogs) {
      working = working.filter((line) => !line.includes(MANAGEMENT_API_PREFIX));
    }

    if (trimmedSearchQuery) {
      const queryLowered = trimmedSearchQuery.toLowerCase();
      working = working.filter((line) => line.toLowerCase().includes(queryLowered));
    }

    return working.map((line) => parseLogLine(line));
  }, [baseLines, hideManagementLogs, trimmedSearchQuery]);

  const filters = useLogFilters({ parsedLines: parsedSearchLines });
  const structuredFiltersPanelId = 'logs-structured-filters';
  const structuredFilterCount =
    filters.methodFilters.length + filters.statusFilters.length + filters.pathFilters.length;

  const { filteredParsedLines, filteredLines, removedCount } = useMemo(() => {
    const filteredParsed = parsedSearchLines.filter((line) => {
      if (
        filters.methodFilterSet.size > 0 &&
        (!line.method || !filters.methodFilterSet.has(line.method))
      ) {
        return false;
      }

      const statusGroup = resolveStatusGroup(line.statusCode);
      if (
        filters.statusFilterSet.size > 0 &&
        (!statusGroup || !filters.statusFilterSet.has(statusGroup))
      ) {
        return false;
      }

      if (filters.pathFilterSet.size > 0 && (!line.path || !filters.pathFilterSet.has(line.path))) {
        return false;
      }

      return true;
    });

    return {
      filteredParsedLines: filteredParsed,
      filteredLines: filteredParsed.map((line) => line.raw),
      removedCount: Math.max(baseLines.length - filteredParsed.length, 0),
    };
  }, [
    baseLines,
    filters.methodFilterSet,
    filters.pathFilterSet,
    filters.statusFilterSet,
    parsedSearchLines,
  ]);

  const parsedVisibleLines = useMemo(
    () => (showRawLogs ? [] : filteredParsedLines),
    [filteredParsedLines, showRawLogs]
  );

  const rawVisibleText = useMemo(() => filteredLines.join('\n'), [filteredLines]);

  const scroller = useLogScroller({
    logState,
    setLogState,
    loading,
    isSearching,
    filteredLineCount: filteredLines.length,
    hasStructuredFilters: filters.hasStructuredFilters,
    showRawLogs,
  });

  logScrollerRef.current = scroller;

  const copyLogLine = async (raw: string) => {
    const ok = await copyToClipboard(raw);
    if (ok) {
      showNotification(t('logs.copy_success', { defaultValue: 'Copied to clipboard' }), 'success');
    } else {
      showNotification(t('logs.copy_failed', { defaultValue: 'Copy failed' }), 'error');
    }
  };

  const clearLongPressTimer = () => {
    if (longPressRef.current?.timer) {
      window.clearTimeout(longPressRef.current.timer);
      longPressRef.current.timer = null;
    }
  };

  const startLongPress = (event: ReactPointerEvent<HTMLDivElement>, id?: string) => {
    if (!requestLogEnabled) return;
    if (!id) return;
    if (requestLogId) return;
    clearLongPressTimer();
    longPressRef.current = {
      timer: window.setTimeout(() => {
        setRequestLogId(id);
        if (longPressRef.current) {
          longPressRef.current.fired = true;
          longPressRef.current.timer = null;
        }
      }, LONG_PRESS_MS),
      startX: event.clientX,
      startY: event.clientY,
      fired: false,
    };
  };

  const cancelLongPress = () => {
    clearLongPressTimer();
    longPressRef.current = null;
  };

  const handleLongPressMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    const current = longPressRef.current;
    if (!current || current.timer === null || current.fired) return;
    const deltaX = Math.abs(event.clientX - current.startX);
    const deltaY = Math.abs(event.clientY - current.startY);
    if (deltaX > LONG_PRESS_MOVE_THRESHOLD || deltaY > LONG_PRESS_MOVE_THRESHOLD) {
      cancelLongPress();
    }
  };

  const closeRequestLogModal = () => {
    if (requestLogDownloading) return;
    setRequestLogId(null);
  };

  const downloadRequestLog = async (id: string) => {
    setRequestLogDownloading(true);
    try {
      const response = await logsApi.downloadRequestLogById(id);
      downloadBlob({
        filename: `request-${id}.log`,
        blob: new Blob([response.data], { type: 'text/plain' }),
      });
      showNotification(t('logs.request_log_download_success'), 'success');
      setRequestLogId(null);
    } catch (err: unknown) {
      const message = getErrorMessage(err);
      showNotification(
        `${t('notification.download_failed')}${message ? `: ${message}` : ''}`,
        'error'
      );
    } finally {
      setRequestLogDownloading(false);
    }
  };

  useEffect(() => {
    return () => {
      if (longPressRef.current?.timer) {
        window.clearTimeout(longPressRef.current.timer);
        longPressRef.current.timer = null;
      }
    };
  }, []);

  const logTabItems = useMemo<SegmentedTabItem<TabType>[]>(
    () => [
      { id: 'logs', label: t('logs.log_content'), testId: 'logs-tab-logs' },
      { id: 'errors', label: t('logs.error_logs_modal_title'), testId: 'logs-tab-errors' },
      { id: 'settings', label: t('logs.settings_tab'), testId: 'logs-tab-settings' },
    ],
    [t]
  );

  const headerActions =
    activeTab === 'logs' ? (
      <>
        <div className={styles.toggleFrame}>
          <ToggleSwitch
            checked={autoRefresh}
            onChange={(value) => setAutoRefresh(value)}
            disabled={disableLogFileControls}
            label={
              <span className={styles.switchLabel}>
                <IconTimer size={15} />
                {t('logs.auto_refresh')}
              </span>
            }
          />
        </div>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => loadLogs(false)}
          disabled={disableLogFileControls || loading}
          className={styles.actionButton}
        >
          <span className={styles.buttonContent}>
            <IconRefreshCw size={15} />
            {t('logs.refresh_button')}
          </span>
        </Button>
        <Button
          variant="secondary"
          size="sm"
          onClick={downloadLogs}
          disabled={!fileLogsAvailable || logState.buffer.length === 0}
          data-testid="logs-download-button"
          className={styles.actionButton}
        >
          <span className={styles.buttonContent}>
            <IconDownload size={15} />
            {t('logs.download_button')}
          </span>
        </Button>
        <Button
          variant="danger"
          size="sm"
          onClick={clearLogs}
          disabled={disableLogFileControls}
          className={styles.actionButton}
        >
          <span className={styles.buttonContent}>
            <IconTrash2 size={15} />
            {t('logs.clear_button')}
          </span>
        </Button>
      </>
    ) : activeTab === 'errors' ? (
      <Button
        variant="secondary"
        size="sm"
        onClick={loadErrorLogs}
        loading={loadingErrors}
        disabled={disableControls}
        className={styles.actionButton}
      >
        <span className={styles.buttonContent}>
          <IconRefreshCw size={15} />
          {t('common.refresh')}
        </span>
      </Button>
    ) : (
      <Button
        variant="secondary"
        size="sm"
        onClick={() => {
          void handleRefreshSettings();
        }}
        loading={refreshingSettings || authority.loading}
        disabled={savingField !== null}
        className={styles.actionButton}
      >
        <span className={styles.buttonContent}>
          <IconRefreshCw size={15} />
          {t('common.refresh')}
        </span>
      </Button>
    );

  const filteredErrorLogs = useMemo(() => {
    const query = errorSearchQuery.trim().toLowerCase();
    if (!query) return errorLogs;
    return errorLogs.filter((item) => item.name.toLowerCase().includes(query));
  }, [errorLogs, errorSearchQuery]);

  const totalErrorPages = Math.max(1, Math.ceil(filteredErrorLogs.length / errorPageSize));
  const currentErrorPage = Math.min(errorPage, totalErrorPages);
  const paginatedErrorLogs = useMemo(() => {
    const start = (currentErrorPage - 1) * errorPageSize;
    return filteredErrorLogs.slice(start, start + errorPageSize);
  }, [currentErrorPage, errorPageSize, filteredErrorLogs]);
  const errorPaginationStart =
    filteredErrorLogs.length === 0 ? 0 : (currentErrorPage - 1) * errorPageSize + 1;
  const errorPaginationEnd = Math.min(
    filteredErrorLogs.length,
    currentErrorPage * errorPageSize
  );

  const clearAllLogFilters = () => {
    setSearchQuery('');
    filters.clearStructuredFilters();
  };

  const headerToolbar =
    activeTab === 'logs' ? (
      <div className={styles.logsToolbarStack}>
        <div className={styles.logsToolbarRow}>
          <div className={styles.searchWrapper}>
            <Input
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              placeholder={t('logs.search_placeholder')}
              aria-label={t('logs.search_placeholder')}
              className={styles.searchInput}
              rightElement={
                searchQuery ? (
                  <button
                    type="button"
                    className={styles.searchClear}
                    onClick={() => setSearchQuery('')}
                    title="Clear"
                    aria-label="Clear"
                  >
                    <IconX size={15} />
                  </button>
                ) : (
                  <IconSearch size={15} className={styles.searchIcon} />
                )
              }
            />
          </div>

          <div className={styles.filterPanelHeader}>
            <Button
              type="button"
              variant="secondary"
              size="sm"
              className={styles.filterPanelToggle}
              onClick={() => setStructuredFiltersExpanded((prev) => !prev)}
              aria-expanded={structuredFiltersExpanded}
              aria-controls={structuredFiltersPanelId}
              title={
                structuredFiltersExpanded
                  ? t('logs.filter_panel_collapse')
                  : t('logs.filter_panel_expand')
              }
            >
              <span className={styles.filterPanelButtonContent}>
                <IconSlidersHorizontal size={15} />
                <span>{t('logs.filter_panel_title')}</span>
                {structuredFilterCount > 0 && (
                  <span className={styles.filterPanelCount}>
                    {t('logs.filter_panel_active_count', { count: structuredFilterCount })}
                  </span>
                )}
                {structuredFiltersExpanded ? (
                  <IconChevronUp size={15} />
                ) : (
                  <IconChevronDown size={15} />
                )}
              </span>
            </Button>
          </div>

          <div className={styles.inlineToggleGroup}>
            <div className={styles.toggleFrame}>
              <ToggleSwitch
                checked={hideManagementLogs}
                onChange={setHideManagementLogs}
                label={
                  <span className={styles.switchLabel}>
                    <IconEyeOff size={15} />
                    {t('logs.hide_management_logs', { prefix: MANAGEMENT_API_PREFIX })}
                  </span>
                }
              />
            </div>
            <div className={styles.toggleFrame}>
              <ToggleSwitch
                checked={showRawLogs}
                onChange={setShowRawLogs}
                label={
                  <span
                    className={styles.switchLabel}
                    title={t('logs.show_raw_logs_hint', {
                      defaultValue: 'Show original log text for easier multi-line copy',
                    })}
                  >
                    <IconCode size={15} />
                    {t('logs.show_raw_logs', { defaultValue: 'Show raw logs' })}
                  </span>
                }
              />
            </div>
          </div>

          {fileLogsAvailable && logState.buffer.length > 0 ? (
            <div className={styles.filterStats}>
              <span>{t('logs.loaded_lines', { count: filteredLines.length })}</span>
              {removedCount > 0 ? (
                <>
                  <span className={styles.metaDivider} aria-hidden="true">
                    ·
                  </span>
                  <span className={styles.removedCount}>
                    {t('logs.filtered_lines', { count: removedCount })}
                  </span>
                </>
              ) : null}
            </div>
          ) : null}
        </div>

        {structuredFiltersExpanded && (
          <div id={structuredFiltersPanelId} className={styles.structuredFilters}>
            <div className={styles.structuredFiltersRows}>
              <div className={styles.filterChipGroup}>
                <span className={styles.filterChipLabel}>{t('logs.filter_method')}</span>
                <div className={styles.filterChipList}>
                  {HTTP_METHODS.map((method) => {
                    const active = filters.methodFilters.includes(method);
                    const count = filters.methodCounts[method] ?? 0;
                    return (
                      <button
                        key={method}
                        type="button"
                        className={`${styles.filterChip} ${active ? styles.filterChipActive : ''}`}
                        onClick={() => filters.toggleMethodFilter(method)}
                        disabled={count === 0 && !active}
                        aria-pressed={active}
                      >
                        <span>{method}</span>
                        <span className={styles.filterChipCount}>{count}</span>
                      </button>
                    );
                  })}
                </div>
              </div>

              <div className={styles.filterChipGroup}>
                <span className={styles.filterChipLabel}>{t('logs.filter_status')}</span>
                <div className={styles.filterChipList}>
                  {STATUS_GROUPS.map((statusGroup) => {
                    const active = filters.statusFilters.includes(statusGroup);
                    const count = filters.statusCounts[statusGroup] ?? 0;
                    return (
                      <button
                        key={statusGroup}
                        type="button"
                        className={`${styles.filterChip} ${active ? styles.filterChipActive : ''}`}
                        onClick={() => filters.toggleStatusFilter(statusGroup)}
                        disabled={count === 0 && !active}
                        aria-pressed={active}
                      >
                        <span>{t(`logs.filter_status_${statusGroup}`)}</span>
                        <span className={styles.filterChipCount}>{count}</span>
                      </button>
                    );
                  })}
                </div>
              </div>

              <div className={styles.filterChipGroup}>
                <span className={styles.filterChipLabel}>{t('logs.filter_path')}</span>
                <div className={styles.filterChipList}>
                  {filters.pathOptions.length === 0 ? (
                    <span className={styles.filterChipHint}>{t('logs.filter_path_empty')}</span>
                  ) : (
                    filters.pathOptions.map(({ path, count }) => {
                      const active = filters.pathFilters.includes(path);
                      return (
                        <button
                          key={path}
                          type="button"
                          className={`${styles.filterChip} ${active ? styles.filterChipActive : ''}`}
                          onClick={() => filters.togglePathFilter(path)}
                          aria-pressed={active}
                          title={path}
                        >
                          <span className={styles.filterChipPath}>{path}</span>
                          <span className={styles.filterChipCount}>{count}</span>
                        </button>
                      );
                    })
                  )}
                </div>
              </div>
            </div>

            <div className={styles.structuredFiltersFooter}>
              <Button
                variant="secondary"
                size="sm"
                onClick={filters.clearStructuredFilters}
                disabled={!filters.hasStructuredFilters}
              >
                {t('logs.clear_filters')}
              </Button>
            </div>
          </div>
        )}
      </div>
    ) : activeTab === 'errors' ? (
      <div className={styles.errorToolbarRow}>
        <div className={styles.searchWrapper}>
          <Input
            type="search"
            value={errorSearchQuery}
            onChange={(event) => {
              setErrorSearchQuery(event.target.value);
              setErrorPage(1);
            }}
            placeholder={t('logs.error_logs_search_placeholder')}
            aria-label={t('logs.error_logs_search_placeholder')}
            className={styles.searchInput}
            rightElement={
              errorSearchQuery ? (
                <button
                  type="button"
                  className={styles.searchClear}
                  onClick={() => {
                    setErrorSearchQuery('');
                    setErrorPage(1);
                  }}
                  title="Clear"
                  aria-label="Clear"
                >
                  <IconX size={15} />
                </button>
              ) : (
                <IconSearch size={15} className={styles.searchIcon} />
              )
            }
          />
        </div>
        {errorLogsLoaded && errorLogs.length > 0 ? (
          <div className={styles.filterStats}>
            <span>
              {filteredErrorLogs.length} / {errorLogs.length}
            </span>
          </div>
        ) : null}
      </div>
    ) : null;

  return (
    <div className={styles.container}>
      <PageControlBar
        leading={
          <SegmentedTabs
            items={logTabItems}
            activeTab={activeTab}
            onChange={handleTabChange}
            ariaLabel={t('nav.logs', { defaultValue: 'Logs' })}
            idBase="logs-workspace-tabs"
          />
        }
        actions={headerActions}
        toolbar={headerToolbar}
      />
      <div className={styles.content}>
        {activeTab === 'logs' && (
          <section className={styles.logCard}>
            {error ? (
              <div className={styles.cardBanner}>
                <div className="error-box">{error}</div>
              </div>
            ) : null}

            {!fileLogsAvailable ? (
              <div className={styles.canvasEmptyWrap}>
                <EmptyState
                  variant="canvas"
                  title={t('logs.empty_title')}
                  description={t('logs.empty_desc')}
                  action={
                    <div className={styles.emptyStateActions}>
                      {!authority.loading && !authority.canEditLogs && authority.reasonText ? (
                        <div
                          className={styles.emptyStateNotice}
                          role="status"
                          data-testid="logs-empty-authority-notice"
                        >
                          {authority.reasonText}
                        </div>
                      ) : null}
                      {settingsSaveError ? (
                        <div className="error-box" role="alert" data-testid="logs-empty-save-error">
                          {settingsSaveError}
                        </div>
                      ) : null}
                      <div className={styles.emptyStateButtons}>
                        <Button
                          variant="secondary"
                          size="sm"
                          onClick={() => handleTabChange('settings')}
                          data-testid="logs-empty-open-settings"
                        >
                          {t('logs.settings_tab')}
                        </Button>
                        <Button
                          size="sm"
                          onClick={() => {
                            void handleToggleSettingField('logging-to-file', true);
                          }}
                          loading={savingField === 'logging-to-file'}
                          disabled={!authority.canEditLogs || savingField !== null}
                          data-testid="logs-empty-enable-button"
                        >
                          {t('logs.enable_file_logging')}
                        </Button>
                      </div>
                    </div>
                  }
                />
              </div>
            ) : loading && !logsLoaded ? (
              <div className={styles.loadingSurface}>{t('logs.loading')}</div>
            ) : error && !logsLoaded ? null : logState.buffer.length > 0 && filteredLines.length > 0 ? (
              <div
                ref={scroller.logViewerRef}
                className={styles.logPanel}
                onScroll={scroller.handleLogScroll}
              >
                {scroller.canLoadMore && (
                  <div className={styles.loadMoreBanner}>
                    <span>{t('logs.load_more_hint')}</span>
                    <div className={styles.loadMoreStats}>
                      <span>
                        {t('logs.loaded_lines', { count: filteredLines.length })}
                      </span>
                      {removedCount > 0 && (
                        <span className={styles.loadMoreCount}>
                          {t('logs.filtered_lines', { count: removedCount })}
                        </span>
                      )}
                      <span className={styles.loadMoreCount}>
                        {t('logs.hidden_lines', { count: logState.visibleFrom })}
                      </span>
                    </div>
                  </div>
                )}
                {showRawLogs ? (
                  <pre className={styles.rawLog} spellCheck={false}>
                    {rawVisibleText}
                  </pre>
                ) : (
                  <div className={styles.logList}>
                    {parsedVisibleLines.map((line, index) => {
                      const rowClassNames = [styles.logRow];
                      if (line.level === 'warn') rowClassNames.push(styles.rowWarn);
                      if (line.level === 'error' || line.level === 'fatal')
                        rowClassNames.push(styles.rowError);
                      return (
                        <div
                          key={`${logState.visibleFrom + index}-${line.raw}`}
                          className={rowClassNames.join(' ')}
                          onDoubleClick={() => {
                            void copyLogLine(line.raw);
                          }}
                          onPointerDown={(event) => startLongPress(event, line.requestId)}
                          onPointerUp={cancelLongPress}
                          onPointerLeave={cancelLongPress}
                          onPointerCancel={cancelLongPress}
                          onPointerMove={handleLongPressMove}
                          title={t('logs.double_click_copy_hint', {
                            defaultValue: 'Double-click to copy',
                          })}
                        >
                          <div className={styles.timestamp}>{line.timestamp || ''}</div>
                          <div className={styles.rowMain}>
                            {line.level && (
                              <span
                                className={[
                                  styles.badge,
                                  line.level === 'info' ? styles.levelInfo : '',
                                  line.level === 'warn' ? styles.levelWarn : '',
                                  line.level === 'error' || line.level === 'fatal'
                                    ? styles.levelError
                                    : '',
                                  line.level === 'debug' ? styles.levelDebug : '',
                                  line.level === 'trace' ? styles.levelTrace : '',
                                ]
                                  .filter(Boolean)
                                  .join(' ')}
                              >
                                {line.level.toUpperCase()}
                              </span>
                            )}

                            {line.source && (
                              <span className={styles.source} title={line.source}>
                                {line.source}
                              </span>
                            )}

                            {line.requestId && (
                              <span
                                className={[styles.badge, styles.requestIdBadge].join(' ')}
                                title={line.requestId}
                              >
                                {line.requestId}
                              </span>
                            )}

                            {typeof line.statusCode === 'number' && (
                              <span
                                className={[
                                  styles.badge,
                                  styles.statusBadge,
                                  line.statusCode >= 200 && line.statusCode < 300
                                    ? styles.statusSuccess
                                    : line.statusCode >= 300 && line.statusCode < 400
                                      ? styles.statusInfo
                                      : line.statusCode >= 400 && line.statusCode < 500
                                        ? styles.statusWarn
                                        : styles.statusError,
                                ].join(' ')}
                              >
                                {line.statusCode}
                              </span>
                            )}

                            {line.latency && <span className={styles.pill}>{line.latency}</span>}
                            {line.ip && <span className={styles.pill}>{line.ip}</span>}

                            {line.method && (
                              <span className={[styles.badge, styles.methodBadge].join(' ')}>
                                {line.method}
                              </span>
                            )}

                            {line.path && (
                              <span className={styles.path} title={line.path}>
                                {line.path}
                              </span>
                            )}

                            {line.message && <span className={styles.message}>{line.message}</span>}
                          </div>
                        </div>
                      );
                    })}
                  </div>
                )}
              </div>
            ) : logState.buffer.length > 0 ? (
              <div className={styles.canvasEmptyWrap}>
                <EmptyState
                  variant="canvas"
                  title={t('logs.search_empty_title')}
                  description={t('logs.search_empty_desc')}
                  action={
                    searchQuery || filters.hasStructuredFilters ? (
                      <Button variant="secondary" size="sm" onClick={clearAllLogFilters}>
                        {t('logs.clear_filters')}
                      </Button>
                    ) : undefined
                  }
                />
              </div>
            ) : (
              <div className={styles.canvasEmptyWrap}>
                <EmptyState
                  variant="canvas"
                  title={t('logs.empty_title')}
                  description={t('logs.enabled_empty_desc')}
                  action={
                    <div className={styles.emptyStateButtons}>
                      <Button
                        variant="secondary"
                        size="sm"
                        onClick={() => loadLogs(false)}
                        disabled={disableLogFileControls || loading}
                      >
                        <span className={styles.buttonContent}>
                          <IconRefreshCw size={15} />
                          {t('logs.refresh_button')}
                        </span>
                      </Button>
                      <Button
                        variant="secondary"
                        size="sm"
                        onClick={() => handleTabChange('settings')}
                      >
                        {t('logs.settings_tab')}
                      </Button>
                    </div>
                  }
                />
              </div>
            )}
          </section>
        )}

        {activeTab === 'errors' && (
          <section className={styles.errorCard}>
            <div className={styles.dataPanelHeader}>
              <p className={styles.panelDescription}>{t('logs.error_logs_description')}</p>
            </div>

            {/* The banner explains an empty listing, so a stale empty result must not keep it
                visible while a refresh is in flight or after that refresh failed. */}
            {requestLogEnabled &&
            errorLogsLoaded &&
            !loadingErrors &&
            !errorLogsError &&
            errorLogs.length === 0 ? (
              <div className={styles.cardBanner}>
                <div className={styles.requestLogNotice} role="status">
                  {t('logs.error_logs_request_log_enabled')}
                </div>
              </div>
            ) : null}

            {errorLogsError ? (
              <div className={styles.cardBanner}>
                <div className="error-box">{errorLogsError}</div>
              </div>
            ) : null}

            <div className={styles.errorPanel}>
              {loadingErrors && !errorLogsLoaded ? (
                <div className={styles.loadingSurface}>{t('common.loading')}</div>
              ) : errorLogsError && !errorLogsLoaded ? null : filteredErrorLogs.length === 0 ? (
                errorLogs.length === 0 ? (
                  <EmptyState
                    variant="canvas"
                    title={t('logs.error_logs_empty')}
                    action={
                      <Button
                        variant="secondary"
                        size="sm"
                        onClick={loadErrorLogs}
                        disabled={disableControls || loadingErrors}
                      >
                        <span className={styles.buttonContent}>
                          <IconRefreshCw size={15} />
                          {t('common.refresh')}
                        </span>
                      </Button>
                    }
                  />
                ) : (
                  <EmptyState
                    variant="canvas"
                    title={t('logs.search_empty_title')}
                    description={t('logs.search_empty_desc')}
                    action={
                      <Button
                        variant="secondary"
                        size="sm"
                        onClick={() => {
                          setErrorSearchQuery('');
                          setErrorPage(1);
                        }}
                      >
                        {t('logs.clear_filters')}
                      </Button>
                    }
                  />
                )
              ) : (
                <>
                  <div className={styles.errorTableWrap}>
                    <table className={styles.errorTable}>
                      <thead>
                        <tr>
                          <th>{t('logs.error_logs_file_col')}</th>
                          <th>{t('logs.error_logs_size')}</th>
                          <th>{t('logs.error_logs_modified')}</th>
                          <th className={styles.errorActionsCol}>{t('common.action')}</th>
                        </tr>
                      </thead>
                      <tbody>
                        {paginatedErrorLogs.map((item) => (
                          <tr key={item.name}>
                            <td className={styles.errorFileNameCell}>
                              <button
                                type="button"
                                className={styles.errorFileNameButton}
                                onClick={() => {
                                  void openErrorLogPreview(item);
                                }}
                                disabled={disableControls}
                                title={item.name}
                              >
                                <code>{item.name}</code>
                              </button>
                            </td>
                            <td className={styles.errorNumericCell}>
                              {typeof item.size === 'number' ? formatByteSize(item.size) : '—'}
                            </td>
                            <td className={styles.errorNumericCell}>
                              {item.modified ? formatUnixTimestamp(item.modified) : '—'}
                            </td>
                            <td className={styles.errorActionsCol}>
                              <div className={styles.errorActionsGroup}>
                                <Button
                                  variant="secondary"
                                  size="sm"
                                  onClick={() => {
                                    void openErrorLogPreview(item);
                                  }}
                                  disabled={disableControls}
                                  data-testid={`error-log-view-${item.name}`}
                                >
                                  <span className={styles.buttonContent}>
                                    <IconEye size={14} />
                                    {t('logs.error_logs_view')}
                                  </span>
                                </Button>
                                <Button
                                  variant="secondary"
                                  size="sm"
                                  onClick={() => downloadErrorLog(item.name)}
                                  disabled={disableControls}
                                >
                                  <span className={styles.buttonContent}>
                                    <IconDownload size={14} />
                                    {t('logs.error_logs_download')}
                                  </span>
                                </Button>
                              </div>
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                  <PaginationFooter
                    variant="flush"
                    currentPage={currentErrorPage}
                    totalPages={totalErrorPages}
                    infoText={t('monitoring.pagination_info', {
                      current: currentErrorPage,
                      total: totalErrorPages,
                      start: errorPaginationStart,
                      end: errorPaginationEnd,
                      count: filteredErrorLogs.length,
                    })}
                    pageSize={errorPageSize}
                    pageSizeOptions={ERROR_LOG_PAGE_SIZE_OPTIONS}
                    pageSizeLabel={t('monitoring.page_size_label')}
                    pageSizeTitle={t('monitoring.page_size_label')}
                    pageSizeAriaLabel={t('monitoring.page_size_label')}
                    formatPageSizeOption={(size) =>
                      t('monitoring.page_size_option', { count: size })
                    }
                    onPageChange={setErrorPage}
                    onPageSizeChange={(nextSize) => {
                      setErrorPageSize(nextSize);
                      setErrorPage(1);
                    }}
                    prevLabel={t('monitoring.pagination_prev')}
                    nextLabel={t('monitoring.pagination_next')}
                    showPageJump
                    jumpPrefixLabel={t('monitoring.pagination_jump_prefix')}
                    jumpSuffixLabel={t('monitoring.pagination_jump_suffix')}
                    jumpInputAriaLabel={t('monitoring.pagination_jump_label')}
                  />
                </>
              )}
            </div>
          </section>
        )}

        {activeTab === 'settings' && (
          <LogsSettingsPanel
            config={config}
            authority={authority}
            savingField={savingField}
            saveError={settingsSaveError}
            onToggleField={handleToggleSettingField}
            onSaveNumberField={handleSaveNumberSettingField}
          />
        )}
      </div>
      <Modal
        open={Boolean(requestLogId)}
        onClose={closeRequestLogModal}
        title={t('logs.request_log_download_title')}
        footer={
          <>
            <Button variant="secondary" onClick={closeRequestLogModal} disabled={requestLogDownloading}>
              {t('common.cancel')}
            </Button>
            <Button
              onClick={() => {
                if (requestLogId) {
                  void downloadRequestLog(requestLogId);
                }
              }}
              loading={requestLogDownloading}
              disabled={!requestLogId}
            >
              {t('common.confirm')}
            </Button>
          </>
        }
      >
        {requestLogId ? t('logs.request_log_download_confirm', { id: requestLogId }) : null}
      </Modal>
      <Modal
        open={Boolean(errorLogPreview)}
        onClose={closeErrorLogPreview}
        width={880}
        title={
          errorLogPreview ? (
            <code className={styles.errorPreviewTitle}>{errorLogPreview.item.name}</code>
          ) : null
        }
        footer={
          <>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => {
                void copyPreviewErrorLog();
              }}
              disabled={!errorLogPreview || errorLogPreview.loading || !errorLogPreview.content}
            >
              <span className={styles.buttonContent}>
                <IconCopy size={14} />
                {t('logs.error_logs_copy')}
              </span>
            </Button>
            <Button
              variant="secondary"
              size="sm"
              onClick={downloadPreviewErrorLog}
              disabled={!errorLogPreview || errorLogPreview.loading}
            >
              <span className={styles.buttonContent}>
                <IconDownload size={14} />
                {t('logs.error_logs_download')}
              </span>
            </Button>
            <Button size="sm" onClick={closeErrorLogPreview}>
              {t('common.close')}
            </Button>
          </>
        }
      >
        {errorLogPreview ? (
          <div className={styles.errorPreviewBody} data-testid="error-log-preview-modal">
            <div className={styles.errorPreviewMetaBar}>
              <div className={styles.errorPreviewMetaStats}>
                <span>
                  {t('logs.error_logs_size')}:{' '}
                  <strong>
                    {errorLogPreview.totalBytes > 0
                      ? formatByteSize(errorLogPreview.totalBytes)
                      : typeof errorLogPreview.item.size === 'number'
                        ? formatByteSize(errorLogPreview.item.size)
                        : '—'}
                  </strong>
                </span>
                {errorLogPreview.item.modified ? (
                  <>
                    <span className={styles.metaDivider} aria-hidden="true">
                      ·
                    </span>
                    <span>
                      {t('logs.error_logs_modified')}:{' '}
                      <strong>{formatUnixTimestamp(errorLogPreview.item.modified)}</strong>
                    </span>
                  </>
                ) : null}
                {!errorLogPreview.loading && !errorLogPreview.error && errorLogPreview.content ? (
                  <>
                    <span className={styles.metaDivider} aria-hidden="true">
                      ·
                    </span>
                    <span>
                      {t('logs.error_logs_preview_lines', {
                        count: errorLogPreview.content.split(/\r?\n/).length,
                      })}
                    </span>
                  </>
                ) : null}
              </div>
              <div className={styles.toggleFrame}>
                <ToggleSwitch
                  checked={errorLogPreviewWrap}
                  onChange={setErrorLogPreviewWrap}
                  label={
                    <span className={styles.switchLabel}>
                      {t('logs.error_logs_preview_wrap')}
                    </span>
                  }
                />
              </div>
            </div>

            {errorLogPreview.truncated ? (
              <div className={styles.errorPreviewTruncated} role="status">
                {t('logs.error_logs_preview_truncated', {
                  total: formatByteSize(errorLogPreview.totalBytes),
                  limit: formatByteSize(ERROR_LOG_PREVIEW_MAX_BYTES),
                })}
              </div>
            ) : null}

            {errorLogPreview.loading ? (
              <div className={styles.errorPreviewState}>{t('logs.error_logs_preview_loading')}</div>
            ) : errorLogPreview.error ? (
              <div className={styles.errorPreviewErrorWrap}>
                <div className="error-box" role="alert">
                  {errorLogPreview.error}
                </div>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => {
                    void openErrorLogPreview(errorLogPreview.item);
                  }}
                >
                  <span className={styles.buttonContent}>
                    <IconRefreshCw size={14} />
                    {t('common.refresh')}
                  </span>
                </Button>
              </div>
            ) : !errorLogPreview.content ? (
              <div className={styles.errorPreviewState}>{t('logs.error_logs_preview_empty')}</div>
            ) : (
              <pre
                className={`${styles.errorPreviewCode} ${
                  errorLogPreviewWrap ? '' : styles.errorPreviewCodeNoWrap
                }`}
                spellCheck={false}
                data-testid="error-log-preview-content"
              >
                {errorLogPreview.content}
              </pre>
            )}
          </div>
        ) : null}
      </Modal>
    </div>
  );
}
