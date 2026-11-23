import { useEffect, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { Button } from '@/components/ui/Button';
import { IconX } from '@/components/ui/icons';
import styles from './MobileFilterSheet.module.scss';

export interface MobileFilterSheetProps {
  open: boolean;
  title: string;
  summary?: string;
  resetLabel: string;
  confirmLabel: string;
  closeLabel: string;
  titleId: string;
  onClose: () => void;
  onReset: () => void;
  resetDisabled?: boolean;
  children: ReactNode;
}

export function MobileFilterSheet({
  open,
  title,
  summary,
  resetLabel,
  confirmLabel,
  closeLabel,
  titleId,
  onClose,
  onReset,
  resetDisabled = false,
  children,
}: MobileFilterSheetProps) {
  useEffect(() => {
    if (!open || typeof document === 'undefined') return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        onClose();
      }
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  }, [onClose, open]);

  if (!open || typeof document === 'undefined') return null;

  return createPortal(
    <div className={styles.layer}>
      <button
        type="button"
        className={styles.backdrop}
        aria-label={closeLabel}
        onClick={onClose}
      />
      <section
        className={styles.panel}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
      >
        <header className={styles.header}>
          <div className={styles.headerText}>
            <h2 id={titleId}>{title}</h2>
            {summary ? <span title={summary}>{summary}</span> : null}
          </div>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            iconOnly
            onClick={onClose}
            aria-label={closeLabel}
          >
            <IconX size={17} />
          </Button>
        </header>
        <div className={styles.body}>{children}</div>
        <footer className={styles.footer}>
          <Button
            type="button"
            variant="secondary"
            size="sm"
            onClick={onReset}
            disabled={resetDisabled}
          >
            {resetLabel}
          </Button>
          <Button type="button" variant="primary" size="sm" onClick={onClose}>
            {confirmLabel}
          </Button>
        </footer>
      </section>
    </div>,
    document.body
  );
}
