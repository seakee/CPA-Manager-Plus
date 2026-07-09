import { describe, expect, it } from 'vitest';
import type { AuthFileItem, CodexQuotaState } from '@/types';
import type { MonitoringAnalyticsCredentialStatRow } from '@/services/api/usageService';
import {
  buildAuthFileUsageSummary,
  buildAuthFileUsageSummaryMap,
  getAuthFileUsageSummaryKey,
} from './authFileUsageSummary';

const credentialRow = (
  overrides: Partial<MonitoringAnalyticsCredentialStatRow>
): MonitoringAnalyticsCredentialStatRow => ({
  id: overrides.id ?? 'row',
  auth_file_snapshot: overrides.auth_file_snapshot,
  auth_index: overrides.auth_index,
  source: overrides.source,
  source_hash: overrides.source_hash,
  account_snapshot: overrides.account_snapshot,
  auth_label_snapshot: overrides.auth_label_snapshot,
  auth_provider_snapshot: overrides.auth_provider_snapshot,
  auth_project_id_snapshot: overrides.auth_project_id_snapshot,
  calls: overrides.calls ?? 1,
  success_calls: overrides.success_calls ?? 1,
  failure_calls: overrides.failure_calls ?? 0,
  success_rate: overrides.success_rate ?? 1,
  input_tokens: overrides.input_tokens ?? 0,
  output_tokens: overrides.output_tokens ?? 0,
  cached_tokens: overrides.cached_tokens ?? 0,
  cache_read_tokens: overrides.cache_read_tokens ?? 0,
  cache_creation_tokens: overrides.cache_creation_tokens ?? 0,
  total_tokens: overrides.total_tokens ?? 0,
  cost: overrides.cost ?? 0,
  average_latency_ms: overrides.average_latency_ms ?? null,
  last_seen_ms: overrides.last_seen_ms ?? 0,
  models: overrides.models,
});

const codexQuota = (overrides: Partial<CodexQuotaState> = {}): CodexQuotaState => ({
  status: 'success',
  windows: [
    {
      id: 'five-hour',
      label: '5-hour limit',
      usedPercent: 25,
      resetLabel: 'soon',
      limitWindowSeconds: 18_000,
    },
    {
      id: 'weekly',
      label: 'Weekly limit',
      usedPercent: 40,
      resetLabel: 'later',
      limitWindowSeconds: 604_800,
    },
  ],
  ...overrides,
});

describe('auth file usage summary model', () => {
  it('matches usage rows by auth file name and auth index', () => {
    const file: AuthFileItem = { name: 'shared-codex.json', type: 'codex', authIndex: '1' };

    const summary = buildAuthFileUsageSummary(file, {
      retainedRows: [
        credentialRow({
          auth_file_snapshot: 'shared-codex.json',
          auth_index: '0',
          total_tokens: 10_000,
          cost: 3,
        }),
        credentialRow({
          auth_file_snapshot: 'shared-codex.json',
          auth_index: '1',
          total_tokens: 20_000,
          cost: 4.25,
        }),
      ],
      fiveHourRows: [],
      weeklyRows: [],
      codexQuota: codexQuota(),
    });

    expect(summary?.totalTokens).toBe(20_000);
    expect(summary?.estimatedCost).toBe(4.25);
  });

  it('estimates Codex 5-hour and weekly token limits from matching window usage', () => {
    const file: AuthFileItem = { name: 'codex-main.json', type: 'codex', authIndex: '0' };

    const summary = buildAuthFileUsageSummary(file, {
      retainedRows: [
        credentialRow({
          auth_file_snapshot: 'codex-main.json',
          auth_index: '0',
          total_tokens: 50_000,
          cost: 1.5,
        }),
      ],
      fiveHourRows: [
        credentialRow({
          auth_file_snapshot: 'codex-main.json',
          auth_index: '0',
          total_tokens: 5_000,
          cost: 0.25,
        }),
      ],
      weeklyRows: [
        credentialRow({
          auth_file_snapshot: 'codex-main.json',
          auth_index: '0',
          total_tokens: 28_000,
          cost: 2.8,
        }),
      ],
      codexQuota: codexQuota(),
    });

    expect(summary?.codexFiveHourLimitTokens).toBe(20_000);
    expect(summary?.codexFiveHourLimitCost).toBe(1);
    expect(summary?.codexWeeklyLimitTokens).toBe(70_000);
    expect(summary?.codexWeeklyLimitCost).toBe(7);
  });

  it('does not estimate limits when quota percentages are missing or zero', () => {
    const file: AuthFileItem = { name: 'codex-main.json', type: 'codex', authIndex: '0' };

    const summary = buildAuthFileUsageSummary(file, {
      retainedRows: [
        credentialRow({
          auth_file_snapshot: 'codex-main.json',
          auth_index: '0',
          total_tokens: 1_000,
          cost: 0.1,
        }),
      ],
      fiveHourRows: [
        credentialRow({
          auth_file_snapshot: 'codex-main.json',
          auth_index: '0',
          total_tokens: 5_000,
        }),
      ],
      weeklyRows: [
        credentialRow({
          auth_file_snapshot: 'codex-main.json',
          auth_index: '0',
          total_tokens: 28_000,
        }),
      ],
      codexQuota: codexQuota({
        windows: [
          {
            id: 'five-hour',
            label: '5-hour limit',
            usedPercent: 0,
            resetLabel: 'soon',
            limitWindowSeconds: 18_000,
          },
          {
            id: 'weekly',
            label: 'Weekly limit',
            usedPercent: null,
            resetLabel: 'later',
            limitWindowSeconds: 604_800,
          },
        ],
      }),
    });

    expect(summary?.codexFiveHourLimitTokens).toBeNull();
    expect(summary?.codexFiveHourLimitCost).toBeNull();
    expect(summary?.codexWeeklyLimitTokens).toBeNull();
    expect(summary?.codexWeeklyLimitCost).toBeNull();
  });

  it('builds a summary map without leaking same-name auth-indexed files', () => {
    const files: AuthFileItem[] = [
      { name: 'shared-codex.json', type: 'codex', authIndex: '0' },
      { name: 'shared-codex.json', type: 'codex', authIndex: '1' },
    ];

    const map = buildAuthFileUsageSummaryMap(files, {
      retainedRows: [
        credentialRow({
          auth_file_snapshot: 'shared-codex.json',
          auth_index: '0',
          total_tokens: 1_000,
          cost: 0.1,
        }),
        credentialRow({
          auth_file_snapshot: 'shared-codex.json',
          auth_index: '1',
          total_tokens: 2_000,
          cost: 0.2,
        }),
      ],
      fiveHourRows: [],
      weeklyRows: [],
      codexQuotaByKey: new Map(),
    });

    expect(map.get(getAuthFileUsageSummaryKey(files[0]))?.totalTokens).toBe(1_000);
    expect(map.get(getAuthFileUsageSummaryKey(files[1]))?.totalTokens).toBe(2_000);
  });
});
