import { useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { IconArrowUpFromLine, IconCheck, IconFileText, IconInfo, IconX } from '@/components/ui/icons';
import iconVertex from '@/assets/icons/vertex.svg';
import type { VertexImportState } from '../hooks/useVertexImport';
import styles from '../OAuthPage.module.scss';

export interface VertexImportDialogProps {
  open: boolean;
  vertex: VertexImportState;
  connected: boolean;
  onPickFile: (file: File | undefined) => void;
  onClearFile: () => void;
  onLocationChange: (value: string) => void;
  onImport: () => void;
  onResetForAnother: () => void;
  onNavigateAccounts: () => void;
  onClose: () => void;
}

export function VertexImportDialog({
  open,
  vertex,
  connected,
  onPickFile,
  onClearFile,
  onLocationChange,
  onImport,
  onResetForAnother,
  onNavigateAccounts,
  onClose,
}: VertexImportDialogProps) {
  const { t } = useTranslation();
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const [dragging, setDragging] = useState(false);

  const modalTitle = (
    <div className={styles.dialogHeader}>
      <span className={styles.dialogIconWrap} aria-hidden="true">
        <img src={iconVertex} alt="" className={styles.dialogIcon} />
      </span>
      <div className={styles.dialogHeaderText}>
        <span className={styles.dialogTitle}>{t('vertex_import.title')}</span>
        <span className={styles.dialogSubtitle}>{t('auth_login.vertex_tile_subtitle')}</span>
      </div>
    </div>
  );

  const hasResult = Boolean(vertex.result);

  const modalFooter = !hasResult ? (
    <div className={styles.dialogFooterEnd}>
      <Button variant="secondary" size="sm" onClick={onClose} disabled={vertex.loading}>
        {t('common.cancel')}
      </Button>
      <Button
        variant="primary"
        size="sm"
        onClick={onImport}
        loading={vertex.loading}
        disabled={!connected || !vertex.file || vertex.loading}
      >
        {t('vertex_import.import_button')}
      </Button>
    </div>
  ) : undefined;

  return (
    <Modal open={open} onClose={onClose} title={modalTitle} footer={modalFooter} width={520}>
      <div className={styles.dialogBody}>
        {hasResult && vertex.result ? (
          <div className={styles.vertexResultWrap}>
            <div className={styles.inlineAlertSuccess}>
              <IconCheck size={14} />
              <span>{t('vertex_import.result_title')}</span>
            </div>

            <dl className={styles.factList}>
              {vertex.result.projectId && (
                <div className={styles.factItem}>
                  <dt className={styles.factLabel}>{t('vertex_import.result_project')}</dt>
                  <dd className={styles.factValueMono}>{vertex.result.projectId}</dd>
                </div>
              )}
              {vertex.result.email && (
                <div className={styles.factItem}>
                  <dt className={styles.factLabel}>{t('vertex_import.result_email')}</dt>
                  <dd className={styles.factValue}>{vertex.result.email}</dd>
                </div>
              )}
              {vertex.result.location && (
                <div className={styles.factItem}>
                  <dt className={styles.factLabel}>{t('vertex_import.result_location')}</dt>
                  <dd className={styles.factValueMono}>{vertex.result.location}</dd>
                </div>
              )}
              {vertex.result.authFile && (
                <div className={styles.factItem}>
                  <dt className={styles.factLabel}>{t('vertex_import.result_file')}</dt>
                  <dd className={styles.factValueMono}>{vertex.result.authFile}</dd>
                </div>
              )}
            </dl>

            <div className={styles.dialogStateActions}>
              <Button variant="primary" size="sm" onClick={onNavigateAccounts}>
                {t('auth_login.view_credentials')}
              </Button>
              <Button variant="secondary" size="sm" onClick={onResetForAnother}>
                {t('vertex_import.import_another')}
              </Button>
            </div>
          </div>
        ) : (
          <div className={styles.flowSteps}>
            <p className={styles.flowHint}>{t('vertex_import.description')}</p>

            <input
              ref={fileInputRef}
              type="file"
              accept=".json,application/json"
              className={styles.hiddenFileInput}
              onChange={(event) => {
                const file = event.target.files?.[0];
                onPickFile(file);
                event.target.value = '';
              }}
            />

            <div className={styles.vertexField}>
              <label className={styles.fieldLabel}>{t('vertex_import.file_label')}</label>
              {vertex.fileName ? (
                <div className={styles.selectedFileCard}>
                  <IconFileText size={18} className={styles.selectedFileIcon} />
                  <div className={styles.selectedFileMeta}>
                    <span className={styles.selectedFileName}>{vertex.fileName}</span>
                    {vertex.file && (
                      <span className={styles.selectedFileSize}>
                        {(vertex.file.size / 1024).toFixed(1)} KB
                      </span>
                    )}
                  </div>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => fileInputRef.current?.click()}
                    disabled={vertex.loading}
                  >
                    {t('vertex_import.replace_file')}
                  </Button>
                  <button
                    type="button"
                    className={styles.iconDismissButton}
                    onClick={onClearFile}
                    disabled={vertex.loading}
                    aria-label={t('vertex_import.clear_file')}
                  >
                    <IconX size={14} />
                  </button>
                </div>
              ) : (
                <button
                  type="button"
                  className={`${styles.dropzone} ${dragging ? styles.dropzoneDragging : ''}`}
                  onClick={() => fileInputRef.current?.click()}
                  onDragOver={(event) => {
                    event.preventDefault();
                    setDragging(true);
                  }}
                  onDragLeave={() => setDragging(false)}
                  onDrop={(event) => {
                    event.preventDefault();
                    setDragging(false);
                    const file = event.dataTransfer.files?.[0];
                    onPickFile(file);
                  }}
                  disabled={vertex.loading}
                >
                  <IconArrowUpFromLine size={20} className={styles.dropzoneIcon} />
                  <span className={styles.dropzoneTitle}>{t('vertex_import.dropzone_title')}</span>
                  <span className={styles.dropzoneHint}>{t('vertex_import.file_hint')}</span>
                </button>
              )}
            </div>

            <Input
              label={t('vertex_import.location_label')}
              placeholder={t('vertex_import.location_placeholder')}
              value={vertex.location}
              onChange={(event) => onLocationChange(event.target.value)}
              hint={t('vertex_import.location_hint')}
              disabled={vertex.loading}
            />

            {vertex.error && (
              <div role="alert" className={styles.inlineAlertError}>
                <IconInfo size={14} />
                <span>{vertex.error}</span>
              </div>
            )}
          </div>
        )}
      </div>
    </Modal>
  );
}
