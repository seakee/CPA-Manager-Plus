import { useEffect, useRef, useState } from 'react';
import { Modal } from '../components/ui/Modal';
import { externalWorkspaceText as x } from '../i18n/freshExternal';
import { PublicApiError, type PublicApiClient } from './publicApi';
import { newExternalRequestId } from './externalState';

type RebindOutcome =
  | { kind: 'none' }
  | { kind: 'error'; message: string; stale: boolean }
  | { kind: 'succeeded'; reverified: boolean };

/**
 * The Rebind dialog. There is no address field because a rebind replaces a
 * credential and never the installation: naming another CPA is refused by
 * design. The key lives in this component only, is sent once, and is cleared as
 * soon as the answer arrives so a closed dialog cannot keep a secret around.
 */
export function FreshRebindDialog({
  open,
  client,
  credential,
  onClose,
  onRebound,
}: {
  open: boolean;
  client: PublicApiClient;
  credential: string;
  onClose: () => void;
  onRebound: (outcome: { reverified: boolean }) => void;
}) {
  const [managementKey, setManagementKey] = useState('');
  const [busy, setBusy] = useState(false);
  const [outcome, setOutcome] = useState<RebindOutcome>({ kind: 'none' });
  // The request ID of an attempt whose answer was not read. Keeping it is what
  // makes a retry a replay of the same request rather than a new write.
  const requestId = useRef('');
  const alive = useRef(true);

  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  useEffect(() => {
    if (!open) {
      setManagementKey('');
      setOutcome({ kind: 'none' });
      requestId.current = '';
    }
  }, [open]);

  function failureMessage(error: unknown): { message: string; stale: boolean } {
    if (!(error instanceof PublicApiError))
      return { message: x('rebindFailedHelp'), stale: false };
    switch (error.code) {
      case 'external_rebind_refused':
        return { message: error.detail || x('rebindRefusedHelp'), stale: false };
      case 'external_rebind_stale_generation':
        return { message: x('rebindStaleHelp'), stale: true };
      case 'external_management_key_rejected':
        return { message: error.detail || x('blockerKeyRejectedHelp'), stale: false };
      case 'external_target_moved':
        return { message: x('blockerTargetMovedHelp'), stale: false };
      case 'external_cpa_version_unsupported':
        return { message: x('blockerVersionUnsupportedHelp'), stale: false };
      case 'external_cpa_authority_unavailable':
        return { message: x('rebindUnavailableHelp'), stale: false };
      default:
        return { message: x('rebindFailedHelp'), stale: false };
    }
  }

  async function submit() {
    if (busy || !managementKey.trim()) return;
    setBusy(true);
    setOutcome({ kind: 'none' });
    if (!requestId.current) requestId.current = newExternalRequestId();
    try {
      const result = await client.post<{ status?: string }>(
        'external/rebind',
        { requestId: requestId.current, managementKey },
        { kind: 'admin', value: credential }
      );
      if (!alive.current) return;
      if (result?.status !== 'rebound' && result?.status !== 'reverified') {
        setOutcome({ kind: 'error', message: x('rebindFailedHelp'), stale: false });
        return;
      }
      requestId.current = '';
      setManagementKey('');
      setOutcome({ kind: 'succeeded', reverified: result.status === 'reverified' });
      onRebound({ reverified: result.status === 'reverified' });
    } catch (error) {
      if (!alive.current) return;
      setOutcome({ kind: 'error', ...failureMessage(error) });
    } finally {
      if (alive.current) setBusy(false);
    }
  }

  const succeeded = outcome.kind === 'succeeded' ? outcome : null;
  return (
    <Modal
      open={open}
      title={x('rebindTitle')}
      className="key-create-modal"
      closeDisabled={busy}
      onClose={() => {
        if (busy) return;
        onClose();
      }}
      footer={
        succeeded ? (
          <button className="btn btn-primary" onClick={onClose} data-testid="rebind-done">
            {x('rebindCancel')}
          </button>
        ) : (
          <>
            <button
              className="btn btn-secondary"
              type="button"
              disabled={busy}
              onClick={onClose}
              data-testid="rebind-cancel"
            >
              {x('rebindCancel')}
            </button>
            <button
              className="btn btn-primary"
              type="button"
              disabled={busy || !managementKey.trim()}
              onClick={() => void submit()}
              data-testid="rebind-submit"
            >
              {busy ? x('rebindSubmitting') : x('rebindSubmit')}
            </button>
          </>
        )
      }
    >
      {succeeded ? (
        <p role="status" data-testid="rebind-result">
          {succeeded.reverified ? x('rebindSucceededReverified') : x('rebindSucceededRebound')}
        </p>
      ) : (
        <form
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          <p className="key-field-help">{x('rebindHelp')}</p>
          <label className="form-label" htmlFor="rebind-management-key">
            {x('rebindKeyLabel')}
          </label>
          <input
            id="rebind-management-key"
            className="input"
            type="password"
            autoComplete="off"
            spellCheck={false}
            data-testid="rebind-management-key"
            value={managementKey}
            disabled={busy}
            onChange={(event) => setManagementKey(event.target.value)}
          />
          {outcome.kind === 'error' && (
            <p className="key-notice key-notice-warning" role="alert" data-testid="rebind-error">
              {outcome.message}
            </p>
          )}
        </form>
      )}
    </Modal>
  );
}
