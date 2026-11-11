import { useTranslation } from 'react-i18next';
import type { PluginQuotaSummaryItem } from '@/types';
import { formatPluginQuotaSummaryValue } from '@/utils/quota/pluginQuota';
import styles from './PluginQuotaSummary.module.scss';

export interface PluginQuotaSummaryProps {
  items: PluginQuotaSummaryItem[];
  variant?: 'compact' | 'detail';
  locale?: string;
}

/**
 * Generic plugin quota summary facts (declared label/value metrics such as
 * overall usage and an extra balance). Purely presentational label/value
 * markup: no progress bar, no health verdict, and no interactive elements so
 * it stays valid inside the inline list detail triggers.
 */
export function PluginQuotaSummary({
  items,
  variant = 'compact',
  locale,
}: PluginQuotaSummaryProps) {
  const { t } = useTranslation();
  if (items.length === 0) return null;

  return (
    <span
      className={`${styles.pluginQuotaSummary} ${variant === 'detail' ? styles.pluginQuotaSummaryDetail : ''}`}
      data-plugin-quota-summary="true"
      aria-label={t('accounts.detail_plugin_quota_summary')}
    >
      {items.map((item) => {
        const valueText = formatPluginQuotaSummaryValue(item, locale);
        const itemText = `${item.label}: ${valueText ?? '-'}`;
        return (
          <span
            key={item.key}
            className={styles.pluginQuotaSummaryItem}
            data-plugin-quota-summary-item={item.key}
            title={itemText}
          >
            <span className={styles.pluginQuotaSummaryLabel}>{item.label}</span>
            <span
              className={`${styles.pluginQuotaSummaryValue} ${valueText === null ? styles.pluginQuotaSummaryValueUnknown : ''}`}
            >
              {valueText ?? '-'}
            </span>
          </span>
        );
      })}
    </span>
  );
}
