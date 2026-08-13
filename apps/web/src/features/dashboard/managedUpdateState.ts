import type { ManagedUpdateCheck, ManagedUpdateStatus } from '@/services/api';
import { compareVersions } from '@/utils/version';

const supersedableTerminalStates = new Set<ManagedUpdateStatus['state']>([
  'succeeded',
  'failed',
  'rolled_back',
]);

export const shouldRestoreManagedUpdateStatus = (
  check: ManagedUpdateCheck | null,
  status: ManagedUpdateStatus
): boolean =>
  !(
    check?.updateAvailable &&
    supersedableTerminalStates.has(status.state) &&
    compareVersions(check.latestVersion, status.targetVersion) === 1
  );
