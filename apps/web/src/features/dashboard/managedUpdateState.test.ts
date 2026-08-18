import { describe, expect, it } from 'vitest';
import type { ManagedUpdateCheck, ManagedUpdateStatus } from '@/services/api';
import { shouldRestoreManagedUpdateStatus } from './managedUpdateState';

const check = (latestVersion: string): ManagedUpdateCheck => ({
  currentVersion: 'v1.1.0',
  latestVersion,
  updateAvailable: true,
  comparable: true,
  releaseUrl: 'https://github.com/seakee/CPA-Manager-Plus/releases/tag/v1.2.0',
  installable: true,
});

const status = (
  targetVersion: string,
  state: ManagedUpdateStatus['state']
): ManagedUpdateStatus => ({
  transactionId: 'transaction',
  installId: 'install',
  currentVersion: 'v1.0.0',
  targetVersion,
  state,
  startedAt: '2026-08-12T00:00:00Z',
  updatedAt: '2026-08-12T00:01:00Z',
});

describe('shouldRestoreManagedUpdateStatus', () => {
  it('does not let a previous success block a newer update', () => {
    expect(shouldRestoreManagedUpdateStatus(check('v1.2.0'), status('v1.1.0', 'succeeded'))).toBe(
      false
    );
  });

  it('restores an active status for the offered target', () => {
    expect(shouldRestoreManagedUpdateStatus(check('v1.2.0'), status('v1.2.0', 'staged'))).toBe(
      true
    );
  });

  it('keeps recovery-critical states visible', () => {
    expect(
      shouldRestoreManagedUpdateStatus(
        check('v1.2.0'),
        status('v1.1.0', 'manual_recovery_required')
      )
    ).toBe(true);
  });
});
