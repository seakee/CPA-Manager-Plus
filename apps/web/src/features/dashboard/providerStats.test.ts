import { describe, expect, it } from 'vitest';
import { resolveProviderCount } from './providerStats';

describe('resolveProviderCount', () => {
  it('returns the provider list size when the request succeeds', () => {
    expect(resolveProviderCount({ status: 'fulfilled', value: ['a', 'b'] })).toBe(2);
  });

  it('treats an explicitly unsupported optional provider endpoint as empty', () => {
    expect(
      resolveProviderCount(
        { status: 'rejected', reason: Object.assign(new Error('not found'), { status: 404 }) },
        { unsupportedAsZero: true }
      )
    ).toBe(0);
  });

  it('keeps other provider request failures unknown', () => {
    expect(
      resolveProviderCount(
        { status: 'rejected', reason: Object.assign(new Error('unavailable'), { status: 503 }) },
        { unsupportedAsZero: true }
      )
    ).toBeNull();
  });
});
