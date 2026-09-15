import type { ModelPrice, ModelPriceContextTier, ModelPriceServiceTier } from '@/utils/usage';
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

export type ModelPriceCandidateGroup = {
  source: string;
  candidates: ModelPriceSyncCandidate[];
};

export type ModelPriceModalities = {
  input: string[];
  output: string[];
};

const MAX_MODEL_PRICE_METADATA_CHARS = 128 * 1024;
const MAX_MODALITY_ITEMS = 8;
const MAX_MODALITY_LENGTH = 48;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

const normalizeModalities = (value: unknown): string[] => {
  if (!Array.isArray(value)) return [];

  const modalities: string[] = [];
  const seen = new Set<string>();
  for (const item of value) {
    if (typeof item !== 'string') continue;
    const modality = item.trim().replace(/\s+/g, ' ').toLowerCase();
    if (!modality || modality.length > MAX_MODALITY_LENGTH || seen.has(modality)) continue;
    seen.add(modality);
    modalities.push(modality);
    if (modalities.length >= MAX_MODALITY_ITEMS) break;
  }
  return modalities;
};

const firstExplicitModalities = (...values: unknown[]): string[] => {
  for (const value of values) {
    const modalities = normalizeModalities(value);
    if (modalities.length > 0) return modalities;
  }
  return [];
};

/**
 * Reads only directional modality arrays explicitly declared by a synced model
 * catalog. Catalog capabilities are not request measurements and are never
 * used as a token or cost input.
 */
export const isCatalogSynchronizedModelPrice = (
  price: Pick<ModelPrice, 'source' | 'syncedAtMs'> | undefined
): boolean =>
  Boolean(price?.syncedAtMs && ['models.dev', 'openrouter'].includes(price.source?.trim() ?? ''));

export const extractModelPriceModalities = (rawJson?: string): ModelPriceModalities | null => {
  const raw = rawJson?.trim();
  if (!raw || raw.length > MAX_MODEL_PRICE_METADATA_CHARS) return null;

  try {
    const parsed: unknown = JSON.parse(raw);
    if (!isRecord(parsed)) return null;
    const architecture = isRecord(parsed.architecture) ? parsed.architecture : undefined;
    const modalities = isRecord(parsed.modalities) ? parsed.modalities : undefined;
    const input = firstExplicitModalities(
      architecture?.input_modalities,
      modalities?.input,
      parsed.input_modalities,
      parsed.inputModalities,
      parsed['input-modalities']
    );
    const output = firstExplicitModalities(
      architecture?.output_modalities,
      modalities?.output,
      parsed.output_modalities,
      parsed.outputModalities,
      parsed['output-modalities']
    );
    return input.length > 0 || output.length > 0 ? { input, output } : null;
  } catch {
    return null;
  }
};

export const hasNonTextModelPriceModality = (modalities: ModelPriceModalities | null): boolean =>
  Boolean(
    modalities &&
      [...modalities.input, ...modalities.output].some((modality) => modality !== 'text')
  );

export const formatModelPriceModalities = (
  modalities: ModelPriceModalities,
  labels: { input: string; output: string } = { input: 'In', output: 'Out' }
): string => {
  const sections: string[] = [];
  if (modalities.input.length > 0) sections.push(`${labels.input}: ${modalities.input.join(', ')}`);
  if (modalities.output.length > 0) sections.push(`${labels.output}: ${modalities.output.join(', ')}`);
  return sections.join(' · ');
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
  completion: price ? createConfiguredDraftValue(price.completion, price.completionConfigured) : '',
  cache: price ? String(price.cache) : '',
  cacheRead: price ? createConfiguredDraftValue(price.cacheRead, price.cacheReadConfigured) : '',
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
    contextTiers: [],
    serviceTiers: [],
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

export const getModelPriceCandidateSource = (candidate: ModelPriceSyncCandidate) =>
  candidate.price.source?.trim() || 'sync';

export const getModelPriceCandidateIdentity = (candidate: ModelPriceSyncCandidate) =>
  JSON.stringify([getModelPriceCandidateSource(candidate), candidate.sourceModelId]);

export const groupModelPriceCandidatesBySource = (
  candidates: ModelPriceSyncCandidate[]
): ModelPriceCandidateGroup[] => {
  const groups = new Map<string, ModelPriceSyncCandidate[]>();
  candidates.forEach((candidate) => {
    const source = getModelPriceCandidateSource(candidate);
    const group = groups.get(source);
    if (group) {
      group.push(candidate);
      return;
    }
    groups.set(source, [candidate]);
  });
  return Array.from(groups, ([source, sourceCandidates]) => ({
    source,
    candidates: sourceCandidates,
  }));
};

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

export const resolveContextTierDisplayPrice = (
  price: ModelPrice | undefined,
  tier: ModelPriceContextTier
) => ({
  prompt: tier.promptConfigured ? tier.prompt : price?.prompt,
  completion: tier.completionConfigured ? tier.completion : price?.completion,
});

export const resolveServiceTierDisplayPrice = (
  price: ModelPrice | undefined,
  tier: ModelPriceServiceTier
) => ({
  prompt: tier.promptConfigured ? tier.prompt : price?.prompt,
  completion: tier.completionConfigured ? tier.completion : price?.completion,
});

export const formatServiceTierRule = (tier: ModelPriceServiceTier) => {
  const mode = tier.mode.trim();
  const serviceTier = tier.serviceTier.trim();
  if (!mode) return serviceTier || '--';
  if (!serviceTier || mode.toLowerCase() === serviceTier.toLowerCase()) return mode;
  return `${mode}/${serviceTier}`;
};

export const formatContextThreshold = (value: number) => {
  const tokens = Number(value);
  if (!Number.isFinite(tokens) || tokens <= 0) return '--';
  if (tokens >= 1_000_000 && tokens % 1_000_000 === 0) return `${tokens / 1_000_000}M`;
  if (tokens >= 1_000 && tokens % 1_000 === 0) return `${tokens / 1_000}K`;
  return tokens.toLocaleString('en-US', { maximumFractionDigits: 0 });
};
