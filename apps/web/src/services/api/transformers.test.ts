import { describe, expect, it } from 'vitest';
import { normalizeConfigResponse } from './transformers';

describe('normalizeConfigResponse xAI API keys', () => {
  it('normalizes the xai-api-key contract using the provider-key shape', () => {
    const config = normalizeConfigResponse({
      'xai-api-key': [
        {
          'api-key': 'xai-key',
          'auth-index': 'xai-auth',
          'base-url': 'https://api.x.ai/v1',
          prefix: 'team-xai',
          websockets: true,
          'disable-cooling': true,
          models: [{ name: 'grok-4.5', alias: 'grok-latest' }],
        },
      ],
    });

    expect(config.xaiApiKeys).toEqual([
      expect.objectContaining({
        apiKey: 'xai-key',
        authIndex: 'xai-auth',
        baseUrl: 'https://api.x.ai/v1',
        prefix: 'team-xai',
        websockets: true,
        disableCooling: true,
        models: [{ name: 'grok-4.5', alias: 'grok-latest' }],
      }),
    ]);
  });

  it('normalizes the xAI inference inspection switch aliases', () => {
    const config = normalizeConfigResponse({
      clean: {
        xai_inference_enabled: true,
        xai_inference_user_agent: 'xai-custom-agent',
        xai_inference_model: 'grok-custom',
        xai_inference_prompt: 'Reply OK.',
      },
    });

    expect(config.clean).toMatchObject({
      xaiInferenceEnabled: true,
      xaiInferenceUserAgent: 'xai-custom-agent',
      xaiInferenceModel: 'grok-custom',
      xaiInferencePrompt: 'Reply OK.',
    });
  });
});
