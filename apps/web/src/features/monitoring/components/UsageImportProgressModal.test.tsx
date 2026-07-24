import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import type { UsageImportProgress } from '@/features/monitoring/services/usageImportSession';
import { UsageImportProgressModal } from './UsageImportProgressModal';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => key,
  }),
}));

const baseProgress: UsageImportProgress = {
  sessionId: 'session-1',
  filename: 'history.jsonl',
  phase: 'paused',
  uploadedBytes: 4,
  totalBytes: 10,
  percent: 40,
};

const renderModal = (progress: UsageImportProgress, busy = false) =>
  renderToStaticMarkup(
    <UsageImportProgressModal
      open
      progress={progress}
      busy={busy}
      onPause={vi.fn()}
      onResume={vi.fn()}
      onCancel={vi.fn()}
      onClose={vi.fn()}
    />
  );

describe('UsageImportProgressModal', () => {
  it('does not offer retry for a non-retryable server failure', () => {
    const markup = renderModal({
      ...baseProgress,
      phase: 'failed',
      retryable: false,
      error: 'invalid payload',
      result: { format: 'usage_service_jsonl', added: 2, skipped: 1, total: 4, failed: 1 },
    });

    expect(markup).not.toContain('common.retry');
    expect(markup).toContain('usage_stats.import_cancel');
    expect(markup).toContain('usage_stats.import_partial_result');
    expect(markup).toContain('role="alert"');
  });

  it('disables modal actions while cancellation is in flight', () => {
    const markup = renderModal(baseProgress, true);

    expect(markup).toContain('usage_stats.import_resume');
    expect(markup.match(/disabled=""/g)?.length).toBeGreaterThanOrEqual(3);
  });

  it('offers retry only when a failed attempt is explicitly retryable', () => {
    const markup = renderModal({
      ...baseProgress,
      phase: 'failed',
      status: 'uploading',
      retryable: true,
      error: 'network down',
    });

    expect(markup).toContain('common.retry');
  });

  it('makes it clear that pausing during processing only pauses monitoring', () => {
    const processing = renderModal({
      ...baseProgress,
      phase: 'processing',
      status: 'processing',
      uploadedBytes: 10,
      percent: 100,
    });
    const paused = renderModal({
      ...baseProgress,
      phase: 'paused',
      status: 'processing',
      uploadedBytes: 10,
      percent: 100,
    });

    expect(processing).toContain('usage_stats.import_pause_monitoring');
    expect(paused).toContain('usage_stats.import_processing_paused_hint');
  });

  it('keeps a cancelled partial result visible until the user closes the modal', () => {
    const markup = renderModal({
      ...baseProgress,
      phase: 'cancelled',
      status: 'cancelled',
      result: { format: 'usage_service_jsonl', added: 2, skipped: 1, total: 4, failed: 1 },
    });

    expect(markup).toContain('usage_stats.import_partial_result');
    expect(markup).toContain('common.close');
    expect(markup).not.toContain('usage_stats.import_resume');
  });
});
