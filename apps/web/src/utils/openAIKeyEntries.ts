const OPENAI_KEY_ENTRY_METADATA_MARKER = Symbol('openai-key-entry-metadata');

export type OpenAIKeyEntrySourceState = 'persisted-keyless' | 'persisted-keyed' | 'keyed-cleared';

type OpenAIKeyEntryMetadata = {
  sourceState?: OpenAIKeyEntrySourceState;
  persistedIndex?: number;
};

type OpenAIKeyEntryMetadataCarrier = {
  [OPENAI_KEY_ENTRY_METADATA_MARKER]?: OpenAIKeyEntryMetadata;
};

type OpenAIKeyEntryLike = {
  apiKey?: string;
  authIndex?: string;
  weight?: number | string;
  proxyUrl?: string;
  headers?: Record<string, string>;
};

const getOpenAIKeyEntryMetadata = (entry: unknown): OpenAIKeyEntryMetadata | undefined =>
  typeof entry === 'object' && entry !== null
    ? (entry as OpenAIKeyEntryMetadataCarrier)[OPENAI_KEY_ENTRY_METADATA_MARKER]
    : undefined;

const setOpenAIKeyEntryMetadata = <T extends object>(
  entry: T,
  metadata: OpenAIKeyEntryMetadata
): T => {
  Object.defineProperty(entry, OPENAI_KEY_ENTRY_METADATA_MARKER, {
    configurable: true,
    enumerable: false,
    value: metadata,
  });
  return entry;
};

export const markPersistedOpenAIKeyEntry = <T extends OpenAIKeyEntryLike>(
  entry: T,
  persistedIndex?: number,
  hasExplicitApiKey = true
): T =>
  setOpenAIKeyEntryMetadata(entry, {
    sourceState: entry.apiKey?.trim()
      ? 'persisted-keyed'
      : hasExplicitApiKey
        ? 'persisted-keyless'
        : undefined,
    persistedIndex,
  });

export const getPersistedOpenAIKeyEntryIndex = (entry: unknown): number | undefined =>
  getOpenAIKeyEntryMetadata(entry)?.persistedIndex;

export const isPersistedOpenAIKeyEntry = (entry: unknown): boolean => {
  if (typeof entry !== 'object' || entry === null) return false;
  const apiKey = (entry as OpenAIKeyEntryLike).apiKey?.trim();
  return !apiKey && getOpenAIKeyEntryMetadata(entry)?.sourceState === 'persisted-keyless';
};

export const isClearedOpenAIKeyEntry = (entry: unknown): boolean => {
  if (typeof entry !== 'object' || entry === null) return false;
  const apiKey = (entry as OpenAIKeyEntryLike).apiKey?.trim();
  if (apiKey) return false;
  const state = getOpenAIKeyEntryMetadata(entry)?.sourceState;
  return state === 'keyed-cleared' || state === 'persisted-keyed';
};

export const preservePersistedOpenAIKeyEntry = <T extends object>(
  source: unknown,
  target: T
): T => {
  const metadata = getOpenAIKeyEntryMetadata(source);
  return metadata ? setOpenAIKeyEntryMetadata(target, { ...metadata }) : target;
};

// CPA uses [] for OpenAI-compatible upstreams that require no API key, while
// mixed or startup-disabled providers may persist explicit {"api-key": ""}
// entries without a runtime auth-index. A previously keyed entry whose key the
// user clears is dropped even if it still carries proxy-url or weight.
// Entry-level headers are not part of CPA's OpenAI compatibility key schema.
export const hasOpenAIKeyEntryConfiguration = (entry: OpenAIKeyEntryLike): boolean => {
  if (entry.apiKey?.trim()) return true;
  if (isClearedOpenAIKeyEntry(entry)) return false;
  return Boolean(
    entry.authIndex?.trim() ||
    entry.proxyUrl?.trim() ||
    (entry.weight !== undefined && String(entry.weight).trim() !== '') ||
    isPersistedOpenAIKeyEntry(entry)
  );
};

export const getOpenAIModelDiscoveryEntry = <T extends OpenAIKeyEntryLike>(
  entries: T[]
): T | undefined =>
  entries.find((entry) => entry.apiKey?.trim()) ??
  entries.find((entry) => hasOpenAIKeyEntryConfiguration(entry));

export const updateOpenAIApiKey = <T extends OpenAIKeyEntryLike>(entry: T, apiKey: string): T => {
  const previousMetadata = getOpenAIKeyEntryMetadata(entry);
  const previousState = previousMetadata?.sourceState;
  const wasKeyed =
    Boolean(entry.apiKey?.trim()) ||
    previousState === 'persisted-keyed' ||
    previousState === 'keyed-cleared';
  const nextTrimmed = apiKey.trim();
  const nextState: OpenAIKeyEntrySourceState | undefined = nextTrimmed
    ? 'persisted-keyed'
    : wasKeyed
      ? 'keyed-cleared'
      : previousState;
  const nextEntry: T = {
    ...entry,
    apiKey,
    // auth-index identifies the server-side credential generated from the old key.
    // Any user edit to the key invalidates that identity until CPA returns a fresh one.
    authIndex: '',
  };
  if (nextState === undefined && previousMetadata?.persistedIndex === undefined) {
    return nextEntry;
  }
  return setOpenAIKeyEntryMetadata(nextEntry, {
    sourceState: nextState,
    persistedIndex: previousMetadata?.persistedIndex,
  });
};

export const getOpenAIKeyCount = (entries: OpenAIKeyEntryLike[]): number =>
  entries.filter((entry) => entry.apiKey?.trim()).length;

export type OpenAIUsageStatsCoverage = 'full' | 'partial' | 'unavailable';

export const getOpenAIUsageStatsCoverage = (
  entries: OpenAIKeyEntryLike[]
): OpenAIUsageStatsCoverage => {
  const keyCount = getOpenAIKeyCount(entries);
  if (keyCount === 0) return 'unavailable';
  return keyCount === entries.length ? 'full' : 'partial';
};

// Only configured entries are independently testable. A single blank row
// represents the anonymous upstream if there are no configured entries.
export const getOpenAITestableKeyIndexes = (
  entries: Array<Parameters<typeof hasOpenAIKeyEntryConfiguration>[0]>
): number[] => {
  const configured = entries.flatMap((entry, index) =>
    hasOpenAIKeyEntryConfiguration(entry) ? [index] : []
  );
  return configured.length ? configured : entries.length ? [0] : [];
};
