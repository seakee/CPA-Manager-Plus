import { artifactText, type FreshArtifactTextKey } from '../i18n/freshArtifact';
import type { BootstrapTransportClient } from './bootstrapTransport';
import type { FullWebCredential, PublicApiClient } from './publicApi';

export const ARTIFACT_MAX_UPLOAD_BYTES = 64 * 1024 * 1024; // 64 MiB

const SHA256_ARTIFACT_ID = /^sha256:[a-f0-9]{64}$/;

export type ArtifactOrigin = 'bundled' | 'acquired';
export type ArtifactSource = 'github' | 'cdn' | 'upload';
export type ArtifactPhase =
  | 'downloading'
  | 'verifying'
  | 'installing'
  | 'succeeded'
  | 'failed';

export type ArtifactErrorCode =
  | 'archive_invalid'
  | 'artifact_mismatch'
  | 'source_unreachable'
  | 'download_failed'
  | 'recovery_required'
  | 'unsupported_platform'
  | 'install_failed'
  | 'runtime_unavailable'
  | 'acquisition_in_progress'
  | 'artifact_conflict'
  | 'artifact_already_active'
  | 'archive_too_large'
  | 'invalid_request'
  | 'unauthorized';

export const ARTIFACT_ERROR_CODES: Record<ArtifactErrorCode, true> = {
  archive_invalid: true,
  artifact_mismatch: true,
  source_unreachable: true,
  download_failed: true,
  recovery_required: true,
  unsupported_platform: true,
  install_failed: true,
  runtime_unavailable: true,
  acquisition_in_progress: true,
  artifact_conflict: true,
  artifact_already_active: true,
  archive_too_large: true,
  invalid_request: true,
  unauthorized: true,
};

export function isArtifactErrorCode(value: unknown): value is ArtifactErrorCode {
  return typeof value === 'string' && ARTIFACT_ERROR_CODES[value as ArtifactErrorCode] === true;
}

export interface ArtifactPinned {
  version: string;
  artifactId: string;
}

export interface ArtifactActive {
  artifactId: string;
  origin: ArtifactOrigin;
}

export interface ArtifactOperation {
  id: string;
  source: ArtifactSource;
  phase: ArtifactPhase;
  receivedBytes: number;
  totalBytes: number;
  code: string;
}

export interface ArtifactCDNPreset {
  id: string;
  prefix: string;
  thirdParty: boolean;
}

export interface ArtifactStatus {
  schemaVersion: 1;
  pinned: ArtifactPinned;
  active: ArtifactActive | null;
  required: boolean;
  operation: ArtifactOperation | null;
  cdnPresets: ArtifactCDNPreset[];
}

export interface ArtifactProbe {
  reachable: boolean;
  code: string;
}

export type ArtifactAcquireRequest =
  | { source: 'github' }
  | { source: 'cdn'; cdnPrefix: string };

export const ARTIFACT_ERROR_MAP: Record<
  ArtifactErrorCode,
  { messageKey: FreshArtifactTextKey; remedyKey: FreshArtifactTextKey }
> = {
  archive_invalid: {
    messageKey: 'error_archive_invalid',
    remedyKey: 'remedy_archive',
  },
  artifact_mismatch: {
    messageKey: 'error_artifact_mismatch',
    remedyKey: 'remedy_archive',
  },
  source_unreachable: {
    messageKey: 'error_source_unreachable',
    remedyKey: 'remedy_network',
  },
  download_failed: {
    messageKey: 'error_download_failed',
    remedyKey: 'remedy_network',
  },
  recovery_required: {
    messageKey: 'error_recovery_required',
    remedyKey: 'remedy_recovery',
  },
  unsupported_platform: {
    messageKey: 'error_unsupported_platform',
    remedyKey: 'remedy_unsupported_platform',
  },
  install_failed: {
    messageKey: 'error_install_failed',
    remedyKey: 'remedy_install_failed',
  },
  runtime_unavailable: {
    messageKey: 'error_runtime_unavailable',
    remedyKey: 'remedy_runtime_unavailable',
  },
  acquisition_in_progress: {
    messageKey: 'error_acquisition_in_progress',
    remedyKey: 'remedy_in_progress',
  },
  artifact_conflict: {
    messageKey: 'error_artifact_conflict',
    remedyKey: 'remedy_conflict',
  },
  artifact_already_active: {
    messageKey: 'error_artifact_already_active',
    remedyKey: 'remedy_already_active',
  },
  archive_too_large: {
    messageKey: 'error_archive_too_large',
    remedyKey: 'remedy_too_large',
  },
  invalid_request: {
    messageKey: 'error_invalid_request',
    remedyKey: 'remedy_invalid_request',
  },
  unauthorized: {
    messageKey: 'error_unauthorized',
    remedyKey: 'remedy_unauthorized',
  },
};

export interface MappedArtifactError {
  code: ArtifactErrorCode | 'unknown';
  messageKey: FreshArtifactTextKey;
  remedyKey: FreshArtifactTextKey;
  message: string;
  remedy: string;
  text: string;
}

export function mapArtifactErrorCode(
  code: string | undefined,
  version = '<version>',
  language?: string
): MappedArtifactError {
  const vars = { version: version.trim() || '<version>' };
  if (isArtifactErrorCode(code)) {
    const entry = ARTIFACT_ERROR_MAP[code];
    const message = artifactText(entry.messageKey, vars, language);
    const remedy = artifactText(entry.remedyKey, vars, language);
    return {
      code,
      messageKey: entry.messageKey,
      remedyKey: entry.remedyKey,
      message,
      remedy,
      text: `${message} ${remedy}`,
    };
  }
  const message = artifactText('error_unknown', vars, language);
  const remedy = artifactText('remedy_runtime_unavailable', vars, language);
  return {
    code: 'unknown',
    messageKey: 'error_unknown',
    remedyKey: 'remedy_runtime_unavailable',
    message,
    remedy,
    text: `${message} ${remedy}`,
  };
}

export function extractArtifactErrorCode(error: unknown): ArtifactErrorCode | undefined {
  if (!error || typeof error !== 'object') return undefined;
  const candidate = error as {
    code?: unknown;
    kind?: unknown;
    status?: unknown;
  };
  if (isArtifactErrorCode(candidate.code)) return candidate.code;
  if (
    candidate.kind === 'authentication' ||
    candidate.kind === 'rejected' ||
    candidate.status === 401 ||
    candidate.status === 403
  ) {
    return 'unauthorized';
  }
  if (candidate.status === 413) return 'archive_too_large';
  return undefined;
}

export function formatArtifactError(
  error: unknown,
  version = '<version>',
  language?: string
): MappedArtifactError {
  return mapArtifactErrorCode(extractArtifactErrorCode(error), version, language);
}

export function validCDNPrefix(prefix: string): boolean {
  const trimmed = prefix.trim();
  if (!trimmed) return false;
  try {
    const parsed = new URL(trimmed);
    return (
      parsed.protocol === 'https:' &&
      !!parsed.hostname &&
      !parsed.username &&
      !parsed.password &&
      !parsed.search &&
      !parsed.hash
    );
  } catch {
    return false;
  }
}

export function parseArtifactOperation(value: unknown): ArtifactOperation {
  if (!value || typeof value !== 'object') {
    throw new Error('Invalid artifact operation');
  }
  const record = value as Record<string, unknown>;
  const raw =
    'operation' in record && record.operation && typeof record.operation === 'object'
      ? (record.operation as Record<string, unknown>)
      : record;
  if (typeof raw.id !== 'string' || !raw.id.trim()) {
    throw new Error('Invalid artifact operation id');
  }
  if (raw.source !== 'github' && raw.source !== 'cdn' && raw.source !== 'upload') {
    throw new Error('Invalid artifact operation source');
  }
  if (
    raw.phase !== 'downloading' &&
    raw.phase !== 'verifying' &&
    raw.phase !== 'installing' &&
    raw.phase !== 'succeeded' &&
    raw.phase !== 'failed'
  ) {
    throw new Error('Invalid artifact operation phase');
  }
  const receivedBytes = raw.receivedBytes === undefined ? 0 : raw.receivedBytes;
  const totalBytes = raw.totalBytes === undefined ? 0 : raw.totalBytes;
  if (
    typeof receivedBytes !== 'number' ||
    !Number.isSafeInteger(receivedBytes) ||
    receivedBytes < 0 ||
    typeof totalBytes !== 'number' ||
    !Number.isSafeInteger(totalBytes) ||
    totalBytes < 0
  ) {
    throw new Error('Invalid artifact operation byte counters');
  }
  const code = raw.code === undefined ? '' : raw.code;
  if (typeof code !== 'string') {
    throw new Error('Invalid artifact operation code');
  }
  return {
    id: raw.id,
    source: raw.source,
    phase: raw.phase,
    receivedBytes,
    totalBytes,
    code,
  };
}

export function parseArtifactProbe(value: unknown): ArtifactProbe {
  if (!value || typeof value !== 'object') {
    throw new Error('Invalid artifact probe response');
  }
  const raw = value as Record<string, unknown>;
  if (typeof raw.reachable !== 'boolean') {
    throw new Error('Invalid artifact probe reachable');
  }
  const code = raw.code === undefined ? '' : raw.code;
  if (typeof code !== 'string') {
    throw new Error('Invalid artifact probe code');
  }
  if (raw.reachable && code !== '') {
    throw new Error('Reachable artifact probe must not carry error code');
  }
  if (!raw.reachable && code !== 'source_unreachable') {
    throw new Error('Unreachable artifact probe must carry source_unreachable');
  }
  return {
    reachable: raw.reachable,
    code,
  };
}

export function parseArtifactStatus(value: unknown): ArtifactStatus {
  if (!value || typeof value !== 'object') {
    throw new Error('Invalid artifact status response');
  }
  const raw = value as Record<string, unknown>;
  if (raw.schemaVersion !== 1) {
    throw new Error('Unsupported artifact status schemaVersion');
  }
  if (!raw.pinned || typeof raw.pinned !== 'object') {
    throw new Error('Invalid artifact pinned block');
  }
  const pinnedRaw = raw.pinned as Record<string, unknown>;
  if (
    typeof pinnedRaw.version !== 'string' ||
    !pinnedRaw.version.trim() ||
    typeof pinnedRaw.artifactId !== 'string' ||
    !SHA256_ARTIFACT_ID.test(pinnedRaw.artifactId)
  ) {
    throw new Error('Invalid artifact pinned metadata');
  }
  let active: ArtifactActive | null = null;
  if (raw.active !== null && raw.active !== undefined) {
    if (typeof raw.active !== 'object') {
      throw new Error('Invalid artifact active block');
    }
    const activeRaw = raw.active as Record<string, unknown>;
    if (
      typeof activeRaw.artifactId !== 'string' ||
      !SHA256_ARTIFACT_ID.test(activeRaw.artifactId) ||
      (activeRaw.origin !== 'bundled' && activeRaw.origin !== 'acquired')
    ) {
      throw new Error('Invalid artifact active metadata');
    }
    active = {
      artifactId: activeRaw.artifactId,
      origin: activeRaw.origin,
    };
  }
  if (typeof raw.required !== 'boolean' || raw.required !== (active === null)) {
    throw new Error('Artifact status required invariant violated');
  }
  let operation: ArtifactOperation | null = null;
  if (raw.operation !== null && raw.operation !== undefined) {
    operation = parseArtifactOperation(raw.operation);
  }
  if (!Array.isArray(raw.cdnPresets)) {
    throw new Error('Invalid artifact cdnPresets');
  }
  const cdnPresets: ArtifactCDNPreset[] = raw.cdnPresets.map((item) => {
    if (!item || typeof item !== 'object') {
      throw new Error('Invalid artifact cdnPreset');
    }
    const preset = item as Record<string, unknown>;
    if (
      typeof preset.id !== 'string' ||
      !preset.id.trim() ||
      typeof preset.prefix !== 'string' ||
      !validCDNPrefix(preset.prefix) ||
      typeof preset.thirdParty !== 'boolean'
    ) {
      throw new Error('Invalid artifact cdnPreset fields');
    }
    return {
      id: preset.id,
      prefix: preset.prefix,
      thirdParty: preset.thirdParty,
    };
  });
  return {
    schemaVersion: 1,
    pinned: {
      version: pinnedRaw.version,
      artifactId: pinnedRaw.artifactId,
    },
    active,
    required: raw.required,
    operation,
    cdnPresets,
  };
}

export function isArtifactRecoveryRequired(
  status: ArtifactStatus | null | undefined
): boolean {
  return (
    !!status &&
    status.operation?.phase === 'failed' &&
    status.operation.code === 'recovery_required'
  );
}

export function isArtifactReady(status: ArtifactStatus | null | undefined): boolean {
  return (
    !!status &&
    status.required === false &&
    status.active !== null &&
    !isArtifactRecoveryRequired(status)
  );
}

export interface ArtifactTransport {
  readStatus: () => Promise<ArtifactStatus>;
  probe: () => Promise<ArtifactProbe>;
  acquire: (input: ArtifactAcquireRequest) => Promise<ArtifactOperation>;
  upload: (file: Blob | ArrayBuffer | Uint8Array) => Promise<ArtifactOperation>;
}

export type ArtifactTransportLike =
  | ArtifactTransport
  | BootstrapTransportClient
  | PublicApiClient
  | { client: PublicApiClient; credential: FullWebCredential };

function isDirectArtifactTransport(value: ArtifactTransportLike): value is ArtifactTransport {
  return (
    typeof (value as ArtifactTransport).readStatus === 'function' &&
    typeof (value as ArtifactTransport).probe === 'function' &&
    typeof (value as ArtifactTransport).acquire === 'function' &&
    typeof (value as ArtifactTransport).upload === 'function'
  );
}

function isBootstrapTransport(value: ArtifactTransportLike): value is BootstrapTransportClient {
  return typeof (value as BootstrapTransportClient).request === 'function';
}

function isBoundPublicTransport(
  value: ArtifactTransportLike
): value is { client: PublicApiClient; credential: FullWebCredential } {
  return (
    typeof value === 'object' &&
    value !== null &&
    'client' in value &&
    'credential' in value &&
    typeof value.client?.get === 'function'
  );
}

function requireCredential(credential?: FullWebCredential): FullWebCredential {
  if (!credential || !credential.value) {
    throw { kind: 'authentication', status: 401, code: 'unauthorized' };
  }
  return credential;
}

export async function readArtifactStatus(
  transport: ArtifactTransportLike,
  credential?: FullWebCredential
): Promise<ArtifactStatus> {
  if (isDirectArtifactTransport(transport)) {
    return parseArtifactStatus(await transport.readStatus());
  }
  if (isBootstrapTransport(transport)) {
    return parseArtifactStatus(await transport.request('runtime/artifact'));
  }
  if (isBoundPublicTransport(transport)) {
    return parseArtifactStatus(
      await transport.client.get('runtime/artifact', requireCredential(transport.credential))
    );
  }
  return parseArtifactStatus(await transport.get('runtime/artifact', requireCredential(credential)));
}

export async function probeArtifact(
  transport: ArtifactTransportLike,
  credential?: FullWebCredential
): Promise<ArtifactProbe> {
  if (isDirectArtifactTransport(transport)) {
    return parseArtifactProbe(await transport.probe());
  }
  if (isBootstrapTransport(transport)) {
    return parseArtifactProbe(await transport.request('runtime/artifact/probe', {}));
  }
  if (isBoundPublicTransport(transport)) {
    return parseArtifactProbe(
      await transport.client.post(
        'runtime/artifact/probe',
        {},
        requireCredential(transport.credential)
      )
    );
  }
  return parseArtifactProbe(
    await transport.post('runtime/artifact/probe', {}, requireCredential(credential))
  );
}

export async function acquireArtifact(
  transport: ArtifactTransportLike,
  input: ArtifactAcquireRequest,
  credential?: FullWebCredential
): Promise<ArtifactOperation> {
  const body: ArtifactAcquireRequest =
    input.source === 'github'
      ? { source: 'github' }
      : { source: 'cdn', cdnPrefix: input.cdnPrefix.trim() };
  if (body.source === 'cdn' && !validCDNPrefix(body.cdnPrefix)) {
    throw { kind: 'bad_request', status: 400, code: 'invalid_request' };
  }
  if (isDirectArtifactTransport(transport)) {
    return parseArtifactOperation(await transport.acquire(body));
  }
  if (isBootstrapTransport(transport)) {
    return parseArtifactOperation(await transport.request('runtime/artifact/acquire', body));
  }
  if (isBoundPublicTransport(transport)) {
    return parseArtifactOperation(
      await transport.client.post(
        'runtime/artifact/acquire',
        body,
        requireCredential(transport.credential)
      )
    );
  }
  return parseArtifactOperation(
    await transport.post('runtime/artifact/acquire', body, requireCredential(credential))
  );
}

export async function uploadArtifact(
  transport: ArtifactTransportLike,
  file: Blob | ArrayBuffer | Uint8Array,
  credential?: FullWebCredential
): Promise<ArtifactOperation> {
  const byteLength = file instanceof Blob ? file.size : file.byteLength;
  if (byteLength > ARTIFACT_MAX_UPLOAD_BYTES) {
    throw { kind: 'unavailable', status: 413, code: 'archive_too_large' };
  }
  if (isDirectArtifactTransport(transport)) {
    return parseArtifactOperation(await transport.upload(file));
  }
  const body = file as unknown as BodyInit;
  if (isBootstrapTransport(transport)) {
    if (typeof transport.uploadArtifact === 'function') {
      return parseArtifactOperation(await transport.uploadArtifact(body));
    }
    return parseArtifactOperation(await transport.request('runtime/artifact/upload', body));
  }
  if (isBoundPublicTransport(transport)) {
    if (typeof transport.client.uploadArtifact === 'function') {
      return parseArtifactOperation(
        await transport.client.uploadArtifact(body, requireCredential(transport.credential))
      );
    }
    return parseArtifactOperation(
      await transport.client.put(
        'runtime/artifact/upload',
        body,
        requireCredential(transport.credential)
      )
    );
  }
  if (typeof transport.uploadArtifact === 'function') {
    return parseArtifactOperation(
      await transport.uploadArtifact(body, requireCredential(credential))
    );
  }
  return parseArtifactOperation(
    await transport.put('runtime/artifact/upload', file, requireCredential(credential))
  );
}

export function createPublicArtifactTransport(
  client: PublicApiClient,
  credential: FullWebCredential | (() => string)
): ArtifactTransport {
  const resolveCredential = (): FullWebCredential =>
    typeof credential === 'function'
      ? requireCredential({ kind: 'admin', value: credential() })
      : requireCredential(credential);
  return {
    readStatus: () => readArtifactStatus(client, resolveCredential()),
    probe: () => probeArtifact(client, resolveCredential()),
    acquire: (input) => acquireArtifact(client, input, resolveCredential()),
    upload: (file) => uploadArtifact(client, file, resolveCredential()),
  };
}
