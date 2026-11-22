import type { ReactNode } from 'react';
import styles from './SectionActionFooter.module.scss';

export type SectionActionFooterProps = {
  status?: ReactNode;
  children?: ReactNode;
  className?: string;
  testId?: string;
};

export function SectionActionFooter({
  status,
  children,
  className = '',
  testId,
}: SectionActionFooterProps) {
  const rootClasses = [styles.root, className].filter(Boolean).join(' ');

  return (
    <div className={rootClasses} data-testid={testId}>
      {status !== null && status !== undefined && status !== false ? (
        <div className={styles.status}>{status}</div>
      ) : (
        <div />
      )}
      {children !== null && children !== undefined && children !== false ? (
        <div className={styles.actions}>{children}</div>
      ) : null}
    </div>
  );
}
