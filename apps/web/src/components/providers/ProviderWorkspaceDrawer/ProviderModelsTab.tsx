import { useMemo, useState, type Dispatch, type SetStateAction } from 'react';
import { useTranslation } from 'react-i18next';
import {
  DrawerSectionCard,
  DrawerSubViewHeader,
  DrawerTabPanel,
} from '@/components/common/WorkspaceDrawer';
import { Button } from '@/components/ui/Button';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import { InfoTooltip } from '@/components/ui/InfoTooltip';
import { Input } from '@/components/ui/Input';
import { ModelInputList } from '@/components/ui/ModelInputList';
import {
  IconModelCluster,
  IconPlus,
  IconRefreshCw,
  IconSearch,
  IconShield,
  IconX,
} from '@/components/ui/icons';
import type { ModelInfo } from '@/utils/models';
import type { ModelEntry } from '../types';
import type { ProviderKind } from '../ProviderTable/rowData';
import type { ProviderDrawerFormState } from './useProviderDrawerEditor';
import styles from './ProviderWorkspaceDrawer.module.scss';

export interface ProviderModelsTabProps {
  mode: 'view-edit' | 'create';
  activeKind: ProviderKind;
  form: ProviderDrawerFormState;
  setForm: Dispatch<SetStateAction<ProviderDrawerFormState>>;
  saving: boolean;
  actionsDisabled: boolean;
  configuredModelCount: number;
  // Discovery
  modelDiscoveryOpen: boolean;
  setModelDiscoveryOpen: (open: boolean) => void;
  modelDiscoveryEndpoint: string;
  discoveredModels: ModelInfo[];
  modelsLoaded: boolean;
  modelDiscoveryFetching: boolean;
  modelDiscoveryError: string;
  modelDiscoverySearch: string;
  setModelDiscoverySearch: (value: string) => void;
  modelDiscoverySelected: Set<string>;
  discoveredModelsFiltered: ModelInfo[];
  configuredModelNames: Set<string>;
  visibleDiscoverableModelNames: string[];
  allVisibleSelected: boolean;
  toggleModelDiscoverySelection: (name: string) => void;
  handleSelectVisibleModels: () => void;
  handleClearModelDiscoverySelection: () => void;
  fetchModelDiscovery: () => Promise<void>;
  applySelectedDiscoveredModels: () => void;
}

const cleanHeadingText = (text: string): string =>
  text.replace(/\s*[（(](?:可选|必填|optional|required)[)）]/gi, '').replace(/[:：]\s*$/, '');

export function ProviderModelsTab({
  activeKind,
  form,
  setForm,
  saving,
  actionsDisabled,
  configuredModelCount,
  modelDiscoveryOpen,
  setModelDiscoveryOpen,
  modelDiscoveryEndpoint,
  discoveredModels,
  modelsLoaded,
  modelDiscoveryFetching,
  modelDiscoveryError,
  modelDiscoverySearch,
  setModelDiscoverySearch,
  modelDiscoverySelected,
  discoveredModelsFiltered,
  configuredModelNames,
  visibleDiscoverableModelNames,
  allVisibleSelected,
  toggleModelDiscoverySelection,
  handleSelectVisibleModels,
  handleClearModelDiscoverySelection,
  fetchModelDiscovery,
}: ProviderModelsTabProps) {
  const { t } = useTranslation();
  const [modelFilter, setModelFilter] = useState('');
  const [pinnedEditOrigIndex, setPinnedEditOrigIndex] = useState<number | null>(null);
  const controlsDisabled = actionsDisabled || saving;
  const isOpenAI = activeKind === 'openai';
  const supportsDiscovery = activeKind !== 'vertex';

  const normalizedFilter = modelFilter.trim().toLowerCase();
  const { visibleIndexes, filteredEntries } = useMemo(() => {
    if (!normalizedFilter) {
      return {
        visibleIndexes: form.modelEntries.map((_, idx) => idx),
        filteredEntries: form.modelEntries,
      };
    }
    const indexes: number[] = [];
    const entries: ModelEntry[] = [];
    form.modelEntries.forEach((entry, idx) => {
      const name = entry.name.toLowerCase();
      const alias = entry.alias.toLowerCase();
      if (
        idx === pinnedEditOrigIndex ||
        name.includes(normalizedFilter) ||
        alias.includes(normalizedFilter)
      ) {
        indexes.push(idx);
        entries.push(entry);
      }
    });
    return { visibleIndexes: indexes, filteredEntries: entries };
  }, [form.modelEntries, normalizedFilter, pinnedEditOrigIndex]);

  const handleModelListChange = (nextVisibleEntries: ModelEntry[]) => {
    if (!normalizedFilter) {
      setForm((prev) => ({ ...prev, modelEntries: nextVisibleEntries }));
      return;
    }

    let editedOrigIndex: number | null = null;
    visibleIndexes.forEach((origIndex, visIdx) => {
      if (nextVisibleEntries[visIdx] !== filteredEntries[visIdx]) {
        editedOrigIndex = origIndex;
      }
    });
    if (editedOrigIndex !== null) {
      setPinnedEditOrigIndex(editedOrigIndex);
    }

    setForm((prev) => {
      const current = [...prev.modelEntries];
      if (nextVisibleEntries.length === filteredEntries.length) {
        visibleIndexes.forEach((origIndex, visIdx) => {
          current[origIndex] = nextVisibleEntries[visIdx];
        });
        return { ...prev, modelEntries: current };
      }

      if (nextVisibleEntries.length < filteredEntries.length) {
        const remaining = [...nextVisibleEntries];
        const removedOrigIndexes: number[] = [];
        filteredEntries.forEach((entry, visIdx) => {
          const matchIdx = remaining.indexOf(entry);
          if (matchIdx >= 0) {
            remaining.splice(matchIdx, 1);
          } else {
            const origIdx = visibleIndexes[visIdx];
            if (origIdx !== undefined) removedOrigIndexes.push(origIdx);
          }
        });
        removedOrigIndexes
          .sort((a, b) => b - a)
          .forEach((origIdx) => {
            current.splice(origIdx, 1);
          });
        return {
          ...prev,
          modelEntries: current.length ? current : [{ name: '', alias: '' }],
        };
      }

      visibleIndexes.forEach((origIndex, visIdx) => {
        if (visIdx < nextVisibleEntries.length) {
          current[origIndex] = nextVisibleEntries[visIdx];
        }
      });
      const extraEntries = nextVisibleEntries.slice(visibleIndexes.length);
      return { ...prev, modelEntries: [...current, ...extraEntries] };
    });
  };

  const handleRemoveVisibleModel = (visIdx: number) => {
    if (!normalizedFilter) {
      setPinnedEditOrigIndex(null);
      setForm((prev) => {
        const next = prev.modelEntries.filter((_, idx) => idx !== visIdx);
        return {
          ...prev,
          modelEntries: next.length ? next : [{ name: '', alias: '' }],
        };
      });
      return;
    }

    const targetOrigIdx = visibleIndexes[visIdx];
    if (targetOrigIdx === undefined) return;
    setPinnedEditOrigIndex((prevPinned) => {
      if (prevPinned === null) return null;
      if (prevPinned === targetOrigIdx) return null;
      return prevPinned > targetOrigIdx ? prevPinned - 1 : prevPinned;
    });
    setForm((prev) => {
      const current = [...prev.modelEntries];
      current.splice(targetOrigIdx, 1);
      return {
        ...prev,
        modelEntries: current.length ? current : [{ name: '', alias: '' }],
      };
    });
  };

  const discoveryModalTitle =
    activeKind === 'claude'
      ? t('ai_providers.claude_models_fetch_title')
      : activeKind === 'codex' || activeKind === 'xai'
        ? t('ai_providers.codex_models_fetch_title')
        : activeKind === 'gemini' || activeKind === 'interactions'
          ? t('ai_providers.gemini_models_fetch_title')
          : t('ai_providers.openai_models_fetch_title');

  const discoverySearchPlaceholder =
    activeKind === 'claude'
      ? t('ai_providers.claude_models_search_placeholder')
      : activeKind === 'codex' || activeKind === 'xai'
        ? t('ai_providers.codex_models_search_placeholder')
        : activeKind === 'gemini' || activeKind === 'interactions'
          ? t('ai_providers.gemini_models_search_placeholder')
          : t('ai_providers.openai_models_search_placeholder');

  const excludedPlaceholder = useMemo(() => {
    if (activeKind === 'claude') {
      return 'claude-3-5-haiku*\n*-legacy';
    }
    if (activeKind === 'codex') {
      return 'gpt-4o-mini*\no1-preview*';
    }
    if (activeKind === 'xai') {
      return 'grok-2*\n*-vision-beta';
    }
    if (activeKind === 'gemini' || activeKind === 'vertex' || activeKind === 'interactions') {
      return 'gemini-1.5-*\n*-exp';
    }
    return t('ai_providers.excluded_models_placeholder');
  }, [activeKind, t]);

  const excludedExampleToken =
    activeKind === 'claude'
      ? 'claude-3-5-haiku*'
      : activeKind === 'xai'
        ? 'grok-2*'
        : activeKind === 'gemini' || activeKind === 'vertex' || activeKind === 'interactions'
          ? 'gemini-1.5-*'
          : 'gpt-4o-mini*';

  const modelNamePlaceholder = useMemo(() => {
    if (activeKind === 'vertex') {
      return t('ai_providers.vertex_model_name_placeholder');
    }
    if (activeKind === 'claude') {
      return t('ai_providers.claude_model_name_placeholder');
    }
    if (activeKind === 'codex') {
      return t('ai_providers.codex_model_name_placeholder', {
        defaultValue: '例如 gpt-5.3-codex',
      });
    }
    if (activeKind === 'xai') {
      return t('ai_providers.xai_model_name_placeholder', {
        defaultValue: '例如 grok-3',
      });
    }
    if (activeKind === 'gemini' || activeKind === 'interactions') {
      return t('ai_providers.gemini_model_name_placeholder', {
        defaultValue: '例如 gemini-2.5-pro',
      });
    }
    if (activeKind === 'openai') {
      return t('ai_providers.openai_model_name_placeholder', {
        defaultValue: '例如 gpt-4.1 或 deepseek-chat',
      });
    }
    return t('common.model_name_placeholder');
  }, [activeKind, t]);

  if (supportsDiscovery && modelDiscoveryOpen) {
    return (
      <DrawerTabPanel testId="drawer-model-discovery-subview">
        <DrawerSectionCard className={styles.discoverySubViewCard}>
          <DrawerSubViewHeader
            backLabel={t('ai_providers.model_discovery_back_btn', { defaultValue: '返回映射列表' })}
            onBack={() => setModelDiscoveryOpen(false)}
            title={
              <>
                {discoveryModalTitle}
                {modelsLoaded && discoveredModels.length > 0 && (
                  <span className={styles.tabCountBadge} style={{ marginLeft: 8 }}>
                    {discoveredModelsFiltered.length}/{discoveredModels.length}
                  </span>
                )}
              </>
            }
            actions={
              <Button
                variant="secondary"
                size="xs"
                onClick={() => void fetchModelDiscovery()}
                loading={modelDiscoveryFetching}
              >
                <IconRefreshCw size={13} />
                {t('common.refresh')}
              </Button>
            }
          />

          <div className={styles.preflightEndpointBar}>
            <span className={styles.httpMethodBadge}>GET</span>
            <span className={styles.preflightUrl}>{modelDiscoveryEndpoint || '—'}</span>
          </div>

          {modelDiscoveryError && (
            <div className={styles.inlineError} role="alert">
              {modelDiscoveryError}
            </div>
          )}

          <div className={styles.modelDiscoveryToolbar}>
            <div className={styles.discoverySearchBox}>
              <Input
                placeholder={discoverySearchPlaceholder}
                value={modelDiscoverySearch}
                onChange={(e) => setModelDiscoverySearch(e.target.value)}
                rightElement={
                  modelDiscoverySearch ? (
                    <button
                      type="button"
                      className={styles.inputEyeBtn}
                      onClick={() => setModelDiscoverySearch('')}
                      aria-label={t('common.clear', { defaultValue: '清除' })}
                    >
                      <IconX size={13} />
                    </button>
                  ) : (
                    <IconSearch size={14} />
                  )
                }
              />
            </div>
            <div className={styles.sectionMetaActions}>
              {modelDiscoverySelected.size > 0 && (
                <span className={styles.officialBadge}>
                  {t('ai_providers.model_discovery_selected_count', {
                    count: modelDiscoverySelected.size,
                    defaultValue: '已选 {{count}}',
                  })}
                </span>
              )}
              {visibleDiscoverableModelNames.length > 0 && (
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={
                    allVisibleSelected
                      ? handleClearModelDiscoverySelection
                      : handleSelectVisibleModels
                  }
                >
                  {allVisibleSelected
                    ? t('common.clear_selection', { defaultValue: '取消全选' })
                    : t('common.select_all', { defaultValue: '全选当前' })}
                </Button>
              )}
            </div>
          </div>

          {modelDiscoveryFetching && !modelsLoaded ? (
            <div className={styles.overviewEmptyState}>
              {t('ai_providers.fetching_models_loading', {
                defaultValue: '正在获取上游模型列表...',
              })}
            </div>
          ) : !modelsLoaded && modelDiscoveryError ? null : !discoveredModels.length ||
            discoveredModelsFiltered.length === 0 ? (
            <div className={styles.overviewEmptyState}>
              {t('ai_providers.no_discovered_models', { defaultValue: '暂无可展示的模型' })}
            </div>
          ) : (
            <div className={styles.modelDiscoveryList}>
              {discoveredModelsFiltered.map((model) => {
                const cleanName =
                  activeKind === 'gemini' || activeKind === 'interactions'
                    ? model.name.replace(/^\/?models\//i, '').trim()
                    : model.name.trim();
                const alreadyAdded = configuredModelNames.has(cleanName.toLowerCase());
                const checked = alreadyAdded || modelDiscoverySelected.has(cleanName);

                return (
                  <div
                    key={`${model.name}-${model.alias ?? ''}`}
                    className={`${styles.modelDiscoveryRow} ${
                      checked && !alreadyAdded ? styles.modelDiscoveryRowSelected : ''
                    }`}
                  >
                    <SelectionCheckbox
                      checked={checked}
                      disabled={alreadyAdded}
                      onChange={() => toggleModelDiscoverySelection(model.name)}
                      ariaLabel={cleanName}
                      label={
                        <div className={styles.modelDiscoveryMeta}>
                          <div className={styles.modelDiscoveryNameRow}>
                            <span>
                              <strong className={styles.fieldValueMono}>{cleanName}</strong>
                              {model.alias && model.alias !== cleanName && (
                                <span className={styles.modelDiscoveryAlias}>
                                  ({model.alias})
                                </span>
                              )}
                            </span>
                            {alreadyAdded && (
                              <span className={styles.modelDiscoveryAddedBadge}>
                                {t('ai_providers.model_already_added', { defaultValue: '已添加' })}
                              </span>
                            )}
                          </div>
                          {model.description && (
                            <span className={styles.modelDiscoveryDesc}>{model.description}</span>
                          )}
                        </div>
                      }
                    />
                  </div>
                );
              })}
            </div>
          )}
        </DrawerSectionCard>
      </DrawerTabPanel>
    );
  }

  return (
    <div className={styles.drawerTabPanel}>
      <section className={styles.sectionCard}>
        <div className={styles.sectionHeader}>
          <div className={styles.sectionHeading}>
            <IconModelCluster size={15} />
            <h4>
              {activeKind === 'vertex'
                ? t('ai_providers.vertex_models_label')
                : t('ai_providers.models_mapping_title', { defaultValue: '模型映射列表' })}
            </h4>
            <span className={styles.tabCountBadge}>{configuredModelCount}</span>
            {isOpenAI && (
              <InfoTooltip
                content={t('ai_providers.openai_model_discovery_proxy_version_hint')}
                ariaLabel={t('ai_providers.openai_model_discovery_proxy_version_hint')}
              />
            )}
          </div>
          <div className={styles.modelToolbar}>
            {supportsDiscovery && (
              <Button
                variant="secondary"
                size="xs"
                onClick={() => setModelDiscoveryOpen(true)}
                disabled={controlsDisabled}
              >
                {t('ai_providers.fetch_upstream_models_btn', {
                  defaultValue: '从上游 /models 获取',
                })}
              </Button>
            )}
            <Button
              variant="secondary"
              size="xs"
              onClick={() => {
                setModelFilter('');
                setPinnedEditOrigIndex(null);
                setForm((prev) => ({
                  ...prev,
                  modelEntries: [...prev.modelEntries, { name: '', alias: '' }],
                }));
              }}
              disabled={controlsDisabled}
            >
              <IconPlus size={13} />
              {t('ai_providers.add_model_row_btn', { defaultValue: '添加模型' })}
            </Button>
          </div>
        </div>

        <p className={styles.sectionHint}>
          {activeKind === 'vertex'
            ? t('ai_providers.vertex_add_modal_models_hint')
            : t('ai_providers.models_mapping_hint', {
                defaultValue: '留空或仅填少量映射时，其余上游模型将按默认规则透传或受下方排除规则约束。',
              })}
        </p>

        {(form.modelEntries.length > 4 || modelFilter.trim().length > 0) && (
          <Input
            placeholder={t('ai_providers.filter_models_placeholder', {
              defaultValue: '筛选模型名称或别名...',
            })}
            value={modelFilter}
            onChange={(e) => {
              setModelFilter(e.target.value);
              setPinnedEditOrigIndex(null);
            }}
          />
        )}

        {filteredEntries.length === 0 ? (
          <div className={styles.emptyHint}>
            {t('ai_providers.filter_models_empty', { defaultValue: '没有匹配的模型条目' })}
          </div>
        ) : (
          <ModelInputList
            entries={filteredEntries}
            onChange={handleModelListChange}
            onRemove={
              normalizedFilter || form.modelEntries.length > 1
                ? handleRemoveVisibleModel
                : undefined
            }
            nameColumnLabel={t('ai_providers.model_upstream_column_label', {
              defaultValue: '上游模型 ID',
            })}
            aliasColumnLabel={t('ai_providers.model_alias_column_label', {
              defaultValue: '对外调用别名（可选）',
            })}
            namePlaceholder={modelNamePlaceholder}
            aliasPlaceholder={
              activeKind === 'vertex'
                ? t('ai_providers.vertex_model_alias_placeholder')
                : activeKind === 'claude'
                  ? t('ai_providers.claude_model_alias_placeholder')
                  : t('common.model_alias_placeholder')
            }
            disabled={controlsDisabled}
            hideAddButton
            showForceMapping={
              isOpenAI ||
              activeKind === 'claude' ||
              activeKind === 'codex' ||
              activeKind === 'xai' ||
              activeKind === 'gemini' ||
              activeKind === 'interactions' ||
              activeKind === 'vertex'
            }
            showModalities={isOpenAI}
            showThinkingLevels={isOpenAI}
            forceMappingLabel={t('ai_providers.force_mapping_label')}
            inputModalitiesPlaceholder={t('ai_providers.input_modalities_placeholder')}
            outputModalitiesPlaceholder={t('ai_providers.output_modalities_placeholder')}
            modelFallbackLabel={t('ai_providers.thinking_model_fallback_label')}
            thinkingLabel={t('ai_providers.thinking_levels_label')}
            thinkingTooltip={t('ai_providers.thinking_levels_tooltip')}
            thinkingTooltipAriaLabel={t('ai_providers.thinking_levels_tooltip_aria_label')}
            thinkingDefaultLabel={t('ai_providers.thinking_default_label')}
            thinkingCustomLabel={t('ai_providers.thinking_custom_label')}
            thinkingAllowedLevelsLabel={t('ai_providers.thinking_allowed_levels_label')}
            thinkingSelectAllLabel={t('ai_providers.thinking_select_all_label')}
            thinkingClearLabel={t('ai_providers.thinking_clear_label')}
            thinkingRequiredError={t('ai_providers.thinking_required_error')}
            thinkingUnknownLevelsLabel={t('ai_providers.thinking_unknown_levels_label')}
            thinkingUnknownLevelsHint={t('ai_providers.thinking_unknown_levels_hint')}
            className={styles.modelListStack}
            itemClassName={styles.modelEntryRow}
            rowClassName={styles.modelInputRow}
            inputClassName={styles.modelInputField}
            removeButtonClassName={styles.modelRowRemoveButton}
          />
        )}
      </section>

      {/* 排除模型规则区 (non-OpenAI) */}
      {!isOpenAI && (
        <section className={styles.sectionCard}>
          <div className={styles.sectionHeader}>
            <div className={styles.sectionHeading}>
              <IconShield size={15} />
              <h4>{cleanHeadingText(t('ai_providers.excluded_models_label'))}</h4>
            </div>
          </div>
          <p className={styles.sectionHint}>
            {t('ai_providers.excluded_models_workspace_hint_v2', {
              example: excludedExampleToken,
              defaultValue:
                '每行一条上游模型名或通配规则（如 {{example}}）；渠道停用标记由系统底层自动维护。',
            })}
          </p>
          <textarea
            className={`input ${styles.monoInput}`}
            rows={3}
            placeholder={excludedPlaceholder}
            value={form.excludedText}
            onChange={(e) => setForm((prev) => ({ ...prev, excludedText: e.target.value }))}
            disabled={controlsDisabled}
          />
        </section>
      )}

    </div>
  );
}
