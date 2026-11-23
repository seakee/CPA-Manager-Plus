import { useEffect, useRef, useState } from 'react';
import { fullWebText as t } from '../i18n/fullweb';
import { Button } from '../components/ui/Button';
import { DropdownMenu } from '../components/ui/DropdownMenu';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { SegmentedTabs } from '../components/ui/SegmentedTabs';
import { Select } from '../components/ui/Select';
import { PageControlBar } from '../components/common/PageControlBar';
import { PaginationFooter } from '../components/common/PaginationFooter';
import { SortControl, type SortDirection } from '../components/ui/SortControl';
import {
  formatKeyPageSizeOption,
  formatKeyPaginationInfo,
  formatKeyTableCount,
  keyWorkspaceText as k,
} from '../i18n/freshKeyWorkspace';
import { externalWorkspaceText as x } from '../i18n/freshExternal';
import { FullWebIcon } from './FullWebIcon';
import './freshKeys.css';
import { useHeaderRefresh } from '../hooks/useHeaderRefresh';
import { PublicApiError, type PublicApiClient } from './publicApi';
import { FreshKeyDetails, FreshKeyRenameModal } from './FreshKeyDetails';
import { FreshKeyRevocation } from './FreshKeyRevocation';
import { FreshKeyRegenerate } from './FreshKeyRegenerate';
import { AccessInfoSummary, type AccessInfo } from './FreshAccessInfo';
import {
  parseUsageSummary,
  usageCountText,
  usageCoveragePill,
  usageReasonText,
  usageStateText,
  usageTimeText,
  type UsageSummary,
} from './freshKeyUsage';
import {
  effectBlockerTextKey,
  isTerminalKeyOperationOutcome,
  keyDisplayName,
  keyUnifiedStatus,
  maskedPreviewOf,
  validKey,
  validKeyNameInput,
  validKeyOperationResult,
  validSavedSecret,
  type Key,
  type KeyOperation,
  type KeyOperationResult,
} from './freshKeyMetadata';
import { blockerOf, type ExternalBlocker } from './externalState';
import { FreshExternalBlocker } from './FreshExternalBlocker';

function extractErrorKey(err: PublicApiError, expectedRequestId?: string): Key | null {
  const raw = err.key;
  if (!raw || typeof raw !== 'object') return null;
  const candidate = raw as Key;
  if (!validKey(candidate)) return null;
  if (expectedRequestId && candidate.requestId !== expectedRequestId) return null;
  return candidate;
}

function extractErrorOperation(err: PublicApiError): KeyOperation | null {
  const res = err.operationResult as KeyOperationResult | undefined;
  if (!res || !validKeyOperationResult(res)) return null;
  return res.operation;
}

function randomRequestId(): string {
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) =>
    b.toString(16).padStart(2, '0')
  ).join('');
}

function validScopedOperationSnapshot(
  raw: unknown,
  expectedOperationId: string,
  expectedTargetId: string
): KeyOperationResult | null {
  if (!raw || typeof raw !== 'object') return null;
  const candidate = raw as KeyOperationResult;
  if (!validKeyOperationResult(candidate)) return null;
  if (
    candidate.operation &&
    (candidate.operation.id !== expectedOperationId ||
      (candidate.operation.targetId !== expectedTargetId &&
        candidate.operation.keeperId !== expectedTargetId))
  ) {
    return null;
  }
  return candidate;
}

function isCommittedMissingAddResponse(
  err: PublicApiError,
  updatedKey: Key,
  expectedId: string
): boolean {
  if (err.code !== 'key_operation_unknown' || updatedKey.runtimeState !== 'not_applied') {
    return false;
  }
  if (!err.operationResult || typeof err.operationResult !== 'object') return false;
  const res = err.operationResult as KeyOperationResult;
  if (!validKeyOperationResult(res) || !res.operation) return false;
  return (
    res.operation.mode === 'add' &&
    res.operation.outcome === 'committed' &&
    res.operation.targetId === expectedId &&
    res.operation.keeperId === ''
  );
}
function isExternalOrSourceBlocker(code?: string): boolean {
  if (!code) return false;
  if (blockerOf(code) !== null) return true;
  return [
    'key_operation_source_conflict',
    'key_operation_conflict',
    'key_operation_generation_mismatch',
    'key_operation_host_proof_required',
    'config_authority_unavailable',
  ].includes(code);
}

type KeySortKey = 'created' | 'recent' | 'calls24h' | 'name';

const KEY_SORT_DEFAULT_DIRECTIONS: Record<KeySortKey, SortDirection> = {
  created: 'desc',
  recent: 'desc',
  calls24h: 'desc',
  name: 'asc',
};

const KEY_PAGE_SIZE_OPTIONS = [10, 20, 50] as const;

export function FreshApiKeys({
  client,
  credential,
  onUnauthorized,
  onCreationPending,
  storedBadge: _storedBadge,
  pageDescription,
  visible = true,
  onRebind,
  reloadToken = 0,
}: {
  client: PublicApiClient;
  credential: string;
  onUnauthorized: () => void;
  onCreationPending: (pending: boolean) => void;
  storedBadge?: string;
  pageDescription?: string;
  visible?: boolean;
  /** Opens the Rebind dialog. Present only on an External installation. */
  onRebind?: () => void;
  /** Bumped by the shell after a successful rebind to re-read this page. */
  reloadToken?: number;
}) {
  const [keys, setKeys] = useState<Key[] | null>(null);
  const [gatewayPending, setGatewayPending] = useState(true);
  const [presenceUnverified, setPresenceUnverified] = useState(false);
  const [accessInfo, setAccessInfo] = useState<AccessInfo | null>(null);
  // A typed External blocker is the page's own contract. It is kept apart from
  // the generic failure so the page names the state and offers its way out
  // instead of collapsing everything into "key state is unavailable".
  const [accessReloadToken, setAccessReloadToken] = useState(0);
  const [blocker, setBlocker] = useState<ExternalBlocker | null>(null);
  const [usage, setUsage] = useState<UsageSummary | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [pending, setPending] = useState('');
  const [retryAllowed, setRetryAllowed] = useState(false);
  const [secret, setSecret] = useState('');
  const [copyFeedback, setCopyFeedback] = useState<'copied' | 'failed' | ''>('');
  const [persistedCopyFeedback, setPersistedCopyFeedback] = useState<'copied' | 'failed' | ''>('');
  const [persistedCopying, setPersistedCopying] = useState(false);
  const [confirmed, setConfirmed] = useState(false);
  const [confirmedEffect, setConfirmedEffect] = useState(false);
  const [createdKeyId, setCreatedKeyId] = useState('');
  const [recoveryKey, setRecoveryKey] = useState<{
    key: Key;
    code: string;
    detail?: string;
    operation: KeyOperation | null;
    statusUnknown?: boolean;
  } | null>(null);
  const [createName, setCreateName] = useState('');
  const [createOpen, setCreateOpen] = useState(false);
  const [selected, setSelected] = useState('');
  const selectedRef = useRef(selected);
  selectedRef.current = selected;
  const [renameTarget, setRenameTarget] = useState<Key | null>(null);
  const [detailIntent, setDetailIntent] = useState<{
    tab?: 'overview' | 'usage';
    action?: 'rename';
  } | null>(null);
  const [rowCopyState, setRowCopyState] = useState<{
    id: string;
    status: 'copying' | 'copied' | 'failed';
  } | null>(null);
  const [search, setSearch] = useState('');
  const [filter, setFilter] = useState<'all' | 'saved' | 'replaced'>('saved');
  const [viewMode, setViewMode] = useState<'table' | 'cards'>('table');
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [workspaceTab, setWorkspaceTab] = useState<'keys' | 'access'>('keys');
  const [runtimeFilter, setRuntimeFilter] = useState<'all' | 'effective' | 'attention'>('all');
  const [sortBy, setSortBy] = useState<{ key: KeySortKey; direction: SortDirection }>({
    key: 'created',
    direction: 'desc',
  });
  const [detailPending, setDetailPending] = useState(false);
  const detailPendingRef = useRef(false);
  const [revocationAvailable, setRevocationAvailable] = useState(false);
  const [operationPending, setOperationPending] = useState(false);
  const [latestOperationResult, setLatestOperationResult] = useState<KeyOperationResult | null>(
    null
  );
  const [revocationOpen, setRevocationOpen] = useState(false);
  const [revocationPreset, setRevocationPreset] = useState<{
    targetId: string;
    mode: 'revoke' | 'replace';
    seq: number;
  } | null>(null);
  const [regenerateTargetId, setRegenerateTargetId] = useState('');
  const regeneratePendingRef = useRef(false);
  const creationPendingRef = useRef(false);
  const operationPendingRef = useRef(false);
  const revocationOpenRef = useRef(false);
  const regenerateOpenRef = useRef(false);
  revocationOpenRef.current = revocationOpen;
  const regenerateChangedRef = useRef(false);
  regenerateOpenRef.current = Boolean(regenerateTargetId);
  const alive = useRef(true);
  // Disjoint ownership for reads vs key mutations so overlapping operations
  // never clear each other's lock, while `busy` reflects whether either owns
  // the page. Only the read matching `activeReadEpoch` owns the read lock.
  const readEpoch = useRef(0);
  const activeReadEpoch = useRef<number | null>(null);
  const activeMutation = useRef(false);
  // Coalesces at most one pending refresh requested while a read or mutation
  // was open, or after a read observed a concurrent key-list mutation.
  const pendingRefresh = useRef(false);
  // Tracks rebinds on the active session so pre-rebind errors do not overwrite
  // post-rebind state, while ownerEpoch invalidates replies across credential changes.
  const rebindSeq = useRef(0);
  const ownerEpoch = useRef(0);
  const prevCredentialRef = useRef(credential);
  // Bumped on every local key-list write (`createKey` POST, `reconcile`,
  // `FreshKeyDetails`/`FreshKeyRenameModal` `onSaved`) so a read that started
  const mutationRev = useRef(0);
  const copyAttempt = useRef(0);
  const persistedCopyAttempt = useRef(0);
  const rowCopyAttempt = useRef(0);
  const rowCopyTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pendingReapplyOpId = useRef('');
  const secretPendingRef = useRef(false);
  const auth = { kind: 'admin' as const, value: credential };

  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      ownerEpoch.current += 1;
      readEpoch.current += 1;
      activeReadEpoch.current = null;
      activeMutation.current = false;
      pendingRefresh.current = false;
      if (rowCopyTimer.current !== null) {
        clearTimeout(rowCopyTimer.current);
        rowCopyTimer.current = null;
      }
    };
  }, []);
  // one panel must not release another panel's unresolved write.
  function reportPending() {
    onCreationPending(
      creationPendingRef.current ||
        secretPendingRef.current ||
        detailPendingRef.current ||
        operationPendingRef.current ||
        regeneratePendingRef.current
    );
  }

  function syncBusy() {
    if (alive.current) setBusy(activeReadEpoch.current !== null || activeMutation.current);
  }

  function finishMutation() {
    activeMutation.current = false;
    if (alive.current && pendingRefresh.current && activeReadEpoch.current === null && !selectedRef.current && !detailPendingRef.current) {
      pendingRefresh.current = false;
      void refresh(true);
    } else {
      syncBusy();
    }
  }

  function failure(err: unknown) {
    if (err instanceof PublicApiError && err.kind === 'authentication') onUnauthorized();
    else {
      // A typed External code is a state the page names, not a generic outage.
      const typed = err instanceof PublicApiError ? blockerOf(err.code) : null;
      setBlocker(
        typed && err instanceof PublicApiError
          ? typed.kind === 'key_rejected'
            ? { ...typed, detail: err.detail }
            : typed
          : null
      );
      setError(typed ? '' : t('keysUnavailable'));
    }
  }

  async function refresh(forced = false) {
    // Forced refreshes (rebind, committed revocation, explicit blocker reload)
    // retire pre-existing reads immediately and run even if details are open.
    // An unforced refresh coalesces behind any open operation or drawer.
    if (
      !forced &&
      (activeReadEpoch.current !== null ||
        activeMutation.current ||
        selectedRef.current ||
        detailPendingRef.current)
    ) {
      pendingRefresh.current = true;
      return;
    }
    pendingRefresh.current = false;
    const epoch = ++readEpoch.current;
    const ownerAtStart = ownerEpoch.current;
    activeReadEpoch.current = epoch;
    const mutationsAtStart = mutationRev.current;
    let authenticatedFailure = false;
    syncBusy();
    setError('');
    try {
      const result = await client.get<{
        keys: Key[];
        gatewayPending: boolean;
        revocationAvailable: boolean;
        presenceUnverified?: boolean;
      }>('api-keys', auth);
      if (!alive.current || readEpoch.current !== epoch || ownerEpoch.current !== ownerAtStart)
        return;
      if (
        !Array.isArray(result.keys) ||
        !result.keys.every(validKey) ||
        new Set(result.keys.map((key) => key.id)).size !== result.keys.length ||
        typeof result.gatewayPending !== 'boolean' ||
        typeof result.revocationAvailable !== 'boolean' ||
        (result.presenceUnverified !== undefined && typeof result.presenceUnverified !== 'boolean')
      )
        throw new Error();
      // A mutation committed while this read was open: skip this snapshot and
      // re-read once settled so neither the write nor the full list is lost.
      if (mutationRev.current !== mutationsAtStart) {
        pendingRefresh.current = true;
        return;
      }
      setKeys(result.keys);
      setGatewayPending(result.gatewayPending);
      setRevocationAvailable(result.revocationAvailable);
      setPresenceUnverified(!!result.presenceUnverified);
      setBlocker(null);
      try {
      if (
        result.revocationAvailable &&
        !operationPendingRef.current &&
        !regeneratePendingRef.current &&
        result.keys.some((item) => Boolean(item.operationId) || item.runtimeState === 'effect_unknown')
      ) {
        try {
          const latestOp = await client.get<KeyOperationResult>('api-keys/operations/latest', auth);
          if (
            alive.current &&
            readEpoch.current === epoch &&
            ownerEpoch.current === ownerAtStart &&
            !revocationOpenRef.current &&
            !regenerateOpenRef.current &&
            validKeyOperationResult(latestOp) &&
            latestOp.operation &&
            !isTerminalKeyOperationOutcome(latestOp.operation.outcome)
          ) {
            setLatestOperationResult(latestOp);
            operationPendingRef.current = true;
            setOperationPending(true);
            reportPending();
          }
        } catch {
          // Best-effort operation sync during list refresh.
        }
      }
        const parsed = parseUsageSummary(await client.get<unknown>('api-keys/usage-summary', auth));
        if (!parsed) throw new Error('Invalid usage summary response');
        if (alive.current && readEpoch.current === epoch && ownerEpoch.current === ownerAtStart)
          setUsage(parsed);
      } catch (usageErr) {
        if (!alive.current || readEpoch.current !== epoch || ownerEpoch.current !== ownerAtStart)
          return;
        if (usageErr instanceof PublicApiError && usageErr.kind === 'authentication') {
          authenticatedFailure = true;
          onUnauthorized();
        }
        setUsage(null);
      }
      // Usage is intentionally best-effort; a failed read leaves the key list usable.
    } catch (err) {
      if (alive.current && readEpoch.current === epoch && ownerEpoch.current === ownerAtStart) {
        if (err instanceof PublicApiError && err.kind === 'authentication') {
          authenticatedFailure = true;
        }
        failure(err);
      }
    } finally {
      if (ownerEpoch.current === ownerAtStart && activeReadEpoch.current === epoch) {
        activeReadEpoch.current = null;
        if (authenticatedFailure) pendingRefresh.current = false;
        if (alive.current && pendingRefresh.current && !activeMutation.current && !selectedRef.current && !detailPendingRef.current) {
          pendingRefresh.current = false;
          void refresh(true);
        } else {
          syncBusy();
        }
      }
    }
  }

  async function refreshUsageSummarySilently(
    ownerAtStart = ownerEpoch.current,
    rebindAtStart = rebindSeq.current
  ) {
    try {
      const parsed = parseUsageSummary(
        await client.get<unknown>('api-keys/usage-summary', auth)
      );
      if (!parsed) return;
      if (
        alive.current &&
        ownerEpoch.current === ownerAtStart &&
        rebindSeq.current === rebindAtStart
      ) {
        setUsage(parsed);
      }
    } catch (usageErr) {
      if (
        alive.current &&
        ownerEpoch.current === ownerAtStart &&
        rebindSeq.current === rebindAtStart &&
        usageErr instanceof PublicApiError &&
        usageErr.kind === 'authentication'
      ) {
        onUnauthorized();
      }
    }
  }
  const headerRefreshRef = useRef<() => Promise<void>>(() => Promise.resolve());
  headerRefreshRef.current = () => refresh();
  const handleHeaderRefresh = useRef(() => headerRefreshRef.current());
  useHeaderRefresh(handleHeaderRefresh.current, visible);


  // No credential, secret or request journal enters persistent browser storage.
  // A successful rebind changes `reloadToken`, clearing stale blockers
  // immediately and retiring pre-rebind reads even while details are open.
  // A credential change additionally resets owner state and pending locks.
  useEffect(() => {
    rebindSeq.current += 1;
    pendingReapplyOpId.current = '';
    if (prevCredentialRef.current !== credential) {
      prevCredentialRef.current = credential;
      ownerEpoch.current += 1;
      readEpoch.current += 1;
      activeReadEpoch.current = null;
      activeMutation.current = false;
      pendingRefresh.current = false;
      creationPendingRef.current = false;
      secretPendingRef.current = false;
      detailPendingRef.current = false;
      operationPendingRef.current = false;
      regeneratePendingRef.current = false;
      rowCopyAttempt.current += 1;
      if (rowCopyTimer.current !== null) {
        clearTimeout(rowCopyTimer.current);
        rowCopyTimer.current = null;
      }
      setKeys(null);
      setUsage(null);
      setPresenceUnverified(false);
      setPending('');
      setRetryAllowed(false);
      setSecret('');
      setCopyFeedback('');
      setPersistedCopyFeedback('');
      setPersistedCopying(false);
      setConfirmed(false);
      setConfirmedEffect(false);
      setCreatedKeyId('');
      setRecoveryKey(null);
      setCreateName('');
      setCreateOpen(false);
      setSelected('');
      setDetailIntent(null);
      setDetailPending(false);
      setRenameTarget(null);
      regenerateOpenRef.current = false;
      regenerateChangedRef.current = false;
      setRegenerateTargetId('');
      setRowCopyState(null);
      setOperationPending(false);
      revocationOpenRef.current = false;
      setRevocationOpen(false);
      setRevocationPreset(null);
      setLatestOperationResult(null);
      reportPending();
    }
    setBlocker(null);
    setError('');
    setAccessInfo(null);
    void refresh(true);
  }, [reloadToken, credential]); // eslint-disable-line react-hooks/exhaustive-deps

  async function copyCreatedSecret() {
    if (!secret) return;
    const attempt = ++copyAttempt.current;
    const ownerAtStart = ownerEpoch.current;
    setCopyFeedback('');
    try {
      if (!navigator.clipboard?.writeText) throw new Error('Clipboard unavailable');
      await navigator.clipboard.writeText(secret);
      if (alive.current && attempt === copyAttempt.current && ownerEpoch.current === ownerAtStart)
        setCopyFeedback('copied');
    } catch {
      if (alive.current && attempt === copyAttempt.current && ownerEpoch.current === ownerAtStart)
        setCopyFeedback('failed');
    }
  }

  function mergeCanonicalKey(latest: Key) {
    mutationRev.current += 1;
    setKeys((previous) =>
      previous
        ? previous.some((item) => item.id === latest.id)
          ? previous.map((item) => (item.id === latest.id ? latest : item))
          : [...previous, latest]
        : [latest]
    );
    setRecoveryKey((prev) =>
      prev && prev.key.id === latest.id
        ? {
            ...prev,
            key: latest,
            code:
              latest.lifecycle === 'superseded'
                ? ''
                : latest.effectCode || prev.code,
            detail:
              latest.lifecycle === 'superseded' || latest.runtimeState === 'revoked_observed'
                ? ''
                : prev.detail,
            statusUnknown:
              latest.lifecycle !== 'superseded' && latest.runtimeState === 'effect_unknown',
          }
        : prev
    );
    if (
      latest.lifecycle === 'superseded' ||
      latest.runtimeState === 'revoked_observed' ||
      latest.runtimeState === 'effective_observed'
    ) {
      pendingReapplyOpId.current = '';
    }
    if (latest.lifecycle === 'superseded') {
      creationPendingRef.current = false;
      reportPending();
    }
  }

  async function copyPersistedKeySecret(targetKey: Key) {
    if (!targetKey || targetKey.lifecycle === 'superseded') return;
    const attempt = ++persistedCopyAttempt.current;
    const ownerAtStart = ownerEpoch.current;
    const rebindAtStart = rebindSeq.current;
    setPersistedCopying(true);
    setPersistedCopyFeedback('');
    try {
      const value = await client.post<{ secret: string }>(
        `api-keys/${targetKey.id}/secret`,
        {},
        auth
      );
      if (
        !alive.current ||
        attempt !== persistedCopyAttempt.current ||
        ownerEpoch.current !== ownerAtStart
      )
        return;
      if (!value || typeof value.secret !== 'string') throw new Error();
      if (!validSavedSecret(targetKey, value.secret)) {
        const latest = await client.get<Key>(`api-keys/${targetKey.id}`, auth);
        if (
          !alive.current ||
          attempt !== persistedCopyAttempt.current ||
          ownerEpoch.current !== ownerAtStart ||
          rebindSeq.current !== rebindAtStart
        )
          return;
        if (!validKey(latest) || latest.id !== targetKey.id) throw new Error();
        mergeCanonicalKey(latest);
        if (latest.lifecycle === 'superseded' || !validSavedSecret(latest, value.secret))
          throw new Error();
      }
      if (!navigator.clipboard?.writeText) throw new Error('Clipboard unavailable');
      await navigator.clipboard.writeText(value.secret);
      if (
        alive.current &&
        attempt === persistedCopyAttempt.current &&
        ownerEpoch.current === ownerAtStart
      )
        setPersistedCopyFeedback('copied');
    } catch (err) {
      if (
        !alive.current ||
        attempt !== persistedCopyAttempt.current ||
        ownerEpoch.current !== ownerAtStart
      )
        return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        onUnauthorized();
        return;
      }
      setPersistedCopyFeedback('failed');
      if (err instanceof PublicApiError && err.status === 409) {
        try {
          const latest = await client.get<Key>(`api-keys/${targetKey.id}`, auth);
          if (
            !alive.current ||
            attempt !== persistedCopyAttempt.current ||
            ownerEpoch.current !== ownerAtStart ||
            rebindSeq.current !== rebindAtStart
          )
            return;
          if (validKey(latest) && latest.id === targetKey.id) {
            mergeCanonicalKey(latest);
          }
        } catch (readErr) {
          if (
            !alive.current ||
            attempt !== persistedCopyAttempt.current ||
            ownerEpoch.current !== ownerAtStart
          )
            return;
          if (readErr instanceof PublicApiError && readErr.kind === 'authentication') {
            onUnauthorized();
            return;
          }
        }
      }
    } finally {
      if (
        alive.current &&
        attempt === persistedCopyAttempt.current &&
        ownerEpoch.current === ownerAtStart
      ) {
        setPersistedCopying(false);
      }
    }
  }

  function openKeyDetails(
    keyId: string,
    intent?: { tab?: 'overview' | 'usage'; action?: 'rename' }
  ) {
    if (locked) return;
    setDetailIntent(intent ?? null);
    setSelected(keyId);
  }

  async function copyRowKeySecret(targetKey: Key) {
    if (!targetKey || targetKey.lifecycle === 'superseded' || locked) return;
    if (rowCopyTimer.current !== null) {
      clearTimeout(rowCopyTimer.current);
      rowCopyTimer.current = null;
    }
    const attempt = ++rowCopyAttempt.current;
    const ownerAtStart = ownerEpoch.current;
    const rebindAtStart = rebindSeq.current;
    setRowCopyState({ id: targetKey.id, status: 'copying' });
    try {
      const value = await client.post<{ secret: string }>(
        `api-keys/${targetKey.id}/secret`,
        {},
        auth
      );
      if (
        !alive.current ||
        attempt !== rowCopyAttempt.current ||
        ownerEpoch.current !== ownerAtStart
      )
        return;
      if (!value || typeof value.secret !== 'string') throw new Error();
      if (!validSavedSecret(targetKey, value.secret)) {
        const latest = await client.get<Key>(`api-keys/${targetKey.id}`, auth);
        if (
          !alive.current ||
          attempt !== rowCopyAttempt.current ||
          ownerEpoch.current !== ownerAtStart ||
          rebindSeq.current !== rebindAtStart
        )
          return;
        if (!validKey(latest) || latest.id !== targetKey.id) throw new Error();
        mergeCanonicalKey(latest);
        if (latest.lifecycle === 'superseded' || !validSavedSecret(latest, value.secret))
          throw new Error();
      }
      if (!navigator.clipboard?.writeText) throw new Error('Clipboard unavailable');
      await navigator.clipboard.writeText(value.secret);
      if (
        alive.current &&
        attempt === rowCopyAttempt.current &&
        ownerEpoch.current === ownerAtStart
      ) {
        setRowCopyState({ id: targetKey.id, status: 'copied' });
        rowCopyTimer.current = setTimeout(() => {
          rowCopyTimer.current = null;
          if (alive.current && attempt === rowCopyAttempt.current) {
            setRowCopyState((current) => (current?.id === targetKey.id ? null : current));
          }
        }, 2500);
      }
    } catch (err) {
      if (
        !alive.current ||
        attempt !== rowCopyAttempt.current ||
        ownerEpoch.current !== ownerAtStart
      )
        return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        onUnauthorized();
        return;
      }
      setRowCopyState({ id: targetKey.id, status: 'failed' });
      rowCopyTimer.current = setTimeout(() => {
        rowCopyTimer.current = null;
        if (alive.current && attempt === rowCopyAttempt.current) {
          setRowCopyState((current) => (current?.id === targetKey.id ? null : current));
        }
      }, 3000);
      if (err instanceof PublicApiError && err.status === 409) {
        try {
          const latest = await client.get<Key>(`api-keys/${targetKey.id}`, auth);
          if (
            !alive.current ||
            attempt !== rowCopyAttempt.current ||
            ownerEpoch.current !== ownerAtStart ||
            rebindSeq.current !== rebindAtStart
          )
            return;
          if (validKey(latest) && latest.id === targetKey.id) {
            mergeCanonicalKey(latest);
          }
        } catch (readErr) {
          if (readErr instanceof PublicApiError && readErr.kind === 'authentication') {
            onUnauthorized();
            return;
          }
        }
      }
    }
  }

  function closeCreatedSecretState(shouldRefreshUsage: boolean) {
    ++copyAttempt.current;
    ++persistedCopyAttempt.current;
    setCopyFeedback('');
    setPersistedCopyFeedback('');
    setPersistedCopying(false);
    setSecret('');
    secretPendingRef.current = false;
    if (!recoveryKey) {
      setError('');
      setConfirmed(false);
      setConfirmedEffect(false);
      setCreatedKeyId('');
      setCreateOpen(false);
      if (shouldRefreshUsage) {
        void refreshUsageSummarySilently();
      }
    }
    reportPending();
  }
  function dismissCreatedSecret() {
    closeCreatedSecretState(!recoveryKey && Boolean(createdKeyId || confirmed || secret));
  }

  function dismissRecoveryKey(andRebind = false) {
    if (!canDismissRecovery || !recoveryKey) return;
    const code = recoveryKey.code || recoveryKey.key.effectCode;
    const typed = blockerOf(code);
    if (typed) {
      setBlocker((prev) =>
        typed.kind === 'key_rejected'
          ? recoveryKey.detail
            ? { ...typed, detail: recoveryKey.detail }
            : prev?.kind === 'key_rejected' && prev.detail
              ? prev
              : typed
          : prev ?? typed
      );
    }
    ++persistedCopyAttempt.current;
    pendingReapplyOpId.current = '';
    setError('');
    setPersistedCopyFeedback('');
    setPersistedCopying(false);
    setRecoveryKey(null);
    setCreateName('');
    setConfirmed(false);
    setConfirmedEffect(false);
    setCreatedKeyId('');
    setCreateOpen(false);
    creationPendingRef.current = false;
    reportPending();
    if (andRebind && onRebind) {
      onRebind();
    }
  }

  function closeCreateResultModal() {
    const shouldRefreshUsage = !recoveryKey && Boolean(createdKeyId || confirmed || secret);
    if (
      activeReadEpoch.current !== null ||
      activeMutation.current ||
      persistedCopying ||
      pending ||
      secret
    ) {
      if (
        secret &&
        !pending &&
        !recoveryKey &&
        activeReadEpoch.current === null &&
        !activeMutation.current &&
        !persistedCopying
      ) {
        closeCreatedSecretState(shouldRefreshUsage);
      }
      return;
    }
    if (recoveryKey) {
      if (canDismissRecovery) dismissRecoveryKey(false);
      return;
    }
    ++persistedCopyAttempt.current;
    setError('');
    setPersistedCopyFeedback('');
    setConfirmed(false);
    setConfirmedEffect(false);
    setCreatedKeyId('');
    closeCreatedSecretState(shouldRefreshUsage);
    setCreateName('');
    setCreateOpen(false);
  }

  function applySettledCreateKey(
    settled: Key,
    createdSecret: string | undefined,
    operation: KeyOperation | null,
    effectErrorCode: string,
    effectErrorDetail = ''
  ) {
    const isPreStrategy =
      gatewayPending && !revocationAvailable && settled.runtimeState === 'not_applied';
    const isReady = settled.runtimeState === 'effective_observed' || isPreStrategy;
    mutationRev.current += 1;
    setKeys((previous) => [...(previous || []).filter((key) => key.id !== settled.id), settled]);
    setCreatedKeyId(settled.id);
    setPending('');
    setRetryAllowed(false);
    if (createdSecret !== undefined) {
      setSecret(createdSecret);
      secretPendingRef.current = !!createdSecret;
    }
    if (isReady) {
      setRecoveryKey(null);
      setConfirmed(true);
      setConfirmedEffect(settled.runtimeState === 'effective_observed');
      return true;
    }
    setConfirmed(false);
    setConfirmedEffect(false);
    setRecoveryKey({
      key: settled,
      code: effectErrorCode || settled.effectCode || '',
      detail:
        (effectErrorCode || settled.effectCode) === 'external_management_key_rejected'
          ? effectErrorDetail
          : '',
      operation,
      statusUnknown: settled.runtimeState === 'effect_unknown',
    });
    creationPendingRef.current = true;
    reportPending();
    return false;
  }

  async function createKey() {
    if (
      activeReadEpoch.current !== null ||
      activeMutation.current ||
      selected ||
      detailPendingRef.current ||
      secret ||
      recoveryKey ||
      (pending && !retryAllowed) ||
      operationPendingRef.current
    )
      return;
    const desiredName = createName.trim();
    if (!validKeyNameInput(desiredName)) {
      setError(t('keysNameRejected'));
      return;
    }
    activeMutation.current = true;
    const rebindAtStart = rebindSeq.current;
    const ownerAtStart = ownerEpoch.current;
    syncBusy();
    setError('');
    setConfirmed(false);
    setConfirmedEffect(false);
    setCreatedKeyId('');
    setRetryAllowed(false);
    ++copyAttempt.current;
    ++persistedCopyAttempt.current;
    setCopyFeedback('');
    setPersistedCopyFeedback('');
    let requestId = '';
    try {
      requestId =
        pending ||
        Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) =>
          byte.toString(16).padStart(2, '0')
        ).join('');
      creationPendingRef.current = true;
      reportPending();
      const payload = desiredName ? { requestId, name: desiredName } : { requestId };
      const result = await client.post<{
        key: Key;
        createdSecret?: string;
        operation?: KeyOperation;
      }>('api-keys', payload, auth);
      if (!alive.current || ownerEpoch.current !== ownerAtStart) return;
      if (
        !validKey(result.key) ||
        result.key.requestId !== requestId ||
        (result.createdSecret !== undefined &&
          (maskedPreviewOf(result.createdSecret) === '' ||
            result.key.maskedPreview !== maskedPreviewOf(result.createdSecret)))
      )
        throw new Error();
      const ready = applySettledCreateKey(
        result.key,
        result.createdSecret || '',
        result.operation ?? null,
        result.key.effectCode || ''
      );
      if (ready) {
        setCreateName('');
        creationPendingRef.current = false;
        reportPending();
      }
    } catch (err) {
      if (!alive.current || ownerEpoch.current !== ownerAtStart) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        pendingRefresh.current = false;
        creationPendingRef.current = false;
        reportPending();
        onUnauthorized();
        return;
      }
      const persisted =
        err instanceof PublicApiError && requestId ? extractErrorKey(err, requestId) : null;
      if (persisted) {
        const errCode =
          (err instanceof PublicApiError ? err.code || '' : '') || persisted.effectCode || '';
        const errDetail = err instanceof PublicApiError ? err.detail || '' : '';
        applySettledCreateKey(
          persisted,
          undefined,
          err instanceof PublicApiError ? extractErrorOperation(err) : null,
          errCode,
          errDetail
        );
        if (rebindAtStart === rebindSeq.current && err instanceof PublicApiError && blockerOf(err.code)) {
          failure(err);
        }
        return;
      }
      if (err instanceof PublicApiError && err.status === 400) {
        creationPendingRef.current = false;
        reportPending();
        setError(t('keysNameRejected'));
        return;
      }
      if (requestId) setPending(requestId);
      if (rebindAtStart === rebindSeq.current) failure(err);
    } finally {
      if (ownerEpoch.current === ownerAtStart) finishMutation();
      else activeMutation.current = false;
    }
  }

  async function executeReapplyRecoveryAdd(
    targetKey: Key,
    ownerAtStart: number,
    rebindAtStart: number
  ) {
    if (rebindSeq.current !== rebindAtStart) return;
    const reapplyId = randomRequestId();
    pendingReapplyOpId.current = reapplyId;
    let preparedOk = false;
    try {
      const prep = await client.post<KeyOperationResult>(
        'api-keys/' + targetKey.id + '/revocation',
        { requestId: reapplyId, keeperId: '', mode: 'add' },
        auth
      );
      if (!alive.current || ownerEpoch.current !== ownerAtStart) return;
      if (
        !validKeyOperationResult(prep) ||
        !prep.operation ||
        prep.operation.id !== reapplyId ||
        prep.operation.targetId !== targetKey.id ||
        prep.operation.keeperId !== '' ||
        prep.operation.mode !== 'add'
      ) {
        throw new Error();
      }
      preparedOk = true;
      let settledOp = prep;
      if (prep.operation.outcome === 'pending' && prep.nextStep === 'add') {
        if (rebindSeq.current !== rebindAtStart) return;
        const cont = await client.post<KeyOperationResult>(
          'api-keys/operations/' + reapplyId + '/continue',
          {},
          auth
        );
        if (!alive.current || ownerEpoch.current !== ownerAtStart) return;
        if (
          !validKeyOperationResult(cont) ||
          !cont.operation ||
          cont.operation.id !== reapplyId ||
          cont.operation.targetId !== targetKey.id ||
          cont.operation.keeperId !== '' ||
          cont.operation.mode !== 'add'
        ) {
          throw new Error();
        }
        settledOp = cont;
      }

      if (settledOp.operation!.outcome === 'aborted') {
        let refreshed = targetKey;
        try {
          const canonical = await client.get<Key>('api-keys/' + targetKey.id, auth);
          if (!alive.current || ownerEpoch.current !== ownerAtStart) return;
          if (validKey(canonical) && canonical.id === targetKey.id && canonical.requestId === targetKey.requestId) {
            refreshed = canonical;
          }
        } catch (refreshErr) {
          if (refreshErr instanceof PublicApiError && refreshErr.kind === 'authentication') {
            throw refreshErr;
          }
          if (!alive.current || ownerEpoch.current !== ownerAtStart) return;
        }
        pendingReapplyOpId.current = '';
        mutationRev.current += 1;
        setKeys((previous) => [...(previous || []).filter((item) => item.id !== refreshed.id), refreshed]);
        setCreatedKeyId(refreshed.id);
        const retired =
          refreshed.lifecycle === 'superseded' || refreshed.runtimeState === 'revoked_observed';
        const resolvedCode = retired ? '' : refreshed.effectCode || '';
        setRecoveryKey({
          key: refreshed,
          code: resolvedCode,
          detail: '',
          operation: settledOp.operation,
          statusUnknown: !retired && refreshed.runtimeState === 'effect_unknown',
        });
        if (rebindAtStart === rebindSeq.current && resolvedCode) {
          const typed = blockerOf(resolvedCode);
          if (typed) setBlocker(typed);
        }
        if (retired) {
          creationPendingRef.current = false;
          reportPending();
        }
        return;
      }

      if (
        settledOp.operation!.outcome === 'committed' &&
        settledOp.runtimeState === 'effective_observed'
      ) {
        const canonical = await client.get<Key>('api-keys/' + targetKey.id, auth);
        if (!alive.current || ownerEpoch.current !== ownerAtStart) return;
        if (
          !validKey(canonical) ||
          canonical.id !== targetKey.id ||
          canonical.requestId !== targetKey.requestId
        ) {
          throw new Error();
        }
        mutationRev.current += 1;
        setKeys((previous) => [
          ...(previous || []).filter((item) => item.id !== canonical.id),
          canonical,
        ]);
        setCreatedKeyId(canonical.id);
        if (canonical.runtimeState === 'effective_observed') {
          pendingReapplyOpId.current = '';
          setRecoveryKey(null);
          setConfirmed(true);
          setConfirmedEffect(true);
          if (rebindAtStart === rebindSeq.current) setBlocker(null);
          setCreateName('');
          creationPendingRef.current = false;
          reportPending();
        } else {
          pendingReapplyOpId.current = '';
          const retired =
            canonical.lifecycle === 'superseded' || canonical.runtimeState === 'revoked_observed';
          setRecoveryKey({
            key: canonical,
            code: retired ? '' : canonical.effectCode || '',
            detail: '',
            operation: settledOp.operation,
            statusUnknown: !retired && canonical.runtimeState === 'effect_unknown',
          });
        }
        return;
      }

      const unsettled: Key = {
        ...targetKey,
        runtimeState: 'effect_unknown',
        operationId: reapplyId,
      };
      mutationRev.current += 1;
      setKeys((previous) => [
        ...(previous || []).filter((item) => item.id !== unsettled.id),
        unsettled,
      ]);
      setRecoveryKey({
        key: unsettled,
        code: '',
        detail: '',
        operation: settledOp.operation,
        statusUnknown: true,
      });
    } catch (err) {
      if (!alive.current || ownerEpoch.current !== ownerAtStart) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        pendingRefresh.current = false;
        creationPendingRef.current = false;
        reportPending();
        onUnauthorized();
        return;
      }
      if (err instanceof PublicApiError) {
        const code = err.code || '';
        const detail = err.detail || '';
        const rawSnap =
          (err.status === 409 || err.status === 503) && err.operationResult !== undefined
            ? (err.operationResult as KeyOperationResult)
            : null;
        const validSnap =
          rawSnap && validKeyOperationResult(rawSnap) && rawSnap.operation ? rawSnap : null;
        const ownOp =
          validSnap?.operation &&
          validSnap.operation.id === reapplyId &&
          validSnap.operation.targetId === targetKey.id &&
          validSnap.operation.mode === 'add'
            ? validSnap
            : null;
        if (ownOp?.operation?.outcome === 'aborted') {
          let refreshed = targetKey;
          let resolvedCode = code === 'key_operation_unknown' ? '' : code;
          const resolvedDetail = resolvedCode === 'external_management_key_rejected' ? detail : '';
          try {
            const canonical = await client.get<Key>('api-keys/' + targetKey.id, auth);
            if (!alive.current || ownerEpoch.current !== ownerAtStart) return;
            if (
              validKey(canonical) &&
              canonical.id === targetKey.id &&
              canonical.requestId === targetKey.requestId
            ) {
              refreshed = canonical;
            }
          } catch (refreshErr) {
            if (refreshErr instanceof PublicApiError && refreshErr.kind === 'authentication') {
              pendingRefresh.current = false;
              creationPendingRef.current = false;
              reportPending();
              onUnauthorized();
              return;
            }
            if (!alive.current || ownerEpoch.current !== ownerAtStart) return;
          }
          if (!resolvedCode && refreshed.effectCode && refreshed.effectCode !== 'key_operation_unknown') {
            resolvedCode = refreshed.effectCode;
          }
          pendingReapplyOpId.current = '';
          mutationRev.current += 1;
          setKeys((previous) => [
            ...(previous || []).filter((item) => item.id !== refreshed.id),
            refreshed,
          ]);
          setCreatedKeyId(refreshed.id);
          const retired =
            refreshed.lifecycle === 'superseded' || refreshed.runtimeState === 'revoked_observed';
          setRecoveryKey({
            key: refreshed,
            code: retired ? '' : resolvedCode,
            detail: retired ? '' : resolvedDetail,
            operation: ownOp.operation,
            statusUnknown: !retired && refreshed.runtimeState === 'effect_unknown',
          });
          if (rebindAtStart === rebindSeq.current && !retired) {
            const typed = blockerOf(resolvedCode);
            if (typed) {
              setBlocker(
                typed.kind === 'key_rejected' && resolvedDetail
                  ? { ...typed, detail: resolvedDetail }
                  : typed
              );
            }
          }
          if (retired) {
            creationPendingRef.current = false;
            reportPending();
          }
          return;
        }
        if (
          !preparedOk &&
          code === 'key_operation_conflict' &&
          validSnap?.operation &&
          validSnap.operation.id !== reapplyId &&
          !isTerminalKeyOperationOutcome(validSnap.operation.outcome)
        ) {
          pendingReapplyOpId.current = '';
          setRecoveryKey({
            key: targetKey,
            code,
            detail: '',
            operation: validSnap.operation,
            statusUnknown: false,
          });
          return;
        }
        if (ownOp?.operation) {
          const nextKey: Key = {
            ...targetKey,
            runtimeState: 'effect_unknown',
            operationId: reapplyId,
            effectCode: code || targetKey.effectCode,
          };
          mutationRev.current += 1;
          setKeys((previous) => [
            ...(previous || []).filter((item) => item.id !== nextKey.id),
            nextKey,
          ]);
          setRecoveryKey({
            key: nextKey,
            code,
            detail: code === 'external_management_key_rejected' ? detail : '',
            operation: ownOp.operation,
            statusUnknown: true,
          });
          if (rebindAtStart === rebindSeq.current && blockerOf(code)) {
            failure(err);
          }
          return;
        }
        if (isExternalOrSourceBlocker(code) || [400, 404, 409].includes(err.status || 0)) {
          if (!preparedOk) pendingReapplyOpId.current = '';
          const nextKey: Key = preparedOk
            ? {
                ...targetKey,
                runtimeState: 'effect_unknown',
                operationId: reapplyId,
                effectCode: code || targetKey.effectCode,
              }
            : {
                ...targetKey,
                effectCode: code || targetKey.effectCode,
              };
          mutationRev.current += 1;
          setKeys((previous) => [
            ...(previous || []).filter((item) => item.id !== nextKey.id),
            nextKey,
          ]);
          setRecoveryKey({
            key: nextKey,
            code,
            detail: code === 'external_management_key_rejected' ? detail : '',
            operation: null,
            statusUnknown: preparedOk,
          });
          if (rebindAtStart === rebindSeq.current && blockerOf(code)) {
            failure(err);
          }
          return;
        }
      }
      setRecoveryKey((prev) => (prev ? { ...prev, code: '', detail: '', statusUnknown: true } : prev));
      if (rebindAtStart === rebindSeq.current) failure(err);
    }
  }

  async function retryApplyRecoveryKey() {
    if (
      !recoveryKey ||
      recoveryKey.statusUnknown ||
      recoveryKey.key.runtimeState === 'effect_unknown' ||
      recoveryKey.key.lifecycle === 'superseded' ||
      recoveryKey.key.runtimeState === 'revoked_observed' ||
      activeReadEpoch.current !== null ||
      activeMutation.current
    )
      return;
    activeMutation.current = true;
    const rebindAtStart = rebindSeq.current;
    const ownerAtStart = ownerEpoch.current;
    const targetKey = recoveryKey.key;
    syncBusy();
    setError('');
    try {
      const res = await client.post<{ key: Key; operation?: KeyOperation }>(
        'api-keys',
        { requestId: targetKey.requestId, ...(targetKey.name ? { name: targetKey.name } : {}) },
        auth
      );
      if (!alive.current || ownerEpoch.current !== ownerAtStart) return;
      if (
        !validKey(res?.key) ||
        res.key.id !== targetKey.id ||
        res.key.requestId !== targetKey.requestId
      )
        throw new Error();
      pendingReapplyOpId.current = '';
      mutationRev.current += 1;
      setKeys((previous) => [...(previous || []).filter((item) => item.id !== res.key.id), res.key]);
      setCreatedKeyId(res.key.id);
      if (res.key.runtimeState === 'effective_observed') {
        setRecoveryKey(null);
        setConfirmed(true);
        setConfirmedEffect(true);
        if (rebindAtStart === rebindSeq.current) setBlocker(null);
        setCreateName('');
        creationPendingRef.current = false;
        reportPending();
      } else {
        setRecoveryKey({
          key: res.key,
          code: res.key.effectCode || '',
          detail: '',
          operation: res.operation ?? null,
          statusUnknown: res.key.runtimeState === 'effect_unknown',
        });
      }
    } catch (err) {
      if (!alive.current || ownerEpoch.current !== ownerAtStart) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        pendingRefresh.current = false;
        creationPendingRef.current = false;
        reportPending();
        onUnauthorized();
        return;
      }
      const rawPersisted =
        err instanceof PublicApiError ? extractErrorKey(err, targetKey.requestId) : null;
      const persisted = rawPersisted && rawPersisted.id === targetKey.id ? rawPersisted : null;
      if (persisted && err instanceof PublicApiError && isCommittedMissingAddResponse(err, persisted, targetKey.id)) {
        await executeReapplyRecoveryAdd(persisted, ownerAtStart, rebindAtStart);
        return;
      }
      if (persisted) {
        const errCode = (err instanceof PublicApiError ? err.code || '' : '') || persisted.effectCode || '';
        const errDetail = err instanceof PublicApiError ? err.detail || '' : '';
        mutationRev.current += 1;
        setKeys((previous) => [
          ...(previous || []).filter((item) => item.id !== persisted.id),
          persisted,
        ]);
        setRecoveryKey({
          key: persisted,
          code: errCode,
          detail: errCode === 'external_management_key_rejected' ? errDetail : '',
          operation: err instanceof PublicApiError ? extractErrorOperation(err) : null,
          statusUnknown: persisted.runtimeState === 'effect_unknown',
        });
        if (rebindAtStart === rebindSeq.current && err instanceof PublicApiError && blockerOf(err.code)) {
          failure(err);
        }
        return;
      }
      setRecoveryKey((prev) => (prev ? { ...prev, code: '', detail: '', statusUnknown: true } : prev));
      if (rebindAtStart === rebindSeq.current) failure(err);
    } finally {
      if (ownerEpoch.current === ownerAtStart) finishMutation();
      else activeMutation.current = false;
    }
  }

  async function reconcileRecoveryKey() {
    if (!recoveryKey || activeReadEpoch.current !== null || activeMutation.current) return;
    activeMutation.current = true;
    const rebindAtStart = rebindSeq.current;
    const ownerAtStart = ownerEpoch.current;
    const targetKey = recoveryKey.key;
    syncBusy();
    setError('');
    try {
      let current = await client.get<Key>('api-keys/requests/' + targetKey.requestId, auth);
      if (!alive.current || ownerEpoch.current !== ownerAtStart) return;
      if (!validKey(current) || current.id !== targetKey.id || current.requestId !== targetKey.requestId)
        throw new Error();
      let nextOp: KeyOperation | null = recoveryKey.operation;
      let opErrCode = '';
      let opErrDetail = '';
      let abortedSnapshot = false;
      const opIdToCheck = current.operationId || pendingReapplyOpId.current;
      if (opIdToCheck && (current.runtimeState === 'effect_unknown' || !!pendingReapplyOpId.current)) {
        let snapshot: KeyOperationResult | null = null;
        try {
          const opRes = await client.get<KeyOperationResult>(
            'api-keys/operations/' + opIdToCheck,
            auth
          );
          if (!alive.current || ownerEpoch.current !== ownerAtStart) return;
          snapshot = validScopedOperationSnapshot(opRes, opIdToCheck, current.id);
        } catch (opErr) {
          if (opErr instanceof PublicApiError && opErr.kind === 'authentication') {
            throw opErr;
          }
          if (!alive.current || ownerEpoch.current !== ownerAtStart) return;
          if (opErr instanceof PublicApiError) {
            opErrCode = opErr.code || '';
            opErrDetail = opErr.detail || '';
            if (
              (opErr.status === 409 || opErr.status === 503) &&
              opErr.operationResult !== undefined
            ) {
              snapshot = validScopedOperationSnapshot(
                opErr.operationResult,
                opIdToCheck,
                current.id
              );
            }
          }
        }
        if (snapshot?.operation) {
          nextOp = snapshot.operation;
        }
        if (
          snapshot &&
          snapshot.operation === null &&
          current.runtimeState === 'not_applied' &&
          !current.operationId
        ) {
          pendingReapplyOpId.current = '';
        }
        if (
          snapshot &&
          (snapshot.runtimeState === 'effective_observed' ||
            snapshot.operation?.outcome === 'aborted' ||
            (snapshot.operation?.outcome === 'committed' && !!pendingReapplyOpId.current))
        ) {
          abortedSnapshot = snapshot.operation?.outcome === 'aborted';
          if (opErrCode === 'key_operation_unknown') {
            opErrCode = '';
          }
          try {
            const refreshed = await client.get<Key>('api-keys/' + current.id, auth);
            if (!alive.current || ownerEpoch.current !== ownerAtStart) return;
            if (
              validKey(refreshed) &&
              refreshed.id === current.id &&
              refreshed.requestId === current.requestId
            ) {
              current = refreshed;
            }
          } catch (refreshErr) {
            if (refreshErr instanceof PublicApiError && refreshErr.kind === 'authentication') {
              throw refreshErr;
            }
            if (!alive.current || ownerEpoch.current !== ownerAtStart) return;
            if (refreshErr instanceof PublicApiError && refreshErr.code) {
              opErrCode = refreshErr.code;
              opErrDetail = refreshErr.detail || opErrDetail;
            }
          }
        }
      }
      mutationRev.current += 1;
      setKeys((previous) => [...(previous || []).filter((item) => item.id !== current.id), current]);
      setCreatedKeyId(current.id);
      const retired = current.lifecycle === 'superseded' || current.runtimeState === 'revoked_observed';
      if (current.runtimeState === 'effective_observed' || abortedSnapshot || retired) {
        pendingReapplyOpId.current = '';
      }
      if (current.runtimeState === 'effective_observed') {
        setRecoveryKey(null);
        setConfirmed(true);
        setConfirmedEffect(true);
        if (rebindAtStart === rebindSeq.current) setBlocker(null);
        setCreateName('');
        creationPendingRef.current = false;
        reportPending();
      } else {
        let resolvedCode = retired ? '' : current.effectCode || opErrCode || '';
        if (
          resolvedCode === 'key_operation_unknown' &&
          (abortedSnapshot || nextOp?.outcome === 'aborted') &&
          current.runtimeState === 'not_applied'
        ) {
          resolvedCode = '';
        }
        const resolvedDetail =
          !retired && resolvedCode === 'external_management_key_rejected'
            ? opErrDetail ||
              (recoveryKey.code === 'external_management_key_rejected'
                ? recoveryKey.detail || ''
                : '')
            : '';
        setRecoveryKey({
          key: current,
          code: resolvedCode,
          detail: resolvedDetail,
          operation: nextOp,
          statusUnknown: !retired && current.runtimeState === 'effect_unknown',
        });
        if (rebindAtStart === rebindSeq.current && !retired) {
          const typed = blockerOf(resolvedCode);
          if (typed) {
            setBlocker(
              typed.kind === 'key_rejected' && resolvedDetail
                ? { ...typed, detail: resolvedDetail }
                : typed
            );
          }
        }
        if (retired) {
          creationPendingRef.current = false;
          reportPending();
        }
      }
    } catch (err) {
      if (!alive.current || ownerEpoch.current !== ownerAtStart) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        pendingRefresh.current = false;
        onUnauthorized();
        return;
      }
      if (rebindAtStart === rebindSeq.current) failure(err);
    } finally {
      if (ownerEpoch.current === ownerAtStart) finishMutation();
      else activeMutation.current = false;
    }
  }


  function openRevocationFor(keyId: string, desiredMode: 'revoke' | 'replace') {
    if (
      activeReadEpoch.current !== null ||
      activeMutation.current ||
      detailPendingRef.current ||
      creationPendingRef.current ||
      operationPendingRef.current ||
      regeneratePendingRef.current
    )
      return;
    setSelected('');
    revocationOpenRef.current = true;
    setRevocationOpen(true);
    setRevocationPreset((previous) => ({
      targetId: keyId,
      mode: desiredMode,
      seq: (previous?.seq || 0) + 1,
    }));
  }

  function closeRevocationModal() {
    revocationOpenRef.current = false;
    setRevocationOpen(false);
    setRevocationPreset(null);
  }

  function openRegenerateFor(keyId: string) {
    if (
      activeReadEpoch.current !== null ||
      activeMutation.current ||
      detailPendingRef.current ||
      creationPendingRef.current ||
      operationPendingRef.current ||
      regeneratePendingRef.current
    )
      return;
    setSelected('');
    revocationOpenRef.current = false;
    setRevocationOpen(false);
    setRevocationPreset(null);
    regenerateChangedRef.current = false;
    regenerateOpenRef.current = true;
    setRegenerateTargetId(keyId);
  }

  async function reconcile() {
    if (!pending || activeReadEpoch.current !== null || activeMutation.current) return;
    activeMutation.current = true;
    const rebindAtStart = rebindSeq.current;
    const ownerAtStart = ownerEpoch.current;
    syncBusy();
    setError('');
    try {
      const key = await client.get<Key>('api-keys/requests/' + pending, auth);
      if (!alive.current || ownerEpoch.current !== ownerAtStart) return;
      if (!validKey(key) || key.requestId !== pending) throw new Error();
      mutationRev.current += 1;
      setKeys((previous) => [...(previous || []).filter((item) => item.id !== key.id), key]);
      setCreatedKeyId(key.id);
      const isPreStrategy = gatewayPending && !revocationAvailable && key.runtimeState === 'not_applied';
      if (key.runtimeState === 'effective_observed' || isPreStrategy) {
        setConfirmed(true);
        setConfirmedEffect(key.runtimeState === 'effective_observed');
        setPending('');
        setRecoveryKey(null);
        setCreateName('');
        creationPendingRef.current = false;
      } else {
        setConfirmed(false);
        setConfirmedEffect(false);
        setPending('');
        setRecoveryKey({
          key,
          code: key.effectCode || '',
          operation: null,
          statusUnknown: key.runtimeState === 'effect_unknown',
        });
        creationPendingRef.current = true;
      }
      reportPending();
      setRetryAllowed(false);
    } catch (err) {
      if (alive.current && ownerEpoch.current === ownerAtStart) {
        if (err instanceof PublicApiError && err.status === 404) setRetryAllowed(true);
        else {
          if (err instanceof PublicApiError && err.kind === 'authentication') {
            pendingRefresh.current = false;
            onUnauthorized();
          } else if (rebindAtStart === rebindSeq.current) {
            failure(err);
          }
        }
      }
    } finally {
      if (ownerEpoch.current === ownerAtStart) finishMutation();
      else activeMutation.current = false;
    }
  }

  const usageById = new Map(usage?.items.map((item) => [item.keyId, item] as const) ?? []);
  const query = search.trim().toLocaleLowerCase();
  const filteredKeys = (keys || []).filter(
    (key) =>
      (filter === 'all' || (filter === 'replaced') === (key.lifecycle === 'superseded')) &&
      (filter === 'replaced' ||
        runtimeFilter === 'all' ||
        (runtimeFilter === 'effective'
          ? key.runtimeState === 'effective_observed'
          : key.runtimeState !== 'effective_observed' && key.lifecycle !== 'superseded')) &&
      (!query ||
        [
          keyDisplayName(key, k('installerOrigin'), k('adoptedOrigin'), t('keysUnnamed')),
          key.maskedPreview,
          key.id,
        ].some((value) => value.toLocaleLowerCase().includes(query)))
  );
  const visibleKeys =
    sortBy.key === 'created' && sortBy.direction === 'desc'
      ? filteredKeys
      : filteredKeys
          .map((item, index) => ({ item, index }))
          .sort((left, right) => {
            const a = left.item;
            const b = right.item;
            const usageA = usageById.get(a.id);
            const usageB = usageById.get(b.id);
            if (sortBy.key === 'calls24h') {
              const callsA =
                usageA &&
                (usageA.coverage.state === 'complete' || usageA.coverage.state === 'partial')
                  ? usageA.calls
                  : null;
              const callsB =
                usageB &&
                (usageB.coverage.state === 'complete' || usageB.coverage.state === 'partial')
                  ? usageB.calls
                  : null;
              if (callsA === null && callsB !== null) return 1;
              if (callsB === null && callsA !== null) return -1;
              if (callsA !== null && callsB !== null && callsA !== callsB) {
                return sortBy.direction === 'asc' ? callsA - callsB : callsB - callsA;
              }
            } else if (sortBy.key === 'recent') {
              const usedA = usageA?.lastUsedAtMs ?? null;
              const usedB = usageB?.lastUsedAtMs ?? null;
              if (usedA === null && usedB !== null) return 1;
              if (usedB === null && usedA !== null) return -1;
              if (usedA !== null && usedB !== null && usedA !== usedB) {
                return sortBy.direction === 'asc' ? usedA - usedB : usedB - usedA;
              }
            } else if (sortBy.key === 'name') {
              const nameA = keyDisplayName(
                a,
                k('installerOrigin'),
                k('adoptedOrigin'),
                t('keysUnnamed')
              );
              const nameB = keyDisplayName(
                b,
                k('installerOrigin'),
                k('adoptedOrigin'),
                t('keysUnnamed')
              );
              const nameCmp = nameA.localeCompare(nameB, undefined, {
                sensitivity: 'base',
                numeric: true,
              });
              if (nameCmp !== 0) {
                return sortBy.direction === 'asc' ? nameCmp : -nameCmp;
              }
            } else if (sortBy.key === 'created') {
              const createdA = a.createdAtMs || 0;
              const createdB = b.createdAtMs || 0;
              if (createdA !== createdB) {
                return sortBy.direction === 'asc' ? createdA - createdB : createdB - createdA;
              }
              return sortBy.direction === 'asc'
                ? right.index - left.index
                : left.index - right.index;
            }
            return (b.createdAtMs || 0) - (a.createdAtMs || 0);
          })
          .map((entry) => entry.item);
  const keySortOptions: Array<{ value: KeySortKey; label: string }> = [
    { value: 'created', label: k('sortFieldCreated') },
    { value: 'recent', label: k('sortFieldRecent') },
    { value: 'calls24h', label: k('sortFieldCalls24h') },
    { value: 'name', label: k('sortFieldName') },
  ];
  const selectedSortLabel =
    keySortOptions.find((option) => option.value === sortBy.key)?.label ?? k('sortFieldCreated');
  const sortDirectionLabel =
    sortBy.direction === 'asc' ? k('sortAscending') : k('sortDescending');
  const activeKeys = (keys || []).filter((key) => key.lifecycle !== 'superseded');
  const savedCount = activeKeys.length;
  const replacedCount = (keys || []).length - savedCount;
  const effectiveActiveKeys = activeKeys.filter(
    (key) => key.runtimeState === 'effective_observed'
  );
  const effectiveCount = effectiveActiveKeys.length;
  const gatewayVerified = keys !== null && !gatewayPending && !presenceUnverified;
  const runtimeAccent = !gatewayVerified || effectiveCount < savedCount ? 'amber' : 'green';
  const runtimeValue = gatewayVerified ? String(effectiveCount) : '—';
  const runtimeMeta = !gatewayVerified
    ? k('summaryGatewaySetupHelp')
    : savedCount === 0
      ? k('summaryGatewayIdleHelp')
      : effectiveCount === savedCount
        ? k('summaryGatewayConsistent')
        : `${effectiveCount}/${savedCount} ${k('summaryRuntimeVerifiedCount')}`;
  const hasCustomSort = !(sortBy.key === 'created' && sortBy.direction === 'desc');
  const hasSearchOrNarrowFilter =
    search.trim() !== '' ||
    (filter !== 'replaced' && runtimeFilter !== 'all') ||
    hasCustomSort;
  const hasActiveFilter =
    search.trim() !== '' ||
    filter !== 'saved' ||
    runtimeFilter !== 'all' ||
    hasCustomSort;
  const usageCalls =
    usage?.items
      .filter((item) => item.coverage.state === 'complete' || item.coverage.state === 'partial')
      .reduce((total, item) => total + item.calls, 0) ?? null;
  const usageUnavailable =
    usage === null ||
    usage.coverage.reason === 'usage_read_failed' ||
    usage.coverage.state === 'unavailable';
  const usageTotalText = usageUnavailable
    ? '—'
    : usageCountText(usageCalls, usage.coverage.state);
  const usageShowCoveragePill =
    !usageUnavailable && !!usage && usage.coverage.state !== 'complete';
  const totalPages = Math.max(1, Math.ceil(visibleKeys.length / pageSize));
  const currentPage = Math.min(Math.max(1, page), totalPages);
  const startIndex = (currentPage - 1) * pageSize;
  const pagedKeys = visibleKeys.slice(startIndex, startIndex + pageSize);
  const startItem = visibleKeys.length === 0 ? 0 : startIndex + 1;
  const endItem = Math.min(startIndex + pageSize, visibleKeys.length);
  const locked =
    busy ||
    createOpen ||
    !!selected ||
    !!renameTarget ||
    !!regenerateTargetId ||
    detailPending ||
    !!pending ||
    !!secret ||
    !!recoveryKey;
  // One cell = the provable count, its coverage and when the key was last used.
  // Unknown coverage prints an em dash instead of a zero.
  const usageCell = (id: string) => {
    const item = usageById.get(id);
    const state = item?.coverage.state ?? 'unknown';
    return (
      <button
        type="button"
        className="key-usage-cell key-usage-trigger"
        data-testid={'open-key-usage-' + id}
        disabled={locked}
        title={usageReasonText(item?.coverage.reason ?? 'usage_read_failed')}
        onClick={() => openKeyDetails(id, { tab: 'usage' })}
      >
        <div className="key-usage-top">
          <strong>{usageCountText(item?.calls, state)}</strong>
          {state !== 'complete' && (
            <span className={'key-state-pill ' + usageCoveragePill(state)}>
              {usageStateText(state)}
            </span>
          )}
        </div>
        <small>
          {k('usageLastUsed')}: {usageTimeText(item?.lastUsedAtMs) ?? '—'}
        </small>
      </button>
    );
  };
  const createdKeyRecord = createdKeyId
    ? (keys || []).find((item) => item.id === createdKeyId) || null
    : null;
  const confirmedRetired =
    !!createdKeyRecord &&
    (createdKeyRecord.lifecycle === 'superseded' ||
      createdKeyRecord.runtimeState === 'revoked_observed');
  const recoveryRetired =
    !!recoveryKey &&
    (recoveryKey.key.lifecycle === 'superseded' ||
      recoveryKey.key.runtimeState === 'revoked_observed');
  const recoveryBlockerCode = recoveryKey ? recoveryKey.code || recoveryKey.key.effectCode || '' : '';
  const recoveryBlocker = blockerOf(recoveryBlockerCode);
  const canDismissRecovery =
    !!recoveryKey &&
    !busy &&
    !persistedCopying &&
    !pending &&
    !secret &&
    (recoveryRetired ||
      (!recoveryKey.statusUnknown && recoveryKey.key.runtimeState === 'not_applied') ||
      isExternalOrSourceBlocker(recoveryBlockerCode));
  return (
    <section className="key-workspace" aria-labelledby="client-key-title" aria-busy={busy}>
      <h1 id="client-key-title" className="sr-only">
        {t('keysTitle')}
      </h1>
      <p className="sr-only">{pageDescription || k('description')}</p>
      <PageControlBar
        testId="key-controls-bar"
        toolbarTestId="key-filter-toolbar"
        leading={
          <SegmentedTabs<'keys' | 'access'>
            ariaLabel={k('workspaceTabsLabel')}
            idBase="key-workspace-tab-btn"
            className="key-workspace-tabs"
            responsiveFullWidth={false}
            activeTab={workspaceTab}
            onChange={(nextTab) => {
              setWorkspaceTab(nextTab);
            }}
            items={[
              {
                id: 'keys',
                testId: 'key-workspace-tab-keys',
                label: (
                  <span className="key-tab-label">
                    <span>{k('workspaceTabKeys')}</span>
                    <span className="key-tab-badge">{keys === null ? '—' : savedCount}</span>
                  </span>
                ),
              },
              {
                id: 'access',
                testId: 'key-workspace-tab-access',
                label: (
                  <span className="key-tab-label">
                    <span>{k('workspaceTabAccess')}</span>
                    <span
                      className={
                        'key-tab-dot ' + (accessInfo?.ready ? 'is-ready' : 'is-pending')
                      }
                      aria-hidden="true"
                    />
                  </span>
                ),
              },
            ]}
          />
        }
        actions={
          <>
            <Button
              type="button"
              variant="secondary"
              size="sm"
              aria-label={t('refresh')}
              disabled={busy || !!selected || detailPending}
              onClick={() => {
                setAccessReloadToken((token) => token + 1);
                void refresh();
              }}
            >
              <FullWebIcon name="refresh" size={14} />
              {t('refresh')}
            </Button>
            {workspaceTab === 'keys' && (
              <Button
                type="button"
                variant="primary"
                size="sm"
                data-testid="key-create-open"
                aria-label={t('keysCreate')}
                onClick={() => {
                  setConfirmed(false);
                  setConfirmedEffect(false);
                  setCreatedKeyId('');
                  setRecoveryKey(null);
                  setCreateOpen(true);
                }}
                disabled={
                  locked ||
                  operationPending ||
                  keys === null ||
                  !!error ||
                  !validKeyNameInput(createName.trim())
                }
              >
                <FullWebIcon name="plus" size={14} />
                {t('keysCreate')}
              </Button>
            )}
          </>
        }
        toolbar={
          workspaceTab === 'keys' && keys && keys.length > 0 ? (
            <>
              <div className="key-filter-tabs-row" data-testid="key-lifecycle-tabs">
                <SegmentedTabs<'saved' | 'all' | 'replaced'>
                  ariaLabel={k('lifecycle')}
                  idBase="key-lifecycle-tab-btn"
                  className="key-lifecycle-tabs"
                  variant="joined"
                  responsiveFullWidth={false}
                  activeTab={filter}
                  onChange={(value) => {
                    setFilter(value);
                    setPage(1);
                  }}
                  items={(['saved', 'all', 'replaced'] as const).map((value) => {
                    const count =
                      keys === null
                        ? '—'
                        : value === 'all'
                          ? keys.length
                          : value === 'saved'
                            ? savedCount
                            : replacedCount;
                    const tabLabel = k(
                      value === 'saved' ? 'tabActive' : value === 'all' ? 'tabAll' : 'tabRevoked'
                    );
                    return {
                      id: value,
                      testId: `key-filter-${value}`,
                      label: (
                        <span className="key-tab-label">
                          <span>{tabLabel}</span>
                          <span className="key-tab-badge">{count}</span>
                        </span>
                      ),
                    };
                  })}
                />
              </div>
              <div className="key-filter-toolbar">
                <div className="key-search-field">
                  <Input
                    type="search"
                    className="key-search-input"
                    data-testid="key-search-input"
                    aria-label={k('search')}
                    placeholder={k('search')}
                    value={search}
                    onChange={(event) => {
                      setSearch(event.target.value);
                      setPage(1);
                    }}
                    rightElement={<FullWebIcon name="search" size={16} />}
                  />
                </div>
                <div className="key-filter-toolbar-selects">
                  <SortControl<KeySortKey>
                    options={keySortOptions}
                    value={sortBy.key}
                    direction={sortBy.direction}
                    onChangeValue={(nextKey) => {
                      setSortBy((prev) => ({
                        key: nextKey,
                        direction:
                          prev.key === nextKey ? prev.direction : KEY_SORT_DEFAULT_DIRECTIONS[nextKey],
                      }));
                      setPage(1);
                    }}
                    onToggleDirection={() => {
                      setSortBy((prev) => ({
                        key: prev.key,
                        direction: prev.direction === 'asc' ? 'desc' : 'asc',
                      }));
                      setPage(1);
                    }}
                    ariaLabel={`${k('sortLabel')}: ${selectedSortLabel}`}
                    directionLabel={sortDirectionLabel}
                    dropdownAlign="right"
                  />
                  {filter !== 'replaced' &&
                    (runtimeFilter !== 'all' || effectiveCount < savedCount) && (
                      <Select
                        value={runtimeFilter}
                        onChange={(next) => {
                          setRuntimeFilter(next as 'all' | 'effective' | 'attention');
                          setPage(1);
                        }}
                        options={[
                          { value: 'all', label: k('runtimeFilterAll') },
                          { value: 'effective', label: k('runtimeFilterEffective') },
                          { value: 'attention', label: k('runtimeFilterAttention') },
                        ]}
                        ariaLabel={k('runtimeFilter')}
                        fullWidth={false}
                        className="key-filter-select"
                        triggerClassName="key-filter-select-trigger"
                      />
                    )}
                  {hasActiveFilter && (
                    <Button
                      type="button"
                      variant="secondary"
                      size="sm"
                      className="key-clear-filter-btn"
                      onClick={() => {
                        setSearch('');
                        setFilter('saved');
                        setRuntimeFilter('all');
                        setSortBy({ key: 'created', direction: 'desc' });
                        setPage(1);
                      }}
                    >
                      {k('clear')}
                    </Button>
                  )}
                </div>
              </div>
            </>
          ) : null
        }
      />
      {blocker && (
        <FreshExternalBlocker
          state={blocker}
          onRebind={onRebind}
          onReload={() => void refresh(true)}
          onDismiss={() => setBlocker(null)}
        />
      )}
      {error && !createOpen && !pending && !recoveryKey && (
        <p role="alert" className="key-notice key-notice-warning">
          <FullWebIcon name="alert" />
          <span>{error}</span>
        </p>
      )}
      {(() => {
        const showPendingOperationBanner =
          operationPending && !revocationOpen && !regenerateTargetId;
        const pendingOp =
          latestOperationResult?.operation &&
          !isTerminalKeyOperationOutcome(latestOperationResult.operation.outcome)
            ? latestOperationResult.operation
            : null;
        const opLabel = pendingOp
          ? k(
              pendingOp.mode === 'add'
                ? 'pendingBannerOpAdd'
                : pendingOp.mode === 'rotate'
                  ? 'pendingBannerOpRotate'
                  : pendingOp.mode === 'replace'
                    ? 'pendingBannerOpReplace'
                    : 'pendingBannerOpRevoke'
            )
          : t('keysOperationPending');
        const affectedKey = pendingOp
          ? (keys || []).find((item) => item.id === pendingOp.targetId) || null
          : null;
        const affectedLabel = affectedKey
          ? `${keyDisplayName(affectedKey, k('installerOrigin'), k('adoptedOrigin'), t('keysUnnamed'))} (${affectedKey.maskedPreview})`
          : pendingOp?.targetId || '';
        return (
          <>
            {presenceUnverified && !operationPending && (
              <p
                role="status"
                className="key-notice key-notice-warning"
                data-testid="keys-presence-unverified"
              >
                <FullWebIcon name="alert" />
                <span>
                  <strong>{k('presenceUnverifiedTitle')}</strong> — {k('presenceUnverifiedHelp')}
                </span>
              </p>
            )}
            {showPendingOperationBanner && (
              <div
                className="key-notice key-notice-warning key-pending-banner"
                role="status"
                data-testid="pending-operation-banner"
              >
                <FullWebIcon name="alert" size={16} />
                <div className="key-pending-banner-body">
                  <div className="key-pending-banner-primary">
                    <strong>
                      {pendingOp
                        ? `${k('pendingBannerPrefix')} ${opLabel}`
                        : t('keysOperationPending')}
                    </strong>
                    {affectedLabel && (
                      <span>
                        <code>{affectedLabel}</code>
                      </span>
                    )}
                  </div>
                  {presenceUnverified && (
                    <p
                      className="key-pending-banner-subtext"
                      data-testid="keys-presence-unverified"
                    >
                      {k('presenceUnverifiedTitle')} — {k('presenceUnverifiedHelp')}
                    </p>
                  )}
                </div>
                <button
                  type="button"
                  className="btn btn-primary btn-sm"
                  data-testid="pending-operation-continue"
                  onClick={() => {
                    setRevocationPreset(null);
                    revocationOpenRef.current = true;
                    setRevocationOpen(true);
                  }}
                >
                  {k('pendingBannerContinue')}
                </button>
              </div>
            )}
          </>
        );
      })()}
      <Modal
        open={visible && (createOpen || !!pending || !!secret || !!recoveryKey)}
        title={
          secret
            ? k('secretTitle')
            : confirmed
              ? confirmedRetired
                ? t('keysRevokedHistory')
                : confirmedEffect
                  ? k('createdActiveTitle')
                  : k('secretTitle')
              : recoveryKey
                ? recoveryRetired
                  ? t('keysRevokedHistory')
                  : recoveryKey.statusUnknown || recoveryKey.key.runtimeState === 'effect_unknown'
                    ? k('createApplyUnknownTitle')
                    : k('createApplyPendingTitle')
                : t('keysCreate')
        }
        className="key-create-modal"
        width={540}
        closeDisabled={
          busy ||
          persistedCopying ||
          !!pending ||
          (!!recoveryKey && !canDismissRecovery)
        }
        onClose={closeCreateResultModal}
        footer={
          !confirmed && !recoveryKey && !secret && !pending ? (
            <div className="key-modal-footer">
              <button
                className="btn btn-secondary"
                type="button"
                disabled={busy || persistedCopying}
                onClick={closeCreateResultModal}
              >
                {k('cancel')}
              </button>
              <button
                className="btn btn-primary"
                type="submit"
                form="create-key-form"
                data-testid="confirm-create-key"
                disabled={
                  busy ||
                  operationPending ||
                  keys === null ||
                  !validKeyNameInput(createName.trim())
                }
              >
                {t('keysCreate')}
              </button>
            </div>
          ) : secret ? (
            <div className="key-modal-footer">
              <button
                type="button"
                className="btn btn-primary"
                onClick={dismissCreatedSecret}
              >
                {t('keysDismiss')}
              </button>
            </div>
          ) : confirmed && !recoveryKey ? (
            <div className="key-modal-footer">
              {createdKeyRecord && createdKeyRecord.lifecycle !== 'superseded' && (
                <button
                  type="button"
                  className="btn btn-secondary"
                  data-testid="copy-confirmed-client-secret"
                  disabled={busy || persistedCopying}
                  onClick={() => void copyPersistedKeySecret(createdKeyRecord)}
                >
                  <FullWebIcon name="copy" />
                  {k('copyPersistedSecret')}
                </button>
              )}
              <button
                type="button"
                className="btn btn-primary"
                data-testid="close-confirmed-create"
                disabled={busy || persistedCopying}
                onClick={closeCreateResultModal}
              >
                {t('keysDismiss')}
              </button>
            </div>
          ) : undefined
        }
      >
        {error && (
          <p className="key-notice key-notice-warning" role="alert">
            {error}
          </p>
        )}
        {!confirmed && !recoveryKey && !secret && (
          <form
            id="create-key-form"
            hidden={!!pending}
            onSubmit={(event) => {
              event.preventDefault();
              void createKey();
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
            <p className="key-field-help">{k('generatedOnServer')}</p>
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
        {recoveryKey && (
          <div
            role="alert"
            className="key-creation-recovery key-notice-warning"
            data-testid="create-apply-recovery"
          >
            <FullWebIcon name="alert" />
            <div>
              <h3>
                {recoveryRetired
                  ? t('keysRevokedHistory')
                  : recoveryKey.statusUnknown || recoveryKey.key.runtimeState === 'effect_unknown'
                    ? k('createApplyUnknownTitle')
                    : k('createApplyPendingTitle')}
              </h3>
              <p>
                {recoveryRetired
                  ? k('detailRevokedHelp')
                  : recoveryKey.statusUnknown || recoveryKey.key.runtimeState === 'effect_unknown'
                    ? k('createApplyUnknownHelp')
                    : k('createApplyPendingHelp')}
              </p>
              {!recoveryRetired && recoveryKey.operation?.outcome === 'aborted' && recoveryBlockerCode && (
                <p data-testid="create-apply-aborted">{k('operationAborted')}</p>
              )}
              {!recoveryRetired &&
                (recoveryBlockerCode ? (
                  <p data-testid="create-apply-blocker-reason">
                    {k(effectBlockerTextKey(recoveryBlockerCode))}
                  </p>
                ) : recoveryKey.operation?.outcome === 'aborted' ? (
                  <p data-testid="create-apply-blocker-reason">{k('operationAborted')}</p>
                ) : null)}
              {!recoveryRetired &&
                recoveryKey.detail &&
                recoveryBlocker?.kind === 'key_rejected' && (
                  <details
                    className="fresh-external-blocker-detail"
                    data-testid="create-apply-blocker-detail"
                  >
                    <summary>{x('details')}</summary>
                    <p>{recoveryKey.detail}</p>
                  </details>
                )}
              <p>
                <code>{recoveryKey.key.maskedPreview}</code>
              </p>
              <div className="key-toolbar-actions">
                {!recoveryRetired &&
                  (recoveryKey.statusUnknown || recoveryKey.key.runtimeState === 'effect_unknown' ? (
                    <button
                      type="button"
                      className="btn btn-primary"
                      data-testid="create-reconcile-apply"
                      disabled={busy || persistedCopying}
                      onClick={() => void reconcileRecoveryKey()}
                    >
                      {k('checkOperationResult')}
                    </button>
                  ) : !['key_rejected', 'target_moved', 'version_unsupported'].includes(
                      recoveryBlocker?.kind ?? ''
                    ) ? (
                    <button
                      type="button"
                      className="btn btn-primary"
                      data-testid="create-retry-apply"
                      disabled={busy || persistedCopying}
                      onClick={() => void retryApplyRecoveryKey()}
                    >
                      {k('retryApplyKey')}
                    </button>
                  ) : null)}
                {!recoveryRetired &&
                  onRebind &&
                  (recoveryBlocker?.kind === 'key_rejected' ||
                    recoveryBlocker?.kind === 'target_moved') && (
                    <button
                      type="button"
                      className="btn btn-primary"
                      data-testid="recovery-rebind-action"
                      disabled={busy || persistedCopying}
                      onClick={() => dismissRecoveryKey(true)}
                    >
                      {k('rebindConnection')}
                    </button>
                  )}
                {recoveryKey.key.lifecycle !== 'superseded' && (
                  <button
                    type="button"
                    className="btn btn-secondary"
                    data-testid="copy-recovered-client-secret"
                    disabled={busy || persistedCopying}
                    onClick={() => void copyPersistedKeySecret(recoveryKey.key)}
                  >
                    <FullWebIcon name="copy" />
                    {k('copyPersistedSecret')}
                  </button>
                )}
                {canDismissRecovery && (
                  <button
                    type="button"
                    className="btn btn-secondary"
                    data-testid="dismiss-create-recovery"
                    disabled={busy || persistedCopying}
                    onClick={() => dismissRecoveryKey(false)}
                  >
                    {recoveryRetired ? k('closeDialog') : k('manageLater')}
                  </button>
                )}
              </div>
              {canDismissRecovery && !recoveryRetired && (
                <p className="key-field-help">{k('recoveryManageLaterHelp')}</p>
              )}
              {persistedCopyFeedback && (
                <p role="status" data-testid="copy-recovered-client-secret-feedback">
                  {k(persistedCopyFeedback === 'copied' ? 'keyCopied' : 'keyCopyFailed')}
                </p>
              )}
            </div>
          </div>
        )}
        {confirmed && !secret && !recoveryKey && (
          <div className="key-create-result-stack" role="status">
            <div className={`key-operation-banner ${confirmedRetired ? 'is-aborted' : 'is-committed'}`}>
              <span className="key-operation-banner-icon">
                <FullWebIcon name={confirmedRetired ? 'alert' : 'check'} size={18} />
              </span>
              <div className="key-operation-banner-body">
                <strong>
                  {confirmedRetired
                    ? t('keysRevokedHistory')
                    : confirmedEffect
                      ? k('createdActiveTitle')
                      : t('keysConfirmed')}
                </strong>
                <p>
                  {confirmedRetired
                    ? k('detailRevokedHelp')
                    : confirmedEffect
                      ? k('createdActiveConfirmed')
                      : t('keysConfirmed')}
                </p>
              </div>
            </div>
            {persistedCopyFeedback && (
              <p
                role="status"
                className={`key-copy-inline-status ${persistedCopyFeedback === 'copied' ? 'is-copied' : 'is-failed'}`}
                data-testid="copy-confirmed-client-secret-feedback"
              >
                {k(persistedCopyFeedback === 'copied' ? 'keyCopied' : 'keyCopyFailed')}
              </p>
            )}
            <div className="key-create-access-panel">
              <AccessInfoSummary
                client={client}
                auth={auth}
                onUnauthorized={onUnauthorized}
                info={accessInfo ?? undefined}
                reloadToken={reloadToken}
                compact
                clientOnly
                defaultProtocolsCollapsed
                testIdPrefix="create-access-info"
              />
            </div>
          </div>
        )}
        {secret && (
          <div className="key-create-result-stack" role="status">
            <div className={`key-operation-banner ${confirmedEffect ? 'is-committed' : 'is-pending'}`}>
              <span className="key-operation-banner-icon">
                <FullWebIcon name={confirmedEffect ? 'check' : 'key'} size={18} />
              </span>
              <div className="key-operation-banner-body">
                <strong>{confirmedEffect ? k('createdActiveTitle') : t('keysCreated')}</strong>
                <p>{confirmedEffect ? k('createdActiveHelp') : k('secretHelp')}</p>
              </div>
            </div>
            <div className="key-secret-card">
              <div className="key-secret-value">
                <code data-testid="created-client-secret">{secret}</code>
              </div>
              <div className="key-secret-card-actions">
                <button
                  type="button"
                  className="btn btn-primary btn-sm"
                  aria-label={t('keysCopy')}
                  data-testid="copy-created-client-secret"
                  onClick={() => void copyCreatedSecret()}
                >
                  <FullWebIcon name="copy" size={14} />
                  {t('keysCopy')}
                </button>
                {copyFeedback && (
                  <span
                    role="status"
                    className={`key-copy-inline-status ${copyFeedback === 'copied' ? 'is-copied' : 'is-failed'}`}
                    data-testid="copy-created-client-secret-feedback"
                  >
                    {t(copyFeedback === 'copied' ? 'keysCopied' : 'keysCopyFailed')}
                  </span>
                )}
              </div>
            </div>
            {confirmed && !recoveryKey && (
              <div className="key-create-access-panel">
                <AccessInfoSummary
                  client={client}
                  auth={auth}
                  onUnauthorized={onUnauthorized}
                  info={accessInfo ?? undefined}
                  reloadToken={reloadToken}
                  compact
                  clientOnly
                  defaultProtocolsCollapsed
                  testIdPrefix="create-access-info"
                />
              </div>
            )}
          </div>
        )}
      </Modal>
      <div
        id="key-workspace-panel-keys"
        role="tabpanel"
        aria-labelledby="key-workspace-tab-btn-keys"
        className="key-list-workspace"
        hidden={workspaceTab !== 'keys'}
      >
        <div className="key-summary-grid" data-testid="key-summary-grid">
          {(
            [
              {
                id: 'saved',
                accent: 'blue',
                icon: 'key' as const,
                label: k('summarySaved'),
                value: keys === null ? '—' : String(savedCount),
                meta: k('summarySavedHelp'),
              },
              {
                id: 'runtime',
                accent: runtimeAccent,
                icon: (!gatewayVerified || effectiveCount < savedCount ? 'alert' : 'shield') as
                  | 'alert'
                  | 'shield',
                label: k('summarySyncedActive'),
                value: runtimeValue,
                meta: runtimeMeta,
              },
              {
                id: 'usage',
                accent: 'indigo',
                icon: 'chart' as const,
                label: k('usageSummaryTitle'),
                value: usageTotalText,
                meta: usageUnavailable ? (
                  <>
                    <span>{k('summaryUsageValue')}</span> · {k('summaryUsageHelp')}
                  </>
                ) : usageShowCoveragePill && usage ? (
                  <>
                    <span>{usageStateText(usage.coverage.state)}</span> · {k('summaryUsageHelp')}
                  </>
                ) : (
                  k('summaryUsageHelp')
                ),
              },
              {
                id: 'replaced',
                accent: 'neutral',
                icon: 'ban' as const,
                label: k('summaryReplaced'),
                value: keys === null ? '—' : String(replacedCount),
                meta: k('summaryReplacedHelp'),
              },
            ] as const
          ).map((card) => (
            <div
              className="card key-summary-card"
              data-accent={card.accent}
              key={card.id}
              data-testid={'key-summary-' + card.id}
            >
              <div className="key-summary-head">
                <span className="key-summary-icon" aria-hidden="true">
                  <FullWebIcon name={card.icon} size={15} />
                </span>
                <span className="key-summary-label">{card.label}</span>
              </div>
              <div className="key-summary-body">
                <strong className="key-summary-value">
                  {card.id === 'usage' ? <span>{card.value}</span> : card.value}
                </strong>
                <small className="key-summary-meta">{card.meta}</small>
              </div>
              <div className="key-summary-card-chart" aria-hidden="true">
                <svg viewBox="0 0 100 30" preserveAspectRatio="none">
                  <path d="M0,25 Q15,5 30,20 T60,10 T100,25" />
                </svg>
              </div>
            </div>
          ))}
        </div>
        <div
          id="key-lifecycle-tabpanel"
          role="tabpanel"
          aria-labelledby={`key-lifecycle-tab-btn-${filter}`}
          className="card key-table-panel"
          data-testid="key-list-panel"
        >
          {keys && keys.length > 0 && (
            <div className="key-table-topbar" data-testid="key-table-topbar">
              <div className="key-table-topbar-summary">
                {formatKeyTableCount(visibleKeys.length)}
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
                    className={`key-view-btn ${viewMode === mode ? 'is-active' : ''}`}
                    data-testid={'key-view-' + mode}
                    aria-pressed={viewMode === mode}
                    onClick={() => setViewMode(mode)}
                  >
                    {k(mode === 'table' ? 'viewTable' : 'viewCards')}
                  </button>
                ))}
              </div>
            </div>
          )}
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
              <button
                type="button"
                className="btn btn-primary btn-sm"
                data-testid="empty-create-key"
                disabled={locked || operationPending || !!error || !validKeyNameInput(createName.trim())}
                onClick={() => {
                  setConfirmed(false);
                  setConfirmedEffect(false);
                  setCreatedKeyId('');
                  setRecoveryKey(null);
                  setCreateOpen(true);
                }}
              >
                {t('keysCreate')}
              </button>
            </div>
          ) : visibleKeys.length === 0 ? (
            !hasSearchOrNarrowFilter && (filter === 'saved' || filter === 'replaced') ? (
              <div className="key-empty" data-testid={`key-empty-tab-${filter}`}>
                <FullWebIcon name={filter === 'saved' ? 'key' : 'clock'} size={26} />
                <h3>{k(filter === 'saved' ? 'emptyActiveTab' : 'emptyRevokedTab')}</h3>
              </div>
            ) : (
              <div className="key-empty">
                <FullWebIcon name="search" size={28} />
                <h3>{k('noFilteredKeys')}</h3>
                <p>{k('noFilteredKeysHelp')}</p>
                <button
                  type="button"
                  className="btn btn-secondary"
                  data-testid="reset-key-filters"
                  onClick={() => {
                    setSearch('');
                    setFilter(savedCount > 0 ? 'saved' : 'all');
                    setRuntimeFilter('all');
                    setSortBy({ key: 'created', direction: 'desc' });
                    setPage(1);
                  }}
                >
                  {k('resetFilters')}
                </button>
              </div>
            )
          ) : (
            <div className="key-table-scroll">
              <table
                className={`key-table ${viewMode === 'cards' ? 'is-cards-view' : ''}`}
                role="table"
              >
                <caption className="sr-only">{k('scope')}</caption>
                <thead role="rowgroup">
                  <tr role="row">
                    {(
                      ['credential', 'originAndCreated', 'lifecycle', 'usageColumn', 'actions'] as const
                    ).map((label) => (
                      <th key={label} scope="col" role="columnheader">
                        {k(label)}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody role="rowgroup">
                  {pagedKeys.map((key) => {
                    const originLabel =
                      key.origin === 'installer'
                        ? k('installerOrigin')
                        : key.origin === 'adopted'
                          ? k('adoptedOrigin')
                          : k('workspaceOrigin');
                    const createdText = usageTimeText(key.createdAtMs) ?? '—';
                    const unified = keyUnifiedStatus(key);
                    const revokedText =
                      key.lifecycle === 'superseded' && key.revokedAtMs
                        ? usageTimeText(key.revokedAtMs)
                        : null;
                    const displayName = keyDisplayName(
                      key,
                      k('installerOrigin'),
                      k('adoptedOrigin'),
                      t('keysUnnamed')
                    );
                    return (
                      <tr
                        key={key.id}
                        role="row"
                        data-testid={'key-row-' + key.id}
                        data-interactive={!locked ? 'true' : undefined}
                        className={selected === key.id ? 'is-selected' : undefined}
                        onClick={(event) => {
                          if (
                            typeof Element !== 'undefined' &&
                            event.target instanceof Element &&
                            event.target.closest('button, a, input, [role="menuitem"]')
                          ) {
                            return;
                          }
                          if (!locked) openKeyDetails(key.id);
                        }}
                      >
                        <td className="key-col-identity" role="cell" data-label={k('name')}>
                          <div className="key-name-cell">
                            <span className="key-resource-icon">
                              <FullWebIcon name="key" size={16} />
                            </span>
                            <div>
                              <button
                                type="button"
                                className="key-name-link"
                                disabled={locked}
                                onClick={() => openKeyDetails(key.id)}
                              >
                                {displayName}
                              </button>
                              <small className="key-secret-inline" title={key.id}>
                                <code>{key.maskedPreview}</code>
                                {key.lifecycle !== 'superseded' && (
                                  <button
                                    type="button"
                                    className="key-inline-copy-btn"
                                    data-testid={'copy-row-secret-' + key.id}
                                    aria-label={k('copyKey') + ': ' + displayName}
                                    disabled={
                                      locked ||
                                      (rowCopyState?.id === key.id &&
                                        rowCopyState.status === 'copying')
                                    }
                                    onClick={(event) => {
                                      event?.stopPropagation?.();
                                      void copyRowKeySecret(key);
                                    }}
                                  >
                                    <FullWebIcon
                                      name={
                                        rowCopyState?.id === key.id &&
                                        rowCopyState.status === 'copied'
                                          ? 'check'
                                          : 'copy'
                                      }
                                      size={13}
                                    />
                                  </button>
                                )}
                                {rowCopyState?.id === key.id &&
                                  rowCopyState.status !== 'copying' && (
                                    <span
                                      role="status"
                                      className={`key-inline-copy-feedback ${rowCopyState.status === 'copied' ? 'is-copied' : 'is-failed'}`}
                                      data-testid={'copy-row-secret-feedback-' + key.id}
                                    >
                                      {k(
                                        rowCopyState.status === 'copied'
                                          ? 'copiedShort'
                                          : 'copyFailedShort'
                                      )}
                                    </span>
                                  )}
                              </small>
                            </div>
                          </div>
                        </td>
                        <td
                          className="key-col-origin"
                          role="cell"
                          data-label={k('originAndCreated')}
                        >
                          <div className="key-origin-cell">
                            <span className="key-origin-label">{originLabel}</span>
                            <small>{createdText}</small>
                          </div>
                        </td>
                        <td className="key-col-state" role="cell" data-label={k('lifecycle')}>
                          <div className="key-state-stack">
                            <span
                              className={'key-state-pill ' + unified.pillClass}
                              data-testid={'key-status-' + key.id}
                            >
                              {k(unified.labelKey)}
                            </span>
                            {revokedText && (
                              <small
                                className="key-state-subtext"
                                data-testid={'key-revoked-at-' + key.id}
                              >
                                {k('revokedAtPrefix')} {revokedText}
                              </small>
                            )}
                          </div>
                        </td>
                        <td
                          className="key-col-usage"
                          role="cell"
                          data-label={k('usageColumn')}
                          data-testid={'key-usage-' + key.id}
                        >
                          {usageCell(key.id)}
                        </td>
                        <td
                          className="key-col-actions"
                          role="cell"
                          data-label={k('actions')}
                          onClick={(event) => event?.stopPropagation?.()}
                        >
                          <div className="key-row-actions">
                            {key.lifecycle !== 'superseded' && (
                              <>
                                <div className="key-quick-actions">
                                  <Button
                                    type="button"
                                    variant="secondary"
                                    size="xs"
                                    iconOnly
                                    className="key-quick-btn key-quick-btn-rename"
                                    data-testid={'row-rename-' + key.id}
                                    title={k('rename')}
                                    aria-label={k('rename')}
                                    disabled={locked || operationPending}
                                    onClick={() => setRenameTarget(key)}
                                  >
                                    <FullWebIcon name="edit" size={14} />
                                  </Button>
                                  {revocationAvailable && (
                                    <Button
                                      type="button"
                                      variant="secondary"
                                      size="xs"
                                      iconOnly
                                      className="key-quick-btn key-quick-btn-regenerate"
                                      data-testid={'row-regenerate-' + key.id}
                                      title={k('regenerateKey')}
                                      aria-label={k('regenerateKey')}
                                      disabled={locked || operationPending}
                                      onClick={() => openRegenerateFor(key.id)}
                                    >
                                      <FullWebIcon name="rotate" size={14} />
                                    </Button>
                                  )}
                                  <DropdownMenu
                                    ariaLabel={k('moreActions') + ': ' + displayName}
                                    triggerIcon={<FullWebIcon name="moreVertical" size={14} />}
                                    triggerTitle={k('moreActions')}
                                    triggerClassName="btn btn-ghost btn-xs btn-icon-only key-quick-btn key-quick-btn-more"
                                    disabled={locked || operationPending}
                                    items={[
                                      {
                                        key: 'rename',
                                        label: k('rename'),
                                        onClick: () => setRenameTarget(key),
                                      },
                                      {
                                        key: 'usage',
                                        label: k('viewUsage'),
                                        onClick: () => openKeyDetails(key.id, { tab: 'usage' }),
                                      },
                                      ...(revocationAvailable
                                        ? [
                                            { key: 'divider-ops', type: 'divider' as const },
                                            {
                                              key: 'regenerate',
                                              label: k('regenerateKey'),
                                              onClick: () => openRegenerateFor(key.id),
                                            },
                                            {
                                              key: 'revoke',
                                              label: k('quickRevoke'),
                                              tone: 'danger' as const,
                                              onClick: () => openRevocationFor(key.id, 'revoke'),
                                            },
                                          ]
                                        : []),
                                    ]}
                                  />
                                </div>
                                <span className="key-actions-divider" aria-hidden="true" />
                              </>
                            )}
                            <Button
                              type="button"
                              variant="ghost"
                              size="xs"
                              className="key-row-detail-btn"
                              data-testid={'open-key-' + key.id}
                              disabled={locked}
                              onClick={() => openKeyDetails(key.id)}
                            >
                              {k('rowDetails')}
                            </Button>
                          </div>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
          {visibleKeys.length > 0 && (
            <PaginationFooter
              variant="flush"
              currentPage={currentPage}
              totalPages={totalPages}
              infoText={formatKeyPaginationInfo(
                currentPage,
                totalPages,
                startItem,
                endItem,
                visibleKeys.length
              )}
              pageSize={pageSize}
              pageSizeOptions={KEY_PAGE_SIZE_OPTIONS}
              pageSizeLabel={k('paginationPageSizeLabel')}
              pageSizeAriaLabel={k('paginationPageSizeLabel')}
              formatPageSizeOption={formatKeyPageSizeOption}
              onPageChange={setPage}
              onPageSizeChange={(next) => {
                setPageSize(next);
                setPage(1);
              }}
              prevLabel={k('paginationPrev')}
              nextLabel={k('paginationNext')}
              showPageJump
              jumpPrefixLabel={k('paginationJumpPrefix')}
              jumpSuffixLabel={k('paginationJumpSuffix')}
              jumpInputAriaLabel={k('paginationJumpLabel')}
              testIds={{
                root: 'key-pagination-bar',
                prev: 'key-pagination-prev',
                next: 'key-pagination-next',
                jumpInput: 'key-pagination-jump-input',
              }}
            />
          )}
        </div>
        {gatewayPending && (
          <aside className="key-gateway-note">
            <FullWebIcon name="shield" />
            <div>
              <strong>{k('unavailable')}</strong>
              <p>{k('unavailableHelp')}</p>
            </div>
          </aside>
        )}
        {!!keys?.some((key) => key.origin === 'adopted') && (
          <aside className="key-gateway-note" data-testid="adopted-key-note">
            <FullWebIcon name="shield" />
            <div>
              <p>{k('adoptedOriginNote')}</p>
            </div>
          </aside>
        )}
      </div>
      <div
        id="key-workspace-panel-access"
        role="tabpanel"
        aria-labelledby="key-workspace-tab-btn-access"
        className="key-access-tab-panel"
        data-testid="key-access-panel"
        hidden={workspaceTab !== 'access'}
      >
        <AccessInfoSummary
          client={client}
          auth={auth}
          onUnauthorized={onUnauthorized}
          reloadToken={reloadToken + accessReloadToken}
          hideRefresh={true}
          className="card fresh-keys-access-panel"
          onLoaded={setAccessInfo}
        />
      </div>
      {selected && (
        <FreshKeyDetails
          open={true}
          visible={visible}
          key={selected}
          id={selected}
          initialTab={detailIntent?.tab}
          initialAction={detailIntent?.action}
          client={client}
          gatewayPending={gatewayPending}
          operationPending={operationPending}
          accessInfo={accessInfo}
          reloadToken={reloadToken}
          credential={credential}
          onUnauthorized={onUnauthorized}
          onPending={(value) => {
            detailPendingRef.current = value;
            setDetailPending(value);
            reportPending();
          }}
          onSaved={(key) => {
            mutationRev.current += 1;
            setKeys((previous) => (previous || []).map((item) => (item.id === key.id ? key : item)));
          }}
          onClose={() => {
            setSelected('');
            setDetailIntent(null);
            if (
              pendingRefresh.current &&
              activeReadEpoch.current === null &&
              !activeMutation.current &&
              !detailPendingRef.current
            ) {
              pendingRefresh.current = false;
              void refresh(true);
            }
          }}
          onRequestOperation={
            revocationAvailable
              ? (desiredMode) =>
                  desiredMode === 'replace' || desiredMode === 'regenerate'
                    ? openRegenerateFor(selected)
                    : openRevocationFor(selected, desiredMode)
              : undefined
          }
          onRebind={
            onRebind
              ? () => {
                  if (!detailPendingRef.current) setSelected('');
                  onRebind();
                }
              : undefined
          }
          onBlocker={(code, detail) => {
            const typed = blockerOf(code);
            if (!typed) return;
            setBlocker(
              typed.kind === 'key_rejected' && detail
                ? { ...typed, detail }
                : typed
            );
          }}
        />
      )}
      <FreshKeyRenameModal
        open={!!renameTarget}
        visible={visible}
        record={renameTarget}
        client={client}
        credential={credential}
        onUnauthorized={onUnauthorized}
        onPending={(value) => {
          detailPendingRef.current = value;
          setDetailPending(value);
          reportPending();
        }}
        onSaved={(updated) => {
          mutationRev.current += 1;
          setKeys((previous) =>
            (previous || []).map((item) => (item.id === updated.id ? updated : item))
          );
          setRenameTarget((prev) => (prev && prev.id === updated.id ? updated : prev));
        }}
        onClose={() => setRenameTarget(null)}
      />
      {revocationAvailable && keys && (
        <FreshKeyRevocation
          open={revocationOpen}
          visible={visible}
          mode="modal"
          onClose={closeRevocationModal}
          onCreateReplacement={() => {
            closeRevocationModal();
            setConfirmed(false);
            setCreateOpen(true);
          }}
          onRegenerate={(targetId) => openRegenerateFor(targetId)}
          keys={keys}
          client={client}
          credential={credential}
          disabled={locked}
          preset={revocationPreset}
          reloadToken={reloadToken}
          onRebind={
            onRebind
              ? () => {
                  closeRevocationModal();
                  onRebind();
                }
              : undefined
          }
          onUnauthorized={onUnauthorized}
          onBlocker={(code, detail) => {
            const typed = blockerOf(code);
            if (!typed) return;
            setBlocker(
              typed.kind === 'key_rejected' && detail
                ? { ...typed, detail }
                : typed
            );
          }}
          onLatestOperation={(snapshot) => {
            setLatestOperationResult(snapshot);
            if (!snapshot?.operation || isTerminalKeyOperationOutcome(snapshot.operation.outcome)) {
              regeneratePendingRef.current = false;
            }
          }}
          onPending={(value) => {
            operationPendingRef.current = value;
            setOperationPending(value || regeneratePendingRef.current);
            reportPending();
          }}
          onChanged={() => void refresh(true)}
        />
      )}
      {revocationAvailable && keys && (
        <FreshKeyRegenerate
          open={!!regenerateTargetId}
          visible={visible}
          target={keys.find((item) => item.id === regenerateTargetId) || null}
          client={client}
          credential={credential}
          accessInfo={accessInfo ?? undefined}
          reloadToken={reloadToken}
          onUnauthorized={onUnauthorized}
          onLatestOperation={(snapshot) => {
            setLatestOperationResult(snapshot);
          }}
          onPending={(value) => {
            regeneratePendingRef.current = value;
            if (!value) operationPendingRef.current = false;
            setOperationPending(value || operationPendingRef.current);
            reportPending();
          }}
          onRebind={
            onRebind
              ? () => {
                  setRegenerateTargetId('');
                  onRebind();
                }
              : undefined
          }
          onBlocker={(code, detail) => {
            const typed = blockerOf(code);
            if (!typed) return;
            setBlocker(
              typed.kind === 'key_rejected' && detail
                ? { ...typed, detail }
                : typed
            );
          }}
          onChanged={() => {
            regenerateChangedRef.current = true;
            void refresh(true);
          }}
          onClose={() => {
            setRegenerateTargetId('');
            if (regenerateChangedRef.current) {
              regenerateChangedRef.current = false;
              void refreshUsageSummarySilently();
            }
          }}
        />
      )}
    </section>
  );
}
