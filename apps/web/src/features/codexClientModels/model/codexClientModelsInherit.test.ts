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
          [INHERIT_KEY]: { 'model_messages.notes': 'gpt-5.5', context_window: 'gpt-5.5' },
          model_messages: { notes: 'local' },
        },
        ['model_messages', 'notes']
      )
    ).toEqual({ [INHERIT_KEY]: { context_window: 'gpt-5.5' } });
  });
});

describe('applyInheritDirectives', () => {
  it('returns the base entry untouched when there is nothing to apply', () => {
    expect(applyInheritDirectives(catalog[0], new Map(), lookup)).toBe(catalog[0]);
  });

  it('keeps the identity fields of the local entry when the whole entry is inherited', () => {
    const result = applyInheritDirectives(
      { slug: 'my-sol', display_name: 'My Sol', description: 'local', context_window: 1 },
      new Map([['', 'gpt-5.6-sol']]),
      lookup
    );

    expect(result).toEqual({
      slug: 'my-sol',
      display_name: 'My Sol',
      description: 'local',
      context_window: 400000,
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
        ['context_window', 'gpt-5.6-sol'],
      ]),
      lookup
    ) as Record<string, unknown>;

    // 整条继承先铺底，随后的深层指令细化它已经替换掉的子树。
    expect(result.context_window).toBe(400000);
    expect(result.model_messages).toEqual({ notes: 'from 5.5' });
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

  it('reports identity fields, self references and unknown sources', () => {
    expect(validate({ [INHERIT_KEY]: { display_name: 'gpt-5.5' } })).toEqual([
      { path: 'display_name', code: 'non_inheritable_field' },
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
        patch: { [INHERIT_KEY]: { context_window: 'thin' } },
        catalog: [source],
      })
    ).toEqual([{ path: 'context_window', code: 'source_missing_path' }]);
  });

  it('collects one issue per broken entry, keeping the others', () => {
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
      { path: 'model_messages.notes', code: 'unknown_source' },
    ]);
  });
});
