import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { FreshKeyRevocation } from './FreshKeyRevocation';
import { createPublicApiClient } from './publicApi';
import { fullWebText as t } from '../i18n/fullweb';
import { keyWorkspaceText as k } from '../i18n/freshKeyWorkspace';
import { Modal } from '../components/ui/Modal';

vi.mock('../components/ui/Modal', () => ({
  Modal: ({
    open,
    children,
    footer,
  }: {
    open: boolean;
    children?: ReactNode;
    footer?: ReactNode;
    closeDisabled?: boolean;
    onClose?: () => void;
  }) =>
    open ? (
      <div>
        {children}
        {footer}
      </div>
    ) : null,
}));
const id = 'b'.repeat(32);
const target = 'a'.repeat(32);
const keeper = 'c'.repeat(32);
const operation = {
  id,
  targetId: target,
  keeperId: keeper,
  mode: 'replace',
  outcome: 'pending',
  revision: 1,
  steps: [
    { name: 'add', state: 'unattempted', returned: false },
    { name: 'revoke', state: 'unattempted', returned: false },
  ],
};
const result = (o: unknown = operation, nextStep = 'add') =>
  Response.json({ operation: o, nextStep, runtimeState: 'effect_unknown', expiresAtMs: 0 });
// The drawer asks the server whether the exact selection on screen would be
// accepted before it offers the submit, so every mount sees that read. It is
// answered outside the queue: the queue holds the calls a test is asserting on.
const ready = () => Response.json({ ready: true, reason: '' });
const routed = (
  responses: Array<() => Response | Promise<Response>>,
  preflight: () => Response = ready
) =>
  vi.fn<typeof fetch>((input, init) => {
    if (
      init?.method !== 'POST' &&
      /\/revocation\/(?:[0-9a-f]{32}\/(?:revoke|replace)|revoke)$/.test(String(input))
    ) {
      return Promise.resolve(preflight());
    }
    const next = responses.shift();
    if (!next) return Promise.reject(new Error('unexpected request: ' + String(input)));
    return Promise.resolve(next());
  });
const keys = [target, keeper].map((id) => ({
  id,
  requestId: id,
  maskedPreview: 'cpamp_sk_…test',
  createdAtMs: 1,
  managerState: 'persisted' as const,
  runtimeState: 'not_applied' as const,
  name: '',
  nameRevision: 1,
  origin: 'workspace' as const,
}));

describe('Canonical revocation recovery UI', () => {
  let view: ReactTestRenderer;
  const pending = vi.fn();
  const changed = vi.fn();
  beforeEach(() => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    pending.mockClear();
    changed.mockClear();
    vi.stubGlobal('window', {
      location: new URL('https://manager.example/control'),
      confirm: () => true,
    });
    vi.stubGlobal('crypto', { getRandomValues: (bytes: Uint8Array) => bytes.fill(0xbb) });
  });
  afterEach(async () => {
    if (view) await act(async () => view.unmount());
    vi.unstubAllGlobals();
  });
  const button = (name: Parameters<typeof t>[0]) =>
    view.root.findAllByType('button').find((b) => b.props.children === t(name))!;
  const click = async (name: Parameters<typeof t>[0]) => {
    await act(async () => button(name).props.onClick());
  };
  async function mount(fetchMock: ReturnType<typeof vi.fn<typeof fetch>>) {
    await act(async () => {
      view = create(
        <FreshKeyRevocation
          keys={keys}
          client={createPublicApiClient(
            () => ({ basePath: '/control', apiBase: '/control/api/v2', revision: 1 }),
            fetchMock
          )}
          credential="test-admin"
          onUnauthorized={vi.fn()}
          onPending={pending}
          onChanged={changed}
          disabled={false}
        />
      );
    });
  }
  async function select() {
    await act(async () =>
      view.root.findAllByType('select')[0].props.onChange({ target: { value: target } })
    );
    await act(async () =>
      view.root.findAllByType('select')[1].props.onChange({ target: { value: keeper } })
    );
    await act(async () =>
      view.root.findAllByType('select')[2].props.onChange({ target: { value: 'replace' } })
    );
  }
  it('rejects a prepare reply with another canonical selection and recovers only the original request', async () => {
    const fetchMock = routed([
      () => result(null, ''),
      () => result({ ...operation, targetId: 'd'.repeat(32) }),
      () => result(),
    ]);
    await mount(fetchMock);
    await select();
    await click('keysOperationPrepare');
    expect(JSON.stringify(view.toJSON())).toContain(t('keysOperationUnknown'));
    expect(pending).toHaveBeenLastCalledWith(true);
    await click('keysOperationCheck');
    expect(
      fetchMock.mock.calls.some(([input]) => String(input).includes('/operations/' + id))
    ).toBe(true);
    expect(button('keysOperationContinue').props.disabled).toBe(false);
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
  });

  it('keeps the first request identity when prepare is activated twice in one turn', async () => {
    let resolvePrepare!: (response: Response) => void;
    const fetchMock = routed([
      () => result(null, ''),
      () => new Promise<Response>((resolve) => { resolvePrepare = resolve; }),
    ]);
    await mount(fetchMock);
    await select();
    const random = vi.fn()
      .mockImplementationOnce((bytes: Uint8Array) => bytes.fill(0xbb))
      .mockImplementationOnce((bytes: Uint8Array) => bytes.fill(0xcc));
    vi.stubGlobal('crypto', { getRandomValues: random });
    await act(async () => {
      button('keysOperationPrepare').props.onClick();
      button('keysOperationPrepare').props.onClick();
    });
    expect(random).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
    await act(async () => resolvePrepare(result()));
    expect(JSON.stringify(view.toJSON())).toContain(id);
    expect(button('keysOperationContinue').props.disabled).toBe(false);
  });

  it('recovers original attempted journal from a 503 read without unlocking a write', async () => {
    const attempted = {
      ...operation,
      outcome: 'unknown',
      revision: 3,
      steps: [{ name: 'add', state: 'attempted', returned: true }, operation.steps[1]],
    };
    const observed = {
      ...attempted,
      outcome: 'pending',
      revision: 4,
      steps: [{ name: 'add', state: 'observed', returned: true }, operation.steps[1]],
    };
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        Response.json(
          {
            code: 'key_operation_unknown',
            result: { operation: attempted, nextStep: '', runtimeState: 'effect_unknown', expiresAtMs: 0 },
          },
          { status: 503 }
        )
      )
      .mockResolvedValueOnce(result(observed, 'revoke'));
    await mount(fetchMock);
    expect(JSON.stringify(view.toJSON())).toContain(id);
    expect(button('keysOperationContinue').props.disabled).toBe(true);
    expect(pending).toHaveBeenLastCalledWith(true);
    await click('keysOperationCheck');
    expect(String(fetchMock.mock.calls[1][0])).toContain('/operations/' + id);
    expect(button('keysOperationContinue').props.disabled).toBe(false);
    expect(fetchMock.mock.calls.every(([, init]) => init?.method === 'GET')).toBe(true);
  });
  it('retains the original journal on generation mismatch but disables continuation', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValueOnce(
      Response.json(
        {
          code: 'key_operation_generation_mismatch',
          result: { operation, nextStep: '', runtimeState: 'effect_unknown', expiresAtMs: 0 },
        },
        { status: 409 }
      )
    );
    await mount(fetchMock);
    expect(JSON.stringify(view.toJSON())).toContain(id);
    expect(JSON.stringify(view.toJSON())).toContain(t('keysOperationRejected'));
    expect(button('keysOperationContinue').props.disabled).toBe(true);
    expect(pending).toHaveBeenLastCalledWith(true);
  });
  it('never unlocks a durable operation on a null or regressed response', async () => {
    const known = {
      ...operation,
      revision: 3,
      steps: [{ name: 'add', state: 'observed', returned: true }, operation.steps[1]],
    };
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(result(known, 'revoke'))
      .mockResolvedValueOnce(result(null, ''))
      .mockResolvedValueOnce(result());
    await mount(fetchMock);
    await click('keysOperationCheck');
    expect(JSON.stringify(view.toJSON())).toContain(t('keysOperationUnknown'));
    expect(button('keysOperationContinue').props.disabled).toBe(true);
    await click('keysOperationCheck');
    expect(button('keysOperationContinue').props.disabled).toBe(true);
    expect(pending).not.toHaveBeenCalledWith(false);
    expect(fetchMock.mock.calls.every(([, init]) => init?.method === 'GET')).toBe(true);
  });
  it('recovers latest after reload, continues once, then refreshes retained canonical history', async () => {
    const committed = {
      ...operation,
      revision: 4,
      outcome: 'committed',
      steps: operation.steps.map((s) => ({ ...s, state: 'observed', returned: true })),
    };
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(result(operation, 'add'))
      .mockResolvedValueOnce(result(committed, ''));
    await mount(fetchMock);
    expect(String(fetchMock.mock.calls[0][0])).toContain('/operations/latest');
    await click('keysOperationContinue');
    expect(String(fetchMock.mock.calls[1][0])).toContain('/operations/' + id + '/continue');
    expect(changed).toHaveBeenCalledTimes(1);
    expect(pending).toHaveBeenLastCalledWith(false);
    expect(JSON.stringify(view.toJSON())).toContain(t('keysOperationCommitted'));
  });
  it('keeps an issued request locked on ambiguous check failure and unlocks when check proves operation null', async () => {
    const committed = {
      ...operation,
      id: 'd'.repeat(32),
      revision: 4,
      outcome: 'committed',
      steps: operation.steps.map((s) => ({ ...s, state: 'observed', returned: true })),
    };
    let checkAttempts = 0;
    const fetchMock = routed([
      () => result(committed, ''),
      () => new Response('', { status: 409 }),
      () => {
        checkAttempts += 1;
        return Promise.reject(new Error('ambiguous network drop'));
      },
      () => result(null, ''),
    ]);
    const closeMock = vi.fn();
    await act(async () => {
      view = create(
        <FreshKeyRevocation
          keys={keys}
          client={createPublicApiClient(
            () => ({ basePath: '/control', apiBase: '/control/api/v2', revision: 1 }),
            fetchMock
          )}
          credential="test-admin"
          onUnauthorized={vi.fn()}
          onPending={pending}
          onChanged={changed}
          disabled={false}
          onClose={closeMock}
        />
      );
    });
    await select();
    await click('keysOperationPrepare');
    expect(pending).toHaveBeenLastCalledWith(true);
    expect(view.root.findByType(Modal).props.closeDisabled).toBe(true);
    // Ambiguous network failure during read-only check must NOT unlock.
    await click('keysOperationCheck');
    expect(checkAttempts).toBe(1);
    expect(pending).toHaveBeenLastCalledWith(true);
    expect(view.root.findByType('fieldset').props.disabled).toBe(true);
    expect(view.root.findByType(Modal).props.closeDisabled).toBe(true);
    // Valid 200 { operation: null } for the same pending request proves unrecorded and unlocks.
    await click('keysOperationCheck');
    expect(
      fetchMock.mock.calls.filter(([input]) => String(input).includes('/operations/' + id))
    ).toHaveLength(2);
    expect(pending).toHaveBeenLastCalledWith(false);
    expect(view.root.findByType('fieldset').props.disabled).toBe(false);
    expect(view.root.findByType(Modal).props.closeDisabled).toBe(false);
    expect(JSON.stringify(view.toJSON())).toContain(k('operationNotRecorded'));
    expect(button('keysOperationPrepare').props.disabled).toBe(false);
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
  });

  it('names the server verdict in the drawer instead of submitting into a conflict', async () => {
    const fetchMock = routed(
      [() => result(null, '')],
      () => Response.json({ ready: false, reason: 'key_operation_conflict' })
    );
    await mount(fetchMock);
    await select();
    expect(
      view.root.findAllByProps({ 'data-testid': 'key-operation-preflight' }).length
    ).toBeGreaterThan(0);
    expect(JSON.stringify(view.toJSON())).toContain(k('operationBlockedInstalledKey'));
    expect(button('keysOperationPrepare').props.disabled).toBe(true);
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(0);
  });

  it('names the refused credential as a rebind instead of a retry', async () => {
    const fetchMock = routed(
      [() => result(null, '')],
      () => Response.json({ ready: false, reason: 'external_management_key_rejected' })
    );
    await mount(fetchMock);
    await select();
    expect(
      view.root.findAllByProps({ 'data-testid': 'key-operation-preflight' }).length
    ).toBeGreaterThan(0);
    const rendered = JSON.stringify(view.toJSON());
    expect(rendered).toContain(k('operationBlockedCredentialRejected'));
    expect(rendered).not.toContain(k('operationBlockedUnavailable'));
    expect(button('keysOperationPrepare').props.disabled).toBe(true);
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(0);
  });

  it('offers the submit once the server accepts the exact selection', async () => {
    const fetchMock = routed([() => result(null, ''), () => result()]);
    await mount(fetchMock);
    await select();
    expect(
      view.root.findAllByProps({ 'data-testid': 'key-operation-preflight' }).length
    ).toBe(0);
    expect(button('keysOperationPrepare').props.disabled).toBe(false);
    await click('keysOperationPrepare');
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
  });

  it('shows last-key protection notice when only one active key exists and honours preset only when idle', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(result(null, ''));
    await act(async () => {
      view = create(
        <FreshKeyRevocation
          keys={[keys[0]]}
          client={createPublicApiClient(
            () => ({ basePath: '/control', apiBase: '/control/api/v2', revision: 1 }),
            fetchMock
          )}
          credential="test-admin"
          onUnauthorized={vi.fn()}
          onPending={pending}
          onChanged={changed}
          disabled={false}
          preset={{ targetId: target, mode: 'replace', seq: 1 }}
        />
      );
    });
    expect(
      view.root.findAllByProps({ 'data-testid': 'last-key-protection-notice' }).length
    ).toBeGreaterThan(0);
    expect(
      view.root.findAllByProps({ 'data-testid': 'key-replacement-guide' }).length
    ).toBeGreaterThan(0);
    expect(view.root.findAllByType('select')[0].props.value).toBe(target);
    expect(view.root.findAllByType('select')[2].props.value).toBe('replace');
  });

  it('completes a preset adopted-key revoke in a single confirmation and renders a clean post-commit state', async () => {
    const adoptedKeys = [
      {
        ...keys[0],
        origin: 'adopted' as const,
        maskedPreview: 'cpamp_sk_…1111',
      },
      {
        ...keys[1],
        origin: 'adopted' as const,
        maskedPreview: 'cpamp_sk_…2222',
      },
    ];
    const preparedRevoke = {
      id,
      targetId: target,
      keeperId: keeper,
      mode: 'revoke' as const,
      outcome: 'pending' as const,
      revision: 1,
      steps: [{ name: 'revoke' as const, state: 'unattempted' as const, returned: false }],
    };
    const committedRevoke = {
      ...preparedRevoke,
      outcome: 'committed' as const,
      revision: 3,
      steps: [{ name: 'revoke' as const, state: 'observed' as const, returned: true }],
    };
    let targetRevoked = false;
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = new URL(String(input)).pathname;
      const method = init?.method ?? 'GET';
      if (method === 'GET' && /\/revocation\/[0-9a-f]{32}\/(revoke|replace)$/.test(url)) {
        return Promise.resolve(
          Response.json({ ready: !targetRevoked, reason: targetRevoked ? 'key_operation_not_found' : '' })
        );
      }
      if (method === 'GET' && url === '/control/api/v2/api-keys/operations/latest') {
        return Promise.resolve(result(null, ''));
      }
      if (method === 'POST' && url === `/control/api/v2/api-keys/${target}/revocation`) {
        return Promise.resolve(result(preparedRevoke, 'revoke'));
      }
      if (
        method === 'POST' &&
        url === `/control/api/v2/api-keys/operations/${preparedRevoke.id}/continue`
      ) {
        targetRevoked = true;
        return Promise.resolve(result(committedRevoke, ''));
      }
      return Promise.reject(new Error(`unexpected ${method} ${url}`));
    });
    const apiClient = createPublicApiClient(
      () => ({ basePath: '/control', apiBase: '/control/api/v2', revision: 1 }),
      fetchMock
    );
    const preset = { targetId: target, mode: 'revoke' as const, seq: 1 };

    await act(async () => {
      view = create(
        <FreshKeyRevocation
          keys={adoptedKeys}
          client={apiClient}
          credential="test-admin"
          onUnauthorized={vi.fn()}
          onPending={pending}
          onChanged={changed}
          disabled={false}
          preset={preset}
          onClose={vi.fn()}
        />
      );
    });

    const confirmBtn = view.root
      .findAllByType('button')
      .find((b) => b.props.children === k('confirmOperation'))!;
    expect(confirmBtn.props.disabled).toBe(false);

    await act(async () => confirmBtn.props.onClick());
    expect(view.root.findByType(Modal).props.closeDisabled).toBe(false);
    expect(pending).toHaveBeenLastCalledWith(false);

    // Parent refreshes keys: revoked target is now superseded, leaving 1 active key.
    const refreshedKeys = [
      { ...adoptedKeys[0], lifecycle: 'superseded' as const },
      adoptedKeys[1],
    ];
    await act(async () => {
      view.update(
        <FreshKeyRevocation
          keys={refreshedKeys}
          client={apiClient}
          credential="test-admin"
          onUnauthorized={vi.fn()}
          onPending={pending}
          onChanged={changed}
          disabled={false}
          preset={preset}
          onClose={vi.fn()}
        />
      );
    });

    const statusRegion = view.root.findByProps({ role: 'status' });
    const statusCodes = statusRegion
      .findByProps({ className: 'key-record-identifiers' })
      .findAllByType('code')
      .map((node) => node.children.join(''));
    expect(statusCodes).toEqual([preparedRevoke.id, target, keeper]);

    expect(view.root.findAllByType('fieldset')).toHaveLength(0);
    expect(view.root.findAllByProps({ 'data-testid': 'key-operation-preflight' })).toHaveLength(0);
    expect(view.root.findAllByProps({ 'data-testid': 'last-key-protection-notice' })).toHaveLength(
      0
    );
  });

  it('retains the durable journal for recovery when a preset step fails mid-flight', async () => {
    const attemptedAdd = {
      ...operation,
      outcome: 'unknown' as const,
      revision: 2,
      steps: [{ name: 'add' as const, state: 'attempted' as const, returned: true }, operation.steps[1]],
    };
    const observedAdd = {
      ...operation,
      outcome: 'pending' as const,
      revision: 3,
      steps: [{ name: 'add' as const, state: 'observed' as const, returned: true }, operation.steps[1]],
    };
    const committedReplace = {
      ...operation,
      outcome: 'committed' as const,
      revision: 5,
      steps: operation.steps.map((s) => ({ ...s, state: 'observed' as const, returned: true })),
    };
    const recordedCalls: Array<{ method: string; url: string; body: unknown }> = [];
    let continueAttempts = 0;
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = new URL(String(input)).pathname;
      const method = init?.method ?? 'GET';
      if (method === 'GET' && /\/revocation\/[0-9a-f]{32}\/(revoke|replace)$/.test(url)) {
        return Promise.resolve(ready());
      }
      const parsedBody = typeof init?.body === 'string' && init.body ? JSON.parse(init.body) : null;
      recordedCalls.push({ method, url, body: parsedBody });
      if (method === 'GET' && url === '/control/api/v2/api-keys/operations/latest') {
        return Promise.resolve(result(null, ''));
      }
      if (method === 'POST' && url === `/control/api/v2/api-keys/${target}/revocation`) {
        return Promise.resolve(result(operation, 'add'));
      }
      if (method === 'POST' && url === `/control/api/v2/api-keys/operations/${operation.id}/continue`) {
        continueAttempts += 1;
        if (continueAttempts === 1) {
          return Promise.resolve(
            Response.json(
              {
                code: 'key_operation_unknown',
                result: {
                  operation: attemptedAdd,
                  nextStep: '',
                  runtimeState: 'effect_unknown',
                  expiresAtMs: 0,
                },
              },
              { status: 503 }
            )
          );
        }
        return Promise.resolve(result(committedReplace, ''));
      }
      if (method === 'GET' && url === `/control/api/v2/api-keys/operations/${operation.id}`) {
        return Promise.resolve(result(observedAdd, 'revoke'));
      }
      return Promise.reject(new Error(`unexpected ${method} ${url}`));
    });
    await act(async () => {
      view = create(
        <FreshKeyRevocation
          keys={keys}
          client={createPublicApiClient(
            () => ({ basePath: '/control', apiBase: '/control/api/v2', revision: 1 }),
            fetchMock
          )}
          credential="test-admin"
          onUnauthorized={vi.fn()}
          onPending={pending}
          onChanged={changed}
          disabled={false}
          preset={{ targetId: target, mode: 'replace', seq: 2 }}
        />
      );
    });
    const confirmBtn = view.root
      .findAllByType('button')
      .find((b) => b.props.children === k('confirmOperation'))!;
    await act(async () => confirmBtn.props.onClick());

    // Stops immediately after the first failed continue without issuing a second POST.
    expect(recordedCalls).toEqual([
      { method: 'GET', url: '/control/api/v2/api-keys/operations/latest', body: null },
      {
        method: 'POST',
        url: `/control/api/v2/api-keys/${target}/revocation`,
        body: { requestId: id, keeperId: keeper, mode: 'replace' },
      },
      {
        method: 'POST',
        url: `/control/api/v2/api-keys/operations/${operation.id}/continue`,
        body: {},
      },
    ]);
    expect(view.root.findByProps({ role: 'alert' }).findByType('span').children).toEqual([
      t('keysOperationUnknown'),
    ]);
    expect(pending).toHaveBeenLastCalledWith(true);
    expect(button('keysOperationContinue').props.disabled).toBe(true);

    // Check inspects the exact original operation ID, then Continue resumes that same operation ID.
    await click('keysOperationCheck');
    expect(button('keysOperationContinue').props.disabled).toBe(false);
    await click('keysOperationContinue');
    expect(changed).toHaveBeenCalledTimes(1);
    expect(pending).toHaveBeenLastCalledWith(false);
    expect(recordedCalls.slice(3)).toEqual([
      { method: 'GET', url: `/control/api/v2/api-keys/operations/${operation.id}`, body: null },
      {
        method: 'POST',
        url: `/control/api/v2/api-keys/operations/${operation.id}/continue`,
        body: {},
      },
    ]);
  });

  it('supports keeperless revoke across 3+ keys without requiring a single keeper selection', async () => {
    const key3 = 'e'.repeat(32);
    const threeKeys = [
      ...keys.map((item) => ({ ...item, runtimeState: 'effective_observed' as const })),
      { ...keys[0], id: key3, requestId: key3, runtimeState: 'effective_observed' as const },
    ];
    const preparedKeeperless = {
      id,
      targetId: key3,
      keeperId: '',
      mode: 'revoke' as const,
      outcome: 'pending' as const,
      revision: 1,
      steps: [{ name: 'revoke' as const, state: 'unattempted' as const, returned: false }],
    };
    const preflightUrls: string[] = [];
    const postBodies: Array<{ url: string; body: unknown }> = [];
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = new URL(String(input)).pathname;
      const method = init?.method ?? 'GET';
      if (method === 'GET' && url.includes('/revocation/')) {
        preflightUrls.push(url);
        return Promise.resolve(ready());
      }
      if (method === 'GET' && url === '/control/api/v2/api-keys/operations/latest') {
        return Promise.resolve(result(null, ''));
      }
      if (method === 'POST') {
        const parsedBody = typeof init?.body === 'string' && init.body ? JSON.parse(init.body) : null;
        postBodies.push({ url, body: parsedBody });
        return Promise.resolve(result(preparedKeeperless, 'revoke'));
      }
      return Promise.reject(new Error(`unexpected ${method} ${url}`));
    });
    await act(async () => {
      view = create(
        <FreshKeyRevocation
          keys={threeKeys}
          client={createPublicApiClient(
            () => ({ basePath: '/control', apiBase: '/control/api/v2', revision: 1 }),
            fetchMock
          )}
          credential="test-admin"
          onUnauthorized={vi.fn()}
          onPending={pending}
          onChanged={changed}
          disabled={false}
        />
      );
    });
    await act(async () => {
      view.root.findAllByType('select')[0].props.onChange({ target: { value: key3 } });
    });
    expect(preflightUrls).toEqual([`/control/api/v2/api-keys/${key3}/revocation/revoke`]);
    expect(button('keysOperationPrepare').props.disabled).toBe(false);
    await click('keysOperationPrepare');
    expect(postBodies).toEqual([
      {
        url: `/control/api/v2/api-keys/${key3}/revocation`,
        body: { requestId: id, keeperId: '', mode: 'revoke' },
      },
    ]);
  });

  it('ignores historical committed add operations on mount and recovers unsettled add operations', async () => {
    const committedAdd = {
      id: 'd'.repeat(32),
      targetId: target,
      keeperId: '',
      mode: 'add' as const,
      outcome: 'committed' as const,
      revision: 2,
      steps: [{ name: 'add' as const, state: 'observed' as const, returned: true }],
    };
    const pendingAdd = {
      ...committedAdd,
      outcome: 'pending' as const,
      revision: 1,
      steps: [{ name: 'add' as const, state: 'unattempted' as const, returned: false }],
    };
    const fetchMock1 = routed([
      () =>
        Response.json({
          operation: committedAdd,
          nextStep: '',
          runtimeState: 'effective_observed',
          expiresAtMs: 0,
        }),
    ]);
    await mount(fetchMock1);
    expect(changed).not.toHaveBeenCalled();
    expect(pending).toHaveBeenLastCalledWith(false);
    expect(JSON.stringify(view.toJSON())).not.toContain(t('keysOperationCommitted'));
    expect(view.root.findByType('fieldset').props.disabled).toBe(false);

    await act(async () => view.unmount());
    const fetchMock2 = routed([
      () => result(pendingAdd, 'add'),
      () =>
        Response.json({
          operation: committedAdd,
          nextStep: '',
          runtimeState: 'effective_observed',
          expiresAtMs: 0,
        }),
    ]);
    await mount(fetchMock2);
    expect(pending).toHaveBeenLastCalledWith(true);
    expect(button('keysOperationContinue').props.disabled).toBe(false);
    await click('keysOperationContinue');
    expect(changed).toHaveBeenCalledTimes(1);
    expect(pending).toHaveBeenLastCalledWith(false);
  });

  it('updates displayed step state immediately from failed POST continue 503 and 409 snapshots and surfaces rebind', async () => {
    const attemptedAdd = {
      ...operation,
      outcome: 'unknown' as const,
      revision: 2,
      steps: [{ name: 'add' as const, state: 'attempted' as const, returned: true }, operation.steps[1]],
    };
    const rejectedAdd = {
      ...operation,
      outcome: 'pending' as const,
      revision: 2,
      steps: [{ name: 'add' as const, state: 'observed' as const, returned: true }, operation.steps[1]],
    };
    const rebind = vi.fn();
    const fetchMock = routed([
      () => result(operation, 'add'),
      () =>
        Response.json(
          {
            code: 'key_operation_unknown',
            detail: 'gateway timeout',
            result: {
              operation: attemptedAdd,
              nextStep: '',
              runtimeState: 'effect_unknown',
              expiresAtMs: 0,
            },
          },
          { status: 503 }
        ),
      () => result({ ...rejectedAdd, revision: 2 }, 'revoke'),
      () =>
        Response.json(
          {
            code: 'external_management_key_rejected',
            detail: 'client_forbidden',
            result: {
              operation: rejectedAdd,
              nextStep: '',
              runtimeState: 'effect_unknown',
              expiresAtMs: 0,
            },
          },
          { status: 409 }
        ),
    ]);
    await act(async () => {
      view = create(
        <FreshKeyRevocation
          keys={keys}
          client={createPublicApiClient(
            () => ({ basePath: '/control', apiBase: '/control/api/v2', revision: 1 }),
            fetchMock
          )}
          credential="test-admin"
          onUnauthorized={vi.fn()}
          onPending={pending}
          onChanged={changed}
          onRebind={rebind}
          disabled={false}
        />
      );
    });
    expect(view.root.findByProps({ 'data-testid': 'key-operation-step-add' }).props['data-state']).toBe(
      'unattempted'
    );
    await click('keysOperationContinue');
    // Immediate update from POST 503 result without any follow-up GET.
    expect(
      fetchMock.mock.calls.filter(([input, init]) => init?.method !== 'POST' && String(input).includes('/operations/' + id))
    ).toHaveLength(0);
    expect(view.root.findByProps({ 'data-testid': 'key-operation-step-add' }).props['data-state']).toBe(
      'attempted'
    );
    expect(JSON.stringify(view.toJSON())).not.toContain(t('keysOperationAddNext'));
    expect(button('keysOperationContinue').props.disabled).toBe(true);

    // Check confirms add observed and nextStep revoke, then POST continue hits 409 external_management_key_rejected.
    await click('keysOperationCheck');
    expect(view.root.findByProps({ 'data-testid': 'key-operation-step-add' }).props['data-state']).toBe(
      'observed'
    );
    await click('keysOperationContinue');
    expect(view.root.findByProps({ 'data-testid': 'key-operation-blocker-detail' }).props.children).toBe(
      k('effectBlockerExternalRejected')
    );
    const rebindBtn = view.root.findByProps({ 'data-testid': 'operation-rebind-action' });
    await act(async () => rebindBtn.props.onClick());
    expect(rebind).toHaveBeenCalledTimes(1);
  });

  it('renders aborted operations as non-applied terminal state, never committed, and unblocks new operations', async () => {
    const aborted = {
      ...operation,
      outcome: 'aborted' as const,
      revision: 2,
      steps: [
        { name: 'add' as const, state: 'unattempted' as const, returned: false },
        { name: 'revoke' as const, state: 'unattempted' as const, returned: false },
      ],
    };
    const fetchMock = routed([
      () => result(operation, 'add'),
      () =>
        Response.json(
          {
            code: 'key_operation_conflict',
            result: {
              operation: aborted,
              nextStep: '',
              runtimeState: 'effect_unknown',
              expiresAtMs: 0,
            },
          },
          { status: 409 }
        ),
    ]);
    await mount(fetchMock);
    await click('keysOperationContinue');
    expect(pending).toHaveBeenLastCalledWith(false);
    expect(changed).toHaveBeenCalledTimes(1);
    expect(view.root.findByProps({ 'data-testid': 'key-operation-outcome' }).props.children).toBe(
      k('operationAborted')
    );
    expect(JSON.stringify(view.toJSON())).not.toContain(t('keysOperationCommitted'));
    expect(view.root.findByType('fieldset').props.disabled).toBe(false);
  });

  it('replaces unrecorded local request on prepare 409 key_operation_conflict with another active server operation and completes it without relaxing lost-response ID matching', async () => {
    const serverAddId = 'e'.repeat(32);
    const serverAddTarget = 'f'.repeat(32);
    const activeAdd = {
      id: serverAddId,
      targetId: serverAddTarget,
      keeperId: '',
      mode: 'add' as const,
      outcome: 'pending' as const,
      revision: 1,
      steps: [{ name: 'add' as const, state: 'unattempted' as const, returned: false }],
    };
    const committedAdd = {
      ...activeAdd,
      outcome: 'committed' as const,
      revision: 2,
      steps: [{ name: 'add' as const, state: 'observed' as const, returned: false }],
    };
    const calls: string[] = [];
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (
        init?.method !== 'POST' &&
        /\/revocation\/(?:[0-9a-f]{32}\/(?:revoke|replace)|revoke)$/.test(url)
      ) {
        return Promise.resolve(ready());
      }
      calls.push(`${init?.method || 'GET'} ${url}`);
      if (url.endsWith('/api-keys/operations/latest')) {
        return Promise.resolve(result(null, ''));
      }
      if (url.endsWith(`/api-keys/${target}/revocation`) && init?.method === 'POST') {
        return Promise.resolve(
          Response.json(
            {
              code: 'key_operation_conflict',
              result: {
                operation: activeAdd,
                nextStep: '',
                runtimeState: 'effect_unknown',
                expiresAtMs: 0,
              },
            },
            { status: 409 }
          )
        );
      }
      if (url.endsWith(`/api-keys/operations/${serverAddId}`) && (!init?.method || init.method === 'GET')) {
        return Promise.resolve(result(activeAdd, 'add'));
      }
      if (url.endsWith(`/api-keys/operations/${serverAddId}/continue`) && init?.method === 'POST') {
        return Promise.resolve(
          Response.json({
            operation: committedAdd,
            nextStep: '',
            runtimeState: 'effective_observed',
            expiresAtMs: 0,
          })
        );
      }
      return Promise.reject(new Error('unexpected request: ' + url));
    });

    await mount(fetchMock);
    await select();
    await click('keysOperationPrepare');

    expect(pending).toHaveBeenLastCalledWith(true);
    expect(JSON.stringify(view.toJSON())).not.toContain('b'.repeat(32));
    expect(JSON.stringify(view.toJSON())).toContain(serverAddId);
    expect(view.root.findByProps({ 'data-testid': 'key-operation-blocker-detail' }).props.children).toBe(
      k('effectBlockerConflict')
    );
    expect(button('keysOperationContinue').props.disabled).toBe(true);
    expect(view.root.findByType('fieldset').props.disabled).toBe(true);

    await click('keysOperationCheck');
    expect(calls.some((entry) => entry.endsWith(`/api-keys/operations/${serverAddId}`))).toBe(true);
    expect(button('keysOperationContinue').props.disabled).toBe(false);

    await click('keysOperationContinue');
    expect(pending).toHaveBeenLastCalledWith(false);
    expect(changed).toHaveBeenCalledTimes(1);

    // Ordinary lost-response check (GET operations/<localId>) still rejects mismatched IDs.
    await act(async () => view.unmount());
    const lostFetch = routed([
      () => result(null, ''),
      () => Promise.reject(new Error('network drop')),
      () =>
        Response.json(
          {
            code: 'key_operation_conflict',
            result: {
              operation: activeAdd,
              nextStep: '',
              runtimeState: 'effect_unknown',
              expiresAtMs: 0,
            },
          },
          { status: 409 }
        ),
    ]);
    await mount(lostFetch);
    await select();
    await click('keysOperationPrepare');
    await click('keysOperationCheck');
    expect(JSON.stringify(view.toJSON())).toContain('b'.repeat(32));
    expect(JSON.stringify(view.toJSON())).not.toContain(serverAddId);
  });

  it('preserves external_management_key_rejected reason, client_forbidden detail, and onRebind across aborted preset replace and parent keys refresh', async () => {
    const rebind = vi.fn();
    const createReplacement = vi.fn();
    const abortedReplace = {
      ...operation,
      outcome: 'aborted' as const,
      revision: 2,
      steps: [
        { name: 'add' as const, state: 'unattempted' as const, returned: false },
        { name: 'revoke' as const, state: 'unattempted' as const, returned: false },
      ],
    };
    const client = createPublicApiClient(
      () => ({ basePath: '/control', apiBase: '/control/api/v2', revision: 1 }),
      routed([
        () => result(null, ''),
        () => result(operation, 'add'),
        () =>
          Response.json(
            {
              code: 'external_management_key_rejected',
              detail: 'client_forbidden',
              result: {
                operation: abortedReplace,
                nextStep: '',
                runtimeState: 'not_applied',
                expiresAtMs: 0,
              },
            },
            { status: 409 }
          ),
      ])
    );

    await act(async () => {
      view = create(
        <FreshKeyRevocation
          keys={keys}
          client={client}
          credential="test-admin"
          onUnauthorized={vi.fn()}
          onPending={pending}
          onChanged={changed}
          onRebind={rebind}
          onCreateReplacement={createReplacement}
          preset={{ targetId: target, mode: 'replace', seq: 1 }}
          disabled={false}
        />
      );
    });

    await act(async () =>
      view.root.findAllByType('button').find((item) => item.props.children === k('replacementReady'))!.props.onClick()
    );
    await act(async () =>
      view.root.findAllByType('button').find((item) => item.props.children === k('confirmOperation'))!.props.onClick()
    );

    expect(changed).toHaveBeenCalledTimes(1);
    // Simulate parent refreshing keys prop after onChanged().
    await act(async () => {
      view.update(
        <FreshKeyRevocation
          keys={[...keys]}
          client={client}
          credential="test-admin"
          onUnauthorized={vi.fn()}
          onPending={pending}
          onChanged={changed}
          onRebind={rebind}
          onCreateReplacement={createReplacement}
          preset={{ targetId: target, mode: 'replace', seq: 1 }}
          disabled={false}
        />
      );
    });

    expect(JSON.stringify(view.toJSON())).toContain(k('operationAborted'));
    expect(view.root.findByProps({ 'data-testid': 'key-operation-blocker-detail' }).props.children).toBe(
      k('effectBlockerExternalRejected')
    );
    expect(
      JSON.stringify(view.root.findByProps({ 'data-testid': 'key-operation-rebind-detail' }).findByType('p').props.children)
    ).toContain('client_forbidden');
    await act(async () =>
      view.root.findByProps({ 'data-testid': 'operation-rebind-action' }).props.onClick()
    );
    expect(rebind).toHaveBeenCalledTimes(1);
  });
});
