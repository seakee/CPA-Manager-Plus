import { afterEach, describe, expect, it, vi } from 'vitest';
import { createAppRoutes } from '@/app/appRoutes';
import {
  getDemoAuthFiles,
  getDemoDashboardSummary,
  getDemoErrorLogsResponse,
  getDemoLatestVersion,
  getDemoManagerLatestRelease,
  getDemoManagerConfig,
  getDemoMonitoringAnalytics,
  getDemoPluginStore,
  getDemoRawConfig,
} from './demoFixtures';
import {
  DEMO_ROUTE_BASE,
  getDemoServerBuildDate,
  ensureRouteBasePathname,
  getDemoLogoutHash,
  getDemoLogoutPath,
  isDemoMode,
  prefixRouteBase,
  setDemoMode,
  stripRouteBase,
} from './demoMode';

describe('DemoPage', () => {
  afterEach(() => {
    setDemoMode(false);
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('keeps demo routes under the demo prefix while matching real routes internally', () => {
    expect(stripRouteBase('/demo', DEMO_ROUTE_BASE)).toBe('/');
    expect(stripRouteBase('/demo/config', DEMO_ROUTE_BASE)).toBe('/config');
    expect(stripRouteBase('/demo/monitoring?tab=events', DEMO_ROUTE_BASE)).toBe(
      '/monitoring?tab=events'
    );

    expect(prefixRouteBase('/', DEMO_ROUTE_BASE)).toBe('/demo');
    expect(prefixRouteBase('/config', DEMO_ROUTE_BASE)).toBe('/demo/config');
    expect(prefixRouteBase('/monitoring/account-actions', DEMO_ROUTE_BASE)).toBe(
      '/demo/monitoring/account-actions'
    );

    expect(ensureRouteBasePathname('/', DEMO_ROUTE_BASE)).toBe('/demo');
    expect(ensureRouteBasePathname('/config', DEMO_ROUTE_BASE)).toBe('/demo/config');
    expect(ensureRouteBasePathname('/demo/config', DEMO_ROUTE_BASE)).toBe('/demo/config');
  });

  it('keeps demo site routing isolated from the real login panel', () => {
    const demoChildren = createAppRoutes()[0]?.children ?? [];
    const demoPaths = demoChildren.map((route) => route.path ?? '(index)');

    expect(demoPaths).toEqual(['(index)', '/demo/*', '*']);
    expect(demoPaths).not.toContain('/login');
    expect(demoPaths).not.toContain('/*');
  });

  it('keeps demo logout inside the demo site', () => {
    expect(getDemoLogoutPath()).toBe('/demo');
    expect(getDemoLogoutPath(DEMO_ROUTE_BASE)).toBe('/demo');
    expect(getDemoLogoutHash()).toBe('#/demo');
    expect(getDemoLogoutHash(DEMO_ROUTE_BASE)).toBe('#/demo');
    expect(getDemoLogoutHash('/demo/')).toBe('#/demo');
    expect(getDemoLogoutHash()).not.toBe('#/login');
  });

  it('recognizes deep demo hash routes before demo stores are mounted', () => {
    vi.stubGlobal('window', {
      location: {
        hash: '#/demo/plugins',
        pathname: '/',
      },
    });

    expect(isDemoMode()).toBe(true);
  });

  it('keeps normal hash routes out of demo mode', () => {
    vi.stubGlobal('window', {
      location: {
        hash: '#/dashboard',
        pathname: '/',
      },
    });

    expect(isDemoMode()).toBe(false);
  });

  it('does not infer demo mode from the deployment pathname without a demo hash route', () => {
    vi.stubGlobal('window', {
      location: {
        hash: '',
        pathname: '/demo/management.html',
      },
    });

    expect(isDemoMode()).toBe(false);
  });

  it('keeps demo mock data free of historical analysis labels', () => {
    const visibleData = JSON.stringify([
      getDemoRawConfig(),
      getDemoAuthFiles(),
      getDemoPluginStore(),
      getDemoManagerConfig(),
      getDemoDashboardSummary(),
      getDemoMonitoringAnalytics(),
    ]);
    const historicalAnalysisLabel = ['cc', 'switch'].join('-');

    expect(visibleData.toLowerCase()).not.toContain(historicalAnalysisLabel);
  });

  it('fills the dashboard request health timeline with real dashboard granularity', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-06-29T10:00:00+08:00'));

    const timeline = getDemoDashboardSummary().today_request_health_timeline;

    expect(timeline).toBeDefined();
    if (!timeline) throw new Error('missing demo request health timeline');

    expect(timeline.bucket_ms).toBe(10 * 60 * 1000);
    expect(timeline.points).toHaveLength(144);
    const tones = new Set(timeline.points.map((point) => point.tone));
    expect(tones.has('empty')).toBe(true);
    expect(tones.has('good')).toBe(true);
    expect(tones.has('warn')).toBe(true);
    expect(tones.has('bad')).toBe(true);
    expect(tones.has('future')).toBe(true);
  });

  it('fills usage analytics and request monitoring tabs with complete demo pages', () => {
    const firstPage = getDemoMonitoringAnalytics({
      from_ms: 0,
      to_ms: Date.now(),
      include: {
        events_page: { limit: 10 },
        drilldown_preview: { from_ms: 0, to_ms: Date.now(), limit: 8 },
      },
    });

    expect(firstPage.model_stats?.length).toBeGreaterThanOrEqual(8);
    expect(firstPage.account_stats?.length).toBeGreaterThanOrEqual(12);
    expect(firstPage.api_key_stats?.length).toBeGreaterThanOrEqual(12);
    expect(firstPage.credential_stats?.length).toBeGreaterThanOrEqual(10);
    expect(firstPage.credential_timeline?.length).toBeGreaterThanOrEqual(70);
    expect(firstPage.heatmap).toHaveLength(168);
    expect(firstPage.heatmap?.some((point) => point.calls > 0)).toBe(true);
    expect(firstPage.events?.items).toHaveLength(10);
    expect(firstPage.events?.has_more).toBe(true);
    expect(
      new Set(firstPage.events?.items.map((event) => event.api_key_hash)).size
    ).toBeGreaterThanOrEqual(8);

    const secondPage = getDemoMonitoringAnalytics({
      from_ms: 0,
      to_ms: Date.now(),
      include: {
        events_page: { limit: 10, before_ms: firstPage.events?.next_before_ms },
      },
    });
    const firstHashes = new Set(firstPage.events?.items.map((event) => event.event_hash));

    expect(secondPage.events?.items).toHaveLength(10);
    expect(secondPage.events?.items.every((event) => !firstHashes.has(event.event_hash))).toBe(
      true
    );
  });

  it('keeps visible demo dates relative to the current day', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-06-29T10:00:00+08:00'));

    expect(getDemoServerBuildDate()).toBe('2026-06-29');
    expect(getDemoLatestVersion().buildDate).toBe('2026-06-29');
    expect(getDemoErrorLogsResponse().files.map((file) => file.name)).toEqual([
      'request-errors-2026-06-29.jsonl',
      'request-errors-2026-06-28.jsonl',
    ]);
    expect(new Date(getDemoManagerLatestRelease().published_at).getTime()).toBe(
      new Date('2026-06-29T00:00:00+08:00').getTime()
    );

    vi.setSystemTime(new Date('2026-06-30T10:00:00+08:00'));

    expect(getDemoServerBuildDate()).toBe('2026-06-30');
    expect(getDemoLatestVersion().buildDate).toBe('2026-06-30');
    expect(getDemoErrorLogsResponse().files.map((file) => file.name)).toEqual([
      'request-errors-2026-06-30.jsonl',
      'request-errors-2026-06-29.jsonl',
    ]);
    expect(new Date(getDemoManagerLatestRelease().published_at).getTime()).toBe(
      new Date('2026-06-30T00:00:00+08:00').getTime()
    );
  });
});
