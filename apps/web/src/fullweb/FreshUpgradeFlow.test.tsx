import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  createUpgradeWizardRead,
  createUpgradeWizardExecute,
  parseUpgradeExecution,
  parseUpgradeStatus,
  type UpgradeWizardExecute,
  createUpgradeWizardSaveRouting,
  createUpgradeWizardVerify,
  FreshUpgradeFlow,
  markBootstrapRetiredRecovery,
  markCanonicalUpgradeStatus,
  parseUpgradePreflight,
  parseUpgradeVerify,
  type UpgradeStatus,
  UpgradeWizardError,
  type UpgradeWizardRead,
  type UpgradeWizardResource,
  type UpgradeWizardSaveRouting,
  type UpgradeWizardVerify,
} from './FreshUpgradeFlow';
import type { ArtifactOperation, ArtifactStatus, ArtifactTransport } from './freshArtifact';
import { blockedCodeText, upgradeExecutionText as e, upgradeWizardText as t } from '../i18n/upgradeWizard';
import type { AccessInfo } from './FreshAccessInfo';
// Drive recorded operation and worker observations independently: an accepted
// job is never sufficient to advance the rendered operation.

const CONTRACT = 'cpamp.upgrade-wizard.v1';

function preflightDocument(overrides: Record<string, unknown> = {}) {
  return {
    contractVersion: CONTRACT,
    sourceKind: 'cpamp_1x',
    sourceVersion: '1.9.4',
    fingerprint: 'ab12cd34ef56…',
    checks: [
      { name: 'source_ownership', status: 'pass', code: '', detail: 'no live source writer' },
      { name: 'source_version', status: 'pass', code: '', detail: 'version=1.9.4' },
      { name: 'fingerprint', status: 'pass', code: '', detail: 'sha256 over source manifest' },
      { name: 'disk', status: 'pass', code: '', detail: 'needs 12 GiB, has 40 GiB' },
      { name: 'cpa_classification', status: 'pass', code: '', detail: 'kind=external' },
      { name: 'parity', status: 'gap', code: '', detail: 'retained projection has gaps' },
    ],
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
    ...overrides,
  };
}

// The route's own shape: mismatch days travel inside `reconciliation`, never as
// a top-level field, so the fixture mirrors that projection — including the
// total and the truncation flag that bound the list.
function statusDocument(
  overrides: Partial<UpgradeStatus> & Record<string, unknown> = {}
) {
  const {
    mismatchDays = [],
    mismatchDaysTotal,
    mismatchDaysTruncated,
    hasParityGaps,
    targetMode,
    routing,
    accessInfo,
    ...rest
  } = overrides;
  const total = mismatchDaysTotal ?? mismatchDays.length;
  return {
    contractVersion: CONTRACT,
    recorded: true,
    stage: 'copy_in',
    stageKnown: true,
    nextAction: 'copy_in',
    domainsDone: [],
    sourceKind: 'cpamp_1x',
    sourceVersion: '1.9.4',
    reconciliation: {
      mismatchDays,
      mismatchDaysTotal: total,
      mismatchDaysTruncated: mismatchDaysTruncated ?? total > mismatchDays.length,
      ...(hasParityGaps !== undefined ? { hasParityGaps } : {}),
    },
    manualItems: [],
    errorCode: '',
    errorDetail: '',
    ...(targetMode !== undefined ? { targetMode } : {}),
    ...(routing !== undefined ? { routing } : {}),
    ...(accessInfo !== undefined ? { accessInfo } : {}),
    ...rest,
  };
}

// Nothing recorded yet: the store answered, and its answer is the precondition
// for the preflight step.
function unrecordedStatus() {
  return statusDocument({ recorded: false, stage: '', nextAction: 'preflight', domainsDone: [] });
}

// The action's own reply. The gate records exactly two positions — complete on an
// External target and a Managed completion — so the fixture is written as the
// Managed completed operation and every test overrides from there.
function verifyDocument(overrides: Record<string, unknown> = {}) {
  return {
    contractVersion: CONTRACT,
    recorded: true,
    stage: 'complete',
    stageKnown: true,
    complete: true,
    targetMode: 'managed',
    domainsDone: [],
    reasonCode: '',
    reason: '',
    manualItems: [],
    alreadyVerified: false,
    updatedAtMs: 40,
    ...overrides,
  };
}

function executionDocument(overrides: Record<string, unknown> = {}) {
  return { contractVersion: 'cpamp.upgrade-execution.v1', available: true,
    running: true, managedAvailable: true, ...overrides };
}

const workerReady = { available: true, running: false, managedAvailable: true };
describe('Full Web upgrade wizard', () => {
  let renderer: ReactTestRenderer | undefined;

  beforeEach(() => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    vi.useFakeTimers();
    vi.stubGlobal('window', { setTimeout, clearTimeout });
  });

  afterEach(async () => {
    const active = renderer;
    if (active) await act(async () => active.unmount());
    renderer = undefined;
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });
  async function mount(
    read: UpgradeWizardRead,
    verify?: UpgradeWizardVerify,
    artifact?: ArtifactTransport,
    saveRouting?: UpgradeWizardSaveRouting,
    execute?: UpgradeWizardExecute,
    onActivation?: (prefix: string) => void,
    activationPersisted?: boolean
  ) {
    await act(async () => {
      renderer = create(
        <FreshUpgradeFlow
          read={read}
          verify={verify}
          saveRouting={saveRouting}
          artifact={artifact}
          execute={execute}
          onActivation={onActivation}
          activationPersisted={activationPersisted}
          language="en"
          pollDelays={[10_000]}
        />
      );
    });
  }
  const byTestId = (id: string) => renderer!.root.findByProps({ 'data-testid': id });
  const allByTestId = (id: string) => renderer!.root.findAllByProps({ 'data-testid': id });
  const view = () => JSON.stringify(renderer!.toJSON());

  async function press(testid: string) {
    await act(async () => {
      await byTestId(testid).props.onClick();
    });
  }

  async function acknowledge() {
    await act(async () => {
      byTestId('upgrade-confirm-ack').props.onChange({ target: { checked: true } });
    });
  }

  // Walks a reading operator from the verified entry to the progress step.
  async function reachProgress(read: UpgradeWizardRead) {
    await mount(read);
    await press('upgrade-continue');
    await acknowledge();
    await press('upgrade-confirm-continue');
  }

  // The ordinary path: the boot read reports no operation, so the wizard walks
  // the preflight and only projects `recorded` once the operator reaches
  // progress. A test that starts from an existing operation uses `resumeRead`.
  function wizardRead(
    options: { preflight?: () => unknown; recorded?: () => unknown } = {}
  ): UpgradeWizardRead {
    let booted = false;
    return vi.fn(async (resource: UpgradeWizardResource) => {
      if (resource === 'installation/upgrade/preflight') {
        return options.preflight ? options.preflight() : preflightDocument();
      }
      if (!booted) {
        booted = true;
        return unrecordedStatus();
      }
      if (!options.recorded) throw new Error('status was read before an operation was recorded');
      return options.recorded();
    });
  }

  // An operation recorded before the page opened: the first status read is the
  // resume. The preflight is never asked for, so asking for it fails the test.
  function resumeRead(recorded: () => unknown): UpgradeWizardRead {
    return vi.fn(async (resource: UpgradeWizardResource) => {
      if (resource !== 'installation/upgrade/status') {
        throw new Error(`a resumed operation asked for ${resource}`);
      }
      return recorded();
    });
  }

  it('renders every check and names each blocking code, holding Continue disabled', async () => {
    const blocked = ['upgrade_source_busy', 'upgrade_disk_insufficient'];
    await mount(
      wizardRead({
        preflight: () =>
          preflightDocument({
            checks: [
              {
                name: 'source_ownership',
                status: 'blocked',
                code: 'upgrade_source_busy',
                detail: 'source lock is held',
              },
              {
                name: 'disk',
                status: 'blocked',
                code: 'upgrade_disk_insufficient',
                detail: 'needs 12 GiB, has 3 GiB',
              },
              {
                name: 'cpa_classification',
                status: 'pass',
                code: '',
                detail: 'kind=external',
              },
            ],
            blocked,
            ready: false,
          }),
      })
    );

    expect(byTestId('upgrade-preflight')).toBeTruthy();
    expect(byTestId('upgrade-check-source_ownership').props['data-status']).toBe('blocked');
    expect(byTestId('upgrade-check-cpa_classification').props['data-status']).toBe('pass');
    expect(byTestId('upgrade-preflight-blocked')).toBeTruthy();
    for (const code of blocked) {
      expect(view()).toContain(blockedCodeText(code, 'en'));
      expect(byTestId(`upgrade-blocked-${code}`)).toBeTruthy();
    }
    expect(allByTestId('upgrade-preflight-ready')).toHaveLength(0);
    // A blocked preflight can only be re-run, never carried forward.
    expect(byTestId('upgrade-continue').props.disabled).toBe(true);
  });

  it('keeps pending source evidence distinct without supplying a CLI declaration', async () => {
    await mount(wizardRead({ preflight: () => preflightDocument({
      sourceVersion: 'unknown', pendingDeclaration: true, expectedVersion: 'v1.14.10',
      checks: [{ name: 'source_version', status: 'pending', code: '', detail: '' }],
    }) }));
    expect(byTestId('upgrade-check-source_version').props['data-status']).toBe('pending');
    expect(byTestId('upgrade-continue').props.disabled).toBe(false);
    expect(allByTestId('upgrade-preflight-blocked')).toHaveLength(0);
    expect(view()).not.toContain('--source-version');
    await press('upgrade-continue');
    expect(byTestId('upgrade-confirm-version').children.join('')).toContain('v1.14.10');
    expect(view()).not.toContain('docker run');
  });

  it('formats an unknown sourceVersion without pending declaration as localized Not reported rather than raw unknown', async () => {
    await mount(
      wizardRead({
        preflight: () => preflightDocument({ sourceVersion: 'unknown' }),
      })
    );
    expect(byTestId('upgrade-preflight-version').children.join('')).toBe(
      t('preflightUnknown', 'en')
    );
    await press('upgrade-continue');
    expect(byTestId('upgrade-confirm-version').children.join('')).toBe(
      t('preflightUnknown', 'en')
    );
  });

  it('treats a pending flag that disagrees with its row as a typed read failure', async () => {
    await mount(
      wizardRead({
        preflight: () => preflightDocument({ pendingDeclaration: true, expectedVersion: 'v1.14.10' }),
      })
    );
    expect(byTestId('upgrade-preflight-error')).toBeTruthy();
    expect(allByTestId('upgrade-preflight-pending-declaration')).toHaveLength(0);
  });

  it('accepts a pending declaration that names its version while another check blocks', () => {
    // The wizard never declares a source version, so the row stays pending — but
    // a blocked disk or parity gap is an independent fact about the same source
    // and must not invalidate the report that carries it.
    const parsed = parseUpgradePreflight(
      preflightDocument({
        checks: [
          { name: 'source_ownership', status: 'pass', code: '', detail: 'no live source writer' },
          {
            name: 'source_version',
            status: 'pending',
            code: 'upgrade_source_version_declared_at_copy_in',
            detail: 'expected=v1.14.10 declared=none',
          },
          {
            name: 'disk',
            status: 'blocked',
            code: 'upgrade_insufficient_disk_space',
            detail: 'needs 12 GiB, has 3 GiB',
          },
        ],
        blocked: ['upgrade_insufficient_disk_space'],
        ready: false,
        pendingDeclaration: true,
        expectedVersion: 'v1.14.10',
      })
    );

    expect(parsed).not.toBeNull();
    expect(parsed?.pendingDeclaration).toBe(true);
    expect(parsed?.expectedVersion).toBe('v1.14.10');
    expect(parsed?.ready).toBe(false);
    expect(parsed?.blocked).toEqual(['upgrade_insufficient_disk_space']);
  });

  it('still refuses a pending declaration without a version or with its own row blocked', () => {
    const pendingRow = { name: 'source_version', status: 'pending', code: '', detail: '' };

    // No version to require at copy-in: the declaration could never be satisfied.
    expect(
      parseUpgradePreflight(
        preflightDocument({
          checks: [pendingRow],
          pendingDeclaration: true,
          expectedVersion: '',
        })
      )
    ).toBeNull();

    // The same row cannot be pending and further down the same report blocked.
    expect(
      parseUpgradePreflight(
        preflightDocument({
          checks: [pendingRow, { ...pendingRow, status: 'blocked' }],
          blocked: ['upgrade_source_version_undeclared'],
          ready: false,
          pendingDeclaration: true,
          expectedVersion: 'v1.14.10',
        })
      )
    ).toBeNull();

    // A pending declaration is not a refusal, so the source-version codes cannot
    // be the document's blocking codes.
    expect(
      parseUpgradePreflight(
        preflightDocument({
          checks: [pendingRow],
          blocked: ['upgrade_source_version_unsupported'],
          ready: false,
          pendingDeclaration: true,
          expectedVersion: 'v1.14.10',
        })
      )
    ).toBeNull();
  });

  it('shows a pending declaration and a separate blocker together, holding Continue disabled', async () => {
    await mount(
      wizardRead({
        preflight: () =>
          preflightDocument({
            checks: [
              {
                name: 'source_ownership',
                status: 'pass',
                code: '',
                detail: 'no live source writer',
              },
              {
                name: 'source_version',
                status: 'pending',
                code: 'upgrade_source_version_declared_at_copy_in',
                detail: 'expected=v1.14.10 declared=none',
              },
              {
                name: 'disk',
                status: 'blocked',
                code: 'upgrade_insufficient_disk_space',
                detail: 'needs 12 GiB, has 3 GiB',
              },
            ],
            blocked: ['upgrade_insufficient_disk_space'],
            ready: false,
            pendingDeclaration: true,
            expectedVersion: 'v1.14.10',
          }),
      })
    );

    // Both facts the document states are shown: the version this read cannot
    // declare and the check that actually stops the upgrade.
    expect(byTestId('upgrade-preflight-pending-declaration')).toBeTruthy();
    expect(view()).not.toContain('--source-version');
    expect(view()).toContain(t('preflightPendingTitle', 'en'));
    expect(byTestId('upgrade-preflight-blocked')).toBeTruthy();
    expect(view()).toContain(blockedCodeText('upgrade_insufficient_disk_space', 'en'));
    expect(allByTestId('upgrade-preflight-ready')).toHaveLength(0);
    expect(byTestId('upgrade-continue').props.disabled).toBe(true);
    // The report was presented, not discarded as an unreadable reply.
    expect(allByTestId('upgrade-preflight-error')).toHaveLength(0);
    expect(view()).not.toContain(t('errorResponse', 'en'));
  });

  it('continues from a ready preflight only after an explicit acknowledgement', async () => {
    await mount(wizardRead({ recorded: () => statusDocument() }));

    expect(byTestId('upgrade-preflight-ready')).toBeTruthy();
    expect(byTestId('upgrade-continue').props.disabled).toBe(false);
    await press('upgrade-continue');

    expect(byTestId('upgrade-confirm')).toBeTruthy();
    expect(byTestId('upgrade-confirm-version').children.join('')).toBe('1.9.4');
    expect(byTestId('upgrade-confirm-continue').props.disabled).toBe(true);

    await acknowledge();
    expect(byTestId('upgrade-confirm-continue').props.disabled).toBe(false);
    await press('upgrade-confirm-continue');
    expect(byTestId('upgrade-progress')).toBeTruthy();
  });

  it('reports a stage in progress without claiming the operation is over', async () => {
    await reachProgress(
      wizardRead({ recorded: () => statusDocument({ domainsDone: ['source_copy', 'usage_events'] }) })
    );

    expect(byTestId('upgrade-progress-stage')).toBeTruthy();
    expect(view()).toContain(t('stageCopyIn', 'en'));
    expect(view()).toContain(t('domainUsageEvents', 'en'));
    expect(byTestId('upgrade-progress-waiting')).toBeTruthy();
    expect(allByTestId('upgrade-result')).toHaveLength(0);
    expect(allByTestId('upgrade-result-complete')).toHaveLength(0);
  });

  it('stalls after three consecutive read failures and retries on demand', async () => {
    let calls = 0;
    // The boot read answered "nothing recorded"; every later status read fails
    // three times before the operation is complete.
    const read = wizardRead({
      recorded: () => {
        calls += 1;
        if (calls <= 3) throw new Error('lost');
        return statusDocument({ stage: 'complete', nextAction: 'complete' });
      },
    });
    await reachProgress(read);

    // Each failure schedules the next read; three of them stop the poll.
    for (let index = 0; index < 3; index++) {
      await act(async () => {
        await vi.runOnlyPendingTimersAsync();
      });
    }
    expect(byTestId('upgrade-progress-stalled')).toBeTruthy();
    expect(view()).toContain(t('progressStalledHelp', 'en'));
    expect(view()).toContain(t('errorNetwork', 'en'));
    expect(allByTestId('upgrade-result')).toHaveLength(0);

    // A fourth read is not attempted until the operator asks for one.
    expect(calls).toBe(3);
    await press('upgrade-progress-retry');
    expect(calls).toBe(4);
    expect(byTestId('upgrade-result-complete')).toBeTruthy();
  });

  it('never claims completion for a rolled-back operation and shows its reason', async () => {
    await reachProgress(
      wizardRead({
        recorded: () =>
          statusDocument({
            stage: 'rolled_back',
            nextAction: 'rolled_back',
            domainsDone: ['source_copy'],
            errorCode: 'upgrade_target_modified',
            errorDetail: 'the target store changed during the copy',
          }),
      })
    );

    expect(byTestId('upgrade-result')).toBeTruthy();
    expect(byTestId('upgrade-result-rollback')).toBeTruthy();
    expect(view()).toContain('the target store changed during the copy');
    expect(view()).toContain(t('resultRollbackRestart', 'en'));
    expect(byTestId('upgrade-result-incomplete')).toBeTruthy();
    expect(allByTestId('upgrade-result-complete')).toHaveLength(0);
  });

  it('lists the reconciliation mismatch days it was given', async () => {
    await reachProgress(
      wizardRead({
        recorded: () =>
          statusDocument({
            stage: 'complete',
            nextAction: 'complete',
            mismatchDays: ['2026-10-01', '2026-10-02'],
          }),
      })
    );

    expect(byTestId('upgrade-mismatch-2026-10-01')).toBeTruthy();
    expect(byTestId('upgrade-mismatch-2026-10-02')).toBeTruthy();
    expect(allByTestId('upgrade-reconciliation-none')).toHaveLength(0);
  });

  it('renders the success headline only when the operation says complete', async () => {
    // A settled stage reaches the result step; an unfinished or unnameable one
    // keeps the wizard on progress and never renders a headline at all.
    const stages: Array<[Partial<UpgradeStatus>, 'complete' | 'incomplete' | 'progress']> = [
      [{ stage: 'complete', nextAction: 'complete' }, 'complete'],
      [{ stage: 'failed', nextAction: 'reconcile' }, 'incomplete'],
      [{ stage: 'rolled_back', nextAction: 'rolled_back' }, 'incomplete'],
      [{ stage: 'from_a_newer_build', stageKnown: false, nextAction: '' }, 'incomplete'],
      [{ stage: 'verify', nextAction: 'reconcile' }, 'progress'],
    ];
    for (const [overrides, expected] of stages) {
      await reachProgress(wizardRead({ recorded: () => statusDocument(overrides) }));
      expect({ expected, complete: allByTestId('upgrade-result-complete').length }).toEqual({
        expected,
        complete: expected === 'complete' ? 1 : 0,
      });
      expect(allByTestId('upgrade-result-incomplete')).toHaveLength(
        expected === 'incomplete' ? 1 : 0
      );
      expect(allByTestId('upgrade-result')).toHaveLength(expected === 'progress' ? 0 : 1);
      await act(async () => renderer!.unmount());
      renderer = undefined;
    }
  });

  it("reads both routes with the entry page's own credential scheme", async () => {
    // The wizard has no session of its own: the entry page proves the source
    // administrator key against the 1.x source, keeps it in a ref and hands this
    // factory a getter. The reader must send exactly that key as a Bearer value
    // on both routes — never the installation session token.
    const credential = 'Cpamp_Legacy_Upgrade_Admin_Secret_11440';
    const calls: Array<{ url: string; authorization: string }> = [];
    const transport = vi.fn(
      async (input: string, init: { headers: Record<string, string> }) => {
        calls.push({ url: input, authorization: init.headers.Authorization });
        return {
          ok: true,
          status: 200,
          json: async () => ({ contractVersion: 'cpamp.upgrade-wizard.v1' }),
        };
      }
    ) as unknown as typeof fetch;
    vi.stubGlobal('document', { getElementById: () => null });
    vi.stubGlobal('window', {
      setTimeout,
      clearTimeout,
      location: { origin: 'http://manager.test' },
    });

    const read = createUpgradeWizardRead(() => credential, transport);
    const signal = new AbortController().signal;
    await read('installation/upgrade/preflight', signal);
    await read('installation/upgrade/status', signal);

    const base = '/.well-known/cpamp-bootstrap/api/v2';
    expect(calls).toEqual([
      {
        url: `http://manager.test${base}/installation/upgrade/preflight`,
        authorization: `Bearer ${credential}`,
      },
      {
        url: `http://manager.test${base}/installation/upgrade/status`,
        authorization: `Bearer ${credential}`,
      },
    ]);
  });

  it('refuses to read at all without the entry credential', async () => {
    const transport = vi.fn() as unknown as typeof fetch;
    vi.stubGlobal('document', { getElementById: () => null });
    vi.stubGlobal('window', {
      setTimeout,
      clearTimeout,
      location: { origin: 'http://manager.test' },
    });

    const read = createUpgradeWizardRead(() => '', transport);
    await expect(
      read('installation/upgrade/status', new AbortController().signal)
    ).rejects.toMatchObject({ failure: 'authentication' });
    expect(transport).not.toHaveBeenCalled();
  });

  it.each(['：legacy-1x-key', 'legacy-1x-key│', 'legacy-1x-key\nnext'])(
    'maps non-ByteString or multiline credential %j to 401 authentication without invoking transport',
    async (invalidKey) => {
      const transport = vi.fn() as unknown as typeof fetch;
      vi.stubGlobal('document', { getElementById: () => null });
      vi.stubGlobal('window', {
        setTimeout,
        clearTimeout,
        location: { origin: 'http://manager.test' },
      });

      const read = createUpgradeWizardRead(() => invalidKey, transport);
      await expect(
        read('installation/upgrade/status', new AbortController().signal)
      ).rejects.toMatchObject({ failure: 'authentication', status: 401 });
      expect(transport).not.toHaveBeenCalled();
    }
  );

  it('treats a contradictory preflight report as a typed read failure', async () => {
    await mount(
      wizardRead({
        preflight: () => preflightDocument({ blocked: ['upgrade_source_busy'], ready: true }),
      })
    );

    expect(byTestId('upgrade-preflight-error')).toBeTruthy();
    expect(allByTestId('upgrade-check-source_ownership')).toHaveLength(0);
    expect(byTestId('upgrade-continue').props.disabled).toBe(true);
  });

  it('resumes a recorded operation without a preflight, for every recorded stage', async () => {
    const stages: Array<[Partial<UpgradeStatus>, 'progress' | 'result']> = [
      [{ stage: 'copy_in', nextAction: 'copy_in' }, 'progress'],
      [{ stage: 'reconcile', nextAction: 'reconcile' }, 'progress'],
      [{ stage: 'rolled_back', nextAction: 'rolled_back' }, 'result'],
      [{ stage: 'verify', nextAction: 'reconcile' }, 'progress'],
    ];
    for (const [overrides, expected] of stages) {
      const read = resumeRead(() => statusDocument(overrides));
      await mount(read);
      // The recorded operation is the page's first read, and the preflight is
      // never asked for: an unmounted source must not hide an existing stage.
      expect(byTestId('upgrade-resumed')).toBeTruthy();
      expect(allByTestId('upgrade-preflight')).toHaveLength(0);
      expect(vi.mocked(read).mock.calls.map(([resource]) => resource)).not.toContain(
        'installation/upgrade/preflight'
      );
      expect(allByTestId('upgrade-progress')).toHaveLength(expected === 'progress' ? 1 : 0);
      expect(allByTestId('upgrade-result')).toHaveLength(expected === 'result' ? 1 : 0);
      if (overrides.stage === 'verify') {
        expect(view()).toContain(t('stageVerify', 'en'));
      }
      if (overrides.stage === 'rolled_back') {
        expect(byTestId('upgrade-result-rollback')).toBeTruthy();
        expect(byTestId('upgrade-result-incomplete')).toBeTruthy();
      }
      await act(async () => renderer!.unmount());
      renderer = undefined;
    }
  });

  it('reports a status read failure instead of assuming no operation exists', async () => {
    let first = true;
    const read: UpgradeWizardRead = vi.fn(async (resource: UpgradeWizardResource) => {
      if (resource === 'installation/upgrade/preflight') return preflightDocument();
      if (first) {
        first = false;
        throw new UpgradeWizardError('unavailable', 'upgrade_operation_unreadable', 503);
      }
      return unrecordedStatus();
    });
    await mount(read);

    expect(byTestId('upgrade-boot-failed')).toBeTruthy();
    expect(view()).toContain(blockedCodeText('upgrade_operation_unreadable', 'en'));
    expect(allByTestId('upgrade-preflight')).toHaveLength(0);

    // The retry reads the record again. Only then, with nothing recorded, does
    // the preflight step open.
    await press('upgrade-boot-retry');
    expect(byTestId('upgrade-preflight')).toBeTruthy();
  });

  it('renders the recorded mismatch total and claims only the bounded window', async () => {
    const shown = Array.from({ length: 366 }, (_, index) =>
      new Date(Date.UTC(2024, 0, 1 + index)).toISOString().slice(0, 10)
    );
    await mount(
      resumeRead(() =>
        statusDocument({
          stage: 'complete',
          nextAction: 'complete',
          mismatchDays: shown,
          mismatchDaysTotal: 400,
        })
      )
    );

    // The total is the recorded history; the list is a window of it.
    expect(byTestId('upgrade-reconciliation-total').children.join('')).toContain('400');
    expect(byTestId('upgrade-reconciliation-bound').children.join('')).toContain('366');
    expect(byTestId('upgrade-mismatch-days').findAllByType('li')).toHaveLength(366);
    expect(byTestId('upgrade-result-complete')).toBeTruthy();
  });

  it('refuses a mismatch window whose total contradicts the list', async () => {
    await mount(
      resumeRead(() =>
        statusDocument({
          stage: 'copy_in',
          mismatchDays: ['2026-10-01', '2026-10-02'],
          mismatchDaysTotal: 1,
        })
      )
    );

    // A reply this build cannot reconcile is a read failure, never a page that
    // silently shows fewer days than the server named.
    expect(byTestId('upgrade-boot-failed')).toBeTruthy();
    expect(view()).toContain(t('errorResponse', 'en'));
    expect(allByTestId('upgrade-reconciliation-total')).toHaveLength(0);
  });

  it('offers the verification exactly where the recorded operation asks for it', async () => {
    // A recorded CPA import is the position this build hands to the gate: it
    // recorded cpa_config, so the next decision is the verification that names
    // where the operation really stands.
    const recordedImport = resumeRead(() =>
      statusDocument({
        stage: 'cpa_import',
        nextAction: 'verify',
        domainsDone: ['source_copy', 'cpa_config'],
        manualItems: ['section:ampcode: re-create it (reconfigure_after_adoption)'],
      })
    );
    await mount(recordedImport, vi.fn());
    expect(byTestId('upgrade-verify-action')).toBeTruthy();
    expect(byTestId('upgrade-verify-offer')).toBeTruthy();
    await act(async () => renderer!.unmount());

    // A recorded copy-in is not a position the gate runs from, so the action is
    // not offered: it would only be refused.
    renderer = undefined;
    await mount(
      resumeRead(() => statusDocument({ stage: 'copy_in', nextAction: 'copy_in' })),
      vi.fn()
    );
    expect(allByTestId('upgrade-verify-action')).toHaveLength(0);
    await act(async () => renderer!.unmount());

    // A wizard mounted without the action stays a reader, even at a position the
    // gate would accept.
    renderer = undefined;
    await mount(recordedImport);
    expect(allByTestId('upgrade-verify-action')).toHaveLength(0);
  });

  it('presents the action verdict and then re-reads what the store recorded', async () => {
    const verified = statusDocument({
      stage: 'complete',
      nextAction: 'complete',
      domainsDone: ['source_copy', 'usage_events', 'cpa_config'],
      errorCode: '',
      errorDetail: '',
      manualItems: ['section:ampcode: re-create it (removed_upstream)'],
    });
    let current = statusDocument({
      stage: 'cpa_import',
      nextAction: 'verify',
      domainsDone: ['source_copy', 'usage_events', 'cpa_config'],
      manualItems: ['section:ampcode: re-create it (removed_upstream)'],
    });
    const read = resumeRead(() => current);
    const verify = vi.fn<UpgradeWizardVerify>(async () => {
      current = verified;
      return verifyDocument({
        domainsDone: verified.domainsDone,
        manualItems: verified.manualItems,
      });
    });
    await mount(read, verify);

    await press('upgrade-verify-action');
    expect(verify).toHaveBeenCalledTimes(1);
    expect(allByTestId('upgrade-result-complete')).toHaveLength(1);
    expect(allByTestId('upgrade-progress-reason')).toHaveLength(0);
    expect(view()).not.toContain('upgrade_verify_waiting_for_repin');
  });

  it('reports a refused verification instead of moving the operation on', async () => {
    const read = resumeRead(() => statusDocument({ stage: 'external_connect', nextAction: 'verify' }));
    const verify = vi.fn<UpgradeWizardVerify>(async () => {
      throw new UpgradeWizardError('unavailable', 'upgrade_verify_binding_drifted', 409);
    });
    await mount(read, verify);

    await press('upgrade-verify-action');
    expect(byTestId('upgrade-verify-failed')).toBeTruthy();
    expect(view()).toContain(blockedCodeText('upgrade_verify_binding_drifted', 'en'));
    expect(allByTestId('upgrade-verify-done')).toHaveLength(0);
  });

  it('refuses a verification reply whose position contradicts itself', () => {
    // The action records exactly two positions: `complete` on an External target
    // and `verify` (the Managed waiting position, which always carries a reason).
    // Anything else is a reply this build cannot present as a position.
    const contradictions: Array<Record<string, unknown>> = [
      // A finished stage that is not reported as complete.
      { stage: 'verify', complete: true },
      // A complete flag without the stage that means it.
      { stage: 'complete', complete: false },
      // The gate completes both target modes; a non-completion is a position
      // this build never records, with or without a reason.
      { complete: false, targetMode: 'external' },
      { complete: false, reasonCode: '' },
      { stage: 'verify', complete: false, targetMode: 'managed', reasonCode: 'upgrade_verify_waiting_for_repin' },
      // A stage this build cannot name is not a position it may present.
      { stageKnown: false },
      { stage: 'copy_in' },
    ];
    for (const contradiction of contradictions) {
      expect({ contradiction, parsed: parseUpgradeVerify(verifyDocument(contradiction)) }).toEqual({
        contradiction,
        parsed: null,
      });
    }

    // Both completions the gate records are read as they are.
    expect(parseUpgradeVerify(verifyDocument())).not.toBeNull();
    expect(
      parseUpgradeVerify(
        verifyDocument({ stage: 'complete', complete: true, targetMode: 'external', reasonCode: '' })
      )
    ).not.toBeNull();
  });

  it('reports a contradictory verification reply instead of presenting a completion', async () => {
    // `stage: 'verify'` with `complete: true` would render the completion note
    // for a position the gate never records: the reply must be a read failure.
    const read = resumeRead(() => statusDocument({ stage: 'cpa_import', nextAction: 'verify' }));
    const verify = vi.fn<UpgradeWizardVerify>(async () =>
      verifyDocument({ stage: 'verify', complete: true, targetMode: 'managed' })
    );
    await mount(read, verify);

    await press('upgrade-verify-action');
    expect(byTestId('upgrade-verify-failed')).toBeTruthy();
    expect(view()).toContain(t('errorResponse', 'en'));
    expect(allByTestId('upgrade-verify-done')).toHaveLength(0);
    expect(view()).not.toContain(t('verifyComplete', 'en'));
  });

  it('asks the verification route with a POST and the same credential, never a cookie', async () => {
    const credential = 'Cpamp_Legacy_Upgrade_Admin_Secret_11441';
    const calls: Array<{ url: string; init: Record<string, unknown> }> = [];
    const transport = vi.fn(async (input: string, init: Record<string, unknown>) => {
      calls.push({ url: input, init });
      return { ok: true, status: 200, json: async () => ({ contractVersion: CONTRACT }) };
    }) as unknown as typeof fetch;
    vi.stubGlobal('document', { getElementById: () => null });
    vi.stubGlobal('window', {
      setTimeout,
      clearTimeout,
      location: { origin: 'http://manager.test' },
    });

    const verify = createUpgradeWizardVerify(() => credential, transport);
    await verify(new AbortController().signal);

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe(
      'http://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/verify'
    );
    expect(calls[0].init).toMatchObject({
      method: 'POST',
      credentials: 'omit',
      mode: 'same-origin',
      redirect: 'error',
      headers: { Accept: 'application/json', Authorization: `Bearer ${credential}` },
    });
  });

  it('refuses to run the action without the entry credential', async () => {
    const transport = vi.fn() as unknown as typeof fetch;
    vi.stubGlobal('document', { getElementById: () => null });
    vi.stubGlobal('window', {
      setTimeout,
      clearTimeout,
      location: { origin: 'http://manager.test' },
    });

    await expect(
      createUpgradeWizardVerify(() => '', transport)(new AbortController().signal)
    ).rejects.toMatchObject({ failure: 'authentication' });
    expect(transport).not.toHaveBeenCalled();
  });

  it('renders the pinned engine preparation only at the CPA import stage', async () => {
    const digest = 'sha256:' + 'd'.repeat(64);
    const makeArtifactStatus = (overrides: Partial<ArtifactStatus> = {}): ArtifactStatus => ({
      schemaVersion: 1,
      pinned: { version: '8.0.55', artifactId: digest },
      active: null,
      required: true,
      operation: null,
      cdnPresets: [],
      ...overrides,
    });

    let currentArtifactStatus = makeArtifactStatus();
    const artifactTransport: ArtifactTransport = {
      readStatus: vi.fn(async () => currentArtifactStatus),
      probe: vi.fn(async () => ({ reachable: true, code: '' })),
      acquire: vi.fn<() => Promise<ArtifactOperation>>(async () => ({
        id: 'op-acq-1',
        source: 'github',
        phase: 'succeeded',
        receivedBytes: 1024,
        totalBytes: 1024,
        code: '',
      })),
      upload: vi.fn(),
    };

    // 1. Missing transport on cpa_import -> no artifact panel and never ready, host waiting instructions remain.
    await mount(
      resumeRead(() =>
        statusDocument({
          stage: 'reconcile',
          nextAction: 'cpa_import',
          domainsDone: ['source_copy', 'usage_events'],
        })
      )
    );
    expect(allByTestId('fresh-artifact-step')).toHaveLength(0);
    expect(allByTestId('upgrade-cpa-import-ready')).toHaveLength(0);
    expect(byTestId('upgrade-progress-waiting')).toBeTruthy();
    await act(async () => renderer!.unmount());
    renderer = undefined;

    // 2. Non-cpa_import actions (external_connect, copy_in, reconcile, verify) -> no artifact panel even with transport.
    for (const nextAction of ['external_connect', 'copy_in', 'reconcile', 'verify']) {
      await mount(
        resumeRead(() =>
          statusDocument({
            stage: nextAction === 'verify' ? 'cpa_import' : nextAction,
            nextAction,
            domainsDone: ['source_copy'],
          })
        ),
        vi.fn(),
        artifactTransport
      );
      expect(allByTestId('fresh-artifact-step')).toHaveLength(0);
      expect(allByTestId('upgrade-cpa-import-ready')).toHaveLength(0);
      await act(async () => renderer!.unmount());
      renderer = undefined;
    }
    expect(artifactTransport.readStatus).not.toHaveBeenCalled();

    // 3. Mismatched active.artifactId !== pinned.artifactId on cpa_import -> never ready, shows pinned_mismatch error.
    currentArtifactStatus = makeArtifactStatus({
      active: { artifactId: 'sha256:' + 'f'.repeat(64), origin: 'bundled' },
      required: false,
    });
    await mount(
      resumeRead(() =>
        statusDocument({
          stage: 'reconcile',
          nextAction: 'cpa_import',
          domainsDone: ['source_copy', 'usage_events'],
        })
      ),
      vi.fn(),
      artifactTransport
    );
    expect(byTestId('fresh-artifact-step')).toBeTruthy();
    expect(byTestId('artifact-error').props['data-error-code']).toBe('pinned_mismatch');
    expect(allByTestId('upgrade-cpa-import-ready')).toHaveLength(0);
    await act(async () => renderer!.unmount());
    renderer = undefined;

    // 3b. Both active.artifactId !== pinned.artifactId AND recovery_required -> recovery_required wins, onReady not called, acquire/upload absent.
    currentArtifactStatus = makeArtifactStatus({
      active: { artifactId: 'sha256:' + 'f'.repeat(64), origin: 'bundled' },
      required: false,
      operation: {
        id: 'op-rec-mismatch',
        source: 'upload',
        phase: 'failed',
        receivedBytes: 512,
        totalBytes: 512,
        code: 'recovery_required',
      },
    });
    await mount(
      resumeRead(() =>
        statusDocument({
          stage: 'reconcile',
          nextAction: 'cpa_import',
          domainsDone: ['source_copy', 'usage_events'],
        })
      ),
      vi.fn(),
      artifactTransport
    );
    expect(byTestId('fresh-artifact-step')).toBeTruthy();
    expect(byTestId('artifact-error').props['data-error-code']).toBe('recovery_required');
    expect(allByTestId('upgrade-cpa-import-ready')).toHaveLength(0);
    expect(allByTestId('artifact-acquire-btn')).toHaveLength(0);
    expect(allByTestId('artifact-upload-btn')).toHaveLength(0);
    expect(allByTestId('artifact-source-github')).toHaveLength(0);
    expect(allByTestId('artifact-source-upload')).toHaveLength(0);
    await act(async () => renderer!.unmount());
    renderer = undefined;

    // 4. On cpa_import with artifact transport -> panel renders, not ready while required === true, then shows host hint on ready.
    currentArtifactStatus = makeArtifactStatus();
    await mount(
      resumeRead(() =>
        statusDocument({
          stage: 'reconcile',
          nextAction: 'cpa_import',
          domainsDone: ['source_copy', 'usage_events'],
        })
      ),
      vi.fn(),
      artifactTransport
    );
    expect(byTestId('fresh-artifact-step')).toBeTruthy();
    expect(allByTestId('upgrade-cpa-import-ready')).toHaveLength(0);
    expect(artifactTransport.readStatus).toHaveBeenCalledTimes(3);

    // Engine readiness does not claim the recorded CPA import completed.
    currentArtifactStatus = makeArtifactStatus({
      active: { artifactId: digest, origin: 'acquired' },
      required: false,
    });
    await press('artifact-acquire-btn');
    expect(byTestId('upgrade-cpa-import-ready')).toBeTruthy();
    expect(allByTestId('upgrade-result-complete')).toHaveLength(0);
  });

  it('configures dual random prefixes for first-time Managed upgrade, blocks overlaps locally, saves routing, and runs verify', async () => {
    let current = statusDocument({
      stage: 'cpa_import',
      nextAction: 'verify',
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
    });
    const read = resumeRead(() => current);
    const saveRouting = vi.fn<UpgradeWizardSaveRouting>(async (_sig, input) => {
      current = statusDocument({
        ...current,
        targetMode: 'managed',
        routing: {
          configured: true,
          committed: false,
          existingPrefixesPreserved: false,
          controlBasePath: input.controlBasePath,
          inferenceBasePath: input.inferenceBasePath,
          sharedEntry: input.sharedEntry,
          externalBaseUrl: '',
          suggestedControlBasePath: '/ctl1234567890abcd',
          suggestedInferenceBasePath: '/inf1234567890abcd',
        },
      });
      return current;
    });
    const verify = vi.fn<UpgradeWizardVerify>(async (_sig, input) => {
      const accessInfo: AccessInfo = {
        mode: 'migrate_legacy',
        managementBasePath: input!.controlBasePath,
        inferenceBasePath: input!.inferenceBasePath,
        protocols: [
          {
            id: 'openai',
            label: 'OpenAI',
            basePath: `${input!.inferenceBasePath}/v1`,
            examplePath: `${input!.inferenceBasePath}/v1/chat/completions`,
          },
        ],
        ready: false,
        code: 'upgrade_restart_required',
      };
      current = statusDocument({
        stage: 'complete',
        nextAction: 'complete',
        targetMode: 'managed',
        routing: {
          configured: true,
          committed: true,
          existingPrefixesPreserved: false,
          controlBasePath: input!.controlBasePath,
          inferenceBasePath: input!.inferenceBasePath,
          sharedEntry: input!.sharedEntry,
          externalBaseUrl: '',
          suggestedControlBasePath: '',
          suggestedInferenceBasePath: '',
        },
        accessInfo,
      });
      return verifyDocument({ targetMode: 'managed', accessInfo });
    });

    await mount(read, verify, undefined, saveRouting);
    expect(byTestId('upgrade-routing-config')).toBeTruthy();
    expect(byTestId('upgrade-control-prefix-input').props.value).toBe('/ctl1234567890abcd');
    expect(byTestId('upgrade-inference-prefix-input').props.value).toBe('/inf1234567890abcd');
    expect(byTestId('upgrade-shared-prefix-checkbox').props.checked).toBe(false);

    // Reject nested prefix locally without posting
    await act(async () => {
      byTestId('upgrade-inference-prefix-input').props.onChange({
        target: { value: '/ctl1234567890abcd/nested' },
      });
    });
    await press('upgrade-save-routing');
    expect(byTestId('upgrade-routing-error')).toBeTruthy();
    expect(saveRouting).not.toHaveBeenCalled();
    expect(verify).not.toHaveBeenCalled();

    // Edit valid distinct prefix, save explicitly, then verify
    await act(async () => {
      byTestId('upgrade-inference-prefix-input').props.onChange({
        target: { value: '/inf-custom-v2' },
      });
    });
    expect(view()).toContain('/inf-custom-v2');
    await press('upgrade-save-routing');
    expect(saveRouting).toHaveBeenCalledTimes(1);
    expect(byTestId('upgrade-routing-saved')).toBeTruthy();

    await press('upgrade-verify-action');
    expect(verify).toHaveBeenCalledTimes(1);
    expect(verify.mock.calls[0][1]).toEqual({
      controlBasePath: '/ctl1234567890abcd',
      inferenceBasePath: '/inf-custom-v2',
      sharedEntry: false,
    });
    expect(byTestId('upgrade-result-access')).toBeTruthy();
    expect(byTestId('upgrade-access-pending-restart')).toBeTruthy();
    expect(allByTestId('upgrade-access-active')).toHaveLength(0);
    expect(byTestId('access-info-summary')).toBeTruthy();
  });

  it('adopts prefixes staged by another session before verifying an untouched form', async () => {
    const routing = {
      configured: false, committed: false, existingPrefixesPreserved: false,
      controlBasePath: '', inferenceBasePath: '', sharedEntry: false,
      externalBaseUrl: '', suggestedControlBasePath: '/suggested-admin',
      suggestedInferenceBasePath: '/suggested-inference',
    };
    let current = statusDocument({ stage: 'cpa_import', nextAction: 'verify', targetMode: 'managed', routing });
    const verify = vi.fn<UpgradeWizardVerify>(async () => verifyDocument());
    const saveRouting = vi.fn<UpgradeWizardSaveRouting>();
    await mount(resumeRead(() => current), verify, undefined, saveRouting);
    expect(byTestId('upgrade-control-prefix-input').props.value).toBe('/suggested-admin');
    current = statusDocument({
      ...current,
      routing: { ...routing, configured: true, controlBasePath: '/canonical-admin', inferenceBasePath: '/canonical-inference' },
    });
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(byTestId('upgrade-control-prefix-input').props.value).toBe('/canonical-admin');
    expect(byTestId('upgrade-inference-prefix-input').props.value).toBe('/canonical-inference');
    await press('upgrade-verify-action');
    expect(verify.mock.calls[0][1]).toEqual({
      controlBasePath: '/canonical-admin', inferenceBasePath: '/canonical-inference', sharedEntry: false,
    });
    expect(saveRouting).not.toHaveBeenCalled();
  });

  it('retains saved prefixes when Verify fails before a new status read', async () => {
    const routing = {
      configured: true, committed: false, existingPrefixesPreserved: false,
      controlBasePath: '/old-admin', inferenceBasePath: '/old-inference', sharedEntry: false,
      externalBaseUrl: '', suggestedControlBasePath: '', suggestedInferenceBasePath: '',
    };
    let current = statusDocument({ stage: 'cpa_import', nextAction: 'verify', targetMode: 'managed', routing });
    const saveRouting = vi.fn<UpgradeWizardSaveRouting>(async (_signal, input) => {
      current = statusDocument({ ...current, routing: { ...routing, ...input } });
      return { contractVersion: CONTRACT, targetMode: current.targetMode, routing: current.routing, accessInfo: current.accessInfo, updatedAtMs: 1234 };
    });
    const verify = vi.fn<UpgradeWizardVerify>()
      .mockRejectedValueOnce(new Error('offline'))
      .mockResolvedValue(verifyDocument());
    await mount(resumeRead(() => current), verify, undefined, saveRouting);
    await act(async () => {
      byTestId('upgrade-control-prefix-input').props.onChange({ target: { value: '/new-admin' } });
    });
    await press('upgrade-verify-action');
    expect(byTestId('upgrade-verify-failed')).toBeTruthy();
    expect(byTestId('upgrade-control-prefix-input').props.value).toBe('/new-admin');
    await press('upgrade-verify-action');
    expect(verify.mock.calls[1][1]).toEqual({
      controlBasePath: '/new-admin', inferenceBasePath: '/old-inference', sharedEntry: false,
    });
  });

  it('drops pre-save status replies and prevents Verify while a routing save is pending', async () => {
    const routing = {
      configured: true, committed: false, existingPrefixesPreserved: false,
      controlBasePath: '/old-admin', inferenceBasePath: '/old-inference', sharedEntry: false,
      externalBaseUrl: '', suggestedControlBasePath: '', suggestedInferenceBasePath: '',
    };
    let current = statusDocument({ stage: 'cpa_import', nextAction: 'verify', targetMode: 'managed', routing });
    const old = current;
    let finishRead!: (value: unknown) => void;
    const pendingRead = new Promise<unknown>((resolve) => { finishRead = resolve; });
    let delayPoll = false;
    const read = resumeRead(() => {
      if (delayPoll) { delayPoll = false; return pendingRead; }
      return current;
    });
    let finishSave!: () => void;
    const pendingSave = new Promise<void>((resolve) => { finishSave = resolve; });
    const saveRouting = vi.fn<UpgradeWizardSaveRouting>(async (_signal, input) => {
      await pendingSave;
      current = statusDocument({ ...current, routing: { ...routing, ...input } });
      return { contractVersion: CONTRACT, targetMode: current.targetMode, routing: current.routing, updatedAtMs: 1234 };
    });
    const verify = vi.fn<UpgradeWizardVerify>(async () => verifyDocument());
    await mount(read, verify, undefined, saveRouting);
    delayPoll = true;
    await act(async () => { vi.advanceTimersByTime(10_000); await Promise.resolve(); });
    await act(async () => {
      byTestId('upgrade-control-prefix-input').props.onChange({ target: { value: '/new-admin' } });
    });
    let saving!: Promise<void>;
    await act(async () => { saving = byTestId('upgrade-save-routing').props.onClick(); });
    expect(byTestId('upgrade-verify-action').props.disabled).toBe(true);
    await press('upgrade-verify-action');
    expect(verify).not.toHaveBeenCalled();
    await act(async () => {
      finishSave();
      await Promise.resolve();
      finishRead(old);
      await saving;
    });
    expect(byTestId('upgrade-control-prefix-input').props.value).toBe('/new-admin');
    await press('upgrade-verify-action');
    expect(verify.mock.calls[0][1]).toEqual({ controlBasePath: '/new-admin', inferenceBasePath: '/old-inference', sharedEntry: false });
  });

  it('shows only management prefix + external URL on External upgrade and distinguishes active vs pending-restart access info', async () => {
    const externalProtocols = [
      {
        id: 'openai',
        label: 'OpenAI',
        basePath: 'https://cpa.upstream.example:8317/v1',
        examplePath: 'https://cpa.upstream.example:8317/v1/chat/completions',
      },
    ];
    let current = statusDocument({
      stage: 'external_connect',
      nextAction: 'verify',
      targetMode: 'external',
      routing: {
        configured: false,
        committed: false,
        existingPrefixesPreserved: false,
        controlBasePath: '',
        inferenceBasePath: '',
        sharedEntry: false,
        externalBaseUrl: 'https://cpa.upstream.example:8317',
        suggestedControlBasePath: '/extctl1234567890',
        suggestedInferenceBasePath: '',
      },
      accessInfo: {
        mode: 'connect_external',
        managementBasePath: '',
        inferenceBasePath: '',
        externalBaseUrl: 'https://cpa.upstream.example:8317',
        protocols: externalProtocols,
        ready: false,
        code: 'upgrade_access_prefix_required',
      },
    });
    const read = resumeRead(() => current);
    const saveRouting = vi.fn<UpgradeWizardSaveRouting>(async (_sig, input) => {
      expect(input).toEqual({
        controlBasePath: '/extctl1234567890',
        inferenceBasePath: '',
        sharedEntry: false,
      });
      current = statusDocument({ ...current, routing: { ...current.routing!, ...input, configured: true } });
      return { contractVersion: CONTRACT, targetMode: current.targetMode, routing: current.routing, accessInfo: current.accessInfo, updatedAtMs: 1234 };
    });
    const verify = vi.fn<UpgradeWizardVerify>(async () => {
      const accessInfo: AccessInfo = {
        mode: 'connect_external',
        managementBasePath: '/extctl1234567890',
        inferenceBasePath: '',
        externalBaseUrl: 'https://cpa.upstream.example:8317',
        protocols: externalProtocols,
        ready: true,
        code: '',
      };
      current = statusDocument({
        stage: 'complete',
        nextAction: 'complete',
        targetMode: 'external',
        accessInfo,
      });
      return verifyDocument({ targetMode: 'external', accessInfo });
    });

    await mount(read, verify, undefined, saveRouting);
    expect(byTestId('upgrade-control-prefix-input').props.value).toBe('/extctl1234567890');
    expect(allByTestId('upgrade-inference-prefix-input')).toHaveLength(0);
    expect(allByTestId('upgrade-shared-prefix-checkbox')).toHaveLength(0);
    expect(view()).toContain('https://cpa.upstream.example:8317');
    expect(
      byTestId('upgrade-external-protocols')
        .findAllByType('code')
        .map((node) => node.children.join(''))
    ).toContain('https://cpa.upstream.example:8317/v1/chat/completions');

    await press('upgrade-verify-action');
    expect(saveRouting).toHaveBeenCalledTimes(1);
    expect(verify).toHaveBeenCalledTimes(1);
    expect(byTestId('upgrade-access-active')).toBeTruthy();
    expect(allByTestId('upgrade-access-pending-restart')).toHaveLength(0);
    expect(byTestId('access-info-external')).toBeTruthy();
    expect(byTestId('access-info-protocols')).toBeTruthy();
  });

  it('surfaces fenced External Upgrade URL and protocol endpoint list pre-restart, in preserved mode, and distinguishes external_target_unavailable', async () => {
    const externalProtocols = [
      {
        id: 'openai',
        label: 'OpenAI',
        basePath: 'https://cpa.upstream.example:8317/cpa/v1',
        examplePath: 'https://cpa.upstream.example:8317/cpa/v1/chat/completions',
      },
      {
        id: 'responses',
        label: 'Responses',
        basePath: 'https://cpa.upstream.example:8317/cpa/v1',
        examplePath: 'https://cpa.upstream.example:8317/cpa/v1/responses',
      },
    ];
    let current = statusDocument({
      stage: 'external_connect',
      nextAction: 'verify',
      targetMode: 'external',
      routing: {
        configured: true,
        committed: true,
        existingPrefixesPreserved: true,
        controlBasePath: '/ext-preserved',
        inferenceBasePath: '',
        sharedEntry: false,
        externalBaseUrl: 'https://cpa.upstream.example:8317/cpa',
        suggestedControlBasePath: '',
        suggestedInferenceBasePath: '',
      },
      accessInfo: {
        mode: 'connect_external',
        managementBasePath: '/ext-preserved',
        inferenceBasePath: '/should-be-cleared',
        externalBaseUrl: 'https://cpa.upstream.example:8317/cpa',
        protocols: externalProtocols,
        ready: false,
        code: 'upgrade_restart_required',
      },
    });
    const read = resumeRead(() => current);
    const verify = vi.fn<UpgradeWizardVerify>(async () => {
      const accessInfo: AccessInfo = {
        mode: 'connect_external',
        managementBasePath: '/ext-preserved',
        inferenceBasePath: '',
        externalBaseUrl: '',
        protocols: [],
        ready: false,
        code: 'external_target_unavailable',
      };
      current = statusDocument({
        stage: 'complete',
        nextAction: 'complete',
        targetMode: 'external',
        routing: {
          configured: true,
          committed: true,
          existingPrefixesPreserved: true,
          controlBasePath: '/ext-preserved',
          inferenceBasePath: '',
          sharedEntry: false,
          externalBaseUrl: '',
          suggestedControlBasePath: '',
          suggestedInferenceBasePath: '',
        },
        accessInfo,
      });
      return verifyDocument({ targetMode: 'external', accessInfo });
    });
    await mount(read, verify);

    expect(byTestId('upgrade-preserved-routing')).toBeTruthy();
    expect(byTestId('upgrade-preserved-inference').findByType('code').children.join('')).toBe(
      'https://cpa.upstream.example:8317/cpa'
    );
    const protocolCodes = byTestId('upgrade-external-protocols')
      .findAllByType('code')
      .map((node) => node.children.join(''));
    expect(protocolCodes).toContain('https://cpa.upstream.example:8317/cpa/v1/chat/completions');
    expect(protocolCodes).toContain('https://cpa.upstream.example:8317/cpa/v1/responses');

    await press('upgrade-verify-action');
    expect(byTestId('upgrade-result-access')).toBeTruthy();
    expect(allByTestId('upgrade-access-pending-restart')).toHaveLength(0);
    expect(byTestId('access-info-not-ready')).toBeTruthy();
  });

  it('locks existing prefixes read-only when existingPrefixesPreserved is true', async () => {
    const read = resumeRead(() =>
      statusDocument({
        stage: 'cpa_import',
        nextAction: 'verify',
        targetMode: 'managed',
        routing: {
          configured: true,
          committed: true,
          existingPrefixesPreserved: true,
          controlBasePath: '/preserved-ctl',
          inferenceBasePath: '/preserved-inf',
          sharedEntry: false,
          externalBaseUrl: '',
          suggestedControlBasePath: '',
          suggestedInferenceBasePath: '',
        },
      })
    );
    const saveRouting = vi.fn<UpgradeWizardSaveRouting>();
    const verify = vi.fn<UpgradeWizardVerify>(async () => verifyDocument());
    await mount(read, verify, undefined, saveRouting);

    expect(byTestId('upgrade-preserved-routing')).toBeTruthy();
    expect(allByTestId('upgrade-routing-config')).toHaveLength(0);
    expect(byTestId('upgrade-preserved-control').findByType('code').children.join('')).toBe('/preserved-ctl');
    expect(byTestId('upgrade-preserved-inference').findByType('code').children.join('')).toBe('/preserved-inf');

    await press('upgrade-verify-action');
    expect(saveRouting).not.toHaveBeenCalled();
    expect(verify).toHaveBeenCalledTimes(1);
    expect(verify.mock.calls[0][1]).toBeUndefined();
  });

  it('posts JSON routing payloads and parses error.code / code / Code envelopes in createUpgradeWizardSaveRouting', async () => {
    const credential = 'Cpamp_Upgrade_Save_Routing_Key_9911';
    const transport = vi
      .fn()
      .mockResolvedValueOnce(Response.json({ ok: true }))
      .mockResolvedValueOnce(Response.json({ error: { code: 'prefix_overlap_forbidden' } }, { status: 400 }))
      .mockResolvedValueOnce(Response.json({ code: 'upgrade_prefixes_already_committed' }, { status: 409 }))
      .mockResolvedValueOnce(Response.json({ Code: 'reserved_prefix_collision' }, { status: 400 })) as unknown as typeof fetch;
    vi.stubGlobal('document', { getElementById: () => null });
    vi.stubGlobal('window', {
      setTimeout,
      clearTimeout,
      location: { origin: 'http://manager.test' },
    });

    const save = createUpgradeWizardSaveRouting(() => credential, transport);
    const payload = { controlBasePath: '/ctl-1', inferenceBasePath: '/inf-1', sharedEntry: false };
    await save(new AbortController().signal, payload);
    expect(vi.mocked(transport).mock.calls[0]).toEqual([
      'http://manager.test/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/routing',
      expect.objectContaining({
        method: 'POST',
        headers: {
          Accept: 'application/json',
          'Content-Type': 'application/json',
          Authorization: `Bearer ${credential}`,
        },
        body: JSON.stringify(payload),
      }),
    ]);
    await expect(save(new AbortController().signal, payload)).rejects.toMatchObject({
      code: 'prefix_overlap_forbidden',
    });
    await expect(save(new AbortController().signal, payload)).rejects.toMatchObject({
      code: 'upgrade_prefixes_already_committed',
    });
    await expect(save(new AbortController().signal, payload)).rejects.toMatchObject({
      code: 'reserved_prefix_collision',
    });
  });

  it('invokes the latest onRecordedOperation callback replaced during active boot without restarting or aborting the boot read', async () => {
    let resolveBoot!: (value: unknown) => void;
    let bootSignal: AbortSignal | undefined;
    const read = vi.fn<UpgradeWizardRead>(async (resource, signal) => {
      if (resource === 'installation/upgrade/preflight') return preflightDocument();
      bootSignal = signal;
      return new Promise((resolve) => {
        resolveBoot = resolve;
      });
    });
    const delays = [10_000];
    const initialCb = vi.fn();
    const replacementCb = vi.fn();

    await act(async () => {
      renderer = create(
        <FreshUpgradeFlow
          read={read}
          language="en"
          pollDelays={delays}
          onRecordedOperation={initialCb}
        />
      );
    });

    expect(byTestId('upgrade-boot-reading')).toBeTruthy();
    expect(read).toHaveBeenCalledTimes(1);
    expect(bootSignal?.aborted).toBe(false);

    await act(async () => {
      renderer!.update(
        <FreshUpgradeFlow
          read={read}
          language="zh-CN"
          pollDelays={delays}
          onRecordedOperation={replacementCb}
        />
      );
    });

    expect(read).toHaveBeenCalledTimes(1);
    expect(bootSignal?.aborted).toBe(false);

    await act(async () => {
      resolveBoot(unrecordedStatus());
    });

    expect(initialCb).not.toHaveBeenCalled();
    expect(replacementCb).toHaveBeenCalledTimes(1);
    expect(replacementCb).toHaveBeenCalledWith(false);
    expect(byTestId('upgrade-preflight')).toBeTruthy();
  });

  it('invokes the latest onRecordedOperation callback replaced between and during active progress polls without restarting polling', async () => {
    let statusCall = 0;
    let resolvePendingPoll: ((value: unknown) => void) | undefined;
    let pendingPollSignal: AbortSignal | undefined;
    const read = vi.fn<UpgradeWizardRead>(async (resource, signal) => {
      if (resource !== 'installation/upgrade/status') {
        throw new Error(`Unexpected resource ${resource}`);
      }
      statusCall += 1;
      if (statusCall <= 2) {
        return statusDocument({ stage: 'copy_in', nextAction: 'copy_in' });
      }
      if (statusCall === 3) {
        return statusDocument({ stage: 'reconcile', nextAction: 'reconcile' });
      }
      pendingPollSignal = signal;
      return new Promise((resolve) => {
        resolvePendingPoll = resolve;
      });
    });
    const delays = [10_000];
    const cb1 = vi.fn();
    const cb2 = vi.fn();
    const cb3 = vi.fn();

    await act(async () => {
      renderer = create(
        <FreshUpgradeFlow
          read={read}
          language="en"
          pollDelays={delays}
          onRecordedOperation={cb1}
        />
      );
    });

    expect(byTestId('upgrade-progress')).toBeTruthy();
    const cb1InitialCalls = cb1.mock.calls.length;
    expect(cb1InitialCalls).toBeGreaterThanOrEqual(1);
    const readsBeforeUpdate = read.mock.calls.length;

    await act(async () => {
      renderer!.update(
        <FreshUpgradeFlow
          read={read}
          language="zh-CN"
          pollDelays={delays}
          onRecordedOperation={cb2}
        />
      );
    });

    expect(read).toHaveBeenCalledTimes(readsBeforeUpdate);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });

    expect(cb1).toHaveBeenCalledTimes(cb1InitialCalls);
    expect(cb2).toHaveBeenCalledTimes(1);
    expect(cb2).toHaveBeenCalledWith(true);
    expect(view()).toContain(t('stageReconcile', 'zh-CN'));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });

    expect(pendingPollSignal?.aborted).toBe(false);
    const readsBeforeInflightUpdate = read.mock.calls.length;

    await act(async () => {
      renderer!.update(
        <FreshUpgradeFlow
          read={read}
          language="en"
          pollDelays={delays}
          onRecordedOperation={cb3}
        />
      );
    });

    expect(read).toHaveBeenCalledTimes(readsBeforeInflightUpdate);
    expect(pendingPollSignal?.aborted).toBe(false);

    await act(async () => {
      resolvePendingPoll!(statusDocument({ stage: 'complete', nextAction: 'complete' }));
    });

    expect(cb2).toHaveBeenCalledTimes(1);
    expect(cb3).toHaveBeenCalledTimes(1);
    expect(cb3).toHaveBeenCalledWith(true);
    expect(byTestId('upgrade-result-complete')).toBeTruthy();
  });

  it('marks completed result step as complete and collapses result protocol details by default', async () => {
    const completedManaged = statusDocument({
      stage: 'complete',
      nextAction: 'complete',
      targetMode: 'managed',
      accessInfo: {
        mode: 'migrate_legacy',
        managementBasePath: '/ctl1234567890abcd',
        inferenceBasePath: '/inf1234567890abcd',
        protocols: [
          {
            id: 'openai',
            label: 'OpenAI',
            basePath: '/inf1234567890abcd/v1',
            examplePath: '/inf1234567890abcd/v1/chat/completions',
          },
        ],
        ready: true,
        code: '',
      },
    });
    await mount(resumeRead(() => completedManaged));
    expect(byTestId('upgrade-step-result').props['data-complete']).toBe('true');
    expect(byTestId('access-info-protocols-disclosure').props.open).toBeUndefined();
    expect(view()).toContain(t('modeBadgeManaged', 'en'));
  });

  it('keeps diagnostics optional and offers deployment-specific recovery without execution support', async () => {
    await mount(
      wizardRead({
        recorded: () => unrecordedStatus(),
        preflight: () =>
          preflightDocument({
            sourceKind: 'legacy-manager',
            checks: [
              { name: 'source_ownership', status: 'pass', code: '', detail: 'source lock held read-only' },
              { name: 'source_version', status: 'pending', code: 'upgrade_source_version_declared_at_copy_in', detail: 'expected=v1.14.10 declared=none' },
              { name: 'fingerprint', status: 'pass', code: '', detail: 'sha256 over source manifest; sourceBytes=667705' },
              { name: 'disk', status: 'pass', code: '', detail: 'requiredBytes=801246 availableBytes=1701408768' },
              { name: 'cpa_classification', status: 'pass', code: '', detail: 'kind=managed_stack cpaVersion=unknown' },
              { name: 'parity', status: 'pass', code: '', detail: 'deletedEventRefs=0 missingProjection=0 mismatchedProjection=0 staleProjection=0 pastWatermark=0 coverageDaysWithGaps=0' },
            ],
            pendingDeclaration: true,
            expectedVersion: 'v1.14.10',
          }),
      })
    );
    expect(byTestId('upgrade-preflight-kind').children.join('')).toBe('CPAMP 1.x Manager');
    expect(view()).toContain('782.5 KB');
    expect(view()).toContain('1.58 GB');

    await press('upgrade-continue');
    await acknowledge();
    await press('upgrade-confirm-continue');
    expect(byTestId('upgrade-execution-recovery')).toBeTruthy();
    expect(view()).not.toContain('docker run');
    expect(view()).not.toContain('docker exec');
    expect(allByTestId('upgrade-execution-actions')).toHaveLength(0);
    expect(byTestId('upgrade-progress-refresh')).toBeTruthy();
    await press('upgrade-progress-back');
    expect(byTestId('upgrade-confirm')).toBeTruthy();
  });

  it('keeps custom prefix drawer and clean migration disclosures collapsed by default while auto-opening on routing errors', async () => {
    const read = resumeRead(() =>
      statusDocument({
        stage: 'cpa_import',
        nextAction: 'verify',
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
      })
    );
    const saveRouting = vi.fn<UpgradeWizardSaveRouting>();
    await mount(read, vi.fn(), undefined, saveRouting);

    const drawer = renderer!.root.findByProps({ className: 'upgrade-routing-drawer' });
    expect(drawer.props.open).toBe(false);

    await act(async () => {
      byTestId('upgrade-control-prefix-input').props.onChange({ target: { value: 'bad-prefix' } });
    });
    await press('upgrade-verify-action');
    expect(byTestId('upgrade-routing-error')).toBeTruthy();
    expect(renderer!.root.findByProps({ className: 'upgrade-routing-drawer' }).props.open).toBe(
      true
    );
  });

  it('gates Managed import on pinned engine readiness and keeps External an explicit alternate', async () => {
    const digest = 'sha256:' + 'd'.repeat(64);
    const artifactTransport: ArtifactTransport = {
      readStatus: vi.fn<ArtifactTransport['readStatus']>(async () => ({
        schemaVersion: 1,
        pinned: { version: '8.0.55', artifactId: digest },
        active: null,
        required: true,
        operation: null,
        cdnPresets: [],
      })),
      probe: vi.fn(async () => ({ reachable: true, code: '' })),
      acquire: vi.fn<() => Promise<ArtifactOperation>>(async () => ({
        id: 'op-slim-upg',
        source: 'github',
        phase: 'succeeded',
        receivedBytes: 100,
        totalBytes: 100,
        code: '',
      })),
      upload: vi.fn(),
    };

    await mount(
      resumeRead(() => statusDocument({ stage: 'reconcile', nextAction: 'cpa_import', execution: workerReady })),
      vi.fn(),
      artifactTransport,
      undefined,
      vi.fn(async () => executionDocument())
    );

    const serializedBefore = view();
    expect(serializedBefore.indexOf('upgrade-cpa-import-artifact')).toBeLessThan(
      serializedBefore.indexOf('upgrade-execution-actions')
    );
    expect(byTestId('upgrade-execute-import').props.disabled).toBe(true);
    await act(async () => byTestId('upgrade-plugins').props.onChange({ target: { value: 'disabled' } }));
    expect(byTestId('upgrade-execute-import').props.disabled).toBe(true);

    await press('upgrade-mode-external');
    expect(allByTestId('upgrade-cpa-import-artifact')).toHaveLength(0);

    await press('upgrade-mode-managed');
    expect(byTestId('upgrade-cpa-import-artifact')).toBeTruthy();

    await press('artifact-acquire-btn');
    expect(byTestId('upgrade-cpa-import-ready')).toBeTruthy();
    expect(byTestId('upgrade-execute-import').props.disabled).toBe(false);
  });

  it('accepts optional execution observations but refuses malformed worker state', () => {
    expect(parseUpgradeStatus(statusDocument())?.execution).toBeUndefined();
    expect(parseUpgradeStatus(statusDocument({ execution: workerReady }))?.execution).toEqual(workerReady);
    for (const execution of [null, {}, { ...workerReady, running: 'yes' },
      { ...workerReady, action: 'unknown' }, { ...workerReady, code: 'unbounded diagnostic text' }]) {
      expect(parseUpgradeStatus(statusDocument({ execution: execution as never }))).toBeNull();
      expect(parseUpgradeExecution(execution)).toBeNull();
    }
  });

  it('resumes a prepared Managed operation, requires plugin choice and never advances on acceptance alone', async () => {
    let current = statusDocument({ nextAction: 'cpa_import', execution: workerReady, sourceVersion: '' });
    const read = resumeRead(() => current);
    const execute = vi.fn<UpgradeWizardExecute>(async () => executionDocument({ action: 'cpa-import' }));
    await mount(read, undefined, undefined, undefined, execute);
    expect(allByTestId('upgrade-preflight')).toHaveLength(0);
    expect(allByTestId('upgrade-confirm')).toHaveLength(0);
    expect(byTestId('upgrade-mode-managed').props['aria-pressed']).toBe(true);
    expect(byTestId('upgrade-execute-import').props.disabled).toBe(true);
    await act(async () => byTestId('upgrade-plugins').props.onChange({ target: { value: 'disabled' } }));
    current = statusDocument({ nextAction: 'cpa_import', execution: { ...workerReady, running: true, action: 'cpa-import' } });
    let pending!: Promise<void>;
    await act(async () => {
      const action = byTestId('upgrade-execute-import').props.onClick;
      pending = action();
      await action();
      await pending;
    });
    expect(execute).toHaveBeenCalledTimes(1);
    expect(execute.mock.calls[0][1]).toEqual({ action: 'cpa-import', pluginsEnabled: false });
    expect(byTestId('upgrade-execute-import').props.disabled).toBe(true);
    expect(allByTestId('upgrade-result-complete')).toHaveLength(0);
    expect(view()).not.toContain('docker exec');
    current = statusDocument({ stage: 'cpa_import', nextAction: 'verify', targetMode: 'managed', execution: workerReady });
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(byTestId('upgrade-verify-action')).toBeTruthy();
    expect(allByTestId('upgrade-result-complete')).toHaveLength(0);
  });

  it('requires the exact disclosed history days and refuses a truncated acknowledgement window', async () => {
    let current = statusDocument({ nextAction: 'reconcile', mismatchDays: ['2026-10-01', '2026-10-02'], execution: workerReady });
    const execute = vi.fn<UpgradeWizardExecute>(async () => executionDocument());
    await mount(resumeRead(() => current), undefined, undefined, undefined, execute);
    expect(byTestId('upgrade-execute-reconcile').props.disabled).toBe(true);
    await act(async () => {
      byTestId('upgrade-ack-day-2026-10-01').props.onChange({ target: { checked: true } });
      byTestId('upgrade-ack-parity').props.onChange({ target: { checked: true } });
    });
    expect(byTestId('upgrade-execute-reconcile').props.disabled).toBe(true);
    await act(async () => byTestId('upgrade-ack-day-2026-10-02').props.onChange({ target: { checked: true } }));
    expect(byTestId('upgrade-execute-reconcile').props.disabled).toBe(false);
    await press('upgrade-execute-reconcile');
    expect(execute.mock.calls[0][1]).toEqual({ action: 'reconcile', acknowledgeDays: ['2026-10-01', '2026-10-02'], acknowledgeParityGaps: true });
    current = statusDocument({ nextAction: 'reconcile', mismatchDays: ['2026-10-01'], mismatchDaysTotal: 400, execution: workerReady });
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(byTestId('upgrade-execute-reconcile').props.disabled).toBe(true);
    expect(execute).toHaveBeenCalledTimes(1);
  });

  it('offers External reuse only explicitly and reports an unavailable executor without commands', async () => {
    let current = statusDocument({ nextAction: 'cpa_import', execution: { ...workerReady, managedAvailable: false } });
    const execute = vi.fn<UpgradeWizardExecute>(async () => { throw new UpgradeWizardError('unavailable', 'upgrade_execution_conflict', 409); });
    await mount(resumeRead(() => current), undefined, undefined, undefined, execute);
    expect(allByTestId('upgrade-mode-managed')).toHaveLength(0);
    expect(allByTestId('upgrade-execute-import')).toHaveLength(0);
    await press('upgrade-execute-external');
    expect(execute.mock.calls[0][1]).toEqual({ action: 'external-connect' });
    expect(byTestId('upgrade-execution-error')).toBeTruthy();
    current = statusDocument({ nextAction: 'cpa_import', execution: { ...workerReady, available: false, code: 'upgrade_worker_unavailable' } });
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(byTestId('upgrade-execution-recovery')).toBeTruthy();
    expect(allByTestId('upgrade-execute-external')).toHaveLength(0);
    expect(view()).not.toContain('docker run');
    expect(view()).not.toContain('--cpa-version');
  });

  it('uses the one worker verify gate, keeps verified state while activating, and survives restart reads until actual routes are ready', async () => {
    vi.stubGlobal('window', { setTimeout, clearTimeout, location: { origin: 'https://manager.example' } });
    const routing = { configured: true, committed: true, existingPrefixesPreserved: true,
      controlBasePath: '/control', inferenceBasePath: '/infer', sharedEntry: false,
      externalBaseUrl: '', suggestedControlBasePath: '', suggestedInferenceBasePath: '' };
    const accessInfo = { mode: 'migrate_legacy' as const, managementBasePath: '/control', inferenceBasePath: '/infer',
      protocols: [], ready: false, code: 'upgrade_restart_required' };
    const manualItems = ['section:legacy.proxy:socks (not_in_import_scope)'];
    let current = statusDocument({ stage: 'cpa_import', nextAction: 'verify', targetMode: 'managed', routing, manualItems, execution: workerReady });
    let pollPhase: 'idle' | 'running-activate' | 'network' = 'idle';
    let unavailableReads = 0;
    const read = resumeRead(() => {
      if (pollPhase === 'running-activate') {
        pollPhase = 'network';
        return statusDocument({
          stage: 'complete',
          nextAction: 'complete',
          targetMode: 'managed',
          routing,
          manualItems,
          accessInfo,
          execution: { ...workerReady, running: true, action: 'activate' },
        });
      }
      if (unavailableReads-- > 0) throw new UpgradeWizardError('network');
      return current;
    });
    let resolveActivate!: (value: unknown) => void;
    const execute = vi.fn<UpgradeWizardExecute>(async (_signal, input) => {
      if (input.action === 'verify') {
        current = statusDocument({ stage: 'complete', nextAction: 'complete', targetMode: 'managed', routing, manualItems, accessInfo, execution: workerReady });
        return executionDocument({ action: input.action });
      }
      return new Promise<unknown>((resolve) => {
        resolveActivate = resolve;
      });
    });
    const verify = vi.fn<UpgradeWizardVerify>();
    const onActivation = vi.fn();
    await mount(read, verify, undefined, undefined, execute, onActivation);
    await press('upgrade-verify-action');
    expect(verify).not.toHaveBeenCalled();
    expect(execute.mock.calls[0][1]).toEqual({ action: 'verify' });
    expect(byTestId('upgrade-result-complete')).toBeTruthy();
    expect(byTestId('upgrade-manual-items').type).toBe('details');
    expect(allByTestId('upgrade-open-console')).toHaveLength(0);
    expect(execute).toHaveBeenCalledTimes(1);

    // In-flight activate POST must keep verified completion intact and manual details collapsed.
    await press('upgrade-execute-activate');
    expect(byTestId('upgrade-result-complete')).toBeTruthy();
    expect(allByTestId('upgrade-result-incomplete')).toHaveLength(0);
    expect(byTestId('upgrade-step-result').props['data-complete']).toBe('true');
    expect(byTestId('upgrade-manual-items').type).toBe('details');
    expect(byTestId('upgrade-access-pending-restart')).toBeTruthy();
    expect(allByTestId('upgrade-open-console')).toHaveLength(0);
    expect(byTestId('upgrade-execute-activate').props.disabled).toBe(true);

    // Resumed/polled worker running action: 'activate' also stays verified-complete (not incomplete).
    pollPhase = 'running-activate';
    unavailableReads = 3;
    current = statusDocument({
      stage: 'complete',
      nextAction: 'complete',
      targetMode: 'managed',
      routing,
      manualItems,
      accessInfo: { ...accessInfo, ready: true, code: '' },
    });
    await act(async () => {
      resolveActivate(executionDocument({ action: 'activate' }));
    });
    expect(onActivation).toHaveBeenCalledWith('/control');
    expect(byTestId('upgrade-result-complete')).toBeTruthy();
    expect(allByTestId('upgrade-result-incomplete')).toHaveLength(0);
    expect(byTestId('upgrade-manual-items').type).toBe('details');
    expect(byTestId('upgrade-activation-waiting')).toBeTruthy();
    expect(allByTestId('upgrade-open-console')).toHaveLength(0);

    await act(async () => { await vi.advanceTimersByTimeAsync(18_000); });
    expect(allByTestId('upgrade-progress-stalled')).toHaveLength(0);
    expect(byTestId('upgrade-open-console').props.href).toBe('https://manager.example/control');
    expect(allByTestId('upgrade-activation')).toHaveLength(0);
    expect(execute).toHaveBeenCalledTimes(2);
  });

  it('keeps polling through transient reads during a recorded root job without resubmitting it', async () => {
    let failures = 0;
    const read = resumeRead(() => {
      if (failures > 0) { failures -= 1; throw new UpgradeWizardError('network'); }
      return statusDocument({ nextAction: 'cpa_import', execution: { ...workerReady, running: true, action: 'cpa-import' } });
    });
    const execute = vi.fn<UpgradeWizardExecute>();
    await mount(read, undefined, undefined, undefined, execute);
    failures = 4;
    await act(async () => { await vi.advanceTimersByTimeAsync(18_000); });
    expect(allByTestId('upgrade-progress-stalled')).toHaveLength(0);
    expect(byTestId('upgrade-execution-running')).toBeTruthy();
    expect(execute).not.toHaveBeenCalled();
  });

  it('sends execution only to the authenticated same-origin POST route and follows committed continuation', async () => {
    vi.stubGlobal('window', { setTimeout, clearTimeout, location: { origin: 'https://manager.example' } });
    const transport = vi.fn<typeof fetch>(async () => Response.json(executionDocument(), { status: 202 }));
    let base = '';
    const execute = createUpgradeWizardExecute(() => 'private-admin-key', transport, () => base);
    await execute(new AbortController().signal, { action: 'cpa-import', pluginsEnabled: false });
    const [url, init] = transport.mock.calls[0];
    expect(String(url)).toBe('https://manager.example/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/execute');
    expect(init).toMatchObject({ method: 'POST', credentials: 'omit', mode: 'same-origin', redirect: 'error', cache: 'no-store' });
    expect(init?.headers).toMatchObject({ Authorization: 'Bearer private-admin-key' });
    expect(JSON.parse(String(init?.body))).toEqual({ action: 'cpa-import', pluginsEnabled: false });
    base = '/control/api/v2';
    const read = createUpgradeWizardRead(() => 'private-admin-key', transport, () => base);
    await read('installation/upgrade/status', new AbortController().signal);
    expect(String(transport.mock.calls[1][0])).toBe('https://manager.example/control/api/v2/installation/upgrade/status');
    base = 'https://other.example/api/v2';
    await expect(execute(new AbortController().signal, { action: 'verify' })).rejects.toMatchObject({ failure: 'unavailable' });
    expect(transport).toHaveBeenCalledTimes(2);
    expect(transport.mock.calls.every(([target]) => !String(target).includes('private-admin-key'))).toBe(true);
  });

  it('renders async status.execution.code separately from HTTP request errors including 409 busy and restart disabled', async () => {
    const digest = 'sha256:' + 'd'.repeat(64);
    const current = statusDocument({
      nextAction: 'cpa_import',
      execution: { ...workerReady, code: 'upgrade_cpa_destination_unready' },
    });
    const execute = vi
      .fn<UpgradeWizardExecute>()
      .mockRejectedValueOnce(new UpgradeWizardError('unavailable', 'upgrade_execution_busy', 409))
      .mockRejectedValueOnce(new UpgradeWizardError('unavailable', 'upgrade_restart_disabled', 409));
    const artifact: ArtifactTransport = {
      readStatus: vi.fn<ArtifactTransport['readStatus']>(async () => ({
        schemaVersion: 1,
        pinned: { version: '8.0.55', artifactId: digest },
        active: { artifactId: digest, origin: 'acquired' },
        required: false,
        operation: null,
        cdnPresets: [],
      })),
      probe: vi.fn(async () => ({ reachable: true, code: '' })),
      acquire: vi.fn<() => Promise<ArtifactOperation>>(async () => ({
        id: 'op-1',
        source: 'github',
        phase: 'succeeded',
        receivedBytes: 100,
        totalBytes: 100,
        code: '',
      })),
      upload: vi.fn(),
    };
    await mount(resumeRead(() => current), undefined, artifact, undefined, execute);

    const statusErr = byTestId('upgrade-execution-status-error');
    expect(statusErr.props.role).toBe('alert');
    expect(statusErr.findByType('code').children.join('')).toBe('upgrade_cpa_destination_unready');
    expect(statusErr.findAllByType('p').map((node) => node.children.join(''))).toContain(
      blockedCodeText('upgrade_cpa_destination_unready', 'en')
    );
    expect(allByTestId('upgrade-execution-error')).toHaveLength(0);

    await act(async () => byTestId('upgrade-plugins').props.onChange({ target: { value: 'disabled' } }));
    await press('upgrade-execute-import');
    expect(
      byTestId('upgrade-execution-error').findAllByType('p').map((node) => node.children.join(''))
    ).toContain(blockedCodeText('upgrade_execution_busy', 'en'));
    expect(byTestId('upgrade-execution-status-error')).toBeTruthy();

    await press('upgrade-execute-import');
    expect(
      byTestId('upgrade-execution-error').findAllByType('p').map((node) => node.children.join(''))
    ).toContain(blockedCodeText('upgrade_restart_disabled', 'en'));
  });

  it('never masquerades stage complete with execution failure or pending restart as ready console completion', async () => {
    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/control',
      inferenceBasePath: '/infer',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const accessInfo = {
      mode: 'migrate_legacy' as const,
      managementBasePath: '/control',
      inferenceBasePath: '/infer',
      protocols: [],
      ready: false,
      code: 'upgrade_restart_required',
    };
    let current = statusDocument({
      stage: 'complete',
      nextAction: 'complete',
      targetMode: 'managed',
      routing,
      accessInfo,
      execution: { ...workerReady, action: 'activate', code: 'upgrade_restart_failed' },
    });
    const execute = vi.fn<UpgradeWizardExecute>();
    await mount(resumeRead(() => current), undefined, undefined, undefined, execute);

    expect(allByTestId('upgrade-result-complete')).toHaveLength(0);
    expect(byTestId('upgrade-result-incomplete')).toBeTruthy();
    expect(byTestId('upgrade-step-result').props['data-complete']).toBeUndefined();
    expect(allByTestId('upgrade-open-console')).toHaveLength(0);
    expect(byTestId('upgrade-execution-status-error').findByType('code').children.join('')).toBe(
      'upgrade_restart_failed'
    );
    expect(byTestId('upgrade-execute-activate').props.disabled).toBe(false);

    current = statusDocument({
      stage: 'complete',
      nextAction: 'complete',
      targetMode: 'managed',
      routing,
      accessInfo,
      execution: { ...workerReady, action: 'verify', code: '' },
    });
    await press('upgrade-result-reread');
    expect(byTestId('upgrade-result-complete')).toBeTruthy();
    expect(byTestId('upgrade-access-pending-restart')).toBeTruthy();
    expect(allByTestId('upgrade-access-active')).toHaveLength(0);
    expect(allByTestId('upgrade-open-console')).toHaveLength(0);
    expect(view()).toContain(e('resultPendingRestartHelp', 'en'));
  });

  it('resets dirty/saved routing draft when targetMode switches between External and Managed', async () => {
    const unconfigured = {
      configured: false,
      committed: false,
      existingPrefixesPreserved: false,
      controlBasePath: '',
      inferenceBasePath: '',
      sharedEntry: false,
      externalBaseUrl: 'https://cpa.example:8317',
      suggestedControlBasePath: '/ext-ctl',
      suggestedInferenceBasePath: '',
    };
    let current = statusDocument({
      stage: 'external_connect',
      nextAction: 'verify',
      targetMode: 'external',
      routing: unconfigured,
      execution: workerReady,
    });
    const saveRouting = vi.fn<UpgradeWizardSaveRouting>(async (_signal, input) => ({
      contractVersion: CONTRACT,
      targetMode: current.targetMode,
      routing: {
        ...unconfigured,
        configured: true,
        controlBasePath: input.controlBasePath,
        inferenceBasePath: input.inferenceBasePath,
        sharedEntry: input.sharedEntry,
      },
    }));
    const execute = vi.fn<UpgradeWizardExecute>(async () => executionDocument({ action: 'verify' }));
    await mount(resumeRead(() => current), undefined, undefined, saveRouting, execute);

    await act(async () => {
      byTestId('upgrade-control-prefix-input').props.onChange({ target: { value: '/ext-dirty' } });
    });
    await press('upgrade-save-routing');
    expect(saveRouting).toHaveBeenCalledTimes(1);
    expect(byTestId('upgrade-routing-saved')).toBeTruthy();

    current = statusDocument({
      stage: 'cpa_import',
      nextAction: 'verify',
      targetMode: 'managed',
      routing: {
        ...unconfigured,
        externalBaseUrl: '',
        suggestedControlBasePath: '/man-ctl',
        suggestedInferenceBasePath: '/man-inf',
      },
      execution: workerReady,
    });
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });

    expect(allByTestId('upgrade-routing-saved')).toHaveLength(0);
    expect(byTestId('upgrade-control-prefix-input').props.value).toBe('/man-ctl');
    expect(byTestId('upgrade-inference-prefix-input').props.value).toBe('/man-inf');

    await press('upgrade-verify-action');
    expect(saveRouting).toHaveBeenCalledTimes(2);
    expect(saveRouting.mock.calls[1][1]).toEqual({
      controlBasePath: '/man-ctl',
      inferenceBasePath: '/man-inf',
      sharedEntry: false,
    });
    expect(execute.mock.calls[0][1]).toEqual({ action: 'verify' });
  });

  it('shows Managed engine prep at nextAction external_connect only when managedAvailable is true', async () => {
    const digest = 'sha256:' + 'a'.repeat(64);
    const artifact: ArtifactTransport = {
      readStatus: vi.fn<ArtifactTransport['readStatus']>(async () => ({
        schemaVersion: 1,
        pinned: { version: '8.0.55', artifactId: digest },
        active: null,
        required: true,
        operation: null,
        cdnPresets: [],
      })),
      probe: vi.fn(async () => ({ reachable: true, code: '' })),
      acquire: vi.fn<() => Promise<ArtifactOperation>>(async () => ({
        id: 'op-2',
        source: 'github',
        phase: 'succeeded',
        receivedBytes: 100,
        totalBytes: 100,
        code: '',
      })),
      upload: vi.fn(),
    };
    const current = statusDocument({
      nextAction: 'external_connect',
      errorCode: 'external_cpa_unreachable',
      execution: workerReady,
    });
    await mount(resumeRead(() => current), undefined, artifact, undefined, vi.fn<UpgradeWizardExecute>());

    expect(byTestId('upgrade-mode-external').props['aria-pressed']).toBe(true);
    expect(allByTestId('upgrade-cpa-import-artifact')).toHaveLength(0);

    await press('upgrade-mode-managed');
    expect(byTestId('upgrade-cpa-import-artifact')).toBeTruthy();
    expect(byTestId('upgrade-execute-import').props.disabled).toBe(true);

    await act(async () => renderer!.unmount());
    renderer = undefined;
    await mount(
      resumeRead(() =>
        statusDocument({
          nextAction: 'external_connect',
          execution: { ...workerReady, managedAvailable: false },
        })
      ),
      undefined,
      artifact,
      undefined,
      vi.fn<UpgradeWizardExecute>()
    );
    expect(allByTestId('upgrade-mode-managed')).toHaveLength(0);
    expect(allByTestId('upgrade-cpa-import-artifact')).toHaveLength(0);
  });

  it('requires paired External override URL and key without persisting the secret in state or UI', async () => {
    const keyMockNode = { value: '' };
    let current = statusDocument({
      stage: 'reconcile',
      nextAction: 'external_connect',
      execution: { ...workerReady, managedAvailable: false },
    });
    const execute = vi.fn<UpgradeWizardExecute>(async () =>
      executionDocument({ action: 'external-connect' })
    );
    await act(async () => {
      renderer = create(
        <FreshUpgradeFlow
          read={resumeRead(() => current)}
          execute={execute}
          language="en"
          pollDelays={[10_000]}
        />,
        {
          createNodeMock: (element) =>
            Object.getOwnPropertyDescriptor(element.props, 'data-testid')?.value === 'upgrade-external-key'
              ? keyMockNode
              : null,
        }
      );
    });

    expect(byTestId('upgrade-execute-external').props.disabled).toBe(false);
    await act(async () => {
      byTestId('upgrade-external-override').props.onChange({ target: { checked: true } });
    });
    expect(byTestId('upgrade-execute-external').props.disabled).toBe(true);
    expect(byTestId('upgrade-external-pair-hint')).toBeTruthy();

    await act(async () => {
      byTestId('upgrade-external-url').props.onChange({
        target: { value: 'https://cpa-new.example:8317' },
      });
    });
    expect(byTestId('upgrade-execute-external').props.disabled).toBe(true);

    const uniqueKey = 'ext-mgmt-unique-fixture-value';
    keyMockNode.value = uniqueKey;
    await act(async () => {
      byTestId('upgrade-external-key').props.onChange({ target: keyMockNode });
    });
    expect(byTestId('upgrade-execute-external').props.disabled).toBe(false);
    expect(byTestId('upgrade-external-key').props.type).toBe('password');
    expect(byTestId('upgrade-external-key').props.value).toBeUndefined();

    await press('upgrade-execute-external');
    expect(execute).toHaveBeenCalledTimes(1);
    expect(execute.mock.calls[0][1]).toEqual({
      action: 'external-connect',
      baseUrl: 'https://cpa-new.example:8317',
      managementKey: uniqueKey,
    });
    // 202 Accepted keeps the key input intact until the connection actually succeeds past reconcile.
    expect(keyMockNode.value).toBe(uniqueKey);
    expect(view()).not.toContain(uniqueKey);

    current = statusDocument({
      stage: 'reconcile',
      nextAction: 'external_connect',
      execution: { ...workerReady, managedAvailable: false, code: 'external_management_key_rejected' },
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_500);
    });
    expect(keyMockNode.value).toBe(uniqueKey);

    await act(async () => {
      byTestId('upgrade-external-override').props.onChange({ target: { checked: false } });
    });
    expect(keyMockNode.value).toBe('');
    await act(async () => {
      byTestId('upgrade-external-override').props.onChange({ target: { checked: true } });
    });
    keyMockNode.value = uniqueKey;
    await act(async () => {
      byTestId('upgrade-external-key').props.onChange({ target: keyMockNode });
    });
    expect(keyMockNode.value).toBe(uniqueKey);

    current = statusDocument({
      stage: 'external_connect',
      nextAction: 'verify',
      execution: workerReady,
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_500);
    });
    expect(keyMockNode.value).toBe('');
    expect(view()).not.toContain(uniqueKey);
  });

  it('surfaces reconcile panel from copy_in source_copy when mismatch requires parity ack', async () => {
    const current = statusDocument({
      stage: 'copy_in',
      nextAction: 'copy_in',
      domainsDone: ['source_copy'],
      mismatchDays: [],
      execution: { ...workerReady, action: 'reconcile', code: 'upgrade_reconcile_mismatch' },
    });
    const execute = vi.fn<UpgradeWizardExecute>(async () =>
      executionDocument({ action: 'reconcile' })
    );
    await mount(resumeRead(() => current), undefined, undefined, undefined, execute);

    expect(byTestId('upgrade-execute-reconcile').props.disabled).toBe(true);
    await act(async () => {
      byTestId('upgrade-ack-parity').props.onChange({ target: { checked: true } });
    });
    expect(byTestId('upgrade-execute-reconcile').props.disabled).toBe(false);
    await press('upgrade-execute-reconcile');
    expect(execute.mock.calls[0][1]).toEqual({
      action: 'reconcile',
      acknowledgeParityGaps: true,
    });
  });

  it('keeps 503 worker unavailable typed as unavailable and surfaces 120s activation timeout cleanly', async () => {
    vi.stubGlobal('window', { setTimeout, clearTimeout, location: { origin: 'https://manager.example' } });
    const transport = vi.fn<typeof fetch>(async () =>
      Response.json({ error: { code: 'upgrade_worker_unavailable' } }, { status: 503 })
    );
    const executeTransport = createUpgradeWizardExecute(() => 'admin-secret', transport);
    await expect(
      executeTransport(new AbortController().signal, { action: 'verify' })
    ).rejects.toMatchObject({
      failure: 'unavailable',
      code: 'upgrade_worker_unavailable',
      status: 503,
    });

    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/control',
      inferenceBasePath: '/infer',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const pendingStatus = statusDocument({
      stage: 'complete',
      nextAction: 'complete',
      targetMode: 'managed',
      routing,
      accessInfo: {
        mode: 'migrate_legacy' as const,
        managementBasePath: '/control',
        inferenceBasePath: '/infer',
        protocols: [],
        ready: false,
        code: 'upgrade_restart_required',
      },
      execution: workerReady,
    });
    let activateCalled = false;
    const read = resumeRead(() => {
      if (activateCalled) throw new UpgradeWizardError('network');
      return pendingStatus;
    });
    const execute = vi.fn<UpgradeWizardExecute>(async () => {
      activateCalled = true;
      return executionDocument({ action: 'activate' });
    });
    await mount(read, undefined, undefined, undefined, execute);
    await press('upgrade-execute-activate');
    expect(byTestId('upgrade-activation-waiting')).toBeTruthy();

    await act(async () => { await vi.advanceTimersByTimeAsync(122_000); });
    expect(
      byTestId('upgrade-execution-error').findAllByType('p').map((node) => node.children.join(''))
    ).toContain(e('activationTimeout', 'en'));
    expect(byTestId('upgrade-result-stalled')).toBeTruthy();
    expect(byTestId('upgrade-execute-activate').props.disabled).toBe(false);
  });

  it('removes artifact preparation controls from DOM/AX subtree once ready while preserving plugin gate', async () => {
    const digest = 'sha256:' + 'c'.repeat(64);
    const unreadyArtifact: ArtifactStatus = {
      schemaVersion: 1,
      pinned: { version: '8.0.55', artifactId: digest },
      active: null,
      required: true,
      operation: null,
      cdnPresets: [],
    };
    const readyArtifact: ArtifactStatus = {
      schemaVersion: 1,
      pinned: { version: '8.0.55', artifactId: digest },
      active: { artifactId: digest, origin: 'acquired' },
      required: false,
      operation: null,
      cdnPresets: [],
    };
    let resolvePendingRead!: (value: ArtifactStatus) => void;
    let mode: 'pending' | 'ready' = 'pending';
    const artifact: ArtifactTransport = {
      readStatus: vi.fn<ArtifactTransport['readStatus']>(() => {
        if (mode === 'pending') {
          return new Promise<ArtifactStatus>((resolve) => {
            resolvePendingRead = resolve;
          });
        }
        return Promise.resolve(readyArtifact);
      }),
      probe: vi.fn(async () => ({ reachable: true, code: '' })),
      acquire: vi.fn<ArtifactTransport['acquire']>(async () => {
        mode = 'ready';
        return {
          id: 'op-ready-1',
          source: 'github',
          phase: 'succeeded',
          receivedBytes: 100,
          totalBytes: 100,
          code: '',
        };
      }),
      upload: vi.fn(),
    };
    const execute = vi.fn<UpgradeWizardExecute>(async () =>
      executionDocument({ action: 'cpa-import' })
    );
    const current = statusDocument({
      stage: 'reconcile',
      nextAction: 'cpa_import',
      domainsDone: ['source_copy', 'usage_events'],
      execution: workerReady,
    });

    await mount(resumeRead(() => current), undefined, artifact, undefined, execute);

    // Initial async undefined/loading state never falsely reports ready.
    expect(allByTestId('upgrade-cpa-import-ready')).toHaveLength(0);
    expect(byTestId('upgrade-execute-import').props.disabled).toBe(true);

    await act(async () => {
      resolvePendingRead(unreadyArtifact);
    });
    expect(byTestId('upgrade-artifact-prep')).toBeTruthy();
    expect(byTestId('fresh-artifact-step')).toBeTruthy();
    expect(byTestId('artifact-acquire-btn')).toBeTruthy();
    expect(byTestId('upgrade-cpa-import-artifact').findAllByProps({ role: 'button' }).length).toBeGreaterThan(0);

    await press('artifact-acquire-btn');

    // Once ready, preparation subtree and all interactive controls leave the DOM/AX/Tab tree.
    expect(allByTestId('upgrade-artifact-prep')).toHaveLength(0);
    expect(allByTestId('fresh-artifact-step')).toHaveLength(0);
    expect(allByTestId('artifact-acquire-btn')).toHaveLength(0);
    expect(allByTestId('artifact-source-github')).toHaveLength(0);
    expect(allByTestId('artifact-source-upload')).toHaveLength(0);
    const artifactRegion = byTestId('upgrade-cpa-import-artifact');
    expect(artifactRegion.findAllByProps({ role: 'button' })).toHaveLength(0);
    expect(artifactRegion.findAllByType('button')).toHaveLength(0);
    expect(artifactRegion.findAllByType('input')).toHaveLength(0);
    expect(byTestId('upgrade-cpa-import-ready')).toBeTruthy();

    // Managed import stays gated until explicit plugin choice is made.
    expect(byTestId('upgrade-execute-import').props.disabled).toBe(true);
    await act(async () => {
      byTestId('upgrade-plugins').props.onChange({ target: { value: 'enabled' } });
    });
    expect(byTestId('upgrade-execute-import').props.disabled).toBe(false);
    await press('upgrade-execute-import');
    expect(execute.mock.calls[0][1]).toEqual({ action: 'cpa-import', pluginsEnabled: true });

    // Pre-ready artifact on initial mount also unmounts prep immediately after async check resolves.
    await act(async () => renderer!.unmount());
    renderer = undefined;
    await mount(resumeRead(() => current), undefined, artifact, undefined, execute);
    expect(allByTestId('upgrade-artifact-prep')).toHaveLength(0);
    expect(allByTestId('fresh-artifact-step')).toHaveLength(0);
    expect(byTestId('upgrade-cpa-import-ready')).toBeTruthy();
    expect(byTestId('upgrade-execute-import').props.disabled).toBe(true);
  });

  it('keeps console access gated until routes are ready with no execution worker', async () => {
    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/control',
      inferenceBasePath: '/infer',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const manualItems = ['section:legacy.proxy:socks (not_in_import_scope)'];
    const nativePending = statusDocument({
      stage: 'complete',
      nextAction: 'complete',
      targetMode: 'managed',
      routing,
      manualItems,
      accessInfo: {
        mode: 'migrate_legacy' as const,
        managementBasePath: '/control',
        inferenceBasePath: '/infer',
        protocols: [],
        ready: false,
        code: 'upgrade_restart_required',
      },
    });
    await mount(resumeRead(() => nativePending));
    expect(byTestId('upgrade-result-complete')).toBeTruthy();
    expect(byTestId('upgrade-execution-recovery')).toBeTruthy();
    expect(allByTestId('upgrade-open-console')).toHaveLength(0);

    await act(async () => renderer!.unmount());
    renderer = undefined;
    const zhActive = statusDocument({
      stage: 'complete',
      nextAction: 'complete',
      targetMode: 'managed',
      routing,
      manualItems,
      accessInfo: {
        mode: 'migrate_legacy' as const,
        managementBasePath: '/control',
        inferenceBasePath: '/infer',
        protocols: [],
        ready: true,
        code: '',
      },
    });
    vi.stubGlobal('window', { setTimeout, clearTimeout, location: { origin: 'https://manager.example' } });
    await act(async () => {
      renderer = create(
        <FreshUpgradeFlow read={resumeRead(() => zhActive)} language="zh-CN" pollDelays={[10_000]} />
      );
    });
    expect(byTestId('upgrade-access-active')).toBeTruthy();
    expect(byTestId('upgrade-open-console').props.href).toBe('https://manager.example/control');
  });

  it('resumes in-flight activate without running flag, handles omitted-action restart failure, and covers gateway waiting and completion copy without keys', async () => {
    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/control',
      inferenceBasePath: '/infer',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const inFlightResume = statusDocument({
      stage: 'complete',
      nextAction: 'complete',
      targetMode: 'managed',
      routing,
      accessInfo: {
        mode: 'migrate_legacy' as const,
        managementBasePath: '/control',
        inferenceBasePath: '/infer',
        protocols: [],
        ready: false,
        code: 'upgrade_restart_required',
      },
      execution: { ...workerReady, action: 'activate', code: '' },
    });
    const delayedFailure = statusDocument({
      stage: 'complete',
      nextAction: 'complete',
      targetMode: 'managed',
      routing,
      accessInfo: {
        mode: 'migrate_legacy' as const,
        managementBasePath: '/control',
        inferenceBasePath: '/infer',
        protocols: [],
        ready: false,
        code: 'upgrade_restart_required',
      },
      execution: { ...workerReady, action: undefined, code: 'upgrade_restart_failed' },
    });

    let phase: 'in-flight' | 'failed' = 'in-flight';
    const onActivation = vi.fn();
    const execute = vi.fn<UpgradeWizardExecute>(async () =>
      executionDocument({ action: 'activate', running: false, code: '' })
    );
    const read: UpgradeWizardRead = vi.fn(async () =>
      phase === 'in-flight' ? inFlightResume : delayedFailure
    );

    await mount(read, undefined, undefined, undefined, execute, onActivation);
    expect(onActivation).toHaveBeenCalledWith('/control');
    expect(byTestId('upgrade-activation-waiting')).toBeTruthy();
    expect(byTestId('upgrade-execute-activate').props.disabled).toBe(true);
    expect(execute).not.toHaveBeenCalled();

    phase = 'failed';
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_500);
    });
    expect(onActivation).toHaveBeenCalledWith('');
    expect(allByTestId('upgrade-activation-waiting')).toHaveLength(0);
    expect(byTestId('upgrade-execution-status-error').findByType('code').children.join('')).toBe(
      'upgrade_restart_failed'
    );
    expect(byTestId('upgrade-result-incomplete')).toBeTruthy();
    expect(byTestId('upgrade-execute-activate').props.disabled).toBe(false);

    // Canonical proof with gateway pending clears activation card, opens console, and never times out.
    await act(async () => renderer!.unmount());
    renderer = undefined;
    let gatewayReady = false;
    let rejectClipboard = true;
    const writeText = vi.fn(async () => {
      if (rejectClipboard) throw new Error('clipboard unavailable');
    });
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    vi.stubGlobal('window', {
      setTimeout,
      clearTimeout,
      location: { origin: 'https://manager.example' },
    });

    const canonicalRead: UpgradeWizardRead = vi.fn(async () =>
      markCanonicalUpgradeStatus(
        statusDocument({
          stage: 'complete',
          nextAction: 'complete',
          targetMode: 'managed',
          routing,
          accessInfo: gatewayReady
            ? {
                mode: 'migrate_legacy' as const,
                managementBasePath: '/control',
                inferenceBasePath: '/infer',
                protocols: [
                  {
                    id: 'openai',
                    label: 'OpenAI',
                    basePath: '/infer/v1',
                    examplePath: '/infer/v1/chat/completions',
                  },
                ],
                ready: true,
                code: '',
              }
            : {
                mode: 'migrate_legacy' as const,
                managementBasePath: '/control',
                inferenceBasePath: '/infer',
                protocols: [],
                ready: false,
                code: 'gateway_pending',
              },
        })
      )
    );

    await mount(canonicalRead, undefined, undefined, undefined, execute);
    expect(byTestId('upgrade-gateway-waiting')).toBeTruthy();
    expect(allByTestId('upgrade-activation')).toHaveLength(0);
    expect(byTestId('upgrade-open-console').props.href).toBe('https://manager.example/control');

    await act(async () => {
      await vi.advanceTimersByTimeAsync(125_000);
    });
    expect(allByTestId('upgrade-execution-error')).toHaveLength(0);
    expect(byTestId('upgrade-gateway-waiting')).toBeTruthy();

    gatewayReady = true;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(15_500);
    });
    expect(allByTestId('upgrade-gateway-waiting')).toHaveLength(0);
    expect(byTestId('upgrade-access-active')).toBeTruthy();

    await press('access-info-copy-access');
    const fallback = byTestId('access-info-copy-access-fallback');
    const expectedCopyText = String(fallback.props.value);
    expect(fallback.type).toBe('textarea');
    expect(expectedCopyText).toContain('https://manager.example/control');
    expect(expectedCopyText).toContain('https://manager.example/infer');
    expect(expectedCopyText).not.toContain('key');
    rejectClipboard = false;
    await press('access-info-copy-access');
    expect(allByTestId('access-info-copy-access-fallback')).toHaveLength(0);
    expect(writeText).toHaveBeenLastCalledWith(expectedCopyText);

    // Retired bootstrap view never offers the bootstrap activation button.
    await act(async () => renderer!.unmount());
    renderer = undefined;
    await act(async () => {
      renderer = create(
        <FreshUpgradeFlow
          read={resumeRead(() =>
            statusDocument({
              stage: 'complete',
              nextAction: 'complete',
              targetMode: 'managed',
              routing,
              accessInfo: {
                mode: 'migrate_legacy' as const,
                managementBasePath: '/control',
                inferenceBasePath: '/infer',
                protocols: [],
                ready: false,
                code: 'upgrade_restart_required',
              },
              execution: workerReady,
            })
          )}
          execute={execute}
          bootstrapRetired
          language="en"
          pollDelays={[10_000]}
        />
      );
    });
    expect(allByTestId('upgrade-execute-activate')).toHaveLength(0);
    expect(byTestId('upgrade-bootstrap-retired-notice')).toBeTruthy();
  });

  it('does not abort activation polling on upgrade_worker_unavailable during shutdown and resumes omitted-action unavailable worker state', async () => {
    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/control',
      inferenceBasePath: '/infer',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    let current = statusDocument({
      stage: 'complete',
      nextAction: 'complete',
      targetMode: 'managed',
      routing,
      accessInfo: {
        mode: 'migrate_legacy' as const,
        managementBasePath: '/control',
        inferenceBasePath: '/infer',
        protocols: [],
        ready: false,
        code: 'upgrade_restart_required',
      },
      execution: { ...workerReady, action: 'verify', code: '' },
    });
    const onActivation = vi.fn();
    const execute = vi.fn<UpgradeWizardExecute>(async () => {
      current = {
        ...current,
        execution: { ...workerReady, running: true, action: 'activate', code: '' },
      };
      return executionDocument({ action: 'activate', running: true, code: '' });
    });
    await mount(resumeRead(() => current), undefined, undefined, undefined, execute, onActivation);

    // After verify without clicking Activate yet, Activate button must remain actionable.
    expect(byTestId('upgrade-execute-activate').props.disabled).toBe(false);
    expect(onActivation).not.toHaveBeenCalled();

    await press('upgrade-execute-activate');
    expect(onActivation).toHaveBeenCalledWith('/control');
    onActivation.mockClear();

    // Worker exits before mgrSvc during shutdown -> serveWorkerUnavailable returns upgrade_worker_unavailable.
    current = statusDocument({
      stage: 'complete',
      nextAction: 'complete',
      targetMode: 'managed',
      routing,
      accessInfo: {
        mode: 'migrate_legacy' as const,
        managementBasePath: '/control',
        inferenceBasePath: '/infer',
        protocols: [],
        ready: false,
        code: 'upgrade_restart_required',
      },
      execution: { available: false, running: false, managedAvailable: false, code: 'upgrade_worker_unavailable' },
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_500);
    });
    expect(onActivation).not.toHaveBeenCalled();
    expect(byTestId('upgrade-result-complete')).toBeTruthy();
    expect(allByTestId('upgrade-result-incomplete')).toHaveLength(0);
    expect(byTestId('upgrade-activation-waiting')).toBeTruthy();

    // Reloading in this shutdown transition resumes activation polling instead of flashing incomplete.
    await act(async () => renderer!.unmount());
    renderer = undefined;
    onActivation.mockClear();
    await mount(resumeRead(() => current), undefined, undefined, undefined, execute, onActivation, true);
    expect(onActivation).toHaveBeenCalledWith('/control');
    expect(byTestId('upgrade-result-complete')).toBeTruthy();
    expect(byTestId('upgrade-activation-waiting')).toBeTruthy();

    // Canonical confirmation clears activation waiting cleanly.
    current = markCanonicalUpgradeStatus(
      statusDocument({
        stage: 'complete',
        nextAction: 'complete',
        targetMode: 'managed',
        routing,
        accessInfo: {
          mode: 'migrate_legacy' as const,
          managementBasePath: '/control',
          inferenceBasePath: '/infer',
          protocols: [],
          ready: true,
        },
      })
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_500);
    });
    expect(allByTestId('upgrade-activation-waiting')).toHaveLength(0);
    expect(byTestId('upgrade-access-active')).toBeTruthy();
  });

  it('bounds post-cutover polling for external outages and preserves 401/response errors during restart', async () => {
    const routing = {
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
    let extReady = false;
    let reads = 0;
    const canonicalExternalRead: UpgradeWizardRead = vi.fn(async () => {
      reads += 1;
      return markCanonicalUpgradeStatus(
        statusDocument({
          stage: 'complete',
          nextAction: 'complete',
          targetMode: 'external',
          routing,
          accessInfo: {
            mode: 'connect_external' as const,
            managementBasePath: '/ext-ctl',
            inferenceBasePath: '',
            externalBaseUrl: 'https://cpa.remote.example',
            protocols: [],
            ready: extReady,
            ...(extReady ? {} : { code: 'external_target_unavailable' }),
          },
        })
      );
    });
    await mount(canonicalExternalRead);
    expect(byTestId('access-info-not-ready')).toBeTruthy();
    extReady = true;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_500);
    });
    expect(byTestId('upgrade-access-active')).toBeTruthy();

    // Unrecoverable External outage settles after the 120s post-cutover deadline.
    await act(async () => renderer!.unmount());
    renderer = undefined;
    extReady = false;
    reads = 0;
    await mount(canonicalExternalRead);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(125_000);
    });
    const settledReads = reads;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(40_000);
    });
    expect(reads).toBe(settledReads);
    expect(allByTestId('upgrade-execution-error')).toHaveLength(0);

    // 401 authentication failure during activation restart must not be overwritten by activation timeout.
    await act(async () => renderer!.unmount());
    renderer = undefined;
    let failAuth = false;
    const authRead: UpgradeWizardRead = vi.fn(async () => {
      if (failAuth) throw new UpgradeWizardError('authentication', '', 401);
      return statusDocument({
        stage: 'complete',
        nextAction: 'complete',
        targetMode: 'managed',
        routing: { ...routing, controlBasePath: '/control', inferenceBasePath: '/infer', externalBaseUrl: '' },
        accessInfo: {
          mode: 'migrate_legacy' as const,
          managementBasePath: '/control',
          inferenceBasePath: '/infer',
          protocols: [],
          ready: false,
          code: 'upgrade_restart_required',
        },
        execution: { ...workerReady, action: 'activate', code: '' },
      });
    });
    await mount(authRead);
    failAuth = true;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(35_000);
    });
    expect(byTestId('upgrade-execution-error').findByType('p').children.join('')).toBe(
      t('errorAuthentication', 'en')
    );
  });

  it('decouples parity ack when hasParityGaps is absent, supports >366 truncated day ack, and uses journey checkbox styling', async () => {
    let current = statusDocument({
      nextAction: 'reconcile',
      mismatchDays: ['2026-10-01', '2026-10-02'],
      hasParityGaps: false,
      execution: workerReady,
    });
    const execute = vi.fn<UpgradeWizardExecute>(async () => executionDocument({ action: 'reconcile' }));
    await mount(resumeRead(() => current), undefined, undefined, undefined, execute);

    expect(renderer!.root.findAllByProps({ className: 'journey-checkbox-control' }).length).toBeGreaterThanOrEqual(3);
    expect(renderer!.root.findAllByProps({ className: 'journey-checkbox-box' }).length).toBeGreaterThanOrEqual(3);
    expect(allByTestId('upgrade-ack-all-truncated')).toHaveLength(0);

    // Only individual days are required when hasParityGaps is not true.
    await act(async () => {
      byTestId('upgrade-ack-day-2026-10-01').props.onChange({ target: { checked: true } });
      byTestId('upgrade-ack-day-2026-10-02').props.onChange({ target: { checked: true } });
    });
    expect(byTestId('upgrade-execute-reconcile').props.disabled).toBe(false);
    await press('upgrade-execute-reconcile');
    expect(execute.mock.calls[0][1]).toEqual({
      action: 'reconcile',
      acknowledgeDays: ['2026-10-01', '2026-10-02'],
    });

    // Truncated (>366) mismatch days + hasParityGaps requires explicit truncated confirmation + parity confirmation.
    current = statusDocument({
      nextAction: 'reconcile',
      mismatchDays: ['2026-10-01', '2026-10-02'],
      mismatchDaysTotal: 400,
      mismatchDaysTruncated: true,
      hasParityGaps: true,
      execution: workerReady,
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_500);
    });
    expect(byTestId('upgrade-ack-all-truncated')).toBeTruthy();
    expect(byTestId('upgrade-execute-reconcile').props.disabled).toBe(true);
    await act(async () => {
      byTestId('upgrade-ack-all-truncated').props.onChange({ target: { checked: true } });
    });
    // Still blocked until parity is confirmed when hasParityGaps is true.
    expect(byTestId('upgrade-execute-reconcile').props.disabled).toBe(true);
    await act(async () => {
      byTestId('upgrade-ack-parity').props.onChange({ target: { checked: true } });
    });
    expect(byTestId('upgrade-execute-reconcile').props.disabled).toBe(false);
    await press('upgrade-execute-reconcile');
    expect(execute.mock.calls[1][1]).toEqual({
      action: 'reconcile',
      acknowledgeDays: ['2026-10-01', '2026-10-02'],
      acknowledgeAllMismatches: true,
      acknowledgeParityGaps: true,
    });
  });

  it('mounts FreshArtifactStep and gates Managed import on Step 4 failure recovery', async () => {
    const digest = 'sha256:' + 'e'.repeat(64);
    const artifactTransport: ArtifactTransport = {
      readStatus: vi.fn<ArtifactTransport['readStatus']>(async () => ({
        schemaVersion: 1,
        pinned: { version: '8.0.55', artifactId: digest },
        active: null,
        required: true,
        operation: null,
        cdnPresets: [],
      })),
      probe: vi.fn(async () => ({ reachable: true, code: '' })),
      acquire: vi.fn<() => Promise<ArtifactOperation>>(async () => ({
        id: 'op-slim-step4',
        source: 'github',
        phase: 'succeeded',
        receivedBytes: 100,
        totalBytes: 100,
        code: '',
      })),
      upload: vi.fn(),
    };

    await mount(
      resumeRead(() =>
        statusDocument({
          stage: 'failed',
          nextAction: 'cpa_import',
          errorCode: 'upgrade_cpa_destination_unready',
          errorDetail: 'engine not ready',
          execution: workerReady,
        })
      ),
      vi.fn(),
      artifactTransport,
      undefined,
      vi.fn(async () => executionDocument())
    );

    expect(byTestId('upgrade-cpa-import-artifact')).toBeTruthy();
    expect(byTestId('upgrade-artifact-prep')).toBeTruthy();
    await act(async () => byTestId('upgrade-plugins').props.onChange({ target: { value: 'disabled' } }));
    expect(byTestId('upgrade-execute-import').props.disabled).toBe(true);

    await press('artifact-acquire-btn');
    expect(byTestId('upgrade-cpa-import-ready')).toBeTruthy();
    expect(byTestId('upgrade-execute-import').props.disabled).toBe(false);
  });

  it('treats pre-activation upgrade_worker_unavailable on boot and live poll as execution recovery, and clears restarting when healthy idle verify status returns', async () => {
    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/control',
      inferenceBasePath: '/infer',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const pendingAccess = {
      mode: 'migrate_legacy' as const,
      managementBasePath: '/control',
      inferenceBasePath: '/infer',
      protocols: [],
      ready: false,
      code: 'upgrade_restart_required',
    };
    let statusMode: 'unavailable' | 'verify' | 'activating' = 'unavailable';
    const readCurrent = () =>
      statusDocument({
        stage: 'complete',
        nextAction: 'complete',
        targetMode: 'managed',
        routing,
        accessInfo: pendingAccess,
        execution:
          statusMode === 'unavailable'
            ? {
                available: false,
                running: false,
                managedAvailable: false,
                code: 'upgrade_worker_unavailable',
              }
            : statusMode === 'activating'
              ? { ...workerReady, action: 'activate', running: true, code: '' }
              : { ...workerReady, action: 'verify', code: '' },
      });
    const onActivation = vi.fn();
    const execute = vi.fn<UpgradeWizardExecute>(async () => {
      statusMode = 'activating';
      return executionDocument({ action: 'activate', running: true, code: '' });
    });

    // 1. Pre-activation boot with upgrade_worker_unavailable must render recovery instead of activation waiting.
    await mount(resumeRead(readCurrent), undefined, undefined, undefined, execute, onActivation);
    expect(onActivation).not.toHaveBeenCalled();
    expect(byTestId('upgrade-execution-recovery')).toBeTruthy();
    expect(allByTestId('upgrade-activation-waiting')).toHaveLength(0);
    expect(byTestId('upgrade-result-incomplete')).toBeTruthy();

    // Worker becomes healthy idle -> Activate becomes enabled.
    statusMode = 'verify';
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_500);
    });
    expect(allByTestId('upgrade-execution-recovery')).toHaveLength(0);
    expect(byTestId('upgrade-execute-activate').props.disabled).toBe(false);

    // 2. Live poll dropping to upgrade_worker_unavailable BEFORE Activate also shows recovery.
    statusMode = 'unavailable';
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_500);
    });
    expect(onActivation).not.toHaveBeenCalled();
    expect(byTestId('upgrade-execution-recovery')).toBeTruthy();
    expect(allByTestId('upgrade-activation-waiting')).toHaveLength(0);

    // Recover worker, then click Activate.
    statusMode = 'verify';
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_500);
    });
    expect(byTestId('upgrade-execute-activate').props.disabled).toBe(false);

    await press('upgrade-execute-activate');
    expect(onActivation).toHaveBeenCalledWith('/control');
    expect(byTestId('upgrade-activation-waiting')).toBeTruthy();

    // 3. While restarting is active, if the next poll reports healthy idle verify (!accessInfo.ready),
    // restarting clears immediately and Activate unlocks without locking for 120s.
    statusMode = 'verify';
    onActivation.mockClear();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_500);
    });
    expect(onActivation).toHaveBeenCalledWith('');
    expect(allByTestId('upgrade-activation-waiting')).toHaveLength(0);
    expect(allByTestId('upgrade-execution-error')).toHaveLength(0);
    expect(byTestId('upgrade-execute-activate').props.disabled).toBe(false);
  });

  it('handles POST /execute activate 503 upgrade_worker_unavailable for both restart-to-ready and recovery-to-idle, and resets resumedActivationRef on non-restart failures', async () => {
    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/control',
      inferenceBasePath: '/infer',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const pendingAccess = {
      mode: 'migrate_legacy' as const,
      managementBasePath: '/control',
      inferenceBasePath: '/infer',
      protocols: [],
      ready: false,
      code: 'upgrade_restart_required',
    };
    let statusMode: 'verify' | 'unavailable' | 'canonical' | 'activating' = 'verify';
    const readCurrent = () => {
      if (statusMode === 'canonical') {
        return markCanonicalUpgradeStatus(
          statusDocument({
            stage: 'complete',
            nextAction: 'complete',
            targetMode: 'managed',
            routing,
            accessInfo: {
              ...pendingAccess,
              ready: true,
              code: '',
            },
          })
        );
      }
      return statusDocument({
        stage: 'complete',
        nextAction: 'complete',
        targetMode: 'managed',
        routing,
        accessInfo: pendingAccess,
        execution:
          statusMode === 'unavailable'
            ? {
                available: false,
                running: false,
                managedAvailable: false,
                code: 'upgrade_worker_unavailable',
              }
            : statusMode === 'activating'
              ? { ...workerReady, action: 'activate', running: true, code: '' }
              : { ...workerReady, action: 'verify', code: '' },
      });
    };

    // 1. POST /execute activate -> 503 upgrade_worker_unavailable followed by worker shutdown -> canonical ready.
    const onActivation = vi.fn();
    const executeRestart = vi.fn<UpgradeWizardExecute>(async () => {
      statusMode = 'unavailable';
      throw new UpgradeWizardError('unavailable', 'upgrade_worker_unavailable', 503);
    });
    await mount(resumeRead(readCurrent), undefined, undefined, undefined, executeRestart, onActivation);
    await press('upgrade-execute-activate');
    expect(onActivation).toHaveBeenCalledWith('/control');
    expect(byTestId('upgrade-activation-waiting')).toBeTruthy();
    expect(allByTestId('upgrade-execution-error')).toHaveLength(0);

    statusMode = 'canonical';
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_500);
    });
    expect(allByTestId('upgrade-activation-waiting')).toHaveLength(0);
    expect(byTestId('upgrade-access-active')).toBeTruthy();

    // 2. POST /execute activate -> 503 upgrade_worker_unavailable followed by worker recovering to idle verify:
    // surfaces the actual upgrade_worker_unavailable failure and clears activation.
    await act(async () => renderer!.unmount());
    renderer = undefined;
    statusMode = 'verify';
    onActivation.mockClear();
    const executeRecoverIdle = vi.fn<UpgradeWizardExecute>(async () => {
      throw new UpgradeWizardError('unavailable', 'upgrade_worker_unavailable', 503);
    });
    await mount(resumeRead(readCurrent), undefined, undefined, undefined, executeRecoverIdle, onActivation);
    await press('upgrade-execute-activate');
    expect(onActivation).toHaveBeenCalledWith('/control');
    expect(onActivation).toHaveBeenCalledWith('');
    expect(allByTestId('upgrade-activation-waiting')).toHaveLength(0);
    expect(byTestId('upgrade-execution-error').findByType('code').children.join('')).toBe(
      'upgrade_worker_unavailable'
    );

    // 3. Non-restart failure on activate (e.g. 409) resets resumedActivationRef so subsequent status poll can still resume.
    await act(async () => renderer!.unmount());
    renderer = undefined;
    statusMode = 'verify';
    onActivation.mockClear();
    const executeConflict = vi.fn<UpgradeWizardExecute>(async () => {
      throw new UpgradeWizardError('unavailable', 'upgrade_execution_busy', 409);
    });
    await mount(resumeRead(readCurrent), undefined, undefined, undefined, executeConflict, onActivation);
    await press('upgrade-execute-activate');
    expect(onActivation).not.toHaveBeenCalled();
    expect(byTestId('upgrade-execution-error').findByType('code').children.join('')).toBe(
      'upgrade_execution_busy'
    );

    // Next status poll observes in-flight activation and enters restarting because resumedActivationRef was reset.
    statusMode = 'activating';
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_500);
    });
    expect(onActivation).toHaveBeenCalledWith('/control');
    expect(byTestId('upgrade-activation-waiting')).toBeTruthy();
  });

  it('clears stale activationRequested: true on boot and tick when worker has already recovered to idle', async () => {
    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/control',
      inferenceBasePath: '/infer',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const idleComplete = statusDocument({
      stage: 'complete',
      nextAction: 'complete',
      targetMode: 'managed',
      routing,
      accessInfo: {
        mode: 'migrate_legacy' as const,
        managementBasePath: '/control',
        inferenceBasePath: '/infer',
        protocols: [],
        ready: false,
        code: 'upgrade_restart_required',
      },
      execution: { ...workerReady, action: 'verify', code: '' },
    });
    const onActivation = vi.fn();
    const execute = vi.fn<UpgradeWizardExecute>(async () => executionDocument({ action: 'activate' }));

    await mount(resumeRead(() => idleComplete), undefined, undefined, undefined, execute, onActivation, true);
    expect(onActivation).toHaveBeenCalledWith('');
    expect(allByTestId('upgrade-activation-waiting')).toHaveLength(0);
    expect(allByTestId('upgrade-execution-error')).toHaveLength(0);
    expect(byTestId('upgrade-execute-activate').props.disabled).toBe(false);
  });

  it('keeps localAction and action button disabled when a status poll fires while POST /execute is still in flight', async () => {
    const current = statusDocument({
      stage: 'copy_in',
      nextAction: 'reconcile',
      mismatchDays: [],
      hasParityGaps: false,
      execution: { ...workerReady, running: false, code: '' },
    });
    let statusReads = 0;
    const read = resumeRead(() => {
      statusReads += 1;
      return current;
    });
    let resolveExecute!: (value: unknown) => void;
    const execute = vi.fn<UpgradeWizardExecute>(
      () =>
        new Promise((resolve) => {
          resolveExecute = resolve;
        })
    );

    await mount(read, undefined, undefined, undefined, execute);
    expect(byTestId('upgrade-execute-reconcile').props.disabled).toBe(false);
    const readsBeforeExecute = statusReads;

    // Start dispatchExecution while leaving POST /execute pending.
    await act(async () => {
      byTestId('upgrade-execute-reconcile').props.onClick();
    });
    expect(execute).toHaveBeenCalledTimes(1);
    expect(byTestId('upgrade-execution-running')).toBeTruthy();
    expect(byTestId('upgrade-execute-reconcile').props.disabled).toBe(true);

    // Advance past the 10s poll timer so an armed status poll fires and receives running: false.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_500);
    });
    expect(statusReads).toBeGreaterThan(readsBeforeExecute);
    expect(byTestId('upgrade-execution-running')).toBeTruthy();
    expect(byTestId('upgrade-execute-reconcile').props.disabled).toBe(true);

    // A duplicate click while POST /execute is still in flight remains blocked by actionPendingRef.
    await act(async () => {
      byTestId('upgrade-execute-reconcile').props.onClick();
    });
    expect(execute).toHaveBeenCalledTimes(1);

    await act(async () => {
      resolveExecute(executionDocument({ action: 'reconcile', running: true }));
      await Promise.resolve();
      await Promise.resolve();
    });
  });

  it('enters restart recovery on plain 503 and plain 404 from activate and clears prior execution error upon entering bootstrap-retired recovery', async () => {
    const routing = {
      configured: true,
      committed: true,
      existingPrefixesPreserved: false,
      controlBasePath: '/control',
      inferenceBasePath: '/infer',
      sharedEntry: false,
      externalBaseUrl: '',
      suggestedControlBasePath: '',
      suggestedInferenceBasePath: '',
    };
    const pendingAccess = {
      mode: 'migrate_legacy' as const,
      managementBasePath: '/control',
      inferenceBasePath: '/infer',
      protocols: [],
      ready: false,
      code: 'upgrade_restart_required',
    };
    let statusMode: 'verify' | 'unavailable' | 'retiredRecovery' | 'canonical' = 'verify';
    const readCurrent = () => {
      if (statusMode === 'canonical') {
        return markCanonicalUpgradeStatus(
          statusDocument({
            stage: 'complete',
            nextAction: 'complete',
            targetMode: 'managed',
            routing,
            accessInfo: { ...pendingAccess, ready: true, code: '' },
          })
        );
      }
      if (statusMode === 'retiredRecovery') {
        return markBootstrapRetiredRecovery(
          statusDocument({
            stage: 'complete',
            nextAction: 'complete',
            targetMode: 'managed',
            routing,
            accessInfo: pendingAccess,
            execution: {
              available: false,
              running: false,
              managedAvailable: false,
              code: 'upgrade_worker_unavailable',
            },
          })
        );
      }
      return statusDocument({
        stage: 'complete',
        nextAction: 'complete',
        targetMode: 'managed',
        routing,
        accessInfo: pendingAccess,
        execution:
          statusMode === 'unavailable'
            ? {
                available: false,
                running: false,
                managedAvailable: false,
                code: 'upgrade_worker_unavailable',
              }
            : { ...workerReady, action: 'verify', code: '' },
      });
    };

    // 1. Plain 503 (empty code) on activate enters restart recovery and calls onActivation(prefix).
    const onActivation = vi.fn();
    const executePlain503 = vi.fn<UpgradeWizardExecute>(async () => {
      statusMode = 'unavailable';
      throw new UpgradeWizardError('unavailable', '', 503);
    });
    await mount(resumeRead(readCurrent), undefined, undefined, undefined, executePlain503, onActivation);
    await press('upgrade-execute-activate');
    expect(onActivation).toHaveBeenCalledWith('/control');
    expect(byTestId('upgrade-activation-waiting')).toBeTruthy();
    expect(allByTestId('upgrade-execution-error')).toHaveLength(0);
    statusMode = 'canonical';
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_500);
    });
    expect(allByTestId('upgrade-activation-waiting')).toHaveLength(0);
    expect(byTestId('upgrade-access-active')).toBeTruthy();

    // 2. Plain 404 (empty code) on activate enters restart recovery and calls onActivation(prefix).
    await act(async () => renderer!.unmount());
    renderer = undefined;
    statusMode = 'verify';
    onActivation.mockClear();
    const executePlain404 = vi.fn<UpgradeWizardExecute>(async () => {
      statusMode = 'unavailable';
      throw new UpgradeWizardError('unavailable', '', 404);
    });
    await mount(resumeRead(readCurrent), undefined, undefined, undefined, executePlain404, onActivation);
    await press('upgrade-execute-activate');
    expect(onActivation).toHaveBeenCalledWith('/control');
    expect(byTestId('upgrade-activation-waiting')).toBeTruthy();
    expect(allByTestId('upgrade-execution-error')).toHaveLength(0);
    statusMode = 'canonical';
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_500);
    });
    expect(allByTestId('upgrade-activation-waiting')).toHaveLength(0);
    expect(byTestId('upgrade-access-active')).toBeTruthy();

    // 3. Prior execution error banner is cleared when entering bootstrap-retired recovery on tick.
    await act(async () => renderer!.unmount());
    renderer = undefined;
    statusMode = 'verify';
    onActivation.mockClear();
    const executeConflict = vi.fn<UpgradeWizardExecute>(async () => {
      throw new UpgradeWizardError('unavailable', 'upgrade_execution_busy', 409);
    });
    await mount(resumeRead(readCurrent), undefined, undefined, undefined, executeConflict, onActivation);
    await press('upgrade-execute-activate');
    expect(byTestId('upgrade-execution-error')).toBeTruthy();

    statusMode = 'retiredRecovery';
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_500);
    });
    expect(onActivation).toHaveBeenCalledWith('/control');
    expect(byTestId('upgrade-activation-waiting')).toBeTruthy();
    expect(allByTestId('upgrade-execution-error')).toHaveLength(0);
  });

  it.each([404, 502, 503, 504])(
    'resumes canonical activation recovery on manual boot retry when reverse proxy returns plain %s while bootstrap is retired',
    async (proxyStatus) => {
      const routing = {
        configured: true,
        committed: true,
        existingPrefixesPreserved: false,
        controlBasePath: '/control',
        inferenceBasePath: '/infer',
        sharedEntry: false,
        externalBaseUrl: '',
        suggestedControlBasePath: '',
        suggestedInferenceBasePath: '',
      };
      const readyStatus = markCanonicalUpgradeStatus(
        statusDocument({
          stage: 'complete',
          nextAction: 'complete',
          targetMode: 'managed',
          routing,
          accessInfo: {
            mode: 'migrate_legacy' as const,
            managementBasePath: '/control',
            inferenceBasePath: '/infer',
            protocols: [],
            ready: true,
            code: '',
          },
        })
      );

      let phase: 'initial500' | 'proxyError' | 'canonical' = 'initial500';
      const read = vi.fn<UpgradeWizardRead>(async (resource) => {
        if (resource !== 'installation/upgrade/status') return preflightDocument();
        if (phase === 'initial500') {
          throw new UpgradeWizardError('unavailable', '', 500);
        }
        if (phase === 'proxyError') {
          throw new UpgradeWizardError('unavailable', '', proxyStatus);
        }
        return readyStatus;
      });

      await act(async () => {
        renderer = create(
          <FreshUpgradeFlow
            read={read}
            bootstrapRetired
            language="en"
            pollDelays={[2_000]}
          />
        );
      });
      // Non-recoverable 500 lands on upgrade-boot-failed so manual retry can be exercised.
      expect(byTestId('upgrade-boot-failed')).toBeTruthy();

      // Manual retry hitting plain reverse-proxy 404/502/503/504 enters canonical activation recovery.
      phase = 'proxyError';
      await press('upgrade-boot-retry');
      expect(allByTestId('upgrade-boot-failed')).toHaveLength(0);
      expect(byTestId('upgrade-progress')).toBeTruthy();
      expect(byTestId('upgrade-progress-reading')).toBeTruthy();

      // Repeated proxy 404/502/503/504 polls keep recovering without stalling.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10_000);
      });
      expect(allByTestId('upgrade-boot-failed')).toHaveLength(0);
      expect(allByTestId('upgrade-result-stalled')).toHaveLength(0);
      expect(byTestId('upgrade-progress-reading')).toBeTruthy();

      // Once canonical endpoint responds, activation completes cleanly.
      phase = 'canonical';
      await act(async () => {
        await vi.advanceTimersByTimeAsync(2_500);
      });
      expect(byTestId('upgrade-access-active')).toBeTruthy();
    }
  );

  it('keeps manual boot retry on upgrade-boot-failed for structured 404 with a non-retired code while bootstrap is retired', async () => {
    let phase: 'initial500' | 'structured404' = 'initial500';
    const read = vi.fn<UpgradeWizardRead>(async (resource) => {
      if (resource !== 'installation/upgrade/status') return preflightDocument();
      if (phase === 'initial500') {
        throw new UpgradeWizardError('unavailable', '', 500);
      }
      throw new UpgradeWizardError('unavailable', 'upgrade_source_missing', 404);
    });

    await act(async () => {
      renderer = create(
        <FreshUpgradeFlow
          read={read}
          bootstrapRetired
          language="en"
          pollDelays={[2_000]}
        />
      );
    });
    expect(byTestId('upgrade-boot-failed')).toBeTruthy();

    phase = 'structured404';
    await press('upgrade-boot-retry');
    expect(byTestId('upgrade-boot-failed')).toBeTruthy();
    expect(allByTestId('upgrade-progress')).toHaveLength(0);
    expect(allByTestId('upgrade-progress-reading')).toHaveLength(0);
  });

  it('surfaces the pinned Managed CPA baseline artifact on preflight and rejects malformed digests', async () => {
    const digest = 'sha256:' + 'e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7';
    expect(parseUpgradePreflight(preflightDocument({ qualifiedCpaArtifactId: digest }))?.qualifiedCpaArtifactId).toBe(
      digest
    );
    expect(parseUpgradePreflight(preflightDocument({ qualifiedCpaArtifactId: '' }))).toBeNull();
    expect(parseUpgradePreflight(preflightDocument({ qualifiedCpaArtifactId: 'sha256:short' }))).toBeNull();
    expect(parseUpgradePreflight(preflightDocument({ qualifiedCpaArtifactId: 123 }))).toBeNull();
    const read: UpgradeWizardRead = vi.fn(async (resource) =>
      resource === 'installation/upgrade/status'
        ? unrecordedStatus()
        : preflightDocument({ qualifiedCpaArtifactId: digest })
    );
    await mount(read);
    const baseline = byTestId('upgrade-preflight-cpa-baseline');
    expect(baseline.props.title).toBe(digest);
    expect(baseline.children.join('')).toBe('sha256:e48c85c75a19…');
  });

  it('keeps the Console and Gateway URL copy banner visible on result even when accessInfo is omitted after canonical cutover', async () => {
    const writeText = vi.fn(async () => undefined);
    vi.stubGlobal('window', { setTimeout, clearTimeout, location: { origin: 'https://manager.example' } });
    vi.stubGlobal('navigator', { clipboard: { writeText } });

    const canonicalWithoutAccessInfo = markCanonicalUpgradeStatus(
      statusDocument({
        stage: 'complete',
        nextAction: 'complete',
        targetMode: 'managed',
        routing: {
          configured: true,
          committed: true,
          existingPrefixesPreserved: false,
          controlBasePath: '/cpamp-ctl',
          inferenceBasePath: '/cpamp-inf',
          sharedEntry: false,
          externalBaseUrl: '',
          suggestedControlBasePath: '',
          suggestedInferenceBasePath: '',
        },
      })
    );

    await act(async () => {
      renderer = create(
        <FreshUpgradeFlow
          read={vi.fn(async () => canonicalWithoutAccessInfo)}
          language="zh-CN"
          pollDelays={[10_000]}
        />
      );
    });

    expect(byTestId('upgrade-result-console-banner')).toBeTruthy();
    expect(allByTestId('upgrade-access-active')).toHaveLength(0);
    expect(byTestId('upgrade-gateway-waiting')).toBeTruthy();
    expect(byTestId('access-info-management').children.join('')).toBe('https://manager.example/cpamp-ctl');
    expect(byTestId('access-info-inference').children.join('')).toBe('https://manager.example/cpamp-inf');
    expect(byTestId('upgrade-open-console').props.href).toBe('https://manager.example/cpamp-ctl');

    await press('access-info-copy-management');
    expect(writeText).toHaveBeenCalledWith('https://manager.example/cpamp-ctl');
    await press('access-info-copy-inference');
    expect(writeText).toHaveBeenCalledWith('https://manager.example/cpamp-inf');

    const externalWithoutAccessInfoOrUrl = markCanonicalUpgradeStatus(
      statusDocument({
        stage: 'complete',
        nextAction: 'complete',
        targetMode: 'external',
        routing: {
          configured: true,
          committed: true,
          existingPrefixesPreserved: false,
          controlBasePath: '/cpamp-ext',
          inferenceBasePath: '',
          sharedEntry: false,
          externalBaseUrl: '',
          suggestedControlBasePath: '',
          suggestedInferenceBasePath: '',
        },
      })
    );

    await act(async () => {
      renderer!.update(
        <FreshUpgradeFlow
          read={vi.fn(async () => externalWithoutAccessInfoOrUrl)}
          language="zh-CN"
          pollDelays={[10_000]}
        />
      );
    });

    expect(byTestId('upgrade-result-console-banner')).toBeTruthy();
    expect(allByTestId('access-info-error')).toHaveLength(0);
    expect(allByTestId('upgrade-access-active')).toHaveLength(0);
    expect(byTestId('access-info-management').children.join('')).toBe('https://manager.example/cpamp-ext');
    expect(byTestId('upgrade-open-console').props.href).toBe('https://manager.example/cpamp-ext');
  });
});
