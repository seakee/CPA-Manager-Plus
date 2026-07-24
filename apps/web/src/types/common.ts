/**
 * 通用类型定义
 */

export type Theme = 'white' | 'dark' | 'auto';

export type VisualEffectsMode = 'full' | 'reduced';

export type ThemePreset =
  | 'default'
  | 'anthropic'
  | 'underground'
  | 'rose-garden'
  | 'lake-view'
  | 'sunset-glow'
  | 'forest-whisper'
  | 'ocean-breeze'
  | 'lavender-dream'
  | 'monochrome'
  | 'custom';

export type ThemeRadius = 'default' | 'none' | 'sm' | 'md' | 'lg' | 'xl';

export type ThemeDensity = 'compact' | 'default' | 'comfortable';

export type ThemeFont = 'system' | 'modern' | 'serif';

export type ThemeContentLayout = 'full' | 'centered';

export type Language = 'zh-CN' | 'zh-TW' | 'en' | 'ru';

export type NotificationType = 'info' | 'success' | 'warning' | 'error';

export interface Notification {
  id: string;
  message: string;
  type: NotificationType;
  duration?: number;
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
