import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import { Input } from '@/components/ui/Input';
import { Select, type SelectOption } from '@/components/ui/Select';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import type { PluginConfigField } from '@/services/api/plugins';
import styles from './plugins.module.scss';

interface PluginConfigModalProps {
  open: boolean;
  pluginId: string;
  pluginName?: string;
  /** 当前完整原始配置（GET /plugins/:id/config） */
  config: Record<string, unknown>;
  /** 插件声明的配置字段（metadata.config_fields），用于动态生成表单 */
  fields?: PluginConfigField[];
  saving: boolean;
  error?: string;
  onClose: () => void;
  /** 整体替换保存（PUT） */
  onSave: (config: Record<string, unknown>) => void;
}

/** 服务端字符串字段经 HTML 转义（文档 §1.4），前端编辑需反转义 */
const unescapeHtml = (value: string): string => {
  if (typeof document === 'undefined') {
    return value
      .replace(/&amp;/g, '&')
      .replace(/&lt;/g, '<')
      .replace(/&gt;/g, '>')
      .replace(/&quot;/g, '"')
      .replace(/&#39;/g, "'");
  }
  const el = document.createElement('textarea');
  el.innerHTML = value;
  return el.value;
};

const coerceString = (value: unknown): string => {
  if (value === null || value === undefined) return '';
  if (typeof value === 'string') return unescapeHtml(value);
  try {
    return unescapeHtml(JSON.stringify(value));
  } catch {
    return String(value);
  }
};

const coerceNumber = (value: unknown): string => {
  if (value === null || value === undefined) return '';
  return String(value);
};

/** 将字段值转换为表单字符串/布尔状态 */
const toFieldValue = (
  raw: unknown,
  field: PluginConfigField
): string | boolean => {
  if (field.type === 'boolean') {
    return raw === true || raw === 'true';
  }
  if (field.type === 'number' || field.type === 'integer') {
    return coerceNumber(raw);
  }
  if (field.type === 'array' || field.type === 'object') {
    try {
      return raw === undefined || raw === null ? '' : JSON.stringify(raw, null, 2);
    } catch {
      return '';
    }
  }
  // string / enum
  return coerceString(raw);
};

/** 校验与解析单个字段输入，返回 [value, error?] */
const parseFieldValue = (
  text: string | boolean,
  field: PluginConfigField
): [unknown, string | undefined] => {
  switch (field.type) {
    case 'boolean':
      return [Boolean(text), undefined];
    case 'integer': {
      const trimmed = String(text).trim();
      if (trimmed === '') return [undefined, undefined];
      if (!/^-?\d+$/.test(trimmed)) {
        return [undefined, 'invalid_integer'];
      }
      return [Number(trimmed), undefined];
    }
    case 'number': {
      const trimmed = String(text).trim();
      if (trimmed === '') return [undefined, undefined];
      const num = Number(trimmed);
      if (Number.isNaN(num)) {
        return [undefined, 'invalid_number'];
      }
      return [num, undefined];
    }
    case 'array': {
      const trimmed = String(text).trim();
      if (trimmed === '') return [undefined, undefined];
      try {
        const parsed = JSON.parse(trimmed);
        if (!Array.isArray(parsed)) return [undefined, 'not_array'];
        return [parsed, undefined];
      } catch {
        return [undefined, 'invalid_json'];
      }
    }
    case 'object': {
      const trimmed = String(text).trim();
      if (trimmed === '') return [undefined, undefined];
      try {
        const parsed = JSON.parse(trimmed);
        if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
          return [undefined, 'not_object'];
        }
        return [parsed, undefined];
      } catch {
        return [undefined, 'invalid_json'];
      }
    }
    default:
      // string / enum
      return [String(text), undefined];
  }
};

export function PluginConfigModal({
  open,
  pluginId,
  pluginName,
  config,
  fields,
  saving,
  error,
  onClose,
  onSave
}: PluginConfigModalProps) {
  const { t } = useTranslation();
  const declaredFields = useMemo(() => fields ?? [], [fields]);
  const declaredNames = useMemo(
    () => new Set(declaredFields.map((f) => f.name)),
    [declaredFields]
  );

  const [fieldValues, setFieldValues] = useState<Record<string, string | boolean>>({});
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [showRaw, setShowRaw] = useState(false);
  const [rawText, setRawText] = useState('');
  const [rawError, setRawError] = useState('');

  // 自定义字段：原始配置中未被声明的字段
  const customConfig = useMemo(() => {
    const result: Record<string, unknown> = {};
    for (const [key, value] of Object.entries(config)) {
      if (!declaredNames.has(key)) {
        result[key] = value;
      }
    }
    return result;
  }, [config, declaredNames]);

  // 每次打开或配置变化时，将外部 config 同步到本地表单状态。
  // 这里属于「将外部状态（props）同步到本地」的合法用途。
  useEffect(() => {
    if (!open) return;
    const nextValues: Record<string, string | boolean> = {};
    for (const field of declaredFields) {
      nextValues[field.name] = toFieldValue(config[field.name], field);
    }
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setFieldValues(nextValues);
    setFieldErrors({});
    try {
      setRawText(JSON.stringify(customConfig, null, 2));
    } catch {
      setRawText('');
    }
    setRawError('');
    setShowRaw(declaredFields.length === 0);
  }, [open, config, declaredFields, customConfig]);

  const handleFieldChange = (name: string, value: string | boolean) => {
    setFieldValues((prev) => ({ ...prev, [name]: value }));
    setFieldErrors((prev) => {
      if (!prev[name]) return prev;
      const next = { ...prev };
      delete next[name];
      return next;
    });
  };

  const handleSave = () => {
    // 1. 校验并收集声明字段的值
    const declaredValues: Record<string, unknown> = {};
    const nextFieldErrors: Record<string, string> = {};
    for (const field of declaredFields) {
      const raw = fieldValues[field.name];
      const empty = raw === undefined || raw === '';
      if (field.required && empty && field.type !== 'boolean') {
        nextFieldErrors[field.name] = 'required';
        continue;
      }
      const [parsed, parseError] = parseFieldValue(raw ?? '', field);
      if (parseError) {
        nextFieldErrors[field.name] = parseError;
        continue;
      }
      if (parsed !== undefined) {
        declaredValues[field.name] = parsed;
      }
    }

    // 2. 解析原始 JSON（自定义字段）
    let customValues: Record<string, unknown> = {};
    if (showRaw || declaredFields.length === 0) {
      const trimmed = rawText.trim();
      if (trimmed === '') {
        customValues = {};
      } else {
        try {
          const parsed = JSON.parse(trimmed);
          if (parsed === null) {
            customValues = {};
          } else if (typeof parsed === 'object' && !Array.isArray(parsed)) {
            customValues = parsed as Record<string, unknown>;
          } else {
            setRawError(t('plugins.config.json_not_object'));
            return;
          }
        } catch (err) {
          setRawError(
            `${t('plugins.config.json_invalid')}${
              err instanceof Error ? `: ${err.message}` : ''
            }`
          );
          return;
        }
      }
    } else {
      customValues = customConfig;
    }

    setFieldErrors(nextFieldErrors);
    if (Object.keys(nextFieldErrors).length > 0) {
      return;
    }
    setRawError('');

    onSave({ ...customValues, ...declaredValues });
  };

  return (
    <Modal
      open={open}
      title={
        pluginName
          ? t('plugins.config.title_with_name', { name: pluginName, id: pluginId })
          : t('plugins.config.title', { id: pluginId })
      }
      onClose={onClose}
      width={620}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={saving}>
            {t('common.cancel')}
          </Button>
          <Button onClick={handleSave} loading={saving}>
            {t('common.save')}
          </Button>
        </>
      }
    >
      <div className={styles.formField} style={{ gap: 12 }}>
        {declaredFields.length > 0 ? (
          declaredFields.map((field) => {
            const value = fieldValues[field.name];
            const fieldErr = fieldErrors[field.name];
            const fieldErrorText = fieldErr
              ? t(`plugins.config.error_${fieldErr}`, {
                  defaultValue: fieldErr
                })
              : undefined;
            const label = (
              <div className={styles.fieldLabel}>
                <span>{field.name}</span>
                <span className={styles.fieldType}>{field.type}</span>
                {field.required && (
                  <span className={styles.fieldType}>{t('plugins.config.required')}</span>
                )}
              </div>
            );
            return (
              <div key={field.name} className={styles.formField}>
                {field.type === 'boolean' ? (
                  <div className={styles.enabledRow}>
                    <div className={styles.enabledLabel}>
                      {label}
                      {field.description && (
                        <span className={styles.fieldHint}>{field.description}</span>
                      )}
                    </div>
                    <ToggleSwitch
                      checked={Boolean(value)}
                      onChange={(checked) => handleFieldChange(field.name, checked)}
                      ariaLabel={field.name}
                    />
                  </div>
                ) : field.type === 'enum' ? (
                  <>
                    {label}
                    <Select
                      value={coerceString(value)}
                      options={(field.enum_values ?? []).map<SelectOption>((v) => ({
                        value: v,
                        label: v
                      }))}
                      onChange={(v) => handleFieldChange(field.name, v)}
                      placeholder={t('plugins.config.enum_placeholder')}
                      ariaLabel={field.name}
                    />
                    {field.description && (
                      <span className={styles.fieldHint}>{field.description}</span>
                    )}
                    {fieldErrorText && <div className={styles.jsonError}>{fieldErrorText}</div>}
                  </>
                ) : field.type === 'array' || field.type === 'object' ? (
                  <>
                    <label className={styles.fieldLabel}>{field.name}</label>
                    <textarea
                      className={styles.jsonTextarea}
                      value={coerceString(value)}
                      onChange={(e) => handleFieldChange(field.name, e.target.value)}
                      placeholder={
                        field.type === 'array' ? '["a","b"]' : '{"key":"value"}'
                      }
                      aria-label={field.name}
                    />
                    {field.description && (
                      <span className={styles.fieldHint}>{field.description}</span>
                    )}
                    {fieldErrorText && <div className={styles.jsonError}>{fieldErrorText}</div>}
                  </>
                ) : (
                  <Input
                    label={field.name}
                    type={field.type === 'integer' || field.type === 'number' ? 'number' : 'text'}
                    value={coerceString(value)}
                    onChange={(e) => handleFieldChange(field.name, e.target.value)}
                    hint={field.description}
                    error={fieldErrorText}
                  />
                )}
              </div>
            );
          })
        ) : (
          <div className={styles.fieldHint}>{t('plugins.config.no_declared_fields')}</div>
        )}

        {(showRaw || declaredFields.length === 0 || Object.keys(customConfig).length > 0) && (
          <div className={styles.jsonSection}>
            {declaredFields.length > 0 && (
              <button
                type="button"
                className={styles.jsonToggle}
                onClick={() => setShowRaw((prev) => !prev)}
              >
                {showRaw ? '▾' : '▸'} {t('plugins.config.custom_fields')}
              </button>
            )}
            {(showRaw || declaredFields.length === 0) && (
              <>
                <label className={styles.fieldLabel}>
                  {t('plugins.config.raw_json')}
                </label>
                <textarea
                  className={styles.jsonTextarea}
                  value={rawText}
                  onChange={(e) => {
                    setRawText(e.target.value);
                    if (rawError) setRawError('');
                  }}
                  spellCheck={false}
                  aria-label={t('plugins.config.raw_json')}
                />
                <span className={styles.fieldHint}>{t('plugins.config.raw_hint')}</span>
                {rawError && <div className={styles.jsonError}>{rawError}</div>}
              </>
            )}
          </div>
        )}

        {error && <div className={styles.jsonError}>{error}</div>}
      </div>
    </Modal>
  );
}
