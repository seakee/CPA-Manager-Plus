import { useEffect, useRef, useState, type FormEvent } from 'react';
import { fullWebLanguage, fullWebText as t } from '../i18n/fullweb';
import { keyWorkspaceText as k } from '../i18n/freshKeyWorkspace';
import { FullWebIcon } from './FullWebIcon';
import './freshKeys.css';
import { PublicApiError, type createPublicApiClient } from './publicApi';
import { validKey, type Key } from './freshKeyMetadata';
import { FreshKeyInstallation } from './FreshKeyInstallation';
import { Drawer } from '../components/ui/Drawer';
import { Modal } from '../components/ui/Modal';
import { SegmentedTabs } from '../components/ui/SegmentedTabs';

function detailTime(value: number) {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return { iso: undefined, label: '—' };
  return {
    iso: date.toISOString(),
    label: date.toLocaleString(fullWebLanguage(), {
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
    }),
  };
}

export function FreshKeyDetails({
  id,
  client,
  credential,
  onUnauthorized,
  onPending,
  onSaved,
  onClose,
  onRequestOperation,
  open = true,
}: {
  id: string;
  client: ReturnType<typeof createPublicApiClient>;
  credential: string;
  onUnauthorized: () => void;
  onPending: (value: boolean) => void;
  onSaved: (key: Key) => void;
  onClose: () => void;
  onRequestOperation?: (mode: 'replace' | 'revoke') => void;
  open?: boolean;
}) {
  const [record, setRecord] = useState<Key | null>(null);
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [unknown, setUnknown] = useState(false);
  const [installationPending, setInstallationPending] = useState(false);
  const installationPendingRef = useRef(false);
  const [rejectedDraft, setRejectedDraft] = useState(false);
  const [message, setMessage] = useState('');
  const [editing, setEditing] = useState(false);
  const inFlight = useRef(false);
  const active = useRef(true);
  const input = useRef<HTMLInputElement>(null);
  const auth = { kind: 'admin' as const, value: credential };
  const resource = 'api-keys/' + id;
  const dirty = rejectedDraft || (record !== null && name.trim() !== record.name);

  // Each mounted record/session owns its replies. The parent keys this view by
  // canonical ID and unmounts the entire key page when authentication is cleared.
  useEffect(() => {
    active.current = true;
    const previous = typeof document !== 'undefined' ? document.activeElement : null;
    void read();
    return () => {
      active.current = false;
      if (
        typeof HTMLElement !== 'undefined' &&
        previous instanceof HTMLElement &&
        previous.isConnected
      )
        previous.focus();
    };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (editing && record) input.current?.focus();
  }, [editing, record]);

  function validate(value: Key) {
    if (!validKey(value) || value.id !== id) throw new Error();
    return value;
  }
  function failure(err: unknown) {
    if (err instanceof PublicApiError && err.kind === 'authentication') onUnauthorized();
    else setMessage(t('keysUnavailable'));
  }
  async function read() {
    if (inFlight.current || installationPendingRef.current) return;
    if (dirty && !unknown && !window.confirm(t('keysDiscardName'))) return;
    inFlight.current = true;
    setBusy(true);
    setMessage('');
    try {
      const value = validate(await client.get<Key>(resource, auth));
      if (!active.current) return;
      setRecord(value);
      setName(value.name);
      setRejectedDraft(false);
      onSaved(value);
      if (unknown) setMessage(t('keysNameChecked'));
      setUnknown(false);
      onPending(false);
    } catch (err) {
      if (active.current) failure(err);
    } finally {
      inFlight.current = false;
      if (active.current) setBusy(false);
    }
  }
  async function save(event: FormEvent) {
    event.preventDefault();
    if (
      inFlight.current ||
      installationPendingRef.current ||
      !record ||
      unknown ||
      name.trim() === record.name
    )
      return;
    inFlight.current = true;
    setBusy(true);
    setMessage('');
    onPending(true);
    const submitted = name.trim();
    try {
      const value = validate(
        await client.put<Key>(
          resource,
          { name: submitted, nameRevision: record.nameRevision },
          auth
        )
      );
      if (!active.current) return;
      if (value.name !== submitted || value.nameRevision !== record.nameRevision + 1)
        throw new Error();
      setRecord(value);
      setName(value.name);
      onSaved(value);
      setMessage(t('keysNameSaved'));
      setEditing(false);
      onPending(false);
    } catch (err) {
      if (!active.current) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        onUnauthorized();
      } else if (err instanceof PublicApiError && [400, 404, 409].includes(err.status || 0)) {
        // These local responses prove rejection. A fresh read is required
        // before another explicit save; preserve the draft until that check.
        setRejectedDraft(true);
        setRecord(null);
        setMessage(t(err.status === 409 ? 'keysNameConflict' : 'keysNameRejected'));
        onPending(false);
      } else {
        setUnknown(true);
        setMessage(t('keysNameUnknown'));
      }
    } finally {
      inFlight.current = false;
      if (active.current) setBusy(false);
    }
  }
  function canClose() {
    if (inFlight.current || installationPendingRef.current || unknown) return false;
    return !dirty || window.confirm(t('keysDiscardName'));
  }
  function close() {
    if (canClose()) onClose();
  }
  return (
    <>
      <Drawer
        open={open}
        title={t('keysDetails')}
        width={680}
        className="key-detail-drawer"
        onBeforeClose={() => !editing && canClose()}
        onClose={onClose}
        footer={
          <div className="key-drawer-actions">
            {record && record.lifecycle !== 'superseded' && onRequestOperation && (
              <div className="key-row-actions" data-testid="key-detail-operation-entry">
                <button
                  type="button"
                  className="btn btn-ghost key-danger"
                  data-testid="detail-revoke-key"
                  disabled={busy || unknown || installationPending || dirty || editing}
                  onClick={() => onRequestOperation('revoke')}
                >
                  {k('quickRevoke')}
                </button>
                <button
                  type="button"
                  className="btn btn-secondary"
                  data-testid="detail-replace-key"
                  disabled={busy || unknown || installationPending || dirty || editing}
                  onClick={() => onRequestOperation('replace')}
                >
                  {k('quickReplace')}
                </button>
              </div>
            )}
            <button
              className="btn btn-secondary"
              aria-label={t('keysCloseDetails')}
              disabled={busy || unknown || installationPending}
              onClick={close}
            >
              {t('keysCloseDetails')}
            </button>
          </div>
        }
      >
        <section className="key-detail-workspace" aria-busy={busy}>
          <SegmentedTabs
            items={[{ id: 'overview', label: k('overview') }]}
            activeTab="overview"
            ariaLabel={t('keysDetails')}
            idBase="key-detail-tab"
            className="key-detail-tabs"
          />
          {busy && (
            <p role="status" className="key-notice">
              {t('keysLoading')}
            </p>
          )}
          {message && !editing && (
            <p role="status" className="key-notice key-notice-warning">
              <FullWebIcon name="info" />
              <span>{message}</span>
            </p>
          )}
          {record && (
            <>
              <div className="key-detail-hero">
                <span className="key-resource-icon">
                  <FullWebIcon name="key" size={24} />
                </span>
                <div>
                  <h3>{record.name || t('keysUnnamed')}</h3>
                  <p>
                    <code>{record.maskedPreview}</code>
                  </p>
                </div>
                <div className="key-detail-badges">
                  <span
                    className={
                      'key-state-pill ' +
                      (record.lifecycle === 'superseded' ? 'is-muted' : 'is-saved')
                    }
                  >
                    {k(record.lifecycle === 'superseded' ? 'replaced' : 'saved')}
                  </span>
                  <span className="key-state-pill is-pending">
                    {k(record.runtimeState === 'effect_unknown' ? 'effectUnknown' : 'notApplied')}
                  </span>
                </div>
              </div>
              <dl className="key-detail-meta-grid" data-testid="key-detail-metadata">
                <div>
                  <dt>{k('created')}</dt>
                  <dd>
                    <time dateTime={detailTime(record.createdAtMs).iso}>
                      {detailTime(record.createdAtMs).label}
                    </time>
                  </dd>
                </div>
                <div>
                  <dt>{k('revisionLabel')}</dt>
                  <dd>v{record.nameRevision}</dd>
                </div>
                <div>
                  <dt>{k('policyScopeLabel')}</dt>
                  <dd>{k('policyGlobalDefault')}</dd>
                </div>
              </dl>
              <div className="key-detail-columns">
                <section className="key-detail-section">
                  <header>
                    <FullWebIcon name="key" />
                    <h4>{k('basic')}</h4>
                  </header>
                  <div className="key-detail-section-body">
                    <p className="key-detail-description">{k('detailHelp')}</p>
                    <div className="key-readonly-field">
                      <span>{t('keysName')}</span>
                      <strong>{record.name || t('keysUnnamed')}</strong>
                      <button
                        className="btn btn-secondary btn-sm"
                        disabled={busy || unknown || installationPending}
                        onClick={() => setEditing(true)}
                      >
                        {k('rename')}
                      </button>
                    </div>
                    <div className="key-private-note">
                      <FullWebIcon name="shield" />
                      <p>{k('privateKeyHelp')}</p>
                    </div>
                  </div>
                </section>
                <section className="key-detail-section">
                  <header>
                    <FullWebIcon name="gateway" />
                    <h4>{k('access')}</h4>
                  </header>
                  <div className="key-detail-section-body">
                    <p className="key-detail-description">{k('connectionPending')}</p>
                    <details className="key-installation-details">
                      <summary>{k('advancedInstallation')}</summary>
                      {record.lifecycle !== 'superseded' ? (
                        <FreshKeyInstallation
                          id={id}
                          client={client}
                          credential={credential}
                          onUnauthorized={onUnauthorized}
                          canInstall={() => !inFlight.current}
                          onPending={(pending) => {
                            installationPendingRef.current = pending;
                            setInstallationPending(pending);
                            onPending(pending);
                          }}
                          disabled={busy || unknown || dirty}
                        />
                      ) : (
                        <p>{t('keysRevokedHistory')}</p>
                      )}
                    </details>
                  </div>
                </section>
                <p className="key-field-help" data-testid="key-detail-usage-policy">
                  {k('usageUnavailableDetail')} {k('policyGlobalHelp')}
                </p>
              </div>
            </>
          )}
          <footer className="key-detail-footer">
            <details className="key-record-identifiers">
              <summary>{k('diagnostics')}</summary>
              <dl>
                <dt>{t('keysIdentity')}</dt>
                <dd>
                  <code>{id}</code>
                </dd>
              </dl>
            </details>
            <button
              className="btn btn-secondary"
              aria-label={t('keysReadDetails')}
              disabled={busy || installationPending}
              onClick={() => void read()}
            >
              <FullWebIcon name="refresh" />
              {t('keysReadDetails')}
            </button>
          </footer>
        </section>
      </Drawer>
      <Modal
        open={open && editing}
        title={k('rename')}
        className="key-rename-modal"
        closeDisabled={busy || unknown || installationPending || dirty}
        onClose={() => {
          if (!canClose()) return;
          setName(record?.name || '');
          setRejectedDraft(false);
          setEditing(false);
        }}
      >
        {message && (
          <p role="status" className="key-notice key-notice-warning">
            {message}
          </p>
        )}
        {record && (
          <form onSubmit={(event) => void save(event)}>
            <label htmlFor="key-name" className="form-label">
              {t('keysName')}
            </label>
            <input
              ref={input}
              id="key-name"
              className="input"
              value={name}
              maxLength={240}
              autoComplete="off"
              disabled={busy || unknown || installationPending}
              onChange={(event) => setName(event.target.value)}
            />
            <p className="key-field-help">{t('keysNameHelp')}</p>
            <div className="key-modal-actions">
              <button
                type="button"
                className="btn btn-secondary"
                disabled={busy || unknown || installationPending}
                onClick={() => {
                  if (canClose()) {
                    setName(record.name);
                    setEditing(false);
                  }
                }}
              >
                {k('cancel')}
              </button>
              <button
                type="submit"
                className="btn btn-primary"
                disabled={
                  busy ||
                  unknown ||
                  installationPending ||
                  name.trim() === record.name ||
                  [...name.trim()].length > 120
                }
              >
                {t('keysSaveName')}
              </button>
            </div>
          </form>
        )}
        {(unknown || rejectedDraft) && (
          <button
            className="btn btn-secondary"
            disabled={busy || installationPending}
            onClick={() => void read()}
          >
            {t('keysReadDetails')}
          </button>
        )}
        {rejectedDraft && !record && (
          <button
            type="button"
            className="btn btn-secondary"
            onClick={() => {
              if (canClose()) {
                setRejectedDraft(false);
                setName('');
                setEditing(false);
              }
            }}
          >
            {k('cancel')}
          </button>
        )}
      </Modal>
    </>
  );
}
