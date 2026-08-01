import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { renderToStaticMarkup } from 'react-dom/server';
import type { TFunction } from 'i18next';
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from 'react-router-dom';
import type {
  CodexInspectionRun,
  CodexInspectionRunDetail,
  ManagerConfig,
} from '@/services/api/usageService';
import { CodexInspectionStopButton } from '@/features/monitoring/components/CodexInspectionStopButton';
import {
  findCancellableRun,
  getRunStatusLabel,
  hasActiveRun,
} from '@/features/monitoring/model/serverCodexInspectionLifecycle';
import { ServerCodexInspectionPage } from './ServerCodexInspectionPage';

const mocks = vi.hoisted(() => ({
  getManagerConfig: vi.fn(),
  listRuns: vi.fn(),
  getRun: vi.fn(),
  runInspection: vi.fn(),
  cancelRun: vi.fn(),
  getHeaderSnapshots: vi.fn(),
  showNotification: vi.fn(),
  showConfirmation: vi.fn(),
  t: (key: string, options?: Record<string, unknown>) =>
    options?.status ? `${key}:${String(options.status)}` : key,
}));

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return {
    ...actual,
    initReactI18next: { type: '3rdParty', init: () => undefined },
    useTranslation: () => ({
      t: mocks.t,
      i18n: { language: 'en' },
    }),
  };
});

vi.mock('react-router-dom', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-router-dom')>();
  return { ...actual, useNavigate: () => vi.fn() };
});

vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => ({
    checking: false,
    panelHostMode: 'manager_embedded',
    panelBase: 'http://manager.local',
    managerServiceBase: 'http://manager.local',
    managerServiceAvailable: true,
    requestMonitoringAvailable: true,
    modelPricesAvailable: true,
    serverCodexInspectionAvailable: true,
    dockerSetupAvailable: true,
    externalManagerConfigAvailable: false,
    reason: '',
  }),
}));

vi.mock('@/stores', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/stores')>();
  return {
    ...actual,
    useAuthStore: (selector: (state: { managementKey: string }) => unknown) =>
      selector({ managementKey: 'management-key' }),
    useNotificationStore: (
      selector: (state: {
        showNotification: typeof mocks.showNotification;
        showConfirmation: typeof mocks.showConfirmation;
      }) => unknown
    ) =>
      selector({
        showNotification: mocks.showNotification,
        showConfirmation: mocks.showConfirmation,
      }),
  };
});

vi.mock('@/services/api/usageService', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/services/api/usageService')>();
  return {
    ...actual,
    usageServiceApi: {
      ...actual.usageServiceApi,
      getManagerConfig: mocks.getManagerConfig,
      listCodexInspectionRuns: mocks.listRuns,
      getCodexInspectionRun: mocks.getRun,
      runCodexInspection: mocks.runInspection,
      cancelCodexInspectionRun: mocks.cancelRun,
    },
    monitoringAnalyticsApi: {
      ...actual.monitoringAnalyticsApi,
      getHeaderSnapshots: mocks.getHeaderSnapshots,
    },
  };
});

const t = mocks.t as TFunction;

const managerConfig: ManagerConfig = {
  cpaConnection: {
    cpaBaseUrl: 'http://cpa.local',
    managementKey: 'management-key',
  },
  collector: {
    enabled: true,
    collectorMode: 'auto',
    queue: 'usage',
    popSide: 'right',
    batchSize: 100,
    pollIntervalMs: 500,
    queryLimit: 50_000,
  },
  codexInspection: {
    enabled: true,
    schedule: { mode: 'interval', intervalMinutes: 60, timePoints: [], timeZone: '' },
    targetTypes: ['codex'],
    workers: 1,
    deleteWorkers: 1,
    timeout: 15_000,
    retries: 0,
    usedPercentThreshold: 100,
    sampleSize: 0,
    autoActionMode: 'none',
    autoRecoverEnabled: false,
  },
  externalUsageService: {
    enabled: false,
    serviceBase: '',
  },
};

const run = (overrides: Partial<CodexInspectionRun> = {}): CodexInspectionRun => ({
  id: 1,
  triggerType: 'manual',
  triggerKey: 'manual',
  status: 'running',
  startedAtMs: 1_000,
  totalFiles: 0,
  probeSetCount: 0,
  sampledCount: 0,
  disabledCount: 0,
  enabledCount: 0,
  deleteCount: 0,
  disableCount: 0,
  enableCount: 0,
  reauthCount: 0,
  keepCount: 0,
  createdAtMs: 1_000,
  updatedAtMs: 1_000,
  active: true,
  cancellable: true,
  ...overrides,
});

const detail = (inspectionRun: CodexInspectionRun): CodexInspectionRunDetail => ({
  run: inspectionRun,
  results: [],
  logs: [],
});

const textContent = (node: ReactTestInstance): string =>
  node.children.map((child) => (typeof child === 'string' ? child : textContent(child))).join('');

const flush = async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  await Promise.resolve();
};

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((nextResolve) => {
    resolve = nextResolve;
  });
  return { promise, resolve };
};

describe('ServerCodexInspectionPage lifecycle controls', () => {
  beforeAll(() => {
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  });

  beforeEach(() => {
    vi.clearAllMocks();
    vi.stubGlobal('window', {
      setInterval: vi.fn(() => 1),
      clearInterval: vi.fn(),
    });
    mocks.getManagerConfig.mockResolvedValue({ config: managerConfig, source: 'db' });
    mocks.getHeaderSnapshots.mockResolvedValue({ items: [] });
    mocks.showConfirmation.mockImplementation((options: { onConfirm: () => void }) => {
      options.onConfirm();
    });
  });

  it('shows a stop button for a truly running run', () => {
    const markup = renderToStaticMarkup(
      <CodexInspectionStopButton run={run()} busy={false} onClick={vi.fn()} t={t} />
    );

    expect(markup).toContain('monitoring.server_codex_inspection_stop');
    expect(markup).not.toContain('disabled=""');
  });

  it('disables the stop button while the run is cancelling', () => {
    const markup = renderToStaticMarkup(
      <CodexInspectionStopButton
        run={run({ status: 'cancelling' })}
        busy={false}
        onClick={vi.fn()}
        t={t}
      />
    );

    expect(markup).toContain('monitoring.server_codex_inspection_cancelling');
    expect(markup).toContain('disabled=""');
  });

  it('shows a disabled cancelling action for a run owned by another instance', () => {
    const markup = renderToStaticMarkup(
      <CodexInspectionStopButton
        run={run({ status: 'cancelling', active: true, cancellable: false })}
        busy={false}
        onClick={vi.fn()}
        t={t}
      />
    );

    expect(markup).toContain('monitoring.server_codex_inspection_cancelling');
    expect(markup).toContain('disabled=""');
  });

  it('shows an explicit stopping label while the cancel request is in flight', () => {
    const markup = renderToStaticMarkup(
      <CodexInspectionStopButton run={run()} busy onClick={vi.fn()} t={t} />
    );

    expect(markup).toContain('monitoring.server_codex_inspection_cancelling');
    expect(markup).toContain('disabled=""');
  });

  it('renders cancelled and unknown statuses without treating them as active', () => {
    expect(
      getRunStatusLabel(run({ status: 'cancelled', active: false, cancellable: false }), t)
    ).toBe('monitoring.codex_inspection_status_cancelled');
    expect(
      getRunStatusLabel(run({ status: 'future_state', active: false, cancellable: false }), t)
    ).toBe('monitoring.codex_inspection_status_unknown:future_state');
    expect(
      renderToStaticMarkup(
        <CodexInspectionStopButton
          run={run({ status: 'cancelled', active: false, cancellable: false })}
          busy={false}
          onClick={vi.fn()}
          t={t}
        />
      )
    ).toBe('');
    expect(
      renderToStaticMarkup(
        <CodexInspectionStopButton
          run={run({ active: undefined, cancellable: undefined })}
          busy={false}
          onClick={vi.fn()}
          t={t}
        />
      )
    ).toBe('');
  });

  it('selects only the lease-backed active run from mixed history', () => {
    const stale = run({ id: 1, status: 'running', active: false, cancellable: false });
    const completed = run({ id: 2, status: 'completed', active: false, cancellable: false });
    const active = run({ id: 3, status: 'running', active: true, cancellable: true });

    expect(findCancellableRun([stale, completed, active], stale)?.id).toBe(active.id);
  });

  it('treats the runs list as authoritative over stale detail activity fields', () => {
    const terminalListItem = run({
      id: 1,
      status: 'interrupted',
      active: false,
      cancellable: false,
      finishedAtMs: 2_000,
    });
    const staleDetail = run({ id: 1, status: 'running', active: true, cancellable: true });

    expect(hasActiveRun([terminalListItem], staleDetail)).toBe(false);
    expect(findCancellableRun([terminalListItem], staleDetail)).toBeNull();
  });

  it('ignores an out-of-order detail response for a previously selected run', async () => {
    const first = run({
      id: 1,
      status: 'completed',
      active: false,
      cancellable: false,
      finishedAtMs: 2_000,
    });
    const second = run({
      id: 2,
      status: 'failed',
      active: false,
      cancellable: false,
      finishedAtMs: 3_000,
    });
    const firstReload = deferred<CodexInspectionRunDetail>();
    const secondLoad = deferred<CodexInspectionRunDetail>();
    mocks.listRuns.mockResolvedValue({ items: [first, second] });
    mocks.getRun
      .mockResolvedValueOnce(detail(first))
      .mockImplementation(async (_base: string, _key: string, id: number) =>
        id === second.id ? secondLoad.promise : firstReload.promise
      );

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <MemoryRouter>
          <ServerCodexInspectionPage />
        </MemoryRouter>
      );
      await flush();
    });

    const findTab = (id: number) =>
      renderer!.root
        .findAll((node) => node.props.role === 'tab')
        .find((node) => String(node.props['aria-label']).includes(`#${id}`));

    await act(async () => {
      findTab(second.id)!.props.onClick();
      await Promise.resolve();
    });
    await act(async () => {
      findTab(first.id)!.props.onClick();
      await Promise.resolve();
    });
    await act(async () => {
      firstReload.resolve(detail({ ...first, keepCount: 11 }));
      await flush();
    });
    await act(async () => {
      secondLoad.resolve(detail({ ...second, keepCount: 22 }));
      await flush();
    });

    expect(findTab(first.id)!.props['aria-selected']).toBe(true);
    expect(findTab(second.id)!.props['aria-selected']).toBe(false);

    act(() => renderer!.unmount());
  });

  it('does not replace a selected historical run with the active cancel response', async () => {
    const active = run({ id: 1, status: 'running', active: true, cancellable: true });
    const historical = run({
      id: 2,
      status: 'completed',
      active: false,
      cancellable: false,
      finishedAtMs: 2_000,
    });
    const cancelRequest = deferred<CodexInspectionRunDetail>();
    mocks.listRuns.mockResolvedValue({ items: [active, historical] });
    mocks.getRun.mockImplementation(async (_base: string, _key: string, id: number) =>
      detail(id === active.id ? active : historical)
    );
    mocks.cancelRun.mockReturnValue(cancelRequest.promise);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <MemoryRouter>
          <ServerCodexInspectionPage />
        </MemoryRouter>
      );
      await flush();
    });

    const findTab = (id: number) =>
      renderer!.root
        .findAll((node) => node.props.role === 'tab')
        .find((node) => String(node.props['aria-label']).includes(`#${id}`));

    await act(async () => {
      findTab(historical.id)!.props.onClick();
      await flush();
    });
    expect(findTab(historical.id)!.props['aria-selected']).toBe(true);

    const stopButton = renderer!.root
      .findAllByType('button')
      .find((button) => textContent(button) === 'monitoring.server_codex_inspection_stop');
    expect(stopButton).toBeDefined();

    await act(async () => {
      stopButton!.props.onClick();
      await Promise.resolve();
    });
    expect(findTab(historical.id)!.props['aria-selected']).toBe(true);

    await act(async () => {
      cancelRequest.resolve(
        detail({ ...active, status: 'cancelling', active: true, cancellable: true })
      );
      await flush();
    });

    expect(findTab(historical.id)!.props['aria-selected']).toBe(true);
    expect(findTab(active.id)!.props['aria-selected']).toBe(false);

    act(() => renderer!.unmount());
  });

  it('refreshes state and restores the stop button after a cancel API failure', async () => {
    const stale = run({ id: 1, status: 'running', active: false, cancellable: false });
    const active = run({ id: 2, status: 'running', active: true, cancellable: true });
    mocks.listRuns.mockResolvedValue({ items: [stale, active] });
    mocks.getRun.mockImplementation(async (_base: string, _key: string, id: number) =>
      detail(id === active.id ? active : stale)
    );
    mocks.cancelRun.mockRejectedValue(new Error('conflict'));

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <MemoryRouter>
          <ServerCodexInspectionPage />
        </MemoryRouter>
      );
      await flush();
    });

    const stopButton = renderer!.root
      .findAllByType('button')
      .find((button) => textContent(button) === 'monitoring.server_codex_inspection_stop');
    expect(stopButton).toBeDefined();

    await act(async () => {
      stopButton!.props.onClick();
      await flush();
      await flush();
    });

    expect(mocks.cancelRun).toHaveBeenCalledWith(
      'http://manager.local',
      'management-key',
      active.id
    );
    expect(mocks.listRuns.mock.calls.length).toBeGreaterThanOrEqual(2);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      expect.stringContaining('monitoring.server_codex_inspection_cancel_failed'),
      'error'
    );
    const restoredStopButton = renderer!.root
      .findAllByType('button')
      .find((button) => textContent(button) === 'monitoring.server_codex_inspection_stop');
    expect(restoredStopButton?.props.disabled).toBe(false);

    act(() => renderer!.unmount());
  });

  it('does not let a pre-cancel list response re-enable the stop action', async () => {
    const active = run({ id: 3, status: 'running', active: true, cancellable: true });
    const cancellingRun = {
      ...active,
      status: 'cancelling',
      active: true,
      cancellable: true,
      updatedAtMs: 2_000,
    };
    const staleRefresh = deferred<{ items: CodexInspectionRun[] }>();
    mocks.listRuns
      .mockResolvedValueOnce({ items: [active] })
      .mockReturnValueOnce(staleRefresh.promise);
    mocks.getRun.mockResolvedValue(detail(active));
    mocks.cancelRun.mockResolvedValue(detail(cancellingRun));

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <MemoryRouter>
          <ServerCodexInspectionPage />
        </MemoryRouter>
      );
      await flush();
    });

    const findButton = (label: string) =>
      renderer!.root.findAllByType('button').find((button) => textContent(button) === label);

    await act(async () => {
      findButton('common.refresh')!.props.onClick();
      await Promise.resolve();
    });
    await act(async () => {
      findButton('monitoring.server_codex_inspection_stop')!.props.onClick();
      await flush();
    });

    let stoppingButton = findButton('monitoring.server_codex_inspection_cancelling');
    expect(stoppingButton?.props.disabled).toBe(true);

    await act(async () => {
      staleRefresh.resolve({ items: [active] });
      await flush();
    });

    stoppingButton = findButton('monitoring.server_codex_inspection_cancelling');
    expect(stoppingButton?.props.disabled).toBe(true);
    expect(findButton('monitoring.server_codex_inspection_stop')).toBeUndefined();

    act(() => renderer!.unmount());
  });

  it('reconciles a fast completed run after the asynchronous start response', async () => {
    const started = run({ id: 4, status: 'running', active: true, cancellable: true });
    const completed = run({
      id: 4,
      status: 'completed',
      active: false,
      cancellable: false,
      finishedAtMs: 2_000,
    });
    mocks.listRuns
      .mockResolvedValueOnce({ items: [] })
      .mockResolvedValueOnce({ items: [completed] });
    mocks.runInspection.mockResolvedValue(detail(started));
    mocks.getRun.mockResolvedValue(detail(completed));

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <MemoryRouter>
          <ServerCodexInspectionPage />
        </MemoryRouter>
      );
      await flush();
    });

    const runButton = renderer!.root
      .findAllByType('button')
      .find((button) => textContent(button) === 'monitoring.server_codex_inspection_run_now');
    expect(runButton).toBeDefined();

    await act(async () => {
      runButton!.props.onClick();
      await flush();
      await flush();
      await flush();
    });

    expect(mocks.getRun).toHaveBeenCalledWith('http://manager.local', 'management-key', started.id);
    expect(
      renderer!.root
        .findAllByType('button')
        .some((button) => textContent(button) === 'monitoring.server_codex_inspection_stop')
    ).toBe(false);
    expect(
      renderer!.root.findAll(
        (node) => textContent(node) === 'monitoring.codex_inspection_status_success'
      ).length
    ).toBeGreaterThan(0);

    act(() => renderer!.unmount());
  });

  it('does not report a successful start as failed when reconciliation fails', async () => {
    const started = run({ id: 5, status: 'running', active: true, cancellable: true });
    mocks.listRuns
      .mockResolvedValueOnce({ items: [] })
      .mockRejectedValueOnce(new Error('list refresh failed'));
    mocks.runInspection.mockResolvedValue(detail(started));

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <MemoryRouter>
          <ServerCodexInspectionPage />
        </MemoryRouter>
      );
      await flush();
    });

    const runButton = renderer!.root
      .findAllByType('button')
      .find((button) => textContent(button) === 'monitoring.server_codex_inspection_run_now');
    expect(runButton).toBeDefined();

    await act(async () => {
      runButton!.props.onClick();
      await flush();
      await flush();
    });

    expect(mocks.showNotification).toHaveBeenCalledWith(
      'monitoring.server_codex_inspection_run_started',
      'success'
    );
    expect(mocks.showNotification).not.toHaveBeenCalledWith(
      expect.stringContaining('monitoring.server_codex_inspection_run_failed'),
      'error'
    );
    expect(
      renderer!.root
        .findAllByType('button')
        .some((button) => textContent(button) === 'monitoring.server_codex_inspection_stop')
    ).toBe(true);

    act(() => renderer!.unmount());
  });
});
