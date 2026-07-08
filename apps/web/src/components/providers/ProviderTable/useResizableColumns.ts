import { useCallback, useMemo, useRef, useState } from 'react';

const STORAGE_KEY = 'providerTable.columnWidths';

const COLUMN_DEFAULTS = [
  '92px',
  'minmax(112px, 0.72fr)',
  'minmax(144px, 0.96fr)',
  '80px',
  '76px',
  '360px',
  '60px',
  '108px',
];

const RESIZABLE_MIN_WIDTHS: Record<number, number> = {
  1: 112,
  2: 144,
};

function loadWidths(): Record<number, number> {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) return parsed;
  } catch {
    /* ignore */
  }
  return {};
}

function saveWidths(widths: Record<number, number>) {
  try {
    if (Object.keys(widths).length === 0) {
      localStorage.removeItem(STORAGE_KEY);
    } else {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(widths));
    }
  } catch {
    /* ignore */
  }
}

export function useResizableColumns() {
  const [widths, setWidths] = useState(loadWidths);
  const dragRef = useRef<{ col: number; startX: number; startW: number } | null>(null);

  const gridTemplateColumns = useMemo(() => {
    const parts = [...COLUMN_DEFAULTS];
    for (const [col, w] of Object.entries(widths)) {
      const i = Number(col);
      if (i in RESIZABLE_MIN_WIDTHS) parts[i] = `${w}px`;
    }
    return parts.join(' ');
  }, [widths]);

  const onResizeStart = useCallback(
    (colIndex: number, e: React.PointerEvent<HTMLDivElement>) => {
      if (!(colIndex in RESIZABLE_MIN_WIDTHS)) return;
      e.preventDefault();
      e.stopPropagation();

      const headerCell = e.currentTarget.parentElement as HTMLElement | null;
      if (!headerCell) return;
      const startW = headerCell.getBoundingClientRect().width;

      dragRef.current = { col: colIndex, startX: e.clientX, startW };
      document.body.style.cursor = 'col-resize';
      document.body.style.userSelect = 'none';

      const onMove = (ev: PointerEvent) => {
        const state = dragRef.current;
        if (!state) return;
        const minW = RESIZABLE_MIN_WIDTHS[state.col] ?? 80;
        const newW = Math.round(Math.max(minW, state.startW + ev.clientX - state.startX));
        setWidths((prev) => ({ ...prev, [state.col]: newW }));
      };

      const onUp = () => {
        dragRef.current = null;
        document.body.style.cursor = '';
        document.body.style.userSelect = '';
        document.removeEventListener('pointermove', onMove);
        document.removeEventListener('pointerup', onUp);
        setWidths((prev) => {
          saveWidths(prev);
          return prev;
        });
      };

      document.addEventListener('pointermove', onMove);
      document.addEventListener('pointerup', onUp);
    },
    []
  );

  const resetColumn = useCallback((colIndex: number) => {
    setWidths((prev) => {
      const next = { ...prev };
      delete next[colIndex];
      saveWidths(next);
      return next;
    });
  }, []);

  const isResizable = useCallback((colIndex: number) => colIndex in RESIZABLE_MIN_WIDTHS, []);

  return { gridTemplateColumns, onResizeStart, resetColumn, isResizable };
}
