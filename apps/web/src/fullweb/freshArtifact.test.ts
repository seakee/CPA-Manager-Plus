import { describe, expect, it, vi } from 'vitest';
import { artifactText } from '../i18n/freshArtifact';
import {
  ARTIFACT_ERROR_CODES,
  ARTIFACT_ERROR_MAP,
  ARTIFACT_MAX_UPLOAD_BYTES,
  acquireArtifact,
  createPublicArtifactTransport,
  formatArtifactError,
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
    expect(codes).toHaveLength(21);

    expect(ARTIFACT_ERROR_MAP.archive_invalid.remedyKey).toBe('remedy_archive');
    expect(ARTIFACT_ERROR_MAP.artifact_mismatch.remedyKey).toBe('remedy_archive');
    expect(ARTIFACT_ERROR_MAP.source_unreachable.remedyKey).toBe('remedy_network');
    expect(ARTIFACT_ERROR_MAP.download_failed.remedyKey).toBe('remedy_network');
    expect(ARTIFACT_ERROR_MAP.recovery_required.remedyKey).toBe('remedy_recovery');
    expect(ARTIFACT_ERROR_MAP.unsupported_platform.remedyKey).toBe('remedy_unsupported_platform');
    expect(ARTIFACT_ERROR_MAP.upgrade_artifact_unavailable.remedyKey).toBe(
      'remedy_upgrade_artifact_unavailable'
    );
    expect(ARTIFACT_ERROR_MAP.upgrade_credential_timeout.remedyKey).toBe(
      'remedy_upgrade_source_timeout'
    );
    expect(ARTIFACT_ERROR_MAP.upgrade_mount_timeout.remedyKey).toBe(
      'remedy_upgrade_source_timeout'
    );
    expect(ARTIFACT_ERROR_MAP.upgrade_preflight_timeout.remedyKey).toBe(
      'remedy_upgrade_source_timeout'
    );
    expect(ARTIFACT_ERROR_MAP.upgrade_source_symlinked.remedyKey).toBe(
      'remedy_upgrade_source_symlinked'
    );

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
