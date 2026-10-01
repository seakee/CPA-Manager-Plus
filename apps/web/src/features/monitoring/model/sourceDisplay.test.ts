import { describe, expect, it } from 'vitest';
import { buildSourceInfoMap } from '@/utils/sourceResolver';
import { sha256Hex } from '@/utils/apiKeyHash';
import {
  buildMonitoringSourceDisplay,
  isGenericMonitoringProviderLabel,
  isKeyDisambiguatedLabel,
  isProviderLikeMonitoringLabel,
  isRedundantMonitoringLabel,
} from './sourceDisplay';
import type { MonitoringAuthMeta, MonitoringChannelMeta } from './types';

const emptyContext = {
  authMetaMap: new Map<string, MonitoringAuthMeta>(),
  channelByAuthIndex: new Map(),
};

describe('isGenericMonitoringProviderLabel', () => {
  it('treats codex and xAI aliases as generic provider labels', () => {
    expect(isGenericMonitoringProviderLabel('codex')).toBe(true);
    expect(isGenericMonitoringProviderLabel('xai')).toBe(true);
    expect(isGenericMonitoringProviderLabel('XAI')).toBe(true);
    expect(isGenericMonitoringProviderLabel('x-ai')).toBe(true);
    expect(isGenericMonitoringProviderLabel('grok')).toBe(true);
    expect(isGenericMonitoringProviderLabel('antigravity')).toBe(true);
    expect(isGenericMonitoringProviderLabel('devin')).toBe(true);
    expect(isGenericMonitoringProviderLabel('Devin')).toBe(true);
    expect(isGenericMonitoringProviderLabel('meta')).toBe(true);
    expect(isGenericMonitoringProviderLabel('Meta')).toBe(true);
    expect(isGenericMonitoringProviderLabel('anyrouter.top #1')).toBe(false);
  });
});

describe('isProviderLikeMonitoringLabel', () => {
  it('identifies generic or provider-equivalent labels', () => {
    expect(isProviderLikeMonitoringLabel('codex', 'codex')).toBe(true);
    expect(isProviderLikeMonitoringLabel('grok', 'xai')).toBe(true);
    expect(isProviderLikeMonitoringLabel('workbuddy', 'workbuddy')).toBe(true);
    expect(isProviderLikeMonitoringLabel('WorkBuddy', 'workbuddy')).toBe(true);
    expect(isProviderLikeMonitoringLabel('Team Relay', 'workbuddy')).toBe(false);
    expect(isProviderLikeMonitoringLabel('workbuddy #1', 'workbuddy')).toBe(false);
    expect(isProviderLikeMonitoringLabel('', 'workbuddy')).toBe(false);
    expect(isProviderLikeMonitoringLabel(null, 'workbuddy')).toBe(false);
    expect(isProviderLikeMonitoringLabel('workbuddy', null)).toBe(false);
  });
});

describe('key disambiguation helpers', () => {
  it('detects provider/key ordinal disambiguation labels', () => {
    expect(isKeyDisambiguatedLabel('kuaileshifu #1', 'kuaileshifu')).toBe(true);
    expect(isKeyDisambiguatedLabel('kuaileshifu #2', 'kuaileshifu')).toBe(true);
    expect(isKeyDisambiguatedLabel('kuaileshifu', 'kuaileshifu')).toBe(false);
    expect(isKeyDisambiguatedLabel('anyrouter.top #1', 'codex')).toBe(false);
    expect(isRedundantMonitoringLabel('kuaileshifu', 'kuaileshifu #1')).toBe(true);
  });
});

describe('buildMonitoringSourceDisplay', () => {
  it('keeps generic xAI provider labels secondary to the account identity', () => {
    const authMetaMap = new Map<string, MonitoringAuthMeta>([
      [
        'xai-1',
        {
          authIndex: 'xai-1',
          label: 'xai',
          account: 'oc0abcdef@yijihwjw.com',
          provider: 'xai',
          status: 'active',
          disabled: false,
          unavailable: false,
          runtimeOnly: false,
          planType: '-',
          updatedAt: '',
        },
      ],
    ]);

    const display = buildMonitoringSourceDisplay(
      {
        authIndex: 'xai-1',
        accountSnapshot: 'oc0abcdef@yijihwjw.com',
        authLabelSnapshot: 'xai',
        authProviderSnapshot: 'xai',
        channel: 'xai',
      },
      {
        authMetaMap,
        channelByAuthIndex: new Map(),
      }
    );

    expect(display.primary).toBe('oc0***@yijihwjw.com');
    expect(display.meta).toBe('xai');
    expect(display.accountMasked).toBe('oc0***@yijihwjw.com');
    expect(display.provider).toBe('xai');
  });

  it('keeps generic codex provider labels secondary to the account identity', () => {
    const authMetaMap = new Map<string, MonitoringAuthMeta>([
      [
        'codex-1',
        {
          authIndex: 'codex-1',
          label: 'codex',
          account: 'fbcabcdef@vip.qq.com',
          provider: 'codex',
          status: 'active',
          disabled: false,
          unavailable: false,
          runtimeOnly: false,
          planType: '-',
          updatedAt: '',
        },
      ],
    ]);

    const display = buildMonitoringSourceDisplay(
      {
        authIndex: 'codex-1',
        accountSnapshot: 'fbcabcdef@vip.qq.com',
        authLabelSnapshot: 'codex',
        authProviderSnapshot: 'codex',
        channel: 'codex',
      },
      {
        authMetaMap,
        channelByAuthIndex: new Map(),
      }
    );

    expect(display.primary).toBe('fbc***@vip.qq.com');
    expect(display.meta).toBe('codex');
  });

  it('puts a provider key alias before generic Codex metadata', () => {
    const apiKey = 'sk-codex-display-alias-test-1234567890';
    const display = buildMonitoringSourceDisplay(
      {
        source: `h:${sha256Hex(apiKey)}`,
        authIndex: 'codex-alias-auth',
        authProviderSnapshot: 'codex',
        channel: 'codex',
      },
      {
        authMetaMap: new Map(),
        channelByAuthIndex: new Map(),
        sourceInfoMap: buildSourceInfoMap({
          codexApiKeys: [
            {
              apiKey,
              authIndex: 'codex-alias-auth',
              baseUrl: 'https://api.alias.example/v1',
            },
          ],
          providerKeyAliases: [{ provider: 'codex', apiKeyHash: sha256Hex(apiKey), alias: 'WWP1' }],
        }),
      }
    );

    expect(display.primary).toBe('WWP1');
    expect(display.meta).toBe('');
    expect(display.providerAlias).toBe('WWP1');
  });

  it('keeps generic devin provider labels secondary to the account identity', () => {
    const authMetaMap = new Map<string, MonitoringAuthMeta>([
      [
        'devin-1',
        {
          authIndex: 'devin-1',
          label: 'devin',
          account: 'user@example.com',
          provider: 'devin',
          status: 'active',
          disabled: false,
          unavailable: false,
          runtimeOnly: false,
          planType: '-',
          updatedAt: '',
        },
      ],
    ]);

    const display = buildMonitoringSourceDisplay(
      {
        authIndex: 'devin-1',
        accountSnapshot: 'user@example.com',
        authLabelSnapshot: 'devin',
        authProviderSnapshot: 'devin',
        channel: 'devin',
      },
      {
        authMetaMap,
        channelByAuthIndex: new Map(),
      }
    );

    expect(display.primary).toBe('use***@example.com');
    expect(display.meta).toBe('devin');
    expect(display.accountMasked).toBe('use***@example.com');
    expect(display.provider).toBe('devin');
  });

  it('still prefers non-generic channel names over the account identity', () => {
    const display = buildMonitoringSourceDisplay(
      {
        authIndex: 'relay-1',
        account: 'user@example.com',
        channel: 'anyrouter.top #1',
        authProviderSnapshot: 'codex',
      },
      emptyContext
    );

    expect(display.primary).toBe('anyrouter.top #1');
    expect(display.meta).toBe('codex');
  });

  it('prefers OpenAI-compatible multi-key disambiguation over the bare provider name', () => {
    const sourceInfoMap = buildSourceInfoMap({
      openaiCompatibility: [
        {
          name: 'kuaileshifu',
          baseUrl: 'https://api.kuaileshifu.example/v1',
          apiKeyEntries: [
            { apiKey: 'sk-openai111111aaaa', authIndex: 'kuai-auth-1' },
            { apiKey: 'sk-openai222222bbbb', authIndex: 'kuai-auth-2' },
          ],
        },
      ],
    });
    const channelByAuthIndex = new Map<string, MonitoringChannelMeta>([
      [
        'kuai-auth-1',
        {
          key: 'openai:0',
          name: 'kuaileshifu',
          baseUrl: 'https://api.kuaileshifu.example/v1',
          host: 'api.kuaileshifu.example',
          disabled: false,
          authIndices: ['kuai-auth-1', 'kuai-auth-2'],
          modelNames: [],
        },
      ],
    ]);

    const display = buildMonitoringSourceDisplay(
      {
        authIndex: 'kuai-auth-1',
        source: 'm:sk-o...aaaa',
        accountSnapshot: 'kuaileshifu',
        authLabelSnapshot: 'kuaileshifu',
        authProviderSnapshot: 'openai',
        channel: 'kuaileshifu',
      },
      {
        authMetaMap: new Map(),
        channelByAuthIndex,
        sourceInfoMap,
      }
    );

    expect(display.primary).toBe('kuaileshifu #1');
    expect(display.meta).toBe('openai');
    expect(display.channel).toBe('kuaileshifu');
    expect(display.channelHost).toBe('api.kuaileshifu.example');
  });

  it('keeps a single-key OpenAI-compatible provider name as primary', () => {
    const sourceInfoMap = buildSourceInfoMap({
      openaiCompatibility: [
        {
          name: 'kuaileshifu',
          baseUrl: 'https://api.kuaileshifu.example/v1',
          apiKeyEntries: [{ apiKey: 'sk-openai111111aaaa', authIndex: 'kuai-auth-1' }],
        },
      ],
    });
    const channelByAuthIndex = new Map<string, MonitoringChannelMeta>([
      [
        'kuai-auth-1',
        {
          key: 'openai:0',
          name: 'kuaileshifu',
          baseUrl: 'https://api.kuaileshifu.example/v1',
          host: 'api.kuaileshifu.example',
          disabled: false,
          authIndices: ['kuai-auth-1'],
          modelNames: [],
        },
      ],
    ]);

    const display = buildMonitoringSourceDisplay(
      {
        authIndex: 'kuai-auth-1',
        source: 'm:sk-o...aaaa',
        accountSnapshot: 'kuaileshifu',
        authProviderSnapshot: 'openai',
        channel: 'kuaileshifu',
      },
      {
        authMetaMap: new Map(),
        channelByAuthIndex,
        sourceInfoMap,
      }
    );

    expect(display.primary).toBe('kuaileshifu');
    expect(display.meta).toBe('openai');
  });

  it('normalizes CPA OpenAI-compatible runtime labels to the configured provider name', () => {
    const sourceInfoMap = buildSourceInfoMap({
      openaiCompatibility: [
        {
          name: 'CC wwp1',
          baseUrl: 'https://wawapii.example/v1',
          apiKeyEntries: [{ apiKey: 'sk-compatible-display-cc-wwp1' }],
        },
      ],
    });

    const display = buildMonitoringSourceDisplay(
      {
        source: 'openai-compatible-cc wwp1',
        authProviderSnapshot: 'openai-compatible-cc wwp1',
      },
      {
        authMetaMap: new Map(),
        channelByAuthIndex: new Map(),
        sourceInfoMap,
      }
    );

    expect(display.primary).toBe('CC wwp1');
    expect(display.provider).toBe('CC wwp1');
    expect(display.channel).toBe('CC wwp1');
  });

  it('prefers credential account over dynamic/unknown provider when channel and source are provider-equivalent (#686)', () => {
    const authMetaMap = new Map<string, MonitoringAuthMeta>([
      [
        'wb-1',
        {
          authIndex: 'wb-1',
          label: 'workbuddy',
          account: 'marscosmo',
          provider: 'workbuddy',
          status: 'active',
          disabled: false,
          unavailable: false,
          runtimeOnly: false,
          planType: '-',
          updatedAt: '',
        },
      ],
    ]);

    const display = buildMonitoringSourceDisplay(
      {
        authIndex: 'wb-1',
        account: 'marscosmo',
        accountSnapshot: 'marscosmo',
        authLabelSnapshot: 'workbuddy',
        authProviderSnapshot: 'workbuddy',
        channel: 'workbuddy',
        source: 'workbuddy',
      },
      {
        authMetaMap,
        channelByAuthIndex: new Map(),
      }
    );

    expect(display.primary).toBe('marscosmo');
    expect(display.meta).toBe('workbuddy');
  });

  it('preserves distinct custom channel name over account for unknown provider', () => {
    const display = buildMonitoringSourceDisplay(
      {
        authIndex: 'wb-1',
        account: 'marscosmo',
        channel: 'Team WorkBuddy Relay',
        authProviderSnapshot: 'workbuddy',
      },
      emptyContext
    );

    expect(display.primary).toBe('Team WorkBuddy Relay');
    expect(display.meta).toBe('workbuddy');
  });

  it('preserves key-disambiguated source over account for unknown provider', () => {
    const display = buildMonitoringSourceDisplay(
      {
        authIndex: 'wb-1',
        account: 'marscosmo',
        channel: 'workbuddy',
        source: 'workbuddy #1',
        authProviderSnapshot: 'workbuddy',
      },
      emptyContext
    );

    expect(display.primary).toBe('workbuddy #1');
    expect(display.meta).toBe('marscosmo');
  });

  it('falls back to unknown provider rather than opaque hash when no account exists', () => {
    const validHash = '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef';
    const display = buildMonitoringSourceDisplay(
      {
        authIndex: 'future-1',
        account: '',
        channel: 'future-provider',
        source: `h:${validHash}`,
        authProviderSnapshot: 'future-provider',
      },
      emptyContext
    );

    expect(display.primary).toBe('future-provider');
    expect(display.primary).not.toContain('h:');
  });
});
