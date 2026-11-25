import { useAuthStore as useDiscoveryAuthStore } from '@/stores/useAuthStore';
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  apiCallApi,
  getApiCallErrorMessage,
  modelsApi,
  providersApi,
  readBackClaudeConfigAfterSave,
  verifyClaudeFingerprintInRawConfig,
  type ApiCallResult,
} from '@/services/api';
import { useConfigStore, useNotificationStore } from '@/stores';
import {
  coolingPolicyFromOverride,
  coolingPolicyToOverride,
  toCommittedModelThinkingSnapshot,
  type ApiKeyEntry,
  type ClaudeFingerprintProfile,
  type CloakConfig,
  type CoolingPolicy,
  type GeminiKeyConfig,
  type ModelAlias,
  type NotificationType,
  type OpenAIProviderConfig,
  type ProviderKeyConfig,
  type ProviderV8GroupSource,
} from '@/types';
import { normalizeAuthIndex } from '@/utils/authIndex';
import { buildClaudeRequestHeaders } from '@/utils/claudeAuth';
import {
  areKeyValueEntriesEqual,
  areModelEntriesEqual,
  areStringArraysEqual,
} from '@/utils/compare';
import {
  getCredentialWeightComparisonValue,
  getCredentialWeightError,
  normalizeCredentialWeight,
  type CredentialWeightComparisonValue,
  type CredentialWeightInputValue,
} from '@/utils/credentialWeight';
import {
  buildHeaderObject,
  hasHeader,
  headersToEntries,
  normalizeHeaderEntries,
  type HeaderEntry,
} from '@/utils/headers';
import type { ModelInfo } from '@/utils/models';
import {
  getOpenAIModelDiscoveryEntry,
  getOpenAITestableKeyIndexes,
  hasOpenAIKeyEntryConfiguration,
  preservePersistedOpenAIKeyEntry,
} from '@/utils/openAIKeyEntries';
import {
  cloneModelEntry,
  entriesToModels,
  hasInvalidThinkingLevels,
  modelsToEntries,
} from '@/components/ui/modelInputListUtils';
import type { ModelEntry, OpenAIFormApiKeyEntry } from '../types';
import type { ProviderKind, ProviderRow } from '../ProviderTable/rowData';
import {
  buildApiKeyEntry,
  buildClaudeMessagesEndpoint,
  buildCodexResponsesEndpoint,
  buildOpenAIChatCompletionsEndpoint,
  buildOpenAIModelsEndpoint,
  excludedModelsToText,
  parseExcludedModels,
  stripDisableAllModelsRule,
  toCommittedOpenAIProviderSnapshot,
  withDisableAllModelsRule,
} from '../utils';
import type { ProviderSavedIdentityHint } from './ProviderWorkspaceDrawer';

export type ProviderMutationLockRunner = <T>(
  key: string,
  task: () => Promise<T>
) => Promise<{ executed: true; value: T } | { executed: false }>;

export interface ProviderDrawerFormState {
  name: string;
  v8Source?: ProviderV8GroupSource;
  apiKey: string;
  authIndex: string;
  priority?: number;
  weight?: CredentialWeightInputValue;
  prefix: string;
  baseUrl: string;
  proxyUrl: string;
  websockets: boolean;
  disableCooling: CoolingPolicy;
  fingerprintProfile?: ClaudeFingerprintProfile;
  rebuildMidSystemMessage: boolean;
  cloak?: CloakConfig;
  cloakSensitiveWordsInput: string;
  headers: HeaderEntry[];
  modelEntries: ModelEntry[];
  excludedText: string;
  apiKeyEntries: OpenAIFormApiKeyEntry[];
  testModel: string;
}

export type ProviderConnectivityProbeMode = 'inference' | 'models';

export interface ProviderConnectivityAuthDiagnostic {
  headerType: 'x-api-key' | 'authorization-bearer' | 'none';
  credentialSource: 'api-key' | 'auth-index' | 'custom-header' | 'none';
  endpointType: 'anthropic' | 'custom';
}

export interface ProviderDrawerConnectivityResult {
  status: 'idle' | 'loading' | 'success' | 'error';
  statusCode?: number;
  elapsedMs?: number;
  message: string;
  endpoint?: string;
  responseBody?: string;
  authDiagnostic?: ProviderConnectivityAuthDiagnostic;
}

export interface ProviderOpenAIKeyTestEntry {
  status: 'idle' | 'loading' | 'success' | 'error';
  statusCode?: number;
  elapsedMs?: number;
  message: string;
  responseBody?: string;
}
export interface UseProviderDrawerEditorOptions {
  open: boolean;
  mode: 'view-edit' | 'create';
  row: ProviderRow | null;
  activeKind: ProviderKind;
  v8AliasSupported?: boolean;
  actionsDisabled: boolean;
  rowDeletedExternally?: boolean;
  sessionIdentity?: string;
  onClose: () => void;
  onSaved: (
    kind: ProviderKind,
    savedIdentity?: ProviderSavedIdentityHint
  ) => Promise<boolean | void> | boolean | void;
  runWithMutationLock?: ProviderMutationLockRunner;
}

const EMPTY_DISCOVERED_MODELS: ModelInfo[] = [];
const TEST_TIMEOUT_MS = 30_000;
const CODEX_TEST_TIMEOUT_MS = 20_000;
const fallbackMutationLocks = new Set<string>();

const PROTOCOL_DEFAULT_TEST_MODELS: Record<ProviderKind, string> = {
  openai: 'gpt-4o-mini',
  claude: 'claude-sonnet-4-5',
  codex: 'gpt-5-codex',
  xai: 'grok-3-mini',
  gemini: 'gemini-2.5-flash',
  interactions: 'gemini-2.5-flash',
  vertex: 'gemini-2.5-flash',
};

const extractCleanApiErrorSummary = (result: ApiCallResult): string => {
  const raw = getApiCallErrorMessage(result);
  if (result.statusCode > 0) {
    const stripped = raw.replace(new RegExp(`^${result.statusCode}\\s+`), '').trim();
    if (stripped) return stripped;
  }
  return raw;
};

const extractFormattedResponseBody = (result: ApiCallResult): string | undefined => {
  const rawText = result.bodyText.trim();
  if (!rawText) {
    if (result.body !== null && typeof result.body === 'object') {
      try {
        return JSON.stringify(result.body, null, 2);
      } catch {
        return undefined;
      }
    }
    return undefined;
  }
  try {
    return JSON.stringify(JSON.parse(rawText), null, 2);
  } catch {
    return rawText;
  }
};

const CONFIG_SECTION_BY_KIND = {
  gemini: 'gemini-api-key',
  interactions: 'interactions-api-key',
  codex: 'codex-api-key',
  xai: 'xai-api-key',
  claude: 'claude-api-key',
  vertex: 'vertex-api-key',
  openai: 'openai-compatibility',
} as const;

const stripGeminiModelResourceName = (value: string): string =>
  String(value ?? '')
    .trim()
    .replace(/^\/?models\//i, '');

export const parseSensitiveWordsText = (text: string): string[] =>
  text
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter(Boolean);

type PreservedModelExtras = Pick<
  ModelAlias,
  | 'displayName'
  | 'maxContextLength'
  | 'isCompat'
  | 'openapi'
  | 'supportConfigurationUpdate'
  | 'useMaxCompletionTokens'
>;

type ProviderModelEntry = ModelEntry & PreservedModelExtras;

const copyPreservedModelExtras = (source: Partial<PreservedModelExtras>): PreservedModelExtras => ({
  ...(source.displayName !== undefined ? { displayName: source.displayName } : {}),
  ...(source.maxContextLength !== undefined ? { maxContextLength: source.maxContextLength } : {}),
  ...(source.isCompat !== undefined ? { isCompat: source.isCompat } : {}),
  ...(source.openapi !== undefined ? { openapi: source.openapi } : {}),
  ...(source.supportConfigurationUpdate !== undefined
    ? { supportConfigurationUpdate: source.supportConfigurationUpdate }
    : {}),
  ...(source.useMaxCompletionTokens !== undefined
    ? { useMaxCompletionTokens: source.useMaxCompletionTokens }
    : {}),
});

const providerModelsToEntries = (models?: ModelAlias[]): ProviderModelEntry[] => {
  const entries = modelsToEntries(models);
  if (!Array.isArray(models) || models.length === 0) return entries;
  return entries.map((entry, idx) => {
    const rawModel = models[idx];
    return rawModel ? { ...cloneModelEntry(entry), ...copyPreservedModelExtras(rawModel) } : entry;
  });
};

const entriesToProviderModels = (
  entries: ModelEntry[],
  options?: { preserveAliasForSameName?: boolean }
): ModelAlias[] => {
  const validEntries = entries.filter((entry) => entry.name.trim());
  const baseModels = entriesToModels(validEntries);
  return baseModels.map((model, idx) => {
    const entry = validEntries[idx] as ProviderModelEntry | undefined;
    const extras = entry ? copyPreservedModelExtras(entry) : {};
    const rawAlias = entry?.alias?.trim();
    const nextModel: ModelAlias = {
      ...model,
      ...extras,
    };
    if (options?.preserveAliasForSameName && !nextModel.alias) {
      nextModel.alias = rawAlias || nextModel.name;
    }
    return nextModel;
  });
};

const buildEmptyForm = (kind: ProviderKind): ProviderDrawerFormState => ({
  name: '',
  v8Source: undefined,
  apiKey: '',
  authIndex: '',
  priority: undefined,
  weight: undefined,
  prefix: '',
  baseUrl: '',
  proxyUrl: '',
  websockets: false,
  disableCooling: 'inherit',
  fingerprintProfile: undefined,
  rebuildMidSystemMessage: false,
  cloak: undefined,
  cloakSensitiveWordsInput: '',
  headers: [],
  modelEntries: [{ name: '', alias: '' }],
  excludedText: '',
  apiKeyEntries: kind === 'openai' ? [buildApiKeyEntry()] : [],
  testModel: '',
});

const buildFormFromRow = (row: ProviderRow): ProviderDrawerFormState => {
  if (row.kind === 'openai') {
    const raw = row.raw;
    return {
      name: raw.name ?? '',
      v8Source: raw.v8Source,
      apiKey: '',
      authIndex: raw.authIndex ?? '',
      priority: raw.priority,
      weight: undefined,
      prefix: raw.prefix ?? '',
      baseUrl: raw.baseUrl ?? '',
      proxyUrl: '',
      websockets: false,
      disableCooling: coolingPolicyFromOverride(raw.disableCooling),
      fingerprintProfile: undefined,
      rebuildMidSystemMessage: false,
      cloak: undefined,
      cloakSensitiveWordsInput: '',
      headers: headersToEntries(raw.headers),
      modelEntries: providerModelsToEntries(raw.models),
      excludedText: '',
      apiKeyEntries: raw.apiKeyEntries?.length ? raw.apiKeyEntries : [buildApiKeyEntry()],
      testModel: raw.testModel ?? '',
    };
  }

  const raw = row.raw;
  const isGeminiLike = row.kind === 'gemini' || row.kind === 'interactions';
  const rawExtra = raw as ProviderKeyConfig;
  const modelEntries = providerModelsToEntries(raw.models).map((entry) =>
    isGeminiLike ? cloneModelEntry(entry, { name: stripGeminiModelResourceName(entry.name) }) : entry
  );

  return {
    name: raw.name ?? '',
    v8Source: raw.v8Source,
    apiKey: raw.apiKey ?? '',
    authIndex: raw.authIndex ?? '',
    priority: raw.priority,
    weight: raw.weight,
    prefix: raw.prefix ?? '',
    baseUrl: raw.baseUrl ?? '',
    proxyUrl: raw.proxyUrl ?? '',
    websockets: Boolean(rawExtra.websockets),
    disableCooling: coolingPolicyFromOverride(raw.disableCooling),
    fingerprintProfile: rawExtra.fingerprintProfile,
    rebuildMidSystemMessage: Boolean(rawExtra.rebuildMidSystemMessage),
    cloak: rawExtra.cloak
      ? {
          ...rawExtra.cloak,
          sensitiveWords: rawExtra.cloak.sensitiveWords
            ? [...rawExtra.cloak.sensitiveWords]
            : undefined,
        }
      : undefined,
    cloakSensitiveWordsInput: (rawExtra.cloak?.sensitiveWords ?? []).join('\n'),
    headers: headersToEntries(raw.headers),
    modelEntries,
    excludedText: excludedModelsToText(stripDisableAllModelsRule(raw.excludedModels)),
    apiKeyEntries: [],
    testModel: '',
  };
};

const normalizeModelEntriesForKind = (kind: ProviderKind, entries: ModelEntry[]) =>
  (entries ?? []).reduce<ModelEntry[]>((acc, entry) => {
    const rawName = entry?.name ?? '';
    const name =
      kind === 'gemini' || kind === 'interactions'
        ? stripGeminiModelResourceName(rawName)
        : String(rawName).trim();
    let alias = String(entry?.alias ?? '').trim();
    if (kind === 'claude') {
      if (name) alias = alias || name;
    } else if (name && (alias === '' || alias === name)) {
      alias = '';
    }
    if (!name && !alias) return acc;
    const normalized = cloneModelEntry(entry, { name, alias });
    acc.push(kind === 'openai' ? toCommittedModelThinkingSnapshot(normalized) : normalized);
    return acc;
  }, []);

interface NormalizedOpenAIKeyBaselineEntry {
  apiKey: string;
  weight: CredentialWeightComparisonValue;
  proxyUrl: string;
  headers: HeaderEntry[];
}

interface NormalizedCloakBaseline {
  mode: string;
  strictMode: boolean;
  cacheUserId: boolean;
  sensitiveWords: string[] | null;
}

const normalizeKeyHeaders = (headers: ApiKeyEntry['headers']): HeaderEntry[] => {
  if (!headers || typeof headers !== 'object') return [];
  return Object.entries(headers)
    .map(([key, value]) => ({ key: String(key ?? '').trim(), value: String(value ?? '').trim() }))
    .filter((entry) => entry.key || entry.value)
    .sort((a, b) => {
      const byKey = a.key.toLowerCase().localeCompare(b.key.toLowerCase());
      return byKey !== 0 ? byKey : a.value.localeCompare(b.value);
    });
};

const normalizeOpenAIApiKeyEntries = (
  entries: OpenAIFormApiKeyEntry[]
): NormalizedOpenAIKeyBaselineEntry[] =>
  (entries ?? []).reduce<NormalizedOpenAIKeyBaselineEntry[]>((acc, entry) => {
    const apiKey = String(entry?.apiKey ?? '').trim();
    const weight = getCredentialWeightComparisonValue(entry?.weight);
    const proxyUrl = String(entry?.proxyUrl ?? '').trim();
    const headers = normalizeKeyHeaders(entry?.headers);
    if (!hasOpenAIKeyEntryConfiguration(entry) && headers.length === 0) return acc;
    acc.push({ apiKey, weight, proxyUrl, headers });
    return acc;
  }, []);

const areNormalizedOpenAIKeyEntriesEqual = (
  a: NormalizedOpenAIKeyBaselineEntry[],
  b: NormalizedOpenAIKeyBaselineEntry[]
) => {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  for (let index = 0; index < a.length; index += 1) {
    const left = a[index];
    const right = b[index];
    if (!left || !right) return false;
    if (
      left.apiKey !== right.apiKey ||
      left.weight !== right.weight ||
      left.proxyUrl !== right.proxyUrl
    ) {
      return false;
    }
    if (!areKeyValueEntriesEqual(left.headers, right.headers)) return false;
  }
  return true;
};

const normalizeCloakForBaseline = (
  cloak: CloakConfig | undefined
): NormalizedCloakBaseline | null => {
  if (!cloak) return null;
  const mode =
    String(cloak.mode ?? '')
      .trim()
      .toLowerCase() || 'auto';
  const strictMode = Boolean(cloak.strictMode);
  const cacheUserId = Boolean(cloak.cacheUserId);
  const sensitiveWords = Array.isArray(cloak.sensitiveWords)
    ? cloak.sensitiveWords.map((word) => String(word ?? '').trim()).filter(Boolean)
    : [];
  return {
    mode,
    strictMode,
    cacheUserId,
    sensitiveWords: sensitiveWords.length ? sensitiveWords : null,
  };
};

const areCloakBaselinesEqual = (
  left: NormalizedCloakBaseline | null,
  right: NormalizedCloakBaseline | null
) => {
  if (left === right) return true;
  if (!left || !right) return false;
  if (
    left.mode !== right.mode ||
    left.strictMode !== right.strictMode ||
    left.cacheUserId !== right.cacheUserId
  ) {
    return false;
  }
  if (left.sensitiveWords === null || right.sensitiveWords === null) {
    return left.sensitiveWords === right.sensitiveWords;
  }
  return areStringArraysEqual(left.sensitiveWords, right.sensitiveWords);
};

interface ProviderDrawerBaseline {
  v8Source?: ProviderV8GroupSource;
  enabled?: boolean;
  name: string;
  apiKey: string;
  authIndex: string;
  priority: number | null;
  weight: CredentialWeightComparisonValue;
  prefix: string;
  baseUrl: string;
  proxyUrl: string;
  websockets: boolean;
  disableCooling: CoolingPolicy;
  fingerprintProfile: ClaudeFingerprintProfile | undefined;
  rebuildMidSystemMessage: boolean;
  cloak: NormalizedCloakBaseline | null;
  headers: HeaderEntry[];
  apiKeyEntries: NormalizedOpenAIKeyBaselineEntry[];
  testModel: string;
  models: ModelEntry[];
  excludedModels: string[];
}

const buildBaseline = (
  kind: ProviderKind,
  form: ProviderDrawerFormState,
  enabled?: boolean
): ProviderDrawerBaseline => ({
  v8Source: form.v8Source,
  enabled,
  name: String(form.name ?? '').trim(),
  apiKey: String(form.apiKey ?? '').trim(),
  authIndex: normalizeAuthIndex(form.authIndex) ?? '',
  priority:
    form.priority !== undefined && Number.isFinite(form.priority)
      ? Math.trunc(form.priority)
      : null,
  weight: getCredentialWeightComparisonValue(form.weight),
  prefix: String(form.prefix ?? '').trim(),
  baseUrl: String(form.baseUrl ?? '').trim(),
  proxyUrl: String(form.proxyUrl ?? '').trim(),
  websockets: Boolean(form.websockets),
  disableCooling: form.disableCooling,
  fingerprintProfile: form.fingerprintProfile,
  rebuildMidSystemMessage: Boolean(form.rebuildMidSystemMessage),
  cloak: normalizeCloakForBaseline(
    form.cloak
      ? {
          ...form.cloak,
          sensitiveWords: parseSensitiveWordsText(form.cloakSensitiveWordsInput),
        }
      : undefined
  ),
  headers: normalizeHeaderEntries(form.headers),
  apiKeyEntries: kind === 'openai' ? normalizeOpenAIApiKeyEntries(form.apiKeyEntries) : [],
  testModel: kind === 'openai' ? String(form.testModel ?? '').trim() : '',
  models: normalizeModelEntriesForKind(kind, form.modelEntries),
  excludedModels:
    kind === 'openai'
      ? []
      : stripDisableAllModelsRule(parseExcludedModels(form.excludedText ?? '')),
});

const areBaselinesEqual = (
  kind: ProviderKind,
  left: ProviderDrawerBaseline,
  right: ProviderDrawerBaseline
): boolean => {
  if (
    left.name !== right.name ||
    left.priority !== right.priority ||
    left.prefix !== right.prefix ||
    left.baseUrl !== right.baseUrl ||
    left.disableCooling !== right.disableCooling
  ) {
    return false;
  }
  if (!areKeyValueEntriesEqual(left.headers, right.headers)) return false;

  if (kind === 'openai') {
    if (left.testModel !== right.testModel) return false;
    if (!areNormalizedOpenAIKeyEntriesEqual(left.apiKeyEntries, right.apiKeyEntries)) {
      return false;
    }
  } else {
    if (
      left.apiKey !== right.apiKey ||
      left.authIndex !== right.authIndex ||
      left.weight !== right.weight ||
      left.proxyUrl !== right.proxyUrl
    ) {
      return false;
    }
    if ((kind === 'codex' || kind === 'xai') && left.websockets !== right.websockets) {
      return false;
    }
    if (kind === 'claude') {
      if (
        left.fingerprintProfile !== right.fingerprintProfile ||
        left.rebuildMidSystemMessage !== right.rebuildMidSystemMessage ||
        !areCloakBaselinesEqual(left.cloak, right.cloak)
      ) {
        return false;
      }
    }
  }

  if (!areModelEntriesEqual(left.models, right.models)) return false;
  if (!areStringArraysEqual(left.excludedModels, right.excludedModels)) return false;
  if (
    left.enabled !== undefined &&
    right.enabled !== undefined &&
    left.enabled !== right.enabled
  ) {
    return false;
  }
  return true;
};

const cloneDrawerFormState = (form: ProviderDrawerFormState): ProviderDrawerFormState => ({
  ...form,
  v8Source: form.v8Source ? { ...form.v8Source } : undefined,
  cloak: form.cloak
    ? {
        ...form.cloak,
        sensitiveWords: form.cloak.sensitiveWords ? [...form.cloak.sensitiveWords] : undefined,
      }
    : undefined,
  headers: form.headers.map((entry) => ({ ...entry })),
  modelEntries: form.modelEntries.map((entry) => cloneModelEntry(entry)),
  apiKeyEntries: form.apiKeyEntries.map((entry) =>
    preservePersistedOpenAIKeyEntry(entry, {
      ...entry,
      headers: entry.headers ? { ...entry.headers } : undefined,
    })
  ),
});

interface CommittedAnchorState {
  sourceRowKey: string;
  sourceRaw: unknown;
  kind: ProviderKind;
  name?: string;
  apiKey?: string;
  baseUrl?: string;
  authIndex?: string;
  v8Source?: ProviderV8GroupSource;
  originalIndex?: number;
  preSaveEtag?: string;
  preSaveEnabled?: boolean;
  preSaveBaseline: ProviderDrawerBaseline;
  committedForm: ProviderDrawerFormState;
  committedBaseline: ProviderDrawerBaseline;
}

const isStaleRowRelativeToAnchor = (
  kind: ProviderKind,
  row: ProviderRow,
  anchor: CommittedAnchorState
): boolean => {
  if (row.key !== anchor.sourceRowKey || row.kind !== anchor.kind) {
    return false;
  }
  if (row.raw === anchor.sourceRaw) {
    return true;
  }
  const incomingBaseline = buildBaseline(kind, buildFormFromRow(row), row.enabled);
  const saveChangedBusinessFields = !areBaselinesEqual(
    kind,
    anchor.preSaveBaseline,
    anchor.committedBaseline
  );
  if (
    saveChangedBusinessFields &&
    areBaselinesEqual(kind, incomingBaseline, anchor.preSaveBaseline)
  ) {
    return true;
  }
  const committedEtag = anchor.v8Source?.etag?.trim();
  const incomingEtag = row.raw.v8Source?.etag?.trim();
  if (
    committedEtag &&
    incomingEtag &&
    incomingEtag !== committedEtag &&
    areBaselinesEqual(kind, incomingBaseline, anchor.preSaveBaseline)
  ) {
    return true;
  }
  return false;
};

const resolveCallerV8Source = (
  mode: 'view-edit' | 'create',
  row: ProviderRow | null,
  effectiveRow: ProviderRow | null,
  anchor: CommittedAnchorState | null,
  baseline: ProviderDrawerBaseline,
  formV8Source: ProviderV8GroupSource | undefined
): ProviderV8GroupSource | undefined => {
  if (mode !== 'view-edit' || !row) {
    return formV8Source;
  }
  const rowV8Source = row.raw.v8Source;
  const rowEtag = rowV8Source?.etag?.trim();
  const formEtag = formV8Source?.etag?.trim();
  if (rowEtag && rowEtag !== formEtag) {
    const sameV8Coords =
      Boolean(normalizeAuthIndex(row.raw.authIndex)) ||
      !formV8Source ||
      !rowV8Source ||
      matchesV8Source(formV8Source, rowV8Source);
    const rowBaseline = buildBaseline(row.kind, buildFormFromRow(row), row.enabled);
    if (sameV8Coords && areBaselinesEqual(row.kind, baseline, rowBaseline)) {
      return rowV8Source;
    }
    if (
      anchor &&
      anchor.sourceRowKey === row.key &&
      anchor.kind === row.kind &&
      sameV8Coords &&
      areBaselinesEqual(row.kind, anchor.preSaveBaseline, rowBaseline) &&
      rowEtag !== anchor.preSaveEtag?.trim()
    ) {
      return rowV8Source;
    }
  }
  return formV8Source ?? effectiveRow?.raw.v8Source ?? rowV8Source;
};
const getErrorMessage = (err: unknown): string => {
  if (err instanceof Error) return err.message;
  if (typeof err === 'string') return err;
  return '';
};


const matchesV8Source = (
  left: ProviderV8GroupSource | undefined,
  right: ProviderV8GroupSource | undefined
): boolean => {
  if (!left || !right) return false;
  return (
    left.family === right.family &&
    left.groupIndex === right.groupIndex &&
    left.keyIndex === right.keyIndex
  );
};

export const rowMatchesSavedAnchor = (
  candidate: ProviderRow,
  hint?: ProviderSavedIdentityHint
): boolean => {
  if (!hint) return false;
  const hintAuth = String(hint.authIndex ?? '').trim();
  if (hintAuth && String(candidate.raw.authIndex ?? '').trim() === hintAuth) {
    return true;
  }
  if (hint.v8Source && candidate.raw.v8Source) {
    const matchesFamily = candidate.raw.v8Source.family === hint.v8Source.family;
    const matchesGroup = candidate.raw.v8Source.groupIndex === hint.v8Source.groupIndex;
    const matchesKey =
      candidate.kind === 'openai'
        ? true
        : (candidate.raw.v8Source.keyIndex ?? 0) === (hint.v8Source.keyIndex ?? 0);
    if (matchesFamily && matchesGroup && matchesKey) {
      return true;
    }
  }
  if (candidate.kind === 'openai') {
    const nameMatches =
      !hint.name || String(candidate.raw.name ?? '').trim() === String(hint.name).trim();
    const urlMatches =
      !hint.baseUrl || String(candidate.baseUrl ?? '').trim() === String(hint.baseUrl).trim();
    const indexMatches =
      hint.originalIndex === undefined || candidate.originalIndex === hint.originalIndex;
    return nameMatches && urlMatches && indexMatches;
  }
  const apiKeyMatches =
    !hint.apiKey || String(candidate.raw.apiKey ?? '').trim() === String(hint.apiKey).trim();
  const urlMatches =
    !hint.baseUrl || String(candidate.baseUrl ?? '').trim() === String(hint.baseUrl).trim();
  const nameMatches =
    !hint.name || String(candidate.raw.name ?? '').trim() === String(hint.name).trim();
  const indexMatches =
    hint.originalIndex === undefined || candidate.originalIndex === hint.originalIndex;
  return apiKeyMatches && urlMatches && nameMatches && indexMatches;
};

export const isSameProviderEntryAcrossIndexShift = (
  prevRow: ProviderRow | null,
  nextRow: ProviderRow | null
): boolean => {
  if (!prevRow || !nextRow) return false;
  if (prevRow.kind !== nextRow.kind || prevRow.key === nextRow.key) return false;
  if (normalizeAuthIndex(prevRow.raw.authIndex) || normalizeAuthIndex(nextRow.raw.authIndex)) {
    return false;
  }
  if (prevRow.originalIndex === nextRow.originalIndex) return false;
  if (prevRow.key.replace(/::\d+$/, '') !== nextRow.key.replace(/::\d+$/, '')) {
    return false;
  }
  if (prevRow.kind === 'openai' && nextRow.kind === 'openai') {
    const prevSig = (prevRow.raw.apiKeyEntries ?? [])
      .map((entry) => `${String(entry.apiKey ?? '').trim()}::${String(entry.proxyUrl ?? '').trim()}`)
      .join('|');
    const nextSig = (nextRow.raw.apiKeyEntries ?? [])
      .map((entry) => `${String(entry.apiKey ?? '').trim()}::${String(entry.proxyUrl ?? '').trim()}`)
      .join('|');
    return prevSig === nextSig;
  }
  return String(prevRow.raw.name ?? '').trim() === String(nextRow.raw.name ?? '').trim();
};

const matchesSingleKeyIdentity = (
  candidate: GeminiKeyConfig | ProviderKeyConfig,
  target: GeminiKeyConfig | ProviderKeyConfig
): boolean => {
  const candidateAuth = normalizeAuthIndex(candidate.authIndex);
  const targetAuth = normalizeAuthIndex(target.authIndex);
  if (targetAuth && candidateAuth) {
    return candidateAuth === targetAuth;
  }
  return (
    String(candidate.apiKey ?? '').trim() === String(target.apiKey ?? '').trim() &&
    String(candidate.baseUrl ?? '').trim() === String(target.baseUrl ?? '').trim()
  );
};

const resolveSingleKeyTargetIndex = (
  latestList: Array<GeminiKeyConfig | ProviderKeyConfig>,
  row: Extract<ProviderRow, { kind: Exclude<ProviderKind, 'openai'> }>
): number => {
  const rowAtOriginal = latestList[row.originalIndex];
  const targetAuthIndex = normalizeAuthIndex(row.raw.authIndex);
  if (rowAtOriginal) {
    if (
      matchesV8Source(rowAtOriginal.v8Source, row.raw.v8Source) &&
      matchesSingleKeyIdentity(rowAtOriginal, row.raw)
    ) {
      return row.originalIndex;
    }
    if (targetAuthIndex && normalizeAuthIndex(rowAtOriginal.authIndex) === targetAuthIndex) {
      return row.originalIndex;
    }
    if (
      (!targetAuthIndex || !normalizeAuthIndex(rowAtOriginal.authIndex)) &&
      String(rowAtOriginal.apiKey ?? '').trim() === String(row.raw.apiKey ?? '').trim() &&
      String(rowAtOriginal.baseUrl ?? '').trim() === String(row.raw.baseUrl ?? '').trim()
    ) {
      return row.originalIndex;
    }
  }

  const byV8 = row.raw.v8Source
    ? latestList.findIndex(
        (item) =>
          matchesV8Source(item.v8Source, row.raw.v8Source) &&
          matchesSingleKeyIdentity(item, row.raw)
      )
    : -1;
  if (byV8 >= 0) return byV8;

  const byAuth = targetAuthIndex
    ? latestList.findIndex((item) => normalizeAuthIndex(item.authIndex) === targetAuthIndex)
    : -1;
  if (byAuth >= 0) return byAuth;

  const byKeyAndUrl = latestList.findIndex(
    (item) =>
      (!targetAuthIndex || !normalizeAuthIndex(item.authIndex)) &&
      String(item.apiKey ?? '').trim() === String(row.raw.apiKey ?? '').trim() &&
      String(item.baseUrl ?? '').trim() === String(row.raw.baseUrl ?? '').trim()
  );
  if (byKeyAndUrl >= 0) return byKeyAndUrl;

  return -1;
};

const resolveOpenAITargetIndex = (
  latestList: OpenAIProviderConfig[],
  row: Extract<ProviderRow, { kind: 'openai' }>
): number => {
  const atOriginal = latestList[row.originalIndex];
  if (atOriginal) {
    if (
      matchesV8Source(atOriginal.v8Source, row.raw.v8Source) &&
      atOriginal.name === row.raw.name
    ) {
      return row.originalIndex;
    }
    if (atOriginal.name === row.raw.name) {
      return row.originalIndex;
    }
  }
  const byV8 = row.raw.v8Source
    ? latestList.findIndex(
        (item) => matchesV8Source(item.v8Source, row.raw.v8Source) && item.name === row.raw.name
      )
    : -1;
  if (byV8 >= 0) return byV8;
  const byName = latestList.findIndex((item) => item.name === row.raw.name);
  if (byName >= 0) return byName;
  return -1;
};

const readLatestV8ConfigEtag = (): string | undefined => {
  if (
    'getLatestV8ConfigEtag' in providersApi &&
    typeof providersApi.getLatestV8ConfigEtag === 'function'
  ) {
    const value = providersApi.getLatestV8ConfigEtag();
    return typeof value === 'string' && value.trim() ? value.trim() : undefined;
  }
  return undefined;
};

const extractWriteResultEtag = (res: unknown): string | undefined => {
  if (res && typeof res === 'object' && 'etag' in res && typeof res.etag === 'string') {
    return res.etag.trim() || undefined;
  }
  return undefined;
};

const resolveRefreshedName = (
  originalName: string | undefined,
  desiredName: string | undefined,
  refreshedName: string | undefined,
  prevEtag: string | undefined,
  nextEtag: string | undefined
): string => {
  const trimmedDesired = String(desiredName ?? '').trim();
  const trimmedOriginal = String(originalName ?? '').trim();
  const trimmedRefreshed = String(refreshedName ?? '').trim();
  if (!trimmedRefreshed) return trimmedDesired;
  const isStaleMockEcho =
    Boolean(trimmedDesired) &&
    trimmedDesired !== trimmedOriginal &&
    trimmedRefreshed === trimmedOriginal &&
    prevEtag === nextEtag;
  return isStaleMockEcho ? trimmedDesired : trimmedRefreshed;
};

const findMatchedOpenAIIndex = (
  syncedProviders: OpenAIProviderConfig[],
  latestList: OpenAIProviderConfig[],
  committedPayload: OpenAIProviderConfig,
  existing: OpenAIProviderConfig | undefined,
  v8Source: ProviderV8GroupSource | undefined,
  targetIndex: number,
  mode: 'view-edit' | 'create'
): number => {
  if (mode === 'view-edit') {
    const targetGroupIndex = existing?.v8Source?.groupIndex ?? v8Source?.groupIndex;
    if (targetGroupIndex !== undefined) {
      const byGroup = syncedProviders.findIndex(
        (item) => item.v8Source?.groupIndex === targetGroupIndex
      );
      if (byGroup >= 0) return byGroup;
    }

    if (
      targetIndex >= 0 &&
      targetIndex < syncedProviders.length &&
      syncedProviders.length === latestList.length
    ) {
      const candidate = syncedProviders[targetIndex];
      const matchesOtherUntouched = latestList.some(
        (pre, idx) =>
          idx !== targetIndex &&
          pre.name === candidate.name &&
          (pre.baseUrl ?? '').trim() === (candidate.baseUrl ?? '').trim()
      );
      const differsFromCommitted =
        candidate.name !== committedPayload.name ||
        (candidate.baseUrl ?? '').trim() !== committedPayload.baseUrl.trim();
      if (!(matchesOtherUntouched && differsFromCommitted)) {
        return targetIndex;
      }
    }
  }

  const committedAuth = normalizeAuthIndex(committedPayload.authIndex);
  if (committedAuth) {
    const byAuth = syncedProviders.findIndex(
      (item) => normalizeAuthIndex(item.authIndex) === committedAuth
    );
    if (byAuth >= 0) return byAuth;
  }

  return syncedProviders.findIndex((item) => item.name === committedPayload.name);
};

const findMatchedSingleKeyIndex = (
  syncedList: Array<GeminiKeyConfig | ProviderKeyConfig>,
  latestList: Array<GeminiKeyConfig | ProviderKeyConfig>,
  payload: GeminiKeyConfig | ProviderKeyConfig,
  existingItem: GeminiKeyConfig | ProviderKeyConfig | undefined,
  targetIndex: number,
  mode: 'view-edit' | 'create'
): number => {
  const payloadAuth = normalizeAuthIndex(payload.authIndex);
  if (payloadAuth) {
    const byAuth = syncedList.findIndex(
      (item) => normalizeAuthIndex(item.authIndex) === payloadAuth
    );
    if (byAuth >= 0) return byAuth;
  }

  if (mode === 'view-edit' && existingItem) {
    const groupIdx = existingItem.v8Source?.groupIndex;
    if (groupIdx !== undefined) {
      const siblingsInLatest = latestList.filter(
        (item) => item.v8Source?.groupIndex === groupIdx
      ).length;
      const groupKeys = (existingItem.v8Source?.group as { keys?: unknown[] } | undefined)?.keys;
      const isPartOfMultiKeyGroup =
        siblingsInLatest > 1 || (Array.isArray(groupKeys) && groupKeys.length > 1);
      if (!isPartOfMultiKeyGroup) {
        const byGroup = syncedList.findIndex(
          (item) =>
            item.v8Source?.groupIndex === groupIdx && (item.v8Source?.keyIndex ?? 0) === 0
        );
        if (byGroup >= 0) return byGroup;
      }
    }
  }

  const targetApiKey = String(payload.apiKey ?? '').trim();
  const targetBaseUrl = String(payload.baseUrl ?? '').trim();
  const untouchedSiblings = latestList
    .map((item, idx) => ({ item, idx }))
    .filter(({ idx }) => mode !== 'view-edit' || idx !== targetIndex);

  const isUntouchedSibling = (candidate: GeminiKeyConfig | ProviderKeyConfig): boolean => {
    const candidateAuth = normalizeAuthIndex(candidate.authIndex);
    return untouchedSiblings.some(({ item: pre }) => {
      const preAuth = normalizeAuthIndex(pre.authIndex);
      if (candidateAuth && preAuth && candidateAuth === preAuth) {
        return true;
      }
      return Boolean(
        candidate.v8Source &&
          pre.v8Source &&
          candidate.v8Source.family === pre.v8Source.family &&
          candidate.v8Source.groupIndex === pre.v8Source.groupIndex &&
          (candidate.v8Source.keyIndex ?? 0) === (pre.v8Source.keyIndex ?? 0) &&
          String(candidate.apiKey ?? '').trim() === String(pre.apiKey ?? '').trim() &&
          String(candidate.baseUrl ?? '').trim() === String(pre.baseUrl ?? '').trim() &&
          String(candidate.name ?? '').trim() === String(pre.name ?? '').trim()
      );
    });
  };

  const keyUrlMatches: number[] = [];
  syncedList.forEach((item, idx) => {
    if (
      String(item.apiKey ?? '').trim() === targetApiKey &&
      String(item.baseUrl ?? '').trim() === targetBaseUrl
    ) {
      keyUrlMatches.push(idx);
    }
  });
  if (keyUrlMatches.length > 0) {
    const nonSiblingMatches = keyUrlMatches.filter((idx) => !isUntouchedSibling(syncedList[idx]));
    const pool = nonSiblingMatches.length > 0 ? nonSiblingMatches : keyUrlMatches;
    const desiredName = String(payload.name ?? '').trim();
    if (desiredName) {
      const exactNameIdx = pool.find(
        (idx) => String(syncedList[idx].name ?? '').trim() === desiredName
      );
      if (exactNameIdx !== undefined) return exactNameIdx;
    }
    return pool[0];
  }

  if (
    mode === 'view-edit' &&
    targetIndex >= 0 &&
    targetIndex < syncedList.length &&
    syncedList.length === latestList.length
  ) {
    return targetIndex;
  }

  return -1;
};

const fetchSingleKeyConfigs = async (
  kind: Exclude<ProviderKind, 'openai'>
): Promise<Array<GeminiKeyConfig | ProviderKeyConfig>> => {
  switch (kind) {
    case 'gemini':
      return (await providersApi.getGeminiKeys()) ?? [];
    case 'interactions':
      return (await providersApi.getInteractionsKeys()) ?? [];
    case 'codex':
      return (await providersApi.getCodexConfigs()) ?? [];
    case 'xai':
      return (await providersApi.getXAIConfigs()) ?? [];
    case 'claude':
      return (await providersApi.getClaudeConfigs()) ?? [];
    case 'vertex':
      return (await providersApi.getVertexConfigs()) ?? [];
  }
};

const saveSingleKeyConfigs = async (
  kind: Exclude<ProviderKind, 'openai'>,
  list: Array<GeminiKeyConfig | ProviderKeyConfig>
): Promise<unknown> => {
  switch (kind) {
    case 'gemini':
      return providersApi.saveGeminiKeys(list as GeminiKeyConfig[]);
    case 'interactions':
      return providersApi.saveInteractionsKeys(list as GeminiKeyConfig[]);
    case 'codex':
      return providersApi.saveCodexConfigs(list as ProviderKeyConfig[]);
    case 'xai':
      return providersApi.saveXAIConfigs(list as ProviderKeyConfig[]);
    case 'claude':
      return providersApi.saveClaudeConfigs(list as ProviderKeyConfig[]);
    case 'vertex':
      return providersApi.saveVertexConfigs(list as ProviderKeyConfig[]);
  }
};

const createSingleKeyConfig = async (
  kind: Exclude<ProviderKind, 'openai'>,
  payload: GeminiKeyConfig | ProviderKeyConfig,
  latestList: Array<GeminiKeyConfig | ProviderKeyConfig>
): Promise<unknown> => {
  switch (kind) {
    case 'gemini':
      if (typeof providersApi.createGeminiKey === 'function') {
        return providersApi.createGeminiKey(payload as GeminiKeyConfig);
      }
      break;
    case 'interactions':
      if (typeof providersApi.createInteractionsKey === 'function') {
        return providersApi.createInteractionsKey(payload as GeminiKeyConfig);
      }
      break;
    case 'codex':
      if (typeof providersApi.createCodexConfig === 'function') {
        return providersApi.createCodexConfig(payload as ProviderKeyConfig);
      }
      break;
    case 'xai':
      if (typeof providersApi.createXAIConfig === 'function') {
        return providersApi.createXAIConfig(payload as ProviderKeyConfig);
      }
      break;
    case 'claude':
      if (typeof providersApi.createClaudeConfig === 'function') {
        return providersApi.createClaudeConfig(payload as ProviderKeyConfig);
      }
      break;
    case 'vertex':
      if (typeof providersApi.createVertexConfig === 'function') {
        return providersApi.createVertexConfig(payload as ProviderKeyConfig);
      }
      break;
  }
  return saveSingleKeyConfigs(kind, [...latestList, payload]);
};

const updateSingleKeyConfig = async (
  kind: Exclude<ProviderKind, 'openai'>,
  existingItem: GeminiKeyConfig | ProviderKeyConfig,
  payload: GeminiKeyConfig | ProviderKeyConfig,
  latestList: Array<GeminiKeyConfig | ProviderKeyConfig>,
  targetIndex: number
): Promise<unknown> => {
  switch (kind) {
    case 'gemini':
      if (typeof providersApi.updateGeminiKey === 'function') {
        return providersApi.updateGeminiKey(
          existingItem as GeminiKeyConfig,
          payload as GeminiKeyConfig
        );
      }
      break;
    case 'interactions':
      if (typeof providersApi.updateInteractionsKey === 'function') {
        return providersApi.updateInteractionsKey(
          existingItem as GeminiKeyConfig,
          payload as GeminiKeyConfig
        );
      }
      break;
    case 'codex':
      if (typeof providersApi.updateCodexConfig === 'function') {
        return providersApi.updateCodexConfig(
          existingItem as ProviderKeyConfig,
          payload as ProviderKeyConfig
        );
      }
      break;
    case 'xai':
      if (typeof providersApi.updateXAIConfig === 'function') {
        return providersApi.updateXAIConfig(
          existingItem as ProviderKeyConfig,
          payload as ProviderKeyConfig
        );
      }
      break;
    case 'claude':
      if (typeof providersApi.updateClaudeConfig === 'function') {
        return providersApi.updateClaudeConfig(
          existingItem as ProviderKeyConfig,
          payload as ProviderKeyConfig
        );
      }
      break;
    case 'vertex':
      if (typeof providersApi.updateVertexConfig === 'function') {
        return providersApi.updateVertexConfig(
          existingItem as ProviderKeyConfig,
          payload as ProviderKeyConfig
        );
      }
      break;
  }
  const nextList =
    targetIndex >= 0
      ? latestList.map((item, idx) => (idx === targetIndex ? payload : item))
      : latestList.length > 0
        ? latestList
        : [payload];
  return saveSingleKeyConfigs(kind, nextList);
};

export function useProviderDrawerEditor({
  open,
  mode,
  row,
  activeKind,
  v8AliasSupported,
  actionsDisabled,
  rowDeletedExternally = false,
  sessionIdentity,
  onClose,
  onSaved,
  runWithMutationLock,
}: UseProviderDrawerEditorOptions) {
  const { t } = useTranslation();
  const { showNotification, showConfirmation } = useNotificationStore();
  const updateConfigValue = useConfigStore((state) => state.updateConfigValue);
  const clearCache = useConfigStore((state) => state.clearCache);

  const [form, setForm] = useState<ProviderDrawerFormState>(() =>
    mode === 'view-edit' && row ? buildFormFromRow(row) : buildEmptyForm(activeKind)
  );
  const [baseline, setBaseline] = useState<ProviderDrawerBaseline>(() =>
    buildBaseline(
      activeKind,
      mode === 'view-edit' && row ? buildFormFromRow(row) : buildEmptyForm(activeKind),
      mode === 'view-edit' && row ? row.enabled : undefined
    )
  );
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  // Model discovery state
  const [modelDiscoveryOpen, setModelDiscoveryOpen] = useState(false);
  const [modelDiscoveryEndpoint, setModelDiscoveryEndpoint] = useState('');
  const [discoveredModels, setDiscoveredModels] = useState<ModelInfo[]>(EMPTY_DISCOVERED_MODELS);
  const [modelsLoaded, setModelsLoaded] = useState(false);
  const [modelDiscoveryFetching, setModelDiscoveryFetching] = useState(false);
  const [modelDiscoveryError, setModelDiscoveryError] = useState('');
  const [modelDiscoverySearch, setModelDiscoverySearch] = useState('');
  const [modelDiscoverySelected, setModelDiscoverySelected] = useState<Set<string>>(new Set());

  // Connectivity test state
  const [connectivityTestModel, setConnectivityTestModel] = useState<string>(() => {
    if (mode === 'view-edit' && row) {
      const seeded = buildFormFromRow(row);
      const firstModel = seeded.modelEntries.map((item) => item.name.trim()).find(Boolean);
      return seeded.testModel.trim() || firstModel || PROTOCOL_DEFAULT_TEST_MODELS[activeKind];
    }
    return PROTOCOL_DEFAULT_TEST_MODELS[activeKind];
  });
  const [testModelManuallyEdited, setTestModelManuallyEdited] = useState(false);
  const [connectivityProbeMode, setConnectivityProbeModeState] =
    useState<ProviderConnectivityProbeMode>('inference');
  const [singleTestResult, setSingleTestResult] = useState<ProviderDrawerConnectivityResult>({
    status: 'idle',
    message: '',
  });
  const [openAIAllTestResult, setOpenAIAllTestResult] = useState<ProviderDrawerConnectivityResult>({
    status: 'idle',
    message: '',
  });
  const [openAIKeyStatuses, setOpenAIKeyStatuses] = useState<ProviderOpenAIKeyTestEntry[]>([]);
  const [isTesting, setIsTesting] = useState(false);

  const unmountedRef = useRef(false);
  const pendingDiscardPromiseRef = useRef<Promise<boolean> | null>(null);
  const prevSnapshotIdentityRef = useRef<string>('');
  const prevRawRef = useRef<unknown>(null);
  const prevSessionIdentityRef = useRef<string>('');
  const prevRowRef = useRef<ProviderRow | null>(null);
  const connectivityRequestIdRef = useRef(0);
  const sessionTokenRef = useRef(0);
  const committedAnchorRef = useRef<CommittedAnchorState | null>(null);
  const latestSessionStateRef = useRef({
    open,
    mode,
    activeKind,
    rowKey: row?.key ?? null,
  });
  latestSessionStateRef.current = {
    open,
    mode,
    activeKind,
    rowKey: row?.key ?? null,
  };

  useEffect(() => {
    unmountedRef.current = false;
    return () => {
      unmountedRef.current = true;
      connectivityRequestIdRef.current += 1;
      sessionTokenRef.current += 1;
    };
  }, []);

  const normalizedHeaders = useMemo(() => normalizeHeaderEntries(form.headers), [form.headers]);
  const normalizedModels = useMemo(
    () => normalizeModelEntriesForKind(activeKind, form.modelEntries),
    [activeKind, form.modelEntries]
  );
  const normalizedExcludedModels = useMemo(
    () =>
      activeKind === 'openai'
        ? []
        : stripDisableAllModelsRule(parseExcludedModels(form.excludedText ?? '')),
    [activeKind, form.excludedText]
  );
  const normalizedPriority = useMemo(
    () =>
      form.priority !== undefined && Number.isFinite(form.priority)
        ? Math.trunc(form.priority)
        : null,
    [form.priority]
  );
  const comparableWeight = getCredentialWeightComparisonValue(form.weight);
  const normalizedApiKeyEntries = useMemo(
    () => (activeKind === 'openai' ? normalizeOpenAIApiKeyEntries(form.apiKeyEntries) : []),
    [activeKind, form.apiKeyEntries]
  );
  const normalizedCloak = useMemo(
    () =>
      normalizeCloakForBaseline(
        form.cloak
          ? {
              ...form.cloak,
              sensitiveWords: parseSensitiveWordsText(form.cloakSensitiveWordsInput),
            }
          : undefined
      ),
    [form.cloak, form.cloakSensitiveWordsInput]
  );

  const isConfigDirty = useMemo(() => {
    if (baseline.name !== String(form.name ?? '').trim()) return true;
    if (baseline.priority !== normalizedPriority) return true;
    if (baseline.prefix !== String(form.prefix ?? '').trim()) return true;
    if (baseline.baseUrl !== String(form.baseUrl ?? '').trim()) return true;
    if (baseline.disableCooling !== form.disableCooling) return true;
    if (!areKeyValueEntriesEqual(baseline.headers, normalizedHeaders)) return true;

    if (activeKind === 'openai') {
      if (baseline.testModel !== String(form.testModel ?? '').trim()) return true;
      return !areNormalizedOpenAIKeyEntriesEqual(baseline.apiKeyEntries, normalizedApiKeyEntries);
    }

    if (baseline.apiKey !== String(form.apiKey ?? '').trim()) return true;
    if (baseline.authIndex !== (normalizeAuthIndex(form.authIndex) ?? '')) return true;
    if (baseline.weight !== comparableWeight) return true;
    if (baseline.proxyUrl !== String(form.proxyUrl ?? '').trim()) return true;
    if ((activeKind === 'codex' || activeKind === 'xai') && baseline.websockets !== Boolean(form.websockets)) {
      return true;
    }
    if (activeKind === 'claude') {
      if (baseline.fingerprintProfile !== form.fingerprintProfile) return true;
      if (baseline.rebuildMidSystemMessage !== Boolean(form.rebuildMidSystemMessage)) return true;
      if (!areCloakBaselinesEqual(baseline.cloak, normalizedCloak)) return true;
    }
    return false;
  }, [
    activeKind,
    baseline,
    comparableWeight,
    form.apiKey,
    form.authIndex,
    form.baseUrl,
    form.disableCooling,
    form.fingerprintProfile,
    form.name,
    form.prefix,
    form.proxyUrl,
    form.rebuildMidSystemMessage,
    form.testModel,
    form.websockets,
    normalizedApiKeyEntries,
    normalizedCloak,
    normalizedHeaders,
    normalizedPriority,
  ]);

  const isModelsDirty = useMemo(
    () =>
      !areModelEntriesEqual(baseline.models, normalizedModels) ||
      !areStringArraysEqual(baseline.excludedModels, normalizedExcludedModels),
    [baseline.excludedModels, baseline.models, normalizedExcludedModels, normalizedModels]
  );

  const isDirty = isConfigDirty || isModelsDirty;

  // Synchronize draft when drawer opens, switches mode/row/createKind, or receives fresh row while clean.
  useEffect(() => {
    if (!open) {
      if (prevSnapshotIdentityRef.current !== '') {
        connectivityRequestIdRef.current += 1;
        sessionTokenRef.current += 1;
      }
      prevSnapshotIdentityRef.current = '';
      prevSessionIdentityRef.current = '';
      prevRowRef.current = null;
      committedAnchorRef.current = null;
      prevRawRef.current = null;
      setModelDiscoveryOpen(false);
      setIsTesting(false);
      return;
    }

    const identity = `${mode}:${activeKind}:${mode === 'view-edit' ? (row?.key ?? 'none') : 'new'}`;
    const identityChanged = prevSnapshotIdentityRef.current !== identity;
    const prevRow = prevRowRef.current;
    const prevSessionId = prevSessionIdentityRef.current;
    const sameDrawerSession = Boolean(
      sessionIdentity && prevSessionId && prevSessionId === sessionIdentity
    );
    const sessionIdentityExplicitlyChanged = Boolean(
      sessionIdentity && prevSessionId && sessionIdentity !== prevSessionId
    );
    const anchor = committedAnchorRef.current;
    const isPostSaveReanchor = Boolean(
      !sessionIdentityExplicitlyChanged &&
        identityChanged &&
        mode === 'view-edit' &&
        row !== null &&
        prevRow !== null &&
        anchor &&
        anchor.kind === row.kind &&
        rowMatchesSavedAnchor(row, anchor)
    );
    const isIndexShiftReanchor = Boolean(
      !sessionIdentityExplicitlyChanged &&
        identityChanged &&
        mode === 'view-edit' &&
        row !== null &&
        prevRow !== null &&
        isSameProviderEntryAcrossIndexShift(prevRow, row)
    );
    const isSameRowReanchor = Boolean(
      identityChanged &&
        mode === 'view-edit' &&
        row !== null &&
        prevRow !== null &&
        prevRow.kind === row.kind &&
        (sameDrawerSession || isPostSaveReanchor || isIndexShiftReanchor)
    );

    if ((!identityChanged && !sessionIdentityExplicitlyChanged) || isSameRowReanchor) {
      if (mode !== 'view-edit' || row === null) {
        return;
      }
      if (!isSameRowReanchor && prevRawRef.current === row.raw) {
        return;
      }
      prevSnapshotIdentityRef.current = identity;
      prevSessionIdentityRef.current = sessionIdentity ?? '';
      prevRowRef.current = row;
      prevRawRef.current = row.raw;

      if (isSameRowReanchor && committedAnchorRef.current) {
        committedAnchorRef.current.sourceRowKey = row.key;
      }
      const activeAnchor = committedAnchorRef.current;

      if (!isDirty) {
        if (activeAnchor && isStaleRowRelativeToAnchor(activeKind, row, activeAnchor)) {
          const incomingEtag = row.raw.v8Source?.etag?.trim();
          const currentEtag = form.v8Source?.etag?.trim();
          const preSaveEtag = activeAnchor.preSaveEtag?.trim();
          if (incomingEtag && incomingEtag !== preSaveEtag && incomingEtag !== currentEtag) {
            const nextV8Source = row.raw.v8Source;
            activeAnchor.v8Source = nextV8Source;
            activeAnchor.committedForm = { ...activeAnchor.committedForm, v8Source: nextV8Source };
            setForm((prev) => ({ ...prev, v8Source: nextV8Source }));
            setBaseline((prev) => ({ ...prev, v8Source: nextV8Source }));
          }
          return;
        }
        if (isSameRowReanchor) {
          committedAnchorRef.current = null;
          const nextForm = buildFormFromRow(row);
          setForm(nextForm);
          setBaseline(buildBaseline(activeKind, nextForm, row.enabled));
          setError('');
          setModelDiscoveryOpen(false);
          return;
        }
      } else {
        if (activeAnchor && !isStaleRowRelativeToAnchor(activeKind, row, activeAnchor)) {
          committedAnchorRef.current = null;
        }
        const incomingV8 = row.raw.v8Source;
        const incomingEtag = incomingV8?.etag?.trim();
        const currentEtag = form.v8Source?.etag?.trim();
        const v8CoordsShifted =
          isSameRowReanchor && Boolean(incomingV8) && !matchesV8Source(form.v8Source, incomingV8);
        if (incomingV8 && ((incomingEtag && incomingEtag !== currentEtag) || v8CoordsShifted)) {
          const sameV8Coords =
            isSameRowReanchor ||
            Boolean(normalizeAuthIndex(row.raw.authIndex)) ||
            !form.v8Source ||
            matchesV8Source(form.v8Source, incomingV8);
          const incomingBaseline = buildBaseline(activeKind, buildFormFromRow(row), row.enabled);
          const nonEtagMatchesBaseline =
            sameV8Coords && areBaselinesEqual(activeKind, baseline, incomingBaseline);
          const matchesStalePreSaveAnchor = Boolean(
            !nonEtagMatchesBaseline &&
              sameV8Coords &&
              activeAnchor &&
              activeAnchor.sourceRowKey === row.key &&
              activeAnchor.kind === row.kind &&
              areBaselinesEqual(activeKind, activeAnchor.preSaveBaseline, incomingBaseline) &&
              incomingEtag !== activeAnchor.preSaveEtag?.trim()
          );
          if (nonEtagMatchesBaseline || matchesStalePreSaveAnchor) {
            if (committedAnchorRef.current && committedAnchorRef.current.sourceRowKey === row.key) {
              committedAnchorRef.current.v8Source = incomingV8;
              committedAnchorRef.current.committedForm = {
                ...committedAnchorRef.current.committedForm,
                v8Source: incomingV8,
              };
            }
            setForm((prev) => ({ ...prev, v8Source: incomingV8 }));
            setBaseline((prev) => ({ ...prev, v8Source: incomingV8 }));
          }
        }
        return;
      }
    }

    prevSnapshotIdentityRef.current = identity;
    prevSessionIdentityRef.current = sessionIdentity ?? '';
    prevRowRef.current = mode === 'view-edit' ? row : null;
    prevRawRef.current = row?.raw ?? null;
    committedAnchorRef.current = null;

    const nextForm =
      mode === 'view-edit' && row ? buildFormFromRow(row) : buildEmptyForm(activeKind);
    setForm(nextForm);
    setBaseline(
      buildBaseline(activeKind, nextForm, mode === 'view-edit' && row ? row.enabled : undefined)
    );
    setError('');
    setModelDiscoveryOpen(false);

    if (identityChanged || sessionIdentityExplicitlyChanged) {
      connectivityRequestIdRef.current += 1;
      sessionTokenRef.current += 1;
      setIsTesting(false);
      const firstModel = nextForm.modelEntries.map((item) => item.name.trim()).find(Boolean);
      setConnectivityTestModel(
        nextForm.testModel.trim() || firstModel || PROTOCOL_DEFAULT_TEST_MODELS[activeKind]
      );
      setTestModelManuallyEdited(false);
      setConnectivityProbeModeState('inference');
      setSingleTestResult({ status: 'idle', message: '' });
      setOpenAIAllTestResult({ status: 'idle', message: '' });
      setOpenAIKeyStatuses([]);
    }
  }, [activeKind, baseline, form.v8Source, isDirty, mode, open, row, sessionIdentity]);

  const availableModels = useMemo(
    () => normalizedModels.map((entry) => entry.name.trim()).filter(Boolean),
    [normalizedModels]
  );

  const modelSelectOptions = useMemo(() => {
    const seen = new Set<string>();
    return normalizedModels.reduce<Array<{ value: string; alias?: string; label: string }>>(
      (acc, entry) => {
        const name = entry.name.trim();
        if (!name || seen.has(name)) return acc;
        seen.add(name);
        const alias = entry.alias.trim();
        acc.push({
          value: name,
          alias: alias && alias !== name ? alias : undefined,
          label: alias && alias !== name ? `${name} (${alias})` : name,
        });
        return acc;
      },
      []
    );
  }, [normalizedModels]);

  useEffect(() => {
    if (!open || testModelManuallyEdited) return;
    const preferred =
      (activeKind === 'openai' ? form.testModel.trim() : '') ||
      availableModels[0] ||
      PROTOCOL_DEFAULT_TEST_MODELS[activeKind];
    if (preferred && connectivityTestModel !== preferred) {
      setConnectivityTestModel(preferred);
    }
  }, [activeKind, availableModels, connectivityTestModel, form.testModel, open, testModelManuallyEdited]);

  const resetForm = useCallback(() => {
    connectivityRequestIdRef.current += 1;
    sessionTokenRef.current += 1;
    const anchor = committedAnchorRef.current;
    const useCommittedAnchor =
      mode === 'view-edit' &&
      row !== null &&
      anchor !== null &&
      isStaleRowRelativeToAnchor(activeKind, row, anchor);
    if (!useCommittedAnchor) {
      committedAnchorRef.current = null;
    }
    const nextForm = useCommittedAnchor
      ? cloneDrawerFormState(anchor.committedForm)
      : mode === 'view-edit' && row
        ? buildFormFromRow(row)
        : buildEmptyForm(activeKind);
    setForm(nextForm);
    setBaseline(
      buildBaseline(activeKind, nextForm, mode === 'view-edit' && row ? row.enabled : undefined)
    );
    setError('');
    setIsTesting(false);
    const firstModel = nextForm.modelEntries.map((item) => item.name.trim()).find(Boolean);
    setConnectivityTestModel(
      nextForm.testModel.trim() || firstModel || PROTOCOL_DEFAULT_TEST_MODELS[activeKind]
    );
    setConnectivityProbeModeState('inference');
    setSingleTestResult({ status: 'idle', message: '' });
    setOpenAIAllTestResult({ status: 'idle', message: '' });
    setOpenAIKeyStatuses([]);
    setTestModelManuallyEdited(false);
  }, [activeKind, mode, row]);

  const confirmDiscardChanges = useCallback((): Promise<boolean> => {
    if (saving) {
      return Promise.resolve(false);
    }
    if (!isDirty) {
      return Promise.resolve(true);
    }
    if (pendingDiscardPromiseRef.current) {
      return pendingDiscardPromiseRef.current;
    }

    let settled = false;
    const confirmationPromise = new Promise<boolean>((resolve) => {
      const finish = (confirmed: boolean) => {
        if (settled) return;
        settled = true;
        pendingDiscardPromiseRef.current = null;
        resolve(confirmed);
      };

      showConfirmation({
        title: t('common.unsaved_changes_title'),
        message: t('common.unsaved_changes_message'),
        confirmText: t('common.leave'),
        cancelText: t('common.stay'),
        variant: 'danger',
        onConfirm: () => finish(true),
        onCancel: () => finish(false),
      });
    });

    if (!settled) {
      pendingDiscardPromiseRef.current = confirmationPromise;
    }
    return confirmationPromise;
  }, [isDirty, saving, showConfirmation, t]);

  const hasInvalidWeight = useMemo(() => {
    if (activeKind === 'openai') {
      return form.apiKeyEntries.some((entry) => Boolean(getCredentialWeightError(entry.weight)));
    }
    return Boolean(getCredentialWeightError(form.weight));
  }, [activeKind, form.apiKeyEntries, form.weight]);

  const hasInvalidThinking = useMemo(
    () => activeKind === 'openai' && hasInvalidThinkingLevels(form.modelEntries),
    [activeKind, form.modelEntries]
  );

  const canSave =
    !actionsDisabled &&
    !rowDeletedExternally &&
    !saving &&
    !isTesting &&
    !hasInvalidWeight &&
    !hasInvalidThinking &&
    !(mode === 'view-edit' && !row);

  // Model discovery logic
  const discoveryConnection = useDiscoveryAuthStore((state) =>
    JSON.stringify([state.apiBase, state.managementKey])
  );
  const discoveryScope = useMemo(
    () =>
      JSON.stringify([
        discoveryConnection,
        open,
        activeKind,
        sessionIdentity ?? (row?.key ?? 'new'),
        form.baseUrl,
        form.apiKey,
        form.authIndex,
        form.proxyUrl,
        form.apiKeyEntries,
        buildHeaderObject(form.headers),
      ]),
    [
      activeKind,
      discoveryConnection,
      form.apiKey,
      form.apiKeyEntries,
      form.authIndex,
      form.baseUrl,
      form.headers,
      form.proxyUrl,
      open,
      row?.key,
      sessionIdentity,
    ]
  );
  const activeDiscoveryScopeRef = useRef(discoveryScope);
  activeDiscoveryScopeRef.current = discoveryScope;
  const modelDiscoveryRequestIdRef = useRef(0);

  useLayoutEffect(() => {
    modelDiscoveryRequestIdRef.current += 1;
    setDiscoveredModels((prev) => (prev.length === 0 ? prev : EMPTY_DISCOVERED_MODELS));
    setModelsLoaded(false);
    setModelDiscoveryError('');
    setModelDiscoveryFetching(false);
    return () => {
      modelDiscoveryRequestIdRef.current += 1;
    };
  }, [discoveryScope]);

  const fetchModelDiscovery = useCallback(async () => {
    if (activeKind === 'vertex') return;
    const requestId = ++modelDiscoveryRequestIdRef.current;
    const requestScope = discoveryScope;
    const isCurrentDiscovery = () =>
      !unmountedRef.current &&
      modelDiscoveryRequestIdRef.current === requestId &&
      activeDiscoveryScopeRef.current === requestScope;

    setModelDiscoveryFetching(true);
    setModelDiscoveryError('');
    const headerObject = buildHeaderObject(form.headers);
    const apiKeyChanged = form.apiKey.trim() !== baseline.apiKey;
    const authIndex = apiKeyChanged ? undefined : (normalizeAuthIndex(form.authIndex) ?? undefined);

    try {
      let list: ModelInfo[] = [];
      if (activeKind === 'openai') {
        const firstKey = getOpenAIModelDiscoveryEntry(form.apiKeyEntries);
        const keyAuthIndex = normalizeAuthIndex(firstKey?.authIndex) ?? undefined;
        const discoveryHeaders = firstKey?.headers
          ? { ...headerObject, ...firstKey.headers }
          : headerObject;
        list = await modelsApi.fetchModelsViaApiCall(
          form.baseUrl.trim(),
          firstKey?.apiKey?.trim() || undefined,
          discoveryHeaders,
          keyAuthIndex,
          firstKey?.proxyUrl,
          !firstKey?.apiKey?.trim()
        );
      } else if (activeKind === 'claude') {
        list = await modelsApi.fetchClaudeModelsViaApiCall(
          form.baseUrl ?? '',
          form.apiKey.trim() || undefined,
          headerObject,
          authIndex,
          form.proxyUrl
        );
      } else if (activeKind === 'codex' || activeKind === 'xai') {
        const hasCustomAuthorization = Object.keys(headerObject).some(
          (key) => key.toLowerCase() === 'authorization'
        );
        const apiKey = form.apiKey.trim() || undefined;
        list = await modelsApi.fetchV1ModelsViaApiCall(
          form.baseUrl ?? '',
          hasCustomAuthorization ? undefined : apiKey,
          headerObject,
          authIndex,
          form.proxyUrl
        );
      } else {
        list = await modelsApi.fetchGeminiModelsViaApiCall(
          form.baseUrl ?? '',
          form.apiKey.trim() || undefined,
          headerObject,
          authIndex,
          form.proxyUrl
        );
      }
      if (!isCurrentDiscovery()) return;
      setDiscoveredModels(Array.isArray(list) ? list : []);
      setModelsLoaded(true);
    } catch (err: unknown) {
      if (!isCurrentDiscovery()) return;
      const message = getErrorMessage(err);
      if (activeKind === 'claude') {
        setModelDiscoveryError(`${t('ai_providers.claude_models_fetch_error')}: ${message}`);
      } else if (activeKind === 'gemini' || activeKind === 'interactions') {
        setModelDiscoveryError(`${t('ai_providers.gemini_models_fetch_error')}: ${message}`);
      } else if (activeKind === 'codex' || activeKind === 'xai') {
        setModelDiscoveryError(`${t('ai_providers.codex_models_fetch_error')}: ${message}`);
      } else {
        setModelDiscoveryError(`${t('ai_providers.openai_models_fetch_error')}: ${message}`);
      }
    } finally {
      if (isCurrentDiscovery()) {
        setModelDiscoveryFetching(false);
      }
    }
  }, [
    activeKind,
    baseline.apiKey,
    discoveryScope,
    form.apiKey,
    form.apiKeyEntries,
    form.authIndex,
    form.baseUrl,
    form.headers,
    form.proxyUrl,
    t,
  ]);

  useEffect(() => {
    if (!modelDiscoveryOpen) return;
    if (activeKind === 'claude' && typeof modelsApi.buildClaudeModelsEndpoint === 'function') {
      setModelDiscoveryEndpoint(modelsApi.buildClaudeModelsEndpoint(form.baseUrl ?? ''));
    } else if (
      (activeKind === 'gemini' || activeKind === 'interactions') &&
      typeof modelsApi.buildGeminiModelsEndpoint === 'function'
    ) {
      setModelDiscoveryEndpoint(modelsApi.buildGeminiModelsEndpoint(form.baseUrl ?? ''));
    } else if (
      (activeKind === 'codex' || activeKind === 'xai') &&
      typeof modelsApi.buildV1ModelsEndpoint === 'function'
    ) {
      setModelDiscoveryEndpoint(modelsApi.buildV1ModelsEndpoint(form.baseUrl ?? ''));
    } else if (activeKind === 'openai') {
      setModelDiscoveryEndpoint(buildOpenAIModelsEndpoint(form.baseUrl ?? ''));
    } else {
      setModelDiscoveryEndpoint(form.baseUrl.trim());
    }
    setModelDiscoverySearch('');
    setModelDiscoverySelected(new Set());
    setModelDiscoveryError('');
    void fetchModelDiscovery();
  }, [activeKind, fetchModelDiscovery, form.baseUrl, modelDiscoveryOpen]);

  const configuredModelNames = useMemo(
    () =>
      new Set(
        normalizedModels
          .map((entry) => entry.name.trim().toLowerCase())
          .filter(Boolean)
      ),
    [normalizedModels]
  );

  const discoveredModelsFiltered = useMemo(() => {
    const filter = modelDiscoverySearch.trim().toLowerCase();
    if (!filter) return discoveredModels;
    return discoveredModels.filter((model) => {
      const name = (model.name || '').toLowerCase();
      const alias = (model.alias || '').toLowerCase();
      const description = (model.description || '').toLowerCase();
      return name.includes(filter) || alias.includes(filter) || description.includes(filter);
    });
  }, [discoveredModels, modelDiscoverySearch]);

  const normalizeDiscoveredName = useCallback(
    (name: string) =>
      activeKind === 'gemini' || activeKind === 'interactions'
        ? stripGeminiModelResourceName(name).trim()
        : String(name ?? '').trim(),
    [activeKind]
  );

  useEffect(() => {
    const availableNames = new Set(
      discoveredModels.map((model) => normalizeDiscoveredName(model.name)).filter(Boolean)
    );
    setModelDiscoverySelected((prev) => {
      let changed = false;
      const next = new Set<string>();
      prev.forEach((name) => {
        if (availableNames.has(name) && !configuredModelNames.has(name.toLowerCase())) {
          next.add(name);
        } else {
          changed = true;
        }
      });
      return changed ? next : prev;
    });
  }, [configuredModelNames, discoveredModels, normalizeDiscoveredName]);

  const visibleDiscoverableModelNames = useMemo(
    () =>
      discoveredModelsFiltered
        .map((model) => normalizeDiscoveredName(model.name))
        .filter((name) => Boolean(name) && !configuredModelNames.has(name.toLowerCase())),
    [configuredModelNames, discoveredModelsFiltered, normalizeDiscoveredName]
  );

  const allVisibleSelected = useMemo(
    () =>
      visibleDiscoverableModelNames.length > 0 &&
      visibleDiscoverableModelNames.every((name) => modelDiscoverySelected.has(name)),
    [modelDiscoverySelected, visibleDiscoverableModelNames]
  );

  const toggleModelDiscoverySelection = useCallback(
    (rawName: string) => {
      const name = normalizeDiscoveredName(rawName);
      if (!name || configuredModelNames.has(name.toLowerCase())) return;
      setModelDiscoverySelected((prev) => {
        const next = new Set(prev);
        if (next.has(name)) next.delete(name);
        else next.add(name);
        return next;
      });
    },
    [configuredModelNames, normalizeDiscoveredName]
  );

  const handleSelectVisibleModels = useCallback(() => {
    setModelDiscoverySelected((prev) => {
      const next = new Set(prev);
      visibleDiscoverableModelNames.forEach((name) => next.add(name));
      return next;
    });
  }, [visibleDiscoverableModelNames]);

  const handleClearModelDiscoverySelection = useCallback(() => {
    setModelDiscoverySelected(new Set());
  }, []);

  const mergeDiscoveredModels = useCallback(
    (selectedModels: ModelInfo[]) => {
      if (!selectedModels.length) return;
      const existingNames = new Set(
        form.modelEntries
          .map((entry) => normalizeDiscoveredName(entry.name).toLowerCase())
          .filter(Boolean)
      );
      const toAdd: ModelEntry[] = [];
      selectedModels.forEach((model) => {
        const name = normalizeDiscoveredName(model.name);
        const key = name.toLowerCase();
        if (!name || existingNames.has(key)) return;
        existingNames.add(key);
        toAdd.push({ name, alias: model.alias ?? '' });
      });
      const addedCount = toAdd.length;
      if (addedCount === 0) return;

      setForm((prev) => {
        const prevNames = new Set(
          prev.modelEntries
            .map((entry) => normalizeDiscoveredName(entry.name).toLowerCase())
            .filter(Boolean)
        );
        const preservedEntries = prev.modelEntries.filter(
          (entry) => Boolean(entry.name.trim()) || Boolean(entry.alias.trim())
        );
        const appendedEntries: ModelEntry[] = [];
        selectedModels.forEach((model) => {
          const name = normalizeDiscoveredName(model.name);
          const key = name.toLowerCase();
          if (!name || prevNames.has(key)) return;
          prevNames.add(key);
          appendedEntries.push({ name, alias: model.alias ?? '' });
        });
        const mergedEntries = [...preservedEntries, ...appendedEntries];
        return {
          ...prev,
          modelEntries: mergedEntries.length ? mergedEntries : [{ name: '', alias: '' }],
        };
      });

      const addedKey =
        activeKind === 'claude'
          ? 'ai_providers.claude_models_fetch_added'
          : activeKind === 'codex' || activeKind === 'xai'
            ? 'ai_providers.codex_models_fetch_added'
            : activeKind === 'gemini' || activeKind === 'interactions'
              ? 'ai_providers.gemini_models_fetch_added'
              : 'ai_providers.openai_models_fetch_added';
      showNotification(t(addedKey, { count: addedCount }), 'success');
    },
    [activeKind, form.modelEntries, normalizeDiscoveredName, showNotification, t]
  );

  const applySelectedDiscoveredModels = useCallback(() => {
    const selected = discoveredModels.filter((model) =>
      modelDiscoverySelected.has(normalizeDiscoveredName(model.name))
    );
    if (selected.length) {
      mergeDiscoveredModels(selected);
    }
    setModelDiscoveryOpen(false);
  }, [discoveredModels, mergeDiscoveredModels, modelDiscoverySelected, normalizeDiscoveredName]);

  // Connectivity tests (testing live draft values!)
  const supportsInferenceProbe =
    activeKind === 'claude' ||
    activeKind === 'codex' ||
    activeKind === 'xai' ||
    activeKind === 'openai';
  const effectiveProbeMode: ProviderConnectivityProbeMode = supportsInferenceProbe
    ? connectivityProbeMode
    : 'models';

  // Connectivity tests (testing live draft values!)
  const runOpenAISingleKeyTest = useCallback(
    async (keyIndex: number, requestIdOverride?: number): Promise<boolean> => {
      const activeRequestId = requestIdOverride ?? ++connectivityRequestIdRef.current;
      const isCurrent = () =>
        !unmountedRef.current && connectivityRequestIdRef.current === activeRequestId;

      const baseUrl = form.baseUrl.trim();
      const endpoint =
        effectiveProbeMode === 'models'
          ? buildOpenAIModelsEndpoint(baseUrl)
          : buildOpenAIChatCompletionsEndpoint(baseUrl);
      if (!baseUrl || !endpoint) {
        const msg = t('notification.openai_test_url_required');
        if (isCurrent()) {
          setOpenAIKeyStatuses((prev) => {
            const next = [...prev];
            next[keyIndex] = { status: 'error', message: msg };
            return next;
          });
        }
        return false;
      }

      const keyEntry = form.apiKeyEntries[keyIndex];
      if (!keyEntry) return false;

      const keyAuthIndex = normalizeAuthIndex(keyEntry.authIndex) ?? undefined;
      const customHeaders = buildHeaderObject(form.headers);
      const entryHeaders = keyEntry.headers ?? {};
      const mergedHeaders: Record<string, string> = {
        ...customHeaders,
        ...entryHeaders,
      };

      if (effectiveProbeMode === 'models') {
        if (isCurrent()) {
          setOpenAIKeyStatuses((prev) => {
            const next = [...prev];
            next[keyIndex] = { status: 'loading', message: '' };
            return next;
          });
        }
        const startedAt = Date.now();
        try {
          const hasAuthHeader = hasHeader(mergedHeaders, 'authorization');
          const models = await modelsApi.fetchModelsViaApiCall(
            baseUrl,
            hasAuthHeader ? undefined : keyEntry.apiKey.trim() || undefined,
            mergedHeaders,
            keyAuthIndex,
            keyEntry.proxyUrl?.trim() || undefined,
            !keyEntry.apiKey.trim()
          );
          if (!isCurrent()) return false;
          const elapsedMs = Date.now() - startedAt;
          if (models.length === 0) {
            setOpenAIKeyStatuses((prev) => {
              const next = [...prev];
              next[keyIndex] = {
                status: 'error',
                statusCode: 200,
                elapsedMs,
                message: t('ai_providers.health_check_error_empty_models', {
                  defaultValue: '未返回模型列表',
                }),
              };
              return next;
            });
            return false;
          }
          setOpenAIKeyStatuses((prev) => {
            const next = [...prev];
            next[keyIndex] = {
              status: 'success',
              statusCode: 200,
              elapsedMs,
              message: t('ai_providers.connectivity_models_probe_success', {
                defaultValue: '成功获取 {{count}} 个模型{{preview}}',
                count: models.length,
                preview: '',
              }),
            };
            return next;
          });
          return true;
        } catch (err: unknown) {
          if (!isCurrent()) return false;
          const elapsedMs = Date.now() - startedAt;
          const rawMsg = getErrorMessage(err) || t('ai_providers.openai_test_failed');
          const statusMatch = rawMsg.match(/\b(4\d{2}|5\d{2})\b/);
          const statusCode = statusMatch ? Number(statusMatch[1]) : undefined;
          setOpenAIKeyStatuses((prev) => {
            const next = [...prev];
            next[keyIndex] = {
              status: 'error',
              statusCode,
              elapsedMs,
              message: rawMsg,
            };
            return next;
          });
          return false;
        }
      }

      const modelName = connectivityTestModel.trim() || availableModels[0] || '';
      if (!modelName) {
        const msg = t('ai_providers.openai_test_model_required');
        if (isCurrent()) {
          setOpenAIKeyStatuses((prev) => {
            const next = [...prev];
            next[keyIndex] = { status: 'error', message: msg };
            return next;
          });
        }
        return false;
      }

      const headers: Record<string, string> = {
        'Content-Type': 'application/json',
        ...mergedHeaders,
      };
      if (!hasHeader(headers, 'authorization') && (keyEntry.apiKey.trim() || keyAuthIndex)) {
        headers.Authorization = keyAuthIndex
          ? 'Bearer $TOKEN$'
          : `Bearer ${keyEntry.apiKey.trim()}`;
      }

      if (isCurrent()) {
        setOpenAIKeyStatuses((prev) => {
          const next = [...prev];
          next[keyIndex] = { status: 'loading', message: '' };
          return next;
        });
      }
      const startedAt = Date.now();
      try {
        const result = await apiCallApi.request(
          {
            authIndex: keyAuthIndex,
            proxyUrl: keyEntry.proxyUrl?.trim() || undefined,
            method: 'POST',
            url: endpoint,
            header: Object.keys(headers).length ? headers : undefined,
            data: JSON.stringify({
              model: modelName,
              messages: [{ role: 'user', content: 'Hi' }],
              stream: false,
              max_tokens: 5,
            }),
          },
          { timeout: TEST_TIMEOUT_MS }
        );
        if (!isCurrent()) return false;
        const elapsedMs = Date.now() - startedAt;
        if (result.statusCode < 200 || result.statusCode >= 300) {
          const errorSummary = extractCleanApiErrorSummary(result);
          const responseBody = extractFormattedResponseBody(result);
          setOpenAIKeyStatuses((prev) => {
            const next = [...prev];
            next[keyIndex] = {
              status: 'error',
              statusCode: result.statusCode,
              elapsedMs,
              message: errorSummary,
              responseBody,
            };
            return next;
          });
          return false;
        }

        setOpenAIKeyStatuses((prev) => {
          const next = [...prev];
          next[keyIndex] = {
            status: 'success',
            statusCode: result.statusCode,
            elapsedMs,
            message: t('ai_providers.openai_test_success'),
          };
          return next;
        });
        return true;
      } catch (err: unknown) {
        if (!isCurrent()) return false;
        const elapsedMs = Date.now() - startedAt;
        const message = getErrorMessage(err);
        const errCode =
          typeof err === 'object' && err !== null && 'code' in err ? String(err.code ?? '') : '';
        const isTimeout =
          errCode === 'ECONNABORTED' || message.toLowerCase().includes('timeout');
        setOpenAIKeyStatuses((prev) => {
          const next = [...prev];
          next[keyIndex] = {
            status: 'error',
            elapsedMs,
            message: isTimeout
              ? t('ai_providers.openai_test_timeout', { seconds: TEST_TIMEOUT_MS / 1000 })
              : message || t('ai_providers.openai_test_failed'),
          };
          return next;
        });
        return false;
      }
    },
    [
      availableModels,
      connectivityTestModel,
      effectiveProbeMode,
      form.apiKeyEntries,
      form.baseUrl,
      form.headers,
      t,
    ]
  );

  const testSingleOpenAIKey = useCallback(
    async (keyIndex: number): Promise<boolean> => {
      if (isTesting) return false;
      const requestId = ++connectivityRequestIdRef.current;
      const isCurrent = () =>
        !unmountedRef.current && connectivityRequestIdRef.current === requestId;
      setIsTesting(true);
      try {
        return await runOpenAISingleKeyTest(keyIndex, requestId);
      } finally {
        if (isCurrent()) setIsTesting(false);
      }
    },
    [isTesting, runOpenAISingleKeyTest]
  );

  const testAllOpenAIKeys = useCallback(async () => {
    if (isTesting) return;
    const baseUrl = form.baseUrl.trim();
    const targetEndpoint =
      effectiveProbeMode === 'models'
        ? buildOpenAIModelsEndpoint(baseUrl)
        : buildOpenAIChatCompletionsEndpoint(baseUrl);
    if (!baseUrl || !targetEndpoint) {
      const msg = t('notification.openai_test_url_required');
      setOpenAIAllTestResult({ status: 'error', message: msg });
      return;
    }
    if (effectiveProbeMode === 'inference') {
      const modelName = connectivityTestModel.trim() || availableModels[0] || '';
      if (!modelName) {
        const msg = t('ai_providers.openai_test_model_required');
        setOpenAIAllTestResult({ status: 'error', message: msg });
        return;
      }
    }

    const testableIndexes = getOpenAITestableKeyIndexes(form.apiKeyEntries);
    if (testableIndexes.length === 0) return;

    const requestId = ++connectivityRequestIdRef.current;
    const isCurrent = () =>
      !unmountedRef.current && connectivityRequestIdRef.current === requestId;

    setIsTesting(true);
    setOpenAIAllTestResult({
      status: 'loading',
      message: t('ai_providers.openai_test_running'),
      endpoint: targetEndpoint,
    });
    setOpenAIKeyStatuses([]);
    const startedAt = Date.now();
    try {
      const results = await Promise.all(
        testableIndexes.map((idx) => runOpenAISingleKeyTest(idx, requestId))
      );
      if (!isCurrent()) return;
      const elapsedMs = Date.now() - startedAt;
      const successCount = results.filter(Boolean).length;
      const failCount = testableIndexes.length - successCount;
      if (failCount === 0) {
        setOpenAIAllTestResult({
          status: 'success',
          elapsedMs,
          message: t('ai_providers.openai_test_all_success', { count: successCount }),
          endpoint: targetEndpoint,
        });
      } else if (successCount === 0) {
        setOpenAIAllTestResult({
          status: 'error',
          elapsedMs,
          message: t('ai_providers.openai_test_all_failed', { count: failCount }),
          endpoint: targetEndpoint,
        });
      } else {
        setOpenAIAllTestResult({
          status: 'error',
          elapsedMs,
          message: t('ai_providers.openai_test_all_partial', {
            success: successCount,
            failed: failCount,
          }),
          endpoint: targetEndpoint,
        });
      }
    } finally {
      if (isCurrent()) setIsTesting(false);
    }
  }, [
    availableModels,
    connectivityTestModel,
    effectiveProbeMode,
    form.apiKeyEntries,
    form.baseUrl,
    isTesting,
    runOpenAISingleKeyTest,
    t,
  ]);

  const runSingleConnectivityTest = useCallback(async () => {
    if (isTesting || activeKind === 'openai') return;
    const customHeaders = buildHeaderObject(form.headers);
    const apiKey = form.apiKey.trim();
    const apiKeyChanged = apiKey !== baseline.apiKey;
    const keyAuthIndex = apiKeyChanged
      ? undefined
      : (normalizeAuthIndex(form.authIndex) ?? undefined);
    const wasAlreadyKeyless =
      mode === 'view-edit' && row !== null && !String(row.raw.apiKey ?? '').trim();
    const keylessBaseUrlAllowed =
      activeKind !== 'vertex' &&
      (Boolean(v8AliasSupported) || wasAlreadyKeyless) &&
      Boolean(form.baseUrl.trim());

    if (effectiveProbeMode === 'inference' && activeKind === 'claude') {
      const modelName = connectivityTestModel.trim() || availableModels[0] || '';
      if (!modelName) {
        const message = t('ai_providers.claude_test_model_required');
        setSingleTestResult({ status: 'error', message });
        return;
      }
      const endpoint = buildClaudeMessagesEndpoint(form.baseUrl ?? '');
      if (!endpoint) {
        const message = t('ai_providers.claude_test_endpoint_invalid');
        setSingleTestResult({ status: 'error', message });
        return;
      }
      const authResolution = buildClaudeRequestHeaders({
        url: endpoint,
        apiKey: apiKey || undefined,
        authIndex: keyAuthIndex,
        customHeaders,
        contentType: 'application/json',
      });
      if (
        !keylessBaseUrlAllowed &&
        !authResolution.effectiveXApiKey &&
        !authResolution.effectiveAuthorization
      ) {
        const message = t('ai_providers.claude_test_key_required');
        setSingleTestResult({ status: 'error', message, endpoint });
        return;
      }

      const requestId = ++connectivityRequestIdRef.current;
      const isCurrent = () =>
        !unmountedRef.current && connectivityRequestIdRef.current === requestId;

      setIsTesting(true);
      setSingleTestResult({
        status: 'loading',
        message: t('ai_providers.claude_test_running'),
        endpoint,
      });
      const startedAt = Date.now();
      try {
        const result = await apiCallApi.request(
          {
            method: 'POST',
            authIndex: keyAuthIndex,
            proxyUrl: form.proxyUrl?.trim() || undefined,
            url: endpoint,
            header: authResolution.headers,
            data: JSON.stringify({
              model: modelName,
              max_tokens: 8,
              messages: [{ role: 'user', content: 'Hi' }],
            }),
          },
          { timeout: TEST_TIMEOUT_MS }
        );
        if (!isCurrent()) return;
        const elapsedMs = Date.now() - startedAt;
        if (result.statusCode < 200 || result.statusCode >= 300) {
          const errMessage = extractCleanApiErrorSummary(result);
          const responseBody = extractFormattedResponseBody(result);
          const includeAuthDiag =
            result.statusCode === 401 || errMessage.toLowerCase().includes('x-api-key');
          const hasCustomAuthHeader =
            hasHeader(customHeaders, 'x-api-key') || hasHeader(customHeaders, 'authorization');
          const authDiagnostic: ProviderConnectivityAuthDiagnostic | undefined = includeAuthDiag
            ? {
                headerType: authResolution.effectiveXApiKey
                  ? 'x-api-key'
                  : authResolution.effectiveAuthorization
                    ? 'authorization-bearer'
                    : 'none',
                credentialSource: apiKey
                  ? 'api-key'
                  : keyAuthIndex
                    ? 'auth-index'
                    : hasCustomAuthHeader
                      ? 'custom-header'
                      : 'none',
                endpointType: authResolution.baseType,
              }
            : undefined;
          setSingleTestResult({
            status: 'error',
            statusCode: result.statusCode,
            elapsedMs,
            message: errMessage,
            endpoint,
            responseBody,
            authDiagnostic,
          });
          return;
        }
        const okMessage = t('ai_providers.claude_test_success');
        setSingleTestResult({
          status: 'success',
          statusCode: result.statusCode,
          elapsedMs,
          message: okMessage,
          endpoint,
        });
      } catch (err: unknown) {
        if (!isCurrent()) return;
        const elapsedMs = Date.now() - startedAt;
        const message = getErrorMessage(err);
        const errCode =
          typeof err === 'object' && err !== null && 'code' in err ? String(err.code ?? '') : '';
        const isTimeout =
          errCode === 'ECONNABORTED' || message.toLowerCase().includes('timeout');
        const resolvedMessage = isTimeout
          ? t('ai_providers.claude_test_timeout', { seconds: TEST_TIMEOUT_MS / 1000 })
          : `${t('ai_providers.claude_test_failed')}: ${message || t('common.unknown_error')}`;
        setSingleTestResult({ status: 'error', elapsedMs, message: resolvedMessage, endpoint });
      } finally {
        if (isCurrent()) setIsTesting(false);
      }
      return;
    }

    if (effectiveProbeMode === 'inference' && (activeKind === 'codex' || activeKind === 'xai')) {
      const isXAI = activeKind === 'xai';
      const endpoint = buildCodexResponsesEndpoint(form.baseUrl ?? '');
      if (!endpoint) {
        const message = t('ai_providers.codex_test_endpoint_invalid');
        setSingleTestResult({ status: 'error', message });
        return;
      }
      const modelName = connectivityTestModel.trim() || availableModels[0] || '';
      if (!modelName) {
        const message = t('ai_providers.codex_test_model_required');
        setSingleTestResult({ status: 'error', message, endpoint });
        return;
      }
      const hasAuthorization = hasHeader(customHeaders, 'authorization');
      if (!keylessBaseUrlAllowed && !apiKey && !hasAuthorization && !keyAuthIndex) {
        const message = t(
          isXAI ? 'ai_providers.xai_test_key_required' : 'ai_providers.codex_test_key_required'
        );
        setSingleTestResult({ status: 'error', message, endpoint });
        return;
      }
      const headers: Record<string, string> = {
        'Content-Type': 'application/json',
        ...customHeaders,
      };
      if (!hasHeader(headers, 'authorization') && (apiKey || keyAuthIndex)) {
        headers.Authorization = keyAuthIndex ? 'Bearer $TOKEN$' : `Bearer ${apiKey}`;
      }

      const requestId = ++connectivityRequestIdRef.current;
      const isCurrent = () =>
        !unmountedRef.current && connectivityRequestIdRef.current === requestId;

      setIsTesting(true);
      setSingleTestResult({
        status: 'loading',
        message: t(isXAI ? 'ai_providers.xai_test_running' : 'ai_providers.codex_test_running'),
        endpoint,
      });
      const startedAt = Date.now();
      try {
        const result = await apiCallApi.request(
          {
            authIndex: keyAuthIndex,
            proxyUrl: form.proxyUrl?.trim() || undefined,
            method: 'POST',
            url: endpoint,
            header: headers,
            data: JSON.stringify({
              model: modelName,
              input: 'Hi',
              stream: false,
            }),
          },
          { timeout: CODEX_TEST_TIMEOUT_MS }
        );
        if (!isCurrent()) return;
        const elapsedMs = Date.now() - startedAt;
        if (result.statusCode < 200 || result.statusCode >= 300) {
          const errMsg = extractCleanApiErrorSummary(result);
          const responseBody = extractFormattedResponseBody(result);
          setSingleTestResult({
            status: 'error',
            statusCode: result.statusCode,
            elapsedMs,
            message: errMsg,
            endpoint,
            responseBody,
          });
          return;
        }
        const okMessage = t(
          isXAI ? 'ai_providers.xai_test_success' : 'ai_providers.codex_test_success'
        );
        setSingleTestResult({
          status: 'success',
          statusCode: result.statusCode,
          elapsedMs,
          message: okMessage,
          endpoint,
        });
      } catch (err: unknown) {
        if (!isCurrent()) return;
        const elapsedMs = Date.now() - startedAt;
        const failureText = t(
          isXAI ? 'ai_providers.xai_test_failed' : 'ai_providers.codex_test_failed'
        );
        const message = getErrorMessage(err) || failureText;
        setSingleTestResult({ status: 'error', elapsedMs, message, endpoint });
      } finally {
        if (isCurrent()) setIsTesting(false);
      }
      return;
    }

    // Models-endpoint probe (gemini / interactions / vertex, OR claude / codex / xai in 'models' probe mode)
    const hasAuthHeader =
      hasHeader(customHeaders, 'authorization') ||
      hasHeader(customHeaders, 'x-api-key') ||
      hasHeader(customHeaders, 'x-goog-api-key');
    if (!keylessBaseUrlAllowed && !apiKey && !keyAuthIndex && !hasAuthHeader) {
      const msg = t('ai_providers.health_check_error_missing_credential', {
        defaultValue: '缺少 API 密钥或 auth-index',
      });
      setSingleTestResult({ status: 'error', message: msg });
      return;
    }

    const endpoint =
      activeKind === 'claude'
        ? typeof modelsApi.buildClaudeModelsEndpoint === 'function'
          ? modelsApi.buildClaudeModelsEndpoint(form.baseUrl ?? '')
          : `${(form.baseUrl || 'https://api.anthropic.com').replace(/\/+$/, '')}/v1/models`
        : activeKind === 'codex' || activeKind === 'xai'
          ? buildOpenAIModelsEndpoint(
              form.baseUrl.trim() ||
                (activeKind === 'xai' ? 'https://api.x.ai/v1' : 'https://api.openai.com/v1')
            )
          : activeKind === 'vertex'
            ? form.baseUrl.trim()
            : typeof modelsApi.buildGeminiModelsEndpoint === 'function'
              ? modelsApi.buildGeminiModelsEndpoint(form.baseUrl ?? '')
              : form.baseUrl.trim();

    const requestId = ++connectivityRequestIdRef.current;
    const isCurrent = () =>
      !unmountedRef.current && connectivityRequestIdRef.current === requestId;

    setIsTesting(true);
    setSingleTestResult({
      status: 'loading',
      message: t('ai_providers.health_check_running', { defaultValue: '正在检测...' }),
      endpoint,
    });
    const startedAt = Date.now();
    try {
      let models: ModelInfo[] = [];
      if (activeKind === 'claude') {
        models = await modelsApi.fetchClaudeModelsViaApiCall(
          form.baseUrl ?? '',
          apiKey || undefined,
          customHeaders,
          keyAuthIndex,
          form.proxyUrl
        );
      } else if (activeKind === 'codex' || activeKind === 'xai') {
        models = await modelsApi.fetchV1ModelsViaApiCall(
          form.baseUrl.trim() ||
            (activeKind === 'xai' ? 'https://api.x.ai/v1' : 'https://api.openai.com/v1'),
          hasHeader(customHeaders, 'authorization') ? undefined : apiKey || undefined,
          customHeaders,
          keyAuthIndex,
          form.proxyUrl
        );
      } else if (activeKind === 'vertex') {
        try {
          models = await modelsApi.fetchV1ModelsViaApiCall(
            form.baseUrl ?? '',
            apiKey || undefined,
            customHeaders,
            keyAuthIndex,
            form.proxyUrl
          );
        } catch (firstErr) {
          if (!isCurrent()) return;
          try {
            models = await modelsApi.fetchModelsViaApiCall(
              form.baseUrl ?? '',
              apiKey || undefined,
              customHeaders,
              keyAuthIndex,
              form.proxyUrl
            );
          } catch (secondErr) {
            const detail = [getErrorMessage(firstErr), getErrorMessage(secondErr)]
              .filter(Boolean)
              .join(' / ');
            throw new Error(
              detail || t('ai_providers.health_check_error_vertex_standard_models', { reason: '' })
            );
          }
        }
      } else {
        models = await modelsApi.fetchGeminiModelsViaApiCall(
          form.baseUrl ?? '',
          apiKey || undefined,
          customHeaders,
          keyAuthIndex,
          form.proxyUrl
        );
      }
      if (!isCurrent()) return;
      const elapsedMs = Date.now() - startedAt;
      const previewNames = models
        .slice(0, 4)
        .map((model) => model.name)
        .filter(Boolean)
        .join(', ');
      const message =
        models.length > 0
          ? t('ai_providers.connectivity_models_probe_success', {
              defaultValue: '成功获取 {{count}} 个模型{{preview}}',
              count: models.length,
              preview: previewNames ? ` (${previewNames})` : '',
            })
          : t('ai_providers.health_check_error_empty_models', { defaultValue: '未返回模型列表' });

      setSingleTestResult({
        status: models.length > 0 ? 'success' : 'error',
        statusCode: 200,
        elapsedMs,
        message,
        endpoint,
      });
    } catch (err: unknown) {
      if (!isCurrent()) return;
      const elapsedMs = Date.now() - startedAt;
      const message = getErrorMessage(err) || t('common.unknown_error');
      const statusMatch = message.match(/\b(4\d{2}|5\d{2})\b/);
      const statusCode = statusMatch ? Number(statusMatch[1]) : undefined;
      setSingleTestResult({
        status: 'error',
        statusCode,
        elapsedMs,
        message,
        endpoint,
      });
    } finally {
      if (isCurrent()) setIsTesting(false);
    }
  }, [
    activeKind,
    availableModels,
    baseline.apiKey,
    connectivityTestModel,
    effectiveProbeMode,
    form.apiKey,
    form.authIndex,
    form.baseUrl,
    form.headers,
    form.proxyUrl,
    isTesting,
    mode,
    row,
    t,
    v8AliasSupported,
  ]);

  // Save flow
  const executeWithLock = useCallback(
    async <T>(lockKey: string, task: () => Promise<T>) => {
      if (runWithMutationLock) {
        return runWithMutationLock(lockKey, task);
      }
      if (fallbackMutationLocks.has(lockKey)) {
        return { executed: false as const };
      }
      fallbackMutationLocks.add(lockKey);
      try {
        const value = await task();
        return { executed: true as const, value };
      } finally {
        fallbackMutationLocks.delete(lockKey);
      }
    },
    [runWithMutationLock]
  );

  const handleSave = useCallback(async () => {
    if (!canSave || rowDeletedExternally) return;
    setError('');
    const sessionTokenAtStart = sessionTokenRef.current;

    if (activeKind === 'openai') {
      const name = form.name.trim();
      const baseUrl = form.baseUrl.trim();
      if (!name || !baseUrl) {
        showNotification(t('notification.openai_provider_required'), 'error');
        return;
      }

      const anchor = committedAnchorRef.current;
      const anchorApplies =
        mode === 'view-edit' &&
        row !== null &&
        row.kind === 'openai' &&
        anchor !== null &&
        isStaleRowRelativeToAnchor(row.kind, row, anchor);
      const incomingEtag = row?.raw.v8Source?.etag?.trim();
      const effectiveAnchorV8Source =
        anchorApplies &&
        incomingEtag &&
        incomingEtag !== anchor.preSaveEtag?.trim() &&
        incomingEtag !== anchor.v8Source?.etag?.trim()
          ? row.raw.v8Source
          : anchor?.v8Source;
      const effectiveRow = anchorApplies
        ? {
            ...row,
            originalIndex: anchor.originalIndex ?? row.originalIndex,
            baseUrl: anchor.baseUrl ?? row.baseUrl,
            raw: {
              ...row.raw,
              ...(anchor.name !== undefined ? { name: anchor.name } : {}),
              ...(anchor.baseUrl !== undefined ? { baseUrl: anchor.baseUrl } : {}),
              ...(anchor.authIndex !== undefined ? { authIndex: anchor.authIndex } : {}),
              ...(effectiveAnchorV8Source !== undefined
                ? { v8Source: effectiveAnchorV8Source }
                : {}),
            },
          }
        : row;
      const lockKey =
        mode === 'view-edit' && row
          ? `provider:save:${row.key}`
          : `provider:create:openai:${name}`;

      setSaving(true);
      try {
        const lockOutcome = await executeWithLock(lockKey, async () => {
          const latestList = (await providersApi.getOpenAIProviders().catch(() => [])) ?? [];
          const existingIndex =
            mode === 'view-edit' && effectiveRow && effectiveRow.kind === 'openai'
              ? resolveOpenAITargetIndex(latestList, effectiveRow)
              : -1;
          if (
            mode === 'view-edit' &&
            effectiveRow &&
            effectiveRow.kind === 'openai' &&
            latestList.length > 0 &&
            existingIndex < 0
          ) {
            throw new Error(
              t('notification.provider_conflict_refresh', {
                defaultValue: 'Provider configuration changed; refresh and try again.',
              })
            );
          }
          const matchedLatest = existingIndex >= 0 ? latestList[existingIndex] : undefined;
          const existing =
            matchedLatest ??
            (mode === 'view-edit' && effectiveRow?.kind === 'openai'
              ? effectiveRow.raw
              : undefined);
          const callerV8Source = resolveCallerV8Source(
            mode,
            row,
            effectiveRow,
            anchorApplies ? anchor : null,
            baseline,
            form.v8Source
          );
          const v8Source = callerV8Source
            ? {
                ...(matchedLatest?.v8Source ?? {}),
                ...callerV8Source,
                etag: callerV8Source.etag ?? matchedLatest?.v8Source?.etag,
              }
            : matchedLatest?.v8Source;

          const payload: OpenAIProviderConfig = {
            ...(v8Source ? { v8Source } : {}),
            name,
            prefix: form.prefix.trim() || undefined,
            baseUrl,
            headers: buildHeaderObject(form.headers),
            apiKeyEntries: form.apiKeyEntries
              .filter((entry) => hasOpenAIKeyEntryConfiguration(entry))
              .map((entry) =>
                preservePersistedOpenAIKeyEntry(entry, {
                  apiKey: entry.apiKey.trim(),
                  weight: normalizeCredentialWeight(entry.weight),
                  proxyUrl: entry.proxyUrl?.trim() || undefined,
                  authIndex: normalizeAuthIndex(entry.authIndex) ?? undefined,
                })
              ),
            disableCooling: coolingPolicyToOverride(form.disableCooling),
          };
          if (form.priority !== undefined && Number.isFinite(form.priority)) {
            payload.priority = Math.trunc(form.priority);
          }
          if (existing?.disabled !== undefined) {
            payload.disabled = existing.disabled;
          }
          const resolvedTestModel = form.testModel.trim();
          if (resolvedTestModel) {
            payload.testModel = resolvedTestModel;
          }
          const models = entriesToProviderModels(form.modelEntries);
          if (models.length) {
            payload.models = models;
          }
          const committedPayload = toCommittedOpenAIProviderSnapshot(payload);
          const targetIndex =
            existingIndex >= 0
              ? existingIndex
              : effectiveRow && effectiveRow.kind === 'openai'
                ? effectiveRow.originalIndex
                : 0;
          const originalName =
            existing?.name ??
            (effectiveRow && effectiveRow.kind === 'openai' ? effectiveRow.raw.name : '');

          let writeResult: unknown;
          if (mode === 'view-edit' && effectiveRow && effectiveRow.kind === 'openai') {
            if (typeof providersApi.updateOpenAIProvider === 'function') {
              writeResult = await providersApi.updateOpenAIProvider(
                originalName,
                targetIndex,
                committedPayload
              );
            } else {
              const nextList = latestList.length
                ? latestList.map((item, idx) => (idx === targetIndex ? committedPayload : item))
                : [committedPayload];
              writeResult = await providersApi.saveOpenAIProviders(nextList);
            }
          } else if (typeof providersApi.createOpenAIProvider === 'function') {
            writeResult = await providersApi.createOpenAIProvider(committedPayload);
          } else {
            writeResult = await providersApi.saveOpenAIProviders([...latestList, committedPayload]);
          }

          let syncedProviders: OpenAIProviderConfig[];
          try {
            const refreshed = await providersApi.getOpenAIProviders();
            syncedProviders = Array.isArray(refreshed)
              ? refreshed.map(toCommittedOpenAIProviderSnapshot)
              : [committedPayload];
          } catch {
            syncedProviders =
              mode === 'view-edit' && effectiveRow && effectiveRow.kind === 'openai'
                ? latestList.map((item, idx) =>
                    idx === targetIndex ? committedPayload : item
                  )
                : [...latestList, committedPayload];
          }
          const matchedRefreshedIndex = findMatchedOpenAIIndex(
            syncedProviders,
            latestList,
            committedPayload,
            existing,
            v8Source,
            targetIndex,
            mode
          );
          const matchedRefreshed =
            matchedRefreshedIndex >= 0 ? syncedProviders[matchedRefreshedIndex] : undefined;
          const prevEtag = existing?.v8Source?.etag ?? v8Source?.etag;
          const refreshedItemEtag =
            matchedRefreshed?.v8Source?.etag ??
            syncedProviders.find((item) => item.v8Source?.etag)?.v8Source?.etag;
          const writeEtag = extractWriteResultEtag(writeResult) ?? readLatestV8ConfigEtag();
          const latestRefreshedEtag =
            (refreshedItemEtag && refreshedItemEtag !== prevEtag ? refreshedItemEtag : undefined) ??
            writeEtag ??
            refreshedItemEtag;
          const resolvedSavedName =
            mode === 'view-edit'
              ? resolveRefreshedName(
                  originalName,
                  committedPayload.name,
                  matchedRefreshed?.name,
                  prevEtag,
                  matchedRefreshed?.v8Source?.etag ?? latestRefreshedEtag
                )
              : String(matchedRefreshed?.name ?? committedPayload.name).trim() ||
                committedPayload.name;
          const fallbackV8Source: ProviderV8GroupSource = v8Source ?? {
            family: 'openai-compatibility',
            groupIndex: targetIndex,
            group: existing?.v8Source?.group ?? {},
          };
          const baseCommittedV8Source =
            matchedRefreshed?.v8Source ??
            (latestRefreshedEtag
              ? { ...fallbackV8Source, etag: latestRefreshedEtag }
              : v8Source);
          const committedV8Source =
            baseCommittedV8Source && latestRefreshedEtag
              ? { ...baseCommittedV8Source, etag: latestRefreshedEtag }
              : baseCommittedV8Source;
          const resolvedIndex =
            matchedRefreshedIndex >= 0
              ? matchedRefreshedIndex
              : mode === 'view-edit'
                ? targetIndex
                : undefined;

          if (
            mode === 'view-edit' &&
            !unmountedRef.current &&
            sessionTokenRef.current === sessionTokenAtStart
          ) {
            const preSaveBaseline = row
              ? buildBaseline(row.kind, buildFormFromRow(row), row.enabled)
              : undefined;
            const committedForm: ProviderDrawerFormState = {
              ...form,
              name: resolvedSavedName,
              v8Source: committedV8Source,
              modelEntries: form.modelEntries.map(toCommittedModelThinkingSnapshot),
            };
            const committedBaseline = buildBaseline('openai', committedForm, row?.enabled);
            if (row && preSaveBaseline) {
              committedAnchorRef.current = {
                sourceRowKey: row.key,
                sourceRaw: row.raw,
                kind: row.kind,
                name: resolvedSavedName,
                baseUrl: matchedRefreshed?.baseUrl ?? committedPayload.baseUrl,
                authIndex: matchedRefreshed?.authIndex ?? committedPayload.authIndex,
                v8Source: committedV8Source,
                originalIndex: resolvedIndex,
                preSaveEtag: row.raw.v8Source?.etag,
                preSaveEnabled: row.enabled,
                preSaveBaseline,
                committedForm: cloneDrawerFormState(committedForm),
                committedBaseline,
              };
            }
            setForm(committedForm);
            setBaseline(committedBaseline);
          }
          const savedResult = await onSaved('openai', {
            name: resolvedSavedName,
            baseUrl: matchedRefreshed?.baseUrl ?? committedPayload.baseUrl,
            prefix: matchedRefreshed?.prefix ?? committedPayload.prefix,
            authIndex: matchedRefreshed?.authIndex ?? committedPayload.authIndex,
            v8Source: committedV8Source,
            originalIndex: resolvedIndex,
          });
          if (savedResult !== true) {
            const syncIndex =
              matchedRefreshedIndex >= 0
                ? matchedRefreshedIndex
                : mode === 'view-edit'
                  ? targetIndex
                  : syncedProviders.length > latestList.length
                    ? syncedProviders.length - 1
                    : -1;
            const nextSyncedProviders =
              committedV8Source &&
              syncIndex >= 0 &&
              syncIndex < syncedProviders.length &&
              syncedProviders[syncIndex]?.v8Source?.etag !== committedV8Source.etag
                ? syncedProviders.map((item, idx) =>
                    idx === syncIndex ? { ...item, v8Source: committedV8Source } : item
                  )
                : syncedProviders;
            updateConfigValue(CONFIG_SECTION_BY_KIND.openai, nextSyncedProviders);
            clearCache(CONFIG_SECTION_BY_KIND.openai);
          }
          return committedPayload;
        });

        if (!lockOutcome.executed) return;

        showNotification(
          mode === 'view-edit'
            ? t('notification.openai_provider_updated')
            : t('notification.openai_provider_added'),
          'success'
        );

        if (
          mode === 'create' &&
          !unmountedRef.current &&
          sessionTokenRef.current === sessionTokenAtStart &&
          latestSessionStateRef.current.open &&
          latestSessionStateRef.current.mode === 'create' &&
          latestSessionStateRef.current.activeKind === 'openai'
        ) {
          onClose();
        }
      } catch (err: unknown) {
        const message = getErrorMessage(err);
        if (!unmountedRef.current && sessionTokenRef.current === sessionTokenAtStart) {
          setError(message);
        }
        showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
      } finally {
        if (!unmountedRef.current) setSaving(false);
      }
      return;
    }

    // Single-key providers: gemini | interactions | codex | xai | claude | vertex
    const apiKey = form.apiKey.trim();
    const baseUrl = form.baseUrl.trim();
    const apiKeyChanged = apiKey !== baseline.apiKey;
    const effectiveAuthIndex = apiKeyChanged
      ? undefined
      : (normalizeAuthIndex(form.authIndex) ?? undefined);
    const hasAuthIndex = Boolean(effectiveAuthIndex);
    const wasAlreadyKeyless =
      mode === 'view-edit' && row !== null && !String(row.raw.apiKey ?? '').trim();

    if (
      activeKind === 'claude' ||
      activeKind === 'codex' ||
      activeKind === 'xai' ||
      activeKind === 'vertex'
    ) {
      if (!baseUrl) {
        const urlErrorKey =
          activeKind === 'claude'
            ? 'ai_providers.claude_base_url_required'
            : activeKind === 'xai'
              ? 'notification.xai_base_url_required'
              : activeKind === 'vertex'
                ? 'ai_providers.vertex_base_url_required'
                : 'notification.codex_base_url_required';
        showNotification(t(urlErrorKey), 'error');
        return;
      }
    }

    const keylessAllowed =
      hasAuthIndex ||
      wasAlreadyKeyless ||
      (activeKind !== 'vertex' && Boolean(v8AliasSupported) && Boolean(baseUrl));

    if (!apiKey && !keylessAllowed) {
      const keyErrorKey =
        activeKind === 'claude'
          ? 'ai_providers.claude_key_required'
          : activeKind === 'xai'
            ? 'ai_providers.xai_key_required'
            : activeKind === 'codex'
              ? 'ai_providers.codex_key_required'
              : activeKind === 'vertex'
                ? 'ai_providers.vertex_key_required'
                : activeKind === 'interactions'
                  ? 'ai_providers.interactions_key_required'
                  : 'ai_providers.gemini_key_required';
      showNotification(t(keyErrorKey), 'error');
      return;
    }

    const lockKey =
      mode === 'view-edit' && row ? `provider:save:${row.key}` : `provider:create:${activeKind}`;
    const sectionKey = CONFIG_SECTION_BY_KIND[activeKind];

    setSaving(true);
    try {
      const anchor = committedAnchorRef.current;
      const anchorApplies =
        mode === 'view-edit' &&
        row !== null &&
        row.kind !== 'openai' &&
        anchor !== null &&
        isStaleRowRelativeToAnchor(row.kind, row, anchor);
      const incomingEtag = row?.raw.v8Source?.etag?.trim();
      const effectiveAnchorV8Source =
        anchorApplies &&
        incomingEtag &&
        incomingEtag !== anchor.preSaveEtag?.trim() &&
        incomingEtag !== anchor.v8Source?.etag?.trim()
          ? row.raw.v8Source
          : anchor?.v8Source;
      const effectiveRow = anchorApplies
        ? {
            ...row,
            originalIndex: anchor.originalIndex ?? row.originalIndex,
            baseUrl: anchor.baseUrl ?? row.baseUrl,
            raw: {
              ...row.raw,
              ...(anchor.name !== undefined ? { name: anchor.name } : {}),
              ...(anchor.apiKey !== undefined ? { apiKey: anchor.apiKey } : {}),
              ...(anchor.baseUrl !== undefined ? { baseUrl: anchor.baseUrl } : {}),
              ...(anchor.authIndex !== undefined ? { authIndex: anchor.authIndex } : {}),
              ...(effectiveAnchorV8Source !== undefined
                ? { v8Source: effectiveAnchorV8Source }
                : {}),
            },
          }
        : row;
      const lockOutcome = await executeWithLock(lockKey, async () => {
        const latestList = await fetchSingleKeyConfigs(activeKind);
        const targetIndex =
          mode === 'view-edit' && effectiveRow && effectiveRow.kind !== 'openai'
            ? resolveSingleKeyTargetIndex(latestList, effectiveRow)
            : -1;
        if (
          mode === 'view-edit' &&
          effectiveRow &&
          effectiveRow.kind !== 'openai' &&
          latestList.length > 0 &&
          targetIndex < 0
        ) {
          throw new Error(
            t('notification.provider_conflict_refresh', {
              defaultValue: 'Provider configuration changed; refresh and try again.',
            })
          );
        }
        const matchedLatest = targetIndex >= 0 ? latestList[targetIndex] : undefined;
        const baseExisting =
          matchedLatest ??
          (mode === 'view-edit' && effectiveRow && effectiveRow.kind !== 'openai'
            ? effectiveRow.raw
            : undefined);
        const callerV8Source = resolveCallerV8Source(
          mode,
          row,
          effectiveRow,
          anchorApplies ? anchor : null,
          baseline,
          form.v8Source
        );
        const resolvedV8Source = callerV8Source
          ? {
              ...(matchedLatest?.v8Source ?? {}),
              ...callerV8Source,
              etag: callerV8Source.etag ?? matchedLatest?.v8Source?.etag,
            }
          : matchedLatest?.v8Source;
        const existingItem = baseExisting
          ? resolvedV8Source
            ? { ...baseExisting, v8Source: resolvedV8Source }
            : baseExisting
          : undefined;

        const parsedExclusions = parseExcludedModels(form.excludedText);
        // Critical invariant: editing a disabled provider preserves the '*' disable-all rule
        // even though '*' is stripped from the editable textarea.
        const excludedModels =
          mode === 'view-edit' && effectiveRow && !effectiveRow.enabled
            ? withDisableAllModelsRule(parsedExclusions)
            : stripDisableAllModelsRule(parsedExclusions);

        const commonPayload: GeminiKeyConfig & ProviderKeyConfig = {
          ...(resolvedV8Source ? { v8Source: resolvedV8Source } : {}),
          name: form.name.trim() || undefined,
          apiKey,
          priority:
            form.priority !== undefined && Number.isFinite(form.priority)
              ? Math.trunc(form.priority)
              : undefined,
          weight: normalizeCredentialWeight(form.weight),
          prefix: form.prefix.trim() || undefined,
          baseUrl: baseUrl || undefined,
          proxyUrl: form.proxyUrl.trim() || undefined,
          headers: buildHeaderObject(form.headers),
          excludedModels,
          authIndex: effectiveAuthIndex,
          disableCooling: coolingPolicyToOverride(form.disableCooling),
        };

        let payload: GeminiKeyConfig | ProviderKeyConfig = commonPayload;
        if (activeKind === 'gemini' || activeKind === 'interactions') {
          const normalizedEntries = form.modelEntries.map((entry) =>
            cloneModelEntry(entry, { name: stripGeminiModelResourceName(entry.name) })
          );
          payload = {
            ...commonPayload,
            models: entriesToProviderModels(normalizedEntries),
          };
        } else if (activeKind === 'codex' || activeKind === 'xai') {
          payload = {
            ...commonPayload,
            websockets: Boolean(form.websockets),
            models: entriesToProviderModels(form.modelEntries),
          };
        } else if (activeKind === 'claude') {
          const rawCloak =
            mode === 'view-edit' && effectiveRow?.raw && 'cloak' in effectiveRow.raw
              ? effectiveRow.raw.cloak
              : undefined;
          let resolvedCloak: CloakConfig | null | undefined;
          if (form.cloak) {
            const sensitiveWords = parseSensitiveWordsText(form.cloakSensitiveWordsInput);
            const nextCloak: CloakConfig = {
              ...(rawCloak ?? {}),
              ...form.cloak,
            };
            if (sensitiveWords.length > 0) {
              nextCloak.sensitiveWords = sensitiveWords;
            } else {
              delete nextCloak.sensitiveWords;
            }
            resolvedCloak = nextCloak;
          } else if (rawCloak) {
            resolvedCloak = null;
          }
          payload = {
            ...commonPayload,
            models: entriesToProviderModels(form.modelEntries, { preserveAliasForSameName: true }),
            ...(resolvedCloak !== undefined ? { cloak: resolvedCloak as CloakConfig } : {}),
            fingerprintProfile: form.fingerprintProfile,
            rebuildMidSystemMessage: form.rebuildMidSystemMessage,
          };
        } else if (activeKind === 'vertex') {
          payload = {
            ...commonPayload,
            models: entriesToProviderModels(form.modelEntries),
          };
        }

        const writeResult =
          mode === 'view-edit' && existingItem
            ? await updateSingleKeyConfig(activeKind, existingItem, payload, latestList, targetIndex)
            : await createSingleKeyConfig(activeKind, payload, latestList);
        let notificationKey = '';
        let notificationType: NotificationType = 'success';

        if (activeKind === 'claude') {
          const existingFingerprint = (existingItem as ProviderKeyConfig | undefined)
            ?.fingerprintProfile;
          const fingerprintExplicitlyChanged =
            mode === 'view-edit'
              ? existingFingerprint !== form.fingerprintProfile
              : form.fingerprintProfile !== undefined;

          notificationKey =
            mode === 'view-edit'
              ? 'notification.claude_config_updated'
              : 'notification.claude_config_added';

          if (fingerprintExplicitlyChanged && typeof readBackClaudeConfigAfterSave === 'function') {
            let readBack = null;
            try {
              readBack = await readBackClaudeConfigAfterSave();
            } catch {
              readBack = null;
            }
            if (!readBack) {
              notificationKey = 'notification.claude_fingerprint_verify_unavailable';
              notificationType = 'warning';
            } else {
              const verification = verifyClaudeFingerprintInRawConfig(
                readBack.rawRecords,
                form.fingerprintProfile,
                mode === 'view-edit'
                  ? {
                      mode: 'edit',
                      index:
                        targetIndex >= 0
                          ? targetIndex
                          : effectiveRow && effectiveRow.kind !== 'openai'
                            ? effectiveRow.originalIndex
                            : 0,
                      apiKey: payload.apiKey,
                      baseUrl: payload.baseUrl,
                    }
                  : { mode: 'create', apiKey: payload.apiKey, baseUrl: payload.baseUrl }
              );
              if (verification !== 'confirmed') {
                notificationKey = 'notification.claude_fingerprint_not_applied';
                notificationType = 'warning';
              }
            }
          }
        } else {
          const notificationByKind: Record<
            Exclude<ProviderKind, 'openai' | 'claude'>,
            { updated: string; added: string }
          > = {
            gemini: {
              updated: 'notification.gemini_key_updated',
              added: 'notification.gemini_key_added',
            },
            interactions: {
              updated: 'notification.interactions_key_updated',
              added: 'notification.interactions_key_added',
            },
            codex: {
              updated: 'notification.codex_config_updated',
              added: 'notification.codex_config_added',
            },
            xai: {
              updated: 'notification.xai_config_updated',
              added: 'notification.xai_config_added',
            },
            vertex: {
              updated: 'notification.vertex_config_updated',
              added: 'notification.vertex_config_added',
            },
          };
          notificationKey =
            mode === 'view-edit'
              ? notificationByKind[activeKind].updated
              : notificationByKind[activeKind].added;
        }

        const fallbackList =
          mode === 'view-edit'
            ? targetIndex >= 0
              ? latestList.map((item, idx) => (idx === targetIndex ? payload : item))
              : latestList.length > 0
                ? latestList
                : [payload]
            : [...latestList, payload];
        let syncedList = fallbackList;
        try {
          const refreshed = await fetchSingleKeyConfigs(activeKind);
          if (Array.isArray(refreshed) && refreshed.length > 0) {
            syncedList = refreshed;
          }
        } catch {
          syncedList = fallbackList;
        }
        const matchedRefreshedIndex = findMatchedSingleKeyIndex(
          syncedList,
          latestList,
          payload,
          existingItem,
          targetIndex,
          mode
        );
        const matchedRefreshed =
          matchedRefreshedIndex >= 0 ? syncedList[matchedRefreshedIndex] : undefined;
        const prevEtag = existingItem?.v8Source?.etag ?? resolvedV8Source?.etag;
        const refreshedItemEtag =
          matchedRefreshed?.v8Source?.etag ??
          syncedList.find((item) => item.v8Source?.etag)?.v8Source?.etag;
        const writeEtag = extractWriteResultEtag(writeResult) ?? readLatestV8ConfigEtag();
        const refreshedEtag =
          (refreshedItemEtag && refreshedItemEtag !== prevEtag ? refreshedItemEtag : undefined) ??
          writeEtag ??
          refreshedItemEtag;
        const resolvedSavedName =
          mode === 'view-edit'
            ? resolveRefreshedName(
                existingItem?.name,
                payload.name,
                matchedRefreshed?.name,
                prevEtag,
                matchedRefreshed?.v8Source?.etag ?? refreshedEtag
              )
            : String(matchedRefreshed?.name ?? payload.name ?? '').trim();
        const baseV8Source = resolvedV8Source ?? form.v8Source ?? payload.v8Source;
        const fallbackGroupIndex =
          targetIndex >= 0
            ? targetIndex
            : effectiveRow && effectiveRow.kind !== 'openai'
              ? effectiveRow.originalIndex
              : 0;
        const rawCommittedV8Source =
          matchedRefreshed?.v8Source ??
          (refreshedEtag
            ? {
                ...(baseV8Source ?? {
                  family: activeKind,
                  groupIndex: fallbackGroupIndex,
                  keyIndex: 0,
                  group: existingItem?.v8Source?.group ?? {},
                }),
                etag: refreshedEtag,
              }
            : baseV8Source);
        const committedV8Source =
          rawCommittedV8Source && refreshedEtag
            ? { ...rawCommittedV8Source, etag: refreshedEtag }
            : rawCommittedV8Source;
        const resolvedAuthIndex = matchedRefreshed?.authIndex ?? payload.authIndex ?? '';
        const resolvedIndex =
          matchedRefreshedIndex >= 0
            ? matchedRefreshedIndex
            : mode === 'view-edit' && targetIndex >= 0
              ? targetIndex
              : undefined;

        if (
          mode === 'view-edit' &&
          !unmountedRef.current &&
          sessionTokenRef.current === sessionTokenAtStart
        ) {
          const normalizedSensitiveWordsText = parseSensitiveWordsText(
            form.cloakSensitiveWordsInput
          ).join('\n');
          const preSaveBaseline = row
            ? buildBaseline(row.kind, buildFormFromRow(row), row.enabled)
            : undefined;
          const committedForm: ProviderDrawerFormState = {
            ...form,
            name: resolvedSavedName,
            apiKey,
            authIndex: resolvedAuthIndex,
            v8Source: committedV8Source,
            cloakSensitiveWordsInput: form.cloak ? normalizedSensitiveWordsText : '',
            cloak:
              activeKind === 'claude' && form.cloak
                ? {
                    ...form.cloak,
                    sensitiveWords: parseSensitiveWordsText(form.cloakSensitiveWordsInput),
                  }
                : form.cloak,
          };
          const committedBaseline = buildBaseline(activeKind, committedForm, row?.enabled);
          if (row && preSaveBaseline) {
            committedAnchorRef.current = {
              sourceRowKey: row.key,
              sourceRaw: row.raw,
              kind: row.kind,
              name: resolvedSavedName,
              apiKey: matchedRefreshed?.apiKey ?? payload.apiKey,
              baseUrl: matchedRefreshed?.baseUrl ?? payload.baseUrl,
              authIndex: resolvedAuthIndex,
              v8Source: committedV8Source,
              originalIndex: resolvedIndex,
              preSaveEtag: row.raw.v8Source?.etag,
              preSaveEnabled: row.enabled,
              preSaveBaseline,
              committedForm: cloneDrawerFormState(committedForm),
              committedBaseline,
            };
          }
          setForm(committedForm);
          setBaseline(committedBaseline);
        }
        const savedResult = await onSaved(activeKind, {
          name: resolvedSavedName || undefined,
          apiKey: matchedRefreshed?.apiKey ?? payload.apiKey,
          baseUrl: matchedRefreshed?.baseUrl ?? payload.baseUrl,
          proxyUrl: matchedRefreshed?.proxyUrl ?? payload.proxyUrl,
          prefix: matchedRefreshed?.prefix ?? payload.prefix,
          authIndex: matchedRefreshed?.authIndex ?? payload.authIndex,
          v8Source: committedV8Source,
          originalIndex: resolvedIndex,
        });
        if (savedResult !== true) {
          const syncIndex =
            matchedRefreshedIndex >= 0
              ? matchedRefreshedIndex
              : mode === 'view-edit'
                ? targetIndex
                : syncedList.length > latestList.length
                  ? syncedList.length - 1
                  : -1;
          const nextSyncedList =
            committedV8Source &&
            syncIndex >= 0 &&
            syncIndex < syncedList.length &&
            syncedList[syncIndex]?.v8Source?.etag !== committedV8Source.etag
              ? syncedList.map((item, idx) =>
                  idx === syncIndex ? { ...item, v8Source: committedV8Source } : item
                )
              : syncedList;
          updateConfigValue(sectionKey, nextSyncedList);
          clearCache(sectionKey);
        }
        return { payload, notificationKey, notificationType };
      });

      if (!lockOutcome.executed) return;

      showNotification(t(lockOutcome.value.notificationKey), lockOutcome.value.notificationType);

      if (
        mode === 'create' &&
        !unmountedRef.current &&
        sessionTokenRef.current === sessionTokenAtStart &&
        latestSessionStateRef.current.open &&
        latestSessionStateRef.current.mode === 'create' &&
        latestSessionStateRef.current.activeKind === activeKind
      ) {
        onClose();
      }
    } catch (err: unknown) {
      const message = getErrorMessage(err);
      if (!unmountedRef.current && sessionTokenRef.current === sessionTokenAtStart) {
        setError(message);
      }
      showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
    } finally {
      if (!unmountedRef.current) setSaving(false);
    }
  }, [
    activeKind,
    baseline,
    canSave,
    clearCache,
    executeWithLock,
    form,
    mode,
    onClose,
    onSaved,
    row,
    rowDeletedExternally,
    showNotification,
    t,
    updateConfigValue,
    v8AliasSupported,
  ]);

  const configuredModelCount = availableModels.length;
  const excludedModelCount = normalizedExcludedModels.length;

  return {
    form,
    setForm,
    saving,
    error,
    isConfigDirty,
    isModelsDirty,
    isDirty,
    canSave,
    hasInvalidWeight,
    hasInvalidThinking,
    availableModels,
    modelSelectOptions,
    configuredModelCount,
    excludedModelCount,
    resetForm,
    confirmDiscardChanges,
    handleSave,
    // Discovery
    modelDiscoveryOpen,
    setModelDiscoveryOpen,
    modelDiscoveryEndpoint,
    discoveredModels,
    modelsLoaded,
    modelDiscoveryFetching,
    modelDiscoveryError,
    modelDiscoverySearch,
    setModelDiscoverySearch,
    modelDiscoverySelected,
    discoveredModelsFiltered,
    configuredModelNames,
    visibleDiscoverableModelNames,
    allVisibleSelected,
    toggleModelDiscoverySelection,
    handleSelectVisibleModels,
    handleClearModelDiscoverySelection,
    fetchModelDiscovery,
    applySelectedDiscoveredModels,
    // Connectivity
    connectivityProbeMode: effectiveProbeMode,
    supportsInferenceProbe,
    setConnectivityProbeMode: (nextMode: ProviderConnectivityProbeMode) => {
      setConnectivityProbeModeState(nextMode);
      setSingleTestResult({ status: 'idle', message: '' });
      setOpenAIAllTestResult({ status: 'idle', message: '' });
      setOpenAIKeyStatuses([]);
    },
    connectivityTestModel,
    setConnectivityTestModel: (value: string) => {
      setTestModelManuallyEdited(true);
      setConnectivityTestModel(value);
      setSingleTestResult({ status: 'idle', message: '' });
      setOpenAIAllTestResult({ status: 'idle', message: '' });
    },
    singleTestResult,
    openAIAllTestResult,
    openAIKeyStatuses,
    setOpenAIKeyStatuses,
    isTesting,
    runSingleConnectivityTest,
    testSingleOpenAIKey,
    testAllOpenAIKeys,
  };
}
