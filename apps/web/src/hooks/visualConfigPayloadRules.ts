import type {
  PayloadFilterRule,
  PayloadHeaderEntry,
  PayloadModelEntry,
  PayloadParamEntry,
  PayloadParamValidationErrorCode,
  PayloadParamValueType,
  PayloadRule,
} from '@/types/visualConfig';

function asRecord(value: unknown): Record<string, unknown> | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null;
  return value as Record<string, unknown>;
}

export function getPayloadParamValidationError(
  param: PayloadParamEntry
): PayloadParamValidationErrorCode | undefined {
  const trimmedValue = param.value.trim();

  switch (param.valueType) {
    case 'number': {
      if (!trimmedValue) return 'payload_invalid_number';
      const parsed = Number(trimmedValue);
      return Number.isFinite(parsed) ? undefined : 'payload_invalid_number';
    }
    case 'boolean': {
      const normalized = trimmedValue.toLowerCase();
      return normalized === 'true' || normalized === 'false'
        ? undefined
        : 'payload_invalid_boolean';
    }
    case 'json': {
      if (!trimmedValue) return 'payload_invalid_json';
      try {
        JSON.parse(param.value);
        return undefined;
      } catch {
        return 'payload_invalid_json';
      }
    }
    default:
      return undefined;
  }
}

export function hasPayloadParamValidationErrors(rules: PayloadRule[]): boolean {
  return rules.some(
    (rule) =>
      rule.params.some((param) => Boolean(getPayloadParamValidationError(param))) ||
      rule.models.some(
        (model) =>
          (model.match ?? []).some((param) => Boolean(getPayloadParamValidationError(param))) ||
          (model.notMatch ?? []).some((param) => Boolean(getPayloadParamValidationError(param)))
      )
  );
}

function arePayloadModelEntriesEqual(
  left: PayloadRule['models'],
  right: PayloadRule['models']
): boolean {
  if (left === right) return true;
  if (left.length !== right.length) return false;
  for (let i = 0; i < left.length; i += 1) {
    const a = left[i];
    const b = right[i];
    if (!a || !b) return false;
    if (
      a.id !== b.id ||
      a.name !== b.name ||
      a.protocol !== b.protocol ||
      a.fromProtocol !== b.fromProtocol
    ) {
      return false;
    }
    if (!arePayloadHeaderEntriesEqual(a.headers, b.headers)) return false;
    if (!arePayloadParamEntriesEqual(a.match ?? [], b.match ?? [])) return false;
    if (!arePayloadParamEntriesEqual(a.notMatch ?? [], b.notMatch ?? [])) return false;
    if (!areStringArraysEqual(a.exist, b.exist)) return false;
    if (!areStringArraysEqual(a.notExist, b.notExist)) return false;
  }
  return true;
}

function arePayloadParamEntriesEqual(
  left: PayloadRule['params'],
  right: PayloadRule['params']
): boolean {
  if (left === right) return true;
  if (left.length !== right.length) return false;
  for (let i = 0; i < left.length; i += 1) {
    const a = left[i];
    const b = right[i];
    if (!a || !b) return false;
    if (a.id !== b.id || a.path !== b.path || a.valueType !== b.valueType || a.value !== b.value) {
      return false;
    }
  }
  return true;
}

function arePayloadHeaderEntriesEqual(
  left: PayloadHeaderEntry[] | undefined,
  right: PayloadHeaderEntry[] | undefined
): boolean {
  const leftEntries = left ?? [];
  const rightEntries = right ?? [];
  if (leftEntries === rightEntries) return true;
  if (leftEntries.length !== rightEntries.length) return false;
  for (let i = 0; i < leftEntries.length; i += 1) {
    const a = leftEntries[i];
    const b = rightEntries[i];
    if (!a || !b) return false;
    if (a.id !== b.id || a.name !== b.name || a.value !== b.value) return false;
  }
  return true;
}

function areStringArraysEqual(left: string[] | undefined, right: string[] | undefined): boolean {
  const leftItems = left ?? [];
  const rightItems = right ?? [];
  if (leftItems === rightItems) return true;
  if (leftItems.length !== rightItems.length) return false;
  for (let i = 0; i < leftItems.length; i += 1) {
    if (leftItems[i] !== rightItems[i]) return false;
  }
  return true;
}

export function arePayloadRulesEqual(left: PayloadRule[], right: PayloadRule[]): boolean {
  if (left === right) return true;
  if (left.length !== right.length) return false;
  for (let i = 0; i < left.length; i += 1) {
    const a = left[i];
    const b = right[i];
    if (!a || !b) return false;
    if (a.id !== b.id) return false;
    if (!arePayloadModelEntriesEqual(a.models, b.models)) return false;
    if (!arePayloadParamEntriesEqual(a.params, b.params)) return false;
  }
  return true;
}

export function arePayloadFilterRulesEqual(
  left: PayloadFilterRule[],
  right: PayloadFilterRule[]
): boolean {
  if (left === right) return true;
  if (left.length !== right.length) return false;
  for (let i = 0; i < left.length; i += 1) {
    const a = left[i];
    const b = right[i];
    if (!a || !b) return false;
    if (a.id !== b.id) return false;
    if (!arePayloadModelEntriesEqual(a.models, b.models)) return false;
    if (a.params.length !== b.params.length) return false;
    for (let j = 0; j < a.params.length; j += 1) {
      if (a.params[j] !== b.params[j]) return false;
    }
  }
  return true;
}

function parsePayloadParamValue(raw: unknown): { valueType: PayloadParamValueType; value: string } {
  if (typeof raw === 'number') {
    return { valueType: 'number', value: String(raw) };
  }

  if (typeof raw === 'boolean') {
    return { valueType: 'boolean', value: String(raw) };
  }

  if (raw === null || typeof raw === 'object') {
    try {
      const json = JSON.stringify(raw, null, 2);
      return { valueType: 'json', value: json ?? 'null' };
    } catch {
      return { valueType: 'json', value: String(raw) };
    }
  }

  return { valueType: 'string', value: String(raw ?? '') };
}

function parseRawPayloadParamValue(raw: unknown): string {
  if (typeof raw === 'string') return raw;

  try {
    const json = JSON.stringify(raw, null, 2);
    return json ?? '';
  } catch {
    return String(raw ?? '');
  }
}

function parsePayloadProtocol(raw: unknown): PayloadModelEntry['protocol'] {
  if (typeof raw !== 'string') return undefined;
  return raw.trim() ? raw : undefined;
}

function parsePayloadHeaders(raw: unknown, idPrefix: string): PayloadHeaderEntry[] {
  const record = asRecord(raw);
  if (!record) return [];

  return Object.entries(record).map(([name, value], index) => ({
    id: `${idPrefix}-header-${index}`,
    name,
    value: String(value ?? ''),
  }));
}

function parsePayloadConditions(raw: unknown, idPrefix: string): PayloadParamEntry[] {
  if (!Array.isArray(raw)) return [];

  const entries: PayloadParamEntry[] = [];
  raw.forEach((item, itemIndex) => {
    const record = asRecord(item);
    if (!record) {
      if (typeof item === 'string') {
        entries.push({
          id: `${idPrefix}-condition-${itemIndex}-0`,
          path: item,
          valueType: 'string',
          value: '',
        });
      }
      return;
    }

    Object.entries(record).forEach(([path, value], valueIndex) => {
      const parsedValue = parsePayloadParamValue(value);
      entries.push({
        id: `${idPrefix}-condition-${itemIndex}-${valueIndex}`,
        path,
        valueType: parsedValue.valueType,
        value: parsedValue.value,
      });
    });
  });

  return entries;
}

function parseStringList(raw: unknown): string[] {
  return Array.isArray(raw) ? raw.map((item) => String(item ?? '').trim()).filter(Boolean) : [];
}

function parsePayloadModelEntries(raw: unknown, idPrefix: string): PayloadRule['models'] {
  if (!Array.isArray(raw)) return [];

  return raw.map((model, modelIndex) => {
    const modelRecord = asRecord(model);
    const nameRaw =
      typeof model === 'string' ? model : (modelRecord?.name ?? modelRecord?.id ?? '');
    const name = typeof nameRaw === 'string' ? nameRaw : String(nameRaw ?? '');
    const modelId = `${idPrefix}-${modelIndex}`;

    return {
      id: modelId,
      name,
      protocol: parsePayloadProtocol(modelRecord?.protocol),
      fromProtocol: parsePayloadProtocol(modelRecord?.['from-protocol']),
      headers: parsePayloadHeaders(modelRecord?.headers, modelId),
      match: parsePayloadConditions(modelRecord?.match, `${modelId}-match`),
      notMatch: parsePayloadConditions(modelRecord?.['not-match'], `${modelId}-not-match`),
      exist: parseStringList(modelRecord?.exist),
      notExist: parseStringList(modelRecord?.['not-exist']),
    };
  });
}

export function parsePayloadRules(rules: unknown): PayloadRule[] {
  if (!Array.isArray(rules)) return [];

  return rules.map((rule, index) => {
    const record = asRecord(rule) ?? {};

    const models = parsePayloadModelEntries(record.models, `model-${index}`);

    const paramsRecord = asRecord(record.params);
    const params = paramsRecord
      ? Object.entries(paramsRecord).map(([path, value], pIndex) => {
          const parsedValue = parsePayloadParamValue(value);
          return {
            id: `param-${index}-${pIndex}`,
            path,
            valueType: parsedValue.valueType,
            value: parsedValue.value,
          };
        })
      : [];

    return { id: `payload-rule-${index}`, models, params };
  });
}

export function parsePayloadFilterRules(rules: unknown): PayloadFilterRule[] {
  if (!Array.isArray(rules)) return [];

  return rules.map((rule, index) => {
    const record = asRecord(rule) ?? {};

    const models = parsePayloadModelEntries(record.models, `filter-model-${index}`);

    const paramsRaw = record.params;
    const params = Array.isArray(paramsRaw) ? paramsRaw.map(String) : [];

    return { id: `payload-filter-rule-${index}`, models, params };
  });
}

export function parseRawPayloadRules(rules: unknown): PayloadRule[] {
  if (!Array.isArray(rules)) return [];

  return rules.map((rule, index) => {
    const record = asRecord(rule) ?? {};

    const models = parsePayloadModelEntries(record.models, `raw-model-${index}`);

    const paramsRecord = asRecord(record.params);
    const params = paramsRecord
      ? Object.entries(paramsRecord).map(([path, value], pIndex) => ({
          id: `raw-param-${index}-${pIndex}`,
          path,
          valueType: 'json' as const,
          value: parseRawPayloadParamValue(value),
        }))
      : [];

    return { id: `payload-raw-rule-${index}`, models, params };
  });
}

function serializePayloadParamEntryValue(param: PayloadParamEntry): unknown {
  if (param.valueType === 'number') {
    const num = Number(param.value);
    return Number.isFinite(num) ? num : param.value;
  }
  if (param.valueType === 'boolean') {
    return param.value === 'true';
  }
  if (param.valueType === 'json') {
    try {
      return JSON.parse(param.value);
    } catch {
      return param.value;
    }
  }
  return param.value;
}

function serializePayloadHeadersForYaml(headers?: PayloadHeaderEntry[]): Record<string, string> {
  const result: Record<string, string> = {};
  for (const header of headers ?? []) {
    const name = header.name.trim();
    if (!name) continue;
    result[name] = header.value;
  }
  return result;
}

function serializePayloadConditionsForYaml(
  conditions?: PayloadParamEntry[]
): Array<Record<string, unknown>> {
  const result: Array<Record<string, unknown>> = [];
  for (const condition of conditions ?? []) {
    const path = condition.path.trim();
    if (!path) continue;
    result.push({ [path]: serializePayloadParamEntryValue(condition) });
  }
  return result;
}

function serializeStringListForYaml(items?: string[]): string[] {
  return (items ?? []).map((item) => item.trim()).filter(Boolean);
}

function serializePayloadModelsForYaml(
  models: PayloadRule['models']
): Array<Record<string, unknown>> {
  return (models || [])
    .filter((model) => model.name?.trim())
    .map((model) => {
      const obj: Record<string, unknown> = { name: model.name.trim() };
      if (model.protocol) obj.protocol = model.protocol;
      if (model.fromProtocol) obj['from-protocol'] = model.fromProtocol;

      const headers = serializePayloadHeadersForYaml(model.headers);
      if (Object.keys(headers).length > 0) obj.headers = headers;

      const match = serializePayloadConditionsForYaml(model.match);
      if (match.length > 0) obj.match = match;

      const notMatch = serializePayloadConditionsForYaml(model.notMatch);
      if (notMatch.length > 0) obj['not-match'] = notMatch;

      const exist = serializeStringListForYaml(model.exist);
      if (exist.length > 0) obj.exist = exist;

      const notExist = serializeStringListForYaml(model.notExist);
      if (notExist.length > 0) obj['not-exist'] = notExist;

      return obj;
    });
}

export function serializePayloadRulesForYaml(rules: PayloadRule[]): Array<Record<string, unknown>> {
  return rules
    .map((rule) => {
      const models = serializePayloadModelsForYaml(rule.models);

      const params: Record<string, unknown> = {};
      for (const param of rule.params || []) {
        if (!param.path?.trim()) continue;
        params[param.path.trim()] = serializePayloadParamEntryValue(param);
      }

      return { models, params };
    })
    .filter((rule) => rule.models.length > 0);
}

export function serializePayloadFilterRulesForYaml(
  rules: PayloadFilterRule[]
): Array<Record<string, unknown>> {
  return rules
    .map((rule) => {
      const models = serializePayloadModelsForYaml(rule.models);

      const params = (Array.isArray(rule.params) ? rule.params : [])
        .map((path) => String(path).trim())
        .filter(Boolean);

      return { models, params };
    })
    .filter((rule) => rule.models.length > 0);
}

export function serializeRawPayloadRulesForYaml(
  rules: PayloadRule[]
): Array<Record<string, unknown>> {
  return rules
    .map((rule) => {
      const models = serializePayloadModelsForYaml(rule.models);

      const params: Record<string, unknown> = {};
      for (const param of rule.params || []) {
        if (!param.path?.trim()) continue;
        params[param.path.trim()] = param.value;
      }

      return { models, params };
    })
    .filter((rule) => rule.models.length > 0);
}

export const VISUAL_CONFIG_PROTOCOL_OPTIONS = [
  {
    value: '',
    labelKey: 'config_management.visual.payload_rules.provider_default',
    defaultLabel: 'Default',
  },
  {
    value: 'openai',
    labelKey: 'config_management.visual.payload_rules.provider_openai',
    defaultLabel: 'OpenAI',
  },
  {
    value: 'openai-response',
    labelKey: 'config_management.visual.payload_rules.provider_openai_response',
    defaultLabel: 'OpenAI Response',
  },
  {
    value: 'responses',
    labelKey: 'config_management.visual.payload_rules.provider_responses',
    defaultLabel: 'Responses',
  },
  {
    value: 'gemini',
    labelKey: 'config_management.visual.payload_rules.provider_gemini',
    defaultLabel: 'Gemini',
  },
  {
    value: 'interactions',
    labelKey: 'config_management.visual.payload_rules.provider_interactions',
    defaultLabel: 'Interactions',
  },
  {
    value: 'claude',
    labelKey: 'config_management.visual.payload_rules.provider_claude',
    defaultLabel: 'Claude',
  },
  {
    value: 'codex',
    labelKey: 'config_management.visual.payload_rules.provider_codex',
    defaultLabel: 'Codex',
  },
  {
    value: 'antigravity',
    labelKey: 'config_management.visual.payload_rules.provider_antigravity',
    defaultLabel: 'Antigravity',
  },
] as const;

export const VISUAL_CONFIG_PAYLOAD_VALUE_TYPE_OPTIONS = [
  {
    value: 'string',
    labelKey: 'config_management.visual.payload_rules.value_type_string',
    defaultLabel: 'String',
  },
  {
    value: 'number',
    labelKey: 'config_management.visual.payload_rules.value_type_number',
    defaultLabel: 'Number',
  },
  {
    value: 'boolean',
    labelKey: 'config_management.visual.payload_rules.value_type_boolean',
    defaultLabel: 'Boolean',
  },
  {
    value: 'json',
    labelKey: 'config_management.visual.payload_rules.value_type_json',
    defaultLabel: 'JSON',
  },
] as const satisfies ReadonlyArray<{
  value: PayloadParamValueType;
  labelKey: string;
  defaultLabel: string;
}>;
