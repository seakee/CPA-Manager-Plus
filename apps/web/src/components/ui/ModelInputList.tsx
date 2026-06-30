import { Fragment } from 'react';
import { Button } from './Button';
import { IconX } from './icons';
import { SelectionCheckbox } from './SelectionCheckbox';
import type { ModelEntry } from './modelInputListUtils';

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
  forceMappingClassName?: string;
  removeButtonClassName?: string;
  removeButtonTitle?: string;
  removeButtonAriaLabel?: string;
  showForceMapping?: boolean;
  forceMappingLabel?: string;
  forceMappingTitle?: string;
  forceMappingAriaLabel?: string;
}

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
  forceMappingClassName = '',
  removeButtonClassName = '',
  removeButtonTitle = 'Remove',
  removeButtonAriaLabel = 'Remove',
  showForceMapping = false,
  forceMappingLabel,
  forceMappingTitle,
  forceMappingAriaLabel,
}: ModelInputListProps) {
  const currentEntries = entries.length ? entries : [{ name: '', alias: '' }];
  const containerClassName = ['header-input-list', className].filter(Boolean).join(' ');
  const inputClassNames = ['input', inputClassName].filter(Boolean).join(' ');
  const baseRowClassNames = ['header-input-row', rowClassName].filter(Boolean).join(' ');

  const updateEntry = (index: number, field: 'name' | 'alias', value: string) => {
    const next = currentEntries.map((entry, idx) =>
      idx === index ? { ...entry, [field]: value } : entry
    );
    onChange(next);
  };

  const updateForceMapping = (index: number, value: boolean) => {
    const next = currentEntries.map((entry, idx) =>
      idx === index ? { ...entry, forceMapping: value } : entry
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
          <div
            className={[
              baseRowClassNames,
              showForceMapping ? 'model-input-row-with-force-mapping' : '',
            ]
              .filter(Boolean)
              .join(' ')}
          >
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
            {showForceMapping && (
              <SelectionCheckbox
                checked={Boolean(entry.forceMapping)}
                onChange={(value) => updateForceMapping(index, value)}
                label={forceMappingLabel}
                ariaLabel={forceMappingAriaLabel || forceMappingLabel}
                title={forceMappingTitle}
                disabled={disabled}
                className={forceMappingClassName}
              />
            )}
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
