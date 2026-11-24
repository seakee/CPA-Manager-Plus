import { describe, expect, it } from 'vitest';
import { assertPanelManagementTarget } from './managementTarget';
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
    '/oauth/status/',
    '/credentials/download',
    '/oauth/siwc/bind/extra',
    '/api/v2',
    '/api/v2/status',
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
