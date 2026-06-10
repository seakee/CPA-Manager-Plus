export type AuthJsonInputType = 'cpa' | 'session' | 'sub2api';

type JsonRecord = Record<string, unknown>;
export type AuthJsonConversionResult = JsonRecord | JsonRecord[];
type TraversalState = {
  visited: WeakSet<object>;
  visitedRecords: number;
};

export class AuthJsonConversionError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'AuthJsonConversionError';
  }
}

const isRecord = (value: unknown): value is JsonRecord =>
  Boolean(value) && typeof value === 'object' && !Array.isArray(value);

const firstNonEmpty = (...values: unknown[]) => {
  for (const value of values) {
    if (typeof value === 'string' && value.trim() !== '') return value.trim();
    if (typeof value === 'number' && Number.isFinite(value)) return value;
    if (typeof value === 'boolean') return value;
  }
  return undefined;
};

const firstNonEmptyString = (...values: unknown[]) => {
  for (const value of values) {
    if (typeof value === 'string' && value.trim() !== '') return value.trim();
  }
  return undefined;
};

const firstRecord = (...values: unknown[]) => {
  for (const value of values) {
    if (isRecord(value)) return value;
  }
  return undefined;
};

const KNOWN_CREDENTIAL_KEYS = new Set([
  'access_token',
  'accesstoken',
  'id_token',
  'idtoken',
  'refresh_token',
  'refreshtoken',
  'session_token',
  'sessiontoken',
  'api_key',
  'apikey',
  'key',
  'token',
  'cookie',
  'cookies',
  'authorization',
  'bearer',
]);

const GENERIC_CREDENTIAL_KEYS = new Set([
  ...KNOWN_CREDENTIAL_KEYS,
  'session_secret',
  'sessionsecret',
  'client_secret',
  'clientsecret',
]);

const SERVICE_ACCOUNT_CREDENTIAL_KEYS = new Set(['private_key', 'privatekey']);

const OPENAI_CREDENTIAL_KEYS = new Set([
  'access_token',
  'accesstoken',
  'refresh_token',
  'refreshtoken',
  'api_key',
  'apikey',
  'authorization',
  'bearer',
]);

const CHATGPT_CREDENTIAL_KEYS = new Set([
  'access_token',
  'accesstoken',
  'id_token',
  'idtoken',
  'refresh_token',
  'refreshtoken',
  'session_token',
  'sessiontoken',
  'cookie',
  'cookies',
]);

const PROVIDER_CREDENTIAL_KEYSETS: Record<string, Set<string>> = {
  codex: KNOWN_CREDENTIAL_KEYS,
  openai: OPENAI_CREDENTIAL_KEYS,
  chatgpt: CHATGPT_CREDENTIAL_KEYS,
  service_account: SERVICE_ACCOUNT_CREDENTIAL_KEYS,
};

const WINDOWS_RESERVED_BASE_NAMES = new Set([
  'con',
  'prn',
  'aux',
  'nul',
  'com1',
  'com2',
  'com3',
  'com4',
  'com5',
  'com6',
  'com7',
  'com8',
  'com9',
  'lpt1',
  'lpt2',
  'lpt3',
  'lpt4',
  'lpt5',
  'lpt6',
  'lpt7',
  'lpt8',
  'lpt9',
]);

const FORBIDDEN_INVISIBLE_CODE_POINTS = new Set([
  0x200b, 0x200c, 0x200d, 0x200e, 0x200f, 0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2060, 0x2066,
  0x2067, 0x2068, 0x2069, 0xfeff,
]);

const CREDENTIAL_CONTAINER_KEYS = ['credentials', 'auth', 'cookies'] as const;

const MAX_AUTH_JSON_INPUT_CHARS = 1_000_000;
const MAX_JSON_TRAVERSAL_DEPTH = 64;
const MAX_JSON_RECORDS = 5_000;
const MAX_JWT_SEGMENT_CHARS = 16_384;

const createTraversalState = (): TraversalState => ({
  visited: new WeakSet<object>(),
  visitedRecords: 0,
});

const assertTraversalDepth = (depth: number) => {
  if (depth > MAX_JSON_TRAVERSAL_DEPTH) {
    throw new AuthJsonConversionError('Auth JSON nesting exceeds depth limit');
  }
};

const markTraversalRecord = (value: object, state: TraversalState) => {
  if (state.visited.has(value)) return false;

  state.visited.add(value);
  state.visitedRecords += 1;
  if (state.visitedRecords > MAX_JSON_RECORDS) {
    throw new AuthJsonConversionError('Auth JSON traversal exceeds record limit');
  }

  return true;
};

const hasCredentialValueForKeySet = (
  value: unknown,
  keySet: Set<string>,
  depth = 0,
  state: TraversalState = createTraversalState()
): boolean => {
  assertTraversalDepth(depth);

  if (Array.isArray(value)) {
    if (!markTraversalRecord(value, state)) return false;
    return value.some((item) => hasCredentialValueForKeySet(item, keySet, depth + 1, state));
  }
  if (!isRecord(value)) return false;
  if (!markTraversalRecord(value, state)) return false;

  for (const [key, item] of Object.entries(value)) {
    const normalizedKey = key.toLowerCase();
    if (keySet.has(normalizedKey) && typeof item === 'string' && item.trim() !== '') {
      return true;
    }

    if (hasCredentialValueForKeySet(item, keySet, depth + 1, state)) {
      return true;
    }
  }

  return false;
};

const stripUnavailable = (value: unknown): unknown => {
  if (Array.isArray(value)) {
    const next = value.map(stripUnavailable).filter((item) => item !== undefined);
    return next.length ? next : undefined;
  }

  if (isRecord(value)) {
    const entries = Object.entries(value)
      .map(([key, item]) => [key, stripUnavailable(item)] as const)
      .filter(([, item]) => item !== undefined);
    return entries.length ? Object.fromEntries(entries) : undefined;
  }

  if (value === undefined || value === null || value === '') return undefined;
  return value;
};

const decodeBase64Url = (value: string) => {
  const normalized = value.replace(/-/g, '+').replace(/_/g, '/');
  const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, '=');
  const binary = atob(padded);
  const bytes = Uint8Array.from(binary, (char) => char.charCodeAt(0));
  return new TextDecoder().decode(bytes);
};

const parseJwtSegment = (token: unknown, segmentIndex: number): JsonRecord | undefined => {
  if (typeof token !== 'string' || token.trim() === '') return undefined;
  const segments = token.split('.');
  if (segments.length <= segmentIndex) return undefined;
  if (segments[segmentIndex].length > MAX_JWT_SEGMENT_CHARS) return undefined;

  try {
    const decoded = JSON.parse(decodeBase64Url(segments[segmentIndex])) as unknown;
    return isRecord(decoded) ? decoded : undefined;
  } catch {
    return undefined;
  }
};

const parseJwtPayload = (token: unknown): JsonRecord | undefined => parseJwtSegment(token, 1);

const parseJwtHeader = (token: unknown): JsonRecord | undefined => parseJwtSegment(token, 0);

const isUnsignedJwtToken = (token: unknown): boolean => {
  const header = parseJwtHeader(token);
  if (!header) return false;

  return typeof header.alg === 'string' && header.alg.trim().toLowerCase() === 'none';
};

const hasEmptyJwtSignatureSegment = (token: unknown): boolean => {
  if (typeof token !== 'string' || token.trim() === '') return false;
  const segments = token.split('.');
  if (segments.length !== 3) return false;
  return segments[0].trim() !== '' && segments[1].trim() !== '' && segments[2].trim() === '';
};

const isUnsafeIdToken = (token: unknown): boolean =>
  isUnsignedJwtToken(token) || hasEmptyJwtSignatureSegment(token);

const normalizeTimestamp = (value: unknown) => {
  const fromEpochLike = (epochValue: number) => {
    const milliseconds = epochValue > 1e11 ? epochValue : epochValue * 1000;
    const date = new Date(milliseconds);
    return Number.isNaN(date.getTime()) ? undefined : date.toISOString();
  };

  if (value instanceof Date && !Number.isNaN(value.getTime())) return value.toISOString();

  if (typeof value === 'number' && Number.isFinite(value)) {
    return fromEpochLike(value);
  }

  if (typeof value !== 'string' || value.trim() === '') return undefined;
  const normalizedValue = value.trim();
  if (/^-?\d+(?:\.\d+)?$/.test(normalizedValue)) {
    const epochValue = Number(normalizedValue);
    if (Number.isFinite(epochValue)) return fromEpochLike(epochValue);
  }

  const date = new Date(normalizedValue);
  return Number.isNaN(date.getTime()) ? undefined : date.toISOString();
};

const parseJsonObject = (text: string, allowArray = false): JsonRecord | unknown[] => {
  if (text.length > MAX_AUTH_JSON_INPUT_CHARS) {
    throw new AuthJsonConversionError('Auth JSON input exceeds size limit');
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch (error) {
    throw new AuthJsonConversionError(error instanceof Error ? error.message : 'Invalid JSON');
  }

  if (!isRecord(parsed) && !(allowArray && Array.isArray(parsed))) {
    throw new AuthJsonConversionError('JSON content must be an object');
  }

  return parsed as JsonRecord | unknown[];
};

type SessionCandidate = {
  value: JsonRecord;
  path: string;
};

const collectRecords = (value: unknown) => {
  const found: JsonRecord[] = [];
  const state = createTraversalState();

  const visit = (item: unknown, depth: number) => {
    assertTraversalDepth(depth);

    if (Array.isArray(item)) {
      if (!markTraversalRecord(item, state)) return;
      item.forEach((child) => visit(child, depth + 1));
      return;
    }

    if (!isRecord(item)) return;
    if (!markTraversalRecord(item, state)) return;
    found.push(item);
    Object.values(item).forEach((child) => visit(child, depth + 1));
  };

  visit(value, 0);
  return found;
};

const hasAccessTokenFields = (record: JsonRecord) =>
  Boolean(firstNonEmptyString(record.accessToken, record.access_token));

const hasUserFields = (record: JsonRecord) =>
  Boolean(firstNonEmpty(record.email, record.name) || isRecord(record.user));

const resolveUserRecord = (value: unknown): JsonRecord | undefined => {
  if (!isRecord(value)) return undefined;
  if (firstNonEmpty(value.email, value.name)) return value;
  return resolveUserRecord(value.user);
};

const hasAccountFields = (record: JsonRecord) =>
  Boolean(
    firstNonEmpty(
      record.id,
      record.account_id,
      record.chatgpt_account_id,
      record.planType,
      record.plan_type,
      record.chatgpt_plan_type
    )
  );

const buildAggregatedSessionObject = (record: JsonRecord): JsonRecord | undefined => {
  const profile = isRecord(record.profile) ? record.profile : undefined;
  const session = isRecord(record.session) ? record.session : undefined;
  const records = collectRecords(record);
  const tokenCandidates = [
    hasAccessTokenFields(record) ? record : undefined,
    isRecord(record.token) && hasAccessTokenFields(record.token) ? record.token : undefined,
    isRecord(record.tokens) && hasAccessTokenFields(record.tokens) ? record.tokens : undefined,
    isRecord(record.credentials) && hasAccessTokenFields(record.credentials)
      ? record.credentials
      : undefined,
    session && hasAccessTokenFields(session) ? session : undefined,
    isRecord(session?.token) && hasAccessTokenFields(session.token) ? session.token : undefined,
    isRecord(session?.tokens) && hasAccessTokenFields(session.tokens) ? session.tokens : undefined,
    ...records.filter(hasAccessTokenFields),
  ].filter((candidate): candidate is JsonRecord => Boolean(candidate));
  const uniqueTokenCandidates = tokenCandidates.filter(
    (candidate, index, list) => list.findIndex((item) => item === candidate) === index
  );
  const tokenValues = Array.from(
    new Set(
      uniqueTokenCandidates
        .map((candidate) => firstNonEmptyString(candidate.accessToken, candidate.access_token))
        .filter((value): value is string => typeof value === 'string')
    )
  );
  if (tokenValues.length > 1) {
    throw new AuthJsonConversionError(
      'Multiple token candidates found in split session JSON; paste one account/session only'
    );
  }
  const tokenLike = uniqueTokenCandidates[0];
  const userLike = firstRecord(
    resolveUserRecord(record.user),
    resolveUserRecord(session?.user),
    resolveUserRecord(profile?.user),
    resolveUserRecord(profile),
    resolveUserRecord(records.find((item) => item !== tokenLike && hasUserFields(item)))
  );
  const accountLike = firstRecord(
    record.account,
    profile?.account,
    records.find((item) => item !== tokenLike && item !== userLike && hasAccountFields(item))
  );

  const accessToken = firstNonEmptyString(tokenLike?.accessToken, tokenLike?.access_token);
  const hasIdentity = Boolean(userLike || accountLike || firstNonEmpty(record.email, record.name));
  if (!accessToken || !hasIdentity) return undefined;

  return stripUnavailable({
    ...record,
    accessToken,
    access_token: tokenLike?.access_token,
    expires: firstNonEmpty(session?.expires, record.expires, tokenLike?.expires),
    expired: firstNonEmpty(session?.expired, record.expired, tokenLike?.expired),
    expires_at: firstNonEmpty(session?.expires_at, record.expires_at, tokenLike?.expires_at),
    sessionToken: firstNonEmptyString(
      tokenLike?.sessionToken,
      tokenLike?.session_token,
      record.sessionToken,
      record.session_token,
      session?.sessionToken,
      session?.session_token
    ),
    refreshToken: firstNonEmptyString(tokenLike?.refreshToken, tokenLike?.refresh_token),
    idToken: firstNonEmptyString(tokenLike?.idToken, tokenLike?.id_token),
    user: userLike,
    account: accountLike,
  }) as JsonRecord;
};

const collectSessionLikeObjects = (value: unknown) => {
  const found: SessionCandidate[] = [];
  const state = createTraversalState();
  const getCandidateAccessToken = (record: JsonRecord) =>
    firstNonEmptyString(
      record.accessToken,
      record.access_token,
      isRecord(record.token) ? record.token.accessToken : undefined,
      isRecord(record.token) ? record.token.access_token : undefined,
      isRecord(record.credentials) ? record.credentials.access_token : undefined
    );

  const visit = (item: unknown, path: string, depth: number) => {
    assertTraversalDepth(depth);

    if (Array.isArray(item)) {
      if (!markTraversalRecord(item, state)) return;
      item.forEach((child, index) => visit(child, `${path}[${index}]`, depth + 1));
      return;
    }

    if (!isRecord(item)) return;
    if (!markTraversalRecord(item, state)) return;

    const token = firstNonEmptyString(
      item.accessToken,
      item.access_token,
      isRecord(item.token) ? item.token.accessToken : undefined,
      isRecord(item.credentials) ? item.credentials.access_token : undefined
    );
    const hasUser = isRecord(item.user) || firstNonEmpty(item.email, item.name);
    if (token && hasUser) {
      found.push({ value: item, path });
      return;
    }

    Object.entries(item).forEach(([key, child]) => {
      if (key === 'accessToken' || key === 'access_token' || key === 'sessionToken') return;
      visit(child, `${path}.${key}`, depth + 1);
    });
  };

  visit(value, '$', 0);
  if (isRecord(value)) {
    const aggregated = buildAggregatedSessionObject(value);
    if (found.length === 0) {
      if (aggregated) found.push({ value: aggregated, path: '$' });
    } else if (found.length === 1 && aggregated) {
      const directToken = getCandidateAccessToken(found[0].value);
      const aggregatedToken = getCandidateAccessToken(aggregated);
      if (directToken && aggregatedToken && directToken === aggregatedToken) {
        found[0] = { value: aggregated, path: '$' };
      }
    }
  } else if (Array.isArray(value)) {
    value.forEach((item, index) => {
      if (!isRecord(item)) return;
      const aggregated = buildAggregatedSessionObject(item);
      if (!aggregated) return;

      const arrayPath = `$[${index}]`;
      if (found.length === 0) {
        found.push({ value: aggregated, path: arrayPath });
        return;
      }

      if (found.length === 1 && found[0].path.startsWith(`${arrayPath}.`)) {
        const directToken = getCandidateAccessToken(found[0].value);
        const aggregatedToken = getCandidateAccessToken(aggregated);
        if (directToken && aggregatedToken && directToken === aggregatedToken) {
          found[0] = { value: aggregated, path: arrayPath };
          return;
        }
      }

      found.push({ value: aggregated, path: arrayPath });
    });
  }
  return found;
};

const hasCpaAuthFileShape = (record: JsonRecord) => {
  const provider = firstNonEmptyString(record.type, record.provider)?.toLowerCase();
  if (!provider) return false;

  const providerCredentialKeys = PROVIDER_CREDENTIAL_KEYSETS[provider];
  const topLevelCredentialRecord = {
    access_token: record.access_token,
    id_token: record.id_token,
    refresh_token: record.refresh_token,
    session_token: record.session_token,
    api_key: record.api_key,
    key: record.key,
    private_key: record.private_key,
    authorization: record.authorization,
    bearer: record.bearer,
    cookie: record.cookie,
    cookies: record.cookies,
    token: record.token,
    accessToken: record.accessToken,
    idToken: record.idToken,
    refreshToken: record.refreshToken,
    sessionToken: record.sessionToken,
    apiKey: record.apiKey,
    privateKey: record.privateKey,
    secret: record.secret,
    session_secret: record.session_secret,
    sessionSecret: record.sessionSecret,
    client_secret: record.client_secret,
    clientSecret: record.clientSecret,
    password: record.password,
    passphrase: record.passphrase,
  } satisfies JsonRecord;
  const hasTopLevelKnownCredentials = Boolean(
    firstNonEmptyString(
      record.access_token,
      record.id_token,
      record.refresh_token,
      record.session_token,
      record.api_key,
      record.key,
      record.private_key,
      record.authorization,
      record.bearer,
      record.cookie,
      record.cookies,
      record.token,
      record.accessToken,
      record.idToken,
      record.refreshToken,
      record.sessionToken,
      record.apiKey,
      record.privateKey
    )
  );

  if (providerCredentialKeys) {
    if (hasTopLevelKnownCredentials) {
      if (hasCredentialValueForKeySet(topLevelCredentialRecord, providerCredentialKeys)) {
        return true;
      }
    }

    return CREDENTIAL_CONTAINER_KEYS.some((containerKey) =>
      hasCredentialValueForKeySet(record[containerKey], providerCredentialKeys)
    );
  }

  return (
    hasCredentialValueForKeySet(topLevelCredentialRecord, GENERIC_CREDENTIAL_KEYS) ||
    CREDENTIAL_CONTAINER_KEYS.some((containerKey) =>
      hasCredentialValueForKeySet(record[containerKey], GENERIC_CREDENTIAL_KEYS)
    )
  );
};

const hasForbiddenInvisibleCharacter = (
  value: unknown,
  depth = 0,
  state: TraversalState = createTraversalState()
): boolean => {
  assertTraversalDepth(depth);

  if (typeof value === 'string') {
    return Array.from(value).some((char) => {
      const codePoint = char.codePointAt(0);
      return codePoint !== undefined && FORBIDDEN_INVISIBLE_CODE_POINTS.has(codePoint);
    });
  }
  if (Array.isArray(value)) {
    if (!markTraversalRecord(value, state)) return false;
    return value.some((item) => hasForbiddenInvisibleCharacter(item, depth + 1, state));
  }
  if (!isRecord(value)) return false;
  if (!markTraversalRecord(value, state)) return false;

  return Object.entries(value).some(
    ([key, item]) =>
      hasForbiddenInvisibleCharacter(key, depth + 1, state) ||
      hasForbiddenInvisibleCharacter(item, depth + 1, state)
  );
};

const hasUnsafeCpaIdToken = (
  value: unknown,
  depth = 0,
  state: TraversalState = createTraversalState()
): boolean => {
  assertTraversalDepth(depth);

  if (Array.isArray(value)) {
    if (!markTraversalRecord(value, state)) return false;
    return value.some((item) => hasUnsafeCpaIdToken(item, depth + 1, state));
  }
  if (!isRecord(value)) return false;
  if (!markTraversalRecord(value, state)) return false;

  return Object.entries(value).some(([key, item]) => {
    const normalizedKey = key.toLowerCase();
    if ((normalizedKey === 'id_token' || normalizedKey === 'idtoken') && isUnsafeIdToken(item)) {
      return true;
    }
    return hasUnsafeCpaIdToken(item, depth + 1, state);
  });
};

const convertSessionToCpaAuthJson = (record: JsonRecord, now: Date): JsonRecord => {
  const token = isRecord(record.token) ? record.token : undefined;
  const credentials = isRecord(record.credentials) ? record.credentials : undefined;
  const user = isRecord(record.user) ? record.user : undefined;
  const account = isRecord(record.account) ? record.account : undefined;
  const profileRecord = isRecord(record.profile) ? record.profile : undefined;
  const profileAccount = isRecord(profileRecord?.account) ? profileRecord.account : undefined;

  const accessToken = firstNonEmptyString(
    record.accessToken,
    record.access_token,
    token?.accessToken,
    token?.access_token,
    credentials?.access_token
  );
  if (!accessToken) throw new AuthJsonConversionError('Missing accessToken');

  const sessionToken = firstNonEmptyString(
    record.sessionToken,
    record.session_token,
    token?.sessionToken,
    token?.session_token,
    credentials?.session_token
  );
  const refreshToken = firstNonEmptyString(
    record.refreshToken,
    record.refresh_token,
    token?.refreshToken,
    token?.refresh_token,
    credentials?.refresh_token
  );
  const inputIdToken = firstNonEmptyString(
    record.idToken,
    record.id_token,
    token?.idToken,
    token?.id_token,
    credentials?.id_token
  );

  // Parse token payloads defensively for malformed/oversized-token safety.
  // Explicit session expiration fields remain canonical; JWT exp is fallback only.
  const accessTokenPayload = parseJwtPayload(accessToken);
  parseJwtPayload(inputIdToken);
  const expiresAt = firstNonEmpty(
    normalizeTimestamp(record.expires),
    normalizeTimestamp(record.expired),
    normalizeTimestamp(record.expires_at),
    normalizeTimestamp(accessTokenPayload?.exp)
  );
  const email = firstNonEmpty(user?.email, record.email, credentials?.email);
  const accountId = firstNonEmpty(
    account?.id,
    account?.account_id,
    account?.chatgpt_account_id,
    profileAccount?.id,
    profileAccount?.account_id,
    profileAccount?.chatgpt_account_id,
    record.account_id,
    record.chatgpt_account_id,
    credentials?.chatgpt_account_id
  );
  const planType = firstNonEmpty(
    account?.planType,
    account?.plan_type,
    account?.chatgpt_plan_type,
    profileAccount?.planType,
    profileAccount?.plan_type,
    profileAccount?.chatgpt_plan_type,
    record.plan_type,
    record.chatgpt_plan_type,
    credentials?.plan_type,
    credentials?.chatgpt_plan_type
  );
  const idToken = isUnsafeIdToken(inputIdToken) ? undefined : firstNonEmptyString(inputIdToken);
  const name = firstNonEmpty(email, record.name, 'ChatGPT Account');

  return stripUnavailable({
    type: 'codex',
    account_id: accountId,
    chatgpt_account_id: accountId,
    email,
    name,
    plan_type: planType,
    chatgpt_plan_type: planType,
    id_token: idToken,
    access_token: accessToken,
    refresh_token: refreshToken,
    session_token: sessionToken,
    last_refresh: normalizeTimestamp(now),
    expired: expiresAt,
    disabled: Boolean(record.disabled) || undefined,
  }) as JsonRecord;
};

const getSub2ApiAccountLabel = (account: JsonRecord, index: number) => {
  const name = firstNonEmptyString(account.name);
  return name ? `"${name}"` : `#${index + 1}`;
};

const isSub2ApiOpenAIOAuthAccount = (account: JsonRecord) =>
  firstNonEmptyString(account.platform)?.toLowerCase() === 'openai' &&
  firstNonEmptyString(account.type)?.toLowerCase() === 'oauth';

const collectSub2ApiAccounts = (
  value: unknown
): { accounts: JsonRecord[]; exportedAt?: unknown } => {
  if (Array.isArray(value)) {
    return {
      accounts: value.filter((item): item is JsonRecord => isRecord(item)),
    };
  }

  if (!isRecord(value)) return { accounts: [] };

  if (Array.isArray(value.accounts)) {
    return {
      accounts: value.accounts.filter((item): item is JsonRecord => isRecord(item)),
      exportedAt: value.exported_at,
    };
  }

  if (isRecord(value.data) && Array.isArray(value.data.accounts)) {
    return {
      accounts: value.data.accounts.filter((item): item is JsonRecord => isRecord(item)),
      exportedAt: value.data.exported_at,
    };
  }

  if (isRecord(value.credentials) && firstNonEmptyString(value.platform, value.type)) {
    return {
      accounts: [value],
      exportedAt: value.exported_at,
    };
  }

  return { accounts: [] };
};

const readSub2ApiCredentialString = (
  credentials: JsonRecord,
  extra: JsonRecord | undefined,
  ...keys: string[]
) => {
  const values = keys.flatMap((key) => [credentials[key], extra?.[key]]);
  return firstNonEmptyString(...values);
};

const convertSub2ApiAccountToCpaAuthJson = (
  account: JsonRecord,
  exportedAt: unknown,
  now: Date,
  index: number
): JsonRecord => {
  const credentials = isRecord(account.credentials) ? account.credentials : undefined;
  if (!credentials) {
    throw new AuthJsonConversionError(
      `sub2api OpenAI OAuth account ${getSub2ApiAccountLabel(account, index)} is missing credentials`
    );
  }

  const extra = isRecord(account.extra) ? account.extra : undefined;
  const accessToken = firstNonEmptyString(credentials.access_token, credentials.accessToken);
  if (!accessToken) {
    throw new AuthJsonConversionError(
      `sub2api OpenAI OAuth account ${getSub2ApiAccountLabel(account, index)} is missing credentials.access_token`
    );
  }

  const inputIdToken = firstNonEmptyString(credentials.id_token, credentials.idToken);
  const idToken = isUnsafeIdToken(inputIdToken) ? undefined : firstNonEmptyString(inputIdToken);
  const email = readSub2ApiCredentialString(
    credentials,
    extra,
    'email',
    'email_address',
    'emailAddress'
  );
  const accountId = readSub2ApiCredentialString(
    credentials,
    extra,
    'chatgpt_account_id',
    'chatgptAccountId',
    'account_id',
    'accountId'
  );
  const userId = readSub2ApiCredentialString(
    credentials,
    extra,
    'chatgpt_user_id',
    'chatgptUserId',
    'user_id',
    'userId'
  );
  const planType = readSub2ApiCredentialString(
    credentials,
    extra,
    'plan_type',
    'planType',
    'chatgpt_plan_type',
    'chatgptPlanType'
  );
  const organizationId = readSub2ApiCredentialString(
    credentials,
    extra,
    'organization_id',
    'organizationId',
    'org_id',
    'orgId',
    'poid'
  );
  const expiresAt = firstNonEmpty(
    normalizeTimestamp(credentials.expires_at),
    normalizeTimestamp(credentials.expiresAt),
    normalizeTimestamp(account.expires_at),
    normalizeTimestamp(account.expiresAt)
  );
  const lastRefresh = firstNonEmpty(
    normalizeTimestamp(exportedAt),
    normalizeTimestamp(account.exported_at),
    normalizeTimestamp(now)
  );
  const status = firstNonEmptyString(account.status);
  const disabled =
    typeof account.disabled === 'boolean'
      ? account.disabled || undefined
      : status && status.toLowerCase() !== 'active'
        ? true
        : undefined;
  const name = firstNonEmpty(account.name, email, accountId, 'OpenAI OAuth Account');

  return stripUnavailable({
    type: 'codex',
    account_id: accountId,
    chatgpt_account_id: accountId,
    chatgpt_user_id: userId,
    organization_id: organizationId,
    email,
    name,
    plan_type: planType,
    chatgpt_plan_type: planType,
    id_token: idToken,
    access_token: accessToken,
    refresh_token: firstNonEmptyString(credentials.refresh_token, credentials.refreshToken),
    client_id: firstNonEmptyString(credentials.client_id, credentials.clientId),
    last_refresh: lastRefresh,
    expired: expiresAt,
    disabled,
  }) as JsonRecord;
};

const convertSub2ApiToCpaAuthJson = (value: unknown, now: Date): AuthJsonConversionResult => {
  const { accounts, exportedAt } = collectSub2ApiAccounts(value);
  const openAiOauthAccounts = accounts.filter(isSub2ApiOpenAIOAuthAccount);
  if (openAiOauthAccounts.length === 0) {
    throw new AuthJsonConversionError(
      'No sub2api OpenAI OAuth account with credentials.access_token was found'
    );
  }

  const converted = openAiOauthAccounts.map((account, index) =>
    convertSub2ApiAccountToCpaAuthJson(account, exportedAt, now, index)
  );

  return converted.length === 1 ? converted[0] : converted;
};

export const convertAuthJsonInput = (
  text: string,
  type: AuthJsonInputType,
  now = new Date()
): AuthJsonConversionResult => {
  const parsed = parseJsonObject(text, type === 'session' || type === 'sub2api');
  if (hasForbiddenInvisibleCharacter(parsed)) {
    throw new AuthJsonConversionError('Auth JSON contains unsupported invisible characters');
  }
  if (type === 'cpa') {
    if (hasUnsafeCpaIdToken(parsed)) {
      throw new AuthJsonConversionError('CPA auth JSON contains unsupported id_token');
    }
    if (!isRecord(parsed) || !hasCpaAuthFileShape(parsed)) {
      throw new AuthJsonConversionError('CPA auth JSON is missing required auth fields');
    }
    return parsed;
  }

  if (type === 'sub2api') {
    return convertSub2ApiToCpaAuthJson(parsed, now);
  }

  const sessions = collectSessionLikeObjects(parsed);
  if (sessions.length === 0) {
    throw new AuthJsonConversionError('No ChatGPT session object with accessToken was found');
  }
  if (sessions.length > 1) {
    throw new AuthJsonConversionError(
      'Multiple ChatGPT session objects found; paste one session only'
    );
  }

  return convertSessionToCpaAuthJson(sessions[0].value, now);
};

const stableStringifyForFingerprint = (value: unknown): string => {
  if (value === null) return 'null';
  if (Array.isArray(value)) {
    return `[${value.map(stableStringifyForFingerprint).join(',')}]`;
  }
  if (isRecord(value)) {
    return `{${Object.keys(value)
      .sort()
      .filter((key) => !GENERIC_CREDENTIAL_KEYS.has(key.toLowerCase()))
      .map((key) => `${JSON.stringify(key)}:${stableStringifyForFingerprint(value[key])}`)
      .join(',')}}`;
  }
  return JSON.stringify(value) ?? String(value);
};

const buildAuthFileFingerprint = (value: unknown) => {
  const text = stableStringifyForFingerprint(value);
  let hash = 0x811c9dc5;
  for (let index = 0; index < text.length; index += 1) {
    hash ^= text.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193);
  }
  return (hash >>> 0).toString(16).padStart(8, '0');
};

const buildSafeFileNameSegment = (
  value: unknown,
  {
    fallback = '',
    maxLength = 80,
    preserveEmailSymbols = false,
  }: { fallback?: string; maxLength?: number; preserveEmailSymbols?: boolean } = {}
) => {
  const raw = String(value ?? '')
    .trim()
    .replace(/\.json$/iu, '')
    .toLowerCase();
  const unsupportedPattern = preserveEmailSymbols ? /[^a-z0-9@._+-]+/g : /[^a-z0-9]+/g;
  const safeName = raw
    .replace(/[\\/:*?"<>|]+/g, '-')
    .replace(unsupportedPattern, '-')
    .replace(/-+/g, '-')
    .replace(/\.{2,}/g, '.')
    .replace(/^[.-]+|[.-]+$/g, '')
    .slice(0, maxLength)
    .replace(/[.-]+$/g, '');
  const safeBaseName = WINDOWS_RESERVED_BASE_NAMES.has(safeName) ? `${safeName}-account` : safeName;
  return safeBaseName || fallback;
};

const getDefaultAuthFileIdSegment = (authJson: JsonRecord) => {
  const rawId = firstNonEmpty(
    authJson.account_id,
    authJson.chatgpt_account_id,
    authJson.organization_id
  );
  return buildSafeFileNameSegment(rawId, { maxLength: 8 }) || buildAuthFileFingerprint(authJson);
};

export const getDefaultSessionAuthFileName = (authJson: JsonRecord) => {
  const provider = buildSafeFileNameSegment(firstNonEmpty(authJson.type, authJson.provider), {
    fallback: 'codex',
    maxLength: 24,
  });
  const id = getDefaultAuthFileIdSegment(authJson);
  const identity = buildSafeFileNameSegment(
    firstNonEmpty(authJson.email, authJson.name, authJson.account_id, 'account'),
    {
      fallback: 'account',
      maxLength: 96,
      preserveEmailSymbols: true,
    }
  );
  const plan = buildSafeFileNameSegment(firstNonEmpty(authJson.plan_type, authJson.chatgpt_plan_type), {
    maxLength: 32,
  });
  const baseName = [provider, id, identity, plan].filter(Boolean).join('-');

  return `${baseName}.json`;
};

export const getDefaultSub2ApiAuthFileName = (authJson: AuthJsonConversionResult) => {
  if (!Array.isArray(authJson)) return getDefaultSessionAuthFileName(authJson);
  if (authJson.length === 1) return getDefaultSessionAuthFileName(authJson[0]);
  return 'sub2api-codex-accounts.codex.json';
};
