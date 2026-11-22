import {
  APP_AUTHOR_URL,
  APP_REPOSITORY_URL,
  resolveAppFooterVersion,
} from './appFooterMeta';
import styles from './AppFooter.module.scss';

export interface AppFooterProps {
  version?: string;
  compact?: boolean;
  className?: string;
}

export function AppFooter({ version, compact = false, className = '' }: AppFooterProps) {
  const rawVersion =
    version ?? (typeof __APP_VERSION__ !== 'undefined' ? __APP_VERSION__ : undefined);
  const versionMeta = resolveAppFooterVersion(rawVersion);

  return (
    <footer
      className={[styles.footer, compact ? styles.compact : '', className].filter(Boolean).join(' ')}
      data-testid="app-footer"
    >
      <p className={styles.text}>
        <a
          className={styles.link}
          href={APP_REPOSITORY_URL}
          target="_blank"
          rel="noopener noreferrer"
        >
          CPAMP
        </a>
        {' '}
        <a
          className={styles.link}
          href={versionMeta.href}
          target="_blank"
          rel="noopener noreferrer"
          title={versionMeta.title}
        >
          {versionMeta.label}
        </a>
        {' '}
        <span>Designed by</span>
        {' '}
        <a
          className={styles.link}
          href={APP_AUTHOR_URL}
          target="_blank"
          rel="noopener noreferrer"
        >
          Seakee
        </a>
      </p>
    </footer>
  );
}
