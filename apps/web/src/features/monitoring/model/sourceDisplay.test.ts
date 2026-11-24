import { describe, expect, it } from 'vitest';
import { buildSourceInfoMap } from '@/utils/sourceResolver';
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
  it('uses provider snapshots to disambiguate failure sources for shared keys (#1013)', () => {
    const firstKey = 'sk-1000000000000001';
    const secondKey = 'sk-2000000000000002';
    const sourceInfoMap = buildSourceInfoMap({
      codexApiKeys: [firstKey, secondKey].map((apiKey) => ({
        apiKey,
        baseUrl: 'https://relay-a.example/v1',
      })),
      claudeApiKeys: [secondKey, firstKey].map((apiKey) => ({
        apiKey,
        baseUrl: 'https://relay-a.example',
        excludedModels: ['*'],
      })),
    });
    const context = { ...emptyContext, sourceInfoMap };

    const codex = buildMonitoringSourceDisplay(
      { source: 'm:sk-1...0001', authLabelSnapshot: 'codex', authProviderSnapshot: 'codex' },
      context
    );
    const claude = buildMonitoringSourceDisplay(
      { source: 'm:sk-1...0001', authLabelSnapshot: 'claude', authProviderSnapshot: 'claude' },
      context
    );

    expect(codex.primary).toBe('relay-a.example #1');
    expect(codex.provider).toBe('codex');
    expect(claude.primary).toBe('relay-a.example #2');
    expect(claude.provider).toBe('claude');
  });

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

  it('treats CPA synthesized *-apikey runtime labels as generic and overrides them with configured provider aliases', () => {
    expect(isGenericMonitoringProviderLabel('claude-apikey')).toBe(true);
    expect(isGenericMonitoringProviderLabel('gemini-interactions')).toBe(true);
    expect(isGenericMonitoringProviderLabel('interactions-apikey')).toBe(true);
    expect(isGenericMonitoringProviderLabel('vertex-compat')).toBe(true);
    expect(isGenericMonitoringProviderLabel('meta-apikey')).toBe(true);

    const sourceInfoMap = buildSourceInfoMap({
      claudeApiKeys: [
        {
          name: 'Anthropic Main',
          apiKey: 'sk-ant-1111111111aaaa',
          authIndex: 'claude-idx-1',
        },
      ],
    });

    const display = buildMonitoringSourceDisplay(
      {
        authIndex: 'claude-idx-1',
        source: 'm:sk-a...aaaa',
        accountSnapshot: 'claude-apikey',
        authLabelSnapshot: 'claude-apikey',
        authProviderSnapshot: 'claude',
        channel: 'claude-apikey',
      },
      {
        authMetaMap: new Map(),
        channelByAuthIndex: new Map(),
        sourceInfoMap,
      }
    );

    expect(display.primary).toBe('Anthropic Main');
    expect(display.sourceLabel).toBe('Anthropic Main');
    expect(display.meta).toBe('claude');
  });

  it('reflects renamed native and OpenAI API-key providers over stale snapshot and channel labels without affecting OAuth accounts', () => {
    const sourceInfoMap = buildSourceInfoMap({
      claudeApiKeys: [
        {
          name: 'Claude Renamed Alias',
          prefix: 'old-claude-prefix',
          apiKey: 'sk-ant-9999999999zzzz',
          authIndex: 'claude-rename-idx',
        },
      ],
      openaiCompatibility: [
        {
          name: 'OpenRouter Renamed',
          baseUrl: 'https://openrouter.ai/api/v1',
          apiKeyEntries: [
            { apiKey: 'sk-or-1111111111aaaa', authIndex: 'or-rename-1' },
            { apiKey: 'sk-or-2222222222bbbb', authIndex: 'or-rename-2' },
          ],
        },
      ],
    });

    const renamedNativeDisplay = buildMonitoringSourceDisplay(
      {
        authIndex: 'claude-rename-idx',
        source: 't:old-claude-prefix',
        accountSnapshot: 'Old Claude Alias',
        authLabelSnapshot: 'Old Claude Alias',
        authProviderSnapshot: 'claude',
        channel: 'Old Claude Alias',
      },
      {
        authMetaMap: new Map(),
        channelByAuthIndex: new Map(),
        sourceInfoMap,
      }
    );
    expect(renamedNativeDisplay.primary).toBe('Claude Renamed Alias');
    expect(renamedNativeDisplay.sourceLabel).toBe('Claude Renamed Alias');
    expect(renamedNativeDisplay.account).toBe('Claude Renamed Alias');

    const renamedOpenAIDisplay = buildMonitoringSourceDisplay(
      {
        authIndex: 'or-rename-1',
        source: 'm:sk-o...aaaa',
        accountSnapshot: 'Old OpenRouter Name',
        authLabelSnapshot: 'Old OpenRouter Name',
        authProviderSnapshot: 'openai',
        channel: 'Old OpenRouter Name',
      },
      {
        authMetaMap: new Map(),
        channelByAuthIndex: new Map([
          [
            'or-rename-1',
            {
              key: 'openai:0',
              name: 'OpenRouter Renamed',
              baseUrl: 'https://openrouter.ai/api/v1',
              host: 'openrouter.ai',
              disabled: false,
              authIndices: ['or-rename-1', 'or-rename-2'],
              modelNames: [],
            },
          ],
        ]),
        sourceInfoMap,
      }
    );
    expect(renamedOpenAIDisplay.primary).toBe('OpenRouter Renamed #1');
    expect(renamedOpenAIDisplay.sourceLabel).toBe('OpenRouter Renamed #1');

    const oauthMeta = new Map<string, MonitoringAuthMeta>([
      [
        'oauth-idx-1',
        {
          authIndex: 'oauth-idx-1',
          label: 'OAuth Team Seat',
          account: 'seat@example.com',
          provider: 'codex',
          status: 'active',
          disabled: false,
          unavailable: false,
          runtimeOnly: false,
          planType: '',
          updatedAt: '',
        },
      ],
    ]);
    const oauthDisplay = buildMonitoringSourceDisplay(
      {
        authIndex: 'oauth-idx-1',
        source: 'codex-oauth.json',
        authLabelSnapshot: 'Stale OAuth Label',
        authProviderSnapshot: 'codex',
      },
      {
        authMetaMap: oauthMeta,
        channelByAuthIndex: new Map(),
        sourceInfoMap,
      }
    );
    expect(oauthDisplay.sourceLabel).toBe('OAuth Team Seat');
  });

  it('preserves historical non-generic snapshot account and label when an unmatched authIndex falls back to bySource', () => {
    const sourceInfoMap = buildSourceInfoMap({
      codexApiKeys: [
        {
          name: 'Current Codex Pool',
          prefix: 'shared-prefix',
          apiKey: 'sk-codex-live-11111111',
          authIndex: 'codex-live-idx',
        },
      ],
    });

    const deletedOAuthDisplay = buildMonitoringSourceDisplay(
      {
        authIndex: 'deleted-oauth-idx',
        source: 't:shared-prefix',
        accountSnapshot: 'alice@example.com',
        authLabelSnapshot: 'codex',
        authProviderSnapshot: 'codex',
        channel: 'codex',
      },
      {
        authMetaMap: new Map(),
        channelByAuthIndex: new Map(),
        sourceInfoMap,
      }
    );
    expect(deletedOAuthDisplay.primary).toBe('ali***@example.com');
    expect(deletedOAuthDisplay.accountMasked).toBe('ali***@example.com');

    const rotatedKeySnapshotDisplay = buildMonitoringSourceDisplay(
      {
        authIndex: 'rotated-out-key-idx',
        source: 't:shared-prefix',
        accountSnapshot: 'Legacy Dedicated Relay',
        authLabelSnapshot: 'Legacy Dedicated Relay',
        authProviderSnapshot: 'codex',
      },
      {
        authMetaMap: new Map(),
        channelByAuthIndex: new Map(),
        sourceInfoMap,
      }
    );
    expect(rotatedKeySnapshotDisplay.primary).toBe('Legacy Dedicated Relay');
    expect(rotatedKeySnapshotDisplay.sourceLabel).toBe('Legacy Dedicated Relay');
  });
});
