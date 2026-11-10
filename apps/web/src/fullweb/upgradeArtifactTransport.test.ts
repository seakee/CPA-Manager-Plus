import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  ARTIFACT_MAX_UPLOAD_BYTES,
  formatArtifactError,
} from './freshArtifact';
import { createUpgradeArtifactTransport } from './upgradeArtifactTransport';

describe('upgradeArtifactTransport', () => {
  beforeEach(() => {
    vi.stubGlobal('window', {
      location: new URL('https://manager.test/.well-known/cpamp-bootstrap/ui'),
    });
  });
  afterEach(() => vi.unstubAllGlobals());

  it('builds the upgrade artifact transport with same-origin Bearer flags, per-op timeouts, strict parsers, and raw gzip upload', async () => {
    vi.useFakeTimers();
    try {
      const digest = 'sha256:' + 'c'.repeat(64);
      const calls: Array<{ url: string; init: RequestInit }> = [];
      const fetcher = vi.fn(async (input: unknown, init?: RequestInit) => {
        const url = String(input);
        calls.push({ url, init: init ?? {} });
        if (url.endsWith('/runtime/artifact')) {
          return Response.json({
            schemaVersion: 1,
            pinned: { version: '8.0.55', artifactId: digest },
            active: null,
            required: true,
            operation: null,
            cdnPresets: [],
          });
        }
        if (url.endsWith('/runtime/artifact/probe')) {
          return Response.json({ reachable: true, code: '' });
        }
        if (url.endsWith('/runtime/artifact/acquire')) {
          return Response.json({
            operation: {
              id: 'op-upgrade-1',
              source: 'github',
              phase: 'downloading',
              receivedBytes: 0,
              totalBytes: 2048,
              code: '',
            },
          });
        }
        if (url.endsWith('/runtime/artifact/upload')) {
          return Response.json({
            operation: {
              id: 'op-upgrade-upload',
              source: 'upload',
              phase: 'succeeded',
              receivedBytes: 4,
              totalBytes: 4,
              code: '',
            },
          });
        }
        throw new Error(`unexpected url ${url}`);
      });

      let currentKey = 'legacy-1x-key';
      const transport = createUpgradeArtifactTransport(
        () => currentKey,
        undefined,
        fetcher as unknown as typeof fetch
      );
      const base = '/.well-known/cpamp-bootstrap/api/v2/installation/upgrade/runtime/artifact';

      const status = await transport.readStatus();
      expect(status.required).toBe(true);
      expect(calls[0].url).toBe(base);
      expect(calls[0].init).toMatchObject({
        method: 'GET',
        mode: 'same-origin',
        credentials: 'omit',
        redirect: 'error',
        cache: 'no-store',
        headers: {
          Accept: 'application/json',
          Authorization: 'Bearer legacy-1x-key',
        },
      });
      expect(calls[0].init.signal).toBeInstanceOf(AbortSignal);

      const probe = await transport.probe();
      expect(probe).toEqual({ reachable: true, code: '' });
      expect(calls[1].url).toBe(`${base}/probe`);
      expect(calls[1].init).toMatchObject({
        method: 'POST',
        mode: 'same-origin',
        credentials: 'omit',
        redirect: 'error',
        cache: 'no-store',
        body: '{}',
        headers: {
          Accept: 'application/json',
          'Content-Type': 'application/json',
          Authorization: 'Bearer legacy-1x-key',
        },
      });
      expect(calls[1].init.signal).toBeInstanceOf(AbortSignal);

      const acquired = await transport.acquire({ source: 'github' });
      expect(acquired.id).toBe('op-upgrade-1');
      expect(calls[2].url).toBe(`${base}/acquire`);
      expect(calls[2].init).toMatchObject({
        method: 'POST',
        mode: 'same-origin',
        credentials: 'omit',
        redirect: 'error',
        cache: 'no-store',
        body: JSON.stringify({ source: 'github' }),
        headers: {
          Accept: 'application/json',
          'Content-Type': 'application/json',
          Authorization: 'Bearer legacy-1x-key',
        },
      });
      expect(calls[2].init.signal).toBeInstanceOf(AbortSignal);

      const rawGzip = new Uint8Array([0x1f, 0x8b, 0x08, 0x00]);
      const uploaded = await transport.upload(rawGzip);
      expect(uploaded.phase).toBe('succeeded');
      expect(calls[3].url).toBe(`${base}/upload`);
      expect(calls[3].init).toMatchObject({
        method: 'PUT',
        mode: 'same-origin',
        credentials: 'omit',
        redirect: 'error',
        cache: 'no-store',
        headers: {
          Accept: 'application/json',
          'Content-Type': 'application/gzip',
          Authorization: 'Bearer legacy-1x-key',
        },
      });
      expect(calls[3].init.body).toBe(rawGzip);
      // Raw gzip upload has no JSON read deadline / timeout signal.
      expect(calls[3].init.signal).toBeUndefined();

      // Enforces 5s / 10s / 15s deadlines on status / probe / acquire, and none on upload.
      const hangingTransport = createUpgradeArtifactTransport(
        () => currentKey,
        undefined,
        vi.fn((_url: unknown, init?: RequestInit) => {
          return new Promise<Response>((_resolve, reject) => {
            init?.signal?.addEventListener('abort', () =>
              reject(new DOMException('Aborted', 'AbortError'))
            );
          });
        }) as unknown as typeof fetch
      );

      const statusPromise = hangingTransport.readStatus();
      const statusAssertion = expect(statusPromise).rejects.toMatchObject({ kind: 'network' });
      await vi.advanceTimersByTimeAsync(4_999);
      await vi.advanceTimersByTimeAsync(1);
      await statusAssertion;

      const probePromise = hangingTransport.probe();
      const probeAssertion = expect(probePromise).rejects.toMatchObject({ kind: 'network' });
      await vi.advanceTimersByTimeAsync(10_000);
      await probeAssertion;

      const acquirePromise = hangingTransport.acquire({ source: 'github' });
      const acquireAssertion = expect(acquirePromise).rejects.toMatchObject({ kind: 'network' });
      await vi.advanceTimersByTimeAsync(15_000);
      await acquireAssertion;

      // Combines lifecycle AbortSignal with per-op deadlines and gives upload only the lifecycle signal.
      const lifecycle = new AbortController();
      let uploadSignal: AbortSignal | undefined;
      const lifecycleTransport = createUpgradeArtifactTransport(
        () => currentKey,
        () => lifecycle.signal,
        vi.fn((_url: unknown, init?: RequestInit) => {
          uploadSignal = init?.signal ?? undefined;
          return new Promise<Response>((_resolve, reject) => {
            init?.signal?.addEventListener('abort', () =>
              reject(new DOMException('Aborted', 'AbortError'))
            );
          });
        }) as unknown as typeof fetch
      );
      const uploadPromise = lifecycleTransport.upload(rawGzip);
      const uploadAssertion = expect(uploadPromise).rejects.toMatchObject({
        kind: 'aborted',
        aborted: true,
      });
      await vi.advanceTimersByTimeAsync(30_000);
      expect(uploadSignal).toBe(lifecycle.signal);
      expect(uploadSignal?.aborted).toBe(false);
      lifecycle.abort();
      await uploadAssertion;
      expect(uploadSignal?.aborted).toBe(true);

      // Maps 409 upgrade_artifact_unavailable and 503 upgrade_credential_timeout.
      fetcher.mockResolvedValueOnce(
        Response.json(
          { error: { code: 'upgrade_artifact_unavailable' } },
          { status: 409 }
        )
      );
      let unavailableErr: unknown;
      try {
        await transport.readStatus();
      } catch (err) {
        unavailableErr = err;
      }
      expect(formatArtifactError(unavailableErr, '8.0.55', 'en').code).toBe(
        'upgrade_artifact_unavailable'
      );

      fetcher.mockResolvedValueOnce(
        Response.json(
          {
            code: 'upgrade_credential_timeout',
            error: { code: 'upgrade_credential_timeout' },
          },
          { status: 503 }
        )
      );
      let timeoutErr: unknown;
      try {
        await transport.readStatus();
      } catch (err) {
        timeoutErr = err;
      }
      const mappedTimeout = formatArtifactError(timeoutErr, '8.0.55', 'en');
      expect(mappedTimeout.code).toBe('upgrade_credential_timeout');
      expect(mappedTimeout.text).toContain('taking longer');
      expect(mappedTimeout.text).toContain('Retry shortly');

      for (const [statusCode, serverCode] of [
        [401, 'admin_unauthorized'],
        [403, 'origin_rejected'],
        [405, 'method_not_allowed'],
        [503, 'upgrade_mount_timeout'],
        [503, 'upgrade_legacy_root_missing'],
        [503, 'upgrade_legacy_root_unusable'],
        [503, 'upgrade_source_missing'],
        [503, 'upgrade_source_unreadable'],
        [503, 'upgrade_operation_unreadable'],
        [503, 'upgrade_operation_invalid'],
        [503, 'upgrade_source_symlinked'],
        [503, 'upgrade_source_unstamped'],
      ] as const) {
        fetcher.mockResolvedValueOnce(
          Response.json(
            { code: serverCode, error: { code: serverCode } },
            { status: statusCode }
          )
        );
        let thrown: unknown;
        try {
          await transport.readStatus();
        } catch (err) {
          thrown = err;
        }
        const mapped = formatArtifactError(thrown, '8.0.55', 'en');
        expect(mapped.code).toBe(serverCode);
        expect(mapped.code).not.toBe('unknown');
      }
      // Rejects oversize uploads, invalid CDN prefixes, and empty credentials client-side without touching fetch.
      const countBeforeGuards = fetcher.mock.calls.length;
      const oversize = { size: ARTIFACT_MAX_UPLOAD_BYTES + 1 } as Blob;
      Object.setPrototypeOf(oversize, Blob.prototype);
      await expect(transport.upload(oversize)).rejects.toMatchObject({
        code: 'archive_too_large',
        status: 413,
      });
      await expect(
        transport.acquire({ source: 'cdn', cdnPrefix: 'http://insecure.example.com/' })
      ).rejects.toMatchObject({
        code: 'invalid_request',
        status: 400,
      });
      currentKey = '';
      await expect(transport.readStatus()).rejects.toMatchObject({
        code: 'unauthorized',
        status: 401,
      });
      expect(fetcher.mock.calls.length).toBe(countBeforeGuards);
    } finally {
      vi.useRealTimers();
    }
  });
});
