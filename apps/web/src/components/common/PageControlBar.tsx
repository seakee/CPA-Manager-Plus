import type { ReactNode } from 'react';
import styles from './PageControlBar.module.scss';

export type PageControlBarProps = {
  leading?: ReactNode;
  actions?: ReactNode;
  toolbar?: ReactNode;
  className?: string;
  topRowClassName?: string;
  leadingClassName?: string;
  actionsClassName?: string;
  toolbarClassName?: string;
  as?: 'section' | 'header' | 'div';
  ariaLabel?: string;
  ariaLabelledby?: string;
  testId?: string;
  toolbarTestId?: string;
};

export function PageControlBar({
  leading,
  actions,
  toolbar,
  className = '',
  topRowClassName = '',
  leadingClassName = '',
  actionsClassName = '',
  toolbarClassName = '',
  as: Component = 'section',
  ariaLabel,
  ariaLabelledby,
  testId,
  toolbarTestId,
}: PageControlBarProps) {
  const hasTopRow =
    (leading !== null && leading !== undefined && leading !== false) ||
    (actions !== null && actions !== undefined && actions !== false);
  const hasToolbar = toolbar !== null && toolbar !== undefined && toolbar !== false;

  if (!hasTopRow && !hasToolbar) {
    return null;
  }

  const rootClasses = [styles.root, className].filter(Boolean).join(' ');
  const topRowClasses = [styles.topRow, topRowClassName].filter(Boolean).join(' ');
  const leadingClasses = [styles.leading, leadingClassName].filter(Boolean).join(' ');
  const actionsClasses = [styles.actions, actionsClassName].filter(Boolean).join(' ');
  const toolbarClasses = [
    styles.toolbar,
    !hasTopRow ? styles.toolbarOnly : '',
    toolbarClassName,
  ]
    .filter(Boolean)
    .join(' ');

  return (
    <Component
      className={rootClasses}
      aria-label={ariaLabel}
      aria-labelledby={ariaLabelledby}
      data-testid={testId}
    >
      {hasTopRow ? (
        <div className={topRowClasses}>
          {leading !== null && leading !== undefined && leading !== false ? (
            <div className={leadingClasses}>{leading}</div>
          ) : null}
          {actions !== null && actions !== undefined && actions !== false ? (
            <div className={actionsClasses}>{actions}</div>
          ) : null}
        </div>
      ) : null}
      {hasToolbar ? (
        <div className={toolbarClasses} data-testid={toolbarTestId}>
          {toolbar}
        </div>
      ) : null}
    </Component>
  );
}
