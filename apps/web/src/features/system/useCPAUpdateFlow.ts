import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useAuthStore } from '@/stores';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { isDemoMode } from '@/features/demo/demoMode';
import { normalizeUsageServiceBase } from '@/services/api/usageService';
import {
  canPrepareCPAUpdate,
  cpaUpdateApi,
  CPAUpdateAPIError,
  type CPAUpdateStatus,
} from './cpaUpdateApi';
import {
  clearCPAUpdateIntent,
  cpaUpdateStorageKey,
  CPAUpdateStorageError,
  createCPAUpdateRequestID,
  persistCPAUpdateIntent,
  readCPAUpdateIntent,
  sameCPAUpdateIntent,
  type CPAUpdateIntent,
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

interface Snapshot {
  status: CPAUpdateStatus | null;
  statusError: boolean;
  intent: CPAUpdateIntent | null;
  stage: CPAUpdateFlowStage;
  initialized: boolean;
  busy: boolean;
  storageError: CPAUpdateStorageError['kind'] | null;
  failureCode?: string;
}

const initialSnapshot = (): Snapshot => ({
  status: null,
  statusError: false,
  intent: null,
  stage: 'idle',
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
  left.expected_active_artifact_id === right.expected_active_artifact_id;

interface Actions {
  refresh: () => Promise<void>;
  check: () => Promise<void>;
  prepare: () => Promise<void>;
  activate: (expected: CPAUpdateIntent) => Promise<void>;
  retry: (expected: CPAUpdateIntent) => Promise<void>;
  forget: () => Promise<void>;
}

export function useCPAUpdateFlow() {
  const authenticated = useAuthStore((state) => state.isAuthenticated);
  const key = useAuthStore((state) => state.managementKey);
  const availability = usePanelFeatureAvailability();
  const base = normalizeUsageServiceBase(availability.managerServiceBase);
  const demo = __DEMO_SITE__ && isDemoMode();
  const available =
    authenticated && !!key && !!base && availability.managerServiceAvailable && !demo;
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
      publish({ status, statusError: false });
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
    const complete = (
      stage: 'confirmed_failed' | 'succeeded' | 'already_applied',
      failureCode?: string
    ) => {
      publish({ stage, failureCode });
      try {
        clearCPAUpdateIntent(scope, view.intent!);
        publish({ storageError: null });
      } catch (error) {
        blockStorage(error);
      }
    };
    const prepared = () => {
      const previous = view.intent!;
      const next: CPAUpdateIntent = {
        ...previous,
        client_stage: 'prepared',
        updated_at_ms: Math.max(Date.now(), previous.updated_at_ms),
      };
      publish({ stage: 'prepared', failureCode: undefined });
      try {
        persistCPAUpdateIntent(scope, next, previous);
        publish({ intent: next, storageError: null });
      } catch (error) {
        blockStorage(error);
      }
    };
    const observe = async () => {
      const intent = view.intent;
      if (!intent || !alive) return;
      try {
        const result = await cpaUpdateApi.observe(connection, intent.phase, intent, waiting.signal);
        if (!alive) return;
        if (result.state === 'succeeded') {
          if (intent.phase === 'prepare') prepared();
          else complete('succeeded');
        } else if (result.state === 'failed') {
          complete('confirmed_failed', result.failure_code);
        } else {
          publish({ stage: result.state, failureCode: undefined });
        }
      } catch (error) {
        publish({
          stage:
            error instanceof CPAUpdateAPIError && error.kind === 'context_changed'
              ? 'context_changed'
              : 'outcome_unknown',
        });
      }
    };
    const refresh = async () => {
      try {
        const stored = readCPAUpdateIntent(scope);
        if (stored) {
          if (
            view.intent &&
            unresolved(view) &&
            (!sameFlow(stored, view.intent) ||
              (view.intent.phase === 'activate' && stored.phase !== 'activate'))
          ) {
            throw new CPAUpdateStorageError('changed');
          }
          publish({
            intent: stored,
            storageError: null,
            ...(!view.initialized ? { stage: 'recovering' } : {}),
          });
          // Recovery always queries durable evidence, even for client_stage=prepared.
          await observe();
        } else if (unresolved(view)) {
          throw new CPAUpdateStorageError('changed');
        } else {
          publish({ storageError: null });
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
    const submit = async (intent: CPAUpdateIntent) => {
      publish({ intent, stage: 'submitting', failureCode: undefined });
      try {
        const result = await cpaUpdateApi.mutate(connection, intent.phase, intent, waiting.signal);
        if (!alive) return;
        if (result.state === 'failed') complete('confirmed_failed', result.failure_code);
        else if (intent.phase === 'prepare') prepared();
        else complete(result.already_applied ? 'already_applied' : 'succeeded');
      } catch (error) {
        if (!alive) return;
        if (error instanceof CPAUpdateAPIError && error.kind === 'context_changed') {
          publish({ stage: 'context_changed' });
        } else {
          publish({ stage: 'outcome_unknown' });
          // A browser timeout is not a Runtime failure. Never POST from recovery.
          await observe();
        }
      }
      if (alive) await readStatus();
    };
    const persistAndSubmit = async (next: CPAUpdateIntent, previous: CPAUpdateIntent | null) => {
      try {
        persistCPAUpdateIntent(scope, next, previous);
      } catch (error) {
        blockStorage(error);
        return;
      }
      await submit(next);
    };
    const controls: Actions = {
      refresh: () => run(refresh),
      check: async () => {
        if (!canCheck(view)) return;
        await run(async () => {
          try {
            // Another tab may have started a flow since this page last rendered.
            if (readCPAUpdateIntent(scope)) {
              await refresh();
              return;
            }
            const status = await cpaUpdateApi.check(connection, waiting.signal);
            if (alive) applyStatus(status);
          } catch (error) {
            if (error instanceof CPAUpdateStorageError) blockStorage(error);
            else publish({ statusError: true });
          }
        });
      },
      prepare: async () => {
        if (!canPrepare(view)) return;
        const displayed = view.status!;
        await run(async () => {
          const fresh = await readStatus();
          if (!alive || !fresh || !canPrepareCPAUpdate(fresh)) return;
          if (
            displayed.target_version !== fresh.target_version ||
            displayed.active_artifact_id !== fresh.active_artifact_id
          ) {
            publish({ stage: 'context_changed' });
            return;
          }
          try {
            if (readCPAUpdateIntent(scope)) {
              await refresh();
              return;
            }
            const now = Date.now();
            const next: CPAUpdateIntent = {
              schema_version: 1,
              request_id: createCPAUpdateRequestID(),
              target_version: fresh.target_version!,
              expected_active_artifact_id: fresh.active_artifact_id!,
              phase: 'prepare',
              client_stage: 'submitted',
              created_at_ms: now,
              updated_at_ms: now,
            };
            await persistAndSubmit(next, null);
          } catch (error) {
            blockStorage(error);
          }
        });
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
              ...expected,
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
              ...expected,
              client_stage: 'submitted',
              updated_at_ms: Math.max(Date.now(), expected.updated_at_ms),
            },
            expected
          );
        });
      },
      forget: () =>
        run(async () => {
          try {
            clearCPAUpdateIntent(scope);
            publish({ intent: null, stage: 'idle', storageError: null, failureCode: undefined });
            await readStatus();
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
      // Abort only browser waiting. The persisted intent and Runtime operation survive.
      waiting.abort();
      document.removeEventListener('visibilitychange', visible);
      window.removeEventListener('online', online);
      window.removeEventListener('storage', storageChanged);
      if (actions.current?.context === context) actions.current = null;
    };
  }, [context]);

  const invoke = useCallback(
    (name: keyof Actions, intent?: CPAUpdateIntent) => {
      const current = actions.current;
      // Old confirmation callbacks cannot act under a different auth/service scope.
      if (current?.context !== context) return Promise.resolve();
      if (name === 'activate' || name === 'retry') {
        return intent ? current.controls[name](intent) : Promise.resolve();
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
    demo,
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
      compatible(view.status, view.intent),
    refresh,
    check: () => invoke('check'),
    prepare: () => invoke('prepare'),
    activate: (intent: CPAUpdateIntent) => invoke('activate', intent),
    retry: (intent: CPAUpdateIntent) => invoke('retry', intent),
    forget: () => invoke('forget'),
  };
}

export type CPAUpdateFlow = ReturnType<typeof useCPAUpdateFlow>;
