import { useTranslation } from 'react-i18next';
import { DropdownMenu, type DropdownMenuItem } from '@/components/ui/DropdownMenu';
import { IconCheck, IconTriangleAlert } from '@/components/ui/icons';
import type { OverrideFieldState } from '../model/codexClientModelsTree';
import styles from './FieldSourceControl.module.scss';

export interface FieldSourceControlProps {
  /** 本字段自己的补丁状态。 */
  state: OverrideFieldState;
  /** 覆盖本字段的继承来源：自身指令或最近的上级指令。 */
  source: string | null;
  /** 本字段自身声明的继承来源。 */
  declared: string | null;
  /**
   * 一个控件覆盖多条路径、而这些路径来源不一致。此时既不能说是目录值，
   * 也不能挑一个来源冒充全部，如实提示用户挑一个来源统一。
   */
  mixed?: boolean;
  /** 可选的继承源模型。 */
  sources: ReadonlyArray<string>;
  /** 身份字段不能继承，菜单里不列继承源。 */
  inheritable?: boolean;
  /** 继承指令无效时的说明。 */
  issue?: string;
  /** 开始本地覆写；配置面板的字段由输入框直接编辑，因此不传。 */
  onSetLocal?: () => void;
  disabled?: boolean;
  /** 恢复到默认值：丢掉本地补丁与自身指令，必要时写上「不继承」。 */
  onSetDefault: () => void;
  onInherit: (slug: string) => void;
  /** 写入 null，把字段从生效条目里删除。 */
  onRemove: () => void;
}

/**
 * 字段来源控制：用一个状态标签同时表达「这个值从哪来」和「能改成什么」。
 *
 * 显示状态与后端一致：本地 null 是「已删除」，本地有值是「本地值」，
 * 有继承来源是「继承自 X」，两者都没有则是「默认值」，也就是客户端当前收到的取值。
 * 菜单负责改写继承指令，让每个字段都能独立选择保持默认、继承别的模型，或留下本地值；
 * 「恢复默认」与「继承自 X」是两条互不影响的路径，恢复默认不会被上级来源重新接管。
 */
export function FieldSourceControl({
  state,
  source,
  declared,
  mixed = false,
  sources,
  inheritable = true,
  issue,
  onSetLocal,
  disabled = false,
  onSetDefault,
  onInherit,
  onRemove,
}: FieldSourceControlProps) {
  const { t } = useTranslation();

  const tone =
    state === 'removed'
      ? 'removed'
      : state === 'override'
        ? 'override'
        : mixed
          ? 'mixed'
          : source
            ? 'inherited'
            : 'default';
  const label =
    state === 'removed'
      ? t('codex_client_models.field_state_removed')
      : state === 'override'
        ? t('codex_client_models.field_state_override')
        : mixed
          ? t('codex_client_models.field_state_mixed_source')
          : source
            ? t('codex_client_models.field_state_inherited', { slug: source })
            : t('codex_client_models.field_state_default');

  const chip = <span className={[styles.chip, styles[`chip_${tone}`]].join(' ')}>{label}</span>;

  const issueChip = issue ? (
    <span className={styles.issue} title={issue}>
      <IconTriangleAlert size={12} />
      {t('codex_client_models.field_source_issue')}
    </span>
  ) : null;

  const items: DropdownMenuItem[] = [];
  if (state === 'inherit' && onSetLocal) {
    items.push({
      key: 'local',
      label: t('codex_client_models.field_source_local'),
      onClick: onSetLocal,
    });
  }
  // 恢复默认一直可用：字段正被上级来源覆盖时，它会把这条路径停回条目自身的取值，
  // 而不是让字段掉回继承。
  items.push({
    key: 'default',
    label: t('codex_client_models.field_source_clear'),
    onClick: onSetDefault,
  });
  if (inheritable && sources.length > 0) {
    items.push({ key: 'divider-inherit', type: 'divider' });
    sources.forEach((slug) => {
      items.push({
        key: `inherit:${slug}`,
        label: t('codex_client_models.field_source_inherit_from', { slug }),
        icon: slug === declared ? <IconCheck size={13} /> : null,
        disabled: slug === declared,
        onClick: () => onInherit(slug),
      });
    });
  }
  if (state !== 'removed') {
    items.push({ key: 'divider-remove', type: 'divider' });
    items.push({
      key: 'remove',
      label: t('codex_client_models.field_source_remove'),
      tone: 'danger',
      onClick: onRemove,
    });
  }

  if (items.length === 0) {
    return (
      <span className={styles.root}>
        {chip}
        {issueChip}
      </span>
    );
  }

  return (
    <span className={styles.root}>
      <DropdownMenu
        items={items}
        ariaLabel={t('codex_client_models.field_source_menu_label')}
        triggerLabel={chip}
        triggerClassName={styles.trigger}
        menuClassName={styles.menu}
        triggerTitle={
          mixed
            ? t('codex_client_models.field_source_mixed_hint')
            : source
              ? t('codex_client_models.field_source_inherited_hint', { slug: source })
              : undefined
        }
        disabled={disabled}
      />
      {issueChip}
    </span>
  );
}
