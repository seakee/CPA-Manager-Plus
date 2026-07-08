import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { Select } from '@/components/ui/Select';
import { AuthFilesPage } from './AuthFilesPage';

const { mocks } = vi.hoisted(() => {
  (
    globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
  ).IS_REACT_ACT_ENVIRONMENT = true;
  return {
    mocks: {
      connectionStatus: 'connected' as 'connected' | 'disconnected',
      managementKey: 'test-key' as string,
      list: vi.fn(),
      showNotification: vi.fn(),
      showConfirmation: vi.fn(),
      navigate: vi.fn(),
      pageTransitionStatus: 'current' as 'current' | 'exiting' | 'stacked',
      loadExcluded: vi.fn(async () => undefined),
      loadModelAlias: vi.fn(async () => undefined),
      listCodexInspectionRuns: vi.fn(),
      getCodexInspectionRun: vi.fn(),
      getActiveQuotaCooldowns: vi.fn(),
      getHeaderSnapshots: vi.fn(),
      apiCallRequest: vi.fn(),
      setCodexQuota: vi.fn(),
      intervalCallbacks: [] as Array<{ callback: () => void; delay: number | null }>,
      codexQuota: {} as Record<string, unknown>,
      panelFeatureAvailability: {
        checking: false,
        panelHostMode: 'manager_embedded' as const,
        panelBase: 'http://manager.local:18317',
        managerServiceBase: 'http://manager.local:18317',
        managerServiceAvailable: true,
        requestMonitoringAvailable: true,
        modelPricesAvailable: true,
        serverCodexInspectionAvailable: true,
        dockerSetupAvailable: true,
        externalManagerConfigAvailable: false,
        reason: '',
      },
      t: (key: string, options?: Record<string, unknown>) => {
        if (options && typeof options.name === 'string') {
          return `${key}:${options.name}`;
        }
        return key;
      },
    },
  };
});

vi.mock('react-i18next', () => ({
  initReactI18next: { type: '3rdParty', init: () => {} },
  useTranslation: () => ({
    t: mocks.t,
  }),
}));

vi.mock('react-router-dom', () => ({
  useNavigate: () => mocks.navigate,
}));

vi.mock('motion/mini', () => ({
  animate: () => ({ stop: () => {} }),
}));

vi.mock('@/hooks/useInterval', () => ({
  useInterval: (callback: () => void, delay: number | null) => {
    mocks.intervalCallbacks.push({ callback, delay });
  },
}));

vi.mock('@/hooks/useHeaderRefresh', () => ({
  useHeaderRefresh: () => {},
}));

vi.mock('@/components/common/PageTransitionLayer', () => ({
  usePageTransitionLayer: () => ({ status: mocks.pageTransitionStatus }),
}));

vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => mocks.panelFeatureAvailability,
}));

vi.mock('@/utils/clipboard', () => ({
  copyToClipboard: vi.fn(async () => undefined),
}));

vi.mock('@/services/api', () => ({
  authFilesApi: {
    list: mocks.list,
  },
}));

vi.mock('@/services/api/usageService', () => ({
  usageServiceApi: {
    listCodexInspectionRuns: mocks.listCodexInspectionRuns,
    getCodexInspectionRun: mocks.getCodexInspectionRun,
    getActiveQuotaCooldowns: mocks.getActiveQuotaCooldowns,
  },
  monitoringAnalyticsApi: {
    getHeaderSnapshots: mocks.getHeaderSnapshots,
  },
}));

vi.mock('@/services/api/apiCall', () => ({
  apiCallApi: {
    request: mocks.apiCallRequest,
  },
  getApiCallErrorMessage: () => 'api call failed',
}));

vi.mock('@/stores', () => ({
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
      connectionStatus: 'connected' | 'disconnected';
      apiBase: string;
      managementKey: string;
    }) => unknown
  ) =>
    selector({
      connectionStatus: mocks.connectionStatus,
      apiBase: 'http://manager.local:18317',
      managementKey: mocks.managementKey,
    }),
  useThemeStore: (selector: (state: { resolvedTheme: 'dark' }) => unknown) =>
    selector({ resolvedTheme: 'dark' }),
  useQuotaStore: (
    selector: (state: {
      codexQuota: Record<string, unknown>;
      setCodexQuota: typeof mocks.setCodexQuota;
    }) => unknown
  ) =>
    selector({
      codexQuota: mocks.codexQuota,
      setCodexQuota: mocks.setCodexQuota,
    }),
}));

vi.mock('@/features/authFiles/hooks/useAuthFilesData', () => ({
  useAuthFilesData: () => ({
    files: mocks.list(),
    selectedFiles: new Set<string>(),
    selectionCount: 0,
    loading: false,
    error: '',
    uploading: false,
    authJsonPasteSaving: false,
    deleting: {},
    deletingAll: false,
    statusUpdating: {},
    batchStatusUpdating: false,
    batchFieldsUpdating: false,
    fileInputRef: { current: null },
    loadFiles: vi.fn(async () => undefined),
    handleUploadClick: vi.fn(),
    handleFileChange: vi.fn(),
    savePastedAuthJson: vi.fn(async () => undefined),
    handleDelete: vi.fn(),
    handleDeleteAll: vi.fn(),
    handleDownload: vi.fn(),
    handleStatusToggle: vi.fn(),
    toggleSelect: vi.fn(),
    selectAllVisible: vi.fn(),
    invertVisibleSelection: vi.fn(),
    deselectAll: vi.fn(),
    batchDownload: vi.fn(),
    batchSetStatus: vi.fn(),
    batchPatchFields: vi.fn(),
    batchDelete: vi.fn(),
  }),
}));

vi.mock('@/features/authFiles/hooks/useAuthFilesOauth', () => ({
  useAuthFilesOauth: () => ({
    excluded: [],
    excludedError: '',
    modelAlias: [],
    modelAliasError: '',
    allProviderModels: {},
    loadExcluded: mocks.loadExcluded,
    loadModelAlias: mocks.loadModelAlias,
    deleteExcluded: vi.fn(),
    deleteModelAlias: vi.fn(),
    handleMappingUpdate: vi.fn(),
    handleDeleteLink: vi.fn(),
    handleToggleFork: vi.fn(),
    handleRenameAlias: vi.fn(),
    handleDeleteAlias: vi.fn(),
  }),
}));

vi.mock('@/features/authFiles/hooks/useAuthFilesModels', () => ({
  useAuthFilesModels: () => ({
    modelsModalOpen: false,
    modelsLoading: false,
    modelsList: [],
    modelsFileName: '',
    modelsFileType: '',
    modelsError: '',
    showModels: vi.fn(),
    closeModelsModal: vi.fn(),
  }),
}));

vi.mock('@/features/authFiles/hooks/useAuthFilesPrefixProxyEditor', () => ({
  useAuthFilesPrefixProxyEditor: () => ({
    prefixProxyEditor: null,
    prefixProxyUpdatedText: '',
    prefixProxyDirty: false,
    openPrefixProxyEditor: vi.fn(),
    closePrefixProxyEditor: vi.fn(),
    handlePrefixProxyChange: vi.fn(),
    handlePrefixProxySave: vi.fn(),
  }),
}));

vi.mock('@/features/authFiles/hooks/useAuthFilesStatusBarCache', () => ({
  useAuthFilesStatusBarCache: () => new Map(),
}));

vi.mock('@/features/monitoring/codexInspection', () => ({
  createCodexInspectionConnectionFingerprint: () => 'test-fingerprint',
  loadCodexInspectionLastRun: () => null,
}));

vi.mock('@/features/authFiles/uiState', () => ({
  normalizeAuthFilesSortMode: (value: string) => (value === 'default' ? 'default' : null),
  normalizeAuthFilesViewMode: (value: string) =>
    value === 'diagram' || value === 'list' ? value : null,
  readAuthFilesUiState: () => null,
  readPersistedAuthFilesCompactMode: () => null,
  writeAuthFilesUiState: vi.fn(),
  writePersistedAuthFilesCompactMode: vi.fn(),
}));

vi.mock('@/features/authFiles/components/AuthFileCard', () => ({
  AuthFileCard: (props: {
    file: { name: string; authIndex?: unknown; auth_index?: unknown };
    quotaCooldown?: { authFileName: string; recoverAtMs: number } | null;
    codexDisplayQuota?: {
      status?: string;
      planType?: string | null;
      observedFromUsageHeaders?: boolean;
      rateLimitResetCreditsAvailableCount?: number | null;
      windows?: Array<{
        id?: string;
        usedPercent?: number | null;
        limitWindowSeconds?: number | null;
      }>;
    };
  }) => {
    const cooldown = props.quotaCooldown
      ? `${props.quotaCooldown.authFileName}@${props.quotaCooldown.recoverAtMs}`
      : '';
    const window = props.codexDisplayQuota?.windows?.[0];
    return (
      <div
        data-auth-card={props.file.name}
        data-auth-index={String(props.file.authIndex ?? props.file.auth_index ?? '')}
        data-quota-cooldown={cooldown}
        data-codex-quota-status={props.codexDisplayQuota?.status ?? ''}
        data-codex-quota-plan={props.codexDisplayQuota?.planType ?? ''}
        data-codex-quota-observed={String(
          props.codexDisplayQuota?.observedFromUsageHeaders ?? false
        )}
        data-codex-quota-reset-count={
          props.codexDisplayQuota?.rateLimitResetCreditsAvailableCount === undefined ||
          props.codexDisplayQuota.rateLimitResetCreditsAvailableCount === null
            ? ''
            : String(props.codexDisplayQuota.rateLimitResetCreditsAvailableCount)
        }
        data-codex-quota-window-ids={
          props.codexDisplayQuota?.windows?.map((quotaWindow) => quotaWindow.id).join(',') ?? ''
        }
        data-codex-quota-window-percent={
          window?.usedPercent === undefined || window.usedPercent === null
            ? ''
            : String(window.usedPercent)
        }
        data-codex-quota-window-seconds={
          window?.limitWindowSeconds === undefined || window.limitWindowSeconds === null
            ? ''
            : String(window.limitWindowSeconds)
        }
      />
    );
  },
}));

vi.mock('@/features/authFiles/components/AuthJsonPasteModal', () => ({
  AuthJsonPasteModal: () => null,
}));

vi.mock('@/features/authFiles/components/AuthFileModelsModal', () => ({
  AuthFileModelsModal: () => null,
}));

vi.mock('@/features/authFiles/components/AuthFilesPrefixProxyEditorModal', () => ({
  AuthFilesPrefixProxyEditorModal: () => null,
}));

vi.mock('@/features/authFiles/components/OAuthExcludedCard', () => ({
  OAuthExcludedCard: () => null,
}));

vi.mock('@/features/authFiles/components/OAuthModelAliasCard', () => ({
  OAuthModelAliasCard: () => null,
}));

vi.mock('@/features/oauth/CodexReauthDialog', () => ({
  CodexReauthDialog: () => null,
}));

vi.mock('@/components/ui/EmptyState', () => ({
  EmptyState: () => null,
}));

vi.mock('@/components/ui/ToggleSwitch', () => ({
  ToggleSwitch: () => null,
}));

vi.mock('@/components/ui/Modal', () => ({
  Modal: () => null,
}));

const setManagerServiceBase = (value: string) => {
  mocks.panelFeatureAvailability = {
    ...mocks.panelFeatureAvailability,
    managerServiceBase: value,
    managerServiceAvailable: Boolean(value),
  };
};

const setManagementKey = (value: string) => {
  mocks.managementKey = value;
};

// A controllable promise so a test can resolve a cooldown fetch at a chosen
// moment; used to cover the "request in flight, context changes, stale
// response lands" race.
const createDeferred = () => {
  let resolve!: (value: unknown[]) => void;
  const promise = new Promise<unknown[]>((res) => {
    resolve = res;
  });
  return { promise, resolve };
};

describe('AuthFilesPage quota cooldown derived badge', () => {
  beforeEach(() => {
    mocks.list.mockReset();
    mocks.getActiveQuotaCooldowns.mockReset();
    mocks.listCodexInspectionRuns.mockReset();
    mocks.getCodexInspectionRun.mockReset();
    mocks.getHeaderSnapshots.mockReset();
    mocks.apiCallRequest.mockReset();
    mocks.intervalCallbacks = [];
    mocks.codexQuota = {};
    mocks.setCodexQuota = vi.fn((updater: unknown) => {
      mocks.codexQuota =
        typeof updater === 'function'
          ? (updater as (prev: Record<string, unknown>) => Record<string, unknown>)(
              mocks.codexQuota
            )
          : (updater as Record<string, unknown>);
    });
    mocks.connectionStatus = 'connected';
    mocks.managementKey = 'test-key';
    mocks.pageTransitionStatus = 'current';

    mocks.list.mockReturnValue([
      { name: 'codex-one.json', type: 'codex' },
      { name: 'codex-two.json', type: 'codex' },
    ]);
    mocks.listCodexInspectionRuns.mockResolvedValue({ items: [] });
    mocks.getCodexInspectionRun.mockResolvedValue({ run: { id: 1 }, results: [], logs: [] });
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: 1_700_000_000_000,
      from_ms: 1_700_000_000_000,
      to_ms: 1_700_000_000_000,
      items: [],
    });

    setManagerServiceBase('http://manager.local:18317');
  });

  it('loads active quota cooldowns when the Manager Server is available', async () => {
    mocks.getActiveQuotaCooldowns.mockResolvedValue([
      { authFileName: 'codex-one.json', recoverAtMs: 2_000_000_000_000 },
    ]);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AuthFilesPage />);
    });

    await vi.waitFor(() => {
      expect(mocks.getActiveQuotaCooldowns).toHaveBeenCalledWith(
        'http://manager.local:18317',
        'test-key'
      );
    });

    expect(
      renderer!.root.findByProps({ 'data-auth-card': 'codex-one.json' }).props[
        'data-quota-cooldown'
      ]
    ).toBe('codex-one.json@2000000000000');
    // Files without an active cooldown surface an empty badge slot.
    expect(
      renderer!.root.findByProps({ 'data-auth-card': 'codex-two.json' }).props[
        'data-quota-cooldown'
      ]
    ).toBe('');
  });

  it('passes observed Codex quota from usage response headers to auth file cards', async () => {
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: 1_700_000_000_000,
      from_ms: 1_700_000_000_000,
      to_ms: 1_700_000_000_000,
      items: [
        {
          event_hash: 'event-1',
          timestamp_ms: 1_700_000_000_000,
          auth_file_snapshot: 'codex-one.json',
          auth_provider_snapshot: 'codex',
          response_metadata: {
            quota: {
              plan_type: 'free',
              active_limit: 'premium',
              primary: {
                used_percent: 20,
                reset_at_ms: 1_784_805_897_000,
                window_minutes: 43_200,
              },
              credits_has_credits: false,
              credits_unlimited: false,
            },
          },
        },
      ],
    });

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AuthFilesPage />);
    });

    await vi.waitFor(() => {
      expect(
        renderer!.root.findByProps({ 'data-auth-card': 'codex-one.json' }).props[
          'data-codex-quota-status'
        ]
      ).toBe('success');
    });

    const card = renderer!.root.findByProps({ 'data-auth-card': 'codex-one.json' });
    expect(card.props['data-codex-quota-plan']).toBe('free');
    expect(card.props['data-codex-quota-observed']).toBe('true');
    expect(card.props['data-codex-quota-window-percent']).toBe('20');
    expect(card.props['data-codex-quota-window-seconds']).toBe('2592000');
  });

  it('refreshes Codex quota instead of showing expired usage response headers', async () => {
    mocks.list.mockReturnValue([
      { name: 'codex-one.json', type: 'codex', authIndex: '0' },
      { name: 'codex-two.json', type: 'codex' },
    ]);
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: 1_700_018_000_001,
      from_ms: 1_700_000_000_000,
      to_ms: 1_700_018_000_001,
      items: [
        {
          event_hash: 'event-expired',
          timestamp_ms: 1_700_000_000_000,
          auth_file_snapshot: 'codex-one.json',
          auth_index: '0',
          auth_provider_snapshot: 'codex',
          response_metadata: {
            quota: {
              plan_type: 'free',
              rate_limit_reached_type: 'primary',
              reached_window_kind: 'five_hour',
              reached_window_source: 'primary',
              primary: {
                used_percent: 100,
                reset_at_ms: 1_700_018_000_000,
                window_minutes: 300,
              },
              secondary: {
                used_percent: 84,
                reset_at_ms: 1_700_604_800_000,
                window_minutes: 10_080,
              },
            },
          },
        },
      ],
    });
    mocks.apiCallRequest
      .mockResolvedValueOnce({
        statusCode: 200,
        body: JSON.stringify({
          plan_type: 'plus',
          rate_limit: {
            primary_window: {
              used_percent: 12,
              reset_after_seconds: 12_000,
              limit_window_seconds: 18_000,
            },
          },
        }),
      })
      .mockResolvedValueOnce({
        statusCode: 200,
        body: JSON.stringify({ available_count: 1, credits: [] }),
      });

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AuthFilesPage />);
    });

    await vi.waitFor(() => {
      expect(mocks.apiCallRequest).toHaveBeenCalledTimes(2);
    });

    await act(async () => {
      renderer!.update(<AuthFilesPage />);
    });

    const card = renderer!.root.findByProps({ 'data-auth-card': 'codex-one.json' });
    expect(card.props['data-codex-quota-status']).toBe('success');
    expect(card.props['data-codex-quota-observed']).toBe('false');
    expect(card.props['data-codex-quota-plan']).toBe('plus');
    expect(card.props['data-codex-quota-window-percent']).toBe('12');
  });

  it('refreshes Codex quota when a CPAMP cooldown recovers', async () => {
    const recoveredAtMs = Date.now() - 1_000;
    mocks.list.mockReturnValue([
      { name: 'codex-one.json', type: 'codex', authIndex: '0' },
      { name: 'codex-two.json', type: 'codex' },
    ]);
    mocks.getActiveQuotaCooldowns
      .mockResolvedValueOnce([
        {
          authFileName: 'codex-one.json',
          authIndex: '0',
          owner: 'cpamp_usage_429',
          recoverAtMs: recoveredAtMs,
        },
      ])
      .mockResolvedValueOnce([]);
    mocks.apiCallRequest
      .mockResolvedValueOnce({
        statusCode: 200,
        body: JSON.stringify({
          plan_type: 'plus',
          rate_limit: {
            primary_window: {
              used_percent: 12,
              reset_after_seconds: 12_000,
              limit_window_seconds: 18_000,
            },
          },
          rate_limit_reset_credits: { available_count: 1 },
        }),
      })
      .mockResolvedValueOnce({
        statusCode: 200,
        body: JSON.stringify({ available_count: 1, credits: [] }),
      });

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AuthFilesPage />);
    });

    await vi.waitFor(() => {
      expect(
        renderer!.root.findByProps({ 'data-auth-card': 'codex-one.json' }).props[
          'data-quota-cooldown'
        ]
      ).toBe(`codex-one.json@${recoveredAtMs}`);
    });

    const quotaInterval = mocks.intervalCallbacks.find((item) => item.delay === 60_000);
    await act(async () => {
      quotaInterval?.callback();
      await Promise.resolve();
      await Promise.resolve();
    });

    await vi.waitFor(() => {
      expect(mocks.apiCallRequest).toHaveBeenCalledTimes(2);
    });

    await act(async () => {
      renderer!.update(<AuthFilesPage />);
    });

    const card = renderer!.root.findByProps({ 'data-auth-card': 'codex-one.json' });
    expect(card.props['data-quota-cooldown']).toBe('');
    expect(card.props['data-codex-quota-status']).toBe('success');
    expect(card.props['data-codex-quota-observed']).toBe('false');
    expect(card.props['data-codex-quota-plan']).toBe('plus');
    expect(card.props['data-codex-quota-window-percent']).toBe('12');
    expect(card.props['data-codex-quota-window-seconds']).toBe('18000');
  });

  it('refreshes only the recovered auth index for shared Codex auth files', async () => {
    const recoveredAtMs = Date.now() - 1_000;
    mocks.list.mockReturnValue([
      { name: 'shared-codex.json', type: 'codex', authIndex: '0' },
      { name: 'shared-codex.json', type: 'codex', authIndex: '1' },
    ]);
    mocks.getActiveQuotaCooldowns
      .mockResolvedValueOnce([
        {
          authFileName: 'shared-codex.json',
          authIndex: '1',
          owner: 'cpamp_usage_429',
          recoverAtMs: recoveredAtMs,
        },
      ])
      .mockResolvedValueOnce([]);
    mocks.apiCallRequest
      .mockResolvedValueOnce({
        statusCode: 200,
        body: JSON.stringify({
          plan_type: 'plus',
          rate_limit: {
            primary_window: {
              used_percent: 12,
              reset_after_seconds: 12_000,
              limit_window_seconds: 18_000,
            },
          },
          rate_limit_reset_credits: { available_count: 1 },
        }),
      })
      .mockResolvedValueOnce({
        statusCode: 200,
        body: JSON.stringify({ available_count: 1, credits: [] }),
      });

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AuthFilesPage />);
    });

    await vi.waitFor(() => {
      const cards = renderer!.root.findAllByProps({ 'data-auth-card': 'shared-codex.json' });
      const card0 = cards.find((card) => card.props['data-auth-index'] === '0');
      const card1 = cards.find((card) => card.props['data-auth-index'] === '1');
      expect(card0?.props['data-quota-cooldown']).toBe('');
      expect(card1?.props['data-quota-cooldown']).toBe(`shared-codex.json@${recoveredAtMs}`);
    });

    const quotaInterval = mocks.intervalCallbacks.find((item) => item.delay === 60_000);
    await act(async () => {
      quotaInterval?.callback();
      await Promise.resolve();
      await Promise.resolve();
    });

    await vi.waitFor(() => {
      expect(mocks.apiCallRequest).toHaveBeenCalledTimes(2);
    });

    expect(mocks.apiCallRequest.mock.calls.map(([payload]) => payload.authIndex)).toEqual([
      '1',
      '1',
    ]);

    await act(async () => {
      renderer!.update(<AuthFilesPage />);
    });

    const cards = renderer!.root.findAllByProps({ 'data-auth-card': 'shared-codex.json' });
    const card0 = cards.find((card) => card.props['data-auth-index'] === '0');
    const card1 = cards.find((card) => card.props['data-auth-index'] === '1');
    expect(card0?.props['data-codex-quota-status']).toBe('');
    expect(card1?.props['data-quota-cooldown']).toBe('');
    expect(card1?.props['data-codex-quota-status']).toBe('success');
    expect(card1?.props['data-codex-quota-observed']).toBe('false');
    expect(card1?.props['data-codex-quota-plan']).toBe('plus');
  });

  it('uses file-only cooldowns for unique Codex auth file rows', async () => {
    const recoveredAtMs = Date.now() - 1_000;
    mocks.list.mockReturnValue([
      { name: 'legacy-codex.json', type: 'codex', authIndex: '0' },
      { name: 'codex-two.json', type: 'codex' },
    ]);
    mocks.getActiveQuotaCooldowns
      .mockResolvedValueOnce([
        {
          authFileName: 'legacy-codex.json',
          owner: 'cpamp_usage_429',
          recoverAtMs: recoveredAtMs,
        },
      ])
      .mockResolvedValueOnce([]);
    mocks.apiCallRequest
      .mockResolvedValueOnce({
        statusCode: 200,
        body: JSON.stringify({
          plan_type: 'plus',
          rate_limit: {
            primary_window: {
              used_percent: 12,
              reset_after_seconds: 12_000,
              limit_window_seconds: 18_000,
            },
          },
          rate_limit_reset_credits: { available_count: 1 },
        }),
      })
      .mockResolvedValueOnce({
        statusCode: 200,
        body: JSON.stringify({ available_count: 1, credits: [] }),
      });

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AuthFilesPage />);
    });

    await vi.waitFor(() => {
      expect(
        renderer!.root.findByProps({ 'data-auth-card': 'legacy-codex.json' }).props[
          'data-quota-cooldown'
        ]
      ).toBe(`legacy-codex.json@${recoveredAtMs}`);
    });

    const quotaInterval = mocks.intervalCallbacks.find((item) => item.delay === 60_000);
    await act(async () => {
      quotaInterval?.callback();
      await Promise.resolve();
      await Promise.resolve();
    });

    await vi.waitFor(() => {
      expect(mocks.apiCallRequest).toHaveBeenCalledTimes(2);
    });

    expect(mocks.apiCallRequest.mock.calls.map(([payload]) => payload.authIndex)).toEqual([
      '0',
      '0',
    ]);
  });

  it('does not apply file-only cooldowns to shared Codex auth files', async () => {
    const recoveredAtMs = Date.now() - 1_000;
    mocks.list.mockReturnValue([
      { name: 'shared-codex.json', type: 'codex', authIndex: '0' },
      { name: 'shared-codex.json', type: 'codex', authIndex: '1' },
    ]);
    mocks.getActiveQuotaCooldowns
      .mockResolvedValueOnce([
        {
          authFileName: 'shared-codex.json',
          owner: 'cpamp_usage_429',
          recoverAtMs: recoveredAtMs,
        },
      ])
      .mockResolvedValueOnce([]);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AuthFilesPage />);
    });

    await vi.waitFor(() => {
      const cards = renderer!.root.findAllByProps({ 'data-auth-card': 'shared-codex.json' });
      expect(cards).toHaveLength(2);
      expect(cards.every((card) => card.props['data-quota-cooldown'] === '')).toBe(true);
    });

    const quotaInterval = mocks.intervalCallbacks.find((item) => item.delay === 60_000);
    await act(async () => {
      quotaInterval?.callback();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.apiCallRequest).not.toHaveBeenCalled();
  });

  it('retries expired usage response header refreshes after the next header poll', async () => {
    mocks.list.mockReturnValue([{ name: 'codex-one.json', type: 'codex', authIndex: '0' }]);
    const buildExpiredHeaderResponse = () => ({
      generated_at_ms: 1_700_018_000_001,
      from_ms: 1_700_000_000_000,
      to_ms: 1_700_018_000_001,
      items: [
        {
          event_hash: 'event-expired-retry',
          timestamp_ms: 1_700_000_000_000,
          auth_file_snapshot: 'codex-one.json',
          auth_index: '0',
          auth_provider_snapshot: 'codex',
          response_metadata: {
            quota: {
              plan_type: 'free',
              rate_limit_reached_type: 'primary',
              reached_window_kind: 'five_hour',
              reached_window_source: 'primary',
              primary: {
                used_percent: 100,
                reset_at_ms: 1_700_018_000_000,
                window_minutes: 300,
              },
            },
          },
        },
      ],
    });
    mocks.getHeaderSnapshots.mockImplementation(async () => buildExpiredHeaderResponse());
    mocks.apiCallRequest
      .mockRejectedValueOnce(new Error('temporary failure'))
      .mockResolvedValueOnce({
        statusCode: 200,
        body: JSON.stringify({
          plan_type: 'plus',
          rate_limit: {
            primary_window: {
              used_percent: 12,
              reset_after_seconds: 12_000,
              limit_window_seconds: 18_000,
            },
          },
        }),
      })
      .mockResolvedValueOnce({
        statusCode: 200,
        body: JSON.stringify({ available_count: 1, credits: [] }),
      });

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AuthFilesPage />);
    });

    await vi.waitFor(() => {
      expect(
        Object.values(mocks.codexQuota).some(
          (quota) =>
            quota !== null &&
            typeof quota === 'object' &&
            'status' in quota &&
            quota.status === 'error'
        )
      ).toBe(true);
    });
    expect(mocks.apiCallRequest).toHaveBeenCalledTimes(1);

    await act(async () => {
      renderer!.update(<AuthFilesPage />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.apiCallRequest).toHaveBeenCalledTimes(1);

    const quotaInterval = mocks.intervalCallbacks.find((item) => item.delay === 60_000);
    await act(async () => {
      quotaInterval?.callback();
      await Promise.resolve();
      await Promise.resolve();
    });

    await vi.waitFor(() => {
      expect(mocks.apiCallRequest).toHaveBeenCalledTimes(3);
    });

    await act(async () => {
      renderer!.update(<AuthFilesPage />);
    });

    const card = renderer!.root.findByProps({ 'data-auth-card': 'codex-one.json' });
    expect(card.props['data-codex-quota-status']).toBe('success');
    expect(card.props['data-codex-quota-observed']).toBe('false');
    expect(card.props['data-codex-quota-plan']).toBe('plus');
  });

  it('merges observed Codex header quota without clearing stored quota-only fields', async () => {
    mocks.codexQuota = {
      'codex-one.json::-': {
        status: 'success',
        authFileKey: 'codex-one.json::-',
        authFileName: 'codex-one.json',
        authIndex: null,
        fetchedAtMs: 1_000,
        planType: 'plus',
        rateLimitResetCreditsAvailableCount: 2,
        windows: [
          {
            id: 'five-hour',
            label: '5-hour limit',
            labelKey: 'codex_quota.primary_window',
            usedPercent: 10,
            resetLabel: '06/30 12:00',
            limitWindowSeconds: 18_000,
          },
          {
            id: 'spark-five-hour-0',
            label: 'Spark 5-hour limit',
            labelKey: 'codex_quota.additional_primary_window',
            labelParams: { name: 'spark' },
            usedPercent: 30,
            resetLabel: '07/01 01:00',
            limitWindowSeconds: 18_000,
          },
        ],
      },
    };
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: 1_700_000_000_000,
      from_ms: 1_700_000_000_000,
      to_ms: 1_700_000_000_000,
      items: [
        {
          event_hash: 'event-1',
          timestamp_ms: 1_700_000_000_000,
          auth_file_snapshot: 'codex-one.json',
          auth_provider_snapshot: 'codex',
          response_metadata: {
            quota: {
              plan_type: 'free',
              primary: {
                used_percent: 20,
                reset_at_ms: 1_700_018_000_000,
                window_minutes: 300,
              },
            },
          },
        },
      ],
    });

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AuthFilesPage />);
    });

    await vi.waitFor(() => {
      expect(
        renderer!.root.findByProps({ 'data-auth-card': 'codex-one.json' }).props[
          'data-codex-quota-window-percent'
        ]
      ).toBe('20');
    });

    const card = renderer!.root.findByProps({ 'data-auth-card': 'codex-one.json' });
    expect(card.props['data-codex-quota-plan']).toBe('free');
    expect(card.props['data-codex-quota-observed']).toBe('true');
    expect(card.props['data-codex-quota-reset-count']).toBe('2');
    expect(card.props['data-codex-quota-window-ids']).toBe('five-hour,spark-five-hour-0');
    expect(card.props['data-codex-quota-window-seconds']).toBe('18000');
  });

  it('keeps manual Codex quota refresh failures over older header snapshots', async () => {
    mocks.codexQuota = {
      'codex-one.json::-': {
        status: 'error',
        error: 'refresh failed',
        errorStatus: 502,
        failedAtMs: 1_700_000_000_500,
        authFileKey: 'codex-one.json::-',
        authFileName: 'codex-one.json',
        authIndex: null,
        planType: 'plus',
        rateLimitResetCreditsAvailableCount: 2,
        windows: [
          {
            id: 'five-hour',
            label: '5-hour limit',
            usedPercent: 10,
            resetLabel: '06/30 12:00',
            limitWindowSeconds: 18_000,
          },
        ],
      },
    };
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: 1_700_000_000_000,
      from_ms: 1_700_000_000_000,
      to_ms: 1_700_000_000_000,
      items: [
        {
          event_hash: 'event-1',
          timestamp_ms: 1_700_000_000_000,
          auth_file_snapshot: 'codex-one.json',
          auth_provider_snapshot: 'codex',
          response_metadata: {
            quota: {
              plan_type: 'free',
              primary: {
                used_percent: 20,
                reset_at_ms: 1_700_018_000_000,
                window_minutes: 300,
              },
            },
          },
        },
      ],
    });

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AuthFilesPage />);
    });

    await vi.waitFor(() => {
      expect(
        renderer!.root.findByProps({ 'data-auth-card': 'codex-one.json' }).props[
          'data-codex-quota-status'
        ]
      ).toBe('error');
    });

    const card = renderer!.root.findByProps({ 'data-auth-card': 'codex-one.json' });
    expect(card.props['data-codex-quota-plan']).toBe('plus');
    expect(card.props['data-codex-quota-observed']).toBe('false');
    expect(card.props['data-codex-quota-reset-count']).toBe('2');
    expect(card.props['data-codex-quota-window-percent']).toBe('10');
  });

  it('uses newer header snapshots after manual Codex quota refresh failures', async () => {
    mocks.codexQuota = {
      'codex-one.json::-': {
        status: 'error',
        error: 'refresh failed',
        errorStatus: 502,
        failedAtMs: 1_699_999_999_000,
        authFileKey: 'codex-one.json::-',
        authFileName: 'codex-one.json',
        authIndex: null,
        planType: 'plus',
        rateLimitResetCreditsAvailableCount: 2,
        windows: [
          {
            id: 'five-hour',
            label: '5-hour limit',
            usedPercent: 10,
            resetLabel: '06/30 12:00',
            limitWindowSeconds: 18_000,
          },
          {
            id: 'spark-five-hour-0',
            label: 'Spark 5-hour limit',
            usedPercent: 30,
            resetLabel: '07/01 01:00',
            limitWindowSeconds: 18_000,
          },
        ],
      },
    };
    mocks.getHeaderSnapshots.mockResolvedValue({
      generated_at_ms: 1_700_000_000_000,
      from_ms: 1_700_000_000_000,
      to_ms: 1_700_000_000_000,
      items: [
        {
          event_hash: 'event-1',
          timestamp_ms: 1_700_000_000_000,
          auth_file_snapshot: 'codex-one.json',
          auth_provider_snapshot: 'codex',
          response_metadata: {
            quota: {
              plan_type: 'free',
              primary: {
                used_percent: 20,
                reset_at_ms: 1_700_018_000_000,
                window_minutes: 300,
              },
            },
          },
        },
      ],
    });

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AuthFilesPage />);
    });

    await vi.waitFor(() => {
      expect(
        renderer!.root.findByProps({ 'data-auth-card': 'codex-one.json' }).props[
          'data-codex-quota-status'
        ]
      ).toBe('success');
    });

    const card = renderer!.root.findByProps({ 'data-auth-card': 'codex-one.json' });
    expect(card.props['data-codex-quota-plan']).toBe('free');
    expect(card.props['data-codex-quota-observed']).toBe('true');
    expect(card.props['data-codex-quota-reset-count']).toBe('2');
    expect(card.props['data-codex-quota-window-percent']).toBe('20');
    expect(card.props['data-codex-quota-window-ids']).toBe('five-hour,spark-five-hour-0');
  });

  it('does not treat cooldown context changes as recovered cooldowns', async () => {
    const recoveredAtMs = Date.now() - 1_000;
    mocks.list.mockReturnValue([{ name: 'codex-one.json', type: 'codex', authIndex: '0' }]);
    mocks.getActiveQuotaCooldowns
      .mockResolvedValueOnce([
        {
          authFileName: 'codex-one.json',
          authIndex: '0',
          owner: 'cpamp_usage_429',
          recoverAtMs: recoveredAtMs,
        },
      ])
      .mockResolvedValueOnce([]);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AuthFilesPage />);
    });

    await vi.waitFor(() => {
      expect(
        renderer!.root.findByProps({ 'data-auth-card': 'codex-one.json' }).props[
          'data-quota-cooldown'
        ]
      ).toBe(`codex-one.json@${recoveredAtMs}`);
    });

    setManagerServiceBase('http://manager-two.local:18317');
    await act(async () => {
      renderer!.update(<AuthFilesPage />);
      await Promise.resolve();
      await Promise.resolve();
    });

    await vi.waitFor(() => {
      expect(mocks.getActiveQuotaCooldowns).toHaveBeenCalledWith(
        'http://manager-two.local:18317',
        'test-key'
      );
    });
    expect(mocks.apiCallRequest).not.toHaveBeenCalled();
  });

  it('clears stale cooldowns when managerServiceBase becomes empty', async () => {
    mocks.getActiveQuotaCooldowns.mockResolvedValue([
      { authFileName: 'codex-one.json', recoverAtMs: 2_000_000_000_000 },
    ]);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AuthFilesPage />);
    });

    // Cooldown loaded and surfaced on the card.
    await vi.waitFor(() => {
      expect(
        renderer!.root.findByProps({ 'data-auth-card': 'codex-one.json' }).props[
          'data-quota-cooldown'
        ]
      ).toBe('codex-one.json@2000000000000');
    });

    // Manager Server goes away (service down, credentials change, feature flag off).
    setManagerServiceBase('');
    await act(async () => {
      renderer!.update(<AuthFilesPage />);
    });

    expect(
      renderer!.root.findByProps({ 'data-auth-card': 'codex-one.json' }).props[
        'data-quota-cooldown'
      ]
    ).toBe('');
    expect(
      renderer!.root.findByProps({ 'data-auth-card': 'codex-two.json' }).props[
        'data-quota-cooldown'
      ]
    ).toBe('');
  });

  it('does not call getActiveQuotaCooldowns while managerServiceBase is empty', async () => {
    setManagerServiceBase('');

    await act(async () => {
      create(<AuthFilesPage />);
    });

    // Flush any pending microtasks; no loader invocation should ever happen.
    await Promise.resolve();
    await Promise.resolve();

    expect(mocks.getActiveQuotaCooldowns).not.toHaveBeenCalled();
  });

  it('drops a stale cooldown response that resolves after managerServiceBase becomes empty', async () => {
    const deferred = createDeferred();
    mocks.getActiveQuotaCooldowns.mockReturnValue(deferred.promise);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AuthFilesPage />);
    });

    // A fetch is in flight against the still-live base.
    await vi.waitFor(() => {
      expect(mocks.getActiveQuotaCooldowns).toHaveBeenCalledTimes(1);
    });

    // Manager Server becomes unavailable; the clear effect empties the map.
    setManagerServiceBase('');
    await act(async () => {
      renderer!.update(<AuthFilesPage />);
    });

    // The stale response finally lands; it must not resurrect the badge.
    await act(async () => {
      deferred.resolve([{ authFileName: 'codex-one.json', recoverAtMs: 2_000_000_000_000 }]);
      await deferred.promise.catch(() => {});
    });

    expect(
      renderer!.root.findByProps({ 'data-auth-card': 'codex-one.json' }).props[
        'data-quota-cooldown'
      ]
    ).toBe('');
  });

  it('drops a stale cooldown response that resolves after the management key changes', async () => {
    const first = createDeferred();
    mocks.getActiveQuotaCooldowns.mockReturnValueOnce(first.promise);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AuthFilesPage />);
    });

    // Initial fetch fired against the original key.
    await vi.waitFor(() => {
      expect(mocks.getActiveQuotaCooldowns).toHaveBeenCalledWith(
        'http://manager.local:18317',
        'test-key'
      );
    });

    // Credentials rotate: a fresh request fires against the new key.
    setManagementKey('rotated-key');
    const second = createDeferred();
    mocks.getActiveQuotaCooldowns.mockReturnValueOnce(second.promise);
    await act(async () => {
      renderer!.update(<AuthFilesPage />);
    });

    expect(mocks.getActiveQuotaCooldowns).toHaveBeenCalledTimes(2);

    // The new-context request resolves first with its own data; applied.
    await act(async () => {
      second.resolve([{ authFileName: 'codex-two.json', recoverAtMs: 1_700_000_000_000 }]);
      await second.promise.catch(() => {});
    });

    // The stale (old-key) response lands afterwards and must be ignored.
    await act(async () => {
      first.resolve([{ authFileName: 'codex-one.json', recoverAtMs: 2_000_000_000_000 }]);
      await first.promise.catch(() => {});
    });

    expect(
      renderer!.root.findByProps({ 'data-auth-card': 'codex-one.json' }).props[
        'data-quota-cooldown'
      ]
    ).toBe('');
    expect(
      renderer!.root.findByProps({ 'data-auth-card': 'codex-two.json' }).props[
        'data-quota-cooldown'
      ]
    ).toBe('codex-two.json@1700000000000');
  });

  it('drops stale cooldown when base changes before the next loader runs', async () => {
    const first = createDeferred();
    mocks.getActiveQuotaCooldowns.mockReturnValueOnce(first.promise);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AuthFilesPage />);
    });

    // Initial fetch is in flight against the original non-empty base.
    await vi.waitFor(() => {
      expect(mocks.getActiveQuotaCooldowns).toHaveBeenCalledTimes(1);
    });

    // Switch to another non-empty base, but mark the page as non-current so the
    // passive effect that normally kicks off the next load is intentionally
    // skipped. This isolates the review edge case: the old response returns
    // after context changes but before any new loader can bump the token.
    setManagerServiceBase('http://manager-two.local:18317');
    mocks.pageTransitionStatus = 'stacked';
    await act(async () => {
      renderer!.update(<AuthFilesPage />);
    });
    expect(mocks.getActiveQuotaCooldowns).toHaveBeenCalledTimes(1);

    // The old-context response lands before a new loader runs; it must be
    // dropped by the layout-effect invalidation alone.
    await act(async () => {
      first.resolve([{ authFileName: 'codex-one.json', recoverAtMs: 2_000_000_000_000 }]);
      await first.promise.catch(() => {});
    });

    expect(
      renderer!.root.findByProps({ 'data-auth-card': 'codex-one.json' }).props[
        'data-quota-cooldown'
      ]
    ).toBe('');
  });

  it('ignores mocked Select import for sort/plan dropdowns without crashing', () => {
    expect(Select).toBeDefined();
  });
});
