/**
 * Builder functions for constructing quota data structures.
 */

import type {
  AntigravityQuotaBucket,
  AntigravityQuotaGroup,
  AntigravityQuotaInfo,
  AntigravityModelsPayload,
  AntigravityQuotaSummaryPayload,
  KimiUsagePayload,
  KimiUsageDetail,
  KimiLimitItem,
  KimiLimitWindow,
  KimiQuotaRow,
} from '@/types';
import { normalizeQuotaFraction, normalizeStringValue } from './parsers';

export function getAntigravityQuotaInfo(entry?: AntigravityQuotaInfo): {
  remainingFraction: number | null;
  resetTime?: string;
  displayName?: string;
} {
  if (!entry) {
    return { remainingFraction: null };
  }
  const quotaInfo = entry.quotaInfo ?? entry.quota_info ?? {};
  const remainingValue =
    quotaInfo.remainingFraction ?? quotaInfo.remaining_fraction ?? quotaInfo.remaining;
  const remainingFraction = normalizeQuotaFraction(remainingValue);
  const resetValue = quotaInfo.resetTime ?? quotaInfo.reset_time;
  const resetTime = typeof resetValue === 'string' ? resetValue : undefined;
  const displayName = normalizeStringValue(entry.displayName ?? entry.display_name) ?? undefined;

  return {
    remainingFraction,
    resetTime,
    displayName,
  };
}

export function findAntigravityModel(
  models: AntigravityModelsPayload,
  identifier: string
): { id: string; entry: AntigravityQuotaInfo } | null {
  const direct = models[identifier];
  if (direct) {
    return { id: identifier, entry: direct };
  }

  const match = Object.entries(models).find(([, entry]) => {
    const name = typeof entry?.displayName === 'string' ? entry.displayName : '';
    return name.toLowerCase() === identifier.toLowerCase();
  });
  if (match) {
    return { id: match[0], entry: match[1] };
  }

  return null;
}

const ANTIGRAVITY_BUCKET_WINDOW_ORDER = new Map<string, number>([
  ['5h', 0],
  ['five-hour', 0],
  ['five_hour', 0],
  ['weekly', 1],
  ['week', 1],
]);

function toStableId(value: string, fallback: string): string {
  const normalized = value
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
  return normalized || fallback;
}

function getAntigravityWindowOrder(bucket: AntigravityQuotaBucket): number {
  const window = bucket.window?.toLowerCase();
  if (!window) return Number.MAX_SAFE_INTEGER;
  return ANTIGRAVITY_BUCKET_WINDOW_ORDER.get(window) ?? Number.MAX_SAFE_INTEGER;
}

type AntigravityModelQuotaEntry = {
  id: string;
  label: string;
  remainingFraction: number;
  resetTime?: string;
  description: string;
};

function uniqueStringList(values: Array<string | null | undefined>): string[] {
  const seen = new Set<string>();
  const result: string[] = [];

  values.forEach((value) => {
    const normalized = normalizeStringValue(value);
    if (!normalized || seen.has(normalized)) return;
    seen.add(normalized);
    result.push(normalized);
  });

  return result;
}

function getPayloadModelIds(
  payload: AntigravityQuotaSummaryPayload | undefined,
  camelKey: keyof AntigravityQuotaSummaryPayload,
  snakeKey: keyof AntigravityQuotaSummaryPayload
): string[] {
  const rawValue = payload?.[camelKey] ?? payload?.[snakeKey];
  if (!Array.isArray(rawValue)) return [];
  return uniqueStringList(rawValue.filter((value): value is string => typeof value === 'string'));
}

function getAntigravityAgentModelIds(payload?: AntigravityQuotaSummaryPayload): string[] {
  const sorts = payload?.agentModelSorts ?? payload?.agent_model_sorts ?? [];
  if (!Array.isArray(sorts)) return [];

  return uniqueStringList(
    sorts.flatMap((sort) =>
      Array.isArray(sort?.groups)
        ? sort.groups.flatMap((group) => group?.modelIds ?? group?.model_ids ?? [])
        : []
    )
  );
}

function getTieredAntigravityModelIds(
  payload: AntigravityQuotaSummaryPayload | undefined,
  tier: string
): string[] {
  const tiered = payload?.tieredModelIds ?? payload?.tiered_model_ids;
  const rawValue = tiered?.[tier];
  if (!Array.isArray(rawValue)) return [];
  return uniqueStringList(rawValue.filter((value): value is string => typeof value === 'string'));
}

function resolveAntigravityModelId(
  id: string,
  models: AntigravityModelsPayload,
  payload?: AntigravityQuotaSummaryPayload
): string {
  if (models[id]) return id;

  const deprecated = payload?.deprecatedModelIds ?? payload?.deprecated_model_ids;
  const replacement = deprecated?.[id]?.newModelId ?? deprecated?.[id]?.new_model_id;
  if (replacement && models[replacement]) return replacement;

  return id;
}

function getAntigravityModelSearchText(id: string, entry: AntigravityQuotaInfo): string {
  return [
    id,
    entry.displayName,
    entry.display_name,
    entry.model,
    entry.apiProvider,
    entry.api_provider,
    entry.modelProvider,
    entry.model_provider,
  ]
    .map((value) => (typeof value === 'string' ? value.toLowerCase() : ''))
    .join(' ');
}

function getAntigravityModelNameSearchText(id: string, entry: AntigravityQuotaInfo): string {
  return [id, entry.displayName, entry.display_name, entry.model]
    .map((value) => (typeof value === 'string' ? value.toLowerCase() : ''))
    .join(' ');
}

function isAntigravityExternalModel(id: string, entry: AntigravityQuotaInfo): boolean {
  const searchText = getAntigravityModelSearchText(id, entry);
  return (
    searchText.includes('anthropic') ||
    searchText.includes('openai') ||
    searchText.includes('claude') ||
    searchText.includes('gpt')
  );
}

function isAntigravityGeminiModel(id: string, entry: AntigravityQuotaInfo): boolean {
  const searchText = getAntigravityModelSearchText(id, entry);
  const nameSearchText = getAntigravityModelNameSearchText(id, entry);
  return (
    nameSearchText.includes('gemini') ||
    searchText.includes('api_provider_google_gemini') ||
    searchText.includes('google_gemini')
  );
}

function getAntigravityModelQuotaEntry(
  id: string,
  models: AntigravityModelsPayload,
  payload?: AntigravityQuotaSummaryPayload
): AntigravityModelQuotaEntry | null {
  const resolvedId = resolveAntigravityModelId(id, models, payload);
  const model = findAntigravityModel(models, resolvedId);
  if (!model) return null;

  const info = getAntigravityQuotaInfo(model.entry);
  const remainingFraction = info.remainingFraction ?? (info.resetTime ? 0 : null);
  if (remainingFraction === null) return null;

  return {
    id: model.id,
    label: info.displayName ?? model.id,
    remainingFraction,
    resetTime: info.resetTime,
    description: model.id,
  };
}

function pickEarlierResetTime(current?: string, next?: string): string | undefined {
  if (!current) return next;
  if (!next) return current;
  const currentTime = new Date(current).getTime();
  const nextTime = new Date(next).getTime();
  if (Number.isNaN(currentTime)) return next;
  if (Number.isNaN(nextTime)) return current;
  return currentTime <= nextTime ? current : next;
}

function buildAntigravitySharedGroup(
  id: string,
  label: string,
  modelIds: string[],
  models: AntigravityModelsPayload,
  payload: AntigravityQuotaSummaryPayload | undefined
): AntigravityQuotaGroup | null {
  const entries = uniqueStringList(modelIds)
    .map((modelId) => getAntigravityModelQuotaEntry(modelId, models, payload))
    .filter((entry): entry is AntigravityModelQuotaEntry => Boolean(entry));

  if (entries.length === 0) return null;

  const remainingFraction = Math.min(...entries.map((entry) => entry.remainingFraction));
  const resetTime = entries.reduce<string | undefined>(
    (current, entry) => pickEarlierResetTime(current, entry.resetTime),
    undefined
  );
  const modelIdsForDescription = entries.map((entry) => entry.id);

  return {
    id,
    label,
    models: modelIdsForDescription,
    buckets: [
      {
        id: `${id}-shared`,
        label,
        remainingFraction,
        resetTime,
        description: modelIdsForDescription.join(', '),
      },
    ],
  };
}

export function buildAntigravityQuotaGroupsFromModels(
  models: AntigravityModelsPayload,
  payload?: AntigravityQuotaSummaryPayload
): AntigravityQuotaGroup[] {
  const modelIds = Object.keys(models);
  const tabModelIds = new Set(getPayloadModelIds(payload, 'tabModelIds', 'tab_model_ids'));
  const geminiModelIds = uniqueStringList([
    ...getAntigravityAgentModelIds(payload),
    ...getTieredAntigravityModelIds(payload, 'pro'),
    ...getTieredAntigravityModelIds(payload, 'flash'),
    ...getTieredAntigravityModelIds(payload, 'flashLite'),
    ...getPayloadModelIds(payload, 'commandModelIds', 'command_model_ids'),
    ...getPayloadModelIds(payload, 'imageGenerationModelIds', 'image_generation_model_ids'),
    ...getPayloadModelIds(payload, 'mqueryModelIds', 'mquery_model_ids'),
    ...getPayloadModelIds(payload, 'webSearchModelIds', 'web_search_model_ids'),
    ...getPayloadModelIds(payload, 'commitMessageModelIds', 'commit_message_model_ids'),
    ...modelIds.filter((id) => isAntigravityGeminiModel(id, models[id])),
  ]).filter((id) => !tabModelIds.has(id) && models[id] && isAntigravityGeminiModel(id, models[id]));

  return [
    buildAntigravitySharedGroup(
      'claude-gpt',
      'Claude/GPT',
      modelIds.filter((id) => isAntigravityExternalModel(id, models[id])),
      models,
      payload
    ),
    buildAntigravitySharedGroup('gemini', 'Gemini', geminiModelIds, models, payload),
  ].filter((group): group is AntigravityQuotaGroup => group !== null);
}

export function buildAntigravityQuotaGroups(
  payload: AntigravityQuotaSummaryPayload
): AntigravityQuotaGroup[] {
  const groups = Array.isArray(payload.groups) ? payload.groups : [];
  const parsedGroups = groups
    .map((group, groupIndex): AntigravityQuotaGroup | null => {
      const label =
        normalizeStringValue(group.displayName ?? group.display_name) ??
        `Quota Group ${groupIndex + 1}`;
      const groupId = toStableId(label, `quota-group-${groupIndex + 1}`);
      const buckets = Array.isArray(group.buckets) ? group.buckets : [];
      const parsedBuckets = buckets
        .map((bucket, bucketIndex): AntigravityQuotaBucket | null => {
          const remainingFraction = normalizeQuotaFraction(
            bucket.remainingFraction ?? bucket.remaining_fraction
          );
          if (remainingFraction === null) return null;

          const window = normalizeStringValue(bucket.window) ?? undefined;
          const rawId =
            normalizeStringValue(bucket.bucketId ?? bucket.bucket_id) ??
            `${groupId}-${window ?? `bucket-${bucketIndex + 1}`}`;
          const label = normalizeStringValue(bucket.displayName ?? bucket.display_name) ?? rawId;

          return {
            id: rawId,
            label,
            window,
            remainingFraction,
            resetTime: normalizeStringValue(bucket.resetTime ?? bucket.reset_time) ?? undefined,
            description: normalizeStringValue(bucket.description) ?? undefined,
          };
        })
        .filter((bucket): bucket is AntigravityQuotaBucket => bucket !== null)
        .sort((a, b) => {
          const orderDiff = getAntigravityWindowOrder(a) - getAntigravityWindowOrder(b);
          if (orderDiff !== 0) return orderDiff;
          return a.label.localeCompare(b.label);
        });

      if (parsedBuckets.length === 0) return null;

      return {
        id: groupId,
        label,
        description: normalizeStringValue(group.description) ?? undefined,
        buckets: parsedBuckets,
      };
    })
    .filter((group): group is AntigravityQuotaGroup => group !== null);

  if (parsedGroups.length > 0) return parsedGroups;

  if (payload.models && typeof payload.models === 'object' && !Array.isArray(payload.models)) {
    return buildAntigravityQuotaGroupsFromModels(payload.models, payload);
  }

  return [];
}

function toInt(value: unknown): number | null {
  if (typeof value === 'number' && Number.isFinite(value)) return Math.floor(value);
  if (typeof value === 'string') {
    const parsed = Number(value.trim());
    return Number.isFinite(parsed) ? Math.floor(parsed) : null;
  }
  return null;
}

type KimiRowLabel = Pick<KimiQuotaRow, 'label' | 'labelKey' | 'labelParams'>;

function kimiResetHint(data: Record<string, unknown>): string | undefined {
  const absoluteKeys = ['reset_at', 'resetAt', 'reset_time', 'resetTime'];
  for (const key of absoluteKeys) {
    const raw = data[key];
    if (typeof raw === 'string' && raw.trim()) {
      try {
        const truncated = raw.replace(/(\.\d{6})\d+/, '$1');
        const date = new Date(truncated);
        if (Number.isNaN(date.getTime())) continue;
        const now = Date.now();
        const delta = date.getTime() - now;
        if (delta <= 0) return undefined;
        const totalMinutes = Math.floor(delta / 60000);
        const hours = Math.floor(totalMinutes / 60);
        const minutes = totalMinutes % 60;
        if (hours > 0 && minutes > 0) return `${hours}h ${minutes}m`;
        if (hours > 0) return `${hours}h`;
        if (minutes > 0) return `${minutes}m`;
        return '<1m';
      } catch {
        continue;
      }
    }
  }

  const relativeKeys = ['reset_in', 'resetIn', 'ttl'];
  for (const key of relativeKeys) {
    const raw = toInt(data[key]);
    if (raw !== null && raw > 0) {
      const hours = Math.floor(raw / 3600);
      const minutes = Math.floor((raw % 3600) / 60);
      if (hours > 0 && minutes > 0) return `${hours}h ${minutes}m`;
      if (hours > 0) return `${hours}h`;
      if (minutes > 0) return `${minutes}m`;
      return '<1m';
    }
  }

  return undefined;
}

function kimiDurationToken(duration: number, rawTimeUnit: unknown): string {
  const unit = typeof rawTimeUnit === 'string' ? rawTimeUnit.trim().toUpperCase() : '';
  if (unit === 'MINUTE' || unit === 'MINUTES') {
    return duration % 60 === 0 ? `${duration / 60}h` : `${duration}m`;
  }
  if (unit === 'HOUR' || unit === 'HOURS') return `${duration}h`;
  if (unit === 'DAY' || unit === 'DAYS') return `${duration}d`;
  return `${duration}s`;
}

function kimiLimitLabel(
  item: KimiLimitItem,
  detail: KimiUsageDetail | KimiLimitItem,
  window: KimiLimitWindow,
  index: number
): KimiRowLabel {
  for (const key of ['name', 'title', 'scope'] as const) {
    const val = (item as Record<string, unknown>)[key] ?? (detail as Record<string, unknown>)[key];
    if (typeof val === 'string' && val.trim()) return { label: val.trim() };
  }

  const duration =
    toInt(window.duration) ??
    toInt((item as Record<string, unknown>).duration) ??
    toInt((detail as Record<string, unknown>).duration);
  const timeUnit =
    (window as Record<string, unknown>).timeUnit ??
    (item as Record<string, unknown>).timeUnit ??
    (detail as Record<string, unknown>).timeUnit;

  if (duration !== null && duration > 0) {
    return {
      labelKey: 'kimi_quota.limit_window',
      labelParams: {
        duration: kimiDurationToken(duration, timeUnit),
      },
    };
  }

  return {
    labelKey: 'kimi_quota.limit_index',
    labelParams: {
      index: index + 1,
    },
  };
}

function toKimiUsageRow(
  data: Record<string, unknown>,
  fallbackLabel: KimiRowLabel
): (KimiRowLabel & { used: number; limit: number; resetHint?: string }) | null {
  const limit = toInt(data.limit);
  let used = toInt(data.used);
  if (used === null) {
    const remaining = toInt(data.remaining);
    if (remaining !== null && limit !== null) {
      used = limit - remaining;
    }
  }
  if (used === null && limit === null) return null;
  const explicitLabel =
    (typeof data.name === 'string' && data.name.trim()) ||
    (typeof data.title === 'string' && data.title.trim());
  const label = explicitLabel ? { label: explicitLabel } : fallbackLabel;
  return {
    ...label,
    used: used ?? 0,
    limit: limit ?? 0,
    resetHint: kimiResetHint(data),
  };
}

export function buildKimiQuotaRows(payload: KimiUsagePayload): KimiQuotaRow[] {
  const rows: KimiQuotaRow[] = [];

  const usage = payload.usage;
  if (usage && typeof usage === 'object') {
    const summary = toKimiUsageRow(usage as Record<string, unknown>, {
      labelKey: 'kimi_quota.weekly_limit',
    });
    if (summary) {
      rows.push({ id: 'summary', ...summary });
    }
  }

  const limits = payload.limits;
  if (Array.isArray(limits)) {
    limits.forEach((item, idx) => {
      const detail = (item.detail && typeof item.detail === 'object' ? item.detail : item) as
        | KimiUsageDetail
        | KimiLimitItem;
      const window = (
        item.window && typeof item.window === 'object' ? item.window : {}
      ) as KimiLimitWindow;
      const fallbackLabel = kimiLimitLabel(item, detail, window, idx);
      const row = toKimiUsageRow(detail as Record<string, unknown>, fallbackLabel);
      if (row) {
        rows.push({ id: `limit-${idx}`, ...row });
      }
    });
  }

  return rows;
}
