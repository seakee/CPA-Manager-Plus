import type {
  UsageArchiveRunSummary,
  UsageArchiveStatusSummary,
  UsageImportResponse,
  UsageImportSession,
  UsageImportSessionStatus,
  UsageMaintenanceCoverage,
} from '@/services/api/usageService';

export const retentionPresetDays = [7, 30, 90] as const;

export type RetentionPresetDays = (typeof retentionPresetDays)[number];
export type RetentionSelection = RetentionPresetDays | 'custom';

export type UsageMaintenanceTab = 'archives' | 'transfer' | 'diagnostics';
export type MaintenanceIntent = 'archive' | 'cleanup';

export type ArchiveRunPresentationStage =
  | 'archiving'
  | 'verifying'
  | 'delete_ready'
  | 'deleting'
  | 'completed'
  | 'attention';

export type ArchiveHistoryFilter =
  | 'all'
  | 'previewed'
  | 'archiving'
  | 'archived'
  | 'verifying'
  | 'verified'
  | 'deleting'
  | 'completed'
  | 'failed'
  | 'cancelled';

export type ArchiveHistorySource = 'all' | 'manual' | 'retention';

export type ArchiveRunAction = 'resume' | 'verify' | 'delete' | 'cancel';

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

export interface ArchiveProgressPresentation {
  phase: string;
  labelKey: string;
  current: number;
  total: number;
  unit: 'events' | 'segments' | null;
  percent: number | null;
  updatedAtMS?: number;
}

const archiveProgressLabels: Record<string, string> = {
  archiving_records: 'usage_maintenance.progress_archiving_records',
  archive_finalizing: 'usage_maintenance.progress_archive_finalizing',
  archive_publishing: 'usage_maintenance.progress_archive_publishing',
  verifying_archive: 'usage_maintenance.progress_verifying_archive',
  cleanup_revalidating: 'usage_maintenance.progress_cleanup_revalidating',
  deleting_records: 'usage_maintenance.progress_deleting_records',
};

export const resolveArchiveProgressPresentation = (
  run: UsageArchiveRunSummary
): ArchiveProgressPresentation | null => {
  let phase = run.progress?.phase;
  let current = run.progress?.current ?? 0;
  let total = run.progress?.total ?? 0;
  let unit: 'events' | 'segments' | null =
    run.progress?.unit === 'events' || run.progress?.unit === 'segments' ? run.progress.unit : null;
  if (!phase || !archiveProgressLabels[phase]) {
    if (run.status === 'archiving' || run.resume_status === 'archiving') {
      phase =
        run.event_count > 0 && run.archived_event_count >= run.event_count
          ? 'archive_publishing'
          : 'archiving_records';
      current = run.archived_event_count;
      total = phase === 'archiving_records' ? run.event_count : 0;
      unit = phase === 'archiving_records' ? 'events' : null;
    } else if (run.status === 'verifying' || run.resume_status === 'verifying') {
      phase = 'verifying_archive';
    } else if (run.status === 'deleting' || run.resume_status === 'deleting') {
      phase = run.deleted_event_count > 0 ? 'deleting_records' : 'cleanup_preparing';
      current = run.deleted_event_count;
      total = phase === 'deleting_records' ? run.event_count : 0;
      unit = phase === 'deleting_records' ? 'events' : null;
    } else {
      return null;
    }
  }
  return {
    phase,
    labelKey: archiveProgressLabels[phase] ?? 'usage_maintenance.progress_cleanup_preparing',
    current,
    total,
    unit,
    percent: resolveProgressPercent(current, total),
    updatedAtMS: run.progress?.updated_at_ms,
  };
};

export const pickFreshestArchiveStatus = (
  selected: UsageArchiveStatusSummary | null,
  operation: UsageArchiveStatusSummary | null
): UsageArchiveStatusSummary | null => {
  if (!selected) return operation;
  if (!operation || selected.run.id !== operation.run.id) return selected;
  const freshness = (status: UsageArchiveStatusSummary) =>
    Math.max(status.run.progress?.updated_at_ms ?? 0, status.run.updated_at_ms);
  const selectedTime = freshness(selected);
  const operationTime = freshness(operation);
  if (selectedTime !== operationTime) return selectedTime > operationTime ? selected : operation;
  const statusOrder = [
    'previewed',
    'archiving',
    'archived',
    'verifying',
    'verified',
    'deleting',
    'completed',
  ];
  const selectedStatus = statusOrder.indexOf(selected.run.status);
  const operationStatus = statusOrder.indexOf(operation.run.status);
  if (selectedStatus !== operationStatus && selectedStatus >= 0 && operationStatus >= 0) {
    return selectedStatus > operationStatus ? selected : operation;
  }
  const phaseOrder = [
    'archiving_records',
    'archive_finalizing',
    'archive_publishing',
    'verifying_archive',
    'cleanup_revalidating',
    'deleting_records',
  ];
  const selectedPhase = phaseOrder.indexOf(selected.run.progress?.phase ?? '');
  const operationPhase = phaseOrder.indexOf(operation.run.progress?.phase ?? '');
  if (selectedPhase !== operationPhase && selectedPhase >= 0 && operationPhase >= 0) {
    return selectedPhase > operationPhase ? selected : operation;
  }
  if (
    selectedPhase === operationPhase &&
    selected.run.progress?.current !== operation.run.progress?.current
  ) {
    return (selected.run.progress?.current ?? 0) > (operation.run.progress?.current ?? 0)
      ? selected
      : operation;
  }
  return operation;
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

export const isArchiveRunCancellable = (run: {
  status: string;
  resume_status?: string;
  requested_stage?: string;
  archived_event_count: number;
  deleted_event_count: number;
  last_deleted_event_id?: number;
  delete_started_at_ms?: number;
}): boolean => {
  if (
    run.deleted_event_count > 0 ||
    (run.last_deleted_event_id ?? 0) > 0 ||
    (run.delete_started_at_ms ?? 0) > 0
  ) {
    return false;
  }
  if (run.status === 'deleting' || run.resume_status === 'deleting') return false;
  if (run.status === 'failed') {
    if (run.resume_status === 'archiving' || run.resume_status === 'verifying') {
      return true;
    }
    if (!run.resume_status) {
      return run.archived_event_count <= 0;
    }
    return false;
  }
  if (run.archived_event_count > 0) return false;
  if (run.status === 'previewed') return true;
  return false;
};

export const isArchiveRunActiveStatus = (status?: string | null): boolean =>
  status === 'previewed' ||
  status === 'archiving' ||
  status === 'verifying' ||
  status === 'deleting';


export const isRecentCutoffWarning = (
  intent: MaintenanceIntent,
  cutoffTimestampMS: number | null,
  nowMS: number
): boolean =>
  intent === 'cleanup' &&
  cutoffTimestampMS !== null &&
  nowMS - cutoffTimestampMS < 14 * dayMS;

export type ArchiveStepName = 'archive' | 'verify' | 'delete';

export interface ArchiveStepState {
  step: ArchiveStepName;
  complete: boolean;
  current: boolean;
  failed: boolean;
  running: boolean;
}

export const computeArchiveSteps = (
  run: {
    status: string;
    resume_status?: string;
    has_error: boolean;
  },
  intent: MaintenanceIntent = 'archive',
  working = false
): ArchiveStepState[] => {
  const deleting =
    run.status === 'deleting' || run.resume_status === 'deleting' || run.status === 'completed';
  const steps: ArchiveStepName[] =
    deleting || intent === 'cleanup' ? ['archive', 'verify', 'delete'] : ['archive', 'verify'];
  const stepOrder = deleting
    ? run.status === 'completed'
      ? 4
      : 3
    : run.status === 'verified'
      ? 3
      : run.status === 'verifying' || run.status === 'archived' || run.resume_status === 'verifying'
        ? 2
        : 1;

  return steps.map((step, index) => {
    const complete = stepOrder > index + 1;
    const current = stepOrder === index + 1;
    const failed = current && (run.status === 'failed' || run.has_error);
    const running =
      !failed &&
      run.status !== 'cancelled' &&
      run.status !== 'completed' &&
      ((step === 'archive' &&
        (run.status === 'archiving' ||
          (working && current && run.status === 'previewed'))) ||
        (step === 'verify' &&
          (run.status === 'verifying' || (working && current && run.status === 'archived'))) ||
        (step === 'delete' &&
          (run.status === 'deleting' || (working && current && run.status === 'verified'))));
    return { step, complete, current, failed, running };
  });
};

export type ArchiveMilestoneKey =
  | 'created'
  | 'started'
  | 'archived'
  | 'verified'
  | 'delete_started'
  | 'completed';

const MILESTONE_FIELDS: ReadonlyArray<readonly [keyof UsageArchiveRunSummary, ArchiveMilestoneKey]> =
  [
    ['created_at_ms', 'created'],
    ['started_at_ms', 'started'],
    ['archived_at_ms', 'archived'],
    ['verified_at_ms', 'verified'],
    ['delete_started_at_ms', 'delete_started'],
    ['completed_at_ms', 'completed'],
  ];

export const computeRunMilestones = (
  run: UsageArchiveRunSummary
): Array<{ key: ArchiveMilestoneKey; timestampMS: number }> => {
  const items: Array<{ key: ArchiveMilestoneKey; timestampMS: number }> = [];
  for (const [field, key] of MILESTONE_FIELDS) {
    const value = run[field];
    if (typeof value === 'number' && value > 0) {
      items.push({ key, timestampMS: value });
    }
  }
  return items;
};

export const computeCoveragePercent = (
  coverage?: UsageMaintenanceCoverage
): number | null => {
  if (!coverage) return null;
  if (coverage.complete) return 100;
  return resolveProgressPercent(coverage.watermark_event_id, coverage.target_event_id);
};

export const filterArchiveRunsByQuery = (
  runs: UsageArchiveRunSummary[],
  query: string
): UsageArchiveRunSummary[] => {
  const normalized = query.trim().toLowerCase();
  if (!normalized) return runs;
  return runs.filter(
    (run) =>
      run.id.toLowerCase().includes(normalized) ||
      run.mode.toLowerCase().includes(normalized) ||
      run.status.toLowerCase().includes(normalized)
  );
};

export const sumStatusCounts = (counts?: Record<string, number>): number | null => {
  if (!counts) return null;
  return Object.values(counts).reduce((sum, value) => sum + value, 0);
};

export const importSessionProgressPercent = (session: UsageImportSession): number => {
  if (session.size_bytes <= 0) return 0;
  return Math.min(
    100,
    Math.max(0, Math.floor((session.received_bytes / session.size_bytes) * 100))
  );
};

export const hasImportIssues = (result?: UsageImportResponse): boolean =>
  Boolean((result?.failed ?? 0) > 0 || (result?.unsupported ?? 0) > 0 || result?.warnings?.length);

const RESUMABLE_IMPORT_STATUSES: Partial<Record<UsageImportSessionStatus, true>> = {
  uploading: true,
  ready: true,
};
const ACTIVE_IMPORT_STATUSES: Partial<Record<UsageImportSessionStatus, true>> = {
  uploading: true,
  ready: true,
  processing: true,
};

export const isActiveImportSessionStatus = (status: UsageImportSessionStatus): boolean =>
  Boolean(ACTIVE_IMPORT_STATUSES[status]);

export const isResumableImportSession = (session: UsageImportSession): boolean =>
  Boolean(RESUMABLE_IMPORT_STATUSES[session.status]) ||
  (session.status === 'failed' && session.retryable === true);

export const isActionableImportSession = (session: UsageImportSession): boolean =>
  session.status === 'processing' || isResumableImportSession(session);
export const parseUsageMaintenanceTab = (raw: string | null): UsageMaintenanceTab => {
  if (raw === 'transfer' || raw === 'diagnostics') return raw;
  return 'archives';
};
