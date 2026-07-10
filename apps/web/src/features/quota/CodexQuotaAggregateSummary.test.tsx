import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import { CodexQuotaAggregateSummary } from './CodexQuotaAggregateSummary';

vi.mock('react-i18next', () => ({
  initReactI18next: { type: '3rdParty', init: () => {} },
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) =>
      key === 'codex_quota.aggregate_coverage'
        ? `${options?.estimated}/${options?.total}`
        : key,
  }),
}));

describe('CodexQuotaAggregateSummary', () => {
  (
    globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
  ).IS_REACT_ACT_ENVIRONMENT = true;

  const readText = (node: ReactTestInstance): string =>
    node.children
      .map((child) => (typeof child === 'string' ? child : readText(child)))
      .join('');

  it('renders separate 5-hour and weekly value/token totals', () => {
    expect(typeof CodexQuotaAggregateSummary).toBe('function');

    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(
        <CodexQuotaAggregateSummary
          summary={{
            fiveHour: {
              remainingTokens: 40_000,
              remainingCost: 2.5,
              estimatedCredentialCount: 2,
              eligibleCredentialCount: 3,
            },
            weekly: {
              remainingTokens: 100_000,
              remainingCost: 10,
              estimatedCredentialCount: 2,
              eligibleCredentialCount: 3,
            },
          }}
        />
      );
    });

    const fiveHour = renderer.root.findByProps({ 'data-codex-aggregate-window': 'five-hour' });
    const weekly = renderer.root.findByProps({ 'data-codex-aggregate-window': 'weekly' });

    expect(readText(fiveHour)).toContain('~$2.50');
    expect(readText(fiveHour)).toContain('~40.0K');
    expect(readText(fiveHour)).toContain('2/3');
    expect(readText(weekly)).toContain('~$10.00');
    expect(readText(weekly)).toContain('~100.0K');
  });
});
