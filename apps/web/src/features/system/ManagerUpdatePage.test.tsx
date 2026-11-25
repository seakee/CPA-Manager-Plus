import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Select } from '@/components/ui/Select';
import { ManagerUpdatePage } from './ManagerUpdatePage';
import type { ReleaseInfo, UpdateStatus } from './managerUpdateApi';
import type { CPAUpdateFlow } from './useCPAUpdateFlow';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import enLocale from '@/i18n/locales/en.json';
import zhCNLocale from '@/i18n/locales/zh-CN.json';
import zhTWLocale from '@/i18n/locales/zh-TW.json';
import ruLocale from '@/i18n/locales/ru.json';

const mocks = vi.hoisted(() => ({
  updates: {
    status: null as UpdateStatus | null,
    available: true,
    busy: false,
    error: false,
    refresh: vi.fn(),
    check: vi.fn(),
    setChannel: vi.fn(),
  },
  copy: vi.fn(),
  cpa: {} as CPAUpdateFlow,
}));
vi.mock('./ManagerUpdates', () => ({ useManagerUpdates: () => mocks.updates }));
vi.mock('./useCPAUpdateFlow', () => ({ useCPAUpdateFlow: () => mocks.cpa }));
vi.mock('@/hooks/useHeaderRefresh', () => ({ useHeaderRefresh: vi.fn() }));
vi.mock('@/utils/clipboard', () => ({ copyToClipboard: mocks.copy }));
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, values?: Record<string, unknown>) => {
      if (values?.version) return key + ':' + values.version;
      if (values?.image) return key + ':' + values.image;
      return key;
    },
    i18n: { language: 'en' },
  }),
}));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let renderer: ReactTestRenderer | undefined;
const headingFocusMock = vi.fn();
const SLIM_DIGEST = 'sha256:' + '1'.repeat(64);
const FULL_DIGEST = 'sha256:' + '2'.repeat(64);
const release = (version = 'v2.0.0-beta.2'): ReleaseInfo => {
  const versionTag = version.replace(/^v/, '');
  const stage = version.includes('-beta.')
    ? 'beta'
    : version.includes('-rc.')
      ? 'rc'
      : 'stable';
  return {
    schema_version: 2,
    release: { version, stage, source_commit: 'a'.repeat(40) },
    content: { summary: { en: 'A concise release summary.', zh: '更新摘要。' }, notes: {} },
    update: {
      breaking: false,
      migration_required: false,
      minimum_direct_upgrade_version: null,
      upgrade_guide_url:
        'https://github.com/seakee/CPA-Manager-Plus/blob/' +
        version +
        '/docs/release-notes/' +
        version +
        '-en.md',
    },
    distribution: {
      docker: {
        version_tag: versionTag,
        images: [
          {
            role: 'slim',
            name: 'seakee/cpa-manager-plus',
            tag: versionTag,
            digest: SLIM_DIGEST,
            platforms: ['linux/amd64'],
          },
          {
            role: 'full',
            name: 'seakee/cpa-manager-plus',
            tag: versionTag + '-full',
            digest: FULL_DIGEST,
            platforms: ['linux/amd64'],
          },
          {
            role: 'slim',
            name: 'ghcr.io/seakee/cpa-manager-plus',
            tag: versionTag,
            digest: SLIM_DIGEST,
            platforms: ['linux/amd64'],
          },
          {
            role: 'full',
            name: 'ghcr.io/seakee/cpa-manager-plus',
            tag: versionTag + '-full',
            digest: FULL_DIGEST,
            platforms: ['linux/amd64'],
          },
        ],
      },
      native: { assets: ['cpa-manager-plus-native-v2_' + version + '_linux_amd64.tar.gz'] },
    },
    compatibility: { minimum_cpa_version: null },
  };
};
beforeEach(() => {
  mocks.updates.status = {
    current_version: 'v2.0.0-beta.1',
    source_commit: 'b'.repeat(40),
    deployment_variant: 'docker-full',
    state: 'update_available',
    channel: 'beta',
    channel_preference: 'auto',
    automatic: true,
    stale: false,
    last_success_at: '2026-11-25T08:00:00Z',
    upgrade_action: 'direct',
    target: release(),
  };
  mocks.updates.available = true;
  mocks.updates.error = false;
  mocks.updates.busy = false;
  mocks.copy.mockResolvedValue(true);
  mocks.cpa = {
    status: null,
    statusError: false,
    intent: null,
    recoveryRecord: null,
    stage: 'idle',
    recoveredSession: false,
    prepareProgress: null,
    initialized: true,
    busy: false,
    storageError: null,
    available: false,
    scope: 'test-cpa-scope',
    unresolved: false,
    canCheck: false,
    canPrepare: false,
    canActivate: false,
    canRetry: false,
    refresh: vi.fn(),
    check: vi.fn(),
    prepare: vi.fn(),
    prepareUpload: vi.fn(),
    activate: vi.fn(),
    retry: vi.fn(),
    forget: vi.fn(),
  };
});
afterEach(async () => {
  if (renderer) await act(async () => renderer?.unmount());
  renderer = undefined;
  vi.clearAllMocks();
});
const text = (node: ReactTestInstance): string =>
  node.children.map((child) => (typeof child === 'string' ? child : text(child))).join('');
const button = (key: string) =>
  renderer!.root.find((node) => node.type === 'button' && text(node).includes(key));
const steps = () => renderer!.root.findByProps({ id: 'manager-upgrade-steps' });
async function renderPage() {
  await act(async () => {
    const page = (
      <MemoryRouter initialEntries={['/system/updates']}>
        <ManagerUpdatePage />
      </MemoryRouter>
    );
    if (renderer) renderer.update(page);
    else
      renderer = create(page, {
        createNodeMock: (element) => {
          if (
            element.type === 'h2' &&
            typeof element.props === 'object' &&
            element.props !== null &&
            'id' in element.props &&
            element.props.id === 'cpamp-update-title'
          ) {
            return { focus: headingFocusMock };
          }
          return { focus: vi.fn() };
        },
      });
  });
}

describe('ManagerUpdatePage', () => {
  it('renders independent CPAMP and CPA areas and refreshes both through read-only status calls', async () => {
    mocks.cpa.available = true;
    mocks.cpa.canCheck = true;
    await renderPage();
    expect(renderer!.root.findAllByType('nav')).toHaveLength(0);
    expect(renderer!.root.findAllByType('h1')).toHaveLength(0);
    const firstHeading = renderer!.root.findByProps({ id: 'cpamp-update-title' });
    expect(firstHeading.type).toBe('h2');
    expect(firstHeading.props.tabIndex).toBe(-1);
    expect(headingFocusMock).toHaveBeenCalledWith({ preventScroll: true });
    expect(renderer!.root.findByProps({ 'aria-labelledby': 'cpamp-update-title' })).toBeDefined();
    expect(renderer!.root.findByProps({ 'aria-labelledby': 'cpa-update-title' })).toBeDefined();
    await act(async () => {
      button('cpa_updates.check').props.onClick();
    });
    expect(mocks.cpa.check).toHaveBeenCalledTimes(1);
    expect(mocks.updates.check).not.toHaveBeenCalled();
    await act(async () => {
      button('manager_updates.check_now').props.onClick();
    });
    expect(mocks.updates.check).toHaveBeenCalledTimes(1);
    expect(mocks.cpa.check).toHaveBeenCalledTimes(1);
    const handler = vi.mocked(useHeaderRefresh).mock.lastCall?.[0];
    await act(async () => {
      await handler?.();
    });
    expect(mocks.updates.refresh).toHaveBeenCalledTimes(1);
    expect(mocks.cpa.refresh).toHaveBeenCalledTimes(1);
    expect(mocks.cpa.check).toHaveBeenCalledTimes(1);
    expect(mocks.updates.check).toHaveBeenCalledTimes(1);
  });

  it('keeps instructions and settings collapsed until requested, then supports both deployment methods', async () => {
    await renderPage();
    expect(steps().props.hidden).toBe(true);
    expect(renderer!.root.findByType('details').props.open).toBeUndefined();
    expect(text(renderer!.root.findByProps({ id: 'manager-release-status' }))).toContain(
      'v2.0.0-beta.2'
    );
    await act(async () => {
      button('manager_updates.show_steps').props.onClick();
    });
    expect(steps().props.hidden).toBe(false);
    expect(text(renderer!.root.findByType('code'))).toBe(
      `seakee/cpa-manager-plus:2.0.0-beta.2-full@${FULL_DIGEST}`
    );
    expect(text(renderer!.root)).not.toContain('cpa-manager-plus:v2.0.0-beta.2');
    await act(async () => {
      renderer!.root.findByProps({ id: 'manager-deployment-native' }).props.onClick({});
    });
    expect(renderer!.root.findAllByType('code')).toHaveLength(0);
    const download = renderer!.root.find(
      (node) => node.type === 'a' && text(node).includes('manager_updates.native_download')
    );
    expect(download.props.href).toBe(
      'https://github.com/seakee/CPA-Manager-Plus/releases/tag/v2.0.0-beta.2'
    );
    expect(renderer!.root.findByProps({ role: 'tabpanel' }).props['aria-labelledby']).toBe(
      'manager-deployment-native'
    );
  });

  it.each([true, false])(
    'copies the exact pinned image and reports clipboard success=%s',
    async (success) => {
      mocks.copy.mockResolvedValue(success);
      await renderPage();
      await act(async () => {
        button('manager_updates.show_steps').props.onClick();
      });
      await act(async () => {
        button('manager_updates.copy_image').props.onClick();
      });
      expect(mocks.copy).toHaveBeenCalledWith(
        `seakee/cpa-manager-plus:2.0.0-beta.2-full@${FULL_DIGEST}`
      );
      expect(text(renderer!.root.findByProps({ role: 'status' }))).toBe(
        success ? 'manager_updates.copied' : 'manager_updates.copy_failed'
      );
    }
  );

  it.each(['server_action', 'migration', 'breaking'] as const)(
    'keeps migration guidance visible and withholds direct deployment steps for %s',
    async (reason) => {
      const status = mocks.updates.status!;
      if (reason === 'server_action') status.upgrade_action = 'migration_guide';
      if (reason === 'migration') status.target!.update.migration_required = true;
      if (reason === 'breaking') status.target!.update.breaking = true;
      status.target!.compatibility.minimum_cpa_version = 'v7.2.0';
      await renderPage();
      expect(text(renderer!.root.findByProps({ role: 'note' }))).toContain(
        'manager_updates.migration'
      );
      expect(text(renderer!.root.findByProps({ role: 'note' }))).toContain('v7.2.0');
      expect(renderer!.root.findAllByType('code')).toHaveLength(0);
      expect(renderer!.root.findAllByProps({ id: 'manager-upgrade-steps' })).toHaveLength(0);
      const guide = renderer!.root.find(
        (node) => node.type === 'a' && text(node).includes('manager_updates.migration_guide')
      );
      expect(guide.props.href).toBe(status.target!.update.upgrade_guide_url);
    }
  );

  it.each(['error', 'last_error', 'stale'] as const)(
    'does not show an up-to-date result when %s is present',
    async (failure) => {
      const status = mocks.updates.status!;
      status.state = 'up_to_date';
      if (failure === 'error') mocks.updates.error = true;
      if (failure === 'last_error') status.last_error = 'offline';
      if (failure === 'stale') status.stale = true;
      await renderPage();
      const content = text(renderer!.root);
      expect(content).not.toContain('manager_updates.up_to_date');
      expect(content).toContain(
        failure === 'stale' ? 'manager_updates.stale' : 'manager_updates.failed'
      );
    }
  );

  it('labels retained release details as cached after an unsuccessful check', async () => {
    mocks.updates.error = true;
    await renderPage();
    expect(text(renderer!.root)).toContain('manager_updates.cached_release');
    expect(text(renderer!.root)).not.toContain('manager_updates.new_release');
    expect(text(renderer!.root.findByProps({ role: 'status' }))).toBe('manager_updates.failed');
  });

  it('checks manually and changes channel through the shared provider', async () => {
    await renderPage();
    await act(async () => {
      button('manager_updates.check_now').props.onClick();
    });
    expect(mocks.updates.check).toHaveBeenCalledTimes(1);
    await act(async () => {
      renderer!.root.findByType(Select).props.onChange('beta');
    });
    expect(mocks.updates.setChannel).toHaveBeenCalledWith('beta');
    mocks.updates.busy = true;
    await renderPage();
    expect(button('manager_updates.check_now').props.disabled).toBe(true);
    expect(renderer!.root.findByType(Select).props.disabled).toBe(true);
  });

  it('resets open instructions when the channel target changes', async () => {
    await renderPage();
    await act(async () => {
      button('manager_updates.show_steps').props.onClick();
    });
    expect(steps().props.hidden).toBe(false);
    mocks.updates.status = { ...mocks.updates.status!, target: release('v2.0.0-beta.3') };
    await renderPage();
    expect(steps().props.hidden).toBe(true);
    expect(text(renderer!.root.findByType('code'))).toBe(
      `seakee/cpa-manager-plus:2.0.0-beta.3-full@${FULL_DIGEST}`
    );
  });

  it('offers a manual check without claiming a successful check for an unpopulated channel', async () => {
    mocks.updates.status = {
      ...mocks.updates.status!,
      state: 'never_checked',
      target: undefined,
      last_success_at: '0001-01-01T00:00:00Z',
    };
    await renderPage();
    expect(text(renderer!.root)).toContain('manager_updates.never_checked');
    expect(text(renderer!.root)).toContain('manager_updates.not_checked');
    expect(button('manager_updates.check_now').props.disabled).toBe(false);
  });

  it('withholds execution guidance and direct steps for stale direct release', async () => {
    mocks.updates.status = {
      ...mocks.updates.status!,
      state: 'update_available',
      stale: true,
      upgrade_action: 'direct',
      target: release('v2.0.0-beta.2'),
    };
    await renderPage();
    const content = text(renderer!.root);
    expect(content).toContain('v2.0.0-beta.2');
    expect(content).toContain('A concise release summary.');
    expect(content).toContain('manager_updates.stale');
    expect(content).toContain('manager_updates.release_notes');
    expect(content).not.toContain('manager_updates.show_steps');
    expect(content).not.toContain('manager_updates.copy_image');
    expect(renderer!.root.findAllByProps({ id: 'manager-upgrade-steps' })).toHaveLength(0);
  });

  it('withholds migration CTA and execution guidance for failed migration release', async () => {
    const target = release('v2.0.0-beta.2');
    target.update.migration_required = true;
    mocks.updates.status = {
      ...mocks.updates.status!,
      state: 'update_available',
      last_error: 'fetch failed',
      upgrade_action: 'migration_guide',
      target,
    };
    await renderPage();
    const content = text(renderer!.root);
    expect(content).toContain('v2.0.0-beta.2');
    expect(content).toContain('A concise release summary.');
    expect(content).toContain('manager_updates.failed');
    expect(content).toContain('manager_updates.release_notes');
    expect(content).not.toContain('manager_updates.migration_guide');
    expect(content).not.toContain('manager_updates.show_steps');
    expect(renderer!.root.findAllByProps({ id: 'manager-upgrade-steps' })).toHaveLength(0);
  });

  it('displays no_candidate message without release details or upgrade action', async () => {
    mocks.updates.status = {
      ...mocks.updates.status!,
      state: 'no_candidate',
      target: undefined,
      stale: false,
    };
    await renderPage();
    const content = text(renderer!.root);
    expect(content).toContain('manager_updates.no_candidate');
    expect(content).not.toContain('manager_updates.show_steps');
    expect(content).not.toContain('manager_updates.migration_guide');
    expect(content).not.toContain('manager_updates.release_notes');
    expect(button('manager_updates.check_now').props.disabled).toBe(false);
    expect(content).toContain('v2.0.0-beta.1');
  });

  it('avoids claiming stale cache when a check fails before any cached release exists', async () => {
    mocks.updates.status = {
      ...mocks.updates.status!,
      state: 'never_checked',
      target: undefined,
      last_success_at: '0001-01-01T00:00:00Z',
      last_error: 'update source returned HTTP 404',
    };
    await renderPage();
    const content = text(renderer!.root);
    expect(content).toContain('manager_updates.check_failed_no_cache');
    expect(content).toContain('manager_updates.check_failed_hint');
    expect(content).toContain('update source returned HTTP 404');
    expect(content).not.toContain('manager_updates.failed');
  });
  it('explains unknown running version cleanly without blaming network when backend reports unknown running version', async () => {
    mocks.updates.status = {
      ...mocks.updates.status!,
      current_version: 'dev',
      state: 'unknown_version',
      target: undefined,
      last_success_at: '0001-01-01T00:00:00Z',
      last_error: 'unknown running version',
    };
    await renderPage();
    const content = text(renderer!.root);
    expect(content).toContain('manager_updates.unknown_version');
    expect(content).toContain('manager_updates.unknown_version_hint');
    expect(content).not.toContain('manager_updates.check_failed_no_cache');
    expect(content).not.toContain('manager_updates.check_failed_hint');
  });
  it('shows loading and then an unavailable state without actionable update controls', async () => {
    mocks.updates.status = null;
    await renderPage();
    expect(text(renderer!.root)).toContain('manager_updates.loading');
    mocks.updates.available = false;
    await renderPage();
    expect(text(renderer!.root)).toContain('manager_updates.unavailable');
    expect(button('manager_updates.check_now').props.disabled).toBe(true);
    expect(renderer!.root.findAllByType('details')).toHaveLength(0);
    expect(renderer!.root.findAllByType('code')).toHaveLength(0);
  });

  it('generates editable Docker Compose and Docker CLI commands and resets copy status timer', async () => {
    vi.useFakeTimers();
    try {
      await renderPage();
      await act(async () => {
        button('manager_updates.show_steps').props.onClick();
      });
      expect(text(renderer!.root)).toContain(
        'docker compose pull cpa-manager-plus && docker compose up -d cpa-manager-plus'
      );
      const targetInput = renderer!.root.findByProps({ id: 'manager-docker-target' });
      await act(async () => {
        targetInput.props.onChange({ target: { value: 'cpamp-prod' } });
      });
      expect(text(renderer!.root)).toContain(
        'docker compose pull cpamp-prod && docker compose up -d cpamp-prod'
      );
      await act(async () => {
        button('manager_updates.copy_command').props.onClick();
      });
      expect(mocks.copy).toHaveBeenCalledWith(
        'docker compose pull cpamp-prod && docker compose up -d cpamp-prod'
      );
      expect(text(renderer!.root.findByProps({ role: 'status' }))).toBe('manager_updates.copied');
      await act(async () => {
        vi.advanceTimersByTime(1800);
      });
      expect(renderer!.root.findAllByProps({ role: 'status' })).toHaveLength(0);
      await act(async () => {
        renderer!.root.findByProps({ id: 'manager-docker-mode-run' }).props.onClick({});
      });
      expect(text(renderer!.root)).toContain(
        `docker pull seakee/cpa-manager-plus:2.0.0-beta.2-full@${FULL_DIGEST}`
      );
    } finally {
      vi.useRealTimers();
    }
  });

  it('switches Docker variants (full vs slim) and registries (Docker Hub vs GHCR)', async () => {
    mocks.updates.status = {
      ...mocks.updates.status!,
      deployment_variant: 'docker-slim',
    };
    await renderPage();
    await act(async () => {
      button('manager_updates.show_steps').props.onClick();
    });
    // docker-slim defaults to slim on Docker Hub.
    expect(text(renderer!.root.findByType('code'))).toBe(
      `seakee/cpa-manager-plus:2.0.0-beta.2@${SLIM_DIGEST}`
    );
    // Switch to GHCR slim.
    await act(async () => {
      renderer!.root.findByProps({ id: 'manager-docker-registry-ghcr' }).props.onClick({});
    });
    expect(text(renderer!.root.findByType('code'))).toBe(
      `ghcr.io/seakee/cpa-manager-plus:2.0.0-beta.2@${SLIM_DIGEST}`
    );
    // Switch to full on GHCR and check docker pull command.
    await act(async () => {
      renderer!.root.findByProps({ id: 'manager-docker-variant-full' }).props.onClick({});
      renderer!.root.findByProps({ id: 'manager-docker-mode-run' }).props.onClick({});
    });
    expect(text(renderer!.root.findByType('code'))).toBe(
      `ghcr.io/seakee/cpa-manager-plus:2.0.0-beta.2-full@${FULL_DIGEST}`
    );
    expect(text(renderer!.root)).toContain(
      `docker pull ghcr.io/seakee/cpa-manager-plus:2.0.0-beta.2-full@${FULL_DIGEST}`
    );
    expect(text(renderer!.root)).not.toContain('cpa-manager-plus:v2.0.0-beta.2');
  });

  it('defaults to native tab and renders v2 systemd bin/share upgrade steps when deployment_variant is native', async () => {
    const target = release('v2.0.0-beta.2');
    target.content.notes = { en: 'Fixes gateway routing.' };
    target.distribution.native.assets = [
      'cpa-manager-plus-native-v2_v2.0.0-beta.2_linux_amd64.tar.gz',
      'cpa-manager-plus-native-v2_v2.0.0-beta.2_linux_arm64.tar.gz',
    ];
    mocks.updates.status = {
      ...mocks.updates.status!,
      deployment_variant: 'native',
      target,
    };
    await renderPage();
    expect(text(renderer!.root)).toContain('manager_updates.notes_toggle');
    expect(text(renderer!.root)).toContain('Fixes gateway routing.');
    await act(async () => {
      button('manager_updates.show_steps').props.onClick();
    });
    expect(renderer!.root.findByProps({ role: 'tabpanel' }).props['aria-labelledby']).toBe(
      'manager-deployment-native'
    );
    const assetSelect = renderer!.root.findByProps({ id: 'manager-native-asset' });
    await act(async () => {
      assetSelect.props.onChange('cpa-manager-plus-native-v2_v2.0.0-beta.2_linux_arm64.tar.gz');
    });
    const directLink = renderer!.root.find(
      (node) =>
        node.type === 'a' &&
        typeof node.props.href === 'string' &&
        node.props.href.includes(
          'releases/download/v2.0.0-beta.2/cpa-manager-plus-native-v2_v2.0.0-beta.2_linux_arm64.tar.gz'
        )
    );
    expect(directLink).toBeDefined();
    const content = text(renderer!.root);
    expect(content).toContain(
      'curl -fLO https://github.com/seakee/CPA-Manager-Plus/releases/download/v2.0.0-beta.2/cpa-manager-plus-native-v2_v2.0.0-beta.2_linux_arm64.tar.gz && tar -xzf cpa-manager-plus-native-v2_v2.0.0-beta.2_linux_arm64.tar.gz && sudo ./cpa-manager-plus-native-v2_v2.0.0-beta.2_linux_arm64/cpa-manager-plusctl upgrade --home /opt/cpa-manager-plus'
    );
    expect(content).not.toContain('systemctl start');
    expect(content).not.toContain('install-cpamp.sh');
  });

  it('shows badge_ahead pill instead of badge_pending when ahead_of_channel', async () => {
    mocks.updates.status = {
      ...mocks.updates.status!,
      state: 'ahead_of_channel',
      target: undefined,
    };
    await renderPage();
    const content = text(renderer!.root);
    expect(content).toContain('manager_updates.badge_ahead');
    expect(content).not.toContain('manager_updates.badge_pending');
  });

  it('passes pinned image to docker_compose_hint and quotes special characters via sanitizeShellArg', async () => {
    const target = release('v2.0.0-beta.2');
    target.distribution.docker.images = [
      {
        role: 'full',
        name: "seakee/cpa-manager-plus$(id)'test",
        tag: '2.0.0-beta.2-full;echo',
        digest: FULL_DIGEST,
        platforms: ['linux/amd64'],
      },
    ];
    target.distribution.native.assets = ["cpa-manager-plus'(bad)_linux_amd64.tar.gz"];
    mocks.updates.status = {
      ...mocks.updates.status!,
      deployment_variant: 'docker-full',
      target,
    };
    await renderPage();
    await act(async () => {
      button('manager_updates.show_steps').props.onClick();
    });

    const expectedImage = `seakee/cpa-manager-plus$(id)'test:2.0.0-beta.2-full;echo@${FULL_DIGEST}`;
    expect(text(renderer!.root)).toContain(`manager_updates.docker_compose_hint:${expectedImage}`);

    await act(async () => {
      renderer!.root.findByProps({ id: 'manager-docker-mode-run' }).props.onClick({});
    });
    expect(text(renderer!.root)).toContain(
      `docker pull 'seakee/cpa-manager-plus$(id)'\\''test:2.0.0-beta.2-full;echo@${FULL_DIGEST}'`
    );

    await act(async () => {
      renderer!.root.findByProps({ id: 'manager-deployment-native' }).props.onClick();
    });
    const nativeText = text(renderer!.root);
    expect(nativeText).toContain(
      "curl -fLO 'https://github.com/seakee/CPA-Manager-Plus/releases/download/v2.0.0-beta.2/cpa-manager-plus'\\''(bad)_linux_amd64.tar.gz'"
    );
    expect(nativeText).toContain(
      "tar -xzf 'cpa-manager-plus'\\''(bad)_linux_amd64.tar.gz'"
    );
    expect(nativeText).toContain(
      "sudo ./'cpa-manager-plus'\\''(bad)_linux_amd64'/cpa-manager-plusctl upgrade --home /opt/cpa-manager-plus"
    );
    expect(nativeText).not.toContain('systemctl start');
  });

  it('falls back safely when dockerTarget starts with a hyphen or contains invalid compose service characters', async () => {
    await renderPage();
    await act(async () => {
      button('manager_updates.show_steps').props.onClick();
    });
    const targetInput = renderer!.root.findByProps({ id: 'manager-docker-target' });

    for (const badTarget of ['-f', '-f evil.yml', '--help', '  --profile foo  ', '   ', 'bad;id']) {
      await act(async () => {
        targetInput.props.onChange({ target: { value: badTarget } });
      });
      const content = text(renderer!.root);
      expect(content).toContain(
        'docker compose pull cpa-manager-plus && docker compose up -d cpa-manager-plus'
      );
      expect(content).not.toContain('docker compose pull -');
      expect(content).not.toContain('docker compose up -d -');
    }

    await act(async () => {
      targetInput.props.onChange({ target: { value: '  cpamp_prod-2.service  ' } });
    });
    expect(text(renderer!.root)).toContain(
      'docker compose pull cpamp_prod-2.service && docker compose up -d cpamp_prod-2.service'
    );
  });

  it('includes docker-compose.v2-embedded.yml and {{image}} guidance in docker_compose_hint across all locales', () => {
    const locales = [
      ['en.json', enLocale],
      ['zh-CN.json', zhCNLocale],
      ['zh-TW.json', zhTWLocale],
      ['ru.json', ruLocale],
    ] as const;
    for (const [name, locale] of locales) {
      const hint = locale.manager_updates.docker_compose_hint;
      expect(hint, `${name} docker_compose_hint`).toContain('docker-compose.yml');
      expect(hint, `${name} docker_compose_hint`).toContain('docker-compose.v2-embedded.yml');
      expect(hint, `${name} docker_compose_hint`).toContain('{{image}}');
    }
  });
});
