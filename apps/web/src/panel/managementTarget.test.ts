import { describe, expect, it } from 'vitest';
import { assertPanelManagementTarget } from './managementTarget';
import {
  apiClient,
  createScopedApiRequestConfig,
  toV8ManagementBaseUrl,
} from '@/services/api/client';
import type { AxiosAdapter, InternalAxiosRequestConfig } from 'axios';
import {
  usageServiceApi,
  dashboardApi,
  monitoringAnalyticsApi,
  accountQuotaSnapshotApi,
} from './unsupportedManagerApi';

describe('Panel Lite CPA transport boundary', () => {
  it.each([
    '/config',
    '/auth-files/a.json',
    '/request-log-by-id/log%201?download=true',
    '/plugin-store/demo%2Fplugin/install',
  ])('preserves CPA Management resources %s', (resource) => {
    expect(() =>
      assertPanelManagementTarget('http://cpa:8317/v0/management', resource)
    ).not.toThrow();
  });

  it.each([
    '/api/v2',
    '/api/v2/status',
    'https://manager.example/control/api/v2/status',
    '//manager.example/control/api/v2/status',
    '/../../control/api/v2/status',
    '/%2e%2e/control/api/v2/status',
    '/.%2e/control/api/v2/status',
    '/%5cmanager.example/path',
    '/\\manager.example/path',
  ])('rejects CPAMP or escaping destinations %s', (resource) => {
    expect(() => assertPanelManagementTarget('http://cpa:8317/v0/management', resource)).toThrow();
  });

  it('rejects a non-CPA base before a CPA key can be sent', () => {
    expect(() =>
      assertPanelManagementTarget('https://manager.example/control/api/v2', '/status')
    ).toThrow();
    expect(() =>
      assertPanelManagementTarget('https://manager.example/control/api/v2/v0/management', '/status')
    ).toThrow();
    expect(() =>
      assertPanelManagementTarget('//evil.example/v0/management', '/config')
    ).toThrow();
    expect(() =>
      assertPanelManagementTarget('//evil.example/v8/management', '/credentials')
    ).toThrow();
    expect(() =>
      assertPanelManagementTarget('  //evil.example/api/v2/cpamp/v8/management', '/oauth/siwc/bind')
    ).toThrow();
  });

  it('allows only the authenticated Full Web legacy bridge target', () => {
    expect(() =>
      assertPanelManagementTarget(
        'https://manager.example/control/api/v2/cpamp/v0/management',
        '/config'
      )
    ).not.toThrow();
    expect(() =>
      assertPanelManagementTarget('https://manager.example/control/api/v2/v0/management', '/config')
    ).toThrow();
    expect(() =>
      assertPanelManagementTarget(
        'https://manager.example/control/api/v2/cpamp/v8/management',
        '/oauth/siwc/bind'
      )
    ).not.toThrow();
    expect(() =>
      assertPanelManagementTarget('http://cpa:8317/v8/management', '/oauth/siwc/bind')
    ).not.toThrow();
    expect(() =>
      assertPanelManagementTarget('https://manager.example/control/api/v2/v8/management', '/oauth/siwc/bind')
    ).toThrow();
  });

  it.each([
    '/config/api-keys',
    '/config/api-keys/gemini',
    '/config/api-keys/interactions',
    '/config/api-keys/vertex',
    '/config/api-keys/codex',
    '/config/api-keys/claude',
    '/config/api-keys/xai',
    '/config/api-keys/meta',
    '/config/api-keys/openai-compatibility',
    '/models/refresh',
    '/credentials',
    '/oauth/siwc/bind',
    '/oauth/callback',
    '/oauth/status',
    '/oauth/session',
  ])('permits allowlisted CPA v8 management path %s on standalone and Full Web bases', (resource) => {
    expect(() =>
      assertPanelManagementTarget('http://cpa:8317/v8/management', resource)
    ).not.toThrow();
    expect(() =>
      assertPanelManagementTarget(
        'https://manager.example/control/api/v2/cpamp/v8/management',
        resource
      )
    ).not.toThrow();
  });

  it.each([
    '/config',
    '/config/access/api-keys',
    '/config/api-keys/unknown',
    '/config/api-keys/',
    '/config/api-keys/claude/',
    '/oauth/status/',
    '/config/api-keys/claude/extra',
    '/config/api-keys/../access/api-keys',
    '/config/api-keys/%2e%2e/access/api-keys',
    '/api/v2',
    '/api/v2/status',
    '/credentials/download',
    '/oauth/siwc/bind/extra',
  ])('rejects non-allowlisted or escaping CPA v8 management path %s', (resource) => {
    expect(() =>
      assertPanelManagementTarget('http://cpa:8317/v8/management', resource)
    ).toThrow();
    expect(() =>
      assertPanelManagementTarget(
        'https://manager.example/control/api/v2/cpamp/v8/management',
        resource
      )
    ).toThrow();
  });

  it('converts v0 management bases to v8 and preserves headers on getV8, getRawV8, and putV8', async () => {
    expect(toV8ManagementBaseUrl('http://127.0.0.1:8317/v0/management/')).toBe(
      'http://127.0.0.1:8317/v8/management'
    );
    expect(
      toV8ManagementBaseUrl('https://manager.example/control/api/v2/cpamp/v0/management')
    ).toBe('https://manager.example/control/api/v2/cpamp/v8/management');

    const seen: InternalAxiosRequestConfig[] = [];
    const adapter: AxiosAdapter = async (config) => {
      seen.push(config);
      return {
        data: { claude: [] },
        status: 200,
        statusText: 'OK',
        headers: { etag: '"etag-v8"' },
        config,
      };
    };

    apiClient.setConfig({
      apiBase: 'https://current-cpa.local:8317',
      managementKey: 'current-secret',
    });

    const data = await apiClient.getV8('/config/api-keys', { adapter });
    expect(data).toEqual({ claude: [] });
    expect(seen[0]?.baseURL).toBe('https://current-cpa.local:8317/v8/management');
    expect(seen[0]?.headers?.Authorization).toBe('Bearer current-secret');

    const rawResponse = await apiClient.getRawV8('/config/api-keys/claude', { adapter });
    expect(rawResponse.headers.etag).toBe('"etag-v8"');

    const scoped = createScopedApiRequestConfig({
      apiBase: 'https://scoped-cpa.local:8317',
      managementKey: 'scoped-secret',
    });
    await apiClient.putV8('/config/api-keys/claude', [], {
      ...scoped,
      adapter,
      headers: {
        ...(scoped.headers || {}),
        'If-Match': '"etag-v8"',
      },
    });
    expect(seen[2]?.baseURL).toBe('https://scoped-cpa.local:8317/v8/management');
    expect(seen[2]?.headers?.Authorization).toBe('Bearer scoped-secret');
    expect(seen[2]?.headers?.['If-Match']).toBe('"etag-v8"');

    await expect(apiClient.getV8('/config/access/api-keys', { adapter })).rejects.toThrow(
      'CPA Management target'
    );
  });

  it('legacy Manager compatibility calls cannot reach the network', async () => {
    await expect(usageServiceApi.getInfo('https://manager.example')).rejects.toThrow(
      'unavailable in Panel Lite'
    );
    for (const adapter of [dashboardApi, monitoringAnalyticsApi, accountQuotaSnapshotApi]) {
      const operation = (
        adapter as unknown as Record<string, (...args: unknown[]) => Promise<never>>
      ).anyOperation;
      await expect(operation('https://manager.example', 'cpa-key')).rejects.toThrow(
        'unavailable in Panel Lite'
      );
    }
  });
});
