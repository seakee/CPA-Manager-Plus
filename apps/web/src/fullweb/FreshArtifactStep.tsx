import { useCallback, useEffect, useRef, useState, type ChangeEvent } from 'react';
import { artifactText } from '../i18n/freshArtifact';
import { FullWebIcon } from './FullWebIcon';
import {
  ARTIFACT_MAX_UPLOAD_BYTES,
  acquireArtifact,
  formatArtifactError,
  isArtifactReady,
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
  initialStatus?: ArtifactStatus | null;
}

export function FreshArtifactStep({
  transport,
  onReady,
  credential,
  initialStatus,
}: FreshArtifactStepProps) {
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
    if (validatedInitial?.operation?.phase === 'failed') {
      return mapArtifactErrorCode(
        validatedInitial.operation.code,
        validatedInitial.pinned.version
      );
    }
    return null;
  });
  const [validationMessage, setValidationMessage] = useState<string>('');

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
      if (isArtifactReady(next)) {
        notifyReady();
        return;
      }
      if (next.operation?.phase === 'failed') {
        setError(mapArtifactErrorCode(next.operation.code, next.pinned.version));
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
        if (isArtifactReady(next)) {
          setPolling(false);
          notifyReady();
          return;
        }
        if (next.operation?.phase === 'failed') {
          setPolling(false);
          setError(mapArtifactErrorCode(next.operation.code, next.pinned.version));
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
    if (busy || !status) return;
    setValidationMessage('');
    setError(null);
    if (source === 'cdn' && !validCDNPrefix(effectiveCDNPrefix)) {
      setValidationMessage(artifactText('cdnInvalidPrefix'));
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
        setError(mapArtifactErrorCode(nextOp.code, version));
        return;
      }
      setPolling(true);
      setPollRun((c) => c + 1);
    } catch (err) {
      if (!aliveRef.current || isAbortedArtifactError(err)) return;
      const mapped = formatArtifactError(err, version);
      if (mapped.code === 'artifact_already_active') {
        notifyReady();
        return;
      }
      setError(mapped);
    } finally {
      if (aliveRef.current) setSubmitting(false);
    }
  };

  const handleFileChange = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    if (event.target) {
      event.target.value = '';
    }
    if (!file || busy || !status) return;
    setValidationMessage('');
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
        setError(mapArtifactErrorCode(nextOp.code, version));
        return;
      }
      setPolling(true);
      setPollRun((c) => c + 1);
    } catch (err) {
      if (!aliveRef.current || isAbortedArtifactError(err)) return;
      const mapped = formatArtifactError(err, version);
      if (mapped.code === 'artifact_already_active') {
        notifyReady();
        return;
      }
      setError(mapped);
    } finally {
      if (aliveRef.current) setUploading(false);
    }
  };

  return (
    <section
      className="artifact-step"
      data-testid="fresh-artifact-step"
      aria-label={artifactText('title')}
      aria-busy={busy}
    >
      <div className="artifact-step-header">
        <h2 className="artifact-step-title">{artifactText('title')}</h2>
        <p className="artifact-step-desc">{artifactText('description')}</p>
      </div>

      {statusPhase === 'loading' && (
        <p className="journey-quiet" role="status" data-testid="artifact-status-loading">
          <FullWebIcon name="refresh" size={18} />
          <span>{artifactText('probeChecking')}</span>
        </p>
      )}

      {statusPhase === 'unavailable' && (
        <div
          className="alert alert-warn"
          role="alert"
          data-testid="artifact-status-unavailable"
        >
          <p>{error ? error.message : artifactText('statusUnavailable')}</p>
          {error && <p className="artifact-error-remedy">{error.remedy}</p>}
          <div className="artifact-actions">
            <button
              type="button"
              className="btn btn-secondary"
              data-testid="artifact-status-retry"
              onClick={() => void loadStatus()}
            >
              {artifactText('retryStatus')}
            </button>
          </div>
        </div>
      )}

      {statusPhase === 'ready' && status && (
        <>
          <dl className="journey-summary" data-testid="artifact-pinned-summary">
            <div className="journey-summary-row">
              <FullWebIcon name="server" size={20} />
              <dt>{artifactText('pinnedVersionLabel')}</dt>
              <dd data-testid="artifact-pinned-version">{status.pinned.version}</dd>
            </div>
            <div className="journey-summary-row">
              <FullWebIcon name="shield" size={20} />
              <dt>{artifactText('pinnedDigestLabel')}</dt>
              <dd data-testid="artifact-pinned-digest">{status.pinned.artifactId}</dd>
            </div>
          </dl>

          <div
            className="artifact-probe-bar"
            role="status"
            data-testid="artifact-probe-status"
            data-probe={probeState}
          >
            <span>
              {probeState === 'checking' || probeState === 'idle'
                ? artifactText('probeChecking')
                : probeState === 'reachable'
                  ? artifactText('probeReachable')
                  : artifactText('probeUnreachable')}
            </span>
            <button
              type="button"
              className="btn btn-ghost"
              data-testid="artifact-probe-retry"
              disabled={probeState === 'checking' || busy}
              onClick={() => void runProbe()}
            >
              {artifactText('probeRetry')}
            </button>
          </div>

          {error && (
            <div
              className="alert alert-warn"
              role="alert"
              data-testid="artifact-error"
              data-error-code={error.code}
            >
              <p data-testid="artifact-error-message">{error.message}</p>
              <p className="artifact-error-remedy" data-testid="artifact-error-remedy">
                {error.remedy}
              </p>
            </div>
          )}

          {validationMessage && (
            <p className="alert alert-warn" role="alert" data-testid="artifact-validation-error">
              {validationMessage}
            </p>
          )}

          {operation && isRunningPhase(operation.phase) && (
            <div
              className="artifact-progress"
              role="status"
              aria-live="polite"
              data-testid="artifact-operation-progress"
              data-phase={operation.phase}
            >
              <span className="artifact-progress-phase" data-testid="artifact-operation-phase">
                {phaseLabel(operation.phase)}
              </span>
              <span
                className="artifact-progress-bytes"
                data-testid="artifact-operation-bytes"
                data-received-bytes={operation.receivedBytes}
                data-total-bytes={operation.totalBytes}
              >
                {operation.totalBytes > 0
                  ? `${formatByteCount(operation.receivedBytes)} / ${formatByteCount(operation.totalBytes)} (${operation.receivedBytes} / ${operation.totalBytes})`
                  : `${formatByteCount(operation.receivedBytes)} (${operation.receivedBytes})`}
              </span>
            </div>
          )}

          {uploading && (
            <div
              className="artifact-progress"
              role="status"
              aria-live="polite"
              data-testid="artifact-upload-busy"
            >
              <span className="artifact-progress-phase">{artifactText('uploadBusy')}</span>
            </div>
          )}

          <div className="artifact-options" role="group" aria-label={artifactText('sourceHeading')}>
            <button
              type="button"
              data-testid="artifact-source-github"
              className={`journey-choice ${source === 'github' ? 'selected' : ''}`}
              aria-pressed={source === 'github'}
              disabled={busy}
              onClick={() => {
                setSource('github');
                setValidationMessage('');
              }}
            >
              <span className="journey-radio" aria-hidden="true" />
              <span>
                <h2>{artifactText('sourceGithubTitle')}</h2>
                <p>{artifactText('sourceGithubDesc')}</p>
              </span>
            </button>

            <button
              type="button"
              data-testid="artifact-source-cdn"
              className={`journey-choice ${source === 'cdn' ? 'selected' : ''}`}
              aria-pressed={source === 'cdn'}
              disabled={busy}
              onClick={() => {
                setSource('cdn');
                setValidationMessage('');
              }}
            >
              <span className="journey-radio" aria-hidden="true" />
              <span>
                <h2>{artifactText('sourceCdnTitle')}</h2>
                <p>{artifactText('sourceCdnDesc')}</p>
              </span>
            </button>

            {source === 'cdn' && (
              <div className="artifact-cdn-controls" data-testid="artifact-cdn-panel">
                {status.cdnPresets.length > 0 && (
                  <div className="journey-field">
                    <label className="form-label" htmlFor="artifact-cdn-preset">
                      {artifactText('cdnPresetLabel')}
                    </label>
                    <select
                      id="artifact-cdn-preset"
                      data-testid="artifact-cdn-preset-select"
                      className="form-input"
                      value={selectedPresetId}
                      disabled={busy}
                      onChange={(e) => setSelectedPresetId(e.target.value)}
                    >
                      {status.cdnPresets.map((preset) => (
                        <option key={preset.id} value={preset.id}>
                          {preset.prefix}
                        </option>
                      ))}
                      <option value="custom">{artifactText('cdnCustomOption')}</option>
                    </select>
                  </div>
                )}

                {(status.cdnPresets.length === 0 || selectedPresetId === 'custom') && (
                  <div className="journey-field">
                    <label className="form-label" htmlFor="artifact-cdn-custom-prefix">
                      {artifactText('cdnCustomLabel')}
                    </label>
                    <input
                      id="artifact-cdn-custom-prefix"
                      data-testid="artifact-cdn-prefix-input"
                      className="form-input"
                      type="url"
                      placeholder={artifactText('cdnCustomPlaceholder')}
                      value={customPrefix}
                      disabled={busy}
                      autoComplete="off"
                      spellCheck={false}
                      onChange={(e) => {
                        setCustomPrefix(e.target.value);
                        setValidationMessage('');
                      }}
                    />
                  </div>
                )}

                <p className="artifact-third-party-note" data-testid="artifact-cdn-third-party-note">
                  {artifactText('cdnThirdPartyNote')}
                </p>
              </div>
            )}

            <button
              type="button"
              data-testid="artifact-source-upload"
              className={`journey-choice ${source === 'upload' ? 'selected' : ''}`}
              aria-pressed={source === 'upload'}
              disabled={busy}
              onClick={() => {
                setSource('upload');
                setValidationMessage('');
              }}
            >
              <span className="journey-radio" aria-hidden="true" />
              <span>
                <h2>{artifactText('sourceUploadTitle')}</h2>
                <p>{artifactText('sourceUploadDesc')}</p>
              </span>
            </button>

            {source === 'upload' && (
              <div className="artifact-upload-controls" data-testid="artifact-upload-panel">
                <input
                  ref={fileInputRef}
                  type="file"
                  accept=".tar.gz,application/gzip,application/x-gzip"
                  className="artifact-file-input"
                  data-testid="artifact-upload-input"
                  disabled={busy}
                  onChange={(e) => void handleFileChange(e)}
                />
                <div className="artifact-actions">
                  <button
                    type="button"
                    className="btn btn-primary"
                    data-testid="artifact-upload-btn"
                    disabled={busy}
                    onClick={() => fileInputRef.current?.click()}
                  >
                    {uploading ? artifactText('uploadBusy') : artifactText('uploadSelectButton')}
                  </button>
                </div>
              </div>
            )}
          </div>

          {(source === 'github' || source === 'cdn') && (
            <div className="artifact-actions">
              <button
                type="button"
                className="btn btn-primary"
                data-testid="artifact-acquire-btn"
                disabled={busy}
                onClick={() => void handleAcquire()}
              >
                {submitting || polling
                  ? artifactText('acquireBusy')
                  : artifactText('acquireStartButton')}
              </button>
            </div>
          )}
        </>
      )}
    </section>
  );
}
