import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { PageControlBar } from '@/components/common/PageControlBar';
import { SectionActionFooter } from '@/components/common/SectionActionFooter';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import {
  IconChartLine,
  IconCheck,
  IconChevronRight,
  IconClock,
  IconCode,
  IconCopy,
  IconModelCluster,
  IconRefreshCw,
  IconRotateCcw,
  IconSatellite,
  IconSearch,
  IconShieldCheck,
  IconTriangleAlert,
  IconX,
} from '@/components/ui/icons';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import type { LocalizedText } from '@/i18n/localizedText';
import { formatLocalizedText } from '@/i18n/localizedText';
import { apiKeysApi } from '@/services/api/apiKeys';
import { isGatewayPendingError, modelsApi } from '@/services/api/models';
import {
  getUsageServiceErrorCode,
  usageServiceApi,
  type UsageServiceStatus,
} from '@/services/api/usageService';
import { useAuthStore, useConfigStore, useModelsStore, useNotificationStore } from '@/stores';
import { copyToClipboard } from '@/utils/clipboard';
import { resolveCollectorErrorHintKey } from '@/utils/collectorErrorHint';
import { formatFileSize } from '@/utils/format';
import { classifyModels } from '@/utils/models';
import { createPublicApiClient, PublicApiError } from '@/fullweb/publicApi';
import type { FullWebCredential, PublicApiClient } from '@/fullweb/publicApi';
import type { ArtifactStatus } from '@/fullweb/freshArtifact';
import { DatabaseStatusCard } from './components/DatabaseStatusCard';
import './RuntimeStatusPage.scss';

export type RuntimeStatusGateway = {
  identity?: string;
  generation?: number;
  protocolVersion?: string;
  state: string;
  ready: boolean;
  version?: string;
  // The External class of the observed version: what this Manager proved about a
  // CPA it does not own. Embedded leaves both empty; its artifact is the
  // authority, not a version.
  versionSupport?: 'supported' | 'unsupported' | 'unknown' | string;
  writeBlocked?: boolean;
  artifactId?: string;
  capabilities?: string[];
  recovery?: { state: string; attemptsRemaining: number };
};

export type RuntimeStatusResponse = {
  mode: 'embedded' | 'external';
  status: string;
  observedAt?: string;
  manager: { state: string };
  gateway?: RuntimeStatusGateway;
  lastKnown?: RuntimeStatusGateway;
  errorCode?: string;
};

type RuntimeOperation = {
  requestId: string;
  action: 'start' | 'stop' | 'restart';
  operationId: string;
  state: string;
  failureCode?: string;
  updatedAt?: string;
};

const defaultClient = createPublicApiClient();
const POLL_MS = 30_000;

function newRequestId() {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  return `runtime-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

function scheduleTimer(callback: () => void, delayMs: number): number {
  const setTimer =
    typeof window !== 'undefined' && typeof window.setTimeout === 'function'
      ? window.setTimeout.bind(window)
      : globalThis.setTimeout.bind(globalThis);
  return Number(setTimer(callback, delayMs));
}

function parseEngineArtifact(value: unknown): Pick<ArtifactStatus, 'active' | 'required'> | null {
  if (!value || typeof value !== 'object') return null;
  const raw = value as Record<string, unknown>;
  if (raw.schemaVersion !== 1 || typeof raw.required !== 'boolean') return null;
  if (raw.active === null || raw.active === undefined) {
    return raw.required ? { active: null, required: true } : null;
  }
  if (typeof raw.active !== 'object') return null;
  const active = raw.active as Record<string, unknown>;
  if (
    typeof active.artifactId !== 'string' ||
    !/^sha256:[a-f0-9]{64}$/.test(active.artifactId) ||
    (active.origin !== 'bundled' && active.origin !== 'acquired') ||
    raw.required !== false
  ) {
    return null;
  }
  return {
    active: { artifactId: active.artifactId, origin: active.origin },
    required: false,
  };
}

function pillToneForState(state: string | undefined): string {
  switch (state) {
    case 'healthy':
    case 'ready':
    case 'online':
    case 'running':
      return 'is-success';
    case 'degraded':
    case 'starting':
      return 'is-warning';
    case 'offline':
    case 'error':
    case 'failed':
      return 'is-danger';
    default:
      return 'is-neutral';
  }
}

function pillToneForOperation(state: string | undefined): string {
  switch (state) {
    case 'succeeded':
      return 'is-success';
    case 'failed':
      return 'is-danger';
    case 'accepted':
    case 'running':
    case 'unknown':
      return 'is-warning';
    default:
      return 'is-info';
  }
}

function formatGatewayVersion(version: string | undefined): string {
  const trimmed = version?.trim();
  if (!trimmed) return '—';
  return /^v/i.test(trimmed) ? trimmed : `v${trimmed}`;
}

function formatArtifactDigest(artifactId: string | undefined): string {
  const trimmed = artifactId?.trim();
  if (!trimmed) return '';
  if (trimmed.startsWith('sha256:') && trimmed.length > 22) {
    const hex = trimmed.slice('sha256:'.length);
    return `sha256:${hex.slice(0, 8)}…${hex.slice(-5)}`;
  }
  if (trimmed.length > 24) {
    return `${trimmed.slice(0, 14)}…${trimmed.slice(-6)}`;
  }
  return trimmed;
}

export function RuntimeStatusPage({
  client = defaultClient,
  credential,
  onUnauthorized,
}: {
  client?: PublicApiClient;
  credential?: string;
  onUnauthorized?: () => void;
} = {}) {
  const { t, i18n } = useTranslation();
  const auth = useAuthStore();
  const config = useConfigStore((state) => state.config);
  const models = useModelsStore((state) => state.models);
  const modelsLoading = useModelsStore((state) => state.loading);
  const modelsError = useModelsStore((state) => state.error);
  const modelsCache = useModelsStore((state) => state.cache);
  const fetchModelsFromStore = useModelsStore((state) => state.fetchModels);
  const featureAvailability = usePanelFeatureAvailability();
  const { showConfirmation, showNotification } = useNotificationStore();
  const managerCredential = credential || auth.managementKey;

  const stateLabel = (value: string | undefined, fallback: string) => {
    if (!value) return fallback;
    const key = `runtime_status.state_${value}`;
    return i18n.exists(key) ? t(key) : value.replace(/_/g, ' ');
  };
  const operationLabel = (value: string | undefined) => {
    const key = `runtime_status.operation_${value ?? 'unknown'}`;
    if (i18n.exists(key)) return t(key);
    return value ? value.replace(/_/g, ' ') : t('runtime_status.operation_unknown');
  };

  const [status, setStatus] = useState<RuntimeStatusResponse | null>(null);
  const [operation, setOperation] = useState<RuntimeOperation | null>(null);
  const [artifact, setArtifact] = useState<Pick<ArtifactStatus, 'active' | 'required'> | null>(null);
  const [usageStatus, setUsageStatus] = useState<UsageServiceStatus | null>(null);
  const [usageStatusLoading, setUsageStatusLoading] = useState(false);
  const [usageStatusError, setUsageStatusError] = useState('');
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [modelsOpen, setModelsOpen] = useState(false);
  const [modelsSyncing, setModelsSyncing] = useState(false);
  const [modelQuery, setModelQuery] = useState('');
  const [copiedModel, setCopiedModel] = useState<string | null>(null);
  const [copiedArtifact, setCopiedArtifact] = useState(false);
  const [detailsOpen, setDetailsOpen] = useState(false);
  const [copiedDetails, setCopiedDetails] = useState(false);
  const [modelsErrorState, setModelsErrorState] = useState<{
    hidden?: boolean;
    detail?: LocalizedText;
  } | null>(null);

  const requestRef = useRef(0);
  const usageRequestRef = useRef(0);
  const refreshModelsSeqRef = useRef(0);
  const busyRef = useRef(false);
  const copyTimerRef = useRef<number | null>(null);
  const modelCopyTimerRef = useRef<number | null>(null);
  const artifactCopyTimerRef = useRef<number | null>(null);

  useEffect(
    () => () => {
      refreshModelsSeqRef.current += 1;
      if (copyTimerRef.current !== null) {
        globalThis.clearTimeout(copyTimerRef.current);
        copyTimerRef.current = null;
      }
      if (modelCopyTimerRef.current !== null) {
        globalThis.clearTimeout(modelCopyTimerRef.current);
        modelCopyTimerRef.current = null;
      }
      if (artifactCopyTimerRef.current !== null) {
        globalThis.clearTimeout(artifactCopyTimerRef.current);
        artifactCopyTimerRef.current = null;
      }
    },
    []
  );

  const otherLabel = useMemo(() => t('system_info.models_group_unclassified'), [t]);
  const groupedModels = useMemo(() => classifyModels(models, { otherLabel }), [models, otherLabel]);

  const normalizedModelQuery = modelQuery.trim().toLowerCase();
  const filteredModelGroups = useMemo(() => {
    if (!normalizedModelQuery) return groupedModels;
    return groupedModels
      .map((group) => {
        const groupMatches = group.label.toLowerCase().includes(normalizedModelQuery);
        const items = groupMatches
          ? group.items
          : group.items.filter(
              (model) =>
                model.name.toLowerCase().includes(normalizedModelQuery) ||
                (model.alias && model.alias.toLowerCase().includes(normalizedModelQuery))
            );
        return { ...group, items };
      })
      .filter((group) => group.items.length > 0);
  }, [groupedModels, normalizedModelQuery]);

  const filteredModelsCount = useMemo(
    () => filteredModelGroups.reduce((sum, group) => sum + group.items.length, 0),
    [filteredModelGroups]
  );

  const configApiKeysKey = useMemo(
    () =>
      JSON.stringify(
        Array.isArray(config?.apiKeys)
          ? config.apiKeys.map((k) => String(k ?? '').trim()).filter(Boolean)
          : []
      ),
    [config?.apiKeys]
  );

  const refreshUsageStatus = useCallback(async () => {
    if (
      !featureAvailability.managerServiceAvailable ||
      !featureAvailability.managerServiceBase ||
      !managerCredential
    ) {
      return;
    }
    const reqId = ++usageRequestRef.current;
    setUsageStatusLoading(true);
    setUsageStatusError('');
    try {
      const next = await usageServiceApi.getStatus(
        featureAvailability.managerServiceBase,
        managerCredential
      );
      if (reqId === usageRequestRef.current) {
        setUsageStatus(next);
      }
    } catch (err) {
      if (reqId === usageRequestRef.current) {
        setUsageStatusError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      if (reqId === usageRequestRef.current) {
        setUsageStatusLoading(false);
      }
    }
  }, [
    featureAvailability.managerServiceAvailable,
    featureAvailability.managerServiceBase,
    managerCredential,
  ]);

  const refreshModels = useCallback(
    async (forceRefresh = false) => {
      if (auth.connectionStatus !== 'connected' || !auth.apiBase) {
        setModelsErrorState(null);
        return;
      }
      const seq = ++refreshModelsSeqRef.current;
      try {
        const modelManagerCredential =
          auth.sessionMode === 'manager_embedded' && /\/api\/v2\/cpamp$/i.test(auth.apiBase)
            ? auth.managementKey || credential
            : undefined;
        let apiKeys: string[] = (JSON.parse(configApiKeysKey) as string[]).filter(
          (item) => typeof item === 'string' && !item.includes('…') && !item.includes('***')
        );
        if (!apiKeys.length && !modelManagerCredential) {
          try {
            const list = await apiKeysApi.list();
            if (Array.isArray(list)) {
              apiKeys = list
                .map((item) =>
                  typeof item === 'string'
                    ? item.trim()
                    : String((item as Record<string, unknown>)?.['api-key'] ?? '').trim()
                )
                .filter(
                  (item) => Boolean(item) && !item.includes('…') && !item.includes('***')
                );
            }
          } catch {
            apiKeys = [];
          }
        }
        if (seq !== refreshModelsSeqRef.current) return;
        const firstKey = apiKeys[0];
        if (!firstKey && !modelManagerCredential) {
          setModelsErrorState(null);
          return;
        }
        setModelsErrorState(null);
        await fetchModelsFromStore(
          auth.apiBase,
          firstKey || undefined,
          forceRefresh,
          modelManagerCredential
        );
        if (seq !== refreshModelsSeqRef.current) return;
        setModelsErrorState(null);
      } catch (err: unknown) {
        if (seq !== refreshModelsSeqRef.current) return;
        const code = getUsageServiceErrorCode(err);
        if (
          auth.sessionMode === 'manager_embedded' &&
          !useModelsStore.getState().cache &&
          (code === 'invalid_admin_key' || code === 'unauthorized')
        ) {
          setModelsErrorState({ hidden: true });
          return;
        }
        const detail: LocalizedText | undefined = isGatewayPendingError(err)
          ? { key: 'system_info.models_gateway_pending' }
          : code
            ? {
                key: `usage_service_errors.${code}`,
                fallbackKey: 'usage_service_errors.request_failed',
              }
            : err instanceof Error
              ? { text: err.message }
              : typeof err === 'string'
                ? { text: err }
                : undefined;
        setModelsErrorState({ detail });
      }
    },
    [
      auth.apiBase,
      auth.connectionStatus,
      auth.managementKey,
      auth.sessionMode,
      configApiKeysKey,
      credential,
      fetchModelsFromStore,
    ]
  );

  const syncUpstreamModels = useCallback(async () => {
    if (auth.connectionStatus !== 'connected' || !auth.apiBase || !managerCredential || modelsSyncing) {
      return;
    }
    setModelsSyncing(true);
    try {
      await modelsApi.refreshUpstreamModels(auth.apiBase, managerCredential);
      await refreshModels(true);
      setModelsOpen(true);
      showNotification(
        t('system_info.models_sync_upstream_success', {
          count: useModelsStore.getState().models.length,
        }),
        'success'
      );
    } catch (err: unknown) {
      const status =
        err && typeof err === 'object'
          ? ((err as { status?: number; response?: { status?: number } }).response?.status ??
            (err as { status?: number }).status)
          : undefined;
      if (status === 404 || status === 405) {
        showNotification(t('system_info.models_sync_upstream_unsupported'), 'warning');
      } else if (isGatewayPendingError(err) || status === 503) {
        showNotification(t('system_info.models_gateway_pending'), 'warning');
      } else {
        const code = getUsageServiceErrorCode(err);
        const detail = code
          ? formatLocalizedText(
              {
                key: `usage_service_errors.${code}`,
                fallbackKey: 'usage_service_errors.request_failed',
              },
              t
            )
          : err instanceof Error
            ? err.message
            : String(err);
        showNotification(
          t('system_info.models_sync_upstream_failed', { error: detail }),
          'error'
        );
      }
    } finally {
      setModelsSyncing(false);
    }
  }, [
    auth.apiBase,
    auth.connectionStatus,
    managerCredential,
    modelsSyncing,
    refreshModels,
    showNotification,
    t,
  ]);
  const refresh = useCallback(async () => {
    if (!managerCredential) {
      setLoading(false);
      return;
    }
    const requestId = ++requestRef.current;
    const adminCredential: FullWebCredential = {
      kind: 'admin',
      value: managerCredential,
    };
    const artifactPromise = client
      .get<unknown>('runtime/artifact', adminCredential)
      .then((raw) => parseEngineArtifact(raw))
      .catch(() => null);
    void refreshUsageStatus();
    try {
      const [next, nextArtifact] = await Promise.all([
        client.get<RuntimeStatusResponse>('runtime/status', adminCredential),
        artifactPromise,
      ]);
      if (requestId === requestRef.current) {
        setStatus(next);
        setArtifact(nextArtifact);
      }
    } catch (error) {
      const nextArtifact = await artifactPromise;
      if (error instanceof PublicApiError && error.kind === 'authentication') {
        onUnauthorized?.();
      } else if (requestId === requestRef.current) {
        setArtifact(nextArtifact);
        setStatus((current) =>
          current
            ? { ...current, status: 'unknown', errorCode: 'manager_unreachable' }
            : {
                mode: 'embedded',
                status: 'unknown',
                manager: { state: 'unknown' },
                errorCode: 'manager_unreachable',
              }
        );
      }
    } finally {
      if (requestId === requestRef.current) setLoading(false);
    }
  }, [client, managerCredential, onUnauthorized, refreshUsageStatus]);

  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') void refresh();
    }, POLL_MS);
    return () => window.clearInterval(timer);
  }, [refresh]);

  useEffect(() => {
    void refreshModels(false);
  }, [refreshModels]);

  const handleHeaderRefresh = useCallback(async () => {
    await Promise.all([refresh(), refreshModels(true)]);
  }, [refresh, refreshModels]);

  useHeaderRefresh(handleHeaderRefresh, !busy);

  const queryOperation = useCallback(
    async (requestId: string) => {
      try {
        const next = await client.get<RuntimeOperation>(`runtime/operations/${requestId}`, {
          kind: 'admin',
          value: managerCredential,
        });
        setOperation(next);
        return next;
      } catch (error) {
        if (error instanceof PublicApiError && error.kind === 'authentication') onUnauthorized?.();
        return null;
      }
    },
    [client, managerCredential, onUnauthorized]
  );

  const submit = useCallback(
    async (action: RuntimeOperation['action']) => {
      if (busyRef.current || !managerCredential || status?.mode !== 'embedded') return;
      busyRef.current = true;
      setBusy(true);
      const requestId = newRequestId();
      try {
        const next = await client.post<RuntimeOperation>(
          'runtime/operations',
          { action, requestId },
          { kind: 'admin', value: managerCredential } satisfies FullWebCredential
        );
        setOperation(next);
        if (next.state === 'unknown') await queryOperation(requestId);
        await refresh();
      } catch (error) {
        if (error instanceof PublicApiError && error.kind === 'authentication') {
          onUnauthorized?.();
        } else if (error instanceof PublicApiError && error.status === 409) {
          showNotification(
            t('runtime_status.operation_conflict', {
              defaultValue: 'Another runtime operation is already in progress.',
            }),
            'warning'
          );
        } else {
          showNotification(
            t('runtime_status.operation_failed', {
              defaultValue: 'Runtime operation could not be completed.',
            }),
            'error'
          );
        }
      } finally {
        busyRef.current = false;
        setBusy(false);
      }
    },
    [
      client,
      managerCredential,
      onUnauthorized,
      queryOperation,
      refresh,
      showNotification,
      status?.mode,
      t,
    ]
  );

  const requestAction = (action: RuntimeOperation['action']) => {
    if (busyRef.current) return;
    if (action === 'stop' || action === 'restart') {
      showConfirmation({
        title:
          action === 'stop'
            ? t('runtime_status.stop_title', { defaultValue: 'Stop CPA gateway?' })
            : t('runtime_status.restart_title', { defaultValue: 'Restart CPA gateway?' }),
        message: t('runtime_status.interruption', {
          defaultValue: 'Gateway requests may be interrupted. CPAMP Manager remains available.',
        }),
        confirmText:
          action === 'stop'
            ? t('runtime_status.stop', { defaultValue: 'Stop' })
            : t('runtime_status.restart', { defaultValue: 'Restart' }),
        variant: 'danger',
        onConfirm: () => submit(action),
      });
      return;
    }
    void submit(action);
  };

  const gateway = status?.gateway || status?.lastKnown;
  const anomaly = useMemo(() => {
    if (!status) return null;
    if (status.status === 'unknown') {
      return {
        tone: 'is-danger',
        happened: t('runtime_status.unknown_happened', {
          defaultValue: 'The latest gateway observation is unavailable.',
        }),
        impact: t('runtime_status.unknown_impact', {
          defaultValue: 'The current gateway state cannot be confirmed.',
        }),
        next: t('runtime_status.unknown_next', {
          defaultValue: 'Refresh or inspect the system logs.',
        }),
      };
    }
    if (gateway?.recovery?.state === 'manual_intervention') {
      return {
        tone: 'is-danger',
        happened: t('runtime_status.manual_happened', {
          defaultValue: 'Automatic recovery requires attention.',
        }),
        impact: t('runtime_status.manual_impact', {
          defaultValue: 'Gateway readiness may be unavailable.',
        }),
        next: t('runtime_status.manual_next', {
          defaultValue: 'Review logs before taking another lifecycle action.',
        }),
      };
    }
    if (gateway && !gateway.ready && gateway.state !== 'offline') {
      return {
        tone: 'is-warning',
        happened: t('runtime_status.not_ready_happened', {
          defaultValue: 'The gateway process is running but not ready.',
        }),
        impact: t('runtime_status.not_ready_impact', {
          defaultValue: 'Gateway requests may fail until readiness returns.',
        }),
        next: t('runtime_status.not_ready_next', {
          defaultValue: 'Wait for readiness or restart if the capability is available.',
        }),
      };
    }
    return null;
  }, [gateway, status, t]);

  const collector = usageStatus?.collector;
  const migration = usageStatus?.dataMigration;
  const deadLettersCount = collector?.deadLetters ?? usageStatus?.deadLetters ?? 0;
  const collectorModeValue = collector?.mode || collector?.collector || '';
  const collectorActive = Boolean(collector?.collector && collector.collector !== 'stopped');
  const collectorTransport = collector?.transport || collector?.collector || '';
  const isRespTransport =
    collectorTransport.toLowerCase().includes('resp') ||
    collectorTransport.toLowerCase().includes('redis');
  const collectorHealthState = !collectorActive
    ? {
        label: t('dashboard.health_status_disabled', { defaultValue: 'Disabled' }),
        tone: 'is-neutral',
      }
    : collector?.lastError || deadLettersCount > 0
      ? {
          label: t('dashboard.health_status_warning', { defaultValue: 'Warning' }),
          tone: 'is-warning',
        }
      : {
          label: t('dashboard.health_status_normal', { defaultValue: 'Normal' }),
          tone: 'is-success',
        };

  const formatCollectorTime = (timestampMs?: number) => {
    if (!timestampMs || !Number.isFinite(timestampMs)) return '-';
    return new Date(timestampMs).toLocaleTimeString(i18n.language, {
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    });
  };

  const diagnosticSnapshot = useMemo(
    () =>
      JSON.stringify(
        {
          mode: status?.mode,
          status: status?.status,
          manager: status?.manager,
          gateway,
          observedAt: status?.observedAt,
          operation,
        },
        null,
        2
      ),
    [gateway, operation, status?.manager, status?.mode, status?.observedAt, status?.status]
  );

  const detailsMetaItems = useMemo(() => {
    const items: Array<{ label: string; value: string; mono?: boolean }> = [];
    if (gateway?.identity) {
      items.push({
        label: t('runtime_status.detail_identity', { defaultValue: 'Identity' }),
        value: gateway.identity,
        mono: true,
      });
    }
    if (gateway?.protocolVersion) {
      items.push({
        label: t('runtime_status.detail_protocol', { defaultValue: 'Protocol' }),
        value: gateway.protocolVersion,
        mono: true,
      });
    }
    if (gateway?.generation != null) {
      items.push({
        label: t('runtime_status.detail_generation', { defaultValue: 'Generation' }),
        value: String(gateway.generation),
        mono: true,
      });
    }
    if (Array.isArray(gateway?.capabilities) && gateway.capabilities.length > 0) {
      items.push({
        label: t('runtime_status.detail_capabilities', { defaultValue: 'Capabilities' }),
        value: String(gateway.capabilities.length),
        mono: true,
      });
    }
    return items;
  }, [gateway?.capabilities, gateway?.generation, gateway?.identity, gateway?.protocolVersion, t]);

  const handleCopySnapshot = async (event: React.MouseEvent) => {
    event.preventDefault();
    event.stopPropagation();
    const copied = await copyToClipboard(diagnosticSnapshot);
    if (!copied) return;
    setCopiedDetails(true);
    if (copyTimerRef.current !== null) {
      globalThis.clearTimeout(copyTimerRef.current);
    }
    copyTimerRef.current = scheduleTimer(() => {
      copyTimerRef.current = null;
      setCopiedDetails(false);
    }, 1800);
  };

  const handleCopyArtifact = async () => {
    if (!gateway?.artifactId) return;
    const copied = await copyToClipboard(gateway.artifactId);
    if (!copied) return;
    setCopiedArtifact(true);
    if (artifactCopyTimerRef.current !== null) {
      globalThis.clearTimeout(artifactCopyTimerRef.current);
    }
    artifactCopyTimerRef.current = scheduleTimer(() => {
      artifactCopyTimerRef.current = null;
      setCopiedArtifact(false);
    }, 1800);
  };

  const handleCopyModel = async (modelName: string) => {
    const copied = await copyToClipboard(modelName);
    if (!copied) return;
    setCopiedModel(modelName);
    showNotification(
      t('runtime_status.model_copied', {
        defaultValue: 'Copied {{model}}',
        model: modelName,
      }),
      'success'
    );
    if (modelCopyTimerRef.current !== null) {
      globalThis.clearTimeout(modelCopyTimerRef.current);
    }
    modelCopyTimerRef.current = scheduleTimer(() => {
      modelCopyTimerRef.current = null;
      setCopiedModel((current) => (current === modelName ? null : current));
    }, 1600);
  };

  const observedLabel = status?.observedAt
    ? t('runtime_status.observed_at', {
        defaultValue: 'Observed {{time}}',
        time: new Date(status.observedAt).toLocaleString(i18n.language),
      })
    : t('runtime_status.no_observation', { defaultValue: 'No observation yet' });

  const recoveryDisplay = useMemo(() => {
    const recovery = gateway?.recovery;
    if (!recovery) {
      return { label: '—', tone: '' };
    }
    if (recovery.state === 'manual_intervention') {
      return {
        label: t('runtime_status.recovery_manual', { defaultValue: 'Manual intervention' }),
        tone: 'is-danger',
      };
    }
    if (recovery.state === 'armed') {
      return {
        label: t('runtime_status.recovery_armed', {
          defaultValue: 'Armed · {{count}} left',
          count: recovery.attemptsRemaining,
        }),
        tone: recovery.attemptsRemaining > 0 ? 'is-success' : 'is-warning',
      };
    }
    return {
      label: recovery.state.replace(/_/g, ' '),
      tone: '',
    };
  }, [gateway?.recovery, t]);

  const resolvedModelsError = modelsErrorState?.hidden
    ? null
    : modelsErrorState?.detail
      ? formatLocalizedText(modelsErrorState.detail, t)
      : modelsError;

  const engineSourceValue =
    artifact?.active?.origin === 'bundled'
      ? t('runtime_status.engine_source_bundled')
      : artifact?.active?.origin === 'acquired'
        ? t('runtime_status.engine_source_acquired')
        : t('runtime_status.engine_source_required');

  const modeBadgeLabel =
    status?.mode === 'external'
      ? t('runtime_status.mode_external', { defaultValue: 'External' })
      : t('runtime_status.mode_embedded', { defaultValue: 'Embedded' });

  const gatewayArtifactShort = formatArtifactDigest(gateway?.artifactId);

  return (
    <section className="runtime-status-page" data-testid="runtime-status-page">
      <PageControlBar
        as="header"
        className="runtime-status-bar"
        leading={
          <div className="runtime-status-bar-main" aria-live="polite">
            <span className={`status-pill ${pillToneForState(status?.status)}`}>
              <span className="status-dot" aria-hidden="true" />
              <strong data-testid="runtime-status-state">
                {stateLabel(
                  status?.status,
                  t('runtime_status.loading', { defaultValue: 'Loading' })
                )}
              </strong>
            </span>
            {status && <span className="runtime-mode-chip">{modeBadgeLabel}</span>}
            <p className="runtime-status-subtitle">
              {status?.mode === 'external'
                ? t('runtime_status.external_subtitle', {
                    defaultValue: 'External CPA connection · read-only',
                  })
                : t('runtime_status.subtitle', {
                    defaultValue: 'Observe the Manager and CPA gateway lifecycle separately.',
                  })}
            </p>
          </div>
        }
        actions={
          <div className="runtime-status-bar-actions">
            <span
              className="runtime-observed-meta"
              title={t('runtime_status.auto_refresh_hint', { defaultValue: 'Auto-refresh 30s' })}
            >
              <IconClock size={13} aria-hidden="true" />
              <span>{observedLabel}</span>
            </span>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => void refresh()}
              disabled={loading || busy}
            >
              <span className="runtime-btn-content">
                <IconRefreshCw
                  size={15}
                  className={loading ? 'runtime-spin' : undefined}
                  aria-hidden="true"
                />
                <span>
                  {loading
                    ? t('common.loading', { defaultValue: 'Loading…' })
                    : t('common.refresh', { defaultValue: 'Refresh' })}
                </span>
              </span>
            </Button>
          </div>
        }
      />

      {anomaly && (
        <div
          className={`card runtime-status-anomaly ${anomaly.tone}`}
          data-testid="runtime-status-anomaly"
        >
          <div className="runtime-anomaly-header">
            <div className="runtime-anomaly-title">
              <IconTriangleAlert size={18} aria-hidden="true" />
              <h2>{t('runtime_status.anomaly', { defaultValue: 'Attention needed' })}</h2>
            </div>
            <Link to="/logs" className="runtime-inline-link">
              <span>{t('nav.logs', { defaultValue: 'Logs' })}</span>
              <IconChevronRight size={14} aria-hidden="true" />
            </Link>
          </div>
          <dl className="runtime-anomaly-grid">
            <div className="runtime-anomaly-item">
              <dt>{t('runtime_status.what_happened', { defaultValue: 'What happened' })}</dt>
              <dd>{anomaly.happened}</dd>
            </div>
            <div className="runtime-anomaly-item">
              <dt>{t('runtime_status.impact', { defaultValue: 'Impact' })}</dt>
              <dd>{anomaly.impact}</dd>
            </div>
            <div className="runtime-anomaly-item">
              <dt>{t('runtime_status.next_step', { defaultValue: 'Next step' })}</dt>
              <dd>{anomaly.next}</dd>
            </div>
          </dl>
        </div>
      )}

      {operation && (
        <div
          className={`runtime-status-operation ${pillToneForOperation(operation.state)}`}
          role="status"
        >
          <span className="status-dot" aria-hidden="true" />
          <span className="runtime-operation-text" data-testid="runtime-status-operation">
            {t('runtime_status.operation_status', {
              defaultValue: 'Operation {{action}}: {{state}}',
              action:
                operation.action === 'start'
                  ? t('runtime_status.start', { defaultValue: 'Start' })
                  : operation.action === 'stop'
                    ? t('runtime_status.stop', { defaultValue: 'Stop' })
                    : t('runtime_status.restart', { defaultValue: 'Restart' }),
              state: operationLabel(operation.state),
            })}
            {operation.state === 'unknown' &&
              ` · ${t('runtime_status.query_original', {
                defaultValue: 'Result pending confirmation; the original operation was not resent.',
              })}`}
          </span>
        </div>
      )}

      <section className="runtime-status-cards">
        <Card className="runtime-status-card">
          <div className="runtime-card-head">
            <div className="runtime-card-heading">
              <span className="runtime-card-icon is-accent" aria-hidden="true">
                <IconShieldCheck size={18} />
              </span>
              <div className="runtime-card-titles">
                <h2>{t('runtime_status.manager', { defaultValue: 'CPAMP Manager' })}</h2>
                <small>
                  {t('runtime_status.manager_fact', {
                    defaultValue:
                      'Manager authentication and API routing are independent from gateway readiness.',
                  })}
                </small>
              </div>
            </div>
            <span className={`status-pill ${pillToneForState(status?.manager.state)}`}>
              <span className="status-dot" aria-hidden="true" />
              <span data-testid="runtime-status-manager-state">
                {stateLabel(
                  status?.manager.state,
                  t('runtime_status.unknown', { defaultValue: 'Unknown' })
                )}
              </span>
            </span>
          </div>

          <dl className="runtime-metric-grid is-cols-3">
            <div className="runtime-metric-tile">
              <dt>{t('runtime_status.mode_label', { defaultValue: 'Connection mode' })}</dt>
              <dd title={modeBadgeLabel}>{modeBadgeLabel}</dd>
            </div>
            <div className="runtime-metric-tile">
              <dt>
                {t('runtime_status.migration_title', {
                  defaultValue: 'Background Data Migration',
                })}
              </dt>
              {(() => {
                const migrationLabel = !migration
                  ? '—'
                  : migration.status === 'completed'
                    ? t('runtime_status.migration_done', { defaultValue: 'Completed' })
                    : t('runtime_status.migration_running', {
                        defaultValue: 'Running ({{done}}/{{total}})',
                        done: migration.processedRows.toLocaleString(i18n.language),
                        total: Math.max(
                          migration.targetEventId,
                          migration.processedRows
                        ).toLocaleString(i18n.language),
                      });
                return (
                  <dd
                    className={
                      migration?.status === 'completed'
                        ? 'is-success'
                        : migration
                          ? 'is-warning'
                          : undefined
                    }
                    title={migrationLabel}
                  >
                    {migrationLabel}
                  </dd>
                );
              })()}
            </div>
            <div className="runtime-metric-tile">
              <dt>{t('runtime_status.sqlite_usage_label', { defaultValue: 'SQLite size' })}</dt>
              {(() => {
                const sqliteSizeLabel =
                  usageStatus?.database?.totalBytes != null
                    ? formatFileSize(usageStatus.database.totalBytes)
                    : '—';
                return (
                  <dd className="is-tabular" title={sqliteSizeLabel}>
                    {sqliteSizeLabel}
                  </dd>
                );
              })()}
            </div>
          </dl>

          <footer className="runtime-card-footer">
            <nav className="runtime-status-links">
              <Link to="/system" className="runtime-inline-link">
                <span>{t('nav.system_config', { defaultValue: 'System Configuration' })}</span>
                <IconChevronRight size={14} aria-hidden="true" />
              </Link>
              <Link to="/monitoring" className="runtime-inline-link">
                <span>{t('nav.monitoring_center', { defaultValue: 'Request Monitor' })}</span>
                <IconChevronRight size={14} aria-hidden="true" />
              </Link>
              <Link to="/logs" className="runtime-inline-link">
                <span>{t('nav.logs', { defaultValue: 'Logs' })}</span>
                <IconChevronRight size={14} aria-hidden="true" />
              </Link>
            </nav>
          </footer>
        </Card>

        <Card className="runtime-status-card">
          <div className="runtime-card-head">
            <div className="runtime-card-heading">
              <span className="runtime-card-icon is-accent" aria-hidden="true">
                <IconSatellite size={18} />
              </span>
              <div className="runtime-card-titles">
                <h2>{t('runtime_status.gateway', { defaultValue: 'CPA gateway' })}</h2>
                <small>
                  {gateway?.ready
                    ? t('runtime_status.ready', { defaultValue: 'Ready for gateway requests' })
                    : t('runtime_status.not_ready', {
                        defaultValue: 'Not ready for gateway requests',
                      })}
                </small>
              </div>
            </div>
            <span className={`status-pill ${pillToneForState(gateway?.state)}`}>
              <span className="status-dot" aria-hidden="true" />
              <span data-testid="runtime-status-gateway-state">
                {stateLabel(
                  gateway?.state,
                  t('runtime_status.unknown', { defaultValue: 'Unknown' })
                )}
              </span>
            </span>
          </div>

          {status?.mode === 'external' ? (
            <dl
              className="runtime-metric-grid is-cols-3"
              data-testid="runtime-status-cpa-version"
            >
              <div className="runtime-metric-tile">
                <dt>{t('runtime_status.cpa_version', { defaultValue: 'CPA version' })}</dt>
                <dd
                  className="is-mono"
                  title={gateway?.version || t('runtime_status.unknown', { defaultValue: 'Unknown' })}
                >
                  {gateway?.version || t('runtime_status.unknown', { defaultValue: 'Unknown' })}
                </dd>
              </div>
              <div className="runtime-metric-tile">
                <dt>
                  {t('runtime_status.version_support', { defaultValue: 'Version support' })}
                </dt>
                <dd
                  className={
                    gateway?.versionSupport === 'supported'
                      ? 'is-success'
                      : gateway?.versionSupport === 'unsupported'
                        ? 'is-danger'
                        : 'is-warning'
                  }
                  title={
                    gateway?.versionSupport === 'supported'
                      ? t('runtime_status.version_supported', { defaultValue: 'Supported' })
                      : gateway?.versionSupport === 'unsupported'
                        ? t('runtime_status.version_unsupported', { defaultValue: 'Unsupported' })
                        : t('runtime_status.version_unverified', { defaultValue: 'Unverified' })
                  }
                >
                  {gateway?.versionSupport === 'supported'
                    ? t('runtime_status.version_supported', { defaultValue: 'Supported' })
                    : gateway?.versionSupport === 'unsupported'
                      ? t('runtime_status.version_unsupported', { defaultValue: 'Unsupported' })
                      : t('runtime_status.version_unverified', { defaultValue: 'Unverified' })}
                </dd>
              </div>
              <div className="runtime-metric-tile">
                <dt>{t('runtime_status.writes', { defaultValue: 'Writes' })}</dt>
                <dd
                  className={gateway?.writeBlocked ? 'is-warning' : 'is-success'}
                  title={
                    gateway?.writeBlocked
                      ? t('runtime_status.writes_blocked', {
                          defaultValue: 'Blocked by this version',
                        })
                      : t('runtime_status.writes_allowed', {
                          defaultValue: 'Not blocked by this version',
                        })
                  }
                >
                  {gateway?.writeBlocked
                    ? t('runtime_status.writes_blocked', {
                        defaultValue: 'Blocked by this version',
                      })
                    : t('runtime_status.writes_allowed', {
                        defaultValue: 'Not blocked by this version',
                      })}
                </dd>
              </div>
            </dl>
          ) : (
            <dl className={`runtime-metric-grid ${artifact ? 'is-cols-3' : 'is-cols-2'}`}>
              <div className="runtime-metric-tile">
                <dt>
                  {t('runtime_status.gateway_version', { defaultValue: 'Gateway version' })}
                </dt>
                <dd
                  className="is-mono"
                  data-testid="runtime-status-gateway-version"
                  title={formatGatewayVersion(gateway?.version)}
                >
                  {formatGatewayVersion(gateway?.version)}
                </dd>
              </div>
              {artifact && (
                <div className="runtime-metric-tile" data-testid="runtime-status-engine-source">
                  <dt>{t('runtime_status.engine_source')}</dt>
                  <dd data-testid="runtime-status-engine-source-value" title={engineSourceValue}>
                    {engineSourceValue}
                  </dd>
                </div>
              )}
              <div className="runtime-metric-tile">
                <dt>{t('runtime_status.recovery_label', { defaultValue: 'Auto recovery' })}</dt>
                <dd
                  className={recoveryDisplay.tone}
                  data-testid="runtime-status-recovery"
                  title={recoveryDisplay.label}
                >
                  {recoveryDisplay.label}
                </dd>
              </div>
            </dl>
          )}

          {status?.mode === 'external' ? (
            <footer className="runtime-card-footer">
              <nav
                className="runtime-status-links"
                aria-label={t('runtime_status.external_links', {
                  defaultValue: 'External runtime links',
                })}
              >
                <Link to="/config" className="runtime-inline-link">
                  <span>
                    {t('nav.gateway_management', { defaultValue: 'Gateway Management' })}
                  </span>
                  <IconChevronRight size={14} aria-hidden="true" />
                </Link>
                <Link to="/logs" className="runtime-inline-link">
                  <span>{t('nav.logs', { defaultValue: 'Logs' })}</span>
                  <IconChevronRight size={14} aria-hidden="true" />
                </Link>
              </nav>
            </footer>
          ) : (
            <SectionActionFooter
              className="runtime-gateway-action-footer"
              status={
                gateway?.artifactId ? (
                  <button
                    type="button"
                    className={`runtime-artifact-chip ${copiedArtifact ? 'is-copied' : ''}`}
                    onClick={() => void handleCopyArtifact()}
                    title={
                      copiedArtifact
                        ? t('runtime_status.copied_artifact', {
                            defaultValue: 'Artifact ID copied',
                          })
                        : `${t('runtime_status.copy_artifact', {
                            defaultValue: 'Click to copy full artifact ID',
                          })}: ${gateway.artifactId}`
                    }
                    data-testid="runtime-artifact-digest-btn"
                  >
                    <span className="runtime-artifact-value">{gatewayArtifactShort}</span>
                    {copiedArtifact ? (
                      <IconCheck size={12} aria-hidden="true" />
                    ) : (
                      <IconCopy size={12} aria-hidden="true" />
                    )}
                  </button>
                ) : null
              }
            >
              {status?.mode === 'embedded' && gateway ? (
                <>
                  {gateway.state === 'offline' ? (
                    <Button
                      variant="primary"
                      size="sm"
                      onClick={() => requestAction('start')}
                      disabled={busy || !gateway.capabilities?.includes('start')}
                      data-testid="runtime-status-start"
                    >
                      {t('runtime_status.start', { defaultValue: 'Start' })}
                    </Button>
                  ) : (
                    <Button
                      variant="secondary"
                      size="sm"
                      onClick={() => requestAction('restart')}
                      disabled={busy || !gateway.capabilities?.includes('restart')}
                      data-testid="runtime-status-restart"
                    >
                      <span className="runtime-btn-content">
                        <IconRotateCcw size={13} aria-hidden="true" />
                        <span>{t('runtime_status.restart', { defaultValue: 'Restart' })}</span>
                      </span>
                    </Button>
                  )}
                  <Button
                    variant="secondary"
                    size="sm"
                    className="runtime-stop-btn"
                    onClick={() => requestAction('stop')}
                    disabled={busy || !gateway.capabilities?.includes('stop')}
                    data-testid="runtime-status-stop"
                  >
                    {t('runtime_status.stop', { defaultValue: 'Stop' })}
                  </Button>
                </>
              ) : null}
            </SectionActionFooter>
          )}
        </Card>
      </section>

      {featureAvailability.managerServiceAvailable && (
        <section className="runtime-observability-grid">
          <Card className="runtime-status-card runtime-collector-card">
            <div className="runtime-card-head">
              <div className="runtime-card-heading">
                <span className="runtime-card-icon is-accent" aria-hidden="true">
                  <IconChartLine size={18} />
                </span>
                <div className="runtime-card-titles">
                  <h2>
                    {t('runtime_status.collector_title', {
                      defaultValue: 'Usage Collector Stream',
                    })}
                  </h2>
                  <small>
                    {t('runtime_status.collector_subtitle', {
                      defaultValue: 'Consumes upstream usage events into SQLite',
                    })}
                  </small>
                </div>
              </div>
              <span className={`status-pill ${collectorHealthState.tone}`}>
                <span className="status-dot" aria-hidden="true" />
                <span>{collectorHealthState.label}</span>
              </span>
            </div>

            <div className="runtime-collector-body" data-testid="runtime-collector-card">
              <div className="runtime-collector-hero">
                <div className="runtime-collector-hero-total">
                  <span>
                    {t('runtime_status.collector_events', { defaultValue: 'Stored events' })}
                  </span>
                  <strong>{(usageStatus?.events ?? 0).toLocaleString(i18n.language)}</strong>
                </div>
                <div className="runtime-collector-heartbeats">
                  <div className="runtime-collector-heartbeat">
                    <span>
                      {t('dashboard.collector_last_consumed', { defaultValue: 'Last read' })}
                    </span>
                    <strong>{formatCollectorTime(collector?.lastConsumedAt)}</strong>
                  </div>
                  <div className="runtime-collector-heartbeat">
                    <span>
                      {t('dashboard.collector_last_inserted', { defaultValue: 'Last write' })}
                    </span>
                    <strong>{formatCollectorTime(collector?.lastInsertedAt)}</strong>
                  </div>
                </div>
              </div>

              <div className="runtime-collector-counters">
                <div className="runtime-collector-counter">
                  <span>
                    {t('runtime_status.collector_inserted', { defaultValue: 'Inserted' })}
                  </span>
                  <strong>{(collector?.totalInserted ?? 0).toLocaleString(i18n.language)}</strong>
                </div>
                <div className="runtime-collector-counter">
                  <span>{t('runtime_status.collector_skipped', { defaultValue: 'Skipped' })}</span>
                  <strong>{(collector?.totalSkipped ?? 0).toLocaleString(i18n.language)}</strong>
                </div>
                <div
                  className={`runtime-collector-counter ${deadLettersCount > 0 ? 'is-alert' : ''}`}
                >
                  <span>
                    {t('runtime_status.collector_dead_letters', { defaultValue: 'Dead letters' })}
                  </span>
                  <strong>{deadLettersCount.toLocaleString(i18n.language)}</strong>
                </div>
              </div>
            </div>

            {collector?.lastError ? (
              <div className="runtime-error-note">
                {(() => {
                  const hintKey = resolveCollectorErrorHintKey(collector.lastError);
                  if (!hintKey) {
                    return collector.lastError;
                  }
                  return (
                    <>
                      <span className="runtime-error-note-hint">{t(hintKey)}</span>
                      <code className="runtime-error-note-raw">{collector.lastError}</code>
                    </>
                  );
                })()}
              </div>
            ) : null}

            <footer className="runtime-card-footer">
              <div className="runtime-pipeline-flow">
                <span
                  className="runtime-pipeline-node is-source"
                  title={`${
                    isRespTransport
                      ? t('runtime_status.collector_queue', { defaultValue: 'Queue' })
                      : t('runtime_status.collector_upstream', { defaultValue: 'Upstream' })
                  }: ${(isRespTransport ? collector?.queue : collector?.upstream) || '-'}`}
                >
                  {(isRespTransport ? collector?.queue : collector?.upstream) || '-'}
                </span>
                <IconChevronRight
                  size={14}
                  className="runtime-pipeline-arrow"
                  aria-hidden="true"
                />
                <span
                  className="runtime-pipeline-node is-transport"
                  title={`${t('runtime_status.collector_transport_label', {
                    defaultValue: 'Collector transport',
                  })}: ${collectorTransport || '-'} · ${t('dashboard.collector_mode', {
                    defaultValue: 'Mode',
                  })}: ${collectorModeValue || '-'}`}
                >
                  {[collectorTransport, collectorModeValue]
                    .filter(Boolean)
                    .filter((item, idx, arr) => arr.indexOf(item) === idx)
                    .join(' · ') || '-'}
                </span>
                <IconChevronRight
                  size={14}
                  className="runtime-pipeline-arrow"
                  aria-hidden="true"
                />
                <span className="runtime-pipeline-node is-sink">SQLite</span>
              </div>
            </footer>
          </Card>
          <div className="runtime-database-stack" data-testid="runtime-database-card">
            {migration && migration.status !== 'completed' ? (
              <Card
                title={t('runtime_status.migration_title', {
                  defaultValue: 'Background Data Migration',
                })}
                extra={
                  <span className="status-pill is-warning">
                    <span className="status-dot" aria-hidden="true" />
                    <span>{migration.status}</span>
                  </span>
                }
              >
                <p className="runtime-migration-meta">
                  {`${migration.name} · ${migration.processedRows.toLocaleString(i18n.language)} / ${Math.max(migration.targetEventId, migration.processedRows).toLocaleString(i18n.language)}`}
                </p>
              </Card>
            ) : null}
            <DatabaseStatusCard
              status={usageStatus}
              loading={usageStatusLoading}
              error={usageStatusError}
            />
          </div>
        </section>
      )}

      {auth.connectionStatus === 'connected' && (
        <div
          className={`card runtime-disclosure-card ${modelsOpen ? 'is-open' : ''}`}
          data-testid="runtime-models-card"
        >
          <div className="runtime-disclosure-header">
            <button
              type="button"
              className="runtime-disclosure-trigger"
              aria-expanded={modelsOpen}
              onClick={() => setModelsOpen((prev) => !prev)}
              data-testid="runtime-models-toggle-btn"
            >
              <IconChevronRight
                size={16}
                className={`runtime-chevron ${modelsOpen ? 'is-open' : ''}`}
                aria-hidden="true"
              />
              <IconModelCluster size={16} className="runtime-disclosure-icon" aria-hidden="true" />
              <span className="runtime-disclosure-title">{t('system_info.models_title')}</span>
              <span className="runtime-count-badge">
                {models.length.toLocaleString(i18n.language)}
              </span>
              {resolvedModelsError && (
                <span className="status-pill is-warning">
                  <span className="status-dot" aria-hidden="true" />
                  <span>{t('dashboard.health_status_warning', { defaultValue: 'Warning' })}</span>
                </span>
              )}
            </button>
            <div className="runtime-disclosure-actions">
              <Button
                variant="secondary"
                size="sm"
                onClick={() => void syncUpstreamModels()}
                loading={modelsSyncing}
                disabled={modelsLoading || modelsSyncing || !managerCredential}
                title={t('system_info.models_sync_upstream_hint')}
                data-testid="runtime-models-sync-btn"
              >
                {t('system_info.models_sync_upstream')}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => void refreshModels(true)}
                loading={modelsLoading}
                disabled={modelsSyncing}
                data-testid="runtime-models-refresh-btn"
              >
                {t('common.refresh')}
              </Button>
            </div>
          </div>
          <div className="runtime-disclosure-body" hidden={!modelsOpen}>
            <div className="runtime-models-panel">
              <div className="runtime-models-toolbar">
                <div className="runtime-models-meta">
                  <span className="muted">{t('system_info.models_desc')}</span>
                  {models.length > 0 && (
                    <>
                      <span className="runtime-models-sep" aria-hidden="true">
                        ·
                      </span>
                      <span className="runtime-models-copy-hint">
                        {t('runtime_status.models_click_to_copy', {
                          defaultValue: 'Click any model to copy its ID',
                        })}
                      </span>
                    </>
                  )}
                </div>
                {models.length > 0 && (
                  <label className="runtime-models-search">
                    <IconSearch size={14} aria-hidden="true" />
                    <input
                      type="search"
                      value={modelQuery}
                      onChange={(event) => setModelQuery(event.target.value)}
                      placeholder={t('runtime_status.models_filter_placeholder', {
                        defaultValue: 'Filter models…',
                      })}
                      aria-label={t('runtime_status.models_filter_placeholder', {
                        defaultValue: 'Filter models…',
                      })}
                      data-testid="runtime-models-filter-input"
                    />
                    {modelQuery && (
                      <button
                        type="button"
                        className="runtime-search-clear"
                        onClick={() => setModelQuery('')}
                        aria-label={t('common.clear', { defaultValue: 'Clear' })}
                      >
                        <IconX size={12} aria-hidden="true" />
                      </button>
                    )}
                  </label>
                )}
              </div>

              {resolvedModelsError ? <div className="error-box">{resolvedModelsError}</div> : null}

              {!modelsCache && modelsLoading ? (
                <div className="hint">{t('common.loading')}</div>
              ) : groupedModels.length === 0 ? (
                <div className="hint">{t('system_info.models_empty')}</div>
              ) : filteredModelsCount === 0 ? (
                <div className="hint">
                  {t('runtime_status.models_filter_empty', {
                    defaultValue: 'No models match this filter.',
                  })}
                </div>
              ) : (
                <div className="runtime-model-groups">
                  {filteredModelGroups.map((group) => (
                    <div key={group.id} className="runtime-model-group">
                      <strong>{`${group.label} (${group.items.length})`}</strong>
                      <div className="runtime-model-tags">
                        {group.items.map((model) => {
                          const isCopied = copiedModel === model.name;
                          return (
                            <button
                              key={`${model.name}-${model.alias ?? 'default'}`}
                              type="button"
                              className={`runtime-model-tag ${isCopied ? 'is-copied' : ''}`}
                              onClick={() => void handleCopyModel(model.name)}
                              title={model.name}
                              data-testid="runtime-model-chip"
                            >
                              <span>{model.name}</span>
                              {isCopied ? (
                                <IconCheck size={12} aria-hidden="true" />
                              ) : (
                                <IconCopy
                                  size={11}
                                  className="runtime-model-copy-icon"
                                  aria-hidden="true"
                                />
                              )}
                            </button>
                          );
                        })}
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>
        </div>
      )}

      <div
        className={`card runtime-disclosure-card runtime-details-card ${detailsOpen ? 'is-open' : ''}`}
      >
        <div className="runtime-disclosure-header">
          <button
            type="button"
            className="runtime-disclosure-trigger"
            aria-expanded={detailsOpen}
            onClick={() => setDetailsOpen((prev) => !prev)}
            data-testid="runtime-details-toggle-btn"
          >
            <IconChevronRight
              size={16}
              className={`runtime-chevron ${detailsOpen ? 'is-open' : ''}`}
              aria-hidden="true"
            />
            <IconCode size={16} className="runtime-disclosure-icon" aria-hidden="true" />
            <span className="runtime-disclosure-title">
              {t('runtime_status.details', { defaultValue: 'Runtime details' })}
            </span>
            <span className="runtime-disclosure-badge">JSON</span>
          </button>
          <div className="runtime-disclosure-actions">
            <Button
              variant="secondary"
              size="sm"
              onClick={(event) => void handleCopySnapshot(event)}
              data-testid="runtime-copy-snapshot-btn"
            >
              <span className="runtime-btn-content">
                {copiedDetails ? (
                  <IconCheck size={13} aria-hidden="true" />
                ) : (
                  <IconCopy size={13} aria-hidden="true" />
                )}
                <span>
                  {copiedDetails
                    ? t('runtime_status.copied_details', { defaultValue: 'Copied' })
                    : t('runtime_status.copy_details', { defaultValue: 'Copy snapshot' })}
                </span>
              </span>
            </Button>
          </div>
        </div>

        <div className="runtime-disclosure-body" hidden={!detailsOpen}>
          {detailsMetaItems.length > 0 && (
            <div className="runtime-details-meta-strip" data-testid="runtime-details-meta-strip">
              {detailsMetaItems.map((item) => (
                <div key={item.label} className="runtime-details-meta-item">
                  <span>{item.label}</span>
                  <strong className={item.mono ? 'is-mono' : undefined} title={item.value}>
                    {item.value}
                  </strong>
                </div>
              ))}
            </div>
          )}
          <pre className="runtime-snapshot-pre">{diagnosticSnapshot}</pre>
        </div>
      </div>
    </section>
  );
}
