import { describe, expect, it } from 'vitest';
import { resolveRetentionCutoff } from './usageMaintenanceModel';
import {
  readUsageMaintenanceNavigation,
  writeUsageMaintenanceNavigation,
} from './usageMaintenanceNavigation';

const now = Date.UTC(2026, 8, 15, 4);
const day = 86_400_000;

describe('usage maintenance navigation', () => {
  it('defaults to a one-time archive and ignores another page’s query', () => {
    const state = readUsageMaintenanceNavigation(
      '#/accounts?tab=history&run=other&intent=cleanup',
      now
    );
    expect(state).toMatchObject({
      tab: 'organize',
      runId: null,
      panel: null,
      intent: 'archive',
      retention: 30,
    });
  });

  it('restores the record, drawer, purpose, range and filter without action parameters', () => {
    const hash =
      '#/usage-maintenance?tab=history&run=archive-123&panel=run&intent=cleanup&days=custom&before=' +
      (now - day) +
      '&filter=verified&delete=true';
    const state = readUsageMaintenanceNavigation(hash, now);
    expect(state).toMatchObject({
      tab: 'organize',
      runId: 'archive-123',
      panel: 'run',
      intent: 'cleanup',
      retention: 'custom',
      filter: 'verified',
    });
    const search = writeUsageMaintenanceNavigation(state);
    expect(search).not.toContain('delete=');
    expect(readUsageMaintenanceNavigation('#/usage-maintenance' + search, now)).toEqual(state);
  });

  it('keeps a preset’s exact cutoff after refresh instead of advancing its target', () => {
    const initial = readUsageMaintenanceNavigation('', now);
    const restored = readUsageMaintenanceNavigation(
      '#/usage-maintenance' + writeUsageMaintenanceNavigation(initial),
      now + day
    );
    expect(
      resolveRetentionCutoff(restored.retention, restored.customCutoff, restored.referenceNowMS)
    ).toBe(now - 30 * day);
  });

  it.each(['0', '-1', 'Infinity', 'not-a-date', String(now + day)])(
    'rejects invalid or future cutoffs: %s',
    (before) => {
      const state = readUsageMaintenanceNavigation(
        '#/usage-maintenance?days=custom&before=' + before,
        now
      );
      expect(
        resolveRetentionCutoff(state.retention, state.customCutoff, state.referenceNowMS)
      ).toBe(now - 30 * day);
    }
  );

  it('does not let a forged preset advance the custom input’s maximum into the future', () => {
    const state = readUsageMaintenanceNavigation(
      '#/usage-maintenance?days=90&before=' + (now - day),
      now
    );
    expect(state.referenceNowMS).toBe(now);
  });

  it('rejects unsupported tabs, filters and unsafe record identifiers', () => {
    const state = readUsageMaintenanceNavigation(
      '#/usage-maintenance?tab=delete&filter=all-events&panel=run&run=../../admin',
      now
    );
    expect(state).toMatchObject({ tab: 'organize', filter: 'all', panel: null, runId: null });
  });

  it('normalizes legacy history links to archive management with their source and status', () => {
    expect(
      readUsageMaintenanceNavigation(
        '#/usage-maintenance?tab=history&source=retention&filter=completed',
        now
      )
    ).toMatchObject({ tab: 'organize', panel: null, source: 'retention', filter: 'completed' });
  });

  it('opens creation only when requested and prevents a record ID from blocking its preview', () => {
    expect(readUsageMaintenanceNavigation('#/usage-maintenance?days=7', now).panel).toBeNull();
    expect(
      readUsageMaintenanceNavigation('#/usage-maintenance?panel=create&run=unrelated&days=7', now)
    ).toMatchObject({ panel: 'create', runId: null, retention: 7 });
  });

  it('restores an import detail without accepting archive action parameters', () => {
    const state = readUsageMaintenanceNavigation(
      '#/usage-maintenance?tab=transfer&panel=import-session&session=import-123&run=unrelated&delete=true',
      now
    );
    expect(state).toMatchObject({ panel: 'import-session', sessionId: 'import-123', runId: null });
    expect(writeUsageMaintenanceNavigation(state)).not.toContain('delete');
    expect(
      readUsageMaintenanceNavigation(
        '#/usage-maintenance' + writeUsageMaintenanceNavigation(state),
        now
      )
    ).toEqual(state);
  });

  it('rejects unsupported sources and unsafe import identifiers', () => {
    expect(
      readUsageMaintenanceNavigation(
        '#/usage-maintenance?tab=transfer&panel=import-session&session=../../admin&source=external',
        now
      )
    ).toMatchObject({ panel: null, sessionId: null, source: 'all' });
  });
});
