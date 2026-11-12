import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import '@/i18n';
import type { PublicApiClient } from '@/fullweb/publicApi';
import { useConfigStore, useNotificationStore } from '@/stores';
import { LogsPage } from './LogsPage';

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
  writable: boolean;
  reason?: string;
}): { client: PublicApiClient; calls: string[] } {
  const calls: string[] = [];
  return {
    calls,
    client: {
      get: (async (resource: string) => {
        calls.push(resource);
        return {
          capability: {
            readable: true,
            rulesWritable: capability.writable,
            ...capability,
          },
        };
      }) as PublicApiClient['get'],
      post: vi.fn() as PublicApiClient['post'],
      put: vi.fn() as PublicApiClient['put'],
      uploadArtifact: vi.fn() as PublicApiClient['uploadArtifact'],
    },
  };
}

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
  vi.clearAllMocks();
});

describe('LogsPage settings and empty-state file logging control', () => {
  it('enables file logging from the empty state, loads logs automatically, and returns to disabled state when turned off', async () => {
    const { client, calls } = buildAuthorityClient({ mode: 'managed', writable: true });
    const mounted = await mountLogsPage(client, '/logs');

    expect(calls).toEqual(['gateway/config']);
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
      const { client } = buildAuthorityClient({ mode, writable: true });
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
    const { client } = buildAuthorityClient({ mode: 'managed', writable: true });
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
    const { client } = buildAuthorityClient({
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
    const { client } = buildAuthorityClient({ mode: 'external', writable: true });
    const mounted = await mountLogsPage(client, '/logs');

    await act(async () => {
      byTestId(mounted.root, 'logs-empty-enable-button').props.onClick();
    });

    expect(useConfigStore.getState().config?.loggingToFile).toBe(false);
    expect(mocks.fetchLogs).not.toHaveBeenCalled();
    const errorAlert = byTestId(mounted.root, 'logs-empty-save-error');
    expect(errorAlert.props.role).toBe('alert');
  });
});
