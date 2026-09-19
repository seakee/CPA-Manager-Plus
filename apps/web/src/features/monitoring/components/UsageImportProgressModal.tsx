import { useTranslation } from 'react-i18next';
import { Modal } from '@/components/ui/Modal';
import {
  UsageImportProgressActions,
  UsageImportProgressView,
} from '@/components/usage/UsageImportProgressView';
import type { UsageImportProgress } from '@/features/monitoring/services/usageImportSession';
import styles from '../MonitoringCenterPage.module.scss';

type UsageImportProgressModalProps = {
  open: boolean;
  progress: UsageImportProgress | null;
  busy?: boolean;
  onPause: () => void;
  onResume: () => void;
  onCancel: () => void;
  onClose: () => void;
};

export function UsageImportProgressModal({
  open,
  progress,
  busy = false,
  onPause,
  onResume,
  onCancel,
  onClose,
}: UsageImportProgressModalProps) {
  const { t } = useTranslation();
  if (!progress) return null;
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('usage_stats.import_progress_title')}
      width={560}
      className={styles.monitorModal}
      closeDisabled={['preparing', 'uploading', 'processing'].includes(progress.phase) || busy}
      footer={
        <UsageImportProgressActions
          progress={progress}
          busy={busy}
          onPause={onPause}
          onResume={onResume}
          onCancel={onCancel}
          onClose={onClose}
        />
      }
    >
      <UsageImportProgressView progress={progress} />
    </Modal>
  );
}
