import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { MobileFilterSheet } from '@/components/common/MobileFilterSheet';
import { PageControlBar } from '@/components/common/PageControlBar';
import { Button } from '@/components/ui/Button';
import { DropdownMenu } from '@/components/ui/DropdownMenu';
import { Input } from '@/components/ui/Input';
import { SegmentedTabs, type SegmentedTabItem } from '@/components/ui/SegmentedTabs';
import { Select, type SelectOption } from '@/components/ui/Select';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import { SortControl, type SortControlOption } from '@/components/ui/SortControl';
import {
  IconChevronDown,
  IconFilterAll,
  IconPlus,
  IconRefreshCw,
  IconRotateCcw,
  IconSearch,
  IconShield,
  IconSlidersHorizontal,
  IconX,
} from '@/components/ui/icons';
import { getProviderKindIcon, PROVIDER_KIND_LABELS } from '../ProviderTable/kindMeta';
import { PROVIDER_KINDS, type ProviderKind } from '../ProviderTable/rowData';
import type {
  ProviderKindFilter,
  ProviderSortDirection,
  ProviderSortOption,
} from '../ProviderTable/sort';
import type { ProviderHealthFilter, ProviderStatusFilter } from '../types';
import styles from './ProviderToolbar.module.scss';

export interface ProviderToolbarProps {
  kind: ProviderKindFilter;
  kindCounts: Record<ProviderKindFilter, number>;
  supportedKinds?: readonly ProviderKind[];
  onKindChange: (kind: ProviderKindFilter) => void;
  statusFilter?: ProviderStatusFilter;
  onStatusFilterChange?: (status: ProviderStatusFilter) => void;
  healthFilter?: ProviderHealthFilter;
  onHealthFilterChange?: (health: ProviderHealthFilter) => void;
  searchText: string;
  onSearchTextChange: (value: string) => void;
  allModelNames: string[];
  selectedModels: Set<string>;
  onSelectedModelsChange: (models: Set<string>) => void;
  sortOption: ProviderSortOption;
  onSortOptionChange: (option: ProviderSortOption) => void;
  sortDirection: ProviderSortDirection;
  onSortDirectionChange: (direction: ProviderSortDirection) => void;
  onResetFilters?: () => void;
  totalCount?: number;
  filteredCount?: number;
  disabled: boolean;
  resolvedTheme: string;
  onRefresh?: () => void;
  refreshing?: boolean;
  onAdd: (kind: ProviderKind) => void;
  onHealthCheck: () => void;
  healthCheckDisabled?: boolean;
}

export function ProviderToolbar({
  kind,
  kindCounts,
  supportedKinds = PROVIDER_KINDS,
  onKindChange,
  statusFilter = 'all',
  onStatusFilterChange,
  healthFilter = 'all',
  onHealthFilterChange,
  searchText,
  onSearchTextChange,
  allModelNames,
  selectedModels,
  onSelectedModelsChange,
  sortOption,
  onSortOptionChange,
  sortDirection,
  onSortDirectionChange,
  onResetFilters,
  totalCount,
  filteredCount,
  disabled,
  resolvedTheme,
  onRefresh,
  refreshing = false,
  onAdd,
  onHealthCheck,
  healthCheckDisabled = false,
}: ProviderToolbarProps) {
  const { t } = useTranslation();
  const [isModelDropdownOpen, setIsModelDropdownOpen] = useState(false);
  const [isMobileFilterOpen, setIsMobileFilterOpen] = useState(false);
  const modelDropdownRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!isModelDropdownOpen || typeof document === 'undefined') {
      return;
    }

    const handleClickOutside = (event: MouseEvent) => {
      const target = event.target as Node;
      if (!modelDropdownRef.current?.contains(target)) {
        setIsModelDropdownOpen(false);
      }
    };

    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, [isModelDropdownOpen]);

  const visibleKinds = useMemo(
    () =>
      kind !== 'all' && !supportedKinds.includes(kind) ? [...supportedKinds, kind] : supportedKinds,
    [kind, supportedKinds]
  );

  const resolvedTotalCount = totalCount ?? kindCounts.all ?? 0;
  const kindTabItems = useMemo<ReadonlyArray<SegmentedTabItem<ProviderKindFilter>>>(
    () => [
      {
        id: 'all',
        title: t('ai_providers.filter_all'),
        label: (
          <span className={styles.kindTabContent}>
            <span className={styles.kindTabIcon} aria-hidden="true">
              <IconFilterAll size={16} />
            </span>
            <span className={styles.kindTabLabel}>{t('ai_providers.filter_all')}</span>
            <span className={styles.kindTabBadge}>{kindCounts.all ?? 0}</span>
          </span>
        ),
      },
      ...visibleKinds.map((id) => ({
        id: id as ProviderKindFilter,
        title: PROVIDER_KIND_LABELS[id],
        label: (
          <span className={styles.kindTabContent}>
            <span className={styles.kindTabIcon} aria-hidden="true">
              <img
                src={getProviderKindIcon(id, resolvedTheme)}
                alt=""
                className={[
                  styles.kindTabProviderIcon,
                  id === 'codex' ? styles.kindTabProviderIconCodex : '',
                  id === 'openai' ? styles.kindTabProviderIconOpenai : '',
                ]
                  .filter(Boolean)
                  .join(' ')}
              />
            </span>
            <span className={styles.kindTabLabel}>{PROVIDER_KIND_LABELS[id]}</span>
            <span className={styles.kindTabBadge}>{kindCounts[id] ?? 0}</span>
          </span>
        ),
      })),
    ],
    [kindCounts, resolvedTheme, t, visibleKinds]
  );

  const statusOptions = useMemo<ReadonlyArray<SelectOption>>(
    () => [
      {
        value: 'all',
        label: t('ai_providers.status_filter_all', { defaultValue: '全部状态' }),
      },
      {
        value: 'enabled',
        label: t('ai_providers.status_filter_enabled', { defaultValue: '已启用' }),
      },
      {
        value: 'disabled',
        label: t('ai_providers.status_filter_disabled', { defaultValue: '已停用' }),
      },
    ],
    [t]
  );

  const healthOptions = useMemo<ReadonlyArray<SelectOption>>(
    () => [
      {
        value: 'all',
        label: t('ai_providers.health_filter_all', { defaultValue: '全部健康度' }),
      },
      {
        value: 'has_failure',
        label: t('ai_providers.health_filter_has_failure', { defaultValue: '存在失败' }),
      },
      {
        value: 'active',
        label: t('ai_providers.health_filter_active', { defaultValue: '近期活跃' }),
      },
      {
        value: 'idle',
        label: t('ai_providers.health_filter_idle', { defaultValue: '暂无请求' }),
      },
    ],
    [t]
  );

  const sortOptions = useMemo<ReadonlyArray<SortControlOption<ProviderSortOption>>>(
    () => [
      { value: 'priority', label: t('common.priority') },
      { value: 'name', label: t('ai_providers.table_col_identity') },
      { value: 'recent-success', label: t('ai_providers.table_col_recent') },
      { value: 'models', label: t('ai_providers.table_col_models') },
    ],
    [t]
  );

  const selectedModelNames = useMemo(() => Array.from(selectedModels).sort(), [selectedModels]);
  const modelFilterActive = selectedModelNames.length > 0;
  const modelFilterLabel = t('ai_providers.table_col_models');
  const modelFilterTitle = modelFilterActive
    ? selectedModelNames.join(', ')
    : t('ai_providers.model_search_placeholder');

  const hasActiveFilters =
    kind !== 'all' ||
    statusFilter !== 'all' ||
    healthFilter !== 'all' ||
    modelFilterActive ||
    searchText.trim().length > 0;

  const activeMobileFilterCount = [
    statusFilter !== 'all',
    healthFilter !== 'all',
    modelFilterActive,
    sortOption !== 'priority' || sortDirection !== 'desc',
  ].filter(Boolean).length;

  const toggleModelSelection = (modelName: string) => {
    const next = new Set(selectedModels);
    if (next.has(modelName)) {
      next.delete(modelName);
    } else {
      next.add(modelName);
    }
    onSelectedModelsChange(next);
  };

  const clearAllModels = () => {
    onSelectedModelsChange(new Set());
  };

  const handleResetAllFilters = () => {
    if (onResetFilters) {
      onResetFilters();
      return;
    }
    onKindChange('all');
    onStatusFilterChange?.('all');
    onHealthFilterChange?.('all');
    onSelectedModelsChange(new Set());
    onSearchTextChange('');
  };

  const handleResetMobileSheet = () => {
    onStatusFilterChange?.('all');
    onHealthFilterChange?.('all');
    onSelectedModelsChange(new Set());
    onSortOptionChange('priority');
    onSortDirectionChange('desc');
  };

  const toggleSortDirection = () => {
    if (disabled) return;
    onSortDirectionChange(sortDirection === 'asc' ? 'desc' : 'asc');
  };

  const addMenuItems = visibleKinds.map((id) => ({
    key: id,
    label: PROVIDER_KIND_LABELS[id],
    icon: (
      <img
        src={getProviderKindIcon(id, resolvedTheme)}
        alt=""
        className={[
          styles.addMenuProviderIcon,
          id === 'codex' ? styles.kindTabProviderIconCodex : '',
          id === 'openai' ? styles.kindTabProviderIconOpenai : '',
        ]
          .filter(Boolean)
          .join(' ')}
      />
    ),
    onClick: () => onAdd(id),
  }));

  const activeFilterSummary =
    hasActiveFilters && filteredCount !== undefined && resolvedTotalCount > 0
      ? t('ai_providers.workspace_filter_summary', {
          filtered: filteredCount,
          total: resolvedTotalCount,
          defaultValue: `命中 ${filteredCount} / ${resolvedTotalCount}`,
        })
      : hasActiveFilters
        ? t('ai_providers.workspace_filters_active', { defaultValue: '筛选已生效' })
        : '';

  const mobileSheetSummary =
    activeFilterSummary ||
    t('ai_providers.workspace_total_summary', {
      count: resolvedTotalCount,
      defaultValue: `共 ${resolvedTotalCount} 条上游渠道`,
    });

  return (
    <>
      <PageControlBar
        topRowClassName={styles.topRow}
        leadingClassName={styles.leading}
        actionsClassName={styles.actions}
        toolbarClassName={styles.toolbar}
        leading={
          <div className={styles.kindTabsStrip}>
            <SegmentedTabs
              items={kindTabItems}
              activeTab={kind}
              onChange={onKindChange}
              ariaLabel={t('ai_providers.filter_kind_aria')}
              idBase="provider-kind-filter"
              variant="default"
              size="md"
              responsiveFullWidth={false}
              disabled={disabled}
              className={styles.kindTabs}
            />
          </div>
        }
        actions={
          <>
            {onRefresh ? (
              <Button
                type="button"
                variant="secondary"
                size="sm"
                onClick={onRefresh}
                loading={refreshing}
                disabled={disabled || refreshing}
                className={styles.actionButton}
              >
                {!refreshing ? <IconRefreshCw size={15} /> : null}
                {t('common.refresh')}
              </Button>
            ) : null}
            <Button
              type="button"
              variant="secondary"
              size="sm"
              onClick={onHealthCheck}
              disabled={disabled || healthCheckDisabled}
              className={styles.actionButton}
            >
              <IconShield size={14} />
              {t('ai_providers.health_check_button')}
            </Button>
            {kind === 'all' ? (
              <DropdownMenu
                ariaLabel={t('ai_providers.add_config_menu_aria')}
                triggerLabel={t('ai_providers.add_config_button')}
                triggerIcon={<IconPlus size={14} />}
                triggerClassName={styles.addButton}
                disabled={disabled}
                items={addMenuItems}
              />
            ) : (
              <Button
                type="button"
                variant="primary"
                size="sm"
                onClick={() => onAdd(kind)}
                disabled={disabled}
                className={styles.addButton}
              >
                <IconPlus size={14} />
                {t('ai_providers.add_kind_button', { name: PROVIDER_KIND_LABELS[kind] })}
              </Button>
            )}
          </>
        }
        toolbar={
          <div className={styles.filterRow}>
              <div className={styles.searchWrap}>
                <Input
                  type="search"
                  className={styles.searchInput}
                  value={searchText}
                  placeholder={t('ai_providers.search_placeholder')}
                  aria-label={t('ai_providers.search_placeholder')}
                  disabled={disabled}
                  onChange={(event) => onSearchTextChange(event.target.value)}
                  rightElement={
                    searchText ? (
                      <button
                        type="button"
                        className={styles.searchClear}
                        onClick={() => onSearchTextChange('')}
                        disabled={disabled}
                        aria-label={t('ai_providers.model_search_clear')}
                        title={t('ai_providers.model_search_clear')}
                      >
                        <IconX size={14} />
                      </button>
                    ) : (
                      <span className={styles.searchIcon} aria-hidden="true">
                        <IconSearch size={15} />
                      </span>
                    )
                  }
                />
              </div>

              <div className={styles.desktopControls}>
                <Select
                  value={statusFilter}
                  options={statusOptions}
                  onChange={(value) => onStatusFilterChange?.(value as ProviderStatusFilter)}
                  disabled={disabled || !onStatusFilterChange}
                  ariaLabel={t('ai_providers.status_filter_aria', { defaultValue: '按启用状态筛选' })}
                  className={styles.filterSelect}
                />

                <Select
                  value={healthFilter}
                  options={healthOptions}
                  onChange={(value) => onHealthFilterChange?.(value as ProviderHealthFilter)}
                  disabled={disabled || !onHealthFilterChange}
                  ariaLabel={t('ai_providers.health_filter_aria', { defaultValue: '按健康度筛选' })}
                  className={styles.filterSelect}
                />

                <div className={styles.modelMultiSelectWrapper} ref={modelDropdownRef}>
                  <div
                    className={[
                      styles.modelFilterControl,
                      modelFilterActive ? styles.modelFilterControlActive : '',
                      disabled ? styles.modelFilterControlDisabled : '',
                    ]
                      .filter(Boolean)
                      .join(' ')}
                  >
                    <button
                      type="button"
                      className={styles.modelFilterTrigger}
                      onClick={() => setIsModelDropdownOpen((prev) => !prev)}
                      disabled={disabled}
                      title={modelFilterTitle}
                      aria-label={modelFilterTitle}
                      aria-haspopup="true"
                      aria-expanded={isModelDropdownOpen}
                    >
                      <span className={styles.modelFilterText}>{modelFilterLabel}</span>
                      {modelFilterActive ? (
                        <span className={styles.modelFilterCount}>{selectedModelNames.length}</span>
                      ) : null}
                      <span className={styles.modelFilterChevron} aria-hidden="true">
                        <IconChevronDown size={14} />
                      </span>
                    </button>
                    {modelFilterActive ? (
                      <button
                        type="button"
                        className={styles.modelFilterInlineClear}
                        onClick={clearAllModels}
                        disabled={disabled}
                        aria-label={t('ai_providers.model_search_clear')}
                        title={t('ai_providers.model_search_clear')}
                      >
                        <IconX size={14} />
                      </button>
                    ) : null}
                  </div>

                  {isModelDropdownOpen ? (
                    <div className={styles.modelDropdownList}>
                      <div className={styles.modelDropdownHeader}>
                        <Button
                          type="button"
                          variant="ghost"
                          size="sm"
                          onClick={() => onSelectedModelsChange(new Set(allModelNames))}
                          disabled={disabled || allModelNames.length === 0}
                        >
                          {t('ai_providers.model_select_all')}
                        </Button>
                        {modelFilterActive ? (
                          <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            onClick={clearAllModels}
                            disabled={disabled}
                          >
                            {t('ai_providers.model_search_clear')}
                          </Button>
                        ) : null}
                      </div>
                      <div
                        className={styles.modelDropdownItems}
                        role="group"
                        aria-label={t('ai_providers.model_search_placeholder')}
                      >
                        {allModelNames.length === 0 ? (
                          <div className={styles.modelDropdownEmpty}>
                            {t('ai_providers.model_filter_empty')}
                          </div>
                        ) : (
                          allModelNames.map((name) => (
                            <SelectionCheckbox
                              key={`provider-model-option-${name}`}
                              checked={selectedModels.has(name)}
                              onChange={() => toggleModelSelection(name)}
                              disabled={disabled}
                              className={styles.modelDropdownItem}
                              labelClassName={styles.modelDropdownItemLabel}
                              label={<span title={name}>{name}</span>}
                            />
                          ))
                        )}
                      </div>
                    </div>
                  ) : null}
                </div>

                {hasActiveFilters ? (
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={handleResetAllFilters}
                    disabled={disabled}
                    className={styles.resetButton}
                  >
                    <IconRotateCcw size={14} />
                    {t('ai_providers.reset_filters', { defaultValue: '重置筛选' })}
                  </Button>
                ) : null}

                <div className={styles.sortGroup}>
                  {activeFilterSummary ? (
                    <span className={styles.filterSummaryNote}>{activeFilterSummary}</span>
                  ) : null}
                  <SortControl<ProviderSortOption>
                    options={sortOptions}
                    value={sortOption}
                    direction={sortDirection}
                    onChangeValue={onSortOptionChange}
                    onToggleDirection={toggleSortDirection}
                    ariaLabel={t('ai_providers.sort_by')}
                    directionLabel={
                      sortDirection === 'asc'
                        ? t('ai_providers.sort_ascending')
                        : t('ai_providers.sort_descending')
                    }
                    title={t('ai_providers.sort_by')}
                    directionDisabled={disabled}
                    dropdownAlign="right"
                    className={styles.sortControl}
                  />
                </div>
              </div>

              <div className={styles.mobileFilterWrap}>
                <Button
                  type="button"
                  variant="secondary"
                  size="sm"
                  onClick={() => setIsMobileFilterOpen(true)}
                  disabled={disabled}
                  className={styles.mobileFilterButton}
                >
                  <IconSlidersHorizontal size={14} />
                  <span>{t('common.filters', { defaultValue: '筛选' })}</span>
                  {activeMobileFilterCount > 0 ? (
                    <span className={styles.mobileFilterCount}>{activeMobileFilterCount}</span>
                  ) : null}
                </Button>
              </div>
            </div>
        }
      />

      <MobileFilterSheet
        open={isMobileFilterOpen}
        title={t('ai_providers.mobile_filters_title', { defaultValue: '筛选与排序' })}
        summary={mobileSheetSummary}
        resetLabel={t('ai_providers.reset_filters', { defaultValue: '重置筛选' })}
        confirmLabel={t('common.confirm', { defaultValue: '确定' })}
        closeLabel={t('common.close', { defaultValue: '关闭' })}
        titleId="provider-mobile-filter-sheet-title"
        onClose={() => setIsMobileFilterOpen(false)}
        onReset={handleResetMobileSheet}
        resetDisabled={activeMobileFilterCount === 0}
      >
        <div className={styles.mobileSheetField}>
          <span className={styles.mobileSheetFieldLabel}>
            {t('ai_providers.status_filter_label', { defaultValue: '启用状态' })}
          </span>
          <Select
            value={statusFilter}
            options={statusOptions}
            onChange={(value) => onStatusFilterChange?.(value as ProviderStatusFilter)}
            disabled={disabled || !onStatusFilterChange}
            ariaLabel={t('ai_providers.status_filter_aria', { defaultValue: '按启用状态筛选' })}
          />
        </div>

        <div className={styles.mobileSheetField}>
          <span className={styles.mobileSheetFieldLabel}>
            {t('ai_providers.health_filter_label', { defaultValue: '健康状态' })}
          </span>
          <Select
            value={healthFilter}
            options={healthOptions}
            onChange={(value) => onHealthFilterChange?.(value as ProviderHealthFilter)}
            disabled={disabled || !onHealthFilterChange}
            ariaLabel={t('ai_providers.health_filter_aria', { defaultValue: '按健康度筛选' })}
          />
        </div>

        <div className={styles.mobileSheetField}>
          <div className={styles.mobileSheetFieldHeader}>
            <span className={styles.mobileSheetFieldLabel}>{modelFilterLabel}</span>
            <div className={styles.mobileSheetModelActions}>
              <Button
                type="button"
                variant="ghost"
                size="xs"
                onClick={() => onSelectedModelsChange(new Set(allModelNames))}
                disabled={disabled || allModelNames.length === 0}
              >
                {t('ai_providers.model_select_all')}
              </Button>
              {modelFilterActive ? (
                <Button
                  type="button"
                  variant="ghost"
                  size="xs"
                  onClick={clearAllModels}
                  disabled={disabled}
                >
                  {t('ai_providers.model_search_clear')}
                </Button>
              ) : null}
            </div>
          </div>
          <div className={styles.mobileSheetModelList}>
            {allModelNames.length === 0 ? (
              <div className={styles.modelDropdownEmpty}>{t('ai_providers.model_filter_empty')}</div>
            ) : (
              allModelNames.map((name) => (
                <SelectionCheckbox
                  key={`provider-mobile-model-option-${name}`}
                  checked={selectedModels.has(name)}
                  onChange={() => toggleModelSelection(name)}
                  disabled={disabled}
                  className={styles.modelDropdownItem}
                  labelClassName={styles.modelDropdownItemLabel}
                  label={<span title={name}>{name}</span>}
                />
              ))
            )}
          </div>
        </div>

        <div className={styles.mobileSheetField}>
          <span className={styles.mobileSheetFieldLabel}>{t('ai_providers.sort_by')}</span>
          <SortControl<ProviderSortOption>
            options={sortOptions}
            value={sortOption}
            direction={sortDirection}
            onChangeValue={onSortOptionChange}
            onToggleDirection={toggleSortDirection}
            ariaLabel={t('ai_providers.sort_by')}
            directionLabel={
              sortDirection === 'asc'
                ? t('ai_providers.sort_ascending')
                : t('ai_providers.sort_descending')
            }
            title={t('ai_providers.sort_by')}
            directionDisabled={disabled}
            fullWidth
            dropdownDirection="up"
            className={styles.sortControl}
          />
        </div>
      </MobileFilterSheet>
    </>
  );
}
