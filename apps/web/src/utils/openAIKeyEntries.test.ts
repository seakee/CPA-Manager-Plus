import { describe, expect, it } from 'vitest';
import {
  getOpenAIKeyCount,
  getOpenAIModelDiscoveryEntry,
  getOpenAITestableKeyIndexes,
  getOpenAIUsageStatsCoverage,
  hasOpenAIKeyEntryConfiguration,
  updateOpenAIApiKey,
} from './openAIKeyEntries';

describe('OpenAI-compatible keyless editor rows', () => {
  it('recognizes only a completely empty placeholder as unconfigured', () => {
    expect(hasOpenAIKeyEntryConfiguration({ apiKey: '' })).toBe(false);
    expect(hasOpenAIKeyEntryConfiguration({ apiKey: '', authIndex: 'cpa-index' })).toBe(true);
    expect(hasOpenAIKeyEntryConfiguration({ apiKey: '', proxyUrl: 'http://proxy:8080' })).toBe(true);
    expect(hasOpenAIKeyEntryConfiguration({ apiKey: '', weight: 0 })).toBe(true);
    expect(hasOpenAIKeyEntryConfiguration({ apiKey: '', headers: { 'X-Test': 'value' } })).toBe(false);
  });

  it('prefers a real API key over an earlier configured keyless entry for model discovery', () => {
    const entries = [
      { apiKey: '', authIndex: 'keyless-index', proxyUrl: 'socks5://keyless:1080' },
      { apiKey: 'real-key', authIndex: 'keyed-index', proxyUrl: 'socks5://keyed:1080' },
    ];
    expect(getOpenAIModelDiscoveryEntry(entries)).toBe(entries[1]);
    expect(getOpenAIModelDiscoveryEntry([{ apiKey: '', proxyUrl: 'socks5://keyless:1080' }]))
      .toEqual({ apiKey: '', proxyUrl: 'socks5://keyless:1080' });
  });

  it('invalidates a server auth-index whenever the user edits an API key', () => {
    expect(
      updateOpenAIApiKey(
        { apiKey: '', authIndex: 'old-keyless-index', proxyUrl: 'socks5://proxy:1080' },
        'new-key'
      )
    ).toEqual({
      apiKey: 'new-key',
      authIndex: '',
      proxyUrl: 'socks5://proxy:1080',
    });
    expect(updateOpenAIApiKey({ apiKey: 'old-key', authIndex: 'old-key-index' }, 'new-key'))
      .toEqual({ apiKey: 'new-key', authIndex: '' });
  });

  it('counts only real API keys and reports keyless accounting coverage explicitly', () => {
    expect(getOpenAIKeyCount([])).toBe(0);
    expect(getOpenAIKeyCount([{ apiKey: '', proxyUrl: 'socks5://proxy:1080' }])).toBe(0);
    expect(getOpenAIKeyCount([{ apiKey: 'k1' }, { apiKey: '', proxyUrl: 'socks5://proxy:1080' }]))
      .toBe(1);

    expect(getOpenAIUsageStatsCoverage([])).toBe('unavailable');
    expect(getOpenAIUsageStatsCoverage([{ apiKey: '', proxyUrl: 'socks5://proxy:1080' }]))
      .toBe('unavailable');
    expect(getOpenAIUsageStatsCoverage([{ apiKey: 'k1' }])).toBe('full');
    expect(getOpenAIUsageStatsCoverage([{ apiKey: 'k1' }, { apiKey: '' }])).toBe('partial');
  });

  it('tests exactly one anonymous request when every editor row is blank', () => {
    expect(getOpenAITestableKeyIndexes([{ apiKey: '' }, { apiKey: '' }])).toEqual([0]);
    expect(getOpenAITestableKeyIndexes([])).toEqual([]);
  });

  it('skips empty trailing rows while retaining multiple configured keyless proxies', () => {
    expect(getOpenAITestableKeyIndexes([
      { apiKey: 'actual-key' },
      { apiKey: '' },
      { apiKey: '', proxyUrl: 'socks5://proxy:1080' },
      { apiKey: '', authIndex: 'keyless-index' },
    ])).toEqual([0, 2, 3]);
  });
});
