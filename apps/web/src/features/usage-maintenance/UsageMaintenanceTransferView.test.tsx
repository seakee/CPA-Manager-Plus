import { act } from 'react';
import { create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { UsageImportSession, UsageImportSessionList } from '@/services/api/usageService';
import { UsageImportFailedError } from '@/features/monitoring/services/usageImportSession';
import { UsageMaintenanceTransferView } from './UsageMaintenanceTransferView';

const { mocks } = vi.hoisted(() => {
  (
    globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
  ).IS_REACT_ACT_ENVIRONMENT = true;
  return {
    mocks: {
      listUsageImportSessions: vi.fn(),
      exportUsage: vi.fn(),
      showNotification: vi.fn(),
      showConfirmation: vi.fn(),
      uploadUsageImportFile: vi.fn(),
      cancelUsageImportFile: vi.fn(),
      downloadBlob: vi.fn(),
      t: (key: string, options?: Record<string, unknown>) => {
        let value = typeof options?.defaultValue === 'string' ? options.defaultValue : key;
        for (const [name, replacement] of Object.entries(options ?? {})) {
          if (name !== 'defaultValue') value = value.split(`{{${name}}}`).join(String(replacement));
        }
        return value;
      },
    },
  };
});

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    i18n: { language: 'en' },
    t: mocks.t,
  }),
}));

vi.mock('@/stores', () => ({
  useNotificationStore: () => ({
    showNotification: mocks.showNotification,
    showConfirmation: mocks.showConfirmation,
  }),
}));

vi.mock('@/services/api/usageService', () => ({
  getUsageServiceErrorCode: (error: unknown) =>
    error && typeof error === 'object' && 'code' in error && typeof error.code === 'string'
      ? error.code
      : '',
  usageServiceApi: {
    listUsageImportSessions: mocks.listUsageImportSessions,
    exportUsage: mocks.exportUsage,
  },
}));

vi.mock('@/features/monitoring/services/usageImportSession', () => ({
  cancelUsageImportFile: mocks.cancelUsageImportFile,
  isUsageImportCancelledError: () => false,
  isUsageImportPausedError: () => false,
  UsageImportFailedError: class UsageImportFailedError extends Error {
    retryable = false;

    constructor(_session?: unknown) {
      super('usage import failed');
    }
  },
  uploadUsageImportFile: mocks.uploadUsageImportFile,
}));

vi.mock('@/utils/download', () => ({
  downloadBlob: mocks.downloadBlob,
}));

const sessionList = (overrides: Partial<UsageImportSessionList> = {}): UsageImportSessionList => ({
  sessions: [],
  total: 0,
  status_counts: {},
  active_sessions: 0,
  max_sessions: 2,
  chunk_size_bytes: 4 * 1024 * 1024,
  disk_quota_bytes: 16 * 1024 * 1024 * 1024,
  ttl_seconds: 24 * 60 * 60,
  ...overrides,
});

const getText = (node: ReactTestInstance): string =>
  node.children
    .map((child) =>
      typeof child === 'string' || typeof child === 'number' ? String(child) : getText(child)
    )
    .join('');

const findButton = (renderer: ReactTestRenderer, text: string) =>
  renderer.root.findAllByType('button').find((button) => getText(button).includes(text));

const flush = async () => {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
};

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
};

const renderView = async () => {
  let renderer!: ReactTestRenderer;
  await act(async () => {
    renderer = create(
      <UsageMaintenanceTransferView
        serviceBase="http://manager.local"
        managementKey="manager-key"
        onBack={vi.fn()}
      />
    );
  });
  await flush();
  return renderer;
};

beforeEach(() => {
  vi.clearAllMocks();
  mocks.listUsageImportSessions.mockResolvedValue(sessionList());
  mocks.uploadUsageImportFile.mockResolvedValue({
    format: 'jsonl',
    added: 3,
    skipped: 1,
    total: 4,
    failed: 0,
    unsupported: 0,
    warnings: [],
  });
});

afterEach(() => {
  vi.useRealTimers();
});

describe('UsageMaintenanceTransferView', () => {
  it('loads session limits and downloads a sanitized export', async () => {
    mocks.exportUsage.mockResolvedValue({
      filename: 'usage-events.jsonl',
      blob: new Blob(['{"event_hash":"safe"}\n'], { type: 'application/x-ndjson' }),
    });
    const renderer = await renderView();

    expect(getText(renderer.root)).toContain('16.00 GB');
    const exportButton = findButton(renderer, 'Export sanitized JSONL');
    expect(exportButton).toBeDefined();
    await act(async () => {
      await exportButton?.props.onClick();
    });

    expect(mocks.exportUsage).toHaveBeenCalledWith('http://manager.local', 'manager-key');
    expect(mocks.downloadBlob).toHaveBeenCalledWith({
      filename: 'usage-events.jsonl',
      blob: expect.any(Blob),
    });
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'Sanitized usage JSONL export downloaded.',
      'success'
    );
    act(() => renderer.unmount());
  });

  it('confirms a supported file before starting the resumable import', async () => {
    const renderer = await renderView();
    const input = renderer.root.findByProps({ type: 'file' });
    const file = new File(['{"event_hash":"safe"}\n'], 'history.jsonl', {
      type: 'application/x-ndjson',
    });

    act(() => input.props.onChange({ target: { files: [file], value: 'history.jsonl' } }));
    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as {
      onConfirm: () => Promise<void>;
    };
    expect(confirmation).toBeDefined();
    expect(confirmation).toHaveProperty('onConfirm');

    await act(async () => {
      await confirmation.onConfirm();
    });

    expect(mocks.uploadUsageImportFile).toHaveBeenCalledWith(
      expect.objectContaining({
        base: 'http://manager.local',
        managementKey: 'manager-key',
        file,
        sessionId: undefined,
      })
    );
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'Import complete: added 3, skipped 1, failed 0.',
      'success'
    );
    act(() => renderer.unmount());
  });

  it('maps session-list errors to safe localized copy', async () => {
    mocks.listUsageImportSessions.mockRejectedValueOnce({
      code: 'usage_import_session_quota_exceeded',
    });
    const renderer = await renderView();

    expect(getText(renderer.root)).toContain(
      'The Manager Server import disk quota is currently reserved by other sessions.'
    );
    expect(getText(renderer.root)).not.toContain('usage_import_session_quota_exceeded');
    act(() => renderer.unmount());
  });

  it('does not offer retry after a non-retryable processing failure', async () => {
    mocks.uploadUsageImportFile.mockRejectedValueOnce(
      new UsageImportFailedError({} as UsageImportSession)
    );
    const renderer = await renderView();
    const input = renderer.root.findByProps({ type: 'file' });
    const file = new File(['{}\n'], 'history.jsonl');

    act(() => input.props.onChange({ target: { files: [file], value: 'history.jsonl' } }));
    const confirmation = mocks.showConfirmation.mock.calls[0][0] as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      await confirmation.onConfirm();
    });

    expect(findButton(renderer, 'Resume upload')).toBeUndefined();
    act(() => renderer.unmount());
  });

  it('aborts an in-flight session-list request on unmount', async () => {
    const pending = deferred<UsageImportSessionList>();
    let signal: AbortSignal | undefined;
    mocks.listUsageImportSessions.mockImplementationOnce(
      (_base: string, _key: string, _options: unknown, requestSignal: AbortSignal) => {
        signal = requestSignal;
        return pending.promise;
      }
    );
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <UsageMaintenanceTransferView
          serviceBase="http://manager.local"
          managementKey="manager-key"
          onBack={vi.fn()}
        />
      );
      await Promise.resolve();
    });

    expect(signal?.aborted).toBe(false);
    act(() => renderer.unmount());
    expect(signal?.aborted).toBe(true);
    pending.resolve(sessionList());
    await pending.promise;
  });
});
