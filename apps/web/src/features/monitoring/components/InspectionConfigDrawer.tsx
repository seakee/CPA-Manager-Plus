import { useEffect, useRef, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { IconX } from '@/components/ui/icons';
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
  footer: ReactNode;
  children: ReactNode;
};

const escapeId = (value: string) =>
  typeof CSS !== 'undefined' && typeof CSS.escape === 'function' ? CSS.escape(value) : value;

// 本地与服务端共享的右侧滑配置容器。结构沿用既有 configDrawer 样式,支持 ESC 与点击遮罩关闭。
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
  const bodyRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    if (!open) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [open, onClose]);

  useEffect(() => {
    if (!open || !focusField) return;
    const frame = window.requestAnimationFrame(() => {
      const root = bodyRef.current;
      if (!root) return;
      const target = root.querySelector<HTMLElement>(`#${escapeId(focusField)}`);
      if (!target) return;
      target.closest('details')?.setAttribute('open', '');
      target.scrollIntoView({ block: 'center', behavior: 'smooth' });
      const focusTarget = getInspectionConfigFocusTarget(target);
      focusTarget?.focus({ preventScroll: true });
    });
    return () => window.cancelAnimationFrame(frame);
  }, [open, focusField]);

  if (!open) return null;

  const drawerContent = (
    <div
      className={styles.configDrawerOverlay}
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <aside
        className={styles.configDrawer}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className={styles.configDrawerHeader}>
          <div>
            <h2>{title}</h2>
            {description ? <p>{description}</p> : null}
          </div>
          <button
            type="button"
            className={styles.configDrawerClose}
            onClick={onClose}
            aria-label={closeLabel}
          >
            <IconX size={18} />
          </button>
        </header>

        <div className={styles.configDrawerBody} ref={bodyRef}>
          {children}
        </div>

        <footer className={styles.configDrawerFooter}>{footer}</footer>
      </aside>
    </div>
  );

  return typeof document === 'undefined'
    ? drawerContent
    : createPortal(drawerContent, document.body);
}
