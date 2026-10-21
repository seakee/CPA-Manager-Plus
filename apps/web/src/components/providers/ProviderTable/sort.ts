import type { ProviderKind, ProviderRow } from './rowData';

export type ProviderSortOption = 'priority' | 'name' | 'recent-success';
export type ProviderSortDirection = 'asc' | 'desc';
export type ProviderKindFilter = ProviderKind | 'all';

export interface FilterSortProviderRowsOptions {
  kind?: ProviderKindFilter;
  searchText?: string;
  selectedModels?: ReadonlySet<string>;
  sortOption?: ProviderSortOption;
  sortDirection?: ProviderSortDirection;
}

const getPriority = (row: ProviderRow) =>
  typeof row.priority === 'number' && Number.isFinite(row.priority) ? row.priority : 0;

const applyDirection = (value: number, direction: ProviderSortDirection) =>
  direction === 'desc' ? -value : value;

const matchesKind = (row: ProviderRow, kind: ProviderKindFilter) =>
  kind === 'all' || row.kind === kind;

const matchesSearch = (row: ProviderRow, searchText: string) =>
  !searchText || row.haystack.includes(searchText);

const matchesSelectedModels = (row: ProviderRow, selectedModels: ReadonlySet<string>) => {
  if (selectedModels.size === 0) return true;
  return row.modelNames.some((name) => selectedModels.has(name));
};

const getUsageStatsCoverageRank = (row: ProviderRow): number => {
  switch (row.usageStatsCoverage) {
    case 'full':
      return 0;
    case 'partial':
      return 1;
    case 'unavailable':
    default:
      return 2;
  }
};

const compareRows = (
  left: ProviderRow,
  right: ProviderRow,
  sortOption: ProviderSortOption,
  sortDirection: ProviderSortDirection
): number => {
  switch (sortOption) {
    case 'name': {
      const diff = left.sortName.localeCompare(right.sortName);
      if (diff !== 0) return applyDirection(diff, sortDirection);
      break;
    }
    case 'recent-success': {
      // Keep complete statistics ahead of partial/unknown statistics regardless
      // of sort direction. A keyless provider with unavailable accounting must
      // never be treated as a real numeric zero.
      const coverageDiff = getUsageStatsCoverageRank(left) - getUsageStatsCoverageRank(right);
      if (coverageDiff !== 0) return coverageDiff;
      const diff = left.recentSuccess - right.recentSuccess;
      if (diff !== 0) return applyDirection(diff, sortDirection);
      const nameDiff = left.sortName.localeCompare(right.sortName);
      if (nameDiff !== 0) return applyDirection(nameDiff, sortDirection);
      break;
    }
    case 'priority':
    default: {
      const diff = getPriority(left) - getPriority(right);
      if (diff !== 0) return applyDirection(diff, sortDirection);
      break;
    }
  }
  return 0;
};

/**
 * 过滤 + 排序统一行集合。
 * 停用配置始终排在启用配置之后；两组分别按同一排序规则比较
 * （与旧 Codex 区“停用组保持源顺序”不同，此处对停用组同样排序，行为更一致），
 * 平局时保持原始顺序（kind 维度按 buildProviderRows 的拼接顺序稳定）。
 */
export function filterAndSortProviderRows(
  rows: ProviderRow[],
  {
    kind = 'all',
    searchText = '',
    selectedModels = new Set<string>(),
    sortOption = 'priority',
    sortDirection = 'desc',
  }: FilterSortProviderRowsOptions = {}
): ProviderRow[] {
  const normalizedSearch = searchText.trim().toLowerCase();
  const filtered = rows.filter(
    (row) =>
      matchesKind(row, kind) &&
      matchesSearch(row, normalizedSearch) &&
      matchesSelectedModels(row, selectedModels)
  );

  const sourceOrder = new Map(filtered.map((row, index) => [row.key, index]));
  const byStableOrder = (left: ProviderRow, right: ProviderRow) =>
    (sourceOrder.get(left.key) ?? 0) - (sourceOrder.get(right.key) ?? 0);

  const sortGroup = (group: ProviderRow[]) =>
    [...group].sort((left, right) => {
      const diff = compareRows(left, right, sortOption, sortDirection);
      if (diff !== 0) return diff;
      return byStableOrder(left, right);
    });

  const enabled = filtered.filter((row) => row.enabled);
  const disabled = filtered.filter((row) => !row.enabled);

  return [...sortGroup(enabled), ...sortGroup(disabled)];
}
