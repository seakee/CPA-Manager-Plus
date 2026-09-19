import { act, useState, type ReactNode } from 'react';
import en from '@/i18n/locales/en.json';
import { create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { UsageImportSession, UsageImportSessionList } from '@/services/api/usageService';
import {
  UsageImportFailedError,
  type UsageImportProgress,
} from '@/features/monitoring/services/usageImportSession';
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
      onUsageChanged: vi.fn(),
      language: 'en',
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

vi.mock('react-i18next', () => {
  const t = (key: string, options?: Record<string, unknown>) => {
    const localized = key.startsWith('usage_maintenance.')
      ? en.usage_maintenance[key.split('.')[1] as keyof typeof en.usage_maintenance]
      : undefined;
    return mocks.t(key, { ...(localized ? { defaultValue: localized } : {}), ...options });
  };
  const translatedT = (key: string, options?: Record<string, unknown>) => t(key, options);
  return {
    useTranslation: () => ({
      i18n: { language: mocks.language },
      t: mocks.language === 'en' ? t : translatedT,
    }),
  };
});

vi.mock('@/components/ui/Drawer', () => ({
  Drawer: ({
    open,
    title,
    children,
    footer,
    onClose,
  }: {
    open: boolean;
    title: ReactNode;
    children: ReactNode;
    footer: ReactNode;
    onClose: () => void;
  }) =>
    open ? (
      <div data-testid="transfer-drawer">
        <strong>{title}</strong>
        {children}
        <div data-testid="transfer-footer">{footer}</div>
        <button onClick={onClose}>Close drawer</button>
      </div>
    ) : null,
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

function TransferHarness({
  serviceBase,
  managementKey,
}: {
  serviceBase: string;
  managementKey?: string;
}) {
  const [panel, setPanel] = useState<'import' | 'import-session' | 'export' | null>('import');
  const [sessionId, setSessionId] = useState<string | null>(null);
  return (
    <>
      <button onClick={() => setPanel('export')}>Open export</button>
      <button onClick={() => setPanel('import')}>Open import</button>
      <UsageMaintenanceTransferView
        serviceBase={serviceBase}
        managementKey={managementKey}
        panel={panel}
        sessionId={sessionId}
        refreshToken={0}
        onUsageChanged={mocks.onUsageChanged}
        onOpenPanel={(value, id) => {
          setPanel(value);
          setSessionId(id ?? null);
        }}
        onClosePanel={() => {
          setPanel(null);
          setSessionId(null);
        }}
      />
    </>
  );
}

const getImportConfirmation = (renderer: ReactTestRenderer) => ({
  onConfirm: findButton(renderer, 'Start import')!.props.onClick,
});

const renderView = async () => {
  let renderer!: ReactTestRenderer;
  await act(async () => {
    renderer = create(
      <TransferHarness serviceBase="http://manager.local" managementKey="manager-key" />
    );
  });
  await flush();
  return renderer;
};

beforeEach(() => {
  vi.clearAllMocks();
  mocks.language = 'en';
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
  it.each(['uploading', 'processing'] as const)(
    'keeps an active %s import running when the language changes',
    async (phase) => {
      const result = { format: 'jsonl', added: 1, skipped: 0, total: 1, failed: 0 };
      const pending = deferred<typeof result>();
      const file = new File(['{}\n'], 'language-change.jsonl');
      let reportProgress!: (value: UsageImportProgress) => void;
      const progress: UsageImportProgress = {
        filename: file.name,
        sessionId: 'language-change',
        phase,
        status: phase,
        uploadedBytes: phase === 'processing' ? file.size : 0,
        totalBytes: file.size,
        percent: phase === 'processing' ? 100 : 0,
      };
      mocks.uploadUsageImportFile.mockImplementationOnce(
        ({ onProgress }: { onProgress: typeof reportProgress }) => {
          reportProgress = onProgress;
          reportProgress(progress);
          return pending.promise;
        }
      );
      const renderer = await renderView();
      try {
        act(() =>
          renderer.root
            .findByProps({ type: 'file' })
            .props.onChange({ target: { files: [file], value: file.name } })
        );
        act(() => getImportConfirmation(renderer).onConfirm());
        mocks.language = 'zh-CN';
        await act(async () =>
          renderer.update(
            <TransferHarness serviceBase="http://manager.local" managementKey="manager-key" />
          )
        );

        expect(mocks.uploadUsageImportFile.mock.calls[0][0].signal.aborted).toBe(false);
        expect(mocks.uploadUsageImportFile).toHaveBeenCalledTimes(1);
        await act(async () => {
          reportProgress({ ...progress, phase: 'completed', status: 'completed', result });
          pending.resolve(result);
        });
        await flush();
        expect(getText(renderer.root)).toContain('usage_stats.import_phase_completed');
        expect(mocks.onUsageChanged).toHaveBeenCalledTimes(1);
      } finally {
        act(() => renderer.unmount());
        pending.resolve(result);
        await flush();
      }
    }
  );

  it('refreshes usage after an import finishes with its drawer closed', async () => {
    const pending = deferred<{
      format: string;
      added: number;
      skipped: number;
      total: number;
      failed: number;
    }>();
    mocks.uploadUsageImportFile.mockReturnValueOnce(pending.promise);
    const renderer = await renderView();
    const file = new File(['{}\n'], 'background-import.jsonl');
    act(() =>
      renderer.root
        .findByProps({ type: 'file' })
        .props.onChange({ target: { files: [file], value: file.name } })
    );
    await act(async () => getImportConfirmation(renderer).onConfirm());
    act(() => findButton(renderer, 'Close drawer')!.props.onClick());
    expect(mocks.onUsageChanged).not.toHaveBeenCalled();
    expect(mocks.uploadUsageImportFile.mock.calls[0][0].signal.aborted).toBe(false);

    await act(async () =>
      pending.resolve({ format: 'jsonl', added: 12, skipped: 0, total: 12, failed: 0 })
    );
    await flush();

    expect(mocks.onUsageChanged).toHaveBeenCalledTimes(1);
    expect(renderer.root.findAllByProps({ 'data-testid': 'transfer-drawer' })).toHaveLength(0);
    act(() => renderer.unmount());
  });

  it('does not refresh a new service when an old import finishes late', async () => {
    const pending = deferred<{
      format: string;
      added: number;
      skipped: number;
      total: number;
      failed: number;
    }>();
    mocks.uploadUsageImportFile.mockReturnValueOnce(pending.promise);
    const renderer = await renderView();
    const file = new File(['{}\n'], 'old-service-import.jsonl');
    act(() =>
      renderer.root
        .findByProps({ type: 'file' })
        .props.onChange({ target: { files: [file], value: file.name } })
    );
    await act(async () => getImportConfirmation(renderer).onConfirm());
    await act(async () =>
      renderer.update(
        <TransferHarness serviceBase="http://manager-new.local" managementKey="new-key" />
      )
    );
    expect(mocks.uploadUsageImportFile.mock.calls[0][0].signal.aborted).toBe(true);

    await act(async () =>
      pending.resolve({ format: 'jsonl', added: 12, skipped: 0, total: 12, failed: 0 })
    );
    await flush();

    expect(mocks.onUsageChanged).not.toHaveBeenCalled();
    act(() => renderer.unmount());
  });

  it('keeps a fully uploaded file in processing and resumes its view after minimizing', async () => {
    const pending = deferred<{
      format: string;
      added: number;
      skipped: number;
      total: number;
      failed: number;
    }>();
    const file = new File(['{}\n'], 'processing.jsonl');
    mocks.uploadUsageImportFile.mockImplementationOnce(
      ({ onProgress }: { onProgress: (progress: UsageImportProgress) => void }) => {
        onProgress({
          filename: file.name,
          sessionId: 'processing-import',
          phase: 'processing',
          status: 'processing',
          uploadedBytes: file.size,
          totalBytes: file.size,
          percent: 100,
        });
        return pending.promise;
      }
    );
    const renderer = await renderView();
    act(() =>
      renderer.root
        .findByProps({ type: 'file' })
        .props.onChange({ target: { files: [file], value: file.name } })
    );
    await act(async () => getImportConfirmation(renderer).onConfirm());
    expect(getText(renderer.root)).toContain('100%');
    expect(getText(renderer.root)).toContain('usage_stats.import_processing_hint');
    expect(getText(renderer.root)).not.toContain('Import complete');
    act(() => findButton(renderer, 'Minimize')!.props.onClick());
    expect(renderer.root.findAllByProps({ 'data-testid': 'transfer-drawer' })).toHaveLength(0);
    act(() => findButton(renderer, 'Open import')!.props.onClick());
    expect(getText(renderer.root)).toContain(file.name);
    expect(mocks.uploadUsageImportFile).toHaveBeenCalledTimes(1);
    act(() => renderer.unmount());
    pending.resolve({ format: 'jsonl', added: 0, skipped: 0, total: 0, failed: 0 });
    await flush();
  });

  it('distinguishes completed imports with unsupported records and warnings', async () => {
    const session: UsageImportSession = {
      id: 'import-with-issues',
      filename: 'history.jsonl',
      status: 'completed',
      size_bytes: 10,
      received_bytes: 10,
      chunk_size_bytes: 10,
      created_at_ms: 1,
      updated_at_ms: 2,
      expires_at_ms: 3,
      result: {
        format: 'jsonl',
        added: 3,
        skipped: 1,
        total: 5,
        failed: 0,
        unsupported: 1,
        warnings: ['One record uses an unsupported format.'],
      },
    };
    mocks.listUsageImportSessions.mockResolvedValueOnce(
      sessionList({ sessions: [session], total: 1 })
    );
    const renderer = await renderView();
    const row = renderer.root.findByProps({ 'data-session-id': session.id });
    expect(getText(row)).toContain('Completed with issues');
    expect(getText(row)).toContain('unsupported 1');
    act(() => findButton(renderer, 'Details')!.props.onClick());
    expect(getText(renderer.root.findByProps({ 'data-testid': 'transfer-drawer' }))).toContain(
      'One record uses an unsupported format.'
    );
    act(() => renderer.unmount());
  });

  it('revokes a file confirmation when the drawer closes', async () => {
    const renderer = await renderView();
    const file = new File(['{}'], 'cancelled-choice.jsonl');
    act(() =>
      renderer.root
        .findByProps({ type: 'file' })
        .props.onChange({ target: { files: [file], value: file.name } })
    );
    const confirmation = getImportConfirmation(renderer);
    act(() => findButton(renderer, 'Close drawer')!.props.onClick());
    act(() => confirmation.onConfirm());
    expect(mocks.uploadUsageImportFile).not.toHaveBeenCalled();
    act(() => renderer.unmount());
  });

  it('loads session limits and downloads a sanitized export', async () => {
    mocks.exportUsage.mockResolvedValue({
      filename: 'usage-events.jsonl',
      blob: new Blob(['{"event_hash":"safe"}\n'], { type: 'application/x-ndjson' }),
    });
    const renderer = await renderView();

    expect(getText(renderer.root)).toContain('16.00 GB');
    act(() => findButton(renderer, 'Open export')!.props.onClick());
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

  it('keeps server-provided upload limits available in technical details', async () => {
    mocks.listUsageImportSessions.mockResolvedValueOnce(
      sessionList({ chunk_size_bytes: 7 * 1024 * 1024 })
    );
    const renderer = await renderView();

    const details = renderer.root.findByType('details');
    expect(details.props.open).not.toBe(true);
    expect(getText(details)).toContain('7.00 MB');
    expect(getText(renderer.root)).toContain('Server import quota: 16.00 GB');
    act(() => renderer.unmount());
  });

  it('reports export failure as export failure and allows retry without a fake download', async () => {
    mocks.exportUsage.mockRejectedValueOnce(new Error('503 raw upstream failure'));
    const renderer = await renderView();
    act(() => findButton(renderer, 'Open export')!.props.onClick());
    await act(async () => findButton(renderer, 'Export sanitized JSONL')!.props.onClick());

    expect(mocks.showNotification).toHaveBeenCalledWith(
      'The export request could not be completed. Please retry.',
      'error'
    );
    expect(mocks.showNotification).not.toHaveBeenCalledWith(expect.anything(), 'success');
    expect(mocks.downloadBlob).not.toHaveBeenCalled();
    expect(findButton(renderer, 'Export sanitized JSONL')!.props.disabled).toBeFalsy();

    mocks.exportUsage.mockResolvedValueOnce({
      filename: 'retry.jsonl',
      blob: new Blob(['{}\n']),
    });
    await act(async () => findButton(renderer, 'Export sanitized JSONL')!.props.onClick());
    expect(mocks.downloadBlob).toHaveBeenCalledTimes(1);
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'Sanitized usage JSONL export downloaded.',
      'success'
    );
    act(() => renderer.unmount());
  });

  it('shows a dedicated file-mismatch message and never starts a chunk upload', async () => {
    mocks.uploadUsageImportFile.mockRejectedValueOnce({
      code: 'usage_import_session_file_mismatch',
      message: 'raw server mismatch',
    });
    const renderer = await renderView();
    const input = renderer.root.findByProps({ type: 'file' });
    const file = new File(['different prefix'], 'history.jsonl');

    act(() => input.props.onChange({ target: { files: [file], value: 'history.jsonl' } }));
    const confirmation = getImportConfirmation(renderer) as {
      onConfirm: () => Promise<void>;
    };
    await act(async () => {
      await confirmation.onConfirm();
    });

    expect(getText(renderer.root)).toContain(
      'The selected file does not match the uploaded session prefix. Choose the original file or start a new import.'
    );
    expect(mocks.uploadUsageImportFile).toHaveBeenCalledTimes(1);
    act(() => renderer.unmount());
  });

  it('confirms a supported file before starting the resumable import', async () => {
    const renderer = await renderView();
    const input = renderer.root.findByProps({ type: 'file' });
    const file = new File(['{"event_hash":"safe"}\n'], 'history.jsonl', {
      type: 'application/x-ndjson',
    });

    act(() => input.props.onChange({ target: { files: [file], value: 'history.jsonl' } }));
    const confirmation = getImportConfirmation(renderer) as {
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
    expect(getText(renderer.root)).not.toContain('No import sessions yet.');
    expect(getText(renderer.root)).not.toContain('Current 0 / 0');
    expect(findButton(renderer, 'common.retry')).toBeDefined();
    act(() => renderer.unmount());
  });

  it('does not render a confirmed empty list or zero limits while the first request is loading', async () => {
    const pending = deferred<UsageImportSessionList>();
    mocks.listUsageImportSessions.mockReturnValueOnce(pending.promise);
    const renderer = await renderView();

    expect(getText(renderer.root)).toContain('common.loading');
    expect(getText(renderer.root)).not.toContain('No import sessions yet.');
    expect(getText(renderer.root)).not.toContain('Current 0 / 0');

    await act(async () => pending.resolve(sessionList()));
    expect(getText(renderer.root)).toContain('No import sessions yet.');
    expect(getText(renderer.root)).toContain('Current 0 / 2');
    act(() => renderer.unmount());
  });

  it('only renders the empty result after a failed request is successfully retried', async () => {
    mocks.listUsageImportSessions.mockRejectedValueOnce(new Error('503'));
    const renderer = await renderView();

    expect(renderer.root.findAllByProps({ role: 'alert' })).toHaveLength(1);
    expect(getText(renderer.root)).not.toContain('No import sessions yet.');
    expect(getText(renderer.root)).not.toContain('Current 0 / 0');
    await act(async () => findButton(renderer, 'common.retry')!.props.onClick());

    expect(mocks.listUsageImportSessions).toHaveBeenCalledTimes(2);
    expect(renderer.root.findAllByProps({ role: 'alert' })).toHaveLength(0);
    expect(getText(renderer.root)).toContain('No import sessions yet.');
    expect(getText(renderer.root)).toContain('Current 0 / 2');
    act(() => renderer.unmount());
  });

  it('preserves previously loaded rows and offers retry when a refresh fails', async () => {
    vi.useFakeTimers();
    const session: UsageImportSession = {
      id: 'existing-session',
      filename: 'existing.jsonl',
      status: 'uploading',
      size_bytes: 100,
      received_bytes: 50,
      chunk_size_bytes: 1024,
      created_at_ms: 1,
      updated_at_ms: 1,
      expires_at_ms: Date.now() + 60_000,
    };
    mocks.listUsageImportSessions
      .mockResolvedValueOnce(sessionList({ sessions: [session], total: 1, active_sessions: 1 }))
      .mockRejectedValueOnce(new Error('503'));
    const renderer = await renderView();
    await act(async () => vi.advanceTimersByTimeAsync(5000));

    expect(renderer.root.findAllByProps({ role: 'alert' })).toHaveLength(1);
    expect(getText(renderer.root)).toContain('existing.jsonl');
    expect(getText(renderer.root)).not.toContain('No import sessions yet.');
    expect(getText(renderer.root)).not.toContain('Current 0 / 0');
    expect(findButton(renderer, 'common.retry')).toBeDefined();
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
    const confirmation = getImportConfirmation(renderer) as {
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
        <TransferHarness serviceBase="http://manager.local" managementKey="manager-key" />
      );
      await Promise.resolve();
    });

    expect(signal?.aborted).toBe(false);
    act(() => renderer.unmount());
    expect(signal?.aborted).toBe(true);
    pending.resolve(sessionList());
    await pending.promise;
  });

  it.each(['cancelled', 'completed', 'completed-with-issues', 'missing'] as const)(
    'preserves the %s cancellation outcome when the aborted upload reports a late pause',
    async (outcome) => {
      const pending = deferred<{
        format: string;
        added: number;
        total: number;
        skipped: number;
        failed: number;
      }>();
      const file = new File(['{}\n'], 'history.jsonl');
      const progress: UsageImportProgress = {
        filename: file.name,
        sessionId: 'cancelled-session',
        phase: 'uploading',
        status: 'uploading',
        uploadedBytes: 0,
        totalBytes: file.size,
        percent: 0,
      };
      let reportProgress!: (value: UsageImportProgress) => void;
      mocks.uploadUsageImportFile.mockImplementationOnce(
        (options: { onProgress: typeof reportProgress }) => {
          reportProgress = options.onProgress;
          reportProgress(progress);
          return pending.promise;
        }
      );
      const completed = outcome === 'completed' || outcome === 'completed-with-issues';
      mocks.cancelUsageImportFile.mockResolvedValueOnce(
        outcome === 'missing'
          ? null
          : {
              id: progress.sessionId,
              filename: file.name,
              status: completed ? 'completed' : 'cancelled',
              size_bytes: file.size,
              received_bytes: 0,
              chunk_size_bytes: file.size,
              created_at_ms: 1,
              updated_at_ms: 2,
              expires_at_ms: 3,
              result: completed
                ? {
                    format: 'jsonl',
                    added: 1,
                    skipped: 0,
                    total: 1,
                    failed: 0,
                    warnings:
                      outcome === 'completed-with-issues' ? ['Review unsupported records.'] : [],
                  }
                : undefined,
            }
      );
      const renderer = await renderView();
      act(() =>
        renderer.root
          .findByProps({ type: 'file' })
          .props.onChange({ target: { files: [file], value: file.name } })
      );
      act(() => getImportConfirmation(renderer).onConfirm());
      act(() => findButton(renderer, 'usage_stats.import_cancel')!.props.onClick());
      expect(mocks.cancelUsageImportFile).not.toHaveBeenCalled();
      await act(async () => mocks.showConfirmation.mock.calls[0][0].onConfirm());
      const expectedPhase = completed ? 'completed' : 'cancelled';
      expect(getText(renderer.root)).toContain(`usage_stats.import_phase_${expectedPhase}`);
      expect(mocks.showNotification).toHaveBeenCalledWith(
        completed
          ? 'The import completed before cancellation took effect (1 added, 0 skipped).'
          : outcome === 'missing'
            ? 'The resumable session has expired or no longer exists.'
            : 'Import session cancelled.',
        outcome !== 'cancelled' ? 'warning' : 'success'
      );
      if (completed || outcome === 'missing') {
        expect(mocks.showNotification).not.toHaveBeenCalledWith(
          'Import session cancelled.',
          'success'
        );
      }

      act(() => reportProgress({ ...progress, phase: 'paused' }));
      expect(getText(renderer.root)).toContain(`usage_stats.import_phase_${expectedPhase}`);
      expect(findButton(renderer, 'usage_stats.import_resume')).toBeUndefined();

      await act(async () =>
        pending.resolve({ format: 'jsonl', added: 0, total: 0, skipped: 0, failed: 0 })
      );
      await flush();
      expect(getText(renderer.root)).toContain(`usage_stats.import_phase_${expectedPhase}`);
      expect(mocks.showNotification).toHaveBeenCalledTimes(1);
      expect(mocks.onUsageChanged).toHaveBeenCalledTimes(1);
      act(() => renderer.unmount());
    }
  );
});

describe('maintenance import context lifetime', () => {
  it('does not execute a saved import confirmation after unmount', async () => {
    const renderer = await renderView();
    const file = new File(['{}'], 'history.jsonl');
    act(() =>
      renderer.root
        .findByProps({ type: 'file' })
        .props.onChange({ target: { files: [file], value: file.name } })
    );
    const confirmation = getImportConfirmation(renderer);
    act(() => renderer.unmount());
    await confirmation.onConfirm();
    expect(mocks.uploadUsageImportFile).not.toHaveBeenCalled();
  });

  it('rejects an old import confirmation after the service changes and changes back', async () => {
    const renderer = await renderView();
    const file = new File(['{}'], 'history.jsonl');
    act(() =>
      renderer.root
        .findByProps({ type: 'file' })
        .props.onChange({ target: { files: [file], value: file.name } })
    );
    const confirmation = getImportConfirmation(renderer);
    await act(async () => {
      renderer.update(
        <TransferHarness serviceBase="http://other.local" managementKey="other-key" />
      );
    });
    await act(async () => {
      renderer.update(
        <TransferHarness serviceBase="http://manager.local" managementKey="manager-key" />
      );
    });
    await confirmation.onConfirm();
    expect(mocks.uploadUsageImportFile).not.toHaveBeenCalled();
    act(() => renderer.unmount());
  });

  it('does not download a late export after the service changes', async () => {
    const pending = deferred<{ filename: string; blob: Blob }>();
    mocks.exportUsage.mockReturnValueOnce(pending.promise);
    const renderer = await renderView();
    act(() => findButton(renderer, 'Open export')!.props.onClick());
    act(() => {
      void findButton(renderer, 'Export sanitized JSONL')!.props.onClick();
    });
    await act(async () => {
      renderer.update(
        <TransferHarness serviceBase="http://other.local" managementKey="other-key" />
      );
    });
    await act(async () => {
      pending.resolve({ filename: 'old.jsonl', blob: new Blob(['{}']) });
    });
    expect(mocks.downloadBlob).not.toHaveBeenCalled();
    expect(mocks.showNotification).not.toHaveBeenCalled();
    act(() => renderer.unmount());
  });
});
