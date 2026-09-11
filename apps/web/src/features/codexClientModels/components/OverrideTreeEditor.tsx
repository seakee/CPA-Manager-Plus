import { useCallback, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { IconSearch } from '@/components/ui/icons';
import {
  buildOverrideTree,
  collectExpandableIds,
  countOverrideNodes,
  filterOverrideTree,
  setOverrideValue,
  type OverridePath,
  type OverrideTreeNode,
} from '../model/codexClientModelsTree';
import type { FieldInheritBinding } from '../model/codexClientModelsInherit';
import { OverrideTreeNodeRow } from './OverrideTreeNodeRow';
import styles from './OverrideTreeEditor.module.scss';

export interface OverrideTreeEditorProps {
  /** 生效条目，作为「继承」状态的参照值；新增条目时传默认模板。 */
  effective: unknown;
  /** 当前覆写补丁；null 表示整个条目被删除。 */
  patch: Record<string, unknown> | null;
  /** 由调用方管理的字段，不在字段树里编辑（例如 slug）。 */
  hiddenKeys?: ReadonlySet<string>;
  /** 逐字段的继承来源与操作。 */
  sourceBinding: FieldInheritBinding;
  onChange: (patch: Record<string, unknown>) => void;
  disabled?: boolean;
}

/** 逐字段覆写编辑：字段树 + 每个字段自己的继承 / 覆写 / 删除控制。 */
export function OverrideTreeEditor({
  effective,
  patch,
  hiddenKeys,
  sourceBinding,
  onChange,
  disabled = false,
}: OverrideTreeEditorProps) {
  const { t } = useTranslation();
  const [query, setQuery] = useState('');
  const [onlyOverridden, setOnlyOverridden] = useState(false);
  const [expandedIds, setExpandedIds] = useState<ReadonlySet<string>>(() => new Set<string>());

  const { directives } = sourceBinding;
  const nodes = useMemo(() => {
    const built = buildOverrideTree({ effective, patch, inherit: directives });
    if (!hiddenKeys) return built;
    // 常用字段由配置面板负责，这里连它们的子节点一起剪掉，避免出现两个编辑入口。
    const prune = (list: ReadonlyArray<OverrideTreeNode>): OverrideTreeNode[] =>
      list
        .filter((node) => !hiddenKeys.has(node.id))
        .map((node) =>
          node.children.length > 0 ? { ...node, children: prune(node.children) } : node
        );
    return prune(built);
  }, [directives, effective, hiddenKeys, patch]);
  const visibleNodes = useMemo(
    () => filterOverrideTree(nodes, { query, onlyOverridden }),
    [nodes, query, onlyOverridden]
  );
  const overrideCount = useMemo(() => countOverrideNodes(patch), [patch]);
  const expandableIds = useMemo(() => collectExpandableIds(nodes), [nodes]);
  const filtering = query.trim().length > 0 || onlyOverridden;

  const toggleNode = useCallback((id: string) => {
    setExpandedIds((previous) => {
      const next = new Set(previous);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  }, []);

  const isExpanded = useCallback(
    (id: string) => filtering || expandedIds.has(id),
    [expandedIds, filtering]
  );

  const handleSetValue = useCallback(
    (path: OverridePath, value: unknown) => {
      onChange(setOverrideValue(patch, path, value, effective));
    },
    [effective, onChange, patch]
  );

  return (
    <div className={styles.root}>
      <div className={styles.toolbar}>
        <div className={styles.searchWrap}>
          <Input
            className={styles.search}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder={t('codex_client_models.tree_search_placeholder')}
            disabled={disabled}
            rightElement={<IconSearch size={15} />}
          />
        </div>

        <button
          type="button"
          className={[styles.countButton, onlyOverridden ? styles.countButtonActive : '']
            .filter(Boolean)
            .join(' ')}
          onClick={() => setOnlyOverridden((previous) => !previous)}
          aria-pressed={onlyOverridden}
        >
          <span>{t('codex_client_models.tree_only_overridden')}</span>
          <strong>{overrideCount}</strong>
        </button>

        <Button
          size="xs"
          variant="ghost"
          disabled={disabled || expandableIds.length === 0}
          onClick={() => setExpandedIds(new Set(expandableIds))}
        >
          {t('codex_client_models.tree_expand_all')}
        </Button>
        <Button
          size="xs"
          variant="ghost"
          disabled={disabled || expandedIds.size === 0}
          onClick={() => setExpandedIds(new Set<string>())}
        >
          {t('codex_client_models.tree_collapse_all')}
        </Button>
      </div>

      {nodes.length === 0 ? (
        <div className={styles.empty}>{t('codex_client_models.tree_empty_no_fields')}</div>
      ) : visibleNodes.length === 0 ? (
        <div className={styles.empty}>{t('codex_client_models.tree_empty_filter')}</div>
      ) : (
        <div className={styles.tree}>
          {visibleNodes.map((node) => (
            <OverrideTreeNodeRow
              key={node.id}
              node={node}
              disabled={disabled}
              isExpanded={isExpanded}
              onToggle={toggleNode}
              inherit={sourceBinding}
              onSetValue={handleSetValue}
            />
          ))}
        </div>
      )}
    </div>
  );
}
