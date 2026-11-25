import type { ReactNode } from 'react';
import { WorkspaceListPanel } from '@/components/common/WorkspaceListPanel';
import {
  MonitoringTabsBar,
  type MonitoringTab,
} from '@/features/monitoring/components/MonitoringTabsBar';
import styles from '@/features/monitoring/MonitoringCenterPage.module.scss';

type MonitoringDataPanelProps<Id extends string> = {
  tabs: ReadonlyArray<MonitoringTab<Id>>;
  activeTab: Id;
  onTabChange: (tab: Id) => void;
  ariaLabel: string;
  actions?: ReactNode;
  idBase?: string;
  renderContent: (tab: Id) => ReactNode;
};

export function MonitoringDataPanel<Id extends string>({
  tabs,
  activeTab,
  onTabChange,
  ariaLabel,
  actions,
  idBase = 'monitoring-data-tabs',
  renderContent,
}: MonitoringDataPanelProps<Id>) {
  return (
    <WorkspaceListPanel
      as="div"
      className={styles.dataPanel}
      headerClassName={styles.dataPanelHeader}
      bodyClassName={styles.dataPanelBody}
      headerLeading={
        <div className={styles.dataPanelTabs}>
          <MonitoringTabsBar
            tabs={tabs}
            activeTab={activeTab}
            onChange={onTabChange}
            ariaLabel={ariaLabel}
            idBase={idBase}
            variant="cards"
          />
        </div>
      }
      headerActions={actions}
    >
      {tabs.map((tab) => {
          const isActive = tab.id === activeTab;
          return (
            <div
              key={tab.id}
              role="tabpanel"
              id={`${idBase}-${tab.id}-panel`}
              aria-labelledby={`${idBase}-${tab.id}`}
              className={styles.dataPanelTabPanel}
              hidden={!isActive}
            >
              {isActive ? renderContent(tab.id) : null}
            </div>
          );
        })}
    </WorkspaceListPanel>
  );
}
