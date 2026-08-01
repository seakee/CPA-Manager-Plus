import { describe, expect, it, vi } from 'vitest';
import type { UsageImportSession, UsageServiceApiError } from '@/services/api/usageService';
import {
  cancelUsageImportFile,
  UsageImportCancelledError,
  UsageImportPausedError,
  uploadUsageImportFile,
  type UsageImportProgress,
  type UsageImportSessionClient,
  type UsageImportStorage,
} from './usageImportSession';

class MemoryStorage implements UsageImportStorage {
  readonly values = new Map<string, string>();

  getItem(key: string) {
    return this.values.get(key) ?? null;
  }

  setItem(key: string, value: string) {
    this.values.set(key, value);
  }

  removeItem(key: string) {
    this.values.delete(key);
  }
}

const createSession = (overrides: Partial<UsageImportSession> = {}): UsageImportSession => ({
  id: 'session-1',
  filename: 'history.jsonl',
  status: 'uploading',
  size_bytes: 10,
  received_bytes: 0,
  chunk_size_bytes: 4,
  created_at_ms: 1,
  updated_at_ms: 1,
  expires_at_ms: 10_000,
  ...overrides,
});

function createClient() {
  let session = createSession();
  const offsets: number[] = [];
  const chunkSizes: number[] = [];
  const client: UsageImportSessionClient = {
    createUsageImportSession: vi.fn(async (_base, filename, sizeBytes) => {
      session = createSession({ filename, size_bytes: sizeBytes });
      return { ...session };
    }),
    getUsageImportSession: vi.fn(async () => {
      if (session.status === 'processing') {
        session = {
          ...session,
          status: 'completed',
          result: { format: 'usage_service_jsonl', added: 2, skipped: 0, total: 2, failed: 0 },
        };
      }
      return { ...session };
    }),
    uploadUsageImportSessionChunk: vi.fn(async (_base, _id, offset, chunk) => {
      offsets.push(offset);
      chunkSizes.push(chunk.size);
      const received = offset + chunk.size;
      session = {
        ...session,
        received_bytes: received,
        status: received === session.size_bytes ? 'ready' : 'uploading',
      };
      return { ...session };
    }),
    completeUsageImportSession: vi.fn(async () => {
      session = { ...session, status: 'processing' };
      return { ...session };
    }),
    cancelUsageImportSession: vi.fn(async () => {
      session = { ...session, status: 'cancelled' };
      return { ...session };
    }),
  };
  return {
    client,
    offsets,
    chunkSizes,
    getSession: () => session,
    setSession: (next: UsageImportSession) => {
      session = next;
    },
  };
}

describe('usage import session orchestration', () => {
  it('uploads File.slice chunks, completes asynchronously, and clears resume storage', async () => {
    const { client, offsets, chunkSizes } = createClient();
    const storage = new MemoryStorage();
    const progress: UsageImportProgress[] = [];
    const file = new File(['0123456789'], 'history.jsonl', {
      type: 'application/x-ndjson',
      lastModified: 123,
    });

    const result = await uploadUsageImportFile({
      base: 'http://manager.local',
      managementKey: 'key',
      file,
      client,
      storage,
      pollIntervalMs: 0,
      onProgress: (value) => progress.push(value),
    });

    expect(offsets).toEqual([0, 4, 8]);
    expect(chunkSizes).toEqual([4, 4, 2]);
    expect(result).toMatchObject({ added: 2, total: 2 });
    expect(progress.map((item) => item.phase)).toContain('processing');
    expect(progress[progress.length - 1]).toMatchObject({ phase: 'completed', percent: 100 });
    expect(storage.values.size).toBe(0);
  });

  it('retains a paused session and resumes from the server offset for the same file', async () => {
    const { client, offsets } = createClient();
    const storage = new MemoryStorage();
    const file = new File(['0123456789'], 'history.jsonl', { lastModified: 456 });
    const controller = new AbortController();

    const firstRun = uploadUsageImportFile({
      base: 'http://manager.local',
      file,
      client,
      storage,
      pollIntervalMs: 0,
      signal: controller.signal,
      onProgress: (progress) => {
        if (progress.phase === 'uploading' && progress.uploadedBytes === 4) {
          controller.abort();
        }
      },
    });
    await expect(firstRun).rejects.toBeInstanceOf(UsageImportPausedError);
    expect(offsets).toEqual([0]);
    expect(storage.values.size).toBe(1);

    const result = await uploadUsageImportFile({
      base: 'http://manager.local',
      file,
      client,
      storage,
      pollIntervalMs: 0,
    });

    expect(client.createUsageImportSession).toHaveBeenCalledTimes(1);
    expect(client.getUsageImportSession).toHaveBeenCalled();
    expect(offsets).toEqual([0, 4, 8]);
    expect(result.added).toBe(2);
    expect(storage.values.size).toBe(0);
  });

  it('resumes a stored session when the server normalized the display filename', async () => {
    const { client, getSession, setSession } = createClient();
    const storage = new MemoryStorage();
    const file = new File(['0123456789'], 'very-long-history-name.jsonl', {
      lastModified: 654,
    });
    const controller = new AbortController();

    await expect(
      uploadUsageImportFile({
        base: 'http://manager.local',
        file,
        client,
        storage,
        signal: controller.signal,
        onProgress: (progress) => {
          if (progress.uploadedBytes === 4) controller.abort();
        },
      })
    ).rejects.toBeInstanceOf(UsageImportPausedError);
    setSession({ ...getSession(), filename: 'history.jsonl' });

    await uploadUsageImportFile({
      base: 'http://manager.local',
      file,
      client,
      storage,
      pollIntervalMs: 0,
    });

    expect(client.createUsageImportSession).toHaveBeenCalledTimes(1);
    expect(storage.values.size).toBe(0);
  });

  it('reuses a persisted resume key when the create response is lost', async () => {
    const { client } = createClient();
    const storage = new MemoryStorage();
    const file = new File(['0123456789'], 'history.jsonl', { lastModified: 741 });
    const resumeKeys: string[] = [];
    const progress: UsageImportProgress[] = [];
    vi.mocked(client.createUsageImportSession)
      .mockImplementationOnce(async (_base, filename, sizeBytes, _managementKey, resumeKey) => {
        resumeKeys.push(resumeKey ?? '');
        throw new Error(`response lost for ${filename}:${sizeBytes}`);
      })
      .mockImplementationOnce(async (_base, filename, sizeBytes, _managementKey, resumeKey) => {
        resumeKeys.push(resumeKey ?? '');
        return createSession({ filename, size_bytes: sizeBytes });
      });

    await expect(
      uploadUsageImportFile({
        base: 'http://manager.local',
        file,
        client,
        storage,
        pollIntervalMs: 0,
        onProgress: (value) => progress.push(value),
      })
    ).rejects.toThrow('response lost');
    expect(storage.values.size).toBe(1);
    expect(progress[progress.length - 1]).toMatchObject({ phase: 'failed', retryable: true });

    const result = await uploadUsageImportFile({
      base: 'http://manager.local',
      file,
      client,
      storage,
      pollIntervalMs: 0,
    });

    expect(resumeKeys).toHaveLength(2);
    expect(resumeKeys[0]).toMatch(/^[0-9a-f]{32}$/);
    expect(resumeKeys[1]).toBe(resumeKeys[0]);
    expect(result.added).toBe(2);
    expect(storage.values.size).toBe(0);
  });

  it('clears the pending resume key and disables retry for a rejected file size', async () => {
    const { client } = createClient();
    const storage = new MemoryStorage();
    const file = new File(['0123456789'], 'history.jsonl', { lastModified: 852 });
    const progress: UsageImportProgress[] = [];
    const tooLarge = new Error('file exceeds quota') as UsageServiceApiError;
    tooLarge.code = 'usage_import_session_too_large';
    vi.mocked(client.createUsageImportSession).mockRejectedValueOnce(tooLarge);

    await expect(
      uploadUsageImportFile({
        base: 'http://manager.local',
        file,
        client,
        storage,
        onProgress: (value) => progress.push(value),
      })
    ).rejects.toThrow('file exceeds quota');

    expect(progress[progress.length - 1]).toMatchObject({
      phase: 'failed',
      retryable: false,
    });
    expect(storage.values.size).toBe(0);
  });

  it('cancels a retained session and clears its stored fingerprint', async () => {
    const { client, getSession } = createClient();
    const storage = new MemoryStorage();
    const file = new File(['0123456789'], 'history.jsonl', { lastModified: 789 });
    const controller = new AbortController();

    await expect(
      uploadUsageImportFile({
        base: 'http://manager.local',
        file,
        client,
        storage,
        signal: controller.signal,
        onProgress: (progress) => {
          if (progress.sessionId) controller.abort();
        },
      })
    ).rejects.toBeInstanceOf(UsageImportPausedError);
    expect(storage.values.size).toBe(1);

    const cancelled = await cancelUsageImportFile({
      base: 'http://manager.local',
      sessionId: getSession().id,
      file,
      client,
      storage,
    });

    expect(client.cancelUsageImportSession).toHaveBeenCalledWith(
      'http://manager.local',
      'session-1',
      undefined
    );
    expect(cancelled?.status).toBe('cancelled');
    expect(storage.values.size).toBe(0);
  });

  it('keeps resume storage when cancellation fails', async () => {
    const { client, getSession } = createClient();
    const storage = new MemoryStorage();
    const file = new File(['0123456789'], 'history.jsonl', { lastModified: 987 });
    const controller = new AbortController();

    await expect(
      uploadUsageImportFile({
        base: 'http://manager.local',
        file,
        client,
        storage,
        signal: controller.signal,
        onProgress: (progress) => {
          if (progress.sessionId) controller.abort();
        },
      })
    ).rejects.toBeInstanceOf(UsageImportPausedError);
    vi.mocked(client.cancelUsageImportSession).mockRejectedValueOnce(new Error('network down'));

    await expect(
      cancelUsageImportFile({
        base: 'http://manager.local',
        sessionId: getSession().id,
        file,
        client,
        storage,
      })
    ).rejects.toThrow('network down');
    expect(storage.values.size).toBe(1);
  });

  it('waits for a processing cancellation to publish its final partial result', async () => {
    const { client } = createClient();
    const storage = new MemoryStorage();
    const file = new File(['0123456789'], 'history.jsonl', { lastModified: 963 });
    storage.setItem(
      'cpa-manager-plus:usage-import-sessions:v1',
      JSON.stringify({
        '["http://manager.local","history.jsonl",10,963]': {
          sessionId: 'session-1',
          resumeKey: '0123456789abcdef0123456789abcdef',
        },
      })
    );
    vi.mocked(client.cancelUsageImportSession).mockResolvedValueOnce(
      createSession({ status: 'processing', received_bytes: 10 })
    );
    vi.mocked(client.getUsageImportSession).mockResolvedValueOnce(
      createSession({
        status: 'cancelled',
        received_bytes: 10,
        result: { format: 'usage_service_jsonl', added: 2, skipped: 1, total: 4, failed: 1 },
      })
    );

    const cancelled = await cancelUsageImportFile({
      base: 'http://manager.local',
      sessionId: 'session-1',
      file,
      client,
      storage,
      pollIntervalMs: 0,
      settleTimeoutMs: 100,
    });

    expect(client.getUsageImportSession).toHaveBeenCalledTimes(1);
    expect(cancelled).toMatchObject({
      status: 'cancelled',
      result: { added: 2, skipped: 1, total: 4, failed: 1 },
    });
    expect(storage.values.size).toBe(0);
  });

  it('clears stale resume storage when cancellation reports a missing session', async () => {
    const { client, getSession } = createClient();
    const storage = new MemoryStorage();
    const file = new File(['0123456789'], 'history.jsonl', { lastModified: 321 });
    const controller = new AbortController();

    await expect(
      uploadUsageImportFile({
        base: 'http://manager.local',
        file,
        client,
        storage,
        signal: controller.signal,
        onProgress: (progress) => {
          if (progress.sessionId) controller.abort();
        },
      })
    ).rejects.toBeInstanceOf(UsageImportPausedError);
    const missing = new Error('missing') as UsageServiceApiError;
    missing.code = 'usage_import_session_not_found';
    vi.mocked(client.cancelUsageImportSession).mockRejectedValueOnce(missing);

    const cancelled = await cancelUsageImportFile({
      base: 'http://manager.local',
      sessionId: getSession().id,
      file,
      client,
      storage,
    });

    expect(cancelled).toBeNull();
    expect(storage.values.size).toBe(0);
  });

  it('reports a server-side cancellation distinctly from an import failure', async () => {
    const { client } = createClient();
    const storage = new MemoryStorage();
    const file = new File(['0123456789'], 'history.jsonl', { lastModified: 111 });
    const progress: UsageImportProgress[] = [];
    vi.mocked(client.getUsageImportSession)
      .mockResolvedValueOnce(createSession({ status: 'processing', received_bytes: 10 }))
      .mockResolvedValueOnce(
        createSession({
          status: 'cancelled',
          received_bytes: 10,
          error: 'usage import cancelled',
          result: { format: 'usage_service_jsonl', added: 2, skipped: 1, total: 4, failed: 1 },
        })
      );

    storage.setItem(
      'cpa-manager-plus:usage-import-sessions:v1',
      JSON.stringify({
        '["http://manager.local","history.jsonl",10,111]': 'session-1',
      })
    );

    await expect(
      uploadUsageImportFile({
        base: 'http://manager.local',
        file,
        client,
        storage,
        pollIntervalMs: 0,
        onProgress: (value) => progress.push(value),
      })
    ).rejects.toBeInstanceOf(UsageImportCancelledError);
    expect(progress[progress.length - 1]).toMatchObject({
      phase: 'cancelled',
      error: undefined,
      result: { added: 2, skipped: 1, total: 4, failed: 1 },
    });
    expect(storage.values.size).toBe(0);
  });
});
