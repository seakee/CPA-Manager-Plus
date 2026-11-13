import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import { UsageMetricsCard } from './UsageMetricsCard';
import { SummaryCard } from '@/features/monitoring/components/MonitoringShared';
import { UsageSummaryGrid } from '@/features/usage-analytics/components/UsageSummaryCards';

vi.mock('react-i18next', () => ({
  initReactI18next: { type: '3rdParty', init: () => undefined },
  useTranslation: () => ({
    t: (key: string) => key,
    i18n: { language: 'en' },
  }),
}));

const countStretchedWaveSvgs = (renderer: ReactTestRenderer): number =>
  renderer.root.findAll(
    (node) => node.type === 'svg' && node.props.preserveAspectRatio === 'none'
  ).length;

const readText = (renderer: ReactTestRenderer): string =>
  renderer.root
    .findAll((node) => typeof node.children?.[0] === 'string')
    .flatMap((node) => node.children)
    .filter((child): child is string => typeof child === 'string')
    .join(' ');

describe('Summary card decorative wave removal (Phase 0)', () => {
  it('renders UsageMetricsCard metrics and header icons without stretched decorative wave SVGs', async () => {
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <UsageMetricsCard
          mode="metrics-only"
          loading={false}
          lastRefreshedAt={null}
          topModels={[]}
          modelCostRank={[]}
          summary={{
            generated_at_ms: 1763000000000,
            window: {
              today_start_ms: 1762992000000,
              now_ms: 1763000000000,
              rolling_30m_start_ms: 1762998200000,
            },
            today: {
              total_calls: 128,
              success_calls: 125,
              failure_calls: 3,
              success_rate: 0.9765,
              zero_token_calls: 1,
              total_tokens: 48000,
              input_tokens: 32000,
              output_tokens: 12000,
              reasoning_tokens: 1000,
              cached_tokens: 3000,
              cache_read_tokens: 3000,
              cache_creation_tokens: 0,
              total_cost: 1.42,
              average_latency_ms: 420,
            },
            rolling_30m: {
              total_calls: 30,
              total_tokens: 9000,
              rpm: 1,
              tpm: 300,
            },
            top_models_today: [],
            model_cost_rank: [],
            traffic_timeline: [],
            token_mix: [{ key: 'input', tokens: 32000, share: 0.66 }],
            channel_health: [],
            recent_failures: [],
          }}
        />
      );
    });

    expect(countStretchedWaveSvgs(renderer)).toBe(0);
    expect(readText(renderer)).toContain('128');
    expect(readText(renderer)).toContain('97.65%');
  });

  it('renders MonitoringShared SummaryCard and UsageSummaryGrid (default & compact) without stretched decorative wave SVGs', async () => {
    let summaryRenderer!: ReactTestRenderer;
    let defaultGridRenderer!: ReactTestRenderer;
    let compactGridRenderer!: ReactTestRenderer;

    await act(async () => {
      summaryRenderer = create(
        <SummaryCard
          label="Total Calls"
          value="512"
          meta="99.2% success"
          accent="blue"
          icon="calls"
          tone="good"
        />
      );
      const cards = [
        {
          key: 'requests',
          label: 'Requests',
          value: '1.2K',
          valueTitle: '1,234',
          meta: 'Prev 1.0K',
          accent: 'blue' as const,
          icon: 'calls' as const,
          tone: 'good' as const,
          variant: 'primary' as const,
        },
      ];
      defaultGridRenderer = create(<UsageSummaryGrid cards={cards} density="default" />);
      compactGridRenderer = create(<UsageSummaryGrid cards={cards} density="compact" />);
    });

    expect(countStretchedWaveSvgs(summaryRenderer)).toBe(0);
    expect(countStretchedWaveSvgs(defaultGridRenderer)).toBe(0);
    expect(countStretchedWaveSvgs(compactGridRenderer)).toBe(0);
    expect(readText(summaryRenderer)).toContain('512');
    expect(readText(defaultGridRenderer)).toContain('1.2K');
    expect(
      defaultGridRenderer.root.findAllByProps({ 'data-usage-summary-meta': 'true' })
    ).toHaveLength(1);
    expect(
      compactGridRenderer.root.findAllByProps({ 'data-usage-summary-meta': 'true' })
    ).toHaveLength(0);
  });
});
