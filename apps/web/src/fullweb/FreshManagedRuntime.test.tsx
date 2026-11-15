import { useState } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import { FreshManagedRuntime } from './FreshManagedRuntime';
import type { FreshOperation } from './bootstrapTransport';
import { PublicApiError, type createPublicApiClient } from './publicApi';
import { shellText as s } from '../i18n/consoleShell';
import { fullWebText as t } from '../i18n/fullweb';

const identity = {
  admissionId: 'a',
  installationId: 'i',
  generation: 'g',
  databasePath: '/data/usage.sqlite',
  operationId: 'o',
};
const initial: FreshOperation = {
  identity,
  revision: 4,
  adminInitialized: true,
  gatewayPending: true,
  routing: {
    operationId: 'o',
    revision: 1,
    target: '/console',
    stage: 'committed',
    committed: true,
  },
};
const status = {
  runtimeIdentity: 'owned-runtime',
  runtimeGeneration: 3,
  activeGatewayArtifact: { artifactId: 'sha256:' + 'a'.repeat(64) },
};
const staged: FreshOperation = {
  ...initial,
  revision: 5,
  strategy: {
    strategy: 'new_managed',
    targetIdentity: status.runtimeIdentity,
    targetGeneration: 3,
    targetArtifactId: status.activeGatewayArtifact.artifactId,
    stage: 'staged',
  },
};
const committed: FreshOperation = {
  ...staged,
  revision: 6,
  strategy: { ...staged.strategy, stage: 'committed' },
};
const managedAccessInfo = {
  mode: 'new_managed',
  managementBasePath: '/console',
  inferenceBasePath: '/infer',
  protocols: [
    {
      id: 'openai',
      label: 'OpenAI',
      basePath: '/infer/v1',
      examplePath: '/infer/v1/chat/completions',
    },
  ],
  ready: true,
  code: '',
};
async function mount(
  get: ReturnType<typeof vi.fn>,
  post: ReturnType<typeof vi.fn>,
  operation = initial,
  guided = false
) {
  const pending = vi.fn(),
    unauthorized = vi.fn();
  const client = { get, post } as unknown as ReturnType<typeof createPublicApiClient>;
  function Harness() {
    const [current, update] = useState(operation);
    return (
      <FreshManagedRuntime
        guided={guided}
        client={client}
        credential="admin-secret"
        operation={current}
        onOperation={update}
        onPending={pending}
        onUnauthorized={unauthorized}
      />
    );
  }
  let renderer!: ReactTestRenderer;
  await act(async () => {
    renderer = create(<Harness />);
  });
  const node = (id: string) => renderer.root.findByProps({ 'data-testid': id });
  const click = async (id: string) => {
    await act(async () => {
      await node(id).props.onClick();
    });
  };
  const observe = async () => {
    await act(async () => {
      node('runtime-observation').props.onChange({ target: { value: JSON.stringify(status) } });
    });
  };
  return { node, click, observe, pending, unauthorized, renderer };
}

describe('Fresh New existing Runtime strategy integration', () => {
  it('guides a user through review and one install action using server observations only', async () => {
    let current = initial;
    const get = vi.fn().mockImplementation((resource: string) => {
      if (resource === 'installation/runtime') return Promise.resolve({ ...status, state: 'ready' });
      if (resource === 'api-keys/access-info') return Promise.resolve(managedAccessInfo);
      return Promise.resolve(current);
    });
    const post = vi.fn().mockImplementation((resource: string) => {
      current =
        resource === 'complete'
          ? { ...committed, revision: 7, productComplete: true }
          : current.strategy
            ? committed
            : staged;
      return Promise.resolve(current);
    });
    const ui = await mount(get, post, initial, true);
    expect(ui.renderer.root.findAllByType('textarea')).toHaveLength(0);
    const next = ui.node('choose-installation');
    await act(async () => next.props.onClick());
    await ui.click('inspect-installation');
    expect(post).not.toHaveBeenCalled();
    expect(ui.node('install-runtime').props.disabled).toBe(true);
    await act(async () =>
      ui.renderer.root.findByType('input').props.onChange({ target: { checked: true } })
    );
    await ui.click('install-runtime');
    expect(post.mock.calls.map(([resource]) => resource)).toEqual([
      'strategy',
      'strategy',
      'complete',
    ]);
    expect(ui.node('install-stage').props.children).toBe(t('installComplete'));
    expect(post.mock.calls[0][1].expectedRuntimeGeneration).toBe(status.runtimeGeneration);
    await act(async () => ui.renderer.unmount());
  });

  it('resumes a confirmed destination without creating a second strategy', async () => {
    const done = { ...committed, revision: 7, productComplete: true };
    const get = vi.fn().mockImplementation((resource: string) => {
      if (resource === 'installation/runtime') return Promise.resolve({ ...status, state: 'ready' });
      if (resource === 'api-keys/access-info') return Promise.resolve(managedAccessInfo);
      return Promise.resolve(done);
    });
    const post = vi.fn().mockResolvedValue(done);
    const ui = await mount(get, post, committed, true);
    await ui.click('install-runtime');
    expect(post.mock.calls.map(([resource]) => resource)).toEqual(['complete']);
    await act(async () => ui.renderer.unmount());
  });

  it('does not complete a saved destination when the live Host generation changed', async () => {
    const get = vi.fn().mockResolvedValue({ ...status, runtimeGeneration: 4, state: 'ready' });
    const post = vi.fn();
    const ui = await mount(get, post, committed, true);
    await ui.click('install-runtime');
    expect(post).not.toHaveBeenCalled();
    expect(JSON.stringify(ui.renderer.toJSON())).toContain(s('failureHelp'));
    await act(async () => ui.renderer.unmount());
  });

  it('requires a host target then stages, starts and completes with authoritative reads', async () => {
    const operationReads = [
      staged,
      committed,
      { ...committed, revision: 7, productComplete: true },
    ];
    const get = vi.fn().mockImplementation((resource: string) => {
      if (resource === 'api-keys/access-info') return Promise.resolve(managedAccessInfo);
      return Promise.resolve(operationReads.shift());
    });
    const post = vi.fn().mockResolvedValue({});
    const ui = await mount(get, post);
    await ui.click('install-runtime');
    expect(post).not.toHaveBeenCalled();
    await ui.observe();
    await ui.click('install-runtime');
    expect(post.mock.calls[0]).toEqual([
      'strategy',
      {
        identity,
        expectedRevision: 4,
        strategy: 'new_managed',
        expectedRuntimeIdentity: status.runtimeIdentity,
        expectedRuntimeGeneration: 3,
        expectedArtifactId: status.activeGatewayArtifact.artifactId,
      },
      { kind: 'admin', value: 'admin-secret' },
    ]);
    await ui.click('install-runtime');
    expect(post.mock.calls[1][1].expectedRevision).toBe(5);
    expect(post.mock.calls[1][1].expectedRuntimeGeneration).toBe(3);
    await ui.click('install-runtime');
    expect(post.mock.calls[2][0]).toBe('complete');
    expect(get.mock.calls.some(([resource]) => resource === 'api-keys/access-info')).toBe(true);
    expect(ui.renderer.root.findAllByProps({ 'data-testid': 'install-runtime' })).toHaveLength(0);
    expect(ui.pending).toHaveBeenLastCalledWith(false);
  });

  it('keeps installation and logout unlocked after a failed read-only refresh', async () => {
    const get = vi.fn().mockRejectedValue(new PublicApiError('network'));
    const post = vi.fn();
    const ui = await mount(get, post, staged);
    await ui.click('refresh-installation');
    expect(get).toHaveBeenCalledTimes(1);
    expect(post).not.toHaveBeenCalled();
    expect(ui.node('install-runtime').props.disabled).toBe(false);
    expect(ui.pending).toHaveBeenLastCalledWith(false);
    expect(ui.renderer.root.findAllByProps({ role: 'status' })).toHaveLength(0);
  });

  it('retains a prior ambiguous-write lock when a read-only refresh also fails', async () => {
    const get = vi.fn().mockRejectedValue(new PublicApiError('network'));
    const post = vi.fn().mockRejectedValue(new PublicApiError('network'));
    const ui = await mount(get, post, staged);
    await ui.click('install-runtime');
    expect(ui.node('install-runtime').props.disabled).toBe(true);
    await ui.click('refresh-installation');
    expect(get).toHaveBeenCalledTimes(2);
    expect(ui.node('install-runtime').props.disabled).toBe(true);
    expect(ui.pending).toHaveBeenLastCalledWith(true);
  });

  it('reconciles a lost start response without another write', async () => {
    const get = vi.fn().mockResolvedValue(committed),
      post = vi.fn().mockRejectedValue(new PublicApiError('network'));
    const ui = await mount(get, post, staged);
    await ui.click('install-runtime');
    expect(post).toHaveBeenCalledTimes(1);
    expect(get).toHaveBeenCalledTimes(1);
    expect(ui.node('install-runtime').props.disabled).toBe(false);
    expect(ui.pending).toHaveBeenLastCalledWith(false);
  });

  it('keeps unknown results locked until a valid read and rejects changed identities', async () => {
    const get = vi
      .fn()
      .mockRejectedValueOnce(new PublicApiError('network'))
      .mockResolvedValueOnce({ ...committed, identity: { ...identity, generation: 'foreign' } })
      .mockResolvedValueOnce(committed);
    const post = vi.fn().mockRejectedValue(new PublicApiError('network'));
    const ui = await mount(get, post, staged);
    await ui.click('install-runtime');
    expect(ui.node('install-runtime').props.disabled).toBe(true);
    await ui.click('install-runtime');
    expect(post).toHaveBeenCalledTimes(1);
    await ui.click('refresh-installation');
    expect(ui.node('install-runtime').props.disabled).toBe(true);
    await ui.click('refresh-installation');
    expect(ui.node('install-runtime').props.disabled).toBe(false);
    expect(post).toHaveBeenCalledTimes(1);
  });

  it('never bypasses a 409 target mismatch, even after a successful read', async () => {
    const get = vi.fn().mockResolvedValue(staged),
      post = vi.fn().mockRejectedValue(new PublicApiError('http', 409));
    const ui = await mount(get, post, staged);
    await ui.click('install-runtime');
    await ui.click('refresh-installation');
    expect(ui.node('install-runtime').props.disabled).toBe(true);
    await ui.click('install-runtime');
    expect(post).toHaveBeenCalledTimes(1);
  });

  it('rejects a server checkpoint that differs from the host-observed target', async () => {
    const get = vi
      .fn()
      .mockResolvedValue({ ...staged, strategy: { ...staged.strategy, targetGeneration: 99 } });
    const ui = await mount(get, vi.fn().mockResolvedValue({}));
    await ui.observe();
    await ui.click('install-runtime');
    expect(ui.node('install-runtime').props.disabled).toBe(true);
    expect(ui.pending).toHaveBeenLastCalledWith(true);
  });

  it.each([
    { action: 'stage', before: initial, after: staged, requiresObservation: true },
    { action: 'start', before: staged, after: committed, requiresObservation: false },
    {
      action: 'complete',
      before: committed,
      after: { ...committed, revision: 7, productComplete: true },
      requiresObservation: false,
    },
  ])(
    'keeps $action UNKNOWN while GET only returns the pre-write state',
    async ({ before, after, requiresObservation }) => {
      let current = before;
      const get = vi.fn().mockImplementation(async (resource: string) =>
        resource === 'api-keys/access-info' ? managedAccessInfo : current
      );
      const post = vi.fn().mockRejectedValue(new PublicApiError('network'));
      const ui = await mount(get, post, before);
      if (requiresObservation) await ui.observe();

      await ui.click('install-runtime');
      expect(post).toHaveBeenCalledTimes(1);
      expect(ui.node('install-runtime').props.disabled).toBe(true);
      expect(ui.pending).toHaveBeenLastCalledWith(true);

      await ui.click('refresh-installation');
      expect(ui.node('install-runtime').props.disabled).toBe(true);
      expect(ui.pending).toHaveBeenLastCalledWith(true);
      expect(post).toHaveBeenCalledTimes(1);

      // Only the original, correctly-targeted transition unlocks this session.
      current = after;
      await ui.click('refresh-installation');
      expect(ui.pending).toHaveBeenLastCalledWith(false);
      if (after.productComplete) {
        expect(ui.renderer.root.findAllByProps({ 'data-testid': 'install-runtime' })).toHaveLength(
          0
        );
      } else {
        expect(ui.node('install-runtime').props.disabled).toBe(false);
      }
      expect(post).toHaveBeenCalledTimes(1);
    }
  );

  it('does not unlock a lost Start on an unrelated revision change', async () => {
    let current: FreshOperation = staged;
    const get = vi.fn().mockImplementation(async () => current);
    const post = vi.fn().mockRejectedValue(new PublicApiError('network'));
    const ui = await mount(get, post, staged);
    await ui.click('install-runtime');
    current = { ...staged, revision: 6 };
    await ui.click('refresh-installation');
    expect(ui.node('install-runtime').props.disabled).toBe(true);
    expect(ui.pending).toHaveBeenLastCalledWith(true);
    expect(post).toHaveBeenCalledTimes(1);
  });

  it('keeps an acknowledged Start locked when the following GET is stale', async () => {
    const get = vi.fn().mockResolvedValue(staged);
    const post = vi.fn().mockResolvedValue(committed);
    const ui = await mount(get, post, staged);
    await ui.click('install-runtime');
    expect(ui.node('install-runtime').props.disabled).toBe(true);
    expect(ui.pending).toHaveBeenLastCalledWith(true);
    await ui.click('refresh-installation');
    expect(post).toHaveBeenCalledTimes(1);
  });

  it('retires admin authority on authentication failure', async () => {
    const ui = await mount(
      vi.fn(),
      vi.fn().mockRejectedValue(new PublicApiError('authentication', 401)),
      staged
    );
    await ui.click('install-runtime');
    expect(ui.unauthorized).toHaveBeenCalledTimes(1);
  });

  it('blocks writes for an incomplete persisted Runtime checkpoint', async () => {
    const post = vi.fn();
    const ui = await mount(vi.fn(), post, {
      ...staged,
      strategy: { ...staged.strategy, targetGeneration: undefined },
    });
    expect(ui.node('install-runtime').props.disabled).toBe(true);
    await ui.click('install-runtime');
    expect(post).not.toHaveBeenCalled();
  });

  it('sends only one Start while the first request is still in flight', async () => {
    let finish!: (value: unknown) => void;
    const post = vi.fn().mockImplementation(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        })
    );
    const ui = await mount(vi.fn().mockResolvedValue(committed), post, staged);
    await ui.click('install-runtime');
    await ui.click('install-runtime');
    expect(post).toHaveBeenCalledTimes(1);
    expect(ui.pending).toHaveBeenLastCalledWith(true);
    await act(async () => {
      finish(committed);
    });
    expect(ui.pending).toHaveBeenLastCalledWith(false);
  });

  it('renders AccessInfoSummary on completion and propagates 401 on access-info to onUnauthorized', async () => {
    const done: FreshOperation = {
      ...committed,
      revision: 7,
      productComplete: true,
      routing: { ...committed.routing, inferenceTarget: '/infer', sharedEntry: false },
    };
    const get = vi.fn().mockImplementation(async (resource: string) => {
      if (resource === 'api-keys/access-info') return managedAccessInfo;
      return done;
    });
    const ui = await mount(get, vi.fn(), done, true);
    expect(ui.node('access-info-summary')).toBeTruthy();
    expect(JSON.stringify(ui.renderer.toJSON())).toContain('/infer/v1');
    await act(async () => ui.renderer.unmount());

    const unauthorizedGet = vi.fn().mockRejectedValue(new PublicApiError('authentication', 401));
    const authUi = await mount(unauthorizedGet, vi.fn(), done, true);
    expect(authUi.unauthorized).toHaveBeenCalledTimes(1);
  });
});
