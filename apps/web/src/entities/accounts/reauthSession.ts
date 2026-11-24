const ACCOUNT_OAUTH_REAUTH_SESSION_STORAGE_KEY = 'accounts.oauthReauthSessions.v1';
export const ACCOUNT_OAUTH_REAUTH_SESSION_PARAM = 'accountReauth';

const ACCOUNT_OAUTH_REAUTH_SESSION_TTL_MS = 24 * 60 * 60 * 1000;
const ACCOUNT_OAUTH_REAUTH_SESSION_LIMIT = 32;

type SessionStorageLike = Pick<Storage, 'getItem' | 'setItem'>;

type AccountOAuthReauthSession = {
  id: string;
  connectionFingerprint: string;
  oauthProvider: string;
  resultKeys: string[];
  createdAtMs: number;
  completedAtMs?: number;
};

type AccountOAuthReauthSessionInput = {
  connectionFingerprint: string | null;
  oauthProvider: string;
  resultKeys: Iterable<string>;
  createdAtMs?: number;
  sessionId?: string;
};

type CompleteAccountOAuthReauthSessionInput = {
  connectionFingerprint: string | null;
  oauthProvider: string;
  sessionId: string | null;
  completedAtMs?: number;
};

type RecordCompletedAccountReauthSessionInput = AccountOAuthReauthSessionInput & {
  completedAtMs?: number;
};

const normalizeString = (value: unknown): string => (typeof value === 'string' ? value.trim() : '');

const normalizeProvider = (value: unknown): string => normalizeString(value).toLowerCase();

const normalizeTimestamp = (value: unknown): number | null => {
  const parsed = typeof value === 'number' ? value : Number(value);
  return Number.isFinite(parsed) && parsed > 0 ? Math.trunc(parsed) : null;
};

const normalizeResultKeys = (values: Iterable<string>): string[] =>
  Array.from(
    new Set(
      Array.from(values, (value) => normalizeString(value)).filter((value) => value.length > 0)
    )
  );

const getBrowserSessionStorage = (): SessionStorageLike | null => {
  if (typeof window === 'undefined') return null;
  try {
    return window.sessionStorage;
  } catch {
    return null;
  }
};

const parseSession = (value: unknown): AccountOAuthReauthSession | null => {
  if (!value || typeof value !== 'object') return null;
  const record = value as Partial<AccountOAuthReauthSession>;
  const id = normalizeString(record.id);
  const connectionFingerprint = normalizeString(record.connectionFingerprint);
  const oauthProvider = normalizeProvider(record.oauthProvider);
  const resultKeys = Array.isArray(record.resultKeys)
    ? normalizeResultKeys(
        record.resultKeys.filter((item): item is string => typeof item === 'string')
      )
    : [];
  const createdAtMs = normalizeTimestamp(record.createdAtMs);
  const completedAtMs = normalizeTimestamp(record.completedAtMs);
  if (!id || !connectionFingerprint || !oauthProvider || resultKeys.length === 0 || !createdAtMs) {
    return null;
  }
  return {
    id,
    connectionFingerprint,
    oauthProvider,
    resultKeys,
    createdAtMs,
    ...(completedAtMs ? { completedAtMs } : {}),
  };
};

const readSessions = (
  storage: SessionStorageLike | null,
  nowMs: number
): AccountOAuthReauthSession[] => {
  if (!storage) return [];
  try {
    const raw = storage.getItem(ACCOUNT_OAUTH_REAUTH_SESSION_STORAGE_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return [];
    return parsed
      .map(parseSession)
      .filter((session): session is AccountOAuthReauthSession => session !== null)
      .filter((session) => nowMs - session.createdAtMs <= ACCOUNT_OAUTH_REAUTH_SESSION_TTL_MS)
      .sort((left, right) => left.createdAtMs - right.createdAtMs)
      .slice(-ACCOUNT_OAUTH_REAUTH_SESSION_LIMIT);
  } catch {
    return [];
  }
};

const writeSessions = (
  storage: SessionStorageLike | null,
  sessions: AccountOAuthReauthSession[]
): boolean => {
  if (!storage) return false;
  try {
    storage.setItem(
      ACCOUNT_OAUTH_REAUTH_SESSION_STORAGE_KEY,
      JSON.stringify(sessions.slice(-ACCOUNT_OAUTH_REAUTH_SESSION_LIMIT))
    );
    return true;
  } catch {
    return false;
  }
};

const createSessionId = (): string => {
  try {
    if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
      return crypto.randomUUID();
    }
  } catch {
    // Fall back to a tab-local opaque identifier when Web Crypto is unavailable.
  }
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
};

const createSession = (
  input: AccountOAuthReauthSessionInput,
  completedAtMs?: number
): AccountOAuthReauthSession | null => {
  const connectionFingerprint = normalizeString(input.connectionFingerprint);
  const oauthProvider = normalizeProvider(input.oauthProvider);
  const resultKeys = normalizeResultKeys(input.resultKeys);
  const createdAtMs = normalizeTimestamp(input.createdAtMs ?? Date.now());
  const id = normalizeString(input.sessionId) || createSessionId();
  if (!connectionFingerprint || !oauthProvider || resultKeys.length === 0 || !createdAtMs || !id) {
    return null;
  }
  const normalizedCompletedAtMs = normalizeTimestamp(completedAtMs);
  return {
    id,
    connectionFingerprint,
    oauthProvider,
    resultKeys,
    createdAtMs,
    ...(normalizedCompletedAtMs ? { completedAtMs: normalizedCompletedAtMs } : {}),
  };
};

export const buildAccountOAuthReauthPath = (
  oauthProvider: string,
  sessionId?: string | null
): string => {
  const normalizedProvider = normalizeProvider(oauthProvider);
  const normalizedSessionId = normalizeString(sessionId);
  const query = normalizedSessionId
    ? `?${ACCOUNT_OAUTH_REAUTH_SESSION_PARAM}=${encodeURIComponent(normalizedSessionId)}`
    : '';
  return `/oauth${query}#oauth-provider-${encodeURIComponent(normalizedProvider)}`;
};

export const readAccountOAuthReauthSessionId = (search: string): string | null => {
  try {
    const value = new URLSearchParams(search).get(ACCOUNT_OAUTH_REAUTH_SESSION_PARAM);
    return normalizeString(value) || null;
  } catch {
    return null;
  }
};

export const beginAccountOAuthReauthSession = (
  input: AccountOAuthReauthSessionInput,
  storage: SessionStorageLike | null = getBrowserSessionStorage()
): string | null => {
  const session = createSession(input);
  if (!session) return null;
  const sessions = readSessions(storage, session.createdAtMs).filter(
    (candidate) => candidate.id !== session.id
  );
  sessions.push(session);
  return writeSessions(storage, sessions) ? session.id : null;
};

export const completeAccountOAuthReauthSession = (
  input: CompleteAccountOAuthReauthSessionInput,
  storage: SessionStorageLike | null = getBrowserSessionStorage()
): boolean => {
  const sessionId = normalizeString(input.sessionId);
  const connectionFingerprint = normalizeString(input.connectionFingerprint);
  const oauthProvider = normalizeProvider(input.oauthProvider);
  const completedAtMs = normalizeTimestamp(input.completedAtMs ?? Date.now());
  if (!sessionId || !connectionFingerprint || !oauthProvider || !completedAtMs) return false;

  const sessions = readSessions(storage, completedAtMs);
  const index = sessions.findIndex(
    (session) =>
      session.id === sessionId &&
      session.connectionFingerprint === connectionFingerprint &&
      session.oauthProvider === oauthProvider
  );
  if (index < 0) return false;
  if (sessions[index]?.completedAtMs) return true;
  sessions[index] = { ...sessions[index], completedAtMs };
  return writeSessions(storage, sessions);
};

export const completeAccountOAuthReauthSessionFromSearch = (
  search: string,
  oauthProvider: string,
  connectionFingerprint: string | null,
  storage: SessionStorageLike | null = getBrowserSessionStorage(),
  completedAtMs = Date.now()
): boolean =>
  completeAccountOAuthReauthSession(
    {
      connectionFingerprint,
      oauthProvider,
      sessionId: readAccountOAuthReauthSessionId(search),
      completedAtMs,
    },
    storage
  );

export const recordCompletedAccountReauthSession = (
  input: RecordCompletedAccountReauthSessionInput,
  storage: SessionStorageLike | null = getBrowserSessionStorage()
): boolean => {
  const completedAtMs = normalizeTimestamp(input.completedAtMs ?? Date.now());
  if (!completedAtMs) return false;
  const session = createSession(input, completedAtMs);
  if (!session) return false;
  const sessions = readSessions(storage, completedAtMs).filter(
    (candidate) => candidate.id !== session.id
  );
  sessions.push(session);
  return writeSessions(storage, sessions);
};

export const readCompletedAccountReauthResultKeys = (
  connectionFingerprint: string | null,
  storage: SessionStorageLike | null = getBrowserSessionStorage(),
  nowMs = Date.now()
): Set<string> => {
  const normalizedFingerprint = normalizeString(connectionFingerprint);
  if (!normalizedFingerprint) return new Set();
  const resultKeys = readSessions(storage, nowMs)
    .filter(
      (session) =>
        session.connectionFingerprint === normalizedFingerprint &&
        typeof session.completedAtMs === 'number'
    )
    .flatMap((session) => session.resultKeys);
  return new Set(resultKeys);
};
