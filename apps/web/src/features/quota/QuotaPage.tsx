/**
 * Quota management page - coordinates the three quota sections.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { useAuthStore, useQuotaStore } from '@/stores';
import { authFilesApi, configFileApi } from '@/services/api';
import {
  monitoringAnalyticsApi,
  type UsageHeaderSnapshot,
} from '@/services/api/usageService';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { IconSearch } from '@/components/ui/icons';
import {
  QuotaSection,
  ANTIGRAVITY_CONFIG,
  CLAUDE_CONFIG,
  CODEX_CONFIG,
  KIMI_CONFIG,
  XAI_CONFIG,
  getScopedQuotaState,
  resolveQuotaDisplayState,
} from '@/components/quota';
import { CodexReauthDialog } from '@/features/oauth/CodexReauthDialog';
import {
  createCodexReauthTargetFromAuthFile,
  type CodexReauthTarget,
} from '@/features/oauth/codexReauthModel';
import type { QuotaSortMode } from '@/components/quota/quotaConfigs';
import type { AuthFileItem, CodexQuotaState } from '@/types';
import {
  buildUsageHeaderSnapshotLookup,
  getHighConfidenceUsageHeaderSnapshotForAuthFile,
} from '@/utils/usageHeaderSnapshots';
import { useAuthFileUsageAnalytics } from '@/features/authFiles/hooks/useAuthFileUsageAnalytics';
import {
  buildAuthFileUsageSummaryMap,
  getAuthFileUsageSummaryKey,
} from '@/features/authFiles/model/authFileUsageSummary';
import { CodexQuotaAggregateSummary } from './CodexQuotaAggregateSummary';
import { buildCodexQuotaAggregateSummary } from './codexQuotaAggregateModel';
import {
  DEFAULT_QUOTA_ACCOUNT_DISPLAY_MODE,
  readQuotaPageUiState,
  writeQuotaPageUiState,
  type QuotaSectionType,
  type QuotaSectionViewMode,
  type QuotaAccountDisplayMode,
} from './quotaPageUiState';
import styles from './QuotaPage.module.scss';

export function QuotaPage() {
  const { t } = useTranslation();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const managementKey = useAuthStore((state) => state.managementKey);
  const codexQuota = useQuotaStore((state) => state.codexQuota);
  const featureAvailability = usePanelFeatureAvailability();
  const managerServiceBase = featureAvailability.managerServiceBase;
  const requestMonitoringAvailable = featureAvailability.requestMonitoringAvailable !== false;
  const {
    rows: codexUsageRows,
    loading: codexUsageLoading,
    load: loadCodexUsageSummaries,
  } = useAuthFileUsageAnalytics({
    managerServiceBase,
    managementKey,
    enabled: requestMonitoringAvailable,
    includeRetained: false,
  });
  const initialUiState = useRef(readQuotaPageUiState());

  const [files, setFiles] = useState<AuthFileItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [searchQuery, setSearchQuery] = useState(() => initialUiState.current.searchQuery);
  const [sortMode, setSortMode] = useState<QuotaSortMode>(() => initialUiState.current.sortMode);
  const [sectionViewModes, setSectionViewModes] = useState(() => ({
    ...initialUiState.current.sectionViewModes,
  }));
  const [codexReauthTarget, setCodexReauthTarget] = useState<CodexReauthTarget | null>(null);
  const [headerSnapshots, setHeaderSnapshots] = useState<UsageHeaderSnapshot[]>([]);
  const [accountDisplayModes, setAccountDisplayModes] = useState(() => ({
    ...initialUiState.current.accountDisplayModes,
  }));

  const disableControls = connectionStatus !== 'connected';
  const sortOptions = useMemo(
    () => [
      { value: 'default', label: t('quota_management.sort_default') },
      { value: 'name-asc', label: t('quota_management.sort_name_asc') },
      { value: 'plan-desc', label: t('quota_management.sort_plan_desc') },
      { value: 'plan-asc', label: t('quota_management.sort_plan_asc') }
    ],
    [t]
  );

  const loadConfig = useCallback(async () => {
    try {
      await configFileApi.fetchConfigYaml();
    } catch (err: unknown) {
      const errorMessage = err instanceof Error ? err.message : t('notification.refresh_failed');
      setError((prev) => prev || errorMessage);
    }
  }, [t]);

  const loadFiles = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await authFilesApi.list();
      setFiles(data?.files || []);
    } catch (err: unknown) {
      const errorMessage = err instanceof Error ? err.message : t('notification.refresh_failed');
      setError(errorMessage);
    } finally {
      setLoading(false);
    }
  }, [t]);

  const loadHeaderSnapshots = useCallback(async () => {
    if (!managerServiceBase) {
      setHeaderSnapshots([]);
      return;
    }
    try {
      const response = await monitoringAnalyticsApi.getHeaderSnapshots(managerServiceBase, managementKey, {
        days: 30,
        limit: 1000,
      });
      setHeaderSnapshots(response.items ?? []);
    } catch {
      setHeaderSnapshots((current) => current);
    }
  }, [managementKey, managerServiceBase]);

  const handleHeaderRefresh = useCallback(async () => {
    await Promise.all([
      loadConfig(),
      loadFiles(),
      loadHeaderSnapshots(),
      loadCodexUsageSummaries(),
    ]);
  }, [loadCodexUsageSummaries, loadConfig, loadFiles, loadHeaderSnapshots]);

  useHeaderRefresh(handleHeaderRefresh);

  useEffect(() => {
    loadFiles();
    loadConfig();
    loadHeaderSnapshots();
    loadCodexUsageSummaries();
  }, [loadFiles, loadConfig, loadHeaderSnapshots, loadCodexUsageSummaries]);

  const headerSnapshotLookup = useMemo(
    () => buildUsageHeaderSnapshotLookup(headerSnapshots),
    [headerSnapshots]
  );

  const codexFiles = useMemo(() => files.filter(CODEX_CONFIG.filterFn), [files]);

  const codexDisplayQuotaByUsageKey = useMemo(() => {
    const quotaMap = new Map<string, CodexQuotaState | undefined>();
    codexFiles.forEach((file) => {
      const activeQuota = getScopedQuotaState(CODEX_CONFIG, codexQuota, file);
      const observedQuota = CODEX_CONFIG.buildObservedState?.(
        file,
        getHighConfidenceUsageHeaderSnapshotForAuthFile(headerSnapshotLookup, file),
        t
      );
      quotaMap.set(
        getAuthFileUsageSummaryKey(file),
        resolveQuotaDisplayState(activeQuota, observedQuota)
      );
    });
    return quotaMap;
  }, [codexFiles, codexQuota, headerSnapshotLookup, t]);

  const codexUsageSummaryByKey = useMemo(
    () =>
      buildAuthFileUsageSummaryMap(codexFiles, {
        retainedRows: [],
        fiveHourRows: codexUsageRows.fiveHour,
        weeklyRows: codexUsageRows.weekly,
        codexQuotaByKey: codexDisplayQuotaByUsageKey,
      }),
    [codexDisplayQuotaByUsageKey, codexFiles, codexUsageRows]
  );

  const codexAggregateSummary = useMemo(
    () => buildCodexQuotaAggregateSummary(codexFiles, codexUsageSummaryByKey),
    [codexFiles, codexUsageSummaryByKey]
  );

  useEffect(() => {
    writeQuotaPageUiState({
      searchQuery,
      sortMode,
      sectionViewModes,
      accountDisplayModes,
    });
  }, [accountDisplayModes, searchQuery, sectionViewModes, sortMode]);

  const getSectionViewMode = useCallback(
    (sectionType: QuotaSectionType): QuotaSectionViewMode =>
      sectionViewModes[sectionType] ?? 'paged',
    [sectionViewModes]
  );

  const setSectionViewMode = useCallback(
    (sectionType: QuotaSectionType, viewMode: QuotaSectionViewMode) => {
      setSectionViewModes((current) => ({
        ...current,
        [sectionType]: viewMode,
      }));
    },
    []
  );

  const handleCodexReauthSuccess = useCallback(async () => {
    await loadFiles();
  }, [loadFiles]);

  const getAccountDisplayMode = useCallback(
    (sectionType: QuotaSectionType): QuotaAccountDisplayMode =>
      accountDisplayModes[sectionType] ?? DEFAULT_QUOTA_ACCOUNT_DISPLAY_MODE,
    [accountDisplayModes]
  );

  const setAccountDisplayMode = useCallback(
    (sectionType: QuotaSectionType, mode: QuotaAccountDisplayMode) => {
      setAccountDisplayModes((current) => ({
        ...current,
        [sectionType]: mode,
      }));
    },
    []
  );

  return (
    <div className={styles.container}>
      {error && <div className={styles.errorBox}>{error}</div>}

      <div className={styles.toolbar}>
        <div className={styles.toolbarField}>
          <Input
            label={t('quota_management.search_label')}
            value={searchQuery}
            onChange={(event) => setSearchQuery(event.target.value)}
            placeholder={t('quota_management.search_placeholder')}
            rightElement={<IconSearch size={16} />}
            aria-label={t('quota_management.search_label')}
          />
        </div>
        <div className={`${styles.toolbarField} ${styles.sortField}`}>
          <label htmlFor="quota-sort-mode" className={styles.toolbarLabel}>
            {t('quota_management.sort_label')}
          </label>
          <Select
            id="quota-sort-mode"
            value={sortMode}
            options={sortOptions}
            onChange={(value) => setSortMode(value as QuotaSortMode)}
            ariaLabel={t('quota_management.sort_label')}
            fullWidth
          />
        </div>
      </div>

      <QuotaSection
        config={CODEX_CONFIG}
        files={files}
        loading={loading}
        disabled={disableControls}
        searchQuery={searchQuery}
        sortMode={sortMode}
        viewMode={getSectionViewMode(CODEX_CONFIG.type)}
        onViewModeChange={(viewMode) => setSectionViewMode(CODEX_CONFIG.type, viewMode)}
        onReauthAccount={(file) => setCodexReauthTarget(createCodexReauthTargetFromAuthFile(file))}
        accountDisplayMode={getAccountDisplayMode(CODEX_CONFIG.type)}
        onAccountDisplayModeChange={(mode) => setAccountDisplayMode(CODEX_CONFIG.type, mode)}
        headerSnapshotLookup={headerSnapshotLookup}
        summary={
          <CodexQuotaAggregateSummary
            summary={codexAggregateSummary}
            loading={codexUsageLoading}
          />
        }
      />
      <QuotaSection
        config={CLAUDE_CONFIG}
        files={files}
        loading={loading}
        disabled={disableControls}
        searchQuery={searchQuery}
        sortMode={sortMode}
        viewMode={getSectionViewMode(CLAUDE_CONFIG.type)}
        onViewModeChange={(viewMode) => setSectionViewMode(CLAUDE_CONFIG.type, viewMode)}
        accountDisplayMode={getAccountDisplayMode(CLAUDE_CONFIG.type)}
        onAccountDisplayModeChange={(mode) => setAccountDisplayMode(CLAUDE_CONFIG.type, mode)}
      />
      <QuotaSection
        config={ANTIGRAVITY_CONFIG}
        files={files}
        loading={loading}
        disabled={disableControls}
        searchQuery={searchQuery}
        sortMode={sortMode}
        viewMode={getSectionViewMode(ANTIGRAVITY_CONFIG.type)}
        onViewModeChange={(viewMode) => setSectionViewMode(ANTIGRAVITY_CONFIG.type, viewMode)}
        accountDisplayMode={getAccountDisplayMode(ANTIGRAVITY_CONFIG.type)}
        onAccountDisplayModeChange={(mode) =>
          setAccountDisplayMode(ANTIGRAVITY_CONFIG.type, mode)
        }
      />
      <QuotaSection
        config={KIMI_CONFIG}
        files={files}
        loading={loading}
        disabled={disableControls}
        searchQuery={searchQuery}
        sortMode={sortMode}
        viewMode={getSectionViewMode(KIMI_CONFIG.type)}
        onViewModeChange={(viewMode) => setSectionViewMode(KIMI_CONFIG.type, viewMode)}
        accountDisplayMode={getAccountDisplayMode(KIMI_CONFIG.type)}
        onAccountDisplayModeChange={(mode) => setAccountDisplayMode(KIMI_CONFIG.type, mode)}
      />
      <QuotaSection
        config={XAI_CONFIG}
        files={files}
        loading={loading}
        disabled={disableControls}
        searchQuery={searchQuery}
        sortMode={sortMode}
        viewMode={getSectionViewMode(XAI_CONFIG.type)}
        onViewModeChange={(viewMode) => setSectionViewMode(XAI_CONFIG.type, viewMode)}
        accountDisplayMode={getAccountDisplayMode(XAI_CONFIG.type)}
        onAccountDisplayModeChange={(mode) => setAccountDisplayMode(XAI_CONFIG.type, mode)}
      />

      <CodexReauthDialog
        open={Boolean(codexReauthTarget)}
        target={codexReauthTarget}
        onClose={() => setCodexReauthTarget(null)}
        onSuccess={handleCodexReauthSuccess}
      />
    </div>
  );
}
