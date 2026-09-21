/**
 * Coding-plan credential identification.
 *
 * Zhipu GLM Coding Plan and OpenCode Go credentials surface as ordinary
 * api-key credentials (provider "claude" / compat name) and carry no base-url
 * in the auth-files payload. They are identified by joining the credential's
 * auth_index against the base-url declared for it in the CPA config.
 */

import type { AuthFileItem } from '@/types';
import type { Config } from '@/types/config';
import { normalizeAuthIndex } from '@/utils/authIndex';
import { sha256Hex } from '@/utils/apiKeyHash';

/** authIndex -> base-url origin for credentials backed by a Zhipu coding plan. */
export type ZhipuAuthIndexBaseMap = Map<string, string>;

/** Origins the Zhipu monitor API is served from (per official glm-plan-usage). */
const ZHIPU_BASE_ORIGINS = new Set([
  'https://open.bigmodel.cn',
  'https://dev.bigmodel.cn',
  'https://api.z.ai',
]);

const originOf = (baseUrl: string): string => {
  try {
    return new URL(baseUrl).origin;
  } catch {
    return '';
  }
};

const isZhipuBaseUrl = (baseUrl: unknown): boolean => {
  if (typeof baseUrl !== 'string' || !baseUrl.trim()) return false;
  const origin = originOf(baseUrl.trim());
  return origin !== '' && ZHIPU_BASE_ORIGINS.has(origin);
};

/**
 * Builds the authIndex -> origin map for Zhipu coding-plan keys found in the
 * CPA `claude-api-key` config section.
 */
export const buildZhipuAuthIndexBaseMap = (config: Config | null): ZhipuAuthIndexBaseMap => {
  const map: ZhipuAuthIndexBaseMap = new Map();
  (config?.claudeApiKeys ?? []).forEach((entry) => {
    if (!isZhipuBaseUrl(entry.baseUrl)) return;
    const authIndex = normalizeAuthIndex(entry.authIndex);
    if (!authIndex) return;
    const origin = originOf(entry.baseUrl ?? '');
    if (origin && !map.has(authIndex)) map.set(authIndex, origin);
  });
  return map;
};

/** OpenCode credentials are OpenAI-compat entries; provider equals the lowercased compat name. */
export const isOpencodeProvider = (provider: string): boolean =>
  provider.trim().toLowerCase().startsWith('opencode');

export const readRawProvider = (file: AuthFileItem): string =>
  String(file.provider ?? file.type ?? '')
    .trim()
    .toLowerCase()
    .replace(/_/g, '-');

/**
 * Normalized quota provider for coding-plan credentials.
 * Returns 'zhipu' / 'opencode' when the credential belongs to a known coding
 * plan, otherwise null (caller falls back to the generic provider).
 */
export const resolveCodingPlanProvider = (
  file: AuthFileItem,
  zhipuBases: ZhipuAuthIndexBaseMap
): 'zhipu' | 'opencode' | null => {
  const provider = readRawProvider(file);
  if (provider === 'zhipu') return 'zhipu';
  if (isOpencodeProvider(provider)) return 'opencode';
  if (provider === 'claude') {
    const authIndex = normalizeAuthIndex(file.authIndex ?? file['auth_index']);
    if (authIndex && zhipuBases.has(authIndex)) return 'zhipu';
  }
  return null;
};

/**
 * CPA's auth-files listing hides config-sourced api-key credentials (no file
 * path, not runtime-only), and since v7.3.8 the /config payload omits auth
 * indices (the per-section endpoints that annotated them were removed). The
 * index is a plain hash of the credential identity, though — see
 * coreauth.Auth.indexSeed — so it is replicated here from the /config data
 * to synthesize AuthFileItem rows the Accounts/Monitoring views can list and
 * query.
 */
const apiKeyTail = (apiKey?: string): string =>
  typeof apiKey === 'string' && apiKey.length > 8 ? apiKey.slice(-4) : '';

/**
 * Replicates coreauth.stableAuthIndex: hex(sha256(seed)[:8]).
 * Uses the bundled pure-JS SHA-256 because crypto.subtle is unavailable when
 * the panel is served over plain HTTP on a LAN address.
 */
export const computeStableAuthIndex = (seed: string): string => {
  const trimmed = seed.trim();
  if (!trimmed) return '';
  return sha256Hex(trimmed).slice(0, 16);
};

/** AuthFileItem attribute carrying the resolved Zhipu monitor origin. */
export const ZHIPU_BASE_URL_ATTRIBUTE = 'zhipu_base_url';

const hasDisableAllModelsRule = (excludedModels?: string[]): boolean =>
  (excludedModels ?? []).some((model) => String(model).trim() === '*');

const buildZhipuAuthFiles = (config: Config | null): AuthFileItem[] => {
  const files: AuthFileItem[] = [];
  let zhipuIndex = 0;
  for (const entry of config?.claudeApiKeys ?? []) {
    if (!isZhipuBaseUrl(entry.baseUrl)) continue;
    const apiKey = String(entry.apiKey ?? '').trim();
    const baseUrl = String(entry.baseUrl ?? '').trim();
    const origin = originOf(baseUrl);
    if (!apiKey || !origin || !baseUrl) continue;
    // Seed must match coreauth.Auth.indexSeed: raw base-url, not the origin.
    const authIndex = computeStableAuthIndex(`claude-api-key:${baseUrl}+${apiKey}`);
    if (!authIndex) continue;
    zhipuIndex += 1;
    const tail = apiKeyTail(apiKey);
    files.push({
      name: `zhipu-coding-plan-${zhipuIndex}`,
      type: 'zhipu',
      provider: 'zhipu',
      label: tail ? `GLM Coding Plan ···${tail}` : `GLM Coding Plan #${zhipuIndex}`,
      authIndex,
      runtimeOnly: 'true',
      status: 'active',
      ...(hasDisableAllModelsRule(entry.excludedModels) ? { disabled: true } : {}),
      [ZHIPU_BASE_URL_ATTRIBUTE]: origin,
    });
  }
  return files;
};

const buildOpencodeAuthFiles = (config: Config | null): AuthFileItem[] => {
  const files: AuthFileItem[] = [];
  for (const compat of config?.openaiCompatibility ?? []) {
    const name = String(compat.name ?? '').trim();
    if (!isOpencodeProvider(name)) continue;
    const base = String(compat.baseUrl ?? '').trim();
    for (let entryIndex = 0; entryIndex < (compat.apiKeyEntries ?? []).length; entryIndex += 1) {
      const entry = compat.apiKeyEntries?.[entryIndex];
      const apiKey = String(entry?.apiKey ?? '').trim();
      if (!apiKey) continue;
      const authIndex = computeStableAuthIndex(`openai-compatibility:${base}+${apiKey}`);
      if (!authIndex) continue;
      const tail = apiKeyTail(apiKey);
      files.push({
        name: `${name}-${entryIndex + 1}`,
        type: 'opencode',
        provider: 'opencode',
        label: tail ? `${name} ···${tail}` : `${name} #${entryIndex + 1}`,
        authIndex,
        runtimeOnly: 'true',
        status: 'active',
        ...(compat.disabled === true ? { disabled: true } : {}),
      });
    }
  }
  return files;
};

export const fetchCodingPlanAuthFiles = async (
  config: Config | null
): Promise<AuthFileItem[]> => {
  const [zhipu, opencode] = await Promise.all([
    buildZhipuAuthFiles(config),
    buildOpencodeAuthFiles(config),
  ]);
  return [...zhipu, ...opencode];
};

/**
 * Merges synthesized coding-plan credentials into an auth-files list,
 * skipping auth indices already provided by the backend.
 */
export const mergeCodingPlanAuthFiles = (
  files: AuthFileItem[],
  codingPlanFiles: AuthFileItem[]
): AuthFileItem[] => {
  const knownIndices = new Set(
    files
      .map((file) => normalizeAuthIndex(file.authIndex ?? file['auth_index']))
      .filter((value): value is string => Boolean(value))
  );
  const synthesized = codingPlanFiles.filter((file) => {
    const authIndex = normalizeAuthIndex(file.authIndex ?? '');
    return authIndex && !knownIndices.has(authIndex);
  });
  if (synthesized.length === 0) return files;
  return [...files, ...synthesized];
};
