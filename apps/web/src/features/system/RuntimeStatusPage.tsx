import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { useAuthStore, useNotificationStore } from '@/stores';
import { createPublicApiClient, PublicApiError } from '@/fullweb/publicApi';
import type { FullWebCredential, PublicApiClient } from '@/fullweb/publicApi';
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

function statusLabel(value: string | undefined, fallback: string) {
  return value ? value.replace(/_/g, ' ') : fallback;
}

function newRequestId() {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return crypto.randomUUID();
  return `runtime-${Date.now()}-${Math.random().toString(16).slice(2)}`;
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
  const { t } = useTranslation();
  const auth = useAuthStore();
  const { showConfirmation, showNotification } = useNotificationStore();
  const managerCredential = credential || auth.managementKey;
  const [status, setStatus] = useState<RuntimeStatusResponse | null>(null);
  const [operation, setOperation] = useState<RuntimeOperation | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [detailsOpen, setDetailsOpen] = useState(false);
  const requestRef = useRef(0);
  const busyRef = useRef(false);

  const refresh = useCallback(async () => {
    if (!managerCredential) {
      setLoading(false);
      return;
    }
    const requestId = ++requestRef.current;
    try {
      const next = await client.get<RuntimeStatusResponse>('runtime/status', {
        kind: 'admin',
        value: managerCredential,
      });
      if (requestId === requestRef.current) setStatus(next);
    } catch (error) {
      if (error instanceof PublicApiError && error.kind === 'authentication') {
        onUnauthorized?.();
      } else if (requestId === requestRef.current) {
        setStatus((current) =>
          current
            ? { ...current, status: 'unknown', errorCode: 'manager_unreachable' }
            : { mode: 'embedded', status: 'unknown', manager: { state: 'unknown' }, errorCode: 'manager_unreachable' }
        );
      }
    } finally {
      if (requestId === requestRef.current) setLoading(false);
    }
  }, [client, managerCredential, onUnauthorized]);

  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') void refresh();
    }, POLL_MS);
    return () => window.clearInterval(timer);
  }, [refresh]);

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
          showNotification(t('runtime_status.operation_conflict', { defaultValue: 'Another runtime operation is already in progress.' }), 'warning');
        } else {
          showNotification(t('runtime_status.operation_failed', { defaultValue: 'Runtime operation could not be completed.' }), 'error');
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
        title: action === 'stop'
          ? t('runtime_status.stop_title', { defaultValue: 'Stop CPA gateway?' })
          : t('runtime_status.restart_title', { defaultValue: 'Restart CPA gateway?' }),
        message: t('runtime_status.interruption', { defaultValue: 'Gateway requests may be interrupted. CPAMP Manager remains available.' }),
        confirmText: action === 'stop'
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
        happened: t('runtime_status.unknown_happened', { defaultValue: 'The latest gateway observation is unavailable.' }),
        impact: t('runtime_status.unknown_impact', { defaultValue: 'The current gateway state cannot be confirmed.' }),
        next: t('runtime_status.unknown_next', { defaultValue: 'Refresh or inspect the system logs.' }),
      };
    }
    if (gateway?.recovery?.state === 'manual_intervention') {
      return {
        happened: t('runtime_status.manual_happened', { defaultValue: 'Automatic recovery requires attention.' }),
        impact: t('runtime_status.manual_impact', { defaultValue: 'Gateway readiness may be unavailable.' }),
        next: t('runtime_status.manual_next', { defaultValue: 'Review logs before taking another lifecycle action.' }),
      };
    }
    if (gateway && !gateway.ready && gateway.state !== 'offline') {
      return {
        happened: t('runtime_status.not_ready_happened', { defaultValue: 'The gateway process is running but not ready.' }),
        impact: t('runtime_status.not_ready_impact', { defaultValue: 'Gateway requests may fail until readiness returns.' }),
        next: t('runtime_status.not_ready_next', { defaultValue: 'Wait for readiness or restart if the capability is available.' }),
      };
    }
    return null;
  }, [gateway, status, t]);

  return (
    <main className="main-content runtime-status-page" data-testid="runtime-status-page">
      <header className="page-header">
        <div>
          <h1>{t('runtime_status.title', { defaultValue: 'Runtime status' })}</h1>
          <p className="page-subtitle">
            {status?.mode === 'external'
              ? t('runtime_status.external_subtitle', { defaultValue: 'External CPA connection · read-only' })
              : t('runtime_status.subtitle', { defaultValue: 'Observe the Manager and CPA gateway lifecycle separately.' })}
          </p>
        </div>
        <Button variant="secondary" onClick={() => void refresh()} disabled={loading || busy}>
          {loading ? t('common.loading', { defaultValue: 'Loading…' }) : t('common.refresh', { defaultValue: 'Refresh' })}
        </Button>
      </header>

      <section className="runtime-status-summary" aria-live="polite">
        <strong>{statusLabel(status?.status, t('runtime_status.loading', { defaultValue: 'Loading' }))}</strong>
        <span>{status?.observedAt ? new Date(status.observedAt).toLocaleString() : t('runtime_status.no_observation', { defaultValue: 'No observation yet' })}</span>
      </section>

      <section className="runtime-status-cards">
        <Card>
          <h2>{t('runtime_status.manager', { defaultValue: 'CPAMP Manager' })}</h2>
          <p>{statusLabel(status?.manager.state, t('runtime_status.unknown', { defaultValue: 'Unknown' }))}</p>
          <small>{t('runtime_status.manager_fact', { defaultValue: 'Manager authentication and API routing are independent from gateway readiness.' })}</small>
        </Card>
        <Card>
          <h2>{t('runtime_status.gateway', { defaultValue: 'CPA gateway' })}</h2>
          <p>{statusLabel(gateway?.state, t('runtime_status.unknown', { defaultValue: 'Unknown' }))}</p>
          <small>{gateway?.ready ? t('runtime_status.ready', { defaultValue: 'Ready for gateway requests' }) : t('runtime_status.not_ready', { defaultValue: 'Not ready for gateway requests' })}</small>
          {status?.mode === 'embedded' && gateway && (
            <div className="runtime-status-actions">
              {gateway.state === 'offline' ? (
                <Button onClick={() => requestAction('start')} disabled={busy || !gateway.capabilities?.includes('start')}>
                  {t('runtime_status.start', { defaultValue: 'Start' })}
                </Button>
              ) : (
                <Button onClick={() => requestAction('restart')} disabled={busy || !gateway.capabilities?.includes('restart')}>
                  {t('runtime_status.restart', { defaultValue: 'Restart' })}
                </Button>
              )}
              <Button variant="secondary" onClick={() => requestAction('stop')} disabled={busy || !gateway.capabilities?.includes('stop')}>
                {t('runtime_status.stop', { defaultValue: 'Stop' })}
              </Button>
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
                      ? t('runtime_status.writes_blocked', { defaultValue: 'Blocked by this version' })
                      : t('runtime_status.writes_allowed', { defaultValue: 'Not blocked by this version' })}
                  </dd>
                </div>
              </dl>
            </div>
          )}
          {status?.mode === 'external' && (
            <nav className="runtime-status-links" aria-label={t('runtime_status.external_links', { defaultValue: 'External runtime links' })}>
              <Link to="/config">{t('nav.gateway_management', { defaultValue: 'Gateway Management' })}</Link>
              <Link to="/logs">{t('nav.logs', { defaultValue: 'Logs' })}</Link>
            </nav>
          )}
        </Card>
      </section>

      {anomaly && (
        <Card className="runtime-status-anomaly">
          <h2>{t('runtime_status.anomaly', { defaultValue: 'Attention needed' })}</h2>
          <dl>
            <div><dt>{t('runtime_status.what_happened', { defaultValue: 'What happened' })}</dt><dd>{anomaly.happened}</dd></div>
            <div><dt>{t('runtime_status.impact', { defaultValue: 'Impact' })}</dt><dd>{anomaly.impact}</dd></div>
            <div><dt>{t('runtime_status.next_step', { defaultValue: 'Next step' })}</dt><dd>{anomaly.next}</dd></div>
          </dl>
        </Card>
      )}

      {operation && (
        <p className="runtime-status-operation" role="status">
          {t('runtime_status.operation_status', { defaultValue: 'Operation {{action}}: {{state}}', action: operation.action, state: operation.state })}
          {operation.state === 'unknown' && ` · ${t('runtime_status.query_original', { defaultValue: 'Result pending confirmation; the original operation was not resent.' })}`}
        </p>
      )}

      <details open={detailsOpen} onToggle={(event) => setDetailsOpen(event.currentTarget.open)}>
        <summary>{t('runtime_status.details', { defaultValue: 'Runtime details' })}</summary>
        <pre>{JSON.stringify({ mode: status?.mode, gateway, observedAt: status?.observedAt, operation }, null, 2)}</pre>
      </details>
    </main>
  );
}
