import type { ProviderSortOption } from '@/components/providers/ProviderTable/sort';
import type { ProviderLayoutMode } from '@/components/providers/types';
import type { SortDirection } from '@/components/ui/SortControl';

export const AI_PROVIDERS_WORKSPACE_UI_STORAGE_KEY = 'cpamp.aiProviders.workspaceUi.v1';

export const PROVIDER_WORKSPACE_PAGE_SIZE_OPTIONS = [10, 20, 50] as const;

export interface AiProvidersWorkspaceUiState {
  layoutMode: ProviderLayoutMode;
  pageSize: number;
  sortOption: ProviderSortOption;
  sortDirection: SortDirection;
}

export const DEFAULT_AI_PROVIDERS_WORKSPACE_UI_STATE: AiProvidersWorkspaceUiState = {
  layoutMode: 'table',
  pageSize: 10,
  sortOption: 'priority',
  sortDirection: 'desc',
};

const VALID_LAYOUT_MODES: Record<ProviderLayoutMode, true> = {
  table: true,
  grid: true,
};

const VALID_SORT_OPTIONS: Record<ProviderSortOption, true> = {
  priority: true,
  name: true,
  'recent-success': true,
  models: true,
};

const VALID_SORT_DIRECTIONS: Record<SortDirection, true> = {
  asc: true,
  desc: true,
};

export const normalizeAiProvidersWorkspaceUiState = (
  value: unknown
): AiProvidersWorkspaceUiState => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return { ...DEFAULT_AI_PROVIDERS_WORKSPACE_UI_STATE };
  }

  const record = value as Record<string, unknown>;
  const layoutMode =
    typeof record.layoutMode === 'string' &&
    Object.prototype.hasOwnProperty.call(VALID_LAYOUT_MODES, record.layoutMode)
      ? (record.layoutMode as ProviderLayoutMode)
      : DEFAULT_AI_PROVIDERS_WORKSPACE_UI_STATE.layoutMode;

  const pageSize =
    typeof record.pageSize === 'number' &&
    Number.isFinite(record.pageSize) &&
    (PROVIDER_WORKSPACE_PAGE_SIZE_OPTIONS as readonly number[]).includes(record.pageSize)
      ? record.pageSize
      : DEFAULT_AI_PROVIDERS_WORKSPACE_UI_STATE.pageSize;

  const sortOption =
    typeof record.sortOption === 'string' &&
    Object.prototype.hasOwnProperty.call(VALID_SORT_OPTIONS, record.sortOption)
      ? (record.sortOption as ProviderSortOption)
      : DEFAULT_AI_PROVIDERS_WORKSPACE_UI_STATE.sortOption;

  const sortDirection =
    typeof record.sortDirection === 'string' &&
    Object.prototype.hasOwnProperty.call(VALID_SORT_DIRECTIONS, record.sortDirection)
      ? (record.sortDirection as SortDirection)
      : DEFAULT_AI_PROVIDERS_WORKSPACE_UI_STATE.sortDirection;

  return {
    layoutMode,
    pageSize,
    sortOption,
    sortDirection,
  };
};

export const readAiProvidersWorkspaceUiState = (): AiProvidersWorkspaceUiState => {
  if (typeof window === 'undefined' || !window.localStorage) {
    return { ...DEFAULT_AI_PROVIDERS_WORKSPACE_UI_STATE };
  }

  try {
    const raw = window.localStorage.getItem(AI_PROVIDERS_WORKSPACE_UI_STORAGE_KEY);
    if (!raw) {
      return { ...DEFAULT_AI_PROVIDERS_WORKSPACE_UI_STATE };
    }
    return normalizeAiProvidersWorkspaceUiState(JSON.parse(raw));
  } catch {
    return { ...DEFAULT_AI_PROVIDERS_WORKSPACE_UI_STATE };
  }
};

export const writeAiProvidersWorkspaceUiState = (state: AiProvidersWorkspaceUiState): void => {
  if (typeof window === 'undefined' || !window.localStorage) {
    return;
  }

  try {
    window.localStorage.setItem(
      AI_PROVIDERS_WORKSPACE_UI_STORAGE_KEY,
      JSON.stringify(normalizeAiProvidersWorkspaceUiState(state))
    );
  } catch {
    // Ignore storage quota or private browsing errors
  }
};
