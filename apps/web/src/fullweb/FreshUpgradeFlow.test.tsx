import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  createUpgradeWizardRead,
  createUpgradeWizardVerify,
  FreshUpgradeFlow,
  parseUpgradePreflight,
  parseUpgradeVerify,
  type UpgradeStatus,
  UpgradeWizardError,
  type UpgradeWizardRead,
  type UpgradeWizardResource,
  type UpgradeWizardVerify,
} from './FreshUpgradeFlow';
import { blockedCodeText, upgradeWizardText as t } from '../i18n/upgradeWizard';

// The wizard is a reader. These tests drive the four steps with injected reads:
// the routes themselves are proven by the Go handler tests, so nothing here
// touches a server or the browser credential.

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
function statusDocument(overrides: Partial<UpgradeStatus> = {}) {
  const { mismatchDays = [], mismatchDaysTotal, mismatchDaysTruncated, ...rest } = overrides;
  const total = mismatchDaysTotal ?? mismatchDays.length;
  return {
    contractVersion: CONTRACT,
    recorded: true,
    stage: 'copy_in',
    stageKnown: true,
    nextAction: 'copy_in',
    domainsDone: ['source_copy'],
    sourceKind: 'cpamp_1x',
    sourceVersion: '1.9.4',
    manualItems: [],
    errorCode: '',
    errorDetail: '',
    ...rest,
    reconciliation: {
      mismatchDays,
      mismatchDaysTotal: total,
      mismatchDaysTruncated: mismatchDaysTruncated ?? total > mismatchDays.length,
    },
  };
}

// Nothing recorded yet: the store answered, and its answer is the precondition
// for the preflight step.
function unrecordedStatus() {
  return statusDocument({ recorded: false, stage: '', nextAction: 'preflight', domainsDone: [] });
}

// The action's own reply. The gate records exactly two positions — complete on an
// External target and verify (waiting) on a Managed one — so the fixture is
// written as the Managed waiting position and every test overrides from there.
function verifyDocument(overrides: Record<string, unknown> = {}) {
  return {
    contractVersion: CONTRACT,
    recorded: true,
    stage: 'verify',
    stageKnown: true,
    complete: false,
    targetMode: 'managed',
    domainsDone: [],
    reasonCode: 'upgrade_verify_waiting_for_repin',
    reason: 'wait for the trusted U1-1b re-pin',
    manualItems: [],
    alreadyVerified: false,
    updatedAtMs: 40,
    ...overrides,
  };
}

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

  async function mount(read: UpgradeWizardRead, verify?: UpgradeWizardVerify) {
    await act(async () => {
      renderer = create(
        <FreshUpgradeFlow read={read} verify={verify} language="en" pollDelays={[10]} />
      );
    });
  }

  const view = () => JSON.stringify(renderer!.toJSON());
  const byTestId = (testid: string) => renderer!.root.findByProps({ 'data-testid': testid });
  const allByTestId = (testid: string) => renderer!.root.findAllByProps({ 'data-testid': testid });

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

  it('shows a pending version declaration as information, not as a failure', async () => {
    await mount(
      wizardRead({
        preflight: () =>
          preflightDocument({
            checks: [
              { name: 'source_ownership', status: 'pass', code: '', detail: 'no live source writer' },
              {
                name: 'source_version',
                status: 'pending',
                code: 'upgrade_source_version_declared_at_copy_in',
                detail: 'expected=v1.14.10 declared=none',
              },
              { name: 'fingerprint', status: 'pass', code: '', detail: 'sha256 over source manifest' },
              { name: 'disk', status: 'pass', code: '', detail: 'needs 12 GiB, has 40 GiB' },
              { name: 'cpa_classification', status: 'pass', code: '', detail: 'kind=external' },
              { name: 'parity', status: 'pass', code: '', detail: 'no gaps' },
            ],
            pendingDeclaration: true,
            expectedVersion: 'v1.14.10',
          }),
      })
    );

    expect(byTestId('upgrade-preflight-pending-declaration')).toBeTruthy();
    expect(view()).toContain('--source-version v1.14.10');
    expect(view()).toContain('declared at copy-in');
    // The row is labelled pending, in the same vocabulary as every other check.
    expect(byTestId('upgrade-check-source_version').props['data-status']).toBe('pending');
    expect(view()).toContain('Pending');
    // Nothing is presented as blocked, and the step stays usable: readiness is a
    // separate fact from the declaration this read cannot make.
    expect(allByTestId('upgrade-preflight-blocked')).toHaveLength(0);
    expect(view()).not.toContain('Preflight blocked the upgrade');
    expect(byTestId('upgrade-continue').props.disabled).toBe(false);
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
    expect(view()).toContain('--source-version v1.14.10');
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
        await vi.advanceTimersByTimeAsync(10);
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
    // A deferred CPA import is the position this build hands to the gate: it
    // recorded no cpa_config, so the next decision is the verification that names
    // where the operation really stands.
    const deferred = resumeRead(() =>
      statusDocument({
        stage: 'cpa_import',
        nextAction: 'verify',
        manualItems: ['section:ampcode: re-create it (upgrade_cpa_import_requires_qualification)'],
      })
    );
    await mount(deferred, vi.fn());
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
    await mount(deferred);
    expect(allByTestId('upgrade-verify-action')).toHaveLength(0);
  });

  it('presents the action verdict and then re-reads what the store recorded', async () => {
    const verified = statusDocument({
      stage: 'verify',
      nextAction: 'verify',
      domainsDone: ['source_copy', 'usage_events', 'identity'],
      errorCode: 'upgrade_verify_waiting_for_repin',
      errorDetail: 'wait for the trusted U1-1b re-pin',
      manualItems: ['verification: wait (upgrade_verify_waiting_for_repin)'],
    });
    let current = statusDocument({
      stage: 'cpa_import',
      nextAction: 'verify',
      manualItems: ['section:ampcode: re-create it (upgrade_cpa_import_requires_qualification)'],
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
    const statusReadsBefore = vi.mocked(read).mock.calls.filter(
      ([resource]) => resource === 'installation/upgrade/status'
    ).length;

    await press('upgrade-verify-action');
    expect(verify).toHaveBeenCalledTimes(1);
    // The action's own answer is shown, and the row is read again so the page
    // presents the record rather than this reply.
    expect(byTestId('upgrade-verify-done')).toBeTruthy();
    expect(byTestId('upgrade-verify-outcome').children.join('')).toContain(t('verifyWaiting', 'en'));
    expect(byTestId('upgrade-verify-target').children.join('')).toContain(
      t('verifyTargetManaged', 'en')
    );
    expect(
      vi.mocked(read).mock.calls.filter(([resource]) => resource === 'installation/upgrade/status')
    ).toHaveLength(statusReadsBefore + 1);
    expect(byTestId('upgrade-progress-reason')).toBeTruthy();
    expect(view()).toContain('upgrade_verify_waiting_for_repin');
    // The Managed position is never presented as a completion.
    expect(allByTestId('upgrade-result-complete')).toHaveLength(0);
  });

  it('reports a refused verification instead of moving the operation on', async () => {
    const read = resumeRead(() => statusDocument({ stage: 'external_connect', nextAction: 'verify' }));
    const verify = vi.fn<UpgradeWizardVerify>(async () => {
      throw new UpgradeWizardError('unavailable', 'upgrade_verify_binding_drifted', 409);
    });
    await mount(read, verify);
    const statusReadsBefore = vi.mocked(read).mock.calls.filter(
      ([resource]) => resource === 'installation/upgrade/status'
    ).length;

    await press('upgrade-verify-action');
    expect(byTestId('upgrade-verify-failed')).toBeTruthy();
    expect(view()).toContain(blockedCodeText('upgrade_verify_binding_drifted', 'en'));
    expect(allByTestId('upgrade-verify-done')).toHaveLength(0);
    // Nothing was re-read into a stage: the recorded position is unchanged.
    expect(
      vi.mocked(read).mock.calls.filter(([resource]) => resource === 'installation/upgrade/status')
    ).toHaveLength(statusReadsBefore);
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
      // Completion can only belong to an External target.
      { stage: 'complete', complete: true, targetMode: 'managed' },
      // The Managed waiting position can only belong to a Managed target.
      { complete: false, targetMode: 'external' },
      // The waiting position always names the reason it waits.
      { complete: false, reasonCode: '' },
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

    // The two positions the gate really records are read as they are.
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
      verifyDocument({ stage: 'complete', complete: true, targetMode: 'managed' })
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
});
