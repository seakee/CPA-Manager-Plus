import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import { buildApiKeyDisplayMap, resolveApiKeyDisplayName } from '@/features/monitoring/model/apiKeys';
import { buildSourceInfoMap } from '@/utils/sourceResolver';
import { HealthAlertsCard } from './HealthAlertsCard';

(
  globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('react-i18next', () => ({
  initReactI18next: { type: '3rdParty', init: () => undefined },
  useTranslation: () => ({
    t: (key: string, options?: { defaultValue?: string }) => options?.defaultValue || key,
    i18n: { language: 'en' },
  }),
}));

describe('HealthAlertsCard', () => {
  it('displays client key human name and maskedPreview instead of shortHash for recent failures', async () => {
    const apiKeyDisplayMap = buildApiKeyDisplayMap(
      [],
      [
        {
          apiKeyHash: '0123456789abcdef0123456789abcdef',
          alias: 'Production',
          apiKeyId: 'key_prod',
          maskedPreview: 'cpamp_sk_…2f85',
          origin: 'workspace',
          lifecycle: 'active',
        },
        {
          apiKeyHash: 'fedcba9876543210fedcba9876543210',
          alias: '',
          apiKeyId: 'key_init',
          maskedPreview: 'sk-…1234',
          origin: 'installer',
          lifecycle: 'superseded',
        },
      ],
      'en'
    );
    const apiKeyAliasMap = new Map<string, string>();
    apiKeyDisplayMap.forEach((info, hash) => {
      const display = resolveApiKeyDisplayName(info, 'en');
      if (display) apiKeyAliasMap.set(hash, display);
    });

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <HealthAlertsCard
          loading={false}
          recentFailures={[
            {
              timestamp_ms: Date.UTC(2026, 5, 1, 12, 0, 0),
              model: 'gpt-4o',
              api_key_hash: '0123456789abcdef0123456789abcdef',
              source: 'openai',
              source_hash: '',
              auth_index: '',
              endpoint: 'POST /v1/chat/completions',
              duration_ms: 120,
            },
            {
              timestamp_ms: Date.UTC(2026, 5, 1, 11, 0, 0),
              model: 'gpt-4o',
              api_key_hash: 'fedcba9876543210fedcba9876543210',
              source: 'openai',
              source_hash: '',
              auth_index: '',
              endpoint: 'POST /v1/chat/completions',
              duration_ms: 90,
            },
          ]}
          channelHealth={[]}
          authMetaMap={new Map()}
          authFileMap={new Map()}
          sourceInfoMap={buildSourceInfoMap({})}
          channelByAuthIndex={new Map()}
          apiKeyAliasMap={apiKeyAliasMap}
          apiKeyDisplayMap={apiKeyDisplayMap}
        />
      );
    });

    const json = JSON.stringify(renderer.toJSON());
    expect(json).toContain('Production (cpamp_sk_…2f85)');
    expect(json).toContain('Installer (sk-…1234) · Revoked');
    expect(json).not.toContain('01234567');
    expect(json).not.toContain('fedcba98');

    await act(async () => {
      renderer.unmount();
    });
  });

  it('renders client key label inline beside upstream channel and preserves raw masked previews without double-masking', async () => {
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <HealthAlertsCard
          loading={false}
          recentFailures={[
            {
              timestamp_ms: Date.UTC(2026, 5, 1, 13, 0, 0),
              model: 'claude-3-7-sonnet',
              api_key_hash: 'aaaa1111bbbb2222cccc3333dddd4444',
              api_key_masked: 'cpk_abcd****ef12',
              api_key_origin: 'adopted',
              source: 'anthropic',
              source_hash: '',
              auth_index: '',
              endpoint: 'POST /v1/messages',
              duration_ms: 85,
            },
          ]}
          channelHealth={[]}
          authMetaMap={new Map()}
          authFileMap={new Map()}
          sourceInfoMap={buildSourceInfoMap({})}
          channelByAuthIndex={new Map()}
          apiKeyAliasMap={new Map()}
          apiKeyDisplayMap={new Map()}
        />
      );
    });

    const json = JSON.stringify(renderer.toJSON());
    expect(json).toContain('anthropic · Adopted (cpk_abcd****ef12)');
    await act(async () => {
      renderer.unmount();
    });
  });
});
