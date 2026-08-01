import { describe, expect, it } from 'vitest';
import {
  applyCandidatePrice,
  buildPriceFromDraft,
  buildModelPriceRows,
  buildModelPriceSummary,
  buildSyncPriceModelsFromSummary,
  filterModelPriceRows,
  formatContextThreshold,
  formatServiceTierRule,
  getModelPriceCandidateIdentity,
  groupModelPriceCandidatesBySource,
  resolveContextTierDisplayPrice,
  resolveServiceTierDisplayPrice,
} from './modelPricesPageModel';

const usageSummary = {
  sampled_events: 1,
  total_events: 1,
  truncated: false,
  models: [
    {
      model: 'alias-fast',
      calls: 1,
      requested_calls: 1,
      resolved_calls: 0,
    },
    {
      model: 'gpt-5.5',
      calls: 1,
      requested_calls: 0,
      resolved_calls: 1,
    },
  ],
};

describe('modelPricesPageModel', () => {
  it('builds sync models from requested, resolved, and saved prices', () => {
    expect(
      buildSyncPriceModelsFromSummary(usageSummary, {
        'manual-model': { prompt: 1, completion: 2, cache: 0.5 },
      })
    ).toEqual(['alias-fast', 'gpt-5.5', 'manual-model']);
  });

  it('keeps saved prices usable when the usage summary endpoint is unavailable', () => {
    const prices = {
      'manual-model': { prompt: 1, completion: 2, cache: 0.5 },
    };

    expect(buildSyncPriceModelsFromSummary(null, prices)).toEqual(['manual-model']);
    expect(buildModelPriceRows(null, prices)).toEqual([
      expect.objectContaining({
        model: 'manual-model',
        calls: 0,
        hasPrice: true,
      }),
    ]);
  });

  it('marks missing models with candidates before saved rows', () => {
    const rows = buildModelPriceRows(
      usageSummary,
      {
        'gpt-5.5': { prompt: 1, completion: 2, cache: 0.5 },
      },
      [
        {
          model: 'alias-fast',
          candidates: [
            {
              sourceModelId: 'openai/gpt-5.5',
              score: 0.75,
              reason: 'similar',
              price: { prompt: 1, completion: 2, cache: 0.5 },
            },
          ],
        },
      ]
    );

    expect(rows[0]).toMatchObject({
      model: 'alias-fast',
      hasPrice: false,
      candidateCount: 1,
      requestedCalls: 1,
    });
    expect(rows[1]).toMatchObject({
      model: 'gpt-5.5',
      calls: 1,
      requestedCalls: 0,
      resolvedCalls: 1,
    });
    expect(buildModelPriceSummary(rows)).toMatchObject({
      total: 2,
      saved: 1,
      missing: 1,
      candidates: 1,
    });
    expect(filterModelPriceRows(rows, 'candidates', '')).toHaveLength(1);
  });

  it('applies a candidate under the local model name', () => {
    const next = applyCandidatePrice({}, 'alias-fast', {
      sourceModelId: 'openai/gpt-5.5',
      score: 0.75,
      reason: 'similar',
      price: { prompt: 1, completion: 2, cache: 0.5, source: 'openrouter' },
    });

    expect(next['alias-fast']).toMatchObject({
      prompt: 1,
      completion: 2,
      cache: 0.5,
      source: 'openrouter',
      sourceModelId: 'openai/gpt-5.5',
    });
  });

  it('keeps identical source model IDs distinct and groups candidates by source', () => {
    const candidates = [
      {
        sourceModelId: 'openai/gpt-5.5',
        score: 0.94,
        reason: 'same-model-with-provider-prefix',
        price: { prompt: 1, completion: 2, cache: 0.5, source: 'models.dev' },
      },
      {
        sourceModelId: 'openai/gpt-5.5',
        score: 0.94,
        reason: 'same-model-with-provider-prefix',
        price: { prompt: 1.1, completion: 2.1, cache: 0.6, source: 'openrouter' },
      },
    ];

    expect(getModelPriceCandidateIdentity(candidates[0])).not.toBe(
      getModelPriceCandidateIdentity(candidates[1])
    );
    expect(groupModelPriceCandidatesBySource(candidates)).toEqual([
      { source: 'models.dev', candidates: [candidates[0]] },
      { source: 'openrouter', candidates: [candidates[1]] },
    ]);
  });

  it('marks manually entered prices with a manual source', () => {
    expect(
      buildPriceFromDraft({
        model: 'manual-model',
        prompt: '1',
        completion: '2',
        cache: '',
        cacheRead: '',
        cacheCreation: '',
      })
    ).toMatchObject({
      prompt: 1,
      completion: 2,
      cache: 1,
      promptConfigured: true,
      completionConfigured: true,
      cacheReadConfigured: false,
      cacheCreationConfigured: false,
      source: 'manual',
      contextTiers: [],
      serviceTiers: [],
    });
  });

  it('distinguishes blank cache prices from explicitly configured zero prices', () => {
    expect(
      buildPriceFromDraft({
        model: 'gpt-5.6-sol',
        prompt: '0',
        completion: '0',
        cache: '',
        cacheRead: '0',
        cacheCreation: '0',
      })
    ).toMatchObject({
      prompt: 0,
      completion: 0,
      cacheRead: 0,
      cacheCreation: 0,
      promptConfigured: true,
      completionConfigured: true,
      cacheReadConfigured: true,
      cacheCreationConfigured: true,
    });
  });

  it('formats context tier thresholds compactly', () => {
    expect(formatContextThreshold(32_000)).toBe('32K');
    expect(formatContextThreshold(1_000_000)).toBe('1M');
    expect(formatContextThreshold(12_345)).toBe('12,345');
  });

  it('displays inherited tier rates while preserving explicit zero prices', () => {
    expect(
      resolveContextTierDisplayPrice(
        { prompt: 1, completion: 2, cache: 0.5 },
        {
          thresholdTokens: 32_000,
          prompt: 0,
          completion: 0,
          cache: 0,
          promptConfigured: true,
          completionConfigured: false,
        }
      )
    ).toEqual({ prompt: 0, completion: 2 });
  });

  it('displays Fast Mode aliases and inherited service-tier rates', () => {
    const tier = {
      mode: 'fast',
      serviceTier: 'priority',
      prompt: 12.5,
      completion: 0,
      cache: 0,
      promptConfigured: true,
      completionConfigured: false,
    };
    expect(formatServiceTierRule(tier)).toBe('fast/priority');
    expect(resolveServiceTierDisplayPrice({ prompt: 5, completion: 30, cache: 0.5 }, tier)).toEqual(
      {
        prompt: 12.5,
        completion: 30,
      }
    );
  });
});
