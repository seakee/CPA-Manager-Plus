import { describe, expect, it, vi } from 'vitest';
import type { ApiCallResult } from '@/services/api/apiCall';
import { requestOpenAIConnectivityTest } from './openAIConnectivityTest';

const result = (statusCode: number, error?: Record<string, string>): ApiCallResult => ({
  statusCode,
  hasStatusCode: true,
  header: {},
  body: error ? { error } : { choices: [{ message: { content: 'OK' } }] },
  bodyText: error ? JSON.stringify({ error }) : '{}',
});

const payload = {
  method: 'POST',
  url: 'https://model.example/v1/chat/completions',
  authIndex: 'existing-credential',
  proxyUrl: 'http://proxy.example',
  header: { Authorization: 'Bearer $TOKEN$', 'X-Provider': 'test' },
};
const config = { timeout: 30_000 };

describe('OpenAI connectivity test compatibility', () => {
  it.each(['gpt-6.1-terra', 'gpt-6.1-sol', 'official/gpt-6.1-terra', 'gpt-5.4', 'o3'])(
    'probes %s directly using completion tokens',
    async (model) => {
      const send = vi.fn().mockResolvedValue(result(200));
      await requestOpenAIConnectivityTest(send, payload, model, config);
      expect(send).toHaveBeenCalledTimes(1);
      expect(send).toHaveBeenCalledWith({ ...payload, data: expect.any(String) }, config);
      const body = JSON.parse(send.mock.calls[0][0].data);
      expect(body).toEqual({
        model,
        messages: [{ role: 'user', content: 'Reply with OK.' }],
        stream: false,
        max_completion_tokens: 128,
      });
      expect(body).not.toHaveProperty('max_tokens');
      expect(body).not.toHaveProperty('temperature');
    }
  );

  it('preserves legacy compatible model requests', async () => {
    const send = vi.fn().mockResolvedValue(result(200));
    await requestOpenAIConnectivityTest(send, payload, 'local-model');
    expect(JSON.parse(send.mock.calls[0][0].data)).toMatchObject({ max_tokens: 5 });
    expect(send).toHaveBeenCalledTimes(1);
  });

  it('retries an aliased modern model once after an explicit max_tokens rejection', async () => {
    const send = vi
      .fn()
      .mockResolvedValueOnce(
        result(400, {
          param: 'max_tokens',
          code: 'unsupported_parameter',
          message: 'Use max_completion_tokens instead.',
        })
      )
      .mockResolvedValueOnce(result(200));
    expect(
      (await requestOpenAIConnectivityTest(send, payload, 'my-terra', config)).statusCode
    ).toBe(200);
    expect(send).toHaveBeenCalledTimes(2);
    expect(JSON.parse(send.mock.calls[1][0].data)).toMatchObject({ max_completion_tokens: 5 });
    expect(JSON.parse(send.mock.calls[1][0].data)).not.toHaveProperty('max_tokens');
    expect(send.mock.calls[1][0]).toMatchObject(payload);
    expect(send.mock.calls[1][1]).toBe(config);
  });

  it('supports legacy providers rejecting completion tokens without retrying indefinitely', async () => {
    const failure = result(400, { message: "Unknown parameter 'max_completion_tokens'" });
    const send = vi.fn().mockResolvedValue(failure);
    expect(await requestOpenAIConnectivityTest(send, payload, 'gpt-6.1-sol')).toBe(failure);
    expect(send).toHaveBeenCalledTimes(2);
    const body = JSON.parse(send.mock.calls[1][0].data);
    expect(body).toMatchObject({ max_tokens: 128 });
    expect(body).not.toHaveProperty('max_completion_tokens');
  });

  it.each([401, 403, 429, 500])('does not retry HTTP %s errors', async (status) => {
    const send = vi.fn().mockResolvedValue(result(status, { message: 'max_tokens not supported' }));
    await requestOpenAIConnectivityTest(send, payload, 'local-model');
    expect(send).toHaveBeenCalledTimes(1);
  });

  it('does not treat a different parameter error as a token compatibility error', async () => {
    const send = vi.fn().mockResolvedValue(
      result(400, {
        param: 'model',
        code: 'unsupported_parameter',
        message: 'Unsupported model',
      })
    );
    await requestOpenAIConnectivityTest(send, payload, 'gpt-6.1-sol');
    expect(send).toHaveBeenCalledTimes(1);
  });

  it('does not retry network timeouts', async () => {
    const send = vi.fn().mockRejectedValue(new Error('timeout'));
    await expect(requestOpenAIConnectivityTest(send, payload, 'gpt-6.1-terra')).rejects.toThrow(
      'timeout'
    );
    expect(send).toHaveBeenCalledTimes(1);
  });
});
