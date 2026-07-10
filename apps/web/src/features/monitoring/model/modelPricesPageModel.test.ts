import { describe, expect, it } from 'vitest';
import {
  applyCandidatePrice,
  buildPriceFromDraft,
  buildModelPriceRows,
  buildModelPriceSummary,
  buildSyncPriceModelsFromSummary,
  filterModelPriceRows,
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
});
