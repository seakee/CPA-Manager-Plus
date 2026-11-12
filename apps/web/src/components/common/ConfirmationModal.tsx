import { useTranslation } from 'react-i18next';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { useNotificationStore } from '@/stores';

export function ConfirmationModal() {
  const { t } = useTranslation();
  const confirmation = useNotificationStore((state) => state.confirmation);
  const hideConfirmation = useNotificationStore((state) => state.hideConfirmation);
  const setConfirmationLoading = useNotificationStore((state) => state.setConfirmationLoading);
  const advanceConfirmation = useNotificationStore((state) => state.advanceConfirmation);

  const { isOpen, isLoading, step, options } = confirmation;

  if (!isOpen || !options) {
    return null;
  }

  const currentStep =
    step === 2 && options.secondConfirmation ? options.secondConfirmation : options;
  const { title, message, confirmText, cancelText, variant = 'primary' } = currentStep;

  const handleConfirm = async () => {
    if (step === 1 && options.secondConfirmation) {
      advanceConfirmation();
      return;
    }

    // Tie every write below to the confirmation this click belongs to.
    // `onConfirm` may still be running when the prompt is replaced — the page
    // that asked can unmount and another action can ask its own question — so
    // dismissing or re-enabling that new prompt would drop its decision.
    const owned = options;
    const stillOurs = () => useNotificationStore.getState().confirmation.options === owned;
    // Checked before the first write, not only after the await: a handler
    // captured before the replacement can start once the new prompt is already
    // on screen, and marking it loading would lock its buttons behind a
    // decision it never asked for.
    if (!stillOurs()) return;
    try {
      setConfirmationLoading(true);
      await options.onConfirm();
      if (!stillOurs()) return;
      hideConfirmation();
    } catch (error) {
      console.error('Confirmation action failed:', error);
      // Optional: show error notification here if needed,
      // but usually the calling component handles specific errors.
    } finally {
      if (stillOurs()) setConfirmationLoading(false);
    }
  };

  const handleCancel = () => {
    if (isLoading) {
      return;
    }
    if (options.onCancel) {
      options.onCancel();
    }
    hideConfirmation();
  };

  return (
    <Modal open={isOpen} onClose={handleCancel} title={title} closeDisabled={isLoading}>
      {typeof message === 'string' ? (
        <p style={{ margin: '1rem 0' }}>{message}</p>
      ) : (
        <div style={{ margin: '1rem 0' }}>{message}</div>
      )}
      <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '1rem', marginTop: '2rem' }}>
        <Button variant="ghost" onClick={handleCancel} disabled={isLoading}>
          {cancelText || t('common.cancel')}
        </Button>
        <Button variant={variant} onClick={handleConfirm} loading={isLoading}>
          {confirmText || t('common.confirm')}
        </Button>
      </div>
    </Modal>
  );
}
