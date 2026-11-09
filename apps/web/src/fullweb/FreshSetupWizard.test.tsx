import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { beforeEach, afterEach, expect, it, vi } from 'vitest';
import { FreshSetupWizard } from './FreshSetupWizard';
import { BOOTSTRAP_UI, createBootstrapTransport, type FreshOperation } from './bootstrapTransport';
import { setFullWebLanguage } from '../i18n/fullweb';

const identity = {
  admissionId: 'admission',
  installationId: 'installation',
  generation: 'generation',
  databasePath: '/data/manager.sqlite',
  operationId: 'operation',
};
const admin = 'Admin_0123456789-abcdefghijklmnopqrstuvwxyz!';
const sourceKey = 'CPA-only-secret';
const runtime = {
  runtimeIdentity: 'runtime',
  runtimeGeneration: '18446744073709551615',
  activeGatewayArtifact: { artifactId: 'sha256:' + 'a'.repeat(64) },
  state: 'ready',
};
let renderer: ReactTestRenderer;
beforeEach(() => {
  setFullWebLanguage('zh-CN');
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  vi.stubGlobal('window', { location: new URL('https://manager.example' + BOOTSTRAP_UI) });
  vi.stubGlobal('localStorage', {
    setItem: vi.fn(() => {
      throw new Error('No setup secrets in storage');
    }),
  });
  vi.stubGlobal('sessionStorage', {
    setItem: vi.fn(() => {
      throw new Error('No setup secrets in storage');
    }),
  });
});
afterEach(async () => {
  if (renderer) await act(async () => renderer.unmount());
  vi.unstubAllGlobals();
});
function byID(id: string) {
  return renderer.root.findByProps({ 'data-testid': id });
}
async function click(id: string) {
  await act(async () => {
    await byID(id).props.onClick();
  });
}
async function input(id: string, value: string | boolean) {
  await act(async () => byID(id).props.onChange({ target: { value, checked: value } }));
}
async function setup(
  options: {
    external?: boolean;
    adopt?: boolean;
    artifactRequired?: boolean;
    artifactRecheck503?: boolean;
    artifactRecoveryRequired?: boolean;
    artifactErrorCode?: string;
    artifactErrorStatus?: number;
    lost?: string;
    unresolved?: boolean;
    runtimePending?: boolean;
    rejectExternal?: boolean;
    loseIdentity?: boolean;
    partialRoute?: boolean;
    verifyPending?: boolean;
  } = {}
) {
  let operation: FreshOperation = {
    version: 1,
    identity,
    revision: 1,
    adminInitialized: false,
    productComplete: false,
    gatewayPending: true,
    routing: { committed: false },
  };
  let lost = false;
  let recovered = false;
  let externalRejected = false;
  let verificationRefused = false;
  let artifactRequired = options.artifactRequired === true;
  let artifactRecheckFail = false;
  const transport = vi.fn<typeof fetch>(async (url, init) => {
    const resource = new URL(String(url)).pathname.split('/api/v2/')[1];
    const body = init?.body ? JSON.parse(String(init.body)) : {};
    if (resource === 'claim/exchange')
      return Response.json({ token: 'installation-session', operation });
    expect(init?.headers).toMatchObject({ Authorization: 'Fresh installation-session' });
    if (resource === 'runtime/artifact') {
      if (artifactRecheckFail) {
        return Response.json({ code: 'runtime_unavailable' }, { status: 503 });
      }
      if (options.artifactErrorCode) {
        return Response.json(
          { code: options.artifactErrorCode },
          { status: options.artifactErrorStatus ?? 409 }
        );
      }
      return Response.json({
        schemaVersion: 1,
        pinned: {
          version: '8.0.55',
          artifactId: runtime.activeGatewayArtifact.artifactId,
        },
        active: artifactRequired
          ? null
          : {
              artifactId: runtime.activeGatewayArtifact.artifactId,
              origin: 'bundled',
            },
        required: artifactRequired,
        operation: options.artifactRecoveryRequired
          ? {
              id: 'op-recovery',
              source: 'upload',
              phase: 'failed',
              receivedBytes: 1024,
              totalBytes: 1024,
              code: 'recovery_required',
            }
          : null,
        cdnPresets: [],
      });
    }
    if (resource === 'runtime/artifact/probe') {
      return Response.json({ reachable: true, code: '' });
    }
    if (resource === 'runtime/artifact/acquire') {
      artifactRequired = false;
      if (options.artifactRecheck503) {
        artifactRecheckFail = true;
      }
      return Response.json({
        operation: {
          id: 'op-acquire',
          source: body.source || 'github',
          phase: 'succeeded',
          receivedBytes: 1024,
          totalBytes: 1024,
          code: '',
        },
      });
    }
    if (resource === 'installation/runtime')
      return Response.json({
        ...runtime,
        state:
          options.runtimePending && operation.strategy?.stage === 'committed'
            ? 'starting'
            : 'ready',
      });
    if (resource === 'admin/generate') return Response.json({ key: admin });
    if (resource === 'operation') {
      if (options.unresolved && lost && !recovered) throw new TypeError('Read unavailable');
      if (operation.routing.committed && String(url).includes(BOOTSTRAP_UI.replace('/ui', '/api/')))
        return new Response('', { status: 404 });
      return Response.json(operation);
    }
    if (resource === 'bootstrap/identity')
      throw new Error('Public identity must not carry a credential');
    if (resource === 'admin/commit') {
      expect(body.key).toBe(admin);
      operation = { ...operation, adminInitialized: true, revision: operation.revision + 1 };
    }
    if (resource === 'strategy') {
      expect(body.identity).toEqual(identity);
      expect(body.expectedRevision).toBe(operation.revision);
      if (options.verifyPending && body.strategy === 'new_managed' && !verificationRefused) {
        // The adapter refuses before it records a checkpoint, so the stored
        // operation stays exactly where the browser read it.
        verificationRefused = true;
        expect(operation.strategy).toBeUndefined();
        return Response.json(
          { Code: 'managed_destination_verification_required' },
          { status: 409 }
        );
      }
      if (body.strategy === 'connect_external') {
        if (options.rejectExternal && !externalRejected) {
          externalRejected = true;
          return new Response('', { status: 409 });
        }
        expect(body.managementKey).toBe(sourceKey);
        expect(body).not.toHaveProperty('key');
        operation = {
          ...operation,
          revision: operation.revision + 1,
          strategy: {
            strategy: body.strategy,
            stage: 'committed',
            targetIdentity: 'external:sha256:' + 'b'.repeat(64),
            targetGeneration: 1,
          },
        };
      } else {
        expect(String(init?.body)).toContain('"expectedRuntimeGeneration":18446744073709551615');
        operation = {
          ...operation,
          revision: operation.revision + 1,
          strategy: {
            strategy: body.strategy,
            stage: operation.strategy ? 'committed' : 'staged',
            targetIdentity: runtime.runtimeIdentity,
            targetGeneration: runtime.runtimeGeneration,
            targetArtifactId: runtime.activeGatewayArtifact.artifactId,
          },
        };
      }
    }
    if (resource === 'routing/prepare') {
      expect(operation.strategy?.stage).toBe('committed');
      operation = {
        ...operation,
        revision: operation.revision + 1,
        routing: {
          target: body.target,
          revision: 1,
          operationId: identity.operationId,
          stage: 'intent_persisted',
          committed: false,
        },
      };
    }
    if (resource === 'routing/activate') {
      if (options.partialRoute && !lost) {
        lost = true;
        operation = {
          ...operation,
          revision: operation.revision + 1,
          routing: { ...operation.routing, stage: 'route_activated' },
        };
        return new Response('', { status: 503 });
      }
      operation = {
        ...operation,
        revision: operation.revision + 1,
        routing: { ...operation.routing, stage: 'committed', committed: true },
      };
    }
    if (resource === 'complete') {
      expect(new URL(String(url)).pathname).toBe('/private/api/v2/complete');
      expect(operation.routing.committed).toBe(true);
      operation = { ...operation, revision: operation.revision + 1, productComplete: true };
    }
    if (resource === options.lost && !lost) {
      lost = true;
      throw new TypeError('Reply lost');
    }
    return Response.json(operation);
  });
  const fetcher = vi.fn<typeof fetch>((url, init) => {
    if (String(url).endsWith('/bootstrap/identity')) {
      expect(init?.headers).not.toHaveProperty('Authorization');
      if (options.loseIdentity && !recovered)
        return Promise.reject(new TypeError('Identity unavailable'));
      return Promise.resolve(
        Response.json(
          {
            installationId: identity.installationId,
            generation: identity.generation,
            operationId: identity.operationId,
            revision: 1,
            target: '/private',
            committed: operation.routing.committed,
          },
          { headers: { 'X-CPAMP-Route-Owner': 'manager' } }
        )
      );
    }
    return transport(url, init);
  });
  const client = createBootstrapTransport(fetcher);
  const initialOperation = await client.start({
    identity,
    claimGeneration: 1,
    token: 'claim-token',
    resume: false,
  });
  await act(async () => {
    renderer = create(<FreshSetupWizard initialOperation={initialOperation} transport={client} />);
  });
  if (options.external) await click('choose-connect_external');
  if (options.adopt) await click('choose-adopt_existing');
  await click('setup-next');
  await input('admin-key-input', admin);
  await input('route-target-input', '/private');
  if (options.external) {
    await input('external-base-url', 'http://cpa:8317');
    await input('external-management-key', sourceKey);
  }
  return {
    fetcher,
    transport,
    client,
    recover: () => {
      recovered = true;
    },
  };
}

it.each([false, true])(
  'keeps key, route and CPA in one confirmed journey; external=%s',
  async (external) => {
    const { transport } = await setup({ external });
    await click('setup-review');
    expect(JSON.stringify(renderer.toJSON())).not.toContain(admin);
    expect(JSON.stringify(renderer.toJSON())).not.toContain(sourceKey);
    expect(transport.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1); // only claim, no product writes
    await input('confirm-key-saved-checkbox', true);
    await click('setup-execute');
    expect(byID('enter-console').props.href).toBe('/private');
    expect(JSON.stringify(renderer.toJSON())).toContain('安装已完成');
    expect(localStorage.setItem).not.toHaveBeenCalled();
    expect(sessionStorage.setItem).not.toHaveBeenCalled();
    const writes = transport.mock.calls
      .filter(([, init]) => init?.method === 'POST')
      .map(([url]) => new URL(String(url)).pathname.split('/api/v2/')[1]);
    expect(writes).toEqual(
      external
        ? [
            'claim/exchange',
            'admin/commit',
            'strategy',
            'routing/prepare',
            'routing/activate',
            'complete',
          ]
        : [
            'claim/exchange',
            'admin/commit',
            'strategy',
            'strategy',
            'routing/prepare',
            'routing/activate',
            'complete',
          ]
    );
  }
);

it.each(['admin/commit', 'strategy', 'routing/activate', 'complete'])(
  'reconciles lost %s replies without repeating writes or logging in',
  async (lost) => {
    const { transport } = await setup({ external: true, lost });
    await click('setup-review');
    await input('confirm-key-saved-checkbox', true);
    await click('setup-execute');
    expect(byID('enter-console').props.href).toBe('/private');
    expect(
      transport.mock.calls.filter(
        ([url, init]) => init?.method === 'POST' && String(url).endsWith('/' + lost)
      )
    ).toHaveLength(1);
  }
);

it('blocks unconfirmed writes when both POST response and readback are lost', async () => {
  const { transport } = await setup({ external: true, lost: 'admin/commit', unresolved: true });
  await click('setup-review');
  await input('confirm-key-saved-checkbox', true);
  await click('setup-execute');
  expect(byID('setup-resume').props.disabled).toBe(true);
  await click('setup-refresh');
  expect(
    transport.mock.calls.filter(([url]) => String(url).endsWith('/admin/commit'))
  ).toHaveLength(1);
  expect(renderer.root.findAllByProps({ 'data-testid': 'enter-console' })).toHaveLength(0);
});

it('does not commit the route or finish while managed CPA is not ready', async () => {
  const { transport } = await setup({ runtimePending: true });
  await click('setup-review');
  await input('confirm-key-saved-checkbox', true);
  await click('setup-execute');
  expect(
    transport.mock.calls.some(
      ([url]) => String(url).endsWith('/routing/activate') || String(url).endsWith('/complete')
    )
  ).toBe(false);
  expect(JSON.stringify(renderer.toJSON())).toContain('CPA 正在启动');
});

it('keeps an unverified CPA readiness refusal resumable instead of freezing the installation', async () => {
  const { transport } = await setup({ verifyPending: true });
  await click('setup-review');
  await input('confirm-key-saved-checkbox', true);
  await click('setup-execute');
  // The refusal recorded no checkpoint, so the same operation stays continuable.
  expect(byID('setup-resume').props.disabled).toBe(false);
  expect(JSON.stringify(renderer.toJSON())).toContain('已保存的进度不会丢失');
  expect(renderer.root.findAllByProps({ 'data-testid': 'enter-console' })).toHaveLength(0);
  const strategyBodies = () =>
    transport.mock.calls
      .filter(([url]) => String(url).endsWith('/strategy'))
      .map(([, init]) => JSON.parse(String(init?.body)));
  const [refusedBody] = strategyBodies();
  expect(strategyBodies()).toHaveLength(1);
  await click('setup-resume');
  expect(byID('enter-console').props.href).toBe('/private');
  expect(
    transport.mock.calls.filter(([url]) => String(url).endsWith('/admin/commit'))
  ).toHaveLength(1);
  // The resume replays that exact request before it stages: same operation,
  // same expected revision and the same fixed target, so the server derives the
  // very same Stage/Start identity instead of a second Supervisor attempt.
  const bodies = strategyBodies();
  expect(bodies).toHaveLength(3); // refused, then staged and committed on resume
  expect(bodies[1]).toEqual(refusedBody);
  // Every resume body keeps the fixed target the refusal carried; only the
  // expected revision advances with the checkpoint the stage recorded.
  for (const body of bodies) {
    expect(body.expectedRuntimeIdentity).toBe(refusedBody.expectedRuntimeIdentity);
    expect(body.expectedRuntimeGeneration).toBe(refusedBody.expectedRuntimeGeneration);
    expect(body.expectedArtifactId).toBe(refusedBody.expectedArtifactId);
  }
  expect(
    transport.mock.calls.filter(([url]) => String(url).endsWith('/complete'))
  ).toHaveLength(1);
});

it.each(['zh-CN', 'zh-TW', 'en', 'ru'])(
  'explains the unverified readiness state in every locale (%s)',
  async (language) => {
    await setup({ verifyPending: true });
    setFullWebLanguage(language);
    await click('setup-review');
    await input('confirm-key-saved-checkbox', true);
    await click('setup-execute');
    expect(byID('setup-resume').props.disabled).toBe(false);
    expect(JSON.stringify(renderer.toJSON())).toContain(
      {
        'zh-CN': '已保存的进度不会丢失',
        'zh-TW': '已儲存的進度不會遺失',
        en: 'could not be verified yet',
        ru: 'пока нельзя проверить',
      }[language]
    );
  }
);

it('recovers the committed route using reads after activation and identity replies were both lost', async () => {
  const { transport, recover } = await setup({
    external: true,
    lost: 'routing/activate',
    unresolved: true,
    loseIdentity: true,
  });
  await click('setup-review');
  await input('confirm-key-saved-checkbox', true);
  await click('setup-execute');
  expect(byID('setup-resume').props.disabled).toBe(true);
  recover();
  await click('setup-refresh');
  expect(byID('setup-resume').props.disabled).toBe(false);
  await click('setup-resume');
  expect(byID('enter-console').props.href).toBe('/private');
  expect(
    transport.mock.calls.filter(([url]) => String(url).endsWith('/routing/activate'))
  ).toHaveLength(1);
  expect(
    transport.mock.calls.filter(([url]) => String(url).endsWith('/admin/commit'))
  ).toHaveLength(1);
});

it('allows correcting a rejected external connection without recreating the saved administrator', async () => {
  const { transport } = await setup({ external: true, rejectExternal: true });
  await click('setup-review');
  await input('confirm-key-saved-checkbox', true);
  await click('setup-execute');
  expect(renderer.root.findAllByProps({ 'data-testid': 'admin-key-input' })).toHaveLength(0);
  expect(byID('external-base-url').props.value).toBe('http://cpa:8317');
  expect(byID('external-management-key').props.value).toBe(sourceKey);
  await click('setup-review');
  expect(byID('setup-execute').props.disabled).toBe(true);
  await input('confirm-key-saved-checkbox', true);
  await click('setup-execute');
  expect(byID('enter-console').props.href).toBe('/private');
  expect(
    transport.mock.calls.filter(([url]) => String(url).endsWith('/admin/commit'))
  ).toHaveLength(1);
});

it('allows explicit same-operation route recovery only after reading the persisted partial checkpoint', async () => {
  const { transport } = await setup({ external: true, partialRoute: true });
  await click('setup-review');
  await input('confirm-key-saved-checkbox', true);
  await click('setup-execute');
  expect(byID('setup-resume').props.disabled).toBe(true);
  expect(
    transport.mock.calls.filter(([url]) => String(url).endsWith('/routing/activate'))
  ).toHaveLength(1);
  await click('setup-refresh');
  expect(byID('setup-resume').props.disabled).toBe(false);
  // Reading never attempts another activation by itself.
  expect(
    transport.mock.calls.filter(([url]) => String(url).endsWith('/routing/activate'))
  ).toHaveLength(1);
  await click('setup-resume');
  expect(byID('enter-console').props.href).toBe('/private');
  const activations = transport.mock.calls.filter(([url]) =>
    String(url).endsWith('/routing/activate')
  );
  expect(activations).toHaveLength(2);
  expect(activations.map(([, init]) => JSON.parse(String(init?.body)).identity)).toEqual([
    identity,
    identity,
  ]);
  expect(
    transport.mock.calls.filter(([url]) => String(url).endsWith('/admin/commit'))
  ).toHaveLength(1);
});

it('validates key and path before confirmation and does not submit an unconfirmed plan', async () => {
  const { transport } = await setup();
  await input('admin-key-input', 'weak');
  await click('setup-review');
  expect(renderer.root.findAllByProps({ 'data-testid': 'setup-execute' })).toHaveLength(0);
  await input('admin-key-input', admin);
  await input('route-target-input', '/v1');
  await click('setup-review');
  expect(renderer.root.findAllByProps({ 'data-testid': 'setup-execute' })).toHaveLength(0);
  await input('route-target-input', '/private');
  await click('setup-review');
  expect(byID('setup-execute').props.disabled).toBe(true);
  await click('setup-execute');
  expect(transport.mock.calls.some(([url]) => String(url).endsWith('/admin/commit'))).toBe(false);
});

it('generates a key only on request and requires fresh confirmation after editing it', async () => {
  const { transport } = await setup();
  await input('admin-key-input', '');
  await click('generate-key-btn');
  expect(byID('admin-key-input').props.value).toBe(admin);
  expect(byID('admin-key-input').props.type).toBe('text');
  await click('setup-review');
  await input('confirm-key-saved-checkbox', true);
  await act(async () =>
    renderer.root
      .findAllByType('button')
      .find((node) => node.children.includes('上一步'))!
      .props.onClick()
  );
  await input('admin-key-input', admin + 'changed');
  await click('setup-review');
  expect(byID('setup-execute').props.disabled).toBe(true);
  expect(
    transport.mock.calls.filter(([url]) => String(url).endsWith('/admin/generate'))
  ).toHaveLength(1);
  expect(transport.mock.calls.some(([url]) => String(url).endsWith('/admin/commit'))).toBe(false);
});

it.each(['zh-CN', 'zh-TW', 'en'])('keeps the unified journey localized (%s)', async (language) => {
  await setup({ external: true });
  setFullWebLanguage(language);
  await click('setup-review');
  expect(JSON.stringify(renderer.toJSON())).toContain(
    language === 'en'
      ? 'Review installation'
      : language === 'zh-TW'
        ? '確認安裝資訊'
        : '确认安装信息'
  );
  await input('confirm-key-saved-checkbox', true);
  await click('setup-execute');
  expect(byID('enter-console').props.href).toBe('/private');
  expect(JSON.stringify(renderer.toJSON())).toContain(
    language === 'en'
      ? 'Sign in and enter CPAMP'
      : language === 'zh-TW'
        ? '登入並進入 CPAMP'
        : '登录并进入 CPAMP'
  );
});

it('uses the maintained shell and shared CSS classes across configuration, review and completion', async () => {
  const { readFileSync } = (
    globalThis as unknown as {
      process: {
        getBuiltinModule: (name: string) => {
          readFileSync: (path: URL, encoding: string) => string;
        };
      };
    }
  ).process.getBuiltinModule('node:fs');
  const css =
    readFileSync(new URL('./carrier.css', import.meta.url), 'utf8') +
    readFileSync(new URL('../styles/components.scss', import.meta.url), 'utf8');
  const defined = new Set(Array.from(css.matchAll(/\.([a-zA-Z0-9_-]+)/g), (match) => match[1]));
  const check = () => {
    for (const node of renderer.root.findAll((node) => typeof node.props.className === 'string')) {
      for (const name of node.props.className.split(/\s+/))
        if (name) expect(defined.has(name), `Missing CSS class .${name}`).toBe(true);
    }
  };
  await setup({ artifactRequired: true });
  check();
  await click('setup-review');
  check();
  await click('artifact-source-cdn');
  check();
  await click('artifact-source-upload');
  check();
  await setup({ external: true });
  check();
  await click('setup-review');
  check();
  await input('confirm-key-saved-checkbox', true);
  await click('setup-execute');
  check();
});

function strategyBodies(transport: { mock: { calls: unknown[][] } }) {
  return transport.mock.calls
    .filter(([url, init]) => {
      const options = init as { method?: string } | undefined;
      return options?.method === 'POST' && String(url).endsWith('/strategy');
    })
    .map(([, init]) => JSON.parse(String((init as { body?: unknown }).body)));
}

it('leaves the plugin opt-in off by default and submits no plugin choice', async () => {
  const { transport } = await setup();
  expect(byID('plugin-optin-checkbox').props.checked).toBe(false);
  await click('setup-review');
  // The default-off decision is asserted as behaviour, not as copy: the review
  // carries the plugins row with its label and value cells, the control is
  // unchecked, and the submitted body below omits the key entirely. Pinning a
  // translated word would make a copy edit look like a behaviour change.
  const reviewRow = renderer.root.findByProps({ 'data-testid': 'review-plugins' });
  const cells = reviewRow.findAll((node) => node.type === 'dd' || node.type === 'dt');
  expect(cells.length).toBe(2);
  expect(cells.map((cell) => cell.props.children).every((value) => String(value).length > 0)).toBe(
    true
  );
  await input('confirm-key-saved-checkbox', true);
  await click('setup-execute');
  expect(byID('enter-console').props.href).toBe('/private');
  const bodies = strategyBodies(transport);
  expect(bodies.length).toBeGreaterThan(0);
  for (const body of bodies) {
    expect(body.strategy).toBe('new_managed');
    expect(body).not.toHaveProperty('pluginsEnabled');
  }
});

it('submits the opt-in exactly once per New Managed attempt', async () => {
  const { transport } = await setup();
  await input('plugin-optin-checkbox', true);
  expect(byID('plugin-optin-checkbox').props.checked).toBe(true);
  await click('setup-review');
  // The review states the decision it will submit, not one particular word for
  // it: the row exists and reports the enabled state of the opt-in.
  // The row exists and reports the enabled state the wizard will submit. The
  // assertion is on the rendered structure plus the submitted body below, not on
  // one language's word for "enabled".
  const reviewRow = renderer.root.findByProps({ 'data-testid': 'review-plugins' });
  const cells = reviewRow.findAll((node) => node.type === 'dd' || node.type === 'dt');
  expect(cells.length).toBe(2);
  await input('confirm-key-saved-checkbox', true);
  await click('setup-execute');
  expect(byID('enter-console').props.href).toBe('/private');
  const bodies = strategyBodies(transport);
  expect(bodies.map((body) => body.pluginsEnabled)).toEqual(
    bodies.map(() => true)
  );
  // Every retried attempt carries the same persisted decision, and nothing else
  // rides along with it.
  for (const body of bodies) expect(body.strategy).toBe('new_managed');
});

it('never offers or submits the plugin opt-in for an External connection', async () => {
  const { transport } = await setup({ external: true });
  expect(renderer.root.findAllByProps({ 'data-testid': 'plugin-optin-checkbox' })).toHaveLength(0);
  await click('setup-review');
  await input('confirm-key-saved-checkbox', true);
  await click('setup-execute');
  expect(byID('enter-console').props.href).toBe('/private');
  const bodies = strategyBodies(transport);
  expect(bodies).toHaveLength(1);
  expect(bodies[0].strategy).toBe('connect_external');
  expect(bodies[0]).not.toHaveProperty('pluginsEnabled');
  // External never offers the decision, so the review has no plugin row either.
  expect(
    renderer.root.findAllByProps({ 'data-testid': 'review-plugins' })
  ).toHaveLength(0);
});

it('shows FreshArtifactStep and blocks execute when artifact is required, then continues to review on ready', async () => {
  const { transport } = await setup({ artifactRequired: true });
  await click('setup-review');

  expect(renderer.root.findAllByProps({ 'data-testid': 'fresh-artifact-step' })).toHaveLength(1);
  expect(renderer.root.findAllByProps({ 'data-testid': 'confirm-key-saved-checkbox' })).toHaveLength(0);
  expect(byID('setup-execute').props.disabled).toBe(true);
  expect(
    transport.mock.calls.some(([url]) => String(url).endsWith('/installation/runtime'))
  ).toBe(false);

  // Prepare engine -> onReady re-runs review() automatically -> shows review summary
  await click('artifact-acquire-btn');
  expect(renderer.root.findAllByProps({ 'data-testid': 'fresh-artifact-step' })).toHaveLength(0);
  expect(renderer.root.findAllByProps({ 'data-testid': 'confirm-key-saved-checkbox' })).toHaveLength(1);
  expect(
    transport.mock.calls.some(([url]) => String(url).endsWith('/installation/runtime'))
  ).toBe(true);
});

it('proceeds unchanged when artifact is not required with no extra request beyond the status read', async () => {
  const { transport } = await setup({ artifactRequired: false });
  const callsBeforeReview = transport.mock.calls.length;
  await click('setup-review');

  const reviewResources = transport.mock.calls
    .slice(callsBeforeReview)
    .map(([url]) => new URL(String(url)).pathname.split('/api/v2/')[1]);
  expect(reviewResources).toEqual(['runtime/artifact', 'installation/runtime']);
  expect(renderer.root.findAllByProps({ 'data-testid': 'fresh-artifact-step' })).toHaveLength(0);
  expect(renderer.root.findAllByProps({ 'data-testid': 'confirm-key-saved-checkbox' })).toHaveLength(1);
});

it('never reads runtime/artifact for External and shows mapped error on unsupported_platform for Managed', async () => {
  const { transport: externalTransport } = await setup({
    external: true,
    artifactErrorCode: 'unsupported_platform',
    artifactErrorStatus: 409,
  });
  await click('setup-review');
  expect(
    externalTransport.mock.calls.some(([url]) => String(url).includes('/runtime/artifact'))
  ).toBe(false);
  expect(renderer.root.findAllByProps({ 'data-testid': 'confirm-key-saved-checkbox' })).toHaveLength(1);

  const { transport: managedTransport } = await setup({
    external: false,
    artifactErrorCode: 'unsupported_platform',
    artifactErrorStatus: 409,
  });
  await click('setup-review');
  expect(
    managedTransport.mock.calls.some(([url]) => String(url).endsWith('/runtime/artifact'))
  ).toBe(true);
  const alertNode = renderer.root.findByProps({ role: 'alert' });
  expect(JSON.stringify(alertNode.children)).not.toContain('初始化尚未完成');
  expect(renderer.root.findAllByProps({ 'data-testid': 'setup-execute' })).toHaveLength(0);
});

it('runs the adopt artifact gate before committing admin key or observing target', async () => {
  const { transport } = await setup({ adopt: true, artifactRequired: true });
  await click('setup-review');

  expect(renderer.root.findAllByProps({ 'data-testid': 'fresh-artifact-step' })).toHaveLength(1);
  expect(renderer.root.findAllByProps({ 'data-testid': 'adopt-source-url' })).toHaveLength(0);
  expect(
    transport.mock.calls.some(([url]) => String(url).endsWith('/admin/commit'))
  ).toBe(false);
  expect(
    transport.mock.calls.some(([url]) => String(url).endsWith('/installation/runtime'))
  ).toBe(false);

  await click('artifact-acquire-btn');
  expect(renderer.root.findAllByProps({ 'data-testid': 'fresh-artifact-step' })).toHaveLength(0);
  expect(renderer.root.findAllByProps({ 'data-testid': 'adopt-source-url' })).toHaveLength(1);
  expect(
    transport.mock.calls.some(([url]) => String(url).endsWith('/admin/commit'))
  ).toBe(true);
  // observeTarget still only runs when staging inventory, not during review gate
  expect(
    transport.mock.calls.some(([url]) => String(url).endsWith('/installation/runtime'))
  ).toBe(false);
});

it.each([false, true])(
  'keeps gate and Execute disabled with no admin commit when post-acquire status re-check returns 503 (adopt=%s)',
  async (adopt) => {
    const { transport } = await setup({
      adopt,
      artifactRequired: true,
      artifactRecheck503: true,
    });
    await click('setup-review');
    expect(renderer.root.findAllByProps({ 'data-testid': 'fresh-artifact-step' })).toHaveLength(1);
    expect(byID('setup-execute').props.disabled).toBe(true);

    // Acquisition succeeds, triggering onReady -> review() re-checks GET runtime/artifact -> 503
    await click('artifact-acquire-btn');

    expect(renderer.root.findAllByProps({ 'data-testid': 'fresh-artifact-step' })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-testid': 'confirm-key-saved-checkbox' })).toHaveLength(0);
    expect(renderer.root.findAllByProps({ 'data-testid': 'adopt-source-url' })).toHaveLength(0);
    expect(byID('setup-execute').props.disabled).toBe(true);

    // Even if onClick is invoked directly on setup-execute, no admin/commit or strategy write goes out
    await click('setup-execute');
    expect(
      transport.mock.calls.some(([url]) => String(url).endsWith('/admin/commit'))
    ).toBe(false);
    expect(
      transport.mock.calls.some(([url]) => String(url).endsWith('/installation/runtime'))
    ).toBe(false);
  }
);

it('keeps gate active and shows restart guidance when status reports recovery_required even with active artifact', async () => {
  const { transport } = await setup({
    artifactRequired: false,
    artifactRecoveryRequired: true,
  });
  await click('setup-review');

  expect(renderer.root.findAllByProps({ 'data-testid': 'fresh-artifact-step' })).toHaveLength(1);
  expect(byID('setup-execute').props.disabled).toBe(true);
  expect(byID('artifact-error').props['data-error-code']).toBe('recovery_required');
  expect(
    transport.mock.calls.some(([url]) => String(url).endsWith('/installation/runtime'))
  ).toBe(false);
});
