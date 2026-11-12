import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { FreshRebindDialog } from './FreshRebindDialog';
import { FreshExternalUsage } from './FreshExternalUsage';
import { parseExternalUsageState } from './externalState';
import { setFullWebLanguage } from '../i18n/fullweb';
import { PublicApiError, type PublicApiClient } from './publicApi';
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
});
