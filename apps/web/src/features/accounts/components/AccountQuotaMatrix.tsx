import {
  formatPercent,
  formatQuotaResetDisplay,
  type AccountQuotaLifecycleBarOverride,
  type AntigravityQuotaMatrix,
} from '@/features/accounts/model/accountsPagePresentation';
import { useTranslation } from 'react-i18next';
import styles from '../AccountsPage.module.scss';

interface AccountQuotaMatrixProps {
  accountKey: string;
  matrix: AntigravityQuotaMatrix;
  lifecycleBarOverride?: AccountQuotaLifecycleBarOverride;
}

const getRemainingPercentBarClass = (remainingPercent: number | null) => {
  if (remainingPercent === null) return styles.quotaBarNeutral;
  if (remainingPercent <= 0) return styles.quotaBarBad;
  if (remainingPercent < 20) return styles.quotaBarWarn;
  return styles.quotaBarGood;
};

const getMatrixBarClass = (
  remainingPercent: number | null,
  lifecycleBarOverride: AccountQuotaLifecycleBarOverride
) => {
  if (lifecycleBarOverride === 'bad') return styles.quotaBarBad;
  if (lifecycleBarOverride === 'neutral') return styles.quotaBarNeutral;
  return getRemainingPercentBarClass(remainingPercent);
};

export function AccountQuotaMatrix({
  accountKey,
  matrix,
  lifecycleBarOverride = null,
}: AccountQuotaMatrixProps) {
  const { t, i18n } = useTranslation();
  return (
    <span className={styles.quotaMatrix} data-account-quota-matrix={accountKey}>
      {matrix.rows.map((matrixRow) => (
        <span
          key={matrixRow.key}
          className={styles.quotaMatrixRow}
          data-account-quota-matrix-row={matrixRow.key}
        >
          <span className={styles.quotaMatrixWindowLabel}>{matrixRow.label}</span>
          <span className={styles.quotaMatrixCells}>
            {matrixRow.cells.map((cell) => {
              const windowRemaining = cell.window.remainingPercent;
              const windowWidth = Math.max(0, Math.min(100, windowRemaining ?? 0));
              const resetDisplay = formatQuotaResetDisplay(
                cell.window.resetAtMs,
                cell.window.resetLabel,
                i18n.language
              );
              const title = [
                `${cell.groupLabel} ${cell.window.label}: ${formatPercent(windowRemaining)}`,
                resetDisplay !== '-' ? `${t('accounts.col_reset')}: ${resetDisplay}` : '',
              ]
                .filter(Boolean)
                .join(' · ');
              return (
                <span
                  key={cell.window.key}
                  className={styles.quotaMatrixCell}
                  data-account-quota-matrix-cell={`${matrixRow.key}:${cell.groupLabel}`}
                  title={title}
                >
                  <span className={styles.quotaMatrixGroupLabel} title={cell.groupLabel}>
                    {cell.displayLabel}
                  </span>
                  <span
                    className={`${styles.quotaTrack} ${styles.quotaMatrixTrack}`}
                    aria-hidden="true"
                  >
                    <span
                      className={`${styles.quotaBar} ${getMatrixBarClass(
                        windowRemaining,
                        lifecycleBarOverride
                      )}`}
                      style={{ width: `${windowWidth}%` }}
                    />
                  </span>
                  <strong className={styles.quotaMatrixPercent}>
                    {windowRemaining !== null ? formatPercent(windowRemaining) : '-'}
                  </strong>
                </span>
              );
            })}
          </span>
        </span>
      ))}
    </span>
  );
}
