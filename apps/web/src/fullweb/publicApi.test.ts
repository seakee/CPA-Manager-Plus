import { afterEach, describe, expect, it, vi } from 'vitest';
import { createPublicApiClient, type FullWebCredential } from './publicApi';
import { resolveRoutingContext, type RoutingContext } from './routingContext';
import r1Transport from '../../../../tests/fixtures/r1-embedded-api-transport.json';

const context = resolveRoutingContext({
  state: 'verified_effective',
  verifiedEffectiveControlBasePath: '/phase4-test',
  revision: 1,
})!;
const credential: FullWebCredential = { kind: 'admin', value: 'full-web-admin-secret' };

function browserOrigin(origin = 'https://manager.example:18317') {
  vi.stubGlobal('window', { location: new URL(origin) });
  vi.stubGlobal('localStorage', {
    getItem: () => {
      throw new Error('Full Web cannot read persisted CPA state');
    },
  });
}

afterEach(() => vi.unstubAllGlobals());

describe('canonical Full Web Public API transport', () => {
  it('uses the R1 Embedded transport fixture to construct the exact authorized request', async () => {
    expect(r1Transport.schemaVersion).toBe(1);
    expect(r1Transport.qualification).toBe(
      'SOURCE_TRANSPORT_CONTRACT_ONLY_NOT_COMPLETE_WEB_API_COMPATIBILITY'
    );
    browserOrigin(r1Transport.origin);
    expect(r1Transport.snapshot.state).toBe('verified_effective');
    const verified = resolveRoutingContext({
      state: 'verified_effective',
      verifiedEffectiveControlBasePath: r1Transport.snapshot.verifiedEffectiveControlBasePath,
      revision: r1Transport.snapshot.revision,
    })!;
    expect(verified.apiBase).toBe(
      r1Transport.snapshot.verifiedEffectiveControlBasePath + r1Transport.apiSuffix
    );
    const transport = vi.fn<typeof fetch>().mockResolvedValue(Response.json(r1Transport.expectedResponse));
    const result = await createPublicApiClient(() => verified, transport).get(
      r1Transport.resource,
      credential
    );
    expect(result).toEqual(r1Transport.expectedResponse);
    expect(transport).toHaveBeenCalledTimes(1);
    expect(transport).toHaveBeenCalledWith(
      r1Transport.origin + r1Transport.expectedPath,
      expect.objectContaining({
        method: r1Transport.method,
        headers: { Accept: 'application/json', Authorization: 'Bearer full-web-admin-secret' },
        mode: 'same-origin',
        credentials: 'omit',
        redirect: 'error',
      })
    );
  });

  it.each([
    ['gateway/config/groups/retry', 'invalid_config_request', 'invalid_config_request'],
    [
      'gateway/config/operations/' + 'a'.repeat(32) + '/reconcile',
      'original_operation_unknown',
      'original_operation_unknown',
    ],
    ['gateway/config/groups/retry', 'raw upstream secret', undefined],
    ['api-keys', 'invalid_config_field', undefined],
  ])('bounds HTTP 400 parsing for %s (%s)', async (resource, code, expectedCode) => {
    browserOrigin();
    const transport = vi
      .fn<typeof fetch>()
      .mockResolvedValue(
        Response.json(
          { code, message: 'raw upstream secret', result: { secret: 'hidden' } },
          { status: 400 }
        )
      );
    const error = await createPublicApiClient(() => context, transport)
      .post(resource, {}, credential)
      .catch((value: unknown) => value);
    expect(error).toMatchObject({
      kind: 'http',
      status: 400,
      code: expectedCode,
      operationResult: undefined,
      message: 'Public API http',
    });
    expect(transport).toHaveBeenCalledTimes(1);
    // The unbounded `result` blob never reaches the page; a recognised code may
    // still expose the Manager-authored diagnostic that names what was refused.
    expect(JSON.stringify(error)).not.toContain('hidden');
    expect(JSON.stringify(error)).not.toContain('"result"');
    if (expectedCode === undefined) expect((error as { detail?: string }).detail).toBeUndefined();
  });

  // A CPA rejection is the only 422 the page maps: it names the group or field and
  // carries the redacted diagnostic the Manager already stripped of values.
  it('retains a bounded CPA rejection with its field and diagnostic', async () => {
    browserOrigin();
    const transport = vi.fn<typeof fetch>().mockResolvedValue(
      Response.json(
        {
          code: 'cpa_rejected_config',
          field: 'routing.retry.request-retry',
          message: 'yaml: unmarshal errors: line 3: cannot unmarshal !!str into int',
          result: { secret: 'hidden' },
        },
        { status: 422 }
      )
    );
    const error = (await createPublicApiClient(() => context, transport)
      .post('gateway/config/groups/retry', {}, credential)
      .catch((value) => value)) as Error & {
      code?: string;
      field?: string;
      detail?: string;
      operationResult?: unknown;
    };
    expect(error).toMatchObject({
      kind: 'http',
      status: 422,
      code: 'cpa_rejected_config',
      field: 'routing.retry.request-retry',
      detail: 'yaml: unmarshal errors: line 3: cannot unmarshal !!str into int',
      operationResult: undefined,
    });
    expect(JSON.stringify(error)).not.toContain('hidden');
  });

  it('retains the Manager private-range refusal on its field', async () => {
    browserOrigin();
    const transport = vi.fn<typeof fetch>().mockResolvedValue(
      Response.json(
        {
          code: 'invalid_config',
          field: 'requests.streaming.keepalive-seconds',
          message: 'value outside the private profile range',
        },
        { status: 422 }
      )
    );
    const error = (await createPublicApiClient(() => context, transport)
      .post('gateway/config/groups/streaming', {}, credential)
      .catch((value) => value)) as Error & { code?: string; field?: string; detail?: string };
    expect(error).toMatchObject({
      kind: 'http',
      status: 422,
      code: 'invalid_config',
      field: 'requests.streaming.keepalive-seconds',
      detail: 'value outside the private profile range',
    });
  });

  // An External install saves the gateway document through its committed CPA
  // credential, so the same conflict carries any External code. Before the merge
  // the gateway parser dropped it, `blockerOf` saw nothing and the page showed a
  // bare status with no way out.
  it('parses an External refusal on the gateway document', async () => {
    browserOrigin();
    const transport = vi.fn<typeof fetch>().mockResolvedValue(
      Response.json(
        { code: 'external_management_key_rejected', detail: 'the CPA replaced this key' },
        { status: 409 }
      )
    );
    const error = (await createPublicApiClient(() => context, transport)
      .post('gateway/config/groups/retry', {}, credential)
      .catch((value) => value)) as Error & { code?: string; detail?: string };
    expect(error).toMatchObject({
      kind: 'http',
      status: 409,
      code: 'external_management_key_rejected',
      detail: 'the CPA replaced this key',
    });
  });

  // A single key is written through the same credential as the list, so its
  // writes answer the same typed codes. The operation journal keeps its own
  // answer: widening the resource must not swallow `key_operation_*` results.
  it('parses an External refusal on one key without taking over its operation journal', async () => {
    browserOrigin();
    const refused = vi.fn<typeof fetch>().mockResolvedValue(
      Response.json({ code: 'external_management_key_rejected' }, { status: 409 })
    );
    const refusal = (await createPublicApiClient(() => context, refused)
      .put('api-keys/key-7', {}, credential)
      .catch((value) => value)) as Error & { code?: string };
    expect(refusal).toMatchObject({ kind: 'http', status: 409, code: 'external_management_key_rejected' });

    const journal = vi.fn<typeof fetch>().mockResolvedValue(
      Response.json(
        { code: 'key_operation_pending', result: { operationId: 'operation-1', status: 'pending' } },
        { status: 409 }
      )
    );
    const pending = (await createPublicApiClient(() => context, journal)
      .get('api-keys/operations/operation-1', credential)
      .catch((value) => value)) as Error & { operationResult?: unknown };
    expect(pending.operationResult).toEqual({ operationId: 'operation-1', status: 'pending' });
  });

  it('keeps a zero leaf a number in the gateway projection', async () => {
    browserOrigin();
    const transport = vi.fn<typeof fetch>().mockResolvedValue(
      Response.json({
        capability: { mode: 'managed', readable: true, writable: true, rulesWritable: true },
        generation: '7',
        groups: [{ id: 'retry', prefix: 'routing.retry', paths: [], baseline: 'a'.repeat(64), writable: true, persisted: 'x', effective: 'unknown' }],
        fields: [
          { path: 'routing.retry.request-retry', type: 'int', kind: 'number', group: 'retry', presence: 'present', value: 0, editable: true },
        ],
        families: [],
        groupsInvalid: false,
      })
    );
    const view = await createPublicApiClient(() => context, transport).get<{
      fields: { path: string; value: unknown }[];
    }>('gateway/config', credential);
    expect(view.fields[0].value).toBe(0);
    expect(typeof view.fields[0].value).toBe('number');
  });

  it.each(['GET', 'PUT', 'operation'])('bounds setting/operation %s without retries', async (method) => {
    browserOrigin();
    vi.useFakeTimers();
    try {
      const transport = vi.fn<typeof fetch>(
        (_url, options) =>
          new Promise((_resolve, reject) => {
            options?.signal?.addEventListener('abort', () => reject(new Error('private detail')));
          })
      );
      const client = createPublicApiClient(() => context, transport);
      const pending =
        method === 'operation'
          ? client.get('operation', credential)
          : method === 'GET'
          ? client.get('settings/update-channel', credential)
          : client.put('settings/update-channel', { channel_preference: 'stable' }, credential);
      const assertion = expect(pending).rejects.toMatchObject({ kind: 'network' });
      await vi.advanceTimersByTimeAsync(15000);
      await assertion;
      expect(transport).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });
  it('bounds a create timeout without retrying or reflecting transport secrets', async () => {
    browserOrigin();
    vi.useFakeTimers();
    try {
      const transport = vi.fn<typeof fetch>(
        (_url, options) =>
          new Promise((_resolve, reject) => {
            options?.signal?.addEventListener('abort', () => reject(new Error('private detail')));
          })
      );
      const pending = createPublicApiClient(() => context, transport).post(
        'api-keys',
        { requestId: 'a'.repeat(32) },
        credential
      );
      const assertion = expect(pending).rejects.toMatchObject({
        kind: 'network',
        message: 'Public API network',
      });
      await vi.advanceTimersByTimeAsync(15000);
      await assertion;
      expect(transport).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it('bounds retry reads and exposes only the bounded retry code and operation result', async () => {
    browserOrigin();
    vi.useFakeTimers();
    try {
      const transport = vi.fn<typeof fetch>(
        (_url, options) =>
          new Promise((_resolve, reject) => {
            options?.signal?.addEventListener('abort', () => reject(new Error('private detail')));
          })
      );
      const pending = createPublicApiClient(() => context, transport).get(
        'gateway/config',
        credential
      );
      const assertion = expect(pending).rejects.toMatchObject({
        kind: 'network',
        message: 'Public API network',
      });
      await vi.advanceTimersByTimeAsync(15000);
      await assertion;
      expect(transport).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it('retains a sanitized retry conflict code and original operation result without upstream detail', async () => {
    browserOrigin();
    const operation = {
      requestId: 'a'.repeat(32),
      value: 3,
      sourceId: 'isolated-source-1234',
      generation: '7',
      revision: 2,
      sha256: 'b'.repeat(64),
      state: 'unknown',
    };
    const transport = vi.fn<typeof fetch>().mockResolvedValue(
      Response.json(
        {
          code: 'source_or_operation_conflict',
          result: { operation, effective: 'unknown' },
          message: 'raw upstream error must not escape',
        },
        { status: 409 }
      )
    );
    const error = (await createPublicApiClient(() => context, transport)
      .post('gateway/config/groups/retry', { requestId: operation.requestId, edits: [] }, credential)
      .catch((value) => value)) as Error & { code?: string; operationResult?: unknown };
    expect(error).toMatchObject({
      kind: 'http',
      status: 409,
      code: 'source_or_operation_conflict',
      operationResult: { operation, effective: 'unknown' },
      message: 'Public API http',
    });
    expect(error.message).not.toContain('raw upstream error');
  });

  it('uses the same canonical non-root base and explicit in-memory credential', async () => {
    browserOrigin();
    const transport = vi.fn<typeof fetch>().mockResolvedValue(Response.json({ technical: true }));
    const client = createPublicApiClient(() => context, transport);
    expect(await client.get('technical', credential)).toEqual({ technical: true });
    expect(transport).toHaveBeenCalledWith(
      'https://manager.example:18317/phase4-test/api/v2/technical',
      {
        method: 'GET',
        headers: { Accept: 'application/json', Authorization: 'Bearer full-web-admin-secret' },
        mode: 'same-origin',
        credentials: 'omit',
        redirect: 'error',
        cache: 'no-store',
      }
    );
  });

  it('does not inherit CPA keys or connection settings for unauthenticated requests', async () => {
    browserOrigin();
    const transport = vi.fn<typeof fetch>().mockResolvedValue(Response.json({}));
    await createPublicApiClient(() => context, transport).get('technical');
    expect(transport.mock.calls[0][1]?.headers).toEqual({ Accept: 'application/json' });
  });

  it('serves the full serialization-stable prefix grammar without a second base model', async () => {
    browserOrigin();
    const basePath = '/AZaz09-._~$&+,/:;=@';
    const stableContext = resolveRoutingContext({
      state: 'verified_effective',
      verifiedEffectiveControlBasePath: basePath,
      revision: 1,
    });
    const transport = vi.fn<typeof fetch>().mockResolvedValue(Response.json({}));
    await createPublicApiClient(() => stableContext, transport).get('technical', credential);
    expect(transport.mock.calls[0][0]).toBe(
      `https://manager.example:18317${basePath}/api/v2/technical`
    );
  });

  it.each([
    '',
    '/',
    '/v1/models',
    '/v0/management',
    '/api/v2',
    '/favicon.ico',
    '/apple-touch-icon.png',
    '//external.example/path',
    'https://external.example/path',
    'http://cpa:8317/v0/management',
    'http://cpa:9081',
    '../models',
    'v0/../management',
    'technical?target=/v0/management',
    'technical#x',
    'technical%2f..',
    'technical\\x',
  ])('rejects a non-resource destination %j before adding credentials', async (resource) => {
    browserOrigin();
    const transport = vi.fn<typeof fetch>();
    await expect(
      createPublicApiClient(() => context, transport).get(resource, credential)
    ).rejects.toMatchObject({ kind: 'routing_unavailable' });
    expect(transport).not.toHaveBeenCalled();
  });

  it.each([
    null,
    { ...context, apiBase: '/v0/management' },
    { ...context, apiBase: 'https://evil/api/v2' },
    { ...context, basePath: '//external.example' },
    { ...context, revision: 0 },
    { ...context, basePath: '/控制', apiBase: '/控制/api/v2' },
    { ...context, basePath: '/control[admin]', apiBase: '/control[admin]/api/v2' },
  ])('fails closed for unavailable or inconsistent context %j', async (value) => {
    browserOrigin();
    const transport = vi.fn<typeof fetch>();
    await expect(
      createPublicApiClient(() => value as RoutingContext | null, transport).get(
        'technical',
        credential
      )
    ).rejects.toMatchObject({ kind: 'routing_unavailable' });
    expect(transport).not.toHaveBeenCalled();
  });

  it.each(['http://manager.example:8317', 'http://manager.example:9081'])(
    'uses verified canonical authority on a custom public origin %s, not port heuristics',
    async (origin) => {
      browserOrigin(origin);
      const transport = vi.fn<typeof fetch>().mockResolvedValue(Response.json({ technical: true }));
      const client = createPublicApiClient(() => context, transport);
      await expect(client.get('technical', credential)).resolves.toEqual({ technical: true });
      expect(transport).toHaveBeenCalledWith(`${origin}/phase4-test/api/v2/technical`, {
        method: 'GET',
        headers: { Accept: 'application/json', Authorization: 'Bearer full-web-admin-secret' },
        mode: 'same-origin',
        credentials: 'omit',
        redirect: 'error',
        cache: 'no-store',
      });

      transport.mockClear();
      for (const invalidContext of [null, { ...context, apiBase: '/v0/management' }]) {
        await expect(
          createPublicApiClient(() => invalidContext, transport).get('technical', credential)
        ).rejects.toMatchObject({ kind: 'routing_unavailable' });
      }
      for (const destination of ['/v1/models', '/v0/management', 'http://cpa:8317/v0/management']) {
        await expect(client.get(destination, credential)).rejects.toMatchObject({
          kind: 'routing_unavailable',
        });
      }
      expect(transport).not.toHaveBeenCalled();
    }
  );

  it.each(['file:///tmp/index.html', 'ftp://manager.example'])(
    'never sends Full Web credentials from a non-HTTP context %s',
    async (origin) => {
      browserOrigin(origin);
      const transport = vi.fn<typeof fetch>();
      await expect(
        createPublicApiClient(() => context, transport).get('technical', credential)
      ).rejects.toMatchObject({ kind: 'routing_unavailable' });
      expect(transport).not.toHaveBeenCalled();
    }
  );

  it.each([
    [401, 'authentication'],
    [403, 'authentication'],
    [404, 'http'],
    [500, 'http'],
  ])('classifies HTTP %i as %s', async (status, kind) => {
    browserOrigin();
    const transport = vi
      .fn<typeof fetch>()
      .mockResolvedValue(new Response(null, { status: status as number }));
    await expect(
      createPublicApiClient(() => context, transport).get('technical')
    ).rejects.toMatchObject({ kind, status });
  });

  it('classifies network failure without exposing credential or transport error details', async () => {
    browserOrigin();
    const transport = vi.fn<typeof fetch>().mockRejectedValue(new Error(credential.value));
    await expect(
      createPublicApiClient(() => context, transport).get('technical', credential)
    ).rejects.toMatchObject({ kind: 'network', message: 'Public API network' });
  });

  it('does not accept an accidental HTML SPA response as API success', async () => {
    browserOrigin();
    const transport = vi.fn<typeof fetch>().mockResolvedValue(new Response('<html>carrier</html>'));
    await expect(
      createPublicApiClient(() => context, transport).get('technical')
    ).rejects.toMatchObject({ kind: 'network' });
  });
});
