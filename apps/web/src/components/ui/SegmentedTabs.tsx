import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ComponentType,
  type KeyboardEvent,
  type MouseEvent,
  type ReactNode,
} from 'react';
import styles from './SegmentedTabs.module.scss';

export type SegmentedTabItem<Id extends string = string> = {
  id: Id;
  label: ReactNode;
  disabled?: boolean;
  title?: string;
  ariaLabel?: string;
  ariaControls?: string;
  to?: string;
  testId?: string;
};

export type SegmentedTabsLinkProps = {
  to: string;
  className: string;
  role: 'tab';
  id: string;
  'aria-selected': boolean;
  'aria-disabled'?: boolean;
  tabIndex: number;
  title?: string;
  onClick: (event: MouseEvent<HTMLElement>) => void;
  onKeyDown: (event: KeyboardEvent<HTMLElement>) => void;
  children: ReactNode;
  ref?: (node: HTMLElement | null) => void;
};

type SegmentedTabsProps<Id extends string> = {
  items: ReadonlyArray<SegmentedTabItem<Id>>;
  activeTab: Id;
  ariaLabel: string;
  onChange?: (tab: Id) => void;
  idBase?: string;
  className?: string;
  variant?: 'default' | 'joined';
  size?: 'md' | 'sm';
  roleMode?: 'tablist' | 'group';
  allowReselect?: boolean;
  fullWidth?: boolean;
  equalWidth?: boolean;
  responsiveFullWidth?: boolean;
  disabled?: boolean;
  linkComponent?: ComponentType<SegmentedTabsLinkProps>;
};

export function SegmentedTabs<Id extends string>({
  items,
  activeTab,
  ariaLabel,
  onChange,
  idBase = 'segmented-tabs',
  className = '',
  variant = 'default',
  size = 'md',
  roleMode = 'tablist',
  allowReselect = false,
  fullWidth = false,
  equalWidth = false,
  responsiveFullWidth = true,
  disabled = false,
  linkComponent: LinkComponent,
}: SegmentedTabsProps<Id>) {
  const rootRef = useRef<HTMLDivElement | null>(null);
  const itemRefs = useRef<Map<Id, HTMLElement | null>>(new Map());
  const [scrollFade, setScrollFade] = useState<{ left: boolean; right: boolean }>({
    left: false,
    right: false,
  });
  const updateScrollFade = useCallback(() => {
    const el = rootRef.current;
    if (!el) return;
    const maxScrollLeft = el.scrollWidth - el.clientWidth;
    const nextLeft = el.scrollLeft > 2;
    const nextRight = maxScrollLeft > 2 && el.scrollLeft < maxScrollLeft - 2;
    setScrollFade((prev) =>
      prev.left === nextLeft && prev.right === nextRight
        ? prev
        : { left: nextLeft, right: nextRight }
    );
  }, []);
  useEffect(() => {
    updateScrollFade();
    const el = rootRef.current;
    if (!el || typeof window === 'undefined') return;
    window.addEventListener('resize', updateScrollFade);
    const observer =
      typeof ResizeObserver !== 'undefined' ? new ResizeObserver(updateScrollFade) : null;
    observer?.observe(el);
    return () => {
      window.removeEventListener('resize', updateScrollFade);
      observer?.disconnect();
    };
  }, [items, activeTab, updateScrollFade]);
  const enabledItems = useMemo(
    () => items.filter((item) => !disabled && !item.disabled),
    [disabled, items]
  );

  const focusItem = useCallback((itemId: Id) => {
    itemRefs.current.get(itemId)?.focus();
  }, []);

  const handleKeyDown = useCallback(
    (event: KeyboardEvent<HTMLElement>, currentId: Id) => {
      if (enabledItems.length === 0) return;

      const currentIndex = enabledItems.findIndex((item) => item.id === currentId);
      if (currentIndex === -1) return;

      let nextIndex = currentIndex;
      switch (event.key) {
        case 'ArrowRight':
        case 'ArrowDown':
          nextIndex = (currentIndex + 1) % enabledItems.length;
          break;
        case 'ArrowLeft':
        case 'ArrowUp':
          nextIndex = (currentIndex - 1 + enabledItems.length) % enabledItems.length;
          break;
        case 'Home':
          nextIndex = 0;
          break;
        case 'End':
          nextIndex = enabledItems.length - 1;
          break;
        default:
          return;
      }

      event.preventDefault();
      const nextItem = enabledItems[nextIndex];
      focusItem(nextItem.id);
      if (!nextItem.to) {
        onChange?.(nextItem.id);
      }
    },
    [enabledItems, focusItem, onChange]
  );

  const rootClassName = [
    styles.root,
    variant === 'joined' ? styles.variantJoined : '',
    size === 'sm' ? styles.sizeSm : '',
    fullWidth ? styles.fullWidth : '',
    equalWidth ? styles.equalWidth : '',
    responsiveFullWidth ? styles.responsiveFullWidth : '',
    className,
  ]
    .filter(Boolean)
    .join(' ');

  const isTablist = roleMode === 'tablist';

  return (
    <div
      ref={rootRef}
      className={rootClassName}
      role={isTablist ? 'tablist' : 'group'}
      aria-label={ariaLabel}
      data-variant={variant}
      data-size={size}
      data-scroll-left={scrollFade.left ? 'true' : undefined}
      data-scroll-right={scrollFade.right ? 'true' : undefined}
      onScroll={updateScrollFade}
    >
      {items.map((item) => {
        const isActive = item.id === activeTab;
        const isDisabled = disabled || Boolean(item.disabled);
        const itemClassName = [
          styles.item,
          isActive ? styles.active : '',
          isDisabled ? styles.disabled : '',
        ]
          .filter(Boolean)
          .join(' ');
        const itemId = `${idBase}-${item.id}`;
        const setItemRef = (node: HTMLElement | null) => {
          itemRefs.current.set(item.id, node);
        };
        const handleClick = (event: MouseEvent<HTMLElement>) => {
          if (isDisabled) {
            event.preventDefault();
            return;
          }
          if (!isActive || allowReselect) {
            onChange?.(item.id);
          }
        };
        if (item.to && !isDisabled && LinkComponent) {
          return (
            <LinkComponent
              key={item.id}
              className={itemClassName}
              role="tab"
              id={itemId}
              aria-selected={isActive}
              tabIndex={isActive && !isDisabled ? 0 : -1}
              title={item.title}
              onClick={handleClick}
              onKeyDown={(event: KeyboardEvent<HTMLElement>) => handleKeyDown(event, item.id)}
              ref={setItemRef}
              to={item.to}
            >
              {item.label}
            </LinkComponent>
          );
        }

        return (
          <button
            key={item.id}
            ref={setItemRef}
            type="button"
            className={itemClassName}
            role={isTablist ? 'tab' : undefined}
            id={isTablist ? itemId : undefined}
            data-testid={item.testId}
            aria-label={item.ariaLabel}
            aria-selected={isTablist ? isActive : undefined}
            aria-controls={isTablist ? item.ariaControls : undefined}
            aria-pressed={!isTablist ? isActive : undefined}
            tabIndex={isTablist ? (isActive && !isDisabled ? 0 : -1) : undefined}
            title={item.title}
            disabled={isDisabled}
            aria-disabled={isDisabled || undefined}
            onClick={handleClick}
            onKeyDown={
              isTablist
                ? (event: KeyboardEvent<HTMLElement>) => handleKeyDown(event, item.id)
                : undefined
            }
          >
            {item.label}
          </button>
        );
      })}
    </div>
  );
}
