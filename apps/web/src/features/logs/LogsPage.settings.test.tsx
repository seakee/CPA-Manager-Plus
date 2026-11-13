import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import '@/i18n';
import { PublicApiError, type PublicApiClient } from '@/fullweb/publicApi';
import { triggerHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useConfigStore, useNotificationStore } from '@/stores';
import { Button } from '@/components/ui/Button';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { IconDownload, IconTimer } from '@/components/ui/icons';
import { LogsPage } from './LogsPage';
import { useLogsConfigAuthority } from './useLogsConfigAuthority';
import { LogsAuthorityClientContext } from './LogsAuthorityContext';

const mocks = vi.hoisted(() => ({
  getConfig: vi.fn(),
  updateLoggingToFile: vi.fn(),
  updateDebug: vi.fn(),
  updateRequestLog: vi.fn(),
  updateLogsMaxTotalSizeMb: vi.fn(),
  updateErrorLogsMaxFiles: vi.fn(),
  fetchLogs: vi.fn(),
  fetchErrorLogs: vi.fn(),
  downloadBlob: vi.fn(),
  authState: {
    connectionStatus: 'connected',
    apiBase: '/console/api/v2/cpamp',
    managementKey: 'admin-key',
  },
}));

vi.mock('@/stores/useAuthStore', () => ({
  useAuthStore: (selector: (state: typeof mocks.authState) => unknown) =>
    selector(mocks.authState),
}));

vi.mock('@/services/api/config', () => ({
  configApi: {
    getConfig: mocks.getConfig,
    updateLoggingToFile: mocks.updateLoggingToFile,
    updateDebug: mocks.updateDebug,
    updateRequestLog: mocks.updateRequestLog,
    updateLogsMaxTotalSizeMb: mocks.updateLogsMaxTotalSizeMb,
    updateErrorLogsMaxFiles: mocks.updateErrorLogsMaxFiles,
  },
}));

vi.mock('@/services/api/logs', () => ({
  logsApi: {
    fetchLogs: mocks.fetchLogs,
    fetchErrorLogs: mocks.fetchErrorLogs,
  },
}));

vi.mock('@/utils/download', () => ({
  downloadBlob: mocks.downloadBlob,
}));
vi.mock('./hooks/useLogScroller', () => ({
  isNearBottom: () => true,
  useLogScroller: () => ({
    logViewerRef: { current: null },
    canLoadMore: false,
    handleLogScroll: vi.fn(),
    scrollToBottom: vi.fn(),
    requestScrollToBottom: vi.fn(),
  }),
}));

function buildAuthorityClient(capability: {
  mode: string;
  writable: unknown;
  reason?: string;
}): PublicApiClient {
  return {
    get: (async () => ({
      capability: {
        readable: true,
        rulesWritable: capability.writable,
        ...capability,
      },
    })) as PublicApiClient['get'],
    post: vi.fn() as PublicApiClient['post'],
    put: vi.fn() as PublicApiClient['put'],
    uploadArtifact: vi.fn() as PublicApiClient['uploadArtifact'],
  };
}

function createDeferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const dispatchWindowEvent = vi.fn<(event: Event) => boolean>(() => true);
let renderer: ReactTestRenderer | undefined;

async function mountLogsPage(
  authorityClient: PublicApiClient,
  initialEntry = '/logs'
): Promise<ReactTestRenderer> {
  await act(async () => {
    renderer = create(
      <MemoryRouter initialEntries={[initialEntry]}>
        <LogsPage authorityClient={authorityClient} />
      </MemoryRouter>
    );
  });
  return renderer!;
}

function byTestId(root: ReactTestInstance, id: string): ReactTestInstance {
  return root.findByProps({ 'data-testid': id });
}

function maybeTestId(root: ReactTestInstance, id: string): ReactTestInstance | undefined {
  return root.findAllByProps({ 'data-testid': id })[0];
}

let storageMap: Map<string, string>;

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  dispatchWindowEvent.mockClear();
  storageMap = new Map<string, string>();
  const fakeLocalStorage = {
    getItem: (key: string) => storageMap.get(key) ?? null,
    setItem: (key: string, value: string) => {
      storageMap.set(key, String(value));
    },
    removeItem: (key: string) => {
      storageMap.delete(key);
    },
    clear: () => {
      storageMap.clear();
    },
  };
  vi.stubGlobal('localStorage', fakeLocalStorage);
  vi.stubGlobal('window', {
    dispatchEvent: dispatchWindowEvent,
    localStorage: fakeLocalStorage,
    setTimeout: (...args: Parameters<typeof globalThis.setTimeout>) =>
      globalThis.setTimeout(...args),
    clearTimeout: (...args: Parameters<typeof globalThis.clearTimeout>) =>
      globalThis.clearTimeout(...args),
    setInterval: (...args: Parameters<typeof globalThis.setInterval>) =>
      globalThis.setInterval(...args),
    clearInterval: (...args: Parameters<typeof globalThis.clearInterval>) =>
      globalThis.clearInterval(...args),
  });
  mocks.authState.connectionStatus = 'connected';
  mocks.authState.apiBase = '/console/api/v2/cpamp';
  mocks.authState.managementKey = 'admin-key';
  useConfigStore.setState({
    config: {
      debug: false,
      requestLog: false,
      loggingToFile: false,
      logsMaxTotalSizeMb: 0,
      errorLogsMaxFiles: 10,
      pprofEnable: false,
      pprofAddr: '127.0.0.1:8316',
      raw: {},
    },
    loading: false,
    error: null,
  });
  useNotificationStore.getState().clearAll();
  mocks.getConfig.mockReset();
  mocks.fetchLogs.mockReset();
  mocks.downloadBlob.mockReset();
  mocks.updateLoggingToFile.mockResolvedValue({ ok: true });
  mocks.updateDebug.mockResolvedValue({ ok: true });
  mocks.updateRequestLog.mockResolvedValue({ ok: true });
  mocks.updateLogsMaxTotalSizeMb.mockResolvedValue({ ok: true });
  mocks.updateErrorLogsMaxFiles.mockResolvedValue({ ok: true });
  mocks.fetchLogs.mockResolvedValue({
    lines: ['[2026-11-12 10:00:00] [info] [log_writer.go:1] file logging active'],
    'line-count': 1,
    'latest-timestamp': 100,
    latestAfter: 100,
    nextCursor: 'cursor-1',
  });
  mocks.fetchErrorLogs.mockResolvedValue({ files: [] });
});

afterEach(async () => {
  if (renderer) {
    await act(async () => {
      renderer?.unmount();
    });
  }
  renderer = undefined;
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

describe('LogsPage settings and empty-state file logging control', () => {
  function findAutoRefreshSwitch(root: ReactTestInstance): ReactTestInstance {
    const match = root.findAllByType(ToggleSwitch).find(
      (node) => node.findAllByType(IconTimer).length > 0
    );
    if (!match) {
      throw new Error('autoRefresh ToggleSwitch not found');
    }
    return match;
  }

  function findDownloadButton(root: ReactTestInstance): ReactTestInstance {
    const match = root.findAllByType(Button).find(
      (node) => node.findAllByType(IconDownload).length > 0
    );
    if (!match) {
      throw new Error('download Button not found');
    }
    return match;
  }

  it('enables file logging from the empty state, loads logs, and clears buffer/cursor/autoRefresh when disabled on the page', async () => {
    vi.useFakeTimers();
    try {
      const client = buildAuthorityClient({ mode: 'managed', writable: true });
      const mounted = await mountLogsPage(client, '/logs');

      expect(mocks.fetchLogs).not.toHaveBeenCalled();
      expect(findDownloadButton(mounted.root).props.disabled).toBe(true);

      const enableButton = byTestId(mounted.root, 'logs-empty-enable-button');
      expect(enableButton.props.disabled).toBe(false);

      await act(async () => {
        enableButton.props.onClick();
      });

      expect(mocks.updateLoggingToFile).toHaveBeenCalledWith(true);
      expect(useConfigStore.getState().config?.loggingToFile).toBe(true);
      expect(mocks.fetchLogs).toHaveBeenCalledTimes(1);
      expect(mocks.fetchLogs).toHaveBeenCalledWith({ limit: 10000 });
      expect(maybeTestId(mounted.root, 'logs-empty-enable-button')).toBeUndefined();
      const downloadBtn = findDownloadButton(mounted.root);
      expect(downloadBtn.props.disabled).toBe(false);
      await act(async () => {
        downloadBtn.props.onClick();
      });
      expect(mocks.downloadBlob).toHaveBeenCalledTimes(1);

      await act(async () => {
        findAutoRefreshSwitch(mounted.root).props.onChange(true);
      });
      expect(storageMap.get('logsPage.autoRefresh')).toBe('true');

      await act(async () => {
        await vi.advanceTimersByTimeAsync(8000);
      });
      expect(mocks.fetchLogs).toHaveBeenCalledTimes(2);
      expect(mocks.fetchLogs).toHaveBeenLastCalledWith({
        limit: 10000,
        cursor: 'cursor-1',
        after: 100,
      });

      await act(async () => {
        byTestId(mounted.root, 'logs-tab-settings').props.onClick();
      });

      const loggingRow = byTestId(mounted.root, 'logs-setting-row-logging-to-file');
      const loggingCheckbox = loggingRow.findByType('input');
      expect(loggingCheckbox.props.checked).toBe(true);

      await act(async () => {
        loggingCheckbox.props.onChange({ target: { checked: false } });
      });

      expect(mocks.updateLoggingToFile).toHaveBeenLastCalledWith(false);
      expect(useConfigStore.getState().config?.loggingToFile).toBe(false);
      expect(storageMap.get('logsPage.autoRefresh')).toBe('false');

      await act(async () => {
        byTestId(mounted.root, 'logs-tab-logs').props.onClick();
      });

      expect(findDownloadButton(mounted.root).props.disabled).toBe(true);
      expect(byTestId(mounted.root, 'logs-empty-enable-button').props.disabled).toBe(false);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(16000);
      });
      expect(mocks.fetchLogs).toHaveBeenCalledTimes(2);

      // Re-enabling from empty state resets query to full load (no stale cursor) and keeps autoRefresh off.
      await act(async () => {
        byTestId(mounted.root, 'logs-empty-enable-button').props.onClick();
      });
      expect(mocks.fetchLogs).toHaveBeenCalledTimes(3);
      expect(mocks.fetchLogs).toHaveBeenLastCalledWith({ limit: 10000 });
      expect(findAutoRefreshSwitch(mounted.root).props.checked).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });

  it('clears buffer, cursor, in-flight backfill, download and polling when file logging is turned off externally', async () => {
    vi.useFakeTimers();
    try {
      useConfigStore.setState((state) => ({
        ...state,
        config: {
          ...state.config!,
          loggingToFile: true,
        },
      }));
      const client = buildAuthorityClient({ mode: 'managed', writable: true });
      const mounted = await mountLogsPage(client, '/logs');

      expect(mocks.fetchLogs).toHaveBeenCalledTimes(1);
      expect(findDownloadButton(mounted.root).props.disabled).toBe(false);

      await act(async () => {
        findAutoRefreshSwitch(mounted.root).props.onChange(true);
      });
      expect(findAutoRefreshSwitch(mounted.root).props.checked).toBe(true);
      expect(storageMap.get('logsPage.autoRefresh')).toBe('true');

      // Start an incremental poll and leave it in flight when external config flips false.
      const stalePoll = createDeferred<{
        lines: string[];
        'line-count': number;
        'latest-timestamp': number;
        latestAfter: number;
        nextCursor: string;
      }>();
      mocks.fetchLogs.mockReturnValueOnce(stalePoll.promise);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(8000);
      });
      expect(mocks.fetchLogs).toHaveBeenCalledTimes(2);
      expect(mocks.fetchLogs).toHaveBeenLastCalledWith({
        limit: 10000,
        cursor: 'cursor-1',
        after: 100,
      });

      // External config refresh observes loggingToFile: false.
      mocks.getConfig.mockResolvedValueOnce({
        ...useConfigStore.getState().config!,
        loggingToFile: false,
      });
      await act(async () => {
        await useConfigStore.getState().fetchConfig(undefined, true);
      });

      expect(findDownloadButton(mounted.root).props.disabled).toBe(true);
      expect(byTestId(mounted.root, 'logs-empty-enable-button').props.disabled).toBe(false);
      expect(storageMap.get('logsPage.autoRefresh')).toBe('false');

      // Resolving the stale in-flight poll must NOT repopulate buffer or enable download.
      await act(async () => {
        stalePoll.resolve({
          lines: ['[2026-11-12 10:01:00] [info] [log_writer.go:2] stale-after-disable'],
          'line-count': 1,
          'latest-timestamp': 200,
          latestAfter: 200,
          nextCursor: 'stale-cursor-2',
        });
        await stalePoll.promise;
      });

      expect(findDownloadButton(mounted.root).props.disabled).toBe(true);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(16000);
      });
      expect(mocks.fetchLogs).toHaveBeenCalledTimes(2);

      // Re-enabling file logging later must start a fresh load without stale cursor
      // and must NOT resume the previous auto-refresh interval.
      mocks.fetchLogs.mockResolvedValueOnce({
        lines: [],
        'line-count': 0,
        'latest-timestamp': 0,
      });
      await act(async () => {
        byTestId(mounted.root, 'logs-empty-enable-button').props.onClick();
      });

      expect(mocks.fetchLogs).toHaveBeenCalledTimes(3);
      expect(mocks.fetchLogs).toHaveBeenLastCalledWith({ limit: 10000 });
      expect(findAutoRefreshSwitch(mounted.root).props.checked).toBe(false);
      expect(findDownloadButton(mounted.root).props.disabled).toBe(true);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(24000);
      });
      expect(mocks.fetchLogs).toHaveBeenCalledTimes(3);
    } finally {
      vi.useRealTimers();
    }
  });

  it('preserves buffer, in-flight header refresh load, and persisted autoRefresh across transient config === null', async () => {
    vi.useFakeTimers();
    try {
      useConfigStore.setState((state) => ({
        ...state,
        config: {
          ...state.config!,
          loggingToFile: true,
        },
      }));
      const client = buildAuthorityClient({ mode: 'managed', writable: true });
      const mounted = await mountLogsPage(client, '/logs');

      expect(mocks.fetchLogs).toHaveBeenCalledTimes(1);
      expect(findDownloadButton(mounted.root).props.disabled).toBe(false);

      await act(async () => {
        findAutoRefreshSwitch(mounted.root).props.onChange(true);
      });
      expect(findAutoRefreshSwitch(mounted.root).props.checked).toBe(true);
      expect(storageMap.get('logsPage.autoRefresh')).toBe('true');

      // 1) MainLayout.handleRefreshAll: clearCache() sets config: null while
      // fetchConfig(undefined, true) and triggerHeaderRefresh() -> loadLogs(false) run concurrently.
      const pendingConfig = createDeferred<Record<string, unknown>>();
      const pendingHeaderLogs = createDeferred<{
        lines: string[];
        'line-count': number;
        'latest-timestamp': number;
        latestAfter: number;
        nextCursor: string;
      }>();
      mocks.getConfig.mockReturnValueOnce(pendingConfig.promise);
      mocks.fetchLogs.mockReturnValueOnce(pendingHeaderLogs.promise);

      let refreshPromise: Promise<unknown> | undefined;
      let headerRefreshPromise: Promise<void> | undefined;
      await act(async () => {
        useConfigStore.getState().clearCache();
        refreshPromise = useConfigStore.getState().fetchConfig(undefined, true).catch(() => undefined);
        headerRefreshPromise = triggerHeaderRefresh();
      });
      expect(storageMap.get('logsPage.autoRefresh')).toBe('true');

      // Concurrent loadLogs(false) started by triggerHeaderRefresh() must NOT be aborted by config === null.
      await act(async () => {
        pendingHeaderLogs.resolve({
          lines: ['[2026-11-12 10:02:00] [info] [log_writer.go:3] refreshed-during-null'],
          'line-count': 1,
          'latest-timestamp': 300,
          latestAfter: 300,
          nextCursor: 'cursor-after-null',
        });
        await headerRefreshPromise;
      });
      expect(storageMap.get('logsPage.autoRefresh')).toBe('true');

      // 2) Failed refetch during transient config === null leaves persisted autoRefresh intact;
      // recovering config with a failing follow-up loadLogs proves the earlier buffer stayed intact.
      mocks.fetchLogs.mockRejectedValueOnce(new Error('temporary log fetch failure'));
      await act(async () => {
        pendingConfig.reject(new Error('transient config failure'));
        await refreshPromise;
      });
      expect(storageMap.get('logsPage.autoRefresh')).toBe('true');

      await act(async () => {
        useConfigStore.setState({
          config: {
            debug: false,
            requestLog: false,
            loggingToFile: true,
            logsMaxTotalSizeMb: 0,
            errorLogsMaxFiles: 10,
            pprofEnable: false,
            pprofAddr: '127.0.0.1:8316',
            raw: {},
          },
          loading: false,
          error: null,
        });
      });

      expect(findAutoRefreshSwitch(mounted.root).props.checked).toBe(true);
      expect(storageMap.get('logsPage.autoRefresh')).toBe('true');
      expect(findDownloadButton(mounted.root).props.disabled).toBe(false);
      await act(async () => {
        findDownloadButton(mounted.root).props.onClick();
      });
      expect(mocks.downloadBlob).toHaveBeenCalledTimes(1);
      expect(await mocks.downloadBlob.mock.calls[0][0].blob.text()).toContain(
        'refreshed-during-null'
      );

      // Polling resumes because autoRefresh remained true across null -> true.
      mocks.fetchLogs.mockResolvedValueOnce({
        lines: ['[2026-11-12 10:03:00] [info] [log_writer.go:4] polled-after-recovery'],
        'line-count': 1,
        'latest-timestamp': 400,
        latestAfter: 400,
        nextCursor: 'cursor-4',
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(8000);
      });
      expect(mocks.fetchLogs).toHaveBeenCalledTimes(4);

      // 3) If a subsequent null -> false lands, full cleanup STILL executes.
      await act(async () => {
        useConfigStore.getState().clearCache();
      });
      await act(async () => {
        useConfigStore.setState({
          config: {
            debug: false,
            requestLog: false,
            loggingToFile: false,
            logsMaxTotalSizeMb: 0,
            errorLogsMaxFiles: 10,
            pprofEnable: false,
            pprofAddr: '127.0.0.1:8316',
            raw: {},
          },
          loading: false,
          error: null,
        });
      });
      expect(storageMap.get('logsPage.autoRefresh')).toBe('false');
      expect(findDownloadButton(mounted.root).props.disabled).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });
  it.each(['managed', 'external'] as const)(
    'edits all five observability.logs.* fields under %s capability while keeping observability.pprof.* read-only',
    async (mode) => {
      const client = buildAuthorityClient({ mode, writable: true });
      const mounted = await mountLogsPage(client, '/logs?tab=settings');

      const debugCheckbox = byTestId(mounted.root, 'logs-setting-row-debug').findByType('input');
      const requestLogCheckbox = byTestId(mounted.root, 'logs-setting-row-request-log').findByType(
        'input'
      );
      const maxSizeInput = byTestId(mounted.root, 'logs-setting-row-max-size').findByType('input');
      const errorFilesInput = byTestId(
        mounted.root,
        'logs-setting-row-error-max-files'
      ).findByType('input');

      await act(async () => {
        debugCheckbox.props.onChange({ target: { checked: true } });
      });
      expect(mocks.updateDebug).toHaveBeenCalledWith(true);
      expect(useConfigStore.getState().config?.debug).toBe(true);

      await act(async () => {
        requestLogCheckbox.props.onChange({ target: { checked: true } });
      });
      expect(mocks.updateRequestLog).toHaveBeenCalledWith(true);
      expect(useConfigStore.getState().config?.requestLog).toBe(true);

      await act(async () => {
        maxSizeInput.props.onChange({ target: { value: '128' } });
      });
      await act(async () => {
        byTestId(mounted.root, 'logs-setting-save-max-size').props.onClick();
      });
      expect(mocks.updateLogsMaxTotalSizeMb).toHaveBeenCalledWith(128);
      expect(useConfigStore.getState().config?.logsMaxTotalSizeMb).toBe(128);

      await act(async () => {
        errorFilesInput.props.onChange({ target: { value: '25' } });
      });
      await act(async () => {
        byTestId(mounted.root, 'logs-setting-save-error-max-files').props.onClick();
      });
      expect(mocks.updateErrorLogsMaxFiles).toHaveBeenCalledWith(25);
      expect(useConfigStore.getState().config?.errorLogsMaxFiles).toBe(25);

      const pprofSection = byTestId(mounted.root, 'logs-settings-pprof-readonly');
      expect(pprofSection.findAllByType('input')).toHaveLength(0);
      expect(pprofSection.findAllByType('button')).toHaveLength(0);
      expect(byTestId(mounted.root, 'logs-readonly-pprof-addr').children.join('')).toBe(
        '127.0.0.1:8316'
      );
    }
  );

  it('validates non-negative integer inputs before sending retention writes', async () => {
    const client = buildAuthorityClient({ mode: 'managed', writable: true });
    const mounted = await mountLogsPage(client, '/logs?tab=settings');

    const maxSizeInput = byTestId(mounted.root, 'logs-setting-row-max-size').findByType('input');
    await act(async () => {
      maxSizeInput.props.onChange({ target: { value: '-5' } });
    });
    await act(async () => {
      byTestId(mounted.root, 'logs-setting-save-max-size').props.onClick();
    });

    expect(mocks.updateLogsMaxTotalSizeMb).not.toHaveBeenCalled();
    expect(maxSizeInput.props['aria-invalid']).toBe(true);
  });

  it('disables all log settings controls when posture or version capability refuses edits', async () => {
    const client = buildAuthorityClient({
      mode: 'external',
      writable: false,
      reason: 'external_cpa_version_unsupported',
    });
    const mounted = await mountLogsPage(client, '/logs');

    expect(byTestId(mounted.root, 'logs-empty-enable-button').props.disabled).toBe(true);
    expect(byTestId(mounted.root, 'logs-empty-authority-notice')).toBeDefined();

    await act(async () => {
      byTestId(mounted.root, 'logs-tab-settings').props.onClick();
    });

    expect(byTestId(mounted.root, 'logs-settings-authority-notice')).toBeDefined();
    for (const rowId of [
      'logs-setting-row-logging-to-file',
      'logs-setting-row-debug',
      'logs-setting-row-request-log',
      'logs-setting-row-max-size',
      'logs-setting-row-error-max-files',
    ]) {
      const input = byTestId(mounted.root, rowId).findByType('input');
      expect(input.props.disabled).toBe(true);
    }
    expect(byTestId(mounted.root, 'logs-setting-save-max-size').props.disabled).toBe(true);
    expect(byTestId(mounted.root, 'logs-setting-save-error-max-files').props.disabled).toBe(true);
  });

  it('surfaces a write refusal and preserves stored state when enabling file logging fails', async () => {
    mocks.updateLoggingToFile.mockRejectedValueOnce({
      message: 'Request failed with status code 409',
      data: { code: 'external_management_key_rejected' },
    });
    const client = buildAuthorityClient({ mode: 'external', writable: true });
    const mounted = await mountLogsPage(client, '/logs');

    await act(async () => {
      byTestId(mounted.root, 'logs-empty-enable-button').props.onClick();
    });

    expect(useConfigStore.getState().config?.loggingToFile).toBe(false);
    expect(mocks.fetchLogs).not.toHaveBeenCalled();
    const errorAlert = byTestId(mounted.root, 'logs-empty-save-error');
    expect(errorAlert.props.role).toBe('alert');
  });

  it.each(['false', 'true', 1, null, undefined, {}] as const)(
    'fails closed when capability.writable is non-boolean (%j)',
    async (nonBooleanWritable) => {
      const client = buildAuthorityClient({
        mode: 'managed',
        writable: nonBooleanWritable,
      });
      const mounted = await mountLogsPage(client, '/logs');

      expect(byTestId(mounted.root, 'logs-empty-enable-button').props.disabled).toBe(true);

      await act(async () => {
        byTestId(mounted.root, 'logs-tab-settings').props.onClick();
      });

      for (const rowId of [
        'logs-setting-row-logging-to-file',
        'logs-setting-row-debug',
        'logs-setting-row-request-log',
        'logs-setting-row-max-size',
        'logs-setting-row-error-max-files',
      ]) {
        expect(byTestId(mounted.root, rowId).findByType('input').props.disabled).toBe(true);
      }
    }
  );

  it('ignores a stale old-key 401 after switching to a valid management key', async () => {
    const oldKeyProbe = createDeferred<unknown>();
    const newKeyProbe = createDeferred<unknown>();

    const client: PublicApiClient = {
      get: ((_resource: string, credential?: { kind: string; value: string }) => {
        if (credential?.value === 'old-key') return oldKeyProbe.promise;
        return newKeyProbe.promise;
      }) as PublicApiClient['get'],
      post: vi.fn() as PublicApiClient['post'],
      put: vi.fn() as PublicApiClient['put'],
      uploadArtifact: vi.fn() as PublicApiClient['uploadArtifact'],
    };

    function AuthorityHarness({ managementKey }: { managementKey: string }) {
      const authority = useLogsConfigAuthority({
        connectionStatus: 'connected',
        apiBase: '/console/api/v2/cpamp',
        managementKey,
        authorityClient: client,
      });
      return (
        <button
          type="button"
          data-testid="harness-can-edit"
          disabled={!authority.canEditLogs}
          onClick={() => {
            void authority.refresh();
          }}
        />
      );
    }

    await act(async () => {
      renderer = create(<AuthorityHarness managementKey="old-key" />);
    });

    await act(async () => {
      renderer!.update(<AuthorityHarness managementKey="new-key" />);
    });

    await act(async () => {
      newKeyProbe.resolve({
        capability: { mode: 'managed', readable: true, writable: true, rulesWritable: true },
      });
      await newKeyProbe.promise;
    });

    expect(byTestId(renderer!.root, 'harness-can-edit').props.disabled).toBe(false);

    await act(async () => {
      oldKeyProbe.reject(new PublicApiError('authentication', 401, 'unauthorized'));
      await Promise.resolve();
    });

    expect(dispatchWindowEvent).not.toHaveBeenCalled();
    expect(byTestId(renderer!.root, 'harness-can-edit').props.disabled).toBe(false);

    // Also verify an in-flight refresh() started under one key does not fire unauthorized after rotating to a newer key
    const staleRefreshProbe = createDeferred<unknown>();
    const rotatedKeyProbe = createDeferred<unknown>();
    let rotatedCall = 0;
    const refreshRaceClient: PublicApiClient = {
      get: ((_resource: string, credential?: { kind: string; value: string }) => {
        rotatedCall += 1;
        if (credential?.value === 'key-before-rotate') {
          if (rotatedCall === 1) {
            return Promise.resolve({
              capability: { mode: 'managed', readable: true, writable: true, rulesWritable: true },
            });
          }
          return staleRefreshProbe.promise;
        }
        return rotatedKeyProbe.promise;
      }) as PublicApiClient['get'],
      post: vi.fn() as PublicApiClient['post'],
      put: vi.fn() as PublicApiClient['put'],
      uploadArtifact: vi.fn() as PublicApiClient['uploadArtifact'],
    };

    function RefreshHarness({ managementKey }: { managementKey: string }) {
      const authority = useLogsConfigAuthority({
        connectionStatus: 'connected',
        apiBase: '/console/api/v2/cpamp',
        managementKey,
        authorityClient: refreshRaceClient,
      });
      return (
        <button
          type="button"
          data-testid="refresh-harness-btn"
          disabled={!authority.canEditLogs}
          onClick={() => {
            void authority.refresh();
          }}
        />
      );
    }

    await act(async () => {
      renderer!.unmount();
      renderer = create(<RefreshHarness managementKey="key-before-rotate" />);
    });
    expect(byTestId(renderer!.root, 'refresh-harness-btn').props.disabled).toBe(false);

    await act(async () => {
      byTestId(renderer!.root, 'refresh-harness-btn').props.onClick();
      await Promise.resolve();
    });

    await act(async () => {
      renderer!.update(<RefreshHarness managementKey="key-after-rotate" />);
    });

    await act(async () => {
      rotatedKeyProbe.resolve({
        capability: { mode: 'managed', readable: true, writable: true, rulesWritable: true },
      });
      await rotatedKeyProbe.promise;
    });
    expect(byTestId(renderer!.root, 'refresh-harness-btn').props.disabled).toBe(false);

    await act(async () => {
      staleRefreshProbe.reject(new PublicApiError('authentication', 401, 'unauthorized'));
      await Promise.resolve();
    });
    expect(dispatchWindowEvent).not.toHaveBeenCalled();
    expect(byTestId(renderer!.root, 'refresh-harness-btn').props.disabled).toBe(false);
  });

  it('does not dispatch unauthorized when an in-flight 401 settles after unmount (initial probe and refresh)', async () => {
    const initialProbe = createDeferred<unknown>();
    const refreshProbe = createDeferred<unknown>();
    let callCount = 0;

    const client: PublicApiClient = {
      get: (() => {
        callCount += 1;
        return callCount === 1 ? initialProbe.promise : refreshProbe.promise;
      }) as PublicApiClient['get'],
      post: vi.fn() as PublicApiClient['post'],
      put: vi.fn() as PublicApiClient['put'],
      uploadArtifact: vi.fn() as PublicApiClient['uploadArtifact'],
    };

    const mounted = await mountLogsPage(client, '/logs?tab=settings');
    await act(async () => {
      mounted.unmount();
    });
    renderer = undefined;

    await act(async () => {
      initialProbe.reject(new PublicApiError('authentication', 401, 'unauthorized'));
      await Promise.resolve();
    });
    expect(dispatchWindowEvent).not.toHaveBeenCalled();

    // Also verify refresh() settling with 401 after unmount does not dispatch unauthorized
    let secondCall = 0;
    const refreshClient: PublicApiClient = {
      get: (() => {
        secondCall += 1;
        if (secondCall === 1) {
          return Promise.resolve({
            capability: { mode: 'managed', readable: true, writable: true, rulesWritable: true },
          });
        }
        return refreshProbe.promise;
      }) as PublicApiClient['get'],
      post: vi.fn() as PublicApiClient['post'],
      put: vi.fn() as PublicApiClient['put'],
      uploadArtifact: vi.fn() as PublicApiClient['uploadArtifact'],
    };

    const mountedForRefresh = await mountLogsPage(refreshClient, '/logs?tab=settings');
    expect(
      byTestId(mountedForRefresh.root, 'logs-setting-row-logging-to-file').findByType('input').props
        .disabled
    ).toBe(false);

    let pendingRefresh!: Promise<void>;
    await act(async () => {
      pendingRefresh = triggerHeaderRefresh();
      await Promise.resolve();
    });

    await act(async () => {
      mountedForRefresh.unmount();
    });
    renderer = undefined;

    await act(async () => {
      refreshProbe.reject(new PublicApiError('authentication', 401, 'unauthorized'));
      await pendingRefresh;
    });
    expect(dispatchWindowEvent).not.toHaveBeenCalled();
  });

  it('dispatches unauthorized and fails closed on a current 401 (initial probe and refresh)', async () => {
    const authError = new PublicApiError('authentication', 401, 'unauthorized');
    let callCount = 0;
    const client: PublicApiClient = {
      get: (() => {
        callCount += 1;
        if (callCount === 1) {
          return Promise.reject(authError);
        }
        if (callCount === 2) {
          return Promise.resolve({
            capability: { mode: 'managed', readable: true, writable: true, rulesWritable: true },
          });
        }
        return Promise.reject(authError);
      }) as PublicApiClient['get'],
      post: vi.fn() as PublicApiClient['post'],
      put: vi.fn() as PublicApiClient['put'],
      uploadArtifact: vi.fn() as PublicApiClient['uploadArtifact'],
    };

    const mounted = await mountLogsPage(client, '/logs?tab=settings');
    expect(dispatchWindowEvent).toHaveBeenCalledTimes(1);
    expect(dispatchWindowEvent.mock.calls[0]?.[0]?.type).toBe('unauthorized');
    expect(
      byTestId(mounted.root, 'logs-setting-row-logging-to-file').findByType('input').props.disabled
    ).toBe(true);

    // Refresh recovers to writable (no new unauthorized event), then a subsequent refresh 401 dispatches unauthorized
    await act(async () => {
      await triggerHeaderRefresh();
    });
    expect(dispatchWindowEvent).toHaveBeenCalledTimes(1);
    expect(
      byTestId(mounted.root, 'logs-setting-row-logging-to-file').findByType('input').props.disabled
    ).toBe(false);

    await act(async () => {
      await triggerHeaderRefresh();
    });
    expect(dispatchWindowEvent).toHaveBeenCalledTimes(2);
    expect(dispatchWindowEvent.mock.calls[1]?.[0]?.type).toBe('unauthorized');
    expect(
      byTestId(mounted.root, 'logs-setting-row-logging-to-file').findByType('input').props.disabled
    ).toBe(true);
  });

  it('uses LogsAuthorityClientContext in Full Web and supports standalone Panel Lite editing with connection and write-refusal guards', async () => {
    const getCalls: string[] = [];
    const refusedClient: PublicApiClient = {
      get: (async (resource: string) => {
        getCalls.push(resource);
        return {
          capability: {
            mode: 'external',
            readable: true,
            writable: false,
            rulesWritable: false,
            reason: 'external_cpa_version_unsupported',
          },
        };
      }) as PublicApiClient['get'],
      post: vi.fn() as PublicApiClient['post'],
      put: vi.fn() as PublicApiClient['put'],
      uploadArtifact: vi.fn() as PublicApiClient['uploadArtifact'],
    };

    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/logs']}>
          <LogsAuthorityClientContext.Provider value={refusedClient}>
            <LogsPage />
          </LogsAuthorityClientContext.Provider>
        </MemoryRouter>
      );
    });
    expect(getCalls).toEqual(['gateway/config/logs-capability']);
    expect(byTestId(renderer!.root, 'logs-empty-enable-button').props.disabled).toBe(true);

    // Panel Lite standalone: disconnected state keeps controls disabled and makes no authority request.
    mocks.authState.connectionStatus = 'disconnected';
    await act(async () => {
      renderer!.unmount();
      renderer = create(
        <MemoryRouter initialEntries={['/logs?tab=settings']}>
          <LogsPage />
        </MemoryRouter>
      );
    });
    expect(getCalls).toHaveLength(1);
    expect(byTestId(renderer!.root, 'logs-setting-row-logging-to-file').findByType('input').props.disabled).toBe(true);

    // Panel Lite standalone connected: edits CPA log settings directly and preserves state on write failure.
    mocks.authState.connectionStatus = 'connected';
    mocks.updateDebug.mockRejectedValueOnce(new Error('panel write refused'));
    await act(async () => {
      renderer!.unmount();
      renderer = create(
        <MemoryRouter initialEntries={['/logs']}>
          <LogsPage />
        </MemoryRouter>
      );
    });
    expect(getCalls).toHaveLength(1);
    expect(byTestId(renderer!.root, 'logs-empty-enable-button').props.disabled).toBe(false);

    await act(async () => {
      byTestId(renderer!.root, 'logs-tab-settings').props.onClick();
    });
    await act(async () => {
      byTestId(renderer!.root, 'logs-setting-row-debug').findByType('input').props.onChange({
        target: { checked: true },
      });
    });
    expect(useConfigStore.getState().config?.debug).toBe(false);
    expect(byTestId(renderer!.root, 'logs-settings-error').props.role).toBe('alert');

    await act(async () => {
      byTestId(renderer!.root, 'logs-setting-row-debug').findByType('input').props.onChange({
        target: { checked: true },
      });
    });
    expect(useConfigStore.getState().config?.debug).toBe(true);
  });
});
