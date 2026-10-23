import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { FullWebLoginPage, FullWebUpgradeHeader } from './FullWebLoginPage';
import { FullWebIcon } from './FullWebIcon';
import { upgradeEntryText as text } from '../i18n/upgradeEntry';
import './upgradeEntry.css';
import {
  rememberUpgradeCredential,
  rememberedUpgradeCredential,
  UpgradeAuthenticationError,
  verifyUpgradeCredential,
} from './upgradeSession';

export function UpgradeAuthenticationEntry() {
  const { i18n } = useTranslation();
  const language = i18n.resolvedLanguage || i18n.language;
  const [revision, setRevision] = useState('');
  const [error, setError] = useState<UpgradeAuthenticationError['reason'] | 'expired' | ''>('');
  const [checking, setChecking] = useState(false);
  const heading = useRef<HTMLHeadingElement>(null);
  const credential = useRef('');
  const request = useRef<AbortController | null>(null);
  const sequence = useRef(0);
  const restored = useRef(false);
  const expiresAt = useRef(0);

  const revoke = useCallback(
    (message: UpgradeAuthenticationError['reason'] | 'expired' | '' = '') => {
      ++sequence.current;
      request.current?.abort();
      credential.current = '';
      expiresAt.current = 0;
      setRevision('');
      setChecking(false);
      setError(message);
    },
    []
  );

  const authenticate = useCallback(
    async (key: string, remember: boolean, source?: string) => {
      const current = ++sequence.current;
      request.current?.abort();
      const controller = new AbortController();
      request.current = controller;
      // Renewal keeps the shell mounted, but hides the previous observation
      // and disables its actions until the server has verified it again.
      if (!source) setRevision('');
      setChecking(true);
      setError('');
      const timeout = window.setTimeout(() => controller.abort(), 10_000);
      try {
        const next = await verifyUpgradeCredential(key, controller.signal, source);
        if (sequence.current !== current || controller.signal.aborted) return false;
        credential.current = key;
        expiresAt.current = Date.now() + 60_000;
        if (!source) rememberUpgradeCredential(key, remember);
        setRevision(next);
        return true;
      } catch (err) {
        if (sequence.current === current)
          revoke(err instanceof UpgradeAuthenticationError ? err.reason : 'network');
        return false;
      } finally {
        window.clearTimeout(timeout);
        if (sequence.current === current) setChecking(false);
      }
    },
    [revoke]
  );

  const restore = useCallback(async () => {
    if (restored.current) return;
    restored.current = true;
    const candidate = rememberedUpgradeCredential();
    if (candidate) await authenticate(candidate, true);
  }, [authenticate]);

  const adapter = useMemo(
    () => ({
      address: window.location.origin,
      error: error ? text(error, language) : '',
      status: checking ? text('checking', language) : undefined,
      connectionHint: text('loginHint', language),
      credentialPlaceholder: text('placeholder', language),
      recoveryHelp: {
        label: text('recoveryLabel', language),
        description: text('recoveryHelp', language),
      },
      restore,
      authenticate: (key: string, remember: boolean) => authenticate(key, remember),
    }),
    [authenticate, checking, error, language, restore]
  );

  const inJourney = Boolean(revision);
  useEffect(() => {
    // Entering the journey moves focus once. Background checks and appearance
    // changes preserve the user's current focus.
    if (inJourney) heading.current?.focus();
  }, [inJourney]);

  useEffect(
    () => () => {
      ++sequence.current;
      request.current?.abort();
      credential.current = '';
    },
    []
  );

  useEffect(() => {
    if (!revision) return;
    const refresh = () => {
      if (Date.now() >= expiresAt.current) {
        revoke('expired');
        return;
      }
      void authenticate(credential.current, false, revision);
    };
    const timer = checking ? undefined : window.setTimeout(refresh, 30_000);
    // BFCache/background returns must never reuse an expired observation.
    const visible = () => {
      if (document.visibilityState === 'visible') refresh();
    };
    const pagehide = () => revoke();
    document.addEventListener('visibilitychange', visible);
    window.addEventListener('pagehide', pagehide);
    return () => {
      window.clearTimeout(timer);
      document.removeEventListener('visibilitychange', visible);
      window.removeEventListener('pagehide', pagehide);
    };
  }, [authenticate, checking, revision, revoke]);

  if (!revision) return <FullWebLoginPage adapter={adapter} />;

  return (
    <div className="upgrade-entry" data-testid="upgrade-shell">
      <FullWebUpgradeHeader />
      <main
        className="upgrade-main"
        data-testid={checking ? 'upgrade-revalidating' : 'upgrade-authenticated'}
        aria-busy={checking}
      >
        <section className="upgrade-card" aria-labelledby="upgrade-title">
          <div className="upgrade-body">
            <div className="upgrade-heading">
              <h1 id="upgrade-title" ref={heading} tabIndex={-1}>
                {text('title', language)}
              </h1>
              <span className="badge">{text('mode', language)}</span>
              <span className={checking ? 'badge' : 'badge badge-emerald'}>
                <FullWebIcon name={checking ? 'refresh' : 'check'} size={16} />
                {text(checking ? 'checkingShort' : 'verified', language)}
              </span>
            </div>
            <p className="upgrade-subtitle">{text('retained', language)}</p>
            <div className="upgrade-notice" role="status" aria-live="polite">
              <FullWebIcon name={checking ? 'refresh' : 'alert'} size={24} />
              <div>
                <h2>{text(checking ? 'checking' : 'blocked', language)}</h2>
                <p>{text(checking ? 'checkingHelp' : 'boundary', language)}</p>
              </div>
            </div>
            <p className="upgrade-next">{text('next', language)}</p>
          </div>
          <div className="upgrade-footer">
            <button className="btn btn-secondary" onClick={() => revoke()}>
              {text('logout', language)}
            </button>
            <button
              className="btn btn-primary"
              disabled={checking}
              onClick={() => void authenticate(credential.current, false, revision)}
            >
              <FullWebIcon name="refresh" /> {text('verify', language)}
            </button>
          </div>
        </section>
      </main>
      <footer className="journey-copyright">{text('footer', language)}</footer>
    </div>
  );
}
