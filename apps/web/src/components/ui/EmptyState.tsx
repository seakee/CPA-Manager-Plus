import type { ReactNode } from 'react';
import { IconInbox } from './icons';

interface EmptyStateProps {
  title: string;
  description?: string;
  action?: ReactNode;
  footer?: ReactNode;
  variant?: 'inline' | 'canvas';
  className?: string;
}

export function EmptyState({
  title,
  description,
  action,
  footer,
  variant = 'inline',
  className = '',
}: EmptyStateProps) {
  const rootClassName = [
    'empty-state',
    variant === 'canvas' ? 'empty-state-canvas' : '',
    className,
  ]
    .filter(Boolean)
    .join(' ');

  return (
    <div className={rootClassName} data-variant={variant}>
      <div className="empty-content">
        <div className="empty-icon" aria-hidden="true">
          <IconInbox size={variant === 'canvas' ? 22 : 20} />
        </div>
        <div className="empty-copy">
          <div className="empty-title">{title}</div>
          {description && <div className="empty-desc">{description}</div>}
        </div>
      </div>
      {action && <div className="empty-action">{action}</div>}
      {footer && <div className="empty-footer">{footer}</div>}
    </div>
  );
}
