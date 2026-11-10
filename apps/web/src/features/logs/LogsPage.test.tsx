import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { LogsPage } from './LogsPage';

const mocks = vi.hoisted(() => ({
  fetchErrorLogs: vi.fn(),
  config: { requestLog: true, loggingToFile: true },
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock('react-router-dom', () => ({
  useSearchParams: () => [new URLSearchParams('tab=errors'), vi.fn()],
}));

vi.mock('@/stores', () => ({
  useAuthStore: (selector: (state: Record<string, unknown>) => unknown) =>
    selector({
      connectionStatus: 'connected',
      apiBase: 'http://cpa.example:8317',
      managementKey: 'management-key',
    }),
  useConfigStore: (selector: (state: { config: typeof mocks.config }) => unknown) =>
    selector({ config: mocks.config }),
  useNotificationStore: () => ({ showNotification: vi.fn(), showConfirmation: vi.fn() }),
}));

vi.mock('@/services/api/logs', () => ({
  logsApi: { fetchErrorLogs: mocks.fetchErrorLogs, fetchLogs: vi.fn() },
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

const bannerText = 'logs.error_logs_request_log_enabled';

let renderer: ReactTestRenderer | undefined;

async function renderErrorTab() {
  await act(async () => {
    renderer = create(<LogsPage />);
  });
  return renderer!;
}

const pageText = () => JSON.stringify(renderer?.toJSON() ?? null);

const createDeferred = <T,>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((nextResolve) => {
    resolve = nextResolve;
  });
  return { promise, resolve };
};

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  mocks.config.requestLog = true;
  mocks.config.loggingToFile = true;
});

afterEach(async () => {
  if (renderer) await act(async () => renderer?.unmount());
  renderer = undefined;
  vi.clearAllMocks();
});

describe('LogsPage error log listing', () => {
  it('shows the request-log banner only once an empty listing has loaded', async () => {
    const listing = createDeferred<{ files: Array<{ name: string }> }>();
    mocks.fetchErrorLogs.mockReturnValue(listing.promise);

    await renderErrorTab();
    expect(pageText()).not.toContain(bannerText);

    await act(async () => listing.resolve({ files: [] }));
    expect(pageText()).toContain(bannerText);
  });

  it('hides the request-log banner while a listing with files is loaded', async () => {
    mocks.fetchErrorLogs.mockResolvedValue({ files: [{ name: 'error-2026-11-10.log' }] });

    await renderErrorTab();
    expect(pageText()).toContain('error-2026-11-10.log');
    expect(pageText()).not.toContain(bannerText);
  });

  it('hides the request-log banner when request logging is disabled', async () => {
    mocks.config.requestLog = false;
    mocks.fetchErrorLogs.mockResolvedValue({ files: [] });

    await renderErrorTab();
    expect(pageText()).not.toContain(bannerText);
  });
});
