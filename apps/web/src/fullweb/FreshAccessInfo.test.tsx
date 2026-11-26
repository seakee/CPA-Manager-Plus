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

  it('renders client-only inference view hiding management URL, mode badge, and clientKeyNote', async () => {
    const fetchMock = vi.fn<typeof fetch>();
    await mount(
      fetchMock,
      { kind: 'admin', value: 'k' },
      {
        info: managedInfo,
        clientOnly: true,
        compact: true,
        defaultProtocolsCollapsed: false,
      }
    );
    expect(hasID('access-info-management')).toBe(false);
    expect(hasID('access-info-mode')).toBe(false);
    expect(byID('access-info-status').props.children).toBe(accessInfoText('statusReady', 'en'));
    expect(byID('access-info-inference').props.children).toBe('https://manager.example/my-api');
    expect(byID('access-info-protocol-base-openai').props.children).toBe(
      'https://manager.example/my-api/v1'
    );
    const renderedText = JSON.stringify(renderer.toJSON());
    expect(renderedText).toContain(accessInfoText('clientTitle', 'en'));
    expect(renderedText).toContain(accessInfoText('clientSubtitle', 'en'));
    expect(renderedText).not.toContain(accessInfoText('clientKeyNote', 'en'));
    expect(renderedText).not.toContain('https://manager.example/my-admin');
  });

  it('switches protocol cards via compact tabs in clientOnly mode while keeping full list when clientOnly is false', async () => {
    const fetchMock = vi.fn<typeof fetch>();
    await mount(
      fetchMock,
      { kind: 'admin', value: 'k' },
      {
        info: managedInfo,
        clientOnly: true,
        compact: true,
      }
    );
    expect(byID('access-info-protocol-tab-openai').props['aria-selected']).toBe(true);
    expect(byID('access-info-protocol-tab-anthropic').props['aria-selected']).toBe(false);
    expect(hasID('access-info-protocol-openai')).toBe(true);
    expect(hasID('access-info-protocol-anthropic')).toBe(false);
    expect(hasID('access-info-protocol-gemini')).toBe(false);
    expect(byID('access-info-summary').props.className).toContain('is-client-access-card');
    expect(
      byID('access-info-protocol-openai').findAllByProps({ className: 'access-protocol-head' })
    ).toHaveLength(0);
    expect(
      byID('access-info-protocols').props.className
    ).toBe('key-access-protocol-panel');

    await act(async () => {
      byID('access-info-protocol-tab-anthropic').props.onClick();
    });
    expect(byID('access-info-protocol-tab-openai').props['aria-selected']).toBe(false);
    expect(byID('access-info-protocol-tab-anthropic').props['aria-selected']).toBe(true);
    expect(hasID('access-info-protocol-openai')).toBe(false);
    expect(hasID('access-info-protocol-anthropic')).toBe(true);
    expect(byID('access-info-protocol-base-anthropic').props.children).toBe(
      'https://manager.example/my-api'
    );
    expect(byID('access-info-protocol-example-anthropic').props.children).toBe(
      'https://manager.example/my-api/v1/messages'
    );

    // Single-protocol external mode omits the tab strip.
    await act(async () => renderer.unmount());
    await mount(
      fetchMock,
      { kind: 'admin', value: 'k' },
      {
        info: externalInfo,
        clientOnly: true,
        compact: true,
      }
    );
    expect(hasID('access-info-protocol-tab-openai')).toBe(false);
    expect(hasID('access-info-protocol-openai')).toBe(true);

    // Non-clientOnly renders all protocols at once with no protocol switcher tabs.
    await act(async () => renderer.unmount());
    await mount(
      fetchMock,
      { kind: 'admin', value: 'k' },
      {
        info: managedInfo,
        clientOnly: false,
      }
    );
    expect(hasID('access-info-protocol-tab-openai')).toBe(false);
    expect(hasID('access-info-protocol-openai')).toBe(true);
    expect(hasID('access-info-protocol-anthropic')).toBe(true);
    expect(hasID('access-info-protocol-gemini')).toBe(true);
  });

  it('copies completion recovery details without keys and exposes textarea fallback on clipboard failure', async () => {
    let rejectNext = true;
    const writeText = vi.fn(async () => {
      if (rejectNext) throw new Error('clipboard denied');
    });
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    const fetchMock = vi.fn<typeof fetch>();

    await mount(fetchMock, { kind: 'admin', value: 'secret-admin-key' }, {
      info: managedInfo,
      completionRecovery: true,
    });

    expect(JSON.stringify(renderer.toJSON())).toContain(
      'docker exec -u 10002:10002 <container> cpa-manager-plus access show'
    );
    await act(async () => {
      await byID('access-info-copy-access').props.onClick();
    });
    const fallback = byID('access-info-copy-access-fallback');
    const expectedValue = String(fallback.props.value);
    expect(fallback.type).toBe('textarea');
    expect(fallback.props.className).toBe('form-input form-input-mono');
    expect(expectedValue).toContain('https://manager.example/admin-cpamp');
    expect(fallback.props.value).toContain('https://manager.example/admin-cpamp/api/v2');
    expect(fallback.props.value).toContain('https://manager.example/my-api');
    expect(fallback.props.value).not.toContain('secret-admin-key');
    expect(fallback.props.value).not.toContain('key');
    expect(byID('access-info-copy-feedback').props.children).toBe(
      accessInfoText('copyFailed', 'en')
    );

    rejectNext = false;
    await act(async () => {
      await byID('access-info-copy-access').props.onClick();
    });
    expect(hasID('access-info-copy-access-fallback')).toBe(false);
    expect(writeText).toHaveBeenLastCalledWith(expectedValue);
    expect(byID('access-info-copy-feedback').props.children).toBe(
      accessInfoText('copied', 'en')
    );

    // External completion recovery keeps external base and omits inference prefix and keys.
    await act(async () => renderer.unmount());
    rejectNext = true;
    await mount(fetchMock, { kind: 'admin', value: 'secret-admin-key' }, {
      info: {
        mode: 'connect_external',
        managementBasePath: '/ext-ctl',
        inferenceBasePath: '',
        externalBaseUrl: 'https://cpa.remote.example/gateway',
        protocols: [],
        ready: true,
      },
      completionRecovery: true,
    });
    await act(async () => {
      await byID('access-info-copy-access').props.onClick();
    });
    const externalFallback = byID('access-info-copy-access-fallback');
    expect(externalFallback.props.value).toContain('https://manager.example/ext-ctl');
    expect(externalFallback.props.value).toContain('https://cpa.remote.example/gateway');
    expect(externalFallback.props.value).not.toContain('/my-api');
    expect(externalFallback.props.value).not.toContain('secret-admin-key');
  });

  it('renders dedicated localized titles for upgrade_authority_unavailable and upgrade_operation_invalid', async () => {
    const fetchMock = vi.fn<typeof fetch>();
    await mount(fetchMock, { kind: 'admin', value: '' }, {
      info: {
        mode: 'migrate_legacy',
        managementBasePath: '/ctl',
        inferenceBasePath: '/inf',
        protocols: [],
        ready: false,
        code: 'upgrade_authority_unavailable',
      },
    });
    expect(
      byID('access-info-not-ready').findByProps({ className: 'alert-title' }).children.join('')
    ).toBe(accessInfoText('notReadyUpgradeAuthorityTitle', 'en'));

    await act(async () => renderer.unmount());
    await mount(fetchMock, { kind: 'admin', value: '' }, {
      info: {
        mode: 'migrate_legacy',
        managementBasePath: '/ctl',
        inferenceBasePath: '/inf',
        protocols: [],
        ready: false,
        code: 'upgrade_operation_invalid',
      },
    });
    expect(
      byID('access-info-not-ready').findByProps({ className: 'alert-title' }).children.join('')
    ).toBe(accessInfoText('notReadyUpgradeOperationInvalidTitle', 'en'));
  });
  it('handles onCopyAuthHeader outcomes, ignores stale copy completions across reloads, and respects authHeaderCopyDisabled', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    const fetchMock = vi.fn<typeof fetch>();

    let resolvePending!: (outcome: 'copied' | 'failed' | 'ignored') => void;
    const onCopyAuthHeader = vi
      .fn<(prefix: string) => Promise<'copied' | 'failed' | 'ignored'>>()
      .mockResolvedValueOnce('ignored')
      .mockResolvedValueOnce('failed')
      .mockReturnValueOnce(
        new Promise((resolve) => {
          resolvePending = resolve;
        })
      );

    await mount(
      fetchMock,
      { kind: 'admin', value: 'k1' },
      {
        info: managedInfo,
        clientOnly: true,
        drawerEmbed: true,
        maskedKeyPreview: 'cpamp_sk_…1234',
        onCopyAuthHeader,
      }
    );

    // 1. 'ignored' must NOT display red copyFailed feedback
    await act(async () => {
      await byID('access-info-copy-header-openai').props.onClick();
    });
    expect(hasID('access-info-copy-feedback')).toBe(false);

    // 2. 'failed' displays copyFailed without falling back to template header
    await act(async () => {
      await byID('access-info-copy-header-openai').props.onClick();
    });
    expect(byID('access-info-copy-feedback').props.children).toBe(
      accessInfoText('copyFailed', 'en')
    );
    expect(writeText).not.toHaveBeenCalled();

    // 3. Stale async completion after reloadToken change is discarded
    act(() => {
      void byID('access-info-copy-header-openai').props.onClick();
    });
    await act(async () => {
      renderer.update(
        <AccessInfoSummary
          client={createPublicApiClient(() => context, fetchMock)}
          auth={{ kind: 'admin', value: 'k1' }}
          info={managedInfo}
          reloadToken={1}
          clientOnly
          drawerEmbed
          maskedKeyPreview="cpamp_sk_…1234"
          onCopyAuthHeader={onCopyAuthHeader}
        />
      );
    });
    await act(async () => {
      resolvePending('copied');
      await Promise.resolve();
    });
    expect(hasID('access-info-copy-feedback')).toBe(false);

    // 4. authHeaderCopyDisabled blocks onCopyAuthHeader cleanly without copyFailed
    await act(async () => {
      renderer.update(
        <AccessInfoSummary
          client={createPublicApiClient(() => context, fetchMock)}
          auth={{ kind: 'admin', value: 'k1' }}
          info={managedInfo}
          reloadToken={1}
          clientOnly
          drawerEmbed
          maskedKeyPreview="cpamp_sk_…1234"
          onCopyAuthHeader={onCopyAuthHeader}
          authHeaderCopyDisabled
        />
      );
    });
    const callsBeforeDisabled = onCopyAuthHeader.mock.calls.length;
    await act(async () => {
      await byID('access-info-copy-header-openai').props.onClick();
    });
    expect(onCopyAuthHeader).toHaveBeenCalledTimes(callsBeforeDisabled);
    expect(hasID('access-info-copy-feedback')).toBe(false);
  });


});
