const memoryStorage = new Map<string, string>();
Object.defineProperty(globalThis, 'localStorage', {
  configurable: true,
  value: {
    getItem: (key: string) => memoryStorage.get(key) ?? null,
    setItem: (key: string, value: string) => {
      memoryStorage.set(key, String(value));
    },
    removeItem: (key: string) => {
      memoryStorage.delete(key);
    },
  },
});
if (typeof globalThis.window === 'undefined') {
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: globalThis,
  });
}

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

import axios, { AxiosError, type AxiosResponse } from 'axios';
import type { ReactNode } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import enLocale from '@/i18n/locales/en.json';
import zhCNLocale from '@/i18n/locales/zh-CN.json';
import {
  getUsageServiceErrorCode,
  usageServiceApi,
  type UsageArchiveRunSummary,
  type UsageMaintenanceStatus,
} from '@/services/api/usageService';
import { useAuthStore, useNotificationStore } from '@/stores';
import { UsageMaintenancePage } from './UsageMaintenancePage';

vi.mock('@/components/ui/Modal', () => ({
  Modal: ({
    children,
    footer,
    open,
  }: {
    children?: ReactNode;
    footer?: ReactNode;
    open: boolean;
  }) =>
    open ? (
      <div role="dialog">
        {children}
        {footer}
      </div>
    ) : null,
}));

vi.mock('@/components/ui/Drawer', () => ({
  Drawer: ({
    children,
    footer,
    open,
    title,
  }: {
    children?: ReactNode;
    footer?: ReactNode;
    open: boolean;
    title?: ReactNode;
  }) =>
    open ? (
      <div role="complementary">
        {title ? <div>{title}</div> : null}
        {children}
        {footer}
      </div>
    ) : null,
}));

const availability = vi.hoisted(() => ({
  checking: false,
  managerServiceAvailable: true,
  managerServiceBase: 'https://manager.test',
  hostedPluginMode: false,
  pluginInstalled: false,
  pluginEnabled: false,
  pluginRunning: false,
  pluginHealthy: false,
  pluginInstalledVersion: '',
  pluginLatestVersion: '',
  pluginUpdateAvailable: false,
  pluginStatusReason: '',
  pluginServiceName: '',
}));

vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => availability,
}));

function buildMaintenanceStatus(
  overrides: Partial<UsageMaintenanceStatus> = {}
): UsageMaintenanceStatus {
  return {
    raw_event_count: 1200,
    raw_min_timestamp_ms: Date.now() - 45 * 86_400_000,
    raw_max_timestamp_ms: Date.now(),
    raw_archived_event_count: 200,
    raw_deleted_event_count: 100,
    readiness: {
      migration_ready: true,
      hourly_aggregate_ready: true,
      archive_delete_enabled: true,
    },
    migration: {
      name: 'model_pricing',
      status: 'completed',
      last_event_id: 1200,
      target_event_id: 1200,
      processed_rows: 1200,
      changed_rows: 0,
      updated_at_ms: Date.now() - 60_000,
    },
    hourly_aggregate: {
      name: 'hourly_usage',
      schema_version: 2,
      status: 'ready',
      coverage_event_id: 1200,
      target_event_id: 1200,
      updated_at_ms: Date.now() - 60_000,
    },
    migration_coverage: {
      status: 'completed',
      watermark_event_id: 1200,
      target_event_id: 1200,
      complete: true,
    },
    hourly_aggregate_coverage: {
      status: 'ready',
      watermark_event_id: 1200,
      target_event_id: 1200,
      complete: true,
    },
    storage: {
      page_size: 4096,
      page_count: 1024,
      freelist_count: 128,
      reclaimable_bytes: 524288,
      database_bytes: 4194304,
      wal_bytes: 65536,
      shm_bytes: 32768,
      total_bytes: 4292608,
    },
    compact_requires_stopped_server: true,
    ...overrides,
  };
}

function buildRun(overrides: Partial<UsageArchiveRunSummary> = {}): UsageArchiveRunSummary {
  return {
    id: 'f1015a1b000000000000000000000001',
    mode: 'manual',
    status: 'archived',
    cutoff_timestamp_ms: Date.now() - 30 * 86_400_000,
    target_event_id: 500,
    event_count: 200,
    estimated_bytes: 4096,
    last_archived_event_id: 500,
    archived_event_count: 200,
    archived_uncompressed_bytes: 4096,
    archived_compressed_bytes: 1024,
    last_deleted_event_id: 0,
    deleted_event_count: 0,
    created_at_ms: Date.now() - 10_000,
    updated_at_ms: Date.now() - 5_000,
    has_error: false,
    ...overrides,
  };
}

async function flushPromises() {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

function extractText(node: unknown): string {
  if (typeof node === 'string' || typeof node === 'number') return String(node);
  if (Array.isArray(node)) return node.map(extractText).join(' ');
  if (node && typeof node === 'object' && 'children' in node) {
    return extractText((node as { children?: unknown }).children);
  }
  return '';
}

describe('UsageMaintenancePage', () => {
  let renderer: ReactTestRenderer | undefined;
  beforeEach(async () => {
    await i18n.changeLanguage('en');
    availability.checking = false;
    availability.managerServiceAvailable = true;
    availability.managerServiceBase = 'https://manager.test';
    useAuthStore.setState({ apiBase: 'https://cpa.test', managementKey: 'admin-secret' });
    useNotificationStore.setState({ notifications: [] });
  });

  afterEach(async () => {
    vi.restoreAllMocks();
    if (renderer) {
      await act(async () => {
        renderer?.unmount();
      });
      renderer = undefined;
    }
  });

  it('renders an unsupported empty state linking to /system when manager service is unavailable', async () => {
    availability.managerServiceAvailable = false;
    availability.managerServiceBase = '';

    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/usage-maintenance']}>
          <UsageMaintenancePage />
        </MemoryRouter>
      );
    });

    expect(
      renderer!.root.findByProps({ 'data-testid': 'usage-maintenance-unsupported' })
    ).toBeDefined();
    const sysLink = renderer!.root.findByType('a');
    expect(sysLink.props.href).toBe('/system');
  });

  it('opens Create Archive Drawer, runs auto-preview + recent warning + guided creation, and completes deletion with acknowledgement checkbox', async () => {
    const runPreviewed = buildRun({ id: 'run-1', status: 'previewed', archived_event_count: 0 });
    const runArchived = buildRun({ id: 'run-1', status: 'archived', started_at_ms: Date.now() - 8000, archived_at_ms: Date.now() - 6000 });
    const runVerified = buildRun({ ...runArchived, status: 'verified', verified_at_ms: Date.now() - 4000 });
    const runCompleted = buildRun({
      ...runVerified,
      status: 'completed',
      deleted_event_count: 200,
      delete_started_at_ms: Date.now() - 2000,
      completed_at_ms: Date.now() - 1000,
    });

    vi.spyOn(usageServiceApi, 'getMaintenanceStatus').mockResolvedValue(buildMaintenanceStatus());
    const listSpy = vi
      .spyOn(usageServiceApi, 'listArchives')
      .mockResolvedValueOnce({ runs: [], total: 0, status_counts: {} })
      .mockResolvedValueOnce({ runs: [runVerified], total: 1, status_counts: { verified: 1 } })
      .mockResolvedValue({ runs: [runCompleted], total: 1, status_counts: { completed: 1 } });
    vi.spyOn(usageServiceApi, 'getStatus').mockResolvedValue({ service: 'usage' });
    const previewSpy = vi.spyOn(usageServiceApi, 'previewArchive').mockResolvedValue({
      cutoff_timestamp_ms: runArchived.cutoff_timestamp_ms,
      target_event_id: 500,
      event_count: 200,
      estimated_bytes: 4096,
      min_timestamp_ms: Date.now() - 45 * 86_400_000,
      max_timestamp_ms: runArchived.cutoff_timestamp_ms,
    });
    const createSpy = vi.spyOn(usageServiceApi, 'createArchive').mockResolvedValue({
      run: runPreviewed,
      segments: [],
    });
    const resumeSpy = vi.spyOn(usageServiceApi, 'resumeArchive').mockResolvedValue({
      run: runArchived,
      segments: [],
    });
    const verifySpy = vi.spyOn(usageServiceApi, 'verifyArchive').mockResolvedValue({
      run: runVerified,
      segments: [],
    });
    vi.spyOn(usageServiceApi, 'getArchiveStatus').mockResolvedValue({
      run: runVerified,
      segments: [
        {
          run_id: 'run-1',
          sequence: 1,
          status: 'verified',
          first_event_id: 1,
          last_event_id: 500,
          min_timestamp_ms: 100,
          max_timestamp_ms: 200,
          event_count: 200,
          uncompressed_bytes: 4096,
          compressed_bytes: 1024,
          created_at_ms: 300,
        },
      ],
    });
    const deleteSpy = vi.spyOn(usageServiceApi, 'deleteArchive').mockResolvedValue({
      run: runCompleted,
      segments: [],
    });

    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/usage-maintenance']}>
          <UsageMaintenancePage />
        </MemoryRouter>
      );
      await flushPromises();
    });

    expect(listSpy).toHaveBeenCalledTimes(1);

    // Open Create Drawer -> auto preview fires
    await act(async () => {
      renderer!.root
        .findByProps({ 'data-testid': 'usage-maintenance-open-create-btn' })
        .props.onClick();
      await flushPromises();
    });
    expect(
      renderer!.root.findByProps({ 'data-testid': 'usage-maintenance-create-drawer' })
    ).toBeDefined();
    expect(previewSpy).toHaveBeenCalled();
    expect(
      renderer!.root.findByProps({ 'data-testid': 'usage-maintenance-preview-result' })
    ).toBeDefined();

    // Switch intent to cleanup + 7 days -> 14d high-risk warning renders
    await act(async () => {
      renderer!.root.findByProps({ 'data-testid': 'usage-maintenance-intent-cleanup' }).props.onClick();
      renderer!.root.findByProps({ id: 'usage-maintenance-retention-tabs-7' }).props.onClick();
      await flushPromises();
    });
    const createDrawerText = extractText(
      renderer!.root.findByProps({ 'data-testid': 'usage-maintenance-create-drawer' })
    );
    expect(createDrawerText).toContain(enLocale.usage_maintenance.cleanup_recent_warning_title);

    // Step 1 -> Step 2 confirmation
    await act(async () => {
      renderer!.root.findByProps({ 'data-testid': 'usage-maintenance-create-btn' }).props.onClick();
    });
    expect(
      renderer!.root.findByProps({ 'data-testid': 'usage-maintenance-create-confirm' })
    ).toBeDefined();

    // Confirm guided creation (create -> resume archiving -> verify)
    await act(async () => {
      await renderer!.root
        .findByProps({ 'data-testid': 'usage-maintenance-create-confirm-btn' })
        .props.onClick();
      await flushPromises();
    });
    expect(createSpy).toHaveBeenCalledTimes(1);
    expect(resumeSpy).toHaveBeenCalledWith(
      'https://manager.test',
      'admin-secret',
      'run-1',
      true,
      expect.any(AbortSignal),
      'archiving'
    );
    expect(verifySpy).toHaveBeenCalledWith(
      'https://manager.test',
      'admin-secret',
      'run-1',
      true,
      expect.any(AbortSignal)
    );
    expect(
      renderer!.root.findByProps({ 'data-testid': 'usage-maintenance-detail-drawer' })
    ).toBeDefined();

    // Trigger Delete from the row -> opens structured Delete Confirmation inside Drawer
    const updatedRow = renderer!.root.findByProps({ 'data-testid': 'usage-maintenance-run-row-run-1' });
    const deleteBtn = updatedRow
      .findAllByType('button')
      .find((btn) =>
        btn.findAll((node) => node.children?.includes(enLocale.usage_maintenance.guided_step_delete))
          .length > 0
      )!;
    await act(async () => {
      deleteBtn.props.onClick();
    });

    expect(
      renderer!.root.findByProps({ 'data-testid': 'usage-maintenance-delete-confirmation' })
    ).toBeDefined();
    const confirmDeleteBtn = renderer!.root.findByProps({
      'data-testid': 'usage-maintenance-confirm-delete-btn',
    });
    expect(confirmDeleteBtn.props.disabled).toBe(true);

    // Check mandatory acknowledgement checkbox
    const ackCheckbox = renderer!.root
      .findByProps({ 'data-testid': 'usage-maintenance-delete-confirmation' })
      .findByType('input');
    await act(async () => {
      ackCheckbox.props.onChange({ target: { checked: true } });
    });

    await act(async () => {
      await renderer!.root
        .findByProps({ 'data-testid': 'usage-maintenance-confirm-delete-btn' })
        .props.onClick();
      await flushPromises();
    });
    expect(deleteSpy).toHaveBeenCalledWith('https://manager.test', 'admin-secret', 'run-1', true);
    expect(
      renderer!.root.findByProps({ 'data-testid': 'usage-post-delete-notice' })
    ).toBeDefined();
  });

  it('supports search, status_counts, cursor pagination, cancel confirmation, transfer sessions, and diagnostics coverage/lock views', async () => {
    await i18n.changeLanguage('zh-CN');
    const previewedRun = buildRun({
      id: 'aaaa1111000000000000000000000001',
      status: 'previewed',
      archived_event_count: 0,
    });
    const completedRun = buildRun({
      id: 'bbbb2222000000000000000000000002',
      status: 'completed',
      deleted_event_count: 200,
    });

    vi.spyOn(usageServiceApi, 'getMaintenanceStatus').mockResolvedValue(
      buildMaintenanceStatus({
        active_run: previewedRun,
        active_lock: {
          run_id: previewedRun.id,
          operation: 'archive',
          acquired_at_ms: Date.now() - 5000,
          updated_at_ms: Date.now() - 1000,
        },
      })
    );
    const listSpy = vi.spyOn(usageServiceApi, 'listArchives').mockResolvedValue({
      runs: [previewedRun, completedRun],
      total: 3,
      status_counts: { previewed: 1, completed: 2 },
      next_cursor: 'cursor-page-2',
    });
    vi.spyOn(usageServiceApi, 'getStatus').mockResolvedValue({ service: 'usage' });
    const cancelSpy = vi.spyOn(usageServiceApi, 'cancelArchive').mockResolvedValue({
      run: { ...previewedRun, status: 'cancelled' },
      segments: [],
    });
    const listSessionsSpy = vi
      .spyOn(usageServiceApi, 'listUsageImportSessions')
      .mockResolvedValue({
        sessions: [
          {
            id: 'import-sess-1',
            filename: 'usage-2026.jsonl',
            status: 'completed',
            size_bytes: 2048,
            received_bytes: 2048,
            chunk_size_bytes: 1024,
            created_at_ms: Date.now() - 60_000,
            updated_at_ms: Date.now() - 10_000,
            expires_at_ms: Date.now() + 86_400_000,
            result: { added: 10, skipped: 2, total: 12, failed: 0 },
          },
        ],
        total: 1,
        active_sessions: 0,
        max_sessions: 4,
        chunk_size_bytes: 4 * 1024 * 1024,
        disk_quota_bytes: 512 * 1024 * 1024,
        ttl_seconds: 86_400,
      });

    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/usage-maintenance']}>
          <UsageMaintenancePage />
        </MemoryRouter>
      );
      await flushPromises();
    });

    // Search filters visible rows locally
    const searchInput = renderer!.root.findByProps({
      'data-testid': 'usage-maintenance-search-input',
    });
    await act(async () => {
      searchInput.props.onChange({ target: { value: 'bbbb2222' } });
    });
    expect(
      renderer!.root.findAllByProps({
        'data-testid': `usage-maintenance-run-row-${previewedRun.id}`,
      })
    ).toHaveLength(0);
    expect(
      renderer!.root.findByProps({
        'data-testid': `usage-maintenance-run-row-${completedRun.id}`,
      })
    ).toBeDefined();

    await act(async () => {
      searchInput.props.onChange({ target: { value: '' } });
    });

    // Cancel confirmation modal on previewed run
    const previewRow = renderer!.root.findByProps({
      'data-testid': `usage-maintenance-run-row-${previewedRun.id}`,
    });
    const cancelBtn = previewRow
      .findAllByType('button')
      .find((btn) => btn.findAll((node) => node.children?.includes(zhCNLocale.common.cancel)).length > 0)!;
    await act(async () => {
      cancelBtn.props.onClick();
    });
    await act(async () => {
      await renderer!.root
        .findByProps({ 'data-testid': 'usage-maintenance-confirm-cancel-btn' })
        .props.onClick();
      await flushPromises();
    });
    expect(cancelSpy).toHaveBeenCalledWith('https://manager.test', 'admin-secret', previewedRun.id);

    // Floating progress pill shown for active run when drawer is closed
    expect(
      renderer!.root.findByProps({ 'data-testid': 'usage-maintenance-floating-progress' })
    ).toBeDefined();

    // Next cursor page
    const nextBtn = renderer!.root
      .findAllByType('button')
      .find((btn) =>
        btn.findAll((node) => node.children?.includes(zhCNLocale.usage_maintenance.pagination_next))
          .length > 0
      )!;
    await act(async () => {
      nextBtn.props.onClick();
      await flushPromises();
    });
    expect(listSpy).toHaveBeenLastCalledWith('https://manager.test', 'admin-secret', {
      limit: 20,
      status: undefined,
      mode: undefined,
      cursor: 'cursor-page-2',
    });

    // Transfer tab: loads import sessions & renders localized export subtitle
    await act(async () => {
      renderer!.root.findByProps({ id: 'usage-maintenance-tabs-transfer' }).props.onClick();
      await flushPromises();
    });
    expect(listSessionsSpy).toHaveBeenCalled();
    expect(
      renderer!.root.findByProps({ 'data-testid': 'usage-import-session-row-import-sess-1' })
    ).toBeDefined();
    const transferText = extractText(
      renderer!.root.findByProps({ 'data-testid': 'usage-maintenance-transfer-tab' })
    );
    expect(transferText).toContain(zhCNLocale.usage_maintenance.transfer_export_note);
    expect(transferText).not.toContain('Download a portable JSON snapshot');

    // Diagnostics tab: renders lock card + open active run button
    await act(async () => {
      renderer!.root.findByProps({ id: 'usage-maintenance-tabs-diagnostics' }).props.onClick();
    });
    expect(
      renderer!.root.findByProps({ 'data-testid': 'usage-maintenance-lock-card' })
    ).toBeDefined();
    expect(
      renderer!.root.findByProps({ 'data-testid': 'usage-maintenance-open-active-run-btn' })
    ).toBeDefined();
  });

  it('maps real Axios usage_archive_* HTTP errors via usage_service_errors', async () => {
    vi.spyOn(usageServiceApi, 'getMaintenanceStatus').mockResolvedValue(buildMaintenanceStatus());
    vi.spyOn(usageServiceApi, 'listArchives').mockResolvedValue({
      runs: [buildRun({ id: 'run-err', status: 'archived' })],
      total: 1,
    });
    vi.spyOn(usageServiceApi, 'getStatus').mockResolvedValue({ service: 'usage' });

    const makeArchiveAxiosError = (status: number, code: string) =>
      new AxiosError(
        `Request failed with status code ${status}`,
        'ERR_BAD_REQUEST',
        undefined,
        undefined,
        {
          status,
          data: { code, error: `backend ${code}` },
        } as AxiosResponse
      );

    const noEventsErr = makeArchiveAxiosError(422, 'usage_archive_no_events');
    const notFoundErr = makeArchiveAxiosError(404, 'usage_archive_not_found');
    expect(getUsageServiceErrorCode(noEventsErr)).toBe('usage_archive_no_events');
    expect(getUsageServiceErrorCode(notFoundErr)).toBe('usage_archive_not_found');

    const postSpy = vi
      .spyOn(axios, 'post')
      .mockRejectedValueOnce(noEventsErr)
      .mockRejectedValueOnce(notFoundErr);

    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/usage-maintenance']}>
          <UsageMaintenancePage />
        </MemoryRouter>
      );
      await flushPromises();
    });

    await act(async () => {
      renderer!.root
        .findByProps({ 'data-testid': 'usage-maintenance-open-create-btn' })
        .props.onClick();
      await flushPromises();
    });
    let notifications = useNotificationStore.getState().notifications;
    expect(notifications[notifications.length - 1]?.message).toBe(
      enLocale.usage_service_errors.usage_archive_no_events
    );

    const row = renderer!.root.findByProps({ 'data-testid': 'usage-maintenance-run-row-run-err' });
    const verifyBtn = row
      .findAllByType('button')
      .find(
        (btn) =>
          btn.findAll((node) => node.children?.includes(enLocale.usage_maintenance.guided_step_verify))
            .length > 0
      )!;
    await act(async () => {
      await verifyBtn.props.onClick();
    });
    notifications = useNotificationStore.getState().notifications;
    expect(notifications[notifications.length - 1]?.message).toBe(
      enLocale.usage_service_errors.usage_archive_not_found
    );
    expect(postSpy).toHaveBeenCalledTimes(2);
  });
});
