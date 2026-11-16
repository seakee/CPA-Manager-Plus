import { useCallback, useEffect, useRef, useState, type ChangeEvent, type DragEvent, type ReactNode } from 'react';
import { artifactText, type FreshArtifactTextKey } from '../i18n/freshArtifact';
import { useFullWebLanguage } from './useFullWebLanguage';
import { FullWebIcon } from './FullWebIcon';
import {
  ARTIFACT_MAX_UPLOAD_BYTES,
  acquireArtifact,
  formatArtifactError,
  formatArtifactPinnedMismatch,
  isArtifactPinnedMismatch,
  isArtifactReady,
  isArtifactRecoveryRequired,
  isNonTransientArtifactReadError,
  mapArtifactErrorCode,
  parseArtifactStatus,
  probeArtifact,
  readArtifactStatus,
  uploadArtifact,
  validCDNPrefix,
  type ArtifactOperation,
  type ArtifactSource,
  type ArtifactStatus,
  type ArtifactTransportLike,
  type MappedArtifactError,
} from './freshArtifact';
import type { FullWebCredential } from './publicApi';

const POLL_INTERVAL_MS = 2000;
const POLL_INTERVAL_MAX_MS = 8000;
const POLL_FAILURE_LIMIT = 3;

function formatByteCount(bytes: number): string {
  if (bytes >= 1024 * 1024) {
    return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
  }
  if (bytes >= 1024) {
    return `${(bytes / 1024).toFixed(1)} KiB`;
  }
  return `${bytes} B`;
}

function phaseLabel(phase: ArtifactOperation['phase']): string {
  switch (phase) {
    case 'downloading':
      return artifactText('phaseDownloading');
    case 'verifying':
      return artifactText('phaseVerifying');
    case 'installing':
      return artifactText('phaseInstalling');
    case 'succeeded':
      return artifactText('phaseSucceeded');
    case 'failed':
      return artifactText('phaseFailed');
  }
}

function isRunningPhase(phase: ArtifactOperation['phase'] | undefined): boolean {
  return phase === 'downloading' || phase === 'verifying' || phase === 'installing';
}

function isAbortedArtifactError(error: unknown): boolean {
  if (!error || typeof error !== 'object') return false;
  const candidate = error as { kind?: unknown; aborted?: unknown; name?: unknown };
  return (
    candidate.kind === 'aborted' ||
    candidate.aborted === true ||
    candidate.name === 'AbortError'
  );
}

export interface FreshArtifactStepProps {
  transport: ArtifactTransportLike;
  onReady: () => void;
  credential?: FullWebCredential;
  initialStatus?: ArtifactStatus;
  hideHeader?: boolean;
  leadingAction?: ReactNode;
}

export function FreshArtifactStep({
  transport,
  onReady,
  credential,
  initialStatus,
  hideHeader = false,
  leadingAction,
}: FreshArtifactStepProps) {
  // Every sentence on this step is produced from a key at render time, so the
  // step itself follows a language switch even when its host keeps its state.
  useFullWebLanguage();
  const validatedInitial = (() => {
    if (!initialStatus) return null;
    try {
      return parseArtifactStatus(initialStatus);
    } catch {
      return undefined;
    }
  })();

  const [statusPhase, setStatusPhase] = useState<'loading' | 'ready' | 'unavailable'>(
    validatedInitial === undefined
      ? 'unavailable'
      : validatedInitial
        ? 'ready'
        : 'loading'
  );
  const [status, setStatus] = useState<ArtifactStatus | null>(validatedInitial || null);
  const [operation, setOperation] = useState<ArtifactOperation | null>(
    validatedInitial?.operation || null
  );
  const [dragOver, setDragOver] = useState(false);
  const [probeState, setProbeState] = useState<'idle' | 'checking' | 'reachable' | 'unreachable'>(
    'idle'
  );
  const [source, setSource] = useState<ArtifactSource>('github');
  const [selectedPresetId, setSelectedPresetId] = useState<string>('custom');
  const [customPrefix, setCustomPrefix] = useState<string>('');
  const [submitting, setSubmitting] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [polling, setPolling] = useState(() => isRunningPhase(validatedInitial?.operation?.phase));
  const [pollRun, setPollRun] = useState(0);
  const [error, setError] = useState<MappedArtifactError | null>(() => {
    if (validatedInitial === undefined) {
      return mapArtifactErrorCode(undefined);
    }
    if (isArtifactRecoveryRequired(validatedInitial)) {
      return mapArtifactErrorCode(
        'recovery_required',
        validatedInitial?.pinned.version
      );
    }
    if (isArtifactPinnedMismatch(validatedInitial)) {
      return formatArtifactPinnedMismatch(validatedInitial?.pinned.version);
    }
    if (validatedInitial?.operation?.phase === 'failed') {
      // A failure that survived a reload keeps the source that produced it, so
      // the remedy still names something that source can act on.
      return mapArtifactErrorCode(
        validatedInitial.operation.code,
        validatedInitial.pinned.version,
        undefined,
        validatedInitial.operation.source
      );
    }
    return null;
  });
  // The failing input keeps its key, so the sentence is produced at render and
  // follows a language switch like every other message on this step.
  const [validationMessage, setValidationMessage] = useState<FreshArtifactTextKey | null>(null);

  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const aliveRef = useRef(true);
  const readyCalledRef = useRef(false);
  const onReadyRef = useRef(onReady);
  onReadyRef.current = onReady;

  const notifyReady = useCallback(() => {
    if (readyCalledRef.current || !aliveRef.current) return;
    readyCalledRef.current = true;
    onReadyRef.current();
  }, []);

  useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
    };
  }, []);

  const runProbe = useCallback(async () => {
    if (!aliveRef.current) return;
    setProbeState('checking');
    try {
      const result = await probeArtifact(transport, credential);
      if (!aliveRef.current) return;
      setProbeState(result.reachable ? 'reachable' : 'unreachable');
    } catch (err) {
      if (!aliveRef.current || isAbortedArtifactError(err)) return;
      setProbeState('unreachable');
    }
  }, [transport, credential]);

  const loadStatus = useCallback(async () => {
    setStatusPhase('loading');
    setError(null);
    try {
      const next = await readArtifactStatus(transport, credential);
      if (!aliveRef.current) return;
      setStatus(next);
      setOperation(next.operation);
      setStatusPhase('ready');
      if (next.cdnPresets.length > 0 && selectedPresetId === 'custom' && !customPrefix) {
        setSelectedPresetId(next.cdnPresets[0].id);
      }
      if (isArtifactRecoveryRequired(next)) {
        setError(mapArtifactErrorCode('recovery_required', next.pinned.version));
        return;
      }
      if (isArtifactPinnedMismatch(next)) {
        setError(formatArtifactPinnedMismatch(next.pinned.version));
        return;
      }
      if (isArtifactReady(next)) {
        notifyReady();
        return;
      }
      if (next.operation?.phase === 'failed') {
        setError(
          mapArtifactErrorCode(
            next.operation.code,
            next.pinned.version,
            undefined,
            next.operation.source
          )
        );
      } else if (isRunningPhase(next.operation?.phase)) {
        setPolling(true);
        setPollRun((c) => c + 1);
      }
      void runProbe();
    } catch (err) {
      if (!aliveRef.current || isAbortedArtifactError(err)) return;
      setStatus(null);
      setStatusPhase('unavailable');
      setError(formatArtifactError(err));
    }
  }, [transport, credential, notifyReady, runProbe, selectedPresetId, customPrefix]);

  useEffect(() => {
    if (validatedInitial === undefined) return;
    if (validatedInitial) {
      if (isArtifactRecoveryRequired(validatedInitial)) {
        return;
      }
      if (isArtifactPinnedMismatch(validatedInitial)) {
        return;
      }
      if (isArtifactReady(validatedInitial)) {
        notifyReady();
        return;
      }
      void runProbe();
      return;
    }
    void loadStatus();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  useEffect(() => {
    if (!polling) return;
    let cancelled = false;
    let timer: number | undefined;
    let failures = 0;
    let stepIndex = 0;

    const scheduleNext = () => {
      const delay = Math.min(
        POLL_INTERVAL_MS * Math.pow(2, stepIndex),
        POLL_INTERVAL_MAX_MS
      );
      stepIndex += 1;
      timer = setTimeout(tick, delay) as unknown as number;
    };

    const tick = async () => {
      try {
        const next = await readArtifactStatus(transport, credential);
        if (cancelled || !aliveRef.current) return;
        failures = 0;
        setStatus(next);
        setOperation(next.operation);
        if (isArtifactRecoveryRequired(next)) {
          setPolling(false);
          setError(mapArtifactErrorCode('recovery_required', next.pinned.version));
          return;
        }
        if (isArtifactPinnedMismatch(next)) {
          setPolling(false);
          setError(formatArtifactPinnedMismatch(next.pinned.version));
          return;
        }
        if (isArtifactReady(next)) {
          setPolling(false);
          notifyReady();
          return;
        }
        if (next.operation?.phase === 'failed') {
          setPolling(false);
          setError(
            mapArtifactErrorCode(
              next.operation.code,
              next.pinned.version,
              undefined,
              next.operation.source
            )
          );
          return;
        }
        if (!isRunningPhase(next.operation?.phase)) {
          setPolling(false);
          return;
        }
        scheduleNext();
      } catch (err) {
        if (cancelled || !aliveRef.current) return;
        if (isAbortedArtifactError(err)) {
          setPolling(false);
          return;
        }
        if (isNonTransientArtifactReadError(err)) {
          setPolling(false);
          setError(formatArtifactError(err, status?.pinned.version));
          return;
        }
        failures += 1;
        if (failures >= POLL_FAILURE_LIMIT) {
          setPolling(false);
          setError(formatArtifactError(err, status?.pinned.version));
          return;
        }
        scheduleNext();
      }
    };
    scheduleNext();
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [polling, pollRun, transport, credential, notifyReady, status?.pinned.version]);

  const version = status?.pinned.version || '<version>';
  const recoveryRequired = isArtifactRecoveryRequired(status);
  const pinnedMismatch = !recoveryRequired && isArtifactPinnedMismatch(status);
  const preparationBlocked = recoveryRequired || pinnedMismatch;
  const busy = submitting || uploading || polling || statusPhase === 'loading';

  const effectiveCDNPrefix = (() => {
    if (!status) return customPrefix.trim();
    if (selectedPresetId !== 'custom') {
      const preset = status.cdnPresets.find((item) => item.id === selectedPresetId);
      if (preset) return preset.prefix;
    }
    return customPrefix.trim();
  })();

  const handleAcquire = async () => {
    if (busy || !status || preparationBlocked) return;
    setValidationMessage(null);
    setError(null);
    if (source === 'cdn' && !validCDNPrefix(effectiveCDNPrefix)) {
      setValidationMessage('cdnInvalidPrefix');
      return;
    }
    readyCalledRef.current = false;
    setSubmitting(true);
    try {
      const nextOp = await acquireArtifact(
        transport,
        source === 'github'
          ? { source: 'github' }
          : { source: 'cdn', cdnPrefix: effectiveCDNPrefix },
        credential
      );
      if (!aliveRef.current) return;
      setOperation(nextOp);
      if (nextOp.phase === 'succeeded') {
        notifyReady();
        return;
      }
      if (nextOp.phase === 'failed') {
        setError(mapArtifactErrorCode(nextOp.code, version, undefined, nextOp.source));
        return;
      }
      setPolling(true);
      setPollRun((c) => c + 1);
    } catch (err) {
      if (!aliveRef.current || isAbortedArtifactError(err)) return;
      const mapped = formatArtifactError(err, version, undefined, source);
      if (mapped.code === 'artifact_already_active') {
        notifyReady();
        return;
      }
      setError(mapped);
    } finally {
      if (aliveRef.current) setSubmitting(false);
    }
  };

  const selectSource = (next: ArtifactSource) => {
    if (busy) return;
    setSource(next);
    setValidationMessage(null);
  };

  const submitArchiveFile = async (file: File) => {
    if (!file || busy || !status || preparationBlocked) return;
    setValidationMessage(null);
    setError(null);
    if (file.size > ARTIFACT_MAX_UPLOAD_BYTES) {
      setError(mapArtifactErrorCode('archive_too_large', version));
      return;
    }
    readyCalledRef.current = false;
    setUploading(true);
    try {
      const nextOp = await uploadArtifact(transport, file, credential);
      if (!aliveRef.current) return;
      setOperation(nextOp);
      if (nextOp.phase === 'succeeded') {
        notifyReady();
        return;
      }
      if (nextOp.phase === 'failed') {
        setError(mapArtifactErrorCode(nextOp.code, version, undefined, nextOp.source));
        return;
      }
      setPolling(true);
      setPollRun((c) => c + 1);
    } catch (err) {
      if (!aliveRef.current || isAbortedArtifactError(err)) return;
      const mapped = formatArtifactError(err, version, undefined, source);
      if (mapped.code === 'artifact_already_active') {
        notifyReady();
        return;
      }
      setError(mapped);
    } finally {
      if (aliveRef.current) setUploading(false);
    }
  };

  const handleFileChange = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    if (event.target) {
      event.target.value = '';
    }
    if (file) {
      await submitArchiveFile(file);
    }
  };

  const handleDrop = (event: DragEvent<HTMLDivElement>) => {
    event.preventDefault();
    setDragOver(false);
    const file = event.dataTransfer?.files?.[0];
    if (file) {
      void submitArchiveFile(file);
    }
  };

  const isPostDownloadPhase =
    operation?.phase === 'verifying' || operation?.phase === 'installing';
  const progressPercent = !operation
    ? 0
    : isPostDownloadPhase
      ? 100
      : operation.totalBytes > 0
        ? Math.min(100, Math.max(0, Math.round((operation.receivedBytes / operation.totalBytes) * 100)))
        : 0;

  return (
    <section
      className="artifact-step"
      data-testid="fresh-artifact-step"
      aria-label={artifactText('title')}
      aria-busy={busy}
    >
      {!hideHeader && (
        <div className="artifact-step-header">
          <h2 className="artifact-step-title">{artifactText('title')}</h2>
          <p className="artifact-step-desc">{artifactText('description')}</p>
        </div>
      )}

      {statusPhase === 'loading' && (
        <p className="journey-quiet" role="status" data-testid="artifact-status-loading">
          <FullWebIcon name="refresh" size={18} />
          <span>{artifactText('probeChecking')}</span>
        </p>
      )}

      {statusPhase === 'unavailable' && (
        <div
          className="alert alert-warn artifact-alert"
          role="alert"
          data-testid="artifact-status-unavailable"
        >
          <p>{artifactText('statusUnavailable')}</p>
          {error && (
            <p data-testid="artifact-status-error">
              {artifactText(error.messageKey, error.vars)} {artifactText(error.remedyKey, error.vars)}
            </p>
          )}
          <div className="artifact-actions">
            <button
              type="button"
              className="btn btn-secondary"
              data-testid="artifact-status-retry"
              onClick={() => void loadStatus()}
            >
              <FullWebIcon name="refresh" size={16} />
              <span>{artifactText('retryStatus')}</span>
            </button>
          </div>
        </div>
      )}

      {status && statusPhase === 'ready' && (
        <>
          <div className="artifact-meta-card">
            <div className="artifact-meta-bar">
              <div className="artifact-meta-left">
                <span className="artifact-version-pill">
                  <FullWebIcon name="server" size={14} />
                  <span>CPA v</span>
                  <span data-testid="artifact-pinned-version">{status.pinned.version}</span>
                </span>
                {!preparationBlocked && (
                  <span
                    className={`artifact-probe-pill is-${probeState}`}
                    role="status"
                    data-testid="artifact-probe-status"
                    data-probe={probeState}
                  >
                    <FullWebIcon
                      name={
                        probeState === 'reachable'
                          ? 'check'
                          : probeState === 'unreachable'
                            ? 'alert'
                            : 'refresh'
                      }
                      size={13}
                    />
                    <span>
                      {probeState === 'checking'
                        ? artifactText('probeChecking')
                        : probeState === 'reachable'
                          ? artifactText('probeReachable')
                          : artifactText('probeUnreachable')}
                    </span>
                  </span>
                )}
              </div>
              {!preparationBlocked && (
                <button
                  type="button"
                  className="btn btn-ghost btn-xs"
                  data-testid="artifact-probe-btn"
                  disabled={busy || probeState === 'checking'}
                  onClick={() => void runProbe()}
                >
                  <FullWebIcon name="refresh" size={13} />
                  <span>{artifactText('probeRetry')}</span>
                </button>
              )}
            </div>
            <details className="artifact-digest-drawer">
              <summary>
                <FullWebIcon name="shield" size={13} />
                <span>{artifactText('digestToggle')}</span>
              </summary>
              <code className="artifact-digest-code" data-testid="artifact-pinned-digest">
                {status.pinned.artifactId}
              </code>
            </details>
          </div>

          {operation && isRunningPhase(operation.phase) && (
            <div
              className="artifact-progress-card"
              role="status"
              aria-live="polite"
              data-testid="artifact-operation-progress"
              data-phase={operation.phase}
            >
              <div className="artifact-progress-top">
                <span className="artifact-progress-phase">
                  <FullWebIcon name="refresh" size={15} />
                  {phaseLabel(operation.phase)}
                </span>
                <span
                  className="artifact-progress-bytes"
                  data-testid="artifact-operation-bytes"
                  data-received-bytes={operation.receivedBytes}
                  data-total-bytes={operation.totalBytes}
                >
                  {operation.totalBytes > 0
                    ? `${formatByteCount(operation.receivedBytes)} / ${formatByteCount(operation.totalBytes)} (${progressPercent}%)`
                    : formatByteCount(operation.receivedBytes)}
                </span>
              </div>
              <div className="artifact-progress-track" aria-hidden="true">
                <div
                  className={`artifact-progress-fill ${isPostDownloadPhase ? 'is-pulsing' : ''}`.trim()}
                  style={{ width: `${progressPercent}%` }}
                />
              </div>
            </div>
          )}

          {error && (
            <div
              className="alert alert-error artifact-alert"
              role="alert"
              data-testid="artifact-error"
              data-error-code={error.code}
            >
              <p>{artifactText(error.messageKey, error.vars)}</p>
              <p className="artifact-error-remedy" data-testid="artifact-error-remedy">
                {artifactText(error.remedyKey, error.vars)}
              </p>
              {(recoveryRequired || pinnedMismatch) && (
                <div className="artifact-actions">
                  <button
                    type="button"
                    className="btn btn-secondary"
                    data-testid="artifact-status-retry"
                    onClick={() => void loadStatus()}
                  >
                    <FullWebIcon name="refresh" size={16} />
                    <span>{artifactText('retryStatus')}</span>
                  </button>
                </div>
              )}
            </div>
          )}

          {validationMessage && (
            <div className="alert alert-warn artifact-alert" role="alert">
              <p data-testid="artifact-validation-error">{artifactText(validationMessage)}</p>
            </div>
          )}

          {!preparationBlocked && (
            <>
              <div
                className="artifact-options"
                role="group"
                aria-label={artifactText('sourceHeading')}
              >
                <div
                  className={`artifact-choice ${source === 'github' ? 'is-selected' : ''}`}
                  role="button"
                  tabIndex={0}
                  aria-pressed={source === 'github'}
                  aria-disabled={busy}
                  data-testid="artifact-source-github"
                  onClick={() => selectSource('github')}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault();
                      selectSource('github');
                    }
                  }}
                >
                  <span className="journey-radio" aria-hidden="true" />
                  <span className="artifact-choice-icon" aria-hidden="true">
                    <FullWebIcon name="globe" size={17} />
                  </span>
                  <div className="artifact-choice-copy">
                    <span className="artifact-choice-title">{artifactText('sourceGithubTitle')}</span>
                    <p>{artifactText('sourceGithubDesc')}</p>
                  </div>
                </div>

                <div className="artifact-choice-group">
                  <div
                    className={`artifact-choice ${source === 'cdn' ? 'is-selected' : ''}`}
                    role="button"
                    tabIndex={0}
                    aria-pressed={source === 'cdn'}
                    aria-disabled={busy}
                    data-testid="artifact-source-cdn"
                    onClick={() => selectSource('cdn')}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter' || e.key === ' ') {
                        e.preventDefault();
                        selectSource('cdn');
                      }
                    }}
                  >
                    <span className="journey-radio" aria-hidden="true" />
                    <span className="artifact-choice-icon" aria-hidden="true">
                      <FullWebIcon name="link" size={17} />
                    </span>
                    <div className="artifact-choice-copy">
                      <span className="artifact-choice-title">{artifactText('sourceCdnTitle')}</span>
                      <p>{artifactText('sourceCdnDesc')}</p>
                    </div>
                  </div>

                  {source === 'cdn' && (
                    <div className="artifact-cdn-controls">
                      {status.cdnPresets.length > 0 && (
                        <div className="form-group">
                          <label htmlFor="artifact-cdn-preset" className="form-label">
                            {artifactText('cdnPresetLabel')}
                          </label>
                          <select
                            id="artifact-cdn-preset"
                            className="form-input"
                            data-testid="artifact-cdn-preset"
                            value={selectedPresetId}
                            disabled={busy}
                            onChange={(e) => {
                              setSelectedPresetId(e.target.value);
                              setValidationMessage(null);
                            }}
                          >
                            {status.cdnPresets.map((preset) => (
                              <option key={preset.id} value={preset.id}>
                                {preset.id} ({preset.prefix})
                              </option>
                            ))}
                            <option value="custom">{artifactText('cdnCustomOption')}</option>
                          </select>
                        </div>
                      )}

                      {(status.cdnPresets.length === 0 || selectedPresetId === 'custom') && (
                        <div className="form-group">
                          <label htmlFor="artifact-cdn-input" className="form-label">
                            {artifactText('cdnCustomLabel')}
                          </label>
                          <input
                            id="artifact-cdn-input"
                            type="url"
                            className="form-input form-input-mono"
                            data-testid="artifact-cdn-prefix-input"
                            placeholder={artifactText('cdnCustomPlaceholder')}
                            value={customPrefix}
                            disabled={busy}
                            onChange={(e) => {
                              setCustomPrefix(e.target.value);
                              setValidationMessage(null);
                            }}
                          />
                        </div>
                      )}

                      <p
                        className="artifact-third-party-note"
                        data-testid="artifact-cdn-third-party-note"
                      >
                        {artifactText('cdnThirdPartyNote')}
                      </p>
                    </div>
                  )}
                </div>

                <div className="artifact-choice-group">
                  <div
                    className={`artifact-choice ${source === 'upload' ? 'is-selected' : ''}`}
                    role="button"
                    tabIndex={0}
                    aria-pressed={source === 'upload'}
                    aria-disabled={busy}
                    data-testid="artifact-source-upload"
                    onClick={() => selectSource('upload')}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter' || e.key === ' ') {
                        e.preventDefault();
                        selectSource('upload');
                      }
                    }}
                  >
                    <span className="journey-radio" aria-hidden="true" />
                    <span className="artifact-choice-icon" aria-hidden="true">
                      <FullWebIcon name="installation" size={17} />
                    </span>
                    <div className="artifact-choice-copy">
                      <span className="artifact-choice-title">{artifactText('sourceUploadTitle')}</span>
                      <p>{artifactText('sourceUploadDesc', { version })}</p>
                    </div>
                  </div>

                  {source === 'upload' && (
                    <div
                      className={`artifact-dropzone ${dragOver ? 'is-dragover' : ''}`.trim()}
                      data-testid="artifact-dropzone"
                      onDragOver={(e) => {
                        e.preventDefault();
                        if (!busy) setDragOver(true);
                      }}
                      onDragLeave={() => setDragOver(false)}
                      onDrop={handleDrop}
                    >
                      <input
                        ref={fileInputRef}
                        type="file"
                        accept=".tar.gz,application/gzip,application/x-gzip"
                        className="artifact-file-input"
                        data-testid="artifact-upload-input"
                        disabled={busy}
                        onChange={(e) => void handleFileChange(e)}
                      />
                      <span className="artifact-dropzone-icon" aria-hidden="true">
                        <FullWebIcon name="installation" size={20} />
                      </span>
                      <p className="artifact-dropzone-hint">
                        {artifactText('dropzoneHint', { version })}
                      </p>
                      <button
                        type="button"
                        className="btn btn-primary btn-sm"
                        data-testid="artifact-upload-btn"
                        disabled={busy}
                        onClick={() => fileInputRef.current?.click()}
                      >
                        {uploading ? (
                          <span data-testid="artifact-upload-busy">{artifactText('uploadBusy')}</span>
                        ) : (
                          artifactText('uploadSelectButton')
                        )}
                      </button>
                    </div>
                  )}
                </div>
              </div>

              {(leadingAction || source !== 'upload') && (
                <div className="artifact-actions artifact-footer-actions">
                  <div>{leadingAction}</div>
                  {source !== 'upload' && (
                    <button
                      type="button"
                      className="btn btn-primary"
                      data-testid="artifact-acquire-btn"
                      disabled={busy}
                      onClick={() => void handleAcquire()}
                    >
                      <span>
                        {submitting
                          ? artifactText('acquireBusy')
                          : artifactText('acquireStartButton')}
                      </span>
                      {!submitting && <FullWebIcon name="arrowRight" size={16} />}
                    </button>
                  )}
                </div>
              )}
            </>
          )}
        </>
      )}
    </section>
  );
}
