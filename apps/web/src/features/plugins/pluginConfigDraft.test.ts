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
});
