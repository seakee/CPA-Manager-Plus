import axios, { AxiosError, type AxiosResponse } from 'axios';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  cpaUpdateApi,
  canPrepareCPAUpdate,
  type CPAUpdatePhase,
  type CPAUpdateStatus,
} from './cpaUpdateApi';
import {
  mutationResult,
  observationResult,
  replacementArtifact,
  updateIntent,
  updateStatus,
} from './cpaUpdateTestFixtures';

const mocks = vi.hoisted(() => ({ demo: false }));
vi.mock('@/features/demo/demoMode', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/demo/demoMode')>()),
  isDemoMode: () => mocks.demo,
}));
const connection = { base: 'https://manager.test/v0/management/', key: 'test-key' };
const response = (data: unknown) => ({ data }) as AxiosResponse;
const httpError = (status: number, data: unknown) =>
  new AxiosError('private raw message', 'ERR_BAD_RESPONSE', undefined, undefined, {
    status,
    data,
  } as AxiosResponse);
const request = vi.spyOn(axios, 'request');
beforeEach(() => {
  mocks.demo = false;
  request.mockReset();
});
afterEach(() => {
  vi.clearAllMocks();
});

describe('accepted CPA update HTTP contract', () => {
  it('preserves canonical versions from the Manager CPA contract through prepare and observation', async () => {
    const status = updateStatus({ current_version: '7.2.3', target_version: '7.3.4' });
    request.mockResolvedValueOnce(response(status));
    expect(canPrepareCPAUpdate(await cpaUpdateApi.status(connection))).toBe(true);
    const intent = updateIntent({ target_version: status.target_version });
    request.mockResolvedValueOnce(response(mutationResult('prepare', intent)));
    await cpaUpdateApi.mutate(connection, 'prepare', intent);
    expect(request.mock.calls[1][0].data).toMatchObject({ target_version: '7.3.4' });
    request.mockResolvedValueOnce(response(observationResult(intent, 'succeeded')));
    await cpaUpdateApi.observe(connection, 'prepare', intent);
    expect(request.mock.calls[2][0].params).toEqual({
      request_id: intent.request_id,
      target_version: '7.3.4',
    });
  });

  it.each([
    'v7.3.4',
    '07.3.4',
    '7.3.4-rc.1',
    '7.3.4+build',
    '7.3',
    ' 7.3.4',
    '7.3.4 ',
    '1'.repeat(93) + '.0.0',
  ])(
    'rejects noncanonical or oversized CPA target versions without a request: %s',
    async (target_version) => {
      expect(canPrepareCPAUpdate(updateStatus({ target_version }))).toBe(false);
      const intent = updateIntent({ target_version });
      await expect(cpaUpdateApi.mutate(connection, 'prepare', intent)).rejects.toMatchObject({
        code: 'invalid_request',
      });
      await expect(cpaUpdateApi.observe(connection, 'prepare', intent)).rejects.toMatchObject({
        code: 'invalid_request',
      });
      expect(request).not.toHaveBeenCalled();
    }
  );

  it('uses status GET and preserves the existing 30 second transport timeout', async () => {
    request.mockResolvedValue(response(updateStatus()));
    await cpaUpdateApi.status(connection);
    expect(request).toHaveBeenCalledExactlyOnceWith(
      expect.objectContaining({
        url: 'https://manager.test/usage-service/runtime/updates',
        method: 'GET',
        timeout: 30_000,
        headers: { Authorization: 'Bearer test-key' },
      })
    );
    expect(request.mock.calls[0][0].data).toBeUndefined();
  });

  it('checks only through an explicit bodyless POST', async () => {
    request.mockResolvedValue(response(updateStatus()));
    await cpaUpdateApi.check(connection);
    expect(request).toHaveBeenCalledExactlyOnceWith(
      expect.objectContaining({
        url: 'https://manager.test/usage-service/runtime/updates/check',
        method: 'POST',
      })
    );
    expect(request.mock.calls[0][0].data).toBeUndefined();
  });

  it.each(['prepare', 'activate'] as const)(
    'sends only the original three guards for %s',
    async (phase) => {
      const intent = updateIntent({ phase });
      request.mockResolvedValue(response(mutationResult(phase, intent)));
      await cpaUpdateApi.mutate(connection, phase, intent);
      expect(request.mock.calls[0][0].data).toEqual({
        request_id: intent.request_id,
        target_version: intent.target_version,
        expected_active_artifact_id: intent.expected_active_artifact_id,
      });
      expect(request.mock.calls[0][0].url?.endsWith('/' + phase)).toBe(true);
    }
  );

  it.each(['not_found', 'accepted', 'running', 'succeeded', 'failed'] as const)(
    'observes %s using only phase/request ID/target and GET',
    async (state) => {
      const intent = updateIntent();
      request.mockResolvedValue(response(observationResult(intent, state)));
      expect(await cpaUpdateApi.observe(connection, 'prepare', intent)).toMatchObject({ state });
      const config = request.mock.calls[0][0];
      expect(config.method).toBe('GET');
      expect(config.url?.endsWith('/operations/prepare')).toBe(true);
      expect(config.params).toEqual({
        request_id: intent.request_id,
        target_version: intent.target_version,
      });
      expect(config.data).toBeUndefined();
    }
  );

  it('accepts correlated durable failure evidence carried by HTTP 502', async () => {
    const intent = updateIntent();
    const failed = mutationResult('prepare', intent, {
      state: 'failed',
      failure_code: 'target_stage_corrupt',
    });
    request.mockRejectedValue(httpError(502, failed));
    expect(await cpaUpdateApi.mutate(connection, 'prepare', intent)).toEqual(failed);
  });

  it.each([
    new AxiosError('timeout of 30000ms exceeded', 'ECONNABORTED'),
    new AxiosError('Network Error', 'ERR_NETWORK'),
    httpError(502, { error: 'private raw message' }),
    httpError(503, { code: 'runtime_transport_unavailable', error: 'private raw message' }),
    httpError(404, { code: 'operation_not_found' }),
  ])('does not turn transport/HTTP errors into terminal evidence', async (error) => {
    request.mockRejectedValue(error);
    await expect(cpaUpdateApi.mutate(connection, 'prepare', updateIntent())).rejects.toMatchObject({
      kind: 'unavailable',
    });
    await expect(cpaUpdateApi.observe(connection, 'prepare', updateIntent())).rejects.toMatchObject(
      { kind: 'unavailable' }
    );
  });

  it.each([
    'stale_runtime_generation',
    'runtime_identity_mismatch',
    'operation_id_conflict',
    'active_artifact_mismatch',
    'target_version_mismatch',
    'recommendation_not_fresh',
  ])('keeps %s as a context conflict with no raw message exposure', async (code) => {
    request.mockRejectedValue(httpError(409, { code, error: 'secret /runtime/path token' }));
    await expect(cpaUpdateApi.observe(connection, 'prepare', updateIntent())).rejects.toMatchObject(
      {
        kind: 'context_changed',
        code,
        message: code,
      }
    );
  });

  it.each([
    { phase: 'activate' as CPAUpdatePhase },
    { request_id: 'someone-else' },
    { target_version: '7.3.0' },
    { expected_active_artifact_id: replacementArtifact },
    { runtime_operation_id: '' },
    { already_applied: true },
    { state: 'failed' as const, failure_code: 'raw secret with spaces' },
  ])('rejects mismatched or unreliable mutation results: %o', async (patch) => {
    request.mockResolvedValue(response(mutationResult('prepare', updateIntent(), patch)));
    await expect(cpaUpdateApi.mutate(connection, 'prepare', updateIntent())).rejects.toMatchObject({
      kind: 'unavailable',
    });
  });

  it('preserves direct already_applied, but never adopts it from observation', async () => {
    const intent = updateIntent({ phase: 'activate' });
    request.mockResolvedValueOnce(
      response(mutationResult('activate', intent, { already_applied: true }))
    );
    expect(await cpaUpdateApi.mutate(connection, 'activate', intent)).toMatchObject({
      already_applied: true,
    });
    request.mockResolvedValueOnce(
      response({ ...observationResult(intent, 'succeeded'), already_applied: true })
    );
    expect(await cpaUpdateApi.observe(connection, 'activate', intent)).not.toHaveProperty(
      'already_applied'
    );
  });

  it.each([
    { mode: 'external' as const },
    { state: 'never_checked' as const },
    { state: 'up_to_date' as const },
    { state: 'ahead_of_stable' as const },
    { state: 'unknown_version' as const },
    { state: 'unsupported' as const },
    { state: 'managed_externally' as const },
    { actionable: false },
    { stale: true },
    { last_error: 'failed' },
    { prepare_supported: false },
    { activate_supported: false },
    { target_version: undefined },
    { active_artifact_id: undefined },
    { target_version: 'latest' },
  ] satisfies Partial<CPAUpdateStatus>[])('requires every update admission fact: %o', (patch) => {
    expect(canPrepareCPAUpdate(updateStatus(patch))).toBe(false);
    expect(canPrepareCPAUpdate(updateStatus())).toBe(true);
  });

  it('rejects malformed status and untrusted request guards', async () => {
    request.mockResolvedValue(response({ ...updateStatus(), actionable: 'true' }));
    await expect(cpaUpdateApi.status(connection)).rejects.toMatchObject({ kind: 'unavailable' });
    request.mockClear();
    await expect(
      cpaUpdateApi.mutate(connection, 'prepare', updateIntent({ target_version: 'latest' }))
    ).rejects.toMatchObject({ code: 'invalid_request' });
    await expect(
      cpaUpdateApi.observe(connection, 'prepare', updateIntent({ request_id: '' }))
    ).rejects.toMatchObject({ code: 'invalid_request' });
    expect(request).not.toHaveBeenCalled();
  });

  it('performs zero real requests in demo mode, including direct transport calls', async () => {
    mocks.demo = true;
    const intent = updateIntent();
    for (const call of [
      () => cpaUpdateApi.status(connection),
      () => cpaUpdateApi.check(connection),
      () => cpaUpdateApi.mutate(connection, 'prepare', intent),
      () => cpaUpdateApi.mutate(connection, 'activate', intent),
      () => cpaUpdateApi.observe(connection, 'prepare', intent),
    ])
      await expect(call()).rejects.toMatchObject({ kind: 'unavailable' });
    expect(request).not.toHaveBeenCalled();
  });
});
