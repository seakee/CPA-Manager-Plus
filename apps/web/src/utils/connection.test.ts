import { describe, expect, it } from 'vitest';
import { computeApiUrl, computeV8ApiUrl, normalizeApiBase } from './connection';

describe('connection base normalization', () => {
  it('strips both v0 and v8 management suffixes and trailing slashes consistently', () => {
    expect(normalizeApiBase('http://127.0.0.1:8317/v0/management/')).toBe('http://127.0.0.1:8317');
    expect(normalizeApiBase('http://127.0.0.1:8317/v8/management/')).toBe('http://127.0.0.1:8317');
    expect(normalizeApiBase('localhost:8317/v8/management')).toBe('http://localhost:8317');
    expect(normalizeApiBase('/v0/management')).toBe('');
    expect(normalizeApiBase('/v8/management/')).toBe('');
  });

  it('preserves relative bases across normalizeApiBase, computeApiUrl, and computeV8ApiUrl', () => {
    expect(normalizeApiBase('/cpamp/api/v2/cpamp/')).toBe('/cpamp/api/v2/cpamp');
    expect(normalizeApiBase('/cpamp/api/v2/cpamp/v8/management')).toBe('/cpamp/api/v2/cpamp');
    expect(computeApiUrl('/cpamp/api/v2/cpamp/v8/management')).toBe(
      '/cpamp/api/v2/cpamp/v0/management'
    );
    expect(computeV8ApiUrl('/cpamp/api/v2/cpamp/v0/management')).toBe(
      '/cpamp/api/v2/cpamp/v8/management'
    );
  });

  it('rejects protocol-relative bases so callers cannot redirect to scheme-relative hosts', () => {
    for (const candidate of [
      '//evil.example',
      '//evil.example/v0/management',
      '//evil.example/v8/management/',
      '  //evil.example/api/v2/cpamp  ',
      '///evil.example',
    ]) {
      expect(normalizeApiBase(candidate), candidate).toBe('');
      expect(computeApiUrl(candidate), candidate).toBe('');
      expect(computeV8ApiUrl(candidate), candidate).toBe('');
    }
  });
});
