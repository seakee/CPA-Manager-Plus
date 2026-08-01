import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { SummaryCard } from './MonitoringShared';

describe('SummaryCard', () => {
  it('renders credential inspection icons through the shared card', () => {
    const icons = ['probe', 'sampled', 'delete', 'disable', 'enable', 'reauth'] as const;
    const html = renderToStaticMarkup(
      <div>
        {icons.map((icon) => (
          <SummaryCard
            key={icon}
            label={icon}
            value="1"
            meta="inspection"
            icon={icon}
            accent="blue"
          />
        ))}
      </div>
    );

    expect(html.match(/summaryIcon/g)).toHaveLength(6);
    icons.forEach((icon) => expect(html).toContain(`>${icon}</span>`));
    expect(html.match(/summaryCard/g)?.length).toBeGreaterThanOrEqual(6);
  });
});
