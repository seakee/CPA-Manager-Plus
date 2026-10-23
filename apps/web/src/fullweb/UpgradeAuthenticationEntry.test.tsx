import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { UpgradeAuthenticationEntry } from './UpgradeAuthenticationEntry';
import { FullWebLoginPage } from './FullWebLoginPage';
import type { ConsoleLoginAdapter } from '../features/login/LoginPage';
import { setFullWebLanguage } from '../i18n/fullweb';
import loginI18n from '../i18n';

const mocks = vi.hoisted(() => ({ candidate: vi.fn(), verify: vi.fn(), remember: vi.fn() }));
vi.mock('./upgradeSession', async (original) => ({
  ...(await original<object>()),
  rememberedUpgradeCredential: mocks.candidate,
  verifyUpgradeCredential: mocks.verify,
  rememberUpgradeCredential: mocks.remember,
}));
vi.mock('./FullWebLoginPage', () => ({
  FullWebLoginPage: () => <div data-testid="login-adapter" />,
  FullWebUpgradeHeader: () => <header />,
}));
// This host leaf lets tests drive the actual adapter rather than imitating the
// login UI; that UI's own form and the real SQLite path are checked separately.

describe('upgrade authentication lifecycle', () => {
  let renderer: ReactTestRenderer;
  const adapter = () =>
    renderer.root.findByType(FullWebLoginPage).props.adapter as ConsoleLoginAdapter;
  beforeEach(async () => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    vi.useFakeTimers();
    setFullWebLanguage('en');
    await loginI18n.changeLanguage('en');
    mocks.candidate.mockReset().mockReturnValue('');
    mocks.verify.mockReset().mockResolvedValue('a'.repeat(64));
    mocks.remember.mockReset();
    vi.stubGlobal('window', {
      location: new URL('https://manager.test'),
      setTimeout,
      clearTimeout,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });
    vi.stubGlobal('document', { addEventListener: vi.fn(), removeEventListener: vi.fn() });
  });
  afterEach(async () => {
    if (renderer) await act(async () => renderer.unmount());
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('updates upgrade login text when the original login language changes without authenticating', async () => {
    await act(async () => {
      renderer = create(<UpgradeAuthenticationEntry />);
    });
    expect(adapter().connectionHint).toContain('Existing installation detected.');
    expect(adapter().credentialPlaceholder).toBe('Existing CPAMP administrator key');
    const restore = adapter().restore;

    await act(async () => {
      await loginI18n.changeLanguage('zh-CN');
    });
    expect(adapter().connectionHint).toContain('检测到现有安装。');
    expect(adapter().credentialPlaceholder).toBe('原 CPAMP 管理员密钥');

    await act(async () => {
      await loginI18n.changeLanguage('en');
    });
    expect(adapter().connectionHint).toContain('Existing installation detected.');
    expect(adapter().credentialPlaceholder).toBe('Existing CPAMP administrator key');
    expect(adapter().restore).toBe(restore);
    expect(mocks.verify).not.toHaveBeenCalled();
  });

  it('restores only a candidate verified by the server and never completes migration', async () => {
    mocks.candidate.mockReturnValue('old-key');
    await act(async () => {
      renderer = create(<UpgradeAuthenticationEntry />);
    });
    await act(async () => adapter().restore());
    expect(mocks.verify).toHaveBeenCalledWith('old-key', expect.any(AbortSignal), undefined);
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-authenticated' })).toHaveLength(
      1
    );
    expect(JSON.stringify(renderer.toJSON())).toContain('Migration is not available');
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(mocks.verify).toHaveBeenLastCalledWith(
      'old-key',
      expect.any(AbortSignal),
      'a'.repeat(64)
    );
  });

  it('keeps the journey and focus during successive renewals while withholding the previous observation', async () => {
    const focus = vi.fn();
    await act(async () => {
      renderer = create(<UpgradeAuthenticationEntry />, {
        createNodeMock: (element) => (element.type === 'h1' ? { focus } : null),
      });
    });
    await act(async () => {
      await adapter().authenticate('old-key', false);
    });
    const heading = renderer.root.findByType('h1');
    expect(focus).toHaveBeenCalledTimes(1);

    for (let index = 0; index < 3; index++) {
      let finish!: (value: string) => void;
      mocks.verify.mockReturnValueOnce(
        new Promise<string>((resolve) => {
          finish = resolve;
        })
      );
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      expect(renderer.root.findAllByType(FullWebLoginPage)).toHaveLength(0);
      expect(
        renderer.root.findByProps({ 'data-testid': 'upgrade-revalidating' }).props['aria-busy']
      ).toBe(true);
      expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-authenticated' })).toHaveLength(
        0
      );
      expect(renderer.root.findByType('h1')).toBe(heading);
      expect(renderer.root.findByProps({ className: 'btn btn-primary' }).props.disabled).toBe(true);
      expect(JSON.stringify(renderer.toJSON())).not.toContain('Administrator identity verified');
      await act(async () => {
        finish('a'.repeat(64));
      });
      expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-authenticated' })).toHaveLength(
        1
      );
      expect(focus).toHaveBeenCalledTimes(1);
    }

    mocks.verify.mockRejectedValueOnce(new Error('source changed'));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(adapter().error).toContain('could not be confirmed');
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-shell' })).toHaveLength(0);
  });

  it('revokes a pending renewal on pagehide and ignores its late success', async () => {
    await act(async () => {
      renderer = create(<UpgradeAuthenticationEntry />);
    });
    await act(async () => {
      await adapter().authenticate('old-key', false);
    });
    let finish!: (value: string) => void;
    mocks.verify.mockReturnValueOnce(
      new Promise<string>((resolve) => {
        finish = resolve;
      })
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    const registrations = vi
      .mocked(window.addEventListener)
      .mock.calls.filter(([type]) => type === 'pagehide');
    const registration = registrations[registrations.length - 1];
    await act(async () => {
      (registration[1] as EventListener)(new Event('pagehide'));
    });
    expect(mocks.verify.mock.calls[mocks.verify.mock.calls.length - 1][1].aborted).toBe(true);
    await act(async () => {
      finish('a'.repeat(64));
    });
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-authenticated' })).toHaveLength(
      0
    );
    expect(renderer.root.findAllByType(FullWebLoginPage)).toHaveLength(1);
  });

  it('falls back on missing/bad cache, accepts weak old keys manually, and revokes on failed revalidation', async () => {
    await act(async () => {
      renderer = create(<UpgradeAuthenticationEntry />);
    });
    await act(async () => adapter().restore());
    expect(mocks.verify).not.toHaveBeenCalled();
    mocks.verify.mockRejectedValueOnce(new Error('bad old cache'));
    await act(async () => {
      await adapter().authenticate('CPA-key', false);
    });
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-authenticated' })).toHaveLength(
      0
    );
    await act(async () => {
      await adapter().authenticate('old-key', true);
    });
    expect(mocks.remember).toHaveBeenLastCalledWith('old-key', true);
    mocks.verify.mockRejectedValueOnce(new Error('source unreadable'));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(adapter().error).toContain('could not be confirmed');
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-authenticated' })).toHaveLength(
      0
    );
  });

  it('ignores a late response after a newer login or unmount', async () => {
    let late!: (value: string) => void;
    mocks.verify.mockReturnValueOnce(
      new Promise<string>((resolve) => {
        late = resolve;
      })
    );
    await act(async () => {
      renderer = create(<UpgradeAuthenticationEntry />);
    });
    let first!: Promise<boolean>;
    await act(async () => {
      first = adapter().authenticate('old-source-key', false);
    });
    await act(async () => {
      await adapter().authenticate('current-key', false);
    });
    await act(async () => {
      late('b'.repeat(64));
      await first;
    });
    expect(mocks.remember).toHaveBeenCalledTimes(1);
    expect(mocks.remember).toHaveBeenCalledWith('current-key', false);
    await act(async () => renderer.unmount());
    expect(mocks.verify.mock.calls[0][1].aborted).toBe(true);
  });
});
