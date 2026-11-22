import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { PageControlBar } from '@/components/common/PageControlBar';
import { SectionActionFooter } from '@/components/common/SectionActionFooter';
import { Button } from '../components/ui/Button';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { SegmentedTabs, type SegmentedTabItem } from '../components/ui/SegmentedTabs';
import { IconRefreshCw } from '../components/ui/icons';
import { Select } from '../components/ui/Select';
import { ToggleSwitch } from '../components/ui/ToggleSwitch';
import { AccountProcessingPolicySection } from '../features/config/components/AccountProcessingPolicySection';
import { usePanelFeatureAvailability } from '../hooks/usePanelFeatureAvailability';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useRequestMonitoringSwitch } from '../hooks/useRequestMonitoringSwitch';
import { configurationText as c } from '../i18n/configurationWorkspace';
import {
  getUsageServiceErrorCode,
  usageServiceApi,
  type ManagerConfigResponse,
  type ManagerRetentionConfig,
  type UsageArchivePreview,
  type UsageArchiveRunSummary,
  type UsageMaintenanceStatus,
} from '../services/api/usageService';
import { useAuthStore, useNotificationStore } from '../stores';
import { STORAGE_KEY_AUTH, STORAGE_KEY_QUOTA_CACHE } from '../utils/constants';
import { formatFileSize } from '../utils/format';
import { FreshRebindDialog } from './FreshRebindDialog';
import type { PublicApiClient } from './publicApi';

type SystemConfigTab = 'monitoring' | 'storage';

const ARCHIVE_PRESET_DAYS = [30, 90, 180, 365] as const;

function isUsageServiceAuthError(error: unknown): boolean {
  if (!error || typeof error !== 'object') return false;
  const candidate = error as {
    kind?: unknown;
    status?: unknown;
    response?: { status?: unknown };
  };
  const status =
    typeof candidate.status === 'number'
      ? candidate.status
      : typeof candidate.response?.status === 'number'
        ? candidate.response.status
        : undefined;
  return status === 401 || status === 403 || candidate.kind === 'authentication';
}

export function FreshSystemWorkspace({
  basePath,
  strategy,
  client,
  credential,
  onUnauthorized,
  logoutDisabled = false,
  hasUnsavedChanges = false,
  onDirtyChange,
}: {
  basePath: string;
  strategy?: string;
  client?: PublicApiClient;
  credential?: string;
  onUnauthorized?: () => void;
  logoutDisabled?: boolean;
  hasUnsavedChanges?: boolean;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { i18n, t } = useTranslation();
  const requestMonitoring = useRequestMonitoringSwitch();
  const { managerServiceBase } = usePanelFeatureAvailability();
  const authManagementKey = useAuthStore((state) => state.managementKey);
  const authLogout = useAuthStore((state) => state.logout);
  const { showConfirmation, showNotification } = useNotificationStore();

  const logoutBlockedRef = useRef(logoutDisabled);
  logoutBlockedRef.current = logoutDisabled;

  const [activeTab, setActiveTab] = useState<SystemConfigTab>('monitoring');
  const [recoveryModalOpen, setRecoveryModalOpen] = useState(false);
  const [rebindOpen, setRebindOpen] = useState(false);
  const [copiedPath, setCopiedPath] = useState(false);

  // Manager config (connection + collector + retention)
  const [configLoaded, setConfigLoaded] = useState(false);
  const [savedBaseline, setSavedBaseline] = useState<{
    collectorMode: string;
    pollIntervalMs: string;
    batchSize: string;
    queryLimit: string;
    queue: string;
    popSide: string;
    tlsSkipVerify: boolean;
    retentionEnabled: boolean;
    retentionDays: string;
  } | null>(null);
  const [configSource, setConfigSource] = useState<'env' | 'db' | ''>('');
  const [cpaBaseUrl, setCpaBaseUrl] = useState('');
  const [cpaKeyConfigured, setCpaKeyConfigured] = useState(false);
  const [collectorMode, setCollectorMode] = useState('auto');
  const [pollIntervalMs, setPollIntervalMs] = useState('500');
  const [batchSize, setBatchSize] = useState('100');
  const [queryLimit, setQueryLimit] = useState('50000');
  const [queue, setQueue] = useState('usage');
  const [popSide, setPopSide] = useState('right');
  const [tlsSkipVerify, setTlsSkipVerify] = useState(false);
  const [collectorSaving, setCollectorSaving] = useState(false);
  const [collectorMessage, setCollectorMessage] = useState<{
    tone: 'ok' | 'warn';
    text: string;
  } | null>(null);

  const [retentionEnabled, setRetentionEnabled] = useState(false);
  const [retentionDays, setRetentionDays] = useState('30');
  const [retentionEnvLocked, setRetentionEnvLocked] = useState(false);
  const [retentionEnabledEnvLocked, setRetentionEnabledEnvLocked] = useState(false);
  const [retentionDaysEnvLocked, setRetentionDaysEnvLocked] = useState(false);
  const [retentionSaving, setRetentionSaving] = useState(false);
  const [retentionMessage, setRetentionMessage] = useState<{
    tone: 'ok' | 'warn';
    text: string;
  } | null>(null);

  // Archive maintenance & runs
  const [maintenanceStatus, setMaintenanceStatus] = useState<UsageMaintenanceStatus | null>(null);
  const [archiveRuns, setArchiveRuns] = useState<UsageArchiveRunSummary[]>([]);
  const [archiveWindowDays, setArchiveWindowDays] = useState('90');
  const [archivePreview, setArchivePreview] = useState<UsageArchivePreview | null>(null);
  const [archiveBusy, setArchiveBusy] = useState(false);
  const [archiveBusyId, setArchiveBusyId] = useState('');

  const [reloading, setReloading] = useState(false);
  const reloadingRef = useRef(false);
  const docsLanguage = i18n.resolvedLanguage?.startsWith('zh') ? '' : 'en/';
  const effectiveKey = authManagementKey || credential || '';
  const managerEditable = Boolean(managerServiceBase && effectiveKey);
  const showQueueAndTlsFields =
    collectorMode === 'auto' || collectorMode === 'resp' || collectorMode === 'subscribe';
  const showPopSideField = collectorMode === 'auto' || collectorMode === 'resp';
  const retentionEnabledDisabled = retentionEnvLocked || retentionEnabledEnvLocked;
  const retentionDaysDisabled = retentionEnvLocked || retentionDaysEnvLocked;
  const retentionSaveLocked =
    retentionEnvLocked || (retentionEnabledEnvLocked && retentionDaysEnvLocked);
  const retentionAnyEnvLocked =
    retentionEnvLocked || retentionEnabledEnvLocked || retentionDaysEnvLocked;

  const handleUsageAuthError = useCallback(
    (error: unknown): boolean => {
      if (!isUsageServiceAuthError(error)) return false;
      onUnauthorized?.();
      return true;
    },
    [onUnauthorized]
  );

  const applyManagerResponse = useCallback(
    (res: ManagerConfigResponse, scope: 'all' | 'collector' | 'retention' = 'all') => {
      setConfigLoaded(true);
      setConfigSource(res.source ?? '');
      setCpaBaseUrl(res.config.cpaConnection?.cpaBaseUrl || '');
      setCpaKeyConfigured(Boolean(res.config.cpaConnection?.managementKeyConfigured));
      const col = res.config.collector;
      const nextCollectorMode = col?.collectorMode || 'auto';
      const nextPollIntervalMs = String(col?.pollIntervalMs || 500);
      const nextBatchSize = String(col?.batchSize || 100);
      const nextQueryLimit = String(col?.queryLimit || 50000);
      const nextQueue = col?.queue || 'usage';
      const nextPopSide = col?.popSide || 'right';
      const nextTlsSkipVerify = Boolean(col?.tlsSkipVerify);
      const updateCollector = (scope === 'all' || scope === 'collector') && Boolean(col);
      if (updateCollector) {
        setCollectorMode(nextCollectorMode);
        setPollIntervalMs(nextPollIntervalMs);
        setBatchSize(nextBatchSize);
        setQueryLimit(nextQueryLimit);
        setQueue(nextQueue);
        setPopSide(nextPopSide);
        setTlsSkipVerify(nextTlsSkipVerify);
      }
      const ret = res.config.retention;
      const nextRetentionEnabled = Boolean(ret?.enabled);
      const nextRetentionDays = String(ret && ret.retentionDays > 0 ? ret.retentionDays : 30);
      const updateRetention = (scope === 'all' || scope === 'retention') && Boolean(ret);
      if (updateRetention && ret) {
        setRetentionEnabled(nextRetentionEnabled);
        setRetentionDays(nextRetentionDays);
        setRetentionEnvLocked(Boolean(ret.envLocked));
        setRetentionEnabledEnvLocked(Boolean(ret.enabledEnvLocked));
        setRetentionDaysEnvLocked(Boolean(ret.retentionDaysEnvLocked));
      }
      setSavedBaseline((prev) => ({
        collectorMode: updateCollector ? nextCollectorMode : (prev?.collectorMode ?? nextCollectorMode),
        pollIntervalMs: updateCollector ? nextPollIntervalMs : (prev?.pollIntervalMs ?? nextPollIntervalMs),
        batchSize: updateCollector ? nextBatchSize : (prev?.batchSize ?? nextBatchSize),
        queryLimit: updateCollector ? nextQueryLimit : (prev?.queryLimit ?? nextQueryLimit),
        queue: updateCollector ? nextQueue : (prev?.queue ?? nextQueue),
        popSide: updateCollector ? nextPopSide : (prev?.popSide ?? nextPopSide),
        tlsSkipVerify: updateCollector ? nextTlsSkipVerify : (prev?.tlsSkipVerify ?? nextTlsSkipVerify),
        retentionEnabled: updateRetention
          ? nextRetentionEnabled
          : (prev?.retentionEnabled ?? nextRetentionEnabled),
        retentionDays: updateRetention ? nextRetentionDays : (prev?.retentionDays ?? nextRetentionDays),
      }));
    },
    []
  );
  const mountedRef = useRef(true);
  const managerConfigSeqRef = useRef(0);
  const copyTimerRef = useRef<number | null>(null);
  const onDirtyChangeRef = useRef(onDirtyChange);
  onDirtyChangeRef.current = onDirtyChange;

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      if (copyTimerRef.current !== null) {
        if (typeof window !== 'undefined' && typeof window.clearTimeout === 'function') {
          window.clearTimeout(copyTimerRef.current);
        } else {
          globalThis.clearTimeout(copyTimerRef.current);
        }
        copyTimerRef.current = null;
      }
    };
  }, []);

  const collectorDirty =
    configLoaded &&
    savedBaseline !== null &&
    (collectorMode !== savedBaseline.collectorMode ||
      pollIntervalMs !== savedBaseline.pollIntervalMs ||
      batchSize !== savedBaseline.batchSize ||
      queryLimit !== savedBaseline.queryLimit ||
      queue !== savedBaseline.queue ||
      popSide !== savedBaseline.popSide ||
      tlsSkipVerify !== savedBaseline.tlsSkipVerify);
  const retentionDirty =
    configLoaded &&
    savedBaseline !== null &&
    (retentionEnabled !== savedBaseline.retentionEnabled ||
      retentionDays !== savedBaseline.retentionDays);
  const localDirty = collectorDirty || retentionDirty;

  useEffect(() => {
    onDirtyChangeRef.current?.(localDirty);
  }, [localDirty]);

  useEffect(() => () => onDirtyChangeRef.current?.(false), []);

  const loadManagerConfig = useCallback(async () => {
    if (!managerServiceBase || !effectiveKey) return;
    const seq = ++managerConfigSeqRef.current;
    try {
      const res = await usageServiceApi.getManagerConfig(managerServiceBase, effectiveKey);
      if (mountedRef.current && managerConfigSeqRef.current === seq) {
        applyManagerResponse(res, 'all');
      }
    } catch (error) {
      if (mountedRef.current && managerConfigSeqRef.current === seq) {
        handleUsageAuthError(error);
      }
    }
  }, [applyManagerResponse, effectiveKey, handleUsageAuthError, managerServiceBase]);

  const refreshArchiveData = useCallback(async () => {
    if (!managerServiceBase || !effectiveKey) return;
    try {
      const [maint, list] = await Promise.all([
        usageServiceApi.getMaintenanceStatus(managerServiceBase, effectiveKey),
        usageServiceApi.listArchives(managerServiceBase, effectiveKey, { limit: 10 }),
      ]);
      if (!mountedRef.current) return;
      setMaintenanceStatus(maint);
      setArchiveRuns(list.runs ?? []);
    } catch (error) {
      if (!mountedRef.current) return;
      if (handleUsageAuthError(error)) return;
      // Optional panel refresh; leave silent if endpoint unavailable on older builds
    }
  }, [effectiveKey, handleUsageAuthError, managerServiceBase]);

  useEffect(() => {
    void loadManagerConfig();
    void refreshArchiveData();
    return () => {
      managerConfigSeqRef.current += 1;
    };
  }, [loadManagerConfig, refreshArchiveData]);

  const isBusy =
    reloading || collectorSaving || retentionSaving || archiveBusy || requestMonitoring.saving;

  const handleReload = useCallback(async () => {
    if (
      reloadingRef.current ||
      collectorSaving ||
      retentionSaving ||
      archiveBusy ||
      requestMonitoring.saving
    ) {
      return;
    }
    if (!managerServiceBase || !effectiveKey) {
      return;
    }
    reloadingRef.current = true;
    setReloading(true);
    setCollectorMessage(null);
    setRetentionMessage(null);
    try {
      await Promise.all([loadManagerConfig(), refreshArchiveData()]);
    } finally {
      reloadingRef.current = false;
      if (mountedRef.current) {
        setReloading(false);
      }
    }
  }, [
    archiveBusy,
    collectorSaving,
    effectiveKey,
    loadManagerConfig,
    managerServiceBase,
    refreshArchiveData,
    requestMonitoring.saving,
    retentionSaving,
  ]);

  useHeaderRefresh(handleReload, !isBusy);

  const handleResetCollector = useCallback(() => {
    if (!savedBaseline) return;
    setCollectorMode(savedBaseline.collectorMode);
    setPollIntervalMs(savedBaseline.pollIntervalMs);
    setBatchSize(savedBaseline.batchSize);
    setQueryLimit(savedBaseline.queryLimit);
    setQueue(savedBaseline.queue);
    setPopSide(savedBaseline.popSide);
    setTlsSkipVerify(savedBaseline.tlsSkipVerify);
    setCollectorMessage(null);
  }, [savedBaseline]);

  const handleResetRetention = useCallback(() => {
    if (!savedBaseline) return;
    if (!retentionEnabledDisabled) {
      setRetentionEnabled(savedBaseline.retentionEnabled);
    }
    if (!retentionDaysDisabled) {
      setRetentionDays(savedBaseline.retentionDays);
    }
    setRetentionMessage(null);
  }, [retentionDaysDisabled, retentionEnabledDisabled, savedBaseline]);

  const handleCopyEntryPath = async () => {
    if (typeof navigator === 'undefined' || !navigator.clipboard?.writeText) return;
    try {
      const origin = typeof window !== 'undefined' ? window.location.origin : '';
      await navigator.clipboard.writeText(`${origin}${basePath}`);
      if (!mountedRef.current) return;
      setCopiedPath(true);
      if (copyTimerRef.current !== null) {
        if (typeof window !== 'undefined' && typeof window.clearTimeout === 'function') {
          window.clearTimeout(copyTimerRef.current);
        } else {
          globalThis.clearTimeout(copyTimerRef.current);
        }
      }
      const schedule =
        typeof window !== 'undefined' && typeof window.setTimeout === 'function'
          ? window.setTimeout.bind(window)
          : globalThis.setTimeout.bind(globalThis);
      copyTimerRef.current = Number(
        schedule(() => {
          copyTimerRef.current = null;
          if (mountedRef.current) setCopiedPath(false);
        }, 1800)
      );
    } catch {
      // ignore clipboard denial
    }
  };
  const handleSaveCollector = async () => {
    if (!managerServiceBase || !effectiveKey || collectorSaving) return;
    const parseIntInput = (raw: string) => {
      const trimmed = raw.trim();
      return trimmed === '' ? NaN : Number(trimmed);
    };
    const poll = parseIntInput(pollIntervalMs);
    const batch = parseIntInput(batchSize);
    const limit = parseIntInput(queryLimit);
    if (
      !Number.isInteger(poll) ||
      poll < 10 ||
      poll > 60000 ||
      !Number.isInteger(batch) ||
      batch < 1 ||
      batch > 5000 ||
      !Number.isInteger(limit) ||
      limit < 1 ||
      limit > 100000
    ) {
      setCollectorMessage({ tone: 'warn', text: c('collectorSaveFailed') });
      return;
    }
    const trimmedQueue = queue.trim();
    if (showQueueAndTlsFields && !trimmedQueue) {
      setCollectorMessage({ tone: 'warn', text: c('collectorQueueRequired') });
      return;
    }
    setCollectorSaving(true);
    setCollectorMessage(null);
    managerConfigSeqRef.current += 1;
    try {
      const res = await usageServiceApi.patchSettings(
        managerServiceBase,
        {
          collector: {
            collectorMode,
            pollIntervalMs: poll,
            batchSize: batch,
            queryLimit: limit,
            ...(showQueueAndTlsFields
              ? {
                  queue: trimmedQueue,
                  ...(showPopSideField ? { popSide } : {}),
                  tlsSkipVerify,
                }
              : {}),
          },
        },
        effectiveKey
      );
      if (!mountedRef.current) return;
      managerConfigSeqRef.current += 1;
      applyManagerResponse(res, 'collector');
      setCollectorMessage({ tone: 'ok', text: c('collectorSaved') });
    } catch (error) {
      if (!mountedRef.current) return;
      if (handleUsageAuthError(error)) return;
      const code = getUsageServiceErrorCode(error);
      setCollectorMessage({
        tone: 'warn',
        text: code
          ? t(`usage_service_errors.${code}`, { defaultValue: c('collectorSaveFailed') })
          : c('collectorSaveFailed'),
      });
    } finally {
      if (mountedRef.current) setCollectorSaving(false);
    }
  };

  const handleSaveRetention = async () => {
    if (!managerServiceBase || !effectiveKey || retentionSaving || retentionSaveLocked) return;
    const retentionPatch: Partial<
      Omit<ManagerRetentionConfig, 'envLocked' | 'enabledEnvLocked' | 'retentionDaysEnvLocked'>
    > = {};
    if (!retentionEnabledDisabled) {
      retentionPatch.enabled = retentionEnabled;
    }
    if (!retentionDaysDisabled) {
      const trimmedDays = retentionDays.trim();
      const days = trimmedDays === '' ? NaN : Number(trimmedDays);
      if (!Number.isInteger(days) || days < 1 || days > 3650) {
        setRetentionMessage({ tone: 'warn', text: c('retentionDaysInvalid') });
        return;
      }
      retentionPatch.retentionDays = days;
    }
    if (Object.keys(retentionPatch).length === 0) return;
    setRetentionSaving(true);
    setRetentionMessage(null);
    managerConfigSeqRef.current += 1;
    try {
      const res = await usageServiceApi.patchSettings(
        managerServiceBase,
        {
          retention: retentionPatch,
        },
        effectiveKey
      );
      if (!mountedRef.current) return;
      managerConfigSeqRef.current += 1;
      applyManagerResponse(res, 'retention');
      setRetentionMessage({ tone: 'ok', text: c('retentionSaved') });
    } catch (error) {
      if (!mountedRef.current) return;
      if (handleUsageAuthError(error)) return;
      const code = getUsageServiceErrorCode(error);
      setRetentionMessage({
        tone: 'warn',
        text: code
          ? t(`usage_service_errors.${code}`, { defaultValue: c('retentionSaveFailed') })
          : c('retentionSaveFailed'),
      });
    } finally {
      if (mountedRef.current) setRetentionSaving(false);
    }
  };

  const resolveArchiveErrorText = (error: unknown) => {
    const code = getUsageServiceErrorCode(error);
    if (code) {
      return t(`usage_service_errors.${code}`, {
        defaultValue: t('usage_service_errors.request_failed'),
      });
    }
    return t('usage_service_errors.request_failed');
  };

  const resolveArchiveCutoffMs = () => {
    const days = Number(archiveWindowDays);
    if (!Number.isInteger(days) || days < 1 || days > 3650) {
      showNotification(c('retentionDaysInvalid'), 'warning');
      return null;
    }
    return Date.now() - days * 86_400_000;
  };

  const handlePreviewArchive = async () => {
    if (!managerServiceBase || !effectiveKey || archiveBusy) return;
    const cutoff = resolveArchiveCutoffMs();
    if (!cutoff) return;
    setArchiveBusy(true);
    try {
      const preview = await usageServiceApi.previewArchive(managerServiceBase, effectiveKey, cutoff);
      if (!mountedRef.current) return;
      setArchivePreview(preview);
    } catch (error) {
      if (!mountedRef.current) return;
      if (handleUsageAuthError(error)) return;
      showNotification(resolveArchiveErrorText(error), 'error');
    } finally {
      if (mountedRef.current) setArchiveBusy(false);
    }
  };

  const handleCreateArchive = async () => {
    if (!managerServiceBase || !effectiveKey || archiveBusy) return;
    const cutoff = resolveArchiveCutoffMs();
    if (!cutoff) return;
    setArchiveBusy(true);
    try {
      await usageServiceApi.createArchive(managerServiceBase, effectiveKey, cutoff);
      if (!mountedRef.current) return;
      setArchivePreview(null);
      await refreshArchiveData();
      if (!mountedRef.current) return;
      showNotification(c('archiveCreateBtn'), 'success');
    } catch (error) {
      if (!mountedRef.current) return;
      if (handleUsageAuthError(error)) return;
      showNotification(resolveArchiveErrorText(error), 'error');
    } finally {
      if (mountedRef.current) setArchiveBusy(false);
    }
  };

  const runArchiveAction = async (
    id: string,
    action: 'verify' | 'delete' | 'resume' | 'cancel'
  ) => {
    if (!managerServiceBase || !effectiveKey || archiveBusy) return;
    setArchiveBusy(true);
    setArchiveBusyId(id);
    try {
      if (action === 'verify') {
        await usageServiceApi.verifyArchive(managerServiceBase, effectiveKey, id, true);
      } else if (action === 'delete') {
        await usageServiceApi.deleteArchive(managerServiceBase, effectiveKey, id, true);
      } else if (action === 'resume') {
        await usageServiceApi.resumeArchive(managerServiceBase, effectiveKey, id, true);
      } else {
        await usageServiceApi.cancelArchive(managerServiceBase, effectiveKey, id);
      }
      if (!mountedRef.current) return;
      await refreshArchiveData();
    } catch (error) {
      if (!mountedRef.current) return;
      if (handleUsageAuthError(error)) return;
      showNotification(resolveArchiveErrorText(error), 'error');
    } finally {
      if (mountedRef.current) {
        setArchiveBusy(false);
        setArchiveBusyId('');
      }
    }
  };

  const handleDeleteArchiveRun = (id: string) => {
    showConfirmation({
      title: c('archiveDeleteConfirmTitle'),
      message: c('archiveDeleteConfirmBody'),
      confirmText: c('archiveDeleteBtn'),
      variant: 'danger',
      onConfirm: () => runArchiveAction(id, 'delete'),
    });
  };

  const handleClearLoginStorage = () => {
    if (logoutDisabled) return;
    showConfirmation({
      title: t('system_info.clear_login_title', { defaultValue: 'Clear Login Storage' }),
      message:
        t('system_info.clear_login_confirm') +
        (hasUnsavedChanges || localDirty ? '\n' + c('leaveHelp') : ''),
      variant: 'danger',
      confirmText: t('common.confirm'),
      onConfirm: () => {
        if (logoutBlockedRef.current) return;
        authLogout();
        if (typeof localStorage === 'undefined') return;
        const keysToRemove = [
          STORAGE_KEY_AUTH,
          STORAGE_KEY_QUOTA_CACHE,
          'isLoggedIn',
          'apiBase',
          'apiUrl',
          'managementKey',
        ];
        keysToRemove.forEach((key) => localStorage.removeItem(key));
        showNotification(t('notification.login_storage_cleared'), 'success');
      },
    });
  };

  const strategyLabel =
    strategy === 'connect_external'
      ? c('strategyExternal')
      : strategy === 'adopt_existing'
        ? c('strategyAdopt')
        : strategy === 'upgrade_existing'
          ? c('strategyUpgrade')
          : strategy === 'fresh_managed'
            ? c('strategyManaged')
            : 'CPAMP';

  const tabItems: SegmentedTabItem<SystemConfigTab>[] = [
    { id: 'monitoring', label: c('systemTabMonitoring') },
    { id: 'storage', label: c('systemTabInstanceAndStorage') },
  ];

  const collectorOptions = [
    { value: 'auto', label: c('collectorModeAuto') },
    { value: 'http', label: c('collectorModeHttp') },
    { value: 'resp', label: c('collectorModeResp') },
    { value: 'subscribe', label: c('collectorModeSubscribe') },
  ];

  const popSideOptions = [
    { value: 'right', label: c('collectorPopRight') },
    { value: 'left', label: c('collectorPopLeft') },
  ];

  const collectorEnvLocked = configSource === 'env';
  const collectorInputsDisabled =
    !managerEditable || !configLoaded || collectorEnvLocked || collectorSaving;

  return (
    <div className="fresh-system-workspace" data-testid="fresh-system-workspace">
      <PageControlBar
        leading={
          <SegmentedTabs
            items={tabItems}
            activeTab={activeTab}
            ariaLabel={c('systemTabsLabel')}
            onChange={setActiveTab}
            idBase="system-config-tabs"
          />
        }
        actions={
          <>
            <span className="badge" data-testid="system-strategy-badge">
              {strategyLabel}
            </span>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => void handleReload()}
              loading={reloading}
              disabled={isBusy}
              data-testid="system-reload-btn"
            >
              <IconRefreshCw size={15} />
              {c('reload')}
            </Button>
          </>
        }
      />

      <div
        role="tabpanel"
        id="system-config-tabs-panel-monitoring"
        aria-labelledby="system-config-tabs-monitoring"
        hidden={activeTab !== 'monitoring'}
        className="system-tab-panel"
      >
        <div className="stack">
          <section className="card" data-testid="system-request-monitoring-card">
            <div className="system-switch-header">
              <div>
                <h2>{c('requestMonitoring')}</h2>
                <p className="muted">{c('requestMonitoringHelp')}</p>
              </div>
              <div className="system-switch-controls" data-testid="system-request-monitoring-switch">
                <span
                  className={requestMonitoring.enabled ? 'badge badge-emerald' : 'badge'}
                  data-testid="system-request-monitoring-state"
                >
                  {requestMonitoring.enabled ? c('requestMonitoringOn') : c('requestMonitoringOff')}
                </span>
                {requestMonitoring.writable ? (
                  <ToggleSwitch
                    checked={requestMonitoring.enabled}
                    disabled={!requestMonitoring.loaded || requestMonitoring.saving}
                    ariaLabel={c('requestMonitoring')}
                    label={requestMonitoring.saving ? c('requestMonitoringSaving') : undefined}
                    onChange={(next) => void requestMonitoring.setEnabled(next)}
                  />
                ) : (
                  <span className="muted system-inline-note">
                    {c('requestMonitoringUnavailable')}
                  </span>
                )}
              </div>
            </div>
            {requestMonitoring.failed ? (
              <div className="notice warn" data-testid="system-request-monitoring-error">
                {c('requestMonitoringFailed')}
              </div>
            ) : null}

            {managerEditable && configLoaded ? (
              <div className="system-collector-subpanel" data-testid="system-collector-settings">
                <div className="system-subpanel-head">
                  <div>
                    <h3>{c('collectorParamsTitle')}</h3>
                    <p className="muted">{c('collectorParamsHelp')}</p>
                  </div>
                  {collectorEnvLocked ? (
                    <span className="badge badge-amber">{c('retentionEnvLocked')}</span>
                  ) : null}
                </div>

                {collectorEnvLocked ? (
                  <div className="notice warn">{c('collectorEnvLocked')}</div>
                ) : null}

                <div className="system-collector-grid">
                  <div className="system-select-field">
                    <span className="system-field-label">{c('collectorModeLabel')}</span>
                    <Select
                      value={collectorMode}
                      options={collectorOptions}
                      onChange={setCollectorMode}
                      disabled={collectorInputsDisabled}
                      ariaLabel={c('collectorModeLabel')}
                    />
                  </div>
                  <Input
                    label={c('collectorPollInterval')}
                    type="number"
                    min="10"
                    max="60000"
                    value={pollIntervalMs}
                    onChange={(event) => setPollIntervalMs(event.target.value)}
                    disabled={collectorInputsDisabled}
                  />
                  <Input
                    label={c('collectorBatchSize')}
                    type="number"
                    min="1"
                    max="5000"
                    value={batchSize}
                    onChange={(event) => setBatchSize(event.target.value)}
                    disabled={collectorInputsDisabled}
                  />
                  <Input
                    label={c('collectorQueryLimit')}
                    type="number"
                    min="1"
                    max="100000"
                    value={queryLimit}
                    onChange={(event) => setQueryLimit(event.target.value)}
                    disabled={collectorInputsDisabled}
                  />
                </div>

                {showQueueAndTlsFields ? (
                  <div className="system-resp-subcard" data-testid="system-collector-resp-fields">
                    <div className="system-form-grid">
                      <Input
                        label={c('collectorQueueLabel')}
                        placeholder="usage"
                        value={queue}
                        onChange={(event) => setQueue(event.target.value)}
                        disabled={collectorInputsDisabled}
                      />
                      {showPopSideField ? (
                        <div className="system-select-field">
                          <span className="system-field-label">{c('collectorPopSideLabel')}</span>
                          <Select
                            value={popSide}
                            options={popSideOptions}
                            onChange={setPopSide}
                            disabled={collectorInputsDisabled}
                            ariaLabel={c('collectorPopSideLabel')}
                          />
                        </div>
                      ) : null}
                    </div>
                    <ToggleSwitch
                      checked={tlsSkipVerify}
                      disabled={collectorInputsDisabled}
                      ariaLabel={c('collectorTlsSkipVerify')}
                      label={c('collectorTlsSkipVerify')}
                      onChange={setTlsSkipVerify}
                    />
                  </div>
                ) : null}

                <SectionActionFooter
                  status={
                    collectorMessage ? (
                      <span
                        className={
                          collectorMessage.tone === 'warn'
                            ? 'system-feedback warn'
                            : 'system-feedback ok'
                        }
                        role="status"
                      >
                        {collectorMessage.text}
                      </span>
                    ) : collectorDirty ? (
                      c('dirty')
                    ) : (
                      c('noDrafts')
                    )
                  }
                >
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={handleResetCollector}
                    disabled={!collectorDirty || collectorInputsDisabled}
                    data-testid="system-collector-reset-btn"
                  >
                    {c('discard')}
                  </Button>
                  <Button
                    variant="primary"
                    size="sm"
                    onClick={() => void handleSaveCollector()}
                    loading={collectorSaving}
                    disabled={collectorInputsDisabled || !collectorDirty}
                    data-testid="system-collector-save-btn"
                  >
                    {collectorSaving ? c('collectorSaving') : c('collectorSave')}
                  </Button>
                </SectionActionFooter>

              </div>
            ) : null}
          </section>

          <section className="card" data-testid="system-automation-card">
            <AccountProcessingPolicySection
              headerExtra={<span className="badge">{c('automationImmediateNote')}</span>}
            />
          </section>
        </div>
      </div>

      <div
        role="tabpanel"
        id="system-config-tabs-panel-storage"
        aria-labelledby="system-config-tabs-storage"
        hidden={activeTab !== 'storage'}
        className="system-tab-panel"
      >
        <div className="stack">
          <section className="card" data-testid="system-entry-card">
            <div className="card-head">
              <div>
                <h2>{c('connectionOverview')}</h2>
                <p className="muted">{c('entrySource')}</p>
              </div>
            </div>

            <div className="system-connection-grid">
              <div className="system-connection-item">
                <span className="system-connection-label">{c('consoleEntryLabel')}</span>
                <div className="system-connection-value-row">
                  <span className="path" data-testid="system-entry-path">
                    <code>{basePath}</code>
                  </span>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => void handleCopyEntryPath()}
                    data-testid="system-copy-entry-btn"
                  >
                    {copiedPath ? c('copiedEntryPath') : c('copyEntryPath')}
                  </Button>
                </div>
              </div>

              <div className="system-connection-item">
                <span className="system-connection-label">{c('cpaUpstreamLabel')}</span>
                <div className="system-connection-value-row">
                  <code className="system-mono-value" data-testid="system-cpa-base-url">
                    {cpaBaseUrl || '—'}
                  </code>
                </div>
              </div>

              <div className="system-connection-item">
                <span className="system-connection-label">{c('credentialStatusLabel')}</span>
                <div className="system-connection-value-row">
                  <span
                    className={
                      cpaKeyConfigured ? 'badge badge-emerald' : 'badge'
                    }
                    data-testid="system-cpa-credential-state"
                  >
                    {configLoaded
                      ? cpaKeyConfigured
                        ? c('credentialConfigured')
                        : c('credentialMissing')
                      : '—'}
                  </span>
                </div>
              </div>
            </div>

            {strategy === 'connect_external' && client && credential && onUnauthorized ? (
              <div className="system-subblock" data-testid="system-external-rebind-card">
                <div className="system-subblock-head">
                  <div>
                    <h3>{c('externalConnectionTitle')}</h3>
                    <p className="muted">{c('externalConnectionHelp')}</p>
                  </div>
                  <Button
                    variant="secondary"
                    size="sm"
                    data-testid="system-rebind-btn"
                    onClick={() => setRebindOpen(true)}
                  >
                    {c('rebindAction')}
                  </Button>
                </div>
              </div>
            ) : null}
          </section>

          <section className="card" data-testid="system-retention-card">
            <div className="system-switch-header">
              <div>
                <h2>{c('retentionTitle')}</h2>
                <p className="muted">{c('retentionHelp')}</p>
              </div>
              <div className="system-switch-controls">
                {retentionAnyEnvLocked ? (
                  <span className="badge badge-amber">{c('retentionEnvLocked')}</span>
                ) : null}
                <ToggleSwitch
                  checked={retentionEnabled}
                  disabled={
                    !managerEditable || !configLoaded || retentionEnabledDisabled || retentionSaving
                  }
                  ariaLabel={c('retentionToggle')}
                  label={c('retentionToggle')}
                  onChange={setRetentionEnabled}
                />
              </div>
            </div>

            {retentionAnyEnvLocked ? (
              <div className="notice warn" data-testid="system-retention-env-lock-hint">
                {retentionSaveLocked
                  ? c('retentionAllEnvLockedHint')
                  : retentionEnabledDisabled
                    ? c('retentionEnabledEnvLockedHint')
                    : c('retentionDaysEnvLockedHint')}
              </div>
            ) : null}

            <div className="system-retention-field">
              <div className="system-retention-inline">
                <label htmlFor="system-retention-days-input" className="system-field-label">
                  {c('retentionDaysLabel')}
                </label>
                <div className="system-retention-days">
                  <Input
                    id="system-retention-days-input"
                    aria-label={c('retentionDaysLabel')}
                    type="number"
                    min="1"
                    max="3650"
                    value={retentionDays}
                    onChange={(event) => setRetentionDays(event.target.value)}
                    disabled={
                      !managerEditable || !configLoaded || retentionDaysDisabled || retentionSaving
                    }
                  />
                </div>
                <span className="system-inline-unit">{c('archiveDaysSuffix')}</span>
              </div>
            </div>

            <SectionActionFooter
              status={
                retentionMessage ? (
                  <span
                    className={
                      retentionMessage.tone === 'warn'
                        ? 'system-feedback warn'
                        : 'system-feedback ok'
                    }
                    role="status"
                  >
                    {retentionMessage.text}
                  </span>
                ) : retentionDirty ? (
                  c('dirty')
                ) : (
                  c('noDrafts')
                )
              }
            >
              <Button
                variant="secondary"
                size="sm"
                onClick={handleResetRetention}
                disabled={
                  !retentionDirty ||
                  !managerEditable ||
                  !configLoaded ||
                  retentionSaveLocked ||
                  retentionSaving
                }
                data-testid="system-retention-reset-btn"
              >
                {c('discard')}
              </Button>
              <Button
                variant="primary"
                size="sm"
                onClick={() => void handleSaveRetention()}
                loading={retentionSaving}
                disabled={
                  !retentionDirty ||
                  !managerEditable ||
                  !configLoaded ||
                  retentionSaveLocked ||
                  retentionSaving
                }
                data-testid="system-retention-save-btn"
              >
                {c('retentionSave')}
              </Button>
            </SectionActionFooter>
          </section>

          {managerEditable ? (
            <section className="card" data-testid="system-archive-cleanup-card">
              <div className="card-head">
                <div>
                  <h2>{c('archiveCleanupTitle')}</h2>
                  <p className="muted">{c('archiveCleanupHelp')}</p>
                </div>
              </div>

              {maintenanceStatus ? (
                <div className="system-archive-stats">
                  <div className="system-stat-tile">
                    <span>{c('archiveRawEvents')}</span>
                    <strong>{maintenanceStatus.raw_event_count.toLocaleString()}</strong>
                  </div>
                  <div className="system-stat-tile">
                    <span>{c('archiveArchivedEvents')}</span>
                    <strong>{maintenanceStatus.raw_archived_event_count.toLocaleString()}</strong>
                  </div>
                  <div className="system-stat-tile">
                    <span>{c('archiveDeletedEvents')}</span>
                    <strong>{maintenanceStatus.raw_deleted_event_count.toLocaleString()}</strong>
                  </div>
                </div>
              ) : null}

              {maintenanceStatus && !maintenanceStatus.readiness.archive_delete_enabled ? (
                <div className="notice warn">{c('archiveDeleteUnavailable')}</div>
              ) : null}

              <div className="system-archive-bar">
                <span className="system-field-label">{c('archiveWindowLabel')}</span>
                <div className="system-preset-chips">
                  {ARCHIVE_PRESET_DAYS.map((days) => (
                    <button
                      key={days}
                      type="button"
                      className={`toggle-chip ${archiveWindowDays === String(days) ? 'is-active-primary' : ''}`}
                      onClick={() => {
                        setArchiveWindowDays(String(days));
                        setArchivePreview(null);
                      }}
                    >
                      {`${days} ${c('archiveDaysSuffix')}`}
                    </button>
                  ))}
                </div>
                <div className="system-archive-custom-wrap">
                  <div className="system-archive-custom-input">
                    <Input
                      aria-label={c('archiveWindowLabel')}
                      placeholder={c('archiveCustomDaysPlaceholder')}
                      type="number"
                      min="1"
                      max="3650"
                      value={archiveWindowDays}
                      onChange={(event) => {
                        setArchiveWindowDays(event.target.value);
                        setArchivePreview(null);
                      }}
                    />
                  </div>
                  <span className="system-inline-unit">{c('archiveDaysSuffix')}</span>
                </div>
                <div className="system-archive-bar-actions">
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => void handlePreviewArchive()}
                    disabled={archiveBusy}
                    data-testid="system-archive-preview-btn"
                  >
                    {c('archivePreviewBtn')}
                  </Button>
                  <Button
                    variant="primary"
                    size="sm"
                    onClick={() => void handleCreateArchive()}
                    disabled={archiveBusy}
                    data-testid="system-archive-create-btn"
                  >
                    {c('archiveCreateBtn')}
                  </Button>
                </div>
              </div>

              {archivePreview ? (
                <div className="notice" data-testid="system-archive-preview-result">
                  {archivePreview.event_count > 0
                    ? `${c('archivePreviewSummary', archivePreview.event_count)} (${formatFileSize(archivePreview.estimated_bytes)})`
                    : c('archivePreviewEmpty')}
                </div>
              ) : null}

              <div className="system-archive-runs">
                {archiveRuns.length === 0 ? (
                  <div className="system-archive-runs-empty">
                    <h3>{c('archiveRunsTitle')}</h3>
                    <span className="muted">{c('archiveNoRuns')}</span>
                  </div>
                ) : (
                  <>
                    <h3>{c('archiveRunsTitle')}</h3>
                    <div className="system-archive-run-list">
                    {archiveRuns.map((run) => {
                      const rowBusy = archiveBusy && archiveBusyId === run.id;
                      const canVerify = run.status === 'archived' || run.status === 'verify_failed';
                      const canDelete =
                        (run.status === 'verified' || run.status === 'delete_failed') &&
                        Boolean(maintenanceStatus?.readiness.archive_delete_enabled);
                      const canResume =
                        run.status === 'paused' ||
                        run.status === 'failed' ||
                        run.status === 'archiving';
                      const canCancel =
                        run.status !== 'completed' &&
                        run.status !== 'cancelled' &&
                        run.status !== 'deleting';

                      return (
                        <div className="system-archive-run-row" key={run.id}>
                          <div className="system-archive-run-meta">
                            <code>{run.id}</code>
                            <span className="status-pill is-neutral">{run.status}</span>
                            <span className="muted">
                              {`${run.archived_event_count.toLocaleString()} / ${run.event_count.toLocaleString()}`}
                            </span>
                          </div>
                          <div className="system-archive-run-actions">
                            {canVerify ? (
                              <Button
                                variant="secondary"
                                size="sm"
                                disabled={archiveBusy}
                                loading={rowBusy}
                                onClick={() => void runArchiveAction(run.id, 'verify')}
                              >
                                {c('archiveVerifyBtn')}
                              </Button>
                            ) : null}
                            {canDelete ? (
                              <Button
                                variant="danger"
                                size="sm"
                                disabled={archiveBusy}
                                loading={rowBusy}
                                onClick={() => handleDeleteArchiveRun(run.id)}
                              >
                                {c('archiveDeleteBtn')}
                              </Button>
                            ) : null}
                            {canResume ? (
                              <Button
                                variant="secondary"
                                size="sm"
                                disabled={archiveBusy}
                                loading={rowBusy}
                                onClick={() => void runArchiveAction(run.id, 'resume')}
                              >
                                {c('archiveResumeBtn')}
                              </Button>
                            ) : null}
                            {canCancel ? (
                              <Button
                                variant="ghost"
                                size="sm"
                                disabled={archiveBusy}
                                loading={rowBusy}
                                onClick={() => void runArchiveAction(run.id, 'cancel')}
                              >
                                {c('archiveCancelBtn')}
                              </Button>
                            ) : null}
                          </div>
                        </div>
                      );
                    })}
                    </div>
                  </>
                )}
              </div>
            </section>
          ) : null}

          <section className="card" data-testid="system-security-card">
            <div className="card-head">
              <div>
                <h2>{c('security')}</h2>
                <p className="muted">{c('securityHelp')}</p>
              </div>
            </div>

            <div className="system-security-grid">
              <div className="system-security-item">
                <div>
                  <h3>{c('session')}</h3>
                  <p className="muted">{c('noReset')}</p>
                </div>
                <button
                  type="button"
                  className="btn btn-secondary"
                  data-testid="view-offline-recovery-btn"
                  onClick={() => setRecoveryModalOpen(true)}
                >
                  {c('recovery')}
                </button>
              </div>

              <div className="system-security-item">
                <div>
                  <h3>{t('system_info.clear_login_title')}</h3>
                  <p className="muted">{t('system_info.clear_login_desc')}</p>
                </div>
                <Button
                  variant="danger"
                  size="sm"
                  onClick={handleClearLoginStorage}
                  disabled={logoutDisabled}
                  data-testid="system-clear-login-btn"
                >
                  {t('system_info.clear_login_button')}
                </Button>
              </div>
            </div>
          </section>
        </div>
      </div>

      {strategy === 'connect_external' && client && credential && onUnauthorized ? (
        <FreshRebindDialog
          open={rebindOpen}
          client={client}
          credential={credential}
          onClose={() => setRebindOpen(false)}
          onRebound={() => {
            void loadManagerConfig();
          }}
          onUnauthorized={onUnauthorized}
        />
      ) : null}

      <Modal
        open={recoveryModalOpen}
        title={c('recoveryTitle')}
        onClose={() => setRecoveryModalOpen(false)}
        footer={
          <button
            type="button"
            className="btn btn-primary"
            data-testid="close-recovery-btn"
            onClick={() => setRecoveryModalOpen(false)}
          >
            {c('understood')}
          </button>
        }
      >
        <div data-testid="recovery-modal">
          <ol className="recovery-steps">
            <li>{c('backup')}</li>
            <li>{c('stop')}</li>
            <li>
              {c('reset')} <code>cpa-manager-plus reset-admin-key</code>
            </li>
            <li>{c('restart')}</li>
          </ol>
          <div className="notice warn">{c('recoveryWarning')}</div>
          <p>
            <a
              href={
                'https://cpamp-docs.seakee.dev/next/' +
                docsLanguage +
                'operations/reset-admin-key.html'
              }
              target="_blank"
              rel="noopener noreferrer"
            >
              {c('recoveryDocs')}
            </a>
          </p>
        </div>
      </Modal>
    </div>
  );
}
