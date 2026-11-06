import { describe, expect, it } from 'vitest';
import { buildPluginConfigPatch, createPluginConfigDraft } from './pluginConfigDraft';

const t = (key: string) => key;

describe('pluginConfigDraft', () => {
  it('patches only touched fields and preserves JSON value types', () => {
    const fields = [
      { name: 'items', type: 'array', enumValues: [], description: '' },
      { name: 'flag', type: 'boolean', enumValues: [], description: '' },
    ];
    const draft = createPluginConfigDraft(fields, { items: [1, true, { id: 'x' }] }, false);
    draft.touched.add('items');

    expect(buildPluginConfigPatch(draft, fields, t)).toEqual({
      patch: { items: [1, true, { id: 'x' }] },
      errors: {},
    });
  });

  it('uses null when a touched field is cleared', () => {
    const fields = [{ name: 'token', type: 'string', enumValues: [], description: '' }];
    const draft = createPluginConfigDraft(fields, { token: 'old' }, true);
    draft.values.token = '';
    draft.touched.add('token');

    expect(buildPluginConfigPatch(draft, fields, t).patch).toEqual({ token: null });
  });

  it('resets a cleared priority to zero', () => {
    const draft = createPluginConfigDraft([], { priority: 10 }, true);
    draft.priority = '';
    draft.touched.add('priority');
    expect(buildPluginConfigPatch(draft, [], t).patch).toEqual({ priority: 0 });
  });

  it('displays typed defaults for absent config keys without persisting them', () => {
    const fields = [
      { name: 'quota_notes', type: 'boolean', enumValues: [], description: '', defaultValue: true },
      { name: 'disabled', type: 'boolean', enumValues: [], description: '', defaultValue: false },
      { name: 'count', type: 'integer', enumValues: [], description: '', defaultValue: 0 },
      { name: 'timezone', type: 'string', enumValues: [], description: '', defaultValue: 'Asia/Shanghai' },
      { name: 'mode', type: 'enum', enumValues: ['safe', 'fast'], description: '', defaultValue: 'safe' },
      { name: 'items', type: 'array', enumValues: [], description: '', defaultValue: [1, true] },
      { name: 'options', type: 'object', enumValues: [], description: '', defaultValue: { active: true } },
      { name: 'legacy', type: 'boolean', enumValues: [], description: '' },
    ];
    const draft = createPluginConfigDraft(fields, { enabled: true }, true);

    expect(draft.values).toEqual({
      quota_notes: true,
      disabled: false,
      count: '0',
      timezone: 'Asia/Shanghai',
      mode: 'safe',
      items: JSON.stringify([1, true], null, 2),
      options: JSON.stringify({ active: true }, null, 2),
      legacy: false,
    });
    expect(draft.touched.size).toBe(0);
    expect(buildPluginConfigPatch(draft, fields, t)).toEqual({ patch: {}, errors: {} });
  });

  it('prefers explicitly configured false, zero, empty string, and null over defaults', () => {
    const fields = [
      { name: 'flag', type: 'boolean', enumValues: [], description: '', defaultValue: true },
      { name: 'count', type: 'integer', enumValues: [], description: '', defaultValue: 5 },
      { name: 'timezone', type: 'string', enumValues: [], description: '', defaultValue: 'Asia/Shanghai' },
      { name: 'mode', type: 'enum', enumValues: ['safe'], description: '', defaultValue: 'safe' },
    ];
    const draft = createPluginConfigDraft(
      fields,
      { flag: false, count: 0, timezone: '', mode: null },
      false
    );

    expect(draft.values).toEqual({ flag: false, count: '0', timezone: '', mode: '' });
    expect(buildPluginConfigPatch(draft, fields, t).patch).toEqual({});
  });

  it('patches only fields explicitly edited from inherited defaults', () => {
    const fields = [
      { name: 'flag', type: 'boolean', enumValues: [], description: '', defaultValue: true },
      { name: 'timezone', type: 'string', enumValues: [], description: '', defaultValue: 'Asia/Shanghai' },
    ];
    const draft = createPluginConfigDraft(fields, {}, true);
    draft.values.flag = false;
    draft.touched.add('flag');

    expect(buildPluginConfigPatch(draft, fields, t)).toEqual({
      patch: { flag: false },
      errors: {},
    });
  });

});
