import { isRecord } from '../utils/helpers';
import { configurationText as c } from '../i18n/configurationWorkspace';
import type { GatewayFamilyMeta } from './gatewayCatalog';
import { isWideInteger, parseGatewayJSON, stringifyGatewayJSON } from './gatewayRuleJSON';

export type RuleConditionKind = 'headers' | 'match' | 'not-match' | 'exist' | 'not-exist';
export type RuleValueType = 'string' | 'number' | 'boolean' | 'null' | 'array' | 'object';
export type RuleParamType = RuleValueType | 'raw';

export interface RuleConditionRow {
  kind: RuleConditionKind;
  path: string;
  type: RuleValueType;
  value: string;
}

export interface RuleModelForm {
  name: string;
  protocol: string;
  from: string;
  rows: RuleConditionRow[];
}

export interface RuleParamRow {
  path: string;
  type: RuleParamType;
  value: string;
}
export interface ReplaceEntryForm {
  search: string;
  replace: string;
}

export interface RuleForm {
  models: RuleModelForm[];
  params: RuleParamRow[];
  replaceTargetMode: 'all' | 'positions';
  replacePositions: string;
  replaceEntries: ReplaceEntryForm[];
}

export type RuleIssue =
  | 'modelName'
  | 'conditionPath'
  | 'duplicateCondition'
  | 'conditionValue'
  | 'headerValue'
  | 'paramPath'
  | 'duplicateParam'
  | 'paramValue'
  | 'rawValue'
  | 'emptyParams'
  | 'replacePositions'
  | 'replacePairs'
  | 'jsonInvalid'
  | 'jsonConditions'
  | 'jsonParams';
export type RuleResult = { ok: true; rule: unknown } | { ok: false; issue: RuleIssue; detail?: string };

export const CONDITION_KINDS: RuleConditionKind[] = ['headers', 'match', 'not-match', 'exist', 'not-exist'];
export const VALUE_TYPES: RuleValueType[] = ['string', 'number', 'boolean', 'null', 'array', 'object'];
export const PROTOCOLS = ['', 'openai', 'responses', 'gemini', 'claude', 'codex', 'antigravity'];

const INTEGER_LITERAL = /^-?(?:0|[1-9][0-9]*)$/;

function typedValue(type: RuleValueType, raw: string): unknown | { issue: RuleIssue } {
  switch (type) {
    case 'number': {
      const trimmed = raw.trim();
      if (trimmed === '') return { issue: 'conditionValue' };
      if (INTEGER_LITERAL.test(trimmed)) {
        return parseGatewayJSON(trimmed);
      }
      const num = Number(trimmed);
      if (!Number.isFinite(num)) return { issue: 'conditionValue' };
      return num;
    }
    case 'boolean':
      return raw === 'true';
    case 'null':
      return null;
    case 'array':
    case 'object': {
      try {
        const parsed: unknown = parseGatewayJSON(raw);
        if (
          type === 'array'
            ? !Array.isArray(parsed)
            : !isRecord(parsed) || isWideInteger(parsed)
        ) {
          return { issue: 'conditionValue' };
        }
        return parsed;
      } catch {
        return { issue: 'conditionValue' };
      }
    }
    default:
      return raw;
  }
}

function displayValue(value: unknown): string {
  if (typeof value === 'string') return value;
  if (value === null) return 'null';
  return stringifyGatewayJSON(value);
}

function typeOfValue(value: unknown): RuleValueType {
  if (value === null) return 'null';
  if (Array.isArray(value)) return 'array';
  if (typeof value === 'number' || isWideInteger(value)) return 'number';
  if (typeof value === 'boolean') return 'boolean';
  if (typeof value === 'object') return 'object';
  return 'string';
}

/** ruleToForm renders one CPA rule into the structured editor's state. */
export function ruleToForm(rule: unknown, family: GatewayFamilyMeta): RuleForm {
  const object = isRecord(rule) ? rule : {};
  const models = Array.isArray(object.models) ? object.models : [];
  const form: RuleForm = {
    models: models.map((model) => {
      const entry = isRecord(model) ? model : {};
      const rows: RuleConditionRow[] = [];
      if (isRecord(entry.headers)) {
        for (const [path, value] of Object.entries(entry.headers)) {
          rows.push({ kind: 'headers', path, type: 'string', value: displayValue(value) });
        }
      }
      for (const kind of ['match', 'not-match'] as const) {
        const values = entry[kind];
        if (!Array.isArray(values)) continue;
        for (const item of values) {
          if (!isRecord(item)) continue;
          for (const [path, value] of Object.entries(item)) {
            rows.push({ kind, path, type: typeOfValue(value), value: displayValue(value) });
          }
        }
      }
      for (const kind of ['exist', 'not-exist'] as const) {
        const values = entry[kind];
        if (!Array.isArray(values)) continue;
        for (const path of values) {
          rows.push({ kind, path: String(path), type: 'string', value: '' });
        }
      }
      return {
        name: typeof entry.name === 'string' ? entry.name : '',
        protocol: typeof entry.protocol === 'string' ? entry.protocol : '',
        from: typeof entry['from-protocol'] === 'string' ? entry['from-protocol'] : '',
        rows,
      };
    }),
    params: [],
    replaceTargetMode: 'all',
    replacePositions: '',
    replaceEntries: [{ search: '', replace: '' }],
  };
  if (family.replace) {
    const target = isRecord(object.target) ? object.target : {};
    const positions = Array.isArray(target.positions)
      ? target.positions.filter((n): n is number => typeof n === 'number' && n > 0)
      : [];
    form.replaceTargetMode = target.all === true || positions.length === 0 ? 'all' : 'positions';
    form.replacePositions = positions.join(', ');
    const rawRepl = Array.isArray(object.replacements) ? object.replacements : [];
    const pairs: ReplaceEntryForm[] = [];
    for (const item of rawRepl) {
      if (!isRecord(item)) continue;
      for (const [k, v] of Object.entries(item)) {
        pairs.push({ search: k, replace: typeof v === 'string' ? v : String(v ?? '') });
      }
    }
    form.replaceEntries = pairs.length ? pairs : [{ search: '', replace: '' }];
    if (!form.models.length) {
      form.models = [{ name: '', protocol: '', from: '', rows: [] }];
    }
    return form;
  }
  const params = object.params;
  if (family.filter && Array.isArray(params)) {
    form.params = params.map((path) => ({ path: String(path), type: 'string', value: '' }));
    return form;
  }
  if (isRecord(params)) {
    form.params = Object.entries(params).map(([path, value]) => ({
      path,
      type: family.raw ? 'raw' : typeOfValue(value),
      value: displayValue(value),
    }));
  }
  if (!form.models.length) {
    form.models = [{ name: '', protocol: '', from: '', rows: [] }];
  }
  return form;
}

/** formToRule serializes the structured editor back into a CPA rule. */
export function formToRule(form: RuleForm, family: GatewayFamilyMeta): RuleResult {
  const models: Record<string, unknown>[] = [];
  for (const model of form.models) {
    const name = model.name.trim();
    if (!name) return { ok: false, issue: 'modelName' };
    const conditions: Record<string, unknown> = {};
    const headerNames = new Set<string>();
    const paths = new Set<string>();
    for (const row of model.rows) {
      const path = row.path.trim();
      if (!path) return { ok: false, issue: 'conditionPath' };
      if (row.kind === 'headers') {
        const folded = path.toLowerCase();
        if (headerNames.has(folded)) return { ok: false, issue: 'duplicateCondition', detail: path };
        headerNames.add(folded);
        const headers = isRecord(conditions.headers) ? conditions.headers : {};
        headers[path] = row.value;
        conditions.headers = headers;
        continue;
      }
      if (row.kind === 'exist' || row.kind === 'not-exist') {
        const list = Array.isArray(conditions[row.kind]) ? (conditions[row.kind] as string[]) : [];
        if (list.includes(path)) return { ok: false, issue: 'duplicateCondition', detail: path };
        list.push(path);
        conditions[row.kind] = list;
        continue;
      }
      const list = Array.isArray(conditions[row.kind])
        ? (conditions[row.kind] as Record<string, unknown>[])
        : [];
      if (paths.has(`${row.kind}:${path}`)) {
        return { ok: false, issue: 'duplicateCondition', detail: path };
      }
      paths.add(`${row.kind}:${path}`);
      const value = typedValue(row.type, row.value);
      if (isRecord(value) && 'issue' in value) return { ok: false, issue: 'conditionValue' };
      list.push({ [path]: value });
      conditions[row.kind] = list;
    }
    if (conditions.headers && Object.values(conditions.headers as Record<string, unknown>).some((value) => typeof value !== 'string')) {
      return { ok: false, issue: 'headerValue' };
    }
    models.push({
      name,
      ...(model.protocol ? { protocol: model.protocol } : {}),
      ...(model.from ? { 'from-protocol': model.from } : {}),
      ...conditions,
    });
  }
  if (family.replace) {
    let target: Record<string, unknown>;
    if (form.replaceTargetMode === 'positions') {
      const tokens = form.replacePositions.split(',').map((item) => item.trim());
      const positions: number[] = [];
      for (const token of tokens) {
        if (!/^\d+$/.test(token)) return { ok: false, issue: 'replacePositions' };
        const num = Number(token);
        if (!Number.isSafeInteger(num) || num <= 0) return { ok: false, issue: 'replacePositions' };
        positions.push(num);
      }
      if (!positions.length) return { ok: false, issue: 'replacePositions' };
      target = { role: 'user', positions };
    } else {
      target = { role: 'user', all: true };
    }
    if (form.replaceEntries.some((item) => item.search === '' && item.replace !== '')) {
      return { ok: false, issue: 'replacePairs' };
    }
    const cleanEntries = form.replaceEntries.filter((item) => item.search.length > 0);
    if (!cleanEntries.length) return { ok: false, issue: 'replacePairs' };
    const replacements = cleanEntries.map((item) => ({ [item.search]: item.replace }));
    return { ok: true, rule: { models, target, replacements } };
  }
  if (family.filter) {
    const params: string[] = [];
    for (const row of form.params) {
      const path = row.path.trim();
      if (!path) return { ok: false, issue: 'paramPath' };
      if (params.includes(path)) return { ok: false, issue: 'duplicateParam', detail: path };
      params.push(path);
    }
    if (!params.length) return { ok: false, issue: 'emptyParams' };
    return { ok: true, rule: { models, params } };
  }
  const params: Record<string, unknown> = {};
  for (const row of form.params) {
    const path = row.path.trim();
    if (!path) return { ok: false, issue: 'paramPath' };
    if (Object.prototype.hasOwnProperty.call(params, path)) return { ok: false, issue: 'duplicateParam', detail: path };
    if (family.raw) {
      try {
        JSON.parse(row.value);
      } catch {
        return { ok: false, issue: 'rawValue', detail: path };
      }
      params[path] = row.value;
      continue;
    }
    const value = typedValue(row.type === 'raw' ? 'string' : row.type, row.value);
    if (isRecord(value) && 'issue' in value) return { ok: false, issue: 'paramValue', detail: path };
    params[path] = value;
  }
  if (!Object.keys(params).length) return { ok: false, issue: 'emptyParams' };
  return { ok: true, rule: { models, params } };
}

function isValidTrimmedPath(value: unknown): value is string {
  return typeof value === 'string' && value.trim() !== '' && value === value.trim();
}

function hasInvalidPathList(entries: unknown[]): boolean {
  const seen = new Set<string>();
  for (const entry of entries) {
    if (!isValidTrimmedPath(entry) || seen.has(entry)) return true;
    seen.add(entry);
  }
  return false;
}

const ALLOWED_MODEL_KEYS = new Set([
  'name',
  'protocol',
  'from-protocol',
  'headers',
  'match',
  'not-match',
  'exist',
  'not-exist',
]);

function isValidConfigModel(model: unknown): boolean {
  if (!isRecord(model)) return false;
  for (const key of Object.keys(model)) {
    if (!ALLOWED_MODEL_KEYS.has(key)) return false;
  }
  if (!isValidTrimmedPath(model.name)) return false;
  if ('protocol' in model && typeof model.protocol !== 'string') return false;
  if ('from-protocol' in model && typeof model['from-protocol'] !== 'string') return false;
  if ('headers' in model) {
    if (!isRecord(model.headers)) return false;
    const seen = new Set<string>();
    for (const [name, header] of Object.entries(model.headers)) {
      if (!isValidTrimmedPath(name) || typeof header !== 'string') return false;
      const folded = name.toLowerCase();
      if (seen.has(folded)) return false;
      seen.add(folded);
    }
  }
  for (const kind of ['match', 'not-match'] as const) {
    if (!(kind in model)) continue;
    const entries = model[kind];
    if (!Array.isArray(entries)) return false;
    const seen = new Set<string>();
    for (const item of entries) {
      if (!isRecord(item)) return false;
      const keys = Object.keys(item);
      if (keys.length !== 1) return false;
      const path = keys[0];
      if (!isValidTrimmedPath(path) || seen.has(path)) return false;
      seen.add(path);
    }
  }
  for (const kind of ['exist', 'not-exist'] as const) {
    if (!(kind in model)) continue;
    const entries = model[kind];
    if (!Array.isArray(entries) || hasInvalidPathList(entries)) return false;
  }
  return true;
}

/** parseRuleText validates one rule typed as JSON and keeps it verbatim. */
export function parseRuleText(source: string, family: GatewayFamilyMeta): RuleResult {
  let parsed: unknown;
  try {
    // A rule parameter can be a 64-bit integer; the plain parser would round it
    // and the page would PUT a different number than the user typed.
    parsed = parseGatewayJSON(source);
  } catch {
    return { ok: false, issue: 'jsonInvalid' };
  }
  if (!isRecord(parsed)) return { ok: false, issue: 'jsonInvalid' };
  if (family.replace) {
    for (const key of Object.keys(parsed)) {
      if (key !== 'models' && key !== 'target' && key !== 'replacements') {
        return { ok: false, issue: 'jsonConditions' };
      }
    }
    const models = parsed.models;
    if (!Array.isArray(models) || !models.length) return { ok: false, issue: 'jsonConditions' };
    for (const model of models) {
      if (!isValidConfigModel(model)) {
        return { ok: false, issue: 'jsonConditions' };
      }
    }
    if (!isRecord(parsed.target) || parsed.target.role !== 'user') {
      return { ok: false, issue: 'replacePositions' };
    }
    for (const key of Object.keys(parsed.target)) {
      if (key !== 'role' && key !== 'positions' && key !== 'all') {
        return { ok: false, issue: 'replacePositions' };
      }
    }
    const hasAll = Object.prototype.hasOwnProperty.call(parsed.target, 'all');
    const hasPos = Object.prototype.hasOwnProperty.call(parsed.target, 'positions');
    let allFlag = false;
    if (hasAll) {
      if (typeof parsed.target.all !== 'boolean') {
        return { ok: false, issue: 'replacePositions' };
      }
      allFlag = parsed.target.all;
    }
    let positionsLength = 0;
    if (hasPos) {
      if (
        !Array.isArray(parsed.target.positions) ||
        parsed.target.positions.some(
          (pos) => typeof pos !== 'number' || !Number.isInteger(pos) || pos <= 0
        )
      ) {
        return { ok: false, issue: 'replacePositions' };
      }
      positionsLength = parsed.target.positions.length;
    }
    if (allFlag ? positionsLength !== 0 : positionsLength === 0) {
      return { ok: false, issue: 'replacePositions' };
    }
    if (!Array.isArray(parsed.replacements) || !parsed.replacements.length) {
      return { ok: false, issue: 'replacePairs' };
    }
    for (const entry of parsed.replacements) {
      if (!isRecord(entry)) return { ok: false, issue: 'replacePairs' };
      const pairs = Object.entries(entry);
      if (pairs.length !== 1) return { ok: false, issue: 'replacePairs' };
      const [search, replace] = pairs[0];
      if (search === '' || typeof replace !== 'string') {
        return { ok: false, issue: 'replacePairs' };
      }
    }
    return { ok: true, rule: parsed };
  }
  for (const key of Object.keys(parsed)) {
    if (key !== 'models' && key !== 'params') return { ok: false, issue: 'jsonConditions' };
  }
  const models = parsed.models;
  if (!Array.isArray(models) || !models.length) return { ok: false, issue: 'jsonConditions' };
  for (const model of models) {
    if (!isValidConfigModel(model)) {
      return { ok: false, issue: 'jsonConditions' };
    }
  }
  if (family.filter) {
    if (!Array.isArray(parsed.params) || hasInvalidPathList(parsed.params)) {
      return { ok: false, issue: 'jsonParams' };
    }
    return { ok: true, rule: parsed };
  }
  if (!isRecord(parsed.params)) {
    return { ok: false, issue: 'jsonParams' };
  }
  for (const [path, value] of Object.entries(parsed.params)) {
    if (!isValidTrimmedPath(path)) return { ok: false, issue: 'jsonParams' };
    if (family.raw) {
      if (typeof value !== 'string') return { ok: false, issue: 'jsonParams' };
      try {
        JSON.parse(value);
      } catch {
        return { ok: false, issue: 'jsonParams' };
      }
    }
  }
  return { ok: true, rule: parsed };
}

export function ruleSummary(rule: unknown, family: GatewayFamilyMeta): string {
  const object = isRecord(rule) ? rule : {};
  const models = Array.isArray(object.models)
    ? object.models
        .map((model) => (isRecord(model) && typeof model.name === 'string' ? model.name : '?'))
        .join(', ')
    : '?';
  if (family.replace) {
    const target = isRecord(object.target) ? object.target : {};
    const scope = target.all === true
      ? c('ruleReplaceAll')
      : Array.isArray(target.positions)
        ? `#${target.positions.join(',')}`
        : 'user';
    const count = Array.isArray(object.replacements) ? object.replacements.length : 0;
    return `${models} · ${scope} (${count})`;
  }
  const params = family.filter
    ? Array.isArray(object.params)
      ? object.params.length
      : 0
    : isRecord(object.params)
      ? Object.keys(object.params).length
      : 0;
  return `${models} · ${c(family.filter ? 'rulePathCount' : 'ruleParamCount', params)}`;
}
