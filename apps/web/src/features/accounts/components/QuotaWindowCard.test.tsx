import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import type {
  AccountDetailQuotaWindow,
  AccountDetailWindowUsageSummary,
} from '@/features/accounts/model/accountDetailViewModel';
import { QuotaWindowCard } from './QuotaWindowCard';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return {
    ...actual,
    useTranslation: () => ({
      t: (key: string) => key,
      i18n: { language: 'en-US' },
    }),
  };
});

const readText = (value: unknown): string => {
  if (typeof value === 'string' || typeof value === 'number') return String(value);
  if (Array.isArray(value)) return value.map(readText).join('');
  if (value && typeof value === 'object' && 'children' in value) {
    return readText((value as { children?: unknown }).children);
  }
  return '';
};

const formatDisplayRange = (fromMs: number, toMs: number): string => {
  const formatter = new Intl.DateTimeFormat('en-US', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  });
  return `${formatter.format(fromMs)} — ${formatter.format(toMs)}`;
};

const usage = (
  overrides: Partial<AccountDetailWindowUsageSummary> = {}
): AccountDetailWindowUsageSummary => ({
  fromMs: 1_000,
  toMs: 2_000,
  matched: true,
  totalRequests: 100,
  successCalls: 99,
  failureCalls: 1,
  totalTokens: 1_000_000,
  totalCost: 100,
  successRate: 99,
  lastSeenMs: 2_000,
  syncStatus: 'ready',
  scopeMatchStatus: 'complete',
  unmatchedRequests: 0,
  ...overrides,
});

const makeWindow = (overrides: Partial<AccountDetailQuotaWindow> = {}): AccountDetailQuotaWindow =>
  ({
    key: 'five-hour',
    label: '5H',
    kind: 'five_hour',
    remainingPercent: 40,
    usedPercent: 60,
    resetLabel: '-',
    resetAtMs: 20_000,
    resetAccuracy: 'estimated',
    limitWindowSeconds: 18_000,
    fromMs: 1_000,
    toMs: 2_000,
    source: 'codex',
    observationSource: 'response_header',
    observedAtMs: 2_000,
    windowMode: 'fixed',
    cycleStartMs: 1_000,
    cycleEndMs: 20_000,
    modelScope: { kind: 'all', complete: true },
    boundaryAccuracy: 'derived',
    stale: false,
    usage: usage(),
    currentUsage: usage(),
    previousUsage: usage({ fromMs: -17_999_000, toMs: 1_000, successRate: 98 }),
    previousPeriod: 'previous',
    forecast: { requests: 200, tokens: 2_000_000, cost: 200, basis: 'quota' },
    ...overrides,
  }) as AccountDetailQuotaWindow;

const renderCard = (
  window: AccountDetailQuotaWindow,
  mode?: 'standard' | 'model' | 'other'
): ReactTestRenderer => {
  let renderer!: ReactTestRenderer;
  act(() => {
    renderer = create(<QuotaWindowCard window={window} mode={mode} locale="en-US" />);
  });
  return renderer;
};

describe('QuotaWindowCard', () => {
  it('renders the standard quota as previous, current, and forecast columns', () => {
    const renderer = renderCard(makeWindow());
    expect(renderer.root.findByProps({ 'data-quota-standard-comparison': 'true' })).toBeTruthy();
    const columns = renderer.root.findByProps({ 'data-quota-standard-comparison': 'true' });
    const forecast = columns.findAll((node) =>
      node.props.className?.includes('compareColumnPrediction')
    )[0];
    if (!forecast) throw new Error('forecast column not found');
    const forecastText = readText(forecast);
    expect(forecastText).toContain('accounts.detail_forecast_requests');
    expect(forecastText).toContain('accounts.detail_forecast_tokens');
    expect(forecastText).toContain('accounts.detail_forecast_cost');
    expect(forecastText).toContain('$200.00');
    expect(forecastText).toContain('accounts.detail_forecast_basis_quota');
    expect(forecastText).not.toContain('accounts.detail_success_rate');
    expect(
      forecast.findByProps({ 'data-quota-forecast-success-rate': 'unavailable' })
    ).toBeTruthy();
    expect(renderer.root.findAllByProps({ 'data-quota-progress': 'shared' })).toHaveLength(1);

    const previous = renderer.root.findByProps({ 'data-quota-usage-period': 'previous' });
    const previousText = readText(previous);
    expect(previousText).toContain('accounts.detail_success_rate');
    expect(previousText).toContain('$100.00');
    expect(previousText).not.toContain('accounts.detail_used');
  });

  it('treats the Codex main family scope as an account-wide standard quota', () => {
    const renderer = renderCard(
      makeWindow({
        modelScope: { kind: 'family', key: 'codex_main', complete: true },
      })
    );

    expect(renderer.root.findAllByProps({ 'data-quota-card-mode': 'standard' })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-quota-standard-comparison': 'true' })).toHaveLength(
      1
    );
    expect(renderer.root.findAllByProps({ 'data-quota-model-comparison': 'true' })).toHaveLength(0);
  });

  it('keeps a fixed billing interval in standard mode when mode is inferred', () => {
    const renderer = renderCard(
      makeWindow({
        kind: 'billing',
        windowMode: 'fixed',
        limitWindowSeconds: 30 * 24 * 60 * 60,
        cycleStartMs: 1_000,
        cycleEndMs: 30 * 24 * 60 * 60 * 1_000,
      })
    );

    expect(renderer.root.findAllByProps({ 'data-quota-card-mode': 'standard' })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-quota-standard-comparison': 'true' })).toHaveLength(
      1
    );
    expect(renderer.root.findAllByProps({ 'data-quota-card-mode': 'other' })).toHaveLength(0);
  });

  it('uses semantic colors for the current usage metric icons', () => {
    const renderer = renderCard(makeWindow());
    const current = renderer.root.findByProps({ 'data-quota-usage-period': 'current' });
    const icons = current.findAll(
      (node) => typeof node.props.className === 'string' && node.props.className.includes('rowIcon')
    );

    expect(icons).toHaveLength(4);
    expect(icons.map((node) => node.props.className)).toEqual([
      expect.stringContaining('rowIconBlue'),
      expect.stringContaining('rowIconTeal'),
      expect.stringContaining('rowIconAmber'),
      expect.stringContaining('rowIconGreen'),
    ]);

    const forecast = renderer.root.findAll(
      (node) =>
        typeof node.props.className === 'string' &&
        node.props.className.includes('compareColumnPrediction')
    )[0];
    if (!forecast) throw new Error('forecast column not found');
    const forecastIcons = forecast.findAll(
      (node) => typeof node.props.className === 'string' && node.props.className.includes('rowIcon')
    );
    expect(forecastIcons.map((node) => node.props.className)).toEqual([
      expect.stringContaining('rowIconBlue'),
      expect.stringContaining('rowIconTeal'),
      expect.stringContaining('rowIconAmber'),
    ]);
  });

  it('uses the shared compact formatter for large token counts', () => {
    const renderer = renderCard(
      makeWindow({
        currentUsage: usage({ totalTokens: 1_000_190_000 }),
      })
    );
    const currentText = readText(
      renderer.root.findByProps({ 'data-quota-usage-period': 'current' })
    );

    expect(currentText).toContain('1.0B');
    expect(currentText).not.toContain('1000.2M');
  });

  it('uses complete fixed-cycle boundaries instead of the current data cutoff', () => {
    const cycleStartMs = Date.parse('2026-08-05T10:00:00Z');
    const cycleEndMs = Date.parse('2026-08-05T15:00:00Z');
    const dataEndMs = Date.parse('2026-08-05T11:40:00Z');
    const window = makeWindow({
      cycleStartMs,
      cycleEndMs,
      usage: usage({ fromMs: cycleStartMs, toMs: dataEndMs }),
      currentUsage: usage({ fromMs: cycleStartMs, toMs: dataEndMs }),
    });

    for (const mode of ['standard', 'model'] as const) {
      const renderer = renderCard(window, mode);
      const current = renderer.root.findByProps({ 'data-quota-usage-period': 'current' });
      const currentText = readText(current);

      expect(currentText).toContain(formatDisplayRange(cycleStartMs, cycleEndMs));
      expect(currentText).not.toContain(formatDisplayRange(cycleStartMs, dataEndMs));
    }
  });

  it('does not present a provisional current candidate as a confirmed range', () => {
    const currentStartMs = Date.parse('2026-08-28T17:42:00Z');
    const currentEndMs = Date.parse('2026-08-28T22:42:00Z');
    const previousStartMs = Date.parse('2026-08-28T07:23:00Z');
    const previousEndMs = Date.parse('2026-08-28T12:23:00Z');
    const window = makeWindow({
      boundaryAccuracy: 'unknown',
      cycleStartMs: currentStartMs,
      cycleEndMs: currentEndMs,
      usage: null,
      currentUsage: null,
      previousUsage: usage({ fromMs: previousStartMs, toMs: previousEndMs }),
      forecast: null,
      currentCycle: {
        id: 2,
        activationId: 1,
        state: 'provisional',
        scheduledStartMs: currentStartMs,
        scheduledEndMs: currentEndMs,
        actualStartMs: currentStartMs,
        actualEndMs: null,
        durationSeconds: 5 * 60 * 60,
        boundaryAccuracy: 'unknown',
        endReason: '',
        parentCycleId: null,
        forecastEligible: false,
      },
      previousCycle: {
        id: 1,
        activationId: 1,
        state: 'closed',
        scheduledStartMs: previousStartMs,
        scheduledEndMs: previousEndMs,
        actualStartMs: previousStartMs,
        actualEndMs: previousEndMs,
        durationSeconds: 5 * 60 * 60,
        boundaryAccuracy: 'exact',
        endReason: 'scheduled',
        parentCycleId: null,
        forecastEligible: false,
      },
    });

    const renderer = renderCard(window);
    const current = renderer.root.findByProps({ 'data-quota-usage-period': 'current' });
    const previous = renderer.root.findByProps({ 'data-quota-usage-period': 'previous' });

    expect(readText(current)).toContain('accounts.detail_current_window_boundary_unconfirmed');
    expect(readText(current)).not.toContain(formatDisplayRange(currentStartMs, currentEndMs));
    expect(readText(previous)).toContain(formatDisplayRange(previousStartMs, previousEndMs));
  });

  it('shows scheduled previous-cycle bounds while retaining actual usage bounds', () => {
    const scheduledStartMs = Date.parse('2026-08-09T00:28:00Z');
    const scheduledEndMs = Date.parse('2026-08-16T00:28:00Z');
    const actualEndMs = Date.parse('2026-08-11T00:00:00Z');
    const window = makeWindow({
      previousCycle: {
        id: 1,
        activationId: 1,
        state: 'closed',
        scheduledStartMs,
        scheduledEndMs,
        actualStartMs: scheduledStartMs,
        actualEndMs,
        durationSeconds: 7 * 24 * 60 * 60,
        boundaryAccuracy: 'exact',
        endReason: 'early_reset',
        parentCycleId: null,
        forecastEligible: false,
      },
      previousUsage: usage({ fromMs: scheduledStartMs, toMs: actualEndMs }),
    });

    for (const mode of ['standard', 'model'] as const) {
      const renderer = renderCard(window, mode);
      const previous = renderer.root.findByProps({ 'data-quota-usage-period': 'previous' });
      const previousText = readText(previous);

      expect(previousText).toContain(formatDisplayRange(scheduledStartMs, scheduledEndMs));
      expect(previousText).not.toContain(formatDisplayRange(scheduledStartMs, actualEndMs));
      expect(window.previousUsage).toMatchObject({
        fromMs: scheduledStartMs,
        toMs: actualEndMs,
      });
    }
  });

  it('labels rolling comparisons separately and exposes stale boundary evidence', () => {
    const renderer = renderCard(
      makeWindow({
        label: 'Last 24 hours',
        windowMode: 'rolling',
        previousPeriod: 'previous_equal_range',
        forecast: null,
        boundaryAccuracy: 'estimated',
        stale: true,
      })
    );
    const text = readText(renderer.root);
    expect(text).toContain('Last 24 hours');
    expect(text).toContain('accounts.detail_previous_equal_range');
    expect(text).toContain('accounts.detail_rolling_estimated_recovery');
    expect(text).toContain('accounts.quota_boundary_estimated');
    expect(text).toContain('accounts.detail_quota_snapshot_stale');
    expect(text).toContain('accounts.detail_forecast_unavailable');
    expect(renderer.root.findAllByProps({ 'data-quota-usage-forecast': 'true' })).toHaveLength(0);
  });

  it('renders lifecycle, reset, reopening, and subwindow evidence', () => {
    const renderer = renderCard(
      makeWindow({
        availability: 'active',
        activationGeneration: 2,
        relationshipKind: 'concurrent_subwindow',
        containerProviderWindowId: 'weekly',
        previousCycle: {
          id: 1,
          activationId: 1,
          state: 'closed',
          scheduledStartMs: 1_000,
          scheduledEndMs: 2_000,
          actualStartMs: 1_000,
          actualEndMs: 1_500,
          durationSeconds: 1,
          boundaryAccuracy: 'exact',
          endReason: 'provider_reset',
          parentCycleId: null,
          forecastEligible: false,
        },
      })
    );

    expect(renderer.root.findByProps({ 'data-quota-window-availability': 'active' })).toBeTruthy();
    expect(renderer.root.findByProps({ 'data-quota-lifecycle-notice': 'reopened' })).toBeTruthy();
    expect(
      renderer.root.findByProps({ 'data-quota-lifecycle-notice': 'provider_reset' })
    ).toBeTruthy();
    expect(
      renderer.root.findByProps({ 'data-quota-window-relationship': 'subwindow' })
    ).toBeTruthy();
    const text = readText(renderer.root);
    expect(text).toContain('accounts.detail_quota_window_reopened');
    expect(text).toContain('accounts.detail_quota_window_provider_reset');
    expect(text).toContain('accounts.detail_quota_window_subwindow');
  });

  it('uses the specific inactive lifecycle warning instead of the generic stale warning', () => {
    const renderer = renderCard(
      makeWindow({
        availability: 'inactive',
        stale: true,
        usage: null,
        currentUsage: null,
        previousUsage: null,
        forecast: null,
      })
    );

    expect(
      renderer.root.findByProps({ 'data-quota-window-availability': 'inactive' })
    ).toBeTruthy();
    expect(renderer.root.findByProps({ 'data-quota-lifecycle-notice': 'inactive' })).toBeTruthy();
    const text = readText(renderer.root);
    expect(text).toContain('accounts.detail_quota_window_inactive');
    expect(text).not.toContain('accounts.detail_quota_snapshot_stale');
  });

  it('does not render interval usage for non-window quota', () => {
    const renderer = renderCard(
      makeWindow({
        windowMode: 'non_window',
        usage: null,
        currentUsage: null,
        previousUsage: null,
        previousPeriod: null,
        forecast: null,
      })
    );
    const text = readText(renderer.root);
    expect(text).toContain('accounts.detail_used');
    expect(text).not.toContain('accounts.detail_window_stats_empty');
    expect(text).not.toContain('accounts.detail_window_requests');
    expect(text).not.toContain('accounts.detail_current_window');
  });

  it('renders model quota cards with a provider-window warning', () => {
    const renderer = renderCard(
      makeWindow({
        modelScope: { kind: 'models', models: ['demo-model'], complete: true },
        usage: null,
        currentUsage: null,
        previousUsage: null,
        forecast: null,
      }),
      'model'
    );

    expect(renderer.root.findAllByProps({ 'data-quota-card-mode': 'model' })).toHaveLength(1);
    const warning = renderer.root.findByProps({ 'data-quota-model-warning': 'true' });
    expect(warning).toBeTruthy();
    expect(warning.props.role).toBe('alert');
    expect(renderer.root.findByProps({ 'data-quota-window-icon': 'model' })).toBeTruthy();
    expect(renderer.root.findAllByProps({ 'data-quota-progress': 'shared' })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-quota-standard-comparison': 'true' })).toHaveLength(
      0
    );
    expect(readText(renderer.root)).toContain('accounts.detail_model_window_stats_unavailable');
  });

  it('renders model quota cards with previous, current, and forecast columns', () => {
    const renderer = renderCard(
      makeWindow({
        modelScope: { kind: 'family', key: 'gemini', complete: true },
      }),
      'model'
    );

    const comparison = renderer.root.findByProps({ 'data-quota-model-comparison': 'true' });
    expect(comparison.findByProps({ 'data-quota-usage-period': 'previous' })).toBeTruthy();
    expect(comparison.findByProps({ 'data-quota-usage-period': 'current' })).toBeTruthy();
    expect(comparison.findByProps({ 'data-quota-usage-forecast': 'true' })).toBeTruthy();
    expect(readText(comparison)).toContain('accounts.detail_forecast_requests');
    expect(readText(comparison)).toContain('accounts.detail_forecast_tokens');
    expect(readText(comparison)).toContain('accounts.detail_forecast_cost');
    expect(readText(comparison)).not.toContain('accounts.detail_model_window_stats_unavailable');
    expect(renderer.root.findAllByProps({ 'data-quota-standard-comparison': 'true' })).toHaveLength(
      0
    );
  });

  it('keeps model comparisons when only the previous window has actual usage', () => {
    const renderer = renderCard(
      makeWindow({
        modelScope: { kind: 'family', key: 'gemini', complete: true },
        usage: usage({ matched: false }),
        currentUsage: usage({ matched: false }),
        forecast: { requests: 120, tokens: 1_200_000, cost: 120, basis: 'previous' },
      }),
      'model'
    );

    expect(renderer.root.findByProps({ 'data-quota-model-comparison': 'true' })).toBeTruthy();
    expect(renderer.root.findAllByProps({ 'data-quota-model-warning': 'true' })).toHaveLength(0);
    expect(readText(renderer.root)).toContain('accounts.detail_window_stats_empty');
    expect(readText(renderer.root)).toContain('accounts.detail_forecast_basis_previous');
  });

  it('merges missing model scope into the single alert while keeping stale evidence separate', () => {
    const renderer = renderCard(
      makeWindow({
        stale: true,
        modelScope: { kind: 'models', models: [], complete: false },
        usage: null,
        currentUsage: null,
        previousUsage: null,
        forecast: null,
      }),
      'model'
    );

    const warning = renderer.root.findByProps({ 'data-quota-model-warning': 'true' });
    const sourceWarnings = renderer.root.findByProps({ 'data-quota-source-warnings': 'true' });
    expect(warning.props.role).toBe('alert');
    expect(readText(warning)).toContain('accounts.detail_scope_unknown');
    expect(readText(sourceWarnings)).toContain('accounts.detail_quota_snapshot_stale');
    expect(readText(sourceWarnings)).not.toContain('accounts.detail_scope_unknown');
    expect(renderer.root.findAllByProps({ role: 'alert' })).toHaveLength(1);
  });

  it('explains unknown window boundaries without rendering interval usage', () => {
    const renderer = renderCard(
      makeWindow({
        windowMode: 'unknown',
        usage: null,
        currentUsage: null,
        previousUsage: null,
        previousPeriod: null,
        forecast: null,
      })
    );
    const text = readText(renderer.root);
    expect(text).toContain('accounts.detail_window_boundary_incomplete');
    expect(text).not.toContain('accounts.detail_window_stats_empty');
    expect(text).not.toContain('accounts.detail_window_requests');
    expect(text).not.toContain('accounts.detail_current_window');
  });

  it('explains when provider model scope is not queryable', () => {
    const renderer = renderCard(
      makeWindow({
        modelScope: { kind: 'feature', key: 'future_feature', complete: false },
        usage: null,
        currentUsage: null,
        previousUsage: null,
        forecast: null,
      })
    );
    expect(readText(renderer.root)).toContain('accounts.detail_scope_unknown');
  });

  it('fails closed for an incomplete all scope instead of rendering account-wide comparison stats', () => {
    const renderer = renderCard(
      makeWindow({
        modelScope: { kind: 'all', complete: false },
        usage: usage({ totalRequests: 250, totalTokens: 29_000_000, totalCost: 21.23 }),
        currentUsage: usage({ totalRequests: 250, totalTokens: 29_000_000, totalCost: 21.23 }),
        previousUsage: usage({ totalRequests: 250, totalTokens: 29_000_000, totalCost: 21.23 }),
        forecast: { requests: 250, tokens: 29_000_000, cost: 21.23, basis: 'previous' },
      })
    );

    expect(renderer.root.findAllByProps({ 'data-quota-card-mode': 'model' })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-quota-model-comparison': 'true' })).toHaveLength(0);
    expect(readText(renderer.root)).toContain('accounts.detail_scope_unknown');
  });

  it('renders a header icon keyed by window kind', () => {
    const fiveHour = renderCard(makeWindow({ kind: 'five_hour' }));
    expect(fiveHour.root.findByProps({ 'data-quota-window-icon': 'five_hour' })).toBeTruthy();

    const weekly = renderCard(makeWindow({ kind: 'weekly' }));
    expect(weekly.root.findByProps({ 'data-quota-window-icon': 'weekly' })).toBeTruthy();
  });

  it('keeps comparison details and source metadata visible by default', () => {
    const renderer = renderCard(makeWindow());
    expect(renderer.root.findByProps({ 'data-quota-standard-comparison': 'true' })).toBeTruthy();
    expect(renderer.root.findAllByProps({ 'data-quota-extra-toggle': 'true' })).toHaveLength(0);
    expect(renderer.root.findAllByProps({ 'data-quota-source-warnings': 'true' })).toHaveLength(0);
    expect(readText(renderer.root)).toContain('accounts.detail_quota_latest_observation_time');
  });

  it('hides interval usage and folds amount-only windows into the compact shape', () => {
    const renderer = renderCard(
      makeWindow({
        kind: 'billing',
        windowMode: 'non_window',
        amountLabel: '剩余 $140.00 / $1000.00',
        usage: null,
        currentUsage: null,
        previousUsage: null,
        previousPeriod: null,
        forecast: null,
      })
    );
    const text = readText(renderer.root);
    expect(text).toContain('剩余 $140.00 / $1000.00');
    expect(renderer.root.findAllByProps({ 'data-quota-usage-period': 'current' })).toHaveLength(0);
    expect(renderer.root.findAllByProps({ 'data-quota-extra-toggle': 'true' })).toHaveLength(0);
    expect(renderer.root.findByProps({ 'data-quota-window-icon': 'billing' })).toBeTruthy();
  });

  it('groups source-meta warnings into a dedicated region', () => {
    const renderer = renderCard(
      makeWindow({
        stale: true,
        modelScope: { kind: 'models', models: [], complete: false },
      })
    );
    const warnings = renderer.root.findByProps({ 'data-quota-source-warnings': 'true' });
    const text = readText(warnings);
    expect(text).toContain('accounts.detail_quota_snapshot_stale');
    expect(text).not.toContain('accounts.detail_scope_unknown');
    expect(readText(renderer.root.findByProps({ 'data-quota-model-warning': 'true' }))).toContain(
      'accounts.detail_scope_unknown'
    );
  });
});
