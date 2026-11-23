/**
 * 通知状态管理
 * 替代原项目中的 showNotification 方法
 */

import { create } from 'zustand';
import type { ReactNode } from 'react';
import type { Notification, NotificationType } from '@/types';
import { generateId } from '@/utils/helpers';
import {
  NOTIFICATION_ALERT_DURATION_MS,
  NOTIFICATION_SHORT_DURATION_MS,
} from '@/utils/constants';
const MAX_ACTIVE_NOTIFICATIONS = 6;

interface ConfirmationStepOptions {
  title?: string;
  message: ReactNode;
  confirmText?: string;
  cancelText?: string;
  variant?: 'danger' | 'primary' | 'secondary';
}

interface ConfirmationOptions extends ConfirmationStepOptions {
  onConfirm: () => void | Promise<void>;
  onCancel?: () => void;
  secondConfirmation?: ConfirmationStepOptions;
}

interface NotificationState {
  timersPaused: boolean;
  notifications: Notification[];
  confirmation: {
    isOpen: boolean;
    isLoading: boolean;
    step: 1 | 2;
    options: ConfirmationOptions | null;
  };
  showNotification: (
    message: string,
    type?: NotificationType,
    duration?: number,
    renderMessage?: Notification['renderMessage']
  ) => void;
  removeNotification: (id: string) => void;
  clearAll: () => void;
  pauseTimers: () => void;
  resumeTimers: () => void;
  showConfirmation: (options: ConfirmationOptions) => void;
  hideConfirmation: () => void;
  setConfirmationLoading: (loading: boolean) => void;
  advanceConfirmation: () => void;
}

type TimeoutHandle = Parameters<typeof clearTimeout>[0];

interface TimerEntry {
  timerId: TimeoutHandle;
  remainingMs: number;
  startedAt: number | null;
}

const notificationTimers = new Map<string, TimerEntry>();

const clearNotificationTimer = (id: string) => {
  const entry = notificationTimers.get(id);
  if (!entry) return;
  clearTimeout(entry.timerId);
  notificationTimers.delete(id);
};

const clearAllNotificationTimers = () => {
  notificationTimers.forEach((entry) => {
    clearTimeout(entry.timerId);
  });
  notificationTimers.clear();
};

const defaultDurationForType = (type: NotificationType): number =>
  type === 'warning' || type === 'error'
    ? NOTIFICATION_ALERT_DURATION_MS
    : NOTIFICATION_SHORT_DURATION_MS;

export const useNotificationStore = create<NotificationState>((set, get) => {
  const armTimer = (id: string, durationMs: number) => {
    clearNotificationTimer(id);
    if (durationMs <= 0) return;
    if (get().timersPaused) {
      notificationTimers.set(id, {
        timerId: undefined,
        remainingMs: durationMs,
        startedAt: null,
      });
      return;
    }
    const startedAt = Date.now();
    const timerId = setTimeout(() => {
      notificationTimers.delete(id);
      set((state) => {
        const remaining = state.notifications.filter((n) => n.id !== id);
        return {
          notifications: remaining,
          timersPaused: remaining.length === 0 ? false : state.timersPaused,
        };
      });
    }, durationMs);
    notificationTimers.set(id, {
      timerId,
      remainingMs: durationMs,
      startedAt,
    });
  };

  return {
    notifications: [],
    timersPaused: false,
    confirmation: {
      isOpen: false,
      isLoading: false,
      step: 1,
      options: null,
    },

    showNotification: (message, type = 'info', duration, renderMessage) => {
      const resolvedDuration = duration !== undefined ? duration : defaultDurationForType(type);
      const existing = get().notifications.find(
        (item) => item.type === type && item.message === message
      );
      if (existing) {
        const updated: Notification = {
          ...existing,
          duration: resolvedDuration,
          count: (existing.count ?? 1) + 1,
          ...(renderMessage ? { renderMessage } : {}),
        };
        set((state) => ({
          notifications: [...state.notifications.filter((n) => n.id !== existing.id), updated],
        }));
        armTimer(existing.id, resolvedDuration);
        return;
      }

      const id = generateId();
      const notification: Notification = {
        id,
        message,
        type,
        duration: resolvedDuration,
        count: 1,
        ...(renderMessage ? { renderMessage } : {}),
      };

      const evictedIds: string[] = [];
      set((state) => {
        const next = [...state.notifications, notification];
        while (next.length > MAX_ACTIVE_NOTIFICATIONS) {
          const lowPriorityIdx = next.findIndex(
            (n) => n.type === 'info' || n.type === 'success'
          );
          const removeIdx = lowPriorityIdx >= 0 ? lowPriorityIdx : 0;
          const [removed] = next.splice(removeIdx, 1);
          if (removed) evictedIds.push(removed.id);
        }
        return { notifications: next };
      });
      evictedIds.forEach(clearNotificationTimer);
      armTimer(id, resolvedDuration);
    },

    removeNotification: (id) => {
      clearNotificationTimer(id);
      set((state) => {
        const remaining = state.notifications.filter((n) => n.id !== id);
        return {
          notifications: remaining,
          timersPaused: remaining.length === 0 ? false : state.timersPaused,
        };
      });
    },

    clearAll: () => {
      clearAllNotificationTimers();
      set({ notifications: [], timersPaused: false });
    },

    pauseTimers: () => {
      if (get().timersPaused) return;
      const now = Date.now();
      notificationTimers.forEach((entry, id) => {
        clearTimeout(entry.timerId);
        const elapsed = entry.startedAt !== null ? Math.max(0, now - entry.startedAt) : 0;
        notificationTimers.set(id, {
          timerId: undefined,
          remainingMs: Math.max(400, entry.remainingMs - elapsed),
          startedAt: null,
        });
      });
      set({ timersPaused: true });
    },

    resumeTimers: () => {
      if (!get().timersPaused) return;
      set({ timersPaused: false });
      const activeIds = new Set(get().notifications.map((n) => n.id));
      Array.from(notificationTimers.entries()).forEach(([id, entry]) => {
        if (!activeIds.has(id)) {
          notificationTimers.delete(id);
          return;
        }
        if (entry.remainingMs > 0) {
          armTimer(id, entry.remainingMs);
        }
      });
    },
  showConfirmation: (options) => {
    const active = get().confirmation;
    if (active.isOpen && active.options) {
      if (active.isLoading) {
        options.onCancel?.();
        return;
      }
      active.options.onCancel?.();
    }
    set({
      confirmation: {
        isOpen: true,
        isLoading: false,
        step: 1,
        options,
      },
    });
  },

  hideConfirmation: () => {
    set((state) => ({
      confirmation: {
        ...state.confirmation,
        isOpen: false,
        isLoading: false,
        step: 1,
        options: null, // Cleanup
      },
    }));
  },

  setConfirmationLoading: (loading) => {
    set((state) => ({
      confirmation: {
        ...state.confirmation,
        isLoading: loading,
      },
    }));
  },

  advanceConfirmation: () => {
    set((state) => ({
      confirmation: {
        ...state.confirmation,
        isLoading: false,
        step: 2,
      },
    }));
  },
  };
});
