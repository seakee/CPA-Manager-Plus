import { describe, expect, it } from 'vitest';
import { CONFIG_SECTION_KEYS, extractConfigSectionValue } from './sections';
import type { Config } from '@/types/config';

describe('config sections', () => {
  it('contains the cacheable section keys used by the config store', () => {
    expect(CONFIG_SECTION_KEYS).toContain('api-keys');
    expect(CONFIG_SECTION_KEYS).toContain('xai-api-key');
    expect(CONFIG_SECTION_KEYS).toContain('openai-compatibility');
    expect(CONFIG_SECTION_KEYS).toContain('oauth-excluded-models');
  });

  it('extracts normalized config properties by raw section key', () => {
    const config: Config = {
      debug: true,
      proxyUrl: 'http://proxy.local',
      apiKeys: ['key-1'],
      xaiApiKeys: [{ apiKey: 'xai-key', baseUrl: 'https://api.x.ai/v1' }],
      raw: {
        custom: 'fallback',
      },
    };

    expect(extractConfigSectionValue(config, 'debug')).toBe(true);
    expect(extractConfigSectionValue(config, 'proxy-url')).toBe('http://proxy.local');
    expect(extractConfigSectionValue(config, 'api-keys')).toEqual(['key-1']);
    expect(extractConfigSectionValue(config, 'xai-api-key')).toEqual(config.xaiApiKeys);
    expect(extractConfigSectionValue(config, 'custom' as never)).toBe('fallback');
  });

  it('returns undefined for missing config or full config extraction', () => {
    expect(extractConfigSectionValue(null, 'debug')).toBeUndefined();
    expect(extractConfigSectionValue({}, undefined)).toBeUndefined();
  });
});
