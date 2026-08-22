import { act, createElement, useLayoutEffect } from 'react';
import { create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import type { ManagerConfig } from '@/services/api/usageService';
import { usageServiceApi } from '@/services/api/usageService';
import { useAuthStore, useUsageServiceStore } from '@/stores';
import {
  buildPanelManagerServiceCandidates,
  managerConfigMatchesPanel,
  resolvePanelFeatureAvailability,
  usePanelFeatureAvailability,
} from './usePanelFeatureAvailability';

const buildManagerConfig = (overrides: Partial<ManagerConfig> = {}): ManagerConfig => ({
  cpaConnection: {
    cpaBaseUrl: 'http://cpa.local:8317',
    managementKeyConfigured: true,
  },
  collector: {
    enabled: true,
    collectorMode: 'auto',
    queue: 'usage',
    popSide: 'right',
    batchSize: 100,
    pollIntervalMs: 500,
    queryLimit: 50000,
  },
  externalUsageService: {
    enabled: true,
    serviceBase: 'http://manager.local:18317',
  },
  ...overrides,
});

const createMemoryStorage = () => {
  const store = new Map<string, string>();
  return {
    getItem: (key: string) => (store.has(key) ? (store.get(key) as string) : null),
    setItem: (key: string, value: string) => {
      store.set(key, value);
    },
    removeItem: (key: string) => {
      store.delete(key);
    },
    clear: () => {
      store.clear();
    },
  };
};

describe('panel feature availability', () => {
  it('uses the current embedded Manager Server as the only Docker-mode candidate', () => {
    expect(
      buildPanelManagerServiceCandidates({
        panelHostedByUsageService: true,
        panelBase: 'http://manager.local:18317',
      })
    ).toEqual(['http://manager.local:18317']);
  });

  it('does not build Manager Server candidates for CPA-hosted panels', () => {
    expect(
      buildPanelManagerServiceCandidates({
        panelHostedByUsageService: false,
        panelBase: 'http://cpa.local:8317',
      })
    ).toEqual([]);
  });

  it('only accepts Manager config for same-origin Manager Server panels', () => {
    expect(
      managerConfigMatchesPanel({
        panelHostedByUsageService: true,
        apiBase: 'http://manager.local:18317',
        config: buildManagerConfig(),
      })
    ).toBe(true);

    expect(
      managerConfigMatchesPanel({
        panelHostedByUsageService: false,
        apiBase: 'http://other-cpa.local:8317',
        config: buildManagerConfig(),
      })
    ).toBe(false);

    expect(
      managerConfigMatchesPanel({
        panelHostedByUsageService: false,
        apiBase: 'http://cpa.local:8317',
        config: buildManagerConfig({
          externalUsageService: { enabled: false, serviceBase: '' },
        }),
      })
    ).toBe(false);
  });

  it('marks Manager-only features available while separately gating request monitoring', () => {
    const availability = resolvePanelFeatureAvailability({
      panelHostedByUsageService: true,
      panelBase: 'http://manager.local:18317',
      managerServiceBase: 'http://manager.local:18317',
      managerConfig: buildManagerConfig({
        collector: {
          ...buildManagerConfig().collector,
          enabled: false,
        },
      }),
      hasManagerCandidate: true,
      managementKey: 'management-key',
    });

    expect(availability.managerServiceAvailable).toBe(true);
    expect(availability.modelPricesAvailable).toBe(true);
    expect(availability.serverCodexInspectionAvailable).toBe(true);
    expect(availability.requestMonitoringAvailable).toBe(false);
    expect(availability.reason).toBe('monitoring_disabled');
  });

  it('requires a configured CPA connection for server inspection', () => {
    const availability = resolvePanelFeatureAvailability({
      panelHostedByUsageService: true,
      panelBase: 'http://manager.local:18317',
      managerServiceBase: 'http://manager.local:18317',
      managerConfig: buildManagerConfig({
        cpaConnection: {
          cpaBaseUrl: '',
          managementKeyConfigured: false,
        },
      }),
      hasManagerCandidate: true,
      managementKey: 'manager-key-without-cpa',
    });

    expect(availability.managerServiceAvailable).toBe(true);
    expect(availability.serverCodexInspectionAvailable).toBe(false);
    expect(availability.requestMonitoringAvailable).toBe(false);
    expect(availability.reason).toBe('service_not_configured');
  });

  it('keeps compatibility with older Manager responses that still include the CPA key', () => {
    const availability = resolvePanelFeatureAvailability({
      panelHostedByUsageService: true,
      panelBase: 'http://manager.local:18317',
      managerServiceBase: 'http://manager.local:18317',
      managerConfig: buildManagerConfig({
        cpaConnection: {
          cpaBaseUrl: 'http://cpa.local:8317',
          managementKey: 'legacy-management-key',
        },
      }),
      hasManagerCandidate: true,
      managementKey: 'manager-key',
    });

    expect(availability.serverCodexInspectionAvailable).toBe(true);
    expect(availability.requestMonitoringAvailable).toBe(true);
    expect(availability.reason).toBe('');
  });

  it('treats a CPA URL without a configured key as unconfigured', () => {
    const availability = resolvePanelFeatureAvailability({
      panelHostedByUsageService: true,
      panelBase: 'http://manager.local:18317',
      managerServiceBase: 'http://manager.local:18317',
      managerConfig: buildManagerConfig({
        cpaConnection: {
          cpaBaseUrl: 'http://cpa.local:8317',
          managementKeyConfigured: false,
        },
      }),
      hasManagerCandidate: true,
      managementKey: 'manager-key',
    });

    expect(availability.serverCodexInspectionAvailable).toBe(false);
    expect(availability.requestMonitoringAvailable).toBe(false);
    expect(availability.reason).toBe('service_not_configured');
  });

  it('keeps Manager-only features unavailable for CPA-hosted panels even with stale Manager config', () => {
    const availability = resolvePanelFeatureAvailability({
      panelHostedByUsageService: false,
      panelBase: 'http://cpa.local:8317',
      managerServiceBase: 'http://manager.local:18317',
      managerConfig: buildManagerConfig(),
      hasManagerCandidate: true,
      managementKey: 'management-key',
    });

    expect(availability.managerServiceAvailable).toBe(false);
    expect(availability.modelPricesAvailable).toBe(false);
    expect(availability.serverCodexInspectionAvailable).toBe(false);
    expect(availability.requestMonitoringAvailable).toBe(false);
    expect(availability.externalManagerConfigAvailable).toBe(false);
    expect(availability.reason).toBe('service_not_configured');
  });

  it('shares one feature detection request across concurrent hook consumers', async () => {
    const getInfoSpy = vi.spyOn(usageServiceApi, 'getInfo').mockImplementation(async (base) => ({
      service: base === 'http://manager.local:18317' ? 'cpa-manager-plus' : 'cli-proxy-api',
    }));
    const getManagerConfigSpy = vi
      .spyOn(usageServiceApi, 'getManagerConfig')
      .mockResolvedValue({ config: buildManagerConfig(), source: 'db' });
    let renderer: ReactTestRenderer | null = null;
    vi.stubGlobal('window', {
      location: {
        protocol: 'http:',
        hostname: 'panel.local',
        host: 'panel.local:5174',
        port: '5174',
      },
    });
    vi.stubGlobal('navigator', { userAgent: 'vitest' });
    vi.stubGlobal('localStorage', createMemoryStorage());

    try {
      useAuthStore.setState({
        apiBase: 'http://cpa.local:8317',
        managementKey: 'management-key',
      });

      function HookConsumer() {
        usePanelFeatureAvailability();
        return null;
      }

      await act(async () => {
        renderer = create(
          createElement('div', null, createElement(HookConsumer), createElement(HookConsumer))
        );
      });

      expect(getInfoSpy).toHaveBeenCalledTimes(1);
      expect(getInfoSpy).toHaveBeenNthCalledWith(1, 'http://panel.local:5174');
      expect(getManagerConfigSpy).not.toHaveBeenCalled();
    } finally {
      act(() => {
        renderer?.unmount();
      });
      getInfoSpy.mockRestore();
      getManagerConfigSpy.mockRestore();
      vi.unstubAllGlobals();
    }
  });

  it('fails closed immediately when the credential scope or service revision changes', async () => {
    const originalRevision = useUsageServiceStore.getState().revision;
    let resolveSecondConfig: ((value: { config: ManagerConfig; source: 'db' }) => void) | null =
      null;
    const secondConfig = new Promise<{ config: ManagerConfig; source: 'db' }>((resolve) => {
      resolveSecondConfig = resolve;
    });
    const getInfoSpy = vi.spyOn(usageServiceApi, 'getInfo').mockResolvedValue({
      service: 'cpa-manager-plus',
    });
    const getManagerConfigSpy = vi
      .spyOn(usageServiceApi, 'getManagerConfig')
      .mockImplementation(async (_base, managementKey) => {
        if (managementKey === 'scope-key-b') return secondConfig;
        return { config: buildManagerConfig(), source: 'db' };
      });
    let renderer: ReactTestRenderer | null = null;
    const latestRef = {
      current: null as ReturnType<typeof usePanelFeatureAvailability> | null,
    };
    vi.stubGlobal('window', {
      location: {
        protocol: 'http:',
        hostname: 'manager.local',
        host: 'manager.local:18317',
        port: '18317',
      },
    });
    vi.stubGlobal('navigator', { userAgent: 'vitest' });
    vi.stubGlobal('localStorage', createMemoryStorage());

    function HookConsumer({
      onAvailability,
    }: {
      onAvailability: (availability: ReturnType<typeof usePanelFeatureAvailability>) => void;
    }) {
      const availability = usePanelFeatureAvailability();
      useLayoutEffect(() => {
        onAvailability(availability);
      }, [availability, onAvailability]);
      return null;
    }
    const onAvailability = (availability: ReturnType<typeof usePanelFeatureAvailability>) => {
      latestRef.current = availability;
    };

    try {
      useAuthStore.setState({
        apiBase: 'http://cpa.scope.local:8317',
        managementKey: 'scope-key-a',
      });

      await act(async () => {
        renderer = create(createElement(HookConsumer, { onAvailability }));
      });

      expect(latestRef.current?.managerServiceAvailable).toBe(true);

      act(() => {
        useAuthStore.setState({ managementKey: 'scope-key-b' });
      });

      expect(latestRef.current).toMatchObject({
        checking: true,
        managerServiceAvailable: false,
        requestMonitoringAvailable: false,
        serverCodexInspectionAvailable: false,
        reason: 'checking',
      });

      await act(async () => {
        resolveSecondConfig?.({ config: buildManagerConfig(), source: 'db' });
        await secondConfig;
      });

      expect(latestRef.current?.managerServiceAvailable).toBe(true);

      act(() => {
        useUsageServiceStore.setState({ revision: originalRevision + 1 });
      });

      expect(latestRef.current).toMatchObject({
        checking: true,
        managerServiceAvailable: false,
        requestMonitoringAvailable: false,
        serverCodexInspectionAvailable: false,
      });

      await act(async () => {
        await Promise.resolve();
      });

      expect(latestRef.current?.managerServiceAvailable).toBe(true);
      expect(getManagerConfigSpy).toHaveBeenCalledTimes(3);
    } finally {
      act(() => {
        renderer?.unmount();
      });
      useUsageServiceStore.setState({ revision: originalRevision });
      getInfoSpy.mockRestore();
      getManagerConfigSpy.mockRestore();
      vi.unstubAllGlobals();
    }
  });
});
