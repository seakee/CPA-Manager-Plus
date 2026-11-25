import { useEffect } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  useClientPagination,
  useListSelection,
  usePersistedViewMode,
  type UseClientPaginationResult,
  type UseListSelectionResult,
  type UsePersistedViewModeResult,
} from './index';

interface RowItem {
  key: string;
  selectable?: boolean;
}

function createMemoryStorage() {
  const store = new Map<string, string>();
  return {
    getItem: (key: string) => store.get(key) ?? null,
    setItem: (key: string, value: string) => {
      store.set(key, String(value));
    },
    removeItem: (key: string) => {
      store.delete(key);
    },
    clear: () => {
      store.clear();
    },
  };
}

describe('List headless hooks', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('clamps page when items shrink and resets to page 1 on pageSize or resetDeps change', () => {
    const ref: { current: UseClientPaginationResult<number> | null } = { current: null };

    function Harness({ items, dep }: { items: number[]; dep: string }) {
      const value = useClientPagination(items, {
        initialPageSize: 5,
        resetDeps: [dep],
      });
      useEffect(() => {
        ref.current = value;
      }, [value]);
      return null;
    }

    const twelveItems = Array.from({ length: 12 }, (_, idx) => idx + 1);
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(<Harness items={twelveItems} dep="a" />);
    });

    expect(ref.current!.totalPages).toBe(3);
    expect(ref.current!.page).toBe(1);
    expect(ref.current!.pageItems).toEqual([1, 2, 3, 4, 5]);
    expect(ref.current!.startItem).toBe(1);
    expect(ref.current!.endItem).toBe(5);

    act(() => {
      ref.current!.setPage(3);
    });
    expect(ref.current!.page).toBe(3);
    expect(ref.current!.pageItems).toEqual([11, 12]);

    act(() => {
      renderer.update(<Harness items={[1, 2, 3]} dep="a" />);
    });
    expect(ref.current!.totalPages).toBe(1);
    expect(ref.current!.page).toBe(1);
    expect(ref.current!.pageItems).toEqual([1, 2, 3]);

    act(() => {
      renderer.update(<Harness items={twelveItems} dep="a" />);
    });
    act(() => {
      ref.current!.setPage(2);
    });
    expect(ref.current!.page).toBe(2);

    act(() => {
      renderer.update(<Harness items={twelveItems} dep="b" />);
    });
    expect(ref.current!.page).toBe(1);

    act(() => {
      ref.current!.setPage(2);
    });
    act(() => {
      ref.current!.setPageSize(10);
    });
    expect(ref.current!.pageSize).toBe(10);
    expect(ref.current!.page).toBe(1);
    expect(ref.current!.totalPages).toBe(2);

    act(() => renderer.unmount());
  });

  it('prunes stale selected keys, supports page/all inversion, and clears on exit', () => {
    const ref: { current: UseListSelectionResult<RowItem> | null } = { current: null };

    function Harness({
      items,
      pageItems,
    }: {
      items: RowItem[];
      pageItems: RowItem[];
    }) {
      const value = useListSelection({
        items,
        pageItems,
        getKey: (item) => item.key,
        isSelectable: (item) => item.selectable !== false,
      });
      useEffect(() => {
        ref.current = value;
      }, [value]);
      return null;
    }

    const allItems: RowItem[] = [
      { key: 'a' },
      { key: 'b', selectable: false },
      { key: 'c' },
      { key: 'd' },
    ];
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(<Harness items={allItems} pageItems={allItems.slice(0, 2)} />);
    });

    act(() => {
      ref.current!.enterSelectionMode();
      ref.current!.selectPage();
    });
    expect(ref.current!.selectionMode).toBe(true);
    expect(Array.from(ref.current!.selectedKeys)).toEqual(['a']);

    act(() => {
      ref.current!.invertSelection('all');
    });
    expect(Array.from(ref.current!.selectedKeys)).toEqual(['c', 'd']);

    act(() => {
      renderer.update(<Harness items={[{ key: 'a' }, { key: 'c' }]} pageItems={[{ key: 'a' }]} />);
    });
    expect(Array.from(ref.current!.selectedKeys)).toEqual(['c']);

    act(() => {
      ref.current!.exitSelectionMode();
    });
    expect(ref.current!.selectionMode).toBe(false);
    expect(ref.current!.selectedCount).toBe(0);

    act(() => renderer.unmount());
  });

  it('persists view mode to localStorage and falls back on compact viewports', () => {
    const storage = createMemoryStorage();
    vi.stubGlobal('window', { localStorage: storage });

    const ref: { current: UsePersistedViewModeResult<'table' | 'grid'> | null } = { current: null };

    function Harness({ isCompact }: { isCompact: boolean }) {
      const value = usePersistedViewMode<'table' | 'grid'>({
        storageKey: 'test.viewMode',
        defaultMode: 'table',
        validModes: ['table', 'grid'],
        compactFallbackMode: 'grid',
        isCompact,
      });
      useEffect(() => {
        ref.current = value;
      }, [value]);
      return null;
    }

    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(<Harness isCompact={false} />);
    });
    expect(ref.current!.mode).toBe('table');
    expect(ref.current!.effectiveMode).toBe('table');
    expect(ref.current!.switcherHidden).toBe(false);

    act(() => {
      ref.current!.setMode('grid');
    });
    expect(storage.getItem('test.viewMode')).toBe('grid');
    expect(ref.current!.effectiveMode).toBe('grid');

    act(() => {
      ref.current!.setMode('table');
      renderer.update(<Harness isCompact />);
    });
    expect(ref.current!.mode).toBe('table');
    expect(ref.current!.effectiveMode).toBe('grid');
    expect(ref.current!.switcherHidden).toBe(true);

    act(() => renderer.unmount());
  });
});
