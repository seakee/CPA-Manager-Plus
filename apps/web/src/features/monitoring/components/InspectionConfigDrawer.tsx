import { useCallback, useEffect, useRef, type ReactNode } from 'react';
import {
  DRAWER_WIDTH,
  DrawerActionFooter,
  DrawerBodyShell,
  DrawerIdentityHeader,
  DrawerTabPanel,
  DrawerViewport,
} from '@/components/common/WorkspaceDrawer';
import { Drawer } from '@/components/ui/Drawer';
import { IconSlidersHorizontal } from '@/components/ui/icons';
import { getInspectionConfigFocusTarget } from './inspectionConfigFocus';
import styles from '../CodexInspectionPage.module.scss';

type InspectionConfigDrawerProps = {
  open: boolean;
  title: string;
  description?: string;
  closeLabel: string;
  /** 概览卡点击传入的目标字段名;Drawer 打开后滚动并聚焦同 id 的输入。 */
  focusField?: string | null;
  /** 已含未保存确认逻辑的关闭回调,遮罩/关闭按钮/ESC 共用。 */
  onClose: () => void;
  footer?: ReactNode;
  children?: ReactNode;
};

// 本地与服务端共享的右侧滑配置容器,基于标准 Drawer 与 WorkspaceDrawer 原语构建。
export function InspectionConfigDrawer({
  open,
  title,
  description,
  closeLabel,
  focusField,
  onClose,
  footer,
  children,
}: InspectionConfigDrawerProps) {
  const drawerBodyRef = useRef<HTMLDivElement | null>(null);

  // 调用方的 onClose 已内置未保存确认弹窗;拦截关闭前置钩子以由 open 状态驱动退场动画。
  const handleBeforeClose = useCallback(() => {
    onClose();
    return false;
  }, [onClose]);

  useEffect(() => {
    if (!open || !focusField || typeof window === 'undefined') return;
    let frame = 0;
    const timer = window.setTimeout(() => {
      const focusTargetField = () => {
        const root = drawerBodyRef.current;
        if (!root) return;
        const escaped =
          typeof CSS !== 'undefined' && typeof CSS.escape === 'function'
            ? CSS.escape(focusField)
            : focusField;
        const target =
          root.querySelector<HTMLElement>(`#${escaped}`) ??
          root.querySelector<HTMLElement>(`[data-config-field="${escaped}"]`);
        if (!target) return;
        target.closest('details')?.setAttribute('open', '');
        target.scrollIntoView?.({ block: 'center', behavior: 'smooth' });
        const focusTarget = getInspectionConfigFocusTarget(target);
        focusTarget?.focus({ preventScroll: true });
      };
      if (typeof window.requestAnimationFrame === 'function') {
        frame = window.requestAnimationFrame(focusTargetField);
      } else {
        focusTargetField();
      }
    }, 0);
    return () => {
      window.clearTimeout(timer);
      if (frame && typeof window.cancelAnimationFrame === 'function') {
        window.cancelAnimationFrame(frame);
      }
    };
  }, [open, focusField]);

  return (
    <Drawer
      open={open}
      onClose={onClose}
      onBeforeClose={handleBeforeClose}
      closeLabel={closeLabel}
      width={DRAWER_WIDTH.MD}
      bodyRef={drawerBodyRef}
      className={styles.configDrawerPanel}
      title={
        <DrawerIdentityHeader
          icon={<IconSlidersHorizontal size={18} />}
          iconBoxed
          title={title}
          metaItems={[description]}
        />
      }
      footer={
        footer ? (
          <DrawerActionFooter className={styles.configDrawerFooter} right={footer} />
        ) : undefined
      }
    >
      <DrawerBodyShell>
        <DrawerViewport>
          <DrawerTabPanel>{children}</DrawerTabPanel>
        </DrawerViewport>
      </DrawerBodyShell>
    </Drawer>
  );
}
