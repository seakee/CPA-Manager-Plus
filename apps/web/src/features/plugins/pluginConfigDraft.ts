import type { PluginConfigField, PluginConfigObject } from '@/types';
import { isRecord } from '@/utils/helpers';

export type PluginConfigDraftValue = string | boolean;

export interface PluginConfigDraftState {
  enabled: boolean;
  priority: string;
  values: Record<string, PluginConfigDraftValue>;
  touched: Set<string>;
  errors: Record<string, string>;
}

const fieldType = (field: PluginConfigField) => field.type.trim().toLowerCase();

const draftValue = (field: PluginConfigField, value: unknown): PluginConfigDraftValue => {
  const type = fieldType(field);
  if (type === 'boolean') return value === true;
  if (value === undefined || value === null) return '';
  if (type === 'array' || type === 'object') return JSON.stringify(value, null, 2);
  return String(value);
};

export const createPluginConfigDraft = (
  fields: PluginConfigField[],
  config: PluginConfigObject,
  enabledFallback: boolean
): PluginConfigDraftState => ({
  enabled: typeof config.enabled === 'boolean' ? config.enabled : enabledFallback,
  priority:
    typeof config.priority === 'number' || typeof config.priority === 'string'
      ? String(config.priority)
      : '0',
  values: Object.fromEntries(fields.map((field) => [field.name, draftValue(field, config[field.name])])),
  touched: new Set<string>(),
  errors: {},
});

type Translate = (key: string, options?: Record<string, unknown>) => string;

export const buildPluginConfigPatch = (
  draft: PluginConfigDraftState,
  fields: PluginConfigField[],
  t: Translate
) => {
  const patch: PluginConfigObject = {};
  const errors: Record<string, string> = {};

  if (draft.touched.has('enabled')) patch.enabled = draft.enabled;
  if (draft.touched.has('priority')) {
    const text = draft.priority.trim();
    if (!text) patch.priority = 0;
    else if (!/^-?\d+$/.test(text)) errors.priority = t('plugin_management.invalid_priority');
    else patch.priority = Number.parseInt(text, 10);
  }

  fields.forEach((field) => {
    if (!draft.touched.has(field.name)) return;
    const type = fieldType(field);
    const value = draft.values[field.name];
    if (type === 'boolean') {
      patch[field.name] = value === true;
      return;
    }
    const text = typeof value === 'string' ? value.trim() : '';
    if (!text) {
      patch[field.name] = null;
      return;
    }
    if (type === 'array' || type === 'object') {
      try {
        const parsed: unknown = JSON.parse(text);
        if (type === 'array' && !Array.isArray(parsed)) {
          errors[field.name] = t('plugin_management.expected_array');
        } else if (type === 'object' && !isRecord(parsed)) {
          errors[field.name] = t('plugin_management.expected_object');
        } else {
          patch[field.name] = parsed;
        }
      } catch {
        errors[field.name] = t('plugin_management.invalid_json');
      }
      return;
    }
    if (type === 'enum' && field.enumValues.length && !field.enumValues.includes(text)) {
      errors[field.name] = t('plugin_management.invalid_enum');
    } else if (type === 'number') {
      const parsed = Number(text);
      if (!Number.isFinite(parsed)) errors[field.name] = t('plugin_management.invalid_number');
      else patch[field.name] = parsed;
    } else if (type === 'integer') {
      if (!/^-?\d+$/.test(text)) errors[field.name] = t('plugin_management.invalid_integer');
      else patch[field.name] = Number.parseInt(text, 10);
    } else {
      patch[field.name] = text;
    }
  });

  return { patch, errors };
};
