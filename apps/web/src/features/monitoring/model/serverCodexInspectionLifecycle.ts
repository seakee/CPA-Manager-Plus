import type { TFunction } from 'i18next';
import type { CodexInspectionRun } from '@/services/api/usageService';

export function getRunStatusLabel(run: CodexInspectionRun | null | undefined, t: TFunction) {
  switch (run?.status) {
    case 'completed':
      return t('monitoring.codex_inspection_status_success');
    case 'failed':
      return t('monitoring.codex_inspection_status_error');
    case 'running':
      return t('monitoring.codex_inspection_status_running');
    case 'cancelling':
      return t('monitoring.codex_inspection_status_cancelling');
    case 'cancelled':
      return t('monitoring.codex_inspection_status_cancelled');
    case 'interrupted':
      return t('monitoring.codex_inspection_status_interrupted');
    default:
      return run?.status
        ? t('monitoring.codex_inspection_status_unknown', { status: run.status })
        : t('monitoring.codex_inspection_status_idle');
  }
}

export function isActiveRun(run?: CodexInspectionRun | null): boolean {
  if (!run) return false;
  const activeStatus = run.status === 'running' || run.status === 'cancelling';
  if (typeof run.active === 'boolean') return run.active && activeStatus;
  return activeStatus;
}

export function isCancellableRun(run?: CodexInspectionRun | null): boolean {
  if (!run || !isActiveRun(run)) return false;
  // A cancellation committed by another Manager Server instance is not owned
  // by this process, but the UI must still expose its disabled "cancelling"
  // action instead of making the in-progress transition disappear.
  if (run.status === 'cancelling') return true;
  // Older Manager Server versions do not expose a cancellation capability or
  // endpoint. Be conservative when the capability field is absent so a stale
  // historical `running` row never gains an action that cannot succeed.
  return run.cancellable === true;
}

export function hasActiveRun(
  runs: CodexInspectionRun[],
  fallback?: CodexInspectionRun | null
): boolean {
  if (runs.some(isActiveRun)) return true;
  if (!fallback) return false;
  // The list response carries the newest lease-backed activity fields. When it
  // already contains this run, an older detail response must not resurrect it
  // as active after lease loss or terminal finalization.
  if (runs.some((run) => run.id === fallback.id)) return false;
  return isActiveRun(fallback);
}

export function findCancellableRun(
  runs: CodexInspectionRun[],
  fallback?: CodexInspectionRun | null
): CodexInspectionRun | null {
  const active = runs.find(isCancellableRun);
  if (active) return active;
  if (!fallback || runs.some((run) => run.id === fallback.id)) return null;
  return isCancellableRun(fallback) ? fallback : null;
}
