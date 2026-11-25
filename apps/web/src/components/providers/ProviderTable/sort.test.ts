import { describe, expect, it } from 'vitest';
import type { OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import type { ProviderRecentUsageMap } from '../utils';
import { buildProviderRows } from './rowData';
import { filterAndSortProviderRows } from './sort';

const emptyInput = {
  gemini: [],
  codex: [],
  claude: [],
  vertex: [],
  openai: [],
  usageByProvider: new Map() as ProviderRecentUsageMap,
};

const codexConfigs: ProviderKeyConfig[] = [
  { apiKey: 'first', baseUrl: 'https://first.example.com/v1', priority: 3 },
  { apiKey: 'unset', baseUrl: 'https://unset.example.com/v1' },
  { apiKey: 'highest', baseUrl: 'https://highest.example.com/v1', priority: 10 },
  { apiKey: 'also-highest', baseUrl: 'https://also-highest.example.com/v1', priority: 10 },
  { apiKey: 'lowest', baseUrl: 'https://lowest.example.com/v1', priority: -1 },
  {
    apiKey: 'disabled-highest',
    baseUrl: 'https://disabled-highest.example.com/v1',
    priority: 99,
    excludedModels: ['*'],
  },
  {
    apiKey: 'disabled-unset',
    baseUrl: 'https://disabled-unset.example.com/v1',
    excludedModels: ['*'],
  },
];

const buildCodexRows = () => buildProviderRows({ ...emptyInput, codex: codexConfigs });

describe('filterAndSortProviderRows', () => {
  it('sorts enabled priorities high to low by default, disabled rows last', () => {
    const result = filterAndSortProviderRows(buildCodexRows());
    expect(result.map((row) => row.originalIndex)).toEqual([2, 3, 0, 1, 4, 5, 6]);
  });

  it('sorts enabled priorities low to high when requested', () => {
    const result = filterAndSortProviderRows(buildCodexRows(), { sortDirection: 'asc' });
    // 停用组同样按规则排序：disabled-unset(0) 在 disabled-highest(99) 之前
    expect(result.map((row) => row.originalIndex)).toEqual([4, 1, 0, 2, 3, 6, 5]);
  });

  it('preserves source order for equal effective priorities', () => {
    const rows = buildProviderRows({
      ...emptyInput,
      codex: [
        { apiKey: 'a', priority: 2 },
        { apiKey: 'b', priority: 2 },
        { apiKey: 'c' },
        { apiKey: 'd' },
      ],
    });

    expect(filterAndSortProviderRows(rows).map((row) => row.raw.apiKey)).toEqual([
      'a',
      'b',
      'c',
      'd',
    ]);
  });

  it('filters by provider kind', () => {
    const rows = buildProviderRows({
      ...emptyInput,
      codex: [{ apiKey: 'codex-key', baseUrl: 'https://codex.example.com/v1' }],
      gemini: [{ apiKey: 'gemini-key', baseUrl: 'https://gemini.example.com/v1' }],
    });

    const result = filterAndSortProviderRows(rows, { kind: 'codex' });
    expect(result).toHaveLength(1);
    expect(result[0].kind).toBe('codex');
  });

  it('filters by search text against base url, name and raw key', () => {
    const openai: OpenAIProviderConfig[] = [
      {
        name: 'qwen2api',
        baseUrl: 'https://qwen.example.com/v1',
        apiKeyEntries: [{ apiKey: 'sk-qwen-entry' }],
      },
    ];
    const rows = buildProviderRows({ ...emptyInput, codex: codexConfigs, openai });

    expect(
      filterAndSortProviderRows(rows, { searchText: 'disabled-highest' }).map(
        (row) => row.originalIndex
      )
    ).toEqual([5]);

    expect(filterAndSortProviderRows(rows, { searchText: 'QWEN2API' })).toHaveLength(1);
    expect(filterAndSortProviderRows(rows, { searchText: 'sk-qwen-entry' })).toHaveLength(1);
    expect(filterAndSortProviderRows(rows, { searchText: 'no-such-thing' })).toHaveLength(0);
  });

  it('filters by selected models without dropping disabled matches', () => {
    const rows = buildProviderRows({
      ...emptyInput,
      codex: [
        {
          apiKey: 'alpha-key',
          baseUrl: 'https://alpha.example.com/v1',
          priority: 1,
          models: [{ name: 'alpha-model' }],
        },
        {
          apiKey: 'disabled-key',
          baseUrl: 'https://disabled.example.com/v1',
          priority: 99,
          excludedModels: ['*'],
          models: [{ name: 'beta-model' }],
        },
        {
          apiKey: 'beta-key',
          baseUrl: 'https://beta.example.com/v1',
          priority: 9,
          models: [{ name: 'beta-model' }],
        },
      ],
    });

    const result = filterAndSortProviderRows(rows, {
      selectedModels: new Set(['beta-model']),
    });

    expect(result.map((row) => row.originalIndex)).toEqual([2, 1]);
  });

  it('keeps partial and unavailable statistics behind complete rows for recent-success sorting', () => {
    const rows = buildProviderRows({
      ...emptyInput,
      openai: [
        {
          name: 'full-high',
          baseUrl: 'https://full-high.example/v1',
          apiKeyEntries: [{ apiKey: 'a' }],
        },
        {
          name: 'full-low',
          baseUrl: 'https://full-low.example/v1',
          apiKeyEntries: [{ apiKey: 'b' }],
        },
        {
          name: 'partial',
          baseUrl: 'https://partial.example/v1',
          apiKeyEntries: [{ apiKey: 'c' }, { apiKey: '', proxyUrl: 'socks5://proxy:1080' }],
        },
        { name: 'unknown', baseUrl: 'https://unknown.example/v1', apiKeyEntries: [] },
      ],
    });
    rows.find((row) => row.label === 'full-high')!.recentSuccess = 10;
    rows.find((row) => row.label === 'full-low')!.recentSuccess = 1;
    rows.find((row) => row.label === 'partial')!.recentSuccess = 99;
    rows.find((row) => row.label === 'unknown')!.recentSuccess = 0;

    expect(
      filterAndSortProviderRows(rows, { sortOption: 'recent-success', sortDirection: 'desc' }).map(
        (row) => row.label
      )
    ).toEqual(['full-high', 'full-low', 'partial', 'unknown']);
    expect(
      filterAndSortProviderRows(rows, { sortOption: 'recent-success', sortDirection: 'asc' }).map(
        (row) => row.label
      )
    ).toEqual(['full-low', 'full-high', 'partial', 'unknown']);
  });

  it('sorts by name using provider name for openai and identity fallback for key configs', () => {
    const rows = buildProviderRows({
      ...emptyInput,
      openai: [
        { name: 'zeta', baseUrl: 'https://z.example.com/v1', apiKeyEntries: [] },
        { name: 'alpha', baseUrl: 'https://a.example.com/v1', apiKeyEntries: [] },
      ],
    });

    const ascending = filterAndSortProviderRows(rows, {
      sortOption: 'name',
      sortDirection: 'asc',
    });
    expect(ascending.map((row) => row.label)).toEqual(['alpha', 'zeta']);
  });

  it('sorts by configured model count ascending and descending while keeping disabled rows last', () => {
    const rows = buildProviderRows({
      ...emptyInput,
      codex: [
        {
          name: 'two-models',
          apiKey: 'k-two',
          models: [{ name: 'm1' }, { name: 'm2' }],
        },
        {
          name: 'wildcard',
          apiKey: 'k-wild',
        },
        {
          name: 'disabled-many',
          apiKey: 'k-disabled',
          excludedModels: ['*'],
          models: [{ name: 'm1' }, { name: 'm2' }, { name: 'm3' }, { name: 'm4' }],
        },
        {
          name: 'three-models',
          apiKey: 'k-three',
          models: [{ name: 'm1' }, { name: 'm2' }, { name: 'm3' }],
        },
      ],
    });

    expect(
      filterAndSortProviderRows(rows, { sortOption: 'models', sortDirection: 'desc' }).map(
        (row) => row.label
      )
    ).toEqual(['three-models', 'two-models', 'wildcard', 'disabled-many']);

    expect(
      filterAndSortProviderRows(rows, { sortOption: 'models', sortDirection: 'asc' }).map(
        (row) => row.label
      )
    ).toEqual(['wildcard', 'two-models', 'three-models', 'disabled-many']);
  });

  it('filters by statusFilter and healthFilter options', () => {
    const rows = buildProviderRows({
      ...emptyInput,
      codex: [
        { name: 'healthy-active', apiKey: 'k1' },
        { name: 'failing-active', apiKey: 'k2' },
        { name: 'idle-disabled', apiKey: 'k3', excludedModels: ['*'] },
      ],
    });
    rows[0].stats = { success: 10, failure: 0 };
    rows[1].stats = { success: 4, failure: 1 };
    rows[1].recentFailure = 1;

    expect(
      filterAndSortProviderRows(rows, { statusFilter: 'disabled' }).map((row) => row.label)
    ).toEqual(['idle-disabled']);

    expect(
      filterAndSortProviderRows(rows, { healthFilter: 'has_failure' }).map((row) => row.label)
    ).toEqual(['failing-active']);

    expect(
      filterAndSortProviderRows(rows, { healthFilter: 'active' }).map((row) => row.label)
    ).toEqual(['healthy-active', 'failing-active']);

    expect(
      filterAndSortProviderRows(rows, { healthFilter: 'idle' }).map((row) => row.label)
    ).toEqual(['idle-disabled']);
  });

  it('ignores historical failures in has_failure filter once recent window is clean, and matches model aliases', () => {
    const rows = buildProviderRows({
      ...emptyInput,
      claude: [
        {
          name: 'recovered-channel',
          apiKey: 'k-rec',
          models: [{ name: 'claude-sonnet-4-5', alias: 'sonnet' }],
        },
        {
          name: 'still-failing',
          apiKey: 'k-fail',
          models: [{ name: 'claude-opus-4-6', alias: 'opus' }],
        },
        {
          name: 'legacy-failure-no-window',
          apiKey: 'k-legacy',
        },
      ],
    });
    rows[0].stats = { success: 49, failure: 1 };
    rows[0].recentSuccess = 10;
    rows[0].recentFailure = 0;

    rows[1].stats = { success: 8, failure: 2 };
    rows[1].recentSuccess = 8;
    rows[1].recentFailure = 2;

    rows[2].stats = { success: 0, failure: 1 };
    rows[2].recentSuccess = 0;
    rows[2].recentFailure = 0;

    expect(
      filterAndSortProviderRows(rows, { healthFilter: 'has_failure' }).map((row) => row.label)
    ).toEqual(['still-failing', 'legacy-failure-no-window']);

    expect(
      filterAndSortProviderRows(rows, { selectedModels: new Set(['sonnet']) }).map(
        (row) => row.label
      )
    ).toEqual(['recovered-channel']);
  });
});
