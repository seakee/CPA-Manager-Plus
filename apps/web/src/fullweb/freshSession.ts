import { refreshPanelFeatureAvailability } from '../hooks/usePanelFeatureAvailability';
import { apiClient } from '../services/api/client';
import { useAuthStore } from '../stores/useAuthStore';
import { useConfigStore } from '../stores/useConfigStore';
import { useModelsStore } from '../stores/useModelsStore';
import { useQuotaStore } from '../stores/useQuotaStore';
import { useUsageServiceStore } from '../stores/useUsageServiceStore';
import { STORAGE_KEY_AUTH } from '../utils/constants';
import type { RoutingContext } from './routingContext';

export function freshSessionBase(context: RoutingContext) {
  return new URL(`${context.apiBase}/cpamp`, window.location.origin).toString();
}

export function isConsoleUiEntryPath(
  basePath: string,
  pathname: string | undefined = typeof window !== 'undefined'
    ? window.location?.pathname
    : undefined
): boolean {
  const normalizedBase = basePath.replace(/\/+$/, '') || '/';
  const trimmedPath = (pathname || '').replace(/\/+$/, '') || '/';
  return trimmedPath === `${normalizedBase}/ui`;
}

export function resolveConsoleEntryPath(
  basePath: string,
  currentLocation: Pick<Location, 'pathname' | 'search' | 'hash'> | undefined =
    typeof window !== 'undefined' ? window.location : undefined
): string {
  const normalizedBase = basePath.replace(/\/+$/, '') || '/';
  if (!currentLocation || typeof currentLocation.pathname !== 'string') {
    return normalizedBase;
  }
  const trimmedPath = currentLocation.pathname.replace(/\/+$/, '') || '/';
  if (
    trimmedPath.startsWith(`${normalizedBase}/`) &&
    trimmedPath !== `${normalizedBase}/ui` &&
    trimmedPath !== `${normalizedBase}/technical-route`
  ) {
    return `${currentLocation.pathname}${currentLocation.search ?? ''}${currentLocation.hash ?? ''}`;
  }
  return normalizedBase;
}

// A stored credential is only a candidate. FreshConsolePage must verify it
// through the canonical operation endpoint before activating any session.
export function rememberedFreshCredential(context: RoutingContext) {
  const state = useAuthStore.getState();
  const base = freshSessionBase(context);
  return state.rememberPassword &&
    state.sessionMode === 'manager_embedded' &&
    state.apiBase === base &&
    state.sessionPanelBase === base
    ? state.managementKey
    : '';
}

export function activateFreshSession(
  context: RoutingContext,
  credential: string,
  rememberPassword: boolean
) {
  const apiBase = freshSessionBase(context);
  const state = useAuthStore.getState();
  if (state.apiBase !== apiBase || state.managementKey !== credential) {
    clearFreshSession();
  } else if (!state.isAuthenticated) {
    try {
      refreshPanelFeatureAvailability();
    } catch { /* Availability cache reset must not block session activation. */ }
  }
  apiClient.setConfig({ apiBase, managementKey: credential });
  const session = {
    isAuthenticated: true,
    apiBase,
    managementKey: credential,
    rememberPassword,
    sessionMode: 'manager_embedded' as const,
    sessionPanelBase: apiBase,
    connectionStatus: 'connected' as const,
    connectionError: null,
  };
  try {
    useAuthStore.setState(session);
  } catch {
    // Zustand updates memory before its storage adapter runs. If storage is
    // disabled/full, keep login usable without promising a remembered session.
    try {
      useAuthStore.setState({ ...session, rememberPassword: false });
    } catch { /* The in-memory session is already installed. */ }
  }
}

export function clearFreshSession() {
  try {
    refreshPanelFeatureAvailability();
  } catch { /* Availability cache reset must not block session revocation. */ }
  try {
    useAuthStore.getState().logout();
    return;
  } catch {
    // A cache's persist callback may throw before logout reaches auth state.
    // Clear each cache independently so another principal cannot inherit it.
    for (const clear of [
      () => useConfigStore.getState().clearCache(),
      () => useModelsStore.getState().clearCache(),
      () => useQuotaStore.getState().clearQuotaCache(),
      () => useUsageServiceStore.getState().clearUsageServiceConfig(),
    ]) {
      try { clear(); } catch { /* Cache memory is cleared before persistence. */ }
    }
    apiClient.setConfig({ apiBase: '', managementKey: '' });
    try {
      useAuthStore.setState({
        isAuthenticated: false,
        apiBase: '',
        managementKey: '',
        rememberPassword: false,
        sessionMode: '',
        sessionPanelBase: '',
        connectionStatus: 'disconnected',
        connectionError: null,
        supportsPlugin: false,
        serverVersion: null,
        serverCommit: null,
        serverBuildDate: null,
      });
    } catch { /* Revocation in memory must not depend on browser storage. */ }
    try {
      localStorage.removeItem(STORAGE_KEY_AUTH);
      localStorage.removeItem('isLoggedIn');
    } catch { /* Storage can be unavailable altogether. */ }
  }
}
