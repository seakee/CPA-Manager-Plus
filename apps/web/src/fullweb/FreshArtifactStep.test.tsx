import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setFullWebLanguage } from '../i18n/fullweb';
import { FreshArtifactStep } from './FreshArtifactStep';
import {
  ARTIFACT_MAX_UPLOAD_BYTES,
  type ArtifactOperation,
  type ArtifactProbe,
  type ArtifactStatus,
  type ArtifactTransport,
} from './freshArtifact';

const DIGEST = 'sha256:' + 'b'.repeat(64);

function makeStatus(overrides: Partial<ArtifactStatus> = {}): ArtifactStatus {
  return {
    schemaVersion: 1,
    pinned: { version: '8.0.55', artifactId: DIGEST },
    active: null,
    required: true,
    operation: null,
    cdnPresets: [],
    ...overrides,
  };
}

describe('FreshArtifactStep', () => {
  let renderer: ReactTestRenderer | undefined;

  beforeEach(() => {
    setFullWebLanguage('zh-CN');
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    vi.useFakeTimers();
  });

  afterEach(async () => {
    if (renderer) {
      await act(async () => renderer!.unmount());
      renderer = undefined;
    }
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  function byID(id: string) {
    return renderer!.root.findByProps({ 'data-testid': id });
  }

  function allByID(id: string) {
    return renderer!.root.findAllByProps({ 'data-testid': id });
  }

  async function click(id: string) {
    await act(async () => {
      await byID(id).props.onClick();
    });
  }

  it('probes official GitHub source, polls acquire progress with 2s->8s backoff, and calls onReady when active', async () => {
    const statuses: ArtifactStatus[] = [
      makeStatus(),
      makeStatus({
        operation: {
          id: 'op-1',
          source: 'github',
          phase: 'downloading',
          receivedBytes: 1048576,
          totalBytes: 4194304,
          code: '',
        },
      }),
      makeStatus({
        operation: {
          id: 'op-1',
          source: 'github',
          phase: 'verifying',
          receivedBytes: 4194304,
          totalBytes: 4194304,
          code: '',
        },
      }),
      makeStatus({
        active: { artifactId: DIGEST, origin: 'acquired' },
        required: false,
        operation: {
          id: 'op-1',
          source: 'github',
          phase: 'succeeded',
          receivedBytes: 4194304,
          totalBytes: 4194304,
          code: '',
        },
      }),
    ];
    const transport: ArtifactTransport = {
      readStatus: vi.fn(async () => statuses.shift() || statuses[0]),
      probe: vi.fn<() => Promise<ArtifactProbe>>(async () => ({ reachable: true, code: '' })),
      acquire: vi.fn<() => Promise<ArtifactOperation>>(async () => ({
        id: 'op-1',
        source: 'github',
        phase: 'downloading',
        receivedBytes: 512,
        totalBytes: 4194304,
        code: '',
      })),
      upload: vi.fn(),
    };
    const onReady = vi.fn();

    await act(async () => {
      renderer = create(<FreshArtifactStep transport={transport} onReady={onReady} />);
    });

    expect(byID('artifact-pinned-version').props.children).toBe('8.0.55');
    expect(byID('artifact-pinned-digest').props.children).toBe(DIGEST);
    expect(byID('artifact-probe-status').props['data-probe']).toBe('reachable');
    expect(byID('artifact-source-github').props['aria-pressed']).toBe(true);

    await click('artifact-acquire-btn');
    expect(transport.acquire).toHaveBeenCalledWith({ source: 'github' });
    expect(byID('artifact-operation-progress').props['data-phase']).toBe('downloading');
    expect(byID('artifact-operation-bytes').props['data-received-bytes']).toBe(512);
    expect(byID('artifact-operation-bytes').props['data-total-bytes']).toBe(4194304);

    // 1st poll at 2000ms
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    expect(transport.readStatus).toHaveBeenCalledTimes(2);
    expect(byID('artifact-operation-bytes').props['data-received-bytes']).toBe(1048576);

    // 2nd poll at 4000ms
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3999);
    });
    expect(transport.readStatus).toHaveBeenCalledTimes(2);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    expect(transport.readStatus).toHaveBeenCalledTimes(3);
    expect(byID('artifact-operation-progress').props['data-phase']).toBe('verifying');

    // 3rd poll at 8000ms -> succeeded -> onReady
    await act(async () => {
      await vi.advanceTimersByTimeAsync(8000);
    });
    expect(transport.readStatus).toHaveBeenCalledTimes(4);
    expect(onReady).toHaveBeenCalledTimes(1);
  });

  it('never auto-falls back to a CDN when probe or GitHub acquire fails, and aborts polling after 3 consecutive read failures', async () => {
    let pollCalls = 0;
    const transport: ArtifactTransport = {
      readStatus: vi.fn(async () => {
        pollCalls += 1;
        if (pollCalls === 1) {
          return makeStatus({
            cdnPresets: [
              { id: 'mirror-1', prefix: 'https://cdn.example.com/', thirdParty: true },
            ],
          });
        }
        throw { code: 'runtime_unavailable', status: 503 };
      }),
      probe: vi.fn(async () => ({ reachable: false, code: 'source_unreachable' })),
      acquire: vi
        .fn<() => Promise<ArtifactOperation>>()
        .mockRejectedValueOnce({ code: 'source_unreachable', status: 502 })
        .mockResolvedValueOnce({
          id: 'op-2',
          source: 'github',
          phase: 'downloading',
          receivedBytes: 0,
          totalBytes: 1000,
          code: '',
        }),
      upload: vi.fn(),
    };
    const onReady = vi.fn();

    await act(async () => {
      renderer = create(<FreshArtifactStep transport={transport} onReady={onReady} />);
    });

    expect(byID('artifact-probe-status').props['data-probe']).toBe('unreachable');
    expect(byID('artifact-source-github').props['aria-pressed']).toBe(true);

    await click('artifact-acquire-btn');
    expect(transport.acquire).toHaveBeenCalledTimes(1);
    expect(transport.acquire).toHaveBeenCalledWith({ source: 'github' });
    expect(byID('artifact-error').props['data-error-code']).toBe('source_unreachable');

    // Retry acquire -> starts polling -> 3 consecutive readStatus failures abort polling
    await click('artifact-acquire-btn');
    expect(allByID('artifact-operation-progress')).toHaveLength(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(4000);
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(8000);
    });
    expect(pollCalls).toBe(4); // 1 initial + 3 failed polls
    expect(byID('artifact-error').props['data-error-code']).toBe('runtime_unavailable');

    // No further poll after 3 consecutive failures
    await act(async () => {
      await vi.advanceTimersByTimeAsync(16000);
    });
    expect(pollCalls).toBe(4);
    expect(onReady).not.toHaveBeenCalled();
  });

  it('validates custom CDN https prefix, shows third-party warning, and enforces 64 MiB upload limit client-side', async () => {
    let resolveUpload!: (op: ArtifactOperation) => void;
    const uploadDeferred = new Promise<ArtifactOperation>((resolve) => {
      resolveUpload = resolve;
    });
    const transport: ArtifactTransport = {
      readStatus: vi.fn(async () => makeStatus()),
      probe: vi.fn(async () => ({ reachable: false, code: 'source_unreachable' })),
      acquire: vi.fn<() => Promise<ArtifactOperation>>(async () => ({
        id: 'op-cdn',
        source: 'cdn',
        phase: 'succeeded',
        receivedBytes: 100,
        totalBytes: 100,
        code: '',
      })),
      upload: vi.fn(() => uploadDeferred),
    };
    const onReady = vi.fn();

    await act(async () => {
      renderer = create(<FreshArtifactStep transport={transport} onReady={onReady} />);
    });

    await click('artifact-source-cdn');
    expect(allByID('artifact-cdn-third-party-note')).toHaveLength(1);

    await act(async () => {
      byID('artifact-cdn-prefix-input').props.onChange({
        target: { value: 'http://not-https.example.com/' },
      });
    });
    await click('artifact-acquire-btn');
    expect(transport.acquire).not.toHaveBeenCalled();
    expect(allByID('artifact-validation-error')).toHaveLength(1);

    // Switch to upload and test > 64 MiB client-side rejection
    await click('artifact-source-upload');
    const tooLargeFile = {
      size: ARTIFACT_MAX_UPLOAD_BYTES + 1,
      name: 'CLIProxyAPI_8.0.55_linux_amd64.tar.gz',
    } as unknown as File;
    await act(async () => {
      await byID('artifact-upload-input').props.onChange({
        target: { files: [tooLargeFile], value: 'fake.tar.gz' },
      });
    });
    expect(transport.upload).not.toHaveBeenCalled();
    expect(byID('artifact-error').props['data-error-code']).toBe('archive_too_large');

    // Valid file upload shows busy state then calls onReady
    const validFile = new Blob([new Uint8Array([0x1f, 0x8b])], { type: 'application/gzip' });
    let uploadPromise!: Promise<void>;
    await act(async () => {
      uploadPromise = byID('artifact-upload-input').props.onChange({
        target: { files: [validFile], value: 'valid.tar.gz' },
      });
    });
    expect(allByID('artifact-upload-busy')).toHaveLength(1);
    expect(byID('fresh-artifact-step').props['aria-busy']).toBe(true);

    await act(async () => {
      resolveUpload({
        id: 'op-upload',
        source: 'upload',
        phase: 'succeeded',
        receivedBytes: 2,
        totalBytes: 2,
        code: '',
      });
      await uploadPromise;
    });
    expect(transport.upload).toHaveBeenCalledWith(validFile);
    expect(onReady).toHaveBeenCalledTimes(1);
  });

  it('renders unreadable status as unavailable and never calls onReady', async () => {
    const transport: ArtifactTransport = {
      readStatus: vi.fn(async () => {
        throw { code: 'runtime_unavailable', status: 503 };
      }),
      probe: vi.fn(),
      acquire: vi.fn(),
      upload: vi.fn(),
    };
    const onReady = vi.fn();

    await act(async () => {
      renderer = create(<FreshArtifactStep transport={transport} onReady={onReady} />);
    });

    expect(allByID('artifact-status-unavailable')).toHaveLength(1);
    expect(onReady).not.toHaveBeenCalled();
  });

  it('does not treat a historical succeeded operation as ready while required is true, and surfaces recovery_required over active artifact', async () => {
    const historicalSucceeded = makeStatus({
      active: null,
      required: true,
      operation: {
        id: 'op-old',
        source: 'github',
        phase: 'succeeded',
        receivedBytes: 1024,
        totalBytes: 1024,
        code: '',
      },
    });
    const recoveryRequiredOnActive = makeStatus({
      active: { artifactId: DIGEST, origin: 'bundled' },
      required: false,
      operation: {
        id: 'op-rec',
        source: 'upload',
        phase: 'failed',
        receivedBytes: 1024,
        totalBytes: 1024,
        code: 'recovery_required',
      },
    });

    let callCount = 0;
    const transport: ArtifactTransport = {
      readStatus: vi.fn(async () => {
        callCount += 1;
        return callCount === 1 ? historicalSucceeded : recoveryRequiredOnActive;
      }),
      probe: vi.fn(async () => ({ reachable: true, code: '' })),
      acquire: vi.fn<() => Promise<ArtifactOperation>>(async () => ({
        id: 'op-poll',
        source: 'github',
        phase: 'downloading',
        receivedBytes: 0,
        totalBytes: 1024,
        code: '',
      })),
      upload: vi.fn(),
    };
    const onReady = vi.fn();

    await act(async () => {
      renderer = create(<FreshArtifactStep transport={transport} onReady={onReady} />);
    });
    expect(onReady).not.toHaveBeenCalled();
    expect(allByID('fresh-artifact-step')).toHaveLength(1);

    // Start acquire -> poll returns recovery_required with active != null -> shows recovery_required error, never calls onReady
    await click('artifact-acquire-btn');
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    expect(onReady).not.toHaveBeenCalled();
    expect(byID('artifact-error').props['data-error-code']).toBe('recovery_required');
  });

  it('handles lifecycle aborts quietly without flashing an error and stops polling immediately', async () => {
    let pollCount = 0;
    const transport: ArtifactTransport = {
      readStatus: vi.fn(async () => {
        pollCount += 1;
        if (pollCount === 1) {
          return makeStatus();
        }
        throw { kind: 'aborted', aborted: true, status: 0 };
      }),
      probe: vi.fn(async () => ({ reachable: true, code: '' })),
      acquire: vi.fn<() => Promise<ArtifactOperation>>(async () => ({
        id: 'op-abort',
        source: 'github',
        phase: 'downloading',
        receivedBytes: 128,
        totalBytes: 1024,
        code: '',
      })),
      upload: vi.fn<() => Promise<ArtifactOperation>>(async () => {
        throw { kind: 'aborted', aborted: true, status: 0 };
      }),
    };
    const onReady = vi.fn();

    await act(async () => {
      renderer = create(<FreshArtifactStep transport={transport} onReady={onReady} />);
    });

    await click('artifact-acquire-btn');
    expect(allByID('artifact-operation-progress')).toHaveLength(1);

    // Poll fires at 2000ms and rejects with lifecycle abort -> stops polling quietly with no error banner
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    expect(pollCount).toBe(2);
    expect(allByID('artifact-error')).toHaveLength(0);

    // No subsequent poll is scheduled
    await act(async () => {
      await vi.advanceTimersByTimeAsync(16000);
    });
    expect(pollCount).toBe(2);
  });

  it('treats 503 upgrade_credential_timeout during status polling as a transient read failure counted toward the 3-failure abort, never ready', async () => {
    let pollCount = 0;
    const transport: ArtifactTransport = {
      readStatus: vi.fn(async () => {
        pollCount += 1;
        if (pollCount === 1) {
          return makeStatus();
        }
        throw { kind: 'http', status: 503, code: 'upgrade_credential_timeout' };
      }),
      probe: vi.fn(async () => ({ reachable: true, code: '' })),
      acquire: vi.fn<() => Promise<ArtifactOperation>>(async () => ({
        id: 'op-timeout',
        source: 'github',
        phase: 'downloading',
        receivedBytes: 256,
        totalBytes: 2048,
        code: '',
      })),
      upload: vi.fn(),
    };
    const onReady = vi.fn();

    await act(async () => {
      renderer = create(<FreshArtifactStep transport={transport} onReady={onReady} />);
    });

    await click('artifact-acquire-btn');
    expect(allByID('artifact-operation-progress')).toHaveLength(1);

    // 1st transient 503 timeout at 2000ms -> keeps polling, no error banner yet
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    expect(pollCount).toBe(2);
    expect(allByID('artifact-error')).toHaveLength(0);

    // 2nd transient 503 timeout at 4000ms -> keeps polling, no error banner yet
    await act(async () => {
      await vi.advanceTimersByTimeAsync(4000);
    });
    expect(pollCount).toBe(3);
    expect(allByID('artifact-error')).toHaveLength(0);

    // 3rd consecutive 503 timeout at 8000ms -> aborts polling and displays mapped retryable timeout message
    await act(async () => {
      await vi.advanceTimersByTimeAsync(8000);
    });
    expect(pollCount).toBe(4);
    expect(byID('artifact-error').props['data-error-code']).toBe('upgrade_credential_timeout');
    expect(onReady).not.toHaveBeenCalled();

    // No 4th poll after 3 consecutive failures
    await act(async () => {
      await vi.advanceTimersByTimeAsync(16000);
    });
    expect(pollCount).toBe(4);
  });

  it('blocks readiness and acquisition controls when active.artifactId does not match pinned.artifactId', async () => {
    const transport: ArtifactTransport = {
      readStatus: vi.fn(async () =>
        makeStatus({
          active: { artifactId: 'sha256:' + 'f'.repeat(64), origin: 'bundled' },
          required: false,
        })
      ),
      probe: vi.fn(async () => ({ reachable: true, code: '' })),
      acquire: vi.fn(),
      upload: vi.fn(),
    };
    const onReady = vi.fn();

    await act(async () => {
      renderer = create(<FreshArtifactStep transport={transport} onReady={onReady} />);
    });

    expect(onReady).not.toHaveBeenCalled();
    expect(transport.probe).not.toHaveBeenCalled();
    expect(byID('artifact-error').props['data-error-code']).toBe('pinned_mismatch');
    expect(allByID('artifact-acquire-btn')).toHaveLength(0);
    expect(allByID('artifact-source-github')).toHaveLength(0);
    expect(allByID('artifact-status-retry')).toHaveLength(1);
  });
});
