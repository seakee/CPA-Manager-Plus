import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import {
  BrowserRouter,
  createBrowserRouter,
  MemoryRouter,
  RouterProvider,
  useInRouterContext,
} from 'react-router-dom';
import '../i18n';
import { AppLifecycle } from '../app/AppLifecycle';
import { MainLayout } from '../components/layout/MainLayout';
import { LoadingSpinner } from '../components/ui/LoadingSpinner';
import { ConfirmationModal } from '../components/common/ConfirmationModal';
import { NotificationContainer } from '../components/common/NotificationContainer';
import { ManagerUpdates } from '../features/system/ManagerUpdates';
import { SystemPage } from '../features/system/SystemPage';
import { FreshSystemWorkspace } from './FreshSystemWorkspace';
import { useAuthStore } from '../stores';
import { FreshManagedResources } from './FreshManagedResources';
import type { createPublicApiClient } from './publicApi';
import { resolveRoutingContext, type RoutingContext } from './routingContext';
import { activateFreshSession } from './freshSession';

// The original CPAMP pages use data-router APIs (for example useBlocker on
// AccountsPage). Keep the route element behind a small context bridge so the
// data router can be created once for the browser while the surrounding Fresh
// session can still update logout/pending state without replacing the router.
const legacyConsoleContentContext = createContext<ReactNode>(null);

function LegacyConsoleRoute() {
  return useContext(legacyConsoleContentContext);
}

// The legacy Console is deliberately a post-installation seam.  This module
// is loaded only after Fresh has authenticated the administrator and observed
// ProductComplete, so its auth/config stores cannot become an installation
// discovery or pre-admission transport.
export function LegacyConsolePage({
  context,
  credential,
  onUnauthorized,
  client,
}: {
  context: RoutingContext;
  credential: string;
  onUnauthorized: () => void;
  client?: ReturnType<typeof createPublicApiClient>;
}) {
  // Keep this hook unconditional: the legacy page can render once while the
  // in-memory credential is being installed, so router context must be read
  // before any early loading/unauthorized return.
  const inRouter = useInRouterContext();
  const [activated, setActivated] = useState(false);
  const [freshPending, setFreshPending] = useState(false);
  const [freshDirty, setFreshDirty] = useState(false);
  const onUnauthorizedRef = useRef(onUnauthorized);

  useEffect(() => {
    onUnauthorizedRef.current = onUnauthorized;
  }, [onUnauthorized]);

  useEffect(() => {
    const verified = resolveRoutingContext({
      state: 'verified_effective',
      verifiedEffectiveControlBasePath: context.basePath,
      revision: context.revision,
    });
    if (!verified || !credential) {
      onUnauthorizedRef.current();
      return;
    }

    // The canonical Fresh endpoint already verified this credential. Reuse the
    // original session without clearing the user's remember-login choice.
    activateFreshSession(verified, credential, useAuthStore.getState().rememberPassword);
    // The store update and this local gate must both settle before mounting the
    // legacy tree. This is an intentional effect-to-view transition: the
    // credential has just been verified by the canonical Fresh boundary.
    setActivated(true); // eslint-disable-line react-hooks/set-state-in-effect

    return () => {
      setActivated(false);
    };
  }, [context.basePath, context.revision, credential]);

  const authenticated = useAuthStore((state) => state.isAuthenticated);
  useEffect(() => {
    if (activated && !authenticated) onUnauthorizedRef.current();
  }, [activated, authenticated]);

  const browserRouter = useMemo(() => {
    const hasDOM =
      typeof window !== 'undefined' &&
      typeof window.history?.replaceState === 'function' &&
      typeof document !== 'undefined' &&
      typeof document.createElement === 'function';
    if (!activated || !authenticated || inRouter || !hasDOM) return null;
    return createBrowserRouter([{ path: '*', element: <LegacyConsoleRoute /> }], {
      basename: context.basePath,
    });
  }, [activated, authenticated, context.basePath, inRouter]);

  useEffect(() => () => browserRouter?.dispose(), [browserRouter]);

  if (!activated || !authenticated) {
    return (
      <div className="main-content" role="status">
        <LoadingSpinner />
      </div>
    );
  }

  const content = (
    <div data-testid="legacy-console">
      <AppLifecycle />
      <NotificationContainer />
      <ConfirmationModal />
      <ManagerUpdates>
        {/*
         * The Full Web router is mounted below the embedded `/console` base.
         * MainLayout already supports an explicit route base (used by the demo
         * shell); passing it here keeps its active item, breadcrumbs and
         * MainRoutes in the same coordinate system as the mounted router.
         */}
        <MainLayout
          routeBase={context.basePath}
          navigationBase={context.basePath}
          logoutDisabled={freshPending}
          hasUnsavedChanges={freshDirty}
          contentFooter={
            client ? (
              <FreshManagedResources
                basePath={context.basePath}
                client={client}
                credential={credential}
                onUnauthorized={onUnauthorized}
                onPending={setFreshPending}
                onDirtyChange={setFreshDirty}
              />
            ) : undefined
          }
          renderRoute={(pathname) => {
            if (!client) return undefined;
            const route = pathname.replace(/\/+$/, '') || '/';
            // In Full Web, API keys, Gateway workspace and System configuration
            // render in the persistent footer panels (RetainedRoutePanel) so that
            // active drafts, one-time secrets and in-flight operations are
            // retained across page transitions.
            if (route === '/api-keys') return null;
            if (route === '/gateway' || route === '/config') return null;
            if (route === '/system') {
              return (
                <SystemPage
                  logoutDisabled={freshPending}
                  hasUnsavedChanges={freshDirty}
                  workspace={<FreshSystemWorkspace basePath={context.basePath} />}
                />
              );
            }
            return undefined;
          }}
        />
      </ManagerUpdates>
    </div>
  );
  // The production Full Web entry normally supplies the BrowserRouter from
  // main.tsx. Keep a browser-backed fallback for direct mounts and embedded
  // hosts that render this seam without that provider: a MemoryRouter would
  // make MainLayout links change only an in-memory location, leaving the
  // address bar stuck at /console and making refresh/deep links impossible.
  if (inRouter) return content;
  // Non-DOM unit mounts provide only a minimal window stub; retain an
  // in-memory harness there while real browser mounts always get URL-backed
  // navigation.
  if (typeof window === 'undefined' || typeof window.history?.replaceState !== 'function') {
    return (
      <MemoryRouter basename={context.basePath} initialEntries={[context.basePath]}>
        {content}
      </MemoryRouter>
    );
  }
  if (!browserRouter) return <BrowserRouter basename={context.basePath}>{content}</BrowserRouter>;
  return (
    <legacyConsoleContentContext.Provider value={content}>
      <RouterProvider router={browserRouter} />
    </legacyConsoleContentContext.Provider>
  );
}
