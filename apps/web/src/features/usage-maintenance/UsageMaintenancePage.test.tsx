import { act, StrictMode, type ReactNode } from 'react';
import { create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type {
  UsageArchiveList,
  UsageArchivePreview,
  UsageArchiveRunSummary,
  UsageMaintenanceStatus,
} from '@/services/api/usageService';
import en from '@/i18n/locales/en.json';
import { Drawer } from '@/components/ui/Drawer';
import { UsageMaintenancePage } from './UsageMaintenancePage';
import { COMPACT_USAGE_COMMAND } from './UsageMaintenanceCapabilityViews';

const { mocks } = vi.hoisted(() => {
  (
    globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
  ).IS_REACT_ACT_ENVIRONMENT = true;
  return {
    mocks: {
      availability: {
        checking: false,
        managerServiceBase: 'http://manager-a.local:18317',
      },
      managementKey: 'management-key-a',
      navigate: vi.fn(),
      showNotification: vi.fn(),
      showConfirmation: vi.fn(),
      probeUsageMaintenance: vi.fn(),
      getUsageMaintenance: vi.fn(),
      getUsageArchive: vi.fn(),
      listUsageArchives: vi.fn(),
      previewUsageArchive: vi.fn(),
      createUsageArchive: vi.fn(),
      resumeUsageArchive: vi.fn(),
      verifyUsageArchive: vi.fn(),
      deleteUsageArchive: vi.fn(),
      cancelUsageArchive: vi.fn(),
      t: vi.fn((key: string, options?: Record<string, unknown>) => {
        if (
          [
            'usage_maintenance.run_status_',
            'usage_maintenance.run_mode_',
            'usage_maintenance.migration_status_',
            'usage_maintenance.aggregate_status_',
          ].some((prefix) => key.startsWith(prefix))
        ) {
          return `translated:${key}`;
        }
        let value = typeof options?.defaultValue === 'string' ? options.defaultValue : key;
        for (const [name, replacement] of Object.entries(options ?? {})) {
          if (name !== 'defaultValue') {
            value = value.split(`{{${name}}}`).join(String(replacement));
          }
        }
        return value;
      }),
    },
  };
});

vi.mock('react-i18next', () => ({
  initReactI18next: { type: '3rdParty', init: () => {} },
  useTranslation: () => ({
    i18n: { language: 'en' },
    t: mocks.t,
  }),
}));

vi.mock('react-router-dom', () => ({ useNavigate: () => mocks.navigate }));
vi.mock('@/components/ui/Drawer', () => ({
  Drawer: ({
    open,
    title,
    children,
    footer,
    onClose,
  }: {
    open: boolean;
    title: ReactNode;
    children: ReactNode;
    footer: ReactNode;
    onClose: () => void;
  }) =>
    open ? (
      <div data-testid="maintenance-drawer">
        <strong>{title}</strong>
        {children}
        <div data-testid="maintenance-drawer-footer">{footer}</div>
        <button onClick={onClose}>Close drawer</button>
      </div>
    ) : null,
}));

vi.mock('@/components/ui/DropdownMenu', () => ({
  DropdownMenu: ({
    items,
  }: {
    items: { key: string; label: ReactNode; onClick: () => void }[];
  }) => (
    <div>
      {items.map((item) => (
        <button key={item.key} onClick={item.onClick}>
          {item.label}
        </button>
      ))}
    </div>
  ),
}));
vi.mock('@/components/ui/Select', () => ({
  Select: ({
    value,
    options,
    onChange,
    ariaLabel,
  }: {
    value: string;
    options: { value: string; label: string }[];
    onChange: (value: string) => void;
    ariaLabel: string;
  }) => (
    <select value={value} aria-label={ariaLabel} onChange={(event) => onChange(event.target.value)}>
      {options.map((option) => (
        <option key={option.value} value={option.value}>
          {option.label}
        </option>
      ))}
    </select>
  ),
}));

vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => mocks.availability,
}));

vi.mock('@/stores', () => ({
  useAuthStore: (selector: (state: { managementKey: string }) => unknown) =>
    selector({ managementKey: mocks.managementKey }),
  useNotificationStore: () => ({
    showNotification: mocks.showNotification,
    showConfirmation: mocks.showConfirmation,
  }),
}));

vi.mock('@/services/api/usageService', () => ({
  getUsageServiceErrorCode: (error: unknown) =>
    error && typeof error === 'object' && 'code' in error && typeof error.code === 'string'
      ? error.code
      : '',
  usageServiceApi: {
    probeUsageMaintenance: mocks.probeUsageMaintenance,
    getUsageMaintenance: mocks.getUsageMaintenance,
    getUsageArchive: mocks.getUsageArchive,
    listUsageArchives: mocks.listUsageArchives,
    previewUsageArchive: mocks.previewUsageArchive,
    createUsageArchive: mocks.createUsageArchive,
    resumeUsageArchive: mocks.resumeUsageArchive,
    verifyUsageArchive: mocks.verifyUsageArchive,
    deleteUsageArchive: mocks.deleteUsageArchive,
    cancelUsageArchive: mocks.cancelUsageArchive,
  },
}));

vi.mock('@/components/ui/LoadingSpinner', () => ({
  LoadingSpinner: () => <div>full-screen-loading</div>,
}));

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
};

const archive = (
  status: UsageArchiveRunSummary['status'],
  id = `run-${status}`
): UsageArchiveRunSummary => ({
  id,
  mode: 'manual',
  status,
  cutoff_timestamp_ms: 1_700_000_000_000,
  target_event_id: 100,
  event_count: 10,
  estimated_bytes: 1_024,
  last_archived_event_id: status === 'previewed' ? 0 : 100,
  archived_event_count: status === 'previewed' ? 0 : 10,
  archived_uncompressed_bytes: 1_024,
  archived_compressed_bytes: 256,
  last_deleted_event_id: status === 'completed' ? 100 : 0,
  deleted_event_count: status === 'completed' ? 10 : 0,
  created_at_ms: 1_700_000_000_000,
  updated_at_ms: 1_700_000_001_000,
  has_error: status === 'failed',
});

const archiveStatus = (run = archive('previewed', 'created-run')) => ({
  run,
  segments: [],
});

const maintenance = (overrides: Partial<UsageMaintenanceStatus> = {}): UsageMaintenanceStatus => ({
  raw_event_count: 10,
  raw_min_timestamp_ms: 1_690_000_000_000,
  raw_max_timestamp_ms: 1_700_000_000_000,
  raw_archived_event_count: 0,
  raw_deleted_event_count: 2,
  migration: {
    name: 'usage_cache_accounting_v2',
    status: 'completed',
    last_event_id: 100,
    target_event_id: 100,
    processed_rows: 100,
    changed_rows: 2,
    updated_at_ms: 1_700_000_000_000,
  },
  hourly_aggregate: {
    name: 'hourly_core',
    schema_version: 1,
    status: 'ready',
    coverage_event_id: 100,
    target_event_id: 100,
    updated_at_ms: 1_700_000_000_000,
  },
  readiness: {
    migration_ready: true,
    hourly_aggregate_ready: true,
    archive_delete_enabled: true,
  },
  storage: {
    page_size: 4_096,
    page_count: 20,
    freelist_count: 1,
    reclaimable_bytes: 4_096,
    database_bytes: 81_920,
    wal_bytes: 0,
    shm_bytes: 0,
    total_bytes: 81_920,
  },
  compact_requires_stopped_server: true,
  ...overrides,
});

const getText = (node: ReactTestInstance): string =>
  node.children
    .map((child) => {
      if (typeof child === 'string' || typeof child === 'number') return String(child);
      return getText(child);
    })
    .join('');

const findButtons = (renderer: ReactTestRenderer, text: string) =>
  renderer.root
    .findAllByType('button')
    .filter(
      (button) =>
        getText(button).includes(text) || String(button.props['aria-label'] ?? '').includes(text)
    );

const findCreateButtons = (renderer: ReactTestRenderer) =>
  renderer.root
    .findAllByProps({ 'data-testid': 'maintenance-drawer-footer' })
    .flatMap((footer) => footer.findAllByType('button'))
    .filter((button) => getText(button) === 'Next');

const getDrawerConfirmation = (renderer: ReactTestRenderer) => {
  const drawer = renderer.root.findByType(Drawer);
  const footer = renderer.root.findByProps({ 'data-testid': 'maintenance-drawer-footer' });
  const buttons = footer.findAllByType('button');
  const confirm = buttons[buttons.length - 1];
  return {
    title: drawer.props.title,
    message: drawer.props.children,
    confirmText: getText(confirm),
    onConfirm: confirm.props.onClick,
  };
};

const renderOverviewPage = async (status = maintenance(), runs: UsageArchiveRunSummary[] = []) => {
  mocks.getUsageMaintenance.mockResolvedValue(status);
  mocks.listUsageArchives.mockResolvedValue({ runs } satisfies UsageArchiveList);
  let renderer!: ReactTestRenderer;
  await act(async () => {
    renderer = create(<UsageMaintenancePage />);
    renderers.add(renderer);
  });
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
  return renderer;
};

const renderResolvedPage = async (status = maintenance(), runs: UsageArchiveRunSummary[] = []) => {
  const renderer = await renderOverviewPage(status, runs);
  const createButton = findButtons(renderer, 'New archive')[0];
  if (createButton) {
    await act(async () => {
      createButton.props.onClick();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
  }
  return renderer;
};

const renderHistoryPage = renderOverviewPage;

const translate = mocks.t.getMockImplementation()!;
const renderers = new Set<ReactTestRenderer>();

beforeEach(() => {
  vi.useRealTimers();
  vi.resetAllMocks();
  mocks.t.mockImplementation((key, options) => {
    const localized = key
      .split('.')
      .reduce<unknown>(
        (value, part) =>
          value && typeof value === 'object' ? (value as Record<string, unknown>)[part] : undefined,
        en
      );
    return translate(key, {
      ...(typeof localized === 'string' ? { defaultValue: localized } : {}),
      ...options,
    });
  });
  mocks.availability.checking = false;
  mocks.availability.managerServiceBase = 'http://manager-a.local:18317';
  mocks.managementKey = 'management-key-a';
  mocks.probeUsageMaintenance.mockResolvedValue(undefined);
  mocks.getUsageArchive.mockImplementation((_base: string, runId: string) =>
    Promise.resolve(archiveStatus(archive('verified', runId)))
  );
  mocks.previewUsageArchive.mockImplementation((_base: string, cutoffTimestampMS: number) =>
    Promise.resolve({
      cutoff_timestamp_ms: cutoffTimestampMS,
      target_event_id: 100,
      event_count: 7,
      estimated_bytes: 2_048,
      min_timestamp_ms: cutoffTimestampMS - 1_000,
      max_timestamp_ms: cutoffTimestampMS - 1,
    })
  );
  mocks.createUsageArchive.mockResolvedValue(archiveStatus());
  mocks.resumeUsageArchive.mockImplementation((_base: string, runId: string) =>
    Promise.resolve(archiveStatus(archive('archived', runId)))
  );
  mocks.verifyUsageArchive.mockImplementation((_base: string, runId: string) =>
    Promise.resolve(archiveStatus(archive('verified', runId)))
  );
  mocks.deleteUsageArchive.mockImplementation((_base: string, runId: string) =>
    Promise.resolve(archiveStatus(archive('completed', runId)))
  );
  mocks.cancelUsageArchive.mockImplementation((_base: string, runId: string) =>
    Promise.resolve(archiveStatus(archive('cancelled', runId)))
  );
});

afterEach(() => {
  act(() => {
    for (const renderer of renderers) renderer.unmount();
  });
  renderers.clear();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('UsageMaintenancePage', () => {
  it('keeps verified archives complete without presenting them as pending cleanup', async () => {
    const renderer = await renderOverviewPage(maintenance(), [archive('verified')]);
    expect(findButtons(renderer, 'Continue this record')).toHaveLength(0);
    expect(getText(renderer.root)).toContain('Online details retained');
    expect(findButtons(renderer, 'Review cleanup')).toHaveLength(1);
    expect(mocks.previewUsageArchive).not.toHaveBeenCalled();
  });

  it('applies source filters on the server and resets pagination when the source changes', async () => {
    const renderer = await renderOverviewPage();
    mocks.listUsageArchives.mockResolvedValue({ runs: [archive('failed')], next_cursor: 'page-2' });
    await act(async () =>
      renderer.root.findAllByType('select')[1].props.onChange({ target: { value: 'manual' } })
    );
    expect(mocks.listUsageArchives).toHaveBeenLastCalledWith(
      'http://manager-a.local:18317',
      'management-key-a',
      { mode: 'manual', status: undefined, limit: 20, cursor: undefined },
      expect.any(AbortSignal)
    );
    await act(async () => findButtons(renderer, 'Next')[0].props.onClick());
    expect(mocks.listUsageArchives).toHaveBeenLastCalledWith(
      'http://manager-a.local:18317',
      'management-key-a',
      { mode: 'manual', status: undefined, limit: 20, cursor: 'page-2' },
      expect.any(AbortSignal)
    );
    await act(async () =>
      renderer.root.findAllByType('select')[1].props.onChange({ target: { value: 'retention' } })
    );
    expect(mocks.listUsageArchives).toHaveBeenLastCalledWith(
      'http://manager-a.local:18317',
      'management-key-a',
      { mode: 'retention', status: undefined, limit: 20, cursor: undefined },
      expect.any(AbortSignal)
    );
    expect(findButtons(renderer, 'Previous')[0].props.disabled).toBe(true);
  });

  it('keeps the chosen cutoff fixed when refreshing a creation preview', async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-09-15T00:00:00Z'));
    const renderer = await renderResolvedPage();
    await act(async () => findButtons(renderer, 'Older than 7 days')[0].props.onClick());
    const cutoff =
      mocks.previewUsageArchive.mock.calls[mocks.previewUsageArchive.mock.calls.length - 1][1];
    vi.setSystemTime(new Date('2026-09-16T00:00:00Z'));
    await act(async () => findButtons(renderer, 'Refresh')[0].props.onClick());
    expect(
      mocks.previewUsageArchive.mock.calls[mocks.previewUsageArchive.mock.calls.length - 1][1]
    ).toBe(cutoff);
  });

  it('revokes a saved cleanup confirmation when its drawer is closed', async () => {
    const renderer = await renderHistoryPage(maintenance(), [archive('verified')]);
    act(() => findButtons(renderer, 'Review cleanup')[0].props.onClick());
    const confirmation = getDrawerConfirmation(renderer);
    act(() => findButtons(renderer, 'Close drawer')[0].props.onClick());
    act(() => confirmation.onConfirm());
    expect(mocks.deleteUsageArchive).not.toHaveBeenCalled();
    expect(renderer.root.findAllByProps({ 'data-testid': 'maintenance-drawer' })).toHaveLength(0);
  });

  it('keeps an archive running when minimized and does not replace another open record', async () => {
    const other = archive('completed', 'older-completed');
    const pending = deferred<ReturnType<typeof archiveStatus>>();
    mocks.resumeUsageArchive.mockReturnValueOnce(pending.promise);
    mocks.getUsageArchive.mockImplementation((_base: string, id: string) =>
      Promise.resolve(archiveStatus(id === other.id ? other : archive('verified', id)))
    );
    const renderer = await renderResolvedPage(maintenance(), [other]);
    act(() => findCreateButtons(renderer)[0].props.onClick());
    await act(async () => getDrawerConfirmation(renderer).onConfirm());
    const signal = mocks.resumeUsageArchive.mock.calls[0][3] as AbortSignal;
    act(() => findButtons(renderer, 'Minimize')[0].props.onClick());
    expect(signal.aborted).toBe(false);
    await act(async () => findButtons(renderer, 'Continue this record')[0].props.onClick());
    expect(findButtons(renderer, 'Stop waiting')).toHaveLength(1);
    expect(mocks.resumeUsageArchive).toHaveBeenCalledTimes(1);
    act(() => findButtons(renderer, 'Minimize')[0].props.onClick());
    await act(async () =>
      renderer.root
        .findByProps({ 'data-run-id': other.id })
        .findAllByType('button')[0]
        .props.onClick()
    );
    expect(findButtons(renderer, 'Stop waiting')).toHaveLength(0);
    await act(async () => pending.resolve(archiveStatus(archive('archived', 'created-run'))));
    const drawer = renderer.root.findByProps({ 'data-testid': 'maintenance-drawer' });
    expect(getText(drawer)).toContain(other.id);
    expect(getText(drawer)).not.toContain('created-run');
    expect(mocks.verifyUsageArchive).toHaveBeenCalledTimes(1);
    expect(mocks.deleteUsageArchive).not.toHaveBeenCalled();
  });

  it('opens with the maintenance overview and renders only API-backed storage metrics', async () => {
    const renderer = await renderOverviewPage(
      maintenance({
        raw_event_count: 1_284_562,
        raw_archived_event_count: 342_118,
        raw_deleted_event_count: 2_845_700,
      }),
      [archive('completed', 'recent-run')]
    );

    const text = getText(renderer.root);
    expect(text).toContain('Archive management');
    expect(text).toContain('1,284,562');
    expect(text).toContain('342,118');
    expect(text).toContain('2,845,700');
    expect(text).toContain('Including 342,118 already archived');
    expect(text).not.toContain('78%');
    expect(findButtons(renderer, 'New archive')).toHaveLength(1);
    expect(findCreateButtons(renderer)).toHaveLength(0);
    expect(mocks.previewUsageArchive).not.toHaveBeenCalled();
    expect(getText(renderer.root)).toContain('Archive records');
    act(() => renderer.unmount());
  });

  it('opens the offline advanced maintenance view from the overview', async () => {
    const renderer = await renderOverviewPage();

    await act(async () => {
      findButtons(renderer, 'Storage and compaction')[0].props.onClick();
      await Promise.resolve();
    });

    const text = getText(renderer.root);
    expect(text).toContain('Storage and compaction');
    expect(text).toContain('usage.sqlite-wal');
    expect(text).toContain('the browser never executes it.');
    expect(text).not.toContain('Maintenance route204');

    vi.stubGlobal('navigator', {
      clipboard: { writeText: vi.fn().mockRejectedValue(new Error('clipboard denied')) },
    });
    await act(async () => {
      findButtons(renderer, 'Copy command')[0].props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'The command could not be copied. Select it manually from the code block.',
      'warning'
    );
    act(() => renderer.unmount());
  });

  it('keeps the last capability snapshot visible and surfaces refresh failures', async () => {
    const renderer = await renderOverviewPage();

    await act(async () => {
      findButtons(renderer, 'Diagnostics')[0].props.onClick();
      await Promise.resolve();
    });
    mocks.getUsageMaintenance.mockRejectedValueOnce(new Error('maintenance snapshot failed'));

    await act(async () => {
      findButtons(renderer, 'Refresh')[0].props.onClick();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(getText(renderer.root)).toContain('maintenance snapshot failed');
    expect(getText(renderer.root)).toContain('Diagnostics');
    act(() => renderer.unmount());
  });

  it('opens diagnostics with real coverage, readiness, storage, and lock state', async () => {
    const activeRun = archive('archiving', 'diagnostic-active-run');
    const renderer = await renderOverviewPage(
      maintenance({
        active_run: activeRun,
        active_lock: {
          run_id: activeRun.id,
          operation: 'archiving',
          acquired_at_ms: 1_700_000_000_000,
          updated_at_ms: 1_700_000_001_000,
        },
        migration_coverage: {
          status: 'running',
          watermark_event_id: 78,
          target_event_id: 100,
          complete: false,
        },
        hourly_aggregate_coverage: {
          status: 'catching_up',
          watermark_event_id: 92,
          target_event_id: 100,
          complete: false,
        },
        readiness: {
          migration_ready: false,
          hourly_aggregate_ready: false,
          archive_delete_enabled: true,
        },
      })
    );

    await act(async () => {
      findButtons(renderer, 'Diagnostics')[0].props.onClick();
      await Promise.resolve();
    });

    const text = getText(renderer.root);
    expect(text).toContain('Diagnostics');
    expect(text).toContain('78%');
    expect(text).toContain('92%');
    expect(text).toContain('diagnostic-active-run');
    expect(text).toContain('Cleanup checks this archive’s exact coverage');
    expect(text).toContain('Stop server required');
    expect(text).not.toContain('raw_json');
    expect(text).not.toContain('fail_body');
    act(() => renderer.unmount());
  });

  it('isolates archive creation into the designed policy, impact, readiness, and footer layout', async () => {
    const renderer = await renderResolvedPage();
    const text = getText(renderer.root);

    expect(text).toContain('What would you like to do?');
    expect(text).toContain('Choose the data range');
    expect(findButtons(renderer, 'Current online range')).toHaveLength(1);
    expect(text).toContain('Impact preview');
    expect(text).toContain('Estimated source size');
    expect(text).toContain('Source-row estimate, not archive size or disk space');
    expect(text).toContain('This range applies to this operation only');
    expect(text).not.toContain('Archive history');
    expect(text).not.toContain('Advanced: reclaim physical SQLite space');
    expect(findCreateButtons(renderer)).toHaveLength(1);
    expect(mocks.previewUsageArchive).toHaveBeenCalledTimes(1);
    act(() => renderer.unmount());
  });

  it('loads archive history with exact server filters and cursor navigation', async () => {
    const firstPage = {
      runs: [archive('failed', 'history-run-1')],
      total: 2,
      status_counts: { failed: 2 },
      next_cursor: 'cursor-2',
    } satisfies UsageArchiveList;
    const secondPage = {
      runs: [archive('failed', 'history-run-2')],
      total: 2,
      status_counts: { failed: 2 },
    } satisfies UsageArchiveList;
    const renderer = await renderOverviewPage(maintenance(), [archive('completed')]);
    mocks.listUsageArchives.mockResolvedValueOnce(firstPage).mockResolvedValueOnce(secondPage);

    await act(async () => {
      renderer.root.findAllByType('select')[0].props.onChange({ target: { value: 'failed' } });
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(renderer.root.findAllByProps({ 'data-run-id': 'history-run-1' })).toHaveLength(1);
    expect(mocks.listUsageArchives).toHaveBeenLastCalledWith(
      'http://manager-a.local:18317',
      'management-key-a',
      { status: 'failed', mode: undefined, limit: 20, cursor: undefined },
      expect.any(AbortSignal)
    );

    await act(async () => {
      findButtons(renderer, 'Next')[0].props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(mocks.listUsageArchives).toHaveBeenLastCalledWith(
      'http://manager-a.local:18317',
      'management-key-a',
      { status: 'failed', mode: undefined, limit: 20, cursor: 'cursor-2' },
      expect.any(AbortSignal)
    );
    expect(renderer.root.findAllByProps({ 'data-run-id': 'history-run-2' })).toHaveLength(1);
    act(() => renderer.unmount());
  });

  it('shows a confirmed abandon action for a previewed run and releases it through the API', async () => {
    const run = archive('previewed', 'cancel-previewed-run');
    const renderer = await renderHistoryPage(maintenance(), [run]);
    mocks.getUsageArchive.mockResolvedValueOnce(archiveStatus(run));
    await act(async () => findButtons(renderer, 'Details')[0].props.onClick());
    const cancelButton = findButtons(renderer, 'Abandon task')[0];
    expect(cancelButton).toBeDefined();

    act(() => cancelButton.props.onClick());
    const confirmation = mocks.showConfirmation.mock.calls[
      mocks.showConfirmation.mock.calls.length - 1
    ]?.[0] as {
      message: string;
      onConfirm: () => Promise<void>;
    };
    expect(confirmation.message).toContain('without deleting raw usage data');
    await act(async () => {
      await confirmation.onConfirm();
    });

    expect(mocks.cancelUsageArchive).toHaveBeenCalledWith(
      'http://manager-a.local:18317',
      run.id,
      'management-key-a',
      expect.any(AbortSignal)
    );
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'Archive task abandoned; raw usage data was not deleted.',
      'success'
    );
    act(() => renderer.unmount());
  });

  it('shows an abandon action for a failed verification run with published events and releases it through the API', async () => {
    const run = {
      ...archive('failed', 'failed-verifying-published-run'),
      resume_status: 'verifying' as const,
      archived_event_count: 5,
      deleted_event_count: 0,
    };
    const renderer = await renderHistoryPage(maintenance(), [run]);
    mocks.getUsageArchive.mockResolvedValueOnce(archiveStatus(run));
    await act(async () => findButtons(renderer, 'Details')[0].props.onClick());
    const cancelButton = findButtons(renderer, 'Abandon task')[0];
    expect(cancelButton).toBeDefined();

    act(() => cancelButton.props.onClick());
    const confirmation = mocks.showConfirmation.mock.calls[
      mocks.showConfirmation.mock.calls.length - 1
    ]?.[0] as {
      message: string;
      onConfirm: () => Promise<void>;
    };
    expect(confirmation.message).toContain('without deleting raw usage data');
    await act(async () => {
      await confirmation.onConfirm();
    });

    expect(mocks.cancelUsageArchive).toHaveBeenCalledWith(
      'http://manager-a.local:18317',
      run.id,
      'management-key-a',
      expect.any(AbortSignal)
    );
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'Archive task abandoned; raw usage data was not deleted.',
      'success'
    );
    act(() => renderer.unmount());
  });

  it('does not expose abandon for a run that has entered raw deletion', async () => {
    const partiallyDeleted = {
      ...archive('failed', 'partial-delete-run'),
      resume_status: 'deleting' as const,
      deleted_event_count: 1,
      delete_started_at_ms: 1_700_000_002_000,
    };
    const renderer = await renderHistoryPage(maintenance(), [partiallyDeleted]);

    expect(findButtons(renderer, 'Abandon task')).toHaveLength(0);
    expect(mocks.cancelUsageArchive).not.toHaveBeenCalled();
    act(() => renderer.unmount());
  });

  it('loads sanitized segment summaries when opening a completed task detail', async () => {
    const run = archive('completed', 'detail-run');
    mocks.getUsageArchive.mockResolvedValueOnce({
      run,
      segments: [
        {
          run_id: run.id,
          sequence: 1,
          status: 'verified',
          first_event_id: 1,
          last_event_id: 10,
          min_timestamp_ms: 1_699_999_000_000,
          max_timestamp_ms: 1_700_000_000_000,
          event_count: 10,
          uncompressed_bytes: 1_024,
          compressed_bytes: 256,
          created_at_ms: 1_700_000_000_000,
          verified_at_ms: 1_700_000_001_000,
        },
      ],
    });
    const renderer = await renderHistoryPage(maintenance(), [run]);

    await act(async () => {
      findButtons(renderer, 'Details')[0].props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.getUsageArchive).toHaveBeenCalledWith(
      'http://manager-a.local:18317',
      run.id,
      'management-key-a',
      expect.any(AbortSignal)
    );
    expect(getText(renderer.root)).toContain('Archive task details');
    expect(getText(renderer.root)).toContain('Segment summary');
    expect(getText(renderer.root)).toContain('verified');
    act(() => renderer.unmount());
  });

  it('keeps the same record visible when reopening its drawer and refreshes its details', async () => {
    const run = archive('completed', 'reopened-record');
    mocks.getUsageArchive.mockResolvedValueOnce(archiveStatus(run));
    const renderer = await renderHistoryPage(maintenance(), [run]);

    await act(async () => findButtons(renderer, 'Details')[0].props.onClick());
    act(() => findButtons(renderer, 'Close drawer')[0].props.onClick());

    const refreshed = deferred<ReturnType<typeof archiveStatus>>();
    mocks.getUsageArchive.mockReturnValueOnce(refreshed.promise);
    await act(async () => findButtons(renderer, 'Details')[0].props.onClick());

    const drawer = renderer.root.findByProps({ 'data-testid': 'maintenance-drawer' });
    expect(getText(drawer)).toContain(run.id);
    expect(getText(drawer)).not.toContain('invalid archive task response');
    expect(mocks.getUsageArchive).toHaveBeenCalledTimes(2);

    await act(async () => {
      refreshed.resolve(archiveStatus({ ...run, archived_compressed_bytes: 512 }));
    });
    expect(getText(drawer)).toContain('512.00 B');
  });

  it('refreshes active task details with maintenance polling', async () => {
    vi.useFakeTimers();
    const activeRun = archive('archiving', 'active-detail-run');
    mocks.getUsageArchive.mockResolvedValue(archiveStatus(activeRun));
    const renderer = await renderOverviewPage(maintenance({ active_run: activeRun }), [activeRun]);

    await act(async () => {
      findButtons(renderer, 'Continue this record')[0].props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(mocks.getUsageArchive).toHaveBeenCalledTimes(1);

    await act(async () => {
      vi.advanceTimersByTime(5_000);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(mocks.getUsageMaintenance).toHaveBeenCalledTimes(2);
    expect(mocks.getUsageArchive).toHaveBeenCalledTimes(2);
    act(() => renderer.unmount());
  });

  it('stops a browser wait started from the active task view without cancelling server work', async () => {
    const activeRun = archive('archiving', 'active-stop-run');
    mocks.getUsageArchive.mockResolvedValueOnce(archiveStatus(activeRun));
    const resume = deferred<unknown>();
    mocks.resumeUsageArchive.mockImplementationOnce(() => resume.promise);
    const renderer = await renderOverviewPage(maintenance({ active_run: activeRun }), [activeRun]);

    await act(async () => {
      findButtons(renderer, 'Continue this record')[0].props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    act(() => findButtons(renderer, 'Continue archive')[0].props.onClick());
    const signal = mocks.resumeUsageArchive.mock.calls[0][3] as AbortSignal;
    expect(signal.aborted).toBe(false);

    act(() => findButtons(renderer, 'Stop waiting')[0].props.onClick());
    expect(signal.aborted).toBe(true);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'The request was stopped. If an archive task was created, it remains recoverable in history.',
      'warning'
    );

    await act(async () => {
      resume.resolve({});
      await Promise.resolve();
    });
    expect(mocks.deleteUsageArchive).not.toHaveBeenCalled();
    act(() => renderer.unmount());
  });

  it('automatically previews the default and selected retention policy before the guided archive workflow', async () => {
    const renderer = await renderResolvedPage();
    expect(mocks.previewUsageArchive).toHaveBeenCalledTimes(1);
    const defaultCutoff = mocks.previewUsageArchive.mock.calls[0][1] as number;

    await act(async () => {
      findButtons(renderer, 'Older than 7 days')[0].props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(mocks.previewUsageArchive).toHaveBeenCalledTimes(2);
    const selectedCutoff = mocks.previewUsageArchive.mock.calls[1][1] as number;
    expect(selectedCutoff).toBeGreaterThan(defaultCutoff);
    expect(findCreateButtons(renderer)).toHaveLength(1);

    act(() => findCreateButtons(renderer)[0].props.onClick());
    const confirmation = getDrawerConfirmation(renderer) as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      await confirmation.onConfirm();
    });
    expect(mocks.createUsageArchive).toHaveBeenCalledWith(
      'http://manager-a.local:18317',
      selectedCutoff,
      'management-key-a',
      expect.any(AbortSignal)
    );
    expect(mocks.resumeUsageArchive).toHaveBeenCalledWith(
      'http://manager-a.local:18317',
      'created-run',
      'management-key-a',
      expect.any(AbortSignal),
      'archiving'
    );
    expect(mocks.verifyUsageArchive).toHaveBeenCalledWith(
      'http://manager-a.local:18317',
      'created-run',
      'management-key-a',
      expect.any(AbortSignal)
    );
    expect(mocks.createUsageArchive.mock.invocationCallOrder[0]).toBeLessThan(
      mocks.resumeUsageArchive.mock.invocationCallOrder[0]
    );
    expect(mocks.resumeUsageArchive.mock.invocationCallOrder[0]).toBeLessThan(
      mocks.verifyUsageArchive.mock.invocationCallOrder[0]
    );
    expect(mocks.deleteUsageArchive).not.toHaveBeenCalled();
    expect(getText(renderer.root)).toContain(
      'Archive complete. Online details are still available.'
    );
    act(() => renderer.unmount());
  });

  it('keeps a recoverable task visible when guided archive writing fails', async () => {
    const failedRun = {
      ...archive('failed', 'resume-failed-run'),
      resume_status: 'archiving',
    };
    const renderer = await renderResolvedPage();
    mocks.createUsageArchive.mockResolvedValueOnce(
      archiveStatus(archive('previewed', failedRun.id))
    );
    mocks.getUsageArchive.mockResolvedValueOnce(archiveStatus(failedRun));
    mocks.resumeUsageArchive.mockRejectedValueOnce(new Error('archive write failed'));
    mocks.getUsageMaintenance.mockResolvedValueOnce(maintenance({ active_run: failedRun }));
    mocks.listUsageArchives.mockResolvedValueOnce({ runs: [failedRun] });

    act(() => findCreateButtons(renderer)[0].props.onClick());
    const confirmation = getDrawerConfirmation(renderer) as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      await confirmation.onConfirm();
    });

    expect(mocks.createUsageArchive).toHaveBeenCalledTimes(1);
    expect(mocks.resumeUsageArchive).toHaveBeenCalledTimes(1);
    expect(mocks.verifyUsageArchive).not.toHaveBeenCalled();
    expect(mocks.deleteUsageArchive).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith('archive write failed', 'error');
    expect(getText(renderer.root)).toContain('Needs attention');
    expect(renderer.root.findAllByProps({ 'data-run-id': 'resume-failed-run' })).toHaveLength(1);
    act(() => renderer.unmount());
  });

  it('explains and blocks archive creation until accounting migration is ready', async () => {
    const renderer = await renderResolvedPage(
      maintenance({
        readiness: {
          migration_ready: false,
          hourly_aggregate_ready: true,
          archive_delete_enabled: true,
        },
      })
    );

    const createButton = findCreateButtons(renderer)[0];
    expect(createButton.props.disabled).toBe(true);
    expect(getText(renderer.root)).toContain(
      'Archiving becomes available after usage accounting preparation completes.'
    );
    expect(mocks.createUsageArchive).not.toHaveBeenCalled();
    act(() => renderer.unmount());
  });

  it('keeps a recoverable task visible when archive verification fails', async () => {
    const failedRun = {
      ...archive('failed', 'verify-failed-run'),
      resume_status: 'verifying',
    };
    const renderer = await renderResolvedPage();
    mocks.createUsageArchive.mockResolvedValueOnce(
      archiveStatus(archive('previewed', failedRun.id))
    );
    mocks.getUsageArchive.mockResolvedValueOnce(archiveStatus(failedRun));
    mocks.verifyUsageArchive.mockRejectedValueOnce(new Error('archive verification failed'));
    mocks.getUsageMaintenance.mockResolvedValueOnce(maintenance({ active_run: failedRun }));
    mocks.listUsageArchives.mockResolvedValueOnce({ runs: [failedRun] });

    act(() => findCreateButtons(renderer)[0].props.onClick());
    const confirmation = getDrawerConfirmation(renderer) as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      await confirmation.onConfirm();
    });

    expect(mocks.resumeUsageArchive).toHaveBeenCalledTimes(1);
    expect(mocks.verifyUsageArchive).toHaveBeenCalledTimes(1);
    expect(mocks.deleteUsageArchive).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith('archive verification failed', 'error');
    expect(getText(renderer.root)).toContain('Needs attention');
    expect(renderer.root.findAllByProps({ 'data-run-id': 'verify-failed-run' })).toHaveLength(1);
    act(() => renderer.unmount());
  });

  it('does not continue a guided workflow when the create response is malformed', async () => {
    const renderer = await renderResolvedPage();
    mocks.createUsageArchive.mockResolvedValueOnce({
      run: archive('previewed', 'malformed-create-run'),
    });

    act(() => findCreateButtons(renderer)[0].props.onClick());
    const confirmation = getDrawerConfirmation(renderer) as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      await confirmation.onConfirm();
    });

    expect(mocks.resumeUsageArchive).not.toHaveBeenCalled();
    expect(mocks.verifyUsageArchive).not.toHaveBeenCalled();
    expect(mocks.deleteUsageArchive).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'The server returned an invalid archive task response.',
      'error'
    );
    act(() => renderer.unmount());
  });

  it('does not report success when a guided action returns a malformed response', async () => {
    const renderer = await renderResolvedPage();
    mocks.verifyUsageArchive.mockResolvedValueOnce({});

    act(() => findCreateButtons(renderer)[0].props.onClick());
    const confirmation = getDrawerConfirmation(renderer) as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      await confirmation.onConfirm();
    });

    expect(mocks.resumeUsageArchive).toHaveBeenCalledTimes(1);
    expect(mocks.verifyUsageArchive).toHaveBeenCalledTimes(1);
    expect(mocks.deleteUsageArchive).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'The server returned an invalid archive task response.',
      'error'
    );
    expect(mocks.showNotification).not.toHaveBeenCalledWith(
      'Archive created and verified. Raw data was not deleted.',
      'success'
    );
    act(() => renderer.unmount());
  });

  it('aborts the active guided request when the user stops waiting', async () => {
    const renderer = await renderResolvedPage();
    const resume = deferred<unknown>();
    let resumeSignal: AbortSignal | undefined;
    mocks.resumeUsageArchive.mockImplementationOnce(
      (_base: string, _runId: string, _key: string, signal: AbortSignal) => {
        resumeSignal = signal;
        return resume.promise;
      }
    );

    act(() => findCreateButtons(renderer)[0].props.onClick());
    const confirmation = getDrawerConfirmation(renderer) as {
      onConfirm: () => Promise<void>;
    };
    let guidedPromise!: Promise<void>;
    await act(async () => {
      guidedPromise = confirmation.onConfirm();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(resumeSignal).toBeDefined();
    expect(findButtons(renderer, 'Stop waiting')).toHaveLength(1);
    act(() => findButtons(renderer, 'Stop waiting')[0].props.onClick());
    expect(resumeSignal?.aborted).toBe(true);
    expect(findButtons(renderer, 'Stop waiting')).toHaveLength(0);

    await act(async () => {
      resume.resolve({});
      await guidedPromise;
    });
    expect(mocks.verifyUsageArchive).not.toHaveBeenCalled();
    expect(mocks.deleteUsageArchive).not.toHaveBeenCalled();
    act(() => renderer.unmount());
  });

  it('does not promise a recoverable task when creation is stopped before its result is known', async () => {
    const renderer = await renderResolvedPage();
    const createRequest = deferred<unknown>();
    let createSignal: AbortSignal | undefined;
    mocks.createUsageArchive.mockImplementationOnce(
      (_base: string, _cutoff: number, _key: string, signal: AbortSignal) => {
        createSignal = signal;
        return createRequest.promise;
      }
    );

    act(() => findCreateButtons(renderer)[0].props.onClick());
    const confirmation = getDrawerConfirmation(renderer) as {
      onConfirm: () => Promise<void>;
    };
    let guidedPromise!: Promise<void>;
    await act(async () => {
      guidedPromise = confirmation.onConfirm();
      await Promise.resolve();
    });

    expect(createSignal).toBeDefined();
    act(() => findButtons(renderer, 'Stop waiting')[0].props.onClick());
    expect(createSignal?.aborted).toBe(true);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'The request was stopped. If an archive task was created, it remains recoverable in history.',
      'warning'
    );

    await act(async () => {
      createRequest.resolve(archiveStatus());
      await guidedPromise;
    });
    expect(mocks.resumeUsageArchive).not.toHaveBeenCalled();
    act(() => renderer.unmount());
  });

  it('explains a zero-result preview and recommends a usable preset', async () => {
    const nowMS = Date.now();
    mocks.previewUsageArchive.mockResolvedValueOnce({
      cutoff_timestamp_ms: nowMS - 30 * 24 * 60 * 60 * 1000,
      target_event_id: 0,
      event_count: 0,
      estimated_bytes: 0,
    });
    const renderer = await renderResolvedPage(
      maintenance({
        raw_min_timestamp_ms: nowMS - 20 * 24 * 60 * 60 * 1000,
        raw_max_timestamp_ms: nowMS - 24 * 60 * 60 * 1000,
      })
    );

    expect(getText(renderer.root)).toContain('No new events to archive in this range');
    const sevenDayButtons = findButtons(renderer, 'Older than 7 days');
    expect(sevenDayButtons).toHaveLength(2);
    await act(async () => {
      sevenDayButtons[1].props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(mocks.previewUsageArchive).toHaveBeenCalledTimes(2);
    expect(mocks.previewUsageArchive.mock.calls[1][1]).toBeGreaterThan(
      mocks.previewUsageArchive.mock.calls[0][1]
    );
    act(() => renderer.unmount());
  });

  it('does not recommend an already-archived raw range when the preview is empty', async () => {
    const nowMS = Date.now();
    mocks.previewUsageArchive.mockResolvedValueOnce({
      cutoff_timestamp_ms: nowMS - 30 * 24 * 60 * 60 * 1000,
      target_event_id: 0,
      event_count: 0,
      estimated_bytes: 0,
    });
    const renderer = await renderResolvedPage(
      maintenance({
        raw_min_timestamp_ms: nowMS - 20 * 24 * 60 * 60 * 1000,
        raw_max_timestamp_ms: nowMS - 24 * 60 * 60 * 1000,
        raw_archived_event_count: 5,
      })
    );

    expect(getText(renderer.root)).toContain('already archived');
    expect(findButtons(renderer, 'Older than 7 days')).toHaveLength(1);
    act(() => renderer.unmount());
  });

  it('accepts maintenance payloads from servers that do not expose the raw time range', async () => {
    const renderer = await renderResolvedPage(
      maintenance({
        raw_min_timestamp_ms: undefined,
        raw_max_timestamp_ms: undefined,
        raw_archived_event_count: undefined,
      })
    );
    act(() => findButtons(renderer, 'Close drawer')[0].props.onClick());
    act(() => findButtons(renderer, 'Current online range')[0].props.onClick());
    expect(getText(renderer.root)).toContain('Time range unavailable on this server version');
    expect(getText(renderer.root)).not.toContain('older than the usage maintenance API');
    act(() => renderer.unmount());
  });

  it('debounces custom cutoffs and refuses a future date before requesting a preview', async () => {
    vi.useFakeTimers();
    const renderer = await renderResolvedPage();
    const initialPreviewCalls = mocks.previewUsageArchive.mock.calls.length;

    act(() => findButtons(renderer, 'Custom date')[0].props.onClick());
    const input = renderer.root.findByType('input');
    act(() => input.props.onChange({ target: { value: '2999-01-01T00:00' } }));
    act(() => vi.advanceTimersByTime(300));
    await act(async () => {
      await Promise.resolve();
    });
    expect(mocks.previewUsageArchive).toHaveBeenCalledTimes(initialPreviewCalls);
    expect(getText(renderer.root)).toContain('not in the future');

    act(() => input.props.onChange({ target: { value: '2026-01-01T00:00' } }));
    act(() => vi.advanceTimersByTime(300));
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(mocks.previewUsageArchive).toHaveBeenCalledTimes(initialPreviewCalls + 1);
    act(() => renderer.unmount());
  });

  it('reloads persisted previewed state without polling when the create response is lost', async () => {
    vi.useFakeTimers();
    const previewCutoff = 1_700_000_000_000;
    mocks.previewUsageArchive.mockResolvedValueOnce({
      cutoff_timestamp_ms: previewCutoff,
      target_event_id: 100,
      event_count: 7,
      estimated_bytes: 2_048,
    });
    const renderer = await renderResolvedPage();
    const active = archive('previewed', 'persisted-after-timeout');
    mocks.createUsageArchive.mockRejectedValueOnce(new Error('create response lost'));
    mocks.getUsageMaintenance.mockResolvedValueOnce(maintenance({ active_run: active }));
    mocks.listUsageArchives.mockResolvedValueOnce({ runs: [active] });

    act(() => findCreateButtons(renderer)[0].props.onClick());
    const confirmation = getDrawerConfirmation(renderer) as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      await confirmation.onConfirm();
    });

    expect(mocks.showNotification).toHaveBeenCalledWith('create response lost', 'error');
    expect(mocks.getUsageMaintenance).toHaveBeenCalledTimes(2);
    expect(renderer.root.findAllByProps({ 'data-run-id': 'persisted-after-timeout' })).toHaveLength(
      1
    );

    await act(async () => {
      vi.advanceTimersByTime(5_000);
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(mocks.getUsageMaintenance).toHaveBeenCalledTimes(2);
    act(() => renderer.unmount());
  });

  it('shows the unsupported state for legacy method-not-allowed responses', async () => {
    mocks.probeUsageMaintenance.mockRejectedValueOnce({ status: 405 });
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<UsageMaintenancePage />);
      renderers.add(renderer);
    });
    expect(getText(renderer.root)).toContain('older than the usage maintenance API');
    expect(getText(renderer.root)).not.toContain('full-screen-loading');
    expect(mocks.probeUsageMaintenance).toHaveBeenCalledWith(
      'http://manager-a.local:18317',
      'management-key-a',
      expect.any(AbortSignal)
    );
    expect(mocks.getUsageMaintenance).not.toHaveBeenCalled();
    expect(mocks.listUsageArchives).not.toHaveBeenCalled();
    act(() => renderer.unmount());
  });

  it('shows archive configuration failures instead of the legacy-server state', async () => {
    mocks.getUsageMaintenance.mockRejectedValueOnce(
      Object.assign(new Error('usage archive is unavailable'), {
        status: 503,
        code: 'usage_archive_unavailable',
      })
    );
    mocks.listUsageArchives.mockResolvedValueOnce({ runs: [] });

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<UsageMaintenancePage />);
      renderers.add(renderer);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(getText(renderer.root)).toContain('usage archive is unavailable');
    expect(getText(renderer.root)).not.toContain('older than the usage maintenance API');
    act(() => renderer.unmount());
  });

  it('aborts the sibling load when one maintenance request fails', async () => {
    let siblingSignal: AbortSignal | undefined;
    mocks.getUsageMaintenance.mockRejectedValueOnce({ status: 404 });
    mocks.listUsageArchives.mockImplementationOnce(
      (_base: string, _key: string, _limit: number, signal: AbortSignal) => {
        siblingSignal = signal;
        return new Promise<UsageArchiveList>(() => {});
      }
    );

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<UsageMaintenancePage />);
      renderers.add(renderer);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(siblingSignal?.aborted).toBe(true);
    expect(getText(renderer.root)).toContain('older than the usage maintenance API');
    act(() => renderer.unmount());
  });

  it('shows the unsupported state for a legacy maintenance payload returned with 200', async () => {
    mocks.getUsageMaintenance.mockResolvedValueOnce({ events: [] });
    mocks.listUsageArchives.mockResolvedValueOnce({ runs: [] });
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<UsageMaintenancePage />);
      renderers.add(renderer);
    });
    expect(getText(renderer.root)).toContain('older than the usage maintenance API');
    expect(getText(renderer.root)).not.toContain('full-screen-loading');
    expect(mocks.probeUsageMaintenance).toHaveBeenCalledTimes(1);
    expect(mocks.getUsageMaintenance).toHaveBeenCalledTimes(1);
    expect(mocks.listUsageArchives).toHaveBeenCalledTimes(1);
    act(() => renderer.unmount());
  });

  it('shows the unsupported state for a legacy archive-list payload returned with 200', async () => {
    mocks.getUsageMaintenance.mockResolvedValueOnce(maintenance());
    mocks.listUsageArchives.mockResolvedValueOnce({ events: [] });
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<UsageMaintenancePage />);
      renderers.add(renderer);
    });
    expect(getText(renderer.root)).toContain('older than the usage maintenance API');
    expect(getText(renderer.root)).not.toContain('full-screen-loading');
    expect(mocks.probeUsageMaintenance).toHaveBeenCalledTimes(1);
    expect(mocks.getUsageMaintenance).toHaveBeenCalledTimes(1);
    expect(mocks.listUsageArchives).toHaveBeenCalledTimes(1);
    act(() => renderer.unmount());
  });

  it('shows the unsupported state for non-numeric archive status counts', async () => {
    mocks.getUsageMaintenance.mockResolvedValueOnce(maintenance());
    mocks.listUsageArchives.mockResolvedValueOnce({
      runs: [],
      status_counts: { completed: '1' },
    });
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<UsageMaintenancePage />);
      renderers.add(renderer);
    });
    expect(getText(renderer.root)).toContain('older than the usage maintenance API');
    expect(getText(renderer.root)).not.toContain('full-screen-loading');
    act(() => renderer.unmount());
  });

  it('shows the unsupported state for a malformed preview payload returned with 200', async () => {
    mocks.previewUsageArchive.mockResolvedValueOnce({
      cutoff_timestamp_ms: 1_700_000_000_000,
      target_event_id: 100,
      event_count: 7,
      estimated_bytes: 2_048,
      min_timestamp_ms: Number.NaN,
    });
    const renderer = await renderResolvedPage();

    expect(getText(renderer.root)).toContain('older than the usage maintenance API');
    expect(findCreateButtons(renderer)).toHaveLength(0);
    expect(mocks.showNotification).not.toHaveBeenCalled();
    act(() => renderer.unmount());
  });

  it('offers stage-specific continuation actions for verifying and deleting runs', async () => {
    const renderer = await renderHistoryPage(maintenance(), [
      archive('verifying'),
      archive('deleting'),
    ]);
    expect(findButtons(renderer, 'Continue verification')).toHaveLength(1);
    expect(findButtons(renderer, 'Continue deletion')).toHaveLength(1);
    act(() => renderer.unmount());
  });

  it('translates known statuses and exposes unknown server values in record details', async () => {
    const known = archive('verified', 'known-mode-run');
    const unknown = {
      ...archive('completed', 'unknown-mode-run'),
      status: 'custom-state',
      mode: 'custom-mode',
    };
    const renderer = await renderHistoryPage(maintenance(), [known, unknown]);
    expect(getText(renderer.root)).toContain('translated:usage_maintenance.run_status_verified');
    expect(getText(renderer.root)).toContain('custom-state');
    mocks.getUsageArchive.mockResolvedValueOnce(archiveStatus(known));
    await act(async () => {
      findButtons(renderer, 'Details')[0].props.onClick();
    });
    expect(getText(renderer.root)).toContain('translated:usage_maintenance.run_mode_manual');
    act(() => findButtons(renderer, 'Close drawer')[0].props.onClick());
    mocks.getUsageArchive.mockResolvedValueOnce(archiveStatus(unknown));
    await act(async () => {
      findButtons(renderer, 'Details')[1].props.onClick();
    });
    expect(getText(renderer.root)).toContain('custom-mode');
  });
  it('identifies the run, event count, and cutoff in destructive confirmation', async () => {
    const run = {
      ...archive('verified', 'delete-target-run'),
      event_count: 12_345,
      deleted_event_count: 345,
      cutoff_timestamp_ms: 1_700_000_000_000,
    };
    const renderer = await renderHistoryPage(maintenance(), [run]);
    act(() => findButtons(renderer, 'Review cleanup')[0].props.onClick());
    const confirmation = getDrawerConfirmation(renderer) as {
      message: ReactNode;
      confirmText: string;
    };
    let messageRenderer!: ReactTestRenderer;
    act(() => {
      messageRenderer = create(<>{confirmation.message}</>);
    });
    const message = getText(messageRenderer.root);
    expect(message).toContain(run.id);
    expect(message).toContain((run.event_count - run.deleted_event_count).toLocaleString('en'));
    expect(message).toContain(run.event_count.toLocaleString('en'));
    expect(message).toContain('Cutoff (strictly before)');
    expect(message).toContain('server rechecks exact coverage before every batch');
    expect(message).toContain('missing detail must not be interpreted as zero usage');
    expect(message).toContain('frozen');
    expect(message).toContain('complete pre-deletion backup');
    expect(message).toContain('database file does not shrink immediately');
    expect(message).toContain('offline compaction');
    expect(confirmation.confirmText).toContain(
      (run.event_count - run.deleted_event_count).toLocaleString('en')
    );
    act(() => messageRenderer.unmount());
    act(() => renderer.unmount());
  });

  it('validates the resulting stage of a separate history action before reporting success', async () => {
    const run = archive('verified', 'wrong-delete-stage-run');
    const renderer = await renderHistoryPage(maintenance(), [run]);
    mocks.deleteUsageArchive.mockResolvedValueOnce(archiveStatus(run));

    act(() => findButtons(renderer, 'Review cleanup')[0].props.onClick());
    const confirmation = getDrawerConfirmation(renderer) as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      await confirmation.onConfirm();
    });

    expect(mocks.deleteUsageArchive).toHaveBeenCalledTimes(1);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'The server returned an invalid archive task response.',
      'error'
    );
    expect(mocks.showNotification).not.toHaveBeenCalledWith(
      'Logical deletion completed.',
      'success'
    );
    act(() => renderer.unmount());
  });

  it('accepts and warns about a delete completed concurrently with verification', async () => {
    const run = archive('archived', 'concurrent-verify-run');
    const renderer = await renderHistoryPage(maintenance(), [run]);
    mocks.verifyUsageArchive.mockResolvedValueOnce(archiveStatus(archive('completed', run.id)));

    await act(async () => {
      findButtons(renderer, 'Verify archive')[0].props.onClick();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.showNotification).toHaveBeenCalledWith(
      'This task advanced to raw-data deletion in another session. Its latest state is shown in archive history.',
      'warning'
    );
    expect(mocks.showNotification).not.toHaveBeenCalledWith('Archive run updated.', 'success');
    expect(mocks.showNotification).not.toHaveBeenCalledWith(
      'The server returned an invalid archive task response.',
      'error'
    );
    act(() => renderer.unmount());
  });

  it('binds a resume request to its displayed stage before accepting a concurrent delete', async () => {
    const run = { ...archive('failed', 'stale-resume-run'), resume_status: 'verifying' };
    const renderer = await renderHistoryPage(maintenance(), [run]);
    mocks.resumeUsageArchive.mockResolvedValueOnce(archiveStatus(archive('completed', run.id)));

    await act(async () => {
      findButtons(renderer, 'Continue verification')[0].props.onClick();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.resumeUsageArchive).toHaveBeenCalledWith(
      'http://manager-a.local:18317',
      run.id,
      'management-key-a',
      expect.any(AbortSignal),
      'verifying'
    );
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'This task advanced to raw-data deletion in another session. Its latest state is shown in archive history.',
      'warning'
    );
    expect(mocks.showNotification).not.toHaveBeenCalledWith('Archive run updated.', 'success');
    act(() => renderer.unmount());
  });

  it('clears stale guided status after the same task is continued from history', async () => {
    const failedRun = {
      ...archive('failed', 'created-run'),
      resume_status: 'verifying',
    };
    const verifiedRun = archive('verified', failedRun.id);
    const renderer = await renderResolvedPage();
    mocks.createUsageArchive.mockResolvedValueOnce(
      archiveStatus(archive('previewed', failedRun.id))
    );
    mocks.getUsageArchive.mockResolvedValueOnce(archiveStatus(failedRun));
    mocks.verifyUsageArchive.mockRejectedValueOnce(new Error('archive verification failed'));
    mocks.getUsageMaintenance.mockResolvedValueOnce(maintenance({ active_run: failedRun }));
    mocks.listUsageArchives.mockResolvedValueOnce({ runs: [failedRun] });

    act(() => findCreateButtons(renderer)[0].props.onClick());
    const createConfirmation = getDrawerConfirmation(renderer) as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      await createConfirmation.onConfirm();
    });
    expect(getText(renderer.root)).toContain('Needs attention');

    mocks.listUsageArchives.mockResolvedValue({ runs: [failedRun] });
    await act(async () => {
      findButtons(renderer, 'Archive management')[0].props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    mocks.resumeUsageArchive.mockResolvedValueOnce(archiveStatus(verifiedRun));
    mocks.getUsageMaintenance.mockResolvedValueOnce(maintenance({ active_run: verifiedRun }));
    mocks.listUsageArchives.mockResolvedValueOnce({ runs: [verifiedRun] });
    await act(async () => {
      findButtons(renderer, 'Continue verification')[0].props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(getText(renderer.root)).not.toContain('Needs attention');
    await act(async () => {
      findButtons(renderer, 'New archive')[0].props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(getText(renderer.root)).not.toContain('Needs attention');
    act(() => renderer.unmount());
  });

  it('does not request an impact preview while a history action changes maintenance state', async () => {
    const run = archive('verified', 'refresh-preview-run');
    const renderer = await renderHistoryPage(maintenance(), [run]);
    const initialPreviewCalls = mocks.previewUsageArchive.mock.calls.length;

    act(() => findButtons(renderer, 'Review cleanup')[0].props.onClick());
    const confirmation = getDrawerConfirmation(renderer) as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      await confirmation.onConfirm();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.previewUsageArchive).toHaveBeenCalledTimes(initialPreviewCalls);
    act(() => renderer.unmount());
  });

  it('requires destructive confirmation before resuming delete stages', async () => {
    const deletingRun = { ...archive('deleting', 'deleting-run'), deleted_event_count: 3 };
    const failedDeletingRun = {
      ...archive('failed', 'failed-deleting-run'),
      resume_status: 'deleting',
      deleted_event_count: 4,
    };
    const renderer = await renderHistoryPage(maintenance(), [deletingRun, failedDeletingRun]);
    mocks.resumeUsageArchive.mockImplementation((_base: string, runId: string) =>
      Promise.resolve(archiveStatus(archive('completed', runId)))
    );
    for (const [index, run] of [deletingRun, failedDeletingRun].entries()) {
      const row = renderer.root.findByProps({ 'data-run-id': run.id });
      act(() =>
        row
          .findAllByType('button')
          .find((button) => getText(button) === 'Continue deletion')!
          .props.onClick()
      );
      expect(mocks.resumeUsageArchive).toHaveBeenCalledTimes(index);
      const confirmation = getDrawerConfirmation(renderer);
      expect(confirmation.confirmText).toContain(String(run.event_count - run.deleted_event_count));
      const footer = renderer.root.findByProps({ 'data-testid': 'maintenance-drawer-footer' });
      expect(
        footer.findAllByType('button').some((button) => button.props.className.includes('danger'))
      ).toBe(true);
      await act(async () => confirmation.onConfirm());
      act(() => findButtons(renderer, 'Close drawer')[0].props.onClick());
    }
    expect(mocks.resumeUsageArchive).toHaveBeenCalledTimes(2);
    expect(mocks.resumeUsageArchive).toHaveBeenNthCalledWith(
      1,
      'http://manager-a.local:18317',
      deletingRun.id,
      'management-key-a',
      expect.any(AbortSignal),
      'deleting'
    );
    expect(mocks.resumeUsageArchive).toHaveBeenNthCalledWith(
      2,
      'http://manager-a.local:18317',
      failedDeletingRun.id,
      'management-key-a',
      expect.any(AbortSignal),
      'deleting'
    );
    expect(mocks.showNotification).toHaveBeenCalledWith('Logical deletion completed.', 'success');
    act(() => renderer.unmount());
  });

  it('translates active migration and aggregate phases and only hard-disables delete when archive deletion is disabled', async () => {
    const pending = maintenance({
      migration: { ...maintenance().migration, status: 'applying' },
      hourly_aggregate: { ...maintenance().hourly_aggregate, status: 'catching_up' },
      readiness: {
        migration_ready: false,
        hourly_aggregate_ready: false,
        archive_delete_enabled: true,
      },
    });
    const renderer = await renderHistoryPage(pending, [
      archive('verified'),
      { ...archive('failed', 'pending-archive-run'), resume_status: 'archiving' },
    ]);
    let deleteButton = findButtons(renderer, 'Review cleanup')[0];
    expect(deleteButton.props.disabled).toBe(false);
    expect(deleteButton.props.title).toContain('exact coverage');
    const pendingArchiveButton = findButtons(renderer, 'Continue archive').find(
      (button) => button.props.title
    );
    expect(pendingArchiveButton?.props.disabled).toBe(true);
    expect(String(pendingArchiveButton?.props.title)).toContain(
      'usage accounting preparation completes'
    );

    act(() => renderer.unmount());
    const pendingRenderer = await renderResolvedPage(pending);
    act(() => findButtons(pendingRenderer, 'Diagnostics')[0].props.onClick());
    expect(getText(pendingRenderer.root)).toContain(
      'translated:usage_maintenance.migration_status_applying'
    );
    expect(getText(pendingRenderer.root)).toContain(
      'translated:usage_maintenance.aggregate_status_catching_up'
    );
    expect(getText(pendingRenderer.root)).toContain('Hourly aggregate');
    act(() => pendingRenderer.unmount());

    const clearingRenderer = await renderResolvedPage(
      maintenance({
        migration: { ...maintenance().migration, status: 'clearing' },
        hourly_aggregate: { ...maintenance().hourly_aggregate, status: 'clearing' },
      })
    );
    act(() => findButtons(clearingRenderer, 'Diagnostics')[0].props.onClick());
    expect(getText(clearingRenderer.root)).toContain(
      'translated:usage_maintenance.migration_status_clearing'
    );
    expect(getText(clearingRenderer.root)).toContain(
      'translated:usage_maintenance.aggregate_status_clearing'
    );
    act(() => clearingRenderer.unmount());

    const disabledRenderer = await renderHistoryPage(
      maintenance({
        readiness: {
          migration_ready: true,
          hourly_aggregate_ready: true,
          archive_delete_enabled: false,
        },
      }),
      [
        archive('verified'),
        archive('deleting'),
        { ...archive('failed'), resume_status: 'deleting' },
      ]
    );
    deleteButton = findButtons(disabledRenderer, 'Review cleanup')[0];
    expect(deleteButton.props.disabled).toBe(true);
    expect(deleteButton.props.title).toContain('disabled');
    const destructiveResumeButtons = findButtons(disabledRenderer, 'Continue deletion');
    expect(destructiveResumeButtons).toHaveLength(2);
    expect(destructiveResumeButtons.every((button) => button.props.disabled)).toBe(true);
    expect(
      destructiveResumeButtons.every((button) => String(button.props.title).includes('disabled'))
    ).toBe(true);
    act(() => disabledRenderer.unmount());
  });

  it('aborts stale base/key loads and prevents old responses from replacing the new context', async () => {
    const oldMaintenance = deferred<UsageMaintenanceStatus>();
    const oldArchives = deferred<UsageArchiveList>();
    mocks.getUsageMaintenance.mockImplementation((base: string) =>
      base.includes('manager-a')
        ? oldMaintenance.promise
        : Promise.resolve(maintenance({ raw_event_count: 22 }))
    );
    mocks.listUsageArchives.mockImplementation((base: string) =>
      base.includes('manager-a')
        ? oldArchives.promise
        : Promise.resolve({ runs: [archive('completed', 'new-context-run')] })
    );

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<UsageMaintenancePage />);
      renderers.add(renderer);
      await Promise.resolve();
      await Promise.resolve();
    });
    const oldMaintenanceSignal = mocks.getUsageMaintenance.mock.calls[0][2] as AbortSignal;
    const oldArchivesSignal = mocks.listUsageArchives.mock.calls[0][3] as AbortSignal;

    mocks.availability.managerServiceBase = 'http://manager-b.local:18317';
    mocks.managementKey = 'management-key-b';
    await act(async () => {
      renderer.update(<UsageMaintenancePage />);
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(oldMaintenanceSignal.aborted).toBe(true);
    expect(oldArchivesSignal.aborted).toBe(true);
    expect(mocks.probeUsageMaintenance).toHaveBeenCalledTimes(2);
    expect(mocks.probeUsageMaintenance).toHaveBeenNthCalledWith(
      2,
      'http://manager-b.local:18317',
      'management-key-b',
      expect.any(AbortSignal)
    );
    expect(getText(renderer.root)).toContain('22');
    await act(async () => {
      findButtons(renderer, 'Archive management')[0].props.onClick();
    });
    expect(renderer.root.findAllByProps({ 'data-run-id': 'new-context-run' })).toHaveLength(1);

    await act(async () => {
      oldMaintenance.resolve(maintenance({ raw_event_count: 999 }));
      oldArchives.resolve({ runs: [archive('completed', 'stale-context-run')] });
      await Promise.resolve();
    });
    expect(getText(renderer.root)).not.toContain('999');
    expect(getText(renderer.root)).not.toContain('stale-context-run');
    act(() => renderer.unmount());
  });

  it('clears old-context data when the new archive configuration is unavailable', async () => {
    const renderer = await renderResolvedPage(maintenance({ raw_event_count: 11 }), [
      archive('completed', 'old-context-run'),
    ]);

    mocks.availability.managerServiceBase = 'http://manager-b.local:18317';
    mocks.managementKey = 'management-key-b';
    mocks.getUsageMaintenance.mockRejectedValueOnce(
      Object.assign(new Error('usage archive is unavailable'), {
        status: 503,
        code: 'usage_archive_unavailable',
      })
    );
    mocks.listUsageArchives.mockResolvedValueOnce({ runs: [] });
    await act(async () => {
      renderer.update(<UsageMaintenancePage />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(getText(renderer.root)).toContain('usage archive is unavailable');
    expect(getText(renderer.root)).not.toContain('older than the usage maintenance API');
    expect(getText(renderer.root)).not.toContain('old-context-run');
    act(() => renderer.unmount());
  });

  it('aborts and ignores a pending preview when base and key change', async () => {
    const previewRequest = deferred<UsageArchivePreview>();
    mocks.previewUsageArchive.mockImplementation((base: string) =>
      base.includes('manager-a')
        ? previewRequest.promise
        : Promise.resolve({
            cutoff_timestamp_ms: 1_700_000_000_000,
            target_event_id: 0,
            event_count: 0,
            estimated_bytes: 0,
          })
    );
    const renderer = await renderResolvedPage();
    const oldSignal = mocks.previewUsageArchive.mock.calls[0][3] as AbortSignal;

    mocks.availability.managerServiceBase = 'http://manager-b.local:18317';
    mocks.managementKey = 'management-key-b';
    await act(async () => {
      renderer.update(<UsageMaintenancePage />);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(oldSignal.aborted).toBe(true);

    await act(async () => {
      previewRequest.resolve({
        cutoff_timestamp_ms: 1_700_000_000_000,
        target_event_id: 100,
        event_count: 7,
        estimated_bytes: 2_048,
      });
      await Promise.resolve();
    });
    expect(findCreateButtons(renderer)).toHaveLength(0);
    expect(findButtons(renderer, 'New archive')).toHaveLength(1);
    expect(mocks.showNotification).not.toHaveBeenCalled();
    act(() => renderer.unmount());
  });

  it('isolates a pending mutation from a new-context operation', async () => {
    const oldRun = archive('previewed', 'old-context-run');
    const oldResume = deferred<unknown>();
    mocks.resumeUsageArchive.mockImplementationOnce(() => oldResume.promise);
    const renderer = await renderHistoryPage(maintenance({ active_run: oldRun }), [oldRun]);
    act(() => findButtons(renderer, 'Continue archive')[0].props.onClick());
    const oldSignal = mocks.resumeUsageArchive.mock.calls[0][3] as AbortSignal;

    mocks.availability.managerServiceBase = 'http://manager-b.local:18317';
    mocks.managementKey = 'management-key-b';
    mocks.getUsageMaintenance.mockResolvedValue(maintenance({ raw_event_count: 22 }));
    mocks.listUsageArchives.mockResolvedValue({
      runs: [archive('completed', 'new-context-run')],
    });
    await act(async () => {
      renderer.update(<UsageMaintenancePage />);
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(oldSignal.aborted).toBe(true);
    expect(renderer.root.findAllByProps({ 'data-run-id': 'new-context-run' })).toHaveLength(1);

    await act(async () => {
      findButtons(renderer, 'New archive')[0].props.onClick();
    });
    const newPreview = deferred<UsageArchivePreview>();
    mocks.previewUsageArchive.mockImplementationOnce(() => newPreview.promise);
    await act(async () => {
      findButtons(renderer, 'Older than 7 days')[0].props.onClick();
      await Promise.resolve();
    });
    const newSignal = mocks.previewUsageArchive.mock.calls[
      mocks.previewUsageArchive.mock.calls.length - 1
    ][3] as AbortSignal;
    expect(getText(renderer.root)).toContain('Calculating…');
    const loadCountAfterContextChange = mocks.getUsageMaintenance.mock.calls.length;

    await act(async () => {
      oldResume.resolve({});
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(newSignal.aborted).toBe(false);
    expect(getText(renderer.root)).toContain('Calculating…');
    expect(mocks.getUsageMaintenance).toHaveBeenCalledTimes(loadCountAfterContextChange);
    expect(mocks.showNotification).not.toHaveBeenCalled();

    await act(async () => {
      newPreview.resolve({
        cutoff_timestamp_ms: 1_700_000_000_000,
        target_event_id: 100,
        event_count: 7,
        estimated_bytes: 2_048,
      });
      await Promise.resolve();
    });
    expect(mocks.getUsageMaintenance).toHaveBeenCalledTimes(loadCountAfterContextChange);
    expect(findCreateButtons(renderer)).toHaveLength(1);
    act(() => renderer.unmount());
  });

  it('does not execute a destructive confirmation after base and key change', async () => {
    const run = archive('verified', 'old-confirmation-run');
    const renderer = await renderHistoryPage(maintenance(), [run]);
    act(() => findButtons(renderer, 'Review cleanup')[0].props.onClick());
    const confirmation = getDrawerConfirmation(renderer) as {
      onConfirm: () => Promise<void>;
    };

    mocks.availability.managerServiceBase = 'http://manager-b.local:18317';
    mocks.managementKey = 'management-key-b';
    mocks.getUsageMaintenance.mockResolvedValue(maintenance({ raw_event_count: 22 }));
    mocks.listUsageArchives.mockResolvedValue({ runs: [] });
    await act(async () => {
      renderer.update(<UsageMaintenancePage />);
      await Promise.resolve();
      await Promise.resolve();
    });
    await act(async () => {
      await confirmation.onConfirm();
    });
    expect(mocks.deleteUsageArchive).not.toHaveBeenCalled();
    expect(mocks.getUsageMaintenance).toHaveBeenCalledTimes(2);
    act(() => renderer.unmount());
  });

  it('does not execute saved create or delete confirmations after unmount', async () => {
    mocks.previewUsageArchive.mockResolvedValueOnce({
      cutoff_timestamp_ms: 1_700_000_000_000,
      target_event_id: 100,
      event_count: 7,
      estimated_bytes: 2_048,
    });
    const createRenderer = await renderResolvedPage();
    act(() => findCreateButtons(createRenderer)[0].props.onClick());
    const createConfirmation = getDrawerConfirmation(createRenderer) as {
      onConfirm: () => Promise<void>;
    };
    const createLoadCount = mocks.getUsageMaintenance.mock.calls.length;
    act(() => createRenderer.unmount());
    await createConfirmation.onConfirm();
    expect(mocks.createUsageArchive).not.toHaveBeenCalled();
    expect(mocks.getUsageMaintenance).toHaveBeenCalledTimes(createLoadCount);
    expect(mocks.showNotification).not.toHaveBeenCalled();

    vi.clearAllMocks();
    const run = archive('verified', 'unmounted-delete-run');
    const deleteRenderer = await renderHistoryPage(maintenance(), [run]);
    act(() => findButtons(deleteRenderer, 'Review cleanup')[0].props.onClick());
    const deleteConfirmation = getDrawerConfirmation(deleteRenderer) as {
      onConfirm: () => Promise<void>;
    };
    const deleteLoadCount = mocks.getUsageMaintenance.mock.calls.length;
    act(() => deleteRenderer.unmount());
    await deleteConfirmation.onConfirm();
    expect(mocks.deleteUsageArchive).not.toHaveBeenCalled();
    expect(mocks.getUsageMaintenance).toHaveBeenCalledTimes(deleteLoadCount);
    expect(mocks.showNotification).not.toHaveBeenCalled();
  });

  it('does not execute an old destructive confirmation after an A-B-A context change', async () => {
    const run = archive('verified', 'aba-delete-run');
    const renderer = await renderHistoryPage(maintenance(), [run]);
    act(() => findButtons(renderer, 'Review cleanup')[0].props.onClick());
    const confirmation = getDrawerConfirmation(renderer) as {
      onConfirm: () => Promise<void>;
    };

    mocks.availability.managerServiceBase = 'http://manager-b.local:18317';
    mocks.managementKey = 'management-key-b';
    await act(async () => {
      renderer.update(<UsageMaintenancePage />);
      await Promise.resolve();
      await Promise.resolve();
    });
    mocks.availability.managerServiceBase = 'http://manager-a.local:18317';
    mocks.managementKey = 'management-key-a';
    await act(async () => {
      renderer.update(<UsageMaintenancePage />);
      await Promise.resolve();
      await Promise.resolve();
    });
    const loadCount = mocks.getUsageMaintenance.mock.calls.length;
    await confirmation.onConfirm();

    expect(mocks.deleteUsageArchive).not.toHaveBeenCalled();
    expect(mocks.getUsageMaintenance).toHaveBeenCalledTimes(loadCount);
    expect(mocks.showNotification).not.toHaveBeenCalled();
    act(() => renderer.unmount());
  });

  it('polls active state without a loading flash, pauses while working, refreshes failures, and cleans up', async () => {
    vi.useFakeTimers();
    const active = archive('verifying');
    const renderer = await renderOverviewPage(maintenance({ active_run: active }), [active]);
    const pollMaintenance = deferred<UsageMaintenanceStatus>();
    const pollArchives = deferred<UsageArchiveList>();
    mocks.getUsageMaintenance.mockImplementationOnce(() => pollMaintenance.promise);
    mocks.listUsageArchives.mockImplementationOnce(() => pollArchives.promise);

    act(() => vi.advanceTimersByTime(5_000));
    expect(mocks.getUsageMaintenance).toHaveBeenCalledTimes(2);
    expect(mocks.probeUsageMaintenance).toHaveBeenCalledTimes(1);
    expect(getText(renderer.root)).not.toContain('full-screen-loading');
    const pollMaintenanceSignal = mocks.getUsageMaintenance.mock.calls[1][2] as AbortSignal;
    const pollArchivesSignal = mocks.listUsageArchives.mock.calls[1][3] as AbortSignal;
    act(() => renderer.unmount());
    expect(pollMaintenanceSignal.aborted).toBe(true);
    expect(pollArchivesSignal.aborted).toBe(true);
    act(() => vi.advanceTimersByTime(10_000));
    expect(mocks.getUsageMaintenance).toHaveBeenCalledTimes(2);

    vi.clearAllMocks();
    mocks.availability.managerServiceBase = 'http://manager-a.local:18317';
    const failedActive = {
      ...archive('failed', active.id),
      resume_status: 'verifying' as const,
    };
    mocks.getUsageMaintenance
      .mockResolvedValueOnce(maintenance({ active_run: active }))
      .mockResolvedValueOnce(maintenance({ active_run: failedActive }));
    mocks.listUsageArchives
      .mockResolvedValueOnce({ runs: [active] })
      .mockResolvedValueOnce({ runs: [failedActive] });
    mocks.getUsageArchive
      .mockResolvedValueOnce(archiveStatus(active))
      .mockResolvedValue(archiveStatus(failedActive));
    const resumeFailure = deferred<unknown>();
    mocks.resumeUsageArchive.mockImplementationOnce(() => resumeFailure.promise);
    const failedRenderer = await renderOverviewPage(maintenance({ active_run: active }), [active]);
    await act(async () => {
      findButtons(failedRenderer, 'Continue this record')[0].props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    act(() => findButtons(failedRenderer, 'Continue verification')[0].props.onClick());
    act(() => vi.advanceTimersByTime(5_000));
    expect(mocks.getUsageMaintenance).toHaveBeenCalledTimes(1);
    await act(async () => {
      resumeFailure.reject(new Error('verification interrupted'));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(mocks.showNotification).toHaveBeenCalledWith('verification interrupted', 'error');
    expect(mocks.getUsageMaintenance).toHaveBeenCalledTimes(2);
    expect(getText(failedRenderer.root)).toContain('failed');
    act(() => failedRenderer.unmount());
  });

  it('polls while an explicit maintenance lock is present', async () => {
    vi.useFakeTimers();
    const locked = maintenance({
      active_lock: {
        run_id: 'locked-run',
        operation: 'archive',
        acquired_at_ms: 1_700_000_000_000,
        updated_at_ms: 1_700_000_001_000,
      },
    });
    const renderer = await renderOverviewPage(locked);
    await act(async () => {
      vi.advanceTimersByTime(5_000);
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(mocks.getUsageMaintenance).toHaveBeenCalledTimes(2);
    act(() => renderer.unmount());
  });

  it('keeps polling a retention run while the worker is waiting to retry a failure', async () => {
    vi.useFakeTimers();
    const failedRetentionRun = {
      ...archive('failed', 'retention-retry-run'),
      mode: 'retention',
      resume_status: 'verifying',
    };
    const renderer = await renderOverviewPage(maintenance({ active_run: failedRetentionRun }), [
      failedRetentionRun,
    ]);

    await act(async () => {
      vi.advanceTimersByTime(5_000);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.getUsageMaintenance).toHaveBeenCalledTimes(2);
    expect(mocks.listUsageArchives).toHaveBeenCalledTimes(2);
    act(() => renderer.unmount());
  });

  it('surfaces post-delete notice on overview with freshly read reclaimable bytes when reclaimable > 0', async () => {
    const targetRun = archive('verified', 'post-delete-run-b');
    const initialMaintenance = maintenance({
      storage: {
        page_size: 4_096,
        page_count: 20,
        freelist_count: 1,
        reclaimable_bytes: 4_096, // Old reclaimable = 4 KB
        database_bytes: 81_920,
        wal_bytes: 0,
        shm_bytes: 0,
        total_bytes: 81_920,
      },
    });
    const refreshedMaintenance = maintenance({
      storage: {
        page_size: 4_096,
        page_count: 100_000,
        freelist_count: 655_360,
        reclaimable_bytes: 2_684_354_560, // New reclaimable = 2.5 GB (A != B)
        database_bytes: 5_000_000_000,
        wal_bytes: 0,
        shm_bytes: 0,
        total_bytes: 5_000_000_000,
      },
    });

    const renderer = await renderHistoryPage(initialMaintenance, [targetRun]);

    // Mock delete API returns completed run
    const completedRun = {
      ...targetRun,
      status: 'completed' as const,
      deleted_event_count: 10,
    };
    mocks.deleteUsageArchive.mockResolvedValueOnce(archiveStatus(completedRun));
    // Subsequent getUsageMaintenance returns the fresh maintenance with 2.5 GB reclaimable
    mocks.getUsageMaintenance.mockResolvedValueOnce(refreshedMaintenance);

    act(() => findButtons(renderer, 'Review cleanup')[0].props.onClick());
    const confirmation = getDrawerConfirmation(renderer) as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      await confirmation.onConfirm();
    });

    // Overview should be active
    const text = getText(renderer.root);
    expect(text).toContain('Archive management');
    expect(text).toContain('Online detail cleanup complete');
    // Notice uses the newly refreshed value (2.50 GB), not the old 4 KB
    expect(text).toContain('2.50 GB');
    expect(text).not.toContain('4 KB of space can be reclaimed');
    expect(text).toContain('SQLite file does not shrink immediately');
    expect(findButtons(renderer, 'View storage and compaction')).toHaveLength(1);
    expect(
      renderer.root
        .findByProps({ 'data-testid': 'usage-post-delete-notice' })
        .findAllByType('button')
        .filter((button) => getText(button) === 'Close')
    ).toHaveLength(1);

    // Verify heading accessibility: notice title does not use h2 before overview h1
    const notice = renderer.root.findByProps({ 'data-testid': 'usage-post-delete-notice' });
    expect(notice.findAllByType('h2')).toHaveLength(0);
    expect(notice.findAllByType('strong')).toHaveLength(1);

    act(() => renderer.unmount());
  });

  it('displays no-reclaimable note instead of 0 B when reclaimable == 0 after deletion', async () => {
    const targetRun = archive('verified', 'post-delete-run-c');
    const initialMaintenance = maintenance();
    const refreshedMaintenance = maintenance({
      storage: {
        page_size: 4_096,
        page_count: 20,
        freelist_count: 0,
        reclaimable_bytes: 0, // Zero reclaimable
        database_bytes: 81_920,
        wal_bytes: 0,
        shm_bytes: 0,
        total_bytes: 81_920,
      },
    });

    const renderer = await renderHistoryPage(initialMaintenance, [targetRun]);

    const completedRun = {
      ...targetRun,
      status: 'completed' as const,
      deleted_event_count: 10,
    };
    mocks.deleteUsageArchive.mockResolvedValueOnce(archiveStatus(completedRun));
    mocks.getUsageMaintenance.mockResolvedValueOnce(refreshedMaintenance);

    act(() => findButtons(renderer, 'Review cleanup')[0].props.onClick());
    const confirmation = getDrawerConfirmation(renderer) as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      await confirmation.onConfirm();
    });

    const text = getText(renderer.root);
    expect(text).toContain('Online detail cleanup complete');
    expect(text).toContain('No significant reclaimable free pages were detected');
    expect(text).toContain('Physical compaction is not immediately necessary');
    expect(text).not.toContain('0 B of space can be reclaimed');
    expect(text).not.toContain('0.00 B of space can be reclaimed');

    act(() => renderer.unmount());
  });

  it('handles maintenance refresh failure after deletion without rolling back delete or fabricating metrics', async () => {
    const targetRun = archive('verified', 'post-delete-run-d');
    const initialMaintenance = maintenance();

    const renderer = await renderHistoryPage(initialMaintenance, [targetRun]);

    const historyListCallsBeforeDelete = mocks.listUsageArchives.mock.calls.filter(
      ([, , options]) => typeof options === 'object'
    ).length;

    const completedRun = {
      ...targetRun,
      status: 'completed' as const,
      deleted_event_count: 10,
    };
    mocks.deleteUsageArchive.mockResolvedValueOnce(archiveStatus(completedRun));
    // Refresh fails
    mocks.getUsageMaintenance.mockRejectedValueOnce(new Error('refresh failed'));
    // Focused history reload returns updated completed list
    mocks.listUsageArchives.mockImplementation(
      (_base: string, _key: string | undefined, options: unknown) => {
        if (typeof options === 'object') {
          return Promise.resolve({ runs: [completedRun] });
        }
        return Promise.resolve({ runs: [] });
      }
    );

    act(() => findButtons(renderer, 'Review cleanup')[0].props.onClick());
    const confirmation = getDrawerConfirmation(renderer) as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      await confirmation.onConfirm();
      await Promise.resolve();
      await Promise.resolve();
    });

    // Delete success notification is still shown
    expect(mocks.showNotification).toHaveBeenCalledWith('Logical deletion completed.', 'success');
    // Cleanup remains successful while fresh metrics are unavailable
    const text = getText(renderer.root);
    expect(text).toContain('Online detail cleanup complete');
    expect(text).toContain('Current storage statistics are unavailable');
    expect(text).toContain('refresh failed');
    expect(text).not.toContain('can be reclaimed');

    // Delete raw button is removed because history was updated to completed run
    expect(findButtons(renderer, 'Review cleanup')).toHaveLength(0);
    expect(renderer.root.findAllByProps({ 'data-run-id': 'post-delete-run-d' })).toHaveLength(1);

    // Asserts focused history refresh was triggered once to update list without clearing error
    const historyListCallsAfterDelete = mocks.listUsageArchives.mock.calls.filter(
      ([, , options]) => typeof options === 'object'
    ).length;
    expect(historyListCallsAfterDelete).toBe(historyListCallsBeforeDelete);

    act(() => renderer.unmount());
  });

  it('preserves maintenance refresh failure in detail view after deletion without extra archive fetch', async () => {
    const targetRun = archive('verified', 'detail-delete-refresh-failure');
    mocks.getUsageArchive.mockResolvedValue(archiveStatus(targetRun));

    const renderer = await renderHistoryPage(maintenance(), [targetRun]);

    // Open detail view
    await act(async () => {
      findButtons(renderer, 'Details')[0].props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(getText(renderer.root)).toContain('Archive task details');
    expect(getText(renderer.root)).toContain('Review cleanup');

    const archiveCallsBeforeDelete = mocks.getUsageArchive.mock.calls.length;

    const completedRun = {
      ...targetRun,
      status: 'completed' as const,
      deleted_event_count: 10,
    };
    mocks.deleteUsageArchive.mockResolvedValueOnce(archiveStatus(completedRun));
    mocks.getUsageMaintenance.mockRejectedValueOnce(new Error('refresh failed'));
    // If buggy implementation triggers getUsageArchive, it would succeed and clear error
    mocks.getUsageArchive.mockResolvedValueOnce(archiveStatus(completedRun));

    act(() => findButtons(renderer, 'Review cleanup')[0].props.onClick());
    const confirmation = getDrawerConfirmation(renderer) as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      await confirmation.onConfirm();
      await Promise.resolve();
      await Promise.resolve();
    });

    // 1. Success notification for deletion is shown
    expect(mocks.showNotification).toHaveBeenCalledWith('Logical deletion completed.', 'success');

    // 2. Refresh failure error is preserved
    const text = getText(renderer.root);
    expect(text).toContain('refresh failed');

    // 3. Detail view updated to completed directly from response, without Delete raw button
    expect(findButtons(renderer, 'Review cleanup')).toHaveLength(0);
    expect(text).toContain('Archive task details');
    expect(text).toContain('completed');

    // 4. No extra getUsageArchive call was made
    expect(mocks.getUsageArchive.mock.calls.length).toBe(archiveCallsBeforeDelete);

    act(() => renderer.unmount());
  });

  it('clears existing error when history list succeeds on subsequent request', async () => {
    const targetRun = archive('completed', 'history-recovered-run');
    const renderer = await renderOverviewPage(maintenance(), []);

    // Initial history request fails
    mocks.listUsageArchives.mockRejectedValueOnce(new Error('temporary history network error'));

    await act(async () => {
      renderer.root.findAllByType('select')[0].props.onChange({ target: { value: 'completed' } });
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(getText(renderer.root)).toContain('temporary history network error');

    // Next history request succeeds
    mocks.listUsageArchives.mockResolvedValueOnce({ runs: [targetRun] });

    // Click refresh to reload history
    const refreshButton = findButtons(renderer, 'Refresh').slice(-1)[0];
    await act(async () => {
      refreshButton.props.onClick();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    const text = getText(renderer.root);
    expect(text).not.toContain('temporary history network error');
    expect(renderer.root.findAllByProps({ 'data-run-id': 'history-recovered-run' })).toHaveLength(
      1
    );

    act(() => renderer.unmount());
  });

  it('updates post-delete notice live as maintenance is refreshed without caching stale storage snapshot', async () => {
    const targetRun = archive('verified', 'post-delete-live-update-run');
    const initialMaintenance = maintenance({
      storage: {
        page_size: 4_096,
        page_count: 20,
        freelist_count: 1,
        reclaimable_bytes: 4_096,
        database_bytes: 81_920,
        wal_bytes: 0,
        shm_bytes: 0,
        total_bytes: 81_920,
      },
    });
    const afterDeleteMaintenance = maintenance({
      storage: {
        page_size: 4_096,
        page_count: 100_000,
        freelist_count: 655_360,
        reclaimable_bytes: 2_684_354_560, // 2.50 GB
        database_bytes: 5_000_000_000,
        wal_bytes: 0,
        shm_bytes: 0,
        total_bytes: 5_000_000_000,
      },
    });

    const renderer = await renderHistoryPage(initialMaintenance, [targetRun]);

    mocks.deleteUsageArchive.mockResolvedValueOnce(
      archiveStatus({ ...targetRun, status: 'completed', deleted_event_count: 50 })
    );
    mocks.getUsageMaintenance.mockResolvedValueOnce(afterDeleteMaintenance);

    act(() => findButtons(renderer, 'Review cleanup')[0].props.onClick());
    const confirmation = getDrawerConfirmation(renderer) as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      await confirmation.onConfirm();
    });

    // 1. Initial notice after delete reflects 2.50 GB
    let text = getText(renderer.root);
    expect(text).toContain('Online detail cleanup complete');
    expect(text).toContain('2.50 GB');

    // 2. User clicks Refresh, API returns 512 MB reclaimable
    const halfGbMaintenance = maintenance({
      storage: {
        ...afterDeleteMaintenance.storage,
        reclaimable_bytes: 536_870_912, // 512 MB
      },
    });
    mocks.getUsageMaintenance.mockResolvedValueOnce(halfGbMaintenance);

    await act(async () => {
      const refreshButton =
        findButtons(renderer, 'Refresh')[0] ?? findButtons(renderer, 'Refresh')[0];
      refreshButton.props.onClick();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    text = getText(renderer.root);
    expect(text).toContain('512.00 MB');
    expect(text).not.toContain('2.50 GB');

    // 3. User clicks Refresh again, API returns 0 reclaimable
    const zeroReclaimableMaintenance = maintenance({
      storage: {
        ...afterDeleteMaintenance.storage,
        freelist_count: 0,
        reclaimable_bytes: 0,
      },
    });
    mocks.getUsageMaintenance.mockResolvedValueOnce(zeroReclaimableMaintenance);

    await act(async () => {
      const refreshButton =
        findButtons(renderer, 'Refresh')[0] ?? findButtons(renderer, 'Refresh')[0];
      refreshButton.props.onClick();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    text = getText(renderer.root);
    expect(text).toContain('No significant reclaimable free pages were detected');
    expect(text).not.toContain('0 B of space can be reclaimed');
    expect(text).not.toContain('512.00 MB');

    act(() => renderer.unmount());
  });

  it('clears post-delete notice when manager service context changes', async () => {
    const targetRun = archive('verified', 'post-delete-context-run');
    const initialMaintenance = maintenance();
    const refreshedMaintenance = maintenance({
      storage: {
        page_size: 4_096,
        page_count: 100_000,
        freelist_count: 655_360,
        reclaimable_bytes: 2_684_354_560,
        database_bytes: 5_000_000_000,
        wal_bytes: 0,
        shm_bytes: 0,
        total_bytes: 5_000_000_000,
      },
    });

    const renderer = await renderHistoryPage(initialMaintenance, [targetRun]);

    mocks.deleteUsageArchive.mockResolvedValueOnce(
      archiveStatus({ ...targetRun, status: 'completed', deleted_event_count: 10 })
    );
    mocks.getUsageMaintenance.mockResolvedValueOnce(refreshedMaintenance);

    act(() => findButtons(renderer, 'Review cleanup')[0].props.onClick());
    const confirmation = getDrawerConfirmation(renderer) as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      await confirmation.onConfirm();
    });

    expect(getText(renderer.root)).toContain('Online detail cleanup complete');

    // Context changes (e.g. managerServiceBase changes)
    await act(async () => {
      mocks.availability.managerServiceBase = 'http://manager-b.local:18317';
      mocks.getUsageMaintenance.mockResolvedValueOnce(maintenance());
      mocks.listUsageArchives.mockResolvedValueOnce({ runs: [] });
      renderer.update(<UsageMaintenancePage />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(getText(renderer.root)).not.toContain('Online detail cleanup complete');

    act(() => renderer.unmount());
  });

  it('handles notice buttons: copy compact command, open advanced view, and dismiss', async () => {
    const targetRun = archive('verified', 'post-delete-run-e');
    const initialMaintenance = maintenance();
    const refreshedMaintenance = maintenance({
      storage: {
        page_size: 4_096,
        page_count: 100_000,
        freelist_count: 655_360,
        reclaimable_bytes: 2_684_354_560,
        database_bytes: 5_000_000_000,
        wal_bytes: 0,
        shm_bytes: 0,
        total_bytes: 5_000_000_000,
      },
    });

    const renderer = await renderHistoryPage(initialMaintenance, [targetRun]);

    mocks.deleteUsageArchive.mockResolvedValueOnce(
      archiveStatus({ ...targetRun, status: 'completed', deleted_event_count: 10 })
    );
    mocks.getUsageMaintenance.mockResolvedValueOnce(refreshedMaintenance);

    act(() => findButtons(renderer, 'Review cleanup')[0].props.onClick());
    const confirmation = getDrawerConfirmation(renderer) as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      await confirmation.onConfirm();
    });

    expect(getText(renderer.root)).toContain('Online detail cleanup complete');

    // Copy is available with the offline instructions in Advanced maintenance
    const writeTextMock = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal('navigator', {
      clipboard: { writeText: writeTextMock },
    });
    await act(async () => {
      findButtons(renderer, 'View storage and compaction')[0].props.onClick();
    });
    await act(async () => {
      findButtons(renderer, 'Copy command')[0].props.onClick();
      await Promise.resolve();
    });
    expect(writeTextMock).toHaveBeenCalledWith(COMPACT_USAGE_COMMAND);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'Offline compact command copied.',
      'success'
    );

    // 2. Open advanced maintenance button
    await act(async () => {
      findButtons(renderer, 'View storage and compaction')[0].props.onClick();
      await Promise.resolve();
    });
    expect(getText(renderer.root)).toContain('Storage and compaction');

    // Return to overview
    await act(async () => {
      findButtons(renderer, 'Close drawer')[0].props.onClick();
      await Promise.resolve();
    });
    expect(getText(renderer.root)).toContain('Online detail cleanup complete');

    // 3. Dismiss button
    act(() => findButtons(renderer, 'Close')[0].props.onClick());
    expect(getText(renderer.root)).not.toContain('Online detail cleanup complete');

    act(() => renderer.unmount());
  });
});

describe('maintenance workspace navigation and continuous operations', () => {
  const installHash = (hash: string) => {
    const events = new EventTarget();
    const browser = {
      location: { hash },
      addEventListener: events.addEventListener.bind(events),
      removeEventListener: events.removeEventListener.bind(events),
    };
    vi.stubGlobal('window', browser);
    return (next: string) => {
      browser.location.hash = next;
      events.dispatchEvent(new Event('hashchange'));
    };
  };

  it('restores a record drawer from the URL using reads only, including forged action parameters', async () => {
    installHash('#/usage-maintenance?tab=history&run=route-record&panel=run&action=delete');
    const renderer = await renderOverviewPage(maintenance(), [archive('verified', 'route-record')]);
    expect(renderer.root.findByProps({ 'data-testid': 'maintenance-drawer' })).toBeTruthy();
    expect(mocks.getUsageArchive).toHaveBeenCalledWith(
      'http://manager-a.local:18317',
      'route-record',
      'management-key-a',
      expect.any(AbortSignal)
    );
    expect(mocks.createUsageArchive).not.toHaveBeenCalled();
    expect(mocks.resumeUsageArchive).not.toHaveBeenCalled();
    expect(mocks.deleteUsageArchive).not.toHaveBeenCalled();
  });

  it('restores tab and cutoff controls on same-path hash navigation', async () => {
    const changeHash = installHash('#/usage-maintenance?tab=history');
    const renderer = await renderOverviewPage();
    await act(async () => {
      changeHash('#/usage-maintenance?panel=create&days=7');
    });
    expect(findButtons(renderer, 'Archive management')[0].props['aria-selected']).toBe(true);
    expect(findButtons(renderer, 'Older than 7 days')[0].props['aria-pressed']).toBe(true);
    await act(async () => {
      changeHash('#/usage-maintenance?tab=history');
    });
    expect(findButtons(renderer, 'Archive management')[0].props['aria-selected']).toBe(true);
    expect(mocks.createUsageArchive).not.toHaveBeenCalled();
  });

  it('preserves the URL while the initial service and authentication context becomes available', async () => {
    installHash(
      '#/usage-maintenance?tab=history&run=restored-record&panel=run&intent=cleanup&days=7'
    );
    mocks.availability.checking = true;
    mocks.availability.managerServiceBase = '';
    mocks.managementKey = '';
    const renderer = await renderOverviewPage(maintenance(), [
      archive('verified', 'restored-record'),
    ]);

    mocks.availability.checking = false;
    mocks.availability.managerServiceBase = 'http://manager-a.local:18317';
    mocks.managementKey = 'management-key-a';
    await act(async () => renderer.update(<UsageMaintenancePage />));

    expect(renderer.root.findByProps({ 'data-testid': 'maintenance-drawer' })).toBeTruthy();
    expect(getText(renderer.root)).toContain('restored-record');
    expect(mocks.navigate).not.toHaveBeenCalled();
    act(() => findButtons(renderer, 'Close drawer')[0].props.onClick());
    act(() => findButtons(renderer, 'New archive')[0].props.onClick());
    expect(findButtons(renderer, 'Older than 7 days')[0].props['aria-pressed']).toBe(true);
    expect(findButtons(renderer, 'Archive, then clean up')[0].props['aria-pressed']).toBe(true);
    expect(mocks.createUsageArchive).not.toHaveBeenCalled();
    expect(mocks.deleteUsageArchive).not.toHaveBeenCalled();
  });

  it('preserves an active archive when the router replaces its navigation callback', async () => {
    const pending = deferred<unknown>();
    mocks.createUsageArchive.mockImplementationOnce(() => pending.promise);
    const renderer = await renderResolvedPage();
    act(() => findButtons(renderer, 'Older than 7 days')[0].props.onClick());
    await act(async () => {
      await Promise.resolve();
    });
    act(() => findCreateButtons(renderer)[0].props.onClick());
    act(() => getDrawerConfirmation(renderer).onConfirm());
    const signal = mocks.createUsageArchive.mock.calls[0][3] as AbortSignal;

    mocks.navigate = vi.fn();
    await act(async () => renderer.update(<UsageMaintenancePage />));

    expect(signal.aborted).toBe(false);
    expect(findButtons(renderer, 'Older than 7 days')[0].props['aria-pressed']).toBe(true);
    expect(findButtons(renderer, 'Stop waiting')[0].props.disabled).not.toBe(true);
    await act(async () => {
      pending.resolve(archiveStatus());
    });
    expect(getText(renderer.root)).toContain(
      'Archive complete. Online details are still available.'
    );
    expect(mocks.deleteUsageArchive).not.toHaveBeenCalled();
  });

  it('keeps URL restoration read-only across Strict Mode effect replay', async () => {
    installHash('#/usage-maintenance?tab=history&run=strict-record&panel=run');
    mocks.getUsageMaintenance.mockResolvedValue(maintenance());
    mocks.listUsageArchives.mockResolvedValue({ runs: [archive('verified', 'strict-record')] });
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <StrictMode>
          <UsageMaintenancePage />
        </StrictMode>
      );
      renderers.add(renderer);
    });
    expect(renderer.root.findByProps({ 'data-testid': 'maintenance-drawer' })).toBeTruthy();
    expect(getText(renderer.root)).toContain('strict-record');
    expect(mocks.navigate).not.toHaveBeenCalled();
    expect(mocks.createUsageArchive).not.toHaveBeenCalled();
    expect(mocks.deleteUsageArchive).not.toHaveBeenCalled();
  });

  it('shows the preview in confirmation and closes confirmation before the archive finishes', async () => {
    const pending = deferred<unknown>();
    mocks.createUsageArchive.mockImplementationOnce(() => pending.promise);
    const renderer = await renderResolvedPage();
    act(() => findCreateButtons(renderer)[0].props.onClick());
    const confirmation = getDrawerConfirmation(renderer) as {
      message: ReactNode;
      onConfirm: () => unknown;
    };
    let summary!: ReactTestRenderer;
    act(() => {
      summary = create(<>{confirmation.message}</>);
      renderers.add(summary);
    });
    expect(summary.root.findAllByType('dd')[0].children).toEqual(['7']);
    expect(getText(summary.root)).toContain('KB');
    act(() => {
      expect(confirmation.onConfirm()).toBeUndefined();
    });
    expect(findButtons(renderer, 'Stop waiting')[0].props.disabled).not.toBe(true);
    act(() => renderer.unmount());
    await act(async () => {
      pending.resolve(archiveStatus());
    });
    expect(mocks.resumeUsageArchive).not.toHaveBeenCalled();
  });

  it('continues from a verified archive to separately confirmed cleanup in place', async () => {
    const renderer = await renderResolvedPage();
    act(() => findButtons(renderer, 'Archive, then clean up')[0].props.onClick());
    act(() => findCreateButtons(renderer)[0].props.onClick());
    await act(async () => {
      getDrawerConfirmation(renderer).onConfirm();
    });
    expect(getText(renderer.root)).toContain('Archive verified. Review cleanup next.');
    expect(mocks.deleteUsageArchive).not.toHaveBeenCalled();
    expect(mocks.navigate.mock.calls.some(([to]) => to.search.includes('run=created-run'))).toBe(
      true
    );
    act(() => findButtons(renderer, 'Continue to cleanup confirmation')[0].props.onClick());
    expect(mocks.deleteUsageArchive).not.toHaveBeenCalled();
    await act(async () => {
      getDrawerConfirmation(renderer).onConfirm();
    });
    expect(mocks.deleteUsageArchive).toHaveBeenCalledTimes(1);
    expect(getText(renderer.root)).toContain('Online detail cleanup complete');
    expect(findButtons(renderer, 'Review cleanup')).toHaveLength(0);
  });
});
