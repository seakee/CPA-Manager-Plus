import type { TFunction } from 'i18next';
import { Button } from '@/components/ui/Button';
import type { CodexInspectionRun } from '@/services/api/usageService';
import { isCancellableRun } from '@/features/monitoring/model/serverCodexInspectionLifecycle';

export function CodexInspectionStopButton({
  run,
  busy,
  onClick,
  t,
}: {
  run?: CodexInspectionRun | null;
  busy: boolean;
  onClick: () => void;
  t: TFunction;
}) {
  if (!isCancellableRun(run)) return null;
  const isCancelling = run?.status === 'cancelling';
  const isStopping = busy || isCancelling;
  return (
    <Button
      variant="danger"
      size="sm"
      onClick={onClick}
      loading={busy}
      disabled={busy || isCancelling}
    >
      {isStopping
        ? t('monitoring.server_codex_inspection_cancelling')
        : t('monitoring.server_codex_inspection_stop')}
    </Button>
  );
}
