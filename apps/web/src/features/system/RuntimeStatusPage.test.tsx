import i18n from '@/i18n';
import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useNotificationStore } from '@/stores';
import type { PublicApiClient } from '@/fullweb/publicApi';
import { RuntimeStatusPage, type RuntimeStatusResponse } from './RuntimeStatusPage';

const reactGlobal = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
reactGlobal.IS_REACT_ACT_ENVIRONMENT = true;

type RuntimeClient = PublicApiClient;

const embeddedStatus: RuntimeStatusResponse = {
  mode: 'embedded',
  status: 'ready',
  observedAt: '2026-10-27T00:00:00.000Z',
  manager: { state: 'ready' },
  gateway: {
    identity: 'runtime-test',
    generation: 7,
    protocolVersion: 'v1',
    state: 'ready',
    ready: true,
    capabilities: ['start', 'stop', 'restart'],
  },
};

const externalStatus: RuntimeStatusResponse = {
  mode: 'external',
  status: 'ready',
  observedAt: '2026-10-27T00:00:00.000Z',
  manager: { state: 'ready' },
  gateway: { state: 'ready', ready: true, version: '8.0.55' },
};

let renderer: ReactTestRenderer | undefined;

const text = (node: ReactTestInstance): string =>
  node.children.map((child) => (typeof child === 'string' ? child : text(child))).join('');

const buttons = () => renderer!.root.findAll((node) => node.type === 'button');

const renderPage = async (client: RuntimeClient) => {
  await act(async () => {
    renderer = create(
      <MemoryRouter>
        <RuntimeStatusPage client={client} credential="admin-test" />
      </MemoryRouter>
    );
  });
};

afterEach(async () => {
  if (renderer)
    await act(async () => {
      renderer?.unmount();
    });
  renderer = undefined;
  useNotificationStore.getState().hideConfirmation();
  vi.unstubAllGlobals();
});

beforeEach(async () => {
  // The i18n default language is derived from the host locale, so the copy these
  // tests assert on must be pinned instead of depending on the machine running them.
  await i18n.changeLanguage('zh-CN');
  vi.stubGlobal('window', { setInterval: vi.fn(() => 1), clearInterval: vi.fn() });
  vi.stubGlobal('document', { visibilityState: 'visible' });
});

describe('Runtime status page', () => {
  it('renders External as read-only and shows the observed version', async () => {
    const client = {
      get: vi.fn().mockResolvedValue(externalStatus),
      post: vi.fn(),
    } as unknown as RuntimeClient;
    await renderPage(client);
    const content = text(renderer!.root);
    expect(content).toContain('外部 CPA 连接');
    expect(content).toContain('8.0.55');
    expect(buttons().map(text)).not.toContain('启动');
    expect(buttons().map(text)).not.toContain('停止');
    expect(buttons().map(text)).not.toContain('重启');
  });

  it('confirms stop and blocks duplicate submission while the request is in flight', async () => {
    let resolvePost: (value: unknown) => void = () => undefined;
    const pendingPost = new Promise((resolve) => {
      resolvePost = resolve;
    });
    const post = vi.fn().mockReturnValue(pendingPost);
    const client = {
      get: vi.fn().mockResolvedValue(embeddedStatus),
      post,
    } as unknown as RuntimeClient;
    await renderPage(client);
    const stop = buttons().find((item) => text(item) === '停止');
    expect(stop).toBeDefined();
    act(() => stop!.props.onClick());
    const confirmation = useNotificationStore.getState().confirmation;
    expect(confirmation.isOpen).toBe(true);
    expect(confirmation.options?.message).toContain('中断');

    let submitted: Promise<void> | undefined;
    await act(async () => {
      submitted = Promise.resolve(confirmation.options?.onConfirm?.());
      await Promise.resolve();
    });
    expect(post).toHaveBeenCalledTimes(1);
    expect(buttons().find((item) => text(item) === '停止')?.props.disabled).toBe(true);
    act(() => buttons().find((item) => text(item) === '停止')?.props.onClick());
    expect(post).toHaveBeenCalledTimes(1);
    resolvePost({ requestId: 'request-1', action: 'stop', operationId: 'runtime-ui/v1:request-1', state: 'succeeded' });
    await act(async () => {
      await submitted;
    });
    expect(buttons().find((item) => text(item) === '停止')?.props.disabled).toBe(false);
  });

  it('queries the original operation after an unknown result without resending it', async () => {
    const unknown = {
      requestId: 'request-unknown',
      action: 'start',
      operationId: 'runtime-ui/v1:request-unknown',
      state: 'unknown',
    };
    const post = vi.fn().mockResolvedValue(unknown);
    const client = {
      get: vi.fn()
        .mockResolvedValueOnce({ ...embeddedStatus, gateway: { ...embeddedStatus.gateway, state: 'offline', ready: false } })
        .mockResolvedValueOnce(unknown),
      post,
    } as unknown as RuntimeClient;
    await renderPage(client);
    const start = buttons().find((item) => text(item) === '启动');
    expect(start).toBeDefined();
    await act(async () => {
      await start!.props.onClick();
    });
    const requestId = post.mock.calls[0][1].requestId as string;
    expect(client.get).toHaveBeenCalledWith(`runtime/operations/${requestId}`, {
      kind: 'admin',
      value: 'admin-test',
    });
  });
});
