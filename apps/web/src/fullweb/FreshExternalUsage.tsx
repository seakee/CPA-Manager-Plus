import { useEffect, useRef, useState } from 'react';
import { Modal } from '../components/ui/Modal';
import { FullWebIcon } from './FullWebIcon';
import {
  blockedReasonText,
  collectorStateText,
  externalWorkspaceText as x,
  versionSupportText,
} from '../i18n/freshExternal';
import { PublicApiError, type PublicApiClient } from './publicApi';
import {
  blockerOf,
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
  onBlocker?: (code: string | null) => void;
  /** Bumped by the shell after a successful rebind to re-read this panel. */
  reloadToken?: number;
}) {
  const [state, setState] = useState<ExternalUsageState | null>(null);
  const [readError, setReadError] = useState('');
  // A failed confirmation keeps the panel and its facts: replacing them with a
  // bare error would hide the state the operator is trying to change and leave no
  // way to ask again.
  const [confirmFailed, setConfirmFailed] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState('');
  const [startOnlyConfirm, setStartOnlyConfirm] = useState(false);
  const startOnlyRef = useRef(false);
  const requestId = useRef('');
  const alive = useRef(true);
  // Every usage-state response — a read or a confirmation — is published only
  // while its request still owns the panel. A `read()` opening a new generation
  // retires whatever was in flight: the answer it is replacing describes the
  // installation before the reload, so applying it would repaint a blocker the
  // rebind resolved or regress facts the newer read already settled. A
  // confirmation that succeeds takes the panel too, because the state it returns
  // is newer than any read still waiting on the pre-confirmation CPA.
  const generation = useRef(0);
  // The publication rule above must cover `busy` as well, but a confirmation the
  // operator can no longer see settled the panel once already: it still has to
  // release the button, so the release is counted instead of claimed by a ticket.
  const pendingConfirm = useRef(0);

  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  async function read() {
    // A read opens a generation: it is the newest statement of the installation's
    // state, so every response still in flight — read or confirmation — belongs to
    // the installation before it and is retired below.
    const ticket = ++generation.current;
    try {
      const parsed = parseExternalUsageState(
        await client.get<unknown>('external/usage', { kind: 'admin', value: credential })
      );
      if (!alive.current || generation.current !== ticket) return;
      if (!parsed) throw new Error('invalid usage state');
      setState(parsed);
      setReadError('');
      if (
        !parsed.blockedReason ||
        parsed.blockedReason === 'usage_monitoring_disabled' ||
        !blockerOf(parsed.blockedReason)
      ) {
        onBlocker?.(null);
      }
    } catch (err) {
      if (!alive.current || generation.current !== ticket) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        onUnauthorized();
        return;
      }
      setState(null);
      if (err instanceof PublicApiError && err.code) onBlocker?.(err.code);
      setReadError(x('usageReadFailed'));
    }
  }

  // Mounted once, so a rebind that resolves the blocker has to re-read here too:
  // otherwise the panel keeps the refusal it saw before the fix.
  useEffect(() => {
    void read();
  }, [reloadToken]); // eslint-disable-line react-hooks/exhaustive-deps

  async function confirmEnable() {
    if (busy) return;
    const startOnly = startOnlyRef.current;
    // A confirmation outlives a read request: it writes to the CPA, so the state
    // it returns is the newest fact this panel holds. The `busy` release is
    // counted rather than reported, because a confirmation the operator can no
    // longer see was already settled by the reload that retired it.
    const ticket = generation.current;
    pendingConfirm.current += 1;
    setBusy(true);
    setNotice('');
    setConfirmFailed(false);
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
      if (generation.current !== ticket) return;
      // This answer is newer than any read still waiting on the pre-confirmation
      // installation, so publishing it retires them.
      generation.current += 1;
      const blockedReason = parsed.blockedReason;
      setState(parsed);
      setConfirming(false);
      if (blockedReason && blockedReason !== 'usage_monitoring_disabled') {
        onBlocker?.(blockedReason);
        setConfirmFailed(true);
      } else {
        onBlocker?.(null);
        requestId.current = '';
        setConfirmFailed(false);
        if (parsed.statisticsEnabled && !blockedReason) {
          setNotice(x(startOnly ? 'usageStarted' : 'usageConfirmed'));
        }
      }
    } catch (err) {
      if (!alive.current || generation.current !== ticket) return;
      // This failure is the newest fact the panel holds, so it retires the reads
      // still waiting on the pre-confirmation installation: a read that reports
      // the state the failed confirmation was trying to change would clear the
      // blocker that failure just named.
      generation.current += 1;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        onUnauthorized();
        return;
      }
      if (err instanceof PublicApiError && err.code) onBlocker?.(err.code);
      setConfirming(false);
      // Keep the request ID across retries of this confirmation attempt; whether
      // CPA receives another PUT depends on whether statistics still read back off.
      setConfirmFailed(true);
    } finally {
      pendingConfirm.current -= 1;
      if (alive.current && pendingConfirm.current === 0) setBusy(false);
    }
  }

  if (readError) {
    return (
      <section className="key-notice key-notice-warning" role="alert" data-testid="external-usage">
        <FullWebIcon name="alert" />
        <span>{readError}</span>
        <button
          className="btn btn-secondary btn-sm"
          type="button"
          data-testid="external-usage-reload"
          onClick={() => void read()}
        >
          {x('usageConfirmRetry')}
        </button>
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
          <dd>{collectorStateText(state.collector)}</dd>
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
            <dd>{blockedReasonText(state.blockedReason)}</dd>
          </div>
        )}
      </dl>
      {notice && (
        <p role="status" data-testid="external-usage-notice">
          {notice}
        </p>
      )}
      {confirmFailed && (
        <p className="key-notice key-notice-warning" role="alert" data-testid="external-usage-confirm-failed">
          <FullWebIcon name="alert" />
          <span>{x('usageEnableFailed')}</span>
          <button
            className="btn btn-secondary btn-sm"
            type="button"
            disabled={busy}
            data-testid="external-usage-confirm-retry"
            onClick={() => void confirmEnable()}
          >
            {x('usageConfirmRetry')}
          </button>
        </p>
      )}
      {(!state.statisticsEnabled ||
        (!state.collectionStarted && state.collector === 'stopped')) &&
        !confirmFailed &&
        !state.cpaVersion.writeBlocked &&
        state.blockedReason !== 'usage_monitoring_disabled' && (
          <button
            className="btn btn-primary btn-sm"
            data-testid="external-usage-enable"
            disabled={busy}
            onClick={() => {
              startOnlyRef.current = state.statisticsEnabled;
              setStartOnlyConfirm(state.statisticsEnabled);
              setConfirming(true);
            }}
          >
            {x(state.statisticsEnabled ? 'usageStartAction' : 'usageEnableAction')}
          </button>
        )}
      <Modal
        open={confirming}
        title={x(startOnlyConfirm ? 'usageStartConfirmTitle' : 'usageConfirmTitle')}
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
              {x(startOnlyConfirm ? 'usageStartConfirmAction' : 'usageConfirmAction')}
            </button>
          </>
        }
      >
        <p>{x(startOnlyConfirm ? 'usageStartConfirmHelp' : 'usageConfirmHelp')}</p>
      </Modal>
    </section>
  );
}
