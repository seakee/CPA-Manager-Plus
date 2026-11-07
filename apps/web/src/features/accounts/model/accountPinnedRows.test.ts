import { describe, expect, it } from 'vitest';
import { applyAccountPinnedOrder, toggleAccountPinnedKey } from './accountPinnedRows';

const rows = (...keys: string[]) => keys.map((selectionKey) => ({ selectionKey }));
const keysOf = (items: { selectionKey: string }[]) => items.map((item) => item.selectionKey);

describe('applyAccountPinnedOrder', () => {
  it('puts pinned rows first in pin order and keeps the rest in sorted order', () => {
    const ordered = applyAccountPinnedOrder(rows('a', 'b', 'c', 'd', 'e'), ['d', 'b']);
    expect(keysOf(ordered)).toEqual(['d', 'b', 'a', 'c', 'e']);
  });

  it('never adds pinned rows that the filters removed', () => {
    const ordered = applyAccountPinnedOrder(rows('a', 'c'), ['b', 'c']);
    expect(keysOf(ordered)).toEqual(['c', 'a']);
  });

  it('appends new pins after existing pins and removes them on second toggle', () => {
    const pinned = toggleAccountPinnedKey(toggleAccountPinnedKey([], 'x'), 'y');
    expect(pinned).toEqual(['x', 'y']);
    expect(toggleAccountPinnedKey(pinned, 'x')).toEqual(['y']);
  });

  it('leaves the incoming row order untouched when nothing is pinned', () => {
    const original = rows('c', 'a', 'b');
    const ordered = applyAccountPinnedOrder(original, []);
    expect(keysOf(ordered)).toEqual(['c', 'a', 'b']);
    expect(ordered).not.toBe(original);
  });
});
