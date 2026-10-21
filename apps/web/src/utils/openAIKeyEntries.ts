type OpenAIKeyEntryLike = {
  apiKey?: string;
  authIndex?: string;
  weight?: number | string;
  proxyUrl?: string;
  headers?: Record<string, string>;
};

// CPA uses [] for OpenAI-compatible upstreams that require no API key.
// Treat only CPA-supported per-entry fields as persisted configuration.
// Entry-level headers are not part of CPA's OpenAI compatibility key schema.
export const hasOpenAIKeyEntryConfiguration = (entry: OpenAIKeyEntryLike): boolean =>
  Boolean(
    entry.apiKey?.trim() ||
      entry.authIndex?.trim() ||
      entry.proxyUrl?.trim() ||
      (entry.weight !== undefined && String(entry.weight).trim() !== '')
  );

export const getOpenAIModelDiscoveryEntry = <T extends OpenAIKeyEntryLike>(
  entries: T[]
): T | undefined =>
  entries.find((entry) => entry.apiKey?.trim()) ??
  entries.find((entry) => hasOpenAIKeyEntryConfiguration(entry));

export const updateOpenAIApiKey = <T extends OpenAIKeyEntryLike>(
  entry: T,
  apiKey: string
): T => ({
  ...entry,
  apiKey,
  // auth-index identifies the server-side credential generated from the old key.
  // Any user edit to the key invalidates that identity until CPA returns a fresh one.
  authIndex: '',
});

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
