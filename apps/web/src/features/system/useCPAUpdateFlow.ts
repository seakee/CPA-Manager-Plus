import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useAuthStore } from '@/stores';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { normalizeUsageServiceBase } from '@/services/api/usageService';
import {
  canPrepareCPAUpdate,
  CPA_UPDATE_MAX_UPLOAD_BYTES,
  cpaUpdateApi,
  CPAUpdateAPIError,
  isValidHttpsCDNPrefix,
  type CPAUpdateRequest,
  type CPAUpdateStatus,
} from './cpaUpdateApi';
import {
  claimCPAUpdateIntent,
  clearCPAUpdateIntent,
  cpaUpdateStorageKey,
  CPAUpdateStorageError,
  createCPAUpdateRequestID,
  persistCPAUpdateIntent,
  parseCPAUpdateRecord,
  readCPAUpdateRecord,
  readCPAUpdateIntent,
  sameCPAUpdateIntent,
  withCPAUpdateIntentLock,
  type CPAUpdateIntent,
  type CPAUpdateRecordSnapshot,
} from './cpaUpdateIntentStorage';

export type CPAUpdateFlowStage =
  | 'idle'
  | 'recovering'
  | 'submitting'
  | 'accepted'
  | 'running'
  | 'prepared'
  | 'outcome_unknown'
  | 'not_found'
  | 'context_changed'
  | 'confirmed_failed'
  | 'succeeded'
  | 'already_applied';

export interface CPAUpdatePrepareProgress {
  phase?: string;
  receivedBytes: number;
  totalBytes: number;
}

export interface CPAUpdatePrepareOptions {
  sourceMode?: 'github' | 'cdn';
  cdnPrefix?: string;
  uploadFile?: Blob;
}

interface Snapshot {
  status: CPAUpdateStatus | null;
  statusError: boolean;
  intent: CPAUpdateIntent | null;
  recoveryRecord: CPAUpdateRecordSnapshot | null;
  stage: CPAUpdateFlowStage;
  recoveredSession: boolean;
  prepareProgress: CPAUpdatePrepareProgress | null;
  initialized: boolean;
  busy: boolean;
  storageError: CPAUpdateStorageError['kind'] | null;
  failureCode?: string;
}

const initialSnapshot = (): Snapshot => ({
  status: null,
  statusError: false,
  intent: null,
  recoveryRecord: null,
  stage: 'idle',
  recoveredSession: false,
  prepareProgress: null,
  initialized: false,
  busy: false,
  storageError: null,
});
const terminal = (stage: CPAUpdateFlowStage) =>
  ['confirmed_failed', 'succeeded', 'already_applied'].includes(stage);
const unresolved = (view: Snapshot) => !!view.intent && !terminal(view.stage);
const ready = (view: Snapshot) => view.initialized && !view.busy && !view.storageError;
const canCheck = (view: Snapshot) =>
  ready(view) && !unresolved(view) && view.status?.mode === 'embedded';
const canPrepare = (view: Snapshot) =>
  ready(view) && !unresolved(view) && !view.statusError && canPrepareCPAUpdate(view.status);

function compatible(status: CPAUpdateStatus | null, intent: CPAUpdateIntent): boolean {
  if (
    !status ||
    status.mode !== 'embedded' ||
    status.stale ||
    status.last_error ||
    !status.prepare_supported ||
    !status.activate_supported ||
    status.target_version !== intent.target_version
  )
    return false;
  if (status.active_artifact_id === intent.expected_active_artifact_id) return true;
  // This permits an explicit activate replay to ask the server for already_applied.
  // It never establishes success, and never replaces the original artifact guard.
  return (
    !!status.active_artifact_id &&
    status.current_version === intent.target_version &&
    (intent.phase === 'activate' || intent.client_stage === 'prepared')
  );
}

const sameFlow = (left: CPAUpdateIntent, right: CPAUpdateIntent) =>
  left.request_id === right.request_id &&
  left.target_version === right.target_version &&
  left.expected_active_artifact_id === right.expected_active_artifact_id &&
  left.source_mode === right.source_mode &&
  left.cdn_prefix === right.cdn_prefix;
const withoutUnrecordedContextChanged = (intent: CPAUpdateIntent): CPAUpdateIntent => {
  if (!('unrecorded_context_changed' in intent)) return intent;
  const next = { ...intent };
  delete next.unrecorded_context_changed;
  return next;
};
// Earlier builds did not store the source, even for CDN and upload prepares.
const canReplaySource = (intent: CPAUpdateIntent) =>
  intent.phase === 'activate' || intent.source_mode === 'github' || intent.source_mode === 'cdn';
const PREPARE_PHASE_ORDER: Record<string, number> = {
  resolving: 0,
  uploading: 1,
  downloading: 1,
  verifying: 2,
  staging: 3,
};

function mergePrepareProgress(
  previous: CPAUpdatePrepareProgress | null,
  incoming: { phase?: string; receivedBytes?: number; totalBytes?: number }
): CPAUpdatePrepareProgress {
  const previousPhase = previous?.phase;
  const nextPhase = incoming.phase;
  let phase = nextPhase ?? previousPhase;
  if (
    previousPhase &&
    nextPhase &&
    previousPhase in PREPARE_PHASE_ORDER &&
    nextPhase in PREPARE_PHASE_ORDER &&
    PREPARE_PHASE_ORDER[nextPhase] < PREPARE_PHASE_ORDER[previousPhase]
  ) {
    phase = previousPhase;
  }
  return {
    phase,
    receivedBytes: Math.max(previous?.receivedBytes ?? 0, incoming.receivedBytes ?? 0),
    totalBytes: Math.max(previous?.totalBytes ?? 0, incoming.totalBytes ?? 0),
  };
}

interface Actions {
  refresh: () => Promise<void>;
  check: () => Promise<void>;
  prepare: (options?: CPAUpdatePrepareOptions) => Promise<void>;
  prepareUpload: (file: Blob) => Promise<void>;
  activate: (expected: CPAUpdateIntent) => Promise<void>;
  retry: (expected: CPAUpdateIntent) => Promise<void>;
  forget: (expected: CPAUpdateRecordSnapshot) => Promise<void>;
}

export function useCPAUpdateFlow() {
  const authenticated = useAuthStore((state) => state.isAuthenticated);
  const key = useAuthStore((state) => state.managementKey);
  const availability = usePanelFeatureAvailability();
  const base = normalizeUsageServiceBase(availability.managerServiceBase);
  const available = authenticated && !!key && !!base && availability.managerServiceAvailable;
  const context = useMemo(
    () => ({ base, key, available, scope: cpaUpdateStorageKey(base, key) }),
    [base, key, available]
  );
  const [state, setState] = useState<{ context: object; view: Snapshot }>(() => ({
    context,
    view: initialSnapshot(),
  }));
  const actions = useRef<{ context: object; controls: Actions } | null>(null);

  useEffect(() => {
    let alive = true;
    let pending = false;
    let timer: number | undefined;
    let view = initialSnapshot();
    let unrecordedOperation: { request_id: string; phase: CPAUpdateIntent['phase'] } | null = null;
    const waiting = new AbortController();
    const connection = { base: context.base, key: context.key };
    const scope = context.scope;

    const publish = (patch: Partial<Snapshot>) => {
      if (!alive) return;
      view = { ...view, ...patch };
      setState({ context, view });
    };
    const stopTimer = () => {
      if (timer !== undefined) window.clearTimeout(timer);
      timer = undefined;
    };
    const blockStorage = (error: unknown) => {
      const kind = error instanceof CPAUpdateStorageError ? error.kind : 'unavailable';
      publish({ storageError: kind, ...(kind === 'changed' ? { stage: 'context_changed' } : {}) });
    };
    const applyStatus = (status: CPAUpdateStatus) => {
      const staleTerminalIntent =
        !!view.intent &&
        terminal(view.stage) &&
        (status.state === 'up_to_date' ||
          status.mode !== 'embedded' ||
          status.target_version !== view.intent.target_version ||
          status.active_artifact_id !== view.intent.expected_active_artifact_id);
      publish({
        status,
        statusError: false,
        ...(staleTerminalIntent ? { intent: null } : {}),
      });
      if (
        view.intent &&
        ['prepared', 'not_found'].includes(view.stage) &&
        !compatible(status, view.intent)
      ) {
        publish({ stage: 'context_changed' });
      }
    };
    const readStatus = async () => {
      try {
        const status = await cpaUpdateApi.status(connection, waiting.signal);
        if (!alive) return null;
        applyStatus(status);
        return status;
      } catch {
        publish({ statusError: true });
        return null;
      }
    };
    const complete = async (
      stage: 'confirmed_failed' | 'succeeded' | 'already_applied',
      failureCode?: string
    ) => {
      const current = view.intent!;
      unrecordedOperation = null;
      publish({
        stage,
        failureCode,
        prepareProgress: null,
        recoveredSession: false,
        intent: withoutUnrecordedContextChanged(current),
      });
      try {
        await clearCPAUpdateIntent(scope, current, waiting.signal);
        publish({ storageError: null, recoveryRecord: { raw: null } });
      } catch (error) {
        blockStorage(error);
      }
    };
    const prepared = async (): Promise<void> => {
      const previous = view.intent!;
      unrecordedOperation = null;
      publish({ stage: 'prepared', failureCode: undefined, prepareProgress: null });
      // Reobserving prepared evidence must not rewrite its revision and cause
      // storage-event ping-pong between tabs.
      if (previous.client_stage === 'prepared' && !previous.unrecorded_context_changed) return;
      const next: CPAUpdateIntent = {
        ...withoutUnrecordedContextChanged(previous),
        client_stage: 'prepared',
        updated_at_ms: Math.max(Date.now(), previous.updated_at_ms),
      };
      try {
        await persistCPAUpdateIntent(scope, next, previous, waiting.signal);
        publish({
          intent: next,
          recoveryRecord: { raw: JSON.stringify(next) },
          storageError: null,
        });
      } catch (error) {
        if (alive && error instanceof CPAUpdateStorageError && error.kind === 'changed') {
          await refresh();
          return;
        }
        blockStorage(error);
      }
    };
    const observe = async (): Promise<void> => {
      const intent = view.intent;
      if (!intent || !alive) return;
      try {
        const result = await cpaUpdateApi.observe(connection, intent.phase, intent, waiting.signal);
        if (!alive) return;
        if (result.state === 'succeeded') {
          if (intent.phase === 'prepare') await prepared();
          else await complete('succeeded');
        } else if (result.state === 'failed') {
          await complete('confirmed_failed', result.failure_code);
        } else {
          const hasProgress =
            result.stage_phase !== undefined ||
            result.received_bytes !== undefined ||
            result.total_bytes !== undefined;
          publish({
            stage: result.state,
            failureCode: undefined,
            ...(hasProgress
              ? {
                  prepareProgress: mergePrepareProgress(view.prepareProgress, {
                    phase: result.stage_phase,
                    receivedBytes: result.received_bytes,
                    totalBytes: result.total_bytes,
                  }),
                }
              : {}),
          });
        }
      } catch (error) {
        publish({
          prepareProgress: null,
          stage:
            error instanceof CPAUpdateAPIError &&
            (error.kind === 'context_changed' || error.kind === 'rejected')
              ? 'context_changed'
              : 'outcome_unknown',
        });
      }
    };
    const refresh = async (): Promise<void> => {
      try {
        const record = readCPAUpdateRecord(scope);
        let stored: CPAUpdateIntent | null;
        try {
          stored = parseCPAUpdateRecord(record);
        } catch (error) {
          publish({ recoveryRecord: record });
          throw error;
        }
        if (stored) {
          if (
            view.intent &&
            unresolved(view) &&
            (!sameFlow(stored, view.intent) ||
              (view.intent.phase === 'activate' && stored.phase !== 'activate'))
          ) {
            throw new CPAUpdateStorageError('changed');
          }
          const isSameActiveFlow = view.initialized && !!view.intent && sameFlow(stored, view.intent);
          const isUnrecordedContextChanged =
            !!stored.unrecorded_context_changed ||
            (isSameActiveFlow &&
              view.stage === 'context_changed' &&
              unrecordedOperation?.request_id === stored.request_id &&
              unrecordedOperation?.phase === stored.phase);
          let nextIntent = stored;
          let nextRecord = record;
          if (!isUnrecordedContextChanged) {
            unrecordedOperation = null;
          } else if (!stored.unrecorded_context_changed) {
            const patched: CPAUpdateIntent = {
              ...withoutUnrecordedContextChanged(stored),
              unrecorded_context_changed: true,
              updated_at_ms: Math.max(Date.now(), stored.updated_at_ms),
            };
            publish({
              stage: 'context_changed',
              failureCode: undefined,
              prepareProgress: null,
            });
            await persistCPAUpdateIntent(scope, patched, stored, waiting.signal);
            if (!alive) return;
            nextIntent = patched;
            nextRecord = { raw: JSON.stringify(patched) };
          }
          publish({
            intent: nextIntent,
            recoveryRecord: nextRecord,
            storageError: null,
            recoveredSession: isSameActiveFlow ? view.recoveredSession : true,
            ...(isUnrecordedContextChanged
              ? { stage: 'context_changed', failureCode: undefined, prepareProgress: null }
              : !isSameActiveFlow
                ? { stage: 'recovering', failureCode: undefined, prepareProgress: null }
                : {}),
          });
          if (!isUnrecordedContextChanged) {
            await observe();
          }
        } else if (unresolved(view)) {
          throw new CPAUpdateStorageError('changed');
        }
      } catch (error) {
        blockStorage(error);
      }
      if (alive) await readStatus();
    };
    const schedule = () => {
      stopTimer();
      if (
        alive &&
        !view.storageError &&
        document.visibilityState === 'visible' &&
        ['accepted', 'running'].includes(view.stage)
      ) {
        timer = window.setTimeout(() => {
          void run(refresh);
        }, 3000);
      }
    };
    const run = async (work: () => Promise<void>) => {
      if (!alive || pending) return;
      pending = true;
      stopTimer();
      publish({ busy: true });
      try {
        await work();
      } finally {
        pending = false;
        publish({ busy: false, initialized: true });
        schedule();
      }
    };
    const submit = async (intent: CPAUpdateIntent, options?: CPAUpdatePrepareOptions) => {
      unrecordedOperation = null;
      publish({
        intent,
        recoveryRecord: { raw: JSON.stringify(intent) },
        stage: 'submitting',
        recoveredSession: false,
        prepareProgress:
          intent.phase === 'prepare'
            ? {
                phase: options?.uploadFile ? 'uploading' : 'resolving',
                receivedBytes: 0,
                totalBytes: options?.uploadFile ? options.uploadFile.size : 0,
              }
            : null,
        failureCode: undefined,
      });
      let progressWatching = true;
      let progressPollTimer: number | undefined;
      const stopProgressWatch = () => {
        progressWatching = false;
        if (progressPollTimer !== undefined) window.clearTimeout(progressPollTimer);
        progressPollTimer = undefined;
        waiting.signal.removeEventListener('abort', stopProgressWatch);
      };
      waiting.signal.addEventListener('abort', stopProgressWatch, { once: true });
      if (intent.phase === 'prepare') {
        const pollProgress = async () => {
          if (!alive || !progressWatching) return;
          try {
            const res = await cpaUpdateApi.observe(connection, 'prepare', intent, waiting.signal);
            if (!alive || !progressWatching) return;
            if (
              res.stage_phase !== undefined ||
              res.received_bytes !== undefined ||
              res.total_bytes !== undefined
            ) {
              publish({
                prepareProgress: mergePrepareProgress(view.prepareProgress, {
                  phase: res.stage_phase,
                  receivedBytes: res.received_bytes,
                  totalBytes: res.total_bytes,
                }),
              });
            }
          } catch {
            // Progress polling is strictly best-effort while POST is in flight.
          }
          if (alive && progressWatching) {
            progressPollTimer = window.setTimeout(() => {
              void pollProgress();
            }, 1000);
          }
        };
        progressPollTimer = window.setTimeout(() => {
          void pollProgress();
        }, 700);
      }
      try {
        let result;
        if (intent.phase === 'prepare' && intent.source_mode === 'upload') {
          if (!options?.uploadFile) {
            throw new CPAUpdateAPIError('rejected', 'invalid_request');
          }
          result = await cpaUpdateApi.prepareUpload(
            connection,
            intent,
            options.uploadFile,
            (loaded, total) => {
              publish({
                prepareProgress: mergePrepareProgress(view.prepareProgress, {
                  phase: loaded >= total && total > 0 ? 'verifying' : 'uploading',
                  receivedBytes: loaded,
                  totalBytes: total,
                }),
              });
            },
            waiting.signal
          );
        } else {
          const requestIntent: CPAUpdateRequest =
            intent.phase === 'prepare' && intent.source_mode === 'cdn'
              ? {
                  request_id: intent.request_id,
                  target_version: intent.target_version,
                  expected_active_artifact_id: intent.expected_active_artifact_id,
                  source_mode: 'cdn',
                  cdn_prefix: intent.cdn_prefix,
                }
              : {
                  request_id: intent.request_id,
                  target_version: intent.target_version,
                  expected_active_artifact_id: intent.expected_active_artifact_id,
                };
          result = await cpaUpdateApi.mutate(connection, intent.phase, requestIntent, waiting.signal);
        }
        stopProgressWatch();
        if (!alive) return;
        if (result.state === 'failed') await complete('confirmed_failed', result.failure_code);
        else if (intent.phase === 'prepare') await prepared();
        else await complete(result.already_applied ? 'already_applied' : 'succeeded');
      } catch (error) {
        stopProgressWatch();
        if (!alive) return;
        if (error instanceof CPAUpdateAPIError && error.kind === 'rejected') {
          await complete('confirmed_failed', error.code);
        } else if (error instanceof CPAUpdateAPIError && error.kind === 'context_changed') {
          unrecordedOperation = { request_id: intent.request_id, phase: intent.phase };
          publish({ stage: 'context_changed', prepareProgress: null });
          const next: CPAUpdateIntent = {
            ...withoutUnrecordedContextChanged(intent),
            unrecorded_context_changed: true,
            updated_at_ms: Math.max(Date.now(), intent.updated_at_ms),
          };
          try {
            await persistCPAUpdateIntent(scope, next, intent, waiting.signal);
            publish({
              intent: next,
              recoveryRecord: { raw: JSON.stringify(next) },
              storageError: null,
            });
          } catch (persistError) {
            if (!alive) return;
            if (
              persistError instanceof CPAUpdateStorageError &&
              persistError.kind === 'changed'
            ) {
              await refresh();
              return;
            }
            blockStorage(persistError);
          }
        } else {
          publish({ stage: 'outcome_unknown', prepareProgress: null });
          // A browser timeout is not a Runtime failure. Never POST from recovery.
          await observe();
        }
      }
      if (alive) await readStatus();
    };
    const persistAndSubmit = async (
      next: CPAUpdateIntent,
      previous: CPAUpdateIntent | null,
      options?: CPAUpdatePrepareOptions
    ) => {
      try {
        await persistCPAUpdateIntent(scope, next, previous, waiting.signal);
      } catch (error) {
        blockStorage(error);
        return;
      }
      if (alive) await submit(next, options);
    };
    const controls: Actions = {
      refresh: () => run(refresh),
      check: async () => {
        if (!canCheck(view)) return;
        await run(async () => {
          try {
            // Another tab may have started a flow since this page last rendered.
            const dispatched = await withCPAUpdateIntentLock(
              scope,
              () => {
                if (readCPAUpdateIntent(scope)) return null;
                // Guard dispatch synchronously, then await the response outside
                // the IndexedDB transaction (which cannot span a network wait).
                const response = cpaUpdateApi.check(connection, waiting.signal);
                // Coordination can fail before its promise is returned to us.
                void response.catch(() => {});
                return { response };
              },
              waiting.signal
            );
            if (!alive) return;
            if (!dispatched) {
              await refresh();
              return;
            }
            const status = await dispatched.response;
            if (alive) applyStatus(status);
          } catch (error) {
            if (error instanceof CPAUpdateStorageError) blockStorage(error);
            else publish({ statusError: true });
          }
        });
      },
      prepare: async (options?: CPAUpdatePrepareOptions) => {
        if (!canPrepare(view)) return;
        if (options?.uploadFile) {
          if (
            typeof options.uploadFile.size !== 'number' ||
            !Number.isFinite(options.uploadFile.size) ||
            options.uploadFile.size <= 0 ||
            options.uploadFile.size > CPA_UPDATE_MAX_UPLOAD_BYTES
          ) {
            return;
          }
        } else if (options?.sourceMode === 'cdn') {
          if (!isValidHttpsCDNPrefix(options.cdnPrefix?.trim() ?? '')) return;
        }
        const displayed = view.status!;
        await run(async () => {
          const fresh = await readStatus();
          if (!alive || !fresh || !canPrepareCPAUpdate(fresh)) return;
          if (
            displayed.target_version !== fresh.target_version ||
            displayed.active_artifact_id !== fresh.active_artifact_id
          ) {
            let hasPersistedIntent = (view.recoveryRecord?.raw ?? null) !== null;
            try {
              hasPersistedIntent = !!readCPAUpdateIntent(scope);
            } catch {
              hasPersistedIntent = (view.recoveryRecord?.raw ?? null) !== null;
            }
            publish({
              stage: 'context_changed',
              ...(terminal(view.stage) || !hasPersistedIntent ? { intent: null } : {}),
            });
            return;
          }
          try {
            const claimed = await claimCPAUpdateIntent(
              scope,
              () => {
                const now = Date.now();
                const sourceFields = options?.uploadFile
                  ? { source_mode: 'upload' as const }
                  : options?.sourceMode === 'cdn'
                    ? { source_mode: 'cdn' as const, cdn_prefix: options.cdnPrefix!.trim() }
                    : { source_mode: 'github' as const };
                return {
                  schema_version: 1,
                  request_id: createCPAUpdateRequestID(),
                  target_version: fresh.target_version!,
                  expected_active_artifact_id: fresh.active_artifact_id!,
                  phase: 'prepare',
                  client_stage: 'submitted',
                  ...sourceFields,
                  created_at_ms: now,
                  updated_at_ms: now,
                };
              },
              waiting.signal
            );
            if (!alive) return;
            if (!claimed.claimed) {
              await refresh();
              return;
            }
            await submit(claimed.intent, options);
          } catch (error) {
            blockStorage(error);
          }
        });
      },
      prepareUpload: async (file: Blob) => {
        if (
          !canPrepare(view) ||
          !file ||
          typeof file.size !== 'number' ||
          !Number.isFinite(file.size) ||
          file.size <= 0 ||
          file.size > CPA_UPDATE_MAX_UPLOAD_BYTES
        ) {
          return;
        }
        await controls.prepare({ uploadFile: file });
      },
      activate: async (expected) => {
        if (
          !ready(view) ||
          view.stage !== 'prepared' ||
          !sameCPAUpdateIntent(view.intent, expected)
        )
          return;
        await run(async () => {
          const fresh = await readStatus();
          if (!alive || !fresh) return;
          if (!compatible(fresh, expected)) {
            publish({ stage: 'context_changed' });
            return;
          }
          await persistAndSubmit(
            {
              ...withoutUnrecordedContextChanged(expected),
              phase: 'activate',
              client_stage: 'submitted',
              updated_at_ms: Math.max(Date.now(), expected.updated_at_ms),
            },
            expected
          );
        });
      },
      retry: async (expected) => {
        if (
          !ready(view) ||
          view.stage !== 'not_found' ||
          !sameCPAUpdateIntent(view.intent, expected) ||
          !canReplaySource(expected)
        )
          return;
        await run(async () => {
          const fresh = await readStatus();
          if (!alive || !fresh) return;
          if (!compatible(fresh, expected)) {
            publish({ stage: 'context_changed' });
            return;
          }
          await persistAndSubmit(
            {
              ...withoutUnrecordedContextChanged(expected),
              client_stage: 'submitted',
              updated_at_ms: Math.max(Date.now(), expected.updated_at_ms),
            },
            expected
          );
        });
      },
      forget: (expected) =>
        run(async () => {
          try {
            await clearCPAUpdateIntent(scope, expected, waiting.signal);
            unrecordedOperation = null;
            publish({
              intent: null,
              recoveryRecord: { raw: null },
              stage: 'idle',
              recoveredSession: false,
              prepareProgress: null,
              storageError: null,
              failureCode: undefined,
            });
          } catch (error) {
            blockStorage(error);
          }
        }),
    };
    if (!context.available) {
      publish({ initialized: true });
      actions.current = null;
      return () => {
        alive = false;
      };
    }
    actions.current = { context, controls };
    const visible = () => {
      if (document.visibilityState !== 'visible') stopTimer();
      else if (unresolved(view)) void run(refresh);
    };
    const online = () => {
      if (unresolved(view)) void run(refresh);
    };
    const storageChanged = (event: StorageEvent) => {
      if (event.key === scope || event.key === null) void run(refresh);
    };
    document.addEventListener('visibilitychange', visible);
    window.addEventListener('online', online);
    window.addEventListener('storage', storageChanged);
    void run(refresh);
    return () => {
      alive = false;
      stopTimer();
      // Abort browser waiting. Non-upload Runtime operations survive, while an
      // in-flight upload request is canceled with its body stream.
      waiting.abort();
      document.removeEventListener('visibilitychange', visible);
      window.removeEventListener('online', online);
      window.removeEventListener('storage', storageChanged);
      if (actions.current?.context === context) actions.current = null;
    };
  }, [context]);

  const invoke = useCallback(
    (
      name: keyof Actions,
      arg?: CPAUpdateIntent | CPAUpdateRecordSnapshot | CPAUpdatePrepareOptions | Blob
    ) => {
      const current = actions.current;
      // Old confirmation callbacks cannot act under a different auth/service scope.
      if (current?.context !== context) return Promise.resolve();
      if (name === 'activate' || name === 'retry') {
        return arg && typeof arg === 'object' && 'request_id' in arg
          ? current.controls[name](arg)
          : Promise.resolve();
      }
      if (name === 'forget') {
        return arg && typeof arg === 'object' && 'raw' in arg
          ? current.controls.forget(arg)
          : Promise.resolve();
      }
      if (name === 'prepare') {
        return current.controls.prepare(arg as CPAUpdatePrepareOptions | undefined);
      }
      if (name === 'prepareUpload') {
        return arg instanceof Blob ? current.controls.prepareUpload(arg) : Promise.resolve();
      }
      return current.controls[name]();
    },
    [context]
  );
  const refresh = useCallback(() => invoke('refresh'), [invoke]);
  const view = state.context === context ? state.view : initialSnapshot();
  return {
    ...view,
    available,
    scope: context.scope,
    unresolved: unresolved(view),
    canCheck: available && canCheck(view),
    canPrepare: available && canPrepare(view),
    canActivate:
      available &&
      ready(view) &&
      !view.statusError &&
      view.stage === 'prepared' &&
      !!view.intent &&
      compatible(view.status, view.intent),
    canRetry:
      available &&
      ready(view) &&
      !view.statusError &&
      view.stage === 'not_found' &&
      !!view.intent &&
      canReplaySource(view.intent) &&
      compatible(view.status, view.intent),
    refresh,
    check: () => invoke('check'),
    prepare: (options?: CPAUpdatePrepareOptions) => invoke('prepare', options),
    prepareUpload: (file: Blob) => invoke('prepareUpload', file),
    activate: (intent: CPAUpdateIntent) => invoke('activate', intent),
    retry: (intent: CPAUpdateIntent) => invoke('retry', intent),
    forget: (expected: CPAUpdateRecordSnapshot) => invoke('forget', expected),
  };
}

export type CPAUpdateFlow = ReturnType<typeof useCPAUpdateFlow>;
