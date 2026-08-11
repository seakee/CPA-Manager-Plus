import { describe, expect, it } from 'vitest';
import { resolveProviderHomepage } from './homepage';

describe('resolveProviderHomepage', () => {
  it('strips the path and keeps scheme and host', () => {
    expect(resolveProviderHomepage('https://sharedchat.cc/v1')).toBe('https://sharedchat.cc');
    expect(resolveProviderHomepage('https://api.example.com')).toBe('https://api.example.com');
    expect(resolveProviderHomepage('https://example.com/api/chat')).toBe('https://example.com');
  });

  it('keeps ports and ip addresses', () => {
    expect(resolveProviderHomepage('http://127.0.0.1:8317')).toBe('http://127.0.0.1:8317');
    expect(resolveProviderHomepage('https://localhost:8443/v1')).toBe('https://localhost:8443');
  });

  it('assumes https when the scheme is missing', () => {
    expect(resolveProviderHomepage('sharedchat.cc/v1')).toBe('https://sharedchat.cc');
  });

  it('returns null for empty or malformed urls', () => {
    expect(resolveProviderHomepage('')).toBeNull();
    expect(resolveProviderHomepage('   ')).toBeNull();
    expect(resolveProviderHomepage('https://')).toBeNull();
    expect(resolveProviderHomepage('not a url at all')).toBeNull();
  });
});
