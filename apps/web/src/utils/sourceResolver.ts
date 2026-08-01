import type { GeminiKeyConfig, OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import type { CredentialInfo, SourceInfo, SourceProviderEnabledState } from '@/types/sourceInfo';
import {
  buildCandidateUsageSourceIds,
  normalizeAuthIndex,
  normalizeUsageSourceId,
} from '@/utils/usage';

export interface SourceInfoMapInput {
  geminiApiKeys?: GeminiKeyConfig[];
  claudeApiKeys?: ProviderKeyConfig[];
  codexApiKeys?: ProviderKeyConfig[];
  xaiApiKeys?: ProviderKeyConfig[];
  vertexApiKeys?: ProviderKeyConfig[];
  openaiCompatibility?: OpenAIProviderConfig[];
}

type SourceInfoEntry = Required<Pick<SourceInfo, 'displayName' | 'type' | 'identityKey'>> &
  Pick<SourceInfo, 'providerEnabledState'>;

export interface SourceInfoMap {
  byAuthIndex: Map<string, SourceInfoEntry | null>;
  bySource: Map<string, SourceInfoEntry | null>;
  byIdentityKey: Map<string, SourceInfoEntry>;
}

const buildProviderIdentityKey = (type: string, index: number | string) => `${type}:${index}`;

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
  entry: SourceInfoEntry
) => {
  if (!key) return;

  const existing = map.get(key);
  if (existing === undefined) {
    map.set(key, entry);
    return;
  }

  if (existing === null || existing.identityKey === entry.identityKey) return;
  if (existing.displayName === entry.displayName) {
    map.set(key, {
      displayName: existing.displayName,
      type: existing.type === entry.type ? existing.type : '',
      identityKey: `shared:${key}`,
      providerEnabledState: mergeProviderEnabledState(
        existing.providerEnabledState,
        entry.providerEnabledState
      ),
    });
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
  const hostCounts = new Map<string, number>();

  items.forEach((item) => {
    if (item.prefix?.trim()) return;
    if (item.name?.trim()) return;
    const host = extractHost(item.baseUrl);
    if (!host) return;
    hostCounts.set(host, (hostCounts.get(host) || 0) + 1);
  });

  const hostOrdinals = new Map<string, number>();
  return items.map((item, index) => {
    const prefix = item.prefix?.trim();
    if (prefix) return prefix;

    const name = item.name?.trim();
    if (name) return name;

    const host = extractHost(item.baseUrl);
    if (!host) return `${fallbackLabel} #${index + 1}`;
    if ((hostCounts.get(host) || 0) <= 1) return host;

    const ordinal = (hostOrdinals.get(host) || 0) + 1;
    hostOrdinals.set(host, ordinal);
    return `${host} #${ordinal}`;
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
          provider.prefix?.trim() ||
          provider.name?.trim() ||
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
  const byIdentityKey = new Map<string, SourceInfoEntry>();

  const registerProvider = (
    entry: SourceInfoEntry,
    authIndices: Array<unknown>,
    candidates: Iterable<string>
  ) => {
    authIndices.forEach((authIndex) => {
      registerIdentity(byAuthIndex, normalizeAuthIndex(authIndex), entry);
    });

    Array.from(candidates).forEach((candidate) => {
      registerIdentity(bySource, candidate, entry);
    });
  };

  const providers: Array<{
    items: Array<{
      apiKey?: string;
      prefix?: string;
      authIndex?: string;
      excludedModels?: string[];
    }>;
    type: string;
    label: string;
  }> = [
    { items: input.geminiApiKeys || [], type: 'gemini', label: 'Gemini' },
    { items: input.claudeApiKeys || [], type: 'claude', label: 'Claude' },
    { items: input.codexApiKeys || [], type: 'codex', label: 'Codex' },
    { items: input.xaiApiKeys || [], type: 'xai', label: 'xAI' },
    { items: input.vertexApiKeys || [], type: 'vertex', label: 'Vertex' },
  ];

  providers.forEach(({ items, type, label }) => {
    const displayNames = buildProviderDisplayNames(items, label);
    items.forEach((item, index) => {
      registerProvider(
        {
          displayName: displayNames[index] || `${label} #${index + 1}`,
          type,
          identityKey: buildProviderIdentityKey(type, index),
          providerEnabledState: buildProviderEnabledState(
            !hasDisableAllModelsRule(item.excludedModels)
          ),
        },
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

  [byAuthIndex, bySource].forEach((map) => {
    map.forEach((entry) => {
      if (entry) {
        byIdentityKey.set(entry.identityKey, entry);
      }
    });
  });

  return { byAuthIndex, bySource, byIdentityKey };
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

export function resolveSourceDisplay(
  sourceRaw: string,
  authIndex: unknown,
  sourceInfoMap: SourceInfoMap,
  authFileMap: Map<string, CredentialInfo>
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
