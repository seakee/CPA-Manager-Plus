import { useCallback, useMemo, useReducer } from 'react';
import { isAlias, isMap, isScalar, parse as parseYaml, parseDocument } from 'yaml';
import type {
  DisableImageGenerationMode,
  PluginStoreAuthApplyTo,
  PluginStoreAuthRule,
  PluginStoreAuthType,
  VisualConfigValues,
  VisualConfigValidationErrors,
} from '@/types/visualConfig';
import { DEFAULT_VISUAL_VALUES } from '@/types/visualConfig';
import { normalizeRoutingStrategy } from '@/utils/routingStrategy';
import {
  arePayloadFilterRulesEqual,
  arePayloadRulesEqual,
  hasPayloadParamValidationErrors,
  parsePayloadFilterRules,
  parsePayloadRules,
  parseRawPayloadRules,
  serializePayloadFilterRulesForYaml,
  serializePayloadRulesForYaml,
  serializeRawPayloadRulesForYaml,
} from './visualConfigPayloadRules';

export {
  getPayloadParamValidationError,
  VISUAL_CONFIG_PAYLOAD_VALUE_TYPE_OPTIONS,
  VISUAL_CONFIG_PROTOCOL_OPTIONS,
} from './visualConfigPayloadRules';

function asRecord(value: unknown): Record<string, unknown> | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null;
  return value as Record<string, unknown>;
}

function extractApiKeyValue(raw: unknown): string | null {
  if (typeof raw === 'string') {
    const trimmed = raw.trim();
    return trimmed ? trimmed : null;
  }

  const record = asRecord(raw);
  if (!record) return null;

  const candidates = [record['api-key'], record.apiKey, record.key, record.Key];
  for (const candidate of candidates) {
    if (typeof candidate === 'string') {
      const trimmed = candidate.trim();
      if (trimmed) return trimmed;
    }
  }

  return null;
}

function parseApiKeysText(raw: unknown): string {
  if (!Array.isArray(raw)) return '';

  const keys: string[] = [];
  for (const item of raw) {
    const key = extractApiKeyValue(item);
    if (key) keys.push(key);
  }
  return keys.join('\n');
}

function parseStringArrayText(raw: unknown): string {
  if (!Array.isArray(raw)) return '';
  return raw
    .map((item) => (typeof item === 'string' ? item.trim() : ''))
    .filter(Boolean)
    .join('\n');
}

function parseStringList(raw: unknown): string[] {
  if (!Array.isArray(raw)) return [];
  return raw.map((item) => String(item ?? '').trim()).filter(Boolean);
}

const PLUGIN_STORE_AUTH_TYPES: PluginStoreAuthType[] = [
  'none',
  'bearer',
  'basic',
  'header',
  'github-token',
];
const PLUGIN_STORE_AUTH_APPLY_TO: PluginStoreAuthApplyTo[] = ['registry', 'metadata', 'artifact'];

function parsePluginStoreAuthType(raw: unknown): PluginStoreAuthType {
  const value = String(raw ?? '')
    .trim()
    .toLowerCase();
  return PLUGIN_STORE_AUTH_TYPES.includes(value as PluginStoreAuthType)
    ? (value as PluginStoreAuthType)
    : 'none';
}

function parsePluginStoreAuthApplyTo(raw: unknown): PluginStoreAuthApplyTo[] {
  return parseStringList(raw)
    .map((item) => item.toLowerCase())
    .filter((item): item is PluginStoreAuthApplyTo =>
      PLUGIN_STORE_AUTH_APPLY_TO.includes(item as PluginStoreAuthApplyTo)
    );
}

function parsePluginStoreAuthRules(raw: unknown): PluginStoreAuthRule[] {
  if (!Array.isArray(raw)) return [];
  return raw
    .map((item, index): PluginStoreAuthRule | null => {
      const record = asRecord(item);
      if (!record) return null;
      const rule: PluginStoreAuthRule = {
        id: `plugin-store-auth-${index}`,
        match: typeof record.match === 'string' ? record.match : '',
        applyTo: parsePluginStoreAuthApplyTo(record['apply-to'] ?? record.apply_to),
        type: parsePluginStoreAuthType(record.type),
        tokenEnv: typeof record['token-env'] === 'string' ? record['token-env'] : '',
        usernameEnv: typeof record['username-env'] === 'string' ? record['username-env'] : '',
        passwordEnv: typeof record['password-env'] === 'string' ? record['password-env'] : '',
        headerName: typeof record['header-name'] === 'string' ? record['header-name'] : '',
        headerValueEnv:
          typeof record['header-value-env'] === 'string' ? record['header-value-env'] : '',
        allowInsecure: Boolean(record['allow-insecure'] ?? record.allow_insecure),
      };
      return rule.match.trim() ||
        rule.type !== 'none' ||
        rule.applyTo.length > 0 ||
        rule.tokenEnv.trim() ||
        rule.usernameEnv.trim() ||
        rule.passwordEnv.trim() ||
        rule.headerName.trim() ||
        rule.headerValueEnv.trim() ||
        rule.allowInsecure
        ? rule
        : null;
    })
    .filter((rule): rule is PluginStoreAuthRule => Boolean(rule));
}

function resolveApiKeysText(parsed: Record<string, unknown>): string {
  const access = asRecord(parsed.access);
  if (access && Object.prototype.hasOwnProperty.call(access, 'api-keys')) {
    return parseApiKeysText(access['api-keys']);
  }

  if (Object.prototype.hasOwnProperty.call(parsed, 'api-keys')) {
    return parseApiKeysText(parsed['api-keys']);
  }

  const auth = asRecord(parsed.auth);
  const providers = asRecord(auth?.providers);
  const configApiKeyProvider = asRecord(providers?.['config-api-key']);
  if (!configApiKeyProvider) return '';

  if (Object.prototype.hasOwnProperty.call(configApiKeyProvider, 'api-key-entries')) {
    return parseApiKeysText(configApiKeyProvider['api-key-entries']);
  }

  return parseApiKeysText(configApiKeyProvider['api-keys']);
}

type CodexIdentityConfuseCompatibility = 'supported' | 'unsupported' | 'unverified';

type VisualConfigRuntime = {
  serverVersion?: string | null;
  serverCommit?: string | null;
};

const CODEX_IDENTITY_CONFUSE_REMOVAL_COMMIT = '48686ccc';
const CODEX_IDENTITY_CONFUSE_LAST_SUPPORTED_VERSION = [8, 0, 3] as const;
const CODEX_IDENTITY_CONFUSE_REMOVED_VERSION = [8, 0, 4] as const;
const CPA_RELEASE_VERSION_PATTERN =
  /^v?(\d+)\.(\d+)\.(\d+)(?:-(?:alpha|beta|rc)(?:[.-]?\d+)?)?$/i;
const CPA_GIT_DESCRIBE_VERSION_PATTERN =
  /^v?(\d+)\.(\d+)\.(\d+)-(\d+)-g([0-9a-f]+)(?:-dirty)?$/i;

function compareCpaVersion(current: readonly number[], baseline: readonly number[]): number {
  for (let index = 0; index < baseline.length; index += 1) {
    const difference = current[index] - baseline[index];
    if (difference !== 0) return difference;
  }
  return 0;
}

function isExactCpaCommit(serverCommit: string | null | undefined, expected: string): boolean {
  const normalized = serverCommit?.trim().toLowerCase().replace(/^g/, '') ?? '';
  if (normalized.length < 7) return false;
  return normalized.startsWith(expected) || expected.startsWith(normalized);
}

export function getCodexIdentityConfuseCompatibility(
  serverVersion?: string | null,
  serverCommit?: string | null
): CodexIdentityConfuseCompatibility {
  if (isExactCpaCommit(serverCommit, CODEX_IDENTITY_CONFUSE_REMOVAL_COMMIT)) {
    return 'unsupported';
  }

  const normalizedVersion = serverVersion?.trim() ?? '';
  const describeMatch = normalizedVersion.match(CPA_GIT_DESCRIBE_VERSION_PATTERN);
  if (describeMatch) {
    const baseVersion = describeMatch.slice(1, 4).map((segment) => Number.parseInt(segment, 10));
    const baseComparison = compareCpaVersion(
      baseVersion,
      CODEX_IDENTITY_CONFUSE_LAST_SUPPORTED_VERSION
    );
    if (baseComparison < 0) return 'supported';
    if (baseComparison > 0) return 'unsupported';

    // Upstream removed identity-confuse in the first commit after v8.0.3.
    const distance = Number.parseInt(describeMatch[4], 10);
    return distance === 0 ? 'supported' : 'unsupported';
  }

  const releaseMatch = normalizedVersion.match(CPA_RELEASE_VERSION_PATTERN);
  if (!releaseMatch) return 'unverified';
  const current = releaseMatch.slice(1, 4).map((segment) => Number.parseInt(segment, 10));
  return compareCpaVersion(current, CODEX_IDENTITY_CONFUSE_REMOVED_VERSION) >= 0
    ? 'unsupported'
    : 'supported';
}

type YamlDocument = ReturnType<typeof parseDocument>;
type YamlPath = string[];

const YAML_EFFECTIVE_PARSE_OPTIONS = { merge: true } as const;

type VisualConfigPathMapping = {
  legacy: YamlPath;
  canonical: YamlPath;
};

const VISUAL_CONFIG_V8_PATH_MAPPINGS: VisualConfigPathMapping[] = [
  { legacy: ['host'], canonical: ['server', 'host'] },
  { legacy: ['port'], canonical: ['server', 'port'] },
  { legacy: ['tls'], canonical: ['server', 'tls'] },
  { legacy: ['commercial-mode'], canonical: ['server', 'commercial-mode'] },
  { legacy: ['remote-management'], canonical: ['management'] },
  { legacy: ['force-model-prefix'], canonical: ['routing', 'force-model-prefix'] },
  { legacy: ['request-retry'], canonical: ['routing', 'retry', 'request-retry'] },
  {
    legacy: ['max-retry-credentials'],
    canonical: ['routing', 'retry', 'max-retry-credentials'],
  },
  { legacy: ['max-retry-interval'], canonical: ['routing', 'retry', 'max-retry-interval'] },
  { legacy: ['disable-cooling'], canonical: ['routing', 'cooldown', 'disable-cooling'] },
  {
    legacy: ['save-cooldown-status'],
    canonical: ['routing', 'cooldown', 'save-cooldown-status'],
  },
  {
    legacy: ['transient-error-cooldown-seconds'],
    canonical: ['routing', 'cooldown', 'transient-error-cooldown-seconds'],
  },
  { legacy: ['proxy-url'], canonical: ['requests', 'proxy-url'] },
  { legacy: ['passthrough-headers'], canonical: ['requests', 'passthrough-headers'] },
  {
    legacy: ['nonstream-keepalive-interval'],
    canonical: ['requests', 'nonstream-keepalive-interval'],
  },
  { legacy: ['streaming'], canonical: ['requests', 'streaming'] },
  { legacy: ['payload'], canonical: ['requests', 'payload'] },
  { legacy: ['auth-dir'], canonical: ['oauth', 'auth-dir'] },
  {
    legacy: ['auth-auto-refresh-workers'],
    canonical: ['oauth', 'auth-auto-refresh-workers'],
  },
  { legacy: ['ws-auth'], canonical: ['oauth', 'providers', 'aistudio', 'ws-auth'] },
  {
    legacy: ['codex-header-defaults'],
    canonical: ['oauth', 'providers', 'codex', 'header-defaults'],
  },
  {
    legacy: ['codex', 'identity-confuse'],
    canonical: ['oauth', 'providers', 'codex', 'identity-confuse'],
  },
  {
    legacy: ['disable-claude-cloak-mode'],
    canonical: ['upstream', 'claude', 'disable-claude-cloak-mode'],
  },
  {
    legacy: ['claude-header-defaults'],
    canonical: ['upstream', 'claude', 'header-defaults'],
  },
  {
    legacy: ['antigravity-signature-cache-enabled'],
    canonical: ['oauth', 'providers', 'antigravity', 'signature-cache-enabled'],
  },
  {
    legacy: ['antigravity-signature-bypass-strict'],
    canonical: ['oauth', 'providers', 'antigravity', 'signature-bypass-strict'],
  },
  {
    legacy: ['quota-exceeded', 'antigravity-credits'],
    canonical: ['oauth', 'providers', 'antigravity', 'antigravity-credits'],
  },
  { legacy: ['devin'], canonical: ['oauth', 'providers', 'devin'] },
  {
    legacy: ['disable-image-generation'],
    canonical: ['multimedia', 'disable-image-generation'],
  },
  { legacy: ['gpt-image-2-base-model'], canonical: ['multimedia', 'gpt-image-2-base-model'] },
  {
    legacy: ['video-result-auth-cache-ttl'],
    canonical: ['multimedia', 'video-result-auth-cache-ttl'],
  },
  { legacy: ['debug'], canonical: ['observability', 'logs', 'debug'] },
  {
    legacy: ['logging-to-file'],
    canonical: ['observability', 'logs', 'logging-to-file'],
  },
  {
    legacy: ['logs-max-total-size-mb'],
    canonical: ['observability', 'logs', 'logs-max-total-size-mb'],
  },
  { legacy: ['request-log'], canonical: ['observability', 'logs', 'request-log'] },
  {
    legacy: ['error-logs-max-files'],
    canonical: ['observability', 'logs', 'error-logs-max-files'],
  },
  {
    legacy: ['usage-statistics-enabled'],
    canonical: ['observability', 'usage', 'usage-statistics-enabled'],
  },
  {
    legacy: ['redis-usage-queue-retention-seconds'],
    canonical: ['observability', 'usage', 'redis-usage-queue-retention-seconds'],
  },
  { legacy: ['pprof'], canonical: ['observability', 'pprof'] },
];

function pathsEqual(left: YamlPath, right: YamlPath): boolean {
  return left.length === right.length && left.every((part, index) => part === right[index]);
}

function pathStartsWith(path: YamlPath, prefix: YamlPath): boolean {
  return prefix.length <= path.length && prefix.every((part, index) => path[index] === part);
}

function findVisualConfigV8Mapping(path: YamlPath): VisualConfigPathMapping | null {
  let best: VisualConfigPathMapping | null = null;
  for (const mapping of VISUAL_CONFIG_V8_PATH_MAPPINGS) {
    if (!pathStartsWith(path, mapping.legacy)) continue;
    if (!best || mapping.legacy.length > best.legacy.length) best = mapping;
  }
  return best;
}

function mapVisualConfigV8Path(path: YamlPath): YamlPath {
  const mapping = findVisualConfigV8Mapping(path);
  if (!mapping) return path;
  return [...mapping.canonical, ...path.slice(mapping.legacy.length)];
}

function readObjectPath(
  root: Record<string, unknown>,
  path: YamlPath
): { found: boolean; value: unknown } {
  let current: unknown = root;
  for (const part of path) {
    const record = asRecord(current);
    if (!record || !Object.prototype.hasOwnProperty.call(record, part)) {
      return { found: false, value: undefined };
    }
    current = record[part];
  }
  return { found: true, value: current };
}

function yamlMapHasMergeKey(value: unknown): boolean {
  if (!isMap(value)) return false;
  return value.items.some((pair) => {
    const key = isScalar(pair.key) ? pair.key.value : pair.key;
    return key === '<<' || (typeof key === 'symbol' && key.description === '<<');
  });
}

function yamlPathKey(path: YamlPath): string {
  return path.join('\u0000');
}

function materializeEffectiveMapAtPath(
  doc: YamlDocument,
  sourceDoc: YamlDocument,
  path: YamlPath,
  effectiveRoot: Record<string, unknown>,
  materializedPaths: Set<string>
): void {
  const key = yamlPathKey(path);
  if (materializedPaths.has(key)) return;

  const effective = readObjectPath(effectiveRoot, path);
  const effectiveMap = asRecord(effective.value);
  if (!effective.found || !effectiveMap) return;

  const original = sourceDoc.getIn(path, true);
  const originallyInherited =
    original === undefined || isAlias(original) || yamlMapHasMergeKey(original);
  if (!originallyInherited) return;

  // Detach only the edited branch from aliases/merge inheritance. Track the
  // detachment for this save transaction so a later sibling write cannot
  // resurrect values that an earlier dirty field already deleted.
  doc.setIn(path, doc.createNode(effectiveMap));
  materializedPaths.add(key);
}

function pathUsesYamlInheritance(
  sourceDoc: YamlDocument,
  path: YamlPath,
  effectiveRoot: Record<string, unknown>
): boolean {
  if (!readObjectPath(effectiveRoot, path).found) return false;
  if (!sourceDoc.hasIn(path)) return true;

  for (let length = 0; length < path.length; length += 1) {
    const node =
      length === 0 ? sourceDoc.contents : sourceDoc.getIn(path.slice(0, length), true);
    if (isAlias(node) || yamlMapHasMergeKey(node)) return true;
  }
  return false;
}

function getHistoricalV8Aliases(path: YamlPath): YamlPath[] {
  if (pathsEqual(path, ['disable-claude-cloak-mode'])) {
    return [['oauth', 'providers', 'claude', 'disable-claude-cloak-mode']];
  }
  const claudeHeaders = ['claude-header-defaults'];
  if (pathStartsWith(path, claudeHeaders)) {
    return [
      [
        'oauth',
        'providers',
        'claude',
        'header-defaults',
        ...path.slice(claudeHeaders.length),
      ],
    ];
  }
  return [];
}

function readVisualConfigValue(
  parsed: Record<string, unknown>,
  legacyPath: YamlPath,
  legacyAliases: YamlPath[] = []
): unknown {
  const canonicalPath = mapVisualConfigV8Path(legacyPath);
  if (!pathsEqual(canonicalPath, legacyPath)) {
    const canonical = readObjectPath(parsed, canonicalPath);
    if (canonical.found) return canonical.value;

    for (const alias of getHistoricalV8Aliases(legacyPath)) {
      const historical = readObjectPath(parsed, alias);
      if (historical.found) return historical.value;
    }
  }

  const legacy = readObjectPath(parsed, legacyPath);
  if (legacy.found) return legacy.value;

  for (const alias of legacyAliases) {
    const fallback = readObjectPath(parsed, alias);
    if (fallback.found) return fallback.value;
  }
  return undefined;
}

function isV8VisualConfigLayout(parsed: Record<string, unknown>): boolean {
  for (const root of [
    'server',
    'management',
    'access',
    'credentials',
    'requests',
    'oauth',
    'upstream',
    'multimedia',
    'observability',
  ]) {
    if (Object.prototype.hasOwnProperty.call(parsed, root)) return true;
  }

  if (asRecord(parsed['api-keys'])) return true;

  const clientCodex = asRecord(asRecord(parsed.client)?.codex);
  if (
    clientCodex &&
    Object.prototype.hasOwnProperty.call(clientCodex, 'optimize-multi-agent-v2')
  ) {
    return true;
  }

  const historicalProviderCodex = asRecord(asRecord(parsed.providers)?.codex);
  if (
    historicalProviderCodex &&
    Object.prototype.hasOwnProperty.call(
      historicalProviderCodex,
      'optimize-multi-agent-v2'
    )
  ) {
    return true;
  }

  const routing = asRecord(parsed.routing);
  return Boolean(
    routing &&
      ['force-model-prefix', 'retry', 'cooldown'].some((key) =>
        Object.prototype.hasOwnProperty.call(routing, key)
      )
  );
}

function docHas(doc: YamlDocument, path: YamlPath): boolean {
  return doc.hasIn(path);
}

function ensureMapInDoc(doc: YamlDocument, path: YamlPath): void {
  const existing = doc.getIn(path, true);
  if (isMap(existing)) return;
  // Use a YAML node here; plain objects are not treated as collections by subsequent `setIn`.
  doc.setIn(path, doc.createNode({}));
}

function deleteIfMapEmpty(doc: YamlDocument, path: YamlPath): void {
  const value = doc.getIn(path, true);
  if (!isMap(value)) return;
  if (value.items.length === 0) doc.deleteIn(path);
}

function setBooleanInDoc(doc: YamlDocument, path: YamlPath, value: boolean): void {
  if (value) {
    doc.setIn(path, true);
    return;
  }
  if (docHas(doc, path)) doc.setIn(path, false);
}

function setStringInDoc(doc: YamlDocument, path: YamlPath, value: unknown): void {
  const safe = typeof value === 'string' ? value : '';
  const trimmed = safe.trim();
  if (trimmed !== '') {
    doc.setIn(path, safe);
    return;
  }
  // Preserve existing empty-string keys to avoid dropping template blocks/comments.
  // Only keep the key when it already exists in the YAML.
  if (docHas(doc, path)) {
    doc.setIn(path, '');
  }
}

function setIntFromStringInDoc(doc: YamlDocument, path: YamlPath, value: unknown): void {
  const safe = typeof value === 'string' ? value : '';
  const trimmed = safe.trim();
  if (trimmed === '') {
    if (docHas(doc, path)) doc.deleteIn(path);
    return;
  }

  if (!/^-?\d+$/.test(trimmed)) {
    return;
  }

  const parsed = Number(trimmed);
  if (Number.isFinite(parsed)) {
    doc.setIn(path, parsed);
    return;
  }
}

function setDisableImageGenerationInDoc(
  doc: YamlDocument,
  path: YamlPath,
  value: DisableImageGenerationMode
): void {
  if (value === 'chat' || value === 'passthrough') {
    doc.setIn(path, value);
    return;
  }

  if (value === 'true') {
    doc.setIn(path, true);
    return;
  }

  if (docHas(doc, path)) doc.setIn(path, false);
}

function serializeStringListForYaml(items?: string[]): string[] {
  return (items ?? []).map((item) => item.trim()).filter(Boolean);
}

function serializePluginStoreAuthForYaml(
  rules: PluginStoreAuthRule[]
): Array<Record<string, unknown>> {
  return rules
    .map((rule) => {
      const match = rule.match.trim();
      if (!match) return null;
      const item: Record<string, unknown> = {
        match,
        type: rule.type,
      };
      const applyTo = serializeStringListForYaml(rule.applyTo);
      if (applyTo.length > 0) item['apply-to'] = applyTo;
      if (rule.tokenEnv.trim()) item['token-env'] = rule.tokenEnv.trim();
      if (rule.usernameEnv.trim()) item['username-env'] = rule.usernameEnv.trim();
      if (rule.passwordEnv.trim()) item['password-env'] = rule.passwordEnv.trim();
      if (rule.headerName.trim()) item['header-name'] = rule.headerName.trim();
      if (rule.headerValueEnv.trim()) item['header-value-env'] = rule.headerValueEnv.trim();
      if (rule.allowInsecure) item['allow-insecure'] = true;
      return item;
    })
    .filter((rule): rule is Record<string, unknown> => Boolean(rule));
}

function areStringArraysEqual(left: string[] | undefined, right: string[] | undefined): boolean {
  const leftItems = left ?? [];
  const rightItems = right ?? [];
  if (leftItems.length !== rightItems.length) return false;
  return leftItems.every((item, index) => item === rightItems[index]);
}

function arePluginStoreAuthRulesEqual(
  left: PluginStoreAuthRule[] | undefined,
  right: PluginStoreAuthRule[] | undefined
): boolean {
  const leftItems = left ?? [];
  const rightItems = right ?? [];
  if (leftItems.length !== rightItems.length) return false;
  return leftItems.every((a, index) => {
    const b = rightItems[index];
    return (
      Boolean(b) &&
      a.match === b.match &&
      a.type === b.type &&
      a.tokenEnv === b.tokenEnv &&
      a.usernameEnv === b.usernameEnv &&
      a.passwordEnv === b.passwordEnv &&
      a.headerName === b.headerName &&
      a.headerValueEnv === b.headerValueEnv &&
      a.allowInsecure === b.allowInsecure &&
      areStringArraysEqual(a.applyTo, b.applyTo)
    );
  });
}

function getNonNegativeIntegerError(value: string): 'non_negative_integer' | undefined {
  const trimmed = value.trim();
  if (!trimmed) return undefined;
  if (!/^-?\d+$/.test(trimmed)) return 'non_negative_integer';
  return Number(trimmed) >= 0 ? undefined : 'non_negative_integer';
}

function getIntegerError(value: string): 'integer' | undefined {
  const trimmed = value.trim();
  if (!trimmed) return undefined;
  return /^-?\d+$/.test(trimmed) ? undefined : 'integer';
}

function getPortError(value: string): 'port_range' | undefined {
  const trimmed = value.trim();
  if (!trimmed) return undefined;
  if (!/^\d+$/.test(trimmed)) return 'port_range';
  const parsed = Number(trimmed);
  return parsed >= 1 && parsed <= 65535 ? undefined : 'port_range';
}

function getRedisUsageQueueRetentionError(value: string): 'retention_seconds_range' | undefined {
  const trimmed = value.trim();
  if (!trimmed) return undefined;
  if (!/^\d+$/.test(trimmed)) return 'retention_seconds_range';
  const parsed = Number(trimmed);
  return parsed >= 1 && parsed <= 3600 ? undefined : 'retention_seconds_range';
}

function parseDisableImageGenerationMode(raw: unknown): DisableImageGenerationMode {
  if (raw === true) return 'true';
  if (typeof raw === 'string') {
    const normalized = raw.trim().toLowerCase();
    if (normalized === 'true') return 'true';
    if (normalized === 'chat') return 'chat';
    if (normalized === 'passthrough') return 'passthrough';
  }
  return 'false';
}

export function getVisualConfigValidationErrors(
  values: VisualConfigValues
): VisualConfigValidationErrors {
  return {
    port: getPortError(values.port),
    errorLogsMaxFiles: getNonNegativeIntegerError(values.errorLogsMaxFiles),
    logsMaxTotalSizeMb: getNonNegativeIntegerError(values.logsMaxTotalSizeMb),
    redisUsageQueueRetentionSeconds: getRedisUsageQueueRetentionError(
      values.redisUsageQueueRetentionSeconds
    ),
    transientErrorCooldownSeconds: getIntegerError(values.transientErrorCooldownSeconds),
    requestRetry: getNonNegativeIntegerError(values.requestRetry),
    maxRetryCredentials: getNonNegativeIntegerError(values.maxRetryCredentials),
    maxRetryInterval: getNonNegativeIntegerError(values.maxRetryInterval),
    authAutoRefreshWorkers: getNonNegativeIntegerError(values.authAutoRefreshWorkers),
    'streaming.keepaliveSeconds': getNonNegativeIntegerError(values.streaming.keepaliveSeconds),
    'streaming.bootstrapRetries': getNonNegativeIntegerError(values.streaming.bootstrapRetries),
    'streaming.nonstreamKeepaliveInterval': getNonNegativeIntegerError(
      values.streaming.nonstreamKeepaliveInterval
    ),
  };
}

function deleteLegacyApiKeysProvider(doc: YamlDocument): void {
  if (docHas(doc, ['auth', 'providers', 'config-api-key', 'api-key-entries'])) {
    doc.deleteIn(['auth', 'providers', 'config-api-key', 'api-key-entries']);
  }
  if (docHas(doc, ['auth', 'providers', 'config-api-key', 'api-keys'])) {
    doc.deleteIn(['auth', 'providers', 'config-api-key', 'api-keys']);
  }
  deleteIfMapEmpty(doc, ['auth', 'providers', 'config-api-key']);
  deleteIfMapEmpty(doc, ['auth', 'providers']);
  deleteIfMapEmpty(doc, ['auth']);
}

function deepClone<T>(value: T): T {
  if (typeof structuredClone === 'function') return structuredClone(value);
  return JSON.parse(JSON.stringify(value)) as T;
}

type VisualConfigState = {
  visualValues: VisualConfigValues;
  baselineValues: VisualConfigValues;
  dirtyFields: Set<string>;
  visualParseError: string | null;
};

type VisualConfigAction =
  | {
      type: 'load_success';
      values: VisualConfigValues;
    }
  | {
      type: 'load_error';
      error: string;
    }
  | {
      type: 'set_values';
      values: Partial<VisualConfigValues>;
    }
  | {
      type: 'commit_api_keys';
      apiKeysText: string;
    };

function createInitialVisualConfigState(): VisualConfigState {
  const initialValues = deepClone(DEFAULT_VISUAL_VALUES);
  return {
    visualValues: initialValues,
    baselineValues: deepClone(initialValues),
    dirtyFields: new Set(),
    visualParseError: null,
  };
}

function mergeVisualConfigValues(
  currentValues: VisualConfigValues,
  patch: Partial<VisualConfigValues>
): VisualConfigValues {
  const nextValues: VisualConfigValues = { ...currentValues, ...patch } as VisualConfigValues;
  if (patch.streaming) {
    nextValues.streaming = { ...currentValues.streaming, ...patch.streaming };
  }
  return nextValues;
}

function getNextDirtyFields(
  currentDirtyFields: Set<string>,
  patch: Partial<VisualConfigValues>,
  nextValues: VisualConfigValues,
  baselineValues: VisualConfigValues
): Set<string> {
  const nextDirtyFields = new Set(currentDirtyFields);
  const updateDirty = (key: string, isEqual: boolean) => {
    if (isEqual) {
      nextDirtyFields.delete(key);
    } else {
      nextDirtyFields.add(key);
    }
  };
  const updateScalarDirty = (key: keyof VisualConfigValues) => {
    if (Object.prototype.hasOwnProperty.call(patch, key)) {
      updateDirty(key, nextValues[key] === baselineValues[key]);
    }
  };

  (
    [
      'rmDisableAutoUpdatePanel',
      'errorLogsMaxFiles',
      'pluginsEnabled',
      'pluginsDir',
      'pluginStoreSourcesText',
      'passthroughHeaders',
      'disableCooling',
      'saveCooldownStatus',
      'transientErrorCooldownSeconds',
      'disableClaudeCloakMode',
      'disableImageGeneration',
      'gptImage2BaseModel',
      'videoResultAuthCacheTtl',
      'authAutoRefreshWorkers',
      'pprofEnable',
      'pprofAddr',
      'antigravitySignatureCacheEnabled',
      'antigravitySignatureBypassStrict',
      'claudeHeaderUserAgent',
      'claudeHeaderPackageVersion',
      'claudeHeaderRuntimeVersion',
      'claudeHeaderOs',
      'claudeHeaderArch',
      'claudeHeaderTimeout',
      'claudeHeaderStabilizeDeviceProfile',
      'codexHeaderUserAgent',
      'codexHeaderBetaFeatures',
      'codexIdentityConfuse',
    ] as Array<keyof VisualConfigValues>
  ).forEach(updateScalarDirty);

  if (Object.prototype.hasOwnProperty.call(patch, 'pluginStoreAuth')) {
    updateDirty(
      'pluginStoreAuth',
      arePluginStoreAuthRulesEqual(nextValues.pluginStoreAuth, baselineValues.pluginStoreAuth)
    );
  }

  if (Object.prototype.hasOwnProperty.call(patch, 'host')) {
    updateDirty('host', nextValues.host === baselineValues.host);
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'port')) {
    updateDirty('port', nextValues.port === baselineValues.port);
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'tlsEnable')) {
    updateDirty('tlsEnable', nextValues.tlsEnable === baselineValues.tlsEnable);
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'tlsCert')) {
    updateDirty('tlsCert', nextValues.tlsCert === baselineValues.tlsCert);
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'tlsKey')) {
    updateDirty('tlsKey', nextValues.tlsKey === baselineValues.tlsKey);
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'rmAllowRemote')) {
    updateDirty('rmAllowRemote', nextValues.rmAllowRemote === baselineValues.rmAllowRemote);
  }
  if (
    Object.prototype.hasOwnProperty.call(patch, 'rmSecretKey') ||
    Object.prototype.hasOwnProperty.call(patch, 'rmSecretKeyAction')
  ) {
    updateDirty(
      'rmSecretKey',
      nextValues.rmSecretKeyAction === baselineValues.rmSecretKeyAction &&
        nextValues.rmSecretKey === baselineValues.rmSecretKey
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'rmDisableControlPanel')) {
    updateDirty(
      'rmDisableControlPanel',
      nextValues.rmDisableControlPanel === baselineValues.rmDisableControlPanel
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'rmPanelRepo')) {
    updateDirty('rmPanelRepo', nextValues.rmPanelRepo === baselineValues.rmPanelRepo);
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'authDir')) {
    updateDirty('authDir', nextValues.authDir === baselineValues.authDir);
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'apiKeysText')) {
    updateDirty('apiKeysText', nextValues.apiKeysText === baselineValues.apiKeysText);
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'debug')) {
    updateDirty('debug', nextValues.debug === baselineValues.debug);
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'commercialMode')) {
    updateDirty('commercialMode', nextValues.commercialMode === baselineValues.commercialMode);
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'usageStatisticsEnabled')) {
    updateDirty(
      'usageStatisticsEnabled',
      nextValues.usageStatisticsEnabled === baselineValues.usageStatisticsEnabled
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'loggingToFile')) {
    updateDirty('loggingToFile', nextValues.loggingToFile === baselineValues.loggingToFile);
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'requestLog')) {
    updateDirty('requestLog', nextValues.requestLog === baselineValues.requestLog);
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'logsMaxTotalSizeMb')) {
    updateDirty(
      'logsMaxTotalSizeMb',
      nextValues.logsMaxTotalSizeMb === baselineValues.logsMaxTotalSizeMb
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'redisUsageQueueRetentionSeconds')) {
    updateDirty(
      'redisUsageQueueRetentionSeconds',
      nextValues.redisUsageQueueRetentionSeconds === baselineValues.redisUsageQueueRetentionSeconds
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'proxyUrl')) {
    updateDirty('proxyUrl', nextValues.proxyUrl === baselineValues.proxyUrl);
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'forceModelPrefix')) {
    updateDirty(
      'forceModelPrefix',
      nextValues.forceModelPrefix === baselineValues.forceModelPrefix
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'requestRetry')) {
    updateDirty('requestRetry', nextValues.requestRetry === baselineValues.requestRetry);
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'maxRetryCredentials')) {
    updateDirty(
      'maxRetryCredentials',
      nextValues.maxRetryCredentials === baselineValues.maxRetryCredentials
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'maxRetryInterval')) {
    updateDirty(
      'maxRetryInterval',
      nextValues.maxRetryInterval === baselineValues.maxRetryInterval
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'wsAuth')) {
    updateDirty('wsAuth', nextValues.wsAuth === baselineValues.wsAuth);
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'quotaSwitchProject')) {
    updateDirty(
      'quotaSwitchProject',
      nextValues.quotaSwitchProject === baselineValues.quotaSwitchProject
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'quotaSwitchPreviewModel')) {
    updateDirty(
      'quotaSwitchPreviewModel',
      nextValues.quotaSwitchPreviewModel === baselineValues.quotaSwitchPreviewModel
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'quotaAntigravityCredits')) {
    updateDirty(
      'quotaAntigravityCredits',
      nextValues.quotaAntigravityCredits === baselineValues.quotaAntigravityCredits
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'routingStrategy')) {
    updateDirty('routingStrategy', nextValues.routingStrategy === baselineValues.routingStrategy);
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'routingSessionAffinity')) {
    updateDirty(
      'routingSessionAffinity',
      nextValues.routingSessionAffinity === baselineValues.routingSessionAffinity
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'routingSessionAffinityTTL')) {
    updateDirty(
      'routingSessionAffinityTTL',
      nextValues.routingSessionAffinityTTL === baselineValues.routingSessionAffinityTTL
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'payloadDefaultRules')) {
    updateDirty(
      'payloadDefaultRules',
      arePayloadRulesEqual(nextValues.payloadDefaultRules, baselineValues.payloadDefaultRules)
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'payloadDefaultRawRules')) {
    updateDirty(
      'payloadDefaultRawRules',
      arePayloadRulesEqual(nextValues.payloadDefaultRawRules, baselineValues.payloadDefaultRawRules)
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'payloadOverrideRules')) {
    updateDirty(
      'payloadOverrideRules',
      arePayloadRulesEqual(nextValues.payloadOverrideRules, baselineValues.payloadOverrideRules)
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'payloadOverrideRawRules')) {
    updateDirty(
      'payloadOverrideRawRules',
      arePayloadRulesEqual(
        nextValues.payloadOverrideRawRules,
        baselineValues.payloadOverrideRawRules
      )
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'payloadFilterRules')) {
    updateDirty(
      'payloadFilterRules',
      arePayloadFilterRulesEqual(nextValues.payloadFilterRules, baselineValues.payloadFilterRules)
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'devinSensitiveWords')) {
    updateDirty(
      'devinSensitiveWords',
      areStringArraysEqual(nextValues.devinSensitiveWords, baselineValues.devinSensitiveWords)
    );
  }
  if (patch.streaming) {
    const streamingPatch = patch.streaming;
    if (Object.prototype.hasOwnProperty.call(streamingPatch, 'keepaliveSeconds')) {
      updateDirty(
        'streaming.keepaliveSeconds',
        nextValues.streaming.keepaliveSeconds === baselineValues.streaming.keepaliveSeconds
      );
    }
    if (Object.prototype.hasOwnProperty.call(streamingPatch, 'bootstrapRetries')) {
      updateDirty(
        'streaming.bootstrapRetries',
        nextValues.streaming.bootstrapRetries === baselineValues.streaming.bootstrapRetries
      );
    }
    if (Object.prototype.hasOwnProperty.call(streamingPatch, 'nonstreamKeepaliveInterval')) {
      updateDirty(
        'streaming.nonstreamKeepaliveInterval',
        nextValues.streaming.nonstreamKeepaliveInterval ===
          baselineValues.streaming.nonstreamKeepaliveInterval
      );
    }
  }

  return nextDirtyFields;
}

function visualConfigReducer(
  state: VisualConfigState,
  action: VisualConfigAction
): VisualConfigState {
  switch (action.type) {
    case 'load_success':
      return {
        visualValues: action.values,
        baselineValues: deepClone(action.values),
        dirtyFields: new Set(),
        visualParseError: null,
      };
    case 'load_error':
      return {
        ...state,
        visualParseError: action.error,
      };
    case 'set_values': {
      const nextValues = mergeVisualConfigValues(state.visualValues, action.values);
      const nextDirtyFields = getNextDirtyFields(
        state.dirtyFields,
        action.values,
        nextValues,
        state.baselineValues
      );

      return {
        ...state,
        visualValues: nextValues,
        dirtyFields: nextDirtyFields,
      };
    }
    case 'commit_api_keys': {
      const dirtyFields = new Set(state.dirtyFields);
      dirtyFields.delete('apiKeysText');

      return {
        ...state,
        visualValues: {
          ...state.visualValues,
          apiKeysText: action.apiKeysText,
        },
        baselineValues: {
          ...state.baselineValues,
          apiKeysText: action.apiKeysText,
        },
        dirtyFields,
      };
    }
    default:
      return state;
  }
}

export function useVisualConfig(runtime: VisualConfigRuntime = {}) {
  const [state, dispatch] = useReducer(
    visualConfigReducer,
    undefined,
    createInitialVisualConfigState
  );
  const {
    visualValues: storedVisualValues,
    visualParseError,
    dirtyFields,
  } = state;
  const codexIdentityConfuseCompatibility = getCodexIdentityConfuseCompatibility(
    runtime.serverVersion,
    runtime.serverCommit
  );
  // Unknown/custom builds are conservative: do not expose a removed upstream
  // control unless the connected CPA version is known to support it.
  const codexIdentityConfuseSupported = codexIdentityConfuseCompatibility === 'supported';
  const visualValues = useMemo<VisualConfigValues>(
    () => ({
      ...storedVisualValues,
      codexIdentityConfuse: codexIdentityConfuseSupported
        ? storedVisualValues.codexIdentityConfuse
        : false,
      codexIdentityConfuseSupported,
    }),
    [codexIdentityConfuseSupported, storedVisualValues]
  );
  const visualDirty = dirtyFields.size > 0;
  const visualValidationErrors = useMemo(
    () => getVisualConfigValidationErrors(visualValues),
    [visualValues]
  );
  const visualHasPayloadValidationErrors = useMemo(
    () =>
      hasPayloadParamValidationErrors(visualValues.payloadDefaultRules) ||
      hasPayloadParamValidationErrors(visualValues.payloadDefaultRawRules) ||
      hasPayloadParamValidationErrors(visualValues.payloadOverrideRules) ||
      hasPayloadParamValidationErrors(visualValues.payloadOverrideRawRules),
    [
      visualValues.payloadDefaultRules,
      visualValues.payloadDefaultRawRules,
      visualValues.payloadOverrideRules,
      visualValues.payloadOverrideRawRules,
    ]
  );

  const loadVisualValuesFromYaml = useCallback((yamlContent: string) => {
    try {
      const document = parseDocument(yamlContent, YAML_EFFECTIVE_PARSE_OPTIONS);
      if (document.errors.length > 0) {
        throw new Error(document.errors[0]?.message ?? 'Invalid YAML');
      }

      const parsedRaw: unknown = parseYaml(yamlContent, YAML_EFFECTIVE_PARSE_OPTIONS) || {};
      const parsed = asRecord(parsedRaw) ?? {};
      const readCompat = (path: YamlPath, aliases: YamlPath[] = []) =>
        readVisualConfigValue(parsed, path, aliases);
      const quotaExceeded = asRecord(parsed['quota-exceeded']);
      const routing = asRecord(parsed.routing);
      const plugins = asRecord(parsed.plugins);

      const newValues: VisualConfigValues = {
        host: typeof readCompat(['host']) === 'string' ? (readCompat(['host']) as string) : '',
        port: String(readCompat(['port']) ?? ''),

        tlsEnable: Boolean(readCompat(['tls', 'enable'])),
        tlsCert:
          typeof readCompat(['tls', 'cert']) === 'string'
            ? (readCompat(['tls', 'cert']) as string)
            : '',
        tlsKey:
          typeof readCompat(['tls', 'key']) === 'string'
            ? (readCompat(['tls', 'key']) as string)
            : '',

        rmAllowRemote: Boolean(readCompat(['remote-management', 'allow-remote'])),
        rmSecretKey: '',
        rmSecretKeyAction: 'unchanged',
        rmSecretKeyConfigured:
          typeof readCompat(['remote-management', 'secret-key']) === 'string' &&
          (readCompat(['remote-management', 'secret-key']) as string).length > 0,
        rmDisableControlPanel: Boolean(
          readCompat(['remote-management', 'disable-control-panel'])
        ),
        rmDisableAutoUpdatePanel: Boolean(
          readCompat(['remote-management', 'disable-auto-update-panel'])
        ),
        rmPanelRepo:
          typeof readCompat(
            ['remote-management', 'panel-github-repository'],
            [['remote-management', 'panel-repo']]
          ) === 'string'
            ? (readCompat(
                ['remote-management', 'panel-github-repository'],
                [['remote-management', 'panel-repo']]
              ) as string)
            : '',

        authDir:
          typeof readCompat(['auth-dir']) === 'string' ? (readCompat(['auth-dir']) as string) : '',
        apiKeysText: resolveApiKeysText(parsed),
        pluginsEnabled: Boolean(plugins?.enabled),
        pluginsDir: typeof plugins?.dir === 'string' ? plugins.dir : '',
        pluginStoreSourcesText: parseStringArrayText(
          plugins?.['store-sources'] ?? plugins?.storeSources
        ),
        pluginStoreAuth: parsePluginStoreAuthRules(plugins?.['store-auth'] ?? plugins?.storeAuth),

        debug: Boolean(readCompat(['debug'])),
        pprofEnable: Boolean(readCompat(['pprof', 'enable'])),
        pprofAddr:
          typeof readCompat(['pprof', 'addr']) === 'string'
            ? (readCompat(['pprof', 'addr']) as string)
            : '127.0.0.1:8316',
        commercialMode: Boolean(readCompat(['commercial-mode'])),
        usageStatisticsEnabled: Boolean(
          readCompat(['usage-statistics-enabled'], [['usageStatisticsEnabled']])
        ),
        loggingToFile: Boolean(readCompat(['logging-to-file'])),
        requestLog: Boolean(readCompat(['request-log'])),
        logsMaxTotalSizeMb: String(readCompat(['logs-max-total-size-mb']) ?? ''),
        errorLogsMaxFiles: String(readCompat(['error-logs-max-files']) ?? ''),
        redisUsageQueueRetentionSeconds: String(
          readCompat(
            ['redis-usage-queue-retention-seconds'],
            [['redisUsageQueueRetentionSeconds']]
          ) ?? ''
        ),

        proxyUrl:
          typeof readCompat(['proxy-url']) === 'string'
            ? (readCompat(['proxy-url']) as string)
            : '',
        forceModelPrefix: Boolean(readCompat(['force-model-prefix'])),
        passthroughHeaders: Boolean(readCompat(['passthrough-headers'])),
        requestRetry: String(readCompat(['request-retry']) ?? ''),
        maxRetryCredentials: String(readCompat(['max-retry-credentials']) ?? ''),
        maxRetryInterval: String(readCompat(['max-retry-interval']) ?? ''),
        disableCooling: Boolean(readCompat(['disable-cooling'])),
        saveCooldownStatus: Boolean(readCompat(['save-cooldown-status'])),
        transientErrorCooldownSeconds: String(
          readCompat(['transient-error-cooldown-seconds']) ?? ''
        ),
        disableClaudeCloakMode: Boolean(readCompat(['disable-claude-cloak-mode'])),
        disableImageGeneration: parseDisableImageGenerationMode(
          readCompat(['disable-image-generation'])
        ),
        gptImage2BaseModel:
          typeof readCompat(['gpt-image-2-base-model']) === 'string'
            ? (readCompat(['gpt-image-2-base-model']) as string)
            : '',
        videoResultAuthCacheTtl:
          typeof readCompat(['video-result-auth-cache-ttl']) === 'string'
            ? (readCompat(['video-result-auth-cache-ttl']) as string)
            : '',
        authAutoRefreshWorkers: String(readCompat(['auth-auto-refresh-workers']) ?? ''),
        wsAuth: Boolean(readCompat(['ws-auth']) ?? true),
        antigravitySignatureCacheEnabled: Boolean(
          readCompat(['antigravity-signature-cache-enabled']) ?? true
        ),
        antigravitySignatureBypassStrict: Boolean(
          readCompat(['antigravity-signature-bypass-strict'])
        ),
        claudeHeaderUserAgent:
          typeof readCompat(['claude-header-defaults', 'user-agent']) === 'string'
            ? (readCompat(['claude-header-defaults', 'user-agent']) as string)
            : '',
        claudeHeaderPackageVersion:
          typeof readCompat(['claude-header-defaults', 'package-version']) === 'string'
            ? (readCompat(['claude-header-defaults', 'package-version']) as string)
            : '',
        claudeHeaderRuntimeVersion:
          typeof readCompat(['claude-header-defaults', 'runtime-version']) === 'string'
            ? (readCompat(['claude-header-defaults', 'runtime-version']) as string)
            : '',
        claudeHeaderOs:
          typeof readCompat(['claude-header-defaults', 'os']) === 'string'
            ? (readCompat(['claude-header-defaults', 'os']) as string)
            : '',
        claudeHeaderArch:
          typeof readCompat(['claude-header-defaults', 'arch']) === 'string'
            ? (readCompat(['claude-header-defaults', 'arch']) as string)
            : '',
        claudeHeaderTimeout:
          typeof readCompat(['claude-header-defaults', 'timeout']) === 'string'
            ? (readCompat(['claude-header-defaults', 'timeout']) as string)
            : '',
        claudeHeaderStabilizeDeviceProfile: Boolean(
          readCompat(['claude-header-defaults', 'stabilize-device-profile'])
        ),
        codexHeaderUserAgent:
          typeof readCompat(['codex-header-defaults', 'user-agent']) === 'string'
            ? (readCompat(['codex-header-defaults', 'user-agent']) as string)
            : '',
        codexHeaderBetaFeatures:
          typeof readCompat(['codex-header-defaults', 'beta-features']) === 'string'
            ? (readCompat(['codex-header-defaults', 'beta-features']) as string)
            : '',
        codexIdentityConfuse: Boolean(
          readCompat(['codex', 'identity-confuse'], [['codex', 'identityConfuse']])
        ),
        // Runtime support is overlaid on returned visualValues so a late version
        // header can update the UI without reparsing the YAML.
        codexIdentityConfuseSupported: true,
        devinSensitiveWords: parseStringList(readCompat(['devin', 'sensitive-words'])),

        quotaSwitchProject: Boolean(quotaExceeded?.['switch-project'] ?? false),
        quotaSwitchPreviewModel: Boolean(quotaExceeded?.['switch-preview-model'] ?? false),
        quotaAntigravityCredits: Boolean(
          readCompat(['quota-exceeded', 'antigravity-credits']) ?? false
        ),

        routingStrategy: normalizeRoutingStrategy(routing?.strategy) ?? 'round-robin',
        routingSessionAffinity: Boolean(
          routing?.['session-affinity'] ?? routing?.sessionAffinity ?? routing?.['sessionAffinity']
        ),
        routingSessionAffinityTTL:
          typeof routing?.['session-affinity-ttl'] === 'string'
            ? routing['session-affinity-ttl']
            : typeof routing?.sessionAffinityTTL === 'string'
              ? routing.sessionAffinityTTL
              : typeof routing?.['sessionAffinityTTL'] === 'string'
                ? routing['sessionAffinityTTL']
                : '',

        payloadDefaultRules: parsePayloadRules(readCompat(['payload', 'default'])),
        payloadDefaultRawRules: parseRawPayloadRules(readCompat(['payload', 'default-raw'])),
        payloadOverrideRules: parsePayloadRules(readCompat(['payload', 'override'])),
        payloadOverrideRawRules: parseRawPayloadRules(readCompat(['payload', 'override-raw'])),
        payloadFilterRules: parsePayloadFilterRules(readCompat(['payload', 'filter'])),

        streaming: {
          keepaliveSeconds: String(readCompat(['streaming', 'keepalive-seconds']) ?? ''),
          bootstrapRetries: String(readCompat(['streaming', 'bootstrap-retries']) ?? ''),
          nonstreamKeepaliveInterval: String(
            readCompat(['nonstream-keepalive-interval']) ?? ''
          ),
        },
      };

      dispatch({ type: 'load_success', values: newValues });
      return { ok: true as const };
    } catch (error: unknown) {
      const message = error instanceof Error ? error.message : 'Invalid YAML';
      dispatch({ type: 'load_error', error: message });
      return { ok: false as const, error: message };
    }
  }, []);

  const applyVisualChangesToYaml = useCallback(
    (currentYaml: string): string => {
      try {
        const doc = parseDocument(currentYaml, YAML_EFFECTIVE_PARSE_OPTIONS);
        const sourceDoc = parseDocument(currentYaml, YAML_EFFECTIVE_PARSE_OPTIONS);
        if (doc.errors.length > 0 || sourceDoc.errors.length > 0) return currentYaml;
        if (!isMap(doc.contents)) {
          doc.contents = doc.createNode({}) as unknown as typeof doc.contents;
        }
        const values = visualValues;
        const isDirty = (key: string) => dirtyFields.has(key);
        const parsedCurrent =
          asRecord(parseYaml(currentYaml, YAML_EFFECTIVE_PARSE_OPTIONS)) ?? {};
        const useV8Layout = isV8VisualConfigLayout(parsedCurrent);
        const materializedPaths = new Set<string>();
        const mappedPath = (path: YamlPath) =>
          useV8Layout ? mapVisualConfigV8Path(path) : path;
        const pruneEmptyParents = (path: YamlPath) => {
          for (let length = path.length - 1; length >= 1; length -= 1) {
            deleteIfMapEmpty(doc, path.slice(0, length));
          }
        };
        const materializeParents = (path: YamlPath) => {
          for (let length = 1; length < path.length; length += 1) {
            materializeEffectiveMapAtPath(
              doc,
              sourceDoc,
              path.slice(0, length),
              parsedCurrent,
              materializedPaths
            );
          }
        };
        const ensureParents = (path: YamlPath) => {
          for (let length = 1; length < path.length; length += 1) {
            const parentPath = path.slice(0, length);
            materializeEffectiveMapAtPath(
              doc,
              sourceDoc,
              parentPath,
              parsedCurrent,
              materializedPaths
            );
            ensureMapInDoc(doc, parentPath);
          }
        };
        const legacyAlternative = (path: YamlPath, target: YamlPath) =>
          useV8Layout && !pathsEqual(path, target) ? path : null;
        const historicalAlternatives = (path: YamlPath, target: YamlPath) =>
          useV8Layout && !pathsEqual(path, target) ? getHistoricalV8Aliases(path) : [];
        const effectiveHas = (path: YamlPath) => readObjectPath(parsedCurrent, path).found;
        const hasCompat = (path: YamlPath) => {
          const target = mappedPath(path);
          const legacy = legacyAlternative(path, target);
          return (
            docHas(doc, target) ||
            effectiveHas(target) ||
            historicalAlternatives(path, target).some(
              (alias) => docHas(doc, alias) || effectiveHas(alias)
            ) ||
            Boolean(legacy && (docHas(doc, legacy) || effectiveHas(legacy)))
          );
        };
        const deleteCompat = (path: YamlPath) => {
          const target = mappedPath(path);
          const legacy = legacyAlternative(path, target);
          materializeParents(target);
          if (docHas(doc, target)) {
            doc.deleteIn(target);
            pruneEmptyParents(target);
          }
          for (const alias of historicalAlternatives(path, target)) {
            materializeParents(alias);
            if (docHas(doc, alias)) {
              doc.deleteIn(alias);
              pruneEmptyParents(alias);
            }
          }
          if (legacy) {
            materializeParents(legacy);
            if (docHas(doc, legacy)) {
              doc.deleteIn(legacy);
              pruneEmptyParents(legacy);
            }
          }
        };
        const dropCompatibilityAlternatives = (path: YamlPath, target: YamlPath) => {
          for (const alias of historicalAlternatives(path, target)) {
            materializeParents(alias);
            if (docHas(doc, alias)) {
              doc.deleteIn(alias);
              pruneEmptyParents(alias);
            }
          }
          const legacy = legacyAlternative(path, target);
          if (legacy) {
            materializeParents(legacy);
            if (docHas(doc, legacy)) {
              doc.deleteIn(legacy);
              pruneEmptyParents(legacy);
            }
          }
        };
        const ensureCompatMap = (path: YamlPath) => {
          const target = mappedPath(path);
          ensureParents(target);
          materializeEffectiveMapAtPath(
            doc,
            sourceDoc,
            target,
            parsedCurrent,
            materializedPaths
          );
          ensureMapInDoc(doc, target);
        };
        const deleteCompatIfMapEmpty = (path: YamlPath) => {
          const target = mappedPath(path);
          deleteIfMapEmpty(doc, target);
          pruneEmptyParents(target);
          for (const alias of historicalAlternatives(path, target)) {
            deleteIfMapEmpty(doc, alias);
            pruneEmptyParents(alias);
          }
          const legacy = legacyAlternative(path, target);
          if (legacy) {
            deleteIfMapEmpty(doc, legacy);
            pruneEmptyParents(legacy);
          }
        };
        const setCompatValue = (path: YamlPath, value: unknown) => {
          const target = mappedPath(path);
          ensureParents(target);
          doc.setIn(target, value);
          dropCompatibilityAlternatives(path, target);
        };
        const setCompatBoolean = (path: YamlPath, value: boolean) => {
          const target = mappedPath(path);
          if (useV8Layout && !pathsEqual(target, path)) {
            ensureParents(target);
            doc.setIn(target, value);
            dropCompatibilityAlternatives(path, target);
            return;
          }
          if (!value && effectiveHas(target)) {
            ensureParents(target);
            doc.setIn(target, false);
            return;
          }
          setBooleanInDoc(doc, target, value);
        };
        const setCompatString = (path: YamlPath, value: unknown) => {
          const target = mappedPath(path);
          if (useV8Layout && !pathsEqual(target, path)) {
            const safe = typeof value === 'string' ? value : '';
            if (safe.trim() !== '' || hasCompat(path)) {
              ensureParents(target);
              doc.setIn(target, safe);
            }
            dropCompatibilityAlternatives(path, target);
            return;
          }
          const safe = typeof value === 'string' ? value : '';
          if (safe.trim() === '' && effectiveHas(target)) {
            ensureParents(target);
            doc.setIn(target, '');
            return;
          }
          setStringInDoc(doc, target, value);
        };
        const setCompatInt = (path: YamlPath, value: unknown) => {
          const target = mappedPath(path);
          if (useV8Layout && !pathsEqual(target, path)) {
            const safe = typeof value === 'string' ? value : '';
            const trimmed = safe.trim();
            if (trimmed === '') {
              deleteCompat(path);
              return;
            }
            if (!/^-?\d+$/.test(trimmed)) return;
            const parsed = Number(trimmed);
            if (!Number.isFinite(parsed)) return;
            ensureParents(target);
            doc.setIn(target, parsed);
            dropCompatibilityAlternatives(path, target);
            return;
          }
          const safe = typeof value === 'string' ? value : '';
          if (
            safe.trim() === '' &&
            pathUsesYamlInheritance(sourceDoc, target, parsedCurrent)
          ) {
            ensureParents(target);
            doc.setIn(target, null);
            return;
          }
          setIntFromStringInDoc(doc, target, value);
        };
        const setCompatDisableImageGeneration = (
          path: YamlPath,
          value: DisableImageGenerationMode
        ) => {
          const target = mappedPath(path);
          if (useV8Layout && !pathsEqual(target, path)) {
            ensureParents(target);
            doc.setIn(
              target,
              value === 'chat' || value === 'passthrough'
                ? value
                : value === 'true'
                  ? true
                  : false
            );
            dropCompatibilityAlternatives(path, target);
            return;
          }
          if (value === 'false' && effectiveHas(target)) {
            ensureParents(target);
            doc.setIn(target, false);
            return;
          }
          setDisableImageGenerationInDoc(doc, target, value);
        };

        if (isDirty('host')) setCompatString(['host'], values.host);
        if (isDirty('port')) setCompatInt(['port'], values.port);

        const tlsDirty = isDirty('tlsEnable') || isDirty('tlsCert') || isDirty('tlsKey');
        if (tlsDirty) {
          ensureCompatMap(['tls']);
          if (isDirty('tlsEnable')) setCompatBoolean(['tls', 'enable'], values.tlsEnable);
          if (isDirty('tlsCert')) setCompatString(['tls', 'cert'], values.tlsCert);
          if (isDirty('tlsKey')) setCompatString(['tls', 'key'], values.tlsKey);
          deleteCompatIfMapEmpty(['tls']);
        }

        const hasRemoteManagementSecretKeyUpdate =
          isDirty('rmSecretKey') &&
          (values.rmSecretKeyAction === 'clear' ||
            (values.rmSecretKeyAction === 'replace' && values.rmSecretKey.length > 0));
        const remoteManagementDirty =
          isDirty('rmAllowRemote') ||
          isDirty('rmSecretKey') ||
          isDirty('rmDisableControlPanel') ||
          isDirty('rmDisableAutoUpdatePanel') ||
          isDirty('rmPanelRepo');
        if (remoteManagementDirty) {
          ensureCompatMap(['remote-management']);
          if (isDirty('rmAllowRemote')) {
            setCompatBoolean(['remote-management', 'allow-remote'], values.rmAllowRemote);
          }
          if (
            hasRemoteManagementSecretKeyUpdate &&
            values.rmSecretKeyAction === 'replace' &&
            values.rmSecretKey.length > 0
          ) {
            setCompatValue(['remote-management', 'secret-key'], values.rmSecretKey);
          } else if (hasRemoteManagementSecretKeyUpdate && values.rmSecretKeyAction === 'clear') {
            setCompatValue(['remote-management', 'secret-key'], '');
          }
          if (isDirty('rmDisableControlPanel')) {
            setCompatBoolean(
              ['remote-management', 'disable-control-panel'],
              values.rmDisableControlPanel
            );
          }
          if (isDirty('rmDisableAutoUpdatePanel')) {
            setCompatBoolean(
              ['remote-management', 'disable-auto-update-panel'],
              values.rmDisableAutoUpdatePanel
            );
          }
          if (isDirty('rmPanelRepo')) {
            setCompatString(
              ['remote-management', 'panel-github-repository'],
              values.rmPanelRepo
            );
            if (hasCompat(['remote-management', 'panel-repo'])) {
              deleteCompat(['remote-management', 'panel-repo']);
            }
          }
          deleteCompatIfMapEmpty(['remote-management']);
        }

        if (isDirty('authDir')) setCompatString(['auth-dir'], values.authDir);
        if (isDirty('apiKeysText')) {
          const apiKeys = values.apiKeysText
            .split('\n')
            .map((key) => key.trim())
            .filter(Boolean);
          // In v8 the root mapping holds upstream credentials, not client keys.
          const hasUpstreamKeyGroups = asRecord(parsedCurrent['api-keys']) !== null;
          if (useV8Layout || hasCompat(['access', 'api-keys']) || hasUpstreamKeyGroups) {
            ensureCompatMap(['access']);
            // Keep an explicit empty list authoritative over any legacy keys.
            setCompatValue(['access', 'api-keys'], apiKeys);
            if (!hasUpstreamKeyGroups) deleteCompat(['api-keys']);
          } else if (apiKeys.length > 0) {
            setCompatValue(['api-keys'], apiKeys);
          } else if (hasCompat(['api-keys'])) {
            deleteCompat(['api-keys']);
          }
          deleteLegacyApiKeysProvider(doc);
        }

        if (isDirty('debug')) setCompatBoolean(['debug'], values.debug);

        const shouldWritePprofEnable = isDirty('pprofEnable');
        const shouldWritePprofAddr = isDirty('pprofAddr');
        if (shouldWritePprofEnable || shouldWritePprofAddr) {
          ensureCompatMap(['pprof']);
          if (shouldWritePprofEnable) setCompatValue(['pprof', 'enable'], values.pprofEnable);
          if (shouldWritePprofAddr) setCompatString(['pprof', 'addr'], values.pprofAddr);
          deleteCompatIfMapEmpty(['pprof']);
        }

        if (isDirty('commercialMode')) {
          setCompatBoolean(['commercial-mode'], values.commercialMode);
        }
        if (isDirty('usageStatisticsEnabled')) {
          setCompatBoolean(['usage-statistics-enabled'], values.usageStatisticsEnabled);
        }
        if (isDirty('loggingToFile')) {
          setCompatBoolean(['logging-to-file'], values.loggingToFile);
        }
        if (isDirty('requestLog')) {
          setCompatBoolean(['request-log'], values.requestLog);
        }
        if (isDirty('logsMaxTotalSizeMb')) {
          setCompatInt(['logs-max-total-size-mb'], values.logsMaxTotalSizeMb);
        }
        if (isDirty('errorLogsMaxFiles')) {
          setCompatInt(['error-logs-max-files'], values.errorLogsMaxFiles);
        }
        if (isDirty('redisUsageQueueRetentionSeconds')) {
          setCompatInt(
            ['redis-usage-queue-retention-seconds'],
            values.redisUsageQueueRetentionSeconds
          );
        }

        const pluginStoreSources = values.pluginStoreSourcesText
          .split('\n')
          .map((source) => source.trim())
          .filter(Boolean);
        const shouldWritePluginStoreAuth = isDirty('pluginStoreAuth');
        const shouldWritePluginsEnabled = isDirty('pluginsEnabled');
        const shouldWritePluginsDir = isDirty('pluginsDir');
        const shouldWritePluginStoreSources = isDirty('pluginStoreSourcesText');
        if (
          shouldWritePluginsEnabled ||
          shouldWritePluginsDir ||
          shouldWritePluginStoreSources ||
          shouldWritePluginStoreAuth
        ) {
          ensureCompatMap(['plugins']);
          if (shouldWritePluginsEnabled) {
            setCompatValue(['plugins', 'enabled'], values.pluginsEnabled);
          }
          if (shouldWritePluginsDir) {
            if (values.pluginsDir.trim()) {
              setCompatValue(['plugins', 'dir'], values.pluginsDir);
            } else if (hasCompat(['plugins', 'dir'])) {
              deleteCompat(['plugins', 'dir']);
            }
          }
          if (shouldWritePluginStoreSources) {
            if (pluginStoreSources.length > 0) {
              setCompatValue(['plugins', 'store-sources'], pluginStoreSources);
            } else if (hasCompat(['plugins', 'store-sources'])) {
              deleteCompat(['plugins', 'store-sources']);
            }
          }
          if (shouldWritePluginStoreAuth) {
            const storeAuth = serializePluginStoreAuthForYaml(values.pluginStoreAuth);
            if (storeAuth.length > 0) {
              setCompatValue(['plugins', 'store-auth'], storeAuth);
            } else if (hasCompat(['plugins', 'store-auth'])) {
              deleteCompat(['plugins', 'store-auth']);
            }
          }
          deleteCompatIfMapEmpty(['plugins']);
        }

        if (isDirty('proxyUrl')) setCompatString(['proxy-url'], values.proxyUrl);
        if (isDirty('forceModelPrefix')) {
          setCompatBoolean(['force-model-prefix'], values.forceModelPrefix);
        }
        if (isDirty('passthroughHeaders')) {
          setCompatBoolean(['passthrough-headers'], values.passthroughHeaders);
        }
        if (isDirty('requestRetry')) setCompatInt(['request-retry'], values.requestRetry);
        if (isDirty('maxRetryCredentials')) {
          setCompatInt(['max-retry-credentials'], values.maxRetryCredentials);
        }
        if (isDirty('maxRetryInterval')) {
          setCompatInt(['max-retry-interval'], values.maxRetryInterval);
        }
        if (isDirty('disableCooling')) setCompatBoolean(['disable-cooling'], values.disableCooling);
        if (isDirty('saveCooldownStatus')) {
          setCompatBoolean(['save-cooldown-status'], values.saveCooldownStatus);
        }
        if (isDirty('transientErrorCooldownSeconds')) {
          setCompatInt(
            ['transient-error-cooldown-seconds'],
            values.transientErrorCooldownSeconds
          );
        }
        if (isDirty('disableClaudeCloakMode')) {
          setCompatBoolean(['disable-claude-cloak-mode'], values.disableClaudeCloakMode);
        }
        if (isDirty('disableImageGeneration')) {
          setCompatDisableImageGeneration(
            ['disable-image-generation'],
            values.disableImageGeneration
          );
        }
        if (isDirty('gptImage2BaseModel')) {
          setCompatString(['gpt-image-2-base-model'], values.gptImage2BaseModel);
        }
        if (isDirty('videoResultAuthCacheTtl')) {
          setCompatString(['video-result-auth-cache-ttl'], values.videoResultAuthCacheTtl);
        }
        if (isDirty('authAutoRefreshWorkers')) {
          setCompatInt(['auth-auto-refresh-workers'], values.authAutoRefreshWorkers);
        }
        if (isDirty('wsAuth')) {
          setCompatValue(['ws-auth'], values.wsAuth);
        }
        if (isDirty('antigravitySignatureCacheEnabled')) {
          setCompatValue(
            ['antigravity-signature-cache-enabled'],
            values.antigravitySignatureCacheEnabled
          );
        }
        if (isDirty('antigravitySignatureBypassStrict')) {
          setCompatBoolean(
            ['antigravity-signature-bypass-strict'],
            values.antigravitySignatureBypassStrict
          );
        }

        const claudeHeadersDirty =
          isDirty('claudeHeaderUserAgent') ||
          isDirty('claudeHeaderPackageVersion') ||
          isDirty('claudeHeaderRuntimeVersion') ||
          isDirty('claudeHeaderOs') ||
          isDirty('claudeHeaderArch') ||
          isDirty('claudeHeaderTimeout') ||
          isDirty('claudeHeaderStabilizeDeviceProfile');
        if (claudeHeadersDirty) {
          ensureCompatMap(['claude-header-defaults']);
          if (isDirty('claudeHeaderUserAgent')) {
            setCompatString(
              ['claude-header-defaults', 'user-agent'],
              values.claudeHeaderUserAgent
            );
          }
          if (isDirty('claudeHeaderPackageVersion')) {
            setCompatString(
              ['claude-header-defaults', 'package-version'],
              values.claudeHeaderPackageVersion
            );
          }
          if (isDirty('claudeHeaderRuntimeVersion')) {
            setCompatString(
              ['claude-header-defaults', 'runtime-version'],
              values.claudeHeaderRuntimeVersion
            );
          }
          if (isDirty('claudeHeaderOs')) {
            setCompatString(['claude-header-defaults', 'os'], values.claudeHeaderOs);
          }
          if (isDirty('claudeHeaderArch')) {
            setCompatString(['claude-header-defaults', 'arch'], values.claudeHeaderArch);
          }
          if (isDirty('claudeHeaderTimeout')) {
            setCompatString(['claude-header-defaults', 'timeout'], values.claudeHeaderTimeout);
          }
          if (isDirty('claudeHeaderStabilizeDeviceProfile')) {
            setCompatBoolean(
              ['claude-header-defaults', 'stabilize-device-profile'],
              values.claudeHeaderStabilizeDeviceProfile
            );
          }
          deleteCompatIfMapEmpty(['claude-header-defaults']);
        }

        const codexHeadersDirty =
          isDirty('codexHeaderUserAgent') || isDirty('codexHeaderBetaFeatures');
        if (codexHeadersDirty) {
          ensureCompatMap(['codex-header-defaults']);
          if (isDirty('codexHeaderUserAgent')) {
            setCompatString(
              ['codex-header-defaults', 'user-agent'],
              values.codexHeaderUserAgent
            );
          }
          if (isDirty('codexHeaderBetaFeatures')) {
            setCompatString(
              ['codex-header-defaults', 'beta-features'],
              values.codexHeaderBetaFeatures
            );
          }
          deleteCompatIfMapEmpty(['codex-header-defaults']);
        }

        const codexIdentityConfusePath = ['codex', 'identity-confuse'];
        const codexIdentityConfuseLegacyPath = ['codex', 'identityConfuse'];
        if (isDirty('codexIdentityConfuse') && codexIdentityConfuseSupported) {
          ensureCompatMap(['codex']);
          setCompatValue(codexIdentityConfusePath, values.codexIdentityConfuse);
          if (hasCompat(codexIdentityConfuseLegacyPath)) {
            deleteCompat(codexIdentityConfuseLegacyPath);
          }
          deleteCompatIfMapEmpty(['codex']);
        }

        if (isDirty('devinSensitiveWords')) {
          const devinSensitiveWords = serializeStringListForYaml(values.devinSensitiveWords);
          if (devinSensitiveWords.length > 0) {
            ensureCompatMap(['devin']);
            setCompatValue(['devin', 'sensitive-words'], devinSensitiveWords);
          } else if (hasCompat(['devin', 'sensitive-words'])) {
            deleteCompat(['devin', 'sensitive-words']);
          }
          deleteCompatIfMapEmpty(['devin']);
        }

        const writeQuotaSwitchProject = isDirty('quotaSwitchProject');
        const writeQuotaSwitchPreviewModel = isDirty('quotaSwitchPreviewModel');
        const writeQuotaAntigravityCredits = isDirty('quotaAntigravityCredits');
        if (writeQuotaSwitchProject || writeQuotaSwitchPreviewModel || writeQuotaAntigravityCredits) {
          ensureCompatMap(['quota-exceeded']);
          if (writeQuotaSwitchProject) {
            setCompatValue(['quota-exceeded', 'switch-project'], values.quotaSwitchProject);
          }
          if (writeQuotaSwitchPreviewModel) {
            setCompatValue(
              ['quota-exceeded', 'switch-preview-model'],
              values.quotaSwitchPreviewModel
            );
          }
          if (writeQuotaAntigravityCredits) {
            setCompatValue(['quota-exceeded', 'antigravity-credits'], values.quotaAntigravityCredits);
          }
          deleteCompatIfMapEmpty(['quota-exceeded']);
        }

        const routingDirty =
          isDirty('routingStrategy') ||
          isDirty('routingSessionAffinity') ||
          isDirty('routingSessionAffinityTTL');
        if (routingDirty) {
          ensureCompatMap(['routing']);
          if (isDirty('routingStrategy')) {
            setCompatValue(['routing', 'strategy'], values.routingStrategy);
          }
          if (isDirty('routingSessionAffinity')) {
            setCompatBoolean(['routing', 'session-affinity'], values.routingSessionAffinity);
          }
          if (isDirty('routingSessionAffinityTTL')) {
            setCompatString(
              ['routing', 'session-affinity-ttl'],
              values.routingSessionAffinityTTL
            );
          }
          deleteCompatIfMapEmpty(['routing']);
        }

        const keepaliveSeconds =
          typeof values.streaming?.keepaliveSeconds === 'string'
            ? values.streaming.keepaliveSeconds
            : '';
        const bootstrapRetries =
          typeof values.streaming?.bootstrapRetries === 'string'
            ? values.streaming.bootstrapRetries
            : '';
        const nonstreamKeepaliveInterval =
          typeof values.streaming?.nonstreamKeepaliveInterval === 'string'
            ? values.streaming.nonstreamKeepaliveInterval
            : '';

        const streamingDirty =
          isDirty('streaming.keepaliveSeconds') || isDirty('streaming.bootstrapRetries');
        if (streamingDirty) {
          ensureCompatMap(['streaming']);
          if (isDirty('streaming.keepaliveSeconds')) {
            setCompatInt(['streaming', 'keepalive-seconds'], keepaliveSeconds);
          }
          if (isDirty('streaming.bootstrapRetries')) {
            setCompatInt(['streaming', 'bootstrap-retries'], bootstrapRetries);
          }
          deleteCompatIfMapEmpty(['streaming']);
        }

        if (isDirty('streaming.nonstreamKeepaliveInterval')) {
          setCompatInt(['nonstream-keepalive-interval'], nonstreamKeepaliveInterval);
        }

        const payloadDirty =
          isDirty('payloadDefaultRules') ||
          isDirty('payloadDefaultRawRules') ||
          isDirty('payloadOverrideRules') ||
          isDirty('payloadOverrideRawRules') ||
          isDirty('payloadFilterRules');
        if (payloadDirty) {
          ensureCompatMap(['payload']);
          if (isDirty('payloadDefaultRules')) {
            if (values.payloadDefaultRules.length > 0) {
              setCompatValue(
                ['payload', 'default'],
                serializePayloadRulesForYaml(values.payloadDefaultRules)
              );
            } else if (hasCompat(['payload', 'default'])) {
              deleteCompat(['payload', 'default']);
            }
          }
          if (isDirty('payloadDefaultRawRules')) {
            if (values.payloadDefaultRawRules.length > 0) {
              setCompatValue(
                ['payload', 'default-raw'],
                serializeRawPayloadRulesForYaml(values.payloadDefaultRawRules)
              );
            } else if (hasCompat(['payload', 'default-raw'])) {
              deleteCompat(['payload', 'default-raw']);
            }
          }
          if (isDirty('payloadOverrideRules')) {
            if (values.payloadOverrideRules.length > 0) {
              setCompatValue(
                ['payload', 'override'],
                serializePayloadRulesForYaml(values.payloadOverrideRules)
              );
            } else if (hasCompat(['payload', 'override'])) {
              deleteCompat(['payload', 'override']);
            }
          }
          if (isDirty('payloadOverrideRawRules')) {
            if (values.payloadOverrideRawRules.length > 0) {
              setCompatValue(
                ['payload', 'override-raw'],
                serializeRawPayloadRulesForYaml(values.payloadOverrideRawRules)
              );
            } else if (hasCompat(['payload', 'override-raw'])) {
              deleteCompat(['payload', 'override-raw']);
            }
          }
          if (isDirty('payloadFilterRules')) {
            if (values.payloadFilterRules.length > 0) {
              setCompatValue(
                ['payload', 'filter'],
                serializePayloadFilterRulesForYaml(values.payloadFilterRules)
              );
            } else if (hasCompat(['payload', 'filter'])) {
              deleteCompat(['payload', 'filter']);
            }
          }
          deleteCompatIfMapEmpty(['payload']);
        }

        return doc.toString({ indent: 2, lineWidth: 120, minContentWidth: 0 });
      } catch {
        return currentYaml;
      }
    },
    [codexIdentityConfuseSupported, dirtyFields, visualValues]
  );

  const setVisualValues = useCallback((newValues: Partial<VisualConfigValues>) => {
    dispatch({ type: 'set_values', values: newValues });
  }, []);

  const commitApiKeysText = useCallback((apiKeysText: string) => {
    dispatch({ type: 'commit_api_keys', apiKeysText });
  }, []);

  return {
    visualValues,
    visualDirty,
    visualParseError,
    visualValidationErrors,
    visualHasPayloadValidationErrors,
    loadVisualValuesFromYaml,
    applyVisualChangesToYaml,
    setVisualValues,
    commitApiKeysText,
  };
}
