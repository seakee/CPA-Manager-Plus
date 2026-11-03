import { useCallback, useEffect, useRef, useState } from 'react';
import {
  adoptBlockerText,
  adoptGuidanceText,
  adoptKindText,
  adoptReasonText,
  adoptResourceText,
  adoptText,
} from '../i18n/freshAdopt';
import { shellText as s } from '../i18n/consoleShell';
import {
  BootstrapTransportError,
  type AdoptionFence,
  type FreshOperation,
  type FreshStrategyCheckpoint,
} from './bootstrapTransport';
import {
  adoptStrategyBody,
  groupAdoptionItems,
  normalizeCPAAddress,
  validManagementKey,
  type AdoptionDecision,
  type AdoptionOutcome,
  type AdoptionSummary,
  type AdoptCompletionBody,
  type AdoptRuntimeTarget,
} from './freshAdoption';
import { PublicApiError } from './publicApi';
import { FullWebIcon, type FullWebIconName } from './FullWebIcon';

// The Adopt flow's two carriers. The wizard before routing exists and the Console
// after it hold the same operation but not the same transport, so the flow talks
// to this small interface and each shell binds it once.
export interface AdoptCarrier {
  /** Read the durable operation, which is the reconciliation after any write. */
  readOperation(): Promise<FreshOperation>;
  /** Observe the CPA this Manager runs: the destination of the adoption. */
  observeTarget(): Promise<AdoptRuntimeTarget>;
  /**
   * The two-POST inventory: staged first, committed second.
   *
   * `fence` is the durable read that decided the body, and only a write that
   * must not commit has one: the revision makes the staging POST stale instead
   * of turning it into the confirmation's commit.
   */
  writeStrategy(body: Record<string, unknown>, fence?: AdoptionFence): Promise<FreshOperation>;
  /** Commit the canonical route before completion. Bootstrap-only. */
  prepareRoute?(): Promise<FreshOperation>;
  complete(body: AdoptCompletionBody): Promise<FreshOperation>;
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

// retryableIdentity is the run and item set a retryable acknowledgement is
// given for. Neither the count nor the item set alone is the identity: a new run
// can fail the same items, and those are items nobody has accepted yet. The run
// identity is what distinguishes the two. Both the poll and the checkbox go
// through it, because an acknowledgement that means two different things is an
// acknowledgement of nothing.
function retryableIdentity(summary: AdoptionSummary): string {
  return [
    summary.runId,
    ...summary.items
      .filter((item) => item.retryable)
      .map((item) => item.id)
      .sort(),
  ].join('\n');
}

// The four recorded outcomes in the order the completion screen states them. One
// group per outcome, read from the record: `absent_at_source` is a settled result
// about a resource the source never had, so it is never counted as a copy.
const outcomeGroups: ReadonlyArray<{
  outcome: AdoptionOutcome;
  label: 'migratedGroup' | 'manualBadge' | 'failedGroup' | 'absentBadge';
  icon: FullWebIconName;
}> = [
  { outcome: 'migrated', label: 'migratedGroup', icon: 'check' },
  { outcome: 'manual', label: 'manualBadge', icon: 'info' },
  { outcome: 'failed', label: 'failedGroup', icon: 'alert' },
  { outcome: 'absent_at_source', label: 'absentBadge', icon: 'info' },
];

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
 * What a recorded run lets its host say about the installation and the copy.
 *
 * The two are different facts. The installation completing is the server's
 * decision, and the server grants it over retryable items once the operator has
 * accepted this exact run: the acknowledgement is durable and run-bound, so a
 * positive retryable count means the decision was taken, not that it is still
 * open. What the copy left behind is the record's own count of manual and failed
 * items, and only a settled run that recorded its whole plan with nothing left
 * over may say the adoption itself is complete.
 *
 * Both hosts call this, so the completion step of a wizard and the shared flow
 * cannot disagree about the same run.
 */
export type AdoptionCompletionState = 'none' | 'partial' | 'complete';

// The setup wizard's own completion step applies this same rule, so it is
// exported beside the flow rather than copied into the host.
// eslint-disable-next-line react-refresh/only-export-components
export function adoptionCompletionState(
  summary: AdoptionSummary | null | undefined,
  productComplete: boolean
): AdoptionCompletionState {
  if (!summary || !summary.finished || summary.running || !productComplete) return 'none';
  const left = Math.max(summary.planned, summary.items.length) - summary.items.length;
  if (left !== 0) return 'none';
  if (summary.counts.manual === 0 && summary.counts.failed === 0)
    return summary.retryable === 0 ? 'complete' : 'none';
  return 'partial';
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
  onEnterConsole,
  onPending,
  onStage,
  onExit,
  embedded = false,
}: {
  operation: FreshOperation;
  carrier: AdoptCarrier;
  onOperation: (operation: FreshOperation) => void;
  /**
   * Completion succeeded. The recorded summary is the last read of the run, so a
   * host that leaves the flow can state what the copy left behind instead of
   * claiming a clean installation.
   */
  onComplete?: (summary: AdoptionSummary) => void;
  /**
   * A completed installation whose host kept this screen visible (because the
   * copy left manual or failed items) offers entry to the console here.
   */
  onEnterConsole?: () => void;
  onPending?: (pending: boolean) => void;
  /** Reports the stage so a host wizard can move its own step indicator. */
  onStage?: (stage: Stage) => void;
  /**
   * Leaves the journey for the host's strategy choice. Only a host that offered
   * the choice can take it back, and only while the record holds no attempt: once
   * the record owns one, the journey is the record's.
   */
  onExit?: () => void;
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
  // The retryable set the acknowledgement was given for, as the stable identity
  // of its items. It is a ref because the poll loop reads it without being
  // restarted by it, and because clearing it must not re-render.
  const acceptedRetryable = useRef<string | null>(null);
  const inflight = useRef(false);
  const alive = useRef(true);
  // The staging POST is the one write whose failure can still mean the record
  // holds the attempt: the server can stage it and lose the reply on the way
  // back. Every other failure in this flow — a read of the record, the
  // destination observation a rebuild performs — leaves the record where it was,
  // so it must leave its own failure on screen instead of being answered with
  // "the record already staged this".
  const stagingMayHaveLanded = useRef(false);
  // The carrier is built by the host on every render, because it closes over the
  // operation the host just accepted. The poll loop must not restart for that, so
  // it reads the latest one through a ref instead of a dependency.
  const carrierRef = useRef(carrier);
  carrierRef.current = carrier;
  // The operator's own inputs as the screen holds them. A rebuilt body is only
  // confirmable while these are still the inputs it was built from: the rebuild
  // awaits the destination read with the fields on screen, so a value typed in
  // that window would otherwise leave the page showing one address and holding a
  // body built from the one before it.
  const screenInputs = useRef({ baseUrl, managementKey });
  screenInputs.current = { baseUrl, managementKey };
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
        // shown, item for item. A different set is a decision they have not made
        // yet, even when it has the same size.
        if (
          acceptedRetryable.current !== null &&
          acceptedRetryable.current !== retryableIdentity(next)
        ) {
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
    // Each action starts from "no write has left this page": only the staging
    // POST of this action can decide that the record already holds the attempt.
    stagingMayHaveLanded.current = false;
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
      // A refusal that says "this body is not this record's attempt" can never
      // succeed as it stands: the key, the address or the choice the record owns
      // has to be corrected, so the confirmation stops offering that body and the
      // source form comes back instead.
      if (
        coded.code === 'fresh_strategy_owner_conflict' ||
        coded.code === 'adopt_plugin_optin_conflict'
      )
        setBody(null);
      try {
        const truth = await carrier.readOperation();
        if (alive.current) {
          onOperation(truth);
          // A durable staged attempt answers a lost reply: the inventory the
          // server wrote is what the operator has to see, and this page has no
          // business claiming the read failed. Only the staging POST's own
          // failure can be that lost reply — a failed read or rebuild staged
          // nothing — and a typed refusal keeps its own message, because it
          // explained itself and the page is still on that step.
          if (
            stagingMayHaveLanded.current &&
            !coded.code &&
            stage === 'source' &&
            truth.strategy?.strategy === 'adopt_existing' &&
            truth.strategy.stage === 'staged'
          ) {
            setStage('inventory');
            setConfirmed(false);
            setError('');
          }
        }
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
  //
  // The record is read first because the two POSTs are one request: a second
  // identical POST of an attempt the server already staged is taken as the
  // commit. That read is also the POST's fence, so the write it authorizes is the
  // staging of the state it saw and nothing else: a staging that landed in
  // between makes the server refuse it as stale instead of accepting it as the
  // commit. Only the confirmation sends an unfenced write.
  async function readInventory() {
    await run(async () => {
      const durable = await carrier.readOperation();
      if (!alive.current) return;
      onOperation(durable);
      const adopt = durable.strategy?.strategy === 'adopt_existing' ? durable.strategy : undefined;
      if (adopt?.stage === 'staged') {
        // The record owns the plugin choice from the moment it staged this
        // attempt, and this page only learns that here: a first reply lost on
        // the way back leaves the checkbox editable while the record already
        // holds the attempt. A choice the record did not record is stated on
        // this step — the help under the locked checkbox — instead of advancing
        // to a confirmation the operator would read as their own change. The
        // recorded value is adopted here, so the next read sends it.
        if (plugins !== (adopt.pluginsEnabled === true)) {
          setPlugins(adopt.pluginsEnabled === true);
          return;
        }
        setStage('inventory');
        setConfirmed(false);
        // The commit sends this page's body, and the record already holds the
        // attempt. Rebuild it on every entry — not only when the page holds no
        // body — from the inputs on screen and the choice the record owns, so a
        // corrected address or key is what the confirmation sends instead of a
        // body built before the record owned the attempt. The old body stops
        // being confirmable before the rebuild starts: the rebuild reads the
        // destination, and a read that fails must leave nothing this page could
        // still commit. Only a body this read rebuilt becomes confirmable again.
        setBody(null);
        const rebuilt = await recordedBody(adopt.pluginsEnabled === true);
        if (rebuilt && alive.current) setBody(rebuilt);
        return;
      }
      if (adopt?.stage === 'committed') {
        // The record, not this page, decides where the journey is.
        setManagementKey('');
        setStage('migration');
        setSummary(null);
        beginRun();
        poll();
        return;
      }
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
      // The body is kept before the write, so a reply lost on the way back still
      // leaves this page holding the exact attempt the server may have staged.
      setBody(next);
      // The fence is this decision read, never a fresher one: a staging that
      // landed between the read above and this write — a slow source, a reply
      // lost on the way back — must make the CAS refuse the POST, not turn it
      // into the commit of an attempt this operator has not confirmed.
      //
      // From here until the reply arrives this write may have reached the
      // record, so a failure is answered by reading the record; no earlier step
      // of this action could have staged anything.
      stagingMayHaveLanded.current = true;
      const result = await carrier.writeStrategy(next, {
        identity: durable.identity,
        revision: durable.revision,
      });
      if (!alive.current) return;
      onOperation(result);
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

  // The exact body of an attempt that is not the first read. The plugin choice is
  // the one the record holds and the source screen shows as locked, the address
  // and key are the operator's, and the destination observation is a read:
  // rebuilding a body never writes. The caller passes the recorded choice because
  // a read of the record may have just replaced this render's checkpoint.
  //
  // A rebuilt body belongs to the inputs it was built from. Nothing else in this
  // flow may make a body confirmable, so this is where the page proves it: the
  // inputs are checked once the observation returns, and a body whose inputs the
  // screen has moved past is discarded instead of offered for confirmation.
  async function recordedBody(pluginsEnabled: boolean): Promise<Record<string, unknown> | null> {
    // Everything up to the observation is synchronous, so these are the values
    // the operator sees at the click that started this rebuild.
    const inputs = { baseUrl, managementKey };
    let normalized: string;
    try {
      normalized = normalizeCPAAddress(inputs.baseUrl);
    } catch {
      setError(adoptBlockerText('management_connection_invalid'));
      return null;
    }
    if (!validManagementKey(inputs.managementKey)) {
      setError(adoptBlockerText('adoption_source_read_unavailable'));
      return null;
    }
    const next = adoptStrategyBody(
      {
        baseUrl: normalized,
        managementKey: inputs.managementKey,
        // The recorded choice is the only one this attempt accepts.
        pluginsEnabled,
      },
      await carrier.observeTarget()
    );
    if (!alive.current) return null;
    // The observation is this rebuild's only await, and the fields stay on screen
    // for it. A body built from inputs the screen no longer holds is not the
    // attempt the operator is looking at, so it is dropped instead of becoming
    // confirmable: the fields stay open and the next click rebuilds from what
    // they show.
    if (
      screenInputs.current.baseUrl !== inputs.baseUrl ||
      screenInputs.current.managementKey !== inputs.managementKey
    ) {
      setError(adoptText('inputsChanged'));
      return null;
    }
    return next;
  }

  // A resume is a committed replay: the server skips every item the record
  // already settled. It needs the source again because the key lives in memory.
  async function resume() {
    if (body) {
      await commit();
      return;
    }
    await run(async () => {
      const next = await recordedBody(checkpoint?.pluginsEnabled === true);
      if (!next || !alive.current) return;
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

  // A staged attempt whose body this page no longer holds: rebuild the identical
  // body from the operator's own inputs. Nothing here writes — the destination
  // observation is a read — so the commit is still only the confirmation's POST.
  async function rebuildConfirmBody() {
    await run(async () => {
      const next = await recordedBody(checkpoint?.pluginsEnabled === true);
      if (!next || !alive.current) return;
      setBody(next);
      setManagementKey('');
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
    if (coded.code === 'adopt_migration_run_changed') {
      // The acknowledgement named a run that is no longer current — another tab
      // or operator resumed in between. It is not this run's decision any more,
      // so the page drops it and reads the run that is.
      acceptedRetryable.current = null;
      setAcceptRetryable(false);
      setNote(adoptText('runChanged'));
      poll();
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
    // The acknowledgement belongs to the run and items on screen. A newer
    // summary invalidates it even when the retryable ids are identical, so the
    // page refuses to send a body it can no longer claim the operator confirmed.
    // The server is the final fence; this only keeps the page from asking with a
    // decision the operator was never shown.
    if (acceptRetryable) {
      const identity = summary ? retryableIdentity(summary) : null;
      if (!summary?.runId || acceptedRetryable.current !== identity) {
        acceptedRetryable.current = null;
        setAcceptRetryable(false);
        setNote(adoptText('runChanged'));
        poll();
        return;
      }
    }
    const acknowledgement: AdoptCompletionBody =
      acceptRetryable && summary ? { acceptRetryable: true, runId: summary.runId } : {};
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
      const next = await carrier.complete(acknowledgement);
      if (!alive.current) return;
      onOperation(next);
      setCompletion(null);
      if (summary) onComplete?.(summary);
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
      // A refusal or a lost reply is not a decision that carries into the next
      // attempt: the operator accepts the run that is on screen when they try
      // again. `completeFailure` already cleared the coded run change.
      if (acceptRetryable) {
        acceptedRetryable.current = null;
        setAcceptRetryable(false);
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
  const manualCount = summary?.counts.manual ?? 0;
  const failedCount = summary?.counts.failed ?? 0;
  // The installation can be complete while the copy is not: the server allows a
  // manual item, a non-retryable failure, or the retryable items of the run the
  // operator accepted to stand, and none of them means the resource was copied.
  // The success headline is a claim about the copy, so it needs the settled run,
  // no retryable item, no unprocessed plan item, no manual work and no failure,
  // and the completed installation. The split state keeps the installation's own
  // headline and the failed items with their counts.
  const completionState = adoptionCompletionState(summary, operation.productComplete === true);
  const adoptionComplete = completionState === 'complete';
  const installationComplete = completionState === 'partial';
  const needingResume =
    !!summary && !summary.running && (!summary.finished || summary.retryable > 0 || remaining > 0);
  const resumeForm = needingResume && !body;
  // A staged inventory whose body this page no longer holds: a reload, or a lost
  // first reply. The commit sends that exact body, so the key is asked for here
  // and the confirmation is only offered once there is one to send.
  const inventoryKeyForm = stage === 'inventory' && !body;
  // The record owns the plugin choice from the moment it holds this attempt: the
  // opt-in was written into the destination template with the checkpoint, so a
  // screen that let it be edited again would offer a decision the server refuses
  // and the operator cannot see. It is shown, locked, with the reason.
  const pluginsLocked = staged || committed;
  const headline =
    stage === 'inventory'
      ? { title: adoptText('inventoryTitle'), help: adoptText('inventoryHelp') }
      : stage === 'migration'
        ? adoptionComplete
          ? { title: adoptText('done'), help: adoptText('doneHelp') }
          : installationComplete
            ? {
                title: adoptText('partialTitle'),
                help: adoptText('partialHelp', { manual: manualCount, failed: failedCount }),
              }
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
        {(stage === 'source' || resumeForm || inventoryKeyForm) && (
          <div className="journey-configuration">
            {resumeForm ? (
              <p className="journey-help">{adoptText('resumeKeyNotice')}</p>
            ) : inventoryKeyForm ? (
              <p className="journey-help">{adoptText('inventoryKeyNotice')}</p>
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
                // The body a confirmation sends is built from these fields, and
                // building it awaits the destination read. Editing while that
                // read runs would leave the page showing one address and holding
                // another, so the fields are closed for the duration of an
                // action, exactly like the buttons that start one.
                disabled={busy}
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
                // Same rule as the address: the key in the body being built is
                // this field's value, and a value typed into a field that is
                // already being read is not the one that would be sent.
                disabled={busy}
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
                  checked={pluginsLocked ? checkpoint?.pluginsEnabled === true : plugins}
                  disabled={pluginsLocked || busy}
                  onChange={(event) => setPlugins(event.target.checked)}
                />
                <span>{adoptText('plugins')}</span>
              </label>
              <p className="journey-help">
                {adoptText(pluginsLocked ? 'pluginsLocked' : 'pluginsHelp')}
              </p>
            </div>
            <div className="journey-preview">
              <FullWebIcon name="info" size={22} />
              <div>
                <strong>{adoptText('scopeTitle')}</strong>
                <p>{adoptText('scopeMigrate')}</p>
                <p>{adoptText('scopeKeep')}</p>
              </div>
            </div>
            {inventoryKeyForm && (
              <button
                type="button"
                className="btn btn-secondary"
                data-testid="adopt-use-key"
                disabled={busy}
                onClick={() => void rebuildConfirmBody()}
              >
                {adoptText('useKey')}
                <FullWebIcon name="check" size={20} />
              </button>
            )}
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
                    {outcomeGroups.map(({ outcome, label, icon }) => {
                      const items = groups[outcome];
                      return (
                        <li key={outcome} data-testid={`adopt-group-${outcome}`}>
                          <span className="journey-stage-mark">
                            <FullWebIcon name={icon} size={22} />
                          </span>
                          <div>
                            <strong>{adoptText(label)}</strong>
                            <p data-testid={`adopt-count-${outcome}`}>
                              {items.length === 0 ? '—' : items.length}
                            </p>
                            {items.length > 0 && (
                              <ul className="adopt-items">
                                {items.map((item) => (
                                  <li
                                    // The identity is the record's own, not the
                                    // text this page shows: two items whose
                                    // public labels coincide are still two
                                    // items, and the id is what an
                                    // acknowledgement names.
                                    key={item.id}
                                    data-testid={`adopt-item-${item.id}`}
                                  >
                                    <span className="adopt-item-name">{item.item}</span>
                                    <span className="badge badge-neutral">
                                      {adoptKindText(item.kind)}
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
                                        {adoptText(
                                          item.retryable ? 'retryableBadge' : 'failedGroup'
                                        )}
                                      </span>
                                    )}
                                    {(item.outcome === 'manual' || item.outcome === 'failed') && (
                                      <details className="journey-disclosure">
                                        <summary>{adoptText('details')}</summary>
                                        <p data-testid={`adopt-guidance-${item.kind}`}>
                                          {adoptGuidanceText(item.kind, item.reason, item.left)}
                                        </p>
                                      </details>
                                    )}
                                  </li>
                                ))}
                              </ul>
                            )}
                          </div>
                          <small>{items.length}</small>
                        </li>
                      );
                    })}
                  </ol>
                )}
              </>
            )}
            {summary && summary.retryable > 0 && !summary.running && !operation.productComplete && (
              // While the installation is not complete this sentence carries the
              // next action: resume, or finish anyway. Once it is complete, both
              // actions are gone — the failed group and the split headline state
              // the remainder instead.
              <p className="journey-help" data-testid="adopt-retryable">
                {adoptText('retryableRefusal', { count: summary.retryable })}
              </p>
            )}
            {completion?.code === 'adopt_migration_retryable' && !summary?.runId && (
              <p className="journey-help" data-testid="adopt-retryable-legacy">
                {adoptText('legacyRunNotice')}
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
            {onExit && (
              <button
                type="button"
                className="btn btn-secondary"
                data-testid="adopt-exit"
                disabled={busy}
                onClick={onExit}
              >
                <FullWebIcon name="arrowLeft" size={20} />
                {s('back')}
              </button>
            )}
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
              data-testid="adopt-inventory-back"
              disabled={busy}
              onClick={() => {
                setStage('source');
                setConfirmed(false);
                // Back is the operator saying the attempt is not settled: the
                // body built for it is dropped with it, so a changed address or
                // key is what the next attempt sends instead of a decision this
                // page made for them.
                setBody(null);
              }}
            >
              <FullWebIcon name="arrowLeft" size={20} />
              {s('back')}
            </button>
            <button
              type="button"
              className="btn btn-primary"
              data-testid="adopt-confirm-migrate"
              // The commit sends this page's body, so there is nothing to offer
              // until the key has been entered again: a clickable button that
              // did nothing would state the opposite.
              disabled={busy || !confirmed || !body}
              onClick={() => void commit()}
            >
              {adoptText('confirmAndMigrate')}
              <FullWebIcon name="arrowRight" size={20} />
            </button>
          </>
        )}
        {stage === 'migration' && operation.productComplete && onEnterConsole && (
          <button
            type="button"
            className="btn btn-primary"
            data-testid="adopt-enter-console"
            disabled={busy}
            onClick={onEnterConsole}
          >
            {s('console')}
            <FullWebIcon name="arrowRight" size={20} />
          </button>
        )}
        {stage === 'migration' && !operation.productComplete && (
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
            {completion?.code === 'adopt_migration_retryable' && summary?.runId && (
              <label className="journey-checkline">
                <input
                  type="checkbox"
                  data-testid="adopt-accept-retryable"
                  checked={acceptRetryable}
                  onChange={(event) => {
                    setAcceptRetryable(event.target.checked);
                    // The acknowledgement covers this run and the retryable
                    // items on screen right now, item for item, and only those:
                    // the completion carries the run identity it was given for.
                    acceptedRetryable.current =
                      event.target.checked && summary ? retryableIdentity(summary) : null;
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
