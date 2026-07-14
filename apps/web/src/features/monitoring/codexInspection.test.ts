import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AuthFileItem } from '@/types';
import { authFilesApi } from '@/services/api/authFiles';
import {
  CODEX_INSPECTION_LAST_RUN_STORAGE_KEY,
  CODEX_INSPECTION_SETTINGS_STORAGE_KEY,
  createCodexInspectionConnectionFingerprint,
  executeCodexInspectionActions,
  hydrateCodexInspectionLastRun,
  isReauthAction,
  loadCodexInspectionConfigurableSettings,
  loadCodexInspectionLastRun,
  resolveCodexInspectionAutoActionItems,
  saveCodexInspectionLastRun,
  toReauthDeleteExecutionItem,
  type CodexInspectionAction,
  type CodexInspectionResultItem,
  type CodexInspectionRunResult,
} from './codexInspection';
import {
  ACTION_FILTERS,
  buildCodexInspectionPaginationState,
  buildConfigOverviewItems,
  countHandlingStates,
  countActions,
  filterInspectionResults,
  filterByAction,
  getCanonicalServerCodexInspectionActionIds,
  normalizeActionFilter,
  getMixedServerCodexInspectionActionIds,
  isActionableServerCodexInspectionResult,
  normalizeServerCodexInspectionActionStatus,
  validateInspectionConfigDraft,
} from './model/codexInspectionPresentation';
import {
  getCodexInspectionOwnedDisableFileNames,
  recordCodexInspectionDisableOwnership,
} from './model/codexInspectionOwnership';

const createStorage = () => {
  const values = new Map<string, string>();
  return {
    getItem: vi.fn((key: string) => values.get(key) ?? null),
    setItem: vi.fn((key: string, value: string) => {
      values.set(key, value);
    }),
    removeItem: vi.fn((key: string) => {
      values.delete(key);
    }),
    clear: vi.fn(() => {
      values.clear();
    }),
  } as unknown as Storage;
};

const createResultItem = (
  action: CodexInspectionAction,
  overrides: Partial<CodexInspectionResultItem> = {}
): CodexInspectionResultItem => ({
  key: overrides.key ?? `${action}.json::1`,
  fileName: overrides.fileName ?? `${action}.json`,
  displayAccount: overrides.displayAccount ?? `${action}@example.com`,
  authIndex: overrides.authIndex ?? '1',
  accountId: overrides.accountId ?? 'account-1',
  provider: overrides.provider ?? 'codex',
  disabled: overrides.disabled ?? false,
  autoRecoverOwned: overrides.autoRecoverOwned ?? false,
  status: overrides.status ?? '',
  state: overrides.state ?? '',
  raw:
    overrides.raw ??
    ({
      name: `${action}.json`,
      type: 'codex',
      access_token: 'raw-secret-token',
    } as AuthFileItem),
  action,
  actionReason: overrides.actionReason ?? 'reason',
  statusCode: overrides.statusCode ?? (action === 'delete' ? 401 : 200),
  usedPercent: overrides.usedPercent ?? null,
  isQuota: overrides.isQuota ?? false,
  autoRecoverEligible: overrides.autoRecoverEligible ?? false,
  error: overrides.error ?? '',
  planType: overrides.planType ?? null,
  quotaWindows: overrides.quotaWindows ?? [],
  errorKind: overrides.errorKind ?? '',
  errorDetail: overrides.errorDetail ?? '',
});

const createRunResult = (): CodexInspectionRunResult => {
  const results = [createResultItem('delete')];
  return {
    settings: {
      baseUrl: 'https://secret.example.test',
      token: 'management-secret-token',
      targetType: 'codex',
      workers: 2,
      deleteWorkers: 1,
      timeout: 1000,
      retries: 0,
      userAgent: 'test-agent',
      usedPercentThreshold: 90,
      sampleSize: 0,
    },
    files: [
      {
        name: 'delete.json',
        type: 'codex',
        access_token: 'file-secret-token',
      } as AuthFileItem,
    ],
    results,
    summary: {
      totalFiles: 1,
      probeSetCount: 1,
      sampledCount: 1,
      disabledCount: 0,
      enabledCount: 1,
      deleteCount: 1,
      disableCount: 0,
      enableCount: 0,
      reauthCount: 0,
      keepCount: 0,
      usedPercentThreshold: 90,
      sampled: false,
      plannedActionPreview: ['delete@example.com -> delete'],
    },
    startedAt: 1000,
    finishedAt: 2000,
  };
};

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('Codex inspection settings', () => {
  it('migrates legacy auto execute settings to auto disable', () => {
    const storage = createStorage();
    vi.stubGlobal('localStorage', storage);
    storage.setItem(
      CODEX_INSPECTION_SETTINGS_STORAGE_KEY,
      JSON.stringify({ autoExecuteActions: true })
    );

    expect(loadCodexInspectionConfigurableSettings(null).autoActionMode).toBe('disable');
  });

  it('validates shared config drafts before saving', () => {
    const t = ((key: string, values?: Record<string, unknown>) => {
      if (key === 'monitoring.codex_inspection_settings_invalid_integer') {
        return `${values?.field} >= ${values?.min}`;
      }
      if (key === 'monitoring.codex_inspection_settings_invalid_threshold') {
        return `${values?.field} 0-100`;
      }
      return key;
    }) as never;

    const invalid = validateInspectionConfigDraft(
      {
        targetType: ' ',
        workers: '0',
        deleteWorkers: '2',
        timeout: '15000',
        retries: '-1',
        userAgent: 'agent',
        usedPercentThreshold: '120',
        sampleSize: 'all',
        autoActionMode: 'delete',
        autoRecoverEnabled: false,
      },
      t
    );

    expect(invalid.ok).toBe(false);
    expect(invalid.errors.targetType).toBe(
      'monitoring.codex_inspection_settings_target_type_required'
    );
    expect(invalid.errors.workers).toContain('>= 1');
    expect(invalid.errors.retries).toContain('>= 0');
    expect(invalid.errors.usedPercentThreshold).toContain('0-100');
    expect(invalid.errors.sampleSize).toContain('>= 0');

    const valid = validateInspectionConfigDraft(
      {
        targetType: ' Codex ',
        workers: '3',
        deleteWorkers: '2',
        timeout: '15000',
        retries: '0',
        userAgent: ' agent ',
        usedPercentThreshold: '99.5',
        sampleSize: '0',
        autoActionMode: 'unexpected',
        autoRecoverEnabled: true,
      },
      t
    );

    expect(valid.ok).toBe(true);
    expect(valid.values).toEqual({
      targetType: 'Codex',
      workers: 3,
      deleteWorkers: 2,
      timeout: 15000,
      retries: 0,
      userAgent: 'agent',
      usedPercentThreshold: 99.5,
      sampleSize: 0,
      autoActionMode: 'none',
      autoRecoverEnabled: true,
    });
  });

  it('builds local and server config overview items from the shared model', () => {
    const labels: Record<string, string> = {
      'monitoring.codex_inspection_threshold': 'Threshold',
      'monitoring.codex_inspection_sample_size': 'Sample',
      'monitoring.codex_inspection_settings_auto_action_mode_label': 'Auto',
      'monitoring.codex_inspection_settings_auto_action_mode_delete': 'Auto delete',
      'monitoring.codex_inspection_settings_auto_recover_label': 'Recovery',
      'monitoring.codex_inspection_workers': 'Workers',
      'monitoring.codex_inspection_settings_timeout_label': 'Timeout',
      'monitoring.codex_inspection_target_type': 'Target',
      'monitoring.server_codex_inspection_sample_all': 'All',
      'monitoring.server_codex_inspection_config_summary_schedule': 'Schedule',
      'monitoring.server_codex_inspection_config_summary_trigger': 'Trigger',
      'monitoring.server_codex_inspection_config_summary_threshold': 'Threshold',
      'monitoring.server_codex_inspection_config_summary_sample': 'Sample',
      'monitoring.server_codex_inspection_config_summary_auto': 'Auto',
      'monitoring.server_codex_inspection_schedule_enabled': 'Enabled',
      'monitoring.server_codex_inspection_schedule_disabled': 'Disabled',
      'common.enabled': 'Enabled',
      'common.disabled': 'Disabled',
    };
    const t = ((key: string) => labels[key] ?? key) as never;
    const settings = {
      targetType: 'codex',
      workers: 4,
      timeout: 15000,
      usedPercentThreshold: 100,
      sampleSize: 0,
      autoActionMode: 'delete' as const,
      autoRecoverEnabled: false,
    };

    expect(buildConfigOverviewItems(settings, { mode: 'local', t })).toMatchObject([
      { key: 'threshold', value: '100%', field: 'usedPercentThreshold' },
      { key: 'sample', value: 'All', field: 'sampleSize' },
      { key: 'auto', value: 'Auto delete', tone: 'bad', field: 'autoActionMode' },
      { key: 'recover', value: 'Disabled', tone: 'idle', field: 'autoActionMode' },
      { key: 'concurrency', value: '4', hint: 'Timeout: 15000', field: 'workers' },
      { key: 'target', value: 'codex', field: 'targetType' },
    ]);

    expect(
      buildConfigOverviewItems(settings, {
        mode: 'server',
        t,
        scheduleEnabled: true,
        scheduleLabel: 'Every 60 minutes',
      })
    ).toMatchObject([
      { key: 'schedule', value: 'Enabled', tone: 'good', field: 'schedule' },
      { key: 'trigger', value: 'Every 60 minutes', field: 'schedule' },
      { key: 'threshold', value: '100%', field: 'usedPercentThreshold' },
      { key: 'sample', value: 'All', field: 'sampleSize' },
      { key: 'auto', value: 'Auto delete', tone: 'bad', field: 'autoActionMode' },
      { key: 'recover', value: 'Disabled', tone: 'idle', field: 'autoActionMode' },
    ]);
  });
});

describe('resolveCodexInspectionAutoActionItems', () => {
  const deleteItem = createResultItem('delete');
  const disableItem = createResultItem('disable');
  const enableItem = createResultItem('enable', { autoRecoverEligible: true });
  const reauthItem = createResultItem('reauth', { statusCode: 401 });

  it('does nothing when automatic mode is none', () => {
    expect(
      resolveCodexInspectionAutoActionItems('none', false, [
        deleteItem,
        disableItem,
        enableItem,
        reauthItem,
      ])
    ).toEqual([]);
  });

  it('only enables recovered accounts in auto enable mode', () => {
    const items = resolveCodexInspectionAutoActionItems('enable', true, [
      deleteItem,
      disableItem,
      enableItem,
      reauthItem,
    ]);

    expect(items.map((item) => [item.fileName, item.action])).toEqual([['enable.json', 'enable']]);
  });

  it('runs automatic recovery independently from the problem-account mode', () => {
    const items = resolveCodexInspectionAutoActionItems('none', true, [
      deleteItem,
      disableItem,
      enableItem,
    ]);

    expect(items.map((item) => [item.fileName, item.action])).toEqual([['enable.json', 'enable']]);
  });

  it('never auto-enables an account without inspection ownership', () => {
    const unownedEnable = createResultItem('enable', { autoRecoverEligible: false });

    expect(resolveCodexInspectionAutoActionItems('delete', true, [unownedEnable])).toEqual([]);
  });

  it('blocks mixed actions for the same file before automatic execution', () => {
    const mixed = [
      createResultItem('enable', {
        fileName: 'mixed.json',
        autoRecoverEligible: true,
      }),
      createResultItem('delete', { fileName: 'mixed.json' }),
    ];

    expect(resolveCodexInspectionAutoActionItems('delete', true, mixed)).toEqual([]);
  });

  it('turns delete suggestions into disable actions in auto disable mode', () => {
    const items = resolveCodexInspectionAutoActionItems('disable', false, [
      deleteItem,
      disableItem,
      enableItem,
      reauthItem,
    ]);

    expect(items.map((item) => [item.fileName, item.action])).toEqual([
      ['delete.json', 'disable'],
      ['disable.json', 'disable'],
    ]);
  });

  it('keeps delete, disable, and enable suggestions in auto delete mode', () => {
    const items = resolveCodexInspectionAutoActionItems('delete', true, [
      deleteItem,
      disableItem,
      enableItem,
      reauthItem,
    ]);

    expect(items.map((item) => [item.fileName, item.action])).toEqual([
      ['delete.json', 'delete'],
      ['disable.json', 'disable'],
      ['enable.json', 'enable'],
    ]);
  });
});

describe('reauth delete execution mapping', () => {
  it('keeps reauth as a non-auto-executable action until the user chooses delete', () => {
    const reauthItem = createResultItem('reauth', {
      fileName: 'reauth.json',
      statusCode: 401,
      actionReason: '接口返回 401，认证令牌已失效，建议重新登录账号',
    });

    expect(isReauthAction(reauthItem)).toBe(true);

    const deleteItem = toReauthDeleteExecutionItem(reauthItem);
    expect(deleteItem).toMatchObject({
      fileName: 'reauth.json',
      action: 'delete',
    });
    expect(deleteItem.actionReason).toContain('用户选择删除需重新登录账号');
  });
});

describe('Codex inspection action presentation', () => {
  it('counts reauth suggestions and separates handling status from action filters', () => {
    const items = [
      createResultItem('delete', { statusCode: 500 }),
      createResultItem('reauth', { statusCode: 401 }),
      createResultItem('keep', { statusCode: 401 }),
    ];

    expect(countActions(items)).toEqual({
      delete: 1,
      disable: 0,
      enable: 0,
      reauth: 1,
      http401: 2,
      keep: 1,
    });
    expect(ACTION_FILTERS).not.toContain('http_401');
    expect(normalizeActionFilter('http_401')).toBe('reauth');
    expect(countHandlingStates(items)).toEqual({
      all: 3,
      pending: 3,
      no_action: 0,
    });
    expect(filterByAction(items, 'reauth').map((item) => item.action)).toEqual(['reauth']);
    expect(filterInspectionResults(items, 'pending', 'reauth').map((item) => item.action)).toEqual([
      'reauth',
    ]);
    expect(filterByAction(items, 'keep').map((item) => item.action)).toEqual(['keep']);
  });

  it('paginates inspection results and clamps out-of-range pages', () => {
    const items = Array.from({ length: 45 }, (_, index) =>
      createResultItem('disable', {
        key: `item-${index + 1}`,
        fileName: `item-${index + 1}.json`,
      })
    );

    const secondPage = buildCodexInspectionPaginationState(items, 2, 20);
    expect(secondPage.currentPage).toBe(2);
    expect(secondPage.totalPages).toBe(3);
    expect(secondPage.startItem).toBe(21);
    expect(secondPage.endItem).toBe(40);
    expect(secondPage.pageItems).toHaveLength(20);
    expect(secondPage.pageItems[0].fileName).toBe('item-21.json');

    const clamped = buildCodexInspectionPaginationState(items, 99, 20);
    expect(clamped.currentPage).toBe(3);
    expect(clamped.startItem).toBe(41);
    expect(clamped.endItem).toBe(45);
    expect(clamped.pageItems).toHaveLength(5);
  });
});

describe('Server Codex inspection action presentation', () => {
  it('normalizes pending action status for server results', () => {
    expect(normalizeServerCodexInspectionActionStatus({ action: 'delete' })).toBe('pending');
    expect(normalizeServerCodexInspectionActionStatus({ action: 'keep' })).toBe('none');
    expect(
      normalizeServerCodexInspectionActionStatus({
        action: 'delete',
        actionStatus: 'needs_review',
      })
    ).toBe('needs_review');
    expect(isActionableServerCodexInspectionResult({ id: 1, action: 'disable' })).toBe(true);
    expect(
      isActionableServerCodexInspectionResult({
        id: 2,
        action: 'disable',
        actionStatus: 'success',
      })
    ).toBe(false);
    expect(
      isActionableServerCodexInspectionResult({
        id: 3,
        action: 'delete',
        actionStatus: 'needs_review',
      })
    ).toBe(false);
  });

  it('exposes only the first file-level server action as executable', () => {
    const canonicalIds = getCanonicalServerCodexInspectionActionIds([
      { id: 1, fileName: 'auth-a.json', action: 'delete', actionStatus: 'success' },
      { id: 2, fileName: 'auth-a.json', action: 'delete', actionStatus: 'pending' },
      { id: 3, fileName: 'auth-b.json', action: 'disable', actionStatus: 'failed' },
      { id: 4, fileName: 'auth-c.json', action: 'reauth' },
    ]);

    expect(Array.from(canonicalIds)).toEqual([3]);
  });

  it('suppresses file-level server actions when same-file suggestions conflict', () => {
    const results = [
      { id: 1, fileName: 'auth-a.json', action: 'enable', actionStatus: 'pending' },
      { id: 2, fileName: 'auth-a.json', action: 'delete', actionStatus: 'pending' },
    ];
    const canonicalIds = getCanonicalServerCodexInspectionActionIds(results);
    const mixedIds = getMixedServerCodexInspectionActionIds(results);

    expect(Array.from(canonicalIds)).toEqual([]);
    expect(Array.from(mixedIds)).toEqual([1, 2]);
  });

  it('keeps one canonical action per same-action file group', () => {
    const canonicalIds = getCanonicalServerCodexInspectionActionIds([
      { id: 1, fileName: 'auth-a.json', action: 'delete', actionStatus: 'pending' },
      { id: 2, fileName: 'auth-a.json', action: 'delete', actionStatus: 'pending' },
    ]);

    expect(Array.from(canonicalIds)).toEqual([1]);
  });

  it('keeps canonical actions for different files independently', () => {
    const canonicalIds = getCanonicalServerCodexInspectionActionIds([
      { id: 1, fileName: 'auth-a.json', action: 'delete', actionStatus: 'pending' },
      { id: 2, fileName: 'auth-b.json', action: 'enable', actionStatus: 'failed' },
      { id: 3, fileName: 'auth-c.json', action: 'disable', actionStatus: 'needs_review' },
    ]);

    expect(Array.from(canonicalIds)).toEqual([1, 2]);
  });
});

describe('executeCodexInspectionActions', () => {
  it('deletes reauth accounts only after explicit delete mapping', async () => {
    const deleteSpy = vi.spyOn(authFilesApi, 'deleteFileByName').mockResolvedValue({
      status: 'ok',
      deleted: 1,
      files: ['reauth.json'],
      failed: [],
    });
    vi.spyOn(authFilesApi, 'list').mockResolvedValue({ files: [] });

    const execution = await executeCodexInspectionActions({
      settings: createRunResult().settings,
      items: [
        toReauthDeleteExecutionItem(
          createResultItem('reauth', { fileName: 'reauth.json', statusCode: 401 })
        ),
      ],
      previousFiles: [],
      connectionFingerprint: 'scope-test',
      source: 'manual',
    });

    expect(deleteSpy).toHaveBeenCalledWith('reauth.json');
    expect(execution.outcomes).toEqual([
      {
        action: 'delete',
        fileName: 'reauth.json',
        displayAccount: 'reauth@example.com',
        success: true,
        error: '',
      },
    ]);
  });

  it('uses action concurrency for disable and enable operations', async () => {
    let activeStatusUpdates = 0;
    let maxStatusUpdates = 0;

    vi.spyOn(authFilesApi, 'setStatusWithFallback').mockImplementation(async () => {
      activeStatusUpdates += 1;
      maxStatusUpdates = Math.max(maxStatusUpdates, activeStatusUpdates);
      await new Promise((resolve) => {
        setTimeout(resolve, 5);
      });
      activeStatusUpdates -= 1;
      return {} as Awaited<ReturnType<typeof authFilesApi.setStatusWithFallback>>;
    });
    vi.spyOn(authFilesApi, 'list').mockResolvedValue({ files: [] });

    const execution = await executeCodexInspectionActions({
      settings: {
        ...createRunResult().settings,
        workers: 10,
        deleteWorkers: 1,
      },
      items: [
        createResultItem('disable', { fileName: 'disable-a.json' }),
        createResultItem('disable', { fileName: 'disable-b.json' }),
        createResultItem('enable', { fileName: 'enable-a.json' }),
      ],
      previousFiles: [],
      connectionFingerprint: 'scope-test',
      source: 'manual',
    });

    expect(execution.outcomes).toHaveLength(3);
    expect(maxStatusUpdates).toBe(1);
  });

  it('records ownership for automatic disables and clears it after manual recovery', async () => {
    const storage = createStorage();
    vi.stubGlobal('localStorage', storage);
    vi.spyOn(authFilesApi, 'setStatusWithFallback').mockResolvedValue(
      {} as Awaited<ReturnType<typeof authFilesApi.setStatusWithFallback>>
    );
    vi.spyOn(authFilesApi, 'list').mockResolvedValue({ files: [] });
    const scope = 'scope-auto-recovery';
    const disabledFile = {
      name: 'owned.json',
      type: 'codex',
      auth_index: 'auth-1',
      disabled: true,
    } as AuthFileItem;

    await executeCodexInspectionActions({
      settings: createRunResult().settings,
      items: [
        {
          ...createResultItem('disable', {
            fileName: 'owned.json',
            authIndex: 'auth-1',
          }),
          accountId: null,
        },
      ],
      previousFiles: [],
      connectionFingerprint: scope,
      source: 'auto',
    });
    expect(Array.from(getCodexInspectionOwnedDisableFileNames(scope, [disabledFile]))).toEqual([
      'owned.json',
    ]);

    await executeCodexInspectionActions({
      settings: createRunResult().settings,
      items: [
        {
          ...createResultItem('enable', {
            fileName: 'owned.json',
            authIndex: 'auth-1',
            autoRecoverEligible: true,
          }),
          accountId: null,
        },
      ],
      previousFiles: [],
      connectionFingerprint: scope,
      source: 'manual',
    });
    expect(getCodexInspectionOwnedDisableFileNames(scope, [disabledFile]).size).toBe(0);
  });
});

describe('Codex inspection disable ownership', () => {
  it('isolates records by connection fingerprint and invalidates identity changes', () => {
    const storage = createStorage();
    vi.stubGlobal('localStorage', storage);
    const file = {
      name: 'owned.json',
      type: 'codex',
      auth_index: 'auth-1',
      disabled: true,
    } as AuthFileItem;

    recordCodexInspectionDisableOwnership('scope-a', {
      fileName: 'owned.json',
      authIndex: 'auth-1',
      accountId: null,
    });

    expect(Array.from(getCodexInspectionOwnedDisableFileNames('scope-a', [file]))).toEqual([
      'owned.json',
    ]);
    expect(getCodexInspectionOwnedDisableFileNames('scope-b', [file]).size).toBe(0);
    expect(
      getCodexInspectionOwnedDisableFileNames('scope-a', [
        { ...file, auth_index: 'auth-2' } as AuthFileItem,
      ]).size
    ).toBe(0);
    expect(getCodexInspectionOwnedDisableFileNames('scope-a', [file]).size).toBe(0);
  });
});

describe('Codex inspection last-run cache', () => {
  it('creates stable connection fingerprints without storing raw inputs', () => {
    const fingerprint = createCodexInspectionConnectionFingerprint(
      'https://cpa.example.test/',
      'management-secret-token'
    );

    expect(fingerprint).toBe(
      createCodexInspectionConnectionFingerprint(
        'https://cpa.example.test',
        'management-secret-token'
      )
    );
    expect(fingerprint).not.toContain('management-secret-token');
    expect(fingerprint).not.toContain('cpa.example.test');
    expect(fingerprint).not.toBe(
      createCodexInspectionConnectionFingerprint('https://cpa.example.test', 'other-token')
    );
  });

  it('sanitizes raw auth data before saving browser cache', () => {
    const storage = createStorage();
    vi.stubGlobal('localStorage', storage);

    const restored = saveCodexInspectionLastRun({
      result: createRunResult(),
      logs: [{ id: 'log-1', level: 'info', message: 'done', timestamp: 2000 }],
      logsCollapsed: true,
      actionFilter: 'delete',
    });

    const raw = storage.getItem(CODEX_INSPECTION_LAST_RUN_STORAGE_KEY);
    expect(raw).toBeTypeOf('string');
    expect(raw).not.toContain('management-secret-token');
    expect(raw).not.toContain('file-secret-token');
    expect(raw).not.toContain('raw-secret-token');
    expect(raw).not.toContain('https://secret.example.test');
    expect(restored?.result.files).toEqual([]);
    expect(restored?.result.results[0].raw).toEqual({
      name: 'delete.json',
      type: 'codex',
      authIndex: '1',
      disabled: false,
    });
  });

  it('ignores incompatible cached payloads', () => {
    expect(hydrateCodexInspectionLastRun({ version: 999 })).toBeNull();
  });

  it('ignores cached payloads that do not match the active connection', () => {
    const storage = createStorage();
    vi.stubGlobal('localStorage', storage);
    const expectedFingerprint = createCodexInspectionConnectionFingerprint(
      'https://cpa-a.example.test',
      'token-a'
    );
    const otherFingerprint = createCodexInspectionConnectionFingerprint(
      'https://cpa-b.example.test',
      'token-b'
    );

    saveCodexInspectionLastRun({
      result: createRunResult(),
      connectionFingerprint: expectedFingerprint,
    });

    expect(loadCodexInspectionLastRun(expectedFingerprint)?.result.results).toHaveLength(1);
    expect(loadCodexInspectionLastRun(otherFingerprint)).toBeNull();
  });

  it('does not restore legacy cached payloads when an active connection is provided', () => {
    const restored = hydrateCodexInspectionLastRun(
      {
        version: 1,
        savedAt: 2000,
        result: {
          settings: createRunResult().settings,
          results: [createResultItem('delete')],
          summary: createRunResult().summary,
          startedAt: 1000,
          finishedAt: 2000,
        },
        logs: [],
      },
      { expectedConnectionFingerprint: 'v1:active-connection' }
    );

    expect(restored).toBeNull();
  });

  it('restores completed runs that have no result rows', () => {
    const restored = hydrateCodexInspectionLastRun({
      version: 1,
      savedAt: 2000,
      result: {
        settings: {
          targetType: 'codex',
          workers: 2,
          deleteWorkers: 1,
          timeout: 1000,
          retries: 0,
          userAgent: 'test-agent',
          usedPercentThreshold: 90,
          sampleSize: 0,
        },
        results: [],
        summary: {
          totalFiles: 0,
          probeSetCount: 0,
          sampledCount: 0,
          sampled: false,
          usedPercentThreshold: 90,
        },
        startedAt: 1000,
        finishedAt: 2000,
      },
      logs: [],
    });

    expect(restored?.result.results).toEqual([]);
    expect(restored?.result.summary.sampledCount).toBe(0);
  });

  it('stores and restores quota windows and error details', () => {
    const storage = createStorage();
    vi.stubGlobal('localStorage', storage);
    const baseResult = createRunResult();
    const resultWithQuota: CodexInspectionRunResult = {
      ...baseResult,
      results: [
        createResultItem('disable', {
          statusCode: 402,
          usedPercent: 87,
          isQuota: true,
          planType: 'team',
          quotaWindows: [
            {
              id: 'monthly',
              labelKey: 'codex_quota.monthly_window',
              usedPercent: 87,
              resetLabel: '06/18 12:00',
              limitWindowSeconds: 2_592_000,
            },
          ],
          error: 'HTTP 402',
          errorKind: 'http_status',
          errorDetail: '{"message":"limit reached"}',
        }),
      ],
    };

    saveCodexInspectionLastRun({ result: resultWithQuota });

    const loaded = loadCodexInspectionLastRun();
    expect(loaded?.result.results[0].planType).toBe('team');
    expect(loaded?.result.results[0].quotaWindows).toEqual([
      {
        id: 'monthly',
        labelKey: 'codex_quota.monthly_window',
        labelParams: undefined,
        usedPercent: 87,
        resetLabel: '06/18 12:00',
        limitWindowSeconds: 2_592_000,
      },
    ]);
    expect(loaded?.result.results[0].errorKind).toBe('http_status');
    expect(loaded?.result.results[0].errorDetail).toContain('limit reached');
  });

  it('loads sanitized last-run records from storage', () => {
    const storage = createStorage();
    vi.stubGlobal('localStorage', storage);
    saveCodexInspectionLastRun({
      result: createRunResult(),
      logs: [{ id: 'log-1', level: 'success', message: 'done', timestamp: 2000 }],
      actionFilter: 'delete',
    });

    const loaded = loadCodexInspectionLastRun();

    expect(loaded?.actionFilter).toBe('delete');
    expect(loaded?.logs).toHaveLength(1);
    expect(loaded?.result.summary.deleteCount).toBe(1);
  });

  it('restores legacy 401 filters as reauth filters', () => {
    const storage = createStorage();
    vi.stubGlobal('localStorage', storage);
    const baseResult = createRunResult();
    const reauthResult: CodexInspectionRunResult = {
      ...baseResult,
      results: [createResultItem('reauth', { statusCode: 401 })],
      summary: {
        ...baseResult.summary,
        deleteCount: 0,
        reauthCount: 1,
        plannedActionPreview: ['reauth@example.com -> reauth'],
      },
    };

    saveCodexInspectionLastRun({
      result: reauthResult,
      actionFilter: 'http_401' as never,
    });

    const loaded = loadCodexInspectionLastRun();

    expect(loaded?.actionFilter).toBe('reauth');
    expect(loaded?.result.results[0].action).toBe('reauth');
    expect(loaded?.result.summary.reauthCount).toBe(1);
  });
});
