import type { ModelPrice } from '@/utils/usage';
import type {
  ModelPriceUsageSummaryResponse,
  ModelPriceSyncCandidate,
  ModelPriceSyncCandidateSet,
} from '@/services/api/usageService';

export type ModelPriceFilter = 'all' | 'missing' | 'saved' | 'candidates';

export type PriceDraft = {
  model: string;
  prompt: string;
  completion: string;
  cache: string;
  cacheRead: string;
  cacheCreation: string;
};

export type ModelPriceRow = {
  model: string;
  calls: number;
  requestedCalls: number;
  resolvedCalls: number;
  hasPrice: boolean;
  price?: ModelPrice;
  candidateCount: number;
};

export type ModelPriceSummary = {
  total: number;
  saved: number;
  missing: number;
  candidates: number;
};

export const createEmptyPriceDraft = (): PriceDraft => ({
  model: '',
  prompt: '',
  completion: '',
  cache: '',
  cacheRead: '',
  cacheCreation: '',
});

const createConfiguredDraftValue = (value: number | undefined, configured?: boolean): string =>
  configured || Number(value) > 0 ? String(Number(value) || 0) : '';

export const createPriceDraft = (model: string, price?: ModelPrice): PriceDraft => ({
  model,
  prompt: price ? createConfiguredDraftValue(price.prompt, price.promptConfigured) : '',
  completion: price
    ? createConfiguredDraftValue(price.completion, price.completionConfigured)
    : '',
  cache: price ? String(price.cache) : '',
  cacheRead: price
    ? createConfiguredDraftValue(price.cacheRead, price.cacheReadConfigured)
    : '',
  cacheCreation: price
    ? createConfiguredDraftValue(price.cacheCreation, price.cacheCreationConfigured)
    : '',
});

export const parsePriceValue = (value: string) => {
  const parsed = Number.parseFloat(value);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : 0;
};

export const buildPriceFromDraft = (draft: PriceDraft): ModelPrice | null => {
  const model = draft.model.trim();
  if (!model) return null;
  const prompt = parsePriceValue(draft.prompt);
  const completion = parsePriceValue(draft.completion);
  const cache = draft.cache.trim() === '' ? prompt : parsePriceValue(draft.cache);
  return {
    prompt,
    completion,
    cache,
    cacheRead: parsePriceValue(draft.cacheRead),
    cacheCreation: parsePriceValue(draft.cacheCreation),
    promptConfigured: draft.prompt.trim() !== '',
    completionConfigured: draft.completion.trim() !== '',
    cacheReadConfigured: draft.cacheRead.trim() !== '',
    cacheCreationConfigured: draft.cacheCreation.trim() !== '',
    source: 'manual',
  };
};

export const applyCandidatePrice = (
  prices: Record<string, ModelPrice>,
  model: string,
  candidate: ModelPriceSyncCandidate
): Record<string, ModelPrice> => ({
  ...prices,
  [model]: {
    ...candidate.price,
    source: candidate.price.source || 'sync',
    sourceModelId: candidate.sourceModelId,
  },
});

export const buildSyncPriceModelsFromSummary = (
  summary: ModelPriceUsageSummaryResponse | null,
  prices: Record<string, ModelPrice>
) => {
  const models = new Set<string>(Object.keys(prices));
  summary?.models?.forEach((item) => {
    if (item.model) models.add(item.model);
  });
  return Array.from(models)
    .filter(Boolean)
    .sort((left, right) => left.localeCompare(right));
};

export const buildCandidateMap = (candidateSets: ModelPriceSyncCandidateSet[] = []) => {
  const map = new Map<string, ModelPriceSyncCandidate[]>();
  candidateSets.forEach((set) => {
    if (!set.model || !Array.isArray(set.candidates) || set.candidates.length === 0) return;
    map.set(set.model, set.candidates);
  });
  return map;
};

export const buildModelPriceRows = (
  summary: ModelPriceUsageSummaryResponse | null,
  prices: Record<string, ModelPrice>,
  candidateSets: ModelPriceSyncCandidateSet[] = []
): ModelPriceRow[] => {
  const rowMap = new Map<string, ModelPriceRow>();
  const candidateMap = buildCandidateMap(candidateSets);

  const ensureRow = (model: string): ModelPriceRow => {
    const existing = rowMap.get(model);
    if (existing) return existing;
    const price = prices[model];
    const row: ModelPriceRow = {
      model,
      calls: 0,
      requestedCalls: 0,
      resolvedCalls: 0,
      hasPrice: Boolean(price),
      price,
      candidateCount: candidateMap.get(model)?.length ?? 0,
    };
    rowMap.set(model, row);
    return row;
  };

  Object.keys(prices).forEach(ensureRow);
  candidateMap.forEach((_candidates, model) => ensureRow(model));

  summary?.models?.forEach((item) => {
    if (!item.model) return;
    const row = ensureRow(item.model);
    row.calls += Number(item.calls) || 0;
    row.requestedCalls += Number(item.requested_calls) || 0;
    row.resolvedCalls += Number(item.resolved_calls) || 0;
  });

  return Array.from(rowMap.values()).sort(
    (left, right) =>
      Number(left.hasPrice) - Number(right.hasPrice) ||
      right.candidateCount - left.candidateCount ||
      right.calls - left.calls ||
      left.model.localeCompare(right.model)
  );
};

export const buildModelPriceSummary = (rows: ModelPriceRow[]): ModelPriceSummary => {
  const saved = rows.filter((row) => row.hasPrice).length;
  const candidates = rows.filter((row) => !row.hasPrice && row.candidateCount > 0).length;
  return {
    total: rows.length,
    saved,
    missing: rows.length - saved,
    candidates,
  };
};

export const filterModelPriceRows = (
  rows: ModelPriceRow[],
  filter: ModelPriceFilter,
  search: string
) => {
  const query = search.trim().toLowerCase();
  return rows.filter((row) => {
    if (filter === 'missing' && row.hasPrice) return false;
    if (filter === 'saved' && !row.hasPrice) return false;
    if (filter === 'candidates' && (row.hasPrice || row.candidateCount === 0)) return false;
    if (!query) return true;
    return (
      row.model.toLowerCase().includes(query) ||
      row.price?.sourceModelId?.toLowerCase().includes(query) ||
      row.price?.source?.toLowerCase().includes(query)
    );
  });
};

export const formatPriceUnit = (value: number | undefined) => {
  const num = Number(value);
  return Number.isFinite(num) ? `$${num.toFixed(4)}/1M` : '--';
};
