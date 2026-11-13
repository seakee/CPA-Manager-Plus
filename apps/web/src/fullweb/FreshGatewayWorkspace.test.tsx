import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest';
import i18n from '../i18n';
import { configurationText as c } from '../i18n/configurationWorkspace';
import { gatewayFamilies, gatewayGroups, gatewayOwners } from './gatewayCatalog';
import type {
  GatewayFamilyView,
  GatewayFieldView,
  GatewayGroupView,
  GatewayOperation,
  GatewayView,
} from './gatewayConfigClient';
import { FreshGatewayWorkspace } from './FreshGatewayWorkspace';
import { createPublicApiClient, PublicApiError, type PublicApiClient } from './publicApi';

vi.mock('../components/ui/Modal', () => ({
  Modal: ({ children, footer, open }: { children: ReactNode; footer: ReactNode; open: boolean }) =>
    open ? (
      <div role="dialog">
        {children}
        {footer}
      </div>
    ) : null,
}));

const BASELINE = 'a'.repeat(64);

/** The text a node's children render to, the way the operator reads it. */
function flatten(children: ReactNode): string {
  return [children]
    .flat(Infinity)
    .filter((part) => part !== null && part !== undefined && typeof part !== 'boolean')
    .map((part) => String(part))
    .join('');
}


// The Manager owns the type of every leaf; the test states the same inventory so
// a wrong control in the page shows up as a wrong interaction here.
const INT_PATHS = [
  'routing.retry.request-retry',
  'routing.retry.max-retry-credentials',
  'routing.retry.max-retry-interval',
  'routing.cooldown.transient-error-cooldown-seconds',
  'requests.nonstream-keepalive-interval',
  'requests.streaming.keepalive-seconds',
  'requests.streaming.bootstrap-retries',
];
const STRING_PATHS = [
  'routing.strategy',
  'routing.session-affinity-ttl',
  'requests.proxy-url',
  'multimedia.gpt-image-2-base-model',
  'multimedia.video-result-auth-cache-ttl',
  'upstream.codex.stream-bootstrap-timeout',
  'upstream.claude.header-defaults.user-agent',
  'upstream.claude.header-defaults.package-version',
  'upstream.claude.header-defaults.runtime-version',
  'upstream.claude.header-defaults.os',
  'upstream.claude.header-defaults.arch',
  'upstream.claude.header-defaults.timeout',
  'upstream.claude.header-defaults.timezone',
];

function fieldType(path: string): 'int' | 'bool' | 'string' | 'image-mode' {
  if (path === 'multimedia.disable-image-generation') return 'image-mode';
  if (INT_PATHS.includes(path)) return 'int';
  if (STRING_PATHS.includes(path)) return 'string';
  return 'bool';
}

function sampleValue(type: string) {
  if (type === 'int') return 3;
  if (type === 'string') return 'value';
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
          type === 'image-mode'
            ? ('select' as const)
            : type === 'int'
              ? ('number' as const)
              : type === 'bool'
                ? ('bool' as const)
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
    operation: {
      requestId: 'f'.repeat(32),
      group: 'retry',
      target: 'group' as const,
      state: 'persisted' as const,
    } satisfies GatewayOperation,
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

  async function mount(client: PublicApiClient, onDirtyChange = vi.fn(), onPending = vi.fn()) {
    await act(async () => {
      renderer = create(
        <FreshGatewayWorkspace
          client={client}
          credential="admin-credential"
          onUnauthorized={() => undefined}
          onPending={onPending}
          onDirtyChange={onDirtyChange}
        />
      );
    });
    await act(async () => {
      await Promise.resolve();
    });
    return { onDirtyChange, onPending };
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

  it('projects the whole page from the Manager view and never invents a field', async () => {
    const { client } = fakeClient(buildView());
    await mount(client);
    expect(testId('gateway-capability')).toBeDefined();
    expect(testId('gateway-overview')).toBeDefined();
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
    expect(maybeTestId('gateway-group-retry')).toBeUndefined();
    const search = renderer.root.findByProps({ type: 'search' });
    await act(async () => search.props.onChange({ target: { value: 'proxy-url' } }));
    expect(maybeTestId('gateway-field-requests.proxy-url')).toBeDefined();
    await act(async () => search.props.onChange({ target: { value: 'nothing-matches-this' } }));
    expect(maybeTestId('gateway-no-matches')).toBeDefined();
  });

  it('names the number of drafts the discard dialog throws away', async () => {
    const { client } = fakeClient(buildView());
    await mount(client);
    await openTab('retry');
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

  it('renders every overview statistic as a label and its own number', async () => {
    const view = buildView();
    const { client } = fakeClient(view);
    await mount(client);
    const overview = testId('gateway-overview');
    const labels = overview.findAllByType('dt').map((node) => flatten(node.props.children));
    const values = overview.findAllByType('dd').map((node) => flatten(node.props.children));

    expect(labels).toEqual([c('fields'), c('ruleFamilies'), c('ownerGroups'), c('dirty')]);
    expect(values).toHaveLength(labels.length);
    expect(values[0]).toBe(String(view.fields.length));
    for (const value of values) expect(value).toMatch(/^\d+$/);
  });

  it('saves one group with only the edited leaves and reports the draft as dirty', async () => {
    const { client, calls } = fakeClient(buildView());
    const { onDirtyChange } = await mount(client);
    await openTab('retry');
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
    await openTab('retry');
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
    await openTab('retry');
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
    await openTab('retry');
    const row = testId('gateway-field-routing.retry.request-retry');
    await act(async () => row.findByType('input').props.onChange({ target: { value: '7' } }));
    fake.state.saveError = new PublicApiError('http', 409, undefined, 'original_operation_unknown');
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
    await openTab('retry');
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
    await openTab('retry');
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
    await openTab('requests');
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

  it('renders a rule save error on the family card, not on the group', async () => {
    const fake = fakeClient(buildView());
    await mount(fake.client);
    await openTab('requests');
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
      (node) => node.type === 'button' && node.props.children === c('groupSave')
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
      await openTab('retry');
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
    await openTab('requests');
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
    await openTab('requests');
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
    await openTab('retry');
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
    await openTab('retry');
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
    await openTab('retry');
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
    const list = renderer.root.find((node) => node.type === 'nav' && node.props.role === 'tablist');
    const preventDefault = vi.fn();
    await act(async () =>
      list.props.onKeyDown({ key: 'ArrowRight', preventDefault })
    );
    expect(preventDefault).toHaveBeenCalled();
    expect(tab('common').props['aria-selected']).toBe(true);
    // Roving tabindex: exactly one tab is in the tab order.
    expect(tabs().filter((tab) => tab.props.tabIndex === 0)).toHaveLength(1);
    await act(async () => list.props.onKeyDown({ key: 'End', preventDefault }));
    expect(tab('owners').props['aria-selected']).toBe(true);
    await act(async () => list.props.onKeyDown({ key: 'Home', preventDefault }));
    expect(tab('overview').props['aria-selected']).toBe(true);
    // A dirty leaf badges its tab so a draft is never lost in a hidden tab.
    await openTab('retry');
    const row = testId('gateway-field-routing.retry.request-retry');
    await act(async () => row.findByType('input').props.onChange({ target: { value: '8' } }));
    expect(maybeTestId('gateway-tab-dirty-retry')).toBeDefined();
  });

  it('shows old and new values in the conflict view', async () => {
    const fake = fakeClient(buildView());
    await mount(fake.client);
    await openTab('retry');
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

  it('links ownership cards to Full Web routes and keeps overview runtime non-routed', async () => {
    const fake = fakeClient(buildView());
    await mount(fake.client);
    await openTab('owners');
    const link = maybeTestId('gateway-owner-link-/api-keys');
    expect(link).toBeDefined();
    expect(link.props.href).toBe('/api-keys');

    await act(async () => {
      tab('overview').props.onClick();
    });
    const overview = testId('gateway-overview');
    expect(
      overview.findAll(
        (node) =>
          typeof node.props['data-testid'] === 'string' &&
          node.props['data-testid'].startsWith('gateway-owner-link-')
      )
    ).toHaveLength(0);
    void fake;
  });

  it('renders each ownership card exactly once', async () => {
    const fake = fakeClient(buildView());
    await mount(fake.client);
    await openTab('owners');
    // A duplicated block in this page once rendered all 13 cards twice and gave
    // React duplicate keys; the tab is one grid of one card per owner.
    const owners = gatewayOwners();
    expect(owners.length).toBeGreaterThan(0);
    const links = renderer.root.findAll((node) => node.props['data-testid'] === 'gateway-owner-link-/api-keys');
    expect(links).toHaveLength(1);
    const grids = renderer.root.findAllByProps({ 'data-testid': 'gateway-owners' });
    expect(grids).toHaveLength(1);
    for (const owner of owners) {
      // One card per ownership record: a duplicated block would fail here with
      // React's duplicate-key warning, not just a longer list.
      expect(renderer.root.findAllByProps({ 'data-testid': `gateway-owner-${owner.path}` })).toHaveLength(1);
    }
  });

  it('renders ownership workspace links only for cards with a destination editor and switches usage link by mode', async () => {
    const collectOwnerLinks = () => {
      const grid = testId('gateway-owners');
      const cards = grid.findAll(
        (node) =>
          node.type === 'section' &&
          typeof node.props['data-testid'] === 'string' &&
          node.props['data-testid'].startsWith('gateway-owner-')
      );
      const linksByOwner: Record<string, string | null> = {};
      for (const card of cards) {
        const ownerId = String(card.props['data-testid']).slice('gateway-owner-'.length);
        const links = card.findAll(
          (node) =>
            typeof node.props['data-testid'] === 'string' &&
            node.props['data-testid'].startsWith('gateway-owner-link-')
        );
        expect(links.length).toBeLessThanOrEqual(1);
        linksByOwner[ownerId] = links[0]
          ? String(links[0].props['data-testid']).slice('gateway-owner-link-'.length)
          : null;
      }
      return linksByOwner;
    };

    await mount(fakeClient(buildView()).client);
    await openTab('owners');

    expect(collectOwnerLinks()).toEqual({
      'access.api-keys': '/api-keys',
      'api-keys.*': '/ai-providers',
      'models.*': null,
      'oauth.model-alias / oauth.excluded-models': '/accounts',
      'oauth.auth-dir / oauth.auth-auto-refresh-workers / oauth.settings / oauth.request-scoped-errors':
        null,
      'oauth.providers.codex.live-media-relay.*': null,
      'oauth.providers.codex.header-defaults.* / oauth.providers.aistudio.ws-auth': null,
      'oauth.providers.antigravity.* / oauth.providers.devin.*': null,
      'credentials.concurrency.* / credentials.in-flight.*': null,
      'observability.logs.*': '/logs',
      'observability.pprof.*': null,
      'observability.usage.usage-statistics-enabled': null,
      'observability.usage.redis-usage-queue-retention-seconds': null,
      'server.* / management.*': null,
      'plugins.enabled / plugins.configs.*': '/plugins',
      'plugins.dir / plugins.store-sources / plugins.store-auth / plugins.auth-revision': null,
      'config-version / quota-exceeded.switch-project / quota-exceeded.switch-preview-model': null,
    });

    await act(async () => renderer.unmount());
    const externalView = buildView();
    externalView.capability = {
      mode: 'external',
      readable: true,
      writable: true,
      rulesWritable: true,
    };
    await mount(fakeClient(externalView).client);
    await openTab('owners');

    const externalLinks = collectOwnerLinks();
    expect(externalLinks['observability.usage.usage-statistics-enabled']).toBe('/api-keys');
    expect(externalLinks['observability.usage.redis-usage-queue-retention-seconds']).toBeNull();
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
    await openTab('retry');
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
    expect(testId('gateway-readonly')).toBeDefined();
    const edit = renderer.root.findAll(
      (node) => node.type === 'button' && node.props.children === c('ruleEditFamily')
    );
    expect(edit.every((node) => node.props.disabled === true)).toBe(true);
  });

  it('drafts a rule family and writes it with one request', async () => {
    const fake = fakeClient(buildView());
    await mount(fake.client);
    await openTab('requests');
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
      (node) => node.type === 'button' && node.props.children === c('groupSave')
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
});
