import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { BOOTSTRAP_UI, createBootstrapTransport } from './bootstrapTransport';
import { FreshBootstrapPage } from './FreshBootstrapPage';
import { setFullWebLanguage } from '../i18n/fullweb';
import { bootstrapJourneyText } from '../i18n/bootstrapJourney';

vi.mock('./UpgradeAuthenticationEntry', () => ({
  UpgradeAuthenticationEntry: () => <div data-testid="upgrade-auth-entry" />,
}));

const identity = {
  admissionId: 'admission-01',
  installationId: 'install-01',
  generation: 'gen-01',
  databasePath: '/data/manager.sqlite',
  operationId: 'op-fresh-01',
};

const pendingOperation = {
  version: 1,
  identity,
  revision: 1,
  adminInitialized: false,
  gatewayPending: true,
  routing: { committed: false },
};

const handoff = {
  identity,
  claimGeneration: 1,
  token: 'claim-secret-012345678901234567890123456789',
  resume: false,
};

function findByTestId(root: ReactTestInstance, testId: string): ReactTestInstance {
  return root.find((node) => node.props?.['data-testid'] === testId);
}

function textOf(node: ReactTestInstance): string {
  return node.children.map((child) => (typeof child === 'string' ? child : textOf(child))).join('');
}

describe('FreshBootstrapPage product preview workflow', () => {
  beforeEach(() => {
    setFullWebLanguage('zh-CN');
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    const location = new URL('https://manager.example' + BOOTSTRAP_UI);
    vi.stubGlobal('window', {
      location,
      history: {
        replaceState: (_: unknown, _title: string, path: string) => {
          location.href = new URL(path, location.origin).href;
        },
      },
    });
    vi.stubGlobal('document', {
      documentElement: {
        setAttribute: vi.fn(),
        classList: {
          toggle: vi.fn(),
        },
      },
    });
    vi.stubGlobal('navigator', {
      clipboard: {
        writeText: vi.fn().mockResolvedValue(undefined),
      },
    });
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Discovery unavailable')));
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('never flashes initialize while source discovery is pending', async () => {
    let resolveDiscovery!: (value: Response) => void;
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(
        () =>
          new Promise<Response>((resolve) => {
            resolveDiscovery = resolve;
          })
      )
    );
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshBootstrapPage handoff={null} transport={createBootstrapTransport(vi.fn())} />
      );
    });
    expect(textOf(findByTestId(renderer.root, 'bootstrap-discovery-unconfirmed'))).toContain(
      '正在读取来源状态'
    );
    expect(renderer.root.findAllByProps({ 'data-testid': 'cli-handoff-command' })).toHaveLength(0);
    expect(renderer.root.findAllByProps({ 'aria-label': 'Fresh Bootstrap 进度' })).toHaveLength(0);
    await act(async () => {
      resolveDiscovery(
        Response.json({
          contract_version: 'cpamp.phase4-01.discovery.v1',
          status: 'recovery_required',
          mode: 'recovery',
          auth_entry: 'host_intervention',
          reason: 'recovery_host_intervention',
          authentication_required: false,
        })
      );
    });
    expect(renderer.root.findAllByProps({ 'data-testid': 'cli-handoff-command' })).toHaveLength(0);
    expect(textOf(findByTestId(renderer.root, 'host-preflight-command'))).toContain(
      'legacy-preflight'
    );
  });

  it('shows initialize only after a canonical Fresh discovery response', async () => {
    let resolveDiscovery!: (value: Response) => void;
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(
        () =>
          new Promise<Response>((resolve) => {
            resolveDiscovery = resolve;
          })
      )
    );
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshBootstrapPage handoff={null} transport={createBootstrapTransport(vi.fn())} />
      );
    });
    expect(renderer.root.findAllByProps({ 'data-testid': 'cli-handoff-command' })).toHaveLength(0);
    await act(async () => {
      resolveDiscovery(
        Response.json({
          contract_version: 'cpamp.phase4-01.discovery.v1',
          status: 'fresh_claim_required',
          mode: 'fresh',
          auth_entry: 'fresh_claim',
          reason: 'fresh_claim_required',
          authentication_required: true,
        })
      );
    });
    expect(textOf(findByTestId(renderer.root, 'cli-handoff-command'))).toBe(
      'cpa-manager-plus initialize'
    );
  });

  it('does not suggest Fresh initialization when discovery fails', async () => {
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshBootstrapPage handoff={null} transport={createBootstrapTransport(vi.fn())} />
      );
    });
    expect(textOf(findByTestId(renderer.root, 'bootstrap-no-handoff'))).toContain(
      '来源状态无法确认'
    );
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'bootstrap-discovery-guidance' })
    ).toHaveLength(0);
    expect(renderer.root.findAllByProps({ 'data-testid': 'cli-handoff-command' })).toHaveLength(0);
    expect(textOf(findByTestId(renderer.root, 'bootstrap-discovery-unconfirmed'))).toContain(
      '来源状态无法确认'
    );
  });

  it('rechecks an unknown source and shows Fresh guidance only after verified discovery', async () => {
    const publicDiscovery = vi
      .fn<typeof fetch>()
      .mockRejectedValueOnce(new TypeError('temporary network error'))
      .mockResolvedValueOnce(
        Response.json({
          contract_version: 'cpamp.phase4-01.discovery.v1',
          status: 'fresh_claim_required',
          mode: 'fresh',
          auth_entry: 'fresh_claim',
          reason: 'fresh_claim_required',
          authentication_required: true,
        })
      );
    const bootstrapTransport = vi.fn();
    vi.stubGlobal('fetch', publicDiscovery);
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshBootstrapPage
          handoff={null}
          transport={createBootstrapTransport(bootstrapTransport)}
        />
      );
    });
    expect(renderer.root.findAllByProps({ 'data-testid': 'cli-handoff-command' })).toHaveLength(0);
    await act(async () =>
      findByTestId(renderer.root, 'bootstrap-recheck-discovery').props.onClick()
    );
    expect(publicDiscovery).toHaveBeenCalledTimes(2);
    expect(publicDiscovery.mock.calls[1][0]).toBe('/.well-known/cpamp-bootstrap');
    expect(publicDiscovery.mock.calls[1][1]).toMatchObject({
      method: 'GET',
      mode: 'same-origin',
      credentials: 'omit',
      redirect: 'error',
      cache: 'no-store',
    });
    expect(textOf(findByTestId(renderer.root, 'cli-handoff-command'))).toBe(
      'cpa-manager-plus initialize'
    );
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'bootstrap-recheck-discovery' })
    ).toHaveLength(0);
    expect(bootstrapTransport).not.toHaveBeenCalled();
  });

  it('rechecks canonical 503 unavailable and remains read-only until Fresh is verified', async () => {
    let resolveRecheck!: (response: Response) => void;
    const publicDiscovery = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        Response.json(
          {
            contract_version: 'cpamp.phase4-01.discovery.v1',
            status: 'unavailable',
            auth_entry: 'unavailable',
            reason: 'observation_unavailable',
            authentication_required: false,
          },
          { status: 503 }
        )
      )
      .mockImplementationOnce(
        () =>
          new Promise<Response>((resolve) => {
            resolveRecheck = resolve;
          })
      );
    const bootstrapTransport = vi.fn();
    vi.stubGlobal('fetch', publicDiscovery);
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshBootstrapPage
          handoff={null}
          transport={createBootstrapTransport(bootstrapTransport)}
        />
      );
    });
    expect(textOf(findByTestId(renderer.root, 'bootstrap-discovery-guidance'))).toContain(
      '来源状态暂无法确认'
    );
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'bootstrap-recheck-discovery' })
    ).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-testid': 'cli-handoff-command' })).toHaveLength(0);
    await act(async () =>
      findByTestId(renderer.root, 'bootstrap-recheck-discovery').props.onClick()
    );
    expect(publicDiscovery).toHaveBeenCalledTimes(2);
    expect(textOf(findByTestId(renderer.root, 'bootstrap-discovery-unconfirmed'))).toContain(
      '正在读取来源状态'
    );
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'bootstrap-recheck-discovery' })
    ).toHaveLength(0);
    expect(renderer.root.findAllByProps({ 'data-testid': 'cli-handoff-command' })).toHaveLength(0);
    expect(bootstrapTransport).not.toHaveBeenCalled();
    await act(async () => {
      resolveRecheck(
        Response.json({
          contract_version: 'cpamp.phase4-01.discovery.v1',
          status: 'fresh_claim_required',
          mode: 'fresh',
          auth_entry: 'fresh_claim',
          reason: 'fresh_claim_required',
          authentication_required: true,
        })
      );
    });
    expect(textOf(findByTestId(renderer.root, 'cli-handoff-command'))).toBe(
      'cpa-manager-plus initialize'
    );
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'bootstrap-recheck-discovery' })
    ).toHaveLength(0);
    expect(bootstrapTransport).not.toHaveBeenCalled();
  });

  it('keeps an invalid source fail-closed during and after a read-only recheck', async () => {
    let resolveRecheck!: (response: Response) => void;
    const publicDiscovery = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        Response.json({
          contract_version: 'cpamp.phase4-01.discovery.v1',
          status: 'fresh_claim_required',
          mode: 'fresh',
          auth_entry: 'fresh_claim',
          reason: 'qualification_unavailable',
          authentication_required: true,
        })
      )
      .mockImplementationOnce(
        () =>
          new Promise<Response>((resolve) => {
            resolveRecheck = resolve;
          })
      );
    const bootstrapTransport = vi.fn();
    vi.stubGlobal('fetch', publicDiscovery);
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshBootstrapPage
          handoff={null}
          transport={createBootstrapTransport(bootstrapTransport)}
        />
      );
    });
    expect(renderer.root.findAllByProps({ 'data-testid': 'cli-handoff-command' })).toHaveLength(0);
    await act(async () =>
      findByTestId(renderer.root, 'bootstrap-recheck-discovery').props.onClick()
    );
    expect(publicDiscovery).toHaveBeenCalledTimes(2);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'bootstrap-recheck-discovery' })
    ).toHaveLength(0);
    expect(textOf(findByTestId(renderer.root, 'bootstrap-discovery-unconfirmed'))).toContain(
      '正在读取来源状态'
    );
    await act(async () => {
      resolveRecheck(
        Response.json(
          {
            contract_version: 'cpamp.phase4-01.discovery.v1',
            status: 'unavailable',
            auth_entry: 'unavailable',
            reason: 'observation_unavailable',
            authentication_required: false,
          },
          { status: 503 }
        )
      );
    });
    expect(renderer.root.findAllByProps({ 'data-testid': 'cli-handoff-command' })).toHaveLength(0);
    expect(textOf(findByTestId(renderer.root, 'bootstrap-discovery-guidance'))).toContain(
      '来源状态暂无法确认'
    );
    expect(bootstrapTransport).not.toHaveBeenCalled();
  });

  it.each([
    [
      'fresh_claim_required',
      'fresh',
      'fresh_claim',
      'fresh_claim_required',
      true,
      '检测到需要宿主机认领的 Fresh 初始化状态',
    ],
  ])(
    'shows owned Fresh discovery status %s without claiming an operation',
    async (status, mode, authEntry, reason, authRequired, title) => {
      const discovery = vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            contract_version: 'cpamp.phase4-01.discovery.v1',
            status,
            mode,
            auth_entry: authEntry,
            reason,
            authentication_required: authRequired,
          })
        )
      );
      vi.stubGlobal('fetch', discovery);
      const transport = createBootstrapTransport(vi.fn());
      let renderer!: ReactTestRenderer;
      await act(async () => {
        renderer = create(<FreshBootstrapPage handoff={null} transport={transport} />);
      });

      expect(textOf(findByTestId(renderer.root, 'bootstrap-discovery-guidance'))).toContain(title);
      const command = renderer.root.findAllByProps({
        'data-testid': 'cli-handoff-command',
      });
      expect(command).toHaveLength(1);
      expect(textOf(command[0])).toBe('cpa-manager-plus initialize');
      expect(discovery).toHaveBeenCalledTimes(1);
      expect(discovery.mock.calls[0][0]).toBe('/.well-known/cpamp-bootstrap');
      expect(discovery.mock.calls[0][1]).toMatchObject({
        method: 'GET',
        mode: 'same-origin',
        credentials: 'omit',
        cache: 'no-store',
        redirect: 'error',
      });
      expect(discovery.mock.calls[0][1].headers).toEqual({ Accept: 'application/json' });
    }
  );

  it('does not project inconsistent Fresh discovery as valid guidance', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            contract_version: 'cpamp.phase4-01.discovery.v1',
            status: 'fresh_claim_required',
            mode: 'fresh',
            auth_entry: 'fresh_claim',
            reason: 'qualification_unavailable',
            authentication_required: true,
          })
        )
      )
    );
    const transport = createBootstrapTransport(vi.fn());
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<FreshBootstrapPage handoff={null} transport={transport} />);
    });
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'bootstrap-discovery-guidance' })
    ).toHaveLength(0);
    expect(renderer.root.findAllByProps({ 'data-testid': 'cli-handoff-command' })).toHaveLength(0);
    expect(textOf(findByTestId(renderer.root, 'bootstrap-discovery-unconfirmed'))).toContain(
      '来源状态无法确认'
    );
  });

  it.each([
    [
      'unsupported_upgrade',
      'unsupported_upgrade',
      'admin_session',
      'qualification_unavailable',
      true,
      '检测到尚未获得升级资格的现有来源',
    ],
    [
      'recovery_required',
      'recovery',
      'host_intervention',
      'recovery_host_intervention',
      false,
      '检测到需要宿主机介入的恢复状态',
    ],
  ])(
    'routes non-Fresh %s to its own authentication or host gate',
    async (status, mode, authEntry, reason, authRequired, title) => {
      vi.stubGlobal(
        'fetch',
        vi.fn().mockResolvedValue(
          new Response(
            JSON.stringify({
              contract_version: 'cpamp.phase4-01.discovery.v1',
              status,
              mode,
              auth_entry: authEntry,
              reason,
              authentication_required: authRequired,
            })
          )
        )
      );
      let renderer!: ReactTestRenderer;
      await act(async () => {
        renderer = create(
          <FreshBootstrapPage handoff={null} transport={createBootstrapTransport(vi.fn())} />
        );
      });
      if (status === 'unsupported_upgrade') {
        expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-auth-entry' })).toHaveLength(1);
        expect(renderer.root.findAllByProps({ 'data-testid': 'cli-handoff-command' })).toHaveLength(0);
        expect(renderer.root.findAllByProps({ 'data-testid': 'host-preflight-command' })).toHaveLength(0);
        return;
      }
      expect(textOf(findByTestId(renderer.root, 'bootstrap-discovery-guidance'))).toContain(title);
      expect(renderer.root.findAllByProps({ 'aria-label': 'Fresh Bootstrap 进度' })).toHaveLength(
        0
      );
      expect(textOf(findByTestId(renderer.root, 'host-preflight-command'))).toBe(
        'cpa-manager-plus legacy-preflight --db /path/to/usage.sqlite --summary'
      );
      expect(renderer.root.findAllByProps({ 'data-testid': 'cli-handoff-command' })).toHaveLength(
        0
      );
      expect(
        renderer.root.findAllByProps({ 'data-testid': 'bootstrap-recheck-discovery' })
      ).toHaveLength(0);
      expect(textOf(findByTestId(renderer.root, 'bootstrap-no-handoff'))).not.toContain(
        'cpa-manager-plus initialize'
      );
    }
  );

  it('shows read-only Host guidance for a canonical unavailable discovery on HTTP 503', async () => {
    const unavailable = {
      contract_version: 'cpamp.phase4-01.discovery.v1',
      status: 'unavailable',
      auth_entry: 'unavailable',
      reason: 'observation_unavailable',
      authentication_required: false,
    };
    const discovery = vi.fn().mockResolvedValue(Response.json(unavailable, { status: 503 }));
    vi.stubGlobal('fetch', discovery);
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshBootstrapPage handoff={null} transport={createBootstrapTransport(vi.fn())} />
      );
    });
    const gate = textOf(findByTestId(renderer.root, 'bootstrap-no-handoff'));
    expect(gate).toContain('来源状态暂无法确认');
    expect(gate).not.toContain('cpa-manager-plus initialize');
    expect(textOf(findByTestId(renderer.root, 'host-preflight-command'))).toBe(
      'cpa-manager-plus legacy-preflight --db /path/to/usage.sqlite --summary'
    );
    expect(textOf(findByTestId(renderer.root, 'fresh-bootstrap-shell'))).not.toContain(
      bootstrapJourneyText('preview')
    );
    expect(textOf(findByTestId(renderer.root, 'fresh-bootstrap-shell'))).toContain(
      bootstrapJourneyText('preflight')
    );
    expect(discovery).toHaveBeenCalledTimes(1);
  });

  it('does not infer read-only Host eligibility from an inconsistent 503 payload', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        Response.json(
          {
            contract_version: 'cpamp.phase4-01.discovery.v1',
            status: 'unavailable',
            mode: 'fresh',
            auth_entry: 'unavailable',
            reason: 'observation_unavailable',
            authentication_required: false,
          },
          { status: 503 }
        )
      )
    );
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshBootstrapPage handoff={null} transport={createBootstrapTransport(vi.fn())} />
      );
    });
    expect(renderer.root.findAllByProps({ 'data-testid': 'host-preflight-command' })).toHaveLength(
      0
    );
  });

  it.each([
    [
      503,
      {
        contract_version: 'cpamp.phase4-01.discovery.v1',
        status: 'fresh_claim_required',
        mode: 'fresh',
        auth_entry: 'fresh_claim',
        reason: 'fresh_claim_required',
        authentication_required: true,
      },
    ],
    [
      503,
      {
        contract_version: 'cpamp.phase4-01.discovery.v1',
        status: 'unsupported_upgrade',
        mode: 'unsupported_upgrade',
        auth_entry: 'admin_session',
        reason: 'qualification_unavailable',
        authentication_required: true,
      },
    ],
    [
      200,
      {
        contract_version: 'cpamp.phase4-01.discovery.v1',
        status: 'unavailable',
        auth_entry: 'unavailable',
        reason: 'observation_unavailable',
        authentication_required: false,
      },
    ],
  ])(
    'rejects inconsistent HTTP %i and discovery status without suggesting setup',
    async (status, payload) => {
      vi.stubGlobal('fetch', vi.fn().mockResolvedValue(Response.json(payload, { status })));
      let renderer!: ReactTestRenderer;
      await act(async () => {
        renderer = create(
          <FreshBootstrapPage handoff={null} transport={createBootstrapTransport(vi.fn())} />
        );
      });
      expect(renderer.root.findAllByProps({ 'data-testid': 'cli-handoff-command' })).toHaveLength(
        0
      );
      expect(
        renderer.root.findAllByProps({ 'data-testid': 'host-preflight-command' })
      ).toHaveLength(0);
      expect(
        renderer.root.findAllByProps({ 'data-testid': 'bootstrap-discovery-guidance' })
      ).toHaveLength(0);
      expect(textOf(findByTestId(renderer.root, 'bootstrap-discovery-unconfirmed'))).toContain(
        '来源状态无法确认'
      );
    }
  );

  it('rejects mismatched non-Fresh discovery tuples and preserves the existing safe fallback', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            contract_version: 'cpamp.phase4-01.discovery.v1',
            status: 'unsupported_upgrade',
            mode: 'unsupported_upgrade',
            auth_entry: 'fresh_claim',
            reason: 'qualification_unavailable',
            authentication_required: true,
          })
        )
      )
    );
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshBootstrapPage handoff={null} transport={createBootstrapTransport(vi.fn())} />
      );
    });
    expect(renderer.root.findAllByProps({ 'data-testid': 'host-preflight-command' })).toHaveLength(
      0
    );
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'bootstrap-discovery-guidance' })
    ).toHaveLength(0);
    expect(renderer.root.findAllByProps({ 'data-testid': 'cli-handoff-command' })).toHaveLength(0);
  });

  it('displays authority rejection feedback when claim exchange returns HTTP 401', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(new Response('unauthorized', { status: 401 }));
    const transport = createBootstrapTransport(fetchMock);
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<FreshBootstrapPage handoff={handoff} transport={transport} />);
    });

    expect(textOf(findByTestId(renderer.root, 'init-error-title'))).toContain('HTTP 401');
  });

  it('explains single retry and HTTP 401 re-initialization when initial claim exchange encounters a transport error', async () => {
    const fetchMock = vi.fn().mockRejectedValueOnce(new TypeError('network down'));
    const transport = createBootstrapTransport(fetchMock);
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<FreshBootstrapPage handoff={handoff} transport={transport} />);
    });

    expect(textOf(findByTestId(renderer.root, 'init-error-detail'))).toContain(
      '若服务端已消费本次一次性 Claim Token 并返回 HTTP 401，请在宿主机重新执行 cpa-manager-plus initialize'
    );
    expect(textOf(findByTestId(renderer.root, 'bootstrap-authority-error'))).toContain(
      '可尝试重新连接一次；若返回 HTTP 401（令牌已被消费）'
    );
  });

  it('opens the unified CPA, key and route journey after claim rather than a separate Admin setup', async () => {
    const transport = vi
      .fn()
      .mockResolvedValue(
        Response.json({ token: 'installation-session', operation: pendingOperation })
      );
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshBootstrapPage handoff={handoff} transport={createBootstrapTransport(transport)} />
      );
    });
    expect(findByTestId(renderer.root, 'fresh-setup-wizard')).toBeDefined();
    expect(renderer.root.findAllByProps({ 'data-testid': 'admin-init-form' })).toHaveLength(0);
    expect(renderer.root.findAllByProps({ 'data-testid': 'choose-new_managed' })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-testid': 'choose-connect_external' })).toHaveLength(
      1
    );
    expect(transport).toHaveBeenCalledTimes(1);
    await act(async () => renderer.unmount());
  });
});
