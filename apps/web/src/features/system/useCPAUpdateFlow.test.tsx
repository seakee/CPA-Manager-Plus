import axios, { AxiosError, type AxiosRequestConfig, type AxiosResponse } from 'axios';
import { useLayoutEffect } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useCPAUpdateFlow, type CPAUpdateFlow, type CPAUpdateFlowStage } from './useCPAUpdateFlow';
import { cpaUpdateStorageKey, readCPAUpdateIntent } from './cpaUpdateIntentStorage';
import type {
  CPAUpdateIdentity,
  CPAUpdateMutationResult,
  CPAUpdateObservation,
  CPAUpdatePhase,
  CPAUpdateRequest,
  CPAUpdateStatus,
} from './cpaUpdateApi';
import {
  memoryStorage,
  mutationResult,
  observationResult,
  originalArtifact,
  replacementArtifact,
  updateIntent,
  updateStatus,
} from './cpaUpdateTestFixtures';

const mocks = vi.hoisted(() => ({
  auth: { isAuthenticated: true, managementKey: 'key-a' },
  base: 'https://manager.test',
  available: true,
  demo: false,
}));
vi.mock('@/stores', () => ({
  useAuthStore: (select: (state: typeof mocks.auth) => unknown) => select(mocks.auth),
}));
vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => ({
    managerServiceAvailable: mocks.available,
    managerServiceBase: mocks.base,
  }),
}));
vi.mock('@/features/demo/demoMode', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/demo/demoMode')>()),
  isDemoMode: () => mocks.demo,
}));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const request = vi.spyOn(axios, 'request');
let renderer: ReactTestRenderer | undefined;
let flow: CPAUpdateFlow;
let stages: CPAUpdateFlowStage[];
let storage: Storage;
let status: CPAUpdateStatus;
let readStatus: () => Promise<CPAUpdateStatus>;
let mutate: (
  phase: CPAUpdatePhase,
  body: CPAUpdateRequest,
  config: AxiosRequestConfig
) => Promise<CPAUpdateMutationResult>;
let observe: (phase: CPAUpdatePhase, body: CPAUpdateIdentity) => Promise<CPAUpdateObservation>;
let uuid: ReturnType<typeof vi.fn>;
const storageKey = () => cpaUpdateStorageKey(mocks.base, mocks.auth.managementKey);
const calls = (suffix: string) =>
  request.mock.calls.filter(
    ([config]) => config.url === mocks.base + '/usage-service/runtime/updates' + suffix
  );
const posts = () => request.mock.calls.filter(([config]) => config.method === 'POST');
const queries = () => request.mock.calls.filter(([config]) => config.url?.includes('/operations/'));
const httpError = (code: string, statusCode = 409) =>
  new AxiosError('private message', 'ERR_BAD_RESPONSE', undefined, undefined, {
    status: statusCode,
    data: { code, error: 'private message' },
  } as AxiosResponse);
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { resolve, reject, promise };
}
function Harness() {
  const value = useCPAUpdateFlow();
  useLayoutEffect(() => {
    flow = value;
    stages.push(value.stage);
  }, [value]);
  return null;
}
const mount = async () => {
  await act(async () => {
    if (renderer) renderer.update(<Harness />);
    else renderer = create(<Harness />);
  });
};
const unmount = async () => {
  await act(async () => {
    renderer?.unmount();
  });
  renderer = undefined;
};
const tick = async (ms: number) => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
};
const storeIntent = (intent = updateIntent()) =>
  storage.setItem(storageKey(), JSON.stringify(intent));

beforeEach(() => {
  vi.useFakeTimers();
  mocks.auth = { isAuthenticated: true, managementKey: 'key-a' };
  mocks.base = 'https://manager.test';
  mocks.available = true;
  mocks.demo = false;
  storage = memoryStorage();
  vi.stubGlobal('localStorage', storage);
  vi.stubGlobal('document', Object.assign(new EventTarget(), { visibilityState: 'visible' }));
  vi.stubGlobal(
    'window',
    Object.assign(new EventTarget(), {
      setTimeout: globalThis.setTimeout,
      clearTimeout: globalThis.clearTimeout,
    })
  );
  let sequence = 0;
  uuid = vi.fn(() => '00000000-0000-4000-8000-' + String(++sequence).padStart(12, '0'));
  vi.stubGlobal('crypto', { randomUUID: uuid });
  status = updateStatus();
  stages = [];
  readStatus = async () => status;
  mutate = async (phase, body) => mutationResult(phase, body);
  observe = async (phase, body) => observationResult(updateIntent({ ...body, phase }), 'running');
  request.mockReset();
  request.mockImplementation(async (config) => {
    const suffix = config.url!.split('/usage-service/runtime/updates')[1];
    let data: unknown;
    if (suffix === '' || suffix === '/check') data = await readStatus();
    else if (suffix.startsWith('/operations/')) {
      data = await observe(suffix.slice('/operations/'.length) as CPAUpdatePhase, config.params);
    } else if (suffix === '/prepare' || suffix === '/activate') {
      data = await mutate(
        suffix.slice(1) as CPAUpdatePhase,
        config.data as CPAUpdateRequest,
        config
      );
    } else throw new Error('Unexpected CPA request: ' + suffix);
    return { data } as AxiosResponse;
  });
});
afterEach(async () => {
  await unmount();
  vi.clearAllTimers();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('CPA page lifecycle and explicit commands', () => {
  it('loads and refreshes using GET, with no implicit check or mutation', async () => {
    await mount();
    expect(flow.initialized).toBe(true);
    expect(calls('')).toHaveLength(1);
    await act(async () => {
      await flow.refresh();
    });
    expect(calls('')).toHaveLength(2);
    expect(posts()).toHaveLength(0);
    expect(queries()).toHaveLength(0);
  });

  it('checks exactly once for repeated clicks while a check is pending', async () => {
    await mount();
    const waiting = deferred<CPAUpdateStatus>();
    readStatus = () => waiting.promise;
    await act(async () => {
      void flow.check();
      void flow.check();
    });
    expect(calls('/check')).toHaveLength(1);
    await act(async () => {
      waiting.resolve(status);
    });
    expect(flow.canCheck).toBe(true);
  });

  it('keeps External at zero POSTs, including programmatic actions', async () => {
    status = updateStatus({ mode: 'external', state: 'managed_externally' });
    await mount();
    await act(async () => {
      await flow.check();
      await flow.prepare();
      await flow.activate(updateIntent());
      await flow.retry(updateIntent());
    });
    expect(flow.status?.state).toBe('managed_externally');
    expect(flow.canPrepare).toBe(false);
    expect(posts()).toHaveLength(0);
  });

  it.each([
    { stale: true },
    { last_error: 'failed' },
    { actionable: false },
    { prepare_supported: false },
    { activate_supported: false },
    { active_artifact_id: undefined },
    { state: 'unknown_version' as const },
  ])('withholds prepare when required status evidence is missing: %o', async (patch) => {
    status = updateStatus(patch);
    await mount();
    await act(async () => {
      await flow.prepare();
    });
    expect(posts()).toHaveLength(0);
  });

  it('persists and verifies the complete intent before prepare, then retains prepared evidence', async () => {
    await mount();
    mutate = async (phase, body) => {
      expect(phase).toBe('prepare');
      expect(readCPAUpdateIntent(storageKey())).toMatchObject({
        ...body,
        phase,
        schema_version: 1,
        client_stage: 'submitted',
      });
      expect(body.expected_active_artifact_id).toBe(originalArtifact);
      return mutationResult(phase, body);
    };
    await act(async () => {
      await flow.prepare();
    });
    expect(flow.stage).toBe('prepared');
    expect(readCPAUpdateIntent(storageKey())?.client_stage).toBe('prepared');
    expect(flow.canActivate).toBe(true);
    expect(flow.canCheck).toBe(false);
    await act(async () => {
      await flow.prepare();
      await flow.check();
    });
    expect(calls('/prepare')).toHaveLength(1);
    expect(calls('/check')).toHaveLength(0);
    expect(uuid).toHaveBeenCalledTimes(1);
  });

  it.each(['write', 'read_back', 'no_op'] as const)(
    'submits zero mutations if persistence fails at %s',
    async (failure) => {
      await mount();
      const originalGet = storage.getItem.bind(storage);
      let written = false;
      if (failure === 'write')
        storage.setItem = () => {
          throw new Error('storage blocked');
        };
      if (failure === 'no_op') storage.setItem = () => {};
      if (failure === 'read_back') {
        const originalSet = storage.setItem.bind(storage);
        storage.setItem = (key, value) => {
          written = true;
          originalSet(key, value);
        };
        storage.getItem = (key) => {
          if (written) throw new Error('read blocked');
          return originalGet(key);
        };
      }
      await act(async () => {
        await flow.prepare();
      });
      expect(flow.storageError).toBe('unavailable');
      expect(posts()).toHaveLength(0);
    }
  );

  it('does not change the displayed target/guard under a new flow when fresh status differs', async () => {
    await mount();
    status = updateStatus({ target_version: '7.3.0', active_artifact_id: replacementArtifact });
    await act(async () => {
      await flow.prepare();
    });
    expect(flow.stage).toBe('context_changed');
    expect(posts()).toHaveLength(0);
    expect(uuid).not.toHaveBeenCalled();
  });

  it('requires prepare success, then persists activate with the same ID and original guards', async () => {
    await mount();
    await act(async () => {
      await flow.activate(updateIntent());
    });
    expect(posts()).toHaveLength(0);
    await act(async () => {
      await flow.prepare();
    });
    const prepared = flow.intent!;
    mutate = async (phase, body) => {
      expect(phase).toBe('activate');
      expect(body).toEqual({
        request_id: prepared.request_id,
        target_version: prepared.target_version,
        expected_active_artifact_id: prepared.expected_active_artifact_id,
      });
      expect(readCPAUpdateIntent(storageKey())).toMatchObject({
        ...body,
        phase: 'activate',
        client_stage: 'submitted',
      });
      return mutationResult(phase, body);
    };
    await act(async () => {
      await flow.activate(prepared);
    });
    expect(flow.stage).toBe('succeeded');
    expect(readCPAUpdateIntent(storageKey())).toBeNull();
    expect(uuid).toHaveBeenCalledTimes(1);
  });

  it('does not activate when the phase transition cannot be persisted', async () => {
    await mount();
    await act(async () => {
      await flow.prepare();
    });
    storage.setItem = () => {
      throw new Error('quota');
    };
    await act(async () => {
      await flow.activate(flow.intent!);
    });
    expect(calls('/activate')).toHaveLength(0);
    expect(flow.storageError).toBe('unavailable');
  });
});

describe('CPA response loss and durable observation', () => {
  it('treats the 30 second timeout as unknown and immediately starts GET observation', async () => {
    await mount();
    const observed = deferred<CPAUpdateObservation>();
    observe = () => observed.promise;
    mutate = (_phase, _body, config) =>
      new Promise((_resolve, reject) => {
        setTimeout(() => reject(new AxiosError('timeout', 'ECONNABORTED')), config.timeout);
      });
    await act(async () => {
      void flow.prepare();
    });
    await tick(29_999);
    expect(flow.stage).toBe('submitting');
    expect(queries()).toHaveLength(0);
    await tick(1);
    expect(flow.stage).toBe('outcome_unknown');
    expect(stages).not.toContain('confirmed_failed');
    expect(queries()).toHaveLength(1);
    expect(queries()[0][0].method).toBe('GET');
    const intent = flow.intent!;
    await act(async () => {
      observed.resolve(observationResult(intent, 'running'));
    });
    expect(flow.stage).toBe('running');
    expect(posts()).toHaveLength(1);
  });

  it.each(['prepare', 'activate'] as const)(
    'recovers refresh during %s before enabling commands',
    async (phase) => {
      const intent = updateIntent({ phase });
      storeIntent(intent);
      const observed = deferred<CPAUpdateObservation>();
      observe = () => observed.promise;
      await mount();
      expect(flow.stage).toBe('recovering');
      expect(flow.canCheck).toBe(false);
      expect(flow.canPrepare).toBe(false);
      expect(calls('')).toHaveLength(0);
      await act(async () => {
        await flow.prepare();
        await flow.check();
      });
      expect(posts()).toHaveLength(0);
      expect(queries()[0][0].params).toEqual({
        request_id: intent.request_id,
        target_version: intent.target_version,
      });
      await act(async () => {
        observed.resolve(observationResult(intent, 'succeeded'));
      });
      expect(flow.stage).toBe(phase === 'prepare' ? 'prepared' : 'succeeded');
      expect(posts()).toHaveLength(0);
    }
  );

  it('reobserves persisted prepared claims rather than trusting client_stage alone', async () => {
    storeIntent(updateIntent({ client_stage: 'prepared' }));
    observe = async (phase, body) =>
      observationResult(updateIntent({ ...body, phase }), 'not_found');
    await mount();
    expect(flow.stage).toBe('not_found');
    expect(flow.canActivate).toBe(false);
    expect(posts()).toHaveLength(0);
  });

  it.each(['accepted', 'running'] as const)(
    'polls %s every three seconds using only GET until durable success',
    async (stage) => {
      const intent = updateIntent();
      storeIntent(intent);
      let observationStage: CPAUpdateObservation['state'] = stage;
      observe = async () => observationResult(intent, observationStage);
      await mount();
      expect(flow.stage).toBe(stage);
      await tick(2999);
      expect(queries()).toHaveLength(1);
      await tick(1);
      expect(queries()).toHaveLength(2);
      observationStage = 'succeeded';
      await tick(3000);
      expect(flow.stage).toBe('prepared');
      const count = queries().length;
      await tick(12_000);
      expect(queries()).toHaveLength(count);
      expect(posts()).toHaveLength(0);
    }
  );

  it('pauses polling when hidden and queries immediately when visible again', async () => {
    storeIntent();
    await mount();
    await act(async () => {
      Object.defineProperty(document, 'visibilityState', { value: 'hidden', configurable: true });
      document.dispatchEvent(new Event('visibilitychange'));
    });
    await tick(30_000);
    expect(queries()).toHaveLength(1);
    await act(async () => {
      Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true });
      document.dispatchEvent(new Event('visibilitychange'));
    });
    expect(queries()).toHaveLength(2);
    expect(posts()).toHaveLength(0);
  });

  it.each(['prepare', 'activate'] as const)(
    'never automatically retries not_found for %s; explicit retry preserves the original request',
    async (phase) => {
      const intent = updateIntent({ phase });
      storeIntent(intent);
      observe = async () => observationResult(intent, 'not_found');
      await mount();
      await tick(30_000);
      expect(flow.stage).toBe('not_found');
      expect(posts()).toHaveLength(0);
      await act(async () => {
        await flow.refresh();
      });
      expect(posts()).toHaveLength(0);
      await act(async () => {
        await flow.retry(flow.intent!);
      });
      expect(calls('/' + phase)[0][0].data).toEqual({
        request_id: intent.request_id,
        target_version: intent.target_version,
        expected_active_artifact_id: intent.expected_active_artifact_id,
      });
      expect(uuid).not.toHaveBeenCalled();
    }
  );

  it('distinguishes already_applied replay and retains the original guard after the active artifact changed', async () => {
    const intent = updateIntent({ phase: 'activate' });
    storeIntent(intent);
    status = updateStatus({
      state: 'up_to_date',
      current_version: intent.target_version,
      active_artifact_id: replacementArtifact,
      actionable: false,
    });
    observe = async () => observationResult(intent, 'not_found');
    await mount();
    expect(flow.stage).toBe('not_found');
    expect(flow.canRetry).toBe(true);
    mutate = async (phase, body) => mutationResult(phase, body, { already_applied: true });
    await act(async () => {
      await flow.retry(flow.intent!);
    });
    expect(flow.stage).toBe('already_applied');
    expect(calls('/activate')[0][0].data).toMatchObject({
      expected_active_artifact_id: originalArtifact,
    });
  });

  it.each(['not_found', 'unavailable'] as const)(
    'does not infer success from a matching current version when observation is %s',
    async (outcome) => {
      const intent = updateIntent({ phase: 'activate' });
      storeIntent(intent);
      status = updateStatus({
        current_version: intent.target_version,
        state: 'up_to_date',
        active_artifact_id: replacementArtifact,
        actionable: false,
      });
      observe = async () => {
        if (outcome === 'unavailable') throw new AxiosError('offline', 'ERR_NETWORK');
        return observationResult(intent, 'not_found');
      };
      await mount();
      expect(flow.stage).toBe(outcome === 'not_found' ? 'not_found' : 'outcome_unknown');
      expect(flow.unresolved).toBe(true);
      expect(readCPAUpdateIntent(storageKey())).toEqual(intent);
      expect(posts()).toHaveLength(0);
    }
  );

  it('recovers after network loss and an online event without a new POST', async () => {
    await mount();
    mutate = async () => {
      throw new AxiosError('no response', 'ERR_NETWORK');
    };
    observe = async () => {
      throw new AxiosError('offline', 'ERR_NETWORK');
    };
    await act(async () => {
      await flow.prepare();
    });
    expect(flow.stage).toBe('outcome_unknown');
    const intent = flow.intent!;
    observe = async () => observationResult(intent, 'succeeded');
    await act(async () => {
      window.dispatchEvent(new Event('online'));
    });
    expect(flow.stage).toBe('prepared');
    expect(posts()).toHaveLength(1);
  });

  it('keeps uncertain operations across route leave/return and ignores the old response', async () => {
    await mount();
    const reply = deferred<CPAUpdateMutationResult>();
    mutate = () => reply.promise;
    await act(async () => {
      void flow.prepare();
    });
    const intent = flow.intent!;
    const signal = calls('/prepare')[0][0].signal;
    await unmount();
    expect(signal?.aborted).toBe(true);
    expect(readCPAUpdateIntent(storageKey())).toEqual(intent);
    await tick(30_000);
    expect(queries()).toHaveLength(0);
    await mount();
    expect(flow.stage).toBe('running');
    await act(async () => {
      reply.resolve(mutationResult('prepare', intent));
    });
    expect(flow.stage).toBe('running');
    expect(readCPAUpdateIntent(storageKey())?.client_stage).toBe('submitted');
    expect(posts()).toHaveLength(1);
  });

  it('clears confirmed failure and uses a fresh status plus new ID only after a new explicit attempt', async () => {
    await mount();
    mutate = async (phase, body) => {
      throw new AxiosError('execution failed', 'ERR_BAD_RESPONSE', undefined, undefined, {
        status: 502,
        data: mutationResult(phase, body, {
          state: 'failed',
          failure_code: 'target_stage_corrupt',
        }),
      } as AxiosResponse);
    };
    await act(async () => {
      await flow.prepare();
    });
    const first = flow.intent!;
    expect(flow.stage).toBe('confirmed_failed');
    expect(readCPAUpdateIntent(storageKey())).toBeNull();
    const reads = calls('').length;
    await tick(30_000);
    expect(calls('/prepare')).toHaveLength(1);
    mutate = async (phase, body) => mutationResult(phase, body);
    await act(async () => {
      await flow.prepare();
    });
    expect(flow.stage).toBe('prepared');
    expect(calls('')).toHaveLength(reads + 2);
    expect(flow.intent!.request_id).not.toBe(first.request_id);
    expect(uuid).toHaveBeenCalledTimes(2);
  });

  it('accepts confirmed failure from observation without submitting anything', async () => {
    const intent = updateIntent();
    storeIntent(intent);
    observe = async () => observationResult(intent, 'failed');
    await mount();
    expect(flow.stage).toBe('confirmed_failed');
    expect(flow.failureCode).toBe('target_stage_corrupt');
    expect(readCPAUpdateIntent(storageKey())).toBeNull();
    expect(posts()).toHaveLength(0);
  });
});

describe('CPA recovery context boundaries', () => {
  it.each([
    'runtime_identity_mismatch',
    'stale_runtime_generation',
    'operation_id_conflict',
    'active_artifact_mismatch',
    'target_version_mismatch',
    'recommendation_not_fresh',
  ])('fails closed on %s and never starts a new flow implicitly', async (code) => {
    await mount();
    mutate = async () => {
      throw httpError(code);
    };
    await act(async () => {
      await flow.prepare();
    });
    const intent = flow.intent!;
    expect(flow.stage).toBe('context_changed');
    expect(flow.canCheck).toBe(false);
    observe = async () => {
      throw httpError(code);
    };
    await act(async () => {
      await flow.refresh();
      await flow.prepare();
      await flow.check();
    });
    expect(readCPAUpdateIntent(storageKey())).toEqual(intent);
    expect(posts()).toHaveLength(1);
    expect(uuid).toHaveBeenCalledTimes(1);
  });

  it.each([
    { target_version: '7.3.0' },
    { active_artifact_id: replacementArtifact },
    { stale: true },
    { mode: 'external' as const, state: 'managed_externally' as const },
  ])('blocks prepared activation when fresh context changes: %o', async (patch) => {
    await mount();
    await act(async () => {
      await flow.prepare();
    });
    const original = flow.intent!;
    status = updateStatus(patch);
    await act(async () => {
      await flow.activate(original);
    });
    expect(flow.stage).toBe('context_changed');
    expect(calls('/activate')).toHaveLength(0);
    expect(readCPAUpdateIntent(storageKey())?.expected_active_artifact_id).toBe(originalArtifact);
  });

  it('ignores stale callbacks after switching auth scope and recovers on return', async () => {
    await mount();
    await act(async () => {
      await flow.prepare();
    });
    const original = flow.intent!;
    const originalFlow = flow;
    const oldKey = storageKey();
    mocks.auth.managementKey = 'key-b';
    await mount();
    await act(async () => {
      await originalFlow.activate(original);
    });
    expect(calls('/activate')).toHaveLength(0);
    expect(flow.intent).toBeNull();
    expect(readCPAUpdateIntent(oldKey)).toEqual(original);
    mocks.auth.managementKey = 'key-a';
    observe = async () => observationResult(original, 'succeeded');
    await mount();
    expect(flow.stage).toBe('prepared');
    expect(flow.intent?.request_id).toBe(original.request_id);
  });

  it('abandons stale pre-submit status work after Manager service changes', async () => {
    await mount();
    const pendingStatus = deferred<CPAUpdateStatus>();
    readStatus = () => pendingStatus.promise;
    await act(async () => {
      void flow.prepare();
    });
    mocks.base = 'https://other-manager.test';
    readStatus = async () => status;
    await mount();
    await act(async () => {
      pendingStatus.resolve(status);
    });
    expect(posts()).toHaveLength(0);
    expect(uuid).not.toHaveBeenCalled();
  });

  it('does not adopt altered original guards from another tab', async () => {
    const intent = updateIntent();
    storeIntent(intent);
    await mount();
    storeIntent({ ...intent, expected_active_artifact_id: replacementArtifact });
    await act(async () => {
      window.dispatchEvent(Object.assign(new Event('storage'), { key: storageKey() }));
    });
    expect(flow.stage).toBe('context_changed');
    expect(flow.intent?.expected_active_artifact_id).toBe(originalArtifact);
    expect(posts()).toHaveLength(0);
  });

  it('preserves corruption and blocks commands until explicitly forgotten', async () => {
    storage.setItem(storageKey(), 'invalid record');
    await mount();
    await act(async () => {
      await flow.prepare();
      await flow.check();
    });
    expect(flow.storageError).toBe('corrupt');
    expect(storage.getItem(storageKey())).toBe('invalid record');
    expect(posts()).toHaveLength(0);
    await act(async () => {
      await flow.forget();
    });
    expect(flow.storageError).toBeNull();
    expect(storage.getItem(storageKey())).toBeNull();
  });

  it('forgets only the local scope and never cancels or rolls back a Runtime operation', async () => {
    storeIntent();
    const otherKey = cpaUpdateStorageKey(mocks.base, 'other-key');
    storage.setItem(otherKey, JSON.stringify(updateIntent()));
    await mount();
    await act(async () => {
      await flow.forget();
    });
    expect(readCPAUpdateIntent(storageKey())).toBeNull();
    expect(readCPAUpdateIntent(otherKey)).not.toBeNull();
    const count = queries().length;
    await tick(30_000);
    expect(queries()).toHaveLength(count);
    expect(posts()).toHaveLength(0);
  });

  it('performs zero real CPA calls in demo, even with a retained intent', async () => {
    mocks.demo = true;
    storeIntent();
    await mount();
    await act(async () => {
      await flow.refresh();
      await flow.prepare();
      await flow.check();
    });
    expect(flow.demo).toBe(true);
    expect(request).not.toHaveBeenCalled();
  });
});
