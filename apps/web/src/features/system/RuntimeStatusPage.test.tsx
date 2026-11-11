import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { PublicApiError, type PublicApiClient } from '@/fullweb/publicApi';
import i18n from '@/i18n';
import { RuntimeStatusPage } from './RuntimeStatusPage';

const DIGEST = 'sha256:' + 'd'.repeat(64);

function embeddedStatus() {
  return {
    mode: 'embedded' as const,
    status: 'healthy',
    observedAt: '2026-11-09T00:00:00Z',
    manager: { state: 'ready' },
    gateway: {
      state: 'online',
      ready: true,
      artifactId: DIGEST,
      capabilities: ['restart', 'stop'],
    },
  };
}

describe('RuntimeStatusPage artifact origin display', () => {
  let renderer: ReactTestRenderer | undefined;

  beforeEach(async () => {
    await i18n.changeLanguage('zh-CN');
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    vi.useFakeTimers();
    vi.stubGlobal('window', {
      setInterval,
      clearInterval,
      location: { hash: '', search: '' },
    });
    vi.stubGlobal('document', {
      visibilityState: 'visible',
    });
  });

  afterEach(async () => {
    if (renderer) {
      await act(async () => renderer!.unmount());
      renderer = undefined;
    }
    // Unmounting cancels the poll, but a refresh that is already awaiting its
    // reads still settles its state later; flush it before the stubbed globals
    // (window, document) go away, so it cannot run against a bare test context.
    await act(async () => {});
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  async function mount(client: PublicApiClient) {
    await act(async () => {
      renderer = create(
        <MemoryRouter>
          <RuntimeStatusPage client={client} credential="admin-secret" />
        </MemoryRouter>
      );
    });
  }

  it.each([
    [
      'bundled',
      {
        schemaVersion: 1,
        pinned: { version: '8.0.55', artifactId: DIGEST },
        active: { artifactId: DIGEST, origin: 'bundled' },
        required: false,
        operation: null,
        cdnPresets: [],
      },
      'runtime_status.engine_source_bundled',
    ],
    [
      'acquired',
      {
        schemaVersion: 1,
        pinned: { version: '8.0.55', artifactId: DIGEST },
        active: { artifactId: DIGEST, origin: 'acquired' },
        required: false,
        operation: null,
        cdnPresets: [],
      },
      'runtime_status.engine_source_acquired',
    ],
    [
      'required',
      {
        schemaVersion: 1,
        pinned: { version: '8.0.55', artifactId: DIGEST },
        active: null,
        required: true,
        operation: null,
        cdnPresets: [],
      },
      'runtime_status.engine_source_required',
    ],
  ])('renders engine version source for %s artifact state', async (_label, artifactPayload, key) => {
    const client: PublicApiClient = {
      get: vi.fn(async (resource: string) => {
        if (resource === 'runtime/status') return embeddedStatus();
        if (resource === 'runtime/artifact') return artifactPayload;
        throw new Error(`unexpected resource ${resource}`);
      }) as PublicApiClient['get'],
      post: vi.fn(),
      put: vi.fn(),
    };

    await mount(client);

    expect(client.get).toHaveBeenCalledWith('runtime/status', {
      kind: 'admin',
      value: 'admin-secret',
    });
    expect(client.get).toHaveBeenCalledWith('runtime/artifact', {
      kind: 'admin',
      value: 'admin-secret',
    });
    const valueNode = renderer!.root.findByProps({
      'data-testid': 'runtime-status-engine-source-value',
    });
    // The value is named by the console catalog, so the test asserts the state is
    // translated rather than pinning one wording of one language.
    expect(valueNode.props.children).toBe(i18n.t(key, { lng: 'zh-CN' }));
  });

  it('shows nothing extra on unsupported_platform and does not break the page when runtime/artifact fails', async () => {
    const unsupportedClient: PublicApiClient = {
      get: vi.fn(async (resource: string) => {
        if (resource === 'runtime/status') return embeddedStatus();
        if (resource === 'runtime/artifact') {
          throw new PublicApiError('http', 409, undefined, 'unsupported_platform');
        }
        throw new Error(`unexpected resource ${resource}`);
      }) as PublicApiClient['get'],
      post: vi.fn(),
      put: vi.fn(),
    };

    await mount(unsupportedClient);

    expect(
      renderer!.root.findByProps({ 'data-testid': 'runtime-status-page' })
    ).toBeTruthy();
    expect(
      renderer!.root.findAllByProps({ 'data-testid': 'runtime-status-engine-source' })
    ).toHaveLength(0);

    const failingClient: PublicApiClient = {
      get: vi.fn(async (resource: string) => {
        if (resource === 'runtime/status') return embeddedStatus();
        throw new PublicApiError('network');
      }) as PublicApiClient['get'],
      post: vi.fn(),
      put: vi.fn(),
    };

    await mount(failingClient);

    expect(
      renderer!.root.findByProps({ 'data-testid': 'runtime-status-page' })
    ).toBeTruthy();
    expect(
      renderer!.root.findAllByProps({ 'data-testid': 'runtime-status-engine-source' })
    ).toHaveLength(0);
    expect(JSON.stringify(renderer!.toJSON())).toContain('healthy');
  });
});

describe('RuntimeStatusPage state wording', () => {
  let renderer: ReactTestRenderer | undefined;

  beforeEach(async () => {
    await i18n.changeLanguage('zh-CN');
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    vi.useFakeTimers();
    vi.stubGlobal('window', {
      setInterval,
      clearInterval,
      location: { hash: '', search: '' },
    });
    vi.stubGlobal('document', {
      visibilityState: 'visible',
    });
  });

  afterEach(async () => {
    if (renderer) {
      await act(async () => renderer!.unmount());
      renderer = undefined;
    }
    // Unmounting cancels the poll, but a refresh that is already awaiting its
    // reads still settles its state later; flush it before the stubbed globals
    // (window, document) go away, so it cannot run against a bare test context.
    await act(async () => {});
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  async function mount(client: PublicApiClient) {
    await act(async () => {
      renderer = create(
        <MemoryRouter>
          <RuntimeStatusPage client={client} credential="admin-secret" />
        </MemoryRouter>
      );
    });
  }

  function stateNode(testID: string) {
    return renderer!.root.findByProps({ 'data-testid': testID }).props.children as string;
  }

  function statusClient(status: unknown, operationState = 'running'): PublicApiClient {
    return {
      get: vi.fn(async (resource: string) => {
        if (resource === 'runtime/status') return status;
        if (resource === 'runtime/artifact') throw new PublicApiError('network');
        throw new Error(`unexpected resource ${resource}`);
      }) as PublicApiClient['get'],
      post: vi.fn(async () => ({ action: 'start', state: operationState })),
      put: vi.fn(),
    } as unknown as PublicApiClient;
  }

  // The text of an operation line, which is a sentence plus an optional trailing
  // note, so the strings it renders are read out of the node.
  function operationText() {
    const children = renderer!.root.findByProps({
      'data-testid': 'runtime-status-operation',
    }).props.children as unknown;
    return (Array.isArray(children) ? children : [children])
      .filter((child): child is string => typeof child === 'string')
      .join('');
  }

  // This language's own entry, without the zh-CN fallback: a missing key comes
  // back as the key itself, so a state that only appears translated because the
  // default language filled the gap cannot pass.
  function ownText(language: string, key: string, vars?: Record<string, string>) {
    return i18n.t(key, { lng: language, fallbackLng: false, ...vars });
  }

  // One observation per state, mounted fresh so each one is rendered by the page
  // exactly as it renders a real response.
  async function renderObservation(
    status: string,
    gateway: Record<string, unknown> = { state: status, ready: status === 'ready' }
  ) {
    if (renderer) {
      await act(async () => renderer!.unmount());
      renderer = undefined;
    }
    await mount(
      statusClient({
        mode: 'embedded',
        status,
        observedAt: '2026-11-09T00:00:00Z',
        manager: { state: status },
        gateway,
      })
    );
  }

  it('names the Manager and gateway states in the console language, never the wire value', async () => {
    await mount(
      statusClient({
        mode: 'embedded',
        status: 'ready',
        observedAt: '2026-11-09T00:00:00Z',
        manager: { state: 'ready' },
        gateway: { state: 'offline', ready: false },
      })
    );

    expect(stateNode('runtime-status-state')).toBe(
      i18n.t('runtime_status.state_ready', { lng: 'zh-CN' })
    );
    expect(stateNode('runtime-status-manager-state')).toBe(
      i18n.t('runtime_status.state_ready', { lng: 'zh-CN' })
    );
    expect(stateNode('runtime-status-gateway-state')).toBe(
      i18n.t('runtime_status.state_offline', { lng: 'zh-CN' })
    );
    expect(stateNode('runtime-status-gateway-state')).not.toBe('offline');

    await act(async () => {
      await i18n.changeLanguage('en');
    });
    expect(stateNode('runtime-status-state')).toBe(
      i18n.t('runtime_status.state_ready', { lng: 'en' })
    );
    expect(stateNode('runtime-status-gateway-state')).toBe(
      i18n.t('runtime_status.state_offline', { lng: 'en' })
    );

    await act(async () => {
      await i18n.changeLanguage('ru');
    });
    expect(stateNode('runtime-status-gateway-state')).toBe(
      i18n.t('runtime_status.state_offline', { lng: 'ru' })
    );
  });

  it('names a running operation in the console language', async () => {
    // The Manager reports `running` while an operation is in flight; an unnamed
    // state would print the wire word in every language.
    const mounted = statusClient({
      mode: 'embedded',
      status: 'ready',
      manager: { state: 'ready' },
      gateway: { state: 'offline', ready: false, capabilities: ['start', 'stop'] },
    });
    await mount(mounted);
    await act(async () => {
      await renderer!.root
        .findByProps({ 'data-testid': 'runtime-status-start' })
        .props.onClick();
    });

    expect(operationText()).toContain(
      i18n.t('runtime_status.operation_running', { lng: 'zh-CN' })
    );
    expect(operationText()).not.toMatch(/\brunning\b/);
  });

  it('serves the demo fixture instead of the unavailable observation on the demo site', async () => {
    (window as unknown as { location: { hash: string } }).location.hash = '#/demo/system/runtime';
    const client = { get: vi.fn(), post: vi.fn(), put: vi.fn() };

    await mount(client as unknown as PublicApiClient);

    // Nothing is requested: the demo has no Manager to answer.
    expect(client.get).not.toHaveBeenCalled();
    expect(stateNode('runtime-status-state')).toBe(
      i18n.t('runtime_status.state_ready', { lng: 'zh-CN' })
    );
    expect(stateNode('runtime-status-gateway-state')).toBe(
      i18n.t('runtime_status.state_ready', { lng: 'zh-CN' })
    );
    // The demo fixture is rendered like any other observation: the engine
    // origin names itself through the console catalog, not as a literal.
    expect(
      renderer!.root.findByProps({ 'data-testid': 'runtime-status-engine-source-value' }).props
        .children
    ).toBe(i18n.t('runtime_status.engine_source_bundled', { lng: 'zh-CN' }));
  });

  it.each(['zh-CN', 'zh-TW', 'en', 'ru'])(
    'renders every Manager, gateway and operation state in %s',
    async (language) => {
      await act(async () => {
        await i18n.changeLanguage(language);
      });

      // The Manager's closed state set, rendered one observation at a time. The
      // expected sentence is looked up in this language without the zh-CN
      // fallback, so an entry this language does not carry renders as the key
      // instead of the default language's sentence and fails here.
      for (const state of ['ready', 'offline', 'starting', 'unknown'] as const) {
        await renderObservation(state);
        const key = `runtime_status.state_${state}`;
        const expected = ownText(language, key);
        expect(expected, key).not.toBe(key);
        for (const testID of [
          'runtime-status-state',
          'runtime-status-manager-state',
          'runtime-status-gateway-state',
        ]) {
          expect(stateNode(testID), `${testID}/${language}`).toBe(expected);
          // The wire word is never what the page shows an operator.
          expect(stateNode(testID), `${testID}/${language}`).not.toBe(state);
        }
      }

      // The operation states a Start leaves on screen, driven through the same
      // button an operator presses rather than through the label helper.
      for (const state of ['accepted', 'running', 'succeeded', 'failed'] as const) {
        if (renderer) {
          await act(async () => renderer!.unmount());
          renderer = undefined;
        }
        await mount(
          statusClient(
            {
              mode: 'embedded',
              status: 'ready',
              observedAt: '2026-11-09T00:00:00Z',
              manager: { state: 'ready' },
              gateway: { state: 'offline', ready: false, capabilities: ['start', 'stop'] },
            },
            state
          )
        );
        await act(async () => {
          await renderer!.root
            .findByProps({ 'data-testid': 'runtime-status-start' })
            .props.onClick();
        });

        const key = `runtime_status.operation_${state}`;
        const expectedState = ownText(language, key);
        expect(expectedState, key).not.toBe(key);
        expect(expectedState, key).not.toBe(state);
        const expected = ownText(language, 'runtime_status.operation_status', {
          action: ownText(language, 'runtime_status.start'),
          state: expectedState,
        });
        // The line is this language's sentence, not a key left unresolved.
        expect(expected, key).not.toContain('runtime_status.');
        expect(operationText(), `${key}/${language}`).toBe(expected);
      }
    }
  );
});
