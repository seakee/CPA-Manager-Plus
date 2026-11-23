import type { ReactNode } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import { UsageMetricsCard } from './UsageMetricsCard';

vi.mock('react-router-dom', () => ({
  Link: ({ to, children, ...rest }: { to: string; children?: ReactNode; [k: string]: unknown }) => (
    <a href={to} {...rest}>
      {children}
    </a>
  ),
}));

vi.mock('react-i18next', () => ({
  initReactI18next: { type: '3rdParty', init: () => undefined },
  useTranslation: () => ({
    t: (key: string) => key,
    i18n: { language: 'en' },
  }),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe('UsageMetricsCard', () => {
  it('displays each model cost share of total today cost while preserving bar width share', async () => {
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <UsageMetricsCard
          mode="rank-only"
          loading={false}
          error=""
          lastRefreshedAt={null}
          topModels={[]}
          summary={{
            generated_at_ms: 1000,
            top_models_today: [],
            recent_failures: [],
            window: {
              today_start_ms: 0,
              now_ms: 1000,
              rolling_30m_start_ms: 500,
            },
            today: {
              total_calls: 72,
              success_calls: 66,
              failure_calls: 6,
              success_rate: 66 / 72,
              zero_token_calls: 0,
              input_tokens: 400000,
              output_tokens: 80000,
              reasoning_tokens: 15000,
              cached_tokens: 247000,
              cache_read_tokens: 0,
              cache_creation_tokens: 0,
              total_tokens: 767400,
              total_cost: 1.76,
              average_latency_ms: 1300,
            },
            rolling_30m: {
              total_calls: 0,
              total_tokens: 0,
              rpm: 0,
              tpm: 0,
            },
          }}
          modelCostRank={[
            {
              model: 'claude-sonnet-4-20250514',
              calls: 30,
              tokens: 300000,
              cost: 0.82,
              success_rate: 0.8,
              cost_share: 1,
            },
            {
              model: 'gpt-5.2-codex',
              calls: 20,
              tokens: 250000,
              cost: 0.55,
              success_rate: 1,
              cost_share: 0.6707,
            },
          ]}
        />
      );
    });

    const json = JSON.stringify(renderer.toJSON());
    expect(json).toContain('46.6%');
    expect(json).toContain('31.3%');
    expect(json).not.toContain('100.0%');
    expect(json).toContain('"--share":1');

    await act(async () => {
      renderer.unmount();
    });
  });
});
