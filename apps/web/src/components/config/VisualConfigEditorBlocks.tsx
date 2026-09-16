import {
  memo,
  useCallback,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Select } from '@/components/ui/Select';
import styles from './VisualConfigEditor.module.scss';
import type {
  PayloadFilterRule,
  PayloadHeaderEntry,
  PayloadModelEntry,
  PayloadParamEntry,
  PayloadParamValidationErrorCode,
  PayloadParamValueType,
  PayloadRule,
  PluginStoreAuthApplyTo,
  PluginStoreAuthRule,
  PluginStoreAuthType,
} from '@/types/visualConfig';
import { makeClientId } from '@/types/visualConfig';
import {
  getPayloadParamValidationError,
  VISUAL_CONFIG_PAYLOAD_VALUE_TYPE_OPTIONS,
  VISUAL_CONFIG_PROTOCOL_OPTIONS,
} from '@/hooks/useVisualConfig';

export { ApiKeysCardEditor } from './ApiKeysCardEditor';

/** Minimum character count before the expand/collapse toggle appears. */
const EXPAND_THRESHOLD = 30;

/** Auto-expanding textarea that collapses back to a single-line input on demand. */
function ExpandableInput({
  value,
  placeholder,
  ariaLabel,
  disabled,
  className,
  onChange,
}: {
  value: string;
  placeholder?: string;
  ariaLabel?: string;
  disabled?: boolean;
  className?: string;
  onChange: (nextValue: string) => void;
}) {
  const { t } = useTranslation();
  const [collapsed, setCollapsed] = useState(true);
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  const autoResize = useCallback((el: HTMLTextAreaElement) => {
    el.style.height = 'auto';
    el.style.height = `${el.scrollHeight}px`;
  }, []);

  const handleChange = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    // Strip newlines — these fields are single-line identifiers/paths that
    // would break YAML serialization if they contained line breaks.
    const sanitized = e.target.value.replace(/[\r\n]/g, '');
    onChange(sanitized);
    // autoResize is handled by useLayoutEffect after React syncs the
    // sanitized value back to the DOM — calling it here would measure
    // stale content.
  };

  // Resize synchronously before paint to avoid visual flicker.
  useLayoutEffect(() => {
    if (!collapsed && textareaRef.current) {
      autoResize(textareaRef.current);
    }
  }, [collapsed, value, autoResize]);

  if (collapsed) {
    return (
      <div className={styles.expandableInputWrapper}>
        <input
          className={`input ${className ?? ''}`}
          placeholder={placeholder}
          aria-label={ariaLabel}
          value={value}
          onChange={(e) => onChange(e.target.value.replace(/[\r\n]/g, ''))}
          disabled={disabled}
        />
        {value.length > EXPAND_THRESHOLD && (
          <button
            type="button"
            className={styles.expandableToggle}
            disabled={disabled}
            onClick={() => {
              setCollapsed(false);
              requestAnimationFrame(() => {
                textareaRef.current?.focus();
              });
            }}
            title={t('common.expand')}
            aria-label={t('common.expand')}
          >
            ▼
          </button>
        )}
      </div>
    );
  }

  return (
    <div className={`${styles.expandableInputWrapper} ${styles.expandableInputExpanded}`}>
      <textarea
        ref={textareaRef}
        className={`input ${styles.expandableTextarea} ${className ?? ''}`}
        placeholder={placeholder}
        aria-label={ariaLabel}
        value={value}
        onChange={handleChange}
        disabled={disabled}
        rows={2}
      />
      <button
        type="button"
        className={styles.expandableToggle}
        disabled={disabled}
        onClick={() => setCollapsed(true)}
        title={t('common.collapse')}
        aria-label={t('common.collapse')}
      >
        ▲
      </button>
    </div>
  );
}

function getValidationMessage(
  t: ReturnType<typeof useTranslation>['t'],
  errorCode?: PayloadParamValidationErrorCode
) {
  if (!errorCode) return undefined;
  return t(`config_management.visual.validation.${errorCode}`);
}

function buildProtocolOptions(
  t: ReturnType<typeof useTranslation>['t'],
  rules: Array<{ models: PayloadModelEntry[] }>
) {
  const options: Array<{ value: string; label: string }> = VISUAL_CONFIG_PROTOCOL_OPTIONS.map(
    (option) => ({
      value: option.value,
      label: t(option.labelKey, { defaultValue: option.defaultLabel }),
    })
  );
  const seen = new Set<string>(options.map((option) => option.value));

  for (const rule of rules) {
    for (const model of rule.models) {
      [model.protocol, model.fromProtocol].forEach((protocol) => {
        if (!protocol || !protocol.trim() || seen.has(protocol)) return;
        seen.add(protocol);
        options.push({ value: protocol, label: protocol });
      });
    }
  }

  return options;
}

export const StringListEditor = memo(function StringListEditor({
  value,
  disabled,
  placeholder,
  inputAriaLabel,
  onChange,
}: {
  value: string[];
  disabled?: boolean;
  placeholder?: string;
  inputAriaLabel?: string;
  onChange: (next: string[]) => void;
}) {
  const { t } = useTranslation();
  const items = value.length ? value : [];
  const [itemIds, setItemIds] = useState(() => items.map(() => makeClientId()));
  const renderItemIds = useMemo(() => {
    if (itemIds.length === items.length) return itemIds;
    if (itemIds.length > items.length) return itemIds.slice(0, items.length);
    return [
      ...itemIds,
      ...Array.from({ length: items.length - itemIds.length }, () => makeClientId()),
    ];
  }, [itemIds, items.length]);

  const updateItem = (index: number, nextValue: string) =>
    onChange(items.map((item, i) => (i === index ? nextValue : item)));
  const addItem = () => {
    setItemIds([...renderItemIds, makeClientId()]);
    onChange([...items, '']);
  };
  const removeItem = (index: number) => {
    setItemIds(renderItemIds.filter((_, i) => i !== index));
    onChange(items.filter((_, i) => i !== index));
  };

  return (
    <div className={styles.stringList}>
      {items.map((item, index) => (
        <div key={renderItemIds[index] ?? `item-${index}`} className={styles.stringListRow}>
          <ExpandableInput
            placeholder={placeholder}
            ariaLabel={inputAriaLabel ?? placeholder}
            value={item}
            onChange={(nextValue) => updateItem(index, nextValue)}
            disabled={disabled}
          />
          <Button variant="ghost" size="xs" onClick={() => removeItem(index)} disabled={disabled}>
            {t('config_management.visual.common.delete')}
          </Button>
        </div>
      ))}
      <div className={styles.actionRow}>
        <Button variant="secondary" size="xs" onClick={addItem} disabled={disabled}>
          {t('config_management.visual.common.add')}
        </Button>
      </div>
    </div>
  );
});

const PLUGIN_STORE_AUTH_TYPE_OPTIONS: Array<{ value: PluginStoreAuthType; labelKey: string }> = [
  { value: 'bearer', labelKey: 'config_management.visual.sections.system.store_auth_type_bearer' },
  {
    value: 'github-token',
    labelKey: 'config_management.visual.sections.system.store_auth_type_github_token',
  },
  { value: 'basic', labelKey: 'config_management.visual.sections.system.store_auth_type_basic' },
  { value: 'header', labelKey: 'config_management.visual.sections.system.store_auth_type_header' },
  { value: 'none', labelKey: 'config_management.visual.sections.system.store_auth_type_none' },
];

const PLUGIN_STORE_AUTH_APPLY_TO_OPTIONS: Array<{
  value: PluginStoreAuthApplyTo;
  labelKey: string;
}> = [
  {
    value: 'registry',
    labelKey: 'config_management.visual.sections.system.store_auth_apply_registry',
  },
  {
    value: 'metadata',
    labelKey: 'config_management.visual.sections.system.store_auth_apply_metadata',
  },
  {
    value: 'artifact',
    labelKey: 'config_management.visual.sections.system.store_auth_apply_artifact',
  },
];

const createPluginStoreAuthRule = (): PluginStoreAuthRule => ({
  id: makeClientId(),
  match: '',
  applyTo: [],
  type: 'bearer',
  tokenEnv: '',
  usernameEnv: '',
  passwordEnv: '',
  headerName: '',
  headerValueEnv: '',
  allowInsecure: false,
});

export const PluginStoreAuthEditor = memo(function PluginStoreAuthEditor({
  value,
  disabled,
  onChange,
}: {
  value: PluginStoreAuthRule[];
  disabled?: boolean;
  onChange: (next: PluginStoreAuthRule[]) => void;
}) {
  const { t } = useTranslation();

  const updateRule = (id: string, patch: Partial<PluginStoreAuthRule>) => {
    onChange(value.map((rule) => (rule.id === id ? { ...rule, ...patch } : rule)));
  };
  const addRule = () => onChange([...value, createPluginStoreAuthRule()]);
  const removeRule = (id: string) => onChange(value.filter((rule) => rule.id !== id));
  const toggleApplyTo = (rule: PluginStoreAuthRule, kind: PluginStoreAuthApplyTo) => {
    const nextApplyTo = rule.applyTo.includes(kind)
      ? rule.applyTo.filter((item) => item !== kind)
      : [...rule.applyTo, kind];
    updateRule(rule.id, { applyTo: nextApplyTo });
  };

  return (
    <div className={styles.storeAuthEditor}>
      {value.length === 0 ? (
        <div className={styles.storeAuthEmpty}>
          <div className={styles.storeAuthEmptyCopy}>
            <strong>{t('config_management.visual.sections.system.store_auth_empty')}</strong>
            <span>{t('config_management.visual.sections.system.plugin_store_auth_hint')}</span>
          </div>
          <Button variant="secondary" size="xs" onClick={addRule} disabled={disabled}>
            {t('config_management.visual.sections.system.store_auth_add')}
          </Button>
        </div>
      ) : null}
      {value.map((rule) => {
        const usesToken = rule.type === 'bearer' || rule.type === 'github-token';
        const usesBasic = rule.type === 'basic';
        const usesHeader = rule.type === 'header';
        return (
          <div key={rule.id} className={styles.storeAuthRule}>
            <div className={styles.storeAuthRuleHeader}>
              <strong>{rule.match || t('config_management.visual.sections.system.store_auth_rule')}</strong>
              <Button
                variant="ghost"
                size="xs"
                onClick={() => removeRule(rule.id)}
                disabled={disabled}
              >
                {t('config_management.visual.common.delete')}
              </Button>
            </div>

            <div className={styles.storeAuthGrid}>
              <label className={styles.storeAuthField}>
                <span>{t('config_management.visual.sections.system.store_auth_match')}</span>
                <ExpandableInput
                  value={rule.match}
                  placeholder="https://api.github.com/repos/owner/repo/releases/"
                  disabled={disabled}
                  onChange={(match) => updateRule(rule.id, { match })}
                />
              </label>
              <label className={styles.storeAuthField}>
                <span>{t('config_management.visual.sections.system.store_auth_type')}</span>
                <Select
                  value={rule.type}
                  options={PLUGIN_STORE_AUTH_TYPE_OPTIONS.map((option) => ({
                    value: option.value,
                    label: t(option.labelKey),
                  }))}
                  disabled={disabled}
                  onChange={(type) => updateRule(rule.id, { type: type as PluginStoreAuthType })}
                />
              </label>
            </div>

            <div className={styles.storeAuthApplyTo}>
              <span>{t('config_management.visual.sections.system.store_auth_apply_to')}</span>
              <div className={styles.storeAuthCheckboxes}>
                {PLUGIN_STORE_AUTH_APPLY_TO_OPTIONS.map((option) => (
                  <label key={option.value} className={styles.storeAuthCheckbox}>
                    <input
                      type="checkbox"
                      checked={rule.applyTo.includes(option.value)}
                      disabled={disabled}
                      onChange={() => toggleApplyTo(rule, option.value)}
                    />
                    <span>{t(option.labelKey)}</span>
                  </label>
                ))}
              </div>
              <small>{t('config_management.visual.sections.system.store_auth_apply_to_hint')}</small>
            </div>

            {usesToken ? (
              <label className={styles.storeAuthField}>
                <span>{t('config_management.visual.sections.system.store_auth_token_env')}</span>
                <input
                  className="input"
                  value={rule.tokenEnv}
                  placeholder="CLIPROXY_PLUGIN_STORE_TOKEN"
                  disabled={disabled}
                  onChange={(event) => updateRule(rule.id, { tokenEnv: event.target.value })}
                />
              </label>
            ) : null}

            {usesBasic ? (
              <div className={styles.storeAuthGrid}>
                <label className={styles.storeAuthField}>
                  <span>
                    {t('config_management.visual.sections.system.store_auth_username_env')}
                  </span>
                  <input
                    className="input"
                    value={rule.usernameEnv}
                    disabled={disabled}
                    onChange={(event) => updateRule(rule.id, { usernameEnv: event.target.value })}
                  />
                </label>
                <label className={styles.storeAuthField}>
                  <span>
                    {t('config_management.visual.sections.system.store_auth_password_env')}
                  </span>
                  <input
                    className="input"
                    value={rule.passwordEnv}
                    disabled={disabled}
                    onChange={(event) => updateRule(rule.id, { passwordEnv: event.target.value })}
                  />
                </label>
              </div>
            ) : null}

            {usesHeader ? (
              <div className={styles.storeAuthGrid}>
                <label className={styles.storeAuthField}>
                  <span>{t('config_management.visual.sections.system.store_auth_header_name')}</span>
                  <input
                    className="input"
                    value={rule.headerName}
                    placeholder="X-Plugin-Token"
                    disabled={disabled}
                    onChange={(event) => updateRule(rule.id, { headerName: event.target.value })}
                  />
                </label>
                <label className={styles.storeAuthField}>
                  <span>
                    {t('config_management.visual.sections.system.store_auth_header_value_env')}
                  </span>
                  <input
                    className="input"
                    value={rule.headerValueEnv}
                    disabled={disabled}
                    onChange={(event) =>
                      updateRule(rule.id, { headerValueEnv: event.target.value })
                    }
                  />
                </label>
              </div>
            ) : null}

            <label className={styles.storeAuthCheckbox}>
              <input
                type="checkbox"
                checked={rule.allowInsecure}
                disabled={disabled}
                onChange={(event) => updateRule(rule.id, { allowInsecure: event.target.checked })}
              />
              <span>{t('config_management.visual.sections.system.store_auth_allow_insecure')}</span>
            </label>
          </div>
        );
      })}
      {value.length > 0 ? (
        <div className={styles.actionRow}>
          <Button variant="secondary" size="xs" onClick={addRule} disabled={disabled}>
            {t('config_management.visual.sections.system.store_auth_add')}
          </Button>
        </div>
      ) : null}
    </div>
  );
});

const PayloadHeadersEditor = memo(function PayloadHeadersEditor({
  value,
  disabled,
  onChange,
}: {
  value: PayloadHeaderEntry[];
  disabled?: boolean;
  onChange: (next: PayloadHeaderEntry[]) => void;
}) {
  const { t } = useTranslation();
  const headers = value ?? [];

  const addHeader = () => onChange([...headers, { id: makeClientId(), name: '', value: '' }]);
  const removeHeader = (index: number) => onChange(headers.filter((_, i) => i !== index));
  const updateHeader = (index: number, patch: Partial<PayloadHeaderEntry>) =>
    onChange(headers.map((header, i) => (i === index ? { ...header, ...patch } : header)));

  return (
    <div className={styles.payloadAdvancedList}>
      {headers.map((header, index) => (
        <div key={header.id} className={styles.payloadHeaderRow}>
          <ExpandableInput
            placeholder={t('config_management.visual.payload_rules.header_name')}
            ariaLabel={t('config_management.visual.payload_rules.header_name')}
            value={header.name}
            onChange={(name) => updateHeader(index, { name })}
            disabled={disabled}
          />
          <ExpandableInput
            placeholder={t('config_management.visual.payload_rules.header_value')}
            ariaLabel={t('config_management.visual.payload_rules.header_value')}
            value={header.value}
            onChange={(nextValue) => updateHeader(index, { value: nextValue })}
            disabled={disabled}
          />
          <Button variant="ghost" size="xs" onClick={() => removeHeader(index)} disabled={disabled}>
            {t('config_management.visual.common.delete')}
          </Button>
        </div>
      ))}
      <div className={styles.actionRow}>
        <Button variant="secondary" size="xs" onClick={addHeader} disabled={disabled}>
          {t('config_management.visual.payload_rules.add_header')}
        </Button>
      </div>
    </div>
  );
});

const PayloadConditionsEditor = memo(function PayloadConditionsEditor({
  value,
  disabled,
  labelKey,
  onChange,
}: {
  value: PayloadParamEntry[];
  disabled?: boolean;
  labelKey: string;
  onChange: (next: PayloadParamEntry[]) => void;
}) {
  const { t } = useTranslation();
  const conditions = value ?? [];
  const payloadValueTypeOptions = useMemo(
    () =>
      VISUAL_CONFIG_PAYLOAD_VALUE_TYPE_OPTIONS.map((option) => ({
        value: option.value,
        label: t(option.labelKey, { defaultValue: option.defaultLabel }),
      })),
    [t]
  );
  const booleanValueOptions = useMemo(
    () => [
      { value: 'true', label: t('config_management.visual.payload_rules.boolean_true') },
      { value: 'false', label: t('config_management.visual.payload_rules.boolean_false') },
    ],
    [t]
  );

  const addCondition = () =>
    onChange([...conditions, { id: makeClientId(), path: '', valueType: 'string', value: '' }]);
  const removeCondition = (index: number) => onChange(conditions.filter((_, i) => i !== index));
  const updateCondition = (index: number, patch: Partial<PayloadParamEntry>) =>
    onChange(
      conditions.map((condition, i) => (i === index ? { ...condition, ...patch } : condition))
    );

  const getValuePlaceholder = (valueType: PayloadParamValueType) => {
    switch (valueType) {
      case 'string':
        return t('config_management.visual.payload_rules.value_string');
      case 'number':
        return t('config_management.visual.payload_rules.value_number');
      case 'boolean':
        return t('config_management.visual.payload_rules.value_boolean');
      case 'json':
        return t('config_management.visual.payload_rules.value_json');
      default:
        return t('config_management.visual.payload_rules.value_default');
    }
  };

  const renderValueEditor = (condition: PayloadParamEntry, index: number) => {
    if (condition.valueType === 'boolean') {
      return (
        <Select
          value={
            condition.value.toLowerCase() === 'true' ||
            condition.value.toLowerCase() === 'false'
              ? condition.value.toLowerCase()
              : ''
          }
          options={booleanValueOptions}
          placeholder={t('config_management.visual.payload_rules.value_boolean')}
          disabled={disabled}
          ariaLabel={t('config_management.visual.payload_rules.condition_value')}
          onChange={(nextValue) => updateCondition(index, { value: nextValue })}
        />
      );
    }

    if (condition.valueType === 'json') {
      return (
        <textarea
          className={`input ${styles.payloadJsonInput}`}
          placeholder={getValuePlaceholder(condition.valueType)}
          aria-label={t('config_management.visual.payload_rules.condition_value')}
          value={condition.value}
          onChange={(event) => updateCondition(index, { value: event.target.value })}
          disabled={disabled}
        />
      );
    }

    return (
      <ExpandableInput
        placeholder={getValuePlaceholder(condition.valueType)}
        ariaLabel={t('config_management.visual.payload_rules.condition_value')}
        value={condition.value}
        onChange={(nextValue) => updateCondition(index, { value: nextValue })}
        disabled={disabled}
      />
    );
  };

  return (
    <div className={styles.payloadAdvancedList}>
      <div className={styles.blockLabel}>{t(labelKey)}</div>
      {conditions.map((condition, index) => {
        const paramError = getValidationMessage(t, getPayloadParamValidationError(condition));

        return (
          <div key={condition.id} className={styles.payloadRuleParamGroup}>
            <div className={styles.payloadRuleParamRow}>
              <ExpandableInput
                placeholder={t('config_management.visual.payload_rules.condition_path')}
                ariaLabel={t('config_management.visual.payload_rules.condition_path')}
                value={condition.path}
                onChange={(path) => updateCondition(index, { path })}
                disabled={disabled}
              />
              <Select
                value={condition.valueType}
                options={payloadValueTypeOptions}
                disabled={disabled}
                ariaLabel={t('config_management.visual.payload_rules.param_type')}
                onChange={(nextValue) =>
                  updateCondition(index, {
                    valueType: nextValue as PayloadParamValueType,
                    value:
                      nextValue === 'boolean'
                        ? 'true'
                        : nextValue === 'json' && condition.value.trim() === ''
                          ? '{}'
                          : condition.value,
                  })
                }
              />
              {renderValueEditor(condition, index)}
              <Button
                variant="ghost"
                size="xs"
                className={styles.payloadRowActionButton}
                onClick={() => removeCondition(index)}
                disabled={disabled}
              >
                {t('config_management.visual.common.delete')}
              </Button>
            </div>
            {paramError && (
              <div className={`error-box ${styles.payloadParamError}`}>{paramError}</div>
            )}
          </div>
        );
      })}
      <div className={styles.actionRow}>
        <Button variant="secondary" size="xs" onClick={addCondition} disabled={disabled}>
          {t('config_management.visual.payload_rules.add_condition')}
        </Button>
      </div>
    </div>
  );
});

const hasModelAdvancedFields = (model: PayloadModelEntry) =>
  Boolean(
    model.fromProtocol ||
      (model.headers && model.headers.length > 0) ||
      (model.match && model.match.length > 0) ||
      (model.notMatch && model.notMatch.length > 0) ||
      (model.exist && model.exist.length > 0) ||
      (model.notExist && model.notExist.length > 0)
  );

export const PayloadRulesEditor = memo(function PayloadRulesEditor({
  value,
  disabled,
  protocolFirst = false,
  rawJsonValues = false,
  onChange,
}: {
  value: PayloadRule[];
  disabled?: boolean;
  protocolFirst?: boolean;
  rawJsonValues?: boolean;
  onChange: (next: PayloadRule[]) => void;
}) {
  const { t } = useTranslation();
  const rules = value;
  const [manualExpandedModelIds, setManualExpandedModelIds] = useState<Set<string>>(
    () => new Set()
  );
  const [collapsedAdvancedModelIds, setCollapsedAdvancedModelIds] = useState<Set<string>>(
    () => new Set()
  );
  const protocolOptions = useMemo(() => buildProtocolOptions(t, rules), [rules, t]);
  const payloadValueTypeOptions = useMemo(
    () =>
      VISUAL_CONFIG_PAYLOAD_VALUE_TYPE_OPTIONS.map((option) => ({
        value: option.value,
        label: t(option.labelKey, { defaultValue: option.defaultLabel }),
      })),
    [t]
  );
  const booleanValueOptions = useMemo(
    () => [
      { value: 'true', label: t('config_management.visual.payload_rules.boolean_true') },
      { value: 'false', label: t('config_management.visual.payload_rules.boolean_false') },
    ],
    [t]
  );

  const addRule = () => onChange([...rules, { id: makeClientId(), models: [], params: [] }]);
  const removeRule = (ruleIndex: number) => onChange(rules.filter((_, i) => i !== ruleIndex));

  const updateRule = (ruleIndex: number, patch: Partial<PayloadRule>) =>
    onChange(rules.map((rule, i) => (i === ruleIndex ? { ...rule, ...patch } : rule)));

  const addModel = (ruleIndex: number) => {
    const rule = rules[ruleIndex];
    const nextModel: PayloadModelEntry = { id: makeClientId(), name: '', protocol: undefined };
    updateRule(ruleIndex, { models: [...rule.models, nextModel] });
  };

  const removeModel = (ruleIndex: number, modelIndex: number) => {
    const rule = rules[ruleIndex];
    updateRule(ruleIndex, { models: rule.models.filter((_, i) => i !== modelIndex) });
  };

  const updateModel = (
    ruleIndex: number,
    modelIndex: number,
    patch: Partial<PayloadModelEntry>
  ) => {
    const rule = rules[ruleIndex];
    updateRule(ruleIndex, {
      models: rule.models.map((m, i) => (i === modelIndex ? { ...m, ...patch } : m)),
    });
  };

  const toggleModelAdvanced = (model: PayloadModelEntry) => {
    if (hasModelAdvancedFields(model)) {
      setCollapsedAdvancedModelIds((current) => {
        const next = new Set(current);
        if (next.has(model.id)) {
          next.delete(model.id);
        } else {
          next.add(model.id);
        }
        return next;
      });
      return;
    }

    setManualExpandedModelIds((current) => {
      const next = new Set(current);
      if (next.has(model.id)) {
        next.delete(model.id);
      } else {
        next.add(model.id);
      }
      return next;
    });
  };

  const addParam = (ruleIndex: number) => {
    const rule = rules[ruleIndex];
    const nextParam: PayloadParamEntry = {
      id: makeClientId(),
      path: '',
      valueType: rawJsonValues ? 'json' : 'string',
      value: '',
    };
    updateRule(ruleIndex, { params: [...rule.params, nextParam] });
  };

  const removeParam = (ruleIndex: number, paramIndex: number) => {
    const rule = rules[ruleIndex];
    updateRule(ruleIndex, { params: rule.params.filter((_, i) => i !== paramIndex) });
  };

  const updateParam = (
    ruleIndex: number,
    paramIndex: number,
    patch: Partial<PayloadParamEntry>
  ) => {
    const rule = rules[ruleIndex];
    updateRule(ruleIndex, {
      params: rule.params.map((p, i) => (i === paramIndex ? { ...p, ...patch } : p)),
    });
  };

  const getValuePlaceholder = (valueType: PayloadParamValueType) => {
    switch (valueType) {
      case 'string':
        return t('config_management.visual.payload_rules.value_string');
      case 'number':
        return t('config_management.visual.payload_rules.value_number');
      case 'boolean':
        return t('config_management.visual.payload_rules.value_boolean');
      case 'json':
        return t('config_management.visual.payload_rules.value_json');
      default:
        return t('config_management.visual.payload_rules.value_default');
    }
  };

  const getParamErrorMessage = (param: PayloadParamEntry) => {
    const errorCode = getPayloadParamValidationError(
      rawJsonValues ? { ...param, valueType: 'json' } : param
    );
    return getValidationMessage(t, errorCode);
  };

  const renderParamValueEditor = (
    ruleIndex: number,
    paramIndex: number,
    param: PayloadParamEntry
  ) => {
    if (rawJsonValues) {
      return (
        <textarea
          className={`input ${styles.payloadJsonInput}`}
          placeholder={t('config_management.visual.payload_rules.value_raw_json')}
          aria-label={t('config_management.visual.payload_rules.param_value')}
          value={param.value}
          onChange={(e) =>
            updateParam(ruleIndex, paramIndex, { value: e.target.value, valueType: 'json' })
          }
          disabled={disabled}
        />
      );
    }

    if (param.valueType === 'boolean') {
      return (
        <Select
          value={
            param.value.toLowerCase() === 'true' || param.value.toLowerCase() === 'false'
              ? param.value.toLowerCase()
              : ''
          }
          options={booleanValueOptions}
          placeholder={t('config_management.visual.payload_rules.value_boolean')}
          disabled={disabled}
          ariaLabel={t('config_management.visual.payload_rules.param_value')}
          onChange={(nextValue) => updateParam(ruleIndex, paramIndex, { value: nextValue })}
        />
      );
    }

    if (param.valueType === 'json') {
      return (
        <textarea
          className={`input ${styles.payloadJsonInput}`}
          placeholder={getValuePlaceholder(param.valueType)}
          aria-label={t('config_management.visual.payload_rules.param_value')}
          value={param.value}
          onChange={(e) => updateParam(ruleIndex, paramIndex, { value: e.target.value })}
          disabled={disabled}
        />
      );
    }

    return (
      <ExpandableInput
        placeholder={getValuePlaceholder(param.valueType)}
        ariaLabel={t('config_management.visual.payload_rules.param_value')}
        value={param.value}
        onChange={(nextValue) => updateParam(ruleIndex, paramIndex, { value: nextValue })}
        disabled={disabled}
      />
    );
  };

  return (
    <div className={styles.blockStack}>
      {rules.map((rule, ruleIndex) => (
        <div key={rule.id} className={styles.ruleCard}>
          <div className={styles.ruleCardHeader}>
            <div className={styles.ruleCardTitle}>
              {t('config_management.visual.payload_rules.rule')} {ruleIndex + 1}
            </div>
            <Button
              variant="ghost"
              size="xs"
              onClick={() => removeRule(ruleIndex)}
              disabled={disabled}
            >
              {t('config_management.visual.common.delete')}
            </Button>
          </div>

          <div className={styles.blockStack}>
            <div className={styles.blockLabel}>
              {t('config_management.visual.payload_rules.models')}
            </div>
            {(rule.models.length ? rule.models : []).map((model, modelIndex) => {
              const modelHasAdvanced = hasModelAdvancedFields(model);
              const advancedExpanded =
                (modelHasAdvanced && !collapsedAdvancedModelIds.has(model.id)) ||
                manualExpandedModelIds.has(model.id);

              return (
                <div key={model.id} className={styles.payloadModelBlock}>
                  <div
                    className={[
                      styles.payloadRuleModelRow,
                      protocolFirst ? styles.payloadRuleModelRowProtocolFirst : '',
                    ]
                      .filter(Boolean)
                      .join(' ')}
                  >
                    {protocolFirst ? (
                      <>
                        <Select
                          value={model.protocol ?? ''}
                          options={protocolOptions}
                          disabled={disabled}
                          ariaLabel={t('config_management.visual.payload_rules.provider_type')}
                          onChange={(nextValue) =>
                            updateModel(ruleIndex, modelIndex, {
                              protocol: (nextValue || undefined) as PayloadModelEntry['protocol'],
                            })
                          }
                        />
                        <ExpandableInput
                          placeholder={t('config_management.visual.payload_rules.model_name')}
                          ariaLabel={t('config_management.visual.payload_rules.model_name')}
                          value={model.name}
                          onChange={(nextValue) =>
                            updateModel(ruleIndex, modelIndex, { name: nextValue })
                          }
                          disabled={disabled}
                        />
                      </>
                    ) : (
                      <>
                        <ExpandableInput
                          placeholder={t('config_management.visual.payload_rules.model_name')}
                          ariaLabel={t('config_management.visual.payload_rules.model_name')}
                          value={model.name}
                          onChange={(nextValue) =>
                            updateModel(ruleIndex, modelIndex, { name: nextValue })
                          }
                          disabled={disabled}
                        />
                        <Select
                          value={model.protocol ?? ''}
                          options={protocolOptions}
                          disabled={disabled}
                          ariaLabel={t('config_management.visual.payload_rules.provider_type')}
                          onChange={(nextValue) =>
                            updateModel(ruleIndex, modelIndex, {
                              protocol: (nextValue || undefined) as PayloadModelEntry['protocol'],
                            })
                          }
                        />
                      </>
                    )}
                    <Button
                      variant="ghost"
                      size="xs"
                      className={styles.payloadRowActionButton}
                      onClick={() => toggleModelAdvanced(model)}
                      disabled={disabled}
                    >
                      {advancedExpanded
                        ? t('config_management.visual.payload_rules.hide_advanced')
                        : t('config_management.visual.payload_rules.advanced')}
                    </Button>
                    <Button
                      variant="ghost"
                      size="xs"
                      className={styles.payloadRowActionButton}
                      onClick={() => removeModel(ruleIndex, modelIndex)}
                      disabled={disabled}
                    >
                      {t('config_management.visual.common.delete')}
                    </Button>
                  </div>

                  {advancedExpanded ? (
                    <div className={styles.payloadModelAdvanced}>
                      <div className={styles.payloadAdvancedGrid}>
                        <div className={styles.payloadAdvancedField}>
                          <div className={styles.blockLabel}>
                            {t('config_management.visual.payload_rules.from_protocol')}
                          </div>
                          <Select
                            value={model.fromProtocol ?? ''}
                            options={protocolOptions}
                            disabled={disabled}
                            ariaLabel={t('config_management.visual.payload_rules.from_protocol')}
                            onChange={(nextValue) =>
                              updateModel(ruleIndex, modelIndex, {
                                fromProtocol: (nextValue ||
                                  undefined) as PayloadModelEntry['fromProtocol'],
                              })
                            }
                          />
                        </div>
                      </div>
                      <div className={styles.payloadAdvancedField}>
                        <div className={styles.blockLabel}>
                          {t('config_management.visual.payload_rules.headers')}
                        </div>
                        <PayloadHeadersEditor
                          value={model.headers ?? []}
                          disabled={disabled}
                          onChange={(headers) => updateModel(ruleIndex, modelIndex, { headers })}
                        />
                      </div>
                      <PayloadConditionsEditor
                        value={model.match ?? []}
                        disabled={disabled}
                        labelKey="config_management.visual.payload_rules.match"
                        onChange={(match) => updateModel(ruleIndex, modelIndex, { match })}
                      />
                      <PayloadConditionsEditor
                        value={model.notMatch ?? []}
                        disabled={disabled}
                        labelKey="config_management.visual.payload_rules.notMatch"
                        onChange={(notMatch) => updateModel(ruleIndex, modelIndex, { notMatch })}
                      />
                      <div className={styles.payloadAdvancedGrid}>
                        <div className={styles.payloadAdvancedField}>
                          <div className={styles.blockLabel}>
                            {t('config_management.visual.payload_rules.exist')}
                          </div>
                          <StringListEditor
                            value={model.exist ?? []}
                            disabled={disabled}
                            placeholder={t('config_management.visual.payload_rules.condition_path')}
                            inputAriaLabel={t(
                              'config_management.visual.payload_rules.condition_path'
                            )}
                            onChange={(exist) => updateModel(ruleIndex, modelIndex, { exist })}
                          />
                        </div>
                        <div className={styles.payloadAdvancedField}>
                          <div className={styles.blockLabel}>
                            {t('config_management.visual.payload_rules.notExist')}
                          </div>
                          <StringListEditor
                            value={model.notExist ?? []}
                            disabled={disabled}
                            placeholder={t('config_management.visual.payload_rules.condition_path')}
                            inputAriaLabel={t(
                              'config_management.visual.payload_rules.condition_path'
                            )}
                            onChange={(notExist) =>
                              updateModel(ruleIndex, modelIndex, { notExist })
                            }
                          />
                        </div>
                      </div>
                    </div>
                  ) : null}
                </div>
              );
            })}
            <div className={styles.actionRow}>
              <Button
                variant="secondary"
                size="xs"
                onClick={() => addModel(ruleIndex)}
                disabled={disabled}
              >
                {t('config_management.visual.payload_rules.add_model')}
              </Button>
            </div>
          </div>

          <div className={styles.blockStack}>
            <div className={styles.blockLabel}>
              {t('config_management.visual.payload_rules.params')}
            </div>
            {(rule.params.length ? rule.params : []).map((param, paramIndex) => {
              const paramError = getParamErrorMessage(param);

              return (
                <div key={param.id} className={styles.payloadRuleParamGroup}>
                  <div
                    className={[
                      styles.payloadRuleParamRow,
                      rawJsonValues ? styles.payloadRuleRawParamRow : '',
                    ]
                      .filter(Boolean)
                      .join(' ')}
                  >
                    <ExpandableInput
                      placeholder={t('config_management.visual.payload_rules.json_path')}
                      ariaLabel={t('config_management.visual.payload_rules.json_path')}
                      value={param.path}
                      onChange={(nextValue) =>
                        updateParam(ruleIndex, paramIndex, { path: nextValue })
                      }
                      disabled={disabled}
                    />
                    {rawJsonValues ? null : (
                      <Select
                        value={param.valueType}
                        options={payloadValueTypeOptions}
                        disabled={disabled}
                        ariaLabel={t('config_management.visual.payload_rules.param_type')}
                        onChange={(nextValue) =>
                          updateParam(ruleIndex, paramIndex, {
                            valueType: nextValue as PayloadParamValueType,
                            value:
                              nextValue === 'boolean'
                                ? 'true'
                                : nextValue === 'json' && param.value.trim() === ''
                                  ? '{}'
                                  : param.value,
                          })
                        }
                      />
                    )}
                    {renderParamValueEditor(ruleIndex, paramIndex, param)}
                    <Button
                      variant="ghost"
                      size="xs"
                      className={styles.payloadRowActionButton}
                      onClick={() => removeParam(ruleIndex, paramIndex)}
                      disabled={disabled}
                    >
                      {t('config_management.visual.common.delete')}
                    </Button>
                  </div>
                  {paramError && (
                    <div className={`error-box ${styles.payloadParamError}`}>{paramError}</div>
                  )}
                </div>
              );
            })}
            <div className={styles.actionRow}>
              <Button
                variant="secondary"
                size="xs"
                onClick={() => addParam(ruleIndex)}
                disabled={disabled}
              >
                {t('config_management.visual.payload_rules.add_param')}
              </Button>
            </div>
          </div>
        </div>
      ))}

      {rules.length === 0 && (
        <div className={styles.emptyState}>
          {t('config_management.visual.payload_rules.no_rules')}
        </div>
      )}

      <div className={styles.actionRow}>
        <Button variant="secondary" size="xs" onClick={addRule} disabled={disabled}>
          {t('config_management.visual.payload_rules.add_rule')}
        </Button>
      </div>
    </div>
  );
});

export const PayloadFilterRulesEditor = memo(function PayloadFilterRulesEditor({
  value,
  disabled,
  onChange,
}: {
  value: PayloadFilterRule[];
  disabled?: boolean;
  onChange: (next: PayloadFilterRule[]) => void;
}) {
  const { t } = useTranslation();
  const rules = value;
  const protocolOptions = useMemo(() => buildProtocolOptions(t, rules), [rules, t]);

  const addRule = () => onChange([...rules, { id: makeClientId(), models: [], params: [] }]);
  const removeRule = (ruleIndex: number) => onChange(rules.filter((_, i) => i !== ruleIndex));

  const updateRule = (ruleIndex: number, patch: Partial<PayloadFilterRule>) =>
    onChange(rules.map((rule, i) => (i === ruleIndex ? { ...rule, ...patch } : rule)));

  const addModel = (ruleIndex: number) => {
    const rule = rules[ruleIndex];
    const nextModel: PayloadModelEntry = { id: makeClientId(), name: '', protocol: undefined };
    updateRule(ruleIndex, { models: [...rule.models, nextModel] });
  };

  const removeModel = (ruleIndex: number, modelIndex: number) => {
    const rule = rules[ruleIndex];
    updateRule(ruleIndex, { models: rule.models.filter((_, i) => i !== modelIndex) });
  };

  const updateModel = (
    ruleIndex: number,
    modelIndex: number,
    patch: Partial<PayloadModelEntry>
  ) => {
    const rule = rules[ruleIndex];
    updateRule(ruleIndex, {
      models: rule.models.map((m, i) => (i === modelIndex ? { ...m, ...patch } : m)),
    });
  };

  return (
    <div className={styles.blockStack}>
      {rules.map((rule, ruleIndex) => (
        <div key={rule.id} className={styles.ruleCard}>
          <div className={styles.ruleCardHeader}>
            <div className={styles.ruleCardTitle}>
              {t('config_management.visual.payload_rules.rule')} {ruleIndex + 1}
            </div>
            <Button
              variant="ghost"
              size="xs"
              onClick={() => removeRule(ruleIndex)}
              disabled={disabled}
            >
              {t('config_management.visual.common.delete')}
            </Button>
          </div>

          <div className={styles.blockStack}>
            <div className={styles.blockLabel}>
              {t('config_management.visual.payload_rules.models')}
            </div>
            {rule.models.map((model, modelIndex) => (
              <div key={model.id} className={styles.payloadFilterModelRow}>
                <ExpandableInput
                  placeholder={t('config_management.visual.payload_rules.model_name')}
                  ariaLabel={t('config_management.visual.payload_rules.model_name')}
                  value={model.name}
                  onChange={(nextValue) => updateModel(ruleIndex, modelIndex, { name: nextValue })}
                  disabled={disabled}
                />
                <Select
                  value={model.protocol ?? ''}
                  options={protocolOptions}
                  disabled={disabled}
                  ariaLabel={t('config_management.visual.payload_rules.provider_type')}
                  onChange={(nextValue) =>
                    updateModel(ruleIndex, modelIndex, {
                      protocol: (nextValue || undefined) as PayloadModelEntry['protocol'],
                    })
                  }
                />
                <Button
                  variant="ghost"
                  size="xs"
                  className={styles.payloadRowActionButton}
                  onClick={() => removeModel(ruleIndex, modelIndex)}
                  disabled={disabled}
                >
                  {t('config_management.visual.common.delete')}
                </Button>
              </div>
            ))}
            <div className={styles.actionRow}>
              <Button
                variant="secondary"
                size="xs"
                onClick={() => addModel(ruleIndex)}
                disabled={disabled}
              >
                {t('config_management.visual.payload_rules.add_model')}
              </Button>
            </div>
          </div>

          <div className={styles.blockStack}>
            <div className={styles.blockLabel}>
              {t('config_management.visual.payload_rules.remove_params')}
            </div>
            <StringListEditor
              value={rule.params}
              disabled={disabled}
              placeholder={t('config_management.visual.payload_rules.json_path_filter')}
              inputAriaLabel={t('config_management.visual.payload_rules.json_path_filter')}
              onChange={(params) => updateRule(ruleIndex, { params })}
            />
          </div>
        </div>
      ))}

      {rules.length === 0 && (
        <div className={styles.emptyState}>
          {t('config_management.visual.payload_rules.no_rules')}
        </div>
      )}

      <div className={styles.actionRow}>
        <Button variant="secondary" size="xs" onClick={addRule} disabled={disabled}>
          {t('config_management.visual.payload_rules.add_rule')}
        </Button>
      </div>
    </div>
  );
});
