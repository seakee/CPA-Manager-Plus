import { useRef, useState } from 'react';
import { Modal } from '../components/ui/Modal';
import { configurationText as c } from '../i18n/configurationWorkspace';
import type { GatewayFamilyMeta } from './gatewayCatalog';
import {
  CONDITION_KINDS,
  PROTOCOLS,
  VALUE_TYPES,
  formToRule,
  parseRuleText,
  ruleSummary,
  ruleToForm,
  type RuleConditionKind,
  type RuleConditionRow,
  type RuleForm,
  type RuleIssue,
  type RuleParamType,
  type RuleValueType,
} from './gatewayRuleForm';
import { stringifyGatewayJSON } from './gatewayRuleJSON';

// One payload rule family editor. The structured form and the advanced JSON view
// are two representations of the same draft; the structured form is authoritative
// while it is open, and switching to JSON serializes it first so nothing is lost.

const ROW_TYPE_LABEL: Record<RuleValueType, Parameters<typeof c>[0]> = {
  string: 'typeString',
  number: 'typeNumber',
  boolean: 'typeBoolean',
  null: 'typeNull',
  array: 'typeArray',
  object: 'typeObject',
};

const KIND_LABEL: Record<RuleConditionKind, Parameters<typeof c>[0]> = {
  headers: 'conditionHeaders',
  match: 'conditionMatch',
  'not-match': 'conditionNotMatch',
  exist: 'conditionExist',
  'not-exist': 'conditionNotExist',
};

export function GatewayRuleEditor({
  family,
  rules,
  readOnly,
  onCancel,
  onApply,
}: {
  family: GatewayFamilyMeta;
  rules: unknown[];
  readOnly: boolean;
  onCancel: () => void;
  onApply: (rules: unknown[]) => void;
}) {
  const [list, setList] = useState<unknown[]>(() => rules.map((rule) => structuredClone(rule)));
  const [editing, setEditing] = useState<number | null>(null);
  const [form, setForm] = useState<RuleForm | null>(null);
  const [jsonText, setJsonText] = useState('');
  const [mode, setMode] = useState<'structured' | 'json'>('structured');
  const [issue, setIssue] = useState<string>('');
  const initialTemplateRef = useRef<{ formJson: string; json: string } | null>(null);

  const hint = c(family.hintKey);
  function startEdit(index: number | null) {
    const target = index === null ? emptyRule(family) : list[index];
    const nextForm = ruleToForm(target, family);
    const nextJson = stringifyGatewayJSON(target);
    initialTemplateRef.current =
      index === null ? { formJson: JSON.stringify(nextForm), json: nextJson } : null;
    setForm(nextForm);
    setJsonText(nextJson);
    setMode('structured');
    setIssue('');
    setEditing(index === null ? list.length : index);
  }

  function commitEdit() {
    if (editing === null || !form) return;
    const result = mode === 'json' ? parseRuleText(jsonText, family) : formToRule(form, family);
    if (!result.ok) {
      setIssue(c(RULE_ISSUE_TEXT[result.issue]));
      return;
    }
    setList((current) => {
      const next = [...current];
      next[editing] = result.rule;
      return next;
    });
    initialTemplateRef.current = null;
    setEditing(null);
    setForm(null);
    setIssue('');
  }

  function switchMode(next: 'structured' | 'json') {
    if (!form || next === mode) return;
    if (next === 'json') {
      const result = formToRule(form, family);
      if (!result.ok) {
        const untouchedNew =
          initialTemplateRef.current !== null &&
          JSON.stringify(form) === initialTemplateRef.current.formJson;
        if (!untouchedNew) {
          setIssue(c(RULE_ISSUE_TEXT[result.issue]));
          return;
        }
        setJsonText(initialTemplateRef.current!.json);
        setMode('json');
        setIssue('');
        return;
      }
      initialTemplateRef.current = null;
      setJsonText(stringifyGatewayJSON(result.rule));
      setMode('json');
      setIssue('');
      return;
    }
    const result = parseRuleText(jsonText, family);
    if (!result.ok) {
      const untouchedNew =
        initialTemplateRef.current !== null && jsonText === initialTemplateRef.current.json;
      if (!untouchedNew) {
        setIssue(c(RULE_ISSUE_TEXT[result.issue]));
        return;
      }
      setForm(ruleToForm(emptyRule(family), family));
      setMode('structured');
      setIssue('');
      return;
    }
    initialTemplateRef.current = null;
    setForm(ruleToForm(result.rule, family));
    setMode('structured');
    setIssue('');
  }

  function move(from: number, to: number) {
    setList((current) => {
      if (to < 0 || to >= current.length) return current;
      const next = [...current];
      const [item] = next.splice(from, 1);
      next.splice(to, 0, item);
      return next;
    });
  }

  function copy(index: number) {
    setList((current) => {
      const next = [...current];
      next.splice(index + 1, 0, structuredClone(current[index]));
      return next;
    });
  }

  const footer = editing === null ? (
    <>
      <button type="button" className="btn btn-secondary" data-testid="rule-close" onClick={onCancel}>
        {c('ruleCancel')}
      </button>
      <button
        type="button"
        className="btn btn-primary"
        data-testid="rule-save"
        disabled={readOnly}
        onClick={() => onApply(list)}
      >
        {c('ruleSaveDraft')}
      </button>
    </>
  ) : (
    <>
      <button
        type="button"
        className="btn btn-secondary"
        data-testid="rule-editor-cancel"
        onClick={() => {
          setEditing(null);
          setForm(null);
          setIssue('');
        }}
      >
        {c('ruleBackToList')}
      </button>
      <button type="button" className="btn btn-primary" data-testid="rule-editor-apply" onClick={commitEdit}>
        {c('ruleApplyToDraft')}
      </button>
    </>
  );

  const editorOpen = form !== null && editing !== null;

  return (
    <Modal
      open
      className="gateway-rule-dialog"
      width={editorOpen ? 880 : 760}
      title={c('rulesTitle') + ' · ' + c(family.labelKey)}
      onClose={onCancel}
      footer={footer}
    >
      <div className="rule-editor" data-testid="gateway-rule-editor">
        <p className="notice">{hint}</p>
        {readOnly && (
          <p className="notice warn" data-testid="rule-readonly">
            {c('rulesReadOnly')}
          </p>
        )}
        {issue && (
          <p className="field-error" role="alert" data-testid="rule-error">
            {issue}
          </p>
        )}
        {!editorOpen && (
          <div className="rule-list" data-testid="rule-list">
            {list.map((rule, index) => (
              <div className="rule-summary" key={index} data-testid={'rule-' + index}>
                <div className="rule-summary-main">
                  <span className="badge">{index + 1}</span>
                  <strong className="rule-summary-text">{ruleSummary(rule, family)}</strong>
                </div>
                <div className="actions">
                  <button
                    className="btn btn-secondary btn-sm"
                    data-testid={'rule-edit-' + index}
                    disabled={readOnly}
                    onClick={() => startEdit(index)}
                  >
                    {c('ruleEdit')}
                  </button>
                  <button
                    className="btn btn-secondary btn-sm"
                    data-testid={'rule-copy-' + index}
                    disabled={readOnly}
                    onClick={() => copy(index)}
                  >
                    {c('ruleCopy')}
                  </button>
                  <button
                    className="btn btn-secondary btn-sm"
                    data-testid={'rule-up-' + index}
                    disabled={readOnly || index === 0}
                    onClick={() => move(index, index - 1)}
                  >
                    {c('ruleMoveUp')}
                  </button>
                  <button
                    className="btn btn-secondary btn-sm"
                    data-testid={'rule-down-' + index}
                    disabled={readOnly || index === list.length - 1}
                    onClick={() => move(index, index + 1)}
                  >
                    {c('ruleMoveDown')}
                  </button>
                  <button
                    className="btn btn-secondary btn-sm"
                    data-testid={'rule-remove-' + index}
                    disabled={readOnly}
                    onClick={() => setList((current) => current.filter((_, at) => at !== index))}
                  >
                    {c('ruleRemove')}
                  </button>
                </div>
              </div>
            ))}
            {!list.length && (
              <p className="muted" data-testid="rule-empty">
                {c('ruleEmpty')}
              </p>
            )}
            <button
              className="btn btn-secondary"
              data-testid="rule-add"
              disabled={readOnly}
              onClick={() => startEdit(null)}
            >
              {c('ruleAdd')}
            </button>
          </div>
        )}
        {editorOpen && form && (
          <div className="rule-form">
            <div className="editor-modes" role="group" aria-label={c('ruleMode')}>
              <button
                className="btn btn-secondary btn-sm"
                aria-pressed={mode === 'structured'}
                data-testid="rule-mode-structured"
                onClick={() => switchMode('structured')}
              >
                {c('ruleStructured')}
              </button>
              <button
                className="btn btn-secondary btn-sm"
                aria-pressed={mode === 'json'}
                data-testid="rule-mode-json"
                onClick={() => switchMode('json')}
              >
                {c('ruleJson')}
              </button>
            </div>
            {mode === 'json' ? (
              <label className="dialog-label">
                {c('ruleJsonLabel')}
                <textarea
                  className="input rule-json-textarea"
                  data-testid="rule-json"
                  rows={16}
                  value={jsonText}
                  onChange={(event) => setJsonText(event.target.value)}
                />
              </label>
            ) : (
              <>
                <h3>{c('ruleModels')}</h3>
                {form.models.map((model, modelIndex) => (
                  <fieldset key={modelIndex} data-testid={'rule-model-' + modelIndex}>
                    <legend>{c('ruleModelLegend', modelIndex + 1)}</legend>
                    <div className="model-grid">
                      <label>
                        {c('ruleModelName')}
                        <input
                          className="input rule-mono-input"
                          data-testid={'rule-model-name-' + modelIndex}
                          value={model.name}
                          placeholder="gpt-*"
                          onChange={(event) =>
                            setForm((current) =>
                              current
                                ? {
                                    ...current,
                                    models: current.models.map((item, at) =>
                                      at === modelIndex ? { ...item, name: event.target.value } : item
                                    ),
                                  }
                                : current
                            )
                          }
                        />
                      </label>
                      <label>
                        {c('ruleProtocol')}
                        <select
                          className="input"
                          data-testid={'rule-model-protocol-' + modelIndex}
                          value={model.protocol}
                          onChange={(event) =>
                            setForm((current) =>
                              current
                                ? {
                                    ...current,
                                    models: current.models.map((item, at) =>
                                      at === modelIndex ? { ...item, protocol: event.target.value } : item
                                    ),
                                  }
                                : current
                            )
                          }
                        >
                          {PROTOCOLS.map((value) => (
                            <option key={value} value={value}>
                              {value || c('ruleAny')}
                            </option>
                          ))}
                        </select>
                      </label>
                      <label>
                        {c('ruleFromProtocol')}
                        <select
                          className="input"
                          data-testid={'rule-model-from-' + modelIndex}
                          value={model.from}
                          onChange={(event) =>
                            setForm((current) =>
                              current
                                ? {
                                    ...current,
                                    models: current.models.map((item, at) =>
                                      at === modelIndex ? { ...item, from: event.target.value } : item
                                    ),
                                  }
                                : current
                            )
                          }
                        >
                          {PROTOCOLS.map((value) => (
                            <option key={value} value={value}>
                              {value || c('ruleAny')}
                            </option>
                          ))}
                        </select>
                      </label>
                    </div>
                    <h3>{c('ruleConditions')}</h3>
                    <p className="detail">{c('ruleConditionsHelp')}</p>
                    {model.rows.map((row, rowIndex) => (
                      <div className="structured-row" key={rowIndex} data-condition-row={rowIndex}>
                        <label>
                          {c('ruleConditionKind')}
                          <select
                            className="input"
                            value={row.kind}
                            onChange={(event) =>
                              updateRow(setForm, modelIndex, rowIndex, {
                                kind: event.target.value as RuleConditionKind,
                                type: 'string',
                                value: '',
                              })
                            }
                          >
                            {CONDITION_KINDS.map((kind) => (
                              <option key={kind} value={kind}>
                                {c(KIND_LABEL[kind])}
                              </option>
                            ))}
                          </select>
                        </label>
                        <label>
                          {c('rulePath')}
                          <input
                            className="input rule-mono-input"
                            value={row.path}
                            placeholder={row.kind === 'headers' ? 'X-Client-Tier' : 'metadata.client'}
                            onChange={(event) =>
                              updateRow(setForm, modelIndex, rowIndex, { path: event.target.value })
                            }
                          />
                        </label>
                        {row.kind === 'exist' || row.kind === 'not-exist' ? null : (
                          <>
                            <label>
                              {c('ruleType')}
                              <select
                                className="input"
                                value={row.type}
                                onChange={(event) =>
                                  updateRow(setForm, modelIndex, rowIndex, {
                                    type: event.target.value as RuleValueType,
                                    value: event.target.value === 'boolean' ? 'true' : row.value,
                                  })
                                }
                              >
                                {VALUE_TYPES.map((type) => (
                                  <option key={type} value={type}>
                                    {c(ROW_TYPE_LABEL[type])}
                                  </option>
                                ))}
                              </select>
                            </label>
                            <label>
                              {c('ruleValue')}
                              <input
                                className="input"
                                value={row.value}
                                disabled={row.type === 'null'}
                                onChange={(event) =>
                                  updateRow(setForm, modelIndex, rowIndex, { value: event.target.value })
                                }
                              />
                            </label>
                          </>
                        )}
                        <button
                          className="btn btn-secondary btn-sm"
                          onClick={() =>
                            setForm((current) =>
                              current
                                ? {
                                    ...current,
                                    models: current.models.map((item, at) =>
                                      at === modelIndex
                                        ? { ...item, rows: item.rows.filter((_, index) => index !== rowIndex) }
                                        : item
                                    ),
                                  }
                                : current
                            )
                          }
                        >
                          {c('ruleRemove')}
                        </button>
                      </div>
                    ))}
                    <button
                      className="btn btn-secondary btn-sm"
                      data-testid={'rule-condition-add-' + modelIndex}
                      onClick={() =>
                        setForm((current) =>
                          current
                            ? {
                                ...current,
                                models: current.models.map((item, at) =>
                                  at === modelIndex
                                    ? {
                                        ...item,
                                        rows: [...item.rows, { kind: 'headers', path: '', type: 'string', value: '' }],
                                      }
                                    : item
                                ),
                              }
                            : current
                        )
                      }
                    >
                      {c('ruleAddCondition')}
                    </button>
                    <div className="actions">
                      <button
                        className="btn btn-secondary btn-sm"
                        disabled={form.models.length === 1}
                        onClick={() =>
                          setForm((current) =>
                            current
                              ? { ...current, models: current.models.filter((_, at) => at !== modelIndex) }
                              : current
                          )
                        }
                      >
                        {c('ruleRemoveModel')}
                      </button>
                    </div>
                  </fieldset>
                ))}
                <button
                  className="btn btn-secondary btn-sm"
                  data-testid="rule-model-add"
                  onClick={() =>
                    setForm((current) =>
                      current
                        ? {
                            ...current,
                            models: [...current.models, { name: '', protocol: '', from: '', rows: [] }],
                          }
                        : current
                    )
                  }
                >
                  {c('ruleAddModel')}
                </button>
                {family.replace ? (
                  <>
                    <h3>{c('ruleReplaceTarget')}</h3>
                    <div className="editor-modes" role="group" aria-label={c('ruleReplaceTarget')}>
                      <button
                        type="button"
                        className="btn btn-secondary btn-sm"
                        aria-pressed={form.replaceTargetMode === 'all'}
                        onClick={() =>
                          setForm((current) =>
                            current ? { ...current, replaceTargetMode: 'all' } : current
                          )
                        }
                      >
                        {c('ruleReplaceAll')}
                      </button>
                      <button
                        type="button"
                        className="btn btn-secondary btn-sm"
                        aria-pressed={form.replaceTargetMode === 'positions'}
                        onClick={() =>
                          setForm((current) =>
                            current ? { ...current, replaceTargetMode: 'positions' } : current
                          )
                        }
                      >
                        {c('ruleReplacePositions')}
                      </button>
                    </div>
                    {form.replaceTargetMode === 'positions' ? (
                      <label className="dialog-label">
                        {c('ruleReplacePositionsLabel')}
                        <input
                          className="input rule-mono-input"
                          data-testid="rule-replace-positions"
                          value={form.replacePositions}
                          placeholder="1, 2"
                          onChange={(event) =>
                            setForm((current) =>
                              current ? { ...current, replacePositions: event.target.value } : current
                            )
                          }
                        />
                      </label>
                    ) : null}
                    <h3>{c('ruleReplacePairs')}</h3>
                    <p className="detail">{c('ruleReplacePairsHelp')}</p>
                    {form.replaceEntries.map((entry, rowIndex) => (
                      <div className="structured-row replace-row" key={rowIndex}>
                        <label>
                          {c('ruleReplaceSearch')}
                          <input
                            className="input rule-mono-input"
                            data-testid={'rule-replace-search-' + rowIndex}
                            value={entry.search}
                            placeholder={c('ruleReplaceSearchPlaceholder')}
                            onChange={(event) =>
                              setForm((current) =>
                                current
                                  ? {
                                      ...current,
                                      replaceEntries: current.replaceEntries.map((item, at) =>
                                        at === rowIndex ? { ...item, search: event.target.value } : item
                                      ),
                                    }
                                  : current
                              )
                            }
                          />
                        </label>
                        <label>
                          {c('ruleReplaceWith')}
                          <input
                            className="input rule-mono-input"
                            data-testid={'rule-replace-with-' + rowIndex}
                            value={entry.replace}
                            placeholder={c('ruleReplaceWithPlaceholder')}
                            onChange={(event) =>
                              setForm((current) =>
                                current
                                  ? {
                                      ...current,
                                      replaceEntries: current.replaceEntries.map((item, at) =>
                                        at === rowIndex ? { ...item, replace: event.target.value } : item
                                      ),
                                    }
                                  : current
                              )
                            }
                          />
                        </label>
                        <button
                          type="button"
                          className="btn btn-secondary btn-sm"
                          disabled={form.replaceEntries.length <= 1}
                          onClick={() =>
                            setForm((current) =>
                              current
                                ? {
                                    ...current,
                                    replaceEntries: current.replaceEntries.filter((_, at) => at !== rowIndex),
                                  }
                                : current
                            )
                          }
                        >
                          {c('ruleRemove')}
                        </button>
                      </div>
                    ))}
                    <button
                      type="button"
                      className="btn btn-secondary btn-sm"
                      data-testid="rule-replace-add"
                      onClick={() =>
                        setForm((current) =>
                          current
                            ? {
                                ...current,
                                replaceEntries: [...current.replaceEntries, { search: '', replace: '' }],
                              }
                            : current
                        )
                      }
                    >
                      {c('ruleReplaceAddPair')}
                    </button>
                  </>
                ) : (
                  <>
                    <h3>{c(family.filter ? 'rulePaths' : 'ruleParams')}</h3>
                    {form.params.map((row, rowIndex) => (
                      <div
                        className={'structured-row' + (family.filter ? ' path-only' : '')}
                        key={rowIndex}
                        data-param-row={rowIndex}
                      >
                        <label>
                          {c('rulePath')}
                          <input
                            className="input rule-mono-input"
                            value={row.path}
                            placeholder={family.filter ? 'metadata.client' : 'reasoning.effort'}
                            onChange={(event) =>
                              setForm((current) =>
                                current
                                  ? {
                                      ...current,
                                      params: current.params.map((item, at) =>
                                        at === rowIndex ? { ...item, path: event.target.value } : item
                                      ),
                                    }
                                  : current
                              )
                            }
                          />
                        </label>
                        {family.filter ? null : (
                          <>
                            <label>
                              {c('ruleType')}
                              <select
                                className="input"
                                value={row.type}
                                disabled={family.raw}
                                onChange={(event) =>
                                  setForm((current) =>
                                    current
                                      ? {
                                          ...current,
                                          params: current.params.map((item, at) =>
                                            at === rowIndex
                                              ? {
                                                  ...item,
                                                  type: event.target.value as RuleParamType,
                                                  value:
                                                    event.target.value === 'boolean'
                                                      ? 'true'
                                                      : item.value,
                                                }
                                              : item
                                          ),
                                        }
                                      : current
                                  )
                                }
                              >
                                {(family.raw ? (['raw'] as RuleParamType[]) : (VALUE_TYPES as RuleParamType[])).map(
                                  (type) => (
                                    <option key={type} value={type}>
                                      {type === 'raw' ? c('typeRaw') : c(ROW_TYPE_LABEL[type as RuleValueType])}
                                    </option>
                                  )
                                )}
                              </select>
                            </label>
                            <label>
                              {c('ruleValue')}
                              <input
                                className="input"
                                value={row.value}
                                disabled={row.type === 'null'}
                                onChange={(event) =>
                                  setForm((current) =>
                                    current
                                      ? {
                                          ...current,
                                          params: current.params.map((item, at) =>
                                            at === rowIndex ? { ...item, value: event.target.value } : item
                                          ),
                                        }
                                      : current
                                  )
                                }
                              />
                            </label>
                          </>
                        )}
                        <button
                          type="button"
                          className="btn btn-secondary btn-sm"
                          onClick={() =>
                            setForm((current) =>
                              current
                                ? { ...current, params: current.params.filter((_, index) => index !== rowIndex) }
                                : current
                            )
                          }
                        >
                          {c('ruleRemove')}
                        </button>
                      </div>
                    ))}
                    <button
                      type="button"
                      className="btn btn-secondary btn-sm"
                      data-testid="rule-param-add"
                      onClick={() =>
                        setForm((current) =>
                          current
                            ? {
                                ...current,
                                params: [
                                  ...current.params,
                                  { path: '', type: family.raw ? 'raw' : 'string', value: '' },
                                ],
                              }
                            : current
                        )
                      }
                    >
                      {c(family.filter ? 'ruleAddPath' : 'ruleAddParam')}
                    </button>
                  </>
                )}
              </>
            )}
            <p className="detail">{c('ruleDraftNotice')}</p>
          </div>
        )}
      </div>
    </Modal>
  );
}

const RULE_ISSUE_TEXT: Record<RuleIssue, Parameters<typeof c>[0]> = {
  modelName: 'ruleIssueModelName',
  conditionPath: 'ruleIssueConditionPath',
  duplicateCondition: 'ruleIssueDuplicateCondition',
  conditionValue: 'ruleIssueConditionValue',
  headerValue: 'ruleIssueHeaderValue',
  paramPath: 'ruleIssueParamPath',
  duplicateParam: 'ruleIssueDuplicateParam',
  paramValue: 'ruleIssueParamValue',
  rawValue: 'ruleIssueRawValue',
  emptyParams: 'ruleIssueEmptyParams',
  replacePositions: 'issueReplacePositions',
  replacePairs: 'issueReplacePairs',
  jsonInvalid: 'ruleIssueJsonInvalid',
  jsonConditions: 'ruleIssueJsonConditions',
  jsonParams: 'ruleIssueJsonParams',
};

function emptyRule(family: GatewayFamilyMeta): unknown {
  if (family.replace) {
    return { models: [{ name: '*' }], target: { role: 'user', all: true }, replacements: [] };
  }
  return family.filter
    ? { models: [{ name: '*' }], params: [] }
    : { models: [{ name: '*' }], params: {} };
}

function updateRow(
  setForm: (updater: (current: RuleForm | null) => RuleForm | null) => void,
  modelIndex: number,
  rowIndex: number,
  patch: Partial<RuleConditionRow>
) {
  setForm((current) =>
    current
      ? {
          ...current,
          models: current.models.map((model, at) =>
            at === modelIndex
              ? {
                  ...model,
                  rows: model.rows.map((row, index) =>
                    index === rowIndex ? { ...row, ...patch } : row
                  ),
                }
              : model
          ),
        }
      : current
  );
}
