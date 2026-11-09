import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { PublicApiError, type PublicApiClient } from '@/fullweb/publicApi';
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

  beforeEach(() => {
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    vi.useFakeTimers();
    vi.stubGlobal('window', {
      setInterval,
      clearInterval,
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
      '固定版本（镜像自带）',
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
      '固定版本（运行时获取）',
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
      '尚未准备',
    ],
  ])('renders engine version source for %s artifact state', async (_label, artifactPayload, expectedText) => {
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
    expect(valueNode.props.children).toBe(expectedText);
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
