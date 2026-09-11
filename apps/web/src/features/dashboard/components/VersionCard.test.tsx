import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from 'react-router-dom';
import type { ConnectionStatus } from '@/types';
import type { PanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { VersionCard } from './VersionCard';
import styles from './VersionCard.module.scss';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const { mocks } = vi.hoisted(() => ({
  mocks: {
    checkManagerUpdateIndex: vi.fn(),
    checkLatest: vi.fn(),
    showNotification: vi.fn(),
    panelFeatureAvailability: {
      checking: false,
      panelHostConfirmed: true,
      panelHostMode: 'manager_embedded' as const,
      panelBase: '',
      managerServiceBase: '',
      managerServiceAvailable: true,
      requestMonitoringAvailable: true,
      modelPricesAvailable: true,
      serverCodexInspectionAvailable: true,
      dockerSetupAvailable: true,
      externalManagerConfigAvailable: false,
      reason: '' as const,
    } as PanelFeatureAvailability,
    updates: {
      status: {} as Record<string, unknown>,
      check: vi.fn(),
      available: true,
      busy: false,
      error: false,
    },
  },
}));

vi.mock('@/features/system/ManagerUpdates', () => ({
  useManagerUpdates: () => mocks.updates,
}));

vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => mocks.panelFeatureAvailability,
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) =>
      options?.version ? `${key}:${String(options.version)}` : key,
    i18n: { language: 'en-US' },
  }),
}));

vi.mock('@/stores', () => ({
  useNotificationStore: (
    selector: (state: { showNotification: typeof mocks.showNotification }) => unknown
  ) => selector({ showNotification: mocks.showNotification }),
}));

vi.mock('@/services/api', () => ({
  versionApi: {
    checkManagerUpdateIndex: mocks.checkManagerUpdateIndex,
    checkLatest: mocks.checkLatest,
  },
}));

let renderer: ReactTestRenderer | null = null;

const getText = (node: ReactTestInstance): string =>
  node.children.map((child) => (typeof child === 'string' ? child : getText(child))).join('');

const findAnchor = (renderer: ReactTestRenderer, className: string, text: string) =>
  renderer.root.find(
    (node) =>
      node.type === 'a' && node.props.className?.includes(className) && getText(node).includes(text)
  );

const findBadge = (renderer: ReactTestRenderer, type: string, text: string) =>
  renderer.root.find(
    (node) =>
      node.type === type &&
      node.props.className?.includes(styles.badge) &&
      node.props.className?.includes(styles.badgeUpdate) &&
      getText(node).includes(text)
  );

const createDeferred = <T,>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;

  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });

  return {
    promise,
    resolve,
    reject,
  };
};

const findManagerRefreshButton = (renderer: ReactTestRenderer) => {
  const managerSection = renderer.root.find(
    (node) =>
      node.type === 'div' &&
      node.props.className?.includes(styles.item) &&
      node.findAll((child) => getText(child).includes('title.abbr')).length > 0
  );
  return managerSection.find(
    (node) =>
      node.type === 'button' &&
      node.props['aria-label'] === 'system_info.version_check_button'
  );
};

const renderCard = async ({
  appVersion = '1.12.6',
  apiVersion = '7.2.143',
  latestApp = '1.12.6',
  latestApi = '7.2.143',
  connectionStatus = 'connected' as ConnectionStatus,
  refreshSignal,
  statusOverrides = {},
  error = false,
  panelHostConfirmed = true,
  panelHostMode = 'manager_embedded' as const,
  managerServiceAvailable = true,
  updateIndexData = {
    schema_version: 1,
    channels: {
      stable: { version: latestApp },
      rc: null,
      beta: null,
    },
  },
  mockUpdateIndex,
}: {
  appVersion?: string;
  apiVersion?: string;
  latestApp?: string;
  latestApi?: string;
  connectionStatus?: ConnectionStatus;
  refreshSignal?: number;
  statusOverrides?: Record<string, unknown>;
  error?: boolean;
  panelHostConfirmed?: boolean;
  panelHostMode?: 'manager_embedded' | 'external_panel';
  managerServiceAvailable?: boolean;
  updateIndexData?: unknown;
  mockUpdateIndex?: () => Promise<unknown>;
} = {}) => {
  mocks.panelFeatureAvailability = {
    checking: false,
    panelHostConfirmed,
    panelHostMode,
    panelBase: '',
    managerServiceBase: '',
    managerServiceAvailable,
    requestMonitoringAvailable: true,
    modelPricesAvailable: true,
    serverCodexInspectionAvailable: true,
    dockerSetupAvailable: true,
    externalManagerConfigAvailable: false,
    reason: '',
  };
  if (mockUpdateIndex) {
    mocks.checkManagerUpdateIndex.mockImplementation(mockUpdateIndex);
  } else {
    mocks.checkManagerUpdateIndex.mockResolvedValue(updateIndexData);
  }
  mocks.updates.status = {
    current_version: appVersion,
    state: latestApp.includes('gabcdef')
      ? 'never_checked'
      : latestApp === appVersion
        ? 'up_to_date'
        : 'update_available',
    target: { release: { version: latestApp } },
    ...statusOverrides,
  };
  mocks.updates.available = managerServiceAvailable;
  mocks.updates.error = error;
  mocks.checkLatest.mockResolvedValue({ 'latest-version': latestApi });

  await act(async () => {
    renderer = create(
      <MemoryRouter>
        <VersionCard
          appVersion={appVersion}
          apiVersion={apiVersion}
          cpaBase="http://cpa.local:8317"
          connectionStatus={connectionStatus}
          refreshSignal={refreshSignal}
          usageEnabled={false}
          usageLoading={false}
          collectorStatus={null}
          collectorLoading={false}
          errorLogCount={0}
          errorLogsLoading={false}
        />
      </MemoryRouter>
    );
    await Promise.resolve();
    await Promise.resolve();
  });

  if (!renderer) throw new Error('VersionCard did not render');
  return renderer;
};

afterEach(() => {
  if (renderer) {
    act(() => renderer?.unmount());
    renderer = null;
  }
  mocks.checkManagerUpdateIndex.mockReset();
  mocks.checkLatest.mockReset();
  mocks.showNotification.mockReset();
});

describe('VersionCard release links', () => {
  it('keeps current Manager and Core versions linked to their installed releases', async () => {
    const renderer = await renderCard();

    expect(findAnchor(renderer, styles.versionLink, '1.12.6').props.href).toBe(
      'https://github.com/seakee/CPA-Manager-Plus/releases/tag/v1.12.6'
    );
    expect(findAnchor(renderer, styles.versionLink, '7.2.143').props.href).toBe(
      'https://github.com/router-for-me/CLIProxyAPI/releases/tag/v7.2.143'
    );
    expect(mocks.checkManagerUpdateIndex).not.toHaveBeenCalled();
    expect(mocks.checkLatest).toHaveBeenCalledTimes(1);
  });

  it('links a Core update badge to the detected latest Core release', async () => {
    const renderer = await renderCard({ latestApi: 'v7.2.146' });
    const badge = findBadge(renderer, 'a', 'v7.2.146');

    expect(badge.props.href).toBe(
      'https://github.com/router-for-me/CLIProxyAPI/releases/tag/v7.2.146'
    );
    expect(badge.props.target).toBe('_blank');
    expect(badge.props.rel).toBe('noopener noreferrer');
  });

  it('opens the internal update page from a compact Manager badge and preserves the current release link', async () => {
    const renderer = await renderCard({ latestApp: 'v1.12.7' });
    const badge = findAnchor(
      renderer,
      styles.managerUpdateBadge,
      'manager_updates.available_badge'
    );

    expect(badge.props.href).toBe('/system/updates');
    expect(badge.props.target).toBeUndefined();
    expect(badge.props.title).toBe('manager_updates.view_version:v1.12.7');
    expect(getText(badge)).not.toContain('v1.12.7');
    expect(findAnchor(renderer, styles.versionLink, '1.12.6').props.href).toContain('/tag/v1.12.6');
    expect(renderer.root.findAllByType('select')).toHaveLength(0);
    expect(renderer.root.findAllByType('code')).toHaveLength(0);
  });

  it('does not create a badge link for an invalid latest version', async () => {
    const renderer = await renderCard({ latestApp: 'v1.12.7-5-gabcdef', latestApi: '' });

    expect(
      renderer.root.findAll(
        (node) => node.type === 'a' && node.props.className?.includes(styles.badge)
      )
    ).toHaveLength(0);
    expect(findAnchor(renderer, styles.versionLink, '1.12.6').props.href).toContain(
      '/CPA-Manager-Plus/releases/tag/v1.12.6'
    );
  });

  it('keeps the Manager overview quiet when there is no update', async () => {
    const renderer = await renderCard();

    expect(
      renderer.root.findAll(
        (node) =>
          node.type === 'span' &&
          node.props.className?.includes(styles.badgeLatest) &&
          getText(node) === 'dashboard.version_is_latest'
      )
    ).toHaveLength(1);
  });

  it.each([{ stale: true }, { last_error: 'offline' }])(
    'hides an untrusted Manager update badge: %j',
    async (statusOverrides) => {
      const renderer = await renderCard({ latestApp: 'v1.12.7', statusOverrides });
      expect(
        renderer.root.findAll((node) => node.type === 'a' && node.props.href === '/system/updates')
      ).toHaveLength(0);
    }
  );

  it('hides the Manager update badge after a failed request', async () => {
    const renderer = await renderCard({ latestApp: 'v1.12.7', error: true });
    expect(
      renderer.root.findAll((node) => node.type === 'a' && node.props.href === '/system/updates')
    ).toHaveLength(0);
  });

  it('shows the running Manager version when it differs from the panel', async () => {
    const renderer = await renderCard({ statusOverrides: { current_version: 'v1.12.5' } });
    expect(findAnchor(renderer, styles.versionLink, 'v1.12.5').props.href).toContain(
      '/tag/v1.12.5'
    );
    expect(getText(renderer.root)).not.toContain('manager_updates.panel_version');
  });
});

describe('VersionCard external panel fallback', () => {
  it('Case 1: does not check public update index and opens update page when running in Manager-hosted mode', async () => {
    const renderer = await renderCard({
      panelHostConfirmed: true,
      panelHostMode: 'manager_embedded',
      managerServiceAvailable: true,
      latestApp: 'v1.12.12',
      appVersion: 'v1.12.11',
    });

    expect(mocks.checkManagerUpdateIndex).not.toHaveBeenCalled();
    const badge = findAnchor(
      renderer,
      styles.managerUpdateBadge,
      'manager_updates.available_badge'
    );
    expect(badge.props.href).toBe('/system/updates');
  });

  it('Case 2: automatically checks public update index and shows direct GitHub release badge for confirmed external panel', async () => {
    const renderer = await renderCard({
      panelHostConfirmed: true,
      panelHostMode: 'external_panel',
      managerServiceAvailable: false,
      appVersion: 'v1.12.11',
      updateIndexData: {
        schema_version: 1,
        revision: 10,
        generated_at: '2026-09-08T00:00:00Z',
        channels: {
          stable: {
            version: 'v1.12.12',
          },
          rc: null,
          beta: null,
        },
      },
    });

    expect(mocks.checkManagerUpdateIndex).toHaveBeenCalledTimes(1);
    const badge = findBadge(renderer, 'a', 'v1.12.12');
    expect(badge.props.href).toBe(
      'https://github.com/seakee/CPA-Manager-Plus/releases/tag/v1.12.12'
    );
    expect(badge.props.target).toBe('_blank');
    expect(badge.props.rel).toBe('noopener noreferrer');

    const internalBadges = renderer.root.findAll(
      (node) => node.type === 'a' && node.props.href === '/system/updates'
    );
    expect(internalBadges).toHaveLength(0);
  });

  it('Case 3: handles stable=null without showing update badge or internal updates link', async () => {
    const renderer = await renderCard({
      panelHostConfirmed: true,
      panelHostMode: 'external_panel',
      managerServiceAvailable: false,
      appVersion: 'v1.12.11',
      updateIndexData: {
        schema_version: 1,
        channels: {
          stable: null,
        },
      },
    });

    expect(mocks.checkManagerUpdateIndex).toHaveBeenCalledTimes(1);

    const updateBadges = renderer.root.findAll(
      (node) =>
        node.props.className?.includes(styles.badge) &&
        node.props.className?.includes(styles.badgeUpdate)
    );
    expect(updateBadges).toHaveLength(0);

    const internalBadges = renderer.root.findAll(
      (node) => node.type === 'a' && node.props.href === '/system/updates'
    );
    expect(internalBadges).toHaveLength(0);

    expect(findAnchor(renderer, styles.versionLink, 'v1.12.11')).toBeDefined();
  });

  it('Case 4: does not check public update index when panel host is not confirmed', async () => {
    await renderCard({
      panelHostConfirmed: false,
      panelHostMode: 'external_panel',
      managerServiceAvailable: false,
      appVersion: 'v1.12.11',
    });

    expect(mocks.checkManagerUpdateIndex).not.toHaveBeenCalled();
  });

  it('Case 5: performs manual check on external panel and shows update available notification', async () => {
    const renderer = await renderCard({
      panelHostConfirmed: true,
      panelHostMode: 'external_panel',
      managerServiceAvailable: false,
      appVersion: 'v1.12.11',
      updateIndexData: {
        schema_version: 1,
        channels: {
          stable: {
            version: 'v1.12.12',
          },
        },
      },
    });

    expect(mocks.checkManagerUpdateIndex).toHaveBeenCalledTimes(1);

    const buttons = renderer.root.findAllByType('button');
    const refreshButton = buttons[0];
    expect(refreshButton).toBeDefined();

    await act(async () => {
      refreshButton.props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.checkManagerUpdateIndex).toHaveBeenCalledTimes(2);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'system_info.manager_version_update_available:v1.12.12',
      'warning'
    );
  });

  it('Case 6: shows info notification when manual check encounters stable=null', async () => {
    const renderer = await renderCard({
      panelHostConfirmed: true,
      panelHostMode: 'external_panel',
      managerServiceAvailable: false,
      appVersion: 'v1.12.11',
      updateIndexData: {
        schema_version: 1,
        channels: {
          stable: null,
        },
      },
    });

    const buttons = renderer.root.findAllByType('button');
    const refreshButton = buttons[0];

    await act(async () => {
      refreshButton.props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.showNotification).toHaveBeenCalledWith(
      'manager_updates.no_candidate',
      'info'
    );
  });

  it('Case 7: fails closed on check error, clearing previous update badge', async () => {
    const renderer = await renderCard({
      panelHostConfirmed: true,
      panelHostMode: 'external_panel',
      managerServiceAvailable: false,
      appVersion: 'v1.12.11',
      updateIndexData: {
        schema_version: 1,
        channels: {
          stable: {
            version: 'v1.12.12',
          },
        },
      },
    });

    expect(findBadge(renderer, 'a', 'v1.12.12')).toBeDefined();

    mocks.checkManagerUpdateIndex.mockRejectedValueOnce(new Error('Network error'));

    const buttons = renderer.root.findAllByType('button');
    const refreshButton = buttons[0];

    await act(async () => {
      refreshButton.props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.showNotification).toHaveBeenCalledWith(
      'system_info.manager_version_check_error: Network error',
      'error'
    );

    const badges = renderer.root.findAll(
      (node) =>
        node.props.className?.includes(styles.badge) &&
        node.props.className?.includes(styles.badgeUpdate)
    );
    expect(badges).toHaveLength(0);
  });

  it('does not render manual check button for Manager in Manager-hosted mode', async () => {
    const renderer = await renderCard({
      panelHostConfirmed: true,
      panelHostMode: 'manager_embedded',
      managerServiceAvailable: true,
    });

    const buttons = renderer.root.findAllByType('button');
    expect(buttons).toHaveLength(1);
  });

  it('Race Test A: stale success does not overwrite newer no-candidate result', async () => {
    const autoReq = createDeferred<{
      schema_version: number;
      channels: { stable: { version: string } | null };
    }>();
    const manualReq = createDeferred<{
      schema_version: number;
      channels: { stable: { version: string } | null };
    }>();

    const renderer = await renderCard({
      panelHostConfirmed: true,
      panelHostMode: 'external_panel',
      managerServiceAvailable: false,
      appVersion: 'v1.12.11',
      mockUpdateIndex: () => autoReq.promise,
    });

    expect(mocks.checkManagerUpdateIndex).toHaveBeenCalledTimes(1);

    mocks.checkManagerUpdateIndex.mockImplementationOnce(() => manualReq.promise);
    const refreshButton = findManagerRefreshButton(renderer);

    await act(async () => {
      refreshButton.props.onClick();
      await Promise.resolve();
    });

    expect(mocks.checkManagerUpdateIndex).toHaveBeenCalledTimes(2);

    await act(async () => {
      manualReq.resolve({
        schema_version: 1,
        channels: {
          stable: null,
        },
      });
      await Promise.resolve();
      await Promise.resolve();
    });

    const getUpdateBadges = () =>
      renderer.root.findAll(
        (node) =>
          node.props.className?.includes(styles.badge) &&
          node.props.className?.includes(styles.badgeUpdate)
      );

    expect(getUpdateBadges()).toHaveLength(0);
    expect(mocks.showNotification).toHaveBeenCalledWith('manager_updates.no_candidate', 'info');
    mocks.showNotification.mockClear();

    await act(async () => {
      autoReq.resolve({
        schema_version: 1,
        channels: {
          stable: {
            version: 'v1.12.12',
          },
        },
      });
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(getUpdateBadges()).toHaveLength(0);
    expect(mocks.showNotification).not.toHaveBeenCalledWith(
      'system_info.manager_version_update_available:v1.12.12',
      'warning'
    );
    expect(mocks.showNotification).not.toHaveBeenCalled();
  });

  it('Race Test B: stale failure does not clear newer successful candidate or set error state', async () => {
    const autoReq = createDeferred<{
      schema_version: number;
      channels: { stable: { version: string } | null };
    }>();
    const manualReq = createDeferred<{
      schema_version: number;
      channels: { stable: { version: string } | null };
    }>();

    const renderer = await renderCard({
      panelHostConfirmed: true,
      panelHostMode: 'external_panel',
      managerServiceAvailable: false,
      appVersion: 'v1.12.11',
      mockUpdateIndex: () => autoReq.promise,
    });

    expect(mocks.checkManagerUpdateIndex).toHaveBeenCalledTimes(1);

    mocks.checkManagerUpdateIndex.mockImplementationOnce(() => manualReq.promise);
    const refreshButton = findManagerRefreshButton(renderer);

    await act(async () => {
      refreshButton.props.onClick();
      await Promise.resolve();
    });

    expect(mocks.checkManagerUpdateIndex).toHaveBeenCalledTimes(2);

    await act(async () => {
      manualReq.resolve({
        schema_version: 1,
        channels: {
          stable: {
            version: 'v1.12.13',
          },
        },
      });
      await Promise.resolve();
      await Promise.resolve();
    });

    const badge = findBadge(renderer, 'a', 'v1.12.13');
    expect(badge).toBeDefined();
    expect(badge.props.href).toBe(
      'https://github.com/seakee/CPA-Manager-Plus/releases/tag/v1.12.13'
    );
    mocks.showNotification.mockClear();

    await act(async () => {
      autoReq.reject(new Error('stale network failure'));
      await Promise.resolve();
      await Promise.resolve();
    });

    const badgeAfter = findBadge(renderer, 'a', 'v1.12.13');
    expect(badgeAfter).toBeDefined();
    expect(mocks.showNotification).not.toHaveBeenCalled();
  });

  it('Race Test C: stale manual check result does not update UI or trigger notifications', async () => {
    const autoReq = createDeferred<{
      schema_version: number;
      channels: { stable: { version: string } | null };
    }>();

    const renderer = await renderCard({
      panelHostConfirmed: true,
      panelHostMode: 'external_panel',
      managerServiceAvailable: false,
      appVersion: 'v1.12.11',
      mockUpdateIndex: () => autoReq.promise,
    });

    await act(async () => {
      autoReq.resolve({
        schema_version: 1,
        channels: { stable: null },
      });
      await Promise.resolve();
    });

    const manualReqA = createDeferred<{
      schema_version: number;
      channels: { stable: { version: string } | null };
    }>();
    mocks.checkManagerUpdateIndex.mockImplementationOnce(() => manualReqA.promise);
    const refreshButton = findManagerRefreshButton(renderer);

    await act(async () => {
      refreshButton.props.onClick();
      await Promise.resolve();
    });

    const autoReqB = createDeferred<{
      schema_version: number;
      channels: { stable: { version: string } | null };
    }>();
    mocks.checkManagerUpdateIndex.mockImplementationOnce(() => autoReqB.promise);

    await act(async () => {
      renderer.update(
        <MemoryRouter>
          <VersionCard
            appVersion="v1.12.11"
            apiVersion="7.2.143"
            cpaBase="http://cpa.local:8317"
            connectionStatus="connected"
            refreshSignal={1}
            usageEnabled={false}
            usageLoading={false}
            collectorStatus={null}
            collectorLoading={false}
            errorLogCount={0}
            errorLogsLoading={false}
          />
        </MemoryRouter>
      );
      await Promise.resolve();
    });

    await act(async () => {
      autoReqB.resolve({
        schema_version: 1,
        channels: {
          stable: {
            version: 'v1.12.13',
          },
        },
      });
      await Promise.resolve();
      await Promise.resolve();
    });

    const badgeB = findBadge(renderer, 'a', 'v1.12.13');
    expect(badgeB).toBeDefined();
    mocks.showNotification.mockClear();

    await act(async () => {
      manualReqA.resolve({
        schema_version: 1,
        channels: {
          stable: {
            version: 'v1.12.12',
          },
        },
      });
      await Promise.resolve();
      await Promise.resolve();
    });

    const badgeAfter = findBadge(renderer, 'a', 'v1.12.13');
    expect(badgeAfter).toBeDefined();
    expect(
      renderer.root.findAll((node) => getText(node).includes('v1.12.12'))
    ).toHaveLength(0);
    expect(mocks.showNotification).not.toHaveBeenCalled();
  });

  it('invalidates pending in-flight request when external fallback is disabled', async () => {
    const autoReq = createDeferred<{
      schema_version: number;
      channels: { stable: { version: string } | null };
    }>();

    const renderer = await renderCard({
      panelHostConfirmed: true,
      panelHostMode: 'external_panel',
      managerServiceAvailable: false,
      appVersion: 'v1.12.11',
      mockUpdateIndex: () => autoReq.promise,
    });

    mocks.panelFeatureAvailability = {
      ...mocks.panelFeatureAvailability,
      panelHostMode: 'manager_embedded',
      managerServiceAvailable: true,
    };

    await act(async () => {
      renderer.update(
        <MemoryRouter>
          <VersionCard
            appVersion="v1.12.11"
            apiVersion="7.2.143"
            cpaBase="http://cpa.local:8317"
            connectionStatus="connected"
            refreshSignal={2}
            usageEnabled={false}
            usageLoading={false}
            collectorStatus={null}
            collectorLoading={false}
            errorLogCount={0}
            errorLogsLoading={false}
          />
        </MemoryRouter>
      );
      await Promise.resolve();
    });

    await act(async () => {
      autoReq.resolve({
        schema_version: 1,
        channels: {
          stable: {
            version: 'v1.12.12',
          },
        },
      });
      await Promise.resolve();
      await Promise.resolve();
    });

    const externalBadges = renderer.root.findAll(
      (node) =>
        node.type === 'a' &&
        node.props.href?.includes('tag/v1.12.12') &&
        node.props.className?.includes(styles.badgeUpdate)
    );
    expect(externalBadges).toHaveLength(0);
  });
});
