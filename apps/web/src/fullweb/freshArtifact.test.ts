import { describe, expect, it, vi } from 'vitest';
import { artifactText } from '../i18n/freshArtifact';
import {
  ARTIFACT_ERROR_CODES,
  ARTIFACT_ERROR_MAP,
  ARTIFACT_MAX_UPLOAD_BYTES,
  acquireArtifact,
  createPublicArtifactTransport,
  formatArtifactError,
  formatArtifactPinnedMismatch,
  isArtifactPinnedMismatch,
  isArtifactReady,
  isArtifactRecoveryRequired,
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
    expect(codes).toHaveLength(29);

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

it('tailors the network remedy to the source that actually failed', () => {
  for (const language of ['en', 'zh-CN', 'zh-TW', 'ru']) {
    const generic = mapArtifactErrorCode('download_failed', '8.0.55', language);
    const github = mapArtifactErrorCode('download_failed', '8.0.55', language, 'github');
    const cdn = mapArtifactErrorCode('download_failed', '8.0.55', language, 'cdn');
    // The official release and an unrecorded source keep the remedy that retries
    // the download; a failed CDN prefix names a different one, because "retry the
    // download" would send the operator back to the prefix that just failed.
    expect(github.remedy, language).toBe(generic.remedy);
    expect(cdn.remedy, language).not.toBe(generic.remedy);
    // The failure stays the same failure: only the recovery changes with source.
    expect(cdn.message, language).toBe(generic.message);
    // A broken archive is its own failure regardless of where it arrived from.
    expect(mapArtifactErrorCode('archive_invalid', '8.0.55', language, 'cdn').remedy).toBe(
      mapArtifactErrorCode('archive_invalid', '8.0.55', language).remedy
    );
  }
});
