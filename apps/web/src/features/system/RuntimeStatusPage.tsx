import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import type { LocalizedText } from '@/i18n/localizedText';
import { formatLocalizedText } from '@/i18n/localizedText';
import { apiKeysApi } from '@/services/api/apiKeys';
import { isGatewayPendingError } from '@/services/api/models';
import { getUsageServiceErrorCode, usageServiceApi, type UsageServiceStatus } from '@/services/api/usageService';
import { useAuthStore, useConfigStore, useModelsStore, useNotificationStore } from '@/stores';
import { copyToClipboard } from '@/utils/clipboard';
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
  useEffect(
    () => () => {
      refreshModelsSeqRef.current += 1;
      if (copyTimerRef.current !== null) {
        globalThis.clearTimeout(copyTimerRef.current);
        copyTimerRef.current = null;
      }
    },
    []
  );

  const otherLabel = useMemo(() => t('system_info.models_group_unclassified'), [t]);
  const groupedModels = useMemo(() => classifyModels(models, { otherLabel }), [models, otherLabel]);
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
        let apiKeys: string[] = JSON.parse(configApiKeysKey) as string[];
        if (!apiKeys.length) {
          try {
            const list = await apiKeysApi.list();
            if (Array.isArray(list)) {
              apiKeys = list
                .map((item) =>
                  typeof item === 'string'
                    ? item.trim()
                    : String((item as Record<string, unknown>)?.['api-key'] ?? '').trim()
                )
                .filter(Boolean);
            }
          } catch {
            apiKeys = [];
          }
        }
        if (seq !== refreshModelsSeqRef.current) return;
        const firstKey = apiKeys[0];
        const modelManagerCredential =
          auth.sessionMode === 'manager_embedded' && /\/api\/v2\/cpamp$/i.test(auth.apiBase)
            ? auth.managementKey || credential
            : undefined;
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
    [client, managerCredential, onUnauthorized, queryOperation, refresh, showNotification, status?.mode, t]
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
    : collector?.lastError
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

  const handleCopySnapshot = async (event: React.MouseEvent) => {
    event.preventDefault();
    event.stopPropagation();
    const copied = await copyToClipboard(diagnosticSnapshot);
    if (!copied) return;
    setCopiedDetails(true);
    if (copyTimerRef.current !== null) {
      globalThis.clearTimeout(copyTimerRef.current);
    }
    const scheduleTimeout =
      typeof window !== 'undefined' && typeof window.setTimeout === 'function'
        ? window.setTimeout.bind(window)
        : globalThis.setTimeout.bind(globalThis);
    copyTimerRef.current = Number(
      scheduleTimeout(() => {
        copyTimerRef.current = null;
        setCopiedDetails(false);
      }, 1800)
    );
  };

  const observedLabel = status?.observedAt
    ? t('runtime_status.observed_at', {
        defaultValue: 'Observed {{time}}',
        time: new Date(status.observedAt).toLocaleString(i18n.language),
      })
    : t('runtime_status.no_observation', { defaultValue: 'No observation yet' });

  return (
    <section className="runtime-status-page" data-testid="runtime-status-page">
      <header className="page-header" aria-live="polite">
        <div className="page-header-meta">
          <span className={`status-pill ${pillToneForState(status?.status)}`}>
            <span className="status-dot" aria-hidden="true" />
            <strong data-testid="runtime-status-state">
              {stateLabel(
                status?.status,
                t('runtime_status.loading', { defaultValue: 'Loading' })
              )}
            </strong>
          </span>
          <p className="page-subtitle">
            {status?.mode === 'external'
              ? t('runtime_status.external_subtitle', {
                  defaultValue: 'External CPA connection · read-only',
                })
              : t('runtime_status.subtitle', {
                  defaultValue: 'Observe the Manager and CPA gateway lifecycle separately.',
                })}
          </p>
          <span className="runtime-observed-time">{`· ${observedLabel}`}</span>
        </div>
        <div className="page-header-actions">
          <Button
            variant="secondary"
            size="sm"
            onClick={() => void refresh()}
            disabled={loading || busy}
          >
            {loading
              ? t('common.loading', { defaultValue: 'Loading…' })
              : t('common.refresh', { defaultValue: 'Refresh' })}
          </Button>
        </div>
      </header>

      <section className="runtime-status-cards">
        <Card className="runtime-status-card">
          <div className="runtime-card-head">
            <h2>{t('runtime_status.manager', { defaultValue: 'CPAMP Manager' })}</h2>
            <span className={`runtime-inline-status ${pillToneForState(status?.manager.state)}`}>
              <span className="status-dot" aria-hidden="true" />
              <span data-testid="runtime-status-manager-state">
                {stateLabel(
                  status?.manager.state,
                  t('runtime_status.unknown', { defaultValue: 'Unknown' })
                )}
              </span>
            </span>
          </div>
          <small>
            {t('runtime_status.manager_fact', {
              defaultValue: 'Manager authentication and API routing are independent from gateway readiness.',
            })}
          </small>
          <div className="runtime-status-version">
            <dl>
              <div>
                <dt>{t('runtime_status.mode_label', { defaultValue: 'Connection mode' })}</dt>
                <dd>
                  {status?.mode === 'external'
                    ? t('runtime_status.mode_external', { defaultValue: 'External' })
                    : t('runtime_status.mode_embedded', { defaultValue: 'Embedded' })}
                </dd>
              </div>
              <div>
                <dt>{t('runtime_status.sqlite_usage_label', { defaultValue: 'SQLite size' })}</dt>
                <dd>
                  {usageStatus?.database?.totalBytes != null
                    ? formatFileSize(usageStatus.database.totalBytes)
                    : '—'}
                </dd>
              </div>
              <div>
                <dt>
                  {t('runtime_status.migration_title', {
                    defaultValue: 'Background Data Migration',
                  })}
                </dt>
                <dd>
                  {!migration
                    ? '—'
                    : migration.status === 'completed'
                      ? t('runtime_status.migration_done', { defaultValue: 'Completed' })
                      : t('runtime_status.migration_running', {
                          defaultValue: 'Running ({{done}}/{{total}})',
                          done: migration.processedRows.toLocaleString(i18n.language),
                          total: Math.max(migration.targetEventId, migration.processedRows).toLocaleString(i18n.language),
                        })}
                </dd>
              </div>
            </dl>
          </div>
          <nav className="runtime-status-links">
            <Link to="/system">
              {t('nav.system_config', { defaultValue: 'System Configuration' })}
            </Link>
            <Link to="/monitoring">
              {t('nav.monitoring_center', { defaultValue: 'Request Monitor' })}
            </Link>
          </nav>
        </Card>

        <Card className="runtime-status-card">
          <div className="runtime-card-head">
            <h2>{t('runtime_status.gateway', { defaultValue: 'CPA gateway' })}</h2>
            <span className={`runtime-inline-status ${pillToneForState(gateway?.state)}`}>
              <span className="status-dot" aria-hidden="true" />
              <span data-testid="runtime-status-gateway-state">
                {stateLabel(gateway?.state, t('runtime_status.unknown', { defaultValue: 'Unknown' }))}
              </span>
            </span>
          </div>
          <small>
            {gateway?.ready
              ? t('runtime_status.ready', { defaultValue: 'Ready for gateway requests' })
              : t('runtime_status.not_ready', { defaultValue: 'Not ready for gateway requests' })}
          </small>
          {status?.mode === 'embedded' && gateway && (
            <div className="runtime-status-actions">
              {gateway.state === 'offline' ? (
                <Button
                  size="sm"
                  onClick={() => requestAction('start')}
                  disabled={busy || !gateway.capabilities?.includes('start')}
                  data-testid="runtime-status-start"
                >
                  {t('runtime_status.start', { defaultValue: 'Start' })}
                </Button>
              ) : (
                <Button
                  size="sm"
                  onClick={() => requestAction('restart')}
                  disabled={busy || !gateway.capabilities?.includes('restart')}
                >
                  {t('runtime_status.restart', { defaultValue: 'Restart' })}
                </Button>
              )}
              <Button
                variant="secondary"
                size="sm"
                onClick={() => requestAction('stop')}
                disabled={busy || !gateway.capabilities?.includes('stop')}
              >
                {t('runtime_status.stop', { defaultValue: 'Stop' })}
              </Button>
            </div>
          )}
          {status?.mode !== 'external' && artifact && (
            <div className="runtime-status-version" data-testid="runtime-status-engine-source">
              <dl>
                <div>
                  <dt>{t('runtime_status.engine_source')}</dt>
                  <dd data-testid="runtime-status-engine-source-value">
                    {artifact.active?.origin === 'bundled'
                      ? t('runtime_status.engine_source_bundled')
                      : artifact.active?.origin === 'acquired'
                        ? t('runtime_status.engine_source_acquired')
                        : t('runtime_status.engine_source_required')}
                  </dd>
                </div>
              </dl>
            </div>
          )}
          {status?.mode === 'external' && (
            <div className="runtime-status-version" data-testid="runtime-status-cpa-version">
              <dl>
                <div>
                  <dt>{t('runtime_status.cpa_version', { defaultValue: 'CPA version' })}</dt>
                  <dd>{gateway?.version || t('runtime_status.unknown', { defaultValue: 'Unknown' })}</dd>
                </div>
                <div>
                  <dt>{t('runtime_status.version_support', { defaultValue: 'Version support' })}</dt>
                  <dd>
                    {gateway?.versionSupport === 'supported'
                      ? t('runtime_status.version_supported', { defaultValue: 'Supported' })
                      : gateway?.versionSupport === 'unsupported'
                        ? t('runtime_status.version_unsupported', { defaultValue: 'Unsupported' })
                        : t('runtime_status.version_unverified', { defaultValue: 'Unverified' })}
                  </dd>
                </div>
                <div>
                  <dt>{t('runtime_status.writes', { defaultValue: 'Writes' })}</dt>
                  <dd>
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
            </div>
          )}
          {status?.mode === 'external' && (
            <nav
              className="runtime-status-links"
              aria-label={t('runtime_status.external_links', {
                defaultValue: 'External runtime links',
              })}
            >
              <Link to="/config">
                {t('nav.gateway_management', { defaultValue: 'Gateway Management' })}
              </Link>
              <Link to="/logs">{t('nav.logs', { defaultValue: 'Logs' })}</Link>
            </nav>
          )}
        </Card>
      </section>

      {featureAvailability.managerServiceAvailable && (
        <section className="runtime-observability-grid">
          <Card
            title={t('runtime_status.collector_title', { defaultValue: 'Usage Collector Stream' })}
            extra={
              <span className={`runtime-inline-status ${collectorHealthState.tone}`}>
                <span className="status-dot" aria-hidden="true" />
                {collectorHealthState.label}
              </span>
            }
          >
            <div className="runtime-kpi-grid" data-testid="runtime-collector-card">
              <div className="runtime-kpi-tile">
                <span>{t('runtime_status.collector_events', { defaultValue: 'Stored events' })}</span>
                <strong>{(usageStatus?.events ?? 0).toLocaleString(i18n.language)}</strong>
              </div>
              <div className="runtime-kpi-tile">
                <span>{t('runtime_status.collector_inserted', { defaultValue: 'Inserted' })}</span>
                <strong>{(collector?.totalInserted ?? 0).toLocaleString(i18n.language)}</strong>
              </div>
              <div className="runtime-kpi-tile">
                <span>{t('runtime_status.collector_skipped', { defaultValue: 'Skipped' })}</span>
                <strong>{(collector?.totalSkipped ?? 0).toLocaleString(i18n.language)}</strong>
              </div>
              <div className="runtime-kpi-tile">
                <span>
                  {t('runtime_status.collector_dead_letters', { defaultValue: 'Dead letters' })}
                </span>
                <strong>
                  {(collector?.deadLetters ?? usageStatus?.deadLetters ?? 0).toLocaleString(
                    i18n.language
                  )}
                </strong>
              </div>
            </div>
            <div className="runtime-collector-details">
              <div className="runtime-collector-detail-item">
                <span>{t('dashboard.collector_mode', { defaultValue: 'Mode' })}</span>
                <strong>{collectorModeValue || '-'}</strong>
              </div>
              <div className="runtime-collector-detail-item">
                <span>
                  {t('runtime_status.collector_transport_label', {
                    defaultValue: 'Collector transport',
                  })}
                </span>
                <strong>{collectorTransport || '-'}</strong>
              </div>
              <div className="runtime-collector-detail-item">
                <span>{t('dashboard.collector_last_consumed', { defaultValue: 'Last read' })}</span>
                <strong>{formatCollectorTime(collector?.lastConsumedAt)}</strong>
              </div>
              <div className="runtime-collector-detail-item">
                <span>{t('dashboard.collector_last_inserted', { defaultValue: 'Last write' })}</span>
                <strong>{formatCollectorTime(collector?.lastInsertedAt)}</strong>
              </div>
              <div className="runtime-collector-detail-item">
                <span>
                  {isRespTransport
                    ? t('runtime_status.collector_queue', { defaultValue: 'Queue' })
                    : t('runtime_status.collector_upstream', { defaultValue: 'Upstream' })}
                </span>
                <strong title={isRespTransport ? collector?.queue : collector?.upstream}>
                  {(isRespTransport ? collector?.queue : collector?.upstream) || '-'}
                </strong>
              </div>
            </div>
            {collector?.lastError ? (
              <div className="runtime-error-note">{collector.lastError}</div>
            ) : null}
            <nav className="runtime-status-links">
              <Link to="/monitoring">
                {t('nav.monitoring_center', { defaultValue: 'Request Monitor' })}
              </Link>
              <Link to="/system">
                {t('nav.system_config', { defaultValue: 'System Configuration' })}
              </Link>
            </nav>
          </Card>

          <div className="runtime-database-stack" data-testid="runtime-database-card">
            {migration && migration.status !== 'completed' ? (
              <Card
                title={t('runtime_status.migration_title', {
                  defaultValue: 'Background Data Migration',
                })}
                extra={<span className="status-pill is-warning">{migration.status}</span>}
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

      {anomaly && (
        <Card className="runtime-status-anomaly">
          <h2>{t('runtime_status.anomaly', { defaultValue: 'Attention needed' })}</h2>
          <dl>
            <div>
              <dt>{t('runtime_status.what_happened', { defaultValue: 'What happened' })}</dt>
              <dd>{anomaly.happened}</dd>
            </div>
            <div>
              <dt>{t('runtime_status.impact', { defaultValue: 'Impact' })}</dt>
              <dd>{anomaly.impact}</dd>
            </div>
            <div>
              <dt>{t('runtime_status.next_step', { defaultValue: 'Next step' })}</dt>
              <dd>{anomaly.next}</dd>
            </div>
          </dl>
        </Card>
      )}

      {operation && (
        <p className="runtime-status-operation" role="status" data-testid="runtime-status-operation">
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
        </p>
      )}

      {auth.connectionStatus === 'connected' && (
        <details data-testid="runtime-models-card">
          <summary>
            {`${t('system_info.models_title')} (${models.length.toLocaleString(i18n.language)})`}
          </summary>
          <div className="runtime-models-panel">
            <div className="runtime-models-toolbar">
              <span className="muted">{t('system_info.models_desc')}</span>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => void refreshModels(true)}
                loading={modelsLoading}
              >
                {t('common.refresh')}
              </Button>
            </div>
            {(() => {
              const resolvedModelsError = modelsErrorState?.hidden
                ? null
                : modelsErrorState?.detail
                  ? formatLocalizedText(modelsErrorState.detail, t)
                  : modelsError;
              return resolvedModelsError ? (
                <div className="error-box">{resolvedModelsError}</div>
              ) : null;
            })()}
            {!modelsCache && modelsLoading ? (
              <div className="hint">{t('common.loading')}</div>
            ) : groupedModels.length === 0 ? (
              <div className="hint">{t('system_info.models_empty')}</div>
            ) : (
              <div className="runtime-model-groups">
                {groupedModels.map((group) => (
                  <div key={group.id} className="runtime-model-group">
                    <strong>{`${group.label} (${group.items.length})`}</strong>
                    <div className="runtime-model-tags">
                      {group.items.map((model) => (
                        <span
                          key={`${model.name}-${model.alias ?? 'default'}`}
                          className="runtime-model-tag"
                        >
                          {model.name}
                        </span>
                      ))}
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </details>
      )}

      <div className="runtime-details-box">
        <details open={detailsOpen} onToggle={(event) => setDetailsOpen(event.currentTarget.open)}>
          <summary className="runtime-details-summary">
            <span>{t('runtime_status.details', { defaultValue: 'Runtime details' })}</span>
          </summary>
          <pre>{diagnosticSnapshot}</pre>
        </details>
        <Button
          variant="secondary"
          size="sm"
          className="runtime-details-copy-btn"
          onClick={(event) => void handleCopySnapshot(event)}
          data-testid="runtime-copy-snapshot-btn"
        >
          {copiedDetails
            ? t('runtime_status.copied_details', { defaultValue: 'Copied' })
            : t('runtime_status.copy_details', { defaultValue: 'Copy snapshot' })}
        </Button>
      </div>
    </section>
  );
}
