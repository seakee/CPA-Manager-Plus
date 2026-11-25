import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

export interface UseClientPaginationOptions {
  initialPageSize?: number;
  pageSize?: number;
  onPageSizeChange?: (nextPageSize: number) => void;
  resetDeps?: readonly unknown[];
}

export interface UseClientPaginationResult<T> {
  page: number;
  setPage: (nextPage: number) => void;
  pageSize: number;
  setPageSize: (nextPageSize: number) => void;
  totalPages: number;
  totalCount: number;
  startItem: number;
  endItem: number;
  pageItems: T[];
}

export function useClientPagination<T>(
  items: readonly T[],
  options: UseClientPaginationOptions = {}
): UseClientPaginationResult<T> {
  const {
    initialPageSize = 20,
    pageSize: controlledPageSize,
    onPageSizeChange,
    resetDeps = [],
  } = options;

  const [uncontrolledPageSize, setUncontrolledPageSize] = useState(initialPageSize);
  const [page, setRawPage] = useState(1);
  const isControlledPageSize = controlledPageSize !== undefined;

  const pageSize =
    typeof controlledPageSize === 'number' && controlledPageSize > 0
      ? controlledPageSize
      : uncontrolledPageSize;

  const totalCount = items.length;
  const totalPages = Math.max(1, Math.ceil(totalCount / pageSize));
  const currentPage = Math.max(1, Math.min(page, totalPages));
  const pageStart = (currentPage - 1) * pageSize;
  const startItem = totalCount === 0 ? 0 : pageStart + 1;
  const endItem = Math.min(totalCount, pageStart + pageSize);

  const pageItems = useMemo(
    () => items.slice(pageStart, pageStart + pageSize),
    [items, pageStart, pageSize]
  );

  // Reset to page 1 when external filter/sort deps change (skip initial mount).
  const hasMountedRef = useRef(false);
  useEffect(() => {
    if (!hasMountedRef.current) {
      hasMountedRef.current = true;
      return;
    }
    setRawPage(1);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, resetDeps);

  // Keep raw page state clamped when item count shrinks below currentPage.
  useEffect(() => {
    if (page !== currentPage) {
      setRawPage(currentPage);
    }
  }, [currentPage, page]);

  const setPage = useCallback(
    (nextPage: number) => {
      const clamped = Number.isFinite(nextPage)
        ? Math.max(1, Math.min(Math.trunc(nextPage), totalPages))
        : 1;
      setRawPage(clamped);
    },
    [totalPages]
  );

  const setPageSize = useCallback(
    (nextPageSize: number) => {
      if (!Number.isFinite(nextPageSize) || nextPageSize <= 0) return;
      const normalized = Math.trunc(nextPageSize);
      if (!isControlledPageSize) {
        setUncontrolledPageSize(normalized);
      }
      onPageSizeChange?.(normalized);
      setRawPage(1);
    },
    [isControlledPageSize, onPageSizeChange]
  );

  return {
    page: currentPage,
    setPage,
    pageSize,
    setPageSize,
    totalPages,
    totalCount,
    startItem,
    endItem,
    pageItems,
  };
}
