import { useCallback, useMemo, useReducer } from 'react';
import { isMap, parse as parseYaml, parseDocument } from 'yaml';
import type {
  DisableImageGenerationMode,
  PluginStoreAuthApplyTo,
  PluginStoreAuthRule,
  PluginStoreAuthType,
  VisualConfigValues,
  VisualConfigValidationErrors,
} from '@/types/visualConfig';
import { DEFAULT_VISUAL_VALUES } from '@/types/visualConfig';
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

type YamlDocument = ReturnType<typeof parseDocument>;
type YamlPath = string[];

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
    default:
      return state;
  }
}

export function useVisualConfig() {
  const [state, dispatch] = useReducer(
    visualConfigReducer,
    undefined,
    createInitialVisualConfigState
  );
  const { visualValues, visualParseError, dirtyFields } = state;
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
      const document = parseDocument(yamlContent);
      if (document.errors.length > 0) {
        throw new Error(document.errors[0]?.message ?? 'Invalid YAML');
      }

      const parsedRaw: unknown = parseYaml(yamlContent) || {};
      const parsed = asRecord(parsedRaw) ?? {};
      const tls = asRecord(parsed.tls);
      const remoteManagement = asRecord(parsed['remote-management']);
      const pprof = asRecord(parsed.pprof);
      const quotaExceeded = asRecord(parsed['quota-exceeded']);
      const routing = asRecord(parsed.routing);
      const plugins = asRecord(parsed.plugins);
      const payload = asRecord(parsed.payload);
      const streaming = asRecord(parsed.streaming);
      const claudeHeaderDefaults = asRecord(parsed['claude-header-defaults']);
      const codexHeaderDefaults = asRecord(parsed['codex-header-defaults']);
      const codex = asRecord(parsed.codex);

      const newValues: VisualConfigValues = {
        host: typeof parsed.host === 'string' ? parsed.host : '',
        port: String(parsed.port ?? ''),

        tlsEnable: Boolean(tls?.enable),
        tlsCert: typeof tls?.cert === 'string' ? tls.cert : '',
        tlsKey: typeof tls?.key === 'string' ? tls.key : '',

        rmAllowRemote: Boolean(remoteManagement?.['allow-remote']),
        rmSecretKey: '',
        rmSecretKeyAction: 'unchanged',
        rmSecretKeyConfigured:
          typeof remoteManagement?.['secret-key'] === 'string' &&
          remoteManagement['secret-key'].length > 0,
        rmDisableControlPanel: Boolean(remoteManagement?.['disable-control-panel']),
        rmDisableAutoUpdatePanel: Boolean(remoteManagement?.['disable-auto-update-panel']),
        rmPanelRepo:
          typeof remoteManagement?.['panel-github-repository'] === 'string'
            ? remoteManagement['panel-github-repository']
            : typeof remoteManagement?.['panel-repo'] === 'string'
              ? remoteManagement['panel-repo']
              : '',

        authDir: typeof parsed['auth-dir'] === 'string' ? parsed['auth-dir'] : '',
        apiKeysText: resolveApiKeysText(parsed),
        pluginsEnabled: Boolean(plugins?.enabled),
        pluginsDir: typeof plugins?.dir === 'string' ? plugins.dir : '',
        pluginStoreSourcesText: parseStringArrayText(
          plugins?.['store-sources'] ?? plugins?.storeSources
        ),
        pluginStoreAuth: parsePluginStoreAuthRules(plugins?.['store-auth'] ?? plugins?.storeAuth),

        debug: Boolean(parsed.debug),
        pprofEnable: Boolean(pprof?.enable),
        pprofAddr: typeof pprof?.addr === 'string' ? pprof.addr : '127.0.0.1:8316',
        commercialMode: Boolean(parsed['commercial-mode']),
        usageStatisticsEnabled: Boolean(
          parsed['usage-statistics-enabled'] ?? parsed.usageStatisticsEnabled
        ),
        loggingToFile: Boolean(parsed['logging-to-file']),
        logsMaxTotalSizeMb: String(parsed['logs-max-total-size-mb'] ?? ''),
        errorLogsMaxFiles: String(parsed['error-logs-max-files'] ?? ''),
        redisUsageQueueRetentionSeconds: String(
          parsed['redis-usage-queue-retention-seconds'] ??
            parsed.redisUsageQueueRetentionSeconds ??
            ''
        ),

        proxyUrl: typeof parsed['proxy-url'] === 'string' ? parsed['proxy-url'] : '',
        forceModelPrefix: Boolean(parsed['force-model-prefix']),
        passthroughHeaders: Boolean(parsed['passthrough-headers']),
        requestRetry: String(parsed['request-retry'] ?? ''),
        maxRetryCredentials: String(parsed['max-retry-credentials'] ?? ''),
        maxRetryInterval: String(parsed['max-retry-interval'] ?? ''),
        disableCooling: Boolean(parsed['disable-cooling']),
        saveCooldownStatus: Boolean(parsed['save-cooldown-status']),
        transientErrorCooldownSeconds: String(parsed['transient-error-cooldown-seconds'] ?? ''),
        disableClaudeCloakMode: Boolean(parsed['disable-claude-cloak-mode']),
        disableImageGeneration: parseDisableImageGenerationMode(parsed['disable-image-generation']),
        gptImage2BaseModel:
          typeof parsed['gpt-image-2-base-model'] === 'string'
            ? parsed['gpt-image-2-base-model']
            : '',
        videoResultAuthCacheTtl:
          typeof parsed['video-result-auth-cache-ttl'] === 'string'
            ? parsed['video-result-auth-cache-ttl']
            : '',
        authAutoRefreshWorkers: String(parsed['auth-auto-refresh-workers'] ?? ''),
        wsAuth: Boolean(parsed['ws-auth'] ?? true),
        antigravitySignatureCacheEnabled: Boolean(
          parsed['antigravity-signature-cache-enabled'] ?? true
        ),
        antigravitySignatureBypassStrict: Boolean(parsed['antigravity-signature-bypass-strict']),
        claudeHeaderUserAgent:
          typeof claudeHeaderDefaults?.['user-agent'] === 'string'
            ? claudeHeaderDefaults['user-agent']
            : '',
        claudeHeaderPackageVersion:
          typeof claudeHeaderDefaults?.['package-version'] === 'string'
            ? claudeHeaderDefaults['package-version']
            : '',
        claudeHeaderRuntimeVersion:
          typeof claudeHeaderDefaults?.['runtime-version'] === 'string'
            ? claudeHeaderDefaults['runtime-version']
            : '',
        claudeHeaderOs: typeof claudeHeaderDefaults?.os === 'string' ? claudeHeaderDefaults.os : '',
        claudeHeaderArch:
          typeof claudeHeaderDefaults?.arch === 'string' ? claudeHeaderDefaults.arch : '',
        claudeHeaderTimeout:
          typeof claudeHeaderDefaults?.timeout === 'string' ? claudeHeaderDefaults.timeout : '',
        claudeHeaderStabilizeDeviceProfile: Boolean(
          claudeHeaderDefaults?.['stabilize-device-profile']
        ),
        codexHeaderUserAgent:
          typeof codexHeaderDefaults?.['user-agent'] === 'string'
            ? codexHeaderDefaults['user-agent']
            : '',
        codexHeaderBetaFeatures:
          typeof codexHeaderDefaults?.['beta-features'] === 'string'
            ? codexHeaderDefaults['beta-features']
            : '',
        codexIdentityConfuse: Boolean(codex?.['identity-confuse'] ?? codex?.identityConfuse),

        quotaSwitchProject: Boolean(quotaExceeded?.['switch-project'] ?? false),
        quotaSwitchPreviewModel: Boolean(quotaExceeded?.['switch-preview-model'] ?? false),
        quotaAntigravityCredits: Boolean(quotaExceeded?.['antigravity-credits'] ?? false),

        routingStrategy: routing?.strategy === 'fill-first' ? 'fill-first' : 'round-robin',
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

        payloadDefaultRules: parsePayloadRules(payload?.default),
        payloadDefaultRawRules: parseRawPayloadRules(payload?.['default-raw']),
        payloadOverrideRules: parsePayloadRules(payload?.override),
        payloadOverrideRawRules: parseRawPayloadRules(payload?.['override-raw']),
        payloadFilterRules: parsePayloadFilterRules(payload?.filter),

        streaming: {
          keepaliveSeconds: String(streaming?.['keepalive-seconds'] ?? ''),
          bootstrapRetries: String(streaming?.['bootstrap-retries'] ?? ''),
          nonstreamKeepaliveInterval: String(parsed['nonstream-keepalive-interval'] ?? ''),
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
        const doc = parseDocument(currentYaml);
        if (doc.errors.length > 0) return currentYaml;
        if (!isMap(doc.contents)) {
          doc.contents = doc.createNode({}) as unknown as typeof doc.contents;
        }
        const values = visualValues;
        const isDirty = (key: string) => dirtyFields.has(key);

        if (isDirty('host')) setStringInDoc(doc, ['host'], values.host);
        if (isDirty('port')) setIntFromStringInDoc(doc, ['port'], values.port);

        const tlsDirty = isDirty('tlsEnable') || isDirty('tlsCert') || isDirty('tlsKey');
        if (tlsDirty) {
          ensureMapInDoc(doc, ['tls']);
          if (isDirty('tlsEnable')) setBooleanInDoc(doc, ['tls', 'enable'], values.tlsEnable);
          if (isDirty('tlsCert')) setStringInDoc(doc, ['tls', 'cert'], values.tlsCert);
          if (isDirty('tlsKey')) setStringInDoc(doc, ['tls', 'key'], values.tlsKey);
          deleteIfMapEmpty(doc, ['tls']);
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
          ensureMapInDoc(doc, ['remote-management']);
          if (isDirty('rmAllowRemote')) {
            setBooleanInDoc(doc, ['remote-management', 'allow-remote'], values.rmAllowRemote);
          }
          if (
            hasRemoteManagementSecretKeyUpdate &&
            values.rmSecretKeyAction === 'replace' &&
            values.rmSecretKey.length > 0
          ) {
            doc.setIn(['remote-management', 'secret-key'], values.rmSecretKey);
          } else if (hasRemoteManagementSecretKeyUpdate && values.rmSecretKeyAction === 'clear') {
            doc.setIn(['remote-management', 'secret-key'], '');
          }
          if (isDirty('rmDisableControlPanel')) {
            setBooleanInDoc(
              doc,
              ['remote-management', 'disable-control-panel'],
              values.rmDisableControlPanel
            );
          }
          if (isDirty('rmDisableAutoUpdatePanel')) {
            setBooleanInDoc(
              doc,
              ['remote-management', 'disable-auto-update-panel'],
              values.rmDisableAutoUpdatePanel
            );
          }
          if (isDirty('rmPanelRepo')) {
            setStringInDoc(
              doc,
              ['remote-management', 'panel-github-repository'],
              values.rmPanelRepo
            );
            if (docHas(doc, ['remote-management', 'panel-repo'])) {
              doc.deleteIn(['remote-management', 'panel-repo']);
            }
          }
          deleteIfMapEmpty(doc, ['remote-management']);
        }

        if (isDirty('authDir')) setStringInDoc(doc, ['auth-dir'], values.authDir);
        if (isDirty('apiKeysText')) {
          const apiKeys = values.apiKeysText
            .split('\n')
            .map((key) => key.trim())
            .filter(Boolean);
          if (apiKeys.length > 0) {
            doc.setIn(['api-keys'], apiKeys);
          } else if (docHas(doc, ['api-keys'])) {
            doc.deleteIn(['api-keys']);
          }
          deleteLegacyApiKeysProvider(doc);
        }

        if (isDirty('debug')) setBooleanInDoc(doc, ['debug'], values.debug);

        const shouldWritePprofEnable = isDirty('pprofEnable');
        const shouldWritePprofAddr = isDirty('pprofAddr');
        if (shouldWritePprofEnable || shouldWritePprofAddr) {
          ensureMapInDoc(doc, ['pprof']);
          if (shouldWritePprofEnable) doc.setIn(['pprof', 'enable'], values.pprofEnable);
          if (shouldWritePprofAddr) setStringInDoc(doc, ['pprof', 'addr'], values.pprofAddr);
          deleteIfMapEmpty(doc, ['pprof']);
        }

        if (isDirty('commercialMode')) {
          setBooleanInDoc(doc, ['commercial-mode'], values.commercialMode);
        }
        if (isDirty('usageStatisticsEnabled')) {
          setBooleanInDoc(doc, ['usage-statistics-enabled'], values.usageStatisticsEnabled);
        }
        if (isDirty('loggingToFile')) {
          setBooleanInDoc(doc, ['logging-to-file'], values.loggingToFile);
        }
        if (isDirty('logsMaxTotalSizeMb')) {
          setIntFromStringInDoc(doc, ['logs-max-total-size-mb'], values.logsMaxTotalSizeMb);
        }
        if (isDirty('errorLogsMaxFiles')) {
          setIntFromStringInDoc(doc, ['error-logs-max-files'], values.errorLogsMaxFiles);
        }
        if (isDirty('redisUsageQueueRetentionSeconds')) {
          setIntFromStringInDoc(
            doc,
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
          ensureMapInDoc(doc, ['plugins']);
          if (shouldWritePluginsEnabled) {
            doc.setIn(['plugins', 'enabled'], values.pluginsEnabled);
          }
          if (shouldWritePluginsDir) {
            if (values.pluginsDir.trim()) {
              doc.setIn(['plugins', 'dir'], values.pluginsDir);
            } else if (docHas(doc, ['plugins', 'dir'])) {
              doc.deleteIn(['plugins', 'dir']);
            }
          }
          if (shouldWritePluginStoreSources) {
            if (pluginStoreSources.length > 0) {
              doc.setIn(['plugins', 'store-sources'], pluginStoreSources);
            } else if (docHas(doc, ['plugins', 'store-sources'])) {
              doc.deleteIn(['plugins', 'store-sources']);
            }
          }
          if (shouldWritePluginStoreAuth) {
            const storeAuth = serializePluginStoreAuthForYaml(values.pluginStoreAuth);
            if (storeAuth.length > 0) {
              doc.setIn(['plugins', 'store-auth'], storeAuth);
            } else if (docHas(doc, ['plugins', 'store-auth'])) {
              doc.deleteIn(['plugins', 'store-auth']);
            }
          }
          deleteIfMapEmpty(doc, ['plugins']);
        }

        if (isDirty('proxyUrl')) setStringInDoc(doc, ['proxy-url'], values.proxyUrl);
        if (isDirty('forceModelPrefix')) {
          setBooleanInDoc(doc, ['force-model-prefix'], values.forceModelPrefix);
        }
        if (isDirty('passthroughHeaders')) {
          setBooleanInDoc(doc, ['passthrough-headers'], values.passthroughHeaders);
        }
        if (isDirty('requestRetry')) setIntFromStringInDoc(doc, ['request-retry'], values.requestRetry);
        if (isDirty('maxRetryCredentials')) {
          setIntFromStringInDoc(doc, ['max-retry-credentials'], values.maxRetryCredentials);
        }
        if (isDirty('maxRetryInterval')) {
          setIntFromStringInDoc(doc, ['max-retry-interval'], values.maxRetryInterval);
        }
        if (isDirty('disableCooling')) setBooleanInDoc(doc, ['disable-cooling'], values.disableCooling);
        if (isDirty('saveCooldownStatus')) {
          setBooleanInDoc(doc, ['save-cooldown-status'], values.saveCooldownStatus);
        }
        if (isDirty('transientErrorCooldownSeconds')) {
          setIntFromStringInDoc(
            doc,
            ['transient-error-cooldown-seconds'],
            values.transientErrorCooldownSeconds
          );
        }
        if (isDirty('disableClaudeCloakMode')) {
          setBooleanInDoc(doc, ['disable-claude-cloak-mode'], values.disableClaudeCloakMode);
        }
        if (isDirty('disableImageGeneration')) {
          setDisableImageGenerationInDoc(
            doc,
            ['disable-image-generation'],
            values.disableImageGeneration
          );
        }
        if (isDirty('gptImage2BaseModel')) {
          setStringInDoc(doc, ['gpt-image-2-base-model'], values.gptImage2BaseModel);
        }
        if (isDirty('videoResultAuthCacheTtl')) {
          setStringInDoc(doc, ['video-result-auth-cache-ttl'], values.videoResultAuthCacheTtl);
        }
        if (isDirty('authAutoRefreshWorkers')) {
          setIntFromStringInDoc(doc, ['auth-auto-refresh-workers'], values.authAutoRefreshWorkers);
        }
        if (isDirty('wsAuth')) {
          doc.setIn(['ws-auth'], values.wsAuth);
        }
        if (isDirty('antigravitySignatureCacheEnabled')) {
          doc.setIn(
            ['antigravity-signature-cache-enabled'],
            values.antigravitySignatureCacheEnabled
          );
        }
        if (isDirty('antigravitySignatureBypassStrict')) {
          setBooleanInDoc(
            doc,
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
          ensureMapInDoc(doc, ['claude-header-defaults']);
          if (isDirty('claudeHeaderUserAgent')) {
            setStringInDoc(
              doc,
              ['claude-header-defaults', 'user-agent'],
              values.claudeHeaderUserAgent
            );
          }
          if (isDirty('claudeHeaderPackageVersion')) {
            setStringInDoc(
              doc,
              ['claude-header-defaults', 'package-version'],
              values.claudeHeaderPackageVersion
            );
          }
          if (isDirty('claudeHeaderRuntimeVersion')) {
            setStringInDoc(
              doc,
              ['claude-header-defaults', 'runtime-version'],
              values.claudeHeaderRuntimeVersion
            );
          }
          if (isDirty('claudeHeaderOs')) {
            setStringInDoc(doc, ['claude-header-defaults', 'os'], values.claudeHeaderOs);
          }
          if (isDirty('claudeHeaderArch')) {
            setStringInDoc(doc, ['claude-header-defaults', 'arch'], values.claudeHeaderArch);
          }
          if (isDirty('claudeHeaderTimeout')) {
            setStringInDoc(doc, ['claude-header-defaults', 'timeout'], values.claudeHeaderTimeout);
          }
          if (isDirty('claudeHeaderStabilizeDeviceProfile')) {
            setBooleanInDoc(
              doc,
              ['claude-header-defaults', 'stabilize-device-profile'],
              values.claudeHeaderStabilizeDeviceProfile
            );
          }
          deleteIfMapEmpty(doc, ['claude-header-defaults']);
        }

        const codexHeadersDirty =
          isDirty('codexHeaderUserAgent') || isDirty('codexHeaderBetaFeatures');
        if (codexHeadersDirty) {
          ensureMapInDoc(doc, ['codex-header-defaults']);
          if (isDirty('codexHeaderUserAgent')) {
            setStringInDoc(
              doc,
              ['codex-header-defaults', 'user-agent'],
              values.codexHeaderUserAgent
            );
          }
          if (isDirty('codexHeaderBetaFeatures')) {
            setStringInDoc(
              doc,
              ['codex-header-defaults', 'beta-features'],
              values.codexHeaderBetaFeatures
            );
          }
          deleteIfMapEmpty(doc, ['codex-header-defaults']);
        }

        const codexIdentityConfusePath = ['codex', 'identity-confuse'];
        const codexIdentityConfuseLegacyPath = ['codex', 'identityConfuse'];
        if (isDirty('codexIdentityConfuse')) {
          ensureMapInDoc(doc, ['codex']);
          doc.setIn(codexIdentityConfusePath, values.codexIdentityConfuse);
          if (docHas(doc, codexIdentityConfuseLegacyPath)) {
            doc.deleteIn(codexIdentityConfuseLegacyPath);
          }
          deleteIfMapEmpty(doc, ['codex']);
        }

        const writeQuotaSwitchProject = isDirty('quotaSwitchProject');
        const writeQuotaSwitchPreviewModel = isDirty('quotaSwitchPreviewModel');
        const writeQuotaAntigravityCredits = isDirty('quotaAntigravityCredits');
        if (writeQuotaSwitchProject || writeQuotaSwitchPreviewModel || writeQuotaAntigravityCredits) {
          ensureMapInDoc(doc, ['quota-exceeded']);
          if (writeQuotaSwitchProject) {
            doc.setIn(['quota-exceeded', 'switch-project'], values.quotaSwitchProject);
          }
          if (writeQuotaSwitchPreviewModel) {
            doc.setIn(
              ['quota-exceeded', 'switch-preview-model'],
              values.quotaSwitchPreviewModel
            );
          }
          if (writeQuotaAntigravityCredits) {
            doc.setIn(['quota-exceeded', 'antigravity-credits'], values.quotaAntigravityCredits);
          }
          deleteIfMapEmpty(doc, ['quota-exceeded']);
        }

        const routingDirty =
          isDirty('routingStrategy') ||
          isDirty('routingSessionAffinity') ||
          isDirty('routingSessionAffinityTTL');
        if (routingDirty) {
          ensureMapInDoc(doc, ['routing']);
          if (isDirty('routingStrategy')) {
            doc.setIn(['routing', 'strategy'], values.routingStrategy);
          }
          if (isDirty('routingSessionAffinity')) {
            setBooleanInDoc(doc, ['routing', 'session-affinity'], values.routingSessionAffinity);
          }
          if (isDirty('routingSessionAffinityTTL')) {
            setStringInDoc(
              doc,
              ['routing', 'session-affinity-ttl'],
              values.routingSessionAffinityTTL
            );
          }
          deleteIfMapEmpty(doc, ['routing']);
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
          ensureMapInDoc(doc, ['streaming']);
          if (isDirty('streaming.keepaliveSeconds')) {
            setIntFromStringInDoc(doc, ['streaming', 'keepalive-seconds'], keepaliveSeconds);
          }
          if (isDirty('streaming.bootstrapRetries')) {
            setIntFromStringInDoc(doc, ['streaming', 'bootstrap-retries'], bootstrapRetries);
          }
          deleteIfMapEmpty(doc, ['streaming']);
        }

        if (isDirty('streaming.nonstreamKeepaliveInterval')) {
          setIntFromStringInDoc(doc, ['nonstream-keepalive-interval'], nonstreamKeepaliveInterval);
        }

        const payloadDirty =
          isDirty('payloadDefaultRules') ||
          isDirty('payloadDefaultRawRules') ||
          isDirty('payloadOverrideRules') ||
          isDirty('payloadOverrideRawRules') ||
          isDirty('payloadFilterRules');
        if (payloadDirty) {
          ensureMapInDoc(doc, ['payload']);
          if (isDirty('payloadDefaultRules')) {
            if (values.payloadDefaultRules.length > 0) {
              doc.setIn(
                ['payload', 'default'],
                serializePayloadRulesForYaml(values.payloadDefaultRules)
              );
            } else if (docHas(doc, ['payload', 'default'])) {
              doc.deleteIn(['payload', 'default']);
            }
          }
          if (isDirty('payloadDefaultRawRules')) {
            if (values.payloadDefaultRawRules.length > 0) {
              doc.setIn(
                ['payload', 'default-raw'],
                serializeRawPayloadRulesForYaml(values.payloadDefaultRawRules)
              );
            } else if (docHas(doc, ['payload', 'default-raw'])) {
              doc.deleteIn(['payload', 'default-raw']);
            }
          }
          if (isDirty('payloadOverrideRules')) {
            if (values.payloadOverrideRules.length > 0) {
              doc.setIn(
                ['payload', 'override'],
                serializePayloadRulesForYaml(values.payloadOverrideRules)
              );
            } else if (docHas(doc, ['payload', 'override'])) {
              doc.deleteIn(['payload', 'override']);
            }
          }
          if (isDirty('payloadOverrideRawRules')) {
            if (values.payloadOverrideRawRules.length > 0) {
              doc.setIn(
                ['payload', 'override-raw'],
                serializeRawPayloadRulesForYaml(values.payloadOverrideRawRules)
              );
            } else if (docHas(doc, ['payload', 'override-raw'])) {
              doc.deleteIn(['payload', 'override-raw']);
            }
          }
          if (isDirty('payloadFilterRules')) {
            if (values.payloadFilterRules.length > 0) {
              doc.setIn(
                ['payload', 'filter'],
                serializePayloadFilterRulesForYaml(values.payloadFilterRules)
              );
            } else if (docHas(doc, ['payload', 'filter'])) {
              doc.deleteIn(['payload', 'filter']);
            }
          }
          deleteIfMapEmpty(doc, ['payload']);
        }

        return doc.toString({ indent: 2, lineWidth: 120, minContentWidth: 0 });
      } catch {
        return currentYaml;
      }
    },
    [dirtyFields, visualValues]
  );

  const setVisualValues = useCallback((newValues: Partial<VisualConfigValues>) => {
    dispatch({ type: 'set_values', values: newValues });
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
  };
}
