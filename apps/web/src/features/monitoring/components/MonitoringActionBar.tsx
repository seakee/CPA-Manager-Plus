import { useEffect, useRef, useState, type ChangeEvent, type ReactNode, type RefObject } from 'react';
import { Link } from 'react-router-dom';
import type { TFunction } from 'i18next';
import { Button } from '@/components/ui/Button';
import {
  IconChevronDown,
  IconDownload,
  IconExternalLink,
  IconFileText,
  IconInbox,
  IconSettings,
  IconSlidersHorizontal,
} from '@/components/ui/icons';
import styles from '../MonitoringCenterPage.module.scss';

type MonitoringActionBarProps = {
  usageTransferAvailable: boolean;
  usageExporting: boolean;
  usageImporting: boolean;
  loggingToFile: boolean;
  modelPricesAvailable: boolean;
  usageImportInputRef: RefObject<HTMLInputElement | null>;
  t: TFunction;
  onUsageExport: () => void | Promise<void>;
  onUsageImportClick: () => void;
  onUsageImportChange: (event: ChangeEvent<HTMLInputElement>) => void;
  statusSummary?: ReactNode;
};

const shortLabel = (t: TFunction, shortKey: string, fallbackKey: string) => {
  const fallback = t(fallbackKey);
  const label = t(shortKey, { defaultValue: fallback });
  return label === shortKey ? fallback : label;
};

export function MonitoringActionBar({
  usageTransferAvailable,
  usageExporting,
  usageImporting,
  loggingToFile,
  modelPricesAvailable,
  usageImportInputRef,
  t,
  onUsageExport,
  onUsageImportClick,
  onUsageImportChange,
  statusSummary,
}: MonitoringActionBarProps) {
  const modelPriceSettingsLabel = shortLabel(
    t,
    'usage_stats.model_price_settings_short',
    'usage_stats.model_price_settings'
  );
  const accountActionsLabel = shortLabel(t, 'nav.account_actions_short', 'nav.account_actions');
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);
  const mobileMenuRef = useRef<HTMLDivElement | null>(null);
  const toolsLabel = t('common.tools', { defaultValue: 'Tools' });

  useEffect(() => {
    if (!mobileMenuOpen || typeof document === 'undefined') return;
    const handlePointerDown = (event: PointerEvent) => {
      const target = event.target as Node | null;
      if (mobileMenuRef.current && target && !mobileMenuRef.current.contains(target)) {
        setMobileMenuOpen(false);
      }
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setMobileMenuOpen(false);
      }
    };
    document.addEventListener('pointerdown', handlePointerDown);
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('pointerdown', handlePointerDown);
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [mobileMenuOpen]);

  const renderActionItems = (mobile = false) => (
    <>
      {loggingToFile ? (
        <Link
          to="/logs"
          className={mobile ? styles.mobileToolsItem : `btn btn-secondary btn-sm ${styles.quickNavLink}`}
          onClick={mobile ? () => setMobileMenuOpen(false) : undefined}
        >
          <IconFileText size={15} />
          <span>{t('monitoring.open_logs')}</span>
          <IconExternalLink size={13} />
        </Link>
      ) : null}
      {modelPricesAvailable ? (
        <Link
          to="/model-prices"
          className={mobile ? styles.mobileToolsItem : 'btn btn-secondary btn-sm'}
          title={t('usage_stats.model_price_settings')}
          onClick={mobile ? () => setMobileMenuOpen(false) : undefined}
        >
          <IconSettings size={15} />
          <span>{modelPriceSettingsLabel}</span>
        </Link>
      ) : null}
      <Link
        to="/monitoring/account-actions"
        className={mobile ? styles.mobileToolsItem : 'btn btn-secondary btn-sm'}
        title={t('nav.account_actions')}
        onClick={mobile ? () => setMobileMenuOpen(false) : undefined}
      >
        <IconInbox size={15} />
        <span>{accountActionsLabel}</span>
      </Link>
      <Button
        variant={mobile ? 'ghost' : 'secondary'}
        size="sm"
        className={mobile ? styles.mobileToolsItem : undefined}
        onClick={() => {
          if (mobile) setMobileMenuOpen(false);
          onUsageImportClick();
        }}
        disabled={!usageTransferAvailable || usageExporting || usageImporting}
        title={
          usageTransferAvailable
            ? t('usage_stats.import')
            : t('usage_stats.import_export_requires_usage_service')
        }
      >
        <IconFileText size={15} />
        <span>{usageImporting ? t('common.loading') : t('usage_stats.import')}</span>
      </Button>
      <Button
        variant={mobile ? 'ghost' : 'secondary'}
        size="sm"
        className={mobile ? styles.mobileToolsItem : undefined}
        onClick={() => {
          if (mobile) setMobileMenuOpen(false);
          void onUsageExport();
        }}
        disabled={!usageTransferAvailable || usageExporting || usageImporting}
        title={
          usageTransferAvailable
            ? t('usage_stats.export')
            : t('usage_stats.import_export_requires_usage_service')
        }
      >
        <IconDownload size={15} />
        <span>{usageExporting ? t('common.loading') : t('usage_stats.export')}</span>
      </Button>
    </>
  );

  return (
    <div className={styles.actionBarRoot}>
      <div className={styles.actionGroup}>
        {statusSummary}
        {renderActionItems(false)}
      </div>
      <div ref={mobileMenuRef} className={styles.mobileToolsMenuWrap}>
        <Button
          type="button"
          variant="secondary"
          size="sm"
          className={styles.mobileToolsTrigger}
          onClick={() => setMobileMenuOpen((prev) => !prev)}
          aria-expanded={mobileMenuOpen}
          aria-haspopup="menu"
        >
          <IconSlidersHorizontal size={14} />
          <span>{toolsLabel}</span>
          <IconChevronDown size={14} />
        </Button>
        {mobileMenuOpen ? (
          <div className={styles.mobileToolsDropdown} role="menu" aria-label={toolsLabel}>
            {renderActionItems(true)}
          </div>
        ) : null}
      </div>
      <input
        ref={usageImportInputRef}
        type="file"
        accept=".json,.jsonl,.ndjson,.txt,application/json,application/x-ndjson,text/plain"
        style={{ display: 'none' }}
        onChange={onUsageImportChange}
      />
    </div>
  );
}
