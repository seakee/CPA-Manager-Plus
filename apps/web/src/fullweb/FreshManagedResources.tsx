import { useEffect, useRef, useState, type ReactNode } from 'react';
import { useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import '../i18n';
import { fullWebLanguage, fullWebText as t, setFullWebLanguage } from '../i18n/fullweb';
import { FreshManagedKeysPage } from './FreshManagedKeysPage';
import { FreshGatewayWorkspace } from './FreshGatewayWorkspace';
import { FreshRebindDialog } from './FreshRebindDialog';
import { externalWorkspaceText as x } from '../i18n/freshExternal';
import type { createPublicApiClient } from './publicApi';

// MainLayout's page transition unmounts routes. These editors live below the
// transition so navigating away cannot discard an unresolved write, its
// reconciliation ID, a one-time key secret, or unsaved draft configurations.
// They still unmount together when the verified administrator session ends.
function RetainedRoutePanel({
  active,
  name,
  language,
  children,
}: {
  active: boolean;
  name: string;
  language: string;
  children: ReactNode;
}) {
  const [visited, setVisited] = useState(active);
  if (active && !visited) setVisited(true);
  if (!active && !visited) return null;
  return (
    <div data-testid={`fresh-route-${name}`} hidden={!active} lang={language}>
      {children}
    </div>
  );
}

export function FreshManagedResources({
  basePath,
  client,
  credential,
  strategy,
  onUnauthorized,
  onPending,
  onDirtyChange,
}: {
  basePath: string;
  client: ReturnType<typeof createPublicApiClient>;
  credential: string;
  /** The committed strategy, so External-only surfaces never mount elsewhere. */
  strategy?: string;
  onUnauthorized: () => void;
  onPending: (pending: boolean) => void;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { i18n } = useTranslation();
  const consoleLanguage = i18n.resolvedLanguage || i18n.language;
  const [language, setLanguage] = useState(fullWebLanguage);
  useEffect(() => {
    // Bridge the original Console's locale into retained Fresh editors without
    // remounting them or losing a draft, request identity or one-time secret.
    setFullWebLanguage(consoleLanguage);
    setLanguage(consoleLanguage);
  }, [consoleLanguage]);
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
  // An External installation gets the External panels: the typed blocker, the
  // rebind dialog and the usage confirmation. Every other profile keeps exactly
  // the surfaces it had.
  const externalProfile = strategy === 'connect_external';
  // One Rebind dialog for the whole Console: both the Keys page and the Gateway
  // page report the refused credential, and the way out is the same one. The
  // dialog is mounted here so switching pages mid-fix cannot drop it.
  const [rebindOpen, setRebindOpen] = useState(false);
  const [rebound, setRebound] = useState('');

  function reportPending(resource: 'keys' | 'retry', value: boolean) {
    if (pending.current[resource] === value) return;
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
      <RetainedRoutePanel name="keys" active={route === '/api-keys'} language={language}>
        <FreshManagedKeysPage
          visible={route === '/api-keys'}
          client={client}
          credential={credential}
          onUnauthorized={onUnauthorized}
          onPending={(value) => reportPending('keys', value)}
          onRebind={externalProfile ? () => setRebindOpen(true) : undefined}
          externalUsage={externalProfile}
        />
      </RetainedRoutePanel>

      {/* 网关工作区保留面板：承接 /gateway 与兼容别名 /config */}
      <RetainedRoutePanel
        name="retry"
        active={route === '/config' || route === '/gateway'}
        language={language}
      >
        <FreshGatewayWorkspace
          onDirtyChange={onDirtyChange}
          client={client}
          credential={credential}
          onUnauthorized={onUnauthorized}
          onPending={(value) => reportPending('retry', value)}
          onRebind={externalProfile ? () => setRebindOpen(true) : undefined}
        />
      </RetainedRoutePanel>

      {rebound && (
        <p className="alert alert-info" role="status" data-testid="rebind-recovered">
          {rebound}
        </p>
      )}
      {externalProfile && (
        <FreshRebindDialog
          open={rebindOpen}
          client={client}
          credential={credential}
          onClose={() => setRebindOpen(false)}
          onRebound={({ reverified }) => {
            setRebindOpen(false);
            setRebound(x(reverified ? 'rebindSucceededReverified' : 'rebindSucceededRebound'));
          }}
        />
      )}

      {(locks.keys || locks.retry) && (
        <p className="alert alert-warn" role="status">
          {t(locks.keys ? 'keysExitBlocked' : 'retryNeedsReconciliation')}
        </p>
      )}
    </>
  );
}
