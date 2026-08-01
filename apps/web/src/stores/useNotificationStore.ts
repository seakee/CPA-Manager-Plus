/**
 * 通知状态管理
 * 替代原项目中的 showNotification 方法
 */

import { create } from 'zustand';
import type { ReactNode } from 'react';
import type { Notification, NotificationType } from '@/types';
import { generateId } from '@/utils/helpers';
import { NOTIFICATION_DURATION_MS } from '@/utils/constants';

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
  notifications: Notification[];
  confirmation: {
    isOpen: boolean;
    isLoading: boolean;
    step: 1 | 2;
    options: ConfirmationOptions | null;
  };
  showNotification: (message: string, type?: NotificationType, duration?: number) => void;
  removeNotification: (id: string) => void;
  clearAll: () => void;
  showConfirmation: (options: ConfirmationOptions) => void;
  hideConfirmation: () => void;
  setConfirmationLoading: (loading: boolean) => void;
  advanceConfirmation: () => void;
}

export const useNotificationStore = create<NotificationState>((set) => ({
  notifications: [],
  confirmation: {
    isOpen: false,
    isLoading: false,
    step: 1,
    options: null,
  },

  showNotification: (message, type = 'info', duration = NOTIFICATION_DURATION_MS) => {
    const id = generateId();
    const notification: Notification = {
      id,
      message,
      type,
      duration,
    };

    set((state) => ({
      notifications: [...state.notifications, notification],
    }));

    // 自动移除通知
    if (duration > 0) {
      setTimeout(() => {
        set((state) => ({
          notifications: state.notifications.filter((n) => n.id !== id),
        }));
      }, duration);
    }
  },

  removeNotification: (id) => {
    set((state) => ({
      notifications: state.notifications.filter((n) => n.id !== id),
    }));
  },

  clearAll: () => {
    set({ notifications: [] });
  },

  showConfirmation: (options) => {
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
}));
