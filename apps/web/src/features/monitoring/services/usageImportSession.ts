import {
  getUsageServiceErrorCode,
  usageServiceApi,
  type UsageImportResponse,
  type UsageImportSession,
  type UsageImportSessionStatus,
} from '@/services/api/usageService';

const STORAGE_KEY = 'cpa-manager-plus:usage-import-sessions:v1';
const DEFAULT_POLL_INTERVAL_MS = 500;
const DEFAULT_CANCEL_POLL_INTERVAL_MS = 250;
const DEFAULT_CANCEL_SETTLE_TIMEOUT_MS = 30_000;
const RESUME_KEY_PATTERN = /^[0-9a-f]{32}$/;

export type UsageImportPhase =
  | 'preparing'
  | 'uploading'
  | 'processing'
  | 'paused'
  | 'completed'
  | 'failed'
  | 'cancelled';

export interface UsageImportProgress {
  sessionId: string;
  filename: string;
  phase: UsageImportPhase;
  status?: UsageImportSessionStatus;
  uploadedBytes: number;
  totalBytes: number;
  percent: number;
  retryable?: boolean;
  error?: string;
  result?: UsageImportResponse;
}

export type UsageImportSessionClient = Pick<
  typeof usageServiceApi,
  | 'createUsageImportSession'
  | 'getUsageImportSession'
  | 'uploadUsageImportSessionChunk'
  | 'completeUsageImportSession'
  | 'cancelUsageImportSession'
>;

export interface UsageImportStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

interface StoredUsageImportSession {
  sessionId: string;
  resumeKey: string;
}

export interface UploadUsageImportFileOptions {
  base: string;
  managementKey?: string;
  file: File;
  signal?: AbortSignal;
  onProgress?: (progress: UsageImportProgress) => void;
  pollIntervalMs?: number;
  client?: UsageImportSessionClient;
  storage?: UsageImportStorage | null;
}

export interface CancelUsageImportFileOptions {
  base: string;
  managementKey?: string;
  sessionId: string;
  file?: File;
  client?: UsageImportSessionClient;
  storage?: UsageImportStorage | null;
  pollIntervalMs?: number;
  settleTimeoutMs?: number;
}

export class UsageImportPausedError extends Error {
  constructor() {
    super('usage import paused');
    this.name = 'UsageImportPausedError';
  }
}

export class UsageImportCancelledError extends Error {
  constructor() {
    super('usage import cancelled');
    this.name = 'UsageImportCancelledError';
  }
}

export class UsageImportFailedError extends Error {
  readonly sessionId: string;
  readonly retryable: boolean;

  constructor(session: UsageImportSession) {
    super(session.error || 'usage import failed');
    this.name = 'UsageImportFailedError';
    this.sessionId = session.id;
    this.retryable = Boolean(session.retryable);
  }
}

export const isUsageImportPausedError = (error: unknown): error is UsageImportPausedError =>
  error instanceof UsageImportPausedError;

export const isUsageImportCancelledError = (error: unknown): error is UsageImportCancelledError =>
  error instanceof UsageImportCancelledError;

export async function uploadUsageImportFile(
  options: UploadUsageImportFileOptions
): Promise<UsageImportResponse> {
  const client = options.client ?? usageServiceApi;
  const storage = options.storage === undefined ? resolveStorage() : options.storage;
  const pollIntervalMs = Math.max(0, options.pollIntervalMs ?? DEFAULT_POLL_INTERVAL_MS);
  let session: UsageImportSession | null = null;
  let retryableCompletionAttempted = false;

  const emit = (
    phase: UsageImportPhase,
    current = session,
    error = '',
    retryable = current?.retryable
  ) => {
    const uploadedBytes = current?.received_bytes ?? 0;
    const totalBytes = current?.size_bytes ?? options.file.size;
    options.onProgress?.({
      sessionId: current?.id ?? '',
      filename: options.file.name,
      phase,
      status: current?.status,
      uploadedBytes,
      totalBytes,
      percent:
        totalBytes > 0
          ? uploadedBytes >= totalBytes
            ? 100
            : Math.max(0, Math.floor((uploadedBytes / totalBytes) * 100))
          : 0,
      retryable,
      error: phase === 'cancelled' ? error || undefined : error || current?.error || undefined,
      result: current?.result,
    });
  };

  try {
    throwIfPaused(options.signal);
    emit('preparing');
    session = await resolveOrCreateSession({ ...options, client, storage });
    emit(resolvePhase(session));

    for (;;) {
      throwIfPaused(options.signal);
      switch (session.status) {
        case 'uploading': {
          const offset = session.received_bytes;
          if (
            offset < 0 ||
            offset >= options.file.size ||
            session.size_bytes !== options.file.size ||
            session.chunk_size_bytes <= 0
          ) {
            throw new Error('usage import session upload state is invalid');
          }
          const end = Math.min(options.file.size, offset + session.chunk_size_bytes);
          const chunk = options.file.slice(offset, end);
          const next = await client.uploadUsageImportSessionChunk(
            options.base,
            session.id,
            offset,
            chunk,
            options.managementKey,
            options.signal
          );
          if (next.received_bytes <= offset || next.received_bytes > end) {
            throw new Error('usage import session did not advance by the uploaded chunk');
          }
          session = next;
          storeSession(storage, options.base, options.file, session.id);
          emit('uploading');
          break;
        }
        case 'ready':
          session = await client.completeUsageImportSession(
            options.base,
            session.id,
            options.managementKey,
            options.signal
          );
          emit(resolvePhase(session));
          break;
        case 'processing':
          emit('processing');
          await abortableDelay(pollIntervalMs, options.signal);
          session = await client.getUsageImportSession(
            options.base,
            session.id,
            options.managementKey,
            options.signal
          );
          emit(resolvePhase(session));
          break;
        case 'completed': {
          if (!session.result) {
            throw new Error('usage import completed without a result');
          }
          clearStoredSession(storage, options.base, options.file, session.id);
          emit('completed');
          return session.result;
        }
        case 'failed':
          if (session.retryable && !retryableCompletionAttempted) {
            retryableCompletionAttempted = true;
            session = await client.completeUsageImportSession(
              options.base,
              session.id,
              options.managementKey,
              options.signal
            );
            emit(resolvePhase(session));
            break;
          }
          if (!session.retryable) {
            clearStoredSession(storage, options.base, options.file, session.id);
          }
          emit('failed');
          throw new UsageImportFailedError(session);
        case 'cancelled':
          clearStoredSession(storage, options.base, options.file, session.id);
          emit('cancelled');
          throw new UsageImportCancelledError();
      }
    }
  } catch (error) {
    if (options.signal?.aborted) {
      emit('paused');
      throw new UsageImportPausedError();
    }
    if (
      !(error instanceof UsageImportFailedError) &&
      !(error instanceof UsageImportCancelledError)
    ) {
      const code = getUsageServiceErrorCode(error);
      let retryable = session?.status === 'failed' ? Boolean(session.retryable) : true;
      if (
        !session &&
        (code === 'usage_import_session_invalid_request' ||
          code === 'usage_import_session_too_large' ||
          code === 'usage_import_session_conflict')
      ) {
        clearStoredSession(storage, options.base, options.file, '');
        retryable = code === 'usage_import_session_conflict';
      }
      emit(
        'failed',
        session,
        error instanceof Error ? error.message : String(error),
        retryable
      );
    }
    throw error;
  }
}

export async function cancelUsageImportFile(
  options: CancelUsageImportFileOptions
): Promise<UsageImportSession | null> {
  const client = options.client ?? usageServiceApi;
  const storage = options.storage === undefined ? resolveStorage() : options.storage;
  let session: UsageImportSession | null;
  try {
    session = await client.cancelUsageImportSession(
      options.base,
      options.sessionId,
      options.managementKey
    );
  } catch (error) {
    if (getUsageServiceErrorCode(error) !== 'usage_import_session_not_found') {
      throw error;
    }
    session = null;
  }
  const pollIntervalMs = Math.max(
    0,
    options.pollIntervalMs ?? DEFAULT_CANCEL_POLL_INTERVAL_MS
  );
  const settleTimeoutMs = Math.max(
    pollIntervalMs,
    options.settleTimeoutMs ?? DEFAULT_CANCEL_SETTLE_TIMEOUT_MS
  );
  const deadline = Date.now() + settleTimeoutMs;
  while (session?.status === 'processing') {
    if (Date.now() >= deadline) {
      throw new Error('usage import cancellation is still processing');
    }
    await abortableDelay(pollIntervalMs);
    session = await client.getUsageImportSession(
      options.base,
      options.sessionId,
      options.managementKey
    );
  }
  if (options.file) {
    clearStoredSession(storage, options.base, options.file, options.sessionId);
  } else {
    clearStoredSessionByID(storage, options.sessionId);
  }
  return session;
}

async function resolveOrCreateSession(
  options: UploadUsageImportFileOptions & {
    client: UsageImportSessionClient;
    storage: UsageImportStorage | null;
  }
): Promise<UsageImportSession> {
  const stored = readStoredSession(options.storage, options.base, options.file);
  let resumeKey = stored?.resumeKey ?? '';
  if (stored?.sessionId) {
    try {
      const storedSession = await options.client.getUsageImportSession(
        options.base,
        stored.sessionId,
        options.managementKey,
        options.signal
      );
      if (storedSession.size_bytes === options.file.size) {
        if (
          storedSession.status !== 'cancelled' &&
          !(storedSession.status === 'failed' && !storedSession.retryable)
        ) {
          return storedSession;
        }
      }
      clearStoredSession(options.storage, options.base, options.file, storedSession.id);
      resumeKey = '';
    } catch (error) {
      if (getUsageServiceErrorCode(error) !== 'usage_import_session_not_found') {
        throw error;
      }
      clearStoredSession(options.storage, options.base, options.file, stored.sessionId);
    }
  }

  if (!RESUME_KEY_PATTERN.test(resumeKey)) {
    resumeKey = createResumeKey();
  }
  storeSession(options.storage, options.base, options.file, '', resumeKey);

  const created = await options.client.createUsageImportSession(
    options.base,
    options.file.name,
    options.file.size,
    options.managementKey,
    resumeKey,
    options.signal
  );
  storeSession(options.storage, options.base, options.file, created.id, resumeKey);
  return created;
}

function resolvePhase(session: UsageImportSession): UsageImportPhase {
  switch (session.status) {
    case 'processing':
      return 'processing';
    case 'completed':
      return 'completed';
    case 'failed':
      return 'failed';
    case 'cancelled':
      return 'cancelled';
    default:
      return 'uploading';
  }
}

function throwIfPaused(signal?: AbortSignal) {
  if (signal?.aborted) {
    throw new UsageImportPausedError();
  }
}

function abortableDelay(durationMs: number, signal?: AbortSignal): Promise<void> {
  if (durationMs <= 0) {
    throwIfPaused(signal);
    return Promise.resolve();
  }
  return new Promise((resolve, reject) => {
    const onAbort = () => {
      globalThis.clearTimeout(timer);
      reject(new UsageImportPausedError());
    };
    const timer = globalThis.setTimeout(() => {
      signal?.removeEventListener('abort', onAbort);
      resolve();
    }, durationMs);
    signal?.addEventListener('abort', onAbort, { once: true });
  });
}

function resolveStorage(): UsageImportStorage | null {
  if (typeof localStorage === 'undefined') return null;
  return localStorage;
}

function sessionFingerprint(base: string, file: File): string {
  return JSON.stringify([base.replace(/\/+$/, ''), file.name, file.size, file.lastModified]);
}

function readStoredSessions(
  storage: UsageImportStorage | null
): Record<string, StoredUsageImportSession> {
  if (!storage) return {};
  try {
    const raw = storage.getItem(STORAGE_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw) as unknown;
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {};
    const sessions: Record<string, StoredUsageImportSession> = {};
    Object.entries(parsed as Record<string, unknown>).forEach(([key, value]) => {
      if (typeof value === 'string' && value.length > 0) {
        sessions[key] = { sessionId: value, resumeKey: '' };
        return;
      }
      if (!value || typeof value !== 'object' || Array.isArray(value)) return;
      const record = value as Record<string, unknown>;
      const sessionId = typeof record.sessionId === 'string' ? record.sessionId : '';
      const resumeKey = typeof record.resumeKey === 'string' ? record.resumeKey : '';
      if (sessionId || resumeKey) {
        sessions[key] = { sessionId, resumeKey };
      }
    });
    return sessions;
  } catch {
    return {};
  }
}

function writeStoredSessions(
  storage: UsageImportStorage | null,
  sessions: Record<string, StoredUsageImportSession>
) {
  if (!storage) return;
  try {
    if (Object.keys(sessions).length === 0) {
      storage.removeItem(STORAGE_KEY);
      return;
    }
    storage.setItem(STORAGE_KEY, JSON.stringify(sessions));
  } catch {
    // Resumability storage is best-effort; the active upload remains usable.
  }
}

function readStoredSession(
  storage: UsageImportStorage | null,
  base: string,
  file: File
): StoredUsageImportSession | null {
  return readStoredSessions(storage)[sessionFingerprint(base, file)] ?? null;
}

function storeSession(
  storage: UsageImportStorage | null,
  base: string,
  file: File,
  sessionId: string,
  resumeKey = ''
) {
  const sessions = readStoredSessions(storage);
  sessions[sessionFingerprint(base, file)] = { sessionId, resumeKey };
  writeStoredSessions(storage, sessions);
}

function clearStoredSession(
  storage: UsageImportStorage | null,
  base: string,
  file: File,
  expectedSessionId: string
) {
  const sessions = readStoredSessions(storage);
  const key = sessionFingerprint(base, file);
  if (sessions[key]?.sessionId === expectedSessionId) {
    delete sessions[key];
    writeStoredSessions(storage, sessions);
  }
}

function clearStoredSessionByID(storage: UsageImportStorage | null, sessionId: string) {
  const sessions = readStoredSessions(storage);
  let changed = false;
  Object.entries(sessions).forEach(([key, value]) => {
    if (value.sessionId === sessionId) {
      delete sessions[key];
      changed = true;
    }
  });
  if (changed) writeStoredSessions(storage, sessions);
}

function createResumeKey(): string {
  const bytes = new Uint8Array(16);
  if (globalThis.crypto?.getRandomValues) {
    globalThis.crypto.getRandomValues(bytes);
  } else {
    for (let index = 0; index < bytes.length; index += 1) {
      bytes[index] = Math.floor(Math.random() * 256);
    }
  }
  return Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('');
}
