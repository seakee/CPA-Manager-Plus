import { useCallback, useEffect, useRef, useState } from 'react';
import {
  adoptBlockerText,
  adoptReasonText,
  adoptResourceText,
  adoptText,
} from '../i18n/freshAdopt';
import { shellText as s } from '../i18n/consoleShell';
import {
  BootstrapTransportError,
  type FreshOperation,
  type FreshStrategyCheckpoint,
} from './bootstrapTransport';
import {
  adoptStrategyBody,
  groupAdoptionItems,
  normalizeCPAAddress,
  validManagementKey,
  type AdoptionDecision,
  type AdoptionSummary,
  type AdoptRuntimeTarget,
} from './freshAdoption';
import { PublicApiError } from './publicApi';
import { FullWebIcon } from './FullWebIcon';

// The Adopt flow's two carriers. The wizard before routing exists and the Console
// after it hold the same operation but not the same transport, so the flow talks
// to this small interface and each shell binds it once.
export interface AdoptCarrier {
  /** Read the durable operation, which is the reconciliation after any write. */
  readOperation(): Promise<FreshOperation>;
  /** Observe the CPA this Manager runs: the destination of the adoption. */
  observeTarget(): Promise<AdoptRuntimeTarget>;
  /** The two-POST inventory: staged first, committed second. */
  writeStrategy(body: Record<string, unknown>): Promise<FreshOperation>;
  /** Commit the canonical route before completion. Bootstrap-only. */
  prepareRoute?(): Promise<FreshOperation>;
  complete(body: { acceptRetryable?: true }): Promise<FreshOperation>;
  /** Read the recorded per-item migration summary. */
  readAdoption(): Promise<AdoptionSummary>;
}

type Stage = 'source' | 'inventory' | 'migration';

// The polling cadence: two seconds while a run settles, backing off when the
// Manager cannot answer. The cap keeps a slow Manager from being polled harder
// than a healthy one.
const POLL_INTERVAL = 2000;
const POLL_INTERVAL_MAX = 8000;
// A read that keeps failing is a state the screen has to state, not a spinner
// that spins forever. The loop gives up after this many consecutive failures and
// waits for the operator's retry, which is the only thing that restarts it.
const POLL_FAILURE_LIMIT = 3;

function decisions(checkpoint?: FreshStrategyCheckpoint): AdoptionDecision[] {
  const value = checkpoint?.adoptionDecisions;
  return Array.isArray(value) ? value : [];
}

function groupDecisions(list: AdoptionDecision[]) {
  const groups: Record<'migrate' | 'manual_action' | 'unsupported', AdoptionDecision[]> = {
    migrate: [],
    manual_action: [],
    unsupported: [],
  };
  for (const decision of list) {
    if (decision?.Disposition === 'migrate') groups.migrate.push(decision);
    else if (decision?.Disposition === 'manual_action') groups.manual_action.push(decision);
    else groups.unsupported.push(decision);
  }
  return groups;
}

// Both carriers report a coded refusal with their own error type; the flow only
// needs the code and the retryable count, never the message.
function refusal(err: unknown): { code?: string; retryable?: number } {
  if (err instanceof BootstrapTransportError) return { code: err.code, retryable: err.retryable };
  if (err instanceof PublicApiError) return { code: err.code, retryable: err.retryable };
  return {};
}

/**
 * FreshAdoptFlow is the one network-adoption journey, rendered by both wizards.
 *
 *   1. source: address, management key, plugin opt-in and the migration scope;
 *   2. inventory: the first POST answers `staged` with the discovered resources
 *      and their dispositions, and nothing is copied until the operator confirms;
 *   3. migration: the second POST commits the destination and the flow polls the
 *      recorded per-item summary until the run settles;
 *   4. resume: a run that stopped with retryable or unprocessed items continues
 *      from its durable prefix, which is also how a reloaded page continues;
 *   5. complete: the completion conditions are the server's, and each refusal it
 *      can answer with has its own next action here.
 *
 * Every write is followed by a read of the durable operation, and no write is
 * ever replayed automatically: an ambiguous reply pauses on an explicit action.
 */
export function FreshAdoptFlow({
  operation,
  carrier,
  onOperation,
  onComplete,
  onPending,
  onStage,
  embedded = false,
}: {
  operation: FreshOperation;
  carrier: AdoptCarrier;
  onOperation: (operation: FreshOperation) => void;
  onComplete?: () => void;
  onPending?: (pending: boolean) => void;
  /** Reports the stage so a host wizard can move its own step indicator. */
  onStage?: (stage: Stage) => void;
  /**
   * `embedded` renders the body and footer inside a host wizard's own card. The
   * two wizards own different chrome — the bootstrap one has a step list above —
   * so the flow provides the journey, not the shell.
   */
  embedded?: boolean;
}) {
  const checkpoint = operation.strategy;
  const committed = checkpoint?.stage === 'committed';
  const staged = checkpoint?.stage === 'staged';
  const [stage, setStage] = useState<Stage>(
    committed ? 'migration' : staged ? 'inventory' : 'source'
  );
  const [baseUrl, setBaseUrl] = useState('');
  const [managementKey, setManagementKey] = useState('');
  const [plugins, setPlugins] = useState(checkpoint?.pluginsEnabled === true);
  // The exact body of the first POST. It is kept so the second POST is byte
  // identical, which is what makes the commit the same attempt rather than a new
  // one. A reload loses it, and that is exactly when the key is asked for again.
  const [body, setBody] = useState<Record<string, unknown> | null>(null);
  const [confirmed, setConfirmed] = useState(false);
  const [summary, setSummary] = useState<AdoptionSummary | null>(null);
  const [busy, setBusy] = useState(false);
  const [unknown, setUnknown] = useState(false);
  const [error, setError] = useState('');
  const [note, setNote] = useState('');
  const [completion, setCompletion] = useState<{ code?: string; retryable?: number } | null>(null);
  const [acceptRetryable, setAcceptRetryable] = useState(false);
  // The progress read's own failure state. A coded refusal is answered with its
  // own sentence; an uncoded one gets the generic read sentence, so the screen
  // never claims to know why the record is unreachable.
  const [progressFailure, setProgressFailure] = useState<{ code?: string; message: string } | null>(
    null
  );
  const [pollEpoch, setPollEpoch] = useState(0);
  // The retryable count the acknowledgement was given for. It is a ref because
  // the poll loop reads it without being restarted by it, and because clearing
  // it must not re-render.
  const acceptedRetryable = useRef<number | null>(null);
  const inflight = useRef(false);
  const alive = useRef(true);
  // The carrier is built by the host on every render, because it closes over the
  // operation the host just accepted. The poll loop must not restart for that, so
  // it reads the latest one through a ref instead of a dependency.
  const carrierRef = useRef(carrier);
  carrierRef.current = carrier;
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  useEffect(() => {
    onStage?.(stage);
  }, [stage, onStage]);

  const poll = useCallback(() => setPollEpoch((value) => value + 1), []);
  // A retry is the operator's action, so it clears the failure it answers for
  // and restarts the loop with a fresh failure count.
  const retryProgress = useCallback(() => {
    setProgressFailure(null);
    poll();
  }, [poll]);

  // One poll loop while the migration screen is showing. It stops on the record's
  // own settled pair — finished and not running — so the page never polls a run
  // that has nothing left to report, and a Resume restarts it through `pollEpoch`.
  // A completed installation is read once and then left alone: the completion
  // screen still states what the run recorded, and nothing can change it.
  useEffect(() => {
    if (stage !== 'migration') return;
    let cancelled = false;
    let timer: number | undefined;
    let failures = 0;
    const tick = async () => {
      if (cancelled || !alive.current) return;
      let settled = false;
      try {
        const next = await carrierRef.current.readAdoption();
        if (cancelled || !alive.current) return;
        failures = 0;
        setProgressFailure(null);
        setSummary(next);
        // The acknowledgement belongs to the retryable set the operator was
        // shown. A different set is a decision they have not made yet.
        if (acceptedRetryable.current !== null && acceptedRetryable.current !== next.retryable) {
          acceptedRetryable.current = null;
          setAcceptRetryable(false);
        }
        settled = next.finished && !next.running;
      } catch (err) {
        if (cancelled || !alive.current) return;
        failures += 1;
        if (failures >= POLL_FAILURE_LIMIT) {
          const coded = refusal(err);
          setProgressFailure({
            code: coded.code,
            message: coded.code ? adoptBlockerText(coded.code) : adoptText('progressUnavailable'),
          });
          return;
        }
      }
      if (settled || operation.productComplete) return;
      timer = setTimeout(tick, Math.min(POLL_INTERVAL + failures * 1000, POLL_INTERVAL_MAX));
    };
    timer = setTimeout(tick, 0);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [stage, pollEpoch, operation.productComplete]);

  /**
   * beginRun clears the retryable acknowledgement. It is given to the set of
   * items the operator was shown, so a run they have not seen yet must ask again
   * rather than inherit it: the run a Resume starts is a new decision even when
   * it ends with the same count.
   */
  function beginRun() {
    acceptedRetryable.current = null;
    setAcceptRetryable(false);
    // The previous run's completion refusal is about that run: its retryable
    // set, and the acknowledgement offered for it, are gone with it.
    setCompletion(null);
  }

  /**
   * run performs one operator action. It is the only writer, it never runs two at
   * once, and after any failure it re-reads the durable operation before telling
   * the operator anything: a lost reply is an ambiguous write, not a failed one.
   */
  async function run(action: () => Promise<void>) {
    if (inflight.current || !alive.current) return;
    inflight.current = true;
    setBusy(true);
    setError('');
    setNote('');
    onPending?.(true);
    try {
      await action();
      if (alive.current) setUnknown(false);
    } catch (err) {
      if (!alive.current) return;
      const coded = refusal(err);
      setError(coded.code ? adoptBlockerText(coded.code) : adoptText('failure'));
      if (coded.code === 'adopt_plugin_optin_conflict') setNote(adoptText('pluginConflict'));
      if (coded.code === 'fresh_strategy_owner_conflict') setNote(adoptText('keyMismatch'));
      try {
        const truth = await carrier.readOperation();
        if (alive.current) onOperation(truth);
      } catch {
        if (alive.current) setUnknown(true);
      }
    } finally {
      inflight.current = false;
      if (alive.current) {
        setBusy(false);
        onPending?.(false);
      }
    }
  }

  // The first POST. It reads the source and persists the staged inventory, so a
  // confirmation always has something durable to confirm.
  async function readInventory() {
    await run(async () => {
      let normalized: string;
      try {
        normalized = normalizeCPAAddress(baseUrl);
      } catch {
        setError(adoptBlockerText('management_connection_invalid'));
        return;
      }
      if (!validManagementKey(managementKey)) {
        setError(adoptBlockerText('adoption_source_read_unavailable'));
        return;
      }
      const next = adoptStrategyBody(
        { baseUrl: normalized, managementKey, pluginsEnabled: plugins },
        await carrier.observeTarget()
      );
      const result = await carrier.writeStrategy(next);
      if (!alive.current) return;
      onOperation(result);
      setBody(next);
      setConfirmed(false);
      if (result.strategy?.stage === 'committed') {
        // The server may have committed the same attempt already; the record,
        // not this page, decides where the journey is.
        setManagementKey('');
        setStage('migration');
        beginRun();
        poll();
        return;
      }
      setStage('inventory');
    });
  }

  // The second POST, with the identical body. This is the commit, so the run
  // starts here and the polls take over.
  async function commit() {
    if (!body) return;
    await run(async () => {
      const result = await carrier.writeStrategy(body);
      if (!alive.current) return;
      onOperation(result);
      if (result.strategy?.stage !== 'committed') {
        // The reply does not show the commit it answers for: re-read before
        // claiming progress, and never resend on this page's own initiative.
        setUnknown(true);
        throw new Error('unconfirmed commit');
      }
      setManagementKey('');
      setStage('migration');
      setSummary(null);
      beginRun();
      poll();
    });
  }

  // A resume is a committed replay: the server skips every item the record
  // already settled. It needs the source again because the key lives in memory.
  async function resume() {
    if (body) {
      await commit();
      return;
    }
    await run(async () => {
      let normalized: string;
      try {
        normalized = normalizeCPAAddress(baseUrl);
      } catch {
        setError(adoptBlockerText('management_connection_invalid'));
        return;
      }
      if (!validManagementKey(managementKey)) {
        setError(adoptBlockerText('adoption_source_read_unavailable'));
        return;
      }
      const next = adoptStrategyBody(
        {
          baseUrl: normalized,
          managementKey,
          // The recorded choice is the only one this attempt accepts.
          pluginsEnabled: checkpoint?.pluginsEnabled === true,
        },
        await carrier.observeTarget()
      );
      const result = await carrier.writeStrategy(next);
      if (!alive.current) return;
      onOperation(result);
      setBody(next);
      setManagementKey('');
      setStage('migration');
      setSummary(null);
      beginRun();
      poll();
    });
  }

  // The completion's own coded refusals. Each one has a different next action, so
  // each one is read here rather than collapsed into a generic failure.
  function completeFailure(err: unknown) {
    const coded = refusal(err);
    setCompletion(coded);
    if (coded.code === 'adopt_migration_running') {
      setNote(adoptText('runningRefusal'));
      poll();
      return true;
    }
    if (coded.code === 'adopt_migration_incomplete') {
      setNote(adoptText('incompleteRefusal'));
      return true;
    }
    if (coded.code === 'adopt_migration_retryable') {
      setNote(adoptText('retryableRefusal', { count: coded.retryable ?? summary?.retryable ?? 0 }));
      return true;
    }
    if (coded.code === 'adopt_destination_unproven') {
      setError(adoptText('unprovenRefusal'));
      return true;
    }
    return false;
  }

  async function complete() {
    if (!alive.current) return;
    inflight.current = true;
    setBusy(true);
    setError('');
    setNote('');
    onPending?.(true);
    try {
      let current = operation;
      if (!current.routing.committed && carrier.prepareRoute) {
        current = await carrier.prepareRoute();
        onOperation(current);
      }
      const next = await carrier.complete(acceptRetryable ? { acceptRetryable: true } : {});
      if (!alive.current) return;
      onOperation(next);
      setCompletion(null);
      onComplete?.();
    } catch (err) {
      if (!alive.current) return;
      if (!completeFailure(err)) {
        const coded = refusal(err);
        setError(coded.code ? adoptBlockerText(coded.code) : s('failureHelp'));
        try {
          const truth = await carrier.readOperation();
          if (alive.current) onOperation(truth);
        } catch {
          if (alive.current) setUnknown(true);
        }
      }
    } finally {
      inflight.current = false;
      if (alive.current) {
        setBusy(false);
        onPending?.(false);
      }
    }
  }

  const groups = summary ? groupAdoptionItems(summary) : null;
  const inventory = groupDecisions(decisions(checkpoint));
  const processed = summary ? summary.items.length : 0;
  const planned = summary ? Math.max(summary.planned, processed) : 0;
  // A run that finished with planned items it never recorded is not a run with
  // nothing left to do: the remainder has to be resumed, and completion cannot
  // be granted over it.
  const remaining = groups ? groups.remaining : 0;
  const settledRun = !!summary && summary.finished && !summary.running;
  // The success headline is a claim about the installation, so it needs all
  // three facts: the run settled, no retryable item remains, and the
  // installation itself completed. A settled run missing any of them says what
  // is left instead of borrowing the sentence for a finished console.
  const adoptionComplete =
    !!summary && settledRun && summary.retryable === 0 && remaining === 0 && operation.productComplete;
  const needingResume =
    !!summary && !summary.running && (!summary.finished || summary.retryable > 0 || remaining > 0);
  const resumeForm = needingResume && !body;
  const headline =
    stage === 'inventory'
      ? { title: adoptText('inventoryTitle'), help: adoptText('inventoryHelp') }
      : stage === 'migration'
        ? adoptionComplete
          ? { title: adoptText('done'), help: adoptText('doneHelp') }
          : settledRun
            ? { title: adoptText('settledTitle'), help: adoptText('settledHelp') }
            : { title: adoptText('migrationTitle'), help: adoptText('migrationHelp') }
        : { title: adoptText('title'), help: adoptText('help') };

  const journey = (
    <>
      <div className="journey-body">
        <div className="journey-heading">
          <span className="journey-tile-icon">
            <FullWebIcon name="copy" size={30} />
          </span>
          <div>
            <h1 className="wizard-title">{headline.title}</h1>
            <p className="wizard-desc">{headline.help}</p>
          </div>
        </div>
        {error && (
          <p role="alert" className="alert alert-warn">
            {error}
          </p>
        )}
        {note && (
          <p role="status" className="alert alert-info">
            {note}
          </p>
        )}
        {unknown && (
          <p role="status" className="alert alert-warn">
            {adoptText('unknownHelp')}
          </p>
        )}
        {(stage === 'source' || resumeForm) && (
          <div className="journey-configuration">
            {resumeForm ? (
              <p className="journey-help">{adoptText('resumeKeyNotice')}</p>
            ) : (
              <p className="journey-help">{adoptText('sourceHelp')}</p>
            )}
            <div className="journey-field">
              <label className="form-label" htmlFor="adopt-source-url">
                {adoptText('baseURL')}
              </label>
              <input
                id="adopt-source-url"
                data-testid="adopt-source-url"
                className="form-input"
                autoComplete="off"
                spellCheck={false}
                value={baseUrl}
                onChange={(event) => setBaseUrl(event.target.value)}
              />
              <p className="journey-help">{adoptText('baseURLHelp')}</p>
            </div>
            <div className="journey-field">
              <label className="form-label" htmlFor="adopt-management-key">
                {adoptText('managementKey')}
              </label>
              <input
                id="adopt-management-key"
                data-testid="adopt-management-key"
                className="form-input"
                type="password"
                autoComplete="off"
                spellCheck={false}
                value={managementKey}
                onChange={(event) => setManagementKey(event.target.value)}
              />
              <p className="journey-help">{adoptText('managementKeyHelp')}</p>
            </div>
            <div className="journey-field">
              <label className="journey-checkline" htmlFor="adopt-plugin-optin">
                <input
                  id="adopt-plugin-optin"
                  data-testid="adopt-plugin-optin"
                  type="checkbox"
                  checked={resumeForm ? checkpoint?.pluginsEnabled === true : plugins}
                  disabled={resumeForm}
                  onChange={(event) => setPlugins(event.target.checked)}
                />
                <span>{adoptText('plugins')}</span>
              </label>
              <p className="journey-help">{adoptText('pluginsHelp')}</p>
            </div>
            <div className="journey-preview">
              <FullWebIcon name="info" size={22} />
              <div>
                <strong>{adoptText('scopeTitle')}</strong>
                <p>{adoptText('scopeMigrate')}</p>
                <p>{adoptText('scopeKeep')}</p>
              </div>
            </div>
          </div>
        )}
        {stage === 'inventory' && (
          <div className="adopt-inventory" data-testid="adopt-inventory">
            <p className="journey-help">{adoptText('inventoryHelp')}</p>
            <dl className="adopt-decisions">
              {(
                [
                  ['migrate', adoptText('migrateGroup')],
                  ['manual_action', adoptText('manualGroup')],
                  ['unsupported', adoptText('unsupportedGroup')],
                ] as const
              ).map(([disposition, label]) => (
                <div className="adopt-decision-group" key={disposition}>
                  <dt>{label}</dt>
                  <dd>
                    {inventory[disposition].length === 0 ? (
                      <span className="journey-quiet">—</span>
                    ) : (
                      <ul>
                        {inventory[disposition].map((decision) => (
                          <li key={decision.ResourceRef}>
                            {adoptResourceText(decision.ResourceRef)}
                          </li>
                        ))}
                      </ul>
                    )}
                  </dd>
                </div>
              ))}
            </dl>
            <label className="journey-checkline">
              <input
                type="checkbox"
                data-testid="adopt-confirm-inventory"
                checked={confirmed}
                onChange={(event) => setConfirmed(event.target.checked)}
              />
              {adoptText('confirmInventory')}
            </label>
          </div>
        )}
        {stage === 'migration' && (
          <div className="adopt-migration" data-testid="adopt-migration">
            {progressFailure && (
              <p role="alert" className="alert alert-warn" data-testid="adopt-progress-error">
                {progressFailure.message}{' '}
                <button
                  type="button"
                  className="btn btn-secondary"
                  data-testid="adopt-retry-progress"
                  disabled={busy}
                  onClick={retryProgress}
                >
                  {adoptText('retryProgress')}
                </button>
              </p>
            )}
            {!summary ? (
              <p className="journey-quiet" role="status" data-testid="adopt-progress">
                {adoptText('waiting')}
              </p>
            ) : (
              <>
                <p className="journey-quiet" data-testid="adopt-progress">
                  {adoptText('progress', { settled: processed, planned })}
                </p>
                {groups && (
                  <ol className="journey-stages" data-testid="adopt-items">
                    {(
                      [
                        ['migrated', adoptText('migratedGroup'), groups.migrated, 'check'],
                        ['manual', adoptText('manualBadge'), groups.manual, 'info'],
                        ['failed', adoptText('failedGroup'), groups.failed, 'alert'],
                        [
                          'absent_at_source',
                          adoptText('absentBadge'),
                          groups.absent_at_source,
                          'info',
                        ],
                      ] as const
                    ).map(([outcome, label, items, icon]) => (
                      <li key={outcome} data-testid={`adopt-group-${outcome}`}>
                        <span className="journey-stage-mark">
                          <FullWebIcon name={icon} size={22} />
                        </span>
                        <div>
                          <strong>{label}</strong>
                          <p data-testid={`adopt-count-${outcome}`}>
                            {items.length === 0 ? '—' : items.length}
                          </p>
                          {items.length > 0 && (
                            <ul className="adopt-items">
                              {items.map((item) => (
                                <li key={item.item} data-testid={`adopt-item-${item.item}`}>
                                  <span className="adopt-item-name">{item.item}</span>
                                  <span className="badge badge-neutral">
                                    {adoptText(
                                      (
                                        {
                                          credential: 'kindCredential',
                                          config_leaf: 'kindConfigLeaf',
                                          config_family: 'kindConfigFamily',
                                          provider_section: 'kindProviderSection',
                                        } as const
                                      )[item.kind]
                                    )}
                                  </span>
                                  {item.reason && (
                                    <span className="adopt-item-reason">
                                      {adoptReasonText(item.reason)}
                                    </span>
                                  )}
                                  {item.outcome === 'failed' && (
                                    <span
                                      className={`badge ${item.retryable ? 'badge-amber' : 'badge-neutral'}`}
                                    >
                                      {adoptText(item.retryable ? 'retryableBadge' : 'failedGroup')}
                                    </span>
                                  )}
                                  {item.instruction && (
                                    <details className="journey-disclosure">
                                      <summary>{adoptText('details')}</summary>
                                      <p>{item.instruction}</p>
                                    </details>
                                  )}
                                </li>
                              ))}
                            </ul>
                          )}
                        </div>
                        <small>{items.length}</small>
                      </li>
                    ))}
                  </ol>
                )}
              </>
            )}
            {summary && summary.retryable > 0 && !summary.running && (
              <p className="journey-help" data-testid="adopt-retryable">
                {adoptText('retryableRefusal', { count: summary.retryable })}
              </p>
            )}
            {summary && !summary.running && remaining > 0 && (
              <p className="journey-help" data-testid="adopt-remaining">
                {adoptText('remainingItems', { count: remaining })}
              </p>
            )}
          </div>
        )}
      </div>
      <footer className="journey-footer">
        {stage === 'source' && (
          <>
            <span className="journey-quiet">
              <FullWebIcon name="shield" size={20} />
              {adoptText('scopeKeep')}
            </span>
            <button
              type="button"
              className="btn btn-primary"
              data-testid="adopt-read-inventory"
              disabled={busy}
              onClick={() => void readInventory()}
            >
              {adoptText('readInventory')}
              <FullWebIcon name="arrowRight" size={20} />
            </button>
          </>
        )}
        {stage === 'inventory' && (
          <>
            <button
              type="button"
              className="btn btn-secondary"
              disabled={busy}
              onClick={() => {
                setStage('source');
                setConfirmed(false);
              }}
            >
              <FullWebIcon name="arrowLeft" size={20} />
              {s('back')}
            </button>
            <button
              type="button"
              className="btn btn-primary"
              data-testid="adopt-confirm-migrate"
              disabled={busy || !confirmed}
              onClick={() => void commit()}
            >
              {adoptText('confirmAndMigrate')}
              <FullWebIcon name="arrowRight" size={20} />
            </button>
          </>
        )}
        {stage === 'migration' && (
          <>
            <button
              type="button"
              className="btn btn-secondary"
              data-testid="adopt-refresh"
              disabled={busy}
              onClick={retryProgress}
            >
              <FullWebIcon name="refresh" size={20} />
              {adoptText('refresh')}
            </button>
            {needingResume && (
              <button
                type="button"
                className="btn btn-primary"
                data-testid="adopt-resume"
                disabled={busy}
                onClick={() => void resume()}
              >
                {adoptText('resume')}
                <FullWebIcon name="arrowRight" size={20} />
              </button>
            )}
            {completion?.code === 'adopt_migration_retryable' && (
              <label className="journey-checkline">
                <input
                  type="checkbox"
                  data-testid="adopt-accept-retryable"
                  checked={acceptRetryable}
                  onChange={(event) => {
                    setAcceptRetryable(event.target.checked);
                    // The acknowledgement covers the set on screen right now,
                    // and only that set.
                    acceptedRetryable.current = event.target.checked
                      ? (summary?.retryable ?? 0)
                      : null;
                  }}
                />
                {adoptText('acceptRetryable')}
              </label>
            )}
            <button
              type="button"
              className="btn btn-primary"
              data-testid="adopt-complete"
              // A settled run with retryable items can always ask to complete:
              // the server's refusal is what names them, and the count is what
              // the acknowledgement is about. A run that never processed its own
              // plan is different — completion cannot succeed over it — so the
              // only action offered there is Resume.
              disabled={busy || !settledRun || remaining > 0}
              onClick={() => void complete()}
            >
              {adoptText(busy ? 'completing' : 'complete')}
              <FullWebIcon name="arrowRight" size={20} />
            </button>
          </>
        )}
      </footer>
    </>
  );

  if (embedded) return journey;
  return (
    <section className="cpamp-installation" data-testid="fresh-adopt-flow" aria-busy={busy}>
      <div className="wizard-card" data-testid="fresh-adopt-flow">
        {journey}
      </div>
    </section>
  );
}
