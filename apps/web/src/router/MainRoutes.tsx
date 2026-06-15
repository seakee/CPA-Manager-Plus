import { useEffect, useRef, useState, type ReactElement } from 'react';
import { Navigate, useLocation, useRoutes, type Location } from 'react-router-dom';
import { DashboardPage } from '@/pages/DashboardPage';
import { AiProvidersPage } from '@/pages/AiProvidersPage';
import { AiProvidersAmpcodeEditPage } from '@/pages/AiProvidersAmpcodeEditPage';
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
import { MonitoringCenterPage } from '@/pages/MonitoringCenterPage';
import { AccountActionCandidatesPage } from '@/pages/AccountActionCandidatesPage';
import { ModelPricesPage } from '@/pages/ModelPricesPage';
import { CodexInspectionPage } from '@/pages/CodexInspectionPage';
import { ServerCodexInspectionPage } from '@/pages/ServerCodexInspectionPage';
import { PluginsPage } from '@/pages/PluginsPage';
import { PluginStorePage } from '@/pages/PluginStorePage';
import { ConfigPage } from '@/pages/ConfigPage';
import { LogsPage } from '@/pages/LogsPage';
import { SystemPage } from '@/pages/SystemPage';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { CodexInspectionModeTabs } from '@/features/monitoring/components/CodexInspectionModeTabs';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { isLogsRouteAvailable } from '@/features/logs/logFeatureAvailability';
import { useConfigStore } from '@/stores';
import codexInspectionStyles from '@/features/monitoring/CodexInspectionPage.module.scss';

type FeatureKey = 'requestMonitoring' | 'modelPrices' | 'serverCodexInspection';

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

const mainRoutes = [
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
  { path: '/ai-providers/ampcode', element: <AiProvidersAmpcodeEditPage /> },
  { path: '/ai-providers', element: <AiProvidersPage /> },
  { path: '/ai-providers/*', element: <AiProvidersPage /> },
  { path: '/auth-files', element: <AuthFilesPage /> },
  { path: '/auth-files/oauth-excluded', element: <AuthFilesOAuthExcludedEditPage /> },
  { path: '/auth-files/oauth-model-alias', element: <AuthFilesOAuthModelAliasEditPage /> },
  { path: '/oauth', element: <OAuthPage /> },
  { path: '/quota', element: <QuotaPage /> },
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
  { path: '/config', element: <ConfigPage /> },
  { path: '/plugins', element: <PluginsPage /> },
  { path: '/plugins-store', element: <PluginStorePage /> },
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

export function MainRoutes({ location }: { location?: Location }) {
  return useRoutes(mainRoutes, location);
}
