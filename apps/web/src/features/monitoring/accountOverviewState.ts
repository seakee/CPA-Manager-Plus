import type { AuthFileItem } from '@/types';
import type { SourceProviderEnabledState } from '@/types/sourceInfo';
import { isDisabledAuthFile } from '@/utils/quota';
import { normalizeRecentRequestAuthIndex, type StatusBarData } from '@/utils/recentRequests';
import type {
  MonitoringAccountRow,
  MonitoringEventRow,
  MonitoringTimeRange,
} from './hooks/useMonitoringData';

export type MonitoringAccountOverviewMode = 'table' | 'card';
export type AccountDisplayMode = 'masked' | 'full';

export type AccountSortKey =
  | 'totalCalls'
  | 'successCalls'
  | 'failureCalls'
  | 'successRate'
  | 'totalTokens'
  | 'inputTokens'
  | 'outputTokens'
  | 'cachedTokens'
  | 'totalCost'
  | 'lastSeenAt';

export type AccountSortDirection = 'asc' | 'desc';

export type AccountSortState = {
  key: AccountSortKey;
  direction: AccountSortDirection;
};

export const ACCOUNT_OVERVIEW_MODE_STORAGE_KEY = 'monitoring.accountOverviewMode';
export const ACCOUNT_OVERVIEW_UI_STATE_STORAGE_KEY = 'monitoring.accountOverviewUiState';
export const ACCOUNT_OVERVIEW_TABLE_PAGE_SIZE_OPTIONS = [12, 20, 50, 100] as const;
export const ACCOUNT_OVERVIEW_CARD_PAGE_SIZE_OPTIONS = [12, 18, 24, 36] as const;

export const ACCOUNT_OVERVIEW_CARD_METRIC_KEYS = [
  'total-tokens',
  'input-tokens',
  'output-tokens',
  'cached-tokens',
] as const;
const DEFAULT_ACCOUNT_OVERVIEW_CARD_PAGINATION = {
  page: 1,
  pageSize: ACCOUNT_OVERVIEW_CARD_PAGE_SIZE_OPTIONS[0],
} as const;

export const DEFAULT_ACCOUNT_SORT: AccountSortState = {
  key: 'lastSeenAt',
  direction: 'desc',
};

export type MonitoringAccountEnabledState = 'enabled' | 'disabled' | 'mixed' | 'unavailable';
export type MonitoringAccountOverviewCardPaginationState = {
  page: number;
  pageSize: number;
};
export type AccountOverviewPageResetState = {
  customEndInput: string;
  customStartInput: string;
  deferredSearch: string;
  selectedAccount: string;
  selectedApiKeyHash: string;
  selectedChannel: string;
  selectedHeaderTraceId: string;
  selectedModel: string;
  selectedProvider: string;
  selectedStatus: string;
  timeRange: MonitoringTimeRange;
};
export type MonitoringAccountOverviewUiState = {
  mode: MonitoringAccountOverviewMode;
  accountDisplayMode: AccountDisplayMode;
  sort: AccountSortState;
  cardPagination: MonitoringAccountOverviewCardPaginationState;
};

export type MonitoringAccountAuthState = {
  files: AuthFileItem[];
  enabledState: MonitoringAccountEnabledState;
};

export type MonitoringStatusRangeBounds = {
  startMs: number;
  endMs: number;
};

const ACCOUNT_SORT_KEYS = [
  'totalCalls',
  'successCalls',
  'failureCalls',
  'successRate',
  'totalTokens',
  'inputTokens',
  'outputTokens',
  'cachedTokens',
  'totalCost',
  'lastSeenAt',
] as const;
const ACCOUNT_SORT_KEY_SET = new Set<AccountSortKey>(ACCOUNT_SORT_KEYS);
const ACCOUNT_SORT_DIRECTION_SET = new Set<AccountSortDirection>(['asc', 'desc']);
const ACCOUNT_DISPLAY_MODE_SET = new Set<AccountDisplayMode>(['masked', 'full']);

export const normalizeAccountOverviewMode = (value: unknown): MonitoringAccountOverviewMode =>
  value === 'card' ? 'card' : 'table';

export const normalizeAccountDisplayMode = (value: unknown): AccountDisplayMode =>
  typeof value === 'string' && ACCOUNT_DISPLAY_MODE_SET.has(value as AccountDisplayMode)
    ? (value as AccountDisplayMode)
    : 'masked';

export const normalizeAccountSortKey = (value: unknown): AccountSortKey | null =>
  typeof value === 'string' && ACCOUNT_SORT_KEY_SET.has(value as AccountSortKey)
    ? (value as AccountSortKey)
    : null;

export const normalizeAccountSortDirection = (value: unknown): AccountSortDirection | null =>
  typeof value === 'string' && ACCOUNT_SORT_DIRECTION_SET.has(value as AccountSortDirection)
    ? (value as AccountSortDirection)
    : null;

export const normalizeAccountSortState = (value: unknown): AccountSortState => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return DEFAULT_ACCOUNT_SORT;
  }

  const record = value as Record<string, unknown>;
  const key = normalizeAccountSortKey(record.key);
  const direction = normalizeAccountSortDirection(record.direction);

  if (!key || !direction) {
    return DEFAULT_ACCOUNT_SORT;
  }

  return {
    key,
    direction,
  };
};

export const normalizeAccountOverviewPageSize = (
  value: number,
  mode: MonitoringAccountOverviewMode
) => {
  const options: readonly number[] =
    mode === 'card'
      ? ACCOUNT_OVERVIEW_CARD_PAGE_SIZE_OPTIONS
      : ACCOUNT_OVERVIEW_TABLE_PAGE_SIZE_OPTIONS;
  return options.includes(value) ? value : options[0];
};

const normalizeStoredPage = (value: unknown) => {
  if (typeof value === 'number' && Number.isFinite(value) && value >= 1) {
    return Math.floor(value);
  }

  if (typeof value === 'string') {
    const parsed = Number.parseInt(value, 10);
    if (Number.isFinite(parsed) && parsed >= 1) {
      return parsed;
    }
  }

  return DEFAULT_ACCOUNT_OVERVIEW_CARD_PAGINATION.page;
};

export const normalizeAccountOverviewCardPaginationState = (
  value: unknown
): MonitoringAccountOverviewCardPaginationState => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return { ...DEFAULT_ACCOUNT_OVERVIEW_CARD_PAGINATION };
  }

  const record = value as Record<string, unknown>;
  return {
    page: normalizeStoredPage(record.page),
    pageSize: normalizeAccountOverviewPageSize(normalizeStoredPage(record.pageSize), 'card'),
  };
};

export const normalizeAccountOverviewUiState = (
  value: unknown
): MonitoringAccountOverviewUiState => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return {
      mode: 'table',
      accountDisplayMode: 'masked',
      sort: DEFAULT_ACCOUNT_SORT,
      cardPagination: { ...DEFAULT_ACCOUNT_OVERVIEW_CARD_PAGINATION },
    };
  }

  const record = value as Record<string, unknown>;
  return {
    mode: normalizeAccountOverviewMode(record.mode),
    accountDisplayMode: normalizeAccountDisplayMode(record.accountDisplayMode),
    sort: normalizeAccountSortState(record.sort),
    cardPagination: normalizeAccountOverviewCardPaginationState(record.cardPagination),
  };
};

const hasReadableAccountValue = (value: string | null | undefined) => {
  const trimmed = String(value || '').trim();
  return Boolean(trimmed) && trimmed !== '-';
};

const firstReadableAccountValue = (...values: Array<string | null | undefined>) =>
  values.find(hasReadableAccountValue)?.trim() || '';

const isRedundantAccountLabel = (
  candidate: string | null | undefined,
  primary: string | null | undefined
) => {
  const candidateValue = String(candidate || '').trim();
  const primaryValue = String(primary || '').trim();
  if (!candidateValue || !primaryValue) return false;
  if (candidateValue === primaryValue) return true;
  const lowerCandidate = candidateValue.toLowerCase();
  const lowerPrimary = primaryValue.toLowerCase();
  return (
    lowerPrimary.startsWith(`${lowerCandidate} #`) || lowerCandidate.startsWith(`${lowerPrimary} #`)
  );
};

export const resolveAccountDisplayText = (
  row: Pick<
    MonitoringAccountRow,
    'account' | 'accountMasked' | 'displayAccount' | 'authLabels' | 'channels'
  >,
  displayMode: AccountDisplayMode
) => {
  const fullAccount = firstReadableAccountValue(row.account, row.displayAccount);
  const maskedAccount =
    displayMode === 'masked'
      ? firstReadableAccountValue(row.accountMasked, row.account, row.displayAccount)
      : fullAccount;
  const configuredPrimary = firstReadableAccountValue(row.displayAccount, row.account);
  const primaryIsAccount =
    !configuredPrimary ||
    configuredPrimary === row.account ||
    configuredPrimary === row.accountMasked ||
    configuredPrimary === fullAccount;
  const primary = primaryIsAccount
    ? firstReadableAccountValue(maskedAccount, fullAccount, configuredPrimary, '-')
    : configuredPrimary;
  const secondaryCandidates = primaryIsAccount
    ? [
        ...row.authLabels.filter((label) => label && label !== primary && label !== fullAccount),
        ...row.channels.filter((label) => label && label !== '-' && label !== primary),
      ]
    : [maskedAccount, fullAccount];
  const secondary =
    secondaryCandidates.find(
      (value) =>
        hasReadableAccountValue(value) &&
        value !== primary &&
        !isRedundantAccountLabel(value, primary)
    ) || '';
  const titleParts = Array.from(
    new Set([primary, fullAccount, maskedAccount, secondary].filter(hasReadableAccountValue))
  );

  return {
    primary,
    secondary,
    fullAccount: fullAccount || primary,
    title: titleParts.join(' / ') || primary,
  };
};

export const shouldResetAccountOverviewPage = (
  previous: AccountOverviewPageResetState | null,
  next: AccountOverviewPageResetState
) => {
  if (!previous) {
    return false;
  }

  return (
    previous.customEndInput !== next.customEndInput ||
    previous.customStartInput !== next.customStartInput ||
    previous.deferredSearch !== next.deferredSearch ||
    previous.selectedAccount !== next.selectedAccount ||
    previous.selectedApiKeyHash !== next.selectedApiKeyHash ||
    previous.selectedChannel !== next.selectedChannel ||
    previous.selectedHeaderTraceId !== next.selectedHeaderTraceId ||
    previous.selectedModel !== next.selectedModel ||
    previous.selectedProvider !== next.selectedProvider ||
    previous.selectedStatus !== next.selectedStatus ||
    previous.timeRange !== next.timeRange
  );
};

export const shouldClampAccountOverviewPage = (
  loading: boolean,
  currentPage: number,
  nextPage: number
) => !loading && currentPage !== nextPage;

const getAccountSortValue = (row: MonitoringAccountRow, key: AccountSortKey) => {
  switch (key) {
    case 'totalCalls':
      return row.totalCalls;
    case 'successCalls':
      return row.successCalls;
    case 'failureCalls':
      return row.failureCalls;
    case 'successRate':
      return row.successRate;
    case 'totalTokens':
      return row.totalTokens;
    case 'inputTokens':
      return row.inputTokens;
    case 'outputTokens':
      return row.outputTokens;
    case 'cachedTokens':
      return row.cachedTokens;
    case 'totalCost':
      return row.totalCost;
    case 'lastSeenAt':
    default:
      return row.lastSeenAt;
  }
};

export const compareAccountRowsByDefault = (
  left: MonitoringAccountRow,
  right: MonitoringAccountRow
) =>
  right.lastSeenAt - left.lastSeenAt ||
  right.totalCalls - left.totalCalls ||
  right.totalCost - left.totalCost ||
  left.account.localeCompare(right.account);

export const sortAccountRows = (
  rows: MonitoringAccountRow[],
  sortState: AccountSortState = DEFAULT_ACCOUNT_SORT
) => {
  const directionFactor = sortState.direction === 'desc' ? -1 : 1;

  return [...rows].sort((left, right) => {
    const valueDiff =
      getAccountSortValue(left, sortState.key) - getAccountSortValue(right, sortState.key);
    if (valueDiff !== 0) {
      return valueDiff * directionFactor;
    }

    return compareAccountRowsByDefault(left, right);
  });
};

const readStoredModeValue = () => {
  if (typeof window === 'undefined' || typeof window.localStorage === 'undefined') {
    return null;
  }

  try {
    const raw = window.localStorage.getItem(ACCOUNT_OVERVIEW_MODE_STORAGE_KEY);
    if (!raw) return null;

    return JSON.parse(raw);
  } catch {
    try {
      return window.localStorage.getItem(ACCOUNT_OVERVIEW_MODE_STORAGE_KEY);
    } catch {
      return null;
    }
  }
};

export const readAccountOverviewMode = (): MonitoringAccountOverviewMode =>
  normalizeAccountOverviewMode(readStoredModeValue());

export const readAccountOverviewUiState = (): MonitoringAccountOverviewUiState => {
  if (typeof window === 'undefined' || typeof window.localStorage === 'undefined') {
    return {
      mode: 'table',
      accountDisplayMode: 'masked',
      sort: DEFAULT_ACCOUNT_SORT,
      cardPagination: { ...DEFAULT_ACCOUNT_OVERVIEW_CARD_PAGINATION },
    };
  }

  try {
    const raw = window.localStorage.getItem(ACCOUNT_OVERVIEW_UI_STATE_STORAGE_KEY);
    if (raw) {
      return normalizeAccountOverviewUiState(JSON.parse(raw));
    }
  } catch {
    // Ignore storage failures and fall back to legacy mode key.
  }

  return {
    mode: readAccountOverviewMode(),
    accountDisplayMode: 'masked',
    sort: DEFAULT_ACCOUNT_SORT,
    cardPagination: { ...DEFAULT_ACCOUNT_OVERVIEW_CARD_PAGINATION },
  };
};

export const writeAccountOverviewMode = (mode: MonitoringAccountOverviewMode) => {
  if (typeof window === 'undefined' || typeof window.localStorage === 'undefined') {
    return;
  }

  try {
    window.localStorage.setItem(
      ACCOUNT_OVERVIEW_MODE_STORAGE_KEY,
      JSON.stringify(normalizeAccountOverviewMode(mode))
    );
  } catch {
    // Ignore storage failures and keep the runtime mode in memory only.
  }
};

export const writeAccountOverviewUiState = (state: MonitoringAccountOverviewUiState) => {
  if (typeof window === 'undefined' || typeof window.localStorage === 'undefined') {
    return;
  }

  const normalizedState = normalizeAccountOverviewUiState(state);

  try {
    window.localStorage.setItem(
      ACCOUNT_OVERVIEW_UI_STATE_STORAGE_KEY,
      JSON.stringify(normalizedState)
    );
  } catch {
    // Ignore storage failures and keep the runtime state in memory only.
  }

  writeAccountOverviewMode(normalizedState.mode);
};

const isRuntimeOnlyAuthFile = (file: AuthFileItem) => {
  const value = file.runtimeOnly ?? file['runtime_only'];
  if (typeof value === 'boolean') return value;
  if (typeof value === 'string') return value.trim().toLowerCase() === 'true';
  return false;
};

const STATUS_BLOCK_COUNT = 20;

export const resolveMonitoringStatusRangeBounds = (
  rows: Pick<MonitoringEventRow, 'timestampMs'>[],
  bounds: MonitoringStatusRangeBounds | null | undefined
): MonitoringStatusRangeBounds | null => {
  if (!bounds || !Number.isFinite(bounds.endMs)) {
    return null;
  }

  if (Number.isFinite(bounds.startMs)) {
    return bounds;
  }

  const startMs = rows.reduce((earliest, row) => {
    if (row.timestampMs > bounds.endMs) {
      return earliest;
    }

    return Math.min(earliest, row.timestampMs);
  }, Number.POSITIVE_INFINITY);

  return {
    startMs: Number.isFinite(startMs) ? startMs : bounds.endMs,
    endMs: bounds.endMs,
  };
};

export const buildEmptyMonitoringStatusData = (
  bounds: MonitoringStatusRangeBounds
): StatusBarData => {
  const spanMs = Math.max(bounds.endMs - bounds.startMs + 1, STATUS_BLOCK_COUNT);
  const blockDetails = Array.from({ length: STATUS_BLOCK_COUNT }, (_, index) => {
    const blockStartTime = Math.floor(bounds.startMs + (spanMs * index) / STATUS_BLOCK_COUNT);
    const nextBlockStartTime =
      index === STATUS_BLOCK_COUNT - 1
        ? bounds.endMs + 1
        : Math.floor(bounds.startMs + (spanMs * (index + 1)) / STATUS_BLOCK_COUNT);

    return {
      success: 0,
      failure: 0,
      rate: -1,
      startTime: blockStartTime,
      endTime: Math.max(blockStartTime, nextBlockStartTime - 1),
    };
  });

  return {
    blocks: Array.from({ length: STATUS_BLOCK_COUNT }, () => 'idle'),
    blockDetails,
    successRate: 100,
    totalSuccess: 0,
    totalFailure: 0,
  };
};

const clampStatusBucketIndex = (timestampMs: number, bounds: MonitoringStatusRangeBounds) => {
  const spanMs = Math.max(bounds.endMs - bounds.startMs + 1, 1);
  const offset = Math.min(Math.max(timestampMs - bounds.startMs, 0), spanMs - 1);
  return Math.min(STATUS_BLOCK_COUNT - 1, Math.floor((offset * STATUS_BLOCK_COUNT) / spanMs));
};

const buildStatusDataForRows = (
  rows: MonitoringEventRow[],
  bounds: MonitoringStatusRangeBounds
): StatusBarData => {
  const statusData = buildEmptyMonitoringStatusData(bounds);

  rows.forEach((row) => {
    if (row.timestampMs < bounds.startMs || row.timestampMs > bounds.endMs) {
      return;
    }

    const bucketIndex = clampStatusBucketIndex(row.timestampMs, bounds);
    const detail = statusData.blockDetails[bucketIndex];

    if (row.failed) {
      detail.failure += 1;
      statusData.totalFailure += 1;
    } else {
      detail.success += 1;
      statusData.totalSuccess += 1;
    }
  });

  statusData.blocks = statusData.blockDetails.map((detail) => {
    const total = detail.success + detail.failure;
    if (total === 0) {
      detail.rate = -1;
      return 'idle';
    }

    detail.rate = detail.success / total;
    if (detail.failure === 0) return 'success';
    if (detail.success === 0) return 'failure';
    return 'mixed';
  });

  const total = statusData.totalSuccess + statusData.totalFailure;
  statusData.successRate = total > 0 ? (statusData.totalSuccess / total) * 100 : 100;

  return statusData;
};

export const buildMonitoringAccountStatusDataMap = (
  rows: MonitoringEventRow[],
  bounds: MonitoringStatusRangeBounds | null | undefined
) => {
  const resolvedBounds = resolveMonitoringStatusRangeBounds(rows, bounds);
  const grouped = new Map<string, MonitoringEventRow[]>();

  if (!resolvedBounds) {
    return new Map<string, StatusBarData>();
  }

  rows.forEach((row) => {
    if (row.timestampMs < resolvedBounds.startMs || row.timestampMs > resolvedBounds.endMs) {
      return;
    }

    const accountKey = row.account || row.authLabel || row.source;
    const existing = grouped.get(accountKey) ?? [];
    existing.push(row);
    grouped.set(accountKey, existing);
  });

  return new Map(
    Array.from(grouped.entries()).map(([accountKey, accountRows]) => [
      accountKey,
      buildStatusDataForRows(accountRows, resolvedBounds),
    ])
  );
};

export const buildMonitoringAccountAuthStateMap = (
  rows: MonitoringAccountRow[],
  authFilesByAuthIndex: Map<string, AuthFileItem>,
  sourceProviderStateBySourceKey: Map<string, SourceProviderEnabledState> = new Map()
) =>
  new Map(
    rows.map(
      (row) =>
        [
          row.id,
          buildMonitoringAccountAuthState(
            row.authIndices,
            authFilesByAuthIndex,
            row.sourceKeys ?? [],
            sourceProviderStateBySourceKey
          ),
        ] as const
    )
  );

export const buildMonitoringAccountAuthState = (
  authIndices: string[],
  authFilesByAuthIndex: Map<string, AuthFileItem>,
  sourceKeys: string[] = [],
  sourceProviderStateBySourceKey: Map<string, SourceProviderEnabledState> = new Map()
): MonitoringAccountAuthState => {
  const files = Array.from(
    authIndices.reduce<Map<string, AuthFileItem>>((map, authIndex) => {
      const normalizedAuthIndex = normalizeRecentRequestAuthIndex(authIndex);
      if (!normalizedAuthIndex) return map;

      const file = authFilesByAuthIndex.get(normalizedAuthIndex);
      if (!file || map.has(file.name)) return map;

      map.set(file.name, file);
      return map;
    }, new Map())
  )
    .map(([, file]) => file)
    .sort((left, right) => left.name.localeCompare(right.name));

  const toggleableFiles = files.filter((file) => !isRuntimeOnlyAuthFile(file));
  const enabledStates: SourceProviderEnabledState[] = [
    ...toggleableFiles.map((file) => (isDisabledAuthFile(file) ? 'disabled' : 'enabled')),
    ...sourceKeys.flatMap((sourceKey) => {
      const providerState = sourceProviderStateBySourceKey.get(sourceKey);
      return providerState ? [providerState] : [];
    }),
  ];
  const enabledState: MonitoringAccountEnabledState =
    enabledStates.length === 0
      ? 'unavailable'
      : enabledStates.every((state) => state === 'enabled')
        ? 'enabled'
        : enabledStates.every((state) => state === 'disabled')
          ? 'disabled'
          : 'mixed';

  return {
    files,
    enabledState,
  };
};
