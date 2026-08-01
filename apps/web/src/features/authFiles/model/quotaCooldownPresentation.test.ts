import { describe, expect, it } from 'vitest';
import { getQuotaCooldownPresentation } from './quotaCooldownPresentation';

describe('getQuotaCooldownPresentation', () => {
  it('describes Codex cooldowns as request-event automation', () => {
    const result = getQuotaCooldownPresentation({
      authFileName: 'codex.json',
      provider: 'codex',
      owner: 'cpamp_usage_429',
      recoverAtMs: 2_000_000_000_000,
    });

    expect(result.kind).toBe('codex');
    expect(result.badgeKey).toBe('auth_files.quota_cooldown_badge_codex');
    expect(result.sourceLabelKey).toBe('auth_files.quota_cooldown_source_codex');
  });

  it('describes xAI cooldowns as free-usage request-event automation', () => {
    const result = getQuotaCooldownPresentation({
      authFileName: 'xai.json',
      provider: 'xai',
      owner: 'cpamp_xai_free_usage',
      recoverAtMs: 2_000_000_000_000,
    });

    expect(result.kind).toBe('xai');
    expect(result.providerLabel).toBe('xAI');
    expect(result.titleKey).toBe('auth_files.quota_cooldown_badge_title_xai');
  });

  it('keeps unknown owners generic instead of guessing a provider policy', () => {
    const result = getQuotaCooldownPresentation({
      authFileName: 'future.json',
      provider: 'future-provider',
      owner: 'future-owner',
      recoverAtMs: 2_000_000_000_000,
    });

    expect(result.kind).toBe('provider');
    expect(result.providerLabel).toBe('future-provider');
    expect(result.titleDefault).toContain('Owner: {{owner}}');
  });

  it('does not let an unknown owner inherit the current xAI automation policy', () => {
    const result = getQuotaCooldownPresentation({
      authFileName: 'xai-future.json',
      provider: 'xai',
      owner: 'future-xai-policy',
      recoverAtMs: 2_000_000_000_000,
    });

    expect(result.kind).toBe('provider');
  });
});
