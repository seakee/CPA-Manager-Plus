import { BOOTSTRAP_ROOT } from './bootstrapTransport';
import {
  ARTIFACT_MAX_UPLOAD_BYTES,
  isArtifactErrorCode,
  parseArtifactOperation,
  parseArtifactProbe,
  parseArtifactStatus,
  validCDNPrefix,
  type ArtifactAcquireRequest,
  type ArtifactOperation,
  type ArtifactProbe,
  type ArtifactStatus,
  type ArtifactTransport,
} from './freshArtifact';
import { readRoutingContext } from './routingContext';

const ARTIFACT_STATUS_TIMEOUT_MS = 5_000;
const ARTIFACT_PROBE_TIMEOUT_MS = 10_000;
const ARTIFACT_ACQUIRE_TIMEOUT_MS = 15_000;

// The canonical continuation prefix owns these reads once routing is committed;
// before that the fixed Bootstrap API does. Neither is discovered from browser
// state beyond the verified routing context the Manager itself served.
export function upgradeAPIBase(): string {
  try {
    const context = readRoutingContext(document);
    if (context) return context.apiBase;
  } catch {
    /* No document: the fixed Bootstrap API owns the read. */
  }
  return `${BOOTSTRAP_ROOT}/api/v2`;
}

export function createUpgradeArtifactTransport(
  credential: () => string,
  lifecycle?: AbortSignal | (() => AbortSignal | undefined),
  transport?: typeof fetch
): ArtifactTransport {
  async function request(
    suffix: '' | 'probe' | 'acquire' | 'upload',
    method: 'GET' | 'POST' | 'PUT',
    timeoutMs: number | undefined,
    body?: string | BodyInit,
    contentType?: string
  ): Promise<unknown> {
    const base = `${upgradeAPIBase()}/installation/upgrade/runtime/artifact`;
    const path = suffix ? `${base}/${suffix}` : base;
    const origin = window.location.origin;
    const target = new URL(path, origin);
    if (
      target.origin !== origin ||
      target.pathname !== path ||
      target.search ||
      target.hash
    ) {
      throw { kind: 'unavailable', status: 0 };
    }
    const lifecycleSignal =
      typeof lifecycle === 'function' ? lifecycle() : lifecycle;
    if (lifecycleSignal?.aborted) {
      throw { kind: 'aborted', aborted: true, status: 0 };
    }
    const key = credential();
    if (!key) {
      throw { kind: 'authentication', status: 401, code: 'unauthorized' };
    }
    const headers: Record<string, string> = {
      Accept: 'application/json',
      Authorization: `Bearer ${key}`,
    };
    if (contentType) {
      headers['Content-Type'] = contentType;
    }
    const controller = timeoutMs !== undefined ? new AbortController() : undefined;
    const onLifecycleAbort = () => controller?.abort();
    if (controller && lifecycleSignal) {
      lifecycleSignal.addEventListener('abort', onLifecycleAbort, { once: true });
    }
    const timer =
      controller && timeoutMs !== undefined
        ? setTimeout(() => controller.abort(), timeoutMs)
        : undefined;
    const requestSignal = controller ? controller.signal : lifecycleSignal;
    const fetcher = transport ?? globalThis.fetch;
    try {
      let response: Response;
      try {
        response = await fetcher(path, {
          method,
          mode: 'same-origin',
          credentials: 'omit',
          redirect: 'error',
          cache: 'no-store',
          headers,
          ...(body !== undefined ? { body } : {}),
          ...(requestSignal ? { signal: requestSignal } : {}),
        });
      } catch {
        if (lifecycleSignal?.aborted) {
          throw { kind: 'aborted', aborted: true, status: 0 };
        }
        throw { kind: 'network', status: 0 };
      }
      if (lifecycleSignal?.aborted) {
        throw { kind: 'aborted', aborted: true, status: 0 };
      }
      if (response.status === 401 || response.status === 403) {
        throw { kind: 'authentication', status: response.status, code: 'unauthorized' };
      }
      if (!response.ok) {
        let code: string | undefined;
        try {
          const payload = (await response.json()) as {
            code?: unknown;
            error?: { code?: unknown };
          };
          const rawCode =
            typeof payload?.code === 'string'
              ? payload.code
              : typeof payload?.error?.code === 'string'
                ? payload.error.code
                : undefined;
          if (isArtifactErrorCode(rawCode)) {
            code = rawCode;
          }
        } catch {
          /* A failure without a JSON envelope keeps its status. */
        }
        throw {
          kind: 'http',
          status: response.status,
          ...(code
            ? { code }
            : response.status === 413
              ? { code: 'archive_too_large' }
              : {}),
        };
      }
      const parsed: unknown = await response.json();
      if (lifecycleSignal?.aborted) {
        throw { kind: 'aborted', aborted: true, status: 0 };
      }
      return parsed;
    } finally {
      clearTimeout(timer);
      lifecycleSignal?.removeEventListener('abort', onLifecycleAbort);
    }
  }

  return {
    readStatus: async (): Promise<ArtifactStatus> =>
      parseArtifactStatus(await request('', 'GET', ARTIFACT_STATUS_TIMEOUT_MS)),
    probe: async (): Promise<ArtifactProbe> =>
      parseArtifactProbe(
        await request(
          'probe',
          'POST',
          ARTIFACT_PROBE_TIMEOUT_MS,
          JSON.stringify({}),
          'application/json'
        )
      ),
    acquire: async (input: ArtifactAcquireRequest): Promise<ArtifactOperation> => {
      const body: ArtifactAcquireRequest =
        input.source === 'github'
          ? { source: 'github' }
          : { source: 'cdn', cdnPrefix: input.cdnPrefix.trim() };
      if (body.source === 'cdn' && !validCDNPrefix(body.cdnPrefix)) {
        throw { kind: 'bad_request', status: 400, code: 'invalid_request' };
      }
      return parseArtifactOperation(
        await request(
          'acquire',
          'POST',
          ARTIFACT_ACQUIRE_TIMEOUT_MS,
          JSON.stringify(body),
          'application/json'
        )
      );
    },
    upload: async (file: Blob | ArrayBuffer | Uint8Array): Promise<ArtifactOperation> => {
      const byteLength = file instanceof Blob ? file.size : file.byteLength;
      if (byteLength > ARTIFACT_MAX_UPLOAD_BYTES) {
        throw { kind: 'unavailable', status: 413, code: 'archive_too_large' };
      }
      return parseArtifactOperation(
        await request(
          'upload',
          'PUT',
          undefined,
          file as unknown as BodyInit,
          'application/gzip'
        )
      );
    },
  };
}
