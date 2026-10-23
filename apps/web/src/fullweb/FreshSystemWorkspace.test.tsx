import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { MemoryRouter } from 'react-router-dom';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../i18n';
import { FreshSystemWorkspace } from './FreshSystemWorkspace';
import { configurationText as c } from '../i18n/configurationWorkspace';

vi.mock('../components/ui/Modal', () => ({
  Modal: ({ children, footer, open }: { children: ReactNode; footer: ReactNode; open: boolean }) =>
    open ? (
      <div role="dialog">
        {children}
        {footer}
      </div>
    ) : null,
}));

describe('FreshSystemWorkspace component', () => {
  let renderer: ReactTestRenderer;

  beforeEach(async () => {
    await i18n.changeLanguage('zh-CN');
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
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

    // Title and cards
    expect(renderer.root.findByProps({ 'data-testid': 'fresh-system-workspace' })).toBeDefined();
    expect(renderer.root.findByProps({ 'data-testid': 'system-entry-card' })).toBeDefined();
    expect(renderer.root.findByProps({ 'data-testid': 'system-startup-card' })).toBeDefined();
    expect(renderer.root.findByProps({ 'data-testid': 'system-security-card' })).toBeDefined();
    expect(renderer.root.findByProps({ 'data-testid': 'system-ownership-card' })).toBeDefined();

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
    expect(recoveryModal.findByType('a').props.href).toContain(
      '/docs/operations/reset-admin-key.html'
    );
    const closeBtn = renderer.root.findByProps({ 'data-testid': 'close-recovery-btn' });
    await act(async () => closeBtn.props.onClick());

    expect(renderer.root.findAllByProps({ 'data-testid': 'recovery-modal' })).toHaveLength(0);
  });

  it('does not invent a host service health result', async () => {
    await mount();
    expect(JSON.stringify(renderer.toJSON())).not.toContain('正常运行');
    expect(JSON.stringify(renderer.toJSON())).toContain(c('notRead'));
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
});
