import { describe, expect, it, vi } from 'vitest';
import { artifactText } from '../i18n/freshArtifact';
import {
  ARTIFACT_ERROR_CODES,
  ARTIFACT_ERROR_MAP,
  ARTIFACT_MAX_UPLOAD_BYTES,
  acquireArtifact,
  createPublicArtifactTransport,
  extractArtifactErrorCode,
  formatArtifactError,
  formatArtifactPinnedMismatch,
  isArtifactErrorCode,
  isArtifactPinnedMismatch,
  isArtifactReady,
  isArtifactRecoveryRequired,
  isNonTransientArtifactReadError,
  mapArtifactErrorCode,
  parseArtifactOperation,
  parseArtifactProbe,
  parseArtifactStatus,
  probeArtifact,
  readArtifactStatus,
  uploadArtifact,
  type ArtifactErrorCode,
} from './freshArtifact';
import type { PublicApiClient } from './publicApi';

const DIGEST = 'sha256:' + 'a'.repeat(64);

function validStatus(overrides: Record<string, unknown> = {}) {
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

describe('freshArtifact parsers, error mapping and transport helpers', () => {
  it('enforces required === (active == null) invariant in parseArtifactStatus', () => {
    const missing = parseArtifactStatus(validStatus());
    expect(missing.required).toBe(true);
    expect(missing.active).toBeNull();

    const active = parseArtifactStatus(
      validStatus({
        active: { artifactId: DIGEST, origin: 'bundled' },
        required: false,
      })
    );
    expect(active.required).toBe(false);
    expect(active.active).toEqual({ artifactId: DIGEST, origin: 'bundled' });

    expect(() =>
      parseArtifactStatus(
        validStatus({
          active: null,
          required: false,
        })
      )
    ).toThrow();

    expect(() =>
      parseArtifactStatus(
        validStatus({
          active: { artifactId: DIGEST, origin: 'acquired' },
          required: true,
        })
      )
    ).toThrow();

    expect(isArtifactReady(missing)).toBe(false);
    expect(isArtifactReady(active)).toBe(true);

    // A historical succeeded operation never overrides required === true
    const requiredWithSucceededOp = parseArtifactStatus(
      validStatus({
        active: null,
        required: true,
        operation: {
          id: 'op-hist',
          source: 'github',
          phase: 'succeeded',
          receivedBytes: 100,
          totalBytes: 100,
          code: '',
        },
      })
    );
    expect(isArtifactReady(requiredWithSucceededOp)).toBe(false);

    // recovery_required wins even when active !== null and required === false
    const activeWithRecoveryRequired = parseArtifactStatus(
      validStatus({
        active: { artifactId: DIGEST, origin: 'bundled' },
        required: false,
        operation: {
          id: 'op-rec',
          source: 'upload',
          phase: 'failed',
          receivedBytes: 100,
          totalBytes: 100,
          code: 'recovery_required',
        },
      })
    );
    expect(isArtifactRecoveryRequired(activeWithRecoveryRequired)).toBe(true);
    expect(isArtifactReady(activeWithRecoveryRequired)).toBe(false);

    // active.artifactId !== pinned.artifactId is never ready and is flagged as a pinned mismatch
    const mismatched = parseArtifactStatus(
      validStatus({
        active: { artifactId: 'sha256:' + 'b'.repeat(64), origin: 'bundled' },
        required: false,
      })
    );
    expect(isArtifactPinnedMismatch(mismatched)).toBe(true);
    expect(isArtifactReady(mismatched)).toBe(false);
    expect(isArtifactPinnedMismatch(active)).toBe(false);
    expect(formatArtifactPinnedMismatch('8.0.55', 'en').code).toBe('pinned_mismatch');
  });

  it('validates operation and probe wire contracts strictly', () => {
    const op = parseArtifactOperation({
      operation: {
        id: 'op-1',
        source: 'github',
        phase: 'downloading',
        receivedBytes: 1024,
        totalBytes: 4096,
        code: '',
      },
    });
    expect(op).toEqual({
      id: 'op-1',
      source: 'github',
      phase: 'downloading',
      receivedBytes: 1024,
      totalBytes: 4096,
      code: '',
    });

    expect(() =>
      parseArtifactOperation({
        id: 'op-1',
        source: 'github',
        phase: 'downloading',
        receivedBytes: -1,
        totalBytes: 4096,
      })
    ).toThrow();

    expect(parseArtifactProbe({ reachable: true, code: '' })).toEqual({
      reachable: true,
      code: '',
    });
    expect(parseArtifactProbe({ reachable: false, code: 'source_unreachable' })).toEqual({
      reachable: false,
      code: 'source_unreachable',
    });
    expect(() => parseArtifactProbe({ reachable: true, code: 'source_unreachable' })).toThrow();
    expect(() => parseArtifactProbe({ reachable: false, code: '' })).toThrow();
  });

  it('maps every artifact error code to its message key and remedy across all four locales', () => {
    const codes = Object.keys(ARTIFACT_ERROR_CODES) as ArtifactErrorCode[];
    expect(codes).toHaveLength(39);

    // Table-driven check of every server code reachable on the Fresh and Upgrade artifact routes.
    const serverArtifactCodes: Array<[ArtifactErrorCode, string]> = [
      ['archive_invalid', 'remedy_archive'],
      ['artifact_mismatch', 'remedy_archive'],
      ['source_unreachable', 'remedy_network'],
      ['download_failed', 'remedy_network'],
      ['recovery_required', 'remedy_recovery'],
      ['unsupported_platform', 'remedy_unsupported_platform'],
      ['install_failed', 'remedy_install_failed'],
      ['runtime_unavailable', 'remedy_runtime_unavailable'],
      ['acquisition_in_progress', 'remedy_in_progress'],
      ['artifact_conflict', 'remedy_conflict'],
      ['artifact_already_active', 'remedy_already_active'],
      ['archive_too_large', 'remedy_too_large'],
      ['invalid_request', 'remedy_invalid_request'],
      ['unauthorized', 'remedy_unauthorized'],
      ['admin_unauthorized', 'remedy_admin_unauthorized'],
      ['origin_rejected', 'remedy_origin_rejected'],
      ['method_not_allowed', 'remedy_invalid_request'],
      ['upgrade_artifact_unavailable', 'remedy_upgrade_artifact_unavailable'],
      ['upgrade_execution_busy', 'remedy_upgrade_execution_busy'],
      ['upgrade_bootstrap_retired', 'remedy_upgrade_bootstrap_retired'],
      ['upgrade_credential_timeout', 'remedy_upgrade_source_timeout'],
      ['upgrade_mount_timeout', 'remedy_upgrade_source_timeout'],
      ['upgrade_preflight_timeout', 'remedy_upgrade_source_timeout'],
      ['upgrade_legacy_root_missing', 'remedy_upgrade_legacy_root_missing'],
      ['upgrade_legacy_root_unusable', 'remedy_upgrade_legacy_root_unusable'],
      ['upgrade_source_missing', 'remedy_upgrade_source_missing'],
      ['upgrade_source_unreadable', 'remedy_upgrade_source_unreadable'],
      ['upgrade_operation_unreadable', 'remedy_upgrade_operation_unreadable'],
      ['upgrade_operation_invalid', 'remedy_upgrade_operation_invalid'],
      ['upgrade_source_symlinked', 'remedy_upgrade_source_symlinked'],
      ['upgrade_source_unstamped', 'remedy_upgrade_source_unstamped'],
      ['upgrade_worker_unavailable', 'remedy_upgrade_worker_unavailable'],
      ['upgrade_worker_peer_refused', 'remedy_upgrade_worker_peer_refused'],
      ['source_ownership_unavailable', 'remedy_source_ownership_unavailable'],
      ['upgrade_source_lock_unavailable', 'remedy_source_ownership_unavailable'],
      ['upgrade_store_locked', 'remedy_upgrade_store_locked'],
      ['upgrade_store_unavailable', 'remedy_upgrade_store_unavailable'],
      ['upgrade_store_schema_outdated', 'remedy_upgrade_store_schema_outdated'],
      ['upgrade_request_invalid', 'remedy_invalid_request'],
    ];
    for (const [serverCode, expectedRemedyKey] of serverArtifactCodes) {
      expect(ARTIFACT_ERROR_MAP[serverCode].remedyKey).toBe(expectedRemedyKey);
      const mapped = mapArtifactErrorCode(serverCode, '8.0.55', 'en');
      expect(mapped.code).toBe(serverCode);
      expect(mapped.code).not.toBe('unknown');
      expect(mapped.remedyKey).toBe(expectedRemedyKey);
    }

    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru']) {
      for (const code of codes) {
        const mapped = mapArtifactErrorCode(code, '8.0.55', locale);
        expect(mapped.code).toBe(code);
        expect(mapped.message.length).toBeGreaterThan(0);
        expect(mapped.remedy.length).toBeGreaterThan(0);
      }
      const archiveError = mapArtifactErrorCode('artifact_mismatch', '8.0.55', locale);
      expect(archiveError.remedy).toContain('CLIProxyAPI_8.0.55_linux_<arch>.tar.gz');
    }

    for (const locale of ['en', 'ru']) {
      expect(artifactText('title', undefined, locale)).not.toMatch(/[\u4e00-\u9fff]/);
      for (const code of codes) {
        const mapped = mapArtifactErrorCode(code, '8.0.55', locale);
        expect(mapped.text).not.toMatch(/[\u4e00-\u9fff]/);
      }
    }

    expect(formatArtifactError({ kind: 'authentication', status: 401 }).code).toBe('unauthorized');
    expect(formatArtifactError({ status: 413 }).code).toBe('archive_too_large');

    const upgradeArtifactEnvelopes: Array<{
      envelope: { kind: string; status: number; code: ArtifactErrorCode };
      expectedCode: ArtifactErrorCode;
      messageKey:
        | 'error_upgrade_artifact_unavailable'
        | 'error_upgrade_execution_busy'
        | 'error_upgrade_bootstrap_retired'
        | 'error_invalid_request'
        | 'error_upgrade_legacy_root_unusable'
        | 'error_upgrade_store_schema_outdated'
        | 'error_source_ownership_unavailable'
        | 'error_upgrade_store_unavailable'
        | 'error_upgrade_worker_peer_refused';
      remedyKey:
        | 'remedy_upgrade_artifact_unavailable'
        | 'remedy_upgrade_execution_busy'
        | 'remedy_upgrade_bootstrap_retired'
        | 'remedy_invalid_request'
        | 'remedy_upgrade_legacy_root_unusable'
        | 'remedy_upgrade_store_schema_outdated'
        | 'remedy_source_ownership_unavailable'
        | 'remedy_upgrade_store_unavailable'
        | 'remedy_upgrade_worker_peer_refused';
      nonTransient: boolean;
    }> = [
      {
        envelope: {
          kind: 'unavailable',
          status: 409,
          code: 'upgrade_artifact_unavailable',
        },
        expectedCode: 'upgrade_artifact_unavailable',
        messageKey: 'error_upgrade_artifact_unavailable',
        remedyKey: 'remedy_upgrade_artifact_unavailable',
        nonTransient: true,
      },
      {
        envelope: {
          kind: 'unavailable',
          status: 409,
          code: 'upgrade_execution_busy',
        },
        expectedCode: 'upgrade_execution_busy',
        messageKey: 'error_upgrade_execution_busy',
        remedyKey: 'remedy_upgrade_execution_busy',
        nonTransient: false,
      },
      {
        envelope: {
          kind: 'unavailable',
          status: 404,
          code: 'upgrade_bootstrap_retired',
        },
        expectedCode: 'upgrade_bootstrap_retired',
        messageKey: 'error_upgrade_bootstrap_retired',
        remedyKey: 'remedy_upgrade_bootstrap_retired',
        nonTransient: true,
      },
      {
        envelope: {
          kind: 'http',
          status: 400,
          code: 'upgrade_request_invalid',
        },
        expectedCode: 'upgrade_request_invalid',
        messageKey: 'error_invalid_request',
        remedyKey: 'remedy_invalid_request',
        nonTransient: true,
      },
      {
        envelope: {
          kind: 'http',
          status: 503,
          code: 'upgrade_legacy_root_unusable',
        },
        expectedCode: 'upgrade_legacy_root_unusable',
        messageKey: 'error_upgrade_legacy_root_unusable',
        remedyKey: 'remedy_upgrade_legacy_root_unusable',
        nonTransient: true,
      },
      {
        envelope: {
          kind: 'http',
          status: 503,
          code: 'upgrade_store_schema_outdated',
        },
        expectedCode: 'upgrade_store_schema_outdated',
        messageKey: 'error_upgrade_store_schema_outdated',
        remedyKey: 'remedy_upgrade_store_schema_outdated',
        nonTransient: true,
      },
      {
        envelope: {
          kind: 'http',
          status: 503,
          code: 'upgrade_source_lock_unavailable',
        },
        expectedCode: 'upgrade_source_lock_unavailable',
        messageKey: 'error_source_ownership_unavailable',
        remedyKey: 'remedy_source_ownership_unavailable',
        nonTransient: false,
      },
      {
        envelope: {
          kind: 'http',
          status: 503,
          code: 'upgrade_store_unavailable',
        },
        expectedCode: 'upgrade_store_unavailable',
        messageKey: 'error_upgrade_store_unavailable',
        remedyKey: 'remedy_upgrade_store_unavailable',
        nonTransient: false,
      },
      {
        envelope: {
          kind: 'authentication',
          status: 403,
          code: 'upgrade_worker_peer_refused',
        },
        expectedCode: 'upgrade_worker_peer_refused',
        messageKey: 'error_upgrade_worker_peer_refused',
        remedyKey: 'remedy_upgrade_worker_peer_refused',
        nonTransient: true,
      },
    ];
    for (const item of upgradeArtifactEnvelopes) {
      expect(extractArtifactErrorCode(item.envelope)).toBe(item.expectedCode);
      expect(isNonTransientArtifactReadError(item.envelope)).toBe(item.nonTransient);
      for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru']) {
        const formatted = formatArtifactError(item.envelope, '8.0.55', locale);
        expect(formatted.code).toBe(item.expectedCode);
        expect(formatted.messageKey).toBe(item.messageKey);
        expect(formatted.remedyKey).toBe(item.remedyKey);
        expect(formatted.message).toBe(artifactText(item.messageKey, { version: '8.0.55' }, locale));
        expect(formatted.remedy).toBe(artifactText(item.remedyKey, { version: '8.0.55' }, locale));
      }
    }

    // Direct nested { error: { code } } (handler.go:writeError) and dual-shape
    // { code, error: { code } } (artifact.go:writeArtifactServiceError) envelopes.
    const nestedCases: Array<{
      input: unknown;
      expectedCode: ArtifactErrorCode;
      nonTransient: boolean;
    }> = [
      {
        input: { error: { code: 'upgrade_store_unavailable' } },
        expectedCode: 'upgrade_store_unavailable',
        nonTransient: false,
      },
      {
        input: { status: 503, error: { code: 'upgrade_operation_invalid' } },
        expectedCode: 'upgrade_operation_invalid',
        nonTransient: true,
      },
      {
        input: { status: 401, error: { code: 'admin_unauthorized' } },
        expectedCode: 'admin_unauthorized',
        nonTransient: true,
      },
      {
        input: { status: 403, error: { code: 'origin_rejected' } },
        expectedCode: 'origin_rejected',
        nonTransient: true,
      },
      {
        input: { status: 403, error: { code: 'upgrade_worker_peer_refused' } },
        expectedCode: 'upgrade_worker_peer_refused',
        nonTransient: true,
      },
      {
        input: {
          code: 'upgrade_legacy_root_unusable',
          error: { code: 'upgrade_legacy_root_unusable' },
        },
        expectedCode: 'upgrade_legacy_root_unusable',
        nonTransient: true,
      },
    ];
    for (const tc of nestedCases) {
      expect(extractArtifactErrorCode(tc.input)).toBe(tc.expectedCode);
      expect(isNonTransientArtifactReadError(tc.input)).toBe(tc.nonTransient);
      const formatted = formatArtifactError(tc.input, '8.0.55', 'en');
      expect(formatted.code).toBe(tc.expectedCode);
      expect(formatted.code).not.toBe('unknown');
    }
  });

  it('recognizes upgrade_source_lock_unavailable consistently with source_ownership_unavailable without fallback', () => {
    expect(isArtifactErrorCode('upgrade_source_lock_unavailable')).toBe(true);
    const lockEnvelope = { kind: 'http', status: 503, code: 'upgrade_source_lock_unavailable' };
    const ownershipEnvelope = { kind: 'http', status: 503, code: 'source_ownership_unavailable' };
    expect(extractArtifactErrorCode(lockEnvelope)).toBe('upgrade_source_lock_unavailable');
    expect(isNonTransientArtifactReadError(lockEnvelope)).toBe(
      isNonTransientArtifactReadError(ownershipEnvelope)
    );
    expect(isNonTransientArtifactReadError(lockEnvelope)).toBe(false);

    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru']) {
      const formatted = formatArtifactError(lockEnvelope, '8.0.55', locale);
      const ownership = formatArtifactError(ownershipEnvelope, '8.0.55', locale);
      expect(formatted.code).toBe('upgrade_source_lock_unavailable');
      expect(formatted.code).not.toBe('upgrade_artifact_unavailable');
      expect(formatted.code).not.toBe('unknown');
      expect(formatted.messageKey).toBe('error_source_ownership_unavailable');
      expect(formatted.remedyKey).toBe('remedy_source_ownership_unavailable');
      expect(formatted.message).toBe(ownership.message);
      expect(formatted.remedy).toBe(ownership.remedy);
    }
  });

  it('executes status, probe, acquire and raw upload helpers with client-side guards', async () => {
    const get = vi.fn().mockResolvedValue(validStatus());
    const post = vi
      .fn()
      .mockResolvedValueOnce({ reachable: true, code: '' })
      .mockResolvedValueOnce({
        operation: {
          id: 'op-github',
          source: 'github',
          phase: 'downloading',
          receivedBytes: 0,
          totalBytes: 100,
          code: '',
        },
      })
      .mockResolvedValueOnce({
        operation: {
          id: 'op-cdn',
          source: 'cdn',
          phase: 'downloading',
          receivedBytes: 0,
          totalBytes: 100,
          code: '',
        },
      });
    const upload = vi.fn().mockResolvedValue({
      operation: {
        id: 'op-upload',
        source: 'upload',
        phase: 'succeeded',
        receivedBytes: 16,
        totalBytes: 16,
        code: '',
      },
    });
    const client: PublicApiClient = {
      get,
      post,
      put: vi.fn(),
      uploadArtifact: upload,
    };
    const transport = createPublicArtifactTransport(client, {
      kind: 'admin',
      value: 'admin-token',
    });

    const status = await readArtifactStatus(transport);
    expect(status.required).toBe(true);
    expect(get).toHaveBeenCalledWith('runtime/artifact', {
      kind: 'admin',
      value: 'admin-token',
    });

    const probe = await probeArtifact(transport);
    expect(probe.reachable).toBe(true);
    expect(post).toHaveBeenNthCalledWith(
      1,
      'runtime/artifact/probe',
      {},
      { kind: 'admin', value: 'admin-token' }
    );

    await acquireArtifact(transport, { source: 'github' });
    expect(post).toHaveBeenNthCalledWith(
      2,
      'runtime/artifact/acquire',
      { source: 'github' },
      { kind: 'admin', value: 'admin-token' }
    );

    await acquireArtifact(transport, {
      source: 'cdn',
      cdnPrefix: ' https://mirror.example.com/ ',
    });
    expect(post).toHaveBeenNthCalledWith(
      3,
      'runtime/artifact/acquire',
      { source: 'cdn', cdnPrefix: 'https://mirror.example.com/' },
      { kind: 'admin', value: 'admin-token' }
    );

    await expect(
      acquireArtifact(transport, { source: 'cdn', cdnPrefix: 'http://insecure.example.com/' })
    ).rejects.toMatchObject({ code: 'invalid_request' });

    const smallFile = new Blob([new Uint8Array(16)], { type: 'application/gzip' });
    const uploaded = await uploadArtifact(transport, smallFile);
    expect(uploaded.phase).toBe('succeeded');
    expect(upload).toHaveBeenCalledWith(smallFile, {
      kind: 'admin',
      value: 'admin-token',
    });

    const oversized = new Uint8Array(ARTIFACT_MAX_UPLOAD_BYTES + 1);
    await expect(uploadArtifact(transport, oversized)).rejects.toMatchObject({
      code: 'archive_too_large',
    });
    expect(upload).toHaveBeenCalledTimes(1);
  });
});

