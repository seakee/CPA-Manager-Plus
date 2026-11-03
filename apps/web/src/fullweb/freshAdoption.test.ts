import { afterEach, describe, expect, it, vi, type Mock } from 'vitest';
import { sha256RawTextHex } from '@/utils/apiKeyHash';
import { BOOTSTRAP_UI, createBootstrapTransport, type FreshOperation } from './bootstrapTransport';
import {
  ADOPT_SOURCE_CLASS,
  adoptStrategyBody,
  normalizeCPAAddress,
  parseAdoptionSummary,
  validManagementKey,
  type AdoptionItem,
} from './freshAdoption';
import { createPublicApiClient, type FullWebCredential } from './publicApi';
import { resolveRoutingContext } from './routingContext';

// One named mock type: every carrier under test is injected with the same fetch.
type FetchMock = Mock<typeof fetch>;

afterEach(() => vi.unstubAllGlobals());

const identity = {
  admissionId: 'admission-a',
  installationId: 'installation-a',
  generation: 'generation-a',
  databasePath: '/data/manager.sqlite',
  operationId: 'fresh-operation-a',
};
const handoff = {
  identity,
  claimGeneration: 1,
  token: 'claim-secret-012345678901234567890123456789',
  resume: true,
};
const artifactId = 'sha256:' + 'a'.repeat(64);
const source = {
  baseUrl: 'http://cpamp-d-adopt-source:8317',
  managementKey: 'adopt-source-management-key',
  pluginsEnabled: true,
};
const target = { runtimeIdentity: 'owned-runtime', runtimeGeneration: 3, artifactId };

// A fixture's stable identity for one item. The server derives the public id
// from the public label and the item's ordinal, so the fixture only needs an
// identity that is stable and distinct within one summary.
const itemID = (name: string) => sha256RawTextHex(name);
const recorded = (
  item: Omit<AdoptionItem, 'id' | 'left' | 'reason'> & { reason?: string; left?: number }
): AdoptionItem => ({
  ...item,
  id: itemID(item.item),
  reason: item.reason ?? '',
  left: item.left ?? 0,
});

// A real `GET installation/adoption` body: the frozen server shape, including an
// omitted reason on a migrated item and the reason code the executor records for
// its manual and retryable ones. A credential is the Manager's generic label and
// ordinal, never a source filename; non-credential items keep canonical
// Manager-owned references. The reply carries no instruction text: the page
// states the next step from the item's kind and reason.
const summary = {
  runId: 'b7c1'.repeat(8),
  items: [
    recorded({
      item: 'Credential file #1',
      kind: 'credential',
      outcome: 'migrated',
      retryable: false,
    }),
    recorded({
      item: 'Credential file #2',
      kind: 'credential',
      outcome: 'failed',
      reason: 'download_failed',
      retryable: true,
    }),
    recorded({
      item: 'Credential file #3',
      kind: 'credential',
      outcome: 'manual',
      reason: 'runtime_only_credential',
      retryable: false,
    }),
    recorded({
      item: 'config:routing.session-affinity',
      kind: 'config_leaf',
      outcome: 'absent_at_source',
      retryable: false,
    }),
    recorded({
      item: 'config:routing.strategy',
      kind: 'config_leaf',
      outcome: 'migrated',
      retryable: false,
    }),
    recorded({
      item: 'section:api-keys.claude',
      kind: 'provider_section',
      outcome: 'migrated',
      retryable: false,
    }),
  ],
  planned: 6,
  finished: true,
  running: false,
  retryable: 1,
  counts: { migrated: 3, manual: 1, failed: 1, absent_at_source: 1 },
};

// The operation a first POST answers with: a staged adoption checkpoint carrying
// the PascalCase inventory fields the pinned domain type has no tags for.
const staged = {
  identity,
  revision: 7,
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
    strategy: 'adopt_existing',
    targetIdentity: target.runtimeIdentity,
    targetGeneration: target.runtimeGeneration,
    targetArtifactId: artifactId,
    stage: 'staged',
    sourceIdentity: 'external:sha256:' + 'b'.repeat(64),
    sourceClass: 'network_existing',
    destinationConnectionIdentity: 'external:sha256:' + 'c'.repeat(64),
    pluginsEnabled: true,
    adoptionResources: ['client-api-keys', 'cpa-auth-files', 'cpa-config-routing'],
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
  },
};

function stubBootstrapOrigin() {
  vi.stubGlobal('window', { location: new URL('https://manager.example' + BOOTSTRAP_UI) });
}

async function bootstrapClient(fetchMock: FetchMock) {
  fetchMock.mockResolvedValueOnce(Response.json(staged));
  const client = createBootstrapTransport(fetchMock);
  await client.start(handoff);
  return client;
}

// The route owner of the canonical namespace, as the Manager answers it at the
// public verification leaf.
const canonicalState = {
  installationId: identity.installationId,
  generation: identity.generation,
  operationId: identity.operationId,
  target: '/console',
  revision: 1,
  committed: true,
};

// A bootstrap client that has already cut over, which is the only state in which
// a completion is sent: the completion goes to the canonical namespace, never to
// the fixed carrier.
async function cutoverClient(fetchMock: FetchMock) {
  const activated = {
    ...staged,
    routing: { ...staged.routing, stage: 'committed', committed: true },
  };
  fetchMock
    .mockResolvedValueOnce(Response.json(staged))
    .mockResolvedValueOnce(Response.json(staged))
    .mockResolvedValueOnce(Response.json(activated))
    .mockResolvedValueOnce(
      Response.json(canonicalState, { headers: { 'X-CPAMP-Route-Owner': 'manager' } })
    );
  const client = createBootstrapTransport(fetchMock);
  await client.start(handoff);
  await client.prepareRouting('/console');
  expect(await client.activateRouting()).toBe('/console');
  return client;
}

describe('the Adopt wire contract', () => {
  it('accepts the recorded summary and keeps the omitted fields empty', () => {
    const parsed = parseAdoptionSummary(summary);
    expect(parsed.runId).toBe(summary.runId);
    expect(parsed.items).toHaveLength(6);
    expect(parsed.counts).toEqual({ migrated: 3, manual: 1, failed: 1, absent_at_source: 1 });
    expect(parsed.retryable).toBe(1);
    expect(parsed.items[0]).toEqual({
      id: itemID('Credential file #1'),
      item: 'Credential file #1',
      kind: 'credential',
      outcome: 'migrated',
      reason: '',
      retryable: false,
      left: 0,
    });
    expect(parsed.items[1].retryable).toBe(true);
  });

  it('parses a legacy record without a run identity as the empty run', () => {
    // Only a record written before run identity existed omits it. Every other
    // shape is refused rather than guessed.
    expect(parseAdoptionSummary({ ...summary, runId: undefined }).runId).toBe('');
  });

  it.each([
    { items: 'none' },
    { planned: -1 },
    { finished: 'yes' },
    { running: null },
    { counts: { migrated: 0, manual: 0, failed: 0 } },
    // A run identity is opaque and fixed-width; a placeholder is not the record.
    { runId: 'not-a-run-identity' },
    { runId: 'A'.repeat(32) },
    { runId: 42 },
    { items: [{ item: '', kind: 'credential', outcome: 'migrated', retryable: false }] },
    { items: [{ item: 'a', kind: 'unknown', outcome: 'migrated', retryable: false }] },
    { items: [{ item: 'a', kind: 'credential', outcome: 'skipped', retryable: false }] },
    { items: [{ item: 'a', kind: 'credential', outcome: 'migrated' }] },
    // The identity is the record's, so a reply without one is not the record: an
    // item the page cannot tell apart from another is not an item it can accept.
    { items: [{ item: 'a', kind: 'credential', outcome: 'migrated', retryable: false }] },
    {
      items: [
        {
          id: 'not-a-digest',
          item: 'a',
          kind: 'credential',
          outcome: 'migrated',
          retryable: false,
        },
      ],
    },
    {
      items: [
        {
          ...recorded({ item: 'b', kind: 'credential', outcome: 'migrated', retryable: false }),
          left: -1,
        },
      ],
    },
  ])('refuses a body that is not the recorded summary: %j', (patch) => {
    expect(() => parseAdoptionSummary({ ...summary, ...patch })).toThrow();
  });

  it('keeps the identity and the count of the items the record consolidated', () => {
    const consolidated = parseAdoptionSummary({
      ...summary,
      items: [
        recorded({
          item: 'Credential files left behind',
          kind: 'credential',
          outcome: 'manual',
          reason: 'adoption_inventory_too_large',
          retryable: false,
          left: 7,
        }),
        recorded({
          item: 'Credential file #2',
          kind: 'credential',
          outcome: 'manual',
          reason: 'adoption_item_name_too_long',
          retryable: false,
        }),
      ],
      planned: 2,
    });
    expect(consolidated.items[0]).toMatchObject({
      id: itemID('Credential files left behind'),
      left: 7,
    });
    // An item without a count is an item with nothing left over, never a missing
    // number the screen would have to guess.
    expect(consolidated.items[1].left).toBe(0);
  });

  it('builds exactly the body the strict server decoder accepts', () => {
    expect(adoptStrategyBody(source, target)).toEqual({
      strategy: 'adopt_existing',
      adoptSourceClass: ADOPT_SOURCE_CLASS,
      baseUrl: 'http://cpamp-d-adopt-source:8317',
      managementKey: 'adopt-source-management-key',
      expectedRuntimeIdentity: 'owned-runtime',
      expectedRuntimeGeneration: 3,
      expectedArtifactId: artifactId,
      pluginsEnabled: true,
    });
  });

  it('normalizes the source address and refuses one the server would reject', () => {
    expect(normalizeCPAAddress('  https://cpa.example:8317/  ')).toBe('https://cpa.example:8317');
    for (const value of [
      'ftp://cpa.example',
      'http://user:secret@cpa.example',
      'http://cpa.example?x=1',
      'http://cpa.example#fragment',
      'not an address',
    ])
      expect(() => normalizeCPAAddress(value)).toThrow();
    expect(validManagementKey('')).toBe(false);
    expect(validManagementKey('has space')).toBe(false);
    expect(validManagementKey('ok-key')).toBe(true);
    expect(() => adoptStrategyBody({ ...source, baseUrl: 'ftp://x' }, target)).toThrow();
    expect(() => adoptStrategyBody({ ...source, managementKey: '' }, target)).toThrow();
    expect(() => adoptStrategyBody(source, { ...target, artifactId: 'sha256:short' })).toThrow();
  });
});

describe('the bootstrap carrier reads and writes the adoption', () => {
  it('reads the recorded summary through the Fresh session', async () => {
    stubBootstrapOrigin();
    const fetchMock = vi.fn<typeof fetch>();
    const client = await bootstrapClient(fetchMock);
    fetchMock.mockResolvedValueOnce(Response.json(summary));
    expect(await client.readAdoption()).toMatchObject({ planned: 6, retryable: 1 });
    expect(fetchMock.mock.calls[1][0]).toContain('/api/v2/installation/adoption');
    expect(fetchMock.mock.calls[1][1]).toMatchObject({
      method: 'GET',
      headers: { Accept: 'application/json', Authorization: 'Fresh ' + handoff.token },
    });
  });

  it.each([{ items: [] }, { planned: 0, finished: false, running: true, retryable: 0 }])(
    'refuses a summary that is not the recorded shape: %j',
    async (body) => {
      stubBootstrapOrigin();
      const fetchMock = vi.fn<typeof fetch>();
      const client = await bootstrapClient(fetchMock);
      fetchMock.mockResolvedValueOnce(Response.json(body));
      await expect(client.readAdoption()).rejects.toMatchObject({
        name: 'BootstrapTransportError',
        kind: 'response',
      });
    }
  );

  it('posts the adoption body with the operation it belongs to', async () => {
    stubBootstrapOrigin();
    const fetchMock = vi.fn<typeof fetch>();
    const client = await bootstrapClient(fetchMock);
    fetchMock.mockResolvedValueOnce(Response.json(staged));
    fetchMock.mockResolvedValueOnce(Response.json(staged));
    const result = await client.executeStrategy(adoptStrategyBody(source, target));
    expect(JSON.parse(fetchMock.mock.calls[2][1]!.body as string)).toEqual({
      ...adoptStrategyBody(source, target),
      identity,
      expectedRevision: 7,
    });
    expect(result.operation.strategy?.stage).toBe('staged');
    expect(result.initialClientKey).toBe('');
  });

  it.each(['staged', 'committed'])(
    'accepts an adoption checkpoint at %s and refuses a starting one',
    async (stage) => {
      stubBootstrapOrigin();
      const fetchMock = vi.fn<typeof fetch>();
      const client = await bootstrapClient(fetchMock);
      const reply = { ...staged, revision: 9, strategy: { ...staged.strategy, stage } };
      fetchMock.mockResolvedValueOnce(Response.json({ ...staged, revision: 9 }));
      fetchMock.mockResolvedValueOnce(Response.json(reply));
      await expect(
        client.executeStrategy(adoptStrategyBody(source, target))
      ).resolves.toMatchObject({ operation: { revision: 9 } });
      const invalid = vi.fn<typeof fetch>();
      const second = await bootstrapClient(invalid);
      // The durable read is a valid checkpoint; the write's own reply is the one
      // that cannot be an adoption.
      invalid.mockResolvedValueOnce(Response.json({ ...staged, revision: 9 }));
      invalid.mockResolvedValueOnce(
        Response.json({
          ...staged,
          revision: 10,
          strategy: { ...staged.strategy, stage: 'starting' },
        })
      );
      await expect(second.executeStrategy(adoptStrategyBody(source, target))).rejects.toMatchObject(
        {
          kind: 'mismatch',
        }
      );
    }
  );

  it('refuses an adoption body the server would reject before sending it', async () => {
    stubBootstrapOrigin();
    const fetchMock = vi.fn<typeof fetch>();
    const client = await bootstrapClient(fetchMock);
    await expect(
      client.executeStrategy({
        ...adoptStrategyBody(source, target),
        strategy: 'adopt_existing',
        requestId: 'x',
      })
    ).rejects.toMatchObject({ kind: 'bad_request' });
    await expect(
      client.executeStrategy({ ...adoptStrategyBody(source, target), pluginsEnabled: 'true' })
    ).rejects.toMatchObject({ kind: 'bad_request' });
    await expect(
      client.executeStrategy({
        ...adoptStrategyBody(source, target),
        adoptSourceClass: 'legacy_managed',
      })
    ).rejects.toMatchObject({ kind: 'bad_request' });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('preserves the strategy blocker code and its unsupported capabilities', async () => {
    stubBootstrapOrigin();
    const fetchMock = vi.fn<typeof fetch>();
    const client = await bootstrapClient(fetchMock);
    fetchMock.mockResolvedValueOnce(
      Response.json(
        {
          Code: 'adoption_destination_staging_required',
          Unsupported: ['cpa_native_destination_staging_required', 'historical_usage_excluded'],
        },
        { status: 409 }
      )
    );
    await expect(client.executeStrategy(adoptStrategyBody(source, target))).rejects.toMatchObject({
      kind: 'conflict',
      status: 409,
      code: 'adoption_destination_staging_required',
      unsupported: ['cpa_native_destination_staging_required', 'historical_usage_excluded'],
    });
  });

  it.each([
    [{ code: 'adopt_migration_retryable', retryable: 2 }, 409, 'adopt_migration_retryable', 2],
    [{ code: 'adopt_migration_running' }, 409, 'adopt_migration_running', undefined],
    [{ code: 'adopt_migration_incomplete' }, 409, 'adopt_migration_incomplete', undefined],
    [{ code: 'adopt_migration_run_changed' }, 409, 'adopt_migration_run_changed', undefined],
    [{ code: 'adopt_destination_unproven' }, 503, 'adopt_destination_unproven', undefined],
  ])('reads the completion refusal %j', async (body, status, code, retryable) => {
    stubBootstrapOrigin();
    const fetchMock = vi.fn<typeof fetch>();
    const client = await cutoverClient(fetchMock);
    fetchMock.mockResolvedValueOnce(Response.json(body, { status: Number(status) }));
    await expect(client.complete()).rejects.toMatchObject({ status, code, retryable });
    expect(fetchMock.mock.calls[4][0]).toBe('https://manager.example/console/api/v2/complete');
  });

  it('never surfaces an unknown completion code', async () => {
    stubBootstrapOrigin();
    const fetchMock = vi.fn<typeof fetch>();
    const client = await cutoverClient(fetchMock);
    fetchMock.mockResolvedValueOnce(
      Response.json({ code: 'raw_upstream_failure', retryable: 1 }, { status: 409 })
    );
    await expect(client.complete()).rejects.toMatchObject({ status: 409, code: undefined });
  });

  it('sends acceptRetryable only when the operator accepted the failed items', async () => {
    stubBootstrapOrigin();
    const fetchMock = vi.fn<typeof fetch>();
    const client = await cutoverClient(fetchMock);
    fetchMock.mockResolvedValueOnce(
      Response.json({
        ...staged,
        revision: 10,
        productComplete: true,
        strategy: { ...staged.strategy, stage: 'committed' },
        routing: { ...staged.routing, stage: 'committed', committed: true },
      })
    );
    await client.complete({ acceptRetryable: true, runId: 'd'.repeat(32) });
    expect(JSON.parse(fetchMock.mock.calls[4][1]!.body as string)).toEqual({
      acceptRetryable: true,
      runId: 'd'.repeat(32),
    });
  });
});

const context = resolveRoutingContext({
  state: 'verified_effective',
  verifiedEffectiveControlBasePath: '/console',
  revision: 1,
})!;
const credential: FullWebCredential = { kind: 'admin', value: 'full-web-admin-secret' };

function canonicalClient(fetchMock: FetchMock) {
  vi.stubGlobal('window', { location: new URL('https://manager.example:18317') });
  vi.stubGlobal('localStorage', {
    getItem: () => {
      throw new Error('Full Web cannot read persisted CPA state');
    },
  });
  return createPublicApiClient(() => context, fetchMock);
}

describe('the canonical carrier reads and writes the adoption', () => {
  it('reads the recorded summary with a bounded read', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(Response.json(summary));
    const client = canonicalClient(fetchMock);
    const reply = parseAdoptionSummary(await client.get('installation/adoption', credential));
    expect(reply).toMatchObject({ planned: 6, finished: true });
    expect(fetchMock.mock.calls[0][0]).toBe(
      'https://manager.example:18317' + context.apiBase + '/installation/adoption'
    );
    expect(fetchMock.mock.calls[0][1]).toMatchObject({
      method: 'GET',
      headers: { Accept: 'application/json', Authorization: 'Bearer full-web-admin-secret' },
    });
    expect(fetchMock.mock.calls[0][1]!.signal).toBeInstanceOf(AbortSignal);
  });

  it('reads the completion refusal code and the retryable count', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValue(
        Response.json({ code: 'adopt_migration_retryable', retryable: 2 }, { status: 409 })
      );
    const client = canonicalClient(fetchMock);
    await expect(
      client.post('complete', { acceptRetryable: false }, credential)
    ).rejects.toMatchObject({
      kind: 'http',
      status: 409,
      code: 'adopt_migration_retryable',
      retryable: 2,
    });
  });

  it('reads a run-changed completion refusal on the canonical carrier', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValue(Response.json({ code: 'adopt_migration_run_changed' }, { status: 409 }));
    const client = canonicalClient(fetchMock);
    await expect(
      client.post('complete', { acceptRetryable: true, runId: 'd'.repeat(32) }, credential)
    ).rejects.toMatchObject({
      kind: 'http',
      status: 409,
      code: 'adopt_migration_run_changed',
    });
  });

  it('reads an adoption blocker and its unsupported capabilities', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValue(
        Response.json(
          { Code: 'adoption_source_read_unavailable', Unsupported: null },
          { status: 409 }
        )
      );
    const client = canonicalClient(fetchMock);
    await expect(
      client.post('strategy', adoptStrategyBody(source, target), credential)
    ).rejects.toMatchObject({ status: 409, code: 'adoption_source_read_unavailable' });
  });

  it('never surfaces an unknown strategy refusal code', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValue(Response.json({ Code: 'raw_upstream_failure' }, { status: 409 }));
    const client = canonicalClient(fetchMock);
    await expect(
      client.post('strategy', adoptStrategyBody(source, target), credential)
    ).rejects.toMatchObject({ status: 409, code: undefined });
  });
});

// The two carriers must agree on what a staged adoption looks like, or the same
// record would be accepted by one wizard and refused by the other.
it('accepts the same staged adoption through both carriers', async () => {
  stubBootstrapOrigin();
  const fetchMock = vi.fn<typeof fetch>();
  const client = await bootstrapClient(fetchMock);
  // The write reads the durable operation first, so the fence is the revision
  // that is current rather than the one this page last saw.
  fetchMock.mockResolvedValueOnce(Response.json(staged));
  fetchMock.mockResolvedValueOnce(Response.json(staged));
  const bootstrapOperation = await client.executeStrategy(adoptStrategyBody(source, target));
  const canonicalFetch = vi.fn<typeof fetch>().mockResolvedValue(Response.json(staged));
  const canonical = canonicalClient(canonicalFetch);
  const canonicalOperation = (await canonical.get('operation', credential)) as FreshOperation;
  expect(bootstrapOperation.operation.strategy).toEqual(canonicalOperation.strategy);
});

// The fixed carrier's progress read is the flow's only source of facts, and the
// flow stops after three failed reads. A request that never answers must count as
// one of those failures instead of leaving "reading the migration record" up
// forever; the canonical carrier already bounds every read the same way.
it('bounds a progress read that never answers', async () => {
  vi.useFakeTimers();
  try {
    stubBootstrapOrigin();
    const fetchMock = vi.fn<typeof fetch>();
    const client = await bootstrapClient(fetchMock);
    let outcome = '';
    fetchMock.mockImplementationOnce((_url, init) => {
      // A request that answers nothing but can still be cancelled: the only way a
      // hung read is distinguishable from a slow one.
      let abort: (reason: unknown) => void = () => undefined;
      const pending = new Promise<Response>((_resolve, reject) => {
        abort = reject;
      });
      init?.signal?.addEventListener('abort', () =>
        abort(new DOMException('aborted', 'AbortError'))
      );
      return pending;
    });
    void client.readAdoption().then(
      () => (outcome = 'answered'),
      () => (outcome = 'failed')
    );
    await vi.advanceTimersByTimeAsync(120000);
    expect(outcome).toBe('failed');
  } finally {
    vi.useRealTimers();
  }
});

// A connection that answers the headers and then stalls is the same hang one step
// later. The deadline has to outlive the headers and cover the body, or the
// screen waits on a read the carrier already promised to bound.
it('bounds a progress read whose body stalls after the headers', async () => {
  vi.useFakeTimers();
  try {
    stubBootstrapOrigin();
    const fetchMock = vi.fn<typeof fetch>();
    const client = await bootstrapClient(fetchMock);
    let outcome = '';
    fetchMock.mockImplementationOnce((_url, init) =>
      Promise.resolve(
        // The status line arrives; the body never does. The stream ends only when
        // the request's own deadline aborts it, which is the behaviour under test.
        new Response(
          new ReadableStream({
            start(controller) {
              init?.signal?.addEventListener('abort', () =>
                controller.error(new DOMException('aborted', 'AbortError'))
              );
            },
          }),
          { status: 200 }
        )
      )
    );
    void client.readAdoption().then(
      () => (outcome = 'answered'),
      () => (outcome = 'failed')
    );
    await vi.advanceTimersByTimeAsync(120000);
    expect(outcome).toBe('failed');
  } finally {
    vi.useRealTimers();
  }
});
