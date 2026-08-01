import { modelsApi } from '@/services/api';
import type { GeminiKeyConfig, OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import { normalizeAuthIndex } from '@/utils/authIndex';
import { hasHeader } from '@/utils/headers';
import { maskApiKey } from '@/utils/format';
import type { ProviderKind, ProviderRow } from '../ProviderTable/rowData';
import { PROVIDER_KIND_LABELS } from '../ProviderTable/kindMeta';

export type ProviderHealthCheckStatus = 'pending' | 'running' | 'success' | 'error';
export type ProviderHealthCheckApplyAction = 'enable' | 'disable';

export interface ProviderHealthCheckItem {
  id: string;
  providerKey: string;
  providerKind: ProviderKind;
  providerIndex: number;
  providerLabel: string;
  providerSubtitle: string;
  targetLabel: string;
  targetLabelKey?: string;
  targetLabelValues?: Record<string, string | number>;
  detailLabel: string;
  detailLabelKey?: string;
  detailLabelValues?: Record<string, string | number>;
  baseUrl: string;
  status: ProviderHealthCheckStatus;
  message: string;
  messageKey?: string;
  messageValues?: Record<string, string | number>;
  modelCount?: number;
  durationMs?: number;
  openAIKeyIndex?: number;
}

export interface ProviderHealthCheckSummary {
  total: number;
  pending: number;
  running: number;
  success: number;
  error: number;
  completed: number;
  percent: number;
}

type ProviderHealthCheckTarget =
  | { kind: 'gemini' | 'interactions'; config: GeminiKeyConfig }
  | { kind: 'codex' | 'xai' | 'claude' | 'vertex'; config: ProviderKeyConfig }
  | { kind: 'openai'; config: OpenAIProviderConfig; keyIndex: number };

const EMPTY_MODELS_ERROR = 'No models returned';
const VERTEX_STANDARD_MODELS_ERROR = 'No standard model discovery endpoint responded';

class HealthCheckError extends Error {
  messageKey: string;
  messageValues?: Record<string, string | number>;

  constructor(
    message: string,
    messageKey: string,
    messageValues?: Record<string, string | number>
  ) {
    super(message);
    this.messageKey = messageKey;
    this.messageValues = messageValues;
  }
}

const getErrorMessage = (err: unknown): string => {
  if (err instanceof Error) return err.message;
  if (typeof err === 'string') return err;
  return '';
};

const getHealthCheckErrorPayload = (
  err: unknown
): Pick<ProviderHealthCheckItem, 'message' | 'messageKey' | 'messageValues'> => {
  if (err instanceof HealthCheckError) {
    const payload: Pick<ProviderHealthCheckItem, 'message' | 'messageKey' | 'messageValues'> = {
      message: err.message,
      messageKey: err.messageKey,
    };
    if (err.messageValues) payload.messageValues = err.messageValues;
    return payload;
  }

  return { message: getErrorMessage(err) || 'Unknown error' };
};

const getKeyLabel = (
  apiKey?: string,
  authIndex?: string
): Pick<ProviderHealthCheckItem, 'targetLabel' | 'targetLabelKey' | 'targetLabelValues'> => {
  const masked = maskApiKey(apiKey ?? '');
  return masked
    ? { targetLabel: masked }
    : {
        targetLabel: normalizeAuthIndex(authIndex) ? 'Credential' : 'No credential',
        targetLabelKey: normalizeAuthIndex(authIndex)
          ? 'ai_providers.health_check_configured_credential'
          : 'ai_providers.health_check_no_credential',
      };
};

const getCredentialDetailLabel = (
  apiKey?: string,
  authIndex?: string
): Pick<ProviderHealthCheckItem, 'detailLabel' | 'detailLabelKey' | 'detailLabelValues'> => {
  const normalizedAuthIndex = normalizeAuthIndex(authIndex);
  if (normalizedAuthIndex) {
    return {
      detailLabel: `auth-index: ${normalizedAuthIndex}`,
      detailLabelKey: 'ai_providers.health_check_auth_index_label',
      detailLabelValues: { index: normalizedAuthIndex },
    };
  }

  const masked = maskApiKey(apiKey ?? '');
  return masked
    ? { detailLabel: masked }
    : {
        detailLabel: 'No credential',
        detailLabelKey: 'ai_providers.health_check_no_credential',
      };
};

const getHostLabel = (baseUrl?: string): string => {
  const trimmed = String(baseUrl ?? '').trim();
  if (!trimmed) return '';

  try {
    return new URL(trimmed).host;
  } catch {
    return trimmed
      .replace(/^[a-z][a-z0-9+.-]*:\/\//i, '')
      .replace(/\/.*$/g, '')
      .trim();
  }
};

const joinProviderLabel = (kindLabel: string, identity: string): string => {
  const trimmedIdentity = identity.trim();
  return trimmedIdentity && trimmedIdentity !== kindLabel
    ? `${kindLabel} · ${trimmedIdentity}`
    : kindLabel;
};

const getKeyProviderDisplay = (
  row: Extract<
    ProviderRow,
    { kind: 'gemini' | 'interactions' | 'codex' | 'xai' | 'claude' | 'vertex' }
  >
): Pick<ProviderHealthCheckItem, 'providerLabel' | 'providerSubtitle'> => {
  const kindLabel = PROVIDER_KIND_LABELS[row.kind];
  const identity =
    String(row.raw.prefix ?? '').trim() ||
    maskApiKey(row.raw.apiKey) ||
    getHostLabel(row.baseUrl) ||
    kindLabel;
  return {
    providerLabel: joinProviderLabel(kindLabel, identity),
    providerSubtitle: row.baseUrl,
  };
};

const getOpenAIProviderDisplay = (
  row: Extract<ProviderRow, { kind: 'openai' }>
): Pick<ProviderHealthCheckItem, 'providerLabel' | 'providerSubtitle'> => {
  const kindLabel = PROVIDER_KIND_LABELS.openai;
  const identity = String(row.label ?? '').trim() || getHostLabel(row.baseUrl) || kindLabel;
  return {
    providerLabel: joinProviderLabel(kindLabel, identity),
    providerSubtitle: row.baseUrl,
  };
};

const getConfiguredModelNames = (models?: Array<{ name: string }>): string[] =>
  (models ?? []).map((model) => String(model?.name ?? '').trim()).filter(Boolean);

const requireCredential = (
  apiKey?: string,
  authIndex?: string,
  headers?: Record<string, string>
) => {
  if (String(apiKey ?? '').trim()) return;
  if (normalizeAuthIndex(authIndex)) return;
  if (hasHeader(headers, 'authorization')) return;
  if (hasHeader(headers, 'x-api-key')) return;
  if (hasHeader(headers, 'x-goog-api-key')) return;
  throw new HealthCheckError(
    'Missing API key or auth-index',
    'ai_providers.health_check_error_missing_credential'
  );
};

const buildKeyProviderItem = (
  row: Extract<
    ProviderRow,
    { kind: 'gemini' | 'interactions' | 'codex' | 'xai' | 'claude' | 'vertex' }
  >
): ProviderHealthCheckItem => {
  const providerDisplay = getKeyProviderDisplay(row);
  const keyLabel = getKeyLabel(row.raw.apiKey, row.raw.authIndex);
  const credentialDetail = getCredentialDetailLabel(row.raw.apiKey, row.raw.authIndex);
  return {
    id: `${row.key}:key`,
    providerKey: row.key,
    providerKind: row.kind,
    providerIndex: row.originalIndex,
    providerLabel: providerDisplay.providerLabel,
    providerSubtitle: providerDisplay.providerSubtitle,
    targetLabel: keyLabel.targetLabel,
    targetLabelKey: keyLabel.targetLabelKey,
    targetLabelValues: keyLabel.targetLabelValues,
    detailLabel: credentialDetail.detailLabel,
    detailLabelKey: credentialDetail.detailLabelKey,
    detailLabelValues: credentialDetail.detailLabelValues,
    baseUrl: row.baseUrl,
    status: 'pending',
    message: '',
  };
};

const buildOpenAIProviderItems = (
  row: Extract<ProviderRow, { kind: 'openai' }>
): ProviderHealthCheckItem[] => {
  const providerDisplay = getOpenAIProviderDisplay(row);
  const entries = row.raw.apiKeyEntries ?? [];
  if (!entries.length) {
    return [
      {
        id: `${row.key}:empty`,
        providerKey: row.key,
        providerKind: 'openai',
        providerIndex: row.originalIndex,
        providerLabel: providerDisplay.providerLabel,
        providerSubtitle: providerDisplay.providerSubtitle,
        targetLabel: 'Key #1',
        targetLabelKey: 'ai_providers.health_check_key_index',
        targetLabelValues: { index: 1 },
        detailLabel: 'No key entries',
        detailLabelKey: 'ai_providers.health_check_no_key_entries',
        baseUrl: row.baseUrl,
        status: 'pending',
        message: '',
        openAIKeyIndex: 0,
      },
    ];
  }

  return entries.map((entry, keyIndex) => {
    const credentialDetail = getCredentialDetailLabel(entry.apiKey, entry.authIndex);
    return {
      id: `${row.key}:key:${keyIndex}`,
      providerKey: row.key,
      providerKind: 'openai',
      providerIndex: row.originalIndex,
      providerLabel: providerDisplay.providerLabel,
      providerSubtitle: providerDisplay.providerSubtitle,
      targetLabel: `Key #${keyIndex + 1}`,
      targetLabelKey: 'ai_providers.health_check_key_index',
      targetLabelValues: { index: keyIndex + 1 },
      detailLabel: credentialDetail.detailLabel,
      detailLabelKey: credentialDetail.detailLabelKey,
      detailLabelValues: credentialDetail.detailLabelValues,
      baseUrl: row.baseUrl,
      status: 'pending',
      message: '',
      openAIKeyIndex: keyIndex,
    };
  });
};

export const buildProviderHealthCheckItems = (rows: ProviderRow[]): ProviderHealthCheckItem[] =>
  rows.flatMap((row) => {
    if (row.kind === 'openai') return buildOpenAIProviderItems(row);
    return [buildKeyProviderItem(row)];
  });

export const summarizeProviderHealthCheckItems = (
  items: ProviderHealthCheckItem[]
): ProviderHealthCheckSummary => {
  const summary = items.reduce(
    (acc, item) => {
      acc[item.status] += 1;
      return acc;
    },
    { pending: 0, running: 0, success: 0, error: 0 }
  );
  const total = items.length;
  const completed = summary.success + summary.error;
  const percent = total > 0 ? Math.round((completed / total) * 100) : 0;
  return {
    total,
    pending: summary.pending,
    running: summary.running,
    success: summary.success,
    error: summary.error,
    completed,
    percent,
  };
};

export const getProviderHealthCheckApplyActions = (
  items: ProviderHealthCheckItem[]
): Map<string, ProviderHealthCheckApplyAction> => {
  const grouped = new Map<string, ProviderHealthCheckItem[]>();
  items.forEach((item) => {
    const existing = grouped.get(item.providerKey) ?? [];
    existing.push(item);
    grouped.set(item.providerKey, existing);
  });

  const actions = new Map<string, ProviderHealthCheckApplyAction>();
  grouped.forEach((groupItems, providerKey) => {
    const hasSuccess = groupItems.some((item) => item.status === 'success');
    const hasCompleted = groupItems.some(
      (item) => item.status === 'success' || item.status === 'error'
    );
    if (!hasCompleted) return;
    actions.set(providerKey, hasSuccess ? 'enable' : 'disable');
  });
  return actions;
};

const getTargetForItem = (
  rows: ProviderRow[],
  item: ProviderHealthCheckItem
): ProviderHealthCheckTarget | null => {
  const row = rows.find((candidate) => candidate.key === item.providerKey);
  if (!row) return null;
  if (row.kind === 'openai') {
    const keyIndex = item.openAIKeyIndex ?? 0;
    return { kind: 'openai', config: row.raw, keyIndex };
  }
  return { kind: row.kind, config: row.raw };
};

const ensureNonEmptyModels = (models: Array<{ name: string }>) => {
  if (models.length === 0) {
    throw new HealthCheckError(EMPTY_MODELS_ERROR, 'ai_providers.health_check_error_empty_models');
  }
  return models.length;
};

const testVertexByStandardModelsEndpoints = async (config: ProviderKeyConfig): Promise<number> => {
  const errors: string[] = [];
  try {
    const models = await modelsApi.fetchV1ModelsViaApiCall(
      config.baseUrl ?? '',
      config.apiKey?.trim() || undefined,
      config.headers ?? {},
      normalizeAuthIndex(config.authIndex) ?? undefined
    );
    return ensureNonEmptyModels(models);
  } catch (err) {
    errors.push(getErrorMessage(err));
  }

  try {
    const models = await modelsApi.fetchModelsViaApiCall(
      config.baseUrl ?? '',
      config.apiKey?.trim() || undefined,
      config.headers ?? {},
      normalizeAuthIndex(config.authIndex) ?? undefined
    );
    return ensureNonEmptyModels(models);
  } catch (err) {
    errors.push(getErrorMessage(err));
  }

  const reason = errors.filter(Boolean).join(' / ');
  throw new HealthCheckError(
    `${VERTEX_STANDARD_MODELS_ERROR}${reason ? `: ${reason}` : ''}`,
    'ai_providers.health_check_error_vertex_standard_models',
    { reason: reason ? `: ${reason}` : '' }
  );
};

export const runProviderHealthCheckItem = async (
  rows: ProviderRow[],
  item: ProviderHealthCheckItem
): Promise<ProviderHealthCheckItem> => {
  const startedAt = Date.now();
  const target = getTargetForItem(rows, item);
  if (!target) {
    return {
      ...item,
      status: 'error',
      message: 'Provider no longer exists',
      messageKey: 'ai_providers.health_check_error_provider_missing',
      durationMs: Date.now() - startedAt,
    };
  }

  try {
    let modelCount = 0;
    if (target.kind === 'gemini' || target.kind === 'interactions') {
      requireCredential(target.config.apiKey, target.config.authIndex, target.config.headers);
      const models = await modelsApi.fetchGeminiModelsViaApiCall(
        target.config.baseUrl ?? '',
        target.config.apiKey?.trim() || undefined,
        target.config.headers ?? {},
        normalizeAuthIndex(target.config.authIndex) ?? undefined
      );
      modelCount = ensureNonEmptyModels(models);
    } else if (target.kind === 'codex' || target.kind === 'xai') {
      requireCredential(target.config.apiKey, target.config.authIndex, target.config.headers);
      const hasCustomAuthorization = hasHeader(target.config.headers, 'authorization');
      const models = await modelsApi.fetchV1ModelsViaApiCall(
        target.config.baseUrl ?? '',
        hasCustomAuthorization ? undefined : target.config.apiKey?.trim() || undefined,
        target.config.headers ?? {},
        normalizeAuthIndex(target.config.authIndex) ?? undefined
      );
      modelCount = ensureNonEmptyModels(models);
    } else if (target.kind === 'claude') {
      requireCredential(target.config.apiKey, target.config.authIndex, target.config.headers);
      const models = await modelsApi.fetchClaudeModelsViaApiCall(
        target.config.baseUrl ?? '',
        target.config.apiKey?.trim() || undefined,
        target.config.headers ?? {},
        normalizeAuthIndex(target.config.authIndex) ?? undefined
      );
      modelCount = ensureNonEmptyModels(models);
    } else if (target.kind === 'vertex') {
      requireCredential(target.config.apiKey, target.config.authIndex, target.config.headers);
      modelCount = await testVertexByStandardModelsEndpoints(target.config);
    } else if (target.kind === 'openai') {
      const entry = target.config.apiKeyEntries?.[target.keyIndex];
      const authIndex =
        normalizeAuthIndex(entry?.authIndex ?? target.config.authIndex) ?? undefined;
      requireCredential(entry?.apiKey, authIndex, {
        ...(target.config.headers ?? {}),
        ...(entry?.headers ?? {}),
      });
      const headers = { ...(target.config.headers ?? {}), ...(entry?.headers ?? {}) };
      const hasAuthHeader = hasHeader(headers, 'authorization');
      const models = await modelsApi.fetchModelsViaApiCall(
        target.config.baseUrl ?? '',
        hasAuthHeader ? undefined : entry?.apiKey?.trim() || undefined,
        headers,
        authIndex
      );
      modelCount = ensureNonEmptyModels(models);
    }

    return {
      ...item,
      status: 'success',
      modelCount,
      durationMs: Date.now() - startedAt,
      message: 'OK',
    };
  } catch (err) {
    const configuredModelCount = getConfiguredModelNames(target.config.models).length;
    const errorPayload = getHealthCheckErrorPayload(err);
    return {
      ...item,
      status: 'error',
      modelCount: configuredModelCount || undefined,
      durationMs: Date.now() - startedAt,
      ...errorPayload,
    };
  }
};
