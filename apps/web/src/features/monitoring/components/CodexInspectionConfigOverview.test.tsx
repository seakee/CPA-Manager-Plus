import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { CodexInspectionConfigOverview } from './CodexInspectionConfigOverview';

describe('CodexInspectionConfigOverview', () => {
  it('supports the borderless embedded presentation while keeping item actions', () => {
    const markup = renderToStaticMarkup(
      <CodexInspectionConfigOverview
        title="Current configuration"
        editLabel="Edit"
        copyLabel="Copy"
        copiedLabel="Copied"
        items={[{ key: 'threshold', label: 'Threshold', value: '100%', field: 'threshold' }]}
        onEdit={() => undefined}
        embedded
      />
    );

    expect(markup).toContain('configOverviewEmbedded');
    expect(markup).toContain('Current configuration');
    expect(markup).toContain('Threshold');
    expect(markup).toContain('aria-label="Threshold: 100%"');
  });

  it('supports the compact presentation for the inspection status panel', () => {
    const markup = renderToStaticMarkup(
      <CodexInspectionConfigOverview
        title="Inspection config"
        editLabel="Edit"
        copyLabel="Copy"
        copiedLabel="Copied"
        items={[{ key: 'sample', label: 'Sample', value: 'All', field: 'sampleSize' }]}
        onEdit={() => undefined}
        compact
        embedded
      />
    );

    expect(markup).toContain('configOverviewCompact');
    expect(markup).toContain('Sample');
    expect(markup).toContain('All');
  });
});
