import type { AuthFileModelItem } from '@/features/authFiles/constants';
import {
  getProviderRecordValues,
  normalizeExcludedModels,
  parseExcludedModelsText,
} from '@/features/authFiles/constants';

export type AccountModelExclusionPriority = 'merge' | 'override' | 'unknown';

// Official CPA releases merge credential and global OAuth exclusions from v6.10.8
// through v8.0.32; v8.0.33+ gives nonempty credential exclusions priority over global
// exclusions, and an empty credential list inherits them. Custom builds stay unknown.
const CPA_RELEASE_VERSION_PATTERN = /^v?(\d+)\.(\d+)\.(\d+)$/i;
const CPA_GIT_DESCRIBE_VERSION_PATTERN = /^v?(\d+)\.(\d+)\.(\d+)-(\d+)-g([0-9a-f]+)(?:-dirty)?$/i;
// The CPAMP demo reports `<release>-demo` and emulates that release.
const CPAMP_DEMO_VERSION_PATTERN = /^v?(\d+)\.(\d+)\.(\d+)-demo$/i;
const FIRST_VERIFIED_MERGE_VERSION = [6, 10, 8] as const;
const LAST_MERGE_VERSION = [8, 0, 32] as const;

const compareVersion = (current: readonly number[], baseline: readonly number[]) => {
  for (let index = 0; index < baseline.length; index += 1) {
    const difference = current[index] - baseline[index];
    if (difference !== 0) return difference;
  }
  return 0;
};

export const getAccountModelExclusionPriority = (
  serverVersion?: string | null
): AccountModelExclusionPriority => {
  const normalizedVersion = serverVersion?.trim() ?? '';
  const describeMatch = normalizedVersion.match(CPA_GIT_DESCRIBE_VERSION_PATTERN);
  const match =
    describeMatch ??
    normalizedVersion.match(CPA_RELEASE_VERSION_PATTERN) ??
    normalizedVersion.match(CPAMP_DEMO_VERSION_PATTERN);
  if (!match) return 'unknown';
  const version = match.slice(1, 4).map((segment) => Number.parseInt(segment, 10));
  if (compareVersion(version, FIRST_VERIFIED_MERGE_VERSION) < 0) return 'unknown';
  const comparison = compareVersion(version, LAST_MERGE_VERSION);
  if (comparison > 0) return 'override';
  if (comparison < 0) return 'merge';
  // Commits after the v8.0.32 tag may or may not include the precedence change.
  return describeMatch ? 'unknown' : 'merge';
};

export type AccountModelRuleScope =
  | 'available'
  | 'unknown'
  | 'credential'
  | 'shared'
  | 'global'
  | 'both'
  | 'shared-global';

export type AccountModelRuleRow = AuthFileModelItem & {
  runtimeAvailable: boolean;
  scope: AccountModelRuleScope;
  credentialPatterns: string[];
  globalPatterns: string[];
  hasCredentialExactRule: boolean;
  hasCredentialWildcardRule: boolean;
  ruleModelId: string;
  ruleModelIdResolved: boolean;
  equivalentRuntimeModelIds: string[];
};

export type AccountModelRuleProjection = {
  rows: AccountModelRuleRow[];
  credentialRules: string[];
  globalRules: string[];
  advancedCredentialRules: string[];
  advancedGlobalRules: string[];
};

export type AccountModelRuleDiff = {
  added: string[];
  removed: string[];
  unchanged: string[];
};

type BuildAccountModelRuleProjectionOptions = {
  provider: string;
  runtimeModels: AuthFileModelItem[];
  modelDefinitions: AuthFileModelItem[];
  credentialRules: string[];
  globalRules: Record<string, string[]>;
  globalRulesKnown?: boolean;
  credentialRulesShared?: boolean;
  credentialPrefix?: string;
  exclusionPriority?: AccountModelExclusionPriority;
};

const normalizeModelId = (value: string): string => value.trim().toLowerCase();

const buildPatternMatcher = (pattern: string): RegExp => {
  const regexSafePattern = pattern
    .split('*')
    .map((segment) => segment.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'))
    .join('.*');
  return new RegExp(`^${regexSafePattern}$`, 'i');
};

export const matchesAccountModelRule = (modelId: string, pattern: string): boolean => {
  const normalizedModelId = normalizeModelId(modelId);
  const normalizedPattern = normalizeModelId(pattern);
  if (!normalizedModelId || !normalizedPattern) return false;
  if (!normalizedPattern.includes('*')) return normalizedModelId === normalizedPattern;
  return buildPatternMatcher(normalizedPattern).test(normalizedModelId);
};

const findMatchingRules = (modelId: string, rules: string[]): string[] =>
  rules.filter((rule) => matchesAccountModelRule(modelId, rule));

const mergeModelItem = (
  existing: AuthFileModelItem | undefined,
  incoming: AuthFileModelItem
): AuthFileModelItem => ({
  ...incoming,
  ...existing,
  id: existing?.id || incoming.id,
  display_name: existing?.display_name || incoming.display_name,
  type: existing?.type || incoming.type,
  owned_by: existing?.owned_by || incoming.owned_by,
});

const getGlobalRulesForProvider = (
  provider: string,
  globalRules: Record<string, string[]>
): string[] => normalizeExcludedModels(getProviderRecordValues(globalRules, provider).flat());

export type ResolvedAccountModelRuleIdentity = {
  ruleModelId: string;
  ruleModelIdResolved: boolean;
};

export const resolveAccountModelRuleIdentity = ({
  modelId,
  credentialPrefix,
  modelDefinitions,
}: {
  modelId: string;
  credentialPrefix?: string;
  modelDefinitions: AuthFileModelItem[];
}): ResolvedAccountModelRuleIdentity => {
  const trimmedId = modelId.trim();
  const normalizedId = normalizeModelId(trimmedId);
  if (!normalizedId) {
    return { ruleModelId: trimmedId, ruleModelIdResolved: false };
  }

  const definitionsMap = new Map<string, AuthFileModelItem>();
  for (const def of modelDefinitions) {
    const key = normalizeModelId(def.id);
    if (key && !definitionsMap.has(key)) {
      definitionsMap.set(key, def);
    }
  }

  // Priority 1: exact definition match
  const exactDef = definitionsMap.get(normalizedId);
  if (exactDef) {
    return {
      ruleModelId: exactDef.id,
      ruleModelIdResolved: true,
    };
  }

  const cleanPrefix = (credentialPrefix ?? '').trim().replace(/^\/+|\/+$/g, '');
  const prefixSegment = `${cleanPrefix.toLowerCase()}/`;

  // Priority 2: verified prefix removal
  if (cleanPrefix.length > 0 && normalizedId.startsWith(prefixSegment)) {
    const candidateId = trimmedId.slice(cleanPrefix.length + 1);
    const normalizedCandidate = normalizeModelId(candidateId);
    const candidateDef = definitionsMap.get(normalizedCandidate);
    if (candidateDef) {
      return {
        ruleModelId: candidateDef.id,
        ruleModelIdResolved: true,
      };
    }
    // Fail closed: cannot verify prefixed candidate
    return {
      ruleModelId: trimmedId,
      ruleModelIdResolved: false,
    };
  }

  // Priority 3: runtime does not carry the prefix
  return {
    ruleModelId: trimmedId,
    ruleModelIdResolved: true,
  };
};

export const buildAccountModelRuleProjection = ({
  provider,
  runtimeModels,
  modelDefinitions,
  credentialRules,
  globalRules,
  globalRulesKnown = true,
  credentialRulesShared = false,
  credentialPrefix,
  exclusionPriority = 'merge',
}: BuildAccountModelRuleProjectionOptions): AccountModelRuleProjection => {
  const normalizedCredentialRules = normalizeExcludedModels(credentialRules);
  const normalizedGlobalRules = globalRulesKnown
    ? getGlobalRulesForProvider(provider, globalRules)
    : [];
  const credentialOverridesGlobal =
    exclusionPriority === 'override' && normalizedCredentialRules.length > 0;
  const effectiveGlobalRules = credentialOverridesGlobal ? [] : normalizedGlobalRules;
  const runtimeIds = new Set(
    runtimeModels.map((model) => normalizeModelId(model.id)).filter(Boolean)
  );
  const modelById = new Map<string, AuthFileModelItem>();
  const ruleIdentitiesByIdKey = new Map<string, ResolvedAccountModelRuleIdentity>();
  const orderedIds: string[] = [];

  for (const model of runtimeModels) {
    const modelId = model.id.trim();
    const idKey = normalizeModelId(modelId);
    if (!idKey) continue;
    if (!modelById.has(idKey)) {
      orderedIds.push(idKey);
      const identity = resolveAccountModelRuleIdentity({
        modelId,
        credentialPrefix,
        modelDefinitions,
      });
      ruleIdentitiesByIdKey.set(idKey, identity);
    }
    modelById.set(idKey, mergeModelItem(modelById.get(idKey), { ...model, id: modelId }));
  }

  const resolvedRuntimeRuleModelIds = new Set<string>();
  for (const idKey of orderedIds) {
    const identity = ruleIdentitiesByIdKey.get(idKey);
    if (identity?.ruleModelIdResolved) {
      resolvedRuntimeRuleModelIds.add(normalizeModelId(identity.ruleModelId));
    }
  }

  for (const def of modelDefinitions) {
    const defId = def.id.trim();
    const defIdKey = normalizeModelId(defId);
    if (!defIdKey) continue;

    if (modelById.has(defIdKey)) {
      modelById.set(defIdKey, mergeModelItem(modelById.get(defIdKey), { ...def, id: defId }));
      continue;
    }

    if (resolvedRuntimeRuleModelIds.has(defIdKey)) {
      for (const idKey of orderedIds) {
        const identity = ruleIdentitiesByIdKey.get(idKey);
        if (identity?.ruleModelIdResolved && normalizeModelId(identity.ruleModelId) === defIdKey) {
          modelById.set(idKey, mergeModelItem(modelById.get(idKey), def));
        }
      }
      continue;
    }

    if (
      findMatchingRules(defId, normalizedCredentialRules).length === 0 &&
      findMatchingRules(defId, effectiveGlobalRules).length === 0
    ) {
      continue;
    }

    orderedIds.push(defIdKey);
    modelById.set(defIdKey, { ...def, id: defId });
    ruleIdentitiesByIdKey.set(defIdKey, {
      ruleModelId: defId,
      ruleModelIdResolved: true,
    });
  }

  const equivalentRuntimeIdsByRuleId = new Map<string, string[]>();
  for (const idKey of orderedIds) {
    if (runtimeIds.has(idKey)) {
      const identity = ruleIdentitiesByIdKey.get(idKey);
      if (identity?.ruleModelIdResolved) {
        const key = normalizeModelId(identity.ruleModelId);
        const list = equivalentRuntimeIdsByRuleId.get(key) ?? [];
        const modelItem = modelById.get(idKey);
        const actualId = modelItem?.id ?? idKey;
        if (!list.includes(actualId)) {
          list.push(actualId);
        }
        equivalentRuntimeIdsByRuleId.set(key, list);
      }
    }
  }

  const rows = orderedIds.map((idKey): AccountModelRuleRow => {
    const model = modelById.get(idKey) ?? { id: idKey };
    const identity = ruleIdentitiesByIdKey.get(idKey) ?? {
      ruleModelId: model.id,
      ruleModelIdResolved: true,
    };
    const isRuntimeAvailable = runtimeIds.has(idKey);

    const equivalentRuntimeModelIds = identity.ruleModelIdResolved
      ? (equivalentRuntimeIdsByRuleId.get(normalizeModelId(identity.ruleModelId)) ?? [])
          .slice()
          .sort((a, b) => a.localeCompare(b))
      : [];

    if (!identity.ruleModelIdResolved) {
      return {
        ...model,
        runtimeAvailable: isRuntimeAvailable,
        scope: 'unknown',
        credentialPatterns: [],
        globalPatterns: [],
        hasCredentialExactRule: false,
        hasCredentialWildcardRule: false,
        ruleModelId: identity.ruleModelId,
        ruleModelIdResolved: false,
        equivalentRuntimeModelIds: [],
      };
    }

    const credentialPatterns = findMatchingRules(identity.ruleModelId, normalizedCredentialRules);
    // Global patterns overridden by a nonempty credential list are not shown as effective.
    const globalPatterns = findMatchingRules(identity.ruleModelId, effectiveGlobalRules);
    const credentialExcluded = credentialPatterns.length > 0;
    const globalApplies = globalPatterns.length > 0;
    const globalEffectUnknown =
      exclusionPriority === 'unknown' && normalizedCredentialRules.length > 0 && globalApplies;
    const scope: AccountModelRuleScope = credentialExcluded
      ? credentialRulesShared
        ? globalApplies && !globalEffectUnknown
          ? 'shared-global'
          : 'shared'
        : globalApplies && !globalEffectUnknown
          ? 'both'
          : 'credential'
      : !globalRulesKnown || globalEffectUnknown
        ? 'unknown'
        : globalApplies
          ? 'global'
          : 'available';

    const normalizedRuleId = normalizeModelId(identity.ruleModelId);

    return {
      ...model,
      runtimeAvailable: isRuntimeAvailable,
      scope,
      credentialPatterns,
      globalPatterns,
      hasCredentialExactRule: credentialPatterns.some(
        (pattern) => !pattern.includes('*') && normalizeModelId(pattern) === normalizedRuleId
      ),
      hasCredentialWildcardRule: credentialPatterns.some((pattern) => pattern.includes('*')),
      ruleModelId: identity.ruleModelId,
      ruleModelIdResolved: true,
      equivalentRuntimeModelIds,
    };
  });

  const knownRuleModelIds = new Set(
    rows
      .filter((row) => row.ruleModelIdResolved)
      .map((row) => normalizeModelId(row.ruleModelId))
  );
  const isAdvancedRule = (rule: string) =>
    rule.includes('*') || !knownRuleModelIds.has(normalizeModelId(rule));

  return {
    rows,
    credentialRules: normalizedCredentialRules,
    globalRules: normalizedGlobalRules,
    advancedCredentialRules: normalizedCredentialRules.filter(isAdvancedRule),
    advancedGlobalRules: effectiveGlobalRules.filter(isAdvancedRule),
  };
};

export const setAccountModelExactRule = (
  rules: string[],
  modelId: string,
  excluded: boolean,
  equivalentExactModelIds: string[] = []
): string[] => {
  const normalizedRules = normalizeExcludedModels(rules);
  const normalizedModelId = normalizeModelId(modelId);
  if (!normalizedModelId) return normalizedRules;

  const idsToRemove = new Set([
    normalizedModelId,
    ...equivalentExactModelIds.map(normalizeModelId).filter(Boolean),
  ]);

  const next = new Set<string>();
  for (const rule of normalizedRules) {
    if (rule.includes('*') || !idsToRemove.has(normalizeModelId(rule))) {
      next.add(rule);
    }
  }

  if (excluded) {
    next.add(normalizedModelId);
  }

  return Array.from(next).sort((left, right) => left.localeCompare(right));
};

// Apply one exact-model change without silently dropping inherited global exclusions.
// An empty credential list cannot override a nonempty global list in official CPA.
export const setEffectiveAccountModelExactRule = ({
  credentialRules,
  globalRules,
  exclusionPriority,
  modelId,
  excluded,
  equivalentExactModelIds = [],
}: {
  credentialRules: string[];
  globalRules: string[];
  exclusionPriority: AccountModelExclusionPriority;
  modelId: string;
  excluded: boolean;
  equivalentExactModelIds?: string[];
}): string[] | null => {
  const existing = normalizeExcludedModels(credentialRules);
  const base =
    exclusionPriority === 'override' && existing.length === 0
      ? normalizeExcludedModels(globalRules)
      : existing;
  const next = setAccountModelExactRule(base, modelId, excluded, equivalentExactModelIds);

  // An empty override would immediately inherit the global rule again.
  if (
    exclusionPriority === 'override' &&
    !excluded &&
    next.length === 0 &&
    normalizeExcludedModels(globalRules).some((rule) => matchesAccountModelRule(modelId, rule))
  ) {
    return null;
  }
  return next;
};

export const buildAccountModelRuleDiff = (
  originalRulesText: string,
  nextRulesText: string
): AccountModelRuleDiff => {
  const originalRules = parseExcludedModelsText(originalRulesText);
  const nextRules = parseExcludedModelsText(nextRulesText);
  const original = new Set(originalRules);
  const next = new Set(nextRules);
  return {
    added: nextRules.filter((rule) => !original.has(rule)),
    removed: originalRules.filter((rule) => !next.has(rule)),
    unchanged: nextRules.filter((rule) => original.has(rule)),
  };
};
