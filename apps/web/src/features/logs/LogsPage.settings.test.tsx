import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import '@/i18n';
import { PublicApiError, type PublicApiClient } from '@/fullweb/publicApi';
import { triggerHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useConfigStore, useNotificationStore } from '@/stores';
import { LogsPage } from './LogsPage';
import { useLogsConfigAuthority } from './useLogsConfigAuthority';

const mocks = vi.hoisted(() => ({
  getConfig: vi.fn(),
  updateLoggingToFile: vi.fn(),
  updateDebug: vi.fn(),
  updateRequestLog: vi.fn(),
  updateLogsMaxTotalSizeMb: vi.fn(),
  updateErrorLogsMaxFiles: vi.fn(),
  fetchLogs: vi.fn(),
  fetchErrorLogs: vi.fn(),
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

vi.mock('@/hooks/useLocalStorage', () => ({
  useLocalStorage: (_key: string, initial: unknown) => [initial, vi.fn()],
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

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  dispatchWindowEvent.mockClear();
  vi.stubGlobal('window', {
    dispatchEvent: dispatchWindowEvent,
    setTimeout: globalThis.setTimeout.bind(globalThis),
    clearTimeout: globalThis.clearTimeout.bind(globalThis),
    setInterval: globalThis.setInterval.bind(globalThis),
    clearInterval: globalThis.clearInterval.bind(globalThis),
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

  mocks.updateLoggingToFile.mockResolvedValue({ ok: true });
  mocks.updateDebug.mockResolvedValue({ ok: true });
  mocks.updateRequestLog.mockResolvedValue({ ok: true });
  mocks.updateLogsMaxTotalSizeMb.mockResolvedValue({ ok: true });
  mocks.updateErrorLogsMaxFiles.mockResolvedValue({ ok: true });
  mocks.fetchLogs.mockResolvedValue({
    lines: ['[2026-11-12 10:00:00] [info] [gin_logger.go:94] 200 | 12ms | 127.0.0.1 | GET "/v1/models"'],
    'line-count': 1,
    'latest-timestamp': 1794477600,
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
  it('enables file logging from the empty state, loads logs automatically, and returns to disabled state when turned off', async () => {
    const client = buildAuthorityClient({ mode: 'managed', writable: true });
    const mounted = await mountLogsPage(client, '/logs');

    expect(mocks.fetchLogs).not.toHaveBeenCalled();

    const enableButton = byTestId(mounted.root, 'logs-empty-enable-button');
    expect(enableButton.props.disabled).toBe(false);

    await act(async () => {
      enableButton.props.onClick();
    });

    expect(mocks.updateLoggingToFile).toHaveBeenCalledWith(true);
    expect(useConfigStore.getState().config?.loggingToFile).toBe(true);
    expect(mocks.fetchLogs).toHaveBeenCalledTimes(1);
    expect(JSON.stringify(mounted.toJSON())).toContain('/v1/models');
    expect(maybeTestId(mounted.root, 'logs-empty-enable-button')).toBeUndefined();

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

    await act(async () => {
      byTestId(mounted.root, 'logs-tab-logs').props.onClick();
    });

    expect(JSON.stringify(mounted.toJSON())).not.toContain('/v1/models');
    expect(byTestId(mounted.root, 'logs-empty-enable-button').props.disabled).toBe(false);
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
});
