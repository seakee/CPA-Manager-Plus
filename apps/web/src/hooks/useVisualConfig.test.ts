import { act, createElement, createRef, useImperativeHandle, type Ref } from 'react';
import { create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it } from 'vitest';
import { parse as parseYaml } from 'yaml';
import { getCodexIdentityConfuseCompatibility, useVisualConfig } from './useVisualConfig';

type UseVisualConfigResult = ReturnType<typeof useVisualConfig>;
type VisualConfigRuntime = Parameters<typeof useVisualConfig>[0];

type UseVisualConfigHarness = {
  getCurrent: () => UseVisualConfigResult;
  unmount: () => void;
};

function HookHarness({
  hookRef,
  runtime,
}: {
  hookRef: Ref<UseVisualConfigResult>;
  runtime?: VisualConfigRuntime;
}) {
  const hook = useVisualConfig(runtime);
  useImperativeHandle(hookRef, () => hook, [hook]);
  return null;
}

const mountUseVisualConfig = (runtime?: VisualConfigRuntime): UseVisualConfigHarness => {
  const hookRef = createRef<UseVisualConfigResult>();
  let renderer: ReactTestRenderer | null = null;

  act(() => {
    renderer = create(createElement(HookHarness, { hookRef, runtime }));
  });

  return {
    getCurrent: () => {
      if (!hookRef.current) {
        throw new Error('Failed to mount useVisualConfig test harness');
      }
      return hookRef.current;
    },
    unmount: () => {
      if (!renderer) return;
      act(() => {
        renderer?.unmount();
      });
    },
  };
};

describe('useVisualConfig', () => {
  it('loads v8 client API keys separately from upstream provider credentials', () => {
    const harness = mountUseVisualConfig();
    const yaml = [
      'config-version: 8',
      'access:',
      '  api-keys: [sk-client-a, sk-client-b]',
      'api-keys:',
      '  gemini:',
      '    - keys:',
      '        - api-key: upstream-only',
      '',
    ].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
    });

    expect(harness.getCurrent().visualValues.apiKeysText).toBe('sk-client-a\nsk-client-b');
    expect(harness.getCurrent().visualDirty).toBe(false);
    expect(parseYaml(harness.getCurrent().applyVisualChangesToYaml(yaml))).toEqual(parseYaml(yaml));
    harness.unmount();
  });

  it.each([
    { value: '[sk-current]', expected: 'sk-current' },
    { value: '[]', expected: '' },
    { value: 'null', expected: '' },
  ])('treats access.api-keys: $value as authoritative over legacy keys', ({ value, expected }) => {
    const harness = mountUseVisualConfig();
    const yaml = [
      'access:',
      `  api-keys: ${value}`,
      'api-keys: [sk-stale]',
      'auth:',
      '  providers:',
      '    config-api-key:',
      '      api-key-entries: [sk-legacy]',
      '',
    ].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
    });

    expect(harness.getCurrent().visualValues.apiKeysText).toBe(expected);
    harness.unmount();
  });

  it('keeps the legacy client-key fallback when a v8 document omits access.api-keys', () => {
    const harness = mountUseVisualConfig();
    act(() => {
      expect(
        harness.getCurrent().loadVisualValuesFromYaml('config-version: 8\napi-keys: [sk-old]\n').ok
      ).toBe(true);
    });
    expect(harness.getCurrent().visualValues.apiKeysText).toBe('sk-old');
    harness.unmount();
  });

  it.each(['sk-replacement', ''])(
    'writes v8 client keys without replacing upstream groups: %s',
    (keys) => {
      const harness = mountUseVisualConfig();
      const yaml = [
        'config-version: 8',
        'access:',
        '  api-keys: [sk-old]',
        '  custom-setting: preserve-me',
        'api-keys:',
        '  gemini:',
        '    - keys:',
        '        - api-key: upstream-only',
        '',
      ].join('\n');

      act(() => {
        expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
        harness.getCurrent().setVisualValues({ apiKeysText: keys });
      });

      const updated = harness.getCurrent().applyVisualChangesToYaml(yaml);
      const parsed = parseYaml(updated);
      expect(parsed.access).toEqual({
        'api-keys': keys ? [keys] : [],
        'custom-setting': 'preserve-me',
      });
      expect(parsed['api-keys']).toEqual({ gemini: [{ keys: [{ 'api-key': 'upstream-only' }] }] });
      act(() => {
        expect(harness.getCurrent().loadVisualValuesFromYaml(updated).ok).toBe(true);
      });
      expect(harness.getCurrent().visualValues.apiKeysText).toBe(keys);
      harness.unmount();
    }
  );

  it('creates the v8 client-key path when only upstream API-key groups exist', () => {
    const harness = mountUseVisualConfig();
    const yaml = 'api-keys:\n  gemini:\n    - keys:\n        - api-key: upstream-only\n';
    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
      harness.getCurrent().setVisualValues({ apiKeysText: 'sk-new' });
    });
    const parsed = parseYaml(harness.getCurrent().applyVisualChangesToYaml(yaml));
    expect(parsed.access?.['api-keys']).toEqual(['sk-new']);
    expect(parsed['api-keys']).toEqual({ gemini: [{ keys: [{ 'api-key': 'upstream-only' }] }] });
    harness.unmount();
  });

  it('creates client keys on access.api-keys for a v8 layout with no existing key nodes', () => {
    const harness = mountUseVisualConfig();
    const yaml = 'server:\n  port: 8317\n';
    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
      harness.getCurrent().setVisualValues({ apiKeysText: 'sk-new' });
    });

    const parsed = parseYaml(harness.getCurrent().applyVisualChangesToYaml(yaml));
    expect(parsed.access?.['api-keys']).toEqual(['sk-new']);
    expect(parsed['api-keys']).toBeUndefined();
    harness.unmount();
  });

  it('clears canonical client keys without reviving stale legacy keys', () => {
    const harness = mountUseVisualConfig();
    const yaml = 'config-version: 8\naccess:\n  api-keys: [sk-current]\napi-keys: [sk-stale]\n';
    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
      harness.getCurrent().setVisualValues({ apiKeysText: '' });
    });
    const updated = harness.getCurrent().applyVisualChangesToYaml(yaml);
    const parsed = parseYaml(updated);
    expect(parsed.access['api-keys']).toEqual([]);
    expect(parsed['api-keys']).toBeUndefined();
    act(() => {
      harness.getCurrent().loadVisualValuesFromYaml(updated);
    });
    expect(harness.getCurrent().visualValues.apiKeysText).toBe('');
    harness.unmount();
  });

  it('does not resurrect a cleared sibling when another field recreates the same v8 parent', () => {
    const harness = mountUseVisualConfig();
    const yaml = [
      'observability:',
      '  logs:',
      '    logs-max-total-size-mb: 256',
      '',
    ].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
      harness.getCurrent().setVisualValues({
        logsMaxTotalSizeMb: '',
        errorLogsMaxFiles: '8',
      });
    });

    const parsed = parseYaml(harness.getCurrent().applyVisualChangesToYaml(yaml)) as {
      observability?: { logs?: Record<string, unknown> };
    };
    expect(parsed.observability?.logs?.['logs-max-total-size-mb']).toBeUndefined();
    expect(parsed.observability?.logs?.['error-logs-max-files']).toBe(8);
    harness.unmount();
  });

  it('overrides legacy root-merge scalars with explicit false, empty, and null defaults', () => {
    const harness = mountUseVisualConfig();
    const yaml = [
      'defaults: &legacy',
      '  debug: true',
      '  proxy-url: http://old.proxy',
      '  request-retry: 3',
      '<<: *legacy',
      '',
    ].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
      harness.getCurrent().setVisualValues({
        debug: false,
        proxyUrl: '',
        requestRetry: '',
      });
    });

    const updated = harness.getCurrent().applyVisualChangesToYaml(yaml);
    const parsed = parseYaml(updated, { merge: true }) as Record<string, unknown>;
    expect(parsed.debug).toBe(false);
    expect(parsed['proxy-url']).toBe('');
    expect(parsed['request-retry']).toBeNull();

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(updated).ok).toBe(true);
    });
    expect(harness.getCurrent().visualValues.debug).toBe(false);
    expect(harness.getCurrent().visualValues.proxyUrl).toBe('');
    expect(harness.getCurrent().visualValues.requestRetry).toBe('');
    harness.unmount();
  });

  it.each([
    {
      name: 'root merge',
      yaml: [
        'defaults: &root',
        '  api-keys:',
        '    gemini:',
        '      - keys:',
        '          - api-key: upstream-only',
        '<<: *root',
        '',
      ].join('\n'),
    },
    {
      name: 'direct alias',
      yaml: [
        'groups: &groups',
        '  gemini:',
        '    - keys:',
        '        - api-key: upstream-only',
        'api-keys: *groups',
        '',
      ].join('\n'),
    },
  ])('preserves effective upstream API-key groups provided through $name', ({ yaml }) => {
    const harness = mountUseVisualConfig();

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
      harness.getCurrent().setVisualValues({ apiKeysText: 'sk-client' });
    });

    const updated = harness.getCurrent().applyVisualChangesToYaml(yaml);
    const parsed = parseYaml(updated, { merge: true }) as {
      access?: { 'api-keys'?: string[] };
      'api-keys'?: Record<string, unknown>;
    };
    expect(parsed.access?.['api-keys']).toEqual(['sk-client']);
    expect(parsed['api-keys']).toEqual({
      gemini: [{ keys: [{ 'api-key': 'upstream-only' }] }],
    });
    harness.unmount();
  });

  it('loads CPA v8 canonical paths across existing visual config groups', () => {
    const harness = mountUseVisualConfig();
    const yaml = [
      'config-version: 8',
      'server:',
      '  host: 0.0.0.0',
      '  port: 9443',
      '  tls:',
      '    enable: true',
      '    cert: /cert.pem',
      '    key: /key.pem',
      '  commercial-mode: true',
      'management:',
      '  allow-remote: true',
      "  secret-key: '$2a$10$existing'",
      '  disable-control-panel: true',
      '  disable-auto-update-panel: true',
      '  panel-github-repository: https://github.com/seakee/CPA-Manager-Plus',
      'oauth:',
      '  auth-dir: /data/auth',
      '  auth-auto-refresh-workers: 4',
      '  providers:',
      '    aistudio:',
      '      ws-auth: false',
      '    antigravity:',
      '      signature-cache-enabled: false',
      '      signature-bypass-strict: true',
      '      antigravity-credits: true',
      '    codex:',
      '      header-defaults:',
      '        user-agent: codex-test',
      '        beta-features: feature-a',
      '    devin:',
      '      sensitive-words: [alpha, beta]',
      'upstream:',
      '  claude:',
      '    disable-claude-cloak-mode: true',
      '    header-defaults:',
      '      user-agent: claude-test',
      '      package-version: 2.1.0',
      '      stabilize-device-profile: true',
      'observability:',
      '  logs:',
      '    debug: true',
      '    logging-to-file: true',
      '    request-log: true',
      '    logs-max-total-size-mb: 256',
      '    error-logs-max-files: 8',
      '  usage:',
      '    usage-statistics-enabled: true',
      '    redis-usage-queue-retention-seconds: 120',
      '  pprof:',
      '    enable: true',
      '    addr: 127.0.0.1:9316',
      'requests:',
      '  proxy-url: http://proxy.local:8080',
      '  passthrough-headers: true',
      '  nonstream-keepalive-interval: 9',
      '  streaming:',
      '    keepalive-seconds: 15',
      '    bootstrap-retries: 2',
      '  payload:',
      '    default: []',
      'routing:',
      '  strategy: weighted-round-robin',
      '  session-affinity: true',
      '  session-affinity-ttl: 2h',
      '  force-model-prefix: true',
      '  retry:',
      '    request-retry: 4',
      '    max-retry-credentials: 5',
      '    max-retry-interval: 6',
      '  cooldown:',
      '    disable-cooling: true',
      '    save-cooldown-status: true',
      '    transient-error-cooldown-seconds: 7',
      'multimedia:',
      '  disable-image-generation: chat',
      '  gpt-image-2-base-model: gpt-image-test',
      '  video-result-auth-cache-ttl: 45m',
      'quota-exceeded:',
      '  switch-project: true',
      '  switch-preview-model: true',
      '',
    ].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
    });

    expect(harness.getCurrent().visualValues).toEqual(
      expect.objectContaining({
        host: '0.0.0.0',
        port: '9443',
        tlsEnable: true,
        tlsCert: '/cert.pem',
        tlsKey: '/key.pem',
        commercialMode: true,
        rmAllowRemote: true,
        rmSecretKeyConfigured: true,
        rmDisableControlPanel: true,
        rmDisableAutoUpdatePanel: true,
        rmPanelRepo: 'https://github.com/seakee/CPA-Manager-Plus',
        authDir: '/data/auth',
        authAutoRefreshWorkers: '4',
        debug: true,
        loggingToFile: true,
        requestLog: true,
        logsMaxTotalSizeMb: '256',
        errorLogsMaxFiles: '8',
        usageStatisticsEnabled: true,
        redisUsageQueueRetentionSeconds: '120',
        pprofEnable: true,
        pprofAddr: '127.0.0.1:9316',
        proxyUrl: 'http://proxy.local:8080',
        passthroughHeaders: true,
        forceModelPrefix: true,
        requestRetry: '4',
        maxRetryCredentials: '5',
        maxRetryInterval: '6',
        disableCooling: true,
        saveCooldownStatus: true,
        transientErrorCooldownSeconds: '7',
        disableClaudeCloakMode: true,
        disableImageGeneration: 'chat',
        gptImage2BaseModel: 'gpt-image-test',
        videoResultAuthCacheTtl: '45m',
        wsAuth: false,
        antigravitySignatureCacheEnabled: false,
        antigravitySignatureBypassStrict: true,
        quotaAntigravityCredits: true,
        claudeHeaderUserAgent: 'claude-test',
        claudeHeaderPackageVersion: '2.1.0',
        claudeHeaderStabilizeDeviceProfile: true,
        codexHeaderUserAgent: 'codex-test',
        codexHeaderBetaFeatures: 'feature-a',
        codexIdentityConfuse: false,
        codexIdentityConfuseSupported: false,
        devinSensitiveWords: ['alpha', 'beta'],
        routingStrategy: 'weighted-round-robin',
        routingSessionAffinity: true,
        routingSessionAffinityTTL: '2h',
        quotaSwitchProject: true,
        quotaSwitchPreviewModel: true,
        streaming: {
          keepaliveSeconds: '15',
          bootstrapRetries: '2',
          nonstreamKeepaliveInterval: '9',
        },
      })
    );
    harness.unmount();
  });

  it('resolves partial mixed v8 structs leaf by leaf', () => {
    const harness = mountUseVisualConfig();
    const hash = '$2a$10$legacy-sibling-hash';
    const yaml = [
      'management:',
      '  allow-remote: false',
      'remote-management:',
      `  secret-key: '${hash}'`,
      '  disable-control-panel: true',
      'server:',
      '  tls:',
      '    enable: false',
      'tls:',
      '  cert: /legacy-cert.pem',
      '  key: /legacy-key.pem',
      'observability:',
      '  pprof:',
      '    enable: false',
      'pprof:',
      '  addr: 127.0.0.1:9316',
      'requests:',
      '  streaming:',
      '    keepalive-seconds: 15',
      '  payload:',
      '    default: []',
      'streaming:',
      '  bootstrap-retries: 3',
      'payload:',
      '  filter:',
      '    - models: [legacy-model]',
      '      params: [temperature]',
      '',
    ].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
    });

    expect(harness.getCurrent().visualValues).toEqual(
      expect.objectContaining({
        rmAllowRemote: false,
        rmSecretKeyConfigured: true,
        rmDisableControlPanel: true,
        tlsEnable: false,
        tlsCert: '/legacy-cert.pem',
        tlsKey: '/legacy-key.pem',
        pprofEnable: false,
        pprofAddr: '127.0.0.1:9316',
        streaming: expect.objectContaining({
          keepaliveSeconds: '15',
          bootstrapRetries: '3',
        }),
      })
    );
    expect(harness.getCurrent().visualValues.payloadFilterRules).toHaveLength(1);

    act(() => {
      harness.getCurrent().setVisualValues({ rmAllowRemote: true });
    });
    const updatedYaml = harness.getCurrent().applyVisualChangesToYaml(yaml);
    const updated = parseYaml(updatedYaml) as {
      management?: Record<string, unknown>;
      'remote-management'?: Record<string, unknown>;
    };
    expect(updated.management?.['allow-remote']).toBe(true);
    expect(updated['remote-management']?.['secret-key']).toBe(hash);
    expect(updated['remote-management']?.['disable-control-panel']).toBe(true);

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(updatedYaml).ok).toBe(true);
    });
    expect(harness.getCurrent().visualValues.rmSecretKeyConfigured).toBe(true);
    expect(harness.getCurrent().visualValues.rmDisableControlPanel).toBe(true);
    harness.unmount();
  });

  it('gives explicit v8 values precedence and preserves legacy sources until CPA migration', () => {
    const harness = mountUseVisualConfig();
    const yaml = [
      'config-version: 8',
      'management:',
      '  allow-remote: false',
      'remote-management:',
      '  allow-remote: true',
      'observability:',
      '  usage:',
      '    usage-statistics-enabled: false',
      'usage-statistics-enabled: true',
      'requests:',
      "  proxy-url: ''",
      'proxy-url: http://stale.proxy',
      'routing:',
      '  retry:',
      '    request-retry: 0',
      'request-retry: 9',
      '',
    ].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
    });
    expect(harness.getCurrent().visualValues.rmAllowRemote).toBe(false);
    expect(harness.getCurrent().visualValues.usageStatisticsEnabled).toBe(false);
    expect(harness.getCurrent().visualValues.proxyUrl).toBe('');
    expect(harness.getCurrent().visualValues.requestRetry).toBe('0');
    harness.unmount();

    const writeHarness = mountUseVisualConfig();
    const legacyFallbacks = [
      'config-version: 8',
      'remote-management:',
      '  allow-remote: true',
      'usage-statistics-enabled: true',
      'proxy-url: http://legacy.proxy',
      'request-retry: 3',
      '',
    ].join('\n');

    act(() => {
      expect(writeHarness.getCurrent().loadVisualValuesFromYaml(legacyFallbacks).ok).toBe(true);
      writeHarness.getCurrent().setVisualValues({
        rmAllowRemote: false,
        usageStatisticsEnabled: false,
        proxyUrl: '',
        requestRetry: '0',
      });
    });

    const updated = writeHarness.getCurrent().applyVisualChangesToYaml(legacyFallbacks);
    const parsed = parseYaml(updated) as {
      management?: Record<string, unknown>;
      observability?: Record<string, unknown>;
      requests?: Record<string, unknown>;
      routing?: Record<string, unknown>;
      'remote-management'?: Record<string, unknown>;
      'usage-statistics-enabled'?: unknown;
      'proxy-url'?: unknown;
      'request-retry'?: unknown;
    };
    expect(parsed['remote-management']).toEqual({ 'allow-remote': false });
    expect(parsed['usage-statistics-enabled']).toBe(false);
    expect(parsed['proxy-url']).toBe('');
    expect(parsed['request-retry']).toBe(0);
    expect(parsed.management).toBeUndefined();
    expect(parsed.observability).toBeUndefined();
    expect(parsed.requests).toBeUndefined();
    expect(parsed.routing).toBeUndefined();

    act(() => {
      expect(writeHarness.getCurrent().loadVisualValuesFromYaml(updated).ok).toBe(true);
    });
    expect(writeHarness.getCurrent().visualValues.rmAllowRemote).toBe(false);
    expect(writeHarness.getCurrent().visualValues.usageStatisticsEnabled).toBe(false);
    expect(writeHarness.getCurrent().visualValues.proxyUrl).toBe('');
    expect(writeHarness.getCurrent().visualValues.requestRetry).toBe('0');
    writeHarness.unmount();
  });

  it('does not treat config-version 8 alone as a v8 layout', () => {
    const harness = mountUseVisualConfig();
    const yaml = [
      'config-version: 8',
      'request-retry: 4',
      'remote-management:',
      '  allow-remote: true',
      '',
    ].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
      harness.getCurrent().setVisualValues({ debug: true });
    });

    const parsed = parseYaml(harness.getCurrent().applyVisualChangesToYaml(yaml)) as {
      debug?: boolean;
      observability?: unknown;
      'request-retry'?: number;
      'remote-management'?: Record<string, unknown>;
    };
    expect(parsed.debug).toBe(true);
    expect(parsed.observability).toBeUndefined();
    expect(parsed['request-retry']).toBe(4);
    expect(parsed['remote-management']?.['allow-remote']).toBe(true);
    harness.unmount();
  });

  it('reads historical Claude v8 aliases with canonical then historical then legacy precedence', () => {
    const harness = mountUseVisualConfig();
    const yaml = [
      'upstream:',
      '  claude:',
      '    disable-claude-cloak-mode: false',
      '    header-defaults:',
      '      user-agent: canonical-agent',
      'oauth:',
      '  providers:',
      '    claude:',
      '      disable-claude-cloak-mode: true',
      '      header-defaults:',
      '        user-agent: historical-agent',
      '        package-version: historical-package',
      '        os: historical-os',
      'claude-header-defaults:',
      '  user-agent: legacy-agent',
      '  package-version: legacy-package',
      '  runtime-version: legacy-runtime',
      '  os: legacy-os',
      'disable-claude-cloak-mode: true',
      '',
    ].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
    });

    expect(harness.getCurrent().visualValues.disableClaudeCloakMode).toBe(false);
    expect(harness.getCurrent().visualValues.claudeHeaderUserAgent).toBe('canonical-agent');
    expect(harness.getCurrent().visualValues.claudeHeaderPackageVersion).toBe(
      'historical-package'
    );
    expect(harness.getCurrent().visualValues.claudeHeaderOs).toBe('historical-os');
    expect(harness.getCurrent().visualValues.claudeHeaderRuntimeVersion).toBe('legacy-runtime');
    harness.unmount();

    const historicalOnly = mountUseVisualConfig();
    const historicalYaml = [
      'oauth:',
      '  providers:',
      '    claude:',
      '      disable-claude-cloak-mode: true',
      '      header-defaults:',
      '        user-agent: historical-only',
      '        stabilize-device-profile: false',
      '',
    ].join('\n');

    act(() => {
      expect(historicalOnly.getCurrent().loadVisualValuesFromYaml(historicalYaml).ok).toBe(true);
    });
    expect(historicalOnly.getCurrent().visualValues.disableClaudeCloakMode).toBe(true);
    expect(historicalOnly.getCurrent().visualValues.claudeHeaderUserAgent).toBe(
      'historical-only'
    );
    expect(
      historicalOnly.getCurrent().visualValues.claudeHeaderStabilizeDeviceProfile
    ).toBe(false);

    act(() => {
      historicalOnly.getCurrent().setVisualValues({
        disableClaudeCloakMode: false,
        claudeHeaderUserAgent: '',
      });
    });
    const historicalUpdated = parseYaml(
      historicalOnly.getCurrent().applyVisualChangesToYaml(historicalYaml)
    ) as {
      upstream?: {
        claude?: {
          'disable-claude-cloak-mode'?: boolean;
          'header-defaults'?: Record<string, unknown>;
        };
      };
      oauth?: {
        providers?: {
          claude?: {
            'disable-claude-cloak-mode'?: unknown;
            'header-defaults'?: Record<string, unknown>;
          };
        };
      };
    };
    expect(historicalUpdated.upstream?.claude?.['disable-claude-cloak-mode']).toBe(false);
    expect(historicalUpdated.upstream?.claude?.['header-defaults']?.['user-agent']).toBe('');
    expect(
      historicalUpdated.oauth?.providers?.claude?.['disable-claude-cloak-mode']
    ).toBeUndefined();
    expect(
      historicalUpdated.oauth?.providers?.claude?.['header-defaults']?.['user-agent']
    ).toBeUndefined();
    expect(
      historicalUpdated.oauth?.providers?.claude?.['header-defaults']?.[
        'stabilize-device-profile'
      ]
    ).toBe(false);
    historicalOnly.unmount();
  });

  it('materializes only the edited aliased management branch and preserves inherited siblings', () => {
    const harness = mountUseVisualConfig();
    const hash = '$2a$10$aliased-management-hash';
    const yaml = [
      'defaults: &management',
      `  secret-key: '${hash}'`,
      '  allow-remote: true',
      '  disable-control-panel: true',
      'management: *management',
      'other: *management',
      '',
    ].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
      harness.getCurrent().setVisualValues({ rmAllowRemote: false });
    });

    const updated = harness.getCurrent().applyVisualChangesToYaml(yaml);
    const effective = parseYaml(updated, { merge: true }) as {
      management?: Record<string, unknown>;
      other?: Record<string, unknown>;
    };

    expect(effective.management?.['secret-key']).toBe(hash);
    expect(effective.management?.['allow-remote']).toBe(false);
    expect(effective.management?.['disable-control-panel']).toBe(true);
    expect(effective.other?.['allow-remote']).toBe(true);
    expect(updated).toContain('other: *management');
    harness.unmount();
  });

  it('reads merge-key inheritance and can clear an inherited management secret safely', () => {
    const harness = mountUseVisualConfig();
    const hash = '$2a$10$merged-management-hash';
    const yaml = [
      'defaults: &management',
      `  secret-key: '${hash}'`,
      '  allow-remote: true',
      '  disable-control-panel: true',
      'management:',
      '  <<: *management',
      '  allow-remote: false',
      '',
    ].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
    });
    expect(harness.getCurrent().visualValues.rmSecretKeyConfigured).toBe(true);
    expect(harness.getCurrent().visualValues.rmAllowRemote).toBe(false);
    expect(harness.getCurrent().visualValues.rmDisableControlPanel).toBe(true);

    act(() => {
      harness.getCurrent().setVisualValues({ rmSecretKey: '', rmSecretKeyAction: 'clear' });
    });
    const updated = harness.getCurrent().applyVisualChangesToYaml(yaml);
    const effective = parseYaml(updated, { merge: true }) as {
      management?: Record<string, unknown>;
    };
    expect(effective.management?.['secret-key']).toBe('');
    expect(effective.management?.['allow-remote']).toBe(false);
    expect(effective.management?.['disable-control-panel']).toBe(true);
    harness.unmount();
  });

  it('detects v8 canonical paths inherited through a root merge before writing', () => {
    const harness = mountUseVisualConfig();
    const yaml = [
      'defaults: &root',
      '  requests:',
      '    proxy-url: http://old.proxy',
      '  observability:',
      '    usage:',
      '      usage-statistics-enabled: true',
      '<<: *root',
      '',
    ].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
    });
    expect(harness.getCurrent().visualValues.proxyUrl).toBe('http://old.proxy');
    expect(harness.getCurrent().visualValues.usageStatisticsEnabled).toBe(true);

    act(() => {
      harness.getCurrent().setVisualValues({ proxyUrl: 'http://new.proxy' });
    });
    const updated = harness.getCurrent().applyVisualChangesToYaml(yaml);
    const effective = parseYaml(updated, { merge: true }) as {
      requests?: Record<string, unknown>;
      'proxy-url'?: unknown;
    };
    expect(effective.requests?.['proxy-url']).toBe('http://new.proxy');
    expect(effective['proxy-url']).toBeUndefined();
    harness.unmount();
  });

  it('removes an inherited merged payload leaf instead of letting it reappear', () => {
    const harness = mountUseVisualConfig();
    const yaml = [
      'payload-defaults: &payload',
      '  filter:',
      '    - models: [legacy-model]',
      '      params: [temperature]',
      'requests:',
      '  payload:',
      '    <<: *payload',
      '',
    ].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
    });
    expect(harness.getCurrent().visualValues.payloadFilterRules).toHaveLength(1);

    act(() => {
      harness.getCurrent().setVisualValues({ payloadFilterRules: [] });
    });

    const updated = harness.getCurrent().applyVisualChangesToYaml(yaml);
    const effective = parseYaml(updated, { merge: true }) as {
      requests?: { payload?: Record<string, unknown> };
    };
    expect(effective.requests?.payload?.filter).toBeUndefined();
    harness.unmount();
  });

  it('preserves an unedited scalar alias while changing a sibling management field', () => {
    const harness = mountUseVisualConfig();
    const hash = '$2a$10$scalar-alias-hash';
    const yaml = [
      `password: &password '${hash}'`,
      'management:',
      '  secret-key: *password',
      '  allow-remote: true',
      '',
    ].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
      harness.getCurrent().setVisualValues({ rmAllowRemote: false });
    });

    const updated = harness.getCurrent().applyVisualChangesToYaml(yaml);
    const effective = parseYaml(updated, { merge: true }) as {
      management?: Record<string, unknown>;
    };
    expect(effective.management?.['secret-key']).toBe(hash);
    expect(effective.management?.['allow-remote']).toBe(false);
    expect(updated).toContain('secret-key: *password');
    harness.unmount();
  });

  it('keeps v8 management secrets on the canonical management path', () => {
    const harness = mountUseVisualConfig();
    const hash = '$2a$10$existing-management-hash';
    const yaml = [
      'config-version: 8',
      'management:',
      `  secret-key: '${hash}'`,
      '  allow-remote: false',
      '',
    ].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
      harness.getCurrent().setVisualValues({ rmAllowRemote: true });
    });

    const updated = harness.getCurrent().applyVisualChangesToYaml(yaml);
    const parsed = parseYaml(updated) as {
      management?: Record<string, unknown>;
      'remote-management'?: Record<string, unknown>;
    };
    expect(parsed.management?.['secret-key']).toBe(hash);
    expect(parsed.management?.['allow-remote']).toBe(true);
    expect(parsed['remote-management']).toBeUndefined();

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(updated).ok).toBe(true);
      harness.getCurrent().setVisualValues({ rmSecretKey: '', rmSecretKeyAction: 'clear' });
    });

    const cleared = parseYaml(harness.getCurrent().applyVisualChangesToYaml(updated)) as {
      management?: Record<string, unknown>;
    };
    expect(cleared.management?.['secret-key']).toBe('');
    harness.unmount();
  });

  it('clears the page dirty state when API keys are the only changed field', () => {
    const harness = mountUseVisualConfig();
    const initialYaml = ['proxy-url: http://proxy.local:8080', 'api-keys:', '  - old-key', ''].join(
      '\n'
    );

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(initialYaml).ok).toBe(true);
      harness.getCurrent().setVisualValues({ apiKeysText: 'old-key\nnew-key' });
    });
    expect(harness.getCurrent().visualDirty).toBe(true);

    act(() => {
      harness.getCurrent().commitApiKeysText('old-key\nnew-key');
    });

    expect(harness.getCurrent().visualDirty).toBe(false);
    expect(harness.getCurrent().applyVisualChangesToYaml(initialYaml)).toBe(
      initialYaml
    );
    harness.unmount();
  });

  it('applies ordinary Visual changes to latest YAML without overwriting an immediate API key', () => {
    const harness = mountUseVisualConfig();
    const initialYaml = ['proxy-url: http://proxy.local:8080', 'api-keys:', '  - old-key', ''].join(
      '\n'
    );
    const latestYaml = [
      'proxy-url: http://proxy.local:8080',
      'api-keys:',
      '  - old-key',
      '  - new-key',
      '',
    ].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(initialYaml).ok).toBe(true);
      harness.getCurrent().setVisualValues({ proxyUrl: 'http://next-proxy.local:8080' });
      harness.getCurrent().commitApiKeysText('old-key\nnew-key');
    });

    const parsed = parseYaml(
      harness.getCurrent().applyVisualChangesToYaml(latestYaml)
    ) as { ['api-keys']?: string[]; ['proxy-url']?: string };
    expect(parsed['proxy-url']).toBe('http://next-proxy.local:8080');
    expect(parsed['api-keys']).toEqual(['old-key', 'new-key']);
    expect(harness.getCurrent().visualDirty).toBe(true);
    harness.unmount();
  });

  it('commits only API keys while preserving other visual dirty fields', () => {
    const harness = mountUseVisualConfig();
    const yaml = ['proxy-url: http://proxy.local:8080', 'api-keys:', '  - old-key', ''].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
      harness.getCurrent().setVisualValues({
        proxyUrl: 'http://next-proxy.local:8080',
        apiKeysText: 'old-key\nnew-key',
      });
    });

    expect(harness.getCurrent().visualDirty).toBe(true);

    act(() => {
      harness.getCurrent().commitApiKeysText('old-key\nnew-key');
    });

    expect(harness.getCurrent().visualValues.apiKeysText).toBe('old-key\nnew-key');
    expect(harness.getCurrent().visualValues.proxyUrl).toBe('http://next-proxy.local:8080');
    expect(harness.getCurrent().visualDirty).toBe(true);

    act(() => {
      harness.getCurrent().setVisualValues({ proxyUrl: 'http://proxy.local:8080' });
    });

    expect(harness.getCurrent().visualDirty).toBe(false);
    harness.unmount();
  });

  it('loads CPA weighted routing aliases and writes the canonical strategy', () => {
    const harness = mountUseVisualConfig();
    const yaml = ['routing:', '  strategy: wrr', ''].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
    });
    expect(harness.getCurrent().visualValues.routingStrategy).toBe('weighted-round-robin');
    harness.unmount();

    const writeHarness = mountUseVisualConfig();
    const roundRobinYaml = ['routing:', '  strategy: round-robin', ''].join('\n');

    act(() => {
      expect(writeHarness.getCurrent().loadVisualValuesFromYaml(roundRobinYaml).ok).toBe(true);
      writeHarness.getCurrent().setVisualValues({ routingStrategy: 'weighted-round-robin' });
    });

    const parsed = parseYaml(
      writeHarness.getCurrent().applyVisualChangesToYaml(roundRobinYaml)
    ) as {
      routing?: { strategy?: string };
    };
    expect(parsed.routing?.strategy).toBe('weighted-round-robin');
    writeHarness.unmount();
  });

  it('loads plugin system state from plugins.enabled', () => {
    const harness = mountUseVisualConfig();
    const yaml = ['plugins:', '  enabled: true', ''].join('\n');

    act(() => {
      const result = harness.getCurrent().loadVisualValuesFromYaml(yaml);
      expect(result.ok).toBe(true);
    });

    expect(harness.getCurrent().visualValues.pluginsEnabled).toBe(true);
    harness.unmount();
  });

  it('loads plugin directory and store sources from plugins config', () => {
    const harness = mountUseVisualConfig();
    const yaml = [
      'plugins:',
      '  enabled: true',
      '  dir: /data/cpa/plugins',
      '  store-sources:',
      '    - https://plugins.example.com/official.json',
      '    - https://plugins.example.com/private.json',
      '',
    ].join('\n');

    act(() => {
      const result = harness.getCurrent().loadVisualValuesFromYaml(yaml);
      expect(result.ok).toBe(true);
    });

    expect(harness.getCurrent().visualValues.pluginsEnabled).toBe(true);
    expect(harness.getCurrent().visualValues.pluginsDir).toBe('/data/cpa/plugins');
    expect(harness.getCurrent().visualValues.pluginStoreSourcesText).toBe(
      [
        'https://plugins.example.com/official.json',
        'https://plugins.example.com/private.json',
      ].join('\n')
    );

    harness.unmount();
  });

  it('loads plugin store auth rules from plugins config', () => {
    const harness = mountUseVisualConfig();
    const yaml = [
      'plugins:',
      '  store-auth:',
      '    - match: https://api.github.com/repos/acme/private/releases/',
      '      apply-to:',
      '        - metadata',
      '        - artifact',
      '      type: github-token',
      '      token-env: GITHUB_TOKEN',
      '      allow-insecure: true',
      '',
    ].join('\n');

    act(() => {
      const result = harness.getCurrent().loadVisualValuesFromYaml(yaml);
      expect(result.ok).toBe(true);
    });

    expect(harness.getCurrent().visualValues.pluginStoreAuth).toEqual([
      expect.objectContaining({
        match: 'https://api.github.com/repos/acme/private/releases/',
        applyTo: ['metadata', 'artifact'],
        type: 'github-token',
        tokenEnv: 'GITHUB_TOKEN',
        allowInsecure: true,
      }),
    ]);

    harness.unmount();
  });

  it('writes plugins.enabled when enabling plugin system from visual editor', () => {
    const harness = mountUseVisualConfig();
    const yaml = ['host: 127.0.0.1', ''].join('\n');

    act(() => {
      const result = harness.getCurrent().loadVisualValuesFromYaml(yaml);
      expect(result.ok).toBe(true);
    });

    act(() => {
      harness.getCurrent().setVisualValues({ pluginsEnabled: true });
    });

    const savedYaml = harness.getCurrent().applyVisualChangesToYaml(yaml);
    expect(savedYaml).toContain('plugins:');
    expect(savedYaml).toContain('enabled: true');

    harness.unmount();
  });

  it('loads and writes request logging through the visual config editor', () => {
    const harness = mountUseVisualConfig();
    const yaml = ['request-log: true', 'logging-to-file: false', ''].join('\n');

    act(() => {
      const result = harness.getCurrent().loadVisualValuesFromYaml(yaml);
      expect(result.ok).toBe(true);
    });
    expect(harness.getCurrent().visualValues.requestLog).toBe(true);

    act(() => {
      harness.getCurrent().setVisualValues({ requestLog: false });
    });

    const parsed = parseYaml(harness.getCurrent().applyVisualChangesToYaml(yaml)) as Record<
      string,
      unknown
    >;
    expect(parsed['request-log']).toBe(false);
    expect(parsed['logging-to-file']).toBe(false);

    harness.unmount();
  });

  it('writes plugin directory and store sources while preserving plugin configs', () => {
    const harness = mountUseVisualConfig();
    const yaml = ['plugins:', '  configs:', '    demo:', '      enabled: true', ''].join('\n');

    act(() => {
      const result = harness.getCurrent().loadVisualValuesFromYaml(yaml);
      expect(result.ok).toBe(true);
    });

    act(() => {
      harness.getCurrent().setVisualValues({
        pluginsDir: '/opt/cpa/plugins',
        pluginStoreSourcesText: [
          'https://plugins.example.com/official.json',
          '',
          ' https://plugins.example.com/private.json ',
        ].join('\n'),
      });
    });

    const savedYaml = harness.getCurrent().applyVisualChangesToYaml(yaml);
    const parsed = parseYaml(savedYaml) as {
      plugins?: {
        dir?: string;
        'store-sources'?: string[];
        configs?: { demo?: { enabled?: boolean } };
      };
    };

    expect(parsed.plugins?.dir).toBe('/opt/cpa/plugins');
    expect(parsed.plugins?.['store-sources']).toEqual([
      'https://plugins.example.com/official.json',
      'https://plugins.example.com/private.json',
    ]);
    expect(parsed.plugins?.configs?.demo?.enabled).toBe(true);

    harness.unmount();
  });

  it('writes plugin store auth rules only after editing the auth field', () => {
    const harness = mountUseVisualConfig();
    const yaml = ['plugins:', '  configs:', '    demo:', '      enabled: true', ''].join('\n');

    act(() => {
      const result = harness.getCurrent().loadVisualValuesFromYaml(yaml);
      expect(result.ok).toBe(true);
    });

    const unchangedYaml = harness.getCurrent().applyVisualChangesToYaml(yaml);
    expect(parseYaml(unchangedYaml) as { plugins?: { 'store-auth'?: unknown } }).toEqual(
      expect.objectContaining({
        plugins: expect.not.objectContaining({ 'store-auth': expect.anything() }),
      })
    );

    act(() => {
      harness.getCurrent().setVisualValues({
        pluginStoreAuth: [
          {
            id: 'rule-1',
            match: 'https://downloads.example.com/private/',
            applyTo: ['artifact'],
            type: 'bearer',
            tokenEnv: 'PLUGIN_TOKEN',
            usernameEnv: '',
            passwordEnv: '',
            headerName: '',
            headerValueEnv: '',
            allowInsecure: false,
          },
        ],
      });
    });

    const savedYaml = harness.getCurrent().applyVisualChangesToYaml(yaml);
    const parsed = parseYaml(savedYaml) as {
      plugins?: {
        'store-auth'?: Array<Record<string, unknown>>;
        configs?: { demo?: { enabled?: boolean } };
      };
    };

    expect(parsed.plugins?.['store-auth']).toEqual([
      {
        match: 'https://downloads.example.com/private/',
        type: 'bearer',
        'apply-to': ['artifact'],
        'token-env': 'PLUGIN_TOKEN',
      },
    ]);
    expect(parsed.plugins?.configs?.demo?.enabled).toBe(true);

    harness.unmount();
  });

  it('clears plugin directory and store sources without removing plugin configs', () => {
    const harness = mountUseVisualConfig();
    const yaml = [
      'plugins:',
      '  dir: /opt/cpa/plugins',
      '  store-sources:',
      '    - https://plugins.example.com/official.json',
      '  configs:',
      '    demo:',
      '      enabled: true',
      '',
    ].join('\n');

    act(() => {
      const result = harness.getCurrent().loadVisualValuesFromYaml(yaml);
      expect(result.ok).toBe(true);
    });

    act(() => {
      harness.getCurrent().setVisualValues({
        pluginsDir: '',
        pluginStoreSourcesText: '',
      });
    });

    const savedYaml = harness.getCurrent().applyVisualChangesToYaml(yaml);
    const parsed = parseYaml(savedYaml) as {
      plugins?: {
        dir?: string;
        'store-sources'?: string[];
        configs?: { demo?: { enabled?: boolean } };
      };
    };

    expect(parsed.plugins?.dir).toBeUndefined();
    expect(parsed.plugins?.['store-sources']).toBeUndefined();
    expect(parsed.plugins?.configs?.demo?.enabled).toBe(true);

    harness.unmount();
  });

  it.each([
    ['v7.3.2', 'supported'],
    ['v8.0.0', 'supported'],
    ['v8.0.1', 'supported'],
    ['v8.0.2', 'supported'],
    ['v8.0.3', 'supported'],
    ['v8.0.4', 'unsupported'],
    ['v8.0.11', 'unsupported'],
    ['v8.0.3-0-gdeadbee', 'supported'],
    ['v8.0.3-1-g48686ccc', 'unsupported'],
    ['dev', 'unverified'],
  ] as const)(
    'detects Codex identity-confuse runtime support for %s',
    (serverVersion, expected) => {
      expect(getCodexIdentityConfuseCompatibility(serverVersion)).toBe(expected);
    }
  );

  it('treats the upstream removal commit as unsupported even without a release version', () => {
    expect(
      getCodexIdentityConfuseCompatibility('dev', '48686ccc8fbe898c2d048ac4815a7b2f1e409e27')
    ).toBe('unsupported');
  });

  it('keeps identity-confuse on the exact canonical path for early CPA v8 releases', () => {
    const harness = mountUseVisualConfig({ serverVersion: 'v8.0.3' });
    const yaml = [
      'oauth:',
      '  providers:',
      '    codex:',
      '      identity-confuse: false',
      '      header-defaults:',
      '        user-agent: codex-test',
      '',
    ].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
    });
    expect(harness.getCurrent().visualValues.codexIdentityConfuseSupported).toBe(true);
    expect(harness.getCurrent().visualValues.codexIdentityConfuse).toBe(false);

    act(() => {
      harness.getCurrent().setVisualValues({ codexIdentityConfuse: true });
    });

    const parsed = parseYaml(harness.getCurrent().applyVisualChangesToYaml(yaml)) as {
      oauth?: { providers?: { codex?: Record<string, unknown> } };
      codex?: unknown;
    };
    expect(parsed.oauth?.providers?.codex?.['identity-confuse']).toBe(true);
    expect(parsed.codex).toBeUndefined();
    harness.unmount();
  });

  it('does not write identity-confuse on CPA v8.0.4+ even when the YAML is legacy layout', () => {
    const harness = mountUseVisualConfig({ serverVersion: 'v8.0.4' });
    const yaml = [
      'host: 127.0.0.1',
      'codex:',
      '  identity-confuse: true',
      '  other-setting: kept',
      '',
    ].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
    });
    expect(harness.getCurrent().visualValues.codexIdentityConfuseSupported).toBe(false);
    expect(harness.getCurrent().visualValues.codexIdentityConfuse).toBe(false);

    act(() => {
      harness.getCurrent().setVisualValues({ codexIdentityConfuse: false });
    });

    const updated = harness.getCurrent().applyVisualChangesToYaml(yaml);
    expect(parseYaml(updated)).toEqual(parseYaml(yaml));
    harness.unmount();
  });

  it('does not write identity-confuse on CPA v8.0.4+ when stale canonical YAML contains it', () => {
    const harness = mountUseVisualConfig({ serverVersion: 'v8.0.11' });
    const yaml = [
      'oauth:',
      '  providers:',
      '    codex:',
      '      identity-confuse: true',
      '      header-defaults:',
      '        user-agent: codex-test',
      '',
    ].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
    });
    expect(harness.getCurrent().visualValues.codexIdentityConfuseSupported).toBe(false);
    expect(harness.getCurrent().visualValues.codexIdentityConfuse).toBe(false);

    act(() => {
      harness.getCurrent().setVisualValues({ codexIdentityConfuse: false });
    });

    const updated = harness.getCurrent().applyVisualChangesToYaml(yaml);
    expect(parseYaml(updated)).toEqual(parseYaml(yaml));
    harness.unmount();
  });

  it('does not expose or write the removed Codex identity-confuse option on current CPA v8', () => {
    const harness = mountUseVisualConfig({ serverVersion: 'v8.0.11' });
    const yaml = [
      'oauth:',
      '  providers:',
      '    codex:',
      '      header-defaults:',
      '        user-agent: codex-test',
      '',
    ].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
    });
    expect(harness.getCurrent().visualValues.codexIdentityConfuse).toBe(false);
    expect(harness.getCurrent().visualValues.codexIdentityConfuseSupported).toBe(false);

    act(() => {
      harness.getCurrent().setVisualValues({ codexIdentityConfuse: true });
    });
    const updated = harness.getCurrent().applyVisualChangesToYaml(yaml);
    expect(updated).not.toContain('identity-confuse');
    expect(updated).not.toContain('identityConfuse');
    harness.unmount();
  });

  it('clears camelCase codex identityConfuse when disabling on a supported legacy CPA', () => {
    const harness = mountUseVisualConfig({ serverVersion: 'v7.3.2' });
    const yaml = [
      'host: 127.0.0.1',
      'codex:',
      '  identityConfuse: true',
      '  other-setting: kept',
      '',
    ].join('\n');

    act(() => {
      const result = harness.getCurrent().loadVisualValuesFromYaml(yaml);
      expect(result.ok).toBe(true);
    });
    expect(harness.getCurrent().visualValues.codexIdentityConfuse).toBe(true);
    expect(harness.getCurrent().visualValues.codexIdentityConfuseSupported).toBe(true);

    act(() => {
      harness.getCurrent().setVisualValues({ codexIdentityConfuse: false });
    });

    const savedYaml = harness.getCurrent().applyVisualChangesToYaml(yaml);
    expect(savedYaml).not.toContain('identityConfuse: true');
    expect(savedYaml).not.toContain('identityConfuse:');
    expect(savedYaml).toContain('identity-confuse: false');
    expect(savedYaml).toContain('other-setting: kept');

    harness.unmount();
  });

  it('round-trips disable-image-generation passthrough without rewriting it', () => {
    const harness = mountUseVisualConfig();
    const yaml = ['disable-image-generation: passthrough', 'debug: false', ''].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
    });
    expect(harness.getCurrent().visualValues.disableImageGeneration).toBe('passthrough');

    act(() => {
      harness.getCurrent().setVisualValues({ debug: true });
    });
    const parsed = parseYaml(harness.getCurrent().applyVisualChangesToYaml(yaml)) as Record<
      string,
      unknown
    >;
    expect(parsed['disable-image-generation']).toBe('passthrough');
    expect(parsed.debug).toBe(true);

    harness.unmount();
  });

  it('applies only dirty visual fields to the latest server YAML', () => {
    const harness = mountUseVisualConfig();
    const originalYaml = ['debug: false', 'proxy-url: http://old-proxy.example', ''].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(originalYaml).ok).toBe(true);
      harness.getCurrent().setVisualValues({ proxyUrl: 'http://localhost:8080' });
    });

    const latestYaml = ['debug: true', 'proxy-url: http://old-proxy.example', ''].join('\n');
    const parsed = parseYaml(harness.getCurrent().applyVisualChangesToYaml(latestYaml)) as Record<
      string,
      unknown
    >;

    expect(parsed).toEqual({
      debug: true,
      'proxy-url': 'http://localhost:8080',
    });
    harness.unmount();
  });

  it('uses CPA defaults for absent quota and WebSocket auth fields', () => {
    const harness = mountUseVisualConfig();
    const yaml = ['host: 127.0.0.1', ''].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
    });

    expect(harness.getCurrent().visualValues.quotaSwitchProject).toBe(false);
    expect(harness.getCurrent().visualValues.quotaSwitchPreviewModel).toBe(false);
    expect(harness.getCurrent().visualValues.wsAuth).toBe(true);

    const parsed = parseYaml(harness.getCurrent().applyVisualChangesToYaml(yaml)) as Record<
      string,
      unknown
    >;
    expect(parsed['quota-exceeded']).toBeUndefined();
    expect(parsed['ws-auth']).toBeUndefined();

    harness.unmount();
  });

  it('writes only the quota option explicitly changed from an absent quota block', () => {
    const harness = mountUseVisualConfig();
    const yaml = ['host: 127.0.0.1', ''].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
      harness.getCurrent().setVisualValues({ quotaSwitchProject: true });
    });

    const parsed = parseYaml(harness.getCurrent().applyVisualChangesToYaml(yaml)) as {
      'quota-exceeded'?: Record<string, unknown>;
    };
    expect(parsed['quota-exceeded']).toEqual({ 'switch-project': true });

    harness.unmount();
  });

  it('writes ws-auth false when the user explicitly disables the CPA default', () => {
    const harness = mountUseVisualConfig();
    const yaml = ['host: 127.0.0.1', ''].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
      harness.getCurrent().setVisualValues({ wsAuth: false });
    });

    const parsed = parseYaml(harness.getCurrent().applyVisualChangesToYaml(yaml)) as Record<
      string,
      unknown
    >;
    expect(parsed['ws-auth']).toBe(false);

    harness.unmount();
  });

  it('rejects zero Redis usage retention because CPA normalizes it to 60', () => {
    const harness = mountUseVisualConfig();

    act(() => {
      harness.getCurrent().setVisualValues({ redisUsageQueueRetentionSeconds: '0' });
    });

    expect(
      harness.getCurrent().visualValidationErrors.redisUsageQueueRetentionSeconds
    ).toBe('retention_seconds_range');
    harness.unmount();
  });

  it('keeps an existing management key unchanged during unrelated visual edits', () => {
    const harness = mountUseVisualConfig();
    const hash = '$2a$10$01234567890123456789012345678901234567890123456789012';
    const yaml = [
      'remote-management:',
      `  secret-key: '${hash}'`,
      '  allow-remote: false',
      '',
    ].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
    });
    expect(harness.getCurrent().visualValues.rmSecretKey).toBe('');
    expect(harness.getCurrent().visualValues.rmSecretKeyAction).toBe('unchanged');
    expect(harness.getCurrent().visualValues.rmSecretKeyConfigured).toBe(true);

    act(() => {
      harness.getCurrent().setVisualValues({ rmAllowRemote: true });
    });
    const parsed = parseYaml(harness.getCurrent().applyVisualChangesToYaml(yaml)) as {
      'remote-management'?: Record<string, unknown>;
    };
    expect(parsed['remote-management']?.['secret-key']).toBe(hash);
    expect(parsed['remote-management']?.['allow-remote']).toBe(true);

    harness.unmount();
  });

  it('replaces a management key without trimming its bytes', () => {
    const harness = mountUseVisualConfig();
    const yaml = ['remote-management:', "  secret-key: '$2a$10$existing'", ''].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
      harness.getCurrent().setVisualValues({
        rmSecretKey: '  exact key  ',
        rmSecretKeyAction: 'replace',
      });
    });

    const parsed = parseYaml(harness.getCurrent().applyVisualChangesToYaml(yaml)) as {
      'remote-management'?: Record<string, unknown>;
    };
    expect(parsed['remote-management']?.['secret-key']).toBe('  exact key  ');

    harness.unmount();
  });

  it('does not clear an existing management key through an empty replacement', () => {
    const harness = mountUseVisualConfig();
    const hash = '$2a$10$existing';
    const yaml = ['remote-management:', `  secret-key: '${hash}'`, ''].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
      harness.getCurrent().setVisualValues({
        rmSecretKey: '',
        rmSecretKeyAction: 'replace',
      });
    });

    const parsed = parseYaml(harness.getCurrent().applyVisualChangesToYaml(yaml)) as {
      'remote-management'?: Record<string, unknown>;
    };
    expect(parsed['remote-management']?.['secret-key']).toBe(hash);

    harness.unmount();
  });

  it('explicitly clears the management key to disable the Management API', () => {
    const harness = mountUseVisualConfig();
    const yaml = ['remote-management:', "  secret-key: '$2a$10$existing'", ''].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
      harness.getCurrent().setVisualValues({ rmSecretKey: '', rmSecretKeyAction: 'clear' });
    });

    const parsed = parseYaml(harness.getCurrent().applyVisualChangesToYaml(yaml)) as {
      'remote-management'?: Record<string, unknown>;
    };
    expect(parsed['remote-management']?.['secret-key']).toBe('');

    harness.unmount();
  });

  it('loads and saves the added CPA runtime settings', () => {
    const harness = mountUseVisualConfig();
    const yaml = [
      'pprof:',
      '  enable: true',
      '  addr: 127.0.0.1:9316',
      'save-cooldown-status: true',
      'transient-error-cooldown-seconds: -1',
      'disable-claude-cloak-mode: true',
      'gpt-image-2-base-model: gpt-5.4',
      'video-result-auth-cache-ttl: 45m',
      '',
    ].join('\n');

    act(() => {
      expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
    });
    expect(harness.getCurrent().visualValues).toEqual(
      expect.objectContaining({
        pprofEnable: true,
        pprofAddr: '127.0.0.1:9316',
        saveCooldownStatus: true,
        transientErrorCooldownSeconds: '-1',
        disableClaudeCloakMode: true,
        gptImage2BaseModel: 'gpt-5.4',
        videoResultAuthCacheTtl: '45m',
      })
    );

    act(() => {
      harness.getCurrent().setVisualValues({
        pprofEnable: false,
        pprofAddr: '127.0.0.1:8316',
        saveCooldownStatus: false,
        transientErrorCooldownSeconds: '15',
        disableClaudeCloakMode: false,
        gptImage2BaseModel: 'gpt-5.4-mini',
        videoResultAuthCacheTtl: '3h',
      });
    });

    const parsed = parseYaml(harness.getCurrent().applyVisualChangesToYaml(yaml)) as Record<
      string,
      unknown
    >;
    expect(parsed.pprof).toEqual({ enable: false, addr: '127.0.0.1:8316' });
    expect(parsed['save-cooldown-status']).toBe(false);
    expect(parsed['transient-error-cooldown-seconds']).toBe(15);
    expect(parsed['disable-claude-cloak-mode']).toBe(false);
    expect(parsed['gpt-image-2-base-model']).toBe('gpt-5.4-mini');
    expect(parsed['video-result-auth-cache-ttl']).toBe('3h');

    harness.unmount();
  });

  describe('devin sensitive words', () => {
    it('parses devin.sensitive-words with trimming, filtering empty items, and preserving order', () => {
      const harness = mountUseVisualConfig();
      const yaml = [
        'devin:',
        '  sensitive-words:',
        '    - "  forbidden-token  "',
        '    - ""',
        '    - "   "',
        '    - "system prompt leak"',
        '    - "secret-key"',
        '',
      ].join('\n');

      act(() => {
        expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
      });

      expect(harness.getCurrent().visualValues.devinSensitiveWords).toEqual([
        'forbidden-token',
        'system prompt leak',
        'secret-key',
      ]);

      // Verify non-canonical keys are ignored
      const nonCanonicalYaml = [
        'devin:',
        '  sensitiveWords:',
        '    - "bad1"',
        'devin-sensitive-words:',
        '  - "bad2"',
        '',
      ].join('\n');

      act(() => {
        expect(harness.getCurrent().loadVisualValuesFromYaml(nonCanonicalYaml).ok).toBe(true);
      });
      expect(harness.getCurrent().visualValues.devinSensitiveWords).toEqual([]);

      harness.unmount();
    });

    it('canonically writes devin.sensitive-words into yaml', () => {
      const harness = mountUseVisualConfig();
      const initialYaml = ['port: 8080', ''].join('\n');

      act(() => {
        expect(harness.getCurrent().loadVisualValuesFromYaml(initialYaml).ok).toBe(true);
        harness.getCurrent().setVisualValues({
          devinSensitiveWords: ['word1', 'word2'],
        });
      });

      const resultYaml = harness.getCurrent().applyVisualChangesToYaml(initialYaml);
      const parsed = parseYaml(resultYaml) as Record<string, unknown>;
      expect(parsed.devin).toEqual({
        'sensitive-words': ['word1', 'word2'],
      });

      harness.unmount();
    });

    it('canonically writes devin.sensitive-words trimming items and dropping empty strings', () => {
      const harness = mountUseVisualConfig();
      const initialYaml = ['port: 8080', ''].join('\n');

      act(() => {
        expect(harness.getCurrent().loadVisualValuesFromYaml(initialYaml).ok).toBe(true);
        harness.getCurrent().setVisualValues({
          devinSensitiveWords: [' API ', '', 'Claude Code'],
        });
      });

      const resultYaml = harness.getCurrent().applyVisualChangesToYaml(initialYaml);
      const parsed = parseYaml(resultYaml) as Record<string, unknown>;
      expect(parsed.devin).toEqual({
        'sensitive-words': ['API', 'Claude Code'],
      });

      harness.unmount();
    });

    it('removes the devin map completely when clearing sensitive words and no other fields exist', () => {
      const harness = mountUseVisualConfig();
      const yaml = [
        'devin:',
        '  sensitive-words:',
        '    - secret',
        'port: 8080',
        '',
      ].join('\n');

      act(() => {
        expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
        harness.getCurrent().setVisualValues({
          devinSensitiveWords: [],
        });
      });

      const resultYaml = harness.getCurrent().applyVisualChangesToYaml(yaml);
      const parsed = parseYaml(resultYaml) as Record<string, unknown>;
      expect(parsed.devin).toBeUndefined();
      expect(parsed.port).toBe(8080);

      harness.unmount();
    });

    it('preserves unknown future sibling properties under devin when editing sensitive words', () => {
      const harness = mountUseVisualConfig();
      const yaml = [
        'devin:',
        '  sensitive-words:',
        '    - old-secret',
        '  future-option: true',
        '  nested-config:',
        '    feature-flag: enabled',
        'port: 8080',
        '',
      ].join('\n');

      act(() => {
        expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
        harness.getCurrent().setVisualValues({
          devinSensitiveWords: ['new-secret'],
        });
      });

      const resultYaml = harness.getCurrent().applyVisualChangesToYaml(yaml);
      const parsed = parseYaml(resultYaml) as Record<string, unknown>;
      expect(parsed.devin).toEqual({
        'sensitive-words': ['new-secret'],
        'future-option': true,
        'nested-config': {
          'feature-flag': 'enabled',
        },
      });

      harness.unmount();
    });

    it('preserves future sibling properties when clearing devin.sensitive-words', () => {
      const harness = mountUseVisualConfig();
      const yaml = [
        'devin:',
        '  sensitive-words:',
        '    - secret',
        '  future-option: "keep-me"',
        'port: 8080',
        '',
      ].join('\n');

      act(() => {
        expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
        harness.getCurrent().setVisualValues({
          devinSensitiveWords: [],
        });
      });

      const resultYaml = harness.getCurrent().applyVisualChangesToYaml(yaml);
      const parsed = parseYaml(resultYaml) as Record<string, unknown>;
      expect(parsed.devin).toEqual({
        'future-option': 'keep-me',
      });
      expect(parsed.port).toBe(8080);

      harness.unmount();
    });

    it('does not touch or modify the devin subtree on unrelated visual edits', () => {
      const harness = mountUseVisualConfig();
      const yaml = [
        '# Custom devin comment',
        'devin:',
        '  sensitive-words:',
        '    - do-not-touch',
        '  custom-flag: 123',
        'port: 8080',
        '',
      ].join('\n');

      act(() => {
        expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
        harness.getCurrent().setVisualValues({
          port: '9090',
        });
      });

      const resultYaml = harness.getCurrent().applyVisualChangesToYaml(yaml);
      expect(resultYaml).toContain('# Custom devin comment');
      expect(resultYaml).toContain('custom-flag: 123');
      const parsed = parseYaml(resultYaml) as Record<string, unknown>;
      expect(parsed.port).toBe(9090);
      expect(parsed.devin).toEqual({
        'sensitive-words': ['do-not-touch'],
        'custom-flag': 123,
      });

      harness.unmount();
    });

    it('tracks the dirty lifecycle accurately for devinSensitiveWords', () => {
      const harness = mountUseVisualConfig();
      const yaml = [
        'devin:',
        '  sensitive-words:',
        '    - foo',
        '    - bar',
        '',
      ].join('\n');

      act(() => {
        expect(harness.getCurrent().loadVisualValuesFromYaml(yaml).ok).toBe(true);
      });
      expect(harness.getCurrent().visualDirty).toBe(false);

      // Setting to identical values does not mark dirty
      act(() => {
        harness.getCurrent().setVisualValues({
          devinSensitiveWords: ['foo', 'bar'],
        });
      });
      expect(harness.getCurrent().visualDirty).toBe(false);

      // Editing marks dirty
      act(() => {
        harness.getCurrent().setVisualValues({
          devinSensitiveWords: ['foo', 'bar', 'baz'],
        });
      });
      expect(harness.getCurrent().visualDirty).toBe(true);

      // Reverting clears dirty
      act(() => {
        harness.getCurrent().setVisualValues({
          devinSensitiveWords: ['foo', 'bar'],
        });
      });
      expect(harness.getCurrent().visualDirty).toBe(false);

      harness.unmount();
    });
  });
});
