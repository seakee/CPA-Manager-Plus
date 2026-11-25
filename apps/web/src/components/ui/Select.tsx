import {
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
} from 'react';
import { createPortal } from 'react-dom';
import { IconChevronDown } from './icons';
import styles from './Select.module.scss';

export interface SelectOption {
  value: string;
  label: string;
}

interface SelectProps {
  value: string;
  options: ReadonlyArray<SelectOption>;
  onChange: (value: string) => void;
  placeholder?: string;
  className?: string;
  triggerClassName?: string;
  dropdownClassName?: string;
  disabled?: boolean;
  ariaLabel?: string;
  ariaLabelledBy?: string;
  ariaDescribedBy?: string;
  fullWidth?: boolean;
  id?: string;
  size?: 'sm' | 'md';
}

const VIEWPORT_MARGIN = 8;
const DROPDOWN_OFFSET = 6;
const DROPDOWN_MAX_HEIGHT = 240;
const DROPDOWN_MIN_HEIGHT = 96;
const DROPDOWN_Z_INDEX = 2010;
const SELECT_OPEN_EVENT = 'cpamp-select-open';

const clamp = (value: number, min: number, max: number) => Math.min(Math.max(value, min), max);

const resolveDropdownStyle = (element: HTMLElement): CSSProperties => {
  const rect = element.getBoundingClientRect();
  const viewportWidth = window.innerWidth;
  const viewportHeight = window.innerHeight;
  const maxAvailableWidth = Math.max(0, viewportWidth - VIEWPORT_MARGIN * 2);
  const minWidth = Math.min(rect.width, maxAvailableWidth);
  const left = clamp(
    rect.left,
    VIEWPORT_MARGIN,
    Math.max(VIEWPORT_MARGIN, viewportWidth - minWidth - VIEWPORT_MARGIN)
  );
  const maxWidth = Math.max(minWidth, viewportWidth - left - VIEWPORT_MARGIN);
  const spaceBelow = viewportHeight - rect.bottom - VIEWPORT_MARGIN - DROPDOWN_OFFSET;
  const spaceAbove = rect.top - VIEWPORT_MARGIN - DROPDOWN_OFFSET;
  const direction = spaceBelow >= DROPDOWN_MAX_HEIGHT || spaceBelow >= spaceAbove ? 'down' : 'up';
  const availableHeight = direction === 'down' ? spaceBelow : spaceAbove;
  const maxHeight = Math.max(DROPDOWN_MIN_HEIGHT, Math.min(DROPDOWN_MAX_HEIGHT, availableHeight));

  return direction === 'down'
    ? {
        position: 'fixed',
        top: rect.bottom + DROPDOWN_OFFSET,
        left,
        minWidth,
        width: 'max-content',
        maxWidth,
        maxHeight,
        zIndex: DROPDOWN_Z_INDEX,
      }
    : {
        position: 'fixed',
        bottom: viewportHeight - rect.top + DROPDOWN_OFFSET,
        left,
        minWidth,
        width: 'max-content',
        maxWidth,
        maxHeight,
        zIndex: DROPDOWN_Z_INDEX,
      };
};

export function Select({
  value,
  options,
  onChange,
  placeholder,
  className,
  triggerClassName,
  dropdownClassName,
  disabled = false,
  ariaLabel,
  ariaLabelledBy,
  ariaDescribedBy,
  fullWidth = true,
  id,
  size = 'md',
}: SelectProps) {
  const generatedId = useId();
  const selectId = id ?? generatedId;
  const listboxId = `${selectId}-listbox`;
  const [open, setOpen] = useState(false);
  const [highlightedIndex, setHighlightedIndex] = useState(-1);
  const wrapRef = useRef<HTMLDivElement | null>(null);
  const dropdownRef = useRef<HTMLDivElement | null>(null);
  const rafRef = useRef<number | null>(null);
  const [dropdownStyle, setDropdownStyle] = useState<CSSProperties | null>(null);
  const selectedIndex = useMemo(
    () => options.findIndex((option) => option.value === value),
    [options, value]
  );
  const isOpen = open && !disabled;

  const openSelect = useCallback(() => {
    if (disabled) return;
    if (typeof document !== 'undefined') {
      document.dispatchEvent(new CustomEvent(SELECT_OPEN_EVENT, { detail: selectId }));
    }
    setOpen(true);
    setHighlightedIndex(selectedIndex >= 0 ? selectedIndex : 0);
  }, [disabled, selectId, selectedIndex]);

  const toggleOpen = useCallback(() => {
    if (disabled) return;
    if (isOpen) {
      setOpen(false);
      return;
    }
    openSelect();
  }, [disabled, isOpen, openSelect]);

  useEffect(() => {
    if (!open || disabled) return;
    const handleClickOutside = (event: MouseEvent) => {
      const target = event.target as Node;
      if (wrapRef.current?.contains(target) || dropdownRef.current?.contains(target)) return;
      setOpen(false);
    };
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, [disabled, open]);

  useEffect(() => {
    if (typeof document === 'undefined') return;
    const handlePeerOpen = (event: Event) => {
      const nextSelectId = (event as CustomEvent<string>).detail;
      if (nextSelectId !== selectId) {
        setOpen(false);
      }
    };
    document.addEventListener(SELECT_OPEN_EVENT, handlePeerOpen);
    return () => document.removeEventListener(SELECT_OPEN_EVENT, handlePeerOpen);
  }, [selectId]);

  const updateDropdownStyle = useCallback(() => {
    if (!wrapRef.current) return;
    setDropdownStyle(resolveDropdownStyle(wrapRef.current));
  }, []);

  const scheduleDropdownStyleUpdate = useCallback(() => {
    if (typeof window === 'undefined') return;
    if (rafRef.current !== null) {
      window.cancelAnimationFrame(rafRef.current);
    }
    rafRef.current = window.requestAnimationFrame(() => {
      rafRef.current = null;
      updateDropdownStyle();
    });
  }, [updateDropdownStyle]);

  useLayoutEffect(() => {
    if (!isOpen) {
      if (rafRef.current !== null && typeof window !== 'undefined') {
        window.cancelAnimationFrame(rafRef.current);
        rafRef.current = null;
      }
      return;
    }

    updateDropdownStyle();

    const handleViewportChange = () => {
      scheduleDropdownStyleUpdate();
    };

    const resizeObserver =
      typeof ResizeObserver !== 'undefined' && wrapRef.current
        ? new ResizeObserver(() => {
            scheduleDropdownStyleUpdate();
          })
        : null;

    if (resizeObserver && wrapRef.current) {
      resizeObserver.observe(wrapRef.current);
    }

    window.addEventListener('resize', handleViewportChange);
    window.addEventListener('scroll', handleViewportChange, true);

    return () => {
      window.removeEventListener('resize', handleViewportChange);
      window.removeEventListener('scroll', handleViewportChange, true);
      resizeObserver?.disconnect();
      if (rafRef.current !== null) {
        window.cancelAnimationFrame(rafRef.current);
        rafRef.current = null;
      }
    };
  }, [isOpen, scheduleDropdownStyleUpdate, updateDropdownStyle]);

  const resolvedHighlightedIndex =
    highlightedIndex >= 0
      ? highlightedIndex
      : selectedIndex >= 0
        ? selectedIndex
        : options.length > 0
          ? 0
          : -1;
  const selected = selectedIndex >= 0 ? options[selectedIndex] : undefined;
  const displayText = selected?.label ?? placeholder ?? '';
  const isPlaceholder = !selected && placeholder;

  const commitSelection = useCallback(
    (nextIndex: number) => {
      const nextOption = options[nextIndex];
      if (!nextOption) return;
      onChange(nextOption.value);
      setOpen(false);
      setHighlightedIndex(nextIndex);
    },
    [onChange, options]
  );

  const moveHighlight = useCallback(
    (direction: 1 | -1) => {
      if (options.length === 0) return;
      const nextIndex = (resolvedHighlightedIndex + direction + options.length) % options.length;
      setHighlightedIndex(nextIndex);
    },
    [options.length, resolvedHighlightedIndex]
  );

  const handleKeyDown = useCallback(
    (event: React.KeyboardEvent<HTMLButtonElement>) => {
      if (disabled) return;

      switch (event.key) {
        case 'ArrowDown':
          event.preventDefault();
          if (!isOpen) {
            openSelect();
            return;
          }
          moveHighlight(1);
          return;
        case 'ArrowUp':
          event.preventDefault();
          if (!isOpen) {
            openSelect();
            return;
          }
          moveHighlight(-1);
          return;
        case 'Home':
          if (!isOpen || options.length === 0) return;
          event.preventDefault();
          setHighlightedIndex(0);
          return;
        case 'End':
          if (!isOpen || options.length === 0) return;
          event.preventDefault();
          setHighlightedIndex(options.length - 1);
          return;
        case 'Enter':
        case ' ': {
          event.preventDefault();
          if (!isOpen) {
            openSelect();
            return;
          }
          if (resolvedHighlightedIndex >= 0) {
            commitSelection(resolvedHighlightedIndex);
          }
          return;
        }
        case 'Escape':
          if (!isOpen) return;
          event.preventDefault();
          setOpen(false);
          return;
        case 'Tab':
          if (isOpen) setOpen(false);
          return;
        default:
          return;
      }
    },
    [
      commitSelection,
      disabled,
      isOpen,
      moveHighlight,
      openSelect,
      options.length,
      resolvedHighlightedIndex,
    ]
  );

  useEffect(() => {
    if (!isOpen || resolvedHighlightedIndex < 0) return;
    const highlightedOption = document.getElementById(
      `${selectId}-option-${resolvedHighlightedIndex}`
    );
    highlightedOption?.scrollIntoView({ block: 'nearest' });
  }, [isOpen, resolvedHighlightedIndex, selectId]);

  const dropdown =
    isOpen && dropdownStyle ? (
      <div
        ref={dropdownRef}
        className={[styles.dropdown, dropdownClassName].filter(Boolean).join(' ')}
        id={listboxId}
        role="listbox"
        aria-label={ariaLabel}
        style={dropdownStyle}
      >
        {options.map((opt, index) => {
          const active = opt.value === value;
          const highlighted = index === resolvedHighlightedIndex;
          return (
            <button
              key={opt.value}
              id={`${selectId}-option-${index}`}
              type="button"
              role="option"
              aria-selected={active}
              className={`${styles.option} ${active ? styles.optionActive : ''} ${highlighted ? styles.optionHighlighted : ''}`.trim()}
              onMouseEnter={() => setHighlightedIndex(index)}
              onKeyDown={handleKeyDown}
              onClick={() => commitSelection(index)}
            >
              {opt.label}
            </button>
          );
        })}
      </div>
    ) : null;

  return (
    <>
      <div
        className={`${styles.wrap} ${fullWidth ? styles.wrapFullWidth : ''} ${className ?? ''}`}
        ref={wrapRef}
      >
        <button
          id={selectId}
          type="button"
          className={[
            styles.trigger,
            size === 'sm' ? styles.triggerSm : '',
            triggerClassName,
          ]
            .filter(Boolean)
            .join(' ')}
          onClick={disabled ? undefined : toggleOpen}
          onKeyDown={handleKeyDown}
          aria-haspopup="listbox"
          aria-expanded={isOpen}
          aria-controls={isOpen ? listboxId : undefined}
          aria-activedescendant={
            isOpen && resolvedHighlightedIndex >= 0
              ? `${selectId}-option-${resolvedHighlightedIndex}`
              : undefined
          }
          aria-label={ariaLabel}
          aria-labelledby={ariaLabelledBy}
          aria-describedby={ariaDescribedBy}
          disabled={disabled}
        >
          <span className={`${styles.triggerText} ${isPlaceholder ? styles.placeholder : ''}`}>
            {displayText}
          </span>
          <span className={styles.triggerIcon} aria-hidden="true">
            <IconChevronDown size={14} />
          </span>
        </button>
      </div>
      {dropdown &&
        (typeof document === 'undefined' ? dropdown : createPortal(dropdown, document.body))}
    </>
  );
}
