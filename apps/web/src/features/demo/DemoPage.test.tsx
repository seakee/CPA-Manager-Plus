import { afterEach, describe, expect, it, vi } from 'vitest';
import { createAppRoutes } from '@/app/appRoutes';
import { CODEX_INSPECTION_LAST_RUN_STORAGE_KEY } from '@/features/monitoring/model/codexInspectionStorage';
import { CODEX_INSPECTION_SETTINGS_STORAGE_KEY } from '@/features/monitoring/model/codexInspectionSettings';
import {
  getDemoAuthFiles,
  getDemoDashboardSummary,
  getDemoErrorLogsResponse,
  getDemoLatestVersion,
  getDemoManagerLatestRelease,
  getDemoManagerConfig,
  getDemoMonitoringAnalytics,
  getDemoHeaderSnapshots,
  getDemoPluginStore,
  getDemoQuotaCooldowns,
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
import { installDemoInspectionState } from './DemoPage';

const createMemoryStorage = () => {
  const values = new Map<string, string>();
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
    removeItem: (key: string) => values.delete(key),
    clear: () => values.clear(),
    key: (index: number) => Array.from(values.keys())[index] ?? null,
    get length() {
      return values.size;
    },
  };
};

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

  it('returns exact API key trend fixtures for selected client keys', () => {
    const page = getDemoMonitoringAnalytics({
      from_ms: 1,
      to_ms: Date.now(),
      filters: {
        api_key_hashes: ['hash_research_shared', 'hash_codex_team'],
      },
      include: {
        api_key_timeline: true,
      },
    });
    const timeline = page.timeline;
    const apiKeyTimeline = page.api_key_timeline;
    if (!timeline || !apiKeyTimeline) throw new Error('missing demo API key timeline');
    const firstBucket = timeline[0];
    const missingCodexBucket = timeline[3];
    if (!firstBucket || !missingCodexBucket) throw new Error('missing demo timeline buckets');

    expect([...new Set(apiKeyTimeline.map((point) => point.api_key_hash))].sort()).toEqual([
      'hash_codex_team',
      'hash_research_shared',
    ]);
    expect(apiKeyTimeline).toHaveLength(timeline.length * 2 - 2);

    const firstResearchPoint = apiKeyTimeline.find(
      (point) =>
        point.api_key_hash === 'hash_research_shared' && point.bucket_ms === firstBucket.bucket_ms
    );
    if (!firstResearchPoint) throw new Error('missing first research API key bucket');
    expect(firstResearchPoint).toMatchObject({
      calls: Math.round(firstBucket.calls * 0.36),
      total_tokens: Math.round(firstBucket.tokens * 0.39),
    });
    expect(firstResearchPoint.success + firstResearchPoint.failure).toBe(firstResearchPoint.calls);
    expect(
      apiKeyTimeline.some(
        (point) =>
          point.api_key_hash === 'hash_codex_team' &&
          point.bucket_ms === missingCodexBucket.bucket_ms
      )
    ).toBe(false);
  });

  it('provides xAI quota exhaustion, successful rate-limit, and cooldown fixtures for UI acceptance', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-07-21T10:25:05.000+08:00'));

    const analytics = getDemoMonitoringAnalytics({
      from_ms: 0,
      to_ms: Date.now(),
      include: { events_page: { limit: 10 } },
    });
    const exhausted = analytics.events?.items.find(
      (event) => event.event_hash === 'demo-event-xai-free-usage-exhausted'
    );
    const successfulRateLimit = analytics.events?.items.find(
      (event) => event.event_hash === 'demo-event-xai-rate-limit-success'
    );
    const xaiCooldown = getDemoQuotaCooldowns().find(
      (cooldown) => cooldown.authFileName === 'xai-ops.json'
    );
    const xaiAuthFile = getDemoAuthFiles().files.find((file) => file.name === 'xai-ops.json');
    const xaiSnapshots = getDemoHeaderSnapshots().items.filter((snapshot) =>
      snapshot.event_hash.startsWith('demo-event-xai-')
    );

    expect(exhausted).toMatchObject({
      failed: true,
      fail_status_code: 429,
      auth_file_snapshot: 'xai-ops.json',
      auth_provider_snapshot: 'xai',
      header_error_code: 'subscription:free-usage-exhausted',
      response_metadata: {
        errors: { should_retry: true },
        provider_usage: {
          provider: 'xai',
          state: 'exhausted',
          actual: 1_024_413,
          limit: 1_000_000,
          overage: 24_413,
          window_kind: 'rolling_24h',
          recover_at_estimated: true,
        },
        data_policy: { retention_mode: 'zdr', zero_retention: true },
      },
    });
    expect(successfulRateLimit).toMatchObject({
      failed: false,
      auth_file_snapshot: 'xai-email-user.json',
      response_metadata: {
        rate_limit: { requests: { limit: 21, remaining: 18 } },
        data_policy: { retention_mode: 'zdr', zero_retention: true },
      },
    });
    expect(xaiCooldown).toMatchObject({
      provider: 'xai',
      owner: 'cpamp_xai_free_usage',
      reasonCode: 'xai_free_usage_exhausted',
      windowKind: 'rolling_24h',
      evidence: {
        actual: 1_024_413,
        limit: 1_000_000,
        recover_at_estimated: true,
      },
    });
    expect(xaiCooldown?.evidence?.recover_at_ms).toBe(xaiCooldown?.recoverAtMs);
    expect(xaiAuthFile).toMatchObject({ disabled: true, status: 'cooldown' });
    expect(xaiSnapshots.map((snapshot) => snapshot.event_hash)).toEqual([
      'demo-event-xai-free-usage-exhausted',
      'demo-event-xai-rate-limit-success',
    ]);
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
      new Date(2026, 5, 29).getTime()
    );

    vi.setSystemTime(new Date('2026-06-30T10:00:00+08:00'));

    expect(getDemoServerBuildDate()).toBe('2026-06-30');
    expect(getDemoLatestVersion().buildDate).toBe('2026-06-30');
    expect(getDemoErrorLogsResponse().files.map((file) => file.name)).toEqual([
      'request-errors-2026-06-30.jsonl',
      'request-errors-2026-06-29.jsonl',
    ]);
    expect(new Date(getDemoManagerLatestRelease().published_at).getTime()).toBe(
      new Date(2026, 5, 30).getTime()
    );
  });

  it('installs inspection demo state and restores the existing local state on exit', () => {
    const storage = createMemoryStorage();
    storage.setItem(CODEX_INSPECTION_LAST_RUN_STORAGE_KEY, 'real-last-run');
    storage.setItem(CODEX_INSPECTION_SETTINGS_STORAGE_KEY, 'real-settings');
    vi.stubGlobal('localStorage', storage);
    vi.stubGlobal('window', {
      localStorage: storage,
      location: { hash: '#/demo/codex-inspection', pathname: '/' },
    });

    const restore = installDemoInspectionState();
    const lastRun = JSON.parse(
      storage.getItem(CODEX_INSPECTION_LAST_RUN_STORAGE_KEY) ?? '{}'
    ) as Record<string, unknown>;
    const settings = JSON.parse(
      storage.getItem(CODEX_INSPECTION_SETTINGS_STORAGE_KEY) ?? '{}'
    ) as Record<string, unknown>;

    expect(lastRun).toMatchObject({
      version: 1,
      actionFilter: 'all',
      logsCollapsed: false,
      result: { results: expect.arrayContaining([expect.objectContaining({ provider: 'xai' })]) },
    });
    expect(lastRun.logs).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ detail: expect.objectContaining({ triggerKey: 'manual' }) }),
      ])
    );
    expect(settings).toMatchObject({
      targetTypes: ['codex', 'xai'],
      xaiInferenceEnabled: true,
      autoActionMode: 'disable',
      autoRecoverEnabled: true,
    });

    restore();

    expect(storage.getItem(CODEX_INSPECTION_LAST_RUN_STORAGE_KEY)).toBe('real-last-run');
    expect(storage.getItem(CODEX_INSPECTION_SETTINGS_STORAGE_KEY)).toBe('real-settings');
  });
});
