import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import cardStyles from '@/components/common/MetricSummaryCard.module.scss';
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

    expect(html.match(new RegExp(cardStyles.icon, 'g'))).toHaveLength(6);
    icons.forEach((icon) => expect(html).toContain(`data-summary-icon="${icon}"`));
    expect(html.match(new RegExp(cardStyles.card, 'g'))?.length).toBeGreaterThanOrEqual(6);
  });

  it('renders credential summary icons with status-specific semantics', () => {
    const icons = [
      'credential',
      'available',
      'attention',
      'quota-risk',
      'disabled',
      'unconfirmed',
    ] as const;
    const html = renderToStaticMarkup(
      <div>
        {icons.map((icon) => (
          <SummaryCard
            key={icon}
            label={icon}
            value="1"
            meta="credential"
            icon={icon}
            accent={icon === 'disabled' ? 'neutral' : 'blue'}
          />
        ))}
      </div>
    );

    icons.forEach((icon) => expect(html).toContain(`data-summary-icon="${icon}"`));
    expect(html).toContain(cardStyles.accentNeutral);
  });
});
