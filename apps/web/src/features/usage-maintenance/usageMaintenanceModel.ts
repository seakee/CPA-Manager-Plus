export const retentionPresetDays = [7, 30, 90] as const;

export type RetentionPresetDays = (typeof retentionPresetDays)[number];
export type RetentionSelection = RetentionPresetDays | 'custom';

export type ArchiveRunPresentationStage =
  | 'archiving'
  | 'verifying'
  | 'delete_ready'
  | 'deleting'
  | 'completed'
  | 'attention';

export type UsageMaintenanceView =
  | 'overview'
  | 'create'
  | 'active'
  | 'history'
  | 'detail'
  | 'transfer'
  | 'advanced'
  | 'diagnostics';

export type ArchiveHistoryFilter = 'all' | 'archiving' | 'archived' | 'verified' | 'failed';

export type ArchiveRunAction = 'resume' | 'verify' | 'delete';

export type RawEventRangeState =
  | { kind: 'empty' }
  | { kind: 'unavailable' }
  | {
      kind: 'available';
      minTimestampMS: number;
      maxTimestampMS: number;
    };

const dayMS = 24 * 60 * 60 * 1000;

export const toLocalDateTimeValue = (timestampMS: number) => {
  if (!Number.isFinite(timestampMS) || timestampMS <= 0) return '';
  const date = new Date(timestampMS);
  const offset = date.getTimezoneOffset() * 60_000;
  return new Date(date.getTime() - offset).toISOString().slice(0, 16);
};

export const resolveRetentionCutoff = (
  selection: RetentionSelection,
  customCutoff: string,
  nowMS: number
): number | null => {
  if (!Number.isFinite(nowMS) || nowMS <= 0) return null;
  if (selection !== 'custom') {
    const cutoffTimestampMS = nowMS - selection * dayMS;
    return cutoffTimestampMS > 0 ? cutoffTimestampMS : null;
  }

  const cutoffTimestampMS = new Date(customCutoff).getTime();
  if (!Number.isFinite(cutoffTimestampMS) || cutoffTimestampMS <= 0 || cutoffTimestampMS > nowMS) {
    return null;
  }
  return cutoffTimestampMS;
};

export const resolveRawEventRange = (status: {
  raw_event_count: number;
  raw_min_timestamp_ms?: number;
  raw_max_timestamp_ms?: number;
}): RawEventRangeState => {
  if (status.raw_event_count <= 0) return { kind: 'empty' };
  const minTimestampMS = status.raw_min_timestamp_ms;
  const maxTimestampMS = status.raw_max_timestamp_ms;
  if (
    !Number.isFinite(minTimestampMS) ||
    !Number.isFinite(maxTimestampMS) ||
    !minTimestampMS ||
    !maxTimestampMS ||
    minTimestampMS > maxTimestampMS
  ) {
    return { kind: 'unavailable' };
  }
  return { kind: 'available', minTimestampMS, maxTimestampMS };
};

export const recommendRetentionDays = (
  range: RawEventRangeState,
  nowMS: number
): RetentionPresetDays | null => {
  if (range.kind !== 'available') return null;
  return (
    [...retentionPresetDays]
      .reverse()
      .find((days) => nowMS - days * dayMS > range.minTimestampMS) ?? null
  );
};

export const getArchiveRunPresentationStage = (run: {
  status: string;
  resume_status?: string;
}): ArchiveRunPresentationStage => {
  if (run.status === 'completed') return 'completed';
  if (run.status === 'verified') return 'delete_ready';
  if (run.status === 'failed' || run.status === 'cancelled') return 'attention';
  if (run.status === 'deleting' || run.resume_status === 'deleting') return 'deleting';
  if (
    run.status === 'archived' ||
    run.status === 'verifying' ||
    run.resume_status === 'verifying'
  ) {
    return 'verifying';
  }
  return run.status === 'previewed' || run.status === 'archiving' ? 'archiving' : 'attention';
};

export const resolveProgressPercent = (completed: number, total: number): number | null => {
  if (!Number.isFinite(completed) || !Number.isFinite(total) || completed < 0 || total <= 0) {
    return null;
  }
  return Math.min(100, Math.max(0, (completed / total) * 100));
};

export const archiveHistoryFilterStatus = (filter: ArchiveHistoryFilter): string | undefined =>
  filter === 'all' ? undefined : filter;

export const getArchiveRunAction = (status: string): ArchiveRunAction | null => {
  if (
    status === 'previewed' ||
    status === 'archiving' ||
    status === 'verifying' ||
    status === 'deleting' ||
    status === 'failed'
  ) {
    return 'resume';
  }
  if (status === 'archived') return 'verify';
  if (status === 'verified') return 'delete';
  return null;
};
