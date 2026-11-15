// @ts-expect-error Node-only Vitest fixture; production Web excludes Node types.
import { webcrypto } from 'node:crypto';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  buildPrefixPreviewUrls,
  generateRandomAccessPrefix,
  isCanonicalPrefix,
  isNestedPrefix,
  isReservedAccessPrefix,
  isSameOrNestedPrefix,
  resolveBrowserOrigin,
  validAccessPrefix,
  validatePrefixPair,
  validatePrefixSingle,
} from './accessPrefix';

describe('accessPrefix validation, random generation, and URL preview', () => {
  beforeEach(() => {
    vi.stubGlobal('crypto', {
      getRandomValues: <T extends Uint8Array>(bytes: T) => webcrypto.getRandomValues(bytes),
    });
    vi.stubGlobal('window', {
      location: new URL('https://manager.example:8443/.well-known/cpamp-bootstrap/ui'),
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('checks canonical paths, nesting, and reserved namespaces', () => {
    expect(isCanonicalPrefix('/console')).toBe(true);
    expect(isCanonicalPrefix('/edge/admin-1')).toBe(true);
    expect(isCanonicalPrefix('/')).toBe(false);
    expect(isCanonicalPrefix('/console/')).toBe(false);
    expect(isCanonicalPrefix('/console?a=1')).toBe(false);
    expect(isCanonicalPrefix('/a/../console')).toBe(false);

    expect(isSameOrNestedPrefix('/a', '/a')).toBe(true);
    expect(isSameOrNestedPrefix('/a', '/a/b')).toBe(true);
    expect(isSameOrNestedPrefix('/a/b', '/a')).toBe(true);
    expect(isSameOrNestedPrefix('/a', '/ab')).toBe(false);

    expect(isNestedPrefix('/a', '/a')).toBe(false);
    expect(isNestedPrefix('/a', '/a/b')).toBe(true);
    expect(isNestedPrefix('/a/b', '/a')).toBe(true);

    for (const reserved of ['/v1', '/api/v2', '/openai/v1', '/models', '/healthz', '/setup']) {
      expect(isReservedAccessPrefix(reserved)).toBe(true);
      expect(validAccessPrefix(reserved)).toBe(false);
      expect(validatePrefixSingle(reserved)).toBe('reserved_prefix_collision');
    }
    expect(validatePrefixSingle('/v1/sub')).toBe('reserved_prefix_collision');
    expect(validatePrefixSingle('/api/v2/extra')).toBe('reserved_prefix_collision');
    expect(validatePrefixSingle('/bad/')).toBe('invalid_access_prefix');
    expect(validatePrefixSingle('/admin-entry')).toBeNull();
  });

  it('generates distinct 16-char canonical CSPRNG prefixes avoiding exclusions', () => {
    const first = generateRandomAccessPrefix();
    const second = generateRandomAccessPrefix([first]);
    expect(first).toMatch(/^\/[a-z][a-z0-9]{15}$/);
    expect(second).toMatch(/^\/[a-z][a-z0-9]{15}$/);
    expect(second).not.toBe(first);
    expect(validAccessPrefix(first)).toBe(true);
    expect(validAccessPrefix(second)).toBe(true);
  });

  it('validates Managed and External prefix pairs strictly', () => {
    expect(
      validatePrefixPair({
        controlBasePath: '/ctl-entry',
        inferenceBasePath: '/inf-entry',
        sharedEntry: false,
        requireInference: true,
      })
    ).toEqual({
      controlError: null,
      inferenceError: null,
      pairError: null,
      valid: true,
    });

    expect(
      validatePrefixPair({
        controlBasePath: '/ctl-entry',
        inferenceBasePath: '',
        sharedEntry: false,
        requireInference: true,
      })
    ).toMatchObject({ valid: false, inferenceError: 'inference_prefix_required' });

    expect(
      validatePrefixPair({
        controlBasePath: '/same-entry',
        inferenceBasePath: '/same-entry',
        sharedEntry: false,
        requireInference: true,
      })
    ).toMatchObject({ valid: false, pairError: 'shared_prefix_confirmation_required' });

    expect(
      validatePrefixPair({
        controlBasePath: '/same-entry',
        inferenceBasePath: '/same-entry',
        sharedEntry: true,
        requireInference: true,
      })
    ).toMatchObject({ valid: true });

    expect(
      validatePrefixPair({
        controlBasePath: '/same-entry',
        inferenceBasePath: '/different-entry',
        sharedEntry: true,
        requireInference: true,
      })
    ).toMatchObject({ valid: false, pairError: 'shared_prefix_mismatch' });

    expect(
      validatePrefixPair({
        controlBasePath: '/app',
        inferenceBasePath: '/app/infer',
        sharedEntry: false,
        requireInference: true,
      })
    ).toMatchObject({ valid: false, pairError: 'prefix_overlap_forbidden' });

    expect(
      validatePrefixPair({
        controlBasePath: '/app/ctl',
        inferenceBasePath: '/app',
        sharedEntry: false,
        requireInference: true,
      })
    ).toMatchObject({ valid: false, pairError: 'prefix_overlap_forbidden' });

    expect(
      validatePrefixPair({
        controlBasePath: '/ctl-entry',
        inferenceBasePath: '',
        sharedEntry: false,
        requireInference: false,
      })
    ).toMatchObject({ valid: true });

    expect(
      validatePrefixPair({
        controlBasePath: '/ctl-entry',
        inferenceBasePath: '/inf-entry',
        sharedEntry: false,
        requireInference: false,
      })
    ).toMatchObject({ valid: false, pairError: 'external_inference_prefix_forbidden' });

    expect(
      validatePrefixPair({
        controlBasePath: '/ctl-entry',
        inferenceBasePath: '',
        sharedEntry: true,
        requireInference: false,
      })
    ).toMatchObject({ valid: false, pairError: 'external_inference_prefix_forbidden' });
  });

  it('resolves browser origin and builds mode-appropriate preview URLs', () => {
    expect(resolveBrowserOrigin()).toBe('https://manager.example:8443');
    expect(
      buildPrefixPreviewUrls(
        'https://manager.example:8443',
        '/ctl-entry',
        '/inf-entry',
        'https://cpa.upstream.example:8317/'
      )
    ).toEqual({
      consoleUrl: 'https://manager.example:8443/ctl-entry/ui',
      managementApiUrl: 'https://manager.example:8443/ctl-entry/api/v2',
      inferenceUrl: 'https://manager.example:8443/inf-entry',
      externalUrl: 'https://cpa.upstream.example:8317',
    });
    expect(buildPrefixPreviewUrls('https://manager.example:8443', '/invalid/', '')).toEqual({
      consoleUrl: '—',
      managementApiUrl: '—',
      inferenceUrl: '—',
      externalUrl: '—',
    });
  });
});
