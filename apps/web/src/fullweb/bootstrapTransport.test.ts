import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  BOOTSTRAP_UI,
  BootstrapTransportError,
  consumeHandoff,
  createBootstrapTransport,
  validateFreshAdminKey,
} from './bootstrapTransport';

const identity = {
  admissionId: 'admission-a',
  installationId: 'installation-a',
  generation: 'generation-a',
  databasePath: '/data/manager.sqlite',
  operationId: 'fresh-operation-a',
};
const operation = {
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

describe('bounded Bootstrap handoff and transport', () => {
  beforeEach(() => {
    const location = new URL('https://manager.example' + BOOTSTRAP_UI);
    vi.stubGlobal('window', {
      location,
      history: {
        replaceState: (_: unknown, _title: string, path: string) => {
          location.href = new URL(path, location.origin).href;
        },
      },
    });
    vi.stubGlobal('localStorage', {
      setItem: vi.fn(() => {
        throw new Error('secret persistence forbidden');
      }),
    });
    vi.stubGlobal('sessionStorage', {
      setItem: vi.fn(() => {
        throw new Error('secret persistence forbidden');
      }),
    });
  });
  afterEach(() => vi.unstubAllGlobals());

  it('removes even malformed fragments before decoding or transport', () => {
    const replaceState = vi.fn();
    expect(
      consumeHandoff(
        { pathname: BOOTSTRAP_UI, search: '', hash: '#handoff=broken' },
        { replaceState }
      )
    ).toBeNull();
    expect(replaceState).toHaveBeenCalledWith(null, '', BOOTSTRAP_UI);
    const encoded = btoa(JSON.stringify(handoff))
      .replace(/=/g, '')
      .replace(/\+/g, '-')
      .replace(/\//g, '_');
    expect(
      consumeHandoff(
        { pathname: BOOTSTRAP_UI, search: '', hash: '#handoff=' + encoded },
        { replaceState }
      )
    ).toEqual(handoff);
  });

  it('keeps claim out of request URL and session out of storage; uses only Fresh scheme', async () => {
    const setLocal = vi.spyOn(localStorage, 'setItem');
    const transport = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ token: 'session-secret', operation })))
      .mockResolvedValueOnce(new Response(JSON.stringify(operation)));
    const client = createBootstrapTransport(transport);
    expect(await client.start(handoff)).toEqual(operation);
    await client.request('operation');
    expect(transport.mock.calls[0][0]).toBe(
      window.location.origin + '/.well-known/cpamp-bootstrap/api/v2/claim/exchange'
    );
    expect(transport.mock.calls[0][0]).not.toContain(handoff.token);
    expect(transport.mock.calls[1][1]).toMatchObject({
      headers: { Authorization: 'Fresh session-secret' },
      credentials: 'omit',
      redirect: 'error',
      cache: 'no-store',
    });
    expect(setLocal).not.toHaveBeenCalled();
    expect(sessionStorage.setItem).not.toHaveBeenCalled();
    setLocal.mockRestore();
    client.clear();
    await expect(client.request('operation')).rejects.toThrow('authority unavailable');
  });

  it('resume uses only same-operation continuation, without exchanging a claim', async () => {
    const resumed = { ...operation, adminInitialized: true };
    const transport = vi.fn().mockResolvedValue(new Response(JSON.stringify(resumed)));
    const client = createBootstrapTransport(transport);
    expect(await client.start({ ...handoff, resume: true })).toEqual(resumed);
    expect(transport).toHaveBeenCalledTimes(1);
    expect(transport.mock.calls[0][0]).toContain('/operation');
  });

  it('rejects operation identity mismatch, API aliases and non-shell destinations before network', async () => {
    const transport = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          token: 'session-secret',
          operation: { ...operation, identity: { ...identity, generation: 'other' } },
        })
      )
    );
    const client = createBootstrapTransport(transport);
    await expect(client.start(handoff)).rejects.toThrow('authority mismatch');
    await expect(client.request('../v1/models' as 'operation')).rejects.toThrow();
    window.history.replaceState(null, '', '/v1/models');
    await expect(client.request('operation')).rejects.toThrow();
    expect(transport).toHaveBeenCalledTimes(1);
  });

  it('does not reflect server errors or secrets and classifies HTTP status codes', async () => {
    const client403 = createBootstrapTransport(
      vi.fn().mockResolvedValue(new Response(handoff.token, { status: 403 }))
    );
    await expect(client403.start(handoff)).rejects.toMatchObject({
      message: 'Bootstrap request unavailable',
      kind: 'rejected',
      status: 403,
    });

    const client400 = createBootstrapTransport(
      vi
        .fn()
        .mockResolvedValueOnce(new Response(JSON.stringify({ token: 'session-secret', operation })))
        .mockResolvedValueOnce(new Response('bad key', { status: 400 }))
    );
    await client400.start(handoff);
    await expect(client400.commitAdminKey('commit-1', 'weak')).rejects.toMatchObject({
      name: 'BootstrapTransportError',
      kind: 'bad_request',
      status: 400,
    });
  });

  it('validates admin key complexity matching Manager security contract', () => {
    expect(validateFreshAdminKey('').valid).toBe(false);
    expect(validateFreshAdminKey('short-key-123').valid).toBe(false);
    expect(validateFreshAdminKey('cpamp_0123456789abcdefABCDEFG withspace').valid).toBe(false);
    expect(validateFreshAdminKey('aaaaaaaaaaaaaaaaaaaaaaaa').valid).toBe(false);
    expect(validateFreshAdminKey('cpamp_0123456789abcdefABCDEFGH99')).toMatchObject({
      valid: true,
      hasForbiddenChar: false,
    });
  });

  it.each([
    { adminInitialized: undefined },
    { adminInitialized: 'false' },
    { revision: 0 },
    { revision: 0.5 },
    { gatewayPending: false },
    { routing: null },
  ])(
    'rejects malformed authoritative state without unlocking an unknown commit: %j',
    async (patch) => {
      const transport = vi
        .fn()
        .mockResolvedValueOnce(Response.json({ token: 'session-secret', operation }))
        .mockResolvedValueOnce(Response.json({ ...operation, ...patch }))
        .mockResolvedValueOnce(Response.json(operation));
      const client = createBootstrapTransport(transport);
      await client.start(handoff);
      await expect(client.fetchOperation()).rejects.toMatchObject({ kind: 'response' });
      await expect(client.fetchOperation()).resolves.toEqual(operation);
    }
  );

  it('generates and commits admin key over authenticated Fresh session', async () => {
    const generatedKey = 'cpamp_0123456789abcdefABCDEFGH99';
    const committedOp = { ...operation, revision: 2, adminInitialized: true };
    const transport = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ token: 'session-secret', operation })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ key: generatedKey })))
      .mockResolvedValueOnce(new Response(JSON.stringify(committedOp)))
      .mockResolvedValueOnce(new Response(JSON.stringify(committedOp)));

    const client = createBootstrapTransport(transport);
    await client.start(handoff);
    expect(await client.generateAdminKey()).toBe(generatedKey);

    const afterCommit = await client.commitAdminKey('commit-abc', generatedKey);
    expect(afterCommit).toEqual(committedOp);
    expect(JSON.parse(transport.mock.calls[2][1].body as string)).toEqual({
      identity,
      commitId: 'commit-abc',
      key: generatedKey,
    });

    expect(await client.fetchOperation()).toEqual(committedOp);
    expect(transport).toHaveBeenCalledTimes(4);
    expect(new BootstrapTransportError('test', 'transport')).toBeInstanceOf(Error);
  });
});

describe('Fresh routing prepare, cutover and lost-response recovery', () => {
  const prepared = {
    ...operation,
    adminInitialized: true,
    revision: 3,
    routing: {
      target: '/console',
      operationId: identity.operationId,
      revision: 1,
      stage: 'intent_persisted',
      committed: false,
    },
  };
  const committed = {
    ...prepared,
    revision: 6,
    routing: { ...prepared.routing, stage: 'committed', committed: true },
  };
  const state = {
    installationId: identity.installationId,
    generation: identity.generation,
    operationId: identity.operationId,
    target: '/console',
    revision: 1,
    committed: true,
  };
  const reply = (value: unknown) =>
    Response.json(value, { headers: { 'X-CPAMP-Route-Owner': 'manager' } });
  beforeEach(() =>
    vi.stubGlobal('window', { location: new URL('https://manager.example' + BOOTSTRAP_UI) })
  );
  afterEach(() => vi.unstubAllGlobals());

  async function preparedClient(fetchMock: ReturnType<typeof vi.fn<typeof fetch>>) {
    fetchMock.mockResolvedValueOnce(reply(prepared));
    const client = createBootstrapTransport(fetchMock);
    await client.start({ ...handoff, resume: true });
    return client;
  }

  it('prepares with the original identity and activates only with a matching committed public identity', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(reply({ ...prepared, routing: { committed: false } }))
      .mockResolvedValueOnce(reply(prepared))
      .mockResolvedValueOnce(reply(committed))
      .mockResolvedValueOnce(reply(state));
    const client = createBootstrapTransport(fetchMock);
    await client.start({ ...handoff, resume: true });
    await client.prepareRouting('/console');
    expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({ identity, target: '/console' });
    expect(await client.activateRouting()).toBe('/console');
    expect(fetchMock.mock.calls[3]).toEqual([
      'https://manager.example/console/api/v2/bootstrap/identity',
      {
        method: 'GET',
        headers: { Accept: 'application/json' },
        mode: 'same-origin',
        credentials: 'omit',
        redirect: 'error',
        cache: 'no-store',
      },
    ]);
  });

  it.each([404, 503, 'network', 'body'])(
    'recovers activation response loss (%s) through the same canonical checkpoint',
    async (failure) => {
      const fetchMock = vi.fn<typeof fetch>();
      const client = await preparedClient(fetchMock);
      if (failure === 'network') fetchMock.mockRejectedValueOnce(new Error('not reflected'));
      else if (failure === 'body') fetchMock.mockResolvedValueOnce(new Response('{'));
      else fetchMock.mockResolvedValueOnce(new Response('', { status: Number(failure) }));
      fetchMock.mockResolvedValueOnce(reply(state));
      expect(await client.activateRouting()).toBe('/console');
      expect(fetchMock.mock.calls[2][1]?.headers).not.toHaveProperty('Authorization');
    }
  );

  it('surfaces a named routing verification refusal instead of a lost reply', async () => {
    const fetchMock = vi.fn<typeof fetch>();
    const client = await preparedClient(fetchMock);
    // A refusal that carries a code names a cause the journey can explain, so it
    // must not be folded into the "reply was lost" recovery path below.
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ Code: 'routing_verification_failed' }), { status: 503 })
    );
    await expect(client.activateRouting()).rejects.toMatchObject({
      code: 'routing_verification_failed',
      status: 503,
    });
  });

  it.each([401, 409])(
    'does not bypass a rejected or conflicting activation (%s)',
    async (status) => {
      const fetchMock = vi.fn<typeof fetch>();
      const client = await preparedClient(fetchMock);
      fetchMock.mockResolvedValueOnce(new Response('', { status }));
      await expect(client.activateRouting()).rejects.toMatchObject({ status });
      expect(fetchMock).toHaveBeenCalledTimes(2);
    }
  );

  it.each([
    { committed: false },
    { generation: 'foreign' },
    { target: '/other' },
    { revision: 2 },
    { operationId: 'foreign' },
  ])('refuses incomplete or foreign recovered state %j', async (change) => {
    const fetchMock = vi.fn<typeof fetch>();
    const client = await preparedClient(fetchMock);
    fetchMock
      .mockResolvedValueOnce(new Response('', { status: 404 }))
      .mockResolvedValueOnce(reply({ ...state, ...change }));
    await expect(client.activateRouting()).rejects.toBeInstanceOf(BootstrapTransportError);
  });

  it.each([
    '/v1',
    '/console/',
    '/a/../console',
    '//other',
    '/console%2fpath',
    '/console?query',
    '/other',
  ])(
    'rejects a noncanonical or replacement target before sending any credential (%s)',
    async (target) => {
      const fetchMock = vi.fn<typeof fetch>();
      const client = await preparedClient(fetchMock);
      await expect(client.prepareRouting(target)).rejects.toMatchObject({ kind: 'bad_request' });
      expect(fetchMock).toHaveBeenCalledTimes(1);
    }
  );

  it('sends runtime/artifact status, probe, acquire and raw gzip upload over both fixed bootstrap and post-cutover canonical carriers', async () => {
    const archive = new Blob([new Uint8Array([0x1f, 0x8b, 0x08, 0x00])], {
      type: 'application/gzip',
    });
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(reply(prepared))
      .mockResolvedValueOnce(reply({ schemaVersion: 1 }))
      .mockResolvedValueOnce(reply({ reachable: true, code: '' }))
      .mockResolvedValueOnce(reply({ operation: { id: 'op-1' } }))
      .mockResolvedValueOnce(reply({ operation: { id: 'op-2' } }))
      .mockResolvedValueOnce(reply(committed))
      .mockResolvedValueOnce(reply(state))
      .mockResolvedValueOnce(reply({ schemaVersion: 1 }))
      .mockResolvedValueOnce(reply({ reachable: true, code: '' }))
      .mockResolvedValueOnce(reply({ operation: { id: 'op-3' } }))
      .mockResolvedValueOnce(reply({ operation: { id: 'op-4' } }))
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ code: 'unsupported_platform' }), { status: 409 })
      );

    const client = createBootstrapTransport(fetchMock);
    await client.start({ ...handoff, resume: true });

    await client.request('runtime/artifact');
    await client.request('runtime/artifact/probe', {});
    await client.request('runtime/artifact/acquire', { source: 'github' });
    await client.uploadArtifact(archive);

    const fixedBase = 'https://manager.example/.well-known/cpamp-bootstrap/api/v2/';
    expect(fetchMock.mock.calls[1]).toEqual([
      `${fixedBase}runtime/artifact`,
      expect.objectContaining({
        method: 'GET',
        headers: { Accept: 'application/json', Authorization: `Fresh ${handoff.token}` },
      }),
    ]);
    expect(fetchMock.mock.calls[2]).toEqual([
      `${fixedBase}runtime/artifact/probe`,
      expect.objectContaining({
        method: 'POST',
        headers: {
          Accept: 'application/json',
          'Content-Type': 'application/json',
          Authorization: `Fresh ${handoff.token}`,
        },
        body: '{}',
      }),
    ]);
    expect(fetchMock.mock.calls[3]).toEqual([
      `${fixedBase}runtime/artifact/acquire`,
      expect.objectContaining({
        method: 'POST',
        headers: {
          Accept: 'application/json',
          'Content-Type': 'application/json',
          Authorization: `Fresh ${handoff.token}`,
        },
        body: JSON.stringify({ source: 'github' }),
      }),
    ]);
    expect(fetchMock.mock.calls[4][0]).toBe(`${fixedBase}runtime/artifact/upload`);
    expect(fetchMock.mock.calls[4][1]).toMatchObject({
      method: 'PUT',
      headers: {
        Accept: 'application/json',
        'Content-Type': 'application/gzip',
        Authorization: `Fresh ${handoff.token}`,
      },
    });
    expect(fetchMock.mock.calls[4][1]?.body).toBe(archive);

    expect(await client.activateRouting()).toBe('/console');

    await client.request('runtime/artifact');
    await client.request('runtime/artifact/probe', {});
    await client.request('runtime/artifact/acquire', {
      source: 'cdn',
      cdnPrefix: 'https://mirror.example.com/',
    });
    await client.request('runtime/artifact/upload', archive);

    const canonicalBase = 'https://manager.example/console/api/v2/';
    expect(fetchMock.mock.calls[7][0]).toBe(`${canonicalBase}runtime/artifact`);
    expect(fetchMock.mock.calls[8][0]).toBe(`${canonicalBase}runtime/artifact/probe`);
    expect(fetchMock.mock.calls[9][0]).toBe(`${canonicalBase}runtime/artifact/acquire`);
    expect(fetchMock.mock.calls[10][0]).toBe(`${canonicalBase}runtime/artifact/upload`);
    expect(fetchMock.mock.calls[10][1]?.body).toBe(archive);
    expect(fetchMock.mock.calls[10][1]?.headers).toEqual({
      Accept: 'application/json',
      'Content-Type': 'application/gzip',
      Authorization: `Fresh ${handoff.token}`,
    });

    await expect(client.request('runtime/artifact')).rejects.toMatchObject({
      kind: 'conflict',
      status: 409,
      code: 'unsupported_platform',
    });
  });

  it('does not abort a raw gzip upload at the 15s JSON read deadline', async () => {
    vi.useFakeTimers();
    try {
      const archive = new Blob([new Uint8Array([0x1f, 0x8b, 0x08, 0x00])], {
        type: 'application/gzip',
      });
      let resolveUpload!: (res: Response) => void;
      const fetchMock = vi
        .fn<typeof fetch>()
        .mockResolvedValueOnce(reply(prepared))
        .mockImplementationOnce((_url, options) => {
          expect(options?.signal).toBeUndefined();
          return new Promise<Response>((resolve) => {
            resolveUpload = resolve;
          });
        });
      const client = createBootstrapTransport(fetchMock);
      await client.start({ ...handoff, resume: true });

      const pending = client.uploadArtifact(archive);
      await vi.advanceTimersByTimeAsync(20_000);
      resolveUpload(reply({ operation: { id: 'op-slow' } }));
      await expect(pending).resolves.toEqual({ operation: { id: 'op-slow' } });
    } finally {
      vi.useRealTimers();
    }
  });
});
