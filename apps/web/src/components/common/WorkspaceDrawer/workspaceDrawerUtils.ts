import { useEffect } from 'react';
import { hasOpenModal } from '@/components/ui/overlayEscapeStack';
import styles from './WorkspaceDrawer.module.scss';

export const DRAWER_WIDTH = {
  SM: 'clamp(480px, 42vw, 580px)',
  MD: 'clamp(540px, 46vw, 720px)',
  LG: 'clamp(560px, 50vw, 780px)',
} as const;

export function useDrawerSubViewEscape(
  active: boolean,
  onBack: () => void,
  disabled = false
): void {
  useEffect(() => {
    if (!active || typeof window === 'undefined' || typeof window.addEventListener !== 'function') {
      return;
    }
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape' || event.defaultPrevented || hasOpenModal()) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      if (!disabled) {
        onBack();
      }
    };
    window.addEventListener('keydown', handleKeyDown, true);
    return () => window.removeEventListener('keydown', handleKeyDown, true);
  }, [active, disabled, onBack]);
}

export { styles as workspaceDrawerStyles };
