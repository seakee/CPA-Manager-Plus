import { describe, expect, it } from 'vitest';
import {
  buildOverrideTree,
  collectExpandableIds,
  countOverrideNodes,
  filterOverrideTree,
  formatOverridePreview,
  formatStringList,
  isStringListArray,
  parseStringList,
  removeOverrideKey,
  resolveInheritSource,
  seedOverrideValue,
  setOverrideRemoved,
  setOverrideValue,
  type OverrideTreeNode,
} from './codexClientModelsTree';

const effectiveEntry = {
  slug: 'gpt-5.5',
  display_name: 'GPT-5.5',
  context_window: 272000,
  supports_parallel_tool_calls: true,
  auto_review_model_override: null,
  base_instructions: 'line one\nline two',
  model_messages: {
    instructions_template: 'template one\ntemplate two',
    notes: 'keep in sync',
  },
  available_in_plans: ['plus', 'pro'],
  supported_reasoning_levels: [
    { description: 'Fast', effort: 'low' },
    { description: 'Deep', effort: 'high' },
  ],
};

const findNode = (
  nodes: ReadonlyArray<OverrideTreeNode>,
  id: string
): OverrideTreeNode | undefined => {
  for (const node of nodes) {
    if (node.id === id) return node;
    const nested = findNode(node.children, id);
    if (nested) return nested;
  }
  return undefined;
};

const requireNode = (nodes: ReadonlyArray<OverrideTreeNode>, id: string): OverrideTreeNode => {
  const node = findNode(nodes, id);
  if (!node) throw new Error(`node ${id} not found`);
  return node;
};

const childIds = (nodes: ReadonlyArray<OverrideTreeNode>) => nodes.map((node) => node.id);

describe('buildOverrideTree', () => {
  it('marks inherited, overridden and removed fields', () => {
    const nodes = buildOverrideTree({
      effective: effectiveEntry,
      patch: {
        display_name: 'Local GPT-5.5',
        model_messages: { notes: null },
      },
    });

    expect(requireNode(nodes, 'display_name')).toMatchObject({
      state: 'override',
      kind: 'string',
      value: 'Local GPT-5.5',
    });
    expect(requireNode(nodes, 'context_window')).toMatchObject({
      state: 'inherit',
      kind: 'number',
      value: 272000,
    });
    expect(requireNode(nodes, 'model_messages.notes')).toMatchObject({
      state: 'removed',
      kind: 'empty',
    });
    expect(requireNode(nodes, 'model_messages')).toMatchObject({
      state: 'override',
      kind: 'object',
    });
  });

  it('classifies multi-line strings so the editor can keep real newlines', () => {
    const nodes = buildOverrideTree({ effective: effectiveEntry, patch: {} });
    expect(requireNode(nodes, 'base_instructions').kind).toBe('multiline');
    expect(requireNode(nodes, 'model_messages.instructions_template').kind).toBe('multiline');
    expect(requireNode(nodes, 'slug').kind).toBe('string');
  });

  it('merges keys that only exist in the patch', () => {
    const nodes = buildOverrideTree({
      effective: { a: 1 },
      patch: { a: 1, b: { c: 2 } },
    });
    expect(childIds(nodes)).toEqual(['a', 'b']);
    expect(requireNode(nodes, 'b.c')).toMatchObject({ state: 'override', value: 2 });
  });

  it('expands arrays of objects as read-only children', () => {
    const nodes = buildOverrideTree({ effective: effectiveEntry, patch: {} });
    const levels = requireNode(nodes, 'supported_reasoning_levels');
    expect(levels.kind).toBe('array');
    expect(childIds(levels.children)).toEqual([
      'supported_reasoning_levels.0',
      'supported_reasoning_levels.1',
    ]);
    expect(levels.children[0]).toMatchObject({ insideArray: true, key: '[0]' });
    expect(levels.children[0].children.every((child) => child.insideArray)).toBe(true);
  });

  it('keeps scalar arrays as a single value node', () => {
    const nodes = buildOverrideTree({ effective: effectiveEntry, patch: {} });
    const plans = requireNode(nodes, 'available_in_plans');
    expect(plans.kind).toBe('array');
    expect(plans.children).toEqual([]);
  });

  it('reads a node value from the patch once the field is overridden', () => {
    const nodes = buildOverrideTree({
      effective: { available_in_plans: ['plus'] },
      patch: { available_in_plans: ['plus', 'team'] },
    });
    expect(requireNode(nodes, 'available_in_plans')).toMatchObject({
      value: ['plus', 'team'],
      effective: ['plus'],
      inPatch: true,
    });
  });
});

describe('setOverrideValue', () => {
  it('writes a field without mutating the incoming patch', () => {
    const patch = { display_name: 'Local' };
    const next = setOverrideValue(patch, ['display_name'], 'Local 2');
    expect(next).toEqual({ display_name: 'Local 2' });
    expect(patch).toEqual({ display_name: 'Local' });
  });

  it('creates intermediate objects on the way down', () => {
    expect(setOverrideValue({}, ['model_messages', 'notes'], 'changed')).toEqual({
      model_messages: { notes: 'changed' },
    });
  });

  it('materializes the effective array before writing an element path', () => {
    const effective = {
      supported_reasoning_levels: [
        { description: 'Fast', effort: 'low' },
        { description: 'Deep', effort: 'high' },
      ],
    };
    const next = setOverrideValue(
      {},
      ['supported_reasoning_levels', 1, 'effort'],
      'medium',
      effective
    );
    expect(next).toEqual({
      supported_reasoning_levels: [
        { description: 'Fast', effort: 'low' },
        { description: 'Deep', effort: 'medium' },
      ],
    });
    expect(effective.supported_reasoning_levels[1].effort).toBe('high');
  });

  it('treats null as a field removal', () => {
    expect(setOverrideRemoved({}, ['display_name'])).toEqual({ display_name: null });
  });
});

describe('resolveInheritSource', () => {
  it('prefers the directive declared on the path itself', () => {
    const directives = new Map([
      ['', 'whole-entry'],
      ['model_messages', 'parent'],
      ['model_messages.notes', 'own'],
    ]);

    expect(resolveInheritSource(directives, ['model_messages', 'notes'])).toEqual({
      source: 'own',
      declared: 'own',
    });
  });

  it('falls back to the nearest ancestor directive', () => {
    const directives = new Map([
      ['', 'whole-entry'],
      ['model_messages', 'parent'],
    ]);

    expect(resolveInheritSource(directives, ['model_messages', 'notes'])).toEqual({
      source: 'parent',
      declared: null,
    });
    // 祖先链上最近的指令胜出，更浅的整条指令不再参与。
    expect(resolveInheritSource(directives, ['context_window'])).toEqual({
      source: 'whole-entry',
      declared: null,
    });
  });

  it('indexes array elements with their position', () => {
    const directives = new Map([['supported_reasoning_levels.0', 'gpt-5.6-sol']]);

    expect(resolveInheritSource(directives, ['supported_reasoning_levels', 0])).toEqual({
      source: 'gpt-5.6-sol',
      declared: 'gpt-5.6-sol',
    });
    expect(resolveInheritSource(directives, ['supported_reasoning_levels', 1]).source).toBeNull();
  });

  it('reports no source when nothing covers the path', () => {
    expect(resolveInheritSource(new Map(), ['display_name'])).toEqual({
      source: null,
      declared: null,
    });
  });
});

describe('seedOverrideValue', () => {
  const seedOf = (effective: unknown, kind: string) =>
    seedOverrideValue({ kind, inEffective: true, effective } as never);

  it('copies the effective value so edits do not touch the catalog entry', () => {
    const effective = { a: 1 };
    const seeded = seedOf(effective, 'object') as Record<string, unknown>;

    expect(seeded).toEqual({ a: 1 });
    expect(seeded).not.toBe(effective);

    const list = ['plus'];
    const seededList = seedOf(list, 'array') as string[];
    expect(seededList).toEqual(['plus']);
    expect(seededList).not.toBe(list);
  });

  it('falls back to an empty value of the same shape when the entry lacks the field', () => {
    const seedFor = (kind: string) =>
      seedOverrideValue({ kind, inEffective: false, effective: undefined } as never);

    expect(seedFor('object')).toEqual({});
    expect(seedFor('array')).toEqual([]);
    expect(seedFor('boolean')).toBe(true);
    expect(seedFor('number')).toBe(0);
    expect(seedFor('string')).toBe('');
    expect(seedFor('multiline')).toBe('');
    expect(seedFor('empty')).toBe('');
  });
});

describe('removeOverrideKey', () => {
  it('drops the key and prunes ancestors that become empty', () => {
    const patch = { model_messages: { notes: 'changed' }, display_name: 'Local' };
    expect(removeOverrideKey(patch, ['model_messages', 'notes'])).toEqual({
      display_name: 'Local',
    });
  });

  it('keeps ancestors that still hold other overrides', () => {
    const patch = { model_messages: { notes: 'changed', instructions_template: 'x' } };
    expect(removeOverrideKey(patch, ['model_messages', 'notes'])).toEqual({
      model_messages: { instructions_template: 'x' },
    });
  });
});

describe('countOverrideNodes', () => {
  it('counts leaf positions and whole arrays', () => {
    expect(countOverrideNodes({})).toBe(0);
    expect(countOverrideNodes({ a: 1, b: { c: 2, d: null }, e: [1, 2] })).toBe(4);
    expect(countOverrideNodes(null)).toBe(1);
  });
});

describe('filterOverrideTree', () => {
  const nodes = buildOverrideTree({
    effective: effectiveEntry,
    patch: { display_name: 'Local GPT-5.5', model_messages: { notes: 'changed' } },
  });

  it('returns the same nodes when nothing is filtered', () => {
    const result = filterOverrideTree(nodes, { query: '', onlyOverridden: false });
    expect(result).toEqual(nodes);
    expect(result[0]).toBe(nodes[0]);
  });

  it('matches field names and keeps the ancestors of a match', () => {
    const result = filterOverrideTree(nodes, { query: 'notes', onlyOverridden: false });
    expect(childIds(result)).toEqual(['model_messages']);
    expect(childIds(result[0].children)).toEqual(['model_messages.notes']);
  });

  it('does not match on field values', () => {
    expect(filterOverrideTree(nodes, { query: 'Local', onlyOverridden: false })).toEqual([]);
  });

  it('keeps only overridden fields with their ancestors', () => {
    const result = filterOverrideTree(nodes, { query: '', onlyOverridden: true });
    expect(childIds(result)).toEqual(['display_name', 'model_messages']);
    expect(childIds(result[1].children)).toEqual(['model_messages.notes']);
  });

  it('combines the keyword and override filters', () => {
    expect(filterOverrideTree(nodes, { query: 'slug', onlyOverridden: true })).toEqual([]);
    expect(childIds(filterOverrideTree(nodes, { query: 'slug', onlyOverridden: false }))).toEqual([
      'slug',
    ]);
  });
});

describe('collectExpandableIds', () => {
  it('collects the ids of nodes that have children', () => {
    const nodes = buildOverrideTree({ effective: effectiveEntry, patch: {} });
    const ids = collectExpandableIds(nodes);
    expect(ids).toContain('model_messages');
    expect(ids).toContain('supported_reasoning_levels');
    expect(ids).not.toContain('display_name');
  });
});

describe('string list helpers', () => {
  it('detects arrays made only of strings', () => {
    expect(isStringListArray(['plus', 'pro'])).toBe(true);
    expect(isStringListArray([])).toBe(true);
    expect(isStringListArray(['plus', 1])).toBe(false);
    expect(isStringListArray('plus')).toBe(false);
  });

  it('round-trips through one item per line', () => {
    expect(formatStringList(['plus', 'pro'])).toBe('plus\npro');
    expect(parseStringList(' plus \n\npro\n')).toEqual(['plus', 'pro']);
  });
});

describe('formatOverridePreview', () => {
  it('keeps real newlines instead of escaping them', () => {
    expect(formatOverridePreview('line one\nline two')).toBe('line one\nline two');
  });

  it('pretty-prints arrays and objects so the preview stays readable', () => {
    expect(formatOverridePreview(['plus', 'pro'])).toBe('[\n  "plus",\n  "pro"\n]');
    expect(formatOverridePreview(null)).toBe('null');
  });
});
