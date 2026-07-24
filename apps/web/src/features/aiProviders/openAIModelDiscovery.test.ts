import { describe, expect, it, vi } from 'vitest';
import { discoverOpenAIModels } from './openAIModelDiscovery';

const options = {
  baseUrl: 'https://api.example.com/v1',
  headers: [],
  proxyRequiresSavedEntryMessage: 'Save the provider first',
};

describe('discoverOpenAIModels', () => {
  it('does not retry without the configured proxy route', async () => {
    const fetchModels = vi.fn().mockRejectedValue(new Error('upstream blocked'));

    await expect(
      discoverOpenAIModels({
        ...options,
        apiKeyEntries: [
          {
            apiKey: 'sk-test',
            authIndex: 'auth-1',
            proxyUrl: 'socks5://proxy.example.com:1080',
          },
        ],
        fetchModels,
      })
    ).rejects.toThrow('upstream blocked');

    expect(fetchModels).toHaveBeenCalledTimes(1);
    expect(fetchModels).toHaveBeenCalledWith(
      options.baseUrl,
      'sk-test',
      {},
      'auth-1'
    );
  });

  it('requires proxy-backed drafts to be saved before discovery', async () => {
    const fetchModels = vi.fn();

    await expect(
      discoverOpenAIModels({
        ...options,
        apiKeyEntries: [
          {
            apiKey: 'sk-test',
            proxyUrl: 'socks5://proxy.example.com:1080',
          },
        ],
        fetchModels,
      })
    ).rejects.toThrow(options.proxyRequiresSavedEntryMessage);

    expect(fetchModels).not.toHaveBeenCalled();
  });

  it('preserves provider and entry headers on the routed request', async () => {
    const fetchModels = vi.fn().mockResolvedValue([]);

    await discoverOpenAIModels({
      ...options,
      headers: [{ key: 'X-Provider', value: 'provider-value' }],
      apiKeyEntries: [
        {
          apiKey: 'sk-test',
          authIndex: 'auth-1',
          proxyUrl: 'socks5://proxy.example.com:1080',
          headers: { 'X-Entry': 'entry-value' },
        },
      ],
      fetchModels,
    });

    expect(fetchModels).toHaveBeenCalledWith(
      options.baseUrl,
      'sk-test',
      {
        'X-Provider': 'provider-value',
        'X-Entry': 'entry-value',
      },
      'auth-1'
    );
  });

  it('uses a single credentialless request for a public endpoint', async () => {
    const fetchModels = vi.fn().mockResolvedValue([{ name: 'public-model' }]);

    await discoverOpenAIModels({
      ...options,
      apiKeyEntries: [],
      fetchModels,
    });

    expect(fetchModels).toHaveBeenCalledTimes(1);
    expect(fetchModels).toHaveBeenCalledWith(options.baseUrl, undefined, {}, undefined);
  });
});
