import type { TFunction } from 'i18next';
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
import {
  CODEX_MAIN_QUOTA_SCOPE_KEY,
  CODEX_AMBIGUOUS_PROVIDER_WINDOW_ALIASES,
  providerWindowAliasMatches,
  canonicalizeCodexProviderWindowIdForScope,
  isCodexKnownScopedProviderWindowId,
  isCodexLegacyAllScopeReplacement,
  isCodexMainProviderWindowId,
  isAmbiguousCodexProviderWindowId,
  inferCodexQuotaScopeFromProviderWindowId,
  resolveCodexSnapshotQuotaLabel,
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

const validEvidenceAtMs = (value: unknown): number =>
  typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : 0;

const snapshotFieldObservedAt = (snapshot: AccountQuotaSnapshotWindow, field: string) => {
  const fieldObservedAt = validEvidenceAtMs(snapshot.field_sources?.[field]?.observed_at_ms);
  if (fieldObservedAt > 0) {
    return fieldObservedAt;
  }
  return validEvidenceAtMs(snapshot.observed_at_ms);
};

export const snapshotLifecycleEvidenceAtMs = (snapshot: AccountQuotaSnapshotWindow): number => {
  switch (snapshot.availability) {
    case 'inactive':
      return (
        validEvidenceAtMs(snapshot.deactivated_at_ms) ||
        validEvidenceAtMs(snapshot.missing_since_ms) ||
        validEvidenceAtMs(snapshot.last_seen_at_ms)
      );
    case 'pending_absent':
      return (
        validEvidenceAtMs(snapshot.missing_since_ms) || validEvidenceAtMs(snapshot.last_seen_at_ms)
      );
    case 'active':
      return (
        validEvidenceAtMs(snapshot.last_seen_at_ms) || validEvidenceAtMs(snapshot.observed_at_ms)
      );
    default:
      return 0;
  }
};

const snapshotFieldTieBreakKey = (snapshot: AccountQuotaSnapshotWindow, field: string) =>
  [
    snapshot.field_sources?.[field]?.source ?? snapshot.source,
    snapshot.source_observation_id ?? '',
    snapshot.provider_window_id,
    snapshot.window_kind,
  ].join('\u0000');

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
  return {
    provider_window_id: definition.providerWindowId,
    provider_window_aliases: definition.providerWindowAliases,
    window_kind: definition.kind,
    window_mode: windowMode,
    scope_display_name: definition.display.scopeDisplayName,
    model_scope_kind: persistedScopeKind,
    model_scope_key: persistedScopeKey,
    model_ids: persistedModelIDs,
    source: observation?.source ?? definition.observationSource,
    source_observation_id: observation?.source_observation_id,
    observed_at_ms: observation?.observed_at_ms ?? definition.observedAtMs ?? nowMs,
    boundary_accuracy: boundaryAccuracy,
    cycle_start_ms: definition.cycleStartMs ?? undefined,
    cycle_end_ms: definition.cycleEndMs ?? undefined,
    duration_seconds: definition.durationSeconds ?? undefined,
    used_percent: definition.usedPercent ?? undefined,
    remaining_percent: definition.remainingPercent ?? undefined,
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
    if (
      definition.identityAmbiguous ||
      isAmbiguousCodexProviderWindowId(definition.providerWindowId)
    ) {
      return;
    }
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

export type AccountQuotaLocalObservationEvidence = Pick<
  AccountQuotaSnapshotObservationInput,
  'observed_at_ms' | 'inventory_scope_key' | 'inventory_mode'
>;

const CODEX_RATE_LIMITS_INVENTORY_SCOPE_KEY = 'codex:rate-limits';

// Reserved ambiguous snapshots are current-observation evidence without a
// durable lifecycle identity, so a newer local complete Provider inventory is
// authoritative enough to supersede an older persisted ambiguous set — even
// when the snapshot write failed and the server never saw that inventory.
// Identifiable lifecycle rows must never take this shortcut: they are governed
// by the active → pending_absent → inactive debounce.
const shouldSuppressServerAmbiguousSnapshot = (
  snapshot: AccountQuotaSnapshotWindow,
  provider: string | undefined,
  localObservation: AccountQuotaLocalObservationEvidence | undefined
): boolean => {
  if (provider !== 'codex') return false;
  if (!isAmbiguousCodexProviderWindowId(snapshot.provider_window_id)) return false;
  if (!localObservation) return false;
  if (localObservation.inventory_mode !== 'complete') return false;
  if (localObservation.inventory_scope_key?.trim() !== CODEX_RATE_LIMITS_INVENTORY_SCOPE_KEY) {
    return false;
  }
  const localObservedAtMs = validEvidenceAtMs(localObservation.observed_at_ms);
  const snapshotObservedAtMs = validEvidenceAtMs(snapshot.observed_at_ms);
  if (localObservedAtMs <= 0 || snapshotObservedAtMs <= 0) return false;
  // Strictly newer only: on equal timestamps the Manager Server owns source
  // authority ordering that the frontend merge does not reproduce.
  return localObservedAtMs > snapshotObservedAtMs;
};

export const normalizeSemanticScopeKey = (scope: QuotaModelScope): string => {
  if (
    scope.complete === false &&
    (scope.kind !== 'feature' || scope.key === INCOMPLETE_MODEL_SCOPE_KEY) &&
    (!scope.models || scope.models.length === 0)
  ) {
    return 'incomplete_unknown';
  }
  const kind = scope.kind.trim().toLowerCase();
  const key = (scope.key ?? '').trim().toLowerCase();
  const complete = scope.complete !== false ? '1' : '0';
  const models = normalizedSnapshotModelIDs(scope.models);
  return [kind, key, complete, ...models].join('\u0000');
};

type CodexWindowRole = 'five_hour' | 'weekly' | 'monthly' | 'generic';

export const resolveCodexWindowRole = (
  providerWindowId: string,
  kind?: string
): CodexWindowRole => {
  const normalizedId = providerWindowId.trim().toLowerCase();
  const normalizedKind = kind?.trim().toLowerCase();
  if (normalizedKind === 'five_hour' || normalizedKind === 'five-hour') return 'five_hour';
  if (normalizedKind === 'weekly') return 'weekly';
  if (normalizedKind === 'monthly') return 'monthly';
  if (normalizedKind === 'generic') return 'generic';

  if (normalizedId.includes('five-hour')) return 'five_hour';
  if (normalizedId.includes('weekly')) return 'weekly';
  if (normalizedId.includes('monthly')) return 'monthly';
  return 'generic';
};

const codexWindowRolesCompatible = (
  left: { providerWindowId: string; kind?: string; durationSeconds?: number | null },
  right: { providerWindowId: string; kind?: string; durationSeconds?: number | null }
): boolean => {
  const leftRole = resolveCodexWindowRole(left.providerWindowId, left.kind);
  const rightRole = resolveCodexWindowRole(right.providerWindowId, right.kind);
  if (leftRole !== rightRole) return false;
  if (leftRole === 'generic') {
    const leftDur = left.durationSeconds ?? null;
    const rightDur = right.durationSeconds ?? null;
    return leftDur !== null && rightDur !== null && leftDur === rightDur;
  }
  return true;
};

const shouldPresentationShadowIdentifiableServerSnapshot = (
  snapshot: AccountQuotaSnapshotWindow,
  definitions: AccountQuotaWindowDefinition[],
  provider: string | undefined,
  localObservation: AccountQuotaLocalObservationEvidence | undefined
): boolean => {
  if (provider !== 'codex') return false;
  if (
    snapshot.identity_ambiguous === true ||
    isAmbiguousCodexProviderWindowId(snapshot.provider_window_id)
  ) {
    return false;
  }
  if (!localObservation) return false;
  if (localObservation.inventory_mode !== 'complete') return false;
  if (localObservation.inventory_scope_key?.trim() !== CODEX_RATE_LIMITS_INVENTORY_SCOPE_KEY) {
    return false;
  }
  const localObservedAtMs = validEvidenceAtMs(localObservation.observed_at_ms);
  if (localObservedAtMs <= 0) return false;

  const serverObservedAtMs = validEvidenceAtMs(snapshot.observed_at_ms);
  const quotaProvenanceAtMs = snapshotFieldObservedAt(snapshot, 'quota');
  const lifecycleEvidenceAtMs = snapshotLifecycleEvidenceAtMs(snapshot);
  const serverCurrentEvidenceAt = Math.max(
    serverObservedAtMs,
    quotaProvenanceAtMs,
    lifecycleEvidenceAtMs
  );

  // Strictly newer only: on equal timestamps the Manager Server owns source
  // authority ordering that the frontend merge does not reproduce.
  if (localObservedAtMs <= serverCurrentEvidenceAt) return false;

  const snapshotScope = snapshotModelScope(snapshot);
  const snapshotScopeKey = normalizeSemanticScopeKey(snapshotScope);
  const snapshotRole = resolveCodexWindowRole(snapshot.provider_window_id, snapshot.window_kind);
  const snapshotDuration =
    snapshot.current_cycle?.duration_seconds ?? snapshot.duration_seconds ?? null;

  return definitions.some((def) => {
    const defObservedAtMs = validEvidenceAtMs(def.observedAtMs);
    if (defObservedAtMs <= 0 || defObservedAtMs !== localObservedAtMs) return false;

    const isAmbiguous =
      def.identityAmbiguous === true || isAmbiguousCodexProviderWindowId(def.providerWindowId);
    if (!isAmbiguous) return false;

    const defScopeKey = normalizeSemanticScopeKey(def.modelScope);
    if (defScopeKey !== snapshotScopeKey) return false;

    const defRole = resolveCodexWindowRole(def.providerWindowId, def.kind);
    if (defRole !== snapshotRole) return false;

    if (snapshotRole === 'generic') {
      const defDuration = def.durationSeconds ?? null;
      if (defDuration === null || snapshotDuration === null || defDuration !== snapshotDuration) {
        return false;
      }
    }

    return true;
  });
};

const applySnapshotOverlayToDefinition = (
  definition: AccountQuotaWindowDefinition,
  snapshot: AccountQuotaSnapshotWindow,
  options: {
    provider?: string;
    getLabel?: (snapshot: AccountQuotaSnapshotWindow) => string;
    t?: TFunction;
  }
): AccountQuotaWindowDefinition => {
  const localQuotaObservedAt = validEvidenceAtMs(definition.observedAtMs);
  const localScopeDisplayName = definition.display.scopeDisplayName?.trim() || '';
  const localDisplayObservedAt = localScopeDisplayName ? localQuotaObservedAt : 0;
  const quotaObservedAt = snapshotFieldObservedAt(snapshot, 'quota');
  const lifecycleObservedAt = snapshotLifecycleEvidenceAtMs(snapshot);
  const scopeDisplayName = snapshot.scope_display_name?.trim() || '';
  const displayObservedAt = scopeDisplayName
    ? snapshotFieldObservedAt(snapshot, 'scope_display_name')
    : 0;
  const quotaIsFresh = localQuotaObservedAt <= 0 || quotaObservedAt >= localQuotaObservedAt;
  const lifecycleIsFresh =
    lifecycleObservedAt > 0 &&
    (localQuotaObservedAt <= 0 || lifecycleObservedAt >= localQuotaObservedAt);
  const displayIsFresh =
    scopeDisplayName !== '' &&
    displayObservedAt > 0 &&
    (localDisplayObservedAt <= 0 || displayObservedAt >= localDisplayObservedAt);
  const localPositiveSupersedesLifecycle =
    definition.currentHidden === true &&
    definition.availability === 'active' &&
    localQuotaObservedAt > 0 &&
    (!lifecycleIsFresh || localQuotaObservedAt > lifecycleObservedAt);

  if (
    !quotaIsFresh &&
    !lifecycleIsFresh &&
    !displayIsFresh &&
    !localPositiveSupersedesLifecycle
  ) {
    return definition;
  }

  const currentCycle = snapshotCycleDefinition(snapshot.current_cycle);
  const snapshotScope = snapshotModelScope(snapshot);
  const mergedDuration = quotaIsFresh
    ? (currentCycle?.durationSeconds ?? snapshot.duration_seconds ?? null)
    : definition.durationSeconds;
  const mergedScope = quotaIsFresh ? snapshotScope : definition.modelScope;
  const quotaOverlay = quotaIsFresh
    ? {
        windowMode: snapshot.window_mode,
        observationSource: snapshot.source,
        observedAtMs: snapshot.observed_at_ms,
        boundaryAccuracy: snapshot.boundary_accuracy,
        cycleStartMs: currentCycle?.actualStartMs ?? snapshot.cycle_start_ms ?? null,
        cycleEndMs: currentCycle?.scheduledEndMs ?? snapshot.cycle_end_ms ?? null,
        durationSeconds: mergedDuration,
        remainingPercent: snapshot.remaining_percent ?? definition.remainingPercent,
        usedPercent: snapshot.used_percent ?? definition.usedPercent,
        modelScope: snapshotScope,
        stale: snapshot.stale,
      }
    : {};
  const lifecycleOverlay = lifecycleIsFresh
    ? {
        ...snapshotLifecycleDefinition(snapshot),
        ...(snapshot.availability === 'pending_absent' || snapshot.availability === 'inactive'
          ? { stale: true }
          : {}),
      }
    : {};
  const displayOverlay: {
    label?: string;
    display?: AccountQuotaDisplayWindow;
  } = displayIsFresh
    ? (() => {
        const label = resolveSnapshotQuotaLabel(snapshot, options, mergedScope, mergedDuration);
        return {
          label,
          display: {
            ...definition.display,
            label,
            scopeDisplayName,
            ...(snapshot.identity_ambiguous ||
            isAmbiguousCodexProviderWindowId(snapshot.provider_window_id)
              ? { identityAmbiguous: true }
              : {}),
          },
        };
      })()
    : {};
  const identityAmbiguous =
    definition.identityAmbiguous === true ||
    snapshot.identity_ambiguous === true ||
    (options.provider === 'codex' &&
      isAmbiguousCodexProviderWindowId(snapshot.provider_window_id));

  const mergedDefinition: AccountQuotaWindowDefinition = {
    ...definition,
    ...quotaOverlay,
    ...lifecycleOverlay,
    ...displayOverlay,
    ...(identityAmbiguous
      ? {
          identityAmbiguous: true,
          display: {
            ...definition.display,
            ...(displayOverlay.display ?? {}),
            identityAmbiguous: true,
          },
        }
      : {}),
  };
  if (
    lifecycleIsFresh &&
    (snapshot.current_hidden !== undefined || snapshot.availability === 'active')
  ) {
    mergedDefinition.currentHidden = snapshot.current_hidden === true;
    mergedDefinition.display = {
      ...mergedDefinition.display,
      currentHidden: mergedDefinition.currentHidden,
    };
  }
  if (localPositiveSupersedesLifecycle) {
    mergedDefinition.currentHidden = false;
    mergedDefinition.display = { ...mergedDefinition.display, currentHidden: false };
  }
  return mergedDefinition;
};

export const mergeAccountQuotaSnapshotWindows = (
  definitions: AccountQuotaWindowDefinition[],
  snapshots: AccountQuotaSnapshotWindow[],
  options: {
    provider?: string;
    localObservation?: AccountQuotaLocalObservationEvidence;
    getLabel?: (snapshot: AccountQuotaSnapshotWindow) => string;
    t?: TFunction;
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
  const exactMatchedSnapshotByDefIndex = new Map<number, AccountQuotaSnapshotWindow>();

  compatibleDefinitions.forEach((definition, index) => {
    if (definition.modelScope.kind === 'all' && definition.modelScope.complete === false) {
      return;
    }
    const key = snapshotScopeKey({
      provider_window_id: definition.providerWindowId,
      model_scope_kind: definition.modelScope.kind,
      model_scope_key: definition.modelScope.key,
      model_ids: definition.modelScope.models,
    });
    const snapshot = snapshotsByKey.get(key);
    if (snapshot) {
      matchedSnapshotKeys.add(key);
      exactMatchedSnapshotByDefIndex.set(index, snapshot);
    }
  });

  const aliasMatchedSnapshotByDefIndex = new Map<number, AccountQuotaSnapshotWindow>();

  if (options.provider === 'codex') {
    const candidateDefs: Array<{
      index: number;
      definition: AccountQuotaWindowDefinition;
      partitionKey: string;
    }> = [];
    compatibleDefinitions.forEach((definition, index) => {
      if (exactMatchedSnapshotByDefIndex.has(index)) return;
      if (
        definition.identityAmbiguous === true ||
        isAmbiguousCodexProviderWindowId(definition.providerWindowId)
      ) {
        return;
      }
      const scopeKey = normalizeSemanticScopeKey(definition.modelScope);
      const role = resolveCodexWindowRole(definition.providerWindowId, definition.kind);
      const partitionKey = `${scopeKey}\u0000${role}`;
      candidateDefs.push({ index, definition, partitionKey });
    });

    const candidateSnaps: Array<{
      key: string;
      snapshot: AccountQuotaSnapshotWindow;
      partitionKey: string;
    }> = [];
    Array.from(snapshotsByKey.entries()).forEach(([key, snapshot]) => {
      if (matchedSnapshotKeys.has(key)) return;
      if (
        snapshot.identity_ambiguous === true ||
        isAmbiguousCodexProviderWindowId(snapshot.provider_window_id)
      ) {
        return;
      }
      const snapshotScope = snapshotModelScope(snapshot);
      const scopeKey = normalizeSemanticScopeKey(snapshotScope);
      const role = resolveCodexWindowRole(snapshot.provider_window_id, snapshot.window_kind);
      const partitionKey = `${scopeKey}\u0000${role}`;
      candidateSnaps.push({ key, snapshot, partitionKey });
    });

    const defsByPartition = new Map<string, typeof candidateDefs>();
    candidateDefs.forEach((item) => {
      const list = defsByPartition.get(item.partitionKey) ?? [];
      list.push(item);
      defsByPartition.set(item.partitionKey, list);
    });

    const snapsByPartition = new Map<string, typeof candidateSnaps>();
    candidateSnaps.forEach((item) => {
      const list = snapsByPartition.get(item.partitionKey) ?? [];
      list.push(item);
      snapsByPartition.set(item.partitionKey, list);
    });

    defsByPartition.forEach((partitionDefs, partitionKey) => {
      const partitionSnaps = snapsByPartition.get(partitionKey);
      if (!partitionSnaps || partitionSnaps.length === 0) return;

      const defMatches = new Map<number, typeof candidateSnaps>();
      const snapMatches = new Map<string, typeof candidateDefs>();

      partitionDefs.forEach((defItem) => {
        partitionSnaps.forEach((snapItem) => {
          if (
            !codexWindowRolesCompatible(
              {
                providerWindowId: defItem.definition.providerWindowId,
                kind: defItem.definition.kind,
                durationSeconds: defItem.definition.durationSeconds,
              },
              {
                providerWindowId: snapItem.snapshot.provider_window_id,
                kind: snapItem.snapshot.window_kind,
                durationSeconds:
                  snapItem.snapshot.current_cycle?.duration_seconds ??
                  snapItem.snapshot.duration_seconds,
              }
            )
          ) {
            return;
          }
          const aliases = providerWindowAliasMatches(
            {
              id: defItem.definition.providerWindowId,
              providerWindowAliases: defItem.definition.providerWindowAliases,
            },
            {
              id: snapItem.snapshot.provider_window_id,
              providerWindowAliases: snapItem.snapshot.provider_window_aliases,
            }
          ).filter((alias) => !CODEX_AMBIGUOUS_PROVIDER_WINDOW_ALIASES.has(alias));

          if (aliases.length > 0) {
            const defMatchedList = defMatches.get(defItem.index) ?? [];
            defMatchedList.push(snapItem);
            defMatches.set(defItem.index, defMatchedList);

            const snapMatchedList = snapMatches.get(snapItem.key) ?? [];
            snapMatchedList.push(defItem);
            snapMatches.set(snapItem.key, snapMatchedList);
          }
        });
      });

      partitionDefs.forEach((defItem) => {
        const matchedSnaps = defMatches.get(defItem.index);
        if (matchedSnaps && matchedSnaps.length === 1) {
          const snapItem = matchedSnaps[0];
          const matchedDefs = snapMatches.get(snapItem.key);
          if (matchedDefs && matchedDefs.length === 1 && matchedDefs[0].index === defItem.index) {
            aliasMatchedSnapshotByDefIndex.set(defItem.index, snapItem.snapshot);
            matchedSnapshotKeys.add(snapItem.key);
          }
        }
      });
    });
  }

  const merged = compatibleDefinitions.map((definition, index) => {
    const snapshot =
      exactMatchedSnapshotByDefIndex.get(index) ?? aliasMatchedSnapshotByDefIndex.get(index);
    if (!snapshot) return definition;
    return applySnapshotOverlayToDefinition(definition, snapshot, options);
  });

  const unmatchedSnapshots = Array.from(snapshotsByKey.entries())
    .filter(([key]) => !matchedSnapshotKeys.has(key))
    .map(([, snapshot]) => snapshot)
    .filter(
      (snapshot) =>
        !shouldSuppressServerAmbiguousSnapshot(
          snapshot,
          options.provider,
          options.localObservation
        ) &&
        !shouldPresentationShadowIdentifiableServerSnapshot(
          snapshot,
          compatibleDefinitions,
          options.provider,
          options.localObservation
        )
    );
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

export const filterCurrentAccountQuotaWindowDefinitions = (
  definitions: AccountQuotaWindowDefinition[]
): AccountQuotaWindowDefinition[] =>
  definitions.filter(
    (definition) => definition.availability !== 'inactive' && definition.currentHidden !== true
  );

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

const snapshotLifecycleDefinition = (
  snapshot: AccountQuotaSnapshotWindow
): Partial<AccountQuotaWindowDefinition> => {
  const hasLifecycle =
    snapshot.logical_window_id !== undefined ||
    snapshot.activation_generation !== undefined ||
    snapshot.availability !== undefined ||
    snapshot.current_cycle !== undefined ||
    snapshot.previous_cycle !== undefined ||
    snapshot.current_hidden !== undefined;
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
    ...(snapshot.current_hidden !== undefined ? { currentHidden: snapshot.current_hidden } : {}),
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

type SnapshotLabelOptions = {
  provider?: string;
  getLabel?: (snapshot: AccountQuotaSnapshotWindow) => string;
  t?: TFunction;
};

const resolveSnapshotQuotaLabel = (
  snapshot: AccountQuotaSnapshotWindow,
  options: SnapshotLabelOptions,
  modelScope: QuotaModelScope,
  durationSeconds: number | null
): string => {
  const scopeDisplayName = snapshot.scope_display_name?.trim() || undefined;

  if (options.provider === 'claude' && scopeDisplayName) return scopeDisplayName;

  if (options.provider === 'codex') {
    const metadata = resolveCodexSnapshotQuotaLabel({
      providerWindowId: snapshot.provider_window_id,
      windowKind: snapshot.window_kind,
      modelScope,
      scopeDisplayName,
      durationSeconds,
    });
    if (metadata) {
      if (options.t) {
        return options.t(metadata.labelKey, metadata.labelParams);
      }
      if (scopeDisplayName) return scopeDisplayName;
    }
  } else if (scopeDisplayName) {
    return scopeDisplayName;
  }

  return options.getLabel?.(snapshot) ?? snapshot.provider_window_id;
};

const definitionSortRank = (definition: AccountQuotaWindowDefinition): number => {
  if (definition.windowMode === 'non_window' || definition.windowMode === 'unknown') {
    return Number.MAX_SAFE_INTEGER;
  }
  return definition.durationSeconds ?? Number.MAX_SAFE_INTEGER - 1;
};

const snapshotDefinition = (
  snapshot: AccountQuotaSnapshotWindow,
  options: SnapshotLabelOptions,
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
  const scopeDisplayName = snapshot.scope_display_name?.trim() || undefined;
  const label = resolveSnapshotQuotaLabel(snapshot, options, modelScope, durationSeconds);
  const display: AccountQuotaDisplayWindow = {
    key,
    label,
    kind: snapshotWindowKind(snapshot.window_kind),
    remainingPercent: snapshot.remaining_percent ?? null,
    usedPercent: snapshot.used_percent ?? null,
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
    windowMode: snapshot.window_mode,
    cycleStartMs: currentStartMs,
    cycleEndMs: currentEndMs,
    modelScope,
    scopeDisplayName,
    providerWindowAliases: snapshot.provider_window_aliases,
    identityAmbiguous:
      snapshot.identity_ambiguous === true ||
      (provider === 'codex' && isAmbiguousCodexProviderWindowId(snapshot.provider_window_id)),
    ...(snapshot.current_hidden !== undefined ? { currentHidden: snapshot.current_hidden } : {}),
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
    identityAmbiguous:
      snapshot.identity_ambiguous === true ||
      (provider === 'codex' && isAmbiguousCodexProviderWindowId(snapshot.provider_window_id)),
    observationSource: snapshot.source,
    observedAtMs: snapshot.observed_at_ms,
    boundaryAccuracy: snapshot.boundary_accuracy,
    cycleStartMs: currentStartMs,
    cycleEndMs: currentEndMs,
    durationSeconds,
    remainingPercent: snapshot.remaining_percent ?? null,
    usedPercent: snapshot.used_percent ?? null,
    stale: snapshot.stale,
    ...lifecycle,
    display,
  };
};
