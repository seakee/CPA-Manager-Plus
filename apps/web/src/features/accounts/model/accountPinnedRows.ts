/** Pinned selection keys in pin order; the first pinned credential stays first. */
export const normalizeAccountPinnedKeys = (value: unknown): string[] => {
  if (!Array.isArray(value)) return [];
  const keys: string[] = [];
  const seen = new Set<string>();
  value.forEach((item) => {
    if (typeof item !== 'string' || !item || seen.has(item)) return;
    seen.add(item);
    keys.push(item);
  });
  return keys;
};

/** Unpins an already pinned key; otherwise appends it after existing pins. */
export const toggleAccountPinnedKey = (keys: readonly string[], key: string): string[] =>
  keys.includes(key) ? keys.filter((item) => item !== key) : [...keys, key];

/**
 * Moves pinned rows to the front in pin order and keeps the remaining rows in
 * their existing order. Pins only reorder rows; they never add filtered-out rows.
 */
export const applyAccountPinnedOrder = <T extends { selectionKey: string }>(
  rows: readonly T[],
  pinnedKeys: readonly string[]
): T[] => {
  if (pinnedKeys.length === 0) return [...rows];
  const pinRank = new Map(pinnedKeys.map((key, index) => [key, index]));
  const pinned: T[] = [];
  const rest: T[] = [];
  rows.forEach((row) => {
    if (pinRank.has(row.selectionKey)) pinned.push(row);
    else rest.push(row);
  });
  if (pinned.length === 0) return rest;
  pinned.sort(
    (left, right) => (pinRank.get(left.selectionKey) ?? 0) - (pinRank.get(right.selectionKey) ?? 0)
  );
  return [...pinned, ...rest];
};
