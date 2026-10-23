import { useEffect, useRef, useState, type ReactNode } from 'react';
import { useLocation } from 'react-router-dom';
import { fullWebText as t } from '../i18n/fullweb';
import { FreshManagedKeysPage } from './FreshManagedKeysPage';
import { FreshGatewayWorkspace } from './FreshGatewayWorkspace';
import type { createPublicApiClient } from './publicApi';

// MainLayout's page transition unmounts routes. These editors live below the
// transition so navigating away cannot discard an unresolved write, its
// reconciliation ID, a one-time key secret, or unsaved draft configurations.
// They still unmount together when the verified administrator session ends.
function RetainedRoutePanel({
  active,
  name,
  children,
}: {
  active: boolean;
  name: string;
  children: ReactNode;
}) {
  const [visited, setVisited] = useState(active);
  if (active && !visited) setVisited(true);
  if (!active && !visited) return null;
  return (
    <div data-testid={`fresh-route-${name}`} hidden={!active}>
      {children}
    </div>
  );
}

export function FreshManagedResources({
  basePath,
  client,
  credential,
  onUnauthorized,
  onPending,
  onDirtyChange,
}: {
  basePath: string;
  client: ReturnType<typeof createPublicApiClient>;
  credential: string;
  onUnauthorized: () => void;
  onPending: (pending: boolean) => void;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { pathname: locationPath } = useLocation();
  const pathname = locationPath.replace(/\/+$/, '') || '/';
  const route =
    pathname === basePath
      ? '/'
      : pathname.startsWith(basePath + '/')
        ? pathname.slice(basePath.length)
        : pathname;
  const pending = useRef({ keys: false, retry: false });
  const [locks, setLocks] = useState({ keys: false, retry: false });

  function reportPending(resource: 'keys' | 'retry', value: boolean) {
    pending.current[resource] = value;
    const next = pending.current.keys || pending.current.retry;
    setLocks({ ...pending.current });
    onPending(next);
  }

  useEffect(() => {
    if (!locks.keys && !locks.retry) return;
    function beforeUnload(event: BeforeUnloadEvent) {
      event.preventDefault();
      event.returnValue = '';
    }
    window.addEventListener('beforeunload', beforeUnload);
    return () => window.removeEventListener('beforeunload', beforeUnload);
  }, [locks.keys, locks.retry]);

  return (
    <>
      {/* 客户端 API 密钥保留面板 */}
      <RetainedRoutePanel name="keys" active={route === '/api-keys'}>
        <FreshManagedKeysPage
          client={client}
          credential={credential}
          onUnauthorized={onUnauthorized}
          onPending={(value) => reportPending('keys', value)}
        />
      </RetainedRoutePanel>

      {/* 网关工作区保留面板：承接 /gateway 与兼容别名 /config */}
      <RetainedRoutePanel
        name="retry"
        active={route === '/config' || route === '/gateway'}
      >
        <FreshGatewayWorkspace
          onDirtyChange={onDirtyChange}
          client={client}
          credential={credential}
          onUnauthorized={onUnauthorized}
          onPending={(value) => reportPending('retry', value)}
        />
      </RetainedRoutePanel>

      {(locks.keys || locks.retry) && (
        <p className="alert alert-warn" role="status">
          {t(locks.keys ? 'keysExitBlocked' : 'retryNeedsReconciliation')}
        </p>
      )}
    </>
  );
}
