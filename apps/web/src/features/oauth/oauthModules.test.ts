import { describe, expect, it, vi } from 'vitest';
import type { PluginListEntry } from '@/types';
import { resolveCallbackUrl, validateDevinCallback, XAI_CALLBACK_URL } from './callbackUrl';
import { isDeviceFlowSession, resolveFlowView, splitAuthUrl } from './flowView';
import { createOAuthAttempts } from './oauthAttempts';
import { buildPluginOAuthProviderCards } from './providers';

describe('oauthAttempts', () => {
  it('aborts the previous attempt and suppresses stale callbacks when restarted', async () => {
    const callbacks: Array<() => void> = [];
    const manager = createOAuthAttempts({
      setTimeout: (cb) => {
        callbacks.push(cb);
        return callbacks.length;
      },
      clearTimeout: vi.fn(),
    });

    const first = manager.begin('codex');
    const onResult = vi.fn(() => true);
    const onError = vi.fn();
    let resolveFirst!: (val: { status: 'ok' }) => void;
    first.poll(
      () =>
        new Promise<{ status: 'ok' }>((resolve) => {
          resolveFirst = resolve;
        }),
      onResult,
      onError,
      3000
    );

    expect(first.isPolling()).toBe(true);
    callbacks[0]();

    const second = manager.begin('codex');
    expect(first.signal.aborted).toBe(true);
    expect(first.isCurrent()).toBe(false);
    expect(second.isCurrent()).toBe(true);

    resolveFirst({ status: 'ok' });
    await Promise.resolve();
    await Promise.resolve();
    expect(onResult).not.toHaveBeenCalled();
    expect(onError).not.toHaveBeenCalled();
  });
});

describe('callbackUrl', () => {
  it('resolves raw xAI code, query string, and full callback URLs cleanly', () => {
    expect(resolveCallbackUrl('xai', 'ABC123', 'st-1')).toBe(
      `${XAI_CALLBACK_URL}?code=ABC123&state=st-1`
    );
    expect(resolveCallbackUrl('xai', 'code: XYZ789', 'st-1')).toBe(
      `${XAI_CALLBACK_URL}?code=XYZ789&state=st-1`
    );
    expect(resolveCallbackUrl('xai', '?code=Q1&state=st-2', 'st-1')).toBe(
      `${XAI_CALLBACK_URL}?state=st-2&code=Q1`
    );
    expect(resolveCallbackUrl('xai', 'https://example.com/cb?code=1&state=2', 'st-1')).toBe(
      'https://example.com/cb?code=1&state=2'
    );
    expect(resolveCallbackUrl('xai', 'ONLY_CODE', undefined)).toBeNull();
  });

  it('validates Devin callback URL structure and state match', () => {
    expect(validateDevinCallback('not-a-url', 'st-1')).toBe('invalid');
    expect(validateDevinCallback('http://localhost/cb?state=st-1', 'st-1')).toBe('invalid');
    expect(validateDevinCallback('http://localhost/cb?code=1&state=other', 'st-1')).toBe(
      'state_mismatch'
    );
    expect(validateDevinCallback('http://localhost/cb?code=1&state=st-1', 'st-1')).toBeUndefined();
    expect(
      validateDevinCallback('http://localhost/cb?error=access_denied&state=st-1', 'st-1')
    ).toBeUndefined();
  });
});

describe('flowView', () => {
  it('keeps flow view active when URL exists even if a transient polling error occurred', () => {
    expect(resolveFlowView({})).toBe('idle');
    expect(resolveFlowView({ status: 'error', error: 'start failed' })).toBe('failed');
    expect(
      resolveFlowView({
        url: 'https://auth.openai.com/oauth',
        state: 's1',
        status: 'waiting',
        pollingError: 'timeout',
      })
    ).toBe('flow');
    expect(resolveFlowView({ status: 'success' })).toBe('success');
  });

  it('detects device flow dynamically from backend response metadata', () => {
    expect(isDeviceFlowSession({ flow: 'device', userCode: 'ABCD-1234' }, 'browser')).toBe(true);
    expect(isDeviceFlowSession({ userCode: 'ABCD-1234' }, 'browser')).toBe(true);
    expect(isDeviceFlowSession({ flow: 'browser' }, 'device')).toBe(false);
    expect(isDeviceFlowSession({}, 'device')).toBe(true);
    expect(isDeviceFlowSession({}, 'browser')).toBe(false);
  });

  it('splits authorization URLs into host and trailing path', () => {
    expect(splitAuthUrl('https://auth.openai.com/oauth/authorize?foo=1')).toEqual({
      host: 'auth.openai.com',
      rest: '/oauth/authorize?foo=1',
    });
    expect(splitAuthUrl('invalid-url')).toEqual({
      host: '',
      rest: 'invalid-url',
    });
  });
});

describe('buildPluginOAuthProviderCards', () => {
  it('filters disabled plugins, skips built-in duplicates, and expands multi-entry plugins', () => {
    const makePlugin = (overrides: Partial<PluginListEntry> & { id: string }): PluginListEntry => ({
      path: `/plugins/${overrides.id}`,
      configured: true,
      registered: true,
      enabled: true,
      effectiveEnabled: true,
      supportsOAuth: true,
      logo: '',
      configFields: [],
      menus: [],
      metadata: null,
      ...overrides,
    });
    const plugins: PluginListEntry[] = [
      makePlugin({
        id: 'disabled-one',
        oauthProvider: 'disabled-prov',
        enabled: false,
        effectiveEnabled: false,
      }),
      makePlugin({
        id: 'builtin-clash',
        oauthProvider: 'devin',
      }),
      makePlugin({
        id: 'suite-plugin',
        oauthProvider: 'one',
        oauthProviders: ['one', 'two', 'codex'],
        oauthProviderEntries: [
          { identifier: 'one', name: 'Provider One' },
          { identifier: 'two', name: 'Provider Two' },
        ],
      }),
    ];

    const cards = buildPluginOAuthProviderCards(plugins, 'http://127.0.0.1:8317');
    expect(cards.map((card) => ({ id: card.id, title: card.title, cb: card.supportsCallback }))).toEqual(
      [
        { id: 'one', title: 'Provider One', cb: true },
        { id: 'two', title: 'Provider Two', cb: true },
      ]
    );
  });
});
