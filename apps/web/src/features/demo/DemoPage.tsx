import { useLayoutEffect } from 'react';
import { MainLayout } from '@/components/layout/MainLayout';
import { apiClient } from '@/services/api/client';
import { normalizeConfigResponse } from '@/services/api/transformers';
import {
  useAuthStore,
  useConfigStore,
  useModelsStore,
  useUsageServiceStore,
} from '@/stores';
import { DemoRouteAdapter } from './DemoRouteAdapter';
import { getDemoProviderModels, getDemoRawConfig } from '@/features/demo/demoFixtures';
import {
  DEMO_API_BASE,
  DEMO_MANAGEMENT_KEY,
  DEMO_ROUTE_BASE,
  DEMO_SERVER_VERSION,
  getDemoServerBuildDate,
  setDemoMode,
} from './demoMode';
import { enableDemoPersistIsolation } from './demoPersistIsolation';

type AuthStoreState = ReturnType<typeof useAuthStore.getState>;
type ConfigStoreState = ReturnType<typeof useConfigStore.getState>;
type ModelsStoreState = ReturnType<typeof useModelsStore.getState>;
type UsageServiceStoreState = ReturnType<typeof useUsageServiceStore.getState>;

const createDemoConfigCache = (config: ReturnType<typeof normalizeConfigResponse>) => {
  const cache = new Map<string, { data: unknown; timestamp: number }>();
  cache.set('__full__', { data: config, timestamp: Date.now() });
  return cache;
};

const captureAuthSnapshot = (state: AuthStoreState) => ({
  isAuthenticated: state.isAuthenticated,
  apiBase: state.apiBase,
  managementKey: state.managementKey,
  rememberPassword: state.rememberPassword,
  serverVersion: state.serverVersion,
  serverBuildDate: state.serverBuildDate,
  supportsPlugin: state.supportsPlugin,
  sessionMode: state.sessionMode,
  sessionPanelBase: state.sessionPanelBase,
  connectionStatus: state.connectionStatus,
  connectionError: state.connectionError,
});

const captureConfigSnapshot = (state: ConfigStoreState) => ({
  config: state.config,
  cache: state.cache,
  loading: state.loading,
  error: state.error,
});

const captureModelsSnapshot = (state: ModelsStoreState) => ({
  models: state.models,
  loading: state.loading,
  error: state.error,
  cache: state.cache,
});

const captureUsageServiceSnapshot = (state: UsageServiceStoreState) => ({
  enabled: state.enabled,
  serviceBase: state.serviceBase,
  panelBase: state.panelBase,
  panelHostMode: state.panelHostMode,
  revision: state.revision,
});

export function DemoPage() {
  useLayoutEffect(() => {
    const authSnapshot = captureAuthSnapshot(useAuthStore.getState());
    const configSnapshot = captureConfigSnapshot(useConfigStore.getState());
    const modelsSnapshot = captureModelsSnapshot(useModelsStore.getState());
    const usageServiceSnapshot = captureUsageServiceSnapshot(useUsageServiceStore.getState());
    const loggedInSnapshot =
      typeof window !== 'undefined' ? window.localStorage.getItem('isLoggedIn') : null;
    const hadLoggedInFlag =
      typeof window !== 'undefined' && window.localStorage.getItem('isLoggedIn') !== null;

    const demoConfig = normalizeConfigResponse(getDemoRawConfig());
    const demoModels = getDemoProviderModels();
    const restoreDemoPersistIsolation = enableDemoPersistIsolation();

    setDemoMode(true);
    apiClient.setConfig({
      apiBase: DEMO_API_BASE,
      managementKey: DEMO_MANAGEMENT_KEY,
    });
    useAuthStore.setState({
      isAuthenticated: true,
      apiBase: DEMO_API_BASE,
      managementKey: DEMO_MANAGEMENT_KEY,
      rememberPassword: false,
      serverVersion: DEMO_SERVER_VERSION,
      serverBuildDate: getDemoServerBuildDate(),
      supportsPlugin: true,
      sessionMode: 'manager_embedded',
      sessionPanelBase: DEMO_API_BASE,
      connectionStatus: 'connected',
      connectionError: null,
    });
    useConfigStore.setState({
      config: demoConfig,
      cache: createDemoConfigCache(demoConfig),
      loading: false,
      error: null,
    });
    useModelsStore.setState({
      models: demoModels,
      loading: false,
      error: null,
      cache: {
        data: demoModels,
        timestamp: Date.now(),
        apiBase: DEMO_API_BASE,
        apiKey: '',
      },
    });
    useUsageServiceStore.setState((state) => ({
      enabled: true,
      serviceBase: DEMO_API_BASE,
      panelBase: DEMO_API_BASE,
      panelHostMode: 'manager_embedded',
      revision: state.revision + 1,
    }));

    return () => {
      setDemoMode(false);
      useAuthStore.setState(authSnapshot);
      useConfigStore.setState(configSnapshot);
      useModelsStore.setState(modelsSnapshot);
      useUsageServiceStore.setState(usageServiceSnapshot);
      apiClient.setConfig({
        apiBase: authSnapshot.apiBase,
        managementKey: authSnapshot.managementKey,
      });
      if (typeof window !== 'undefined') {
        if (hadLoggedInFlag && loggedInSnapshot !== null) {
          window.localStorage.setItem('isLoggedIn', loggedInSnapshot);
        } else {
          window.localStorage.removeItem('isLoggedIn');
        }
      }
      restoreDemoPersistIsolation();
    };
  }, []);

  return (
    <DemoRouteAdapter routeBase={DEMO_ROUTE_BASE}>
      <MainLayout routeBase={DEMO_ROUTE_BASE} demoMode />
    </DemoRouteAdapter>
  );
}
