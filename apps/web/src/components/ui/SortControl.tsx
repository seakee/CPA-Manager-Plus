import { useEffect, useRef, useState, type KeyboardEvent } from 'react';
import { IconArrowDownWideNarrow, IconArrowUpNarrowWide } from './icons';
import styles from './SortControl.module.scss';

export type SortDirection = 'asc' | 'desc';

export type SortControlOption<Key extends string = string> = {
  value: Key;
  label: string;
};

type SortControlProps<Key extends string> = {
  options: ReadonlyArray<SortControlOption<Key>>;
  value: Key;
  direction: SortDirection;
  onChangeValue: (nextValue: Key) => void;
  onToggleDirection: () => void;
  ariaLabel: string;
  directionLabel: string;
  title?: string;
  directionDisabled?: boolean;
  fullWidth?: boolean;
  dropdownAlign?: 'left' | 'right';
  dropdownDirection?: 'down' | 'up';
  className?: string;
  testId?: string;
};

export function SortControl<Key extends string>({
  options,
  value,
  direction,
  onChangeValue,
  onToggleDirection,
  ariaLabel,
  directionLabel,
  title,
  directionDisabled = false,
  fullWidth = false,
  dropdownAlign = 'left',
  dropdownDirection = 'down',
  className = '',
  testId,
}: SortControlProps<Key>) {
  const [isOpen, setIsOpen] = useState(false);
  const [highlightedIndex, setHighlightedIndex] = useState(-1);
  const rootRef = useRef<HTMLDivElement | null>(null);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const optionRefs = useRef<Map<Key, HTMLButtonElement | null>>(new Map());

  const selectedIndex = options.findIndex((option) => option.value === value);
  const selectedOption = options[selectedIndex >= 0 ? selectedIndex : 0];
  const selectedLabel = selectedOption?.label ?? '';

  useEffect(() => {
    if (!isOpen || typeof document === 'undefined') return;

    const handleClickOutside = (event: MouseEvent) => {
      const target = event.target as Node;
      if (!rootRef.current?.contains(target)) {
        setIsOpen(false);
      }
    };

    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, [isOpen]);

  useEffect(() => {
    if (!isOpen || highlightedIndex < 0) return;
    const highlightedOption = options[highlightedIndex];
    if (!highlightedOption) return;
    optionRefs.current.get(highlightedOption.value)?.focus();
  }, [highlightedIndex, isOpen, options]);

  const openDropdown = () => {
    if (options.length === 0) return;
    setHighlightedIndex(selectedIndex >= 0 ? selectedIndex : 0);
    setIsOpen(true);
  };

  const toggleDropdown = () => {
    if (isOpen) {
      setIsOpen(false);
      return;
    }
    openDropdown();
  };

  const closeDropdown = () => {
    setIsOpen(false);
    triggerRef.current?.focus();
  };

  const moveHighlight = (nextIndex: number) => {
    if (options.length === 0) return;
    const normalizedIndex = (nextIndex + options.length) % options.length;
    setHighlightedIndex(normalizedIndex);
  };

  const commitOption = (optionValue: Key) => {
    onChangeValue(optionValue);
    setIsOpen(false);
    triggerRef.current?.focus();
  };

  const handleTriggerKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    switch (event.key) {
      case 'Enter':
      case ' ':
        event.preventDefault();
        toggleDropdown();
        return;
      case 'ArrowDown':
        event.preventDefault();
        if (!isOpen) {
          openDropdown();
          return;
        }
        moveHighlight(highlightedIndex + 1);
        return;
      case 'ArrowUp':
        event.preventDefault();
        if (!isOpen) {
          openDropdown();
          return;
        }
        moveHighlight(highlightedIndex - 1);
        return;
      case 'Escape':
        if (!isOpen) return;
        event.preventDefault();
        closeDropdown();
        return;
      default:
        return;
    }
  };

  const handleOptionKeyDown = (event: KeyboardEvent<HTMLButtonElement>, optionIndex: number) => {
    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault();
        moveHighlight(optionIndex + 1);
        return;
      case 'ArrowUp':
        event.preventDefault();
        moveHighlight(optionIndex - 1);
        return;
      case 'Home':
        event.preventDefault();
        moveHighlight(0);
        return;
      case 'End':
        event.preventDefault();
        moveHighlight(options.length - 1);
        return;
      case 'Escape':
        event.preventDefault();
        closeDropdown();
        return;
      default:
        return;
    }
  };

  const rootClassName = [styles.root, fullWidth ? styles.fullWidth : '', className]
    .filter(Boolean)
    .join(' ');
  const dropdownClassName = [
    styles.dropdownList,
    dropdownAlign === 'right' ? styles.dropdownRight : '',
    dropdownDirection === 'up' ? styles.dropdownUp : '',
  ]
    .filter(Boolean)
    .join(' ');

  return (
    <div className={rootClassName} ref={rootRef} data-testid={testId}>
      <button
        ref={triggerRef}
        type="button"
        className={styles.trigger}
        onClick={toggleDropdown}
        onKeyDown={handleTriggerKeyDown}
        title={title ?? ariaLabel}
        aria-label={ariaLabel}
        aria-haspopup="listbox"
        aria-expanded={isOpen}
      >
        <span className={styles.label}>{selectedLabel}</span>
      </button>
      <button
        type="button"
        className={styles.directionButton}
        onClick={onToggleDirection}
        disabled={directionDisabled}
        title={directionLabel}
        aria-label={directionLabel}
      >
        <span className={styles.directionIcon} aria-hidden="true">
          {direction === 'asc' ? (
            <IconArrowUpNarrowWide size={14} />
          ) : (
            <IconArrowDownWideNarrow size={14} />
          )}
        </span>
      </button>
      {isOpen ? (
        <div className={dropdownClassName} role="listbox">
          {options.map((option, optionIndex) => {
            const isSelected = option.value === value;
            const isHighlighted = optionIndex === highlightedIndex;
            const optionClassName = [
              styles.dropdownItem,
              isSelected ? styles.dropdownItemSelected : '',
              isHighlighted ? styles.dropdownItemHighlighted : '',
            ]
              .filter(Boolean)
              .join(' ');
            return (
              <button
                key={option.value}
                ref={(node) => {
                  optionRefs.current.set(option.value, node);
                }}
                type="button"
                role="option"
                aria-selected={isSelected}
                className={optionClassName}
                onClick={() => commitOption(option.value)}
                onKeyDown={(event) => handleOptionKeyDown(event, optionIndex)}
                onMouseEnter={() => setHighlightedIndex(optionIndex)}
              >
                {option.label}
              </button>
            );
          })}
        </div>
      ) : null}
    </div>
  );
}
