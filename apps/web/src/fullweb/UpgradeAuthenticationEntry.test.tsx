import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { UpgradeAuthenticationEntry } from './UpgradeAuthenticationEntry';
import { FullWebLoginPage } from './FullWebLoginPage';
import {
  UPGRADE_AUTH_CONTRACT,
  UpgradeAuthenticationError,
  type verifyUpgradeCredential,
} from './upgradeSession';
import type { ConsoleLoginAdapter } from '../features/login/LoginPage';
import { setFullWebLanguage } from '../i18n/fullweb';
import loginI18n from '../i18n';

const mocks = vi.hoisted(() => ({
  candidate: vi.fn(),
  verify: vi.fn(),
  remember: vi.fn(),
  realVerify: null as typeof verifyUpgradeCredential | null,
}));
vi.mock('./upgradeSession', async (original) => {
  const actual = await original<{ verifyUpgradeCredential: typeof verifyUpgradeCredential }>();
  mocks.realVerify = actual.verifyUpgradeCredential;
  return {
    ...actual,
    rememberedUpgradeCredential: mocks.candidate,
    verifyUpgradeCredential: mocks.verify,
    rememberUpgradeCredential: mocks.remember,
  };
});
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
    // Hiding the page retires the envelope and its requests, not the key: a
    // BFCache return must not ask the operator to retype a key the server has
    // already proved, and re-entry is a fresh verification that never reuses
    // the retired revision.
    expect(adapter().error).toBe('');
    await act(async () => {
      await adapter().authenticate('old-key', false);
    });
    expect(mocks.verify).toHaveBeenLastCalledWith('old-key', expect.any(AbortSignal), undefined);
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-authenticated' })).toHaveLength(
      1
    );
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
    // A recorded operation needs no public summary either: the entry's own
    // "nothing was written" sentence is what the record below contradicts, so
    // this case is the observation that has that sentence.
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
        return Response.json(url.includes('/installation/upgrade/status') ? recorded : {});
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
    // The boot read answered with a record, so the wizard resumed on it instead
    // of asking the operator to qualify the source again.
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'upgrade-boot-failed' })
    ).toHaveLength(0);
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-flow' })).toHaveLength(1);
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-entry-status' }).props['data-observation']
    ).toBe('observation_unavailable');
    // Neither the "nothing was written" sentence nor the "no migration path"
    // headline may stand next to the recorded row.
    expect(page).not.toContain('Nothing was written and migration is not available');
    expect(page).not.toContain('Migration is not available for this source yet.');
    expect(page).not.toContain('Further upgrade decisions will become available');
    expect(page).toContain('it is the record this page acts on');
  });

  it('offers the cpa_import artifact step on the entry and only offers it as ready after acquisition', async () => {
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
    // The step only offers a source after it has both read the pinned artifact
    // and probed the official source, so the fixture is that pair, not a
    // single "already active" shortcut.
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown) => {
        const url = String(input);
        if (url.endsWith('/installation/upgrade/status')) {
          return Response.json(recordedCpaImport);
        }
        if (url.endsWith('/installation/upgrade/runtime/artifact')) {
          return Response.json({
            schemaVersion: 1,
            pinned: { version: '8.0.55', artifactId: digest },
            active: null,
            required: true,
            operation: null,
            cdnPresets: [],
          });
        }
        if (url.endsWith('/installation/upgrade/runtime/artifact/probe')) {
          return Response.json({ reachable: true, code: '' });
        }
        if (url.endsWith('/installation/upgrade/runtime/artifact/acquire')) {
          return Response.json({
            id: 'op-1',
            source: 'github',
            phase: 'succeeded',
            receivedBytes: 0,
            totalBytes: 0,
            code: '',
          });
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
    for (let i = 0; i < 4; i++) {
      await act(async () => {
        await Promise.resolve();
      });
    }

    expect(renderer.root.findAllByProps({ 'data-testid': 'fresh-artifact-step' })).toHaveLength(1);
    expect(
      renderer.root.findByProps({ 'data-testid': 'artifact-probe-status' }).props['data-probe']
    ).toBe('reachable');
    expect(renderer.root.findByProps({ 'data-testid': 'artifact-source-upload' })).toBeDefined();
    // A pinned-but-not-active artifact is not ready, and the entry does not
    // claim the operator can continue before the artifact is in place.
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-cpa-import-ready' })).toHaveLength(
      0
    );

    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'artifact-acquire-btn' }).props.onClick();
    });
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-cpa-import-ready' })).toHaveLength(
      1
    );
    expect(JSON.stringify(renderer.toJSON())).toContain('upgrade cpa-import');
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
      // One request is held open so the page can be hidden while it is still in
      // flight: the artifact read itself, or the upload PUT, which has no
      // deadline of its own and is cancelled only by the session lifecycle.
      let held: RequestInit | undefined;
      let phase: 'hanging' | 'reauthenticated' = 'hanging';
      const reauthArtifactReads: RequestInit[] = [];

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
          if (url.endsWith('/runtime/artifact') && phase === 'reauthenticated') {
            reauthArtifactReads.push(init ?? {});
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
            held = init;
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
        expect(
          renderer.root.findAllByProps({ 'data-testid': 'fresh-artifact-step' })
        ).toHaveLength(1);
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
        expect(held?.method).toBe('PUT');
        // Upload has no 15s deadline of its own; only the session lifecycle can
        // abort it, so the timer must not cancel it on its own.
        await act(async () => {
          await vi.advanceTimersByTimeAsync(15_000);
        });
        expect(held?.signal?.aborted).toBe(false);
      } else {
        expect(held?.method).toBe('GET');
      }
      expect(held?.signal).toBeDefined();

      const pagehideCalls = vi
        .mocked(window.addEventListener)
        .mock.calls.filter(([name]) => name === 'pagehide');
      const onPagehide = pagehideCalls[pagehideCalls.length - 1]?.[1] as () => void;
      expect(onPagehide).toBeTypeOf('function');
      await act(async () => {
        onPagehide();
      });
      // The pending artifact request is cancelled and the entry is back on the
      // login shell; the retained credential is not what pagehide discards.
      expect(renderer.root.findAllByType(FullWebLoginPage)).toHaveLength(1);
      expect(held?.signal?.aborted).toBe(true);

      phase = 'reauthenticated';
      await act(async () => {
        await adapter().authenticate('review-source-admin-reauth', false);
      });
      for (let i = 0; i < 3; i++) {
        await act(async () => {
          await Promise.resolve();
        });
      }
      // A fresh session controller owns the re-authenticated artifact read, so
      // a request made after re-verification is live rather than inheriting the
      // retired envelope's cancellation.
      expect(reauthArtifactReads).toHaveLength(1);
      expect(reauthArtifactReads[0].signal?.aborted).toBe(false);
      expect(held?.signal?.aborted).toBe(true);
      expect(
        renderer.root.findAllByProps({ 'data-testid': 'upgrade-cpa-import-ready' })
      ).toHaveLength(1);
    }
  );

  it('parses real 403 origin_rejected responses and names the canonical console URL across locales', async () => {
    mocks.verify.mockImplementation((credential: string, signal: AbortSignal, source?: string) =>
      mocks.realVerify!(credential, signal, source)
    );
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);

    await act(async () => {
      renderer = create(<UpgradeAuthenticationEntry />);
    });

    fetchMock.mockResolvedValueOnce(
      Response.json(
        {
          contract_version: UPGRADE_AUTH_CONTRACT,
          error: { code: 'surface_rejected' },
        },
        { status: 403 }
      )
    );
    await act(async () => {
      await adapter().authenticate('old-key', false);
    });
    const surfaceErrorByLocale: Record<string, string | null> = {};
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru'] as const) {
      await act(async () => {
        await loginI18n.changeLanguage(locale);
      });
      surfaceErrorByLocale[locale] = adapter().error;
    }

    fetchMock.mockResolvedValueOnce(
      Response.json(
        {
          contract_version: UPGRADE_AUTH_CONTRACT,
          error: {
            code: 'origin_rejected',
            expected_origin: 'https://public-manager.test',
          },
        },
        { status: 403 }
      )
    );
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
      expect(adapter().error).not.toBe(surfaceErrorByLocale[locale]);
    }

    fetchMock.mockResolvedValueOnce(
      Response.json(
        {
          contract_version: UPGRADE_AUTH_CONTRACT,
          error: { code: 'origin_rejected' },
        },
        { status: 403 }
      )
    );
    await act(async () => {
      await adapter().authenticate('old-key', false);
    });
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru'] as const) {
      await act(async () => {
        await loginI18n.changeLanguage(locale);
      });
      expect(adapter().error).toContain('CPAMP_PUBLIC_ORIGIN');
      expect(adapter().error).not.toContain('undefined');
      expect(adapter().error).not.toContain('/.well-known/cpamp-bootstrap/ui');
      expect(adapter().error).not.toBe(surfaceErrorByLocale[locale]);
    }
  });

  it('classifies 503 source_ownership_unavailable distinctly from generic 503 states and keeps unauthenticated alert state across locales', async () => {
    mocks.verify.mockImplementation((credential: string, signal: AbortSignal, source?: string) =>
      mocks.realVerify!(credential, signal, source)
    );
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);

    for (const tc of [
      {
        name: 'typed source_ownership_unavailable envelope',
        response: () =>
          Response.json(
            {
              contract_version: UPGRADE_AUTH_CONTRACT,
              error: { code: 'source_ownership_unavailable' },
            },
            { status: 503 }
          ),
        expectedReason: 'source_ownership_unavailable',
      },
      {
        name: 'generic source_unavailable envelope',
        response: () =>
          Response.json(
            {
              contract_version: UPGRADE_AUTH_CONTRACT,
              error: { code: 'source_unavailable' },
            },
            { status: 503 }
          ),
        expectedReason: 'unavailable',
      },
      {
        name: 'mismatched contract version on 503',
        response: () =>
          Response.json(
            {
              contract_version: 'unexpected.contract.v0',
              error: { code: 'source_ownership_unavailable' },
            },
            { status: 503 }
          ),
        expectedReason: 'unavailable',
      },
      {
        name: 'non-JSON 503 upstream body',
        response: () => new Response('service unavailable', { status: 503 }),
        expectedReason: 'unavailable',
      },
    ] as const) {
      fetchMock.mockResolvedValueOnce(tc.response());
      const pending = mocks.realVerify!('old-key', new AbortController().signal);
      await expect(pending, tc.name).rejects.toBeInstanceOf(UpgradeAuthenticationError);
      await expect(pending, tc.name).rejects.toMatchObject({
        reason: tc.expectedReason,
      });
    }

    await act(async () => {
      renderer = create(<UpgradeAuthenticationEntry />);
    });

    fetchMock.mockResolvedValueOnce(
      Response.json(
        {
          contract_version: UPGRADE_AUTH_CONTRACT,
          error: { code: 'source_unavailable' },
        },
        { status: 503 }
      )
    );
    let ok = true;
    await act(async () => {
      ok = await adapter().authenticate('old-key', true);
    });
    expect(ok).toBe(false);
    const genericUnavailableByLocale: Record<string, string | null> = {};
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru'] as const) {
      await act(async () => {
        await loginI18n.changeLanguage(locale);
      });
      genericUnavailableByLocale[locale] = adapter().error;
    }

    fetchMock.mockResolvedValueOnce(
      Response.json(
        {
          contract_version: UPGRADE_AUTH_CONTRACT,
          error: { code: 'admin_unauthorized' },
        },
        { status: 401 }
      )
    );
    await act(async () => {
      ok = await adapter().authenticate('old-key', true);
    });
    expect(ok).toBe(false);
    const invalidByLocale: Record<string, string | null> = {};
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru'] as const) {
      await act(async () => {
        await loginI18n.changeLanguage(locale);
      });
      invalidByLocale[locale] = adapter().error;
    }

    fetchMock.mockResolvedValueOnce(
      Response.json(
        {
          contract_version: UPGRADE_AUTH_CONTRACT,
          error: { code: 'source_ownership_unavailable' },
        },
        { status: 503 }
      )
    );
    await act(async () => {
      ok = await adapter().authenticate('old-key', true);
    });
    expect(ok).toBe(false);
    expect(mocks.remember).not.toHaveBeenCalled();

    const ownershipErrorByLocale: Record<string, string | null> = {};
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru'] as const) {
      await act(async () => {
        await loginI18n.changeLanguage(locale);
      });
      const message = adapter().error;
      ownershipErrorByLocale[locale] = message;
      expect(message).toBeTruthy();
      expect(message).not.toBe(genericUnavailableByLocale[locale]);
      expect(message).not.toBe(invalidByLocale[locale]);
      expect(message).toContain('10002:10002');
      expect(message).toContain('0:0');
    }
    expect(ownershipErrorByLocale['zh-CN']).not.toBe(ownershipErrorByLocale['zh-TW']);
    expect(ownershipErrorByLocale['ru']).toMatch(/[\u0400-\u04ff]/);
  });

  it('offers the first-upgrade Managed prefix editor on the entry and records what it saved without rendering the key', async () => {
    mocks.verify.mockResolvedValueOnce({
      revision: 'e'.repeat(64),
      observation: 'observation_unavailable',
    });
    const adminSecret = 'old-upgrade-admin-key';
    // The first-upgrade Managed row: no routing committed yet, so the wizard
    // offers the dual-prefix editor whose suggestions the server generated.
    const firstUpgrade = {
      contractVersion: 'cpamp.upgrade-wizard.v1',
      recorded: true,
      stage: 'cpa_import',
      stageKnown: true,
      nextAction: 'verify',
      domainsDone: ['source_copy', 'cpa_config'],
      sourceKind: 'cpamp_1x',
      sourceVersion: 'v1.14.4',
      manualItems: [],
      errorCode: '',
      errorDetail: '',
      targetMode: 'managed',
      routing: {
        configured: false,
        committed: false,
        existingPrefixesPreserved: false,
        controlBasePath: '',
        inferenceBasePath: '',
        sharedEntry: false,
        externalBaseUrl: '',
        suggestedControlBasePath: '/ctl1234567890abcd',
        suggestedInferenceBasePath: '/inf1234567890abcd',
      },
      reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
    };
    let writes = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown) => {
        const url = String(input);
        if (url.endsWith('/installation/upgrade/routing')) {
          writes += 1;
          return Response.json({
            ...firstUpgrade,
            routing: {
              ...firstUpgrade.routing,
              configured: true,
              controlBasePath: '/ctl1234567890abcd',
              inferenceBasePath: '/inf1234567890abcd',
            },
          });
        }
        if (url.endsWith('/installation/upgrade/status')) {
          return Response.json(firstUpgrade);
        }
        return Response.json({});
      })
    );
    await act(async () => {
      renderer = create(<UpgradeAuthenticationEntry />);
    });
    await act(async () => {
      await adapter().authenticate(adminSecret, false);
    });
    for (let i = 0; i < 4; i++) {
      await act(async () => {
        await Promise.resolve();
      });
    }

    // The editor is initialized from the recorded suggestions, and the page
    // never renders the key it holds in its ref.
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-control-prefix-input' }).props.value).toBe(
      '/ctl1234567890abcd'
    );
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-inference-prefix-input' }).props.value
    ).toBe('/inf1234567890abcd');
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'upgrade-routing-saved' })
    ).toHaveLength(0);
    expect(JSON.stringify(renderer.toJSON())).not.toContain(adminSecret);

    await act(async () => {
      await renderer.root.findByProps({ 'data-testid': 'upgrade-save-routing' }).props.onClick();
    });

    // One write, reported as the save's own outcome, and the key still never
    // appears in the page the operator sees.
    expect(writes).toBe(1);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'upgrade-routing-saved' })
    ).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-routing-error' })).toHaveLength(0);
    expect(JSON.stringify(renderer.toJSON())).not.toContain(adminSecret);
  });
});
