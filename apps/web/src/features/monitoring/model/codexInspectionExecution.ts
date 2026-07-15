import { authFilesApi } from '@/services/api/authFiles';
import {
  type CodexInspectionExecutionOutcome,
  type CodexInspectionExecutionResult,
  type CodexInspectionLogLevel,
  type CodexInspectionResultItem,
  type CodexInspectionSettings,
} from '@/features/monitoring/codexInspection';
import type { AuthFileItem } from '@/types';
import { normalizeAuthIndex } from '@/utils/authIndex';
import { resolveAuthProvider, resolveCodexChatgptAccountId } from '@/utils/quota';
import { clampPositiveInteger } from './codexInspectionSettings';
import {
  clearCodexInspectionDisableOwnership,
  recordCodexInspectionDisableOwnership,
} from './codexInspectionOwnership';

type LogHandler = (level: CodexInspectionLogLevel, message: string) => void;

type ExecuteCodexInspectionActionsOptions = {
  settings: CodexInspectionSettings;
  items: CodexInspectionResultItem[];
  previousFiles: AuthFileItem[];
  connectionFingerprint: string;
  source: 'auto' | 'manual';
  onLog?: LogHandler;
};

const runConcurrently = async <T, R>(
  items: T[],
  limit: number,
  task: (item: T, index: number) => Promise<R>
): Promise<R[]> => {
  if (items.length === 0) return [];

  const size = clampPositiveInteger(limit, 1);
  const results = new Array<R>(items.length);
  let cursor = 0;

  const worker = async () => {
    while (true) {
      const index = cursor;
      cursor += 1;
      if (index >= items.length) {
        return;
      }
      results[index] = await task(items[index], index);
    }
  };

  await Promise.all(Array.from({ length: Math.min(size, items.length) }, () => worker()));
  return results;
};

const dedupeExecutionItems = (items: CodexInspectionResultItem[]) => {
  const map = new Map<string, CodexInspectionResultItem>();
  items.forEach((item) => {
    if (item.action === 'keep') return;
    if (!item.fileName) return;
    if (!map.has(item.fileName)) {
      map.set(item.fileName, item);
    }
  });
  return Array.from(map.values()).sort((left, right) =>
    left.fileName.localeCompare(right.fileName)
  );
};

const normalizeProvider = (value: unknown): string => {
  const normalized = String(value ?? '')
    .trim()
    .toLowerCase()
    .replace(/_/g, '-');
  if (normalized === 'x-ai' || normalized === 'grok') return 'xai';
  return normalized || 'codex';
};

const readCurrentFileName = (file: AuthFileItem): string =>
  String(file.name ?? file.id ?? '').trim();

const matchesCurrentDeleteIdentity = (
  file: AuthFileItem,
  item: CodexInspectionResultItem
): boolean => {
  if (readCurrentFileName(file) !== item.fileName) return false;
  if (normalizeProvider(resolveAuthProvider(file)) !== normalizeProvider(item.provider)) return false;
  const authIndex = normalizeAuthIndex(file['auth_index'] ?? file.authIndex ?? file['auth-index']);
  if (item.authIndex && authIndex !== normalizeAuthIndex(item.authIndex)) return false;
  const accountId = resolveCodexChatgptAccountId(file);
  if (item.accountId && accountId !== item.accountId.trim()) return false;
  return true;
};

const failedDeletePreflightOutcome = (
  item: CodexInspectionResultItem,
  error: string
): CodexInspectionExecutionOutcome => ({
  action: 'delete',
  fileName: item.fileName,
  displayAccount: item.displayAccount,
  success: false,
  error,
});

const executeDelete = async (
  item: CodexInspectionResultItem
): Promise<CodexInspectionExecutionOutcome> => {
  try {
    const result = await authFilesApi.deleteFileByName(item.fileName);
    const failed = result.failed[0];
    if (failed) {
      return {
        action: 'delete',
        fileName: item.fileName,
        displayAccount: item.displayAccount,
        success: false,
        error: failed.error || '删除失败',
      };
    }
    if (result.deleted <= 0) {
      return {
        action: 'delete',
        fileName: item.fileName,
        displayAccount: item.displayAccount,
        success: false,
        error: '删除接口未确认认证文件已删除',
      };
    }
    return {
      action: 'delete',
      fileName: item.fileName,
      displayAccount: item.displayAccount,
      success: true,
      error: '',
    };
  } catch (error) {
    return {
      action: 'delete',
      fileName: item.fileName,
      displayAccount: item.displayAccount,
      success: false,
      error: error instanceof Error ? error.message : String(error || '删除失败'),
    };
  }
};

const executeStatusChange = async (
  item: CodexInspectionResultItem,
  disabled: boolean
): Promise<CodexInspectionExecutionOutcome> => {
  try {
    await authFilesApi.setStatusWithFallback(item.fileName, disabled);
    return {
      action: disabled ? 'disable' : 'enable',
      fileName: item.fileName,
      displayAccount: item.displayAccount,
      success: true,
      error: '',
    };
  } catch (error) {
    return {
      action: disabled ? 'disable' : 'enable',
      fileName: item.fileName,
      displayAccount: item.displayAccount,
      success: false,
      error: error instanceof Error ? error.message : String(error || '状态更新失败'),
    };
  }
};

export const executeCodexInspectionActions = async ({
  settings,
  items,
  previousFiles,
  connectionFingerprint,
  source,
  onLog,
}: ExecuteCodexInspectionActionsOptions): Promise<CodexInspectionExecutionResult> => {
  const dedupedItems = dedupeExecutionItems(items);
  const requestedDeleteItems = dedupedItems.filter((item) => item.action === 'delete');
  const disableItems = dedupedItems.filter((item) => item.action === 'disable');
  const enableItems = dedupedItems.filter((item) => item.action === 'enable');
  const outcomes: CodexInspectionExecutionOutcome[] = [];
  let deleteItems = requestedDeleteItems;

  if (requestedDeleteItems.length > 0) {
    try {
      const response = await authFilesApi.list();
      const currentFiles = Array.isArray(response.files) ? response.files : [];
      deleteItems = requestedDeleteItems.filter((item) => {
        const matched = currentFiles.some((file) => matchesCurrentDeleteIdentity(file, item));
        if (matched) return true;
        const outcome = failedDeletePreflightOutcome(
          item,
          '认证文件不存在、Provider 不匹配或账号标识已变化，已拒绝删除'
        );
        outcomes.push(outcome);
        onLog?.('error', `${outcome.displayAccount} 删除失败：${outcome.error}`);
        return false;
      });
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error || '未知错误');
      requestedDeleteItems.forEach((item) => {
        const outcome = failedDeletePreflightOutcome(
          item,
          `删除前刷新认证文件失败，已拒绝删除：${message}`
        );
        outcomes.push(outcome);
        onLog?.('error', `${outcome.displayAccount} 删除失败：${outcome.error}`);
      });
      deleteItems = [];
    }
  }

  if (deleteItems.length > 0) {
    onLog?.('info', `开始删除 ${deleteItems.length} 个账号`);
    const deleteOutcomes = await runConcurrently(
      deleteItems,
      settings.deleteWorkers,
      executeDelete
    );
    deleteOutcomes.forEach((outcome) => {
      onLog?.(
        outcome.success ? 'success' : 'error',
        `${outcome.displayAccount} 删除${outcome.success ? '成功' : `失败：${outcome.error}`}`
      );
    });
    outcomes.push(...deleteOutcomes);
  }

  if (disableItems.length > 0) {
    onLog?.('info', `开始禁用 ${disableItems.length} 个账号`);
    const disableOutcomes = await runConcurrently(disableItems, settings.deleteWorkers, (item) =>
      executeStatusChange(item, true)
    );
    disableOutcomes.forEach((outcome) => {
      onLog?.(
        outcome.success ? 'success' : 'error',
        `${outcome.displayAccount} 禁用${outcome.success ? '成功' : `失败：${outcome.error}`}`
      );
    });
    outcomes.push(...disableOutcomes);
  }

  if (enableItems.length > 0) {
    onLog?.('info', `开始启用 ${enableItems.length} 个账号`);
    const enableOutcomes = await runConcurrently(enableItems, settings.deleteWorkers, (item) =>
      executeStatusChange(item, false)
    );
    enableOutcomes.forEach((outcome) => {
      onLog?.(
        outcome.success ? 'success' : 'error',
        `${outcome.displayAccount} 启用${outcome.success ? '成功' : `失败：${outcome.error}`}`
      );
    });
    outcomes.push(...enableOutcomes);
  }

  const itemByFileName = new Map(dedupedItems.map((item) => [item.fileName, item] as const));
  outcomes.forEach((outcome) => {
    if (!outcome.success) return;
    const item = itemByFileName.get(outcome.fileName);
    if (!item) return;
    if (outcome.action === 'disable' && source === 'auto') {
      recordCodexInspectionDisableOwnership(connectionFingerprint, {
        fileName: item.fileName,
        provider: item.provider,
        authIndex: item.authIndex,
        accountId: item.accountId,
      });
      return;
    }
    clearCodexInspectionDisableOwnership(connectionFingerprint, item.fileName);
  });

  let refreshedFiles = previousFiles;
  let refreshError = '';
  try {
    const response = await authFilesApi.list();
    refreshedFiles = Array.isArray(response.files) ? response.files : previousFiles;
  } catch (error) {
    refreshError = error instanceof Error ? error.message : String(error || '刷新账号列表失败');
    onLog?.('warning', `执行后刷新账号列表失败，已回退旧快照：${refreshError}`);
  }

  return {
    outcomes,
    refreshedFiles,
    refreshError,
  };
};
