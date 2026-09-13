import { describe, expect, it } from 'vitest';
import {
  INHERIT_KEY,
  applyInheritDirectives,
  canInheritPath,
  clearFieldOverride,
  clearInheritSource,
  collectInheritSources,
  countInheritUsage,
  createInheritSourceLookup,
  inheritPathKey,
  isNonInheritablePathKey,
  readInheritDirectives,
  restoreFieldDefault,
  setInheritOptOut,
  setInheritSource,
  validateInheritDirectives,
} from './codexClientModelsInherit';

const catalog = [
  {
    slug: 'gpt-5.5',
    display_name: 'GPT-5.5',
    context_window: 272000,
    model_messages: { notes: 'from 5.5' },
  },
  {
    slug: 'gpt-5.6-sol',
    display_name: 'GPT-5.6 Sol',
    context_window: 400000,
    model_messages: { notes: 'from sol' },
  },
] as const;

const lookup = createInheritSourceLookup(catalog);

const directivesOf = (patch: unknown) => readInheritDirectives(patch);

describe('readInheritDirectives', () => {
  it('reads the string shorthand as a whole-entry directive', () => {
    expect([...directivesOf({ [INHERIT_KEY]: ' gpt-5.5 ' })]).toEqual([['', 'gpt-5.5']]);
    expect(directivesOf({ [INHERIT_KEY]: '   ' }).size).toBe(0);
  });

  it('reads an object of dotted paths and ignores unusable entries', () => {
    const directives = directivesOf({
      [INHERIT_KEY]: { 'model_messages.notes': 'gpt-5.5', context_window: '  ', other: 3 },
    });

    expect([...directives]).toEqual([['model_messages.notes', 'gpt-5.5']]);
  });

  it('reads a null source as an explicit opt-out', () => {
    expect([...directivesOf({ [INHERIT_KEY]: { model_messages: null } })]).toEqual([
      ['model_messages', null],
    ]);
    expect([...directivesOf({ [INHERIT_KEY]: ' gpt-5.5 ' })]).toEqual([['', 'gpt-5.5']]);
  });

  it('treats a missing or malformed directive block as no inheritance', () => {
    expect(directivesOf({}).size).toBe(0);
    expect(directivesOf(null).size).toBe(0);
    expect(directivesOf('scalar').size).toBe(0);
    expect(directivesOf({ [INHERIT_KEY]: [1, 2] }).size).toBe(0);
  });
});

describe('setInheritSource and clearInheritSource', () => {
  it('writes the shorthand when the whole entry is the only directive', () => {
    expect(setInheritSource({ display_name: 'Local' }, [], 'gpt-5.5')).toEqual({
      [INHERIT_KEY]: 'gpt-5.5',
      display_name: 'Local',
    });
  });

  it('writes a sorted object once a field joins the directive', () => {
    const patch = setInheritSource(
      setInheritSource({}, [], 'gpt-5.5'),
      ['context_window'],
      'gpt-5.6-sol'
    );

    expect(patch).toEqual({
      [INHERIT_KEY]: { '': 'gpt-5.5', context_window: 'gpt-5.6-sol' },
    });
  });

  it('drops the directive block once the last directive is cleared', () => {
    expect(clearInheritSource({ [INHERIT_KEY]: 'gpt-5.5', display_name: 'Local' }, [])).toEqual({
      display_name: 'Local',
    });
    expect(
      clearInheritSource({ [INHERIT_KEY]: { context_window: 'gpt-5.5' } }, ['context_window'])
    ).toEqual({});
  });

  it('ignores blank sources and keeps ancestor directives intact', () => {
    expect(setInheritSource({ [INHERIT_KEY]: 'gpt-5.5' }, ['context_window'], '  ')).toEqual({
      [INHERIT_KEY]: 'gpt-5.5',
    });
    expect(
      clearInheritSource({ [INHERIT_KEY]: { '': 'gpt-5.5', context_window: 'gpt-5.5' } }, [
        'context_window',
      ])
    ).toEqual({ [INHERIT_KEY]: 'gpt-5.5' });
  });
});

describe('clearFieldOverride', () => {
  it('removes both the local value and the field directive', () => {
    expect(
      clearFieldOverride(
        {
          [INHERIT_KEY]: { 'model_messages.notes': 'gpt-5.5', base_instructions: 'gpt-5.5' },
          model_messages: { notes: 'local' },
        },
        ['model_messages', 'notes']
      )
    ).toEqual({ [INHERIT_KEY]: { base_instructions: 'gpt-5.5' } });
  });
});

describe('applyInheritDirectives', () => {
  it('returns the base entry untouched when there is nothing to apply', () => {
    expect(applyInheritDirectives(catalog[0], new Map(), lookup)).toBe(catalog[0]);
  });

  it('keeps the fields only the model supplies out of a whole-entry inherit', () => {
    const result = applyInheritDirectives(
      { slug: 'my-sol', display_name: 'My Sol', description: 'local', context_window: 1 },
      new Map([['', 'gpt-5.6-sol']]),
      lookup
    );

    // 身份字段与可见性、位置、上下文窗口都来自条目自身，其余字段照旧继承。
    expect(result).toEqual({
      slug: 'my-sol',
      display_name: 'My Sol',
      description: 'local',
      context_window: 1,
      model_messages: { notes: 'from sol' },
    });
  });

  it('replaces exactly the declared path, including nested keys', () => {
    const result = applyInheritDirectives(
      {
        slug: 'my-sol',
        display_name: 'My Sol',
        context_window: 1,
        model_messages: { notes: 'local' },
      },
      new Map([
        ['', 'gpt-5.5'],
        ['model_messages.notes', 'gpt-5.6-sol'],
      ]),
      lookup
    ) as Record<string, unknown>;

    // 整条继承先铺底，随后的深层指令细化它已经替换掉的子树。
    expect(result.context_window).toBe(1);
    expect(result.model_messages).toEqual({ notes: 'from sol' });
  });

  it('leaves the fields only the model supplies alone when a directive names them', () => {
    const result = applyInheritDirectives(
      { slug: 'my-sol', display_name: 'My Sol', description: 'local', context_window: 1 },
      new Map([
        ['display_name', 'gpt-5.6-sol'],
        ['description', 'gpt-5.6-sol'],
        ['context_window', 'gpt-5.6-sol'],
      ]),
      lookup
    );

    // 后端会拒绝这些指令，预览保持条目自身的取值。
    expect(result).toEqual({
      slug: 'my-sol',
      display_name: 'My Sol',
      description: 'local',
      context_window: 1,
    });
  });

  it('keeps the fields only the model supplies out of a whole-entry inherit', () => {
    const result = applyInheritDirectives(
      {
        slug: 'my-sol',
        display_name: 'My Sol',
        context_window: 1,
        supported_reasoning_levels: [{ effort: 'low' }],
        model_messages: { notes: 'local' },
      },
      new Map([['', 'gpt-5.6-sol']]),
      lookup
    );

    // 只由模型自身提供的字段保持条目自身的取值，其余字段整条换成来源的取值。
    expect(result).toEqual({
      slug: 'my-sol',
      display_name: 'My Sol',
      context_window: 1,
      model_messages: { notes: 'from sol' },
    });
  });

  it('drops a field the entry does not set when the source does not supply it either', () => {
    const result = applyInheritDirectives(
      { slug: 'my-sol', display_name: 'My Sol' },
      new Map([['', 'gpt-5.6-sol']]),
      lookup
    ) as Record<string, unknown>;

    expect(result.supported_reasoning_levels).toBeUndefined();
    expect(result.model_messages).toEqual({ notes: 'from sol' });
  });

  it('ignores a directive written on a field only the model supplies', () => {
    const result = applyInheritDirectives(
      { slug: 'my-sol', display_name: 'My Sol', context_window: 1 },
      new Map([['context_window', 'gpt-5.6-sol']]),
      lookup
    ) as Record<string, unknown>;

    expect(result.context_window).toBe(1);
  });

  it('writes a field the source entry does not have and keeps the base value otherwise', () => {
    const missing = applyInheritDirectives(
      { slug: 'my-sol', display_name: 'My Sol', context_window: 1 },
      new Map([['service_tiers', 'gpt-5.5']]),
      lookup
    );
    expect(missing).toEqual({ slug: 'my-sol', display_name: 'My Sol', context_window: 1 });

    const unknown = applyInheritDirectives(
      { slug: 'my-sol', display_name: 'My Sol', context_window: 1 },
      new Map([['', 'ghost']]),
      lookup
    );
    expect(unknown).toEqual({ slug: 'my-sol', display_name: 'My Sol', context_window: 1 });
  });
});

describe('applyInheritDirectives opt-out', () => {
  const withOptOut = (path: string) =>
    applyInheritDirectives(
      { slug: 'my-sol', display_name: 'My Sol', model_messages: { notes: 'local' } },
      new Map<string, string | null>([
        ['', 'gpt-5.6-sol'],
        [path, null],
      ]),
      lookup
    ) as Record<string, unknown>;

  it('puts the entry value back at a path that opts out', () => {
    expect(withOptOut('model_messages.notes').model_messages).toEqual({ notes: 'local' });
  });

  it('drops a path the entry does not set instead of inheriting it', () => {
    const result = applyInheritDirectives(
      { slug: 'my-sol', display_name: 'My Sol' },
      new Map<string, string | null>([
        ['', 'gpt-5.6-sol'],
        ['model_messages', null],
      ]),
      lookup
    ) as Record<string, unknown>;

    expect(result.model_messages).toBeUndefined();
  });
});

describe('restoreFieldDefault', () => {
  it('clears the local value when nothing covers the path', () => {
    expect(restoreFieldDefault({ display_name: 'Changed' }, ['display_name'])).toEqual({});
  });

  it('opts the path out instead of letting an ancestor take it over', () => {
    expect(
      restoreFieldDefault({ [INHERIT_KEY]: 'gpt-5.5', model_messages: { notes: 'local' } }, [
        'model_messages',
        'notes',
      ])
    ).toEqual({
      [INHERIT_KEY]: { '': 'gpt-5.5', 'model_messages.notes': null },
    });
  });

  it('leaves the path alone when the field only the model supplies is covered', () => {
    // 上下文窗口不接受继承，清掉本地值就已经回到默认值。
    expect(
      restoreFieldDefault({ [INHERIT_KEY]: 'gpt-5.5', context_window: 1 }, ['context_window'])
    ).toEqual({ [INHERIT_KEY]: 'gpt-5.5' });
  });

  it('needs no opt-out for a field a source cannot supply', () => {
    // 这类字段本来就不接受继承，清掉本地值就已经回到默认值。
    expect(
      restoreFieldDefault({ [INHERIT_KEY]: 'gpt-5.5', context_window: 1 }, ['context_window'])
    ).toEqual({ [INHERIT_KEY]: 'gpt-5.5' });
  });
});

describe('setInheritOptOut', () => {
  it('adds a null source next to an existing whole-entry directive', () => {
    expect(setInheritOptOut({ [INHERIT_KEY]: 'gpt-5.5' }, ['model_messages'])).toEqual({
      [INHERIT_KEY]: { '': 'gpt-5.5', model_messages: null },
    });
  });

  it('drops the shorthand when the whole entry is opted out', () => {
    expect(setInheritOptOut({ [INHERIT_KEY]: 'gpt-5.5' }, [])).toEqual({});
  });
});

describe('collectInheritSources', () => {
  it('lists unique trimmed slugs in order and can exclude the entry itself', () => {
    expect(collectInheritSources([...catalog, { slug: ' gpt-5.5 ' }, { slug: '  ' }, {}])).toEqual([
      'gpt-5.5',
      'gpt-5.6-sol',
    ]);
    expect(collectInheritSources(catalog, 'gpt-5.5')).toEqual(['gpt-5.6-sol']);
  });
});

describe('countInheritUsage', () => {
  it('counts each referencing entry once per source', () => {
    const usage = countInheritUsage({
      a: { [INHERIT_KEY]: 'gpt-5.5' },
      b: { [INHERIT_KEY]: { '': 'gpt-5.5', context_window: 'gpt-5.5' } },
      c: { display_name: 'no inheritance' },
    });

    expect(usage.get('gpt-5.5')).toBe(2);
    expect(usage.get('gpt-5.6-sol')).toBeUndefined();
  });
});

describe('inherit path rules', () => {
  it('knows the identity fields that can never be inherited', () => {
    expect(isNonInheritablePathKey('display_name')).toBe(true);
    expect(isNonInheritablePathKey('model_messages.notes')).toBe(false);
    expect(canInheritPath(['model_messages', 'notes'])).toBe(true);
    expect(canInheritPath(['description'])).toBe(false);
    expect(inheritPathKey(['model_messages', 'notes'])).toBe('model_messages.notes');
    expect(inheritPathKey([])).toBe('');
  });
});

describe('validateInheritDirectives', () => {
  const validate = (patch: unknown, slug = 'my-sol') =>
    validateInheritDirectives({ slug, patch, catalog });

  it('accepts a well formed directive', () => {
    expect(validate({ [INHERIT_KEY]: 'gpt-5.5' })).toEqual([]);
    expect(validate({ [INHERIT_KEY]: { 'model_messages.notes': 'gpt-5.5' } })).toEqual([]);
    expect(validate({ [INHERIT_KEY]: { model_messages: null } })).toEqual([]);
  });

  it('reports nothing when the entry has no directive at all', () => {
    expect(validate({ display_name: 'x' })).toEqual([]);
    expect(validate({ [INHERIT_KEY]: null })).toEqual([]);
    expect(validate(null)).toEqual([]);
  });

  it('reports an unusable directive shape', () => {
    expect(validate({ [INHERIT_KEY]: 5 })).toEqual([{ path: '', code: 'invalid_shape' }]);
    expect(validate({ [INHERIT_KEY]: {} })).toEqual([{ path: '', code: 'invalid_shape' }]);
    expect(validate({ [INHERIT_KEY]: { context_window: 7 } })).toEqual([
      { path: 'context_window', code: 'invalid_shape' },
    ]);
  });

  it('reports paths the backend cannot address', () => {
    expect(validate({ [INHERIT_KEY]: { 'a[0].b': 'gpt-5.5' } })).toEqual([
      { path: 'a[0].b', code: 'invalid_path' },
    ]);
    expect(validate({ [INHERIT_KEY]: { 'a..b': 'gpt-5.5' } })).toEqual([
      { path: 'a..b', code: 'invalid_path' },
    ]);
    expect(validate({ [INHERIT_KEY]: { [INHERIT_KEY]: 'gpt-5.5' } })).toEqual([
      { path: INHERIT_KEY, code: 'invalid_path' },
    ]);
  });

  it('reports fields only the model supplies, self references and unknown sources', () => {
    expect(validate({ [INHERIT_KEY]: { display_name: 'gpt-5.5' } })).toEqual([
      { path: 'display_name', code: 'non_inheritable_field' },
    ]);
    expect(validate({ [INHERIT_KEY]: { visibility: 'gpt-5.5' } })).toEqual([
      { path: 'visibility', code: 'non_inheritable_field' },
    ]);
    expect(validate({ [INHERIT_KEY]: { priority: null } })).toEqual([
      { path: 'priority', code: 'non_inheritable_field' },
    ]);
    expect(validate({ [INHERIT_KEY]: { 'model_messages.notes': 'my-sol' } })).toEqual([
      { path: 'model_messages.notes', code: 'self_reference' },
    ]);
    expect(validate({ [INHERIT_KEY]: { 'model_messages.notes': 'ghost' } })).toEqual([
      { path: 'model_messages.notes', code: 'unknown_source' },
    ]);
  });

  it('reports a source that does not define the inherited path', () => {
    const source = { slug: 'thin', display_name: 'Thin' };
    expect(
      validateInheritDirectives({
        slug: 'my-sol',
        patch: { [INHERIT_KEY]: { base_instructions: 'thin' } },
        catalog: [source],
      })
    ).toEqual([{ path: 'base_instructions', code: 'source_missing_path' }]);
  });

  it('collects one issue per broken path, keeping the others', () => {
    expect(
      validate({
        [INHERIT_KEY]: {
          display_name: 'gpt-5.5',
          context_window: 'gpt-5.6-sol',
          'model_messages.notes': 'ghost',
        },
      })
    ).toEqual([
      { path: 'display_name', code: 'non_inheritable_field' },
      { path: 'context_window', code: 'non_inheritable_field' },
      { path: 'model_messages.notes', code: 'unknown_source' },
    ]);
  });
});
