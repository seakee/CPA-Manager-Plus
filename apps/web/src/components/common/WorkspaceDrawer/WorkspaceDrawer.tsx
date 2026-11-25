import {
  Fragment,
  useId,
  useRef,
  type ComponentProps,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
  type RefObject,
} from 'react';
import { Button } from '@/components/ui/Button';
import { IconCheck, IconChevronLeft, IconCopy } from '@/components/ui/icons';
import styles from './WorkspaceDrawer.module.scss';

export type DrawerStatusTone = 'success' | 'danger' | 'warning' | 'info' | 'neutral';

export interface DrawerStatusPillProps {
  tone?: DrawerStatusTone;
  dot?: boolean;
  title?: string;
  testId?: string;
  className?: string;
  children: ReactNode;
}

const TONE_CLASS_MAP: Record<DrawerStatusTone, string> = {
  success: styles.statusPillSuccess,
  danger: styles.statusPillDanger,
  warning: styles.statusPillWarning,
  info: styles.statusPillInfo,
  neutral: styles.statusPillNeutral,
};

export function DrawerStatusPill({
  tone = 'neutral',
  dot = false,
  title,
  testId,
  className = '',
  children,
}: DrawerStatusPillProps) {
  const classes = [styles.statusPill, TONE_CLASS_MAP[tone], className].filter(Boolean).join(' ');
  return (
    <span className={classes} title={title} data-testid={testId}>
      {dot ? <span className={styles.statusPillDot} aria-hidden="true" /> : null}
      {children}
    </span>
  );
}

export interface DrawerCopyableMetaProps {
  children: ReactNode;
  onCopy: () => void | Promise<void>;
  copied?: boolean;
  title?: string;
  ariaLabel?: string;
  disabled?: boolean;
  testId?: string;
  className?: string;
}

export function DrawerCopyableMeta({
  children,
  onCopy,
  copied = false,
  title,
  ariaLabel,
  disabled = false,
  testId,
  className = '',
}: DrawerCopyableMetaProps) {
  const classes = [styles.copyMetaBtn, className].filter(Boolean).join(' ');
  return (
    <button
      type="button"
      className={classes}
      onClick={() => void onCopy()}
      title={title}
      aria-label={ariaLabel}
      disabled={disabled}
      data-testid={testId}
    >
      <span>{children}</span>
      {copied ? <IconCheck size={12} /> : <IconCopy size={12} />}
    </button>
  );
}

export interface DrawerIdentityHeaderProps {
  icon?: ReactNode;
  iconBoxed?: boolean;
  iconClassName?: string;
  iconDataAttributes?: Record<string, string | undefined>;
  title: ReactNode;
  titleText?: string;
  titleAs?: 'strong' | 'h3' | 'span';
  titleTrailing?: ReactNode;
  metaItems?: ReadonlyArray<ReactNode>;
  meta?: ReactNode;
  className?: string;
  testId?: string;
}

const isNonEmptyMeta = (item: ReactNode): boolean =>
  item !== null && item !== undefined && item !== false && item !== '';

export function DrawerIdentityHeader({
  icon,
  iconBoxed = false,
  iconClassName = '',
  iconDataAttributes,
  title,
  titleText,
  titleAs: TitleTag = 'strong',
  titleTrailing,
  metaItems,
  meta,
  className = '',
  testId,
}: DrawerIdentityHeaderProps) {
  const visibleMetaItems = metaItems ? metaItems.filter(isNonEmptyMeta) : [];
  const hasMeta = Boolean(meta) || visibleMetaItems.length > 0;
  const resolvedTitleAttr =
    titleText ?? (typeof title === 'string' && title.length > 0 ? title : undefined);
  const iconClasses = [
    styles.drawerTitleIcon,
    iconBoxed ? styles.drawerTitleIconBoxed : '',
    iconClassName,
  ]
    .filter(Boolean)
    .join(' ');

  return (
    <div
      className={[styles.drawerTitleIdentity, className].filter(Boolean).join(' ')}
      data-testid={testId}
    >
      {icon ? (
        <span className={iconClasses} aria-hidden="true" {...iconDataAttributes}>
          {icon}
        </span>
      ) : null}
      <div className={styles.drawerTitleStack}>
        {titleTrailing ? (
          <div className={styles.drawerTitleRow}>
            <TitleTag className={styles.drawerTitlePrimary} title={resolvedTitleAttr}>
              {title}
            </TitleTag>
            <div className={styles.drawerTitleTrailing}>{titleTrailing}</div>
          </div>
        ) : (
          <TitleTag className={styles.drawerTitlePrimary} title={resolvedTitleAttr}>
            {title}
          </TitleTag>
        )}
        {hasMeta ? (
          <div className={styles.drawerTitleMeta}>
            {visibleMetaItems.map((item, idx) => (
              <Fragment key={idx}>
                {idx > 0 ? (
                  <span className={styles.metaSeparator} aria-hidden="true">
                    ·
                  </span>
                ) : null}
                {item}
              </Fragment>
            ))}
            {meta}
          </div>
        ) : null}
      </div>
    </div>
  );
}

export interface DrawerTabItem<Id extends string = string> {
  id: Id;
  label: ReactNode;
  count?: number;
  dirty?: boolean;
  disabled?: boolean;
  testId?: string;
  tabDomId?: string;
  panelId?: string;
}

export interface DrawerTabBarProps<Id extends string = string> {
  items: ReadonlyArray<DrawerTabItem<Id>>;
  activeTab: Id;
  onChange: (tab: Id) => void;
  ariaLabel: string;
  idBase?: string;
  scrollContainerRef?: RefObject<HTMLElement | null>;
  className?: string;
}

export function DrawerTabBar<Id extends string = string>({
  items,
  activeTab,
  onChange,
  ariaLabel,
  idBase,
  scrollContainerRef,
  className = '',
}: DrawerTabBarProps<Id>) {
  const generatedId = useId();
  const basePrefix = idBase || `drawer-tabs-${generatedId}`;
  const tabRefs = useRef<Record<string, HTMLButtonElement | null>>({});

  const selectTab = (nextId: Id) => {
    if (nextId !== activeTab) {
      onChange(nextId);
      if (scrollContainerRef?.current) {
        scrollContainerRef.current.scrollTop = 0;
      }
    }
  };

  const handleKeyDown = (event: ReactKeyboardEvent<HTMLButtonElement>, index: number) => {
    const enabledItems = items.filter((item) => !item.disabled);
    if (enabledItems.length === 0) return;
    const currentItem = items[index];
    const currentEnabledIndex = enabledItems.findIndex((item) => item.id === currentItem.id);
    if (currentEnabledIndex === -1) return;

    let nextIndex = -1;
    if (event.key === 'ArrowRight') {
      nextIndex = (currentEnabledIndex + 1) % enabledItems.length;
    } else if (event.key === 'ArrowLeft') {
      nextIndex = (currentEnabledIndex - 1 + enabledItems.length) % enabledItems.length;
    } else if (event.key === 'Home') {
      nextIndex = 0;
    } else if (event.key === 'End') {
      nextIndex = enabledItems.length - 1;
    }

    if (nextIndex >= 0) {
      event.preventDefault();
      const target = enabledItems[nextIndex];
      selectTab(target.id);
      tabRefs.current[target.id]?.focus();
    }
  };

  return (
    <div
      className={[styles.drawerTabs, className].filter(Boolean).join(' ')}
      role="tablist"
      aria-label={ariaLabel}
    >
      {items.map((tab, index) => {
        const selected = activeTab === tab.id;
        const tabId = tab.tabDomId ?? `${basePrefix}-tab-${tab.id}`;
        const panelId = tab.panelId ?? `${basePrefix}-panel-${tab.id}`;
        return (
          <button
            key={tab.id}
            ref={(el) => {
              tabRefs.current[tab.id] = el;
            }}
            id={tabId}
            type="button"
            role="tab"
            aria-selected={selected}
            aria-controls={panelId}
            tabIndex={selected ? 0 : -1}
            disabled={tab.disabled}
            data-testid={tab.testId}
            className={selected ? styles.drawerTabActive : undefined}
            onClick={() => selectTab(tab.id)}
            onKeyDown={(e) => handleKeyDown(e, index)}
          >
            {tab.count === undefined && !tab.dirty ? (
              tab.label
            ) : (
              <>
                <span>{tab.label}</span>
                {tab.count !== undefined ? (
                  <span className={styles.tabCountBadge}>{tab.count}</span>
                ) : null}
                {tab.dirty ? <span className={styles.tabDirtyDot} aria-hidden="true" /> : null}
              </>
            )}
          </button>
        );
      })}
    </div>
  );
}

export interface DrawerBodyShellProps {
  topSlot?: ReactNode;
  tabs?: ReactNode;
  children: ReactNode;
  className?: string;
  testId?: string;
  ariaBusy?: boolean;
  dataAttributes?: Record<string, string | boolean | undefined>;
}

export function DrawerBodyShell({
  topSlot,
  tabs,
  children,
  className = '',
  testId,
  ariaBusy,
  dataAttributes,
}: DrawerBodyShellProps) {
  return (
    <div
      className={[styles.drawerBodyShell, className].filter(Boolean).join(' ')}
      data-testid={testId}
      aria-busy={ariaBusy}
      {...dataAttributes}
    >
      {topSlot ? <div className={styles.topBarSlot}>{topSlot}</div> : null}
      {tabs}
      {children}
    </div>
  );
}

export interface DrawerViewportProps {
  children: ReactNode;
  className?: string;
  testId?: string;
}

export function DrawerViewport({ children, className = '', testId }: DrawerViewportProps) {
  return (
    <div
      className={[styles.drawerViewport, className].filter(Boolean).join(' ')}
      data-testid={testId}
    >
      {children}
    </div>
  );
}

export interface DrawerTabPanelProps {
  id?: string;
  labelledBy?: string;
  asTabPanel?: boolean;
  hidden?: boolean;
  className?: string;
  testId?: string;
  children: ReactNode;
}

export function DrawerTabPanel({
  id,
  labelledBy,
  asTabPanel = Boolean(id || labelledBy),
  hidden,
  className = '',
  testId,
  children,
}: DrawerTabPanelProps) {
  return (
    <div
      id={id}
      role={asTabPanel ? 'tabpanel' : undefined}
      aria-labelledby={labelledBy}
      hidden={hidden}
      data-testid={testId}
      className={[styles.drawerTabPanel, className].filter(Boolean).join(' ')}
    >
      {children}
    </div>
  );
}

export interface DrawerSubViewHeaderProps {
  backLabel: ReactNode;
  onBack: () => void;
  backDisabled?: boolean;
  title: ReactNode;
  actions?: ReactNode;
}

export function DrawerSubViewHeader({
  backLabel,
  onBack,
  backDisabled = false,
  title,
  actions,
}: DrawerSubViewHeaderProps) {
  return (
    <div className={styles.sectionHeader}>
      <div className={styles.subViewHeaderLeft}>
        <Button
          variant="ghost"
          size="xs"
          className={styles.subViewBackBtn}
          disabled={backDisabled}
          onClick={onBack}
        >
          <IconChevronLeft size={14} />
          {backLabel}
        </Button>
        <span className={styles.subViewDivider} aria-hidden="true" />
        <div className={styles.sectionHeading}>
          <h4>{title}</h4>
        </div>
      </div>
      {actions ? <div className={styles.sectionMetaActions}>{actions}</div> : null}
    </div>
  );
}

export type DrawerNoticeTone = 'danger' | 'warning' | 'info' | 'neutral';

const NOTICE_TONE_CLASS: Record<DrawerNoticeTone, string> = {
  danger: styles.noticeDanger,
  warning: styles.noticeWarning,
  info: styles.noticeInfo,
  neutral: styles.noticeNeutral,
};

export interface DrawerNoticeProps {
  tone?: DrawerNoticeTone;
  icon?: ReactNode;
  title?: ReactNode;
  children?: ReactNode;
  action?: ReactNode;
  role?: 'alert' | 'status' | 'note';
  testId?: string;
  className?: string;
}

export function DrawerNotice({
  tone = 'info',
  icon,
  title,
  children,
  action,
  role = tone === 'danger' ? 'alert' : 'status',
  testId,
  className = '',
}: DrawerNoticeProps) {
  const classes = [styles.notice, NOTICE_TONE_CLASS[tone], className].filter(Boolean).join(' ');
  return (
    <div className={classes} role={role} data-testid={testId}>
      <div className={styles.noticeMain}>
        {icon ? (
          <span className={styles.noticeIcon} aria-hidden="true">
            {icon}
          </span>
        ) : null}
        <div className={styles.noticeText}>
          {title ? <strong className={styles.noticeTitle}>{title}</strong> : null}
          {typeof children === 'string' ? <p>{children}</p> : children}
        </div>
      </div>
      {action ? <div className={styles.noticeAction}>{action}</div> : null}
    </div>
  );
}

export interface DrawerScopePillProps {
  fallback?: boolean;
  className?: string;
  children: ReactNode;
}

export function DrawerScopePill({
  fallback = false,
  className = '',
  children,
}: DrawerScopePillProps) {
  const classes = [styles.scopePill, fallback ? styles.scopePillFallback : '', className]
    .filter(Boolean)
    .join(' ');
  return <span className={classes}>{children}</span>;
}

export interface DrawerSectionCardProps {
  id?: string;
  ariaLabel?: string;
  variant?: 'default' | 'highlight';
  as?: 'section' | 'div';
  icon?: ReactNode;
  title?: ReactNode;
  headingLevel?: 'h3' | 'h4';
  titleBadge?: ReactNode;
  metaActions?: ReactNode;
  hint?: ReactNode;
  testId?: string;
  dataAttributes?: Record<string, string | boolean | undefined>;
  className?: string;
  children: ReactNode;
}

export function DrawerSectionCard({
  id,
  ariaLabel,
  variant = 'default',
  as: Tag = 'section',
  icon,
  title,
  headingLevel: HeadingTag = 'h4',
  titleBadge,
  metaActions,
  hint,
  testId,
  dataAttributes,
  className = '',
  children,
}: DrawerSectionCardProps) {
  const isHighlight = variant === 'highlight';
  const cardClasses = [
    styles.sectionCard,
    isHighlight ? styles.sectionCardHighlight : '',
    className,
  ]
    .filter(Boolean)
    .join(' ');
  const hasHeader = Boolean(icon || title || titleBadge || metaActions);

  return (
    <Tag
      id={id}
      aria-label={ariaLabel}
      className={cardClasses}
      data-testid={testId}
      {...dataAttributes}
    >
      {hasHeader ? (
        <div className={styles.sectionHeader}>
          <div
            className={[
              styles.sectionHeading,
              isHighlight ? styles.sectionHeadingHighlight : '',
            ]
              .filter(Boolean)
              .join(' ')}
          >
            {icon ? (
              isHighlight ? (
                <span className={styles.sectionIconBox} aria-hidden="true">
                  {icon}
                </span>
              ) : (
                icon
              )
            ) : null}
            {title ? <HeadingTag>{title}</HeadingTag> : null}
            {titleBadge}
          </div>
          {metaActions ? <div className={styles.sectionMetaActions}>{metaActions}</div> : null}
        </div>
      ) : null}
      {hint ? <p className={styles.sectionHint}>{hint}</p> : null}
      {children}
    </Tag>
  );
}

export interface DrawerDefinitionListProps {
  className?: string;
  testId?: string;
  children: ReactNode;
}

export function DrawerDefinitionList({
  className = '',
  testId,
  children,
}: DrawerDefinitionListProps) {
  return (
    <dl className={[styles.dl, className].filter(Boolean).join(' ')} data-testid={testId}>
      {children}
    </dl>
  );
}

export interface DrawerDlRowProps {
  label: ReactNode;
  mono?: boolean;
  muted?: boolean;
  onCopy?: () => void | Promise<void>;
  copied?: boolean;
  copyAriaLabel?: string;
  copyTitle?: string;
  copyTestId?: string;
  valueTestId?: string;
  className?: string;
  children: ReactNode;
}

export function DrawerDlRow({
  label,
  mono = false,
  muted = false,
  onCopy,
  copied = false,
  copyAriaLabel,
  copyTitle,
  copyTestId,
  valueTestId,
  className = '',
  children,
}: DrawerDlRowProps) {
  const ddClasses = [
    styles.dd,
    mono ? styles.ddMono : '',
    muted ? styles.ddMuted : '',
  ]
    .filter(Boolean)
    .join(' ');

  return (
    <div className={[styles.dlRow, className].filter(Boolean).join(' ')}>
      <dt className={styles.dt}>{label}</dt>
      <dd className={ddClasses} data-testid={valueTestId}>
        {children}
        {onCopy ? (
          <button
            type="button"
            className={styles.inlineCopyBtn}
            onClick={() => void onCopy()}
            aria-label={copyAriaLabel}
            title={copyTitle}
            data-testid={copyTestId}
          >
            {copied ? <IconCheck size={13} /> : <IconCopy size={13} />}
          </button>
        ) : null}
      </dd>
    </div>
  );
}

export interface DrawerEmptyBoxProps {
  className?: string;
  testId?: string;
  dataAttributes?: Record<string, string | boolean | undefined>;
  children: ReactNode;
}

export function DrawerEmptyBox({
  className = '',
  testId,
  dataAttributes,
  children,
}: DrawerEmptyBoxProps) {
  return (
    <div
      className={[styles.drawerEmptyBox, className].filter(Boolean).join(' ')}
      data-testid={testId}
      {...dataAttributes}
    >
      {children}
    </div>
  );
}

export interface DrawerActionFooterProps {
  left?: ReactNode;
  right?: ReactNode;
  className?: string;
  testId?: string;
  leftTestId?: string;
  rightTestId?: string;
}

export function DrawerActionFooter({
  left,
  right,
  className = '',
  testId,
  leftTestId,
  rightTestId,
}: DrawerActionFooterProps) {
  return (
    <div className={[styles.drawerFooter, className].filter(Boolean).join(' ')} data-testid={testId}>
      {left ? (
        <div className={styles.drawerFooterLeft} data-testid={leftTestId}>
          {left}
        </div>
      ) : null}
      {right ? (
        <div className={styles.drawerFooterRight} data-testid={rightTestId}>
          {right}
        </div>
      ) : null}
    </div>
  );
}

export type DrawerDangerButtonProps = Omit<ComponentProps<typeof Button>, 'variant'>;

export function DrawerDangerButton({ className = '', size = 'sm', ...rest }: DrawerDangerButtonProps) {
  return (
    <Button
      variant="ghost"
      size={size}
      className={[styles.footerDangerBtn, className].filter(Boolean).join(' ')}
      {...rest}
    />
  );
}

export interface DrawerFormGridProps {
  className?: string;
  children: ReactNode;
}

export function DrawerFormGrid({ className = '', children }: DrawerFormGridProps) {
  return (
    <div className={[styles.formGridTwo, className].filter(Boolean).join(' ')}>{children}</div>
  );
}

export interface DrawerFormFullRowProps {
  className?: string;
  children: ReactNode;
}

export function DrawerFormFullRow({ className = '', children }: DrawerFormFullRowProps) {
  return (
    <div className={[styles.formFullRow, className].filter(Boolean).join(' ')}>{children}</div>
  );
}

export interface DrawerSettingListProps {
  className?: string;
  children: ReactNode;
}

export function DrawerSettingList({ className = '', children }: DrawerSettingListProps) {
  return (
    <div className={[styles.settingList, className].filter(Boolean).join(' ')}>{children}</div>
  );
}

export interface DrawerSettingRowProps {
  id?: string;
  title: ReactNode;
  description?: ReactNode;
  control: ReactNode;
  controlWide?: boolean;
  className?: string;
  testId?: string;
}

export function DrawerSettingRow({
  id,
  title,
  description,
  control,
  controlWide = false,
  className = '',
  testId,
}: DrawerSettingRowProps) {
  return (
    <div
      id={id}
      data-testid={testId}
      className={[styles.settingRow, className].filter(Boolean).join(' ')}
    >
      <div className={styles.settingRowMeta}>
        <strong className={styles.settingRowTitle}>{title}</strong>
        {description ? <p className={styles.settingRowDesc}>{description}</p> : null}
      </div>
      <div className={controlWide ? styles.settingControlBox : styles.settingControlAuto}>
        {control}
      </div>
    </div>
  );
}

export interface DrawerIndentGroupProps {
  className?: string;
  children: ReactNode;
}

export function DrawerIndentGroup({ className = '', children }: DrawerIndentGroupProps) {
  return (
    <div className={[styles.indentGroup, className].filter(Boolean).join(' ')}>{children}</div>
  );
}
