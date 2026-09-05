import { describe, expect, it } from 'vitest';
import type { ManagerConfig } from '@/services/api/usageService';
import enLocale from '@/i18n/locales/en.json';
import ruLocale from '@/i18n/locales/ru.json';
import zhCNLocale from '@/i18n/locales/zh-CN.json';
import zhTWLocale from '@/i18n/locales/zh-TW.json';
import {
  resolveManagerCPAConnection,
  resolveManagerBindingStatus,
  resolveManagerFormDirty,
  resolveManagerRequestAuthKey,
  resolveManagerSaveState,
  resolveApiKeyOperationBlockReason,
  resolveApiKeyReplacePreflight,
  shouldBlockStaleSourceSave,
} from './ConfigPage';

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
  it('keeps the saved embedded CPA URL while omitting the write-only key', () => {
    expect(
      resolveManagerCPAConnection({
        panelHostedByUsageService: true,
        managerConfig: buildManagerConfig({
          cpaConnection: {
            cpaBaseUrl: 'http://saved-cpa.local:8317',
            managementKey: 'legacy-readable-key',
          },
        }),
      })
    ).toEqual({
      cpaBaseUrl: 'http://saved-cpa.local:8317',
      managementKeyConfigured: true,
    });
  });

  it('updates only the saved embedded CPA key when a new key is submitted', () => {
    expect(
      resolveManagerCPAConnection({
        panelHostedByUsageService: true,
        managerConfig: buildManagerConfig({
          cpaConnection: {
            cpaBaseUrl: 'http://saved-cpa.local:8317',
            managementKeyConfigured: true,
          },
        }),
        managementKeyInput: ' new-cpa-key ',
      })
    ).toEqual({
      cpaBaseUrl: 'http://saved-cpa.local:8317',
      managementKeyConfigured: true,
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
            managementKeyConfigured: true,
          },
        }),
        cpaBaseUrlInput: ' http://next-cpa.local:9009 ',
      })
    ).toEqual({
      cpaBaseUrl: 'http://next-cpa.local:9009',
      managementKeyConfigured: true,
    });
  });

  it('updates both embedded CPA URL and key when both are submitted', () => {
    expect(
      resolveManagerCPAConnection({
        panelHostedByUsageService: true,
        managerConfig: buildManagerConfig({
          cpaConnection: {
            cpaBaseUrl: 'http://saved-cpa.local:8317',
            managementKeyConfigured: true,
          },
        }),
        cpaBaseUrlInput: ' http://next-cpa.local:9009 ',
        managementKeyInput: ' next-cpa-key ',
      })
    ).toEqual({
      cpaBaseUrl: 'http://next-cpa.local:9009',
      managementKeyConfigured: true,
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
      managementKeyConfigured: false,
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
      managementKeyConfigured: true,
    });

    expect(
      resolveManagerCPAConnection({
        panelHostedByUsageService: false,
        managerConfig: null,
      })
    ).toEqual({
      cpaBaseUrl: '',
      managementKeyConfigured: false,
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
            managementKeyConfigured: true,
          },
        }),
        ...cleanForm,
        managementKeyInput: '   ',
      })
    ).toBe(false);
  });

  it('treats every non-empty CPA key input as an explicit rotation', () => {
    expect(
      resolveManagerFormDirty({
        managerConfig: buildManagerConfig({
          cpaConnection: {
            cpaBaseUrl: 'http://cpa.local:8317',
            managementKeyConfigured: true,
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
            managementKeyConfigured: true,
          },
        }),
        ...cleanForm,
        managementKeyInput: ' saved-key ',
      })
    ).toBe(true);
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

describe('resolveApiKeyOperationBlockReason', () => {
  it('blocks API key CRUD for an unsaved Source draft', () => {
    expect(
      resolveApiKeyOperationBlockReason({
        sourceDirty: true,
        saving: false,
        managerSaving: false,
        apiKeyMutationInFlight: false,
        diffModalOpen: false,
      })
    ).toBe('source_config_dirty');
  });

  it('allows API key CRUD alongside ordinary Visual dirty state', () => {
    expect(
      resolveApiKeyOperationBlockReason({
        sourceDirty: false,
        saving: false,
        managerSaving: false,
        apiKeyMutationInFlight: false,
        diffModalOpen: false,
      })
    ).toBeNull();
  });

  it.each([
    { saving: true, managerSaving: false, apiKeyMutationInFlight: false, diffModalOpen: false },
    { saving: false, managerSaving: true, apiKeyMutationInFlight: false, diffModalOpen: false },
    { saving: false, managerSaving: false, apiKeyMutationInFlight: true, diffModalOpen: false },
    { saving: false, managerSaving: false, apiKeyMutationInFlight: false, diffModalOpen: true },
  ])('blocks concurrent operations: %j', (state) => {
    expect(
      resolveApiKeyOperationBlockReason({
        sourceDirty: false,
        ...state,
      })
    ).toBe('api_key_operation_busy');
  });
});

describe('API-key locale parity', () => {
  it('keeps API-key persistence keys in every supported locale', () => {
    const locales = [enLocale, zhCNLocale, zhTWLocale, ruLocale];
    const expectedKeys = Object.keys(enLocale.config_management.visual.api_keys).sort();

    for (const locale of locales) {
      expect(Object.keys(locale.config_management.visual.api_keys).sort()).toEqual(expectedKeys);
      for (const key of expectedKeys) {
        expect(
          locale.config_management.visual.api_keys[
            key as keyof typeof locale.config_management.visual.api_keys
          ]
        ).toBeTypeOf('string');
      }
    }
  });
});

describe('resolveApiKeyReplacePreflight', () => {
  it('rejects a replace when the old key is no longer in CPA', () => {
    expect(
      resolveApiKeyReplacePreflight({
        currentKeys: ['sk-other'],
        oldApiKey: ' sk-old ',
        newApiKey: ' sk-new ',
      })
    ).toEqual({ ok: false, reason: 'api_key_stale' });
  });

  it('rejects a replace when the new key already exists in CPA', () => {
    expect(
      resolveApiKeyReplacePreflight({
        currentKeys: ['sk-old', 'sk-new'],
        oldApiKey: 'sk-old',
        newApiKey: 'sk-new',
      })
    ).toEqual({ ok: false, reason: 'api_key_duplicate' });
  });

  it('returns the unique raw canonical old key for a safe replace', () => {
    expect(
      resolveApiKeyReplacePreflight({
        currentKeys: ['sk-old'],
        oldApiKey: ' sk-old ',
        newApiKey: ' sk-new ',
      })
    ).toEqual({ ok: true, canonicalOldApiKey: 'sk-old' });
  });

  it('preserves whitespace in the unique raw canonical old key', () => {
    expect(
      resolveApiKeyReplacePreflight({
        currentKeys: ['  sk-old  '],
        oldApiKey: 'sk-old',
        newApiKey: 'sk-new',
      })
    ).toEqual({ ok: true, canonicalOldApiKey: '  sk-old  ' });
  });

  it('rejects multiple canonical entries with the same runtime identity', () => {
    expect(
      resolveApiKeyReplacePreflight({
        currentKeys: ['sk-old', '  sk-old  '],
        oldApiKey: 'sk-old',
        newApiKey: 'sk-new',
      })
    ).toEqual({ ok: false, reason: 'api_key_ambiguous' });
  });

  it('detects a normalized duplicate replacement target', () => {
    expect(
      resolveApiKeyReplacePreflight({
        currentKeys: ['sk-old', '  sk-new  '],
        oldApiKey: 'sk-old',
        newApiKey: 'sk-new',
      })
    ).toEqual({ ok: false, reason: 'api_key_duplicate' });
  });
});

describe('shouldBlockStaleSourceSave', () => {
  it('blocks a Source save while the snapshot is stale', () => {
    expect(shouldBlockStaleSourceSave({ activeTab: 'source', sourceSnapshotStale: true })).toBe(
      true
    );
  });

  it('does not block Visual save because of a stale Source snapshot', () => {
    expect(shouldBlockStaleSourceSave({ activeTab: 'visual', sourceSnapshotStale: true })).toBe(
      false
    );
  });
});
