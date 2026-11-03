import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  createUpgradeWizardRead,
  FreshUpgradeFlow,
  type UpgradeStatus,
  type UpgradeWizardRead,
  type UpgradeWizardResource,
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
    ...overrides,
  };
}

// The route's own shape: mismatch days travel inside `reconciliation`, never as
// a top-level field, so the fixture mirrors that projection.
function statusDocument(overrides: Partial<UpgradeStatus> = {}) {
  const { mismatchDays = [], ...rest } = overrides;
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
    reconciliation: { mismatchDays },
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

  async function mount(read: UpgradeWizardRead) {
    await act(async () => {
      renderer = create(<FreshUpgradeFlow read={read} language="en" pollDelays={[10]} />);
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

  function reader(status: () => unknown): UpgradeWizardRead {
    return vi.fn(async (resource: UpgradeWizardResource) =>
      resource === 'installation/upgrade/preflight' ? preflightDocument() : status()
    );
  }

  it('renders every check and names each blocking code, holding Continue disabled', async () => {
    const blocked = ['upgrade_source_busy', 'upgrade_disk_insufficient'];
    await mount(async () =>
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

  it('continues from a ready preflight only after an explicit acknowledgement', async () => {
    await mount(reader(() => statusDocument()));

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
      reader(() => statusDocument({ domainsDone: ['source_copy', 'usage_events'] }))
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
    const read: UpgradeWizardRead = vi.fn(async (resource: UpgradeWizardResource) => {
      if (resource === 'installation/upgrade/preflight') return preflightDocument();
      calls += 1;
      if (calls <= 3) throw new Error('lost');
      return statusDocument({ stage: 'complete', nextAction: 'complete' });
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
      reader(() =>
        statusDocument({
          stage: 'rolled_back',
          nextAction: 'rolled_back',
          domainsDone: ['source_copy'],
          errorCode: 'upgrade_target_modified',
          errorDetail: 'the target store changed during the copy',
        })
      )
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
      reader(() =>
        statusDocument({
          stage: 'complete',
          nextAction: 'complete',
          mismatchDays: ['2026-10-01', '2026-10-02'],
        })
      )
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
      await reachProgress(reader(() => statusDocument(overrides)));
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
    await mount(async () => preflightDocument({ blocked: ['upgrade_source_busy'], ready: true }));

    expect(byTestId('upgrade-preflight-error')).toBeTruthy();
    expect(allByTestId('upgrade-check-source_ownership')).toHaveLength(0);
    expect(byTestId('upgrade-continue').props.disabled).toBe(true);
  });
});
