import { describe, expect, it } from 'vitest';
import { buildSourceInfoMap } from '@/utils/sourceResolver';
import {
  buildMonitoringSourceDisplay,
  isGenericMonitoringProviderLabel,
  isKeyDisambiguatedLabel,
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
    expect(isGenericMonitoringProviderLabel('anyrouter.top #1')).toBe(false);
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
});
