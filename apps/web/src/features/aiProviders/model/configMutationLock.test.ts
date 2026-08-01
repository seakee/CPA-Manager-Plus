import { describe, expect, it } from 'vitest';
import { createConfigMutationLock } from './configMutationLock';

describe('createConfigMutationLock', () => {
  it('rejects overlapping mutations until the active mutation releases the lock', () => {
    const lock = createConfigMutationLock();

    expect(lock.tryAcquire()).toBe(true);
    expect(lock.isLocked()).toBe(true);
    expect(lock.tryAcquire()).toBe(false);

    lock.release();

    expect(lock.isLocked()).toBe(false);
    expect(lock.tryAcquire()).toBe(true);
  });
});
