/**
 * 认证文件与 OAuth 排除模型相关 API
 */

import { apiClient, createScopedApiRequestConfig, type ApiClientRequestScope } from './client';
import type { AuthFilesResponse } from '@/types/authFile';
import type { AuthFileItem, OAuthModelAliasEntry } from '@/types';
import {
  readAuthFileStatusAccountId,
  readAuthFileStatusAccountIdInvalid,
  readAuthFileStatusAccountSnapshot,
  readAuthFileStatusCodexMember,
  readAuthFileStatusCodexMemberInvalid,
  readAuthFileStatusProvider,
  readAuthFileStatusRuntimeId,
  normalizeCodexMemberSnapshot,
} from '@/utils/authFileStatusMutation';
import { sha256RawTextHex } from '@/utils/apiKeyHash';
import { parseTimestampMs } from '@/utils/timestamp';

type StatusError = { status?: number };
export type AuthFilesApiRequestScope = ApiClientRequestScope;
type AuthFileStatusResponse = { status: string; disabled: boolean };
export type AuthFileStatusMutationResult = AuthFileStatusResponse & {
  mutationScope: 'credential' | 'source-file';
};
export type AuthFileStatusTarget = {
  name: string;
  runtimeId?: string | null;
  authIndex?: string | number | null;
  provider?: string | null;
  accountId?: string | null;
  accountSnapshot?: string | null;
};
export type AuthFileDeleteIdentityTarget = AuthFileStatusTarget;
export type AuthFilePluginSourceFallbackVerifier = () => Promise<void>;
export type AuthFileStatusPluginSourceFallbackVerifier = () => Promise<AuthFileStatusTarget[]>;
type AuthFileEntry = AuthFilesResponse['files'][number];
type AuthFileJsonValue = Record<string, unknown> | Record<string, unknown>[];
type AuthFileModelApiItem = {
  id: string;
  display_name?: string;
  type?: string;
  owned_by?: string;
};
export type AuthFileFieldsPatch = {
  expired?: string;
  last_refresh?: string;
  prefix?: string;
  proxy_url?: string;
  proxyUrl?: null;
  'proxy-url'?: null;
  base_url?: string;
  baseUrl?: null;
  'base-url'?: null;
  websockets?: boolean;
  using_api?: boolean;
  usingApi?: null;
  'using-api'?: null;
  headers?: Record<string, string>;
  priority?: number;
  weight?: number | null;
  note?: string;
  'excluded-models'?: string[] | null;
  excluded_models?: string[] | null;
  excludedModels?: null;
  disable_cooling?: boolean | null;
  disableCooling?: null;
  'disable-cooling'?: null;
  request_retry?: number | null;
  'request-retry'?: null;
  requestRetry?: null;
  cloak_mode?: string;
  cloakMode?: null;
  'cloak-mode'?: null;
  cloak_strict_mode?: string;
  cloakStrictMode?: null;
  'cloak-strict-mode'?: null;
  cloak_sensitive_words?: string;
  cloakSensitiveWords?: null;
  'cloak-sensitive-words'?: null;
  cloak_cache_user_id?: string;
  cloakCacheUserId?: null;
  'cloak-cache-user-id'?: null;
  tool_prefix_disabled?: boolean;
  'tool-prefix-disabled'?: null;
  toolPrefixDisabled?: null;
};
export type AuthFilePatchAuthIndex = string | number;
type AuthFileBatchFailure = { name: string; error: string };
type AuthFileBatchUploadResponse = {
  status?: string;
  uploaded?: number;
  files?: unknown;
  failed?: unknown;
};
type AuthFileBatchDeleteResponse = {
  status?: string;
  deleted?: number;
  files?: unknown;
  failed?: unknown;
};
type AuthFileBatchUploadResult = {
  status: string;
  uploaded: number;
  files: string[];
  failed: AuthFileBatchFailure[];
};
type AuthFileBatchDeleteResult = {
  status: string;
  deleted: number;
  files: string[];
  failed: AuthFileBatchFailure[];
};

const AUTH_FILE_PHYSICAL_NAME_HEADER = 'X-CPAMP-Auth-File-Physical-Name';
const AUTH_FILE_DELETE_IDENTITIES_HEADER = 'X-CPAMP-Auth-File-Delete-Identities';
const AUTH_FILE_MUTATION_IDENTITY_HEADER = 'X-CPAMP-Auth-File-Mutation-Identity';
const AUTH_FILE_WRITE_IDENTITIES_HEADER = 'X-CPAMP-Auth-File-Write-Identities';
const AUTH_FILE_WRITE_CONTENT_SHA256_HEADER = 'X-CPAMP-Auth-File-Write-Content-SHA256';
const CPA_PLUGIN_VIRTUAL_MUTATION_CONFLICT =
  'plugin virtual auth cannot be modified directly; edit or delete the source auth file';

export const AUTH_FILE_INVALID_JSON_OBJECT_ERROR = 'AUTH_FILE_INVALID_JSON_OBJECT';

const getStatusCode = (err: unknown): number | undefined => {
  if (!err || typeof err !== 'object') return undefined;
  if ('status' in err) return (err as StatusError).status;
  return undefined;
};

const normalizeAuthFileModelItems = (value: unknown): AuthFileModelApiItem[] => {
  if (!Array.isArray(value)) return [];
  const seen = new Set<string>();
  const result: AuthFileModelApiItem[] = [];
  value.forEach((entry) => {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) return;
    const raw = entry as Record<string, unknown>;
    const id = typeof raw.id === 'string' ? raw.id.trim() : '';
    if (!id) return;
    const key = id.toLowerCase();
    if (seen.has(key)) return;
    seen.add(key);
    const item: AuthFileModelApiItem = { id };
    if (typeof raw.display_name === 'string' && raw.display_name.trim()) {
      item.display_name = raw.display_name.trim();
    }
    if (typeof raw.type === 'string' && raw.type.trim()) item.type = raw.type.trim();
    if (typeof raw.owned_by === 'string' && raw.owned_by.trim()) {
      item.owned_by = raw.owned_by.trim();
    }
    result.push(item);
  });
  return result;
};

const isCPAPluginVirtualMutationConflict = (err: unknown): boolean =>
  getStatusCode(err) === 409 &&
  err instanceof Error &&
  err.message.trim() === CPA_PLUGIN_VIRTUAL_MUTATION_CONFLICT;

const normalizeRequestedAuthFileNames = (names: string[]): string[] => {
  const seen = new Set<string>();
  const normalized: string[] = [];

  names.forEach((name) => {
    const trimmed = String(name ?? '').trim();
    if (!trimmed || seen.has(trimmed)) return;
    seen.add(trimmed);
    normalized.push(trimmed);
  });

  return normalized;
};

const normalizeBatchFileNames = (value: unknown): string[] => {
  if (!Array.isArray(value)) return [];
  return normalizeRequestedAuthFileNames(value.map((item) => String(item ?? '')));
};

const normalizeBatchFailures = (value: unknown): AuthFileBatchFailure[] => {
  if (!Array.isArray(value)) return [];

  return value.reduce<AuthFileBatchFailure[]>((result, item) => {
    if (!item || typeof item !== 'object') return result;
    const entry = item as Record<string, unknown>;
    const name = String(entry.name ?? '').trim();
    const error =
      typeof entry.error === 'string'
        ? entry.error.trim()
        : typeof entry.message === 'string'
          ? entry.message.trim()
          : '';

    if (!name && !error) return result;
    result.push({ name, error: error || 'Unknown error' });
    return result;
  }, []);
};

const deriveSuccessfulFileNames = (
  requestedNames: string[],
  failed: AuthFileBatchFailure[]
): string[] => {
  const failedNames = new Set(failed.map((entry) => entry.name.trim()).filter(Boolean));

  if (failedNames.size === 0) {
    return [...requestedNames];
  }

  return requestedNames.filter((name) => !failedNames.has(name));
};

const normalizeBatchUploadResponse = (
  payload: AuthFileBatchUploadResponse | undefined,
  requestedNames: string[]
): AuthFileBatchUploadResult => {
  const failed = normalizeBatchFailures(payload?.failed);
  const uploadedFilesFromPayload = normalizeBatchFileNames(payload?.files);
  const uploaded =
    typeof payload?.uploaded === 'number'
      ? payload.uploaded
      : uploadedFilesFromPayload.length > 0
        ? uploadedFilesFromPayload.length
        : requestedNames.length === 1 && failed.length === 0
          ? 1
          : 0;

  let uploadedFiles = uploadedFilesFromPayload;
  if (uploadedFiles.length === 0 && uploaded > 0) {
    if (failed.length === 0 && uploaded === requestedNames.length) {
      uploadedFiles = [...requestedNames];
    } else {
      const derivedNames = deriveSuccessfulFileNames(requestedNames, failed);
      if (derivedNames.length === uploaded) {
        uploadedFiles = derivedNames;
      }
    }
  }

  return {
    status:
      typeof payload?.status === 'string' ? payload.status : failed.length > 0 ? 'partial' : 'ok',
    uploaded,
    files: uploadedFiles,
    failed,
  };
};

const readUploadErrorMessage = (err: unknown): string => {
  if (err instanceof Error && err.message.trim()) return err.message;
  if (typeof err === 'string' && err.trim()) return err.trim();
  return 'Upload failed';
};

const uploadSingleAuthFile = async (
  file: File,
  headers: Record<string, string> = {},
  requestScope?: AuthFilesApiRequestScope
): Promise<AuthFileBatchUploadResult> => {
  const formData = new FormData();
  formData.append('file', file, file.name);
  const scopedRequestConfig = requestScope ? createScopedApiRequestConfig(requestScope) : null;
  const requestConfig = scopedRequestConfig
    ? {
        ...scopedRequestConfig,
        headers: { ...scopedRequestConfig.headers, ...headers },
      }
    : Object.keys(headers).length > 0
      ? { headers }
      : undefined;
  const payload = requestConfig
    ? await apiClient.postForm<AuthFileBatchUploadResponse>('/auth-files', formData, requestConfig)
    : await apiClient.postForm<AuthFileBatchUploadResponse>('/auth-files', formData);
  return normalizeBatchUploadResponse(payload, [file.name]);
};

const mergeBatchUploadResults = (
  results: AuthFileBatchUploadResult[],
  requestedNames: string[]
): AuthFileBatchUploadResult => {
  const uploaded = results.reduce((total, result) => total + Math.max(0, result.uploaded), 0);
  const files = results.flatMap((result) => result.files);
  const failed = results.flatMap((result) => result.failed);
  const hasFailureStatus = results.some((result) => {
    const status = result.status.trim().toLowerCase();
    return status === 'error' || status === 'failed' || status === 'partial';
  });

  return {
    status:
      failed.length > 0 || hasFailureStatus || uploaded < requestedNames.length
        ? uploaded > 0
          ? 'partial'
          : 'error'
        : 'ok',
    uploaded,
    files,
    failed,
  };
};

const normalizeBatchDeleteResponse = (
  payload: AuthFileBatchDeleteResponse | undefined,
  requestedNames: string[]
): AuthFileBatchDeleteResult => {
  const failed = normalizeBatchFailures(payload?.failed);
  const deletedFilesFromPayload = normalizeBatchFileNames(payload?.files);
  const deleted =
    typeof payload?.deleted === 'number'
      ? payload.deleted
      : deletedFilesFromPayload.length > 0
        ? deletedFilesFromPayload.length
        : requestedNames.length === 1 && failed.length === 0
          ? 1
          : 0;

  let deletedFiles = deletedFilesFromPayload;
  if (deletedFiles.length === 0 && deleted > 0) {
    if (failed.length === 0 && deleted === requestedNames.length) {
      deletedFiles = [...requestedNames];
    } else {
      const derivedNames = deriveSuccessfulFileNames(requestedNames, failed);
      if (derivedNames.length === deleted) {
        deletedFiles = derivedNames;
      }
    }
  }

  return {
    status:
      typeof payload?.status === 'string' ? payload.status : failed.length > 0 ? 'partial' : 'ok',
    deleted,
    files: deletedFiles,
    failed,
  };
};

const normalizeSingleDeleteResponse = (
  payload: AuthFileBatchDeleteResponse | undefined,
  selector: string,
  physicalName: string
): AuthFileBatchDeleteResult => {
  const normalizedSelector = selector.trim();
  const normalizedPhysicalName = physicalName.trim() || normalizedSelector;
  const result = normalizeBatchDeleteResponse(payload, [normalizedPhysicalName]);
  return {
    ...result,
    files: result.files.map((name) =>
      name === normalizedSelector ? normalizedPhysicalName : name
    ),
    failed: result.failed.map((failure) =>
      failure.name === normalizedSelector ? { ...failure, name: normalizedPhysicalName } : failure
    ),
  };
};

const normalizeAuthFileIdentityTargets = (identityTargets: AuthFileStatusTarget[]) =>
  identityTargets
    .map((target) => {
      const name = String(target.name ?? '').trim();
      if (!name) return null;
      const runtimeId = String(target.runtimeId ?? '').trim();
      const authIndex =
        target.authIndex === null || target.authIndex === undefined
          ? ''
          : String(target.authIndex).trim();
      const provider = String(target.provider ?? '').trim();
      const accountId = String(target.accountId ?? '').trim();
      const accountSnapshot = String(target.accountSnapshot ?? '').trim();
      return {
        name,
        ...(runtimeId ? { runtimeId } : {}),
        ...(authIndex ? { authIndex } : {}),
        ...(provider ? { provider } : {}),
        ...(accountId ? { accountId } : {}),
        ...(accountSnapshot ? { accountSnapshot } : {}),
      };
    })
    .filter((target): target is NonNullable<typeof target> => target !== null);

const encodeAuthFileIdentityTargets = (identityTargets: AuthFileStatusTarget[]): string => {
  const normalized = normalizeAuthFileIdentityTargets(identityTargets);
  return normalized.length > 0 ? encodeURIComponent(JSON.stringify(normalized)) : '';
};

const buildAuthFileIdentityHeaders = (
  headerName: string,
  identityTargets: AuthFileStatusTarget[]
): Record<string, string> => {
  const encoded = encodeAuthFileIdentityTargets(identityTargets);
  if (!encoded) {
    throw new Error('auth file identity snapshot is required');
  }
  return { [headerName]: encoded };
};

const deleteAuthFileBySelector = async (
  selector: string,
  physicalName: string,
  identityTargets: AuthFileDeleteIdentityTarget[] = [],
  requestScope?: AuthFilesApiRequestScope
): Promise<AuthFileBatchDeleteResult> => {
  const encodedDeleteIdentities = encodeAuthFileIdentityTargets(identityTargets);
  const headers: Record<string, string> = {
    [AUTH_FILE_PHYSICAL_NAME_HEADER]: physicalName,
  };
  if (encodedDeleteIdentities) {
    headers[AUTH_FILE_DELETE_IDENTITIES_HEADER] = encodedDeleteIdentities;
  }
  const payload = await apiClient.delete<AuthFileBatchDeleteResponse>(
    `/auth-files?name=${encodeURIComponent(selector)}`,
    requestScope
      ? (() => {
          const scopedRequestConfig = createScopedApiRequestConfig(requestScope);
          return {
            ...scopedRequestConfig,
            headers: { ...scopedRequestConfig.headers, ...headers },
          };
        })()
      : { headers }
  );
  return normalizeSingleDeleteResponse(payload, selector, physicalName);
};

const readTextField = (entry: AuthFileEntry, key: string): string => {
  const value = entry[key];
  return typeof value === 'string' ? value.trim() : '';
};

const readAuthIndexField = (entry: AuthFileEntry): string => {
  const value = entry.authIndex ?? entry['auth_index'] ?? entry['auth-index'];
  if (typeof value === 'number' && Number.isFinite(value)) return String(value);
  if (typeof value === 'string') return value.trim();
  return '';
};

const getAuthFileDedupeKey = (entry: AuthFileEntry): string => {
  const name = readTextField(entry, 'name');
  const authIndex = readAuthIndexField(entry);
  const runtimeId = readAuthFileStatusRuntimeId(entry);
  if (name && authIndex && runtimeId) {
    return `${name}\u0000${authIndex}\u0000${runtimeId}`;
  }
  if (name && authIndex) return `${name}\u0000${authIndex}`;
  return name || JSON.stringify(entry);
};

const readDateField = (entry: AuthFileEntry): number => {
  const candidates = [entry['modtime'], entry.modified, entry['updated_at'], entry['last_refresh']];

  for (const value of candidates) {
    if (typeof value === 'number' && Number.isFinite(value)) {
      return value < 1e12 ? value * 1000 : value;
    }
    if (typeof value === 'string') {
      const trimmed = value.trim();
      if (!trimmed) continue;
      const asNumber = Number(trimmed);
      if (Number.isFinite(asNumber)) {
        return asNumber < 1e12 ? asNumber * 1000 : asNumber;
      }
      const parsed = parseTimestampMs(trimmed);
      if (!Number.isNaN(parsed)) {
        return parsed;
      }
    }
  }

  return 0;
};

const isRuntimeOnlyEntry = (entry: AuthFileEntry): boolean => {
  const value = entry['runtime_only'] ?? entry.runtimeOnly;
  if (typeof value === 'boolean') return value;
  if (typeof value === 'string') return value.trim().toLowerCase() === 'true';
  return false;
};

const hasMeaningfulValue = (value: unknown): boolean => {
  if (value == null) return false;
  if (typeof value === 'string') return value.trim().length > 0;
  if (Array.isArray(value)) return value.length > 0;
  return true;
};

const countMeaningfulFields = (entry: AuthFileEntry): number =>
  Object.values(entry).reduce<number>(
    (count, value) => count + (hasMeaningfulValue(value) ? 1 : 0),
    0
  );

const authFilePriorityScore = (entry: AuthFileEntry): number => {
  let score = 0;
  if (readTextField(entry, 'source').toLowerCase() === 'file') score += 32;
  if (readTextField(entry, 'path')) score += 16;
  if (!isRuntimeOnlyEntry(entry)) score += 8;
  if (entry.disabled !== true) score += 4;
  if (readDateField(entry) > 0) score += 2;
  return score;
};

const compareAuthFileEntries = (left: AuthFileEntry, right: AuthFileEntry): number => {
  const scoreDiff = authFilePriorityScore(right) - authFilePriorityScore(left);
  if (scoreDiff !== 0) return scoreDiff;

  const dateDiff = readDateField(right) - readDateField(left);
  if (dateDiff !== 0) return dateDiff;

  const fieldDiff = countMeaningfulFields(right) - countMeaningfulFields(left);
  if (fieldDiff !== 0) return fieldDiff;

  return 0;
};

const mergeAuthFileEntries = (entries: AuthFileEntry[]): AuthFileEntry => {
  const [primary, ...rest] = [...entries].sort(compareAuthFileEntries);
  const merged: AuthFileEntry = { ...primary };

  rest.forEach((entry) => {
    Object.entries(entry).forEach(([key, value]) => {
      if (!hasMeaningfulValue(merged[key]) && hasMeaningfulValue(value)) {
        merged[key] = value;
      }
    });
  });

  return merged;
};

const dedupeAuthFilesResponse = (payload: AuthFilesResponse): AuthFilesResponse => {
  const files = Array.isArray(payload?.files) ? payload.files : [];
  const grouped = new Map<string, AuthFileEntry[]>();

  files.forEach((entry) => {
    const key = getAuthFileDedupeKey(entry);
    const bucket = grouped.get(key);
    if (bucket) {
      bucket.push(entry);
      return;
    }
    grouped.set(key, [entry]);
  });

  const normalizedFiles = Array.from(grouped.values()).map(mergeAuthFileEntries);
  normalizedFiles.sort((left, right) => {
    const nameDiff = readTextField(left, 'name').localeCompare(
      readTextField(right, 'name'),
      undefined,
      {
        sensitivity: 'accent',
      }
    );
    if (nameDiff !== 0) return nameDiff;

    return readAuthIndexField(left).localeCompare(readAuthIndexField(right), undefined, {
      numeric: true,
      sensitivity: 'accent',
    });
  });

  return {
    ...payload,
    files: normalizedFiles,
    total: normalizedFiles.length,
  };
};

const parseAuthFileJsonObject = (rawText: string): Record<string, unknown> => {
  const trimmed = rawText.trim();

  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed) as unknown;
  } catch {
    throw new Error(AUTH_FILE_INVALID_JSON_OBJECT_ERROR);
  }

  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error(AUTH_FILE_INVALID_JSON_OBJECT_ERROR);
  }

  return { ...(parsed as Record<string, unknown>) };
};

const parseAuthFileJsonValue = (rawText: string): AuthFileJsonValue => {
  const trimmed = rawText.trim();

  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed) as unknown;
  } catch {
    throw new Error(AUTH_FILE_INVALID_JSON_OBJECT_ERROR);
  }

  if (!parsed || typeof parsed !== 'object') {
    throw new Error(AUTH_FILE_INVALID_JSON_OBJECT_ERROR);
  }

  if (Array.isArray(parsed)) {
    if (!parsed.every((item) => item && typeof item === 'object' && !Array.isArray(item))) {
      throw new Error(AUTH_FILE_INVALID_JSON_OBJECT_ERROR);
    }
    return parsed.map((item) => ({ ...(item as Record<string, unknown>) }));
  }

  return { ...(parsed as Record<string, unknown>) };
};

const normalizePatchAuthIndex = (value: unknown): string => {
  if (value === undefined || value === null) return '';
  return String(value).trim();
};

const normalizePatchProviderIdentity = (value: unknown): string => {
  const provider = String(value ?? '')
    .trim()
    .toLowerCase()
    .replace(/_/g, '-');
  if (provider === 'x-ai' || provider === 'grok') return 'xai';
  return provider === 'gemini' ? 'gemini-cli' : provider;
};

const readPatchRecordAuthIndex = (record: Record<string, unknown>, arrayIndex?: number): string => {
  const explicit = normalizePatchAuthIndex(
    record.authIndex ?? record['auth_index'] ?? record['auth-index']
  );
  if (explicit) return explicit;
  return arrayIndex === undefined ? '' : String(arrayIndex);
};

export const applyAuthFileFieldsPatchToRecord = (
  record: Record<string, unknown>,
  fields: AuthFileFieldsPatch
): Record<string, unknown> => {
  const next = { ...record };

  const applyTrimmedString = (key: string, value: string | undefined) => {
    if (value === undefined) return;
    const normalized = value.trim();
    if (normalized) {
      next[key] = normalized;
    } else {
      delete next[key];
    }
  };

  if (fields.expired !== undefined) {
    const value = fields.expired.trim();
    if (value) {
      next.expired = value;
    } else {
      delete next.expired;
    }
  }

  if (fields.last_refresh !== undefined) {
    const value = fields.last_refresh.trim();
    if (value) {
      next.last_refresh = value;
    } else {
      delete next.last_refresh;
    }
  }

  if (fields.prefix !== undefined) {
    const value = fields.prefix.trim();
    if (value) {
      next.prefix = value;
    } else {
      delete next.prefix;
    }
  }

  if (fields.proxy_url !== undefined) {
    const value = fields.proxy_url.trim();
    if (value) {
      next.proxy_url = value;
    } else {
      delete next.proxy_url;
    }
  }
  if (fields.proxyUrl === null) delete next.proxyUrl;
  if (fields['proxy-url'] === null) delete next['proxy-url'];

  applyTrimmedString('base_url', fields.base_url);
  if (fields.baseUrl === null) delete next.baseUrl;
  if (fields['base-url'] === null) delete next['base-url'];

  if (fields.priority !== undefined) {
    if (fields.priority === 0) {
      delete next.priority;
    } else {
      next.priority = fields.priority;
    }
  }

  if (fields.weight !== undefined) {
    if (fields.weight === null) {
      delete next.weight;
    } else {
      next.weight = fields.weight;
    }
  }

  if (fields.note !== undefined) {
    const value = fields.note.trim();
    if (value) {
      next.note = value;
    } else {
      delete next.note;
    }
  }

  const excludedModelsPatch =
    fields['excluded-models'] !== undefined ? fields['excluded-models'] : fields.excluded_models;
  if (excludedModelsPatch !== undefined) {
    if (excludedModelsPatch === null) {
      delete next['excluded-models'];
      delete next.excluded_models;
    } else {
      const models = excludedModelsPatch.map((model) => model.trim()).filter(Boolean);
      if (models.length > 0) {
        next['excluded-models'] = models;
      } else {
        delete next['excluded-models'];
      }
      delete next.excluded_models;
    }
  }
  if (fields.excludedModels === null) delete next.excludedModels;

  if (fields.disable_cooling !== undefined) {
    if (fields.disable_cooling === null) {
      delete next.disable_cooling;
    } else {
      next.disable_cooling = fields.disable_cooling;
    }
    delete next.disableCooling;
    delete next['disable-cooling'];
  }
  if (fields.disableCooling === null) delete next.disableCooling;
  if (fields['disable-cooling'] === null) delete next['disable-cooling'];

  if (fields.request_retry !== undefined) {
    if (fields.request_retry === null) {
      delete next.request_retry;
    } else {
      next.request_retry = fields.request_retry;
    }
  }
  if (fields['request-retry'] === null) delete next['request-retry'];
  if (fields.requestRetry === null) delete next.requestRetry;

  applyTrimmedString('cloak_mode', fields.cloak_mode);
  if (fields.cloakMode === null) delete next.cloakMode;
  if (fields['cloak-mode'] === null) delete next['cloak-mode'];
  applyTrimmedString('cloak_strict_mode', fields.cloak_strict_mode);
  if (fields.cloakStrictMode === null) delete next.cloakStrictMode;
  if (fields['cloak-strict-mode'] === null) delete next['cloak-strict-mode'];
  applyTrimmedString('cloak_sensitive_words', fields.cloak_sensitive_words);
  if (fields.cloakSensitiveWords === null) delete next.cloakSensitiveWords;
  if (fields['cloak-sensitive-words'] === null) delete next['cloak-sensitive-words'];
  applyTrimmedString('cloak_cache_user_id', fields.cloak_cache_user_id);
  if (fields.cloakCacheUserId === null) delete next.cloakCacheUserId;
  if (fields['cloak-cache-user-id'] === null) delete next['cloak-cache-user-id'];

  if (fields.tool_prefix_disabled !== undefined) {
    if (fields.tool_prefix_disabled) {
      next.tool_prefix_disabled = true;
    } else {
      delete next.tool_prefix_disabled;
    }
  }
  if (fields['tool-prefix-disabled'] === null) delete next['tool-prefix-disabled'];
  if (fields.toolPrefixDisabled === null) delete next.toolPrefixDisabled;

  if (fields.headers !== undefined) {
    const currentHeaders =
      next.headers && typeof next.headers === 'object' && !Array.isArray(next.headers)
        ? { ...(next.headers as Record<string, unknown>) }
        : {};
    Object.entries(fields.headers).forEach(([name, rawValue]) => {
      const headerName = name.trim();
      if (!headerName) return;
      const value = rawValue.trim();
      if (value) {
        currentHeaders[headerName] = value;
      } else {
        delete currentHeaders[headerName];
      }
    });
    if (Object.keys(currentHeaders).length > 0) {
      next.headers = currentHeaders;
    } else {
      delete next.headers;
    }
  }

  if (fields.websockets !== undefined) {
    delete next.websocket;
    next.websockets = fields.websockets;
  }

  if (fields.using_api !== undefined) {
    next.using_api = fields.using_api;
  }
  if (fields.usingApi === null) delete next.usingApi;
  if (fields['using-api'] === null) delete next['using-api'];

  return next;
};

const patchAuthFileJsonValueByAuthIndexes = (
  value: AuthFileJsonValue,
  authIndexes: AuthFilePatchAuthIndex[],
  fields: AuthFileFieldsPatch
): AuthFileJsonValue => {
  const targetIndexes = new Set(
    authIndexes.map(normalizePatchAuthIndex).filter((authIndex) => authIndex)
  );

  if (targetIndexes.size === 0) {
    return Array.isArray(value)
      ? value.map((record) => applyAuthFileFieldsPatchToRecord(record, fields))
      : applyAuthFileFieldsPatchToRecord(value, fields);
  }

  if (!Array.isArray(value)) {
    const recordAuthIndex = readPatchRecordAuthIndex(value);
    if (recordAuthIndex && !targetIndexes.has(recordAuthIndex)) {
      throw new Error('Auth index not found');
    }
    return applyAuthFileFieldsPatchToRecord(value, fields);
  }

  const matchedIndexes = new Set<string>();
  const nextValue = value.map((record, index) => {
    const recordAuthIndex = readPatchRecordAuthIndex(record, index);
    if (!recordAuthIndex || !targetIndexes.has(recordAuthIndex)) {
      return record;
    }
    matchedIndexes.add(recordAuthIndex);
    return applyAuthFileFieldsPatchToRecord(record, fields);
  });

  if (matchedIndexes.size !== targetIndexes.size) {
    throw new Error('Auth index not found');
  }

  return nextValue;
};

const authFileRecordMatchesPatchTarget = (
  record: Record<string, unknown>,
  target: AuthFileStatusTarget
): boolean => {
  const authFileRecord = { name: '', ...record } as AuthFileItem;
  const credentialLocatorPresent = Boolean(
    String(target.runtimeId ?? '').trim() || normalizePatchAuthIndex(target.authIndex)
  );
  const expectedProvider = normalizePatchProviderIdentity(target.provider);
  const actualProvider = normalizePatchProviderIdentity(readAuthFileStatusProvider(authFileRecord));
  if (expectedProvider && actualProvider && actualProvider !== expectedProvider) {
    return false;
  }
  if (actualProvider === 'codex' && readAuthFileStatusAccountIdInvalid(authFileRecord)) {
    return false;
  }

  const expectedAccountId = String(target.accountId ?? '').trim();
  if (expectedAccountId) {
    if (actualProvider === 'codex') {
      const currentWorkspace = readAuthFileStatusAccountId(authFileRecord);
      if (currentWorkspace && currentWorkspace !== expectedAccountId) return false;
      const expectedSnapshot = String(target.accountSnapshot ?? '').trim();
      const expectedMember = normalizeCodexMemberSnapshot(expectedSnapshot);
      if (!expectedMember) {
        // A Workspace-only target must still carry a credential locator. The
        // raw source record may be one of several Team members in one file.
        return credentialLocatorPresent && !readAuthFileStatusCodexMemberInvalid(authFileRecord);
      }
      if (readAuthFileStatusCodexMemberInvalid(authFileRecord)) return false;
      const currentMember = readAuthFileStatusCodexMember(authFileRecord);
      return !currentMember || currentMember === expectedMember;
    }
    if (readAuthFileStatusAccountId(authFileRecord) !== expectedAccountId) return false;
    return true;
  }

  const expectedAccountSnapshot = String(target.accountSnapshot ?? '').trim();
  if (expectedAccountSnapshot) {
    if (actualProvider === 'codex') {
      const expectedMember = normalizeCodexMemberSnapshot(expectedAccountSnapshot);
      if (!credentialLocatorPresent || !expectedMember) return false;
      if (readAuthFileStatusCodexMemberInvalid(authFileRecord)) return false;
      const currentMember = readAuthFileStatusCodexMember(authFileRecord);
      return !currentMember || currentMember === expectedMember;
    }
    return readAuthFileStatusAccountSnapshot(authFileRecord) === expectedAccountSnapshot;
  }

  return true;
};

const verifyAuthFileJsonPatchTargets = (
  value: AuthFileJsonValue,
  targets: AuthFileStatusTarget[]
): void => {
  const normalizedTargets = normalizeAuthFileIdentityTargets(targets);
  if (normalizedTargets.length === 0) {
    throw new Error('auth file patch identity snapshot is required');
  }

  if (!Array.isArray(value)) {
    if (normalizedTargets.length !== 1) throw new Error('Auth file patch target changed');
    const target = normalizedTargets[0];
    const recordAuthIndex = readPatchRecordAuthIndex(value);
    const expectedAuthIndex = normalizePatchAuthIndex(target.authIndex);
    if (
      (recordAuthIndex && expectedAuthIndex && recordAuthIndex !== expectedAuthIndex) ||
      !authFileRecordMatchesPatchTarget(value, target)
    ) {
      throw new Error('Auth file patch target changed');
    }
    return;
  }

  normalizedTargets.forEach((target) => {
    const expectedAuthIndex = normalizePatchAuthIndex(target.authIndex);
    if (!expectedAuthIndex) throw new Error('Auth file patch target changed');
    const matches = value.filter(
      (record, index) => readPatchRecordAuthIndex(record, index) === expectedAuthIndex
    );
    if (matches.length !== 1 || !authFileRecordMatchesPatchTarget(matches[0], target)) {
      throw new Error('Auth file patch target changed');
    }
  });
};

const saveAuthFileText = async (
  name: string,
  text: string,
  headers: Record<string, string> = {},
  requestScope?: AuthFilesApiRequestScope
) => {
  const file = new File([text], name, { type: 'application/json' });
  const result = await uploadSingleAuthFile(file, headers, requestScope);
  const normalizedStatus = result.status.trim().toLowerCase();
  const hasExplicitFailureStatus =
    normalizedStatus === 'error' || normalizedStatus === 'failed' || normalizedStatus === 'partial';
  if (hasExplicitFailureStatus || result.failed.length > 0 || result.uploaded === 0) {
    const failure = result.failed[0];
    throw new Error(failure?.error || 'Upload failed');
  }
};

export const isAuthFileInvalidJsonObjectError = (err: unknown): boolean =>
  err instanceof Error && err.message === AUTH_FILE_INVALID_JSON_OBJECT_ERROR;

const normalizeOauthExcludedModels = (payload: unknown): Record<string, string[]> => {
  if (!payload || typeof payload !== 'object') return {};

  const record = payload as Record<string, unknown>;
  const source = record['oauth-excluded-models'] ?? record.items ?? payload;
  if (!source || typeof source !== 'object') return {};

  const result: Record<string, string[]> = {};

  Object.entries(source as Record<string, unknown>).forEach(([provider, models]) => {
    const key = String(provider ?? '')
      .trim()
      .toLowerCase();
    if (!key) return;

    const rawList = Array.isArray(models)
      ? models
      : typeof models === 'string'
        ? models.split(/[\n,]+/)
        : [];

    const seen = new Set<string>();
    const normalized: string[] = [];
    rawList.forEach((item) => {
      const trimmed = String(item ?? '').trim();
      if (!trimmed) return;
      const modelKey = trimmed.toLowerCase();
      if (seen.has(modelKey)) return;
      seen.add(modelKey);
      normalized.push(trimmed);
    });

    result[key] = normalized;
  });

  return result;
};

const normalizeOauthModelAlias = (payload: unknown): Record<string, OAuthModelAliasEntry[]> => {
  if (!payload || typeof payload !== 'object') return {};

  const record = payload as Record<string, unknown>;
  const source = record['oauth-model-alias'] ?? record.items ?? payload;
  if (!source || typeof source !== 'object') return {};

  const result: Record<string, OAuthModelAliasEntry[]> = {};

  Object.entries(source as Record<string, unknown>).forEach(([channel, mappings]) => {
    const key = String(channel ?? '')
      .trim()
      .toLowerCase();
    if (!key) return;
    if (!Array.isArray(mappings)) return;

    const seen = new Set<string>();
    const normalized = mappings
      .map((item) => {
        if (!item || typeof item !== 'object') return null;
        const entry = item as Record<string, unknown>;
        const name = String(entry.name ?? entry.id ?? entry.model ?? '').trim();
        const alias = String(entry.alias ?? '').trim();
        if (!name || !alias) return null;
        // Match CPA SanitizeOAuthModelAlias: drop identity mappings.
        if (name.toLowerCase() === alias.toLowerCase()) return null;
        const fork = entry.fork === true;
        const forceMapping =
          entry['force-mapping'] === true ||
          entry.forceMapping === true ||
          entry.force_mapping === true;
        const displayName = String(
          entry['display-name'] ?? entry.displayName ?? entry.display_name ?? ''
        ).trim();
        return {
          name,
          alias,
          ...(fork ? { fork: true } : {}),
          ...(displayName ? { displayName } : {}),
          ...(forceMapping ? { forceMapping: true } : {}),
        };
      })
      .filter(Boolean)
      .filter((entry) => {
        const aliasEntry = entry as OAuthModelAliasEntry;
        // Match CPA: aliases must be unique within a channel (first wins).
        const aliasKey = aliasEntry.alias.toLowerCase();
        if (seen.has(aliasKey)) return false;
        seen.add(aliasKey);
        return true;
      }) as OAuthModelAliasEntry[];

    if (normalized.length) {
      result[key] = normalized;
    }
  });

  return result;
};

const OAUTH_MODEL_ALIAS_ENDPOINT = '/oauth-model-alias';
const AUTH_FILE_FORCE_REFRESH_TIMESTAMP = '2000-01-01T00:00:00Z';

const buildAuthFileSourceIdentityPayload = (
  physicalName: string,
  identities: AuthFileStatusTarget[]
) => {
  if (!Array.isArray(identities) || identities.length === 0) {
    throw new Error('auth file source identity snapshot is required');
  }
  return identities.map((identity) => {
    const name = String(identity.name ?? '').trim();
    const runtimeId = String(identity.runtimeId ?? '').trim();
    if (!name || name !== physicalName || !runtimeId) {
      throw new Error('auth file source identity snapshot is invalid');
    }
    const authIndex =
      identity.authIndex === undefined || identity.authIndex === null
        ? ''
        : String(identity.authIndex).trim();
    const provider = String(identity.provider ?? '').trim();
    const accountId = String(identity.accountId ?? '').trim();
    const accountSnapshot = String(identity.accountSnapshot ?? '').trim();
    return {
      name,
      runtime_id: runtimeId,
      ...(authIndex ? { auth_index: authIndex } : {}),
      ...(provider ? { provider } : {}),
      ...(accountId ? { account_id: accountId } : {}),
      ...(accountSnapshot ? { account_snapshot: accountSnapshot } : {}),
    };
  });
};

const buildAuthFileStatusPayload = (
  target: AuthFileStatusTarget,
  disabled: boolean,
  sourceFile = false,
  sourceIdentities: AuthFileStatusTarget[] = []
) => {
  const physicalName = target.name.trim();
  const runtimeId = String(target.runtimeId ?? '').trim();
  const authIndex =
    target.authIndex === undefined || target.authIndex === null
      ? ''
      : String(target.authIndex).trim();
  const provider = String(target.provider ?? '').trim();
  const accountId = String(target.accountId ?? '').trim();
  const accountSnapshot = String(target.accountSnapshot ?? '').trim();
  const hasCPAMPIdentity = Boolean(provider || accountId || accountSnapshot);
  return {
    name: sourceFile ? physicalName : runtimeId || physicalName,
    disabled,
    ...(authIndex ? { auth_index: authIndex } : {}),
    ...(sourceFile
      ? {
          cpamp_source_file: true,
          cpamp_source_identities: buildAuthFileSourceIdentityPayload(
            physicalName,
            sourceIdentities
          ),
        }
      : {}),
    ...(hasCPAMPIdentity
      ? {
          cpamp_physical_name: physicalName,
          ...(runtimeId ? { cpamp_runtime_id: runtimeId } : {}),
          ...(provider ? { cpamp_provider: provider } : {}),
          ...(accountId ? { cpamp_account_id: accountId } : {}),
          ...(accountSnapshot ? { cpamp_account_snapshot: accountSnapshot } : {}),
        }
      : {}),
  };
};

export const authFilesApi = {
  list: async (requestScope?: AuthFilesApiRequestScope) => {
    const response = requestScope
      ? await apiClient.get<AuthFilesResponse>(
          '/auth-files',
          createScopedApiRequestConfig(requestScope)
        )
      : await apiClient.get<AuthFilesResponse>('/auth-files');
    return dedupeAuthFilesResponse(response);
  },

  setStatus: (
    target: AuthFileStatusTarget,
    disabled: boolean,
    requestScope?: AuthFilesApiRequestScope
  ) => {
    const payload = buildAuthFileStatusPayload(target, disabled);
    return requestScope
      ? apiClient.patch<AuthFileStatusResponse>(
          '/auth-files/status',
          payload,
          createScopedApiRequestConfig(requestScope)
        )
      : apiClient.patch<AuthFileStatusResponse>('/auth-files/status', payload);
  },

  setVerifiedSourceFileStatus: async (
    target: AuthFileStatusTarget,
    disabled: boolean,
    sourceIdentities: AuthFileStatusTarget[],
    requestScope?: AuthFilesApiRequestScope
  ): Promise<AuthFileStatusMutationResult> => {
    const payload = buildAuthFileStatusPayload(target, disabled, true, sourceIdentities);
    const response = requestScope
      ? await apiClient.patch<AuthFileStatusResponse>(
          '/auth-files/status',
          payload,
          createScopedApiRequestConfig(requestScope)
        )
      : await apiClient.patch<AuthFileStatusResponse>('/auth-files/status', payload);
    return { ...response, mutationScope: 'source-file' };
  },

  setStatusWithPluginSourceFallback: async (
    target: AuthFileStatusTarget,
    disabled: boolean,
    verifyPluginSourceFallback?: AuthFileStatusPluginSourceFallbackVerifier,
    requestScope?: AuthFilesApiRequestScope
  ): Promise<AuthFileStatusMutationResult> => {
    try {
      const response = requestScope
        ? await authFilesApi.setStatus(target, disabled, requestScope)
        : await authFilesApi.setStatus(target, disabled);
      return { ...response, mutationScope: 'credential' };
    } catch (err: unknown) {
      const physicalName = target.name.trim();
      const runtimeId = String(target.runtimeId ?? '').trim();
      if (
        !isCPAPluginVirtualMutationConflict(err) ||
        !physicalName ||
        !runtimeId ||
        runtimeId === physicalName
      ) {
        throw err;
      }
      if (!verifyPluginSourceFallback) {
        throw err;
      }
      const sourceIdentities = await verifyPluginSourceFallback();
      return requestScope
        ? authFilesApi.setVerifiedSourceFileStatus(target, disabled, sourceIdentities, requestScope)
        : authFilesApi.setVerifiedSourceFileStatus(target, disabled, sourceIdentities);
    }
  },

  setStatusWithFallback: (
    target: AuthFileStatusTarget,
    disabled: boolean,
    verifyPluginSourceFallback?: AuthFileStatusPluginSourceFallbackVerifier,
    requestScope?: AuthFilesApiRequestScope
  ) =>
    authFilesApi.setStatusWithPluginSourceFallback(
      target,
      disabled,
      verifyPluginSourceFallback,
      requestScope
    ),

  patchFields: (
    target: AuthFileStatusTarget,
    fields: AuthFileFieldsPatch,
    requestScope?: AuthFilesApiRequestScope
  ) => {
    const selector = String(target.runtimeId ?? '').trim() || target.name.trim();
    const identityHeaders = buildAuthFileIdentityHeaders(AUTH_FILE_MUTATION_IDENTITY_HEADER, [
      target,
    ]);
    const scopedRequestConfig = requestScope ? createScopedApiRequestConfig(requestScope) : null;
    return apiClient.patch(
      '/auth-files/fields',
      { name: selector, ...fields },
      scopedRequestConfig
        ? {
            ...scopedRequestConfig,
            headers: { ...scopedRequestConfig.headers, ...identityHeaders },
          }
        : { headers: identityHeaders }
    );
  },

  patchFieldsWithPluginSourceFallback: async (
    target: AuthFileStatusTarget,
    fields: AuthFileFieldsPatch,
    sourceIdentities: AuthFileStatusTarget[] = [],
    requestScope?: AuthFilesApiRequestScope
  ) => {
    try {
      return await authFilesApi.patchFields(target, fields, requestScope);
    } catch (err: unknown) {
      const physicalName = target.name.trim();
      const runtimeId = String(target.runtimeId ?? '').trim();
      if (
        !isCPAPluginVirtualMutationConflict(err) ||
        !physicalName ||
        !runtimeId ||
        runtimeId === physicalName ||
        sourceIdentities.length === 0
      ) {
        throw err;
      }
      return authFilesApi.patchFieldsForAuthIndexes(
        physicalName,
        [target],
        sourceIdentities,
        fields,
        requestScope
      );
    }
  },

  requestCredentialRefresh: (
    target: AuthFileStatusTarget,
    sourceIdentities: AuthFileStatusTarget[] = [],
    requestScope?: AuthFilesApiRequestScope
  ) =>
    authFilesApi.patchFieldsWithPluginSourceFallback(
      target,
      {
        expired: AUTH_FILE_FORCE_REFRESH_TIMESTAMP,
        last_refresh: AUTH_FILE_FORCE_REFRESH_TIMESTAMP,
      },
      sourceIdentities,
      requestScope
    ),

  patchFieldsForAuthIndexes: async (
    name: string,
    targets: AuthFileStatusTarget[],
    sourceIdentities: AuthFileStatusTarget[],
    fields: AuthFileFieldsPatch,
    requestScope?: AuthFilesApiRequestScope
  ) => {
    const authIndexes = targets
      .map((target) => target.authIndex)
      .filter(
        (authIndex): authIndex is AuthFilePatchAuthIndex =>
          authIndex !== undefined && authIndex !== null && String(authIndex).trim() !== ''
      );
    const rawText = await authFilesApi.downloadText(name, requestScope);
    const value = parseAuthFileJsonValue(rawText);
    if (!Array.isArray(value) && sourceIdentities.length !== 1) {
      throw new Error('Auth file patch target changed');
    }
    if (Array.isArray(value) && authIndexes.length !== targets.length) {
      throw new Error('Auth file patch target changed');
    }
    verifyAuthFileJsonPatchTargets(value, targets);
    const nextValue = patchAuthFileJsonValueByAuthIndexes(value, authIndexes, fields);
    await saveAuthFileText(
      name,
      JSON.stringify(nextValue),
      {
        ...buildAuthFileIdentityHeaders(AUTH_FILE_WRITE_IDENTITIES_HEADER, sourceIdentities),
        [AUTH_FILE_WRITE_CONTENT_SHA256_HEADER]: sha256RawTextHex(rawText),
      },
      requestScope
    );
  },

  uploadFiles: async (
    files: File[],
    requestScope?: AuthFilesApiRequestScope
  ): Promise<AuthFileBatchUploadResult> => {
    const requestedNames = files.map((file) => file.name);
    if (requestedNames.length === 0) {
      return { status: 'ok', uploaded: 0, files: [], failed: [] };
    }

    if (files.length === 1) {
      return uploadSingleAuthFile(files[0], {}, requestScope);
    }

    const results: AuthFileBatchUploadResult[] = [];
    for (const file of files) {
      try {
        results.push(await uploadSingleAuthFile(file, {}, requestScope));
      } catch (err) {
        results.push({
          status: 'error',
          uploaded: 0,
          files: [],
          failed: [{ name: file.name, error: readUploadErrorMessage(err) }],
        });
      }
    }
    return mergeBatchUploadResults(results, requestedNames);
  },

  upload: (file: File, requestScope?: AuthFilesApiRequestScope) =>
    authFilesApi.uploadFiles([file], requestScope),

  deleteFiles: async (names: string[]): Promise<AuthFileBatchDeleteResult> => {
    const requestedNames = normalizeRequestedAuthFileNames(names);
    if (requestedNames.length === 0) {
      return { status: 'ok', deleted: 0, files: [], failed: [] };
    }

    const payload = await apiClient.delete<AuthFileBatchDeleteResponse>('/auth-files', {
      data: { names: requestedNames },
    });
    return normalizeBatchDeleteResponse(payload, requestedNames);
  },

  deleteFile: (name: string) => authFilesApi.deleteFiles([name]),

  deleteFileByName: async (
    selector: string,
    physicalName = selector,
    verifyPluginSourceFallback?: AuthFilePluginSourceFallbackVerifier,
    identityTargets: AuthFileDeleteIdentityTarget[] = [],
    requestScope?: AuthFilesApiRequestScope
  ): Promise<AuthFileBatchDeleteResult> => {
    const requestedSelectors = normalizeRequestedAuthFileNames([selector]);
    if (requestedSelectors.length === 0) {
      return { status: 'ok', deleted: 0, files: [], failed: [] };
    }

    const normalizedPhysicalName = String(physicalName ?? '').trim() || requestedSelectors[0];

    const normalizedSelector = requestedSelectors[0];
    try {
      return await deleteAuthFileBySelector(
        normalizedSelector,
        normalizedPhysicalName,
        identityTargets,
        requestScope
      );
    } catch (err: unknown) {
      if (
        !isCPAPluginVirtualMutationConflict(err) ||
        normalizedSelector === normalizedPhysicalName
      ) {
        throw err;
      }
      if (!verifyPluginSourceFallback) {
        throw err;
      }
      await verifyPluginSourceFallback();
      return deleteAuthFileBySelector(
        normalizedPhysicalName,
        normalizedPhysicalName,
        identityTargets,
        requestScope
      );
    }
  },

  downloadText: async (name: string, requestScope?: AuthFilesApiRequestScope): Promise<string> => {
    const scopedRequestConfig = requestScope ? createScopedApiRequestConfig(requestScope) : {};
    const response = await apiClient.getRaw(
      `/auth-files/download?name=${encodeURIComponent(name)}`,
      {
        ...scopedRequestConfig,
        responseType: 'blob',
      }
    );
    const blob = response.data as Blob;
    return blob.text();
  },

  async downloadJsonObject(
    name: string,
    requestScope?: AuthFilesApiRequestScope
  ): Promise<Record<string, unknown>> {
    const rawText = await authFilesApi.downloadText(name, requestScope);
    return parseAuthFileJsonObject(rawText);
  },

  saveText: (name: string, text: string, requestScope?: AuthFilesApiRequestScope) =>
    saveAuthFileText(name, text, {}, requestScope),

  saveJsonObject: (
    name: string,
    json: AuthFileJsonValue,
    requestScope?: AuthFilesApiRequestScope
  ) => saveAuthFileText(name, JSON.stringify(json), {}, requestScope),

  // OAuth 排除模型
  async getOauthExcludedModels(
    requestScope?: AuthFilesApiRequestScope
  ): Promise<Record<string, string[]>> {
    const data = requestScope
      ? await apiClient.get('/oauth-excluded-models', createScopedApiRequestConfig(requestScope))
      : await apiClient.get('/oauth-excluded-models');
    return normalizeOauthExcludedModels(data);
  },

  saveOauthExcludedModels: (
    provider: string,
    models: string[],
    requestScope?: AuthFilesApiRequestScope
  ) =>
    requestScope
      ? apiClient.patch(
          '/oauth-excluded-models',
          { provider, models },
          createScopedApiRequestConfig(requestScope)
        )
      : apiClient.patch('/oauth-excluded-models', { provider, models }),

  deleteOauthExcludedEntry: (provider: string, requestScope?: AuthFilesApiRequestScope) =>
    requestScope
      ? apiClient.delete(
          `/oauth-excluded-models?provider=${encodeURIComponent(provider)}`,
          createScopedApiRequestConfig(requestScope)
        )
      : apiClient.delete(`/oauth-excluded-models?provider=${encodeURIComponent(provider)}`),

  replaceOauthExcludedModels: (
    map: Record<string, string[]>,
    requestScope?: AuthFilesApiRequestScope
  ) =>
    requestScope
      ? apiClient.put(
          '/oauth-excluded-models',
          normalizeOauthExcludedModels(map),
          createScopedApiRequestConfig(requestScope)
        )
      : apiClient.put('/oauth-excluded-models', normalizeOauthExcludedModels(map)),

  // OAuth 模型别名
  async getOauthModelAlias(
    requestScope?: AuthFilesApiRequestScope
  ): Promise<Record<string, OAuthModelAliasEntry[]>> {
    const data = requestScope
      ? await apiClient.get(OAUTH_MODEL_ALIAS_ENDPOINT, createScopedApiRequestConfig(requestScope))
      : await apiClient.get(OAUTH_MODEL_ALIAS_ENDPOINT);
    return normalizeOauthModelAlias(data);
  },

  saveOauthModelAlias: async (
    channel: string,
    aliases: OAuthModelAliasEntry[],
    requestScope?: AuthFilesApiRequestScope
  ) => {
    const normalizedChannel = String(channel ?? '')
      .trim()
      .toLowerCase();
    const normalizedAliases =
      normalizeOauthModelAlias({ [normalizedChannel]: aliases })[normalizedChannel] ?? [];
    const payload = {
      channel: normalizedChannel,
      aliases: normalizedAliases.map(({ displayName, forceMapping, ...entry }) => ({
        ...entry,
        ...(displayName ? { 'display-name': displayName } : {}),
        ...(forceMapping ? { 'force-mapping': true } : {}),
      })),
    };
    if (requestScope) {
      await apiClient.patch(
        OAUTH_MODEL_ALIAS_ENDPOINT,
        payload,
        createScopedApiRequestConfig(requestScope)
      );
      return;
    }
    await apiClient.patch(OAUTH_MODEL_ALIAS_ENDPOINT, payload);
  },

  deleteOauthModelAlias: async (channel: string, requestScope?: AuthFilesApiRequestScope) => {
    const normalizedChannel = String(channel ?? '')
      .trim()
      .toLowerCase();
    const requestConfig = requestScope ? createScopedApiRequestConfig(requestScope) : undefined;
    const payload = {
      channel: normalizedChannel,
      aliases: [],
    };

    try {
      if (requestConfig) {
        await apiClient.patch(OAUTH_MODEL_ALIAS_ENDPOINT, payload, requestConfig);
      } else {
        await apiClient.patch(OAUTH_MODEL_ALIAS_ENDPOINT, payload);
      }
    } catch (err: unknown) {
      const status = getStatusCode(err);
      if (status !== 405) throw err;
      const endpoint = `${OAUTH_MODEL_ALIAS_ENDPOINT}?channel=${encodeURIComponent(normalizedChannel)}`;
      if (requestConfig) {
        await apiClient.delete(endpoint, requestConfig);
      } else {
        await apiClient.delete(endpoint);
      }
    }
  },

  // 获取认证凭证支持的模型
  async getModelsForAuthFile(
    name: string,
    requestScope?: AuthFilesApiRequestScope
  ): Promise<AuthFileModelApiItem[]> {
    const endpoint = `/auth-files/models?name=${encodeURIComponent(name)}`;
    const data = requestScope
      ? await apiClient.get<Record<string, unknown>>(
          endpoint,
          createScopedApiRequestConfig(requestScope)
        )
      : await apiClient.get<Record<string, unknown>>(endpoint);
    const models = data.models ?? data['models'];
    return normalizeAuthFileModelItems(models);
  },

  // 获取指定 channel 的模型定义
  async getModelDefinitions(
    channel: string,
    requestScope?: AuthFilesApiRequestScope
  ): Promise<AuthFileModelApiItem[]> {
    const normalizedChannel = String(channel ?? '')
      .trim()
      .toLowerCase();
    if (!normalizedChannel) return [];
    const endpointChannel = normalizedChannel === 'gemini-cli' ? 'gemini' : normalizedChannel;
    const endpoint = `/model-definitions/${encodeURIComponent(endpointChannel)}`;
    const data = requestScope
      ? await apiClient.get<Record<string, unknown>>(
          endpoint,
          createScopedApiRequestConfig(requestScope)
        )
      : await apiClient.get<Record<string, unknown>>(endpoint);
    const models = data.models ?? data['models'];
    return normalizeAuthFileModelItems(models);
  },
};
