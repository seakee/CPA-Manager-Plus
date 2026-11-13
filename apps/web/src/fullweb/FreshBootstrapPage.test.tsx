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

// Names, wire formats and identifiers a translator must not translate. Generic
// English words are deliberately absent: a block built out of those is
// untranslated copy, not protocol, and no exemption list may grow to hide it.
const NAMES =
  /\bcpa-manager-plus\b|\bdata\.key\b|\bhttp\b|\bsqlite\b|\butf-8\b|\bapi\b|\burl\b|\bjson\b|\bv2\b|\bid\b/gi;

// Two or more neighbouring lower-case Latin words are an English clause. Copy
// keeps a product name capitalized ("Claim Token", "Fresh/Adopt/Upgrade") and
// never carries an English sentence.
const ENGLISH_CLAUSE = /(?:^|[^A-Za-z])([a-z]{2,}(?:[ \t]+[a-z]{2,})+)/;

type Language = 'zh-CN' | 'zh-TW' | 'en' | 'ru';

function englishClauseIn(text: string): string | null {
  const match = ENGLISH_CLAUSE.exec(text.replace(NAMES, ' '));
  return match ? match[1] : null;
}

// A visible copy block: a title, a description, an action or a recovery
// explanation. It must be written in the reader's script. Mixed-in English
// product names are fine; a block that stayed in English is not, whatever
// words it happens to use.
function assertCopyIn(language: Language, text: string): void {
  expect(text, `empty copy block in ${language}`).not.toBe('');
  if (language === 'en') {
    expect(text).toMatch(LATIN);
    expect(text).not.toMatch(CYRILLIC);
    expect(text).not.toMatch(CJK);
    return;
  }
  expect(text, `expected ${language} copy in: ${text}`).toMatch(language === 'ru' ? CYRILLIC : CJK);
  expect(text, `foreign script in ${language}: ${text}`).not.toMatch(
    language === 'ru' ? CJK : CYRILLIC
  );
  expect(englishClauseIn(text), `English clause left in ${language}: ${text}`).toBeNull();
}

// A fixed, standalone protocol label such as a card step tag. It may stay in
// English by design ("Claim", "Bootstrap Gate"), so it is not required to carry
// the reader's script — only to carry no other script and no English sentence.
function assertLabelIn(language: Language, text: string): void {
  expect(text, `empty label in ${language}`).not.toBe('');
  if (language === 'en') {
    expect(text).not.toMatch(CYRILLIC);
    expect(text).not.toMatch(CJK);
    return;
  }
  expect(text, `foreign script in ${language}: ${text}`).not.toMatch(
    language === 'ru' ? CJK : CYRILLIC
  );
  expect(englishClauseIn(text), `English clause left in ${language}: ${text}`).toBeNull();
}

// Stands in for the browser store the Console language menu persists into.
const storage = new Map<string, string>();

const isHost = (className: string) => (node: ReactTestInstance) =>
  typeof node.type === 'string' &&
  String(node.props?.className ?? '')
    .split(' ')
    .includes(className);

/** Each visible block carrying that class, as its own string. */
function blocksOf(root: ReactTestInstance, className: string): string[] {
  return root.findAll(isHost(className)).map((node) => textOf(node));
}

/** The action alert of a gate: the last one, after any status guidance. */
function actionAlert(gate: ReactTestInstance): ReactTestInstance {
  const alerts = gate.findAll(isHost('alert'));
  return alerts[alerts.length - 1];
}

/** Every visible block of a card header, asserted on its own kind. */
function assertCardHeaderReads(language: Language, card: ReactTestInstance) {
  const [tag] = blocksOf(card, 'wizard-step-tag');
  const [title] = blocksOf(card, 'wizard-title');
  const [desc] = blocksOf(card, 'wizard-desc');
  // The tag is a fixed protocol label; the title and description are copy, so a
  // block that silently kept the fallback table fails here on its own.
  assertLabelIn(language, tag);
  assertCopyIn(language, title);
  assertCopyIn(language, desc);
  expect(title).not.toBe(desc);
  return { tag, title, desc };
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
    [
      'unsupported_upgrade',
      'unsupported_upgrade',
      'admin_session',
      'qualification_unavailable',
      true,
    ],
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
        expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-auth-entry' })).toHaveLength(
          1
        );
        expect(renderer.root.findAllByProps({ 'data-testid': 'cli-handoff-command' })).toHaveLength(
          0
        );
        expect(
          renderer.root.findAllByProps({ 'data-testid': 'host-preflight-command' })
        ).toHaveLength(0);
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
    expect(renderer.root.findAllByProps({ 'data-testid': 'retry-start-btn' })).toHaveLength(1);
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

  it('surfaces unfinished Fresh installation resume guidance without legacy-preflight, re-translates, and rechecks discovery on demand', async () => {
    const discoveryFetch = vi
      .fn()
      .mockResolvedValueOnce(
        Response.json(
          {
            contract_version: 'cpamp.phase4-01.discovery.v1',
            status: 'fresh_claim_required',
            mode: 'fresh',
            auth_entry: 'fresh_claim',
            reason: 'fresh_resume_required',
            authentication_required: true,
          },
          { status: 200 }
        )
      )
      .mockResolvedValueOnce(
        Response.json(
          {
            contract_version: 'cpamp.phase4-01.discovery.v1',
            status: 'fresh_claim_required',
            mode: 'fresh',
            auth_entry: 'fresh_claim',
            reason: 'fresh_claim_required',
            authentication_required: true,
          },
          { status: 200 }
        )
      );
    vi.stubGlobal('fetch', discoveryFetch);
    setFullWebLanguage('zh-CN');
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshBootstrapPage handoff={null} transport={createBootstrapTransport(vi.fn())} />
      );
    });
    const shell = findByTestId(renderer.root, 'fresh-bootstrap-shell');
    expect(textOf(shell)).toContain(bootstrapJourneyText('pending', 'zh-CN'));
    expect(findByTestId(renderer.root, 'fresh-resume-instructions')).toBeTruthy();
    expect(textOf(findByTestId(renderer.root, 'cli-handoff-command'))).toBe(
      'cpa-manager-plus initialize'
    );
    expect(renderer.root.findAllByProps({ 'data-testid': 'host-preflight-command' })).toHaveLength(
      0
    );
    expect(textOf(shell)).not.toContain('legacy-preflight');

    await act(async () => {
      findByTestId(renderer.root, 'bootstrap-language').props.onChange({ target: { value: 'en' } });
    });
    expect(textOf(shell)).toContain(bootstrapJourneyText('pending', 'en'));
    expect(textOf(findByTestId(renderer.root, 'fresh-resume-instructions'))).toContain(
      '/.well-known/cpamp-bootstrap/ui#handoff=...'
    );

    await act(async () => {
      findByTestId(renderer.root, 'bootstrap-recheck-discovery').props.onClick();
    });
    expect(discoveryFetch).toHaveBeenCalledTimes(2);
    expect(renderer.root.findAllByProps({ 'data-testid': 'fresh-resume-instructions' })).toHaveLength(
      0
    );
    expect(textOf(shell)).not.toContain(bootstrapJourneyText('pending', 'en'));
    expect(textOf(shell)).toContain('New installation');
    await act(async () => renderer.unmount());
  });

  it.each(['en', 'ru', 'zh-CN', 'zh-TW'] as const)(
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
            status: 'fresh_claim_required',
            mode: 'fresh',
            auth_entry: 'fresh_claim',
            reason: 'fresh_resume_required',
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
          vi
            .fn()
            .mockResolvedValue(
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
        const gate = findByTestId(renderer.root, 'bootstrap-no-handoff');
        assertCardHeaderReads(language, gate);

        // The status guidance explains why the gate is closed.
        const guidance = findByTestId(renderer.root, 'bootstrap-discovery-guidance');
        const [guidanceTitle] = blocksOf(guidance, 'alert-title');
        const [guidanceDesc] = blocksOf(guidance, 'alert-desc');
        assertCopyIn(language, guidanceTitle);
        assertCopyIn(language, guidanceDesc);
        expect(guidanceDesc).not.toBe(guidanceTitle);

        // The action area names the host step on its own, with its command.
        const action = actionAlert(gate);
        const [actionTitle] = blocksOf(action, 'alert-title');
        const [actionDesc] = blocksOf(action, 'alert-desc');
        assertCopyIn(language, actionTitle);
        assertCopyIn(language, actionDesc);
        expect(actionDesc).not.toBe(actionTitle);
        if (state.hostPreflight) {
          expect(
            renderer.root.findAllByProps({ 'data-testid': 'host-preflight-instructions' })
          ).toHaveLength(1);
          expect(textOf(findByTestId(renderer.root, 'host-preflight-command'))).toBe(
            'cpa-manager-plus legacy-preflight --db /path/to/usage.sqlite --summary'
          );
          expect(
            renderer.root.findAllByProps({ 'data-testid': 'cli-handoff-command' })
          ).toHaveLength(0);
        } else {
          // The Fresh path keeps the copyable Handoff command and its fragment.
          expect(textOf(findByTestId(renderer.root, 'cli-handoff-command'))).toBe(
            'cpa-manager-plus initialize'
          );
          expect(actionDesc).toContain('#handoff=');
          expect(
            renderer.root.findAllByProps({ 'data-testid': 'host-preflight-instructions' })
          ).toHaveLength(0);
        }
        await act(async () => renderer.unmount());
      }
    }
  );

  it.each(['en', 'ru', 'zh-CN', 'zh-TW'] as const)(
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
      assertCopyIn(language, title);
      assertCopyIn(language, detail);
      expect(detail).not.toBe(title);
      // The 401 recovery path stays quotable in every language.
      expect(detail).toContain('HTTP 401');
      expect(detail).toContain('cpa-manager-plus initialize');

      const card = findByTestId(renderer.root, 'bootstrap-authority-error');
      // Each block of the card is checked where it renders, not as one blob.
      assertCardHeaderReads(language, card);
      const note = blocksOf(card, 'bootstrap-retry-note');
      expect(note).toHaveLength(1);
      note.forEach((text) => assertCopyIn(language, text));
      const labels = blocksOf(card, 'info-key');
      expect(labels.length).toBeGreaterThan(0);
      labels.forEach((text) => assertCopyIn(language, text));
      const values = blocksOf(card, 'info-val');
      // Data values are quoted verbatim; only the trailing mode row is copy.
      expect(values).toContain(identity.operationId);
      expect(values).toContain(`#${handoff.claimGeneration}`);
      assertCopyIn(language, values[values.length - 1]);

      // The operator-facing facts and the way out survive translation.
      const cardText = textOf(card);
      expect(cardText).toContain(identity.operationId);
      expect(cardText).toContain(`#${handoff.claimGeneration}`);
      expect(cardText).toContain('cpa-manager-plus initialize');
      expect(renderer.root.findAllByProps({ 'data-testid': 'retry-start-btn' })).toHaveLength(1);
      await act(async () => renderer.unmount());
    }
  );

  it('re-renders a failure card already on screen when the language changes', async () => {
    const transport = createBootstrapTransport(
      vi.fn().mockRejectedValueOnce(new TypeError('network down'))
    );
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<FreshBootstrapPage handoff={handoff} transport={transport} />);
    });
    const before = {
      title: textOf(findByTestId(renderer.root, 'init-error-title')),
      detail: textOf(findByTestId(renderer.root, 'init-error-detail')),
    };
    assertCopyIn('zh-CN', before.title);
    assertCopyIn('zh-CN', before.detail);

    await act(async () => {
      findByTestId(renderer.root, 'bootstrap-language').props.onChange({
        target: { value: 'ru' },
      });
    });

    const after = {
      title: textOf(findByTestId(renderer.root, 'init-error-title')),
      detail: textOf(findByTestId(renderer.root, 'init-error-detail')),
    };
    // The feedback is held as keys, so the sentence around the same failure is
    // written again rather than kept as the string first rendered.
    assertCopyIn('ru', after.title);
    assertCopyIn('ru', after.detail);
    expect(after.title).not.toBe(before.title);
    expect(after.detail).not.toBe(before.detail);
    // Nothing about the operation changed: same facts, same way out.
    const card = textOf(findByTestId(renderer.root, 'bootstrap-authority-error'));
    expect(card).toContain(identity.operationId);
    expect(card).toContain('cpa-manager-plus initialize');
    expect(renderer.root.findAllByProps({ 'data-testid': 'retry-start-btn' })).toHaveLength(1);
    await act(async () => renderer.unmount());
  });

  it.each(['en', 'ru', 'zh-CN', 'zh-TW'] as const)(
    'localizes the claim diagnostics in %s',
    async (language) => {
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
      const card = findByTestId(renderer.root, 'bootstrap-connecting');
      assertCardHeaderReads(language, card);
      const labels = blocksOf(card, 'info-key');
      expect(labels.length).toBeGreaterThan(0);
      labels.forEach((text) => assertCopyIn(language, text));
      // The diagnostics name the real claim facts an operator quotes back.
      const cardText = textOf(card);
      expect(cardText).toContain(identity.operationId);
      expect(cardText).toContain(`${identity.installationId} · ${identity.generation}`);
      await act(async () => {
        resolveStart(Response.json({ token: 'installation-session', operation: pendingOperation }));
      });
      await act(async () => renderer.unmount());
    }
  );

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
    // The wizard is a foreign component (#1181 owns its message state), so the
    // assertion here is only that the whole surface re-rendered in the reader's
    // script instead of keeping the previous one.
    expect(wizard).toMatch(CYRILLIC);
    expect(wizard).not.toMatch(CJK);
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
      const alert = findByTestId(renderer.root, 'bootstrap-discovery-unconfirmed');
      const [title] = blocksOf(alert, 'alert-title');
      const [desc] = blocksOf(alert, 'alert-desc');
      await act(async () => renderer.unmount());
      return { title, desc };
    };

    const traditional = await renderGate('zh-TW');
    const simplified = await renderGate('zh-CN');
    assertCopyIn('zh-TW', traditional.title);
    assertCopyIn('zh-TW', traditional.desc);
    assertCopyIn('zh-CN', simplified.title);
    assertCopyIn('zh-CN', simplified.desc);
    // Same state, same component: the only difference is the selected table, so
    // a zh-TW branch that collapsed into zh-CN could not pass this.
    expect(traditional.title).not.toBe(simplified.title);
    expect(traditional.desc).not.toBe(simplified.desc);
  });

  const isTestInstance = (node: ReactTestInstance | string): node is ReactTestInstance =>
    typeof node !== 'string';

  const elementChildren = (node: ReactTestInstance): ReactTestInstance[] =>
    node.children.filter(isTestInstance);

  // Finds the 1.x ownership guidance card purely by landmarks, live role,
  // heading/paragraph/button elements, and structural position rather than CSS
  // class names so renaming styling classes cannot break or hide assertions.
  const locateOwnershipGuidanceCard = (root: ReactTestInstance) => {
    const statusRegion: ReactTestInstance[] = [];
    const walkRole = (node: ReactTestInstance) => {
      if (typeof node.type === 'string' && node.props?.role === 'status') {
        statusRegion.push(node);
        return;
      }
      elementChildren(node).forEach(walkRole);
    };
    walkRole(root);
    expect(statusRegion).toHaveLength(1);
    const [status] = statusRegion;
    const [statusBody] = elementChildren(status);
    const [statusTitle, statusDetail] = elementChildren(statusBody);

    const [card] = root.findAll((node) => node.type === 'section');
    expect(card.props['data-testid']).toBe('bootstrap-no-handoff');
    const [header] = elementChildren(card);
    const [stepTag] = elementChildren(header);
    const [heading] = card.findAll((node) => node.type === 'h1');
    const [intro] = card.findAll((node) => node.type === 'p');

    const statusParent = status.parent;
    if (!statusParent) {
      throw new Error('status region missing parent container');
    }
    const bodyChildren = elementChildren(statusParent);
    const options = bodyChildren[1];
    const optionBlocks = elementChildren(options).map((option) => {
      const [content] = elementChildren(option);
      return elementChildren(content);
    });
    expect(optionBlocks).toHaveLength(2);
    const [[upgradeTitle, upgradeDetail], [ownershipTitle, ownershipDetail]] = optionBlocks;

    const recheck = bodyChildren.find((child) => child.type === 'button');
    if (!recheck) {
      throw new Error('recheck button missing from ownership card body');
    }

    const snapshot = (language: Language) => {
      const blocks = {
        tag: textOf(stepTag),
        heading: textOf(heading),
        intro: textOf(intro),
        title: textOf(statusTitle),
        detail: textOf(statusDetail),
        upgradeTitle: textOf(upgradeTitle),
        upgradeDetail: textOf(upgradeDetail),
        ownershipTitle: textOf(ownershipTitle),
        ownershipDetail: textOf(ownershipDetail),
        recheck: textOf(recheck),
      };
      assertLabelIn(language, blocks.tag);
      for (const block of [
        blocks.heading,
        blocks.intro,
        blocks.title,
        blocks.detail,
        blocks.upgradeTitle,
        blocks.upgradeDetail,
        blocks.ownershipTitle,
        blocks.ownershipDetail,
        blocks.recheck,
      ]) {
        assertCopyIn(language, block);
      }
      expect(blocks.heading).not.toBe(blocks.intro);
      expect(blocks.title).not.toBe(blocks.detail);
      expect(blocks.upgradeTitle).not.toBe(blocks.ownershipTitle);
      expect(blocks.upgradeDetail).not.toBe(blocks.ownershipDetail);
      return blocks;
    };

    return { options, recheck, snapshot };
  };

  it.each(['zh-CN', 'zh-TW', 'en', 'ru'] as const)(
    'renders root-owned 1.x volume guidance in %s and transitions to Upgrade entry after recheck',
    async (locale) => {
      setFullWebLanguage(locale);
      const discovery = vi
        .fn<typeof fetch>()
        .mockResolvedValueOnce(
          Response.json({
            contract_version: 'cpamp.phase4-01.discovery.v1',
            status: 'recovery_required',
            mode: 'recovery',
            auth_entry: 'host_intervention',
            reason: 'source_ownership_unavailable',
            authentication_required: false,
          })
        )
        .mockResolvedValueOnce(
          Response.json({
            contract_version: 'cpamp.phase4-01.discovery.v1',
            status: 'unsupported_upgrade',
            mode: 'unsupported_upgrade',
            auth_entry: 'admin_session',
            reason: 'qualification_unavailable',
            authentication_required: true,
          })
        );
      vi.stubGlobal('fetch', discovery);
      let renderer!: ReactTestRenderer;
      await act(async () => {
        renderer = create(
          <FreshBootstrapPage handoff={null} transport={createBootstrapTransport(vi.fn())} />
        );
      });

      const cardView = locateOwnershipGuidanceCard(renderer.root);
      const blocks = cardView.snapshot(locale);
      expect(blocks.upgradeDetail).toContain('10002:10002');
      expect(blocks.upgradeDetail).toContain('0:0');
      expect(textOf(findByTestId(renderer.root, 'legacy-upgrade-preflight-command'))).toContain(
        '--entrypoint cpa-manager-plus'
      );
      expect(textOf(findByTestId(renderer.root, 'legacy-upgrade-preflight-command'))).toContain(
        'upgrade preflight --legacy /legacy --source-version v1.14.10 --target-dir /data --json'
      );
      // Option 2 must be executable on a Docker named volume and must never
      // hard-code a single pre-release image tag.
      const ownershipCommand = textOf(findByTestId(renderer.root, 'legacy-ownership-command'));
      expect(ownershipCommand).toContain('docker stop <v2_container>');
      expect(ownershipCommand).toContain(
        `seakee/cpa-manager-plus:${__APP_VERSION__} -c 'chown -h 10002:10002 /data/usage.sqlite* /data/data.key'`
      );
      expect(ownershipCommand).toContain('docker start <v2_container>');
      expect(ownershipCommand).not.toContain('2.0.0-beta.1');
      expect(textOf(findByTestId(renderer.root, 'legacy-upgrade-preflight-command'))).toContain(
        `seakee/cpa-manager-plus:${__APP_VERSION__}`
      );
      expect(textOf(cardView.options)).toContain('/data/runtime');
      expect(renderer.root.findAllByProps({ 'data-testid': 'cli-handoff-command' })).toHaveLength(
        0
      );
      // The read-only card offers no Fresh write control in any language.
      expect(renderer.root.findAllByProps({ 'data-testid': 'admin-init-form' })).toHaveLength(0);

      await act(async () => cardView.recheck.props.onClick());
      expect(discovery).toHaveBeenCalledTimes(2);
      expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-auth-entry' })).toHaveLength(1);
      await act(async () => renderer.unmount());
    }
  );

  it('re-translates the one 1.x volume guidance card through the header switcher without refetching', async () => {
    setFullWebLanguage('zh-CN');
    const discovery = vi.fn<typeof fetch>().mockResolvedValue(
      Response.json({
        contract_version: 'cpamp.phase4-01.discovery.v1',
        status: 'recovery_required',
        mode: 'recovery',
        auth_entry: 'host_intervention',
        reason: 'source_ownership_unavailable',
        authentication_required: false,
      })
    );
    vi.stubGlobal('fetch', discovery);
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshBootstrapPage handoff={null} transport={createBootstrapTransport(vi.fn())} />
      );
    });

    const { snapshot } = locateOwnershipGuidanceCard(renderer.root);

    const switcher = findByTestId(renderer.root, 'bootstrap-language');
    const switchTo = async (language: Language) => {
      await act(async () => switcher.props.onChange({ target: { value: language } }));
      // The page's own control kept the choice and wrote the Console's store.
      expect(findByTestId(renderer.root, 'bootstrap-language').props.value).toBe(language);
      expect(JSON.parse(localStorage.getItem(FRESH_LANGUAGE_STORAGE_KEY) ?? 'null')).toMatchObject({
        state: { language },
      });
    };

    const simplified = snapshot('zh-CN');
    await switchTo('zh-TW');
    const traditional = snapshot('zh-TW');
    // Traditional is its own table, not Simplified handed back unchanged.
    expect(traditional.heading).not.toBe(simplified.heading);
    expect(traditional.intro).not.toBe(simplified.intro);
    expect(traditional.title).not.toBe(simplified.title);
    expect(traditional.detail).not.toBe(simplified.detail);
    expect(traditional.upgradeTitle).not.toBe(simplified.upgradeTitle);
    expect(traditional.upgradeDetail).not.toBe(simplified.upgradeDetail);
    expect(traditional.ownershipTitle).not.toBe(simplified.ownershipTitle);
    expect(traditional.ownershipDetail).not.toBe(simplified.ownershipDetail);
    expect(traditional.recheck).not.toBe(simplified.recheck);

    await switchTo('en');
    const english = snapshot('en');
    expect(english.title).not.toBe(traditional.title);
    expect(english.detail).not.toBe(traditional.detail);
    expect(english.upgradeTitle).not.toBe(traditional.upgradeTitle);
    expect(english.ownershipTitle).not.toBe(traditional.ownershipTitle);

    await switchTo('ru');
    const russian = snapshot('ru');
    expect(russian.heading).not.toBe(english.heading);
    expect(russian.title).not.toBe(english.title);
    expect(russian.detail).not.toBe(english.detail);

    // Four languages, one discovery answer: the payload was never re-fetched.
    expect(discovery).toHaveBeenCalledTimes(1);
    await act(async () => renderer.unmount());
  });

  it('rejects inconsistent source_ownership_unavailable payloads fail-closed', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        Response.json(
          {
            contract_version: 'cpamp.phase4-01.discovery.v1',
            status: 'recovery_required',
            mode: 'recovery',
            auth_entry: 'host_intervention',
            reason: 'source_ownership_unavailable',
            authentication_required: true,
          },
          { status: 200 }
        )
      )
    );
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <FreshBootstrapPage handoff={null} transport={createBootstrapTransport(vi.fn())} />
      );
    });
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'legacy-ownership-instructions' })
    ).toHaveLength(0);
    expect(renderer.root.findAllByProps({ 'data-testid': 'cli-handoff-command' })).toHaveLength(0);
    await act(async () => renderer.unmount());
  });
});
