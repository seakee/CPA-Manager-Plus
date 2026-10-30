import { describe, expect, it } from 'vitest';
import { sha256Hex } from '@/utils/apiKeyHash';
import {
  buildSourceInfoMap,
  resolveSourceDisplay,
  resolveSourceIdentityKey,
} from '@/utils/sourceResolver';
import { buildMonitoringSourceDisplay } from './sourceDisplay';
import { buildAccountRowsFromAnalytics } from './analyticsAdapters';
import { buildEventRows } from './eventRows';
import { buildRealtimeLogRows } from './monitoringCenterPageModel';
import { buildRealtimeSourceDisplay } from '../realtimeSourceDisplay';
import { buildAccountRows } from './rowBuilders';

const apiKey = 'sk-synthetic-account-a';
const hash = sha256Hex(apiKey);
const config = { codexApiKeys: [{ apiKey, authIndex: '7', baseUrl: 'https://api.openai.com/v1' }] };
const names = { [hash]: 'account@example.com' };
const empty = new Map();
const namedMap = () => buildSourceInfoMap({ ...config, credentialLabels: names });
const context = () => ({
  sourceInfoMap: namedMap(),
  authMetaMap: empty,
  authFileMap: empty,
  channelByAuthIndex: empty,
});

describe('upstream credential remarks across monitoring', () => {
  it('shows full explicit remark ahead of generic provider/host without changing identity', () => {
    const before = buildSourceInfoMap(config),
      after = namedMap();
    for (const source of [apiKey, `h:${hash}`]) {
      expect(resolveSourceIdentityKey(source, '7', after, empty)).toBe(
        resolveSourceIdentityKey(source, '7', before, empty)
      );
      const display = buildMonitoringSourceDisplay(
        { source, authIndex: '7', channel: 'Codex', authLabelSnapshot: 'generic upstream' },
        context()
      );
      expect(display.primary).toBe('account@example.com');
      expect(display.title).not.toContain(apiKey);
    }
  });
  it('keeps a removed credential remark readable from an exact historical source hash', () => {
    const map = buildSourceInfoMap({ credentialLabels: names });
    expect(resolveSourceDisplay(`h:${hash}`, '', map, empty).displayName).toBe(
      'account@example.com'
    );
    expect(resolveSourceIdentityKey(`h:${hash}`, '', map, empty)).toBe(`source:h:${hash}`);
  });
  it('does not merge separate keys that have the same remark', () => {
    const other = 'sk-synthetic-account-b';
    const map = buildSourceInfoMap({
      codexApiKeys: [
        ...config.codexApiKeys,
        { apiKey: other, authIndex: '8', baseUrl: 'https://api.openai.com/v1' },
      ],
      credentialLabels: { ...names, [sha256Hex(other)]: names[hash] },
    });
    expect(resolveSourceIdentityKey(apiKey, '7', map, empty)).not.toBe(
      resolveSourceIdentityKey(other, '8', map, empty)
    );
  });
  it('updates analytics account text while preserving grouping, totals and filter identity', () => {
    const data = [
      {
        id: 'stable-account-id',
        sources: [`h:${hash}`],
        auth_indices: ['7'],
        account_snapshot: 'original',
        auth_provider_snapshot: 'codex',
        total_requests: 5,
        total_tokens: 1234,
        total_cost: 0.25,
      },
    ];
    const before = buildAccountRowsFromAnalytics(
      data as never,
      empty,
      empty,
      buildSourceInfoMap(config),
      empty
    );
    const after = buildAccountRowsFromAnalytics(data as never, empty, empty, namedMap(), empty);
    expect(after[0].displayAccount).toBe('account@example.com');
    expect(after[0].id).toBe(before[0].id);
    expect(after[0].filterValue).toBe(before[0].filterValue);
    expect(after[0].totalTokens).toBe(before[0].totalTokens);
    expect(after[0].totalCost).toBe(before[0].totalCost);
  });
  it('keeps realtime rows and client-side account totals stable on rename', () => {
    const details = [
      {
        timestamp: '2026-10-30T00:00:00Z',
        source: `h:${hash}`,
        auth_index: '7',
        provider: 'codex',
        tokens: { input_tokens: 100, output_tokens: 20 },
        __modelName: 'test-model',
        __endpoint: '/v1/responses',
      },
    ];
    const rows = (map: ReturnType<typeof buildSourceInfoMap>) =>
      buildEventRows(details as never, empty, empty, map, empty, {}, empty);
    const before = rows(buildSourceInfoMap(config)),
      after = rows(namedMap());
    expect(after[0].sourceMasked).toBe('account@example.com');
    expect(after[0].id).toBe(before[0].id);
    expect(buildRealtimeLogRows(after)[0].streamKey).toBe(buildRealtimeLogRows(before)[0].streamKey);
    expect(buildRealtimeSourceDisplay(after[0], ((key: string) => key) as never).primary).toBe('account@example.com');
    const oldAccounts = buildAccountRows(before),
      newAccounts = buildAccountRows(after);
    expect(newAccounts[0].displayAccount).toBe('account@example.com');
    expect(newAccounts[0].id).toBe(oldAccounts[0].id);
    expect(newAccounts[0].totalTokens).toBe(oldAccounts[0].totalTokens);
  });
});
