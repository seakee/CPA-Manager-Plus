import { describe, expect, it } from 'vitest';

import {
  ANTIGRAVITY_HUB_PLATFORM,
  ANTIGRAVITY_HUB_VERSION,
  ANTIGRAVITY_REQUEST_HEADERS,
  ANTIGRAVITY_USER_AGENT,
  buildAntigravityUserAgent,
  XAI_GROK_CLIENT_VERSION,
  XAI_GROK_USER_AGENT,
  XAI_INFERENCE_USER_AGENT,
} from './constants';

const versionAtLeast = (version: string, floor: string): boolean => {
  const actual = version.split('.').map((part) => Number.parseInt(part, 10) || 0);
  const required = floor.split('.').map((part) => Number.parseInt(part, 10) || 0);
  const length = Math.max(actual.length, required.length);

  for (let index = 0; index < length; index += 1) {
    const actualPart = actual[index] ?? 0;
    const requiredPart = required[index] ?? 0;
    if (actualPart !== requiredPart) return actualPart > requiredPart;
  }

  return true;
};

describe('Antigravity client identity', () => {
  it('meets the CPA Antigravity Hub compatibility floor', () => {
    expect(versionAtLeast(ANTIGRAVITY_HUB_VERSION, '2.23.0')).toBe(true);
  });

  it('keeps the Hub user agent and request header synchronized', () => {
    expect(ANTIGRAVITY_USER_AGENT).toBe(
      `antigravity/hub/${ANTIGRAVITY_HUB_VERSION} ${ANTIGRAVITY_HUB_PLATFORM}`
    );
    expect(buildAntigravityUserAgent({ version: '2.24.1', platform: 'darwin/arm64' })).toBe(
      'antigravity/hub/2.24.1 darwin/arm64'
    );
    expect(ANTIGRAVITY_REQUEST_HEADERS['User-Agent']).toBe(ANTIGRAVITY_USER_AGENT);
  });
});

describe('xAI Grok client identity', () => {
  it('meets the known xAI chat-proxy minimum Grok CLI version', () => {
    expect(versionAtLeast(XAI_GROK_CLIENT_VERSION, '1.0.13')).toBe(true);
  });

  it('keeps Grok user agents synchronized with the client version', () => {
    expect(XAI_GROK_USER_AGENT).toContain(`grok-pager/${XAI_GROK_CLIENT_VERSION}`);
    expect(XAI_GROK_USER_AGENT).toContain(`grok-shell/${XAI_GROK_CLIENT_VERSION}`);
    expect(XAI_INFERENCE_USER_AGENT).toBe(`xai-grok-workspace/${XAI_GROK_CLIENT_VERSION}`);
  });
});
