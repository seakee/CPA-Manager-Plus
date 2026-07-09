import { act, createElement, createRef, useImperativeHandle, type Ref } from 'react';
import { create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it } from 'vitest';
import { parse as parseYaml } from 'yaml';
import { useVisualConfig } from './useVisualConfig';

type UseVisualConfigResult = ReturnType<typeof useVisualConfig>;

type UseVisualConfigHarness = {
  getCurrent: () => UseVisualConfigResult;
  unmount: () => void;
};

function HookHarness({ hookRef }: { hookRef: Ref<UseVisualConfigResult> }) {
  const hook = useVisualConfig();
  useImperativeHandle(hookRef, () => hook, [hook]);
  return null;
}

const mountUseVisualConfig = (): UseVisualConfigHarness => {
  const hookRef = createRef<UseVisualConfigResult>();
  let renderer: ReactTestRenderer | null = null;

  act(() => {
    renderer = create(createElement(HookHarness, { hookRef }));
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

  it('clears camelCase codex identityConfuse when disabling from visual editor', () => {
    const harness = mountUseVisualConfig();
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

  it('does not load the hashed secret-key into the editable field', () => {
    const harness = mountUseVisualConfig();
    const yaml = [
      'remote-management:',
      '  allow-remote: true',
      '  secret-key: "$2a$10$abcdefghijklmnopqrstuvwxyzABCDEF0123456789ABCDEFG"',
      '',
    ].join('\n');

    act(() => {
      const result = harness.getCurrent().loadVisualValuesFromYaml(yaml);
      expect(result.ok).toBe(true);
    });

    // The backend stores secret-key hashed; echoing it back into the field would
    // rewrite the hash on every save and leave the form permanently dirty.
    expect(harness.getCurrent().visualValues.rmSecretKey).toBe('');

    harness.unmount();
  });

  it('preserves the existing secret-key when the field is left blank', () => {
    const harness = mountUseVisualConfig();
    const hash = '$2a$10$abcdefghijklmnopqrstuvwxyzABCDEF0123456789ABCDEFG';
    const yaml = [
      'remote-management:',
      '  allow-remote: true',
      `  secret-key: "${hash}"`,
      '',
    ].join('\n');

    act(() => {
      const result = harness.getCurrent().loadVisualValuesFromYaml(yaml);
      expect(result.ok).toBe(true);
    });

    // User changes an unrelated field and saves without touching secret-key.
    act(() => {
      harness.getCurrent().setVisualValues({ rmDisableControlPanel: true });
    });

    const savedYaml = harness.getCurrent().applyVisualChangesToYaml(yaml);
    const parsed = parseYaml(savedYaml) as {
      'remote-management'?: { 'secret-key'?: string; 'disable-control-panel'?: boolean };
    };

    // The on-disk hash must survive untouched, and the unrelated edit must apply.
    expect(parsed['remote-management']?.['secret-key']).toBe(hash);
    expect(parsed['remote-management']?.['disable-control-panel']).toBe(true);

    harness.unmount();
  });

  it('writes a new secret-key only when the user types one', () => {
    const harness = mountUseVisualConfig();
    const yaml = [
      'remote-management:',
      '  allow-remote: true',
      '  secret-key: "$2a$10$oldhashvalue0000000000000000000000000000000000000"',
      '',
    ].join('\n');

    act(() => {
      const result = harness.getCurrent().loadVisualValuesFromYaml(yaml);
      expect(result.ok).toBe(true);
    });

    act(() => {
      harness.getCurrent().setVisualValues({ rmSecretKey: 'my-new-plaintext-key' });
    });

    const savedYaml = harness.getCurrent().applyVisualChangesToYaml(yaml);
    const parsed = parseYaml(savedYaml) as {
      'remote-management'?: { 'secret-key'?: string };
    };

    expect(parsed['remote-management']?.['secret-key']).toBe('my-new-plaintext-key');

    harness.unmount();
  });
});
