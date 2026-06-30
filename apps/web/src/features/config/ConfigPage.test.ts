import { describe, expect, it } from 'vitest';
import type { ManagerConfig } from '@/services/api/usageService';
import {
  resolveManagerCPAConnection,
  resolveManagerBindingStatus,
  resolveManagerFormDirty,
  resolveManagerRequestAuthKey,
  resolveManagerSaveState,
} from './ConfigPage';

const buildManagerConfig = (overrides: Partial<ManagerConfig> = {}): ManagerConfig => ({
  cpaConnection: {
    cpaBaseUrl: 'http://cpa.local:8317',
    managementKey: 'management-key',
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
    enabled: false,
    serviceBase: '',
  },
  ...overrides,
});

describe('resolveManagerRequestAuthKey', () => {
  it('uses the login key for same-origin Manager Server panels', () => {
    expect(
      resolveManagerRequestAuthKey({
        panelHostedByUsageService: true,
        managementKey: ' cpa-or-admin-key ',
      })
    ).toBe('cpa-or-admin-key');
  });

  it('does not use CPA-hosted panel credentials for Manager config requests', () => {
    expect(
      resolveManagerRequestAuthKey({
        panelHostedByUsageService: false,
        managementKey: ' cpa-management-key ',
      })
    ).toBe('');
  });
});

describe('resolveManagerCPAConnection', () => {
  it('keeps the saved embedded CPA URL and key when no new key is submitted', () => {
    expect(
      resolveManagerCPAConnection({
        panelHostedByUsageService: true,
        managerConfig: buildManagerConfig({
          cpaConnection: {
            cpaBaseUrl: 'http://saved-cpa.local:8317',
            managementKey: 'old-cpa-key',
          },
        }),
      })
    ).toEqual({
      cpaBaseUrl: 'http://saved-cpa.local:8317',
      managementKey: 'old-cpa-key',
    });
  });

  it('updates only the saved embedded CPA key when a new key is submitted', () => {
    expect(
      resolveManagerCPAConnection({
        panelHostedByUsageService: true,
        managerConfig: buildManagerConfig({
          cpaConnection: {
            cpaBaseUrl: 'http://saved-cpa.local:8317',
            managementKey: 'old-cpa-key',
          },
        }),
        managementKeyInput: ' new-cpa-key ',
      })
    ).toEqual({
      cpaBaseUrl: 'http://saved-cpa.local:8317',
      managementKey: 'new-cpa-key',
    });
  });

  it('updates the embedded CPA URL when a new URL is submitted', () => {
    expect(
      resolveManagerCPAConnection({
        panelHostedByUsageService: true,
        managerConfig: buildManagerConfig({
          cpaConnection: {
            cpaBaseUrl: 'http://saved-cpa.local:8317',
            managementKey: 'old-cpa-key',
          },
        }),
        cpaBaseUrlInput: ' http://next-cpa.local:9009 ',
      })
    ).toEqual({
      cpaBaseUrl: 'http://next-cpa.local:9009',
      managementKey: 'old-cpa-key',
    });
  });

  it('updates both embedded CPA URL and key when both are submitted', () => {
    expect(
      resolveManagerCPAConnection({
        panelHostedByUsageService: true,
        managerConfig: buildManagerConfig({
          cpaConnection: {
            cpaBaseUrl: 'http://saved-cpa.local:8317',
            managementKey: 'old-cpa-key',
          },
        }),
        cpaBaseUrlInput: ' http://next-cpa.local:9009 ',
        managementKeyInput: ' next-cpa-key ',
      })
    ).toEqual({
      cpaBaseUrl: 'http://next-cpa.local:9009',
      managementKey: 'next-cpa-key',
    });
  });

  it('returns an empty connection when embedded Manager config is not loaded yet', () => {
    expect(
      resolveManagerCPAConnection({
        panelHostedByUsageService: true,
        managerConfig: null,
      })
    ).toEqual({
      cpaBaseUrl: '',
      managementKey: '',
    });
  });

  it('keeps external panel connections unchanged instead of binding the current CPA', () => {
    expect(
      resolveManagerCPAConnection({
        panelHostedByUsageService: false,
        managerConfig: buildManagerConfig(),
      })
    ).toEqual({
      cpaBaseUrl: 'http://cpa.local:8317',
      managementKey: 'management-key',
    });

    expect(
      resolveManagerCPAConnection({
        panelHostedByUsageService: false,
        managerConfig: null,
      })
    ).toEqual({
      cpaBaseUrl: '',
      managementKey: '',
    });
  });
});

describe('resolveManagerFormDirty', () => {
  const cleanForm = {
    cpaBaseUrlInput: 'http://cpa.local:8317',
    managementKeyInput: '',
    requestMonitoringEnabled: true,
    collectorMode: 'auto',
    pollIntervalMs: '500',
    batchSize: '100',
    queryLimit: '50000',
    codexQuotaUserAgent: '',
  };

  it('does not mark a freshly loaded Manager config as dirty when the key input is empty', () => {
    expect(
      resolveManagerFormDirty({
        managerConfig: buildManagerConfig(),
        ...cleanForm,
      })
    ).toBe(false);
  });

  it('treats an empty CPA key input as keeping the saved key', () => {
    expect(
      resolveManagerFormDirty({
        managerConfig: buildManagerConfig({
          cpaConnection: {
            cpaBaseUrl: 'http://cpa.local:8317',
            managementKey: 'saved-key',
          },
        }),
        ...cleanForm,
        managementKeyInput: '   ',
      })
    ).toBe(false);
  });

  it('marks the form dirty only when a submitted CPA key differs from the saved key', () => {
    expect(
      resolveManagerFormDirty({
        managerConfig: buildManagerConfig({
          cpaConnection: {
            cpaBaseUrl: 'http://cpa.local:8317',
            managementKey: 'saved-key',
          },
        }),
        ...cleanForm,
        managementKeyInput: ' next-key ',
      })
    ).toBe(true);

    expect(
      resolveManagerFormDirty({
        managerConfig: buildManagerConfig({
          cpaConnection: {
            cpaBaseUrl: 'http://cpa.local:8317',
            managementKey: 'saved-key',
          },
        }),
        ...cleanForm,
        managementKeyInput: ' saved-key ',
      })
    ).toBe(false);
  });

  it('normalizes CPA base URLs and numeric inputs before comparing dirty state', () => {
    expect(
      resolveManagerFormDirty({
        managerConfig: buildManagerConfig(),
        ...cleanForm,
        cpaBaseUrlInput: ' http://cpa.local:8317/ ',
        pollIntervalMs: '0500',
      })
    ).toBe(false);
  });

  it('marks changed monitoring fields and invalid numeric input as dirty', () => {
    expect(
      resolveManagerFormDirty({
        managerConfig: buildManagerConfig(),
        ...cleanForm,
        requestMonitoringEnabled: false,
      })
    ).toBe(true);

    expect(
      resolveManagerFormDirty({
        managerConfig: buildManagerConfig(),
        ...cleanForm,
        pollIntervalMs: '',
      })
    ).toBe(true);
  });
});

describe('resolveManagerBindingStatus', () => {
  it('treats same-origin Manager Server panels as matched', () => {
    expect(
      resolveManagerBindingStatus({
        panelHostedByUsageService: true,
      })
    ).toBe('matched');
  });

  it('treats all CPA-hosted panels as unconfigured for Manager binding', () => {
    expect(
      resolveManagerBindingStatus({
        panelHostedByUsageService: false,
      })
    ).toBe('unconfigured');
  });
});

describe('resolveManagerSaveState', () => {
  it('allows saving only dirty same-origin Manager Server config', () => {
    expect(
      resolveManagerSaveState({
        panelHostedByUsageService: true,
        managerDirty: true,
      })
    ).toEqual({
      adminKeyLoadPending: false,
      adminKeyOnlyPending: false,
      hasPendingSave: true,
      canSave: true,
    });
  });

  it('does not create pending saves for clean same-origin Manager Server config', () => {
    expect(
      resolveManagerSaveState({
        panelHostedByUsageService: true,
        managerDirty: false,
      })
    ).toEqual({
      adminKeyLoadPending: false,
      adminKeyOnlyPending: false,
      hasPendingSave: false,
      canSave: false,
    });
  });

  it('does not allow Manager config saves from CPA-hosted panels', () => {
    expect(
      resolveManagerSaveState({
        panelHostedByUsageService: false,
        managerDirty: true,
      })
    ).toEqual({
      adminKeyLoadPending: false,
      adminKeyOnlyPending: false,
      hasPendingSave: false,
      canSave: false,
    });
  });

  it('does not allow Manager config saves while host mode is unknown', () => {
    expect(
      resolveManagerSaveState({
        panelHostedByUsageService: null,
        managerDirty: true,
      })
    ).toEqual({
      adminKeyLoadPending: false,
      adminKeyOnlyPending: false,
      hasPendingSave: false,
      canSave: false,
    });
  });
});
