import type { AccountPolicyCapability, AccountProcessingPolicy } from '@/services/api/usageService';

export type AccountPolicyCapabilityKey =
  | 'codexQuotaCooldown'
  | 'authIssueQueue'
  | 'authIssueAutoDisable';

export type AccountPolicyGroupKey = 'quota' | 'authIssues';

export interface AccountPolicyViewOptions {
  loading?: boolean;
  savingKey?: AccountPolicyCapabilityKey | null;
}

export interface AccountPolicyViewItem {
  key: AccountPolicyCapabilityKey;
  capability: AccountPolicyCapability;
  configured: boolean;
  enabled: boolean;
  locked: boolean;
  source: string;
  dependencyKey?: AccountPolicyCapabilityKey;
  dependencyBlocked: boolean;
  toggleDisabled: boolean;
  nested: boolean;
  titleKey: string;
  descriptionKey: string;
  behaviorKey: string;
  summaryKey: string;
  toggleLabelKey: string;
  effectiveStateKey: string;
  configuredStateKey: string;
  statusTone: 'on' | 'off' | 'blocked' | 'locked';
}

export interface AccountPolicyViewGroup {
  key: AccountPolicyGroupKey;
  titleKey: string;
  descriptionKey: string;
  items: AccountPolicyViewItem[];
}

const capabilityKeys: AccountPolicyCapabilityKey[] = [
  'codexQuotaCooldown',
  'authIssueQueue',
  'authIssueAutoDisable',
];

const capabilityMetadata: Record<
  AccountPolicyCapabilityKey,
  Pick<
    AccountPolicyViewItem,
    'titleKey' | 'descriptionKey' | 'behaviorKey' | 'summaryKey' | 'toggleLabelKey' | 'nested'
  >
> = {
  codexQuotaCooldown: {
    titleKey: 'accountPolicy.codexQuotaCooldown_title',
    descriptionKey: 'accountPolicy.codexQuotaCooldown_description',
    behaviorKey: 'accountPolicy.codexQuotaCooldown_behavior',
    summaryKey: 'accountPolicy.codexQuotaCooldown_summary',
    toggleLabelKey: 'accountPolicy.codexQuotaCooldown_toggle',
    nested: false,
  },
  authIssueQueue: {
    titleKey: 'accountPolicy.authIssueQueue_title',
    descriptionKey: 'accountPolicy.authIssueQueue_description',
    behaviorKey: 'accountPolicy.authIssueQueue_behavior',
    summaryKey: 'accountPolicy.authIssueQueue_summary',
    toggleLabelKey: 'accountPolicy.authIssueQueue_toggle',
    nested: false,
  },
  authIssueAutoDisable: {
    titleKey: 'accountPolicy.authIssueAutoDisable_title',
    descriptionKey: 'accountPolicy.authIssueAutoDisable_description',
    behaviorKey: 'accountPolicy.authIssueAutoDisable_behavior',
    summaryKey: 'accountPolicy.authIssueAutoDisable_summary',
    toggleLabelKey: 'accountPolicy.authIssueAutoDisable_toggle',
    nested: true,
  },
};

const groupDefinitions: Array<{
  key: AccountPolicyGroupKey;
  titleKey: string;
  descriptionKey: string;
  itemKeys: AccountPolicyCapabilityKey[];
}> = [
  {
    key: 'quota',
    titleKey: 'accountPolicy.group_quota_title',
    descriptionKey: 'accountPolicy.group_quota_description',
    itemKeys: ['codexQuotaCooldown'],
  },
  {
    key: 'authIssues',
    titleKey: 'accountPolicy.group_auth_issues_title',
    descriptionKey: 'accountPolicy.group_auth_issues_description',
    itemKeys: ['authIssueQueue', 'authIssueAutoDisable'],
  },
];

export function buildAccountProcessingPolicyViewModel(
  status: AccountProcessingPolicy,
  options: AccountPolicyViewOptions = {}
): AccountPolicyViewGroup[] {
  const itemByKey = Object.fromEntries(
    capabilityKeys.map((key) => [key, buildItem(status, key, options)])
  ) as Record<AccountPolicyCapabilityKey, AccountPolicyViewItem>;

  return groupDefinitions.map((group) => ({
    key: group.key,
    titleKey: group.titleKey,
    descriptionKey: group.descriptionKey,
    items: group.itemKeys.map((key) => itemByKey[key]),
  }));
}

function buildItem(
  status: AccountProcessingPolicy,
  key: AccountPolicyCapabilityKey,
  options: AccountPolicyViewOptions
): AccountPolicyViewItem {
  const capability = status[key];
  const configured = capability.configured ?? capability.enabled;
  const enabled = Boolean(capability.enabled);
  const locked = Boolean(capability.locked);
  const dependencyKey = parseCapabilityKey(capability.dependsOn);
  const dependencyBlocked = Boolean(
    dependencyKey && status[dependencyKey] && !status[dependencyKey].enabled
  );
  const toggleDisabled = Boolean(
    options.loading ||
    (options.savingKey !== null && options.savingKey !== undefined) ||
    locked ||
    (dependencyBlocked && !configured)
  );
  const statusTone = locked ? 'locked' : dependencyBlocked ? 'blocked' : enabled ? 'on' : 'off';

  return {
    key,
    capability,
    configured: Boolean(configured),
    enabled,
    locked,
    source: capability.source || 'startup',
    dependencyKey,
    dependencyBlocked,
    toggleDisabled,
    effectiveStateKey: dependencyBlocked
      ? 'accountPolicy.effective_blocked'
      : enabled
        ? 'accountPolicy.effective_on'
        : 'accountPolicy.effective_off',
    configuredStateKey: configured ? 'accountPolicy.configured_on' : 'accountPolicy.configured_off',
    statusTone,
    ...capabilityMetadata[key],
  };
}

function parseCapabilityKey(value?: string): AccountPolicyCapabilityKey | undefined {
  if (!value) return undefined;
  return capabilityKeys.includes(value as AccountPolicyCapabilityKey)
    ? (value as AccountPolicyCapabilityKey)
    : undefined;
}
