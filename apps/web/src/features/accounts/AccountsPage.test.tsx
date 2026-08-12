import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { isValidElement, StrictMode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Button } from '@/components/ui/Button';
import { Drawer } from '@/components/ui/Drawer';
import { DropdownMenu } from '@/components/ui/DropdownMenu';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { ProviderStatusBar } from '@/components/providers/ProviderStatusBar';
import { CODEX_CONFIG } from '@/components/quota';
import { accountQuotaSnapshotApi } from '@/services/api';
import type { AuthFileItem, CodexQuotaState, OAuthModelAliasEntry } from '@/types';
import type {
  AccountActionCandidatesResponse,
  AccountQuotaSnapshotWriteEntry,
  CodexInspectionResult,
  CodexInspectionRun,
  QuotaCooldownInfo,
  UsageHeaderSnapshot,
} from '@/services/api/usageService';
import { copyToClipboard } from '@/utils/clipboard';
import {
  buildQuotaCredentialIdentity,
  getQuotaCredentialStoreKey,
} from '@/utils/quota/credentialScope';
import {
  getAuthFilePatchTarget,
  getAuthFileSelectionKey,
} from '@/features/authFiles/model/authFilesPageModel';
import type { CodexQuotaData } from '@/utils/quota/providerRequests';
import { AccountDiagnosticsTab } from './components/accountDetail/AccountDiagnosticsTab';
import { AccountModelsTab } from './components/accountDetail/AccountModelsTab';
import { AccountQuotaTab } from './components/accountDetail/AccountQuotaTab';
import { QuotaWindowCard } from './components/QuotaWindowCard';
import { formatQuotaResetTimestamp } from './model/accountsPagePresentation';
import { useUsageHeaderSnapshotStore } from '@/stores/useUsageHeaderSnapshotStore';
import { AccountsPage } from './AccountsPage';

type AnalyticsRequestForTest = {
  from_ms?: number;
  to_ms?: number;
  filters?: {
    auth_files?: string[];
    auth_indices?: string[];
  };
  include?: {
    events_page?: unknown;
    summary?: boolean;
    summary_profile?: 'full' | 'compact';
    summary_percentiles?: boolean;
    recent_failures?: number;
    account_stats?: boolean;
  };
};

type AnalyticsResponseForTest = {
  generated_at_ms: number;
  granularity: string;
  summary?: {
    total_calls: number;
    success_calls: number;
    failure_calls: number;
    success_rate: number;
    input_tokens: number;
    output_tokens: number;
    total_tokens: number;
    total_cost: number;
    p95_latency_ms?: number | null;
  };
  recent_failures?: Array<{
    timestamp_ms: number;
    model: string;
    fail_status_code?: number | null;
    fail_summary?: string;
    header_error_kind?: string;
    header_error_code?: string;
  }>;
  events?: {
    items: Array<Record<string, unknown>>;
    next_before_ms: number;
    next_before_id?: number;
    has_more: boolean;
    total_count?: number;
  };
  account_stats?: unknown[];
  timeline?: unknown[];
};

type HeaderSnapshotsResponseForTest = {
  generated_at_ms: number;
  from_ms: number;
  to_ms: number;
  items: UsageHeaderSnapshot[];
};

type AccountHistoryResponseForTest = {
  generated_at_ms: number;
  checkpoint: {
    last_event_id: number;
    latest_id: number;
    pending: boolean;
    processed: number;
  };
  items: Array<{
    row_key: string;
    account_key: string;
    matched: boolean;
    total_requests: number;
    success_calls: number;
    failure_calls: number;
    total_tokens: number;
    total_cost: number;
    success_rate: number | null;
    first_seen_ms: number | null;
    last_seen_ms: number | null;
    latest_request?: {
      timestamp_ms: number;
      failed: boolean;
      fail_status_code?: number | null;
      fail_summary?: string;
      header_error_kind?: string;
      header_error_code?: string;
      header_trace_id?: string;
    } | null;
    recent_requests?: Array<{
      timestamp_ms: number;
      failed: boolean;
      fail_status_code?: number | null;
      fail_summary?: string;
      header_error_kind?: string;
      header_error_code?: string;
      header_trace_id?: string;
    }>;
    sync_status: string;
  }>;
};

type AccountHistoryRequestForTest = {
  accounts: unknown[];
  catch_up?: boolean;
};

type AccountWindowUsageResponseForTest = {
  generated_at_ms: number;
  items: Array<{
    row_key: string;
    window_key: string;
    from_ms: number;
    to_ms: number;
    matched: boolean;
    total_requests: number;
    success_calls: number;
    failure_calls: number;
    total_tokens: number;
    total_cost: number;
    success_rate: number | null;
    last_seen_ms: number | null;
    sync_status: string;
  }>;
};

type AccountWindowUsageRequestForTest = {
  windows: unknown[];
};

const makeCodexFile = (name: string, authIndex: string, account: string): AuthFileItem =>
  ({
    name,
    type: 'codex',
    provider: 'codex',
    authIndex,
    account,
    priority: 0,
    disabled: false,
  }) as AuthFileItem;

const makeCodexQuotaData = (): CodexQuotaData => ({
  planType: 'plus',
  windows: [],
  quotaInventoryObserved: true,
  subscriptionActiveUntil: null,
  rateLimitResetCreditsAvailableCount: null,
  rateLimitResetCredits: [],
  rateLimitResetCreditsError: null,
});

const buildCredentialScopedQuotaRecord = <TState extends object>(
  file: AuthFileItem,
  state: TState
) => ({
  [getQuotaCredentialStoreKey(file)]: {
    ...state,
    ...buildQuotaCredentialIdentity(file),
  },
});

const makeAnalyticsEvent = (
  overrides: Partial<Record<string, unknown>>
): Record<string, unknown> => ({
  request_id: 'req-1',
  event_hash: 'event-1',
  timestamp_ms: 1,
  model: 'gpt-5',
  endpoint: '/v1/chat/completions',
  method: 'POST',
  path: '/v1/chat/completions',
  auth_index: 'auth-1',
  source: 'codex.json',
  source_hash: 'source-hash',
  api_key_hash: 'api-key-hash',
  account_snapshot: 'codex@example.com',
  auth_label_snapshot: 'codex@example.com',
  auth_file_snapshot: 'codex.json',
  auth_provider_snapshot: 'codex',
  input_tokens: 10,
  output_tokens: 5,
  cached_tokens: 0,
  cache_read_tokens: 0,
  cache_creation_tokens: 0,
  reasoning_tokens: 0,
  total_tokens: 15,
  latency_ms: 120,
  failed: false,
  ...overrides,
});

const makeEventsResponse = (event: Record<string, unknown>): AnalyticsResponseForTest => ({
  generated_at_ms: 1,
  granularity: 'day',
  events: {
    items: [event],
    next_before_ms: 0,
    has_more: false,
  },
});

const makeEmptyAnalyticsResponse = (): AnalyticsResponseForTest => ({
  generated_at_ms: 1,
  granularity: 'day',
  account_stats: [],
  timeline: [],
});

const defaultGetAnalytics = async (
  _base: string,
  _key: string | undefined,
  request: unknown
): Promise<AnalyticsResponseForTest> => {
  const include = (request as AnalyticsRequestForTest).include;
  if (include?.events_page) {
    return makeEventsResponse(makeAnalyticsEvent({}));
  }
  return makeEmptyAnalyticsResponse();
};

const makeAccountHistoryResponse = (
  items: AccountHistoryResponseForTest['items']
): AccountHistoryResponseForTest => ({
  generated_at_ms: 1,
  checkpoint: {
    last_event_id: 1,
    latest_id: 1,
    pending: false,
    processed: 0,
  },
  items,
});

const { mocks } = vi.hoisted(() => {
  (
    globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
  ).IS_REACT_ACT_ENVIRONMENT = true;

  const codexFile = {
    name: 'codex.json',
    type: 'codex',
    provider: 'codex',
    authIndex: 'auth-1',
    account: 'codex@example.com',
    priority: 0,
    disabled: false,
  } as AuthFileItem;

  return {
    mocks: {
      files: [codexFile] as AuthFileItem[],
      authFilesLoading: false,
      selectedFiles: new Set<string>(),
      selectionCount: 0,
      batchFieldsUpdating: false,
      configurationDirty: false,
      configurationSaving: false,
      configurationEnabledCalls: [] as boolean[],
      configurationSourceMemberCounts: [] as number[],
      configurationReset: vi.fn(),
      configurationReload: vi.fn(async () => undefined),
      configurationSave: vi.fn(async () => undefined),
      allowNextNavigation: vi.fn(),
      allowNavigationTo: vi.fn(),
      lastUnsavedGuardOptions: null as null | {
        enabled?: boolean;
        shouldBlock: boolean | ((args: Record<string, unknown>) => boolean);
        onConfirmNavigation?: () => boolean | void | Promise<boolean | void>;
      },
      location: { pathname: '/accounts', search: '' },
      apiBase: 'http://cpa-a.local:8317',
      managementKey: 'manager-key',
      navigate: vi.fn(),
      showNotification: vi.fn(),
      showConfirmation: vi.fn(),
      loadFiles: vi.fn(async () => undefined),
      toggleSelect: vi.fn(),
      selectAllVisible: vi.fn(),
      invertVisibleSelection: vi.fn(),
      deselectAll: vi.fn(),
      batchPatchFields: vi.fn(async () => ({ success: 1, failed: 0, failedNames: [] })),
      batchSetStatus: vi.fn(async () => undefined),
      batchDownload: vi.fn(async () => undefined),
      batchDelete: vi.fn(),
      handleDelete: vi.fn(),
      handleDownload: vi.fn(async () => undefined),
      handleCredentialRefresh: vi.fn(async () => undefined),
      showModels: vi.fn(async () => undefined),
      refreshModels: vi.fn(async () => undefined),
      invalidateModels: vi.fn(),
      loadExcluded: vi.fn(async () => undefined),
      loadModelAlias: vi.fn(async () => undefined),
      oauthExcluded: {} as Record<string, string[]>,
      oauthModelAlias: {} as Record<string, OAuthModelAliasEntry[]>,
      listCodexInspectionRuns: vi.fn(
        async (): Promise<{ items: CodexInspectionRun[] }> => ({
          items: [],
        })
      ),
      getCodexInspectionRun: vi.fn(
        async (): Promise<{
          run: CodexInspectionRun | null;
          results: CodexInspectionResult[];
        }> => ({ run: null, results: [] })
      ),
      getActiveQuotaCooldowns: vi.fn(async (): Promise<QuotaCooldownInfo[]> => []),
      listAccountActionCandidates: vi.fn(
        async (): Promise<AccountActionCandidatesResponse> => ({
          items: [],
          pendingCount: 0,
        })
      ),
      getAnalytics: vi.fn(
        async (_base: string, _key: string | undefined, _request: unknown): Promise<unknown> => ({
          generated_at_ms: 1,
          granularity: 'day',
          account_stats: [],
          timeline: [],
        })
      ),
      getHeaderSnapshots: vi.fn(
        async (): Promise<HeaderSnapshotsResponseForTest> => ({
          generated_at_ms: 1,
          from_ms: 0,
          to_ms: 1,
          items: [],
        })
      ),
      getAccountHistory: vi.fn(
        async (
          _base: string,
          _managementKey: string | undefined,
          _request: AccountHistoryRequestForTest,
          _signal?: AbortSignal
        ): Promise<AccountHistoryResponseForTest> => ({
          generated_at_ms: 1,
          checkpoint: {
            last_event_id: 1,
            latest_id: 1,
            pending: false,
            processed: 0,
          },
          items: [],
        })
      ),
      getAccountWindowUsage: vi.fn(
        async (
          _base: string,
          _managementKey: string | undefined,
          _request: AccountWindowUsageRequestForTest
        ): Promise<AccountWindowUsageResponseForTest> => ({
          generated_at_ms: 1,
          items: [],
        })
      ),
      panelFeatureAvailability: {
        checking: false,
        managerServiceBase: 'http://manager.local:18317',
        requestMonitoringAvailable: false,
        serverCodexInspectionAvailable: false,
      },
      lastExcludedEditorProps: null as null | {
        open: boolean;
        provider?: string;
        onClose: () => void;
      },
      lastAliasEditorProps: null as null | {
        open: boolean;
        provider?: string;
        onClose: () => void;
      },
      localInspection: null as null | Record<string, unknown>,
      lastHealthWorkspaceProps: null as null | {
        mode: 'local' | 'server';
        onModeChange: (mode: 'local' | 'server') => void;
        onOpenCredential: (target: { fileName: string; authIndex: string | null }) => void;
      },
      quotaState: {
        antigravityQuota: {},
        claudeQuota: {},
        codexQuota: {},
        kimiQuota: {},
        xaiQuota: {},
        setAntigravityQuota: vi.fn(),
        setClaudeQuota: vi.fn(),
        setCodexQuota: vi.fn(),
        setKimiQuota: vi.fn(),
        setXaiQuota: vi.fn(),
      },
      t: (key: string, options?: Record<string, unknown>) => {
        if (options && typeof options.name === 'string') return `${key}:${options.name}`;
        return key;
      },
    },
  };
});

vi.mock('react-i18next', () => ({
  initReactI18next: { type: '3rdParty', init: () => {} },
  useTranslation: () => ({
    t: mocks.t,
    i18n: { language: 'en' },
  }),
}));

vi.mock('react-router-dom', () => ({
  useNavigate: () => mocks.navigate,
  useLocation: () => mocks.location,
}));

vi.mock('@/hooks/useHeaderRefresh', () => ({
  useHeaderRefresh: () => {},
}));

vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => mocks.panelFeatureAvailability,
}));

vi.mock('@/hooks/useUnsavedChangesGuard', () => ({
  useUnsavedChangesGuard: (options: {
    enabled?: boolean;
    shouldBlock: boolean | ((args: Record<string, unknown>) => boolean);
    onConfirmNavigation?: () => boolean | void | Promise<boolean | void>;
  }) => {
    mocks.lastUnsavedGuardOptions = options;
    return {
      allowNextNavigation: mocks.allowNextNavigation,
      allowNavigationTo: mocks.allowNavigationTo,
    };
  },
}));

vi.mock('@/features/authFiles/hooks/useAuthFilesData', () => ({
  useAuthFilesData: () => ({
    files: mocks.files,
    selectedFiles: mocks.selectedFiles,
    selectionCount: mocks.selectionCount,
    loading: mocks.authFilesLoading,
    error: '',
    uploading: false,
    authJsonPasteSaving: false,
    deleting: null,
    batchFieldsUpdating: mocks.batchFieldsUpdating,
    fileInputRef: { current: null },
    loadFiles: mocks.loadFiles,
    handleUploadClick: vi.fn(),
    handleFileChange: vi.fn(),
    savePastedAuthJson: vi.fn(async () => 'saved.json'),
    handleDelete: mocks.handleDelete,
    handleDownload: mocks.handleDownload,
    handleCredentialRefresh: mocks.handleCredentialRefresh,
    credentialRefreshing: {},
    toggleSelect: mocks.toggleSelect,
    selectAllVisible: mocks.selectAllVisible,
    invertVisibleSelection: mocks.invertVisibleSelection,
    deselectAll: mocks.deselectAll,
    batchDownload: mocks.batchDownload,
    batchSetStatus: mocks.batchSetStatus,
    batchPatchFields: mocks.batchPatchFields,
    batchDelete: mocks.batchDelete,
  }),
}));

vi.mock('@/features/authFiles/hooks/useAuthFilesOauth', () => ({
  useAuthFilesOauth: () => ({
    excluded: mocks.oauthExcluded,
    excludedError: 'ready',
    modelAlias: mocks.oauthModelAlias,
    modelAliasError: 'ready',
    allProviderModels: {},
    providerList: ['codex'],
    loadExcluded: mocks.loadExcluded,
    loadModelAlias: mocks.loadModelAlias,
    deleteExcluded: vi.fn(),
    deleteModelAlias: vi.fn(),
    handleMappingUpdate: vi.fn(async () => undefined),
    handleDeleteLink: vi.fn(),
    handleToggleFork: vi.fn(async () => undefined),
    handleRenameAlias: vi.fn(async () => undefined),
    handleDeleteAlias: vi.fn(),
  }),
}));

vi.mock('@/features/authFiles/hooks/useAuthFilesModels', () => ({
  useAuthFilesModels: () => ({
    modelsLoading: false,
    modelsRefreshing: false,
    modelsList: [],
    modelDefinitions: [],
    modelDefinitionsLoading: false,
    modelDefinitionsError: null,
    modelsFileName: '',
    modelsFileType: '',
    modelsSelectionKey: getAuthFileSelectionKey(mocks.files[0]),
    modelsError: null,
    showModels: mocks.showModels,
    refreshModels: mocks.refreshModels,
    invalidateModels: mocks.invalidateModels,
  }),
}));

vi.mock('@/features/authFiles/hooks/useAuthFileConfigurationEditor', () => ({
  useAuthFileConfigurationEditor: (options: {
    enabled: boolean;
    file: AuthFileItem | null;
    sourceMemberCount?: number;
  }) => {
    mocks.configurationEnabledCalls.push(options.enabled);
    mocks.configurationSourceMemberCounts.push(options.sourceMemberCount ?? 0);
    const draft = {
      prefix: '',
      proxyUrl: '',
      priority: '',
      weight: '',
      note: '',
      headersText: '',
      excludedModelsText: '',
      disableCooling: false,
      requestRetry: '',
      websockets: false,
      xaiRoutingMode: 'grok-build' as const,
      baseUrl: '',
      cloakMode: '',
      cloakStrictMode: false,
      cloakSensitiveWordsText: '',
      cloakCacheUserId: false,
      toolPrefixDisabled: false,
    };
    return {
      state:
        options.enabled && options.file
          ? {
              authFile: options.file,
              fileName: options.file.name,
              loading: false,
              saving: mocks.configurationSaving,
              error: '',
              record: { type: options.file.type ?? options.file.provider ?? 'codex' },
              recordIndex: null,
              providerKey: String(options.file.type ?? options.file.provider ?? 'codex'),
              originalDraft: draft,
              draft,
            }
          : null,
      draft: options.enabled && options.file ? draft : null,
      errors: {},
      dirty: mocks.configurationDirty,
      canSave: mocks.configurationDirty && !mocks.configurationSaving,
      rawDataText: '{}',
      sourceMemberCount: options.sourceMemberCount ?? 0,
      sharedSourceReadOnly: false,
      updateField: vi.fn(),
      reset: () => {
        mocks.configurationDirty = false;
        mocks.configurationReset();
      },
      reload: mocks.configurationReload,
      save: mocks.configurationSave,
    };
  },
}));

vi.mock('@/features/monitoring/components/CredentialHealthInspectionWorkspace', () => ({
  CredentialHealthInspectionWorkspace: (props: {
    mode: 'local' | 'server';
    onModeChange: (mode: 'local' | 'server') => void;
    onOpenCredential: (target: { fileName: string; authIndex: string | null }) => void;
  }) => {
    mocks.lastHealthWorkspaceProps = props;
    return <div data-testid="credential-health-workspace">credential-health:{props.mode}</div>;
  },
}));

vi.mock('@/features/monitoring/codexInspection', () => ({
  createCodexInspectionConnectionFingerprint: (apiBase: string, managementKey: string) =>
    `${apiBase}:${managementKey}`,
  loadCodexInspectionLastRun: () => mocks.localInspection,
}));

vi.mock('@/features/authFiles/components/AuthJsonPasteModal', () => ({
  AuthJsonPasteModal: () => null,
}));

vi.mock('@/features/authFiles/components/AuthFileModelsModal', () => ({
  AuthFileModelsContent: () => <div>models-content</div>,
  AuthFileModelsModal: () => null,
}));

vi.mock('@/features/authFiles/components/OAuthExcludedCard', () => ({
  OAuthExcludedCard: (props: { onAdd: () => void; onEdit: (provider: string) => void }) => (
    <div>
      <button type="button" onClick={props.onAdd}>
        oauth-excluded-add
      </button>
      <button type="button" onClick={() => props.onEdit('codex')}>
        oauth-excluded-edit
      </button>
    </div>
  ),
}));

vi.mock('@/features/authFiles/components/OAuthModelAliasCard', () => ({
  OAuthModelAliasCard: (props: {
    onAdd: () => void;
    onEditProvider: (provider: string) => void;
  }) => (
    <div>
      <button type="button" onClick={props.onAdd}>
        oauth-alias-add
      </button>
      <button type="button" onClick={() => props.onEditProvider('codex')}>
        oauth-alias-edit
      </button>
    </div>
  ),
}));

vi.mock('@/features/authFiles/components/OAuthEditorModals', () => ({
  OAuthExcludedEditorModal: (props: { open: boolean; provider?: string; onClose: () => void }) => {
    mocks.lastExcludedEditorProps = props;
    return props.open ? <div>oauth-excluded-editor-open</div> : null;
  },
  OAuthModelAliasEditorModal: (props: {
    open: boolean;
    provider?: string;
    onClose: () => void;
  }) => {
    mocks.lastAliasEditorProps = props;
    return props.open ? <div>oauth-alias-editor-open</div> : null;
  },
}));

vi.mock('@/features/oauth/CodexReauthDialog', () => ({
  CodexReauthDialog: () => null,
}));

vi.mock('@/services/api', () => ({
  accountQuotaSnapshotApi: {
    write: vi.fn(async (_base, _managementKey, entries: unknown[]) => ({
      observed_at_ms: Date.now(),
      items: entries.map(() => ({})),
    })),
    query: vi.fn(
      async (_base, _managementKey, accounts: Array<{ row_key: string; provider: string }>) => ({
        generated_at_ms: Date.now(),
        items: accounts.map((account) => ({
          row_key: account.row_key,
          account_key: account.row_key,
          provider: account.provider,
          windows: [],
        })),
      })
    ),
  },
  monitoringAnalyticsApi: {
    getAnalytics: mocks.getAnalytics,
    getHeaderSnapshots: mocks.getHeaderSnapshots,
    getAccountHistory: mocks.getAccountHistory,
    getAccountWindowUsage: mocks.getAccountWindowUsage,
  },
  usageServiceApi: {
    listCodexInspectionRuns: mocks.listCodexInspectionRuns,
    getCodexInspectionRun: mocks.getCodexInspectionRun,
    getActiveQuotaCooldowns: mocks.getActiveQuotaCooldowns,
    listAccountActionCandidates: mocks.listAccountActionCandidates,
  },
}));

vi.mock('@/services/api/usageService', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/services/api/usageService')>();
  return {
    ...actual,
    monitoringAnalyticsApi: {
      ...actual.monitoringAnalyticsApi,
      getHeaderSnapshots: mocks.getHeaderSnapshots,
    },
  };
});

vi.mock('@/stores', () => ({
  captureQuotaCacheGeneration: () => 0,
  commitIfQuotaCacheCurrent: (_generation: number, commit: () => void) => {
    commit();
    return true;
  },
  useNotificationStore: (
    selector?: (state: {
      showNotification: typeof mocks.showNotification;
      showConfirmation: typeof mocks.showConfirmation;
    }) => unknown
  ) => {
    const state = {
      showNotification: mocks.showNotification,
      showConfirmation: mocks.showConfirmation,
    };
    return selector ? selector(state) : state;
  },
  useAuthStore: (
    selector: (state: {
      apiBase: string;
      connectionStatus: 'connected';
      managementKey: string;
    }) => unknown
  ) =>
    selector({
      apiBase: mocks.apiBase,
      connectionStatus: 'connected',
      managementKey: mocks.managementKey,
    }),
  useQuotaStore: (
    selector: (state: {
      antigravityQuota: Record<string, never>;
      claudeQuota: Record<string, never>;
      codexQuota: Record<string, never>;
      kimiQuota: Record<string, never>;
      xaiQuota: Record<string, never>;
      setAntigravityQuota: () => void;
      setClaudeQuota: () => void;
      setCodexQuota: () => void;
      setKimiQuota: () => void;
      setXaiQuota: () => void;
    }) => unknown
  ) => selector(mocks.quotaState),
  useThemeStore: (selector: (state: { resolvedTheme: 'light' | 'dark' }) => unknown) =>
    selector({ resolvedTheme: 'light' }),
}));

vi.mock('@/utils/clipboard', () => ({
  copyToClipboard: vi.fn(async () => true),
}));

const readText = (value: unknown): string => {
  if (typeof value === 'string' || typeof value === 'number') return String(value);
  if (Array.isArray(value)) return value.map(readText).join('');
  if (isValidElement<{ children?: unknown }>(value)) return readText(value.props.children);
  if (value && typeof value === 'object' && 'children' in value) {
    return readText((value as { children?: unknown }).children);
  }
  return '';
};

const findButtonByText = (renderer: ReactTestRenderer, text: string) => {
  const button = renderer.root
    .findAllByType(Button)
    .find((node) => readText(node.props.children).includes(text));
  if (!button) throw new Error(`Button not found: ${text}`);
  return button;
};

const findHostButtonByText = (renderer: ReactTestRenderer, text: string) => {
  const button = renderer.root
    .findAll((node) => node.type === 'button')
    .find((node) => readText(node.props.children).includes(text));
  if (!button) throw new Error(`Host button not found: ${text}`);
  return button;
};

const findLoadingSpinners = (node: ReactTestInstance) =>
  node.findAll(
    (candidate) =>
      typeof candidate.props.className === 'string' &&
      candidate.props.className.split(/\s+/).includes('loading-spinner')
  );

const findHostButtonByAriaLabel = (renderer: ReactTestRenderer, label: string) => {
  const button = renderer.root
    .findAll((node) => node.type === 'button')
    .find((node) => node.props['aria-label'] === label);
  if (!button) throw new Error(`Host button not found: ${label}`);
  return button;
};

const findBatchMoreItem = (renderer: ReactTestRenderer, key: string) => {
  const batchMoreMenu = renderer.root
    .findAllByType(DropdownMenu)
    .find((node) => node.props.ariaLabel === 'accounts.batch_more');
  const item = batchMoreMenu?.props.items.find((entry: { key?: string }) => entry.key === key);
  if (!item || item.type === 'divider') throw new Error(`Batch menu item not found: ${key}`);
  return item;
};

const findDrawerMoreItem = (renderer: ReactTestRenderer, key: string) => {
  const drawerMoreMenu = renderer.root
    .findAllByType(DropdownMenu)
    .find((node) => node.props.ariaLabel === 'accounts.drawer_more_actions');
  const item = drawerMoreMenu?.props.items.find((entry: { key?: string }) => entry.key === key);
  if (!item || item.type === 'divider') throw new Error(`Drawer menu item not found: ${key}`);
  return item;
};

const findInputByAriaLabel = (renderer: ReactTestRenderer, label: string) => {
  const input = renderer.root
    .findAll((node) => node.type === 'input')
    .find((node) => node.props['aria-label'] === label);
  if (!input) throw new Error(`Input not found: ${label}`);
  return input;
};

const mountedAccountsRenderers = new Set<ReactTestRenderer>();

const renderAccountsPage = async () => {
  let renderer: ReactTestRenderer | null = null;
  await act(async () => {
    renderer = create(<AccountsPage />);
    await Promise.resolve();
  });
  mountedAccountsRenderers.add(renderer!);
  return renderer!;
};

const findDetailButtonByName = (renderer: ReactTestRenderer, fileName: string) => {
  const button = renderer.root
    .findAll((node) => node.type === 'button')
    .find((node) => node.props['aria-label'] === `accounts.open_detail:${fileName}`);
  if (!button) throw new Error(`Detail button not found: ${fileName}`);
  return button;
};

const findAccountCardByKey = (renderer: ReactTestRenderer, selectionKey: string) =>
  renderer.root.findByProps({ 'data-account-card': selectionKey });

const findAccountCardButtonByAriaLabel = (
  renderer: ReactTestRenderer,
  selectionKey: string,
  label: string
) => {
  const card = findAccountCardByKey(renderer, selectionKey);
  const button = card
    .findAll((node) => node.type === 'button')
    .find((node) => node.props['aria-label'] === label);
  if (!button) throw new Error(`Card button not found: ${label}`);
  return button;
};

const findAccountCardInputByAriaLabel = (
  renderer: ReactTestRenderer,
  selectionKey: string,
  label: string
) => {
  const card = findAccountCardByKey(renderer, selectionKey);
  const input = card
    .findAll((node) => node.type === 'input')
    .find((node) => node.props['aria-label'] === label);
  if (!input) throw new Error(`Card input not found: ${label}`);
  return input;
};

const getAccountTableRowTexts = (renderer: ReactTestRenderer) => {
  const table = renderer.root.findByType('table');
  const body = table.findByType('tbody');
  return body.findAllByType('tr').map((row) => readText(row));
};

const getAccountListItemTexts = (renderer: ReactTestRenderer) => {
  const cards = renderer.root.findAll(
    (node) => node.type === 'article' && typeof node.props['data-account-card'] === 'string'
  );
  if (cards.length > 0) return cards.map((row) => readText(row));
  return getAccountTableRowTexts(renderer);
};

const treeText = (renderer: ReactTestRenderer) => readText(renderer.toJSON());

const findAncestorByType = (node: ReactTestInstance, type: string): ReactTestInstance => {
  let current = node.parent;
  while (current) {
    if (current.type === type) return current;
    current = current.parent;
  }
  throw new Error(`Ancestor not found: ${type}`);
};

const createDeferred = <T,>() => {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((nextResolve, nextReject) => {
    resolve = nextResolve;
    reject = nextReject;
  });
  return { promise, reject, resolve };
};

const flushPromises = async () => {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
};

describe('AccountsPage replacement flows', () => {
  afterEach(async () => {
    const restoreWindow = typeof window === 'undefined';
    if (restoreWindow) {
      vi.stubGlobal('window', {
        addEventListener: () => {},
        removeEventListener: () => {},
      });
    }
    await act(async () => {
      mountedAccountsRenderers.forEach((renderer) => renderer.unmount());
    });
    mountedAccountsRenderers.clear();
    vi.useRealTimers();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  beforeEach(() => {
    if (typeof window !== 'undefined') {
      window.localStorage.clear();
      window.location.hash = '';
    }
    mocks.files = [makeCodexFile('codex.json', 'auth-1', 'codex@example.com')];
    mocks.authFilesLoading = false;
    useUsageHeaderSnapshotStore.setState({
      scopeKey: '',
      items: [],
      generatedAtMs: 0,
      loadedAtMs: 0,
      contentRevision: '',
    });
    mocks.selectedFiles = new Set<string>();
    mocks.selectionCount = 0;
    mocks.batchFieldsUpdating = false;
    mocks.configurationDirty = false;
    mocks.configurationSaving = false;
    mocks.configurationEnabledCalls = [];
    mocks.configurationSourceMemberCounts = [];
    mocks.configurationReset.mockClear();
    mocks.configurationReload.mockClear();
    mocks.configurationSave.mockClear();
    mocks.allowNextNavigation.mockClear();
    mocks.allowNavigationTo.mockClear();
    mocks.lastUnsavedGuardOptions = null;
    mocks.location = { pathname: '/accounts', search: '' };
    mocks.apiBase = 'http://cpa-a.local:8317';
    mocks.managementKey = 'manager-key';
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: false,
      serverCodexInspectionAvailable: false,
    };
    mocks.navigate.mockClear();
    mocks.showNotification.mockClear();
    mocks.showConfirmation.mockClear();
    mocks.toggleSelect.mockClear();
    mocks.selectAllVisible.mockClear();
    mocks.invertVisibleSelection.mockClear();
    mocks.deselectAll.mockClear();
    mocks.batchSetStatus.mockClear();
    mocks.batchPatchFields.mockClear();
    mocks.batchDelete.mockClear();
    mocks.handleDelete.mockClear();
    mocks.handleDownload.mockClear();
    mocks.handleCredentialRefresh.mockClear();
    mocks.showModels.mockClear();
    mocks.refreshModels.mockClear();
    mocks.invalidateModels.mockClear();
    vi.mocked(copyToClipboard).mockClear();
    vi.mocked(copyToClipboard).mockResolvedValue(true);
    mocks.getAnalytics.mockReset();
    mocks.getAnalytics.mockImplementation(defaultGetAnalytics);
    mocks.getHeaderSnapshots.mockReset();
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: 1,
      from_ms: 0,
      to_ms: 1,
      items: [],
    });
    mocks.getAccountHistory.mockReset();
    mocks.getAccountHistory.mockResolvedValue(makeAccountHistoryResponse([]));
    mocks.getAccountWindowUsage.mockReset();
    mocks.getAccountWindowUsage.mockResolvedValue({ generated_at_ms: 1, items: [] });
    vi.mocked(accountQuotaSnapshotApi.write).mockReset();
    vi.mocked(accountQuotaSnapshotApi.write).mockImplementation(
      async (_base, _managementKey, entries) => ({
        observed_at_ms: Date.now(),
        items: entries.map((entry) => ({
          row_key: entry.row_key,
          account_key: entry.row_key ?? 'account-key',
          provider: entry.provider,
          inserted_count: entry.windows.length,
        })),
      })
    );
    vi.mocked(accountQuotaSnapshotApi.query).mockReset();
    vi.mocked(accountQuotaSnapshotApi.query).mockImplementation(
      async (_base, _managementKey, accounts) => ({
        generated_at_ms: Date.now(),
        items: accounts.map((account) => ({
          row_key: account.row_key,
          account_key: account.row_key,
          provider: account.provider,
          windows: [],
        })),
      })
    );
    mocks.listCodexInspectionRuns.mockReset();
    mocks.listCodexInspectionRuns.mockResolvedValue({ items: [] });
    mocks.getCodexInspectionRun.mockReset();
    mocks.getCodexInspectionRun.mockResolvedValue({ run: null, results: [] });
    mocks.getActiveQuotaCooldowns.mockReset();
    mocks.getActiveQuotaCooldowns.mockResolvedValue([]);
    mocks.listAccountActionCandidates.mockReset();
    mocks.listAccountActionCandidates.mockResolvedValue({ items: [], pendingCount: 0 });
    mocks.quotaState.antigravityQuota = {};
    mocks.quotaState.claudeQuota = {};
    mocks.quotaState.codexQuota = {};
    mocks.quotaState.kimiQuota = {};
    mocks.quotaState.xaiQuota = {};
    mocks.quotaState.setAntigravityQuota.mockClear();
    mocks.quotaState.setClaudeQuota.mockClear();
    mocks.quotaState.setCodexQuota.mockClear();
    mocks.quotaState.setKimiQuota.mockClear();
    mocks.quotaState.setXaiQuota.mockClear();
    mocks.loadFiles.mockClear();
    mocks.loadExcluded.mockClear();
    mocks.loadModelAlias.mockClear();
    mocks.oauthExcluded = {};
    mocks.oauthModelAlias = {};
    mocks.lastExcludedEditorProps = null;
    mocks.lastAliasEditorProps = null;
    mocks.lastHealthWorkspaceProps = null;
    mocks.localInspection = null;
  });

  it('opens OAuth editors inline instead of navigating to auth-files routes', async () => {
    const renderer = await renderAccountsPage();

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_oauth').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'oauth-excluded-add').props.onClick();
    });

    expect(mocks.navigate).not.toHaveBeenCalledWith(
      expect.stringContaining('/auth-files/oauth-excluded'),
      expect.anything()
    );
    expect(mocks.lastExcludedEditorProps?.open).toBe(true);
    expect(mocks.lastExcludedEditorProps?.provider).toBe('');

    await act(async () => {
      findHostButtonByText(renderer, 'oauth-alias-edit').props.onClick();
    });

    expect(mocks.navigate).not.toHaveBeenCalledWith(
      expect.stringContaining('/auth-files/oauth-model-alias'),
      expect.anything()
    );
    expect(mocks.lastAliasEditorProps?.open).toBe(true);
    expect(mocks.lastAliasEditorProps?.provider).toBe('codex');
  });

  it('reloads credentials when the CPA connection fingerprint changes', async () => {
    const renderer = await renderAccountsPage();

    expect(mocks.loadFiles).toHaveBeenCalledTimes(1);

    mocks.apiBase = 'http://cpa-b.local:8317';
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });

    expect(mocks.loadFiles).toHaveBeenCalledTimes(2);

    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });

    expect(mocks.loadFiles).toHaveBeenCalledTimes(2);
  });

  it('clears quota snapshot state and ignores a late query from the previous connection', async () => {
    const file = {
      ...makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
      disabled: true,
    } as AuthFileItem;
    const rowKey = 'codex.json\u0000auth-1';
    const fetchedAtMs = Date.now();
    mocks.files = [file];
    mocks.location = {
      pathname: '/accounts',
      search: `?account=${encodeURIComponent(rowKey)}&tab=quota`,
    };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      fetchedAtMs,
      quotaInventoryObserved: true,
      windows: [
        {
          id: 'five-hour',
          label: 'Five hours',
          usedPercent: 20,
          resetAtMs: fetchedAtMs + 60 * 60 * 1000,
          resetLabel: new Date(fetchedAtMs + 60 * 60 * 1000).toISOString(),
          resetAccuracy: 'exact',
          limitWindowSeconds: 5 * 60 * 60,
        },
      ],
    });
    const lateQuery = createDeferred<Awaited<ReturnType<typeof accountQuotaSnapshotApi.query>>>();
    vi.mocked(accountQuotaSnapshotApi.query)
      .mockImplementationOnce(() => lateQuery.promise)
      .mockResolvedValue({ generated_at_ms: fetchedAtMs, items: [] });

    const renderer = await renderAccountsPage();
    await flushPromises();
    expect(accountQuotaSnapshotApi.query).toHaveBeenCalledTimes(1);
    const previousQuerySignal = vi.mocked(accountQuotaSnapshotApi.query).mock.calls[0]?.[4] as
      | AbortSignal
      | undefined;
    expect(previousQuerySignal?.aborted).toBe(false);

    mocks.apiBase = 'http://cpa-b.local:8317';
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await flushPromises();
    expect(previousQuerySignal?.aborted).toBe(true);

    lateQuery.resolve({
      generated_at_ms: fetchedAtMs + 1,
      items: [
        {
          row_key: rowKey,
          account_key: rowKey,
          provider: 'codex',
          windows: [
            {
              provider_window_id: 'five-hour',
              window_kind: 'five_hour',
              window_mode: 'fixed',
              model_scope_kind: 'all',
              source: 'response_header',
              observed_at_ms: fetchedAtMs + 1,
              boundary_accuracy: 'derived',
              cycle_start_ms: fetchedAtMs - 1_000,
              cycle_end_ms: fetchedAtMs + 60 * 60 * 1000,
              duration_seconds: 5 * 60 * 60,
              used_percent: 95,
              remaining_percent: 5,
              stale: false,
            },
          ],
        },
      ],
    });
    await flushPromises();

    const quotaCard = renderer.root
      .findAllByType(QuotaWindowCard)
      .find((node) => node.props.window.providerWindowId === 'five-hour');
    expect(quotaCard?.props.window.usedPercent).toBe(20);
  });

  it('restarts the initial credential load when StrictMode replays effects', async () => {
    let renderer: ReactTestRenderer | null = null;
    await act(async () => {
      renderer = create(
        <StrictMode>
          <AccountsPage />
        </StrictMode>
      );
      await Promise.resolve();
    });
    mountedAccountsRenderers.add(renderer!);

    expect(mocks.loadFiles).toHaveBeenCalledTimes(2);
  });

  it('initializes the active view from the accounts view query', async () => {
    mocks.location = { pathname: '/accounts', search: '?view=oauth' };

    const renderer = await renderAccountsPage();

    expect(treeText(renderer)).toContain('oauth-excluded-add');
    expect(findHostButtonByText(renderer, 'accounts.tab_oauth').props['aria-selected']).toBe(true);
  });

  it('starts the OAuth rule preview empty instead of assuming a model', async () => {
    mocks.location = { pathname: '/accounts', search: '?view=oauth' };

    const renderer = await renderAccountsPage();
    const previewInput = renderer.root
      .findAllByType(Input)
      .find((node) => node.props['aria-label'] === 'accounts.oauth_preview_input_label');

    expect(previewInput?.props.value).toBe('');
    expect(treeText(renderer)).toContain('accounts.oauth_preview_empty');
  });

  it('prioritizes affected OAuth previews, collapses direct providers and supports filtering', async () => {
    mocks.location = { pathname: '/accounts', search: '?view=oauth' };
    mocks.files = [
      makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
      {
        name: 'claude.json',
        type: 'claude',
        provider: 'claude',
        account: 'claude@example.com',
        disabled: false,
      } as AuthFileItem,
      {
        name: 'kimi.json',
        type: 'kimi',
        provider: 'kimi',
        account: 'kimi@example.com',
        disabled: false,
      } as AuthFileItem,
    ];
    mocks.oauthExcluded = { kimi: ['team-*'] };
    mocks.oauthModelAlias = {
      codex: [{ name: 'gpt-5-codex', alias: 'team-codex' }],
    };

    const renderer = await renderAccountsPage();
    const previewInput = renderer.root
      .findAllByType(Input)
      .find((node) => node.props['aria-label'] === 'accounts.oauth_preview_input_label');
    if (!previewInput) throw new Error('OAuth preview input not found');

    act(() => previewInput.props.onChange({ target: { value: 'team-codex' } }));

    const getRenderedProviders = () =>
      renderer.root
        .findAll((node) => typeof node.props['data-oauth-preview-provider'] === 'string')
        .map((node) => node.props['data-oauth-preview-provider']);

    expect(getRenderedProviders()).toEqual(['codex', 'kimi']);
    const directSummary = renderer.root.findByProps({
      'data-oauth-preview-direct-summary': 1,
    });
    expect(directSummary.props['aria-expanded']).toBe(false);

    act(() => directSummary.props.onClick());
    expect(getRenderedProviders()).toEqual(['codex', 'kimi', 'claude']);

    const providerSelect = renderer.root
      .findAllByType(Select)
      .find((node) => node.props.id === 'oauth-preview-provider');
    if (!providerSelect) throw new Error('OAuth preview provider filter not found');
    act(() => providerSelect.props.onChange('claude'));

    expect(getRenderedProviders()).toEqual(['claude']);
    expect(
      renderer.root.findAll(
        (node) => typeof node.props['data-oauth-preview-direct-summary'] === 'number'
      )
    ).toHaveLength(0);
  });

  it('opens OAuth editors from a deep link', async () => {
    mocks.location = {
      pathname: '/accounts',
      search: '?view=oauth&editor=excluded&editorProvider=codex',
    };

    const renderer = await renderAccountsPage();

    expect(treeText(renderer)).toContain('oauth-excluded-editor-open');
    expect(mocks.lastExcludedEditorProps?.provider).toBe('codex');
  });

  it('restores filters and account detail tabs from the URL', async () => {
    mocks.files = [
      makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
      {
        name: 'xai.json',
        type: 'xai',
        provider: 'xai',
        account: 'xai@example.com',
        disabled: false,
      } as AuthFileItem,
    ];
    mocks.location = {
      pathname: '/accounts',
      search: '?provider=codex&account=codex.json%00auth-1&tab=quota',
    };

    const renderer = await renderAccountsPage();

    expect(
      renderer.root.findAllByProps({ 'data-account-card': 'codex.json\u0000auth-1' })
    ).toHaveLength(1);
    expect(
      renderer.root.findAllByProps({
        'data-account-card': getAuthFileSelectionKey(mocks.files[1]),
      })
    ).toHaveLength(0);
    expect(findHostButtonByText(renderer, 'accounts.detail_tab_quota').props['aria-selected']).toBe(
      true
    );
    expect(
      renderer.root.findByProps({ id: 'accounts-provider-filter-codex' }).props['aria-selected']
    ).toBe(true);
  });

  it('opens the configuration tab from a deep link', async () => {
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex.json%00auth-1&tab=config',
    };

    const renderer = await renderAccountsPage();

    expect(
      findHostButtonByText(renderer, 'accounts.detail_tab_config').props['aria-selected']
    ).toBe(true);
    expect(treeText(renderer)).toContain('accounts.config_section_routing');
    expect(mocks.configurationEnabledCalls).toContain(true);
    expect(mocks.lastUnsavedGuardOptions?.enabled).toBe(true);
    expect(typeof mocks.lastUnsavedGuardOptions?.shouldBlock).toBe('function');
    const shouldBlock = mocks.lastUnsavedGuardOptions?.shouldBlock;
    if (typeof shouldBlock !== 'function') throw new Error('missing navigation blocker');
    expect(
      shouldBlock({
        currentLocation: mocks.location,
        nextLocation: { pathname: '/accounts', search: '?view=health&healthMode=local' },
      })
    ).toBe(false);
  });

  it('loads runtime models and global model rules from a models deep link', async () => {
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex.json%00auth-1&tab=models',
    };

    const renderer = await renderAccountsPage();
    await flushPromises();

    expect(
      findHostButtonByText(renderer, 'accounts.detail_tab_models').props['aria-selected']
    ).toBe(true);
    expect(mocks.showModels).toHaveBeenCalledWith(mocks.files[0]);
    expect(mocks.loadExcluded).toHaveBeenCalledTimes(1);
    expect(mocks.loadModelAlias).toHaveBeenCalledTimes(1);
  });

  it('masks the models summary credential name when credential display is masked', async () => {
    mocks.files = [
      makeCodexFile('customer-private.json', 'auth-1', 'customer-private@example.com'),
    ];
    mocks.location = {
      pathname: '/accounts',
      search: '?display=masked&account=customer-private.json%00auth-1&tab=models',
    };

    const renderer = await renderAccountsPage();

    expect(renderer.root.findByType(AccountModelsTab).props.fileName).toBe('cus***vate.json');
  });

  it('migrates legacy credential detail links to configuration without rendering the old tab', async () => {
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex.json%00auth-1&tab=credential',
    };

    const renderer = await renderAccountsPage();
    const detailTabLabels = renderer.root
      .findAll((node) => node.type === 'button' && node.props.role === 'tab')
      .map((node) => readText(node.props.children));

    expect(
      findHostButtonByText(renderer, 'accounts.detail_tab_config').props['aria-selected']
    ).toBe(true);
    expect(detailTabLabels).not.toContain('accounts.detail_tab_credential');
    expect(mocks.configurationEnabledCalls).toContain(true);
  });

  it('passes the number of runtime identities sharing the selected physical source', async () => {
    mocks.files = [
      makeCodexFile('codex.json', 'auth-1', 'first@example.com'),
      makeCodexFile('codex.json', 'auth-2', 'second@example.com'),
    ];
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex.json%00auth-1&tab=config',
    };

    await renderAccountsPage();

    expect(mocks.configurationSourceMemberCounts).toContain(2);
  });

  it('shows the provider icon in the credential detail title', async () => {
    const renderer = await renderAccountsPage();

    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
      await Promise.resolve();
    });

    const providerIcon = renderer.root.findByProps({ 'data-account-provider-icon': 'codex' });
    expect(providerIcon.findByType('img').props.alt).toBe('');
    expect(providerIcon.findByType('img').props.src).toContain('codex');
  });

  it('keeps Codex credential refresh in the drawer more menu', async () => {
    const renderer = await renderAccountsPage();

    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    const refreshItem = findDrawerMoreItem(renderer, 'refresh-credential');

    expect(refreshItem.label).toBe('auth_files.credential_refresh_button');
    expect(refreshItem.disabled).toBe(false);
    await act(async () => {
      refreshItem.onClick();
      await Promise.resolve();
    });
    expect(mocks.handleCredentialRefresh).toHaveBeenCalledWith(mocks.files[0]);
  });

  it('confirms before leaving a dirty configuration tab', async () => {
    mocks.configurationDirty = true;
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex.json%00auth-1&tab=config',
    };
    const renderer = await renderAccountsPage();

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
      await Promise.resolve();
    });

    expect(mocks.showConfirmation).toHaveBeenCalledTimes(1);
    expect(
      findHostButtonByText(renderer, 'accounts.detail_tab_config').props['aria-selected']
    ).toBe(true);

    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as {
      onConfirm: () => void;
    };
    await act(async () => {
      confirmation.onConfirm();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.configurationReset).toHaveBeenCalledTimes(1);
    expect(findHostButtonByText(renderer, 'accounts.detail_tab_quota').props['aria-selected']).toBe(
      true
    );
  });

  it('preserves a dirty draft when switching between configuration and models', async () => {
    mocks.configurationDirty = true;
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex.json%00auth-1&tab=config',
    };
    const renderer = await renderAccountsPage();

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_models').props.onClick();
      await Promise.resolve();
    });

    expect(mocks.showConfirmation).not.toHaveBeenCalled();
    expect(mocks.configurationReset).not.toHaveBeenCalled();
    expect(mocks.allowNextNavigation).toHaveBeenCalledTimes(1);
    expect(
      findHostButtonByText(renderer, 'accounts.detail_tab_models').props['aria-selected']
    ).toBe(true);
    expect(mocks.navigate).toHaveBeenCalledWith(
      {
        pathname: '/accounts',
        search: '?account=codex.json%00auth-1&tab=models',
      },
      { replace: true }
    );
    expect(mocks.configurationEnabledCalls).toContain(true);
  });

  it('refreshes the credential configuration snapshot with the models workspace when clean', async () => {
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex.json%00auth-1&tab=models',
    };
    const renderer = await renderAccountsPage();

    await act(async () => {
      renderer.root.findByType(AccountModelsTab).props.onRefresh();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.refreshModels).toHaveBeenCalledWith(mocks.files[0]);
    expect(mocks.loadExcluded).toHaveBeenCalled();
    expect(mocks.loadModelAlias).toHaveBeenCalled();
    expect(mocks.configurationReload).toHaveBeenCalledTimes(1);
  });

  it('uses the same dirty guard for drawer close and browser navigation', async () => {
    mocks.configurationDirty = true;
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex.json%00auth-1&tab=config',
    };
    const renderer = await renderAccountsPage();
    const drawer = renderer.root.findByType(Drawer);

    const closeRequest = drawer.props.onBeforeClose?.();
    expect(closeRequest).toBeInstanceOf(Promise);
    expect(mocks.lastUnsavedGuardOptions?.enabled).toBe(true);
    const shouldBlock = mocks.lastUnsavedGuardOptions?.shouldBlock;
    if (typeof shouldBlock !== 'function') throw new Error('missing navigation blocker');
    expect(
      shouldBlock({
        currentLocation: mocks.location,
        nextLocation: {
          pathname: '/accounts',
          search: '?account=codex.json%00auth-1&tab=models',
        },
      })
    ).toBe(false);
    expect(
      shouldBlock({
        currentLocation: mocks.location,
        nextLocation: {
          pathname: '/accounts',
          search: '?account=codex.json%00auth-1&tab=quota',
        },
      })
    ).toBe(true);

    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as {
      onCancel: () => void;
    };
    confirmation.onCancel();
    await expect(closeRequest).resolves.toBe(false);
    expect(mocks.configurationReset).not.toHaveBeenCalled();
  });

  it('guards deletion of the open credential without discarding the draft before deletion', async () => {
    mocks.configurationDirty = true;
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex.json%00auth-1&tab=config',
    };
    const renderer = await renderAccountsPage();
    const deleteItem = findDrawerMoreItem(renderer, 'delete');

    await act(async () => {
      deleteItem.onClick();
      await Promise.resolve();
    });

    expect(mocks.showConfirmation).toHaveBeenCalledTimes(1);
    expect(mocks.handleDelete).not.toHaveBeenCalled();
    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as {
      onConfirm: () => void;
    };

    await act(async () => {
      confirmation.onConfirm();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.handleDelete).toHaveBeenCalledWith(mocks.files[0]);
    expect(mocks.configurationReset).not.toHaveBeenCalled();
  });

  it('does not let router navigation discard a configuration while it is saving', async () => {
    mocks.configurationDirty = true;
    mocks.configurationSaving = true;
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex.json%00auth-1&tab=config',
    };
    await renderAccountsPage();

    const confirmNavigation = mocks.lastUnsavedGuardOptions?.onConfirmNavigation;
    if (!confirmNavigation) throw new Error('missing router confirmation hook');
    expect(await confirmNavigation()).toBe(false);
    expect(mocks.configurationReset).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith('accounts.config_save_in_progress', 'info');
  });

  it('disables drawer credential mutations while configuration is saving', async () => {
    mocks.configurationSaving = true;
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex.json%00auth-1&tab=config',
    };
    const renderer = await renderAccountsPage();

    expect(findButtonByText(renderer, 'accounts.disable').props.disabled).toBe(true);
    expect(findDrawerMoreItem(renderer, 'refresh-credential').disabled).toBe(true);
    expect(findDrawerMoreItem(renderer, 'delete').disabled).toBe(true);
    expect(findDrawerMoreItem(renderer, 'download').disabled).toBe(false);
    expect(findButtonByText(renderer, 'accounts.refresh_quota').props.disabled).not.toBe(true);
  });

  it('clears the selected account and detail tab from the URL after the drawer closes', async () => {
    mocks.location = {
      pathname: '/accounts',
      search: '?provider=codex&account=codex.json%00auth-1&tab=config',
    };
    const renderer = await renderAccountsPage();
    const drawer = renderer.root.findByType(Drawer);

    act(() => {
      drawer.props.onClose();
    });

    expect(mocks.allowNextNavigation).toHaveBeenCalledTimes(1);
    expect(mocks.navigate).toHaveBeenCalledWith(
      { pathname: '/accounts', search: '?provider=codex' },
      { replace: true }
    );
  });

  it('confirms before switching workspace views and then allows the intended navigation', async () => {
    mocks.configurationDirty = true;
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex.json%00auth-1&tab=config',
    };
    const renderer = await renderAccountsPage();

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
      await Promise.resolve();
    });
    expect(mocks.navigate).not.toHaveBeenCalledWith(
      expect.objectContaining({ search: expect.stringContaining('view=health') }),
      expect.anything()
    );

    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as {
      onConfirm: () => void;
    };
    await act(async () => {
      confirmation.onConfirm();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.allowNextNavigation).toHaveBeenCalledTimes(1);
    expect(mocks.navigate).toHaveBeenCalledWith(
      {
        pathname: '/accounts',
        search: '?view=health&healthMode=local&account=codex.json%00auth-1&tab=config',
      },
      { replace: false }
    );
  });

  it('filters credential rows through platform tabs without rendering a duplicate selector', async () => {
    mocks.files = [
      makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
      {
        name: 'xai.json',
        type: 'xai',
        provider: 'xai',
        account: 'xai@example.com',
        disabled: false,
      } as AuthFileItem,
    ];

    const renderer = await renderAccountsPage();
    const platformControls = renderer.root.findAll(
      (node) => node.props['aria-label'] === 'accounts.provider_filter'
    );

    expect(platformControls).toHaveLength(1);
    expect(platformControls[0]?.props.role).toBe('tablist');

    await act(async () => {
      renderer.root
        .findByProps({ id: 'accounts-provider-filter-xai' })
        .props.onClick({ preventDefault: () => {} });
      await Promise.resolve();
    });

    expect(
      renderer.root.findAllByProps({ 'data-account-card': 'codex.json\u0000auth-1' })
    ).toHaveLength(0);
    expect(
      renderer.root.findAllByProps({
        'data-account-card': getAuthFileSelectionKey(mocks.files[1]),
      })
    ).toHaveLength(1);
    expect(mocks.navigate).toHaveBeenCalledWith(
      { pathname: '/accounts', search: '?provider=xai' },
      { replace: true }
    );
  });

  it('removes an account deep link after files load without a matching account', async () => {
    mocks.location = {
      pathname: '/accounts',
      search: '?account=missing.json%00auth-9&tab=diagnostics',
    };

    await renderAccountsPage();
    await flushPromises();

    expect(mocks.navigate).toHaveBeenCalledWith(
      { pathname: '/accounts', search: '' },
      { replace: true }
    );
  });

  it('resets omitted filters to defaults during later browser navigation', async () => {
    mocks.location = { pathname: '/accounts', search: '?provider=codex' };
    mocks.files = [
      makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
      {
        name: 'xai.json',
        type: 'xai',
        provider: 'xai',
        account: 'xai@example.com',
        disabled: false,
      } as AuthFileItem,
    ];
    const renderer = await renderAccountsPage();

    expect(
      renderer.root.findAllByProps({ 'data-account-card': 'codex.json\u0000auth-1' })
    ).toHaveLength(1);
    expect(
      renderer.root.findAllByProps({
        'data-account-card': getAuthFileSelectionKey(mocks.files[1]),
      })
    ).toHaveLength(0);

    mocks.location = { pathname: '/accounts', search: '?provider=xai' };
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    expect(
      renderer.root.findAllByProps({ 'data-account-card': 'codex.json\u0000auth-1' })
    ).toHaveLength(0);
    expect(
      renderer.root.findAllByProps({
        'data-account-card': getAuthFileSelectionKey(mocks.files[1]),
      })
    ).toHaveLength(1);

    mocks.location = { pathname: '/accounts', search: '' };
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    expect(
      renderer.root.findAllByProps({ 'data-account-card': 'codex.json\u0000auth-1' })
    ).toHaveLength(1);
    expect(
      renderer.root.findAllByProps({
        'data-account-card': getAuthFileSelectionKey(mocks.files[1]),
      })
    ).toHaveLength(1);
  });

  it('resets omitted filters when the hash changes outside React Router navigation', async () => {
    const windowEvents = new EventTarget();
    const location = { hash: '#/accounts?provider=codex' };
    const storage = new Map<string, string>();
    vi.stubGlobal('window', {
      location,
      localStorage: {
        getItem: (key: string) => storage.get(key) ?? null,
        setItem: (key: string, value: string) => storage.set(key, value),
        removeItem: (key: string) => storage.delete(key),
        clear: () => storage.clear(),
      },
      addEventListener: windowEvents.addEventListener.bind(windowEvents),
      removeEventListener: windowEvents.removeEventListener.bind(windowEvents),
    });
    mocks.location = { pathname: '/accounts', search: '?provider=codex' };
    mocks.files = [
      makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
      {
        name: 'xai.json',
        type: 'xai',
        provider: 'xai',
        account: 'xai@example.com',
        disabled: false,
      } as AuthFileItem,
    ];
    const renderer = await renderAccountsPage();

    expect(
      renderer.root.findAllByProps({ 'data-account-card': 'codex.json\u0000auth-1' })
    ).toHaveLength(1);
    expect(
      renderer.root.findAllByProps({
        'data-account-card': getAuthFileSelectionKey(mocks.files[1]),
      })
    ).toHaveLength(0);

    try {
      await act(async () => {
        location.hash = '#/accounts';
        windowEvents.dispatchEvent(new Event('hashchange'));
        await Promise.resolve();
      });

      expect(
        renderer.root.findAllByProps({ 'data-account-card': 'codex.json\u0000auth-1' })
      ).toHaveLength(1);
      expect(
        renderer.root.findAllByProps({
          'data-account-card': getAuthFileSelectionKey(mocks.files[1]),
        })
      ).toHaveLength(1);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it('restores an unsupported external hash navigation until dirty changes are confirmed', async () => {
    const windowEvents = new EventTarget();
    const browserLocation = {
      hash: '#/accounts?account=codex.json%00auth-1&tab=config',
    };
    const browserHistory = {
      state: { idx: 7 } as unknown,
      replaceState: vi.fn((state: unknown, _title: string, url: string) => {
        browserHistory.state = state;
        browserLocation.hash = url;
      }),
    };
    const storage = new Map<string, string>();
    vi.stubGlobal('window', {
      location: browserLocation,
      history: browserHistory,
      localStorage: {
        getItem: (key: string) => storage.get(key) ?? null,
        setItem: (key: string, value: string) => storage.set(key, value),
        removeItem: (key: string) => storage.delete(key),
        clear: () => storage.clear(),
      },
      addEventListener: windowEvents.addEventListener.bind(windowEvents),
      removeEventListener: windowEvents.removeEventListener.bind(windowEvents),
    });
    mocks.configurationDirty = true;
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex.json%00auth-1&tab=config',
    };

    try {
      await renderAccountsPage();
      browserHistory.state = null;
      browserLocation.hash = '#/accounts?view=health&healthMode=local';

      await act(async () => {
        windowEvents.dispatchEvent(new Event('popstate'));
        await Promise.resolve();
      });

      expect(browserHistory.replaceState).toHaveBeenCalledWith(
        { idx: 7 },
        '',
        '#/accounts?account=codex.json%00auth-1&tab=config'
      );
      expect(browserLocation.hash).toBe('#/accounts?account=codex.json%00auth-1&tab=config');
      expect(mocks.navigate).not.toHaveBeenCalledWith(
        '/accounts?view=health&healthMode=local',
        expect.anything()
      );

      const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as {
        onConfirm: () => void;
      };
      await act(async () => {
        confirmation.onConfirm();
        await Promise.resolve();
        await Promise.resolve();
      });

      expect(mocks.configurationReset).toHaveBeenCalledTimes(1);
      expect(mocks.allowNavigationTo).toHaveBeenCalledWith(
        '/accounts?view=health&healthMode=local'
      );
      expect(mocks.navigate).toHaveBeenCalledWith('/accounts?view=health&healthMode=local', {
        replace: true,
      });
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it('loads passive Header quota evidence with the initial credential list', async () => {
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: true,
    };
    const observedAtMs = Date.now();
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: observedAtMs,
      from_ms: observedAtMs - 1_000,
      to_ms: observedAtMs,
      items: [
        {
          event_hash: 'initial-header-quota',
          timestamp_ms: observedAtMs,
          auth_file_snapshot: 'codex.json',
          auth_index: 'auth-1',
          account_snapshot: 'codex@example.com',
          auth_provider_snapshot: 'codex',
          header_quota_used_percent: 80,
          header_quota_recover_at_ms: observedAtMs + 5 * 60 * 60 * 1000,
        },
      ],
    });

    const renderer = await renderAccountsPage();
    await flushPromises();

    expect(mocks.loadFiles).toHaveBeenCalledTimes(1);
    expect(mocks.getAccountHistory).toHaveBeenCalledTimes(1);
    expect(mocks.getActiveQuotaCooldowns).not.toHaveBeenCalled();
    expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(1);
    expect(mocks.listAccountActionCandidates).not.toHaveBeenCalled();
    expect(mocks.listCodexInspectionRuns).not.toHaveBeenCalled();
    expect(mocks.getCodexInspectionRun).not.toHaveBeenCalled();
    expect(mocks.loadExcluded).not.toHaveBeenCalled();
    expect(mocks.loadModelAlias).not.toHaveBeenCalled();
    expect(mocks.getAnalytics).not.toHaveBeenCalled();
    expect(mocks.getAccountWindowUsage).not.toHaveBeenCalled();
    expect(getAccountListItemTexts(renderer)[0]).toContain('20%');
    expect(mocks.quotaState.setCodexQuota).not.toHaveBeenCalled();
  });

  it('polls passive Header evidence only while the accounts view is visible', async () => {
    vi.useFakeTimers();
    let visibilityState: DocumentVisibilityState = 'visible';
    const documentEvents = new EventTarget();
    vi.stubGlobal('document', {
      get visibilityState() {
        return visibilityState;
      },
      addEventListener: documentEvents.addEventListener.bind(documentEvents),
      removeEventListener: documentEvents.removeEventListener.bind(documentEvents),
    });
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: true,
    };

    await renderAccountsPage();
    await flushPromises();
    expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(2);

    visibilityState = 'hidden';
    await act(async () => {
      documentEvents.dispatchEvent(new Event('visibilitychange'));
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(2);

    visibilityState = 'visible';
    await act(async () => {
      documentEvents.dispatchEvent(new Event('visibilitychange'));
      await Promise.resolve();
    });
    expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(3);
  });

  it('reuses initial Codex Header evidence when filtering by quota window', async () => {
    mocks.files = [
      makeCodexFile('weekly.json', 'weekly-auth', 'weekly@example.com'),
      makeCodexFile('available.json', 'available-auth', 'available@example.com'),
    ];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: true,
    };
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: 1_700_000_000_000,
      from_ms: 0,
      to_ms: 1_700_000_000_000,
      items: [
        {
          event_hash: 'weekly-limit',
          timestamp_ms: 1_700_000_000_000,
          auth_file_snapshot: 'weekly.json',
          auth_index: 'weekly-auth',
          account_snapshot: 'weekly@example.com',
          auth_provider_snapshot: 'codex',
          response_metadata: {
            quota: {
              rate_limit_reached_type: 'secondary',
              reached_window_kind: 'weekly',
              reached_window_source: 'secondary',
              recover_at_ms: 1_700_604_800_000,
            },
            errors: {
              kind: 'rate_limit',
              code: 'usage_limit_reached',
            },
          },
        },
        {
          event_hash: 'expired-weekly-limit',
          timestamp_ms: 1_699_000_000_000,
          auth_file_snapshot: 'available.json',
          auth_index: 'available-auth',
          account_snapshot: 'available@example.com',
          auth_provider_snapshot: 'codex',
          response_metadata: {
            quota: {
              rate_limit_reached_type: 'secondary',
              reached_window_kind: 'weekly',
              reached_window_source: 'secondary',
              recover_at_ms: 1_699_999_999_999,
            },
            errors: {
              kind: 'rate_limit',
              code: 'usage_limit_reached',
            },
          },
        },
      ],
    });

    const renderer = await renderAccountsPage();
    await flushPromises();

    expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(1);
    expect(mocks.listCodexInspectionRuns).not.toHaveBeenCalled();

    const statusSelect = renderer.root
      .findAllByType(Select)
      .find((node) => node.props.ariaLabel === 'accounts.status_filter');
    if (!statusSelect) throw new Error('Accounts status filter not found');
    await act(async () => {
      statusSelect.props.onChange('weekly_limited');
      await Promise.resolve();
    });
    await flushPromises();

    expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(1);
    expect(mocks.listCodexInspectionRuns).toHaveBeenCalledTimes(1);
    expect(
      renderer.root.findAllByProps({
        'data-account-card': getAuthFileSelectionKey(mocks.files[0]),
      })
    ).toHaveLength(1);
    expect(
      renderer.root.findAllByProps({
        'data-account-card': getAuthFileSelectionKey(mocks.files[1]),
      })
    ).toHaveLength(0);
  });

  it('offers and applies the unknown plan filter', async () => {
    mocks.files = [
      {
        ...makeCodexFile('plus.json', 'plus-auth', 'plus@example.com'),
        planType: 'plus',
      } as AuthFileItem,
      makeCodexFile('unknown.json', 'unknown-auth', 'unknown@example.com'),
    ];

    const renderer = await renderAccountsPage();
    const planSelect = renderer.root
      .findAllByType(Select)
      .find((node) => node.props.ariaLabel === 'accounts.plan_filter');
    if (!planSelect) throw new Error('Accounts plan filter not found');

    expect(planSelect.props.options).toContainEqual({
      value: 'unknown',
      label: 'auth_files.codex_plan_filter_unknown',
    });
    await act(async () => {
      planSelect.props.onChange('unknown');
      await Promise.resolve();
    });

    expect(
      renderer.root.findAllByProps({
        'data-account-card': getAuthFileSelectionKey(mocks.files[0]),
      })
    ).toHaveLength(0);
    expect(
      renderer.root.findAllByProps({
        'data-account-card': getAuthFileSelectionKey(mocks.files[1]),
      })
    ).toHaveLength(1);
  });

  it('updates the accounts view query when switching views', async () => {
    const renderer = await renderAccountsPage();

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });

    expect(mocks.navigate).toHaveBeenCalledWith(
      { pathname: '/accounts', search: '?view=health&healthMode=local' },
      { replace: false }
    );
  });

  it('keeps credential list filters outside the workspace navigation panel', async () => {
    const renderer = await renderAccountsPage();
    const tabs = renderer.root.find((node) => node.props['aria-label'] === 'accounts.tabs_label');
    const navigationPanel = findAncestorByType(tabs, 'section');

    expect(
      navigationPanel.findAll(
        (node) => node.type === 'input' && node.props['aria-label'] === 'accounts.search_label'
      )
    ).toHaveLength(0);
    expect(
      renderer.root.findAll(
        (node) => node.type === 'input' && node.props['aria-label'] === 'accounts.search_label'
      )
    ).toHaveLength(1);
  });

  it('keeps the credential health mode in the Accounts URL', async () => {
    mocks.location = { pathname: '/accounts', search: '?view=health&healthMode=local' };
    await renderAccountsPage();

    expect(mocks.lastHealthWorkspaceProps?.mode).toBe('local');
    mocks.navigate.mockClear();

    await act(async () => {
      mocks.lastHealthWorkspaceProps?.onModeChange('server');
      await Promise.resolve();
    });

    expect(mocks.lastHealthWorkspaceProps?.mode).toBe('server');
    expect(mocks.navigate).toHaveBeenCalledWith(
      { pathname: '/accounts', search: '?view=health&healthMode=server' },
      { replace: true }
    );
  });

  it('keeps syncing health mode after React Router and hashchange apply the same URL', async () => {
    const windowEvents = new EventTarget();
    const location = { hash: '#/accounts?view=health&healthMode=local' };
    const storage = new Map<string, string>();
    vi.stubGlobal('window', {
      location,
      localStorage: {
        getItem: (key: string) => storage.get(key) ?? null,
        setItem: (key: string, value: string) => storage.set(key, value),
        removeItem: (key: string) => storage.delete(key),
        clear: () => storage.clear(),
      },
      addEventListener: windowEvents.addEventListener.bind(windowEvents),
      removeEventListener: windowEvents.removeEventListener.bind(windowEvents),
    });
    mocks.location = { pathname: '/accounts', search: '?view=health&healthMode=local' };
    const renderer = await renderAccountsPage();

    try {
      await act(async () => {
        mocks.lastHealthWorkspaceProps?.onModeChange('server');
        await Promise.resolve();
      });

      mocks.location = { pathname: '/accounts', search: '?view=health&healthMode=server' };
      location.hash = '#/accounts?view=health&healthMode=server';
      await act(async () => {
        renderer.update(<AccountsPage />);
        await Promise.resolve();
      });
      await act(async () => {
        windowEvents.dispatchEvent(new Event('hashchange'));
        await Promise.resolve();
      });
      mocks.navigate.mockClear();

      await act(async () => {
        mocks.lastHealthWorkspaceProps?.onModeChange('local');
        await Promise.resolve();
      });

      expect(mocks.navigate).toHaveBeenCalledWith(
        { pathname: '/accounts', search: '?view=health&healthMode=local' },
        { replace: true }
      );
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it('opens the exact shared credential from an inspection result', async () => {
    mocks.files = [
      makeCodexFile('shared-codex.json', 'auth-1', 'first@example.com'),
      makeCodexFile('shared-codex.json', 'auth-2', 'second@example.com'),
    ];
    mocks.location = { pathname: '/accounts', search: '?view=health&healthMode=local' };
    await renderAccountsPage();
    const healthWorkspace = mocks.lastHealthWorkspaceProps;
    mocks.navigate.mockClear();

    await act(async () => {
      healthWorkspace?.onOpenCredential({
        fileName: 'shared-codex.json',
        authIndex: 'auth-2',
      });
      await Promise.resolve();
    });

    expect(mocks.navigate).toHaveBeenCalledWith(
      {
        pathname: '/accounts',
        search: '?account=shared-codex.json%00auth-2&tab=diagnostics',
      },
      { replace: false }
    );
    expect(mocks.showNotification).not.toHaveBeenCalledWith(
      'accounts.inspection_credential_not_found',
      'warning'
    );
  });

  it('does not guess between shared credentials when inspection identity is incomplete', async () => {
    mocks.files = [
      makeCodexFile('shared-codex.json', 'auth-1', 'first@example.com'),
      makeCodexFile('shared-codex.json', 'auth-2', 'second@example.com'),
    ];
    mocks.location = { pathname: '/accounts', search: '?view=health&healthMode=local' };
    await renderAccountsPage();
    mocks.navigate.mockClear();

    await act(async () => {
      mocks.lastHealthWorkspaceProps?.onOpenCredential({
        fileName: 'shared-codex.json',
        authIndex: null,
      });
      await Promise.resolve();
    });

    expect(mocks.showNotification).toHaveBeenCalledWith(
      'accounts.inspection_credential_not_found',
      'warning'
    );
    expect(mocks.navigate).not.toHaveBeenCalled();
  });

  it('patches Codex websockets through auth-index aware batch fields', async () => {
    mocks.selectedFiles = new Set(['codex.json\u0000auth-1']);
    mocks.selectionCount = 1;
    const renderer = await renderAccountsPage();

    await act(async () => {
      await findBatchMoreItem(renderer, 'websockets-enable').onClick();
    });

    expect(mocks.batchPatchFields).toHaveBeenCalledWith([getAuthFilePatchTarget(mocks.files[0])], {
      websockets: true,
    });
  });

  it('disables batch delete for partial shared auth-file selections', async () => {
    mocks.files = [
      makeCodexFile('shared-codex.json', 'auth-1', 'first@example.com'),
      makeCodexFile('shared-codex.json', 'auth-2', 'second@example.com'),
    ];
    mocks.selectedFiles = new Set(['shared-codex.json\u0000auth-1']);
    mocks.selectionCount = 1;

    const renderer = await renderAccountsPage();
    const batchMoreMenu = renderer.root
      .findAllByType(DropdownMenu)
      .find((node) => node.props.ariaLabel === 'accounts.batch_more');
    const deleteItem = batchMoreMenu?.props.items.find(
      (item: { key?: string }) => item.key === 'delete'
    );

    expect(deleteItem?.disabled).toBe(true);

    await act(async () => {
      deleteItem?.onClick?.();
    });

    expect(mocks.batchDelete).not.toHaveBeenCalled();
  });

  it('passes a file-scoped preview into the single batch delete confirmation', async () => {
    mocks.selectedFiles = new Set(['codex.json\u0000auth-1']);
    mocks.selectionCount = 1;

    const renderer = await renderAccountsPage();
    const deleteItem = findBatchMoreItem(renderer, 'delete');

    await act(async () => {
      deleteItem.onClick();
    });

    expect(mocks.batchDelete).toHaveBeenCalledTimes(1);
    expect(mocks.batchDelete.mock.calls[0]?.[0]).toEqual([mocks.files[0]]);
    const options = mocks.batchDelete.mock.calls[0]?.[1] as
      | { message?: unknown; confirmText?: string }
      | undefined;
    expect(options?.confirmText).toBe('common.delete');
    expect(
      isValidElement<{
        summary: string;
        warning: string;
        fileNames: string[];
      }>(options?.message)
    ).toBe(true);
    if (
      !isValidElement<{
        summary: string;
        warning: string;
        fileNames: string[];
      }>(options?.message)
    ) {
      throw new Error('Expected batch delete preview element');
    }
    expect(options.message.props.summary).toContain('accounts.batch_delete_preview_summary');
    expect(options.message.props.warning).toContain('accounts.batch_delete_preview_file_scope');
    expect(options.message.props.fileNames).toContain('codex.json');
  });

  it('keeps runtime Aistudio model discovery available', async () => {
    mocks.files = [
      {
        name: 'runtime-aistudio.json',
        type: 'aistudio',
        provider: 'aistudio',
        runtimeOnly: true,
        disabled: false,
      } as AuthFileItem,
    ];

    const renderer = await renderAccountsPage();
    const modelsButton = findAccountCardButtonByAriaLabel(
      renderer,
      getAuthFileSelectionKey(mocks.files[0]),
      'auth_files.models_button'
    );

    expect(modelsButton.props.disabled).toBe(false);
    await act(async () => {
      modelsButton.props.onClick();
    });
    expect(mocks.showModels).toHaveBeenCalledWith(mocks.files[0]);
  });

  it('falls the removed quota workspace back to the credential list', async () => {
    mocks.location = { pathname: '/accounts', search: '?view=quota' };

    const renderer = await renderAccountsPage();

    expect(
      renderer.root.findAllByProps({ 'data-account-card': 'codex.json\u0000auth-1' })
    ).toHaveLength(1);
    expect(findHostButtonByText(renderer, 'accounts.tab_accounts').props['aria-selected']).toBe(
      true
    );
    expect(treeText(renderer)).not.toContain('accounts.tab_quota');
    expect(treeText(renderer)).not.toContain('accounts.tab_value');
    expect(mocks.navigate).toHaveBeenCalledWith(
      { pathname: '/accounts', search: '' },
      { replace: true }
    );
  });

  it('marks the last local inspection as conflicting when a newer request succeeds', async () => {
    mocks.location = { pathname: '/accounts', search: '' };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getAnalytics.mockImplementation(
      async (_base: string, _key: string | undefined, request: unknown) => {
        const analyticsRequest = request as AnalyticsRequestForTest;
        if (!analyticsRequest.include?.events_page) return makeEmptyAnalyticsResponse();
        return {
          generated_at_ms: 10_000,
          granularity: 'day',
          events: {
            items: [makeAnalyticsEvent({ timestamp_ms: 9_000, failed: false })],
            next_before_ms: 0,
            has_more: false,
            total_count: 1,
          },
        };
      }
    );
    mocks.localInspection = {
      savedAt: 300,
      logs: [],
      logsCollapsed: true,
      actionFilter: 'all',
      connectionFingerprint: 'http://manager.local:18317:manager-key',
      result: {
        settings: {},
        files: mocks.files,
        startedAt: 100,
        finishedAt: 200,
        summary: {
          totalFiles: 1,
          probeSetCount: 1,
          sampledCount: 1,
          disabledCount: 0,
          enabledCount: 0,
          deleteCount: 0,
          disableCount: 0,
          enableCount: 0,
          reauthCount: 1,
          keepCount: 0,
          usedPercentThreshold: 100,
          sampled: false,
          plannedActionPreview: [],
        },
        results: [
          {
            key: 'codex.json\u0000auth-1',
            fileName: 'codex.json',
            displayAccount: 'codex@example.com',
            authIndex: 'auth-1',
            accountId: null,
            provider: 'codex',
            disabled: false,
            autoRecoverOwned: false,
            status: 'error',
            state: 'error',
            raw: mocks.files[0],
            action: 'reauth',
            actionReason: 'expired token',
            statusCode: 401,
            usedPercent: null,
            isQuota: false,
            autoRecoverEligible: false,
            error: 'expired token',
            actionHandled: false,
          },
        ],
      },
    };

    const renderer = await renderAccountsPage();
    await flushPromises();

    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_diagnostics').props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(
      renderer.root.findByProps({ 'data-diagnostic-evidence-status': 'conflict' })
    ).toBeDefined();
    expect(treeText(renderer)).toContain('accounts.detail_diagnostic_reinspect');
    expect(treeText(renderer)).toContain('expired token');
    expect(treeText(renderer)).toContain('accounts.action_reauth');
    expect(treeText(renderer)).toContain('accounts.inspection_source_local');
  });

  it('translates inspection reason keys before rendering them', async () => {
    const originalT = mocks.t;
    mocks.t = (key: string, options?: Record<string, unknown>) => {
      if (key.startsWith('monitoring.')) return `translated:${key}`;
      return originalT(key, options);
    };
    mocks.location = { pathname: '/accounts', search: '' };
    mocks.localInspection = {
      savedAt: 300,
      logs: [],
      logsCollapsed: true,
      actionFilter: 'all',
      connectionFingerprint: 'http://manager.local:18317:manager-key',
      result: {
        settings: {},
        files: mocks.files,
        startedAt: 100,
        finishedAt: 200,
        summary: {
          totalFiles: 1,
          probeSetCount: 1,
          sampledCount: 1,
          disabledCount: 0,
          enabledCount: 0,
          deleteCount: 0,
          disableCount: 0,
          enableCount: 0,
          reauthCount: 0,
          keepCount: 1,
          usedPercentThreshold: 100,
          sampled: false,
          plannedActionPreview: [],
        },
        results: [
          {
            key: 'codex.json\u0000auth-1',
            fileName: 'codex.json',
            displayAccount: 'codex@example.com',
            authIndex: 'auth-1',
            accountId: null,
            provider: 'codex',
            disabled: false,
            autoRecoverOwned: false,
            status: 'ok',
            state: 'ok',
            raw: mocks.files[0],
            action: 'keep',
            actionReason: 'monitoring.xai_inspection_reason_billing_healthy',
            statusCode: 200,
            usedPercent: null,
            isQuota: false,
            autoRecoverEligible: false,
            error: '',
            actionHandled: false,
          },
        ],
      },
    };

    try {
      const renderer = await renderAccountsPage();
      await flushPromises();

      await act(async () => {
        findDetailButtonByName(renderer, 'codex.json').props.onClick();
      });
      await act(async () => {
        findHostButtonByText(renderer, 'accounts.detail_tab_diagnostics').props.onClick();
      });

      expect(treeText(renderer)).toContain(
        'translated:monitoring.xai_inspection_reason_billing_healthy'
      );
    } finally {
      mocks.t = originalT;
    }
  });

  it('ignores stale Manager inspection responses after the CPA connection changes', async () => {
    mocks.location = { pathname: '/accounts', search: '' };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: false,
      serverCodexInspectionAvailable: true,
    };
    const run: CodexInspectionRun = {
      id: 7,
      triggerType: 'manual',
      status: 'completed',
      startedAtMs: 100,
      finishedAtMs: 200,
      totalFiles: 1,
      probeSetCount: 1,
      sampledCount: 1,
      disabledCount: 0,
      enabledCount: 0,
      deleteCount: 0,
      disableCount: 0,
      enableCount: 0,
      reauthCount: 1,
      keepCount: 0,
      createdAtMs: 100,
      updatedAtMs: 200,
    };
    const makeInspectionResult = (id: number, account: string): CodexInspectionResult => ({
      id,
      runId: 7,
      accountKey: account,
      fileName: 'codex.json',
      displayAccount: account,
      authIndex: 'auth-1',
      provider: 'codex',
      disabled: false,
      action: 'reauth',
      actionReason: `${account} reason`,
      statusCode: 401,
      isQuota: false,
      createdAtMs: 200,
    });
    const firstDetail = createDeferred<{
      run: typeof run;
      results: ReturnType<typeof makeInspectionResult>[];
    }>();
    mocks.listCodexInspectionRuns.mockResolvedValue({ items: [run] });
    mocks.getCodexInspectionRun
      .mockImplementationOnce(() => firstDetail.promise)
      .mockResolvedValue({ run, results: [makeInspectionResult(2, 'new-connection@example.com')] });

    const renderer = await renderAccountsPage();
    await flushPromises();
    expect(mocks.getCodexInspectionRun).not.toHaveBeenCalled();

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(mocks.getCodexInspectionRun).toHaveBeenCalledTimes(1);

    mocks.apiBase = 'http://cpa-b.local:8317';
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
      await Promise.resolve();
    });
    await flushPromises();
    expect(mocks.getCodexInspectionRun).toHaveBeenCalledTimes(2);

    firstDetail.resolve({ run, results: [makeInspectionResult(1, 'old-connection@example.com')] });
    await flushPromises();

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });
    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_diagnostics').props.onClick();
    });

    expect(treeText(renderer)).toContain('new-connection@example.com reason');
    expect(treeText(renderer)).not.toContain('old-connection@example.com reason');
  });

  it('removes Manager-only operational filters after switching to CPA control mode', async () => {
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: false,
      serverCodexInspectionAvailable: false,
    };
    const renderer = await renderAccountsPage();
    const findOperationalSelect = () => {
      const select = renderer.root
        .findAllByType(Select)
        .find((node) => node.props.ariaLabel === 'accounts.operational_filter');
      if (!select) throw new Error('Accounts operational filter not found');
      return select;
    };

    expect(findOperationalSelect().props.options).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ value: 'cooldown' }),
        expect.objectContaining({ value: 'automation' }),
      ])
    );

    act(() => {
      findOperationalSelect().props.onChange('cooldown');
    });
    expect(findOperationalSelect().props.value).toBe('cooldown');

    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: '',
      requestMonitoringAvailable: false,
      serverCodexInspectionAvailable: false,
    };
    act(() => {
      renderer.update(<AccountsPage />);
    });

    const cpaModeSelect = findOperationalSelect();
    expect(cpaModeSelect.props.value).toBe('all');
    expect(cpaModeSelect.props.options).not.toEqual(
      expect.arrayContaining([
        expect.objectContaining({ value: 'cooldown' }),
        expect.objectContaining({ value: 'automation' }),
      ])
    );
  });

  it('uses unique table row keys for shared auth accounts', async () => {
    mocks.files = [
      makeCodexFile('shared-codex.json', 'auth-1', 'first@example.com'),
      makeCodexFile('shared-codex.json', 'auth-2', 'second@example.com'),
    ];
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});

    try {
      await renderAccountsPage();
      const duplicateKeyWarning = errorSpy.mock.calls.some((call) =>
        call.some(
          (item) =>
            typeof item === 'string' && item.includes('Encountered two children with the same key')
        )
      );
      expect(duplicateKeyWarning).toBe(false);
    } finally {
      errorSpy.mockRestore();
    }
  });

  it('sorts account cards from the toolbar sort control', async () => {
    mocks.files = [
      {
        ...makeCodexFile('low.json', 'auth-low', 'low@example.com'),
        priority: -1,
        createdAtMs: 1000,
        recent_requests: [{ success: 1, failed: 0 }],
      },
      {
        ...makeCodexFile('middle.json', 'auth-middle', 'middle@example.com'),
        priority: 2,
        createdAtMs: 3000,
        recent_requests: [{ success: 3, failed: 2 }],
      },
      {
        ...makeCodexFile('high.json', 'auth-high', 'high@example.com'),
        priority: 10,
        createdAtMs: 4000,
        recent_requests: [{ success: 2, failed: 1 }],
      },
    ];
    mocks.quotaState.codexQuota = {
      ...buildCredentialScopedQuotaRecord(mocks.files[0], {
        status: 'success',
        windows: [{ id: 'weekly', label: 'Weekly', usedPercent: 10, resetLabel: '2026-01-10' }],
      }),
      ...buildCredentialScopedQuotaRecord(mocks.files[1], {
        status: 'success',
        windows: [{ id: 'weekly', label: 'Weekly', usedPercent: 40, resetLabel: '2026-01-02' }],
      }),
      ...buildCredentialScopedQuotaRecord(mocks.files[2], {
        status: 'success',
        windows: [{ id: 'weekly', label: 'Weekly', usedPercent: 70, resetLabel: '2026-01-05' }],
      }),
    };

    const renderer = await renderAccountsPage();

    expect(getAccountListItemTexts(renderer)[0]).toContain('middle.json');

    await act(async () => {
      findHostButtonByAriaLabel(
        renderer,
        'accounts.sort_label: accounts.col_recent'
      ).props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.col_priority').props.onClick();
    });

    expect(getAccountListItemTexts(renderer)[0]).toContain('high.json');

    await act(async () => {
      findHostButtonByAriaLabel(
        renderer,
        'accounts.sort_label: accounts.col_priority'
      ).props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.col_recent').props.onClick();
    });

    expect(getAccountListItemTexts(renderer)[0]).toContain('middle.json');

    await act(async () => {
      findHostButtonByAriaLabel(
        renderer,
        'accounts.sort_label: accounts.col_recent'
      ).props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.col_quota').props.onClick();
    });

    expect(getAccountListItemTexts(renderer)[0]).toContain('low.json');

    await act(async () => {
      findHostButtonByAriaLabel(
        renderer,
        'accounts.sort_label: accounts.col_quota'
      ).props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.col_created').props.onClick();
    });

    expect(getAccountListItemTexts(renderer)[0]).toContain('high.json');
  });

  it('renders xAI monthly and pay-as-you-go quota windows on account cards', async () => {
    mocks.files = [
      {
        name: 'xai.json',
        type: 'xai',
        provider: 'xai',
        authIndex: 'xai-1',
        account: 'xai@example.com',
        priority: 0,
        disabled: false,
      } as AuthFileItem,
    ];
    mocks.quotaState.xaiQuota = buildCredentialScopedQuotaRecord(mocks.files[0], {
      status: 'success',
      billing: {
        monthlyLimitCents: 10_000,
        usedCents: 12_500,
        includedUsedCents: 10_000,
        onDemandCapCents: 5_000,
        onDemandUsedCents: 2_500,
        onDemandUsedPercent: 50,
        billingPeriodEnd: '2026-07-31T00:00:00Z',
        usedPercent: 100,
      },
    });

    const renderer = await renderAccountsPage();
    const text = treeText(renderer);

    expect(text).toContain('30D');
    expect(text).toContain('PAYG');
  });

  it('renders Antigravity Pro model groups as a two-row quota matrix', async () => {
    mocks.files = [
      {
        name: 'antigravity-pro-matrix.json',
        type: 'antigravity',
        provider: 'antigravity',
        authIndex: 'antigravity-pro-matrix-04',
        account: 'AG Pro Matrix',
        label: 'Antigravity Pro Matrix',
        priority: 0,
        disabled: false,
      } as AuthFileItem,
    ];
    mocks.quotaState.antigravityQuota = buildCredentialScopedQuotaRecord(mocks.files[0], {
      status: 'success',
      subscription: { plan: 'pro', tierName: 'Pro', tierId: 'g1-pro' },
      groups: [
        {
          id: 'gemini-models',
          label: 'Gemini Models',
          description: 'Models within this group: Gemini Flash, Gemini Pro',
          models: ['gemini-2.5-flash', 'gemini-2.5-pro'],
          buckets: [
            {
              id: 'gemini-5h',
              label: 'Five Hour Limit',
              window: '5h',
              remainingFraction: 0.96,
              resetTime: '2026-07-09T12:00:00Z',
            },
            {
              id: 'gemini-weekly',
              label: 'Weekly Limit',
              window: 'weekly',
              remainingFraction: 0.04,
              resetTime: '2026-07-15T12:00:00Z',
            },
          ],
        },
        {
          id: 'claude-gpt-models',
          label: 'Claude and GPT models',
          description: 'Models within this group: Claude Sonnet, GPT-OSS',
          models: ['claude-sonnet-4-5', 'gpt-oss-120b-medium'],
          buckets: [
            {
              id: '3p-5h',
              label: 'Five Hour Limit',
              window: '5h',
              remainingFraction: 0.11,
              resetTime: '2026-07-09T11:00:00Z',
            },
            {
              id: '3p-weekly',
              label: 'Weekly Limit',
              window: 'weekly',
              remainingFraction: 0.19,
              resetTime: '2026-07-13T12:00:00Z',
            },
          ],
        },
      ],
    });

    const renderer = await renderAccountsPage();
    const matrices = renderer.root.findAll(
      (node) => typeof node.props['data-account-quota-matrix'] === 'string'
    );
    expect(matrices).toHaveLength(1);
    const matrix = matrices[0];
    const matrixRows = matrix.findAll(
      (node) => typeof node.props['data-account-quota-matrix-row'] === 'string'
    );
    const matrixCells = matrix.findAll(
      (node) => typeof node.props['data-account-quota-matrix-cell'] === 'string'
    );

    expect(matrixRows.map((node) => node.props['data-account-quota-matrix-row'])).toEqual([
      'five_hour',
      'weekly',
    ]);
    expect(matrixCells).toHaveLength(4);
    expect(readText(matrix)).toContain('5H');
    expect(readText(matrix)).toContain('7D');
    expect(readText(matrix)).toContain('Claude');
    expect(readText(matrix)).toContain('Gemini');
    expect(readText(matrix)).toContain('11%');
    expect(readText(matrix)).toContain('96%');
    expect(readText(matrix)).toContain('19%');
    expect(readText(matrix)).toContain('4%');
    expect(readText(matrix)).not.toContain('Claude/GPT');
    expect(readText(matrix)).not.toContain('accounts.quota_more_windows');
  });

  it('renders Antigravity Free weekly groups as a single-row quota matrix', async () => {
    mocks.files = [
      {
        name: 'antigravity-free-weekly.json',
        type: 'antigravity',
        provider: 'antigravity',
        authIndex: 'antigravity-free-weekly-05',
        account: 'AG Free Seat',
        label: 'Antigravity Free Weekly',
        priority: 0,
        disabled: false,
      } as AuthFileItem,
    ];
    mocks.quotaState.antigravityQuota = buildCredentialScopedQuotaRecord(mocks.files[0], {
      status: 'success',
      subscription: { plan: 'free', tierName: 'Free', tierId: 'g1-free' },
      groups: [
        {
          id: 'gemini-models',
          label: 'Gemini Models',
          description: 'Models within this group: Gemini Flash, Gemini Pro',
          models: ['gemini-2.5-flash', 'gemini-2.5-pro'],
          buckets: [
            {
              id: 'gemini-weekly',
              label: 'Weekly Limit',
              window: 'weekly',
              remainingFraction: 0.76,
              resetTime: '2026-07-15T12:00:00Z',
            },
          ],
        },
        {
          id: 'claude-gpt-models',
          label: 'Claude and GPT models',
          description: 'Models within this group: Claude Sonnet, GPT-OSS',
          models: ['claude-sonnet-4-5', 'gpt-oss-120b-medium'],
          buckets: [
            {
              id: '3p-weekly',
              label: 'Weekly Limit',
              window: 'weekly',
              remainingFraction: 0.31,
              resetTime: '2026-07-13T12:00:00Z',
            },
          ],
        },
      ],
    });

    const renderer = await renderAccountsPage();
    const matrices = renderer.root.findAll(
      (node) => typeof node.props['data-account-quota-matrix'] === 'string'
    );
    expect(matrices).toHaveLength(1);
    const matrix = matrices[0];
    const matrixRows = matrix.findAll(
      (node) => typeof node.props['data-account-quota-matrix-row'] === 'string'
    );
    const matrixCells = matrix.findAll(
      (node) => typeof node.props['data-account-quota-matrix-cell'] === 'string'
    );

    expect(matrixRows.map((node) => node.props['data-account-quota-matrix-row'])).toEqual([
      'weekly',
    ]);
    expect(matrixCells).toHaveLength(2);
    expect(readText(matrix)).toContain('7D');
    expect(readText(matrix)).toContain('Claude');
    expect(readText(matrix)).toContain('Gemini');
    expect(readText(matrix)).toContain('31%');
    expect(readText(matrix)).toContain('76%');
    expect(readText(matrix)).not.toContain('5H');
    expect(readText(matrix)).not.toContain('Claude/GPT');
    expect(readText(matrix)).not.toContain('accounts.quota_more_windows');
  });

  it('keeps the accounts view in card mode without table controls', async () => {
    mocks.files = [
      {
        ...makeCodexFile('low.json', 'auth-low', 'low@example.com'),
        priority: -1,
        recent_requests: [{ success: 1, failed: 0 }],
      },
      {
        ...makeCodexFile('high.json', 'auth-high', 'high@example.com'),
        priority: 10,
        recent_requests: [{ success: 2, failed: 1 }],
      },
    ];

    const renderer = await renderAccountsPage();

    expect(renderer.root.findAllByType('table')).toHaveLength(0);
    expect(
      renderer.root.findAll(
        (node) => node.type === 'article' && typeof node.props['data-account-card'] === 'string'
      )
    ).toHaveLength(2);
    expect(
      renderer.root.findAll(
        (node) =>
          typeof node.props['aria-label'] === 'string' &&
          node.props['aria-label'].startsWith('accounts.select_account:')
      )
    ).toHaveLength(0);
    expect(getAccountListItemTexts(renderer).join('\n')).toContain('high.json');
    expect(() => findHostButtonByText(renderer, 'accounts.view_mode_table')).toThrow();
  });

  it('renders the six localized credential list headers', async () => {
    const renderer = await renderAccountsPage();
    const header = renderer.root.findByProps({ 'data-account-list-header': 'true' });

    expect(header.findAllByType('span').map((node) => readText(node))).toEqual([
      'accounts.list_header_credential',
      'accounts.list_header_availability',
      'accounts.list_header_recent_requests',
      'accounts.list_header_historical_usage',
      'accounts.list_header_quota',
      'accounts.list_header_actions',
    ]);

    expect(renderer.root.findAllByProps({ 'data-account-quota-empty': 'true' })).toHaveLength(1);
    expect(treeText(renderer)).not.toContain('SUM');
  });

  it('selects account cards by row click while selection mode is active', async () => {
    const renderer = await renderAccountsPage();

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.selection_mode_enter').props.onClick();
    });

    const card = renderer.root.findByProps({ 'data-account-card': 'codex.json\u0000auth-1' });
    await act(async () => {
      card.props.onClick();
    });

    expect(mocks.toggleSelect).toHaveBeenCalledWith('codex.json\u0000auth-1');
  });

  it('does not open account details from normal row clicks', async () => {
    const renderer = await renderAccountsPage();
    const card = findAccountCardByKey(renderer, 'codex.json\u0000auth-1');

    expect(card.props.onClick).toBeUndefined();
    expect(treeText(renderer)).not.toContain('accounts.detail_tab_overview');
  });

  it('copies account identity text from the first column with inline feedback', async () => {
    const renderer = await renderAccountsPage();
    const selectionKey = 'codex.json\u0000auth-1';

    await act(async () => {
      await findAccountCardButtonByAriaLabel(
        renderer,
        selectionKey,
        'common.copy codex@example.com'
      ).props.onClick({ stopPropagation: vi.fn() });
    });

    expect(copyToClipboard).toHaveBeenLastCalledWith('codex@example.com');
    expect(treeText(renderer)).toContain('accounts.copy_feedback_copied');

    await act(async () => {
      await findAccountCardButtonByAriaLabel(
        renderer,
        selectionKey,
        'common.copy codex.json'
      ).props.onClick({ stopPropagation: vi.fn() });
    });

    expect(copyToClipboard).toHaveBeenLastCalledWith('codex.json');

    await act(async () => {
      renderer.unmount();
    });
  });

  it('runs account row actions from the explicit action column', async () => {
    const renderer = await renderAccountsPage();
    const selectionKey = 'codex.json\u0000auth-1';

    await act(async () => {
      findAccountCardButtonByAriaLabel(
        renderer,
        selectionKey,
        'auth_files.models_button'
      ).props.onClick();
      await Promise.resolve();
    });

    expect(mocks.showModels).toHaveBeenCalledWith(mocks.files[0]);
    expect(treeText(renderer)).toContain('auth_files.models_empty');

    const modelsTab = renderer.root
      .findAll((node) => node.type === 'button' && node.props.role === 'tab')
      .find((node) => readText(node.props.children) === 'accounts.detail_tab_models');
    expect(modelsTab?.props['aria-selected']).toBe(true);

    await act(async () => {
      findAccountCardButtonByAriaLabel(
        renderer,
        selectionKey,
        'auth_files.download_button'
      ).props.onClick();
    });
    expect(mocks.handleDownload).toHaveBeenCalledWith('codex.json');

    await act(async () => {
      findAccountCardButtonByAriaLabel(
        renderer,
        selectionKey,
        'auth_files.delete_button'
      ).props.onClick();
    });
    expect(mocks.handleDelete).toHaveBeenCalledWith(mocks.files[0]);

    await act(async () => {
      const statusToggle = findAccountCardInputByAriaLabel(
        renderer,
        selectionKey,
        'auth_files.status_toggle_label'
      );
      expect(statusToggle.props.checked).toBe(true);
      statusToggle.props.onChange({ target: { checked: false } });
      await Promise.resolve();
    });
    expect(mocks.batchSetStatus).toHaveBeenCalledWith(
      [getAuthFilePatchTarget(mocks.files[0])],
      false
    );

    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    expect(treeText(renderer)).toContain('accounts.detail_tab_overview');
  });

  it('renders a decision-first overview', async () => {
    const renderer = await renderAccountsPage();

    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await flushPromises();

    expect(renderer.root.findAllByProps({ 'data-overview-section': 'decision' })).toHaveLength(1);
    const overviewSectionNames = renderer.root
      .findAll((node) => typeof node.props['data-overview-section'] === 'string')
      .map((node) => node.props['data-overview-section']);
    expect(overviewSectionNames).toEqual([
      'decision',
      'recent-status',
      'capacity',
      'credential',
      'activity',
    ]);
    expect(renderer.root.findAllByProps({ 'data-overview-section': 'recent-status' })).toHaveLength(
      1
    );
    expect(renderer.root.findAllByProps({ 'data-overview-section': 'capacity' })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-overview-section': 'credential' })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-overview-section': 'activity' })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-overview-section': 'attention' })).toHaveLength(0);
    const recentStatusSection = renderer.root.findByProps({
      'data-overview-section': 'recent-status',
    });
    expect(recentStatusSection.props['data-overview-recent-status-empty']).toBe(true);
    expect(
      recentStatusSection.findAllByProps({ 'data-overview-recent-status-empty-message': 'true' })
    ).toHaveLength(1);
    const recentStatusBar = recentStatusSection.findByType(ProviderStatusBar);
    expect(recentStatusBar.props.statusData.blockDetails).toHaveLength(20);
    expect(recentStatusBar.props.statusData.totalSuccess).toBe(0);
    expect(recentStatusBar.props.statusData.totalFailure).toBe(0);
    expect(
      renderer.root.findAllByProps({ 'data-overview-activity-scope': 'recent_snapshot' })
    ).toHaveLength(1);
    const overviewText = treeText(renderer);
    expect(overviewText).toContain('accounts.detail_overview_decision_title');
    expect(overviewText).toContain('accounts.detail_overview_capacity_title');
    expect(overviewText).toContain('accounts.detail_overview_credential_title');
    expect(overviewText).toContain('accounts.detail_overview_activity_title');
    expect(overviewText).toContain('accounts.detail_overview_activity_scope_recent');
    [
      'accounts.detail_overview_decision_eyebrow',
      'accounts.detail_overview_capacity_eyebrow',
      'accounts.detail_overview_credential_eyebrow',
      'accounts.detail_overview_credential_desc',
      'accounts.detail_overview_activity_eyebrow',
      'accounts.detail_overview_activity_source',
    ].forEach((key) => expect(overviewText).not.toContain(key));
  });

  it('aggregates recent request buckets and shows the current status explanation', async () => {
    mocks.files = [
      {
        ...makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
        recent_requests: [
          { success: 2, failed: 1 },
          { success: 3, failed: 0 },
        ],
        status_message: 'rate_limit_reached',
      } as AuthFileItem,
    ];
    const renderer = await renderAccountsPage();

    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await flushPromises();

    const recentStatusSection = renderer.root.findByProps({
      'data-overview-section': 'recent-status',
    });
    expect(recentStatusSection.props['data-overview-recent-status-empty']).toBe(false);
    expect(
      recentStatusSection.findAllByProps({ 'data-overview-recent-status-empty-message': 'true' })
    ).toHaveLength(0);
    expect(
      recentStatusSection.findAllByProps({ 'data-overview-recent-status-message': 'true' })
    ).toHaveLength(1);
    expect(readText(recentStatusSection)).toContain('rate_limit_reached');

    const recentStatusBar = recentStatusSection.findByType(ProviderStatusBar);
    expect(recentStatusBar.props.statusData).toMatchObject({
      totalSuccess: 5,
      totalFailure: 1,
    });
    expect(recentStatusBar.props.statusData.blockDetails).toHaveLength(20);
  });

  it('loads and renders matching pending actions in the overview', async () => {
    mocks.listAccountActionCandidates.mockResolvedValue({
      pendingCount: 1,
      items: [
        {
          id: 1,
          actionType: 'reauth',
          status: 'pending',
          provider: 'codex',
          authFileName: 'codex.json',
          authIndex: 'auth-1',
          accountSnapshot: 'codex@example.com',
          authLabel: 'codex@example.com',
          reason: 'expired',
          firstSeenAtMs: 100,
          lastSeenAtMs: 200,
          hitCount: 1,
          createdAtMs: 100,
          updatedAtMs: 200,
        },
      ],
    });
    const renderer = await renderAccountsPage();

    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await flushPromises();

    expect(mocks.listAccountActionCandidates).toHaveBeenCalledTimes(1);
    expect(renderer.root.findAllByProps({ 'data-overview-section': 'attention' })).toHaveLength(1);
    expect(treeText(renderer)).toContain('accounts.detail_overview_attention_candidates');
  });

  it('navigates between the overview and contextual detail tabs', async () => {
    const renderer = await renderAccountsPage();

    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });

    expect(findHostButtonByText(renderer, 'accounts.detail_tab_quota').props['aria-selected']).toBe(
      true
    );
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_overview').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_config').props.onClick();
    });

    expect(
      findHostButtonByText(renderer, 'accounts.detail_tab_config').props['aria-selected']
    ).toBe(true);
    expect(mocks.configurationEnabledCalls).toContain(true);
  });

  it('uses the fixed seven-day monitoring range for overview activity', async () => {
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getAnalytics.mockImplementation(
      async (_base: string, _key: string | undefined, request: unknown) => {
        const analyticsRequest = request as AnalyticsRequestForTest;
        if (analyticsRequest.include?.events_page) {
          return makeEventsResponse(makeAnalyticsEvent({}));
        }
        return {
          generated_at_ms: 1,
          granularity: 'day',
          account_stats: [
            {
              id: 'codex-overview',
              account_snapshot: 'codex@example.com',
              auth_label_snapshot: 'codex@example.com',
              auth_provider_snapshot: 'codex',
              auth_indices: ['auth-1'],
              sources: ['codex.json'],
              calls: 8,
              success_rate: 0.875,
              input_tokens: 800,
              output_tokens: 200,
              cost: 0.42,
              last_seen_ms: new Date(2026, 7, 26, 17, 44, 5, 0).getTime(),
            },
          ],
          timeline: [],
        };
      }
    );
    const renderer = await renderAccountsPage();

    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await flushPromises();

    expect(
      renderer.root.findAllByProps({ 'data-overview-activity-scope': 'monitoring_7d' })
    ).toHaveLength(1);
    const lastActiveMetric = renderer.root.findByProps({
      'data-overview-metric-key': 'lastSeenMs',
    });
    const activitySummaryGrid = renderer.root.findByProps({
      'data-usage-summary-density': 'compact',
    });
    expect(activitySummaryGrid.findAllByProps({ 'data-usage-summary-meta': 'true' })).toHaveLength(
      0
    );
    expect(activitySummaryGrid.findAllByProps({ 'data-usage-summary-chart': 'true' })).toHaveLength(
      0
    );
    expect(activitySummaryGrid.findAllByProps({ role: 'tooltip' })).toHaveLength(0);
    expect(lastActiveMetric.props['data-overview-metric-kind']).toBe('timestamp');
    expect(readText(lastActiveMetric)).toContain('accounts.detail_overview_activity_last_active');
    expect(readText(lastActiveMetric)).toContain('08/26 17:44');
    expect(lastActiveMetric.findByType('strong').props.title).toBeTruthy();
    const overviewCall = mocks.getAnalytics.mock.calls.find(
      (call) => (call[2] as AnalyticsRequestForTest).include?.account_stats === true
    );
    const overviewRequest = overviewCall?.[2] as AnalyticsRequestForTest | undefined;
    expect((overviewRequest?.to_ms ?? 0) - (overviewRequest?.from_ms ?? 0)).toBe(
      7 * 24 * 60 * 60 * 1000
    );
    expect(overviewRequest?.filters).toEqual({
      auth_files: ['codex.json'],
      auth_indices: ['auth-1'],
    });
  });

  it('shows an empty seven-day monitoring state instead of stale recent activity', async () => {
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getAnalytics.mockImplementation(defaultGetAnalytics);
    const renderer = await renderAccountsPage();

    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await flushPromises();

    expect(
      renderer.root.findAllByProps({ 'data-overview-activity-scope': 'monitoring_7d' })
    ).toHaveLength(1);
    expect(treeText(renderer)).toContain('accounts.detail_overview_activity_empty_7d');
    expect(treeText(renderer)).not.toContain('accounts.detail_overview_activity_scope_recent');
  });

  it('uses the filtered overview summary when one credential has split account stats', async () => {
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getAnalytics.mockImplementation(
      async (_base: string, _key: string | undefined, request: unknown) => {
        const analyticsRequest = request as AnalyticsRequestForTest;
        if (analyticsRequest.include?.events_page) {
          return makeEventsResponse(makeAnalyticsEvent({}));
        }
        return {
          generated_at_ms: 1,
          granularity: 'day',
          summary: {
            total_calls: 15,
            success_calls: 12,
            failure_calls: 3,
            success_rate: 0.8,
            input_tokens: 1_000,
            output_tokens: 300,
            total_tokens: 1_500,
            total_cost: 0.75,
          },
          account_stats: [
            {
              id: 'old-label',
              account_snapshot: 'old@example.com',
              auth_label_snapshot: 'old@example.com',
              auth_provider_snapshot: 'codex',
              auth_indices: ['auth-1'],
              sources: ['codex.json'],
              calls: 6,
              success_rate: 0.5,
              input_tokens: 100,
              output_tokens: 20,
              cost: 0.1,
              last_seen_ms: 1_700_000_000_100,
            },
            {
              id: 'current-label',
              account_snapshot: 'codex@example.com',
              auth_label_snapshot: 'codex@example.com',
              auth_provider_snapshot: 'codex',
              auth_indices: ['auth-1'],
              sources: ['codex.json'],
              calls: 9,
              success_rate: 1,
              input_tokens: 200,
              output_tokens: 30,
              cost: 0.2,
              last_seen_ms: 1_700_000_000_900,
            },
          ],
          timeline: [],
        };
      }
    );
    const renderer = await renderAccountsPage();

    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await flushPromises();

    expect(
      readText(renderer.root.findByProps({ 'data-overview-metric-key': 'requests' }))
    ).toContain('15');
    expect(readText(renderer.root.findByProps({ 'data-overview-metric-key': 'tokens' }))).toContain(
      '1.5K'
    );
    expect(readText(renderer.root.findByProps({ 'data-overview-metric-key': 'cost' }))).toContain(
      '$0.750'
    );
  });

  it('reuses loaded overview and diagnostic analytics until the user refreshes', async () => {
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    const renderer = await renderAccountsPage();
    mocks.getAnalytics.mockClear();

    const countAnalyticsRequests = () => {
      const requests = mocks.getAnalytics.mock.calls.map(
        (call) => call[2] as AnalyticsRequestForTest
      );
      return {
        overview: requests.filter((request) => !request.include?.events_page).length,
        diagnostics: requests.filter((request) => request.include?.events_page).length,
      };
    };

    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await flushPromises();
    expect(countAnalyticsRequests()).toEqual({ overview: 1, diagnostics: 0 });

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_diagnostics').props.onClick();
    });
    await flushPromises();
    expect(countAnalyticsRequests()).toEqual({ overview: 1, diagnostics: 1 });

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_overview').props.onClick();
    });
    await flushPromises();
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_diagnostics').props.onClick();
    });
    await flushPromises();
    expect(countAnalyticsRequests()).toEqual({ overview: 1, diagnostics: 1 });

    await act(async () => {
      renderer.root.findByType(AccountDiagnosticsTab).props.onRefreshEvents();
    });
    await flushPromises();
    expect(countAnalyticsRequests()).toEqual({ overview: 1, diagnostics: 2 });
  });

  it('ignores stale overview activity responses after switching rows', async () => {
    mocks.files = [
      makeCodexFile('codex-a.json', 'auth-a', 'first@example.com'),
      makeCodexFile('codex-b.json', 'auth-b', 'second@example.com'),
    ];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };

    const firstActivity = createDeferred<AnalyticsResponseForTest>();
    const secondActivity = createDeferred<AnalyticsResponseForTest>();
    mocks.getAnalytics.mockImplementation(
      async (_base: string, _key: string | undefined, request: unknown) => {
        const analyticsRequest = request as AnalyticsRequestForTest;
        const fileName = analyticsRequest.filters?.auth_files?.[0];
        if (fileName === 'codex-a.json') return firstActivity.promise;
        if (fileName === 'codex-b.json') return secondActivity.promise;
        return makeEmptyAnalyticsResponse();
      }
    );

    const renderer = await renderAccountsPage();

    await act(async () => {
      findDetailButtonByName(renderer, 'codex-a.json').props.onClick();
      await Promise.resolve();
    });
    await act(async () => {
      findDetailButtonByName(renderer, 'codex-b.json').props.onClick();
      await Promise.resolve();
    });

    await act(async () => {
      secondActivity.resolve({
        generated_at_ms: 2,
        granularity: 'day',
        account_stats: [
          {
            id: 'codex-b-overview',
            account_snapshot: 'second@example.com',
            auth_label_snapshot: 'second@example.com',
            auth_provider_snapshot: 'codex',
            auth_indices: ['auth-b'],
            sources: ['codex-b.json'],
            calls: 22,
            success_rate: 1,
            input_tokens: 220,
            output_tokens: 22,
            cost: 0.22,
            last_seen_ms: 1_700_000_000_022,
          },
        ],
        timeline: [],
      });
      await Promise.resolve();
    });

    expect(
      readText(renderer.root.findByProps({ 'data-overview-metric-key': 'requests' }))
    ).toContain('22');

    await act(async () => {
      firstActivity.resolve({
        generated_at_ms: 1,
        granularity: 'day',
        account_stats: [
          {
            id: 'codex-a-overview',
            account_snapshot: 'first@example.com',
            auth_label_snapshot: 'first@example.com',
            auth_provider_snapshot: 'codex',
            auth_indices: ['auth-a'],
            sources: ['codex-a.json'],
            calls: 11,
            success_rate: 1,
            input_tokens: 110,
            output_tokens: 11,
            cost: 0.11,
            last_seen_ms: 1_700_000_000_011,
          },
        ],
        timeline: [],
      });
      await Promise.resolve();
    });

    const requestsMetric = renderer.root.findByProps({
      'data-overview-metric-key': 'requests',
    });
    expect(readText(requestsMetric)).toContain('22');
    expect(readText(requestsMetric)).not.toContain('11');
  });

  it('reloads overview analytics after an in-flight request is invalidated by closing the drawer', async () => {
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    const firstActivity = createDeferred<AnalyticsResponseForTest>();
    let overviewRequestCount = 0;
    mocks.getAnalytics.mockImplementation(
      async (_base: string, _key: string | undefined, request: unknown) => {
        const analyticsRequest = request as AnalyticsRequestForTest;
        if (analyticsRequest.include?.events_page) {
          return makeEventsResponse(makeAnalyticsEvent({}));
        }
        overviewRequestCount += 1;
        if (overviewRequestCount === 1) return firstActivity.promise;
        return {
          generated_at_ms: 2,
          granularity: 'day',
          account_stats: [
            {
              id: 'codex-reloaded',
              account_snapshot: 'codex@example.com',
              auth_label_snapshot: 'codex@example.com',
              auth_provider_snapshot: 'codex',
              auth_indices: ['auth-1'],
              sources: ['codex.json'],
              calls: 33,
              success_rate: 1,
              input_tokens: 330,
              output_tokens: 33,
              cost: 0.33,
              last_seen_ms: 1_700_000_000_033,
            },
          ],
          timeline: [],
        };
      }
    );

    const renderer = await renderAccountsPage();
    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
      await Promise.resolve();
    });
    await act(async () => {
      renderer.root.findByType(Drawer).props.onClose();
    });
    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await flushPromises();

    expect(overviewRequestCount).toBe(2);
    expect(
      readText(renderer.root.findByProps({ 'data-overview-metric-key': 'requests' }))
    ).toContain('33');

    firstActivity.resolve({
      generated_at_ms: 1,
      granularity: 'day',
      account_stats: [],
      timeline: [],
    });
    await flushPromises();
    expect(
      readText(renderer.root.findByProps({ 'data-overview-metric-key': 'requests' }))
    ).toContain('33');
  });

  it('keeps the row quota refresh isolated from Manager history', async () => {
    mocks.files = [makeCodexFile('codex-row.json', 'auth-row', 'row@example.com')];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    const quotaFetch = vi.spyOn(CODEX_CONFIG, 'fetchQuota').mockResolvedValue(makeCodexQuotaData());

    const renderer = await renderAccountsPage();
    await flushPromises();
    mocks.getAccountHistory.mockClear();

    await act(async () => {
      findAccountCardButtonByAriaLabel(
        renderer,
        'codex-row.json\u0000auth-row',
        'accounts.refresh_quota'
      ).props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(quotaFetch).toHaveBeenCalledTimes(1);
    expect(mocks.getAccountHistory).not.toHaveBeenCalled();
  });

  it('clears stale single-account history when the refresh response cannot be correlated', async () => {
    mocks.files = [
      {
        ...makeCodexFile('stale.json', 'auth-stale', 'stale@example.com'),
        type: 'generic',
        provider: 'generic',
      } as AuthFileItem,
    ];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getAccountHistory
      .mockResolvedValueOnce(
        makeAccountHistoryResponse([
          {
            row_key: 'stale.json\u0000auth-stale',
            account_key: 'opaque-stale',
            matched: true,
            total_requests: 777,
            success_calls: 700,
            failure_calls: 77,
            total_tokens: 123456,
            total_cost: 7.77,
            success_rate: 0.9,
            first_seen_ms: 1,
            last_seen_ms: 2,
            sync_status: 'ready',
          },
        ])
      )
      .mockResolvedValueOnce(
        makeAccountHistoryResponse([
          {
            row_key: 'unexpected-row',
            account_key: 'opaque-unexpected',
            matched: true,
            total_requests: 999,
            success_calls: 999,
            failure_calls: 0,
            total_tokens: 999999,
            total_cost: 9.99,
            success_rate: 1,
            first_seen_ms: 1,
            last_seen_ms: 2,
            sync_status: 'ready',
          },
        ])
      );

    const renderer = await renderAccountsPage();
    await flushPromises();
    expect(getAccountListItemTexts(renderer).join('\n')).toContain('777');

    await act(async () => {
      findDetailButtonByName(renderer, 'stale.json').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    await flushPromises();

    await act(async () => {
      await renderer.root.findByType(AccountQuotaTab).props.onRefreshHistory();
    });
    await flushPromises();

    const cardText = getAccountListItemTexts(renderer).join('\n');
    expect(cardText).not.toContain('777');
    expect(cardText).not.toContain('999');
  });

  it('keeps a newer targeted history result when an older page request finishes later', async () => {
    const firstFile = {
      ...makeCodexFile('generic-a.json', 'auth-a', 'a@example.com'),
      type: 'generic',
      provider: 'generic',
    } as AuthFileItem;
    const secondFile = {
      ...makeCodexFile('generic-b.json', 'auth-b', 'b@example.com'),
      type: 'generic',
      provider: 'generic',
    } as AuthFileItem;
    mocks.files = [firstFile, secondFile];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    const pageHistory = createDeferred<AccountHistoryResponseForTest>();
    mocks.getAccountHistory
      .mockImplementationOnce(() => pageHistory.promise)
      .mockResolvedValueOnce(
        makeAccountHistoryResponse([
          {
            row_key: 'generic-a.json\u0000auth-a',
            account_key: 'generic-a',
            matched: true,
            total_requests: 777,
            success_calls: 700,
            failure_calls: 77,
            total_tokens: 7_777,
            total_cost: 7.77,
            success_rate: 0.9,
            first_seen_ms: 1,
            last_seen_ms: 2,
            sync_status: 'ready',
          },
        ])
      );

    const renderer = await renderAccountsPage();
    await act(async () => {
      findDetailButtonByName(renderer, 'generic-a.json').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    await flushPromises();

    await act(async () => {
      await renderer.root.findByType(AccountQuotaTab).props.onRefreshHistory();
    });
    expect(readText(findAccountCardByKey(renderer, 'generic-a.json\u0000auth-a'))).toContain('777');

    pageHistory.resolve(
      makeAccountHistoryResponse([
        {
          row_key: 'generic-a.json\u0000auth-a',
          account_key: 'generic-a',
          matched: true,
          total_requests: 111,
          success_calls: 100,
          failure_calls: 11,
          total_tokens: 1_111,
          total_cost: 1.11,
          success_rate: 0.9,
          first_seen_ms: 1,
          last_seen_ms: 2,
          sync_status: 'ready',
        },
        {
          row_key: 'generic-b.json\u0000auth-b',
          account_key: 'generic-b',
          matched: true,
          total_requests: 222,
          success_calls: 200,
          failure_calls: 22,
          total_tokens: 2_222,
          total_cost: 2.22,
          success_rate: 0.9,
          first_seen_ms: 1,
          last_seen_ms: 2,
          sync_status: 'ready',
        },
      ])
    );
    await flushPromises();

    expect(readText(findAccountCardByKey(renderer, 'generic-a.json\u0000auth-a'))).toContain('777');
    expect(readText(findAccountCardByKey(renderer, 'generic-a.json\u0000auth-a'))).not.toContain(
      '111'
    );
    expect(readText(findAccountCardByKey(renderer, 'generic-b.json\u0000auth-b'))).toContain('222');
  });

  it('does not let an older page history failure mark a newer targeted result unavailable', async () => {
    const firstFile = {
      ...makeCodexFile('generic-a.json', 'auth-a', 'a@example.com'),
      type: 'generic',
      provider: 'generic',
    } as AuthFileItem;
    const secondFile = {
      ...makeCodexFile('generic-b.json', 'auth-b', 'b@example.com'),
      type: 'generic',
      provider: 'generic',
    } as AuthFileItem;
    mocks.files = [firstFile, secondFile];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    const pageHistory = createDeferred<AccountHistoryResponseForTest>();
    mocks.getAccountHistory
      .mockImplementationOnce(() => pageHistory.promise)
      .mockResolvedValueOnce(
        makeAccountHistoryResponse([
          {
            row_key: 'generic-a.json\u0000auth-a',
            account_key: 'generic-a',
            matched: true,
            total_requests: 777,
            success_calls: 700,
            failure_calls: 77,
            total_tokens: 7_777,
            total_cost: 7.77,
            success_rate: 0.9,
            first_seen_ms: 1,
            last_seen_ms: 2,
            sync_status: 'ready',
          },
        ])
      );

    const renderer = await renderAccountsPage();
    await act(async () => {
      findDetailButtonByName(renderer, 'generic-a.json').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    await flushPromises();

    await act(async () => {
      await renderer.root.findByType(AccountQuotaTab).props.onRefreshHistory();
    });
    expect(readText(findAccountCardByKey(renderer, 'generic-a.json\u0000auth-a'))).toContain('777');

    pageHistory.reject(new Error('page history offline'));
    await flushPromises();

    const refreshedCardText = readText(
      findAccountCardByKey(renderer, 'generic-a.json\u0000auth-a')
    );
    expect(refreshedCardText).toContain('777');
    expect(refreshedCardText).not.toContain('accounts.history_recent_fallback');
    expect(refreshedCardText).not.toContain('accounts.history_unavailable');
    expect(readText(findAccountCardByKey(renderer, 'generic-b.json\u0000auth-b'))).toContain(
      'accounts.history_unavailable'
    );
  });

  it('cancels a manual history refresh across capability changes without blocking the next refresh', async () => {
    const file = {
      ...makeCodexFile('generic-history.json', 'auth-history', 'history@example.com'),
      type: 'generic',
      provider: 'generic',
    } as AuthFileItem;
    mocks.files = [file];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };

    const renderer = await renderAccountsPage();
    await flushPromises();
    await act(async () => {
      findDetailButtonByName(renderer, 'generic-history.json').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    await flushPromises();

    const previousRefresh = createDeferred<AccountHistoryResponseForTest>();
    const nextRefresh = createDeferred<AccountHistoryResponseForTest>();
    let refreshCall = 0;
    mocks.getAccountHistory.mockClear();
    mocks.getAccountHistory.mockImplementation(() => {
      refreshCall += 1;
      if (refreshCall === 1) return previousRefresh.promise;
      if (refreshCall === 3) return nextRefresh.promise;
      return Promise.resolve(makeAccountHistoryResponse([]));
    });

    await act(async () => {
      renderer.root.findByType(AccountQuotaTab).props.onRefreshHistory();
      await Promise.resolve();
    });

    expect(mocks.getAccountHistory).toHaveBeenCalledTimes(1);
    const previousSignal = mocks.getAccountHistory.mock.calls[0]?.[3] as AbortSignal | undefined;
    expect(previousSignal?.aborted).toBe(false);
    expect(renderer.root.findByType(AccountQuotaTab).props.historyRefreshing).toBe(true);

    mocks.panelFeatureAvailability = {
      ...mocks.panelFeatureAvailability,
      checking: true,
    };
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });

    expect(previousSignal?.aborted).toBe(true);
    expect(renderer.root.findByType(AccountQuotaTab).props.historyRefreshing).toBe(false);

    mocks.panelFeatureAvailability = {
      ...mocks.panelFeatureAvailability,
      checking: false,
    };
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await flushPromises();
    expect(mocks.getAccountHistory).toHaveBeenCalledTimes(2);

    await act(async () => {
      renderer.root.findByType(AccountQuotaTab).props.onRefreshHistory();
      await Promise.resolve();
    });

    expect(mocks.getAccountHistory).toHaveBeenCalledTimes(3);
    expect(renderer.root.findByType(AccountQuotaTab).props.historyRefreshing).toBe(true);

    previousRefresh.resolve(makeAccountHistoryResponse([]));
    await flushPromises();
    expect(renderer.root.findByType(AccountQuotaTab).props.historyRefreshing).toBe(true);

    nextRefresh.resolve(makeAccountHistoryResponse([]));
    await flushPromises();
    expect(renderer.root.findByType(AccountQuotaTab).props.historyRefreshing).toBe(false);
  });

  it('refreshes only the visible page and ignores a repeated batch trigger', async () => {
    mocks.files = Array.from({ length: 11 }, (_, index) =>
      makeCodexFile(
        `codex-page-${String(index + 1).padStart(2, '0')}.json`,
        `auth-page-${index + 1}`,
        `page-${index + 1}@example.com`
      )
    );
    const quotaResult = createDeferred<CodexQuotaData>();
    const quotaFetch = vi
      .spyOn(CODEX_CONFIG, 'fetchQuota')
      .mockImplementation(() => quotaResult.promise);
    const renderer = await renderAccountsPage();
    const visibleNames = renderer.root
      .findAll(
        (node) => node.type === 'article' && typeof node.props['data-account-card'] === 'string'
      )
      .map((node) => String(node.props['data-account-card']).split('\u0000')[0])
      .sort();
    expect(visibleNames).toHaveLength(10);

    const refreshButton = findButtonByText(renderer, 'accounts.refresh_quota');
    let firstRefresh!: Promise<void>;
    let repeatedRefresh!: Promise<void>;
    await act(async () => {
      firstRefresh = refreshButton.props.onClick();
      repeatedRefresh = refreshButton.props.onClick();
      await Promise.resolve();
    });

    expect(quotaFetch).toHaveBeenCalledTimes(1);
    quotaResult.resolve(makeCodexQuotaData());
    await act(async () => {
      await Promise.all([firstRefresh, repeatedRefresh]);
    });

    expect(quotaFetch).toHaveBeenCalledTimes(10);
    expect(quotaFetch.mock.calls.map(([file]) => file.name).sort()).toEqual(visibleNames);
  });

  it('keeps the last successful quota visible while a manual refresh is pending or fails', async () => {
    const file = makeCodexFile('codex-preserved.json', 'auth-preserved', 'preserved@example.com');
    mocks.files = [file];
    const storeKey = getQuotaCredentialStoreKey(file);
    const previousQuota = {
      status: 'success' as const,
      windows: [
        {
          id: 'weekly',
          label: 'Weekly',
          usedPercent: 25,
          resetLabel: 'later',
          resetAtMs: Date.now() + 60_000,
          resetAccuracy: 'exact' as const,
          limitWindowSeconds: 7 * 24 * 60 * 60,
        },
      ],
      quotaInventoryObserved: true,
      ...buildQuotaCredentialIdentity(file),
      fetchedAtMs: 1,
    };
    mocks.quotaState.codexQuota = { [storeKey]: previousQuota };
    const quotaResult = createDeferred<CodexQuotaData>();
    vi.spyOn(CODEX_CONFIG, 'fetchQuota').mockImplementation(() => quotaResult.promise);
    const renderer = await renderAccountsPage();

    let refreshPromise!: Promise<void>;
    await act(async () => {
      refreshPromise = findAccountCardButtonByAriaLabel(
        renderer,
        'codex-preserved.json\u0000auth-preserved',
        'accounts.refresh_quota'
      ).props.onClick();
      await Promise.resolve();
    });

    expect(mocks.quotaState.setCodexQuota).not.toHaveBeenCalled();
    quotaResult.reject(new Error('provider unavailable'));
    await act(async () => {
      await refreshPromise;
    });

    expect(mocks.quotaState.setCodexQuota).toHaveBeenCalledTimes(1);
    const updater = mocks.quotaState.setCodexQuota.mock.calls[0]?.[0] as (
      current: Record<string, CodexQuotaState>
    ) => Record<string, CodexQuotaState>;
    const failedState = updater(mocks.quotaState.codexQuota as Record<string, CodexQuotaState>)[
      storeKey
    ];
    expect(failedState.status).toBe('error');
    expect(failedState.windows).toEqual(previousQuota.windows);
  });

  it('keeps the last successful Codex quota visible while a reset is pending or fails', async () => {
    const file = makeCodexFile('codex-reset.json', 'auth-reset', 'reset@example.com');
    mocks.files = [file];
    const storeKey = getQuotaCredentialStoreKey(file);
    const previousQuota = {
      status: 'success' as const,
      windows: [
        {
          id: 'weekly',
          label: 'Weekly',
          usedPercent: 40,
          resetLabel: 'later',
          resetAtMs: Date.now() + 60_000,
          resetAccuracy: 'exact' as const,
          limitWindowSeconds: 7 * 24 * 60 * 60,
        },
      ],
      quotaInventoryObserved: true,
      rateLimitResetCreditsAvailableCount: 1,
      ...buildQuotaCredentialIdentity(file),
      fetchedAtMs: 1,
    };
    mocks.quotaState.codexQuota = { [storeKey]: previousQuota };
    const resetResult = createDeferred<CodexQuotaData>();
    vi.spyOn(CODEX_CONFIG, 'resetQuota').mockImplementation(() => resetResult.promise);
    const renderer = await renderAccountsPage();

    await act(async () => {
      findDetailButtonByName(renderer, 'codex-reset.json').props.onClick();
    });
    await act(async () => {
      findDrawerMoreItem(renderer, 'reset-codex-quota').onClick();
    });
    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as {
      onConfirm: () => Promise<void>;
    };

    let resetPromise!: Promise<void>;
    await act(async () => {
      resetPromise = confirmation.onConfirm();
      await Promise.resolve();
    });

    expect(mocks.quotaState.setCodexQuota).not.toHaveBeenCalled();
    resetResult.reject(new Error('reset unavailable'));
    await act(async () => {
      await resetPromise;
    });

    expect(mocks.quotaState.setCodexQuota).toHaveBeenCalledTimes(1);
    const updater = mocks.quotaState.setCodexQuota.mock.calls[0]?.[0] as (
      current: Record<string, CodexQuotaState>
    ) => Record<string, CodexQuotaState>;
    const failedState = updater(mocks.quotaState.codexQuota as Record<string, CodexQuotaState>)[
      storeKey
    ];
    expect(failedState.status).toBe('error');
    expect(failedState.windows).toEqual(previousQuota.windows);
  });

  it('keeps auth-file selection helpers in accounts selection mode', async () => {
    mocks.files = [
      makeCodexFile('codex-page.json', 'auth-1', 'page@example.com'),
      makeCodexFile('codex-filtered.json', 'auth-2', 'filtered@example.com'),
    ];
    const renderer = await renderAccountsPage();

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.selection_mode_enter').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'auth_files.batch_select_page').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'auth_files.batch_select_filtered').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'auth_files.batch_invert_page').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'auth_files.batch_deselect').props.onClick();
    });

    expect(mocks.selectAllVisible).toHaveBeenCalledTimes(2);
    expect(
      mocks.selectAllVisible.mock.calls[0][0].map((item: AuthFileItem) => item.name).sort()
    ).toEqual(['codex-filtered.json', 'codex-page.json']);
    expect(
      mocks.selectAllVisible.mock.calls[1][0].map((item: AuthFileItem) => item.name).sort()
    ).toEqual(['codex-filtered.json', 'codex-page.json']);
    expect(mocks.invertVisibleSelection).toHaveBeenCalledTimes(1);
    expect(mocks.deselectAll).toHaveBeenCalledTimes(1);
  });

  it('renders account history from rollup data instead of monitoring account stats or auth-file health', async () => {
    mocks.files = [
      {
        ...makeCodexFile('healthy.json', 'auth-1', 'healthy@example.com'),
        success: 87,
        failed: 3,
        recent_requests: [{ success: 128, failed: 0 }],
      },
    ];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getAnalytics.mockResolvedValue({
      generated_at_ms: 1,
      granularity: 'day',
      account_stats: [
        {
          id: 'healthy-monitoring',
          account_snapshot: 'healthy@example.com',
          auth_label_snapshot: 'healthy@example.com',
          auth_provider_snapshot: 'codex',
          auth_indices: ['auth-1'],
          sources: ['healthy.json'],
          calls: 999,
          success_rate: 0.01,
          input_tokens: 0,
          output_tokens: 0,
          cost: 0,
          last_seen_ms: 1,
        },
      ],
      timeline: [],
    });
    mocks.getAccountHistory.mockResolvedValue(
      makeAccountHistoryResponse([
        {
          row_key: 'healthy.json\u0000auth-1',
          account_key: 'healthy@example.com',
          matched: true,
          total_requests: 1234,
          success_calls: 1218,
          failure_calls: 16,
          total_tokens: 5678900,
          total_cost: 12.34,
          success_rate: 0.987,
          first_seen_ms: 1,
          last_seen_ms: 2,
          sync_status: 'ready',
        },
      ])
    );

    const renderer = await renderAccountsPage();
    await flushPromises();
    const cardText = getAccountListItemTexts(renderer).join('\n');

    expect(mocks.getAccountHistory).toHaveBeenCalledWith(
      'http://manager.local:18317',
      'manager-key',
      {
        accounts: [
          {
            row_key: 'healthy.json\u0000auth-1',
            account_snapshot: 'healthy@example.com',
            auth_label_snapshot: undefined,
            auth_file_snapshot: 'healthy.json',
            auth_provider_snapshot: 'codex',
            auth_project_id_snapshot: undefined,
            auth_index: 'auth-1',
            source: 'healthy.json',
          },
        ],
      },
      expect.anything()
    );
    const accountHistoryRequest = mocks.getAccountHistory.mock.calls[0]?.[2];
    expect(accountHistoryRequest).not.toHaveProperty('catch_up');
    expect(cardText).toContain('1.2K');
    expect(cardText).toContain('5.7M');
    expect(cardText).toContain('$12.340');
    expect(cardText).toContain('98.7%');
    expect(cardText).not.toContain('accounts.history_requests');
    expect(cardText).not.toContain('accounts.history_tokens');
    expect(cardText).not.toContain('accounts.history_cost');
    expect(cardText).not.toContain('accounts.history_success');
    expect(cardText).not.toContain('stats.success 87');
    expect(cardText).not.toContain('stats.failure 3');
    expect(cardText).not.toContain('auth_files.health_status_label');
    expect(cardText).not.toContain('accounts.activity_success_failure');
    expect(cardText).not.toContain('999');

    await act(async () => {
      findDetailButtonByName(renderer, 'healthy.json').props.onClick();
    });
    await flushPromises();
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    await flushPromises();

    const quotaSummary = renderer.root.findByProps({
      'data-account-quota-usage-summary': 'true',
    });
    expect(
      quotaSummary.findAllByProps({ 'data-account-quota-metric-header': 'true' })
    ).toHaveLength(4);
    expect(quotaSummary.findAllByProps({ 'data-account-quota-metric-value': 'true' })).toHaveLength(
      4
    );
    const compactSummaryValues = quotaSummary
      .findAll(
        (node) => node.type === 'strong' && typeof node.props['aria-describedby'] === 'string'
      )
      .map((node) => readText(node));
    expect(compactSummaryValues).toEqual(expect.arrayContaining(['1.2K', '5.7M']));
    const summaryTooltips = quotaSummary
      .findAll((node) => node.props.role === 'tooltip')
      .map((node) => readText(node));
    expect(summaryTooltips).toEqual(
      expect.arrayContaining([
        'accounts.detail_total_requests1,234',
        'accounts.detail_total_tokens5,678,900',
      ])
    );
  });

  it('renders the latest real request from the existing account-history response without polling again', async () => {
    mocks.files = [makeCodexFile('latest.json', 'auth-latest', 'latest@example.com')];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getAccountHistory.mockResolvedValue(
      makeAccountHistoryResponse([
        {
          row_key: 'latest.json\u0000auth-latest',
          account_key: 'latest@example.com',
          matched: true,
          total_requests: 1,
          success_calls: 0,
          failure_calls: 1,
          total_tokens: 0,
          total_cost: 0,
          success_rate: 0,
          first_seen_ms: 1,
          last_seen_ms: 2,
          latest_request: {
            timestamp_ms: 1_700_000_000_000,
            failed: true,
            fail_status_code: 429,
            fail_summary: 'rate limit exceeded',
            header_error_kind: 'rate_limit',
            header_error_code: 'quota_exceeded',
          },
          recent_requests: [
            {
              timestamp_ms: 1_700_000_000_000,
              failed: true,
              fail_status_code: 429,
              fail_summary: 'rate limit exceeded',
              header_error_kind: 'rate_limit',
              header_error_code: 'quota_exceeded',
            },
            { timestamp_ms: 1_699_999_999_000, failed: false },
            { timestamp_ms: 1_699_999_998_000, failed: true },
          ],
          sync_status: 'ready',
        },
      ])
    );

    const renderer = await renderAccountsPage();
    await flushPromises();

    const statusTrack = renderer.root.findByProps({
      'data-account-request-status-track': 'true',
    });
    const renderedStatuses = statusTrack
      .findAll((node) => typeof node.props['data-request-status'] === 'string')
      .map((node) => node.props['data-request-status']);
    expect(renderedStatuses.slice(-3)).toEqual(['failed', 'success', 'failed']);
    expect(renderedStatuses.slice(0, -3).every((status) => status === 'empty')).toBe(true);
    const settledHistoryCallCount = mocks.getAccountHistory.mock.calls.length;
    expect(settledHistoryCallCount).toBeGreaterThan(0);

    await flushPromises();
    expect(mocks.getAccountHistory).toHaveBeenCalledTimes(settledHistoryCallCount);
  });

  it('shows pending history without blocking account rows', async () => {
    mocks.files = [makeCodexFile('pending.json', 'auth-1', 'pending@example.com')];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getAccountHistory.mockResolvedValue(
      makeAccountHistoryResponse([
        {
          row_key: 'pending.json\u0000auth-1',
          account_key: 'pending@example.com',
          matched: true,
          total_requests: 5,
          success_calls: 4,
          failure_calls: 1,
          total_tokens: 600,
          total_cost: 0.08,
          success_rate: 0.8,
          first_seen_ms: 1,
          last_seen_ms: 2,
          sync_status: 'pending',
        },
      ])
    );

    const renderer = await renderAccountsPage();
    await flushPromises();

    expect(getAccountListItemTexts(renderer).join('\n')).toContain('pending.json');
    expect(treeText(renderer)).toContain('accounts.history_syncing');
  });

  it('keeps the account list usable when account history is unavailable', async () => {
    mocks.files = [makeCodexFile('offline.json', 'auth-1', 'offline@example.com')];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getAccountHistory.mockRejectedValue(new Error('history offline'));

    const renderer = await renderAccountsPage();
    await flushPromises();

    expect(getAccountListItemTexts(renderer).join('\n')).toContain('offline.json');
    expect(treeText(renderer)).toContain('accounts.history_unavailable');
  });

  it('renders the mobile filters entrypoint in the accounts toolbar', async () => {
    const renderer = await renderAccountsPage();

    expect(treeText(renderer)).toContain('accounts.mobile_filters_button');
    expect(treeText(renderer)).toContain('accounts.col_recent');

    await act(async () => {
      findButtonByText(renderer, 'accounts.mobile_filters_button').props.onClick();
    });
  });

  it('searches diagnostic-only Codex usage header snapshots without rendering them in quota', async () => {
    mocks.files = [
      makeCodexFile('codex-diagnostic.json', 'auth-1', 'diagnostic@example.com'),
      makeCodexFile('codex-other.json', 'auth-2', 'other@example.com'),
    ];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: 1,
      from_ms: 0,
      to_ms: 1,
      items: [
        {
          event_hash: 'diagnostic-only',
          timestamp_ms: 1700000000000,
          auth_file_snapshot: 'codex-diagnostic.json',
          auth_index: 'auth-1',
          account_snapshot: 'diagnostic@example.com',
          auth_provider_snapshot: 'codex',
          header_trace_id: 'trace-diagnostic-only',
          header_error_kind: 'rate_limit',
          header_error_code: 'usage_limit_reached',
        },
      ],
    });

    const renderer = await renderAccountsPage();
    await flushPromises();
    expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(1);

    await act(async () => {
      findDetailButtonByName(renderer, 'codex-diagnostic.json').props.onClick();
    });
    await flushPromises();

    expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(1);
    expect(treeText(renderer)).toContain('accounts.quota_source_observed_header');

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    await flushPromises();

    expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(1);

    await act(async () => {
      findInputByAriaLabel(renderer, 'accounts.search_label').props.onChange({
        target: { value: 'trace-diagnostic-only' },
      });
    });

    const rowTexts = getAccountListItemTexts(renderer);
    expect(rowTexts).toHaveLength(1);
    expect(rowTexts[0]).toContain('codex-diagnostic.json');

    expect(treeText(renderer)).not.toContain('accounts.quota_source_observed_header');
    expect(treeText(renderer)).not.toContain('trace-diagnostic-only');
    expect(treeText(renderer)).not.toContain('usage_limit_reached');
  });

  it('loads quota history without automatically refreshing provider quota', async () => {
    mocks.files = [
      makeCodexFile('codex-a.json', 'auth-a', 'first@example.com'),
      makeCodexFile('codex-b.json', 'auth-b', 'second@example.com'),
    ];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    const resetLabel = new Date(Date.now() + 60 * 60 * 1000).toISOString();
    const resetAtMs = Date.parse(resetLabel);
    mocks.quotaState.codexQuota = {
      ...buildCredentialScopedQuotaRecord(mocks.files[0], {
        status: 'success',
        windows: [
          {
            id: 'five-hour',
            label: 'Five hours',
            usedPercent: 20,
            resetLabel,
            resetAtMs,
            resetAccuracy: 'exact',
            limitWindowSeconds: 5 * 60 * 60,
          },
        ],
      }),
      ...buildCredentialScopedQuotaRecord(mocks.files[1], {
        status: 'success',
        windows: [
          {
            id: 'five-hour',
            label: 'Five hours',
            usedPercent: 30,
            resetLabel,
            resetAtMs,
            resetAccuracy: 'exact',
            limitWindowSeconds: 5 * 60 * 60,
          },
        ],
      }),
    };
    mocks.getActiveQuotaCooldowns.mockResolvedValue([
      {
        authFileName: 'codex-a.json',
        authIndex: 'auth-a',
        recoverAtMs: Date.now() + 2 * 60 * 60 * 1000,
        disabledAtMs: Date.now() - 5 * 60 * 1000,
      },
    ]);

    const renderer = await renderAccountsPage();
    await flushPromises();

    expect(mocks.getActiveQuotaCooldowns).not.toHaveBeenCalled();
    expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(1);
    expect(mocks.listAccountActionCandidates).not.toHaveBeenCalled();
    expect(mocks.getAccountWindowUsage).not.toHaveBeenCalled();

    await act(async () => {
      findDetailButtonByName(renderer, 'codex-a.json').props.onClick();
    });
    await flushPromises();

    expect(mocks.getActiveQuotaCooldowns).toHaveBeenCalledTimes(1);
    expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(1);
    expect(mocks.listAccountActionCandidates).toHaveBeenCalledTimes(1);
    expect(mocks.getAccountWindowUsage).not.toHaveBeenCalled();
    expect(treeText(renderer)).toContain('accounts.detail_overview_basis_cooldown');

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    await flushPromises();

    expect(mocks.getActiveQuotaCooldowns).toHaveBeenCalledTimes(1);
    expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(1);
    expect(mocks.listAccountActionCandidates).toHaveBeenCalledTimes(1);
    expect(mocks.getAccountWindowUsage).toHaveBeenCalledTimes(1);
    expect(mocks.quotaState.setCodexQuota).not.toHaveBeenCalled();
    expect(treeText(renderer)).toContain('accounts.detail_total_requests');
    expect(treeText(renderer)).toContain('accounts.detail_total_tokens');
    expect(treeText(renderer)).toContain('accounts.detail_total_cost');
    expect(
      renderer.root.findAllByProps({ 'data-account-quota-summary-strip': 'true' })
    ).toHaveLength(0);
    expect(
      renderer.root.findAllByProps({ 'data-account-quota-usage-summary': 'true' })
    ).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-account-quota-metrics': 'true' })).toHaveLength(1);
    const quotaMetrics = renderer.root.findByProps({ 'data-account-quota-metrics': 'true' });
    const quotaMetricIcons = quotaMetrics.findAll(
      (node) =>
        typeof node.props.className === 'string' && node.props.className.includes('metricIcon')
    );
    expect(quotaMetricIcons).toHaveLength(4);
    expect(quotaMetricIcons.map((node) => node.props.className)).toEqual([
      expect.stringContaining('metricIconBlue'),
      expect.stringContaining('metricIconTeal'),
      expect.stringContaining('metricIconAmber'),
      expect.stringContaining('metricIconGreen'),
    ]);
    expect(renderer.root.findAllByProps({ 'data-quota-window-group': 'standard' })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-quota-card-mode': 'standard' })).toHaveLength(1);
    expect(treeText(renderer)).toContain('accounts.detail_quota_standard_title');
    expect(renderer.root.findAllByProps({ 'data-account-quota-evidence': 'true' })).toHaveLength(0);
    expect(renderer.root.findAllByProps({ 'data-quota-evidence-panel': 'reset' })).toHaveLength(0);
    const windowUsageRequest = mocks.getAccountWindowUsage.mock.calls[0]?.[2] as
      | AccountWindowUsageRequestForTest
      | undefined;
    expect(windowUsageRequest?.windows).toHaveLength(2);
    const windowUsageTargets = windowUsageRequest?.windows as Array<Record<string, unknown>>;
    expect(windowUsageTargets[0]).toMatchObject({
      row_key: 'codex-a.json\u0000auth-a',
      source: 'codex-a.json',
      auth_index: 'auth-a',
      provider_window_id: 'five-hour',
      period: 'current',
    });
    expect(windowUsageTargets[1]).toMatchObject({
      provider_window_id: 'five-hour',
      period: 'previous',
    });
    const historyRequestCount = mocks.getAccountHistory.mock.calls.length;

    await act(async () => {
      await renderer.root.findByType(AccountQuotaTab).props.onRefreshHistory();
    });
    await flushPromises();

    expect(mocks.getAccountHistory).toHaveBeenCalledTimes(historyRequestCount + 1);
    expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(2);
    expect(mocks.getAccountWindowUsage).toHaveBeenCalledTimes(2);
    expect(mocks.quotaState.setCodexQuota).not.toHaveBeenCalled();

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_overview').props.onClick();
    });
    await flushPromises();
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    await flushPromises();

    expect(mocks.getAccountWindowUsage).toHaveBeenCalledTimes(2);
    expect(mocks.quotaState.setCodexQuota).not.toHaveBeenCalled();
  });

  it('does not show an animated icon during automatic quota window loading', async () => {
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    const windowUsage = createDeferred<AccountWindowUsageResponseForTest>();
    mocks.getAccountWindowUsage.mockReturnValue(windowUsage.promise);

    const renderer = await renderAccountsPage();
    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await flushPromises();
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    await flushPromises();

    const quotaTab = renderer.root.findByType(AccountQuotaTab);
    expect(findLoadingSpinners(quotaTab)).toHaveLength(0);
    expect(quotaTab.props.historyRefreshing).toBe(false);

    await act(async () => {
      windowUsage.resolve({ generated_at_ms: 1, items: [] });
      await windowUsage.promise;
    });
    await flushPromises();
    expect(findLoadingSpinners(renderer.root.findByType(AccountQuotaTab))).toHaveLength(0);
  });

  it('does not refresh provider quota when opening a quota-tab deep link', async () => {
    const file = makeCodexFile('codex-deep-link.json', 'auth-deep-link', 'deep-link@example.com');
    mocks.files = [file];
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex-deep-link.json%00auth-deep-link&tab=quota',
    };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    const resetAtMs = Date.now() + 60 * 60 * 1000;
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      quotaInventoryObserved: true,
      fetchedAtMs: Date.now(),
      windows: [
        {
          id: 'five-hour',
          label: 'Five hours',
          usedPercent: 10,
          resetAtMs,
          resetLabel: new Date(resetAtMs).toISOString(),
          resetAccuracy: 'exact',
          limitWindowSeconds: 5 * 60 * 60,
        },
      ],
    });
    const quotaFetch = vi.spyOn(CODEX_CONFIG, 'fetchQuota').mockResolvedValue(makeCodexQuotaData());
    mocks.getHeaderSnapshots
      .mockResolvedValueOnce({ generated_at_ms: 100, from_ms: 0, to_ms: 100, items: [] })
      .mockResolvedValueOnce({ generated_at_ms: 200, from_ms: 0, to_ms: 200, items: [] });

    const renderer = await renderAccountsPage();
    await flushPromises();

    expect(renderer.root.findByType(AccountQuotaTab)).toBeTruthy();
    expect(quotaFetch).not.toHaveBeenCalled();
    expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(1);
    expect(mocks.getAccountWindowUsage).toHaveBeenCalledTimes(1);

    await act(async () => {
      await findButtonByText(renderer, 'common.refresh').props.onClick();
    });
    await flushPromises();

    expect(mocks.loadFiles).toHaveBeenCalledTimes(2);
    expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(2);
    expect(mocks.getAccountWindowUsage).toHaveBeenCalledTimes(1);
    expect(quotaFetch).not.toHaveBeenCalled();
  });

  it('loads quota lifecycle after a deep-linked credential resolves asynchronously', async () => {
    const target = {
      name: 'xai-delayed.json',
      type: 'xai',
      provider: 'xai',
      authIndex: 'xai-delayed-01',
      account: 'delayed@example.com',
      disabled: true,
    } as AuthFileItem;
    const rowKey = getAuthFileSelectionKey(target);
    mocks.files = [
      makeCodexFile('placeholder.json', 'placeholder-auth', 'placeholder@example.com'),
    ];
    mocks.authFilesLoading = true;
    mocks.location = {
      pathname: '/accounts',
      search: `?account=${encodeURIComponent(rowKey)}&tab=quota`,
    };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    vi.mocked(accountQuotaSnapshotApi.query).mockResolvedValue({
      generated_at_ms: 1,
      items: [
        {
          row_key: rowKey,
          account_key: rowKey,
          provider: 'xai',
          windows: [
            {
              provider_window_id: 'included-free-rolling-24h',
              window_kind: 'rolling_24h',
              window_mode: 'rolling',
              model_scope_kind: 'models',
              model_scope_key: 'grok-4.5-build-free',
              model_ids: ['grok-4.5-build-free'],
              source: 'response_body',
              observed_at_ms: 1,
              boundary_accuracy: 'estimated',
              duration_seconds: 24 * 60 * 60,
              used_percent: 100,
              remaining_percent: 0,
              stale: false,
            },
          ],
        },
      ],
    });

    const renderer = await renderAccountsPage();
    await flushPromises();

    expect(accountQuotaSnapshotApi.query).not.toHaveBeenCalled();
    expect(mocks.getAccountWindowUsage).not.toHaveBeenCalled();

    await act(async () => {
      mocks.files = [target];
      mocks.authFilesLoading = false;
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await flushPromises();
    await flushPromises();

    expect(renderer.root.findByType(AccountQuotaTab)).toBeTruthy();
    expect(accountQuotaSnapshotApi.query).toHaveBeenCalledTimes(1);
    expect(mocks.getAccountWindowUsage).toHaveBeenCalledTimes(1);
  });

  it('reloads lifecycle and window usage after detail quota refresh without loading history', async () => {
    const file = makeCodexFile('codex-refresh.json', 'auth-refresh', 'refresh@example.com');
    mocks.files = [file];
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex-refresh.json%00auth-refresh&tab=quota',
    };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    const resetAtMs = Date.now() + 5 * 60 * 60 * 1000;
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      quotaInventoryObserved: true,
      fetchedAtMs: Date.now(),
      windows: [
        {
          id: 'five-hour',
          label: 'Five hours',
          usedPercent: 10,
          resetAtMs,
          resetLabel: new Date(resetAtMs).toISOString(),
          resetAccuracy: 'exact',
          limitWindowSeconds: 5 * 60 * 60,
        },
      ],
    });
    const quotaFetch = vi.spyOn(CODEX_CONFIG, 'fetchQuota').mockResolvedValue(makeCodexQuotaData());

    const renderer = await renderAccountsPage();
    await flushPromises();
    await flushPromises();
    expect(accountQuotaSnapshotApi.query).toHaveBeenCalledTimes(1);
    expect(mocks.getAccountWindowUsage).toHaveBeenCalledTimes(1);
    mocks.getAccountHistory.mockClear();

    await act(async () => {
      const detailRefreshButton = renderer.root
        .findByType(Drawer)
        .findAllByType(Button)
        .find((node) => readText(node.props.children).includes('accounts.refresh_quota'));
      if (!detailRefreshButton) throw new Error('Detail quota refresh button not found');
      detailRefreshButton.props.onClick();
      await Promise.resolve();
    });
    await flushPromises();
    await flushPromises();

    expect(quotaFetch).toHaveBeenCalledTimes(1);
    expect(accountQuotaSnapshotApi.query).toHaveBeenCalledTimes(2);
    expect(mocks.getAccountWindowUsage).toHaveBeenCalledTimes(2);
    expect(mocks.getAccountHistory).not.toHaveBeenCalled();
  });

  it('loads history for a deep-linked credential outside the visible page', async () => {
    mocks.files = Array.from({ length: 11 }, (_, index) =>
      makeCodexFile(
        `codex-history-${String(index + 1).padStart(2, '0')}.json`,
        `auth-history-${index + 1}`,
        `history-${index + 1}@example.com`
      )
    );
    const target = mocks.files[10];
    const targetKey = getAuthFileSelectionKey(target);
    mocks.location = {
      pathname: '/accounts',
      search: `?account=${encodeURIComponent(targetKey)}&tab=quota`,
    };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };

    const renderer = await renderAccountsPage();
    await flushPromises();

    const visibleKeys = renderer.root
      .findAll(
        (node) => node.type === 'article' && typeof node.props['data-account-card'] === 'string'
      )
      .map((node) => String(node.props['data-account-card']));
    expect(visibleKeys).toHaveLength(10);
    expect(visibleKeys).not.toContain(targetKey);
    const request = mocks.getAccountHistory.mock.calls[0]?.[2] as AccountHistoryRequestForTest;
    expect(request.accounts).toHaveLength(11);
    expect(request.accounts).toContainEqual(expect.objectContaining({ row_key: targetKey }));
  });

  it('keeps rolling window usage visible across refreshes and detail drawer reopen', async () => {
    let currentTimeMs = 2_000_000_000_000;
    vi.spyOn(Date, 'now').mockImplementation(() => {
      currentTimeMs += 1_000;
      return currentTimeMs;
    });
    const xaiFile = {
      name: 'xai-ops.json',
      type: 'xai',
      provider: 'xai',
      authIndex: 'xai-ops-01',
      account: 'xai@example.com',
      disabled: true,
    } as AuthFileItem;
    const rowKey = 'xai-ops.json\u0000xai-ops-01';
    mocks.files = [xaiFile];
    mocks.location = {
      pathname: '/accounts',
      search: '?account=xai-ops.json%00xai-ops-01&tab=quota',
    };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    vi.mocked(accountQuotaSnapshotApi.query).mockResolvedValue({
      generated_at_ms: 1,
      items: [
        {
          row_key: rowKey,
          account_key: rowKey,
          provider: 'xai',
          windows: [
            {
              provider_window_id: 'included-free-rolling-24h',
              window_kind: 'rolling_24h',
              window_mode: 'rolling',
              model_scope_kind: 'models',
              model_scope_key: 'grok-4.5-build-free',
              model_ids: ['grok-4.5-build-free'],
              source: 'response_body',
              observed_at_ms: 1,
              boundary_accuracy: 'estimated',
              duration_seconds: 24 * 60 * 60,
              used_percent: 100,
              remaining_percent: 0,
              stale: false,
            },
          ],
        },
      ],
    });
    let usageRequestCount = 0;
    mocks.getAccountWindowUsage.mockImplementation(async (_base, _managementKey, request) => {
      usageRequestCount += 1;
      const totalRequests = usageRequestCount === 1 ? 4 : usageRequestCount === 2 ? 5 : 6;
      const totalTokens =
        usageRequestCount === 1 ? 9_939 : usageRequestCount === 2 ? 12_460 : 14_981;
      const windows = request.windows as Array<{
        request_key: string;
        row_key: string;
        window_key: string;
        provider_window_id: string;
        period: 'current' | 'previous' | 'previous_equal_range';
        from_ms: number;
        to_ms: number;
      }>;
      return {
        generated_at_ms: Date.now(),
        items: windows.map((window) => ({
          ...window,
          matched: true,
          total_requests: totalRequests,
          success_calls: totalRequests,
          failure_calls: 0,
          total_tokens: totalTokens,
          total_cost: 0.12,
          success_rate: 1,
          last_seen_ms: window.to_ms - 1,
          sync_status: 'ready',
        })),
      };
    });

    const renderer = await renderAccountsPage();
    await flushPromises();
    await flushPromises();

    expect(renderer.root.findByProps({ 'data-account-quota-usage-summary': 'true' })).toBeTruthy();
    expect(accountQuotaSnapshotApi.query).toHaveBeenCalledTimes(1);
    expect(mocks.getAccountWindowUsage).toHaveBeenCalledTimes(1);
    const lastWindowUsageRequest = mocks.getAccountWindowUsage.mock.calls[
      mocks.getAccountWindowUsage.mock.calls.length - 1
    ]?.[2] as AccountWindowUsageRequestForTest | undefined;
    expect(lastWindowUsageRequest?.windows).toHaveLength(2);
    const firstCurrentTarget = (
      lastWindowUsageRequest?.windows as Array<{
        period: string;
        from_ms: number;
        to_ms: number;
      }>
    ).find((window) => window.period === 'current');
    expect(firstCurrentTarget).toBeDefined();
    expect(
      renderer.root.findByType(AccountQuotaTab).props.detailView.quota.windows[0].currentUsage
    ).toMatchObject({
      fromMs: firstCurrentTarget?.from_ms,
      toMs: firstCurrentTarget?.to_ms,
      totalRequests: 4,
      totalTokens: 9_939,
    });

    await act(async () => {
      renderer.root.findByType(AccountQuotaTab).props.onRefreshHistory();
      await Promise.resolve();
    });
    await flushPromises();
    await flushPromises();

    expect(mocks.getAccountWindowUsage).toHaveBeenCalledTimes(2);
    const refreshedWindowUsageRequest = mocks.getAccountWindowUsage.mock.calls[1]?.[2] as
      | AccountWindowUsageRequestForTest
      | undefined;
    const refreshedCurrentTarget = (
      refreshedWindowUsageRequest?.windows as Array<{
        period: string;
        from_ms: number;
        to_ms: number;
      }>
    ).find((window) => window.period === 'current');
    expect(refreshedCurrentTarget?.from_ms).toBeGreaterThan(firstCurrentTarget?.from_ms ?? 0);
    expect(refreshedCurrentTarget?.to_ms).toBeGreaterThan(firstCurrentTarget?.to_ms ?? 0);
    expect(
      renderer.root.findByType(AccountQuotaTab).props.detailView.quota.windows[0].currentUsage
    ).toMatchObject({
      fromMs: refreshedCurrentTarget?.from_ms,
      toMs: refreshedCurrentTarget?.to_ms,
      totalRequests: 5,
      totalTokens: 12_460,
    });

    await act(async () => {
      renderer.root.findByType(Drawer).props.onClose();
    });
    await flushPromises();

    expect(renderer.root.findAllByType(AccountQuotaTab)).toHaveLength(0);

    await act(async () => {
      findDetailButtonByName(renderer, 'xai-ops.json').props.onClick();
    });
    await flushPromises();
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    await flushPromises();
    await flushPromises();

    expect(mocks.getAccountWindowUsage).toHaveBeenCalledTimes(3);
    const reopenedWindowUsageRequest = mocks.getAccountWindowUsage.mock.calls[2]?.[2] as
      | AccountWindowUsageRequestForTest
      | undefined;
    const reopenedCurrentTarget = (
      reopenedWindowUsageRequest?.windows as Array<{
        period: string;
        from_ms: number;
        to_ms: number;
      }>
    ).find((window) => window.period === 'current');
    expect(reopenedCurrentTarget?.from_ms).toBeGreaterThan(refreshedCurrentTarget?.from_ms ?? 0);
    expect(reopenedCurrentTarget?.to_ms).toBeGreaterThan(refreshedCurrentTarget?.to_ms ?? 0);
    expect(
      renderer.root.findByType(AccountQuotaTab).props.detailView.quota.windows[0].currentUsage
    ).toMatchObject({
      fromMs: reopenedCurrentTarget?.from_ms,
      toMs: reopenedCurrentTarget?.to_ms,
      totalRequests: 6,
      totalTokens: 14_981,
    });
  });

  it('persists a successful empty provider inventory as a complete observation', async () => {
    const file = {
      ...makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
      disabled: true,
    } as AuthFileItem;
    mocks.files = [file];
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex.json%00auth-1&tab=quota',
    };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      windows: [],
      quotaInventoryObserved: true,
      fetchedAtMs: 123_456,
    });

    await renderAccountsPage();
    await flushPromises();
    await flushPromises();

    expect(accountQuotaSnapshotApi.write).toHaveBeenCalled();
    const writeCalls = vi.mocked(accountQuotaSnapshotApi.write).mock.calls;
    const entries = writeCalls[writeCalls.length - 1]?.[2];
    expect(entries).toEqual([
      expect.objectContaining({
        windows: [],
        observation: {
          source: 'api_query',
          source_observation_id: 'accounts-provider-query:123456',
          observed_at_ms: 123_456,
          inventory_scope_key: 'codex:rate-limits',
          inventory_mode: 'complete',
        },
      }),
    ]);
    const queryCalls = vi.mocked(accountQuotaSnapshotApi.query).mock.calls;
    expect(queryCalls[queryCalls.length - 1]?.[3]).toEqual({
      includeInactive: true,
    });
  });

  it('does not persist an unrecognized Codex success payload as an empty complete inventory', async () => {
    const file = {
      ...makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
      disabled: true,
    } as AuthFileItem;
    mocks.files = [file];
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex.json%00auth-1&tab=quota',
    };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      windows: [],
      quotaInventoryObserved: false,
      fetchedAtMs: 123_456,
    });

    await renderAccountsPage();
    await flushPromises();
    await flushPromises();

    expect(accountQuotaSnapshotApi.write).not.toHaveBeenCalled();
    expect(accountQuotaSnapshotApi.query).toHaveBeenCalled();
  });

  it.each(['claude', 'antigravity', 'kimi'] as const)(
    'does not persist an unrecognized %s success payload as a complete inventory',
    async (provider) => {
      const file = {
        name: `${provider}.json`,
        type: provider,
        provider,
        authIndex: `${provider}-1`,
        account: `${provider}@example.com`,
        disabled: true,
      } as AuthFileItem;
      mocks.files = [file];
      mocks.location = {
        pathname: '/accounts',
        search: `?account=${encodeURIComponent(`${file.name}\u0000${file.authIndex}`)}&tab=quota`,
      };
      mocks.panelFeatureAvailability = {
        checking: false,
        managerServiceBase: 'http://manager.local:18317',
        requestMonitoringAvailable: true,
        serverCodexInspectionAvailable: false,
      };
      const commonState = {
        status: 'success' as const,
        quotaInventoryObserved: false,
        fetchedAtMs: 123_456,
      };
      if (provider === 'claude') {
        mocks.quotaState.claudeQuota = buildCredentialScopedQuotaRecord(file, {
          ...commonState,
          windows: [],
        });
      } else if (provider === 'antigravity') {
        mocks.quotaState.antigravityQuota = buildCredentialScopedQuotaRecord(file, {
          ...commonState,
          groups: [],
          subscription: null,
          serverTimeOffsetMs: null,
        });
      } else {
        mocks.quotaState.kimiQuota = buildCredentialScopedQuotaRecord(file, {
          ...commonState,
          rows: [],
        });
      }

      await renderAccountsPage();
      await flushPromises();
      await flushPromises();

      expect(accountQuotaSnapshotApi.write).not.toHaveBeenCalled();
      expect(accountQuotaSnapshotApi.query).toHaveBeenCalled();
    }
  );

  it.each(['codex', 'claude', 'antigravity', 'kimi'] as const)(
    'persists legacy cached %s windows with no inventory marker as partial evidence',
    async (provider) => {
      const fetchedAtMs = Date.now();
      const resetAtMs = fetchedAtMs + 7 * 24 * 60 * 60 * 1000;
      const file = {
        name: `${provider}.json`,
        type: provider,
        provider,
        authIndex: `${provider}-1`,
        account: `${provider}@example.com`,
        disabled: true,
      } as AuthFileItem;
      mocks.files = [file];
      mocks.location = {
        pathname: '/accounts',
        search: `?account=${encodeURIComponent(`${file.name}\u0000${file.authIndex}`)}&tab=quota`,
      };
      mocks.panelFeatureAvailability = {
        checking: false,
        managerServiceBase: 'http://manager.local:18317',
        requestMonitoringAvailable: true,
        serverCodexInspectionAvailable: false,
      };
      const commonState = {
        status: 'success' as const,
        fetchedAtMs,
      };
      if (provider === 'codex') {
        mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
          ...commonState,
          windows: [
            {
              id: 'weekly',
              label: 'Weekly',
              usedPercent: 20,
              resetLabel: new Date(resetAtMs).toISOString(),
              resetAtMs,
              resetAccuracy: 'exact',
              limitWindowSeconds: 7 * 24 * 60 * 60,
            },
          ],
        });
      } else if (provider === 'claude') {
        mocks.quotaState.claudeQuota = buildCredentialScopedQuotaRecord(file, {
          ...commonState,
          windows: [
            {
              id: 'seven-day',
              label: 'Seven days',
              usedPercent: 20,
              resetLabel: new Date(resetAtMs).toISOString(),
              resetAtMs,
              resetAccuracy: 'exact',
              limitWindowSeconds: 7 * 24 * 60 * 60,
            },
          ],
        });
      } else if (provider === 'antigravity') {
        mocks.quotaState.antigravityQuota = buildCredentialScopedQuotaRecord(file, {
          ...commonState,
          groups: [
            {
              id: 'gemini',
              label: 'Gemini models',
              models: ['gemini-2.5-pro'],
              buckets: [
                {
                  id: 'weekly',
                  label: 'Weekly limit',
                  window: '7d',
                  remainingFraction: 0.8,
                  resetTime: new Date(resetAtMs).toISOString(),
                },
              ],
            },
          ],
          subscription: null,
          serverTimeOffsetMs: null,
        });
      } else {
        mocks.quotaState.kimiQuota = buildCredentialScopedQuotaRecord(file, {
          ...commonState,
          rows: [
            {
              id: 'weekly',
              label: 'Weekly',
              used: 20,
              limit: 100,
              resetHint: new Date(resetAtMs).toISOString(),
              resetAtMs,
              resetAccuracy: 'exact',
              limitWindowSeconds: 7 * 24 * 60 * 60,
            },
          ],
        });
      }

      await renderAccountsPage();
      await flushPromises();
      await flushPromises();

      const writtenEntries: AccountQuotaSnapshotWriteEntry[] = vi
        .mocked(accountQuotaSnapshotApi.write)
        .mock.calls.flatMap((call) => call[2] ?? []);
      expect(writtenEntries).toContainEqual(
        expect.objectContaining({
          provider,
          observation: expect.objectContaining({
            source: 'api_query',
            observed_at_ms: fetchedAtMs,
            inventory_mode: 'partial',
          }),
        })
      );
    }
  );

  it('treats legacy xAI billing without a partial marker as partial evidence', async () => {
    const fetchedAtMs = Date.now();
    const resetAtMs = fetchedAtMs + 30 * 24 * 60 * 60 * 1000;
    const file = {
      name: 'xai.json',
      type: 'xai',
      provider: 'xai',
      authIndex: 'xai-1',
      account: 'xai@example.com',
      disabled: true,
    } as AuthFileItem;
    mocks.files = [file];
    mocks.location = {
      pathname: '/accounts',
      search: '?account=xai.json%00xai-1&tab=quota',
    };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.quotaState.xaiQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      fetchedAtMs,
      billing: {
        periodType: 'monthly',
        usagePercent: null,
        productUsage: [],
        monthlyLimitCents: 10_000,
        usedCents: 2_500,
        includedUsedCents: 2_500,
        onDemandCapCents: null,
        onDemandUsedCents: null,
        onDemandUsedPercent: null,
        usedPercent: 25,
        billingPeriodStart: new Date(fetchedAtMs).toISOString(),
        billingPeriodEnd: new Date(resetAtMs).toISOString(),
      },
    });

    await renderAccountsPage();
    await flushPromises();
    await flushPromises();

    const writtenEntries: AccountQuotaSnapshotWriteEntry[] = vi
      .mocked(accountQuotaSnapshotApi.write)
      .mock.calls.flatMap((call) => call[2] ?? []);
    expect(writtenEntries).toContainEqual(
      expect.objectContaining({
        provider: 'xai',
        observation: expect.objectContaining({
          source: 'api_query',
          observed_at_ms: fetchedAtMs,
          inventory_scope_key: 'xai:quota-windows',
          inventory_mode: 'partial',
        }),
      })
    );
  });

  it('does not persist legacy xAI success state without billing inventory', async () => {
    const file = {
      name: 'xai.json',
      type: 'xai',
      provider: 'xai',
      authIndex: 'xai-1',
      account: 'xai@example.com',
      disabled: true,
    } as AuthFileItem;
    mocks.files = [file];
    mocks.location = {
      pathname: '/accounts',
      search: '?account=xai.json%00xai-1&tab=quota',
    };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.quotaState.xaiQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      fetchedAtMs: Date.now(),
      billing: null,
    });

    await renderAccountsPage();
    await flushPromises();
    await flushPromises();

    expect(accountQuotaSnapshotApi.write).not.toHaveBeenCalled();
    expect(accountQuotaSnapshotApi.query).toHaveBeenCalled();
  });

  it('persists partial Codex API and Header windows as separate observations', async () => {
    const fetchedAtMs = Date.now();
    const headerTimestampMs = fetchedAtMs - 1_000;
    const resetAtMs = fetchedAtMs + 7 * 24 * 60 * 60 * 1000;
    const file = {
      ...makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
      disabled: true,
    } as AuthFileItem;
    mocks.files = [file];
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex.json%00auth-1&tab=quota',
    };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      quotaInventoryObserved: false,
      fetchedAtMs,
      windows: [
        {
          id: 'weekly',
          label: 'Weekly',
          usedPercent: 20,
          resetLabel: new Date(resetAtMs).toISOString(),
          resetAtMs,
          resetAccuracy: 'exact',
          limitWindowSeconds: 7 * 24 * 60 * 60,
        },
      ],
    });
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: headerTimestampMs,
      from_ms: headerTimestampMs - 1_000,
      to_ms: headerTimestampMs,
      items: [
        {
          event_hash: 'newer-header-event',
          timestamp_ms: headerTimestampMs,
          auth_file_snapshot: 'codex.json',
          auth_index: 'auth-1',
          account_snapshot: 'codex@example.com',
          auth_provider_snapshot: 'codex',
          header_quota_used_percent: 80,
          header_quota_recover_at_ms: headerTimestampMs + 5 * 60 * 60 * 1000,
        },
      ],
    });

    await renderAccountsPage();
    await flushPromises();
    await flushPromises();

    const writeCalls = vi.mocked(accountQuotaSnapshotApi.write).mock.calls;
    const writtenEntries: AccountQuotaSnapshotWriteEntry[] =
      writeCalls[writeCalls.length - 1]?.[2] ?? [];
    const codexEntries = writtenEntries.filter((item) => item.provider === 'codex');
    expect(codexEntries).toHaveLength(2);
    const apiEntry = codexEntries.find((item) => item.observation?.source === 'api_query');
    expect(apiEntry).toMatchObject({
      observation: {
        source: 'api_query',
        source_observation_id: `accounts-provider-query:${fetchedAtMs}`,
        observed_at_ms: fetchedAtMs,
        inventory_scope_key: 'codex:rate-limits',
        inventory_mode: 'partial',
      },
      windows: [
        expect.objectContaining({
          provider_window_id: 'weekly',
          source: 'api_query',
          observed_at_ms: fetchedAtMs,
        }),
      ],
    });
    expect(apiEntry?.windows.some((window) => window.source === 'response_header')).toBe(false);

    const headerEntry = codexEntries.find((item) => item.observation?.source === 'response_header');
    expect(headerEntry).toMatchObject({
      observation: {
        source: 'response_header',
        source_observation_id: 'newer-header-event',
        observed_at_ms: headerTimestampMs,
        inventory_scope_key: 'codex:rate-limits',
        inventory_mode: 'partial',
      },
      windows: [
        expect.objectContaining({
          provider_window_id: 'usage-header-observed',
          source: 'response_header',
          observed_at_ms: headerTimestampMs,
        }),
      ],
    });
  });

  it('does not persist an older Header inventory behind a newer complete Codex API observation', async () => {
    const fetchedAtMs = Date.now();
    const headerTimestampMs = fetchedAtMs - 1_000;
    const file = {
      ...makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
      disabled: true,
    } as AuthFileItem;
    mocks.files = [file];
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex.json%00auth-1&tab=quota',
    };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      quotaInventoryObserved: true,
      fetchedAtMs,
      windows: [
        {
          id: 'weekly',
          label: 'Weekly',
          usedPercent: 20,
          resetLabel: new Date(fetchedAtMs + 7 * 24 * 60 * 60 * 1000).toISOString(),
          resetAtMs: fetchedAtMs + 7 * 24 * 60 * 60 * 1000,
          resetAccuracy: 'exact',
          limitWindowSeconds: 7 * 24 * 60 * 60,
        },
      ],
    });
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: fetchedAtMs,
      from_ms: headerTimestampMs - 1_000,
      to_ms: fetchedAtMs,
      items: [
        {
          event_hash: 'older-header-event',
          timestamp_ms: headerTimestampMs,
          auth_file_snapshot: 'codex.json',
          auth_index: 'auth-1',
          account_snapshot: 'codex@example.com',
          auth_provider_snapshot: 'codex',
          header_quota_used_percent: 80,
          header_quota_recover_at_ms: headerTimestampMs + 5 * 60 * 60 * 1000,
        },
      ],
    });

    await renderAccountsPage();
    await flushPromises();
    await flushPromises();

    const writeCalls = vi.mocked(accountQuotaSnapshotApi.write).mock.calls;
    const entries = writeCalls[writeCalls.length - 1]?.[2] ?? [];
    const codexEntries = entries.filter((item) => item.provider === 'codex');
    expect(codexEntries).toHaveLength(1);
    expect(codexEntries[0]?.observation?.source).toBe('api_query');
    expect(codexEntries.some((item) => item.observation?.source === 'response_header')).toBe(false);
  });

  it('persists partial xAI billing evidence without treating omitted periods as removed', async () => {
    const file = {
      name: 'xai.json',
      type: 'xai',
      provider: 'xai',
      authIndex: 'xai-1',
      account: 'xai@example.com',
      disabled: true,
    } as AuthFileItem;
    mocks.files = [file];
    mocks.location = {
      pathname: '/accounts',
      search: '?account=xai.json%00xai-1&tab=quota',
    };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.quotaState.xaiQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      fetchedAtMs: 123_456,
      billing: {
        periodType: 'monthly',
        usagePercent: null,
        productUsage: [],
        monthlyLimitCents: 10_000,
        usedCents: 2_500,
        includedUsedCents: 2_500,
        onDemandCapCents: null,
        onDemandUsedCents: null,
        onDemandUsedPercent: null,
        usedPercent: 25,
        billingPeriodStart: '2026-08-01T00:00:00Z',
        billingPeriodEnd: '2026-09-01T00:00:00Z',
        partial: true,
      },
    });

    await renderAccountsPage();
    await flushPromises();
    await flushPromises();

    const writtenEntries: AccountQuotaSnapshotWriteEntry[] = vi
      .mocked(accountQuotaSnapshotApi.write)
      .mock.calls.flatMap((call) => call[2] ?? []);
    expect(writtenEntries).toContainEqual(
      expect.objectContaining({
        provider: 'xai',
        observation: expect.objectContaining({
          source: 'api_query',
          observed_at_ms: 123_456,
          inventory_scope_key: 'xai:quota-windows',
          inventory_mode: 'partial',
        }),
      })
    );
  });

  it('persists a Codex usage-header fallback as a partial rate-limit observation', async () => {
    const timestampMs = Date.now();
    const file = {
      ...makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
      disabled: true,
    } as AuthFileItem;
    mocks.files = [file];
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex.json%00auth-1&tab=quota',
    };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      windows: [],
      quotaInventoryObserved: false,
      fetchedAtMs: timestampMs + 1_000,
    });
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: timestampMs,
      from_ms: timestampMs - 1_000,
      to_ms: timestampMs,
      items: [
        {
          event_hash: 'header-fallback-event',
          timestamp_ms: timestampMs,
          auth_file_snapshot: 'codex.json',
          auth_index: 'auth-1',
          account_snapshot: 'codex@example.com',
          auth_provider_snapshot: 'codex',
          header_quota_used_percent: 25,
          header_quota_recover_at_ms: timestampMs + 60 * 60 * 1000,
        },
      ],
    });

    await renderAccountsPage();
    await flushPromises();
    await flushPromises();

    const writtenEntries: AccountQuotaSnapshotWriteEntry[] = vi
      .mocked(accountQuotaSnapshotApi.write)
      .mock.calls.flatMap((call) => call[2] ?? []);
    const headerEntry = writtenEntries.find(
      (entry) => entry.observation?.source === 'response_header'
    );
    expect(headerEntry).toEqual(
      expect.objectContaining({
        observation: {
          source: 'response_header',
          source_observation_id: 'header-fallback-event',
          observed_at_ms: timestampMs,
          inventory_scope_key: 'codex:rate-limits',
          inventory_mode: 'partial',
        },
        windows: expect.arrayContaining([
          expect.objectContaining({
            source: 'response_header',
            observed_at_ms: timestampMs,
          }),
        ]),
      })
    );
  });

  it('does not revive an unrecognized Codex inventory from an expired Header window', async () => {
    const generatedAtMs = Date.now();
    const timestampMs = generatedAtMs - 60 * 60 * 1000;
    const file = {
      ...makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
      disabled: true,
    } as AuthFileItem;
    mocks.files = [file];
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex.json%00auth-1&tab=quota',
    };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      windows: [],
      quotaInventoryObserved: false,
      fetchedAtMs: generatedAtMs,
    });
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: generatedAtMs,
      from_ms: timestampMs,
      to_ms: generatedAtMs,
      items: [
        {
          event_hash: 'expired-header-event',
          timestamp_ms: timestampMs,
          auth_file_snapshot: 'codex.json',
          auth_index: 'auth-1',
          account_snapshot: 'codex@example.com',
          auth_provider_snapshot: 'codex',
          header_quota_used_percent: 100,
          header_quota_recover_at_ms: generatedAtMs - 1,
        },
      ],
    });

    const renderer = await renderAccountsPage();
    await flushPromises();
    await flushPromises();

    expect(accountQuotaSnapshotApi.write).not.toHaveBeenCalled();
    expect(renderer.root.findAllByType(QuotaWindowCard)).toHaveLength(0);
  });

  it('does not persist a diagnostic-only Codex header as an empty partial observation', async () => {
    const timestampMs = Date.now();
    const file = {
      ...makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
      disabled: true,
    } as AuthFileItem;
    mocks.files = [file];
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex.json%00auth-1&tab=quota',
    };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: timestampMs,
      from_ms: timestampMs - 1_000,
      to_ms: timestampMs,
      items: [
        {
          event_hash: 'diagnostic-only-event',
          timestamp_ms: timestampMs,
          auth_file_snapshot: 'codex.json',
          auth_index: 'auth-1',
          account_snapshot: 'codex@example.com',
          auth_provider_snapshot: 'codex',
          header_trace_id: 'trace-diagnostic-only',
          response_metadata: {
            trace: { primary_trace_id: 'trace-diagnostic-only' },
          },
        },
      ],
    });

    await renderAccountsPage();
    await flushPromises();
    await flushPromises();

    expect(accountQuotaSnapshotApi.write).not.toHaveBeenCalled();
    expect(accountQuotaSnapshotApi.query).toHaveBeenCalled();
  });

  it('queries persisted quota lifecycle evidence when snapshot writing fails', async () => {
    const fetchedAtMs = Date.now();
    const file = {
      ...makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
      disabled: true,
    } as AuthFileItem;
    mocks.files = [file];
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex.json%00auth-1&tab=quota',
    };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      fetchedAtMs,
      quotaInventoryObserved: true,
      windows: [
        {
          id: 'five-hour',
          label: 'Five hours',
          usedPercent: 20,
          resetLabel: new Date(fetchedAtMs + 5 * 60 * 60 * 1000).toISOString(),
          resetAtMs: fetchedAtMs + 5 * 60 * 60 * 1000,
          resetAccuracy: 'exact',
          limitWindowSeconds: 5 * 60 * 60,
        },
      ],
    });
    vi.mocked(accountQuotaSnapshotApi.write).mockRejectedValue(
      new Error('snapshot write unavailable')
    );

    await renderAccountsPage();
    await flushPromises();
    await flushPromises();

    expect(accountQuotaSnapshotApi.write).toHaveBeenCalled();
    expect(accountQuotaSnapshotApi.query).toHaveBeenCalled();
    expect(mocks.getAccountWindowUsage).toHaveBeenCalled();
  });

  it('loads Manager quota snapshots without issuing monitoring requests when collection is disabled', async () => {
    const file = {
      ...makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
      disabled: true,
    } as AuthFileItem;
    mocks.files = [file];
    mocks.location = {
      pathname: '/accounts',
      search: '?account=codex.json%00auth-1&tab=quota',
    };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: false,
      serverCodexInspectionAvailable: false,
    };
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      fetchedAtMs: 123_456,
      quotaInventoryObserved: true,
      windows: [],
    });

    const renderer = await renderAccountsPage();
    await flushPromises();
    await flushPromises();

    expect(accountQuotaSnapshotApi.write).toHaveBeenCalled();
    expect(accountQuotaSnapshotApi.query).toHaveBeenCalled();
    expect(mocks.getAccountWindowUsage).not.toHaveBeenCalled();
    expect(mocks.getAccountHistory).not.toHaveBeenCalled();

    const quotaTab = renderer.root.findByType(AccountQuotaTab);
    expect(quotaTab.props.historyAvailable).toBe(false);
    expect(findHostButtonByText(renderer, 'accounts.refresh_history').props.disabled).toBe(true);

    await act(async () => {
      await quotaTab.props.onRefreshHistory();
    });
    expect(mocks.getAccountHistory).not.toHaveBeenCalled();
  });

  it('keeps quota display available when the Manager Server monitoring path is unavailable', async () => {
    const resetAtMs = Date.now() + 60 * 60 * 1000;
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: '',
      requestMonitoringAvailable: false,
      serverCodexInspectionAvailable: false,
    };
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(mocks.files[0], {
      status: 'success',
      windows: [
        {
          id: 'five-hour',
          label: 'Five hours',
          usedPercent: 20,
          resetLabel: new Date(resetAtMs).toISOString(),
          resetAtMs,
          resetAccuracy: 'exact',
          limitWindowSeconds: 5 * 60 * 60,
        },
      ],
    });

    const renderer = await renderAccountsPage();
    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    await flushPromises();

    expect(treeText(renderer)).toContain('Five hours');
    expect(treeText(renderer)).toContain('accounts.detail_quota_remaining_label');
    expect(accountQuotaSnapshotApi.write).not.toHaveBeenCalled();
    expect(accountQuotaSnapshotApi.query).not.toHaveBeenCalled();
    expect(mocks.getAccountWindowUsage).not.toHaveBeenCalled();
  });

  it('associates credential detail tabs with their active panel', async () => {
    const renderer = await renderAccountsPage();

    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });

    const tablist = renderer.root
      .findAllByProps({ role: 'tablist' })
      .find((node) => node.props['aria-label'] === 'accounts.detail_tablist_label');
    const panel = renderer.root.findByProps({ role: 'tabpanel' });
    const tabs = renderer.root
      .findAllByProps({ role: 'tab' })
      .filter((node) => node.props['aria-controls'] === panel.props.id);

    expect(tablist?.props['aria-label']).toBe('accounts.detail_tablist_label');
    expect(tabs).toHaveLength(5);
    expect(tabs.every((tab) => tab.props['aria-controls'] === panel.props.id)).toBe(true);
    expect(panel.props['aria-labelledby']).toBe('accounts-detail-tab-overview');
  });

  it('resets the detail drawer scroll position when switching tabs', async () => {
    const renderer = await renderAccountsPage();

    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
      await Promise.resolve();
    });

    const drawer = renderer.root.findByType(Drawer);
    const bodyRef = drawer.props.bodyRef as { current: { scrollTop: number } | null };
    bodyRef.current = { scrollTop: 240 };

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
      await Promise.resolve();
    });

    expect(bodyRef.current?.scrollTop).toBe(0);
  });

  it('uses the unified quota timestamp format for cooldown and reset-credit expiry', async () => {
    const cooldownRecoverAtMs = new Date(2026, 6, 30, 10, 5, 0, 0).getTime();
    const resetCreditExpiresAtMs = Date.now() + 24 * 60 * 60 * 1000;
    mocks.quotaState.codexQuota = {
      'codex.json': {
        status: 'success',
        authFileKey: 'codex.json::auth-1',
        windows: [],
        rateLimitResetCreditsAvailableCount: 1,
        rateLimitResetCredits: [
          {
            id: 'reset-credit-1',
            status: 'available',
            grantedAt: new Date(resetCreditExpiresAtMs - 24 * 60 * 60 * 1000).toISOString(),
            expiresAt: new Date(resetCreditExpiresAtMs).toISOString(),
          },
        ],
      },
    };
    mocks.getActiveQuotaCooldowns.mockResolvedValue([
      {
        authFileName: 'codex.json',
        authIndex: 'auth-1',
        recoverAtMs: cooldownRecoverAtMs,
        disabledAtMs: cooldownRecoverAtMs - 60 * 60 * 1000,
      },
    ]);

    const renderer = await renderAccountsPage();

    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await flushPromises();
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    await flushPromises();

    expect(readText(renderer.root.findByProps({ 'data-quota-cooldown-recover-at': 'true' }))).toBe(
      formatQuotaResetTimestamp(cooldownRecoverAtMs, 'en')
    );
    expect(
      readText(renderer.root.findByProps({ 'data-quota-reset-credit-expiry': 'reset-credit-1' }))
    ).toBe(formatQuotaResetTimestamp(resetCreditExpiresAtMs, 'en'));
    expect(
      renderer.root.findAllByProps({ 'data-account-quota-reset-records': 'true' })
    ).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-quota-evidence-panel': 'reset' })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-quota-evidence-panel': 'fields' })).toHaveLength(0);
    expect(
      renderer.root.findAllByProps({ 'data-quota-evidence-panel': 'diagnostics' })
    ).toHaveLength(0);
    expect(treeText(renderer)).toContain('codex_quota.reset_credits_card_subtitle');
    expect(treeText(renderer)).toContain('codex_quota.reset_credits_available_label');
    expect(treeText(renderer)).toContain('codex_quota.reset_credits_unit');
    expect(treeText(renderer)).toContain('codex_quota.reset_credits_expected_expiry_label');

    const resetAction = renderer.root.findByProps({ 'data-quota-reset-action': 'true' });
    expect(resetAction.props.disabled).toBe(false);
    expect(resetAction.props.className).toContain('quotaResetAction');
    expect(renderer.root.findAllByProps({ 'data-quota-reset-count': 'true' })).toHaveLength(1);
    await act(async () => {
      resetAction.props.onClick();
    });
    expect(mocks.showConfirmation).toHaveBeenCalledWith(
      expect.objectContaining({ title: 'codex_quota.reset_confirm_title' })
    );
  });

  it('keeps reset records visible but disables reset when no credits remain', async () => {
    const resetCreditExpiresAtMs = Date.now() + 24 * 60 * 60 * 1000;
    mocks.quotaState.codexQuota = {
      'codex.json': {
        status: 'success',
        authFileKey: 'codex.json::auth-1',
        windows: [],
        rateLimitResetCreditsAvailableCount: 0,
        rateLimitResetCredits: [
          {
            id: 'expired-reset-credit-1',
            status: 'available',
            grantedAt: new Date(resetCreditExpiresAtMs - 24 * 60 * 60 * 1000).toISOString(),
            expiresAt: new Date(resetCreditExpiresAtMs).toISOString(),
          },
        ],
      },
    };

    const renderer = await renderAccountsPage();

    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await flushPromises();
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    await flushPromises();

    expect(
      renderer.root.findAllByProps({ 'data-account-quota-reset-records': 'true' })
    ).toHaveLength(1);
    const resetAction = renderer.root.findByProps({ 'data-quota-reset-action': 'true' });
    expect(resetAction.props.disabled).toBe(true);
    expect(treeText(renderer)).toContain('codex_quota.reset_credits_unavailable_label');
    expect(mocks.showConfirmation).not.toHaveBeenCalled();
  });

  it('loads detail events filtered by auth file and auth index', async () => {
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    const renderer = await renderAccountsPage();
    mocks.getAnalytics.mockClear();

    const detailButton = renderer.root
      .findAll((node) => node.type === 'button')
      .find(
        (node) =>
          typeof node.props['aria-label'] === 'string' &&
          node.props['aria-label'].startsWith('accounts.open_detail:')
      );
    if (!detailButton) throw new Error('Detail button not found');

    await act(async () => {
      detailButton.props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_diagnostics').props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    const eventRequest = mocks.getAnalytics.mock.calls
      .map((call) => call[2] as AnalyticsRequestForTest)
      .find((request) => request.include?.events_page);

    expect(eventRequest?.filters).toEqual({
      auth_files: ['codex.json'],
      auth_indices: ['auth-1'],
    });
    expect(eventRequest?.include).toMatchObject({
      summary: true,
      summary_profile: 'compact',
      summary_percentiles: true,
      recent_failures: 1,
    });
    expect(eventRequest?.include?.events_page).toMatchObject({ limit: 20 });
  });

  it('does not show an animated icon during automatic diagnostic event loading', async () => {
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    const events = createDeferred<AnalyticsResponseForTest>();
    mocks.getAnalytics.mockImplementation(
      async (_base: string, _key: string | undefined, request: unknown) => {
        const analyticsRequest = request as AnalyticsRequestForTest;
        if (analyticsRequest.include?.events_page) return events.promise;
        return makeEmptyAnalyticsResponse();
      }
    );

    const renderer = await renderAccountsPage();
    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_diagnostics').props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    const panel = renderer.root.findByProps({ id: 'accounts-detail-tab-panel' });
    expect(findLoadingSpinners(panel)).toHaveLength(0);
    const refreshButton = panel
      .findAllByType(Button)
      .find((button) => readText(button.props.children).includes('common.refresh'));
    expect(refreshButton?.props.loading).toBe(false);

    await act(async () => {
      events.resolve(makeEventsResponse(makeAnalyticsEvent({ request_id: 'event-ready' })));
      await events.promise;
    });
    await flushPromises();

    expect(findLoadingSpinners(panel)).toHaveLength(0);

    const manualEvents = createDeferred<AnalyticsResponseForTest>();
    mocks.getAnalytics.mockImplementation(
      async (_base: string, _key: string | undefined, request: unknown) => {
        const analyticsRequest = request as AnalyticsRequestForTest;
        if (analyticsRequest.include?.events_page) return manualEvents.promise;
        return makeEmptyAnalyticsResponse();
      }
    );
    await act(async () => {
      renderer.root.findByType(AccountDiagnosticsTab).props.onRefreshEvents();
      await Promise.resolve();
    });

    expect(findLoadingSpinners(panel)).toHaveLength(1);
    manualEvents.resolve(makeEventsResponse(makeAnalyticsEvent({ request_id: 'manual-ready' })));
    await flushPromises();
    expect(findLoadingSpinners(panel)).toHaveLength(0);
  });

  it('renders full-range activity summary and recent failure independently of the event page', async () => {
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    const activityTimestamp = new Date(2026, 7, 26, 17, 44, 5, 0).getTime();
    mocks.getAnalytics.mockImplementation(
      async (_base: string, _key: string | undefined, request: unknown) => {
        const analyticsRequest = request as AnalyticsRequestForTest;
        if (!analyticsRequest.include?.events_page) return makeEmptyAnalyticsResponse();
        return {
          generated_at_ms: 2500,
          granularity: 'day',
          summary: {
            total_calls: 42,
            success_calls: 35,
            failure_calls: 7,
            success_rate: 35 / 42,
            input_tokens: 100,
            output_tokens: 50,
            total_tokens: 150,
            total_cost: 1.25,
            p95_latency_ms: 2345,
          },
          recent_failures: [
            {
              timestamp_ms: activityTimestamp,
              model: 'gpt-5',
              fail_status_code: 503,
              fail_summary: 'full-range failure',
            },
          ],
          events: {
            items: [makeAnalyticsEvent({ timestamp_ms: activityTimestamp, failed: false })],
            next_before_ms: 0,
            has_more: false,
            total_count: 42,
          },
        };
      }
    );

    const renderer = await renderAccountsPage();
    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_diagnostics').props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    const requestsMetric = renderer.root.findByProps({
      'data-diagnostic-activity-metric': 'requests',
    });
    const failureRateMetric = renderer.root.findByProps({
      'data-diagnostic-activity-metric': 'failure-rate',
    });
    const latencyMetric = renderer.root.findByProps({
      'data-diagnostic-activity-metric': 'p95-latency',
    });
    expect(readText(requestsMetric)).toContain('42');
    expect(readText(failureRateMetric)).toContain('16.7%');
    expect(readText(latencyMetric)).toContain('2345 ms');
    expect(treeText(renderer)).toContain('full-range failure');
    expect(treeText(renderer)).toContain('08/26 17:44:05');
  });

  it('renders the diagnostics tab with the prototype layout marker and active tab state', async () => {
    const renderer = await renderAccountsPage();
    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_diagnostics').props.onClick();
      await Promise.resolve();
    });

    const diagnosticShell = renderer.root.findByProps({ 'data-detail-tab': 'diagnostics' });
    expect(diagnosticShell.findByProps({ 'data-diagnostic-layout': 'prototype' })).toBeDefined();
    expect(diagnosticShell.findByProps({ 'data-diagnostic-card': 'conclusion' })).toBeDefined();
    expect(diagnosticShell.findByProps({ 'data-diagnostic-card': 'activity' })).toBeDefined();

    const selectedTabs = diagnosticShell
      .findAll((node) => node.type === 'button' && node.props['aria-selected'] === true)
      .filter((node) => node.props.role === 'tab');
    expect(selectedTabs).toHaveLength(1);
    expect(readText(selectedTabs[0])).toContain('accounts.detail_tab_diagnostics');
  });

  it('keeps the scoped monitoring link visible when the event list is empty', async () => {
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getAnalytics.mockImplementation(
      async (_base: string, _key: string | undefined, request: unknown) => {
        const analyticsRequest = request as AnalyticsRequestForTest;
        if (!analyticsRequest.include?.events_page) return makeEmptyAnalyticsResponse();
        return {
          generated_at_ms: 1,
          granularity: 'day',
          events: {
            items: [],
            next_before_ms: 0,
            has_more: false,
            total_count: 0,
          },
        };
      }
    );

    const renderer = await renderAccountsPage();
    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_diagnostics').props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    const monitoringLink = renderer.root
      .findAll((node) => node.type === 'a')
      .find((node) => String(node.props.href).startsWith('#/monitoring?'));
    expect(monitoringLink?.props.href).toBe('#/monitoring?auth_file=codex.json&auth_index=auth-1');
    expect(treeText(renderer)).not.toContain('accounts.detail_diagnostic_candidate_evidence');
  });

  it('translates known action-candidate reason codes in diagnostic evidence', async () => {
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.listAccountActionCandidates.mockResolvedValue({
      items: [
        {
          id: 9,
          actionType: 'reauth',
          status: 'pending',
          provider: 'codex',
          authFileName: 'codex.json',
          authIndex: 'auth-1',
          reasonCode: 'invalid_credentials',
          reason: 'Credentials are invalid or expired',
          firstSeenAtMs: 100,
          lastSeenAtMs: 200,
          hitCount: 2,
          createdAtMs: 100,
          updatedAtMs: 200,
        },
      ],
      pendingCount: 1,
    });

    const renderer = await renderAccountsPage();
    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_diagnostics').props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    await flushPromises();

    const diagnosticEvidence = renderer.root.findByProps({ 'data-diagnostic-card': 'evidence' });
    expect(diagnosticEvidence.props.open).toBeUndefined();
    expect(treeText(renderer)).toContain('account_actions.reason_invalid_credentials');
    expect(treeText(renderer)).not.toContain('Credentials are invalid or expired');
  });

  it('reloads diagnostic analytics after an in-flight request is invalidated by closing the drawer', async () => {
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    const firstEvents = createDeferred<AnalyticsResponseForTest>();
    let eventRequestCount = 0;
    mocks.getAnalytics.mockImplementation(
      async (_base: string, _key: string | undefined, request: unknown) => {
        const analyticsRequest = request as AnalyticsRequestForTest;
        if (!analyticsRequest.include?.events_page) return makeEmptyAnalyticsResponse();
        eventRequestCount += 1;
        if (eventRequestCount === 1) return firstEvents.promise;
        return makeEventsResponse(
          makeAnalyticsEvent({ request_id: 'req-reloaded', event_hash: 'event-reloaded' })
        );
      }
    );

    const renderer = await renderAccountsPage();
    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_diagnostics').props.onClick();
      await Promise.resolve();
    });
    await act(async () => {
      renderer.root.findByType(Drawer).props.onClose();
    });
    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_diagnostics').props.onClick();
    });
    await flushPromises();

    expect(eventRequestCount).toBe(2);
    expect(treeText(renderer)).toContain('req-reloaded');

    firstEvents.resolve(
      makeEventsResponse(makeAnalyticsEvent({ request_id: 'req-stale', event_hash: 'event-stale' }))
    );
    await flushPromises();
    expect(treeText(renderer)).toContain('req-reloaded');
    expect(treeText(renderer)).not.toContain('req-stale');
  });

  it('loads additional detail events with the returned cursor', async () => {
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getAnalytics.mockImplementation(
      async (_base: string, _key: string | undefined, request: unknown) => {
        const analyticsRequest = request as AnalyticsRequestForTest;
        if (!analyticsRequest.include?.events_page) return makeEmptyAnalyticsResponse();
        const eventsPage = analyticsRequest.include.events_page as {
          before_ms?: number | null;
          before_id?: number | null;
        };
        if (eventsPage.before_ms === 100 && eventsPage.before_id === 7) {
          return {
            generated_at_ms: 1,
            granularity: 'day',
            events: {
              items: [makeAnalyticsEvent({ request_id: 'req-older', event_hash: 'event-older' })],
              next_before_ms: 0,
              has_more: false,
              total_count: 42,
            },
          };
        }
        return {
          generated_at_ms: 1,
          granularity: 'day',
          events: {
            items: [makeAnalyticsEvent({ request_id: 'req-latest', event_hash: 'event-latest' })],
            next_before_ms: 100,
            next_before_id: 7,
            has_more: true,
            total_count: 42,
          },
        };
      }
    );

    const renderer = await renderAccountsPage();
    mocks.getAnalytics.mockClear();

    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_diagnostics').props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(treeText(renderer)).toContain('req-latest');

    await act(async () => {
      findButtonByText(renderer, 'accounts.detail_event_load_more').props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    const paginatedRequest = mocks.getAnalytics.mock.calls
      .map((call) => call[2] as AnalyticsRequestForTest)
      .find((request) => {
        const page = request.include?.events_page as
          | { before_ms?: number | null; before_id?: number | null }
          | undefined;
        return page?.before_ms === 100 && page.before_id === 7;
      });
    expect(paginatedRequest).toBeDefined();
    expect(treeText(renderer)).toContain('req-latest');
    expect(treeText(renderer)).toContain('req-older');
  });

  it('ignores stale detail-event responses after switching rows', async () => {
    mocks.files = [
      makeCodexFile('codex-a.json', 'auth-a', 'first@example.com'),
      makeCodexFile('codex-b.json', 'auth-b', 'second@example.com'),
    ];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };

    const firstEvents = createDeferred<AnalyticsResponseForTest>();
    const secondEvents = createDeferred<AnalyticsResponseForTest>();
    mocks.getAnalytics.mockImplementation(
      async (_base: string, _key: string | undefined, request: unknown) => {
        const analyticsRequest = request as AnalyticsRequestForTest;
        if (!analyticsRequest.include?.events_page) {
          return makeEmptyAnalyticsResponse();
        }
        const fileName = analyticsRequest.filters?.auth_files?.[0];
        if (fileName === 'codex-a.json') return firstEvents.promise;
        if (fileName === 'codex-b.json') return secondEvents.promise;
        return makeEventsResponse(makeAnalyticsEvent({}));
      }
    );

    const renderer = await renderAccountsPage();
    mocks.getAnalytics.mockClear();

    await act(async () => {
      findDetailButtonByName(renderer, 'codex-a.json').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_diagnostics').props.onClick();
      await Promise.resolve();
    });
    await act(async () => {
      findDetailButtonByName(renderer, 'codex-b.json').props.onClick();
      await Promise.resolve();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_diagnostics').props.onClick();
      await Promise.resolve();
    });

    await act(async () => {
      secondEvents.resolve(
        makeEventsResponse(
          makeAnalyticsEvent({
            request_id: 'req-second',
            event_hash: 'event-second',
            auth_index: 'auth-b',
            source: 'codex-b.json',
          })
        )
      );
      await Promise.resolve();
    });

    expect(treeText(renderer)).toContain('req-second');

    await act(async () => {
      firstEvents.resolve(
        makeEventsResponse(
          makeAnalyticsEvent({
            request_id: 'req-first',
            event_hash: 'event-first',
            auth_index: 'auth-a',
            source: 'codex-a.json',
          })
        )
      );
      await Promise.resolve();
    });

    expect(treeText(renderer)).toContain('req-second');
    expect(treeText(renderer)).not.toContain('req-first');
  });
});
