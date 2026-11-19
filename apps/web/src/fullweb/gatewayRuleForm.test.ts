import { createElement, type ReactNode } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import { isRecord } from '../utils/helpers';
import { configurationText as c } from '../i18n/configurationWorkspace';
import { GatewayRuleEditor } from './GatewayRuleEditor';
import {
  formToRule,
  parseRuleText,
  ruleSummary,
  ruleToForm,
} from './gatewayRuleForm';
import { gatewayFamilies } from './gatewayCatalog';
import { parseGatewayJSON, stringifyGatewayJSON } from './gatewayRuleJSON';

vi.mock('../components/ui/Modal', () => ({
  Modal: ({
    children,
    footer,
    open,
  }: {
    children: ReactNode;
    footer: ReactNode;
    open: boolean;
  }) => (open ? createElement('div', { role: 'dialog' }, children, footer) : null),
}));

const families = Object.fromEntries(gatewayFamilies().map((family) => [family.family, family]));

describe('gateway rule form', () => {
  it('round-trips a standard rule without losing a condition or a parameter', () => {
    const rule = {
      models: [
        {
          name: 'gpt-*',
          protocol: 'codex',
          'from-protocol': 'responses',
          headers: { 'X-Tier': 'gold' },
          match: [{ 'metadata.client': 'x' }, { 'metadata.n': 2 }],
          'not-match': [{ 'metadata.beta': true }],
          exist: ['metadata.tags'],
          'not-exist': ['tools'],
        },
      ],
      params: { 'reasoning.effort': 'high', temperature: 0.2 },
    };
    const form = ruleToForm(rule, families.default);
    const result = formToRule(form, families.default);
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    // The structured form is lossless for everything the CPAMP editor accepts.
    expect(result.rule).toEqual(rule);
  });

  it('keeps raw family values as JSON strings and rejects a non-JSON value', () => {
    const form = ruleToForm(
      { models: [{ name: 'gpt-*' }], params: { response_format: '{"type":"json_object"}' } },
      families['default-raw']
    );
    const result = formToRule(form, families['default-raw']);
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(isRecord(result.rule) && isRecord(result.rule.params)).toBe(true);
    if (isRecord(result.rule) && isRecord(result.rule.params)) {
      expect(result.rule.params.response_format).toBe('{"type":"json_object"}');
    }

    form.params[0].value = '{';
    const broken = formToRule(form, families['default-raw']);
    expect(broken.ok).toBe(false);
    if (!broken.ok) expect(broken.issue).toBe('rawValue');
  });

  it('rejects a duplicated rule path', () => {
    const filter = ruleToForm(
      { models: [{ name: '*' }], params: ['metadata.client'] },
      families.filter
    );
    filter.params.push({ path: 'metadata.client', type: 'string', value: '' });
    const duplicated = formToRule(filter, families.filter);
    expect(duplicated.ok).toBe(false);
    if (!duplicated.ok) expect(duplicated.issue).toBe('duplicateParam');

    const standard = ruleToForm({ models: [{ name: '*' }], params: {} }, families.default);
    standard.models[0].rows.push(
      { kind: 'exist', path: 'metadata.tags', type: 'string', value: '' },
      { kind: 'exist', path: 'metadata.tags', type: 'string', value: '' }
    );
    standard.params.push({ path: 'top_p', type: 'number', value: '1' });
    const duplicateCondition = formToRule(standard, families.default);
    expect(duplicateCondition.ok).toBe(false);
    if (!duplicateCondition.ok) expect(duplicateCondition.issue).toBe('duplicateCondition');
  });

  it('requires a model pattern and at least one parameter', () => {
    const form = ruleToForm({ models: [{ name: '*' }], params: {} }, families.default);
    form.models[0].name = '';
    const noName = formToRule(form, families.default);
    expect(noName.ok).toBe(false);
    if (!noName.ok) expect(noName.issue).toBe('modelName');

    form.models[0].name = '*';
    const noParams = formToRule(form, families.default);
    expect(noParams.ok).toBe(false);
    if (!noParams.ok) expect(noParams.issue).toBe('emptyParams');
  });

  it('validates advanced JSON without silently rewriting it and rejects unknown model keys', () => {
    const verbatim =
      '{"models":[{"name":"*","protocol":"codex","headers":{"X-Tier":"gold"}}],"params":{"a":"b"}}';
    const accepted = parseRuleText(verbatim, families.default);
    expect(accepted.ok).toBe(true);
    if (accepted.ok) expect(JSON.stringify(accepted.rule)).toBe(verbatim);

    for (const [family, body] of [
      ['default', '{"models":[{"name":"*","custom":1}],"params":{"a":"b"}}'],
      ['filter', '{"models":[{"name":"*","params":[]}],"params":["a"]}'],
      [
        'replace',
        '{"models":[{"name":"*","custom":1}],"target":{"role":"user","all":true},"replacements":[{"a":"b"}]}',
      ],
    ] as const) {
      expect(parseRuleText(body, families[family]), `${family} ${body}`).toEqual({
        ok: false,
        issue: 'jsonConditions',
      });
    }
    for (const [family, body] of [
      ['default', '{'],
      ['default', '{"models":[],"params":{}}'],
      ['default', '{"models":[{"name":""}],"params":{}}'],
      ['default', '{"models":[{"name":"*"}],"params":{},"extra":1}'],
      ['default', '{"models":[{"name":"*"}],"params":[]}'],
      ['filter', '{"models":[{"name":"*"}],"params":{}}'],
      ['default-raw', '{"models":[{"name":"*"}],"params":{"a":{"b":1}}}'],
    ] as const) {
      expect(parseRuleText(body, families[family]).ok, `${family} ${body}`).toBe(false);
    }
    for (const [family, body] of [
      ['default', '{"models":[{"name":" gpt-* "}],"params":{"a":1}}'],
      ['default', '{"models":[{"name":"*","headers":{"X-Foo":"1","x-foo":"2"}}],"params":{"a":1}}'],
      ['default', '{"models":[{"name":"*","headers":{" X-Foo ":"1"}}],"params":{"a":1}}'],
      ['default', '{"models":[{"name":"*","match":[{"a":1,"b":2}]}],"params":{"a":1}}'],
      ['default', '{"models":[{"name":"*","match":[{"a":1},{"a":2}]}],"params":{"a":1}}'],
      ['default', '{"models":[{"name":"*","not-match":[{" a ":1}]}],"params":{"a":1}}'],
      ['default', '{"models":[{"name":"*","exist":["a","a"]}],"params":{"a":1}}'],
      ['default', '{"models":[{"name":"*","not-exist":[" a "]}],"params":{"a":1}}'],
      ['default', '{"models":[{"name":"*"}],"params":{" a ":1}}'],
      ['filter', '{"models":[{"name":"*"}],"params":["a","a"]}'],
      ['filter', '{"models":[{"name":"*"}],"params":[" a "]}'],
      ['default-raw', '{"models":[{"name":"*"}],"params":{"a":"{bad"}}'],
    ] as const) {
      expect(parseRuleText(body, families[family]).ok, `${family} ${body}`).toBe(false);
    }

    const replaceRule = formToRule(
      {
        models: [{ name: 'gpt-*', protocol: '', from: '', rows: [] }],
        params: [],
        replaceTargetMode: 'all',
        replacePositions: '',
        replaceEntries: [{ search: '   ', replace: 'x' }],
      },
      families.replace
    );
    expect(replaceRule.ok).toBe(true);
  });

  it('summarizes a rule for the list without a translated value', () => {
    const summary = ruleSummary(
      { models: [{ name: 'gpt-*' }, { name: 'o3-*' }], params: { a: 1, b: 2 } },
      families.default
    );
    expect(summary).toContain('gpt-*, o3-*');
    expect(ruleSummary({ models: [{ name: '*' }], params: ['a'] }, families.filter)).toContain('*');
  });

  it('builds a valid payload.filter emptyRule for JSON mode save validation', async () => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    const onApply = vi.fn();
    let renderer: ReactTestRenderer | undefined;
    try {
      await act(async () => {
        renderer = create(
          createElement(GatewayRuleEditor, {
            family: families.filter,
            rules: [],
            readOnly: false,
            onCancel: vi.fn(),
            onApply,
          })
        );
      });
      await act(async () => {
        renderer!.root.findByProps({ 'data-testid': 'rule-add' }).props.onClick();
      });
      await act(async () => {
        renderer!.root.findByProps({ 'data-testid': 'rule-mode-json' }).props.onClick();
      });
      const jsonTextarea = renderer!.root.findByProps({ 'data-testid': 'rule-json' });
      expect(JSON.parse(jsonTextarea.props.value)).toEqual({
        models: [{ name: '*' }],
        params: [],
      });
      expect(parseRuleText(jsonTextarea.props.value, families.filter).ok).toBe(true);

      await act(async () => {
        jsonTextarea.props.onChange({
          target: { value: '{"models":[{"name":"*"}],"params":["metadata.client"]}' },
        });
      });
      await act(async () => {
        renderer!.root.findByProps({ 'data-testid': 'rule-editor-apply' }).props.onClick();
      });
      expect(renderer!.root.findAllByProps({ 'data-testid': 'rule-error' })).toHaveLength(0);

      await act(async () => {
        renderer!.root.findByProps({ 'data-testid': 'rule-save' }).props.onClick();
      });
      expect(onApply).toHaveBeenCalledWith([
        { models: [{ name: '*' }], params: ['metadata.client'] },
      ]);
    } finally {
      if (renderer) {
        await act(async () => {
          renderer!.unmount();
        });
      }
    }
  });

  it('round-trips 64-bit wide integers losslessly in structured mode without failing object check', async () => {
    const wideRule = parseGatewayJSON(
      '{"models":[{"name":"*","match":[{"metadata.id":9007199254740993}]}],"params":{"requests.payload.limit":9007199254740993,"nested":{"id":-9007199254740995}}}'
    );
    const form = ruleToForm(wideRule, families.default);
    expect(form.models[0].rows[0]).toMatchObject({
      kind: 'match',
      path: 'metadata.id',
      type: 'number',
      value: '9007199254740993',
    });
    expect(form.params[0]).toMatchObject({
      path: 'requests.payload.limit',
      type: 'number',
      value: '9007199254740993',
    });

    const roundTripped = formToRule(form, families.default);
    expect(roundTripped.ok).toBe(true);
    if (roundTripped.ok) {
      const json = stringifyGatewayJSON(roundTripped.rule);
      expect(json).toContain('9007199254740993');
      expect(json).toContain('-9007199254740995');
      expect(json).not.toContain('9007199254740992');
    }

    // Bare wide-integer literal is still rejected when type is explicitly object.
    const badObject = formToRule(
      {
        ...form,
        params: [{ path: 'nested', type: 'object', value: '9007199254740993' }],
      },
      families.default
    );
    expect(badObject).toEqual({ ok: false, issue: 'paramValue', detail: 'nested' });

    // End-to-end through GatewayRuleEditor structured Apply -> Save.
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    const onApply = vi.fn();
    let renderer: ReactTestRenderer | undefined;
    try {
      await act(async () => {
        renderer = create(
          createElement(GatewayRuleEditor, {
            family: families.default,
            rules: [wideRule],
            readOnly: false,
            onCancel: vi.fn(),
            onApply,
          })
        );
      });
      await act(async () => {
        renderer!.root.findByProps({ 'data-testid': 'rule-edit-0' }).props.onClick();
      });
      await act(async () => {
        renderer!.root.findByProps({ 'data-testid': 'rule-editor-apply' }).props.onClick();
      });
      expect(renderer!.root.findAllByProps({ 'data-testid': 'rule-error' })).toHaveLength(0);
      await act(async () => {
        renderer!.root.findByProps({ 'data-testid': 'rule-save' }).props.onClick();
      });
      expect(onApply).toHaveBeenCalledTimes(1);
      expect(stringifyGatewayJSON(onApply.mock.calls[0][0])).toContain('9007199254740993');
    } finally {
      if (renderer) {
        await act(async () => {
          renderer!.unmount();
        });
      }
    }
  });

  it('blocks switchMode("json") on invalid structured form without overwriting edits and lets empty replace round-trip', async () => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    let renderer: ReactTestRenderer | undefined;
    try {
      await act(async () => {
        renderer = create(
          createElement(GatewayRuleEditor, {
            family: families.default,
            rules: [{ models: [{ name: 'gpt-*' }], params: { temperature: 0.5 } }],
            readOnly: false,
            onCancel: vi.fn(),
            onApply: vi.fn(),
          })
        );
      });
      await act(async () => {
        renderer!.root.findByProps({ 'data-testid': 'rule-edit-0' }).props.onClick();
      });
      await act(async () => {
        renderer!.root
          .findByProps({ 'data-testid': 'rule-model-name-0' })
          .props.onChange({ target: { value: '' } });
      });
      await act(async () => {
        renderer!.root.findByProps({ 'data-testid': 'rule-mode-json' }).props.onClick();
      });
      expect(renderer!.root.findAllByProps({ 'data-testid': 'rule-json' })).toHaveLength(0);
      expect(renderer!.root.findByProps({ 'data-testid': 'rule-error' }).props.children).toBe(
        c('ruleIssueModelName')
      );
      expect(
        renderer!.root.findByProps({ 'data-testid': 'rule-model-name-0' }).props.value
      ).toBe('');
      await act(async () => {
        renderer!.unmount();
      });

      // Untouched Add rule on replace round-trips cleanly between structured and JSON without trapping.
      await act(async () => {
        renderer = create(
          createElement(GatewayRuleEditor, {
            family: families.replace,
            rules: [],
            readOnly: false,
            onCancel: vi.fn(),
            onApply: vi.fn(),
          })
        );
      });
      await act(async () => {
        renderer!.root.findByProps({ 'data-testid': 'rule-add' }).props.onClick();
      });
      await act(async () => {
        renderer!.root.findByProps({ 'data-testid': 'rule-mode-json' }).props.onClick();
      });
      expect(renderer!.root.findByProps({ 'data-testid': 'rule-json' })).toBeDefined();
      await act(async () => {
        renderer!.root.findByProps({ 'data-testid': 'rule-mode-structured' }).props.onClick();
      });
      expect(renderer!.root.findByProps({ 'data-testid': 'rule-replace-search-0' })).toBeDefined();
      expect(renderer!.root.findAllByProps({ 'data-testid': 'rule-error' })).toHaveLength(0);
    } finally {
      if (renderer) {
        await act(async () => {
          renderer!.unmount();
        });
      }
    }
  });

  it('defaults param row value to "true" when switching param type to boolean', async () => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    const onApply = vi.fn();
    let renderer: ReactTestRenderer | undefined;
    try {
      await act(async () => {
        renderer = create(
          createElement(GatewayRuleEditor, {
            family: families.default,
            rules: [],
            readOnly: false,
            onCancel: vi.fn(),
            onApply,
          })
        );
      });
      await act(async () => {
        renderer!.root.findByProps({ 'data-testid': 'rule-add' }).props.onClick();
      });
      await act(async () => {
        renderer!.root.findByProps({ 'data-testid': 'rule-param-add' }).props.onClick();
      });
      const findParamRow = () =>
        renderer!.root.find(
          (node) => typeof node.type === 'string' && node.props['data-param-row'] === 0
        );
      await act(async () => {
        findParamRow().findAllByType('input')[0].props.onChange({ target: { value: 'stream' } });
      });
      await act(async () => {
        findParamRow().findByType('select').props.onChange({ target: { value: 'boolean' } });
      });
      expect(findParamRow().findAllByType('input')[1].props.value).toBe('true');

      await act(async () => {
        renderer!.root.findByProps({ 'data-testid': 'rule-editor-apply' }).props.onClick();
      });
      await act(async () => {
        renderer!.root.findByProps({ 'data-testid': 'rule-save' }).props.onClick();
      });
      expect(onApply).toHaveBeenCalledWith([{ models: [{ name: '*' }], params: { stream: true } }]);
    } finally {
      if (renderer) {
        await act(async () => {
          renderer!.unmount();
        });
      }
    }
  });

  it('flags replace rows with empty search and non-empty replacement instead of silently dropping', () => {
    const result = formToRule(
      {
        models: [{ name: '*', protocol: '', from: '', rows: [] }],
        params: [],
        replaceTargetMode: 'all',
        replacePositions: '',
        replaceEntries: [
          { search: 'foo', replace: 'bar' },
          { search: '', replace: 'non-empty' },
        ],
      },
      families.replace
    );
    expect(result).toEqual({ ok: false, issue: 'replacePairs' });
  });
});
