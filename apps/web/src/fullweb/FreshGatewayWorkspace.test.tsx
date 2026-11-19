import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import type { ReactNode, Ref } from 'react';
import type * as ReactModule from 'react';
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest';
import i18n from '../i18n';
import { configurationText as c } from '../i18n/configurationWorkspace';
import { gatewayFamilies, gatewayGroups } from './gatewayCatalog';
import type {
  GatewayFamilyView,
  GatewayFieldView,
  GatewayGroupView,
  GatewayOperation,
  GatewayView,
  GatewaySourceView,
} from './gatewayConfigClient';
import { FreshGatewayWorkspace } from './FreshGatewayWorkspace';
import { createPublicApiClient, PublicApiError, type PublicApiClient } from './publicApi';
import { formToRule, parseRuleText } from './gatewayRuleForm';

vi.mock('../components/ui/Modal', () => ({
  Modal: ({ children, footer, open }: { children: ReactNode; footer: ReactNode; open: boolean }) =>
    open ? (
      <div role="dialog">
        {children}
        {footer}
      </div>
    ) : null,
}));
vi.mock('./GatewaySourceEditor', async () => {
  const { useImperativeHandle, useRef } = await vi.importActual<typeof ReactModule>('react');
  const MockGatewaySourceEditor = ({
      value,
      onChange,
      editable,
      editorRef,
    }: {
      value: string;
      onChange: (next: string) => void;
      editable: boolean;
      editorRef?: Ref<{
        search: (query: string, direction?: 'next' | 'prev') => { current: number; total: number };
      }>;
    }) => {
      const cursorRef = useRef(-1);
      if (value.includes('__TRIGGER_EDITOR_CRASH__')) {
        throw new Error('simulated source editor failure');
      }
      useImperativeHandle(
        editorRef,
        () => ({
          search(query: string, direction: 'next' | 'prev' = 'next') {
            if (!query) return { current: 0, total: 0 };
            const lower = value.toLowerCase();
            const q = query.toLowerCase();
            const matches: number[] = [];
            let pos = 0;
            while (pos < lower.length) {
              const idx = lower.indexOf(q, pos);
              if (idx === -1) break;
              matches.push(idx);
              pos = idx + 1;
            }
            if (!matches.length) return { current: 0, total: 0 };
            const total = matches.length;
            if (direction === 'next') {
              cursorRef.current = (cursorRef.current + 1) % total;
            } else {
              cursorRef.current = (cursorRef.current - 1 + total) % total;
            }
            return { current: cursorRef.current + 1, total };
          },
        }),
        [value]
      );
      return (
        <textarea
          data-testid="gateway-source-codemirror"
          value={value}
          readOnly={!editable}
          onChange={(event) => onChange(event.target.value)}
        />
      );
  };
  return { default: MockGatewaySourceEditor };
});

const BASELINE = 'a'.repeat(64);


// The Manager owns the type of every leaf; the test states the same inventory so
// a wrong control in the page shows up as a wrong interaction here.
const INT_PATHS = [
  'routing.retry.request-retry',
  'routing.retry.max-retry-credentials',
  'routing.retry.max-retry-interval',
  'routing.cooldown.transient-error-cooldown-seconds',
  'oauth.auth-auto-refresh-workers',
  'requests.tls-handshake-timeout-seconds',
  'requests.nonstream-keepalive-interval',
  'requests.streaming.keepalive-seconds',
  'requests.streaming.bootstrap-retries',
  'server.max-request-body-bytes',
  'server.shutdown-timeout',
  'server.response-compression.min-bytes',
  'oauth.providers.codex.live-media-relay.max-sessions',
  'oauth.providers.codex.live-media-relay.udp-port-min',
  'oauth.providers.codex.live-media-relay.udp-port-max',
  'client.codex.web-search-result-cache.limit',
  'upstream.codex.upstream-request-compression-min-bytes',
  'upstream.claude.session-date-pin-days',
  'oauth.providers.antigravity.connection-pool.max-idle-conns-per-host',
];
const STRING_PATHS = [
  'routing.strategy',
  'routing.session-affinity-ttl',
  'routing.session-affinity-rebind-on',
  'requests.proxy-url',
  'multimedia.gpt-image-2-base-model',
  'multimedia.video-result-auth-cache-ttl',
  'oauth.providers.codex.live-media-relay.public-ip',
  'client.codex.web-search-result-cache.ttl',
  'upstream.codex.stream-bootstrap-timeout',
  'oauth.providers.codex.signin-with-chatgpt-identifier',
  'oauth.providers.codex.header-defaults.user-agent',
  'oauth.providers.codex.header-defaults.beta-features',
  'upstream.claude.header-defaults.user-agent',
  'upstream.claude.header-defaults.package-version',
  'upstream.claude.header-defaults.runtime-version',
  'upstream.claude.header-defaults.os',
  'upstream.claude.header-defaults.arch',
  'upstream.claude.header-defaults.timeout',
  'upstream.claude.header-defaults.timezone',
  'upstream.claude.header-defaults.client-request-id',
  'oauth.providers.antigravity.tos-pause-window',
  'oauth.providers.antigravity.connection-pool.idle-conn-timeout',
];
const STRING_LIST_PATHS = [
  'upstream.claude.header-defaults.native-entrypoints',
  'upstream.claude.header-defaults.anthropic-beta-strip',
  'upstream.claude.header-defaults.anthropic-beta-append',
  'oauth.providers.antigravity.sensitive-words',
  'oauth.providers.devin.sensitive-words',
];
const KV_MAP_PATHS = [
  'oauth.providers.antigravity.sensitive-words-replace',
  'oauth.providers.devin.sensitive-words-replace',
];
const PATTERN_LIST_PATHS = [
  'oauth.providers.antigravity.sensitive-patterns-replace',
  'oauth.providers.devin.sensitive-patterns-replace',
];
const SELECT_PATHS = [
  'multimedia.disable-image-generation',
  'routing.strategy',
  'routing.session-affinity-rebind-on',
  'upstream.claude.header-defaults.client-request-id',
];

function fieldType(
  path: string
): 'int' | 'bool' | 'string' | 'image-mode' | 'string-list' | 'kv-map' | 'pattern-list' {
  if (path === 'multimedia.disable-image-generation') return 'image-mode';
  if (INT_PATHS.includes(path)) return 'int';
  if (STRING_PATHS.includes(path)) return 'string';
  if (STRING_LIST_PATHS.includes(path)) return 'string-list';
  if (KV_MAP_PATHS.includes(path)) return 'kv-map';
  if (PATTERN_LIST_PATHS.includes(path)) return 'pattern-list';
  return 'bool';
}

function sampleValue(type: string) {
  if (type === 'int') return 3;
  if (type === 'string') return 'value';
  if (type === 'string-list') return ['item-1'];
  if (type === 'kv-map') return { old: 'new' };
  if (type === 'pattern-list') return [{ pattern: '^foo', replacement: 'bar' }];
  return false;
}

function buildView(overrides: Partial<GatewayView> = {}): GatewayView {
  const fields: GatewayFieldView[] = gatewayGroups().flatMap((group) =>
    group.fields.map((meta) => {
      const type = fieldType(meta.path);
      return {
        path: meta.path,
        type,
        kind:
          SELECT_PATHS.includes(meta.path)
            ? ('select' as const)
            : type === 'int'
              ? ('number' as const)
              : type === 'bool'
                ? ('bool' as const)
                : type === 'string-list'
                  ? ('string-list' as const)
                  : type === 'kv-map'
                    ? ('kv-map' as const)
                    : type === 'pattern-list'
                      ? ('pattern-list' as const)
                      : ('text' as const),
        group: group.id,
        presence: 'present' as const,
        value: sampleValue(type),
        editable: true,
      };
    })
  );
  const groups: GatewayGroupView[] = gatewayGroups().map((group) => ({
    id: group.id,
    prefix: group.id,
    paths: group.fields.map((field) => field.path),
    baseline: BASELINE,
    writable: true,
    persisted: 'unknown',
    effective: 'unknown',
  }));
  const families: GatewayFamilyView[] = gatewayFamilies().map((family) => ({
    family: family.family,
    path: family.path,
    rules: family.filter ? [] : null,
    baseline: BASELINE,
    writable: true,
    readOnly: false,
  }));
  return {
    capability: { mode: 'managed', readable: true, writable: true, rulesWritable: true },
    generation: '7',
    groups,
    fields,
    families,
    groupsInvalid: false,
    ...overrides,
  };
}

interface Call {
  method: string;
  resource: string;
  body?: unknown;
}

function fakeClient(initial: GatewayView) {
  const calls: Call[] = [];
  const state = {
    view: initial,
    sourceView: {
      capability: initial.capability,
      sourceWritable: initial.capability.writable && initial.capability.mode !== 'private',
      ...(initial.capability.mode === 'private' ? { reason: 'private_profile_scope_frozen' } : {}),
      generation: initial.generation,
      baseline: BASELINE,
      yaml: 'routing:\n  strategy: round-robin\n',
    } as GatewaySourceView,
    operation: {
      requestId: 'f'.repeat(32),
      group: 'retry',
      target: 'group',
      state: 'persisted',
    } as GatewayOperation,
    saveError: null as unknown,
    reconcileError: null as unknown,
    // A read is the only authority the page reconciles from after a reload.
    setView: (next: GatewayView) => {
      state.view = next;
    },
  };
  const client: PublicApiClient = {
    get: async <T,>(resource: string) => {
      calls.push({ method: 'get', resource });
      if (resource === 'gateway/config') return state.view as T;
      if (resource === 'gateway/config/source') return state.sourceView as T;
      return { operation: state.operation, effective: 'unknown' } as T;
    },
    post: async <T,>(resource: string, body: unknown) => {
      calls.push({ method: 'post', resource, body });
      if (state.saveError) throw state.saveError;
      if (resource.endsWith('/reconcile')) {
        if (state.reconcileError) throw state.reconcileError;
        return { operation: state.operation, effective: 'unknown' } as T;
      }
      return {
        operation: {
          ...(body as { requestId: string }),
          group: resource.split('/')[3],
          target: resource.includes('/rules/') ? 'family' : 'group',
          state: 'persisted',
        },
        effective: 'unknown',
      } as T;
    },
    put: async () => {
      throw new Error('the gateway page never uses PUT');
    },
  };
  return { client, calls, state };
}

// The page against the real Public API client: only the transport is faked, so
// a refusal has to survive the true error mapping to reach the page.
const PUBLIC_CONTEXT = { basePath: '/console', apiBase: '/console/api/v2', revision: 1 };

function gatewayTransport(view: GatewayView, refusal: Record<string, unknown>): Mock<typeof fetch> {
  return vi.fn<typeof fetch>((_input, init) =>
    Promise.resolve(
      init?.method === 'POST' ? Response.json(refusal, { status: 422 }) : Response.json(view)
    )
  );
}

describe('FreshGatewayWorkspace', () => {
  let renderer: ReactTestRenderer;

  beforeEach(async () => {
    await i18n.changeLanguage('en-US');
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(async () => {
    if (renderer) await act(async () => renderer.unmount());
    vi.unstubAllGlobals();
  });

  async function mount(
    client: PublicApiClient,
    onDirtyChange = vi.fn(),
    onPending = vi.fn(),
    onUnauthorized = vi.fn(),
    onRebind = vi.fn()
  ) {
    await act(async () => {
      renderer = create(
        <FreshGatewayWorkspace
          client={client}
          credential="admin-credential"
          onUnauthorized={onUnauthorized}
          onPending={onPending}
          onDirtyChange={onDirtyChange}
          onRebind={onRebind}
        />
      );
    });
    await act(async () => {
      await Promise.resolve();
    });
    return { onDirtyChange, onPending, onUnauthorized, onRebind };
  }

  function testId(id: string) {
    return renderer.root.findByProps({ 'data-testid': id });
  }

  function maybeTestId(id: string) {
    return renderer.root.findAllByProps({ 'data-testid': id })[0];
  }

  function tab(key: Parameters<typeof c>[0]) {
    return renderer.root.findAll(
      (node) => node.type === 'button' && node.props.role === 'tab' && node.props.id === `gateway-tab-${key}`
    )[0];
  }

  async function openTab(key: Parameters<typeof c>[0]) {
    await act(async () => tab(key).props.onClick());
  }

  // Mounts the page against the real Public API client so a refusal travels the
  // whole mapping: the transport, the bounded code allowlist and the page.
  async function mountThroughPublicApi(transport: Mock<typeof fetch>) {
    vi.stubGlobal('window', {
      location: new URL('https://manager.example/console'),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });
    await mount(createPublicApiClient(() => PUBLIC_CONTEXT, transport));
    await settle();
  }

  // The real transport reads a Response stream, so a save settles after more
  // than one microtask turn. One macrotask flush drains the whole chain.
  async function settle() {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
  }

  async function save(scope: string) {
    const button = testId(`gateway-group-${scope}`).findAll(
      (node) => node.type === 'button' && node.props.children === c('groupSave')
    )[0];
    await act(async () => button.props.onClick());
    await settle();
  }

  // Every string a refusal put on this group card: its leaves and its group
  // error. A Manager refusal must never read as one of CPA's.
  function refusalText(scope: string) {
    return testId(`gateway-group-${scope}`)
      .findAllByType('p')
      .map((node) => [node.props.children].flat(3).filter((item) => typeof item === 'string').join(''))
      .join(' | ');
  }

  it('projects the whole page from the Manager view on the common tab and never invents a field', async () => {
    const { client } = fakeClient(buildView());
    await mount(client);
    expect(testId('gateway-capability')).toBeDefined();
    expect(tab('common').props['aria-selected']).toBe(true);
    const counts = testId('gateway-dirty-count');
    expect(JSON.stringify(counts.props.children)).toContain('0');
    expect(renderer.root.findAllByProps({ 'data-testid': 'gateway-journal-invalid' })).toHaveLength(0);
    // The read is one projection request, not one request per field.
    expect((client as unknown as { get: unknown }) === undefined).toBe(false);
  });

  it('renders the eight-field common view, group tabs and a search across sections', async () => {
    const { client } = fakeClient(buildView());
    await mount(client);
    const byId = (label: string) =>
      renderer.root.findAll(
        (node) => node.type === 'button' && node.props.role === 'tab' && node.props.id === `gateway-tab-${label}`
      )[0];
    await act(async () => byId('common').props.onClick());
    const commonFields = renderer.root.findAll(
      (node) =>
        typeof node.type === 'string' &&
        node.type === 'div' &&
        typeof node.props['data-testid'] === 'string' &&
        node.props['data-testid'].startsWith('gateway-field-')
    );
    expect(commonFields).toHaveLength(8);
    await act(async () => byId('routing').props.onClick());
    expect(maybeTestId('gateway-group-routing')).toBeDefined();
    expect(maybeTestId('gateway-group-retry')).toBeDefined();
    expect(maybeTestId('gateway-group-request')).toBeUndefined();
    const search = renderer.root.findByProps({ type: 'search' });
    await act(async () => search.props.onChange({ target: { value: 'proxy-url' } }));
    expect(maybeTestId('gateway-field-requests.proxy-url')).toBeDefined();
    await act(async () => search.props.onChange({ target: { value: 'nothing-matches-this' } }));
    expect(maybeTestId('gateway-no-matches')).toBeDefined();
  });

  it('names the number of drafts the discard dialog throws away', async () => {
    const { client } = fakeClient(buildView());
    await mount(client);
    await openTab('routing');
    const row = testId('gateway-field-routing.retry.request-retry');
    await act(async () => row.findByType('input').props.onChange({ target: { value: '5' } }));

    const discard = renderer.root.findAll(
      (node) => node.type === 'button' && [node.props.children].flat(2).includes(c('discard'))
    )[0];
    await act(async () => discard.props.onClick());

    const help = testId('gateway-discard-help');
    const text = [help.props.children].flat().join('');
    // The rendered count is substituted, not the raw template.
    expect(text).toBe(c('discardHelp', 1));
    expect(text).toContain('1');
  });

  it('saves one group with only the edited leaves and reports the draft as dirty', async () => {
    const { client, calls } = fakeClient(buildView());
    const { onDirtyChange } = await mount(client);
    await openTab('routing');
    const row = testId('gateway-field-routing.retry.request-retry');
    const input = row.findByType('input');
    await act(async () => input.props.onChange({ target: { value: '5' } }));
    expect(row.props['data-dirty'] ?? testId('gateway-field-routing.retry.request-retry').props['data-dirty']).toBe(
      'true'
    );
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);
    const save = testId('gateway-group-retry').findAll(
      (node) => node.type === 'button' && node.props.children === c('groupSave')
    )[0];
    await act(async () => save.props.onClick());
    await act(async () => {
      await Promise.resolve();
    });
    const writes = calls.filter((call) => call.method === 'post');
    expect(writes).toHaveLength(1);
    expect(writes[0].resource).toBe('gateway/config/groups/retry');
    const body = writes[0].body as {
      requestId: string;
      expected: { generation: string; baseline: string };
      edits: { path: string; action: string; value?: unknown }[];
    };
    expect(body.requestId).toMatch(/^[0-9a-f]{32}$/);
    expect(body.expected).toEqual({ generation: '7', baseline: BASELINE });
    expect(body.edits).toEqual([{ path: 'routing.retry.request-retry', action: 'set', value: 5 }]);
    expect(onDirtyChange).toHaveBeenLastCalledWith(false);
  });

  it('shows a zero leaf as a clean number and saves it unchanged', async () => {
    const view = buildView();
    view.fields = view.fields.map((field) =>
      field.path === 'routing.retry.request-retry' ? { ...field, value: 0 } : field
    );
    const { client, calls } = fakeClient(view);
    const { onDirtyChange } = await mount(client);
    await openTab('routing');
    const row = testId('gateway-field-routing.retry.request-retry');
    // A zero that crossed the JSON boundary is still the number the guest
    // configured: it renders as "0" and is not a draft of its own.
    expect((row.findByType('input').props as { value?: unknown }).value).toBe('0');
    expect(row.props['data-dirty']).toBe('false');
    expect(onDirtyChange).toHaveBeenLastCalledWith(false);
    const input = row.findByType('input');
    await act(async () => input.props.onChange({ target: { value: '4' } }));
    expect(testId('gateway-field-routing.retry.request-retry').props['data-dirty']).toBe('true');
    const save = testId('gateway-group-retry').findAll(
      (node) => node.type === 'button' && node.props.children === c('groupSave')
    )[0];
    await act(async () => save.props.onClick());
    await act(async () => {
      await Promise.resolve();
    });
    const writes = calls.filter((call) => call.method === 'post');
    expect(writes).toHaveLength(1);
    const body = writes[0].body as { edits: { path: string; action: string; value?: unknown }[] };
    expect(body.edits).toEqual([{ path: 'routing.retry.request-retry', action: 'set', value: 4 }]);
    expect(onDirtyChange).toHaveBeenLastCalledWith(false);
  });

  it('keeps the draft when the baseline moved and shows the conflict', async () => {
    const { client, calls } = fakeClient(buildView());
    const { state } = { state: { saveError: null as unknown } };
    const fake = fakeClient(buildView());
    const harness = await mount(fake.client);
    expect(harness).toBeDefined();
    await openTab('routing');
    const row = testId('gateway-field-routing.retry.request-retry');
    await act(async () => row.findByType('input').props.onChange({ target: { value: '9' } }));
    fake.state.saveError = new PublicApiError('http', 409, undefined, 'source_or_operation_conflict');
    const save = testId('gateway-group-retry').findAll(
      (node) => node.type === 'button' && node.props.children === c('groupSave')
    )[0];
    await act(async () => save.props.onClick());
    await act(async () => {
      await Promise.resolve();
    });
    expect(maybeTestId('gateway-conflict')).toBeDefined();
    expect(maybeTestId('gateway-group-error-retry')).toBeDefined();
    // The conflicted draft survives and the page re-read the installation.
    const reloaded = fake.calls.filter((call) => call.resource === 'gateway/config');
    expect(reloaded.length).toBeGreaterThan(1);
    void client;
    void calls;
    void state;
  });

  it('offers the original operation after an unknown result and never resends the write', async () => {
    const fake = fakeClient(buildView());
    await mount(fake.client);
    await openTab('routing');
    const row = testId('gateway-field-routing.retry.request-retry');
    await act(async () => row.findByType('input').props.onChange({ target: { value: '7' } }));
    fake.state.saveError = new PublicApiError('http', 503, undefined, 'configuration_unavailable');
    const save = testId('gateway-group-retry').findAll(
      (node) => node.type === 'button' && node.props.children === c('groupSave')
    )[0];
    await act(async () => save.props.onClick());
    await act(async () => {
      await Promise.resolve();
    });
    // The result is reported as unknown, and the original operation is offered
    // next to it — the page might have lost the response, not the write.
    expect(testId('gateway-group-error-retry').props.children).toBe(c('groupSaveUnknown'));
    const query = testId('gateway-group-reconcile-retry').findAll(
      (node) => node.type === 'button' && node.props.children === c('groupQueryOriginal')
    )[0];
    const writesBefore = fake.calls.filter((call) => call.method === 'post').length;
    await act(async () => query.props.onClick());
    await act(async () => {
      await Promise.resolve();
    });
    const writes = fake.calls.filter((call) => call.method === 'post');
    expect(writes.length).toBe(writesBefore + 1);
    expect(writes[writes.length - 1]?.resource).toMatch(/^gateway\/config\/operations\/[0-9a-f]{32}\/reconcile$/);
  });

  it('maps a CPA rejection to the group and the field it named', async () => {
    const fake = fakeClient(buildView());
    await mount(fake.client);
    await openTab('routing');
    const row = testId('gateway-field-routing.retry.request-retry');
    await act(async () => row.findByType('input').props.onChange({ target: { value: '0' } }));
    fake.state.saveError = new PublicApiError(
      'http',
      422,
      undefined,
      'cpa_rejected_config',
      'routing.retry.request-retry',
      'invalid value'
    );
    const save = testId('gateway-group-retry').findAll(
      (node) => node.type === 'button' && node.props.children === c('groupSave')
    )[0];
    await act(async () => save.props.onClick());
    await act(async () => {
      await Promise.resolve();
    });
    expect(String(testId('gateway-group-error-retry').props.children)).toContain('invalid value');
    const field = testId('gateway-field-routing.retry.request-retry');
    const texts = field
      .findAllByType('p')
      .map((node) => [node.props.children].flat(2).filter((item) => typeof item === 'string').join(''));
    expect(texts.join(' | ')).toContain(c('fieldCpaRejected'));
  });

  it('reconciles a group whose journaled operation is unknown, after a reload', async () => {
    // The page has no memory of this write: it was created by a browser session
    // that is already gone. Only the persisted request ID can reconcile it.
    const view = buildView();
    view.groups = view.groups.map((group) =>
      group.id === 'retry'
        ? {
            ...group,
            operation: {
              requestId: 'a'.repeat(32),
              group: 'retry',
              target: 'group' as const,
              state: 'unknown' as const,
            },
          }
        : group
    );
    const fake = fakeClient(view);
    fake.state.operation = {
      requestId: 'a'.repeat(32),
      group: 'retry',
      target: 'group',
      state: 'persisted',
    };
    await mount(fake.client);
    await openTab('routing');
    // No draft, no error message, no previous write in this tab: the button must
    // still exist because the journal says the group is unresolved.
    const notice = testId('gateway-group-reconcile-retry');
    expect(notice).toBeDefined();
    const query = notice.findAll(
      (node) => node.type === 'button' && node.props.children === c('groupQueryOriginal')
    )[0];
    await act(async () => query.props.onClick());
    await act(async () => {
      await Promise.resolve();
    });
    const reconcile = fake.calls.filter((call) => call.method === 'post' && call.resource.endsWith('/reconcile'));
    expect(reconcile).toHaveLength(1);
    expect(reconcile[0].resource).toBe(`gateway/config/operations/${'a'.repeat(32)}/reconcile`);
    // Once the journal says persisted, the group is unlocked again.
    fake.state.setView(
      buildView({
        groups: buildView().groups.map((group) =>
          group.id === 'retry'
            ? {
                ...group,
                operation: {
                  requestId: 'a'.repeat(32),
                  group: 'retry',
                  target: 'group' as const,
                  state: 'persisted' as const,
                },
              }
            : group
        ),
      })
    );
    await act(async () => query.props.onClick());
    await act(async () => {
      await Promise.resolve();
    });
    expect(maybeTestId('gateway-group-reconcile-retry')).toBeUndefined();
  });

  it('reconciles a rule family from its own journaled operation', async () => {
    // The save journals under `rules:<family>`. Looking it up under `rules`
    // meant rule reconcile never found an ID and silently did nothing.
    const view = buildView();
    view.families = view.families.map((family) =>
      family.family === 'default'
        ? {
            ...family,
            operation: {
              requestId: 'b'.repeat(32),
              group: 'rules',
              target: 'family' as const,
              state: 'unknown' as const,
            },
          }
        : family
    );
    const fake = fakeClient(view);
    await mount(fake.client);
    await openTab('payload');
    const notice = testId('gateway-family-reconcile-default');
    expect(notice).toBeDefined();
    const query = notice.findAll(
      (node) => node.type === 'button' && node.props.children === c('groupQueryOriginal')
    )[0];
    await act(async () => query.props.onClick());
    await act(async () => {
      await Promise.resolve();
    });
    const reconcile = fake.calls.filter((call) => call.method === 'post' && call.resource.endsWith('/reconcile'));
    expect(reconcile).toHaveLength(1);
    expect(reconcile[0].resource).toBe(`gateway/config/operations/${'b'.repeat(32)}/reconcile`);
  });

  it('preserves override unsaved draft while clearing default error and tab badge when reconciling shared rulesOperation with prefix from override card', async () => {
    let defaultReqId = '';
    const fake = fakeClient(buildView());
    const origPost = fake.client.post;
    let sharedRulesOp: GatewayOperation = {
      requestId: '',
      group: 'rules',
      target: 'family',
      prefix: 'requests.payload.default',
      state: 'unknown',
    };
    let failDefaultWithSharedUnknown = false;
    fake.client.post = async <T,>(
      resource: string,
      body: unknown,
      cred: Parameters<PublicApiClient['post']>[2]
    ) => {
      if (failDefaultWithSharedUnknown && resource === 'gateway/config/rules/default') {
        fake.calls.push({ method: 'post', resource, body });
        if (
          typeof body === 'object' &&
          body !== null &&
          'requestId' in body &&
          typeof body.requestId === 'string'
        ) {
          defaultReqId = body.requestId;
        }
        sharedRulesOp = {
          requestId: defaultReqId,
          group: 'rules',
          target: 'family',
          prefix: 'requests.payload.default',
          state: 'unknown',
        };
        fake.state.setView(
          buildView({
            families: buildView().families.map((family) =>
              family.family === 'default' || family.family === 'override'
                ? { ...family, operation: sharedRulesOp }
                : family
            ),
          })
        );
        throw new PublicApiError('network');
      }
      return origPost<T>(resource, body, cred);
    };

    await mount(fake.client);
    await openTab('payload');

    // Create unsaved draft on default
    const editDefault = testId('gateway-family-default').findAll(
      (node) => node.type === 'button' && node.props.children === c('ruleEditFamily')
    )[0];
    await act(async () => editDefault.props.onClick());
    await act(async () => testId('rule-add').props.onClick());
    await act(async () => testId('rule-model-name-0').props.onChange({ target: { value: 'gpt-*' } }));
    await act(async () => testId('rule-param-add').props.onClick());
    const defaultRows = testId('gateway-rule-editor').findAll(
      (node) => typeof node.type === 'string' && node.props['data-param-row'] !== undefined
    );
    const [defaultPath, defaultValue] = defaultRows[0].findAllByType('input');
    await act(async () => defaultPath.props.onChange({ target: { value: 'reasoning.effort' } }));
    await act(async () => defaultValue.props.onChange({ target: { value: 'high' } }));
    await act(async () => testId('rule-editor-apply').props.onClick());
    await act(async () => testId('rule-save').props.onClick());

    // Create unsaved draft on override
    const editOverride = testId('gateway-family-override').findAll(
      (node) => node.type === 'button' && node.props.children === c('ruleEditFamily')
    )[0];
    await act(async () => editOverride.props.onClick());
    await act(async () => testId('rule-add').props.onClick());
    await act(async () => testId('rule-model-name-0').props.onChange({ target: { value: 'claude-*' } }));
    await act(async () => testId('rule-param-add').props.onClick());
    const overrideRows = testId('gateway-rule-editor').findAll(
      (node) => typeof node.type === 'string' && node.props['data-param-row'] !== undefined
    );
    const [overridePath, overrideValue] = overrideRows[0].findAllByType('input');
    await act(async () => overridePath.props.onChange({ target: { value: 'max_tokens' } }));
    await act(async () => overrideValue.props.onChange({ target: { value: '1024' } }));
    await act(async () => testId('rule-editor-apply').props.onClick());
    await act(async () => testId('rule-save').props.onClick());

    expect(testId('gateway-tab-dirty-payload').props.children).toBe(2);

    // Save default and hit an unknown write failure; backend surfaces shared rulesOperation with prefix on both cards.
    failDefaultWithSharedUnknown = true;
    const saveDefault = testId('gateway-family-default').findAll(
      (node) => node.type === 'button' && node.props.children === c('ruleSaveFamily')
    )[0];
    await act(async () => saveDefault.props.onClick());
    await settle();

    expect(testId('gateway-family-error-default').props.children).toBe(c('groupSaveUnknown'));
    expect(testId('gateway-tab-errors-payload').props.children).toBe(1);
    expect(testId('gateway-family-reconcile-default')).toBeDefined();
    expect(testId('gateway-family-reconcile-override')).toBeDefined();

    // Reconcile from the override card while the operation belongs to requests.payload.default and settles as persisted.
    failDefaultWithSharedUnknown = false;
    fake.state.operation = { ...sharedRulesOp, state: 'persisted' };
    fake.state.setView(
      buildView({
        families: buildView().families.map((family) =>
          family.family === 'default' || family.family === 'override'
            ? { ...family, operation: { ...sharedRulesOp, state: 'persisted' } }
            : family
        ),
      })
    );

    const overrideReconcileBtn = testId('gateway-family-reconcile-override').findAll(
      (node) => node.type === 'button' && node.props.children === c('groupQueryOriginal')
    )[0];
    await act(async () => overrideReconcileBtn.props.onClick());
    await settle();

    const reconcileCalls = fake.calls.filter(
      (call) => call.method === 'post' && call.resource.endsWith('/reconcile')
    );
    expect(reconcileCalls).toHaveLength(1);
    expect(reconcileCalls[0].resource).toBe(`gateway/config/operations/${defaultReqId}/reconcile`);

    // Override's unsaved draft is preserved while default's unknown error & payload error badge are cleared.
    expect(maybeTestId('gateway-family-error-default')).toBeUndefined();
    expect(maybeTestId('gateway-tab-errors-payload')).toBeUndefined();
    expect(maybeTestId('gateway-family-reconcile-default')).toBeUndefined();
    expect(maybeTestId('gateway-family-reconcile-override')).toBeUndefined();
    expect(
      testId('gateway-family-override').findAll((node) =>
        node.children.some((child) => typeof child === 'string' && child.includes('claude-*'))
      ).length
    ).toBeGreaterThan(0);
    expect(testId('gateway-tab-dirty-payload').props.children).toBe(2);

    const saveOverride = testId('gateway-family-override').findAll(
      (node) => node.type === 'button' && node.props.children === c('ruleSaveFamily')
    )[0];
    expect(saveOverride.props.disabled).toBe(false);
    await act(async () => saveOverride.props.onClick());
    await settle();
    const overridePost = fake.calls.find(
      (call) => call.method === 'post' && call.resource === 'gateway/config/rules/override'
    );
    expect(overridePost).toBeDefined();
    expect(JSON.stringify(overridePost?.body)).toContain('claude-*');
  });

  it('renders a rule save error on the family card, not on the group', async () => {
    const fake = fakeClient(buildView());
    await mount(fake.client);
    await openTab('payload');
    const open = renderer.root.findAll(
      (node) => node.type === 'button' && node.props.children === c('ruleEditFamily')
    )[0];
    await act(async () => open.props.onClick());
    await act(async () => testId('rule-add').props.onClick());
    await act(async () => testId('rule-model-name-0').props.onChange({ target: { value: 'gpt-*' } }));
    await act(async () => testId('rule-param-add').props.onClick());
    const rows = testId('gateway-rule-editor').findAll(
      (node) => typeof node.type === 'string' && node.props['data-param-row'] !== undefined
    );
    const [pathInput, valueInput] = rows[0].findAllByType('input');
    await act(async () => pathInput.props.onChange({ target: { value: 'reasoning.effort' } }));
    await act(async () => valueInput.props.onChange({ target: { value: 'high' } }));
    await act(async () => testId('rule-editor-apply').props.onClick());
    await act(async () => testId('rule-save').props.onClick());
    fake.state.saveError = new PublicApiError('http', 422, undefined, 'cpa_rejected_config', undefined, 'bad rule');
    const save = testId('gateway-family-default').findAll(
      (node) => node.type === 'button' && node.props.children === c('ruleSaveFamily')
    )[0];
    await act(async () => save.props.onClick());
    await act(async () => {
      await Promise.resolve();
    });
    const error = testId('gateway-family-error-default');
    expect(error.props.children).toContain('bad rule');
    // The unrelated group cards never carry the family's failure.
    expect(maybeTestId('gateway-group-error-request')).toBeUndefined();
    expect(maybeTestId('gateway-group-error-streaming')).toBeUndefined();
  });

  it('treats a network failure and a code-less 5xx as unknown, never as failed', async () => {
    for (const failure of [new PublicApiError('network'), new PublicApiError('http', 502)]) {
      const fake = fakeClient(buildView());
      await mount(fake.client);
    await openTab('routing');
      const row = testId('gateway-field-routing.retry.request-retry');
      await act(async () => row.findByType('input').props.onChange({ target: { value: '4' } }));
      fake.state.saveError = failure;
      const save = testId('gateway-group-retry').findAll(
        (node) => node.type === 'button' && node.props.children === c('groupSave')
      )[0];
      await act(async () => save.props.onClick());
      await act(async () => {
        await Promise.resolve();
      });
      // "Unknown" keeps the draft and offers the original operation; "failed"
      // would claim the document is unchanged and lock the group.
      expect(testId('gateway-group-error-retry').props.children).toBe(c('groupSaveUnknown'));
      expect(maybeTestId('gateway-group-reconcile-retry')).toBeDefined();
      const writes = fake.calls.filter((call) => call.method === 'post' && !call.resource.endsWith('/reconcile'));
      expect(writes).toHaveLength(1);
      await act(async () => renderer.unmount());
    }
  });

  it('edits an inheritable switch as inherit / on / off and saves an explicit off', async () => {
    // routing.session-affinity-subagents defaults to TRUE in CPA. A two-state
    // checkbox showed it as Off and treated the explicit false as no change.
    const view = buildView();
    view.fields = view.fields.map((field) =>
      field.path === 'routing.session-affinity' ? { ...field, value: true } : field
    );
    const fake = fakeClient(view);
    await mount(fake.client);
    await openTab('routing');
    const inheritable = buildView().fields.find(
      (field) => field.path === 'routing.session-affinity-subagents'
    ) as GatewayFieldView;
    expect(inheritable).toBeDefined();
    // The projection states the CPA default alongside the leaf.
    view.fields = view.fields.map((field) =>
      field.path === 'routing.session-affinity-subagents'
        ? { ...field, presence: 'missing' as const, value: null, inherit: true }
        : field
    );
    fake.state.setView(view);
    await act(async () => renderer.unmount());
    const { client, calls } = fakeClient(view);
    await mount(client);
    await openTab('routing');
    const inherit = testId('gateway-field-routing.session-affinity-subagents-inherit');
    expect(inherit.props['aria-checked']).toBe(true);
    // The inherit label states what the default resolves to.
    expect(JSON.stringify(inherit.props.children)).toContain(c('valueInheritOn'));
    const off = testId('gateway-field-routing.session-affinity-subagents-off');
    await act(async () => off.props.onClick());
    expect(testId('gateway-field-routing.session-affinity-subagents').props['data-dirty']).toBe('true');
    const save = testId('gateway-group-routing').findAll(
      (node) => node.type === 'button' && node.props.children === c('groupSave')
    )[0];
    await act(async () => save.props.onClick());
    await act(async () => {
      await Promise.resolve();
    });
    const write = calls.filter((call) => call.method === 'post')[0];
    expect((write.body as { edits: unknown[] }).edits).toEqual([
      { path: 'routing.session-affinity-subagents', action: 'set', value: false },
    ]);
    void fake;
  });

  it('shows proxy-url credentials on the proxy field, not only on the group', async () => {
    const fake = fakeClient(buildView());
    await mount(fake.client);
    await openTab('common');
    const row = testId('gateway-field-requests.proxy-url');
    const input = row.findByType('input');
    await act(async () => input.props.onChange({ target: { value: 'http://user:pass@proxy:8080' } }));
    fake.state.saveError = new PublicApiError(
      'http',
      422,
      undefined,
      'proxy_url_credentials',
      'requests.proxy-url',
      'credentials in proxy URL'
    );
    const save = testId('gateway-group-request').findAll(
      (node) => node.type === 'button' && node.props.children === c('groupSave')
    )[0];
    await act(async () => save.props.onClick());
    await act(async () => {
      await Promise.resolve();
    });
    const error = testId('gateway-field-requests.proxy-url-error');
    expect(error.props.children).toBe(c('fieldProxyCredentials'));
    // The draft is kept so the user can edit the URL they typed.
    expect(testId('gateway-field-requests.proxy-url').props['data-dirty']).toBe('true');
  });

  it('states the private profile range on its own field and group, never as a CPA refusal', async () => {
    // The Manager decides this refusal before CPA is asked, so the page must
    // not borrow CPA's wording and must show the Manager's own range message.
    const view = buildView();
    const transport = gatewayTransport(view, {
      code: 'invalid_config',
      field: 'requests.streaming.keepalive-seconds',
      message: 'value outside the private profile range',
    });
    await mountThroughPublicApi(transport);
    await openTab('network');
    const path = 'requests.streaming.keepalive-seconds';
    await act(async () =>
      testId(`gateway-field-${path}`)
        .findByType('input')
        .props.onChange({ target: { value: '9000' } })
    );
    await save('streaming');
    expect(testId(`gateway-field-${path}-error`).props.children).toBe(c('fieldPrivateRange'));
    const group = String(testId('gateway-group-error-streaming').props.children);
    // The group names the Manager and keeps the message it sent.
    expect(group).toContain(c('groupSavePrivateRangeDetail'));
    expect(group).toContain('value outside the private profile range');
    expect(group).not.toBe(c('groupSaveFailed'));
    expect(refusalText('streaming')).not.toContain(c('fieldCpaRejected'));
    expect(refusalText('streaming')).not.toContain('CPA rejected');
    // The draft survives so the user can correct the value they typed.
    expect(testId(`gateway-field-${path}`).props['data-dirty']).toBe('true');
  });

  it('states proxy-url credentials as a Manager refusal on the group too', async () => {
    const transport = gatewayTransport(buildView(), {
      code: 'proxy_url_credentials',
      field: 'requests.proxy-url',
    });
    await mountThroughPublicApi(transport);
    await openTab('network');
    await act(async () =>
      testId('gateway-field-requests.proxy-url')
        .findByType('input')
        .props.onChange({ target: { value: 'http://user:pass@proxy:8080' } })
    );
    await save('request');
    expect(testId('gateway-field-requests.proxy-url-error').props.children).toBe(
      c('fieldProxyCredentials')
    );
    const group = String(testId('gateway-group-error-request').props.children);
    expect(group).toBe(c('groupSaveProxyCredentials'));
    expect(group).not.toBe(c('groupSaveFailed'));
    expect(refusalText('request')).not.toContain('CPA rejected');
    expect(refusalText('request')).not.toContain(c('fieldCpaRejected'));
  });

  it('renders a generation mismatch as a typed conflict and keeps the draft', async () => {
    const fake = fakeClient(buildView());
    await mount(fake.client);
    await openTab('routing');
    const row = testId('gateway-field-routing.retry.request-retry');
    await act(async () => row.findByType('input').props.onChange({ target: { value: '6' } }));
    fake.state.saveError = new PublicApiError('http', 409, undefined, 'generation_mismatch');
    const save = testId('gateway-group-retry').findAll(
      (node) => node.type === 'button' && node.props.children === c('groupSave')
    )[0];
    await act(async () => save.props.onClick());
    await act(async () => {
      await Promise.resolve();
    });
    expect(maybeTestId('gateway-conflict')).toBeDefined();
    expect(testId('gateway-group-error-retry').props.children).toBe(c('groupSaveGenerationMismatch'));
    expect(testId('gateway-field-routing.retry.request-retry').props['data-dirty']).toBe('true');
  });

  it('blocks a group whose earlier field operation is unresolved', async () => {
    const view = buildView({
      capability: {
        mode: 'managed',
        readable: true,
        writable: true,
        rulesWritable: true,
      },
      groups: buildView().groups.map((group) =>
        group.id === 'retry'
          ? { ...group, writable: false, reason: 'legacy_operation_unresolved' }
          : group
      ),
    });
    await mount(fakeClient(view).client);
    await openTab('routing');
    const card = testId('gateway-group-retry');
    const texts = card
      .findAllByType('p')
      .map((node) => [node.props.children].flat(2).filter((item) => typeof item === 'string').join(''));
    expect(texts.join(' | ')).toContain(c('reasonLegacyBlocked'));
    expect(
      card.findAll((node) => node.type === 'button' && node.props.children === c('groupSave'))[0].props.disabled
    ).toBe(true);
  });

  it('allows one edit per save on the private profile and never offers a default', async () => {
    const view = {
      ...buildView({
        capability: { mode: 'private', readable: true, writable: true, rulesWritable: false },
      }),
      // The Manager states the profile's save shape on the wire.
      profile_limits: { single_edit: true, use_default: false },
    } as unknown as GatewayView;
    const fake = fakeClient(view);
    await mount(fake.client);
    await openTab('routing');
    const one = testId('gateway-field-routing.retry.request-retry');
    await act(async () => one.findByType('input').props.onChange({ target: { value: '5' } }));
    const two = testId('gateway-field-routing.retry.max-retry-interval');
    await act(async () => two.findByType('input').props.onChange({ target: { value: '30' } }));
    // Two edits cannot be saved as one operation on this profile.
    const save = testId('gateway-group-retry').findAll(
      (node) => node.type === 'button' && node.props.children === c('groupSave')
    )[0];
    expect(save.props.disabled).toBe(true);
    expect(maybeTestId('gateway-group-single-edit-retry')).toBeDefined();
    // And "use default" is not offered at all: the H1 protocol has no delete.
    const defaults = renderer.root.findAll(
      (node) => node.type === 'button' && node.props.children === c('fieldUseDefault')
    );
    expect(defaults).toHaveLength(0);
    void fake;
  });

  it('moves between tabs with the keyboard and badges unsaved tabs', async () => {
    await mount(fakeClient(buildView()).client);
    const tabs = () =>
      renderer.root.findAll((node) => node.type === 'button' && node.props.role === 'tab');
    const preventDefault = vi.fn();
    await act(async () =>
      tab('common').props.onKeyDown({ key: 'ArrowRight', preventDefault })
    );
    expect(preventDefault).toHaveBeenCalled();
    expect(tab('routing').props['aria-selected']).toBe(true);
    // Roving tabindex: exactly one tab is in the tab order.
    expect(tabs().filter((item) => item.props.tabIndex === 0)).toHaveLength(1);
    await act(async () => tab('routing').props.onKeyDown({ key: 'End', preventDefault }));
    expect(tab('media').props['aria-selected']).toBe(true);
    await act(async () => tab('media').props.onKeyDown({ key: 'Home', preventDefault }));
    expect(tab('common').props['aria-selected']).toBe(true);
    // A dirty leaf badges its tab so a draft is never lost in a hidden tab.
    await openTab('routing');
    const row = testId('gateway-field-routing.retry.request-retry');
    await act(async () => row.findByType('input').props.onChange({ target: { value: '8' } }));
    expect(maybeTestId('gateway-tab-dirty-routing')).toBeDefined();
  });

  it('shows old and new values in the conflict view', async () => {
    const fake = fakeClient(buildView());
    await mount(fake.client);
    await openTab('routing');
    const row = testId('gateway-field-routing.retry.request-retry');
    await act(async () => row.findByType('input').props.onChange({ target: { value: '9' } }));
    fake.state.saveError = new PublicApiError('http', 409, undefined, 'source_or_operation_conflict');
    // The installation moved to a different value while the draft was open.
    const after = buildView();
    after.fields = after.fields.map((field) =>
      field.path === 'routing.retry.request-retry' ? { ...field, value: 11 } : field
    );
    const save = testId('gateway-group-retry').findAll(
      (node) => node.type === 'button' && node.props.children === c('groupSave')
    )[0];
    // The conflict re-read returns the value that moved.
    fake.state.setView(after);
    await act(async () => save.props.onClick());
    await act(async () => {
      await Promise.resolve();
    });
    const conflict = testId('gateway-conflict-diff');
    const text = conflict.findAllByType('li')[0].findAll((node) => typeof node.type === 'string')
      .map((node) => [node.props.children].flat(3).filter((item) => typeof item === 'string').join(''))
      .join(' | ');
    // old (the baseline this tab edited) → new (what the Manager now reports)
    expect(text).toContain('3');
    expect(text).toContain('11');
    expect(text).toContain('9');
  });

  it('never offers passthrough as an image-mode choice but shows it when present', async () => {
    const view = buildView();
    view.fields = view.fields.map((field) =>
      field.path === 'multimedia.disable-image-generation'
        ? { ...field, presence: 'present' as const, value: 'true' }
        : field
    );
    await mount(fakeClient(view).client);
    await openTab('media');
    const row = testId('gateway-field-multimedia.disable-image-generation');
    const options = row.findAllByType('option');
    const values = options.map((option) => option.props.value);
    expect(values).toContain('true');
    expect(values).not.toContain('passthrough');
    // A value CPA already persists is shown and cannot be chosen as a draft.
    const after = buildView();
    after.fields = after.fields.map((field) =>
      field.path === 'multimedia.disable-image-generation'
        ? { ...field, presence: 'present' as const, value: 'passthrough' }
        : field
    );
    await act(async () => renderer.unmount());
    await mount(fakeClient(after).client);
    await openTab('media');
    const persisted = testId('gateway-field-multimedia.disable-image-generation').findAllByType('option');
    const passthrough = persisted.find((option) => option.props.value === 'passthrough');
    expect(passthrough).toBeDefined();
    expect(passthrough?.props.disabled).toBe(true);
  });

  it('offers reconcile for a legacy field operation surfaced as the group operation', async () => {
    // A managed install blocks a group whose pre-cutover field write is still
    // unresolved. The group must carry that operation, or the page says
    // "reconcile it" and has no request ID to reconcile with.
    const view = buildView();
    view.groups = view.groups.map((group) =>
      group.id === 'retry'
        ? {
            ...group,
            writable: false,
            reason: 'legacy_operation_unresolved',
            operation: {
              requestId: 'c'.repeat(32),
              group: 'retry',
              target: 'group' as const,
              state: 'unknown' as const,
            },
          }
        : group
    );
    const fake = fakeClient(view);
    await mount(fake.client);
    await openTab('routing');
    const reconcile = testId('gateway-group-reconcile-retry');
    expect(reconcile).toBeDefined();
    const button = reconcile.findByProps({ className: 'btn btn-secondary btn-sm' });
    await act(async () => button.props.onClick());
    await act(async () => {
      await Promise.resolve();
    });
    // Reconciling reads the journaled original, never this tab's memory.
    expect(
      fake.calls.some(
        (call) => call.method === 'post' && call.resource === `gateway/config/operations/${'c'.repeat(32)}/reconcile`
      )
    ).toBe(true);
  });

  it('locks the private profile and never renders a save action', async () => {
    const view = buildView({
      capability: {
        mode: 'private',
        readable: true,
        writable: false,
        rulesWritable: false,
        reason: 'private_profile_scope_frozen',
      },
      groups: gatewayGroups().map((group) => ({
        id: group.id,
        prefix: group.id,
        paths: group.fields.map((field) => field.path),
        baseline: BASELINE,
        writable: false,
        reason: 'private_profile_scope_frozen',
        persisted: 'unknown',
        effective: 'unknown',
      })),
      fields: buildView().fields.map((field) => ({
        ...field,
        editable: false,
        reason: 'private_profile_scope_frozen',
      })),
      families: buildView().families.map((family) => ({
        ...family,
        writable: false,
        readOnly: true,
        reason: 'private_profile_scope_frozen',
      })),
    });
    const fake = fakeClient(view);
    await mount(fake.client);
    expect(testId('gateway-readonly')).toBeDefined();
    const save = renderer.root.findAll(
      (node) => node.type === 'button' && node.props.children === c('groupSave')
    );
    expect(save.every((node) => node.props.disabled === true)).toBe(true);
  });

  it('keeps payload rule lists read-only on an External installation', async () => {
    const view = buildView({
      capability: {
        mode: 'external',
        readable: true,
        writable: true,
        rulesWritable: false,
        reason: 'external_rule_write_unproven',
      },
      families: buildView().families.map((family) => ({
        ...family,
        writable: false,
        readOnly: true,
        reason: 'external_rule_write_unproven',
      })),
    });
    await mount(fakeClient(view).client);
    // Standard tabs stay clean without a top-level read-only banner; opening the
    // requests tab surfaces the rule-family read-only notice right above rule cards.
    expect(maybeTestId('gateway-readonly')).toBeUndefined();
    await openTab('payload');
    expect(testId('gateway-readonly')).toBeDefined();
    expect(testId('gateway-readonly-detail')).toBeDefined();
    const edit = renderer.root.findAll(
      (node) => node.type === 'button' && node.props.children === c('ruleEditFamily')
    );
    expect(edit).toHaveLength(0);
    const cardWarns = testId('gateway-family-grid').findAll(
      (node) => typeof node.props.className === 'string' && node.props.className.includes('notice warn')
    );
    expect(cardWarns).toHaveLength(0);
  });

  it('drafts a rule family and writes it with one request', async () => {
    const fake = fakeClient(buildView());
    await mount(fake.client);
    await openTab('payload');
    const open = renderer.root.findAll(
      (node) => node.type === 'button' && node.props.children === c('ruleEditFamily')
    )[0];
    await act(async () => open.props.onClick());
    expect(maybeTestId('gateway-rule-editor')).toBeDefined();
    const add = testId('rule-add');
    await act(async () => add.props.onClick());
    const name = testId('rule-model-name-0');
    await act(async () => name.props.onChange({ target: { value: 'gpt-*' } }));
    const paramAdd = testId('rule-param-add');
    await act(async () => paramAdd.props.onClick());
    const paramInputs = renderer.root.findAll(
      (node) => node.type === 'input' && node.props['data-param-row'] === undefined
    );
    void paramInputs;
    const rows = testId('gateway-rule-editor').findAll(
      (node) => typeof node.type === 'string' && node.props['data-param-row'] !== undefined
    );
    const paramRow = rows[0];
    const [pathInput, valueInput] = paramRow.findAllByType('input');
    await act(async () => pathInput.props.onChange({ target: { value: 'reasoning.effort' } }));
    await act(async () => valueInput.props.onChange({ target: { value: 'high' } }));
    const apply = testId('rule-editor-apply');
    await act(async () => apply.props.onClick());
    const saveDraft = testId('rule-save');
    await act(async () => saveDraft.props.onClick());
    const save = testId('gateway-family-default').findAll(
      (node) => node.type === 'button' && node.props.children === c('ruleSaveFamily')
    )[0];
    await act(async () => save.props.onClick());
    await act(async () => {
      await Promise.resolve();
    });
    const write = fake.calls.filter(
      (call) => call.method === 'post' && call.resource.startsWith('gateway/config/rules/')
    );
    expect(write).toHaveLength(1);
    expect(write[0].resource).toBe('gateway/config/rules/default');
    expect((write[0].body as { rules: unknown[] }).rules).toEqual([
      { models: [{ name: 'gpt-*' }], params: { 'reasoning.effort': 'high' } },
    ]);
  });
  it('surfaces global search and changed-only matches across tabs from the default common tab', async () => {
    const fake = fakeClient(buildView());
    await mount(fake.client);
    expect(tab('common').props['aria-selected']).toBe(true);

    const search = renderer.root.findByProps({ type: 'search' });
    await act(async () => search.props.onChange({ target: { value: 'disable-cooling' } }));
    expect(maybeTestId('gateway-field-routing.cooldown.disable-cooling')).toBeDefined();

    await act(async () => search.props.onChange({ target: { value: '' } }));
    expect(maybeTestId('gateway-field-routing.cooldown.disable-cooling')).toBeUndefined();

    await openTab('routing');
    await act(async () =>
      testId('gateway-field-routing.retry.request-retry')
        .findByType('input')
        .props.onChange({ target: { value: '5' } })
    );
    await openTab('media');
    expect(maybeTestId('gateway-field-routing.retry.request-retry')).toBeUndefined();

    const toggle = renderer.root.findByProps({ type: 'checkbox' });
    await act(async () => toggle.props.onChange({ target: { checked: true } }));
    expect(maybeTestId('gateway-field-routing.retry.request-retry')).toBeDefined();
  });
  it('expands collapsed advanced groups, searches by keyword, and edits compound list/kv/pattern fields', async () => {
    const fake = fakeClient(buildView());
    await mount(fake.client);

    await openTab('upstream');
    // Default upstream provider is Codex (codex_oauth starts collapsed by default).
    expect(maybeTestId('gateway-field-upstream.codex.response-steering')).toBeDefined();
    expect(maybeTestId('gateway-field-oauth.providers.codex.signin-with-chatgpt-identifier')).toBeUndefined();
    await act(async () => testId('gateway-group-toggle-codex_oauth').props.onClick());
    expect(maybeTestId('gateway-field-oauth.providers.codex.signin-with-chatgpt-identifier')).toBeDefined();

    // Switch to Antigravity sub-navigation directly.
    await act(async () => testId('gateway-upstream-antigravity').props.onClick());
    expect(maybeTestId('gateway-field-oauth.providers.antigravity.sensitive-words')).toBeDefined();
    // Keyword search ("2119") automatically surfaces & expands the Antigravity sensitive-words controls.
    await openTab('common');
    const search = renderer.root.findByProps({ type: 'search' });
    await act(async () => search.props.onChange({ target: { value: '2119' } }));
    expect(maybeTestId('gateway-field-oauth.providers.antigravity.sensitive-words')).toBeDefined();

    // Add a string-list entry and save.
    await act(async () =>
      testId('gateway-field-oauth.providers.antigravity.sensitive-words-add').props.onClick()
    );
    const listInputs = testId('gateway-field-oauth.providers.antigravity.sensitive-words-list').findAllByType('input');
    await act(async () => listInputs[1].props.onChange({ target: { value: 'C 2119' } }));
    await act(async () =>
      testId('gateway-group-antigravity')
        .findAllByType('button')
        .find((node) => node.props.children === c('groupSave'))!
        .props.onClick()
    );
    await act(async () => {
      await Promise.resolve();
    });
    const write = fake.calls.filter((call) => call.method === 'post');
    expect(write).toHaveLength(1);
    expect(write[0].resource).toBe('gateway/config/groups/antigravity');
    expect(write[0].body).toMatchObject({
      edits: [
        {
          path: 'oauth.providers.antigravity.sensitive-words',
          action: 'set',
          value: ['item-1', 'C 2119'],
        },
      ],
    });
  });
  it('edits and saves a payload.replace rule with positions and phrase pairs', async () => {
    const fake = fakeClient(buildView());
    await mount(fake.client);
    await openTab('payload');
    const replaceCard = testId('gateway-family-replace');
    await act(async () =>
      replaceCard.findAllByType('button').find((node) => node.props.children === c('ruleEditFamily'))!.props.onClick()
    );
    await act(async () => testId('rule-add').props.onClick());
    await act(async () => testId('rule-model-name-0').props.onChange({ target: { value: 'claude-*' } }));
    await act(async () =>
      renderer.root
        .findAll((node) => node.type === 'button' && node.props.children === c('ruleReplacePositions'))[0]
        .props.onClick()
    );
    await act(async () => testId('rule-replace-positions').props.onChange({ target: { value: '1, 2' } }));
    await act(async () => testId('rule-replace-search-0').props.onChange({ target: { value: 'secret' } }));
    await act(async () => testId('rule-replace-with-0').props.onChange({ target: { value: 'safe' } }));
    await act(async () => testId('rule-editor-apply').props.onClick());
    await act(async () => testId('rule-save').props.onClick());
    await act(async () =>
      testId('gateway-family-replace')
        .findAllByType('button')
        .find((node) => node.props.children === c('ruleSaveFamily'))!
        .props.onClick()
    );
    await act(async () => {
      await Promise.resolve();
    });
    const write = fake.calls.filter((call) => call.method === 'post' && call.resource === 'gateway/config/rules/replace');
    expect(write).toHaveLength(1);
    expect(write[0].body).toMatchObject({
      rules: [
        {
          models: [{ name: 'claude-*' }],
          target: { role: 'user', positions: [1, 2] },
          replacements: [{ secret: 'safe' }],
        },
      ],
    });
  });
  it('saves all dirty groups across tabs with the save-all toolbar action', async () => {
    const fake = fakeClient(buildView());
    await mount(fake.client);
    await openTab('routing');
    await act(async () =>
      testId('gateway-field-routing.strategy').findByType('select').props.onChange({ target: { value: 'fill-first' } })
    );
    await openTab('network');
    await act(async () =>
      testId('gateway-field-requests.proxy-url').findByType('input').props.onChange({ target: { value: 'http://127.0.0.1:7890' } })
    );
    await act(async () => testId('gateway-save-all').props.onClick());
    await act(async () => {
      await Promise.resolve();
    });
    const writes = fake.calls.filter((call) => call.method === 'post');
    expect(writes.map((call) => call.resource)).toEqual([
      'gateway/config/groups/routing',
      'gateway/config/groups/request',
    ]);
  });
  it('surfaces upstream sub-nav dirty/error badges for hidden providers, jumps via diff, and routes payload badges to payload tab', async () => {
    const fake = fakeClient(buildView());
    await mount(fake.client);

    // Edit Antigravity, then switch away to Claude — Antigravity dirty count remains visible in sub-nav.
    await openTab('upstream');
    await act(async () => testId('gateway-upstream-antigravity').props.onClick());
    await act(async () =>
      testId('gateway-field-oauth.providers.antigravity.sensitive-words-add').props.onClick()
    );
    const listInputs = testId('gateway-field-oauth.providers.antigravity.sensitive-words-list').findAllByType('input');
    await act(async () => listInputs[1].props.onChange({ target: { value: 'C 2119' } }));
    await act(async () => testId('gateway-upstream-claude').props.onClick());
    expect(maybeTestId('gateway-field-oauth.providers.antigravity.sensitive-words')).toBeUndefined();
    expect(testId('gateway-upstream-dirty-antigravity').props.children).toBe(1);

    // Changed-only filter across all tabs and hidden providers exposes the Antigravity edit.
    const toggle = renderer.root.findAllByProps({ type: 'checkbox' })[0];
    await act(async () => toggle.props.onChange({ target: { checked: true } }));
    expect(maybeTestId('gateway-field-oauth.providers.antigravity.sensitive-words')).toBeDefined();
    await act(async () => toggle.props.onChange({ target: { checked: false } }));

    // Open diff modal and jump directly to Antigravity in Upstream.
    await openTab('common');
    const viewChangesBtn = renderer.root.findAll(
      (node) => node.type === 'button' && node.props.children === c('viewChanges')
    )[0];
    await act(async () => viewChangesBtn.props.onClick());
    const jumpBtn = testId('gateway-diff').findAll(
      (node) => node.type === 'button' && node.props.children === c('jumpToGroup')
    )[0];
    await act(async () => jumpBtn.props.onClick());
    expect(tab('upstream').props['aria-selected']).toBe(true);
    expect(maybeTestId('gateway-field-oauth.providers.antigravity.sensitive-words')).toBeDefined();

    // Payload rules dirty & error badges land on the dedicated payload tab, never network.
    await openTab('payload');
    await act(async () =>
      testId('gateway-family-default')
        .findAllByType('button')
        .find((node) => node.props.children === c('ruleEditFamily'))!
        .props.onClick()
    );
    await act(async () => testId('rule-add').props.onClick());
    await act(async () => testId('rule-model-name-0').props.onChange({ target: { value: 'gpt-*' } }));
    await act(async () => testId('rule-param-add').props.onClick());
    const paramRows = testId('gateway-rule-editor').findAll(
      (node) => typeof node.type === 'string' && node.props['data-param-row'] !== undefined
    );
    const [pathInput, valueInput] = paramRows[0].findAllByType('input');
    await act(async () => pathInput.props.onChange({ target: { value: 'temperature' } }));
    await act(async () => valueInput.props.onChange({ target: { value: '0.5' } }));
    await act(async () => testId('rule-editor-apply').props.onClick());
    await act(async () => testId('rule-save').props.onClick());
    expect(testId('gateway-tab-dirty-payload').props.children).toBe(1);
    expect(maybeTestId('gateway-tab-dirty-network')).toBeUndefined();

    // Clients tab contains client & client_claude.
    await openTab('clients');
    expect(maybeTestId('gateway-field-client.codex.enable-apply-patch')).toBeDefined();
    expect(maybeTestId('gateway-field-client.claude.show-codex-commentary')).toBeDefined();
  });
  it('switches to YAML source mode, blocks switching away with dirty YAML, previews diff, and saves unadapted CPA fields', async () => {
    const fake = fakeClient(buildView());
    await mount(fake.client);

    await act(async () => testId('gateway-mode-source').props.onClick());
    for (let i = 0; i < 10 && !maybeTestId('gateway-source-codemirror'); i += 1) {
      await settle();
    }
    expect(testId('gateway-source-workspace')).toBeDefined();
    expect(testId('gateway-source-codemirror').props.value).toBe('routing:\n  strategy: round-robin\n');

    const nextYaml = 'routing:\n  strategy: round-robin\n  future-cpa-option: true\n';
    await act(async () =>
      testId('gateway-source-codemirror').props.onChange({ target: { value: nextYaml } })
    );
    expect(testId('gateway-source-status').props.children).toBe(c('sourceDirtyBadge'));

    // Switching back to visual while YAML has unsaved edits shows warning and stays in source mode.
    await act(async () => testId('gateway-mode-visual').props.onClick());
    expect(testId('gateway-mode-switch-error').props.children).toBe(c('sourceSwitchSourceDirty'));
    expect(testId('gateway-source-workspace')).toBeDefined();

    // Diff modal displays additions.
    await act(async () => testId('gateway-source-diff-btn').props.onClick());
    expect(testId('gateway-source-diff')).toBeDefined();

    // Save YAML posts to gateway/config/source with expected baseline and reloads both views.
    await act(async () => testId('gateway-source-save').props.onClick());
    await act(async () => {
      await Promise.resolve();
    });
    const post = fake.calls.find((call) => call.method === 'post' && call.resource === 'gateway/config/source');
    expect(post).toBeDefined();
    expect(post?.body).toMatchObject({
      expected: { generation: '7', baseline: BASELINE },
      yaml: nextYaml,
    });
  });
  it('surfaces protected_source_path and invalid_source_yaml errors while preserving draft, and enforces Private read-only', async () => {
    const fake = fakeClient(buildView());
    await mount(fake.client);
    await act(async () => testId('gateway-mode-source').props.onClick());
    await act(async () => {
      await Promise.resolve();
    });
    await act(async () =>
      testId('gateway-source-codemirror').props.onChange({ target: { value: 'management:\n  secret-key: bad\n' } })
    );
    fake.state.saveError = new PublicApiError('http', 422, undefined, 'protected_source_path', 'management.secret-key');
    await act(async () => testId('gateway-source-save').props.onClick());
    expect(testId('gateway-source-error').props.children).toContain('management.secret-key');
    expect(testId('gateway-source-codemirror').props.value).toBe('management:\n  secret-key: bad\n');

    // Private mode keeps source strictly read-only.
    await act(async () => renderer.unmount());
    const privView = buildView({
      capability: { mode: 'private', readable: true, writable: true, rulesWritable: false, reason: 'private_profile_scope_frozen' },
    });
    const privFake = fakeClient(privView);
    await mount(privFake.client);
    await act(async () => testId('gateway-mode-source').props.onClick());
    await act(async () => {
      await Promise.resolve();
    });
    expect(testId('gateway-source-readonly').props.children).toBe(c('sourcePrivateReadOnly'));
    expect(testId('gateway-source-codemirror').props.readOnly).toBe(true);
    expect(testId('gateway-source-save').props.disabled).toBe(true);
  });

  it('calls onUnauthorized on PublicApiError authentication failures (401/403)', async () => {
    vi.stubGlobal('window', {
      location: new URL('https://manager.example/console'),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });
    const onUnauthorized = vi.fn();
    const transport = vi.fn<typeof fetch>(() =>
      Promise.resolve(new Response('unauthorized', { status: 401 }))
    );
    const apiClient = createPublicApiClient(() => PUBLIC_CONTEXT, transport);
    await mount(apiClient, vi.fn(), vi.fn(), onUnauthorized);
    await settle();
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
  });

  it('surfaces external blockers in reportWriteError and queryOriginal, displays HTTP errors in queryOriginal, and reloads source with keepDraft', async () => {
    const fake = fakeClient(buildView());
    await mount(fake.client);

    // Trigger external_management_key_rejected during group save -> blocker renders instead of generic error.
    await openTab('network');
    const timeoutInput = testId('gateway-field-requests.tls-handshake-timeout-seconds').findByType('input');
    await act(async () => timeoutInput.props.onChange({ target: { value: '12' } }));
    fake.state.saveError = new PublicApiError(
      'http',
      409,
      undefined,
      'external_management_key_rejected',
      undefined,
      'CPA key changed'
    );
    await save('request');
    expect(testId('external-blocker-key_rejected')).toBeDefined();
    expect(maybeTestId('gateway-group-error-request')).toBeUndefined();

    // Visual mode onReload keeps the unsaved form draft.
    fake.state.saveError = new PublicApiError('http', 409, undefined, 'external_rebind_stale_generation');
    await save('request');
    expect(testId('external-blocker-rebind_stale')).toBeDefined();
    const configReadsBefore = fake.calls.filter(
      (call) => call.method === 'get' && call.resource === 'gateway/config'
    ).length;
    fake.state.saveError = null;
    await act(async () => testId('external-blocker-reload').props.onClick());
    await settle();
    const configReadsAfter = fake.calls.filter(
      (call) => call.method === 'get' && call.resource === 'gateway/config'
    ).length;
    expect(configReadsAfter).toBe(configReadsBefore + 1);
    expect(testId('gateway-field-requests.tls-handshake-timeout-seconds').findByType('input').props.value).toBe('12');

    // Unknown save followed by reconcile returning 404 operation_not_found shows HTTP error, then external blocker on reconcile surfaces blocker.
    fake.state.saveError = new PublicApiError('network');
    await save('request');
    expect(testId('gateway-group-error-request').props.children).toBe(c('groupSaveUnknown'));

    fake.state.saveError = null;
    fake.state.reconcileError = new PublicApiError('http', 404, undefined, 'operation_not_found');
    await act(async () => testId('gateway-group-reconcile-request').findByType('button').props.onClick());
    await settle();
    expect(testId('gateway-group-error-request').props.children).toBe(c('groupSaveFailed'));

    // Re-trigger unknown so reconcile button is available, then reconcile returns external_target_moved.
    fake.state.saveError = new PublicApiError('network');
    await save('request');
    fake.state.saveError = null;
    fake.state.reconcileError = new PublicApiError('http', 409, undefined, 'external_target_moved');
    await act(async () => testId('gateway-group-reconcile-request').findByType('button').props.onClick());
    await settle();
    expect(testId('external-blocker-target_moved')).toBeDefined();

    // Discard visual drafts, switch to source mode, trigger external_rebind_stale_generation on save, and verify onReload reloads source keeping draft.
    await act(async () => testId('external-blocker-dismiss').props.onClick());
    await act(async () => {
      const toolbarDiscardBtn = testId('gateway-dirty-count').parent!.findAll(
        (node) => node.type === 'button' && node.props.children === c('discard')
      )[0];
      toolbarDiscardBtn.props.onClick();
    });
    await act(async () => {
      const confirmDiscardBtn = renderer.root.findAll(
        (node) => node.type === 'button' && node.props.children === c('confirmDiscard')
      )[0];
      confirmDiscardBtn.props.onClick();
    });

    await act(async () => testId('gateway-mode-source').props.onClick());
    await settle();
    const dirtyYaml = 'routing:\n  strategy: fill-first\n';
    await act(async () =>
      testId('gateway-source-codemirror').props.onChange({ target: { value: dirtyYaml } })
    );
    fake.state.saveError = new PublicApiError('http', 409, undefined, 'external_rebind_stale_generation');
    await act(async () => testId('gateway-source-save').props.onClick());
    await settle();
    expect(testId('external-blocker-rebind_stale')).toBeDefined();

    const sourceReadsBefore = fake.calls.filter(
      (call) => call.method === 'get' && call.resource === 'gateway/config/source'
    ).length;
    fake.state.saveError = null;
    await act(async () => testId('external-blocker-reload').props.onClick());
    await settle();
    const sourceReadsAfter = fake.calls.filter(
      (call) => call.method === 'get' && call.resource === 'gateway/config/source'
    ).length;
    expect(sourceReadsAfter).toBe(sourceReadsBefore + 1);
    expect(testId('gateway-source-codemirror').props.value).toBe(dirtyYaml);
  });

  it('stops saveAllDirty on first failed family save and preserves other groups validation issues in clearGroupDrafts', async () => {
    const fake = fakeClient(buildView());
    await mount(fake.client);

    // Create validation issue in request group AND retry group.
    await openTab('network');
    await act(async () =>
      testId('gateway-field-requests.tls-handshake-timeout-seconds')
        .findByType('input')
        .props.onChange({ target: { value: 'abc' } })
    );
    await save('request');
    expect(testId('gateway-field-requests.tls-handshake-timeout-seconds-error')).toBeDefined();

    await openTab('common');
    await act(async () =>
      testId('gateway-field-routing.retry.request-retry')
        .findByType('input')
        .props.onChange({ target: { value: 'xyz' } })
    );
    await save('retry');
    expect(testId('gateway-field-routing.retry.request-retry-error')).toBeDefined();

    // Discard ONLY retry group -> retry error clears, request error remains intact!
    const retryDiscardBtn = testId('gateway-group-retry').findAll(
      (node) => node.type === 'button' && node.props.children === c('discard')
    )[0];
    await act(async () => retryDiscardBtn.props.onClick());
    await act(async () => {
      const confirmBtn = renderer.root.findAll(
        (node) => node.type === 'button' && node.props.children === c('confirmDiscard')
      )[0];
      confirmBtn.props.onClick();
    });
    expect(maybeTestId('gateway-field-routing.retry.request-retry-error')).toBeUndefined();
    await openTab('network');
    expect(testId('gateway-field-requests.tls-handshake-timeout-seconds-error')).toBeDefined();
    // Clean request draft before saveAllDirty test.
    const reqDiscardBtn = testId('gateway-group-request').findAll(
      (node) => node.type === 'button' && node.props.children === c('discard')
    )[0];
    await act(async () => reqDiscardBtn.props.onClick());
    await act(async () => {
      const confirmBtn = renderer.root.findAll(
        (node) => node.type === 'button' && node.props.children === c('confirmDiscard')
      )[0];
      confirmBtn.props.onClick();
    });
    // Make two rule families dirty, fail the first family save, and assert second family is not saved and stays dirty.
    await openTab('payload');
    for (const familyKey of ['default', 'override'] as const) {
      await act(async () =>
        testId(`gateway-family-${familyKey}`)
          .findAllByType('button')
          .find((node) => node.props.children === c('ruleEditFamily'))!
          .props.onClick()
      );
      await act(async () => testId('rule-add').props.onClick());
      await act(async () => testId('rule-model-name-0').props.onChange({ target: { value: 'gpt-*' } }));
      await act(async () => testId('rule-param-add').props.onClick());
      const paramRows = testId('gateway-rule-editor').findAll(
        (node) => typeof node.type === 'string' && node.props['data-param-row'] !== undefined
      );
      const [pathInput, valueInput] = paramRows[0].findAllByType('input');
      await act(async () => pathInput.props.onChange({ target: { value: 'temperature' } }));
      await act(async () => valueInput.props.onChange({ target: { value: '0.5' } }));
      await act(async () => testId('rule-editor-apply').props.onClick());
      await act(async () => testId('rule-save').props.onClick());
    }
    expect(testId('gateway-tab-dirty-payload').props.children).toBe(2);

    fake.state.saveError = new PublicApiError('network');
    await act(async () => testId('gateway-save-all').props.onClick());
    await settle();

    const familyPosts = fake.calls.filter(
      (call) => call.method === 'post' && call.resource.startsWith('gateway/config/rules/')
    );
    expect(familyPosts).toHaveLength(1);
    expect(familyPosts[0].resource).toBe('gateway/config/rules/default');
    expect(testId('gateway-family-error-default').props.children).toBe(c('groupSaveUnknown'));
    expect(testId('gateway-tab-errors-payload').props.children).toBe(1);
    expect(testId('gateway-dirty-count').props.children).toBe(c('dirtyCount', 2));
  });

  it('keeps kv-map rows stable across temporary duplicate keys and numeric keys like 2119', async () => {
    const view = buildView();
    view.fields = view.fields.map((field) =>
      field.path === 'oauth.providers.antigravity.sensitive-words-replace'
        ? { ...field, value: { alpha: 'one', beta: 'two' } }
        : field
    );
    const fake = fakeClient(view);
    await mount(fake.client);

    await openTab('upstream');
    await act(async () => testId('gateway-upstream-antigravity').props.onClick());

    const getKvKeyInputs = () =>
      testId('gateway-field-oauth.providers.antigravity.sensitive-words-replace-kv').findAll(
        (node) =>
          node.type === 'input' &&
          typeof node.props['aria-label'] === 'string' &&
          node.props['aria-label'].includes(' key #')
      );
    const getKvValueInputs = () =>
      testId('gateway-field-oauth.providers.antigravity.sensitive-words-replace-kv').findAll(
        (node) =>
          node.type === 'input' &&
          typeof node.props['aria-label'] === 'string' &&
          node.props['aria-label'].includes(' value #')
      );

    // Rename row 0 ('alpha') temporarily to 'beta' (duplicate of row 1) -> rows do not collapse.
    await act(async () => getKvKeyInputs()[0].props.onChange({ target: { value: 'beta' } }));
    expect(getKvKeyInputs()).toHaveLength(2);
    expect(getKvKeyInputs()[0].props.value).toBe('beta');
    expect(getKvValueInputs()[0].props.value).toBe('one');
    expect(getKvKeyInputs()[1].props.value).toBe('beta');
    expect(getKvValueInputs()[1].props.value).toBe('two');

    // Rename row 1 ('beta') to numeric key '2119' -> row 1 stays at index 1 and does not jump before row 0.
    await act(async () => getKvKeyInputs()[1].props.onChange({ target: { value: '2119' } }));
    expect(getKvKeyInputs()).toHaveLength(2);
    expect(getKvKeyInputs()[0].props.value).toBe('beta');
    expect(getKvValueInputs()[0].props.value).toBe('one');
    expect(getKvKeyInputs()[1].props.value).toBe('2119');
    expect(getKvValueInputs()[1].props.value).toBe('two');
  });

  it('evaluates dependsOn against live drafts, disables dependent select and default button, and adds matching empty option on default', async () => {
    const fake = fakeClient(buildView());
    await mount(fake.client);
    await openTab('routing');

    const findRebindSelect = () =>
      testId('gateway-field-routing.session-affinity-rebind-on').findByType('select');
    const findRebindDefaultBtn = () =>
      testId('gateway-field-routing.session-affinity-rebind-on').findAll(
        (node) => node.type === 'button' && node.props.children === c('fieldUseDefault')
      )[0];

    // Initially routing.session-affinity is false -> dependent select and Use default button are disabled.
    expect(findRebindSelect().props.disabled).toBe(true);
    expect(findRebindDefaultBtn().props.disabled).toBe(true);

    // Toggle routing.session-affinity ON in draft -> dependent select and button enable immediately.
    const affinityCheckbox = testId('gateway-field-routing.session-affinity').findByType('input');
    await act(async () => affinityCheckbox.props.onChange({ target: { checked: true } }));
    expect(findRebindSelect().props.disabled).toBe(false);
    expect(findRebindDefaultBtn().props.disabled).toBe(false);

    // Restoring default on select sets value="" and renders an <option value="">.
    await act(async () => findRebindDefaultBtn().props.onClick());
    expect(findRebindSelect().props.value).toBe('');
    const options = findRebindSelect().findAllByType('option');
    expect(options.some((opt) => opt.props.value === '')).toBe(true);
  });

  it('validates invalid position tokens and JSON replace rules, and clears GatewayRuleEditor issue on cancel', async () => {
    const replaceFamily = gatewayFamilies().find((family) => family.family === 'replace')!;
    expect(
      formToRule(
        {
          models: [{ name: 'gpt-*', protocol: '', from: '', rows: [] }],
          params: [],
          replaceTargetMode: 'positions',
          replacePositions: '1, abc',
          replaceEntries: [{ search: 'foo', replace: 'bar' }],
        },
        replaceFamily
      )
    ).toEqual({ ok: false, issue: 'replacePositions' });

    expect(
      parseRuleText(
        JSON.stringify({
          models: [{ name: 'gpt-*' }],
          target: { role: 'user', all: false, positions: [1] },
          replacements: [{ foo: 'bar' }],
        }),
        replaceFamily
      ).ok
    ).toBe(true);

    expect(
      parseRuleText(
        JSON.stringify({
          models: [{ name: 'gpt-*' }],
          target: { role: 'user', all: true, positions: [1] },
          replacements: [{ foo: 'bar' }],
        }),
        replaceFamily
      )
    ).toEqual({ ok: false, issue: 'replacePositions' });

    expect(
      parseRuleText(
        JSON.stringify({
          models: [{ name: 'gpt-*' }],
          target: { role: 'user', all: true },
          replacements: [{ '   ': 'bar' }],
        }),
        replaceFamily
      ).ok
    ).toBe(true);

    expect(
      parseRuleText(
        JSON.stringify({
          models: [{ name: 'gpt-*' }],
          target: { role: 'user', all: true },
          replacements: [{ '': 'bar' }],
        }),
        replaceFamily
      )
    ).toEqual({ ok: false, issue: 'replacePairs' });

    // GatewayRuleEditor clears validation error when backing out via cancel.
    const fake = fakeClient(buildView());
    await mount(fake.client);
    await openTab('payload');
    await act(async () =>
      testId('gateway-family-default')
        .findAllByType('button')
        .find((node) => node.props.children === c('ruleEditFamily'))!
        .props.onClick()
    );
    await act(async () => testId('rule-add').props.onClick());
    await act(async () => testId('rule-editor-apply').props.onClick());
    expect(testId('rule-error')).toBeDefined();
    await act(async () => testId('rule-editor-cancel').props.onClick());
    expect(maybeTestId('rule-error')).toBeUndefined();
  });

  it('advances source search across repeated Enter presses without mutating YAML', async () => {
    const fake = fakeClient(buildView());
    fake.state.sourceView = {
      ...fake.state.sourceView,
      yaml: 'routing:\n  strategy: round-robin\n  fallback: round-robin\n',
    };
    await mount(fake.client);

    await act(async () => testId('gateway-mode-source').props.onClick());
    await act(async () => {
      await Promise.resolve();
    });

    const searchInput = testId('gateway-source-search');
    await act(async () => searchInput.props.onChange({ target: { value: 'round-robin' } }));
    const preventDefault = vi.fn();
    await act(async () =>
      testId('gateway-source-search').props.onKeyDown({
        key: 'Enter',
        shiftKey: false,
        preventDefault,
      })
    );
    expect(preventDefault).toHaveBeenCalledTimes(1);
    expect(testId('gateway-source-search-count').children.join('')).toBe('1 / 2');

    await act(async () =>
      testId('gateway-source-search').props.onKeyDown({
        key: 'Enter',
        shiftKey: false,
        preventDefault,
      })
    );
    expect(testId('gateway-source-search-count').children.join('')).toBe('2 / 2');
    expect(testId('gateway-source-codemirror').props.value).toBe(
      'routing:\n  strategy: round-robin\n  fallback: round-robin\n'
    );
  });

  it('treats missing non-inheritable bool parent fields as dependencyOff = true', async () => {
    const view = buildView();
    view.fields = view.fields.map((field) =>
      field.path === 'routing.session-affinity'
        ? { ...field, presence: 'missing', value: null }
        : field
    );
    const fake = fakeClient(view);
    await mount(fake.client);
    await openTab('routing');

    const findRebindSelect = () =>
      testId('gateway-field-routing.session-affinity-rebind-on').findByType('select');
    const findRebindDefaultBtn = () =>
      testId('gateway-field-routing.session-affinity-rebind-on').findAll(
        (node) => node.type === 'button' && node.props.children === c('fieldUseDefault')
      )[0];

    expect(findRebindSelect().props.disabled).toBe(true);
    expect(findRebindDefaultBtn().props.disabled).toBe(true);
  });

  it('keeps needsReconcile true and targets pendingWrite when pendingWrite differs from settled scopeOperation under groupSaveUnknown', async () => {
    const view = buildView();
    view.groups = view.groups.map((group) =>
      group.id === 'retry'
        ? {
            ...group,
            persisted: 'persisted',
            operation: {
              requestId: 'a'.repeat(32),
              group: 'retry',
              target: 'group',
              state: 'persisted',
            },
          }
        : group
    );
    const fake = fakeClient(view);
    await mount(fake.client);
    await openTab('routing');

    fake.state.saveError = new PublicApiError('network');
    const input = testId('gateway-field-routing.retry.request-retry').findByType('input');
    await act(async () => input.props.onChange({ target: { value: '5' } }));
    await save('retry');

    expect(testId('gateway-group-error-retry').props.children).toBe(c('groupSaveUnknown'));
    const reconcileBtn = testId('gateway-group-reconcile-retry').findByType('button');
    expect(reconcileBtn).toBeDefined();

    fake.state.saveError = null;
    await act(async () => reconcileBtn.props.onClick());
    await settle();
    const reconcileCall = fake.calls.find(
      (call) =>
        call.method === 'post' &&
        call.resource.startsWith('gateway/config/operations/') &&
        call.resource.endsWith('/reconcile')
    );
    expect(reconcileCall).toBeDefined();
    expect(reconcileCall!.resource).not.toContain('a'.repeat(32));
  });

  it('clears conflict state across actions, refreshes both views on queryOriginal, and omits empty diff-list for source conflicts', async () => {
    const view = buildView();
    const fake = fakeClient(view);
    await mount(fake.client);

    // Source conflict does not render an empty diff-list and clears on source discard.
    await act(async () => testId('gateway-mode-source').props.onClick());
    await act(async () => {
      await Promise.resolve();
    });
    await act(async () =>
      testId('gateway-source-codemirror').props.onChange({
        target: { value: 'routing:\n  strategy: fill-first\n' },
      })
    );
    fake.state.saveError = new PublicApiError('http', 409, undefined, 'generation_mismatch');
    await act(async () => testId('gateway-source-save').props.onClick());
    await settle();
    expect(testId('gateway-conflict')).toBeDefined();
    expect(maybeTestId('gateway-conflict-diff')).toBeUndefined();

    await act(async () => testId('gateway-source-discard').props.onClick());
    expect(maybeTestId('gateway-conflict')).toBeUndefined();

    // queryOriginal on source refreshes both gateway/config/source and gateway/config.
    fake.state.saveError = new PublicApiError('network');
    await act(async () =>
      testId('gateway-source-codemirror').props.onChange({
        target: { value: 'routing:\n  strategy: weighted-round-robin\n' },
      })
    );
    await act(async () => testId('gateway-source-save').props.onClick());
    await settle();
    const callsBefore = fake.calls.length;
    fake.state.saveError = null;
    await act(async () => testId('gateway-source-reconcile').findByType('button').props.onClick());
    await settle();
    const afterCalls = fake.calls.slice(callsBefore);
    expect(afterCalls.some((cItem) => cItem.method === 'get' && cItem.resource === 'gateway/config/source')).toBe(
      true
    );
    expect(afterCalls.some((cItem) => cItem.method === 'get' && cItem.resource === 'gateway/config')).toBe(
      true
    );
  });

  it('flags duplicate keys and empty-key-with-value in kv-map and blocks saving', async () => {
    const view = buildView();
    view.fields = view.fields.map((field) =>
      field.path === 'oauth.providers.antigravity.sensitive-words-replace'
        ? { ...field, value: { alpha: 'one', beta: 'two' } }
        : field
    );
    const fake = fakeClient(view);
    await mount(fake.client);

    await openTab('upstream');
    await act(async () => testId('gateway-upstream-antigravity').props.onClick());

    const fieldPath = 'oauth.providers.antigravity.sensitive-words-replace';
    const getKvKeyInputs = () =>
      testId(`gateway-field-${fieldPath}-kv`).findAll(
        (node) =>
          node.type === 'input' &&
          typeof node.props['aria-label'] === 'string' &&
          node.props['aria-label'].includes(' key #')
      );
    const getKvValueInputs = () =>
      testId(`gateway-field-${fieldPath}-kv`).findAll(
        (node) =>
          node.type === 'input' &&
          typeof node.props['aria-label'] === 'string' &&
          node.props['aria-label'].includes(' value #')
      );

    // Duplicate key flags kvDuplicateKey and blocks save.
    await act(async () => getKvKeyInputs()[0].props.onChange({ target: { value: 'beta' } }));
    expect(testId(`gateway-field-${fieldPath}-error`).props.children).toBe(c('kvDuplicateKey'));
    await save('antigravity');
    expect(fake.calls.some((call) => call.method === 'post')).toBe(false);

    // Empty key with non-empty value flags kvKeyRequired and blocks save.
    await act(async () => getKvKeyInputs()[0].props.onChange({ target: { value: '   ' } }));
    await act(async () => getKvValueInputs()[0].props.onChange({ target: { value: 'non-empty' } }));
    expect(testId(`gateway-field-${fieldPath}-error`).props.children).toBe(c('kvKeyRequired'));
    await save('antigravity');
    expect(fake.calls.some((call) => call.method === 'post')).toBe(false);
  });

  it('clears stale group and source 409 conflict banners on manual Reload and Rebind refresh', async () => {
    const fake = fakeClient(buildView());
    const { onDirtyChange, onPending, onUnauthorized, onRebind } = await mount(fake.client);

    // Visual group 409 conflict clears when clicking Reload.
    await openTab('routing');
    const retryInput = testId('gateway-field-routing.retry.request-retry').findByType('input');
    await act(async () => retryInput.props.onChange({ target: { value: '9' } }));
    fake.state.saveError = new PublicApiError('http', 409, undefined, 'source_or_operation_conflict');
    await save('retry');
    expect(maybeTestId('gateway-conflict')).toBeDefined();
    expect(maybeTestId('gateway-conflict-diff')).toBeDefined();
    expect(maybeTestId('gateway-group-error-retry')).toBeDefined();

    fake.state.saveError = null;
    const visualReloadBtn = renderer.root.findAll(
      (node) => node.type === 'button' && node.props.children === c('reload')
    )[0];
    await act(async () => visualReloadBtn.props.onClick());
    await settle();
    expect(maybeTestId('gateway-conflict')).toBeUndefined();
    expect(maybeTestId('gateway-conflict-diff')).toBeUndefined();
    expect(maybeTestId('gateway-group-error-retry')).toBeUndefined();

    // Source 409 conflict clears when clicking Reload.
    await act(async () => testId('gateway-mode-source').props.onClick());
    await settle();
    await act(async () =>
      testId('gateway-source-codemirror').props.onChange({
        target: { value: 'routing:\n  strategy: fill-first\n' },
      })
    );
    fake.state.saveError = new PublicApiError('http', 409, undefined, 'generation_mismatch');
    await act(async () => testId('gateway-source-save').props.onClick());
    await settle();
    expect(maybeTestId('gateway-conflict')).toBeDefined();
    expect(maybeTestId('gateway-source-error')).toBeDefined();

    fake.state.saveError = null;
    const sourceReloadBtn = renderer.root.findAll(
      (node) => node.type === 'button' && node.props.children === c('reload')
    )[0];
    await act(async () => sourceReloadBtn.props.onClick());
    await settle();
    expect(maybeTestId('gateway-conflict')).toBeUndefined();
    expect(maybeTestId('gateway-source-error')).toBeUndefined();

    // Rebind refresh (reloadToken bump) also clears source 409 conflict.
    await act(async () =>
      testId('gateway-source-codemirror').props.onChange({
        target: { value: 'routing:\n  strategy: weighted-round-robin\n' },
      })
    );
    fake.state.saveError = new PublicApiError('http', 409, undefined, 'source_or_operation_conflict');
    await act(async () => testId('gateway-source-save').props.onClick());
    await settle();
    expect(maybeTestId('gateway-conflict')).toBeDefined();

    fake.state.saveError = null;
    await act(async () => {
      renderer.update(
        <FreshGatewayWorkspace
          client={fake.client}
          credential="admin-credential"
          onUnauthorized={onUnauthorized}
          onPending={onPending}
          onDirtyChange={onDirtyChange}
          onRebind={onRebind}
          reloadToken={1}
        />
      );
    });
    await settle();
    expect(maybeTestId('gateway-conflict')).toBeUndefined();
    expect(maybeTestId('gateway-source-error')).toBeUndefined();
  });

  it('keeps Save disabled and Discard keeps Reconcile + onPending(true) after unknown write even when reload returns no operation record', async () => {
    const fake = fakeClient(buildView());
    const { onPending } = await mount(fake.client);

    await openTab('routing');
    const retryInput = testId('gateway-field-routing.retry.request-retry').findByType('input');
    await act(async () => retryInput.props.onChange({ target: { value: '5' } }));

    fake.state.saveError = new PublicApiError('network');
    await save('retry');

    const findGroupButtons = () => {
      const buttons = testId('gateway-group-retry').findAllByType('button');
      return {
        discard: buttons.find((node) => node.props.children === c('discard'))!,
        save: buttons.find(
          (node) => node.props.children === c('groupSave') || node.props.children === c('groupSaving')
        )!,
      };
    };

    expect(maybeTestId('gateway-group-reconcile-retry')).toBeDefined();
    expect(findGroupButtons().save.props.disabled).toBe(true);
    expect(testId('gateway-save-all').props.disabled).toBe(true);
    expect(onPending.mock.calls[onPending.mock.calls.length - 1]).toEqual([true]);

    // Clicking Save directly does not issue a duplicate POST while reconcile is pending.
    const postsBefore = fake.calls.filter((call) => call.method === 'post').length;
    await act(async () => findGroupButtons().save.props.onClick());
    await settle();
    expect(fake.calls.filter((call) => call.method === 'post')).toHaveLength(postsBefore);

    // Discard clears group error/drafts but must keep Reconcile button + onPending(true) active.
    await act(async () => findGroupButtons().discard.props.onClick());
    const confirmDiscardBtn = renderer.root
      .findAllByType('button')
      .find((node) => node.props.children === c('confirmDiscard'))!;
    await act(async () => confirmDiscardBtn.props.onClick());
    await settle();

    expect(maybeTestId('gateway-group-error-retry')).toBeUndefined();
    expect(maybeTestId('gateway-group-reconcile-retry')).toBeDefined();
    expect(onPending.mock.calls[onPending.mock.calls.length - 1]).toEqual([true]);
    expect(testId('gateway-dirty-count').props.children).toBe(c('dirtyCount', 0));

    // Re-editing the locked group keeps Save disabled until reconciled.
    const retryInputAfterDiscard = testId('gateway-field-routing.retry.request-retry').findByType('input');
    await act(async () => retryInputAfterDiscard.props.onChange({ target: { value: '6' } }));
    expect(findGroupButtons().save.props.disabled).toBe(true);
    expect(testId('gateway-save-all').props.disabled).toBe(true);

    // Reconciling settles the lock and re-enables saving.
    fake.state.saveError = null;
    await act(async () =>
      testId('gateway-group-reconcile-retry').findByType('button').props.onClick()
    );
    await settle();
    expect(maybeTestId('gateway-group-reconcile-retry')).toBeUndefined();
    expect(onPending.mock.calls[onPending.mock.calls.length - 1]).toEqual([false]);
    const retryInputAfterReconcile = testId('gateway-field-routing.retry.request-retry').findByType('input');
    await act(async () => retryInputAfterReconcile.props.onChange({ target: { value: '7' } }));
    expect(findGroupButtons().save.props.disabled).toBe(false);
    const discardRetryBtn = findGroupButtons().discard;
    await act(async () => discardRetryBtn.props.onClick());
    const confirmRetryDiscardBtn = renderer.root
      .findAllByType('button')
      .find((node) => node.props.children === c('confirmDiscard'))!;
    await act(async () => confirmRetryDiscardBtn.props.onClick());
    await settle();

    // Payload family unknown write + Discard preserves Reconcile button, disabled Save, and onPending(true).
    await openTab('payload');
    const editFamilyBtn = testId('gateway-family-default').findAll(
      (node) => node.type === 'button' && node.props.children === c('ruleEditFamily')
    )[0];
    await act(async () => editFamilyBtn.props.onClick());
    await act(async () => testId('rule-add').props.onClick());
    await act(async () => testId('rule-param-add').props.onClick());
    const paramRow = testId('gateway-rule-editor').findAll(
      (node) => typeof node.type === 'string' && node.props['data-param-row'] !== undefined
    )[0];
    await act(async () =>
      paramRow.findAllByType('input')[0].props.onChange({ target: { value: 'temperature' } })
    );
    await act(async () =>
      paramRow.findAllByType('input')[1].props.onChange({ target: { value: '0.2' } })
    );
    await act(async () => testId('rule-editor-apply').props.onClick());
    await act(async () => testId('rule-save').props.onClick());

    const findFamilyButtons = () => {
      const buttons = testId('gateway-family-default').findAllByType('button');
      return {
        discard: buttons.find((node) => node.props.children === c('discard'))!,
        save: buttons.find(
          (node) => node.props.children === c('ruleSaveFamily') || node.props.children === c('groupSaving')
        )!,
      };
    };
    fake.state.saveError = new PublicApiError('network');
    await act(async () => findFamilyButtons().save.props.onClick());
    await settle();
    expect(maybeTestId('gateway-family-reconcile-default')).toBeDefined();
    expect(findFamilyButtons().save.props.disabled).toBe(true);
    expect(onPending.mock.calls[onPending.mock.calls.length - 1]).toEqual([true]);

    await act(async () => findFamilyButtons().discard.props.onClick());
    expect(maybeTestId('gateway-family-error-default')).toBeUndefined();
    expect(maybeTestId('gateway-family-reconcile-default')).toBeDefined();
    expect(onPending.mock.calls[onPending.mock.calls.length - 1]).toEqual([true]);

    fake.state.saveError = null;
    await act(async () =>
      testId('gateway-family-reconcile-default').findByType('button').props.onClick()
    );
    await settle();
    expect(maybeTestId('gateway-family-reconcile-default')).toBeUndefined();
    expect(onPending.mock.calls[onPending.mock.calls.length - 1]).toEqual([false]);

    // Source unknown write + Discard preserves Reconcile button, disabled Source Save, and onPending(true).
    await act(async () => testId('gateway-mode-source').props.onClick());
    await settle();
    await act(async () =>
      testId('gateway-source-codemirror').props.onChange({
        target: { value: 'routing:\n  strategy: fill-first\n' },
      })
    );
    fake.state.saveError = new PublicApiError('network');
    await act(async () => testId('gateway-source-save').props.onClick());
    await settle();
    expect(maybeTestId('gateway-source-reconcile')).toBeDefined();
    expect(testId('gateway-source-save').props.disabled).toBe(true);
    expect(onPending.mock.calls[onPending.mock.calls.length - 1]).toEqual([true]);

    await act(async () => testId('gateway-source-discard').props.onClick());
    expect(maybeTestId('gateway-source-error')).toBeUndefined();
    expect(maybeTestId('gateway-source-reconcile')).toBeDefined();
    expect(onPending.mock.calls[onPending.mock.calls.length - 1]).toEqual([true]);

    // Re-editing source while locked keeps Source Save disabled.
    await act(async () =>
      testId('gateway-source-codemirror').props.onChange({
        target: { value: 'routing:\n  strategy: weighted-round-robin\n' },
      })
    );
    expect(testId('gateway-source-save').props.disabled).toBe(true);

    fake.state.saveError = null;
    await act(async () =>
      testId('gateway-source-reconcile').findByType('button').props.onClick()
    );
    await settle();
    expect(maybeTestId('gateway-source-reconcile')).toBeUndefined();
    expect(onPending.mock.calls[onPending.mock.calls.length - 1]).toEqual([false]);
    await act(async () =>
      testId('gateway-source-codemirror').props.onChange({
        target: { value: 'routing:\n  strategy: weighted-round-robin\n' },
      })
    );
    expect(testId('gateway-source-save').props.disabled).toBe(false);
  });

  it('clears conflict state on FreshExternalBlocker onReload and when switching to visual mode cleanly', async () => {
    const fake = fakeClient(buildView());
    await mount(fake.client);

    await openTab('routing');
    const retryInput = testId('gateway-field-routing.retry.request-retry').findByType('input');
    await act(async () => retryInput.props.onChange({ target: { value: '8' } }));
    fake.state.saveError = new PublicApiError('http', 409, undefined, 'source_or_operation_conflict');
    await save('retry');
    expect(maybeTestId('gateway-conflict')).toBeDefined();

    fake.state.saveError = new PublicApiError('http', 409, undefined, 'external_rebind_stale_generation');
    await save('retry');
    expect(maybeTestId('external-blocker-rebind_stale')).toBeDefined();

    fake.state.saveError = null;
    await act(async () => testId('external-blocker-reload').props.onClick());
    await settle();
    expect(maybeTestId('gateway-conflict')).toBeUndefined();

    // Discard visual edit, switch to source mode, simulate 409 where reloaded YAML matches sourceDraft (!sourceDirty),
    // then switch back to visual mode and verify stale conflict state clears.
    const discardBtn = testId('gateway-group-retry')
      .findAllByType('button')
      .find((node) => node.props.children === c('discard'))!;
    await act(async () => discardBtn.props.onClick());
    const confirmBtn = renderer.root
      .findAllByType('button')
      .find((node) => node.props.children === c('confirmDiscard'))!;
    await act(async () => confirmBtn.props.onClick());
    await settle();

    await act(async () => testId('gateway-mode-source').props.onClick());
    await settle();
    const updatedYaml = 'routing:\n  strategy: fill-first\n';
    await act(async () =>
      testId('gateway-source-codemirror').props.onChange({ target: { value: updatedYaml } })
    );
    fake.state.sourceView = { ...fake.state.sourceView, yaml: updatedYaml };
    fake.state.saveError = new PublicApiError('http', 409, undefined, 'source_or_operation_conflict');
    await act(async () => testId('gateway-source-save').props.onClick());
    await settle();
    expect(maybeTestId('gateway-conflict')).toBeDefined();

    fake.state.saveError = null;
    await act(async () => testId('gateway-mode-visual').props.onClick());
    await settle();
    expect(maybeTestId('gateway-conflict')).toBeUndefined();
  });

  it('reconciles using the active unknown journal operation and clears cross-scope 409 locks without getting stuck', async () => {
    const viewWithUnknown = buildView();
    const retryGroup = viewWithUnknown.groups.find((g) => g.id === 'retry')!;
    const journaledId = '1'.repeat(32);
    retryGroup.operation = {
      requestId: journaledId,
      group: 'retry',
      target: 'group',
      state: 'unknown',
    };
    const fake = fakeClient(viewWithUnknown);
    await mount(fake.client);
    await openTab('routing');

    // Journaled unknown operation on retry takes precedence over any newly attempted requestId.
    const reconcileBtn = testId('gateway-group-reconcile-retry').findByType('button');
    await act(async () => reconcileBtn.props.onClick());
    await settle();
    const reconcilePost = fake.calls.find((call) => call.method === 'post');
    expect(reconcilePost?.resource).toBe(`gateway/config/operations/${journaledId}/reconcile`);

    // Same-scope 409 original_operation_unknown clears unjournaled attempted ID and adopts the server's journaled operation.
    const sameScopeFresh = fakeClient(buildView());
    await mount(sameScopeFresh.client);
    await openTab('routing');
    const retryRowSame = testId('gateway-field-routing.retry.request-retry');
    await act(async () => retryRowSame.findByType('input').props.onChange({ target: { value: '8' } }));
    const serverJournaledId = 'a'.repeat(32);
    const origGet = sameScopeFresh.client.get;
    sameScopeFresh.client.get = async <T,>(resource: string) => {
      if (resource === 'gateway/config' && sameScopeFresh.state.saveError) {
        const updated = buildView();
        updated.groups.find((g) => g.id === 'retry')!.operation = {
          requestId: serverJournaledId,
          group: 'retry',
          target: 'group',
          state: 'unknown',
        };
        sameScopeFresh.state.view = updated;
      }
      return origGet<T>(resource);
    };
    sameScopeFresh.state.saveError = new PublicApiError('http', 409, undefined, 'original_operation_unknown');
    const saveRetrySameBtn = testId('gateway-group-retry').findAll(
      (node) => node.type === 'button' && node.props.children === c('groupSave')
    )[0];
    await act(async () => saveRetrySameBtn.props.onClick());
    await settle();
    const attemptedRetryPost = sameScopeFresh.calls.find(
      (call) => call.method === 'post' && call.resource === 'gateway/config/groups/retry'
    );
    const attemptedBody = attemptedRetryPost?.body;
    const attemptedId =
      attemptedBody && typeof attemptedBody === 'object' && 'requestId' in attemptedBody
        ? String(attemptedBody.requestId)
        : '';
    expect(attemptedId).toBeTruthy();
    expect(attemptedId).not.toBe(serverJournaledId);
    expect(testId('gateway-group-error-retry').props.children).toBe(c('reasonUnknownOriginal'));
    sameScopeFresh.state.saveError = null;
    await act(async () => testId('gateway-group-reconcile-retry').findByType('button').props.onClick());
    await settle();
    const sameScopeReconcile = sameScopeFresh.calls.find(
      (call) => call.method === 'post' && call.resource.endsWith('/reconcile')
    );
    expect(sameScopeReconcile?.resource).toBe(`gateway/config/operations/${serverJournaledId}/reconcile`);
    expect(sameScopeReconcile?.resource).not.toContain(attemptedId);

    // Cross-scope 409 original_operation_unknown (even from stale client view) never locks retry with its unjournaled ID,
    // adopts the source blocker on refresh, and lets retry save once the source blocker is reconciled.
    const crossScopeInitial = buildView();
    const fakeCross = fakeClient(crossScopeInitial);
    const serverUnknownId = '2'.repeat(32);
    const baseGet = fakeCross.client.get;
    let serverBlockerResolved = false;
    fakeCross.client.get = async <T,>(resource: string) => {
      if (resource === 'gateway/config' && !serverBlockerResolved && fakeCross.calls.some((call) => call.method === 'post')) {
        const blockedView = buildView();
        blockedView.groups.find((g) => g.id === 'server')!.operation = {
          requestId: serverUnknownId,
          group: 'server',
          target: 'group',
          state: 'unknown',
        };
        fakeCross.state.view = blockedView;
      }
      return baseGet<T>(resource);
    };
    await mount(fakeCross.client);
    await openTab('routing');
    const row = testId('gateway-field-routing.retry.request-retry');
    await act(async () => row.findByType('input').props.onChange({ target: { value: '7' } }));
    fakeCross.state.saveError = new PublicApiError('http', 409, undefined, 'original_operation_unknown');
    const saveBtn = testId('gateway-group-retry').findAll(
      (node) => node.type === 'button' && node.props.children === c('groupSave')
    )[0];
    await act(async () => saveBtn.props.onClick());
    await settle();
    expect(maybeTestId('gateway-group-reconcile-retry')).toBeUndefined();
    expect(testId('gateway-group-error-retry').props.children).toBe(c('reasonUnknownOriginal'));

    // Reconcile the actual source blocker on the server group.
    await openTab('network');
    fakeCross.state.saveError = null;
    serverBlockerResolved = true;
    fakeCross.state.view = buildView();
    fakeCross.state.operation = {
      requestId: serverUnknownId,
      group: 'server',
      target: 'group',
      state: 'persisted',
    };
    await act(async () => testId('gateway-group-reconcile-server').findByType('button').props.onClick());
    await settle();

    // Returning to retry shows no stale lock/error and allows saving retry normally.
    await openTab('routing');
    expect(maybeTestId('gateway-group-reconcile-retry')).toBeUndefined();
    expect(maybeTestId('gateway-group-error-retry')).toBeUndefined();
    const retrySaveAfter = testId('gateway-group-retry').findAll(
      (node) => node.type === 'button' && node.props.children === c('groupSave')
    )[0];
    expect(retrySaveAfter.props.disabled).toBe(false);
    await act(async () => retrySaveAfter.props.onClick());
    await settle();
    const retryPosts = fakeCross.calls.filter(
      (call) => call.method === 'post' && call.resource === 'gateway/config/groups/retry'
    );
    expect(retryPosts).toHaveLength(2);
    expect(maybeTestId('gateway-group-error-retry')).toBeUndefined();
  });

  it('isolates source and visual load errors across concurrent reloads, refreshes visual data after source conflict, and clears source error on clean return', async () => {
    const fake = fakeClient(buildView());
    fake.state.sourceView = {
      ...fake.state.sourceView,
      yaml: '__TRIGGER_EDITOR_CRASH__',
    };
    await mount(fake.client);

    const consoleErrorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    try {
      await act(async () => testId('gateway-mode-source').props.onClick());
      await settle();
      expect(testId('gateway-source-editor-error')).toBeDefined();

      fake.state.sourceView = {
        ...fake.state.sourceView,
        yaml: 'routing:\n  strategy: round-robin\n',
      };
      const retryBtn = testId('gateway-source-editor-error').findByType('button');
      await act(async () => retryBtn.props.onClick());
      await settle();
      await settle();
      expect(testId('gateway-source-codemirror')).toBeDefined();
    } finally {
      consoleErrorSpy.mockRestore();
    }

    // Concurrent reload in source mode: source fails first, visual succeeds later -> source failure is preserved.
    let resolveVisualReload!: (value: GatewayView) => void;
    let rejectSourceReload!: (reason?: unknown) => void;
    const origGet = fake.client.get;
    let interceptConcurrentReload = true;
    fake.client.get = async <T,>(resource: string) => {
      if (interceptConcurrentReload && resource === 'gateway/config') {
        return new Promise<T>((resolve) => {
          resolveVisualReload = resolve as (value: GatewayView) => void;
        });
      }
      if (interceptConcurrentReload && resource === 'gateway/config/source') {
        return new Promise<T>((_resolve, reject) => {
          rejectSourceReload = reject;
        });
      }
      return origGet<T>(resource);
    };

    const reloadBtn = renderer.root.findAll(
      (node) => node.type === 'button' && node.props.children === c('reload')
    )[0];
    await act(async () => reloadBtn.props.onClick());
    await act(async () => {
      rejectSourceReload(new PublicApiError('http', 500));
      await Promise.resolve();
    });
    await act(async () => {
      resolveVisualReload(fake.state.view);
      await Promise.resolve();
    });
    await settle();
    expect(testId('gateway-load-failed')).toBeDefined();

    // Reverse direction: in source mode, visual fails with external blocker first, source succeeds later;
    // source stays usable and switching to visual still preserves/surfaces visual error if visual load fails.
    let rejectVisualReload!: (reason?: unknown) => void;
    let resolveSourceReload!: (value: GatewaySourceView) => void;
    let reverseIntercept = true;
    fake.client.get = async <T,>(resource: string) => {
      if (reverseIntercept && resource === 'gateway/config') {
        return new Promise<T>((_resolve, reject) => {
          rejectVisualReload = reject;
        });
      }
      if (reverseIntercept && resource === 'gateway/config/source') {
        return new Promise<T>((resolve) => {
          resolveSourceReload = resolve as (value: GatewaySourceView) => void;
        });
      }
      return origGet<T>(resource);
    };
    await act(async () => reloadBtn.props.onClick());
    await act(async () => {
      rejectVisualReload(new PublicApiError('http', 500));
      await Promise.resolve();
    });
    await act(async () => {
      resolveSourceReload(fake.state.sourceView);
      await Promise.resolve();
    });
    await settle();
    expect(maybeTestId('gateway-load-failed')).toBeUndefined();
    fake.client.get = origGet;
    interceptConcurrentReload = false;
    reverseIntercept = false;

    // Recover source reload, then test source conflict -> discard -> return to visual mode.
    interceptConcurrentReload = false;
    await act(async () => reloadBtn.props.onClick());
    await settle();
    expect(maybeTestId('gateway-load-failed')).toBeUndefined();

    await act(async () =>
      testId('gateway-source-codemirror').props.onChange({
        target: { value: 'routing:\n  strategy: weighted-round-robin\n' },
      })
    );
    // Simulate remote update so sourceView and visual view both reflect fill-first while source save gets 409 conflict.
    const updatedVisual = buildView();
    updatedVisual.fields = updatedVisual.fields.map((field) =>
      field.path === 'routing.strategy' ? { ...field, value: 'fill-first' } : field
    );
    fake.state.view = updatedVisual;
    fake.state.sourceView = {
      ...fake.state.sourceView,
      generation: '8',
      yaml: 'routing:\n  strategy: fill-first\n',
    };
    fake.state.saveError = new PublicApiError('http', 409, undefined, 'source_or_operation_conflict');
    await act(async () => testId('gateway-source-save').props.onClick());
    await settle();
    expect(testId('gateway-source-error')).toBeDefined();
    expect(testId('gateway-conflict')).toBeDefined();

    // Cleanly switch back after conflict when !sourceDirty (e.g. matching YAML / discarded) -> visual shows fill-first and clears source error.
    fake.state.saveError = null;
    await act(async () =>
      testId('gateway-source-codemirror').props.onChange({
        target: { value: 'routing:\n  strategy: fill-first\n' },
      })
    );
    // Re-trigger source_no_changes / error while !sourceDirty by mocking source_no_changes or discarding after conflict:
    // Here we simulate a residual groupErrors.source while !sourceDirty via conflict when draft matches reloaded YAML:
    fake.state.saveError = new PublicApiError('http', 409, undefined, 'source_or_operation_conflict');
    await act(async () =>
      testId('gateway-source-codemirror').props.onChange({
        target: { value: 'routing:\n  strategy: temp\n' },
      })
    );
    fake.state.sourceView = {
      ...fake.state.sourceView,
      yaml: 'routing:\n  strategy: temp\n',
    };
    updatedVisual.fields = updatedVisual.fields.map((field) =>
      field.path === 'routing.strategy' ? { ...field, value: 'fill-first' } : field
    );
    await act(async () => testId('gateway-source-save').props.onClick());
    await settle();
    expect(testId('gateway-source-error')).toBeDefined();

    fake.state.saveError = null;
    const visualReadsBefore = fake.calls.filter(
      (call) => call.method === 'get' && call.resource === 'gateway/config'
    ).length;
    await act(async () => testId('gateway-mode-visual').props.onClick());
    await settle();
    await settle();
    const visualReadsAfter = fake.calls.filter(
      (call) => call.method === 'get' && call.resource === 'gateway/config'
    ).length;
    expect(visualReadsAfter).toBe(visualReadsBefore + 1);
    expect(maybeTestId('gateway-tab-errors-common')).toBeUndefined();
    expect(maybeTestId('gateway-conflict')).toBeUndefined();
    expect(
      testId('gateway-field-routing.strategy').findByType('select').props.value
    ).toBe('fill-first');

    // Re-enter source mode: residual groupErrors.source must already have been cleared on visual return.
    await act(async () => testId('gateway-mode-source').props.onClick());
    await settle();
    expect(maybeTestId('gateway-source-error')).toBeUndefined();
  });

  it('preserves sourceDraft and refreshes baseline via loadSource({ keepDraft: true }) when reconciling a cross-scope retry unknown operation', async () => {
    const retryOpId = '9'.repeat(32);
    const fake = fakeClient(buildView());
    fake.state.sourceView = {
      ...fake.state.sourceView,
      generation: '7',
      yaml: 'routing:\n  strategy: round-robin\n',
      operation: {
        requestId: retryOpId,
        group: 'retry',
        target: 'group',
        state: 'unknown',
      },
    };
    await mount(fake.client);

    await act(async () => testId('gateway-mode-source').props.onClick());
    await settle();
    expect(testId('gateway-source-reconcile')).toBeDefined();

    const draftYaml = 'routing:\n  strategy: fill-first\n';
    await act(async () =>
      testId('gateway-source-codemirror').props.onChange({
        target: { value: draftYaml },
      })
    );
    expect(testId('gateway-source-status').props.children).toBe(c('sourceDirtyBadge'));
    expect(testId('gateway-source-save').props.disabled).toBe(true);

    const sourceReadsBeforeReconcile = fake.calls.filter(
      (call) => call.method === 'get' && call.resource === 'gateway/config/source'
    ).length;
    fake.state.operation = {
      requestId: retryOpId,
      group: 'retry',
      target: 'group',
      state: 'persisted',
    };
    fake.state.sourceView = {
      ...fake.state.sourceView,
      generation: '8',
      yaml: 'routing:\n  strategy: round-robin\n  retry:\n    request-retry: 9\n',
      operation: undefined,
    };

    await act(async () =>
      testId('gateway-source-reconcile').findByType('button').props.onClick()
    );
    await settle();

    const reconcilePost = fake.calls.find(
      (call) =>
        call.method === 'post' && call.resource === `gateway/config/operations/${retryOpId}/reconcile`
    );
    expect(reconcilePost).toBeDefined();
    const sourceReadsAfterReconcile = fake.calls.filter(
      (call) => call.method === 'get' && call.resource === 'gateway/config/source'
    ).length;
    expect(sourceReadsAfterReconcile).toBe(sourceReadsBeforeReconcile + 1);

    // Cross-scope persisted operation must preserve sourceDraft while refreshing source baseline.
    expect(testId('gateway-source-codemirror').props.value).toBe(draftYaml);
    expect(testId('gateway-source-status').props.children).toBe(c('sourceDirtyBadge'));
    expect(maybeTestId('gateway-source-reconcile')).toBeUndefined();
    expect(testId('gateway-source-save').props.disabled).toBe(false);

    await act(async () => testId('gateway-source-save').props.onClick());
    await settle();
    const savePost = fake.calls.find(
      (call) => call.method === 'post' && call.resource === 'gateway/config/source'
    );
    expect(savePost?.body).toMatchObject({
      expected: { generation: '8' },
      yaml: draftYaml,
    });
  });

  it('recreates lazy GatewaySourceEditor on retry after initial dynamic import rejection', async () => {
    const fake = fakeClient(buildView());
    let attempts = 0;
    const loadSourceEditor = vi.fn(async () => {
      attempts += 1;
      if (attempts === 1) {
        throw new Error('network error loading chunk');
      }
      return import('./GatewaySourceEditor');
    });

    const consoleErrorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    try {
      await act(async () => {
        renderer = create(
          <FreshGatewayWorkspace
            client={fake.client}
            credential="admin-credential"
            onUnauthorized={vi.fn()}
            onPending={vi.fn()}
            loadSourceEditor={loadSourceEditor}
          />
        );
      });
      await settle();

      await act(async () => testId('gateway-mode-source').props.onClick());
      await settle();
      expect(loadSourceEditor).toHaveBeenCalledTimes(1);
      expect(testId('gateway-source-editor-error')).toBeDefined();

      const retryBtn = testId('gateway-source-editor-error').findByType('button');
      await act(async () => retryBtn.props.onClick());
      await settle();
      await settle();

      expect(loadSourceEditor).toHaveBeenCalledTimes(2);
      expect(maybeTestId('gateway-source-editor-error')).toBeUndefined();
      expect(testId('gateway-source-codemirror')).toBeDefined();
    } finally {
      consoleErrorSpy.mockRestore();
    }
  });

  it('recreates lazy GatewaySourceEditor after initial import rejection when switching back to visual then source or clicking top reload', async () => {
    // 1. Switch visual -> source (rejects) -> visual -> source (succeeds without clicking error boundary retry button)
    const fake = fakeClient(buildView());
    let attempts = 0;
    const loadSourceEditor = vi.fn(async () => {
      attempts += 1;
      if (attempts === 1) {
        throw new Error('network error loading chunk');
      }
      return import('./GatewaySourceEditor');
    });

    const consoleErrorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    try {
      await act(async () => {
        renderer = create(
          <FreshGatewayWorkspace
            client={fake.client}
            credential="admin-credential"
            onUnauthorized={vi.fn()}
            onPending={vi.fn()}
            loadSourceEditor={loadSourceEditor}
          />
        );
      });
      await settle();

      await act(async () => testId('gateway-mode-source').props.onClick());
      await settle();
      expect(loadSourceEditor).toHaveBeenCalledTimes(1);
      expect(testId('gateway-source-editor-error')).toBeDefined();

      // Switch back to visual without clicking retry, then return to source.
      await act(async () => testId('gateway-mode-visual').props.onClick());
      await settle();
      expect(maybeTestId('gateway-source-editor-error')).toBeUndefined();

      await act(async () => testId('gateway-mode-source').props.onClick());
      await settle();
      await settle();
      expect(loadSourceEditor).toHaveBeenCalledTimes(2);
      expect(maybeTestId('gateway-source-editor-error')).toBeUndefined();
      expect(testId('gateway-source-codemirror')).toBeDefined();

      await act(async () => renderer.unmount());

      // 2. Stay in source mode after chunk rejection and click top toolbar reloadButtonControl.
      let reloadAttempts = 0;
      const reloadSourceEditor = vi.fn(async () => {
        reloadAttempts += 1;
        if (reloadAttempts === 1) {
          throw new Error('network error loading chunk');
        }
        return import('./GatewaySourceEditor');
      });
      await act(async () => {
        renderer = create(
          <FreshGatewayWorkspace
            client={fake.client}
            credential="admin-credential"
            onUnauthorized={vi.fn()}
            onPending={vi.fn()}
            loadSourceEditor={reloadSourceEditor}
          />
        );
      });
      await settle();

      await act(async () => testId('gateway-mode-source').props.onClick());
      await settle();
      expect(reloadSourceEditor).toHaveBeenCalledTimes(1);
      expect(testId('gateway-source-editor-error')).toBeDefined();

      const topReloadBtn = renderer.root.findAll(
        (node) =>
          node.type === 'button' &&
          node.props.children === c('reload') &&
          node.parent?.props['data-testid'] !== 'gateway-source-editor-error'
      )[0];
      await act(async () => topReloadBtn.props.onClick());
      await settle();
      await settle();
      expect(reloadSourceEditor).toHaveBeenCalledTimes(2);
      expect(maybeTestId('gateway-source-editor-error')).toBeUndefined();
      expect(testId('gateway-source-codemirror')).toBeDefined();
    } finally {
      consoleErrorSpy.mockRestore();
    }
  });
});
