import axios, { AxiosHeaders, type AxiosResponse } from 'axios';
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('@/features/demo/demoMode', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/demo/demoMode')>()),
  isDemoMode: () => false,
}));

import {
  getUsageServiceErrorCode,
  usageServiceApi,
  type UsageMaintenanceStatus,
} from './usageService';

const head = vi.spyOn(axios, 'head');
const get = vi.spyOn(axios, 'get');
const post = vi.spyOn(axios, 'post');
const responseWithStatus = (status: number): AxiosResponse => ({
  data: undefined,
  status,
  statusText: '',
  headers: {},
  config: { headers: new AxiosHeaders() },
});

beforeEach(() => {
  head.mockReset();
  get.mockReset();
  post.mockReset();
});

describe('usage maintenance capability probe', () => {
  it('accepts the authenticated 204 capability response', async () => {
    const signal = new AbortController().signal;
    head.mockResolvedValue(responseWithStatus(204));

    await usageServiceApi.probeUsageMaintenance('http://manager.local:18317/', 'admin-key', signal);

    expect(head).toHaveBeenCalledWith(
      'http://manager.local:18317/v0/management/usage/maintenance',
      expect.objectContaining({
        headers: { Authorization: 'Bearer admin-key' },
        signal,
      })
    );
  });

  it('rejects a generic 200 response that does not prove capability support', async () => {
    head.mockResolvedValue(responseWithStatus(200));

    await expect(
      usageServiceApi.probeUsageMaintenance('http://manager.local:18317', 'admin-key')
    ).rejects.toMatchObject({
      status: 200,
      code: 'usage_archive_unavailable',
    });
  });

  it('uses the authenticated archive and maintenance endpoints with cancellation support', async () => {
    const signal = new AbortController().signal;
    post.mockResolvedValue(responseWithStatus(200));
    get.mockResolvedValue(responseWithStatus(200));

    await usageServiceApi.previewUsageArchive(
      'http://manager.local:18317/',
      1_700_000_000_000,
      'admin-key',
      signal
    );
    await usageServiceApi.createUsageArchive(
      'http://manager.local:18317/',
      1_700_000_000_000,
      'admin-key',
      signal
    );
    await usageServiceApi.listUsageArchives('http://manager.local:18317/', 'admin-key', 25, signal);
    await usageServiceApi.getUsageArchive(
      'http://manager.local:18317/',
      'run/id',
      'admin-key',
      signal
    );
    await usageServiceApi.getUsageMaintenance('http://manager.local:18317/', 'admin-key', signal);

    expect(post).toHaveBeenNthCalledWith(
      1,
      'http://manager.local:18317/v0/management/usage/archives/preview',
      { cutoff_timestamp_ms: 1_700_000_000_000 },
      expect.objectContaining({
        timeout: 0,
        headers: { Authorization: 'Bearer admin-key' },
        signal,
      })
    );
    expect(post).toHaveBeenNthCalledWith(
      2,
      'http://manager.local:18317/v0/management/usage/archives',
      { cutoff_timestamp_ms: 1_700_000_000_000 },
      expect.objectContaining({
        timeout: 0,
        headers: { Authorization: 'Bearer admin-key' },
        signal,
      })
    );
    expect(get).toHaveBeenNthCalledWith(
      1,
      'http://manager.local:18317/v0/management/usage/archives',
      expect.objectContaining({
        headers: { Authorization: 'Bearer admin-key' },
        params: { limit: 25 },
        signal,
      })
    );
    expect(get).toHaveBeenNthCalledWith(
      2,
      'http://manager.local:18317/v0/management/usage/archives/run%2Fid',
      expect.objectContaining({
        headers: { Authorization: 'Bearer admin-key' },
        signal,
      })
    );
    expect(get).toHaveBeenNthCalledWith(
      3,
      'http://manager.local:18317/v0/management/usage/maintenance',
      expect.objectContaining({
        headers: { Authorization: 'Bearer admin-key' },
        signal,
      })
    );
  });

  it('returns the optional raw event time range from maintenance status', async () => {
    const data = {
      raw_event_count: 2,
      raw_min_timestamp_ms: 1_000,
      raw_max_timestamp_ms: 2_000,
      raw_archived_event_count: 1,
      raw_deleted_event_count: 0,
      migration: {
        name: 'usage_cache_accounting_v2',
        status: 'completed',
        last_event_id: 2,
        target_event_id: 2,
        processed_rows: 2,
        changed_rows: 0,
        updated_at_ms: 2_000,
      },
      hourly_aggregate: {
        name: 'hourly_core',
        schema_version: 1,
        status: 'ready',
        coverage_event_id: 2,
        target_event_id: 2,
        updated_at_ms: 2_000,
      },
      readiness: {
        migration_ready: true,
        hourly_aggregate_ready: true,
        archive_delete_enabled: true,
      },
      migration_coverage: {
        status: 'completed',
        watermark_event_id: 2,
        target_event_id: 2,
        complete: true,
      },
      hourly_aggregate_coverage: {
        status: 'ready',
        watermark_event_id: 2,
        target_event_id: 2,
        complete: true,
      },
      storage: {
        page_size: 4_096,
        page_count: 10,
        freelist_count: 0,
        reclaimable_bytes: 0,
        database_bytes: 40_960,
        wal_bytes: 0,
        shm_bytes: 0,
        total_bytes: 40_960,
      },
      compact_requires_stopped_server: true,
    } satisfies UsageMaintenanceStatus;
    get.mockResolvedValue({ ...responseWithStatus(200), data });

    await expect(
      usageServiceApi.getUsageMaintenance('http://manager.local:18317', 'admin-key')
    ).resolves.toEqual(data);
  });

  it('preserves stable archive error codes for UI handling', () => {
    for (const code of [
      'usage_archive_invalid_id',
      'usage_archive_invalid_request',
      'usage_archive_request_too_large',
      'usage_archive_no_events',
      'usage_archive_maintenance_locked',
      'usage_archive_invalid_state',
      'usage_archive_coverage_incomplete',
      'usage_archive_delete_unavailable',
      'usage_archive_not_found',
      'usage_archive_unavailable',
    ]) {
      expect(getUsageServiceErrorCode({ code })).toBe(code);
    }
  });

  it('keeps long-running archive actions unbounded while forwarding cancellation', async () => {
    const signal = new AbortController().signal;
    post.mockResolvedValue(responseWithStatus(200));

    await usageServiceApi.resumeUsageArchive(
      'http://manager.local:18317',
      'run/id',
      'admin-key',
      signal,
      'archiving'
    );
    await usageServiceApi.verifyUsageArchive(
      'http://manager.local:18317',
      'run/id',
      'admin-key',
      signal
    );
    await usageServiceApi.deleteUsageArchive(
      'http://manager.local:18317',
      'run/id',
      'admin-key',
      signal
    );

    for (const [index, action] of ['resume', 'verify', 'delete'].entries()) {
      expect(post).toHaveBeenNthCalledWith(
        index + 1,
        `http://manager.local:18317/v0/management/usage/archives/run%2Fid/${action}`,
        undefined,
        expect.objectContaining({
          timeout: 0,
          headers: { Authorization: 'Bearer admin-key' },
          signal,
        })
      );
    }
    expect(post.mock.calls[0][2]).toEqual(
      expect.objectContaining({ params: { expected_stage: 'archiving' } })
    );
  });

  it('forwards archive filters, cursors, and background execution options', async () => {
    const signal = new AbortController().signal;
    get.mockResolvedValue(responseWithStatus(200));
    post.mockResolvedValue(responseWithStatus(202));

    await usageServiceApi.listUsageArchives(
      'http://manager.local:18317',
      'admin-key',
      { status: 'failed', mode: 'manual', limit: 15, cursor: 'next-page' },
      signal
    );
    await usageServiceApi.resumeUsageArchive(
      'http://manager.local:18317',
      'run/id',
      'admin-key',
      signal,
      'verifying',
      { background: true }
    );

    expect(get).toHaveBeenCalledWith(
      'http://manager.local:18317/v0/management/usage/archives',
      expect.objectContaining({
        headers: { Authorization: 'Bearer admin-key' },
        params: { status: 'failed', mode: 'manual', limit: 15, cursor: 'next-page' },
        signal,
      })
    );
    expect(post).toHaveBeenCalledWith(
      'http://manager.local:18317/v0/management/usage/archives/run%2Fid/resume',
      undefined,
      expect.objectContaining({
        params: { expected_stage: 'verifying', background: true },
        signal,
      })
    );
  });

  it('lists resumable import sessions with capabilities and pagination filters', async () => {
    const signal = new AbortController().signal;
    get.mockResolvedValue(responseWithStatus(200));

    await usageServiceApi.listUsageImportSessions(
      'http://manager.local:18317',
      'management-key',
      { status: 'uploading', limit: 10, cursor: 'next-import-page' },
      signal
    );

    expect(get).toHaveBeenCalledWith(
      'http://manager.local:18317/v0/management/usage/import-sessions',
      expect.objectContaining({
        headers: { Authorization: 'Bearer management-key' },
        params: { status: 'uploading', limit: 10, cursor: 'next-import-page' },
        signal,
      })
    );
  });
});
