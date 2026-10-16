import type { ReactNode } from 'react';
import { IconCheck, IconRefreshCw } from './icons';
import styles from './SaveStatusPill.module.scss';

export type SaveStatusTone = 'saved' | 'modified' | 'neutral';

interface SaveStatusPillProps {
  /** Short status text, e.g. "Saved" or "Unsaved changes". */
  status: ReactNode;
  /**
   * Every status text this pill can show. The status slot is sized to the widest
   * so the pill keeps a constant width: it can stay centred while the buttons
   * never move, and a shorter status simply leaves room on its left.
   */
  statusOptions?: ReactNode[];
  tone: SaveStatusTone;
  onReset: () => void;
  onSave: () => void;
  resetDisabled?: boolean;
  saveDisabled?: boolean;
  /** Shows the pending-changes dot on the save button. */
  dirty?: boolean;
  resetLabel: string;
  saveLabel: string;
  className?: string;
}

/**
 * Status + discard + save cluster for forms with staged edits. Buttons show icon
 * and text, separated by thin dividers; on narrow screens they collapse to icons. Same visual
 * language as the Config page's floating save actions; this variant is laid
 * out in flow (e.g. a drawer footer) rather than fixed to the viewport.
 */
export function SaveStatusPill({
  status,
  statusOptions = [],
  tone,
  onReset,
  onSave,
  resetDisabled = false,
  saveDisabled = false,
  dirty = false,
  resetLabel,
  saveLabel,
  className,
}: SaveStatusPillProps) {
  return (
    <div className={[styles.pill, className].filter(Boolean).join(' ')}>
      <span className={styles.statusSlot}>
        {statusOptions.map((option, index) => (
          <span key={index} className={`${styles.status} ${styles.statusSizer}`} aria-hidden="true">
            {option}
          </span>
        ))}
        <span className={`${styles.status} ${styles[tone]}`} role="status" aria-live="polite">
          {status}
        </span>
      </span>
      <span className={styles.separator} aria-hidden="true" />
      <button
        type="button"
        className={styles.action}
        onClick={onReset}
        disabled={resetDisabled}
        title={resetLabel}
        aria-label={resetLabel}
      >
        <IconRefreshCw size={16} />
        <span className={styles.actionText}>{resetLabel}</span>
      </button>
      <span className={styles.separator} aria-hidden="true" />
      <button
        type="button"
        className={`${styles.action} ${dirty && !saveDisabled ? styles.actionPrimary : ''}`}
        onClick={onSave}
        disabled={saveDisabled}
        title={saveLabel}
        aria-label={saveLabel}
      >
        <IconCheck size={16} />
        <span className={styles.actionText}>{saveLabel}</span>
        {dirty ? <span className={styles.dirtyDot} aria-hidden="true" /> : null}
      </button>
    </div>
  );
}
