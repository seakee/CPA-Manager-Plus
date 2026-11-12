import { useEffect, useMemo, useRef, useState, type ReactElement } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Navigate,
  useLocation,
  useRoutes,
  type Location,
  type RouteObject,
} from 'react-router-dom';
import { AccountsPage } from '@/pages/AccountsPage';
import { DashboardPage } from '@/pages/DashboardPage';
import { AiProvidersPage } from '@/pages/AiProvidersPage';
import { AiProvidersClaudeEditLayout } from '@/pages/AiProvidersClaudeEditLayout';
import { AiProvidersClaudeEditPage } from '@/pages/AiProvidersClaudeEditPage';
import { AiProvidersClaudeModelsPage } from '@/pages/AiProvidersClaudeModelsPage';
import { AiProvidersCodexEditPage } from '@/pages/AiProvidersCodexEditPage';
import { AiProvidersGeminiEditPage } from '@/pages/AiProvidersGeminiEditPage';
import { AiProvidersOpenAIEditLayout } from '@/pages/AiProvidersOpenAIEditLayout';
import { AiProvidersOpenAIEditPage } from '@/pages/AiProvidersOpenAIEditPage';
import { AiProvidersOpenAIModelsPage } from '@/pages/AiProvidersOpenAIModelsPage';
import { AiProvidersVertexEditPage } from '@/pages/AiProvidersVertexEditPage';
import { OAuthPage } from '@/pages/OAuthPage';
import { UsageAnalyticsPage } from '@/pages/UsageAnalyticsPage';
import { MonitoringCenterPage } from '@/pages/MonitoringCenterPage';
import { AccountActionCandidatesPage } from '@/pages/AccountActionCandidatesPage';
import { ModelPricesPage } from '@/pages/ModelPricesPage';
import { ConfigPage } from '@/pages/ConfigPage';
import { LogsPage } from '@/pages/LogsPage';
import { PluginResourcePage } from '@/pages/PluginResourcePage';
import { PluginsPage } from '@/pages/PluginsPage';
import { SystemPage } from '@/pages/SystemPage';
import { RuntimeStatusPage } from '@/pages/RuntimeStatusPage';
import { ManagerUpdatePage } from '@/pages/ManagerUpdatePage';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { pluginsApi } from '@/services/api';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { ensureRouteBasePathname, isDemoMode } from '@/features/demo/demoMode';
import { useAuthStore, useConfigStore } from '@/stores';

type FeatureKey = 'requestMonitoring' | 'modelPrices';

function LegacyAccountsRedirect({ healthMode }: { healthMode: 'local' | 'server' }) {
  const location = useLocation();
  const params = new URLSearchParams(location.search);
  params.set('view', 'health');
  params.set('healthMode', healthMode);
  const search = params.toString();
  return <Navigate to={{ pathname: '/accounts', search: search ? `?${search}` : '' }} replace />;
}

type PluginProbeStatus = 'unknown' | 'supported' | 'unsupported' | 'error';

interface PluginProbeTarget {
  apiBase: string;
  managementKey: string;
}

interface PluginProbe extends PluginProbeTarget {
  status: PluginProbeStatus;
}

function samePluginProbeTarget(
  left: PluginProbeTarget | null,
  apiBase: string,
  managementKey: string
): boolean {
  return left !== null && left.apiBase === apiBase && left.managementKey === managementKey;
}

/**
 * Only a refusal that names the plugin namespace itself proves the CPA has no
 * plugins. Every other failure — missing auth, reload, timeout, proxy error —
 * is evidence about that one request, not about the connection, so it must
 * never be remembered as "unsupported": that verdict redirects a page that a
 * supporting CPA would have served.
 */
function pluginNamespaceMissing(error: unknown): boolean {
  const status = (error as { status?: unknown } | null)?.status;
  return status === 404 || status === 405;
}

export function PluginGate({ children }: { children: ReactElement }) {
  const { t } = useTranslation();
  const supportsPlugin = useAuthStore((state) => state.supportsPlugin);
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey) ?? '';
  const updateServerPluginSupport = useAuthStore((state) => state.updateServerPluginSupport);
  const demo = __DEMO_SITE__ && isDemoMode();
  const [probe, setProbe] = useState<PluginProbe>({
    apiBase: '',
    managementKey: '',
    status: 'unknown',
  });
  // The probe reads and writes this mirror rather than the render state, so a
  // re-render caused by the probe itself cannot invalidate its own result.
  const probeRef = useRef<PluginProbe>(probe);
  // The connection and credential a probe is already being asked of, so a
  // re-render for an unrelated reason does not ask it twice.
  const askedRef = useRef<PluginProbeTarget | null>(null);
  const [attempt, setAttempt] = useState(0);

  const record = (next: PluginProbe) => {
    probeRef.current = next;
    setProbe(next);
  };

  // The plugin capability travels on a CPA management response header, so a
  // cold load into `/console/plugins` has not seen it yet. One probe settles
  // the answer, keyed by the connection and credential it was asked of:
  // pointing the Console at another CPA or re-authenticating after a 401
  // probes again instead of inheriting the previous verdict.
  useEffect(() => {
    if (demo || supportsPlugin || !apiBase) return;
    if (
      samePluginProbeTarget(probeRef.current, apiBase, managementKey) &&
      probeRef.current.status !== 'unknown'
    ) {
      return;
    }
    if (samePluginProbeTarget(askedRef.current, apiBase, managementKey)) return;
    askedRef.current = { apiBase, managementKey };
    let cancelled = false;
    void pluginsApi.list().then(
      () => {
        // A namespace that answered is itself the evidence: this CPA serves
        // plugins even when the async capability header has not arrived yet, so
        // sync the shared store before child plugin pages read `supportsPlugin`.
        if (!cancelled) {
          record({ apiBase, managementKey, status: 'supported' });
          updateServerPluginSupport?.(true);
        }
      },
      (error: unknown) => {
        if (!cancelled) {
          record({
            apiBase,
            managementKey,
            status: pluginNamespaceMissing(error) ? 'unsupported' : 'error',
          });
        }
      }
    );
    return () => {
      cancelled = true;
      if (samePluginProbeTarget(askedRef.current, apiBase, managementKey)) {
        askedRef.current = null;
      }
    };
    // The probe result stays out of the dependencies: settling the probe would
    // otherwise re-run this effect and cancel its own result.
  }, [apiBase, attempt, demo, managementKey, supportsPlugin, updateServerPluginSupport]);

  if (demo || supportsPlugin) {
    return children;
  }
  if (!apiBase) {
    return <LoadingSpinner />;
  }
  const status = samePluginProbeTarget(probe, apiBase, managementKey) ? probe.status : 'unknown';
  if (status === 'supported') {
    return children;
  }
  if (status === 'unsupported') {
    return <Navigate to="/" replace />;
  }
  if (status === 'error') {
    return (
      <div className="card">
        <p role="alert" className="card-body" data-testid="plugin-probe-error">
          {t('fullweb.pluginsProbeFailed')}
        </p>
        <button
          type="button"
          className="btn"
          onClick={() => {
            const reset: PluginProbe = { apiBase: '', managementKey: '', status: 'unknown' };
            probeRef.current = reset;
            askedRef.current = null;
            setProbe(reset);
            setAttempt((current) => current + 1);
          }}
        >
          {t('fullweb.pluginsProbeRetry')}
        </button>
      </div>
    );
  }
  return <LoadingSpinner />;
}

function FeatureGate({
  feature,
  children,
  fallback,
}: {
  feature: FeatureKey;
  children: ReactElement;
  fallback?: ReactElement | null;
}) {
  const availability = usePanelFeatureAvailability();
  const enabled =
    feature === 'requestMonitoring'
      ? availability.requestMonitoringAvailable
      : availability.modelPricesAvailable;

  if (availability.checking) {
    return fallback ?? <LoadingSpinner />;
  }

  if (!enabled) {
    return <Navigate to="/config" replace />;
  }

  return children;
}

/**
 * Request monitoring is a switch, not a missing feature: when only the
 * collector is off, the monitoring page itself owns the "not started" state and
 * its enable action. Redirecting away from it here made the Gateway workspace's
 * ownership link bounce back and left the user with no way to turn it on.
 *
 * Data sub-pages cannot render without the collector, so they pass
 * `unavailablePath` and land on the monitoring page that explains the switch.
 */
export function MonitoringGate({
  children,
  unavailablePath,
}: {
  children: ReactElement;
  unavailablePath?: string;
}) {
  const availability = usePanelFeatureAvailability();

  if (availability.checking) {
    return <LoadingSpinner />;
  }

  if (availability.requestMonitoringAvailable) {
    return children;
  }

  if (!unavailablePath && availability.reason === 'monitoring_disabled') {
    return children;
  }

  return <Navigate to={unavailablePath ?? '/config'} replace />;
}

function LogsGate({ children }: { children: ReactElement }) {
  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const requestedRef = useRef(false);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    if (config || requestedRef.current) return;
    requestedRef.current = true;
    fetchConfig().catch(() => setFailed(true));
  }, [config, fetchConfig]);

  if (!config && !failed) {
    return <LoadingSpinner />;
  }

  return children;
}

const mainRoutes: RouteObject[] = [
  { path: '/', element: <DashboardPage /> },
  { path: '/dashboard', element: <DashboardPage /> },
  { path: '/settings', element: <Navigate to="/config" replace /> },
  { path: '/api-keys', element: <Navigate to="/config" replace /> },
  { path: '/ai-providers/gemini/new', element: <AiProvidersGeminiEditPage /> },
  { path: '/ai-providers/gemini/:index', element: <AiProvidersGeminiEditPage /> },
  { path: '/ai-providers/codex/new', element: <AiProvidersCodexEditPage /> },
  { path: '/ai-providers/codex/:index', element: <AiProvidersCodexEditPage /> },
  {
    path: '/ai-providers/claude/new',
    element: <AiProvidersClaudeEditLayout />,
    children: [
      { index: true, element: <AiProvidersClaudeEditPage /> },
      { path: 'models', element: <AiProvidersClaudeModelsPage /> },
    ],
  },
  {
    path: '/ai-providers/claude/:index',
    element: <AiProvidersClaudeEditLayout />,
    children: [
      { index: true, element: <AiProvidersClaudeEditPage /> },
      { path: 'models', element: <AiProvidersClaudeModelsPage /> },
    ],
  },
  { path: '/ai-providers/vertex/new', element: <AiProvidersVertexEditPage /> },
  { path: '/ai-providers/vertex/:index', element: <AiProvidersVertexEditPage /> },
  {
    path: '/ai-providers/openai/new',
    element: <AiProvidersOpenAIEditLayout />,
    children: [
      { index: true, element: <AiProvidersOpenAIEditPage /> },
      { path: 'models', element: <AiProvidersOpenAIModelsPage /> },
    ],
  },
  {
    path: '/ai-providers/openai/:index',
    element: <AiProvidersOpenAIEditLayout />,
    children: [
      { index: true, element: <AiProvidersOpenAIEditPage /> },
      { path: 'models', element: <AiProvidersOpenAIModelsPage /> },
    ],
  },
  { path: '/ai-providers', element: <AiProvidersPage /> },
  { path: '/ai-providers/*', element: <AiProvidersPage /> },
  { path: '/accounts', element: <AccountsPage /> },
  { path: '/oauth', element: <OAuthPage /> },
  {
    path: '/usage-analytics',
    element: (
      <FeatureGate feature="requestMonitoring">
        <UsageAnalyticsPage />
      </FeatureGate>
    ),
  },
  {
    path: '/codex-inspection',
    element: <LegacyAccountsRedirect healthMode="local" />,
  },
  {
    path: '/codex-inspection/server',
    element: <LegacyAccountsRedirect healthMode="server" />,
  },
  {
    path: '/model-prices',
    element: (
      <FeatureGate feature="modelPrices">
        <ModelPricesPage />
      </FeatureGate>
    ),
  },
  {
    path: '/monitoring',
    element: (
      <MonitoringGate>
        <MonitoringCenterPage />
      </MonitoringGate>
    ),
  },
  {
    path: '/monitoring/account-actions',
    element: (
      <MonitoringGate unavailablePath="/monitoring">
        <AccountActionCandidatesPage />
      </MonitoringGate>
    ),
  },
  {
    path: '/monitoring/model-prices',
    element: (
      <FeatureGate feature="modelPrices">
        <Navigate to="/model-prices" replace />
      </FeatureGate>
    ),
  },
  {
    path: '/monitoring/codex-inspection',
    element: <LegacyAccountsRedirect healthMode="local" />,
  },
  {
    path: '/monitoring/codex-inspection/server',
    element: <LegacyAccountsRedirect healthMode="server" />,
  },
  {
    path: '/plugins',
    element: (
      <PluginGate>
        <PluginsPage />
      </PluginGate>
    ),
  },
  {
    path: '/plugin-store',
    element: (
      <PluginGate>
        <Navigate to="/plugins?tab=store" replace />
      </PluginGate>
    ),
  },
  {
    path: '/plugin-pages/:pluginId/:menuIndex',
    element: (
      <PluginGate>
        <PluginResourcePage />
      </PluginGate>
    ),
  },
  { path: '/plugins/*', element: <Navigate to="/plugins" replace /> },
  { path: '/plugin-store/*', element: <Navigate to="/plugins?tab=store" replace /> },
  { path: '/plugin-pages/*', element: <Navigate to="/" replace /> },
  { path: '/config', element: <ConfigPage /> },
  { path: '/gateway', element: <ConfigPage /> },
  {
    path: '/logs',
    element: (
      <LogsGate>
        <LogsPage />
      </LogsGate>
    ),
  },
  { path: '/system', element: <SystemPage /> },
  { path: '/system/runtime', element: <RuntimeStatusPage /> },
  { path: '/system/updates', element: <ManagerUpdatePage /> },
  { path: '*', element: <Navigate to="/" replace /> },
];

const ensureRouteLocationBase = (
  location: Location | undefined,
  routeBase: string | undefined
): Location | undefined => {
  if (!location || !routeBase) return location;

  const pathname = ensureRouteBasePathname(location.pathname, routeBase);
  if (pathname === location.pathname) return location;

  return {
    ...location,
    pathname,
  };
};

export function MainRoutes({ location, routeBase }: { location?: Location; routeBase?: string }) {
  const routeLocation = useMemo(
    () => ensureRouteLocationBase(location, routeBase),
    [location, routeBase]
  );

  return useRoutes(mainRoutes, routeLocation);
}
