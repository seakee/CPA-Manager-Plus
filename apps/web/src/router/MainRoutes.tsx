import { useEffect, useMemo, useRef, useState, type ReactElement } from 'react';
import {
  Navigate,
  useLocation,
  useRoutes,
  type Location,
  type RouteObject,
} from 'react-router-dom';
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
import { AuthFilesPage } from '@/pages/AuthFilesPage';
import { AuthFilesOAuthExcludedEditPage } from '@/pages/AuthFilesOAuthExcludedEditPage';
import { AuthFilesOAuthModelAliasEditPage } from '@/pages/AuthFilesOAuthModelAliasEditPage';
import { OAuthPage } from '@/pages/OAuthPage';
import { QuotaPage } from '@/pages/QuotaPage';
import { UsageAnalyticsPage } from '@/pages/UsageAnalyticsPage';
import { MonitoringCenterPage } from '@/pages/MonitoringCenterPage';
import { AccountActionCandidatesPage } from '@/pages/AccountActionCandidatesPage';
import { ModelPricesPage } from '@/pages/ModelPricesPage';
import { CodexInspectionPage } from '@/pages/CodexInspectionPage';
import { ServerCodexInspectionPage } from '@/pages/ServerCodexInspectionPage';
import { ConfigPage } from '@/pages/ConfigPage';
import { LogsPage } from '@/pages/LogsPage';
import { PluginResourcePage } from '@/pages/PluginResourcePage';
import { PluginsPage } from '@/pages/PluginsPage';
import { SystemPage } from '@/pages/SystemPage';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { CodexInspectionModeTabs } from '@/features/monitoring/components/CodexInspectionModeTabs';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { isLogsRouteAvailable } from '@/features/logs/logFeatureAvailability';
import { ensureRouteBasePathname, isDemoMode } from '@/features/demo/demoMode';
import { useAuthStore, useConfigStore } from '@/stores';
import codexInspectionStyles from '@/features/monitoring/CodexInspectionPage.module.scss';

type FeatureKey = 'requestMonitoring' | 'modelPrices' | 'serverCodexInspection';

function PluginGate({ children }: { children: ReactElement }) {
  const supportsPlugin = useAuthStore((state) => state.supportsPlugin);
  if (__DEMO_SITE__ && isDemoMode()) {
    return children;
  }
  if (!supportsPlugin) {
    return <Navigate to="/" replace />;
  }
  return children;
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
      : feature === 'modelPrices'
        ? availability.modelPricesAvailable
        : availability.serverCodexInspectionAvailable;

  if (availability.checking) {
    return fallback ?? <LoadingSpinner />;
  }

  if (!enabled) {
    return <Navigate to="/config" replace />;
  }

  return children;
}

function ServerCodexInspectionRouteFallback() {
  return (
    <div className={codexInspectionStyles.page} aria-busy="true">
      <CodexInspectionModeTabs activeMode="server" />
      <section
        className={[
          codexInspectionStyles.panel,
          codexInspectionStyles.statusPanel,
          codexInspectionStyles.routeSkeletonPanel,
        ]
          .filter(Boolean)
          .join(' ')}
      >
        <div className={codexInspectionStyles.routeSkeletonHeader}>
          <span
            className={[
              codexInspectionStyles.routeSkeletonLine,
              codexInspectionStyles.routeSkeletonLineTitle,
            ]
              .filter(Boolean)
              .join(' ')}
          />
          <span className={codexInspectionStyles.routeSkeletonPill} />
        </div>
        <div className={codexInspectionStyles.routeSkeletonMeta}>
          <span className={codexInspectionStyles.routeSkeletonPill} />
          <span className={codexInspectionStyles.routeSkeletonPill} />
          <span className={codexInspectionStyles.routeSkeletonPillWide} />
        </div>
        <div className={codexInspectionStyles.routeSkeletonGrid}>
          {Array.from({ length: 6 }).map((_, index) => (
            <span key={index} className={codexInspectionStyles.routeSkeletonCard} />
          ))}
        </div>
      </section>
      <section className={codexInspectionStyles.routeSkeletonDetailGrid}>
        <span className={codexInspectionStyles.routeSkeletonBlock} />
        <span className={codexInspectionStyles.routeSkeletonBlockTall} />
      </section>
    </div>
  );
}

function LogsGate({ children }: { children: ReactElement }) {
  const location = useLocation();
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

  if (!isLogsRouteAvailable(config, location.search)) {
    return <Navigate to="/config" replace />;
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
  { path: '/auth-files', element: <AuthFilesPage /> },
  { path: '/auth-files/oauth-excluded', element: <AuthFilesOAuthExcludedEditPage /> },
  { path: '/auth-files/oauth-model-alias', element: <AuthFilesOAuthModelAliasEditPage /> },
  { path: '/oauth', element: <OAuthPage /> },
  { path: '/quota', element: <QuotaPage /> },
  {
    path: '/usage-analytics',
    element: (
      <FeatureGate feature="requestMonitoring">
        <UsageAnalyticsPage />
      </FeatureGate>
    ),
  },
  { path: '/codex-inspection', element: <CodexInspectionPage /> },
  {
    path: '/codex-inspection/server',
    element: (
      <FeatureGate
        feature="serverCodexInspection"
        fallback={<ServerCodexInspectionRouteFallback />}
      >
        <ServerCodexInspectionPage />
      </FeatureGate>
    ),
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
      <FeatureGate feature="requestMonitoring">
        <MonitoringCenterPage />
      </FeatureGate>
    ),
  },
  {
    path: '/monitoring/account-actions',
    element: (
      <FeatureGate feature="requestMonitoring">
        <AccountActionCandidatesPage />
      </FeatureGate>
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
  { path: '/monitoring/codex-inspection', element: <Navigate to="/codex-inspection" replace /> },
  {
    path: '/monitoring/codex-inspection/server',
    element: (
      <FeatureGate feature="serverCodexInspection">
        <Navigate to="/codex-inspection/server" replace />
      </FeatureGate>
    ),
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
  {
    path: '/logs',
    element: (
      <LogsGate>
        <LogsPage />
      </LogsGate>
    ),
  },
  { path: '/system', element: <SystemPage /> },
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

export function MainRoutes({
  location,
  routeBase,
}: {
  location?: Location;
  routeBase?: string;
}) {
  const routeLocation = useMemo(
    () => ensureRouteLocationBase(location, routeBase),
    [location, routeBase]
  );

  return useRoutes(mainRoutes, routeLocation);
}
