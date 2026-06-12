import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { DropdownMenu } from '@/components/ui/DropdownMenu';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import {
  IconArrowDownWideNarrow,
  IconArrowUpNarrowWide,
  IconChevronDown,
  IconFilterAll,
  IconSearch,
  IconShield,
  IconX,
} from '@/components/ui/icons';
import { getProviderKindIcon, PROVIDER_KIND_LABELS } from '../ProviderTable/kindMeta';
import { PROVIDER_KINDS, type ProviderKind } from '../ProviderTable/rowData';
import type {
  ProviderKindFilter,
  ProviderSortDirection,
  ProviderSortOption,
} from '../ProviderTable/sort';
import styles from './ProviderToolbar.module.scss';

interface ProviderToolbarProps {
  kind: ProviderKindFilter;
  kindCounts: Record<ProviderKindFilter, number>;
  onKindChange: (kind: ProviderKindFilter) => void;
  searchText: string;
  onSearchTextChange: (value: string) => void;
  allModelNames: string[];
  selectedModels: Set<string>;
  onSelectedModelsChange: (models: Set<string>) => void;
  sortOption: ProviderSortOption;
  onSortOptionChange: (option: ProviderSortOption) => void;
  sortDirection: ProviderSortDirection;
  onSortDirectionChange: (direction: ProviderSortDirection) => void;
  disabled: boolean;
  resolvedTheme: string;
  onAdd: (kind: ProviderKind) => void;
  onHealthCheck: () => void;
  healthCheckDisabled?: boolean;
}

type ProviderKindTab = {
  id: ProviderKindFilter;
  label: string;
  badge: number;
};

type ProviderSortItem = {
  value: ProviderSortOption;
  label: string;
};

export function ProviderToolbar({
  kind,
  kindCounts,
  onKindChange,
  searchText,
  onSearchTextChange,
  allModelNames,
  selectedModels,
  onSelectedModelsChange,
  sortOption,
  onSortOptionChange,
  sortDirection,
  onSortDirectionChange,
  disabled,
  resolvedTheme,
  onAdd,
  onHealthCheck,
  healthCheckDisabled = false,
}: ProviderToolbarProps) {
  const { t } = useTranslation();
  const [isModelDropdownOpen, setIsModelDropdownOpen] = useState(false);
  const [isSortDropdownOpen, setIsSortDropdownOpen] = useState(false);
  const [highlightedSortIndex, setHighlightedSortIndex] = useState(-1);
  const modelDropdownRef = useRef<HTMLDivElement>(null);
  const sortDropdownRef = useRef<HTMLDivElement>(null);
  const sortTriggerRef = useRef<HTMLButtonElement>(null);
  const sortOptionRefs = useRef<Map<ProviderSortOption, HTMLButtonElement | null>>(new Map());
  const kindTabRefs = useRef<Map<ProviderKindFilter, HTMLButtonElement | null>>(new Map());

  useEffect(() => {
    if ((!isModelDropdownOpen && !isSortDropdownOpen) || typeof document === 'undefined') {
      return;
    }

    const handleClickOutside = (event: MouseEvent) => {
      const target = event.target as Node;
      if (isModelDropdownOpen && !modelDropdownRef.current?.contains(target)) {
        setIsModelDropdownOpen(false);
      }
      if (isSortDropdownOpen && !sortDropdownRef.current?.contains(target)) {
        setIsSortDropdownOpen(false);
      }
    };

    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, [isModelDropdownOpen, isSortDropdownOpen]);

  const kindTabs = useMemo<ProviderKindTab[]>(
    () => [
      {
        id: 'all',
        label: t('ai_providers.filter_all'),
        badge: kindCounts.all,
      },
      ...PROVIDER_KINDS.map((id) => ({
        id: id as ProviderKindFilter,
        label: PROVIDER_KIND_LABELS[id],
        badge: kindCounts[id],
      })),
    ],
    [kindCounts, t]
  );

  const focusableKindTabs = useMemo(() => kindTabs.map((tab) => tab.id), [kindTabs]);

  const focusKindTab = useCallback((tab: ProviderKindFilter) => {
    kindTabRefs.current.get(tab)?.focus();
  }, []);

  const handleKindTabKeyDown = useCallback(
    (event: KeyboardEvent<HTMLButtonElement>, currentTab: ProviderKindFilter) => {
      if (disabled || focusableKindTabs.length === 0) return;

      const currentIndex = focusableKindTabs.indexOf(currentTab);
      if (currentIndex === -1) return;

      let nextIndex = currentIndex;
      switch (event.key) {
        case 'ArrowRight':
        case 'ArrowDown':
          nextIndex = (currentIndex + 1) % focusableKindTabs.length;
          break;
        case 'ArrowLeft':
        case 'ArrowUp':
          nextIndex = (currentIndex - 1 + focusableKindTabs.length) % focusableKindTabs.length;
          break;
        case 'Home':
          nextIndex = 0;
          break;
        case 'End':
          nextIndex = focusableKindTabs.length - 1;
          break;
        default:
          return;
      }

      event.preventDefault();
      const nextTab = focusableKindTabs[nextIndex];
      onKindChange(nextTab);
      focusKindTab(nextTab);
    },
    [disabled, focusKindTab, focusableKindTabs, onKindChange]
  );

  const sortOptions = useMemo<ProviderSortItem[]>(
    () => [
      { value: 'priority', label: t('common.priority') },
      { value: 'name', label: t('ai_providers.table_col_identity') },
      { value: 'recent-success', label: t('ai_providers.table_col_recent') },
    ],
    [t]
  );
  const selectedSortIndex = useMemo(
    () => sortOptions.findIndex((option) => option.value === sortOption),
    [sortOption, sortOptions]
  );
  const selectedSortLabel = sortOptions[selectedSortIndex]?.label ?? sortOptions[0]?.label ?? '';

  useEffect(() => {
    if (!isSortDropdownOpen || highlightedSortIndex < 0) return;
    const highlightedOption = sortOptions[highlightedSortIndex];
    if (!highlightedOption) return;
    sortOptionRefs.current.get(highlightedOption.value)?.focus();
  }, [highlightedSortIndex, isSortDropdownOpen, sortOptions]);

  const selectedModelNames = useMemo(() => Array.from(selectedModels).sort(), [selectedModels]);
  const modelFilterActive = selectedModelNames.length > 0;
  const modelFilterLabel = t('ai_providers.table_col_models');
  const modelFilterTitle = modelFilterActive
    ? selectedModelNames.join(', ')
    : t('ai_providers.model_search_placeholder');

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

  const openSortDropdown = () => {
    if (disabled) return;
    setHighlightedSortIndex(selectedSortIndex >= 0 ? selectedSortIndex : 0);
    setIsSortDropdownOpen(true);
  };

  const toggleSortDropdown = () => {
    if (disabled) return;
    if (isSortDropdownOpen) {
      setIsSortDropdownOpen(false);
      return;
    }
    openSortDropdown();
  };

  const closeSortDropdown = () => {
    setIsSortDropdownOpen(false);
    sortTriggerRef.current?.focus();
  };

  const moveSortHighlight = (nextIndex: number) => {
    if (sortOptions.length === 0) return;
    const normalizedIndex = (nextIndex + sortOptions.length) % sortOptions.length;
    setHighlightedSortIndex(normalizedIndex);
  };

  const commitSortOption = (value: ProviderSortOption) => {
    onSortOptionChange(value);
    setIsSortDropdownOpen(false);
    sortTriggerRef.current?.focus();
  };

  const toggleSortDirection = () => {
    if (disabled) return;
    onSortDirectionChange(sortDirection === 'asc' ? 'desc' : 'asc');
  };

  const handleSortTriggerKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    if (disabled) return;

    switch (event.key) {
      case 'Enter':
      case ' ':
        event.preventDefault();
        toggleSortDropdown();
        return;
      case 'ArrowDown':
        event.preventDefault();
        if (!isSortDropdownOpen) {
          openSortDropdown();
          return;
        }
        moveSortHighlight(highlightedSortIndex + 1);
        return;
      case 'ArrowUp':
        event.preventDefault();
        if (!isSortDropdownOpen) {
          openSortDropdown();
          return;
        }
        moveSortHighlight(highlightedSortIndex - 1);
        return;
      case 'Escape':
        if (!isSortDropdownOpen) return;
        event.preventDefault();
        closeSortDropdown();
        return;
      default:
        return;
    }
  };

  const handleSortOptionKeyDown = (
    event: KeyboardEvent<HTMLButtonElement>,
    optionIndex: number
  ) => {
    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault();
        moveSortHighlight(optionIndex + 1);
        return;
      case 'ArrowUp':
        event.preventDefault();
        moveSortHighlight(optionIndex - 1);
        return;
      case 'Home':
        event.preventDefault();
        moveSortHighlight(0);
        return;
      case 'End':
        event.preventDefault();
        moveSortHighlight(sortOptions.length - 1);
        return;
      case 'Escape':
        event.preventDefault();
        closeSortDropdown();
        return;
      default:
        return;
    }
  };

  const addMenuItems = PROVIDER_KINDS.map((id) => ({
    key: id,
    label: PROVIDER_KIND_LABELS[id],
    onClick: () => onAdd(id),
  }));

  return (
    <div className={styles.toolbar}>
      <div
        className={styles.kindTabs}
        role="tablist"
        aria-label={t('ai_providers.filter_kind_aria')}
      >
        {kindTabs.map((tab) => {
          const isActive = tab.id === kind;
          const tabClassName = [
            styles.kindTabButton,
            isActive ? styles.kindTabButtonActive : '',
          ]
            .filter(Boolean)
            .join(' ');
          return (
            <button
              key={tab.id}
              ref={(node) => {
                kindTabRefs.current.set(tab.id, node);
              }}
              type="button"
              role="tab"
              id={`provider-kind-filter-${tab.id}`}
              aria-selected={isActive}
              className={tabClassName}
              disabled={disabled}
              tabIndex={isActive && !disabled ? 0 : -1}
              title={tab.label}
              onClick={() => {
                if (!isActive) onKindChange(tab.id);
              }}
              onKeyDown={(event) => handleKindTabKeyDown(event, tab.id)}
            >
              <span className={styles.kindTabIcon} aria-hidden="true">
                {tab.id === 'all' ? (
                  <IconFilterAll size={18} />
                ) : (
                  <img
                    src={getProviderKindIcon(tab.id, resolvedTheme)}
                    alt=""
                    className={[
                      styles.kindTabProviderIcon,
                      tab.id === 'codex' ? styles.kindTabProviderIconCodex : '',
                      tab.id === 'openai' ? styles.kindTabProviderIconOpenai : '',
                    ]
                      .filter(Boolean)
                      .join(' ')}
                  />
                )}
              </span>
              <span className={styles.kindTabLabel}>{tab.label}</span>
              <span className={styles.kindTabBadge}>{tab.badge}</span>
            </button>
          );
        })}
      </div>

      <div className={styles.controls}>
        <div className={styles.searchBox}>
          <span className={styles.searchIcon} aria-hidden="true">
            <IconSearch size={14} />
          </span>
          <input
            type="search"
            className={styles.searchInput}
            value={searchText}
            placeholder={t('ai_providers.search_placeholder')}
            aria-label={t('ai_providers.search_placeholder')}
            disabled={disabled}
            onChange={(event) => onSearchTextChange(event.target.value)}
          />
          {searchText && (
            <button
              type="button"
              className={styles.searchClear}
              onClick={() => onSearchTextChange('')}
              aria-label={t('ai_providers.model_search_clear')}
            >
              <IconX size={14} />
            </button>
          )}
        </div>

        <div className={styles.filterGroup}>
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
                {modelFilterActive && (
                  <span className={styles.modelFilterCount}>{selectedModelNames.length}</span>
                )}
                <span className={styles.modelFilterChevron} aria-hidden="true">
                  <IconChevronDown size={14} />
                </span>
              </button>
              {modelFilterActive && (
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
              )}
            </div>

            {isModelDropdownOpen && (
              <div className={styles.modelDropdownList}>
                <div className={styles.modelDropdownHeader}>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => onSelectedModelsChange(new Set(allModelNames))}
                    disabled={disabled || allModelNames.length === 0}
                  >
                    {t('ai_providers.model_select_all')}
                  </Button>
                  {modelFilterActive && (
                    <Button variant="ghost" size="sm" onClick={clearAllModels} disabled={disabled}>
                      {t('ai_providers.model_search_clear')}
                    </Button>
                  )}
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
            )}
          </div>
        </div>

        <div className={styles.sortControls} ref={sortDropdownRef}>
          <button
            ref={sortTriggerRef}
            type="button"
            className={styles.sortTrigger}
            onClick={toggleSortDropdown}
            onKeyDown={handleSortTriggerKeyDown}
            disabled={disabled}
            title={`${t('ai_providers.sort_by')}: ${selectedSortLabel}`}
            aria-label={`${t('ai_providers.sort_by')}: ${selectedSortLabel}`}
            aria-haspopup="listbox"
            aria-expanded={isSortDropdownOpen}
          >
            <span className={styles.sortLabel}>{selectedSortLabel}</span>
          </button>
          <button
            type="button"
            className={styles.sortDirectionButton}
            onClick={toggleSortDirection}
            disabled={disabled}
            title={
              sortDirection === 'asc'
                ? t('ai_providers.sort_ascending')
                : t('ai_providers.sort_descending')
            }
            aria-label={
              sortDirection === 'asc'
                ? t('ai_providers.sort_ascending')
                : t('ai_providers.sort_descending')
            }
          >
            <span className={styles.sortDirectionIcon} aria-hidden="true">
              {sortDirection === 'asc' ? (
                <IconArrowUpNarrowWide size={14} />
              ) : (
                <IconArrowDownWideNarrow size={14} />
              )}
            </span>
          </button>
          {isSortDropdownOpen && (
            <div className={styles.sortDropdownList} role="listbox">
              {sortOptions.map((option, optionIndex) => {
                const isSelected = option.value === sortOption;
                const isHighlighted = optionIndex === highlightedSortIndex;
                const optionClassName = [
                  styles.sortDropdownItem,
                  isSelected ? styles.sortDropdownItemSelected : '',
                  isHighlighted ? styles.sortDropdownItemHighlighted : '',
                ]
                  .filter(Boolean)
                  .join(' ');
                return (
                  <button
                    key={option.value}
                    ref={(node) => {
                      sortOptionRefs.current.set(option.value, node);
                    }}
                    type="button"
                    role="option"
                    aria-selected={isSelected}
                    className={optionClassName}
                    onClick={() => commitSortOption(option.value)}
                    onKeyDown={(event) => handleSortOptionKeyDown(event, optionIndex)}
                    onMouseEnter={() => setHighlightedSortIndex(optionIndex)}
                  >
                    {option.label}
                  </button>
                );
              })}
            </div>
          )}
        </div>

        <Button
          variant="secondary"
          size="sm"
          onClick={onHealthCheck}
          disabled={disabled || healthCheckDisabled}
          className={styles.healthCheckButton}
        >
          <IconShield size={14} />
          {t('ai_providers.health_check_button')}
        </Button>
        {kind === 'all' ? (
          <DropdownMenu
            ariaLabel={t('ai_providers.add_config_menu_aria')}
            triggerLabel={t('ai_providers.add_config_button')}
            triggerClassName={styles.addButton}
            disabled={disabled}
            items={addMenuItems}
          />
        ) : (
          <Button
            variant="primary"
            size="sm"
            onClick={() => onAdd(kind)}
            disabled={disabled}
            className={styles.addButton}
          >
            {t('ai_providers.add_kind_button', { name: PROVIDER_KIND_LABELS[kind] })}
          </Button>
        )}
      </div>
    </div>
  );
}
