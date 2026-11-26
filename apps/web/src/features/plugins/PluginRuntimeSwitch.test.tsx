import i18n from '@/i18n';
import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest';
import { PublicApiError, type PublicApiClient } from '@/fullweb/publicApi';
import { PluginRuntimeSwitch } from './PluginRuntimeSwitch';

const reactGlobal = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
reactGlobal.IS_REACT_ACT_ENVIRONMENT = true;

const BASELINE = 'a'.repeat(64);

const managedView = (options: {
  enabled?: boolean;
  writable?: boolean;
  operation?: { requestId: string; state: string; http200?: boolean };
} = {}) => ({
  capability: { mode: 'managed', readable: true, writable: true, rulesWritable: true },
  generation: '7',
  groups: [
    {
      id: 'plugins',
      prefix: 'plugins',
      paths: ['plugins.enabled'],
      baseline: BASELINE,
      writable: options.writable !== false,
      persisted: options.operation?.state ?? 'unknown',
      effective: 'unknown',
      ...(options.operation ? { operation: { group: 'plugins', target: 'group', ...options.operation } } : {}),
    },
  ],
  fields: [
    {
      path: 'plugins.enabled',
      type: 'bool',
      kind: 'bool',
      group: 'plugins',
      presence: 'present',
      value: options.enabled === true,
      editable: true,
    },
  ],
  families: [],
  groupsInvalid: false,
});

const savedReply = (requestId: string) => ({
  operation: { requestId, group: 'plugins', target: 'group', state: 'persisted', http200: true },
  effective: 'unknown',
});

let renderer: ReactTestRenderer | undefined;

const text = (node: ReactTestInstance): string =>
  node.children.map((child) => (typeof child === 'string' ? child : text(child))).join('');

const switchInput = () => renderer!.root.findByType('input');

// The notice is the page's one place for a settled outcome. Asserting its key
// keeps these tests about behaviour: a copy edit must not read as a regression,
// and a changed message must not pass silently.
const noticeText = () => {
  const nodes = renderer!.root.findAllByProps({ 'data-testid': 'plugin-runtime-notice' });
  return nodes.length === 0 ? undefined : text(nodes[0]);
};

const client = (get: Mock, post: Mock): PublicApiClient =>
  ({ get, post, put: vi.fn() }) as unknown as PublicApiClient;

const mount = async (api: PublicApiClient, onApplied = vi.fn()) => {
  await act(async () => {
    renderer = create(
      <PluginRuntimeSwitch client={api} credential="admin-test" onApplied={onApplied} />
    );
  });
  return onApplied;
};

afterEach(async () => {
  if (renderer)
    await act(async () => {
      renderer?.unmount();
    });
  renderer = undefined;
  vi.unstubAllGlobals();
});

beforeEach(async () => {
  // These assertions are about structure and behaviour, not copy: the switch's
  // presence, its tested state and what the page claims about the effective
  // value. Pinning a translation would make a copy edit look like a regression.
  await i18n.changeLanguage('en-US');
});

describe('Plugin runtime switch', () => {
  it('shows the value CPA is serving and claims nothing beyond it', async () => {
    const api = client(vi.fn().mockResolvedValue(managedView()), vi.fn());
    await mount(api);
    // The persisted readout reports the tri-state from the Manager projection:
    // "off" here, because presence is `present` and the value is false.
    const persisted = renderer!.root.findByProps({ 'data-testid': 'plugin-runtime-persisted' });
    expect(text(persisted)).toBe(i18n.t('plugin_management.runtime_off'));
    // There is exactly one readout, and it reports the value read back from
    // CPA's own configuration document. A second "effectively running" cell
    // could only repeat it or be a constant, so its absence is the invariant.
    expect(renderer!.root.findAllByProps({ 'data-testid': 'plugin-runtime-effective' })).toHaveLength(
      0
    );
    expect(switchInput().props.checked).toBe(false);
    expect(switchInput().props.disabled).toBe(false);
  });

  it('is absent when the installation does not own the plugin root', async () => {
    const external = {
      ...managedView(),
      capability: {
        mode: 'external',
        readable: false,
        writable: false,
        rulesWritable: false,
        reason: 'plugin_switch_requires_managed_install',
      },
    };
    await mount(client(vi.fn().mockResolvedValue(external), vi.fn()));
    expect(renderer!.toJSON()).toBeNull();
  });

  it('is absent where the Manager has no plugin runtime route at all', async () => {
    const get = vi.fn().mockRejectedValue(new PublicApiError('routing_unavailable'));
    await mount(client(get, vi.fn()));
    expect(renderer!.toJSON()).toBeNull();
  });

  it('writes the switch against the observed baseline and re-reads it', async () => {
    const get = vi
      .fn()
      .mockResolvedValueOnce(managedView())
      .mockResolvedValueOnce(managedView({ enabled: true }));
    const post = vi.fn().mockResolvedValue(savedReply('request-write'));
    const onApplied = await mount(client(get, post));

    await act(async () => {
      switchInput().props.onChange({ target: { checked: true } });
    });

    expect(post).toHaveBeenCalledTimes(1);
    const [resource, body] = post.mock.calls[0];
    expect(resource).toBe('plugins/runtime/groups/plugins');
    expect(body).toMatchObject({
      expected: { generation: '7', baseline: BASELINE },
      edits: [{ path: 'plugins.enabled', action: 'set', value: true }],
    });
    expect(typeof body.requestId).toBe('string');
    expect(body.requestId).toHaveLength(32);
    expect(get).toHaveBeenCalledTimes(2);
    expect(
      text(renderer!.root.findByProps({ 'data-testid': 'plugin-runtime-persisted' }))
    ).toBe(i18n.t('plugin_management.runtime_on'));
    expect(noticeText()).toBe(i18n.t('plugin_management.runtime_saved_effect_unknown'));
    expect(onApplied).toHaveBeenCalledTimes(1);
  });

  it('settles an unknown result by reading back and never resends the write', async () => {
    const get = vi
      .fn()
      .mockResolvedValueOnce(managedView())
      .mockResolvedValueOnce(
        managedView({
          writable: false,
          operation: { requestId: 'request-unknown', state: 'unknown' },
        })
      )
      .mockResolvedValueOnce(managedView({ enabled: true }));
    const post = vi
      .fn()
      .mockRejectedValueOnce(new PublicApiError('network'))
      .mockResolvedValueOnce(savedReply('request-unknown'));
    const onApplied = await mount(client(get, post));

    await act(async () => {
      switchInput().props.onChange({ target: { checked: true } });
    });

    expect(post).toHaveBeenCalledTimes(2);
    expect(post.mock.calls[0][0]).toBe('plugins/runtime/groups/plugins');
    expect(post.mock.calls[1][0]).toBe('plugins/runtime/operations/request-unknown/reconcile');
    expect(post.mock.calls[1][1]).toEqual({});
    expect(noticeText()).toBe(i18n.t('plugin_management.runtime_reconciled'));
    expect(onApplied).toHaveBeenCalledTimes(1);
  });

  it('offers the read-back control for a journaled unknown write after a reload', async () => {
    const pending = managedView({
      enabled: true,
      writable: false,
      operation: { requestId: 'request-journaled', state: 'unknown' },
    });
    const get = vi.fn().mockResolvedValueOnce(pending).mockResolvedValueOnce(managedView({ enabled: true }));
    const post = vi.fn().mockResolvedValue(savedReply('request-journaled'));
    await mount(client(get, post));

    expect(switchInput().props.disabled).toBe(true);
    const reconcile = renderer!.root
      .findAll((node) => node.type === 'button')
      .find((item) => text(item) === i18n.t('plugin_management.runtime_reconcile'));
    expect(reconcile).toBeDefined();

    await act(async () => {
      reconcile!.props.onClick();
    });

    expect(post).toHaveBeenCalledTimes(1);
    expect(post.mock.calls[0][0]).toBe('plugins/runtime/operations/request-journaled/reconcile');
    expect(noticeText()).toBe(i18n.t('plugin_management.runtime_reconciled'));
  });

  it('keeps the draft and reports a moved baseline without writing again', async () => {
    const get = vi.fn().mockResolvedValue(managedView());
    const post = vi
      .fn()
      .mockRejectedValue(new PublicApiError('http', 409, undefined, 'source_or_operation_conflict'));
    const onApplied = await mount(client(get, post));

    await act(async () => {
      switchInput().props.onChange({ target: { checked: true } });
    });

    expect(post).toHaveBeenCalledTimes(1);
    expect(noticeText()).toBe(i18n.t('plugin_management.runtime_conflict'));
    expect(
      renderer!.root.findByProps({ 'data-testid': 'plugin-runtime-notice' }).props['data-tone']
    ).toBe('warning');
    expect(onApplied).not.toHaveBeenCalled();
  });

  it('uses success tone and auto-hides the saved confirmation after 4s while keeping errors persistent', async () => {
    vi.useFakeTimers();
    try {
      const get = vi
        .fn()
        .mockResolvedValueOnce(managedView())
        .mockResolvedValueOnce(managedView({ enabled: true }))
        .mockResolvedValueOnce(managedView({ enabled: true }));
      const post = vi
        .fn()
        .mockResolvedValueOnce(savedReply('request-auto-hide'))
        .mockRejectedValueOnce(new PublicApiError('http', 500, undefined, 'write_failed'));
      await mount(client(get, post));

      await act(async () => {
        switchInput().props.onChange({ target: { checked: true } });
      });

      const successNotice = renderer!.root.findByProps({ 'data-testid': 'plugin-runtime-notice' });
      expect(successNotice.props['data-tone']).toBe('success');
      expect(text(successNotice)).toBe(i18n.t('plugin_management.runtime_saved_effect_unknown'));

      await act(async () => {
        vi.advanceTimersByTime(4_000);
      });
      expect(noticeText()).toBeUndefined();

      await act(async () => {
        switchInput().props.onChange({ target: { checked: false } });
      });
      const errorNotice = renderer!.root.findByProps({ 'data-testid': 'plugin-runtime-notice' });
      expect(errorNotice.props['data-tone']).toBe('error');
      await act(async () => {
        vi.advanceTimersByTime(10_000);
      });
      expect(noticeText()).toBe(i18n.t('plugin_management.runtime_save_failed'));
    } finally {
      vi.useRealTimers();
    }
  });

  it('invokes onApplied immediately after persistence even if unmounted before read-back settles', async () => {
    let resolveReload!: (value: ReturnType<typeof managedView>) => void;
    const pendingReload = new Promise<ReturnType<typeof managedView>>((resolve) => {
      resolveReload = resolve;
    });
    const onApplied = vi.fn();
    const get = vi.fn().mockResolvedValueOnce(managedView()).mockReturnValueOnce(pendingReload);
    const post = vi.fn().mockResolvedValueOnce(savedReply('request-unmount-before-load'));
    await mount(client(get, post), onApplied);

    await act(async () => {
      switchInput().props.onChange({ target: { checked: true } });
      await Promise.resolve();
    });

    expect(onApplied).toHaveBeenCalledTimes(1);
    expect(get).toHaveBeenCalledTimes(2);

    await act(async () => {
      renderer?.unmount();
      renderer = undefined;
      resolveReload(managedView({ enabled: true }));
      await Promise.resolve();
    });
  });

  it('shows warning superseded notice instead of claiming automatic apply when operation is superseded', async () => {
    const get = vi.fn().mockResolvedValue(managedView({ enabled: false }));
    const post = vi.fn().mockResolvedValueOnce({
      operation: {
        requestId: 'request-superseded',
        state: 'persisted',
        code: 'superseded',
      },
      effective: 'unknown',
    });
    await mount(client(get, post));

    await act(async () => {
      switchInput().props.onChange({ target: { checked: true } });
    });

    const notice = renderer!.root.findByProps({ 'data-testid': 'plugin-runtime-notice' });
    expect(notice.props['data-tone']).toBe('warning');
    expect(text(notice)).toBe(i18n.t('plugin_management.runtime_superseded'));
  });
});
