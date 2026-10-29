import { useState } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { FreshSetupWizard } from './FreshSetupWizard';
import { FreshManagedRuntime } from './FreshManagedRuntime';
import {
  BOOTSTRAP_UI,
  createBootstrapTransport,
  type FreshOperation,
  type FreshStrategyCheckpoint,
} from './bootstrapTransport';
import type { AdoptionSummary } from './freshAdoption';
import type { createPublicApiClient } from './publicApi';
import { setFullWebLanguage } from '../i18n/fullweb';
import { adoptReasonText, adoptText } from '../i18n/freshAdopt';

const identity = {
  admissionId: 'admission',
  installationId: 'installation',
  generation: 'generation',
  databasePath: '/data/manager.sqlite',
  operationId: 'operation',
};
const admin = 'Admin_0123456789-abcdefghijklmnopqrstuvwxyz!';
const sourceKey = 'adopt-source-management-key';
const artifactId = 'sha256:' + 'a'.repeat(64);
const runtime = {
  runtimeIdentity: 'runtime',
  runtimeGeneration: 4,
  activeGatewayArtifact: { artifactId },
  state: 'ready',
};
const adoptCheckpoint: FreshStrategyCheckpoint = {
  strategy: 'adopt_existing',
  targetIdentity: 'runtime',
  targetGeneration: 4,
  targetArtifactId: artifactId,
  stage: 'staged',
  sourceClass: 'network_existing',
  sourceIdentity: 'external:sha256:' + 'b'.repeat(64),
  destinationConnectionIdentity: 'external:sha256:' + 'c'.repeat(64),
  pluginsEnabled: false,
  adoptionResources: ['client-api-keys', 'cpa-auth-files', 'cpa-config-plugins'],
  adoptionDecisions: [
    {
      ResourceRef: 'client-api-keys',
      Kind: 'api_keys',
      Scope: 'client_api_keys',
      ReadEvidenceRef: 'preflight:client-api-keys-read',
      ReplayEvidenceRef: 'preflight:client-api-keys-replay',
      VerifyEvidenceRef: 'preflight:client-api-keys-verify',
      Qualification: 'qualified',
      ManualAction: '',
      Disposition: 'migrate',
    },
    {
      ResourceRef: 'cpa-config-plugins',
      Kind: 'cpa_configuration',
      Scope: 'management_api_fields',
      ReadEvidenceRef: 'preflight:cpa-config-plugins-read',
      ReplayEvidenceRef: '',
      VerifyEvidenceRef: '',
      Qualification: 'qualified',
      ManualAction: 'reconfigure_after_adoption',
      Disposition: 'manual_action',
    },
  ],
};

// One recorded run: a copy that needs no retry, with one manual item and one
// unknown reason, which is what the localization fallback is for.
const finishedRun: AdoptionSummary = {
  items: [
    { item: 'credential:a.json', kind: 'credential', outcome: 'migrated', reason: '', instruction: '', retryable: false },
    {
      item: 'credential:b.json',
      kind: 'credential',
      outcome: 'manual',
      reason: 'runtime_only_credential',
      instruction: 'This credential exists only in the source CPA process and has no file.',
      retryable: false,
    },
    {
      item: 'config:routing.strategy',
      kind: 'config_leaf',
      outcome: 'failed',
      reason: 'download_failed',
      instruction: 'Copy this credential file into the destination CPA.',
      retryable: true,
    },
    {
      item: 'config:unknown',
      kind: 'config_leaf',
      outcome: 'manual',
      reason: 'reason_this_browser_does_not_know',
      instruction: '',
      retryable: false,
    },
  ],
  planned: 4,
  finished: true,
  running: false,
  retryable: 1,
  counts: { migrated: 1, manual: 2, failed: 1, absent_at_source: 0 },
};

let renderer: ReactTestRenderer;
beforeEach(() => {
  setFullWebLanguage('en');
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  vi.stubGlobal('window', { location: new URL('https://manager.example' + BOOTSTRAP_UI) });
  vi.stubGlobal('localStorage', {
    setItem: vi.fn(() => {
      throw new Error('No adoption secrets in storage');
    }),
  });
  vi.stubGlobal('sessionStorage', {
    setItem: vi.fn(() => {
      throw new Error('No adoption secrets in storage');
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
function maybe(id: string) {
  return renderer.root.findAllByProps({ 'data-testid': id });
}
async function click(id: string) {
  await act(async () => {
    await byID(id).props.onClick();
  });
}
async function input(id: string, value: string | boolean) {
  await act(async () => byID(id).props.onChange({ target: { value, checked: value } }));
}
// Let the flow's poll timer fire without relying on wall-clock sleeps in a
// specific order: the summary is queued by the fake CPA.
// The browser's timer is the flow's own clock, so a test waits on a real one.
async function settle(ms = 10) {
  await act(() => new Promise<void>((resolve) => setTimeout(resolve, ms)));
}
function text() {
  return JSON.stringify(renderer.toJSON());
}

/**
 * The Adopt server as the browser sees it: the two-POST inventory, the recorded
 * summary and the completion, all stateful so a replay answers like the real one.
 */
async function adoptServer(options: {
  resume?: boolean;
  pluginChecked?: boolean;
  summaries?: AdoptionSummary[];
  completions?: { status: number; body: Record<string, unknown> }[];
} = {}) {
  // A resume is a browser that reloaded a committed adoption: the checkpoint is
  // durable and the source key is not.
  let operation: FreshOperation = options.resume
    ? {
        version: 1,
        identity,
        revision: 5,
        adminInitialized: true,
        productComplete: false,
        gatewayPending: true,
        routing: {
          operationId: identity.operationId,
          revision: 1,
          target: '/console',
          stage: 'committed',
          committed: true,
        },
        strategy: { ...adoptCheckpoint, stage: 'committed' },
      }
    : {
        version: 1,
        identity,
        revision: 1,
        adminInitialized: false,
        productComplete: false,
        gatewayPending: true,
        routing: { committed: false },
      };
  const summaries = [...(options.summaries ?? [finishedRun])];
  const completions = [...(options.completions ?? [])];
  const strategyBodies: Record<string, unknown>[] = [];
  let summaryReads = 0;
  const fetcher = vi.fn<typeof fetch>(async (url, init) => {
    const resource = new URL(String(url)).pathname.split('/api/v2/')[1];
    const body = init?.body ? (JSON.parse(String(init.body)) as Record<string, unknown>) : {};
    if (resource === 'claim/exchange')
      return Response.json({ token: 'installation-session', operation });
    if (String(url).endsWith('/bootstrap/identity'))
      return Response.json(
        {
          installationId: identity.installationId,
          generation: identity.generation,
          operationId: identity.operationId,
          revision: 1,
          target: '/console',
          committed: operation.routing.committed,
        },
        { headers: { 'X-CPAMP-Route-Owner': 'manager' } }
      );
    expect(String((init?.headers as Record<string, string>).Authorization)).toMatch(/^Fresh /);
    if (resource === 'installation/runtime') return Response.json(runtime);
    if (resource === 'admin/commit') {
      expect(body.key).toBe(admin);
      operation = { ...operation, adminInitialized: true, revision: operation.revision + 1 };
      return Response.json(operation);
    }
    if (resource === 'operation') return Response.json(operation);
    if (resource === 'strategy') {
      expect(body.identity).toEqual(identity);
      expect(body.expectedRevision).toBe(operation.revision);
      expect(body.strategy).toBe('adopt_existing');
      expect(body.adoptSourceClass).toBe('network_existing');
      expect(body.baseUrl).toBe('http://source-cpa:8317');
      expect(body.managementKey).toBe(sourceKey);
      expect(body.expectedRuntimeIdentity).toBe(runtime.runtimeIdentity);
      expect(body.expectedRuntimeGeneration).toBe(runtime.runtimeGeneration);
      expect(body.expectedArtifactId).toBe(artifactId);
      expect(typeof body.pluginsEnabled).toBe('boolean');
      strategyBodies.push(body);
      if (operation.strategy?.stage === 'committed') {
        // A committed replay is the resume path: nothing is rewritten.
        return Response.json({ ...operation, migrationState: 'running' });
      }
      if (operation.strategy?.stage === 'staged') {
        operation = {
          ...operation,
          revision: operation.revision + 1,
          strategy: { ...operation.strategy, stage: 'committed' },
        };
        return Response.json({ ...operation, migrationState: 'running' });
      }
      operation = {
        ...operation,
        revision: operation.revision + 1,
        strategy: { ...adoptCheckpoint },
      };
      return Response.json(operation);
    }
    if (resource === 'installation/adoption') {
      summaryReads += 1;
      const summary = summaries.length > 1 ? summaries.shift()! : summaries[0];
      return Response.json(summary);
    }
    if (resource === 'routing/prepare') {
      operation = {
        ...operation,
        revision: operation.revision + 1,
        routing: {
          operationId: identity.operationId,
          revision: 1,
          target: '/console',
          stage: 'intent_persisted',
          committed: false,
        },
      };
      return Response.json(operation);
    }
    if (resource === 'routing/activate') {
      operation = {
        ...operation,
        revision: operation.revision + 1,
        routing: { ...operation.routing, stage: 'committed', committed: true },
      };
      return Response.json(operation);
    }
    if (resource === 'complete') {
      const refusal = completions.shift();
      if (refusal) return Response.json(refusal.body, { status: refusal.status });
      operation = { ...operation, revision: operation.revision + 1, productComplete: true };
      return Response.json(operation);
    }
    throw new Error('unexpected resource ' + resource);
  });
  const client = createBootstrapTransport(fetcher);
  const initialOperation = await client.start({
    identity,
    claimGeneration: 1,
    token: 'claim-token',
    resume: options.resume === true,
  });
  await act(async () => {
    renderer = create(<FreshSetupWizard initialOperation={initialOperation} transport={client} />);
  });
  if (!options.resume) {
    await click('choose-adopt_existing');
    await click('setup-next');
    await input('admin-key-input', admin);
    await input('route-target-input', '/console');
    await click('setup-review');
    await input('adopt-source-url', 'http://source-cpa:8317');
    await input('adopt-management-key', sourceKey);
    if (options.pluginChecked) await input('adopt-plugin-optin', true);
  }
  return {
    fetcher,
    strategyBodies,
    summaryReads: () => summaryReads,
    posts: (name: string) =>
      fetcher.mock.calls.filter(([url, init]) => init?.method === 'POST' && String(url).endsWith('/' + name)),
    reads: (name: string) =>
      fetcher.mock.calls.filter(([url]) => String(url).endsWith('/' + name)),
  };
}

it('walks source, inventory, migration and completion without writing the commit early', async () => {
  const server = await adoptServer();
  await click('adopt-read-inventory');
  // The first POST is the inventory; the inventory names what will and will not
  // be copied, and the commit button stays disabled until it is confirmed.
  expect(server.strategyBodies).toHaveLength(1);
  expect(text()).toContain(adoptText('migrateGroup'));
  expect(text()).toContain('Client API keys');
  expect(text()).toContain('Plugin settings');
  expect(byID('adopt-confirm-migrate').props.disabled).toBe(true);
  expect(server.strategyBodies[0].pluginsEnabled).toBe(false);

  await input('adopt-confirm-inventory', true);
  await click('adopt-confirm-migrate');
  await settle();

  expect(server.strategyBodies).toHaveLength(2);
  // The commit repeats the inventory's own inputs: same attempt, not a new one.
  // Only the CAS fence moves, because the inventory POST itself advanced it.
  const [first, second] = server.strategyBodies;
  expect({ ...second, expectedRevision: first.expectedRevision }).toEqual(first);
  expect(text()).toContain(adoptText('progress', { settled: 4, planned: 4 }));
  expect(text()).toContain(adoptText('migratedGroup'));
  expect(text()).toContain(adoptText('manualBadge'));

  await click('adopt-complete');
  expect(byID('enter-console').props.href).toBe('/console');
  expect(localStorage.setItem).not.toHaveBeenCalled();
  expect(sessionStorage.setItem).not.toHaveBeenCalled();
});

it('sends the plugin opt-in the operator chose, not the default', async () => {
  const server = await adoptServer({ pluginChecked: true });
  await click('adopt-read-inventory');
  expect(server.strategyBodies[0].pluginsEnabled).toBe(true);
});

it('stops polling once the recorded run is finished', async () => {
  const server = await adoptServer();
  await click('adopt-read-inventory');
  await input('adopt-confirm-inventory', true);
  await click('adopt-confirm-migrate');
  await settle();
  const settled = server.summaryReads();
  expect(settled).toBeGreaterThan(0);
  await settle(60);
  expect(server.summaryReads()).toBe(settled);
});

it('asks for the source key again after a reload and resumes from the recorded prefix', async () => {
  // A committed checkpoint with no key in memory: exactly the state a browser
  // reload leaves behind.
  const server = await adoptServer({
    resume: true,
    summaries: [
      { items: [], planned: 2, finished: false, running: false, retryable: 0, counts: { migrated: 0, manual: 0, failed: 0, absent_at_source: 0 } },
      finishedRun,
    ],
  });
  await settle();
  // The source form comes back, the plugin choice shown is the recorded one, and
  // it cannot be changed here.
  expect(maybe('adopt-source-url')).toHaveLength(1);
  expect(byID('adopt-plugin-optin').props.disabled).toBe(true);
  await input('adopt-source-url', 'http://source-cpa:8317');
  await input('adopt-management-key', sourceKey);
  await click('adopt-resume');
  await settle();
  expect(server.strategyBodies).toHaveLength(1);
  expect(server.strategyBodies[0].pluginsEnabled).toBe(false);
  expect(text()).toContain(adoptText('progress', { settled: 4, planned: 4 }));
});

it('keeps polling on a running refusal and only finishes over failed items when accepted', async () => {
  const server = await adoptServer({
    completions: [
      { status: 409, body: { code: 'adopt_migration_running' } },
      { status: 409, body: { code: 'adopt_migration_retryable', retryable: 1 } },
    ],
  });
  await click('adopt-read-inventory');
  await input('adopt-confirm-inventory', true);
  await click('adopt-confirm-migrate');
  await settle();
  const reads = server.summaryReads();

  await click('adopt-complete');
  await settle();
  expect(text()).toContain(adoptText('runningRefusal'));
  expect(server.summaryReads()).toBeGreaterThan(reads);
  expect(maybe('adopt-accept-retryable')).toHaveLength(0);

  await click('adopt-complete');
  await settle();
  expect(text()).toContain(adoptText('retryableRefusal', { count: 1 }));
  expect(maybe('adopt-accept-retryable')).toHaveLength(1);
  expect(maybe('enter-console')).toHaveLength(0);

  await input('adopt-accept-retryable', true);
  await click('adopt-complete');
  expect(JSON.parse(String(server.posts('complete')[2][1]!.body))).toEqual({
    acceptRetryable: true,
  });
  expect(byID('enter-console').props.href).toBe('/console');
});

it('names an unproven destination instead of offering a retry that cannot work', async () => {
  await adoptServer({
    completions: [{ status: 503, body: { code: 'adopt_destination_unproven' } }],
  });
  await click('adopt-read-inventory');
  await input('adopt-confirm-inventory', true);
  await click('adopt-confirm-migrate');
  await settle();
  await click('adopt-complete');
  expect(text()).toContain(adoptText('unprovenRefusal'));
  expect(maybe('enter-console')).toHaveLength(0);
});

it('localizes a known reason and falls back to one sentence for an unknown one', async () => {
  await adoptServer();
  await click('adopt-read-inventory');
  await input('adopt-confirm-inventory', true);
  await click('adopt-confirm-migrate');
  await settle();
  expect(text()).toContain(adoptReasonText('runtime_only_credential'));
  expect(text()).toContain(adoptReasonText('reason_this_browser_does_not_know'));
  expect(adoptReasonText('reason_this_browser_does_not_know')).toBe(
    adoptText('reasonFallback')
  );
  // The server's English instruction is still available, behind the disclosure.
  expect(text()).toContain('This credential exists only in the source CPA process');
});

// --- the Console carrier ---------------------------------------------------

// The Console path holds the same adoption but talks to the canonical API, so the
// flow must be reachable there too — and must render, never `null`.
async function mountManaged(operation: FreshOperation, get: ReturnType<typeof vi.fn>) {
  const post = vi.fn();
  const client = { get, post } as unknown as ReturnType<typeof createPublicApiClient>;
  const pending = vi.fn();
  function Harness() {
    const [current, update] = useState(operation);
    return (
      <FreshManagedRuntime
        guided
        client={client}
        credential="admin-secret"
        operation={current}
        onOperation={update}
        onPending={pending}
        onContinue={() => undefined}
        onUnauthorized={() => undefined}
      />
    );
  }
  await act(async () => {
    renderer = create(<Harness />);
  });
  return { post, client };
}

it('renders the Adopt flow for an adopt checkpoint instead of an empty card', async () => {
  const committed: FreshOperation = {
    identity,
    revision: 9,
    adminInitialized: true,
    gatewayPending: true,
    routing: {
      operationId: identity.operationId,
      revision: 1,
      target: '/console',
      stage: 'committed',
      committed: true,
    },
    strategy: { ...adoptCheckpoint, stage: 'committed' },
  };
  const get = vi.fn(async (resource: string) =>
    resource === 'installation/adoption' ? finishedRun : committed
  );
  await mountManaged(committed, get);
  await settle();
  expect(maybe('fresh-adopt-flow').length).toBeGreaterThan(0);
  expect(text()).toContain(adoptText('progress', { settled: 4, planned: 4 }));
  expect(get.mock.calls.map(([resource]) => resource)).toContain('installation/adoption');
});

it('sends the New Managed plugin opt-in from the Console path', async () => {
  const staged: FreshOperation = {
    identity,
    revision: 4,
    adminInitialized: true,
    gatewayPending: true,
    routing: {
      operationId: identity.operationId,
      revision: 1,
      target: '/console',
      stage: 'committed',
      committed: true,
    },
    strategy: {
      strategy: 'new_managed',
      targetIdentity: runtime.runtimeIdentity,
      targetGeneration: runtime.runtimeGeneration,
      targetArtifactId: artifactId,
      stage: 'staged',
    },
  };
  const get = vi.fn(async (resource: string) =>
    resource === 'installation/runtime' ? runtime : staged
  );
  const { post } = await mountManaged(staged, get);
  post.mockResolvedValue({ ...staged, revision: 5, strategy: { ...staged.strategy, stage: 'committed' } });
  await act(async () => {
    await byID('install-runtime').props.onClick();
  });
  expect(post.mock.calls[0][1]).toEqual({
    identity,
    expectedRevision: 4,
    strategy: 'new_managed',
    expectedRuntimeIdentity: runtime.runtimeIdentity,
    expectedRuntimeGeneration: runtime.runtimeGeneration,
    expectedArtifactId: artifactId,
  });
});
