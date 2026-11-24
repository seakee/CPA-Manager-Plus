import { useTranslation } from 'react-i18next';
import type { SiwcBindingStatus } from '@/entities/accounts/siwcBinding';
import styles from './SiwcStatusBadge.module.scss';

export interface SiwcStatusBadgeProps {
  status: SiwcBindingStatus;
  /** Prefix label with "SIWC · " when shown outside a SIWC-specific context (e.g. Accounts list). */
  showPrefix?: boolean;
  /** Optional title tooltip (for example formatted expiry timestamp). */
  title?: string;
  className?: string;
}

export function SiwcStatusBadge({
  status,
  showPrefix = false,
  title,
  className,
}: SiwcStatusBadgeProps) {
  const { t } = useTranslation();
  const statusLabel = t(`siwc.${status}`);
  const label = showPrefix ? `SIWC · ${statusLabel}` : statusLabel;

  return (
    <span
      className={[styles.badge, styles[status], className].filter(Boolean).join(' ')}
      title={title}
    >
      <span className={styles.dot} aria-hidden="true" />
      <span>{label}</span>
    </span>
  );
}
