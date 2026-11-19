import {
  useCallback,
  useMemo,
  useRef,
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
  fullWidth = false,
  equalWidth = false,
  responsiveFullWidth = true,
  disabled = false,
  linkComponent: LinkComponent,
}: SegmentedTabsProps<Id>) {
  const itemRefs = useRef<Map<Id, HTMLElement | null>>(new Map());
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
    fullWidth ? styles.fullWidth : '',
    equalWidth ? styles.equalWidth : '',
    responsiveFullWidth ? styles.responsiveFullWidth : '',
    className,
  ]
    .filter(Boolean)
    .join(' ');

  return (
    <div className={rootClassName} role="tablist" aria-label={ariaLabel}>
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
          if (!isActive) {
            onChange?.(item.id);
          }
        };
        const commonProps = {
          className: itemClassName,
          role: 'tab' as const,
          id: itemId,
          'data-testid': item.testId,
          'aria-selected': isActive,
          tabIndex: isActive && !isDisabled ? 0 : -1,
          title: item.title,
          onClick: handleClick,
          onKeyDown: (event: KeyboardEvent<HTMLElement>) => handleKeyDown(event, item.id),
          children: item.label,
        };
        if (item.to && !isDisabled && LinkComponent) {
          return (
            <LinkComponent
              key={item.id}
              {...commonProps}
              ref={setItemRef}
              to={item.to}
            />
          );
        }

        return (
          <button
            key={item.id}
            {...commonProps}
            ref={setItemRef}
            type="button"
            disabled={isDisabled}
            aria-disabled={isDisabled || undefined}
          />
        );
      })}
    </div>
  );
}
