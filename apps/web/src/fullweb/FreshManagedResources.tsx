import { useEffect, useRef, useState, type ReactNode } from 'react';
import { useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import '../i18n';
import { fullWebLanguage, fullWebText as t, setFullWebLanguage } from '../i18n/fullweb';
import { workspaceText as w } from '../i18n/consoleWorkspace';
import { FreshManagedKeysPage } from './FreshManagedKeysPage';
import { FreshRetryConfiguration } from './FreshRetryConfiguration';
import type { createPublicApiClient } from './publicApi';

// MainLayout's page transition unmounts routes. These two editors instead live
// below the transition so navigating away cannot discard an unresolved write,
// its reconciliation ID, or a one-time key secret. They still unmount together
// when the verified administrator session ends.
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
  onUnauthorized,
  onPending,
}: {
  basePath: string;
  client: ReturnType<typeof createPublicApiClient>;
  credential: string;
  onUnauthorized: () => void;
  onPending: (pending: boolean) => void;
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
      <RetainedRoutePanel name="keys" active={route === '/api-keys'} language={language}>
        <FreshManagedKeysPage
          visible={route === '/api-keys'}
          client={client}
          credential={credential}
          onUnauthorized={onUnauthorized}
          onPending={(value) => reportPending('keys', value)}
        />
      </RetainedRoutePanel>
      <RetainedRoutePanel name="retry" active={route === '/config'} language={language}>
        <section className="card fresh-managed-resource" data-testid="fresh-managed-retry">
          <div className="fresh-managed-resource-heading">
            <div>
              <h2>{w('connection')}</h2>
              <p>{w('configurationHelp')}</p>
            </div>
            <span className="badge">{w('stored')}</span>
          </div>
          <FreshRetryConfiguration
            client={client}
            credential={credential}
            onUnauthorized={onUnauthorized}
            onPending={(value) => reportPending('retry', value)}
          />
        </section>
      </RetainedRoutePanel>
      {(locks.keys || locks.retry) && (
        <p className="alert alert-warn" role="status">
          {t(locks.keys ? 'keysExitBlocked' : 'retryNeedsReconciliation')}
        </p>
      )}
    </>
  );
}
