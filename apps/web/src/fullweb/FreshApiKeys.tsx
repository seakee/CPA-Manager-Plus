import { useEffect, useRef, useState } from 'react';
import { fullWebText as t } from '../i18n/fullweb';
import { Modal } from '../components/ui/Modal';
import { DropdownMenu } from '../components/ui/DropdownMenu';
import { keyWorkspaceText as k } from '../i18n/freshKeyWorkspace';
import { FullWebIcon } from './FullWebIcon';
import './freshKeys.css';
import { PublicApiError, type createPublicApiClient } from './publicApi';
import { FreshKeyDetails } from './FreshKeyDetails';
import { FreshKeyRevocation } from './FreshKeyRevocation';
import { validKey, type Key } from './freshKeyMetadata';

export function FreshApiKeys({
  client,
  credential,
  onUnauthorized,
  onCreationPending,
  storedBadge,
  pageDescription,
  visible = true,
}: {
  client: ReturnType<typeof createPublicApiClient>;
  credential: string;
  onUnauthorized: () => void;
  onCreationPending: (pending: boolean) => void;
  storedBadge?: string;
  pageDescription?: string;
  visible?: boolean;
}) {
  const [keys, setKeys] = useState<Key[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [pending, setPending] = useState('');
  const [retryAllowed, setRetryAllowed] = useState(false);
  const [secret, setSecret] = useState('');
  const [copyFeedback, setCopyFeedback] = useState<'copied' | 'failed' | ''>('');
  const [confirmed, setConfirmed] = useState(false);
  const [createName, setCreateName] = useState('');
  const [createOpen, setCreateOpen] = useState(false);
  const [failedCreateName, setFailedCreateName] = useState<{
    id: string;
    message: string;
  } | null>(null);
  const [selected, setSelected] = useState('');
  const [search, setSearch] = useState('');
  const [filter, setFilter] = useState<'all' | 'saved' | 'replaced'>('saved');
  const [viewMode, setViewMode] = useState<'table' | 'cards'>('table');
  const [detailPending, setDetailPending] = useState(false);
  const [revocationAvailable, setRevocationAvailable] = useState(false);
  const [operationPending, setOperationPending] = useState(false);
  const [revocationOpen, setRevocationOpen] = useState(false);
  const [revocationPreset, setRevocationPreset] = useState<{
    targetId: string;
    mode: 'revoke' | 'replace';
    seq: number;
  } | null>(null);
  const detailPendingRef = useRef(false);
  const creationPendingRef = useRef(false);
  const operationPendingRef = useRef(false);
  const alive = useRef(true);
  const inFlight = useRef(false);
  const copyAttempt = useRef(0);
  const secretPendingRef = useRef(false);
  const auth = { kind: 'admin' as const, value: credential };

  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  // Multiple Key operations share the Console exit lock; a completed read in
  // one panel must not release another panel's unresolved write.
  function reportPending() {
    onCreationPending(
      creationPendingRef.current ||
        secretPendingRef.current ||
        detailPendingRef.current ||
        operationPendingRef.current
    );
  }

  function failure(err: unknown) {
    if (err instanceof PublicApiError && err.kind === 'authentication') onUnauthorized();
    else setError(t('keysUnavailable'));
  }

  async function refresh() {
    if (inFlight.current || selected || detailPendingRef.current) return;
    inFlight.current = true;
    setBusy(true);
    setError('');
    try {
      const result = await client.get<{
        keys: Key[];
        gatewayPending: boolean;
        revocationAvailable: boolean;
      }>('api-keys', auth);
      if (!alive.current) return;
      if (
        !Array.isArray(result.keys) ||
        !result.keys.every(validKey) ||
        new Set(result.keys.map((key) => key.id)).size !== result.keys.length ||
        result.gatewayPending !== true ||
        typeof result.revocationAvailable !== 'boolean'
      )
        throw new Error();
      setKeys(result.keys);
      setRevocationAvailable(result.revocationAvailable);
    } catch (err) {
      if (alive.current) failure(err);
    } finally {
      inFlight.current = false;
      if (alive.current) setBusy(false);
    }
  }

  // No credential, secret or request journal enters persistent browser storage.
  useEffect(() => {
    void refresh();
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  async function copyCreatedSecret() {
    if (!secret) return;
    const attempt = ++copyAttempt.current;
    setCopyFeedback('');
    try {
      if (!navigator.clipboard?.writeText) throw new Error('Clipboard unavailable');
      await navigator.clipboard.writeText(secret);
      if (alive.current && attempt === copyAttempt.current) setCopyFeedback('copied');
    } catch {
      if (alive.current && attempt === copyAttempt.current) setCopyFeedback('failed');
    }
  }

  function dismissCreatedSecret() {
    ++copyAttempt.current;
    setCopyFeedback('');
    setSecret('');
    secretPendingRef.current = false;
    if (!failedCreateName) setCreateOpen(false);
    reportPending();
  }

  async function createKey() {
    if (
      inFlight.current ||
      selected ||
      detailPendingRef.current ||
      secret ||
      failedCreateName ||
      (pending && !retryAllowed) ||
      operationPendingRef.current
    )
      return;
    const desiredName = createName.trim();
    if ([...desiredName].length > 120) return;
    inFlight.current = true;
    setBusy(true);
    setError('');
    setConfirmed(false);
    setRetryAllowed(false);
    ++copyAttempt.current;
    setCopyFeedback('');
    try {
      const requestId =
        pending ||
        Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) =>
          byte.toString(16).padStart(2, '0')
        ).join('');
      setPending(requestId);
      creationPendingRef.current = true;
      reportPending();
      const result = await client.post<{ key: Key; createdSecret?: string }>(
        'api-keys',
        { requestId },
        auth
      );
      if (!alive.current) return;
      if (
        !validKey(result.key) ||
        result.key.requestId !== requestId ||
        (result.createdSecret !== undefined &&
          (!/^cpamp_sk_[a-f0-9]{64}$/.test(result.createdSecret) ||
            result.key.maskedPreview !== 'cpamp_sk_…' + result.createdSecret.slice(-4)))
      )
        throw new Error();
      setKeys((previous) => [
        ...(previous || []).filter((key) => key.id !== result.key.id),
        result.key,
      ]);
      setSecret(result.createdSecret || '');
      secretPendingRef.current = !!result.createdSecret;
      setConfirmed(true);
      setPending('');
      if (desiredName) {
        try {
          const namedKey = await client.put<Key>(
            'api-keys/' + result.key.id,
            { name: desiredName, nameRevision: result.key.nameRevision },
            auth
          );
          if (!alive.current) return;
          if (
            !validKey(namedKey) ||
            namedKey.id !== result.key.id ||
            namedKey.name !== desiredName ||
            namedKey.nameRevision !== result.key.nameRevision + 1
          )
            throw new Error();
          setKeys((previous) =>
            (previous || []).map((item) => (item.id === namedKey.id ? namedKey : item))
          );
          setCreateName('');
          setFailedCreateName(null);
          creationPendingRef.current = false;
          reportPending();
        } catch (nameErr) {
          if (!alive.current) return;
          if (nameErr instanceof PublicApiError && nameErr.kind === 'authentication') {
            creationPendingRef.current = false;
            reportPending();
            onUnauthorized();
            return;
          }
          const status = nameErr instanceof PublicApiError ? nameErr.status || 0 : 0;
          setFailedCreateName({
            id: result.key.id,
            message: t(
              status === 409
                ? 'keysNameConflict'
                : [400, 404].includes(status)
                  ? 'keysNameRejected'
                  : 'keysNameUnknown'
            ),
          });
          creationPendingRef.current = true;
          reportPending();
        }
      } else {
        creationPendingRef.current = false;
        reportPending();
      }
    } catch (err) {
      if (alive.current) {
        // HTTP errors are also reconciled: a proxy failure can lose a committed reply.
        failure(err);
      }
    } finally {
      inFlight.current = false;
      if (alive.current) setBusy(false);
    }
  }

  async function retryCreateName(discard = false) {
    if (!failedCreateName || inFlight.current) return;
    const desiredName = createName.trim();
    if (!discard && [...desiredName].length > 120) return;
    inFlight.current = true;
    setBusy(true);
    setError('');
    creationPendingRef.current = true;
    reportPending();
    try {
      const current = await client.get<Key>('api-keys/' + failedCreateName.id, auth);
      if (!alive.current) return;
      if (!validKey(current) || current.id !== failedCreateName.id) throw new Error();
      setKeys((previous) =>
        (previous || []).map((item) => (item.id === current.id ? current : item))
      );
      // Even abandoning an unknown write requires a read of the saved result.
      if (discard || current.name === desiredName) {
        setFailedCreateName(null);
        setCreateName('');
        creationPendingRef.current = false;
        reportPending();
        return;
      }
      const saved = await client.put<Key>(
        'api-keys/' + current.id,
        { name: desiredName, nameRevision: current.nameRevision },
        auth
      );
      if (!alive.current) return;
      if (
        !validKey(saved) ||
        saved.id !== current.id ||
        saved.name !== desiredName ||
        saved.nameRevision !== current.nameRevision + 1
      )
        throw new Error();
      setKeys((previous) => (previous || []).map((item) => (item.id === saved.id ? saved : item)));
      setFailedCreateName(null);
      setCreateName('');
      creationPendingRef.current = false;
      reportPending();
    } catch (err) {
      if (!alive.current) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        creationPendingRef.current = false;
        reportPending();
        onUnauthorized();
        return;
      }
      const status = err instanceof PublicApiError ? err.status || 0 : 0;
      setFailedCreateName((previous) =>
        previous
          ? {
              ...previous,
              message: t(
                status === 409
                  ? 'keysNameConflict'
                  : [400, 404].includes(status)
                    ? 'keysNameRejected'
                    : 'keysNameUnknown'
              ),
            }
          : null
      );
    } finally {
      inFlight.current = false;
      if (alive.current) setBusy(false);
    }
  }

  function openRevocationFor(keyId: string, desiredMode: 'revoke' | 'replace') {
    if (
      inFlight.current ||
      detailPendingRef.current ||
      creationPendingRef.current ||
      operationPendingRef.current
    )
      return;
    setSelected('');
    setRevocationOpen(true);
    setRevocationPreset((previous) => ({
      targetId: keyId,
      mode: desiredMode,
      seq: (previous?.seq || 0) + 1,
    }));
  }

  async function reconcile() {
    if (!pending || inFlight.current) return;
    inFlight.current = true;
    setBusy(true);
    setError('');
    try {
      const key = await client.get<Key>('api-keys/requests/' + pending, auth);
      if (!alive.current) return;
      if (!validKey(key) || key.requestId !== pending) throw new Error();
      setKeys((previous) => [...(previous || []).filter((item) => item.id !== key.id), key]);
      setConfirmed(true);
      setPending('');
      // Creation readback must not silently drop the optional name or replay
      // either write. Let the user save or abandon the name on this same key.
      if (createName.trim() && key.name !== createName.trim()) {
        setFailedCreateName({ id: key.id, message: k('createNameNotSaved') });
        creationPendingRef.current = true;
      } else {
        setCreateName('');
        creationPendingRef.current = false;
      }
      reportPending();
      setRetryAllowed(false);
    } catch (err) {
      if (alive.current) {
        if (err instanceof PublicApiError && err.status === 404) setRetryAllowed(true);
        else failure(err);
      }
    } finally {
      inFlight.current = false;
      if (alive.current) setBusy(false);
    }
  }

  const query = search.trim().toLocaleLowerCase();
  const visibleKeys = (keys || []).filter(
    (key) =>
      (filter === 'all' || (filter === 'replaced') === (key.lifecycle === 'superseded')) &&
      (!query ||
        [key.name, key.maskedPreview, key.id].some((value) =>
          value.toLocaleLowerCase().includes(query)
        ))
  );
  const savedCount = (keys || []).filter((key) => key.lifecycle !== 'superseded').length;
  const replacedCount = (keys || []).length - savedCount;
  const locked =
    busy ||
    createOpen ||
    !!selected ||
    detailPending ||
    !!pending ||
    !!secret ||
    !!failedCreateName;
  return (
    <section className="key-workspace" aria-labelledby="client-key-title" aria-busy={busy}>
      <header className="card page-header key-workspace-heading">
        <div>
          <div className="key-heading-title-row">
            <h1 id="client-key-title">
              {t('keysTitle')} <span className="key-count">{keys?.length ?? '—'}</span>
            </h1>
            {storedBadge && <span className="badge badge-emerald">{storedBadge}</span>}
          </div>
          <p className="page-description">{pageDescription || k('description')}</p>
        </div>
        <div className="key-toolbar-actions">
          <button
            className="btn btn-primary"
            aria-label={t('keysCreate')}
            onClick={() => {
              setConfirmed(false);
              setCreateOpen(true);
            }}
            disabled={
              locked ||
              operationPending ||
              keys === null ||
              !!error ||
              [...createName.trim()].length > 120
            }
          >
            <FullWebIcon name="plus" />
            {t('keysCreate')}
          </button>
        </div>
      </header>
      {error && !createOpen && !pending && (
        <p role="alert" className="key-notice key-notice-warning">
          <FullWebIcon name="alert" />
          <span>{error}</span>
        </p>
      )}
      {operationPending && !revocationOpen && (
        <p className="key-notice" role="status">
          {t('keysOperationPending')}
          <button className="btn btn-secondary btn-sm" onClick={() => setRevocationOpen(true)}>
            {t('keysOperationCheck')}
          </button>
        </p>
      )}
      <Modal
        open={visible && (createOpen || !!pending || !!secret || !!failedCreateName)}
        title={confirmed ? k('secretTitle') : t('keysCreate')}
        className="key-create-modal"
        closeDisabled={busy || !!pending || !!failedCreateName}
        onClose={() => {
          if (inFlight.current || pending || failedCreateName) return;
          dismissCreatedSecret();
          setCreateName('');
          setCreateOpen(false);
        }}
      >
        {error && (
          <p className="key-notice key-notice-warning" role="alert">
            {error}
          </p>
        )}
        {(!confirmed || failedCreateName) && (
          <form
            onSubmit={(event) => {
              event.preventDefault();
              if (!failedCreateName) void createKey();
            }}
          >
            <label className="form-label" htmlFor="create-key-name">
              {k('createNameLabel')}
            </label>
            <input
              id="create-key-name"
              className="input"
              type="text"
              data-testid="create-key-name-input"
              aria-label={k('createNameLabel')}
              placeholder={k('createNamePlaceholder')}
              maxLength={120}
              value={createName}
              disabled={busy || operationPending || !!pending}
              onChange={(event) => setCreateName(event.target.value)}
            />
            {!failedCreateName && (
              <>
                <p className="key-field-help">{k('generatedOnServer')}</p>
                <div className="key-modal-actions">
                  <button
                    className="btn btn-secondary"
                    type="button"
                    disabled={busy || !!pending}
                    onClick={() => {
                      setCreateName('');
                      setCreateOpen(false);
                    }}
                  >
                    {k('cancel')}
                  </button>
                  <button
                    className="btn btn-primary"
                    type="submit"
                    data-testid="confirm-create-key"
                    disabled={
                      busy ||
                      !!pending ||
                      operationPending ||
                      keys === null ||
                      [...createName.trim()].length > 120
                    }
                  >
                    {t('keysCreate')}
                  </button>
                </div>
              </>
            )}
          </form>
        )}
        {pending && (
          <div role="status" className="key-creation-recovery">
            <FullWebIcon name="clock" />
            <div>
              <h3>{k('pending')}</h3>
              <p>{t('keysUnknown')}</p>
              <div className="key-toolbar-actions">
                <button
                  className="btn btn-secondary"
                  disabled={busy}
                  onClick={() => void reconcile()}
                >
                  {t('keysCheck')}
                </button>
                {retryAllowed && (
                  <button
                    className="btn btn-secondary"
                    disabled={busy}
                    onClick={() => void createKey()}
                  >
                    {t('keysRetry')}
                  </button>
                )}
              </div>
            </div>
          </div>
        )}
        {failedCreateName && (
          <div
            role="alert"
            className="key-creation-recovery key-notice-warning"
            data-testid="create-name-partial-failure"
          >
            <FullWebIcon name="alert" />
            <div>
              <h3>{k('createNameFailedTitle')}</h3>
              <p>{k('createNameFailedHelp')}</p>
              <p>{failedCreateName.message}</p>
              <div className="key-toolbar-actions">
                <button
                  className="btn btn-primary"
                  data-testid="retry-create-name"
                  disabled={busy}
                  onClick={() => void retryCreateName()}
                >
                  {k('retrySaveName')}
                </button>
                <button
                  className="btn btn-secondary"
                  data-testid="discard-create-name"
                  disabled={busy}
                  onClick={() => void retryCreateName(true)}
                >
                  {k('discardCreateName')}
                </button>
              </div>
            </div>
          </div>
        )}
        {confirmed && !secret && !failedCreateName && (
          <p role="status" className="key-notice">
            <FullWebIcon name="check" />
            <span>{t('keysConfirmed')}</span>
          </p>
        )}
        {secret && (
          <div className="key-secret-panel" role="status">
            <div className="key-secret-title">
              <span className="key-resource-icon">
                <FullWebIcon name="key" size={24} />
              </span>
              <div>
                <h3>{k('secretTitle')}</h3>
                <p>{t('keysCreated')}</p>
              </div>
            </div>
            <p>{k('secretHelp')}</p>
            <p className="key-once-note">{t('keysOnce')}</p>
            <div className="key-secret-value">
              <code data-testid="created-client-secret">{secret}</code>
            </div>
            <div className="key-toolbar-actions">
              <button
                className="btn btn-primary"
                aria-label={t('keysCopy')}
                data-testid="copy-created-client-secret"
                onClick={() => void copyCreatedSecret()}
              >
                <FullWebIcon name="copy" />
                {t('keysCopy')}
              </button>
              <button className="btn btn-secondary" onClick={dismissCreatedSecret}>
                {t('keysDismiss')}
              </button>
            </div>
            {copyFeedback && (
              <p role="status" data-testid="copy-created-client-secret-feedback">
                {t(copyFeedback === 'copied' ? 'keysCopied' : 'keysCopyFailed')}
              </p>
            )}
          </div>
        )}
      </Modal>
      <div className="key-list-workspace">
        <div className="card key-filter-panel">
          <div className="key-filter-bar">
            <select
              className="input"
              aria-label={k('lifecycle')}
              value={filter}
              onChange={(event) => setFilter(event.target.value as typeof filter)}
            >
              {(['all', 'saved', 'replaced'] as const).map((value) => (
                <option key={value} value={value}>
                  {k(value)}{' '}
                  {keys === null
                    ? '—'
                    : value === 'all'
                      ? keys.length
                      : value === 'saved'
                        ? savedCount
                        : replacedCount}
                </option>
              ))}
            </select>
          </div>
          <label className="key-search">
            <FullWebIcon name="search" />
            <input
              className="input"
              type="search"
              aria-label={k('search')}
              placeholder={k('search')}
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
          </label>
          <button
            className="btn btn-secondary"
            aria-label={t('refresh')}
            disabled={busy || !!selected || detailPending}
            onClick={() => void refresh()}
          >
            <FullWebIcon name="refresh" />
            {t('refresh')}
          </button>
          <button
            className="btn btn-secondary"
            onClick={() => {
              setSearch('');
              setFilter('saved');
            }}
          >
            {k('clear')}
          </button>
        </div>
        <div className="key-summary-grid" data-testid="key-summary-grid">
          {(
            [
              [
                'saved',
                'key',
                'summarySavedTitle',
                'summarySavedHelp',
                keys === null ? '—' : savedCount,
              ],
              [
                'runtime',
                'shield',
                'summaryRuntimeTitle',
                'summaryRuntimeHelp',
                keys === null ? '—' : k('effectUnknown'),
              ],
              [
                'replaced',
                'clock',
                'summaryReplacedTitle',
                'summaryReplacedHelp',
                keys === null ? '—' : replacedCount,
              ],
              ['usage', 'info', 'summaryUsageTitle', 'summaryUsageHelp', k('summaryUsageValue')],
            ] as const
          ).map(([id, icon, label, help, value]) => (
            <div className="card key-summary-card" key={id} data-testid={'key-summary-' + id}>
              <span className="key-resource-icon">
                <FullWebIcon name={icon} />
              </span>
              <div>
                <span className="key-summary-label">{k(label)}</span>
                <small>{k(help)}</small>
              </div>
              <strong
                className={
                  'key-summary-value' +
                  (typeof value === 'string' && value !== '—' ? ' is-unavailable' : '')
                }
              >
                {id === 'runtime' || id === 'usage' ? (
                  <span
                    className={'key-state-pill ' + (id === 'runtime' ? 'is-pending' : 'is-muted')}
                  >
                    {value}
                  </span>
                ) : (
                  value
                )}
              </strong>
            </div>
          ))}
        </div>
        <div className="card key-table-panel">
          <div className="key-table-heading">
            <div>
              <strong>{k('list')}</strong>
              <small>{k('scope')}</small>
            </div>
            <div
              className="key-view-toggle"
              role="group"
              aria-label={k('viewMode')}
              data-testid="key-view-toggle"
            >
              {(['table', 'cards'] as const).map((mode) => (
                <button
                  key={mode}
                  type="button"
                  className={`btn btn-sm ${viewMode === mode ? 'btn-primary' : 'btn-ghost'}`}
                  data-testid={'key-view-' + mode}
                  aria-pressed={viewMode === mode}
                  onClick={() => setViewMode(mode)}
                >
                  {k(mode === 'table' ? 'viewTable' : 'viewCards')}
                </button>
              ))}
            </div>
          </div>
          {keys === null ? (
            !error && (
              <div className="key-empty" role="status">
                <FullWebIcon name="refresh" size={28} />
                <p>{t('keysLoading')}</p>
              </div>
            )
          ) : keys.length === 0 ? (
            <div className="key-empty">
              <span className="key-resource-icon">
                <FullWebIcon name="key" size={26} />
              </span>
              <h3>{k('emptyTitle')}</h3>
              <p>{k('emptyHelp')}</p>
              <small>{t('keysEmpty')}</small>
            </div>
          ) : visibleKeys.length === 0 ? (
            <div className="key-empty">
              <FullWebIcon name="search" size={28} />
              <h3>{k('noMatches')}</h3>
              <p>{k('noMatchesHelp')}</p>
              <button
                className="btn btn-secondary"
                onClick={() => {
                  setSearch('');
                  setFilter('all');
                }}
              >
                {k('clear')}
              </button>
            </div>
          ) : (
            <div className="key-table-scroll">
              <table className={`key-table ${viewMode === 'cards' ? 'is-cards-view' : ''}`}>
                <thead>
                  <tr>
                    {(['credential', 'lifecycle', 'actions'] as const).map((label) => (
                      <th key={label} scope="col">
                        {k(label)}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {visibleKeys.map((key) => (
                    <tr key={key.id} data-testid={'key-row-' + key.id}>
                      <td data-label={k('name')}>
                        <div className="key-name-cell">
                          <span className="key-resource-icon">
                            <FullWebIcon name="key" />
                          </span>
                          <div>
                            <button
                              type="button"
                              className="key-name-link"
                              disabled={locked}
                              onClick={() => setSelected(key.id)}
                            >
                              {key.name || t('keysUnnamed')}
                            </button>
                            <small title={key.id}>
                              <code>{key.maskedPreview}</code>
                            </small>
                          </div>
                        </div>
                      </td>
                      <td data-label={k('lifecycle')}>
                        <span
                          className={
                            'key-state-pill ' +
                            (key.lifecycle === 'superseded' ? 'is-muted' : 'is-saved')
                          }
                        >
                          {k(key.lifecycle === 'superseded' ? 'replaced' : 'saved')}
                        </span>
                        <br />
                        <span className="key-state-pill is-pending">
                          {k(
                            key.runtimeState === 'effect_unknown' ? 'effectUnknown' : 'notApplied'
                          )}
                        </span>
                      </td>
                      <td data-label={k('actions')}>
                        <div className="key-row-actions">
                          <button
                            className="btn btn-ghost btn-xs key-detail-link"
                            data-testid={'open-key-' + key.id}
                            disabled={locked}
                            onClick={() => setSelected(key.id)}
                          >
                            {t('keysDetails')}
                            <FullWebIcon name="chevronRight" size={15} />
                          </button>
                          {revocationAvailable && key.lifecycle !== 'superseded' && (
                            <DropdownMenu
                              ariaLabel={k('moreActions') + ': ' + (key.name || t('keysUnnamed'))}
                              triggerLabel="⋮"
                              triggerClassName="btn btn-ghost btn-xs"
                              disabled={locked || operationPending}
                              items={[
                                {
                                  key: 'replace',
                                  label: k('quickReplace'),
                                  onClick: () => openRevocationFor(key.id, 'replace'),
                                },
                                { key: 'divider', type: 'divider' },
                                {
                                  key: 'revoke',
                                  label: k('quickRevoke'),
                                  tone: 'danger',
                                  disabled: savedCount <= 1,
                                  onClick: () => openRevocationFor(key.id, 'revoke'),
                                },
                              ]}
                            />
                          )}
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {keys !== null && (
            <footer className="key-table-footer">
              <span>
                {k('listed')} {visibleKeys.length} {k('count')}
              </span>
              <span>
                {k('total')} {keys.length} {k('count')}
              </span>
            </footer>
          )}
        </div>
        <aside className="key-gateway-note">
          <FullWebIcon name="shield" />
          <div>
            <strong>{k('unavailable')}</strong>
            <p>{k('unavailableHelp')}</p>
          </div>
        </aside>
      </div>
      {selected && (
        <FreshKeyDetails
          open={visible}
          key={selected}
          id={selected}
          client={client}
          credential={credential}
          onUnauthorized={onUnauthorized}
          onPending={(value) => {
            detailPendingRef.current = value;
            setDetailPending(value);
            reportPending();
          }}
          onSaved={(key) =>
            setKeys((previous) => (previous || []).map((item) => (item.id === key.id ? key : item)))
          }
          onClose={() => setSelected('')}
          onRequestOperation={
            revocationAvailable
              ? (desiredMode) => openRevocationFor(selected, desiredMode)
              : undefined
          }
        />
      )}
      {revocationAvailable && keys && (
        <FreshKeyRevocation
          open={visible && revocationOpen}
          onClose={() => {
            if (!operationPendingRef.current) setRevocationOpen(false);
          }}
          onCreateReplacement={() => {
            setRevocationOpen(false);
            setConfirmed(false);
            setCreateOpen(true);
          }}
          keys={keys}
          client={client}
          credential={credential}
          disabled={locked}
          preset={revocationPreset}
          onUnauthorized={onUnauthorized}
          onPending={(value) => {
            operationPendingRef.current = value;
            setOperationPending(value);
            reportPending();
          }}
          onChanged={() => void refresh()}
        />
      )}
    </section>
  );
}
