import { useEffect, useMemo, useRef, useState } from 'react';
import { fullWebLanguage, subscribeFullWebLanguage } from '../i18n/fullweb';
import { accessInfoText as a, type FreshAccessInfoTextKey } from '../i18n/freshAccessInfo';
import { FullWebIcon } from './FullWebIcon';
import {
  PublicApiError,
  type FullWebCredential,
  type PublicApiClient,
} from './publicApi';

export type AccessMode =
  | 'new_managed'
  | 'adopt_existing'
  | 'connect_external'
  | 'migrate_legacy'
  | '';

export interface AccessProtocol {
  id: string;
  label: string;
  basePath: string;
  examplePath: string;
}

export interface AccessInfo {
  mode: AccessMode;
  managementBasePath: string;
  inferenceBasePath: string;
  externalBaseUrl?: string;
  protocols: AccessProtocol[];
  ready: boolean;
  code?: string;
}

export interface AccessInfoSummaryProps {
  client: PublicApiClient;
  auth: FullWebCredential;
  onUnauthorized?: () => void;
  info?: AccessInfo;
  pendingRestart?: boolean;
  compact?: boolean;
  reloadToken?: number;
  origin?: string;
  className?: string;
  testIdPrefix?: string;
  onLoaded?: (info: AccessInfo) => void;
  defaultProtocolsCollapsed?: boolean;
  subtitleNote?: string;
  completionRecovery?: boolean;
}

const VALID_MODES: Record<AccessMode, true> = {
  new_managed: true,
  adopt_existing: true,
  connect_external: true,
  migrate_legacy: true,
  '': true,
};

const MAX_RELATIVE_PATH_LEN = 256;
const MAX_URL_LEN = 1024;
const MAX_PROTOCOL_ID_LEN = 48;
const MAX_PROTOCOL_LABEL_LEN = 80;
const MAX_CODE_LEN = 96;
const SEGMENT_RE = /^[A-Za-z0-9._~$&+,:;=@-]+$/;
const PROTOCOL_ID_RE = /^[A-Za-z0-9._-]+$/;
const CODE_RE = /^[A-Za-z0-9_:-]+$/;

function hasUnsafeCharacters(value: string): boolean {
  for (let i = 0; i < value.length; i++) {
    const code = value.charCodeAt(i);
    if (code <= 0x1f || code === 0x7f || /\s/.test(value[i])) return true;
  }
  return false;
}

export function isValidRelativeAccessPath(value: unknown): value is string {
  if (typeof value !== 'string') return false;
  if (!value || value.length > MAX_RELATIVE_PATH_LEN) return false;
  if (!value.startsWith('/') || value.startsWith('//')) return false;
  if (value === '/') return false;
  if (value.endsWith('/')) return false;
  if (
    value.includes('\\') ||
    value.includes('?') ||
    value.includes('#') ||
    value.includes('%') ||
    hasUnsafeCharacters(value)
  ) {
    return false;
  }
  const segments = value.slice(1).split('/');
  return segments.every(
    (segment) =>
      segment.length > 0 && segment !== '.' && segment !== '..' && SEGMENT_RE.test(segment)
  );
}

export function isAccessPathWithinPrefix(path: unknown, prefix: unknown): path is string {
  if (!isValidRelativeAccessPath(path) || !isValidRelativeAccessPath(prefix)) return false;
  return path === prefix || path.startsWith(`${prefix}/`);
}

export function isValidExternalAccessUrl(value: unknown): value is string {
  if (typeof value !== 'string') return false;
  if (!value || value.length > MAX_URL_LEN || hasUnsafeCharacters(value)) return false;
  try {
    const parsed = new URL(value);
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return false;
    if (!parsed.hostname || parsed.username || parsed.password) return false;
    if (parsed.search || parsed.hash) return false;
    return true;
  } catch {
    return false;
  }
}

export function isExternalEndpointWithinBase(endpoint: unknown, externalBaseUrl?: string): boolean {
  if (!isValidExternalAccessUrl(endpoint)) return false;
  if (!externalBaseUrl) return false;
  if (!isValidExternalAccessUrl(externalBaseUrl)) return false;
  const ep = new URL(endpoint);
  const base = new URL(externalBaseUrl);
  if (ep.origin !== base.origin) return false;
  const basePath = base.pathname.replace(/\/+$/, '');
  const epPath = ep.pathname.replace(/\/+$/, '') || '/';
  if (basePath === '' || basePath === '/') return true;
  return epPath === basePath || epPath.startsWith(`${basePath}/`);
}

export function resolveAccessOrigin(explicitOrigin?: string): string {
  if (explicitOrigin !== undefined) {
    if (!explicitOrigin || !isValidExternalAccessUrl(explicitOrigin)) return '';
    return new URL(explicitOrigin).origin;
  }
  if (typeof window !== 'undefined' && window.location && typeof window.location.origin === 'string') {
    const candidate = window.location.origin;
    if (candidate !== 'null' && isValidExternalAccessUrl(candidate)) {
      return new URL(candidate).origin;
    }
  }
  return '';
}

export function formatAccessEndpointUrl(
  pathOrUrl: string,
  mode: AccessMode,
  origin: string
): string {
  if (!pathOrUrl) return '';
  if (mode === 'connect_external') {
    return isValidExternalAccessUrl(pathOrUrl) ? pathOrUrl : '';
  }
  if (!isValidRelativeAccessPath(pathOrUrl)) return '';
  if (origin) {
    return new URL(pathOrUrl, origin).href;
  }
  return pathOrUrl;
}

export function parseAccessInfo(value: unknown): AccessInfo | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const doc = value as Record<string, unknown>;
  if (
    typeof doc.mode !== 'string' ||
    !Object.prototype.hasOwnProperty.call(VALID_MODES, doc.mode)
  ) {
    return null;
  }
  const mode = doc.mode as AccessMode;
  if (typeof doc.ready !== 'boolean') return null;

  if (typeof doc.managementBasePath !== 'string') return null;
  if (doc.managementBasePath !== '' && !isValidRelativeAccessPath(doc.managementBasePath)) {
    return null;
  }

  if (typeof doc.inferenceBasePath !== 'string') return null;
  if (doc.inferenceBasePath !== '' && !isValidRelativeAccessPath(doc.inferenceBasePath)) {
    return null;
  }

  let externalBaseUrl: string | undefined;
  if (doc.externalBaseUrl !== undefined) {
    if (typeof doc.externalBaseUrl !== 'string') return null;
    if (doc.externalBaseUrl !== '') {
      if (!isValidExternalAccessUrl(doc.externalBaseUrl)) return null;
      externalBaseUrl = doc.externalBaseUrl;
    }
  }

  let code: string | undefined;
  if (doc.code !== undefined) {
    if (typeof doc.code !== 'string') return null;
    if (doc.code !== '') {
      if (doc.code.length > MAX_CODE_LEN || !CODE_RE.test(doc.code)) return null;
      code = doc.code;
    }
  }

  if (!Array.isArray(doc.protocols)) return null;
  if (mode === '' && doc.protocols.length > 0) return null;
  if (mode !== 'connect_external' && doc.ready && doc.inferenceBasePath === '') return null;
  if (mode === 'connect_external' && doc.ready && !externalBaseUrl) return null;

  const seenIds = new Set<string>();
  const protocols: AccessProtocol[] = [];
  for (const item of doc.protocols) {
    if (!item || typeof item !== 'object' || Array.isArray(item)) return null;
    const raw = item as Record<string, unknown>;
    if (
      typeof raw.id !== 'string' ||
      !raw.id ||
      raw.id.length > MAX_PROTOCOL_ID_LEN ||
      !PROTOCOL_ID_RE.test(raw.id) ||
      seenIds.has(raw.id)
    ) {
      return null;
    }
    seenIds.add(raw.id);
    if (
      typeof raw.label !== 'string' ||
      !raw.label.trim() ||
      raw.label.length > MAX_PROTOCOL_LABEL_LEN
    ) {
      return null;
    }
    for (let i = 0; i < raw.label.length; i += 1) {
      const code = raw.label.charCodeAt(i);
      if (code < 0x20 || code === 0x7f) return null;
    }
    if (typeof raw.basePath !== 'string' || typeof raw.examplePath !== 'string') return null;
    if (mode === 'connect_external') {
      if (
        !isExternalEndpointWithinBase(raw.basePath, externalBaseUrl) ||
        !isExternalEndpointWithinBase(raw.examplePath, externalBaseUrl)
      ) {
        return null;
      }
    } else {
      if (
        !doc.inferenceBasePath ||
        !isAccessPathWithinPrefix(raw.basePath, doc.inferenceBasePath) ||
        !isAccessPathWithinPrefix(raw.examplePath, doc.inferenceBasePath)
      ) {
        return null;
      }
    }
    protocols.push({
      id: raw.id,
      label: raw.label.trim(),
      basePath: raw.basePath,
      examplePath: raw.examplePath,
    });
  }

  return {
    mode,
    managementBasePath: doc.managementBasePath,
    inferenceBasePath: doc.inferenceBasePath,
    ...(externalBaseUrl ? { externalBaseUrl } : {}),
    protocols,
    ready: doc.ready,
    ...(code ? { code } : {}),
  };
}

function modeTextKey(mode: AccessMode): FreshAccessInfoTextKey {
  switch (mode) {
    case 'new_managed':
      return 'modeManaged';
    case 'adopt_existing':
      return 'modeAdopt';
    case 'connect_external':
      return 'modeExternal';
    case 'migrate_legacy':
      return 'modeMigrate';
    default:
      return 'modeUnconfigured';
  }
}

function notReadyTitleAndDesc(
  code: string | undefined,
  language: string
): { title: string; desc: string } {
  switch (code) {
    case 'upgrade_restart_required':
      return {
        title: a('pendingRestartTitle', language),
        desc: a('pendingRestartDesc', language),
      };
    case 'inference_prefix_required':
    case 'inference_prefix_unconfigured':
      return {
        title: a('notReadyInferencePrefixTitle', language),
        desc: a('notReadyInferencePrefixDesc', language),
      };
    case 'routing_unavailable':
    case 'routing_unverified':
      return {
        title: a('notReadyRoutingTitle', language),
        desc: a('notReadyRoutingDesc', language),
      };
    case 'installation_incomplete':
      return {
        title: a('notReadyInstallTitle', language),
        desc: a('notReadyInstallDesc', language),
      };
    case 'external_target_unavailable':
      return {
        title: a('notReadyExternalTitle', language),
        desc: a('notReadyExternalDesc', language),
      };
    case 'gateway_pending':
      return {
        title: a('notReadyGatewayTitle', language),
        desc: a('notReadyGatewayDesc', language),
      };
    case 'upgrade_authority_unavailable':
      return {
        title: a('notReadyUpgradeAuthorityTitle', language),
        desc: a('notReadyUpgradeAuthorityDesc', language),
      };
    case 'upgrade_operation_invalid':
      return {
        title: a('notReadyUpgradeOperationInvalidTitle', language),
        desc: a('notReadyUpgradeOperationInvalidDesc', language),
      };
    default:
      return {
        title: a('notReadyGenericTitle', language),
        desc: a('notReadyGenericDesc', language),
      };
  }
}

function protocolHelpAndHeader(
  id: string,
  language: string
): { hint: string; header: string } {
  switch (id) {
    case 'openai':
      return {
        hint: a('hintOpenai', language),
        header: a('headerAuthBearer', language),
      };
    case 'responses':
      return {
        hint: a('hintResponses', language),
        header: a('headerAuthBearer', language),
      };
    case 'anthropic':
      return {
        hint: a('hintAnthropic', language),
        header: a('headerAnthropic', language),
      };
    case 'gemini':
      return {
        hint: a('hintGemini', language),
        header: a('headerGemini', language),
      };
    default:
      return {
        hint: a('hintGeneric', language),
        header: a('headerAuthBearer', language),
      };
  }
}

export function AccessInfoSummary({
  client,
  auth,
  onUnauthorized,
  info,
  pendingRestart = false,
  compact = false,
  reloadToken = 0,
  origin,
  className,
  testIdPrefix = 'access-info',
  onLoaded,
  defaultProtocolsCollapsed = false,
  subtitleNote,
  completionRecovery = false,
}: AccessInfoSummaryProps) {
  const [language, setLanguage] = useState(fullWebLanguage);
  const [fetched, setFetched] = useState<AccessInfo | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadFailed, setLoadFailed] = useState(false);
  const [manualRetry, setManualRetry] = useState(0);
  const [copyFeedback, setCopyFeedback] = useState<{
    target: string;
    state: 'copied' | 'failed';
  } | null>(null);
  const [copiedTarget, setCopiedTarget] = useState<string | null>(null);
  const copyTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const activeRef = useRef(true);
  const requestSeqRef = useRef(0);
  const onUnauthorizedRef = useRef(onUnauthorized);
  const onLoadedRef = useRef(onLoaded);
  onUnauthorizedRef.current = onUnauthorized;
  onLoadedRef.current = onLoaded;

  useEffect(() => {
    return subscribeFullWebLanguage(() => setLanguage(fullWebLanguage()));
  }, []);

  useEffect(() => {
    activeRef.current = true;
    return () => {
      activeRef.current = false;
      if (copyTimerRef.current !== null) {
        clearTimeout(copyTimerRef.current);
        copyTimerRef.current = null;
      }
    };
  }, []);

  const providedInfo = useMemo(
    () => (info !== undefined ? parseAccessInfo(info) : undefined),
    [info]
  );

  const authFingerprint = `${auth.kind}:${auth.value}`;

  useEffect(() => {
    setCopyFeedback(null);
    setCopiedTarget(null);
    if (copyTimerRef.current !== null) {
      clearTimeout(copyTimerRef.current);
      copyTimerRef.current = null;
    }
    if (info !== undefined) {
      requestSeqRef.current += 1;
      setLoading(false);
      setLoadFailed(false);
      setFetched(null);
      return;
    }
    if (!auth.value) {
      requestSeqRef.current += 1;
      setLoading(false);
      setLoadFailed(false);
      setFetched(null);
      return;
    }

    const seq = ++requestSeqRef.current;
    const ownerAtStart = authFingerprint;
    let cancelled = false;
    setLoading(true);
    setLoadFailed(false);

    void (async () => {
      try {
        const raw = await client.get<unknown>('api-keys/access-info', {
          kind: auth.kind,
          value: auth.value,
        });
        if (
          cancelled ||
          !activeRef.current ||
          seq !== requestSeqRef.current ||
          ownerAtStart !== `${auth.kind}:${auth.value}`
        ) {
          return;
        }
        const parsed = parseAccessInfo(raw);
        if (!parsed) {
          setFetched(null);
          setLoadFailed(true);
          return;
        }
        setFetched(parsed);
        setLoadFailed(false);
        onLoadedRef.current?.(parsed);
      } catch (err) {
        if (
          cancelled ||
          !activeRef.current ||
          seq !== requestSeqRef.current ||
          ownerAtStart !== `${auth.kind}:${auth.value}`
        ) {
          return;
        }
        if (err instanceof PublicApiError && err.kind === 'authentication') {
          setFetched(null);
          setLoadFailed(false);
          onUnauthorizedRef.current?.();
          return;
        }
        setFetched(null);
        setLoadFailed(true);
      } finally {
        if (
          !cancelled &&
          activeRef.current &&
          seq === requestSeqRef.current &&
          ownerAtStart === `${auth.kind}:${auth.value}`
        ) {
          setLoading(false);
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [client, auth.kind, auth.value, authFingerprint, info, reloadToken, manualRetry]);

  const effective = info !== undefined ? providedInfo : fetched;
  const resolvedOrigin = useMemo(() => resolveAccessOrigin(origin), [origin]);

  async function copyValue(target: string, textValue: string) {
    if (!textValue) return;
    try {
      if (
        typeof navigator === 'undefined' ||
        !navigator.clipboard ||
        typeof navigator.clipboard.writeText !== 'function'
      ) {
        throw new Error('clipboard_unavailable');
      }
      await navigator.clipboard.writeText(textValue);
      if (activeRef.current) {
        setCopyFeedback({ target, state: 'copied' });
        setCopiedTarget(target);
        if (copyTimerRef.current !== null) {
          clearTimeout(copyTimerRef.current);
        }
        copyTimerRef.current = setTimeout(() => {
          if (activeRef.current) {
            setCopiedTarget((prev) => (prev === target ? null : prev));
          }
          copyTimerRef.current = null;
        }, 1500);
      }
    } catch {
      if (activeRef.current) {
        setCopiedTarget(null);
        setCopyFeedback({ target, state: 'failed' });
      }
    }
  }

  const rootClassName = className ? `journey-field ${className}` : 'journey-field';

  if (info === undefined && !auth.value) {
    return (
      <section className={rootClassName} data-testid={`${testIdPrefix}-summary`}>
        <div className="alert alert-info" role="status" data-testid={`${testIdPrefix}-idle`}>
          <FullWebIcon name="info" size={20} />
          <div className="alert-content">
            <div className="alert-title">{a('title', language)}</div>
            <div className="alert-desc">{a('awaitingAuth', language)}</div>
          </div>
        </div>
      </section>
    );
  }

  if (info === undefined && loading && !effective) {
    return (
      <section
        className={rootClassName}
        data-testid={`${testIdPrefix}-summary`}
        aria-busy="true"
      >
        <div className="alert alert-info" role="status" data-testid={`${testIdPrefix}-loading`}>
          <FullWebIcon name="clock" size={20} />
          <div className="alert-content">
            <div className="alert-title">{a('title', language)}</div>
            <div className="alert-desc">{a('loading', language)}</div>
          </div>
        </div>
      </section>
    );
  }

  if ((info !== undefined && !providedInfo) || (info === undefined && loadFailed && !effective)) {
    return (
      <section className={rootClassName} data-testid={`${testIdPrefix}-summary`}>
        <div className="alert alert-warn" role="alert" data-testid={`${testIdPrefix}-error`}>
          <FullWebIcon name="alert" size={20} />
          <div className="alert-content">
            <div className="alert-title">{a('loadErrorTitle', language)}</div>
            <div className="alert-desc">{a('loadErrorHelp', language)}</div>
            {info === undefined && (
              <div className="header-actions">
                <button
                  type="button"
                  className="btn btn-secondary btn-sm"
                  data-testid={`${testIdPrefix}-retry`}
                  onClick={() => setManualRetry((n) => n + 1)}
                >
                  <FullWebIcon name="refresh" size={16} />
                  {a('retry', language)}
                </button>
              </div>
            )}
          </div>
        </div>
      </section>
    );
  }

  if (!effective) {
    return null;
  }

  const isPendingRestart =
    !effective.ready && (pendingRestart || effective.code === 'upgrade_restart_required');
  const isSharedPrefix =
    effective.mode !== 'connect_external' &&
    effective.managementBasePath !== '' &&
    effective.managementBasePath === effective.inferenceBasePath;

  const managementUrl = effective.managementBasePath
    ? formatAccessEndpointUrl(effective.managementBasePath, 'new_managed', resolvedOrigin)
    : '';
  const inferenceUrl =
    effective.mode !== 'connect_external' && effective.inferenceBasePath
      ? formatAccessEndpointUrl(effective.inferenceBasePath, effective.mode, resolvedOrigin)
      : '';
  const externalUrl =
    effective.mode === 'connect_external' && effective.externalBaseUrl
      ? effective.externalBaseUrl
      : '';
  const recoveryCommand = 'docker exec -u 10002:10002 <container> cpa-manager-plus access show';
  const recoveryText = completionRecovery && effective.ready && managementUrl
    ? [
        `${a('consoleUrlLabel', language)}: ${managementUrl}`,
        `${a('managementApiLabel', language)}: ${managementUrl}/api/v2`,
        ...(externalUrl ? [`${a('externalBase', language)}: ${externalUrl}`] : []),
        ...(inferenceUrl ? [`${a('inferenceBase', language)}: ${inferenceUrl}`] : []),
      ].join('\n')
    : '';

  const notReadyCopy = !effective.ready
    ? notReadyTitleAndDesc(
        isPendingRestart ? 'upgrade_restart_required' : effective.code,
        language
      )
    : null;

  const hasCoreEntry = Boolean(
    managementUrl ||
      (effective.mode === 'connect_external' && externalUrl) ||
      (effective.mode !== 'connect_external' && inferenceUrl)
  );
  const subtitleText = isPendingRestart
    ? a('pendingRestartSubtitle', language)
    : subtitleNote
      ? `${a('subtitle', language)} ${subtitleNote}`
      : a('subtitle', language);

  const protocolsCard =
    effective.protocols.length > 0 ? (
      <div className="info-card access-protocols-card" data-testid={`${testIdPrefix}-protocols`}>
        {effective.protocols.map((protocol) => {
          const baseUrl = formatAccessEndpointUrl(
            protocol.basePath,
            effective.mode,
            resolvedOrigin
          );
          const exampleUrl = formatAccessEndpointUrl(
            protocol.examplePath,
            effective.mode,
            resolvedOrigin
          );
          const hasDistinctExample = Boolean(exampleUrl && exampleUrl !== baseUrl);
          const meta = protocolHelpAndHeader(protocol.id, language);
          const baseCopied = copiedTarget === `protocol-${protocol.id}`;
          const exampleCopied = copiedTarget === `protocol-example-${protocol.id}`;
          return (
            <div
              key={protocol.id}
              className="access-protocol-item"
              data-testid={`${testIdPrefix}-protocol-${protocol.id}`}
            >
              <div className="access-protocol-head">
                <strong className="access-protocol-name">
                  {protocol.label}
                  {isPendingRestart ? ` (${a('plannedPrefixTag', language)})` : ''}
                </strong>
                {!compact && (
                  <code
                    className="access-protocol-header-chip"
                    data-testid={`${testIdPrefix}-protocol-snippet-${protocol.id}`}
                  >
                    {meta.header}
                  </code>
                )}
              </div>
              {!compact && <p className="journey-help access-protocol-hint">{meta.hint}</p>}
              <div className="access-protocol-urls">
                <div className="access-protocol-url-row">
                  <span className="access-protocol-url-label">{a('baseUrlLabel', language)}</span>
                  <span className="info-val-group">
                    <code
                      className="info-val"
                      data-testid={`${testIdPrefix}-protocol-base-${protocol.id}`}
                    >
                      {baseUrl}
                    </code>
                    <button
                      type="button"
                      className={`btn btn-ghost btn-xs btn-icon-only ${
                        baseCopied ? 'is-copied' : ''
                      }`}
                      aria-label={`${a('copyUrl', language)}: ${protocol.label}`}
                      title={a('copyUrl', language)}
                      data-testid={`${testIdPrefix}-copy-${protocol.id}`}
                      onClick={() => void copyValue(`protocol-${protocol.id}`, baseUrl)}
                    >
                      <FullWebIcon name={baseCopied ? 'check' : 'copy'} size={14} />
                    </button>
                  </span>
                </div>
                {hasDistinctExample && (
                  <div className="access-protocol-url-row">
                    <span className="access-protocol-url-label">
                      {a('exampleEndpointLabel', language)}
                    </span>
                    <span className="info-val-group">
                      <code
                        className="info-val"
                        data-testid={`${testIdPrefix}-protocol-example-${protocol.id}`}
                      >
                        {exampleUrl}
                      </code>
                      <button
                        type="button"
                        className={`btn btn-ghost btn-xs btn-icon-only ${
                          exampleCopied ? 'is-copied' : ''
                        }`}
                        aria-label={`${a('copyExample', language)}: ${protocol.label}`}
                        title={a('copyExample', language)}
                        data-testid={`${testIdPrefix}-copy-example-${protocol.id}`}
                        onClick={() =>
                          void copyValue(`protocol-example-${protocol.id}`, exampleUrl)
                        }
                      >
                        <FullWebIcon name={exampleCopied ? 'check' : 'copy'} size={14} />
                      </button>
                    </span>
                  </div>
                )}
              </div>
            </div>
          );
        })}
      </div>
    ) : null;

  return (
    <section
      className={`${rootClassName} access-info-section`}
      data-testid={`${testIdPrefix}-summary`}
      aria-busy={loading || undefined}
    >
      <div className="access-info-header">
        <div className="access-info-title-row">
          <h3>{a('title', language)}</h3>
          <div className="access-info-badges">
            <span className="badge badge-indigo" data-testid={`${testIdPrefix}-mode`}>
              {a(modeTextKey(effective.mode), language)}
            </span>
            <span
              className={`badge ${effective.ready ? 'badge-emerald' : 'badge-amber'}`}
              data-testid={`${testIdPrefix}-status`}
            >
              {effective.ready
                ? a('statusReady', language)
                : isPendingRestart
                  ? a('statusPendingRestart', language)
                  : a('statusNotReady', language)}
            </span>
            {isSharedPrefix && (
              <span className="badge badge-amber" data-testid={`${testIdPrefix}-shared-badge`}>
                {a('sharedBadge', language)}
              </span>
            )}
            {info === undefined &&
              (effective.ready ? (
                <button
                  type="button"
                  className="btn btn-ghost btn-xs btn-icon-only"
                  aria-label={a('retry', language)}
                  title={a('retry', language)}
                  data-testid={`${testIdPrefix}-retry`}
                  disabled={loading}
                  onClick={() => setManualRetry((n) => n + 1)}
                >
                  <FullWebIcon name="refresh" size={14} />
                </button>
              ) : (
                <button
                  type="button"
                  className="btn btn-ghost btn-sm"
                  data-testid={`${testIdPrefix}-retry`}
                  disabled={loading}
                  onClick={() => setManualRetry((n) => n + 1)}
                >
                  <FullWebIcon name="refresh" size={15} />
                  {a('retry', language)}
                </button>
              ))}
          </div>
        </div>
        <p className="journey-help access-info-subtitle">{subtitleText}</p>
      </div>

      {notReadyCopy && (
        <div
          className="alert alert-warn"
          role="status"
          data-testid={`${testIdPrefix}-not-ready`}
        >
          <FullWebIcon name="alert" size={20} />
          <div className="alert-content">
            <div className="alert-title">{notReadyCopy.title}</div>
            <div className="alert-desc">{notReadyCopy.desc}</div>
          </div>
        </div>
      )}

      {isSharedPrefix && (
        <div
          className="alert alert-warn"
          role="status"
          data-testid={`${testIdPrefix}-shared-warning`}
        >
          <FullWebIcon name="alert" size={20} />
          <div className="alert-content">
            <div className="alert-title">{a('sharedWarningTitle', language)}</div>
            <div className="alert-desc">{a('sharedWarningDesc', language)}</div>
          </div>
        </div>
      )}

      {hasCoreEntry && (
        <div className="info-card prefix-preview-card">
          {managementUrl && (
            <div className="info-row info-row-primary">
              <span className="info-key">
                {a('managementEntry', language)}
                {isPendingRestart ? ` (${a('plannedPrefixTag', language)})` : ''}
              </span>
              <span className="info-val-group">
                <code className="info-val" data-testid={`${testIdPrefix}-management`}>
                  {managementUrl}
                </code>
                <button
                  type="button"
                  className={`btn btn-ghost btn-xs btn-icon-only ${
                    copiedTarget === 'management' ? 'is-copied' : ''
                  }`}
                  aria-label={`${a('copyUrl', language)}: ${a('managementEntry', language)}`}
                  title={a('copyUrl', language)}
                  data-testid={`${testIdPrefix}-copy-management`}
                  onClick={() => void copyValue('management', managementUrl)}
                >
                  <FullWebIcon name={copiedTarget === 'management' ? 'check' : 'copy'} size={14} />
                </button>
              </span>
            </div>
          )}

          {effective.mode === 'connect_external' && externalUrl && (
            <div className="info-row">
              <span className="info-key">
                {a('externalBase', language)}
                {isPendingRestart ? ` (${a('plannedPrefixTag', language)})` : ''}
              </span>
              <span className="info-val-group">
                <code className="info-val" data-testid={`${testIdPrefix}-external`}>
                  {externalUrl}
                </code>
                <button
                  type="button"
                  className={`btn btn-ghost btn-xs btn-icon-only ${
                    copiedTarget === 'external' ? 'is-copied' : ''
                  }`}
                  aria-label={`${a('copyUrl', language)}: ${a('externalBase', language)}`}
                  title={a('copyUrl', language)}
                  data-testid={`${testIdPrefix}-copy-external`}
                  onClick={() => void copyValue('external', externalUrl)}
                >
                  <FullWebIcon name={copiedTarget === 'external' ? 'check' : 'copy'} size={14} />
                </button>
              </span>
            </div>
          )}

          {effective.mode !== 'connect_external' && inferenceUrl && (
            <div className="info-row">
              <span className="info-key">
                {a('inferenceBase', language)}
                {isPendingRestart ? ` (${a('plannedPrefixTag', language)})` : ''}
              </span>
              <span className="info-val-group">
                <code className="info-val" data-testid={`${testIdPrefix}-inference`}>
                  {inferenceUrl}
                </code>
                <button
                  type="button"
                  className={`btn btn-ghost btn-xs btn-icon-only ${
                    copiedTarget === 'inference' ? 'is-copied' : ''
                  }`}
                  aria-label={`${a('copyUrl', language)}: ${a('inferenceBase', language)}`}
                  title={a('copyUrl', language)}
                  data-testid={`${testIdPrefix}-copy-inference`}
                  onClick={() => void copyValue('inference', inferenceUrl)}
                >
                  <FullWebIcon name={copiedTarget === 'inference' ? 'check' : 'copy'} size={14} />
                </button>
              </span>
            </div>
          )}
        </div>
      )}

      {effective.mode === 'connect_external' && externalUrl && (
        <p className="journey-help" data-testid={`${testIdPrefix}-external-note`}>
          {a('externalDirectNote', language)}
        </p>
      )}

      {recoveryText && (
        <div className="journey-field">
          <div className="header-actions">
            <button
              type="button"
              className="btn btn-secondary btn-sm"
              data-testid={`${testIdPrefix}-copy-access`}
              onClick={() => void copyValue('access', recoveryText)}
            >
              <FullWebIcon name={copiedTarget === 'access' ? 'check' : 'copy'} size={14} />
              {a('copyAccess', language)}
            </button>
          </div>
          {copyFeedback?.target === 'access' && copyFeedback.state === 'failed' && (
            <textarea
              className="form-input form-input-mono"
              aria-label={a('copyAccess', language)}
              data-testid={`${testIdPrefix}-copy-access-fallback`}
              readOnly
              rows={3}
              value={recoveryText}
            />
          )}
          <p className="journey-help">
            {a('recoverDockerHelp', language)} <code className="info-val">{recoveryCommand}</code>
          </p>
        </div>
      )}

      {protocolsCard ? (
        defaultProtocolsCollapsed ? (
          <details
            className="journey-advanced access-protocols-disclosure"
            data-testid={`${testIdPrefix}-protocols-disclosure`}
          >
            <summary className="journey-advanced-summary">
              <span className="journey-advanced-title">
                <FullWebIcon
                  name="chevronRight"
                  size={15}
                  className="journey-advanced-chevron"
                />
                {a('protocolsTitle', language)}
              </span>
              <span className="journey-advanced-meta">
                {effective.protocols.map((item) => item.label).join(' · ')}
              </span>
            </summary>
            <div className="journey-advanced-body access-protocols-body">{protocolsCard}</div>
          </details>
        ) : (
          protocolsCard
        )
      ) : (
        <p className="journey-quiet" data-testid={`${testIdPrefix}-protocols-empty`}>
          {a('protocolsEmpty', language)}
        </p>
      )}

      {copyFeedback && (
        <p
          className={copyFeedback.state === 'copied' ? 'sr-only' : 'journey-inline-error'}
          role="status"
          data-testid={`${testIdPrefix}-copy-feedback`}
        >
          {a(copyFeedback.state === 'copied' ? 'copied' : 'copyFailed', language)}
        </p>
      )}

      <p className="journey-help access-client-key-note">{a('clientKeyNote', language)}</p>
    </section>
  );
}
