/**
 * 通用类型定义
 */

import type { TFunction } from 'i18next';

export type Theme = 'white' | 'dark' | 'auto';

export type VisualEffectsMode = 'full' | 'reduced';

export type Language = 'zh-CN' | 'zh-TW' | 'en' | 'ru';

export type NotificationType = 'info' | 'success' | 'warning' | 'error';

export interface Notification {
  id: string;
  message: string;
  type: NotificationType;
  duration?: number;
  /**
   * Rendered with the live translator while the toast is on screen, so a message
   * built from translation keys follows a language switch. `message` stays the
   * fallback for text that is not localized.
   */
  renderMessage?: (t: TFunction) => string;
}

export interface ApiResponse<T = unknown> {
  data?: T;
  error?: string;
  message?: string;
}

export interface PaginationState {
  currentPage: number;
  pageSize: number;
  totalPages: number;
  totalItems?: number;
}

export interface LoadingState {
  isLoading: boolean;
  error: Error | null;
}

// 泛型异步状态
export interface AsyncState<T> extends LoadingState {
  data: T | null;
}
