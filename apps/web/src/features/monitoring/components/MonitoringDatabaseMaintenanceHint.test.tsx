import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import { MonitoringDatabaseMaintenanceHint } from './MonitoringDatabaseMaintenanceHint';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

describe('MonitoringDatabaseMaintenanceHint', () => {
  it('warns before a degraded Request Monitoring query is run', () => {
    const markup = renderToStaticMarkup(
      <MonitoringDatabaseMaintenanceHint performanceDegraded longRange={false} />
    );

    expect(markup).toContain('monitoring.database_maintenance_hint_title');
    expect(markup).toContain('monitoring.database_maintenance_hint_body');
    expect(markup).not.toContain('monitoring.database_maintenance_long_range_hint');
  });

  it('adds a soft warning for long time ranges without blocking the query', () => {
    const markup = renderToStaticMarkup(
      <MonitoringDatabaseMaintenanceHint performanceDegraded longRange />
    );

    expect(markup).toContain('monitoring.database_maintenance_long_range_hint');
    expect(markup).not.toContain('disabled');
  });

  it('stays hidden while database maintenance is clean', () => {
    expect(
      renderToStaticMarkup(
        <MonitoringDatabaseMaintenanceHint performanceDegraded={false} longRange />
      )
    ).toBe('');
  });
});
