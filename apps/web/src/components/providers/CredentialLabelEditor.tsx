import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Modal } from '@/components/ui/Modal';
import { Input } from '@/components/ui/Input';
import { Button } from '@/components/ui/Button';

interface Props {
  initialLabel: string;
  maskedKey: string;
  saving: boolean;
  onClose: () => void;
  onSave: (label: string) => Promise<boolean>;
}

export function CredentialLabelEditor({ initialLabel, maskedKey, saving, onClose, onSave }: Props) {
  const { t } = useTranslation();
  const [label, setLabel] = useState(initialLabel);
  const [error, setError] = useState('');
  const invalid =
    [...label.trim()].length > 120 ||
    [...label].some(
      (char) => char.charCodeAt(0) < 32 || (char.charCodeAt(0) >= 127 && char.charCodeAt(0) <= 159)
    );
  const save = async () => {
    if (saving || invalid) return;
    setError('');
    try {
      if (await onSave(label.trim())) onClose();
    } catch {
      setError(t('ai_providers.credential_label_save_error'));
    }
  };
  return (
    <Modal
      open
      title={t('ai_providers.credential_label_edit')}
      onClose={onClose}
      closeDisabled={saving}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={saving}>
            {t('common.cancel')}
          </Button>
          <Button onClick={() => void save()} loading={saving} disabled={invalid}>
            {t('common.save')}
          </Button>
        </>
      }
    >
      <div className="hint">{maskedKey}</div>
      <Input
        label={t('ai_providers.credential_label')}
        value={label}
        autoFocus
        disabled={saving}
        hint={t('ai_providers.credential_label_hint')}
        error={invalid ? t('ai_providers.credential_label_invalid') : error}
        onChange={(event) => setLabel(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === 'Enter') {
            event.preventDefault();
            void save();
          }
        }}
      />
    </Modal>
  );
}
