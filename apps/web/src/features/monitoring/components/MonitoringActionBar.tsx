import type { ChangeEvent, ReactNode, RefObject } from 'react';
import { Link } from 'react-router-dom';
import type { TFunction } from 'i18next';
import { Button } from '@/components/ui/Button';
import {
  IconDownload,
  IconExternalLink,
  IconFileText,
  IconInbox,
  IconSettings,
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

  return (
    <div className={styles.actionGroup}>
      {statusSummary}
      {loggingToFile ? (
        <Link to="/logs" className={`btn btn-secondary btn-sm ${styles.quickNavLink}`}>
          <IconFileText size={15} />
          <span>{t('monitoring.open_logs')}</span>
          <IconExternalLink size={13} />
        </Link>
      ) : null}
      {modelPricesAvailable ? (
        <Link
          to="/model-prices"
          className="btn btn-secondary btn-sm"
          title={t('usage_stats.model_price_settings')}
        >
          <IconSettings size={15} />
          <span>{modelPriceSettingsLabel}</span>
        </Link>
      ) : null}
      <Link
        to="/monitoring/account-actions"
        className="btn btn-secondary btn-sm"
        title={t('nav.account_actions')}
      >
        <IconInbox size={15} />
        <span>{accountActionsLabel}</span>
      </Link>
      <Button
        variant="secondary"
        size="sm"
        onClick={onUsageImportClick}
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
        variant="secondary"
        size="sm"
        onClick={() => void onUsageExport()}
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
