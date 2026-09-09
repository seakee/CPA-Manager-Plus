import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { TFunction } from 'i18next';
import { UsageCoverageWarning } from './UsageCoverageWarning';

const t = ((key: string, options?: Record<string, unknown>) => {
  const translations: Record<string, string> = {
    'monitoring.coverage_warning_current': 'current {{deleted}}',
    'monitoring.coverage_warning_comparison': 'comparison {{deleted}}',
    'monitoring.coverage_warning_rolling': 'rolling {{deleted}}',
    'monitoring.coverage_warning_drilldown': 'drilldown {{deleted}}',
    'monitoring.coverage_warning_auxiliary': 'auxiliary {{deleted}}',
  };
  let value = translations[key] ?? key;
  Object.entries(options ?? {}).forEach(([name, replacement]) => {
    value = value.replace(`{{${name}}}`, String(replacement));
  });
  return value;
}) as TFunction;

describe('UsageCoverageWarning', () => {
  it('identifies current and comparison deletion separately and reports fidelity limits', () => {
    const html = renderToStaticMarkup(
      <UsageCoverageWarning
        t={t}
        coverage={{
          scope: 'time_range',
          mode: 'mixed',
          raw_complete: false,
          core_aggregate_used: true,
          raw_event_count: 4,
          raw_deleted_event_count: 2,
          min_deleted_timestamp_ms: 1,
          max_deleted_timestamp_ms: 2,
          comparison_raw_event_count: 0,
          comparison_raw_deleted_event_count: 3,
          comparison_min_deleted_timestamp_ms: 3,
          comparison_max_deleted_timestamp_ms: 4,
          fidelity_limitations: ['event_details_require_raw_events'],
        }}
      />
    );

    expect(html).toContain('role="status"');
    expect(html).toContain('data-coverage-mode="mixed"');
    expect(html).toContain('current 2');
    expect(html).toContain('comparison 3');
    expect(html).toContain('monitoring.coverage_warning_limited');
  });

  it('renders when only auxiliary requested ranges contain deleted raw events', () => {
    const html = renderToStaticMarkup(
      <UsageCoverageWarning
        t={t}
        coverage={{
          scope: 'requested_ranges',
          mode: 'aggregate_only',
          raw_complete: false,
          core_aggregate_used: true,
          raw_deleted_event_count: 0,
          min_deleted_timestamp_ms: 0,
          max_deleted_timestamp_ms: 0,
          auxiliary_ranges: [
            {
              scope: 'rolling_30m',
              from_ms: 1,
              to_ms: 2,
              raw_deleted_event_count: 2,
            },
            {
              scope: 'drilldown_preview',
              from_ms: 3,
              to_ms: 4,
              raw_deleted_event_count: 1,
            },
          ],
          fidelity_limitations: ['event_details_require_raw_events'],
        }}
      />
    );

    expect(html).toContain('rolling 2');
    expect(html).toContain('drilldown 1');
    expect(html).toContain('monitoring.coverage_warning_limited');
  });

  it('does not render without deleted raw events', () => {
    const html = renderToStaticMarkup(
      <UsageCoverageWarning
        t={t}
        coverage={{
          scope: 'time_range',
          mode: 'raw',
          raw_complete: true,
          core_aggregate_used: false,
          raw_event_count: 4,
          raw_deleted_event_count: 0,
          min_deleted_timestamp_ms: 0,
          max_deleted_timestamp_ms: 0,
          fidelity_limitations: [],
        }}
      />
    );

    expect(html).toBe('');
  });
});
