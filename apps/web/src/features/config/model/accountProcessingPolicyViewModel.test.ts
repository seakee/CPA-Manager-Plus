import { describe, expect, it } from 'vitest';
import type { AccountProcessingPolicy } from '@/services/api/usageService';
import { buildAccountProcessingPolicyViewModel } from './accountProcessingPolicyViewModel';

function policy(overrides: Partial<AccountProcessingPolicy> = {}): AccountProcessingPolicy {
  return {
    source: 'startup',
    codexQuotaCooldown: {
      enabled: false,
      configured: false,
      source: 'startup',
      locked: false,
      envKey: 'USAGE_QUOTA_COOLDOWN_ENABLED',
      configFileKey: 'quotaCooldownEnabled',
    },
    authIssueQueue: {
      enabled: false,
      configured: false,
      source: 'startup',
      locked: false,
      envKey: 'USAGE_ACCOUNT_ACTIONS_ENABLED',
      configFileKey: 'accountActionsEnabled',
    },
    authIssueAutoDisable: {
      enabled: false,
      configured: false,
      source: 'startup',
      locked: false,
      envKey: 'USAGE_ACCOUNT_ACTIONS_AUTO_DISABLE',
      configFileKey: 'accountActionsAutoDisable',
      dependsOn: 'authIssueQueue',
    },
    ...overrides,
  };
}

describe('buildAccountProcessingPolicyViewModel', () => {
  it('groups quota handling and auth issue handling separately', () => {
    const groups = buildAccountProcessingPolicyViewModel(policy());

    expect(groups).toHaveLength(2);
    expect(groups[0].key).toBe('quota');
    expect(groups[0].items.map((item) => item.key)).toEqual(['codexQuotaCooldown']);
    expect(groups[1].key).toBe('authIssues');
    expect(groups[1].items.map((item) => item.key)).toEqual([
      'authIssueQueue',
      'authIssueAutoDisable',
    ]);
  });

  it('marks auto-disable as configured but blocked when its dependency is off', () => {
    const groups = buildAccountProcessingPolicyViewModel(
      policy({
        authIssueAutoDisable: {
          enabled: false,
          configured: true,
          source: 'database',
          locked: false,
          envKey: 'USAGE_ACCOUNT_ACTIONS_AUTO_DISABLE',
          configFileKey: 'accountActionsAutoDisable',
          dependsOn: 'authIssueQueue',
        },
      })
    );

    const autoDisable = groups[1].items[1];
    expect(autoDisable.configured).toBe(true);
    expect(autoDisable.enabled).toBe(false);
    expect(autoDisable.dependencyBlocked).toBe(true);
    expect(autoDisable.effectiveStateKey).toBe('accountPolicy.effective_blocked');
    expect(autoDisable.configuredStateKey).toBe('accountPolicy.configured_on');
    expect(autoDisable.toggleDisabled).toBe(false);
  });

  it('prevents enabling auto-disable before the auth issue queue is effective', () => {
    const groups = buildAccountProcessingPolicyViewModel(policy());

    const autoDisable = groups[1].items[1];
    expect(autoDisable.dependencyBlocked).toBe(true);
    expect(autoDisable.configured).toBe(false);
    expect(autoDisable.toggleDisabled).toBe(true);
  });

  it('uses locked status when a capability is controlled by environment variables', () => {
    const groups = buildAccountProcessingPolicyViewModel(
      policy({
        codexQuotaCooldown: {
          enabled: true,
          configured: true,
          source: 'env',
          locked: true,
          envKey: 'USAGE_QUOTA_COOLDOWN_ENABLED',
          configFileKey: 'quotaCooldownEnabled',
        },
      })
    );

    const quota = groups[0].items[0];
    expect(quota.locked).toBe(true);
    expect(quota.statusTone).toBe('locked');
    expect(quota.toggleDisabled).toBe(true);
    expect(quota.effectiveStateKey).toBe('accountPolicy.effective_on');
  });
});
