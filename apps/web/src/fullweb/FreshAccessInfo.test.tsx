import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setFullWebLanguage } from '../i18n/fullweb';
import { accessInfoText } from '../i18n/freshAccessInfo';
import {
  AccessInfoSummary,
  formatAccessEndpointUrl,
  parseAccessInfo,
  type AccessInfo,
} from './FreshAccessInfo';
import { createPublicApiClient, type FullWebCredential } from './publicApi';

const context = { basePath: '/admin-cpamp', apiBase: '/admin-cpamp/api/v2', revision: 1 };

const managedInfo: AccessInfo = {
  mode: 'new_managed',
  managementBasePath: '/admin-cpamp',
  inferenceBasePath: '/my-api',
  protocols: [
    {
      id: 'openai',
      label: 'OpenAI',
      basePath: '/my-api/v1',
      examplePath: '/my-api/v1/chat/completions',
    },
    {
      id: 'anthropic',
      label: 'Anthropic',
      basePath: '/my-api',
      examplePath: '/my-api/v1/messages',
    },
    {
      id: 'gemini',
      label: 'Gemini',
      basePath: '/my-api/v1beta',
      examplePath: '/my-api/v1beta/models',
    },
  ],
  ready: true,
};

const externalInfo: AccessInfo = {
  mode: 'connect_external',
  managementBasePath: '/admin-cpamp',
  inferenceBasePath: '',
  externalBaseUrl: 'https://cpa.remote.example/gateway',
  protocols: [
    {
      id: 'openai',
      label: 'OpenAI',
      basePath: 'https://cpa.remote.example/gateway/v1',
      examplePath: 'https://cpa.remote.example/gateway/v1/chat/completions',
    },
  ],
  ready: true,
};

describe('FreshAccessInfo', () => {
  let renderer: ReactTestRenderer;

  beforeEach(() => {
    Reflect.set(globalThis, 'IS_REACT_ACT_ENVIRONMENT', true);
    vi.stubGlobal('window', { location: new URL('https://manager.example/admin-cpamp/api-keys') });
    setFullWebLanguage('en');
  });

  afterEach(async () => {
    if (renderer) await act(async () => renderer.unmount());
    setFullWebLanguage('en');
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  const byID = (id: string) => renderer.root.findByProps({ 'data-testid': id });
  const hasID = (id: string) => renderer.root.findAllByProps({ 'data-testid': id }).length > 0;

  async function mount(
    fetchMock: ReturnType<typeof vi.fn<typeof fetch>>,
    auth: FullWebCredential = { kind: 'admin', value: 'secret-admin' },
    extra: Partial<Parameters<typeof AccessInfoSummary>[0]> = {}
  ) {
    await act(async () => {
      renderer = create(
        <AccessInfoSummary
          client={createPublicApiClient(() => context, fetchMock)}
          auth={auth}
          {...extra}
        />
      );
    });
  }

  it('validates managed prefixes, control isolation, and external URLs strictly', () => {
    expect(parseAccessInfo(managedInfo)).toEqual(managedInfo);
    expect(parseAccessInfo(externalInfo)).toEqual(externalInfo);

    // Rejects protocol endpoints outside declared inferenceBasePath (e.g. falling back to management prefix)
    expect(
      parseAccessInfo({
        ...managedInfo,
        protocols: [
          {
            id: 'openai',
            label: 'OpenAI',
            basePath: '/admin-cpamp/v1',
            examplePath: '/admin-cpamp/v1/chat/completions',
          },
        ],
      })
    ).toBeNull();

    // Rejects traversal, encoding, query/hash, protocol-relative paths
    for (const badPath of ['//evil.example/v1', '/my-api/../v1', '/my-api/%2f', '/my-api?x=1', '/']) {
      expect(
        parseAccessInfo({
          ...managedInfo,
          inferenceBasePath: badPath,
          protocols: [],
        })
      ).toBeNull();
    }

    // Rejects external endpoints outside externalBaseUrl or containing credentials
    expect(
      parseAccessInfo({
        ...externalInfo,
        protocols: [
          {
            id: 'openai',
            label: 'OpenAI',
            basePath: 'https://other.example/gateway/v1',
            examplePath: 'https://other.example/gateway/v1/chat/completions',
          },
        ],
      })
    ).toBeNull();
    expect(
      parseAccessInfo({
        ...externalInfo,
        externalBaseUrl: 'https://user:pass@cpa.remote.example/gateway',
        protocols: [],
      })
    ).toBeNull();

    // Never double-concatenates External URLs
    expect(
      formatAccessEndpointUrl(
        'https://cpa.remote.example/gateway/v1',
        'connect_external',
        'https://manager.example'
      )
    ).toBe('https://cpa.remote.example/gateway/v1');
  });

  it('does not fetch when auth.value is empty and skips GET when info prop is provided', async () => {
    const fetchMock = vi.fn<typeof fetch>();
    await mount(fetchMock, { kind: 'session', value: '' });
    expect(fetchMock).not.toHaveBeenCalled();
    expect(hasID('access-info-idle')).toBe(true);

    await act(async () => renderer.unmount());
    await mount(fetchMock, { kind: 'admin', value: '' }, { info: managedInfo });
    expect(fetchMock).not.toHaveBeenCalled();
    expect(byID('access-info-inference').props.children).toBe('https://manager.example/my-api');
    expect(byID('access-info-protocol-base-anthropic').props.children).toBe(
      'https://manager.example/my-api'
    );
    expect(byID('access-info-protocol-base-gemini').props.children).toBe(
      'https://manager.example/my-api/v1beta'
    );
  });

  it('fetches access-info with auth, discards stale owner responses, and calls onUnauthorized', async () => {
    let resolveFirst!: (value: Response) => void;
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockImplementationOnce(
        () =>
          new Promise<Response>((resolve) => {
            resolveFirst = resolve;
          })
      )
      .mockResolvedValueOnce(Response.json(externalInfo))
      .mockResolvedValueOnce(new Response('', { status: 401 }));

    const onUnauthorized = vi.fn();
    await mount(fetchMock, { kind: 'session', value: 'token-1' }, { onUnauthorized });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0][0]).toBe(
      'https://manager.example/admin-cpamp/api/v2/api-keys/access-info'
    );

    // Owner changes before first request completes -> stale first response is ignored
    await act(async () => {
      renderer.update(
        <AccessInfoSummary
          client={createPublicApiClient(() => context, fetchMock)}
          auth={{ kind: 'session', value: 'token-2' }}
          onUnauthorized={onUnauthorized}
        />
      );
    });
    expect(byID('access-info-external').props.children).toBe(
      'https://cpa.remote.example/gateway'
    );

    await act(async () => {
      resolveFirst(Response.json(managedInfo));
    });
    expect(hasID('access-info-external')).toBe(true);
    expect(hasID('access-info-inference')).toBe(false);

    // 401 calls onUnauthorized cleanly
    await act(async () => {
      await byID('access-info-retry').props.onClick();
    });
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
  });

  it('shows shared prefix warning, pending restart distinction, not-ready honesty, and copy feedback', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    const fetchMock = vi.fn<typeof fetch>();

    // Shared prefix
    await mount(fetchMock, { kind: 'admin', value: 'k' }, {
      info: {
        ...managedInfo,
        managementBasePath: '/shared',
        inferenceBasePath: '/shared',
        protocols: [
          {
            id: 'openai',
            label: 'OpenAI',
            basePath: '/shared/v1',
            examplePath: '/shared/v1/chat/completions',
          },
        ],
      },
    });
    expect(hasID('access-info-shared-badge')).toBe(true);
    expect(hasID('access-info-shared-warning')).toBe(true);

    await act(async () => {
      await byID('access-info-copy-openai').props.onClick();
    });
    expect(writeText).toHaveBeenCalledWith('https://manager.example/shared/v1');
    expect(byID('access-info-copy-feedback').props.children).toBe(accessInfoText('copied', 'en'));

    // Pre-restart Upgrade projection
    await act(async () => renderer.unmount());
    await mount(
      fetchMock,
      { kind: 'admin', value: '' },
      {
        info: {
          mode: 'migrate_legacy',
          managementBasePath: '/next-admin',
          inferenceBasePath: '/next-api',
          protocols: [
            {
              id: 'openai',
              label: 'OpenAI',
              basePath: '/next-api/v1',
              examplePath: '/next-api/v1/chat/completions',
            },
          ],
          ready: false,
          code: 'upgrade_restart_required',
        },
        pendingRestart: true,
      }
    );
    expect(byID('access-info-status').props.children).toBe(
      accessInfoText('statusPendingRestart', 'en')
    );
    expect(hasID('access-info-not-ready')).toBe(true);

    // Not ready with no protocols never fabricates /v1
    await act(async () => renderer.unmount());
    await mount(
      fetchMock,
      { kind: 'admin', value: '' },
      {
        info: {
          mode: 'adopt_existing',
          managementBasePath: '/admin-cpamp',
          inferenceBasePath: '',
          protocols: [],
          ready: false,
          code: 'inference_prefix_required',
        },
      }
    );
    expect(hasID('access-info-protocols-empty')).toBe(true);
    expect(hasID('access-info-protocols')).toBe(false);
  });

  it('supports default-collapsed protocol disclosure while keeping protocol endpoints mounted', async () => {
    const fetchMock = vi.fn<typeof fetch>();
    await mount(
      fetchMock,
      { kind: 'admin', value: '' },
      {
        info: managedInfo,
        defaultProtocolsCollapsed: true,
        subtitleNote: 'Bookmark the console URL.',
      }
    );
    const disclosure = byID('access-info-protocols-disclosure');
    expect(disclosure.type).toBe('details');
    expect(disclosure.props.open).toBeUndefined();
    expect(byID('access-info-protocol-base-openai').props.children).toBe(
      'https://manager.example/my-api/v1'
    );
    expect(JSON.stringify(renderer.toJSON())).toContain('Bookmark the console URL.');
  });

});
