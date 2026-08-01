import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Drawer } from '@/components/ui/Drawer';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { IconCheck, IconX } from '@/components/ui/icons';
import { maskApiKey } from '@/utils/format';
import { ProviderStatusBar } from '../ProviderStatusBar';
import {
  getOpenAIEntryKey,
  getProviderTotalStats,
  stripDisableAllModelsRule,
  type ProviderRecentUsageMap,
} from '../utils';
import { getProviderKindIcon, PROVIDER_KIND_LABELS } from '../ProviderTable/kindMeta';
import type { ProviderRow } from '../ProviderTable/rowData';
import styles from './ProviderDetailDrawer.module.scss';

interface ProviderDetailDrawerProps {
  row: ProviderRow | null;
  open: boolean;
  usageByProvider: ProviderRecentUsageMap;
  resolvedTheme: string;
  actionsDisabled: boolean;
  toggleDisabled: boolean;
  onClose: () => void;
  onEdit: (row: ProviderRow) => void;
  onDelete: (row: ProviderRow) => void;
  onToggle: (row: ProviderRow, enabled: boolean) => void;
  onToggleWebsockets: (row: ProviderRow, enabled: boolean) => void;
  onToggleCloak: (row: ProviderRow, enabled: boolean) => void;
  onToggleDisableCooling: (row: ProviderRow, enabled: boolean) => void;
}

interface FieldRowProps {
  label: string;
  value: string | number | undefined | null;
}

function FieldRow({ label, value }: FieldRowProps) {
  if (value === undefined || value === null || value === '') return null;
  return (
    <div className={styles.fieldRow}>
      <span className={styles.fieldLabel}>{label}</span>
      <span className={styles.fieldValue}>{value}</span>
    </div>
  );
}

export function ProviderDetailDrawer({
  row,
  open,
  usageByProvider,
  resolvedTheme,
  actionsDisabled,
  toggleDisabled,
  onClose,
  onEdit,
  onDelete,
  onToggle,
  onToggleWebsockets,
  onToggleCloak,
  onToggleDisableCooling,
}: ProviderDetailDrawerProps) {
  const { t } = useTranslation();

  const renderQuickSwitches = () => {
    if (!row) return null;
    const supportsProviderKeySwitches =
      row.kind === 'codex' || row.kind === 'xai' || row.kind === 'claude';
    const showWebsockets =
      supportsProviderKeySwitches &&
      (row.kind === 'codex' || row.kind === 'xai' || row.raw.websockets !== undefined);
    const showCloak =
      supportsProviderKeySwitches && (row.kind === 'claude' || row.raw.cloak !== undefined);
    const showDisableCooling = row.kind !== 'vertex';
    if (!showWebsockets && !showCloak && !showDisableCooling) return null;

    return (
      <section className={styles.section}>
        <h4 className={styles.sectionTitle}>{t('ai_providers.table_col_actions')}</h4>
        <div className={styles.quickSwitchList}>
          {showWebsockets && (
            <div className={styles.quickSwitchRow}>
              <div className={styles.quickSwitchText}>
                <span className={styles.quickSwitchLabel}>
                  {t('ai_providers.codex_websockets_label')}
                </span>
                <span className={styles.quickSwitchHint}>
                  {t('ai_providers.codex_websockets_hint')}
                </span>
              </div>
              <ToggleSwitch
                checked={Boolean(row.raw.websockets)}
                disabled={toggleDisabled}
                onChange={(value) => onToggleWebsockets(row, value)}
                ariaLabel={t('ai_providers.codex_websockets_label')}
              />
            </div>
          )}
          {showCloak && (
            <div className={styles.quickSwitchRow}>
              <div className={styles.quickSwitchText}>
                <span className={styles.quickSwitchLabel}>
                  {t('ai_providers.claude_cloak_title')}
                </span>
                <span className={styles.quickSwitchHint}>
                  {t('ai_providers.claude_cloak_hint')}
                </span>
              </div>
              <ToggleSwitch
                checked={Boolean(row.raw.cloak)}
                disabled={toggleDisabled}
                onChange={(value) => onToggleCloak(row, value)}
                ariaLabel={t('ai_providers.claude_cloak_toggle_aria')}
              />
            </div>
          )}
          {showDisableCooling && (
            <div className={styles.quickSwitchRow}>
              <div className={styles.quickSwitchText}>
                <span className={styles.quickSwitchLabel}>
                  {t('ai_providers.disable_cooling_label')}
                </span>
                <span className={styles.quickSwitchHint}>
                  {t('ai_providers.disable_cooling_hint')}
                </span>
              </div>
              <ToggleSwitch
                checked={Boolean(row.raw.disableCooling)}
                disabled={toggleDisabled}
                onChange={(value) => onToggleDisableCooling(row, value)}
                ariaLabel={t('ai_providers.disable_cooling_label')}
              />
            </div>
          )}
        </div>
      </section>
    );
  };

  const renderCloak = () => {
    if (!row || row.kind === 'gemini' || row.kind === 'interactions' || row.kind === 'openai')
      return null;
    const cloak = row.raw.cloak;
    if (!cloak) return null;

    const modeKey = (cloak.mode ?? '').trim().toLowerCase();
    const modeLabel = ['auto', 'always', 'never'].includes(modeKey)
      ? t(`ai_providers.claude_cloak_mode_${modeKey}`)
      : cloak.mode;

    return (
      <section className={styles.section}>
        <h4 className={styles.sectionTitle}>{t('ai_providers.claude_cloak_title')}</h4>
        <FieldRow label={t('ai_providers.claude_cloak_mode_label')} value={modeLabel} />
        <FieldRow
          label={t('ai_providers.claude_cloak_strict_label')}
          value={cloak.strictMode ? t('common.yes') : t('common.no')}
        />
        <FieldRow
          label={t('ai_providers.claude_cloak_sensitive_words_count')}
          value={cloak.sensitiveWords?.length || 0}
        />
      </section>
    );
  };

  const renderOpenAIKeys = () => {
    if (!row || row.kind !== 'openai') return null;
    const provider = row.raw;
    const entries = provider.apiKeyEntries ?? [];
    if (!entries.length) return null;

    return (
      <section className={styles.section}>
        <h4 className={styles.sectionTitle}>
          {t('ai_providers.openai_keys_count')}: {entries.length}
        </h4>
        <div className={styles.keyEntryList}>
          {entries.map((entry, entryIndex) => {
            const entryStats = getProviderTotalStats(
              usageByProvider,
              provider.name,
              entry.apiKey,
              provider.baseUrl
            );
            return (
              <div key={getOpenAIEntryKey(entry, entryIndex)} className={styles.keyEntryCard}>
                <span className={styles.keyEntryIndex}>{entryIndex + 1}</span>
                <span className={styles.keyEntryKey}>{maskApiKey(entry.apiKey)}</span>
                {entry.proxyUrl && <span className={styles.keyEntryProxy}>{entry.proxyUrl}</span>}
                <span className={styles.keyEntryStats}>
                  <span className={styles.statSuccess}>
                    <IconCheck size={12} /> {entryStats.success}
                  </span>
                  <span className={styles.statFailure}>
                    <IconX size={12} /> {entryStats.failure}
                  </span>
                </span>
              </div>
            );
          })}
        </div>
      </section>
    );
  };

  const renderBody = () => {
    if (!row) return null;

    const headerEntries = Object.entries(row.raw.headers ?? {});
    const models = row.raw.models ?? [];
    const excludedModels =
      row.kind === 'openai' ? [] : stripDisableAllModelsRule(row.raw.excludedModels);

    return (
      <>
        <section className={styles.section}>
          {!row.enabled && (
            <div className={styles.disabledBadge}>{t('ai_providers.config_disabled_badge')}</div>
          )}
          {row.kind !== 'openai' && (
            <FieldRow label={t('common.api_key')} value={maskApiKey(row.raw.apiKey)} />
          )}
          <FieldRow label={t('common.base_url')} value={row.baseUrl} />
          <FieldRow label={t('common.priority')} value={row.priority} />
          <FieldRow label={t('common.prefix')} value={row.raw.prefix} />
          {row.kind !== 'openai' && (
            <FieldRow label={t('common.proxy_url')} value={row.raw.proxyUrl} />
          )}
          {row.kind === 'openai' && (
            <FieldRow label={t('ai_providers.openai_test_model')} value={row.raw.testModel} />
          )}
        </section>

        {renderQuickSwitches()}
        {renderCloak()}
        {renderOpenAIKeys()}

        {headerEntries.length > 0 && (
          <section className={styles.section}>
            <h4 className={styles.sectionTitle}>{t('ai_providers.detail_section_headers')}</h4>
            <div className={styles.badgeList}>
              {headerEntries.map(([key, value]) => (
                <span key={key} className={styles.headerBadge}>
                  <strong>{key}:</strong> {String(value)}
                </span>
              ))}
            </div>
          </section>
        )}

        {models.length > 0 && (
          <section className={styles.section}>
            <h4 className={styles.sectionTitle}>
              {t('ai_providers.table_col_models')}: {models.length}
            </h4>
            <div className={styles.badgeList}>
              {models.map((model) => (
                <span key={model.name} className={styles.modelTag}>
                  <span className={styles.modelName}>{model.name}</span>
                  {model.alias && model.alias !== model.name && (
                    <span className={styles.modelAlias}>{model.alias}</span>
                  )}
                </span>
              ))}
            </div>
          </section>
        )}

        {excludedModels.length > 0 && (
          <section className={styles.section}>
            <h4 className={styles.sectionTitle}>
              {t('ai_providers.excluded_models_count', { count: excludedModels.length })}
            </h4>
            <div className={styles.badgeList}>
              {excludedModels.map((model) => (
                <span key={model} className={`${styles.modelTag} ${styles.excludedModelTag}`}>
                  <span className={styles.modelName}>{model}</span>
                </span>
              ))}
            </div>
          </section>
        )}

        <section className={styles.section}>
          <h4 className={styles.sectionTitle}>{t('ai_providers.table_col_recent')}</h4>
          <div className={styles.recentStats}>
            <span className={styles.statSuccess}>
              {t('stats.success')}: {row.stats.success}
            </span>
            <span className={styles.statFailure}>
              {t('stats.failure')}: {row.stats.failure}
            </span>
          </div>
          <ProviderStatusBar statusData={row.statusData} />
        </section>
      </>
    );
  };

  return (
    <Drawer
      open={open && row !== null}
      onClose={onClose}
      width={440}
      title={
        row ? (
          <>
            <img
              src={getProviderKindIcon(row.kind, resolvedTheme)}
              alt=""
              className={styles.titleIcon}
            />
            <span className={styles.titleKind}>{PROVIDER_KIND_LABELS[row.kind]}</span>
            <span className={styles.titleLabel} title={row.label}>
              {row.label}
            </span>
          </>
        ) : null
      }
      footer={
        row ? (
          <>
            <div className={styles.footerToggle}>
              <ToggleSwitch
                label={t('ai_providers.config_toggle_label')}
                checked={row.enabled}
                disabled={toggleDisabled}
                onChange={(value) => onToggle(row, value)}
              />
            </div>
            <Button
              variant="danger"
              size="sm"
              onClick={() => onDelete(row)}
              disabled={actionsDisabled}
            >
              {t('common.delete')}
            </Button>
            <Button
              variant="primary"
              size="sm"
              onClick={() => onEdit(row)}
              disabled={actionsDisabled}
            >
              {t('common.edit')}
            </Button>
          </>
        ) : null
      }
    >
      {renderBody()}
    </Drawer>
  );
}
