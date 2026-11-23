import axios, { AxiosError, type AxiosResponse } from 'axios';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { MemoryRouter } from 'react-router-dom';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../i18n';
import { FreshSystemWorkspace } from './FreshSystemWorkspace';
import { createPublicApiClient } from './publicApi';
import { externalWorkspaceText as x } from '../i18n/freshExternal';
import { configurationText as c } from '../i18n/configurationWorkspace';
import {
  getUsageServiceErrorCode,
  usageServiceApi,
  type ManagerConfigResponse,
} from '../services/api/usageService';
import enLocale from '../i18n/locales/en.json';
import { useNotificationStore } from '../stores';

vi.mock('../components/ui/Modal', () => ({
  Modal: ({ children, footer, open }: { children: ReactNode; footer: ReactNode; open: boolean }) =>
    open ? (
      <div role="dialog">
        {children}
        {footer}
      </div>
    ) : null,
}));

const monitoring = vi.hoisted(() => ({
  enabled: true,
  loaded: true,
  saving: false,
  failed: false,
  writable: true,
  setEnabled: vi.fn(async () => undefined),
}));

vi.mock('../hooks/useRequestMonitoringSwitch', () => ({
  useRequestMonitoringSwitch: () => monitoring,
}));

const availability = vi.hoisted(() => ({
  managerServiceAvailable: false,
  managerServiceBase: '',
}));

vi.mock('../hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => availability,
}));

describe('FreshSystemWorkspace component', () => {
  let renderer: ReactTestRenderer;

  beforeEach(async () => {
    await i18n.changeLanguage('zh-CN');
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    monitoring.enabled = true;
    monitoring.loaded = true;
    monitoring.saving = false;
    monitoring.failed = false;
    monitoring.writable = true;
    monitoring.setEnabled.mockClear();
    availability.managerServiceAvailable = false;
    availability.managerServiceBase = '';
    useNotificationStore.getState().clearAll();
    vi.restoreAllMocks();
  });

  afterEach(async () => {
    if (renderer) await act(async () => renderer.unmount());
  });

  async function mount(basePath = '/console') {
    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/console/system']}>
          <FreshSystemWorkspace basePath={basePath} />
        </MemoryRouter>
      );
    });
  }

  it('renders system workspace cards with base path and security status', async () => {
    await mount('/custom-console');

    // Cards and connection info
    expect(renderer.root.findByProps({ 'data-testid': 'fresh-system-workspace' })).toBeDefined();
    expect(renderer.root.findByProps({ 'data-testid': 'system-strategy-badge' })).toBeDefined();
    expect(renderer.root.findByProps({ 'data-testid': 'system-entry-card' })).toBeDefined();
    expect(renderer.root.findByProps({ 'data-testid': 'system-cpa-base-url' })).toBeDefined();
    expect(renderer.root.findByProps({ 'data-testid': 'system-security-card' })).toBeDefined();

    // Entry path
    const pathCode = renderer.root.findByProps({
      'data-testid': 'system-entry-path',
    });
    expect(pathCode.findByType('code').props.children).toBe('/custom-console');
  });

  it('opens and closes the offline recovery modal', async () => {
    await mount();

    const viewModalBtn = renderer.root.findByProps({
      'data-testid': 'view-offline-recovery-btn',
    });
    await act(async () => viewModalBtn.props.onClick());

    const recoveryModal = renderer.root.findByProps({
      'data-testid': 'recovery-modal',
    });
    expect(recoveryModal).toBeDefined();

    // Close modal
    expect(JSON.stringify(renderer.toJSON())).toContain('cpa-manager-plus reset-admin-key');
    // The v2 docs live in cpamp-hq/docs: the guide follows the UI language.
    expect(recoveryModal.findByType('a').props.href).toMatch(
      /^https:\/\/cpamp-docs\.seakee\.dev\/next\/(?:en\/)?operations\/reset-admin-key\.html$/
    );
    const closeBtn = renderer.root.findByProps({ 'data-testid': 'close-recovery-btn' });
    await act(async () => closeBtn.props.onClick());

    expect(renderer.root.findAllByProps({ 'data-testid': 'recovery-modal' })).toHaveLength(0);
  });

  it('switches request monitoring through the shared write path', async () => {
    await mount();

    const state = renderer.root.findByProps({ 'data-testid': 'system-request-monitoring-state' });
    expect(state.children.join('')).toBe(c('requestMonitoringOn'));

    const switchNode = renderer.root.findByProps({
      'data-testid': 'system-request-monitoring-switch',
    });
    const input = switchNode.findByType('input');
    expect(input.props.checked).toBe(true);
    await act(async () => input.props.onChange({ target: { checked: false } }));
    expect(monitoring.setEnabled).toHaveBeenCalledWith(false);
  });

  it('keeps the switch reachable when monitoring is off and reports a failed write', async () => {
    monitoring.enabled = false;
    monitoring.failed = true;
    await mount();

    const state = renderer.root.findByProps({ 'data-testid': 'system-request-monitoring-state' });
    expect(state.children.join('')).toBe(c('requestMonitoringOff'));
    const switchNode = renderer.root.findByProps({
      'data-testid': 'system-request-monitoring-switch',
    });
    expect(switchNode.findByType('input').props.checked).toBe(false);
    expect(
      renderer.root.findByProps({ 'data-testid': 'system-request-monitoring-error' })
    ).toBeDefined();
  });

  it('explains why request monitoring cannot be switched without a Manager service', async () => {
    monitoring.writable = false;
    await mount();

    const switchNode = renderer.root.findByProps({
      'data-testid': 'system-request-monitoring-switch',
    });
    expect(switchNode.findAllByType('input')).toHaveLength(0);
    expect(JSON.stringify(renderer.toJSON())).toContain(c('requestMonitoringUnavailable'));
  });

  it('does not invent a host service health result', async () => {
    await mount();
    expect(JSON.stringify(renderer.toJSON())).not.toContain('正常运行');
    expect(renderer.root.findAllByProps({ 'data-testid': 'system-startup-card' })).toHaveLength(0);
  });

  it.each(['en', 'zh-TW', 'ru'])(
    'translates system and recovery content in %s',
    async (language) => {
      await i18n.changeLanguage(language);
      await mount();
      await act(async () =>
        renderer.root.findByProps({ 'data-testid': 'view-offline-recovery-btn' }).props.onClick()
      );
      expect(JSON.stringify(renderer.toJSON())).not.toMatch(/系统配置|管理员|恢复说明/);
      expect(JSON.stringify(renderer.toJSON())).toContain('cpa-manager-plus reset-admin-key');
    }
  );

  it('switches between the two configuration tabs and keeps external rebind accessible', async () => {
    const fakeClient = {
      get: vi.fn(),
      post: vi.fn(),
      put: vi.fn(),
    };
    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/console/system']}>
          <FreshSystemWorkspace
            basePath="/console"
            strategy="connect_external"
            client={fakeClient as never}
            credential="admin-secret"
            onUnauthorized={vi.fn()}
          />
        </MemoryRouter>
      );
    });

    expect(renderer.root.findByProps({ 'data-testid': 'system-rebind-btn' })).toBeDefined();
    const monitoringPanel = renderer.root.findByProps({
      id: 'system-config-tabs-panel-monitoring',
    });
    const storagePanel = renderer.root.findByProps({
      id: 'system-config-tabs-panel-storage',
    });
    expect(monitoringPanel.props.hidden).toBe(false);
    expect(storagePanel.props.hidden).toBe(true);

    await act(async () => {
      renderer.root.findByProps({ id: 'system-config-tabs-storage' }).props.onClick();
    });
    expect(monitoringPanel.props.hidden).toBe(true);
    expect(storagePanel.props.hidden).toBe(false);
    expect(
      renderer.root.findByProps({ 'data-testid': 'system-clear-login-btn' })
    ).toBeDefined();
  });

  function buildManagerConfigResponse(
    overrides: Partial<ManagerConfigResponse['config']['collector']> = {}
  ): ManagerConfigResponse {
    return {
      source: 'db',
      config: {
        cpaConnection: { cpaBaseUrl: 'https://cpa.example', managementKeyConfigured: true },
        collector: {
          collectorMode: 'auto',
          queue: 'usage',
          popSide: 'right',
          batchSize: 100,
          pollIntervalMs: 500,
          queryLimit: 50000,
          tlsSkipVerify: false,
          ...overrides,
        },
        externalUsageService: { enabled: false, serviceBase: '' },
        retention: { enabled: true, retentionDays: 30, envLocked: false },
      },
    };
  }

  it('requires non-empty queue in resp/auto modes and uses usage as default queue', async () => {
    availability.managerServiceAvailable = true;
    availability.managerServiceBase = 'https://manager.test';
    vi.spyOn(usageServiceApi, 'getManagerConfig').mockResolvedValue(
      buildManagerConfigResponse({ collectorMode: 'resp', queue: '' })
    );
    vi.spyOn(usageServiceApi, 'getMaintenanceStatus').mockResolvedValue({
      raw_event_count: 0,
      raw_min_timestamp_ms: 0,
      raw_max_timestamp_ms: 0,
      raw_archived_event_count: 0,
      raw_deleted_event_count: 0,
      readiness: {
        migration_ready: true,
        hourly_aggregate_ready: true,
        archive_delete_enabled: true,
      },
      compact_requires_stopped_server: false,
    });
    vi.spyOn(usageServiceApi, 'listArchives').mockResolvedValue({ runs: [], total: 0 });
    const patchSpy = vi.spyOn(usageServiceApi, 'patchSettings');

    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/console/system']}>
          <FreshSystemWorkspace
            basePath="/console"
            credential="admin-secret"
            onUnauthorized={vi.fn()}
          />
        </MemoryRouter>
      );
    });
    await act(async () => {
      await Promise.resolve();
    });

    const queueInput = renderer.root.findByProps({ placeholder: 'usage' });
    expect(queueInput.props.value).toBe('usage');

    await act(async () => {
      queueInput.props.onChange({ target: { value: '   ' } });
    });
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'system-collector-save-btn' }).props.onClick();
    });
    expect(patchSpy).not.toHaveBeenCalled();
    expect(JSON.stringify(renderer.toJSON())).toContain(c('collectorQueueRequired'));
  });

  it('distinguishes local retention validation from backend save failures', async () => {
    availability.managerServiceAvailable = true;
    availability.managerServiceBase = 'https://manager.test';
    vi.spyOn(usageServiceApi, 'getManagerConfig').mockResolvedValue(buildManagerConfigResponse());
    vi.spyOn(usageServiceApi, 'getMaintenanceStatus').mockResolvedValue({
      raw_event_count: 0,
      raw_min_timestamp_ms: 0,
      raw_max_timestamp_ms: 0,
      raw_archived_event_count: 0,
      raw_deleted_event_count: 0,
      readiness: {
        migration_ready: true,
        hourly_aggregate_ready: true,
        archive_delete_enabled: true,
      },
      compact_requires_stopped_server: false,
    });
    vi.spyOn(usageServiceApi, 'listArchives').mockResolvedValue({ runs: [], total: 0 });
    const patchSpy = vi
      .spyOn(usageServiceApi, 'patchSettings')
      .mockRejectedValue(Object.assign(new Error('server error'), { status: 500 }));

    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/console/system']}>
          <FreshSystemWorkspace
            basePath="/console"
            credential="admin-secret"
            onUnauthorized={vi.fn()}
          />
        </MemoryRouter>
      );
    });
    await act(async () => {
      await Promise.resolve();
    });

    await act(async () => {
      renderer.root.findByProps({ id: 'system-config-tabs-storage' }).props.onClick();
    });
    const daysInput = renderer.root.findByProps({ id: 'system-retention-days-input' });

    await act(async () => {
      daysInput.props.onChange({ target: { value: '0' } });
    });
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'system-retention-save-btn' }).props.onClick();
    });
    expect(patchSpy).not.toHaveBeenCalled();
    expect(JSON.stringify(renderer.toJSON())).toContain(c('retentionDaysInvalid'));

    await act(async () => {
      daysInput.props.onChange({ target: { value: '45' } });
    });
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'system-retention-save-btn' })
        .props.onClick();
    });
    expect(patchSpy).toHaveBeenCalledTimes(1);
    const json = JSON.stringify(renderer.toJSON());
    expect(json).toContain(c('retentionSaveFailed'));
    expect(json).not.toContain(c('retentionDaysInvalid'));
  });

  it('invokes onUnauthorized when usageServiceApi returns 401 or 403', async () => {
    availability.managerServiceAvailable = true;
    availability.managerServiceBase = 'https://manager.test';
    const onUnauthorized = vi.fn();
    vi.spyOn(usageServiceApi, 'getManagerConfig').mockResolvedValue(buildManagerConfigResponse());
    vi.spyOn(usageServiceApi, 'getMaintenanceStatus').mockResolvedValue({
      raw_event_count: 0,
      raw_min_timestamp_ms: 0,
      raw_max_timestamp_ms: 0,
      raw_archived_event_count: 0,
      raw_deleted_event_count: 0,
      readiness: {
        migration_ready: true,
        hourly_aggregate_ready: true,
        archive_delete_enabled: true,
      },
      compact_requires_stopped_server: false,
    });
    vi.spyOn(usageServiceApi, 'listArchives').mockResolvedValue({ runs: [], total: 0 });
    vi.spyOn(usageServiceApi, 'patchSettings').mockRejectedValue(
      Object.assign(new Error('forbidden'), { status: 403 })
    );

    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/console/system']}>
          <FreshSystemWorkspace
            basePath="/console"
            credential="admin-secret"
            onUnauthorized={onUnauthorized}
          />
        </MemoryRouter>
      );
    });
    await act(async () => {
      await Promise.resolve();
    });

    expect(
      renderer.root.findByProps({ 'data-testid': 'system-collector-save-btn' }).props.disabled
    ).toBe(true);
    const pollInput = renderer.root.findByProps({ label: c('collectorPollInterval') });
    await act(async () => {
      pollInput.props.onChange({ target: { value: '600' } });
    });
    expect(
      renderer.root.findByProps({ 'data-testid': 'system-collector-save-btn' }).props.disabled
    ).toBe(false);
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'system-collector-save-btn' })
        .props.onClick();
    });
    expect(onUnauthorized).toHaveBeenCalledTimes(1);

    vi.spyOn(usageServiceApi, 'getManagerConfig').mockRejectedValueOnce(
      Object.assign(new Error('unauthorized'), { status: 401 })
    );
    await act(async () => renderer.unmount());
    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/console/system']}>
          <FreshSystemWorkspace
            basePath="/console"
            credential="admin-secret"
            onUnauthorized={onUnauthorized}
          />
        </MemoryRouter>
      );
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(onUnauthorized).toHaveBeenCalledTimes(2);
  });
  it('handles partial and full retention env locks and displays retention_env_managed errors', async () => {
    availability.managerServiceAvailable = true;
    availability.managerServiceBase = 'https://manager.test';
    const maintenancePayload = {
      raw_event_count: 0,
      raw_min_timestamp_ms: 0,
      raw_max_timestamp_ms: 0,
      raw_archived_event_count: 0,
      raw_deleted_event_count: 0,
      readiness: {
        migration_ready: true,
        hourly_aggregate_ready: true,
        archive_delete_enabled: true,
      },
      compact_requires_stopped_server: false,
    };
    vi.spyOn(usageServiceApi, 'getMaintenanceStatus').mockResolvedValue(maintenancePayload);
    vi.spyOn(usageServiceApi, 'listArchives').mockResolvedValue({ runs: [], total: 0 });

    // 1) enabledEnvLocked only: switch disabled, days & save enabled, PATCH omits enabled.
    const enabledLockedConfig = buildManagerConfigResponse();
    enabledLockedConfig.config.retention = {
      enabled: true,
      retentionDays: 30,
      envLocked: false,
      enabledEnvLocked: true,
      retentionDaysEnvLocked: false,
    };
    vi.spyOn(usageServiceApi, 'getManagerConfig').mockResolvedValueOnce(enabledLockedConfig);
    const patchSpy = vi
      .spyOn(usageServiceApi, 'patchSettings')
      .mockResolvedValueOnce(enabledLockedConfig);

    await i18n.changeLanguage('en');
    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/console/system']}>
          <FreshSystemWorkspace
            basePath="/console"
            credential="admin-secret"
            onUnauthorized={vi.fn()}
          />
        </MemoryRouter>
      );
    });
    await act(async () => {
      await Promise.resolve();
    });
    await act(async () => {
      renderer.root.findByProps({ id: 'system-config-tabs-storage' }).props.onClick();
    });

    const toggle = renderer.root.findByProps({ 'aria-label': c('retentionToggle') });
    const daysInput = renderer.root.findByProps({ id: 'system-retention-days-input' });
    const saveBtn = renderer.root.findByProps({ 'data-testid': 'system-retention-save-btn' });

    expect(toggle.props.disabled).toBe(true);
    expect(daysInput.props.disabled).toBe(false);
    expect(saveBtn.props.disabled).toBe(true);
    expect(
      renderer.root.findByProps({ 'data-testid': 'system-retention-env-lock-hint' }).props.children
    ).toBe(c('retentionEnabledEnvLockedHint'));

    await act(async () => {
      daysInput.props.onChange({ target: { value: '60' } });
    });
    expect(
      renderer.root.findByProps({ 'data-testid': 'system-retention-save-btn' }).props.disabled
    ).toBe(false);
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'system-retention-save-btn' })
        .props.onClick();
    });
    expect(patchSpy).toHaveBeenCalledWith(
      'https://manager.test',
      { retention: { retentionDays: 60 } },
      'admin-secret'
    );

    // 2) retentionDaysEnvLocked only: days disabled, switch & save enabled, PATCH omits retentionDays,
    //    and retention_env_managed server 409 displays localized error copy.
    await act(async () => renderer.unmount());
    const daysLockedConfig = buildManagerConfigResponse();
    daysLockedConfig.config.retention = {
      enabled: false,
      retentionDays: 30,
      envLocked: false,
      enabledEnvLocked: false,
      retentionDaysEnvLocked: true,
    };
    vi.spyOn(usageServiceApi, 'getManagerConfig').mockResolvedValueOnce(daysLockedConfig);
    patchSpy
      .mockResolvedValueOnce(daysLockedConfig)
      .mockRejectedValueOnce(
        Object.assign(new Error('retention env managed'), {
          status: 409,
          code: 'retention_env_managed',
        })
      );

    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/console/system']}>
          <FreshSystemWorkspace
            basePath="/console"
            credential="admin-secret"
            onUnauthorized={vi.fn()}
          />
        </MemoryRouter>
      );
    });
    await act(async () => {
      await Promise.resolve();
    });
    await act(async () => {
      renderer.root.findByProps({ id: 'system-config-tabs-storage' }).props.onClick();
    });

    const toggle2 = renderer.root.findByProps({ 'aria-label': c('retentionToggle') });
    const daysInput2 = renderer.root.findByProps({ id: 'system-retention-days-input' });
    const saveBtn2 = renderer.root.findByProps({ 'data-testid': 'system-retention-save-btn' });

    expect(toggle2.props.disabled).toBe(false);
    expect(daysInput2.props.disabled).toBe(true);
    expect(saveBtn2.props.disabled).toBe(true);
    expect(
      renderer.root.findByProps({ 'data-testid': 'system-retention-env-lock-hint' }).props.children
    ).toBe(c('retentionDaysEnvLockedHint'));

    await act(async () => {
      toggle2.props.onChange({ target: { checked: true } });
    });
    expect(
      renderer.root.findByProps({ 'data-testid': 'system-retention-save-btn' }).props.disabled
    ).toBe(false);
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'system-retention-save-btn' })
        .props.onClick();
    });
    expect(patchSpy).toHaveBeenLastCalledWith(
      'https://manager.test',
      { retention: { enabled: true } },
      'admin-secret'
    );

    await act(async () => {
      renderer.root
        .findByProps({ 'aria-label': c('retentionToggle') })
        .props.onChange({ target: { checked: true } });
    });
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'system-retention-save-btn' })
        .props.onClick();
    });
    expect(JSON.stringify(renderer.toJSON())).toContain(
      enLocale.usage_service_errors.retention_env_managed
    );

    // 3) Both enabledEnvLocked and retentionDaysEnvLocked: switch, days, and Save all disabled.
    await act(async () => renderer.unmount());
    const bothLockedConfig = buildManagerConfigResponse();
    bothLockedConfig.config.retention = {
      enabled: true,
      retentionDays: 30,
      envLocked: false,
      enabledEnvLocked: true,
      retentionDaysEnvLocked: true,
    };
    vi.spyOn(usageServiceApi, 'getManagerConfig').mockResolvedValueOnce(bothLockedConfig);

    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/console/system']}>
          <FreshSystemWorkspace
            basePath="/console"
            credential="admin-secret"
            onUnauthorized={vi.fn()}
          />
        </MemoryRouter>
      );
    });
    await act(async () => {
      await Promise.resolve();
    });
    await act(async () => {
      renderer.root.findByProps({ id: 'system-config-tabs-storage' }).props.onClick();
    });
    expect(
      renderer.root.findByProps({ 'aria-label': c('retentionToggle') }).props.disabled
    ).toBe(true);
    expect(
      renderer.root.findByProps({ id: 'system-retention-days-input' }).props.disabled
    ).toBe(true);
    expect(
      renderer.root.findByProps({ 'data-testid': 'system-retention-save-btn' }).props.disabled
    ).toBe(true);
  });

  it('renders queue and tlsSkipVerify without popSide in subscribe mode and never validates hidden fields in http mode', async () => {
    availability.managerServiceAvailable = true;
    availability.managerServiceBase = 'https://manager.test';
    const subResponse = buildManagerConfigResponse({
      collectorMode: 'subscribe',
      queue: 'usage-events',
      tlsSkipVerify: true,
    });
    const httpResponse = buildManagerConfigResponse({
      collectorMode: 'http',
      queue: '',
      tlsSkipVerify: false,
    });
    vi.spyOn(usageServiceApi, 'getManagerConfig')
      .mockResolvedValueOnce(subResponse)
      .mockResolvedValueOnce(httpResponse);
    vi.spyOn(usageServiceApi, 'getMaintenanceStatus').mockResolvedValue({
      raw_event_count: 0,
      raw_min_timestamp_ms: 0,
      raw_max_timestamp_ms: 0,
      raw_archived_event_count: 0,
      raw_deleted_event_count: 0,
      readiness: {
        migration_ready: true,
        hourly_aggregate_ready: true,
        archive_delete_enabled: true,
      },
      compact_requires_stopped_server: false,
    });
    vi.spyOn(usageServiceApi, 'listArchives').mockResolvedValue({ runs: [], total: 0 });
    const patchSpy = vi
      .spyOn(usageServiceApi, 'patchSettings')
      .mockResolvedValueOnce(subResponse)
      .mockResolvedValueOnce(httpResponse);

    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/console/system']}>
          <FreshSystemWorkspace
            basePath="/console"
            credential="admin-secret"
            onUnauthorized={vi.fn()}
          />
        </MemoryRouter>
      );
    });
    await act(async () => {
      await Promise.resolve();
    });

    const respSubcard = renderer.root.findByProps({
      'data-testid': 'system-collector-resp-fields',
    });
    expect(respSubcard.findByProps({ placeholder: 'usage' }).props.value).toBe('usage-events');
    expect(respSubcard.findAllByProps({ 'aria-label': c('collectorPopSideLabel') })).toHaveLength(
      0
    );
    expect(
      respSubcard.findByProps({ 'aria-label': c('collectorTlsSkipVerify') }).props.checked
    ).toBe(true);

    expect(
      renderer.root.findByProps({ 'data-testid': 'system-collector-save-btn' }).props.disabled
    ).toBe(true);
    await act(async () => {
      renderer.root
        .findByProps({ label: c('collectorPollInterval') })
        .props.onChange({ target: { value: '600' } });
    });
    expect(
      renderer.root.findByProps({ 'data-testid': 'system-collector-save-btn' }).props.disabled
    ).toBe(false);
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'system-collector-save-btn' })
        .props.onClick();
    });
    expect(patchSpy).toHaveBeenCalledWith(
      'https://manager.test',
      {
        collector: {
          collectorMode: 'subscribe',
          pollIntervalMs: 600,
          batchSize: 100,
          queryLimit: 50000,
          queue: 'usage-events',
          tlsSkipVerify: true,
        },
      },
      'admin-secret'
    );

    // In http mode, queue/popSide/tlsSkipVerify are hidden and empty queue is NOT validated or sent.
    await act(async () => renderer.unmount());
    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/console/system']}>
          <FreshSystemWorkspace
            basePath="/console"
            credential="admin-secret"
            onUnauthorized={vi.fn()}
          />
        </MemoryRouter>
      );
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'system-collector-resp-fields' })
    ).toHaveLength(0);
    expect(
      renderer.root.findByProps({ 'data-testid': 'system-collector-save-btn' }).props.disabled
    ).toBe(true);
    await act(async () => {
      renderer.root
        .findByProps({ label: c('collectorPollInterval') })
        .props.onChange({ target: { value: '600' } });
    });
    expect(
      renderer.root.findByProps({ 'data-testid': 'system-collector-save-btn' }).props.disabled
    ).toBe(false);
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'system-collector-save-btn' })
        .props.onClick();
    });
    expect(patchSpy).toHaveBeenLastCalledWith(
      'https://manager.test',
      {
        collector: {
          collectorMode: 'http',
          pollIntervalMs: 600,
          batchSize: 100,
          queryLimit: 50000,
        },
      },
      'admin-secret'
    );
  });

  it('keeps FreshRebindDialog result banner visible and refreshes getManagerConfig on rebind success', async () => {
    vi.stubGlobal('crypto', { getRandomValues: (bytes: Uint8Array) => bytes.fill(0x22) });
    try {
      availability.managerServiceAvailable = true;
      availability.managerServiceBase = 'https://manager.test';
    const baseConfig = buildManagerConfigResponse();
    const initialConfig: ManagerConfigResponse = {
      ...baseConfig,
      config: {
        ...baseConfig.config,
        cpaConnection: { cpaBaseUrl: 'https://cpa.example.com', managementKeyConfigured: false },
      },
    };
    const refreshedConfig: ManagerConfigResponse = {
      ...baseConfig,
      config: {
        ...baseConfig.config,
        cpaConnection: { cpaBaseUrl: 'https://cpa.example.com', managementKeyConfigured: true },
      },
    };
    const getConfigSpy = vi
      .spyOn(usageServiceApi, 'getManagerConfig')
      .mockResolvedValueOnce(initialConfig)
      .mockResolvedValueOnce(refreshedConfig);
    vi.spyOn(usageServiceApi, 'getMaintenanceStatus').mockRejectedValue(new Error('not configured'));
    vi.spyOn(usageServiceApi, 'listArchives').mockResolvedValue({ runs: [], total: 0 });

    const fakeClient = {
      get: vi.fn(),
      post: vi.fn(async (resource: string) => {
        if (resource === 'external/rebind') {
          return { status: 'rebound' };
        }
        return {};
      }),
    };

    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/console/system']}>
          <FreshSystemWorkspace
            basePath="/console"
            strategy="connect_external"
            client={fakeClient as never}
            credential="admin-secret"
            onUnauthorized={vi.fn()}
          />
        </MemoryRouter>
      );
    });
    await act(async () => {
      await Promise.resolve();
    });

    await act(async () => {
      renderer.root.findByProps({ id: 'system-config-tabs-storage' }).props.onClick();
    });
    expect(
      renderer.root.findByProps({ 'data-testid': 'system-cpa-credential-state' }).props.children
    ).toBe(c('credentialMissing'));

    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'system-rebind-btn' }).props.onClick();
    });
    await act(async () => {
      renderer.root
        .findByProps({ 'data-testid': 'rebind-management-key' })
        .props.onChange({ target: { value: 'new-mgmt-key' } });
    });
      await act(async () => {
        await renderer.root.findByProps({ 'data-testid': 'rebind-submit' }).props.onClick();
        await Promise.resolve();
        await Promise.resolve();
        await Promise.resolve();
      });

      expect(getConfigSpy).toHaveBeenCalledTimes(2);
      expect(renderer.root.findByProps({ 'data-testid': 'rebind-result' })).toBeDefined();
      expect(
        renderer.root.findByProps({ 'data-testid': 'system-cpa-credential-state' }).props.children
      ).toBe(c('credentialConfigured'));

      await act(async () => {
        renderer.root.findByProps({ 'data-testid': 'rebind-done' }).props.onClick();
      });
      expect(renderer.root.findAllByProps({ 'data-testid': 'rebind-result' })).toHaveLength(0);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it('tracks dirty collector/retention edits via onDirtyChange and drops stale getManagerConfig responses after save', async () => {
    vi.stubGlobal('crypto', { getRandomValues: (bytes: Uint8Array) => bytes.fill(0x22) });
    try {
      availability.managerServiceAvailable = true;
      availability.managerServiceBase = 'https://manager.test';
      const initialConfig = buildManagerConfigResponse();
      const savedConfig = buildManagerConfigResponse();
      savedConfig.config.retention = {
        ...savedConfig.config.retention,
        enabled: false,
        retentionDays: 60,
      };

      let resolveGet!: (val: ManagerConfigResponse) => void;
      const delayedGet = new Promise<ManagerConfigResponse>((res) => {
        resolveGet = res;
      });
      vi.spyOn(usageServiceApi, 'getManagerConfig')
        .mockResolvedValueOnce(initialConfig)
        .mockReturnValueOnce(delayedGet);
      vi.spyOn(usageServiceApi, 'patchSettings').mockResolvedValueOnce(savedConfig);
      vi.spyOn(usageServiceApi, 'getMaintenanceStatus').mockRejectedValue(
        new Error('not configured')
      );
      vi.spyOn(usageServiceApi, 'listArchives').mockResolvedValue({ runs: [], total: 0 });

      const fakeClient = {
        get: vi.fn(),
        post: vi.fn(async () => ({ status: 'rebound' })),
      };

      const onDirtyChange = vi.fn();
      await act(async () => {
        renderer = create(
          <MemoryRouter initialEntries={['/console/system']}>
            <FreshSystemWorkspace
              basePath="/console"
              strategy="connect_external"
              client={fakeClient as never}
              credential="admin-secret"
              onUnauthorized={vi.fn()}
              onDirtyChange={onDirtyChange}
            />
          </MemoryRouter>
        );
        await Promise.resolve();
      });
      expect(onDirtyChange).toHaveBeenLastCalledWith(false);

      await act(async () => {
        renderer.root.findByProps({ id: 'system-config-tabs-storage' }).props.onClick();
      });

      // Start an in-flight getManagerConfig refresh via rebind BEFORE saving retention.
      await act(async () => {
        renderer.root.findByProps({ 'data-testid': 'system-rebind-btn' }).props.onClick();
      });
      await act(async () => {
        renderer.root
          .findByProps({ 'data-testid': 'rebind-management-key' })
          .props.onChange({ target: { value: 'new-mgmt-key' } });
      });
      await act(async () => {
        await renderer.root.findByProps({ 'data-testid': 'rebind-submit' }).props.onClick();
        await Promise.resolve();
      });
      await act(async () => {
        renderer.root.findByProps({ 'data-testid': 'rebind-done' }).props.onClick();
      });

      const daysInput = renderer.root.findByProps({ id: 'system-retention-days-input' });
      await act(async () => {
        daysInput.props.onChange({ target: { value: '60' } });
      });
      expect(onDirtyChange).toHaveBeenLastCalledWith(true);

      await act(async () => {
        await renderer.root
          .findByProps({ 'data-testid': 'system-retention-save-btn' })
          .props.onClick();
      });
      expect(onDirtyChange).toHaveBeenLastCalledWith(false);

      // Resolving the older in-flight GET must not overwrite the saved value.
      resolveGet(initialConfig);
      await act(async () => {
        await Promise.resolve();
      });
      expect(renderer.root.findByProps({ id: 'system-retention-days-input' }).props.value).toBe(
        '60'
      );
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it('preserves unsaved collector edits when saving retention across tabs, tracks collector dirty state, and restores clean state after collector save', async () => {
    availability.managerServiceAvailable = true;
    availability.managerServiceBase = 'https://manager.test';
    const initialConfig = buildManagerConfigResponse({ pollIntervalMs: 500, batchSize: 100 });
    const retentionSavedConfig = buildManagerConfigResponse({ pollIntervalMs: 500, batchSize: 100 });
    retentionSavedConfig.config.retention = {
      enabled: true,
      retentionDays: 60,
      envLocked: false,
    };
    const collectorSavedConfig = buildManagerConfigResponse({ pollIntervalMs: 900, batchSize: 100 });
    collectorSavedConfig.config.retention = {
      enabled: true,
      retentionDays: 60,
      envLocked: false,
    };
    const collectorSavedConfig2 = buildManagerConfigResponse({ pollIntervalMs: 950, batchSize: 100 });
    collectorSavedConfig2.config.retention = {
      enabled: true,
      retentionDays: 60,
      envLocked: false,
    };

    vi.spyOn(usageServiceApi, 'getManagerConfig').mockResolvedValue(initialConfig);
    vi.spyOn(usageServiceApi, 'getMaintenanceStatus').mockRejectedValue(
      new Error('not configured')
    );
    vi.spyOn(usageServiceApi, 'listArchives').mockResolvedValue({ runs: [], total: 0 });
    const patchSpy = vi
      .spyOn(usageServiceApi, 'patchSettings')
      .mockResolvedValueOnce(retentionSavedConfig)
      .mockResolvedValueOnce(collectorSavedConfig)
      .mockResolvedValueOnce(collectorSavedConfig2);
    const onDirtyChange = vi.fn();
    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/console/system']}>
          <FreshSystemWorkspace
            basePath="/console"
            credential="admin-secret"
            onUnauthorized={vi.fn()}
            onDirtyChange={onDirtyChange}
          />
        </MemoryRouter>
      );
      await Promise.resolve();
    });
    expect(onDirtyChange).toHaveBeenLastCalledWith(false);

    // Edit collector pollIntervalMs on the Monitoring tab.
    const pollInput = renderer.root.findByProps({ label: c('collectorPollInterval') });
    await act(async () => {
      pollInput.props.onChange({ target: { value: '900' } });
    });
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);

    // Switch to Storage tab, edit retentionDays, and save Retention.
    await act(async () => {
      renderer.root.findByProps({ id: 'system-config-tabs-storage' }).props.onClick();
    });
    const daysInput = renderer.root.findByProps({ id: 'system-retention-days-input' });
    await act(async () => {
      daysInput.props.onChange({ target: { value: '60' } });
    });
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'system-retention-save-btn' })
        .props.onClick();
    });
    expect(patchSpy).toHaveBeenCalledTimes(1);

    // Unsaved collector edit and overall dirty state MUST survive the retention save.
    expect(
      renderer.root.findByProps({ label: c('collectorPollInterval') }).props.value
    ).toBe('900');
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);

    // Switch back to Monitoring tab and save Collector -> now dirty state clears.
    await act(async () => {
      renderer.root.findByProps({ id: 'system-config-tabs-monitoring' }).props.onClick();
    });
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'system-collector-save-btn' })
        .props.onClick();
    });
    expect(patchSpy).toHaveBeenCalledTimes(2);
    expect(onDirtyChange).toHaveBeenLastCalledWith(false);

    // Symmetric check: edit retention, switch to Monitoring and save Collector -> retention edit survives.
    await act(async () => {
      renderer.root.findByProps({ id: 'system-config-tabs-storage' }).props.onClick();
    });
    await act(async () => {
      renderer.root
        .findByProps({ id: 'system-retention-days-input' })
        .props.onChange({ target: { value: '75' } });
    });
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);
    await act(async () => {
      renderer.root.findByProps({ id: 'system-config-tabs-monitoring' }).props.onClick();
    });
    await act(async () => {
      renderer.root
        .findByProps({ label: c('collectorPollInterval') })
        .props.onChange({ target: { value: '950' } });
    });
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'system-collector-save-btn' })
        .props.onClick();
    });
    expect(renderer.root.findByProps({ id: 'system-retention-days-input' }).props.value).toBe('75');
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);
  });

  it('localizes archive operation errors via usage_service_errors, skips unmount state updates, and clears copy timer on unmount', async () => {
    await i18n.changeLanguage('en');
    availability.managerServiceAvailable = true;
    availability.managerServiceBase = 'https://manager.test';
    vi.spyOn(usageServiceApi, 'getManagerConfig').mockResolvedValue(buildManagerConfigResponse());
    vi.spyOn(usageServiceApi, 'getMaintenanceStatus').mockResolvedValue({
      raw_event_count: 100,
      raw_min_timestamp_ms: 1,
      raw_max_timestamp_ms: 2,
      raw_archived_event_count: 0,
      raw_deleted_event_count: 0,
      readiness: {
        migration_ready: true,
        hourly_aggregate_ready: true,
        archive_delete_enabled: true,
      },
      compact_requires_stopped_server: false,
    });
    vi.spyOn(usageServiceApi, 'listArchives').mockResolvedValue({
      runs: [
        {
          id: 'run-1',
          mode: 'archive',
          status: 'archived',
          cutoff_timestamp_ms: 2,
          target_event_id: 10,
          event_count: 10,
          estimated_bytes: 100,
          last_archived_event_id: 10,
          archived_event_count: 10,
          archived_uncompressed_bytes: 100,
          archived_compressed_bytes: 50,
          last_deleted_event_id: 0,
          deleted_event_count: 0,
          created_at_ms: 1,
          updated_at_ms: 2,
          has_error: false,
        },
      ],
      total: 1,
    });
    vi.spyOn(usageServiceApi, 'previewArchive').mockRejectedValueOnce({
      code: 'external_cpa_authority_unavailable',
    });
    vi.spyOn(usageServiceApi, 'createArchive').mockRejectedValueOnce(new Error('boom'));
    let rejectVerify!: (reason?: unknown) => void;
    const verifyPromise = new Promise<never>((_resolve, reject) => {
      rejectVerify = reject;
    });
    vi.spyOn(usageServiceApi, 'verifyArchive').mockReturnValueOnce(verifyPromise);

    const writeText = vi.fn().mockResolvedValue(undefined);
    const clearTimeoutSpy = vi.spyOn(globalThis, 'clearTimeout');
    vi.stubGlobal('navigator', { clipboard: { writeText } });

    try {
      await act(async () => {
        renderer = create(
          <MemoryRouter initialEntries={['/console/system']}>
            <FreshSystemWorkspace
              basePath="/console"
              credential="admin-secret"
              onUnauthorized={vi.fn()}
            />
          </MemoryRouter>
        );
        await Promise.resolve();
      });

      await act(async () => {
        renderer.root.findByProps({ id: 'system-config-tabs-storage' }).props.onClick();
      });

      // Known error code maps via usage_service_errors.*
      await act(async () => {
        await renderer.root
          .findByProps({ 'data-testid': 'system-archive-preview-btn' })
          .props.onClick();
      });
      const previewNotifications = useNotificationStore.getState().notifications;
      const lastPreviewNotification = previewNotifications[previewNotifications.length - 1];
      expect(lastPreviewNotification?.message).toBe(
        enLocale.usage_service_errors.external_cpa_authority_unavailable
      );
      expect(lastPreviewNotification?.message).not.toBe(c('collectorSaveFailed'));

      // Unknown error falls back to usage_service_errors.request_failed
      await act(async () => {
        await renderer.root
          .findByProps({ 'data-testid': 'system-archive-create-btn' })
          .props.onClick();
      });
      const createNotifications = useNotificationStore.getState().notifications;
      expect(createNotifications[createNotifications.length - 1]?.message).toBe(
        enLocale.usage_service_errors.request_failed
      );
      // Copy entry path schedules timer, then unmount clears it and ignores late archive rejection.
      await act(async () => {
        await renderer.root
          .findByProps({ 'data-testid': 'system-copy-entry-btn' })
          .props.onClick();
      });
      const verifyBtn = renderer.root
        .findAllByType('button')
        .find((node) =>
          node.findAll((child) => child.props?.children === c('archiveVerifyBtn')).length > 0
        )!;
      await act(async () => {
        verifyBtn.props.onClick();
      });
      const notificationsBeforeUnmount = useNotificationStore.getState().notifications.length;
      await act(async () => renderer.unmount());
      expect(clearTimeoutSpy).toHaveBeenCalled();
      await act(async () => {
        rejectVerify({ code: 'external_cpa_authority_unavailable' });
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(useNotificationStore.getState().notifications).toHaveLength(
        notificationsBeforeUnmount
      );
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it('maps real Axios usage_archive_* HTTP errors to usage_service_errors without falling back to usage_service_not_configured', async () => {
    await i18n.changeLanguage('en');
    availability.managerServiceAvailable = true;
    availability.managerServiceBase = 'https://manager.test';
    vi.spyOn(usageServiceApi, 'getManagerConfig').mockResolvedValue(buildManagerConfigResponse());
    vi.spyOn(usageServiceApi, 'getMaintenanceStatus').mockResolvedValue({
      raw_event_count: 100,
      raw_min_timestamp_ms: 1,
      raw_max_timestamp_ms: 2,
      raw_archived_event_count: 0,
      raw_deleted_event_count: 0,
      readiness: {
        migration_ready: true,
        hourly_aggregate_ready: true,
        archive_delete_enabled: true,
      },
      compact_requires_stopped_server: false,
    });
    vi.spyOn(usageServiceApi, 'listArchives').mockResolvedValue({
      runs: [
        {
          id: 'run-1',
          mode: 'archive',
          status: 'archived',
          cutoff_timestamp_ms: 2,
          target_event_id: 10,
          event_count: 10,
          estimated_bytes: 100,
          last_archived_event_id: 10,
          archived_event_count: 10,
          archived_uncompressed_bytes: 100,
          archived_compressed_bytes: 50,
          last_deleted_event_id: 0,
          deleted_event_count: 0,
          created_at_ms: 1,
          updated_at_ms: 2,
          has_error: false,
        },
      ],
      total: 1,
    });

    const makeArchiveAxiosError = (status: number, code: string) =>
      new AxiosError(`Request failed with status code ${status}`, 'ERR_BAD_REQUEST', undefined, undefined, {
        status,
        data: { code, error: `backend ${code}` },
      } as AxiosResponse);

    const noEventsErr = makeArchiveAxiosError(422, 'usage_archive_no_events');
    const lockedErr = makeArchiveAxiosError(409, 'usage_archive_maintenance_locked');
    const notFoundErr = makeArchiveAxiosError(404, 'usage_archive_not_found');

    expect(getUsageServiceErrorCode(noEventsErr)).toBe('usage_archive_no_events');
    expect(getUsageServiceErrorCode(lockedErr)).toBe('usage_archive_maintenance_locked');
    expect(getUsageServiceErrorCode(notFoundErr)).toBe('usage_archive_not_found');
    expect(getUsageServiceErrorCode(notFoundErr)).not.toBe('usage_service_not_configured');

    const postSpy = vi
      .spyOn(axios, 'post')
      .mockRejectedValueOnce(noEventsErr)
      .mockRejectedValueOnce(lockedErr)
      .mockRejectedValueOnce(notFoundErr);

    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/console/system']}>
          <FreshSystemWorkspace
            basePath="/console"
            credential="admin-secret"
            onUnauthorized={vi.fn()}
          />
        </MemoryRouter>
      );
      await Promise.resolve();
    });

    await act(async () => {
      renderer.root.findByProps({ id: 'system-config-tabs-storage' }).props.onClick();
    });

    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'system-archive-preview-btn' }).props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    let notifications = useNotificationStore.getState().notifications;
    expect(notifications[notifications.length - 1]?.message).toBe(
      enLocale.usage_service_errors.usage_archive_no_events
    );

    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'system-archive-create-btn' }).props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    notifications = useNotificationStore.getState().notifications;
    expect(notifications[notifications.length - 1]?.message).toBe(
      enLocale.usage_service_errors.usage_archive_maintenance_locked
    );

    const verifyBtn = renderer.root
      .findAllByType('button')
      .find((node) =>
        node.findAll((child) => child.props?.children === c('archiveVerifyBtn')).length > 0
      )!;
    await act(async () => {
      verifyBtn.props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    notifications = useNotificationStore.getState().notifications;
    const notFoundMessage = notifications[notifications.length - 1]?.message;
    expect(notFoundMessage).toBe(enLocale.usage_service_errors.usage_archive_not_found);
    expect(notFoundMessage).not.toBe(enLocale.usage_service_errors.usage_service_not_configured);
    expect(notFoundMessage).not.toBe(enLocale.usage_service_errors.request_failed);
    expect(postSpy).toHaveBeenCalledTimes(3);
  });

  it('hosts External CPA usage collection inside the monitoring tab and clears usage blockers after retry', async () => {
    vi.stubGlobal('window', { location: new URL('https://manager.example/console') });
    vi.stubGlobal('crypto', { getRandomValues: (bytes: Uint8Array) => bytes.fill(0xbb) });
    try {
    const context = {
      basePath: '/console',
      apiBase: '/console/api/v2',
      revision: 1,
    };
    const usageBase = {
      cpaVersion: { version: 'v8.0.64', support: 'supported', writeBlocked: false },
    };
    let usagePostCount = 0;
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/external/usage') && init?.method === 'POST') {
        usagePostCount += 1;
        if (usagePostCount === 1) {
          return Promise.resolve(
            Response.json({
              ...usageBase,
              statisticsEnabled: true,
              collector: 'stopped',
              collectionStarted: false,
              blockedReason: 'external_cpa_authority_unavailable',
            })
          );
        }
        return Promise.resolve(
          Response.json({
            ...usageBase,
            statisticsEnabled: true,
            collector: 'running',
            collectionStarted: true,
            blockedReason: '',
          })
        );
      }
      if (url.endsWith('/external/usage')) {
        return Promise.resolve(
          Response.json({
            ...usageBase,
            statisticsEnabled: false,
            collector: 'stopped',
            collectionStarted: false,
            blockedReason: 'usage_statistics_disabled',
          })
        );
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });
    const client = createPublicApiClient(() => context, fetchMock);
    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/console/system']}>
          <FreshSystemWorkspace
            basePath="/console"
            strategy="connect_external"
            client={client}
            credential="admin-test-only"
            onUnauthorized={vi.fn()}
          />
        </MemoryRouter>
      );
    });

    const monitoringCard = renderer.root.findByProps({
      'data-testid': 'system-request-monitoring-card',
    });
    expect(
      monitoringCard.findAllByProps({ 'data-testid': 'external-usage' })
    ).toHaveLength(1);
    const enableBtn = renderer.root.findByProps({ 'data-testid': 'external-usage-enable' });
    await act(async () => enableBtn.props.onClick());
    const confirmBtn = renderer.root.findByProps({ 'data-testid': 'external-usage-confirm' });
    await act(async () => confirmBtn.props.onClick());

    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-blocker-authority_unavailable' })
    ).toHaveLength(1);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-usage-confirm-failed' })
    ).toHaveLength(1);

    const retryBtn = renderer.root.findByProps({ 'data-testid': 'external-usage-confirm-retry' });
    await act(async () => retryBtn.props.onClick());
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-blocker-authority_unavailable' })
    ).toHaveLength(0);
    expect(
      renderer.root
        .findByProps({ 'data-testid': 'external-usage-collector' })
        .findByType('dd')
        .children.join('')
    ).toContain(x('usageCollectorRunning'));
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it('does not repaint an External usage blocker in system monitoring from a read retired by reload', async () => {
    vi.stubGlobal('window', { location: new URL('https://manager.example/console') });
    try {
    const context = {
      basePath: '/console',
      apiBase: '/console/api/v2',
      revision: 1,
    };
    const usageBase = {
      cpaVersion: { version: 'v8.0.64', support: 'supported', writeBlocked: false },
    };
    let settleStaleRead: (response: Response) => void = () => undefined;
    let usageGetCount = 0;
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = String(input);
      if (url.endsWith('/external/usage')) {
        usageGetCount += 1;
        if (usageGetCount === 1) {
          return new Promise<Response>((resolve) => {
            settleStaleRead = resolve;
          });
        }
        return Promise.resolve(
          Response.json({
            ...usageBase,
            statisticsEnabled: true,
            collector: 'running',
            collectionStarted: true,
            blockedReason: '',
          })
        );
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });
    const client = createPublicApiClient(() => context, fetchMock);
    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/console/system']}>
          <FreshSystemWorkspace
            basePath="/console"
            strategy="connect_external"
            client={client}
            credential="admin-test-only"
            onUnauthorized={vi.fn()}
          />
        </MemoryRouter>
      );
    });
    expect(usageGetCount).toBe(1);

    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'system-reload-btn' }).props.onClick();
    });
    expect(usageGetCount).toBe(2);

    await act(async () => {
      settleStaleRead(
        Response.json({ code: 'external_cpa_authority_unavailable' }, { status: 503 })
      );
    });
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-blocker-authority_unavailable' })
    ).toHaveLength(0);
    expect(
      renderer.root
        .findByProps({ 'data-testid': 'external-usage-collector' })
        .findByType('dd')
        .children.join('')
    ).toContain(x('usageCollectorRunning'));
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
