import { useEffect, useRef, useState } from 'react';
import { fullWebText as t } from '../i18n/fullweb';
import { keyWorkspaceText as k } from '../i18n/freshKeyWorkspace';
import { FullWebIcon } from './FullWebIcon';
import { PublicApiError, type createPublicApiClient } from './publicApi';
import type { Key } from './freshKeyMetadata';
import { Modal } from '../components/ui/Modal';

interface Operation {
  id: string;
  targetId: string;
  keeperId: string;
  mode: 'revoke' | 'replace';
  outcome: 'pending' | 'unknown' | 'committed';
  revision: number;
  steps: {
    name: 'add' | 'revoke';
    state: 'unattempted' | 'attempted' | 'observed';
    returned: boolean;
  }[];
}
interface Result {
  operation: Operation | null;
  runtimeState: 'effect_unknown' | 'effective_observed';
  expiresAtMs: number;
  nextStep: '' | 'add' | 'revoke';
}
function validResult(r: Result): boolean {
  const o = r?.operation;
  return (
    !!r &&
    ['effect_unknown', 'effective_observed'].includes(r.runtimeState) &&
    Number.isSafeInteger(r.expiresAtMs) &&
    ['', 'add', 'revoke'].includes(r.nextStep) &&
    (o === null ||
      (!!o &&
        [o.id, o.targetId, o.keeperId].every((v) => /^[a-f0-9]{32}$/.test(v)) &&
        o.targetId !== o.keeperId &&
        ['revoke', 'replace'].includes(o.mode) &&
        ['pending', 'unknown', 'committed'].includes(o.outcome) &&
        Number.isSafeInteger(o.revision) &&
        o.revision > 0 &&
        Array.isArray(o.steps) &&
        o.steps.length === (o.mode === 'replace' ? 2 : 1) &&
        o.steps.every(
          (s, i) =>
            s.name === (o.mode === 'replace' && i === 0 ? 'add' : 'revoke') &&
            ['unattempted', 'attempted', 'observed'].includes(s.state) &&
            typeof s.returned === 'boolean'
        ) &&
        (o.outcome !== 'committed' || o.steps.every((s) => s.state === 'observed'))))
  );
}

export function FreshKeyRevocation({
  keys,
  client,
  credential,
  onUnauthorized,
  onPending,
  onChanged,
  disabled,
  preset,
  open = true,
  onClose,
  onCreateReplacement,
}: {
  keys: Key[];
  client: ReturnType<typeof createPublicApiClient>;
  credential: string;
  onUnauthorized: () => void;
  onPending: (value: boolean) => void;
  onChanged: () => void;
  disabled: boolean;
  preset?: { targetId: string; mode: 'revoke' | 'replace'; seq: number } | null;
  open?: boolean;
  onClose?: () => void;
  onCreateReplacement?: () => void;
}) {
  const [target, setTarget] = useState('');
  const [keeper, setKeeper] = useState('');
  const [mode, setMode] = useState<'revoke' | 'replace'>('revoke');
  const [result, setResult] = useState<Result | null>(null);
  const [request, setRequest] = useState<{
    id: string;
    target: string;
    keeper: string;
    mode: string;
  } | null>(null);
  const [busy, setBusy] = useState(false);
  const [checked, setChecked] = useState(false);
  const [message, setMessage] = useState('');
  const [showGuide, setShowGuide] = useState(false);
  const active = useRef(true);
  const flight = useRef(false);
  const auth = { kind: 'admin' as const, value: credential };
  const available = keys.filter((k) => k.lifecycle !== 'superseded');

  useEffect(() => {
    active.current = true;
    onPending(true);
    void read();
    return () => {
      active.current = false;
    };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!preset || request || (result?.operation && result.operation.outcome !== 'committed'))
      return;
    if (available.some((item) => item.id === preset.targetId)) {
      setTarget(preset.targetId);
      setMode(preset.mode);
      setShowGuide(preset.mode === 'replace');
      const fallbackKeeper = available.find((item) => item.id !== preset.targetId)?.id || '';
      setKeeper((previous) =>
        previous && previous !== preset.targetId && available.some((item) => item.id === previous)
          ? previous
          : fallbackKeeper
      );
    }
  }, [preset, keys]); // eslint-disable-line react-hooks/exhaustive-deps

  function accept(value: Result, expected?: string, selection = request, observed = true) {
    if (!validResult(value) || (expected && (!value.operation || value.operation.id !== expected)))
      throw new Error();
    if (
      selection &&
      value.operation &&
      (value.operation.id !== selection.id ||
        value.operation.targetId !== selection.target ||
        value.operation.keeperId !== selection.keeper ||
        value.operation.mode !== selection.mode)
    )
      throw new Error();
    if (
      result?.operation &&
      ((!value.operation &&
        (result.operation.outcome !== 'committed' ||
          !expected ||
          expected === result.operation.id)) ||
        (value.operation &&
          value.operation.id === result.operation.id &&
          (value.operation.targetId !== result.operation.targetId ||
            value.operation.keeperId !== result.operation.keeperId ||
            value.operation.mode !== result.operation.mode ||
            value.operation.revision < result.operation.revision ||
            (result.operation.outcome === 'committed' && value.operation.outcome !== 'committed') ||
            value.operation.steps.some((step, i) => {
              const previous = result.operation!.steps[i];
              const rank = { unattempted: 0, attempted: 1, observed: 2 };
              return (
                rank[step.state] < rank[previous.state] || (previous.returned && !step.returned)
              );
            }))))
    )
      throw new Error();
    if (!active.current) return;
    setResult(value);
    setChecked(observed);
    setMessage(observed ? '' : t('keysOperationUnknown'));
    setRequest(null);
    onPending(!observed || (!!value.operation && value.operation.outcome !== 'committed'));
    if (observed && value.operation?.outcome === 'committed') onChanged();
  }
  function failure(err: unknown, expected?: string, selection = request) {
    if (!active.current) return;
    if (err instanceof PublicApiError && err.kind === 'authentication') {
      onUnauthorized();
      return;
    }
    if (err instanceof PublicApiError && err.operationResult !== undefined) {
      try {
        const recovered = err.operationResult as Result;
        if (!recovered?.operation || recovered.runtimeState !== 'effect_unknown') throw new Error();
        accept(recovered, expected, selection, false);
        if (err.status === 409) setMessage(t('keysOperationRejected'));
        return;
      } catch {
        // Invalid or regressed failed-response snapshots never unlock a write.
      }
    }
    setMessage(
      t(
        err instanceof PublicApiError && err.status === 409
          ? 'keysOperationRejected'
          : 'keysOperationUnknown'
      )
    );
    setChecked(false);
  }
  async function run(action: () => Promise<Result>, expected?: string, selection = request) {
    if (flight.current) return;
    flight.current = true;
    setBusy(true);
    try {
      accept(await action(), expected, selection);
    } catch (err) {
      failure(err, expected, selection);
    } finally {
      flight.current = false;
      if (active.current) setBusy(false);
    }
  }
  async function read() {
    const id = request?.id || result?.operation?.id || 'latest';
    await run(
      () => client.get<Result>('api-keys/operations/' + id, auth),
      id === 'latest' ? undefined : id
    );
  }
  async function prepare() {
    if (
      flight.current ||
      disabled ||
      !checked ||
      request ||
      (result?.operation && result.operation.outcome !== 'committed') ||
      !target ||
      !keeper ||
      available.length <= 1 ||
      target === keeper
    )
      return;
    const id = Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) =>
      b.toString(16).padStart(2, '0')
    ).join('');
    const selection = { id, target, keeper, mode };
    setRequest(selection);
    setChecked(false);
    onPending(true);
    await run(
      async () => {
        const value = await client.post<Result>(
          'api-keys/' + target + '/revocation',
          { requestId: id, keeperId: keeper, mode },
          auth
        );
        if (!value.operation) throw new Error();
        return value;
      },
      id,
      selection
    );
  }
  async function proceed() {
    const o = result?.operation;
    if (
      flight.current ||
      disabled ||
      !checked ||
      !o ||
      !result.nextStep ||
      o.outcome === 'committed' ||
      request
    )
      return;
    setChecked(false);
    onPending(true);
    await run(
      () => client.post<Result>('api-keys/operations/' + o.id + '/continue', {}, auth),
      o.id
    );
  }
  const unresolved = !!request || (!!result?.operation && result.operation.outcome !== 'committed');
  const guiding = showGuide && !!onCreateReplacement && checked && !unresolved;
  const content = (
    <section className="console-key-result key-revocation-workspace" aria-busy={busy}>
      {/* CI browser proofs locate controls via this heading's direct parent. */}
      <h3>{t('keysRevokeTitle')}</h3>
      {(!preset || guiding || (showGuide && !onCreateReplacement)) && (
        <div className="key-replace-guide" data-testid="key-replacement-guide">
          <strong>{k('replaceGuideTitle')}</strong>
          <ol className="key-replace-steps">
            <li>{k('replaceStep1')}</li>
            <li>{k('replaceStep2')}</li>
            <li>{k('replaceStep3')}</li>
          </ol>
          {showGuide && onCreateReplacement && (
            <div className="key-modal-actions">
              <button
                className="btn btn-secondary"
                disabled={busy || unresolved || !checked}
                onClick={() => setShowGuide(false)}
              >
                {k('replacementReady')}
              </button>
              <button
                className="btn btn-primary"
                disabled={busy || unresolved || !checked}
                onClick={onCreateReplacement}
              >
                {k('createReplacement')}
              </button>
            </div>
          )}
        </div>
      )}
      {preset && (
        <div className="key-operation-resource">
          <strong>
            {available.find((key) => key.id === preset.targetId)?.name || t('keysUnnamed')}
          </strong>
          <code>{available.find((key) => key.id === preset.targetId)?.maskedPreview}</code>
        </div>
      )}
      {available.length <= 1 && guiding && (
        <div className="key-notice key-notice-warning" data-testid="last-key-protection-notice">
          {k('lastKeyProtectionHelp')}
        </div>
      )}
      {!guiding && (
        <>
          <p className="key-field-help">{t('keysOperationHelp')}</p>
          {available.length <= 1 && (
            <div className="key-notice key-notice-warning" data-testid="last-key-protection-notice">
              <FullWebIcon name="shield" />
              <div>
                <strong>{k('lastKeyProtectionTitle')}</strong>
                <p>{k('lastKeyProtectionHelp')}</p>
              </div>
            </div>
          )}
          {message && (
            <p role="alert" className="key-notice key-notice-warning">
              <FullWebIcon name="alert" />
              <span>{message}</span>
            </p>
          )}
          {request && (
            <p>
              {t('keysOperationId')}: <code>{request.id}</code>
            </p>
          )}
          {result?.operation && (
            <div role="status">
              <details className="key-record-identifiers">
                <summary>{k('diagnostics')}</summary>
                <p>
                  {t('keysOperationId')}: <code>{result.operation.id}</code>
                </p>
                <p>
                  {t('keysOperationTarget')}: <code>{result.operation.targetId}</code>
                </p>
                <p>
                  {t('keysOperationKeeper')}: <code>{result.operation.keeperId}</code>
                </p>
              </details>
              <p>
                {t(
                  result.operation.outcome === 'committed'
                    ? 'keysOperationCommitted'
                    : 'keysOperationPending'
                )}
              </p>
              {result.nextStep && (
                <p>
                  {t(
                    result.nextStep === 'add' ? 'keysOperationAddNext' : 'keysOperationRevokeNext'
                  )}
                </p>
              )}
              <button
                className="btn btn-primary"
                disabled={disabled || busy || !checked || !result.nextStep || !!request}
                onClick={() => void proceed()}
              >
                {t('keysOperationContinue')}
              </button>
            </div>
          )}
          <fieldset
            className="form-group console-key-operation-fields"
            disabled={disabled || busy || unresolved || !checked}
          >
            <label className="form-group" hidden={!!preset}>
              {t('keysOperationTarget')}
              <span className="input-wrap">
                <select
                  className="form-input"
                  value={target}
                  onChange={(e) => setTarget(e.target.value)}
                >
                  <option value="">{t('keysOperationSelect')}</option>
                  {available.map((k) => (
                    <option key={k.id} value={k.id}>
                      {k.name || t('keysUnnamed')} — {k.maskedPreview} ({k.id})
                    </option>
                  ))}
                </select>
              </span>
            </label>
            <label className="form-group">
              {t('keysOperationKeeper')}
              <span className="input-wrap">
                <select
                  className="form-input"
                  value={keeper}
                  onChange={(e) => setKeeper(e.target.value)}
                >
                  <option value="">{t('keysOperationSelect')}</option>
                  {available
                    .filter((k) => k.id !== target)
                    .map((k) => (
                      <option key={k.id} value={k.id}>
                        {k.name || t('keysUnnamed')} — {k.maskedPreview} ({k.id})
                      </option>
                    ))}
                </select>
              </span>
            </label>
            <label className="form-group" hidden={!!preset}>
              {t('keysOperationMode')}
              <span className="input-wrap">
                <select
                  className="form-input"
                  value={mode}
                  onChange={(e) => setMode(e.target.value as 'revoke' | 'replace')}
                >
                  <option value="revoke">{t('keysOperationRevoke')}</option>
                  <option value="replace">{t('keysOperationReplace')}</option>
                </select>
              </span>
            </label>
            <p className="key-field-help">{t('keysOperationConfirm')}</p>
            <button
              className="btn btn-primary"
              disabled={!target || !keeper || target === keeper || available.length <= 1}
              onClick={() => void prepare()}
            >
              {preset ? k('confirmOperation') : t('keysOperationPrepare')}
            </button>
          </fieldset>
          <button className="btn btn-secondary" disabled={busy} onClick={() => void read()}>
            {t('keysOperationCheck')}
          </button>
        </>
      )}
    </section>
  );
  return onClose ? (
    <Modal
      open={open}
      onClose={onClose}
      title={preset?.mode === 'replace' ? k('quickReplace') : k('quickRevoke')}
      closeDisabled={busy || unresolved || !checked}
      className="key-operation-modal"
      width={560}
    >
      {content}
    </Modal>
  ) : (
    content
  );
}
