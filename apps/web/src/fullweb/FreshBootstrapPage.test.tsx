import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { BOOTSTRAP_UI, createBootstrapTransport } from './bootstrapTransport';
import { FreshBootstrapPage } from './FreshBootstrapPage';
import { fullWebLanguage, setFullWebLanguage } from '../i18n/fullweb';
import { bootstrapJourneyText } from '../i18n/bootstrapJourney';
import { activateFreshLanguage, FRESH_LANGUAGE_STORAGE_KEY } from './freshLanguage';

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

// Copy is asserted by what a reader can see in the rendered tree — the script
// it is written in and the structure the state exposes — never by echoing the
// production translation table back at itself.
const CJK = /[\u4e00-\u9fff]/;
const CYRILLIC = /[\u0400-\u04ff]/;
const LATIN = /[A-Za-z]/;

// What a reader of that language must actually see: the interface copy is in
// their script, not the fallback table. Protocol terms (Handoff, Claim,
// data.key, cpa-manager-plus) stay untranslated in every language.
function assertRenderedIn(language: 'zh-CN' | 'zh-TW' | 'en' | 'ru', text: string): void {
  expect(text).not.toBe('');
  if (language === 'ru') {
    expect(text).toMatch(CYRILLIC);
  } else {
    expect(text).toMatch(LATIN);
  }
  if (language === 'en') {
    expect(text).not.toMatch(CYRILLIC);
  }
  if (language === 'zh-CN' || language === 'zh-TW') {
    expect(text).toMatch(CJK);
    expect(text).not.toMatch(CYRILLIC);
  } else {
    expect(text).not.toMatch(CJK);
  }
}

// Stands in for the browser store the Console language menu persists into.
const storage = new Map<string, string>();

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
    storage.clear();
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => storage.get(key) ?? null,
      setItem: (key: string, value: string) => void storage.set(key, String(value)),
      removeItem: (key: string) => void storage.delete(key),
    });
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
    expect(textOf(findByTestId(renderer.root, 'bootstrap-discovery-unconfirmed'))).not.toBe('');
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'bootstrap-discovery-guidance' })
    ).toHaveLength(0);
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
    const gate = textOf(findByTestId(renderer.root, 'bootstrap-no-handoff'));
    expect(gate).toMatch(CJK);
    expect(gate).not.toContain('cpa-manager-plus initialize');
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'bootstrap-discovery-guidance' })
    ).toHaveLength(0);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'host-preflight-instructions' })
    ).toHaveLength(0);
    expect(textOf(findByTestId(renderer.root, 'bootstrap-discovery-unconfirmed'))).not.toBe('');
  });

  it('tells the pending source check apart from an unconfirmed source', async () => {
    let settle!: (value: Response) => void;
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(
        () =>
          new Promise<Response>((resolve) => {
            settle = resolve;
          })
      )
    );
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshBootstrapPage handoff={null} transport={createBootstrapTransport(vi.fn())} />
      );
    });
    const pending = textOf(findByTestId(renderer.root, 'bootstrap-discovery-unconfirmed'));
    expect(pending).toMatch(CJK);

    // A result that grants no authority retires the pending wording: reaching
    // the server must change what the operator reads, not leave it as-is.
    await act(async () => {
      settle(
        Response.json({
          contract_version: 'cpamp.phase4-01.discovery.v1',
          status: 'fresh_claim_required',
          mode: 'fresh',
          auth_entry: 'fresh_claim',
          reason: 'qualification_unavailable',
          authentication_required: true,
        })
      );
    });
    const settled = textOf(findByTestId(renderer.root, 'bootstrap-discovery-unconfirmed'));
    expect(settled).toMatch(CJK);
    expect(settled).not.toBe(pending);
    expect(renderer.root.findAllByProps({ 'data-testid': 'cli-handoff-command' })).toHaveLength(0);
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
    expect(textOf(findByTestId(renderer.root, 'bootstrap-discovery-guidance'))).toMatch(CJK);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'bootstrap-recheck-discovery' })
    ).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-testid': 'cli-handoff-command' })).toHaveLength(0);
    await act(async () =>
      findByTestId(renderer.root, 'bootstrap-recheck-discovery').props.onClick()
    );
    expect(publicDiscovery).toHaveBeenCalledTimes(2);
    // The recheck retires the guidance and shows the pending state instead.
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'bootstrap-discovery-guidance' })
    ).toHaveLength(0);
    expect(textOf(findByTestId(renderer.root, 'bootstrap-discovery-unconfirmed'))).not.toBe('');
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
    expect(textOf(findByTestId(renderer.root, 'bootstrap-discovery-unconfirmed'))).not.toBe('');
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'bootstrap-discovery-guidance' })
    ).toHaveLength(0);
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
    expect(textOf(findByTestId(renderer.root, 'bootstrap-discovery-guidance'))).toMatch(CJK);
    expect(bootstrapTransport).not.toHaveBeenCalled();
  });

  it.each([['fresh_claim_required', 'fresh', 'fresh_claim', 'fresh_claim_required', true]])(
    'shows owned Fresh discovery status %s without claiming an operation',
    async (status, mode, authEntry, reason, authRequired) => {
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

      const guidance = textOf(findByTestId(renderer.root, 'bootstrap-discovery-guidance'));
      expect(guidance).toMatch(CJK);
      expect(
        renderer.root.findAllByProps({ 'data-testid': 'host-preflight-instructions' })
      ).toHaveLength(0);
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
    expect(textOf(findByTestId(renderer.root, 'bootstrap-discovery-unconfirmed'))).toMatch(CJK);
  });

  it.each([
    ['unsupported_upgrade', 'unsupported_upgrade', 'admin_session', 'qualification_unavailable', true],
    ['recovery_required', 'recovery', 'host_intervention', 'recovery_host_intervention', false],
  ])(
    'routes non-Fresh %s to its own authentication or host gate',
    async (status, mode, authEntry, reason, authRequired) => {
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
      expect(textOf(findByTestId(renderer.root, 'bootstrap-discovery-guidance'))).toMatch(CJK);
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
    expect(gate).toMatch(CJK);
    expect(gate).not.toContain('cpa-manager-plus initialize');
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'host-preflight-instructions' })
    ).toHaveLength(1);
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
      expect(textOf(findByTestId(renderer.root, 'bootstrap-discovery-unconfirmed'))).toMatch(CJK);
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

    const detail = textOf(findByTestId(renderer.root, 'init-error-detail'));
    expect(detail).toMatch(CJK);
    expect(detail).not.toBe(textOf(findByTestId(renderer.root, 'init-error-title')));
    // The transport failure keeps the named recovery affordances on screen.
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'retry-start-btn' })
    ).toHaveLength(1);
    expect(textOf(findByTestId(renderer.root, 'bootstrap-authority-error'))).toMatch(CJK);
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

  it.each(['en', 'ru'] as const)(
    'renders every blocked discovery gate in %s without falling back to one language',
    async (language) => {
      const states = [
        {
          payload: {
            status: 'fresh_claim_required',
            mode: 'fresh',
            auth_entry: 'fresh_claim',
            reason: 'fresh_claim_required',
            authentication_required: true,
          },
          httpStatus: 200,
          hostPreflight: false,
        },
        {
          payload: {
            status: 'recovery_required',
            mode: 'recovery',
            auth_entry: 'host_intervention',
            reason: 'recovery_host_intervention',
            authentication_required: false,
          },
          httpStatus: 200,
          hostPreflight: true,
        },
        {
          payload: {
            status: 'unavailable',
            auth_entry: 'unavailable',
            reason: 'observation_unavailable',
            authentication_required: false,
          },
          httpStatus: 503,
          hostPreflight: true,
        },
      ] as const;
      for (const state of states) {
        vi.stubGlobal(
          'fetch',
          vi.fn().mockResolvedValue(
            Response.json(
              { contract_version: 'cpamp.phase4-01.discovery.v1', ...state.payload },
              { status: state.httpStatus }
            )
          )
        );
        setFullWebLanguage(language);
        let renderer!: ReactTestRenderer;
        await act(async () => {
          renderer = create(
            <FreshBootstrapPage handoff={null} transport={createBootstrapTransport(vi.fn())} />
          );
        });
        const gate = textOf(findByTestId(renderer.root, 'bootstrap-no-handoff'));
        assertRenderedIn(language, gate);
        // The gate says which host action applies, with its command present.
        expect(
          renderer.root.findAllByProps({ 'data-testid': 'host-preflight-instructions' })
        ).toHaveLength(state.hostPreflight ? 1 : 0);
        expect(
          renderer.root.findAllByProps({ 'data-testid': 'cli-handoff-command' })
        ).toHaveLength(state.hostPreflight ? 0 : 1);
        await act(async () => renderer.unmount());
      }
    }
  );

  it.each(['en', 'ru'] as const)(
    'renders the transport failure, its recovery copy and the diagnostics in %s',
    async (language) => {
      vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('network down')));
      setFullWebLanguage(language);
      const transport = createBootstrapTransport(
        vi.fn().mockRejectedValue(new TypeError('network down'))
      );
      let renderer!: ReactTestRenderer;
      await act(async () => {
        renderer = create(<FreshBootstrapPage handoff={handoff} transport={transport} />);
      });
      const title = textOf(findByTestId(renderer.root, 'init-error-title'));
      const detail = textOf(findByTestId(renderer.root, 'init-error-detail'));
      assertRenderedIn(language, title);
      assertRenderedIn(language, detail);
      expect(detail).not.toBe(title);
      const card = textOf(findByTestId(renderer.root, 'bootstrap-authority-error'));
      // The card keeps the operator-facing facts and the way out, in that language.
      assertRenderedIn(language, card);
      expect(card).toContain(identity.operationId);
      expect(card).toContain(`#${handoff.claimGeneration}`);
      expect(card).toContain('cpa-manager-plus initialize');
      expect(
        renderer.root.findAllByProps({ 'data-testid': 'retry-start-btn' })
      ).toHaveLength(1);
      await act(async () => renderer.unmount());
    }
  );

  it.each(['en', 'ru'] as const)('localizes the claim diagnostics in %s', async (language) => {
    let resolveStart!: (value: Response) => void;
    const transport = createBootstrapTransport(
      vi.fn().mockImplementation(
        () =>
          new Promise<Response>((resolve) => {
            resolveStart = resolve;
          })
      )
    );
    setFullWebLanguage(language);
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<FreshBootstrapPage handoff={handoff} transport={transport} />);
    });
    const card = textOf(findByTestId(renderer.root, 'bootstrap-connecting'));
    assertRenderedIn(language, card);
    // The diagnostics name the real claim facts an operator quotes back.
    expect(card).toContain(identity.operationId);
    expect(card).toContain(`${identity.installationId} · ${identity.generation}`);
    await act(async () => {
      resolveStart(Response.json({ token: 'installation-session', operation: pendingOperation }));
    });
    await act(async () => renderer.unmount());
  });

  it('re-renders the whole Bootstrap and wizard surface from the header language menu', async () => {
    const transport = createBootstrapTransport(
      vi
        .fn()
        .mockResolvedValue(
          Response.json({ token: 'installation-session', operation: pendingOperation })
        )
    );
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<FreshBootstrapPage handoff={handoff} transport={transport} />);
    });
    // The menu offers every Console language once, in the Console's order, and
    // each entry names itself so a reader can find their own language.
    const select = findByTestId(renderer.root, 'bootstrap-language');
    const options = select.children.filter(
      (child): child is ReactTestInstance => typeof child !== 'string'
    );
    expect(options.map((option) => option.props.value)).toEqual(['zh-CN', 'zh-TW', 'en', 'ru']);
    const labels = options.map((option) => textOf(option));
    expect(labels.every((label) => label !== '')).toBe(true);
    expect(new Set(labels).size).toBe(labels.length);
    expect(select.props.value).toBe('zh-CN');
    const before = textOf(findByTestId(renderer.root, 'fresh-setup-wizard'));
    expect(before).toMatch(CJK);

    await act(async () => {
      select.props.onChange({ target: { value: 'ru' } });
    });

    expect(findByTestId(renderer.root, 'bootstrap-language').props.value).toBe('ru');
    const wizard = textOf(findByTestId(renderer.root, 'fresh-setup-wizard'));
    expect(wizard).not.toBe(before);
    assertRenderedIn('ru', wizard);
    // The same persisted store the Console header menu writes.
    expect(JSON.parse(storage.get(FRESH_LANGUAGE_STORAGE_KEY) ?? '').state.language).toBe('ru');
    await act(async () => renderer.unmount());

    // A fresh document starts on that stored choice, not the browser language.
    vi.stubGlobal('navigator', { languages: ['en-US'], language: 'en-US' });
    setFullWebLanguage('en');
    expect(activateFreshLanguage()).toBe('ru');
    expect(fullWebLanguage()).toBe('ru');
  });

  it('renders Traditional Chinese copy distinct from Simplified when the Console stores zh-TW', async () => {
    storage.set(
      FRESH_LANGUAGE_STORAGE_KEY,
      JSON.stringify({ state: { language: 'zh-TW' }, version: 0 })
    );
    expect(activateFreshLanguage()).toBe('zh-TW');

    const renderGate = async (language: 'zh-CN' | 'zh-TW') => {
      setFullWebLanguage(language);
      let renderer!: ReactTestRenderer;
      await act(async () => {
        renderer = create(
          <FreshBootstrapPage handoff={null} transport={createBootstrapTransport(vi.fn())} />
        );
      });
      const gate = textOf(findByTestId(renderer.root, 'bootstrap-discovery-unconfirmed'));
      await act(async () => renderer.unmount());
      return gate;
    };

    const traditional = await renderGate('zh-TW');
    const simplified = await renderGate('zh-CN');
    assertRenderedIn('zh-TW', traditional);
    assertRenderedIn('zh-CN', simplified);
    // Same state, same component: the only difference is the selected table, so
    // a zh-TW branch that collapsed into zh-CN could not pass this.
    expect(traditional).not.toBe(simplified);
  });
});
