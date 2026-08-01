import { Fragment } from 'react';
import { Button } from './Button';
import { IconX } from './icons';
import { ToggleSwitch } from './ToggleSwitch';
import type { ModelEntry } from './modelInputListUtils';
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
  removeButtonClassName?: string;
  removeButtonTitle?: string;
  removeButtonAriaLabel?: string;
  showForceMapping?: boolean;
  showModalities?: boolean;
  forceMappingLabel?: string;
  inputModalitiesPlaceholder?: string;
  outputModalitiesPlaceholder?: string;
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
  removeButtonClassName = '',
  removeButtonTitle = 'Remove',
  removeButtonAriaLabel = 'Remove',
  showForceMapping = false,
  showModalities = false,
  forceMappingLabel = 'Rewrite response model',
  inputModalitiesPlaceholder = 'Input modalities: text, image',
  outputModalitiesPlaceholder = 'Output modalities: text, image',
}: ModelInputListProps) {
  const currentEntries = entries.length ? entries : [{ name: '', alias: '' }];
  const containerClassName = ['header-input-list', className].filter(Boolean).join(' ');
  const inputClassNames = ['input', inputClassName].filter(Boolean).join(' ');
  const rowClassNames = ['header-input-row', rowClassName].filter(Boolean).join(' ');

  const updateEntry = (index: number, field: 'name' | 'alias', value: string) => {
    const next = currentEntries.map((entry, idx) =>
      idx === index ? { ...entry, [field]: value } : entry
    );
    onChange(next);
  };

  const updateAdvancedEntry = (index: number, patch: Partial<ModelEntry>) => {
    const next = currentEntries.map((entry, idx) =>
      idx === index ? { ...entry, ...patch } : entry
    );
    onChange(next);
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
      {currentEntries.map((entry, index) => (
        <Fragment key={index}>
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
                    value={
                      entry.inputModalitiesDraft ?? (entry.inputModalities ?? []).join(', ')
                    }
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
        </Fragment>
      ))}
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
