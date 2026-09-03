import { Fragment, useId } from 'react';
import { Button } from './Button';
import { IconX } from './icons';
import { InfoTooltip } from './InfoTooltip';
import { SelectionCheckbox } from './SelectionCheckbox';
import { ToggleSwitch } from './ToggleSwitch';
import {
  MODEL_THINKING_LEVELS_CLEAR_MARKER,
  hasModelThinkingLevelsClearMarker,
  markModelThinkingLevelsForClear,
  markModelThinkingLevelsForEdit,
} from '@/types';
import {
  buildThinkingWithLevels,
  cloneModelEntry,
  getEffectiveThinkingLevelsForEntry,
  getEffectiveThinkingKnownLevels,
  getKnownThinkingLevels,
  getUnknownThinkingLevels,
  hasExplicitEmptyThinkingContainer,
  hasInvalidThinkingLevelEntry,
  KNOWN_THINKING_LEVELS,
  removeThinkingLevels,
  type ModelEntry,
  type ThinkingLevel,
} from './modelInputListUtils';
import styles from './ModelInputList.module.scss';

interface ModelInputListProps {
  entries: ModelEntry[];
  onChange: (entries: ModelEntry[]) => void;
  addLabel?: string;
  disabled?: boolean;
  namePlaceholder?: string;
  aliasPlaceholder?: string;
  hideAddButton?: boolean;
  onAdd?: () => void;
  className?: string;
  rowClassName?: string;
  inputClassName?: string;
  itemClassName?: string;
  modelFallbackLabel?: string;
  removeButtonClassName?: string;
  removeButtonTitle?: string;
  removeButtonAriaLabel?: string;
  showForceMapping?: boolean;
  showModalities?: boolean;
  showThinkingLevels?: boolean;
  forceMappingLabel?: string;
  inputModalitiesPlaceholder?: string;
  outputModalitiesPlaceholder?: string;
  thinkingLabel?: string;
  thinkingTooltip?: string;
  thinkingTooltipAriaLabel?: string;
  thinkingDefaultLabel?: string;
  thinkingCustomLabel?: string;
  thinkingAllowedLevelsLabel?: string;
  thinkingSelectAllLabel?: string;
  thinkingClearLabel?: string;
  thinkingRequiredError?: string;
  thinkingUnknownLevelsLabel?: string;
  thinkingUnknownLevelsHint?: string;
}

const parseModalities = (value: string) =>
  value
    .split(/[\n,]+/)
    .map((item) => item.trim())
    .filter(Boolean);

export function ModelInputList({
  entries,
  onChange,
  addLabel,
  disabled = false,
  namePlaceholder = 'model-name',
  aliasPlaceholder = 'alias (optional)',
  hideAddButton = false,
  onAdd,
  className = '',
  rowClassName = '',
  inputClassName = '',
  itemClassName = '',
  removeButtonClassName = '',
  removeButtonTitle = 'Remove',
  removeButtonAriaLabel = 'Remove',
  showForceMapping = false,
  showModalities = false,
  showThinkingLevels = false,
  forceMappingLabel = 'Rewrite response model',
  inputModalitiesPlaceholder = 'Input modalities: text, image',
  outputModalitiesPlaceholder = 'Output modalities: text, image',
  modelFallbackLabel = 'Model',
  thinkingLabel = 'Thinking levels',
  thinkingTooltip = '',
  thinkingTooltipAriaLabel = 'Thinking levels information',
  thinkingDefaultLabel = 'Do not explicitly configure levels',
  thinkingCustomLabel = 'Custom levels',
  thinkingAllowedLevelsLabel = 'Allowed thinking levels',
  thinkingSelectAllLabel = 'Select all',
  thinkingClearLabel = 'Clear known levels',
  thinkingRequiredError = 'Select at least one thinking level',
  thinkingUnknownLevelsLabel = 'Other configured levels',
  thinkingUnknownLevelsHint = 'This level is preserved when saving.',
}: ModelInputListProps) {
  const thinkingModeId = useId();
  const currentEntries = entries.length ? entries : [{ name: '', alias: '' }];
  const containerClassName = ['header-input-list', className].filter(Boolean).join(' ');
  const inputClassNames = ['input', inputClassName].filter(Boolean).join(' ');
  const rowClassNames = ['header-input-row', rowClassName].filter(Boolean).join(' ');
  const itemClassNames = [styles.modelItem, itemClassName].filter(Boolean).join(' ');
  const shouldWrapModelItem = showThinkingLevels || Boolean(itemClassName);

  const updateEntry = (index: number, field: 'name' | 'alias', value: string) => {
    const next = currentEntries.map((entry, idx) =>
      idx === index ? cloneModelEntry(entry, { [field]: value }) : entry
    );
    onChange(next);
  };

  const updateAdvancedEntry = (index: number, patch: Partial<ModelEntry>) => {
    const next = currentEntries.map((entry, idx) =>
      idx === index ? cloneModelEntry(entry, patch) : entry
    );
    onChange(next);
  };

  const updateThinking = (
    index: number,
    selectedLevels: readonly ThinkingLevel[],
    unknownLevels: readonly unknown[]
  ) => {
    const next = currentEntries.map((entry, idx) => {
      if (idx !== index) return entry;
      const nextEntry = cloneModelEntry(entry, {
        thinking: buildThinkingWithLevels(
          entry.thinking,
          selectedLevels,
          unknownLevels,
          getEffectiveThinkingLevelsForEntry(entry)
        ),
      });
      delete nextEntry[MODEL_THINKING_LEVELS_CLEAR_MARKER];
      markModelThinkingLevelsForEdit(nextEntry);
      return nextEntry;
    });
    onChange(next);
  };

  const setThinkingMode = (index: number, custom: boolean) => {
    const entry = currentEntries[index];
    if (!entry) return;
    if (custom) {
      const levels = getEffectiveThinkingLevelsForEntry(entry);
      const selectedLevels = getKnownThinkingLevels(levels);
      updateThinking(index, selectedLevels, getUnknownThinkingLevels(levels));
      return;
    }

    const nextThinking = removeThinkingLevels(
      entry.thinking,
      hasExplicitEmptyThinkingContainer(entry)
    );
    const next = currentEntries.map((item, idx) => {
      if (idx !== index) return item;
      const nextEntry = cloneModelEntry(item);
      delete nextEntry.thinking;
      delete nextEntry[MODEL_THINKING_LEVELS_CLEAR_MARKER];
      if (nextThinking) {
        nextEntry.thinking = nextThinking;
      }
      if (Array.isArray(item.thinking?.levels) || hasModelThinkingLevelsClearMarker(item)) {
        markModelThinkingLevelsForClear(nextEntry);
      }
      return nextEntry;
    });
    onChange(next);
  };

  const updateThinkingLevel = (index: number, level: ThinkingLevel, checked: boolean) => {
    const entry = currentEntries[index];
    if (!entry) return;
    const levels = getEffectiveThinkingLevelsForEntry(entry);
    const selectedLevels = new Set(getEffectiveThinkingKnownLevels(entry));
    if (checked) selectedLevels.add(level);
    else selectedLevels.delete(level);
    updateThinking(index, Array.from(selectedLevels), getUnknownThinkingLevels(levels));
  };

  const selectAllThinkingLevels = (index: number) => {
    const entry = currentEntries[index];
    if (!entry) return;
    updateThinking(
      index,
      KNOWN_THINKING_LEVELS,
      getUnknownThinkingLevels(getEffectiveThinkingLevelsForEntry(entry))
    );
  };

  const clearThinkingLevels = (index: number) => {
    const entry = currentEntries[index];
    if (!entry) return;
    updateThinking(index, [], getUnknownThinkingLevels(getEffectiveThinkingLevelsForEntry(entry)));
  };

  const addEntry = () => {
    if (onAdd) {
      onAdd();
    } else {
      onChange([...currentEntries, { name: '', alias: '' }]);
    }
  };

  const removeEntry = (index: number) => {
    const next = currentEntries.filter((_, idx) => idx !== index);
    onChange(next.length ? next : [{ name: '', alias: '' }]);
  };

  return (
    <div className={containerClassName}>
      {currentEntries.map((entry, index) => {
        const modelOrdinal = `${modelFallbackLabel} ${index + 1}`;
        const modelContext = entry.name.trim()
          ? `${entry.name.trim()} (${modelOrdinal})`
          : modelOrdinal;
        const thinkingAccessibleName = `${modelContext} ${thinkingLabel}`;
        const thinkingTooltipAccessibleName = `${modelContext} ${thinkingTooltipAriaLabel}`;
        const thinkingLevels = getEffectiveThinkingLevelsForEntry(entry);
        const knownThinkingLevels = getKnownThinkingLevels(thinkingLevels);
        const unknownThinkingLevels = getUnknownThinkingLevels(thinkingLevels);
        const modelContent = (
          <>
            <div className={rowClassNames}>
              <input
                className={inputClassNames}
                placeholder={namePlaceholder}
                value={entry.name}
                onChange={(e) => updateEntry(index, 'name', e.target.value)}
                disabled={disabled}
              />
              <span className="header-separator">→</span>
              <input
                className={inputClassNames}
                placeholder={aliasPlaceholder}
                value={entry.alias}
                onChange={(e) => updateEntry(index, 'alias', e.target.value)}
                disabled={disabled}
              />
              <Button
                variant="ghost"
                size="xs"
                iconOnly
                onClick={() => removeEntry(index)}
                disabled={disabled || currentEntries.length <= 1}
                className={removeButtonClassName}
                title={removeButtonTitle}
                aria-label={removeButtonAriaLabel}
              >
                <IconX size={14} />
              </Button>
            </div>
            {(showForceMapping || showModalities) && (
              <div className={styles.advancedRow}>
                {showForceMapping && (
                  <ToggleSwitch
                    label={forceMappingLabel}
                    labelPosition="left"
                    checked={Boolean(entry.forceMapping)}
                    onChange={(forceMapping) => updateAdvancedEntry(index, { forceMapping })}
                    disabled={disabled}
                  />
                )}
                {showModalities && (
                  <>
                    <input
                      className={inputClassNames}
                      placeholder={inputModalitiesPlaceholder}
                      value={entry.inputModalitiesDraft ?? (entry.inputModalities ?? []).join(', ')}
                      aria-label={inputModalitiesPlaceholder}
                      onChange={(event) => {
                        const inputModalitiesDraft = event.target.value;
                        updateAdvancedEntry(index, {
                          inputModalitiesDraft,
                          inputModalities: parseModalities(inputModalitiesDraft),
                        });
                      }}
                      disabled={disabled}
                    />
                    <input
                      className={inputClassNames}
                      placeholder={outputModalitiesPlaceholder}
                      value={
                        entry.outputModalitiesDraft ?? (entry.outputModalities ?? []).join(', ')
                      }
                      aria-label={outputModalitiesPlaceholder}
                      onChange={(event) => {
                        const outputModalitiesDraft = event.target.value;
                        updateAdvancedEntry(index, {
                          outputModalitiesDraft,
                          outputModalities: parseModalities(outputModalitiesDraft),
                        });
                      }}
                      disabled={disabled}
                    />
                  </>
                )}
              </div>
            )}
            {showThinkingLevels && (
              <div className={styles.thinkingSection}>
                <div className={styles.thinkingHeader}>
                  <span className={styles.thinkingLabel}>{thinkingLabel}</span>
                  {thinkingTooltip ? (
                    <InfoTooltip
                      content={thinkingTooltip}
                      ariaLabel={thinkingTooltipAccessibleName}
                    />
                  ) : null}
                </div>
                <div
                  className={styles.thinkingMode}
                  role="radiogroup"
                  aria-label={thinkingAccessibleName}
                >
                  <label className={styles.thinkingModeOption}>
                    <input
                      type="radio"
                      name={`thinking-mode-${thinkingModeId}-${index}`}
                      aria-label={`${modelContext} ${thinkingDefaultLabel}`}
                      checked={!Array.isArray(entry.thinking?.levels)}
                      onChange={() => setThinkingMode(index, false)}
                      disabled={disabled}
                    />
                    <span>{thinkingDefaultLabel}</span>
                  </label>
                  <label className={styles.thinkingModeOption}>
                    <input
                      type="radio"
                      name={`thinking-mode-${thinkingModeId}-${index}`}
                      aria-label={`${modelContext} ${thinkingCustomLabel}`}
                      checked={Array.isArray(entry.thinking?.levels)}
                      onChange={() => setThinkingMode(index, true)}
                      disabled={disabled}
                    />
                    <span>{thinkingCustomLabel}</span>
                  </label>
                </div>
                {Array.isArray(entry.thinking?.levels) && (
                  <div className={styles.thinkingCustomPanel}>
                    <div className={styles.thinkingOptionsHeader}>
                      <span>{thinkingAllowedLevelsLabel}</span>
                      <span className={styles.thinkingActions}>
                        <Button
                          type="button"
                          variant="ghost"
                          size="xs"
                          onClick={() => selectAllThinkingLevels(index)}
                          disabled={disabled}
                          aria-label={`${modelContext} ${thinkingSelectAllLabel}`}
                        >
                          {thinkingSelectAllLabel}
                        </Button>
                        <Button
                          type="button"
                          variant="ghost"
                          size="xs"
                          onClick={() => clearThinkingLevels(index)}
                          disabled={disabled}
                          aria-label={`${modelContext} ${thinkingClearLabel}`}
                        >
                          {thinkingClearLabel}
                        </Button>
                      </span>
                    </div>
                    <div className={styles.thinkingLevels}>
                      {KNOWN_THINKING_LEVELS.map((level) => (
                        <SelectionCheckbox
                          key={level}
                          checked={knownThinkingLevels.includes(level)}
                          onChange={(checked) => updateThinkingLevel(index, level, checked)}
                          disabled={disabled}
                          label={level}
                          ariaLabel={`${modelContext} ${level}`}
                          className={styles.thinkingLevelOption}
                          labelClassName={styles.thinkingLevelLabel}
                        />
                      ))}
                    </div>
                    {unknownThinkingLevels.length > 0 && (
                      <div className={styles.thinkingUnknown} role="note">
                        <div className={styles.thinkingUnknownLabel}>
                          {thinkingUnknownLevelsLabel}
                        </div>
                        <div className={styles.thinkingUnknownLevels}>
                          {unknownThinkingLevels.map((level, levelIndex) => (
                            <span
                              key={`${String(level)}-${levelIndex}`}
                              className={styles.thinkingUnknownChip}
                            >
                              {String(level)}
                            </span>
                          ))}
                        </div>
                        <div className={styles.thinkingUnknownHint}>
                          {thinkingUnknownLevelsHint}
                        </div>
                      </div>
                    )}
                    {hasInvalidThinkingLevelEntry(entry) && (
                      <div className={styles.thinkingError} role="alert">
                        {thinkingRequiredError}
                      </div>
                    )}
                  </div>
                )}
              </div>
            )}
          </>
        );
        return shouldWrapModelItem ? (
          <div key={index} className={itemClassNames}>
            {modelContent}
          </div>
        ) : (
          <Fragment key={index}>{modelContent}</Fragment>
        );
      })}
      {!hideAddButton && addLabel && (
        <Button
          variant="secondary"
          size="xs"
          onClick={addEntry}
          disabled={disabled}
          className="align-start"
        >
          {addLabel}
        </Button>
      )}
    </div>
  );
}
