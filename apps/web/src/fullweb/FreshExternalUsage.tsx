import { useEffect, useRef, useState } from 'react';
import { Modal } from '../components/ui/Modal';
import { FullWebIcon } from './FullWebIcon';
import { externalWorkspaceText as x, versionSupportText } from '../i18n/freshExternal';
import { PublicApiError, type PublicApiClient } from './publicApi';
import {
  newExternalRequestId,
  parseExternalUsageState,
  type ExternalUsageState,
} from './externalState';


// A Collector that never started is not coverage with zero calls: the page says
// "not started" rather than presenting an unexplained empty window.
function collectionStartedText(state: ExternalUsageState): string {
  return state.collectionStarted ? x('usageCollectionStarted') : x('usageNotStarted');
}

/**
 * The External usage panel. It reads the state, and enabling statistics is a
 * two-step gate: the panel only opens a confirmation, and the POST that writes
 * `usage-statistics-enabled: true` to the user's own CPA happens after an
 * explicit confirm. Nothing here writes on a render or a refresh.
 */
export function FreshExternalUsage({
  client,
  credential,
  onUnauthorized,
  onBlocker,
  reloadToken = 0,
}: {
  client: PublicApiClient;
  credential: string;
  onUnauthorized: () => void;
  onBlocker?: (code: string) => void;
  /** Bumped by the shell after a successful rebind to re-read this panel. */
  reloadToken?: number;
}) {
  const [state, setState] = useState<ExternalUsageState | null>(null);
  const [error, setError] = useState('');
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState('');
  const requestId = useRef('');
  const alive = useRef(true);

  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  async function read() {
    try {
      const parsed = parseExternalUsageState(
        await client.get<unknown>('external/usage', { kind: 'admin', value: credential })
      );
      if (!alive.current) return;
      if (!parsed) throw new Error('invalid usage state');
      setState(parsed);
      setError('');
    } catch (err) {
      if (!alive.current) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        onUnauthorized();
        return;
      }
      setState(null);
      if (err instanceof PublicApiError && err.code) onBlocker?.(err.code);
      setError(x('usageReadFailed'));
    }
  }

  // Mounted once, so a rebind that resolves the blocker has to re-read here too:
  // otherwise the panel keeps the refusal it saw before the fix.
  useEffect(() => {
    void read();
  }, [reloadToken]); // eslint-disable-line react-hooks/exhaustive-deps

  async function confirmEnable() {
    if (busy) return;
    setBusy(true);
    setNotice('');
    if (!requestId.current) requestId.current = newExternalRequestId();
    try {
      const parsed = parseExternalUsageState(
        await client.post<unknown>(
          'external/usage',
          { requestId: requestId.current, confirmEnableStatistics: true },
          { kind: 'admin', value: credential }
        )
      );
      if (!alive.current) return;
      if (!parsed) throw new Error('invalid usage state');
      requestId.current = '';
      setState(parsed);
      setConfirming(false);
      setNotice(x('usageConfirmed'));
    } catch (err) {
      if (!alive.current) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        onUnauthorized();
        return;
      }
      setConfirming(false);
      setError(x('usageEnableFailed'));
    } finally {
      if (alive.current) setBusy(false);
    }
  }

  if (error) {
    return (
      <section className="key-notice key-notice-warning" role="alert" data-testid="external-usage">
        <FullWebIcon name="alert" />
        <span>{error}</span>
      </section>
    );
  }
  if (!state) {
    return (
      <section className="key-notice" role="status" data-testid="external-usage">
        <span>{x('usageTitle')}</span>
      </section>
    );
  }
  return (
    <section className="fresh-external-usage" data-testid="external-usage">
      <header className="fresh-external-usage-head">
        <h2>{x('usageTitle')}</h2>
        <p className="key-field-help">{x('usageHelp')}</p>
      </header>
      <dl className="fresh-external-usage-facts">
        <div data-testid="external-usage-statistics">
          <dt>{x('usageStatistics')}</dt>
          <dd>{state.statisticsEnabled ? x('usageEnabled') : x('usageDisabled')}</dd>
        </div>
        <div data-testid="external-usage-collector">
          <dt>{x('usageCollector')}</dt>
          <dd>{state.collector || x('usageNotStarted')}</dd>
        </div>
        <div data-testid="external-usage-started">
          <dt>{x('usageCollectionStarted')}</dt>
          <dd>{collectionStartedText(state)}</dd>
        </div>
        <div data-testid="external-usage-version">
          <dt>{x('usageVersion')}</dt>
          <dd>
            {state.cpaVersion.version || x('blockerVersionUnknown')} ·{' '}
            {versionSupportText(state.cpaVersion.support)} ·{' '}
            {state.cpaVersion.writeBlocked ? x('usageWritesBlocked') : x('usageWritesAllowed')}
          </dd>
        </div>
        {state.blockedReason && (
          <div data-testid="external-usage-blocked">
            <dt>{x('usageBlockedReason')}</dt>
            <dd>
              <code>{state.blockedReason}</code>
            </dd>
          </div>
        )}
      </dl>
      {notice && (
        <p role="status" data-testid="external-usage-notice">
          {notice}
        </p>
      )}
      {!state.statisticsEnabled && (
        <button
          className="btn btn-primary btn-sm"
          data-testid="external-usage-enable"
          disabled={busy}
          onClick={() => setConfirming(true)}
        >
          {x('usageEnableAction')}
        </button>
      )}
      <Modal
        open={confirming}
        title={x('usageConfirmTitle')}
        className="key-create-modal"
        closeDisabled={busy}
        onClose={() => {
          if (!busy) setConfirming(false);
        }}
        footer={
          <>
            <button
              className="btn btn-secondary"
              type="button"
              disabled={busy}
              data-testid="external-usage-cancel"
              onClick={() => setConfirming(false)}
            >
              {x('usageConfirmCancel')}
            </button>
            <button
              className="btn btn-primary"
              type="button"
              disabled={busy}
              data-testid="external-usage-confirm"
              onClick={() => void confirmEnable()}
            >
              {x('usageConfirmAction')}
            </button>
          </>
        }
      >
        <p>{x('usageConfirmHelp')}</p>
      </Modal>
    </section>
  );
}
