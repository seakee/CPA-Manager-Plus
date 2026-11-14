// @ts-expect-error Node-only Vitest fixture; production Web excludes Node types.
import { webcrypto } from 'node:crypto';
import { useState } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setFullWebLanguage } from '../i18n/fullweb';
import { bootstrapJourneyText as j } from '../i18n/bootstrapJourney';
import { accessInfoText as a } from '../i18n/freshAccessInfo';
import type { FreshOperation } from './bootstrapTransport';
import {
  FreshInferencePrefixCompletion,
  type InferencePrefixCompletionState,
} from './FreshInferencePrefixCompletion';
import { PublicApiError, type PublicApiClient } from './publicApi';

const identity = {
  admissionId: 'adm-1',
  installationId: 'inst-1',
  generation: 'gen-1',
  databasePath: '/data/manager.sqlite',
  operationId: 'op-1',
};

const baseOperation: FreshOperation = {
  identity,
  revision: 12,
  adminInitialized: true,
  productComplete: true,
  gatewayPending: false,
  routing: {
    operationId: identity.operationId,
    revision: 2,
    target: '/ctl-installed',
    inferenceTarget: '',
    sharedEntry: false,
    inferenceCommitted: false,
    missingInferenceCompletionRequired: true,
    stage: 'committed',
    committed: true,
  },
};

const initialCompletion: InferencePrefixCompletionState = {
  required: true,
  mode: 'new_managed',
  controlBasePath: '/ctl-installed',
  inferenceBasePath: '',
  sharedEntry: false,
  inferenceCommitted: false,
  suggestedInferenceBasePath: '/inf1234567890abcd',
};

const accessInfo = {
  mode: 'new_managed',
  managementBasePath: '/ctl-installed',
  inferenceBasePath: '/inf1234567890abcd',
  protocols: [
    {
      id: 'openai',
      label: 'OpenAI',
      basePath: '/inf1234567890abcd/v1',
      examplePath: '/inf1234567890abcd/v1/chat/completions',
    },
  ],
  ready: true,
  code: '',
};

let renderer: ReactTestRenderer;

function byID(id: string) {
  return renderer.root.findByProps({ 'data-testid': id });
}

function maybeID(id: string) {
  return renderer.root.findAllByProps({ 'data-testid': id });
}

async function mountCompletion(
  getImpl: (resource: string) => Promise<unknown>,
  postImpl: (resource: string, body: unknown) => Promise<unknown>,
  onContinue = vi.fn(),
  onUnauthorized = vi.fn(),
  op: FreshOperation = baseOperation
) {
  const client = {
    get: vi.fn(getImpl),
    post: vi.fn(postImpl),
    put: vi.fn(),
  } as unknown as PublicApiClient;
  const onOpSpy = vi.fn();
  function Harness() {
    const [operation, setOperation] = useState(op);
    return (
      <FreshInferencePrefixCompletion
        client={client}
        credential="admin-secret"
        operation={operation}
        onOperation={(next) => {
          onOpSpy(next);
          setOperation(next);
        }}
        onUnauthorized={onUnauthorized}
        onContinue={onContinue}
      />
    );
  }
  await act(async () => {
    renderer = create(<Harness />);
  });
  return { client, onContinue, onUnauthorized, onOpSpy };
}

describe('FreshInferencePrefixCompletion', () => {
  beforeEach(() => {
    setFullWebLanguage('en');
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    vi.stubGlobal('window', {
      location: new URL('https://manager.example/ctl-installed'),
    });
    vi.stubGlobal('crypto', {
      getRandomValues: <T extends Uint8Array>(bytes: T) => webcrypto.getRandomValues(bytes),
    });
  });

  afterEach(async () => {
    if (renderer) await act(async () => renderer.unmount());
    vi.unstubAllGlobals();
  });

  it('loads suggested prefix, toggles shared mode safely, validates overlaps, submits once, and shows AccessInfoSummary', async () => {
    const updatedOp: FreshOperation = {
      ...baseOperation,
      revision: 13,
      routing: {
        ...baseOperation.routing,
        inferenceTarget: '/inf-custom-entry',
        sharedEntry: false,
        inferenceCommitted: true,
        missingInferenceCompletionRequired: false,
      },
    };
    const committedCompletion: InferencePrefixCompletionState = {
      required: true,
      mode: 'new_managed',
      controlBasePath: '/ctl-installed',
      inferenceBasePath: '/inf-custom-entry',
      sharedEntry: false,
      inferenceCommitted: true,
    };

    const { client, onContinue, onOpSpy } = await mountCompletion(
      async (resource) => {
        if (resource === 'installation/access/completion') return initialCompletion;
        if (resource === 'operation') return updatedOp;
        if (resource === 'api-keys/access-info') {
          return {
            ...accessInfo,
            inferenceBasePath: '/inf-custom-entry',
            protocols: [
              {
                id: 'openai',
                label: 'OpenAI',
                basePath: '/inf-custom-entry/v1',
                examplePath: '/inf-custom-entry/v1/chat/completions',
              },
            ],
          };
        }
        throw new Error(`Unexpected GET ${resource}`);
      },
      async (resource, body) => {
        expect(resource).toBe('installation/access/completion');
        expect(body).toEqual({ inferenceBasePath: '/inf-custom-entry', sharedEntry: false });
        return committedCompletion;
      }
    );

    expect(byID('inference-completion-control').children.join('')).not.toBe('');
    expect(JSON.stringify(renderer.toJSON())).toContain('/ctl-installed');
    expect(byID('inference-completion-input').props.value).toBe('/inf1234567890abcd');
    expect(byID('inference-completion-shared-checkbox').props.checked).toBe(false);

    // Randomizer updates prefix cleanly without touching management path
    await act(async () => {
      byID('inference-completion-randomize').props.onClick();
    });
    const randomized = String(byID('inference-completion-input').props.value);
    expect(randomized).toMatch(/^\/[a-z][a-z0-9]{15}$/);
    expect(randomized).not.toBe('/ctl-installed');

    // Toggle shared mode ON locks input to management prefix, OFF restores distinct random prefix
    await act(async () => {
      byID('inference-completion-shared-checkbox').props.onChange({ target: { checked: true } });
    });
    expect(byID('inference-completion-input').props.value).toBe('/ctl-installed');
    expect(byID('inference-completion-input').props.disabled).toBe(true);
    await act(async () => {
      byID('inference-completion-shared-checkbox').props.onChange({ target: { checked: false } });
    });
    expect(byID('inference-completion-input').props.value).not.toBe('/ctl-installed');
    expect(byID('inference-completion-input').props.disabled).toBe(false);

    // Reject nested prefix locally before network POST
    await act(async () => {
      byID('inference-completion-input').props.onChange({ target: { value: '/ctl-installed/sub' } });
    });
    await act(async () => {
      await byID('inference-completion-submit').props.onClick();
    });
    expect(byID('inference-completion-error').props.children).toBe(j('prefixErrorOverlapForbidden'));
    expect(client.post).not.toHaveBeenCalled();

    // Valid prefix commits cleanly and mounts AccessInfoSummary + Enter Console
    await act(async () => {
      byID('inference-completion-input').props.onChange({ target: { value: '/inf-custom-entry' } });
    });
    await act(async () => {
      await byID('inference-completion-submit').props.onClick();
    });
    expect(client.post).toHaveBeenCalledTimes(1);
    expect(onOpSpy).toHaveBeenCalledWith(updatedOp);
    expect(maybeID('inference-completion-committed-summary')).toHaveLength(1);
    expect(maybeID('access-info-summary')).toHaveLength(1);
    expect(maybeID('inference-completion-submit')).toHaveLength(0);

    await act(async () => {
      byID('inference-completion-enter-console').props.onClick();
    });
    expect(onContinue).toHaveBeenCalledOnce();
  });

  it('reloads completion lock state when server responds 409 inference_prefix_already_committed', async () => {
    let loaded = 0;
    const { onUnauthorized } = await mountCompletion(
      async (resource) => {
        if (resource === 'installation/access/completion') {
          loaded += 1;
          return loaded === 1
            ? initialCompletion
            : { ...initialCompletion, inferenceBasePath: '/locked-inf', inferenceCommitted: true };
        }
        if (resource === 'api-keys/access-info') return accessInfo;
        throw new Error(`Unexpected GET ${resource}`);
      },
      async () => {
        throw new PublicApiError('http', 409, undefined, 'inference_prefix_already_committed');
      }
    );

    await act(async () => {
      await byID('inference-completion-submit').props.onClick();
    });
    expect(onUnauthorized).not.toHaveBeenCalled();
    expect(maybeID('inference-completion-committed-summary')).toHaveLength(1);
  });

  it('calls onUnauthorized when loading or submitting returns authentication failure', async () => {
    const onUnauthorized = vi.fn();
    await mountCompletion(
      async () => {
        throw new PublicApiError('authentication', 401);
      },
      async () => ({}),
      vi.fn(),
      onUnauthorized
    );
    expect(onUnauthorized).toHaveBeenCalledOnce();
  });

  it('accepts Upgrade canonical operation lacking Fresh-only admissionId/databasePath and rejects foreign owner or regressive revision', async () => {
    const upgradeOp: FreshOperation = {
      identity: {
        admissionId: '',
        installationId: 'upg-inst',
        generation: 'upg-gen',
        databasePath: '',
        operationId: 'upgrade:sha256:99',
      },
      revision: 4,
      adminInitialized: true,
      productComplete: true,
      gatewayPending: false,
      routing: {
        operationId: 'upgrade:sha256:99',
        revision: 4,
        target: '/ctl-installed',
        inferenceTarget: '',
        sharedEntry: false,
        inferenceCommitted: false,
        missingInferenceCompletionRequired: true,
        stage: 'committed',
        committed: true,
      },
    };
    let returnForeign = true;
    const { client, onContinue, onOpSpy } = await mountCompletion(
      async (resource) => {
        if (resource === 'installation/access/completion') return initialCompletion;
        if (resource === 'operation') {
          if (returnForeign) {
            returnForeign = false;
            return {
              ...upgradeOp,
              identity: { ...upgradeOp.identity, installationId: 'foreign-inst' },
              revision: 5,
              routing: {
                ...upgradeOp.routing,
                revision: 5,
                inferenceTarget: '/inf-upg-entry',
                inferenceCommitted: true,
                missingInferenceCompletionRequired: false,
              },
            };
          }
          return {
            ...upgradeOp,
            revision: 5,
            routing: {
              ...upgradeOp.routing,
              revision: 5,
              inferenceTarget: '/inf-upg-entry',
              inferenceCommitted: true,
              missingInferenceCompletionRequired: false,
            },
          };
        }
        if (resource === 'api-keys/access-info') {
          return { ...accessInfo, inferenceBasePath: '/inf-upg-entry' };
        }
        throw new Error(`Unexpected GET ${resource}`);
      },
      async () => ({
        ...initialCompletion,
        inferenceBasePath: '/inf-upg-entry',
        inferenceCommitted: true,
      }),
      vi.fn(),
      vi.fn(),
      upgradeOp
    );

    await act(async () => {
      byID('inference-completion-input').props.onChange({ target: { value: '/inf-upg-entry' } });
    });
    await act(async () => {
      await byID('inference-completion-submit').props.onClick();
    });
    // Foreign owner operation was rejected; no fake confirmation happened.
    expect(onOpSpy).not.toHaveBeenCalled();
    expect(maybeID('inference-completion-error')).toHaveLength(1);

    // Continuing retries GET operation only (never repeats POST) and succeeds with matching Upgrade owner.
    const postsBeforeContinue = vi.mocked(client.post).mock.calls.length;
    await act(async () => {
      await byID('inference-completion-enter-console').props.onClick();
    });
    expect(vi.mocked(client.post)).toHaveBeenCalledTimes(postsBeforeContinue);
    expect(onOpSpy).toHaveBeenCalledTimes(1);
    expect(onContinue).toHaveBeenCalledOnce();
  });

  it('recovers lost POST reply via GET completion + GET operation for same committed intent without repeating POST', async () => {
    let committedOnServer = false;
    const committedOp: FreshOperation = {
      ...baseOperation,
      revision: baseOperation.revision + 1,
      routing: {
        ...baseOperation.routing,
        revision: 3,
        inferenceTarget: '/inf-lost-reply',
        sharedEntry: false,
        inferenceCommitted: true,
        missingInferenceCompletionRequired: false,
      },
    };
    const { client, onContinue, onOpSpy } = await mountCompletion(
      async (resource) => {
        if (resource === 'installation/access/completion') {
          return committedOnServer
            ? {
                ...initialCompletion,
                inferenceBasePath: '/inf-lost-reply',
                inferenceCommitted: true,
              }
            : initialCompletion;
        }
        if (resource === 'operation') {
          if (!committedOnServer) throw new Error('Operation not committed');
          return committedOp;
        }
        if (resource === 'api-keys/access-info') {
          return { ...accessInfo, inferenceBasePath: '/inf-lost-reply' };
        }
        throw new Error(`Unexpected GET ${resource}`);
      },
      async () => {
        committedOnServer = true;
        throw new TypeError('Connection reset after commit');
      }
    );

    await act(async () => {
      byID('inference-completion-input').props.onChange({ target: { value: '/inf-lost-reply' } });
    });
    await act(async () => {
      await byID('inference-completion-submit').props.onClick();
    });

    const postsBeforeContinue = vi.mocked(client.post).mock.calls.length;
    expect(onOpSpy).toHaveBeenCalledWith(committedOp);
    expect(maybeID('inference-completion-error')).toHaveLength(0);
    expect(maybeID('inference-completion-committed-summary')).toHaveLength(1);

    await act(async () => {
      await byID('inference-completion-enter-console').props.onClick();
    });
    expect(vi.mocked(client.post)).toHaveBeenCalledTimes(postsBeforeContinue);
    expect(onContinue).toHaveBeenCalledOnce();
  });

  it('retains conflict error visibly on 409 or lost-POST different-intent commit, and Continue only runs GET operation', async () => {
    let serverState: InferencePrefixCompletionState = initialCompletion;
    const committedOtherOp: FreshOperation = {
      ...baseOperation,
      revision: baseOperation.revision + 1,
      routing: {
        ...baseOperation.routing,
        revision: 3,
        inferenceTarget: '/locked-by-peer',
        sharedEntry: false,
        inferenceCommitted: true,
        missingInferenceCompletionRequired: false,
      },
    };
    const { client, onContinue, onOpSpy } = await mountCompletion(
      async (resource) => {
        if (resource === 'installation/access/completion') return serverState;
        if (resource === 'operation') return committedOtherOp;
        if (resource === 'api-keys/access-info') {
          return { ...accessInfo, inferenceBasePath: '/locked-by-peer' };
        }
        throw new Error(`Unexpected GET ${resource}`);
      },
      async () => {
        serverState = {
          ...initialCompletion,
          inferenceBasePath: '/locked-by-peer',
          inferenceCommitted: true,
        };
        throw new PublicApiError('http', 409, undefined, 'inference_prefix_already_committed');
      }
    );

    await act(async () => {
      byID('inference-completion-input').props.onChange({ target: { value: '/inf-my-attempt' } });
    });
    await act(async () => {
      await byID('inference-completion-submit').props.onClick();
    });

    expect(maybeID('inference-completion-committed-summary')).toHaveLength(1);
    expect(byID('inference-completion-error').props.children).toBe(
      j('prefixErrorAlreadyCommitted')
    );

    const postsBeforeContinue = vi.mocked(client.post).mock.calls.length;
    await act(async () => {
      await byID('inference-completion-enter-console').props.onClick();
    });
    expect(vi.mocked(client.post)).toHaveBeenCalledTimes(postsBeforeContinue);
    expect(onOpSpy).toHaveBeenCalledWith(committedOtherOp);
    expect(onContinue).toHaveBeenCalledOnce();
  });

  it('recovers when POST succeeds but GET operation fails initially, retrying GET operation on Continue without repeating POST', async () => {
    let failOpOnce = true;
    const committedOp: FreshOperation = {
      ...baseOperation,
      revision: baseOperation.revision + 1,
      routing: {
        ...baseOperation.routing,
        revision: 3,
        inferenceTarget: '/inf-get-retry',
        sharedEntry: false,
        inferenceCommitted: true,
        missingInferenceCompletionRequired: false,
      },
    };
    const { client, onContinue, onOpSpy } = await mountCompletion(
      async (resource) => {
        if (resource === 'installation/access/completion') return initialCompletion;
        if (resource === 'operation') {
          if (failOpOnce) {
            failOpOnce = false;
            throw new TypeError('Transient GET failure');
          }
          return committedOp;
        }
        if (resource === 'api-keys/access-info') {
          return { ...accessInfo, inferenceBasePath: '/inf-get-retry' };
        }
        throw new Error(`Unexpected GET ${resource}`);
      },
      async () => ({
        ...initialCompletion,
        inferenceBasePath: '/inf-get-retry',
        inferenceCommitted: true,
      })
    );

    await act(async () => {
      byID('inference-completion-input').props.onChange({ target: { value: '/inf-get-retry' } });
    });
    await act(async () => {
      await byID('inference-completion-submit').props.onClick();
    });
    expect(maybeID('inference-completion-error')).toHaveLength(1);
    expect(maybeID('inference-completion-enter-console')).toHaveLength(1);
    expect(onContinue).not.toHaveBeenCalled();

    const postsBeforeContinue = vi.mocked(client.post).mock.calls.length;
    await act(async () => {
      await byID('inference-completion-enter-console').props.onClick();
    });
    expect(vi.mocked(client.post)).toHaveBeenCalledTimes(postsBeforeContinue);
    expect(onOpSpy).toHaveBeenCalledWith(committedOp);
    expect(onContinue).toHaveBeenCalledOnce();
  });

  it('retains conflict visibly when POST response is lost and GET completion shows a different committed intent', async () => {
    let serverState: InferencePrefixCompletionState = initialCompletion;
    const committedOtherOp: FreshOperation = {
      ...baseOperation,
      revision: baseOperation.revision + 1,
      routing: {
        ...baseOperation.routing,
        revision: 3,
        inferenceTarget: '/peer-committed',
        sharedEntry: false,
        inferenceCommitted: true,
        missingInferenceCompletionRequired: false,
      },
    };
    const { client, onContinue, onOpSpy } = await mountCompletion(
      async (resource) => {
        if (resource === 'installation/access/completion') return serverState;
        if (resource === 'operation') return committedOtherOp;
        if (resource === 'api-keys/access-info') {
          return { ...accessInfo, inferenceBasePath: '/peer-committed' };
        }
        throw new Error(`Unexpected GET ${resource}`);
      },
      async () => {
        serverState = {
          ...initialCompletion,
          inferenceBasePath: '/peer-committed',
          inferenceCommitted: true,
        };
        throw new PublicApiError('http', 502);
      }
    );

    await act(async () => {
      byID('inference-completion-input').props.onChange({ target: { value: '/inf-mine' } });
    });
    await act(async () => {
      await byID('inference-completion-submit').props.onClick();
    });

    expect(maybeID('inference-completion-committed-summary')).toHaveLength(1);
    expect(byID('inference-completion-error').props.children).toBe(
      j('prefixErrorAlreadyCommitted')
    );
    const postsBeforeContinue = vi.mocked(client.post).mock.calls.length;
    await act(async () => {
      await byID('inference-completion-enter-console').props.onClick();
    });
    expect(vi.mocked(client.post)).toHaveBeenCalledTimes(postsBeforeContinue);
    expect(onOpSpy).toHaveBeenCalledWith(committedOtherOp);
    expect(onContinue).toHaveBeenCalledOnce();
  });

  it('reconciles staged pending nonempty inferenceBasePath and sharedEntry after initial hydration on 409 distinct intent conflict and keeps readonly recovery', async () => {
    let serverState: InferencePrefixCompletionState = initialCompletion;
    const committedSharedOp: FreshOperation = {
      ...baseOperation,
      revision: baseOperation.revision + 1,
      routing: {
        ...baseOperation.routing,
        revision: 3,
        inferenceTarget: '/ctl-installed',
        sharedEntry: true,
        inferenceCommitted: true,
        missingInferenceCompletionRequired: false,
      },
    };
    const { client, onContinue, onOpSpy } = await mountCompletion(
      async (resource) => {
        if (resource === 'installation/access/completion') return serverState;
        if (resource === 'operation') return committedSharedOp;
        if (resource === 'api-keys/access-info') {
          return { ...accessInfo, inferenceBasePath: '/ctl-installed', sharedEntry: true };
        }
        throw new Error(`Unexpected GET ${resource}`);
      },
      async (_resource, body) => {
        const payload = body as { inferenceBasePath: string; sharedEntry: boolean };
        if (payload.inferenceBasePath !== '/ctl-installed' || payload.sharedEntry !== true) {
          serverState = {
            ...initialCompletion,
            inferenceBasePath: '/ctl-installed',
            sharedEntry: true,
            inferenceCommitted: false,
          };
          throw new PublicApiError('http', 409, undefined, 'inference_prefix_already_committed');
        }
        serverState = {
          ...initialCompletion,
          required: false,
          inferenceBasePath: '/ctl-installed',
          sharedEntry: true,
          inferenceCommitted: true,
        };
        return serverState;
      }
    );

    await act(async () => {
      byID('inference-completion-input').props.onChange({ target: { value: '/inf-distinct' } });
    });
    await act(async () => {
      await byID('inference-completion-submit').props.onClick();
    });

    expect(byID('inference-completion-error').props.children).toBe(
      j('prefixErrorAlreadyCommitted')
    );
    expect(byID('inference-completion-shared-checkbox').props.checked).toBe(true);
    expect(byID('inference-completion-shared-checkbox').props.disabled).toBe(true);
    expect(byID('inference-completion-input').props.value).toBe('/ctl-installed');
    expect(byID('inference-completion-input').props.disabled).toBe(true);
    expect(byID('inference-completion-randomize').props.disabled).toBe(true);
    expect(byID('inference-completion-submit').props.disabled).toBe(false);

    await act(async () => {
      await byID('inference-completion-submit').props.onClick();
    });
    const sent = vi.mocked(client.post).mock.calls;
    expect(sent[sent.length - 1]?.[1]).toEqual({
      inferenceBasePath: '/ctl-installed',
      sharedEntry: true,
    });
    expect(maybeID('inference-completion-committed-summary')).toHaveLength(1);
    expect(onOpSpy).toHaveBeenCalledWith(committedSharedOp);
    await act(async () => {
      await byID('inference-completion-enter-console').props.onClick();
    });
    expect(onContinue).toHaveBeenCalledOnce();
  });

  it('recovers from pre-commit routing_verification_failed by ingesting staged path+sharedEntry and operation while preserving error', async () => {
    let serverState: InferencePrefixCompletionState = initialCompletion;
    const stagedOp: FreshOperation = {
      ...baseOperation,
      revision: baseOperation.revision + 1,
      routing: {
        ...baseOperation.routing,
        revision: 3,
        inferenceTarget: '/staged-verif',
        sharedEntry: false,
        inferenceCommitted: false,
        missingInferenceCompletionRequired: true,
      },
    };
    const committedOp: FreshOperation = {
      ...stagedOp,
      revision: stagedOp.revision + 1,
      routing: {
        ...stagedOp.routing,
        revision: 3,
        inferenceTarget: '/staged-verif',
        sharedEntry: false,
        inferenceCommitted: true,
        missingInferenceCompletionRequired: false,
      },
    };
    let currentOp: FreshOperation = baseOperation;
    let failOnce = true;
    const { client, onContinue, onOpSpy } = await mountCompletion(
      async (resource) => {
        if (resource === 'installation/access/completion') return serverState;
        if (resource === 'operation') return currentOp;
        if (resource === 'api-keys/access-info') {
          return { ...accessInfo, inferenceBasePath: '/staged-verif' };
        }
        throw new Error(`Unexpected GET ${resource}`);
      },
      async () => {
        if (failOnce) {
          failOnce = false;
          serverState = {
            ...initialCompletion,
            inferenceBasePath: '/staged-verif',
            sharedEntry: false,
            inferenceCommitted: false,
          };
          currentOp = stagedOp;
          throw new PublicApiError('http', 503, undefined, 'routing_verification_failed');
        }
        serverState = {
          ...initialCompletion,
          required: false,
          inferenceBasePath: '/staged-verif',
          sharedEntry: false,
          inferenceCommitted: true,
        };
        currentOp = committedOp;
        return serverState;
      }
    );

    await act(async () => {
      byID('inference-completion-input').props.onChange({ target: { value: '/staged-verif' } });
    });
    await act(async () => {
      await byID('inference-completion-submit').props.onClick();
    });

    expect(byID('inference-completion-error').props.children).toBe(
      j('prefixErrorVerificationFailed')
    );
    expect(byID('inference-completion-input').props.value).toBe('/staged-verif');
    expect(byID('inference-completion-input').props.disabled).toBe(true);
    expect(byID('inference-completion-shared-checkbox').props.checked).toBe(false);
    expect(byID('inference-completion-shared-checkbox').props.disabled).toBe(true);
    expect(onOpSpy).toHaveBeenCalledWith(stagedOp);
    expect(onContinue).not.toHaveBeenCalled();

    await act(async () => {
      await byID('inference-completion-submit').props.onClick();
    });
    expect(vi.mocked(client.post)).toHaveBeenCalledTimes(2);
    expect(onOpSpy).toHaveBeenLastCalledWith(committedOp);
    expect(maybeID('inference-completion-committed-summary')).toHaveLength(1);
    await act(async () => {
      await byID('inference-completion-enter-console').props.onClick();
    });
    expect(onContinue).toHaveBeenCalledOnce();
  });

  it('recovers from post-commit final Apply routing_verification_failed via status+operation without repeating POST or accepting fake Fresh owner', async () => {
    let serverState: InferencePrefixCompletionState = initialCompletion;
    const fakeFreshOwnerOp: FreshOperation = {
      ...baseOperation,
      identity: {
        ...baseOperation.identity,
        admissionId: 'adm-fake-fresh',
        databasePath: '/tmp/fake-fresh.db',
      },
      revision: baseOperation.revision + 1,
      routing: {
        ...baseOperation.routing,
        revision: 3,
        inferenceTarget: '/ctl-installed',
        sharedEntry: true,
        inferenceCommitted: true,
        missingInferenceCompletionRequired: false,
      },
    };
    const realCommittedOp: FreshOperation = {
      ...baseOperation,
      revision: baseOperation.revision + 1,
      routing: {
        ...baseOperation.routing,
        revision: 3,
        inferenceTarget: '/ctl-installed',
        sharedEntry: true,
        inferenceCommitted: true,
        missingInferenceCompletionRequired: false,
      },
    };
    let opCalls = 0;
    const { client, onContinue, onOpSpy } = await mountCompletion(
      async (resource) => {
        if (resource === 'installation/access/completion') return serverState;
        if (resource === 'operation') {
          opCalls += 1;
          return opCalls === 1 ? fakeFreshOwnerOp : realCommittedOp;
        }
        if (resource === 'api-keys/access-info') {
          return { ...accessInfo, inferenceBasePath: '/ctl-installed', sharedEntry: true };
        }
        throw new Error(`Unexpected GET ${resource}`);
      },
      async () => {
        serverState = {
          ...initialCompletion,
          required: false,
          inferenceBasePath: '/ctl-installed',
          sharedEntry: true,
          inferenceCommitted: true,
        };
        throw new PublicApiError('http', 503, undefined, 'routing_verification_failed');
      }
    );

    await act(async () => {
      byID('inference-completion-shared-checkbox').props.onChange({ target: { checked: true } });
    });
    await act(async () => {
      await byID('inference-completion-submit').props.onClick();
    });

    // Fake Fresh owner rejected; committed summary rendered with canonical shared path; no second POST
    expect(onOpSpy).not.toHaveBeenCalled();
    expect(onContinue).not.toHaveBeenCalled();
    expect(maybeID('inference-completion-committed-summary')).toHaveLength(1);
    expect(maybeID('inference-completion-error')).toHaveLength(1);

    const postsAfterFailure = vi.mocked(client.post).mock.calls.length;
    await act(async () => {
      await byID('inference-completion-enter-console').props.onClick();
    });
    expect(vi.mocked(client.post)).toHaveBeenCalledTimes(postsAfterFailure);
    expect(onOpSpy).toHaveBeenCalledWith(realCommittedOp);
    expect(onContinue).toHaveBeenCalledOnce();

    // When GET operation succeeds immediately after post-commit final Apply failure, verification error stays preserved
    let committedState: InferencePrefixCompletionState = initialCompletion;
    const { client: client2, onContinue: onContinue2, onOpSpy: onOpSpy2 } = await mountCompletion(
      async (resource) => {
        if (resource === 'installation/access/completion') return committedState;
        if (resource === 'operation') return realCommittedOp;
        if (resource === 'api-keys/access-info') {
          return { ...accessInfo, inferenceBasePath: '/ctl-installed', sharedEntry: true };
        }
        throw new Error(`Unexpected GET ${resource}`);
      },
      async () => {
        committedState = {
          ...initialCompletion,
          required: false,
          inferenceBasePath: '/ctl-installed',
          sharedEntry: true,
          inferenceCommitted: true,
        };
        throw new PublicApiError('http', 503, undefined, 'routing_verification_failed');
      }
    );
    await act(async () => {
      byID('inference-completion-shared-checkbox').props.onChange({ target: { checked: true } });
    });
    await act(async () => {
      await byID('inference-completion-submit').props.onClick();
    });
    expect(onOpSpy2).toHaveBeenCalledWith(realCommittedOp);
    expect(maybeID('inference-completion-committed-summary')).toHaveLength(1);
    expect(byID('inference-completion-error').props.children).toBe(
      j('prefixErrorVerificationFailed')
    );
    const postsBeforeContinue2 = vi.mocked(client2.post).mock.calls.length;
    await act(async () => {
      await byID('inference-completion-enter-console').props.onClick();
    });
    expect(vi.mocked(client2.post)).toHaveBeenCalledTimes(postsBeforeContinue2);
    expect(onContinue2).toHaveBeenCalledOnce();
  });

  it('rejects regressive operation or routing revisions during committed completion confirmation', async () => {
    const regressiveOp: FreshOperation = {
      ...baseOperation,
      revision: baseOperation.revision - 1,
      routing: {
        ...baseOperation.routing,
        revision: (baseOperation.routing.revision ?? 2) - 1,
        inferenceTarget: '/inf-monotonic',
        sharedEntry: false,
        inferenceCommitted: true,
        missingInferenceCompletionRequired: false,
      },
    };
    const monotonicOp: FreshOperation = {
      ...baseOperation,
      revision: baseOperation.revision + 1,
      routing: {
        ...baseOperation.routing,
        revision: (baseOperation.routing.revision ?? 2) + 1,
        inferenceTarget: '/inf-monotonic',
        sharedEntry: false,
        inferenceCommitted: true,
        missingInferenceCompletionRequired: false,
      },
    };
    let opCalls = 0;
    const { client, onContinue, onOpSpy } = await mountCompletion(
      async (resource) => {
        if (resource === 'installation/access/completion') return initialCompletion;
        if (resource === 'operation') {
          opCalls += 1;
          return opCalls === 1 ? regressiveOp : monotonicOp;
        }
        if (resource === 'api-keys/access-info') {
          return { ...accessInfo, inferenceBasePath: '/inf-monotonic' };
        }
        throw new Error(`Unexpected GET ${resource}`);
      },
      async () => ({
        ...initialCompletion,
        required: false,
        inferenceBasePath: '/inf-monotonic',
        sharedEntry: false,
        inferenceCommitted: true,
      })
    );

    await act(async () => {
      byID('inference-completion-input').props.onChange({ target: { value: '/inf-monotonic' } });
    });
    await act(async () => {
      await byID('inference-completion-submit').props.onClick();
    });

    expect(onOpSpy).not.toHaveBeenCalled();
    expect(onContinue).not.toHaveBeenCalled();
    expect(byID('inference-completion-error').props.children).toBe(
      j('inferenceCompletionLoadError')
    );

    const postsBeforeContinue = vi.mocked(client.post).mock.calls.length;
    await act(async () => {
      await byID('inference-completion-enter-console').props.onClick();
    });
    expect(vi.mocked(client.post)).toHaveBeenCalledTimes(postsBeforeContinue);
    expect(onOpSpy).toHaveBeenCalledWith(monotonicOp);
    expect(onContinue).toHaveBeenCalledOnce();
  });

  it('keeps HTTP 503 routing_unavailable warning when final Apply fails after canonical commit, while distinguishing lost network reply', async () => {
    let serverState: InferencePrefixCompletionState = initialCompletion;
    const committedOp: FreshOperation = {
      ...baseOperation,
      revision: baseOperation.revision + 1,
      routing: {
        ...baseOperation.routing,
        revision: 3,
        inferenceTarget: '/inf-final-503',
        sharedEntry: false,
        inferenceCommitted: true,
        missingInferenceCompletionRequired: false,
      },
    };
    const { client, onContinue, onOpSpy } = await mountCompletion(
      async (resource) => {
        if (resource === 'installation/access/completion') return serverState;
        if (resource === 'operation') return committedOp;
        if (resource === 'api-keys/access-info') {
          return { ...accessInfo, inferenceBasePath: '/inf-final-503', ready: false, code: 'routing_unavailable' };
        }
        throw new Error(`Unexpected GET ${resource}`);
      },
      async () => {
        serverState = {
          ...initialCompletion,
          required: false,
          inferenceBasePath: '/inf-final-503',
          sharedEntry: false,
          inferenceCommitted: true,
        };
        throw new PublicApiError('http', 503, undefined, 'routing_unavailable');
      }
    );

    await act(async () => {
      byID('inference-completion-input').props.onChange({ target: { value: '/inf-final-503' } });
    });
    await act(async () => {
      await byID('inference-completion-submit').props.onClick();
    });

    expect(onOpSpy).toHaveBeenCalledWith(committedOp);
    expect(maybeID('inference-completion-committed-summary')).toHaveLength(1);
    expect(byID('inference-completion-error').props.children).toBe(a('notReadyRoutingDesc'));

    const postsBeforeContinue = vi.mocked(client.post).mock.calls.length;
    await act(async () => {
      await byID('inference-completion-enter-console').props.onClick();
    });
    expect(vi.mocked(client.post)).toHaveBeenCalledTimes(postsBeforeContinue);
    expect(onContinue).toHaveBeenCalledOnce();

    await act(async () => renderer.unmount());

    // Uncoded HTTP 503 after commit must also preserve warning alongside committed summary.
    let uncodedState: InferencePrefixCompletionState = initialCompletion;
    await mountCompletion(
      async (resource) => {
        if (resource === 'installation/access/completion') return uncodedState;
        if (resource === 'operation') return committedOp;
        if (resource === 'api-keys/access-info') {
          return { ...accessInfo, inferenceBasePath: '/inf-final-503' };
        }
        throw new Error(`Unexpected GET ${resource}`);
      },
      async () => {
        uncodedState = {
          ...initialCompletion,
          required: false,
          inferenceBasePath: '/inf-final-503',
          sharedEntry: false,
          inferenceCommitted: true,
        };
        throw new PublicApiError('http', 503);
      }
    );

    await act(async () => {
      byID('inference-completion-input').props.onChange({ target: { value: '/inf-final-503' } });
    });
    await act(async () => {
      await byID('inference-completion-submit').props.onClick();
    });
    expect(maybeID('inference-completion-committed-summary')).toHaveLength(1);
    expect(byID('inference-completion-error').props.children).toBe(j('setupError'));

    await act(async () => renderer.unmount());

    // Transport/network loss (PublicApiError('network')) with same committed intent recovers cleanly.
    let networkLostState: InferencePrefixCompletionState = initialCompletion;
    const { onOpSpy: networkOpSpy } = await mountCompletion(
      async (resource) => {
        if (resource === 'installation/access/completion') return networkLostState;
        if (resource === 'operation') return committedOp;
        if (resource === 'api-keys/access-info') {
          return { ...accessInfo, inferenceBasePath: '/inf-final-503' };
        }
        throw new Error(`Unexpected GET ${resource}`);
      },
      async () => {
        networkLostState = {
          ...initialCompletion,
          required: false,
          inferenceBasePath: '/inf-final-503',
          sharedEntry: false,
          inferenceCommitted: true,
        };
        throw new PublicApiError('network');
      }
    );

    await act(async () => {
      byID('inference-completion-input').props.onChange({ target: { value: '/inf-final-503' } });
    });
    await act(async () => {
      await byID('inference-completion-submit').props.onClick();
    });
    expect(networkOpSpy).toHaveBeenCalledWith(committedOp);
    expect(maybeID('inference-completion-committed-summary')).toHaveLength(1);
    expect(maybeID('inference-completion-error')).toHaveLength(0);
  });

  it('reconciles parent operation and locks staged fields readonly on pre-commit 409 routing_conflict and 503 routing_unavailable', async () => {
    let serverState: InferencePrefixCompletionState = initialCompletion;
    const stagedOp: FreshOperation = {
      ...baseOperation,
      revision: baseOperation.revision + 1,
      routing: {
        ...baseOperation.routing,
        revision: 3,
        inferenceTarget: '/staged-conflict',
        sharedEntry: false,
        inferenceCommitted: false,
        missingInferenceCompletionRequired: true,
      },
    };
    const committedOp: FreshOperation = {
      ...stagedOp,
      revision: stagedOp.revision + 1,
      routing: {
        ...stagedOp.routing,
        revision: 3,
        inferenceTarget: '/staged-conflict',
        sharedEntry: false,
        inferenceCommitted: true,
        missingInferenceCompletionRequired: false,
      },
    };
    let currentOp: FreshOperation = baseOperation;
    let failConflictOnce = true;
    const { client, onContinue, onOpSpy } = await mountCompletion(
      async (resource) => {
        if (resource === 'installation/access/completion') return serverState;
        if (resource === 'operation') return currentOp;
        if (resource === 'api-keys/access-info') {
          return { ...accessInfo, inferenceBasePath: '/staged-conflict' };
        }
        throw new Error(`Unexpected GET ${resource}`);
      },
      async () => {
        if (failConflictOnce) {
          failConflictOnce = false;
          serverState = {
            ...initialCompletion,
            inferenceBasePath: '/staged-conflict',
            sharedEntry: false,
            inferenceCommitted: false,
          };
          currentOp = stagedOp;
          throw new PublicApiError('http', 409, undefined, 'routing_conflict');
        }
        serverState = {
          ...initialCompletion,
          required: false,
          inferenceBasePath: '/staged-conflict',
          sharedEntry: false,
          inferenceCommitted: true,
        };
        currentOp = committedOp;
        return serverState;
      }
    );

    await act(async () => {
      byID('inference-completion-input').props.onChange({ target: { value: '/staged-conflict' } });
    });
    await act(async () => {
      await byID('inference-completion-submit').props.onClick();
    });

    expect(byID('inference-completion-error').props.children).toBe(j('prefixErrorRoutingConflict'));
    expect(byID('inference-completion-input').props.value).toBe('/staged-conflict');
    expect(byID('inference-completion-input').props.disabled).toBe(true);
    expect(byID('inference-completion-shared-checkbox').props.disabled).toBe(true);
    expect(byID('inference-completion-randomize').props.disabled).toBe(true);
    expect(onOpSpy).toHaveBeenCalledWith(stagedOp);
    expect(onContinue).not.toHaveBeenCalled();

    await act(async () => {
      await byID('inference-completion-submit').props.onClick();
    });
    expect(vi.mocked(client.post)).toHaveBeenCalledTimes(2);
    expect(onOpSpy).toHaveBeenLastCalledWith(committedOp);
    expect(maybeID('inference-completion-committed-summary')).toHaveLength(1);
    await act(async () => {
      await byID('inference-completion-enter-console').props.onClick();
    });
    expect(onContinue).toHaveBeenCalledOnce();

    await act(async () => renderer.unmount());

    let stagedUnavailState: InferencePrefixCompletionState = initialCompletion;
    const stagedUnavailOp: FreshOperation = {
      ...baseOperation,
      revision: baseOperation.revision + 1,
      routing: {
        ...baseOperation.routing,
        revision: 3,
        inferenceTarget: '/staged-unavail',
        sharedEntry: false,
        inferenceCommitted: false,
        missingInferenceCompletionRequired: true,
      },
    };
    const { onOpSpy: unavailOpSpy } = await mountCompletion(
      async (resource) => {
        if (resource === 'installation/access/completion') return stagedUnavailState;
        if (resource === 'operation') return stagedUnavailOp;
        throw new Error(`Unexpected GET ${resource}`);
      },
      async () => {
        stagedUnavailState = {
          ...initialCompletion,
          inferenceBasePath: '/staged-unavail',
          sharedEntry: false,
          inferenceCommitted: false,
        };
        throw new PublicApiError('http', 503, undefined, 'routing_unavailable');
      }
    );

    await act(async () => {
      byID('inference-completion-input').props.onChange({ target: { value: '/staged-unavail' } });
    });
    await act(async () => {
      await byID('inference-completion-submit').props.onClick();
    });
    expect(byID('inference-completion-error').props.children).toBe(a('notReadyRoutingDesc'));
    expect(byID('inference-completion-input').props.value).toBe('/staged-unavail');
    expect(byID('inference-completion-input').props.disabled).toBe(true);
    expect(unavailOpSpy).toHaveBeenCalledWith(stagedUnavailOp);
  });

  it('surfaces typed warning on HTTP 400 external_inference_prefix_forbidden', async () => {
    await mountCompletion(
      async (resource) => {
        if (resource === 'installation/access/completion') return initialCompletion;
        throw new Error(`Unexpected GET ${resource}`);
      },
      async () => {
        throw new PublicApiError('http', 400, undefined, 'external_inference_prefix_forbidden');
      }
    );

    await act(async () => {
      await byID('inference-completion-submit').props.onClick();
    });
    expect(byID('inference-completion-error').props.children).toBe(
      j('prefixErrorExternalInferenceForbidden')
    );
  });
});
