import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { UpgradeAuthenticationEntry } from './UpgradeAuthenticationEntry';
import { FullWebLoginPage } from './FullWebLoginPage';
import { UpgradeAuthenticationError } from './upgradeSession';
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
    mocks.verify
      .mockReset()
      .mockResolvedValue({ revision: 'a'.repeat(64), observation: 'unsupported_upgrade' });
    mocks.remember.mockReset();
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('offline')));
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
    // The wizard is mounted only behind a verified session, and it is a reader.
    // Its first request is the recorded operation — the preflight is a step that
    // only an empty record opens — and every request it makes stays on the two
    // Upgrade read routes.
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-flow' })).toHaveLength(1);
    const readRoutes = [
      'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/status',
      'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/preflight',
    ];
    const requested = vi.mocked(fetch).mock.calls.map(([url]) => String(url));
    expect(requested[0]).toBe(readRoutes[0]);
    for (const url of requested) expect(readRoutes).toContain(url);
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
      let finish!: (value: { revision: string; observation: string }) => void;
      mocks.verify.mockReturnValueOnce(
        new Promise<{ revision: string; observation: string }>((resolve) => {
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
      expect(renderer.root.findByProps({ 'data-testid': 'upgrade-verify' }).props.disabled).toBe(
        true
      );
      expect(JSON.stringify(renderer.toJSON())).not.toContain('Administrator identity verified');
      await act(async () => {
        finish({ revision: 'a'.repeat(64), observation: 'unsupported_upgrade' });
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
    let finish!: (value: { revision: string; observation: string }) => void;
    mocks.verify.mockReturnValueOnce(
      new Promise<{ revision: string; observation: string }>((resolve) => {
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
      finish({ revision: 'a'.repeat(64), observation: 'unsupported_upgrade' });
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
    let late!: (value: { revision: string; observation: string }) => void;
    mocks.verify.mockReturnValueOnce(
      new Promise<{ revision: string; observation: string }>((resolve) => {
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
      late({ revision: 'b'.repeat(64), observation: 'unsupported_upgrade' });
      await first;
    });
    expect(mocks.remember).toHaveBeenCalledTimes(1);
    expect(mocks.remember).toHaveBeenCalledWith('current-key', false);
    await act(async () => renderer.unmount());
    expect(mocks.verify.mock.calls[0][1].aborted).toBe(true);
  });

  it('accepts a proved key whose public observation is unavailable as a read-only entry', async () => {
    mocks.verify.mockResolvedValueOnce({
      revision: 'c'.repeat(64),
      observation: 'observation_unavailable',
    });
    await act(async () => {
      renderer = create(<UpgradeAuthenticationEntry />);
    });
    await act(async () => {
      await adapter().authenticate('old-key', false);
    });

    // A refused observation is not a credential verdict: the entry stays
    // authenticated, names the source condition, and mounts the read-only view.
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-authenticated' })).toHaveLength(1);
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-entry-status' }).props['data-observation']
    ).toBe('observation_unavailable');
    const page = JSON.stringify(renderer.toJSON());
    expect(page).toContain('The source cannot be summarized publicly.');
    expect(page).not.toContain('Migration is not available for this source yet.');
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-flow' })).toHaveLength(1);
  });

  it('does not claim there is no migration path once the wizard reads a recorded operation', async () => {
    mocks.verify.mockResolvedValueOnce({
      revision: 'd'.repeat(64),
      observation: 'observation_unavailable',
    });
    const recorded = {
      contractVersion: 'cpamp.upgrade-wizard.v1',
      recorded: true,
      stage: 'external_connect',
      stageKnown: true,
      nextAction: 'verify',
      domainsDone: ['source_copy'],
      sourceKind: 'upgraded-legacy',
      sourceVersion: 'v1.14.4',
      manualItems: [],
      errorCode: '',
      errorDetail: '',
      reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
    };
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown) => {
        const url = String(input);
        const payload = url.includes('/installation/upgrade/status') ? recorded : {};
        return { ok: true, status: 200, json: async () => payload } as Response;
      })
    );
    await act(async () => {
      renderer = create(<UpgradeAuthenticationEntry />);
    });
    await act(async () => {
      await adapter().authenticate('old-key', false);
    });
    // The wizard's boot read is one promise deep: flush it before reading the page.
    await act(async () => {
      await Promise.resolve();
    });
    await act(async () => {
      await Promise.resolve();
    });

    const page = JSON.stringify(renderer.toJSON());
    // A recorded operation is a migration that is already under way. The entry
    // must not tell the operator nothing was written, that migration is
    // unavailable, or that further decisions wait for a future path — with the
    // recorded row on screen those sentences contradict it.
    expect(page).not.toContain('Nothing was written and migration is not available');
    expect(page).not.toContain('Further upgrade decisions will become available');
    expect(page).toContain('it is the record this page acts on');
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-flow' })).toHaveLength(1);
  });

  it('wires the upgrade artifact transport through the real UpgradeAuthenticationEntry on cpa_import', async () => {
    const digest = 'sha256:' + 'e'.repeat(64);
    const recordedCpaImport = {
      contractVersion: 'cpamp.upgrade-wizard.v1',
      recorded: true,
      stage: 'reconcile',
      stageKnown: true,
      nextAction: 'cpa_import',
      domainsDone: ['source_copy', 'usage_events'],
      sourceKind: 'cpamp_1x',
      sourceVersion: 'v1.14.4',
      manualItems: [],
      errorCode: '',
      errorDetail: '',
      reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
    };
    const artifactCalls: Array<{ url: string; init?: RequestInit }> = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown, init?: RequestInit) => {
        const url = String(input);
        if (url.includes('/installation/upgrade/status')) {
          return Response.json(recordedCpaImport);
        }
        if (url.endsWith('/installation/upgrade/runtime/artifact')) {
          artifactCalls.push({ url, init });
          return Response.json({
            schemaVersion: 1,
            pinned: { version: '8.0.55', artifactId: digest },
            active: { artifactId: digest, origin: 'acquired' },
            required: false,
            operation: null,
            cdnPresets: [],
          });
        }
        if (url.endsWith('/installation/upgrade/runtime/artifact/probe')) {
          artifactCalls.push({ url, init });
          return Response.json({ reachable: true, code: '' });
        }
        throw new Error(`unexpected request: ${url}`);
      })
    );

    await act(async () => {
      renderer = create(<UpgradeAuthenticationEntry />);
    });
    await act(async () => {
      await adapter().authenticate('old-key', false);
    });
    await act(async () => {
      await Promise.resolve();
    });
    await act(async () => {
      await Promise.resolve();
    });

    expect(renderer.root.findAllByProps({ 'data-testid': 'fresh-artifact-step' })).toHaveLength(1);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'upgrade-cpa-import-ready' })
    ).toHaveLength(1);
    expect(JSON.stringify(renderer.toJSON())).toContain('upgrade cpa-import');
    expect(artifactCalls.length).toBeGreaterThanOrEqual(1);
    expect(artifactCalls[0].url).toBe(
      '/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/runtime/artifact'
    );
    expect(artifactCalls[0].init).toMatchObject({
      method: 'GET',
      mode: 'same-origin',
      credentials: 'omit',
      redirect: 'error',
      headers: {
        Accept: 'application/json',
        Authorization: 'Bearer old-key',
      },
    });
  });

  it.each(['status', 'upload'] as const)(
    'revokes an in-flight artifact %s when the entry receives pagehide and creates a fresh session on re-authentication',
    async (kind) => {
      const digest = 'sha256:' + 'e'.repeat(64);
      const record = {
        contractVersion: 'cpamp.upgrade-wizard.v1',
        recorded: true,
        stage: 'reconcile',
        stageKnown: true,
        nextAction: 'cpa_import',
        domainsDone: ['source_copy', 'usage_events'],
        sourceKind: 'upgraded-legacy',
        sourceVersion: 'v1.14.10',
        manualItems: [],
        errorCode: '',
        errorDetail: '',
        reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
      };
      const artifact = {
        schemaVersion: 1,
        pinned: { version: '8.0.55', artifactId: digest },
        active: null,
        required: true,
        operation: null,
        cdnPresets: [],
      };
      let pending: RequestInit | undefined;
      let phase: 'hanging' | 'reauthenticated' = 'hanging';
      const reauthArtifactCalls: Array<{ url: string; init?: RequestInit }> = [];

      vi.stubGlobal(
        'fetch',
        vi.fn((input: unknown, init?: RequestInit) => {
          const url = String(input);
          if (url.endsWith('/installation/upgrade/status')) {
            return Promise.resolve(Response.json(record));
          }
          if (url.endsWith('/runtime/artifact/probe')) {
            return Promise.resolve(Response.json({ reachable: true, code: '' }));
          }
          if (phase === 'reauthenticated' && url.endsWith('/runtime/artifact')) {
            reauthArtifactCalls.push({ url, init });
            return Promise.resolve(
              Response.json({
                ...artifact,
                active: { artifactId: digest, origin: 'acquired' },
                required: false,
              })
            );
          }
          if (url.endsWith('/runtime/artifact') && kind !== 'status') {
            return Promise.resolve(Response.json(artifact));
          }
          if (
            (kind === 'status' && url.endsWith('/runtime/artifact')) ||
            (kind === 'upload' && url.endsWith('/runtime/artifact/upload'))
          ) {
            pending = init;
            return new Promise<Response>(() => {});
          }
          throw new Error(`unexpected request ${url}`);
        })
      );

      await act(async () => {
        renderer = create(<UpgradeAuthenticationEntry />);
      });
      await act(async () => {
        await adapter().authenticate('review-source-admin', false);
      });
      for (let i = 0; i < 3; i++) {
        await act(async () => {
          await Promise.resolve();
        });
      }
      if (kind === 'upload') {
        await act(async () => {
          renderer.root.findByProps({ 'data-testid': 'artifact-source-upload' }).props.onClick();
        });
        const file = new Blob([new Uint8Array([0x1f, 0x8b, 0x08, 0])], {
          type: 'application/gzip',
        });
        await act(async () => {
          void renderer.root
            .findByProps({ 'data-testid': 'artifact-upload-input' })
            .props.onChange({ target: { files: [file], value: 'review.tar.gz' } });
        });
        // Upload has no 15s deadline; only the session lifecycle signal can abort it.
        await act(async () => {
          await vi.advanceTimersByTimeAsync(15_000);
        });
        expect(pending?.signal?.aborted).toBe(false);
      }
      expect(pending).toBeDefined();
      const pagehideCalls = vi
        .mocked(window.addEventListener)
        .mock.calls.filter(([name]) => name === 'pagehide');
      const onPagehide = pagehideCalls[pagehideCalls.length - 1]?.[1] as () => void;
      expect(onPagehide).toBeTypeOf('function');
      await act(async () => {
        onPagehide();
      });
      expect(renderer.root.findAllByType(FullWebLoginPage)).toHaveLength(1);
      expect({
        hasCancellationSignal: !!pending?.signal,
        aborted: pending?.signal?.aborted ?? false,
      }).toEqual({ hasCancellationSignal: true, aborted: true });

      // Re-authenticating creates a fresh session controller so subsequent artifact calls work.
      phase = 'reauthenticated';
      await act(async () => {
        await adapter().authenticate('review-source-admin-reauth', false);
      });
      for (let i = 0; i < 3; i++) {
        await act(async () => {
          await Promise.resolve();
        });
      }
      expect(reauthArtifactCalls).toHaveLength(1);
      expect(reauthArtifactCalls[0].init?.signal?.aborted).toBe(false);
      expect(reauthArtifactCalls[0].init?.headers).toMatchObject({
        Authorization: 'Bearer review-source-admin-reauth',
      });
      expect(
        renderer.root.findAllByProps({ 'data-testid': 'upgrade-cpa-import-ready' })
      ).toHaveLength(1);
    }
  );

  it('names the canonical console URL across locales when upgrade authentication fails with origin_rejected', async () => {
    mocks.verify.mockRejectedValueOnce(
      new UpgradeAuthenticationError('origin_rejected', 'https://public-manager.test')
    );
    await act(async () => {
      renderer = create(<UpgradeAuthenticationEntry />);
    });
    await act(async () => {
      await adapter().authenticate('old-key', false);
    });
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru'] as const) {
      await act(async () => {
        await loginI18n.changeLanguage(locale);
      });
      expect(adapter().error).toContain(
        'https://public-manager.test/.well-known/cpamp-bootstrap/ui'
      );
      expect(adapter().error).not.toContain('cannot be verified safely');
      expect(adapter().error).not.toContain('当前来源无法安全验证');
    }

    mocks.verify.mockRejectedValueOnce(new UpgradeAuthenticationError('origin_rejected'));
    await act(async () => {
      await loginI18n.changeLanguage('en');
      await adapter().authenticate('old-key', false);
    });
    expect(adapter().error).toContain('CPAMP_PUBLIC_ORIGIN');
    expect(adapter().error).not.toContain('cannot be verified safely');
  });
});
