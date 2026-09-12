import {
  ACCOUNT_STATUS_FILTERS,
  type AccountQuotaBand,
  type AccountRowSort,
  type AccountStatusFilter,
} from './accountRows';
import type { QuotaAccountDisplayMode } from '@/components/quota/quotaDisplay';

export type AccountOperationalFilter = 'all' | 'reauth' | 'cooldown' | 'automation' | 'recovered';
export type AccountsLayoutMode = 'table' | 'grid';

export interface AccountsWorkspaceUiState {
  search: string;
  providerFilter: string;
  statusFilter: AccountStatusFilter;
  planFilter: string;
  quotaBandFilter: AccountQuotaBand;
  operationalFilter: AccountOperationalFilter;
  accountSort: AccountRowSort;
  pageSize: number;
  accountDisplayMode: QuotaAccountDisplayMode;
  layoutMode: AccountsLayoutMode;
}

const STORAGE_KEY = 'cpa_manager_accounts_workspace_ui_v1';
const PAGE_SIZES = new Set([10, 20, 50]);
const SORT_KEYS = new Set([
  'default',
  'name',
  'plan',
  'note',
  'reset',
  'remaining',
  'priority',
  'recent',
  'quota',
  'created',
]);
const SORT_DIRECTIONS = new Set(['asc', 'desc']);
const STATUS_FILTERS = new Set<AccountStatusFilter>(ACCOUNT_STATUS_FILTERS);
const QUOTA_BANDS = new Set(['all', 'ge50', 'between20and50', 'lt20', 'spent']);
const OPERATIONAL_FILTERS = new Set(['all', 'reauth', 'cooldown', 'automation', 'recovered']);

export const DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE: AccountsWorkspaceUiState = {
  search: '',
  providerFilter: 'all',
  statusFilter: 'all',
  planFilter: 'all',
  quotaBandFilter: 'all',
  operationalFilter: 'all',
  accountSort: { key: 'recent', direction: 'desc' },
  pageSize: 10,
  accountDisplayMode: 'full',
  layoutMode: 'table',
};

const isRecord = (value: unknown): value is Record<string, unknown> =>
  Boolean(value) && typeof value === 'object' && !Array.isArray(value);

export const normalizeAccountsWorkspaceUiState = (value: unknown): AccountsWorkspaceUiState => {
  if (!isRecord(value)) return DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE;
  const sort = isRecord(value.accountSort) ? value.accountSort : {};
  const sortKey = SORT_KEYS.has(String(sort.key))
    ? (String(sort.key) as AccountRowSort['key'])
    : DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE.accountSort.key;
  const sortDirection = SORT_DIRECTIONS.has(String(sort.direction))
    ? (String(sort.direction) as AccountRowSort['direction'])
    : DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE.accountSort.direction;
  const pageSize = Number(value.pageSize);

  return {
    search: typeof value.search === 'string' ? value.search : '',
    providerFilter:
      typeof value.providerFilter === 'string' && value.providerFilter.trim()
        ? value.providerFilter
        : 'all',
    statusFilter: STATUS_FILTERS.has(String(value.statusFilter) as AccountStatusFilter)
      ? (String(value.statusFilter) as AccountStatusFilter)
      : 'all',
    planFilter:
      typeof value.planFilter === 'string' && value.planFilter.trim() ? value.planFilter : 'all',
    quotaBandFilter: QUOTA_BANDS.has(String(value.quotaBandFilter))
      ? (String(value.quotaBandFilter) as AccountQuotaBand)
      : 'all',
    operationalFilter: OPERATIONAL_FILTERS.has(String(value.operationalFilter))
      ? (String(value.operationalFilter) as AccountOperationalFilter)
      : 'all',
    accountSort: { key: sortKey, direction: sortDirection },
    pageSize: PAGE_SIZES.has(pageSize) ? pageSize : 10,
    accountDisplayMode: value.accountDisplayMode === 'masked' ? 'masked' : 'full',
    layoutMode: value.layoutMode === 'grid' ? 'grid' : 'table',
  };
};

export const readAccountsWorkspaceUiState = (): AccountsWorkspaceUiState => {
  if (typeof window === 'undefined' || !window.localStorage) {
    return DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE;
  }
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    return raw
      ? normalizeAccountsWorkspaceUiState(JSON.parse(raw) as unknown)
      : DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE;
  } catch {
    return DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE;
  }
};

export const writeAccountsWorkspaceUiState = (state: AccountsWorkspaceUiState): void => {
  if (typeof window === 'undefined' || !window.localStorage) return;
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(state));
  } catch {
    // UI preferences are best-effort only.
  }
};
