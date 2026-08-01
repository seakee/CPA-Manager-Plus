import { describe, expect, it } from 'vitest';
import { getApiCallErrorDetails, type ApiCallResult } from './apiCall';

const buildResult = (overrides: Partial<ApiCallResult> = {}): ApiCallResult => ({
  statusCode: 401,
  hasStatusCode: true,
  header: {},
  bodyText: '',
  body: null,
  ...overrides,
});

describe('getApiCallErrorDetails', () => {
  it('keeps the complete structured response body', () => {
    const result = buildResult({
      bodyText: '{"error":{"code":16,"message":"Forbidden"}}',
      body: { error: { code: 16, message: 'Forbidden' } },
    });

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

  it('preserves a plain-text response body', () => {
    const result = buildResult({ bodyText: 'upstream access denied', body: 'upstream access denied' });

    expect(getApiCallErrorDetails(result)).toBe(
      '401 upstream access denied\n\nBody:\nupstream access denied'
    );
  });

  it('returns the summary when the response body is empty', () => {
    expect(getApiCallErrorDetails(buildResult())).toBe('HTTP 401');
  });
});
