import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { SegmentedTabs, type SegmentedTabItem } from '@/components/ui/SegmentedTabs';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import styles from '../CodexInspectionPage.module.scss';

export type CodexInspectionMode = 'local' | 'server';

type CodexInspectionModeTabsProps = {
  activeMode: CodexInspectionMode;
};

const MODES: ReadonlyArray<{
  mode: CodexInspectionMode;
  path: string;
  labelKey: string;
}> = [
  {
    mode: 'local',
    path: '/codex-inspection',
    labelKey: 'monitoring.codex_inspection_mode_local',
  },
  {
    mode: 'server',
    path: '/codex-inspection/server',
    labelKey: 'monitoring.codex_inspection_mode_server',
  },
];

export function CodexInspectionModeTabs({ activeMode }: CodexInspectionModeTabsProps) {
  const { t } = useTranslation();
  const availability = usePanelFeatureAvailability();
  const visibleModes = MODES.filter(
    (item) =>
      item.mode === 'local' ||
      item.mode === activeMode ||
      availability.checking ||
      availability.serverCodexInspectionAvailable
  );
  const modeTabs: ReadonlyArray<SegmentedTabItem<CodexInspectionMode>> = visibleModes.map(
    (item) => ({
      id: item.mode,
      label: t(item.labelKey),
      to: item.path,
    })
  );

  return (
    <section
      className={styles.modeSwitchPanel}
      aria-label={t('monitoring.codex_inspection_mode_label')}
    >
      <div className={styles.modeSwitchMain}>
        <SegmentedTabs
          items={modeTabs}
          activeTab={activeMode}
          ariaLabel={t('monitoring.codex_inspection_mode_label')}
          equalWidth
          linkComponent={Link}
        />

      </div>
    </section>
  );
}
