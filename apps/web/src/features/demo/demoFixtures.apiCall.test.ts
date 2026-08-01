import { afterEach, describe, expect, it } from 'vitest';
import { apiCallApi, getApiCallErrorDetails, getApiCallErrorMessage } from '@/services/api/apiCall';
import { getDemoApiCallResult } from './demoFixtures';
import { setDemoMode } from './demoMode';

afterEach(() => {
  setDemoMode(false);
});

describe('API call demo fixtures', () => {
  it('returns a deterministic upstream 401 for the reserved forbidden host', () => {
    const result = getDemoApiCallResult({
      method: 'POST',
      url: 'https://forbidden.demo.invalid/v1/chat/completions',
    });

    expect(result).toMatchObject({
      status_code: 401,
      has_status_code: true,
      header: {
        'access-control-allow-origin': ['*'],
        'content-type': ['application/json'],
        'x-request-id': ['demo-forbidden-request'],
      },
    });
    expect(result.body).toBe(JSON.stringify({ error: { code: 16, message: 'Forbidden' } }));
  });

  it('keeps ordinary API calls successful', () => {
    const result = getDemoApiCallResult({
      method: 'POST',
      url: 'https://api.openai.example/v1/chat/completions',
    });

    expect(result.status_code).toBe(200);
    expect(result.has_status_code).toBe(true);
  });

  it('flows through the demo API client as a displayable 401 error', async () => {
    setDemoMode(true);

    const result = await apiCallApi.request({
      method: 'POST',
      url: 'https://forbidden.demo.invalid/v1/chat/completions',
    });

    expect(result.statusCode).toBe(401);
    expect(result.hasStatusCode).toBe(true);
    expect(result.body).toEqual({ error: { code: 16, message: 'Forbidden' } });
    expect(getApiCallErrorMessage(result)).toBe('401 Forbidden');
    expect(getApiCallErrorDetails(result)).toBe(
      [
        '401 Forbidden',
        '',
        'Body:',
        '{',
        '  "error": {',
        '    "code": 16,',
        '    "message": "Forbidden"',
        '  }',
        '}',
      ].join('\n')
    );
  });
});
