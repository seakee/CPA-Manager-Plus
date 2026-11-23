import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { MemoryRouter } from 'react-router-dom';
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
        <MemoryRouter>
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
        </MemoryRouter>
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
        <MemoryRouter>
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
        </MemoryRouter>
      );
    });

    const json = JSON.stringify(renderer.toJSON());
    expect(json).toContain('anthropic · Adopted (cpk_abcd****ef12)');
    await act(async () => {
      renderer.unmount();
    });
  });

  it('surfaces inline HTTP status code badges and drill-down links for channels and failures', async () => {
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <MemoryRouter>
          <HealthAlertsCard
            loading={false}
            recentFailures={[
              {
                timestamp_ms: Date.UTC(2026, 5, 1, 14, 0, 0),
                model: 'claude-sonnet-4-20250514',
                api_key_hash: '',
                source: 'anthropic',
                source_hash: '',
                auth_index: '',
                endpoint: 'POST /v1/messages',
                duration_ms: 420,
                fail_status_code: 429,
              },
              {
                timestamp_ms: Date.UTC(2026, 5, 1, 13, 30, 0),
                model: 'gpt-5.2-codex',
                api_key_hash: '',
                source: 'codex',
                source_hash: '',
                auth_index: '',
                endpoint: 'POST /v1/responses',
                duration_ms: 310,
              },
            ]}
            channelHealth={[
              {
                auth_index: 'auth-1',
                source: 'anthropic',
                calls: 20,
                failures: 1,
                failure_rate: 0.05,
                success_rate: 0.95,
                tokens: 1000,
                cost: 0.12,
                average_latency_ms: 210,
                tone: 'warn',
              },
            ]}
            authMetaMap={new Map()}
            authFileMap={new Map()}
            sourceInfoMap={buildSourceInfoMap({})}
            channelByAuthIndex={new Map()}
            apiKeyAliasMap={new Map()}
            apiKeyDisplayMap={new Map()}
          />
        </MemoryRouter>
      );
    });

    const links = renderer.root.findAllByType('a').map((node) => node.props.href);
    expect(links).toContain('/monitoring?tab=accounts');
    expect(links).toContain('/monitoring?tab=realtime');
    const json = JSON.stringify(renderer.toJSON());
    expect(json).toContain('429');
    expect(json).toContain('POST /v1/responses');
    expect(json).toContain('1/20');

    await act(async () => {
      renderer.unmount();
    });
  });
});
