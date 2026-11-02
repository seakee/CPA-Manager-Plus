import { createHash } from 'node:crypto';
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
import type { AdoptionItem, AdoptionSummary } from './freshAdoption';
import type { createPublicApiClient } from './publicApi';
import { setFullWebLanguage } from '../i18n/fullweb';
import { adoptGuidanceText, adoptReasonText, adoptText } from '../i18n/freshAdopt';

const identity = {
  admissionId: 'admission',
  installationId: 'installation',
  generation: 'generation',
  databasePath: '/data/manager.sqlite',
  operationId: 'operation',
};
const admin = 'Admin_0123456789-abcdefghijklmnopqrstuvwxyz!';
const sourceKey = 'adopt-source-management-key';
const sourceAddress = 'http://source-cpa:8317';

// The record's own identity for one item: the server digests the item name, so a
// fixture builds the same digest. `withIdentity` is how every recorded item in
// this file carries the two fields the reply always states.
const itemID = (name: string) => createHash('sha256').update(name).digest('hex');
const withIdentity = (
  items: Array<Omit<AdoptionItem, 'id' | 'left'> & { left?: number }>
): AdoptionItem[] => items.map((item) => ({ id: itemID(item.item), left: 0, ...item }));
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
  items: withIdentity([
    { item: 'credential:a.json', kind: 'credential', outcome: 'migrated', reason: '', retryable: false },
    {
      item: 'credential:b.json',
      kind: 'credential',
      outcome: 'manual',
      reason: 'runtime_only_credential',
      retryable: false,
    },
    {
      item: 'config:routing.strategy',
      kind: 'config_leaf',
      outcome: 'failed',
      reason: 'download_failed',
      retryable: true,
    },
    {
      item: 'config:unknown',
      kind: 'config_leaf',
      outcome: 'manual',
      reason: 'reason_this_browser_does_not_know',
      retryable: false,
    },
  ]),
  planned: 4,
  finished: true,
  running: false,
  retryable: 1,
  counts: { migrated: 1, manual: 2, failed: 1, absent_at_source: 0 },
};

// A run that stopped with planned items it never recorded. Nothing is retryable
// and the run is finished, so the old completion rule called this a finished
// console — the one state whose only working action is a resume.
const shortfallRun: AdoptionSummary = {
  items: withIdentity([
    { item: 'credential:a.json', kind: 'credential', outcome: 'migrated', reason: '', retryable: false },
    { item: 'credential:b.json', kind: 'credential', outcome: 'migrated', reason: '', retryable: false },
    { item: 'config:routing.strategy', kind: 'config_leaf', outcome: 'migrated', reason: '', retryable: false },
  ]),
  planned: 5,
  finished: true,
  running: false,
  retryable: 0,
  counts: { migrated: 3, manual: 0, failed: 0, absent_at_source: 0 },
};

// Every outcome at once. The copy count on screen has to be the wire's
// `counts.migrated`, and a resource the source never had is not a copy.
const allOutcomes: AdoptionSummary = {
  items: withIdentity([
    { item: 'credential:copied-a.json', kind: 'credential', outcome: 'migrated', reason: '', retryable: false },
    { item: 'config:routing.strategy', kind: 'config_leaf', outcome: 'migrated', reason: '', retryable: false },
    {
      item: 'credential:runtime-only.json',
      kind: 'credential',
      outcome: 'manual',
      reason: 'runtime_only_credential',
      retryable: false,
    },
    { item: 'credential:left-1.json', kind: 'credential', outcome: 'absent_at_source', reason: '', retryable: false },
    { item: 'credential:left-2.json', kind: 'credential', outcome: 'absent_at_source', reason: '', retryable: false },
    { item: 'credential:left-3.json', kind: 'credential', outcome: 'absent_at_source', reason: '', retryable: false },
  ]),
  planned: 6,
  finished: true,
  running: false,
  retryable: 0,
  counts: { migrated: 2, manual: 1, failed: 0, absent_at_source: 3 },
};

// A settled run with nothing left over: the only record that may be called a
// complete adoption, and only once the installation itself is complete.
const cleanRun: AdoptionSummary = {
  items: withIdentity([
    { item: 'credential:a.json', kind: 'credential', outcome: 'migrated', reason: '', retryable: false },
    { item: 'config:routing.strategy', kind: 'config_leaf', outcome: 'migrated', reason: '', retryable: false },
  ]),
  planned: 2,
  finished: true,
  running: false,
  retryable: 0,
  counts: { migrated: 2, manual: 0, failed: 0, absent_at_source: 0 },
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
  // A browser that reloaded a staged adoption: the inventory is durable, the body
  // the page would commit is not.
  staged?: boolean;
  pluginChecked?: boolean;
  summaries?: AdoptionSummary[];
  completions?: { status: number; body: Record<string, unknown> }[];
  // The next N progress reads answer a plain 500, which is what a lost route or a
  // refused credential looks like to the page: no bounded refusal envelope.
  readFailures?: number;
  // The inventory POST is staged by the server and its reply is lost on the way
  // back: the ambiguous write the page has to reconcile.
  loseStrategyReply?: boolean;
  // The revision the migration checkpoints wrote after the page last read the
  // operation.
  checkpointAdvance?: number;
  // The first POST's reply is lost and the server finishes staging only when the
  // operation is read for the Nth time afterwards: a slow source read whose
  // staging lands between two of the page's own reads.
  lateStage?: number;
} = {}) {
  // A resume is a browser that reloaded a committed adoption: the checkpoint is
  // durable and the source key is not.
  let operation: FreshOperation = options.resume || options.staged
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
        strategy: { ...adoptCheckpoint, stage: options.staged ? 'staged' : 'committed' },
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
  let readFailures = options.readFailures ?? 0;
  let lostReply = false;
  let lateLost = false;
  let pendingStage = 0;
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
    if (resource === 'operation') {
      if (pendingStage > 0) {
        pendingStage -= 1;
        if (pendingStage === 0)
          operation = { ...operation, revision: operation.revision + 1, strategy: { ...adoptCheckpoint } };
      }
      return Response.json(operation);
    }
    if (resource === 'strategy') {
      expect(body.identity).toEqual(identity);
      expect(body.expectedRevision).toBe(operation.revision);
      expect(body.strategy).toBe('adopt_existing');
      expect(body.adoptSourceClass).toBe('network_existing');
      expect(body.expectedRuntimeIdentity).toBe(runtime.runtimeIdentity);
      expect(body.expectedRuntimeGeneration).toBe(runtime.runtimeGeneration);
      expect(body.expectedArtifactId).toBe(artifactId);
      expect(typeof body.pluginsEnabled).toBe('boolean');
      strategyBodies.push(body);
      // A staged record is this attempt: the route refuses another plugin choice,
      // address or key by code, before it stages or commits anything.
      if (operation.strategy?.stage) {
        if (body.pluginsEnabled !== operation.strategy.pluginsEnabled)
          return Response.json({ Code: 'adopt_plugin_optin_conflict' }, { status: 409 });
        if (body.managementKey !== sourceKey || body.baseUrl !== sourceAddress)
          return Response.json({ Code: 'fresh_strategy_owner_conflict' }, { status: 409 });
      }
      if (options.lateStage && !lateLost) {
        // The reply is lost while the source read is still running; the staging
        // itself lands on a later read.
        lateLost = true;
        pendingStage = options.lateStage;
        throw new TypeError('Failed to fetch');
      }
      if (options.loseStrategyReply && !lostReply && !operation.strategy) {
        // The server staged the attempt and the reply is what was lost.
        lostReply = true;
        operation = { ...operation, revision: operation.revision + 1, strategy: { ...adoptCheckpoint } };
        throw new TypeError('Failed to fetch');
      }
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
      if (readFailures > 0) {
        readFailures -= 1;
        return new Response('unavailable', { status: 500 });
      }
      if (options.checkpointAdvance && summaryReads === 1) {
        // A migration checkpoint is a durable write: the record's revision moves
        // while the page is only reading progress.
        operation = { ...operation, revision: operation.revision + options.checkpointAdvance };
      }
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
  if (!options.resume && !options.staged) {
    await click('choose-adopt_existing');
    await click('setup-next');
    await input('admin-key-input', admin);
    await input('route-target-input', '/console');
    await click('setup-review');
    await input('adopt-source-url', sourceAddress);
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

it('never turns a re-read of a staged inventory into the committing POST', async () => {
  const server = await adoptServer();
  await click('adopt-read-inventory');
  expect(server.strategyBodies).toHaveLength(1);
  // Back is a read of the page, not a new attempt. The durable inventory is still
  // there, so reading it again states it instead of sending the second POST the
  // server would take as the commit.
  await click('adopt-inventory-back');
  await click('adopt-read-inventory');
  await settle();
  expect(server.strategyBodies).toHaveLength(1);
  expect(maybe('adopt-inventory')).toHaveLength(1);
  expect(maybe('adopt-migration')).toHaveLength(0);
  // Only the explicit confirmation commits.
  await input('adopt-confirm-inventory', true);
  await click('adopt-confirm-migrate');
  await settle();
  expect(server.strategyBodies).toHaveLength(2);
  expect(maybe('adopt-migration')).toHaveLength(1);
});

it('lands on the confirmation when the inventory reply was lost', async () => {
  // The server staged the attempt; the reply never arrived. The page cannot claim
  // the read failed — the next read of the record shows the inventory it wrote.
  const server = await adoptServer({ loseStrategyReply: true });
  await click('adopt-read-inventory');
  await settle();
  expect(server.strategyBodies).toHaveLength(1);
  expect(maybe('adopt-inventory')).toHaveLength(1);
  expect(maybe('adopt-migration')).toHaveLength(0);
  await input('adopt-confirm-inventory', true);
  await click('adopt-confirm-migrate');
  await settle();
  expect(server.strategyBodies).toHaveLength(2);
  expect(maybe('adopt-migration')).toHaveLength(1);
});

it('sends the inputs on screen, not the pre-staging body, after a lost first reply', async () => {
  // The first POST's reply is lost and the staging lands after the page's own
  // reconciliation read, so the page stays on the source step still holding the
  // body it built before the record owned the attempt. The operator reads the
  // failure as a mistyped address and opts in to plugins.
  const server = await adoptServer({ lateStage: 2 });
  await click('adopt-read-inventory');
  await settle();
  expect(maybe('adopt-read-inventory')).toHaveLength(1);
  expect(byID('adopt-plugin-optin').props.disabled).toBe(false);

  await input('adopt-source-url', 'http://source-cpa-2:8317');
  await input('adopt-plugin-optin', true);
  await click('adopt-read-inventory');
  await settle();

  // The read found the record already staged: the choice the record holds is the
  // one the attempt accepts, so the page states it locked, with its own
  // explanation, instead of carrying the tick that was never recorded.
  expect(maybe('adopt-inventory')).toHaveLength(0);
  expect(byID('adopt-plugin-optin').props.disabled).toBe(true);
  expect(byID('adopt-plugin-optin').props.checked).toBe(false);
  expect(text()).toContain(adoptText('pluginsLocked'));

  await click('adopt-read-inventory');
  await settle();
  expect(maybe('adopt-inventory')).toHaveLength(1);

  await input('adopt-confirm-inventory', true);
  await click('adopt-confirm-migrate');
  await settle();

  // What the operator confirmed is what left the page: the corrected address,
  // and the plugin value the checkbox showed.
  const posted = server.strategyBodies.at(-1)!;
  expect(posted.baseUrl).toBe('http://source-cpa-2:8317');
  expect(posted.pluginsEnabled).toBe(false);
  // The corrected address is not this record's attempt, so the server refuses it
  // by code: the page names the refusal and claims no migration.
  expect(text()).toContain(adoptText('keyMismatch'));
  expect(maybe('adopt-migration')).toHaveLength(0);
});

it('does not commit when a lost staging reply lands between two reads', async () => {
  // The first POST's reply is lost while the source read is still running, so the
  // staging lands after the page already decided nothing was staged. The second
  // POST must therefore be refused as stale or land as a staging — never be
  // accepted as the commit of an attempt the operator has not confirmed.
  const server = await adoptServer({ lateStage: 3 });
  await click('adopt-read-inventory');
  await settle();
  expect(server.strategyBodies).toHaveLength(1);
  expect(maybe('adopt-migration')).toHaveLength(0);

  await click('adopt-read-inventory');
  await settle();
  // The record answers the run, and the confirmation is still the operator's.
  expect(maybe('adopt-migration')).toHaveLength(0);
  expect(maybe('adopt-inventory')).toHaveLength(1);
  expect(server.strategyBodies.length).toBeGreaterThanOrEqual(1);
});

it('lets a refused source key be corrected without reloading the page', async () => {
  // A staged reload: the inventory is durable, the key is not, and the operator
  // types the wrong one.
  const server = await adoptServer({ staged: true });
  await settle();
  await input('adopt-source-url', sourceAddress);
  await input('adopt-management-key', 'a-mistyped-source-key-000000000000');
  await click('adopt-use-key');
  await settle();
  await input('adopt-confirm-inventory', true);
  await click('adopt-confirm-migrate');
  await settle();
  // The server refuses this attempt as another one. The body it refused is gone,
  // so the source form is back and the confirmation has nothing to offer.
  expect(text()).toContain(adoptText('keyMismatch'));
  expect(maybe('adopt-management-key')).toHaveLength(1);
  expect(byID('adopt-confirm-migrate').props.disabled).toBe(true);
  expect(maybe('adopt-migration')).toHaveLength(0);

  // Correcting the key is the whole recovery: the recorded inventory is reused.
  await input('adopt-management-key', sourceKey);
  await click('adopt-use-key');
  await settle();
  await input('adopt-confirm-inventory', true);
  await click('adopt-confirm-migrate');
  await settle();
  expect(server.strategyBodies.at(-1)?.managementKey).toBe(sourceKey);
  expect(maybe('adopt-migration')).toHaveLength(1);
});

it('locks the recorded plugin choice after Back instead of committing an old one', async () => {
  const server = await adoptServer();
  await click('adopt-read-inventory');
  expect(server.strategyBodies[0].pluginsEnabled).toBe(false);
  await click('adopt-inventory-back');
  // The record owns the opt-in from the staging write on: the checkbox shows the
  // recorded value, cannot be changed, and says why.
  expect(byID('adopt-plugin-optin').props.checked).toBe(false);
  expect(byID('adopt-plugin-optin').props.disabled).toBe(true);
  expect(text()).toContain(adoptText('pluginsLocked'));

  await click('adopt-read-inventory');
  await settle();
  await input('adopt-confirm-inventory', true);
  await click('adopt-confirm-migrate');
  await settle();
  // The commit sends the value the screen showed.
  expect(server.strategyBodies[1].pluginsEnabled).toBe(false);
  expect(maybe('adopt-migration')).toHaveLength(1);
});

it('sends the address the operator corrected after Back', async () => {
  // The same attempt is staged, so a corrected address is not silently dropped:
  // it is what the confirmation sends, and the server's own refusal is what the
  // operator sees.
  const server = await adoptServer();
  await click('adopt-read-inventory');
  await click('adopt-inventory-back');
  await input('adopt-source-url', 'http://source-cpa-2:8317');
  await click('adopt-read-inventory');
  await settle();
  await input('adopt-confirm-inventory', true);
  await click('adopt-confirm-migrate');
  await settle();
  expect(server.strategyBodies.at(-1)?.baseUrl).toBe('http://source-cpa-2:8317');
  expect(text()).toContain(adoptText('keyMismatch'));
  expect(maybe('adopt-migration')).toHaveLength(0);
});

it('asks for the key again before a staged inventory can be confirmed', async () => {
  // A reload on a staged checkpoint: the inventory is durable, the body that
  // would commit it is not, and the key was never stored anywhere.
  const server = await adoptServer({ staged: true });
  await settle();
  expect(maybe('adopt-inventory')).toHaveLength(1);
  expect(maybe('adopt-source-url')).toHaveLength(1);
  await input('adopt-confirm-inventory', true);
  // The confirmation cannot be offered until there is a body to send, and it is
  // disabled rather than clickable and inert.
  expect(byID('adopt-confirm-migrate').props.disabled).toBe(true);
  expect(server.strategyBodies).toHaveLength(0);

  await input('adopt-source-url', sourceAddress);
  await input('adopt-management-key', sourceKey);
  await click('adopt-use-key');
  await settle();
  // Rebuilding the body observes the destination; the commit is still the
  // operator's confirmation and nothing was written here.
  expect(server.strategyBodies).toHaveLength(0);
  expect(byID('adopt-confirm-migrate').props.disabled).toBe(false);

  await click('adopt-confirm-migrate');
  await settle();
  expect(server.strategyBodies).toHaveLength(1);
  expect(server.strategyBodies[0].pluginsEnabled).toBe(false);
  expect(maybe('adopt-migration')).toHaveLength(1);
});

it('resumes with the durable revision, not the one the page last read', async () => {
  const server = await adoptServer({
    resume: true,
    checkpointAdvance: 2,
    summaries: [
      {
        items: [],
        planned: 2,
        finished: false,
        running: false,
        retryable: 0,
        counts: { migrated: 0, manual: 0, failed: 0, absent_at_source: 0 },
      },
      finishedRun,
    ],
  });
  await settle();
  // The migration checkpoints moved the record forward while this page was only
  // reading progress, so the replay's fence has to be the durable revision.
  await input('adopt-source-url', sourceAddress);
  await input('adopt-management-key', sourceKey);
  await click('adopt-resume');
  await settle();
  expect(server.strategyBodies).toHaveLength(1);
  expect(server.strategyBodies[0].expectedRevision).toBe(7);
});

it('clears the retryable acknowledgement when the set changes at the same size', async () => {
  const retryableRun = (item: string): AdoptionSummary => ({
    items: withIdentity([
      { item, kind: 'credential', outcome: 'failed', reason: 'download_failed', retryable: true },
    ]),
    planned: 1,
    finished: true,
    running: false,
    retryable: 1,
    counts: { migrated: 0, manual: 0, failed: 1, absent_at_source: 0 },
  });
  await adoptServer({
    summaries: [retryableRun('credential:a.json'), retryableRun('credential:b.json')],
    completions: [{ status: 409, body: { code: 'adopt_migration_retryable', retryable: 1 } }],
  });
  await click('adopt-read-inventory');
  await input('adopt-confirm-inventory', true);
  await click('adopt-confirm-migrate');
  await settle();
  await click('adopt-complete');
  await input('adopt-accept-retryable', true);
  expect(byID('adopt-accept-retryable').props.checked).toBe(true);
  // The next read answers a different failed item with the same count. The
  // acknowledgement was given for the set that was on screen, not for a number.
  await click('adopt-refresh');
  await settle();
  expect(byID('adopt-accept-retryable').props.checked).toBe(false);
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
  await input('adopt-source-url', sourceAddress);
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
  // The refusal is the server's answer, not a disabled control: a settled run can
  // always ask to complete, and its refusal is what names the failed items.
  expect(byID('adopt-complete').props.disabled).toBe(false);

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
  // The next step is the page's own sentence for the item's kind and reason,
  // behind the disclosure. The record's instruction is not part of the reply at
  // all.
  expect(maybe('adopt-guidance-credential').length).toBeGreaterThan(0);
  expect(text()).toContain(adoptGuidanceText('credential', 'runtime_only_credential'));
  expect(text()).toContain(adoptGuidanceText('config_leaf', 'reason_this_browser_does_not_know'));
});

it('never claims a complete adoption over a settled run with retryable items', async () => {
  await adoptServer();
  await click('adopt-read-inventory');
  await input('adopt-confirm-inventory', true);
  await click('adopt-confirm-migrate');
  await settle();
  // `finished` is a fact about the run. It is not a fact about the installation,
  // and an item that could not be copied is still on the source.
  expect(text()).not.toContain(adoptText('done'));
  expect(text()).not.toContain(adoptText('doneHelp'));
  expect(text()).toContain(adoptText('settledTitle'));
  expect(text()).toContain(adoptText('settledHelp'));
  expect(text()).toContain(adoptText('retryableRefusal', { count: 1 }));
});

it('counts only copies as migrated and gives source-absent items their own group', async () => {
  await adoptServer({ summaries: [allOutcomes] });
  await click('adopt-read-inventory');
  await input('adopt-confirm-inventory', true);
  await click('adopt-confirm-migrate');
  await settle();
  expect(byID('adopt-count-migrated').props.children).toBe(allOutcomes.counts.migrated);
  expect(byID('adopt-count-absent_at_source').props.children).toBe(
    allOutcomes.counts.absent_at_source
  );
  expect(byID('adopt-count-manual').props.children).toBe(allOutcomes.counts.manual);
  expect(byID('adopt-count-failed').props.children).toBe('—');
  // Each item is listed under its own outcome, so the migrated group can never
  // stand in for a resource the source did not have.
  expect(
    renderer.root
      .findByProps({ 'data-testid': 'adopt-group-migrated' })
      .findAllByProps({ className: 'adopt-item-name' })
      .map((name) => name.props.children)
  ).toEqual(['credential:copied-a.json', 'config:routing.strategy']);
  expect(
    renderer.root
      .findByProps({ 'data-testid': 'adopt-group-absent_at_source' })
      .findAllByProps({ className: 'adopt-item-name' })
      .map((name) => name.props.children)
  ).toEqual(['credential:left-1.json', 'credential:left-2.json', 'credential:left-3.json']);
  expect(byID('adopt-progress').props.children).toBe(
    adoptText('progress', { settled: 6, planned: 6 })
  );
});

it('offers a resume, not a completion, when a settled run left planned items unprocessed', async () => {
  const server = await adoptServer({ summaries: [shortfallRun] });
  await click('adopt-read-inventory');
  await input('adopt-confirm-inventory', true);
  await click('adopt-confirm-migrate');
  await settle();
  // The plan is what the run fixed before its first write, so a run that settled
  // short of it has work left even though nothing failed.
  expect(byID('adopt-progress').props.children).toBe(
    adoptText('progress', { settled: 3, planned: 5 })
  );
  expect(text()).toContain(adoptText('remainingItems', { count: 2 }));
  expect(maybe('adopt-resume')).toHaveLength(1);
  expect(byID('adopt-complete').props.disabled).toBe(true);
  expect(text()).not.toContain(adoptText('done'));
  // The next action works: a replay with the same body, not a dead button.
  await click('adopt-resume');
  await settle();
  expect(server.strategyBodies).toHaveLength(3);
});

it('does not carry the retryable acknowledgement into the run a Resume starts', async () => {
  const server = await adoptServer({
    summaries: [finishedRun, finishedRun],
    completions: [
      { status: 409, body: { code: 'adopt_migration_retryable', retryable: 1 } },
      { status: 409, body: { code: 'adopt_migration_retryable', retryable: 1 } },
    ],
  });
  await click('adopt-read-inventory');
  await input('adopt-confirm-inventory', true);
  await click('adopt-confirm-migrate');
  await settle();
  await click('adopt-complete');
  await input('adopt-accept-retryable', true);
  expect(byID('adopt-accept-retryable').props.checked).toBe(true);
  // Resume starts a run the operator has not seen. The old acknowledgement was
  // given for the old set, so the next completion must not carry it.
  await click('adopt-resume');
  await settle();
  expect(maybe('adopt-accept-retryable')).toHaveLength(0);
  await click('adopt-complete');
  expect(JSON.parse(String(server.posts('complete')[1][1]!.body))).toEqual({});
  expect(maybe('adopt-accept-retryable')).toHaveLength(1);
  expect(byID('adopt-accept-retryable').props.checked).toBe(false);
});

it('stops polling and says so when the progress read keeps failing', async () => {
  vi.useFakeTimers();
  try {
    const server = await adoptServer({ readFailures: 3 });
    await click('adopt-read-inventory');
    await input('adopt-confirm-inventory', true);
    await click('adopt-confirm-migrate');
    // Three consecutive failures are all the loop accepts; a fourth read would
    // mean it is still spinning behind a screen that cannot name the problem.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30000);
    });
    expect(server.summaryReads()).toBe(3);
    expect(text()).toContain(adoptText('progressUnavailable'));
    expect(maybe('adopt-retry-progress')).toHaveLength(1);
    // The retry is the operator's, and it is the only thing that restarts the
    // read: the next answer is the record.
    await act(async () => {
      await byID('adopt-retry-progress').props.onClick();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000);
    });
    expect(server.summaryReads()).toBe(4);
    expect(maybe('adopt-progress-error')).toHaveLength(0);
    expect(text()).toContain(adoptText('progress', { settled: 4, planned: 4 }));
  } finally {
    vi.useRealTimers();
  }
});

it('states the next step the recorded reason needs and the count it kept', async () => {
  // One credential exists only inside the source process, so there is no file to
  // copy; the other stands for files the record could not hold, and the count is
  // the record's own.
  const consolidated: AdoptionSummary = {
    items: [
      {
        id: itemID('credential:runtime-only.json'),
        item: 'credential:runtime-only.json',
        kind: 'credential',
        outcome: 'manual',
        reason: 'runtime_only_credential',
        retryable: false,
        left: 0,
      },
      {
        id: itemID('credential-inventory-excess'),
        item: 'credential-inventory-excess',
        kind: 'credential',
        outcome: 'manual',
        reason: 'adoption_inventory_too_large',
        retryable: false,
        left: 4,
      },
    ],
    planned: 2,
    finished: true,
    running: false,
    retryable: 0,
    counts: { migrated: 0, manual: 2, failed: 0, absent_at_source: 0 },
  };
  await adoptServer({ summaries: [consolidated] });
  await click('adopt-read-inventory');
  await input('adopt-confirm-inventory', true);
  await click('adopt-confirm-migrate');
  await settle();

  expect(text()).toContain(adoptGuidanceText('credential', 'runtime_only_credential'));
  expect(text()).toContain(adoptGuidanceText('credential', 'adoption_inventory_too_large', 4));
  expect(text()).toContain('4');
  // The generic "copy this credential file" step is wrong for both of them.
  expect(text()).not.toContain(adoptText('guidanceCredential'));
});

it('keeps two items that scrub to the same name apart', async () => {
  // Two token-shaped file names reach the page as the same text. The
  // acknowledgement is about the record's items, so a different pair is a
  // decision the operator has not made even though the text is identical.
  const scrubbedRun = (first: string, second: string): AdoptionSummary => ({
    items: [first, second].map((name) => ({
      id: itemID(name),
      item: 'credential:[redacted].json',
      kind: 'credential',
      outcome: 'failed',
      reason: 'download_failed',
      retryable: true,
      left: 0,
    })),
    planned: 2,
    finished: true,
    running: false,
    retryable: 2,
    counts: { migrated: 0, manual: 0, failed: 2, absent_at_source: 0 },
  });
  await adoptServer({
    summaries: [
      scrubbedRun('credential:sk-proj-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.json', 'credential:sk-proj-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.json'),
      scrubbedRun('credential:sk-proj-cccccccccccccccccccccccccccccccc.json', 'credential:sk-proj-dddddddddddddddddddddddddddddddd.json'),
    ],
    completions: [{ status: 409, body: { code: 'adopt_migration_retryable', retryable: 2 } }],
  });
  await click('adopt-read-inventory');
  await input('adopt-confirm-inventory', true);
  await click('adopt-confirm-migrate');
  await settle();
  await click('adopt-complete');
  await input('adopt-accept-retryable', true);
  expect(byID('adopt-accept-retryable').props.checked).toBe(true);
  await click('adopt-refresh');
  await settle();
  expect(byID('adopt-accept-retryable').props.checked).toBe(false);
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
  // The record owns this journey now: there is no other choice to go back to.
  expect(maybe('adopt-exit')).toHaveLength(0);
  expect(get.mock.calls.map(([resource]) => resource)).toContain('installation/adoption');
});

it('lets the Console wizard choose Adopt instead of a dead tile', async () => {
  // A guided Runtime page with no stored strategy: all three journeys are the
  // operator's to choose, and Adopt is one of them.
  const choosing: FreshOperation = {
    identity,
    revision: 2,
    adminInitialized: true,
    gatewayPending: true,
    routing: {
      operationId: identity.operationId,
      revision: 1,
      target: '/console',
      stage: 'committed',
      committed: true,
    },
  };
  const get = vi.fn(async (resource: string) =>
    resource === 'installation/runtime' ? runtime : choosing
  );
  await mountManaged(choosing, get);
  await click('choose-adopt');
  await settle();
  // The same shared flow the setup wizard renders, from its source step.
  expect(maybe('fresh-adopt-flow').length).toBeGreaterThan(0);
  expect(maybe('adopt-source-url')).toHaveLength(1);
});

it('lets the Console take back the Adopt choice before an attempt is staged', async () => {
  const choosing: FreshOperation = {
    identity,
    revision: 2,
    adminInitialized: true,
    gatewayPending: true,
    routing: {
      operationId: identity.operationId,
      revision: 1,
      target: '/console',
      stage: 'committed',
      committed: true,
    },
  };
  const get = vi.fn(async (resource: string) =>
    resource === 'installation/runtime' ? runtime : choosing
  );
  await mountManaged(choosing, get);
  await click('choose-adopt');
  await settle();
  expect(maybe('adopt-source-url')).toHaveLength(1);

  // Nothing was written, so the choice is still the operator's to change.
  await click('adopt-exit');
  await settle();
  expect(maybe('adopt-source-url')).toHaveLength(0);
  expect(maybe('choose-adopt')).toHaveLength(1);
});

it('sends the durable revision when the Console carrier resumes an adoption', async () => {
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
  // The record moved on after this page read it: the migration checkpoints are
  // their own durable writes.
  const advanced: FreshOperation = { ...committed, revision: 12 };
  const stopped: AdoptionSummary = {
    items: [],
    planned: 2,
    finished: false,
    running: false,
    retryable: 0,
    counts: { migrated: 0, manual: 0, failed: 0, absent_at_source: 0 },
  };
  const get = vi.fn(async (resource: string) => {
    if (resource === 'installation/adoption') return stopped;
    if (resource === 'operation') return advanced;
    return runtime;
  });
  const { post } = await mountManaged(committed, get);
  post.mockResolvedValue(advanced);
  await settle();
  await input('adopt-source-url', sourceAddress);
  await input('adopt-management-key', sourceKey);
  await click('adopt-resume');
  await settle();
  expect(post.mock.calls[0][0]).toBe('strategy');
  expect(post.mock.calls[0][1]).toMatchObject({ expectedRevision: 12 });
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

// A completed installation whose operator accepted the retryable items: the
// installation is done, the copies are not. The screen said "Adoption complete"
// here, next to a list of items that were never copied.
it('does not call an installation complete while accepted items are still uncopied', async () => {
  const done: FreshOperation = {
    identity,
    revision: 12,
    adminInitialized: true,
    productComplete: true,
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
    resource === 'installation/adoption' ? finishedRun : done
  );
  await mountManaged(done, get);
  await settle();
  expect(text()).not.toContain(adoptText('done'));
  expect(text()).not.toContain(adoptText('doneHelp'));
  expect(text()).toContain(adoptText('settledTitle'));
  expect(text()).toContain(adoptText('retryableRefusal', { count: 1 }));
});

// The one state that may say the adoption is complete: the run settled, nothing
// is left to retry, and the installation completed.
it('shows the success headline only for a settled, clean, completed installation', async () => {
  const done: FreshOperation = {
    identity,
    revision: 12,
    adminInitialized: true,
    productComplete: true,
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
    resource === 'installation/adoption' ? cleanRun : done
  );
  await mountManaged(done, get);
  await settle();
  expect(text()).toContain(adoptText('done'));
  expect(text()).toContain(adoptText('doneHelp'));
  expect(text()).not.toContain(adoptText('settledTitle'));
});
