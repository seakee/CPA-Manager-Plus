import type { ComponentType } from 'react';
import { SegmentedTabs } from '@/components/ui/SegmentedTabs';
import {
  IconKey,
  IconSidebarMonitor,
  IconSidebarProviders,
  type IconProps,
} from '@/components/ui/icons';
import styles from '../MonitoringCenterPage.module.scss';

export type MonitoringTabBadgeTone = 'default' | 'failure';

export type MonitoringTabsVariant = 'standalone' | 'cards';

export type MonitoringTabIcon = 'accounts' | 'apiKeys' | 'realtime';

export type MonitoringTab<Id extends string = string> = {
  id: Id;
  label: string;
  fullLabel?: string;
  icon?: MonitoringTabIcon;
  badge?: string | number | null;
  badgeTitle?: string;
  badgeTone?: MonitoringTabBadgeTone;
};

type MonitoringTabsBarProps<Id extends string> = {
  tabs: ReadonlyArray<MonitoringTab<Id>>;
  activeTab: Id;
  onChange: (tab: Id) => void;
  ariaLabel: string;
  idBase?: string;
  variant?: MonitoringTabsVariant;
};

const tabIconMap: Record<MonitoringTabIcon, ComponentType<IconProps>> = {
  accounts: IconSidebarProviders,
  apiKeys: IconKey,
  realtime: IconSidebarMonitor,
};

export function MonitoringTabsBar<Id extends string>({
  tabs,
  activeTab,
  onChange,
  ariaLabel,
  idBase = 'monitoring-data-tabs',
  variant = 'standalone',
}: MonitoringTabsBarProps<Id>) {
  const isCards = variant === 'cards';

  return (
    <SegmentedTabs<Id>
      variant={isCards ? 'joined' : 'default'}
      responsiveFullWidth={false}
      idBase={idBase}
      ariaLabel={ariaLabel}
      activeTab={activeTab}
      onChange={onChange}
      items={tabs.map((tab) => {
        const isActive = tab.id === activeTab;
        const Icon = tab.icon ? tabIconMap[tab.icon] : null;
        const badgeClass = [
          styles.tabBadge,
          isActive ? styles.tabBadgeActive : '',
          tab.badgeTone === 'failure' ? styles.tabBadgeFailure : '',
        ]
          .filter(Boolean)
          .join(' ');

        return {
          id: tab.id,
          title: tab.fullLabel ?? tab.label,
          ariaControls: `${idBase}-${tab.id}-panel`,
          label: (
            <span className={styles.tabItemContent}>
              {Icon ? (
                <span className={styles.tabIcon} aria-hidden="true">
                  <Icon size={15} />
                </span>
              ) : null}
              <span className={styles.tabLabel}>{tab.label}</span>
              {tab.badge !== undefined && tab.badge !== null && tab.badge !== '' ? (
                <span className={badgeClass} title={tab.badgeTitle}>
                  {tab.badge}
                </span>
              ) : null}
            </span>
          ),
        };
      })}
    />
  );
}
