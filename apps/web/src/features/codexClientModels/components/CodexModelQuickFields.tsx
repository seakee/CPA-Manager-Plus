import { useCallback, useMemo, useState, type ComponentType, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Select } from '@/components/ui/Select';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import {
  IconChevronRight,
  IconFileText,
  IconScrollText,
  IconShieldCheck,
  IconSlidersHorizontal,
  IconTimer,
  type IconProps,
} from '@/components/ui/icons';
import {
  EMPTY_QUICK_FIELD_VIEW,
  QUICK_FIELD_LINKS,
  QUICK_FIELD_SECTIONS,
  buildQuickFieldPreview,
  buildQuickFieldViews,
  buildQuickGroupChildren,
  buildQuickLinkView,
  findQuickTreeNode,
  formatQuickFieldText,
  multilineRows,
  quickFieldKey,
  quickFieldViewOf,
  type QuickFieldDescriptor,
  type QuickFieldKind,
  type QuickFieldView,
  type QuickLinkDescriptor,
  type QuickLinkView,
  type QuickSectionDescriptor,
  type QuickSectionId,
} from '../model/codexModelQuickFields';
import {
  buildOverrideTree,
  setOverrideValue,
  type OverridePath,
  type OverrideTreeNode,
} from '../model/codexClientModelsTree';
import { canInheritPath, type FieldInheritBinding } from '../model/codexClientModelsInherit';
import {
  formatPlainText,
  parseJsonArrayText,
  parseNumberText,
  parsePlainText,
  parseStringListText,
} from '../model/draftValues';
import { FieldSourceControl } from './FieldSourceControl';
import { DraftField } from './ValueDraftField';
import styles from './CodexModelQuickFields.module.scss';

const SECTION_ICONS: Record<QuickSectionId, ComponentType<IconProps>> = {
  basic: IconFileText,
  context: IconTimer,
  capability: IconShieldCheck,
  tools: IconSlidersHorizontal,
  prompt: IconScrollText,
};

type Translate = ReturnType<typeof useTranslation>['t'];

/** 字段标签与控件之间的 aria 关联用的 DOM id。 */
const quickFieldDomId = (key: string): string => `codex-model-quick-${key.replace(/\./g, '-')}`;

/** 常用字段的标签：没有专门写过的键沿用原始键名，目录里新增字段也能直接显示。 */
const quickFieldLabel = (t: Translate, path: OverridePath): string =>
  t(`codex_client_models.quick.fields.${quickFieldKey(path)}.label`, {
    defaultValue: String(path[path.length - 1] ?? ''),
  });

const quickFieldHint = (t: Translate, path: OverridePath): string =>
  t(`codex_client_models.quick.fields.${quickFieldKey(path)}.hint`, { defaultValue: '' });

/** 长文本与分组默认收起，展开后才渲染内容。 */
const hasCollapsedBody = (kind: QuickFieldKind): boolean =>
  kind === 'multiline' || kind === 'group';

/** 单条路径的来源控制；联动组会按同样方式作用到组里的每条路径。 */
function sourceControlFor(
  inherit: FieldInheritBinding,
  options: { path: OverridePath; view: QuickFieldView; disabled: boolean }
) {
  const { path, view, disabled } = options;
  return (
    <FieldSourceControl
      state={view.state}
      source={view.source}
      declared={view.declared}
      sources={inherit.sources}
      inheritable={canInheritPath(path)}
      issue={inherit.issueOf(path)}
      disabled={disabled}
      onClear={() => inherit.clear(path)}
      onInherit={(slug) => inherit.inherit(path, slug)}
      onRemove={() => inherit.remove(path)}
    />
  );
}

export interface CodexModelQuickFieldsProps {
  /** 生效条目，作为「继承」状态的参照值；新增条目时传默认模板。 */
  effective: unknown;
  /** 当前覆写补丁；null 表示整个条目被删除。 */
  patch: Record<string, unknown> | null;
  /** 枚举字段的候选值，来自现有目录。 */
  fieldOptions?: ReadonlyMap<string, ReadonlyArray<string>>;
  /** 逐字段的继承来源与操作。 */
  sourceBinding: FieldInheritBinding;
  onChange: (patch: Record<string, unknown>) => void;
  disabled?: boolean;
}

/**
 * 折叠状态下的取值预览：只展示前几行，点击后换成真正的编辑框。
 * 多行文本按真实换行渲染，不出现转义符号。
 */
function QuickFieldPreview({
  kind,
  value,
  onExpand,
}: {
  kind: QuickFieldKind;
  value: unknown;
  onExpand: () => void;
}) {
  const { t } = useTranslation();
  const preview = useMemo(() => buildQuickFieldPreview(kind, value), [kind, value]);

  if (preview.empty) {
    return (
      <div className={styles.previewEmpty}>
        {t('codex_client_models.quick_field_preview_empty')}
      </div>
    );
  }

  return (
    <button type="button" className={styles.preview} onClick={onExpand}>
      <span className={styles.previewText}>{preview.text}</span>
      <span className={styles.previewMeta}>
        {preview.hiddenLines > 0
          ? t('codex_client_models.quick_field_preview_expand', { value: preview.hiddenLines })
          : t('codex_client_models.quick_field_preview_open')}
      </span>
    </button>
  );
}

interface QuickFieldShellProps {
  /** i18n 与 DOM id 用的字段键。 */
  fieldKey: string;
  kind: QuickFieldKind;
  label: string;
  hint: string;
  value: unknown;
  /** 字段来源控制，由调用方按自己的路径组装。 */
  sourceControl: ReactNode;
  /** 标题后面的补充信息，例如分组里可编辑的子项数量。 */
  meta?: string;
  wide?: boolean;
  defaultOpen?: boolean;
  /** 分组内部的字段：不再套一层卡片，只保留行本身。 */
  nested?: boolean;
  renderControl: (labelId: string) => ReactNode;
}

/** 单个字段的外壳：标题、来源控制，以及折叠 / 展开的切换。 */
function QuickFieldShell({
  fieldKey,
  kind,
  label,
  hint,
  value,
  sourceControl,
  meta,
  wide = false,
  defaultOpen = false,
  nested = false,
  renderControl,
}: QuickFieldShellProps) {
  const labelId = quickFieldDomId(fieldKey);
  const collapsible = hasCollapsedBody(kind);
  // 分组没有自己的取值，折叠时只留标题，展开后由子项说明内容。
  const previewable = kind !== 'group';
  const [expanded, setExpanded] = useState(defaultOpen);
  const open = !collapsible || expanded;
  const hintFirst = kind === 'boolean' || kind === 'group';

  return (
    <div
      className={styles.field}
      data-kind={kind}
      data-wide={wide ? 'true' : undefined}
      data-nested={nested ? 'true' : undefined}
    >
      <div className={styles.fieldHead}>
        {collapsible ? (
          <button
            type="button"
            className={styles.fieldToggle}
            aria-expanded={open}
            onClick={() => setExpanded((previous) => !previous)}
          >
            <IconChevronRight
              size={12}
              className={open ? styles.fieldChevronOpen : styles.fieldChevron}
            />
            <span className={styles.fieldLabel} id={labelId}>
              {label}
            </span>
            {meta ? <span className={styles.fieldMeta}>{meta}</span> : null}
          </button>
        ) : (
          <span className={styles.fieldLabel} id={labelId}>
            {label}
          </span>
        )}
        <span className={styles.fieldSpacer} />
        {sourceControl}
      </div>

      {open ? (
        hintFirst ? (
          <div className={kind === 'boolean' ? styles.booleanBody : styles.groupLead}>
            {hint ? <p className={styles.fieldHint}>{hint}</p> : null}
            {renderControl(labelId)}
          </div>
        ) : (
          <>
            {renderControl(labelId)}
            {hint ? <p className={styles.fieldHint}>{hint}</p> : null}
          </>
        )
      ) : (
        <>
          {previewable ? (
            <QuickFieldPreview kind={kind} value={value} onExpand={() => setExpanded(true)} />
          ) : null}
          {hint ? <p className={styles.fieldHint}>{hint}</p> : null}
        </>
      )}
    </div>
  );
}

interface QuickFieldRowProps {
  field: QuickFieldDescriptor;
  view: QuickFieldView;
  options: ReadonlyArray<string>;
  inherit: FieldInheritBinding;
  nested?: boolean;
  disabled: boolean;
  onSetValue: (value: unknown) => void;
}

/** 叶子字段：按控件类型渲染，取值写回它自己的路径。 */
function QuickFieldRow({
  field,
  view,
  options,
  inherit,
  nested,
  disabled,
  onSetValue,
}: QuickFieldRowProps) {
  const { t } = useTranslation();
  const fieldKey = quickFieldKey(field.path);
  const label = quickFieldLabel(t, field.path);
  const hint = quickFieldHint(t, field.path);

  const renderControl = (labelId: string) => {
    switch (field.kind) {
      case 'boolean':
        return (
          <div className={styles.toggle}>
            <ToggleSwitch
              checked={view.value === true}
              onChange={onSetValue}
              ariaLabel={label}
              disabled={disabled}
            />
          </div>
        );
      case 'select': {
        const value = typeof view.value === 'string' ? view.value : '';
        const choices = [...options];
        if (value && !choices.includes(value)) choices.push(value);
        if (choices.length === 0) {
          return (
            <DraftField
              value={view.value}
              format={formatPlainText}
              parse={parsePlainText}
              onCommit={onSetValue}
              ariaLabel={label}
              disabled={disabled}
              singleLine
            />
          );
        }
        return (
          <Select
            value={value}
            options={choices.map((choice) => ({ value: choice, label: choice }))}
            onChange={onSetValue}
            placeholder={t('codex_client_models.quick_select_placeholder')}
            ariaLabelledBy={labelId}
            disabled={disabled}
          />
        );
      }
      case 'number':
        return (
          <DraftField
            value={view.value}
            format={formatPlainText}
            parse={parseNumberText}
            onCommit={onSetValue}
            ariaLabel={label}
            disabled={disabled}
            singleLine
            monospace
            placeholder="0"
          />
        );
      case 'multiline':
        return (
          <DraftField
            value={view.value}
            format={(value) => formatQuickFieldText('multiline', value)}
            parse={parsePlainText}
            onCommit={onSetValue}
            ariaLabel={label}
            disabled={disabled}
            rows={multilineRows(view.value)}
            resizable={false}
          />
        );
      case 'string-list':
        return (
          <DraftField
            value={view.value}
            format={(value) => formatQuickFieldText('string-list', value)}
            parse={parseStringListText}
            onCommit={onSetValue}
            ariaLabel={label}
            disabled={disabled}
            rows={3}
            monospace
            resizable={false}
          />
        );
      case 'array':
        return (
          <DraftField
            value={view.value}
            format={(value) => formatQuickFieldText('array', value)}
            parse={parseJsonArrayText}
            onCommit={onSetValue}
            ariaLabel={label}
            disabled={disabled}
            rows={4}
            monospace
            resizable={false}
          />
        );
      default:
        return (
          <DraftField
            value={view.value}
            format={formatPlainText}
            parse={parsePlainText}
            onCommit={onSetValue}
            ariaLabel={label}
            disabled={disabled}
            singleLine
            placeholder={view.value === null ? 'null' : undefined}
          />
        );
    }
  };

  return (
    <QuickFieldShell
      fieldKey={fieldKey}
      kind={field.kind}
      label={label}
      hint={hint}
      value={view.value}
      sourceControl={sourceControlFor(inherit, { path: field.path, view, disabled })}
      wide={field.wide}
      defaultOpen={field.defaultOpen}
      nested={nested}
      renderControl={renderControl}
    />
  );
}

interface QuickFieldNodeProps {
  node: OverrideTreeNode;
  field: QuickFieldDescriptor;
  hiddenPaths: ReadonlySet<string>;
  options: ReadonlyMap<string, ReadonlyArray<string>> | undefined;
  inherit: FieldInheritBinding;
  nested?: boolean;
  disabled: boolean;
  setValueAtPath: (path: OverridePath, value: unknown) => void;
}

/**
 * 字段树里的一个节点：对象继续分组展开，其余按控件类型渲染。
 * 分组字段的子项按目录内容生成，因此 model_messages 这类深层配置可以多级展开。
 */
function QuickFieldNode({
  node,
  field,
  hiddenPaths,
  options,
  inherit,
  nested = false,
  disabled,
  setValueAtPath,
}: QuickFieldNodeProps) {
  const { t } = useTranslation();
  const view = quickFieldViewOf(node);

  if (field.kind !== 'group') {
    return (
      <QuickFieldRow
        field={field}
        view={view}
        options={options?.get(quickFieldKey(field.path)) ?? []}
        inherit={inherit}
        nested={nested}
        disabled={disabled}
        onSetValue={(value) => setValueAtPath(field.path, value)}
      />
    );
  }

  const children = buildQuickGroupChildren(node, hiddenPaths);
  return (
    <QuickFieldShell
      fieldKey={quickFieldKey(field.path)}
      kind="group"
      label={quickFieldLabel(t, field.path)}
      hint={quickFieldHint(t, field.path)}
      value={view.value}
      sourceControl={sourceControlFor(inherit, { path: field.path, view, disabled })}
      meta={t('codex_client_models.quick_group_fields', { value: children.length })}
      wide={field.wide}
      defaultOpen={field.defaultOpen}
      nested={nested}
      renderControl={() => (
        <div className={styles.groupBody}>
          {children.map((child) => (
            <QuickFieldNode
              key={child.node.id}
              node={child.node}
              field={child.field}
              hiddenPaths={hiddenPaths}
              options={options}
              inherit={inherit}
              nested
              disabled={disabled}
              setValueAtPath={setValueAtPath}
            />
          ))}
        </div>
      )}
    />
  );
}

interface QuickLinkRowProps {
  link: QuickLinkDescriptor;
  view: QuickLinkView;
  inherit: FieldInheritBinding;
  disabled: boolean;
  onSetValue: (value: unknown) => void;
}

/** 联动字段合并成的单个输入框：写入时同步到参与联动的每条路径。 */
function QuickLinkRow({ link, view, inherit, disabled, onSetValue }: QuickLinkRowProps) {
  const { t } = useTranslation();
  const label = quickFieldLabel(t, link.paths[0]);
  // 联动的两条路径要一起改来源，否则「两个字段内容一致」的前提立刻被破坏。
  const eachPath = (action: (path: OverridePath) => void) => () =>
    link.paths.forEach((path) => action(path));

  return (
    <QuickFieldShell
      fieldKey={`link-${link.id}`}
      kind="multiline"
      label={label}
      hint={t(`codex_client_models.quick_links.${link.id}.hint`)}
      value={view.value}
      sourceControl={
        <FieldSourceControl
          state={view.state}
          source={view.source}
          declared={view.declared}
          mixed={view.mixed}
          sources={inherit.sources}
          inheritable={link.paths.every((path) => canInheritPath(path))}
          disabled={disabled}
          onClear={eachPath(inherit.clear)}
          onInherit={(slug) => link.paths.forEach((path) => inherit.inherit(path, slug))}
          onRemove={eachPath(inherit.remove)}
        />
      }
      wide
      renderControl={() => (
        <DraftField
          value={view.value}
          format={(value) => formatQuickFieldText('multiline', value)}
          parse={parsePlainText}
          onCommit={onSetValue}
          ariaLabel={t(`codex_client_models.quick_links.${link.id}.label`)}
          disabled={disabled}
          rows={multilineRows(view.value)}
          resizable={false}
        />
      )}
    />
  );
}

/** 联动字段的开关：决定这一组是合并成一个输入框还是逐个编辑。 */
function QuickLinkToggle({
  link,
  enabled,
  disabled,
  onToggle,
}: {
  link: QuickLinkDescriptor;
  enabled: boolean;
  disabled: boolean;
  onToggle: () => void;
}) {
  const { t } = useTranslation();
  const label = t(`codex_client_models.quick_links.${link.id}.label`);

  return (
    <div className={styles.linkRow}>
      <div className={styles.linkText}>
        <span className={styles.linkLabel}>{label}</span>
        <span className={styles.linkHint}>
          {t(`codex_client_models.quick_links.${link.id}.hint`)}
        </span>
      </div>
      <ToggleSwitch checked={enabled} onChange={onToggle} ariaLabel={label} disabled={disabled} />
    </div>
  );
}

/** 常用字段的配置面板：分组、本地化标签，直接编辑即写入覆写。 */
export function CodexModelQuickFields({
  effective,
  patch,
  fieldOptions,
  sourceBinding,
  onChange,
  disabled = false,
}: CodexModelQuickFieldsProps) {
  const { t } = useTranslation();
  // 字段树只构建一次：分组字段的子项与扁平字段的状态都从同一棵树上读。
  const { directives } = sourceBinding;
  const nodes = useMemo(
    () => buildOverrideTree({ effective, patch, inherit: directives }),
    [directives, effective, patch]
  );
  const views = useMemo(() => buildQuickFieldViews(nodes, directives), [directives, nodes]);
  // 内容通常一致的字段默认合并成一个输入框，用户关掉后才逐个编辑。
  const [linkedGroups, setLinkedGroups] = useState<ReadonlySet<string>>(
    () => new Set(QUICK_FIELD_LINKS.map((link) => link.id))
  );

  // 联动合并走的路径不再在分组里重复出现，保证每条路径只有一个编辑入口。
  const hiddenPaths = useMemo(() => {
    const keys = new Set<string>();
    QUICK_FIELD_LINKS.forEach((link) => {
      if (!linkedGroups.has(link.id)) return;
      link.paths.forEach((path) => keys.add(quickFieldKey(path)));
    });
    return keys;
  }, [linkedGroups]);

  const setValueAtPath = useCallback(
    (path: OverridePath, value: unknown) => {
      onChange(setOverrideValue(patch, path, value, effective));
    },
    [effective, onChange, patch]
  );

  const setLinkValue = useCallback(
    (link: QuickLinkDescriptor, value: unknown) => {
      let next: Record<string, unknown> | null = patch;
      link.paths.forEach((path) => {
        next = setOverrideValue(next, path, value, effective);
      });
      if (next) onChange(next);
    },
    [effective, onChange, patch]
  );

  const toggleLink = useCallback((id: string) => {
    setLinkedGroups((previous) => {
      const next = new Set(previous);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  }, []);

  const renderFieldRow = (field: QuickFieldDescriptor) => {
    const fieldKey = quickFieldKey(field.path);
    const node = findQuickTreeNode(nodes, field.path);
    if (field.kind === 'group') {
      if (!node) return null;
      return (
        <QuickFieldNode
          key={fieldKey}
          node={node}
          field={field}
          hiddenPaths={hiddenPaths}
          options={fieldOptions}
          inherit={sourceBinding}
          disabled={disabled}
          setValueAtPath={setValueAtPath}
        />
      );
    }
    return (
      <QuickFieldRow
        key={fieldKey}
        field={field}
        view={views.get(fieldKey) ?? EMPTY_QUICK_FIELD_VIEW}
        options={fieldOptions?.get(fieldKey) ?? []}
        inherit={sourceBinding}
        disabled={disabled}
        onSetValue={(value) => setValueAtPath(field.path, value)}
      />
    );
  };

  const renderSectionRows = (section: QuickSectionDescriptor): ReactNode[] => {
    const rows: ReactNode[] = [];
    const sectionLinks = QUICK_FIELD_LINKS.filter((link) =>
      section.fields.some((field) => field.link === link.id)
    );

    // 先放联动开关，再按开关状态决定这一组是合并的输入框还是逐个字段。
    sectionLinks.forEach((link) => {
      const enabled = linkedGroups.has(link.id);
      rows.push(
        <QuickLinkToggle
          key={`link-toggle-${link.id}`}
          link={link}
          enabled={enabled}
          disabled={disabled}
          onToggle={() => toggleLink(link.id)}
        />
      );
      if (!enabled) return;
      rows.push(
        <QuickLinkRow
          key={`link-${link.id}`}
          link={link}
          view={buildQuickLinkView(link, views)}
          inherit={sourceBinding}
          disabled={disabled}
          onSetValue={(value) => setLinkValue(link, value)}
        />
      );
    });

    section.fields.forEach((field) => {
      if (field.link && linkedGroups.has(field.link)) return;
      rows.push(renderFieldRow(field));
    });

    return rows;
  };

  return (
    <div className={styles.root}>
      {QUICK_FIELD_SECTIONS.map((section) => {
        const Icon = SECTION_ICONS[section.id];
        return (
          <section className={styles.section} key={section.id}>
            <div className={styles.sectionHeader}>
              <span className={styles.iconBadge}>
                <Icon size={15} />
              </span>
              <span className={styles.sectionHeading}>
                <span className={styles.sectionTitle}>
                  {t(`codex_client_models.quick.sections.${section.id}.title`)}
                </span>
                <span className={styles.sectionDescription}>
                  {t(`codex_client_models.quick.sections.${section.id}.description`)}
                </span>
              </span>
            </div>
            <div className={styles.grid}>{renderSectionRows(section)}</div>
          </section>
        );
      })}
    </div>
  );
}
