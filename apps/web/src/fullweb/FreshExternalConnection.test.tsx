// @ts-expect-error Node-only Vitest fixture; production Web excludes Node types.
import { webcrypto } from 'node:crypto';
import { useState } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { FreshExternalConnection } from './FreshExternalConnection';
import type { FreshOperation } from './bootstrapTransport';
import { PublicApiError, createPublicApiClient, type FullWebCredential } from './publicApi';
import { fullWebText as t, setFullWebLanguage } from '../i18n/fullweb';
import { externalWorkspaceText as x } from '../i18n/freshExternal';

const initial: FreshOperation = {
  identity: {
    admissionId: 'a',
    installationId: 'i',
    generation: 'g',
    databasePath: '/data/usage.sqlite',
    operationId: 'o',
  },
  revision: 4,
  adminInitialized: true,
  productComplete: false,
  gatewayPending: true,
  routing: {
    operationId: 'o',
    revision: 1,
    target: '/console',
    stage: 'committed',
    committed: true,
  },
};
const committed: FreshOperation = {
  ...initial,
  revision: 5,
  strategy: {
    strategy: 'connect_external',
    targetIdentity: 'external:sha256:' + 'a'.repeat(64),
    targetGeneration: 1,
    stage: 'committed',
  },
};
const completed = { ...committed, revision: 6, productComplete: true };
const renderers: ReactTestRenderer[] = [];

async function mount(
  get = vi.fn().mockResolvedValue(committed),
  post = vi.fn().mockResolvedValue(committed),
  start = initial
) {
  const pending = vi.fn(),
    unauthorized = vi.fn(),
    enter = vi.fn(),
    back = vi.fn();
  const client = { get, post } as unknown as ReturnType<typeof createPublicApiClient>;
  function Harness() {
    const [current, update] = useState(start);
    return (
      <FreshExternalConnection
        client={client}
        credential="private-admin"
        operation={current}
        onOperation={update}
        onPending={pending}
        onUnauthorized={unauthorized}
        onContinue={enter}
        onBack={back}
      />
    );
  }
  let renderer!: ReactTestRenderer;
  await act(async () => {
    renderer = create(<Harness />);
  });
  renderers.push(renderer);
  const node = (id: string) => renderer.root.findByProps({ 'data-testid': id });
  const has = (id: string) => renderer.root.findAllByProps({ 'data-testid': id }).length > 0;
  const click = async (id: string) => act(async () => node(id).props.onClick());
  const field = async (id: string, value: string) =>
    act(async () => node(id).props.onChange({ target: { value } }));
  const prepare = async () => {
    await field('external-base-url', ' http://cpa.private:8317/ ');
    await field('external-management-key', 'source-secret');
    await click('external-review');
  };
  return {
    renderer,
    node,
    has,
    click,
    field,
    prepare,
    pending,
    unauthorized,
    enter,
    back,
    get,
    post,
  };
}

describe('Fresh External initialization', () => {
  beforeEach(() => {
    vi.stubGlobal('crypto', {
      getRandomValues: <T extends Uint8Array>(bytes: T) => webcrypto.getRandomValues(bytes),
    });
    setFullWebLanguage('en');
  });
  afterEach(async () => {
    for (const renderer of renderers.splice(0)) await act(async () => renderer.unmount());
    vi.unstubAllGlobals();
  });

  it('reviews without network or plaintext key, saves once, completes and enters the existing Console', async () => {
    const ui = await mount();
    await ui.prepare();
    expect(ui.post).not.toHaveBeenCalled();
    expect(JSON.stringify(ui.renderer.toJSON())).not.toContain('source-secret');
    await ui.click('external-connect');
    expect(ui.post.mock.calls[0]).toEqual([
      'strategy',
      {
        identity: initial.identity,
        expectedRevision: 4,
        strategy: 'connect_external',
        baseUrl: 'http://cpa.private:8317',
        managementKey: 'source-secret',
        attemptId: expect.stringMatching(/^[a-f0-9]{32}$/),
      },
      { kind: 'admin', value: 'private-admin' },
    ]);
    expect(ui.has('external-complete')).toBe(true);
    expect(ui.has('external-base-url')).toBe(false);
    expect(ui.pending).toHaveBeenLastCalledWith(false);
    ui.post.mockResolvedValue(completed);
    ui.get.mockResolvedValue(completed);
    await ui.click('external-complete');
    expect(ui.post).toHaveBeenCalledTimes(2);
    expect(ui.post.mock.calls[1][0]).toBe('complete');
    await ui.click('external-console');
    expect(ui.enter).toHaveBeenCalledOnce();
  });

  it('recovers a lost save response from authoritative GET without resending', async () => {
    const ui = await mount(
      vi.fn().mockResolvedValue(committed),
      vi.fn().mockRejectedValue(new PublicApiError('network'))
    );
    await ui.prepare();
    await ui.click('external-connect');
    expect(ui.has('external-complete')).toBe(true);
    await ui.click('external-refresh');
    expect(ui.post).toHaveBeenCalledOnce();
    expect(ui.pending).toHaveBeenLastCalledWith(false);
  });

  it('restores saved setup after reload without asking for the key', async () => {
    const ui = await mount(undefined, undefined, committed);
    expect(ui.has('external-complete')).toBe(true);
    expect(ui.has('external-management-key')).toBe(false);
    expect(ui.post).not.toHaveBeenCalled();
  });

  it('recovers a lost completion response', async () => {
    const ui = await mount(
      vi.fn().mockResolvedValue(completed),
      vi.fn().mockRejectedValue(new PublicApiError('network')),
      committed
    );
    await ui.click('external-complete');
    expect(ui.has('external-console')).toBe(true);
    expect(ui.post).toHaveBeenCalledOnce();
  });

  it('lets an authenticated user correct a rejected source key', async () => {
    const post = vi
      .fn()
      .mockRejectedValue(
        new PublicApiError('http', 409, undefined, 'management_authentication_rejected')
      );
    const ui = await mount(vi.fn().mockResolvedValue(initial), post);
    await ui.prepare();
    await ui.click('external-connect');
    expect(ui.node('external-management-key').props.value).toBe('');
    expect(JSON.stringify(ui.renderer.toJSON())).toContain('CPA rejected');
    expect(ui.pending).toHaveBeenLastCalledWith(false);
    ui.get.mockResolvedValue(committed);
    ui.post.mockResolvedValue(committed);
    await ui.prepare();
    await ui.click('external-connect');
    expect(ui.post).toHaveBeenCalledTimes(2);
    expect(ui.has('external-complete')).toBe(true);
  });

  it('can retry an old validated-only checkpoint', async () => {
    const ui = await mount(undefined, undefined, {
      ...committed,
      strategy: { ...committed.strategy, stage: 'validated' },
    });
    expect(ui.has('external-management-key')).toBe(true);
    await ui.prepare();
    await ui.click('external-connect');
    expect(ui.has('external-complete')).toBe(true);
  });

  it('requires a successful read after an unknown result before offering another save', async () => {
    const ui = await mount(
      vi.fn().mockRejectedValue(new PublicApiError('network')),
      vi.fn().mockRejectedValue(new PublicApiError('network'))
    );
    await ui.prepare();
    await ui.click('external-connect');
    expect(ui.has('external-review')).toBe(false);
    expect(ui.has('external-complete')).toBe(false);
    ui.get.mockResolvedValue(initial);
    await ui.click('external-refresh');
    expect(ui.has('external-review')).toBe(true);
    expect(ui.node('external-management-key').props.value).toBe('');
    expect(ui.post).toHaveBeenCalledOnce();
  });

  it.each([401, 403])('retires Admin authority on %s without a recovery read', async (status) => {
    const ui = await mount(
      vi.fn(),
      vi.fn().mockRejectedValue(new PublicApiError('authentication', status))
    );
    await ui.prepare();
    await ui.click('external-connect');
    expect(ui.unauthorized).toHaveBeenCalledOnce();
    expect(ui.get).not.toHaveBeenCalled();
  });

  it('refuses foreign or stale recovery state', async () => {
    for (const invalid of [
      { ...committed, revision: 3 },
      { ...committed, identity: { ...initial.identity, installationId: 'other' } },
      { ...committed, routing: { ...initial.routing, target: '/other' } },
      { ...committed, strategy: { ...committed.strategy, stage: 'staged' } },
    ]) {
      const ui = await mount(
        vi.fn().mockResolvedValue(invalid),
        vi.fn().mockRejectedValue(new PublicApiError('network'))
      );
      await ui.prepare();
      await ui.click('external-connect');
      expect(ui.has('external-complete')).toBe(false);
      expect(ui.has('external-review')).toBe(false);
    }
  });

  it('prevents duplicate clicks while saving and removes key plaintext immediately', async () => {
    let resolve!: (op: FreshOperation) => void;
    const post = vi.fn().mockImplementation(
      () =>
        new Promise<FreshOperation>((r) => {
          resolve = r;
        })
    );
    const ui = await mount(undefined, post);
    await ui.prepare();
    const handler = ui.node('external-connect').props.onClick;
    await act(async () => {
      handler();
      handler();
    });
    expect(post).toHaveBeenCalledOnce();
    expect(JSON.stringify(ui.renderer.toJSON())).not.toContain('source-secret');
    await act(async () => resolve(committed));
    expect(ui.has('external-complete')).toBe(true);
  });

  it('keeps local validation errors out of the network', async () => {
    const ui = await mount();
    await ui.field('external-base-url', 'http://user:password@cpa:8317');
    await ui.field('external-management-key', 'source-secret');
    await ui.click('external-review');
    expect(ui.has('external-connect')).toBe(false);
    expect(ui.post).not.toHaveBeenCalled();
  });

  it('turns each guarded Supervisor refusal into its own recovery message', async () => {
    vi.stubGlobal('window', { location: new URL('https://manager.example:18317') });
    const cases = [
      ['embedded_supervisor_status_unavailable', 'setupBlockerSupervisorUnavailable'],
      ['embedded_runtime_start_unresolved', 'setupBlockerRuntimeStartUnresolved'],
      ['embedded_runtime_not_stopped', 'setupBlockerRuntimeNotStopped'],
      ['embedded_runtime_recovery_unverified', 'setupBlockerRuntimeRecoveryUnverified'],
    ] as const;
    const generic = new Set([
      t('externalConflict'),
      t('externalUnavailable'),
      t('externalSourceAuthRejected'),
    ]);
    for (const [code, key] of cases) {
      const actionSentinel = `RAW_SUPERVISOR_ACTION_SENTINEL_${code}_9f4c2e`;
      const transport = vi.fn<typeof fetch>(async (input) => {
        const url = String(input);
        if (url.endsWith('/strategy')) {
          return Response.json({ Code: code, Action: actionSentinel }, { status: 409 });
        }
        return Response.json(initial, { status: 200 });
      });
      const realClient = createPublicApiClient(
        () => ({ basePath: '/console', apiBase: '/console/api/v2', revision: 4 }),
        transport
      );
      const ui = await mount(
        vi.fn((resource: string, credential?: FullWebCredential) =>
          realClient.get(resource, credential)
        ),
        vi.fn((resource: string, body: unknown, credential: FullWebCredential) =>
          realClient.post(resource, body, credential)
        )
      );
      await ui.prepare();
      await ui.click('external-connect');
      expect(transport).toHaveBeenCalledWith(
        'https://manager.example:18317/console/api/v2/strategy',
        expect.objectContaining({ method: 'POST' })
      );
      const alert = ui.renderer.root.findByProps({ role: 'alert' }).props.children;
      expect(code + ': ' + String(alert)).toBe(code + ': ' + x(key));
      expect(alert).not.toBe(t('externalConflict'));
      for (const other of generic) expect(alert).not.toBe(other);
      const shown = JSON.stringify(ui.renderer.toJSON());
      expect(shown).not.toContain(actionSentinel);
    }
  });

  it('keeps the generic conflict for a refusal it cannot name', async () => {
    const post = vi
      .fn()
      .mockRejectedValue(new PublicApiError('http', 409, undefined, 'embedded_runtime_unknown'));
    const ui = await mount(vi.fn().mockResolvedValue(initial), post);
    await ui.prepare();
    await ui.click('external-connect');
    expect(ui.renderer.root.findByProps({ role: 'alert' }).props.children).toBe(
      t('externalConflict')
    );
  });

  it('re-reads the recovery message in the operator language after a switch', async () => {
    const post = vi
      .fn()
      .mockRejectedValue(
        new PublicApiError('http', 409, undefined, 'embedded_supervisor_status_unavailable')
      );
    const ui = await mount(vi.fn().mockResolvedValue(initial), post);
    await ui.prepare();
    await ui.click('external-connect');
    await act(async () => setFullWebLanguage('ru'));
    expect(ui.renderer.root.findByProps({ role: 'alert' }).props.children).toBe(
      x('setupBlockerSupervisorUnavailable')
    );
    expect(String(x('setupBlockerSupervisorUnavailable'))).toContain('cpamp-runtime-supervisor');
  });
});
