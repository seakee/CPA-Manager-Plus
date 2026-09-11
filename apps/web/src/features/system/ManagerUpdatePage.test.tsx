import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Select } from '@/components/ui/Select';
import { ManagerUpdatePage } from './ManagerUpdatePage';
import type { ReleaseInfo, UpdateStatus } from './managerUpdateApi';

const mocks = vi.hoisted(() => ({
  updates: {
    status: null as UpdateStatus | null,
    available: true,
    busy: false,
    error: false,
    check: vi.fn(),
    setChannel: vi.fn(),
  },
  copy: vi.fn(),
}));
vi.mock('./ManagerUpdates', () => ({ useManagerUpdates: () => mocks.updates }));
vi.mock('@/hooks/useHeaderRefresh', () => ({ useHeaderRefresh: vi.fn() }));
vi.mock('@/utils/clipboard', () => ({ copyToClipboard: mocks.copy }));
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, values?: Record<string, unknown>) =>
      values?.version ? key + ':' + values.version : key,
    i18n: { language: 'en' },
  }),
}));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let renderer: ReactTestRenderer | undefined;
const release = (version = 'v1.12.11'): ReleaseInfo => ({
  schema_version: 1,
  release: { version, stage: 'stable', source_commit: 'a'.repeat(40) },
  content: { summary: { en: 'A concise release summary.', zh: '更新摘要。' }, notes: {} },
  update: {
    breaking: false,
    migration_required: false,
    minimum_direct_upgrade_version: null,
    upgrade_guide_url:
      'https://github.com/seakee/CPA-Manager-Plus/blob/' + version + '/docs/update-check.md',
  },
  distribution: {
    docker: { image: 'seakee/cpa-manager-plus', version_tag: version },
    native: { assets: ['cpa-manager-plus_' + version + '_linux_amd64.tar.gz'] },
  },
  compatibility: { minimum_cpa_version: null },
});
beforeEach(() => {
  mocks.updates.status = {
    current_version: 'v1.12.10',
    source_commit: 'b'.repeat(40),
    state: 'update_available',
    channel: 'stable',
    channel_preference: 'auto',
    automatic: true,
    stale: false,
    last_success_at: '2026-09-08T08:00:00Z',
    upgrade_action: 'direct',
    target: release(),
  };
  mocks.updates.available = true;
  mocks.updates.error = false;
  mocks.updates.busy = false;
  mocks.copy.mockResolvedValue(true);
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
    else renderer = create(page);
  });
}

describe('ManagerUpdatePage', () => {
  it('keeps instructions and settings collapsed until requested, then supports both deployment methods', async () => {
    await renderPage();
    expect(steps().props.hidden).toBe(true);
    expect(renderer!.root.findByType('details').props.open).toBeUndefined();
    expect(text(renderer!.root.findByType('h2'))).toContain('v1.12.11');
    await act(async () => {
      button('manager_updates.show_steps').props.onClick();
    });
    expect(steps().props.hidden).toBe(false);
    expect(text(renderer!.root.findByType('code'))).toBe('seakee/cpa-manager-plus:v1.12.11');
    await act(async () => {
      renderer!.root.findByProps({ id: 'manager-deployment-native' }).props.onClick({});
    });
    expect(renderer!.root.findAllByType('code')).toHaveLength(0);
    const download = renderer!.root.find(
      (node) => node.type === 'a' && text(node).includes('manager_updates.native_download')
    );
    expect(download.props.href).toBe(
      'https://github.com/seakee/CPA-Manager-Plus/releases/tag/v1.12.11'
    );
    expect(renderer!.root.findByProps({ role: 'tabpanel' }).props['aria-labelledby']).toBe(
      'manager-deployment-native'
    );
  });

  it.each([true, false])(
    'copies the exact image and reports clipboard success=%s',
    async (success) => {
      mocks.copy.mockResolvedValue(success);
      await renderPage();
      await act(async () => {
        button('manager_updates.show_steps').props.onClick();
      });
      await act(async () => {
        button('manager_updates.copy_image').props.onClick();
      });
      expect(mocks.copy).toHaveBeenCalledWith('seakee/cpa-manager-plus:v1.12.11');
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
    mocks.updates.status = { ...mocks.updates.status!, target: release('v1.12.12') };
    await renderPage();
    expect(steps().props.hidden).toBe(true);
    expect(text(renderer!.root.findByType('code'))).toBe('seakee/cpa-manager-plus:v1.12.12');
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
      target: release('v1.12.11'),
    };
    await renderPage();
    const content = text(renderer!.root);
    expect(content).toContain('v1.12.11');
    expect(content).toContain('A concise release summary.');
    expect(content).toContain('manager_updates.stale');
    expect(content).toContain('manager_updates.release_notes');
    expect(content).not.toContain('manager_updates.show_steps');
    expect(content).not.toContain('manager_updates.copy_image');
    expect(renderer!.root.findAllByProps({ id: 'manager-upgrade-steps' })).toHaveLength(0);
  });

  it('withholds migration CTA and execution guidance for failed migration release', async () => {
    const target = release('v1.12.11');
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
    expect(content).toContain('v1.12.11');
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
    expect(content).toContain('v1.12.10');
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
});
