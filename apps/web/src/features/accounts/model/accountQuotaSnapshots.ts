import type { CodexQuotaState, QuotaModelScope } from '@/types';
import type {
  AccountQuotaSnapshotCycle,
  AccountQuotaSnapshotObservationInput,
  AccountQuotaSnapshotQueryAccount,
  AccountQuotaSnapshotTarget,
  AccountQuotaSnapshotWindow,
  AccountQuotaSnapshotWindowInput,
  AccountQuotaSnapshotWriteEntry,
} from '@/services/api/usageService';
import { buildAccountHistoryTargetEntries } from './accountHistoryRows';
import type { AccountRow } from './accountRows';
import type {
  AccountQuotaBoundaryAccuracy,
  AccountQuotaCycleDefinition,
  AccountQuotaWindowDefinition,
} from './accountQuotaWindowDefinitions';
import type {
  AccountQuotaDisplayWindow,
  AccountQuotaWindowKind,
  AccountQuotaWindowSource,
} from './accountQuotaDisplayWindows';
import { remainingPercentFromUsed } from './accountQuotaDisplayWindows';
import {
  CODEX_MAIN_QUOTA_SCOPE_KEY,
  canonicalizeCodexProviderWindowIdForScope,
  isCodexKnownScopedProviderWindowId,
  isCodexLegacyAllScopeReplacement,
  isCodexMainProviderWindowId,
  inferCodexQuotaScopeFromProviderWindowId,
  shouldClearInheritedCodexQuotaProgress,
} from '@/utils/quota/codexQuota';

const INCOMPLETE_MODEL_SCOPE_KIND = 'feature';
const INCOMPLETE_MODEL_SCOPE_KEY = 'scope_unknown';

const isIncompleteModelScopeSnapshot = (scope: {
  model_scope_kind: string;
  model_scope_key?: string;
  model_ids?: string[];
}): boolean =>
  scope.model_scope_kind.trim().toLowerCase() === INCOMPLETE_MODEL_SCOPE_KIND &&
  scope.model_scope_key?.trim().toLowerCase() === INCOMPLETE_MODEL_SCOPE_KEY &&
  (scope.model_ids?.length ?? 0) === 0;

const toSnapshotTarget = (
  row: AccountRow,
  target: ReturnType<typeof buildAccountHistoryTargetEntries>[number]['target']
): AccountQuotaSnapshotTarget => ({
  account_snapshot: target.account_snapshot,
  auth_label_snapshot: target.auth_label_snapshot,
  auth_file_snapshot: target.auth_file_snapshot,
  auth_provider_snapshot: target.auth_provider_snapshot ?? row.provider,
  auth_account_id_snapshot: target.auth_account_id_snapshot,
  auth_project_id_snapshot: target.auth_project_id_snapshot,
  auth_index: target.auth_index,
  source: target.source,
});

const toResetCredits = (quota: CodexQuotaState | undefined) =>
  (quota?.rateLimitResetCredits ?? [])
    .map((credit) => ({ id: credit.id.trim(), expires_at_ms: Date.parse(credit.expiresAt) }))
    .filter(
      (credit) => credit.id && Number.isFinite(credit.expires_at_ms) && credit.expires_at_ms > 0
    );

const snapshotFieldObservedAt = (snapshot: AccountQuotaSnapshotWindow, field: string) => {
  const fieldObservedAt = snapshot.field_sources?.[field]?.observed_at_ms;
  if (
    typeof fieldObservedAt === 'number' &&
    Number.isFinite(fieldObservedAt) &&
    fieldObservedAt > 0
  ) {
    return fieldObservedAt;
  }
  return typeof snapshot.observed_at_ms === 'number' &&
    Number.isFinite(snapshot.observed_at_ms) &&
    snapshot.observed_at_ms > 0
    ? snapshot.observed_at_ms
    : 0;
};

const snapshotFieldTieBreakKey = (snapshot: AccountQuotaSnapshotWindow, field: string) =>
  [
    snapshot.field_sources?.[field]?.source ?? snapshot.source,
    snapshot.source_observation_id ?? '',
    snapshot.provider_window_id,
    snapshot.window_kind,
  ].join('\u0000');

const isFiniteQuotaProgress = (value: number | null | undefined): value is number =>
  typeof value === 'number' && Number.isFinite(value);

const isValidQuotaProgressObservedAtMs = (value: number | null | undefined): value is number =>
  typeof value === 'number' && Number.isFinite(value) && value > 0;

const resolveSnapshotQuotaProgressObservedAtMs = (
  snapshot: AccountQuotaSnapshotWindow
): number | null => {
  if (!isFiniteQuotaProgress(snapshot.used_percent)) return null;
  const quotaObservedAtMs = snapshot.field_sources?.quota?.observed_at_ms;
  if (isValidQuotaProgressObservedAtMs(quotaObservedAtMs)) {
    return quotaObservedAtMs;
  }
  return isValidQuotaProgressObservedAtMs(snapshot.observed_at_ms) ? snapshot.observed_at_ms : null;
};

const resolveSnapshotQuotaEvidenceObservedAtMs = (
  snapshot: AccountQuotaSnapshotWindow
): number | null => {
  const fieldObservedAtMs = snapshot.field_sources?.quota?.observed_at_ms;
  if (isValidQuotaProgressObservedAtMs(fieldObservedAtMs)) return fieldObservedAtMs;
  return isValidQuotaProgressObservedAtMs(snapshot.observed_at_ms) ? snapshot.observed_at_ms : null;
};

type SnapshotQuotaEvidenceKind = 'used-bearing' | 'remaining-only' | 'metadata-only';

const snapshotQuotaEvidenceKind = (
  snapshot: AccountQuotaSnapshotWindow
): SnapshotQuotaEvidenceKind => {
  if (isFiniteQuotaProgress(snapshot.used_percent)) return 'used-bearing';
  if (isFiniteQuotaProgress(snapshot.remaining_percent)) return 'remaining-only';
  return 'metadata-only';
};

const resolveDefinitionQuotaEvidenceObservedAtMs = (
  definition: AccountQuotaWindowDefinition
): number | null => {
  if (isFiniteQuotaProgress(definition.usedPercent)) {
    if (definition.quotaProgressObservedAtMs !== undefined) {
      return isValidQuotaProgressObservedAtMs(definition.quotaProgressObservedAtMs)
        ? definition.quotaProgressObservedAtMs
        : null;
    }
  }
  if (
    isFiniteQuotaProgress(definition.usedPercent) ||
    isFiniteQuotaProgress(definition.remainingPercent)
  ) {
    return isValidQuotaProgressObservedAtMs(definition.observedAtMs)
      ? definition.observedAtMs
      : null;
  }
  return null;
};

type CodexSnapshotCycleRelationship = 'same' | 'different' | 'unknown';

const resolveCodexSnapshotCycleRelationship = (
  definition: AccountQuotaWindowDefinition,
  snapshot: AccountQuotaSnapshotWindow
): CodexSnapshotCycleRelationship => {
  if (definition.currentCycle && snapshot.current_cycle) {
    return definition.currentCycle.id === snapshot.current_cycle.id &&
      definition.currentCycle.activationId === snapshot.current_cycle.activation_id
      ? 'same'
      : 'different';
  }

  return shouldClearInheritedCodexQuotaProgress(
    {
      providerWindowId: definition.providerWindowId,
      endMs: definition.currentCycle?.scheduledEndMs ?? definition.cycleEndMs,
      durationSeconds: definition.currentCycle?.durationSeconds ?? definition.durationSeconds,
      boundaryAccuracy: definition.currentCycle?.boundaryAccuracy ?? definition.boundaryAccuracy,
    },
    {
      providerWindowId: snapshot.provider_window_id,
      endMs: snapshot.current_cycle?.scheduled_end_ms ?? snapshot.cycle_end_ms,
      durationSeconds: snapshot.current_cycle?.duration_seconds ?? snapshot.duration_seconds,
      boundaryAccuracy: snapshot.current_cycle?.boundary_accuracy ?? snapshot.boundary_accuracy,
    }
  )
    ? 'different'
    : 'unknown';
};

const compareSnapshotFieldFreshness =
  (field: string) => (left: AccountQuotaSnapshotWindow, right: AccountQuotaSnapshotWindow) => {
    const observedAtDelta =
      snapshotFieldObservedAt(right, field) - snapshotFieldObservedAt(left, field);
    if (Number.isFinite(observedAtDelta) && observedAtDelta !== 0) return observedAtDelta;
    const leftKey = snapshotFieldTieBreakKey(left, field);
    const rightKey = snapshotFieldTieBreakKey(right, field);
    if (leftKey === rightKey) return 0;
    return leftKey < rightKey ? -1 : 1;
  };

export const mergeCodexResetCreditsFromQuotaSnapshots = (
  quota: CodexQuotaState | undefined,
  snapshots: AccountQuotaSnapshotWindow[]
): CodexQuotaState | undefined => {
  const localObservedAt = quota?.fetchedAtMs ?? quota?.observedAtMs ?? 0;
  const usableSnapshots = snapshots.filter(
    (snapshot) =>
      snapshot.stale !== true &&
      snapshot.availability !== 'pending_absent' &&
      snapshot.availability !== 'inactive'
  );
  const countSnapshot = usableSnapshots
    .filter(
      (snapshot) =>
        typeof snapshot.reset_credits_available === 'number' &&
        Number.isFinite(snapshot.reset_credits_available) &&
        snapshot.reset_credits_available >= 0
    )
    .sort(compareSnapshotFieldFreshness('reset_credits_available'))[0];
  const creditsSnapshot = usableSnapshots
    .filter((snapshot) => snapshot.reset_credits !== undefined)
    .sort(compareSnapshotFieldFreshness('reset_credits'))[0];
  const countObservedAt = countSnapshot
    ? snapshotFieldObservedAt(countSnapshot, 'reset_credits_available')
    : 0;
  const creditsObservedAt = creditsSnapshot
    ? snapshotFieldObservedAt(creditsSnapshot, 'reset_credits')
    : 0;
  const useSnapshotCount =
    countSnapshot !== undefined &&
    (quota?.rateLimitResetCreditsAvailableCount === undefined ||
      countObservedAt >= localObservedAt);
  const useSnapshotCredits =
    creditsSnapshot !== undefined &&
    (quota?.rateLimitResetCredits === undefined || creditsObservedAt >= localObservedAt);
  if (!useSnapshotCount && !useSnapshotCredits) return quota;

  const clearCreditsFromNewZeroCount =
    useSnapshotCount &&
    countSnapshot?.reset_credits_available === 0 &&
    countObservedAt >= creditsObservedAt;

  const base: CodexQuotaState = quota ?? { status: 'success', windows: [] };
  const next: CodexQuotaState = {
    ...base,
    rateLimitResetCreditsAvailableCount: useSnapshotCount
      ? (countSnapshot.reset_credits_available ?? null)
      : base.rateLimitResetCreditsAvailableCount,
    rateLimitResetCredits: clearCreditsFromNewZeroCount
      ? []
      : useSnapshotCredits
        ? (creditsSnapshot.reset_credits ?? []).map((credit) => ({
            id: credit.id,
            status: 'available',
            grantedAt: '',
            expiresAt: new Date(credit.expires_at_ms).toISOString(),
          }))
        : base.rateLimitResetCredits,
  };
  return next;
};

const toSnapshotWindow = (
  definition: AccountQuotaWindowDefinition,
  nowMs: number,
  codexQuota?: CodexQuotaState,
  observation?: AccountQuotaSnapshotObservationInput
): AccountQuotaSnapshotWindowInput => {
  const scopeComplete = definition.modelScope.complete !== false;
  const hasModels =
    definition.modelScope.kind !== 'models' || (definition.modelScope.models?.length ?? 0) > 0;
  // The snapshot schema predates the explicit `complete` bit. Encode every
  // incomplete scope as a feature scope so a round-trip cannot silently turn
  // `all`/`family`/`models` back into a complete account-wide scope.
  const persistedScopeKind =
    !scopeComplete && definition.modelScope.kind !== 'feature'
      ? INCOMPLETE_MODEL_SCOPE_KIND
      : definition.modelScope.kind === 'models' && !hasModels
        ? INCOMPLETE_MODEL_SCOPE_KIND
        : definition.modelScope.kind;
  const persistedScopeKey =
    !scopeComplete && definition.modelScope.kind !== 'feature'
      ? INCOMPLETE_MODEL_SCOPE_KEY
      : definition.modelScope.kind === 'models' && !hasModels
        ? INCOMPLETE_MODEL_SCOPE_KEY
        : definition.modelScope.key;
  const persistedModelIDs = scopeComplete && hasModels ? definition.modelScope.models : undefined;
  const boundaryAccuracy =
    scopeComplete && hasModels ? definition.boundaryAccuracy : ('unknown' as const);
  const windowMode = scopeComplete && hasModels ? definition.windowMode : ('unknown' as const);
  const resetCredits = definition.provider === 'codex' ? toResetCredits(codexQuota) : [];
  const snapshotObservedAtMs = observation?.observed_at_ms ?? definition.observedAtMs ?? nowMs;
  const quotaProgressBelongsToObservation =
    isFiniteQuotaProgress(definition.usedPercent) &&
    isValidQuotaProgressObservedAtMs(definition.quotaProgressObservedAtMs) &&
    definition.quotaProgressObservedAtMs === snapshotObservedAtMs;
  const remainingOnlyBelongsToObservation =
    !isFiniteQuotaProgress(definition.usedPercent) &&
    isFiniteQuotaProgress(definition.remainingPercent) &&
    isValidQuotaProgressObservedAtMs(definition.observedAtMs) &&
    definition.observedAtMs === snapshotObservedAtMs;
  return {
    provider_window_id: definition.providerWindowId,
    provider_window_aliases: definition.providerWindowAliases,
    window_kind: definition.kind,
    window_mode: windowMode,
    model_scope_kind: persistedScopeKind,
    model_scope_key: persistedScopeKey,
    model_ids: persistedModelIDs,
    source: observation?.source ?? definition.observationSource,
    source_observation_id: observation?.source_observation_id,
    observed_at_ms: snapshotObservedAtMs,
    boundary_accuracy: boundaryAccuracy,
    cycle_start_ms: definition.cycleStartMs ?? undefined,
    cycle_end_ms: definition.cycleEndMs ?? undefined,
    duration_seconds: definition.durationSeconds ?? undefined,
    ...(quotaProgressBelongsToObservation
      ? {
          used_percent: definition.usedPercent ?? undefined,
          remaining_percent: definition.remainingPercent ?? undefined,
        }
      : remainingOnlyBelongsToObservation
        ? { remaining_percent: definition.remainingPercent ?? undefined }
        : {}),
    reset_credits_available:
      definition.provider === 'codex'
        ? (codexQuota?.rateLimitResetCreditsAvailableCount ?? undefined)
        : undefined,
    reset_credits: resetCredits.length > 0 ? resetCredits : undefined,
    plan_type: definition.provider === 'codex' ? (codexQuota?.planType ?? undefined) : undefined,
    relationship_kind: definition.relationshipKind,
    container_provider_window_id: definition.containerProviderWindowId,
  };
};

const applySnapshotWindowRelationships = (
  provider: string,
  definitions: AccountQuotaWindowDefinition[],
  windows: AccountQuotaSnapshotWindowInput[]
) => {
  if (provider !== 'codex') return;

  const familyRole = (
    providerWindowId: string
  ): { family: string; role: 'five-hour' | 'weekly' | 'monthly' } | null => {
    const id = providerWindowId.trim();
    if (id === 'five-hour' || id === 'weekly' || id === 'monthly') {
      return { family: 'main', role: id };
    }
    if (id === 'code-review-five-hour') {
      return { family: 'code-review', role: 'five-hour' };
    }
    if (id === 'code-review-weekly') {
      return { family: 'code-review', role: 'weekly' };
    }
    if (id === 'code-review-monthly') {
      return { family: 'code-review', role: 'monthly' };
    }
    const match = id.match(/^(.*)-(five-hour|weekly|monthly)-(\d+)$/);
    if (!match?.[1] || !match[2] || match[3] === undefined) return null;
    return {
      family: `${match[1]}\u0000${match[3]}`,
      role: match[2] as 'five-hour' | 'weekly' | 'monthly',
    };
  };

  const scopeKey = (definition: AccountQuotaWindowDefinition) =>
    [
      definition.modelScope.kind,
      definition.modelScope.key?.trim().toLowerCase() ?? '',
      ...(definition.modelScope.models ?? [])
        .map((model) => model.trim().toLowerCase())
        .filter(Boolean)
        .sort(),
    ].join('\u0000');
  const weekly = definitions
    .map((definition, index) => ({ definition, index, scopeKey: scopeKey(definition) }))
    .filter((item) => item.definition.kind === 'weekly');
  const containersByFamily = new Map<
    string,
    { weekly?: AccountQuotaWindowDefinition; monthly?: AccountQuotaWindowDefinition }
  >();
  definitions.forEach((definition) => {
    const identity = familyRole(definition.providerWindowId);
    if (!identity || identity.role === 'five-hour') return;
    const key = `${scopeKey(definition)}\u0000${identity.family}`;
    const container = containersByFamily.get(key) ?? {};
    container[identity.role] = definition;
    containersByFamily.set(key, container);
  });

  definitions.forEach((definition, index) => {
    if (definition.kind !== 'five_hour') return;
    if (windows[index].relationship_kind && windows[index].container_provider_window_id) return;
    const identity = familyRole(definition.providerWindowId);
    if (identity?.role === 'five-hour') {
      const container = containersByFamily.get(`${scopeKey(definition)}\u0000${identity.family}`);
      const providerWindowId =
        container?.weekly?.providerWindowId ?? container?.monthly?.providerWindowId;
      if (providerWindowId) {
        windows[index].relationship_kind = 'concurrent_subwindow';
        windows[index].container_provider_window_id = providerWindowId;
        return;
      }
    }
    const matchingWeekly = weekly.filter((item) => item.scopeKey === scopeKey(definition));
    const container = matchingWeekly.length === 1 ? matchingWeekly[0] : null;
    if (!container) return;
    windows[index].relationship_kind = 'concurrent_subwindow';
    windows[index].container_provider_window_id = container.definition.providerWindowId;
  });
};

export const buildAccountQuotaSnapshotWriteEntries = (
  rows: AccountRow[],
  definitionsByRowKey: ReadonlyMap<string, AccountQuotaWindowDefinition[]>,
  options: {
    nowMs?: number;
    getCodexQuota?: (row: AccountRow) => CodexQuotaState | undefined;
    getObservation?: (row: AccountRow) => AccountQuotaSnapshotObservationInput | undefined;
  } = {}
): AccountQuotaSnapshotWriteEntry[] => {
  const targets = new Map(
    buildAccountHistoryTargetEntries(rows).map((entry) => [entry.rowKey, entry.target])
  );
  const nowMs = options.nowMs ?? Date.now();
  const observationProviderConfigured = typeof options.getObservation === 'function';
  return rows.flatMap((row) => {
    const definitions = (definitionsByRowKey.get(row.selectionKey) ?? []).filter(
      (definition) => definition.provider !== 'summary'
    );
    const target = targets.get(row.selectionKey);
    if (!target) return [];
    const observation = options.getObservation?.(row);
    if (observationProviderConfigured && !isUsableObservation(observation)) return [];
    if (definitions.length === 0 && !observation) return [];
    if (definitions.length === 0 && observation?.inventory_mode === 'partial') return [];
    const windows = definitions.map((definition) =>
      toSnapshotWindow(definition, nowMs, options.getCodexQuota?.(row), observation)
    );
    applySnapshotWindowRelationships(row.provider, definitions, windows);
    return [
      {
        row_key: row.selectionKey,
        provider: row.provider,
        account: toSnapshotTarget(row, target),
        observation,
        windows,
      },
    ];
  });
};

const isUsableObservation = (
  observation: AccountQuotaSnapshotObservationInput | undefined
): observation is AccountQuotaSnapshotObservationInput =>
  observation !== undefined &&
  typeof observation.source === 'string' &&
  observation.source.trim().length > 0 &&
  typeof observation.inventory_scope_key === 'string' &&
  observation.inventory_scope_key.trim().length > 0 &&
  typeof observation.inventory_mode === 'string' &&
  observation.inventory_mode.trim().length > 0 &&
  typeof observation.observed_at_ms === 'number' &&
  Number.isFinite(observation.observed_at_ms) &&
  observation.observed_at_ms > 0;

export const buildAccountQuotaSnapshotQueryAccounts = (
  rows: AccountRow[]
): AccountQuotaSnapshotQueryAccount[] => {
  const targets = new Map(
    buildAccountHistoryTargetEntries(rows).map((entry) => [entry.rowKey, entry.target])
  );
  return rows.flatMap((row) => {
    const target = targets.get(row.selectionKey);
    if (!target || !['codex', 'claude', 'antigravity', 'kimi', 'xai'].includes(row.provider)) {
      return [];
    }
    return [
      {
        row_key: row.selectionKey,
        provider: row.provider,
        account: toSnapshotTarget(row, target),
      },
    ];
  });
};

const normalizedSnapshotModelIDs = (modelIDs: string[] | undefined): string[] =>
  Array.from(
    new Set((modelIDs ?? []).map((model) => model.trim().toLowerCase()).filter(Boolean))
  ).sort();

const snapshotScopeParts = (window: {
  provider_window_id: string;
  model_scope_kind: string;
  model_scope_key?: string;
  model_ids?: string[];
}) => {
  if (isIncompleteModelScopeSnapshot(window)) {
    return [window.provider_window_id.trim(), 'models', ''];
  }
  const kind = window.model_scope_kind.trim().toLowerCase();
  const key = window.model_scope_key?.trim().toLowerCase() ?? '';
  const models = normalizedSnapshotModelIDs(window.model_ids);
  return [window.provider_window_id.trim(), kind, key, ...models];
};

const snapshotScopeKey = (window: {
  provider_window_id: string;
  model_scope_kind: string;
  model_scope_key?: string;
  model_ids?: string[];
}) => snapshotScopeParts(window).join('\u0000');

const snapshotDisplayKey = (snapshot: AccountQuotaSnapshotWindow): string =>
  [
    snapshot.provider_window_id,
    'scope',
    ...snapshotScopeParts(snapshot)
      .slice(1)
      .map((part) => encodeURIComponent(part || '-')),
  ].join('::');

const compareSnapshotFreshness = (
  left: AccountQuotaSnapshotWindow,
  right: AccountQuotaSnapshotWindow
): number => {
  if (left.observed_at_ms !== right.observed_at_ms) {
    return left.observed_at_ms - right.observed_at_ms;
  }
  const leftKey = [left.source, left.source_observation_id ?? '', left.provider_window_id].join(
    '\u0000'
  );
  const rightKey = [right.source, right.source_observation_id ?? '', right.provider_window_id].join(
    '\u0000'
  );
  if (leftKey === rightKey) return 0;
  return leftKey < rightKey ? -1 : 1;
};

export const mergeAccountQuotaSnapshotWindows = (
  definitions: AccountQuotaWindowDefinition[],
  snapshots: AccountQuotaSnapshotWindow[],
  options: {
    provider?: string;
    getLabel?: (snapshot: AccountQuotaSnapshotWindow) => string;
  } = {}
): AccountQuotaWindowDefinition[] => {
  const canonicalProviderWindowId = (
    providerWindowId: string,
    windowKind?: string,
    modelScope?: QuotaModelScope
  ) => {
    if (options.provider !== 'codex') return providerWindowId;
    return canonicalizeCodexProviderWindowIdForScope(providerWindowId, windowKind, modelScope);
  };
  const canonicalDefinitions = definitions.map((definition) => {
    const modelScope =
      options.provider === 'codex' &&
      definition.modelScope.kind === 'all' &&
      definition.modelScope.complete !== false &&
      isCodexKnownScopedProviderWindowId(definition.providerWindowId) &&
      !isCodexMainProviderWindowId(definition.providerWindowId)
        ? inferCodexQuotaScopeFromProviderWindowId(definition.providerWindowId)
        : definition.modelScope;
    const providerWindowId = canonicalProviderWindowId(
      definition.providerWindowId,
      definition.kind,
      modelScope
    );
    const providerWindowAliases = Array.from(
      new Set(
        (definition.providerWindowAliases ?? [])
          .map((alias) => alias.trim().toLowerCase())
          .filter((alias) => alias && alias !== providerWindowId)
      )
    ).sort();
    return providerWindowId === definition.providerWindowId &&
      providerWindowAliases.length === 0 &&
      modelScope === definition.modelScope
      ? definition
      : {
          ...definition,
          providerWindowId,
          modelScope,
          display:
            modelScope === definition.modelScope
              ? definition.display
              : { ...definition.display, modelScope },
          providerWindowAliases:
            providerWindowAliases.length > 0 ? providerWindowAliases : undefined,
        };
  });
  const canonicalSnapshots = snapshots.map((snapshot) => {
    const inferredScope =
      options.provider === 'codex' &&
      snapshot.model_scope_kind.trim().toLowerCase() === 'all' &&
      isCodexKnownScopedProviderWindowId(snapshot.provider_window_id)
        ? inferCodexQuotaScopeFromProviderWindowId(snapshot.provider_window_id)
        : options.provider === 'codex'
          ? snapshotModelScope(snapshot)
          : undefined;
    const providerWindowId = canonicalProviderWindowId(
      snapshot.provider_window_id,
      snapshot.window_kind,
      inferredScope
    );
    const normalizedSnapshot =
      options.provider === 'codex' &&
      isCodexMainProviderWindowId(providerWindowId) &&
      snapshot.model_scope_kind.trim().toLowerCase() === 'all'
        ? {
            ...snapshot,
            model_scope_kind: 'family' as const,
            model_scope_key: CODEX_MAIN_QUOTA_SCOPE_KEY,
            model_ids: undefined,
          }
        : snapshot;
    return providerWindowId === snapshot.provider_window_id && normalizedSnapshot === snapshot
      ? snapshot
      : { ...normalizedSnapshot, provider_window_id: providerWindowId };
  });
  const scopedProviderWindowIds = new Set<string>();
  const scopedProviderWindowAliases = new Set<string>();
  if (options.provider === 'codex') {
    canonicalDefinitions
      .filter((definition) =>
        isCodexLegacyAllScopeReplacement(definition.providerWindowId, definition.modelScope)
      )
      .forEach((definition) => {
        scopedProviderWindowIds.add(definition.providerWindowId);
        for (const alias of definition.providerWindowAliases ?? []) {
          scopedProviderWindowAliases.add(alias);
        }
      });
    canonicalSnapshots
      .filter((snapshot) => snapshot.model_scope_kind.trim().toLowerCase() !== 'all')
      .forEach((snapshot) => scopedProviderWindowIds.add(snapshot.provider_window_id));
  }
  const compatibleDefinitions = canonicalDefinitions.filter(
    (definition) =>
      definition.modelScope.kind !== 'all' ||
      definition.modelScope.complete === false ||
      !scopedProviderWindowIds.has(definition.providerWindowId)
  );
  const compatibleSnapshots = canonicalSnapshots.filter((snapshot) => {
    if (snapshot.model_scope_kind.trim().toLowerCase() !== 'all') return true;
    if (options.provider !== 'codex') return true;
    const isKnownScopedLegacy = isCodexKnownScopedProviderWindowId(snapshot.provider_window_id);
    return (
      !isKnownScopedLegacy &&
      !scopedProviderWindowIds.has(snapshot.provider_window_id) &&
      !scopedProviderWindowAliases.has(snapshot.provider_window_id)
    );
  });
  const snapshotsByKey = new Map<string, AccountQuotaSnapshotWindow>();
  compatibleSnapshots.forEach((snapshot) => {
    const key = snapshotScopeKey(snapshot);
    const current = snapshotsByKey.get(key);
    if (!current || compareSnapshotFreshness(current, snapshot) < 0) {
      snapshotsByKey.set(key, snapshot);
    }
  });
  const matchedSnapshotKeys = new Set<string>();
  const merged = compatibleDefinitions.map((definition) => {
    if (definition.modelScope.kind === 'all' && definition.modelScope.complete === false) {
      return definition;
    }
    const key = snapshotScopeKey({
      provider_window_id: definition.providerWindowId,
      model_scope_kind: definition.modelScope.kind,
      model_scope_key: definition.modelScope.key,
      model_ids: definition.modelScope.models,
    });
    const snapshot = snapshotsByKey.get(key);
    if (!snapshot) return definition;
    matchedSnapshotKeys.add(key);
    const snapshotMetadataIsNewer =
      definition.observedAtMs === null ||
      !Number.isFinite(definition.observedAtMs) ||
      snapshot.observed_at_ms >= definition.observedAtMs;
    const currentCycle = snapshotCycleDefinition(snapshot.current_cycle);
    const isCodex = definition.provider === 'codex' || options.provider === 'codex';
    const cycleRelationship = isCodex
      ? resolveCodexSnapshotCycleRelationship(definition, snapshot)
      : 'unknown';
    const definitionUsedPercent = isFiniteQuotaProgress(definition.usedPercent)
      ? definition.usedPercent
      : null;
    const definitionRemainingPercent = isFiniteQuotaProgress(definition.remainingPercent)
      ? definition.remainingPercent
      : definitionUsedPercent !== null
        ? remainingPercentFromUsed(definitionUsedPercent)
        : null;
    const snapshotUsedPercent = isFiniteQuotaProgress(snapshot.used_percent)
      ? snapshot.used_percent
      : null;
    const snapshotRemainingPercent = isFiniteQuotaProgress(snapshot.remaining_percent)
      ? snapshot.remaining_percent
      : null;
    const definitionQuotaProgressObservedAtMs =
      resolveDefinitionQuotaEvidenceObservedAtMs(definition);
    const snapshotQuotaProgressObservedAtMs = resolveSnapshotQuotaProgressObservedAtMs(snapshot);
    const snapshotQuotaEvidenceObservedAtMs = resolveSnapshotQuotaEvidenceObservedAtMs(snapshot);
    const liveQuotaEvidenceObservedAtMs = definitionQuotaProgressObservedAtMs;
    const snapshotQuotaIsAtLeastLive =
      liveQuotaEvidenceObservedAtMs === null ||
      (snapshotQuotaEvidenceObservedAtMs !== null &&
        snapshotQuotaEvidenceObservedAtMs >= liveQuotaEvidenceObservedAtMs);
    const differentCodexCycle = cycleRelationship === 'different';
    const snapshotCanSupersedeDifferentCycle = differentCodexCycle && snapshotMetadataIsNewer;
    const quotaEvidenceKind = snapshotQuotaEvidenceKind(snapshot);

    if (differentCodexCycle && !snapshotCanSupersedeDifferentCycle) return definition;

    let mergedUsedPercent = definitionUsedPercent;
    let mergedRemainingPercent = definitionRemainingPercent;
    let quotaProgressObservedAtMs =
      definitionUsedPercent !== null ? definitionQuotaProgressObservedAtMs : null;
    let quotaChanged = false;

    if (snapshotCanSupersedeDifferentCycle) {
      quotaChanged = true;
      if (quotaEvidenceKind === 'used-bearing') {
        mergedUsedPercent = snapshotUsedPercent;
        mergedRemainingPercent =
          snapshotRemainingPercent ?? remainingPercentFromUsed(snapshotUsedPercent);
        quotaProgressObservedAtMs = snapshotQuotaProgressObservedAtMs;
      } else if (quotaEvidenceKind === 'remaining-only') {
        mergedUsedPercent = null;
        mergedRemainingPercent = snapshotRemainingPercent;
        quotaProgressObservedAtMs = null;
      } else {
        mergedUsedPercent = null;
        mergedRemainingPercent = null;
        quotaProgressObservedAtMs = null;
      }
    } else if (quotaEvidenceKind === 'used-bearing' && snapshotQuotaIsAtLeastLive) {
      quotaChanged = true;
      mergedUsedPercent = snapshotUsedPercent;
      mergedRemainingPercent =
        snapshotRemainingPercent ?? remainingPercentFromUsed(snapshotUsedPercent);
      quotaProgressObservedAtMs = snapshotQuotaProgressObservedAtMs;
    } else if (quotaEvidenceKind === 'remaining-only' && snapshotQuotaIsAtLeastLive) {
      quotaChanged = true;
      mergedUsedPercent = null;
      mergedRemainingPercent = snapshotRemainingPercent;
      quotaProgressObservedAtMs = null;
    }

    const useSnapshotMetadata = snapshotMetadataIsNewer || snapshotCanSupersedeDifferentCycle;
    if (!useSnapshotMetadata && !quotaChanged) return definition;
    const mergedModelScope = useSnapshotMetadata
      ? snapshotModelScope(snapshot)
      : definition.modelScope;
    return {
      ...definition,
      ...(useSnapshotMetadata
        ? {
            windowMode: snapshot.window_mode,
            observationSource: snapshot.source,
            observedAtMs: snapshot.observed_at_ms,
            boundaryAccuracy: snapshot.boundary_accuracy,
            cycleStartMs: currentCycle?.actualStartMs ?? snapshot.cycle_start_ms ?? null,
            cycleEndMs: currentCycle?.scheduledEndMs ?? snapshot.cycle_end_ms ?? null,
            durationSeconds: currentCycle?.durationSeconds ?? snapshot.duration_seconds ?? null,
            modelScope: mergedModelScope,
            stale: snapshot.stale,
            ...snapshotLifecycleDefinition(snapshot),
          }
        : {}),
      remainingPercent: mergedRemainingPercent,
      usedPercent: mergedUsedPercent,
      quotaProgressObservedAtMs,
      display: {
        ...definition.display,
        ...(useSnapshotMetadata
          ? {
              observationSource: snapshot.source,
              observedAtMs: snapshot.observed_at_ms,
              modelScope: mergedModelScope,
            }
          : {}),
        quotaProgressObservedAtMs,
        remainingPercent: mergedRemainingPercent,
        usedPercent: mergedUsedPercent,
      },
    };
  });
  const unmatchedSnapshots = Array.from(snapshotsByKey.entries())
    .filter(([key]) => !matchedSnapshotKeys.has(key))
    .map(([, snapshot]) => snapshot);
  const appendedProviderCounts = new Map<string, number>();
  unmatchedSnapshots.forEach((snapshot) => {
    appendedProviderCounts.set(
      snapshot.provider_window_id,
      (appendedProviderCounts.get(snapshot.provider_window_id) ?? 0) + 1
    );
  });
  const usedDisplayKeys = new Set(compatibleDefinitions.map((definition) => definition.key));
  const appended = unmatchedSnapshots.map((snapshot) => {
    const providerKey = snapshot.provider_window_id;
    const requiresScopedKey =
      usedDisplayKeys.has(providerKey) || (appendedProviderCounts.get(providerKey) ?? 0) > 1;
    const key = requiresScopedKey ? snapshotDisplayKey(snapshot) : providerKey;
    usedDisplayKeys.add(key);
    return snapshotDefinition(snapshot, options, key);
  });
  return [...merged, ...appended].sort((left, right) => {
    const leftRank = definitionSortRank(left);
    const rightRank = definitionSortRank(right);
    if (leftRank !== rightRank) return leftRank - rightRank;
    return left.providerWindowId.localeCompare(right.providerWindowId);
  });
};

const snapshotModelScope = (snapshot: AccountQuotaSnapshotWindow): QuotaModelScope => {
  if (isIncompleteModelScopeSnapshot(snapshot)) {
    return { kind: 'models', models: [], complete: false };
  }
  const kind = snapshot.model_scope_kind.trim().toLowerCase() as QuotaModelScope['kind'];
  const key = snapshot.model_scope_key?.trim();
  const models = normalizedSnapshotModelIDs(snapshot.model_ids);
  const complete =
    kind === 'all' ||
    (kind === 'models' && models.length > 0) ||
    (kind === 'family' && (Boolean(key) || models.length > 0)) ||
    ((kind === 'product' || kind === 'feature') && models.length > 0);
  return {
    kind,
    key: key || undefined,
    models: models.length > 0 ? models : undefined,
    complete,
  };
};

const snapshotCycleDefinition = (
  cycle: AccountQuotaSnapshotCycle | undefined
): AccountQuotaCycleDefinition | null =>
  cycle
    ? {
        id: cycle.id,
        activationId: cycle.activation_id,
        state: cycle.state,
        scheduledStartMs: cycle.scheduled_start_ms ?? null,
        scheduledEndMs: cycle.scheduled_end_ms ?? null,
        actualStartMs: cycle.actual_start_ms,
        actualEndMs: cycle.actual_end_ms ?? null,
        durationSeconds: cycle.duration_seconds ?? null,
        boundaryAccuracy: cycle.boundary_accuracy,
        endReason: cycle.end_reason ?? '',
        parentCycleId: cycle.parent_cycle_id ?? null,
        forecastEligible: cycle.forecast_eligible,
      }
    : null;

const snapshotLifecycleDefinition = (snapshot: AccountQuotaSnapshotWindow) => {
  const hasLifecycle =
    snapshot.logical_window_id !== undefined ||
    snapshot.activation_generation !== undefined ||
    snapshot.availability !== undefined ||
    snapshot.current_cycle !== undefined ||
    snapshot.previous_cycle !== undefined;
  if (!hasLifecycle) return {};
  return {
    logicalWindowId: snapshot.logical_window_id,
    activationGeneration: snapshot.activation_generation,
    availability: snapshot.availability,
    relationshipKind: snapshot.relationship_kind,
    containerProviderWindowId: snapshot.container_provider_window_id,
    firstSeenAtMs: snapshot.first_seen_at_ms,
    lastSeenAtMs: snapshot.last_seen_at_ms,
    missingSinceMs: snapshot.missing_since_ms ?? null,
    deactivatedAtMs: snapshot.deactivated_at_ms ?? null,
    currentCycle: snapshotCycleDefinition(snapshot.current_cycle),
    previousCycle: snapshotCycleDefinition(snapshot.previous_cycle),
  };
};

const snapshotWindowKind = (value: string): AccountQuotaWindowKind => {
  switch (value) {
    case 'five_hour':
    case 'daily':
    case 'weekly':
    case 'monthly':
    case 'billing':
    case 'payg':
    case 'product':
    case 'summary':
      return value;
    default:
      return 'unknown';
  }
};

const snapshotResetAccuracy = (
  accuracy: AccountQuotaBoundaryAccuracy
): AccountQuotaDisplayWindow['resetAccuracy'] => {
  if (accuracy === 'exact') return 'exact';
  if (accuracy === 'derived' || accuracy === 'estimated') return 'estimated';
  return 'unknown';
};

const definitionSortRank = (definition: AccountQuotaWindowDefinition): number => {
  if (definition.windowMode === 'non_window' || definition.windowMode === 'unknown') {
    return Number.MAX_SAFE_INTEGER;
  }
  return definition.durationSeconds ?? Number.MAX_SAFE_INTEGER - 1;
};

const snapshotDefinition = (
  snapshot: AccountQuotaSnapshotWindow,
  options: {
    provider?: string;
    getLabel?: (snapshot: AccountQuotaSnapshotWindow) => string;
  },
  key: string
): AccountQuotaWindowDefinition => {
  const provider: AccountQuotaWindowSource =
    options.provider === 'codex' ||
    options.provider === 'claude' ||
    options.provider === 'antigravity' ||
    options.provider === 'kimi' ||
    options.provider === 'xai'
      ? options.provider
      : 'summary';
  const resetAtMs = snapshot.cycle_end_ms ?? null;
  const lifecycle = snapshotLifecycleDefinition(snapshot);
  const currentStartMs = lifecycle.currentCycle?.actualStartMs ?? snapshot.cycle_start_ms ?? null;
  const currentEndMs = lifecycle.currentCycle?.scheduledEndMs ?? resetAtMs;
  const durationSeconds =
    lifecycle.currentCycle?.durationSeconds ?? snapshot.duration_seconds ?? null;
  const modelScope = snapshotModelScope(snapshot);
  const usedPercent = isFiniteQuotaProgress(snapshot.used_percent) ? snapshot.used_percent : null;
  const remainingPercent = isFiniteQuotaProgress(snapshot.remaining_percent)
    ? snapshot.remaining_percent
    : usedPercent !== null
      ? remainingPercentFromUsed(usedPercent)
      : null;
  const quotaProgressObservedAtMs = resolveSnapshotQuotaProgressObservedAtMs(snapshot);
  const display: AccountQuotaDisplayWindow = {
    key,
    label: options.getLabel?.(snapshot) ?? snapshot.provider_window_id,
    kind: snapshotWindowKind(snapshot.window_kind),
    remainingPercent,
    usedPercent,
    resetLabel: '-',
    resetAtMs: currentEndMs,
    resetAccuracy: snapshotResetAccuracy(snapshot.boundary_accuracy),
    limitWindowSeconds: durationSeconds,
    fromMs: currentStartMs,
    toMs: currentEndMs,
    amountLabel:
      snapshot.used_value !== undefined && snapshot.limit_value !== undefined
        ? `${snapshot.used_value} / ${snapshot.limit_value}${snapshot.quota_unit ? ` ${snapshot.quota_unit}` : ''}`
        : undefined,
    source: provider,
    observationSource: snapshot.source,
    observedAtMs: snapshot.observed_at_ms,
    quotaProgressObservedAtMs,
    windowMode: snapshot.window_mode,
    cycleStartMs: currentStartMs,
    cycleEndMs: currentEndMs,
    modelScope,
    providerWindowAliases: snapshot.provider_window_aliases,
  };
  return {
    key,
    providerWindowId: snapshot.provider_window_id,
    provider,
    label: display.label,
    kind: display.kind ?? 'unknown',
    windowMode: snapshot.window_mode,
    modelScope,
    providerWindowAliases: snapshot.provider_window_aliases,
    observationSource: snapshot.source,
    observedAtMs: snapshot.observed_at_ms,
    quotaProgressObservedAtMs,
    boundaryAccuracy: snapshot.boundary_accuracy,
    cycleStartMs: currentStartMs,
    cycleEndMs: currentEndMs,
    durationSeconds,
    remainingPercent,
    usedPercent,
    stale: snapshot.stale,
    ...lifecycle,
    display,
  };
};
