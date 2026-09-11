import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { IconChevronRight } from '@/components/ui/icons';
import {
  formatOverridePreview,
  isStringListArray,
  seedOverrideValue,
  type OverridePath,
  type OverrideTreeNode,
} from '../model/codexClientModelsTree';
import { canInheritPath, type FieldInheritBinding } from '../model/codexClientModelsInherit';
import { FieldSourceControl } from './FieldSourceControl';
import {
  formatArrayText,
  formatPlainText,
  parseJsonArrayText,
  parseNumberText,
  parsePlainText,
  parseStringListText,
} from '../model/draftValues';
import { DraftField } from './ValueDraftField';
import styles from './OverrideTreeEditor.module.scss';

/** 折叠预览里最多显示几行，超出的部分交给「展开」按钮。 */
const PREVIEW_LINE_LIMIT = 4;

const clampRows = (lines: number, max: number) =>
  Math.min(Math.max(lines, PREVIEW_LINE_LIMIT), max);

const lineCountOf = (text: string) => text.split('\n').length;

/** 继承状态下展示生效值；多行文本按真实换行渲染，不转义。 */
function NodeValuePreview({ node }: { node: OverrideTreeNode }) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);

  if (node.kind === 'object') return null;

  if (node.kind === 'empty') {
    return <div className={styles.previewNull}>null</div>;
  }

  if (node.kind === 'string' || node.kind === 'number' || node.kind === 'boolean') {
    const text = formatOverridePreview(node.value);
    return (
      <div className={styles.previewLine} title={text}>
        {text}
      </div>
    );
  }

  const text =
    node.kind === 'array' && isStringListArray(node.value)
      ? node.value.join('\n')
      : formatOverridePreview(node.value);
  const lines = lineCountOf(text);

  return (
    <div className={styles.previewGroup}>
      <div
        className={[styles.previewBlock, expanded ? styles.previewBlockExpanded : '']
          .filter(Boolean)
          .join(' ')}
        data-monospace={node.kind === 'array' ? 'true' : 'false'}
      >
        {text}
      </div>
      {lines > PREVIEW_LINE_LIMIT ? (
        <button
          type="button"
          className={styles.previewToggle}
          onClick={() => setExpanded((previous) => !previous)}
        >
          {expanded
            ? t('codex_client_models.tree_preview_collapse')
            : t('codex_client_models.tree_preview_expand', { value: lines })}
        </button>
      ) : null}
    </div>
  );
}

interface NodeValueEditorProps {
  node: OverrideTreeNode;
  disabled: boolean;
  onSetValue: (value: unknown) => void;
}

function NodeValueEditor({ node, disabled, onSetValue }: NodeValueEditorProps) {
  const { t } = useTranslation();

  switch (node.kind) {
    case 'boolean':
      return (
        <div className={styles.editor}>
          <ToggleSwitch
            checked={node.value === true}
            onChange={onSetValue}
            ariaLabel={node.key}
            disabled={disabled}
          />
        </div>
      );
    case 'number':
      return (
        <DraftField
          value={node.value}
          format={formatPlainText}
          parse={parseNumberText}
          onCommit={onSetValue}
          ariaLabel={node.key}
          disabled={disabled}
          singleLine
          monospace
          placeholder="0"
        />
      );
    case 'string':
      return (
        <DraftField
          value={node.value}
          format={formatPlainText}
          parse={parsePlainText}
          onCommit={onSetValue}
          ariaLabel={node.key}
          disabled={disabled}
          singleLine
        />
      );
    case 'multiline': {
      const text = formatPlainText(node.value);
      return (
        <DraftField
          value={node.value}
          format={formatPlainText}
          parse={parsePlainText}
          onCommit={onSetValue}
          ariaLabel={node.key}
          disabled={disabled}
          rows={clampRows(lineCountOf(text), 18)}
        />
      );
    }
    case 'array': {
      const stringList = isStringListArray(node.value);
      return (
        <>
          <p className={styles.note}>{t('codex_client_models.tree_array_note')}</p>
          <DraftField
            value={node.value}
            format={formatArrayText}
            parse={stringList ? parseStringListText : parseJsonArrayText}
            onCommit={onSetValue}
            ariaLabel={node.key}
            disabled={disabled}
            rows={clampRows(lineCountOf(formatArrayText(node.value)), 16)}
            monospace
          />
        </>
      );
    }
    default:
      return null;
  }
}

export interface OverrideTreeNodeRowProps {
  node: OverrideTreeNode;
  /** 逐字段的继承来源与操作。 */
  inherit: FieldInheritBinding;
  disabled: boolean;
  isExpanded: (id: string) => boolean;
  onToggle: (id: string) => void;
  onSetValue: (path: OverridePath, value: unknown) => void;
}

export function OverrideTreeNodeRow({
  node,
  inherit,
  disabled,
  isExpanded,
  onToggle,
  onSetValue,
}: OverrideTreeNodeRowProps) {
  const { t } = useTranslation();
  const hasChildren = node.children.length > 0;
  const open = hasChildren && isExpanded(node.id);
  const itemCount = Array.isArray(node.value) ? node.value.length : 0;
  const textLineCount = node.kind === 'multiline' ? lineCountOf(String(node.value)) : 0;

  return (
    <div className={styles.node}>
      <div className={styles.head}>
        {hasChildren ? (
          <button
            type="button"
            className={[styles.toggle, open ? styles.toggleOpen : ''].filter(Boolean).join(' ')}
            onClick={() => onToggle(node.id)}
            aria-expanded={open}
            aria-label={node.key}
          >
            <IconChevronRight size={13} />
          </button>
        ) : (
          <span className={styles.toggleSpacer} />
        )}

        <code className={styles.key}>{node.key}</code>

        {node.insideArray ? null : (
          <FieldSourceControl
            state={node.state}
            source={node.inheritSource}
            declared={node.inheritDeclared}
            sources={inherit.sources}
            inheritable={canInheritPath(node.path)}
            issue={inherit.issueOf(node.path)}
            disabled={disabled}
            onSetLocal={() => onSetValue(node.path, seedOverrideValue(node))}
            onClear={() => inherit.clear(node.path)}
            onInherit={(slug) => inherit.inherit(node.path, slug)}
            onRemove={() => inherit.remove(node.path)}
          />
        )}

        {node.kind === 'object' ? (
          <span className={styles.summary}>
            {t('codex_client_models.tree_summary_fields', { value: node.children.length })}
          </span>
        ) : null}
        {node.kind === 'array' ? (
          <span className={styles.summary}>
            {t('codex_client_models.tree_summary_items', { value: itemCount })}
          </span>
        ) : null}
        {node.kind === 'multiline' ? (
          <span className={styles.summary}>
            {t('codex_client_models.tree_summary_lines', { value: textLineCount })}
          </span>
        ) : null}

        <span className={styles.headSpacer} />
      </div>

      {open ? (
        <div className={styles.children}>
          {node.children.map((child) => (
            <OverrideTreeNodeRow
              key={child.id}
              node={child}
              inherit={inherit}
              disabled={disabled}
              isExpanded={isExpanded}
              onToggle={onToggle}
              onSetValue={onSetValue}
            />
          ))}
        </div>
      ) : null}

      {node.state === 'removed' ? null : (
        <div className={styles.body}>
          {node.state === 'override' ? (
            <NodeValueEditor
              node={node}
              disabled={disabled}
              onSetValue={(value) => onSetValue(node.path, value)}
            />
          ) : (
            <NodeValuePreview node={node} />
          )}
        </div>
      )}
    </div>
  );
}
