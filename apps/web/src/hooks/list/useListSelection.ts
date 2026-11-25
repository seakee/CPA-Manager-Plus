import { useCallback, useMemo, useState, type Dispatch, type SetStateAction } from 'react';

export interface UseListSelectionOptions<T> {
  items: readonly T[];
  pageItems: readonly T[];
  getKey: (item: T) => string;
  isSelectable?: (item: T) => boolean;
}

export interface UseListSelectionResult<T> {
  selectionMode: boolean;
  setSelectionMode: (next: boolean) => void;
  enterSelectionMode: () => void;
  exitSelectionMode: () => void;
  toggleSelectionMode: () => void;
  selectedKeys: Set<string>;
  setSelectedKeys: Dispatch<SetStateAction<Set<string>>>;
  selectedItems: T[];
  selectedCount: number;
  isSelected: (key: string) => boolean;
  toggleItem: (key: string) => void;
  togglePage: (checked: boolean) => void;
  selectPage: () => void;
  selectAll: () => void;
  invertSelection: (scope?: 'all' | 'page') => void;
  clearSelection: () => void;
  removeKeys: (keys: Iterable<string>) => void;
}

export function useListSelection<T>({
  items,
  pageItems,
  getKey,
  isSelectable,
}: UseListSelectionOptions<T>): UseListSelectionResult<T> {
  const [selectionMode, setSelectionMode] = useState(false);
  const [rawSelectedKeys, setRawSelectedKeys] = useState<Set<string>>(() => new Set());

  const canSelect = useCallback(
    (item: T) => (isSelectable ? isSelectable(item) : true),
    [isSelectable]
  );

  const validKeys = useMemo(() => {
    const keys = new Set<string>();
    for (const item of items) {
      if (canSelect(item)) {
        keys.add(getKey(item));
      }
    }
    return keys;
  }, [canSelect, getKey, items]);

  // Derive visible, selectable keys so filtered-out or deleted items are never acted upon.
  const selectedKeys = useMemo(() => {
    if (rawSelectedKeys.size === 0) return rawSelectedKeys;
    const pruned = new Set<string>();
    rawSelectedKeys.forEach((key) => {
      if (validKeys.has(key)) {
        pruned.add(key);
      }
    });
    return pruned.size === rawSelectedKeys.size ? rawSelectedKeys : pruned;
  }, [rawSelectedKeys, validKeys]);

  const setSelectedKeys: Dispatch<SetStateAction<Set<string>>> = useCallback(
    (updater) => {
      setRawSelectedKeys((prev) => {
        const prunedPrev = new Set<string>();
        prev.forEach((key) => {
          if (validKeys.has(key)) {
            prunedPrev.add(key);
          }
        });
        const nextRaw = typeof updater === 'function' ? updater(prunedPrev) : updater;
        const next = new Set<string>();
        nextRaw.forEach((key) => {
          if (validKeys.has(key)) {
            next.add(key);
          }
        });
        return next;
      });
    },
    [validKeys]
  );

  const selectedItems = useMemo(
    () => items.filter((item) => canSelect(item) && selectedKeys.has(getKey(item))),
    [canSelect, getKey, items, selectedKeys]
  );

  const isSelected = useCallback((key: string) => selectedKeys.has(key), [selectedKeys]);

  const enterSelectionMode = useCallback(() => {
    setSelectionMode(true);
  }, []);

  const exitSelectionMode = useCallback(() => {
    setSelectionMode(false);
    setRawSelectedKeys(new Set());
  }, []);

  const toggleSelectionMode = useCallback(() => {
    setSelectionMode((prev) => {
      if (prev) {
        setRawSelectedKeys(new Set());
      }
      return !prev;
    });
  }, []);

  const toggleItem = useCallback(
    (key: string) => {
      setSelectedKeys((prev) => {
        const next = new Set(prev);
        if (next.has(key)) {
          next.delete(key);
        } else {
          next.add(key);
        }
        return next;
      });
    },
    [setSelectedKeys]
  );

  const togglePage = useCallback(
    (checked: boolean) => {
      setSelectedKeys((prev) => {
        const next = new Set(prev);
        for (const item of pageItems) {
          if (!canSelect(item)) continue;
          const key = getKey(item);
          if (checked) {
            next.add(key);
          } else {
            next.delete(key);
          }
        }
        return next;
      });
    },
    [canSelect, getKey, pageItems, setSelectedKeys]
  );

  const selectPage = useCallback(() => {
    togglePage(true);
  }, [togglePage]);

  const selectAll = useCallback(() => {
    setRawSelectedKeys(new Set(validKeys));
  }, [validKeys]);

  const invertSelection = useCallback(
    (scope: 'all' | 'page' = 'all') => {
      const targetItems = scope === 'page' ? pageItems : items;
      setSelectedKeys((prev) => {
        const next = new Set(prev);
        for (const item of targetItems) {
          if (!canSelect(item)) continue;
          const key = getKey(item);
          if (next.has(key)) {
            next.delete(key);
          } else {
            next.add(key);
          }
        }
        return next;
      });
    },
    [canSelect, getKey, items, pageItems, setSelectedKeys]
  );

  const clearSelection = useCallback(() => {
    setRawSelectedKeys(new Set());
  }, []);

  const removeKeys = useCallback((keys: Iterable<string>) => {
    setRawSelectedKeys((prev) => {
      if (prev.size === 0) return prev;
      const toRemove = new Set(keys);
      let changed = false;
      const next = new Set<string>();
      prev.forEach((key) => {
        if (toRemove.has(key)) {
          changed = true;
        } else {
          next.add(key);
        }
      });
      return changed ? next : prev;
    });
  }, []);

  return {
    selectionMode,
    setSelectionMode,
    enterSelectionMode,
    exitSelectionMode,
    toggleSelectionMode,
    selectedKeys,
    setSelectedKeys,
    selectedItems,
    selectedCount: selectedItems.length,
    isSelected,
    toggleItem,
    togglePage,
    selectPage,
    selectAll,
    invertSelection,
    clearSelection,
    removeKeys,
  };
}
