import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { SegmentedTabs, type SegmentedTabItem } from '@/components/ui/SegmentedTabs';
import { IconFilterAll, IconKey } from '@/components/ui/icons';
import { getAuthFileIcon, type ResolvedTheme } from '@/features/authFiles/constants';
import { getProviderLabel } from '@/features/accounts/model/accountsPagePresentation';
import styles from './AccountProviderTabs.module.scss';

type AccountProviderTabRow = {
  provider: string;
};

interface AccountProviderTabsProps {
  rows: readonly AccountProviderTabRow[];
  value: string;
  onChange: (provider: string) => void;
  resolvedTheme: ResolvedTheme;
}

const ALL_PROVIDERS = 'all';

export function AccountProviderTabs({
  rows,
  value,
  onChange,
  resolvedTheme,
}: AccountProviderTabsProps) {
  const { t } = useTranslation();

  const tabs = useMemo<ReadonlyArray<SegmentedTabItem<string>>>(() => {
    const counts = new Map<string, number>();
    rows.forEach((row) => {
      counts.set(row.provider, (counts.get(row.provider) ?? 0) + 1);
    });

    const providers = Array.from(counts.keys()).sort((left, right) => left.localeCompare(right));
    if (value !== ALL_PROVIDERS && !counts.has(value)) {
      providers.unshift(value);
    }

    const renderTabLabel = (provider: string, count: number) => {
      const isAll = provider === ALL_PROVIDERS;
      const label = isAll ? t('accounts.filter_all') : getProviderLabel(provider, t);
      const icon = isAll ? null : getAuthFileIcon(provider, resolvedTheme);

      return (
        <span className={styles.tabContent}>
          <span className={styles.tabIcon} aria-hidden="true">
            {isAll ? (
              <IconFilterAll size={17} />
            ) : icon ? (
              <img className={styles.providerIcon} src={icon} alt="" />
            ) : (
              <IconKey size={16} />
            )}
          </span>
          <span className={styles.tabLabel}>{label}</span>
          <span className={styles.tabCount}>{count}</span>
        </span>
      );
    };

    return [
      {
        id: ALL_PROVIDERS,
        label: renderTabLabel(ALL_PROVIDERS, rows.length),
        title: t('accounts.filter_all'),
      },
      ...providers.map((provider) => ({
        id: provider,
        label: renderTabLabel(provider, counts.get(provider) ?? 0),
        title: getProviderLabel(provider, t),
      })),
    ];
  }, [resolvedTheme, rows, t, value]);

  return (
    <div className={styles.root}>
      <SegmentedTabs
        items={tabs}
        activeTab={value}
        onChange={onChange}
        ariaLabel={t('accounts.provider_filter')}
        idBase="accounts-provider-filter"
        variant="joined"
        className={styles.tabs}
      />
    </div>
  );
}
