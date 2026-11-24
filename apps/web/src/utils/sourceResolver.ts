import type { GeminiKeyConfig, OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import type { CredentialInfo, SourceInfo, SourceProviderEnabledState } from '@/types/sourceInfo';
import {
  buildCandidateUsageSourceIds,
  normalizeAuthIndex,
  normalizeUsageSourceId,
} from '@/utils/usage';

export interface SourceInfoMapInput {
  geminiApiKeys?: GeminiKeyConfig[];
  interactionsApiKeys?: GeminiKeyConfig[];
  claudeApiKeys?: ProviderKeyConfig[];
  codexApiKeys?: ProviderKeyConfig[];
  xaiApiKeys?: ProviderKeyConfig[];
  metaApiKeys?: ProviderKeyConfig[];
  vertexApiKeys?: ProviderKeyConfig[];
  openaiCompatibility?: OpenAIProviderConfig[];
}

type SourceInfoEntry = Required<Pick<SourceInfo, 'displayName' | 'type' | 'identityKey'>> &
  Pick<SourceInfo, 'providerEnabledState'>;

export interface SourceInfoMap {
  byAuthIndex: Map<string, SourceInfoEntry | null>;
  bySource: Map<string, SourceInfoEntry | null>;
  bySourceAndProvider?: Map<string, SourceInfoEntry | null>;
  byIdentityKey: Map<string, SourceInfoEntry>;
}

const buildProviderIdentityKey = (type: string, index: number | string) => `${type}:${index}`;
const buildProviderSourceKey = (provider: string, source: string) =>
  JSON.stringify([provider, source]);

const hasDisableAllModelsRule = (models?: string[]) =>
  Array.isArray(models) && models.some((model) => String(model ?? '').trim() === '*');

const buildProviderEnabledState = (enabled: boolean): SourceProviderEnabledState =>
  enabled ? 'enabled' : 'disabled';

const mergeProviderEnabledState = (
  left?: SourceProviderEnabledState,
  right?: SourceProviderEnabledState
): SourceProviderEnabledState | undefined => {
  if (!left) return right;
  if (!right || left === right) return left;
  return 'mixed';
};

const registerIdentity = (
  map: Map<string, SourceInfoEntry | null>,
  key: string | null | undefined,
  entry: SourceInfoEntry,
  groupNameByEntry?: WeakMap<SourceInfoEntry, string>
) => {
  if (!key) return;

  const existing = map.get(key);
  if (existing === undefined) {
    map.set(key, entry);
    return;
  }

  if (existing === null || existing.identityKey === entry.identityKey) return;
  const existingGroupName = groupNameByEntry?.get(existing);
  const entryGroupName = groupNameByEntry?.get(entry);
  const sharedDisplayName =
    existing.displayName === entry.displayName
      ? existing.displayName
      : existingGroupName && existingGroupName === entryGroupName
        ? existingGroupName
        : null;
  if (sharedDisplayName) {
    const mergedEntry: SourceInfoEntry = {
      displayName: sharedDisplayName,
      type: existing.type === entry.type ? existing.type : '',
      identityKey: `shared:${key}`,
      providerEnabledState: mergeProviderEnabledState(
        existing.providerEnabledState,
        entry.providerEnabledState
      ),
    };
    if (existingGroupName && existingGroupName === entryGroupName) {
      groupNameByEntry?.set(mergedEntry, existingGroupName);
    }
    map.set(key, mergedEntry);
    return;
  }
  map.set(key, null);
};

const formatRawSourceDisplayName = (source: string) => {
  if (!source) return '-';
  return source.startsWith('t:') ? source.slice(2) : source;
};

const extractHost = (baseUrl: string | undefined) => {
  const trimmed = String(baseUrl || '').trim();
  if (!trimmed) return '';

  try {
    return new URL(trimmed).host || trimmed;
  } catch {
    return trimmed.replace(/^https?:\/\//i, '').split('/')[0] || trimmed;
  }
};

const buildProviderDisplayNames = (
  items: Array<{ prefix?: string; name?: string; baseUrl?: string }>,
  fallbackLabel: string
) => {
  const resolved = items.map((item, index) => {
    const name = item.name?.trim();
    if (name) return { label: name, kind: 'name' as const };
    const prefix = item.prefix?.trim();
    if (prefix) return { label: prefix, kind: 'prefix' as const };
    const host = extractHost(item.baseUrl);
    if (host) return { label: host, kind: 'host' as const };
    return { label: `${fallbackLabel} #${index + 1}`, kind: 'fallback' as const };
  });

  const totalCounts = new Map<string, number>();
  const nonPrefixCounts = new Map<string, number>();
  resolved.forEach(({ label, kind }) => {
    totalCounts.set(label, (totalCounts.get(label) || 0) + 1);
    if (kind !== 'prefix') {
      nonPrefixCounts.set(label, (nonPrefixCounts.get(label) || 0) + 1);
    }
  });

  const ordinals = new Map<string, number>();
  return resolved.map(({ label }) => {
    const shouldDisambiguate =
      (totalCounts.get(label) || 0) > 1 && (nonPrefixCounts.get(label) || 0) > 0;
    if (!shouldDisambiguate) return label;
    const ordinal = (ordinals.get(label) || 0) + 1;
    ordinals.set(label, ordinal);
    return `${label} #${ordinal}`;
  });
};

const disambiguateDuplicateNames = (names: string[]) => {
  const counts = new Map<string, number>();
  names.forEach((name) => {
    counts.set(name, (counts.get(name) || 0) + 1);
  });

  const ordinals = new Map<string, number>();
  return names.map((name) => {
    if ((counts.get(name) || 0) <= 1) return name;
    const ordinal = (ordinals.get(name) || 0) + 1;
    ordinals.set(name, ordinal);
    return `${name} #${ordinal}`;
  });
};

const buildOpenAIKeyDisplayNameMap = (providers: OpenAIProviderConfig[]) => {
  const entries: Array<{ key: string; name: string }> = [];

  providers.forEach((provider, providerIndex) => {
    (provider.apiKeyEntries || []).forEach((_entry, entryIndex) => {
      entries.push({
        key: `${providerIndex}:${entryIndex}`,
        name:
          provider.name?.trim() ||
          provider.prefix?.trim() ||
          extractHost(provider.baseUrl) ||
          `OpenAI #${providerIndex + 1}`,
      });
    });
  });

  const displayNames = disambiguateDuplicateNames(entries.map((entry) => entry.name));
  return new Map(entries.map((entry, index) => [entry.key, displayNames[index] || entry.name]));
};

export function buildSourceInfoMap(input: SourceInfoMapInput): SourceInfoMap {
  const byAuthIndex = new Map<string, SourceInfoEntry | null>();
  const bySource = new Map<string, SourceInfoEntry | null>();
  const bySourceAndProvider = new Map<string, SourceInfoEntry | null>();
  const byIdentityKey = new Map<string, SourceInfoEntry>();
  const groupNameByEntry = new WeakMap<SourceInfoEntry, string>();

  const registerProvider = (
    entry: SourceInfoEntry,
    authIndices: Array<unknown>,
    candidates: Iterable<string>
  ) => {
    authIndices.forEach((authIndex) => {
      registerIdentity(byAuthIndex, normalizeAuthIndex(authIndex), entry, groupNameByEntry);
    });

    Array.from(candidates).forEach((candidate) => {
      registerIdentity(bySource, candidate, entry, groupNameByEntry);
      registerIdentity(
        bySourceAndProvider,
        buildProviderSourceKey(entry.type, candidate),
        entry,
        groupNameByEntry
      );
    });
  };

  const providers: Array<{
    items: Array<{
      name?: string;
      apiKey?: string;
      prefix?: string;
      baseUrl?: string;
      authIndex?: string;
      excludedModels?: string[];
    }>;
    type: string;
    label: string;
  }> = [
    { items: input.geminiApiKeys || [], type: 'gemini', label: 'Gemini' },
    { items: input.interactionsApiKeys || [], type: 'interactions', label: 'Interactions' },
    { items: input.claudeApiKeys || [], type: 'claude', label: 'Claude' },
    { items: input.codexApiKeys || [], type: 'codex', label: 'Codex' },
    { items: input.xaiApiKeys || [], type: 'xai', label: 'xAI' },
    { items: input.metaApiKeys || [], type: 'meta', label: 'Muse (Meta)' },
    { items: input.vertexApiKeys || [], type: 'vertex', label: 'Vertex' },
  ];

  providers.forEach(({ items, type, label }) => {
    const displayNames = buildProviderDisplayNames(items, label);
    items.forEach((item, index) => {
      const entry: SourceInfoEntry = {
        displayName: displayNames[index] || `${label} #${index + 1}`,
        type,
        identityKey: buildProviderIdentityKey(type, index),
        providerEnabledState: buildProviderEnabledState(
          !hasDisableAllModelsRule(item.excludedModels)
        ),
      };
      const trimmedName = item.name?.trim();
      if (trimmedName) {
        groupNameByEntry.set(entry, trimmedName);
      }
      registerProvider(
        entry,
        [item.authIndex],
        buildCandidateUsageSourceIds({ apiKey: item.apiKey, prefix: item.prefix })
      );
    });
  });

  const openaiProviders = input.openaiCompatibility || [];
  const openaiProviderDisplayNames = buildProviderDisplayNames(openaiProviders, 'OpenAI');
  const openaiKeyDisplayNames = buildOpenAIKeyDisplayNameMap(openaiProviders);

  openaiProviders.forEach((provider, providerIndex) => {
    const entryAuthIndexKeys = new Set(
      (provider.apiKeyEntries || [])
        .map((entry) => normalizeAuthIndex(entry.authIndex))
        .filter(Boolean)
    );
    const providerAuthIndex = normalizeAuthIndex(provider.authIndex);
    const providerEntry = {
      displayName: openaiProviderDisplayNames[providerIndex] || `OpenAI #${providerIndex + 1}`,
      type: 'openai',
      identityKey: buildProviderIdentityKey('openai', providerIndex),
      providerEnabledState: buildProviderEnabledState(provider.disabled !== true),
    };

    registerProvider(
      providerEntry,
      providerAuthIndex && !entryAuthIndexKeys.has(providerAuthIndex) ? [providerAuthIndex] : [],
      buildCandidateUsageSourceIds({ prefix: provider.prefix })
    );

    (provider.apiKeyEntries || []).forEach((entry, entryIndex) => {
      registerProvider(
        {
          displayName:
            openaiKeyDisplayNames.get(`${providerIndex}:${entryIndex}`) ||
            providerEntry.displayName,
          type: 'openai',
          identityKey: buildProviderIdentityKey('openai', `${providerIndex}:${entryIndex}`),
          providerEnabledState: providerEntry.providerEnabledState,
        },
        [entry.authIndex],
        buildCandidateUsageSourceIds({ apiKey: entry.apiKey })
      );
    });
  });

  [byAuthIndex, bySource, bySourceAndProvider].forEach((map) => {
    map.forEach((entry) => {
      if (entry) {
        byIdentityKey.set(entry.identityKey, entry);
      }
    });
  });

  return { byAuthIndex, bySource, bySourceAndProvider, byIdentityKey };
}

export const buildSourceProviderStateMap = (sourceInfoMap: SourceInfoMap) => {
  const map = new Map<string, SourceProviderEnabledState>();
  sourceInfoMap.byIdentityKey.forEach((entry, identityKey) => {
    if (entry.providerEnabledState) {
      map.set(identityKey, entry.providerEnabledState);
    }
  });
  return map;
};

const PROVIDER_RAW_TYPE_ALIASES: Record<string, string> = {
  'gemini-apikey': 'gemini',
  'gemini-api-key': 'gemini',
  'gemini-interactions': 'interactions',
  'interactions-apikey': 'interactions',
  'interactions-api-key': 'interactions',
  'claude-apikey': 'claude',
  'claude-api-key': 'claude',
  'codex-apikey': 'codex',
  'codex-api-key': 'codex',
  'xai-apikey': 'xai',
  'xai-api-key': 'xai',
  'meta-apikey': 'meta',
  'meta-api-key': 'meta',
  'vertex-apikey': 'vertex',
  'vertex-api-key': 'vertex',
  'vertex-compat': 'vertex',
  'openai-compatibility': 'openai',
};

export function resolveSourceDisplay(
  sourceRaw: string,
  authIndex: unknown,
  sourceInfoMap: SourceInfoMap,
  authFileMap: Map<string, CredentialInfo>,
  providerRaw: unknown = ''
): SourceInfo {
  const source = normalizeUsageSourceId(sourceRaw);
  const authIndexKey = normalizeAuthIndex(authIndex);

  if (authIndexKey) {
    const matchedByAuthIndex = sourceInfoMap.byAuthIndex.get(authIndexKey);
    if (matchedByAuthIndex) return matchedByAuthIndex;

    const authInfo = authFileMap.get(authIndexKey);
    if (authInfo) {
      return {
        displayName: authInfo.name || authIndexKey,
        type: authInfo.type,
        identityKey: `auth:${authIndexKey}`,
      };
    }
  }

  const matchedBySource = source ? sourceInfoMap.bySource.get(source) : null;
  const sharedAcrossProviders =
    matchedBySource?.identityKey.startsWith('shared:') && !matchedBySource.type;
  if (matchedBySource && !sharedAcrossProviders) return matchedBySource;

  // A source shared across providers may have one display name but distinct
  // provider identities. Keep same-provider ambiguity unchanged.
  if (source && (matchedBySource === null || sharedAcrossProviders)) {
    const provider = typeof providerRaw === 'string' ? providerRaw.trim().toLowerCase() : '';
    const normalizedProvider = provider ? PROVIDER_RAW_TYPE_ALIASES[provider] || provider : '';
    const matchedByProvider = provider
      ? (sourceInfoMap.bySourceAndProvider?.get(buildProviderSourceKey(provider, source)) ??
        (normalizedProvider !== provider
          ? sourceInfoMap.bySourceAndProvider?.get(
              buildProviderSourceKey(normalizedProvider, source)
            )
          : null))
      : null;
    if (matchedByProvider) return matchedByProvider;
  }
  if (matchedBySource) return matchedBySource;

  if (source) {
    return {
      displayName: formatRawSourceDisplayName(source),
      type: '',
      identityKey: `source:${source}`,
    };
  }

  if (authIndexKey) {
    return {
      displayName: authIndexKey,
      type: '',
      identityKey: `auth:${authIndexKey}`,
    };
  }

  return {
    displayName: '-',
    type: '',
    identityKey: 'source:-',
  };
}

export function resolveSourceIdentityKey(
  sourceRaw: string,
  authIndex: unknown,
  sourceInfoMap: SourceInfoMap,
  authFileMap: Map<string, CredentialInfo>
): string {
  return resolveSourceDisplay(sourceRaw, authIndex, sourceInfoMap, authFileMap).identityKey || '';
}
