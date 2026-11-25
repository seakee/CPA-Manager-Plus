import { Fragment } from 'react';
import { Button } from './Button';
import { IconX } from './icons';
import type { HeaderEntry } from '@/utils/headers';

interface HeaderInputListProps {
  entries: HeaderEntry[];
  onChange: (entries: HeaderEntry[]) => void;
  addLabel: string;
  disabled?: boolean;
  keyPlaceholder?: string;
  valuePlaceholder?: string;
  removeButtonTitle?: string;
  removeButtonAriaLabel?: string;
  allowEmpty?: boolean;
  hideAddButton?: boolean;
  emptyText?: string;
}

export function HeaderInputList({
  entries,
  onChange,
  addLabel,
  disabled = false,
  keyPlaceholder = 'X-Custom-Header',
  valuePlaceholder = 'value',
  removeButtonTitle = 'Remove',
  removeButtonAriaLabel = 'Remove',
  allowEmpty = false,
  hideAddButton = false,
  emptyText,
}: HeaderInputListProps) {
  const currentEntries = allowEmpty ? entries : entries.length ? entries : [{ key: '', value: '' }];

  const updateEntry = (index: number, field: 'key' | 'value', value: string) => {
    const next = currentEntries.map((entry, idx) =>
      idx === index ? { ...entry, [field]: value } : entry
    );
    onChange(next);
  };

  const addEntry = () => {
    onChange([...currentEntries, { key: '', value: '' }]);
  };

  const removeEntry = (index: number) => {
    const next = currentEntries.filter((_, idx) => idx !== index);
    onChange(allowEmpty ? next : next.length ? next : [{ key: '', value: '' }]);
  };

  return (
    <div className="header-input-list">
      {allowEmpty && currentEntries.length === 0 && emptyText ? (
        <div className="hint">{emptyText}</div>
      ) : null}
      {currentEntries.map((entry, index) => (
        <Fragment key={index}>
          <div className="header-input-row">
            <input
              className="input"
              placeholder={keyPlaceholder}
              value={entry.key}
              onChange={(e) => updateEntry(index, 'key', e.target.value)}
              disabled={disabled}
            />
            <span className="header-separator">:</span>
            <input
              className="input"
              placeholder={valuePlaceholder}
              value={entry.value}
              onChange={(e) => updateEntry(index, 'value', e.target.value)}
              disabled={disabled}
            />
            <Button
              variant="ghost"
              size="xs"
              iconOnly
              onClick={() => removeEntry(index)}
              disabled={disabled || (!allowEmpty && currentEntries.length <= 1)}
              title={removeButtonTitle}
              aria-label={removeButtonAriaLabel}
            >
              <IconX size={14} />
            </Button>
          </div>
        </Fragment>
      ))}
      {!hideAddButton && (
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
