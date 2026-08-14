import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  buildCandidateUsageSourceIds,
  calculateCacheHitRate,
  calculateCacheHitRateFromTotals,
  calculateCost,
  collectUsageDetails,
  collectUsageDetailsWithEndpoint,
  compatibleCachedTokens,
  extractTotalTokens,
  formatCompactNumber,
  formatUsd,
  getServiceTierMultiplier,
  inferCacheInputMode,
  loadModelPrices,
  normalizeAnalyticsModel,
  normalizeCacheAccounting,
  normalizeUsageSourceId,
  resolveTokenAccounting,
} from './usage';
import { maskSensitiveText } from './format';
import cacheInputAccountingFixtures from './cacheInputAccounting.fixtures.json';

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('formatCompactNumber', () => {
  it('keeps large values compact as data grows beyond millions', () => {
    expect(formatCompactNumber(999)).toBe('999');
    expect(formatCompactNumber(1_200)).toBe('1.2K');
    expect(formatCompactNumber(999_950)).toBe('1.0M');
    expect(formatCompactNumber(2_795_200_000)).toBe('2.8B');
    expect(formatCompactNumber(1_200_000_000_000)).toBe('1.2T');
    expect(formatCompactNumber(-2_500_000_000_000_000)).toBe('-2.5P');
    expect(formatCompactNumber(Number.POSITIVE_INFINITY)).toBe('0');
  });
});

describe('formatUsd', () => {
  it('formats costs globally to two decimal places', () => {
    expect(formatUsd(19.99)).toBe('$19.99');
    expect(formatUsd(0.006)).toBe('$0.01');
    expect(formatUsd(Number.NaN)).toBe('$0.00');
  });

  it('allows request-scoped precision overrides', () => {
    expect(formatUsd(19.99, 3)).toBe('$19.990');
    expect(formatUsd(0.0006, 3)).toBe('$0.001');
    expect(formatUsd(Number.NaN, 3)).toBe('$0.000');
  });
});

describe('usage source candidates', () => {
  it('includes the masked source emitted by CPA for raw upstream keys', () => {
    expect(buildCandidateUsageSourceIds({ apiKey: 'sk-1234567890abcdef' })).toContain(
      'm:sk-1...cdef'
    );
  });

  it('aligns short secret masking with the backend source contract', () => {
    expect(buildCandidateUsageSourceIds({ apiKey: 'sk-12345' })).toContain('m:****');
  });

  it('preserves already-normalized masked usage event sources', () => {
    const usageData = {
      apis: {
        'POST /v1/responses': {
          models: {
            'gpt-5.5': {
              details: [
                {
                  timestamp: '2026-05-26T10:00:00Z',
                  source: 'm:sk-1...cdef',
                  auth_index: '',
                  tokens: {},
                  failed: false,
                },
              ],
            },
          },
        },
      },
    };

    expect(collectUsageDetails(usageData)[0].source).toBe('m:sk-1...cdef');
  });

  it('does not trust text-prefixed raw API key sources', () => {
    const sourceId = buildCandidateUsageSourceIds({ prefix: 'codex' })[0];
    expect(sourceId).toBe('t:codex');

    const usageData = {
      apis: {
        'POST /v1/responses': {
          models: {
            'gpt-5.5': {
              details: [
                {
                  timestamp: '2026-05-26T10:00:00Z',
                  source: 't:sk-1234567890abcdef',
                  auth_index: '',
                  tokens: {},
                  failed: false,
                },
              ],
            },
          },
        },
      },
    };

    const normalized = collectUsageDetails(usageData)[0].source;
    expect(normalized).toMatch(/^k:/);
    expect(normalized).not.toContain('sk-1234567890abcdef');
  });

  it('does not trust abnormal masked sources that contain raw secrets', () => {
    const normalized = normalizeUsageSourceId('m:sk-realsecret');

    expect(normalized).toMatch(/^k:/);
    expect(normalized).not.toContain('sk-realsecret');
  });

  it('preserves legacy UI-masked source IDs when no raw secret is present', () => {
    expect(normalizeUsageSourceId('m:sk******ef')).toBe('m:sk******ef');
  });
});

describe('normalizeAnalyticsModel', () => {
  it('removes only supported CPA reasoning suffixes', () => {
    expect(normalizeAnalyticsModel('deepseek-v4-flash(max)')).toBe('deepseek-v4-flash');
    expect(normalizeAnalyticsModel('gemini-2.5-pro(+08192)')).toBe('gemini-2.5-pro');
    expect(normalizeAnalyticsModel('gemini-2.5-pro(-000)')).toBe('gemini-2.5-pro');
    expect(normalizeAnalyticsModel('custom(model)(HIGH)')).toBe('custom(model)');
    expect(normalizeAnalyticsModel('custom-model(region-us)')).toBe('custom-model(region-us)');
    expect(normalizeAnalyticsModel('custom-model(9223372036854775808)')).toBe(
      'custom-model(9223372036854775808)'
    );
    expect(normalizeAnalyticsModel(' custom-model(max) ')).toBe(' custom-model(max) ');
  });
});

describe('usage detail collection', () => {
  it('preserves Codex identity from legacy auth type metadata', () => {
    const usageData = {
      apis: {
        'POST /v1/responses': {
          models: {
            'gpt-5.4': {
              details: [
                {
                  timestamp: '2026-07-20T00:00:00Z',
                  source: 'codex-account',
                  auth_index: 'auth-1',
                  auth_type: 'codex',
                  request_service_tier: 'priority',
                  response_service_tier: 'default',
                  tokens: { input_tokens: 100_000 },
                  failed: false,
                },
              ],
            },
          },
        },
      },
    };

    for (const detail of [
      collectUsageDetails(usageData)[0],
      collectUsageDetailsWithEndpoint(usageData)[0],
    ]) {
      expect(detail.auth_type).toBe('codex');
      expect(detail.provider).toBe('codex');
      expect(
        calculateCost(detail, {
          'gpt-5.4': { prompt: 2.5, completion: 5, cache: 1 },
        })
      ).toBeCloseTo(0.5);
    }
  });

  it('copies project id snapshots into normalized usage details', () => {
    const usageData = {
      apis: {
        'POST /v1/chat/completions': {
          models: {
            'gemini-2.5-pro': {
              details: [
                {
                  timestamp: '2026-05-09T01:12:43.000Z',
                  source: 'alice@example.com',
                  auth_index: 'auth-1',
                  auth_project_id_snapshot: 'vertex-project-42',
                  tokens: {
                    input_tokens: 10,
                    output_tokens: 5,
                  },
                  failed: false,
                },
              ],
            },
          },
        },
      },
    };

    expect(collectUsageDetails(usageData)[0].auth_project_id_snapshot).toBe('vertex-project-42');
    expect(collectUsageDetailsWithEndpoint(usageData)[0].auth_project_id_snapshot).toBe(
      'vertex-project-42'
    );
  });

  it('accepts camelCase project id snapshots from usage details', () => {
    const usageData = {
      apis: {
        'POST /v1/chat/completions': {
          models: {
            'gemini-2.5-pro': {
              details: [
                {
                  timestamp: '2026-05-09T01:12:43.000Z',
                  source: 'alice@example.com',
                  authIndex: 'auth-1',
                  authProjectIdSnapshot: 'camel-project-42',
                  tokens: {},
                  failed: false,
                },
              ],
            },
          },
        },
      },
    };

    expect(collectUsageDetails(usageData)[0].auth_project_id_snapshot).toBe('camel-project-42');
    expect(collectUsageDetailsWithEndpoint(usageData)[0].auth_project_id_snapshot).toBe(
      'camel-project-42'
    );
  });

  it('extracts analytics, requested, and resolved model identities', () => {
    const usageData = {
      apis: {
        'POST /v1/chat/completions': {
          models: {
            'gpt-5.4': {
              details: [
                {
                  timestamp: '2026-05-19T10:00:00Z',
                  source: 'alice@example.com',
                  auth_index: 'auth-1',
                  analytics_model: 'gpt-5',
                  requested_model: 'gpt-5.4(max)',
                  resolved_model: 'gpt-5.5',
                  tokens: { input_tokens: 1 },
                  failed: false,
                },
              ],
            },
          },
        },
      },
    };

    const detail = collectUsageDetails(usageData)[0];
    expect(detail.__modelName).toBe('gpt-5.4');
    expect(detail.__requestedModel).toBe('gpt-5.4(max)');
    expect(detail.__resolvedModel).toBe('gpt-5.5');
    expect(collectUsageDetailsWithEndpoint(usageData)[0]).toMatchObject({
      __modelName: 'gpt-5.4',
      __requestedModel: 'gpt-5.4(max)',
      __resolvedModel: 'gpt-5.5',
    });
  });

  it('derives analytics identity for legacy payloads without analytics_model', () => {
    const usageData = {
      apis: {
        'POST /v1/chat/completions': {
          models: {
            'deepseek-v4-flash(max)': {
              details: [
                {
                  timestamp: '2026-05-19T10:00:00Z',
                  tokens: { input_tokens: 1 },
                  failed: false,
                },
              ],
            },
            'custom-model(region-us)': {
              details: [
                {
                  timestamp: '2026-05-19T10:00:01Z',
                  tokens: { input_tokens: 1 },
                  failed: false,
                },
              ],
            },
          },
        },
      },
    };

    expect(collectUsageDetailsWithEndpoint(usageData)).toEqual([
      expect.objectContaining({
        __modelName: 'deepseek-v4-flash',
        __requestedModel: 'deepseek-v4-flash(max)',
      }),
      expect.objectContaining({
        __modelName: 'custom-model(region-us)',
        __requestedModel: 'custom-model(region-us)',
      }),
    ]);
  });

  it('copies TTFT metadata into normalized usage details', () => {
    const usageData = {
      apis: {
        'POST /v1/chat/completions': {
          models: {
            'gpt-5.4': {
              details: [
                {
                  timestamp: '2026-05-19T10:00:00Z',
                  source: 'alice@example.com',
                  auth_index: 'auth-1',
                  latency_ms: 1500,
                  ttft_ms: 450,
                  tokens: { output_tokens: 20 },
                  failed: false,
                },
              ],
            },
          },
        },
      },
    };

    expect(collectUsageDetails(usageData)[0].ttft_ms).toBe(450);
    expect(collectUsageDetailsWithEndpoint(usageData)[0].ttft_ms).toBe(450);
  });

  it('normalizes CPA mirrored cached tokens without double counting fine-grained cache', () => {
    const usageData = {
      apis: {
        'POST /v1/messages': {
          models: {
            'claude-sonnet': {
              details: [
                {
                  timestamp: '2026-05-19T10:00:00Z',
                  source: 'alice@example.com',
                  auth_index: 'auth-1',
                  tokens: {
                    input_tokens: 100,
                    output_tokens: 20,
                    cached_tokens: 500,
                    cache_read_tokens: 500,
                  },
                  failed: false,
                },
              ],
            },
          },
        },
      },
    };

    const detail = collectUsageDetailsWithEndpoint(usageData)[0];

    expect(detail.tokens.cached_tokens).toBe(0);
    expect(detail.tokens.cache_read_tokens).toBe(500);
  });

  it('normalizes Anthropic cache input token fields', () => {
    const usageData = {
      apis: {
        'POST /v1/messages': {
          models: {
            'claude-sonnet': {
              details: [
                {
                  timestamp: '2026-05-19T10:00:00Z',
                  source: 'alice@example.com',
                  auth_index: 'auth-1',
                  tokens: {
                    input_tokens: 100,
                    output_tokens: 20,
                    cached_tokens: 34,
                    cache_creation_input_tokens: 11,
                    cache_read_input_tokens: 23,
                  },
                  failed: false,
                },
              ],
            },
          },
        },
      },
    };

    const detail = collectUsageDetailsWithEndpoint(usageData)[0];

    expect(detail.tokens.cached_tokens).toBe(0);
    expect(detail.tokens.cache_creation_tokens).toBe(11);
    expect(detail.tokens.cache_read_tokens).toBe(23);
    expect(detail.tokens.total_tokens).toBe(154);
  });

  it('preserves explicit legacy accounting provenance through detail collectors', () => {
    const usageData = {
      apis: {
        'POST /v1/responses': {
          models: {
            'gpt-5.4': {
              details: [
                {
                  timestamp: '2026-07-20T00:00:00Z',
                  source: 'codex-account',
                  auth_index: 'auth-1',
                  accounting_version: 0,
                  token_breakdown: {
                    schema_version: 2,
                    quality: 'complete',
                    total_tokens: 140,
                    input: {
                      total_tokens: 100,
                      uncached_tokens: 80,
                      cache_read_tokens: 20,
                      cache_write_tokens: 0,
                    },
                    output: {
                      total_tokens: 40,
                      non_reasoning_tokens: 30,
                      reasoning_tokens: 10,
                    },
                    unclassified_tokens: 0,
                  },
                  tokens: {
                    input_tokens: 100,
                    output_tokens: 40,
                    reasoning_tokens: 10,
                    total_tokens: 140,
                  },
                  failed: false,
                },
              ],
            },
          },
        },
      },
    };

    for (const detail of [
      collectUsageDetails(usageData)[0],
      collectUsageDetailsWithEndpoint(usageData)[0],
    ]) {
      expect(detail).toMatchObject({
        accounting_version: 0,
        accounting_valid: false,
        accounting_quality: 'complete',
      });
    }
  });
});

describe('usage token helpers', () => {
  const canonicalDetailForValidation = (
    accountingVersion: unknown = 2,
    unclassifiedTokens: unknown = 0
  ) => ({
    accounting_version: accountingVersion,
    token_breakdown: {
      schema_version: 2,
      quality: 'complete',
      total_tokens: 140,
      input: {
        total_tokens: 100,
        uncached_tokens: 100,
        cache_read_tokens: 0,
        cache_write_tokens: 0,
      },
      output: {
        total_tokens: 40,
        non_reasoning_tokens: 30,
        reasoning_tokens: 10,
      },
      unclassified_tokens: unclassifiedTokens,
    },
    provider: 'openai',
    tokens: {
      input_tokens: 100,
      output_tokens: 40,
      reasoning_tokens: 10,
      total_tokens: 140,
    },
  });

  it('keeps legacy cached tokens separate from fine-grained cache buckets', () => {
    expect(compatibleCachedTokens(5, 0, 4, 1)).toBe(0);
    expect(compatibleCachedTokens(10, 0, 4, 1)).toBe(5);
    expect(compatibleCachedTokens(0, 8, 3, 0)).toBe(5);
  });

  it('normalizes cache hit rates across legacy, Anthropic, and GPT-5.6 usage', () => {
    expect(
      calculateCacheHitRate({
        modelName: 'gpt-5.4',
        inputTokens: 1_000,
        cachedTokens: 400,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
      })
    ).toBeCloseTo(0.4, 6);
    expect(
      calculateCacheHitRate({
        modelName: 'claude-sonnet-4',
        inputTokens: 450,
        cachedTokens: 0,
        cacheReadTokens: 300,
        cacheCreationTokens: 50,
      })
    ).toBeCloseTo(300 / 450, 6);
    expect(
      calculateCacheHitRate({
        modelName: 'openai/gpt-5.6-sol',
        inputTokens: 152_600,
        cachedTokens: 0,
        cacheReadTokens: 151_000,
        cacheCreationTokens: 1_000,
      })
    ).toBeCloseTo(151_000 / 152_600, 6);
  });

  it('clamps aggregated malformed cache ratios to 100%', () => {
    expect(calculateCacheHitRateFromTotals(1_500, 1_000)).toBe(1);
  });

  it('keeps ambiguous unknown-provider legacy fields in one unclassified lower bound', () => {
    expect(
      extractTotalTokens({
        tokens: {
          input_tokens: 10,
          output_tokens: 20,
          reasoning_tokens: 3,
          cached_tokens: 10,
          cache_read_tokens: 4,
          cache_creation_tokens: 1,
        },
      })
    ).toBe(40);
  });

  it('keeps unknown-provider legacy fields unclassified even without reasoning tokens', () => {
    expect(
      resolveTokenAccounting({
        tokens: {
          input_tokens: 10,
          output_tokens: 20,
          reasoning_tokens: 0,
          total_tokens: 30,
        },
      })
    ).toMatchObject({
      sourceValid: false,
      quality: 'unclassified',
      inputTokens: 0,
      outputTokens: 0,
      unclassifiedTokens: 30,
      totalTokens: 30,
      pricingSafe: false,
    });
  });

  it('marks included cache buckets larger than input as inconsistent', () => {
    expect(
      resolveTokenAccounting({
        provider: 'openai',
        tokens: {
          input_tokens: 1,
          output_tokens: 2,
          cached_tokens: 5,
          total_tokens: 3,
        },
      })
    ).toMatchObject({
      quality: 'inconsistent',
      inputTokens: 0,
      outputTokens: 0,
      unclassifiedTokens: 7,
      totalTokens: 7,
      pricingSafe: false,
    });
  });

  it('uses a valid nested CPA v2 breakdown as the authoritative contract', () => {
    const accounting = resolveTokenAccounting({
      accounting_version: 2,
      accounting_valid: true,
      token_breakdown: {
        schema_version: 2,
        quality: 'complete',
        total_tokens: 165,
        input: {
          total_tokens: 125,
          uncached_tokens: 100,
          cache_read_tokens: 20,
          cache_write_tokens: 5,
        },
        output: {
          total_tokens: 40,
          non_reasoning_tokens: 30,
          reasoning_tokens: 10,
        },
        unclassified_tokens: 0,
      },
      tokens: {
        input_tokens: 999,
        output_tokens: 888,
        reasoning_tokens: 777,
        total_tokens: 2_664,
      },
    });

    expect(accounting).toMatchObject({
      sourceValid: true,
      quality: 'complete',
      inputTokens: 125,
      uncachedInputTokens: 100,
      cacheReadTokens: 20,
      cacheCreationTokens: 5,
      outputTokens: 40,
      nonReasoningOutputTokens: 30,
      reasoningTokens: 10,
      unclassifiedTokens: 0,
      totalTokens: 165,
      incomplete: false,
    });
  });

  it('preserves a migrated canonical breakdown with legacy accounting provenance', () => {
    expect(
      resolveTokenAccounting({
        accounting_version: 0,
        accounting_valid: false,
        token_breakdown: {
          schema_version: 2,
          quality: 'complete',
          total_tokens: 165,
          input: {
            total_tokens: 125,
            uncached_tokens: 100,
            cache_read_tokens: 20,
            cache_write_tokens: 5,
          },
          output: {
            total_tokens: 40,
            non_reasoning_tokens: 30,
            reasoning_tokens: 10,
          },
          unclassified_tokens: 0,
        },
        tokens: {
          input_tokens: 999,
          output_tokens: 888,
          reasoning_tokens: 777,
          total_tokens: 2_664,
        },
      })
    ).toMatchObject({
      accountingVersion: 0,
      sourceValid: false,
      quality: 'complete',
      inputTokens: 125,
      outputTokens: 40,
      nonReasoningOutputTokens: 30,
      reasoningTokens: 10,
      unclassifiedTokens: 0,
      totalTokens: 165,
      pricingSafe: true,
    });
  });

  it('does not mark explicit legacy nested provenance valid when accounting_valid is absent', () => {
    const detail = canonicalDetailForValidation(0);

    expect(resolveTokenAccounting(detail)).toMatchObject({
      accountingVersion: 0,
      sourceValid: false,
      quality: 'complete',
      pricingSafe: true,
    });
  });

  it('preserves migrated flat canonical buckets with legacy accounting provenance', () => {
    expect(
      resolveTokenAccounting({
        accounting_version: 0,
        accounting_valid: false,
        accounting_quality: 'complete',
        input_tokens: 125,
        output_tokens: 40,
        non_reasoning_output_tokens: 30,
        reasoning_tokens: 10,
        cache_read_tokens: 20,
        cache_creation_tokens: 5,
        unclassified_tokens: 0,
        total_tokens: 165,
      })
    ).toMatchObject({
      accountingVersion: 0,
      sourceValid: false,
      quality: 'complete',
      inputTokens: 125,
      uncachedInputTokens: 100,
      outputTokens: 40,
      nonReasoningOutputTokens: 30,
      reasoningTokens: 10,
      cacheReadTokens: 20,
      cacheCreationTokens: 5,
      unclassifiedTokens: 0,
      totalTokens: 165,
      pricingSafe: true,
    });
  });

  it('keeps an invalid claimed CPA v2 breakdown incomplete and unpriced', () => {
    const detail = {
      accounting_version: 2,
      token_breakdown: {
        schema_version: 2,
        quality: 'complete',
        total_tokens: 139,
        input: {
          total_tokens: 100,
          uncached_tokens: 100,
          cache_read_tokens: 0,
          cache_write_tokens: 0,
        },
        output: {
          total_tokens: 40,
          non_reasoning_tokens: 30,
          reasoning_tokens: 10,
        },
        unclassified_tokens: 0,
      },
      provider: 'openai',
      tokens: {
        input_tokens: 100,
        output_tokens: 40,
        reasoning_tokens: 10,
        total_tokens: 140,
      },
      __modelName: 'gpt-invalid-v2',
    };

    expect(resolveTokenAccounting(detail)).toMatchObject({
      accountingVersion: 2,
      sourceValid: false,
      quality: 'inconsistent',
      inputTokens: 0,
      outputTokens: 0,
      unclassifiedTokens: 140,
      totalTokens: 140,
      incomplete: true,
      pricingSafe: false,
    });
    expect(
      calculateCost(detail, {
        'gpt-invalid-v2': { prompt: 1, completion: 2, cache: 0 },
      })
    ).toBe(0);
  });

  it('rejects a canonical breakdown with missing required fields', () => {
    const detail = {
      accounting_version: 2,
      token_breakdown: {
        schema_version: 2,
        quality: 'complete',
      },
      provider: 'openai',
      tokens: {
        input_tokens: 100,
        output_tokens: 40,
        reasoning_tokens: 10,
        total_tokens: 140,
      },
    };

    expect(resolveTokenAccounting(detail)).toMatchObject({
      accountingVersion: 2,
      sourceValid: false,
      quality: 'inconsistent',
      inputTokens: 0,
      outputTokens: 0,
      unclassifiedTokens: 140,
      totalTokens: 140,
      pricingSafe: false,
    });
  });

  it('rejects a string-encoded canonical accounting version', () => {
    expect(resolveTokenAccounting(canonicalDetailForValidation('2'))).toMatchObject({
      accountingVersion: 0,
      sourceValid: false,
      quality: 'inconsistent',
      unclassifiedTokens: 140,
      totalTokens: 140,
      pricingSafe: false,
    });
  });

  it('rejects string-encoded canonical token buckets', () => {
    expect(resolveTokenAccounting(canonicalDetailForValidation(2, '0'))).toMatchObject({
      accountingVersion: 2,
      sourceValid: false,
      quality: 'inconsistent',
      unclassifiedTokens: 140,
      totalTokens: 140,
      pricingSafe: false,
    });
  });

  it('rejects a non-boolean canonical validity flag', () => {
    expect(
      resolveTokenAccounting({
        ...canonicalDetailForValidation(2),
        accounting_valid: 'false',
      })
    ).toMatchObject({
      accountingVersion: 2,
      sourceValid: false,
      quality: 'inconsistent',
      unclassifiedTokens: 140,
      totalTokens: 140,
      pricingSafe: false,
    });
  });

  it('treats an isolated canonical validity flag as an incomplete canonical claim', () => {
    expect(
      resolveTokenAccounting({
        accounting_valid: false,
        provider: 'openai',
        tokens: { input_tokens: 100, output_tokens: 20, total_tokens: 120 },
      })
    ).toMatchObject({
      accountingVersion: 0,
      sourceValid: false,
      quality: 'inconsistent',
      unclassifiedTokens: 120,
      totalTokens: 120,
      pricingSafe: false,
    });
  });

  it('keeps an explicit legacy accounting version without canonical markers on the legacy path', () => {
    expect(
      resolveTokenAccounting({
        accounting_version: 0,
        provider: 'openai',
        tokens: { input_tokens: 100, output_tokens: 20, total_tokens: 120 },
      })
    ).toMatchObject({
      accountingVersion: 0,
      sourceValid: false,
      quality: 'complete',
      inputTokens: 100,
      outputTokens: 20,
      unclassifiedTokens: 0,
      totalTokens: 120,
      pricingSafe: true,
    });
  });

  it.each([
    {
      name: 'string cache read',
      overrides: { cache_read_tokens: '20' },
    },
    {
      name: 'fractional cache write',
      overrides: {
        input_tokens: 125.5,
        cache_creation_tokens: 5.5,
        total_tokens: 165.5,
      },
    },
    {
      name: 'negative unclassified',
      overrides: { unclassified_tokens: -1 },
    },
    {
      name: 'fractional reasoning',
      overrides: {
        output_tokens: 40.5,
        reasoning_tokens: 10.5,
        total_tokens: 165.5,
      },
    },
    {
      name: 'negative input',
      overrides: {
        input_tokens: -1,
        cache_read_tokens: 0,
        cache_creation_tokens: 0,
        output_tokens: 40,
        total_tokens: 40,
      },
    },
  ])('rejects flat canonical $name tokens', ({ overrides }) => {
    const detail = {
      accounting_version: 2,
      accounting_valid: true,
      accounting_quality: 'complete',
      input_tokens: 125,
      output_tokens: 40,
      non_reasoning_output_tokens: 30,
      reasoning_tokens: 10,
      cache_read_tokens: 20,
      cache_creation_tokens: 5,
      unclassified_tokens: 0,
      total_tokens: 165,
      ...overrides,
    };

    expect(resolveTokenAccounting(detail)).toMatchObject({
      accountingVersion: 2,
      sourceValid: false,
      quality: 'inconsistent',
      pricingSafe: false,
    });
  });

  it.each(['cache_read_tokens', 'cache_creation_tokens', 'unclassified_tokens'])(
    'rejects a flat canonical breakdown missing %s',
    (field) => {
      const detail: Record<string, unknown> = {
        accounting_version: 2,
        accounting_valid: true,
        accounting_quality: 'complete',
        input_tokens: 125,
        output_tokens: 40,
        non_reasoning_output_tokens: 30,
        reasoning_tokens: 10,
        cache_read_tokens: 20,
        cache_creation_tokens: 5,
        unclassified_tokens: 0,
        total_tokens: 165,
      };
      delete detail[field];

      expect(resolveTokenAccounting(detail)).toMatchObject({
        sourceValid: false,
        quality: 'inconsistent',
        pricingSafe: false,
      });
    }
  );

  it('rejects an explicit accounting version that conflicts with the breakdown schema', () => {
    const detail = {
      accounting_version: 3,
      accounting_valid: true,
      token_breakdown: {
        schema_version: 2,
        quality: 'complete',
        total_tokens: 140,
        input: {
          total_tokens: 100,
          uncached_tokens: 100,
          cache_read_tokens: 0,
          cache_write_tokens: 0,
        },
        output: {
          total_tokens: 40,
          non_reasoning_tokens: 30,
          reasoning_tokens: 10,
        },
        unclassified_tokens: 0,
      },
      provider: 'openai',
      tokens: {
        input_tokens: 100,
        output_tokens: 40,
        reasoning_tokens: 10,
        total_tokens: 140,
      },
      __modelName: 'gpt-version-conflict',
    };

    expect(resolveTokenAccounting(detail)).toMatchObject({
      accountingVersion: 3,
      sourceValid: false,
      quality: 'inconsistent',
      inputTokens: 0,
      outputTokens: 0,
      unclassifiedTokens: 140,
      totalTokens: 140,
      pricingSafe: false,
    });
    expect(
      calculateCost(detail, {
        'gpt-version-conflict': { prompt: 1, completion: 2, cache: 0 },
      })
    ).toBe(0);
  });

  it('rejects a canonical breakdown without an explicit accounting version', () => {
    const detail = {
      accounting_valid: true,
      token_breakdown: {
        schema_version: 2,
        quality: 'complete',
        total_tokens: 140,
        input: {
          total_tokens: 100,
          uncached_tokens: 100,
          cache_read_tokens: 0,
          cache_write_tokens: 0,
        },
        output: {
          total_tokens: 40,
          non_reasoning_tokens: 30,
          reasoning_tokens: 10,
        },
        unclassified_tokens: 0,
      },
      provider: 'openai',
      tokens: {
        input_tokens: 100,
        output_tokens: 40,
        reasoning_tokens: 10,
        total_tokens: 140,
      },
    };

    expect(resolveTokenAccounting(detail)).toMatchObject({
      accountingVersion: 0,
      sourceValid: false,
      quality: 'inconsistent',
      inputTokens: 0,
      outputTokens: 0,
      unclassifiedTokens: 140,
      totalTokens: 140,
      pricingSafe: false,
    });
  });

  it('treats an explicit null token breakdown as invalid canonical metadata', () => {
    expect(
      resolveTokenAccounting({
        token_breakdown: null,
        provider: 'openai',
        tokens: { input_tokens: 100, output_tokens: 20, total_tokens: 120 },
      })
    ).toMatchObject({
      accountingVersion: 0,
      sourceValid: false,
      quality: 'inconsistent',
      inputTokens: 0,
      outputTokens: 0,
      unclassifiedTokens: 120,
      totalTokens: 120,
      pricingSafe: false,
    });
  });

  it.each([
    { name: 'null accounting version', marker: { accounting_version: null } },
    { name: 'null accounting validity', marker: { accounting_valid: null } },
    { name: 'empty accounting quality', marker: { accounting_quality: '' } },
  ])('treats $name as invalid canonical metadata', ({ marker }) => {
    expect(
      resolveTokenAccounting({
        ...marker,
        provider: 'openai',
        tokens: { input_tokens: 100, output_tokens: 20, total_tokens: 120 },
      })
    ).toMatchObject({
      sourceValid: false,
      quality: 'inconsistent',
      inputTokens: 0,
      outputTokens: 0,
      unclassifiedTokens: 120,
      totalTokens: 120,
      pricingSafe: false,
    });
  });

  it('does not infer a missing canonical non-reasoning bucket from legacy output fields', () => {
    const detail = {
      accounting_version: 2,
      accounting_valid: true,
      accounting_quality: 'complete',
      provider: 'anthropic',
      tokens: {
        input_tokens: 100,
        output_tokens: 50,
        reasoning_tokens: 10,
        cache_read_tokens: 0,
        cache_creation_tokens: 0,
        unclassified_tokens: 0,
        total_tokens: 150,
      },
      __modelName: 'claude-invalid-flat-v2',
    };

    expect(resolveTokenAccounting(detail)).toMatchObject({
      quality: 'inconsistent',
      inputTokens: 0,
      outputTokens: 0,
      unclassifiedTokens: 150,
      totalTokens: 150,
      pricingSafe: false,
    });
    expect(
      calculateCost(detail, {
        'claude-invalid-flat-v2': { prompt: 1, completion: 2, cache: 0 },
      })
    ).toBe(0);
  });

  it.each([
    {
      name: 'OpenAI output includes reasoning',
      detail: {
        provider: 'openai',
        tokens: { input_tokens: 100, output_tokens: 40, reasoning_tokens: 10 },
      },
      expected: {
        inputTokens: 100,
        outputTokens: 40,
        nonReasoningOutputTokens: 30,
        totalTokens: 140,
      },
    },
    {
      name: 'Claude cache and reasoning are independent',
      detail: {
        provider: 'anthropic',
        tokens: {
          input_tokens: 100,
          output_tokens: 40,
          reasoning_tokens: 10,
          cache_read_tokens: 20,
          cache_creation_tokens: 5,
        },
      },
      expected: {
        inputTokens: 125,
        outputTokens: 50,
        nonReasoningOutputTokens: 40,
        totalTokens: 175,
      },
    },
    {
      name: 'Gemini reasoning is separate while cache is included',
      detail: {
        executor_type: 'GeminiExecutor',
        tokens: {
          input_tokens: 100,
          output_tokens: 40,
          reasoning_tokens: 10,
          cache_read_tokens: 20,
        },
      },
      expected: {
        inputTokens: 100,
        outputTokens: 50,
        nonReasoningOutputTokens: 40,
        totalTokens: 150,
      },
    },
    {
      name: 'OpenRouter output includes reasoning',
      detail: {
        provider: 'openrouter',
        tokens: {
          input_tokens: 100,
          output_tokens: 40,
          reasoning_tokens: 10,
          cache_read_tokens: 20,
        },
      },
      expected: {
        inputTokens: 100,
        outputTokens: 40,
        nonReasoningOutputTokens: 30,
        totalTokens: 140,
      },
    },
    {
      name: 'Anthropic marker wins over a generic OpenAI marker',
      detail: {
        provider: 'anthropic-openai-gateway',
        tokens: {
          input_tokens: 100,
          output_tokens: 40,
          reasoning_tokens: 10,
          cache_read_tokens: 20,
        },
      },
      expected: {
        inputTokens: 120,
        outputTokens: 50,
        nonReasoningOutputTokens: 40,
        totalTokens: 170,
      },
    },
    {
      name: 'Explicit OpenAI-compatible provider wins over an Anthropic suffix',
      detail: {
        provider: 'openai-compatible-anthropic',
        tokens: {
          input_tokens: 100,
          output_tokens: 40,
          reasoning_tokens: 10,
          cache_read_tokens: 20,
        },
      },
      expected: {
        inputTokens: 100,
        outputTokens: 40,
        nonReasoningOutputTokens: 30,
        totalTokens: 140,
      },
    },
  ])('$name', ({ detail, expected }) => {
    expect(resolveTokenAccounting(detail)).toMatchObject({
      ...expected,
      quality: 'complete',
      unclassifiedTokens: 0,
    });
  });

  it('does not price ambiguous unknown-provider legacy buckets', () => {
    const detail = {
      tokens: {
        input_tokens: 100_000,
        output_tokens: 20_000,
        reasoning_tokens: 5_000,
      },
      __modelName: 'custom-model',
    };

    expect(resolveTokenAccounting(detail)).toMatchObject({
      inputTokens: 0,
      outputTokens: 0,
      unclassifiedTokens: 120_000,
      totalTokens: 120_000,
      pricingSafe: false,
    });
    expect(
      calculateCost(detail, {
        'custom-model': { prompt: 1, completion: 2, cache: 0.1 },
      })
    ).toBe(0);
  });

  it('uses Anthropic cache input fields when total tokens are missing', () => {
    expect(
      extractTotalTokens({
        tokens: {
          input_tokens: 100,
          output_tokens: 20,
          cached_tokens: 34,
          cache_read_input_tokens: 23,
          cache_creation_input_tokens: 11,
        },
      })
    ).toBe(154);
  });
});

describe('cache input accounting semantics', () => {
  it.each(cacheInputAccountingFixtures)('matches shared fixture: $name', (fixture) => {
    const accounting = normalizeCacheAccounting({
      context: fixture.context,
      inputTokens: fixture.tokens.input,
      cachedTokens: fixture.tokens.cached,
      cacheTokens: fixture.tokens.cache,
      cacheReadTokens: fixture.tokens.read,
      cacheCreationTokens: fixture.tokens.creation,
    });

    expect(accounting).toMatchObject({
      mode: fixture.expected.mode,
      uncachedInputTokens: fixture.expected.uncached,
      totalInputTokens: fixture.expected.totalInput,
      cacheCreationTokens: fixture.expected.cacheCreation,
    });
    expect(accounting.legacyRead + accounting.cacheReadTokens).toBe(fixture.expected.cacheRead);
  });

  it.each([
    {
      name: 'OpenAICompat executor beats Claude alias',
      context: { executorType: 'OpenAICompatExecutor', resolvedModel: 'claude-sonnet-4' },
      mode: 'included_in_input',
    },
    {
      name: 'Claude executor beats Grok alias',
      context: { executorType: 'ClaudeExecutor', resolvedModel: 'grok-4' },
      mode: 'separate_from_input',
    },
    {
      name: 'XAI executor beats Claude alias',
      context: { executorType: 'XAIWebsocketsExecutor', displayModel: 'claude-alias' },
      mode: 'included_in_input',
    },
    {
      name: 'Grok executor beats Claude alias',
      context: { executorType: 'GrokExecutor', displayModel: 'claude-alias' },
      mode: 'included_in_input',
    },
    {
      name: 'Moonshot executor beats Claude alias',
      context: { executorType: 'MoonshotExecutor', displayModel: 'claude-alias' },
      mode: 'included_in_input',
    },
    {
      name: 'Grok provider beats Claude alias',
      context: { provider: 'grok', resolvedModel: 'claude-sonnet' },
      mode: 'included_in_input',
    },
    {
      name: 'provider snapshot beats model',
      context: { providerSnapshot: 'moonshot', resolvedModel: 'claude-sonnet' },
      mode: 'included_in_input',
    },
    {
      name: 'OpenAI-compatible provider beats Anthropic suffix',
      context: { provider: 'openai-compatible-anthropic' },
      mode: 'included_in_input',
    },
    {
      name: 'resolved model beats requested model',
      context: { resolvedModel: 'claude-sonnet', requestedModel: 'gpt-5' },
      mode: 'separate_from_input',
    },
  ])('$name', ({ context, mode }) => {
    expect(inferCacheInputMode(context, 20, 10)).toBe(mode);
  });

  it('keeps a valid explicit mode above executor classification', () => {
    expect(
      inferCacheInputMode(
        { explicitMode: 'separate_from_input', executorType: 'XAIExecutor' },
        20,
        0
      )
    ).toBe('separate_from_input');
  });

  it('normalizes included and separate totals with the mirrored Go formulas', () => {
    expect(
      normalizeCacheAccounting({
        context: { executorType: 'XAIExecutor' },
        inputTokens: 100,
        cachedTokens: 0,
        cacheTokens: 0,
        cacheReadTokens: 20,
        cacheCreationTokens: 10,
      })
    ).toMatchObject({
      mode: 'included_in_input',
      uncachedInputTokens: 70,
      totalInputTokens: 100,
    });
    expect(
      normalizeCacheAccounting({
        context: { executorType: 'ClaudeExecutor' },
        inputTokens: 100,
        cachedTokens: 0,
        cacheTokens: 0,
        cacheReadTokens: 20,
        cacheCreationTokens: 10,
      })
    ).toMatchObject({
      mode: 'separate_from_input',
      uncachedInputTokens: 100,
      totalInputTokens: 130,
    });
  });

  it.each([
    {
      name: 'xAI included input',
      model: 'grok-4',
      detail: { executor_type: 'XAIExecutor' },
      totalInput: 100,
    },
    {
      name: 'Kimi provider included input',
      model: 'claude-alias',
      detail: { provider: 'moonshot' },
      totalInput: 100,
    },
    {
      name: 'Claude executor separate input',
      model: 'grok-alias',
      detail: { executor_type: 'ClaudeExecutor' },
      totalInput: 130,
    },
    {
      name: 'nested explicit mode',
      model: 'grok-4',
      detail: {},
      tokenMode: 'separate_from_input',
      totalInput: 130,
    },
  ])('$name is applied by readTokens', ({ model, detail, tokenMode, totalInput }) => {
    const usageData = {
      apis: {
        'POST /v1/chat/completions': {
          models: {
            [model]: {
              details: [
                {
                  timestamp: '2026-07-15T00:00:00Z',
                  source: 'account',
                  auth_index: 'auth-1',
                  ...detail,
                  tokens: {
                    input_tokens: 100,
                    cache_read_tokens: 20,
                    cache_creation_tokens: 10,
                    cache_input_mode: tokenMode,
                  },
                  failed: false,
                },
              ],
            },
          },
        },
      },
    };
    const [normalized] = collectUsageDetails(usageData);

    expect(normalized.tokens.input_tokens).toBe(totalInput);
    expect(normalized.tokens.total_tokens).toBe(totalInput);
  });

  it('prices xAI cache without double-counting included input', () => {
    const usageData = {
      apis: {
        'POST /v1/chat/completions': {
          models: {
            'grok-4': {
              details: [
                {
                  timestamp: '2026-07-15T00:00:00Z',
                  source: 'account',
                  auth_index: 'auth-1',
                  executor_type: 'XAIExecutor',
                  tokens: { input_tokens: 100, cache_read_tokens: 40 },
                  failed: false,
                },
              ],
            },
          },
        },
      },
    };
    const [detail] = collectUsageDetails(usageData);
    const cost = calculateCost(detail, {
      'grok-4': { prompt: 1, completion: 2, cache: 0.1, cacheRead: 0.1 },
    });

    expect(detail.tokens.input_tokens).toBe(100);
    expect(
      calculateCacheHitRate({
        inputTokens: detail.tokens.input_tokens,
        cachedTokens: detail.tokens.cached_tokens,
        cacheReadTokens: detail.tokens.cache_read_tokens,
        cacheCreationTokens: detail.tokens.cache_creation_tokens,
      })
    ).toBeCloseTo(0.4);
    expect(cost).toBeCloseTo(0.000064);
  });
});

describe('sensitive text masking', () => {
  it('does not redact ordinary AI-prefixed diagnostics or swallow JSON after cookie fields', () => {
    const text = `AImproved fallback AIServer down {"cookie":"session=secret","status":"401","detail":"upstream denied","retry_after":30}`;
    const masked = maskSensitiveText(text);

    expect(masked).toContain('AImproved fallback');
    expect(masked).toContain('AIServer down');
    expect(masked).toContain('"status":"401"');
    expect(masked).toContain('"detail":"upstream denied"');
    expect(masked).toContain('"retry_after":30');
    expect(masked).not.toContain('session=secret');
  });
});

describe('calculateCost model price preference', () => {
  const prices = {
    'gpt-5.5': { prompt: 5, completion: 10, cache: 1 },
    'gpt-5.4': { prompt: 50, completion: 100, cache: 10 },
  };

  it('prefers resolved upstream model when present', () => {
    const cost = calculateCost(
      {
        tokens: { input_tokens: 100_000, output_tokens: 0 },
        __modelName: 'gpt-5.4',
        __resolvedModel: 'gpt-5.5',
      },
      prices
    );
    expect(cost).toBeCloseTo(0.5);
  });

  it('falls back to requested alias when resolved is absent', () => {
    const cost = calculateCost(
      {
        tokens: { input_tokens: 100_000, output_tokens: 0 },
        __modelName: 'gpt-5.4',
      },
      prices
    );
    expect(cost).toBeCloseTo(5);
  });

  it('falls back to requested alias when resolved has no price entry', () => {
    const cost = calculateCost(
      {
        tokens: { input_tokens: 1_000_000, output_tokens: 0 },
        __modelName: 'gpt-5.4',
        __resolvedModel: 'unknown-upstream',
      },
      prices
    );
    expect(cost).toBeCloseTo(50);
  });

  it('prefers analytics model pricing before the raw requested suffix model', () => {
    const cost = calculateCost(
      {
        tokens: { input_tokens: 1_000_000, output_tokens: 0 },
        __modelName: 'deepseek-v4-flash',
        __requestedModel: 'deepseek-v4-flash(max)',
      },
      {
        'deepseek-v4-flash': { prompt: 2, completion: 0, cache: 0 },
        'deepseek-v4-flash(max)': { prompt: 9, completion: 0, cache: 0 },
      }
    );

    expect(cost).toBeCloseTo(2);
  });

  it('falls back to the raw requested model when analytics pricing is unavailable', () => {
    const cost = calculateCost(
      {
        tokens: { input_tokens: 1_000_000, output_tokens: 0 },
        __modelName: 'deepseek-v4-flash',
        __requestedModel: 'deepseek-v4-flash(max)',
      },
      {
        'deepseek-v4-flash(max)': { prompt: 9, completion: 0, cache: 0 },
      }
    );

    expect(cost).toBeCloseTo(9);
  });

  it('keeps resolved model tier behavior when using a requested price fallback', () => {
    const cost = calculateCost(
      {
        tokens: { input_tokens: 1_000_000, output_tokens: 0 },
        __modelName: 'gpt-5.4',
        __resolvedModel: 'unknown-upstream',
        service_tier: 'priority',
      },
      prices
    );

    expect(cost).toBeCloseTo(50);
  });

  it('charges cached input tokens only at the cache price', () => {
    const detail = {
      tokens: {
        input_tokens: 100_000,
        output_tokens: 50_000,
        cached_tokens: 25_000,
      },
      __modelName: 'gpt-5.5',
    };
    expect(resolveTokenAccounting(detail, 'gpt-5.5')).toMatchObject({
      quality: 'complete',
      uncachedInputTokens: 75_000,
      cachedTokens: 0,
      cacheReadTokens: 25_000,
      outputTokens: 50_000,
      totalTokens: 150_000,
      pricingSafe: true,
    });
    const cost = calculateCost(detail, {
      'gpt-5.5': { prompt: 2, completion: 4, cache: 1 },
    });
    expect(cost).toBeCloseTo(0.375);
  });

  it('prices canonical cache buckets without a legacy cached-token guess', () => {
    const cost = calculateCost(
      {
        accounting_version: 2,
        accounting_valid: true,
        token_breakdown: {
          schema_version: 2,
          quality: 'complete',
          total_tokens: 1_300_000,
          input: {
            total_tokens: 1_300_000,
            uncached_tokens: 900_000,
            cache_read_tokens: 300_000,
            cache_write_tokens: 100_000,
          },
          output: {
            total_tokens: 0,
            non_reasoning_tokens: 0,
            reasoning_tokens: 0,
          },
          unclassified_tokens: 0,
        },
        tokens: {
          input_tokens: 1_300_000,
          cached_tokens: 100_000,
          cache_read_tokens: 200_000,
          cache_creation_tokens: 100_000,
        },
        __modelName: 'mixed-cache',
      },
      {
        'mixed-cache': {
          prompt: 2,
          completion: 4,
          cache: 1,
          cacheRead: 0.5,
          cacheCreation: 3,
        },
      }
    );

    expect(cost).toBeCloseTo(2.25);
  });

  it('applies gpt-5.4 priority service tier multiplier', () => {
    const cost = calculateCost(
      {
        tokens: { input_tokens: 100_000 },
        __modelName: 'gpt-5.4',
        service_tier: 'priority',
      },
      {
        'gpt-5.4': { prompt: 2.5, completion: 5, cache: 1 },
      }
    );

    expect(cost).toBeCloseTo(0.5);
  });

  it('applies gpt-5.5 priority service tier multiplier', () => {
    const cost = calculateCost(
      {
        tokens: { input_tokens: 100_000 },
        __modelName: 'gpt-5.5',
        serviceTier: 'priority',
      },
      {
        'gpt-5.5': { prompt: 2, completion: 4, cache: 1 },
      }
    );

    expect(cost).toBeCloseTo(0.5);
  });

  it('uses the requested service tier for Codex billing', () => {
    const cost = calculateCost(
      {
        tokens: { input_tokens: 100_000 },
        __modelName: 'gpt-5.4',
        executor_type: 'codex',
        request_service_tier: 'priority',
        response_service_tier: 'default',
      },
      { 'gpt-5.4': { prompt: 2.5, completion: 5, cache: 1 } }
    );
    expect(cost).toBeCloseTo(0.5);
  });

  it('recognizes Codex OAuth from auth type metadata', () => {
    const cost = calculateCost(
      {
        tokens: { input_tokens: 100_000 },
        __modelName: 'gpt-5.4',
        auth_type: 'codex',
        request_service_tier: 'priority',
        response_service_tier: 'default',
      },
      { 'gpt-5.4': { prompt: 2.5, completion: 5, cache: 1 } }
    );
    expect(cost).toBeCloseTo(0.5);
  });

  it('keeps the response service tier for non-Codex billing', () => {
    const cost = calculateCost(
      {
        tokens: { input_tokens: 100_000 },
        __modelName: 'gpt-5.4',
        provider: 'openai-compatible',
        request_service_tier: 'priority',
        response_service_tier: 'default',
      },
      { 'gpt-5.4': { prompt: 2.5, completion: 5, cache: 1 } }
    );
    expect(cost).toBeCloseTo(0.25);
  });

  it('does not stack priority pricing with long-context pricing', () => {
    const modelPrices = { 'gpt-5.5': { prompt: 2, completion: 4, cache: 1 } };
    const tokens = { input_tokens: 1_000_000, output_tokens: 100_000 };
    const standard = calculateCost(
      { tokens, __modelName: 'gpt-5.5', service_tier: 'default' },
      modelPrices
    );
    const priority = calculateCost(
      { tokens, __modelName: 'gpt-5.5', service_tier: 'priority' },
      modelPrices
    );
    expect(priority).toBeCloseTo(standard);
  });

  it('uses flex pricing at half the standard rate', () => {
    const cost = calculateCost(
      { tokens: { input_tokens: 100_000 }, __modelName: 'gpt-5.5', service_tier: 'flex' },
      { 'gpt-5.5': { prompt: 2, completion: 4, cache: 1 } }
    );
    expect(cost).toBeCloseTo(0.1);
  });

  it('keeps flex and batch discounts for legacy long-context pricing', () => {
    const modelPrices = { 'gpt-5.5': { prompt: 2, completion: 4, cache: 1 } };
    const tokens = { input_tokens: 1_000_000, output_tokens: 100_000 };
    const standard = calculateCost(
      { tokens, __modelName: 'gpt-5.5', service_tier: 'default' },
      modelPrices
    );
    for (const serviceTier of ['flex', 'batch']) {
      expect(
        calculateCost({ tokens, __modelName: 'gpt-5.5', service_tier: serviceTier }, modelPrices)
      ).toBeCloseTo(standard * 0.5);
    }
  });

  it('uses an explicit batch price with legacy long-context multipliers', () => {
    const cost = calculateCost(
      {
        tokens: { input_tokens: 300_000 },
        __modelName: 'gpt-5.5',
        service_tier: 'batch',
      },
      {
        'gpt-5.5': {
          prompt: 5,
          completion: 30,
          cache: 0.5,
          serviceTiers: [
            {
              mode: 'batch',
              serviceTier: 'batch',
              prompt: 2,
              completion: 15,
              cache: 0.25,
              promptConfigured: true,
              completionConfigured: true,
            },
          ],
        },
      }
    );
    expect(cost).toBeCloseTo(1.2);
  });

  it('keeps default and missing service tier at standard cost', () => {
    const modelPrices = {
      'gpt-5.4': { prompt: 2.5, completion: 5, cache: 1 },
    };

    expect(
      calculateCost(
        {
          tokens: { input_tokens: 1_000_000 },
          __modelName: 'gpt-5.4',
          service_tier: 'default',
        },
        modelPrices
      )
    ).toBeCloseTo(5);
    expect(
      calculateCost(
        {
          tokens: { input_tokens: 1_000_000 },
          __modelName: 'gpt-5.4',
        },
        modelPrices
      )
    ).toBeCloseTo(5);
  });

  it('uses official gpt-5.6 prices when the current price book has no entry', () => {
    const cost = calculateCost(
      {
        accounting_version: 2,
        accounting_valid: true,
        token_breakdown: {
          schema_version: 2,
          quality: 'complete',
          total_tokens: 220_000,
          input: {
            total_tokens: 200_000,
            uncached_tokens: 120_000,
            cache_read_tokens: 60_000,
            cache_write_tokens: 20_000,
          },
          output: {
            total_tokens: 20_000,
            non_reasoning_tokens: 20_000,
            reasoning_tokens: 0,
          },
          unclassified_tokens: 0,
        },
        tokens: {
          input_tokens: 200_000,
          output_tokens: 20_000,
          cached_tokens: 20_000,
          cache_read_tokens: 40_000,
          cache_creation_tokens: 20_000,
        },
        __modelName: 'openai/gpt-5.6-sol',
      },
      {}
    );

    expect(cost).toBeCloseTo(1.355);
  });

  it('prefers configured gpt-5.6 base prices over the official fallback', () => {
    const cost = calculateCost(
      {
        tokens: { input_tokens: 100_000 },
        __modelName: 'gpt-5.6-sol',
      },
      {
        'gpt-5.6-sol': { prompt: 9, completion: 18, cache: 0 },
      }
    );

    expect(cost).toBeCloseTo(0.9);
  });

  it('applies gpt-5.6 long-context multipliers above 272K', () => {
    const cost = calculateCost(
      {
        tokens: {
          input_tokens: 1_000_000,
          output_tokens: 100_000,
          cache_read_tokens: 200_000,
          cache_creation_tokens: 100_000,
        },
        __modelName: 'gpt-5.6-sol',
      },
      {}
    );

    expect(cost).toBeCloseTo(12.95);
  });

  it('keeps exactly 272K of gpt-5.6 input at standard rates', () => {
    const cost = calculateCost(
      {
        tokens: { input_tokens: 272_000, output_tokens: 100_000 },
        __modelName: 'gpt-5.6-luna',
      },
      {}
    );

    expect(cost).toBeCloseTo(0.872);
  });

  it('uses resolved gpt-5.6 behavior with a configured alias price', () => {
    const cost = calculateCost(
      {
        tokens: {
          input_tokens: 200_000,
          cache_read_tokens: 40_000,
          cache_creation_tokens: 20_000,
        },
        __modelName: 'internal-fast',
        __resolvedModel: 'openai/gpt-5.6-luna',
      },
      {
        'internal-fast': { prompt: 2, completion: 4, cache: 0 },
      }
    );

    expect(cost).toBeCloseTo(0.338);
  });

  it('ignores the legacy cache price when gpt-5.6 cache-read pricing is missing', () => {
    const cost = calculateCost(
      {
        tokens: {
          input_tokens: 200_000,
          cache_read_tokens: 40_000,
          cache_creation_tokens: 20_000,
        },
        __modelName: 'gpt-5.6-terra',
      },
      {
        'gpt-5.6-terra': {
          prompt: 10,
          completion: 20,
          cache: 10,
          promptConfigured: true,
          completionConfigured: true,
        },
      }
    );

    expect(cost).toBeCloseTo(1.69);
  });

  it('respects explicitly configured zero prices for gpt-5.6', () => {
    const cost = calculateCost(
      {
        tokens: {
          input_tokens: 250_000,
          cache_read_tokens: 150_000,
          cache_creation_tokens: 100_000,
        },
        __modelName: 'gpt-5.6-sol',
      },
      {
        'gpt-5.6-sol': {
          prompt: 5,
          completion: 30,
          cache: 0.5,
          cacheRead: 0,
          cacheCreation: 0,
          promptConfigured: true,
          completionConfigured: true,
          cacheReadConfigured: true,
          cacheCreationConfigured: true,
        },
      }
    );

    expect(cost).toBe(0);
  });

  it('keeps resolved non-gpt behavior when the requested alias is gpt-5.6', () => {
    const cost = calculateCost(
      {
        tokens: { input_tokens: 1_000_000 },
        __modelName: 'gpt-5.6-sol',
        __resolvedModel: 'resolved-other',
        service_tier: 'priority',
      },
      {
        'resolved-other': { prompt: 2, completion: 4, cache: 0 },
      }
    );

    expect(cost).toBeCloseTo(2);
  });

  it('does not guess priority multiplier for unknown models', () => {
    const cost = calculateCost(
      {
        tokens: { input_tokens: 1_000_000 },
        __modelName: 'unknown-model',
        provider: 'openai',
        service_tier: 'priority',
      },
      {
        'unknown-model': { prompt: 2.5, completion: 5, cache: 1 },
      }
    );

    expect(cost).toBeCloseTo(2.5);
  });

  it('uses explicit Fast Mode prices for fast and priority in the base context band', () => {
    const modelPrices = {
      'gpt-5.5': {
        prompt: 5,
        completion: 30,
        cache: 0.5,
        contextTiers: [
          {
            thresholdTokens: 272_000,
            prompt: 10,
            completion: 45,
            cache: 0,
            promptConfigured: true,
            completionConfigured: true,
          },
        ],
        serviceTiers: [
          {
            mode: 'fast',
            serviceTier: 'priority',
            prompt: 12.5,
            completion: 75,
            cache: 0,
            promptConfigured: true,
            completionConfigured: true,
          },
        ],
      },
    };

    for (const serviceTier of ['fast', 'priority']) {
      expect(
        calculateCost(
          {
            tokens: { input_tokens: 100_000, output_tokens: 10_000 },
            __modelName: 'gpt-5.5',
            service_tier: serviceTier,
          },
          modelPrices
        )
      ).toBeCloseTo(2);
    }
    expect(
      calculateCost(
        {
          tokens: { input_tokens: 100_000, output_tokens: 10_000 },
          __modelName: 'gpt-5.5',
          service_tier: 'default',
        },
        modelPrices
      )
    ).toBeCloseTo(0.8);
  });

  it('does not re-enable legacy long-context pricing inside an explicit base context band', () => {
    const cost = calculateCost(
      {
        tokens: { input_tokens: 300_000, output_tokens: 100_000 },
        __modelName: 'gpt-5.5',
        service_tier: 'priority',
      },
      {
        'gpt-5.5': {
          prompt: 5,
          completion: 30,
          cache: 0.5,
          contextTiers: [
            {
              thresholdTokens: 500_000,
              prompt: 10,
              completion: 45,
              cache: 0,
              promptConfigured: true,
              completionConfigured: true,
            },
          ],
          serviceTiers: [
            {
              mode: 'fast',
              serviceTier: 'priority',
              prompt: 10,
              completion: 20,
              cache: 0,
              promptConfigured: true,
              completionConfigured: true,
            },
          ],
        },
      }
    );

    expect(cost).toBeCloseTo(5);
  });

  it('uses long-context pricing instead of an explicit Fast Mode price', () => {
    const cost = calculateCost(
      {
        tokens: { input_tokens: 300_000, output_tokens: 100_000 },
        __modelName: 'gpt-5.5',
        service_tier: 'priority',
      },
      {
        'gpt-5.5': {
          prompt: 5,
          completion: 30,
          cache: 0.5,
          serviceTiers: [
            {
              mode: 'fast',
              serviceTier: 'priority',
              prompt: 12.5,
              completion: 75,
              cache: 0,
              promptConfigured: true,
              completionConfigured: true,
            },
          ],
        },
      }
    );

    expect(cost).toBeCloseTo(7.5);
  });

  it('selects the highest context tier with strict threshold semantics', () => {
    const modelPrices = {
      'tiered-model': {
        prompt: 1,
        completion: 2,
        cache: 0.1,
        contextTiers: [
          {
            thresholdTokens: 32_000,
            prompt: 3,
            completion: 4,
            cache: 0,
            promptConfigured: true,
            completionConfigured: true,
          },
          {
            thresholdTokens: 200_000,
            prompt: 5,
            completion: 8,
            cache: 0,
            promptConfigured: true,
            completionConfigured: true,
          },
        ],
      },
    };

    expect(
      calculateCost(
        { tokens: { input_tokens: 32_000 }, __modelName: 'tiered-model', provider: 'openai' },
        modelPrices
      )
    ).toBeCloseTo(0.032);
    expect(
      calculateCost(
        { tokens: { input_tokens: 200_000 }, __modelName: 'tiered-model', provider: 'openai' },
        modelPrices
      )
    ).toBeCloseTo(0.6);
    expect(
      calculateCost(
        { tokens: { input_tokens: 200_001 }, __modelName: 'tiered-model', provider: 'openai' },
        modelPrices
      )
    ).toBeCloseTo(1.000005);
  });

  it('does not stack priority pricing with active context-tier pricing', () => {
    const modelPrices = {
      'gpt-5.6-sol': {
        prompt: 5,
        completion: 30,
        cache: 0.5,
        contextTiers: [
          {
            thresholdTokens: 272_000,
            prompt: 10,
            completion: 40,
            cache: 0,
            promptConfigured: true,
            completionConfigured: true,
          },
        ],
        serviceTiers: [
          {
            mode: 'fast',
            serviceTier: 'priority',
            prompt: 12.5,
            completion: 75,
            cache: 0,
            promptConfigured: true,
            completionConfigured: true,
          },
        ],
      },
    };
    const tokens = { input_tokens: 1_000_000 };

    const standard = calculateCost(
      { tokens, __modelName: 'gpt-5.6-sol', service_tier: 'default' },
      modelPrices
    );
    const priority = calculateCost(
      { tokens, __modelName: 'gpt-5.6-sol', service_tier: 'priority' },
      modelPrices
    );

    expect(priority).toBeCloseTo(standard);
  });

  it('inherits missing tier cache rates and preserves explicit zero overrides', () => {
    const cost = calculateCost(
      {
        accounting_version: 2,
        accounting_valid: true,
        token_breakdown: {
          schema_version: 2,
          quality: 'complete',
          total_tokens: 1_000_000,
          input: {
            total_tokens: 1_000_000,
            uncached_tokens: 700_000,
            cache_read_tokens: 200_000,
            cache_write_tokens: 100_000,
          },
          output: {
            total_tokens: 0,
            non_reasoning_tokens: 0,
            reasoning_tokens: 0,
          },
          unclassified_tokens: 0,
        },
        tokens: {
          input_tokens: 1_000_000,
          cache_read_tokens: 200_000,
          cache_creation_tokens: 100_000,
        },
        __modelName: 'tiered-cache',
      },
      {
        'tiered-cache': {
          prompt: 2,
          completion: 4,
          cache: 1,
          cacheRead: 0.5,
          cacheCreation: 3,
          cacheReadConfigured: true,
          cacheCreationConfigured: true,
          contextTiers: [
            {
              thresholdTokens: 100,
              prompt: 4,
              completion: 8,
              cache: 0,
              cacheRead: 0,
              promptConfigured: true,
              completionConfigured: true,
              cacheReadConfigured: true,
            },
          ],
        },
      }
    );

    expect(cost).toBeCloseTo(3.1);
  });

  it('uses generic context tiers instead of the hardcoded GPT long-context rule', () => {
    const cost = calculateCost(
      {
        tokens: { input_tokens: 1_000_000 },
        __modelName: 'gpt-5.6-sol',
      },
      {
        'gpt-5.6-sol': {
          prompt: 5,
          completion: 30,
          cache: 0.5,
          contextTiers: [
            {
              thresholdTokens: 272_000,
              prompt: 10,
              completion: 40,
              cache: 0,
              promptConfigured: true,
              completionConfigured: true,
            },
          ],
        },
      }
    );

    expect(cost).toBeCloseTo(10);
  });
});

describe('getServiceTierMultiplier', () => {
  it('matches backend priority tier rules', () => {
    expect(getServiceTierMultiplier('gpt-5.4', 'default')).toBe(1);
    expect(getServiceTierMultiplier('openai/gpt-5.6-sol', 'priority')).toBe(2);
    expect(getServiceTierMultiplier('gpt-5.4', 'priority')).toBe(2);
    expect(getServiceTierMultiplier('gpt-5.4', 'fast')).toBe(2);
    expect(getServiceTierMultiplier('gpt-5.4-mini', 'priority')).toBe(2);
    expect(getServiceTierMultiplier('gpt-5.5', 'priority')).toBe(2.5);
    expect(getServiceTierMultiplier('gpt-5.3-codex', 'priority')).toBe(2);
    expect(getServiceTierMultiplier('gpt-5.4', 'unknown')).toBe(1);
    expect(getServiceTierMultiplier('unknown-model', 'priority')).toBe(1);
  });
});

describe('model price storage', () => {
  it('normalizes persisted service-tier rules and rejects ambiguous aliases', () => {
    const stored = {
      'gpt-valid': {
        prompt: 5,
        completion: 30,
        cache: 0.5,
        serviceTiers: [
          {
            mode: ' FAST ',
            serviceTier: ' PRIORITY ',
            prompt: 12.5,
            completion: 75,
            cache: 0,
            promptConfigured: true,
            completionConfigured: true,
          },
        ],
      },
      'gpt-ambiguous': {
        prompt: 5,
        completion: 30,
        cache: 0.5,
        serviceTiers: [
          {
            mode: 'fast',
            serviceTier: 'priority',
            prompt: 12.5,
            completion: 75,
            cache: 0,
            promptConfigured: true,
          },
          {
            mode: 'priority',
            serviceTier: 'turbo',
            prompt: 15,
            completion: 80,
            cache: 0,
            promptConfigured: true,
          },
        ],
      },
    };
    vi.stubGlobal('localStorage', {
      getItem: (key: string) =>
        key === 'cli-proxy-model-prices-v2' ? JSON.stringify(stored) : null,
    });

    const prices = loadModelPrices();
    expect(prices['gpt-valid'].serviceTiers).toEqual([
      expect.objectContaining({ mode: 'fast', serviceTier: 'priority', prompt: 12.5 }),
    ]);
    expect(prices['gpt-ambiguous'].serviceTiers).toBeUndefined();
  });
});
