import {
  authFilesApi,
  type AuthFileDeleteIdentityTarget,
  type AuthFileStatusTarget,
} from '@/services/api/authFiles';
import type { TFunction } from 'i18next';
import {
  type CodexInspectionExecutionOutcome,
  type CodexInspectionExecutionResult,
  type CodexInspectionLogHandler,
  type CodexInspectionResultItem,
  type CodexInspectionSettings,
} from '@/features/monitoring/codexInspection';
import type { AuthFileItem } from '@/types';
import { normalizeAuthIndex } from '@/utils/authIndex';
import {
  readAuthFileStatusAccountId,
  readAuthFileStatusAccountSnapshot,
  readAuthFileStatusRuntimeId,
  resolveAuthFileStatusMutationTarget,
  type AuthFileStatusMutationResolution,
} from '@/utils/authFileStatusMutation';
import { resolveAuthProvider } from '@/utils/quota/validators';
import { clampPositiveInteger } from './codexInspectionSettings';
import {
  clearCodexInspectionDisableOwnership,
  clearCodexInspectionDisableOwnershipForFile,
  getCodexInspectionOwnershipIdentityKey,
  hasCodexInspectionStableIdentity,
  recordCodexInspectionDisableOwnership,
  replaceCodexInspectionDisableOwnershipForFile,
} from './codexInspectionOwnership';

const identityT = ((key: string) => key) as TFunction;
const STATUS_MUTATION_SCOPE_REASON =
  '认证凭证缺少唯一 runtime ID，或 runtime ID 与物理文件选择器冲突，已阻止状态修改，请人工确认';
const FILE_DELETE_COVERAGE_REASON =
  '删除动作未完整覆盖该物理认证文件当前包含的全部凭证，已阻止删除，请人工确认';
const PLUGIN_SOURCE_STATUS_CHANGED_REASON =
  '认证文件成员、Provider 或账号标识已变化，已拒绝插件源文件状态修改';

const formatExecutionAction = (action: string, t: TFunction) => {
  switch (action) {
    case 'delete':
      return t('monitoring.codex_inspection_action_delete');
    case 'disable':
      return t('monitoring.codex_inspection_action_disable');
    case 'enable':
      return t('monitoring.codex_inspection_action_enable');
    default:
      return action;
  }
};

const buildExecutionOutcomeLogDetail = (outcome: CodexInspectionExecutionOutcome) => ({
  fileName: outcome.fileName,
  displayAccount: outcome.displayAccount,
  action: outcome.action,
  status: outcome.status,
  success: outcome.success,
  ...(outcome.error ? { error: outcome.error } : {}),
});

const logExecutionOutcome = (
  outcome: CodexInspectionExecutionOutcome,
  onLog: CodexInspectionLogHandler | undefined,
  t: TFunction
) => {
  const level =
    outcome.status === 'success'
      ? 'success'
      : outcome.status === 'failed'
        ? 'error'
        : outcome.status === 'needs_review'
          ? 'warning'
          : 'info';
  const messageKey =
    outcome.status === 'success'
      ? 'monitoring.codex_inspection_log_action_success'
      : outcome.status === 'needs_review'
        ? 'monitoring.codex_inspection_log_action_needs_review'
        : outcome.status === 'skipped'
          ? 'monitoring.codex_inspection_log_action_skipped'
          : 'monitoring.codex_inspection_log_action_failed';
  onLog?.(
    level,
    t(messageKey, {
      account: outcome.displayAccount,
      action: formatExecutionAction(outcome.action, t),
      ...(outcome.status === 'success' ? {} : { message: outcome.error }),
    }),
    buildExecutionOutcomeLogDetail(outcome)
  );
};

type ExecuteCodexInspectionActionsOptions = {
  settings: CodexInspectionSettings;
  items: CodexInspectionResultItem[];
  referenceItems?: CodexInspectionResultItem[];
  previousFiles: AuthFileItem[];
  connectionFingerprint: string;
  source: 'auto' | 'manual';
  preflightOutcomes?: CodexInspectionExecutionOutcome[];
  onLog?: CodexInspectionLogHandler;
  t?: TFunction;
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

const buildPreflightOutcome = (
  item: CodexInspectionResultItem,
  status: CodexInspectionExecutionOutcome['status'],
  success: boolean,
  error: string,
  coveredByAccountKey?: string
): CodexInspectionExecutionOutcome => ({
  accountKey: item.key,
  ...(coveredByAccountKey ? { coveredByAccountKey } : {}),
  action: item.action as CodexInspectionExecutionOutcome['action'],
  fileName: item.fileName,
  displayAccount: item.displayAccount,
  status,
  success,
  error,
});

const isFileExecutionAction = (item: CodexInspectionResultItem) =>
  item.action === 'delete' || item.action === 'disable' || item.action === 'enable';

const mergeExecutionReferenceItems = (
  items: CodexInspectionResultItem[],
  referenceItems: CodexInspectionResultItem[]
) => {
  const selectedByKey = new Map(items.map((item) => [item.key, item] as const));
  const mergedItems = referenceItems.map((referenceItem) => {
    const selectedItem = selectedByKey.get(referenceItem.key);
    if (!selectedItem) return referenceItem;
    selectedByKey.delete(referenceItem.key);

    // Use the actual selected action. In automatic disable mode a delete suggestion
    // becomes a credential-scoped disable and must not retain file-level delete semantics.
    return selectedItem;
  });
  mergedItems.push(...selectedByKey.values());
  return mergedItems;
};

type ExecutionActionGroup = {
  key: string;
  items: CodexInspectionResultItem[];
  action: CodexInspectionResultItem['action'];
  mixed: boolean;
};

const getExecutionIdentityKey = (item: CodexInspectionResultItem) =>
  getCodexInspectionOwnershipIdentityKey({
    fileName: item.fileName,
    provider: item.provider,
    authIndex: item.authIndex,
    accountId: item.accountId,
    accountSnapshot: readInspectionAccountSnapshot(item),
  });

const buildExecutionActionGroups = (items: CodexInspectionResultItem[]) => {
  const groups: ExecutionActionGroup[] = [];
  const groupByItemKey = new Map<string, ExecutionActionGroup>();
  const fileOrder: string[] = [];
  const itemsByFile = new Map<string, CodexInspectionResultItem[]>();

  items.forEach((item) => {
    const fileName = item.fileName.trim();
    if (!fileName) return;
    if (!itemsByFile.has(fileName)) {
      itemsByFile.set(fileName, []);
      fileOrder.push(fileName);
    }
    itemsByFile.get(fileName)?.push(item);
  });

  fileOrder.forEach((fileName) => {
    const allFileItems = itemsByFile.get(fileName) ?? [];
    const fileItems = allFileItems.filter(isFileExecutionAction);
    if (fileItems.length === 0) return;
    if (fileItems.some((item) => item.action === 'delete')) {
      const group: ExecutionActionGroup = {
        key: `file:${fileName}`,
        items: fileItems,
        action: 'delete',
        mixed: allFileItems.some((item) => item.action !== 'delete'),
      };
      groups.push(group);
      fileItems.forEach((item) => groupByItemKey.set(item.key, group));
      return;
    }

    const identityGroups = new Map<string, ExecutionActionGroup>();
    fileItems.forEach((item) => {
      const identityKey = getExecutionIdentityKey(item);
      const group = identityGroups.get(identityKey) ?? {
        key: `credential:${identityKey}`,
        items: [],
        action: item.action,
        mixed: false,
      };
      if (item.action !== group.action) group.mixed = true;
      group.items.push(item);
      identityGroups.set(identityKey, group);
      groupByItemKey.set(item.key, group);
    });
    groups.push(...identityGroups.values());
  });

  return { groups, groupByItemKey };
};

const planExecutionItems = (
  items: CodexInspectionResultItem[],
  referenceItems: CodexInspectionResultItem[]
) => {
  const preflightOutcomes: CodexInspectionExecutionOutcome[] = [];
  const { groupByItemKey } = buildExecutionActionGroups(
    mergeExecutionReferenceItems(items, referenceItems)
  );

  const executableItems: CodexInspectionResultItem[] = [];
  const canonicalAccountKeyByGroup = new Map<string, string>();
  items.forEach((item) => {
    if (!isFileExecutionAction(item)) return;
    const fileName = item.fileName.trim();
    if (!fileName) {
      preflightOutcomes.push(
        buildPreflightOutcome(item, 'failed', false, '认证文件名为空，无法执行')
      );
      return;
    }
    if (
      !hasCodexInspectionStableIdentity({
        fileName: item.fileName,
        provider: item.provider,
        authIndex: item.authIndex,
        accountId: item.accountId,
        accountSnapshot: item.accountSnapshot,
      })
    ) {
      preflightOutcomes.push(
        buildPreflightOutcome(
          item,
          'needs_review',
          true,
          '巡检结果缺少稳定账号标识，已阻止处理，请人工确认'
        )
      );
      return;
    }
    const group = groupByItemKey.get(item.key) ?? {
      key: `credential:${getExecutionIdentityKey(item)}`,
      items: [item],
      action: item.action,
      mixed: false,
    };
    if (group.mixed) {
      preflightOutcomes.push(
        buildPreflightOutcome(
          item,
          'needs_review',
          true,
          '同一认证文件下存在多个不同建议动作，文件级处理已阻止，请到认证文件管理中手动处理'
        )
      );
      return;
    }
    const canonicalAccountKey = canonicalAccountKeyByGroup.get(group.key);
    if (canonicalAccountKey) {
      preflightOutcomes.push(
        buildPreflightOutcome(
          item,
          'skipped',
          true,
          '该认证目标已由另一条结果处理',
          canonicalAccountKey
        )
      );
      return;
    }
    canonicalAccountKeyByGroup.set(group.key, item.key);
    executableItems.push(item);
  });

  return {
    items: executableItems.sort((left, right) => {
      const fileOrder = left.fileName.localeCompare(right.fileName);
      return (
        fileOrder || getExecutionIdentityKey(left).localeCompare(getExecutionIdentityKey(right))
      );
    }),
    preflightOutcomes,
  };
};

const summarizeExecutionOutcomes = (outcomes: CodexInspectionExecutionOutcome[]) =>
  outcomes.reduce(
    (summary, outcome) => {
      summary[outcome.status] += 1;
      return summary;
    },
    { success: 0, failed: 0, skipped: 0, needs_review: 0 }
  );

const normalizeProvider = (value: unknown): string => {
  const normalized = String(value ?? '')
    .trim()
    .toLowerCase()
    .replace(/_/g, '-');
  if (normalized === 'x-ai' || normalized === 'grok') return 'xai';
  return normalized;
};

const readCurrentFileName = (file: AuthFileItem): string => String(file.name ?? '').trim();

const buildDeleteIdentityTarget = (file: AuthFileItem): AuthFileDeleteIdentityTarget => ({
  name: readCurrentFileName(file),
  runtimeId: readAuthFileStatusRuntimeId(file) || null,
  authIndex: normalizeAuthIndex(file['auth_index'] ?? file.authIndex ?? file['auth-index']),
  provider: resolveAuthProvider(file),
  accountId: readAuthFileStatusAccountId(file) || null,
  accountSnapshot: readAuthFileStatusAccountSnapshot(file) || null,
});

const getAuthFileSourceMemberIdentityKey = (file: AuthFileItem): string =>
  JSON.stringify(buildDeleteIdentityTarget(file));

const authFileSourceMembershipMatches = (
  expectedMembers: AuthFileItem[],
  currentMembers: AuthFileItem[]
): boolean => {
  if (expectedMembers.length !== currentMembers.length) return false;
  const expectedKeys = expectedMembers.map(getAuthFileSourceMemberIdentityKey).sort();
  const currentKeys = currentMembers.map(getAuthFileSourceMemberIdentityKey).sort();
  return expectedKeys.every((key, index) => key === currentKeys[index]);
};

const readInspectionAccountSnapshot = (item: CodexInspectionResultItem): string => {
  const snapshot = item.accountSnapshot?.trim() ?? '';
  return snapshot && snapshot !== item.fileName.trim() ? snapshot : '';
};

const matchesCurrentActionIdentity = (
  file: AuthFileItem,
  item: CodexInspectionResultItem
): boolean => {
  if (readCurrentFileName(file) !== item.fileName) return false;
  const currentProvider = normalizeProvider(resolveAuthProvider(file));
  const expectedProvider = normalizeProvider(item.provider);
  if (!currentProvider || !expectedProvider || currentProvider !== expectedProvider) return false;
  const authIndex = normalizeAuthIndex(file['auth_index'] ?? file.authIndex ?? file['auth-index']);
  if (item.authIndex && authIndex !== normalizeAuthIndex(item.authIndex)) return false;
  const accountId = readAuthFileStatusAccountId(file);
  const expectedAccountId = item.accountId?.trim() ?? '';
  if (expectedAccountId) return accountId === expectedAccountId;
  const expectedSnapshot = readInspectionAccountSnapshot(item);
  if (!expectedSnapshot) return normalizeAuthIndex(item.authIndex) !== null;
  return readAuthFileStatusAccountSnapshot(file) === expectedSnapshot;
};

type ResolvedStatusActionItem = {
  item: CodexInspectionResultItem;
  currentFile: AuthFileItem;
  resolution: AuthFileStatusMutationResolution;
};

type StatusActionGroupPlan = {
  canonicalKey: string;
  action: 'disable' | 'enable';
  members: CodexInspectionResultItem[];
  affectedFiles: AuthFileItem[];
};

const isStatusExecutionAction = (
  item: CodexInspectionResultItem
): item is CodexInspectionResultItem & { action: 'disable' | 'enable' } =>
  item.action === 'disable' || item.action === 'enable';

const buildStatusActionGroupPlans = (
  resolvedItems: Map<string, ResolvedStatusActionItem>,
  currentFilesByName: Map<string, AuthFileItem[]>
): Map<string, StatusActionGroupPlan> => {
  const plans = new Map<string, StatusActionGroupPlan>();
  const entriesByFile = new Map<string, ResolvedStatusActionItem[]>();
  resolvedItems.forEach((entry) => {
    const fileName = readCurrentFileName(entry.currentFile);
    const entries = entriesByFile.get(fileName) ?? [];
    entries.push(entry);
    entriesByFile.set(fileName, entries);
  });

  currentFilesByName.forEach((currentFiles, fileName) => {
    if (currentFiles.length <= 1) return;
    const entries = entriesByFile.get(fileName) ?? [];
    const matchedEntries: ResolvedStatusActionItem[] = [];
    const members: CodexInspectionResultItem[] = [];
    let action: 'disable' | 'enable' | null = null;
    for (const currentFile of currentFiles) {
      const matches = entries.filter((entry) => entry.currentFile === currentFile);
      if (matches.length !== 1 || !isStatusExecutionAction(matches[0].item)) return;
      const matchedAction = matches[0].item.action;
      if (action === null) action = matchedAction;
      if (matchedAction !== action) return;
      matchedEntries.push(matches[0]);
      members.push(matches[0].item);
    }
    const sourceEntries = matchedEntries.filter(
      (entry) => entry.resolution.scope === 'source-file'
    );
    if (!action || sourceEntries.length > 1) return;
    const canonicalEntry = sourceEntries[0] ?? matchedEntries[0];
    if (!canonicalEntry) return;

    plans.set(fileName, {
      canonicalKey: canonicalEntry.item.key,
      action,
      members,
      affectedFiles: currentFiles,
    });
  });
  return plans;
};

const deleteActionCoversCurrentFile = (
  currentFiles: AuthFileItem[],
  referenceItems: CodexInspectionResultItem[],
  fileName: string
): boolean => {
  if (currentFiles.length === 0) return false;
  const normalizedFileName = fileName.trim();
  const deleteByIdentity = new Map<string, CodexInspectionResultItem>();
  for (const item of referenceItems) {
    if (item.fileName.trim() !== normalizedFileName) continue;
    if (item.action !== 'delete') return false;
    deleteByIdentity.set(getExecutionIdentityKey(item), item);
  }
  const deleteItems = Array.from(deleteByIdentity.values());
  if (deleteItems.length === 0) return false;

  const used = new Set<number>();
  for (const currentFile of currentFiles) {
    const matches = deleteItems
      .map((item, index) => ({ item, index }))
      .filter(
        ({ item, index }) => !used.has(index) && matchesCurrentActionIdentity(currentFile, item)
      );
    if (matches.length !== 1) return false;
    used.add(matches[0].index);
  }
  return true;
};

const resolveVerifiedDeleteSelector = (
  currentFiles: AuthFileItem[],
  currentCandidates: AuthFileItem[],
  item: CodexInspectionResultItem
): string => {
  const identityMatches = currentCandidates.filter((file) =>
    matchesCurrentActionIdentity(file, item)
  );
  if (identityMatches.length !== 1) return '';

  const physicalName = item.fileName.trim();
  const sourceRows = currentCandidates.filter(
    (file) => readAuthFileStatusRuntimeId(file) === physicalName
  );
  if (sourceRows.length > 1) return '';

  const deletesPhysicalFile = currentCandidates.length > 1;
  const selector = deletesPhysicalFile
    ? physicalName
    : readAuthFileStatusRuntimeId(sourceRows[0] ?? identityMatches[0]);
  if (!selector) return '';
  const selectorMatches = currentFiles.filter(
    (file) => readAuthFileStatusRuntimeId(file) === selector
  );
  if (deletesPhysicalFile) {
    return selectorMatches.some((file) => readCurrentFileName(file) !== physicalName)
      ? ''
      : selector;
  }
  if (selectorMatches.length !== 1 || readCurrentFileName(selectorMatches[0]) !== physicalName) {
    return '';
  }
  return selector;
};

const statusActionCoversCurrentFile = (
  currentFiles: AuthFileItem[],
  members: CodexInspectionResultItem[],
  fileName: string
): boolean => {
  if (currentFiles.length === 0 || currentFiles.length !== members.length) return false;
  const normalizedFileName = fileName.trim();
  const relevantMembers = members.filter(
    (member) => member.fileName.trim() === normalizedFileName && isStatusExecutionAction(member)
  );
  if (relevantMembers.length !== currentFiles.length) return false;

  const used = new Set<number>();
  for (const currentFile of currentFiles) {
    const matches = relevantMembers
      .map((member, index) => ({ member, index }))
      .filter(
        ({ member, index }) => !used.has(index) && matchesCurrentActionIdentity(currentFile, member)
      );
    if (matches.length !== 1) return false;
    used.add(matches[0].index);
  }
  return true;
};

const verifyPluginSourceStatusFallback = async (
  snapshotMembers: AuthFileItem[],
  actionMembers: CodexInspectionResultItem[],
  item: CodexInspectionResultItem,
  runtimeId: string
): Promise<AuthFileDeleteIdentityTarget[]> => {
  const physicalName = item.fileName.trim();
  if (
    !physicalName ||
    !runtimeId ||
    runtimeId === physicalName ||
    !statusActionCoversCurrentFile(snapshotMembers, actionMembers, physicalName)
  ) {
    throw new Error(PLUGIN_SOURCE_STATUS_CHANGED_REASON);
  }

  const response = await authFilesApi.list();
  const freshFiles = Array.isArray(response.files) ? response.files : [];
  const freshMembers = freshFiles.filter((file) => readCurrentFileName(file) === physicalName);
  const resolution = resolveAuthFileStatusMutationTarget(freshFiles, {
    name: physicalName,
    runtimeId,
    authIndex: item.authIndex,
    provider: item.provider,
    accountId: item.accountId,
    accountSnapshot: readInspectionAccountSnapshot(item),
  });
  const physicalSelectorCollides = freshFiles.some(
    (file) =>
      readAuthFileStatusRuntimeId(file) === physicalName &&
      readCurrentFileName(file) !== physicalName
  );
  if (
    !authFileSourceMembershipMatches(snapshotMembers, freshMembers) ||
    !statusActionCoversCurrentFile(freshMembers, actionMembers, physicalName) ||
    !resolution.target ||
    resolution.failure !== null ||
    resolution.scope !== 'credential' ||
    readAuthFileStatusRuntimeId(resolution.target) !== runtimeId ||
    !matchesCurrentActionIdentity(resolution.target, item) ||
    physicalSelectorCollides
  ) {
    throw new Error(PLUGIN_SOURCE_STATUS_CHANGED_REASON);
  }
  return freshMembers.map(buildDeleteIdentityTarget);
};

const buildStatusActionTarget = (
  item: CodexInspectionResultItem,
  currentFile: AuthFileItem
): AuthFileStatusTarget => ({
  name: item.fileName,
  runtimeId: readAuthFileStatusRuntimeId(currentFile) || null,
  authIndex: item.authIndex,
  provider: item.provider,
  accountId: item.accountId,
  accountSnapshot: readInspectionAccountSnapshot(item),
});

type VerifiedStatusActionMember = {
  item: CodexInspectionResultItem;
  currentFile: AuthFileItem;
  resolution: AuthFileStatusMutationResolution;
};

type VerifiedStatusActionGroup = {
  members: VerifiedStatusActionMember[];
  sourceMember: VerifiedStatusActionMember | null;
  affectedFiles: AuthFileItem[];
};

const resolveVerifiedStatusActionGroup = (
  currentFiles: AuthFileItem[],
  actionMembers: CodexInspectionResultItem[],
  fileName: string
): VerifiedStatusActionGroup | null => {
  const physicalName = fileName.trim();
  const affectedFiles = currentFiles.filter((file) => readCurrentFileName(file) === physicalName);
  if (
    !physicalName ||
    !statusActionCoversCurrentFile(affectedFiles, actionMembers, physicalName) ||
    actionMembers.some((member) => member.action !== actionMembers[0]?.action)
  ) {
    return null;
  }

  const usedFiles = new Set<AuthFileItem>();
  const members: VerifiedStatusActionMember[] = [];
  for (const member of actionMembers) {
    const resolution = resolveAuthFileStatusMutationTarget(currentFiles, {
      name: member.fileName,
      runtimeId: member.runtimeId,
      authIndex: member.authIndex,
      provider: member.provider,
      accountId: member.accountId,
      accountSnapshot: readInspectionAccountSnapshot(member),
    });
    const currentFile = resolution.target;
    if (
      !currentFile ||
      resolution.failure !== null ||
      resolution.scope === 'ambiguous' ||
      usedFiles.has(currentFile) ||
      readCurrentFileName(currentFile) !== physicalName ||
      !matchesCurrentActionIdentity(currentFile, member)
    ) {
      return null;
    }
    usedFiles.add(currentFile);
    members.push({ item: member, currentFile, resolution });
  }

  const sourceMembers = members.filter((member) => member.resolution.scope === 'source-file');
  if (sourceMembers.length > 1) return null;
  if (sourceMembers.length === 1) {
    if (
      members.some(
        (member) =>
          member.resolution.scope !== 'source-file' && member.resolution.scope !== 'expanded-child'
      ) ||
      !authFileSourceMembershipMatches(affectedFiles, sourceMembers[0].resolution.affectedFiles)
    ) {
      return null;
    }
  } else if (members.some((member) => member.resolution.scope !== 'credential')) {
    return null;
  }

  return {
    members,
    sourceMember: sourceMembers[0] ?? null,
    affectedFiles,
  };
};

type PatchedStatusActionMember = {
  item: CodexInspectionResultItem;
  originalDisabled: boolean;
};

const rollbackPatchedStatusActionMembers = async (
  patchedMembers: PatchedStatusActionMember[]
): Promise<string> => {
  const failures: string[] = [];
  for (let index = patchedMembers.length - 1; index >= 0; index--) {
    const patchedMember = patchedMembers[index];
    try {
      const response = await authFilesApi.list();
      const currentFiles = Array.isArray(response.files) ? response.files : [];
      const resolution = resolveAuthFileStatusMutationTarget(currentFiles, {
        name: patchedMember.item.fileName,
        runtimeId: patchedMember.item.runtimeId,
        authIndex: patchedMember.item.authIndex,
        provider: patchedMember.item.provider,
        accountId: patchedMember.item.accountId,
        accountSnapshot: readInspectionAccountSnapshot(patchedMember.item),
      });
      const currentFile = resolution.target;
      if (
        !currentFile ||
        resolution.failure !== null ||
        resolution.scope !== 'credential' ||
        !matchesCurrentActionIdentity(currentFile, patchedMember.item)
      ) {
        throw new Error('回滚目标不再是唯一凭证');
      }
      if ((currentFile.disabled === true) === patchedMember.originalDisabled) continue;
      await authFilesApi.setStatusWithFallback(
        buildStatusActionTarget(patchedMember.item, currentFile),
        patchedMember.originalDisabled
      );
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error || '未知错误');
      failures.push(`${patchedMember.item.displayAccount}: ${message}`);
    }
  }
  return failures.join('; ');
};

const buildActionValidationOutcome = (
  item: CodexInspectionResultItem,
  status: 'failed' | 'skipped' | 'needs_review',
  error: string,
  coveredByAccountKey?: string
): CodexInspectionExecutionOutcome => ({
  accountKey: item.key,
  ...(coveredByAccountKey ? { coveredByAccountKey } : {}),
  action: item.action as CodexInspectionExecutionOutcome['action'],
  fileName: item.fileName,
  displayAccount: item.displayAccount,
  status,
  success: status !== 'failed',
  error,
});

const executeDelete = async (
  item: CodexInspectionResultItem,
  actionReferenceItems: CodexInspectionResultItem[]
): Promise<CodexInspectionExecutionOutcome> => {
  let deleteSelector = '';
  let deleteIdentityTargets: AuthFileDeleteIdentityTarget[] = [];
  try {
    const response = await authFilesApi.list();
    const currentFiles = Array.isArray(response.files) ? response.files : [];
    const currentCandidates = currentFiles.filter(
      (file) => readCurrentFileName(file) === item.fileName.trim()
    );
    if (!deleteActionCoversCurrentFile(currentCandidates, actionReferenceItems, item.fileName)) {
      return buildActionValidationOutcome(
        item,
        'failed',
        '认证文件成员、Provider 或账号标识已变化，已拒绝删除'
      );
    }
    deleteSelector = resolveVerifiedDeleteSelector(currentFiles, currentCandidates, item);
    if (!deleteSelector) {
      return buildActionValidationOutcome(
        item,
        'failed',
        '认证文件缺少唯一 runtime ID，已拒绝删除'
      );
    }
    deleteIdentityTargets = currentCandidates.map(buildDeleteIdentityTarget);
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error || '未知错误');
    return buildActionValidationOutcome(
      item,
      'failed',
      `执行删除前刷新认证文件失败，已拒绝执行：${message}`
    );
  }

  try {
    const verifyPluginSourceFallback = async () => {
      const response = await authFilesApi.list();
      const currentFiles = Array.isArray(response.files) ? response.files : [];
      const currentCandidates = currentFiles.filter(
        (file) => readCurrentFileName(file) === item.fileName.trim()
      );
      const physicalSelectorCollides = currentFiles.some(
        (file) =>
          readAuthFileStatusRuntimeId(file) === item.fileName.trim() &&
          readCurrentFileName(file) !== item.fileName.trim()
      );
      if (
        currentCandidates.length !== 1 ||
        physicalSelectorCollides ||
        !deleteActionCoversCurrentFile(currentCandidates, actionReferenceItems, item.fileName) ||
        resolveVerifiedDeleteSelector(currentFiles, currentCandidates, item) !== deleteSelector
      ) {
        throw new Error('认证文件成员、Provider 或账号标识已变化，已拒绝删除');
      }
    };
    const result =
      deleteSelector === item.fileName.trim()
        ? await authFilesApi.deleteFileByName(
            deleteSelector,
            item.fileName,
            undefined,
            deleteIdentityTargets
          )
        : await authFilesApi.deleteFileByName(
            deleteSelector,
            item.fileName,
            verifyPluginSourceFallback,
            deleteIdentityTargets
          );
    const failed = result.failed[0];
    if (failed) {
      return {
        accountKey: item.key,
        action: 'delete',
        fileName: item.fileName,
        displayAccount: item.displayAccount,
        status: 'failed',
        success: false,
        error: failed.error || '删除失败',
      };
    }
    if (result.deleted <= 0) {
      return {
        accountKey: item.key,
        action: 'delete',
        fileName: item.fileName,
        displayAccount: item.displayAccount,
        status: 'failed',
        success: false,
        error: '删除接口未确认认证文件已删除',
      };
    }
    return {
      accountKey: item.key,
      action: 'delete',
      fileName: item.fileName,
      displayAccount: item.displayAccount,
      status: 'success',
      success: true,
      error: '',
    };
  } catch (error) {
    return {
      accountKey: item.key,
      action: 'delete',
      fileName: item.fileName,
      displayAccount: item.displayAccount,
      status: 'failed',
      success: false,
      error: error instanceof Error ? error.message : String(error || '删除失败'),
    };
  }
};

const executeStatusChange = async (
  item: CodexInspectionResultItem,
  disabled: boolean,
  actionMembers: CodexInspectionResultItem[] = []
): Promise<CodexInspectionExecutionOutcome> => {
  let snapshotMembers: AuthFileItem[] = [];
  let singleMember: VerifiedStatusActionMember | null = null;
  let actionGroup: VerifiedStatusActionGroup | null = null;
  try {
    const response = await authFilesApi.list();
    const currentFiles = Array.isArray(response.files) ? response.files : [];
    if (actionMembers.length > 0) {
      actionGroup = resolveVerifiedStatusActionGroup(currentFiles, actionMembers, item.fileName);
      if (!actionGroup) {
        return buildActionValidationOutcome(
          item,
          'failed',
          '认证文件成员、Provider 或账号标识已变化，已拒绝状态修改'
        );
      }
      snapshotMembers = actionGroup.affectedFiles;
      if (actionGroup.affectedFiles.every((file) => (file.disabled === true) === disabled)) {
        return buildActionValidationOutcome(
          item,
          'skipped',
          disabled ? '账号已是禁用状态，未重复执行' : '账号已是启用状态，未重复执行'
        );
      }
    } else {
      const resolution = resolveAuthFileStatusMutationTarget(currentFiles, {
        name: item.fileName,
        runtimeId: item.runtimeId,
        authIndex: item.authIndex,
        provider: item.provider,
        accountId: item.accountId,
        accountSnapshot: readInspectionAccountSnapshot(item),
      });
      const currentFile = resolution.target;
      if (!currentFile || !matchesCurrentActionIdentity(currentFile, item)) {
        return buildActionValidationOutcome(
          item,
          'failed',
          '认证文件不存在、Provider 不匹配或账号标识已变化，已拒绝执行'
        );
      }
      if (resolution.failure !== null || resolution.scope === 'ambiguous') {
        return buildActionValidationOutcome(item, 'failed', STATUS_MUTATION_SCOPE_REASON);
      }
      if (resolution.scope !== 'credential') {
        return buildActionValidationOutcome(item, 'failed', STATUS_MUTATION_SCOPE_REASON);
      }

      snapshotMembers = currentFiles.filter(
        (file) => readCurrentFileName(file) === readCurrentFileName(currentFile)
      );

      if ((currentFile.disabled === true) === disabled) {
        return buildActionValidationOutcome(
          item,
          'skipped',
          disabled ? '账号已是禁用状态，未重复执行' : '账号已是启用状态，未重复执行'
        );
      }
      singleMember = { item, currentFile, resolution };
    }
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error || '未知错误');
    return buildActionValidationOutcome(
      item,
      'failed',
      `执行状态修改前刷新认证文件失败，已拒绝执行：${message}`
    );
  }

  try {
    if (actionGroup) {
      if (actionGroup.sourceMember) {
        await authFilesApi.setVerifiedSourceFileStatus(
          buildStatusActionTarget(
            actionGroup.sourceMember.item,
            actionGroup.sourceMember.currentFile
          ),
          disabled,
          actionGroup.affectedFiles.map(buildDeleteIdentityTarget)
        );
      } else {
        const patchedMembers: PatchedStatusActionMember[] = [];
        for (let index = 0; index < actionGroup.members.length; index++) {
          const member = actionGroup.members[index];
          const target = buildStatusActionTarget(member.item, member.currentFile);
          try {
            const result =
              index === 0
                ? await authFilesApi.setStatusWithFallback(target, disabled, () =>
                    verifyPluginSourceStatusFallback(
                      snapshotMembers,
                      actionMembers,
                      member.item,
                      readAuthFileStatusRuntimeId(member.currentFile)
                    )
                  )
                : await authFilesApi.setStatusWithFallback(target, disabled);
            if (result.mutationScope === 'source-file') break;
            patchedMembers.push({
              item: member.item,
              originalDisabled: member.currentFile.disabled === true,
            });
          } catch (error) {
            const message =
              error instanceof Error ? error.message : String(error || '状态更新失败');
            const rollbackError = await rollbackPatchedStatusActionMembers(patchedMembers);
            throw new Error(
              rollbackError ? `${message}; 已修改凭证回滚失败：${rollbackError}` : message
            );
          }
        }
      }
    } else if (singleMember) {
      const target = buildStatusActionTarget(singleMember.item, singleMember.currentFile);
      await authFilesApi.setStatusWithFallback(target, disabled, () =>
        verifyPluginSourceStatusFallback(
          snapshotMembers,
          [singleMember.item],
          singleMember.item,
          readAuthFileStatusRuntimeId(singleMember.currentFile)
        )
      );
    } else {
      throw new Error('认证凭证状态修改目标未通过校验');
    }
    return {
      accountKey: item.key,
      action: disabled ? 'disable' : 'enable',
      fileName: item.fileName,
      displayAccount: item.displayAccount,
      status: 'success',
      success: true,
      error: '',
    };
  } catch (error) {
    return {
      accountKey: item.key,
      action: disabled ? 'disable' : 'enable',
      fileName: item.fileName,
      displayAccount: item.displayAccount,
      status: 'failed',
      success: false,
      error: error instanceof Error ? error.message : String(error || '状态更新失败'),
    };
  }
};

export const executeCodexInspectionActions = async ({
  settings,
  items,
  referenceItems,
  previousFiles,
  connectionFingerprint,
  source,
  preflightOutcomes: suppliedPreflightOutcomes = [],
  onLog,
  t = identityT,
}: ExecuteCodexInspectionActionsOptions): Promise<CodexInspectionExecutionResult> => {
  const suppliedPreflightAccountKeys = new Set(
    suppliedPreflightOutcomes.map((outcome) => outcome.accountKey)
  );
  const actionReferenceItems = mergeExecutionReferenceItems(items, referenceItems ?? items);
  const plan = planExecutionItems(
    items,
    (referenceItems ?? items).filter((item) => !suppliedPreflightAccountKeys.has(item.key))
  );
  const dedupedItems = plan.items;
  const outcomes: CodexInspectionExecutionOutcome[] = [
    ...suppliedPreflightOutcomes,
    ...plan.preflightOutcomes,
  ];
  let executableItems = dedupedItems;
  let preflightFiles: AuthFileItem[] | null = null;
  const statusGroupMembersByAccountKey = new Map<string, CodexInspectionResultItem[]>();

  if (source === 'manual' || source === 'auto') {
    onLog?.(
      'info',
      t(
        source === 'manual'
          ? 'monitoring.codex_inspection_log_manual_started'
          : 'monitoring.codex_inspection_log_auto_started',
        {
          requested: items.length + suppliedPreflightOutcomes.length,
          actions: dedupedItems.length,
        }
      ),
      {
        requestedCount: items.length + suppliedPreflightOutcomes.length,
        actionCount: dedupedItems.length,
      }
    );
  }

  [...suppliedPreflightOutcomes, ...plan.preflightOutcomes].forEach((outcome) =>
    logExecutionOutcome(outcome, onLog, t)
  );

  if (dedupedItems.length > 0) {
    try {
      const response = await authFilesApi.list();
      const currentFiles = Array.isArray(response.files) ? response.files : [];
      preflightFiles = currentFiles;
      const currentFilesByName = currentFiles.reduce((filesByName, file) => {
        const fileName = readCurrentFileName(file);
        if (!fileName) return filesByName;
        const siblings = filesByName.get(fileName) ?? [];
        siblings.push(file);
        filesByName.set(fileName, siblings);
        return filesByName;
      }, new Map<string, AuthFileItem[]>());
      const statusResolutionByKey = new Map<string, AuthFileStatusMutationResolution>();
      const resolvedStatusItems = new Map<string, ResolvedStatusActionItem>();
      dedupedItems.filter(isStatusExecutionAction).forEach((item) => {
        const resolution = resolveAuthFileStatusMutationTarget(currentFiles, {
          name: item.fileName,
          runtimeId: item.runtimeId,
          authIndex: item.authIndex,
          provider: item.provider,
          accountId: item.accountId,
          accountSnapshot: readInspectionAccountSnapshot(item),
        });
        statusResolutionByKey.set(item.key, resolution);
        if (
          resolution.target &&
          resolution.failure === null &&
          matchesCurrentActionIdentity(resolution.target, item)
        ) {
          resolvedStatusItems.set(item.key, {
            item,
            currentFile: resolution.target,
            resolution,
          });
        }
      });
      const statusActionGroupPlans = buildStatusActionGroupPlans(
        resolvedStatusItems,
        currentFilesByName
      );
      const validatedItems: CodexInspectionResultItem[] = [];
      dedupedItems.forEach((item) => {
        const currentCandidates = currentFilesByName.get(item.fileName.trim()) ?? [];
        const statusResolution = statusResolutionByKey.get(item.key);
        const matchingCurrentFiles = isStatusExecutionAction(item)
          ? []
          : currentCandidates.filter((file) => matchesCurrentActionIdentity(file, item));
        const currentFile = isStatusExecutionAction(item)
          ? statusResolution?.target
          : matchingCurrentFiles.length === 1
            ? matchingCurrentFiles[0]
            : undefined;
        const statusActionGroupPlan = isStatusExecutionAction(item)
          ? statusActionGroupPlans.get(
              currentFile ? readCurrentFileName(currentFile) : item.fileName.trim()
            )
          : undefined;
        let outcome: CodexInspectionExecutionOutcome | null = null;
        if (!currentFile) {
          outcome = buildActionValidationOutcome(
            item,
            'failed',
            '认证文件不存在、Provider 不匹配或账号标识已变化，已拒绝执行'
          );
        } else if (
          isStatusExecutionAction(item) &&
          !matchesCurrentActionIdentity(currentFile, item)
        ) {
          outcome = buildActionValidationOutcome(
            item,
            'failed',
            '认证文件不存在、Provider 不匹配或账号标识已变化，已拒绝执行'
          );
        } else if (
          isStatusExecutionAction(item) &&
          (statusResolution?.failure === 'ambiguous' || statusResolution?.scope === 'ambiguous')
        ) {
          outcome = buildActionValidationOutcome(
            item,
            'needs_review',
            STATUS_MUTATION_SCOPE_REASON
          );
        } else if (
          isStatusExecutionAction(item) &&
          statusActionGroupPlan &&
          statusActionGroupPlan.canonicalKey !== item.key
        ) {
          outcome = buildActionValidationOutcome(
            item,
            'skipped',
            '该认证目标已由另一条结果处理',
            statusActionGroupPlan.canonicalKey
          );
        } else if (
          isStatusExecutionAction(item) &&
          !statusActionGroupPlan &&
          statusResolution?.scope !== 'credential'
        ) {
          outcome = buildActionValidationOutcome(
            item,
            'needs_review',
            STATUS_MUTATION_SCOPE_REASON
          );
        } else if (
          item.action === 'delete' &&
          !deleteActionCoversCurrentFile(currentCandidates, actionReferenceItems, item.fileName)
        ) {
          outcome = buildActionValidationOutcome(item, 'needs_review', FILE_DELETE_COVERAGE_REASON);
        } else if (
          item.action === 'disable' &&
          (statusActionGroupPlan
            ? statusActionGroupPlan.affectedFiles.every((file) => file.disabled === true)
            : currentFile.disabled === true)
        ) {
          outcome = buildActionValidationOutcome(item, 'skipped', '账号已是禁用状态，未重复执行');
        } else if (
          item.action === 'enable' &&
          (statusActionGroupPlan
            ? statusActionGroupPlan.affectedFiles.every((file) => file.disabled !== true)
            : currentFile.disabled !== true)
        ) {
          outcome = buildActionValidationOutcome(item, 'skipped', '账号已是启用状态，未重复执行');
        }
        if (!outcome && currentFile) {
          if (statusActionGroupPlan?.canonicalKey === item.key) {
            statusGroupMembersByAccountKey.set(item.key, statusActionGroupPlan.members);
          }
          validatedItems.push({
            ...item,
            runtimeId: readAuthFileStatusRuntimeId(currentFile) || null,
            disabled: currentFile.disabled === true,
            raw: currentFile,
          });
          return;
        }
        if (!outcome) return;
        outcomes.push(outcome);
        const level =
          outcome.status === 'failed'
            ? 'error'
            : outcome.status === 'needs_review'
              ? 'warning'
              : 'info';
        const messageKey =
          outcome.status === 'failed'
            ? 'monitoring.codex_inspection_log_action_failed'
            : outcome.status === 'needs_review'
              ? 'monitoring.codex_inspection_log_action_needs_review'
              : 'monitoring.codex_inspection_log_action_skipped';
        onLog?.(
          level,
          t(messageKey, {
            account: outcome.displayAccount,
            action: formatExecutionAction(outcome.action, t),
            message: outcome.error,
          }),
          buildExecutionOutcomeLogDetail(outcome)
        );
        return;
      });
      executableItems = validatedItems;
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error || '未知错误');
      dedupedItems.forEach((item) => {
        const outcome = buildActionValidationOutcome(
          item,
          'failed',
          `刷新认证文件失败，已拒绝执行：${message}`
        );
        outcomes.push(outcome);
        onLog?.(
          'error',
          t('monitoring.codex_inspection_log_action_failed', {
            account: outcome.displayAccount,
            action: formatExecutionAction(outcome.action, t),
            message: outcome.error,
          }),
          buildExecutionOutcomeLogDetail(outcome)
        );
      });
      executableItems = [];
    }
  }

  const deleteItems = executableItems.filter((item) => item.action === 'delete');
  const disableItems = executableItems.filter((item) => item.action === 'disable');
  const enableItems = executableItems.filter((item) => item.action === 'enable');

  if (deleteItems.length > 0) {
    const deleteOutcomes = await runConcurrently(deleteItems, settings.deleteWorkers, (item) =>
      executeDelete(item, actionReferenceItems)
    );
    deleteOutcomes.forEach((outcome) => logExecutionOutcome(outcome, onLog, t));
    outcomes.push(...deleteOutcomes);
  }

  if (disableItems.length > 0) {
    const disableOutcomes = await runConcurrently(disableItems, settings.deleteWorkers, (item) =>
      executeStatusChange(item, true, statusGroupMembersByAccountKey.get(item.key))
    );
    if (source === 'auto') {
      const itemByAccountKey = new Map(disableItems.map((item) => [item.key, item] as const));
      for (const outcome of disableOutcomes) {
        if (!outcome.success || outcome.status !== 'success') continue;
        const item = itemByAccountKey.get(outcome.accountKey);
        if (!item) continue;
        const statusGroupMembers = statusGroupMembersByAccountKey.get(outcome.accountKey);
        const persisted = statusGroupMembers
          ? replaceCodexInspectionDisableOwnershipForFile(
              connectionFingerprint,
              item.fileName,
              statusGroupMembers.map((member) => ({
                fileName: member.fileName,
                provider: member.provider,
                authIndex: member.authIndex,
                accountId: member.accountId,
                accountSnapshot: readInspectionAccountSnapshot(member),
              }))
            )
          : recordCodexInspectionDisableOwnership(connectionFingerprint, {
              fileName: item.fileName,
              provider: item.provider,
              authIndex: item.authIndex,
              accountId: item.accountId,
              accountSnapshot: readInspectionAccountSnapshot(item),
            });
        if (persisted) continue;

        const rollbackOutcome = await executeStatusChange(item, false, statusGroupMembers);
        const rolledBack =
          rollbackOutcome.success &&
          (rollbackOutcome.status === 'success' || rollbackOutcome.status === 'skipped');
        outcome.status = 'failed';
        outcome.success = false;
        outcome.error = rolledBack
          ? '自动禁用所有权保存失败，禁用操作已回滚'
          : `自动禁用所有权保存失败，且禁用回滚失败：${
              rollbackOutcome.error || '未知错误'
            }；请到认证文件管理中手动恢复`;
      }
    }
    disableOutcomes.forEach((outcome) => logExecutionOutcome(outcome, onLog, t));
    outcomes.push(...disableOutcomes);
  }

  if (enableItems.length > 0) {
    const enableOutcomes = await runConcurrently(enableItems, settings.deleteWorkers, (item) =>
      executeStatusChange(item, false, statusGroupMembersByAccountKey.get(item.key))
    );
    enableOutcomes.forEach((outcome) => logExecutionOutcome(outcome, onLog, t));
    outcomes.push(...enableOutcomes);
  }

  const itemByAccountKey = new Map(dedupedItems.map((item) => [item.key, item] as const));
  outcomes.forEach((outcome) => {
    if (!outcome.success || outcome.status !== 'success') return;
    const item = itemByAccountKey.get(outcome.accountKey);
    if (!item) return;
    const statusGroupMembers = statusGroupMembersByAccountKey.get(outcome.accountKey);
    if (outcome.action === 'disable' && source === 'auto') {
      return;
    }
    if (outcome.action === 'delete' || statusGroupMembers) {
      clearCodexInspectionDisableOwnershipForFile(connectionFingerprint, item.fileName);
      return;
    }
    clearCodexInspectionDisableOwnership(connectionFingerprint, {
      fileName: item.fileName,
      provider: item.provider,
      authIndex: item.authIndex,
      accountId: item.accountId,
      accountSnapshot: readInspectionAccountSnapshot(item),
    });
  });

  let refreshedFiles = preflightFiles ?? previousFiles;
  let refreshError = '';
  if (deleteItems.length + disableItems.length + enableItems.length > 0) {
    try {
      const response = await authFilesApi.list();
      refreshedFiles = Array.isArray(response.files) ? response.files : previousFiles;
    } catch (error) {
      refreshError = error instanceof Error ? error.message : String(error || '刷新账号列表失败');
      onLog?.(
        'warning',
        t('monitoring.codex_inspection_log_refresh_failed', { message: refreshError }),
        { error: refreshError }
      );
    }
  }

  if (source === 'manual') {
    const summary = summarizeExecutionOutcomes(outcomes);
    onLog?.(
      summary.failed > 0 || summary.needs_review > 0 || refreshError ? 'warning' : 'success',
      t('monitoring.codex_inspection_log_manual_completed', {
        success: summary.success,
        skipped: summary.skipped,
        review: summary.needs_review,
        failed: summary.failed,
      }),
      {
        successCount: summary.success,
        failedCount: summary.failed,
        skippedCount: summary.skipped,
        needsReviewCount: summary.needs_review,
        refreshFailed: Boolean(refreshError),
        ...(refreshError ? { refreshError } : {}),
      }
    );
  }

  return {
    outcomes,
    refreshedFiles,
    refreshError,
  };
};
