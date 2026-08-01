/**
 * 认证状态管理
 * 从原项目 src/modules/login.js 和 src/core/connection.js 迁移
 */

import { create } from 'zustand';
import { persist, createJSONStorage } from 'zustand/middleware';
import type {
  AuthSessionMode,
  AuthState,
  LoginCredentials,
  LoginResult,
  RestoreSessionResult,
  ConnectionStatus,
} from '@/types';
import { STORAGE_KEY_AUTH } from '@/utils/constants';
import { obfuscatedStorage } from '@/services/storage/secureStorage';
import { apiClient } from '@/services/api/client';
import { usageServiceApi } from '@/services/api/usageService';
import { useConfigStore } from './useConfigStore';
import { useModelsStore } from './useModelsStore';
import { useQuotaStore } from './useQuotaStore';
import { useUsageServiceStore } from './useUsageServiceStore';
import { detectApiBaseFromLocation, normalizeApiBase } from '@/utils/connection';
import { sha256Hex } from '@/utils/apiKeyHash';

interface AuthStoreState extends AuthState {
  sessionMode: AuthSessionMode | '';
  sessionPanelBase: string;
  connectionStatus: ConnectionStatus;
  connectionError: string | null;

  // 操作
  login: (credentials: LoginCredentials) => Promise<LoginResult>;
  logout: () => void;
  checkAuth: () => Promise<boolean>;
  restoreSession: (options?: RestoreSessionOptions) => Promise<RestoreSessionResult>;
  updateServerVersion: (version: string | null, buildDate?: string | null) => void;
  updateServerPluginSupport: (supportsPlugin: boolean) => void;
  updateConnectionStatus: (status: ConnectionStatus, error?: string | null) => void;
}

interface RestoreSessionOptions {
  expectedMode?: AuthSessionMode;
  expectedPanelBase?: string;
}

let restoreSessionPromise: Promise<RestoreSessionResult> | null = null;

const sessionMatchesExpectedRuntime = ({
  expectedMode,
  expectedPanelBase,
  resolvedBase,
  sessionMode,
}: {
  expectedMode?: AuthSessionMode;
  expectedPanelBase?: string;
  resolvedBase: string;
  sessionMode: AuthSessionMode | '';
}) => {
  const normalizedExpectedPanelBase = normalizeApiBase(expectedPanelBase || '');
  if (!expectedMode) return true;
  if (sessionMode && sessionMode !== expectedMode) return false;
  if (expectedMode === 'manager_embedded' && normalizedExpectedPanelBase) {
    return resolvedBase === normalizedExpectedPanelBase;
  }
  if (expectedMode === 'external_panel' && normalizedExpectedPanelBase) {
    return resolvedBase === normalizedExpectedPanelBase;
  }
  return true;
};

export const useAuthStore = create<AuthStoreState>()(
  persist(
    (set, get) => ({
      // 初始状态
      isAuthenticated: false,
      apiBase: '',
      managementKey: '',
      rememberPassword: false,
      serverVersion: null,
      serverBuildDate: null,
      supportsPlugin: false,
      sessionMode: '',
      sessionPanelBase: '',
      connectionStatus: 'disconnected',
      connectionError: null,

      // 恢复会话并自动登录
      restoreSession: (options) => {
        if (restoreSessionPromise) return restoreSessionPromise;

        restoreSessionPromise = (async () => {
          obfuscatedStorage.migratePlaintextKeys(['apiBase', 'apiUrl', 'managementKey']);

          const wasLoggedIn = localStorage.getItem('isLoggedIn') === 'true';
          const legacyBase =
            obfuscatedStorage.getItem<string>('apiBase') ||
            obfuscatedStorage.getItem<string>('apiUrl', { encrypt: true });
          const legacyKey = obfuscatedStorage.getItem<string>('managementKey');

          const { apiBase, managementKey, rememberPassword, sessionMode } = get();
          const resolvedBase = normalizeApiBase(apiBase || legacyBase || detectApiBaseFromLocation());
          const resolvedKey = managementKey || legacyKey || '';
          const resolvedRememberPassword = rememberPassword || Boolean(managementKey) || Boolean(legacyKey);

          if (
            !sessionMatchesExpectedRuntime({
              expectedMode: options?.expectedMode,
              expectedPanelBase: options?.expectedPanelBase,
              resolvedBase,
              sessionMode,
            })
          ) {
            const fallbackBase = normalizeApiBase(options?.expectedPanelBase || detectApiBaseFromLocation());
            set({
              apiBase: fallbackBase,
              managementKey: '',
              rememberPassword: false,
              sessionMode: options?.expectedMode ?? '',
              sessionPanelBase: normalizeApiBase(options?.expectedPanelBase || ''),
            });
            apiClient.setConfig({ apiBase: fallbackBase, managementKey: '' });
            localStorage.removeItem('isLoggedIn');
            return false;
          }

          set({
            apiBase: resolvedBase,
            managementKey: resolvedKey,
            rememberPassword: resolvedRememberPassword,
            sessionMode: options?.expectedMode ?? sessionMode,
            sessionPanelBase: normalizeApiBase(options?.expectedPanelBase || get().sessionPanelBase)
          });
          apiClient.setConfig({ apiBase: resolvedBase, managementKey: resolvedKey });

          if (wasLoggedIn && resolvedBase && resolvedKey) {
            try {
              const restoredSessionMode = options?.expectedMode ?? (sessionMode || undefined);
              const result = await get().login({
                apiBase: resolvedBase,
                managementKey: resolvedKey,
                rememberPassword: resolvedRememberPassword,
                sessionMode: restoredSessionMode,
                sessionPanelBase: options?.expectedPanelBase || get().sessionPanelBase,
              });
              return result.recoveryMode ? result : {};
            } catch (error) {
              console.warn('Auto login failed:', error);
              return false;
            }
          }

          return false;
        })();

        return restoreSessionPromise;
      },

      // 登录
      login: async (credentials) => {
        const apiBase = normalizeApiBase(credentials.apiBase);
        const managementKey = credentials.managementKey.trim();
        const rememberPassword = credentials.rememberPassword ?? get().rememberPassword ?? false;
        const sessionMode = credentials.sessionMode ?? get().sessionMode;
        const sessionPanelBase = normalizeApiBase(credentials.sessionPanelBase || get().sessionPanelBase);
        const quotaCacheScope = sha256Hex(`${apiBase}\u0000${managementKey}`);

        const markAuthenticated = (result: LoginResult = {}) => {
          useQuotaStore.getState().activateQuotaCacheScope(quotaCacheScope);
          apiClient.setConfig({ apiBase, managementKey });
          set({
            isAuthenticated: true,
            apiBase,
            managementKey,
            rememberPassword,
            sessionMode,
            sessionPanelBase,
            connectionStatus: 'connected',
            connectionError: null
          });
          if (rememberPassword) {
            localStorage.setItem('isLoggedIn', 'true');
          } else {
            localStorage.removeItem('isLoggedIn');
          }
          return result;
        };

        try {
          set({ connectionStatus: 'connecting', supportsPlugin: false });
          useModelsStore.getState().clearCache();

          // 配置 API 客户端
          apiClient.setConfig({
            apiBase,
            managementKey
          });

          // 测试连接 - 获取配置
          try {
            await useConfigStore.getState().fetchConfig(undefined, true);
          } catch (error) {
            if (sessionMode !== 'manager_embedded') {
              throw error;
            }
            await usageServiceApi.getManagerConfig(apiBase, managementKey);
            useConfigStore.getState().clearCache();
            useUsageServiceStore.getState().setUsageServiceConfig(
              {
                enabled: true,
                serviceBase: apiBase,
              },
              {
                panelBase: sessionPanelBase || apiBase,
                panelHostMode: 'manager_embedded',
              }
            );
            return markAuthenticated({ recoveryMode: 'manager_config' });
          }

          // 登录成功
          return markAuthenticated();
        } catch (error: unknown) {
          const message =
            error instanceof Error
              ? error.message
              : typeof error === 'string'
                ? error
                : 'Connection failed';
          set({
            connectionStatus: 'error',
            connectionError: message || 'Connection failed',
            supportsPlugin: false
          });
          throw error;
        }
      },

      // 登出
      logout: () => {
        restoreSessionPromise = null;
        useConfigStore.getState().clearCache();
        useModelsStore.getState().clearCache();
        useQuotaStore.getState().clearQuotaCache();
        useUsageServiceStore.getState().clearUsageServiceConfig();
        apiClient.setConfig({ apiBase: '', managementKey: '' });
        set({
          isAuthenticated: false,
          apiBase: '',
          managementKey: '',
          serverVersion: null,
          serverBuildDate: null,
          supportsPlugin: false,
          sessionMode: '',
          sessionPanelBase: '',
          connectionStatus: 'disconnected',
          connectionError: null
        });
        localStorage.removeItem('isLoggedIn');
      },

      // 检查认证状态
      checkAuth: async () => {
        const { managementKey, apiBase } = get();

        if (!managementKey || !apiBase) {
          return false;
        }

        try {
          // 重新配置客户端
          apiClient.setConfig({ apiBase, managementKey });
          set({ supportsPlugin: false });

          // 验证连接
          await useConfigStore.getState().fetchConfig();

          set({
            isAuthenticated: true,
            connectionStatus: 'connected'
          });

          return true;
        } catch {
          set({
            isAuthenticated: false,
            connectionStatus: 'error',
            supportsPlugin: false
          });
          return false;
        }
      },

      // 更新服务器版本
      updateServerVersion: (version, buildDate) => {
        set({ serverVersion: version || null, serverBuildDate: buildDate || null });
      },

      updateServerPluginSupport: (supportsPlugin) => {
        set({ supportsPlugin });
      },

      // 更新连接状态
      updateConnectionStatus: (status, error = null) => {
        set({
          connectionStatus: status,
          connectionError: error
        });
      }
    }),
    {
      name: STORAGE_KEY_AUTH,
      storage: createJSONStorage(() => ({
        getItem: (name) => {
          const data = obfuscatedStorage.getItem<AuthStoreState>(name);
          return data ? JSON.stringify(data) : null;
        },
        setItem: (name, value) => {
          obfuscatedStorage.setItem(name, JSON.parse(value));
        },
        removeItem: (name) => {
          obfuscatedStorage.removeItem(name);
        }
      })),
      partialize: (state) => ({
        apiBase: state.apiBase,
        ...(state.rememberPassword ? { managementKey: state.managementKey } : {}),
        rememberPassword: state.rememberPassword,
        serverVersion: state.serverVersion,
        serverBuildDate: state.serverBuildDate,
        sessionMode: state.sessionMode,
        sessionPanelBase: state.sessionPanelBase
      })
    }
  )
);

// 监听全局未授权事件
if (typeof window !== 'undefined') {
  window.addEventListener('unauthorized', () => {
    useAuthStore.getState().logout();
  });

  window.addEventListener(
    'server-version-update',
    ((e: CustomEvent) => {
      const detail = e.detail || {};
      useAuthStore.getState().updateServerVersion(detail.version || null, detail.buildDate || null);
    }) as EventListener
  );

  window.addEventListener(
    'server-plugin-support-update',
    ((e: CustomEvent) => {
      useAuthStore.getState().updateServerPluginSupport(e.detail?.supportsPlugin === true);
    }) as EventListener
  );
}
