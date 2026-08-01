import type { OAuthModelAliasEntry } from '@/types';

export type OAuthAliasValidationIssueCode =
  | 'empty_fields'
  | 'same_as_name'
  | 'duplicate_alias'
  | 'duplicate_entry';

export type OAuthAliasValidationIssue = {
  code: OAuthAliasValidationIssueCode;
  index: number;
  name?: string;
  alias?: string;
};

export type OAuthAliasNormalizationResult = {
  accepted: OAuthModelAliasEntry[];
  issues: OAuthAliasValidationIssue[];
  /** Entries that had both name and alias but were dropped by CPA-compatible rules. */
  rejectedCount: number;
  /** Rows skipped because either name or alias was empty (draft rows). */
  incompleteCount: number;
};

const toEntry = (entry: OAuthModelAliasEntry): OAuthModelAliasEntry => {
  const name = String(entry.name ?? '').trim();
  const alias = String(entry.alias ?? '').trim();
  return {
    name,
    alias,
    ...(entry.fork === true ? { fork: true } : {}),
    ...(entry.forceMapping === true ? { forceMapping: true } : {}),
  };
};

/**
 * Normalize OAuth model alias rows with the same rules CPA applies server-side:
 * - require non-empty name and alias
 * - drop name === alias (case-insensitive)
 * - keep the first occurrence of each alias within the channel
 */
export const normalizeOAuthAliasEntries = (
  entries: OAuthModelAliasEntry[]
): OAuthAliasNormalizationResult => {
  const accepted: OAuthModelAliasEntry[] = [];
  const issues: OAuthAliasValidationIssue[] = [];
  const seenAlias = new Set<string>();
  const seenEntry = new Set<string>();
  let incompleteCount = 0;
  let rejectedCount = 0;

  entries.forEach((rawEntry, index) => {
    const entry = toEntry(rawEntry);
    if (!entry.name && !entry.alias) {
      incompleteCount += 1;
      return;
    }
    if (!entry.name || !entry.alias) {
      incompleteCount += 1;
      issues.push({ code: 'empty_fields', index, name: entry.name, alias: entry.alias });
      return;
    }

    if (entry.name.toLowerCase() === entry.alias.toLowerCase()) {
      rejectedCount += 1;
      issues.push({
        code: 'same_as_name',
        index,
        name: entry.name,
        alias: entry.alias,
      });
      return;
    }

    const aliasKey = entry.alias.toLowerCase();
    if (seenAlias.has(aliasKey)) {
      rejectedCount += 1;
      issues.push({
        code: 'duplicate_alias',
        index,
        name: entry.name,
        alias: entry.alias,
      });
      return;
    }

    const entryKey = `${entry.name.toLowerCase()}::${aliasKey}::${entry.fork ? '1' : '0'}::${entry.forceMapping ? '1' : '0'}`;
    if (seenEntry.has(entryKey)) {
      rejectedCount += 1;
      issues.push({
        code: 'duplicate_entry',
        index,
        name: entry.name,
        alias: entry.alias,
      });
      return;
    }

    seenAlias.add(aliasKey);
    seenEntry.add(entryKey);
    accepted.push(entry);
  });

  return { accepted, issues, rejectedCount, incompleteCount };
};

export const findChannelMappings = (
  modelAlias: Record<string, OAuthModelAliasEntry[]>,
  channel: string
): { channelKey: string | null; mappings: OAuthModelAliasEntry[] } => {
  const normalizedChannel = channel.trim().toLowerCase();
  if (!normalizedChannel) {
    return { channelKey: null, mappings: [] };
  }
  const channelKey =
    Object.keys(modelAlias).find((key) => key.trim().toLowerCase() === normalizedChannel) ?? null;
  return {
    channelKey,
    mappings: channelKey ? (modelAlias[channelKey] ?? []) : [],
  };
};

export const createSerialAsyncQueue = () => {
  let chain: Promise<unknown> = Promise.resolve();

  return <T>(task: () => Promise<T>): Promise<T> => {
    const run = chain.then(task, task);
    chain = run.then(
      () => undefined,
      () => undefined
    );
    return run;
  };
};

export const getHttpStatusCode = (err: unknown): number | undefined => {
  if (!err || typeof err !== 'object' || !('status' in err)) return undefined;
  const status = (err as { status?: unknown }).status;
  return typeof status === 'number' && Number.isFinite(status) ? status : undefined;
};

/** DELETE fallback is only safe when the endpoint is missing or method is unsupported. */
export const isMissingOrMethodNotAllowedStatus = (status: number | undefined): boolean =>
  status === 404 || status === 405;

export type OAuthAliasRenamePlan = {
  channel: string;
  nextMappings: OAuthModelAliasEntry[];
};

export type OAuthAliasWritePlan = OAuthAliasRenamePlan & {
  previousMappings: OAuthModelAliasEntry[];
};

export const applyOAuthAliasWritePlans = async (
  plans: OAuthAliasWritePlan[],
  persist: (channel: string, mappings: OAuthModelAliasEntry[]) => Promise<void>
): Promise<void> => {
  const appliedPlans: OAuthAliasWritePlan[] = [];
  let activePlan: OAuthAliasWritePlan | null = null;

  try {
    for (const plan of plans) {
      activePlan = plan;
      await persist(plan.channel, plan.nextMappings);
      appliedPlans.push(plan);
      activePlan = null;
    }
  } catch (writeError: unknown) {
    // The failing request may have reached the server before its response was lost,
    // so restore that channel too. Rewriting the previous value is safe if it did not apply.
    const rollbackPlans = activePlan ? [...appliedPlans, activePlan] : appliedPlans;
    for (const plan of [...rollbackPlans].reverse()) {
      try {
        await persist(plan.channel, plan.previousMappings);
      } catch {
        // Best-effort rollback. The caller reloads the server state and reports the original error.
      }
    }
    throw writeError;
  }
};

export type OAuthAliasRenamePlanResult =
  | { ok: true; plans: OAuthAliasRenamePlan[] }
  | {
      ok: false;
      reason: 'not_found' | 'duplicate_alias' | 'same_as_name';
      alias?: string;
      channel?: string;
    };

/**
 * Build a multi-channel rename plan. Validates every channel before any write so
 * callers can avoid partial updates when one channel would fail.
 */
export const planOAuthAliasRename = (
  modelAlias: Record<string, OAuthModelAliasEntry[]>,
  oldAlias: string,
  newAlias: string
): OAuthAliasRenamePlanResult => {
  const oldTrim = oldAlias.trim();
  const newTrim = newAlias.trim();
  if (!oldTrim || !newTrim || oldTrim.toLowerCase() === newTrim.toLowerCase()) {
    return { ok: false, reason: 'not_found' };
  }

  const oldKey = oldTrim.toLowerCase();
  const newKey = newTrim.toLowerCase();
  const plans: OAuthAliasRenamePlan[] = [];

  for (const [channel, mappings] of Object.entries(modelAlias)) {
    if (!mappings.some((mapping) => (mapping.alias ?? '').trim().toLowerCase() === oldKey)) {
      continue;
    }

    const aliasConflict = mappings.some((mapping) => {
      const mappingAlias = (mapping.alias ?? '').trim().toLowerCase();
      return mappingAlias === newKey && mappingAlias !== oldKey;
    });
    if (aliasConflict) {
      return { ok: false, reason: 'duplicate_alias', alias: newTrim, channel };
    }

    const nextMappings: OAuthModelAliasEntry[] = [];
    for (const mapping of mappings) {
      if ((mapping.alias ?? '').trim().toLowerCase() !== oldKey) {
        nextMappings.push(mapping);
        continue;
      }
      if ((mapping.name ?? '').trim().toLowerCase() === newKey) {
        return { ok: false, reason: 'same_as_name', alias: newTrim, channel };
      }
      nextMappings.push({ ...mapping, alias: newTrim });
    }
    plans.push({ channel, nextMappings });
  }

  if (plans.length === 0) {
    return { ok: false, reason: 'not_found' };
  }
  return { ok: true, plans };
};

/**
 * Merge a new source→alias link into the latest channel mappings (GET-then-merge).
 * Returns `unchanged` when the link already exists, `rejected` for CPA-incompatible rows,
 * or `updated` with the merged mapping list.
 */
export const mergeOAuthAliasLink = (
  currentMappings: OAuthModelAliasEntry[],
  sourceModel: string,
  newAlias: string
):
  | { kind: 'unchanged' }
  | { kind: 'updated'; mappings: OAuthModelAliasEntry[] }
  | { kind: 'rejected'; reason: 'same_as_name' | 'duplicate_alias'; alias: string } => {
  const nameTrim = sourceModel.trim();
  const aliasTrim = newAlias.trim();
  if (!nameTrim || !aliasTrim) {
    return { kind: 'unchanged' };
  }
  if (nameTrim.toLowerCase() === aliasTrim.toLowerCase()) {
    return { kind: 'rejected', reason: 'same_as_name', alias: aliasTrim };
  }

  const nameKey = nameTrim.toLowerCase();
  const aliasKey = aliasTrim.toLowerCase();
  if (
    currentMappings.some(
      (mapping) =>
        (mapping.name ?? '').trim().toLowerCase() === nameKey &&
        (mapping.alias ?? '').trim().toLowerCase() === aliasKey
    )
  ) {
    return { kind: 'unchanged' };
  }
  if (currentMappings.some((mapping) => (mapping.alias ?? '').trim().toLowerCase() === aliasKey)) {
    return { kind: 'rejected', reason: 'duplicate_alias', alias: aliasTrim };
  }

  return {
    kind: 'updated',
    mappings: [...currentMappings, { name: nameTrim, alias: aliasTrim, fork: true }],
  };
};

export const formatOAuthAliasPreview = (mappings: OAuthModelAliasEntry[], maxItems = 3): string => {
  if (!mappings.length) return '';
  const previews = mappings.slice(0, maxItems).map((entry) => {
    const name = String(entry.name ?? '').trim();
    const alias = String(entry.alias ?? '').trim();
    return `${name} → ${alias}`;
  });
  if (mappings.length > maxItems) {
    previews.push(`+${mappings.length - maxItems}`);
  }
  return previews.join(' · ');
};
