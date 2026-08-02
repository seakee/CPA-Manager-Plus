import { describe, expect, it } from 'vitest';

import { handleDemoApiRequest } from './demoApi';

describe('demo plugin API', () => {
  it('returns an empty cpa-key-policy catalog', async () => {
    const response = await handleDemoApiRequest<{ keys: unknown[] }>(
      'get',
      '/plugins/cpa-key-policy/keys'
    );

    expect(response).toEqual({ keys: [] });
  });
});
