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
import type {
  AuthFileItem,
  CodexQuotaState,
  CodexRateLimitResetCredit,
  OAuthModelAliasEntry,
} from '@/types';
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
} from '@/features/authFiles/model/credentialStatus';
import type { AuthFilesCredentialMutation } from '@/features/authFiles/hooks/useAuthFilesData';
import { clearAccountCredentialEvidenceBoundaryStateCache } from './model/accountCredentialEvidenceStorage';
import {
  clearAccountCredentialMutationMarkersForTests,
  createAccountCredentialMutationBaseline,
  listAccountCredentialMutationMarkers,
  recordAccountCredentialMutationMarker,
} from './model/accountCredentialMutationMarker';
import type { CodexQuotaData } from '@/utils/quota/providerRequests';
import { buildKimiQuotaRows } from '@/utils/quota/builders';
import type {
  CredentialInspectionSnapshot,
  CredentialInspectionTarget,
} from '@/features/monitoring/model/credentialInspectionSnapshot';
import type { CodexReauthTarget } from '@/features/oauth/codexReauthModel';
import { AccountDiagnosticsTab } from './components/accountDetail/AccountDiagnosticsTab';
import { AccountModelsTab } from './components/accountDetail/AccountModelsTab';
import { AccountOverviewTab } from './components/accountDetail/AccountOverviewTab';
import { AccountQuotaTab } from './components/accountDetail/AccountQuotaTab';
import { QuotaWindowCard } from './components/QuotaWindowCard';
import { formatQuotaResetTimestamp } from './model/accountsPagePresentation';
import { buildAccountQuotaDisplayWindow } from './model/accountQuotaDisplayWindows';
import type { AccountQuotaDisplayWindow } from './model/accountQuotaDisplayWindows';
import {
  completeAccountOAuthReauthSession,
  readAccountOAuthReauthSessionId,
} from './model/accountReauthSession';
import {
  clearPendingAccountDirectReauthsForTests,
  listPendingAccountDirectReauths,
} from './model/accountDirectReauth';
import { useUsageHeaderSnapshotStore } from '@/stores/useUsageHeaderSnapshotStore';
import { publishAccountCredentialMutationRevision } from '@/stores';
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

const CODEX_MAIN_MODEL = 'gpt-5.6-sol';
const CODEX_MAIN_SCOPE = { kind: 'family', key: 'codex_main', complete: true } as const;

const makeCodexQuotaData = (
  resetCreditsAvailableCount: number | null = null,
  credits: CodexRateLimitResetCredit[] = []
): CodexQuotaData => ({
  planType: 'plus',
  windows: [],
  quotaInventoryObserved: true,
  subscriptionActiveUntil: null,
  rateLimitResetCreditsAvailableCount: resetCreditsAvailableCount,
  rateLimitResetCredits: credits,
  rateLimitResetCreditsError: null,
});

const makeCodexQuotaWindow = (
  overrides: Partial<CodexQuotaState['windows'][number]> = {}
): CodexQuotaState['windows'][number] => ({
  id: 'five-hour',
  label: 'Five hours',
  usedPercent: 20,
  resetLabel: 'later',
  resetAtMs: Date.now() + 6 * 60 * 60 * 1000,
  resetAccuracy: 'exact',
  limitWindowSeconds: 5 * 60 * 60,
  modelScope: CODEX_MAIN_SCOPE,
  ...overrides,
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
      loadFiles: vi.fn<
        (options?: { throwOnError?: boolean }) => Promise<AuthFileItem[] | undefined>
      >(async () => undefined),
      lastAuthFilesDataOptions: null as null | {
        connectionFingerprint?: string | null;
        onCredentialMutation?: (mutation: AuthFilesCredentialMutation) => void;
      },
      toggleSelect: vi.fn(),
      selectAllVisible: vi.fn(),
      invertVisibleSelection: vi.fn(),
      deselectAll: vi.fn(),
      batchPatchFields: vi.fn(async () => ({ success: 1, failed: 0, failedNames: [] })),
      batchSetStatus: vi.fn(async () => undefined),
      consumeResetCredit: vi.fn(async () => ({ statusCode: 200, body: '' })),
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
        requestScope: { apiBase: string; managementKey: string };
        onClose: () => void;
      },
      lastAliasEditorProps: null as null | {
        open: boolean;
        provider?: string;
        requestScope: { apiBase: string; managementKey: string };
        onClose: () => void;
      },
      lastCodexReauthProps: null as null | {
        open: boolean;
        target: CodexReauthTarget | null;
        requestScope?: { apiBase: string; managementKey: string };
        onClose: () => void;
        onSuccess?: () => void | Promise<void>;
      },
      localInspection: null as null | Record<string, unknown>,
      lastHealthWorkspaceProps: null as null | {
        mode: 'local' | 'server';
        onModeChange: (mode: 'local' | 'server') => void;
        onSnapshotChange: (snapshot: CredentialInspectionSnapshot) => void;
        onCredentialsChanged: (
          target?: CodexReauthTarget | null,
          snapshot?: CredentialInspectionSnapshot | null
        ) => void | Promise<void>;
        onCodexReauthStart?: (target: CodexReauthTarget) => boolean | void;
        onOpenCredential: (target: CredentialInspectionTarget) => void;
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
        if (key === 'auth_files.codex_plan_filter_unknown') return 'Unknown plan';
        if (!options) return key;
        const parts: string[] = [];
        if (typeof options.name === 'string') parts.push(options.name);
        if (typeof options.count === 'number') parts.push(String(options.count));
        if (typeof options.message === 'string') parts.push(options.message);
        if (typeof options.requests === 'string') parts.push(options.requests);
        if (typeof options.tokens === 'string') parts.push(options.tokens);
        if (typeof options.cost === 'string') parts.push(options.cost);
        if (typeof options.rate === 'string') parts.push(options.rate);
        return parts.length > 0 ? `${key}:${parts.join(':')}` : key;
      },
      quotaDisplayWindowsOverride: null as AccountQuotaDisplayWindow[] | null,
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
  useAuthFilesData: (
    options: {
      connectionFingerprint?: string | null;
      onCredentialMutation?: (mutation: AuthFilesCredentialMutation) => void;
    } = {}
  ) => {
    mocks.lastAuthFilesDataOptions = options;
    return {
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
    };
  },
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
vi.mock('@/features/accounts/model/accountQuotaDisplayWindows', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/accounts/model/accountQuotaDisplayWindows')>();
  return {
    ...actual,
    buildAccountQuotaDisplayWindows: (
      row: Parameters<typeof actual.buildAccountQuotaDisplayWindows>[0],
      options: Parameters<typeof actual.buildAccountQuotaDisplayWindows>[1]
    ) => mocks.quotaDisplayWindowsOverride ?? actual.buildAccountQuotaDisplayWindows(row, options),
  };
});

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
      disableCooling: 'inherit' as const,
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
    onSnapshotChange: (snapshot: CredentialInspectionSnapshot) => void;
    onCredentialsChanged: (
      target?: CodexReauthTarget | null,
      snapshot?: CredentialInspectionSnapshot | null
    ) => void | Promise<void>;
    onCodexReauthStart?: (target: CodexReauthTarget) => boolean | void;
    onOpenCredential: (target: CredentialInspectionTarget) => void;
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
  OAuthExcludedEditorModal: (props: {
    open: boolean;
    provider?: string;
    requestScope: { apiBase: string; managementKey: string };
    onClose: () => void;
  }) => {
    mocks.lastExcludedEditorProps = props;
    return props.open ? <div>oauth-excluded-editor-open</div> : null;
  },
  OAuthModelAliasEditorModal: (props: {
    open: boolean;
    provider?: string;
    requestScope: { apiBase: string; managementKey: string };
    onClose: () => void;
  }) => {
    mocks.lastAliasEditorProps = props;
    return props.open ? <div>oauth-alias-editor-open</div> : null;
  },
}));

vi.mock('@/features/oauth/CodexReauthDialog', () => ({
  CodexReauthDialog: (props: {
    open: boolean;
    target: CodexReauthTarget | null;
    requestScope?: { apiBase: string; managementKey: string };
    onClose: () => void;
    onSuccess?: () => void | Promise<void>;
  }) => {
    mocks.lastCodexReauthProps = props;
    return props.open ? <div data-codex-reauth-open="true" /> : null;
  },
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
  consumeCodexRateLimitResetCredit: mocks.consumeResetCredit,
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
  publishAccountCredentialMutationRevision: vi.fn(),
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

const findQuotaBarByWindow = (card: ReactTestInstance, windowKey: string) => {
  const quotaWindow = card.findByProps({ 'data-account-quota-window': windowKey });
  const bar = quotaWindow.findAll(
    (node) =>
      typeof node.props.className === 'string' &&
      node.props.className.includes('quotaBar') &&
      !node.props.className.includes('quotaTrack')
  )[0];
  if (!bar) throw new Error(`Quota bar not found: ${windowKey}`);
  return bar;
};

const findQuotaBarByMatrixCell = (card: ReactTestInstance, cellKey: string) => {
  const cell = card.findByProps({ 'data-account-quota-matrix-cell': cellKey });
  const bar = cell.findAll(
    (node) =>
      typeof node.props.className === 'string' &&
      node.props.className.includes('quotaBar') &&
      !node.props.className.includes('quotaTrack')
  )[0];
  if (!bar) throw new Error(`Matrix quota bar not found: ${cellKey}`);
  return bar;
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

const openCodexQuotaTab = async (renderer: ReactTestRenderer, fileName: string) => {
  await act(async () => {
    findDetailButtonByName(renderer, fileName).props.onClick();
  });
  await flushPromises();
  await act(async () => {
    findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
  });
  await flushPromises();
};

const applyCodexQuotaCommits = (): Record<string, CodexQuotaState> => {
  let states = mocks.quotaState.codexQuota as Record<string, CodexQuotaState>;
  mocks.quotaState.setCodexQuota.mock.calls.forEach((call) => {
    const updater = call[0] as (
      prev: Record<string, CodexQuotaState>
    ) => Record<string, CodexQuotaState>;
    if (typeof updater === 'function') states = updater(states);
  });
  return states;
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

const findAccountDetailRegion = (
  renderer: ReactTestRenderer,
  selectionKey: string,
  kind: 'history' | 'quota'
) => {
  const region = findAccountCardByKey(renderer, selectionKey).findAll(
    (node) => node.props['data-account-detail-region'] === kind
  )[0];
  if (!region) throw new Error(`Account detail region not found: ${kind}`);
  return region;
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

const getAccountCardText = (renderer: ReactTestRenderer, selectionKey: string) =>
  readText(findAccountCardByKey(renderer, selectionKey));

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

const makeResetCredit = (id: string): CodexRateLimitResetCredit => ({
  id,
  status: 'available',
  grantedAt: new Date(Date.now() - 60_000).toISOString(),
  expiresAt: new Date(Date.now() + 24 * 60 * 60 * 1000).toISOString(),
});

const makeInspectionSnapshot = (
  files: AuthFileItem[],
  results: Array<Partial<CodexInspectionResult>>,
  completedAtMs = 2_000
): CredentialInspectionSnapshot => ({
  source: 'server',
  completedAtMs,
  runs: [],
  results: results.map((overrides, index) => {
    const file = files[index] ?? files[0];
    return {
      id: index + 1,
      runId: 1,
      accountKey: getAuthFileSelectionKey(file),
      fileName: file.name,
      displayAccount: String(file.account ?? file.name),
      runtimeId: typeof file.id === 'string' ? file.id : undefined,
      accountSnapshot: String(file.account ?? ''),
      authIndex: String(file.authIndex ?? ''),
      provider: 'codex',
      disabled: Boolean(file.disabled),
      action: 'keep',
      actionReason: '',
      actionStatus: 'none',
      statusCode: 200,
      usedPercent: 30,
      isQuota: false,
      createdAtMs: completedAtMs,
      inspectionSource: 'server',
      ...overrides,
    };
  }),
});

const installCodexQuotaStoreMutationMock = () => {
  mocks.quotaState.setCodexQuota.mockImplementation(
    (
      update:
        | Record<string, CodexQuotaState>
        | ((current: Record<string, CodexQuotaState>) => Record<string, CodexQuotaState>)
    ) => {
      const current = mocks.quotaState.codexQuota as Record<string, CodexQuotaState>;
      mocks.quotaState.codexQuota = typeof update === 'function' ? update(current) : update;
    }
  );
};

const flushPromises = async () => {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
};

const runCodexReauthSuccessAndCaptureError = async (): Promise<unknown> => {
  const onSuccess = mocks.lastCodexReauthProps?.onSuccess;
  if (!onSuccess) throw new Error('Codex re-login success callback not found');
  let caught: unknown;
  await act(async () => {
    try {
      await onSuccess();
    } catch (error) {
      caught = error;
    }
  });
  return caught;
};

const runInspectionCodexReauth = async (target: CodexReauthTarget): Promise<void> => {
  expect(mocks.lastHealthWorkspaceProps?.onCodexReauthStart?.(target)).not.toBe(false);
  await mocks.lastHealthWorkspaceProps?.onCredentialsChanged(target);
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
    clearAccountCredentialEvidenceBoundaryStateCache();
    clearAccountCredentialMutationMarkersForTests();
    clearPendingAccountDirectReauthsForTests();
    if (typeof window !== 'undefined') {
      window.localStorage.clear();
      window.sessionStorage?.clear();
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
    vi.mocked(publishAccountCredentialMutationRevision).mockClear();
    mocks.toggleSelect.mockClear();
    mocks.selectAllVisible.mockClear();
    mocks.invertVisibleSelection.mockClear();
    mocks.deselectAll.mockClear();
    mocks.batchSetStatus.mockClear();
    mocks.batchPatchFields.mockClear();
    mocks.consumeResetCredit.mockClear();
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
    mocks.quotaDisplayWindowsOverride = null;
    mocks.quotaState.setAntigravityQuota.mockReset();
    mocks.quotaState.setClaudeQuota.mockReset();
    mocks.quotaState.setCodexQuota.mockReset();
    mocks.quotaState.setKimiQuota.mockReset();
    mocks.quotaState.setXaiQuota.mockReset();
    mocks.loadFiles.mockReset();
    mocks.loadFiles.mockImplementation(async () => mocks.files);
    mocks.lastAuthFilesDataOptions = null;
    mocks.loadExcluded.mockClear();
    mocks.loadModelAlias.mockClear();
    mocks.oauthExcluded = {};
    mocks.oauthModelAlias = {};
    mocks.lastExcludedEditorProps = null;
    mocks.lastAliasEditorProps = null;
    mocks.lastCodexReauthProps = null;
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
    expect(mocks.lastExcludedEditorProps?.requestScope).toEqual({
      apiBase: 'http://cpa-a.local:8317',
      managementKey: 'manager-key',
    });

    await act(async () => {
      findHostButtonByText(renderer, 'oauth-alias-edit').props.onClick();
    });

    expect(mocks.navigate).not.toHaveBeenCalledWith(
      expect.stringContaining('/auth-files/oauth-model-alias'),
      expect.anything()
    );
    expect(mocks.lastAliasEditorProps?.open).toBe(true);
    expect(mocks.lastAliasEditorProps?.provider).toBe('codex');
    expect(mocks.lastAliasEditorProps?.requestScope).toEqual({
      apiBase: 'http://cpa-a.local:8317',
      managementKey: 'manager-key',
    });
  });

  it.each([
    ['excluded', 'oauth-excluded-add'],
    ['alias', 'oauth-alias-add'],
  ] as const)('closes the %s editor when the CPA connection changes', async (editor, trigger) => {
    const renderer = await renderAccountsPage();

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_oauth').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, trigger).props.onClick();
    });
    const readEditorProps = () =>
      editor === 'excluded' ? mocks.lastExcludedEditorProps : mocks.lastAliasEditorProps;
    expect(readEditorProps()?.open).toBe(true);

    mocks.apiBase = 'http://cpa-b.local:8317';
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await flushPromises();

    expect(readEditorProps()?.open).toBe(false);
    expect(readEditorProps()?.requestScope).toEqual({
      apiBase: 'http://cpa-b.local:8317',
      managementKey: 'manager-key',
    });
  });

  it('closes Codex re-login and publishes the new CPA scope when the connection changes', async () => {
    const file = {
      ...makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
      status: 'error',
      statusMessage: 'token_expired',
      errorStatus: 401,
      statusCode: 401,
    } as AuthFileItem;
    mocks.files = [file];
    const selectionKey = getAuthFileSelectionKey(file);
    const renderer = await renderAccountsPage();

    await act(async () => {
      findAccountCardButtonByAriaLabel(
        renderer,
        selectionKey,
        'accounts.recommend_action_reauth'
      ).props.onClick();
    });

    expect(mocks.lastCodexReauthProps?.open).toBe(true);
    expect(mocks.lastCodexReauthProps?.requestScope).toEqual({
      apiBase: 'http://cpa-a.local:8317',
      managementKey: 'manager-key',
    });

    mocks.apiBase = 'http://cpa-b.local:8317';
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await flushPromises();

    expect(mocks.lastCodexReauthProps?.open).toBe(false);
    expect(mocks.lastCodexReauthProps?.requestScope).toEqual({
      apiBase: 'http://cpa-b.local:8317',
      managementKey: 'manager-key',
    });
  });

  it('keeps a direct re-login pending after a credential reload failure and retries it manually', async () => {
    const file = {
      ...makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
      account_id: 'space-codex',
      status: 'error',
      statusMessage: 'token_expired',
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    mocks.files = [file];
    const selectionKey = getAuthFileSelectionKey(file);
    const renderer = await renderAccountsPage();

    await act(async () => {
      findAccountCardButtonByAriaLabel(
        renderer,
        selectionKey,
        'accounts.recommend_action_reauth'
      ).props.onClick();
    });
    mocks.loadFiles
      .mockRejectedValueOnce(new Error('temporary auth-file list failure'))
      .mockImplementationOnce(async () => {
        mocks.files = [
          { ...file, status: 'ready', statusMessage: '', last_refresh: 3_000, modified: 3_100 },
        ] as AuthFileItem[];
        return mocks.files;
      });

    expect(await runCodexReauthSuccessAndCaptureError()).toEqual(
      new Error('temporary auth-file list failure')
    );
    expect(listPendingAccountDirectReauths('http://cpa-a.local:8317:manager-key')).toHaveLength(1);

    await act(async () => {
      await findButtonByText(renderer, 'common.refresh').props.onClick();
    });
    await flushPromises();

    expect(listPendingAccountDirectReauths('http://cpa-a.local:8317:manager-key')).toEqual([]);
    expect(getAccountCardText(renderer, selectionKey)).not.toContain('accounts.health_reauth');
  });

  it('creates a persisted recovery boundary for a confirmed re-login with stale raw 401 evidence', async () => {
    const file = {
      ...makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
      account_id: 'space-a',
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    const reloadedFile = {
      ...file,
      last_refresh: 3_000,
      modified: 3_100,
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
    } as AuthFileItem;
    mocks.files = [file];
    const selectionKey = getAuthFileSelectionKey(file);
    const renderer = await renderAccountsPage();

    await act(async () => {
      findAccountCardButtonByAriaLabel(
        renderer,
        selectionKey,
        'accounts.recommend_action_reauth'
      ).props.onClick();
    });
    mocks.loadFiles.mockImplementationOnce(async () => {
      mocks.files = [reloadedFile];
      return mocks.files;
    });

    expect(await runCodexReauthSuccessAndCaptureError()).toBeUndefined();
    await flushPromises();

    expect(getAccountCardText(renderer, selectionKey)).not.toContain('accounts.health_reauth');
    expect(getAccountCardText(renderer, selectionKey)).not.toContain('accounts.health_available');

    await act(async () => {
      renderer.unmount();
    });
    mountedAccountsRenderers.delete(renderer);

    const remountedRenderer = await renderAccountsPage();
    await flushPromises();
    expect(getAccountCardText(remountedRenderer, selectionKey)).not.toContain(
      'accounts.health_reauth'
    );
  });

  it('keeps an older exhausted quota while clearing an unknown-time quota 401 after re-login', async () => {
    const file = {
      ...makeCodexFile('quota-auth.codex.json', 'auth-1', 'quota-auth@example.com'),
      account_id: 'space-quota-auth',
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    const reloadedFile = {
      ...file,
      last_refresh: 3_000,
      modified: 3_100,
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
    } as AuthFileItem;
    const quota = {
      status: 'error' as const,
      windows: [
        makeCodexQuotaWindow({
          id: 'weekly',
          usedPercent: 100,
          observedAtMs: 1_000,
          resetLabel: 'later',
          limitWindowSeconds: 604_800,
        }),
      ],
      quotaInventoryObserved: true,
      error: 'HTTP 401 token expired',
      errorStatus: 401,
      ...buildQuotaCredentialIdentity(file),
    } satisfies CodexQuotaState;
    mocks.files = [file];
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, quota);
    installCodexQuotaStoreMutationMock();
    const selectionKey = getAuthFileSelectionKey(file);
    const quotaStoreKey = getQuotaCredentialStoreKey(file);
    const renderer = await renderAccountsPage();

    await act(async () => {
      findAccountCardButtonByAriaLabel(
        renderer,
        selectionKey,
        'accounts.recommend_action_reauth'
      ).props.onClick();
    });
    mocks.loadFiles.mockImplementationOnce(async () => {
      mocks.files = [reloadedFile];
      return mocks.files;
    });

    expect(await runCodexReauthSuccessAndCaptureError()).toBeUndefined();
    await flushPromises();

    const retainedQuota = (mocks.quotaState.codexQuota as Record<string, CodexQuotaState>)[
      quotaStoreKey
    ];
    expect(retainedQuota).toMatchObject({
      status: 'success',
      error: undefined,
      errorStatus: undefined,
      windows: [expect.objectContaining({ id: 'weekly', usedPercent: 100 })],
    });
    const accountCard = findAccountCardByKey(renderer, selectionKey);
    expect(readText(accountCard)).not.toContain('accounts.health_reauth');
    expect(readText(accountCard)).not.toContain('accounts.health_available');
    expect(readText(accountCard)).toContain('accounts.health_weekly_exhausted');
  });

  it('keeps an older 429 quota limit after confirmed re-login', async () => {
    const file = {
      ...makeCodexFile('quota-429.codex.json', 'auth-1', 'quota-429@example.com'),
      account_id: 'space-quota-429',
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    const reloadedFile = {
      ...file,
      last_refresh: 3_000,
      modified: 3_100,
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
    } as AuthFileItem;
    const quota = {
      status: 'error' as const,
      windows: [],
      error: 'HTTP 429 rate limit reached',
      errorStatus: 429,
      failedAtMs: 1_000,
      ...buildQuotaCredentialIdentity(file),
    } satisfies CodexQuotaState;
    mocks.files = [file];
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, quota);
    installCodexQuotaStoreMutationMock();
    const selectionKey = getAuthFileSelectionKey(file);
    const quotaStoreKey = getQuotaCredentialStoreKey(file);
    const renderer = await renderAccountsPage();

    await act(async () => {
      findAccountCardButtonByAriaLabel(
        renderer,
        selectionKey,
        'accounts.recommend_action_reauth'
      ).props.onClick();
    });
    mocks.loadFiles.mockImplementationOnce(async () => {
      mocks.files = [reloadedFile];
      return mocks.files;
    });

    expect(await runCodexReauthSuccessAndCaptureError()).toBeUndefined();
    await flushPromises();

    const retainedQuota = (mocks.quotaState.codexQuota as Record<string, CodexQuotaState>)[
      quotaStoreKey
    ];
    expect(retainedQuota).toMatchObject({
      status: 'error',
      errorStatus: 429,
      failedAtMs: 1_000,
    });
    const accountCard = findAccountCardByKey(renderer, selectionKey);
    expect(readText(accountCard)).not.toContain('accounts.health_reauth');
    expect(readText(accountCard)).toContain('accounts.health_limited');
  });

  it('supersedes stale Header authentication while retaining its quota evidence', async () => {
    const file = {
      ...makeCodexFile('header.codex.json', 'auth-1', 'header@example.com'),
      account_id: 'space-header',
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    const reloadedFile = {
      ...file,
      last_refresh: 2_000,
      modified: 2_100,
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
    } as AuthFileItem;
    mocks.files = [file];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: 3_000,
      from_ms: 0,
      to_ms: 3_000,
      items: [
        {
          event_hash: 'stale-auth-header',
          timestamp_ms: 3_000,
          model: CODEX_MAIN_MODEL,
          auth_file_snapshot: file.name,
          auth_index: 'auth-1',
          account_snapshot: 'header@example.com',
          auth_provider_snapshot: 'codex',
          header_error_kind: 'auth',
          header_error_code: 'invalid_token',
          header_quota_used_percent: 20,
        },
      ],
    });
    const selectionKey = getAuthFileSelectionKey(file);
    const renderer = await renderAccountsPage();
    await flushPromises();

    expect(getAccountCardText(renderer, selectionKey)).toContain('accounts.health_reauth');
    await act(async () => {
      findAccountCardButtonByAriaLabel(
        renderer,
        selectionKey,
        'accounts.recommend_action_reauth'
      ).props.onClick();
    });
    mocks.loadFiles.mockImplementationOnce(async () => {
      mocks.files = [reloadedFile];
      return mocks.files;
    });

    expect(await runCodexReauthSuccessAndCaptureError()).toBeUndefined();
    await flushPromises();

    expect(getAccountCardText(renderer, selectionKey)).not.toContain('accounts.health_reauth');

    await act(async () => {
      findDetailButtonByName(renderer, file.name).props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    const quotaDetailView = renderer.root.findByType(AccountQuotaTab).props.detailView;
    expect(quotaDetailView.quota.windows).toEqual(
      expect.arrayContaining([expect.objectContaining({ usedPercent: 20 })])
    );
  });

  it('applies the recovery boundary to a confirmed credential after identity rotation', async () => {
    const original = {
      ...makeCodexFile('codex-old.json', 'auth-1', 'workspace@example.com'),
      account_id: 'space-a',
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    const replacement = {
      ...makeCodexFile('codex-new.json', 'auth-2', 'workspace@example.com'),
      account_id: 'space-a',
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      last_refresh: 3_000,
      modified: 3_100,
    } as AuthFileItem;
    mocks.files = [original];
    const originalSelectionKey = getAuthFileSelectionKey(original);
    const replacementSelectionKey = getAuthFileSelectionKey(replacement);
    const renderer = await renderAccountsPage();

    await act(async () => {
      findAccountCardButtonByAriaLabel(
        renderer,
        originalSelectionKey,
        'accounts.recommend_action_reauth'
      ).props.onClick();
    });
    mocks.loadFiles.mockImplementationOnce(async () => {
      mocks.files = [replacement];
      return mocks.files;
    });

    expect(await runCodexReauthSuccessAndCaptureError()).toBeUndefined();
    await flushPromises();

    expect(
      renderer.root.findAllByProps({ 'data-account-card': originalSelectionKey })
    ).toHaveLength(0);
    expect(getAccountCardText(renderer, replacementSelectionKey)).not.toContain(
      'accounts.health_reauth'
    );
  });

  it('migrates exhausted quota when confirmed re-login rotates only the auth index', async () => {
    const original = {
      ...makeCodexFile('codex.json', 'auth-1', 'workspace@example.com'),
      account_id: 'space-a',
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    const replacement = {
      ...original,
      authIndex: 'auth-2',
      last_refresh: 3_000,
      modified: 3_100,
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
    } as AuthFileItem;
    const quota = {
      status: 'success' as const,
      windows: [
        makeCodexQuotaWindow({
          id: 'weekly',
          usedPercent: 100,
          observedAtMs: 1_000,
          limitWindowSeconds: 604_800,
        }),
      ],
      quotaInventoryObserved: true,
      fetchedAtMs: 1_000,
      ...buildQuotaCredentialIdentity(original),
    } satisfies CodexQuotaState;
    const originalStoreKey = getQuotaCredentialStoreKey(original);
    const replacementStoreKey = getQuotaCredentialStoreKey(replacement);
    const replacementAuthFailure = {
      status: 'error' as const,
      windows: [],
      error: 'HTTP 401 token expired',
      errorStatus: 401,
      failedAtMs: 1_500,
      ...buildQuotaCredentialIdentity(replacement),
    } satisfies CodexQuotaState;
    mocks.files = [original];
    mocks.quotaState.codexQuota = {
      ...buildCredentialScopedQuotaRecord(original, quota),
      [replacementStoreKey]: replacementAuthFailure,
    };
    installCodexQuotaStoreMutationMock();
    const originalSelectionKey = getAuthFileSelectionKey(original);
    const replacementSelectionKey = getAuthFileSelectionKey(replacement);
    const renderer = await renderAccountsPage();

    await act(async () => {
      findAccountCardButtonByAriaLabel(
        renderer,
        originalSelectionKey,
        'accounts.recommend_action_reauth'
      ).props.onClick();
    });
    mocks.loadFiles.mockImplementationOnce(async () => {
      mocks.files = [replacement];
      return mocks.files;
    });

    expect(await runCodexReauthSuccessAndCaptureError()).toBeUndefined();
    await flushPromises();

    expect(mocks.quotaState.codexQuota).not.toHaveProperty(originalStoreKey);
    expect(mocks.quotaState.codexQuota).toMatchObject({
      [replacementStoreKey]: {
        authFileKey: replacementStoreKey,
        status: 'success',
        windows: [expect.objectContaining({ id: 'weekly', usedPercent: 100 })],
      },
    });
    const accountCard = findAccountCardByKey(renderer, replacementSelectionKey);
    expect(readText(accountCard)).not.toContain('accounts.health_reauth');
    expect(readText(accountCard)).not.toContain('accounts.health_available');
    expect(readText(accountCard)).toContain('accounts.health_weekly_exhausted');
  });

  it('migrates a filename and auth-index rotation without dropping an older 429', async () => {
    const original = {
      ...makeCodexFile('codex-old.json', 'auth-1', 'workspace@example.com'),
      account_id: 'space-a',
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    const replacement = {
      ...makeCodexFile('codex-new.json', 'auth-2', 'workspace@example.com'),
      account_id: 'space-a',
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      last_refresh: 3_000,
      modified: 3_100,
    } as AuthFileItem;
    const quota = {
      status: 'error' as const,
      windows: [],
      error: 'HTTP 429 rate limit reached',
      errorStatus: 429,
      failedAtMs: 1_000,
      ...buildQuotaCredentialIdentity(original),
    } satisfies CodexQuotaState;
    mocks.files = [original];
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(original, quota);
    installCodexQuotaStoreMutationMock();
    const originalStoreKey = getQuotaCredentialStoreKey(original);
    const replacementStoreKey = getQuotaCredentialStoreKey(replacement);
    const originalSelectionKey = getAuthFileSelectionKey(original);
    const replacementSelectionKey = getAuthFileSelectionKey(replacement);
    const renderer = await renderAccountsPage();

    await act(async () => {
      findAccountCardButtonByAriaLabel(
        renderer,
        originalSelectionKey,
        'accounts.recommend_action_reauth'
      ).props.onClick();
    });
    mocks.loadFiles.mockImplementationOnce(async () => {
      mocks.files = [replacement];
      return mocks.files;
    });

    expect(await runCodexReauthSuccessAndCaptureError()).toBeUndefined();
    await flushPromises();

    expect(mocks.quotaState.codexQuota).not.toHaveProperty(originalStoreKey);
    expect(mocks.quotaState.codexQuota).toMatchObject({
      [replacementStoreKey]: {
        authFileKey: replacementStoreKey,
        status: 'error',
        errorStatus: 429,
        failedAtMs: 1_000,
      },
    });
    const accountCard = findAccountCardByKey(renderer, replacementSelectionKey);
    expect(readText(accountCard)).not.toContain('accounts.health_reauth');
    expect(readText(accountCard)).toContain('accounts.health_limited');
  });

  it('clears an unknown-time old 401 while retaining its quota windows during identity rotation', async () => {
    const original = {
      ...makeCodexFile('unknown-time-old.json', 'auth-1', 'workspace@example.com'),
      account_id: 'space-a',
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    const replacement = {
      ...makeCodexFile('unknown-time-new.json', 'auth-2', 'workspace@example.com'),
      account_id: 'space-a',
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      last_refresh: 3_000,
      modified: 3_100,
    } as AuthFileItem;
    const quota = {
      status: 'error' as const,
      windows: [
        makeCodexQuotaWindow({
          id: 'weekly',
          usedPercent: 100,
          observedAtMs: 1_000,
          limitWindowSeconds: 604_800,
        }),
      ],
      quotaInventoryObserved: true,
      error: 'HTTP 401 token expired',
      errorStatus: 401,
      ...buildQuotaCredentialIdentity(original),
    } satisfies CodexQuotaState;
    mocks.files = [original];
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(original, quota);
    installCodexQuotaStoreMutationMock();
    const replacementStoreKey = getQuotaCredentialStoreKey(replacement);
    const originalSelectionKey = getAuthFileSelectionKey(original);
    const replacementSelectionKey = getAuthFileSelectionKey(replacement);
    const renderer = await renderAccountsPage();

    await act(async () => {
      findAccountCardButtonByAriaLabel(
        renderer,
        originalSelectionKey,
        'accounts.recommend_action_reauth'
      ).props.onClick();
    });
    mocks.loadFiles.mockImplementationOnce(async () => {
      mocks.files = [replacement];
      return mocks.files;
    });

    expect(await runCodexReauthSuccessAndCaptureError()).toBeUndefined();
    await flushPromises();

    expect(mocks.quotaState.codexQuota).toMatchObject({
      [replacementStoreKey]: {
        authFileKey: replacementStoreKey,
        status: 'success',
        error: undefined,
        errorStatus: undefined,
        windows: [expect.objectContaining({ id: 'weekly', usedPercent: 100 })],
      },
    });
    expect(getAccountCardText(renderer, replacementSelectionKey)).not.toContain(
      'accounts.health_reauth'
    );
  });

  it('migrates only the reauthenticated quota in a shared physical file', async () => {
    const first = {
      ...makeCodexFile('shared.json', 'auth-1', 'first@example.com'),
      account_id: 'space-first',
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    const replacementFirst = {
      ...makeCodexFile('shared.json', 'auth-3', 'first@example.com'),
      account_id: 'space-first',
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      last_refresh: 3_000,
      modified: 3_100,
    } as AuthFileItem;
    const second = {
      ...makeCodexFile('shared.json', 'auth-2', 'second@example.com'),
      account_id: 'space-second',
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    const firstQuota = {
      status: 'success' as const,
      windows: [makeCodexQuotaWindow({ id: 'weekly', usedPercent: 100, observedAtMs: 1_000 })],
      quotaInventoryObserved: true,
      fetchedAtMs: 1_000,
      ...buildQuotaCredentialIdentity(first),
    } satisfies CodexQuotaState;
    const secondQuota = {
      status: 'success' as const,
      windows: [makeCodexQuotaWindow({ id: 'weekly', usedPercent: 30, observedAtMs: 1_000 })],
      quotaInventoryObserved: true,
      fetchedAtMs: 1_000,
      ...buildQuotaCredentialIdentity(second),
    } satisfies CodexQuotaState;
    const replacementStoreKey = getQuotaCredentialStoreKey(replacementFirst);
    const replacementAuthFailure = {
      status: 'error' as const,
      windows: [],
      error: 'HTTP 401 token expired',
      errorStatus: 401,
      failedAtMs: 1_500,
      ...buildQuotaCredentialIdentity(replacementFirst),
    } satisfies CodexQuotaState;
    mocks.files = [first, second];
    mocks.quotaState.codexQuota = {
      ...buildCredentialScopedQuotaRecord(first, firstQuota),
      ...buildCredentialScopedQuotaRecord(second, secondQuota),
      [replacementStoreKey]: replacementAuthFailure,
    };
    installCodexQuotaStoreMutationMock();
    const firstStoreKey = getQuotaCredentialStoreKey(first);
    const secondStoreKey = getQuotaCredentialStoreKey(second);
    const firstSelectionKey = getAuthFileSelectionKey(first);
    const replacementSelectionKey = getAuthFileSelectionKey(replacementFirst);
    const secondSelectionKey = getAuthFileSelectionKey(second);
    const renderer = await renderAccountsPage();

    await act(async () => {
      findAccountCardButtonByAriaLabel(
        renderer,
        firstSelectionKey,
        'accounts.recommend_action_reauth'
      ).props.onClick();
    });
    mocks.loadFiles.mockImplementationOnce(async () => {
      mocks.files = [replacementFirst, second];
      return mocks.files;
    });

    expect(await runCodexReauthSuccessAndCaptureError()).toBeUndefined();
    await flushPromises();

    expect(mocks.quotaState.codexQuota).not.toHaveProperty(firstStoreKey);
    expect(mocks.quotaState.codexQuota).toMatchObject({
      [replacementStoreKey]: expect.objectContaining({
        authFileKey: replacementStoreKey,
        windows: [expect.objectContaining({ usedPercent: 100 })],
      }),
      [secondStoreKey]: secondQuota,
    });
    expect(getAccountCardText(renderer, replacementSelectionKey)).not.toContain(
      'accounts.health_reauth'
    );
    expect(getAccountCardText(renderer, secondSelectionKey)).toContain('accounts.health_reauth');
  });

  it('keeps stale raw authentication evidence isolated from a shared-file sibling', async () => {
    const first = {
      ...makeCodexFile('shared.codex.json', 'auth-1', 'first@example.com'),
      account_id: 'space-first',
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    const second = {
      ...makeCodexFile('shared.codex.json', 'auth-2', 'second@example.com'),
      account_id: 'space-second',
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    const refreshedFirst = {
      ...first,
      last_refresh: 3_000,
      modified: 3_100,
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
    } as AuthFileItem;
    mocks.files = [first, second];
    const firstSelectionKey = getAuthFileSelectionKey(first);
    const secondSelectionKey = getAuthFileSelectionKey(second);
    const renderer = await renderAccountsPage();

    await act(async () => {
      findAccountCardButtonByAriaLabel(
        renderer,
        firstSelectionKey,
        'accounts.recommend_action_reauth'
      ).props.onClick();
    });
    mocks.loadFiles.mockImplementationOnce(async () => {
      mocks.files = [refreshedFirst, second];
      return mocks.files;
    });

    expect(await runCodexReauthSuccessAndCaptureError()).toBeUndefined();
    await flushPromises();

    expect(getAccountCardText(renderer, firstSelectionKey)).not.toContain('accounts.health_reauth');
    expect(getAccountCardText(renderer, secondSelectionKey)).toContain('accounts.health_reauth');
  });

  it('does not leak a shared-file recovery boundary when the reauthenticated sibling is removed', async () => {
    const first = {
      ...makeCodexFile('shared.codex.json', 'auth-1', 'first@example.com'),
      account_id: 'space-first',
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    const second = {
      ...makeCodexFile('shared.codex.json', 'auth-2', 'second@example.com'),
      account_id: 'space-second',
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    const refreshedFirst = {
      ...first,
      last_refresh: 3_000,
      modified: 3_100,
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
    } as AuthFileItem;
    mocks.files = [first, second];
    const firstSelectionKey = getAuthFileSelectionKey(first);
    const secondSelectionKey = getAuthFileSelectionKey(second);
    const renderer = await renderAccountsPage();

    await act(async () => {
      findAccountCardButtonByAriaLabel(
        renderer,
        firstSelectionKey,
        'accounts.recommend_action_reauth'
      ).props.onClick();
    });
    mocks.loadFiles.mockImplementationOnce(async () => {
      mocks.files = [refreshedFirst, second];
      return mocks.files;
    });

    expect(await runCodexReauthSuccessAndCaptureError()).toBeUndefined();
    await flushPromises();
    expect(getAccountCardText(renderer, firstSelectionKey)).not.toContain('accounts.health_reauth');
    expect(getAccountCardText(renderer, secondSelectionKey)).toContain('accounts.health_reauth');

    mocks.files = [second];
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await flushPromises();

    expect(getAccountCardText(renderer, secondSelectionKey)).toContain('accounts.health_reauth');
  });

  it('clears unique-file recovery before a shared filename can become unique again', async () => {
    const first = {
      ...makeCodexFile('shared.codex.json', 'auth-1', 'first@example.com'),
      account_id: 'space-first',
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    const refreshedFirst = {
      ...first,
      last_refresh: 3_000,
      modified: 3_100,
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
    } as AuthFileItem;
    const second = {
      ...makeCodexFile('shared.codex.json', 'auth-2', 'second@example.com'),
      account_id: 'space-second',
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    mocks.files = [first];
    const firstSelectionKey = getAuthFileSelectionKey(first);
    const secondSelectionKey = getAuthFileSelectionKey(second);
    const renderer = await renderAccountsPage();

    await act(async () => {
      findAccountCardButtonByAriaLabel(
        renderer,
        firstSelectionKey,
        'accounts.recommend_action_reauth'
      ).props.onClick();
    });
    mocks.loadFiles.mockImplementationOnce(async () => {
      mocks.files = [refreshedFirst];
      return mocks.files;
    });

    expect(await runCodexReauthSuccessAndCaptureError()).toBeUndefined();
    await flushPromises();
    expect(getAccountCardText(renderer, firstSelectionKey)).not.toContain('accounts.health_reauth');

    mocks.files = [refreshedFirst, second];
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await flushPromises();
    expect(getAccountCardText(renderer, secondSelectionKey)).toContain('accounts.health_reauth');

    mocks.files = [second];
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await flushPromises();

    expect(getAccountCardText(renderer, secondSelectionKey)).toContain('accounts.health_reauth');
  });

  it('retires old request-history 401 evidence and recognizes a newer 401 after re-login', async () => {
    const file = {
      ...makeCodexFile('history.codex.json', 'auth-1', 'history@example.com'),
      account_id: 'space-history',
    } as AuthFileItem;
    const refreshedFile = {
      ...file,
      last_refresh: Date.now() + 1,
      modified: Date.now() + 2,
    } as AuthFileItem;
    const selectionKey = getAuthFileSelectionKey(file);
    const makeHistoryItem = (timestampMs: number) => ({
      row_key: selectionKey,
      account_key: 'history@example.com',
      matched: true,
      total_requests: 1,
      success_calls: 0,
      failure_calls: 1,
      total_tokens: 0,
      total_cost: 0,
      success_rate: 0,
      first_seen_ms: timestampMs,
      last_seen_ms: timestampMs,
      latest_request: {
        timestamp_ms: timestampMs,
        failed: true,
        fail_status_code: 401,
      },
      recent_requests: [
        {
          timestamp_ms: timestampMs,
          failed: true,
          fail_status_code: 401,
        },
      ],
      sync_status: 'ready',
    });
    mocks.files = [file];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getAccountHistory.mockResolvedValue(makeAccountHistoryResponse([makeHistoryItem(1_000)]));
    const renderer = await renderAccountsPage();
    await flushPromises();

    await act(async () => {
      findAccountCardButtonByAriaLabel(
        renderer,
        selectionKey,
        'accounts.recommend_action_reauth'
      ).props.onClick();
    });
    mocks.loadFiles.mockImplementationOnce(async () => {
      mocks.files = [refreshedFile];
      return mocks.files;
    });

    expect(await runCodexReauthSuccessAndCaptureError()).toBeUndefined();
    await flushPromises();
    expect(getAccountCardText(renderer, selectionKey)).not.toContain('accounts.health_reauth');

    const newerRequestAtMs = Date.now() + 1_000;
    mocks.getAccountHistory.mockResolvedValue(
      makeAccountHistoryResponse([makeHistoryItem(newerRequestAtMs)])
    );
    await act(async () => {
      findButtonByText(renderer, 'common.refresh').props.onClick();
    });
    await flushPromises();

    expect(getAccountCardText(renderer, selectionKey)).toContain('accounts.health_reauth');
  });

  it('does not confirm a direct re-login when OAuth returns another account', async () => {
    const file = {
      ...makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
      status: 'error',
      statusMessage: 'token_expired',
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    const wrongAccount = {
      ...makeCodexFile(file.name, 'auth-2', 'other@example.com'),
      status: 'ready',
      statusMessage: '',
      last_refresh: 3_000,
      modified: 3_100,
    } as AuthFileItem;
    mocks.files = [file];
    const selectionKey = getAuthFileSelectionKey(file);
    const renderer = await renderAccountsPage();

    await act(async () => {
      findAccountCardButtonByAriaLabel(
        renderer,
        selectionKey,
        'accounts.recommend_action_reauth'
      ).props.onClick();
    });
    mocks.loadFiles.mockImplementationOnce(async () => {
      mocks.files = [wrongAccount];
      return mocks.files;
    });

    expect(await runCodexReauthSuccessAndCaptureError()).toMatchObject({
      name: 'CodexReauthReconciliationError',
      code: 'identity_unconfirmed',
      message: 'codex_reauth.identity_unconfirmed',
    });

    expect(listPendingAccountDirectReauths('http://cpa-a.local:8317:manager-key')).toHaveLength(1);
  });

  it('retries a pending direct re-login after Accounts remounts', async () => {
    const file = {
      ...makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
      account_id: 'space-codex',
      status: 'error',
      statusMessage: 'token_expired',
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    mocks.files = [file];
    const selectionKey = getAuthFileSelectionKey(file);
    const firstRenderer = await renderAccountsPage();

    await act(async () => {
      findAccountCardButtonByAriaLabel(
        firstRenderer,
        selectionKey,
        'accounts.recommend_action_reauth'
      ).props.onClick();
    });
    mocks.loadFiles.mockRejectedValueOnce(new Error('temporary auth-file list failure'));
    expect(await runCodexReauthSuccessAndCaptureError()).toEqual(
      new Error('temporary auth-file list failure')
    );
    expect(listPendingAccountDirectReauths('http://cpa-a.local:8317:manager-key')).toHaveLength(1);

    await act(async () => firstRenderer.unmount());
    mountedAccountsRenderers.delete(firstRenderer);
    mocks.files = [
      { ...file, status: 'ready', statusMessage: '', last_refresh: 3_000, modified: 3_100 },
    ] as AuthFileItem[];

    const secondRenderer = await renderAccountsPage();
    await flushPromises();

    expect(listPendingAccountDirectReauths('http://cpa-a.local:8317:manager-key')).toEqual([]);
    expect(getAccountCardText(secondRenderer, selectionKey)).not.toContain(
      'accounts.health_reauth'
    );
  });

  it('keeps pending direct re-login retries isolated by CPA connection', async () => {
    const file = {
      ...makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
      status: 'error',
      statusMessage: 'token_expired',
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    mocks.files = [file];
    const selectionKey = getAuthFileSelectionKey(file);
    const renderer = await renderAccountsPage();

    await act(async () => {
      findAccountCardButtonByAriaLabel(
        renderer,
        selectionKey,
        'accounts.recommend_action_reauth'
      ).props.onClick();
    });
    mocks.loadFiles.mockRejectedValueOnce(new Error('temporary auth-file list failure'));
    expect(await runCodexReauthSuccessAndCaptureError()).toEqual(
      new Error('temporary auth-file list failure')
    );

    mocks.apiBase = 'http://cpa-b.local:8317';
    mocks.managementKey = 'key-b';
    mocks.loadFiles.mockImplementation(async () => [
      { ...file, status: 'ready', statusMessage: '', last_refresh: 3_000, modified: 3_100 },
    ]);
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await flushPromises();

    expect(listPendingAccountDirectReauths('http://cpa-a.local:8317:manager-key')).toHaveLength(1);
    expect(listPendingAccountDirectReauths('http://cpa-b.local:8317:key-b')).toEqual([]);
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

  it('reloads account history when only the CPA connection fingerprint changes', async () => {
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    const renderer = await renderAccountsPage();
    await flushPromises();

    expect(mocks.getAccountHistory).toHaveBeenCalledTimes(1);

    mocks.apiBase = 'http://cpa-b.local:8317';
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await flushPromises();

    expect(mocks.getAccountHistory).toHaveBeenCalledTimes(2);

    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await flushPromises();

    expect(mocks.getAccountHistory).toHaveBeenCalledTimes(2);
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
    expect(mocks.listCodexInspectionRuns).toHaveBeenCalledWith(
      'http://manager.local:18317',
      'manager-key',
      10
    );
    expect(mocks.getCodexInspectionRun).not.toHaveBeenCalled();
    expect(mocks.loadExcluded).not.toHaveBeenCalled();
    expect(mocks.loadModelAlias).not.toHaveBeenCalled();
    expect(mocks.getAnalytics).not.toHaveBeenCalled();
    expect(mocks.getAccountWindowUsage).not.toHaveBeenCalled();
    expect(getAccountListItemTexts(renderer)[0]).toContain('accounts.quota_details_only');
    expect(getAccountListItemTexts(renderer)[0]).not.toContain('accounts.quota_source_none');
    expect(getAccountListItemTexts(renderer)[0]).not.toContain('20%');
    expect(mocks.quotaState.setCodexQuota).not.toHaveBeenCalled();
  });

  it('filters xAI quota limits using only unexpired provider usage evidence', async () => {
    const generatedAtMs = 3_000;
    const activeFile = {
      name: 'xai-active.json',
      type: 'xai',
      provider: 'xai',
      authIndex: 'xai-active',
      account: 'active@example.com',
      disabled: false,
    } as AuthFileItem;
    const expiredFile = {
      name: 'xai-expired.json',
      type: 'xai',
      provider: 'xai',
      authIndex: 'xai-expired',
      account: 'expired@example.com',
      disabled: false,
    } as AuthFileItem;
    mocks.files = [activeFile, expiredFile];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: generatedAtMs,
      from_ms: 1_000,
      to_ms: generatedAtMs,
      items: [
        {
          event_hash: 'xai-active-provider-usage',
          timestamp_ms: 2_000,
          auth_file_snapshot: activeFile.name,
          auth_index: 'xai-active',
          account_snapshot: 'active@example.com',
          auth_provider_snapshot: 'xai',
          response_metadata: {
            provider_usage: {
              provider: 'xai',
              state: 'exhausted',
              actual: 100,
              limit: 100,
              remaining: 0,
              recover_at_ms: generatedAtMs + 1_000,
            },
          },
        },
        {
          event_hash: 'xai-expired-provider-usage',
          timestamp_ms: 1_500,
          auth_file_snapshot: expiredFile.name,
          auth_index: 'xai-expired',
          account_snapshot: 'expired@example.com',
          auth_provider_snapshot: 'xai',
          response_metadata: {
            provider_usage: {
              provider: 'xai',
              state: 'exhausted',
              actual: 100,
              limit: 100,
              remaining: 0,
              recover_at_ms: generatedAtMs - 1,
            },
          },
        },
      ],
    });

    const renderer = await renderAccountsPage();
    await flushPromises();

    const statusSelect = renderer.root
      .findAllByType(Select)
      .find((node) => node.props.ariaLabel === 'accounts.status_filter');
    if (!statusSelect) throw new Error('Accounts status filter not found');
    await act(async () => {
      statusSelect.props.onChange('quota_limited');
      await Promise.resolve();
    });

    const visibleRows = getAccountListItemTexts(renderer);
    expect(visibleRows).toHaveLength(1);
    expect(visibleRows[0]).toContain(activeFile.name);
    expect(visibleRows[0]).not.toContain(expiredFile.name);
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
    expect(mocks.getAccountHistory).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(2);
    expect(mocks.getAccountHistory).toHaveBeenCalledTimes(2);

    visibilityState = 'hidden';
    await act(async () => {
      documentEvents.dispatchEvent(new Event('visibilitychange'));
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(2);
    expect(mocks.getAccountHistory).toHaveBeenCalledTimes(2);

    visibilityState = 'visible';
    await act(async () => {
      documentEvents.dispatchEvent(new Event('visibilitychange'));
      await Promise.resolve();
    });
    expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(3);
    expect(mocks.getAccountHistory).toHaveBeenCalledTimes(3);
  });

  it('does not restart an in-flight full history load on passive refresh', async () => {
    vi.useFakeTimers();
    mocks.files = Array.from({ length: 401 }, (_, index) =>
      makeCodexFile(
        `credential-${String(index + 1).padStart(3, '0')}.json`,
        `auth-${index + 1}`,
        `account-${index + 1}@example.com`
      )
    );
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    const firstBatches = [
      createDeferred<AccountHistoryResponseForTest>(),
      createDeferred<AccountHistoryResponseForTest>(),
    ];
    let requestIndex = 0;
    mocks.getAccountHistory.mockImplementation(() => {
      const currentIndex = requestIndex;
      requestIndex += 1;
      return currentIndex < firstBatches.length
        ? firstBatches[currentIndex].promise
        : Promise.resolve(makeAccountHistoryResponse([]));
    });

    await renderAccountsPage();
    await flushPromises();

    expect(mocks.getAccountHistory).toHaveBeenCalledTimes(2);
    const initialSignals = mocks.getAccountHistory.mock.calls.map((call) => call[3] as AbortSignal);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });

    expect(mocks.getAccountHistory).toHaveBeenCalledTimes(2);
    expect(initialSignals.every((signal) => signal.aborted === false)).toBe(true);

    await act(async () => {
      firstBatches.forEach((batch) => batch.resolve(makeAccountHistoryResponse([])));
      await Promise.resolve();
      await Promise.resolve();
    });
    await flushPromises();
    expect(mocks.getAccountHistory).toHaveBeenCalledTimes(3);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    await flushPromises();
    expect(mocks.getAccountHistory).toHaveBeenCalledTimes(6);
  });

  it('keeps successful history batches when another batch fails', async () => {
    mocks.files = Array.from({ length: 201 }, (_, index) =>
      makeCodexFile(
        `credential-${String(index + 1).padStart(3, '0')}.json`,
        `auth-${index + 1}`,
        `account-${index + 1}@example.com`
      )
    );
    const problemFile = mocks.files[200];
    const problemRowKey = getAuthFileSelectionKey(problemFile);
    mocks.location = { pathname: '/accounts', search: '?status=problem' };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getAccountHistory.mockImplementation(
      async (_base, _managementKey, request: AccountHistoryRequestForTest) => {
        const targets = request.accounts as Array<{ row_key?: string }>;
        if (!targets.some((target) => target.row_key === problemRowKey)) {
          throw new Error('first batch unavailable');
        }
        return makeAccountHistoryResponse([
          {
            row_key: problemRowKey,
            account_key: 'problem-account',
            matched: true,
            total_requests: 1,
            success_calls: 0,
            failure_calls: 1,
            total_tokens: 0,
            total_cost: 0,
            success_rate: 0,
            first_seen_ms: 2_000,
            last_seen_ms: 2_000,
            latest_request: {
              timestamp_ms: 2_000,
              failed: true,
              fail_status_code: 401,
            },
            recent_requests: [
              {
                timestamp_ms: 2_000,
                failed: true,
                fail_status_code: 401,
              },
            ],
            sync_status: 'ready',
          },
        ]);
      }
    );

    const renderer = await renderAccountsPage();
    await flushPromises();

    expect(mocks.getAccountHistory).toHaveBeenCalledTimes(2);
    expect(
      renderer.root.findAllByProps({
        'data-account-card': problemRowKey,
      })
    ).toHaveLength(1);
    expect(getAccountListItemTexts(renderer).join('\n')).toContain('accounts.health_reauth');
  });

  it('retains mixed quota Header evidence without leaving stale reauth filters', async () => {
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: 2_000,
      from_ms: 0,
      to_ms: 2_000,
      items: [
        {
          event_hash: 'mixed-auth-quota-header',
          timestamp_ms: 1_000,
          model: CODEX_MAIN_MODEL,
          auth_file_snapshot: 'codex.json',
          auth_index: 'auth-1',
          account_snapshot: 'codex@example.com',
          auth_provider_snapshot: 'codex',
          header_error_kind: 'auth',
          header_error_code: 'invalid_api_key',
          header_trace_id: 'trace-mixed-auth-quota',
          header_quota_used_percent: 100,
          response_metadata: {
            quota: {
              rate_limit_reached_type: 'secondary',
              recover_at_ms: 9_000,
            },
            errors: {
              kind: 'auth',
              code: 'invalid_token',
            },
          },
        },
      ],
    });
    mocks.getAccountHistory.mockResolvedValue(
      makeAccountHistoryResponse([
        {
          row_key: 'codex.json\u0000auth-1',
          account_key: 'codex@example.com',
          matched: true,
          total_requests: 1,
          success_calls: 1,
          failure_calls: 0,
          total_tokens: 10,
          total_cost: 0.01,
          success_rate: 1,
          first_seen_ms: 2_000,
          last_seen_ms: 2_000,
          latest_request: { timestamp_ms: 2_000, failed: false },
          recent_requests: [{ timestamp_ms: 2_000, failed: false }],
          sync_status: 'ready',
        },
      ])
    );

    const renderer = await renderAccountsPage();
    await flushPromises();

    expect(getAccountListItemTexts(renderer).join('\n')).toContain('accounts.health_limited');

    const searchInput = findInputByAriaLabel(renderer, 'accounts.search_label');
    await act(async () => {
      searchInput.props.onChange({ target: { value: 'invalid_api_key' } });
    });
    expect(
      renderer.root.findAll(
        (node) => node.type === 'article' && typeof node.props['data-account-card'] === 'string'
      )
    ).toHaveLength(0);

    await act(async () => {
      searchInput.props.onChange({ target: { value: 'trace-mixed-auth-quota' } });
    });
    expect(
      renderer.root.findAllByProps({ 'data-account-card': 'codex.json\u0000auth-1' })
    ).toHaveLength(1);

    await act(async () => {
      searchInput.props.onChange({ target: { value: '' } });
    });

    const operationalSelect = renderer.root
      .findAllByType(Select)
      .find((node) => node.props.ariaLabel === 'accounts.operational_filter');
    if (!operationalSelect) throw new Error('Accounts operational filter not found');
    await act(async () => {
      operationalSelect.props.onChange('reauth');
      await Promise.resolve();
    });

    expect(
      renderer.root.findAll(
        (node) => node.type === 'article' && typeof node.props['data-account-card'] === 'string'
      )
    ).toHaveLength(0);
  });

  it('clears an older quota-refresh 401 after a newer successful request', async () => {
    const file = mocks.files[0];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'error',
      windows: [],
      fetchedAtMs: 1_000,
      failedAtMs: 1_000,
      error: 'HTTP 401 unauthorized',
      errorStatus: 401,
    });
    mocks.getAccountHistory.mockResolvedValue(
      makeAccountHistoryResponse([
        {
          row_key: 'codex.json\u0000auth-1',
          account_key: 'codex@example.com',
          matched: true,
          total_requests: 1,
          success_calls: 1,
          failure_calls: 0,
          total_tokens: 10,
          total_cost: 0.01,
          success_rate: 1,
          first_seen_ms: 2_000,
          last_seen_ms: 2_000,
          latest_request: { timestamp_ms: 2_000, failed: false },
          recent_requests: [{ timestamp_ms: 2_000, failed: false }],
          sync_status: 'ready',
        },
      ])
    );

    const renderer = await renderAccountsPage();
    await flushPromises();

    const accountCard = renderer.root.findByProps({
      'data-account-card': 'codex.json\u0000auth-1',
    });
    expect(readText(accountCard)).toContain('accounts.health_available');
    expect(readText(accountCard)).not.toContain('accounts.health_reauth');
    expect(readText(accountCard)).not.toContain('HTTP 401 unauthorized');

    const operationalSelect = renderer.root
      .findAllByType(Select)
      .find((node) => node.props.ariaLabel === 'accounts.operational_filter');
    if (!operationalSelect) throw new Error('Accounts operational filter not found');
    await act(async () => {
      operationalSelect.props.onChange('reauth');
      await Promise.resolve();
    });
    expect(
      renderer.root.findAll(
        (node) => node.type === 'article' && typeof node.props['data-account-card'] === 'string'
      )
    ).toHaveLength(0);
  });

  it('does not classify a disabled account with current authentication failure as recovered', async () => {
    const authenticationFailure = {
      ...makeCodexFile('auth-failure.json', 'auth-failure', 'auth-failure@example.com'),
      disabled: true,
      status_code: 401,
      status_message: 'unauthorized',
      updated_at_ms: 2_000,
    } as AuthFileItem;
    const recovered = {
      ...makeCodexFile('recovered.json', 'recovered', 'recovered@example.com'),
      disabled: true,
    } as AuthFileItem;
    mocks.files = [authenticationFailure, recovered];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getAccountHistory.mockResolvedValue(
      makeAccountHistoryResponse([
        {
          row_key: getAuthFileSelectionKey(recovered),
          account_key: 'recovered@example.com',
          matched: true,
          total_requests: 1,
          success_calls: 1,
          failure_calls: 0,
          total_tokens: 10,
          total_cost: 0.01,
          success_rate: 1,
          first_seen_ms: 3_000,
          last_seen_ms: 3_000,
          latest_request: { timestamp_ms: 3_000, failed: false },
          recent_requests: [{ timestamp_ms: 3_000, failed: false }],
          sync_status: 'ready',
        },
      ])
    );

    const renderer = await renderAccountsPage();
    await flushPromises();
    const operationalSelect = renderer.root
      .findAllByType(Select)
      .find((node) => node.props.ariaLabel === 'accounts.operational_filter');
    if (!operationalSelect) throw new Error('Accounts operational filter not found');

    await act(async () => {
      operationalSelect.props.onChange('recovered');
      await Promise.resolve();
    });

    expect(
      renderer.root.findAllByProps({
        'data-account-card': getAuthFileSelectionKey(authenticationFailure),
      })
    ).toHaveLength(0);
    expect(
      renderer.root.findAllByProps({
        'data-account-card': getAuthFileSelectionKey(recovered),
      })
    ).toHaveLength(1);
  });

  it('keeps a superseded raw 401 out of both reauth filters', async () => {
    const credentialUpdatedAtMs = 1_700_000_000_000;
    const successfulRequestAtMs = credentialUpdatedAtMs + 1_000;
    mocks.files = [
      {
        ...makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
        status_code: 401,
        status_message: 'unauthorized',
        updated_at_ms: credentialUpdatedAtMs,
      } as AuthFileItem,
    ];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getAccountHistory.mockResolvedValue(
      makeAccountHistoryResponse([
        {
          row_key: 'codex.json\u0000auth-1',
          account_key: 'codex@example.com',
          matched: true,
          total_requests: 1,
          success_calls: 1,
          failure_calls: 0,
          total_tokens: 10,
          total_cost: 0.01,
          success_rate: 1,
          first_seen_ms: successfulRequestAtMs,
          last_seen_ms: successfulRequestAtMs,
          latest_request: { timestamp_ms: successfulRequestAtMs, failed: false },
          recent_requests: [{ timestamp_ms: successfulRequestAtMs, failed: false }],
          sync_status: 'ready',
        },
      ])
    );

    const renderer = await renderAccountsPage();
    await flushPromises();

    const accountCard = renderer.root.findByProps({
      'data-account-card': 'codex.json\u0000auth-1',
    });
    expect(readText(accountCard)).toContain('accounts.health_available');
    expect(readText(accountCard)).not.toContain('accounts.health_reauth');

    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
      await Promise.resolve();
    });
    await flushPromises();
    expect(
      renderer.root.findAllByProps({ 'data-overview-recent-status-message': 'true' })
    ).toHaveLength(0);

    const statusSelect = renderer.root
      .findAllByType(Select)
      .find((node) => node.props.ariaLabel === 'accounts.status_filter');
    const operationalSelect = renderer.root
      .findAllByType(Select)
      .find((node) => node.props.ariaLabel === 'accounts.operational_filter');
    if (!statusSelect || !operationalSelect) throw new Error('Accounts reauth filters not found');

    await act(async () => {
      statusSelect.props.onChange('reauth');
      await Promise.resolve();
    });
    expect(
      renderer.root.findAll(
        (node) => node.type === 'article' && typeof node.props['data-account-card'] === 'string'
      )
    ).toHaveLength(0);

    await act(async () => {
      statusSelect.props.onChange('all');
      operationalSelect.props.onChange('reauth');
      await Promise.resolve();
    });
    expect(
      renderer.root.findAll(
        (node) => node.type === 'article' && typeof node.props['data-account-card'] === 'string'
      )
    ).toHaveLength(0);
  });

  it('keeps quota Header evidence when a newer request clears a same-batch quota-refresh 401', async () => {
    const file = mocks.files[0];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'error',
      windows: [],
      fetchedAtMs: 1_000,
      failedAtMs: 1_000,
      error: 'HTTP 401 unauthorized',
      errorStatus: 401,
    });
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: 2_000,
      from_ms: 0,
      to_ms: 2_000,
      items: [
        {
          event_hash: 'quota-refresh-401-header',
          timestamp_ms: 1_000,
          model: CODEX_MAIN_MODEL,
          auth_file_snapshot: 'codex.json',
          auth_index: 'auth-1',
          account_snapshot: 'codex@example.com',
          auth_provider_snapshot: 'codex',
          header_error_kind: 'auth',
          header_error_code: 'invalid_api_key',
          header_quota_used_percent: 100,
          header_quota_recover_at_ms: 9_000,
        },
      ],
    });
    mocks.getAccountHistory.mockResolvedValue(
      makeAccountHistoryResponse([
        {
          row_key: 'codex.json\u0000auth-1',
          account_key: 'codex@example.com',
          matched: true,
          total_requests: 1,
          success_calls: 1,
          failure_calls: 0,
          total_tokens: 10,
          total_cost: 0.01,
          success_rate: 1,
          first_seen_ms: 2_000,
          last_seen_ms: 2_000,
          latest_request: { timestamp_ms: 2_000, failed: false },
          recent_requests: [{ timestamp_ms: 2_000, failed: false }],
          sync_status: 'ready',
        },
      ])
    );

    const renderer = await renderAccountsPage();
    await flushPromises();

    const accountCard = renderer.root.findByProps({
      'data-account-card': 'codex.json\u0000auth-1',
    });
    expect(readText(accountCard)).toContain('accounts.health_limited');
    expect(readText(accountCard)).not.toContain('accounts.health_reauth');

    const searchInput = findInputByAriaLabel(renderer, 'accounts.search_label');
    await act(async () => {
      searchInput.props.onChange({ target: { value: 'invalid_api_key' } });
    });
    expect(
      renderer.root.findAll(
        (node) => node.type === 'article' && typeof node.props['data-account-card'] === 'string'
      )
    ).toHaveLength(0);
  });

  it('lets newer healthy Header quota replace an older quota-refresh 401', async () => {
    const file = mocks.files[0];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'error',
      windows: [],
      fetchedAtMs: 1_000,
      failedAtMs: 1_000,
      error: 'HTTP 401 unauthorized',
      errorStatus: 401,
    });
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: 2_000,
      from_ms: 0,
      to_ms: 2_000,
      items: [
        {
          event_hash: 'newer-healthy-quota-header',
          timestamp_ms: 2_000,
          model: CODEX_MAIN_MODEL,
          auth_file_snapshot: 'codex.json',
          auth_index: 'auth-1',
          account_snapshot: 'codex@example.com',
          auth_provider_snapshot: 'codex',
          header_quota_used_percent: 20,
          header_quota_plan_type: 'plus',
        },
      ],
    });

    const renderer = await renderAccountsPage();
    await flushPromises();

    const accountCard = renderer.root.findByProps({
      'data-account-card': 'codex.json\u0000auth-1',
    });
    expect(readText(accountCard)).toContain('accounts.health_available');
    expect(readText(accountCard)).not.toContain('accounts.health_reauth');

    const operationalSelect = renderer.root
      .findAllByType(Select)
      .find((node) => node.props.ariaLabel === 'accounts.operational_filter');
    if (!operationalSelect) throw new Error('Accounts operational filter not found');
    await act(async () => {
      operationalSelect.props.onChange('reauth');
      await Promise.resolve();
    });
    expect(
      renderer.root.findAll(
        (node) => node.type === 'article' && typeof node.props['data-account-card'] === 'string'
      )
    ).toHaveLength(0);
  });

  it('does not let an older Header diagnostic override a newer complete quota refresh', async () => {
    const file = mocks.files[0];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      quotaInventoryObserved: true,
      fetchedAtMs: 2_000,
      windows: [
        {
          id: 'weekly',
          label: 'Weekly',
          usedPercent: 20,
          resetLabel: 'Mon',
          modelScope: CODEX_MAIN_SCOPE,
        },
      ],
    });
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: 2_000,
      from_ms: 0,
      to_ms: 2_000,
      items: [
        {
          event_hash: 'older-stale-diagnostic',
          timestamp_ms: 1_000,
          auth_file_snapshot: 'codex.json',
          auth_index: 'auth-1',
          account_snapshot: 'codex@example.com',
          auth_provider_snapshot: 'codex',
          header_error_kind: 'auth',
          header_error_code: 'invalid_api_key',
          header_trace_id: 'trace-older-diagnostic',
          header_quota_used_percent: 100,
        },
      ],
    });

    const renderer = await renderAccountsPage();
    await flushPromises();

    const accountCard = renderer.root.findByProps({
      'data-account-card': 'codex.json\u0000auth-1',
    });
    expect(readText(accountCard)).toContain('accounts.health_available');
    expect(readText(accountCard)).not.toContain('accounts.health_reauth');
    expect(readText(accountCard)).not.toContain('accounts.health_limited');

    const searchInput = findInputByAriaLabel(renderer, 'accounts.search_label');
    for (const value of ['invalid_api_key', 'trace-older-diagnostic']) {
      await act(async () => {
        searchInput.props.onChange({ target: { value } });
      });
      expect(
        renderer.root.findAll(
          (node) => node.type === 'article' && typeof node.props['data-account-card'] === 'string'
        )
      ).toHaveLength(0);
    }
  });

  it('keeps same-time Header quota but removes auth diagnostics after a partial Provider refresh', async () => {
    const file = mocks.files[0];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      quotaInventoryObserved: false,
      fetchedAtMs: 2_000,
      windows: [
        {
          id: 'weekly',
          label: 'Weekly',
          usedPercent: 20,
          resetLabel: 'Mon',
        },
      ],
    });
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: 2_000,
      from_ms: 0,
      to_ms: 2_000,
      items: [
        {
          event_hash: 'same-time-partial-header',
          timestamp_ms: 2_000,
          model: CODEX_MAIN_MODEL,
          auth_file_snapshot: 'codex.json',
          auth_index: 'auth-1',
          account_snapshot: 'codex@example.com',
          auth_provider_snapshot: 'codex',
          header_error_kind: 'auth',
          header_error_code: 'invalid_api_key',
          header_quota_used_percent: 100,
          header_quota_recover_at_ms: 9_000,
        },
      ],
    });

    const renderer = await renderAccountsPage();
    await flushPromises();

    const accountCard = renderer.root.findByProps({
      'data-account-card': 'codex.json\u0000auth-1',
    });
    expect(readText(accountCard)).toContain('accounts.health_limited');
    expect(readText(accountCard)).not.toContain('accounts.health_reauth');

    const searchInput = findInputByAriaLabel(renderer, 'accounts.search_label');
    await act(async () => {
      searchInput.props.onChange({ target: { value: 'invalid_api_key' } });
    });
    expect(
      renderer.root.findAll(
        (node) => node.type === 'article' && typeof node.props['data-account-card'] === 'string'
      )
    ).toHaveLength(0);
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
          model: CODEX_MAIN_MODEL,
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
          model: CODEX_MAIN_MODEL,
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
    expect(mocks.listCodexInspectionRuns).toHaveBeenCalledTimes(1);

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

  it('offers the unconfirmed status filter with the metric label', async () => {
    const renderer = await renderAccountsPage();
    await flushPromises();

    const statusSelect = renderer.root
      .findAllByType(Select)
      .find((node) => node.props.ariaLabel === 'accounts.status_filter');
    if (!statusSelect) throw new Error('Accounts status filter not found');

    expect(statusSelect.props.options).toEqual(
      expect.arrayContaining([{ value: 'unconfirmed', label: 'accounts.metric_unconfirmed' }])
    );
  });

  it('loads request evidence for every account before applying the problem filter', async () => {
    mocks.files = Array.from({ length: 201 }, (_, index) =>
      makeCodexFile(
        `credential-${String(index + 1).padStart(3, '0')}.json`,
        `auth-${index + 1}`,
        `account-${index + 1}@example.com`
      )
    );
    const problemFile = mocks.files[200];
    const problemRowKey = getAuthFileSelectionKey(problemFile);
    mocks.location = { pathname: '/accounts', search: '?status=problem' };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getAccountHistory.mockImplementation(
      async (_base, _managementKey, request: AccountHistoryRequestForTest) => {
        const targets = request.accounts as Array<{ row_key?: string }>;
        return makeAccountHistoryResponse(
          targets.some((target) => target.row_key === problemRowKey)
            ? [
                {
                  row_key: problemRowKey,
                  account_key: 'problem-account',
                  matched: true,
                  total_requests: 1,
                  success_calls: 0,
                  failure_calls: 1,
                  total_tokens: 0,
                  total_cost: 0,
                  success_rate: 0,
                  first_seen_ms: 2_000,
                  last_seen_ms: 2_000,
                  latest_request: {
                    timestamp_ms: 2_000,
                    failed: true,
                    fail_status_code: 401,
                  },
                  recent_requests: [
                    {
                      timestamp_ms: 2_000,
                      failed: true,
                      fail_status_code: 401,
                    },
                  ],
                  sync_status: 'ready',
                },
              ]
            : []
        );
      }
    );

    const renderer = await renderAccountsPage();
    await flushPromises();

    const historyRequests = mocks.getAccountHistory.mock.calls.map(
      (call) => call[2] as AccountHistoryRequestForTest
    );
    expect(historyRequests).toHaveLength(2);
    expect(historyRequests.every((request) => request.accounts.length <= 200)).toBe(true);
    expect(historyRequests.flatMap((request) => request.accounts)).toHaveLength(201);
    expect(
      renderer.root.findAllByProps({
        'data-account-card': problemRowKey,
      })
    ).toHaveLength(1);
    expect(getAccountListItemTexts(renderer).join('\n')).toContain('accounts.health_reauth');
  });

  it('limits full account-history loading to two concurrent batches', async () => {
    mocks.files = Array.from({ length: 601 }, (_, index) =>
      makeCodexFile(
        `credential-${String(index + 1).padStart(3, '0')}.json`,
        `auth-${index + 1}`,
        `account-${index + 1}@example.com`
      )
    );
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    const batchResponses = Array.from({ length: 4 }, () =>
      createDeferred<AccountHistoryResponseForTest>()
    );
    let nextBatchIndex = 0;
    let activeRequests = 0;
    let peakActiveRequests = 0;
    mocks.getAccountHistory.mockImplementation(() => {
      const batchIndex = nextBatchIndex;
      nextBatchIndex += 1;
      activeRequests += 1;
      peakActiveRequests = Math.max(peakActiveRequests, activeRequests);
      return batchResponses[batchIndex].promise.finally(() => {
        activeRequests -= 1;
      });
    });

    await renderAccountsPage();
    await flushPromises();

    expect(mocks.getAccountHistory).toHaveBeenCalledTimes(2);
    expect(peakActiveRequests).toBe(2);

    await act(async () => {
      batchResponses[0].resolve(makeAccountHistoryResponse([]));
      batchResponses[1].resolve(makeAccountHistoryResponse([]));
      await Promise.resolve();
      await Promise.resolve();
    });
    await flushPromises();

    expect(mocks.getAccountHistory).toHaveBeenCalledTimes(4);
    expect(peakActiveRequests).toBe(2);

    await act(async () => {
      batchResponses[2].resolve(makeAccountHistoryResponse([]));
      batchResponses[3].resolve(makeAccountHistoryResponse([]));
      await Promise.resolve();
      await Promise.resolve();
    });
    await flushPromises();

    const requests = mocks.getAccountHistory.mock.calls.map(
      (call) => call[2] as AccountHistoryRequestForTest
    );
    expect(requests.map((request) => request.accounts.length)).toEqual([200, 200, 200, 1]);
    expect(activeRequests).toBe(0);
    expect(peakActiveRequests).toBe(2);
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
      label: 'Unknown plan',
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

  it('keeps a stale selected plan option visible when its account row disappears', async () => {
    const claudeFile = {
      name: 'claude-pro.json',
      type: 'claude',
      provider: 'claude',
      account: 'claude@example.com',
      id_token: { planType: 'plan_pro' },
      disabled: false,
    } as AuthFileItem;
    const codexFile = {
      ...makeCodexFile('codex-pro.json', 'codex-auth', 'codex@example.com'),
      planType: 'pro',
    } as AuthFileItem;
    mocks.files = [claudeFile, codexFile];

    const renderer = await renderAccountsPage();
    const getPlanSelect = () => {
      const select = renderer.root
        .findAllByType(Select)
        .find((node) => node.props.ariaLabel === 'accounts.plan_filter');
      if (!select) throw new Error('Accounts plan filter not found');
      return select;
    };

    await act(async () => {
      getPlanSelect().props.onChange('pro');
      await Promise.resolve();
    });
    expect(getPlanSelect().props.value).toBe('pro');
    expect(getPlanSelect().props.options).toContainEqual({ value: 'pro', label: 'Pro' });
    expect(getAccountListItemTexts(renderer)).toHaveLength(1);
    expect(getAccountListItemTexts(renderer)[0]).toContain('claude-pro.json');

    mocks.files = [codexFile];
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });

    const updatedSelect = getPlanSelect();
    expect(updatedSelect.props.value).toBe('pro');
    expect(updatedSelect.props.options).toContainEqual({ value: 'pro', label: 'Pro' });
    expect(updatedSelect.props.options).not.toContainEqual({ value: 'pro', label: 'Pro 20x' });
    expect(
      renderer.root.findAll(
        (node) => node.type === 'article' && typeof node.props['data-account-card'] === 'string'
      )
    ).toHaveLength(0);
  });

  it('keeps a scoped unknown filter readable after its account row disappears', async () => {
    const antigravityFile = {
      name: 'antigravity-future.json',
      type: 'antigravity',
      provider: 'antigravity',
      authIndex: 'antigravity-auth',
      account: 'antigravity@example.com',
      planType: 'Antigravity Future',
      disabled: false,
    } as AuthFileItem;
    mocks.files = [antigravityFile];

    const renderer = await renderAccountsPage();
    const getPlanSelect = () => {
      const select = renderer.root
        .findAllByType(Select)
        .find((node) => node.props.ariaLabel === 'accounts.plan_filter');
      if (!select) throw new Error('Accounts plan filter not found');
      return select;
    };
    const canonical = 'unknown:antigravity:antigravity future';

    await act(async () => {
      getPlanSelect().props.onChange(canonical);
      await Promise.resolve();
    });
    expect(getPlanSelect().props.value).toBe(canonical);
    expect(getPlanSelect().props.options).toContainEqual({
      value: canonical,
      label: 'Antigravity Future',
    });

    mocks.files = [makeCodexFile('other.json', 'other-auth', 'other@example.com')];
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });

    const updatedSelect = getPlanSelect();
    expect(updatedSelect.props.value).toBe(canonical);
    const selectedOption = updatedSelect.props.options.find(
      (option: { value: string }) => option.value === canonical
    );
    expect(selectedOption?.label).toBe('antigravity future');
    expect(selectedOption?.label).not.toContain('unknown:antigravity:');
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

  it('opens the configuration tab from the row settings action', async () => {
    const rowKey = getAuthFileSelectionKey(mocks.files[0]);
    const renderer = await renderAccountsPage();

    await act(async () => {
      findAccountCardButtonByAriaLabel(
        renderer,
        rowKey,
        'accounts.detail_tab_config'
      ).props.onClick();
      await Promise.resolve();
    });

    expect(
      findHostButtonByText(renderer, 'accounts.detail_tab_config').props['aria-selected']
    ).toBe(true);
    expect(mocks.navigate).toHaveBeenCalledWith(
      {
        pathname: '/accounts',
        search: '?account=codex.json%00auth-1&tab=config',
      },
      { replace: true }
    );
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

  it('uses a newer successful request instead of a stale local inspection conclusion', async () => {
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
    mocks.getAccountHistory.mockResolvedValue(
      makeAccountHistoryResponse([
        {
          row_key: 'codex.json\u0000auth-1',
          account_key: 'codex@example.com',
          matched: true,
          total_requests: 1,
          success_calls: 1,
          failure_calls: 0,
          total_tokens: 10,
          total_cost: 0.01,
          success_rate: 1,
          first_seen_ms: 9_000,
          last_seen_ms: 9_000,
          latest_request: { timestamp_ms: 9_000, failed: false },
          recent_requests: [{ timestamp_ms: 9_000, failed: false }],
          sync_status: 'ready',
        },
      ])
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

    const accountCard = renderer.root.findByProps({
      'data-account-card': 'codex.json\u0000auth-1',
    });
    expect(readText(accountCard)).toContain('accounts.health_available');
    expect(readText(accountCard)).not.toContain('accounts.health_reauth');

    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_diagnostics').props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(
      renderer.root.findByProps({ 'data-diagnostic-evidence-status': 'current' })
    ).toBeDefined();
    expect(treeText(renderer)).toContain('accounts.recommend_normal');
    expect(treeText(renderer)).toContain('accounts.latest_request_time_title');
    expect(treeText(renderer)).not.toContain('accounts.detail_diagnostic_reinspect');
    expect(treeText(renderer)).toContain('expired token');
    expect(treeText(renderer)).toContain('accounts.action_reauth');
    expect(treeText(renderer)).toContain('accounts.inspection_source_local');
  });

  it('does not resurrect an already executed reauth inspection in account status', async () => {
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
            actionHandled: true,
          },
        ],
      },
    };

    const renderer = await renderAccountsPage();
    await flushPromises();

    const accountCard = renderer.root.findByProps({
      'data-account-card': 'codex.json\u0000auth-1',
    });
    expect(readText(accountCard)).not.toContain('accounts.health_reauth');

    const operationalSelect = renderer.root
      .findAllByType(Select)
      .find((node) => node.props.ariaLabel === 'accounts.operational_filter');
    if (!operationalSelect) throw new Error('Accounts operational filter not found');
    await act(async () => {
      operationalSelect.props.onChange('reauth');
      await Promise.resolve();
    });
    expect(
      renderer.root.findAll(
        (node) => node.type === 'article' && typeof node.props['data-account-card'] === 'string'
      )
    ).toHaveLength(0);
  });

  it('keeps an OAuth-completed inspection suppressed after Accounts remounts', async () => {
    const windowEvents = new EventTarget();
    const localStorageValues = new Map<string, string>();
    const sessionStorageValues = new Map<string, string>();
    const createStorage = (values: Map<string, string>) => ({
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => values.set(key, value),
      removeItem: (key: string) => values.delete(key),
      clear: () => values.clear(),
    });
    vi.stubGlobal('window', {
      location: { hash: '' },
      history: { state: null },
      localStorage: createStorage(localStorageValues),
      sessionStorage: createStorage(sessionStorageValues),
      addEventListener: windowEvents.addEventListener.bind(windowEvents),
      removeEventListener: windowEvents.removeEventListener.bind(windowEvents),
    });

    const xaiFile = {
      name: 'xai.json',
      type: 'xai',
      provider: 'xai',
      authIndex: 'xai-auth-1',
      account: 'xai@example.com',
      priority: 0,
      disabled: false,
    } as AuthFileItem;
    mocks.files = [xaiFile];
    mocks.localInspection = {
      savedAt: 300,
      logs: [],
      logsCollapsed: true,
      actionFilter: 'all',
      connectionFingerprint: 'http://cpa-a.local:8317:manager-key',
      result: {
        settings: {},
        files: [xaiFile],
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
            key: 'xai.json\u0000xai-auth-1',
            fileName: 'xai.json',
            displayAccount: 'xai@example.com',
            authIndex: null,
            accountId: null,
            provider: 'xai',
            disabled: false,
            autoRecoverOwned: false,
            status: 'error',
            state: 'error',
            raw: xaiFile,
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

    const firstRenderer = await renderAccountsPage();
    await flushPromises();
    const selectionKey = 'xai.json\u0000xai-auth-1';
    expect(readText(findAccountCardByKey(firstRenderer, selectionKey))).toContain(
      'accounts.health_reauth'
    );

    await act(async () => {
      findAccountCardButtonByAriaLabel(
        firstRenderer,
        selectionKey,
        'accounts.recommend_action_reauth'
      ).props.onClick();
      await Promise.resolve();
    });

    const navigateCalls = mocks.navigate.mock.calls;
    const oauthPath = navigateCalls[navigateCalls.length - 1]?.[0];
    expect(oauthPath).toEqual(
      expect.stringMatching(/^\/oauth\?accountReauth=.+#oauth-provider-xai$/)
    );
    const sessionId =
      typeof oauthPath === 'string'
        ? readAccountOAuthReauthSessionId(new URL(oauthPath, 'http://localhost').search)
        : null;
    expect(sessionId).not.toBeNull();
    expect(
      completeAccountOAuthReauthSession({
        connectionFingerprint: 'http://cpa-a.local:8317:manager-key',
        oauthProvider: 'xai',
        sessionId,
        completedAtMs: 400,
      })
    ).toBe(true);

    await act(async () => {
      firstRenderer.unmount();
    });
    mountedAccountsRenderers.delete(firstRenderer);

    const secondRenderer = await renderAccountsPage();
    await flushPromises();
    const remountedCardText = readText(findAccountCardByKey(secondRenderer, selectionKey));
    expect(remountedCardText).not.toContain('accounts.health_reauth');
    expect(remountedCardText).not.toContain('expired token');
  });

  it('uses a newer healthy inspection to retire an older authentication Header', async () => {
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: 300,
      from_ms: 0,
      to_ms: 300,
      items: [
        {
          event_hash: 'older-auth-header',
          timestamp_ms: 100,
          auth_file_snapshot: 'codex.json',
          auth_index: 'auth-1',
          account_snapshot: 'codex@example.com',
          auth_provider_snapshot: 'codex',
          header_error_kind: 'auth',
          header_error_code: 'invalid_api_key',
        },
      ],
    });
    mocks.localInspection = {
      savedAt: 300,
      logs: [],
      logsCollapsed: true,
      actionFilter: 'all',
      connectionFingerprint: 'http://manager.local:18317:manager-key',
      result: {
        settings: {},
        files: mocks.files,
        startedAt: 150,
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
            actionReason: 'healthy',
            statusCode: 200,
            usedPercent: 20,
            isQuota: false,
            autoRecoverEligible: false,
            error: '',
            errorKind: 'inference_healthy',
            actionHandled: false,
          },
        ],
      },
    };

    const renderer = await renderAccountsPage();
    await flushPromises();

    const accountCard = renderer.root.findByProps({
      'data-account-card': 'codex.json\u0000auth-1',
    });
    expect(readText(accountCard)).toContain('accounts.health_available');
    expect(readText(accountCard)).not.toContain('accounts.health_reauth');
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

  it('reconciles a healthy inspection over older quota, cooldown, and action evidence', async () => {
    const file = mocks.files[0];
    const selectionKey = getAuthFileSelectionKey(file);
    const inspectionAtMs = 1_700_000_010_000;
    Object.assign(file, {
      disabled: true,
      statusMessage: 'token_expired',
      errorStatus: 401,
      statusCode: 401,
      updatedAtMs: inspectionAtMs - 1_000,
    });
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: true,
    };
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'error',
      windows: [
        {
          id: 'weekly',
          label: 'Weekly',
          usedPercent: 100,
          resetLabel: 'old reset',
        },
      ],
      error: 'HTTP 401',
      errorStatus: 401,
      failedAtMs: 1_000,
    });
    mocks.getActiveQuotaCooldowns.mockResolvedValue([
      {
        authFileName: file.name,
        authIndex: String(file.authIndex ?? ''),
        disabledAtMs: 1_000,
        recoverAtMs: 10_000,
      },
    ]);
    mocks.listAccountActionCandidates.mockResolvedValue({
      pendingCount: 2,
      items: [
        {
          id: 1,
          actionType: 'disable',
          status: 'pending',
          provider: 'codex',
          authFileName: file.name,
          authIndex: String(file.authIndex ?? ''),
          reason: 'old credential evidence',
          firstSeenAtMs: 1_000,
          lastSeenAtMs: 1_000,
          hitCount: 1,
          createdAtMs: 1_000,
          updatedAtMs: 1_000,
        },
      ],
    });

    const renderer = await renderAccountsPage();
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    const snapshot = makeInspectionSnapshot(
      [file],
      [
        {
          action: 'enable',
          actionStatus: 'success',
          executedAction: 'enable',
          disabled: false,
          statusCode: 200,
          usedPercent: 30,
          quotaWindows: [
            {
              id: 'weekly',
              labelKey: 'codex_quota.secondary_window',
              usedPercent: 30,
              resetLabel: 'next week',
              resetAtMs: 20_000,
              resetAccuracy: 'exact',
              limitWindowSeconds: 604_800,
            },
          ],
        },
      ],
      inspectionAtMs
    );
    await act(async () => {
      mocks.lastHealthWorkspaceProps?.onSnapshotChange(snapshot);
      await mocks.lastHealthWorkspaceProps?.onCredentialsChanged();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });

    const cardText = getAccountCardText(renderer, selectionKey);
    expect(cardText).toContain('accounts.health_available');
    expect(cardText).not.toContain('accounts.health_reauth');
    expect(cardText).not.toContain('accounts.health_disabled');
    expect(cardText).not.toContain('accounts.health_weekly_exhausted');

    await act(async () => {
      findDetailButtonByName(renderer, file.name).props.onClick();
    });
    expect(renderer.root.findAllByProps({ 'data-overview-section': 'attention' })).toHaveLength(0);
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    expect(renderer.root.findByType(AccountQuotaTab).props.detailView.quota.windows).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          usedPercent: 30,
          resetAtMs: 20_000,
          resetAccuracy: 'exact',
        }),
      ])
    );
  });

  it('lets an explicitly empty server inspection inventory clear an older exhausted state', async () => {
    const file = mocks.files[0];
    const selectionKey = getAuthFileSelectionKey(file);
    const inspectionAtMs = 1_700_000_010_000;
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: false,
      serverCodexInspectionAvailable: true,
    };
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      windows: [
        {
          id: 'weekly',
          label: 'Weekly',
          usedPercent: 100,
          resetLabel: 'old reset',
        },
      ],
      quotaInventoryObserved: true,
      fetchedAtMs: inspectionAtMs - 1_000,
    });

    const renderer = await renderAccountsPage();
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    await act(async () => {
      mocks.lastHealthWorkspaceProps?.onSnapshotChange(
        makeInspectionSnapshot(
          [file],
          [
            {
              statusCode: 200,
              usedPercent: undefined,
              quotaWindows: [],
              quotaInventoryObserved: true,
            },
          ],
          inspectionAtMs
        )
      );
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });

    const cardText = getAccountCardText(renderer, selectionKey);
    expect(cardText).toContain('accounts.health_available');
    expect(cardText).not.toContain('accounts.health_exhausted');
    expect(cardText).not.toContain('accounts.health_weekly_exhausted');
    await act(async () => {
      findDetailButtonByName(renderer, file.name).props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    expect(renderer.root.findByType(AccountQuotaTab).props.detailView.quota.windows).toEqual([]);
  });

  it('retains an older permission review candidate after newer healthy inspection evidence', async () => {
    const file = mocks.files[0];
    const selectionKey = getAuthFileSelectionKey(file);
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: true,
    };
    mocks.listAccountActionCandidates.mockResolvedValue({
      pendingCount: 1,
      items: [
        {
          id: 2,
          actionType: 'review',
          status: 'pending',
          provider: 'codex',
          authFileName: file.name,
          authIndex: String(file.authIndex ?? ''),
          reason: 'permission review required',
          firstSeenAtMs: 1_000,
          lastSeenAtMs: 1_000,
          hitCount: 1,
          createdAtMs: 1_000,
          updatedAtMs: 1_000,
        },
      ],
    });

    const renderer = await renderAccountsPage();
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    await act(async () => {
      mocks.lastHealthWorkspaceProps?.onSnapshotChange(
        makeInspectionSnapshot([file], [{ action: 'keep', statusCode: 200 }], 2_000)
      );
      await mocks.lastHealthWorkspaceProps?.onCredentialsChanged();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });

    expect(getAccountCardText(renderer, selectionKey)).toContain('accounts.health_available');
    await act(async () => {
      findDetailButtonByName(renderer, file.name).props.onClick();
    });
    await flushPromises();

    expect(renderer.root.findAllByProps({ 'data-overview-section': 'attention' })).toHaveLength(1);
    expect(treeText(renderer)).toContain('accounts.detail_overview_attention_candidates');
  });

  it('keeps quota actions when a newer inspection only supersedes authentication actions', async () => {
    const file = mocks.files[0];
    const selectionKey = getAuthFileSelectionKey(file);
    mocks.location = {
      pathname: '/accounts',
      search: `?account=${encodeURIComponent(selectionKey)}&tab=diagnostics`,
    };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: true,
    };
    mocks.listAccountActionCandidates.mockResolvedValue({
      pendingCount: 2,
      items: [
        {
          id: 3,
          actionType: 'reauth',
          status: 'pending',
          provider: 'codex',
          authFileName: file.name,
          authIndex: String(file.authIndex ?? ''),
          reason: 'old authentication evidence',
          firstSeenAtMs: 1_000,
          lastSeenAtMs: 1_000,
          hitCount: 1,
          createdAtMs: 1_000,
          updatedAtMs: 1_000,
        },
        {
          id: 4,
          actionType: 'disable',
          status: 'pending',
          provider: 'codex',
          authFileName: file.name,
          authIndex: String(file.authIndex ?? ''),
          reason: 'independent quota evidence',
          firstSeenAtMs: 1_000,
          lastSeenAtMs: 1_000,
          hitCount: 1,
          createdAtMs: 1_000,
          updatedAtMs: 1_000,
        },
      ],
    });

    const renderer = await renderAccountsPage();
    await flushPromises();
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    await act(async () => {
      mocks.lastHealthWorkspaceProps?.onSnapshotChange(
        makeInspectionSnapshot(
          [file],
          [
            {
              action: 'reauth',
              actionStatus: 'pending',
              statusCode: 401,
              usedPercent: undefined,
            },
          ],
          2_000
        )
      );
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_diagnostics').props.onClick();
    });

    expect(
      renderer.root.findByType(AccountDiagnosticsTab).props.detailView.strategy.actionCandidates
    ).toEqual([expect.objectContaining({ id: 4, actionType: 'disable' })]);
  });

  it('reconciles healthy Header evidence over older inspection and operational evidence', async () => {
    const file = mocks.files[0];
    const selectionKey = getAuthFileSelectionKey(file);
    const observedAtMs = Date.now();
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: true,
    };
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: observedAtMs,
      from_ms: observedAtMs - 1_000,
      to_ms: observedAtMs,
      items: [
        {
          event_hash: 'healthy-header-evidence',
          timestamp_ms: observedAtMs,
          model: CODEX_MAIN_MODEL,
          auth_file_snapshot: file.name,
          auth_index: String(file.authIndex ?? ''),
          account_snapshot: String(file.account ?? ''),
          auth_provider_snapshot: 'codex',
          header_quota_used_percent: 30,
          header_quota_recover_at_ms: observedAtMs + 5 * 60 * 60 * 1_000,
        },
      ],
    });
    mocks.getActiveQuotaCooldowns.mockResolvedValue([
      {
        authFileName: file.name,
        authIndex: String(file.authIndex ?? ''),
        disabledAtMs: observedAtMs - 2_000,
        recoverAtMs: observedAtMs + 10_000,
      },
    ]);
    mocks.listAccountActionCandidates.mockResolvedValue({
      pendingCount: 1,
      items: [
        {
          id: 1,
          actionType: 'disable',
          status: 'pending',
          provider: 'codex',
          authFileName: file.name,
          authIndex: String(file.authIndex ?? ''),
          reason: 'old credential evidence',
          firstSeenAtMs: observedAtMs - 2_000,
          lastSeenAtMs: observedAtMs - 2_000,
          hitCount: 1,
          createdAtMs: observedAtMs - 2_000,
          updatedAtMs: observedAtMs - 2_000,
        },
      ],
    });

    const renderer = await renderAccountsPage();
    await flushPromises();
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    await act(async () => {
      mocks.lastHealthWorkspaceProps?.onSnapshotChange(
        makeInspectionSnapshot(
          [file],
          [
            {
              action: 'reauth',
              actionStatus: 'pending',
              statusCode: 401,
              usedPercent: undefined,
            },
          ],
          observedAtMs - 1_000
        )
      );
      await mocks.lastHealthWorkspaceProps?.onCredentialsChanged();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });

    const cardText = getAccountCardText(renderer, selectionKey);
    expect(cardText).toContain('accounts.health_available');
    expect(cardText).not.toContain('accounts.health_reauth');
    expect(cardText).not.toContain('accounts.health_weekly_exhausted');

    await act(async () => {
      findDetailButtonByName(renderer, file.name).props.onClick();
    });
    expect(renderer.root.findAllByProps({ 'data-overview-section': 'attention' })).toHaveLength(0);
    expect(treeText(renderer)).not.toContain('accounts.detail_overview_basis_cooldown');
  });

  it.each([
    ['plan-only', { header_quota_plan_type: 'plus' }],
    [
      'generic upstream error',
      { header_error_kind: 'upstream_error', header_error_code: 'bad_gateway' },
    ],
  ])(
    'does not let newer %s Header metadata clear an older reauth result',
    async (_label, metadata) => {
      const file = mocks.files[0];
      const selectionKey = getAuthFileSelectionKey(file);
      const observedAtMs = Date.now();
      mocks.panelFeatureAvailability = {
        checking: false,
        managerServiceBase: 'http://manager.local:18317',
        requestMonitoringAvailable: true,
        serverCodexInspectionAvailable: true,
      };
      mocks.getHeaderSnapshots.mockResolvedValue({
        generated_at_ms: observedAtMs,
        from_ms: observedAtMs - 1_000,
        to_ms: observedAtMs,
        items: [
          {
            event_hash: `weak-header-${_label}`,
            timestamp_ms: observedAtMs,
            auth_file_snapshot: file.name,
            auth_index: String(file.authIndex ?? ''),
            account_snapshot: String(file.account ?? ''),
            auth_provider_snapshot: 'codex',
            ...metadata,
          },
        ],
      });

      const renderer = await renderAccountsPage();
      await flushPromises();
      await act(async () => {
        findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
      });
      await act(async () => {
        mocks.lastHealthWorkspaceProps?.onSnapshotChange(
          makeInspectionSnapshot(
            [file],
            [
              {
                action: 'reauth',
                actionStatus: 'pending',
                statusCode: 401,
                usedPercent: undefined,
              },
            ],
            observedAtMs - 1_000
          )
        );
        await mocks.lastHealthWorkspaceProps?.onCredentialsChanged();
      });
      await act(async () => {
        findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
      });

      const cardText = getAccountCardText(renderer, selectionKey);
      expect(cardText).toContain('accounts.health_reauth');
      expect(cardText).not.toContain('accounts.health_available');
    }
  );

  it('invalidates only the reauthorized shared credential and ignores its late quota response', async () => {
    const first = {
      ...makeCodexFile('shared-codex.json', 'auth-1', 'first@example.com'),
      account_id: 'space-first',
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    const second = {
      ...makeCodexFile('shared-codex.json', 'auth-2', 'second@example.com'),
      account_id: 'space-second',
    } as AuthFileItem;
    mocks.files = [first, second];
    installCodexQuotaStoreMutationMock();
    mocks.quotaState.codexQuota = {
      ...buildCredentialScopedQuotaRecord(first, {
        status: 'error',
        windows: [],
        error: 'HTTP 401',
        errorStatus: 401,
        failedAtMs: 1_000,
      }),
      ...buildCredentialScopedQuotaRecord(second, {
        status: 'error',
        windows: [],
        error: 'HTTP 401',
        errorStatus: 401,
        failedAtMs: 1_000,
      }),
    };
    const quotaResult = createDeferred<CodexQuotaData>();
    vi.spyOn(CODEX_CONFIG, 'fetchQuota').mockImplementation(() => quotaResult.promise);
    const renderer = await renderAccountsPage();

    let refreshPromise!: Promise<void>;
    await act(async () => {
      refreshPromise = findAccountCardButtonByAriaLabel(
        renderer,
        getAuthFileSelectionKey(first),
        'accounts.refresh_quota'
      ).props.onClick();
      await Promise.resolve();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    mocks.loadFiles.mockImplementationOnce(async () => {
      mocks.files = [
        { ...first, last_refresh: 3_000, modified: 3_100, status: 'ready', statusMessage: '' },
        second,
      ] as AuthFileItem[];
      return mocks.files;
    });
    await act(async () => {
      mocks.lastHealthWorkspaceProps?.onSnapshotChange(
        makeInspectionSnapshot(
          [first, second],
          [
            { action: 'reauth', actionStatus: 'pending', statusCode: 401, usedPercent: undefined },
            { action: 'reauth', actionStatus: 'pending', statusCode: 401, usedPercent: undefined },
          ],
          2_000
        )
      );
      await runInspectionCodexReauth({
        account: 'first@example.com',
        fileName: first.name,
        provider: 'codex',
        authIndex: first.authIndex,
        accountId: 'space-first',
        accountSnapshot: 'first@example.com',
      });
    });
    const setterCallsAfterInvalidation = mocks.quotaState.setCodexQuota.mock.calls.length;
    expect(mocks.quotaState.codexQuota).toHaveProperty(getQuotaCredentialStoreKey(first));
    expect(mocks.quotaState.codexQuota).toMatchObject({
      [getQuotaCredentialStoreKey(first)]: expect.objectContaining({
        status: 'success',
        error: undefined,
        errorStatus: undefined,
        failedAtMs: 1_000,
      }),
    });
    expect(mocks.quotaState.codexQuota).toMatchObject({
      [getQuotaCredentialStoreKey(second)]: expect.objectContaining({
        status: 'error',
        errorStatus: 401,
        failedAtMs: 1_000,
      }),
    });
    expect(mocks.quotaState.codexQuota).toHaveProperty(getQuotaCredentialStoreKey(second));

    quotaResult.resolve({
      ...makeCodexQuotaData(),
      windows: [
        {
          id: 'weekly',
          label: 'Weekly',
          usedPercent: 25,
          resetLabel: 'later',
          resetAtMs: 30_000,
          resetAccuracy: 'exact',
          limitWindowSeconds: 604_800,
        },
      ],
    });
    await act(async () => {
      await refreshPromise;
    });
    expect(mocks.quotaState.setCodexQuota).toHaveBeenCalledTimes(setterCallsAfterInvalidation);

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });
    expect(getAccountCardText(renderer, getAuthFileSelectionKey(first))).not.toContain(
      'accounts.health_reauth'
    );
    expect(getAccountCardText(renderer, getAuthFileSelectionKey(second))).toContain(
      'accounts.health_reauth'
    );
  });

  it('does not let an in-flight old-identity quota response overwrite migrated state', async () => {
    const original = {
      ...makeCodexFile('codex-old.json', 'auth-1', 'workspace@example.com'),
      account_id: 'space-a',
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    const replacement = {
      ...makeCodexFile('codex-new.json', 'auth-2', 'workspace@example.com'),
      account_id: 'space-a',
      status: 'error',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      last_refresh: 3_000,
      modified: 3_100,
    } as AuthFileItem;
    const oldQuota = {
      status: 'success' as const,
      windows: [makeCodexQuotaWindow({ id: 'weekly', usedPercent: 10, observedAtMs: 1_000 })],
      quotaInventoryObserved: true,
      fetchedAtMs: 1_000,
      ...buildQuotaCredentialIdentity(original),
    } satisfies CodexQuotaState;
    mocks.files = [original];
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(original, oldQuota);
    installCodexQuotaStoreMutationMock();
    const quotaResult = createDeferred<CodexQuotaData>();
    vi.spyOn(CODEX_CONFIG, 'fetchQuota').mockImplementation(() => quotaResult.promise);
    const originalSelectionKey = getAuthFileSelectionKey(original);
    const replacementSelectionKey = getAuthFileSelectionKey(replacement);
    const replacementStoreKey = getQuotaCredentialStoreKey(replacement);
    const renderer = await renderAccountsPage();

    let refreshPromise!: Promise<void>;
    await act(async () => {
      refreshPromise = findAccountCardButtonByAriaLabel(
        renderer,
        originalSelectionKey,
        'accounts.refresh_quota'
      ).props.onClick();
      await Promise.resolve();
    });
    await act(async () => {
      findAccountCardButtonByAriaLabel(
        renderer,
        originalSelectionKey,
        'accounts.recommend_action_reauth'
      ).props.onClick();
    });
    mocks.loadFiles.mockImplementationOnce(async () => {
      mocks.files = [replacement];
      return mocks.files;
    });

    expect(await runCodexReauthSuccessAndCaptureError()).toBeUndefined();
    await flushPromises();
    expect(mocks.quotaState.codexQuota).toMatchObject({
      [replacementStoreKey]: expect.objectContaining({
        windows: [expect.objectContaining({ usedPercent: 10 })],
      }),
    });

    quotaResult.resolve({
      ...makeCodexQuotaData(),
      windows: [
        makeCodexQuotaWindow({
          id: 'weekly',
          usedPercent: 25,
          observedAtMs: 4_000,
        }),
      ],
    });
    await act(async () => {
      await refreshPromise;
    });

    expect(mocks.quotaState.codexQuota).not.toHaveProperty(getQuotaCredentialStoreKey(original));
    expect(mocks.quotaState.codexQuota).toMatchObject({
      [replacementStoreKey]: expect.objectContaining({
        windows: [expect.objectContaining({ usedPercent: 10 })],
      }),
    });
    expect(getAccountCardText(renderer, replacementSelectionKey)).not.toContain(
      'accounts.health_reauth'
    );
  });

  it('keeps the health-workspace reauth baseline when credentials refresh during OAuth', async () => {
    const original = {
      ...makeCodexFile('codex-old.json', 'auth-1', 'workspace@example.com'),
      account_id: 'workspace-a',
      status: 'error',
      statusMessage: 'token_expired',
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    const replacement = {
      ...makeCodexFile('codex-new.json', 'auth-2', 'workspace@example.com'),
      account_id: 'workspace-a',
      status: 'ready',
      statusMessage: '',
      last_refresh: 3_000,
      modified: 3_100,
    } as AuthFileItem;
    const target: CodexReauthTarget = {
      account: 'workspace@example.com',
      fileName: original.name,
      provider: 'codex',
      authIndex: original.authIndex,
      accountId: 'workspace-a',
      accountSnapshot: 'workspace@example.com',
    };
    mocks.files = [original];
    const renderer = await renderAccountsPage();

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    expect(mocks.lastHealthWorkspaceProps?.onCodexReauthStart?.(target)).toBe(true);

    mocks.loadFiles.mockImplementationOnce(async () => {
      mocks.files = [replacement];
      return mocks.files;
    });
    await act(async () => {
      await mocks.lastHealthWorkspaceProps?.onCredentialsChanged();
    });

    mocks.loadFiles.mockImplementationOnce(async () => mocks.files);
    await act(async () => {
      await mocks.lastHealthWorkspaceProps?.onCredentialsChanged(target);
    });

    expect(listPendingAccountDirectReauths('http://cpa-a.local:8317:manager-key')).toEqual([]);
  });

  it('invalidates only the refreshed credential inside a shared physical file', async () => {
    const first = makeCodexFile('shared-codex.json', 'auth-1', 'first@example.com');
    const second = makeCodexFile('shared-codex.json', 'auth-2', 'second@example.com');
    const dormant = makeCodexFile('shared-codex.json', 'auth-dormant', 'dormant@example.com');
    mocks.files = [first, second];
    installCodexQuotaStoreMutationMock();
    mocks.quotaState.codexQuota = {
      ...buildCredentialScopedQuotaRecord(first, {
        status: 'error',
        windows: [],
        error: 'HTTP 401',
        errorStatus: 401,
        failedAtMs: 1_000,
      }),
      ...buildCredentialScopedQuotaRecord(second, {
        status: 'error',
        windows: [],
        error: 'HTTP 401',
        errorStatus: 401,
        failedAtMs: 1_000,
      }),
      ...buildCredentialScopedQuotaRecord(dormant, {
        status: 'error',
        windows: [],
        error: 'HTTP 401',
        errorStatus: 401,
        failedAtMs: 1_000,
      }),
    };
    const renderer = await renderAccountsPage();

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    await act(async () => {
      mocks.lastHealthWorkspaceProps?.onSnapshotChange(
        makeInspectionSnapshot(
          [first, second],
          [
            { action: 'reauth', actionStatus: 'pending', statusCode: 401 },
            { action: 'reauth', actionStatus: 'pending', statusCode: 401 },
          ],
          2_000
        )
      );
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });

    act(() => {
      mocks.lastAuthFilesDataOptions?.onCredentialMutation?.({
        kind: 'credential-refreshed',
        selectionKeys: [getAuthFileSelectionKey(first)],
      });
    });

    expect(mocks.quotaState.codexQuota).not.toHaveProperty(getQuotaCredentialStoreKey(first));
    expect(mocks.quotaState.codexQuota).toHaveProperty(getQuotaCredentialStoreKey(second));
    expect(mocks.quotaState.codexQuota).not.toHaveProperty(getQuotaCredentialStoreKey(dormant));
    expect(getAccountCardText(renderer, getAuthFileSelectionKey(first))).not.toContain(
      'accounts.health_reauth'
    );
    expect(getAccountCardText(renderer, getAuthFileSelectionKey(second))).toContain(
      'accounts.health_reauth'
    );
  });

  it('does not let a shared sibling timestamp suppress later operational evidence', async () => {
    const first = makeCodexFile('shared-codex.json', 'auth-1', 'first@example.com');
    const second = makeCodexFile('shared-codex.json', 'auth-2', 'second@example.com');
    const firstSelectionKey = getAuthFileSelectionKey(first);
    mocks.files = [first, second];
    mocks.location = {
      pathname: '/accounts',
      search: `?account=${encodeURIComponent(firstSelectionKey)}&tab=overview`,
    };
    const makeCandidate = (
      id: number,
      file: AuthFileItem,
      observedAtMs: number,
      reason: string
    ) => ({
      id,
      actionType: 'reauth',
      status: 'pending',
      provider: 'codex',
      authFileName: file.name,
      authIndex: String(file.authIndex ?? ''),
      reason,
      firstSeenAtMs: observedAtMs,
      lastSeenAtMs: observedAtMs,
      hitCount: 1,
      createdAtMs: observedAtMs,
      updatedAtMs: observedAtMs,
    });
    const makeCooldown = (file: AuthFileItem, observedAtMs: number) => ({
      authFileName: file.name,
      authIndex: String(file.authIndex ?? ''),
      disabledAtMs: observedAtMs,
      recoverAtMs: observedAtMs + 10_000,
    });
    mocks.listAccountActionCandidates.mockResolvedValue({
      pendingCount: 2,
      items: [
        makeCandidate(1, first, 1_000, 'first old evidence'),
        makeCandidate(2, second, 9_000, 'second newer evidence'),
      ],
    });
    mocks.getActiveQuotaCooldowns.mockResolvedValue([
      makeCooldown(first, 1_000),
      makeCooldown(second, 9_000),
    ]);
    const renderer = await renderAccountsPage();
    await flushPromises();

    act(() => {
      mocks.lastAuthFilesDataOptions?.onCredentialMutation?.({
        kind: 'credential-refreshed',
        selectionKeys: [firstSelectionKey],
      });
    });
    const newFirstCandidate = makeCandidate(3, first, 5_000, 'first new evidence');
    const newFirstCooldown = makeCooldown(first, 5_000);
    mocks.listAccountActionCandidates.mockResolvedValue({
      pendingCount: 2,
      items: [newFirstCandidate, makeCandidate(2, second, 9_000, 'second newer evidence')],
    });
    mocks.getActiveQuotaCooldowns.mockResolvedValue([
      newFirstCooldown,
      makeCooldown(second, 9_000),
    ]);

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    await act(async () => {
      await mocks.lastHealthWorkspaceProps?.onCredentialsChanged();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    expect(renderer.root.findByType(AccountQuotaTab).props.detailView.quota.cooldown).toEqual(
      newFirstCooldown
    );
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_diagnostics').props.onClick();
    });
    expect(
      renderer.root.findByType(AccountDiagnosticsTab).props.detailView.strategy.actionCandidates
    ).toEqual([
      expect.objectContaining({ id: newFirstCandidate.id, reason: 'first new evidence' }),
    ]);
  });

  it('ignores in-flight operational evidence after a credential is refreshed', async () => {
    const file = mocks.files[0];
    const selectionKey = getAuthFileSelectionKey(file);
    mocks.location = {
      pathname: '/accounts',
      search: `?account=${encodeURIComponent(selectionKey)}&tab=overview`,
    };
    const candidatesRequest = createDeferred<AccountActionCandidatesResponse>();
    const cooldownsRequest = createDeferred<QuotaCooldownInfo[]>();
    mocks.listAccountActionCandidates.mockReturnValue(candidatesRequest.promise);
    mocks.getActiveQuotaCooldowns.mockReturnValue(cooldownsRequest.promise);
    const renderer = await renderAccountsPage();
    await flushPromises();
    expect(mocks.listAccountActionCandidates).toHaveBeenCalledTimes(1);
    expect(mocks.getActiveQuotaCooldowns).toHaveBeenCalledTimes(1);

    act(() => {
      mocks.lastAuthFilesDataOptions?.onCredentialMutation?.({
        kind: 'credential-refreshed',
        selectionKeys: [selectionKey],
      });
    });
    await act(async () => {
      candidatesRequest.resolve({
        pendingCount: 1,
        items: [
          {
            id: 1,
            actionType: 'reauth',
            status: 'pending',
            provider: 'codex',
            authFileName: file.name,
            authIndex: String(file.authIndex ?? ''),
            reason: 'stale in-flight evidence',
            firstSeenAtMs: 1_000,
            lastSeenAtMs: 1_000,
            hitCount: 1,
            createdAtMs: 1_000,
            updatedAtMs: 1_000,
          },
        ],
      });
      cooldownsRequest.resolve([
        {
          authFileName: file.name,
          authIndex: String(file.authIndex ?? ''),
          disabledAtMs: 1_000,
          recoverAtMs: 11_000,
        },
      ]);
      await Promise.all([candidatesRequest.promise, cooldownsRequest.promise]);
    });
    await flushPromises();

    const detailView = renderer.root.findByType(AccountOverviewTab).props.detailView;
    expect(detailView.quota.cooldown).toBeNull();
    expect(detailView.strategy.actionCandidates).toEqual([]);
  });

  it('treats the first post-mutation operational reload as a stale baseline', async () => {
    const file = mocks.files[0];
    const selectionKey = getAuthFileSelectionKey(file);
    mocks.location = {
      pathname: '/accounts',
      search: `?account=${encodeURIComponent(selectionKey)}&tab=overview`,
    };
    const candidatesRequest = createDeferred<AccountActionCandidatesResponse>();
    const cooldownsRequest = createDeferred<QuotaCooldownInfo[]>();
    mocks.listAccountActionCandidates.mockReturnValueOnce(candidatesRequest.promise);
    mocks.getActiveQuotaCooldowns.mockReturnValueOnce(cooldownsRequest.promise);
    const makeCandidate = (id: number, observedAtMs: number) => ({
      id,
      actionType: 'reauth',
      status: 'pending',
      provider: 'codex',
      authFileName: file.name,
      authIndex: String(file.authIndex ?? ''),
      reason: `candidate-${id}`,
      firstSeenAtMs: observedAtMs,
      lastSeenAtMs: observedAtMs,
      hitCount: 1,
      createdAtMs: observedAtMs,
      updatedAtMs: observedAtMs,
    });
    const makeCooldown = (observedAtMs: number) => ({
      authFileName: file.name,
      authIndex: String(file.authIndex ?? ''),
      disabledAtMs: observedAtMs,
      recoverAtMs: observedAtMs + 10_000,
    });
    const renderer = await renderAccountsPage();
    await flushPromises();

    act(() => {
      mocks.lastAuthFilesDataOptions?.onCredentialMutation?.({
        kind: 'credential-refreshed',
        selectionKeys: [selectionKey],
      });
    });
    await act(async () => {
      candidatesRequest.resolve({ pendingCount: 1, items: [makeCandidate(1, 2_000)] });
      cooldownsRequest.resolve([makeCooldown(2_000)]);
      await Promise.all([candidatesRequest.promise, cooldownsRequest.promise]);
    });
    await flushPromises();

    mocks.listAccountActionCandidates.mockResolvedValue({
      pendingCount: 1,
      items: [makeCandidate(1, 2_000)],
    });
    mocks.getActiveQuotaCooldowns.mockResolvedValue([makeCooldown(2_000)]);
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    await act(async () => {
      await mocks.lastHealthWorkspaceProps?.onCredentialsChanged();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });
    expect(renderer.root.findByType(AccountOverviewTab).props.detailView.quota.cooldown).toBeNull();
    expect(
      renderer.root.findByType(AccountOverviewTab).props.detailView.strategy.actionCandidates
    ).toEqual([]);

    const nextCandidate = makeCandidate(2, 3_000);
    const nextCooldown = makeCooldown(3_000);
    mocks.listAccountActionCandidates.mockResolvedValue({
      pendingCount: 1,
      items: [nextCandidate],
    });
    mocks.getActiveQuotaCooldowns.mockResolvedValue([nextCooldown]);
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    await act(async () => {
      await mocks.lastHealthWorkspaceProps?.onCredentialsChanged();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });

    expect(renderer.root.findByType(AccountOverviewTab).props.detailView.quota.cooldown).toEqual(
      nextCooldown
    );
    expect(
      renderer.root.findByType(AccountOverviewTab).props.detailView.strategy.actionCandidates
    ).toEqual([expect.objectContaining({ id: nextCandidate.id })]);
  });

  it('does not let another credential future-date a pending operational baseline', async () => {
    const first = makeCodexFile('first.json', 'auth-1', 'first@example.com');
    const second = makeCodexFile('second.json', 'auth-2', 'second@example.com');
    const firstSelectionKey = getAuthFileSelectionKey(first);
    mocks.files = [first, second];
    mocks.location = {
      pathname: '/accounts',
      search: `?account=${encodeURIComponent(firstSelectionKey)}&tab=overview`,
    };
    const candidatesRequest = createDeferred<AccountActionCandidatesResponse>();
    const cooldownsRequest = createDeferred<QuotaCooldownInfo[]>();
    mocks.listAccountActionCandidates.mockReturnValueOnce(candidatesRequest.promise);
    mocks.getActiveQuotaCooldowns.mockReturnValueOnce(cooldownsRequest.promise);
    const makeCandidate = (id: number, file: AuthFileItem, observedAtMs: number) => ({
      id,
      actionType: 'reauth',
      status: 'pending',
      provider: 'codex',
      authFileName: file.name,
      authIndex: String(file.authIndex ?? ''),
      reason: `candidate-${id}`,
      firstSeenAtMs: observedAtMs,
      lastSeenAtMs: observedAtMs,
      hitCount: 1,
      createdAtMs: observedAtMs,
      updatedAtMs: observedAtMs,
    });
    const makeCooldown = (file: AuthFileItem, observedAtMs: number) => ({
      authFileName: file.name,
      authIndex: String(file.authIndex ?? ''),
      disabledAtMs: observedAtMs,
      recoverAtMs: observedAtMs + 10_000,
    });
    const renderer = await renderAccountsPage();
    await flushPromises();

    act(() => {
      mocks.lastAuthFilesDataOptions?.onCredentialMutation?.({
        kind: 'credential-refreshed',
        selectionKeys: [firstSelectionKey],
      });
    });
    await act(async () => {
      candidatesRequest.resolve({
        pendingCount: 2,
        items: [makeCandidate(1, first, 2_000), makeCandidate(2, second, 9_000)],
      });
      cooldownsRequest.resolve([makeCooldown(first, 2_000), makeCooldown(second, 9_000)]);
      await Promise.all([candidatesRequest.promise, cooldownsRequest.promise]);
    });
    await flushPromises();

    mocks.listAccountActionCandidates.mockResolvedValue({
      pendingCount: 2,
      items: [makeCandidate(1, first, 2_000), makeCandidate(2, second, 9_000)],
    });
    mocks.getActiveQuotaCooldowns.mockResolvedValue([
      makeCooldown(first, 2_000),
      makeCooldown(second, 9_000),
    ]);
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    await act(async () => {
      await mocks.lastHealthWorkspaceProps?.onCredentialsChanged();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });
    expect(renderer.root.findByType(AccountOverviewTab).props.detailView.quota.cooldown).toBeNull();

    const nextCandidate = makeCandidate(3, first, 3_000);
    const nextCooldown = makeCooldown(first, 3_000);
    mocks.listAccountActionCandidates.mockResolvedValue({
      pendingCount: 2,
      items: [nextCandidate, makeCandidate(2, second, 9_000)],
    });
    mocks.getActiveQuotaCooldowns.mockResolvedValue([nextCooldown, makeCooldown(second, 9_000)]);
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    await act(async () => {
      await mocks.lastHealthWorkspaceProps?.onCredentialsChanged();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });

    const detailView = renderer.root.findByType(AccountOverviewTab).props.detailView;
    expect(detailView.quota.cooldown).toEqual(nextCooldown);
    expect(detailView.strategy.actionCandidates).toEqual([
      expect.objectContaining({ id: nextCandidate.id }),
    ]);
  });

  it('keeps status-change inspection boundaries scoped to each selected credential', async () => {
    const first = makeCodexFile('first.json', 'auth-1', 'first@example.com');
    const second = makeCodexFile('second.json', 'auth-2', 'second@example.com');
    const firstSelectionKey = getAuthFileSelectionKey(first);
    const secondSelectionKey = getAuthFileSelectionKey(second);
    mocks.files = [first, second];
    const renderer = await renderAccountsPage();

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    await act(async () => {
      mocks.lastHealthWorkspaceProps?.onSnapshotChange(
        makeInspectionSnapshot(
          [first, second],
          [
            {
              action: 'reauth',
              actionStatus: 'pending',
              statusCode: 401,
              usedPercent: undefined,
              createdAtMs: 1_000,
            },
            {
              action: 'reauth',
              actionStatus: 'pending',
              statusCode: 401,
              usedPercent: undefined,
              createdAtMs: 9_000,
            },
          ],
          9_000
        )
      );
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });

    act(() => {
      mocks.lastAuthFilesDataOptions?.onCredentialMutation?.({
        kind: 'status-changed',
        selectionKeys: [firstSelectionKey, secondSelectionKey],
      });
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    await act(async () => {
      mocks.lastHealthWorkspaceProps?.onSnapshotChange(
        makeInspectionSnapshot(
          [first, second],
          [
            {
              action: 'reauth',
              actionStatus: 'pending',
              statusCode: 401,
              usedPercent: undefined,
              createdAtMs: 5_000,
            },
            {
              action: 'reauth',
              actionStatus: 'pending',
              statusCode: 401,
              usedPercent: undefined,
              createdAtMs: 9_000,
            },
          ],
          10_000
        )
      );
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });

    expect(getAccountCardText(renderer, firstSelectionKey)).toContain('accounts.health_reauth');
    expect(getAccountCardText(renderer, secondSelectionKey)).not.toContain(
      'accounts.health_reauth'
    );
  });

  it('supersedes only old status evidence while preserving shared credential quota state', async () => {
    const first = {
      ...makeCodexFile('shared-codex.json', 'auth-1', 'first@example.com'),
      statusMessage: 'token_expired',
    } as AuthFileItem;
    const second = {
      ...makeCodexFile('shared-codex.json', 'auth-2', 'second@example.com'),
      statusMessage: 'token_expired',
    } as AuthFileItem;
    mocks.files = [first, second];
    mocks.quotaState.codexQuota = {
      ...buildCredentialScopedQuotaRecord(first, {
        status: 'success',
        windows: [
          {
            id: 'weekly',
            label: 'Weekly',
            usedPercent: 30,
            resetLabel: 'later',
            observedAtMs: 1_500,
          },
        ],
        quotaInventoryObserved: true,
        fetchedAtMs: 1_500,
      }),
      ...buildCredentialScopedQuotaRecord(second, {
        status: 'success',
        windows: [
          {
            id: 'weekly',
            label: 'Weekly',
            usedPercent: 30,
            resetLabel: 'later',
            observedAtMs: 1_500,
          },
        ],
        quotaInventoryObserved: true,
        fetchedAtMs: 1_500,
      }),
    };
    const renderer = await renderAccountsPage();

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    await act(async () => {
      mocks.lastHealthWorkspaceProps?.onSnapshotChange(
        makeInspectionSnapshot(
          [first, second],
          [
            { action: 'reauth', actionStatus: 'pending', statusCode: 401 },
            { action: 'reauth', actionStatus: 'pending', statusCode: 401 },
          ],
          2_000
        )
      );
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });
    expect(getAccountCardText(renderer, getAuthFileSelectionKey(first))).toContain(
      'accounts.health_reauth'
    );

    const quotaSetterCalls = mocks.quotaState.setCodexQuota.mock.calls.length;
    act(() => {
      mocks.lastAuthFilesDataOptions?.onCredentialMutation?.({
        kind: 'status-changed',
        selectionKeys: [getAuthFileSelectionKey(first)],
      });
    });

    expect(mocks.quotaState.setCodexQuota).toHaveBeenCalledTimes(quotaSetterCalls);
    expect(mocks.quotaState.codexQuota).toHaveProperty(getQuotaCredentialStoreKey(first));
    expect(mocks.quotaState.codexQuota).toHaveProperty(getQuotaCredentialStoreKey(second));
    expect(getAccountCardText(renderer, getAuthFileSelectionKey(first))).toContain(
      'accounts.health_available'
    );
    expect(getAccountCardText(renderer, getAuthFileSelectionKey(first))).not.toContain(
      'token_expired'
    );
    expect(getAccountCardText(renderer, getAuthFileSelectionKey(second))).toContain(
      'accounts.health_reauth'
    );
  });

  it('allows the same raw authentication error to reappear after a clear state is observed', async () => {
    const file = {
      ...makeCodexFile('raw-status-cycle.json', 'auth-cycle', 'cycle@example.com'),
      statusMessage: 'token_expired',
    } as AuthFileItem;
    const selectionKey = getAuthFileSelectionKey(file);
    mocks.files = [file];
    const renderer = await renderAccountsPage();
    expect(getAccountCardText(renderer, selectionKey)).toContain('accounts.health_reauth');

    act(() => {
      mocks.lastAuthFilesDataOptions?.onCredentialMutation?.({
        kind: 'status-changed',
        selectionKeys: [selectionKey],
      });
    });
    expect(getAccountCardText(renderer, selectionKey)).not.toContain('accounts.health_reauth');

    mocks.files = [{ ...file, statusMessage: '' }];
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await flushPromises();

    mocks.files = [{ ...file, statusMessage: 'token_expired' }];
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await flushPromises();

    expect(getAccountCardText(renderer, selectionKey)).toContain('accounts.health_reauth');
  });

  it('invalidates every identity in a replaced file and ignores its late quota response', async () => {
    const first = makeCodexFile('shared-codex.json', 'auth-1', 'first@example.com');
    const second = makeCodexFile('shared-codex.json', 'auth-2', 'second@example.com');
    const other = makeCodexFile('other-codex.json', 'auth-3', 'other@example.com');
    const staleServerEvidenceAtMs = Date.now() + 5 * 60_000;
    mocks.files = [first, second, other];
    installCodexQuotaStoreMutationMock();
    mocks.quotaState.codexQuota = {
      ...buildCredentialScopedQuotaRecord(first, {
        status: 'error',
        windows: [],
        error: 'HTTP 401',
        errorStatus: 401,
        failedAtMs: 1_000,
      }),
      ...buildCredentialScopedQuotaRecord(second, {
        status: 'error',
        windows: [],
        error: 'HTTP 401',
        errorStatus: 401,
        failedAtMs: 1_000,
      }),
      ...buildCredentialScopedQuotaRecord(other, {
        status: 'error',
        windows: [],
        error: 'HTTP 401',
        errorStatus: 401,
        failedAtMs: 1_000,
      }),
    };
    const quotaResult = createDeferred<CodexQuotaData>();
    vi.spyOn(CODEX_CONFIG, 'fetchQuota').mockImplementation(() => quotaResult.promise);
    const renderer = await renderAccountsPage();

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    await act(async () => {
      mocks.lastHealthWorkspaceProps?.onSnapshotChange(
        makeInspectionSnapshot(
          [first, second, other],
          [
            { action: 'reauth', actionStatus: 'pending', statusCode: 401 },
            { action: 'reauth', actionStatus: 'pending', statusCode: 401 },
            { action: 'reauth', actionStatus: 'pending', statusCode: 401 },
          ],
          staleServerEvidenceAtMs
        )
      );
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });

    let refreshPromise!: Promise<void>;
    await act(async () => {
      refreshPromise = findAccountCardButtonByAriaLabel(
        renderer,
        getAuthFileSelectionKey(first),
        'accounts.refresh_quota'
      ).props.onClick();
      await Promise.resolve();
    });
    act(() => {
      mocks.lastAuthFilesDataOptions?.onCredentialMutation?.({
        kind: 'source-files-changed',
        fileNames: [' shared-codex.json ', 'shared-codex.json'],
      });
    });

    const setterCallsAfterInvalidation = mocks.quotaState.setCodexQuota.mock.calls.length;
    expect(mocks.quotaState.codexQuota).not.toHaveProperty(getQuotaCredentialStoreKey(first));
    expect(mocks.quotaState.codexQuota).not.toHaveProperty(getQuotaCredentialStoreKey(second));
    expect(mocks.quotaState.codexQuota).toHaveProperty(getQuotaCredentialStoreKey(other));

    quotaResult.resolve({
      ...makeCodexQuotaData(),
      windows: [
        {
          id: 'weekly',
          label: 'Weekly',
          usedPercent: 25,
          resetLabel: 'later',
          resetAtMs: 30_000,
          resetAccuracy: 'exact',
          limitWindowSeconds: 604_800,
        },
      ],
    });
    await act(async () => refreshPromise);

    expect(mocks.quotaState.setCodexQuota).toHaveBeenCalledTimes(setterCallsAfterInvalidation);
    expect(getAccountCardText(renderer, getAuthFileSelectionKey(first))).not.toContain(
      'accounts.health_reauth'
    );
    expect(getAccountCardText(renderer, getAuthFileSelectionKey(second))).not.toContain(
      'accounts.health_reauth'
    );
    expect(getAccountCardText(renderer, getAuthFileSelectionKey(other))).toContain(
      'accounts.health_reauth'
    );
  });

  it.each([
    [
      'physical replacement',
      { kind: 'source-files-changed', fileNames: ['fallback.json'] } as AuthFilesCredentialMutation,
    ],
    [
      'credential refresh',
      {
        kind: 'credential-refreshed',
        selectionKeys: ['fallback.json\u0000auth-fallback'],
      } as AuthFilesCredentialMutation,
    ],
  ])('invalidates filename-only inspection evidence after %s', async (_label, mutation) => {
    const file = makeCodexFile('fallback.json', 'auth-fallback', 'fallback@example.com');
    mocks.files = [file];
    const renderer = await renderAccountsPage();

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    await act(async () => {
      mocks.lastHealthWorkspaceProps?.onSnapshotChange(
        makeInspectionSnapshot(
          [file],
          [
            {
              authIndex: undefined,
              accountSnapshot: undefined,
              action: 'reauth',
              actionStatus: 'pending',
              statusCode: 401,
              usedPercent: undefined,
            },
          ],
          2_000
        )
      );
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });
    expect(getAccountCardText(renderer, getAuthFileSelectionKey(file))).toContain(
      'accounts.health_reauth'
    );

    act(() => {
      mocks.lastAuthFilesDataOptions?.onCredentialMutation?.(mutation);
    });

    expect(getAccountCardText(renderer, getAuthFileSelectionKey(file))).not.toContain(
      'accounts.health_reauth'
    );
  });

  it('invalidates future-dated Header evidence when a physical file is replaced', async () => {
    const file = makeCodexFile('header-replaced.json', 'auth-header', 'header@example.com');
    const staleHeaderAtMs = Date.now() + 5 * 60_000;
    mocks.files = [file];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: staleHeaderAtMs,
      from_ms: staleHeaderAtMs - 1_000,
      to_ms: staleHeaderAtMs,
      items: [
        {
          event_hash: 'future-stale-header',
          timestamp_ms: staleHeaderAtMs,
          auth_file_snapshot: file.name,
          auth_index: String(file.authIndex ?? ''),
          account_snapshot: String(file.account ?? ''),
          auth_provider_snapshot: 'codex',
          header_quota_used_percent: 30,
          header_quota_recover_at_ms: staleHeaderAtMs + 5 * 60 * 60_000,
        },
      ],
    });

    const renderer = await renderAccountsPage();
    await flushPromises();
    await act(async () => {
      findDetailButtonByName(renderer, file.name).props.onClick();
    });
    expect(treeText(renderer)).toContain('accounts.quota_source_observed_header');

    act(() => {
      mocks.lastAuthFilesDataOptions?.onCredentialMutation?.({
        kind: 'source-files-changed',
        fileNames: [file.name],
      });
    });

    expect(treeText(renderer)).not.toContain('accounts.quota_source_observed_header');
    expect(treeText(renderer)).toContain('accounts.quota_source_none');
  });

  it('captures the first Header response as a stale baseline when mutation happens before initial load', async () => {
    const file = makeCodexFile('pending-header.json', 'auth-header', 'header@example.com');
    const futureSibling = makeCodexFile('future-header.json', 'auth-future', 'future@example.com');
    mocks.files = [file, futureSibling];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    const makeHeaderResponse = (
      timestampMs: number,
      eventHash: string,
      includeFutureSibling = false
    ) => ({
      generated_at_ms: timestampMs,
      from_ms: Math.max(0, timestampMs - 1_000),
      to_ms: timestampMs,
      items: [
        {
          event_hash: eventHash,
          timestamp_ms: timestampMs,
          auth_file_snapshot: file.name,
          auth_index: String(file.authIndex ?? ''),
          account_snapshot: String(file.account ?? ''),
          auth_provider_snapshot: 'codex',
          header_quota_used_percent: 30,
          header_quota_recover_at_ms: timestampMs + 5 * 60 * 60_000,
        },
        ...(includeFutureSibling
          ? [
              {
                event_hash: 'future-sibling-header',
                timestamp_ms: 9_000,
                auth_file_snapshot: futureSibling.name,
                auth_index: String(futureSibling.authIndex ?? ''),
                account_snapshot: String(futureSibling.account ?? ''),
                auth_provider_snapshot: 'codex',
                header_quota_used_percent: 40,
                header_quota_recover_at_ms: 19_000,
              },
            ]
          : []),
      ],
    });
    const initialHeaders = createDeferred<ReturnType<typeof makeHeaderResponse>>();
    mocks.getHeaderSnapshots
      .mockReturnValueOnce(initialHeaders.promise)
      .mockResolvedValueOnce(makeHeaderResponse(2_000, 'stale-header', true))
      .mockResolvedValue(makeHeaderResponse(3_000, 'new-header'));

    const renderer = await renderAccountsPage();
    act(() => {
      mocks.lastAuthFilesDataOptions?.onCredentialMutation?.({
        kind: 'source-files-changed',
        fileNames: [file.name],
      });
    });
    await act(async () => {
      initialHeaders.resolve(makeHeaderResponse(2_000, 'ignored-in-flight-header'));
      await initialHeaders.promise;
    });
    await flushPromises();

    await act(async () => {
      await findButtonByText(renderer, 'common.refresh').props.onClick();
    });
    await flushPromises();
    await act(async () => {
      findDetailButtonByName(renderer, file.name).props.onClick();
    });
    expect(treeText(renderer)).not.toContain('accounts.quota_source_observed_header');
    expect(treeText(renderer)).toContain('accounts.quota_source_none');

    await act(async () => {
      await findButtonByText(renderer, 'common.refresh').props.onClick();
    });
    await flushPromises();
    expect(treeText(renderer)).toContain('accounts.quota_source_observed_header');
  });

  it('captures the first inspection snapshot as a stale baseline when mutation happens before initial load', async () => {
    const file = makeCodexFile('pending-inspection.json', 'auth-pending', 'pending@example.com');
    const futureSibling = makeCodexFile(
      'future-inspection.json',
      'auth-future',
      'future@example.com'
    );
    mocks.files = [file, futureSibling];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: false,
      serverCodexInspectionAvailable: true,
    };
    const oldRun: CodexInspectionRun = {
      id: 20,
      triggerType: 'manual',
      status: 'completed',
      startedAtMs: 1_000,
      finishedAtMs: 2_000,
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
      createdAtMs: 1_000,
      updatedAtMs: 2_000,
    };
    const newRun: CodexInspectionRun = {
      ...oldRun,
      id: 21,
      startedAtMs: 3_000,
      finishedAtMs: 4_000,
      reauthCount: 0,
      keepCount: 1,
      createdAtMs: 3_000,
      updatedAtMs: 4_000,
    };
    const buildDetail = (
      run: CodexInspectionRun,
      overrides: Partial<CodexInspectionResult>,
      includeFutureSibling = false
    ) => ({
      run,
      results: [
        {
          id: run.id,
          runId: run.id,
          accountKey: getAuthFileSelectionKey(file),
          fileName: file.name,
          displayAccount: String(file.account ?? file.name),
          accountSnapshot: String(file.account ?? ''),
          authIndex: String(file.authIndex ?? ''),
          provider: 'codex',
          disabled: false,
          action: 'keep',
          actionReason: '',
          actionStatus: 'none',
          statusCode: 200,
          usedPercent: 30,
          isQuota: false,
          createdAtMs: run.finishedAtMs ?? run.updatedAtMs,
          ...overrides,
        },
        ...(includeFutureSibling
          ? [
              {
                id: run.id + 100,
                runId: run.id,
                accountKey: getAuthFileSelectionKey(futureSibling),
                fileName: futureSibling.name,
                displayAccount: String(futureSibling.account ?? futureSibling.name),
                accountSnapshot: String(futureSibling.account ?? ''),
                authIndex: String(futureSibling.authIndex ?? ''),
                provider: 'codex',
                disabled: false,
                action: 'reauth',
                actionReason: 'future sibling',
                actionStatus: 'pending',
                statusCode: 401,
                isQuota: false,
                createdAtMs: 9_000,
              },
            ]
          : []),
      ],
    });
    const initialRuns = createDeferred<{ items: CodexInspectionRun[] }>();
    mocks.listCodexInspectionRuns
      .mockReturnValueOnce(initialRuns.promise)
      .mockResolvedValueOnce({ items: [oldRun] })
      .mockResolvedValue({ items: [newRun, oldRun] });
    mocks.getCodexInspectionRun
      .mockResolvedValueOnce(
        buildDetail(
          oldRun,
          {
            action: 'reauth',
            actionStatus: 'pending',
            statusCode: 401,
            usedPercent: undefined,
          },
          true
        )
      )
      .mockResolvedValue(buildDetail(newRun, {}));

    const renderer = await renderAccountsPage();
    act(() => {
      mocks.lastAuthFilesDataOptions?.onCredentialMutation?.({
        kind: 'source-files-changed',
        fileNames: [file.name],
      });
    });
    await act(async () => {
      initialRuns.resolve({ items: [oldRun] });
      await initialRuns.promise;
    });
    await flushPromises();

    await act(async () => {
      await findButtonByText(renderer, 'common.refresh').props.onClick();
    });
    await flushPromises();
    expect(getAccountCardText(renderer, getAuthFileSelectionKey(file))).not.toContain(
      'accounts.health_reauth'
    );

    await act(async () => {
      await findButtonByText(renderer, 'common.refresh').props.onClick();
    });
    await flushPromises();
    expect(getAccountCardText(renderer, getAuthFileSelectionKey(file))).toContain(
      'accounts.health_available'
    );
  });

  it('accepts newer inspection quota evidence when the browser clock is ahead of the server', async () => {
    const browserNow = Date.now();
    const oldInspectionAtMs = browserNow - 10_000;
    const newInspectionAtMs = browserNow - 5_000;
    const file = makeCodexFile('clock-skew.json', 'auth-clock', 'clock@example.com');
    mocks.files = [file];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: false,
      serverCodexInspectionAvailable: true,
    };
    const oldRun: CodexInspectionRun = {
      id: 1,
      triggerType: 'manual',
      status: 'completed',
      startedAtMs: oldInspectionAtMs - 1_000,
      finishedAtMs: oldInspectionAtMs,
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
      createdAtMs: oldInspectionAtMs - 1_000,
      updatedAtMs: oldInspectionAtMs,
    };
    const newRun: CodexInspectionRun = {
      ...oldRun,
      id: 2,
      startedAtMs: newInspectionAtMs - 1_000,
      finishedAtMs: newInspectionAtMs,
      reauthCount: 0,
      keepCount: 1,
      createdAtMs: newInspectionAtMs - 1_000,
      updatedAtMs: newInspectionAtMs,
    };
    const buildResult = (
      run: CodexInspectionRun,
      overrides: Partial<CodexInspectionResult>
    ): CodexInspectionResult => ({
      id: run.id,
      runId: run.id,
      accountKey: getAuthFileSelectionKey(file),
      fileName: file.name,
      displayAccount: String(file.account ?? file.name),
      accountSnapshot: String(file.account ?? ''),
      authIndex: String(file.authIndex ?? ''),
      provider: 'codex',
      disabled: false,
      action: 'keep',
      actionReason: '',
      actionStatus: 'none',
      statusCode: 200,
      usedPercent: 30,
      isQuota: false,
      createdAtMs: run.finishedAtMs ?? run.updatedAtMs,
      ...overrides,
    });
    mocks.listCodexInspectionRuns
      .mockResolvedValueOnce({ items: [oldRun] })
      .mockResolvedValue({ items: [newRun, oldRun] });
    mocks.getCodexInspectionRun
      .mockResolvedValueOnce({
        run: oldRun,
        results: [
          buildResult(oldRun, {
            action: 'reauth',
            actionStatus: 'pending',
            statusCode: 401,
            usedPercent: undefined,
          }),
        ],
      })
      .mockResolvedValue({ run: newRun, results: [buildResult(newRun, {})] });

    const renderer = await renderAccountsPage();
    await flushPromises();
    expect(getAccountCardText(renderer, getAuthFileSelectionKey(file))).toContain(
      'accounts.health_reauth'
    );

    act(() => {
      mocks.lastAuthFilesDataOptions?.onCredentialMutation?.({
        kind: 'source-files-changed',
        fileNames: [file.name],
      });
    });
    await act(async () => {
      await findButtonByText(renderer, 'common.refresh').props.onClick();
    });
    await flushPromises();

    expect(mocks.listCodexInspectionRuns).toHaveBeenCalledTimes(2);
    expect(mocks.getCodexInspectionRun).toHaveBeenCalledTimes(2);
    expect(getAccountCardText(renderer, getAuthFileSelectionKey(file))).toContain(
      'accounts.health_available'
    );
    await act(async () => {
      findDetailButtonByName(renderer, file.name).props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    expect(renderer.root.findByType(AccountQuotaTab).props.detailView.quota.windows).toEqual(
      expect.arrayContaining([expect.objectContaining({ usedPercent: 30 })])
    );
  });

  it('does not reattach filename-only inspection evidence after reauth or capability rechecks', async () => {
    const original = {
      ...makeCodexFile('rotated-codex.json', 'auth-before-reauth', 'before@example.com'),
      account_id: 'space-before',
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    const replacement = {
      ...makeCodexFile(original.name, 'auth-after-reauth', 'after@example.com'),
      account_id: 'space-after',
    } as AuthFileItem;
    mocks.files = [original];
    installCodexQuotaStoreMutationMock();
    mocks.quotaState.codexQuota = {
      ...buildCredentialScopedQuotaRecord(original, {
        status: 'error',
        windows: [],
        error: 'HTTP 401',
        errorStatus: 401,
        failedAtMs: 1_000,
      }),
      ...buildCredentialScopedQuotaRecord(replacement, {
        status: 'error',
        windows: [],
        error: 'HTTP 401',
        errorStatus: 401,
        failedAtMs: 1_000,
      }),
    };
    const renderer = await renderAccountsPage();

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    mocks.loadFiles.mockImplementationOnce(async () => {
      mocks.files = [
        {
          ...original,
          last_refresh: 3_000,
          modified: 3_100,
          status: 'ready',
          statusMessage: '',
        },
      ] as AuthFileItem[];
      return mocks.files;
    });
    await act(async () => {
      mocks.lastHealthWorkspaceProps?.onSnapshotChange(
        makeInspectionSnapshot(
          [original],
          [
            {
              authIndex: undefined,
              accountSnapshot: undefined,
              action: 'reauth',
              actionStatus: 'pending',
              statusCode: 401,
              usedPercent: undefined,
            },
          ],
          2_000
        )
      );
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });
    expect(getAccountCardText(renderer, getAuthFileSelectionKey(original))).toContain(
      'accounts.health_reauth'
    );

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    await act(async () => {
      await runInspectionCodexReauth({
        account: 'before@example.com',
        fileName: original.name,
        provider: 'codex',
        authIndex: original.authIndex,
        accountId: 'space-before',
        accountSnapshot: 'before@example.com',
      });
    });

    expect(mocks.quotaState.codexQuota).toHaveProperty(getQuotaCredentialStoreKey(original));
    expect(mocks.quotaState.codexQuota).not.toHaveProperty(getQuotaCredentialStoreKey(replacement));
    mocks.files = [replacement];
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });

    const cardText = getAccountCardText(renderer, getAuthFileSelectionKey(replacement));
    expect(cardText).not.toContain('accounts.health_reauth');

    mocks.panelFeatureAvailability = {
      ...mocks.panelFeatureAvailability,
      checking: true,
    };
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    mocks.panelFeatureAvailability = {
      ...mocks.panelFeatureAvailability,
      checking: false,
      requestMonitoringAvailable: true,
    };
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });

    expect(getAccountCardText(renderer, getAuthFileSelectionKey(replacement))).not.toContain(
      'accounts.health_reauth'
    );
  });

  it('keeps credential replacement boundaries after the Accounts page remounts', async () => {
    const file = makeCodexFile('remount-boundary.json', 'auth-remount', 'remount@example.com');
    mocks.files = [file];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: false,
      serverCodexInspectionAvailable: true,
    };
    const staleRun: CodexInspectionRun = {
      id: 22,
      triggerType: 'manual',
      status: 'completed',
      startedAtMs: 1_000,
      finishedAtMs: 2_000,
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
      createdAtMs: 1_000,
      updatedAtMs: 2_000,
    };
    const staleDetail = {
      run: staleRun,
      results: [
        {
          id: 22,
          runId: staleRun.id,
          accountKey: getAuthFileSelectionKey(file),
          fileName: file.name,
          displayAccount: String(file.account ?? file.name),
          accountSnapshot: String(file.account ?? ''),
          authIndex: String(file.authIndex ?? ''),
          provider: 'codex',
          disabled: false,
          action: 'reauth',
          actionReason: 'expired',
          actionStatus: 'pending',
          statusCode: 401,
          usedPercent: undefined,
          isQuota: false,
          createdAtMs: 2_000,
        },
      ],
    };
    const firstRenderer = await renderAccountsPage();
    await act(async () => {
      findHostButtonByText(firstRenderer, 'accounts.tab_health').props.onClick();
    });
    await act(async () => {
      mocks.lastHealthWorkspaceProps?.onSnapshotChange(
        makeInspectionSnapshot(
          [file],
          [
            {
              action: 'reauth',
              actionStatus: 'pending',
              statusCode: 401,
              usedPercent: undefined,
            },
          ],
          2_000
        )
      );
    });
    await act(async () => {
      findHostButtonByText(firstRenderer, 'accounts.tab_accounts').props.onClick();
    });
    expect(getAccountCardText(firstRenderer, getAuthFileSelectionKey(file))).toContain(
      'accounts.health_reauth'
    );

    act(() => {
      mocks.lastAuthFilesDataOptions?.onCredentialMutation?.({
        kind: 'source-files-changed',
        fileNames: [file.name],
      });
    });
    expect(getAccountCardText(firstRenderer, getAuthFileSelectionKey(file))).not.toContain(
      'accounts.health_reauth'
    );
    mocks.listCodexInspectionRuns.mockResolvedValue({ items: [staleRun] });
    mocks.getCodexInspectionRun.mockResolvedValue(staleDetail);
    await act(async () => {
      firstRenderer.unmount();
    });
    mountedAccountsRenderers.delete(firstRenderer);

    const secondRenderer = await renderAccountsPage();
    await flushPromises();
    expect(getAccountCardText(secondRenderer, getAuthFileSelectionKey(file))).not.toContain(
      'accounts.health_reauth'
    );
  });

  it('does not reattach filename-only inspection evidence when a shared file becomes singular after reauth', async () => {
    const first = {
      ...makeCodexFile('shared-codex.json', 'auth-1', 'first@example.com'),
      account_id: 'space-first',
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    const second = {
      ...makeCodexFile('shared-codex.json', 'auth-2', 'second@example.com'),
      account_id: 'space-second',
    } as AuthFileItem;
    mocks.files = [first, second];
    const renderer = await renderAccountsPage();

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    mocks.loadFiles.mockImplementationOnce(async () => {
      mocks.files = [
        { ...first, last_refresh: 3_000, modified: 3_100, status: 'ready', statusMessage: '' },
        second,
      ] as AuthFileItem[];
      return mocks.files;
    });
    await act(async () => {
      mocks.lastHealthWorkspaceProps?.onSnapshotChange(
        makeInspectionSnapshot(
          [first],
          [
            {
              authIndex: undefined,
              accountSnapshot: undefined,
              action: 'reauth',
              actionStatus: 'pending',
              statusCode: 401,
              usedPercent: undefined,
            },
          ],
          2_000
        )
      );
      await runInspectionCodexReauth({
        account: 'first@example.com',
        fileName: first.name,
        provider: 'codex',
        authIndex: first.authIndex,
        accountId: 'space-first',
        accountSnapshot: 'first@example.com',
      });
    });

    const replacement = makeCodexFile(first.name, 'auth-after-reauth', 'replacement@example.com');
    mocks.files = [replacement];
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });

    expect(getAccountCardText(renderer, getAuthFileSelectionKey(replacement))).not.toContain(
      'accounts.health_reauth'
    );
  });

  it('preserves exact sibling evidence when a shared file becomes singular after reauth', async () => {
    const first = {
      ...makeCodexFile('shared-codex.json', 'auth-1', 'first@example.com'),
      account_id: 'space-first',
      last_refresh: 1_000,
      modified: 1_100,
    } as AuthFileItem;
    const second = {
      ...makeCodexFile('shared-codex.json', 'auth-2', 'second@example.com'),
      account_id: 'space-second',
    } as AuthFileItem;
    const fallbackAtMs = 2_000;
    const inspectionAtMs = 3_000;
    const exactOperationalAtMs = 4_000;
    const fallbackCandidate = {
      id: 1,
      actionType: 'review',
      status: 'pending',
      provider: 'codex',
      authFileName: first.name,
      reason: 'stale filename fallback',
      firstSeenAtMs: fallbackAtMs,
      lastSeenAtMs: fallbackAtMs,
      hitCount: 1,
      createdAtMs: fallbackAtMs,
      updatedAtMs: fallbackAtMs,
    };
    const exactCandidate = {
      id: 2,
      actionType: 'review',
      status: 'pending',
      provider: 'codex',
      authFileName: second.name,
      authIndex: String(second.authIndex ?? ''),
      reason: 'exact sibling evidence',
      firstSeenAtMs: exactOperationalAtMs,
      lastSeenAtMs: exactOperationalAtMs,
      hitCount: 1,
      createdAtMs: exactOperationalAtMs,
      updatedAtMs: exactOperationalAtMs,
    };
    const fallbackCooldown = {
      authFileName: first.name,
      disabledAtMs: fallbackAtMs,
      recoverAtMs: exactOperationalAtMs + 10_000,
    };
    const exactCooldown = {
      authFileName: second.name,
      authIndex: String(second.authIndex ?? ''),
      disabledAtMs: exactOperationalAtMs,
      recoverAtMs: exactOperationalAtMs + 20_000,
    };
    mocks.files = [first, second];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: true,
    };
    mocks.listAccountActionCandidates.mockResolvedValue({
      pendingCount: 2,
      items: [fallbackCandidate, exactCandidate],
    });
    mocks.getActiveQuotaCooldowns.mockResolvedValue([fallbackCooldown, exactCooldown]);
    const renderer = await renderAccountsPage();
    await flushPromises();

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    mocks.loadFiles.mockImplementationOnce(async () => {
      mocks.files = [
        second,
        { ...first, last_refresh: 5_000, modified: 5_100, status: 'ready', statusMessage: '' },
      ] as AuthFileItem[];
      return mocks.files;
    });
    await act(async () => {
      mocks.lastHealthWorkspaceProps?.onSnapshotChange(
        makeInspectionSnapshot(
          [second, first],
          [
            {
              action: 'keep',
              statusCode: 200,
              usedPercent: 30,
              createdAtMs: inspectionAtMs,
            },
            {
              runtimeId: undefined,
              authIndex: undefined,
              accountSnapshot: undefined,
              action: 'reauth',
              actionStatus: 'pending',
              statusCode: 401,
              usedPercent: undefined,
              createdAtMs: fallbackAtMs,
            },
          ],
          inspectionAtMs
        )
      );
      await runInspectionCodexReauth({
        account: 'first@example.com',
        fileName: first.name,
        provider: 'codex',
        authIndex: first.authIndex,
        accountId: 'space-first',
        accountSnapshot: 'first@example.com',
      });
    });

    mocks.files = [second];
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });

    expect(getAccountCardText(renderer, getAuthFileSelectionKey(second))).not.toContain(
      'accounts.health_reauth'
    );
    await act(async () => {
      findDetailButtonByName(renderer, second.name).props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    expect(renderer.root.findByType(AccountQuotaTab).props.detailView.quota.cooldown).toEqual(
      exactCooldown
    );
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_diagnostics').props.onClick();
    });
    const detailView = renderer.root.findByType(AccountDiagnosticsTab).props.detailView;
    expect(detailView.strategy.inspectionFields).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ key: 'action', value: 'accounts.action_keep' }),
        expect.objectContaining({ key: 'createdAtMs', value: inspectionAtMs }),
      ])
    );
    expect(detailView.strategy.actionCandidates).toEqual([
      expect.objectContaining({ id: exactCandidate.id, reason: exactCandidate.reason }),
    ]);

    mocks.listAccountActionCandidates.mockResolvedValue({
      pendingCount: 1,
      items: [fallbackCandidate],
    });
    mocks.getActiveQuotaCooldowns.mockResolvedValue([fallbackCooldown]);
    await act(async () => {
      await mocks.lastHealthWorkspaceProps?.onCredentialsChanged();
    });
    await flushPromises();

    expect(
      renderer.root.findByType(AccountDiagnosticsTab).props.detailView.strategy.actionCandidates
    ).toEqual([]);
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    expect(renderer.root.findByType(AccountQuotaTab).props.detailView.quota.cooldown).toBeNull();
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

  it('renders xAI monthly billing and pay-as-you-go fallback on account cards', async () => {
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
    const selectionKey = getAuthFileSelectionKey(mocks.files[0]);
    const card = findAccountCardByKey(renderer, selectionKey);
    const quotaRegion = findAccountDetailRegion(renderer, selectionKey, 'quota');
    const text = readText(card);

    expect(text).not.toContain('accounts.quota_details_only');
    expect(text).not.toContain('accounts.quota_source_none');
    expect(text).toContain('30D');
    expect(text).toContain('PAYG');
    expect(quotaRegion.props['aria-label']).toContain('xai_quota.monthly_credits');
    expect(quotaRegion.props['aria-label']).toContain('xai_quota.pay_as_you_go_label');

    await act(async () => {
      quotaRegion.props.onClick();
    });

    const otherQuotaGroup = renderer.root.findByProps({ 'data-quota-window-group': 'other' });
    expect(readText(otherQuotaGroup)).toContain('xai_quota.monthly_credits');
    expect(readText(otherQuotaGroup)).toContain('xai_quota.pay_as_you_go_label');
    expect(renderer.root.findAllByProps({ 'data-quota-window-group': 'standard' })).toHaveLength(0);
  });

  it('uses each xAI fallback window remaining percent for its quota bar color', async () => {
    const file = {
      name: 'xai-divergent-fallback.json',
      type: 'xai',
      provider: 'xai',
      authIndex: 'xai-divergent-fallback-1',
      account: 'xai-divergent@example.com',
      priority: 0,
      disabled: false,
    } as AuthFileItem;
    mocks.files = [file];
    mocks.quotaState.xaiQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      billing: {
        monthlyLimitCents: 10_000,
        usedCents: 9_000,
        includedUsedCents: 9_000,
        onDemandCapCents: 5_000,
        onDemandUsedCents: 500,
        onDemandUsedPercent: 10,
        billingPeriodEnd: '2026-07-31T00:00:00Z',
        usedPercent: 90,
      },
    });

    const renderer = await renderAccountsPage();
    const card = findAccountCardByKey(renderer, getAuthFileSelectionKey(file));

    expect(findQuotaBarByWindow(card, 'billing').props.className).toContain('quotaBarWarn');
    expect(findQuotaBarByWindow(card, 'pay-as-you-go').props.className).toContain('quotaBarGood');
  });

  it('keeps retained xAI fallback windows visible with error-tone bars after refresh failure', async () => {
    const file = {
      name: 'xai-retained-error.json',
      type: 'xai',
      provider: 'xai',
      authIndex: 'xai-retained-error-1',
      account: 'xai-retained-error@example.com',
      priority: 0,
      disabled: false,
    } as AuthFileItem;
    mocks.files = [file];
    mocks.quotaState.xaiQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'error',
      error: 'refresh failed',
      errorStatus: 500,
      failedAtMs: Date.now(),
      billing: {
        monthlyLimitCents: 10_000,
        usedCents: 2_000,
        includedUsedCents: 2_000,
        onDemandCapCents: 5_000,
        onDemandUsedCents: 500,
        onDemandUsedPercent: 10,
        billingPeriodEnd: '2026-07-31T00:00:00Z',
        usedPercent: 20,
      },
    });

    const renderer = await renderAccountsPage();
    const card = findAccountCardByKey(renderer, getAuthFileSelectionKey(file));

    expect(readText(card)).toContain('30D');
    expect(readText(card)).toContain('PAYG');
    expect(readText(card)).not.toContain('accounts.quota_details_only');
    expect(findQuotaBarByWindow(card, 'billing').props.className).toContain('quotaBarBad');
    expect(findQuotaBarByWindow(card, 'pay-as-you-go').props.className).toContain('quotaBarBad');
  });

  it('keeps healthy xAI fallback bars independent from an exhausted hidden product quota', async () => {
    const file = {
      name: 'xai-hidden-product-exhausted.json',
      type: 'xai',
      provider: 'xai',
      authIndex: 'xai-hidden-product-exhausted-1',
      account: 'xai-hidden-product@example.com',
      priority: 0,
      disabled: false,
    } as AuthFileItem;
    mocks.files = [file];
    mocks.quotaState.xaiQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      billing: {
        periodType: 'monthly',
        usagePercent: 20,
        periodStart: '2026-08-01T00:00:00Z',
        periodEnd: '2026-09-01T00:00:00Z',
        productUsage: [{ product: 'Grok Code Fast', usagePercent: 100 }],
        monthlyLimitCents: 10_000,
        usedCents: 2_000,
        includedUsedCents: 2_000,
        onDemandCapCents: 5_000,
        onDemandUsedCents: 500,
        onDemandUsedPercent: 10,
        billingPeriodStart: '2026-08-01T00:00:00Z',
        billingPeriodEnd: '2026-09-01T00:00:00Z',
        usedPercent: 20,
      },
    });

    const renderer = await renderAccountsPage();
    const selectionKey = getAuthFileSelectionKey(file);
    const card = findAccountCardByKey(renderer, selectionKey);
    const quotaRegion = findAccountDetailRegion(renderer, selectionKey, 'quota');

    expect(readText(card)).toContain('30D');
    expect(readText(card)).toContain('PAYG');
    expect(readText(card)).not.toContain('Grok Code Fast');
    expect(findQuotaBarByWindow(card, 'billing').props.className).toContain('quotaBarGood');
    expect(findQuotaBarByWindow(card, 'pay-as-you-go').props.className).toContain('quotaBarGood');

    await act(async () => {
      quotaRegion.props.onClick();
    });
    await flushPromises();

    expect(readText(renderer.root.findByProps({ 'data-quota-window-group': 'other' }))).toContain(
      'Grok Code Fast'
    );
  });

  it('keeps a fixed xAI billing period in detail standard mode while showing billing and PAYG fallbacks', async () => {
    const file = {
      name: 'xai-fixed-billing.json',
      type: 'xai',
      provider: 'xai',
      authIndex: 'xai-fixed-billing-1',
      account: 'xai-fixed-billing@example.com',
      priority: 0,
      disabled: false,
    } as AuthFileItem;
    mocks.files = [file];
    mocks.quotaState.xaiQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      billing: {
        periodType: 'monthly',
        usagePercent: 20,
        periodStart: '2026-08-01T00:00:00Z',
        periodEnd: '2026-09-01T00:00:00Z',
        productUsage: [{ product: 'Grok Code Fast', usagePercent: 20 }],
        monthlyLimitCents: 10_000,
        usedCents: 2_000,
        includedUsedCents: 2_000,
        onDemandCapCents: 5_000,
        onDemandUsedCents: 1_750,
        onDemandUsedPercent: 35,
        billingPeriodStart: '2026-08-01T00:00:00Z',
        billingPeriodEnd: '2026-09-01T00:00:00Z',
        usedPercent: 20,
      },
    });

    const renderer = await renderAccountsPage();
    const selectionKey = getAuthFileSelectionKey(file);
    const card = findAccountCardByKey(renderer, selectionKey);
    const quotaRegion = findAccountDetailRegion(renderer, selectionKey, 'quota');
    const cardText = readText(card);
    expect(cardText).not.toContain('accounts.quota_details_only');
    expect(cardText).not.toContain('accounts.quota_source_none');
    expect(cardText).toContain('30D');
    expect(cardText).toContain('PAYG');
    expect(cardText).not.toContain('Grok Code Fast');
    expect(quotaRegion.props['aria-label']).toContain('xai_quota.monthly_credits');
    expect(quotaRegion.props['aria-label']).toContain('xai_quota.pay_as_you_go_label');

    await act(async () => {
      findAccountDetailRegion(renderer, selectionKey, 'quota').props.onClick();
    });
    await flushPromises();

    const standardGroup = renderer.root.findByProps({ 'data-quota-window-group': 'standard' });
    const otherGroup = renderer.root.findByProps({ 'data-quota-window-group': 'other' });
    expect(standardGroup.findAllByType(QuotaWindowCard)).toHaveLength(1);
    expect(standardGroup.findByProps({ 'data-quota-card-mode': 'standard' })).toBeTruthy();
    expect(standardGroup.findByProps({ 'data-quota-standard-comparison': 'true' })).toBeTruthy();
    expect(readText(otherGroup)).toContain('xai_quota.monthly_credits');
    expect(readText(otherGroup)).toContain('Grok Code Fast');
  });

  it('keeps xAI weekly credits standard-first without adding monthly or PAYG windows', async () => {
    const file = {
      name: 'xai-weekly.json',
      type: 'xai',
      provider: 'xai',
      authIndex: 'xai-weekly-1',
      account: 'xai-weekly@example.com',
      priority: 0,
      disabled: false,
    } as AuthFileItem;
    mocks.files = [file];
    mocks.quotaState.xaiQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      billing: {
        periodType: 'weekly',
        usagePercent: 20,
        periodStart: '2026-08-31T00:00:00Z',
        periodEnd: '2026-09-07T00:00:00Z',
        monthlyLimitCents: 10_000,
        usedCents: 2_000,
        includedUsedCents: 2_000,
        onDemandCapCents: 5_000,
        onDemandUsedCents: 1_750,
        onDemandUsedPercent: 35,
        billingPeriodStart: '2026-09-01T00:00:00Z',
        billingPeriodEnd: '2026-10-01T00:00:00Z',
        usedPercent: 20,
      },
    });

    const renderer = await renderAccountsPage();
    const selectionKey = getAuthFileSelectionKey(file);
    const card = findAccountCardByKey(renderer, selectionKey);
    const quotaRegion = findAccountDetailRegion(renderer, selectionKey, 'quota');

    expect(readText(card)).toContain('7D');
    expect(readText(card)).not.toContain('30D');
    expect(readText(card)).not.toContain('PAYG');
    expect(quotaRegion.props['aria-label']).toContain('xai_quota.weekly_credits');
    expect(quotaRegion.props['aria-label']).not.toContain('xai_quota.monthly_credits');
  });

  it('renders Kimi summary-only quota on the account card', async () => {
    const file = {
      name: 'kimi-summary.json',
      type: 'kimi',
      provider: 'kimi',
      authIndex: 'kimi-summary-1',
      account: 'kimi-summary@example.com',
      priority: 0,
      disabled: false,
    } as AuthFileItem;
    mocks.files = [file];
    const rows = buildKimiQuotaRows(
      {
        usage: {
          used: 20,
          limit: 100,
        },
      },
      { observedAtMs: Date.parse('2026-07-29T10:00:00Z') }
    );
    mocks.quotaState.kimiQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      rows,
    });

    const renderer = await renderAccountsPage();
    const selectionKey = getAuthFileSelectionKey(file);
    const card = findAccountCardByKey(renderer, selectionKey);
    const quotaRegion = findAccountDetailRegion(renderer, selectionKey, 'quota');

    expect(readText(card)).toContain('7D');
    expect(readText(card)).not.toContain('accounts.quota_details_only');
    expect(quotaRegion.props['aria-label']).toContain('kimi_quota.weekly_limit');

    await act(async () => {
      quotaRegion.props.onClick();
    });
    await flushPromises();

    expect(renderer.root.findAllByType(QuotaWindowCard)).toHaveLength(1);
  });

  it('keeps Kimi scoped usage summaries in quota details', async () => {
    const file = {
      name: 'kimi-scoped-summary.json',
      type: 'kimi',
      provider: 'kimi',
      authIndex: 'kimi-scoped-summary-1',
      account: 'kimi-scoped-summary@example.com',
      priority: 0,
      disabled: false,
    } as AuthFileItem;
    const rows = buildKimiQuotaRows(
      {
        usages: [
          {
            scope: 'FEATURE_CHAT',
            detail: { used: 20, limit: 100 },
          },
        ],
      },
      { observedAtMs: Date.parse('2026-07-29T10:00:00Z') }
    );
    expect(rows[0]?.id).toBe('usage-0-summary');
    mocks.files = [file];
    mocks.quotaState.kimiQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      rows,
    });

    const renderer = await renderAccountsPage();
    const selectionKey = getAuthFileSelectionKey(file);
    const card = findAccountCardByKey(renderer, selectionKey);
    const quotaRegion = findAccountDetailRegion(renderer, selectionKey, 'quota');

    expect(readText(card)).toContain('accounts.quota_details_only');
    expect(quotaRegion.props['aria-label']).toContain('accounts.quota_details_only');

    await act(async () => {
      quotaRegion.props.onClick();
    });
    await flushPromises();

    expect(renderer.root.findAllByType(QuotaWindowCard)).toHaveLength(1);
    expect(readText(renderer.root)).toContain('kimi_quota.scoped_weekly_limit');
  });

  it('keeps Claude model-scoped-only quota in quota details', async () => {
    const file = {
      name: 'claude-model-only.json',
      type: 'claude',
      provider: 'claude',
      authIndex: 'claude-model-only-1',
      account: 'claude-model-only@example.com',
      priority: 0,
      disabled: false,
    } as AuthFileItem;
    mocks.files = [file];
    mocks.quotaState.claudeQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      windows: [
        {
          id: 'opus-model',
          label: 'Opus model quota',
          usedPercent: 70,
          resetLabel: 'later',
          resetAtMs: Date.now() + 7 * 24 * 60 * 60 * 1000,
          limitWindowSeconds: null,
          modelScope: { kind: 'models', models: ['claude-opus-4-1'], complete: true },
        },
      ],
    });

    const renderer = await renderAccountsPage();
    const selectionKey = getAuthFileSelectionKey(file);
    const card = findAccountCardByKey(renderer, selectionKey);
    const quotaRegion = findAccountDetailRegion(renderer, selectionKey, 'quota');

    expect(readText(card)).toContain('accounts.quota_details_only');
    expect(quotaRegion.props['aria-label']).toContain('accounts.quota_details_only');

    await act(async () => {
      quotaRegion.props.onClick();
    });
    await flushPromises();

    expect(renderer.root.findAllByType(QuotaWindowCard)).toHaveLength(1);
    expect(readText(renderer.root)).toContain('Opus model quota');
  });

  it('keeps Kimi standard windows ahead of summary data', async () => {
    const file = {
      name: 'kimi-standard.json',
      type: 'kimi',
      provider: 'kimi',
      authIndex: 'kimi-standard-1',
      account: 'kimi-standard@example.com',
      priority: 0,
      disabled: false,
    } as AuthFileItem;
    mocks.files = [file];
    mocks.quotaState.kimiQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      rows: [
        {
          id: 'summary',
          label: 'Kimi quota summary',
          used: 20,
          limit: 100,
          resetAtMs: null,
          limitWindowSeconds: null,
        },
        {
          id: 'five-hour',
          label: 'Five Hour Limit',
          used: 30,
          limit: 100,
          resetAtMs: Date.now() + 4 * 60 * 60 * 1000,
          limitWindowSeconds: 5 * 60 * 60,
        },
      ],
    });

    const renderer = await renderAccountsPage();
    const selectionKey = getAuthFileSelectionKey(file);
    const card = findAccountCardByKey(renderer, selectionKey);

    expect(readText(card)).toContain('5H');
    expect(readText(card)).not.toContain('SUM');
    expect(readText(card)).not.toContain('accounts.quota_details_only');
  });

  it('renders Antigravity Pro model groups in the quota matrix and keeps them in quota details', async () => {
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
    const card = findAccountCardByKey(renderer, getAuthFileSelectionKey(mocks.files[0]));
    const quotaRegion = findAccountDetailRegion(
      renderer,
      getAuthFileSelectionKey(mocks.files[0]),
      'quota'
    );
    card.findByProps({
      'data-account-quota-matrix': getAuthFileSelectionKey(mocks.files[0]),
    });
    expect(card.findAllByProps({ 'data-account-quota-matrix-row': 'five_hour' })).toHaveLength(1);
    expect(card.findAllByProps({ 'data-account-quota-matrix-row': 'weekly' })).toHaveLength(1);
    expect(
      card.findAll((node) => typeof node.props['data-account-quota-matrix-cell'] === 'string')
    ).toHaveLength(4);
    expect(readText(card)).not.toContain('accounts.quota_details_only');
    expect(quotaRegion.props['aria-label']).toContain('Claude 5H 11%');
    expect(quotaRegion.props['aria-label']).toContain('Gemini 7D 4%');

    await act(async () => {
      findAccountDetailRegion(
        renderer,
        getAuthFileSelectionKey(mocks.files[0]),
        'quota'
      ).props.onClick();
    });

    const modelQuotaGroup = renderer.root.findByProps({ 'data-quota-window-group': 'model' });
    expect(modelQuotaGroup.findAllByType(QuotaWindowCard)).toHaveLength(4);
    expect(readText(modelQuotaGroup)).toContain('antigravity_quota.group_claude_gpt_models');
    expect(readText(modelQuotaGroup)).toContain('antigravity_quota.group_gemini_models');
    expect(renderer.root.findAllByProps({ 'data-quota-window-group': 'standard' })).toHaveLength(0);
  });

  it('renders Antigravity Free model groups in the quota matrix and keeps them in quota details', async () => {
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
    const card = findAccountCardByKey(renderer, getAuthFileSelectionKey(mocks.files[0]));
    card.findByProps({
      'data-account-quota-matrix': getAuthFileSelectionKey(mocks.files[0]),
    });
    expect(card.findAllByProps({ 'data-account-quota-matrix-row': 'five_hour' })).toHaveLength(0);
    expect(card.findAllByProps({ 'data-account-quota-matrix-row': 'weekly' })).toHaveLength(1);
    expect(
      card.findAll((node) => typeof node.props['data-account-quota-matrix-cell'] === 'string')
    ).toHaveLength(2);
    expect(readText(card)).not.toContain('accounts.quota_details_only');

    await act(async () => {
      findAccountDetailRegion(
        renderer,
        getAuthFileSelectionKey(mocks.files[0]),
        'quota'
      ).props.onClick();
    });

    const modelQuotaGroup = renderer.root.findByProps({ 'data-quota-window-group': 'model' });
    expect(modelQuotaGroup.findAllByType(QuotaWindowCard)).toHaveLength(2);
    expect(readText(modelQuotaGroup)).toContain('antigravity_quota.group_claude_gpt_models');
    expect(readText(modelQuotaGroup)).toContain('antigravity_quota.group_gemini_models');
    expect(renderer.root.findAllByProps({ 'data-quota-window-group': 'standard' })).toHaveLength(0);
  });

  it('keeps a retained Antigravity matrix visible with error-tone bars after refresh failure', async () => {
    const file = {
      name: 'antigravity-retained-error.json',
      type: 'antigravity',
      provider: 'antigravity',
      authIndex: 'antigravity-retained-error-01',
      account: 'AG Retained Error',
      label: 'Antigravity Retained Error',
      priority: 0,
      disabled: false,
    } as AuthFileItem;
    mocks.files = [file];
    mocks.quotaState.antigravityQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'error',
      error: 'refresh failed',
      errorStatus: 500,
      failedAtMs: Date.now(),
      subscription: { plan: 'pro', tierName: 'Pro', tierId: 'g1-pro' },
      groups: [
        {
          id: 'gemini-models',
          label: 'Gemini Models',
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
              remainingFraction: 0.8,
              resetTime: '2026-07-15T12:00:00Z',
            },
          ],
        },
        {
          id: 'claude-gpt-models',
          label: 'Claude and GPT models',
          buckets: [
            {
              id: 'claude-5h',
              label: 'Five Hour Limit',
              window: '5h',
              remainingFraction: 0.6,
              resetTime: '2026-07-09T11:00:00Z',
            },
            {
              id: 'claude-weekly',
              label: 'Weekly Limit',
              window: 'weekly',
              remainingFraction: 0.4,
              resetTime: '2026-07-13T12:00:00Z',
            },
          ],
        },
      ],
    });

    const renderer = await renderAccountsPage();
    const selectionKey = getAuthFileSelectionKey(file);
    const card = findAccountCardByKey(renderer, selectionKey);
    const quotaRegion = findAccountDetailRegion(renderer, selectionKey, 'quota');

    expect(card.findAllByProps({ 'data-account-quota-matrix': selectionKey })).toHaveLength(1);
    expect(readText(card)).toContain('5H');
    expect(readText(card)).toContain('7D');
    expect(readText(card)).toContain('Gemini');
    expect(readText(card)).toContain('Claude');
    const matrixCells = card.findAll(
      (node) => typeof node.props['data-account-quota-matrix-cell'] === 'string'
    );
    expect(matrixCells).toHaveLength(4);
    matrixCells.forEach((cell) => {
      expect(
        findQuotaBarByMatrixCell(card, cell.props['data-account-quota-matrix-cell'] as string).props
          .className
      ).toContain('quotaBarBad');
    });

    await act(async () => {
      quotaRegion.props.onClick();
    });
    await flushPromises();

    expect(renderer.root.findByProps({ 'data-quota-window-group': 'model' })).toBeTruthy();
    expect(renderer.root.findAllByType(QuotaWindowCard)).toHaveLength(4);
  });

  it('renders a single Antigravity model group through the ordinary quota fallback', async () => {
    const file = {
      name: 'antigravity-single-group.json',
      type: 'antigravity',
      provider: 'antigravity',
      authIndex: 'antigravity-single-group-01',
      account: 'AG Single Group',
      label: 'Antigravity Single Group',
      priority: 0,
      disabled: false,
    } as AuthFileItem;
    mocks.files = [file];
    mocks.quotaState.antigravityQuota = buildCredentialScopedQuotaRecord(file, {
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
      ],
    });

    const renderer = await renderAccountsPage();
    const selectionKey = getAuthFileSelectionKey(file);
    const card = findAccountCardByKey(renderer, selectionKey);
    const quotaRegion = findAccountDetailRegion(renderer, selectionKey, 'quota');

    expect(card.findAllByProps({ 'data-account-quota-matrix': selectionKey })).toHaveLength(0);
    expect(readText(card)).toContain('Gemini');
    expect(readText(card)).toContain('7D');
    expect(readText(card)).not.toContain('accounts.quota_details_only');
    expect(quotaRegion.props['aria-label']).toContain('76%');

    await act(async () => {
      quotaRegion.props.onClick();
    });

    expect(renderer.root.findByProps({ 'data-quota-window-group': 'model' })).toBeTruthy();
    expect(
      renderer.root
        .findByProps({ 'data-quota-window-group': 'model' })
        .findAllByType(QuotaWindowCard)
    ).toHaveLength(1);
  });

  it('renders incomplete Antigravity matrix windows through the ordinary fallback', async () => {
    const file = {
      name: 'antigravity-incomplete-matrix.json',
      type: 'antigravity',
      provider: 'antigravity',
      authIndex: 'antigravity-incomplete-matrix-01',
      account: 'AG Incomplete Matrix',
      priority: 0,
      disabled: false,
    } as AuthFileItem;
    mocks.files = [file];
    mocks.quotaState.antigravityQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      subscription: { plan: 'pro', tierName: 'Pro', tierId: 'g1-pro' },
      groups: [
        {
          id: 'gemini-models',
          label: 'Gemini Models',
          buckets: [
            {
              id: 'gemini-5h',
              label: 'Five Hour Limit',
              window: '5h',
              remainingFraction: 0.1,
              resetTime: '2026-07-09T12:00:00Z',
            },
          ],
        },
        {
          id: 'claude-gpt-models',
          label: 'Claude and GPT models',
          buckets: [
            {
              id: 'claude-weekly',
              label: 'Weekly Limit',
              window: 'weekly',
              remainingFraction: 0.8,
              resetTime: '2026-07-13T12:00:00Z',
            },
          ],
        },
      ],
    });

    const renderer = await renderAccountsPage();
    const selectionKey = getAuthFileSelectionKey(file);
    const card = findAccountCardByKey(renderer, selectionKey);

    expect(card.findAllByProps({ 'data-account-quota-matrix': selectionKey })).toHaveLength(0);
    expect(readText(card)).toContain('Gemini');
    expect(readText(card)).toContain('Claude');
    expect(readText(card)).toContain('5H');
    expect(readText(card)).toContain('7D');
    expect(readText(card)).not.toContain('accounts.quota_details_only');
    expect(findQuotaBarByWindow(card, 'gemini-models:gemini-5h').props.className).toContain(
      'quotaBarWarn'
    );
    expect(findQuotaBarByWindow(card, 'claude-gpt-models:claude-weekly').props.className).toContain(
      'quotaBarGood'
    );

    await act(async () => {
      findAccountDetailRegion(renderer, selectionKey, 'quota').props.onClick();
    });

    const modelQuotaGroup = renderer.root.findByProps({ 'data-quota-window-group': 'model' });
    expect(modelQuotaGroup.findAllByType(QuotaWindowCard)).toHaveLength(2);
  });

  it('prioritizes an Antigravity matrix when standard and model-family windows coexist', async () => {
    const file = {
      name: 'antigravity-mixed-standard.json',
      type: 'antigravity',
      provider: 'antigravity',
      authIndex: 'antigravity-mixed-standard-01',
      account: 'AG Mixed Standard',
      priority: 0,
      disabled: false,
    } as AuthFileItem;
    const nowMs = Date.now();
    const makeMatrixWindow = (
      key: string,
      kind: 'five_hour' | 'weekly',
      groupLabel: string,
      remainingPercent: number
    ) =>
      buildAccountQuotaDisplayWindow({
        key,
        label: kind === 'five_hour' ? 'Five Hour Limit' : 'Weekly Limit',
        kind,
        remainingPercent,
        usedPercent: 100 - remainingPercent,
        resetLabel: 'later',
        resetAtMs: nowMs + 6 * 60 * 60 * 1000,
        limitWindowSeconds: kind === 'five_hour' ? 5 * 60 * 60 : 7 * 24 * 60 * 60,
        groupLabel,
        modelScope: {
          kind: 'family',
          key: groupLabel.includes('Gemini') ? 'gemini' : 'claude_gpt',
          complete: true,
        },
        source: 'antigravity',
        windowMode: 'fixed',
        nowMs,
      });
    const standardWindow = buildAccountQuotaDisplayWindow({
      key: 'account-weekly',
      label: 'Account Weekly',
      kind: 'weekly',
      remainingPercent: 70,
      usedPercent: 30,
      resetLabel: 'later',
      resetAtMs: nowMs + 6 * 24 * 60 * 60 * 1000,
      limitWindowSeconds: 7 * 24 * 60 * 60,
      modelScope: { kind: 'all', complete: true },
      source: 'antigravity',
      windowMode: 'fixed',
      nowMs,
    });
    mocks.files = [file];
    mocks.quotaDisplayWindowsOverride = [
      standardWindow,
      makeMatrixWindow('gemini-5h', 'five_hour', 'Gemini Models', 96),
      makeMatrixWindow('gemini-weekly', 'weekly', 'Gemini Models', 4),
      makeMatrixWindow('claude-5h', 'five_hour', 'Claude and GPT models', 11),
      makeMatrixWindow('claude-weekly', 'weekly', 'Claude and GPT models', 19),
    ];

    const renderer = await renderAccountsPage();
    const selectionKey = getAuthFileSelectionKey(file);
    const card = findAccountCardByKey(renderer, selectionKey);
    const quotaRegion = findAccountDetailRegion(renderer, selectionKey, 'quota');

    expect(card.findAllByProps({ 'data-account-quota-matrix': selectionKey })).toHaveLength(1);
    expect(card.findAllByProps({ 'data-account-quota-matrix-row': 'five_hour' })).toHaveLength(1);
    expect(card.findAllByProps({ 'data-account-quota-matrix-row': 'weekly' })).toHaveLength(1);
    expect(quotaRegion.props['aria-label']).toContain('Claude 5H 11%');
    expect(quotaRegion.props['aria-label']).not.toContain('Account Weekly: 70%');

    await act(async () => {
      quotaRegion.props.onClick();
    });

    expect(
      renderer.root
        .findByProps({ 'data-quota-window-group': 'standard' })
        .findAllByType(QuotaWindowCard)
    ).toHaveLength(1);
    expect(
      renderer.root
        .findByProps({ 'data-quota-window-group': 'model' })
        .findAllByType(QuotaWindowCard)
    ).toHaveLength(4);
  });

  it('keeps an Antigravity matrix non-interactive while selection mode is active', async () => {
    const file = {
      name: 'antigravity-selection-matrix.json',
      type: 'antigravity',
      provider: 'antigravity',
      authIndex: 'antigravity-selection-matrix-01',
      account: 'AG Selection Matrix',
      priority: 0,
      disabled: false,
    } as AuthFileItem;
    const nowMs = Date.now();
    const makeMatrixWindow = (key: string, kind: 'five_hour' | 'weekly', groupLabel: string) =>
      buildAccountQuotaDisplayWindow({
        key,
        label: kind === 'five_hour' ? 'Five Hour Limit' : 'Weekly Limit',
        kind,
        remainingPercent: 60,
        usedPercent: 40,
        resetLabel: 'later',
        resetAtMs: nowMs + 6 * 60 * 60 * 1000,
        limitWindowSeconds: kind === 'five_hour' ? 5 * 60 * 60 : 7 * 24 * 60 * 60,
        groupLabel,
        modelScope: {
          kind: 'family',
          key: groupLabel.includes('Gemini') ? 'gemini' : 'claude_gpt',
          complete: true,
        },
        source: 'antigravity',
        windowMode: 'fixed',
        nowMs,
      });
    mocks.files = [file];
    mocks.quotaDisplayWindowsOverride = [
      makeMatrixWindow('gemini-5h', 'five_hour', 'Gemini Models'),
      makeMatrixWindow('gemini-weekly', 'weekly', 'Gemini Models'),
      makeMatrixWindow('claude-5h', 'five_hour', 'Claude and GPT models'),
      makeMatrixWindow('claude-weekly', 'weekly', 'Claude and GPT models'),
    ];

    const renderer = await renderAccountsPage();
    const selectionKey = getAuthFileSelectionKey(file);
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.selection_mode_enter').props.onClick();
    });

    const card = findAccountCardByKey(renderer, selectionKey);
    const quotaRegion = findAccountDetailRegion(renderer, selectionKey, 'quota');
    expect(quotaRegion.type).toBe('div');
    expect(quotaRegion.props['data-account-detail-trigger']).toBeUndefined();
    expect(quotaRegion.props.onClick).toBeUndefined();
    expect(card.findByProps({ 'data-account-quota-matrix': selectionKey })).toBeTruthy();

    await act(async () => {
      card.props.onClick();
    });

    expect(mocks.toggleSelect).toHaveBeenCalledWith(selectionKey);
    expect(renderer.root.findAllByType(AccountQuotaTab)).toHaveLength(0);
    expect(mocks.navigate).not.toHaveBeenCalled();
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
    expect(treeText(renderer)).toContain('accounts.quota_source_none');
    expect(treeText(renderer)).not.toContain('accounts.quota_details_only');
    expect(treeText(renderer)).not.toContain('SUM');
  });

  it('opens the quota detail from the full historical usage region', async () => {
    const file = mocks.files[0];
    const selectionKey = getAuthFileSelectionKey(file);
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getAccountHistory.mockResolvedValue(
      makeAccountHistoryResponse([
        {
          row_key: selectionKey,
          account_key: 'codex@example.com',
          matched: true,
          total_requests: 12,
          success_calls: 10,
          failure_calls: 2,
          total_tokens: 1_200,
          total_cost: 0.12,
          success_rate: 10 / 12,
          first_seen_ms: 1,
          last_seen_ms: 2,
          sync_status: 'ready',
        },
      ])
    );

    const renderer = await renderAccountsPage();
    await flushPromises();

    const historyRegion = findAccountDetailRegion(renderer, selectionKey, 'history');
    const quotaRegion = findAccountDetailRegion(renderer, selectionKey, 'quota');
    expect(historyRegion.type).toBe('button');
    expect(historyRegion.props['data-account-detail-trigger']).toBe('history');
    const historyLabel = historyRegion.props['aria-label'] as string;
    expect(historyLabel).toContain('accounts.list_header_historical_usage');
    expect(historyLabel).toContain('accounts.history_title:12:1,200:$0.12:83.33%');
    expect(historyLabel).toContain('accounts.open_detail:codex.json');
    expect(historyLabel).toContain('accounts.detail_tab_quota');
    expect(quotaRegion.props['aria-label']).not.toBe(historyRegion.props['aria-label']);
    expect(historyRegion.findAllByType('div')).toHaveLength(0);
    expect(readText(historyRegion)).toContain('12');

    await act(async () => {
      historyRegion.props.onClick();
      await Promise.resolve();
    });
    await flushPromises();

    expect(renderer.root.findByType(AccountQuotaTab)).toBeTruthy();
    expect(findHostButtonByText(renderer, 'accounts.detail_tab_quota').props['aria-selected']).toBe(
      true
    );
    expect(mocks.navigate).toHaveBeenCalledWith(
      {
        pathname: '/accounts',
        search: `?account=${encodeURIComponent(selectionKey)}&tab=quota`,
      },
      { replace: true }
    );
  });

  it('opens the quota detail from the full quota information region', async () => {
    const file = mocks.files[0];
    const selectionKey = getAuthFileSelectionKey(file);
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      windows: [
        makeCodexQuotaWindow({
          resetAtMs: Date.now() + 5 * 60 * 60 * 1000 - 60_000,
        }),
      ],
    });

    const renderer = await renderAccountsPage();
    const quotaRegion = findAccountDetailRegion(renderer, selectionKey, 'quota');
    const historyRegion = findAccountDetailRegion(renderer, selectionKey, 'history');

    expect(quotaRegion.type).toBe('button');
    expect(quotaRegion.props['data-account-detail-trigger']).toBe('quota');
    const quotaLabel = quotaRegion.props['aria-label'] as string;
    expect(quotaLabel).toContain('accounts.list_header_quota');
    expect(quotaLabel).toContain('Five hours');
    expect(quotaLabel).toContain('80%');
    expect(quotaLabel).toContain('accounts.open_detail:codex.json');
    expect(quotaLabel).toContain('accounts.detail_tab_quota');
    expect(quotaLabel).not.toBe(historyRegion.props['aria-label']);
    expect(quotaRegion.findAllByType('div')).toHaveLength(0);
    expect(readText(quotaRegion)).toContain('5H');

    await act(async () => {
      quotaRegion.props.onClick();
      await Promise.resolve();
    });
    await flushPromises();

    expect(renderer.root.findByType(AccountQuotaTab)).toBeTruthy();
    expect(findHostButtonByText(renderer, 'accounts.detail_tab_quota').props['aria-selected']).toBe(
      true
    );
    expect(mocks.navigate).toHaveBeenCalledWith(
      {
        pathname: '/accounts',
        search: `?account=${encodeURIComponent(selectionKey)}&tab=quota`,
      },
      { replace: true }
    );
  });

  it('keeps an empty historical usage region openable', async () => {
    const selectionKey = getAuthFileSelectionKey(mocks.files[0]);
    const renderer = await renderAccountsPage();
    const historyRegion = findAccountDetailRegion(renderer, selectionKey, 'history');

    expect(readText(historyRegion)).toContain('-');
    expect(historyRegion.props.disabled).not.toBe(true);
    expect(historyRegion.props['aria-label']).toContain('accounts.history_empty');

    await act(async () => {
      historyRegion.props.onClick();
      await Promise.resolve();
    });
    await flushPromises();

    expect(renderer.root.findByType(AccountQuotaTab)).toBeTruthy();
  });

  it('keeps a details-only quota region openable when only model quota exists', async () => {
    const file = mocks.files[0];
    const selectionKey = getAuthFileSelectionKey(file);
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      windows: [
        makeCodexQuotaWindow({
          id: 'spark-model',
          label: 'Spark model quota',
          resetAtMs: Date.now() + 5 * 60 * 60 * 1000 - 60_000,
          modelScope: { kind: 'family', key: 'codex_spark', complete: true },
        }),
      ],
    });

    const renderer = await renderAccountsPage();
    const card = findAccountCardByKey(renderer, selectionKey);
    const quotaRegion = findAccountDetailRegion(renderer, selectionKey, 'quota');

    expect(readText(card)).toContain('accounts.quota_details_only');
    expect(readText(card)).not.toContain('accounts.quota_source_none');
    expect(readText(card)).not.toContain('Spark model quota');
    expect(quotaRegion.props['aria-label']).toContain('accounts.quota_details_only');

    await act(async () => {
      quotaRegion.props.onClick();
      await Promise.resolve();
    });
    await flushPromises();

    expect(renderer.root.findAllByProps({ 'data-quota-window-group': 'standard' })).toHaveLength(0);
    expect(renderer.root.findByProps({ 'data-quota-window-group': 'model' })).toBeTruthy();
    expect(renderer.root.findByType(AccountQuotaTab)).toBeTruthy();
  });

  it('uses card selection instead of opening details for both shortcut regions in selection mode', async () => {
    const selectionKey = getAuthFileSelectionKey(mocks.files[0]);
    const renderer = await renderAccountsPage();

    await act(async () => {
      findHostButtonByText(renderer, 'accounts.selection_mode_enter').props.onClick();
    });

    const card = findAccountCardByKey(renderer, selectionKey);
    const historyRegion = findAccountDetailRegion(renderer, selectionKey, 'history');
    const quotaRegion = findAccountDetailRegion(renderer, selectionKey, 'quota');
    expect(historyRegion.type).toBe('div');
    expect(quotaRegion.type).toBe('div');
    expect(historyRegion.props['data-account-detail-trigger']).toBeUndefined();
    expect(quotaRegion.props['data-account-detail-trigger']).toBeUndefined();

    await act(async () => {
      card.props.onClick();
      card.props.onClick();
    });

    expect(mocks.toggleSelect).toHaveBeenNthCalledWith(1, selectionKey);
    expect(mocks.toggleSelect).toHaveBeenNthCalledWith(2, selectionKey);
    expect(renderer.root.findAllByType(AccountQuotaTab)).toHaveLength(0);
    expect(mocks.navigate).not.toHaveBeenCalled();
  });

  it('keeps shortcut navigation behind the existing dirty configuration guard', async () => {
    const selectionKey = getAuthFileSelectionKey(mocks.files[0]);
    mocks.configurationDirty = true;
    mocks.location = {
      pathname: '/accounts',
      search: `?account=${encodeURIComponent(selectionKey)}&tab=config`,
    };
    const renderer = await renderAccountsPage();
    const quotaRegion = findAccountDetailRegion(renderer, selectionKey, 'quota');

    await act(async () => {
      quotaRegion.props.onClick();
      await Promise.resolve();
    });

    expect(mocks.showConfirmation).toHaveBeenCalledTimes(1);
    expect(mocks.navigate).not.toHaveBeenCalled();
    expect(
      findHostButtonByText(renderer, 'accounts.detail_tab_config').props['aria-selected']
    ).toBe(true);

    const firstConfirmation = mocks.showConfirmation.mock.calls[0]?.[0] as {
      onCancel: () => void;
    };
    await act(async () => {
      firstConfirmation.onCancel();
      await Promise.resolve();
    });
    await flushPromises();
    expect(mocks.navigate).not.toHaveBeenCalled();
    expect(renderer.root.findAllByType(AccountQuotaTab)).toHaveLength(0);

    await act(async () => {
      quotaRegion.props.onClick();
      await Promise.resolve();
    });
    const secondConfirmation = mocks.showConfirmation.mock.calls[1]?.[0] as {
      onConfirm: () => void;
    };
    await act(async () => {
      secondConfirmation.onConfirm();
      await Promise.resolve();
    });
    await flushPromises();

    expect(mocks.configurationReset).toHaveBeenCalledTimes(1);
    expect(renderer.root.findByType(AccountQuotaTab)).toBeTruthy();
    expect(mocks.navigate).toHaveBeenCalledWith(
      {
        pathname: '/accounts',
        search: `?account=${encodeURIComponent(selectionKey)}&tab=quota`,
      },
      { replace: true }
    );
  });

  it('shows every standard window while keeping model and other quota in the detail tab', async () => {
    const file = mocks.files[0];
    const selectionKey = getAuthFileSelectionKey(file);
    const nowMs = Date.now();
    const standardWindow = (
      id: string,
      label: string,
      limitWindowSeconds: number,
      usedPercent: number
    ) =>
      makeCodexQuotaWindow({
        id,
        label,
        limitWindowSeconds,
        usedPercent,
        resetAtMs: nowMs + limitWindowSeconds * 1000 - 60_000,
        modelScope: CODEX_MAIN_SCOPE,
      });
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success',
      windows: [
        standardWindow('five-hour', 'Five hours', 5 * 60 * 60, 20),
        standardWindow('weekly', 'Weekly', 7 * 24 * 60 * 60, 30),
        standardWindow('monthly', 'Monthly', 30 * 24 * 60 * 60, 40),
        makeCodexQuotaWindow({
          id: 'spark-model',
          label: 'Spark model quota',
          resetAtMs: nowMs + 5 * 60 * 60 * 1000 - 60_000,
          modelScope: { kind: 'family', key: 'codex_spark', complete: true },
        }),
        makeCodexQuotaWindow({
          id: 'billing',
          label: 'Billing credits',
          usedPercent: 10,
          resetLabel: '-',
          resetAtMs: null,
          limitWindowSeconds: null,
          modelScope: { kind: 'all', complete: true },
        }),
      ],
    });

    const renderer = await renderAccountsPage();
    const cardText = getAccountCardText(renderer, selectionKey);
    expect(cardText).toContain('5H');
    expect(cardText).toContain('7D');
    expect(cardText).toContain('30D');
    expect(cardText).not.toContain('Spark model quota');
    expect(cardText).not.toContain('Billing credits');
    expect(cardText).not.toMatch(/\+\d+/);

    await act(async () => {
      findAccountDetailRegion(renderer, selectionKey, 'quota').props.onClick();
      await Promise.resolve();
    });
    await flushPromises();

    const standardGroup = renderer.root.findByProps({ 'data-quota-window-group': 'standard' });
    const modelGroup = renderer.root.findByProps({ 'data-quota-window-group': 'model' });
    const otherGroup = renderer.root.findByProps({ 'data-quota-window-group': 'other' });
    expect(standardGroup.findAllByType(QuotaWindowCard)).toHaveLength(3);
    expect(modelGroup.findAllByType(QuotaWindowCard)).toHaveLength(1);
    expect(otherGroup.findAllByType(QuotaWindowCard)).toHaveLength(1);
    expect(readText(standardGroup)).toContain('Five hours');
    expect(readText(standardGroup)).toContain('Weekly');
    expect(readText(standardGroup)).toContain('Monthly');
    expect(readText(modelGroup)).toContain('Spark model quota');
    expect(readText(otherGroup)).toContain('Billing credits');
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
      '$0.75'
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

  it('allows a disabled credential to request its latest quota', async () => {
    const file = {
      ...makeCodexFile('codex-disabled.json', 'auth-disabled', 'disabled@example.com'),
      disabled: true,
    } as AuthFileItem;
    mocks.files = [file];
    const quotaFetch = vi.spyOn(CODEX_CONFIG, 'fetchQuota').mockResolvedValue(makeCodexQuotaData());

    const renderer = await renderAccountsPage();
    const refreshButton = findAccountCardButtonByAriaLabel(
      renderer,
      getAuthFileSelectionKey(file),
      'accounts.refresh_quota'
    );
    expect(refreshButton.props.disabled).toBe(false);

    await act(async () => {
      await refreshButton.props.onClick();
    });

    expect(quotaFetch).toHaveBeenCalledTimes(1);
    expect(quotaFetch).toHaveBeenCalledWith(file, expect.anything(), expect.anything());
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

  it('ignores a targeted history result after the account is removed and recreated', async () => {
    const file = {
      ...makeCodexFile('generic-recreated.json', 'auth-recreated', 'recreated@example.com'),
      type: 'generic',
      provider: 'generic',
    } as AuthFileItem;
    const stableFile = {
      ...makeCodexFile('generic-stable.json', 'auth-stable', 'stable@example.com'),
      type: 'generic',
      provider: 'generic',
    } as AuthFileItem;
    const rowKey = 'generic-recreated.json\u0000auth-recreated';
    mocks.files = [file, stableFile];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getAccountHistory.mockResolvedValue(makeAccountHistoryResponse([]));

    const renderer = await renderAccountsPage();
    await flushPromises();
    await act(async () => {
      findDetailButtonByName(renderer, file.name).props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    await flushPromises();

    const staleHistory = createDeferred<AccountHistoryResponseForTest>();
    const currentHistory = createDeferred<AccountHistoryResponseForTest>();
    mocks.getAccountHistory.mockClear();
    mocks.getAccountHistory
      .mockImplementationOnce(() => staleHistory.promise)
      .mockResolvedValueOnce(makeAccountHistoryResponse([]))
      .mockImplementationOnce(() => currentHistory.promise);

    await act(async () => {
      void renderer.root.findByType(AccountQuotaTab).props.onRefreshHistory();
      await Promise.resolve();
    });
    expect(mocks.getAccountHistory).toHaveBeenCalledTimes(1);

    mocks.files = [stableFile];
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await flushPromises();

    mocks.files = [{ ...file }, stableFile];
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await flushPromises();
    expect(mocks.getAccountHistory).toHaveBeenCalledTimes(3);

    staleHistory.resolve(
      makeAccountHistoryResponse([
        {
          row_key: rowKey,
          account_key: 'stale-recreated-account',
          matched: true,
          total_requests: 777,
          success_calls: 0,
          failure_calls: 777,
          total_tokens: 7_777,
          total_cost: 7.77,
          success_rate: 0,
          first_seen_ms: 1,
          last_seen_ms: 2,
          sync_status: 'ready',
          latest_request: {
            timestamp_ms: 2,
            failed: true,
            fail_status_code: 401,
            fail_summary: 'stale unauthorized',
          },
        },
      ])
    );
    await flushPromises();

    const recreatedCardText = readText(findAccountCardByKey(renderer, rowKey));
    expect(recreatedCardText).not.toContain('777');
    expect(recreatedCardText).not.toContain('accounts.health_reauth');
    expect(recreatedCardText).not.toContain('stale unauthorized');

    currentHistory.resolve(makeAccountHistoryResponse([]));
    await flushPromises();
  });

  it('ignores a targeted history failure after the account is removed and recreated', async () => {
    const file = {
      ...makeCodexFile('generic-recreated.json', 'auth-recreated', 'recreated@example.com'),
      type: 'generic',
      provider: 'generic',
    } as AuthFileItem;
    const stableFile = {
      ...makeCodexFile('generic-stable.json', 'auth-stable', 'stable@example.com'),
      type: 'generic',
      provider: 'generic',
    } as AuthFileItem;
    const rowKey = 'generic-recreated.json\u0000auth-recreated';
    mocks.files = [file, stableFile];
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.getAccountHistory.mockResolvedValue(makeAccountHistoryResponse([]));

    const renderer = await renderAccountsPage();
    await flushPromises();
    await act(async () => {
      findDetailButtonByName(renderer, file.name).props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    await flushPromises();

    const staleHistory = createDeferred<AccountHistoryResponseForTest>();
    const currentHistory = createDeferred<AccountHistoryResponseForTest>();
    mocks.getAccountHistory.mockClear();
    mocks.getAccountHistory
      .mockImplementationOnce(() => staleHistory.promise)
      .mockResolvedValueOnce(makeAccountHistoryResponse([]))
      .mockImplementationOnce(() => currentHistory.promise);

    await act(async () => {
      void renderer.root.findByType(AccountQuotaTab).props.onRefreshHistory();
      await Promise.resolve();
    });

    mocks.files = [stableFile];
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await flushPromises();

    mocks.files = [{ ...file }, stableFile];
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await flushPromises();
    expect(mocks.getAccountHistory).toHaveBeenCalledTimes(3);

    staleHistory.reject(new Error('stale history offline'));
    await flushPromises();

    const recreatedCardText = readText(findAccountCardByKey(renderer, rowKey));
    expect(recreatedCardText).not.toContain('accounts.history_unavailable');
    expect(recreatedCardText).not.toContain('accounts.history_recent_fallback');
    expect(recreatedCardText).not.toContain('stale history offline');

    currentHistory.resolve(makeAccountHistoryResponse([]));
    await flushPromises();
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

  it('isolates overlapping quota refresh batches when the CPA connection changes', async () => {
    const firstFile = makeCodexFile('codex-a.json', 'auth-a', 'a@example.com');
    mocks.files = [firstFile];
    const firstQuota = createDeferred<CodexQuotaData>();
    const secondQuota = createDeferred<CodexQuotaData>();
    const quotaFetch = vi
      .spyOn(CODEX_CONFIG, 'fetchQuota')
      .mockReturnValueOnce(firstQuota.promise)
      .mockReturnValueOnce(secondQuota.promise);
    const renderer = await renderAccountsPage();
    let firstRefresh!: Promise<void>;
    let secondRefresh!: Promise<void>;

    await act(async () => {
      firstRefresh = findButtonByText(renderer, 'accounts.refresh_quota').props.onClick();
      await Promise.resolve();
    });
    expect(quotaFetch).toHaveBeenNthCalledWith(
      1,
      firstFile,
      expect.any(Function),
      expect.objectContaining({ apiBase: 'http://cpa-a.local:8317' })
    );

    const secondFile = makeCodexFile('codex-b.json', 'auth-b', 'b@example.com');
    mocks.files = [secondFile];
    mocks.apiBase = 'http://cpa-b.local:8317';
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    expect(findButtonByText(renderer, 'accounts.refresh_quota').props.loading).toBe(false);

    await act(async () => {
      secondRefresh = findButtonByText(renderer, 'accounts.refresh_quota').props.onClick();
      await Promise.resolve();
    });
    expect(quotaFetch).toHaveBeenNthCalledWith(
      2,
      secondFile,
      expect.any(Function),
      expect.objectContaining({ apiBase: 'http://cpa-b.local:8317' })
    );
    expect(findButtonByText(renderer, 'accounts.refresh_quota').props.loading).toBe(true);

    await act(async () => {
      firstQuota.resolve(makeCodexQuotaData());
      await firstRefresh;
    });
    expect(findButtonByText(renderer, 'accounts.refresh_quota').props.loading).toBe(true);
    expect(mocks.showNotification).not.toHaveBeenCalledWith(
      'accounts.quota_refresh_result',
      expect.anything()
    );

    await act(async () => {
      secondQuota.resolve(makeCodexQuotaData());
      await secondRefresh;
    });
    expect(findButtonByText(renderer, 'accounts.refresh_quota').props.loading).toBe(false);
    expect(mocks.showNotification).toHaveBeenCalledTimes(1);
    expect(mocks.showNotification).toHaveBeenCalledWith('accounts.quota_refresh_result', 'success');
  });

  it('uses a healthy manual quota refresh to clear older inspection and operational evidence', async () => {
    const file = mocks.files[0];
    const selectionKey = getAuthFileSelectionKey(file);
    const staleServerEvidenceAtMs = Date.now() + 5 * 60_000;
    Object.assign(file, {
      statusMessage: 'token_expired',
      errorStatus: 401,
      statusCode: 401,
      updatedAtMs: Date.now() - 60_000,
    });
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: true,
    };
    mocks.getActiveQuotaCooldowns.mockResolvedValue([
      {
        authFileName: file.name,
        authIndex: String(file.authIndex ?? ''),
        disabledAtMs: staleServerEvidenceAtMs,
        recoverAtMs: staleServerEvidenceAtMs + 10_000,
      },
    ]);
    mocks.listAccountActionCandidates.mockResolvedValue({
      pendingCount: 1,
      items: [
        {
          id: 1,
          actionType: 'disable',
          status: 'pending',
          provider: 'codex',
          authFileName: file.name,
          authIndex: String(file.authIndex ?? ''),
          reason: 'old credential evidence',
          firstSeenAtMs: staleServerEvidenceAtMs,
          lastSeenAtMs: staleServerEvidenceAtMs,
          hitCount: 1,
          createdAtMs: staleServerEvidenceAtMs,
          updatedAtMs: staleServerEvidenceAtMs,
        },
      ],
    });
    installCodexQuotaStoreMutationMock();
    vi.spyOn(CODEX_CONFIG, 'fetchQuota').mockResolvedValue({
      ...makeCodexQuotaData(),
      windows: [
        {
          id: 'weekly',
          label: 'Weekly',
          usedPercent: 30,
          resetLabel: 'next week',
          resetAtMs: 20_000,
          resetAccuracy: 'exact',
          limitWindowSeconds: 604_800,
        },
      ],
    });
    const renderer = await renderAccountsPage();
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    await act(async () => {
      mocks.lastHealthWorkspaceProps?.onSnapshotChange(
        makeInspectionSnapshot(
          [file],
          [
            {
              action: 'reauth',
              actionStatus: 'pending',
              statusCode: 401,
              usedPercent: undefined,
            },
          ],
          staleServerEvidenceAtMs
        )
      );
      await mocks.lastHealthWorkspaceProps?.onCredentialsChanged();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });
    await act(async () => {
      findDetailButtonByName(renderer, file.name).props.onClick();
    });
    await flushPromises();
    expect(renderer.root.findAllByProps({ 'data-overview-section': 'attention' })).toHaveLength(1);

    await act(async () => {
      await findAccountCardButtonByAriaLabel(
        renderer,
        selectionKey,
        'accounts.refresh_quota'
      ).props.onClick();
    });
    await flushPromises();

    const cardText = getAccountCardText(renderer, selectionKey);
    expect(cardText).toContain('accounts.health_available');
    expect(cardText).not.toContain('accounts.health_reauth');
    expect(renderer.root.findAllByProps({ 'data-overview-section': 'attention' })).toHaveLength(0);
    expect(treeText(renderer)).not.toContain('accounts.detail_overview_basis_cooldown');
  });

  it('uses an exhausted manual quota refresh to clear reauth evidence and preserve quota cooldown', async () => {
    const file = mocks.files[0];
    const selectionKey = getAuthFileSelectionKey(file);
    const staleServerEvidenceAtMs = Date.now() + 5 * 60_000;
    Object.assign(file, {
      statusMessage: 'token_expired',
      errorStatus: 401,
      statusCode: 401,
      updatedAtMs: Date.now() - 60_000,
    });
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: true,
    };
    const cooldown = {
      authFileName: file.name,
      authIndex: String(file.authIndex ?? ''),
      disabledAtMs: staleServerEvidenceAtMs,
      recoverAtMs: staleServerEvidenceAtMs + 10_000,
    };
    mocks.getActiveQuotaCooldowns.mockResolvedValue([cooldown]);
    mocks.listAccountActionCandidates.mockResolvedValue({
      pendingCount: 2,
      items: [
        {
          id: 1,
          actionType: 'reauth',
          status: 'pending',
          provider: 'codex',
          authFileName: file.name,
          authIndex: String(file.authIndex ?? ''),
          reason: 'old credential evidence',
          firstSeenAtMs: staleServerEvidenceAtMs,
          lastSeenAtMs: staleServerEvidenceAtMs,
          hitCount: 1,
          createdAtMs: staleServerEvidenceAtMs,
          updatedAtMs: staleServerEvidenceAtMs,
        },
        {
          id: 2,
          actionType: 'disable',
          status: 'pending',
          provider: 'codex',
          authFileName: file.name,
          authIndex: String(file.authIndex ?? ''),
          reason: 'quota remains exhausted',
          firstSeenAtMs: staleServerEvidenceAtMs,
          lastSeenAtMs: staleServerEvidenceAtMs,
          hitCount: 1,
          createdAtMs: staleServerEvidenceAtMs,
          updatedAtMs: staleServerEvidenceAtMs,
        },
      ],
    });
    installCodexQuotaStoreMutationMock();
    vi.spyOn(CODEX_CONFIG, 'fetchQuota').mockResolvedValue({
      ...makeCodexQuotaData(),
      windows: [
        {
          id: 'weekly',
          label: 'Weekly',
          usedPercent: 100,
          resetLabel: 'next week',
          resetAtMs: 20_000,
          resetAccuracy: 'exact',
          limitWindowSeconds: 604_800,
        },
      ],
    });
    const renderer = await renderAccountsPage();
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    await act(async () => {
      mocks.lastHealthWorkspaceProps?.onSnapshotChange(
        makeInspectionSnapshot(
          [file],
          [
            {
              action: 'reauth',
              actionStatus: 'pending',
              statusCode: 401,
              usedPercent: undefined,
            },
          ],
          staleServerEvidenceAtMs
        )
      );
      await mocks.lastHealthWorkspaceProps?.onCredentialsChanged();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });

    expect(getAccountCardText(renderer, selectionKey)).toContain('accounts.health_reauth');

    await act(async () => {
      await findAccountCardButtonByAriaLabel(
        renderer,
        selectionKey,
        'accounts.refresh_quota'
      ).props.onClick();
    });
    await flushPromises();

    const cardText = getAccountCardText(renderer, selectionKey);
    expect(cardText).toContain('accounts.health_weekly_cooldown');
    expect(cardText).not.toContain('accounts.health_available');
    expect(cardText).not.toContain('accounts.health_reauth');

    await act(async () => {
      findDetailButtonByName(renderer, file.name).props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    const quotaDetailView = renderer.root.findByType(AccountQuotaTab).props.detailView;
    expect(quotaDetailView.quota.statusLabelKey).toBe('accounts.quota_status_exhausted');
    expect(quotaDetailView.quota.windows).toEqual(
      expect.arrayContaining([expect.objectContaining({ usedPercent: 100 })])
    );
    expect(quotaDetailView.quota.cooldown).toEqual(cooldown);
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_diagnostics').props.onClick();
    });
    expect(
      renderer.root.findByType(AccountDiagnosticsTab).props.detailView.strategy.actionCandidates
    ).toEqual([expect.objectContaining({ actionType: 'disable' })]);
  });

  it('uses action-domain timestamps when an exhausted refresh supersedes authentication evidence', async () => {
    const file = mocks.files[0];
    const selectionKey = getAuthFileSelectionKey(file);
    const now = Date.now();
    const oldReauthAtMs = now + 5 * 60_000;
    const newReauthAtMs = now + 7 * 60_000;
    const disableAtMs = now + 10 * 60_000;
    mocks.location = {
      pathname: '/accounts',
      search: `?account=${encodeURIComponent(selectionKey)}&tab=diagnostics`,
    };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: true,
    };
    const makeCandidate = (id: number, actionType: 'reauth' | 'disable', observedAtMs: number) => ({
      id,
      actionType,
      status: 'pending',
      provider: 'codex',
      authFileName: file.name,
      authIndex: String(file.authIndex ?? ''),
      reason: `${actionType}-${id}`,
      firstSeenAtMs: observedAtMs,
      lastSeenAtMs: observedAtMs,
      hitCount: 1,
      createdAtMs: observedAtMs,
      updatedAtMs: observedAtMs,
    });
    const disableCandidate = makeCandidate(6, 'disable', disableAtMs);
    mocks.listAccountActionCandidates.mockResolvedValue({
      pendingCount: 2,
      items: [makeCandidate(5, 'reauth', oldReauthAtMs), disableCandidate],
    });
    installCodexQuotaStoreMutationMock();
    vi.spyOn(CODEX_CONFIG, 'fetchQuota').mockResolvedValue({
      ...makeCodexQuotaData(),
      windows: [
        {
          id: 'weekly',
          label: 'Weekly',
          usedPercent: 100,
          resetLabel: 'next week',
          resetAtMs: 20_000,
          resetAccuracy: 'exact',
          limitWindowSeconds: 604_800,
        },
      ],
    });
    const renderer = await renderAccountsPage();
    await flushPromises();

    await act(async () => {
      await findAccountCardButtonByAriaLabel(
        renderer,
        selectionKey,
        'accounts.refresh_quota'
      ).props.onClick();
    });
    await flushPromises();

    const newReauthCandidate = makeCandidate(7, 'reauth', newReauthAtMs);
    mocks.listAccountActionCandidates.mockResolvedValue({
      pendingCount: 2,
      items: [newReauthCandidate, disableCandidate],
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_health').props.onClick();
    });
    await act(async () => {
      await mocks.lastHealthWorkspaceProps?.onCredentialsChanged();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_diagnostics').props.onClick();
    });

    expect(
      renderer.root.findByType(AccountDiagnosticsTab).props.detailView.strategy.actionCandidates
    ).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ id: newReauthCandidate.id, actionType: 'reauth' }),
        expect.objectContaining({ id: disableCandidate.id, actionType: 'disable' }),
      ])
    );
  });

  it('turns a manual quota 401 into a reauth state', async () => {
    const file = mocks.files[0];
    installCodexQuotaStoreMutationMock();
    vi.spyOn(CODEX_CONFIG, 'fetchQuota').mockRejectedValue(
      Object.assign(new Error('unauthorized'), { status: 401 })
    );
    const renderer = await renderAccountsPage();

    await act(async () => {
      await findAccountCardButtonByAriaLabel(
        renderer,
        getAuthFileSelectionKey(file),
        'accounts.refresh_quota'
      ).props.onClick();
    });
    await flushPromises();

    expect(getAccountCardText(renderer, getAuthFileSelectionKey(file))).toContain(
      'accounts.health_reauth'
    );
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

  it('keeps the last successful Codex quota visible while a reset verification fails', async () => {
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
    const verifyResult = createDeferred<CodexQuotaData>();
    vi.spyOn(CODEX_CONFIG, 'fetchQuota').mockImplementation(() => verifyResult.promise);
    const renderer = await renderAccountsPage();

    await act(async () => {
      findDetailButtonByName(renderer, 'codex-reset.json').props.onClick();
    });
    await act(async () => {
      findDrawerMoreItem(renderer, 'reset-codex-quota').onClick();
    });

    expect(mocks.showConfirmation).not.toHaveBeenCalled();
    expect(mocks.consumeResetCredit).not.toHaveBeenCalled();
    expect(mocks.quotaState.setCodexQuota).not.toHaveBeenCalled();
    verifyResult.reject(new Error('verify unavailable'));
    await flushPromises();

    expect(mocks.quotaState.setCodexQuota).toHaveBeenCalledTimes(1);
    const updater = mocks.quotaState.setCodexQuota.mock.calls[0]?.[0] as (
      current: Record<string, CodexQuotaState>
    ) => Record<string, CodexQuotaState>;
    const failedState = updater(mocks.quotaState.codexQuota as Record<string, CodexQuotaState>)[
      storeKey
    ];
    expect(failedState.status).toBe('error');
    expect(failedState.windows).toEqual(previousQuota.windows);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'codex_quota.reset_verify_failed:reset@example.com:verify unavailable',
      'error'
    );
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
          total_requests: 1_234_567,
          success_calls: 1_218_000,
          failure_calls: 16_567,
          total_tokens: 1_000_190_000,
          total_cost: 12_345.67,
          success_rate: 0.98321,
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
    expect(cardText).toContain('1.2M');
    expect(cardText).toContain('1.0B');
    expect(cardText).toContain('$12.35K');
    expect(cardText).toContain('98.3%');
    expect(cardText).not.toContain('1000.2M');
    expect(
      renderer.root.findByProps({
        title: 'accounts.history_title:1,234,567:1,000,190,000:$12,345.67:98.32%',
      })
    ).toBeTruthy();
    const historyMetricAriaLabels = [
      'accounts.history_requests: 1,234,567',
      'accounts.history_tokens: 1,000,190,000',
      'accounts.history_cost: $12,345.67',
      'accounts.history_success: 98.32%',
    ];
    const historyMetrics = renderer.root.findAll((node) =>
      historyMetricAriaLabels.includes(node.props['aria-label'])
    );
    expect(historyMetrics.map((metric) => metric.props['aria-label'])).toEqual(
      historyMetricAriaLabels
    );
    historyMetrics.forEach((metric) => expect(metric.props).not.toHaveProperty('title'));
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
    expect(compactSummaryValues).toEqual(expect.arrayContaining(['1.2M', '1.0B']));
    const summaryTooltips = quotaSummary
      .findAll((node) => node.props.role === 'tooltip')
      .map((node) => readText(node));
    expect(summaryTooltips).toEqual(
      expect.arrayContaining([
        'accounts.detail_total_requests1,234,567',
        'accounts.detail_total_tokens1,000,190,000',
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
            modelScope: CODEX_MAIN_SCOPE,
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
            modelScope: CODEX_MAIN_SCOPE,
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
      serverCodexInspectionAvailable: true,
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
    expect(mocks.listCodexInspectionRuns).toHaveBeenCalledTimes(1);
    expect(mocks.getAccountWindowUsage).toHaveBeenCalledTimes(1);

    await act(async () => {
      await findButtonByText(renderer, 'common.refresh').props.onClick();
    });
    await flushPromises();

    expect(mocks.loadFiles).toHaveBeenCalledTimes(2);
    expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(2);
    expect(mocks.listCodexInspectionRuns).toHaveBeenCalledTimes(2);
    expect(mocks.listCodexInspectionRuns.mock.invocationCallOrder[1]).toBeLessThan(
      mocks.loadFiles.mock.invocationCallOrder[1]
    );
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
          modelScope: CODEX_MAIN_SCOPE,
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
          model: CODEX_MAIN_MODEL,
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
    vi.spyOn(CODEX_CONFIG, 'fetchQuota').mockResolvedValue(
      makeCodexQuotaData(1, [makeResetCredit('reset-credit-1')])
    );

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
    await flushPromises();
    expect(mocks.showConfirmation).toHaveBeenCalledWith(
      expect.objectContaining({ title: 'codex_quota.reset_confirm_title' })
    );
    expect(mocks.consumeResetCredit).not.toHaveBeenCalled();
  });

  it('keeps reset records visible but disables reset when no credits remain', async () => {
    mocks.quotaState.codexQuota = {
      'codex.json': {
        status: 'success',
        authFileKey: 'codex.json::auth-1',
        windows: [],
        rateLimitResetCreditsAvailableCount: 0,
        rateLimitResetCredits: [],
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

  it('enables reset when only the reset-credit list carries display evidence', async () => {
    mocks.quotaState.codexQuota = {
      'codex.json': {
        status: 'success',
        authFileKey: 'codex.json::auth-1',
        windows: [],
        rateLimitResetCredits: [makeResetCredit('list-only-credit-1')],
      },
    };

    const renderer = await renderAccountsPage();
    await openCodexQuotaTab(renderer, 'codex.json');

    const resetAction = renderer.root.findByProps({ 'data-quota-reset-action': 'true' });
    expect(resetAction.props.disabled).toBe(false);
  });

  it('enables reset from quota snapshot evidence when the live quota has no credits', async () => {
    mocks.quotaState.codexQuota = {
      'codex.json': {
        status: 'success',
        authFileKey: 'codex.json::auth-1',
        windows: [],
      },
    };
    vi.mocked(accountQuotaSnapshotApi.query).mockResolvedValueOnce({
      generated_at_ms: Date.now(),
      items: [
        {
          row_key: 'codex.json\u0000auth-1',
          account_key: 'codex.json\u0000auth-1',
          provider: 'codex',
          windows: [
            {
              provider_window_id: 'weekly',
              window_kind: 'weekly',
              window_mode: 'fixed',
              model_scope_kind: 'all',
              source: 'api_query',
              observed_at_ms: Date.now(),
              boundary_accuracy: 'derived',
              cycle_start_ms: Date.now() - 3_600_000,
              cycle_end_ms: Date.now() + 86_400_000,
              duration_seconds: 604_800,
              remaining_percent: 80,
              used_percent: 20,
              stale: false,
              reset_credits_available: 2,
              reset_credits: [{ id: 'snapshot-credit-1', expires_at_ms: Date.now() + 86_400_000 }],
            },
          ],
        },
      ],
    });

    const renderer = await renderAccountsPage();
    await openCodexQuotaTab(renderer, 'codex.json');

    const resetRecords = renderer.root.findAllByProps({
      'data-account-quota-reset-records': 'true',
    });
    expect(resetRecords).toHaveLength(1);
    const resetAction = renderer.root.findByProps({ 'data-quota-reset-action': 'true' });
    expect(resetAction.props.disabled).toBe(false);
  });

  it('verifies live reset credits before showing confirmation or consuming', async () => {
    mocks.quotaState.codexQuota = {
      'codex.json': {
        status: 'success',
        authFileKey: 'codex.json::auth-1',
        windows: [],
        rateLimitResetCreditsAvailableCount: 1,
      },
    };
    const verification = createDeferred<CodexQuotaData>();
    const fetchSpy = vi
      .spyOn(CODEX_CONFIG, 'fetchQuota')
      .mockImplementation(() => verification.promise);

    const renderer = await renderAccountsPage();
    await openCodexQuotaTab(renderer, 'codex.json');
    await act(async () => {
      renderer.root.findByProps({ 'data-quota-reset-action': 'true' }).props.onClick();
    });

    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(mocks.showConfirmation).not.toHaveBeenCalled();
    expect(mocks.consumeResetCredit).not.toHaveBeenCalled();
  });

  it('skips consume and refreshes the quota when fresh verification reports zero credits', async () => {
    mocks.quotaState.codexQuota = {
      'codex.json': {
        status: 'success',
        authFileKey: 'codex.json::auth-1',
        windows: [],
        rateLimitResetCreditsAvailableCount: 1,
      },
    };
    vi.spyOn(CODEX_CONFIG, 'fetchQuota').mockResolvedValue(makeCodexQuotaData(0));

    const renderer = await renderAccountsPage();
    await openCodexQuotaTab(renderer, 'codex.json');
    await act(async () => {
      renderer.root.findByProps({ 'data-quota-reset-action': 'true' }).props.onClick();
    });
    await flushPromises();

    expect(mocks.showConfirmation).not.toHaveBeenCalled();
    expect(mocks.consumeResetCredit).not.toHaveBeenCalled();
    const committed = applyCodexQuotaCommits();
    expect(committed['codex.json::auth-1'].status).toBe('success');
    expect(committed['codex.json::auth-1'].rateLimitResetCreditsAvailableCount).toBe(0);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'codex_quota.reset_no_credits:codex@example.com',
      'info'
    );
  });

  it('reports a verification failure when reset-credit availability is unknown', async () => {
    mocks.quotaState.codexQuota = {
      'codex.json': {
        status: 'success',
        authFileKey: 'codex.json::auth-1',
        windows: [],
        rateLimitResetCreditsAvailableCount: 1,
      },
    };
    vi.spyOn(CODEX_CONFIG, 'fetchQuota').mockResolvedValue({
      ...makeCodexQuotaData(null),
      rateLimitResetCreditsError: 'reset endpoint unavailable',
    });

    const renderer = await renderAccountsPage();
    await openCodexQuotaTab(renderer, 'codex.json');
    await act(async () => {
      renderer.root.findByProps({ 'data-quota-reset-action': 'true' }).props.onClick();
    });
    await flushPromises();

    expect(mocks.showConfirmation).not.toHaveBeenCalled();
    expect(mocks.consumeResetCredit).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'codex_quota.reset_verify_failed:codex@example.com:reset endpoint unavailable',
      'error'
    );
    expect(mocks.showNotification).not.toHaveBeenCalledWith(
      'codex_quota.reset_no_credits:codex@example.com',
      'info'
    );
  });

  it('confirms with the fresh verified count instead of the displayed count', async () => {
    mocks.quotaState.codexQuota = {
      'codex.json': {
        status: 'success',
        authFileKey: 'codex.json::auth-1',
        windows: [],
        rateLimitResetCreditsAvailableCount: 5,
      },
    };
    vi.spyOn(CODEX_CONFIG, 'fetchQuota').mockResolvedValue(
      makeCodexQuotaData(1, [makeResetCredit('fresh-credit-1')])
    );

    const renderer = await renderAccountsPage();
    await openCodexQuotaTab(renderer, 'codex.json');
    await act(async () => {
      renderer.root.findByProps({ 'data-quota-reset-action': 'true' }).props.onClick();
    });
    await flushPromises();

    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as {
      message: string;
      confirmText: string;
    };
    expect(confirmation.message).toBe('codex_quota.reset_confirm_message:codex@example.com:1');
    expect(confirmation.confirmText).toBe('codex_quota.reset_button:1');
  });

  it('consumes once and commits the refreshed quota after confirmation', async () => {
    mocks.quotaState.codexQuota = {
      'codex.json': {
        status: 'success',
        authFileKey: 'codex.json::auth-1',
        windows: [],
        rateLimitResetCreditsAvailableCount: 1,
      },
    };
    vi.spyOn(CODEX_CONFIG, 'fetchQuota')
      .mockResolvedValueOnce(makeCodexQuotaData(1, [makeResetCredit('fresh-credit-1')]))
      .mockResolvedValueOnce(makeCodexQuotaData(0));

    const renderer = await renderAccountsPage();
    await openCodexQuotaTab(renderer, 'codex.json');
    await act(async () => {
      renderer.root.findByProps({ 'data-quota-reset-action': 'true' }).props.onClick();
    });
    await flushPromises();
    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      await confirmation.onConfirm();
    });
    await flushPromises();

    expect(mocks.consumeResetCredit).toHaveBeenCalledTimes(1);
    const committed = applyCodexQuotaCommits();
    expect(committed['codex.json::auth-1'].status).toBe('success');
    expect(committed['codex.json::auth-1'].rateLimitResetCreditsAvailableCount).toBe(0);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'codex_quota.reset_success:codex@example.com',
      'success'
    );
  });

  it('runs a single verification when reset is clicked twice quickly', async () => {
    mocks.quotaState.codexQuota = {
      'codex.json': {
        status: 'success',
        authFileKey: 'codex.json::auth-1',
        windows: [],
        rateLimitResetCreditsAvailableCount: 1,
      },
    };
    const verification = createDeferred<CodexQuotaData>();
    const fetchSpy = vi
      .spyOn(CODEX_CONFIG, 'fetchQuota')
      .mockImplementation(() => verification.promise);

    const renderer = await renderAccountsPage();
    await openCodexQuotaTab(renderer, 'codex.json');
    const resetAction = renderer.root.findByProps({ 'data-quota-reset-action': 'true' });
    await act(async () => {
      resetAction.props.onClick();
      resetAction.props.onClick();
    });

    expect(fetchSpy).toHaveBeenCalledTimes(1);
    verification.resolve(makeCodexQuotaData(1, [makeResetCredit('fresh-credit-1')]));
    await flushPromises();
    expect(mocks.showConfirmation).toHaveBeenCalledTimes(1);
  });

  it('releases the reset lock when confirmation is cancelled so the credential can retry', async () => {
    mocks.quotaState.codexQuota = {
      'codex.json': {
        status: 'success',
        authFileKey: 'codex.json::auth-1',
        windows: [],
        rateLimitResetCreditsAvailableCount: 1,
      },
    };
    const fetchSpy = vi
      .spyOn(CODEX_CONFIG, 'fetchQuota')
      .mockResolvedValue(makeCodexQuotaData(1, [makeResetCredit('fresh-credit-1')]));

    const renderer = await renderAccountsPage();
    await openCodexQuotaTab(renderer, 'codex.json');
    await act(async () => {
      renderer.root.findByProps({ 'data-quota-reset-action': 'true' }).props.onClick();
    });
    await flushPromises();
    const firstConfirmation = mocks.showConfirmation.mock.calls[0]?.[0] as {
      onCancel: () => void;
    };
    await act(async () => {
      firstConfirmation.onCancel();
    });
    await act(async () => {
      renderer.root.findByProps({ 'data-quota-reset-action': 'true' }).props.onClick();
    });
    await flushPromises();

    expect(fetchSpy).toHaveBeenCalledTimes(2);
    expect(mocks.showConfirmation).toHaveBeenCalledTimes(2);
    expect(mocks.consumeResetCredit).not.toHaveBeenCalled();
  });

  it('consumes exactly once when the confirmation callback fires twice', async () => {
    mocks.quotaState.codexQuota = {
      'codex.json': {
        status: 'success',
        authFileKey: 'codex.json::auth-1',
        windows: [],
        rateLimitResetCreditsAvailableCount: 1,
      },
    };
    vi.spyOn(CODEX_CONFIG, 'fetchQuota')
      .mockResolvedValueOnce(makeCodexQuotaData(1, [makeResetCredit('fresh-credit-1')]))
      .mockResolvedValue(makeCodexQuotaData(0));

    const renderer = await renderAccountsPage();
    await openCodexQuotaTab(renderer, 'codex.json');
    await act(async () => {
      renderer.root.findByProps({ 'data-quota-reset-action': 'true' }).props.onClick();
    });
    await flushPromises();
    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      await confirmation.onConfirm();
    });
    await act(async () => {
      await confirmation.onConfirm();
    });
    await flushPromises();

    expect(mocks.consumeResetCredit).toHaveBeenCalledTimes(1);
  });

  it('shows a reset failure when consuming the credit fails', async () => {
    mocks.quotaState.codexQuota = {
      'codex.json': {
        status: 'success',
        authFileKey: 'codex.json::auth-1',
        windows: [],
        rateLimitResetCreditsAvailableCount: 1,
      },
    };
    vi.spyOn(CODEX_CONFIG, 'fetchQuota').mockResolvedValue(
      makeCodexQuotaData(1, [makeResetCredit('fresh-credit-1')])
    );
    mocks.consumeResetCredit.mockRejectedValueOnce(new Error('consume rejected'));

    const renderer = await renderAccountsPage();
    await openCodexQuotaTab(renderer, 'codex.json');
    await act(async () => {
      renderer.root.findByProps({ 'data-quota-reset-action': 'true' }).props.onClick();
    });
    await flushPromises();
    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      await confirmation.onConfirm();
    });
    await flushPromises();

    expect(mocks.showNotification).toHaveBeenCalledWith(
      'codex_quota.reset_failed:codex@example.com:consume rejected',
      'error'
    );
    const committed = applyCodexQuotaCommits();
    expect(committed['codex.json::auth-1'].rateLimitResetCreditsAvailableCount).toBe(1);
  });

  it('treats a failed post-reset refresh as a partial success without restoring credits', async () => {
    mocks.quotaState.codexQuota = {
      'codex.json': {
        status: 'success',
        authFileKey: 'codex.json::auth-1',
        windows: [],
        rateLimitResetCreditsAvailableCount: 1,
      },
    };
    vi.spyOn(CODEX_CONFIG, 'fetchQuota')
      .mockResolvedValueOnce(makeCodexQuotaData(1, [makeResetCredit('fresh-credit-1')]))
      .mockRejectedValueOnce(new Error('refresh failed'));

    const renderer = await renderAccountsPage();
    await openCodexQuotaTab(renderer, 'codex.json');
    await act(async () => {
      renderer.root.findByProps({ 'data-quota-reset-action': 'true' }).props.onClick();
    });
    await flushPromises();
    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      await confirmation.onConfirm();
    });
    await flushPromises();

    expect(mocks.showNotification).toHaveBeenCalledWith(
      'codex_quota.reset_partial_success:codex@example.com',
      'warning'
    );
    const failedResetCalls = mocks.showNotification.mock.calls.filter(
      (call) => typeof call[0] === 'string' && call[0].startsWith('codex_quota.reset_failed')
    );
    expect(failedResetCalls).toHaveLength(0);
    const committed = applyCodexQuotaCommits();
    expect(committed['codex.json::auth-1'].rateLimitResetCreditsAvailableCount).toBeNull();
    expect(committed['codex.json::auth-1'].rateLimitResetCredits).toEqual([]);
  });

  it('discards quota requests started before the reset transaction', async () => {
    mocks.quotaState.codexQuota = {
      'codex.json': {
        status: 'success',
        authFileKey: 'codex.json::auth-1',
        windows: [],
        rateLimitResetCreditsAvailableCount: 1,
      },
    };
    const staleQuotaResult = createDeferred<CodexQuotaData>();
    const verification = createDeferred<CodexQuotaData>();
    vi.spyOn(CODEX_CONFIG, 'fetchQuota')
      .mockImplementationOnce(() => staleQuotaResult.promise)
      .mockImplementationOnce(() => verification.promise)
      .mockResolvedValueOnce(makeCodexQuotaData(0));

    const renderer = await renderAccountsPage();
    await act(async () => {
      findDetailButtonByName(renderer, 'codex.json').props.onClick();
    });
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.refresh_quota').props.onClick();
    });
    await flushPromises();
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    await flushPromises();
    await act(async () => {
      renderer.root.findByProps({ 'data-quota-reset-action': 'true' }).props.onClick();
    });
    verification.resolve(makeCodexQuotaData(1, [makeResetCredit('fresh-credit-1')]));
    await flushPromises();
    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      await confirmation.onConfirm();
    });
    await flushPromises();

    staleQuotaResult.resolve(makeCodexQuotaData(5));
    await flushPromises();

    const committed = applyCodexQuotaCommits();
    expect(committed['codex.json::auth-1'].rateLimitResetCreditsAvailableCount).toBe(0);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'codex_quota.reset_success:codex@example.com',
      'success'
    );
  });

  it('discards quota refreshes started after verification but before consume completes', async () => {
    mocks.quotaState.codexQuota = {
      'codex.json': {
        status: 'success',
        authFileKey: 'codex.json::auth-1',
        windows: [],
        rateLimitResetCreditsAvailableCount: 1,
      },
    };
    const staleQuotaResult = createDeferred<CodexQuotaData>();
    vi.spyOn(CODEX_CONFIG, 'fetchQuota')
      .mockResolvedValueOnce(makeCodexQuotaData(1, [makeResetCredit('fresh-credit-1')]))
      .mockImplementationOnce(() => staleQuotaResult.promise)
      .mockResolvedValueOnce(makeCodexQuotaData(0));

    const renderer = await renderAccountsPage();
    await openCodexQuotaTab(renderer, 'codex.json');
    await act(async () => {
      renderer.root.findByProps({ 'data-quota-reset-action': 'true' }).props.onClick();
    });
    await flushPromises();
    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      void findHostButtonByText(renderer, 'accounts.refresh_quota').props.onClick();
      await Promise.resolve();
    });
    await act(async () => {
      await confirmation.onConfirm();
    });
    await flushPromises();

    staleQuotaResult.resolve(makeCodexQuotaData(1, [makeResetCredit('stale-credit-1')]));
    await flushPromises();

    expect(mocks.consumeResetCredit).toHaveBeenCalledTimes(1);
    const committed = applyCodexQuotaCommits();
    expect(committed['codex.json::auth-1'].rateLimitResetCreditsAvailableCount).toBe(0);
    expect(committed['codex.json::auth-1'].rateLimitResetCredits).toEqual([]);
  });

  it('keeps reset available for disabled credentials with a valid auth index', async () => {
    const file = {
      ...makeCodexFile('codex-disabled.json', 'auth-1', 'disabled@example.com'),
      disabled: true,
    };
    mocks.files = [file];
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success' as const,
      windows: [],
      rateLimitResetCreditsAvailableCount: 1,
    });
    vi.spyOn(CODEX_CONFIG, 'fetchQuota').mockResolvedValue(
      makeCodexQuotaData(1, [makeResetCredit('fresh-credit-1')])
    );

    const renderer = await renderAccountsPage();
    await openCodexQuotaTab(renderer, 'codex-disabled.json');
    const resetAction = renderer.root.findByProps({ 'data-quota-reset-action': 'true' });
    expect(resetAction.props.disabled).toBe(false);
    await act(async () => {
      resetAction.props.onClick();
    });
    await flushPromises();
    expect(mocks.showConfirmation).toHaveBeenCalledWith(
      expect.objectContaining({ title: 'codex_quota.reset_confirm_title' })
    );
  });

  it('keeps reset unavailable for runtime-only credentials', async () => {
    const file = {
      ...makeCodexFile('codex-runtime.json', 'auth-1', 'runtime@example.com'),
      runtimeOnly: true,
    };
    mocks.files = [file];
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success' as const,
      windows: [],
      rateLimitResetCreditsAvailableCount: 1,
    });
    const fetchSpy = vi.spyOn(CODEX_CONFIG, 'fetchQuota');

    const renderer = await renderAccountsPage();
    await openCodexQuotaTab(renderer, 'codex-runtime.json');
    const resetAction = renderer.root.findByProps({ 'data-quota-reset-action': 'true' });
    expect(resetAction.props.disabled).toBe(true);
    await act(async () => {
      resetAction.props.onClick();
    });
    expect(fetchSpy).not.toHaveBeenCalled();
    expect(mocks.showConfirmation).not.toHaveBeenCalled();
  });

  it('keeps reset unavailable without an auth index', async () => {
    const file = makeCodexFile('codex-noauth.json', '', 'noauth@example.com');
    mocks.files = [file];
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'success' as const,
      windows: [],
      rateLimitResetCreditsAvailableCount: 1,
    });
    const fetchSpy = vi.spyOn(CODEX_CONFIG, 'fetchQuota');

    const renderer = await renderAccountsPage();
    await openCodexQuotaTab(renderer, 'codex-noauth.json');
    const resetAction = renderer.root.findByProps({ 'data-quota-reset-action': 'true' });
    expect(resetAction.props.disabled).toBe(true);
    await act(async () => {
      resetAction.props.onClick();
    });
    expect(fetchSpy).not.toHaveBeenCalled();
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

  it('consumes a scoped OAuth mutation marker and suppresses stale credential status', async () => {
    const file = {
      ...makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
      status: 'error',
      statusMessage: 'token_expired',
      modified: 1_000,
    } as AuthFileItem;
    mocks.files = [file];
    recordAccountCredentialMutationMarker({
      connectionFingerprint: 'http://cpa-a.local:8317:manager-key',
      provider: 'codex',
      createdAtMs: Date.now(),
    });

    const renderer = await renderAccountsPage();
    await flushPromises();

    expect(mocks.loadFiles).toHaveBeenCalledTimes(2);
    expect(mocks.getActiveQuotaCooldowns).toHaveBeenCalledTimes(1);
    expect(mocks.listAccountActionCandidates).toHaveBeenCalledTimes(1);
    expect(listAccountCredentialMutationMarkers('http://cpa-a.local:8317:manager-key')).toEqual([]);
    expect(getAccountCardText(renderer, getAuthFileSelectionKey(file))).not.toContain(
      'accounts.health_reauth'
    );
  });
  it.each(['files', 'cooldowns', 'actions'] as const)(
    'keeps an OAuth mutation marker after a failed %s reload and consumes it on the next Accounts refresh',
    async (failedArtifact) => {
      const marker = recordAccountCredentialMutationMarker({
        connectionFingerprint: 'http://cpa-a.local:8317:manager-key',
        provider: 'codex',
        createdAtMs: Date.now(),
      });
      if (failedArtifact === 'files') {
        mocks.loadFiles
          .mockImplementationOnce(async () => mocks.files)
          .mockRejectedValueOnce(new Error('temporary auth-file list failure'))
          .mockImplementation(async () => mocks.files);
      } else if (failedArtifact === 'cooldowns') {
        mocks.getActiveQuotaCooldowns.mockRejectedValueOnce(
          new Error('temporary cooldown list failure')
        );
      } else {
        mocks.listAccountActionCandidates.mockRejectedValueOnce(
          new Error('temporary action-candidate list failure')
        );
      }

      const renderer = await renderAccountsPage();
      await flushPromises();

      expect(listAccountCredentialMutationMarkers('http://cpa-a.local:8317:manager-key')).toEqual([
        marker,
      ]);

      await act(async () => {
        await findButtonByText(renderer, 'common.refresh').props.onClick();
      });
      await flushPromises();

      expect(listAccountCredentialMutationMarkers('http://cpa-a.local:8317:manager-key')).toEqual(
        []
      );
      expect(mocks.loadFiles).toHaveBeenCalledTimes(failedArtifact === 'files' ? 3 : 2);
      expect(mocks.getActiveQuotaCooldowns).toHaveBeenCalledTimes(2);
      expect(mocks.listAccountActionCandidates).toHaveBeenCalledTimes(2);
    }
  );
  it('does not acknowledge an old-connection OAuth marker when its reload finishes after a connection change', async () => {
    const marker = recordAccountCredentialMutationMarker({
      connectionFingerprint: 'http://cpa-a.local:8317:manager-key',
      provider: 'codex',
      createdAtMs: Date.now(),
    });
    const delayedCredentialReload = createDeferred<AuthFileItem[]>();
    mocks.loadFiles
      .mockImplementationOnce(async () => mocks.files)
      .mockImplementationOnce(() => delayedCredentialReload.promise)
      .mockImplementation(async () => mocks.files);

    const renderer = await renderAccountsPage();
    await flushPromises();
    expect(mocks.loadFiles).toHaveBeenCalledTimes(2);

    mocks.apiBase = 'http://cpa-b.local:8317';
    mocks.managementKey = 'key-b';
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await flushPromises();

    await act(async () => {
      delayedCredentialReload.resolve(mocks.files);
      await delayedCredentialReload.promise;
    });
    await flushPromises();

    expect(listAccountCredentialMutationMarkers('http://cpa-a.local:8317:manager-key')).toEqual([
      marker,
    ]);
    expect(listAccountCredentialMutationMarkers('http://cpa-b.local:8317:key-b')).toEqual([]);
  });
  it('keeps inspection, operational, quota, and raw status evidence newer than OAuth', async () => {
    const markerAtMs = 1_700_000_000_000;
    const newerEvidenceAtMs = markerAtMs + 10_000;
    const newerCandidateAtMs = newerEvidenceAtMs + 1_000;
    const file = {
      ...makeCodexFile('codex.json', 'auth-1', 'codex@example.com'),
      status: 'error',
      statusMessage: 'post_oauth_failure',
      updatedAtMs: newerEvidenceAtMs,
    } as AuthFileItem;
    const selectionKey = getAuthFileSelectionKey(file);
    mocks.files = [file];
    mocks.location = {
      pathname: '/accounts',
      search: `?account=${encodeURIComponent(selectionKey)}&tab=overview`,
    };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: true,
      serverCodexInspectionAvailable: false,
    };
    mocks.localInspection = {
      savedAt: newerEvidenceAtMs,
      logs: [],
      logsCollapsed: true,
      actionFilter: 'all',
      connectionFingerprint: 'http://cpa-a.local:8317:manager-key',
      result: {
        settings: {},
        files: [file],
        startedAt: newerEvidenceAtMs - 1_000,
        finishedAt: newerEvidenceAtMs,
        summary: {
          totalFiles: 1,
          probeSetCount: 1,
          sampledCount: 1,
          disabledCount: 1,
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
            key: selectionKey,
            fileName: file.name,
            displayAccount: String(file.account ?? file.name),
            authIndex: String(file.authIndex ?? ''),
            accountId: null,
            accountSnapshot: String(file.account ?? ''),
            provider: 'codex',
            disabled: true,
            autoRecoverOwned: false,
            status: 'error',
            state: 'error',
            raw: file,
            action: 'reauth',
            actionReason: 'post OAuth inspection failure',
            statusCode: 401,
            usedPercent: null,
            isQuota: false,
            autoRecoverEligible: false,
            error: 'post OAuth inspection failure',
            actionHandled: false,
          },
        ],
      },
    };
    const cooldown = {
      authFileName: file.name,
      authIndex: String(file.authIndex ?? ''),
      disabledAtMs: newerEvidenceAtMs,
      recoverAtMs: newerEvidenceAtMs + 10_000,
    };
    const candidate = {
      id: 1,
      actionType: 'reauth',
      status: 'pending',
      provider: 'codex',
      authFileName: file.name,
      authIndex: String(file.authIndex ?? ''),
      reason: 'post OAuth candidate',
      firstSeenAtMs: newerCandidateAtMs,
      lastSeenAtMs: newerCandidateAtMs,
      hitCount: 1,
      createdAtMs: newerCandidateAtMs,
      updatedAtMs: newerCandidateAtMs,
    };
    mocks.getActiveQuotaCooldowns.mockResolvedValue([cooldown]);
    mocks.listAccountActionCandidates.mockResolvedValue({
      pendingCount: 1,
      items: [candidate],
    });
    installCodexQuotaStoreMutationMock();
    const storeKey = getQuotaCredentialStoreKey(file);
    mocks.quotaState.codexQuota = buildCredentialScopedQuotaRecord(file, {
      status: 'error',
      windows: [],
      error: 'post OAuth quota failure',
      errorStatus: 429,
      failedAtMs: newerEvidenceAtMs,
    });
    recordAccountCredentialMutationMarker({
      connectionFingerprint: 'http://cpa-a.local:8317:manager-key',
      provider: 'codex',
      createdAtMs: markerAtMs,
    });

    const renderer = await renderAccountsPage();
    await flushPromises();

    expect(mocks.quotaState.codexQuota).toHaveProperty(storeKey);
    expect(getAccountCardText(renderer, selectionKey)).toContain('accounts.health_reauth');
    const overview = renderer.root.findByType(AccountOverviewTab).props.detailView;
    expect(overview.overview.recentStatus.statusMessage).toBe('post_oauth_failure');
    expect(overview.strategy.actionCandidates).toEqual([
      expect.objectContaining({ id: candidate.id, reason: candidate.reason }),
    ]);
    expect(renderer.root.findAllByProps({ 'data-overview-section': 'attention' })).toHaveLength(1);
    await act(async () => {
      findHostButtonByText(renderer, 'accounts.detail_tab_quota').props.onClick();
    });
    expect(renderer.root.findByType(AccountQuotaTab).props.detailView.quota.cooldown).toEqual(
      cooldown
    );
  });
  it('invalidates stale evidence for a same-provider credential first returned by OAuth reload', async () => {
    const markerAtMs = Date.now();
    const existingFile = makeCodexFile('existing.json', 'auth-existing', 'existing@example.com');
    const oauthFile = {
      ...makeCodexFile('oauth.json', 'auth-oauth', 'oauth@example.com'),
      status: 'error',
      statusMessage: 'token_expired',
      updatedAtMs: markerAtMs - 1_000,
    } as AuthFileItem;
    const oauthSelectionKey = getAuthFileSelectionKey(oauthFile);
    mocks.files = [existingFile];
    mocks.location = {
      pathname: '/accounts',
      search: `?account=${encodeURIComponent(oauthSelectionKey)}&tab=overview`,
    };
    installCodexQuotaStoreMutationMock();
    const existingStoreKey = getQuotaCredentialStoreKey(existingFile);
    const existingQuota = {
      status: 'success' as const,
      windows: [],
      quotaInventoryObserved: true,
      fetchedAtMs: markerAtMs - 2_000,
      ...buildQuotaCredentialIdentity(existingFile),
    };
    const storeKey = getQuotaCredentialStoreKey(oauthFile);
    mocks.quotaState.codexQuota = {
      [existingStoreKey]: existingQuota,
      ...buildCredentialScopedQuotaRecord(oauthFile, {
        status: 'error',
        windows: [],
        error: 'stale OAuth quota failure',
        errorStatus: 429,
        failedAtMs: markerAtMs - 1_000,
      }),
    };
    mocks.loadFiles.mockImplementation(async () => {
      if (mocks.loadFiles.mock.calls.length === 1) return mocks.files;
      mocks.files = [existingFile, oauthFile];
      return mocks.files;
    });
    recordAccountCredentialMutationMarker({
      connectionFingerprint: 'http://cpa-a.local:8317:manager-key',
      provider: 'codex',
      baseline: createAccountCredentialMutationBaseline([existingFile], 'codex'),
      requireObservedMutation: true,
      createdAtMs: markerAtMs,
    });

    const renderer = await renderAccountsPage();
    await flushPromises();
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await flushPromises();

    expect(mocks.loadFiles).toHaveBeenCalledTimes(2);
    expect(mocks.quotaState.setCodexQuota).toHaveBeenCalledTimes(1);
    expect(listAccountCredentialMutationMarkers('http://cpa-a.local:8317:manager-key')).toEqual([]);
    expect(mocks.quotaState.codexQuota).toHaveProperty(existingStoreKey, existingQuota);
    expect(mocks.quotaState.codexQuota).not.toHaveProperty(storeKey);
    expect(getAccountCardText(renderer, oauthSelectionKey)).not.toContain('accounts.health_reauth');
  });
  it('keeps post-OAuth raw status and quota evidence for a credential first returned by reload', async () => {
    const markerAtMs = Date.now();
    const newerEvidenceAtMs = markerAtMs + 10_000;
    const existingFile = makeCodexFile('existing.json', 'auth-existing', 'existing@example.com');
    const oauthFile = {
      ...makeCodexFile('oauth.json', 'auth-oauth', 'oauth@example.com'),
      status: 'error',
      statusMessage: 'post_oauth_failure',
      updatedAtMs: newerEvidenceAtMs,
    } as AuthFileItem;
    const oauthSelectionKey = getAuthFileSelectionKey(oauthFile);
    mocks.files = [existingFile];
    mocks.location = {
      pathname: '/accounts',
      search: `?account=${encodeURIComponent(oauthSelectionKey)}&tab=overview`,
    };
    installCodexQuotaStoreMutationMock();
    const existingStoreKey = getQuotaCredentialStoreKey(existingFile);
    const existingQuota = {
      status: 'success' as const,
      windows: [],
      quotaInventoryObserved: true,
      fetchedAtMs: markerAtMs - 2_000,
      ...buildQuotaCredentialIdentity(existingFile),
    };
    const storeKey = getQuotaCredentialStoreKey(oauthFile);
    const oauthQuota = {
      status: 'error' as const,
      windows: [],
      error: 'post OAuth quota failure',
      errorStatus: 429,
      failedAtMs: newerEvidenceAtMs,
      ...buildQuotaCredentialIdentity(oauthFile),
    };
    mocks.quotaState.codexQuota = {
      [existingStoreKey]: existingQuota,
      [storeKey]: oauthQuota,
    };
    mocks.loadFiles.mockImplementation(async () => {
      if (mocks.loadFiles.mock.calls.length === 1) return mocks.files;
      mocks.files = [existingFile, oauthFile];
      return mocks.files;
    });
    recordAccountCredentialMutationMarker({
      connectionFingerprint: 'http://cpa-a.local:8317:manager-key',
      provider: 'codex',
      baseline: createAccountCredentialMutationBaseline([existingFile], 'codex'),
      requireObservedMutation: true,
      createdAtMs: markerAtMs,
    });

    const renderer = await renderAccountsPage();
    await flushPromises();
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await flushPromises();

    expect(mocks.quotaState.setCodexQuota).toHaveBeenCalledTimes(1);
    expect(listAccountCredentialMutationMarkers('http://cpa-a.local:8317:manager-key')).toEqual([]);
    expect(mocks.quotaState.codexQuota).toHaveProperty(existingStoreKey, existingQuota);
    expect(mocks.quotaState.codexQuota).toHaveProperty(storeKey, oauthQuota);
    expect(getAccountCardText(renderer, oauthSelectionKey)).toContain('accounts.health_limited');
    await act(async () => {
      findDetailButtonByName(renderer, oauthFile.name).props.onClick();
    });
    expect(
      renderer.root.findByType(AccountOverviewTab).props.detailView.overview.recentStatus
        .statusMessage
    ).toBe('post_oauth_failure');
  });
  it('preserves every existing same-provider quota when OAuth adds one credential', async () => {
    const markerAtMs = Date.now();
    const existingFiles = [
      makeCodexFile('existing-a.json', 'auth-a', 'a@example.com'),
      makeCodexFile('existing-b.json', 'auth-b', 'b@example.com'),
      makeCodexFile('existing-c.json', 'auth-c', 'c@example.com'),
    ];
    const oauthFile = {
      ...makeCodexFile('oauth-d.json', 'auth-d', 'd@example.com'),
      status: 'error',
      statusMessage: 'token_expired',
      updatedAtMs: markerAtMs - 1_000,
    } as AuthFileItem;
    mocks.files = existingFiles;
    installCodexQuotaStoreMutationMock();
    const existingQuotaByKey = Object.fromEntries(
      existingFiles.map((file, index) => [
        getQuotaCredentialStoreKey(file),
        {
          status: 'success' as const,
          windows: [],
          quotaInventoryObserved: true,
          fetchedAtMs: markerAtMs - (index + 2) * 1_000,
          ...buildQuotaCredentialIdentity(file),
        },
      ])
    );
    const oauthStoreKey = getQuotaCredentialStoreKey(oauthFile);
    const oauthStaleQuota = {
      status: 'error' as const,
      windows: [],
      error: 'stale OAuth quota failure',
      errorStatus: 429,
      failedAtMs: markerAtMs - 1_000,
      ...buildQuotaCredentialIdentity(oauthFile),
    };
    mocks.quotaState.codexQuota = {
      ...existingQuotaByKey,
      [oauthStoreKey]: oauthStaleQuota,
    };
    mocks.loadFiles.mockImplementation(async () => {
      if (mocks.loadFiles.mock.calls.length === 1) return mocks.files;
      mocks.files = [...existingFiles, oauthFile];
      return mocks.files;
    });
    const marker = recordAccountCredentialMutationMarker({
      connectionFingerprint: 'http://cpa-a.local:8317:manager-key',
      provider: 'codex',
      baseline: createAccountCredentialMutationBaseline(existingFiles, 'codex'),
      requireObservedMutation: true,
      createdAtMs: markerAtMs,
    });

    const renderer = await renderAccountsPage();
    await flushPromises();
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await flushPromises();

    expect(marker).not.toBeNull();
    expect(mocks.quotaState.setCodexQuota).toHaveBeenCalledTimes(1);
    existingFiles.forEach((file) => {
      const key = getQuotaCredentialStoreKey(file);
      expect(mocks.quotaState.codexQuota).toHaveProperty(key, existingQuotaByKey[key]);
    });
    expect(mocks.quotaState.codexQuota).not.toHaveProperty(oauthStoreKey);
    expect(listAccountCredentialMutationMarkers('http://cpa-a.local:8317:manager-key')).toEqual([]);
  });
  it('keeps existing quota and the OAuth marker when credential reload fails', async () => {
    const markerAtMs = Date.now();
    const existingFile = makeCodexFile('existing.json', 'auth-existing', 'existing@example.com');
    const existingStoreKey = getQuotaCredentialStoreKey(existingFile);
    const existingQuota = {
      status: 'success' as const,
      windows: [],
      quotaInventoryObserved: true,
      fetchedAtMs: markerAtMs - 1_000,
      ...buildQuotaCredentialIdentity(existingFile),
    };
    mocks.files = [existingFile];
    installCodexQuotaStoreMutationMock();
    mocks.quotaState.codexQuota = { [existingStoreKey]: existingQuota };
    const marker = recordAccountCredentialMutationMarker({
      connectionFingerprint: 'http://cpa-a.local:8317:manager-key',
      provider: 'codex',
      baseline: createAccountCredentialMutationBaseline([existingFile], 'codex'),
      requireObservedMutation: true,
      createdAtMs: markerAtMs,
    });
    mocks.loadFiles
      .mockImplementationOnce(async () => mocks.files)
      .mockRejectedValueOnce(new Error('temporary auth-file list failure'))
      .mockImplementation(async () => mocks.files);

    await renderAccountsPage();
    await flushPromises();

    expect(marker).not.toBeNull();
    expect(listAccountCredentialMutationMarkers('http://cpa-a.local:8317:manager-key')).toEqual([
      marker,
    ]);
    expect(mocks.quotaState.setCodexQuota).not.toHaveBeenCalled();
    expect(mocks.quotaState.codexQuota).toHaveProperty(existingStoreKey, existingQuota);
  });
  it('preserves post-first-OAuth quota when consuming multiple same-provider OAuth markers', async () => {
    const now = Date.now();
    const firstMarkerAtMs = now - 30_000;
    const bQuotaAtMs = now - 20_000;
    const secondMarkerAtMs = now - 10_000;
    const existingFile = makeCodexFile('existing.json', 'auth-existing', 'existing@example.com');
    const firstOauthFile = makeCodexFile('shared.json', 'auth-b', 'b@example.com');
    const secondOauthFile = {
      ...makeCodexFile('shared.json', 'auth-c', 'c@example.com'),
      status: 'error',
      statusMessage: 'token_expired',
      updatedAtMs: secondMarkerAtMs - 100,
    } as AuthFileItem;
    const existingStoreKey = getQuotaCredentialStoreKey(existingFile);
    const firstOauthStoreKey = getQuotaCredentialStoreKey(firstOauthFile);
    const secondOauthStoreKey = getQuotaCredentialStoreKey(secondOauthFile);
    const existingQuota = {
      status: 'success' as const,
      windows: [],
      quotaInventoryObserved: true,
      fetchedAtMs: firstMarkerAtMs - 1_000,
      ...buildQuotaCredentialIdentity(existingFile),
    };
    const firstOauthQuota = {
      status: 'success' as const,
      windows: [],
      quotaInventoryObserved: true,
      fetchedAtMs: bQuotaAtMs,
      ...buildQuotaCredentialIdentity(firstOauthFile),
    };
    const secondOauthStaleQuota = {
      status: 'error' as const,
      windows: [],
      error: 'stale second OAuth quota failure',
      errorStatus: 429,
      failedAtMs: secondMarkerAtMs - 100,
      ...buildQuotaCredentialIdentity(secondOauthFile),
    };
    const reloadedFiles = [existingFile, firstOauthFile, secondOauthFile];
    mocks.files = [existingFile];
    installCodexQuotaStoreMutationMock();
    mocks.quotaState.codexQuota = {
      [existingStoreKey]: existingQuota,
      [firstOauthStoreKey]: firstOauthQuota,
      [secondOauthStoreKey]: secondOauthStaleQuota,
    };
    const firstMarker = recordAccountCredentialMutationMarker({
      connectionFingerprint: 'http://cpa-a.local:8317:manager-key',
      provider: 'codex',
      baseline: createAccountCredentialMutationBaseline([existingFile], 'codex'),
      requireObservedMutation: true,
      createdAtMs: firstMarkerAtMs,
    });
    const secondMarker = recordAccountCredentialMutationMarker({
      connectionFingerprint: 'http://cpa-a.local:8317:manager-key',
      provider: 'codex',
      baseline: createAccountCredentialMutationBaseline([existingFile, firstOauthFile], 'codex'),
      requireObservedMutation: true,
      createdAtMs: secondMarkerAtMs,
    });
    mocks.loadFiles.mockImplementation(async () => {
      if (mocks.loadFiles.mock.calls.length === 1) return mocks.files;
      return reloadedFiles;
    });

    const renderer = await renderAccountsPage();
    await flushPromises();
    await act(async () => {
      renderer.update(<AccountsPage />);
      await Promise.resolve();
    });
    await flushPromises();

    expect(firstMarker).not.toBeNull();
    expect(secondMarker).not.toBeNull();
    expect(mocks.quotaState.codexQuota).toHaveProperty(existingStoreKey, existingQuota);
    expect(mocks.quotaState.codexQuota).toHaveProperty(firstOauthStoreKey, firstOauthQuota);
    expect(mocks.quotaState.codexQuota).not.toHaveProperty(secondOauthStoreKey);
    expect(listAccountCredentialMutationMarkers('http://cpa-a.local:8317:manager-key')).toEqual([]);
    expect(publishAccountCredentialMutationRevision).toHaveBeenCalledTimes(1);
    expect(publishAccountCredentialMutationRevision).toHaveBeenCalledWith({
      connectionFingerprint: 'http://cpa-a.local:8317:manager-key',
      provider: 'codex',
      kind: 'oauth',
    });
  });
  it('preserves confirmed OAuth quota when a newer marker remains unconfirmed after retry exhaustion', async () => {
    vi.useFakeTimers();
    const now = Date.now();
    const firstMarkerAtMs = now - 30_000;
    const bQuotaAtMs = now - 20_000;
    const secondMarkerAtMs = now - 10_000;
    const existingFile = makeCodexFile('existing.json', 'auth-existing', 'existing@example.com');
    const firstOauthFile = makeCodexFile('oauth-b.json', 'auth-b', 'b@example.com');
    const existingStoreKey = getQuotaCredentialStoreKey(existingFile);
    const firstOauthStoreKey = getQuotaCredentialStoreKey(firstOauthFile);
    const existingQuota = {
      status: 'success' as const,
      windows: [],
      quotaInventoryObserved: true,
      fetchedAtMs: firstMarkerAtMs - 1_000,
      ...buildQuotaCredentialIdentity(existingFile),
    };
    const firstOauthQuota = {
      status: 'success' as const,
      windows: [],
      quotaInventoryObserved: true,
      fetchedAtMs: bQuotaAtMs,
      ...buildQuotaCredentialIdentity(firstOauthFile),
    };
    mocks.files = [existingFile];
    installCodexQuotaStoreMutationMock();
    mocks.quotaState.codexQuota = {
      [existingStoreKey]: existingQuota,
      [firstOauthStoreKey]: firstOauthQuota,
    };
    const firstMarker = recordAccountCredentialMutationMarker({
      connectionFingerprint: 'http://cpa-a.local:8317:manager-key',
      provider: 'codex',
      baseline: createAccountCredentialMutationBaseline([existingFile], 'codex'),
      requireObservedMutation: true,
      createdAtMs: firstMarkerAtMs,
    });
    const secondMarker = recordAccountCredentialMutationMarker({
      connectionFingerprint: 'http://cpa-a.local:8317:manager-key',
      provider: 'codex',
      baseline: createAccountCredentialMutationBaseline([existingFile, firstOauthFile], 'codex'),
      requireObservedMutation: true,
      createdAtMs: secondMarkerAtMs,
    });
    mocks.loadFiles.mockImplementation(async () => {
      if (mocks.loadFiles.mock.calls.length === 1) return mocks.files;
      mocks.files = [existingFile, firstOauthFile];
      return mocks.files;
    });

    await renderAccountsPage();
    await flushPromises();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(15_000);
    });
    await flushPromises();

    expect(firstMarker).not.toBeNull();
    expect(secondMarker).not.toBeNull();
    expect(mocks.quotaState.codexQuota).toHaveProperty(existingStoreKey, existingQuota);
    expect(mocks.quotaState.codexQuota).toHaveProperty(firstOauthStoreKey, firstOauthQuota);
    expect(listAccountCredentialMutationMarkers('http://cpa-a.local:8317:manager-key')).toEqual([
      secondMarker,
    ]);
    expect(publishAccountCredentialMutationRevision).toHaveBeenCalledTimes(1);
  });
  it('does not consume OAuth mutation markers from another CPA connection', async () => {
    recordAccountCredentialMutationMarker({
      connectionFingerprint: 'http://cpa-b.local:8317:key-b',
      provider: 'codex',
      createdAtMs: Date.now(),
    });

    await renderAccountsPage();
    await flushPromises();

    expect(mocks.loadFiles).toHaveBeenCalledTimes(1);
    expect(listAccountCredentialMutationMarkers('http://cpa-b.local:8317:key-b')).toHaveLength(1);
  });
  it('polls passive Header and inspection evidence only while the accounts view is visible', async () => {
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
    expect(mocks.listCodexInspectionRuns).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(2);
    expect(mocks.listCodexInspectionRuns).toHaveBeenCalledTimes(2);

    visibilityState = 'hidden';
    await act(async () => {
      documentEvents.dispatchEvent(new Event('visibilitychange'));
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(2);
    expect(mocks.listCodexInspectionRuns).toHaveBeenCalledTimes(2);

    visibilityState = 'visible';
    await act(async () => {
      documentEvents.dispatchEvent(new Event('visibilitychange'));
      await Promise.resolve();
    });
    expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(3);
    expect(mocks.listCodexInspectionRuns).toHaveBeenCalledTimes(3);
  });
  it('synchronizes automatic inspection credential mutations once after completion', async () => {
    vi.useFakeTimers();
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: false,
      serverCodexInspectionAvailable: true,
    };
    const runningRun: CodexInspectionRun = {
      id: 17,
      triggerType: 'schedule',
      status: 'running',
      startedAtMs: 1_000,
      totalFiles: 1,
      probeSetCount: 1,
      sampledCount: 1,
      disabledCount: 1,
      enabledCount: 0,
      deleteCount: 0,
      disableCount: 0,
      enableCount: 0,
      reauthCount: 0,
      keepCount: 0,
      createdAtMs: 1_000,
      updatedAtMs: 1_000,
    };
    const completedRun: CodexInspectionRun = {
      ...runningRun,
      status: 'completed',
      finishedAtMs: 2_000,
      disabledCount: 0,
      enabledCount: 1,
      enableCount: 1,
      updatedAtMs: 2_000,
    };
    mocks.listCodexInspectionRuns
      .mockResolvedValueOnce({ items: [runningRun] })
      .mockResolvedValue({ items: [completedRun] });
    mocks.getCodexInspectionRun.mockResolvedValue({
      run: completedRun,
      results: [
        {
          id: 31,
          runId: completedRun.id,
          accountKey: 'codex.json\u0000auth-1',
          fileName: 'codex.json',
          displayAccount: 'codex@example.com',
          authIndex: 'auth-1',
          provider: 'codex',
          disabled: false,
          status: 'success',
          state: 'healthy',
          action: 'enable',
          actionReason: 'quota recovered',
          actionStatus: 'success',
          executedAction: 'enable',
          statusCode: 200,
          isQuota: false,
          createdAtMs: 2_000,
        },
      ],
    });

    await renderAccountsPage();
    await flushPromises();
    expect(mocks.loadFiles).toHaveBeenCalledTimes(1);
    expect(mocks.getActiveQuotaCooldowns).not.toHaveBeenCalled();
    expect(mocks.listAccountActionCandidates).not.toHaveBeenCalled();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    await flushPromises();

    expect(mocks.loadFiles).toHaveBeenCalledTimes(2);
    expect(mocks.getActiveQuotaCooldowns).toHaveBeenCalledTimes(1);
    expect(mocks.listAccountActionCandidates).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    await flushPromises();

    expect(mocks.loadFiles).toHaveBeenCalledTimes(2);
    expect(mocks.getActiveQuotaCooldowns).toHaveBeenCalledTimes(1);
    expect(mocks.listAccountActionCandidates).toHaveBeenCalledTimes(1);
  });
  it.each(['files', 'cooldowns', 'actions'] as const)(
    'retries automatic inspection credential synchronization after a transient %s reload failure',
    async (failedArtifact) => {
      vi.useFakeTimers();
      mocks.panelFeatureAvailability = {
        checking: false,
        managerServiceBase: 'http://manager.local:18317',
        requestMonitoringAvailable: false,
        serverCodexInspectionAvailable: true,
      };
      const runningRun: CodexInspectionRun = {
        id: 18,
        triggerType: 'schedule',
        status: 'running',
        startedAtMs: 1_000,
        totalFiles: 1,
        probeSetCount: 1,
        sampledCount: 1,
        disabledCount: 1,
        enabledCount: 0,
        deleteCount: 0,
        disableCount: 0,
        enableCount: 0,
        reauthCount: 0,
        keepCount: 0,
        createdAtMs: 1_000,
        updatedAtMs: 1_000,
      };
      const completedRun: CodexInspectionRun = {
        ...runningRun,
        status: 'completed',
        finishedAtMs: 2_000,
        disabledCount: 0,
        enabledCount: 1,
        enableCount: 1,
        updatedAtMs: 2_000,
      };
      mocks.listCodexInspectionRuns
        .mockResolvedValueOnce({ items: [runningRun] })
        .mockResolvedValue({ items: [completedRun] });
      mocks.getCodexInspectionRun.mockResolvedValue({
        run: completedRun,
        results: [
          {
            id: 32,
            runId: completedRun.id,
            accountKey: 'codex.json\u0000auth-1',
            fileName: 'codex.json',
            displayAccount: 'codex@example.com',
            authIndex: 'auth-1',
            provider: 'codex',
            disabled: false,
            status: 'success',
            state: 'healthy',
            action: 'enable',
            actionReason: 'quota recovered',
            actionStatus: 'success',
            executedAction: 'enable',
            statusCode: 200,
            isQuota: false,
            createdAtMs: 2_000,
          },
        ],
      });

      await renderAccountsPage();
      await flushPromises();
      expect(mocks.loadFiles).toHaveBeenCalledTimes(1);
      if (failedArtifact === 'files') {
        mocks.loadFiles.mockRejectedValueOnce(new Error('temporary auth-file list failure'));
      } else if (failedArtifact === 'cooldowns') {
        mocks.getActiveQuotaCooldowns.mockRejectedValueOnce(
          new Error('temporary cooldown list failure')
        );
      } else {
        mocks.listAccountActionCandidates.mockRejectedValueOnce(
          new Error('temporary action-candidate list failure')
        );
      }

      await act(async () => {
        await vi.advanceTimersByTimeAsync(60_000);
      });
      await flushPromises();
      expect(mocks.loadFiles).toHaveBeenCalledTimes(2);
      expect(mocks.getActiveQuotaCooldowns).toHaveBeenCalledTimes(1);
      expect(mocks.listAccountActionCandidates).toHaveBeenCalledTimes(1);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(60_000);
      });
      await flushPromises();
      expect(mocks.loadFiles).toHaveBeenCalledTimes(3);
      expect(mocks.getActiveQuotaCooldowns).toHaveBeenCalledTimes(2);
      expect(mocks.listAccountActionCandidates).toHaveBeenCalledTimes(2);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(60_000);
      });
      await flushPromises();
      expect(mocks.loadFiles).toHaveBeenCalledTimes(3);
      expect(mocks.getActiveQuotaCooldowns).toHaveBeenCalledTimes(2);
      expect(mocks.listAccountActionCandidates).toHaveBeenCalledTimes(2);
    }
  );
  it('ignores passive historical mutations but synchronizes an explicit historical action once', async () => {
    mocks.location = { pathname: '/accounts', search: '?view=health&healthMode=server' };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: false,
      serverCodexInspectionAvailable: true,
    };
    const historicalRun: CodexInspectionRun = {
      id: 1,
      triggerType: 'manual',
      status: 'completed',
      startedAtMs: 1_000,
      finishedAtMs: 2_000,
      totalFiles: 1,
      probeSetCount: 1,
      sampledCount: 1,
      disabledCount: 1,
      enabledCount: 0,
      deleteCount: 0,
      disableCount: 1,
      enableCount: 0,
      reauthCount: 0,
      keepCount: 0,
      createdAtMs: 1_000,
      updatedAtMs: 2_000,
    };
    const latestRun: CodexInspectionRun = {
      ...historicalRun,
      id: 2,
      startedAtMs: 3_000,
      finishedAtMs: 4_000,
      createdAtMs: 3_000,
      updatedAtMs: 4_000,
    };
    const historicalSnapshot: CredentialInspectionSnapshot = {
      ...makeInspectionSnapshot(
        mocks.files,
        [
          {
            runId: historicalRun.id,
            action: 'disable',
            actionStatus: 'success',
            executedAction: 'disable',
          },
        ],
        2_000
      ),
      runs: [latestRun, historicalRun],
    };

    await renderAccountsPage();
    await flushPromises();
    expect(mocks.loadFiles).toHaveBeenCalledTimes(1);

    await act(async () => {
      mocks.lastHealthWorkspaceProps?.onSnapshotChange(historicalSnapshot);
      await Promise.resolve();
    });
    expect(mocks.loadFiles).toHaveBeenCalledTimes(1);

    await act(async () => {
      await mocks.lastHealthWorkspaceProps?.onCredentialsChanged(undefined, historicalSnapshot);
    });
    expect(mocks.loadFiles).toHaveBeenCalledTimes(2);
    expect(mocks.getActiveQuotaCooldowns).toHaveBeenCalledTimes(1);
    expect(mocks.listAccountActionCandidates).toHaveBeenCalledTimes(1);

    await act(async () => {
      mocks.lastHealthWorkspaceProps?.onSnapshotChange(historicalSnapshot);
      await mocks.lastHealthWorkspaceProps?.onCredentialsChanged(undefined, historicalSnapshot);
    });
    expect(mocks.loadFiles).toHaveBeenCalledTimes(2);
    expect(mocks.getActiveQuotaCooldowns).toHaveBeenCalledTimes(1);
    expect(mocks.listAccountActionCandidates).toHaveBeenCalledTimes(1);
  });
  it('retries a failed explicit historical action synchronization after returning to accounts', async () => {
    mocks.location = { pathname: '/accounts', search: '?view=health&healthMode=server' };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: false,
      serverCodexInspectionAvailable: true,
    };
    const historicalRun: CodexInspectionRun = {
      id: 21,
      triggerType: 'manual',
      status: 'completed',
      startedAtMs: 1_000,
      finishedAtMs: 2_000,
      totalFiles: 1,
      probeSetCount: 1,
      sampledCount: 1,
      disabledCount: 1,
      enabledCount: 0,
      deleteCount: 0,
      disableCount: 1,
      enableCount: 0,
      reauthCount: 0,
      keepCount: 0,
      createdAtMs: 1_000,
      updatedAtMs: 2_000,
    };
    const latestRun: CodexInspectionRun = {
      ...historicalRun,
      id: 22,
      startedAtMs: 3_000,
      finishedAtMs: 4_000,
      createdAtMs: 3_000,
      updatedAtMs: 4_000,
    };
    const historicalSnapshot: CredentialInspectionSnapshot = {
      ...makeInspectionSnapshot(
        mocks.files,
        [
          {
            runId: 21,
            action: 'disable',
            actionStatus: 'success',
            executedAction: 'disable',
          },
        ],
        2_000
      ),
      runs: [latestRun, historicalRun],
    };

    const renderer = await renderAccountsPage();
    await flushPromises();
    mocks.loadFiles.mockRejectedValueOnce(new Error('temporary auth-file list failure'));

    await act(async () => {
      try {
        await mocks.lastHealthWorkspaceProps?.onCredentialsChanged(undefined, historicalSnapshot);
      } catch {
        // The mutation succeeded, but Accounts reports the failed synchronization to its caller.
      }
    });
    expect(mocks.loadFiles).toHaveBeenCalledTimes(2);

    await act(async () => {
      await findHostButtonByText(renderer, 'accounts.tab_accounts').props.onClick();
    });
    await flushPromises();

    expect(mocks.loadFiles).toHaveBeenCalledTimes(3);
    expect(mocks.getActiveQuotaCooldowns).toHaveBeenCalledTimes(2);
    expect(mocks.listAccountActionCandidates).toHaveBeenCalledTimes(2);
  });
  it('does not acknowledge a newer inspection mutation after its overlapping reload fails', async () => {
    mocks.location = { pathname: '/accounts', search: '?view=health&healthMode=server' };
    mocks.panelFeatureAvailability = {
      checking: false,
      managerServiceBase: 'http://manager.local:18317',
      requestMonitoringAvailable: false,
      serverCodexInspectionAvailable: true,
    };
    const firstReload = createDeferred<undefined>();
    const secondReload = createDeferred<undefined>();
    mocks.loadFiles
      .mockResolvedValueOnce(undefined)
      .mockReturnValueOnce(firstReload.promise)
      .mockReturnValueOnce(secondReload.promise);

    const makeMutationSnapshot = (
      runId: number,
      resultId: number,
      finishedAtMs: number
    ): CredentialInspectionSnapshot => {
      const run: CodexInspectionRun = {
        id: runId,
        triggerType: 'manual',
        status: 'completed',
        startedAtMs: finishedAtMs - 1_000,
        finishedAtMs,
        totalFiles: 1,
        probeSetCount: 1,
        sampledCount: 1,
        disabledCount: 1,
        enabledCount: 0,
        deleteCount: 0,
        disableCount: 1,
        enableCount: 0,
        reauthCount: 0,
        keepCount: 0,
        createdAtMs: finishedAtMs - 1_000,
        updatedAtMs: finishedAtMs,
      };
      return {
        ...makeInspectionSnapshot(
          mocks.files,
          [
            {
              id: resultId,
              runId,
              action: 'disable',
              actionStatus: 'success',
              executedAction: 'disable',
            },
          ],
          finishedAtMs
        ),
        runs: [run],
      };
    };
    const firstSnapshot = makeMutationSnapshot(31, 311, 2_000);
    const secondSnapshot = makeMutationSnapshot(32, 321, 3_000);

    await renderAccountsPage();
    await flushPromises();

    let firstSynchronization!: Promise<void>;
    act(() => {
      firstSynchronization = Promise.resolve(
        mocks.lastHealthWorkspaceProps?.onCredentialsChanged(undefined, firstSnapshot)
      );
    });
    await flushPromises();

    let secondSynchronization!: Promise<void>;
    act(() => {
      secondSynchronization = Promise.resolve(
        mocks.lastHealthWorkspaceProps?.onCredentialsChanged(undefined, secondSnapshot)
      );
    });
    await flushPromises();
    expect(mocks.loadFiles).toHaveBeenCalledTimes(3);

    await act(async () => {
      secondReload.reject(new Error('newer reload failed'));
      try {
        await secondSynchronization;
      } catch {
        // The newer snapshot remains pending and must be retried below.
      }
    });
    await act(async () => {
      firstReload.resolve(undefined);
      await firstSynchronization;
    });

    await act(async () => {
      await mocks.lastHealthWorkspaceProps?.onCredentialsChanged(undefined, secondSnapshot);
    });

    expect(mocks.loadFiles).toHaveBeenCalledTimes(4);
    expect(mocks.getActiveQuotaCooldowns).toHaveBeenCalledTimes(3);
    expect(mocks.listAccountActionCandidates).toHaveBeenCalledTimes(3);
  });
});
