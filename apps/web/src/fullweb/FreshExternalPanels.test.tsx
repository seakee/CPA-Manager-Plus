import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { StrictMode, type ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { FreshRebindDialog } from './FreshRebindDialog';
import { FreshExternalUsage } from './FreshExternalUsage';
import { parseExternalUsageState } from './externalState';
import { setFullWebLanguage } from '../i18n/fullweb';
import { createPublicApiClient, PublicApiError, type PublicApiClient } from './publicApi';
import { externalWorkspaceText as x } from '../i18n/freshExternal';

// The rendered text of one subtree. A renderer node's children are elements as
// well as strings, so the assertion collects the leaves rather than stringifying
// the React tree (which is circular).
function textOf(renderer: ReactTestRenderer, testId: string): string {
  const node = renderer.root.find((candidate) => candidate.props['data-testid'] === testId);
  return node
    .findAll((candidate) => typeof candidate.type === 'string')
    .flatMap((candidate) => candidate.children)
    .filter((child): child is string => typeof child === 'string')
    .join(' ');
}

vi.mock('../components/ui/Modal', () => ({
  Modal: ({
    open,
    title,
    children,
    footer,
  }: {
    open: boolean;
    title?: string;
    children?: ReactNode;
    footer?: ReactNode;
  }) =>
    open ? (
      <div data-testid="mock-modal">
        {title && <h3>{title}</h3>}
        {children}
        {footer}
      </div>
    ) : null,
}));

// A client stub that records exactly what the components asked for. Nothing here
// writes: the point of these tests is that the POST happens only when asked.
function stubClient(post: (resource: string, body: unknown) => Promise<unknown>, get?: () => Promise<unknown>) {
  const calls: { resource: string; body: unknown }[] = [];
  const client = {
    get: vi.fn(async () => (get ? await get() : {})),
    post: vi.fn(async (resource: string, body: unknown) => {
      calls.push({ resource, body });
      return post(resource, body);
    }),
    put: vi.fn(),
  } as unknown as PublicApiClient;
  return { client, calls };
}

const context = { basePath: '/console', apiBase: '/console/api/v2', revision: 1 };

const usageState = {
  statisticsEnabled: false,
  collector: 'stopped',
  collectionStarted: false,
  cpaVersion: { version: 'v8.0.55', support: 'supported', writeBlocked: false },
};

describe('External rebind dialog', () => {
  beforeEach(() => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    vi.stubGlobal('crypto', { getRandomValues: (bytes: Uint8Array) => bytes.fill(0x11) });
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    setFullWebLanguage('zh-CN');
  });

  async function typeKey(renderer: ReactTestRenderer, value: string) {
    const input = renderer.root.find(
      (node) => node.props['data-testid'] === 'rebind-management-key'
    );
    await act(async () => input.props.onChange({ target: { value } }));
  }

  async function submit(renderer: ReactTestRenderer) {
    const button = renderer.root.find(
      (node) => node.props['data-testid'] === 'rebind-submit'
    );
    await act(async () => button.props.onClick());
  }

  it('posts the key once and reports the rebound outcome', async () => {
    const { client, calls } = stubClient(async () => ({
      identity: 'external:sha256:' + 'a'.repeat(64),
      generation: 1,
      status: 'rebound',
      revision: 4,
    }));
    const rebound = vi.fn();
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshRebindDialog
          open
          client={client}
          credential="admin"
          onUnauthorized={() => undefined}
          onClose={() => undefined}
          onRebound={rebound}
        />
      );
    });
    await typeKey(renderer, 'rotated-key');
    await submit(renderer);
    expect(calls).toHaveLength(1);
    expect(calls[0].resource).toBe('external/rebind');
    expect(calls[0].body).toMatchObject({ managementKey: 'rotated-key' });
    expect(rebound).toHaveBeenCalledWith({ reverified: false });
    expect(textOf(renderer, 'rebind-result')).toBe(x('rebindSucceededRebound'));
  });

  it('reports a same-key re-verify as reverified, not as a rotation', async () => {
    const { client } = stubClient(async () => ({
      identity: 'external:sha256:' + 'a'.repeat(64),
      generation: 1,
      status: 'reverified',
      revision: 4,
    }));
    const rebound = vi.fn();
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshRebindDialog
          open
          client={client}
          credential="admin"
          onUnauthorized={() => undefined}
          onClose={() => undefined}
          onRebound={rebound}
        />
      );
    });
    await typeKey(renderer, 'same-key');
    await submit(renderer);
    expect(rebound).toHaveBeenCalledWith({ reverified: true });
  });

  it('reads a refused rebind in the operator language and keeps the server detail as a diagnostic', async () => {
    let attempt = 0;
    const { client, calls } = stubClient(async () => {
      attempt += 1;
      if (attempt === 1)
        throw new PublicApiError(
          'http',
          409,
          undefined,
          'external_rebind_refused',
          undefined,
          'the address of this installation is already committed'
        );
      return { identity: 'x', generation: 1, status: 'rebound', revision: 5 };
    });
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshRebindDialog
          open
          client={client}
          credential="admin"
          onUnauthorized={() => undefined}
          onClose={() => undefined}
          onRebound={() => undefined}
        />
      );
    });
    await typeKey(renderer, 'wrong-key');
    await submit(renderer);
    expect(textOf(renderer, 'rebind-error')).toBe(x('rebindRefusedHelp'));
    expect(textOf(renderer, 'rebind-error-detail')).toContain('already committed');
    // The input is cleared only on success, so a lost answer is not re-keyed.
    await submit(renderer);
    expect(calls).toHaveLength(2);
    expect((calls[0].body as { requestId: string }).requestId).toBe(
      (calls[1].body as { requestId: string }).requestId
    );
  });

  it('handles an expired Manager session without blaming the key', async () => {
    const { client, calls } = stubClient(async () => {
      throw new PublicApiError('authentication', 401);
    });
    const unauthorized = vi.fn();
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshRebindDialog
          open
          client={client}
          credential="admin"
          onUnauthorized={unauthorized}
          onClose={() => undefined}
          onRebound={() => undefined}
        />
      );
    });
    await typeKey(renderer, 'key');
    await submit(renderer);
    expect(calls).toHaveLength(1);
    // The session ended, not the key: the shell takes the operator back and the
    // dialog says so instead of claiming the rebind failed.
    expect(unauthorized).toHaveBeenCalledTimes(1);
    expect(textOf(renderer, 'rebind-authentication-error')).toBe(x('rebindAuthenticationHelp'));
    expect(
      renderer.root.findAll((node) => node.props['data-testid'] === 'rebind-error')
    ).toHaveLength(0);
    const input = renderer.root.find(
      (node) => node.props['data-testid'] === 'rebind-management-key'
    );
    expect(input.props.value).toBe('');
  });

  it('names a stale generation as reload-and-retry rather than a refused key', async () => {
    const { client } = stubClient(async () => {
      throw new PublicApiError('http', 409, undefined, 'external_rebind_stale_generation');
    });
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshRebindDialog
          open
          client={client}
          credential="admin"
          onUnauthorized={() => undefined}
          onClose={() => undefined}
          onRebound={() => undefined}
        />
      );
    });
    await typeKey(renderer, 'key');
    await submit(renderer);
    const text = textOf(renderer, 'rebind-error');
    // The message must not be the refused-key instruction: the two need
    // different actions, and the page must not send the user down the wrong one.
    expect(text).not.toBe(x('blockerKeyRejectedHelp'));
    expect(text).toBe(x('rebindStaleHelp'));
  });
});

describe('External usage confirmation', () => {
  beforeEach(() => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    // The real client resolves its resource against the page origin.
    vi.stubGlobal('window', { location: new URL('https://manager.example/console') });
    vi.stubGlobal('crypto', { getRandomValues: (bytes: Uint8Array) => bytes.fill(0x22) });
  });
  afterEach(() => vi.unstubAllGlobals());

  it('never enables statistics without an explicit confirm', async () => {
    const { client, calls } = stubClient(async () => usageState, async () => usageState);
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshExternalUsage client={client} credential="admin" onUnauthorized={() => undefined} />
      );
    });
    // Opening the confirmation is not a write.
    const open = renderer.root.find(
      (node) => node.props['data-testid'] === 'external-usage-enable'
    );
    await act(async () => open.props.onClick());
    expect(calls).toHaveLength(0);
    // Cancelling is not a write either.
    const cancel = renderer.root.find(
      (node) => node.props['data-testid'] === 'external-usage-cancel'
    );
    await act(async () => cancel.props.onClick());
    expect(calls).toHaveLength(0);

    await act(async () => open.props.onClick());
    const confirm = renderer.root.find(
      (node) => node.props['data-testid'] === 'external-usage-confirm'
    );
    await act(async () => confirm.props.onClick());
    expect(calls).toHaveLength(1);
    expect(calls[0].resource).toBe('external/usage');
    expect(calls[0].body).toMatchObject({ confirmEnableStatistics: true });
    expect(typeof (calls[0].body as { requestId: string }).requestId).toBe('string');
  });

  it('shows a collector that never started as not started, never as zero coverage', async () => {
    const { client } = stubClient(async () => usageState, async () => usageState);
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshExternalUsage client={client} credential="admin" onUnauthorized={() => undefined} />
      );
    });
    const text = textOf(renderer, 'external-usage-started');
    // The value is "not started" in the active locale. The point is that a window
    // that never opened never prints a count.
    expect(text.endsWith(x('usageNotStarted'))).toBe(true);
    expect(text).not.toMatch(/\d/);
  });

  it('re-reads the panel when the shell reports a successful rebind', async () => {
    let reads = 0;
    const { client } = stubClient(
      async () => undefined,
      async () => {
        reads += 1;
        return usageState;
      }
    );
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshExternalUsage client={client} credential="admin" onUnauthorized={() => undefined} />
      );
    });
    expect(reads).toBe(1);
    // The panel is mounted once, so without this it would keep the refusal it
    // saw before the fix that resolved it.
    await act(async () => {
      renderer.update(
        <FreshExternalUsage
          client={client}
          credential="admin"
          onUnauthorized={() => undefined}
          reloadToken={1}
        />
      );
    });
    expect(reads).toBe(2);
  });

  // A rebind re-reads the panel. Whatever the previous generation still owes the
  // panel describes the installation before the fix, so it must never repaint a
  // blocker the rebind resolved: not a read, not a confirmation.
  it('ignores a read superseded by the rebind re-read', async () => {
    let failStale: (error: Error) => void = () => undefined;
    let reads = 0;
    const blockers: (string | null | undefined)[] = [];
    const client = {
      get: vi.fn(async () => {
        reads += 1;
        if (reads === 1)
          return new Promise<unknown>((_resolve, reject) => {
            failStale = reject;
          });
        return {
          ...usageState,
          statisticsEnabled: true,
          collector: 'running',
          collectionStarted: true,
        };
      }),
      post: vi.fn(),
      put: vi.fn(),
    } as unknown as PublicApiClient;
    let renderer!: ReactTestRenderer;
    const panel = (reloadToken: number) => (
      <FreshExternalUsage
        client={client}
        credential="admin"
        onUnauthorized={() => undefined}
        onBlocker={(code) => blockers.push(code)}
        reloadToken={reloadToken}
      />
    );
    await act(async () => {
      renderer = create(panel(0));
    });
    expect(reads).toBe(1);
    await act(async () => {
      renderer.update(panel(1));
    });
    // The stale read fails the way a superseded authority read does: a typed code
    // that on its own would paint `authority_unavailable`.
    await act(async () =>
      failStale(
        new PublicApiError('http', 503, undefined, 'external_cpa_authority_unavailable')
      )
    );
    expect(textOf(renderer, 'external-usage-collector')).toContain(x('usageCollectorRunning'));
    expect(renderer.root.findAllByProps({ 'data-testid': 'external-usage-reload' })).toHaveLength(
      0
    );
    // The retried authority code never reached the page: the only statement the
    // caller heard is the clean one from the read that replaced it.
    expect(blockers).toEqual([null]);
  });

  // Ordering 3: a confirmation is in flight when the rebind re-reads the panel.
  // The stale confirmation's failure must not put an authority blocker over the
  // healthy state the reload just proved, and its release must still settle.
  it('ignores a confirmation superseded by the rebind re-read', async () => {
    let confirmAt = 0;
    let reads = 0;
    let failStaleConfirm: (error: Error) => void = () => undefined;
    const blockers: (string | null | undefined)[] = [];
    const client = {
      // The first read offers the enable action; the rebind's re-read proves it.
      get: vi.fn(async () => {
        reads += 1;
        return reads === 1
          ? usageState
          : { ...usageState, statisticsEnabled: true, collector: 'running', collectionStarted: true };
      }),
      post: vi.fn(async () => {
        confirmAt += 1;
        if (confirmAt === 1)
          return new Promise<unknown>((_resolve, reject) => {
            failStaleConfirm = reject;
          });
        return { ...usageState, statisticsEnabled: true, collector: 'running', collectionStarted: true };
      }),
      put: vi.fn(),
    } as unknown as PublicApiClient;
    let renderer!: ReactTestRenderer;
    const panel = (reloadToken: number) => (
      <FreshExternalUsage
        client={client}
        credential="admin"
        onUnauthorized={() => undefined}
        onBlocker={(code) => blockers.push(code)}
        reloadToken={reloadToken}
      />
    );
    await act(async () => {
      renderer = create(panel(0));
    });
    const enable = renderer.root.findByProps({ 'data-testid': 'external-usage-enable' });
    await act(async () => enable.props.onClick());
    // The confirmation is pending and the operator rebinds meanwhile.
    const confirm = renderer.root.findByProps({ 'data-testid': 'external-usage-confirm' });
    act(() => void confirm.props.onClick());
    await act(async () => {
      renderer.update(panel(1));
    });
    await act(async () =>
      failStaleConfirm(
        new PublicApiError('http', 503, undefined, 'external_cpa_authority_unavailable')
      )
    );
    expect(textOf(renderer, 'external-usage-collector')).toContain(x('usageCollectorRunning'));
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-usage-confirm-failed' })
    ).toHaveLength(0);
    // Every statement the page heard is a clear one; the stale failure added none.
    expect(blockers).not.toContain('external_cpa_authority_unavailable');
    expect(new Set(blockers)).toEqual(new Set([null]));
  });

  // Ordering 5: a confirmation failure is the newest fact the panel holds, so it
  // retires the reads still waiting on the pre-confirmation installation. Those
  // report the state the failed confirmation was trying to change, and applying
  // one would clear the blocker the failure just named.
  it('keeps a confirmation blocker when a read from before it settles afterwards', async () => {
    let settleHeldRead: (response: unknown) => void = () => undefined;
    let reads = 0;
    const blockers: (string | null | undefined)[] = [];
    const client = {
      get: vi.fn(async () => {
        reads += 1;
        // The read issued before the confirmation is held open.
        if (reads === 2)
          return new Promise<unknown>((resolve) => {
            settleHeldRead = resolve;
          });
        return usageState;
      }),
      post: vi.fn(async () => {
        throw new PublicApiError('http', 503, undefined, 'external_cpa_authority_unavailable');
      }),
      put: vi.fn(),
    } as unknown as PublicApiClient;
    let renderer!: ReactTestRenderer;
    const panel = (reloadToken: number) => (
      <FreshExternalUsage
        client={client}
        credential="admin"
        onUnauthorized={() => undefined}
        onBlocker={(code) => blockers.push(code)}
        reloadToken={reloadToken}
      />
    );
    await act(async () => {
      renderer = create(panel(0));
    });
    const enable = renderer.root.findByProps({ 'data-testid': 'external-usage-enable' });
    await act(async () => enable.props.onClick());
    // A reload starts a read that is still pending when the operator confirms.
    await act(async () => {
      renderer.update(panel(1));
    });
    const confirm = renderer.root.findByProps({ 'data-testid': 'external-usage-confirm' });
    await act(async () => confirm.props.onClick());
    expect(blockers[blockers.length - 1]).toBe('external_cpa_authority_unavailable');
    const afterFailure = blockers.length;

    // The read that described the pre-confirmation installation now answers.
    await act(async () =>
      settleHeldRead({
        ...usageState,
        statisticsEnabled: true,
        collector: 'running',
        collectionStarted: true,
      })
    );

    // The page does not hear a stale clear, and the blocker stands.
    expect(blockers).toHaveLength(afterFailure);
    expect(renderer.root.findAllByProps({ 'data-testid': 'external-usage-confirm-failed' })).toHaveLength(1);
  });

  // Ordering 6: an authentication failure from a retired confirmation is not an
  // answer about the current session. The rebind already proved the connection
  // works, so retiring happens before any failure callback, including that one.
  it('does not end the session for an authentication failure of a retired confirmation', async () => {
    let failStaleConfirm: (response: Response) => void = () => undefined;
    let reads = 0;
    const unauthorized = vi.fn();
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/external/usage') && init?.method === 'POST') {
        return new Promise<Response>((resolve) => {
          failStaleConfirm = resolve;
        });
      }
      if (url.endsWith('/external/usage')) {
        reads += 1;
        return Promise.resolve(
          Response.json(
            reads === 1
              ? usageState
              : { ...usageState, statisticsEnabled: true, collector: 'running', collectionStarted: true }
          )
        );
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });
    const client = createPublicApiClient(() => context, fetchMock);
    let renderer!: ReactTestRenderer;
    const panel = (reloadToken: number) => (
      <FreshExternalUsage
        client={client}
        credential="admin"
        onUnauthorized={unauthorized}
        reloadToken={reloadToken}
      />
    );
    await act(async () => {
      renderer = create(panel(0));
    });
    const enable = renderer.root.findByProps({ 'data-testid': 'external-usage-enable' });
    await act(async () => enable.props.onClick());
    const confirm = renderer.root.findByProps({ 'data-testid': 'external-usage-confirm' });
    act(() => void confirm.props.onClick());
    // The rebind re-reads the panel and proves the connection works.
    await act(async () => {
      renderer.update(panel(1));
    });
    await act(async () => failStaleConfirm(Response.json({ code: 'unauthorized' }, { status: 401 })));
    expect(unauthorized).not.toHaveBeenCalled();
    expect(textOf(renderer, 'external-usage-collector')).toContain(x('usageCollectorRunning'));
  });

  // Ordering 4: an older confirmation settles after the panel has moved on.
  // Its late success must not clear the blocker the newer state named, and the
  // claim it held must be released so a confirmation can be offered again.
  it('keeps the newer blocker when an older confirmation settles afterwards', async () => {
    let postResolve: ((value: unknown) => void) | undefined;
    const blockers: (string | null | undefined)[] = [];
    let reads = 0;
    // The reload succeeds and names the refusal itself, so the panel keeps both
    // its facts and an enable action for the operator to retry.
    const refusedState = {
      ...usageState,
      statisticsEnabled: true,
      collector: 'stopped',
      collectionStarted: false,
      blockedReason: 'external_cpa_authority_unavailable',
    };
    const client = {
      get: vi.fn(async () => {
        reads += 1;
        return reads === 1 ? usageState : refusedState;
      }),
      post: vi.fn(
        async () =>
          new Promise<unknown>((resolve) => {
            postResolve = resolve;
          })
      ),
      put: vi.fn(),
    } as unknown as PublicApiClient;
    let renderer!: ReactTestRenderer;
    const panel = (reloadToken: number) => (
      <FreshExternalUsage
        client={client}
        credential="admin"
        onUnauthorized={() => undefined}
        onBlocker={(code) => blockers.push(code)}
        reloadToken={reloadToken}
      />
    );
    await act(async () => {
      renderer = create(panel(0));
    });
    const enable = renderer.root.findByProps({ 'data-testid': 'external-usage-enable' });
    await act(async () => enable.props.onClick());
    const confirm = renderer.root.findByProps({ 'data-testid': 'external-usage-confirm' });
    act(() => void confirm.props.onClick());
    await act(async () => {
      renderer.update(panel(1));
    });
    // The reload named the refusal, and the panel offers the way to retry it.
    expect(
      renderer.root.findByProps({ 'data-testid': 'external-usage-blocked' })
    ).toBeDefined();
    const blockerCount = blockers.length;

    await act(async () =>
      postResolve?.({
        ...usageState,
        statisticsEnabled: true,
        collector: 'running',
        collectionStarted: true,
      })
    );

    // The stale success publishes nothing: the newer facts and blocker stand.
    expect(blockers).toHaveLength(blockerCount);
    expect(textOf(renderer, 'external-usage-collector')).toContain(x('usageCollectorStopped'));

    // The claim the superseded confirmation held is released: a new confirmation
    // is accepted instead of being refused by an owner that no longer exists.
    const enableAgain = renderer.root.findByProps({ 'data-testid': 'external-usage-enable' });
    expect(enableAgain.props.disabled).toBe(false);
    await act(async () => enableAgain.props.onClick());
    const confirmAgain = renderer.root.findByProps({ 'data-testid': 'external-usage-confirm' });
    await act(async () => confirmAgain.props.onClick());
    expect(client.post).toHaveBeenCalledTimes(2);
  });

  // A double click lands twice before React commits the first `setBusy`: both
  // entries read the old state, so only a synchronous claim can stop the second
  // POST. The later real outcome must be the one the panel reports.
  it('sends one confirmation for two entries in the same React batch', async () => {
    const blockers: (string | null | undefined)[] = [];
    let settleConfirm: (value: unknown) => void = () => undefined;
    const running = {
      ...usageState,
      statisticsEnabled: true,
      collector: 'running',
      collectionStarted: true,
    };
    const client = {
      get: vi.fn(async () => usageState),
      post: vi.fn(
        async () =>
          new Promise<unknown>((resolve) => {
            settleConfirm = resolve;
          })
      ),
      put: vi.fn(),
    } as unknown as PublicApiClient;
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshExternalUsage
          client={client}
          credential="admin"
          onUnauthorized={() => undefined}
          onBlocker={(code) => blockers.push(code)}
        />
      );
    });
    const enable = renderer.root.findByProps({ 'data-testid': 'external-usage-enable' });
    await act(async () => enable.props.onClick());
    const confirm = renderer.root.findByProps({ 'data-testid': 'external-usage-confirm' });
    // Both clicks are dispatched before either answer, in one act().
    await act(async () => {
      void confirm.props.onClick();
      void confirm.props.onClick();
    });
    expect(client.post).toHaveBeenCalledTimes(1);

    await act(async () => settleConfirm(running));
    expect(textOf(renderer, 'external-usage-collector')).toContain(x('usageCollectorRunning'));
    // The sole POST is what produced the running facts: the second entry sent
    // nothing, so nothing could be dropped in its favour.
    expect(new Set(blockers)).toEqual(new Set([null]));
  });

  // StrictMode replays mount effects on the same instance. The discarded pass's
  // read must be retired without publishing a stale error or ending the session,
  // and the panel must still allow a subsequent confirmation.
  it('stays correct when React replays its mount effects', async () => {
    let settleFirstRead: (response: Response) => void = () => undefined;
    let reads = 0;
    let posts = 0;
    const blockers: (string | null | undefined)[] = [];
    const unauthorized = vi.fn();
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/external/usage') && init?.method === 'POST') {
        posts += 1;
        return Promise.resolve(
          Response.json({
            ...usageState,
            statisticsEnabled: true,
            collector: 'running',
            collectionStarted: true,
          })
        );
      }
      if (url.endsWith('/external/usage')) {
        reads += 1;
        if (reads === 1) {
          return new Promise<Response>((resolve) => {
            settleFirstRead = resolve;
          });
        }
        return Promise.resolve(Response.json(usageState));
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });
    const client = createPublicApiClient(() => context, fetchMock);
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <StrictMode>
          <FreshExternalUsage
            client={client}
            credential="admin"
            onUnauthorized={unauthorized}
            onBlocker={(code) => blockers.push(code)}
          />
        </StrictMode>
      );
    });
    expect(reads).toBe(2);
    expect(new Set(blockers)).toEqual(new Set([null]));

    // Discarded pass-1 read answers late with 401; replay must have retired it.
    await act(async () => settleFirstRead(Response.json({ code: 'unauthorized' }, { status: 401 })));
    expect(unauthorized).not.toHaveBeenCalled();
    expect(renderer.root.findAllByProps({ 'data-testid': 'external-usage-reload' })).toHaveLength(0);
    expect(posts).toBe(0);

    // Confirmation remains usable after StrictMode replay.
    const enable = renderer.root.findByProps({ 'data-testid': 'external-usage-enable' });
    await act(async () => enable.props.onClick());
    const confirm = renderer.root.findByProps({ 'data-testid': 'external-usage-confirm' });
    await act(async () => confirm.props.onClick());
    expect(posts).toBe(1);
    expect(textOf(renderer, 'external-usage-collector')).toContain(x('usageCollectorRunning'));
  });

  // A panel that unmounts while its read is pending publishes nothing when the
  // answer arrives: no state, no blocker, no session callback.
  it('publishes nothing from a read that answers after unmount', async () => {
    let settleRead: (value: unknown) => void = () => undefined;
    const blockers: (string | null | undefined)[] = [];
    const unauthorized = vi.fn();
    const client = {
      get: vi.fn(
        async () =>
          new Promise<unknown>((resolve) => {
            settleRead = resolve;
          })
      ),
      post: vi.fn(),
      put: vi.fn(),
    } as unknown as PublicApiClient;
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshExternalUsage
          client={client}
          credential="admin"
          onUnauthorized={unauthorized}
          onBlocker={(code) => blockers.push(code)}
        />
      );
    });
    await act(async () => renderer.unmount());
    await act(async () => settleRead(usageState));
    expect(blockers).toEqual([]);
    expect(unauthorized).not.toHaveBeenCalled();
  });

  // A confirmation still pending when the panel goes away must leave nothing
  // behind: its answer publishes nothing and ends no session.
  it('publishes nothing from a confirmation that answers after unmount', async () => {
    let settleConfirm: (value: unknown) => void = () => undefined;
    const blockers: (string | null | undefined)[] = [];
    const unauthorized = vi.fn();
    const client = {
      get: vi.fn(async () => usageState),
      post: vi.fn(
        async () =>
          new Promise<unknown>((resolve) => {
            settleConfirm = resolve;
          })
      ),
      put: vi.fn(),
    } as unknown as PublicApiClient;
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshExternalUsage
          client={client}
          credential="admin"
          onUnauthorized={unauthorized}
          onBlocker={(code) => blockers.push(code)}
        />
      );
    });
    const enable = renderer.root.findByProps({ 'data-testid': 'external-usage-enable' });
    await act(async () => enable.props.onClick());
    const confirm = renderer.root.findByProps({ 'data-testid': 'external-usage-confirm' });
    act(() => void confirm.props.onClick());
    expect(client.post).toHaveBeenCalledTimes(1);
    const published = blockers.length;

    await act(async () => renderer.unmount());
    await act(async () =>
      settleConfirm({
        ...usageState,
        statisticsEnabled: true,
        collector: 'running',
        collectionStarted: true,
      })
    );
    expect(blockers).toHaveLength(published);
    expect(unauthorized).not.toHaveBeenCalled();
  });

  // The ownership rule suppresses only *retired* failures. A failure that still
  // owns the panel must end the session, or the suppression would hide a real
  // expiry behind a dead panel.
  // The 401 must arrive as what the client really produces for it: a Response,
  // parsed by `createPublicApiClient` into an authentication failure.
  it('ends the session for an authentication failure of the current read', async () => {
    const unauthorized = vi.fn();
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshExternalUsage
          client={createPublicApiClient(
            () => context,
            vi.fn<typeof fetch>(async () => Response.json({ code: 'unauthorized' }, { status: 401 }))
          )}
          credential="admin"
          onUnauthorized={unauthorized}
        />
      );
    });
    expect(unauthorized).toHaveBeenCalledTimes(1);
    await act(async () => renderer.unmount());
  });

  it('ends the session for an authentication failure of the current confirmation', async () => {
    const unauthorized = vi.fn();
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshExternalUsage
          client={createPublicApiClient(
            () => context,
            vi.fn<typeof fetch>(async (_input, init) =>
              init?.method === 'POST'
                ? Response.json({ code: 'unauthorized' }, { status: 401 })
                : Response.json(usageState)
            )
          )}
          credential="admin"
          onUnauthorized={unauthorized}
        />
      );
    });
    const enable = renderer.root.findByProps({ 'data-testid': 'external-usage-enable' });
    await act(async () => enable.props.onClick());
    const confirm = renderer.root.findByProps({ 'data-testid': 'external-usage-confirm' });
    await act(async () => confirm.props.onClick());
    expect(unauthorized).toHaveBeenCalledTimes(1);
  });

  it('names the collector state in the page language instead of the wire value', async () => {
    const { client } = stubClient(
      async () => usageState,
      async () => ({ ...usageState, collector: 'running' })
    );
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshExternalUsage client={client} credential="admin" onUnauthorized={() => undefined} />
      );
    });
    const text = textOf(renderer, 'external-usage-collector');
    expect(text).toContain(x('usageCollectorRunning'));
    expect(text).not.toContain('running');
  });

  it('says writes are allowed rather than verified', async () => {
    setFullWebLanguage('en');
    const { client } = stubClient(async () => usageState, async () => usageState);
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshExternalUsage client={client} credential="admin" onUnauthorized={() => undefined} />
      );
    });
    const text = textOf(renderer, 'external-usage-version');
    // `writeBlocked=false` is permission, not proof: the Manager has not written
    // anything yet, so the row may only claim that writes are allowed.
    expect(text).toContain('Writes allowed');
    expect(text).not.toContain('Writes verified');
  });

  it('offers a retry when the confirmation fails, keeping the panel and the request', async () => {
    let attempts = 0;
    const { client, calls } = stubClient(
      async () => {
        attempts += 1;
        if (attempts === 1) throw new PublicApiError('http', 409, undefined, 'external_cpa_authority_unavailable');
        return { ...usageState, statisticsEnabled: true };
      },
      async () => usageState
    );
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshExternalUsage client={client} credential="admin" onUnauthorized={() => undefined} />
      );
    });
    const open = renderer.root.find(
      (node) => node.props['data-testid'] === 'external-usage-enable'
    );
    await act(async () => open.props.onClick());
    const confirm = renderer.root.find(
      (node) => node.props['data-testid'] === 'external-usage-confirm'
    );
    await act(async () => confirm.props.onClick());
    // The state the operator is trying to change stays readable, and the failure
    // carries its own way to ask again.
    expect(textOf(renderer, 'external-usage-confirm-failed')).toContain(x('usageEnableFailed'));
    expect(
      renderer.root.findAll((node) => node.props['data-testid'] === 'external-usage-statistics')
    ).toHaveLength(1);
    const retry = renderer.root.find(
      (node) => node.props['data-testid'] === 'external-usage-confirm-retry'
    );
    await act(async () => retry.props.onClick());
    expect(calls).toHaveLength(2);
    // The retry is a replay of the same request, not a second write identity.
    expect((calls[0].body as { requestId: string }).requestId).toBe(
      (calls[1].body as { requestId: string }).requestId
    );
    expect(textOf(renderer, 'external-usage-notice')).toBe(x('usageConfirmed'));
  });

  it('reports the CPA version class alongside the collector state', async () => {
    const { client } = stubClient(
      async () => usageState,
      async () => ({
        ...usageState,
        cpaVersion: { version: 'v7.0.12', support: 'unsupported', writeBlocked: true },
      })
    );
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshExternalUsage client={client} credential="admin" onUnauthorized={() => undefined} />
      );
    });
    const text = textOf(renderer, 'external-usage-version');
    expect(text).toContain('v7.0.12');
    // The class is translated, never the raw wire token.
    expect(text).not.toContain('unsupported');
    expect(text).toContain(x('supportUnsupported'));
  });

  it('names the Manager monitoring-disabled blocker in all four locales and never claims confirmation while disabled', async () => {
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru'] as const) {
      setFullWebLanguage(locale);
      const { client } = stubClient(
        async () => ({ ...usageState, blockedReason: 'usage_monitoring_disabled' }),
        async () => ({ ...usageState, blockedReason: 'usage_monitoring_disabled' })
      );
      let renderer!: ReactTestRenderer;
      await act(async () => {
        renderer = create(
          <FreshExternalUsage client={client} credential="admin" onUnauthorized={() => undefined} />
        );
      });
      const blocked = textOf(renderer, 'external-usage-blocked');
      expect(blocked).toContain(x('usageBlockedMonitoring'));
      expect(blocked).not.toContain(x('usageBlockedUnknown'));
      expect(blocked).not.toContain('usage_monitoring_disabled');
      // While Manager's own monitoring switch is off, the CPA write button is hidden.
      expect(
        renderer.root.findAll((node) => node.props['data-testid'] === 'external-usage-enable')
      ).toHaveLength(0);
      await act(async () => renderer.unmount());
    }

    // If monitoring was turned off while a confirmation dialog was already open,
    // settling with `usage_monitoring_disabled` must not announce `usageConfirmed`.
    setFullWebLanguage('en');
    const { client } = stubClient(
      async () => ({
        ...usageState,
        statisticsEnabled: false,
        collectionStarted: false,
        blockedReason: 'usage_monitoring_disabled',
      }),
      async () => usageState
    );
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshExternalUsage client={client} credential="admin" onUnauthorized={() => undefined} />
      );
    });
    const open = renderer.root.find(
      (node) => node.props['data-testid'] === 'external-usage-enable'
    );
    await act(async () => open.props.onClick());
    const confirm = renderer.root.find(
      (node) => node.props['data-testid'] === 'external-usage-confirm'
    );
    await act(async () => confirm.props.onClick());
    expect(
      renderer.root.findAll((node) => node.props['data-testid'] === 'external-usage-notice')
    ).toHaveLength(0);
    expect(textOf(renderer, 'external-usage-blocked')).toContain(x('usageBlockedMonitoring'));
  });

  it('surfaces retry when confirm returns 200 with unstarted blocked collector and allows starting when statistics are already enabled', async () => {
    let attempts = 0;
    const { client, calls } = stubClient(
      async () => {
        attempts += 1;
        if (attempts === 1) {
          return {
            ...usageState,
            statisticsEnabled: true,
            collector: 'stopped',
            collectionStarted: false,
            blockedReason: 'external_cpa_authority_unavailable',
          };
        }
        return {
          ...usageState,
          statisticsEnabled: true,
          collector: 'running',
          collectionStarted: true,
          blockedReason: '',
        };
      },
      async () => ({ ...usageState, blockedReason: 'usage_statistics_disabled' })
    );
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshExternalUsage client={client} credential="admin" onUnauthorized={() => undefined} />
      );
    });
    const open = renderer.root.find(
      (node) => node.props['data-testid'] === 'external-usage-enable'
    );
    await act(async () => open.props.onClick());
    const confirm = renderer.root.find(
      (node) => node.props['data-testid'] === 'external-usage-confirm'
    );
    await act(async () => confirm.props.onClick());

    expect(textOf(renderer, 'external-usage-confirm-failed')).toContain(x('usageEnableFailed'));
    expect(
      renderer.root.findAll((node) => node.props['data-testid'] === 'external-usage-notice')
    ).toHaveLength(0);
    const retry = renderer.root.find(
      (node) => node.props['data-testid'] === 'external-usage-confirm-retry'
    );
    await act(async () => retry.props.onClick());
    expect(calls).toHaveLength(2);
    expect(calls[1].body).toEqual(calls[0].body);
    expect(textOf(renderer, 'external-usage-notice')).toBe(x('usageConfirmed'));
    await act(async () => renderer.unmount());

    // Also verify when statisticsEnabled is already true on initial GET while collector is stopped (e.g. post-rebind).
    const postRebind = stubClient(
      async () => ({
        ...usageState,
        statisticsEnabled: true,
        collector: 'running',
        collectionStarted: true,
        blockedReason: '',
      }),
      async () => ({
        ...usageState,
        statisticsEnabled: true,
        collector: 'stopped',
        collectionStarted: false,
        blockedReason: 'external_cpa_authority_unavailable',
      })
    );
    await act(async () => {
      renderer = create(
        <FreshExternalUsage
          client={postRebind.client}
          credential="admin"
          onUnauthorized={() => undefined}
        />
      );
    });
    const startBtn = renderer.root.find(
      (node) => node.props['data-testid'] === 'external-usage-enable'
    );
    expect(startBtn.children).toEqual([x('usageStartAction')]);
    await act(async () => startBtn.props.onClick());
    const modalText = textOf(renderer, 'mock-modal');
    expect(modalText).toContain(x('usageStartConfirmTitle'));
    expect(modalText).toContain(x('usageStartConfirmHelp'));
    expect(modalText).not.toContain(x('usageConfirmTitle'));
    expect(modalText).not.toContain(x('usageConfirmHelp'));
    const startConfirm = renderer.root.find(
      (node) => node.props['data-testid'] === 'external-usage-confirm'
    );
    expect(startConfirm.children).toEqual([x('usageStartConfirmAction')]);
    await act(async () => startConfirm.props.onClick());
    expect(postRebind.calls).toHaveLength(1);
    expect(textOf(renderer, 'external-usage-collector')).toContain(x('usageCollectorRunning'));
    expect(textOf(renderer, 'external-usage-notice')).toBe(x('usageStarted'));
  });

  it('rejects a malformed usage reply instead of inventing a state', () => {
    expect(parseExternalUsageState({})).toBeNull();
    expect(parseExternalUsageState({ ...usageState, collector: 7 })).toBeNull();
    expect(parseExternalUsageState(usageState)).toEqual(usageState);
  });

  it('retires an earlier read when confirmation starts so its failure cannot trap the panel on readError', async () => {
    let settleRead2: (response: Response) => void = () => undefined;
    let settlePost: (response: Response) => void = () => undefined;
    let reads = 0;
    const blockers: (string | null | undefined)[] = [];
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/external/usage') && init?.method === 'POST') {
        return new Promise<Response>((resolve) => {
          settlePost = resolve;
        });
      }
      if (url.endsWith('/external/usage')) {
        reads += 1;
        if (reads === 1) return Promise.resolve(Response.json(usageState));
        return new Promise<Response>((resolve) => {
          settleRead2 = resolve;
        });
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });
    const client = createPublicApiClient(() => context, fetchMock);
    let renderer!: ReactTestRenderer;
    const panel = (reloadToken: number) => (
      <FreshExternalUsage
        client={client}
        credential="admin"
        onUnauthorized={() => undefined}
        onBlocker={(code) => blockers.push(code)}
        reloadToken={reloadToken}
      />
    );
    await act(async () => {
      renderer = create(panel(0));
    });
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'external-usage-enable' }).props.onClick();
    });
    // Rebind starts read #2 while the confirm modal is open, then operator confirms.
    await act(async () => {
      renderer.update(panel(1));
    });
    act(() => {
      void renderer.root.findByProps({ 'data-testid': 'external-usage-confirm' }).props.onClick();
    });
    // Earlier read #2 settles with 503 while confirm POST is in flight.
    await act(async () => {
      settleRead2(Response.json({ code: 'external_cpa_authority_unavailable' }, { status: 503 }));
    });
    // Confirm succeeds afterwards: panel must show running state, not readError fallback.
    await act(async () => {
      settlePost(
        Response.json({
          ...usageState,
          statisticsEnabled: true,
          collector: 'running',
          collectionStarted: true,
        })
      );
    });
    expect(renderer.root.findAllByProps({ 'data-testid': 'external-usage-reload' })).toHaveLength(0);
    expect(textOf(renderer, 'external-usage-collector')).toContain(x('usageCollectorRunning'));
    expect(textOf(renderer, 'external-usage-notice')).toBe(x('usageConfirmed'));
    expect(blockers[blockers.length - 1]).toBeNull();
  });

  it('discards a retired pre-rebind requestId when a new read supersedes the confirmation', async () => {
    let nextByte = 0x10;
    vi.stubGlobal('crypto', {
      getRandomValues: (bytes: Uint8Array) => bytes.fill((nextByte += 1)),
    });
    const requestIds: string[] = [];
    let settleFirstPost: (response: Response) => void = () => undefined;
    let posts = 0;
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/external/usage') && init?.method === 'POST') {
        posts += 1;
        const parsed: unknown = JSON.parse(String(init.body));
        if (parsed && typeof parsed === 'object' && 'requestId' in parsed && typeof parsed.requestId === 'string') {
          requestIds.push(parsed.requestId);
        }
        if (posts === 1) {
          return new Promise<Response>((resolve) => {
            settleFirstPost = resolve;
          });
        }
        return Promise.resolve(
          Response.json({
            ...usageState,
            statisticsEnabled: true,
            collector: 'running',
            collectionStarted: true,
          })
        );
      }
      if (url.endsWith('/external/usage')) {
        return Promise.resolve(Response.json(usageState));
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });
    const client = createPublicApiClient(() => context, fetchMock);
    let renderer!: ReactTestRenderer;
    const panel = (reloadToken: number) => (
      <FreshExternalUsage
        client={client}
        credential="admin"
        onUnauthorized={() => undefined}
        reloadToken={reloadToken}
      />
    );
    await act(async () => {
      renderer = create(panel(0));
    });
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'external-usage-enable' }).props.onClick();
    });
    act(() => {
      void renderer.root.findByProps({ 'data-testid': 'external-usage-confirm' }).props.onClick();
    });
    await act(async () => {
      renderer.update(panel(1));
    });
    await act(async () => {
      settleFirstPost(Response.json({ code: 'external_cpa_authority_unavailable' }, { status: 503 }));
    });
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'external-usage-enable' }).props.onClick();
    });
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'external-usage-confirm' }).props.onClick();
    });
    expect(requestIds).toHaveLength(2);
    expect(requestIds[1]).not.toBe(requestIds[0]);
  });

  it('releases the confirmation lock when requestId generation throws synchronously', async () => {
    const client = createPublicApiClient(
      () => context,
      vi.fn<typeof fetch>((input, init) => {
        const url = String(input);
        if (url.endsWith('/external/usage') && init?.method === 'POST') {
          return Promise.resolve(
            Response.json({
              ...usageState,
              statisticsEnabled: true,
              collector: 'running',
              collectionStarted: true,
            })
          );
        }
        if (url.endsWith('/external/usage')) {
          return Promise.resolve(Response.json(usageState));
        }
        return Promise.reject(new Error('unexpected url: ' + url));
      })
    );
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshExternalUsage client={client} credential="admin" onUnauthorized={() => undefined} />
      );
    });
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'external-usage-enable' }).props.onClick();
    });
    const entropySpy = vi.spyOn(crypto, 'getRandomValues').mockImplementationOnce(() => {
      throw new Error('entropy unavailable');
    });
    try {
      await act(async () => {
        renderer.root.findByProps({ 'data-testid': 'external-usage-confirm' }).props.onClick();
      });
    } finally {
      entropySpy.mockRestore();
    }
    const retry = renderer.root.findByProps({ 'data-testid': 'external-usage-confirm-retry' });
    expect(retry.props.disabled).toBe(false);
    await act(async () => {
      retry.props.onClick();
    });
    expect(textOf(renderer, 'external-usage-collector')).toContain(x('usageCollectorRunning'));
  });
});
