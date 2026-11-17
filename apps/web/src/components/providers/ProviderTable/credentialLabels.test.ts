import { describe, it, expect } from 'vitest';
import { buildProviderRows, withCredentialLabels } from './rowData';
import { filterAndSortProviderRows } from './sort';
import { sha256Hex } from '@/utils/apiKeyHash';
describe('credential display labels', () => {
  it('distinguishes keys with the same masked suffix and preserves route and row identity', () => {
    const rows = buildProviderRows({
      gemini: [],
      codex: [
        { apiKey: 'sk-account-one-AA', baseUrl: 'https://api.openai.com/v1', websockets: true },
        { apiKey: 'sk-account-two-AA', baseUrl: 'https://api.openai.com/v1' },
      ],
      claude: [],
      vertex: [],
      openai: [],
      usageByProvider: new Map(),
    });
    const labeled = withCredentialLabels(rows, {
      [sha256Hex('sk-account-one-AA')]: 'First@example.com',
      [sha256Hex('sk-account-two-AA')]: 'Second@example.com',
    });
    expect(labeled.map((row) => row.label)).toEqual(['First@example.com', 'Second@example.com']);
    expect(labeled[0].key).toBe(rows[0].key);
    expect(labeled[0].raw).toBe(rows[0].raw);
    expect(labeled[0].maskedKey).toBe(rows[0].label);
    expect(filterAndSortProviderRows(labeled, { searchText: 'SECOND@EXAMPLE.COM' })).toEqual([
      labeled[1],
    ]);
    expect(withCredentialLabels(rows, {})[0].label).toBe(rows[0].label);
  });
});
