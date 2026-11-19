import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { UpgradeAuthenticationEntry } from './UpgradeAuthenticationEntry';
import { FreshUpgradeFlow } from './FreshUpgradeFlow';
import { FullWebLoginPage } from './FullWebLoginPage';
import {
  UPGRADE_AUTH_CONTRACT,
  UPGRADE_COMMITTED_STATUS_KEY,
  UpgradeAuthenticationError,
  type verifyUpgradeCredential,
} from './upgradeSession';
import { isNonTransientArtifactReadError, type ArtifactTransport } from './freshArtifact';
import { upgradeEntryText } from '../i18n/upgradeEntry';
import { upgradeWizardText } from '../i18n/upgradeWizard';
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

function activatedRoute(url: string, init?: RequestInit) {
  const identity = { installationId: 'install', generation: 'generation', operationId: 'operation' };
  const routing = { operationId: identity.operationId, target: '/ctl-next', inferenceTarget: '/inf-next', revision: 3, committed: true };
  const authorization = new Headers(init?.headers).get('Authorization');
  switch (url) {
    case 'https://manager.test/ctl-next/api/v2/bootstrap/identity':
      expect(authorization).toBeNull();
      return Response.json({ ...identity, ...routing }, { headers: { 'X-CPAMP-Route-Owner': 'manager' } });
    case 'https://manager.test/ctl-next/api/v2/operation':
      expect(authorization).toBe('Bearer old-key');
      return Response.json({ adminInitialized: true, productComplete: true, identity, routing });
    case 'https://manager.test/ctl-next/api/v2/api-keys/access-info':
      expect(authorization).toBe('Bearer old-key');
      return Response.json({ mode: 'migrate_legacy', managementBasePath: '/ctl-next', inferenceBasePath: '/inf-next', protocols: [], ready: true });
  }
}

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
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-entry-status' }).props['data-observation']
    ).toBe('unsupported_upgrade');
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-result-complete' })).toHaveLength(
      0
    );
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
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-progress' })).toHaveLength(1);
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
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-result-complete' })).toHaveLength(
      0
    );
    expect(JSON.stringify(renderer.toJSON())).not.toContain('docker run');
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

  it('renews administrator identity while a worker job executes and preserves session on 503 unavailable', async () => {
    mocks.verify.mockResolvedValue({
      revision: 'f'.repeat(64),
      observation: 'observation_unavailable',
    });
    const runningStatus = {
      contractVersion: 'cpamp.upgrade-wizard.v1',
      recorded: true,
      stage: 'reconcile',
      stageKnown: true,
      nextAction: 'cpa_import',
      domainsDone: ['source_copy'],
      sourceKind: 'cpamp_1x',
      sourceVersion: 'v1.14.10',
      manualItems: [],
      errorCode: '',
      errorDetail: '',
      reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
      execution: {
        available: true,
        running: true,
        action: 'cpa-import',
        code: '',
        managedAvailable: true,
      },
    };
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown) => {
        const url = String(input);
        if (url.endsWith('/installation/upgrade/status')) {
          return Response.json(runningStatus);
        }
        return Response.json({});
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
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-verify' }).props.disabled).toBe(
      true
    );
    mocks.verify.mockRejectedValueOnce(new UpgradeAuthenticationError('unavailable'));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(mocks.verify).toHaveBeenCalledTimes(2);
    expect(mocks.verify).toHaveBeenLastCalledWith('old-key', expect.any(AbortSignal), 'f'.repeat(64));
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-shell' })).toHaveLength(1);
    expect(renderer.root.findAllByType(FullWebLoginPage)).toHaveLength(0);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(mocks.verify).toHaveBeenCalledTimes(3);
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-shell' })).toHaveLength(1);

    mocks.verify.mockRejectedValueOnce(new UpgradeAuthenticationError('invalid'));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(renderer.root.findAllByType(FullWebLoginPage)).toHaveLength(1);
  });

  it('checks canonical readiness after activation and reverts if the worker reports restart failure', async () => {
    mocks.verify.mockResolvedValue({
      revision: '9'.repeat(64),
      observation: 'observation_unavailable',
    });
    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/ctl-next',
      inferenceBasePath: '/inf-next',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const pendingStatus = {
      contractVersion: 'cpamp.upgrade-wizard.v1',
      recorded: true,
      stage: 'complete',
      stageKnown: true,
      nextAction: 'complete',
      domainsDone: ['source_copy', 'cpa_config', 'identity'],
      sourceKind: 'cpamp_1x',
      sourceVersion: 'v1.14.10',
      manualItems: [],
      errorCode: '',
      errorDetail: '',
      targetMode: 'managed',
      routing,
      accessInfo: {
        mode: 'managed',
        managementBasePath: '/ctl-next',
        inferenceBasePath: '/inf-next',
        protocols: [],
        ready: false,
        code: 'upgrade_restart_required',
      },
      reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
      execution: {
        available: true,
        running: false,
        action: 'verify',
        code: '',
        managedAvailable: true,
      },
    };
    let canonicalPolls = 0;
    let restartFailedMode = false;
    let activateAccepted = false;
    const fetchMock = vi.fn(async (input: unknown, init?: RequestInit) => {
      const url = String(input);
      if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/execute') {
        activateAccepted = true;
        return Response.json({
          contractVersion: 'cpamp.upgrade-execution.v1',
          available: true,
          running: false,
          action: 'activate',
          code: '',
          managedAvailable: true,
        });
      }
      if (url === 'https://manager.test/ctl-next/api/v2/bootstrap/identity') {
        canonicalPolls += 1;
        if (restartFailedMode) {
          return Response.json({ error: { code: 'not_found' } }, { status: 404 });
        }
        if (canonicalPolls <= 2) {
          return Response.json({ error: { code: 'upgrade_restart_required' } }, { status: 503 });
        }
      }
      if (url.startsWith('https://manager.test/ctl-next/api/v2/')) {
        const reply = activatedRoute(url, init);
        if (!reply) throw new Error(`Activation used a retired route: ${url}`);
        return reply;
      }
      if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/status') {
        if (restartFailedMode) {
          return Response.json({
            ...pendingStatus,
            execution: {
              available: true,
              running: false,
              action: 'activate',
              code: 'upgrade_restart_failed',
              managedAvailable: true,
            },
          });
        }
        return Response.json({
          ...pendingStatus,
          execution: {
            ...pendingStatus.execution,
            action: activateAccepted ? 'activate' : 'verify',
          },
        });
      }
      return Response.json({});
    });
    vi.stubGlobal('fetch', fetchMock);
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

    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-open-console' })).toHaveLength(0);
    await act(async () => {
      await renderer.root.findByProps({ 'data-testid': 'upgrade-execute-activate' }).props.onClick();
    });
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-verify' }).props.disabled).toBe(true);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(35_000);
    });
    expect(mocks.verify).toHaveBeenCalledTimes(1);
    expect(canonicalPolls).toBeGreaterThanOrEqual(3);
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-open-console' }).props.href
    ).toBe('https://manager.test/ctl-next');

    await act(async () => renderer.unmount());

    restartFailedMode = true;
    canonicalPolls = 0;
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
    await act(async () => {
      await renderer.root.findByProps({ 'data-testid': 'upgrade-execute-activate' }).props.onClick();
    });
    for (let i = 0; i < 6; i++) {
      await act(async () => {
        await Promise.resolve();
      });
    }
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-execution-status-error' })
    ).toBeTruthy();
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'upgrade-result-complete' })
    ).toHaveLength(0);
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-result-incomplete' })
    ).toBeTruthy();
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-execute-activate' }).props.disabled
    ).toBe(false);
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-verify' }).props.disabled).toBe(
      false
    );
  });

  it('recovers bootstrap execution route on unreceived activate network drop and after 120s activation timeout for explicit retry', async () => {
    mocks.verify.mockResolvedValue({
      revision: '7'.repeat(64),
      observation: 'observation_unavailable',
    });
    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/ctl-next',
      inferenceBasePath: '/inf-next',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const pendingStatus = {
      contractVersion: 'cpamp.upgrade-wizard.v1',
      recorded: true,
      stage: 'complete',
      stageKnown: true,
      nextAction: 'complete',
      domainsDone: ['source_copy', 'cpa_config', 'identity'],
      sourceKind: 'cpamp_1x',
      sourceVersion: 'v1.14.10',
      manualItems: [],
      errorCode: '',
      errorDetail: '',
      targetMode: 'managed',
      routing,
      accessInfo: {
        mode: 'managed',
        managementBasePath: '/ctl-next',
        inferenceBasePath: '/inf-next',
        protocols: [],
        ready: false,
        code: 'upgrade_restart_required',
      },
      reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
      execution: {
        available: true,
        running: false,
        action: 'verify',
        code: '',
        managedAvailable: true,
      },
    };

    let mode: 'drop' | 'stalling' | 'ready' = 'drop';
    let bootstrapExecuteCalls = 0;
    let canonicalExecuteCalls = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown, init?: RequestInit) => {
        const url = String(input);
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/execute') {
          bootstrapExecuteCalls += 1;
          if (mode === 'drop') {
            throw new TypeError('network down before worker');
          }
          return Response.json({
            contractVersion: 'cpamp.upgrade-execution.v1',
            available: true,
            running: false,
            action: 'activate',
            code: '',
            managedAvailable: true,
          });
        }
        if (url === 'https://manager.test/ctl-next/api/v2/installation/upgrade/execute') {
          canonicalExecuteCalls += 1;
          return Response.json({ error: { code: 'not_found' } }, { status: 404 });
        }
        if (url.startsWith('https://manager.test/ctl-next/api/v2/')) {
          if (mode !== 'ready') return Response.json({}, { status: 404 });
          const reply = activatedRoute(url, init);
          if (!reply) throw new Error(`Activation used a retired route: ${url}`);
          return reply;
        }
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/status') {
          if (mode === 'stalling') {
            return Response.json({ error: { code: 'upgrade_worker_unavailable' } }, { status: 503 });
          }
          return Response.json(pendingStatus);
        }
        return Response.json({});
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

    // 1. Activate fails with network drop before reaching worker -> canonical 404 probes
    // bootstrap 200 (action: 'verify', no execution.code) and reverts cleanly without auto-retry.
    await act(async () => {
      await renderer.root.findByProps({ 'data-testid': 'upgrade-execute-activate' }).props.onClick();
    });
    for (let i = 0; i < 6; i++) {
      await act(async () => {
        await Promise.resolve();
      });
    }
    expect(bootstrapExecuteCalls).toBe(1);
    expect(canonicalExecuteCalls).toBe(0);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'upgrade-activation-waiting' })
    ).toHaveLength(0);
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-execution-error' })
    ).toBeTruthy();
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'upgrade-open-console' })
    ).toHaveLength(0);
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-execute-activate' }).props.disabled
    ).toBe(false);
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-verify' }).props.disabled).toBe(
      false
    );

    // 2. Explicit retry enters 120s restart window that stalls -> reverts back to bootstrap
    // execution endpoint so subsequent explicit retry does not hit canonical 404.
    mode = 'stalling';
    await act(async () => {
      await renderer.root.findByProps({ 'data-testid': 'upgrade-execute-activate' }).props.onClick();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(122_000);
    });
    expect(bootstrapExecuteCalls).toBe(2);
    expect(canonicalExecuteCalls).toBe(0);
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-result-stalled' })).toBeTruthy();
    expect(
      renderer.root
        .findByProps({ 'data-testid': 'upgrade-execution-error' })
        .findByType('code')
        .children.join('')
    ).toBe('upgrade_activation_timeout');
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-execute-activate' }).props.disabled
    ).toBe(false);
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-verify' }).props.disabled).toBe(
      false
    );

    // 3. Explicit retry after timeout hits bootstrap execute (not canonical 404) and opens console.
    mode = 'ready';
    await act(async () => {
      await renderer.root.findByProps({ 'data-testid': 'upgrade-execute-activate' }).props.onClick();
    });
    for (let i = 0; i < 6; i++) {
      await act(async () => {
        await Promise.resolve();
      });
    }
    expect(bootstrapExecuteCalls).toBe(3);
    expect(canonicalExecuteCalls).toBe(0);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'upgrade-result-stalled' })
    ).toHaveLength(0);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'upgrade-execution-error' })
    ).toHaveLength(0);
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-open-console' }).props.href
    ).toBe('https://manager.test/ctl-next');
  });

  it('falls back to bootstrap on real Ingress 503 pre-cutover, preserves genuine activate in-flight, and handles omitted-action restart failure', async () => {
    mocks.verify.mockResolvedValue({
      revision: '8'.repeat(64),
      observation: 'observation_unavailable',
    });
    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/ctl-next',
      inferenceBasePath: '/inf-next',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const baseStatus = {
      contractVersion: 'cpamp.upgrade-wizard.v1',
      recorded: true,
      stage: 'complete',
      stageKnown: true,
      nextAction: 'complete',
      domainsDone: ['source_copy', 'cpa_config', 'identity'],
      sourceKind: 'cpamp_1x',
      sourceVersion: 'v1.14.10',
      manualItems: [],
      errorCode: '',
      errorDetail: '',
      targetMode: 'managed',
      routing,
      accessInfo: {
        mode: 'managed',
        managementBasePath: '/ctl-next',
        inferenceBasePath: '/inf-next',
        protocols: [],
        ready: false,
        code: 'upgrade_restart_required',
      },
      reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
      execution: {
        available: true,
        running: false,
        action: 'verify',
        code: '',
        managedAvailable: true,
      },
    };

    const replaceState = vi.fn();
    vi.stubGlobal('window', {
      location: new URL('https://manager.test/.well-known/cpamp-bootstrap/ui'),
      history: { replaceState },
      setTimeout,
      clearTimeout,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });

    let scenario: 'in-flight' | 'failed-omitted-action' = 'in-flight';
    let bootstrapStatusAction = 'verify';
    let canonicalPolls = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown, init?: RequestInit) => {
        const url = String(input);
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/execute') {
          bootstrapStatusAction = 'activate';
          return Response.json({
            contractVersion: 'cpamp.upgrade-execution.v1',
            available: true,
            running: false,
            action: 'activate',
            code: '',
            managedAvailable: true,
          });
        }
        if (url === 'https://manager.test/ctl-next/api/v2/bootstrap/identity') {
          canonicalPolls += 1;
          if (scenario === 'failed-omitted-action' || canonicalPolls <= 2) {
            // Real Ingress returns plain-text 503 with gateway owner before cutover.
            return new Response('gateway pending configuration\n', {
              status: 503,
              headers: { 'X-CPAMP-Route-Owner': 'gateway' },
            });
          }
          return activatedRoute(url, init)!;
        }
        if (url.startsWith('https://manager.test/ctl-next/api/v2/')) {
          const reply = activatedRoute(url, init);
          if (!reply) throw new Error(`Unexpected canonical route: ${url}`);
          return reply;
        }
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/status') {
          if (scenario === 'failed-omitted-action' && bootstrapStatusAction === 'activate') {
            return Response.json({
              ...baseStatus,
              execution: {
                available: true,
                running: false,
                code: 'upgrade_restart_failed',
                managedAvailable: true,
              },
            });
          }
          return Response.json({
            ...baseStatus,
            execution: {
              ...baseStatus.execution,
              action: bootstrapStatusAction,
            },
          });
        }
        return Response.json({});
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

    // 1. Genuine activate stays in waiting state with zero error across real Ingress 503 pre-cutover fallback.
    await act(async () => {
      await renderer.root.findByProps({ 'data-testid': 'upgrade-execute-activate' }).props.onClick();
    });
    for (let i = 0; i < 6; i++) {
      await act(async () => {
        await Promise.resolve();
      });
    }
    expect(canonicalPolls).toBe(1);
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-activation-waiting' })
    ).toBeTruthy();
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'upgrade-execution-error' })
    ).toHaveLength(0);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'upgrade-execution-status-error' })
    ).toHaveLength(0);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000);
    });
    expect(canonicalPolls).toBe(2);
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-activation-waiting' })
    ).toBeTruthy();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000);
    });
    expect(canonicalPolls).toBeGreaterThanOrEqual(3);
    expect(replaceState).toHaveBeenCalledWith(null, '', '/ctl-next');
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-open-console' }).props.href
    ).toBe('https://manager.test/ctl-next');

    // 2. Delayed Linux restart failure with omitted action on 503 pre-cutover fallback cleanly reverts to bootstrap.
    await act(async () => renderer.unmount());
    scenario = 'failed-omitted-action';
    canonicalPolls = 0;
    bootstrapStatusAction = 'verify';
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
    await act(async () => {
      await renderer.root.findByProps({ 'data-testid': 'upgrade-execute-activate' }).props.onClick();
    });
    for (let i = 0; i < 6; i++) {
      await act(async () => {
        await Promise.resolve();
      });
    }
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'upgrade-activation-waiting' })
    ).toHaveLength(0);
    expect(
      renderer.root
        .findByProps({ 'data-testid': 'upgrade-execution-status-error' })
        .findByType('code')
        .children.join('')
    ).toBe('upgrade_restart_failed');
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-execute-activate' }).props.disabled
    ).toBe(false);
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-verify' }).props.disabled).toBe(
      false
    );
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'upgrade-open-console' })
    ).toHaveLength(0);
  });

  it('resumes while bootstrap status remains observable and proves canonical independent of gateway readiness', async () => {
    mocks.verify.mockResolvedValue({
      revision: '9'.repeat(64),
      observation: 'observation_unavailable',
    });
    const localSetItem = vi.fn();
    const sessionSetItem = vi.fn();
    vi.stubGlobal('localStorage', { getItem: vi.fn(), setItem: localSetItem, removeItem: vi.fn() });
    vi.stubGlobal('sessionStorage', { getItem: vi.fn(), setItem: sessionSetItem, removeItem: vi.fn() });
    const replaceState = vi.fn();
    vi.stubGlobal('window', {
      location: new URL('https://manager.test/.well-known/cpamp-bootstrap/ui'),
      history: { replaceState },
      setTimeout,
      clearTimeout,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });

    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/ctl-next',
      inferenceBasePath: '/inf-next',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const resumedActivateStatus = {
      contractVersion: 'cpamp.upgrade-wizard.v1',
      recorded: true,
      stage: 'complete',
      stageKnown: true,
      nextAction: 'complete',
      domainsDone: ['source_copy', 'cpa_config', 'identity'],
      sourceKind: 'cpamp_1x',
      sourceVersion: 'v1.14.10',
      manualItems: [],
      errorCode: '',
      errorDetail: '',
      targetMode: 'managed',
      routing,
      accessInfo: {
        mode: 'managed',
        managementBasePath: '/ctl-next',
        inferenceBasePath: '/inf-next',
        protocols: [],
        ready: false,
        code: 'upgrade_restart_required',
      },
      reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
      execution: {
        available: true,
        running: false,
        action: 'activate',
        code: '',
        managedAvailable: true,
      },
    };

    let phase: 'bootstrap-resuming' | 'canonical-gateway-pending' | 'canonical-ready' =
      'bootstrap-resuming';
    let executeCalls = 0;
    let bootstrapReadsAfterProof = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown, init?: RequestInit) => {
        const url = String(input);
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/execute') {
          executeCalls += 1;
          return Response.json({});
        }
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/status') {
          if (phase !== 'bootstrap-resuming') {
            bootstrapReadsAfterProof += 1;
            return Response.json({}, { status: 404 });
          }
          return Response.json(resumedActivateStatus);
        }
        if (url.startsWith('https://manager.test/ctl-next/api/v2/')) {
          if (phase === 'bootstrap-resuming') {
            return new Response('gateway pending configuration\n', {
              status: 503,
              headers: { 'X-CPAMP-Route-Owner': 'gateway' },
            });
          }
          const identity = {
            installationId: 'install',
            generation: 'generation',
            operationId: 'operation',
          };
          const canonicalRouting = {
            operationId: identity.operationId,
            target: '/ctl-next',
            inferenceTarget: '/inf-next',
            revision: 3,
            committed: true,
          };
          if (url === 'https://manager.test/ctl-next/api/v2/bootstrap/identity') {
            return Response.json(
              { ...identity, ...canonicalRouting },
              { headers: { 'X-CPAMP-Route-Owner': 'manager' } }
            );
          }
          if (url === 'https://manager.test/ctl-next/api/v2/operation') {
            expect(new Headers(init?.headers).get('Authorization')).toBe('Bearer old-key');
            return Response.json({
              adminInitialized: true,
              productComplete: true,
              gatewayPending: phase === 'canonical-gateway-pending',
              identity,
              routing: canonicalRouting,
            });
          }
          if (url === 'https://manager.test/ctl-next/api/v2/api-keys/access-info') {
            return Response.json({
              mode: 'migrate_legacy',
              managementBasePath: '/ctl-next',
              inferenceBasePath: '/inf-next',
              protocols: [],
              ready: phase === 'canonical-ready',
              code: phase === 'canonical-ready' ? '' : 'installation_incomplete',
            });
          }
        }
        return Response.json({});
      })
    );

    await act(async () => {
      renderer = create(<UpgradeAuthenticationEntry />);
    });
    await act(async () => {
      await adapter().authenticate('old-key', false);
    });
    for (let i = 0; i < 6; i++) {
      await act(async () => {
        await Promise.resolve();
      });
    }

    // Resumed in-flight activate immediately enters waiting without re-submitting activate.
    expect(executeCalls).toBe(0);
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-activation-waiting' })
    ).toBeTruthy();
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-execute-activate' }).props.disabled
    ).toBe(true);
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-verify' }).props.disabled).toBe(
      true
    );

    // Canonical proof succeeds independently while gatewayPending is still true.
    phase = 'canonical-gateway-pending';
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    expect(replaceState).toHaveBeenCalledWith(null, '', '/ctl-next');
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-open-console' }).props.href
    ).toBe('https://manager.test/ctl-next');
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'upgrade-activation' })
    ).toHaveLength(0);
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-gateway-waiting' })
    ).toBeTruthy();
    expect(
      renderer.root.findByProps({ 'data-testid': 'access-info-not-ready' }).findByProps({ className: 'alert-title' })
        .children.join('')
    ).toBe('Gateway update pending');

    // Advancing past 120s must never time out or revert to retired bootstrap.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(125_000);
    });
    expect(bootstrapReadsAfterProof).toBe(0);
    expect(mocks.verify).toHaveBeenCalledTimes(1);
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-verify' }).props.disabled).toBe(
      true
    );
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'upgrade-execution-error' })
    ).toHaveLength(0);
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-open-console' }).props.href
    ).toBe('https://manager.test/ctl-next');

    // Gateway clears -> completion flips to Ready without keys in recovery copy.
    phase = 'canonical-ready';
    await act(async () => {
      await vi.advanceTimersByTimeAsync(15_500);
    });
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'upgrade-gateway-waiting' })
    ).toHaveLength(0);
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-access-active' })).toBeTruthy();
    expect(renderer.root.findByProps({ 'data-testid': 'access-info-copy-access' })).toBeTruthy();
    expect(bootstrapReadsAfterProof).toBe(0);
    expect(localSetItem).not.toHaveBeenCalled();
    expect(sessionSetItem).toHaveBeenCalled();
    for (const [storageKey, serialized] of sessionSetItem.mock.calls) {
      expect(storageKey).toBe(UPGRADE_COMMITTED_STATUS_KEY);
      expect(String(serialized)).not.toContain('old-key');
      expect(String(serialized)).not.toContain('"execution"');
    }

    // External mode canonical proof with external_target_unavailable preserves External semantics.
    await act(async () => renderer.unmount());
    replaceState.mockClear();
    const extRouting = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/ext-ctl',
      inferenceBasePath: '',
      sharedEntry: false,
      externalBaseUrl: 'https://cpa.remote.example',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    let extPhase: 'bootstrap' | 'canonical' = 'bootstrap';
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown) => {
        const url = String(input);
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/status') {
          return Response.json({
            ...resumedActivateStatus,
            targetMode: 'external',
            routing: extRouting,
            accessInfo: {
              mode: 'connect_external',
              managementBasePath: '/ext-ctl',
              inferenceBasePath: '',
              protocols: [],
              ready: false,
              code: 'upgrade_restart_required',
            },
          });
        }
        if (url === 'https://manager.test/ext-ctl/api/v2/bootstrap/identity') {
          if (extPhase === 'bootstrap') return Response.json({}, { status: 503 });
          return Response.json(
            {
              installationId: 'install',
              generation: 'generation',
              operationId: 'operation',
              target: '/ext-ctl',
              inferenceTarget: '',
              revision: 4,
              committed: true,
            },
            { headers: { 'X-CPAMP-Route-Owner': 'manager' } }
          );
        }
        if (url === 'https://manager.test/ext-ctl/api/v2/operation') {
          return Response.json({
            adminInitialized: true,
            productComplete: true,
            gatewayPending: false,
            identity: {
              installationId: 'install',
              generation: 'generation',
              operationId: 'operation',
            },
            routing: {
              operationId: 'operation',
              target: '/ext-ctl',
              inferenceTarget: '',
              sharedEntry: false,
              revision: 4,
              committed: true,
            },
          });
        }
        if (url === 'https://manager.test/ext-ctl/api/v2/api-keys/access-info') {
          return Response.json({
            mode: 'connect_external',
            managementBasePath: '/ext-ctl',
            inferenceBasePath: '',
            protocols: [],
            ready: false,
            code: 'external_target_unavailable',
          });
        }
        return Response.json({});
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
    extPhase = 'canonical';
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    expect(replaceState).toHaveBeenCalledWith(null, '', '/ext-ctl');
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-open-console' }).props.href
    ).toBe('https://manager.test/ext-ctl');
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'upgrade-gateway-waiting' })
    ).toHaveLength(0);
    expect(
      renderer.root.findByProps({ 'data-testid': 'access-info-not-ready' }).findByProps({ className: 'alert-title' })
        .children.join('')
    ).toBe('External CPA target unavailable');
  });

  it('prevents reversion to retired bootstrap after timeout when bootstrap returns 404', async () => {
    mocks.verify.mockResolvedValue({
      revision: 'b'.repeat(64),
      observation: 'observation_unavailable',
    });
    const replaceState = vi.fn();
    vi.stubGlobal('window', {
      location: new URL('https://manager.test/.well-known/cpamp-bootstrap/ui'),
      history: { replaceState },
      setTimeout,
      clearTimeout,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });
    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/ctl-next',
      inferenceBasePath: '/inf-next',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const pendingStatus = {
      contractVersion: 'cpamp.upgrade-wizard.v1',
      recorded: true,
      stage: 'complete',
      stageKnown: true,
      nextAction: 'complete',
      domainsDone: ['source_copy', 'cpa_config', 'identity'],
      sourceKind: 'cpamp_1x',
      sourceVersion: 'v1.14.10',
      manualItems: [],
      errorCode: '',
      errorDetail: '',
      targetMode: 'managed',
      routing,
      accessInfo: {
        mode: 'managed',
        managementBasePath: '/ctl-next',
        inferenceBasePath: '/inf-next',
        protocols: [],
        ready: false,
        code: 'upgrade_restart_required',
      },
      reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
      execution: {
        available: true,
        running: false,
        action: 'verify',
        code: '',
        managedAvailable: true,
      },
    };

    let bootstrapRetired = false;
    let canonicalReady = false;
    let bootstrapCallsAfterRetired = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown, init?: RequestInit) => {
        const url = String(input);
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/execute') {
          if (bootstrapRetired) {
            bootstrapCallsAfterRetired += 1;
            return Response.json({}, { status: 404 });
          }
          bootstrapRetired = true;
          return Response.json({
            contractVersion: 'cpamp.upgrade-execution.v1',
            available: true,
            running: false,
            action: 'activate',
            code: '',
            managedAvailable: true,
          });
        }
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/status') {
          if (!bootstrapRetired) return Response.json(pendingStatus);
          bootstrapCallsAfterRetired += 1;
          return Response.json({}, { status: 404 });
        }
        if (url.startsWith('https://manager.test/ctl-next/api/v2/')) {
          if (!canonicalReady) {
            return new Response('gateway pending configuration\n', {
              status: 503,
              headers: { 'X-CPAMP-Route-Owner': 'gateway' },
            });
          }
          return activatedRoute(url, init)!;
        }
        return Response.json({});
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

    await act(async () => {
      await renderer.root.findByProps({ 'data-testid': 'upgrade-execute-activate' }).props.onClick();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(125_000);
    });

    // Only the initial fallback probe observed 404; subsequent polls and timeout never hit retired bootstrap.
    expect(bootstrapCallsAfterRetired).toBe(1);
    expect(mocks.verify).toHaveBeenCalledTimes(1);
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-verify' }).props.disabled).toBe(
      true
    );
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'upgrade-execute-activate' })
    ).toHaveLength(0);
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-bootstrap-retired-notice' })
    ).toBeTruthy();

    // Rereading after canonical comes up uses canonical endpoints only and succeeds.
    canonicalReady = true;
    await act(async () => {
      await renderer.root.findByProps({ 'data-testid': 'upgrade-result-reread' }).props.onClick();
    });
    for (let i = 0; i < 6; i++) {
      await act(async () => {
        await Promise.resolve();
      });
    }
    expect(bootstrapCallsAfterRetired).toBe(1);
    expect(replaceState).toHaveBeenCalledWith(null, '', '/ctl-next');
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-open-console' }).props.href
    ).toBe('https://manager.test/ctl-next');
  });

  it('keeps polling through upgrade_worker_unavailable shutdown transition and supports post-cutover logout navigation', async () => {
    mocks.verify.mockResolvedValue({
      revision: 'c'.repeat(64),
      observation: 'observation_unavailable',
    });
    const sessionMap = new Map<string, string>();
    vi.stubGlobal('sessionStorage', {
      getItem: (k: string) => sessionMap.get(k) ?? null,
      setItem: (k: string, v: string) => void sessionMap.set(k, v),
      removeItem: (k: string) => void sessionMap.delete(k),
    });
    const replaceState = vi.fn();
    const assign = vi.fn();
    vi.stubGlobal('window', {
      location: {
        origin: 'https://manager.test',
        pathname: '/.well-known/cpamp-bootstrap/ui',
        href: 'https://manager.test/.well-known/cpamp-bootstrap/ui',
        assign,
      },
      history: { replaceState },
      setTimeout,
      clearTimeout,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });
    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/ctl-next',
      inferenceBasePath: '/inf-next',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const baseStatus = {
      contractVersion: 'cpamp.upgrade-wizard.v1',
      recorded: true,
      stage: 'complete',
      stageKnown: true,
      nextAction: 'complete',
      domainsDone: ['source_copy', 'cpa_config', 'identity'],
      sourceKind: 'cpamp_1x',
      sourceVersion: 'v1.14.10',
      manualItems: [],
      errorCode: '',
      errorDetail: '',
      targetMode: 'managed',
      routing,
      accessInfo: {
        mode: 'managed',
        managementBasePath: '/ctl-next',
        inferenceBasePath: '/inf-next',
        protocols: [],
        ready: false,
        code: 'upgrade_restart_required',
      },
      reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
    };

    let phase: 'verify-done' | 'worker-shutdown' | 'canonical' = 'verify-done';
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown, init?: RequestInit) => {
        const url = String(input);
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/execute') {
          phase = 'worker-shutdown';
          return Response.json(
            {
              contractVersion: 'cpamp.upgrade-execution.v1',
              available: true,
              running: true,
              action: 'activate',
              managedAvailable: true,
            },
            { status: 202 }
          );
        }
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/status') {
          return Response.json({
            ...baseStatus,
            execution:
              phase === 'verify-done'
                ? { available: true, running: false, action: 'verify', code: '', managedAvailable: true }
                : {
                    available: false,
                    running: false,
                    code: 'upgrade_worker_unavailable',
                    managedAvailable: false,
                  },
          });
        }
        if (url.startsWith('https://manager.test/ctl-next/api/v2/')) {
          if (phase !== 'canonical') {
            return Response.json({}, { status: 503 });
          }
          return activatedRoute(url, init)!;
        }
        return Response.json({});
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

    await act(async () => {
      await renderer.root.findByProps({ 'data-testid': 'upgrade-execute-activate' }).props.onClick();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_500);
    });

    // Shutdown worker unavailability keeps Step 4 in activating wait without flipping to incomplete.
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-result-complete' })).toBeTruthy();
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'upgrade-result-incomplete' })
    ).toHaveLength(0);
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-activation-waiting' })).toBeTruthy();
    expect(sessionMap.has(UPGRADE_COMMITTED_STATUS_KEY)).toBe(true);

    phase = 'canonical';
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_500);
    });
    expect(replaceState).toHaveBeenCalledWith(null, '', '/ctl-next');
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-open-console' }).props.href
    ).toBe('https://manager.test/ctl-next');

    // Post-cutover Logout cleans storage and navigates directly to the canonical prefix.
    const logoutLabel = upgradeEntryText('logout', 'en');
    const logoutBtn = renderer.root.find(
      (node) => node.type === 'button' && node.children.join('') === logoutLabel
    );
    await act(async () => {
      logoutBtn.props.onClick();
    });
    expect(mocks.remember).toHaveBeenCalledWith('', false);
    expect(sessionMap.has(UPGRADE_COMMITTED_STATUS_KEY)).toBe(false);
    expect(assign).toHaveBeenCalledWith('/ctl-next');
  });

  it('recovers across reload when bootstrap returns 404 and Ingress transiently returns 503 using persisted sessionStorage snapshot', async () => {
    mocks.verify.mockResolvedValue({
      revision: 'd'.repeat(64),
      observation: 'observation_unavailable',
    });
    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/ctl-next',
      inferenceBasePath: '/inf-next',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const snapshot = {
      contractVersion: 'cpamp.upgrade-wizard.v1',
      recorded: true,
      stage: 'complete',
      stageKnown: true,
      nextAction: 'complete',
      domainsDone: ['source_copy', 'cpa_config', 'identity'],
      sourceKind: 'cpamp_1x',
      sourceVersion: 'v1.14.10',
      manualItems: [],
      errorCode: '',
      errorDetail: '',
      targetMode: 'managed',
      routing,
      accessInfo: {
        mode: 'migrate_legacy',
        managementBasePath: '/ctl-next',
        inferenceBasePath: '/inf-next',
        protocols: [],
        ready: false,
        code: 'upgrade_restart_required',
      },
      reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
    };
    const sessionMap = new Map<string, string>([
      [
        UPGRADE_COMMITTED_STATUS_KEY,
        JSON.stringify({
          controlBasePath: '/ctl-next',
          revision: 'd'.repeat(64),
          activationRequested: true,
          snapshot,
        }),
      ],
    ]);
    vi.stubGlobal('sessionStorage', {
      getItem: (k: string) => sessionMap.get(k) ?? null,
      setItem: (k: string, v: string) => void sessionMap.set(k, v),
      removeItem: (k: string) => void sessionMap.delete(k),
    });
    const replaceState = vi.fn();
    vi.stubGlobal('window', {
      location: new URL('https://manager.test/.well-known/cpamp-bootstrap/ui'),
      history: { replaceState },
      setTimeout,
      clearTimeout,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });

    let ingressReady = false;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown, init?: RequestInit) => {
        const url = String(input);
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/status') {
          return Response.json({}, { status: 404 });
        }
        if (url.startsWith('https://manager.test/ctl-next/api/v2/')) {
          if (!ingressReady) return Response.json({}, { status: 503 });
          return activatedRoute(url, init)!;
        }
        return Response.json({});
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

    // Must enter activation wait instead of failing boot or stalling after 3 transient 503 polls.
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-boot-failed' })).toHaveLength(0);
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-activation-waiting' })).toBeTruthy();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(12_000);
    });
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-result-stalled' })).toHaveLength(0);
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-activation-waiting' })).toBeTruthy();

    ingressReady = true;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(replaceState).toHaveBeenCalledWith(null, '', '/ctl-next');
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-open-console' }).props.href
    ).toBe('https://manager.test/ctl-next');
  });

  it('does not mark canonicalObserved when operation returns 401 and surfaces authentication failure cleanly', async () => {
    mocks.verify.mockResolvedValue({
      revision: 'e'.repeat(64),
      observation: 'observation_unavailable',
    });
    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/ctl-next',
      inferenceBasePath: '/inf-next',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const sessionMap = new Map<string, string>();
    vi.stubGlobal('sessionStorage', {
      getItem: (k: string) => sessionMap.get(k) ?? null,
      setItem: (k: string, v: string) => void sessionMap.set(k, v),
      removeItem: (k: string) => void sessionMap.delete(k),
    });
    const replaceState = vi.fn();
    const assign = vi.fn();
    vi.stubGlobal('window', {
      location: {
        origin: 'https://manager.test',
        pathname: '/.well-known/cpamp-bootstrap/ui',
        href: 'https://manager.test/.well-known/cpamp-bootstrap/ui',
        assign,
      },
      history: { replaceState },
      setTimeout,
      clearTimeout,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown) => {
        const url = String(input);
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/status') {
          return Response.json({
            contractVersion: 'cpamp.upgrade-wizard.v1',
            recorded: true,
            stage: 'complete',
            stageKnown: true,
            nextAction: 'complete',
            domainsDone: ['source_copy', 'cpa_config', 'identity'],
            sourceKind: 'cpamp_1x',
            sourceVersion: 'v1.14.10',
            manualItems: [],
            errorCode: '',
            errorDetail: '',
            targetMode: 'managed',
            routing,
            accessInfo: {
              mode: 'migrate_legacy',
              managementBasePath: '/ctl-next',
              inferenceBasePath: '/inf-next',
              protocols: [],
              ready: false,
              code: 'upgrade_restart_required',
            },
            reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
            execution: { available: true, running: true, action: 'activate', code: '', managedAvailable: true },
          });
        }
        if (url === 'https://manager.test/ctl-next/api/v2/bootstrap/identity') {
          return Response.json(
            {
              installationId: 'install',
              generation: 'generation',
              operationId: 'operation',
              target: '/ctl-next',
              inferenceTarget: '/inf-next',
              revision: 3,
              committed: true,
            },
            { headers: { 'X-CPAMP-Route-Owner': 'manager' } }
          );
        }
        if (url === 'https://manager.test/ctl-next/api/v2/operation') {
          return Response.json({}, { status: 401 });
        }
        return Response.json({});
      })
    );

    await act(async () => {
      renderer = create(<UpgradeAuthenticationEntry />);
    });
    await act(async () => {
      await adapter().authenticate('old-key', false);
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(25_000);
    });

    expect(replaceState).not.toHaveBeenCalled();
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-open-console' })).toHaveLength(0);
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-execution-error' }).findByType('p').children.join('')
    ).toBe(upgradeWizardText('errorAuthentication', 'en'));

    const logoutLabel = upgradeEntryText('logout', 'en');
    const logoutBtn = renderer.root.find(
      (node) => node.type === 'button' && node.children.join('') === logoutLabel
    );
    await act(async () => {
      logoutBtn.props.onClick();
    });
    expect(sessionMap.has(UPGRADE_COMMITTED_STATUS_KEY)).toBe(false);
    expect(assign).toHaveBeenCalledWith('/ctl-next');
  });

  it('evicts stale or un-revisioned persisted committed status on new-revision login and clears storage when opening console', async () => {
    const routingOld = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/ctl-old',
      inferenceBasePath: '/inf-old',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const oldSnapshot = {
      contractVersion: 'cpamp.upgrade-wizard.v1',
      recorded: true,
      stage: 'complete',
      stageKnown: true,
      nextAction: 'complete',
      domainsDone: ['source_copy', 'cpa_config', 'identity'],
      sourceKind: 'cpamp_1x',
      sourceVersion: 'v1.14.10',
      manualItems: [],
      errorCode: '',
      errorDetail: '',
      targetMode: 'managed',
      routing: routingOld,
      accessInfo: {
        mode: 'migrate_legacy',
        managementBasePath: '/ctl-old',
        inferenceBasePath: '/inf-old',
        protocols: [],
        ready: false,
        code: 'upgrade_restart_required',
      },
      reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
    };

    const revOld = '1'.repeat(64);
    const revNew = '2'.repeat(64);
    mocks.verify.mockResolvedValue({
      revision: revNew,
      observation: 'observation_unavailable',
    });

    const sessionMap = new Map<string, string>([
      [
        UPGRADE_COMMITTED_STATUS_KEY,
        JSON.stringify({
          controlBasePath: '/ctl-old',
          revision: revOld,
          activationRequested: true,
          snapshot: oldSnapshot,
        }),
      ],
    ]);
    vi.stubGlobal('sessionStorage', {
      getItem: (k: string) => sessionMap.get(k) ?? null,
      setItem: (k: string, v: string) => void sessionMap.set(k, v),
      removeItem: (k: string) => void sessionMap.delete(k),
    });
    const replaceState = vi.fn();
    vi.stubGlobal('window', {
      location: new URL('https://manager.test/.well-known/cpamp-bootstrap/ui'),
      history: { replaceState },
      setTimeout,
      clearTimeout,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });

    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown, init?: RequestInit) => {
        const url = String(input);
        if (url.startsWith('https://manager.test/ctl-old/')) {
          throw new Error(`Stale old-revision prefix was probed: ${url}`);
        }
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/status') {
          return Response.json({
            contractVersion: 'cpamp.upgrade-wizard.v1',
            recorded: true,
            stage: 'complete',
            stageKnown: true,
            nextAction: 'complete',
            domainsDone: ['source_copy', 'cpa_config', 'identity'],
            sourceKind: 'cpamp_1x',
            sourceVersion: 'v1.14.10',
            manualItems: [],
            errorCode: '',
            errorDetail: '',
            targetMode: 'managed',
            routing: {
              ...routingOld,
              controlBasePath: '/ctl-next',
              inferenceBasePath: '/inf-next',
            },
            accessInfo: {
              mode: 'migrate_legacy',
              managementBasePath: '/ctl-next',
              inferenceBasePath: '/inf-next',
              protocols: [],
              ready: false,
              code: 'upgrade_restart_required',
            },
            reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
            execution: { available: true, running: false, action: 'activate', code: '', managedAvailable: true },
          });
        }
        if (url.startsWith('https://manager.test/ctl-next/api/v2/')) {
          return activatedRoute(url, init)!;
        }
        return Response.json({});
      })
    );

    await act(async () => {
      renderer = create(<UpgradeAuthenticationEntry />);
    });
    await act(async () => {
      await adapter().authenticate('old-key', false);
    });
    for (let i = 0; i < 8; i++) {
      await act(async () => {
        await Promise.resolve();
      });
    }
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_500);
    });

    const storedAfterLogin = JSON.parse(sessionMap.get(UPGRADE_COMMITTED_STATUS_KEY) || '{}');
    expect(storedAfterLogin.revision).toBe(revNew);
    expect(storedAfterLogin.controlBasePath).toBe('/ctl-next');

    const openConsole = renderer.root.findByProps({ 'data-testid': 'upgrade-open-console' });
    expect(openConsole.props.href).toBe('https://manager.test/ctl-next');
    await act(async () => {
      openConsole.props.onClick?.();
    });
    expect(sessionMap.has(UPGRADE_COMMITTED_STATUS_KEY)).toBe(false);

    // Un-revisioned legacy persisted entry is also evicted when authenticating with revNew.
    await act(async () => renderer.unmount());
    sessionMap.set(
      UPGRADE_COMMITTED_STATUS_KEY,
      JSON.stringify({ controlBasePath: '/ctl-old', snapshot: oldSnapshot })
    );
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown) => {
        const url = String(input);
        if (url.startsWith('https://manager.test/ctl-old/')) {
          throw new Error(`Legacy un-revisioned prefix was probed: ${url}`);
        }
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/status') {
          return Response.json({}, { status: 404 });
        }
        return Response.json({});
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
    expect(sessionMap.has(UPGRADE_COMMITTED_STATUS_KEY)).toBe(false);
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-boot-failed' })).toBeTruthy();
  });

  it('does not treat pre-activation upgrade_worker_unavailable as activation and resets activationRequested when healthy idle verify resumes', async () => {
    const rev = '3'.repeat(64);
    mocks.verify.mockResolvedValue({
      revision: rev,
      observation: 'observation_unavailable',
    });
    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/ctl-next',
      inferenceBasePath: '/inf-next',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const sessionMap = new Map<string, string>();
    vi.stubGlobal('sessionStorage', {
      getItem: (k: string) => sessionMap.get(k) ?? null,
      setItem: (k: string, v: string) => void sessionMap.set(k, v),
      removeItem: (k: string) => void sessionMap.delete(k),
    });
    vi.stubGlobal('window', {
      location: new URL('https://manager.test/.well-known/cpamp-bootstrap/ui'),
      history: { replaceState: vi.fn() },
      setTimeout,
      clearTimeout,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });

    let statusMode: 'unavailable' | 'verify' | 'activating' = 'unavailable';
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown) => {
        const url = String(input);
        if (url === 'https://manager.test/ctl-next/api/v2/bootstrap/identity') {
          return Response.json({ error: { code: 'upgrade_restart_required' } }, { status: 503 });
        }
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/execute') {
          statusMode = 'activating';
          return Response.json(
            {
              contractVersion: 'cpamp.upgrade-execution.v1',
              available: true,
              running: true,
              action: 'activate',
              code: '',
              managedAvailable: true,
            },
            { status: 202 }
          );
        }
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/status') {
          return Response.json({
            contractVersion: 'cpamp.upgrade-wizard.v1',
            recorded: true,
            stage: 'complete',
            stageKnown: true,
            nextAction: 'complete',
            domainsDone: ['source_copy', 'cpa_config', 'identity'],
            sourceKind: 'cpamp_1x',
            sourceVersion: 'v1.14.10',
            manualItems: [],
            errorCode: '',
            errorDetail: '',
            targetMode: 'managed',
            routing,
            accessInfo: {
              mode: 'migrate_legacy',
              managementBasePath: '/ctl-next',
              inferenceBasePath: '/inf-next',
              protocols: [],
              ready: false,
              code: 'upgrade_restart_required',
            },
            reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
            execution:
              statusMode === 'unavailable'
                ? {
                    available: false,
                    running: false,
                    code: 'upgrade_worker_unavailable',
                    managedAvailable: false,
                  }
                : statusMode === 'activating'
                  ? { available: true, running: true, action: 'activate', code: '', managedAvailable: true }
                  : { available: true, running: false, action: 'verify', code: '', managedAvailable: true },
          });
        }
        return Response.json({});
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

    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-execution-recovery' })).toBeTruthy();
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-activation-waiting' })).toHaveLength(0);
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-verify' }).props.disabled).toBe(false);
    const persistedPreActivate = JSON.parse(sessionMap.get(UPGRADE_COMMITTED_STATUS_KEY) || '{}');
    expect(persistedPreActivate.revision).toBe(rev);
    expect(persistedPreActivate.activationRequested).not.toBe(true);

    statusMode = 'verify';
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_500);
    });
    const activateBtn = renderer.root.findByProps({ 'data-testid': 'upgrade-execute-activate' });
    expect(activateBtn.props.disabled).toBe(false);

    await act(async () => {
      await activateBtn.props.onClick();
    });
    const persistedActivating = JSON.parse(sessionMap.get(UPGRADE_COMMITTED_STATUS_KEY) || '{}');
    expect(persistedActivating.activationRequested).toBe(true);
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-activation-waiting' })).toBeTruthy();

    // Next poll returns healthy idle action: 'verify' -> restarting clears and activationRequested resets.
    statusMode = 'verify';
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_500);
    });
    const persistedReset = JSON.parse(sessionMap.get(UPGRADE_COMMITTED_STATUS_KEY) || '{}');
    expect(persistedReset.activationRequested).toBe(false);
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-activation-waiting' })).toHaveLength(0);
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-execute-activate' }).props.disabled).toBe(
      false
    );
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-verify' }).props.disabled).toBe(false);
  });

  it('bridges fallback->live source_revision on renewal and scopes committed_status.v1 to operation_revision across shutdown reload vs new operation eviction', async () => {
    const fallbackRev = '4'.repeat(64);
    const liveRev = '5'.repeat(64);
    const opRev1 = '6'.repeat(64);
    const opRev2 = '7'.repeat(64);
    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/ctl-next',
      inferenceBasePath: '/inf-next',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const sessionMap = new Map<string, string>();
    vi.stubGlobal('sessionStorage', {
      getItem: (k: string) => sessionMap.get(k) ?? null,
      setItem: (k: string, v: string) => void sessionMap.set(k, v),
      removeItem: (k: string) => void sessionMap.delete(k),
    });
    vi.stubGlobal('window', {
      location: new URL('https://manager.test/.well-known/cpamp-bootstrap/ui'),
      history: { replaceState: vi.fn() },
      setTimeout,
      clearTimeout,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });

    let statusMode: 'unavailable' | 'verify' | 'activating' = 'unavailable';
    let canonicalReady = false;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown, init?: RequestInit) => {
        const url = String(input);
        if (url.startsWith('https://manager.test/ctl-next/api/v2/')) {
          if (!canonicalReady) {
            return Response.json({ error: { code: 'upgrade_restart_required' } }, { status: 503 });
          }
          return activatedRoute(url, init)!;
        }
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/execute') {
          statusMode = 'activating';
          return Response.json(
            {
              contractVersion: 'cpamp.upgrade-execution.v1',
              available: true,
              running: true,
              action: 'activate',
              code: '',
              managedAvailable: true,
            },
            { status: 202 }
          );
        }
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/status') {
          return Response.json({
            contractVersion: 'cpamp.upgrade-wizard.v1',
            recorded: true,
            stage: 'complete',
            stageKnown: true,
            nextAction: 'complete',
            domainsDone: ['source_copy', 'cpa_config', 'identity'],
            sourceKind: 'cpamp_1x',
            sourceVersion: 'v1.14.10',
            manualItems: [],
            errorCode: '',
            errorDetail: '',
            targetMode: 'managed',
            routing,
            accessInfo: {
              mode: 'migrate_legacy',
              managementBasePath: '/ctl-next',
              inferenceBasePath: '/inf-next',
              protocols: [],
              ready: false,
              code: 'upgrade_restart_required',
            },
            reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
            execution:
              statusMode === 'unavailable'
                ? {
                    available: false,
                    running: false,
                    code: 'upgrade_worker_unavailable',
                    managedAvailable: false,
                  }
                : statusMode === 'activating'
                  ? { available: true, running: true, action: 'activate', code: '', managedAvailable: true }
                  : { available: true, running: false, action: 'verify', code: '', managedAvailable: true },
          });
        }
        return Response.json({});
      })
    );

    // 1. Initial sign-in while worker is unavailable (fallbackRev, operationRevision: opRev1).
    mocks.verify.mockResolvedValueOnce({
      revision: fallbackRev,
      operationRevision: opRev1,
      observation: 'observation_unavailable',
    });
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

    const initialStored = JSON.parse(sessionMap.get(UPGRADE_COMMITTED_STATUS_KEY) || '{}');
    expect(initialStored.revision).toBe(opRev1);

    // 2. Worker comes up: 30s renewal sends X-CPAMP-Upgrade-Source: fallbackRev and bridges to liveRev (same opRev1).
    statusMode = 'verify';
    mocks.verify.mockResolvedValueOnce({
      revision: liveRev,
      operationRevision: opRev1,
      observation: 'unsupported_upgrade',
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(mocks.verify).toHaveBeenLastCalledWith('old-key', expect.any(AbortSignal), fallbackRev);
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-authenticated' })).toHaveLength(1);
    expect(JSON.parse(sessionMap.get(UPGRADE_COMMITTED_STATUS_KEY) || '{}').revision).toBe(opRev1);

    // Next renewal sends liveRev in X-CPAMP-Upgrade-Source.
    mocks.verify.mockResolvedValueOnce({
      revision: liveRev,
      operationRevision: opRev1,
      observation: 'unsupported_upgrade',
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(mocks.verify).toHaveBeenLastCalledWith('old-key', expect.any(AbortSignal), liveRev);

    // 3. Operator triggers Activate -> activationRequested: true is stored under opRev1.
    const activateBtn = renderer.root.findByProps({ 'data-testid': 'upgrade-execute-activate' });
    await act(async () => {
      await activateBtn.props.onClick();
    });
    const activatingStored = JSON.parse(sessionMap.get(UPGRADE_COMMITTED_STATUS_KEY) || '{}');
    expect(activatingStored.revision).toBe(opRev1);
    expect(activatingStored.activationRequested).toBe(true);

    // 4. Reload during shutdown while serveEntryUnavailable answers fallbackRev + opRev1:
    // committed_status.v1 is preserved and activation resumes to canonical ready.
    await act(async () => renderer.unmount());
    statusMode = 'unavailable';
    mocks.verify.mockResolvedValueOnce({
      revision: fallbackRev,
      operationRevision: opRev1,
      observation: 'observation_unavailable',
    });
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
    const reloadedStored = JSON.parse(sessionMap.get(UPGRADE_COMMITTED_STATUS_KEY) || '{}');
    expect(reloadedStored.revision).toBe(opRev1);
    expect(reloadedStored.activationRequested).toBe(true);
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-activation-waiting' })).toBeTruthy();

    canonicalReady = true;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-access-active' })).toBeTruthy();

    // 5. Fresh sign-in against a new upgrade operation (opRev2 !== opRev1) evicts stale committed_status.v1.
    await act(async () => renderer.unmount());
    sessionMap.set(
      UPGRADE_COMMITTED_STATUS_KEY,
      JSON.stringify({
        controlBasePath: reloadedStored.controlBasePath,
        revision: opRev1,
        activationRequested: true,
        snapshot: reloadedStored.snapshot,
      })
    );
    mocks.verify.mockResolvedValueOnce({
      revision: fallbackRev,
      operationRevision: opRev2,
      observation: 'observation_unavailable',
    });
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => Response.json({}, { status: 404 }))
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
    expect(sessionMap.has(UPGRADE_COMMITTED_STATUS_KEY)).toBe(false);
  });

  it('migrates committed_status.v1 revision in place on renewal when operationRevision is omitted by a mock', async () => {
    const fallbackRev = '8'.repeat(64);
    const liveRev = '9'.repeat(64);
    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/ctl-next',
      inferenceBasePath: '/inf-next',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const sessionMap = new Map<string, string>();
    vi.stubGlobal('sessionStorage', {
      getItem: (k: string) => sessionMap.get(k) ?? null,
      setItem: (k: string, v: string) => void sessionMap.set(k, v),
      removeItem: (k: string) => void sessionMap.delete(k),
    });
    vi.stubGlobal('window', {
      location: new URL('https://manager.test/.well-known/cpamp-bootstrap/ui'),
      history: { replaceState: vi.fn() },
      setTimeout,
      clearTimeout,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown) => {
        const url = String(input);
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/status') {
          return Response.json({
            contractVersion: 'cpamp.upgrade-wizard.v1',
            recorded: true,
            stage: 'complete',
            stageKnown: true,
            nextAction: 'complete',
            domainsDone: ['source_copy'],
            sourceKind: 'cpamp_1x',
            sourceVersion: 'v1.14.10',
            manualItems: [],
            errorCode: '',
            errorDetail: '',
            targetMode: 'managed',
            routing,
            accessInfo: {
              mode: 'migrate_legacy',
              managementBasePath: '/ctl-next',
              inferenceBasePath: '/inf-next',
              protocols: [],
              ready: false,
              code: 'upgrade_restart_required',
            },
            reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
            execution: { available: true, running: false, action: 'verify', code: '', managedAvailable: true },
          });
        }
        return Response.json({});
      })
    );

    mocks.verify.mockResolvedValueOnce({
      revision: fallbackRev,
      observation: 'observation_unavailable',
    });
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
    expect(JSON.parse(sessionMap.get(UPGRADE_COMMITTED_STATUS_KEY) || '{}').revision).toBe(fallbackRev);

    mocks.verify.mockResolvedValueOnce({
      revision: liveRev,
      observation: 'unsupported_upgrade',
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(JSON.parse(sessionMap.get(UPGRADE_COMMITTED_STATUS_KEY) || '{}').revision).toBe(liveRev);
  });

  it('evicts committed_status.v1 and resets activationPersisted on renewal when operation_revision changes for the same source_revision', async () => {
    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/ctl-next',
      inferenceBasePath: '/inf-next',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const snapshot = {
      contractVersion: 'cpamp.upgrade-wizard.v1',
      recorded: true,
      stage: 'complete',
      stageKnown: true,
      nextAction: 'complete',
      domainsDone: ['source_copy', 'cpa_config', 'identity'],
      sourceKind: 'cpamp_1x',
      sourceVersion: 'v1.14.10',
      manualItems: [],
      errorCode: '',
      errorDetail: '',
      targetMode: 'managed',
      routing,
      accessInfo: {
        mode: 'migrate_legacy',
        managementBasePath: '/ctl-next',
        inferenceBasePath: '/inf-next',
        protocols: [],
        ready: false,
        code: 'upgrade_restart_required',
      },
      reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
    };
    const sessionMap = new Map<string, string>([
      [
        UPGRADE_COMMITTED_STATUS_KEY,
        JSON.stringify({
          controlBasePath: '/ctl-next',
          revision: 'op-rev-1',
          activationRequested: true,
          snapshot,
        }),
      ],
    ]);
    vi.stubGlobal('sessionStorage', {
      getItem: (k: string) => sessionMap.get(k) ?? null,
      setItem: (k: string, v: string) => void sessionMap.set(k, v),
      removeItem: (k: string) => void sessionMap.delete(k),
    });
    vi.stubGlobal('window', {
      location: new URL('https://manager.test/.well-known/cpamp-bootstrap/ui'),
      history: { replaceState: vi.fn() },
      setTimeout,
      clearTimeout,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });

    let initialStatusServed = false;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown) => {
        const url = String(input);
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/status') {
          if (!initialStatusServed) {
            initialStatusServed = true;
            return Response.json({
              ...snapshot,
              execution: {
                available: true,
                running: true,
                action: 'verify',
                code: '',
                managedAvailable: true,
              },
            });
          }
          return Response.json(
            { error: { code: 'upgrade_worker_unavailable' } },
            { status: 503 }
          );
        }
        return Response.json({}, { status: 503 });
      })
    );

    mocks.verify.mockResolvedValueOnce({
      revision: 'fp-same',
      operationRevision: 'op-rev-1',
      observation: 'observation_unavailable',
    });
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

    expect(renderer.root.findByType(FreshUpgradeFlow).props.activationPersisted).toBe(true);
    expect(JSON.parse(sessionMap.get(UPGRADE_COMMITTED_STATUS_KEY) || '{}')).toMatchObject({
      revision: 'op-rev-1',
      activationRequested: true,
    });

    // Renewal returns 200 OK with the same source_revision ("fp-same") but a new operation_revision ("op-rev-2").
    mocks.verify.mockResolvedValueOnce({
      revision: 'fp-same',
      operationRevision: 'op-rev-2',
      observation: 'unsupported_upgrade',
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(mocks.verify).toHaveBeenLastCalledWith('old-key', expect.any(AbortSignal), 'fp-same');
    expect(sessionMap.has(UPGRADE_COMMITTED_STATUS_KEY)).toBe(false);
    expect(renderer.root.findByType(FreshUpgradeFlow).props.activationPersisted).toBe(false);
  });

  it('does not hydrate sessionStorage committed_status.v1 on unauthenticated mount until authenticate() verifies the matching revision', async () => {
    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/ctl-next',
      inferenceBasePath: '/inf-next',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const snapshot = {
      contractVersion: 'cpamp.upgrade-wizard.v1',
      recorded: true,
      stage: 'complete',
      stageKnown: true,
      nextAction: 'complete',
      domainsDone: ['source_copy', 'cpa_config', 'identity'],
      sourceKind: 'cpamp_1x',
      sourceVersion: 'v1.14.10',
      manualItems: [],
      errorCode: '',
      errorDetail: '',
      targetMode: 'managed',
      routing,
      accessInfo: {
        mode: 'migrate_legacy',
        managementBasePath: '/ctl-next',
        inferenceBasePath: '/inf-next',
        protocols: [],
        ready: false,
        code: 'upgrade_restart_required',
      },
      reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
    };
    const sessionMap = new Map<string, string>([
      [
        UPGRADE_COMMITTED_STATUS_KEY,
        JSON.stringify({
          controlBasePath: '/ctl-next',
          revision: 'op-rev-1',
          activationRequested: true,
          snapshot,
        }),
      ],
    ]);
    const getItem = vi.fn((k: string) => sessionMap.get(k) ?? null);
    const removeItem = vi.fn((k: string) => void sessionMap.delete(k));
    vi.stubGlobal('sessionStorage', {
      getItem,
      setItem: (k: string, v: string) => void sessionMap.set(k, v),
      removeItem,
    });
    vi.stubGlobal('window', {
      location: new URL('https://manager.test/.well-known/cpamp-bootstrap/ui'),
      history: { replaceState: vi.fn() },
      setTimeout,
      clearTimeout,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        Response.json({ error: { code: 'upgrade_worker_unavailable' } }, { status: 503 })
      )
    );

    await act(async () => {
      renderer = create(<UpgradeAuthenticationEntry />);
    });
    expect(renderer.root.findAllByType(FullWebLoginPage)).toHaveLength(1);
    expect(getItem).not.toHaveBeenCalledWith(UPGRADE_COMMITTED_STATUS_KEY);
    expect(removeItem).not.toHaveBeenCalledWith(UPGRADE_COMMITTED_STATUS_KEY);

    mocks.verify.mockResolvedValueOnce({
      revision: 'fp-same',
      operationRevision: 'op-rev-1',
      observation: 'observation_unavailable',
    });
    await act(async () => {
      await adapter().authenticate('old-key', false);
    });
    expect(getItem).toHaveBeenCalledWith(UPGRADE_COMMITTED_STATUS_KEY);
    expect(renderer.root.findByType(FreshUpgradeFlow).props.activationPersisted).toBe(true);
    expect(sessionMap.has(UPGRADE_COMMITTED_STATUS_KEY)).toBe(true);
  });

  it('remounts FreshUpgradeFlow and clears recorded and stale state when operation_revision changes on the same source_revision', async () => {
    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/ctl-next',
      inferenceBasePath: '/inf-next',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const op1Status = {
      contractVersion: 'cpamp.upgrade-wizard.v1',
      recorded: true,
      stage: 'complete',
      stageKnown: true,
      nextAction: 'complete',
      domainsDone: ['source_copy', 'cpa_config', 'identity'],
      sourceKind: 'cpamp_1x',
      sourceVersion: 'v1.14.10',
      manualItems: [],
      errorCode: '',
      errorDetail: '',
      targetMode: 'managed',
      routing,
      accessInfo: {
        mode: 'migrate_legacy',
        managementBasePath: '/ctl-next',
        inferenceBasePath: '/inf-next',
        protocols: [],
        ready: false,
        code: 'upgrade_restart_required',
      },
      reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
      execution: {
        available: true,
        running: false,
        action: 'verify',
        code: '',
        managedAvailable: true,
      },
    };
    const op2UnrecordedStatus = {
      contractVersion: 'cpamp.upgrade-wizard.v1',
      recorded: false,
      stage: '',
      stageKnown: true,
      nextAction: 'preflight',
      domainsDone: [],
      sourceKind: 'cpamp_1x',
      sourceVersion: 'v1.14.10',
      manualItems: [],
      errorCode: '',
      errorDetail: '',
      reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
    };
    const op2Preflight = {
      contractVersion: 'cpamp.upgrade-wizard.v1',
      sourceKind: 'cpamp_1x',
      sourceVersion: 'v1.14.10',
      fingerprint: 'ab12cd34ef56…',
      checks: [],
      parityGaps: {
        deletedEventRefs: 0,
        missingProjection: 0,
        mismatchedProjection: 0,
        pastWatermark: 0,
        projectionStateUsable: true,
        coverageDaysWithGaps: 0,
        affectedDates: [],
      },
      blocked: [],
      ready: true,
      pendingDeclaration: false,
      expectedVersion: '',
    };

    let activeOp: 'op1' | 'op2' = 'op1';
    let statusReads = 0;
    vi.stubGlobal('window', {
      location: new URL('https://manager.test/.well-known/cpamp-bootstrap/ui'),
      history: { replaceState: vi.fn() },
      setTimeout,
      clearTimeout,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown) => {
        const url = String(input);
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/status') {
          statusReads += 1;
          return Response.json(activeOp === 'op1' ? op1Status : op2UnrecordedStatus);
        }
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/preflight') {
          return Response.json(op2Preflight);
        }
        if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/execute') {
          return Response.json({ error: { code: 'upgrade_execution_busy' } }, { status: 409 });
        }
        return Response.json({}, { status: 404 });
      })
    );

    mocks.verify.mockResolvedValueOnce({
      revision: 'fp-same',
      operationRevision: 'op-rev-1',
      observation: 'unsupported_upgrade',
    });
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

    // Produce stale executionError inside op-rev-1's FreshUpgradeFlow instance.
    await act(async () => {
      await renderer.root.findByProps({ 'data-testid': 'upgrade-execute-activate' }).props.onClick();
    });
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-execution-error' })).toHaveLength(1);
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-entry-status' }).findAllByType('p')
        .map((node) => node.children.join(''))
    ).toContain(upgradeEntryText('recordedBlockedHelp', 'en'));

    const readsBeforeRenewal = statusReads;
    activeOp = 'op2';
    mocks.verify.mockResolvedValueOnce({
      revision: 'fp-same',
      operationRevision: 'op-rev-2',
      observation: 'unsupported_upgrade',
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    for (let i = 0; i < 12; i++) {
      await act(async () => {
        await Promise.resolve();
      });
    }

    expect(statusReads).toBeGreaterThan(readsBeforeRenewal);
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-execution-error' })).toHaveLength(0);
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-execute-activate' })).toHaveLength(0);
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-continue' })).toBeTruthy();
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-entry-status' }).findAllByType('p')
        .map((node) => node.children.join(''))
    ).toContain(upgradeEntryText('boundary', 'en'));
  });

  it('rejects artifact transport calls immediately with upgrade_bootstrap_retired once canonical routing is observed', async () => {
    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/ctl-next',
      inferenceBasePath: '/inf-next',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const snapshot = {
      contractVersion: 'cpamp.upgrade-wizard.v1',
      recorded: true,
      stage: 'complete',
      stageKnown: true,
      nextAction: 'complete',
      domainsDone: ['source_copy', 'cpa_config', 'identity'],
      sourceKind: 'cpamp_1x',
      sourceVersion: 'v1.14.10',
      manualItems: [],
      errorCode: '',
      errorDetail: '',
      targetMode: 'managed',
      routing,
      accessInfo: {
        mode: 'migrate_legacy',
        managementBasePath: '/ctl-next',
        inferenceBasePath: '/inf-next',
        protocols: [],
        ready: false,
        code: 'upgrade_restart_required',
      },
      reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
      execution: {
        available: true,
        running: false,
        action: 'verify',
        code: '',
        managedAvailable: true,
      },
    };
    vi.stubGlobal('window', {
      location: new URL('https://manager.test/.well-known/cpamp-bootstrap/ui'),
      history: { replaceState: vi.fn() },
      setTimeout,
      clearTimeout,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });

    let canonicalReady = false;
    const fetchMock = vi.fn(async (input: unknown, init?: RequestInit) => {
      const url = String(input);
      if (url.startsWith('https://manager.test/ctl-next/api/v2/')) {
        if (!canonicalReady) {
          return Response.json({ error: { code: 'upgrade_restart_required' } }, { status: 503 });
        }
        return activatedRoute(url, init)!;
      }
      if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/execute') {
        return Response.json(
          {
            contractVersion: 'cpamp.upgrade-execution.v1',
            available: true,
            running: true,
            action: 'activate',
            code: '',
            managedAvailable: true,
          },
          { status: 202 }
        );
      }
      if (url === 'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/status') {
        return Response.json(snapshot);
      }
      return Response.json({}, { status: 404 });
    });
    vi.stubGlobal('fetch', fetchMock);

    mocks.verify.mockResolvedValueOnce({
      revision: 'fp-same',
      operationRevision: 'op-rev-1',
      observation: 'unsupported_upgrade',
    });
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

    canonicalReady = true;
    await act(async () => {
      await renderer.root.findByProps({ 'data-testid': 'upgrade-execute-activate' }).props.onClick();
    });
    for (let i = 0; i < 12; i++) {
      await act(async () => {
        await Promise.resolve();
      });
    }
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-access-active' })).toBeTruthy();

    const artifact = renderer.root.findByType(FreshUpgradeFlow).props.artifact as ArtifactTransport;
    const callsBefore = fetchMock.mock.calls.length;
    const ops = [
      () => artifact.readStatus(),
      () => artifact.probe(),
      () => artifact.acquire({ source: 'github' }),
      () => artifact.upload(new Uint8Array([0x1f, 0x8b, 0x08, 0x00])),
    ];
    for (const op of ops) {
      let caught: unknown;
      try {
        await op();
      } catch (err) {
        caught = err;
      }
      expect(caught).toMatchObject({
        kind: 'http',
        status: 404,
        code: 'upgrade_bootstrap_retired',
      });
      expect(isNonTransientArtifactReadError(caught)).toBe(true);
    }
    expect(fetchMock.mock.calls.length).toBe(callsBefore);
  });

  it.each([
    [502, 'Bad Gateway'],
    [504, 'Gateway Timeout'],
  ])(
    'recovers across reload when bootstrap returns 404 and reverse proxy transiently returns plain %s',
    async (proxyStatus, proxyBody) => {
      mocks.verify.mockResolvedValue({
        revision: 'd'.repeat(64),
        observation: 'observation_unavailable',
      });
      const routing = {
        configured: true,
        committed: true,
        existingPrefixesPreserved: false,
        controlBasePath: '/ctl-next',
        inferenceBasePath: '/inf-next',
        sharedEntry: false,
        externalBaseUrl: '',
        suggestedControlBasePath: '',
        suggestedInferenceBasePath: '',
      };
      const snapshot = {
        contractVersion: 'cpamp.upgrade-wizard.v1',
        recorded: true,
        stage: 'complete',
        stageKnown: true,
        nextAction: 'complete',
        domainsDone: ['source_copy', 'cpa_config', 'identity'],
        sourceKind: 'cpamp_1x',
        sourceVersion: 'v1.14.10',
        manualItems: [],
        errorCode: '',
        errorDetail: '',
        targetMode: 'managed',
        routing,
        accessInfo: {
          mode: 'migrate_legacy',
          managementBasePath: '/ctl-next',
          inferenceBasePath: '/inf-next',
          protocols: [],
          ready: false,
          code: 'upgrade_restart_required',
        },
        reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
      };
      const sessionMap = new Map<string, string>([
        [
          UPGRADE_COMMITTED_STATUS_KEY,
          JSON.stringify({
            controlBasePath: '/ctl-next',
            revision: 'd'.repeat(64),
            activationRequested: true,
            snapshot,
          }),
        ],
      ]);
      vi.stubGlobal('sessionStorage', {
        getItem: (k: string) => sessionMap.get(k) ?? null,
        setItem: (k: string, v: string) => void sessionMap.set(k, v),
        removeItem: (k: string) => void sessionMap.delete(k),
      });
      const replaceState = vi.fn();
      vi.stubGlobal('window', {
        location: new URL('https://manager.test/.well-known/cpamp-bootstrap/ui'),
        history: { replaceState },
        setTimeout,
        clearTimeout,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      });

      let proxyReady = false;
      vi.stubGlobal(
        'fetch',
        vi.fn(async (input: unknown, init?: RequestInit) => {
          const url = String(input);
          if (
            url ===
            'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/status'
          ) {
            return Response.json({}, { status: 404 });
          }
          if (url.startsWith('https://manager.test/ctl-next/api/v2/')) {
            if (!proxyReady) {
              return new Response(proxyBody, {
                status: proxyStatus,
                headers: { 'Content-Type': 'text/plain' },
              });
            }
            return activatedRoute(url, init)!;
          }
          return Response.json({});
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

      // Plain reverse-proxy 502/504 must enter canonical activation recovery instead of failing boot.
      expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-boot-failed' })).toHaveLength(
        0
      );
      expect(
        renderer.root.findByProps({ 'data-testid': 'upgrade-activation-waiting' })
      ).toBeTruthy();
      await act(async () => {
        await vi.advanceTimersByTimeAsync(12_000);
      });
      expect(
        renderer.root.findAllByProps({ 'data-testid': 'upgrade-result-stalled' })
      ).toHaveLength(0);
      expect(
        renderer.root.findByProps({ 'data-testid': 'upgrade-activation-waiting' })
      ).toBeTruthy();

      proxyReady = true;
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      expect(replaceState).toHaveBeenCalledWith(null, '', '/ctl-next');
      expect(
        renderer.root.findByProps({ 'data-testid': 'upgrade-open-console' }).props.href
      ).toBe('https://manager.test/ctl-next');
    }
  );

  it('recovers across reload when initial canonical probe returns 500 and manual boot retry hits plain 404 before canonical 200', async () => {
    mocks.verify.mockResolvedValue({
      revision: 'e'.repeat(64),
      observation: 'observation_unavailable',
    });
    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/ctl-next',
      inferenceBasePath: '/inf-next',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const snapshot = {
      contractVersion: 'cpamp.upgrade-wizard.v1',
      recorded: true,
      stage: 'complete',
      stageKnown: true,
      nextAction: 'complete',
      domainsDone: ['source_copy', 'cpa_config', 'identity'],
      sourceKind: 'cpamp_1x',
      sourceVersion: 'v1.14.10',
      manualItems: [],
      errorCode: '',
      errorDetail: '',
      targetMode: 'managed',
      routing,
      accessInfo: {
        mode: 'migrate_legacy',
        managementBasePath: '/ctl-next',
        inferenceBasePath: '/inf-next',
        protocols: [],
        ready: false,
        code: 'upgrade_restart_required',
      },
      reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
    };
    const sessionMap = new Map<string, string>([
      [
        UPGRADE_COMMITTED_STATUS_KEY,
        JSON.stringify({
          controlBasePath: '/ctl-next',
          revision: 'e'.repeat(64),
          activationRequested: true,
          snapshot,
        }),
      ],
    ]);
    vi.stubGlobal('sessionStorage', {
      getItem: (k: string) => sessionMap.get(k) ?? null,
      setItem: (k: string, v: string) => void sessionMap.set(k, v),
      removeItem: (k: string) => void sessionMap.delete(k),
    });
    const replaceState = vi.fn();
    vi.stubGlobal('window', {
      location: new URL('https://manager.test/.well-known/cpamp-bootstrap/ui'),
      history: { replaceState },
      setTimeout,
      clearTimeout,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });

    let canonicalPhase: 'initial500' | 'plain404' | 'ready' = 'initial500';
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown, init?: RequestInit) => {
        const url = String(input);
        if (
          url ===
          'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/status'
        ) {
          return Response.json({}, { status: 404 });
        }
        if (url.startsWith('https://manager.test/ctl-next/api/v2/')) {
          if (canonicalPhase === 'initial500') {
            return new Response('Internal Server Error', {
              status: 500,
              headers: { 'Content-Type': 'text/plain' },
            });
          }
          if (canonicalPhase === 'plain404') {
            return new Response('Not Found', {
              status: 404,
              headers: { 'Content-Type': 'text/plain' },
            });
          }
          return activatedRoute(url, init)!;
        }
        return Response.json({});
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

    // Initial reload hits 500 after bootstrap retirement and lands on upgrade-boot-failed.
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-boot-failed' })).toBeTruthy();

    // Manual retry hitting plain 404 enters canonical cutover recovery instead of staying failed.
    canonicalPhase = 'plain404';
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'upgrade-boot-retry' }).props.onClick();
    });
    for (let i = 0; i < 4; i++) {
      await act(async () => {
        await Promise.resolve();
      });
    }
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-boot-failed' })).toHaveLength(0);
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-progress' })).toBeTruthy();
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-progress-reading' })).toBeTruthy();

    // Repeated plain 404 polls keep recovering without stalling.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(12_000);
    });
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-boot-failed' })).toHaveLength(0);
    expect(renderer.root.findAllByProps({ 'data-testid': 'upgrade-result-stalled' })).toHaveLength(
      0
    );
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-progress-reading' })).toBeTruthy();

    // Once canonical endpoint returns 200, activation completes cleanly.
    canonicalPhase = 'ready';
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(replaceState).toHaveBeenCalledWith(null, '', '/ctl-next');
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-access-active' })).toBeTruthy();
    expect(
      renderer.root.findByProps({ 'data-testid': 'upgrade-open-console' }).props.href
    ).toBe('https://manager.test/ctl-next');
  });

  it('resets recorded state on logout or session retirement so re-login never leaks prior recordedObservationHelp', async () => {
    mocks.verify.mockResolvedValue({
      revision: 'f'.repeat(64),
      observation: 'observation_unavailable',
    });
    const recordedStatus = {
      contractVersion: 'cpamp.upgrade-wizard.v1',
      recorded: true,
      stage: 'verify',
      stageKnown: true,
      nextAction: 'verify',
      domainsDone: ['source_copy', 'cpa_config', 'identity'],
      sourceKind: 'cpamp_1x',
      sourceVersion: 'v1.14.10',
      manualItems: [],
      errorCode: '',
      errorDetail: '',
      targetMode: 'managed',
      reconciliation: { mismatchDays: [], mismatchDaysTotal: 0, mismatchDaysTruncated: false },
    };

    let statusMode: 'recorded' | 'pending' | 'fail503' = 'recorded';
    let resolvePendingStatus: ((res: Response) => void) | null = null;
    const pagehideListeners: Array<() => void> = [];
    vi.stubGlobal('window', {
      location: new URL('https://manager.test/.well-known/cpamp-bootstrap/ui'),
      history: { replaceState: vi.fn() },
      setTimeout,
      clearTimeout,
      addEventListener: vi.fn((event: string, listener: () => void) => {
        if (event === 'pagehide') pagehideListeners.push(listener);
      }),
      removeEventListener: vi.fn(),
    });
    vi.stubGlobal(
      'fetch',
      vi.fn((input: unknown) => {
        const url = String(input);
        if (
          url ===
          'https://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/status'
        ) {
          if (statusMode === 'recorded') {
            return Promise.resolve(Response.json(recordedStatus));
          }
          if (statusMode === 'pending') {
            return new Promise<Response>((resolve) => {
              resolvePendingStatus = resolve;
            });
          }
          return Promise.resolve(Response.json({}, { status: 503 }));
        }
        return Promise.resolve(Response.json({}));
      })
    );

    await act(async () => {
      renderer = create(<UpgradeAuthenticationEntry />);
    });
    await act(async () => {
      await adapter().authenticate('first-key', false);
    });
    for (let i = 0; i < 4; i++) {
      await act(async () => {
        await Promise.resolve();
      });
    }

    const entryParagraphs = () =>
      renderer.root
        .findByProps({ 'data-testid': 'upgrade-entry-status' })
        .findAllByType('p')
        .map((node) => node.children.join(''));

    expect(entryParagraphs()).toContain(upgradeEntryText('recordedObservationHelp', 'en'));

    // 1. Logout retires the envelope and resets recorded before re-login.
    const logoutLabel = upgradeEntryText('logout', 'en');
    const logoutBtn = renderer.root.find(
      (node) => node.type === 'button' && node.children.join('') === logoutLabel
    );
    await act(async () => {
      logoutBtn.props.onClick();
    });

    statusMode = 'pending';
    await act(async () => {
      await adapter().authenticate('second-key', false);
    });
    // While /status is still pending on an observation_unavailable source, copy must not leak recordedObservationHelp.
    expect(entryParagraphs()).toContain(upgradeEntryText('observationUnavailableHelp', 'en'));
    expect(entryParagraphs()).not.toContain(upgradeEntryText('recordedObservationHelp', 'en'));

    // Even if /status fails (503 -> upgrade-boot-failed), copy remains observationUnavailableHelp.
    await act(async () => {
      resolvePendingStatus?.(Response.json({}, { status: 503 }));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-boot-failed' })).toBeTruthy();
    expect(entryParagraphs()).toContain(upgradeEntryText('observationUnavailableHelp', 'en'));
    expect(entryParagraphs()).not.toContain(upgradeEntryText('recordedObservationHelp', 'en'));

    // 2. Re-enter recorded state, retire via pagehide, and verify fresh login also resets recorded.
    statusMode = 'recorded';
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'upgrade-boot-retry' }).props.onClick();
    });
    for (let i = 0; i < 4; i++) {
      await act(async () => {
        await Promise.resolve();
      });
    }
    expect(entryParagraphs()).toContain(upgradeEntryText('recordedObservationHelp', 'en'));

    await act(async () => {
      pagehideListeners[pagehideListeners.length - 1]?.();
    });

    statusMode = 'fail503';
    await act(async () => {
      await adapter().authenticate('third-key', false);
    });
    for (let i = 0; i < 4; i++) {
      await act(async () => {
        await Promise.resolve();
      });
    }
    expect(renderer.root.findByProps({ 'data-testid': 'upgrade-boot-failed' })).toBeTruthy();
    expect(entryParagraphs()).toContain(upgradeEntryText('observationUnavailableHelp', 'en'));
    expect(entryParagraphs()).not.toContain(upgradeEntryText('recordedObservationHelp', 'en'));
  });
});
