import { IconCheck, IconChevronRight, IconKey } from '@/components/ui/icons';
import styles from '../OAuthPage.module.scss';

export type ProviderTileStatus = 'idle' | 'waiting' | 'success' | 'error';

export interface ProviderTileProps {
  domId?: string;
  icon?: string;
  title: string;
  subtitle: string;
  status?: ProviderTileStatus;
  statusText?: string;
  highlighted?: boolean;
  disabled?: boolean;
  onClick: () => void;
}

export function ProviderTile({
  domId,
  icon,
  title,
  subtitle,
  status = 'idle',
  statusText,
  highlighted = false,
  disabled = false,
  onClick,
}: ProviderTileProps) {
  const tileClassName = [
    styles.tile,
    status === 'waiting' ? styles.tileWaiting : '',
    status === 'success' ? styles.tileSuccess : '',
    status === 'error' ? styles.tileError : '',
    highlighted ? styles.tileHighlighted : '',
  ]
    .filter(Boolean)
    .join(' ');

  return (
    <div id={domId} className={styles.tileAnchor}>
      <button
        type="button"
        className={tileClassName}
        disabled={disabled}
        onClick={onClick}
        aria-label={title}
      >
        <span className={styles.tileIconWrap} aria-hidden="true">
          {icon ? (
            <img src={icon} alt="" className={styles.tileIcon} />
          ) : (
            <IconKey size={18} className={styles.tileFallbackIcon} />
          )}
        </span>

        <span className={styles.tileBody}>
          <span className={styles.tileTitle}>{title}</span>
          <span className={styles.tileMeta}>
            {status === 'waiting' && (
              <>
                <span className={styles.statusDotWaiting} aria-hidden="true" />
                <span className={styles.statusTextWaiting}>{statusText || subtitle}</span>
              </>
            )}
            {status === 'success' && (
              <>
                <IconCheck size={12} className={styles.statusIconSuccess} aria-hidden="true" />
                <span className={styles.statusTextSuccess}>{statusText || subtitle}</span>
              </>
            )}
            {status === 'error' && (
              <>
                <span className={styles.statusDotError} aria-hidden="true" />
                <span className={styles.statusTextError}>{statusText || subtitle}</span>
              </>
            )}
            {status === 'idle' && <span className={styles.tileSubtitle}>{subtitle}</span>}
          </span>
        </span>

        <span className={styles.tileTrailing} aria-hidden="true">
          <IconChevronRight size={15} />
        </span>
      </button>
    </div>
  );
}
