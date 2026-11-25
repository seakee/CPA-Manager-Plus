import type { ProviderHealthFilter, ProviderStatusFilter } from '../types';
import type { ProviderKind, ProviderRow } from './rowData';

export type ProviderSortOption = 'priority' | 'name' | 'recent-success' | 'models';
export type ProviderSortDirection = 'asc' | 'desc';
export type ProviderKindFilter = ProviderKind | 'all';

export interface FilterSortProviderRowsOptions {
  kind?: ProviderKindFilter;
  statusFilter?: ProviderStatusFilter;
  healthFilter?: ProviderHealthFilter;
  searchText?: string;
  selectedModels?: ReadonlySet<string>;
  sortOption?: ProviderSortOption;
  sortDirection?: ProviderSortDirection;
}

const EMPTY_MODELS: ReadonlySet<string> = new Set();

const COVERAGE_RANK_BY_STATE: Record<ProviderRow['usageStatsCoverage'], number> = {
  full: 0,
  partial: 1,
  unavailable: 2,
};

const hasRecentWindowObservation = (row: ProviderRow): boolean => {
  if (row.history?.recentRequests && row.history.recentRequests.length > 0) {
    return true;
  }
  return row.recentSuccess + row.recentFailure > 0;
};

const rowMatchesHasFailure = (row: ProviderRow): boolean => {
  if (row.recentFailure > 0 || Boolean(row.latestFailure)) {
    return true;
  }
  if (hasRecentWindowObservation(row)) {
    return false;
  }
  return row.stats.failure > 0;
};

const compareRows = (
  left: ProviderRow,
  right: ProviderRow,
  sortOption: ProviderSortOption,
  sortDirection: ProviderSortDirection
): number => {
  const directional = (comparison: number) =>
    sortDirection === 'asc' ? comparison : -comparison;

  switch (sortOption) {
    case 'name':
      return directional(left.sortName.localeCompare(right.sortName));
    case 'recent-success': {
      // Keep complete statistics ahead of partial/unknown statistics regardless
      // of sort direction. A keyless provider with unavailable accounting must
      // never be treated as a real numeric zero.
      const leftRank = COVERAGE_RANK_BY_STATE[left.usageStatsCoverage] ?? 2;
      const rightRank = COVERAGE_RANK_BY_STATE[right.usageStatsCoverage] ?? 2;
      const coverageDiff = leftRank - rightRank;
      if (coverageDiff !== 0) return coverageDiff;
      const diff = left.recentSuccess - right.recentSuccess;
      if (diff !== 0) return directional(diff);
      return directional(left.sortName.localeCompare(right.sortName));
    }
    case 'models':
      return directional(left.modelCount - right.modelCount);
    case 'priority':
    default:
      return directional((left.priority ?? 0) - (right.priority ?? 0));
  }
};

/**
 * 对供应商配置行执行筛选与排序：
 * 停用配置始终排在启用配置之后；两组分别按同一排序规则比较；
 * 平局时保持原始顺序（kind 维度按 buildProviderRows 的拼接顺序稳定）。
 */
export function filterAndSortProviderRows(
  rows: ProviderRow[],
  options: FilterSortProviderRowsOptions = {}
): ProviderRow[] {
  const {
    kind = 'all',
    statusFilter = 'all',
    healthFilter = 'all',
    searchText = '',
    selectedModels = EMPTY_MODELS,
    sortOption = 'priority',
    sortDirection = 'desc',
  } = options;

  const normalizedSearch = searchText.trim().toLowerCase();

  const filtered = rows.filter((row) => {
    if (kind !== 'all' && row.kind !== kind) return false;
    if (statusFilter === 'enabled' && !row.enabled) return false;
    if (statusFilter === 'disabled' && row.enabled) return false;
    if (healthFilter === 'has_failure' && !rowMatchesHasFailure(row)) {
      return false;
    }
    if (healthFilter === 'active' && row.stats.success + row.stats.failure === 0) return false;
    if (healthFilter === 'idle' && row.stats.success + row.stats.failure > 0) return false;
    const filterKeys = row.modelFilterKeys ?? row.modelNames;
    if (selectedModels.size > 0 && !filterKeys.some((model) => selectedModels.has(model))) {
      return false;
    }
    if (normalizedSearch && !row.haystack.includes(normalizedSearch)) return false;
    return true;
  });

  const sortGroup = (group: ProviderRow[]) =>
    group
      .map((row, index) => ({ row, index }))
      .sort((left, right) => {
        const diff = compareRows(left.row, right.row, sortOption, sortDirection);
        return diff !== 0 ? diff : left.index - right.index;
      })
      .map((item) => item.row);

  const enabled = filtered.filter((row) => row.enabled);
  const disabled = filtered.filter((row) => !row.enabled);

  return [...sortGroup(enabled), ...sortGroup(disabled)];
}
